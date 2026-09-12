# Native AWS event: fires whenever any Spot instance in this account/region
# gets an interruption notice, independent of surviva's own daemon.
resource "aws_cloudwatch_event_rule" "spot_interruption" {
  name = "${var.name_prefix}-spot-interruption"
  event_pattern = jsonencode({
    source      = ["aws.ec2"]
    detail-type = ["EC2 Spot Instance Interruption Warning"]
  })
  tags = var.tags
}

resource "aws_cloudwatch_event_target" "restore_orchestrator" {
  rule     = aws_cloudwatch_event_rule.spot_interruption.name
  arn      = aws_sfn_state_machine.restore_orchestrator.arn
  role_arn = aws_iam_role.eventbridge.arn
}
