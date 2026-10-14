# Changing the document updates the policy in place: the provider creates a
# new default version (pruning the oldest once there are five).
resource "aws_iam_policy" "p" {
  name        = "cycle-policy"
  path        = "/cycle/"
  description = "cycle policy"
  policy = jsonencode({
    Version = "2012-10-17"
    Statement = [{
      Sid      = "Objects"
      Effect   = "Allow"
      Action   = var.variant == "a" ? ["s3:GetObject"] : ["s3:GetObject", "s3:PutObject"]
      Resource = "arn:aws:s3:::cycle bucket+${var.variant}/*"
    }]
  })
  tags = {
    Variant = var.variant
  }
}
