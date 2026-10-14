locals {
  ec2_trust = jsonencode({
    Version = "2012-10-17"
    Statement = [{
      Effect    = "Allow"
      Principal = { Service = "ec2.amazonaws.com" }
      Action    = "sts:AssumeRole"
    }]
  })
}

resource "aws_iam_role" "a" {
  name               = "cycle-profile-role-a"
  assume_role_policy = local.ec2_trust
}

resource "aws_iam_role" "b" {
  name               = "cycle-profile-role-b"
  path               = "/workers/"
  assume_role_policy = local.ec2_trust
}

# Switching the role swaps it in place (RemoveRoleFromInstanceProfile, then
# AddRoleToInstanceProfile).
resource "aws_iam_instance_profile" "p" {
  name = "cycle-profile"
  path = "/cycle/"
  role = var.variant == "a" ? aws_iam_role.a.name : aws_iam_role.b.name
  tags = {
    Variant = var.variant
  }
}
