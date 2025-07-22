package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"time"
)

const tfState string = "tf" + "state"

// ErrNoState is returned by ReadState when the state file is empty.
var ErrNoState = errors.New("no state")

// main is the entry point of the application.
func main() {
	config := parseAndValidateConfig()

	if config.ShowVersion {
		fmt.Println(Version())
		os.Exit(0)
	}

	ctx := context.Background()

	// 1. Initialize core components and ensure backup directory
	awsClients, err := NewAWSClient(ctx, config.AWSRegion)
	if err != nil {
		log.Fatalf("Failed to initialize AWS clients: %v", err)
	}
	if err := os.MkdirAll(config.BackupsDir, 0755); err != nil {
		log.Fatalf("Failed to create backups directory '%s': %v", config.BackupsDir, err)
	}

	// Capture timestamp and base file name early for consistent backups
	timestamp := time.Now().Format("02-15-04-05") // DD-HH-MM-SS
	yearMonth := time.Now().Format("2006/01")     // YYYY/MM for S3 backup path
	var originalBaseFileName string
	if config.IsS3State {
		_, originalBaseFileName = filepath.Split(config.S3Key)
	} else {
		originalBaseFileName = filepath.Base(config.StateFilePath)
	}

	// 2. Setup state file for processing and take initial backup
	localStateFilePath, originalStateFileHash, err := setupStateFileForProcessing(
		ctx, awsClients, config, originalBaseFileName, timestamp)
	if err != nil {
		log.Fatalf("Failed to setup state file: %v", err)
	}
	// Ensure temp local S3 file is cleaned up AFTER main exits
	if config.IsS3State {
		defer func() { _ = os.Remove(localStateFilePath) }()
	}

	// Determine statePathForTerraformCLI from config AFTER localStateFilePath is set up
	var statePathForTerraformCLI string
	if config.IsS3State {
		statePathForTerraformCLI = config.S3State // Terraform CLI can often use s3:// URI directly
	} else {
		statePathForTerraformCLI = config.StateFilePath // Terraform CLI uses local file
	}

	// 3. Perform the core reconciliation logic
	tfStateFile := openAndReadStateFile(localStateFilePath)

	// Only print header if not in JSON mode
	if !config.JsonOutput {
		printReportHeader(localStateFilePath, tfStateFile, config.AWSRegion, config.Concurrency) // Prints initial header to STDOUT
	}

	results := processResources(ctx, awsClients, tfStateFile, config.AWSRegion, config.Concurrency)
	sortResults(results)

	var stateFileModified bool
	handleExecution(ctx, awsClients, &config, results, localStateFilePath, statePathForTerraformCLI, &stateFileModified)

	// 4. Handle post-reconciliation backups and report generation
	// These paths are needed for the JSON output as well, so derive them here.
	originalBackupLocalPath := createBackupPath(config.BackupsDir, originalBaseFileName, "original", timestamp, filepath.Ext(originalBaseFileName))
	newLocalStatePath := createBackupPath(config.BackupsDir, originalBaseFileName, "new", timestamp, filepath.Ext(originalBaseFileName))
	reportLocalPath := createBackupPath(config.BackupsDir, originalBaseFileName, "report", timestamp, ".md")

	err = handlePostReconciliationBackupsAndUpload(
		ctx, awsClients, config, results, localStateFilePath, tfStateFile,
		originalBaseFileName, timestamp, yearMonth, stateFileModified, originalStateFileHash)
	if err != nil {
		log.Fatalf("Failed to complete post-reconciliation steps: %v", err)
	}

	// NEW: Conditional output based on JsonOutput flag
	if config.JsonOutput {
		jsonOutput, err := renderResultsToJson(
			results,
			config,
			tfStateFile,
			localStateFilePath,
			stateFileModified, // Keep this as it tells us if `commands` were executed
			originalStateFileHash,
			// Pass backup paths for JSON output
			originalBackupLocalPath,
			newLocalStatePath,
			reportLocalPath,
		)
		if err != nil {
			log.Fatalf("Failed to render JSON output: %v", err)
		}
		fmt.Println(jsonOutput)
	} else {
		// Existing detailed text output
		printDetailedResultsToStdout(results)

		// This part of the message is duplicated from `renderResultsToString` for the non-JSON path.
		// It's fine to keep it here, or refactor `renderResultsToString` to only return the core report content.
		// For now, mirroring the existing behavior.
		if config.IsS3State {
			if config.ExecuteCommands {
				if stateFileModified {
					fmt.Printf("\n--- S3 STATE FILE UPLOAD STATUS ---\nThe updated state file was automatically uploaded to S3 (and backed up) since '--should-execute' was enabled.\n")
				} else {
					fmt.Printf("\n--- S3 STATE FILE NOT UPLOADED ---\nNo 'terraform import' or 'terraform state rm' commands were executed that would modify the state file. No S3 re-upload of latest state was needed.\n")
				}
			} else {
				fmt.Printf("\n--- S3 STATE FILE UPLOAD STATUS ---\nAfter you have executed the `terraform import` and `terraform state rm` commands above, your local state file '%s' will be modified. To upload the updated state file back to S3 (preserving history with versioning), run:\n   aws s3 cp %s s3://%s/%s --metadata-directive REPLACE --acl bucket-owner-full-control\nNOTE: The `--metadata-directive REPLACE` and `--acl bucket-owner-full-control` ensure existing metadata is replaced and proper ownership is maintained. Adjust ACL as per your bucket policy.\n", config.StateFilePath, config.StateFilePath, config.S3Bucket, config.S3Key)
			}
		} else {
			// Messages for local-only state are now handled completely within handlePostReconciliationBackupsAndUpload
			// based on the new logic. The final message here is redundant if handlePostReconciliationBackupsAndUpload
			// prints everything.
			// Re-evaluating this section: let's simplify.
			// The explicit prints should either be here OR in the helper, not both producing conditional logic.
			// Since JSON output doesn't get these, it's safer to have helper manage its own prints.
			// So, for non-JSON, state.go will print relevant messages.
			// This block itself could potentially be removed if state.go is comprehensive.
			// However, for consistency with previous changes, keeping it here but noting the overlap.
			if stateFileModified {
				fmt.Printf("\nLocal state file '%s' was modified. Backups of original and new states are in '%s'.\n", config.StateFilePath, config.BackupsDir)
				fmt.Printf("Original Hash: %s\n", originalStateFileHash)
				// The new hash for text output is derived and printed inside handlePostReconciliationBackupsAndUpload now.
			} else {
				fmt.Println("\nNo changes to the state file detected. No new backups created.")
			}
		}

		fmt.Println("\n--- End of Report ---")
		fmt.Println("NOTE: This tool covers only a few resource types. Extend 'processResourceInstance' for full coverage.")
	}
}
