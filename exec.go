package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"os/exec"
	"strings"
)

// handleExecution encapsulates the logic for executing commands and uploading the state file.
func handleExecution(ctx context.Context, awsClients *AWSClient, config *Config, results *categorizedResults, localStateFilePath, statePathForTerraformCLI string, stateFileModified *bool) {
	if config.ExecuteCommands {
		stateWasModifiedByCommands, err := executeCommands(results.RunCommands, localStateFilePath, statePathForTerraformCLI)
		if err != nil {
			log.Fatalf("Failed to execute remediation commands: %v", err)
		}

		// Update the shared stateFileModified flag
		if stateWasModifiedByCommands {
			*stateFileModified = true
		}

		// After executing commands, the local state file *might* have been modified
		// by `terraform import` or `terraform state rm` if they operated on the local file.
		// If using S3 backend, the terraform commands might directly update S3.
		// For safety and consistency, if we started with an S3 state, we re-upload
		// the local copy *after* commands run if they modified the local file.
		// IMPORTANT: If `terraform import/state rm` directly write to S3, this re-upload might be redundant.
		// However, it ensures our local copy (from download) is the authoritative source for re-upload.
		if config.IsS3State {
			if *stateFileModified { // Only upload if it was actually modified
				fmt.Println("\n--- UPLOADING UPDATED STATE FILE TO S3 ---")
				// This upload should only push the *content*. It should not mess with ACLs/metadata.
				err := uploadStateFileToS3(ctx, awsClients, localStateFilePath, config.S3Bucket, config.S3Key)
				if err != nil {
					log.Fatalf("Failed to upload updated state file to S3: %v", err)
				}
				fmt.Println("Upload of updated state file complete.")
			} else {
				fmt.Println("\n--- S3 STATE FILE NOT UPLOADED ---")
				fmt.Println("No 'terraform import' or 'terraform state rm' commands were executed that would modify the state file.")
			}
		}
	}
}

// executeCommands iterates through the provided commands and executes them.
// It returns an error if any command fails.
// It now takes `statePathForTerraformCLI` which can be an `s3://` URI.
func executeCommands(commands []string, localStateFilePath, statePathForTerraformCLI string) (bool, error) {
	if len(commands) == 0 {
		fmt.Println("\nNo remediation commands to execute.")
		return false, nil // No commands, so no modification
	}

	stateAlteringCommandExecuted := false

	fmt.Println("\n--- EXECUTING REMEDIATION COMMANDS ---")
	for _, cmdStr := range commands {
		cmdStr = strings.TrimSpace(cmdStr)
		parts := strings.Fields(cmdStr)
		if len(parts) == 0 {
			continue
		}

		cmdName := parts[0]
		var cmdArgs []string
		if len(parts) > 1 {
			cmdArgs = parts[1:]
		}

		// Dynamically add the -state flag, preferring the S3 URI if applicable.
		// Terraform CLI (0.13+ for state subcommands, earlier for import) can handle s3:// URIs.
		var finalArgs []string
		if cmdName == "terraform" && len(cmdArgs) > 0 && (cmdArgs[0] == "import" || cmdArgs[0] == "state") {
			// Check if -state flag is already present in the command string itself.
			// This covers cases where Terraform might generate commands with -state already.
			foundStateFlag := false
			for _, arg := range cmdArgs {
				if strings.HasPrefix(arg, "-state=") {
					foundStateFlag = true
					break
				}
			}

			if !foundStateFlag {
				finalArgs = append(finalArgs, cmdArgs[0]) // e.g., "import" or "state"
				if cmdArgs[0] == "state" && len(cmdArgs) > 1 {
					// Handle subcommands like "state rm", "state push"
					finalArgs = append(finalArgs, cmdArgs[1])     // e.g., "rm", "push"
					finalArgs = append(finalArgs, cmdArgs[2:]...) // remaining arguments like address
				} else {
					// For "import", or "state <something_else_than_rm/push>"
					finalArgs = append(finalArgs, cmdArgs[1:]...) // remaining arguments
				}
				// Append the state flag with the S3 URI (or local path if not S3)
				finalArgs = append(finalArgs, fmt.Sprintf("-state=%s", statePathForTerraformCLI))
			} else {
				// If -state is already in the generated command, use the args as is.
				// This shouldn't happen if our tool generates them cleanly, but for robustness.
				finalArgs = cmdArgs
			}
			stateAlteringCommandExecuted = true // Mark that a state-altering command was targeted for execution
		} else {
			finalArgs = cmdArgs // For non-terraform commands (e.g., aws s3 cp), use as is.
		}

		fmt.Printf("Executing: %s %s\n", cmdName, strings.Join(finalArgs, " "))

		cmd := exec.Command(cmdName, finalArgs...)
		cmd.Env = os.Environ() // Inherit current environment variables for AWS creds/region

		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr

		if err := cmd.Run(); err != nil {
			return false, fmt.Errorf("command failed: %s %s - %w", cmdName, strings.Join(finalArgs, " "), err)
		}
	}
	fmt.Println("--- REMEDIATION COMMANDS EXECUTION COMPLETE ---")
	return stateAlteringCommandExecuted, nil
}
