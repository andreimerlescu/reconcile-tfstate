// verify.go (updated to implement verifySecurityGroupRule directly using SecurityGroupRuleIds)
package main

import (
	"context"
	"fmt"
	"log"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/acm"
	"github.com/aws/aws-sdk-go-v2/service/acm/types"
	"github.com/aws/aws-sdk-go-v2/service/cloudwatchlogs"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	ec2types "github.com/aws/aws-sdk-go-v2/service/ec2/types"
	"github.com/aws/aws-sdk-go-v2/service/ecs"
	"github.com/aws/aws-sdk-go-v2/service/elasticloadbalancingv2"
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
