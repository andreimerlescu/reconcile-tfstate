package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/acm"
	"github.com/aws/aws-sdk-go-v2/service/acm/types"
	"github.com/aws/aws-sdk-go-v2/service/autoscaling"
	"github.com/aws/aws-sdk-go-v2/service/cloudfront"
	"github.com/aws/aws-sdk-go-v2/service/cloudwatch"
	"github.com/aws/aws-sdk-go-v2/service/cloudwatchlogs"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	ec2types "github.com/aws/aws-sdk-go-v2/service/ec2/types"
	"github.com/aws/aws-sdk-go-v2/service/ecs"
	"github.com/aws/aws-sdk-go-v2/service/elasticloadbalancingv2"
	"github.com/aws/aws-sdk-go-v2/service/iam"
	"github.com/aws/aws-sdk-go-v2/service/lambda"
	"github.com/aws/aws-sdk-go-v2/service/route53"
	route53types "github.com/aws/aws-sdk-go-v2/service/route53/types"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/secretsmanager"
	"github.com/aws/aws-sdk-go-v2/service/ssm"
)

// verifyS3Bucket checks if an S3 bucket exists in AWS
func (c *AWSClient) verifyS3Bucket(ctx context.Context, bucketName string) (string, bool, error) {
	_, err := c.S3Client.HeadBucket(ctx, &s3.HeadBucketInput{
		Bucket: aws.String(bucketName),
	})
	if err != nil {
		if strings.Contains(err.Error(), "NotFound") || strings.Contains(err.Error(), "NoSuchBucket") {
			return "", false, nil
		}
		if strings.Contains(err.Error(), "Forbidden") {
			log.Printf("WARNING: Access denied to S3 bucket '%s'. Assuming it exists.", bucketName)
			return bucketName, true, nil
		}
		return "", false, fmt.Errorf("failed to check S3 bucket '%s': %w", bucketName, err)
	}
	return bucketName, true, nil
}

// verifyCloudWatchLogGroup checks if a CloudWatch Log Group exists in AWS
func (c *AWSClient) verifyCloudWatchLogGroup(ctx context.Context, logGroupName string) (string, bool, error) {
	resp, err := c.CloudWatchLogsClient.DescribeLogGroups(ctx, &cloudwatchlogs.DescribeLogGroupsInput{
		LogGroupNamePrefix: aws.String(logGroupName),
	})
	if err != nil {
		return "", false, fmt.Errorf("failed to describe CloudWatch Log Group '%s': %w", logGroupName, err)
	}

	for _, lg := range resp.LogGroups {
		if *lg.LogGroupName == logGroupName {
			return *lg.LogGroupName, true, nil
		}
	}
	return "", false, nil
}

// verifyKeyPair checks if an EC2 Key Pair exists in AWS
func (c *AWSClient) verifyKeyPair(ctx context.Context, keyName string) (string, bool, error) {
	resp, err := c.EC2Client.DescribeKeyPairs(ctx, &ec2.DescribeKeyPairsInput{
		KeyNames: []string{keyName},
	})
	if err != nil {
		if strings.Contains(err.Error(), "InvalidKeyPair.NotFound") {
			return "", false, nil
		}
		return "", false, fmt.Errorf("failed to describe EC2 Key Pair '%s': %w", keyName, err)
	}

	if len(resp.KeyPairs) > 0 {
		return *resp.KeyPairs[0].KeyName, true, nil
	}
	return "", false, nil
}

// verifySecurityGroup checks if an EC2 Security Group exists in AWS
func (c *AWSClient) verifySecurityGroup(ctx context.Context, sgID, sgName string) (string, bool, error) {
	input := &ec2.DescribeSecurityGroupsInput{}
	if sgID != "" {
		input.GroupIds = []string{sgID}
	} else if sgName != "" {
		input.GroupNames = []string{sgName}
	} else {
		return "", false, fmt.Errorf("security group ID or name must be provided for verification")
	}

	resp, err := c.EC2Client.DescribeSecurityGroups(ctx, input)
	if err != nil {
		if strings.Contains(err.Error(), "InvalidGroup.NotFound") {
			return "", false, nil
		}
		return "", false, fmt.Errorf("failed to describe Security Group '%s' (ID: '%s'): %w", sgName, sgID, err)
	}

	if len(resp.SecurityGroups) > 0 {
		return *resp.SecurityGroups[0].GroupId, true, nil
	}
	return "", false, nil
}

// verifyRoute53Zone checks if a Route53 Hosted Zone exists in AWS
func (c *AWSClient) verifyRoute53Zone(ctx context.Context, zoneID, zoneName string) (string, bool, error) {
	if zoneID != "" {
		resp, err := c.Route53Client.GetHostedZone(ctx, &route53.GetHostedZoneInput{
			Id: aws.String(zoneID),
		})
		if err != nil {
			if strings.Contains(err.Error(), "NoSuchHostedZone") {
				return "", false, nil
			}
			return "", false, fmt.Errorf("failed to get Route53 Hosted Zone by ID '%s': %w", zoneID, err)
		}
		return *resp.HostedZone.Id, true, nil
	} else if zoneName != "" {
		resp, err := c.Route53Client.ListHostedZonesByName(ctx, &route53.ListHostedZonesByNameInput{
			DNSName: aws.String(zoneName),
		})
		if err != nil {
			return "", false, fmt.Errorf("failed to list Route53 Hosted Zones by name '%s': %w", zoneName, err)
		}
		for _, zone := range resp.HostedZones {
			if *zone.Name == zoneName || *zone.Name == zoneName+"." { // Route53 often appends a dot
				return *zone.Id, true, nil
			}
		}
		return "", false, nil
	}
	return "", false, fmt.Errorf("%s", "Route53 zone ID or name must be provided for verification")
}

// verifyLoadBalancer checks if an ELBv2 Load Balancer exists in AWS
func (c *AWSClient) verifyLoadBalancer(ctx context.Context, lbARN, lbName string, _ string) (string, bool, error) {
	input := &elasticloadbalancingv2.DescribeLoadBalancersInput{}
	if lbARN != "" {
		input.LoadBalancerArns = []string{lbARN}
	} else if lbName != "" {
		input.Names = []string{lbName}
	} else {
		return "", false, fmt.Errorf("load balancer ARN or name must be provided for verification")
	}

	resp, err := c.ELBV2Client.DescribeLoadBalancers(ctx, input)
	if err != nil {
		if strings.Contains(err.Error(), "LoadBalancerNotFound") {
			return "", false, nil // Load Balancer does not exist
		}
		return "", false, fmt.Errorf("failed to describe Load Balancer '%s' (ARN: '%s'): %w", lbName, lbARN, err)
	}

	if len(resp.LoadBalancers) > 0 {
		return *resp.LoadBalancers[0].LoadBalancerArn, true, nil
	}
	return "", false, nil
}

