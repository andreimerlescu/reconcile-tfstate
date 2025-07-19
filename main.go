package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log"
	"os"
	_ "path/filepath"
	"sort"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	_ "github.com/aws/aws-sdk-go-v2/service/s3/types"
)

const tfState string = "tf" + "state"

// ErrNoState is returned by ReadState when the state file is empty.
var ErrNoState = errors.New("no state")

func main() {
	// Updated default values as requested
	stateFilePath := flag.String("state", fmt.Sprintf("terraform.%s", tfState), "Path to the Terraform state file (can be S3 URI like s3://bucket/key)")
	awsRegion := flag.String("region", "us-west-2", "AWS Region to check resources against")
	concurrency := flag.Int("concurrency", 10, "Number of concurrent AWS API calls")
	s3State := flag.String("s3-state", "", "Optional: S3 URI of the state file (e.g., s3://bucket/key). If provided, state will be downloaded/uploaded.")
	showVersion := flag.Bool("v", false, "Show version")
	flag.Parse()

	if *showVersion {
		fmt.Println(Version())
		os.Exit(0)
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

	ctx := context.Background()

	awsClients, err := NewAWSClient(ctx, *awsRegion)
	if err != nil {
		log.Fatalf("Failed to initialize AWS clients: %v", err)
	}

	var localStateFilePath string
	var isS3State bool
	var s3Bucket, s3Key string

	if *s3State != "" {
		isS3State = true
		// Parse S3 URI: s3://bucket/key
		s3Parts := strings.SplitN(strings.TrimPrefix(*s3State, "s3://"), "/", 2)
		if len(s3Parts) != 2 {
			log.Fatalf("Invalid S3 state path format: %s. Expected s3://bucket/key", *s3State)
		}
		s3Bucket = s3Parts[0]
		s3Key = s3Parts[1]

		// Create a temporary file for the downloaded state
		tempFile, err := os.CreateTemp("", fmt.Sprintf("%s-download-*.%s", tfState, tfState))
		if err != nil {
			log.Fatalf("Failed to create temporary file for S3 state: %v", err)
		}
		localStateFilePath = tempFile.Name()
		_ = tempFile.Close() // Close immediately, downloader will open it

		fmt.Printf("Downloading state from s3://%s/%s to %s...\n", s3Bucket, s3Key, localStateFilePath)
		file, err := os.Create(localStateFilePath)
		if err != nil {
			log.Fatalf("Failed to create local file for S3 download: %v", err)
		}
		defer func() {
			_ = os.Remove(localStateFilePath)
		}() // Clean up temp file
		defer func() {
			_ = file.Close()
		}()

		_, err = awsClients.S3Downloader.Download(ctx, file, &s3.GetObjectInput{
			Bucket: aws.String(s3Bucket),
			Key:    aws.String(s3Key),
		})
		if err != nil {
			log.Fatalf("Failed to download state from S3: %v", err)
		}
		fmt.Println("Download complete.")
	} else {
		localStateFilePath = *stateFilePath
	}

	stateFile, err := os.Open(localStateFilePath)
	if err != nil {
		log.Fatalf("Failed to open state file '%s': %v", localStateFilePath, err)
	}
	defer func() {
		_ = stateFile.Close()
	}()

	tfState, err := Read(stateFile)
	if err != nil {
		log.Fatalf("Failed to parse state file: %v", err)
	}

	fmt.Println("--- Terraform State Reconciliation Report ---")
	fmt.Printf("State File: %s (State Version: %d, Terraform Version: %s)\n", localStateFilePath, tfState.Version, tfState.TerraformVersion)
	fmt.Printf("AWS Region: %s\n", *awsRegion)
	fmt.Printf("Concurrency: %d\n", *concurrency)
	fmt.Println("-------------------------------------------")

	resultsChan := make(chan ResourceStatus, *concurrency)
	var wg sync.WaitGroup
	var regionMismatchErrors atomic.Int64 // Atomic counter for region mismatches

	totalResources := 0
	if len(tfState.Resources) > 0 {
		for _, resource := range tfState.Resources {
			for _, instance := range resource.Instances {
				totalResources++
				wg.Add(1)
				go func(res ResourceStateV4, inst InstanceObjectStateV4) {
					defer wg.Done()
					status := processResourceInstance(ctx, awsClients, res, inst, *awsRegion, &regionMismatchErrors)
					resultsChan <- status
				}(resource, instance)
			}
		}
	}

	go func() {
		wg.Wait()
		close(resultsChan)
	}()

	var (
		infoResults            []ResourceStatus
		okResults              []ResourceStatus
		warningResults         []ResourceStatus
		errorResults           []ResourceStatus
		potentialImportResults []ResourceStatus
		dangerousResults       []ResourceStatus
		regionMismatchResults  []ResourceStatus // New category
		runCommands            []string
	)

	for status := range resultsChan {
		switch status.Category {
		case "INFO":
			infoResults = append(infoResults, status)
		case "OK":
			okResults = append(okResults, status)
		case "WARNING":
			warningResults = append(warningResults, status)
		case "ERROR":
			errorResults = append(errorResults, status)
		case "POTENTIAL_IMPORT":
			potentialImportResults = append(potentialImportResults, status)
			if status.Command != "" {
				runCommands = append(runCommands, status.Command)
			}
		case "DANGEROUS":
			dangerousResults = append(dangerousResults, status)
			if status.Command != "" {
				runCommands = append(runCommands, status.Command)
			}
		case "REGION_MISMATCH": // Handle new category
			regionMismatchResults = append(regionMismatchResults, status)
			if status.Command != "" {
				runCommands = append(runCommands, status.Command)
			}
		}
	}

	// Sort results within each category by TerraformAddress for consistent output
	sort.Slice(infoResults, func(i, j int) bool { return infoResults[i].TerraformAddress < infoResults[j].TerraformAddress })
	sort.Slice(okResults, func(i, j int) bool { return okResults[i].TerraformAddress < okResults[j].TerraformAddress })
	sort.Slice(warningResults, func(i, j int) bool { return warningResults[i].TerraformAddress < warningResults[j].TerraformAddress })
	sort.Slice(errorResults, func(i, j int) bool { return errorResults[i].TerraformAddress < errorResults[j].TerraformAddress })
	sort.Slice(potentialImportResults, func(i, j int) bool {
		return potentialImportResults[i].TerraformAddress < potentialImportResults[j].TerraformAddress
	})
	sort.Slice(dangerousResults, func(i, j int) bool {
		return dangerousResults[i].TerraformAddress < dangerousResults[j].TerraformAddress
	})
	sort.Slice(regionMismatchResults, func(i, j int) bool {
		return regionMismatchResults[i].TerraformAddress < regionMismatchResults[j].TerraformAddress
	}) // Sort new category
	sort.Strings(runCommands) // Sort commands alphabetically for consistency

	// Print categorized results in specified order
	printCategory := func(title string, results []ResourceStatus) {
		if len(results) > 0 {
			fmt.Printf("\n--- %s (%d) ---\n", title, len(results))
			for _, res := range results {
				fmt.Printf("%s: %s\n", res.Category, res.Message)
			}
		}
	}

	printCategory("INFO Results", infoResults)
	printCategory("OK Results", okResults)
	printCategory("WARNING Results", warningResults)
	printCategory("ERROR Results", errorResults)
	printCategory("REGION MISMATCH Results", regionMismatchResults) // New section
	printCategory("POTENTIAL IMPORT Results", potentialImportResults)
	printCategory("DANGEROUS Results", dangerousResults)

	// Print RUN_THESE_COMMANDS section last
	if len(runCommands) > 0 {
		fmt.Printf("\n--- RUN THESE COMMANDS (%d) ---\n", len(runCommands))
		for _, cmd := range runCommands {
			fmt.Printf("   %s\n", cmd) // Indent by 3 spaces
		}
	}

	// S3 upload instruction if applicable
	if isS3State {
		fmt.Printf("\n--- S3 STATE FILE UPLOAD INSTRUCTION ---\n")
		fmt.Printf("After you have executed the `terraform import` and `terraform state rm` commands above, ")
		fmt.Printf("your local state file '%s' will be modified. ", localStateFilePath)
		fmt.Printf("To upload the updated state file back to S3 (preserving history with versioning), run:\n")
		fmt.Printf("   aws s3 cp %s s3://%s/%s --metadata-directive REPLACE --acl bucket-owner-full-control\n", localStateFilePath, s3Bucket, s3Key)
		fmt.Printf("NOTE: The `--metadata-directive REPLACE` and `--acl bucket-owner-full-control` ensure existing metadata is replaced and proper ownership is maintained. Adjust ACL as per your bucket policy.\n")
	}

	fmt.Println("\n--- End of Report ---")
	fmt.Println("NOTE: This tool covers only a few resource types. Extend 'processResourceInstance' for full coverage.")
}
