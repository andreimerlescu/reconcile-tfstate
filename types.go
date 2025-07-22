package main

import (
	"encoding/json"

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

type (
	// Config holds the application's runtime configuration.
	Config struct {
		StateFilePath   string
		AWSRegion       string
		Concurrency     int
		S3State         string
		ShowVersion     bool
		IsS3State       bool
		S3Bucket        string
		S3Key           string
		ExecuteCommands bool
		BackupsDir      string
		JsonOutput      bool // NEW: Flag for JSON output
	}

	// ResourceStatus represents the status of a resource after checking AWS
	ResourceStatus struct {
		TerraformAddress string
		StateID          string
		LiveID           string // The ID found in AWS
		ExistsInAWS      bool
		Error            error
		Category         string // INFO, OK, WARNING, ERROR, POTENTIAL_IMPORT, DANGEROUS, REGION_MISMATCH
		Message          string // The descriptive message
		Command          string // The terraform import/state rm command, if applicable
		Kind             string // "data" or "resource" for JSON output
		TFID             string // Terraform ID for JSON output
		AWSID            string // AWS ID for JSON output
		Stdout           string // Stdout from command execution (if any, currently empty)
		Stderr           string // Stderr from command execution (if any, currently empty)
	}

	// AWSClient holds all necessary AWS service clients
	AWSClient struct {
		S3Client             *s3.Client
		CloudWatchLogsClient *cloudwatchlogs.Client
		EC2Client            *ec2.Client
		Route53Client        *route53.Client
		ELBV2Client          *elasticloadbalancingv2.Client
		S3Downloader         *manager.Downloader
		ACMClient            *acm.Client
		SSMClient            *ssm.Client
		SecretsManagerClient *secretsmanager.Client
		ECSClient            *ecs.Client
		AutoscalingClient    *autoscaling.Client
		CloudWatchClient     *cloudwatch.Client
		IAMClient            *iam.Client
		LambdaClient         *lambda.Client
		CloudFrontClient     *cloudfront.Client
	}

	// TFStateFile represents the contents of a Terraform state file.
	TFStateFile struct {
		Version          uint64                   `json:"version"`
		TerraformVersion string                   `json:"terraform_version,omitempty"`
		Serial           uint64                   `json:"serial"`
		Lineage          string                   `json:"lineage"`
		RootOutputs      map[string]OutputStateV4 `json:"outputs"`
		Resources        []ResourceStateV4        `json:"resources"`
		CheckResults     []CheckResultsV4         `json:"check_results,omitempty"`
	}

	// OutputStateV4 is the state of a single output variable.
	OutputStateV4 struct {
		ValueRaw     json.RawMessage `json:"value"`
		ValueTypeRaw json.RawMessage `json:"type"`
		Sensitive    bool            `json:"sensitive,omitempty"`
	}

	// ResourceStateV4 is the state of a single resource.
	ResourceStateV4 struct {
		Module         string                  `json:"module,omitempty"`
		Mode           string                  `json:"mode"`
		Type           string                  `json:"type"`
		Name           string                  `json:"name"`
		EachMode       string                  `json:"each,omitempty"`
		ProviderConfig string                  `json:"provider"`
		Instances      []InstanceObjectStateV4 `json:"instances"`
	}

	// InstanceObjectStateV4 is the state of a single instance of a resource.
	InstanceObjectStateV4 struct {
		IndexKey interface{} `json:"index_key,omitempty"`
		Status   string      `json:"status,omitempty"`
		Deposed  string      `json:"deposed,omitempty"`

		SchemaVersion           uint64            `json:"schema_version"`
		AttributesRaw           json.RawMessage   `json:"attributes,omitempty"`
		AttributesFlat          map[string]string `json:"attributes_flat,omitempty"`
		AttributeSensitivePaths json.RawMessage   `json:"sensitive_attributes,omitempty"`

		PrivateRaw []byte `json:"private,omitempty"`

		Dependencies []string `json:"dependencies,omitempty"`

		CreateBeforeDestroy bool `json:"create_before_destroy,omitempty"`
	}

	// CheckResultsV4 is the results of a single check block.
	CheckResultsV4 struct {
		ObjectKind string                 `json:"object_kind"`
		ConfigAddr string                 `json:"config_addr"`
		Status     string                 `json:"status"`
		Objects    []CheckResultsObjectV4 `json:"objects"`
	}

	// CheckResultsObjectV4 is the result of a single object within a check block.
	CheckResultsObjectV4 struct {
		ObjectAddr      string   `json:"object_addr"`
		Status          string   `json:"status"`
		FailureMessages []string `json:"failure_messages,omitempty"`
	}

	// StateVersionV4 is a weird special type we use to produce our hard-coded
	// "version": 4 in the JSON serialization.
	StateVersionV4 struct{}

	// StateFileV4 is the internal representation of a state file at version 4.
	StateFileV4 struct {
		Version          StateVersionV4           `json:"version"`
		TerraformVersion string                   `json:"terraform_version"`
		Serial           uint64                   `json:"serial"`
		Lineage          string                   `json:"lineage"`
		RootOutputs      map[string]OutputStateV4 `json:"outputs"`
		Resources        []ResourceStateV4        `json:"resources"`
		CheckResults     []CheckResultsV4         `json:"check_results"`
	}

	// categorizedResults holds slices of ResourceStatus for each category.
	categorizedResults struct {
		InfoResults            []ResourceStatus
		OkResults              []ResourceStatus
		WarningResults         []ResourceStatus
		ErrorResults           []ResourceStatus
		PotentialImportResults []ResourceStatus
		DangerousResults       []ResourceStatus
		RegionMismatchResults  []ResourceStatus
		RunCommands            []string
	}

	JSONBackupPaths struct {
		OriginalPath     string `json:"original_path"`
		OriginalChecksum string `json:"original_checksum"`
		NewPath          string `json:"new_path"`
		NewChecksum      string `json:"new_checksum"`
		ReportPath       string `json:"report_path"`
		ReportChecksum   string `json:"report_checksum"`
	}

	JSONResultItem struct {
		Kind     string `json:"kind"`
		Resource string `json:"resource"`
		TFID     string `json:"tf_id"`
		AWSID    string `json:"aws_id"`
		Command  string `json:"command"`
		Stdout   string `json:"stdout"`
		Stderr   string `json:"stderr"`
	}

	JSONResults struct {
		InfoResults            []JSONResultItem `json:"INFO"`
		OkResults              []JSONResultItem `json:"OK"`
		PotentialImportResults []JSONResultItem `json:"POTENTIAL_IMPORT"`
		RegionMismatchResults  []JSONResultItem `json:"REGION_MISMATCH"`
		WarningResults         []JSONResultItem `json:"WARNING"`
		ErrorResults           []JSONResultItem `json:"ERROR"`
		DangerousResults       []JSONResultItem `json:"DANGEROUS"`
	}

	JSONOutput struct {
		State          string          `json:"state"`
		StateChecksum  string          `json:"state_checksum"`
		Region         string          `json:"region"`
		LocalStateFile string          `json:"local_statefile"`
		TFVersion      string          `json:"tf_version"`
		StateVersion   uint64          `json:"state_version"`
		Concurrency    int             `json:"concurrency"`
		Backup         JSONBackupPaths `json:"backup"`
		Commands       []string        `json:"commands"`
		Results        JSONResults     `json:"results"`
	}
)
