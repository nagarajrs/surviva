# Job status table written by surviva daemon (internal/remote) and read by
# the restore orchestrator. The GSI is what lets the orchestrator find every
# job that belonged to a specific interrupted instance — the daemon only
# ever looks up/writes by job_id, so it never needs this index itself.

resource "aws_dynamodb_table" "jobs" {
  name         = "${var.name_prefix}-jobs"
  billing_mode = "PAY_PER_REQUEST"
  hash_key     = "job_id"

  attribute {
    name = "job_id"
    type = "S"
  }

  attribute {
    name = "instance_id"
    type = "S"
  }

  global_secondary_index {
    name            = local.instance_id_index_name
    hash_key        = "instance_id"
    projection_type = "ALL"
  }

  tags = var.tags
}

locals {
  instance_id_index_name = "instance_id-index"
}
