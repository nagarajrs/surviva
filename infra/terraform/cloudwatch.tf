# A single log group for troubleshooting: the surviva daemon's own ongoing
# log (checkpoint attempts, IMDS signals) from every instance this module's
# launch template ever produces (original and replacements alike, since
# they share the AMI/config), plus the orchestrator's own restore command
# output (see state_machine.tf's SendRestoreCommand CloudWatchOutputConfig)
# -- so a failed restore is visible here without needing SSM access to an
# instance that may already be gone by the time anyone looks.
resource "aws_cloudwatch_log_group" "surviva" {
  name              = "/surviva/${var.name_prefix}"
  retention_in_days = var.log_retention_days
  tags              = var.tags
}