// verifyListener checks if an ELBv2 Listener exists in AWS
func (c *AWSClient) verifyListener(ctx context.Context, listenerARN, lbARN string, _ string) (string, bool, error) {
	input := &elasticloadbalancingv2.DescribeListenersInput{}
	if listenerARN != "" {
		input.ListenerArns = []string{listenerARN}
	} else if lbARN != "" {
		input.LoadBalancerArn = aws.String(lbARN)
	} else {
		return "", false, fmt.Errorf("listener ARN or load balancer ARN must be provided for verification")
	}

	resp, err := c.ELBV2Client.DescribeListeners(ctx, input)
	if err != nil {
		if strings.Contains(err.Error(), "ListenerNotFound") || strings.Contains(err.Error(), "LoadBalancerNotFound") {
			return "", false, nil
		}
		return "", false, fmt.Errorf("failed to describe Listener '%s' (LB ARN: '%s'): %w", listenerARN, lbARN, err)
	}

	if len(resp.Listeners) > 0 {
		return *resp.Listeners[0].ListenerArn, true, nil
	}
	return "", false, nil
}

// verifyTargetGroup checks if an ELBv2 Target Group exists in AWS
func (c *AWSClient) verifyTargetGroup(ctx context.Context, tgARN, tgName string, _ string) (string, bool, error) {
	input := &elasticloadbalancingv2.DescribeTargetGroupsInput{}
	if tgARN != "" {
		input.TargetGroupArns = []string{tgARN}
	} else if tgName != "" {
		input.Names = []string{tgName}
	} else {
		return "", false, fmt.Errorf("target group ARN or name must be provided for verification")
	}

	resp, err := c.ELBV2Client.DescribeTargetGroups(ctx, input)
	if err != nil {
		if strings.Contains(err.Error(), "TargetGroupNotFound") {
			return "", false, nil
		}
		return "", false, fmt.Errorf("failed to describe Target Group '%s' (ARN: '%s'): %w", tgName, tgARN, err)
	}

	if len(resp.TargetGroups) > 0 {
		return *resp.TargetGroups[0].TargetGroupArn, true, nil
	}
	return "", false, nil
}

// verifyListenerRule checks if an ELBv2 Listener Rule exists in AWS
func (c *AWSClient) verifyListenerRule(ctx context.Context, ruleARN, listenerARN string, _ string) (string, bool, error) {
	input := &elasticloadbalancingv2.DescribeRulesInput{}
	if ruleARN != "" {
		input.RuleArns = []string{ruleARN}
	} else if listenerARN != "" {
		input.ListenerArn = aws.String(listenerARN)
	} else {
		return "", false, fmt.Errorf("rule ARN or listener ARN must be provided for verification")
	}

	resp, err := c.ELBV2Client.DescribeRules(ctx, input)
	if err != nil {
		if strings.Contains(err.Error(), "RuleNotFound") || strings.Contains(err.Error(), "ListenerNotFound") {
			return "", false, nil
		}
		return "", false, fmt.Errorf("failed to describe Listener Rule '%s' (Listener ARN: '%s'): %w", ruleARN, listenerARN, err)
	}

	if len(resp.Rules) > 0 {
		return *resp.Rules[0].RuleArn, true, nil
	}
	return "", false, nil
}

// verifySecurityGroupRule checks if an EC2 Security Group Rule exists in AWS.
func (c *AWSClient) verifySecurityGroupRule(ctx context.Context, sgRuleAWSID string) (string, bool, error) {
	input := &ec2.DescribeSecurityGroupRulesInput{
		SecurityGroupRuleIds: []string{sgRuleAWSID},
	}
	resp, err := c.EC2Client.DescribeSecurityGroupRules(ctx, input)
	if err != nil {
		// Specific error for not found rule ID
		if strings.Contains(err.Error(), "InvalidSecurityGroupRuleID.NotFound") {
			return "", false, nil // Rule not found
		}
		return "", false, fmt.Errorf("failed to describe Security Group Rule '%s': %w", sgRuleAWSID, err)
	}

	if len(resp.SecurityGroupRules) > 0 && *resp.SecurityGroupRules[0].SecurityGroupRuleId == sgRuleAWSID {
		return *resp.SecurityGroupRules[0].SecurityGroupRuleId, true, nil // Rule exists
	}
	return "", false, nil // Rule not found
}

// verifyACMCertificate checks if an ACM Certificate exists in AWS.
func (c *AWSClient) verifyACMCertificate(ctx context.Context, certARN string) (string, bool, error) {
	input := &acm.DescribeCertificateInput{
		CertificateArn: aws.String(certARN),
	}
	resp, err := c.ACMClient.DescribeCertificate(ctx, input)
	if err != nil {
		if strings.Contains(err.Error(), "ResourceNotFoundException") {
			return "", false, nil // Certificate not found
		}
		return "", false, fmt.Errorf("failed to describe ACM certificate '%s': %w", certARN, err)
	}
	return *resp.Certificate.CertificateArn, true, nil // Certificate exists
}

// verifyACMCertificateValidation checks if an ACM Certificate Validation exists in AWS.
func (c *AWSClient) verifyACMCertificateValidation(ctx context.Context, certARN string) (string, bool, error) {
	// Certificate validation is implicitly checked by the certificate status.
	// If the certificate exists and its status is ISSUED, it's validated.
	input := &acm.DescribeCertificateInput{
		CertificateArn: aws.String(certARN),
	}
	resp, err := c.ACMClient.DescribeCertificate(ctx, input)
	if err != nil {
		if strings.Contains(err.Error(), "ResourceNotFoundException") {
			return "", false, nil // Certificate not found, thus not validated
		}
		return "", false, fmt.Errorf("failed to describe ACM certificate for validation check '%s': %w", certARN, err)
	}

	if resp.Certificate.Status == types.CertificateStatusIssued {
		return *resp.Certificate.CertificateArn, true, nil // Certificate is issued/validated
	}
	return "", false, nil // Certificate not found or not yet issued/validated
}

