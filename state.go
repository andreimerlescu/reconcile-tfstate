package main

import (
	"context"
	"fmt"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"log"
	"os"
)

// handleStateFilePath determines whether to use a local or S3 state file
// and handles downloading if it's an S3 state. It returns the local path to the state file.
func handleStateFilePath(ctx context.Context, awsClients *AWSClient, config *Config) string {
	if !config.IsS3State {
		return config.StateFilePath
	}

	tempFile, err := os.CreateTemp("", fmt.Sprintf("%s-download-*.%s", tfState, tfState))
	if err != nil {
		log.Fatalf("Failed to create temporary file for S3 state: %v", err)
	}
	localStateFilePath := tempFile.Name()
	_ = tempFile.Close() // Close immediately, downloader will open it

	fmt.Printf("Downloading state from s3://%s/%s to %s...\n", config.S3Bucket, config.S3Key, localStateFilePath)
	file, err := os.Create(localStateFilePath)
	if err != nil {
		log.Fatalf("Failed to create local file for S3 download: %v", err)
	}
	defer func() {
		_ = file.Close()
	}()

	_, err = awsClients.S3Downloader.Download(ctx, file, &s3.GetObjectInput{
		Bucket: aws.String(config.S3Bucket),
		Key:    aws.String(config.S3Key),
	})
	if err != nil {
		log.Fatalf("Failed to download state from S3: %v", err)
	}
	fmt.Println("Download complete.")
	return localStateFilePath
}

// openAndReadStateFile opens the specified state file and reads its content.
func openAndReadStateFile(filePath string) *TFStateFile {
	stateFile, err := os.Open(filePath)
	if err != nil {
		log.Fatalf("Failed to open state file '%s': %v", filePath, err)
	}
	defer func() {
		_ = stateFile.Close()
	}()

	tfState, err := Read(stateFile)
	if err != nil {
		log.Fatalf("Failed to parse state file: %v", err)
	}
	return tfState
}
