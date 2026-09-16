# Optional S3 destination for the S3 checkpoint storage path. The lifecycle
# rule aborts abandoned multipart uploads (e.g. an instance that died
# mid-upload) so they don't linger as unbilled-looking-but-still-charged
# storage forever.

resource "aws_s3_bucket" "checkpoints" {
  count  = var.enable_s3_bucket ? 1 : 0
  bucket = "${var.name_prefix}-checkpoints-${data.aws_caller_identity.current.account_id}"
  tags   = var.tags
}

resource "aws_s3_bucket_lifecycle_configuration" "checkpoints" {
  count  = var.enable_s3_bucket ? 1 : 0
  bucket = aws_s3_bucket.checkpoints[0].id

  rule {
    id     = "abort-incomplete-multipart-uploads"
    status = "Enabled"

    filter {}

    abort_incomplete_multipart_upload {
      days_after_initiation = 1
    }
  }
}

data "aws_caller_identity" "current" {}