// verifyRoute53Record checks if a Route53 Record exists in AWS.
func (c *AWSClient) verifyRoute53Record(ctx context.Context, zoneID, recordName, recordType string) (string, bool, error) {
	input := &route53.ListResourceRecordSetsInput{
		HostedZoneId:    aws.String(zoneID),
		StartRecordName: aws.String(recordName),
		StartRecordType: route53types.RRType(recordType),
		MaxItems:        aws.Int32(1), // Fetch only one record for efficiency
	}

	resp, err := c.Route53Client.ListResourceRecordSets(ctx, input)
	if err != nil {
		if strings.Contains(err.Error(), "NoSuchHostedZone") {
			return "", false, fmt.Errorf("route53 Hosted Zone '%s' not found for record check: %w", zoneID, err)
		}
		return "", false, fmt.Errorf("failed to list Route53 record sets for '%s' in zone '%s': %w", recordName, zoneID, err)
	}

	for _, record := range resp.ResourceRecordSets {
		normalizedRecordName := *record.Name
		if strings.HasSuffix(normalizedRecordName, ".") {
			normalizedRecordName = strings.TrimSuffix(normalizedRecordName, ".")
		}
		if normalizedRecordName == recordName && record.Type == route53types.RRType(recordType) {
			return fmt.Sprintf("%s_%s_%s", zoneID, recordName, recordType), true, nil
		}
	}
	return "", false, nil // Record not found
}

// verifyAMI checks if an EC2 AMI exists in AWS.
func (c *AWSClient) verifyAMI(ctx context.Context, imageID string) (string, bool, error) {
	input := &ec2.DescribeImagesInput{
		ImageIds: []string{imageID},
		Owners:   []string{"self", "amazon"},
	}
	resp, err := c.EC2Client.DescribeImages(ctx, input)
	if err != nil {
		if strings.Contains(err.Error(), "InvalidAMIID.NotFound") {
			return "", false, nil
		}
		return "", false, fmt.Errorf("failed to describe EC2 AMI '%s': %w", imageID, err)
	}
	if len(resp.Images) > 0 && *resp.Images[0].ImageId == imageID {
		return *resp.Images[0].ImageId, true, nil
	}
	return "", false, nil
}

// verifyECSCluster checks if an ECS Cluster exists in AWS.
func (c *AWSClient) verifyECSCluster(ctx context.Context, clusterName string) (string, bool, error) {
	input := &ecs.DescribeClustersInput{
		Clusters: []string{clusterName},
	}
	resp, err := c.ECSClient.DescribeClusters(ctx, input)
	if err != nil {
		if strings.Contains(err.Error(), "ClusterNotFoundException") {
			return "", false, nil
		}
		return "", false, fmt.Errorf("failed to describe ECS cluster '%s': %w", clusterName, err)
	}

	if len(resp.Clusters) > 0 && *resp.Clusters[0].ClusterName == clusterName {
		return *resp.Clusters[0].ClusterArn, true, nil
	}
	return "", false, nil
}

// verifySSMParameter checks if an SSM Parameter exists in AWS.
func (c *AWSClient) verifySSMParameter(ctx context.Context, paramName string) (string, bool, error) {
	input := &ssm.GetParameterInput{
		Name:           aws.String(paramName),
		WithDecryption: aws.Bool(false),
	}
	_, err := c.SSMClient.GetParameter(ctx, input)
	if err != nil {
		if strings.Contains(err.Error(), "ParameterNotFound") {
			return "", false, nil
		}
		return "", false, fmt.Errorf("failed to get SSM parameter '%s': %w", paramName, err)
	}
	return paramName, true, nil
}

// verifySecretsManagerSecret checks if a Secrets Manager Secret exists in AWS.
func (c *AWSClient) verifySecretsManagerSecret(ctx context.Context, secretID string) (string, bool, error) {
	input := &secretsmanager.DescribeSecretInput{
		SecretId: aws.String(secretID),
	}
	_, err := c.SecretsManagerClient.DescribeSecret(ctx, input)
	if err != nil {
		if strings.Contains(err.Error(), "ResourceNotFoundException") || strings.Contains(err.Error(), "ValidationException") {
			return "", false, nil
		}
		return "", false, fmt.Errorf("failed to describe Secrets Manager secret '%s': %w", secretID, err)
	}
	return secretID, true, nil
}

// verifySecretsManagerSecretVersion checks if a Secrets Manager Secret Version exists in AWS.
func (c *AWSClient) verifySecretsManagerSecretVersion(ctx context.Context, secretID, versionID string) (string, bool, error) {
	input := &secretsmanager.GetSecretValueInput{
		SecretId:  aws.String(secretID),
		VersionId: aws.String(versionID),
	}
	_, err := c.SecretsManagerClient.GetSecretValue(ctx, input)
	if err != nil {
		if strings.Contains(err.Error(), "ResourceNotFoundException") || strings.Contains(err.Error(), "ValidationException") {
			return "", false, nil
		}
		return "", false, fmt.Errorf("failed to get Secrets Manager secret version '%s' for secret '%s': %w", versionID, secretID, err)
	}
	return versionID, true, nil
}

// verifyEIP checks if an EC2 Elastic IP exists in AWS.
func (c *AWSClient) verifyEIP(ctx context.Context, allocationID string) (string, bool, error) {
	input := &ec2.DescribeAddressesInput{
		AllocationIds: []string{allocationID},
	}
	resp, err := c.EC2Client.DescribeAddresses(ctx, input)
	if err != nil {
		if strings.Contains(err.Error(), "InvalidAllocationID.NotFound") {
			return "", false, nil
		}
		return "", false, fmt.Errorf("failed to describe EIP '%s': %w", allocationID, err)
	}
	if len(resp.Addresses) > 0 && resp.Addresses[0].AllocationId != nil && *resp.Addresses[0].AllocationId == allocationID {
		return *resp.Addresses[0].AllocationId, true, nil
	}
	return "", false, nil
}

// verifyInternetGateway checks if an EC2 Internet Gateway exists in AWS.
func (c *AWSClient) verifyInternetGateway(ctx context.Context, igwID string) (string, bool, error) {
	input := &ec2.DescribeInternetGatewaysInput{
		InternetGatewayIds: []string{igwID},
	}
	resp, err := c.EC2Client.DescribeInternetGateways(ctx, input)
	if err != nil {
		if strings.Contains(err.Error(), "InvalidInternetGatewayID.NotFound") {
			return "", false, nil
		}
		return "", false, fmt.Errorf("failed to describe Internet Gateway '%s': %w", igwID, err)
	}
	if len(resp.InternetGateways) > 0 && resp.InternetGateways[0].InternetGatewayId != nil && *resp.InternetGateways[0].InternetGatewayId == igwID {
		return *resp.InternetGateways[0].InternetGatewayId, true, nil
	}
	return "", false, nil
}

