package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"path/filepath"
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

		fmt.Printf("Downloading state from s3://%s/%s to %s...\n", config.S3Bucket, config.S3Key, localPath)
		if err := downloadStateFileFromS3(ctx, awsClients, localPath, config.S3Bucket, config.S3Key); err != nil {
			return "", "", fmt.Errorf("failed to download state from S3: %w", err)
		}
	} else {
		localPath = config.StateFilePath
		fileToHashPath = localPath // The existing local file is what we'll hash/backup first
	}

	// Backup original local file
	originalBackupLocalPath := createBackupPath(config.BackupsDir, originalBaseFileName, "original", timestamp, filepath.Ext(originalBaseFileName))
	fmt.Printf("Backing up original state to %s...\n", originalBackupLocalPath)
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
func handlePostReconciliationBackupsAndUpload( // tfStateFile added here
	ctx context.Context,
	awsClients *AWSClient,
	config Config,
	results *categorizedResults,
	localStateFilePath string, // The potentially modified local state file
	tfStateFile *TFStateFile,  // ADDED: The parsed TFStateFile
	originalBaseFileName string,
	timestamp string,
	yearMonth string,
	stateFileModified bool,
	originalStateFileHash string,
) error {
	// Check if the local state file contents changed
	var newStateFileHash string
	var changed bool
	if stateFileModified { // Only calculate if commands were executed
		var hashErr error
		newStateFileHash, hashErr = calculateFileSHA256(localStateFilePath)
		if hashErr != nil {
			log.Printf("WARNING: Failed to calculate SHA256 for modified state file: %v", hashErr)
		} else {
			if originalStateFileHash != "" && newStateFileHash != originalStateFileHash {
				changed = true
			}
		}
	}

	// Render and save report
	reportContent := renderResultsToString(results, config, tfStateFile, stateFileModified, changed, originalStateFileHash, newStateFileHash) // Pass tfStateFile here
	reportLocalPath := createBackupPath(config.BackupsDir, originalBaseFileName, "report", timestamp, ".md")
	fmt.Printf("Writing report to %s...\n", reportLocalPath)
	if err := writeReportToFile(reportLocalPath, reportContent); err != nil {
		log.Printf("WARNING: Failed to write report to file: %v", err)
	} else {
		hash, hashErr := calculateFileSHA256(reportLocalPath)
		if hashErr != nil {
			log.Printf("WARNING: Failed to calculate SHA256 for report: %v", hashErr)
		} else {
			if err := os.WriteFile(reportLocalPath+".sha256", []byte(hash), 0644); err != nil {
				log.Printf("WARNING: Failed to write SHA256 for report: %v", err)
			}
		}
	}

	// S3-specific post-processing for backups and final upload
	if config.IsS3State && changed {
		fmt.Println("\n--- PERFORMING S3 BACKUP AND FINAL UPLOAD ---")

		s3BackupPrefix := fmt.Sprintf("state-backups/%s/%s/", yearMonth, timestamp)

		// Upload original.tfstate + hash to S3 backup path
		originalS3BackupKey := s3BackupPrefix + "original." + originalBaseFileName
		originalS3HashKey := originalS3BackupKey + ".sha256"
		originalBackupLocalPath := createBackupPath(config.BackupsDir, originalBaseFileName, "original", timestamp, filepath.Ext(originalBaseFileName)) // Need to re-derive for upload
		fmt.Printf("Uploading original local backup to s3://%s/%s...\n", config.S3Bucket, originalS3BackupKey)
		if err := uploadFileToS3(ctx, awsClients, originalBackupLocalPath, config.S3Bucket, originalS3BackupKey); err != nil {
			log.Printf("ERROR: Failed to upload original local backup to S3: %v", err)
		}
		if originalStateFileHash != "" {
			if err := uploadStringContentToS3(ctx, awsClients, originalStateFileHash, config.S3Bucket, originalS3HashKey); err != nil {
				log.Printf("ERROR: Failed to upload original hash to S3: %v", err)
			}
		}

		// Upload new.tfstate + hash to S3 backup path
		newLocalStatePath := createBackupPath(config.BackupsDir, originalBaseFileName, "new", timestamp, filepath.Ext(originalBaseFileName))
		// Ensure the new local file is present for upload, copy it from localStateFilePath (which is already modified)
		if err := copyFile(localStateFilePath, newLocalStatePath); err != nil {
			log.Printf("WARNING: Failed to copy modified state to new backup path for S3 upload: %v", err)
		} else {
			newS3BackupKey := s3BackupPrefix + "new." + originalBaseFileName
			newS3HashKey := newS3BackupKey + ".sha256"
			fmt.Printf("Uploading modified local state to s3://%s/%s...\n", config.S3Bucket, newS3BackupKey)
			if err := uploadFileToS3(ctx, awsClients, newLocalStatePath, config.S3Bucket, newS3BackupKey); err != nil {
				log.Printf("ERROR: Failed to upload modified state to S3: %v", err)
			}
			if newStateFileHash != "" {
				if err := uploadStringContentToS3(ctx, awsClients, newStateFileHash, config.S3Bucket, newS3HashKey); err != nil {
					log.Printf("ERROR: Failed to upload new hash to S3: %v", err)
				}
			}
		}

		// Upload report.md + hash to S3 backup path
		reportS3Key := s3BackupPrefix + "report." + originalBaseFileName + ".md"
		reportHashS3Key := reportS3Key + ".sha256"
		fmt.Printf("Uploading report to s3://%s/%s...\n", config.S3Bucket, reportS3Key)
		if err := uploadFileToS3(ctx, awsClients, reportLocalPath, config.S3Bucket, reportS3Key); err != nil {
			log.Printf("ERROR: Failed to upload report to S3: %v", err)
		}
		// Calculate report hash again as it might have been generated inline
		if hash, hashErr := calculateFileSHA256(reportLocalPath); hashErr == nil {
			if err := uploadStringContentToS3(ctx, awsClients, hash, config.S3Bucket, reportHashS3Key); err != nil {
				log.Printf("ERROR: Failed to upload report hash to S3: %v", err)
			}
		}

		// Finally, upload the modified local state back to the original S3 location
		fmt.Printf("Uploading FINAL modified state to original s3://%s/%s...\n", config.S3Bucket, config.S3Key)
		return uploadStateFileToS3(ctx, awsClients, localStateFilePath, config.S3Bucket, config.S3Key) // Returns final error
	} else if changed { // Local file changed, but not S3 state
		fmt.Printf("\nLocal state file '%s' was modified. A backup of the 'original' state and the 'new' state are in '%s'.\n", localStateFilePath, config.BackupsDir)
		fmt.Printf("Original Hash: %s\n", originalStateFileHash)
		fmt.Printf("New Hash:      %s\n", newStateFileHash)
	} else { // No change
		fmt.Println("\nNo changes to the state file detected. No new backups created.")
	}
	return nil
}

// Helper to get statePathForTerraformCLI based on config
func statePathForTerraformCLI(config Config) string {
	if config.IsS3State {
		return config.S3State
	}
	return config.StateFilePath
}
