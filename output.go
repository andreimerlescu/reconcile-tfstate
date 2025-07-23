package main

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

// printCategoryToStdout is a helper function to print results for a given category directly to stdout.
func printCategoryToStdout(title string, results []ResourceStatus) {
	if len(results) > 0 {
		fmt.Printf("\n--- %s (%d) ---\n", title, len(results))
		for _, res := range results {
			// CORRECTED: Access res.Category
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

	if len(results.CommandExecutionLogs) > 0 {
		fmt.Printf("\n--- COMMAND EXECUTION LOGS (%d) ---\n", len(results.CommandExecutionLogs))
		for _, log := range results.CommandExecutionLogs {
			fmt.Printf("Command: %s\n", log.Command)
			fmt.Printf("Exit Code: %d\n", log.ExitCode)
			if log.Error != "" {
				fmt.Printf("Error: %s\n", log.Error)
			}
			if log.Stdout != "" {
				fmt.Printf("Stdout:\n%s\n", log.Stdout)
			}
			if log.Stderr != "" {
				fmt.Printf("Stderr:\n%s\n", log.Stderr)
			}
			fmt.Println("---")
		}
	}

	if results.ApplicationError != "" {
		fmt.Printf("\n--- APPLICATION ERROR ---\n%s\n", results.ApplicationError)
	}

	fmt.Println("-------------------------------------------")
}

// printReportHeader prints the initial header for the reconciliation report.
func printReportHeader(localStateFilePath string, tfState *TFStateFile, awsRegion string, concurrency int, backupsDir string) {
	fmt.Println("--- Terraform State Reconciliation Report ---")
	fmt.Printf("State File: %s (State Version: %d, Terraform Version: %s)\n", localStateFilePath, tfState.Version, tfState.TerraformVersion)
	fmt.Printf("AWS Region: %s\n", awsRegion)
	fmt.Printf("Concurrency: %d\n", concurrency)
	fmt.Printf("Backups Directory: %s\n", backupsDir) // Added backups directory
	fmt.Printf("-------------------------------------------\n")
	fmt.Println("")
}

// sortResults sorts the collected ResourceStatus slices by TerraformAddress.
func sortResults(results *categorizedResults) {
	sort.Slice(results.InfoResults, func(i, j int) bool {
		return results.InfoResults[i].TerraformAddress < results.InfoResults[j].TerraformAddress
	})
	sort.Slice(results.OkResults, func(i, j int) bool {
		return results.OkResults[i].TerraformAddress < results.OkResults[j].TerraformAddress
	})
	sort.Slice(results.WarningResults, func(i, j int) bool {
		return results.WarningResults[i].TerraformAddress < results.WarningResults[j].TerraformAddress
	})
	sort.Slice(results.ErrorResults, func(i, j int) bool {
		return results.ErrorResults[i].TerraformAddress < results.ErrorResults[j].TerraformAddress
	})
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
	// Sort command execution logs by command string for consistent output
	sort.Slice(results.CommandExecutionLogs, func(i, j int) bool {
		// CORRECTED: Typo fixed from .C to .Command
		return results.CommandExecutionLogs[i].Command < results.CommandExecutionLogs[j].Command
	})
}

// printCategoryToBuilder is a helper function to print results for a given category to a string builder.
// This is used for Markdown report generation.
func printCategoryToBuilder(builder *strings.Builder, title string, results []ResourceStatus) {
	if len(results) > 0 {
		builder.WriteString(fmt.Sprintf("\n--- %s (%d) ---\n", title, len(results)))
		for _, res := range results {
			// CORRECTED: Access res.Category
			builder.WriteString(fmt.Sprintf("%s: %s\n", res.Category, res.Message))
		}
	}
}

// renderResultsToString renders the categorized and sorted results to a string, along with S3 upload instructions if applicable.
func renderResultsToString(
	results *categorizedResults,
	config Config,
	tfStateFile *TFStateFile,
	stateFileModified bool,
	contentChanged bool,
	originalHash, newHash string,
) string {
	var builder strings.Builder

	builder.WriteString("--- Terraform State Reconciliation Report ---\n")
	// Use config.S3State if available, otherwise fallback to config.StateFilePath for the report header
	stateIdentifier := config.StateFilePath
	if config.IsS3State {
		stateIdentifier = config.S3State
	}
	builder.WriteString(fmt.Sprintf("State File: %s (State Version: %d, Terraform Version: %s)\n", stateIdentifier, tfStateFile.Version, tfStateFile.TerraformVersion))
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
		builder.WriteString(fmt.Sprintf("\n--- SUGGESTED REMEDIATION COMMANDS (%d) ---\n", len(results.RunCommands)))
		for _, cmd := range results.RunCommands {
			builder.WriteString(fmt.Sprintf("   %s\n", cmd))
		}
	}

	if len(results.CommandExecutionLogs) > 0 {
		builder.WriteString(fmt.Sprintf("\n--- COMMAND EXECUTION LOGS (%d) ---\n", len(results.CommandExecutionLogs)))
		for _, log := range results.CommandExecutionLogs {
			builder.WriteString(fmt.Sprintf("Command: %s\n", log.Command))
			builder.WriteString(fmt.Sprintf("Exit Code: %d\n", log.ExitCode))
			if log.Error != "" {
				builder.WriteString(fmt.Sprintf("Error: %s\n", log.Error))
			}
			if log.Stdout != "" {
				builder.WriteString(fmt.Sprintf("Stdout:\n%s\n", log.Stdout))
			}
			if log.Stderr != "" {
				builder.WriteString(fmt.Sprintf("Stderr:\n%s\n", log.Stderr))
			}
			builder.WriteString("---\n")
		}
	}

	if results.ApplicationError != "" {
		builder.WriteString(fmt.Sprintf("\n--- APPLICATION ERROR ---\n%s\n", results.ApplicationError))
	}

	return builder.String()
}

// convertResourceStatusToJSONItem converts a slice of ResourceStatus to JSONResultItem.
func convertResourceStatusToJSONItem(statuses []ResourceStatus) []JSONResultItem {
	items := make([]JSONResultItem, len(statuses))
	for i, s := range statuses {
		items[i] = JSONResultItem{
			Kind:     s.Kind,
			Resource: s.TerraformAddress,
			TFID:     s.StateID,
			AWSID:    s.LiveID,
			Command:  s.Command,
			Stdout:   s.Stdout, // Correctly populate
			Stderr:   s.Stderr, // Correctly populate
		}
	}
	return items
}

// renderResultsToJson renders the categorized and sorted results to a JSON string.
func renderResultsToJson(
	results *categorizedResults,
	config Config,
	tfStateFile *TFStateFile,
	localStateFilePath string,
	stateFileModified bool, // This is true if `executeCommands` ran and potentially modified.
	originalStateFileHash string,
	originalBackupLocalPath string,
	newLocalStatePath string,
	reportLocalPathMD string,   // Renamed to clearly indicate it's the MD report path
	reportLocalPathJSON string, // Added JSON report path
) (string, error) {

	// Always calculate newStateFileHash if localStateFilePath is available.
	// It will be the same as originalStateFileHash if no modifications occurred.
	var finalStateChecksum string
	calculatedNewStateHash, hashErr := calculateFileSHA256(localStateFilePath)
	if hashErr != nil {
		fmt.Printf("WARNING: Failed to calculate SHA256 for final local state file: %v\n", hashErr)
		finalStateChecksum = originalStateFileHash // Fallback to original if calculation fails
	} else {
		finalStateChecksum = calculatedNewStateHash
	}

	// Determine paths for JSON output
	jsonBackupPaths := JSONBackupPaths{
		OriginalPath:   originalBackupLocalPath,
		NewPath:        newLocalStatePath,
		ReportPath:     reportLocalPathMD,
		JsonReportPath: reportLocalPathJSON,
	}

	// Get checksums for backup files (these would have been created by handlePostReconciliationBackupsAndUpload)
	if originalBackupLocalPath != "" {
		hash, err := calculateFileSHA256(originalBackupLocalPath)
		if err == nil {
			jsonBackupPaths.OriginalChecksum = hash
		}
	}
	if newLocalStatePath != "" { // Check if the new backup file exists before trying to hash it
		hash, err := calculateFileSHA256(newLocalStatePath)
		if err == nil {
			jsonBackupPaths.NewChecksum = hash
		}
	}
	if reportLocalPathMD != "" { // Use reportLocalPathMD to hash the MD file
		hash, err := calculateFileSHA256(reportLocalPathMD)
		if err == nil {
			jsonBackupPaths.ReportChecksum = hash
		}
	}
	if reportLocalPathJSON != "" { // Hash the JSON report itself
		hash, err := calculateFileSHA256(reportLocalPathJSON)
		if err == nil {
			jsonBackupPaths.JsonReportChecksum = hash
		}
	}

	// Correctly set the 'state' field based on whether it's an S3 state or local
	stateIdentifier := config.StateFilePath
	if config.IsS3State {
		stateIdentifier = config.S3State
	}

	jsonOutput := JSONOutput{
		State:          stateIdentifier,
		StateChecksum:  finalStateChecksum,
		Region:         config.AWSRegion,
		LocalStateFile: localStateFilePath,
		TFVersion:      tfStateFile.TerraformVersion,
		StateVersion:   tfStateFile.Version,
		Concurrency:    config.Concurrency,
		Backup:         jsonBackupPaths,
		Commands:       results.RunCommands,
		ExecutionLogs:  results.CommandExecutionLogs,
		Results: JSONResults{
			InfoResults:            convertResourceStatusToJSONItem(results.InfoResults),
			OkResults:              convertResourceStatusToJSONItem(results.OkResults),
			PotentialImportResults: convertResourceStatusToJSONItem(results.PotentialImportResults),
			RegionMismatchResults:  convertResourceStatusToJSONItem(results.RegionMismatchResults),
			WarningResults:         convertResourceStatusToJSONItem(results.WarningResults),
			ErrorResults:           convertResourceStatusToJSONItem(results.ErrorResults),
			DangerousResults:       convertResourceStatusToJSONItem(results.DangerousResults),
		},
		ApplicationError: results.ApplicationError,
	}

	jsonData, err := json.MarshalIndent(jsonOutput, "", "\t")
	if err != nil {
		return "", fmt.Errorf("failed to marshal JSON output: %w", err)
	}

	return string(jsonData), nil
}