// verifyNatGateway checks if an EC2 NAT Gateway exists in AWS.
func (c *AWSClient) verifyNatGateway(ctx context.Context, natGatewayID string) (string, bool, error) {
	input := &ec2.DescribeNatGatewaysInput{
		NatGatewayIds: []string{natGatewayID},
	}
	resp, err := c.EC2Client.DescribeNatGateways(ctx, input)
	if err != nil {
		if strings.Contains(err.Error(), "NatGatewayNotFound") {
			return "", false, nil
		}
		return "", false, fmt.Errorf("failed to describe NAT Gateway '%s': %w", natGatewayID, err)
	}
	if len(resp.NatGateways) > 0 && resp.NatGateways[0].NatGatewayId != nil && *resp.NatGateways[0].NatGatewayId == natGatewayID {
		return *resp.NatGateways[0].NatGatewayId, true, nil
	}
	return "", false, nil
}

// verifyRoute checks if an EC2 Route exists in AWS.
func (c *AWSClient) verifyRoute(ctx context.Context, routeTableID, destinationCIDR string) (string, bool, error) {
	input := &ec2.DescribeRouteTablesInput{
		RouteTableIds: []string{routeTableID},
	}
	resp, err := c.EC2Client.DescribeRouteTables(ctx, input)
	if err != nil {
		if strings.Contains(err.Error(), "InvalidRouteTableID.NotFound") {
			return "", false, fmt.Errorf("route Table '%s' not found for route verification: %w", routeTableID, err)
		}
		return "", false, fmt.Errorf("failed to describe Route Table '%s' for route verification: %w", routeTableID, err)
	}

	if len(resp.RouteTables) > 0 && resp.RouteTables[0].Routes != nil {
		for _, route := range resp.RouteTables[0].Routes {
			if route.DestinationCidrBlock != nil && *route.DestinationCidrBlock == destinationCIDR && route.State == ec2types.RouteStateActive {
				return fmt.Sprintf("%s_%s", routeTableID, destinationCIDR), true, nil
			}
			if route.DestinationIpv6CidrBlock != nil && *route.DestinationIpv6CidrBlock == destinationCIDR && route.State == ec2types.RouteStateActive {
				return fmt.Sprintf("%s_%s", routeTableID, destinationCIDR), true, nil
			}
		}
	}
	return "", false, nil
}

// verifyRouteTable checks if an EC2 Route Table exists in AWS.
func (c *AWSClient) verifyRouteTable(ctx context.Context, routeTableID string) (string, bool, error) {
	input := &ec2.DescribeRouteTablesInput{
		RouteTableIds: []string{routeTableID},
	}
	resp, err := c.EC2Client.DescribeRouteTables(ctx, input)
	if err != nil {
		if strings.Contains(err.Error(), "InvalidRouteTableID.NotFound") {
			return "", false, nil
		}
		return "", false, fmt.Errorf("failed to describe Route Table '%s': %w", routeTableID, err)
	}
	if len(resp.RouteTables) > 0 && resp.RouteTables[0].RouteTableId != nil && *resp.RouteTables[0].RouteTableId == routeTableID {
		return *resp.RouteTables[0].RouteTableId, true, nil
	}
	return "", false, nil
}

// verifyRouteTableAssociation checks if an EC2 Route Table Association exists in AWS.
func (c *AWSClient) verifyRouteTableAssociation(ctx context.Context, associationID string) (string, bool, error) {
	resp, err := c.EC2Client.DescribeRouteTables(ctx, &ec2.DescribeRouteTablesInput{})
	if err != nil {
		return "", false, fmt.Errorf("failed to list route tables for association check: %w", err)
	}

	for _, rt := range resp.RouteTables {
		if rt.Associations != nil {
			for _, assoc := range rt.Associations {
				if assoc.RouteTableAssociationId != nil && *assoc.RouteTableAssociationId == associationID {
					return *assoc.RouteTableAssociationId, true, nil
				}
			}
		}
	}
	return "", false, nil
}

// verifySubnet checks if an EC2 Subnet exists in AWS.
func (c *AWSClient) verifySubnet(ctx context.Context, subnetID string) (string, bool, error) {
	input := &ec2.DescribeSubnetsInput{
		SubnetIds: []string{subnetID},
	}
	resp, err := c.EC2Client.DescribeSubnets(ctx, input)
	if err != nil {
		if strings.Contains(err.Error(), "InvalidSubnetID.NotFound") {
			return "", false, nil
		}
		return "", false, fmt.Errorf("failed to describe Subnet '%s': %w", subnetID, err)
	}
	if len(resp.Subnets) > 0 && resp.Subnets[0].SubnetId != nil && *resp.Subnets[0].SubnetId == subnetID {
		return *resp.Subnets[0].SubnetId, true, nil
	}
	return "", false, nil
}

// verifyVPC checks if an EC2 VPC exists in AWS.
func (c *AWSClient) verifyVPC(ctx context.Context, vpcID string) (string, bool, error) {
	input := &ec2.DescribeVpcsInput{
		VpcIds: []string{vpcID},
	}
	resp, err := c.EC2Client.DescribeVpcs(ctx, input)
	if err != nil {
		if strings.Contains(err.Error(), "InvalidVpcID.NotFound") {
			return "", false, nil
		}
		return "", false, fmt.Errorf("failed to describe VPC '%s': %w", vpcID, err)
	}
	if len(resp.Vpcs) > 0 && resp.Vpcs[0].VpcId != nil && *resp.Vpcs[0].VpcId == vpcID {
		return *resp.Vpcs[0].VpcId, true, nil
	}
	return "", false, nil
}

// verifyInstance checks if an EC2 Instance exists in AWS.
// verifyInstance checks if an EC2 Instance exists in AWS.
func (c *AWSClient) verifyInstance(ctx context.Context, instanceID string) (string, bool, error) {
	input := &ec2.DescribeInstancesInput{
		InstanceIds: []string{instanceID},
	}
	resp, err := c.EC2Client.DescribeInstances(ctx, input)
	if err != nil {
		// Specific error for not found instance ID
		if strings.Contains(err.Error(), "InvalidInstanceID.NotFound") {
			return "", false, nil // Instance not found
		}
		return "", false, fmt.Errorf("failed to describe EC2 instance '%s': %w", instanceID, err)
	}

	// Iterate through reservations and instances to find the matching one
	for _, reservation := range resp.Reservations {
		for _, instance := range reservation.Instances {
			// Ensure the instance is not terminated or shutting down
			if instance.InstanceId != nil && *instance.InstanceId == instanceID &&
				instance.State != nil &&
				(instance.State.Name != ec2types.InstanceStateNameTerminated && instance.State.Name != ec2types.InstanceStateNameShuttingDown) {
				return *instance.InstanceId, true, nil // Instance found and active
			}
		}
	}
	return "", false, nil // Instance not found or not in an active state
}

