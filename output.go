package main

import (
	"fmt"
	"sort"
)

// printReportHeader prints the initial header for the reconciliation report.
func printReportHeader(stateFilePath string, tfState *TFStateFile, awsRegion string, concurrency int) {
	fmt.Println("--- Terraform State Reconciliation Report ---")
	fmt.Printf("State File: %s (State Version: %d, Terraform Version: %s)\n", stateFilePath, tfState.Version, tfState.TerraformVersion)
	fmt.Printf("AWS Region: %s\n", awsRegion)
	fmt.Printf("Concurrency: %d\n", concurrency)
	fmt.Println("-------------------------------------------")
}

// sortResults sorts the collected ResourceStatus slices by TerraformAddress.
func sortResults(results *categorizedResults) {
	sort.Slice(results.InfoResults, func(i, j int) bool { return results.InfoResults[i].TerraformAddress < results.InfoResults[j].TerraformAddress })
	sort.Slice(results.OkResults, func(i, j int) bool { return results.OkResults[i].TerraformAddress < results.OkResults[j].TerraformAddress })
	sort.Slice(results.WarningResults, func(i, j int) bool { return results.WarningResults[i].TerraformAddress < results.WarningResults[j].TerraformAddress })
	sort.Slice(results.ErrorResults, func(i, j int) bool { return results.ErrorResults[i].TerraformAddress < results.ErrorResults[j].TerraformAddress })
	sort.Slice(results.PotentialImportResults, func(i, j int) bool {
		return results.PotentialImportResults[i].TerraformAddress < results.PotentialImportResults[j].TerraformAddress
	})
	sort.Slice(results.DangerousResults, func(i, j int) bool {
		return results.DangerousResults[i].TerraformAddress < results.DangerousResults[j].TerraformAddress
	})
	sort.Slice(results.RegionMismatchResults, func(i, j int) bool {
		return results.RegionMismatchResults[i].TerraformAddress < results.RegionMismatchResults[j].TerraformAddress
	})
	sort.Strings(results.RunCommands)
}

// printCategory is a helper function to print results for a given category.
func printCategory(title string, results []ResourceStatus) {
	if len(results) > 0 {
		fmt.Printf("\n--- %s (%d) ---\n", title, len(results))
		for _, res := range results {
			fmt.Printf("%s: %s\n", res.Category, res.Message)
		}
	}
}

// renderResults prints the categorized and sorted results, along with S3 upload instructions if applicable.
func renderResults(results *categorizedResults, config Config) {
	printCategory("INFO Results", results.InfoResults)
	printCategory("OK Results", results.OkResults)
	printCategory("WARNING Results", results.WarningResults)
	printCategory("ERROR Results", results.ErrorResults)
	printCategory("REGION MISMATCH Results", results.RegionMismatchResults)
	printCategory("POTENTIAL IMPORT Results", results.PotentialImportResults)
	printCategory("DANGEROUS Results", results.DangerousResults)

	if len(results.RunCommands) > 0 {
		fmt.Printf("\n--- RUN THESE COMMANDS (%d) ---\n", len(results.RunCommands))
		for _, cmd := range results.RunCommands {
			fmt.Printf("   %s\n", cmd)
		}
	}

	if config.IsS3State {
		fmt.Printf("\n--- S3 STATE FILE UPLOAD INSTRUCTION ---\n")
		fmt.Printf("After you have executed the `terraform import` and `terraform state rm` commands above, ")
		fmt.Printf("your local state file '%s' will be modified. ", config.StateFilePath) // Use original stateFilePath for instructions
		fmt.Printf("To upload the updated state file back to S3 (preserving history with versioning), run:\n")
		fmt.Printf("   aws s3 cp %s s3://%s/%s --metadata-directive REPLACE --acl bucket-owner-full-control\n", config.StateFilePath, config.S3Bucket, config.S3Key)
		fmt.Printf("NOTE: The `--metadata-directive REPLACE` and `--acl bucket-owner-full-control` ensure existing metadata is replaced and proper ownership is maintained. Adjust ACL as per your bucket policy.\n")
	}
}
