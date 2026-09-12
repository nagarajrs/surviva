output "jobs_table_name" {
  description = "Pass to `surviva daemon -dynamodb-table`."
  value       = aws_dynamodb_table.jobs.name
}

output "checkpoints_bucket_name" {
  description = "Pass to `surviva daemon -s3-bucket`, if enable_s3_bucket is true."
  value       = var.enable_s3_bucket ? aws_s3_bucket.checkpoints[0].id : null
}

output "instance_profile_name" {
  description = "Attach this instance profile to the launch template so surviva daemon can write checkpoint status and so the replacement instance is reachable via SSM."
  value       = aws_iam_instance_profile.instance.name
}

output "instance_profile_arn" {
  value = aws_iam_instance_profile.instance.arn
}

output "state_machine_arn" {
  value = aws_sfn_state_machine.restore_orchestrator.arn
}