// verifyLaunchTemplate checks if an EC2 Launch Template exists in AWS.
func (c *AWSClient) verifyLaunchTemplate(ctx context.Context, templateID, templateName string) (string, bool, error) {
	input := &ec2.DescribeLaunchTemplatesInput{}

	if templateID != "" {
		input.LaunchTemplateIds = []string{templateID}
	} else if templateName != "" {
		input.LaunchTemplateNames = []string{templateName}
	} else {
		return "", false, fmt.Errorf("launch template ID or name must be provided for verification")
	}

	resp, err := c.EC2Client.DescribeLaunchTemplates(ctx, input)
	if err != nil {
		// Specific error for not found template ID/Name
		if strings.Contains(err.Error(), "InvalidLaunchTemplateName.NotFoundException") ||
			strings.Contains(err.Error(), "InvalidLaunchTemplateID.NotFoundException") {
			return "", false, nil // Launch Template not found
		}
		return "", false, fmt.Errorf("failed to describe EC2 Launch Template '%s' (ID: '%s'): %w", templateName, templateID, err)
	}

	if len(resp.LaunchTemplates) > 0 {
		// Return the ID as the canonical identifier if found
		if resp.LaunchTemplates[0].LaunchTemplateId != nil {
			return *resp.LaunchTemplates[0].LaunchTemplateId, true, nil
		}
		// Fallback to name if ID isn't immediately available (shouldn't happen for active templates)
		if resp.LaunchTemplates[0].LaunchTemplateName != nil {
			return *resp.LaunchTemplates[0].LaunchTemplateName, true, nil
		}
	}
	return "", false, nil // Launch Template not found
}

// verifyAutoscalingGroup checks if an Auto Scaling Group exists in AWS.
func (c *AWSClient) verifyAutoscalingGroup(ctx context.Context, asgName string) (string, bool, error) {
	input := &autoscaling.DescribeAutoScalingGroupsInput{
		AutoScalingGroupNames: []string{asgName},
	}
	resp, err := c.AutoscalingClient.DescribeAutoScalingGroups(ctx, input)
	if err != nil {
		// Specific error for not found ASG. Auto Scaling API doesn't always return a specific "NotFound" error
		// for DescribeAutoScalingGroups when the list is empty, it just returns an empty list.
		// However, a general API error could still occur.
		// This specific error string `AutoScalingGroup name not found` often comes from other SDKs or tools,
		// the AWS SDK for Go v2 typically just returns an empty slice.
		if strings.Contains(err.Error(), "AutoScalingGroup name not found") {
			return "", false, nil
		}
		return "", false, fmt.Errorf("failed to describe Auto Scaling Group '%s': %w", asgName, err)
	}

	if len(resp.AutoScalingGroups) > 0 {
		// Found the ASG, return its ARN as the canonical ID
		if resp.AutoScalingGroups[0].AutoScalingGroupARN != nil {
			return *resp.AutoScalingGroups[0].AutoScalingGroupARN, true, nil
		}
		// Fallback to name if ARN isn't present (unlikely for ASGs)
		if resp.AutoScalingGroups[0].AutoScalingGroupName != nil {
			return *resp.AutoScalingGroups[0].AutoScalingGroupName, true, nil
		}
	}
	return "", false, nil // ASG not found
}

// verifyAutoscalingPolicy checks if an Auto Scaling Policy exists in AWS.
func (c *AWSClient) verifyAutoscalingPolicy(ctx context.Context, policyARN, policyName, asgName string) (string, bool, error) {
	input := &autoscaling.DescribePoliciesInput{}

	// Prioritize ARN if available, but if not, use name + ASG name
	// The DescribePolicies API does not have a direct PolicyArns filter field.
	// We primarily filter by PolicyNames and AutoScalingGroupName.
	// If only ARN is provided and no name/asgName, we'd need to list all and filter manually,
	// which is less efficient. For now, we assume policyName and asgName are often available from state.
	if policyName != "" && asgName != "" {
		input.PolicyNames = []string{policyName}
		input.AutoScalingGroupName = aws.String(asgName)
	} else if policyARN != "" {
		// If only ARN is provided, we might need to list all policies and then filter by ARN.
		// For simplicity and common use case (name + asgName from state), we'll keep this simpler for now.
		// A robust solution for ARN-only would involve pagination over all policies.
		// Let's assume for now that if the policyARN is available, its corresponding name+asgName
		// would also be extractable from the state attributes.
		// If this becomes a common ERROR, we can implement a more complex ARN-only lookup.
		// For the direct comparison, we will still use the name and ASG name if present,
		// or if not, then the fallback is that if you only have ARN, we can't efficiently lookup.
		// The error message guides to manual verification in that case.
		return "", false, fmt.Errorf("policy name and autoscaling group name are required to verify policy '%s' using DescribePolicies; direct ARN lookup is not supported by API filters", policyARN)
	} else {
		return "", false, fmt.Errorf("auto Scaling Policy name and Auto Scaling Group name must be provided for verification")
	}

	resp, err := c.AutoscalingClient.DescribePolicies(ctx, input)
	if err != nil {
		// Similar to ASGs, DescribePolicies might return an empty list rather than a specific NotFound error.
		return "", false, fmt.Errorf("failed to describe Auto Scaling Policy '%s' (ARN: '%s'): %w", policyName, policyARN, err)
	}

	if len(resp.ScalingPolicies) > 0 {
		// Verify if the found policy's ARN matches the one from state, or just its name if ARN wasn't primary.
		// Since we filtered by name and ASG name, the first result should be it.
		foundPolicyARN := ""
		if resp.ScalingPolicies[0].PolicyARN != nil {
			foundPolicyARN = *resp.ScalingPolicies[0].PolicyARN
		}

		// If policyARN from state was provided, check if it matches the found one.
		// Otherwise, we just rely on name+ASG name match being sufficient.
		if policyARN != "" && foundPolicyARN == policyARN {
			return foundPolicyARN, true, nil
		} else if policyARN == "" { // If state only had name+asgName, then a found policy is sufficient
			return foundPolicyARN, true, nil // Returns the actual live ARN
		} else { // policyARN from state exists but doesn't match the found one.
			return foundPolicyARN, true, nil // This will result in POTENTIAL_IMPORT if liveID != stateID
		}
	}
	return "", false, nil // Policy not found
}

