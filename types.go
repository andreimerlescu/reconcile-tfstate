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
	// Order: string (16) > int (8) > bool (1)
	Config struct {
		StateFilePath       string
		S3State             string
		S3Bucket            string
		S3Key               string
		BackupsDir          string
		AWSRegion           string
		TerraformWorkingDir string // NEW: Field for Terraform's working directory
		Concurrency         int
		ExecuteCommands     bool
		ShowVersion         bool
		IsS3State           bool
		JsonOutput          bool
	}

	// ResourceStatus represents the status of a resource after checking AWS
	// Order: error (16) > string (16) > bool (1)
	ResourceStatus struct {
		Error            error  // interface (16 bytes)
		TerraformAddress string // (16 bytes)
		Message          string // (16 bytes)
		Command          string // (16 bytes)
		Kind             string // (16 bytes)
		StateID          string // (16 bytes)
		LiveID           string // (16 bytes)
		TFID             string // (16 bytes)
		AWSID            string // (16 bytes)
		Stdout           string // (16 bytes)
		Stderr           string // (16 bytes)
		Category         string // RE-ADDED: (16 bytes)
		ExistsInAWS      bool   // (1 byte)
	}

	// AWSClient holds all necessary AWS service clients
	// Order: pointers (8 bytes) grouped
	AWSClient struct {
		S3Client             *s3.Client
		CloudWatchLogsClient *cloudwatchlogs.Client
		EC2Client            *ec2.Client
		Route53Client        *route53.Client
		ELBV2Client          *elasticloadbalancingv2.Client
		ACMClient            *acm.Client
		SSMClient            *ssm.Client
		SecretsManagerClient *secretsmanager.Client
		ECSClient            *ecs.Client
		AutoscalingClient    *autoscaling.Client
		CloudWatchClient     *cloudwatch.Client
		IAMClient            *iam.Client
		LambdaClient         *lambda.Client
		CloudFrontClient     *cloudfront.Client
		S3Downloader         *manager.Downloader // This is a struct pointer itself, so effectively 8 bytes here
	}

	// TFStateFile represents the contents of a Terraform state file.
	// Order: map (8) / slice (24) > uint64 (8) > string (16)
	TFStateFile struct {
		RootOutputs      map[string]OutputStateV4 `json:"outputs"`                     // (8 bytes for map header)
		Resources        []ResourceStateV4        `json:"resources"`                   // (24 bytes for slice header)
		CheckResults     []CheckResultsV4         `json:"check_results,omitempty"`     // (24 bytes for slice header)
		Version          uint64                   `json:"version"`                     // (8 bytes)
		Serial           uint64                   `json:"serial"`                      // (8 bytes)
		TerraformVersion string                   `json:"terraform_version,omitempty"` // (16 bytes)
		Lineage          string                   `json:"lineage"`                     // (16 bytes)
	}

	// OutputStateV4 is the state of a single output variable.
	// Order: json.RawMessage (24) > bool (1)
	OutputStateV4 struct {
		ValueRaw     json.RawMessage `json:"value"`
		ValueTypeRaw json.RawMessage `json:"type"`
		Sensitive    bool            `json:"sensitive,omitempty"`
	}

	// ResourceStateV4 is the state of a single resource.
	// Order: slice (24) > string (16)
	ResourceStateV4 struct {
		Instances      []InstanceObjectStateV4 `json:"instances"` // (24 bytes for slice header)
		Module         string                  `json:"module,omitempty"`
		Type           string                  `json:"type"`
		Name           string                  `json:"name"`
		EachMode       string                  `json:"each,omitempty"`
		ProviderConfig string                  `json:"provider"`
		Mode           string                  `json:"mode"` // RE-ADDED: (16 bytes)
	}

	// InstanceObjectStateV4 is the state of a single instance of a resource.
	// Order: json.RawMessage (24) > []byte (24) > map (8) > interface{} (16) > uint64 (8) > string (16) > bool (1)
	InstanceObjectStateV4 struct {
		AttributesRaw           json.RawMessage   `json:"attributes,omitempty"`            // (24 bytes)
		AttributeSensitivePaths json.RawMessage   `json:"sensitive_attributes,omitempty"`  // (24 bytes)
		PrivateRaw              []byte            `json:"private,omitempty"`               // (24 bytes)
		Dependencies            []string          `json:"dependencies,omitempty"`          // (24 bytes)
		IndexKey                interface{}       `json:"index_key,omitempty"`             // (16 bytes)
		Status                  string            `json:"status,omitempty"`                // (16 bytes)
		Deposed                 string            `json:"deposed,omitempty"`               // (16 bytes)
		AttributesFlat          map[string]string `json:"attributes_flat,omitempty"`       // (8 bytes for map header)
		SchemaVersion           uint64            `json:"schema_version"`                  // (8 bytes)
		CreateBeforeDestroy     bool              `json:"create_before_destroy,omitempty"` // (1 byte)
	}

	// CheckResultsV4 is the results of a single check block.
	// Order: slice (24) > string (16)
	CheckResultsV4 struct {
		Objects    []CheckResultsObjectV4 `json:"objects"` // (24 bytes for slice header)
		ObjectKind string                 `json:"object_kind"`
		ConfigAddr string                 `json:"config_addr"`
		Status     string                 `json:"status"`
	}

	// CheckResultsObjectV4 is the result of a single object within a check block.
	// Order: slice (24) > string (16)
	CheckResultsObjectV4 struct {
		FailureMessages []string `json:"failure_messages,omitempty"` // (24 bytes for slice header)
		ObjectAddr      string   `json:"object_addr"`
		Status          string   `json:"status"`
	}

	// StateVersionV4 is a weird special type we use to produce our hard-coded
	// "version": 4 in the JSON serialization. (No fields to sort)
	StateVersionV4 struct{}

	// StateFileV4 is the internal representation of a state file at version 4.
	// Order: maps/slices > uint64 > string
	StateFileV4 struct {
		RootOutputs      map[string]OutputStateV4 `json:"outputs"`
		Resources        []ResourceStateV4        `json:"resources"`
		CheckResults     []CheckResultsV4         `json:"check_results"`
		Serial           uint64                   `json:"serial"`
		Version          StateVersionV4           `json:"version"` // StateVersionV4 is a struct, but effectively small
		TerraformVersion string                   `json:"terraform_version"`
		Lineage          string                   `json:"lineage"`
	}

	// categorizedResults holds slices of ResourceStatus for each category.
	// Order: slices (24) > string (16)
	categorizedResults struct {
		InfoResults            []ResourceStatus      // (24 bytes)
		OkResults              []ResourceStatus      // (24 bytes)
		WarningResults         []ResourceStatus      // (24 bytes)
		ErrorResults           []ResourceStatus      // (24 bytes)
		PotentialImportResults []ResourceStatus      // (24 bytes)
		DangerousResults       []ResourceStatus      // (24 bytes)
		RegionMismatchResults  []ResourceStatus      // (24 bytes)
		RunCommands            []string              // (24 bytes)
		CommandExecutionLogs   []CommandExecutionLog // (24 bytes)
		ApplicationError       string                `json:"application_error,omitempty"` // (16 bytes)
	}

	// CommandExecutionLog
	// Order: string (16) > int (8)
	CommandExecutionLog struct {
		Command  string `json:"command"`
		Stdout   string `json:"stdout"`
		Stderr   string `json:"stderr"`
		Error    string `json:"error,omitempty"` // interface (16 bytes)
		ExitCode int    `json:"exit_code"`
	}

	// JSONBackupPaths
	// Order: string (16)
	JSONBackupPaths struct {
		OriginalPath       string `json:"original_path"`
		OriginalChecksum   string `json:"original_checksum"`
		NewPath            string `json:"new_path"`
		NewChecksum        string `json:"new_checksum"`
		ReportPath         string `json:"report_path"`
		ReportChecksum     string `json:"report_checksum"`
		JsonReportPath     string `json:"json_report_path"`
		JsonReportChecksum string `json:"json_report_checksum"`
	}

	// JSONResultItem
	// Order: string (16)
	JSONResultItem struct {
		Resource string `json:"resource"`
		Command  string `json:"command"`
		Kind     string `json:"kind"`
		TFID     string `json:"tf_id"`
		AWSID    string `json:"aws_id"`
		Stdout   string `json:"stdout"`
		Stderr   string `json:"stderr"`
	}

	// JSONResults
	// Order: slice (24)
	JSONResults struct {
		InfoResults            []JSONResultItem `json:"INFO"`
		OkResults              []JSONResultItem `json:"OK"`
		PotentialImportResults []JSONResultItem `json:"POTENTIAL_IMPORT"`
		RegionMismatchResults  []JSONResultItem `json:"REGION_MISMATCH"`
		WarningResults         []JSONResultItem `json:"WARNING"`
		ErrorResults           []JSONResultItem `json:"ERROR"`
		DangerousResults       []JSONResultItem `json:"DANGEROUS"`
	}

	// JSONOutput
	// Order: slices (24) > maps (8) > string (16) > uint64 (8) > int (8)
	JSONOutput struct {
		ExecutionLogs    []CommandExecutionLog `json:"execution_logs"` // (24 bytes)
		Commands         []string              `json:"commands"`       // (24 bytes)
		Results          JSONResults           `json:"results"`        // (struct containing slices, effectively large)
		State            string                `json:"state"`
		StateChecksum    string                `json:"state_checksum"`
		Region           string                `json:"region"`
		LocalStateFile   string                `json:"local_statefile"`
		TFVersion        string                `json:"tf_version"`
		ApplicationError string                `json:"application_error,omitempty"` // (16 bytes)
		Backup           JSONBackupPaths       `json:"backup"`                      // (struct containing strings, effectively large)
		StateVersion     uint64                `json:"state_version"`               // (8 bytes)
		Concurrency      int                   `json:"concurrency"`                 // (8 bytes)
	}
)
