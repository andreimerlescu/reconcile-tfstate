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
					// Determine Kind for JSON output
					// CORRECTED: Access res.Mode
					if res.Mode == "data" {
						status.Kind = "data"
					} else {
						status.Kind = "resource" // Default to resource
					}
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
		// CORRECTED: Access status.Category
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
				Category:         "ERROR", // CORRECTED: Set Category
				Message:          fmt.Sprintf("Failed to unmarshal attributes for %s: %v", tfAddress, err),
				Kind:             resource.Mode, // CORRECTED: Access resource.Mode
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
	status.Kind = resource.Mode // CORRECTED: Access resource.Mode

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
	} else if val, ok := attributes["instance_profile_arn"].(string); ok { // For IAM Instance Profile
		arnInState = val
	} else if val, ok := attributes["role_arn"].(string); ok { // For IAM Role
		arnInState = val
	} else if val, ok := attributes["function_arn"].(string); ok { // For Lambda Function
		arnInState = val
	} else if val, ok := attributes["distribution_arn"].(string); ok { // For CloudFront Distribution
		arnInState = val
	} else if val, ok := attributes["autoscaling_group_arn"].(string); ok { // For Auto Scaling Group
		arnInState = val
	} else if val, ok := attributes["policy_arn"].(string); ok { // For Auto Scaling Policy
		arnInState = val
	} else if val, ok := attributes["alarm_arn"].(string); ok { // For CloudWatch Metric Alarm
		arnInState = val
	} else if val, ok := attributes["bucket_arn"].(string); ok { // For S3 Bucket Policy
		arnInState = val
	} else if val, ok := attributes["service_arn"].(string); ok { // For ECS Service
		arnInState = val
	} else if val, ok := attributes["task_definition_arn"].(string); ok { // For ECS Task Definition
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
			status.Category = "REGION_MISMATCH" // CORRECTED: Set Category
			status.Message = fmt.Sprintf("%s (state file claims in '%s') not found in '%s'. Suggest `terraform state rm %s` if resource moved.", tfAddress, stateRegionFromARN, currentFlagRegion, tfAddress)
			status.Command = fmt.Sprintf("terraform state rm %s", tfAddress)
			status.TFID = stateRegionFromARN // For JSON output
			status.AWSID = currentFlagRegion // For JSON output
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
		status.Category = "INFO" // CORRECTED: Set Category
		status.Message = fmt.Sprintf("Data/Local resource '%s'. No external verification needed.", tfAddress)
		status.TFID = stateID // Set TFID and AWSID for JSON
		status.AWSID = liveID // Will be empty in this case
		return status
	case "aws_security_group_rule":
		if sgRuleAWSID, ok := attributes["security_group_rule_id"].(string); ok && sgRuleAWSID != "" {
			liveID, exists, err = clients.verifySecurityGroupRule(ctx, sgRuleAWSID)
		} else {
			status.Category = "WARNING" // CORRECTED: Set Category
			status.Message = fmt.Sprintf("Resource type '%s' (ID: %s) verification is complex and 'security_group_rule_id' not found in state attributes. Manual verification recommended.", resource.Type, stateID)
			status.TFID = stateID
			status.AWSID = liveID
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
		var clusterName string
		val, ok := attributes["name"]
		if !ok || val == nil {
			// If 'name' is not found, check 'cluster_name' (common for data sources)
			val, ok = attributes["cluster_name"]
			if !ok || val == nil {
				return ResourceStatus{
					TerraformAddress: tfAddress,
					Error:            fmt.Errorf("neither 'name' nor 'cluster_name' attribute found or are null for aws_ecs_cluster. Raw values: name=%v, cluster_name=%v", attributes["name"], attributes["cluster_name"]),
					Category:         "ERROR", // CORRECTED: Set Category
					Message:          fmt.Sprintf("Failed to retrieve valid name/cluster_name attribute for %s. Inspect state file.", tfAddress),
					Kind:             resource.Mode,
				}
			}
		}
		clusterName = fmt.Sprintf("%v", val) // Robustly convert to string
		if clusterName == "" {
			return ResourceStatus{
				TerraformAddress: tfAddress,
				Error:            fmt.Errorf("attribute for aws_ecs_cluster converted to an empty string. Raw value: %v", val),
				Category:         "ERROR", // CORRECTED: Set Category
				Message:          fmt.Sprintf("Failed to retrieve valid name/cluster_name attribute for %s. Inspect state file.", tfAddress),
				Kind:             resource.Mode,
			}
		}
		liveID, exists, err = clients.verifyECSCluster(ctx, clusterName)
	case "aws_region":
		val, ok := attributes["name"]
		if !ok || val == nil {
			status.Category = "ERROR" // CORRECTED: Set Category
			status.Message = fmt.Sprintf("Data source '%s' has no valid 'name' attribute for region. Raw value: %v", tfAddress, attributes["name"])
			status.Kind = resource.Mode
			return status
		}
		regionInState := fmt.Sprintf("%v", val)
		if regionInState == "" {
			status.Category = "ERROR" // CORRECTED: Set Category
			status.Message = fmt.Sprintf("Data source '%s' 'name' attribute converted to an empty string. Raw value: %v", tfAddress, val)
			status.Kind = resource.Mode
			return status
		}

		if regionInState == currentFlagRegion {
			status.Category = "OK" // CORRECTED: Set Category
			status.Message = fmt.Sprintf("%s (ID: %s) resolves to current region and is in state.", tfAddress, regionInState)
			status.LiveID = regionInState
			status.ExistsInAWS = true
			status.TFID = regionInState  // For JSON output
			status.AWSID = regionInState // For JSON output
			return status
		} else {
			regionMismatchCount.Add(1)
			status.Category = "REGION_MISMATCH" // CORRECTED: Set Category
			status.Message = fmt.Sprintf("%s (state file claims region '%s') does not match current region '%s'. Suggest `terraform state rm %s` if resource moved or is irrelevant.", tfAddress, regionInState, currentFlagRegion, tfAddress)
			status.Command = fmt.Sprintf("terraform state rm %s", tfAddress)
			status.TFID = regionInState      // For JSON output
			status.AWSID = currentFlagRegion // For JSON output
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
	case "aws_instance":
		if instanceID, ok := attributes["id"].(string); ok && instanceID != "" {
			liveID, exists, err = clients.verifyInstance(ctx, instanceID)
		} else {
			err = fmt.Errorf("could not find 'id' attribute for aws_instance")
		}
	case "aws_launch_template":
		templateID, _ := attributes["id"].(string)
		templateName, _ := attributes["name"].(string)
		if templateID != "" || templateName != "" {
			liveID, exists, err = clients.verifyLaunchTemplate(ctx, templateID, templateName)
		} else {
			err = fmt.Errorf("could not find 'id' or 'name' attribute for aws_launch_template")
		}
	case "aws_autoscaling_group":
		if asgName, ok := attributes["name"].(string); ok && asgName != "" {
			liveID, exists, err = clients.verifyAutoscalingGroup(ctx, asgName)
		} else {
			err = fmt.Errorf("could not find 'name' attribute for aws_autoscaling_group")
		}
	case "aws_autoscaling_policy":
		policyARN, _ := attributes["arn"].(string)
		policyName, _ := attributes["name"].(string)
		asgName, _ := attributes["autoscaling_group_name"].(string)
		if policyARN != "" || (policyName != "" && asgName != "") {
			liveID, exists, err = clients.verifyAutoscalingPolicy(ctx, policyARN, policyName, asgName)
		} else {
			err = fmt.Errorf("could not find 'arn' or ('name' and 'autoscaling_group_name') attributes for aws_autoscaling_policy")
		}
	case "aws_cloudwatch_metric_alarm":
		if alarmName, ok := attributes["alarm_name"].(string); ok && alarmName != "" {
			liveID, exists, err = clients.verifyCloudWatchMetricAlarm(ctx, alarmName)
		} else {
			err = fmt.Errorf("could not find 'alarm_name' attribute for aws_cloudwatch_metric_alarm")
		}
	case "aws_iam_instance_profile":
		if profileName, ok := attributes["name"].(string); ok && profileName != "" {
			liveID, exists, err = clients.verifyIAMInstanceProfile(ctx, profileName)
		} else {
			err = fmt.Errorf("could not find 'name' attribute for aws_iam_instance_profile")
		}
	case "aws_iam_role":
		if roleName, ok := attributes["name"].(string); ok && roleName != "" {
			liveID, exists, err = clients.verifyIAMRole(ctx, roleName)
		} else {
			err = fmt.Errorf("could not find 'name' attribute for aws_iam_role")
		}
	case "aws_iam_role_policy":
		roleName, _ := attributes["role"].(string)
		policyName, _ := attributes["name"].(string)
		if roleName != "" && policyName != "" {
			liveID, exists, err = clients.verifyIAMRolePolicy(ctx, roleName, policyName)
		} else {
			err = fmt.Errorf("could not find 'role' or 'name' attributes for aws_iam_role_policy")
		}
	case "aws_lambda_function":
		if functionName, ok := attributes["function_name"].(string); ok && functionName != "" {
			liveID, exists, err = clients.verifyLambdaFunction(ctx, functionName)
		} else {
			err = fmt.Errorf("could not find 'function_name' attribute for aws_lambda_function")
		}
	case "aws_lambda_permission":
		functionName, _ := attributes["function_name"].(string)
		statementID, _ := attributes["statement_id"].(string)
		if functionName != "" && statementID != "" {
			liveID, exists, err = clients.verifyLambdaPermission(ctx, functionName, statementID)
		} else {
			err = fmt.Errorf("could not find 'function_name' or 'statement_id' attributes for aws_lambda_permission")
		}
	case "aws_cloudfront_distribution":
		if distributionID, ok := attributes["id"].(string); ok && distributionID != "" {
			liveID, exists, err = clients.verifyCloudFrontDistribution(ctx, distributionID)
		} else {
			err = fmt.Errorf("could not find 'id' attribute for aws_cloudfront_distribution")
		}
	case "aws_cloudfront_origin_access_identity":
		if oaiID, ok := attributes["id"].(string); ok && oaiID != "" {
			liveID, exists, err = clients.verifyCloudFrontOriginAccessIdentity(ctx, oaiID)
		} else {
			err = fmt.Errorf("could not find 'id' attribute for aws_cloudfront_origin_access_identity")
		}
	case "aws_s3_bucket_policy":
		if bucketName, ok := attributes["bucket"].(string); ok && bucketName != "" {
			liveID, exists, err = clients.verifyS3BucketPolicy(ctx, bucketName)
		} else {
			err = fmt.Errorf("could not find 'bucket' attribute for aws_s3_bucket_policy")
		}
	case "aws_s3_bucket_acl":
		if bucketName, ok := attributes["bucket"].(string); ok && bucketName != "" {
			liveID, exists, err = clients.verifyS3BucketACL(ctx, bucketName)
		} else {
			err = fmt.Errorf("could not find 'bucket' attribute for aws_s3_bucket_acl")
		}
	case "aws_s3_bucket_ownership_controls":
		if bucketName, ok := attributes["bucket"].(string); ok && bucketName != "" {
			liveID, exists, err = clients.verifyS3BucketOwnershipControls(ctx, bucketName)
		} else {
			err = fmt.Errorf("could not find 'bucket' attribute for aws_s3_bucket_ownership_controls")
		}
	case "aws_s3_bucket_public_access_block":
		if bucketName, ok := attributes["bucket"].(string); ok && bucketName != "" {
			liveID, exists, err = clients.verifyS3BucketPublicAccessBlock(ctx, bucketName)
		} else {
			err = fmt.Errorf("could not find 'bucket' attribute for aws_s3_bucket_public_access_block")
		}
	case "aws_s3_bucket_website_configuration":
		if bucketName, ok := attributes["bucket"].(string); ok && bucketName != "" {
			liveID, exists, err = clients.verifyS3BucketWebsiteConfiguration(ctx, bucketName)
		} else {
			err = fmt.Errorf("could not find 'bucket' attribute for aws_s3_bucket_website_configuration")
		}
	case "aws_s3_bucket_cors_configuration":
		if bucketName, ok := attributes["bucket"].(string); ok && bucketName != "" {
			liveID, exists, err = clients.verifyS3BucketCORSConfiguration(ctx, bucketName)
		} else {
			err = fmt.Errorf("could not find 'bucket' attribute for aws_s3_bucket_cors_configuration")
		}
	case "aws_s3_bucket_notification":
		if bucketName, ok := attributes["bucket"].(string); ok && bucketName != "" {
			liveID, exists, err = clients.verifyS3BucketNotification(ctx, bucketName)
		} else {
			err = fmt.Errorf("could not find 'bucket' attribute for aws_s3_bucket_notification")
		}
	case "aws_s3_object":
		bucketName, _ := attributes["bucket"].(string)
		key, _ := attributes["key"].(string)
		if bucketName != "" && key != "" {
			liveID, exists, err = clients.verifyS3Object(ctx, bucketName, key)
		} else {
			err = fmt.Errorf("could not find 'bucket' or 'key' attributes for aws_s3_object")
		}
	case "aws_ecs_service":
		// Get "cluster" attribute and convert robustly
		valCluster, okCluster := attributes["cluster"]
		if !okCluster || valCluster == nil {
			return ResourceStatus{
				TerraformAddress: tfAddress,
				Error:            fmt.Errorf("attribute 'cluster' for aws_ecs_service is missing or null. Raw value: %v", attributes["cluster"]),
				Category:         "ERROR",
				Message:          fmt.Sprintf("Failed to retrieve valid 'cluster' attribute for %s. Inspect state file.", tfAddress),
				Kind:             resource.Mode,
			}
		}
		clusterName := fmt.Sprintf("%v", valCluster)
		if clusterName == "" {
			return ResourceStatus{
				TerraformAddress: tfAddress,
				Error:            fmt.Errorf("attribute 'cluster' for aws_ecs_service converted to an empty string. Raw value: %v", valCluster),
				Category:         "ERROR",
				Message:          fmt.Sprintf("Failed to retrieve valid 'cluster' attribute for %s. Inspect state file.", tfAddress),
				Kind:             resource.Mode,
			}
		}

		valService, okService := attributes["name"]
		if !okService || valService == nil {
			return ResourceStatus{
				TerraformAddress: tfAddress,
				Error:            fmt.Errorf("attribute 'name' for aws_ecs_service is missing or null. Raw value: %v", attributes["name"]),
				Category:         "ERROR",
				Message:          fmt.Sprintf("Failed to retrieve valid 'name' attribute for %s. Inspect state file.", tfAddress),
				Kind:             resource.Mode,
			}
		}
		serviceName := fmt.Sprintf("%v", valService)
		if serviceName == "" {
			return ResourceStatus{
				TerraformAddress: tfAddress,
				Error:            fmt.Errorf("attribute 'name' for aws_ecs_service converted to an empty string. Raw value: %v", valService),
				Category:         "ERROR",
				Message:          fmt.Sprintf("Failed to retrieve valid 'name' attribute for %s. Inspect state file.", tfAddress),
				Kind:             resource.Mode,
			}
		}
		liveID, exists, err = clients.verifyECSService(ctx, clusterName, serviceName)

	case "aws_ecs_task_definition":
		val, ok := attributes["arn"]
		if !ok || val == nil { // Attribute not found or is null
			return ResourceStatus{
				TerraformAddress: tfAddress,
				Error:            fmt.Errorf("attribute 'arn' for aws_ecs_task_definition is missing or null. Raw value: %v", attributes["arn"]),
				Category:         "ERROR", // CORRECTED: Set Category
				Message:          fmt.Sprintf("Failed to retrieve valid 'arn' attribute for %s. Inspect state file.", tfAddress),
				Kind:             resource.Mode,
			}
		}
		taskDefinitionARN := fmt.Sprintf("%v", val) // Robustly convert to string
		if taskDefinitionARN == "" {                // Check if the string representation is empty
			return ResourceStatus{
				TerraformAddress: tfAddress,
				Error:            fmt.Errorf("attribute 'arn' for aws_ecs_task_definition converted to an empty string. Raw value: %v", val),
				Category:         "ERROR", // CORRECTED: Set Category
				Message:          fmt.Sprintf("Failed to retrieve valid 'arn' attribute for %s. Inspect state file.", tfAddress),
				Kind:             resource.Mode,
			}
		}
		liveID, exists, err = clients.verifyECSTaskDefinition(ctx, taskDefinitionARN)
	case "aws_lb_listener_certificate":
		listenerARN, _ := attributes["listener_arn"].(string)
		certificateARN, _ := attributes["certificate_arn"].(string)
		if listenerARN != "" && certificateARN != "" {
			liveID, exists, err = clients.verifyLBListenerCertificate(ctx, listenerARN, certificateARN)
		} else {
			err = fmt.Errorf("could not find 'listener_arn' or 'certificate_arn' attributes for aws_lb_listener_certificate")
		}

	default:
		status.Category = "WARNING" // CORRECTED: Set Category
		status.Message = fmt.Sprintf("Resource type '%s' not supported by this checker. Manual verification needed.", resource.Type)
		status.TFID = stateID
		status.AWSID = liveID
		return status
	}

	status.LiveID = liveID
	status.ExistsInAWS = exists
	status.Error = err

	if err != nil {
		status.Category = "ERROR" // CORRECTED: Set Category
		status.Message = fmt.Sprintf("Failed to verify %s: %v", tfAddress, err)
		status.TFID = stateID // For JSON output
		status.AWSID = liveID // For JSON output
	} else if exists {
		if strings.EqualFold(stateID, liveID) || len(stateID) == 0 {
			status.Category = "OK" // CORRECTED: Set Category
			status.Message = fmt.Sprintf("%s (ID: %s) exists in state and AWS.", tfAddress, liveID)
			status.TFID = stateID // For JSON output
			status.AWSID = liveID // For JSON output
		} else {
			status.Category = "POTENTIAL_IMPORT" // CORRECTED: Set Category
			status.Message = fmt.Sprintf("%s exists in AWS with ID '%s'. State ID: '%s'.", tfAddress, liveID, stateID)
			status.Command = fmt.Sprintf("terraform import %s %s", tfAddress, liveID)
			status.TFID = stateID // For JSON output
			status.AWSID = liveID // For JSON output
		}
	} else {
		status.Category = "DANGEROUS" // CORRECTED: Set Category
		status.Message = fmt.Sprintf("%s (ID: %s) is in state but NOT FOUND in AWS.", tfAddress, stateID)
		status.Command = fmt.Sprintf("terraform state rm %s", tfAddress)
		status.TFID = stateID // For JSON output
		status.AWSID = liveID // For JSON output
	}

	return status
}
