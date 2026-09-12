# Instance role: attach this (via instance_profile_name output) to the
# launch template so `surviva daemon` can write its own checkpoint status,
# and so the replacement instance is reachable via SSM SendCommand.

data "aws_iam_policy_document" "ec2_assume_role" {
  statement {
    actions = ["sts:AssumeRole"]
    principals {
      type        = "Service"
      identifiers = ["ec2.amazonaws.com"]
    }
  }
}

resource "aws_iam_role" "instance" {
  name               = "${var.name_prefix}-instance-role"
  assume_role_policy = data.aws_iam_policy_document.ec2_assume_role.json
  tags               = var.tags
}

resource "aws_iam_role_policy_attachment" "instance_ssm" {
  role       = aws_iam_role.instance.name
  policy_arn = "arn:aws:iam::aws:policy/AmazonSSMManagedInstanceCore"
}

data "aws_iam_policy_document" "instance" {
  statement {
    sid       = "JobStatusReadWrite"
    actions   = ["dynamodb:PutItem", "dynamodb:UpdateItem", "dynamodb:GetItem"]
    resources = [aws_dynamodb_table.jobs.arn]
  }

  statement {
    sid       = "ValidateEBSVolume"
    actions   = ["ec2:DescribeVolumes"]
    resources = ["*"] # DescribeVolumes has no useful resource-level restriction
  }

  dynamic "statement" {
    for_each = var.enable_s3_bucket ? [1] : []
    content {
      sid       = "PushCheckpointsToS3"
      actions   = ["s3:PutObject"]
      resources = ["${aws_s3_bucket.checkpoints[0].arn}/*"]
    }
  }
}

resource "aws_iam_role_policy" "instance" {
  name   = "${var.name_prefix}-instance-policy"
  role   = aws_iam_role.instance.id
  policy = data.aws_iam_policy_document.instance.json
}

resource "aws_iam_instance_profile" "instance" {
  name = "${var.name_prefix}-instance-profile"
  role = aws_iam_role.instance.name
}

# State machine role: the restore orchestrator's own permissions.

data "aws_iam_policy_document" "sfn_assume_role" {
  statement {
    actions = ["sts:AssumeRole"]
    principals {
      type        = "Service"
      identifiers = ["states.amazonaws.com"]
    }
  }
}

resource "aws_iam_role" "state_machine" {
  name               = "${var.name_prefix}-orchestrator-role"
  assume_role_policy = data.aws_iam_policy_document.sfn_assume_role.json
  tags               = var.tags
}

data "aws_iam_policy_document" "state_machine" {
  statement {
    sid     = "QueryJobsByInstance"
    actions = ["dynamodb:Query"]
    resources = [
      aws_dynamodb_table.jobs.arn,
      "${aws_dynamodb_table.jobs.arn}/index/*",
    ]
  }

  statement {
    sid       = "MarkJobsRestored"
    actions   = ["dynamodb:UpdateItem"]
    resources = [aws_dynamodb_table.jobs.arn]
  }

  statement {
    sid     = "LaunchAndInspectReplacementInstance"
    actions = ["ec2:RunInstances", "ec2:DescribeInstances", "ec2:DescribeVolumes", "ec2:AttachVolume"]
    # RunInstances/AttachVolume don't support restricting to a single
    # not-yet-known instance/volume ARN pattern usefully for this use case.
    resources = ["*"]
  }

  statement {
    sid     = "SendRestoreCommand"
    actions = ["ssm:SendCommand"]
    resources = [
      "arn:aws:ssm:${var.aws_region}::document/${var.restore_ssm_document}",
      "arn:aws:ec2:${var.aws_region}:${data.aws_caller_identity.current.account_id}:instance/*",
    ]
  }

  statement {
    sid       = "WaitForSSMRegistration"
    actions   = ["ssm:DescribeInstanceInformation"]
    resources = ["*"] # DescribeInstanceInformation has no resource-level restriction
  }

  statement {
    # RunInstances with the surviva instance profile attached requires the
    # caller be allowed to pass that role to EC2; scoped by service rather
    # than by resource since the profile isn't known until this module
    # creates it (self-reference is fine since it's still a specific ARN).
    sid       = "PassInstanceRoleToEC2"
    actions   = ["iam:PassRole"]
    resources = [aws_iam_role.instance.arn]
    condition {
      test     = "StringEquals"
      variable = "iam:PassedToService"
      values   = ["ec2.amazonaws.com"]
    }
  }
}

resource "aws_iam_role_policy" "state_machine" {
  name   = "${var.name_prefix}-orchestrator-policy"
  role   = aws_iam_role.state_machine.id
  policy = data.aws_iam_policy_document.state_machine.json
}

# EventBridge rule role: lets the rule start the state machine.

data "aws_iam_policy_document" "eventbridge_assume_role" {
  statement {
    actions = ["sts:AssumeRole"]
    principals {
      type        = "Service"
      identifiers = ["events.amazonaws.com"]
    }
  }
}

resource "aws_iam_role" "eventbridge" {
  name               = "${var.name_prefix}-eventbridge-role"
  assume_role_policy = data.aws_iam_policy_document.eventbridge_assume_role.json
  tags               = var.tags
}

data "aws_iam_policy_document" "eventbridge" {
  statement {
    actions   = ["states:StartExecution"]
    resources = [aws_sfn_state_machine.restore_orchestrator.arn]
  }
}

resource "aws_iam_role_policy" "eventbridge" {
  name   = "${var.name_prefix}-eventbridge-policy"
  role   = aws_iam_role.eventbridge.id
  policy = data.aws_iam_policy_document.eventbridge.json
}
