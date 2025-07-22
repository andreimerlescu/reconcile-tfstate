package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/feature/s3/manager"
	"github.com/aws/aws-sdk-go-v2/service/s3"
)

// createBackupPath generates a timestamped path for backup files.
// baseDir: the configured backups directory
// originalFileName: the base name of the state file (e.g., "dev.tfstate")
// prefix: "original", "new", "report"
// timestamp: formatted timestamp string (e.g., "02-15-04")
// finalExtension: the desired final extension for the file, e.g., ".tfstate", ".json", ".md", ".sha256"
func createBackupPath(baseDir, originalFileName, prefix, timestamp, finalExtension string) string {
	// Extract just the base name from originalFileName, stripping only .tfstate if present.
	// This ensures we get "dev" from "dev.tfstate", or "myfile" from "myfile.txt".
	base := filepath.Base(originalFileName)
	nameWithoutTfstateExt := strings.TrimSuffix(strings.ToLower(base), ".tfstate")                  // Always strip .tfstate first
	cleanBaseName := strings.TrimSuffix(nameWithoutTfstateExt, filepath.Ext(nameWithoutTfstateExt)) // Strip any other extension after .tfstate is handled (e.g., if original was "file.txt.tfstate")

	// Special handling for originalFileName if it was like "tfstate-download-123.tfstate"
	// We want to extract "tfstate-download-123" as the base name.
	// If the cleanBaseName is empty (e.g., if originalFileName was just ".tfstate"), default to "state".
	if cleanBaseName == "" && strings.HasSuffix(strings.ToLower(base), ".tfstate") {
		cleanBaseName = strings.TrimSuffix(base, ".tfstate")
	} else if cleanBaseName == "" { // e.g. for `tfstate-download-123`
		cleanBaseName = base
	}

	// Format: <baseDir>/YYYY/MM/<timestamp>/<prefix>.<cleanBaseName><finalExtension>
	yearMonth := time.Now().Format("2006/01") // YYYY/MM

	// Create subdirectories if they don't exist
	dir := filepath.Join(baseDir, yearMonth, timestamp)
	if err := os.MkdirAll(dir, 0755); err != nil {
		// Log the warning, but don't stop execution. Fallback to baseDir if creation fails.
		// This makes it more robust if only read access to subdirs is allowed for some reason.
		log.Printf("WARNING: Failed to create backup subdirectory '%s': %v. Storing in base directory.", dir, err)
		dir = baseDir
	}

	// Combine components: prefix.cleanBaseName.finalExtension
	return filepath.Join(dir, fmt.Sprintf("%s.%s%s", prefix, cleanBaseName, finalExtension))
}

// uploadFileToS3 uploads a local file to S3.
func uploadFileToS3(ctx context.Context, awsClients *AWSClient, localPath, bucket, key string) error {
	file, err := os.Open(localPath)
	if err != nil {
		return fmt.Errorf("failed to open local file '%s' for S3 upload: %w", localPath, err)
	}
	defer file.Close()

	uploader := manager.NewUploader(awsClients.S3Client)
	_, err = uploader.Upload(ctx, &s3.PutObjectInput{
		Bucket: aws.String(bucket),
		Key:    aws.String(key),
		Body:   file,
	})
	if err != nil {
		return fmt.Errorf("failed to upload '%s' to s3://%s/%s: %w", localPath, bucket, key, err)
	}
	return nil
}

// uploadStringContentToS3 uploads a string content to S3 (e.g., for hash files)
func uploadStringContentToS3(ctx context.Context, awsClients *AWSClient, content, bucket, key string) error {
	reader := strings.NewReader(content)
	uploader := manager.NewUploader(awsClients.S3Client)
	_, err := uploader.Upload(ctx, &s3.PutObjectInput{
		Bucket: aws.String(bucket),
		Key:    aws.String(key),
		Body:   reader,
	})
	if err != nil {
		return fmt.Errorf("failed to upload string content to s3://%s/%s: %w", bucket, key, err)
	}
	return nil
}

// writeReportToFile writes the given report content to a specified file.
func writeReportToFile(filePath string, content string) error {
	return os.WriteFile(filePath, []byte(content), 0644)
}

// Helper to copy files.
func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return fmt.Errorf("couldn't open source file: %w", err)
	}
	defer in.Close()

	out, err := os.Create(dst)
	if err != nil {
		return fmt.Errorf("couldn't create dest file: %w", err)
	}
	defer out.Close()

	_, err = io.Copy(out, in)
	if err != nil {
		return fmt.Errorf("failed to copy file contents: %w", err)
	}
	return nil
}

// calculateFileSHA256 calculates the SHA256 checksum of a file.
func calculateFileSHA256(filePath string) (string, error) {
	file, err := os.Open(filePath)
	if err != nil {
		return "", fmt.Errorf("failed to open file for SHA256: %w", err)
	}
	defer file.Close()

	hash := sha256.New()
	if _, err := io.Copy(hash, file); err != nil {
		return "", fmt.Errorf("failed to calculate SHA256 for file: %w", err)
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}
