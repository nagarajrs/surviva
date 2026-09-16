variable "aws_region" {
  description = "AWS region to deploy the orchestrator and job status table in."
  type        = string
  default     = "us-east-1"
}

variable "name_prefix" {
  description = "Prefix applied to every resource this module creates."
  type        = string
  default     = "surviva"
}

variable "launch_template_id" {
  description = "Launch template the replacement instance is launched from. Must produce an instance with the surviva instance role/profile this module outputs (instance_profile_arn) attached, and with CRIU/kernel compatible with the original checkpointed instances."
  type        = string
}

variable "launch_template_version" {
  description = "Launch template version to launch the replacement instance from."
  type        = string
  default     = "$Latest"
}

variable "az_subnet_map" {
  description = "Availability zone -> subnet id, used to pin the replacement instance's subnet when an EBS checkpoint volume constrains it to a specific AZ (EBS volumes can't cross AZs). Leave empty ({}) if every job only ever uses S3 storage, or if the launch template's own subnet already fixes a single AZ shared with the checkpoint volumes."
  type        = map(string)
  default     = {}
}

variable "checkpoint_wait_seconds" {
  description = "How long the orchestrator waits after being triggered before querying job status, giving surviva's daemon time to finish checkpointing. Should be at least the daemon's -max-concurrent-checkpoints job timeout; defaults to slightly more than surviva's internal 100s per-job checkpoint timeout."
  type        = number
  default     = 110
}

variable "ebs_attach_device" {
  description = "Device name checkpoint EBS volumes are attached as on the replacement instance."
  type        = string
  default     = "/dev/sdf"
}

variable "replacement_market_type" {
  description = "Purchasing option the orchestrator launches the replacement instance with. \"on-demand\" (default) avoids the replacement itself being immediately Spot-interruptible again right after a restore, at the cost of that workload no longer running on Spot going forward. \"spot\" keeps the cost saving but accepts the risk of a repeated interruption/restore cycle."
  type        = string
  default     = "on-demand"
  validation {
    condition     = contains(["on-demand", "spot"], var.replacement_market_type)
    error_message = "replacement_market_type must be \"on-demand\" or \"spot\"."
  }
}

variable "restore_ssm_document" {
  description = "SSM document used to run the restore command on the replacement instance."
  type        = string
  default     = "AWS-RunShellScript"
}

variable "restore_command_prefix" {
  description = "Command prefix the orchestrator appends each restorable job's id to and sends via SSM, e.g. \"surviva restore\" produces \"surviva restore <job_id>\"."
  type        = string
  default     = "surviva restore"
}

variable "enable_s3_bucket" {
  description = "Whether to create an S3 bucket for the S3 checkpoint storage path. Leave false if every job uses EBS storage only."
  type        = bool
  default     = false
}

variable "tags" {
  description = "Tags applied to every resource this module creates."
  type        = map(string)
  default     = {}
}

variable "log_retention_days" {
  description = "How long CloudWatch keeps the surviva daemon/restore log group's events."
  type        = number
  default     = 14
}

variable "create_fis_demo_template" {
  description = "Whether to create a dormant AWS Fault Injection Simulator experiment template (plus its execution role) pre-configured to send a real Spot interruption to fis_target_instance_arn. Never auto-run by Terraform; fire it yourself with `aws fis start-experiment` whenever you're ready. Leave false for a normal deployment -- this is only useful for the ansible sandbox / manual validation."
  type        = bool
  default     = false
}

variable "fis_target_instance_arn" {
  description = "Instance ARN the FIS experiment template targets. Required when create_fis_demo_template is true."
  type        = string
  default     = ""
}

variable "fis_interruption_delay" {
  description = "ISO-8601 duration FIS waits after starting the experiment before sending the interruption notice (the aws:ec2:send-spot-instance-interruptions action's durationBeforeInterruption parameter)."
  type        = string
  default     = "PT2M"
}
