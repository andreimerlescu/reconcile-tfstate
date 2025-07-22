package main

import (
	"context"
	"fmt"
	"log"
	"os"
)

// setupStateFileForProcessing handles downloading/copying the state file and initial local backup/hashing.
// Returns the local path to the state file and its original SHA256 hash.
func setupStateFileForProcessing(
	ctx context.Context,
	awsClients *AWSClient,
	config Config,
	originalBaseFileName string,
	timestamp string,
) (localPath string, originalHash string, err error) {
	var fileToHashPath string // The path of the file we will backup and hash

	if config.IsS3State {
		// Our tool downloads the S3 state to a local temp file for processing.
		localPath = createLocalTempStateFile(tfState)
		fileToHashPath = localPath // The downloaded file is what we'll hash/backup first

		if !config.JsonOutput { // Only print download message in non-JSON mode
			fmt.Printf("Downloading state from s3://%s/%s to %s...\n", config.S3Bucket, config.S3Key, localPath)
		}
		if err := downloadStateFileFromS3(ctx, awsClients, localPath, config.S3Bucket, config.S3Key); err != nil {
			return "", "", fmt.Errorf("failed to download state from S3: %w", err)
		}
	} else {
		localPath = config.StateFilePath
		fileToHashPath = localPath // The existing local file is what we'll hash/backup first
	}

	// Backup original local file
	originalBackupLocalPath := createBackupPath(config.BackupsDir, originalBaseFileName, "original", timestamp, ".tfstate") // Use .tfstate extension explicitly
	if !config.JsonOutput {                                                                                                 // Only print backup message in non-JSON mode
		fmt.Printf("Backing up original state to %s...\n", originalBackupLocalPath)
	}
	if err := copyFile(fileToHashPath, originalBackupLocalPath); err != nil {
		log.Printf("WARNING: Failed to backup original state to local: %v", err)
	} else {
		hash, hashErr := calculateFileSHA256(originalBackupLocalPath)
		if hashErr != nil {
			log.Printf("WARNING: Failed to calculate SHA256 for original backup: %v", hashErr)
		} else {
			if err := os.WriteFile(originalBackupLocalPath+".sha256", []byte(hash), 0644); err != nil {
				log.Printf("WARNING: Failed to write SHA256 for original backup: %v", err)
			}
			originalHash = hash
		}
	}
	return localPath, originalHash, nil
}

