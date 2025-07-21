package main

import (
	"context"
	"fmt"
	"os"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/feature/s3/manager"
	"github.com/aws/aws-sdk-go-v2/service/s3"
)

// uploadStateFileToS3 uploads the state file to S3.
// It performs a simple PutObject, relying on the bucket's default ACLs and versioning settings.
func uploadStateFileToS3(ctx context.Context, awsClients *AWSClient, filePath, bucket, key string) error {
	file, err := os.Open(filePath)
	if err != nil {
		return fmt.Errorf("failed to open local file for S3 upload: %w", err)
	}
	defer func() {
		_ = file.Close()
	}()

	uploader := manager.NewUploader(awsClients.S3Client) // Use the existing S3Client

	_, err = uploader.Upload(ctx, &s3.PutObjectInput{
		Bucket: aws.String(bucket),
		Key:    aws.String(key),
		Body:   file,
		// Removed explicit ACL and MetadataDirective to mimic `aws s3 cp` default behavior
		// and respect existing bucket configurations (like default ACLs, versioning, object locking).
	})
	if err != nil {
		return fmt.Errorf("failed to upload state to S3: %w", err)
	}
	return nil
}
