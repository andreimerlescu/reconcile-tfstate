package main

import (
	"context"
	"fmt"
	"log"
	"os"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
)

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

// createLocalTempStateFile creates a local temporary file for S3 download.
func createLocalTempStateFile(prefix string) string {
	tempFile, err := os.CreateTemp("", fmt.Sprintf("%s-download-*.%s", prefix, tfState))
	if err != nil {
		log.Fatalf("Failed to create temporary file for S3 state: %v", err)
	}
	localPath := tempFile.Name()
	_ = tempFile.Close() // Close immediately, downloader will open it
	return localPath
}

// downloadStateFileFromS3 downloads the state file from S3 to a local path.
func downloadStateFileFromS3(ctx context.Context, awsClients *AWSClient, localPath, bucket, key string) error {
	file, err := os.Create(localPath)
	if err != nil {
		return fmt.Errorf("failed to create local file for S3 download: %w", err)
	}
	defer func() {
		_ = file.Close()
	}()

	_, err = awsClients.S3Downloader.Download(ctx, file, &s3.GetObjectInput{
		Bucket: aws.String(bucket),
		Key:    aws.String(key),
	})
	if err != nil {
		return fmt.Errorf("failed to download state from S3: %w", err)
	}
	fmt.Println("Download complete.")
	return nil
}