// handlePostReconciliationBackupsAndUpload manages post-reconciliation local backups, report generation, and conditional S3 uploads.
func handlePostReconciliationBackupsAndUpload(
	ctx context.Context,
	awsClients *AWSClient,
	config Config,
	results *categorizedResults,
	localStateFilePath string,
	tfStateFile *TFStateFile,
	originalBaseFileName string,
	timestamp string,
	yearMonth string,
	stateFileModified bool, // This is true if `executeCommands` ran and potentially modified.
	originalStateFileHash string,
) error {
	// Calculate newStateFileHash first, as it's needed for both text and JSON outputs
	var newStateFileHash string
	// Always attempt to get the hash of the current local state file (which is the result after commands or no changes)
	calculatedNewStateHash, hashErr := calculateFileSHA256(localStateFilePath)
	if hashErr != nil {
		log.Printf("WARNING: Failed to calculate SHA256 for final local state file: %v", hashErr)
		newStateFileHash = "" // Use empty string if calculation fails
	} else {
		newStateFileHash = calculatedNewStateHash
	}

	// Determine if content actually changed based on hashes
	contentChanged := originalStateFileHash != "" && newStateFileHash != "" && newStateFileHash != originalStateFileHash

	// --- Save Markdown Report (Always) ---
	reportContentMD := renderResultsToString(results, config, tfStateFile, stateFileModified, contentChanged, originalStateFileHash, newStateFileHash)
	reportLocalPathMD := createBackupPath(config.BackupsDir, originalBaseFileName, "report", timestamp, ".md")
	if !config.JsonOutput { // Only print report writing message for MD in non-JSON mode
		fmt.Printf("Writing Markdown report to %s...\n", reportLocalPathMD)
	}
	if err := writeReportToFile(reportLocalPathMD, reportContentMD); err != nil {
		log.Printf("WARNING: Failed to write Markdown report to file: %v", err)
	} else {
		hash, hashErr := calculateFileSHA256(reportLocalPathMD)
		if hashErr != nil {
			log.Printf("WARNING: Failed to calculate SHA256 for Markdown report: %v", hashErr)
		} else {
			if err := os.WriteFile(reportLocalPathMD+".sha256", []byte(hash), 0644); err != nil {
				log.Printf("WARNING: Failed to write SHA256 for Markdown report: %v", err)
			}
		}
	}

	// --- Always create the 'new' state backup locally ---
	// This ensures `newLocalStatePath` is always valid for hashing in `renderResultsToJson`
	newLocalStatePath := createBackupPath(config.BackupsDir, originalBaseFileName, "new", timestamp, ".tfstate") // Use .tfstate extension explicitly
	if _, err := os.Stat(localStateFilePath); err == nil {                                                       // Double check source exists
		if !config.JsonOutput {
			fmt.Printf("Copying final state to new backup path: %s...\n", newLocalStatePath)
		}
		if err := copyFile(localStateFilePath, newLocalStatePath); err != nil {
			log.Printf("WARNING: Failed to copy final state to new backup path: %v", err)
			// Clear path if copy failed, so we don't try to hash a non-existent file later
			newLocalStatePath = ""
		} else {
			// Write the hash for the 'new' local backup
			if newStateFileHash != "" { // Only write if we successfully calculated a hash
				if err := os.WriteFile(newLocalStatePath+".sha256", []byte(newStateFileHash), 0644); err != nil {
					log.Printf("WARNING: Failed to write SHA256 for new backup: %v", err)
				}
			}
		}
	} else {
		log.Printf("WARNING: Skipping creation of 'new' backup as local state file source '%s' was not found: %v", localStateFilePath, err)
		newLocalStatePath = "" // Ensure it's empty if source is missing
	}

	// --- Save JSON Report (Always) ---
	jsonReportContent, err := renderResultsToJson(
		results,
		config,
		tfStateFile,
		localStateFilePath,
		stateFileModified,
		originalStateFileHash,
		createBackupPath(config.BackupsDir, originalBaseFileName, "original", timestamp, ".tfstate"), // Original backup path
		newLocalStatePath,                                                                            // Pass the *actual* path where new.tfstate was/will be saved (could be empty if copy failed)
		reportLocalPathMD,                                                                            // Pass the MD report path for reference in JSON output
	)
	if err != nil {
		log.Printf("ERROR: Failed to render JSON report for backup: %v", err)
	} else {
		reportLocalPathJSON := createBackupPath(config.BackupsDir, originalBaseFileName, "report", timestamp, ".json")
		if !config.JsonOutput { // Only print report writing message for JSON in non-JSON mode
			fmt.Printf("Writing JSON report to %s...\n", reportLocalPathJSON)
		}
		if err := writeReportToFile(reportLocalPathJSON, jsonReportContent); err != nil {
			log.Printf("WARNING: Failed to write JSON report to file: %v", err)
		} else {
			hash, hashErr := calculateFileSHA256(reportLocalPathJSON)
			if hashErr != nil {
				log.Printf("WARNING: Failed to calculate SHA256 for JSON report: %v", hashErr)
			} else {
				if err := os.WriteFile(reportLocalPathJSON+".sha256", []byte(hash), 0644); err != nil {
					log.Printf("WARNING: Failed to write SHA256 for JSON report: %v", err)
				}
			}
		}
	}

	// S3-specific post-processing for backups and final upload
	if config.IsS3State && contentChanged { // Only upload if it's S3 state and content actually changed
		if !config.JsonOutput { // Only print upload status in non-JSON mode
			fmt.Println("\n--- PERFORMING S3 BACKUP AND FINAL UPLOAD ---")
		}

		s3BackupPrefix := fmt.Sprintf("state-backups/%s/%s/", yearMonth, timestamp)

		// Upload original.tfstate + hash to S3 backup path
		originalS3BackupKey := s3BackupPrefix + "original." + originalBaseFileName
		originalS3HashKey := originalS3BackupKey + ".sha256"
		originalBackupLocalPathForS3 := createBackupPath(config.BackupsDir, originalBaseFileName, "original", timestamp, ".tfstate") // Re-derive
		if !config.JsonOutput {
			fmt.Printf("Uploading original local backup to s3://%s/%s...\n", config.S3Bucket, originalS3BackupKey)
		}
		if err := uploadFileToS3(ctx, awsClients, originalBackupLocalPathForS3, config.S3Bucket, originalS3BackupKey); err != nil {
			log.Printf("ERROR: Failed to upload original local backup to S3: %v", err)
		}
		if originalStateFileHash != "" {
			if err := uploadStringContentToS3(ctx, awsClients, originalStateFileHash, config.S3Bucket, originalS3HashKey); err != nil {
				log.Printf("ERROR: Failed to upload original hash to S3: %v", err)
			}
		}

		// Upload new.tfstate + hash to S3 backup path
		// Ensure newLocalStatePath is correctly derived and exists from the unconditional copy above
		if newLocalStatePath != "" { // Only attempt S3 upload if local new.tfstate was successfully created
			newS3BackupKey := s3BackupPrefix + "new." + originalBaseFileName
			newS3HashKey := newS3BackupKey + ".sha256"
			if !config.JsonOutput {
				fmt.Printf("Uploading modified local state to s3://%s/%s...\n", config.S3Bucket, newS3BackupKey)
			}
			if err := uploadFileToS3(ctx, awsClients, newLocalStatePath, config.S3Bucket, newS3BackupKey); err != nil {
				log.Printf("ERROR: Failed to upload modified state to S3: %v", err)
			}
			if newStateFileHash != "" {
				if err := uploadStringContentToS3(ctx, awsClients, newStateFileHash, config.S3Bucket, newS3HashKey); err != nil {
					log.Printf("ERROR: Failed to upload new hash to S3: %v", err)
				}
			}
		} else {
			log.Printf("WARNING: Skipping S3 upload of 'new' state as local 'new' backup file was not found (path was empty).\n")
		}

		// Upload MD report to S3
		reportS3KeyMD := s3BackupPrefix + "report." + originalBaseFileName + ".md"
		reportHashS3KeyMD := reportS3KeyMD + ".sha256"
		if !config.JsonOutput {
			fmt.Printf("Uploading Markdown report to s3://%s/%s...\n", config.S3Bucket, reportS3KeyMD)
		}
		if err := uploadFileToS3(ctx, awsClients, reportLocalPathMD, config.S3Bucket, reportS3KeyMD); err != nil {
			log.Printf("ERROR: Failed to upload Markdown report to S3: %v", err)
		}
		if hash, hashErr := calculateFileSHA256(reportLocalPathMD); hashErr == nil {
			if err := uploadStringContentToS3(ctx, awsClients, hash, config.S3Bucket, reportHashS3KeyMD); err != nil {
				log.Printf("ERROR: Failed to upload Markdown report hash to S3: %v", err)
			}
		}

		// Upload JSON report to S3
		reportS3KeyJSON := s3BackupPrefix + "report." + originalBaseFileName + ".json"
		reportHashS3KeyJSON := reportS3KeyJSON + ".sha256"
		reportLocalPathJSON := createBackupPath(config.BackupsDir, originalBaseFileName, "report", timestamp, ".json") // Re-derive
		if _, err := os.Stat(reportLocalPathJSON); err == nil {                                                        // Check if file exists locally
			if !config.JsonOutput {
				fmt.Printf("Uploading JSON report to s3://%s/%s...\n", config.S3Bucket, reportS3KeyJSON)
			}
			if err := uploadFileToS3(ctx, awsClients, reportLocalPathJSON, config.S3Bucket, reportS3KeyJSON); err != nil {
				log.Printf("ERROR: Failed to upload JSON report to S3: %v", err)
			}
			if hash, hashErr := calculateFileSHA256(reportLocalPathJSON); hashErr == nil {
				if err := uploadStringContentToS3(ctx, awsClients, hash, config.S3Bucket, reportHashS3KeyJSON); err != nil {
					log.Printf("ERROR: Failed to upload JSON report hash to S3: %v", err)
				}
			}
		} else {
			log.Printf("WARNING: Skipping S3 upload of JSON report as local JSON report file was not found (path was empty).\n")
		}

		// Finally, upload the modified local state back to the original S3 location
		if !config.JsonOutput {
			fmt.Printf("Uploading FINAL modified state to original s3://%s/%s...\n", config.S3Bucket, config.S3Key)
		}
		return uploadStateFileToS3(ctx, awsClients, localStateFilePath, config.S3Bucket, config.S3Key) // Returns final error
	} else if !config.IsS3State && contentChanged && !config.JsonOutput { // Local file changed, but not S3 state, AND not JSON output
		// This block implies contentChanged is true (hashes are different)
		fmt.Printf("\nLocal state file '%s' was modified. A backup of the 'original' state and the 'new' state are in '%s'.\n", localStateFilePath, config.BackupsDir)
		fmt.Printf("Original Hash: %s\n", originalStateFileHash)
		fmt.Printf("New Hash:      %s\n", newStateFileHash)
	} else if !contentChanged && !config.JsonOutput { // No content change and not JSON output
		fmt.Println("\nNo changes to the state file detected. No new backups created.")
	}
	return nil
}