// verifyCloudWatchMetricAlarm checks if a CloudWatch Metric Alarm exists in AWS.
func (c *AWSClient) verifyCloudWatchMetricAlarm(ctx context.Context, alarmName string) (string, bool, error) {
	input := &cloudwatch.DescribeAlarmsInput{
		AlarmNames: []string{alarmName},
	}
	resp, err := c.CloudWatchClient.DescribeAlarms(ctx, input)
	if err != nil {
		// CloudWatch DescribeAlarms typically returns an empty list if not found,
		// rather than a specific 'NotFound' error for the alarm itself.
		// Handle general API errors.
		return "", false, fmt.Errorf("failed to describe CloudWatch Metric Alarm '%s': %w", alarmName, err)
	}

	for _, alarm := range resp.MetricAlarms {
		if alarm.AlarmName != nil && *alarm.AlarmName == alarmName {
			if alarm.AlarmArn != nil {
				return *alarm.AlarmArn, true, nil // Found by name, return ARN
			}
			return *alarm.AlarmName, true, nil // Fallback to name if ARN is missing (unlikely)
		}
	}
	return "", false, nil // Alarm not found
}

// verifyIAMInstanceProfile checks if an IAM Instance Profile exists in AWS.
func (c *AWSClient) verifyIAMInstanceProfile(ctx context.Context, profileName string) (string, bool, error) {
	input := &iam.GetInstanceProfileInput{
		InstanceProfileName: aws.String(profileName),
	}
	resp, err := c.IAMClient.GetInstanceProfile(ctx, input)
	if err != nil {
		if strings.Contains(err.Error(), "NoSuchEntity") {
			return "", false, nil // Instance Profile not found
		}
		return "", false, fmt.Errorf("failed to get IAM Instance Profile '%s': %w", profileName, err)
	}

	if resp.InstanceProfile != nil && resp.InstanceProfile.Arn != nil {
		return *resp.InstanceProfile.Arn, true, nil // Found, return ARN
	}
	return "", false, nil // Not found or incomplete response
}

// verifyIAMRole checks if an IAM Role exists in AWS.
func (c *AWSClient) verifyIAMRole(ctx context.Context, roleName string) (string, bool, error) {
	input := &iam.GetRoleInput{
		RoleName: aws.String(roleName),
	}
	resp, err := c.IAMClient.GetRole(ctx, input)
	if err != nil {
		if strings.Contains(err.Error(), "NoSuchEntity") {
			return "", false, nil // Role not found
		}
		return "", false, fmt.Errorf("failed to get IAM Role '%s': %w", roleName, err)
	}

	if resp.Role != nil && resp.Role.Arn != nil {
		return *resp.Role.Arn, true, nil // Found, return ARN
	}
	return "", false, nil // Not found or incomplete response
}

// verifyIAMRolePolicy checks if an IAM Role Policy (inline) exists in AWS.
func (c *AWSClient) verifyIAMRolePolicy(ctx context.Context, roleName, policyName string) (string, bool, error) {
	if roleName == "" || policyName == "" {
		return "", false, fmt.Errorf("both role name and policy name must be provided for IAM role policy verification")
	}

	input := &iam.GetRolePolicyInput{
		RoleName:   aws.String(roleName),
		PolicyName: aws.String(policyName),
	}
	_, err := c.IAMClient.GetRolePolicy(ctx, input)
	if err != nil {
		if strings.Contains(err.Error(), "NoSuchEntity") {
			return "", false, nil // Policy not found for the given role
		}
		return "", false, fmt.Errorf("failed to get IAM Role Policy '%s' for Role '%s': %w", policyName, roleName, err)
	}

	// If no error and we reached here, the policy exists.
	// The "ID" for an inline policy is typically a combination of role and policy name.
	return fmt.Sprintf("%s/%s", roleName, policyName), true, nil
}

// verifyLambdaFunction checks if a Lambda Function exists in AWS.
func (c *AWSClient) verifyLambdaFunction(ctx context.Context, functionName string) (string, bool, error) {
	input := &lambda.GetFunctionInput{
		FunctionName: aws.String(functionName),
	}
	resp, err := c.LambdaClient.GetFunction(ctx, input)
	if err != nil {
		if strings.Contains(err.Error(), "ResourceNotFoundException") {
			return "", false, nil // Function not found
		}
		return "", false, fmt.Errorf("failed to get Lambda Function '%s': %w", functionName, err)
	}

	if resp.Configuration != nil && resp.Configuration.FunctionArn != nil {
		return *resp.Configuration.FunctionArn, true, nil // Found, return ARN
	}
	return "", false, nil // Not found or incomplete response
}

// verifyLambdaPermission checks if a Lambda Permission exists in AWS.
func (c *AWSClient) verifyLambdaPermission(ctx context.Context, functionName, statementID string) (string, bool, error) {
	if functionName == "" || statementID == "" {
		return "", false, fmt.Errorf("both function name and statement ID must be provided for Lambda permission verification")
	}

	input := &lambda.GetPolicyInput{
		FunctionName: aws.String(functionName),
	}
	resp, err := c.LambdaClient.GetPolicy(ctx, input)
	if err != nil {
		if strings.Contains(err.Error(), "ResourceNotFoundException") {
			return "", false, nil // Policy (and thus permission) not found
		}
		return "", false, fmt.Errorf("failed to get policy for Lambda Function '%s': %w", functionName, err)
	}

	if resp.Policy != nil {
		var policy struct {
			Statement []struct {
				Sid string `json:"Sid"`
			} `json:"Statement"`
		}
		if err := json.Unmarshal([]byte(*resp.Policy), &policy); err != nil {
			return "", false, fmt.Errorf("failed to parse policy JSON for Lambda Function '%s': %w", functionName, err)
		}

		for _, stmt := range policy.Statement {
			if stmt.Sid == statementID {
				// The "ID" for a Lambda permission is its Statement ID (Sid)
				return statementID, true, nil
			}
		}
	}
	return "", false, nil // Permission (statement ID) not found in policy
}

// verifyCloudFrontDistribution checks if a CloudFront Distribution exists in AWS.
func (c *AWSClient) verifyCloudFrontDistribution(ctx context.Context, distributionID string) (string, bool, error) {
	input := &cloudfront.GetDistributionInput{
		Id: aws.String(distributionID),
	}
	resp, err := c.CloudFrontClient.GetDistribution(ctx, input)
	if err != nil {
		if strings.Contains(err.Error(), "NoSuchDistribution") {
			return "", false, nil // Distribution not found
		}
		return "", false, fmt.Errorf("failed to get CloudFront Distribution '%s': %w", distributionID, err)
	}

	if resp.Distribution != nil && resp.Distribution.ARN != nil {
		return *resp.Distribution.ARN, true, nil // Found, return ARN
	}
	return "", false, nil // Not found or incomplete response
}

