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

	printReportHeader(localStateFilePath, tfStateFile, config.AWSRegion, config.Concurrency) // Prints initial header to STDOUT
	results := processResources(ctx, awsClients, tfStateFile, config.AWSRegion, config.Concurrency)
	sortResults(results)

	var stateFileModified bool
	handleExecution(ctx, awsClients, &config, results, localStateFilePath, statePathForTerraformCLI, &stateFileModified)

	// NEW: Print detailed results to STDOUT
	printDetailedResultsToStdout(results) // Call a new helper function here

	// 4. Handle post-reconciliation backups and report generation
	err = handlePostReconciliationBackupsAndUpload(
		ctx, awsClients, config, results, localStateFilePath, tfStateFile,
		originalBaseFileName, timestamp, yearMonth, stateFileModified, originalStateFileHash)
	if err != nil {
		log.Fatalf("Failed to complete post-reconciliation steps: %v", err)
	}

	fmt.Println("\n--- End of Report ---")
	fmt.Println("NOTE: This tool covers only a few resource types. Extend 'processResourceInstance' for full coverage.")
}
