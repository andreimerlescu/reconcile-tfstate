package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"time" // Added for time.Now().Format for S3 paths
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
	stateFileModified bool, // This is true if `executeCommands` ran and potentially modified.
	originalStateFileHash string,
	originalBackupLocalPath string, // Pass actual path from main
	newLocalStatePath string,       // Pass actual path from main
	reportLocalPathMD string,       // Pass actual path from main
	reportLocalPathJSON string,     // Pass actual path from main
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
	if _, err := os.Stat(localStateFilePath); err == nil { // Double check source exists
		if !config.JsonOutput {
			fmt.Printf("Copying final state to new backup path: %s...\n", newLocalStatePath)
		}
		if err := copyFile(localStateFilePath, newLocalStatePath); err != nil {
			log.Printf("WARNING: Failed to copy final state to new backup path: %v", err)
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
	}

	// --- Save JSON Report (Always) ---
	jsonReportContent, err := renderResultsToJson(
		results,
		config,
		tfStateFile,
		localStateFilePath,
		stateFileModified,
		originalStateFileHash,
		originalBackupLocalPath,
		newLocalStatePath,
		reportLocalPathMD,
		reportLocalPathJSON, // Pass the path for the JSON report itself
	)
	if err != nil {
		log.Printf("ERROR: Failed to render JSON report for backup: %v", err)
	} else {
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
	if config.IsS3State && (contentChanged || stateFileModified || (results.ApplicationError != "")) { // Upload if modified, commands run, or app crashed
		if !config.JsonOutput { // Only print upload status in non-JSON mode
			fmt.Println("\n--- PERFORMING S3 BACKUP AND FINAL UPLOAD ---")
		}
		// yearMonth must be derived consistently with createBackupPath
		yearMonth := time.Now().Format("2006/01")
		s3BackupPrefix := fmt.Sprintf("state-backups/%s/%s/", yearMonth, timestamp)

		// Upload original.tfstate + hash to S3 backup path
		originalS3BackupKey := s3BackupPrefix + "original." + originalBaseFileName + ".tfstate"
		originalS3HashKey := originalS3BackupKey + ".sha256"
		if _, err := os.Stat(originalBackupLocalPath); err == nil { // Check if file exists locally
			if !config.JsonOutput {
				fmt.Printf("Uploading original local backup to s3://%s/%s...\n", config.S3Bucket, originalS3BackupKey)
			}
			if err := uploadFileToS3(ctx, awsClients, originalBackupLocalPath, config.S3Bucket, originalS3BackupKey); err != nil {
				log.Printf("ERROR: Failed to upload original local backup to S3: %v", err)
			}
			if originalStateFileHash != "" {
				if err := uploadStringContentToS3(ctx, awsClients, originalStateFileHash, config.S3Bucket, originalS3HashKey); err != nil {
					log.Printf("ERROR: Failed to upload original hash to S3: %v", err)
				}
			}
		} else {
			log.Printf("WARNING: Skipping S3 upload of 'original' state backup as local file '%s' was not found: %v\n", originalBackupLocalPath, err)
		}

		// Upload new.tfstate + hash to S3 backup path
		if _, err := os.Stat(newLocalStatePath); err == nil { // Only attempt S3 upload if local new.tfstate was successfully created
			newS3BackupKey := s3BackupPrefix + "new." + originalBaseFileName + ".tfstate"
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
			log.Printf("WARNING: Skipping S3 upload of 'new' state as local 'new' backup file '%s' was not found: %v\n", newLocalStatePath, err)
		}

		// Upload MD report to S3
		reportS3KeyMD := s3BackupPrefix + "report." + originalBaseFileName + ".txt"
		reportHashS3KeyMD := reportS3KeyMD + ".sha256"
		if _, err := os.Stat(reportLocalPathMD); err == nil { // Check if file exists locally
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
		} else {
			log.Printf("WARNING: Skipping S3 upload of Markdown report as local file '%s' was not found: %v\n", reportLocalPathMD, err)
		}

		// Upload JSON report to S3
		reportS3KeyJSON := s3BackupPrefix + "report." + originalBaseFileName + ".json"
		reportHashS3KeyJSON := reportS3KeyJSON + ".sha256"
		if _, err := os.Stat(reportLocalPathJSON); err == nil { // Check if file exists locally
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
			log.Printf("WARNING: Skipping S3 upload of JSON report as local file '%s' was not found: %v\n", reportLocalPathJSON, err)
		}

		// Finally, upload the modified local state back to the original S3 location
		if !config.JsonOutput {
			fmt.Printf("Uploading FINAL modified state to original s3://%s/%s...\n", config.S3Bucket, config.S3Key)
		}
		uploadErr := uploadStateFileToS3(ctx, awsClients, localStateFilePath, config.S3Bucket, config.S3Key)
		if uploadErr != nil {
			log.Printf("ERROR: Final upload of state file to original S3 location failed: %v", uploadErr)
		}
		return uploadErr // Return the error from the final upload
	} else if !config.IsS3State && (contentChanged || stateFileModified || (results.ApplicationError != "")) && !config.JsonOutput { // Local file changed, but not S3 state, AND not JSON output
		fmt.Printf("\nLocal state file '%s' was modified. A backup of the 'original' state and the 'new' state are in '%s'.\n", localStateFilePath, config.BackupsDir)
		fmt.Printf("Original Hash: %s\n", originalStateFileHash)
		fmt.Printf("New Hash:      %s\n", newStateFileHash)
	} else if !contentChanged && !stateFileModified && (results.ApplicationError == "") && !config.JsonOutput { // No content change and not JSON output
		fmt.Println("\nNo changes to the state file detected. No new backups created.")
	}
	return nil
}