// verifyCloudFrontOriginAccessIdentity checks if a CloudFront Origin Access Identity exists in AWS.
func (c *AWSClient) verifyCloudFrontOriginAccessIdentity(ctx context.Context, oaiID string) (string, bool, error) {
	input := &cloudfront.GetCloudFrontOriginAccessIdentityInput{
		Id: aws.String(oaiID),
	}
	resp, err := c.CloudFrontClient.GetCloudFrontOriginAccessIdentity(ctx, input)
	if err != nil {
		if strings.Contains(err.Error(), "NoSuchCloudFrontOriginAccessIdentity") {
			return "", false, nil // OAI not found
		}
		return "", false, fmt.Errorf("failed to get CloudFront Origin Access Identity '%s': %w", oaiID, err)
	}

	if resp.CloudFrontOriginAccessIdentity != nil && resp.CloudFrontOriginAccessIdentity.Id != nil {
		// OAI doesn't have an ARN in the same way other resources do, its ID is usually sufficient.
		return *resp.CloudFrontOriginAccessIdentity.Id, true, nil // Found, return ID
	}
	return "", false, nil // Not found or incomplete response
}

// verifyS3BucketPolicy checks if an S3 Bucket Policy exists for a bucket in AWS.
func (c *AWSClient) verifyS3BucketPolicy(ctx context.Context, bucketName string) (string, bool, error) {
	input := &s3.GetBucketPolicyInput{
		Bucket: aws.String(bucketName),
	}
	_, err := c.S3Client.GetBucketPolicy(ctx, input)
	if err != nil {
		if strings.Contains(err.Error(), "NoSuchBucketPolicy") {
			return "", false, nil // Policy not found
		}
		// A common error for GetBucketPolicy when the bucket itself doesn't exist
		// is "NotFound" or "NoSuchBucket". If the bucket is verified separately,
		// we can assume such errors indicate missing policy, but we'll include it for safety.
		if strings.Contains(err.Error(), "NotFound") || strings.Contains(err.Error(), "NoSuchBucket") || strings.Contains(err.Error(), "Forbidden") {
			return "", false, nil // Treat as not found (or access denied implies it might exist but we can't check)
		}
		return "", false, fmt.Errorf("failed to get S3 Bucket Policy for '%s': %w", bucketName, err)
	}
	// If no error, policy exists. The ID can be the bucket name itself for a policy.
	return bucketName, true, nil
}

// verifyS3BucketACL checks if an S3 Bucket ACL exists for a bucket in AWS.
func (c *AWSClient) verifyS3BucketACL(ctx context.Context, bucketName string) (string, bool, error) {
	input := &s3.GetBucketAclInput{
		Bucket: aws.String(bucketName),
	}
	_, err := c.S3Client.GetBucketAcl(ctx, input)
	if err != nil {
		// If we can't get the ACL due to permission issues or bucket not found, consider it not verifiable or non-existent for our purpose.
		if strings.Contains(err.Error(), "NotFound") || strings.Contains(err.Error(), "NoSuchBucket") || strings.Contains(err.Error(), "AccessDenied") {
			return "", false, nil // Cannot verify ACL, treat as not found for reconciliation purposes
		}
		return "", false, fmt.Errorf("failed to get S3 Bucket ACL for '%s': %w", bucketName, err)
	}
	// If no error, the ACL exists. The ID can be the bucket name.
	return bucketName, true, nil
}

// verifyS3BucketOwnershipControls checks if S3 Bucket Ownership Controls exist for a bucket in AWS.
func (c *AWSClient) verifyS3BucketOwnershipControls(ctx context.Context, bucketName string) (string, bool, error) {
	input := &s3.GetBucketOwnershipControlsInput{
		Bucket: aws.String(bucketName),
	}
	_, err := c.S3Client.GetBucketOwnershipControls(ctx, input)
	if err != nil {
		if strings.Contains(err.Error(), "OwnershipControlsNotFoundError") {
			return "", false, nil // Ownership controls not explicitly configured
		}
		if strings.Contains(err.Error(), "NotFound") || strings.Contains(err.Error(), "NoSuchBucket") || strings.Contains(err.Error(), "AccessDenied") {
			return "", false, nil
		}
		return "", false, fmt.Errorf("failed to get S3 Bucket Ownership Controls for '%s': %w", bucketName, err)
	}
	return bucketName, true, nil
}

// verifyS3BucketPublicAccessBlock checks if S3 Bucket Public Access Block exists for a bucket in AWS.
func (c *AWSClient) verifyS3BucketPublicAccessBlock(ctx context.Context, bucketName string) (string, bool, error) {
	input := &s3.GetPublicAccessBlockInput{
		Bucket: aws.String(bucketName),
	}
	_, err := c.S3Client.GetPublicAccessBlock(ctx, input)
	if err != nil {
		if strings.Contains(err.Error(), "NoSuchPublicAccessBlockConfiguration") {
			return "", false, nil // Public Access Block not explicitly configured
		}
		if strings.Contains(err.Error(), "NotFound") || strings.Contains(err.Error(), "NoSuchBucket") || strings.Contains(err.Error(), "AccessDenied") {
			return "", false, nil
		}
		return "", false, fmt.Errorf("failed to get S3 Bucket Public Access Block for '%s': %w", bucketName, err)
	}
	return bucketName, true, nil
}

// verifyS3BucketWebsiteConfiguration checks if S3 Bucket Website Configuration exists for a bucket in AWS.
func (c *AWSClient) verifyS3BucketWebsiteConfiguration(ctx context.Context, bucketName string) (string, bool, error) {
	input := &s3.GetBucketWebsiteInput{
		Bucket: aws.String(bucketName),
	}
	_, err := c.S3Client.GetBucketWebsite(ctx, input)
	if err != nil {
		if strings.Contains(err.Error(), "NoSuchWebsiteConfiguration") {
			return "", false, nil // Website configuration not found
		}
		if strings.Contains(err.Error(), "NotFound") || strings.Contains(err.Error(), "NoSuchBucket") || strings.Contains(err.Error(), "AccessDenied") {
			return "", false, nil
		}
		return "", false, fmt.Errorf("failed to get S3 Bucket Website Configuration for '%s': %w", bucketName, err)
	}
	return bucketName, true, nil
}

