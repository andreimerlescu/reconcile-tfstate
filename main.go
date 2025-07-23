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

// globalConfig and globalAWSClients are used by the panic handler if main exits prematurely.
// This is not ideal, but necessary for clean access to configuration and clients in a panic/recover scenario
// without passing them around explicitly or using global state more broadly.
var globalConfig Config
var globalAWSClients *AWSClient
var globalResults *categorizedResults // This will now have ApplicationError
var globalLocalStateFilePath string
var globalTfStateFile *TFStateFile
var globalOriginalBaseFileName string
var globalTimestamp string
var globalStateFileModified bool
var globalOriginalStateFileHash string

// main is the entry point of the application.
func main() {
	// 1. Parse config first, as it's needed for error reporting and setup
	config := parseAndValidateConfig()
	globalConfig = config // Store globally for panic handler

	if config.ShowVersion {
		fmt.Println(Version())
		os.Exit(0)
	}

	// Initialize these here as well for global access
	globalResults = &categorizedResults{}              // Ensure this is initialized before potentially being used by panic handler
	globalTimestamp = time.Now().Format("02-15-04-05") // DD-HH-MM-SS
	if config.IsS3State {
		_, globalOriginalBaseFileName = filepath.Split(config.S3Key)
	} else {
		globalOriginalBaseFileName = filepath.Base(config.StateFilePath)
	}

	// Set up the deferred function to handle panics and ensure S3 upload on failure
	defer func() {
		if r := recover(); r != nil {
			err := fmt.Errorf("application crashed: %v", r)
			log.Printf("FATAL ERROR: %v", err)

			// Add the application error to the global results object
			// This needs to be done *before* calling handlePostReconciliationBackupsAndUpload
			// and assumes globalResults has been initialized.
			if globalResults != nil {
				globalResults.ApplicationError = err.Error()
			} else {
				// Fallback if globalResults wasn't initialized for some reason
				globalResults = &categorizedResults{ApplicationError: err.Error()}
			}

			// Try to upload whatever state/reports we have
			if globalConfig.IsS3State {
				originalBackupLocalPath := createBackupPath(globalConfig.BackupsDir, globalOriginalBaseFileName, "original", globalTimestamp, ".tfstate")
				newLocalStatePathPlaceholder := createBackupPath(globalConfig.BackupsDir, globalOriginalBaseFileName, "new", globalTimestamp, ".tfstate")
				reportLocalPathMD := createBackupPath(globalConfig.BackupsDir, globalOriginalBaseFileName, "report", globalTimestamp, ".txt")
				reportLocalPathJSON := createBackupPath(globalConfig.BackupsDir, globalOriginalBaseFileName, "report", globalTimestamp, ".json")

				// Create a dummy TFStateFile if it wasn't populated due to early error
				if globalTfStateFile == nil {
					globalTfStateFile = &TFStateFile{
						Version:          0, // Indicate unknown version
						TerraformVersion: "unknown",
						Serial:           0,
						Lineage:          "unknown",
						RootOutputs:      make(map[string]OutputStateV4),
						Resources:        []ResourceStateV4{},
					}
				}

				log.Println("Attempting to upload available backups and reports to S3 after crash...")
				uploadErr := handlePostReconciliationBackupsAndUpload(
					context.Background(), globalAWSClients, globalConfig, globalResults,
					globalLocalStateFilePath, globalTfStateFile, globalOriginalBaseFileName, globalTimestamp,
					globalStateFileModified, globalOriginalStateFileHash,
					originalBackupLocalPath, newLocalStatePathPlaceholder, reportLocalPathMD, reportLocalPathJSON)
				if uploadErr != nil {
					log.Printf("ERROR: Failed to complete S3 upload during crash recovery: %v", uploadErr)
				} else {
					log.Println("Successfully uploaded available backups and reports to S3.")
				}
			} else { // Local only mode, just ensure reports are written
				log.Println("Application crashed in local-only mode. Reports should be available locally.")
			}
			os.Exit(1) // Exit with an error code after recovery/cleanup
		}
	}()

	// Run the main application logic in a separate function
	if appErr := runApplication(config); appErr != nil {
		// If runApplication returns an error, log it and then panic to trigger the defer
		// The panic value will be the error itself
		panic(appErr)
	}
}
