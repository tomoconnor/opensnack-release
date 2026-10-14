# Everything here lands in the generated default VPC, which must still be
# there (and leave nothing in the store) after destroy.

data "aws_vpc" "default" {
  default = true
}

data "aws_subnets" "default" {
  filter {
    name   = "vpc-id"
    values = [data.aws_vpc.default.id]
  }

  filter {
    name   = "default-for-az"
    values = ["true"]
  }
}

data "aws_subnet" "first" {
  id = sort(data.aws_subnets.default.ids)[0]
}

data "aws_security_group" "default" {
  vpc_id = data.aws_vpc.default.id
  name   = "default"
}

data "aws_ami" "ubuntu" {
  most_recent = true
  owners      = ["099720109477"]

  filter {
    name   = "name"
    values = ["ubuntu/images/hvm-ssd-gp3/ubuntu-noble-24.04-amd64-server-*"]
  }
}

data "aws_regions" "all" {}

data "aws_region" "current" {}

data "aws_ec2_instance_type" "small" {
  instance_type = "t3.small"
}

resource "aws_security_group" "app" {
  name_prefix = "cycle-default-"
  description = "in the default VPC"

  ingress {
    from_port       = 8080
    to_port         = 8080
    protocol        = "tcp"
    security_groups = [data.aws_security_group.default.id]
  }

  tags = {
    Variant = var.variant
  }
}

resource "aws_instance" "app" {
  ami                    = data.aws_ami.ubuntu.id
  instance_type          = "t3.micro"
  subnet_id              = data.aws_subnet.first.id
  vpc_security_group_ids = [aws_security_group.app.id]
  user_data              = "#!/bin/sh\necho hello\n"

  metadata_options {
    http_tokens = "required"
  }

  tags = {
    Name    = "cycle-default"
    Variant = var.variant
  }
}

resource "aws_ebs_volume" "data" {
  availability_zone = data.aws_subnet.first.availability_zone
  size              = var.variant == "a" ? 10 : 20
  type              = "gp3"

  tags = {
    Name = "cycle-data"
  }
}

resource "aws_volume_attachment" "data" {
  device_name = "/dev/sdf"
  volume_id   = aws_ebs_volume.data.id
  instance_id = aws_instance.app.id
}

output "checks" {
  value = {
    region       = data.aws_region.current.region
    regions      = length(data.aws_regions.all.names)
    default_cidr = data.aws_vpc.default.cidr_block
    vcpus        = data.aws_ec2_instance_type.small.default_vcpus
  }
}