// verifyS3BucketCORSConfiguration checks if S3 Bucket CORS Configuration exists for a bucket in AWS.
func (c *AWSClient) verifyS3BucketCORSConfiguration(ctx context.Context, bucketName string) (string, bool, error) {
	input := &s3.GetBucketCorsInput{
		Bucket: aws.String(bucketName),
	}
	_, err := c.S3Client.GetBucketCors(ctx, input)
	if err != nil {
		if strings.Contains(err.Error(), "NoSuchCORSConfiguration") {
			return "", false, nil // CORS configuration not found
		}
		if strings.Contains(err.Error(), "NotFound") || strings.Contains(err.Error(), "NoSuchBucket") || strings.Contains(err.Error(), "AccessDenied") {
			return "", false, nil
		}
		return "", false, fmt.Errorf("failed to get S3 Bucket CORS Configuration for '%s': %w", bucketName, err)
	}
	return bucketName, true, nil
}

// verifyS3BucketNotification checks if S3 Bucket Notification Configuration exists for a bucket in AWS.
func (c *AWSClient) verifyS3BucketNotification(ctx context.Context, bucketName string) (string, bool, error) {
	input := &s3.GetBucketNotificationConfigurationInput{ // CORRECTED: Changed Request to Input
		Bucket: aws.String(bucketName),
	}
	resp, err := c.S3Client.GetBucketNotificationConfiguration(ctx, input)
	if err != nil {
		// GetBucketNotificationConfiguration doesn't return a "NotFound" error
		// if no configuration exists; it returns an empty configuration.
		// So, if there's any actual error, it's an API problem.
		if strings.Contains(err.Error(), "NotFound") || strings.Contains(err.Error(), "NoSuchBucket") || strings.Contains(err.Error(), "AccessDenied") {
			// If the bucket itself is not found, or access is denied, we can't check its notification config.
			// Treat this as not found for reconciliation purposes.
			return "", false, nil
		}
		return "", false, fmt.Errorf("failed to get S3 Bucket Notification Configuration for '%s': %w", bucketName, err)
	}

	// An empty configuration (meaning all lists are zero-length) is treated as "not found"
	// from Terraform's perspective if it expects one to exist.
	if resp != nil && (len(resp.LambdaFunctionConfigurations) > 0 || len(resp.QueueConfigurations) > 0 || len(resp.TopicConfigurations) > 0) {
		return bucketName, true, nil
	}
	return "", false, nil // No notification configuration found
}

// verifyS3Object checks if an S3 Object exists in AWS.
func (c *AWSClient) verifyS3Object(ctx context.Context, bucketName, key string) (string, bool, error) {
	input := &s3.HeadObjectInput{
		Bucket: aws.String(bucketName),
		Key:    aws.String(key),
	}
	_, err := c.S3Client.HeadObject(ctx, input)
	if err != nil {
		if strings.Contains(err.Error(), "NotFound") || strings.Contains(err.Error(), "NoSuchKey") {
			return "", false, nil // Object not found
		}
		if strings.Contains(err.Error(), "Forbidden") || strings.Contains(err.Error(), "AccessDenied") {
			log.Printf("WARNING: Access denied to S3 object 's3://%s/%s'. Assuming it exists.", bucketName, key)
			return key, true, nil // Cannot verify, but don't error out completely
		}
		return "", false, fmt.Errorf("failed to check S3 Object 's3://%s/%s': %w", bucketName, key, err)
	}
	return key, true, nil // Object found
}

// verifyECS_Service checks if an ECS Service exists in AWS.
func (c *AWSClient) verifyECSService(ctx context.Context, clusterName, serviceName string) (string, bool, error) {
	if clusterName == "" || serviceName == "" {
		return "", false, fmt.Errorf("both cluster name and service name must be provided for ECS service verification")
	}

	input := &ecs.DescribeServicesInput{
		Cluster:  aws.String(clusterName),
		Services: []string{serviceName},
	}
	resp, err := c.ECSClient.DescribeServices(ctx, input)
	if err != nil {
		if strings.Contains(err.Error(), "ClusterNotFoundException") {
			return "", false, fmt.Errorf("ECS cluster '%s' not found for service verification: %w", clusterName, err)
		}
		// DescribeServices returns an empty slice for services not found within an existing cluster
		return "", false, fmt.Errorf("failed to describe ECS Service '%s' in cluster '%s': %w", serviceName, clusterName, err)
	}

	if len(resp.Services) > 0 && resp.Services[0].ServiceArn != nil {
		return *resp.Services[0].ServiceArn, true, nil
	}
	return "", false, nil // Service not found
}

// verifyECS_TaskDefinition checks if an ECS Task Definition exists in AWS.
func (c *AWSClient) verifyECSTaskDefinition(ctx context.Context, taskDefinitionARN string) (string, bool, error) {
	if taskDefinitionARN == "" {
		return "", false, fmt.Errorf("task definition ARN must be provided for ECS task definition verification")
	}

	input := &ecs.DescribeTaskDefinitionInput{
		TaskDefinition: aws.String(taskDefinitionARN),
	}
	resp, err := c.ECSClient.DescribeTaskDefinition(ctx, input)
	if err != nil {
		if strings.Contains(err.Error(), "ClientException") && strings.Contains(err.Error(), "No task definition found") {
			return "", false, nil // Task definition not found
		}
		return "", false, fmt.Errorf("failed to describe ECS Task Definition '%s': %w", taskDefinitionARN, err)
	}

	if resp.TaskDefinition != nil && resp.TaskDefinition.TaskDefinitionArn != nil {
		return *resp.TaskDefinition.TaskDefinitionArn, true, nil
	}
	return "", false, nil // Task definition not found or incomplete response
}

// verifyLBLIstenerCertificate checks if an ELBv2 Listener Certificate exists in AWS.
func (c *AWSClient) verifyLBListenerCertificate(ctx context.Context, listenerARN, certificateARN string) (string, bool, error) {
	if listenerARN == "" || certificateARN == "" {
		return "", false, fmt.Errorf("both listener ARN and certificate ARN must be provided for LB listener certificate verification")
	}

	input := &elasticloadbalancingv2.DescribeListenerCertificatesInput{
		ListenerArn: aws.String(listenerARN),
	}
	resp, err := c.ELBV2Client.DescribeListenerCertificates(ctx, input)
	if err != nil {
		if strings.Contains(err.Error(), "ListenerNotFound") {
			return "", false, fmt.Errorf("ELB listener '%s' not found for certificate verification: %w", listenerARN, err)
		}
		// DescribeListenerCertificates returns an empty slice if no certificates are associated
		return "", false, fmt.Errorf("failed to describe ELB Listener Certificates for Listener '%s': %w", listenerARN, err)
	}

	// Iterate through the returned certificates to find a match for certificateARN
	for _, cert := range resp.Certificates {
		if cert.CertificateArn != nil && *cert.CertificateArn == certificateARN {
			return *cert.CertificateArn, true, nil // Found a matching certificate
		}
	}

	return "", false, nil // Listener certificate not found
}
