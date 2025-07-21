package main

import (
	"fmt"
	"sort"
	"strings"
)

// printDetailedResultsToStdout prints the categorized results to the standard output.
// printCategoryToStdout is a helper function to print results for a given category directly to stdout.
func printCategoryToStdout(title string, results []ResourceStatus) {
	if len(results) > 0 {
		fmt.Printf("\n--- %s (%d) ---\n", title, len(results))
		for _, res := range results {
			fmt.Printf("%s: %s\n", res.Category, res.Message)
		}
	}
}

// printDetailedResultsToStdout prints the categorized results to the standard output.
func printDetailedResultsToStdout(results *categorizedResults) {
	fmt.Println("\n--- DETAILED RECONCILIATION RESULTS ---")
	printCategoryToStdout("INFO Results", results.InfoResults)
	printCategoryToStdout("OK Results", results.OkResults)
	printCategoryToStdout("WARNING Results", results.WarningResults)
	printCategoryToStdout("ERROR Results", results.ErrorResults)
	printCategoryToStdout("REGION MISMATCH Results", results.RegionMismatchResults)
	printCategoryToStdout("POTENTIAL IMPORT Results", results.PotentialImportResults)
	printCategoryToStdout("DANGEROUS Results", results.DangerousResults)

	if len(results.RunCommands) > 0 {
		fmt.Printf("\n--- SUGGESTED REMEDIATION COMMANDS (%d) ---\n", len(results.RunCommands))
		for _, cmd := range results.RunCommands {
			fmt.Printf("   %s\n", cmd)
		}
	}
	fmt.Println("-------------------------------------------")
}

// printReportHeader prints the initial header for the reconciliation report.
// This function is still used for console output, but the main report is now generated via renderResultsToString.
func printReportHeader(localStateFilePath string, tfState *TFStateFile, awsRegion string, concurrency int) {
	fmt.Println("--- Terraform State Reconciliation Report ---")
	fmt.Printf("State File: %s (State Version: %d, Terraform Version: %s)\n", localStateFilePath, tfState.Version, tfState.TerraformVersion)
	fmt.Printf("AWS Region: %s\n", awsRegion)
	fmt.Printf("Concurrency: %d\n", concurrency)
	fmt.Printf("-------------------------------------------\n") // Added newline for clarity
	fmt.Println("")
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

// printCategoryToBuilder is a helper function to print results for a given category to a string builder.
func printCategoryToBuilder(builder *strings.Builder, title string, results []ResourceStatus) {
	if len(results) > 0 {
		builder.WriteString(fmt.Sprintf("\n--- %s (%d) ---\n", title, len(results)))
		for _, res := range results {
			builder.WriteString(fmt.Sprintf("%s: %s\n", res.Category, res.Message))
		}
	}
}

// renderResultsToString renders the categorized and sorted results to a string, along with S3 upload instructions if applicable.
func renderResultsToString(
	results *categorizedResults,
	config Config,
	tfStateFile *TFStateFile, // CORRECTED: Added tfStateFile parameter
	stateFileModified bool,
	contentChanged bool,
	originalHash, newHash string,
) string {
	var builder strings.Builder

	builder.WriteString("--- Terraform State Reconciliation Report ---\n")
	builder.WriteString(fmt.Sprintf("State File: %s (State Version: %d, Terraform Version: %s)\n", config.StateFilePath, tfStateFile.Version, tfStateFile.TerraformVersion))
	builder.WriteString(fmt.Sprintf("AWS Region: %s\n", config.AWSRegion))
	builder.WriteString(fmt.Sprintf("Concurrency: %d\n", config.Concurrency))
	builder.WriteString(fmt.Sprintf("Backups Directory: %s\n", config.BackupsDir))
	builder.WriteString("-------------------------------------------\n")
	builder.WriteString("\n")

	// Include hashes in the report
	if originalHash != "" {
		builder.WriteString(fmt.Sprintf("Original State File Hash (SHA256): %s\n", originalHash))
	}
	if newHash != "" {
		builder.WriteString(fmt.Sprintf("Modified State File Hash (SHA256): %s\n", newHash))
	}
	if contentChanged {
		builder.WriteString("State File Content Changed: YES\n")
	} else {
		builder.WriteString("State File Content Changed: NO\n")
	}
	builder.WriteString("-------------------------------------------\n")
	builder.WriteString("\n")

	printCategoryToBuilder(&builder, "INFO Results", results.InfoResults)
	printCategoryToBuilder(&builder, "OK Results", results.OkResults)
	printCategoryToBuilder(&builder, "WARNING Results", results.WarningResults)
	printCategoryToBuilder(&builder, "ERROR Results", results.ErrorResults)
	printCategoryToBuilder(&builder, "REGION MISMATCH Results", results.RegionMismatchResults)
	printCategoryToBuilder(&builder, "POTENTIAL IMPORT Results", results.PotentialImportResults)
	printCategoryToBuilder(&builder, "DANGEROUS Results", results.DangerousResults)

	if len(results.RunCommands) > 0 {
		builder.WriteString(fmt.Sprintf("\n--- RUN THESE COMMANDS (%d) ---\n", len(results.RunCommands)))
		for _, cmd := range results.RunCommands {
			builder.WriteString(fmt.Sprintf("   %s\n", cmd))
		}
	}

	if config.IsS3State {
		builder.WriteString(fmt.Sprintf("\n--- S3 STATE FILE UPLOAD STATUS ---\n")) // Changed instruction to status
		if config.ExecuteCommands {
			if contentChanged {
				builder.WriteString("The updated state file was automatically uploaded to S3 (and backed up) since '--should-execute' was enabled.\n")
			} else {
				builder.WriteString("No 'terraform import' or 'terraform state rm' commands were executed that would modify the state file. No S3 re-upload of latest state was needed.\n")
			}
		} else {
			builder.WriteString(fmt.Sprintf("After you have executed the `terraform import` and `terraform state rm` commands above, "))
			builder.WriteString(fmt.Sprintf("your local state file '%s' will be modified. ", config.StateFilePath))
			builder.WriteString(fmt.Sprintf("To upload the updated state file back to S3 (preserving history with versioning), run:\n"))
			builder.WriteString(fmt.Sprintf("   aws s3 cp %s s3://%s/%s --metadata-directive REPLACE --acl bucket-owner-full-control\n", config.StateFilePath, config.S3Bucket, config.S3Key))
			builder.WriteString(fmt.Sprintf("NOTE: The `--metadata-directive REPLACE` and `--acl bucket-owner-full-control` ensure existing metadata is replaced and proper ownership is maintained. Adjust ACL as per your bucket policy.\n"))
		}
	} else { // Local state, not S3
		if contentChanged {
			builder.WriteString(fmt.Sprintf("\nLocal state file '%s' was modified. A backup of the 'original' state and the 'new' state are in '%s'.\n", config.StateFilePath, config.BackupsDir))
		} else {
			builder.WriteString("\nNo changes to the local state file detected. No new backups created.\n")
		}
	}

	builder.WriteString("\n--- End of Report ---\n")
	builder.WriteString("NOTE: This tool covers only a few resource types. Extend 'processResourceInstance' for full coverage.\n")
	builder.WriteString("\n")

	return builder.String()
}
