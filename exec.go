package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"os/exec"
	"strings"
)

// handleExecution encapsulates the logic for executing commands and uploading the state file.
func handleExecution(ctx context.Context, awsClients *AWSClient, config *Config, results *categorizedResults, localStateFilePath, statePathForTerraformCLI string, stateFileModified *bool) {
	if config.ExecuteCommands {
		// Pass relevant config fields instead of the whole config object to executeCommands
		stateWasModifiedByCommands, commandExecutionLogs, err := executeCommands(
			results.RunCommands,
			statePathForTerraformCLI,
			config.TerraformWorkingDir,
			config.JsonOutput, // Pass JsonOutput here
		)

		// Store command execution logs regardless of success or failure of commands
		results.CommandExecutionLogs = commandExecutionLogs

		if err != nil {
			log.Printf("ERROR: One or more remediation commands failed: %v", err)
			*stateFileModified = stateWasModifiedByCommands
			return // Exit this function but allow main to continue
		}

		// Update the shared stateFileModified flag
		*stateFileModified = stateWasModifiedByCommands

		if config.IsS3State {
			if *stateFileModified {
				if !config.JsonOutput {
					fmt.Println("\n--- UPLOADING UPDATED STATE FILE TO S3 ---")
				}
				err := uploadStateFileToS3(ctx, awsClients, localStateFilePath, config.S3Bucket, config.S3Key)
				if err != nil {
					log.Printf("ERROR: Failed to upload updated state file to S3: %v", err)
					return // Exit this function but allow main to continue
				}
				if !config.JsonOutput {
					fmt.Println("Upload of updated state file complete.")
				}
			} else {
				if !config.JsonOutput {
					fmt.Println("\n--- S3 STATE FILE NOT UPLOADED ---")
					fmt.Println("No 'terraform import' or 'terraform state rm' commands were executed that would modify the state file.")
				}
			}
		}
	}
	return
}

// executeCommands iterates through the provided commands and executes them.
// It returns a boolean indicating if any state-altering command was targeted,
// a slice of CommandExecutionLog detailing each command's outcome,
// and an error if any command failed.
func executeCommands(commands []string, statePathForTerraformCLI, terraformWorkingDir string, jsonOutput bool) (bool, []CommandExecutionLog, error) { // Added jsonOutput
	if len(commands) == 0 {
		if !jsonOutput { // Use passed jsonOutput
			fmt.Println("\nNo remediation commands to execute.")
		}
		return false, []CommandExecutionLog{}, nil
	}

	stateAlteringCommandExecuted := false
	var allCommandLogs []CommandExecutionLog
	var firstError error

	if !jsonOutput { // Use passed jsonOutput
		fmt.Println("\n--- EXECUTING REMEDIATION COMMANDS ---")
	}

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

		var finalArgs []string
		// isTerraformStateCommand := false // REMOVED: Declared and not used

		if cmdName == "terraform" && len(cmdArgs) > 0 {
			subcommand := cmdArgs[0]
			if subcommand == "import" {
				// isTerraformStateCommand = true // REMOVED
				stateAlteringCommandExecuted = true // Mark as state-altering
				if len(cmdArgs) < 3 {
					cmdLog := CommandExecutionLog{
						Command:  cmdStr,
						Error:    "malformed terraform import command: expects ADDR and ID",
						ExitCode: 1,
					}
					allCommandLogs = append(allCommandLogs, cmdLog)
					if firstError == nil {
						firstError = fmt.Errorf(cmdLog.Error)
					}
					continue // Skip execution of this malformed command
				}
				addr := cmdArgs[1]
				id := cmdArgs[2]

				foundStateFlag := false
				for _, arg := range cmdArgs {
					if strings.HasPrefix(arg, "-state=") {
						foundStateFlag = true
						break
					}
				}

				finalArgs = append(finalArgs, subcommand) // "import"
				if !foundStateFlag {
					finalArgs = append(finalArgs, fmt.Sprintf("-state=%s", statePathForTerraformCLI))
				}
				finalArgs = append(finalArgs, addr, id)
				finalArgs = append(finalArgs, cmdArgs[3:]...) // Append any other args that might exist after ADDR ID

			} else if subcommand == "state" {
				// isTerraformStateCommand = true // REMOVED
				stateAlteringCommandExecuted = true // Mark as state-altering
				if len(cmdArgs) < 2 {               // Expect at least "state", subcommand (e.g., "rm")
					cmdLog := CommandExecutionLog{
						Command:  cmdStr,
						Error:    "malformed terraform state command: missing subcommand",
						ExitCode: 1,
					}
					allCommandLogs = append(allCommandLogs, cmdLog)
					if firstError == nil {
						firstError = fmt.Errorf(cmdLog.Error)
					}
					continue // Skip execution
				}

				foundStateFlag := false
				for _, arg := range cmdArgs {
					if strings.HasPrefix(arg, "-state=") {
						foundStateFlag = true
						break
					}
				}

				finalArgs = append(finalArgs, subcommand) // "state"
				finalArgs = append(finalArgs, cmdArgs[1]) // e.g., "rm"
				if !foundStateFlag {
					finalArgs = append(finalArgs, fmt.Sprintf("-state=%s", statePathForTerraformCLI))
				}
				finalArgs = append(finalArgs, cmdArgs[2:]...) // remaining arguments like ADDR

			} else {
				finalArgs = cmdArgs
			}
		} else {
			finalArgs = cmdArgs
		}

		if !jsonOutput { // Use passed jsonOutput
			fmt.Printf("Executing: %s %s\n", cmdName, strings.Join(finalArgs, " "))
		}

		cmd := exec.Command(cmdName, finalArgs...)
		cmd.Env = os.Environ()
		cmd.Dir = terraformWorkingDir // Set the working directory for the command

		var stdoutBuf, stderrBuf bytes.Buffer
		cmd.Stdout = &stdoutBuf
		cmd.Stderr = &stderrBuf

		cmdLog := CommandExecutionLog{
			Command:  strings.Join(append([]string{cmdName}, finalArgs...), " "),
			ExitCode: 0, // Default to 0, updated on error
		}

		err := cmd.Run()
		cmdLog.Stdout = stdoutBuf.String()
		cmdLog.Stderr = stderrBuf.String()

		if err != nil {
			var exitError *exec.ExitError
			if errors.As(err, &exitError) {
				cmdLog.ExitCode = exitError.ExitCode()
			}
			cmdLog.Error = err.Error()
			if firstError == nil {
				firstError = fmt.Errorf("command '%s' failed: %w", cmdLog.Command, err)
			}
		}
		allCommandLogs = append(allCommandLogs, cmdLog)
	}

	if !jsonOutput { // Use passed jsonOutput
		fmt.Println("--- REMEDIATION COMMANDS EXECUTION COMPLETE ---")
	}
	return stateAlteringCommandExecuted, allCommandLogs, firstError
}
