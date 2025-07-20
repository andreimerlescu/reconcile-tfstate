package main

import (
	"context"
	"encoding/json"
	"fmt"
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

	// Common ARN attribute for region check (extracted here for all ARN-based resources)
	var arnInState string
	if val, ok := attributes["arn"].(string); ok {
		arnInState = val
	} else if val, ok := attributes["load_balancer_arn"].(string); ok { // For listeners
		arnInState = val
	} else if val, ok := attributes["target_group_arn"].(string); ok { // For listener rules
		arnInState = val
	} else if val, ok := attributes["rule_arn"].(string); ok { // For listener rules
		arnInState = val
	} else if val, ok := attributes["certificate_arn"].(string); ok { // For ACM certificates
		arnInState = val
	}

	// --- REGION MISMATCH PRE-CHECK: Centralized Logic ---
	// If an ARN is present and its region doesn't match the current flagged region,
	// immediately categorize it as REGION_MISMATCH without making an AWS API call.
	// This prevents API errors from cross-region calls.
	if arnInState != "" {
		stateRegionFromARN := extractRegionFromARN(arnInState)
		if stateRegionFromARN != "" && stateRegionFromARN != currentFlagRegion {
			regionMismatchCount.Add(1)
			status.Category = "REGION_MISMATCH"
			status.Message = fmt.Sprintf("%s (state file claims in '%s') not found in '%s'. Suggest `terraform state rm %s` if resource moved.", tfAddress, stateRegionFromARN, currentFlagRegion, tfAddress)
			status.Command = fmt.Sprintf("terraform state rm %s", tfAddress)
			return status
		}
	}

	var liveID string
	var exists bool
	var err error

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
	case "aws_security_group_rule":
		if sgRuleAWSID, ok := attributes["security_group_rule_id"].(string); ok && sgRuleAWSID != "" {
			liveID, exists, err = clients.verifySecurityGroupRule(ctx, sgRuleAWSID)
		} else {
			status.Category = "WARNING"
			status.Message = fmt.Sprintf("Resource type '%s' (ID: %s) verification is complex and 'security_group_rule_id' not found in state attributes. Manual verification recommended.", resource.Type, stateID)
			return status
		}
	case "aws_acm_certificate":
		if certARN, ok := attributes["arn"].(string); ok && certARN != "" {
			liveID, exists, err = clients.verifyACMCertificate(ctx, certARN)
		} else {
			err = fmt.Errorf("could not find 'arn' attribute for aws_acm_certificate")
		}
	case "aws_acm_certificate_validation":
		if certARN, ok := attributes["certificate_arn"].(string); ok && certARN != "" {
			liveID, exists, err = clients.verifyACMCertificateValidation(ctx, certARN)
		} else {
			err = fmt.Errorf("could not find 'certificate_arn' attribute for aws_acm_certificate_validation")
		}
	case "aws_route53_record":
		zoneID, _ := attributes["zone_id"].(string)
		recordName, _ := attributes["name"].(string)
		recordType, _ := attributes["type"].(string)
		if zoneID != "" && recordName != "" && recordType != "" {
			liveID, exists, err = clients.verifyRoute53Record(ctx, zoneID, recordName, recordType)
		} else {
			err = fmt.Errorf("could not find 'zone_id', 'name', or 'type' attributes for aws_route53_record")
		}
	case "aws_ami":
		if imageID, ok := attributes["id"].(string); ok && imageID != "" {
			liveID, exists, err = clients.verifyAMI(ctx, imageID)
		} else {
			err = fmt.Errorf("could not find 'id' attribute for aws_ami")
		}
	case "aws_ecs_cluster":
		if clusterName, ok := attributes["name"].(string); ok && clusterName != "" {
			liveID, exists, err = clients.verifyECSCluster(ctx, clusterName)
		} else {
			err = fmt.Errorf("could not find 'name' attribute for aws_ecs_cluster")
		}
	case "aws_region":
		// Special handling for aws_region data source:
		// It's not a real resource that can be "found" or "not found" in AWS by ID.
		// Its "id" in state is just the region name (e.g., "us-east-1").
		// If the state's region matches the current execution region, it's OK.
		// If not, it's a region mismatch, as it implies a state artifact from another region.
		regionInState, ok := attributes["name"].(string) // Use "name" attribute for data.aws_region
		if !ok || regionInState == "" {
			// If region name attribute is missing/empty, it's an error in state format.
			status.Category = "ERROR"
			status.Message = fmt.Sprintf("Data source '%s' has no valid 'name' attribute for region.", tfAddress)
			return status
		}
		if regionInState == currentFlagRegion {
			status.Category = "OK"
			status.Message = fmt.Sprintf("%s (ID: %s) resolves to current region and is in state.", tfAddress, regionInState)
			status.LiveID = regionInState // Set LiveID to show what it resolved to
			status.ExistsInAWS = true
			return status
		} else {
			// If the region in state doesn't match the currently checked region
			// then it's a mismatch, not "dangerous" in the sense of a missing resource.
			regionMismatchCount.Add(1)
			status.Category = "REGION_MISMATCH"
			status.Message = fmt.Sprintf("%s (state file claims region '%s') does not match current region '%s'. Suggest `terraform state rm %s` if resource moved or is irrelevant.", tfAddress, regionInState, currentFlagRegion, tfAddress)
			status.Command = fmt.Sprintf("terraform state rm %s", tfAddress)
			return status
		}
	case "aws_ssm_parameter":
		if paramName, ok := attributes["name"].(string); ok && paramName != "" {
			liveID, exists, err = clients.verifySSMParameter(ctx, paramName)
		} else {
			err = fmt.Errorf("could not find 'name' attribute for aws_ssm_parameter")
		}
	case "aws_secretsmanager_secret":
		if secretID, ok := attributes["id"].(string); ok && secretID != "" {
			liveID, exists, err = clients.verifySecretsManagerSecret(ctx, secretID)
		} else {
			err = fmt.Errorf("could not find 'id' attribute for aws_secretsmanager_secret")
		}
	case "aws_secretsmanager_secret_version":
		secretID, _ := attributes["secret_id"].(string)
		versionID, _ := attributes["version_id"].(string)
		if secretID != "" && versionID != "" {
			liveID, exists, err = clients.verifySecretsManagerSecretVersion(ctx, secretID, versionID)
		} else {
			err = fmt.Errorf("could not find 'secret_id' or 'version_id' attribute for aws_secretsmanager_secret_version")
		}
	case "aws_eip":
		if allocationID, ok := attributes["allocation_id"].(string); ok && allocationID != "" {
			liveID, exists, err = clients.verifyEIP(ctx, allocationID)
		} else {
			err = fmt.Errorf("could not find 'allocation_id' attribute for aws_eip")
		}
	case "aws_internet_gateway":
		if igwID, ok := attributes["id"].(string); ok && igwID != "" {
			liveID, exists, err = clients.verifyInternetGateway(ctx, igwID)
		} else {
			err = fmt.Errorf("could not find 'id' attribute for aws_internet_gateway")
		}
	case "aws_nat_gateway":
		if natGatewayID, ok := attributes["id"].(string); ok && natGatewayID != "" {
			liveID, exists, err = clients.verifyNatGateway(ctx, natGatewayID)
		} else {
			err = fmt.Errorf("could not find 'id' attribute for aws_nat_gateway")
		}
	case "aws_route":
		routeTableID, _ := attributes["route_table_id"].(string)
		destinationCIDR, _ := attributes["destination_cidr_block"].(string) // Or ipv6
		if routeTableID != "" && (destinationCIDR != "" || attributes["destination_ipv6_cidr_block"] != nil) {
			liveID, exists, err = clients.verifyRoute(ctx, routeTableID, destinationCIDR)
		} else {
			err = fmt.Errorf("could not find 'route_table_id' or destination CIDR attributes for aws_route")
		}
	case "aws_route_table":
		if routeTableID, ok := attributes["id"].(string); ok && routeTableID != "" {
			liveID, exists, err = clients.verifyRouteTable(ctx, routeTableID)
		} else {
			err = fmt.Errorf("could not find 'id' attribute for aws_route_table")
		}
	case "aws_route_table_association":
		if associationID, ok := attributes["id"].(string); ok && associationID != "" {
			liveID, exists, err = clients.verifyRouteTableAssociation(ctx, associationID)
		} else {
			err = fmt.Errorf("could not find 'id' attribute for aws_route_table_association")
		}
	case "aws_subnet":
		if subnetID, ok := attributes["id"].(string); ok && subnetID != "" {
			liveID, exists, err = clients.verifySubnet(ctx, subnetID)
		} else {
			err = fmt.Errorf("could not find 'id' attribute for aws_subnet")
		}
	case "aws_vpc":
		if vpcID, ok := attributes["id"].(string); ok && vpcID != "" {
			liveID, exists, err = clients.verifyVPC(ctx, vpcID)
		} else {
			err = fmt.Errorf("could not find 'id' attribute for aws_vpc")
		}
	default:
		status.Category = "WARNING"
		status.Message = fmt.Sprintf("Resource type '%s' not supported by this checker. Manual verification needed.", resource.Type)
		return status
	}

	status.LiveID = liveID
	status.ExistsInAWS = exists
	status.Error = err

	if err != nil {
		status.Category = "ERROR"
		status.Message = fmt.Sprintf("Failed to verify %s: %v", tfAddress, err)
	} else if exists {
		if stateID == liveID || stateID == "" { // If stateID is empty, it's usually an OK data source where ID isn't critical.
			status.Category = "OK"
			status.Message = fmt.Sprintf("%s (ID: %s) exists in state and AWS.", tfAddress, liveID)
		} else {
			// This case is for resources that exist, but the stateID != liveID.
			// This often happens if the resource was manually modified or re-created outside of Terraform.
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
