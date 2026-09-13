resource "aws_sfn_state_machine" "restore_orchestrator" {
  name     = "${var.name_prefix}-restore-orchestrator"
  role_arn = aws_iam_role.state_machine.arn
  tags     = var.tags

  definition = templatefile("${path.module}/templates/restore_orchestrator.asl.json.tftpl", {
    table_name              = aws_dynamodb_table.jobs.name
    gsi_name                = local.instance_id_index_name
    launch_template_id      = var.launch_template_id
    launch_template_version = var.launch_template_version
    az_subnet_map_json      = jsonencode(var.az_subnet_map)
    checkpoint_wait_seconds = var.checkpoint_wait_seconds
    ebs_attach_device       = var.ebs_attach_device
    restore_ssm_document    = var.restore_ssm_document
    replacement_market_type = var.replacement_market_type
    # surviva restore needs to know which table/region to read job status
    # from; the replacement instance has no other way to learn this, so
    # the orchestrator (which already knows both) bakes them into the
    # command it sends rather than relying on the AMI to somehow know.
    restore_command_prefix = "${var.restore_command_prefix} --dynamodb-table ${aws_dynamodb_table.jobs.name} --aws-region ${var.aws_region}"
  })
}
