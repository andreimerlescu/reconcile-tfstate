package main

import (
	"context"
	"fmt"
	"os"
)

// runApplication contains the main logic, allowing `main` to handle panics/errors with defer.
func runApplication(config Config) error {
	ctx := context.Background()

	// 1. Initialize core components and ensure backup directory
	awsClients, err := NewAWSClient(ctx, config.AWSRegion)
	if err != nil {
		return fmt.Errorf("failed to initialize AWS clients: %w", err)
	}
	globalAWSClients = awsClients // Store globally for panic handler

	if err := os.MkdirAll(config.BackupsDir, 0755); err != nil {
		return fmt.Errorf("failed to create backups directory '%s': %w", config.BackupsDir, err)
	}

	// 2. Setup state file for processing and take initial backup
	localStateFilePath, originalStateFileHash, err := setupStateFileForProcessing(
		ctx, awsClients, config, globalOriginalBaseFileName, globalTimestamp)
	if err != nil {
		return fmt.Errorf("failed to setup state file: %w", err)
	}
	globalLocalStateFilePath = localStateFilePath       // Store globally for panic handler
	globalOriginalStateFileHash = originalStateFileHash // Store globally for panic handler

	// Ensure temp local S3 file is cleaned up AFTER main exits (only if S3 state)
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
	globalTfStateFile = tfStateFile // Store globally for panic handler

	// Only print header if not in JSON mode
	if !config.JsonOutput {
		printReportHeader(localStateFilePath, tfStateFile, config.AWSRegion, config.Concurrency, config.BackupsDir)
	}

	results := processResources(ctx, awsClients, tfStateFile, config.AWSRegion, config.Concurrency)
	globalResults = results // Store globally for panic handler
	sortResults(results)

	stateFileModified := false // Initialize here, globalStateFileModified will be updated in handleExecution
	handleExecution(ctx, awsClients, &config, results, localStateFilePath, statePathForTerraformCLI, &stateFileModified)
	globalStateFileModified = stateFileModified // Update global flag after handleExecution

	// 4. Handle post-reconciliation backups and report generation
	originalBackupLocalPath := createBackupPath(config.BackupsDir, globalOriginalBaseFileName, "original", globalTimestamp, ".tfstate")
	newLocalStatePathPlaceholder := createBackupPath(config.BackupsDir, globalOriginalBaseFileName, "new", globalTimestamp, ".tfstate")
	reportLocalPathMD := createBackupPath(config.BackupsDir, globalOriginalBaseFileName, "report", globalTimestamp, ".txt")
	reportLocalPathJSON := createBackupPath(config.BackupsDir, globalOriginalBaseFileName, "report", globalTimestamp, ".json")

	err = handlePostReconciliationBackupsAndUpload(
		ctx, awsClients, config, results, localStateFilePath, tfStateFile,
		globalOriginalBaseFileName, globalTimestamp, globalStateFileModified, globalOriginalStateFileHash,
		originalBackupLocalPath, newLocalStatePathPlaceholder, reportLocalPathMD, reportLocalPathJSON)
	if err != nil {
		return fmt.Errorf("failed to complete post-reconciliation steps: %w", err)
	}

	if config.JsonOutput {
		jsonOutput, err := renderResultsToJson(
			results,
			config,
			tfStateFile,
			localStateFilePath,
			globalStateFileModified,
			globalOriginalStateFileHash,
			originalBackupLocalPath,
			newLocalStatePathPlaceholder,
			reportLocalPathMD,
			reportLocalPathJSON,
		)
		if err != nil {
			return fmt.Errorf("failed to render JSON output: %w", err)
		}
		fmt.Println(jsonOutput)
	} else {
		printDetailedResultsToStdout(results)
		fmt.Println("\n--- End of Report ---")
		fmt.Println("NOTE: This tool covers only a few resource types. Extend 'processResourceInstance' for full coverage.")
	}
	return nil // Success
}
