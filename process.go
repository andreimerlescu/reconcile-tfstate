package main

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
)

// processResources concurrently processes each resource instance in the Terraform state file
// and returns categorized results.
func processResources(ctx context.Context, awsClients *AWSClient, tfState *TFStateFile, awsRegion string, concurrency int) *categorizedResults {
	resultsChan := make(chan ResourceStatus, concurrency)
	var wg sync.WaitGroup
	var regionMismatchErrors atomic.Int64

	if len(tfState.Resources) > 0 {
		for _, resource := range tfState.Resources {
			for _, instance := range resource.Instances {
				wg.Add(1)
				go func(res ResourceStateV4, inst InstanceObjectStateV4) {
					defer wg.Done()
					status := processResourceInstance(ctx, awsClients, res, inst, awsRegion, &regionMismatchErrors)
					resultsChan <- status
				}(resource, instance)
			}
		}
	}

	go func() {
		wg.Wait()
		close(resultsChan)
	}()

	results := &categorizedResults{}
	for status := range resultsChan {
		switch status.Category {
		case "INFO":
			results.InfoResults = append(results.InfoResults, status)
		case "OK":
			results.OkResults = append(results.OkResults, status)
		case "WARNING":
			results.WarningResults = append(results.WarningResults, status)
		case "ERROR":
			results.ErrorResults = append(results.ErrorResults, status)
		case "POTENTIAL_IMPORT":
			results.PotentialImportResults = append(results.PotentialImportResults, status)
			if status.Command != "" {
				results.RunCommands = append(results.RunCommands, status.Command)
			}
		case "DANGEROUS":
			results.DangerousResults = append(results.DangerousResults, status)
			if status.Command != "" {
				results.RunCommands = append(results.RunCommands, status.Command)
			}
		case "REGION_MISMATCH":
			results.RegionMismatchResults = append(results.RegionMismatchResults, status)
			if status.Command != "" {
				results.RunCommands = append(results.RunCommands, status.Command)
			}
		}
	}
	return results
}

