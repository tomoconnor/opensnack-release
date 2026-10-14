resource "aws_iam_user" "u" {
  name = "cycle-user"
  path = "/team/"
  tags = {
    Variant = var.variant
  }
}

resource "aws_iam_group" "g" {
  name = "cycle-group"
  path = "/${var.variant}/"
}

resource "aws_iam_group" "g2" {
  name = "cycle-group-2"
}

# Exclusive membership of one group...
resource "aws_iam_group_membership" "gm" {
  name  = "cycle-gm"
  group = aws_iam_group.g.name
  users = [aws_iam_user.u.name]
}

# ...and the user-side view of another.
resource "aws_iam_user_group_membership" "m" {
  user   = aws_iam_user.u.name
  groups = [aws_iam_group.g2.name]
}

resource "aws_iam_user_policy" "up" {
  name = "cycle-user-inline"
  user = aws_iam_user.u.name
  policy = jsonencode({
    Version = "2012-10-17"
    Statement = [{
      Effect   = "Allow"
      Action   = "sqs:ReceiveMessage"
      Resource = "arn:aws:sqs:*:*:cycle-${var.variant}"
    }]
  })
}

resource "aws_iam_group_policy" "gp" {
  name  = "cycle-group-inline"
  group = aws_iam_group.g2.name
  policy = jsonencode({
    Version   = "2012-10-17"
    Statement = [{ Effect = "Allow", Action = "sns:Publish", Resource = "*" }]
  })
}

resource "aws_iam_policy" "managed" {
  name = "cycle-user-managed"
  policy = jsonencode({
    Version   = "2012-10-17"
    Statement = [{ Effect = "Allow", Action = "s3:ListBucket", Resource = "*" }]
  })
}

resource "aws_iam_user_policy_attachment" "ua" {
  user       = aws_iam_user.u.name
  policy_arn = aws_iam_policy.managed.arn
}

resource "aws_iam_group_policy_attachment" "ga" {
  group      = aws_iam_group.g.name
  policy_arn = aws_iam_policy.managed.arn
}

resource "aws_iam_access_key" "k" {
  user   = aws_iam_user.u.name
  status = var.variant == "a" ? "Active" : "Inactive"
}
