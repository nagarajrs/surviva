# Optional, dormant AWS Fault Injection Simulator experiment template: lets
# an operator (or the ansible sandbox) fire a real Spot interruption against
# a real instance on demand, the same action used to validate this project's
# whole restore pipeline end to end. Never auto-run by Terraform itself --
# `create_fis_demo_template` just materializes the template + its role;
# firing it is a separate, explicit `aws fis start-experiment` call.

data "aws_iam_policy_document" "fis_assume_role" {
  count = var.create_fis_demo_template ? 1 : 0
  statement {
    actions = ["sts:AssumeRole"]
    principals {
      type        = "Service"
      identifiers = ["fis.amazonaws.com"]
    }
  }
}

resource "aws_iam_role" "fis" {
  count              = var.create_fis_demo_template ? 1 : 0
  name               = "${var.name_prefix}-fis-role"
  assume_role_policy = data.aws_iam_policy_document.fis_assume_role[0].json
  tags               = var.tags
}

resource "aws_iam_role_policy_attachment" "fis" {
  count      = var.create_fis_demo_template ? 1 : 0
  role       = aws_iam_role.fis[0].name
  policy_arn = "arn:aws:iam::aws:policy/service-role/AWSFaultInjectionSimulatorEC2Access"
}

resource "aws_fis_experiment_template" "spot_interruption" {
  count       = var.create_fis_demo_template ? 1 : 0
  description = "surviva sandbox: send a real Spot interruption notice to the sandbox instance"
  role_arn    = aws_iam_role.fis[0].arn

  stop_condition {
    source = "none"
  }

  target {
    name           = "sandbox-instance"
    resource_type  = "aws:ec2:spot-instance"
    resource_arns  = [var.fis_target_instance_arn]
    selection_mode = "ALL"
  }

  action {
    name      = "send-interruption"
    action_id = "aws:ec2:send-spot-instance-interruptions"

    target {
      key   = "SpotInstances"
      value = "sandbox-instance"
    }

    parameter {
      key   = "durationBeforeInterruption"
      value = var.fis_interruption_delay
    }
  }

  tags = var.tags
}
