package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"
)

const tfState string = "tf" + "state"

// ErrNoState is returned by ReadState when the state file is empty.
var ErrNoState = errors.New("no state")

func main() {
	config := parseAndValidateConfig()

	if config.ShowVersion {
		fmt.Println(Version())
		os.Exit(0)
	}

	ctx := context.Background()

	awsClients, err := NewAWSClient(ctx, config.AWSRegion)
	if err != nil {
		log.Fatalf("Failed to initialize AWS clients: %v", err)
	}

	localStateFilePath := handleStateFilePath(ctx, awsClients, &config)
	defer func() {
		if config.IsS3State {
			_ = os.Remove(localStateFilePath)
		}
	}()

	tfStateFile := openAndReadStateFile(localStateFilePath)

	printReportHeader(localStateFilePath, tfStateFile, config.AWSRegion, config.Concurrency)

	results := processResources(ctx, awsClients, tfStateFile, config.AWSRegion, config.Concurrency)

	sortResults(results)

	renderResults(results, config)

	fmt.Println("\n--- End of Report ---")
	fmt.Println("NOTE: This tool covers only a few resource types. Extend 'processResourceInstance' for full coverage.")
}
