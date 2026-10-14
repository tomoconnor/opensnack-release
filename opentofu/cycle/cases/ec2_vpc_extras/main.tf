data "aws_availability_zones" "available" {
  state = "available"
}

data "aws_region" "current" {}

data "aws_ami" "al2023" {
  most_recent = true
  owners      = ["amazon"]

  filter {
    name   = "name"
    values = ["al2023-ami-2023.*-arm64"]
  }
}

resource "aws_vpc" "main" {
  cidr_block           = "10.50.0.0/16"
  enable_dns_support   = true
  enable_dns_hostnames = true
}

resource "aws_subnet" "a" {
  vpc_id            = aws_vpc.main.id
  cidr_block        = "10.50.1.0/24"
  availability_zone = data.aws_availability_zones.available.names[0]
}

resource "aws_subnet" "b" {
  vpc_id               = aws_vpc.main.id
  cidr_block           = "10.50.2.0/24"
  availability_zone_id = data.aws_availability_zones.available.zone_ids[1]
}

resource "aws_internet_gateway" "igw" {
  vpc_id = aws_vpc.main.id
}

# Adopt the defaults AWS created with the VPC.
resource "aws_default_security_group" "default" {
  vpc_id = aws_vpc.main.id

  ingress {
    protocol  = "-1"
    self      = true
    from_port = 0
    to_port   = 0
  }

  tags = {
    Variant = var.variant
  }
}

resource "aws_default_network_acl" "default" {
  default_network_acl_id = aws_vpc.main.default_network_acl_id
  subnet_ids             = [aws_subnet.a.id]

  ingress {
    protocol   = -1
    rule_no    = 100
    action     = "allow"
    cidr_block = "0.0.0.0/0"
    from_port  = 0
    to_port    = 0
  }

  egress {
    protocol   = -1
    rule_no    = 100
    action     = "allow"
    cidr_block = "0.0.0.0/0"
    from_port  = 0
    to_port    = 0
  }

  lifecycle {
    ignore_changes = [subnet_ids]
  }
}

resource "aws_route_table" "alt" {
  vpc_id = aws_vpc.main.id
}

resource "aws_main_route_table_association" "alt" {
  vpc_id         = aws_vpc.main.id
  route_table_id = aws_route_table.alt.id
}

resource "aws_network_acl" "b" {
  vpc_id = aws_vpc.main.id
}

resource "aws_network_acl_association" "b" {
  network_acl_id = aws_network_acl.b.id
  subnet_id      = aws_subnet.b.id
}

resource "aws_network_acl_rule" "b_https" {
  network_acl_id = aws_network_acl.b.id
  rule_number    = 200
  egress         = false
  protocol       = "tcp"
  rule_action    = "allow"
  cidr_block     = "0.0.0.0/0"
  from_port      = 443
  to_port        = 443
}

resource "aws_network_acl_rule" "b_icmp" {
  network_acl_id = aws_network_acl.b.id
  rule_number    = 210
  egress         = false
  protocol       = "icmp"
  rule_action    = "allow"
  cidr_block     = "10.0.0.0/8"
  icmp_type      = 8
  icmp_code      = 0
}

resource "aws_security_group" "svc" {
  name   = "cycle-svc"
  vpc_id = aws_vpc.main.id
}

resource "aws_security_group_rule" "svc_https" {
  type              = "ingress"
  security_group_id = aws_security_group.svc.id
  from_port         = 443
  to_port           = 443
  protocol          = "tcp"
  cidr_blocks       = [aws_vpc.main.cidr_block]
  description       = var.variant == "a" ? "https" : "https from the vpc"
}

resource "aws_security_group_rule" "svc_out" {
  type              = "egress"
  security_group_id = aws_security_group.svc.id
  from_port         = 0
  to_port           = 0
  protocol          = "-1"
  cidr_blocks       = ["0.0.0.0/0"]
}

resource "aws_vpc_endpoint" "s3" {
  vpc_id            = aws_vpc.main.id
  service_name      = "com.amazonaws.${data.aws_region.current.region}.s3"
  route_table_ids   = [aws_route_table.alt.id]
  vpc_endpoint_type = "Gateway"

  tags = {
    Variant = var.variant
  }
}

resource "aws_vpc_endpoint" "ssm" {
  vpc_id              = aws_vpc.main.id
  service_name        = "com.amazonaws.${data.aws_region.current.region}.ssm"
  vpc_endpoint_type   = "Interface"
  subnet_ids          = [aws_subnet.a.id]
  security_group_ids  = [aws_security_group.svc.id]
  private_dns_enabled = true
}

resource "aws_instance" "arm" {
  ami           = data.aws_ami.al2023.id
  instance_type = "t4g.micro"
  subnet_id     = aws_subnet.a.id
  private_ip    = "10.50.1.20"

  credit_specification {
    cpu_credits = var.variant == "a" ? "standard" : "unlimited"
  }

  tags = {
    Name = "cycle-arm"
  }
}

resource "aws_ec2_instance_state" "arm" {
  instance_id = aws_instance.arm.id
  state       = var.variant == "a" ? "running" : "stopped"
}

resource "aws_network_interface" "spare" {
  subnet_id = aws_subnet.a.id
}

resource "aws_network_interface_attachment" "spare" {
  instance_id          = aws_instance.arm.id
  network_interface_id = aws_network_interface.spare.id
  device_index         = 1
}

resource "aws_eip" "arm" {
  domain = "vpc"
}

resource "aws_eip_association" "arm" {
  allocation_id        = aws_eip.arm.id
  network_interface_id = aws_network_interface.spare.id

  depends_on = [aws_internet_gateway.igw]
}

# The same CIDR in another region is a separate network.
data "aws_availability_zones" "west" {
  provider = aws.west
}

resource "aws_vpc" "west" {
  provider   = aws.west
  cidr_block = "10.50.0.0/16"

  tags = {
    Variant = var.variant
  }
}

resource "aws_subnet" "west" {
  provider          = aws.west
  vpc_id            = aws_vpc.west.id
  cidr_block        = "10.50.1.0/24"
  availability_zone = data.aws_availability_zones.west.names[2]
}
