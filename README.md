# Reconcile Terraform State

This utility will take a **Terraform State File** and perform a validation/fix check on the state file in order to
determine whether there are missing or invalid resources in the state file. The script will then provide you with
the list of commands that are required to execute in order to address the terraform apply errors. This script is 
designed to address problems that `terraform plan` cannot identify, but only show up in `terraform apply`. 

## Installation

```bash
go install github.com/andreimerlescu/reconcile-tfstate@latest
```

### Basic Usage

```bash
which reconcile-tfstate
reconcile-tfstate -h
```

## Examples

### S3 Resources

```bash
reconcile-tfstate  -s3-state s3://acme-terraform-tfstate/state/terraform.tfstate
```

### File Resources

```bash
stat terraform.tfstate
# 16777234 12069336 -rw-r--r-- 1 andrei staff 0 185972 "Jul 18 13:45:17 2025" "Jul 18 13:45:16 2025" "Jul 18 13:45:16 2025" "Jul 18 13:45:16 2025" 4096 368 0 dev.tfstate
reconcile-tfstate -state dev.tfstate
```

## Output

Command executed:

```bash
reconcile-tfstate  -s3-state s3://acme-terraform-tfstate/state/terraform.tfstate
```

The output of that in the `STDOUT` is: 

```log
Downloading state from s3://acme-terraform-tfstate/state/terraform.tfstate to /var/folders/5w/qrhs5nfd58zcpz56ddt1mqsw0000gn/T/tfstate-download-309601416.tfstate...
Download complete.
--- Terraform State Reconciliation Report ---
State File: /var/folders/5w/qrhs5nfd58zcpz56ddt1mqsw0000gn/T/tfstate-download-309601416.tfstate (State Version: 4, Terraform Version: 1.3.7)
AWS Region: us-west-2
Concurrency: 10
-------------------------------------------

--- INFO Results (9) ---
INFO: Data/Local resource 'module.ecs_cluster.aws_caller_identity.current'. No external verification needed.

--- OK Results (54) ---
OK: aws_cloudwatch_log_group.svc (ID: terraform-acme-terraform) exists in state and AWS.

--- WARNING Results (173) ---
WARNING: Resource type 'aws_instance' not supported by this checker. Manual verification needed.

--- POTENTIAL IMPORT Results (2) ---
POTENTIAL_IMPORT: module.acme-registry.aws_route53_zone.domain exists in AWS with ID '/hostedzone/Z2338249CVK734QUI1X3'. State ID: 'Z4824184CVK713QUIOX2'.

--- RUN THESE COMMANDS (2) ---
   terraform import module.acme-registry.aws_route53_zone.domain /hostedzone/Z4824184CVK713QUIOX2

--- S3 STATE FILE UPLOAD INSTRUCTION ---
After you have executed the `terraform import` and `terraform state rm` commands above, your local state 
file '/var/folders/5w/qrhs5nfd58zcpz56ddt1mqsw0000gn/T/tfstate-download-309601416.tfstate' will be modified. 
To upload the updated state file back to S3 (preserving history with versioning), run:

   aws s3 cp /var/folders/5w/qrhs5nfd58zcpz56ddt1mqsw0000gn/T/tfstate-download-309601416.tfstate \
          s3://acme-terraform-tfstate/state/terraform.tfstate \
          --metadata-directive REPLACE \
          --acl bucket-owner-full-control
   
NOTE: The `--metadata-directive REPLACE` and `--acl bucket-owner-full-control` ensure existing metadata is replaced and 
proper ownership is maintained. Adjust ACL as per your bucket policy.

--- End of Report ---
NOTE: This tool covers only a few resource types. Extend 'processResourceInstance' for full coverage.

```


