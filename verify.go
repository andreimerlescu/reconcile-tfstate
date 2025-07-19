package main

import (
	"context"
	"fmt"
	"log"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/cloudwatchlogs"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	"github.com/aws/aws-sdk-go-v2/service/elasticloadbalancingv2"
	"github.com/aws/aws-sdk-go-v2/service/route53"
	"github.com/aws/aws-sdk-go-v2/service/s3"
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
			if *zone.Name == zoneName || *zone.Name == zoneName+"." {
				return *zone.Id, true, nil
			}
		}
		return "", false, nil
	}
	return "", false, fmt.Errorf("%s", "Route53 zone ID or name must be provided for verification")
}

// verifyLoadBalancer checks if an ELBv2 Load Balancer exists in AWS
func (c *AWSClient) verifyLoadBalancer(ctx context.Context, lbARN, lbName string, currentRegion string) (string, bool, error) {
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
		// Check for region mismatch error
		if strings.Contains(err.Error(), "ValidationError") && strings.Contains(err.Error(), "is not a valid load balancer ARN") {
			arnRegion := extractRegionFromARN(lbARN)
			if arnRegion != "" && arnRegion != currentRegion {
				return "", false, fmt.Errorf("region mismatch: ARN '%s' claims region '%s', but checking in '%s'", lbARN, arnRegion, currentRegion)
			}
		}
		return "", false, fmt.Errorf("failed to describe Load Balancer '%s' (ARN: '%s'): %w", lbName, lbARN, err)
	}

	if len(resp.LoadBalancers) > 0 {
		return *resp.LoadBalancers[0].LoadBalancerArn, true, nil
	}
	return "", false, nil
}

// verifyListener checks if an ELBv2 Listener exists in AWS
func (c *AWSClient) verifyListener(ctx context.Context, listenerARN, lbARN string, currentRegion string) (string, bool, error) {
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
		// Check for region mismatch error
		if strings.Contains(err.Error(), "ValidationError") && strings.Contains(err.Error(), "is not a valid listener ARN") {
			arnRegion := extractRegionFromARN(listenerARN)
			if arnRegion != "" && arnRegion != currentRegion {
				return "", false, fmt.Errorf("region mismatch: ARN '%s' claims region '%s', but checking in '%s'", listenerARN, arnRegion, currentRegion)
			}
		}
		return "", false, fmt.Errorf("failed to describe Listener '%s' (LB ARN: '%s'): %w", listenerARN, lbARN, err)
	}

	if len(resp.Listeners) > 0 {
		return *resp.Listeners[0].ListenerArn, true, nil
	}
	return "", false, nil
}

// verifyTargetGroup checks if an ELBv2 Target Group exists in AWS
func (c *AWSClient) verifyTargetGroup(ctx context.Context, tgARN, tgName string, currentRegion string) (string, bool, error) {
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
		if strings.Contains(err.Error(), "ValidationError") && strings.Contains(err.Error(), "is not a valid target group ARN") {
			arnRegion := extractRegionFromARN(tgARN)
			if arnRegion != "" && arnRegion != currentRegion {
				return "", false, fmt.Errorf("region mismatch: ARN '%s' claims region '%s', but checking in '%s'", tgARN, arnRegion, currentRegion)
			}
		}
		return "", false, fmt.Errorf("failed to describe Target Group '%s' (ARN: '%s'): %w", tgName, tgARN, err)
	}

	if len(resp.TargetGroups) > 0 {
		return *resp.TargetGroups[0].TargetGroupArn, true, nil
	}
	return "", false, nil
}

// verifyListenerRule checks if an ELBv2 Listener Rule exists in AWS
func (c *AWSClient) verifyListenerRule(ctx context.Context, ruleARN, listenerARN string, currentRegion string) (string, bool, error) {
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
		if strings.Contains(err.Error(), "ValidationError") && strings.Contains(err.Error(), "is not a valid rule ARN") {
			arnRegion := extractRegionFromARN(ruleARN)
			if arnRegion != "" && arnRegion != currentRegion {
				return "", false, fmt.Errorf("region mismatch: ARN '%s' claims region '%s', but checking in '%s'", ruleARN, arnRegion, currentRegion)
			}
		}
		return "", false, fmt.Errorf("failed to describe Listener Rule '%s' (Listener ARN: '%s'): %w", ruleARN, listenerARN, err)
	}

	if len(resp.Rules) > 0 {
		return *resp.Rules[0].RuleArn, true, nil
	}
	return "", false, nil
}
