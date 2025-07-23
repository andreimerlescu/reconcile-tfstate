package main

import (
	"flag"
	"fmt"
	"log"
	"path/filepath"
	"strings"
)

// parseAndValidateConfig parses command-line flags and validates the input.
func parseAndValidateConfig() Config {
	stateFilePath := flag.String("state", fmt.Sprintf("terraform.%s", tfState), "Path to the Terraform state file (can be S3 URI like s3://bucket/key)")
	awsRegion := flag.String("region", "us-west-2", "AWS Region to check resources against")
	concurrency := flag.Int("concurrency", 10, "Number of concurrent AWS API calls")
	s3State := flag.String("s3-state", "", "Optional: S3 URI of the state file (e.g., s3://bucket/key). If provided, state will be downloaded/uploaded.")
	showVersion := flag.Bool("v", false, "Show version")
	shouldExecute := flag.Bool("should-execute", false, "If true, automatically execute the suggested 'terraform import' and 'terraform state rm' commands.") // New flag
	backupsDir := flag.String("backups-dir", filepath.Join(".", "backups"), "Directory to store local backups and reports.")
	jsonOutput := flag.Bool("json", false, "If true, render results in JSON format to stdout.") // NEW: JSON flag
	terraformWorkingDir := flag.String("tf-dir", ".", "Optional: The directory where 'terraform' commands should be executed. Defaults to the current directory.")

	flag.Parse()

	if *showVersion {
		return Config{ShowVersion: true}
	}

	if *stateFilePath == "" && *s3State == "" {
		log.Fatal("State file path (--state) or S3 state path (--s3-state) is required.")
	}
	if *awsRegion == "" {
		log.Fatal("AWS region is required. Use --region <region>")
	}
	if *concurrency <= 0 {
		log.Fatal("Concurrency must be a positive integer.")
	}

	config := Config{
		StateFilePath:       *stateFilePath,
		AWSRegion:           *awsRegion,
		Concurrency:         *concurrency,
		S3State:             *s3State,
		ExecuteCommands:     *shouldExecute,
		BackupsDir:          *backupsDir,
		JsonOutput:          *jsonOutput,
		TerraformWorkingDir: *terraformWorkingDir,
	}

	if *s3State != "" {
		config.IsS3State = true
		s3Parts := strings.SplitN(strings.TrimPrefix(*s3State, "s3://"), "/", 2)
		if len(s3Parts) != 2 {
			log.Fatalf("Invalid S3 state path format: %s. Expected s3://bucket/key", *s3State)
		}
		config.S3Bucket = s3Parts[0]
		config.S3Key = s3Parts[1]
	}

	return config
}
