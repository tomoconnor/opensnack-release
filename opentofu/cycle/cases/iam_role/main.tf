resource "aws_iam_role" "r" {
  name                 = "cycle-role"
  description          = "cycle role ${var.variant}"
  max_session_duration = var.variant == "a" ? 3600 : 7200
  assume_role_policy = jsonencode({
    Version = "2012-10-17"
    Statement = [{
      Effect    = "Allow"
      Principal = { Service = var.variant == "a" ? "ec2.amazonaws.com" : "lambda.amazonaws.com" }
      Action    = "sts:AssumeRole"
    }]
  })
  tags = {
    Team    = "team"
    Variant = var.variant
  }
}

resource "aws_iam_role_policy" "inline" {
  name = "cycle-inline"
  role = aws_iam_role.r.id
  policy = jsonencode({
    Version = "2012-10-17"
    Statement = [{
      Effect   = "Allow"
      Action   = ["s3:GetObject"]
      Resource = "arn:aws:s3:::cycle-${var.variant}/*"
    }]
  })
}

resource "aws_iam_policy" "managed" {
  name = "cycle-role-managed"
  policy = jsonencode({
    Version = "2012-10-17"
    Statement = [{
      Effect   = "Allow"
      Action   = "sqs:SendMessage"
      Resource = "*"
    }]
  })
}

resource "aws_iam_role_policy_attachment" "a" {
  role       = aws_iam_role.r.name
  policy_arn = aws_iam_policy.managed.arn
}
