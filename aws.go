package main

import (
	"context"
	"fmt"
	"strings"

	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/feature/s3/manager"
	"github.com/aws/aws-sdk-go-v2/service/acm"
	"github.com/aws/aws-sdk-go-v2/service/autoscaling"
	"github.com/aws/aws-sdk-go-v2/service/cloudfront"
	"github.com/aws/aws-sdk-go-v2/service/cloudwatch"
	"github.com/aws/aws-sdk-go-v2/service/cloudwatchlogs"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	"github.com/aws/aws-sdk-go-v2/service/ecs"
	"github.com/aws/aws-sdk-go-v2/service/elasticloadbalancingv2"
	"github.com/aws/aws-sdk-go-v2/service/iam"
	"github.com/aws/aws-sdk-go-v2/service/lambda"
	"github.com/aws/aws-sdk-go-v2/service/route53"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/secretsmanager"
	"github.com/aws/aws-sdk-go-v2/service/ssm"
)

// NewAWSClient initializes and returns AWS service clients
func NewAWSClient(ctx context.Context, region string) (*AWSClient, error) {
	cfg, err := config.LoadDefaultConfig(ctx, config.WithRegion(region))
	if err != nil {
		return nil, fmt.Errorf("failed to load AWS SDK config: %w", err)
	}

	return &AWSClient{
		S3Client:             s3.NewFromConfig(cfg),
		CloudWatchLogsClient: cloudwatchlogs.NewFromConfig(cfg),
		EC2Client:            ec2.NewFromConfig(cfg),
		Route53Client:        route53.NewFromConfig(cfg),
		ELBV2Client:          elasticloadbalancingv2.NewFromConfig(cfg),
		S3Downloader:         manager.NewDownloader(s3.NewFromConfig(cfg)),
		ACMClient:            acm.NewFromConfig(cfg),
		SSMClient:            ssm.NewFromConfig(cfg),
		SecretsManagerClient: secretsmanager.NewFromConfig(cfg),
		ECSClient:            ecs.NewFromConfig(cfg),
		AutoscalingClient:    autoscaling.NewFromConfig(cfg),
		CloudWatchClient:     cloudwatch.NewFromConfig(cfg),
		IAMClient:            iam.NewFromConfig(cfg),
		LambdaClient:         lambda.NewFromConfig(cfg),
		CloudFrontClient:     cloudfront.NewFromConfig(cfg),
	}, nil
}

// extractRegionFromARN attempts to parse the region from an AWS ARN.
// Returns an empty string if parsing fails.
func extractRegionFromARN(arn string) string {
	parts := strings.Split(arn, ":")
	if len(parts) >= 4 {
		return parts[3] // ARN format: arn:partition:service:region:account-id:resource
	}
	return ""
}