// processResourceInstance checks a single Terraform resource instance against AWS
// It now accepts the ResourceStateV4 and InstanceObjectStateV4 from the copied types.
func processResourceInstance(ctx context.Context, clients *AWSClient, resource ResourceStateV4, instance InstanceObjectStateV4, currentFlagRegion string, regionMismatchCount *atomic.Int64) ResourceStatus {
	tfAddress := fmt.Sprintf("%s.%s", resource.Type, resource.Name)
	if resource.Module != "" {
		tfAddress = fmt.Sprintf("%s.%s", resource.Module, tfAddress)
	}
	// For instances with IndexKey (e.g., count, for_each), append it to the address
	if instance.IndexKey != nil {
		switch v := instance.IndexKey.(type) {
		case string:
			tfAddress = fmt.Sprintf("%s[\"%s\"]", tfAddress, v)
		case float64: // JSON numbers unmarshal to float64 by default
			tfAddress = fmt.Sprintf("%s[%d]", tfAddress, int(v))
		default:
			tfAddress = fmt.Sprintf("%s[%v]", tfAddress, v) // Fallback for other types
		}
	}

	var attributes map[string]interface{}
	// AttributesRaw is json.RawMessage, need to unmarshal it
	if len(instance.AttributesRaw) > 0 {
		if err := json.Unmarshal(instance.AttributesRaw, &attributes); err != nil {
			return ResourceStatus{
				TerraformAddress: tfAddress,
				Error:            fmt.Errorf("failed to unmarshal resource attributes for %s: %w", tfAddress, err),
				Category:         "ERROR",
				Message:          fmt.Sprintf("Failed to unmarshal attributes for %s: %v", tfAddress, err),
			}
		}
	} else if len(instance.AttributesFlat) > 0 {
		// Fallback for older flatmap attributes, though less common in v4+
		attributes = make(map[string]interface{})
		for k, v := range instance.AttributesFlat {
			attributes[k] = v
		}
	}

	stateID, _ := attributes["id"].(string) // Get ID from attributes map

	status := ResourceStatus{TerraformAddress: tfAddress, StateID: stateID}

	var liveID string
	var exists bool
	var err error

	// Common ARN attribute for region check
	var arnInState string
	if val, ok := attributes["arn"].(string); ok {
		arnInState = val
	} else if val, ok := attributes["load_balancer_arn"].(string); ok { // For listeners
		arnInState = val
	} else if val, ok := attributes["target_group_arn"].(string); ok { // For listener rules
		arnInState = val
	} else if val, ok := attributes["rule_arn"].(string); ok { // For listener rules
		arnInState = val
	}

	stateRegionFromARN := extractRegionFromARN(arnInState)

	switch resource.Type {
	case "aws_s3_bucket":
		if bucketName, ok := attributes["bucket"].(string); ok && bucketName != "" {
			liveID, exists, err = clients.verifyS3Bucket(ctx, bucketName)
		} else {
			err = fmt.Errorf("could not find 'bucket' attribute for aws_s3_bucket")
		}
	case "aws_cloudwatch_log_group":
		if logGroupName, ok := attributes["name"].(string); ok && logGroupName != "" {
			liveID, exists, err = clients.verifyCloudWatchLogGroup(ctx, logGroupName)
		} else {
			err = fmt.Errorf("could not find 'name' attribute for aws_cloudwatch_log_group")
		}
	case "aws_key_pair":
		if keyName, ok := attributes["key_name"].(string); ok && keyName != "" {
			liveID, exists, err = clients.verifyKeyPair(ctx, keyName)
		} else {
			err = fmt.Errorf("could not find 'key_name' attribute for aws_key_pair")
		}
	case "aws_security_group":
		sgID, _ := attributes["id"].(string)
		sgName, _ := attributes["name"].(string)
		if sgID != "" || sgName != "" {
			liveID, exists, err = clients.verifySecurityGroup(ctx, sgID, sgName)
		} else {
			err = fmt.Errorf("could not find 'id' or 'name' attribute for aws_security_group")
		}
	case "aws_route53_zone":
		zoneID, _ := attributes["zone_id"].(string)
		zoneName, _ := attributes["name"].(string)
		if zoneID != "" || zoneName != "" {
			liveID, exists, err = clients.verifyRoute53Zone(ctx, zoneID, zoneName)
		} else {
			err = fmt.Errorf("could not find 'id' or 'name' attribute for aws_route53_zone")
		}
	case "aws_lb":
		lbARN, _ := attributes["arn"].(string)
		lbName, _ := attributes["name"].(string)
		if lbARN != "" || lbName != "" {
			liveID, exists, err = clients.verifyLoadBalancer(ctx, lbARN, lbName, currentFlagRegion)
		} else {
			err = fmt.Errorf("could not find 'arn' or 'name' attribute for aws_lb")
		}
	case "aws_lb_listener":
		listenerARN, _ := attributes["arn"].(string)
		lbARN, _ := attributes["load_balancer_arn"].(string)
		if listenerARN != "" || lbARN != "" {
			liveID, exists, err = clients.verifyListener(ctx, listenerARN, lbARN, currentFlagRegion)
		} else {
			err = fmt.Errorf("could not find 'arn' or 'load_balancer_arn' attribute for aws_lb_listener")
		}
	case "aws_lb_target_group":
		tgARN, _ := attributes["arn"].(string)
		tgName, _ := attributes["name"].(string)
		if tgARN != "" || tgName != "" {
			liveID, exists, err = clients.verifyTargetGroup(ctx, tgARN, tgName, currentFlagRegion)
		} else {
			err = fmt.Errorf("could not find 'arn' or 'name' attribute for aws_lb_target_group")
		}
	case "aws_lb_listener_rule":
		ruleARN, _ := attributes["arn"].(string)
		listenerARN, _ := attributes["listener_arn"].(string)
		if ruleARN != "" || listenerARN != "" {
			liveID, exists, err = clients.verifyListenerRule(ctx, ruleARN, listenerARN, currentFlagRegion)
		} else {
			err = fmt.Errorf("could not find 'arn' or 'listener_arn' attribute for aws_lb_listener_rule")
		}
	case "aws_caller_identity", "aws_iam_policy_document", "archive_file", "local_file", "random_password":
		status.Category = "INFO"
		status.Message = fmt.Sprintf("Data/Local resource '%s'. No external verification needed.", tfAddress)
		return status
	default:
		status.Category = "WARNING"
		status.Message = fmt.Sprintf("Resource type '%s' not supported by this checker. Manual verification needed.", resource.Type)
		return status
	}

	status.LiveID = liveID
	status.ExistsInAWS = exists
	status.Error = err

	if err != nil {
		// Check for region mismatch specific error
		if strings.Contains(err.Error(), "region mismatch:") {
			regionMismatchCount.Add(1)
			status.Category = "REGION_MISMATCH"
			status.Message = fmt.Sprintf("%s (state file claims in '%s') not found in '%s'.", tfAddress, stateRegionFromARN, currentFlagRegion)
			status.Command = fmt.Sprintf("terraform state rm %s", tfAddress) // Suggest removal
		} else {
			status.Category = "ERROR"
			status.Message = fmt.Sprintf("Failed to verify %s: %v", tfAddress, err)
		}
	} else if exists {
		if stateID == liveID && stateID != "" {
			status.Category = "OK"
			status.Message = fmt.Sprintf("%s (ID: %s) exists in state and AWS.", tfAddress, liveID)
		} else {
			status.Category = "POTENTIAL_IMPORT"
			status.Message = fmt.Sprintf("%s exists in AWS with ID '%s'. State ID: '%s'.", tfAddress, liveID, stateID)
			status.Command = fmt.Sprintf("terraform import %s %s", tfAddress, liveID)
		}
	} else {
		status.Category = "DANGEROUS"
		status.Message = fmt.Sprintf("%s (ID: %s) is in state but NOT FOUND in AWS.", tfAddress, stateID)
		status.Command = fmt.Sprintf("terraform state rm %s", tfAddress)
	}

	return status
}
