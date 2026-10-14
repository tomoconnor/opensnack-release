data "aws_availability_zones" "available" {
  state = "available"
}

resource "aws_vpc" "main" {
  cidr_block           = "10.20.0.0/16"
  enable_dns_hostnames = true

  tags = {
    Name    = "cycle-vpc"
    Variant = var.variant
  }
}

resource "aws_vpc_ipv4_cidr_block_association" "secondary" {
  vpc_id     = aws_vpc.main.id
  cidr_block = "10.21.0.0/16"
}

resource "aws_subnet" "public" {
  count                   = 2
  vpc_id                  = aws_vpc.main.id
  cidr_block              = cidrsubnet(aws_vpc.main.cidr_block, 8, count.index)
  availability_zone       = data.aws_availability_zones.available.names[count.index]
  map_public_ip_on_launch = true

  tags = {
    Name = "cycle-public-${count.index}"
    Tier = var.variant == "a" ? "public" : "edge"
  }
}

resource "aws_subnet" "private" {
  vpc_id            = aws_vpc_ipv4_cidr_block_association.secondary.vpc_id
  cidr_block        = cidrsubnet(aws_vpc_ipv4_cidr_block_association.secondary.cidr_block, 4, 1)
  availability_zone = data.aws_availability_zones.available.names[1]

  tags = {
    Name = "cycle-private"
  }
}

resource "aws_internet_gateway" "igw" {
  vpc_id = aws_vpc.main.id

  tags = {
    Name = "cycle-igw"
  }
}

resource "aws_route_table" "public" {
  vpc_id = aws_vpc.main.id

  tags = {
    Name = "cycle-public"
  }
}

resource "aws_route" "internet" {
  route_table_id         = aws_route_table.public.id
  destination_cidr_block = "0.0.0.0/0"
  gateway_id             = aws_internet_gateway.igw.id
}

resource "aws_route_table_association" "public" {
  count          = 2
  subnet_id      = aws_subnet.public[count.index].id
  route_table_id = aws_route_table.public.id
}

resource "aws_eip" "nat" {
  domain = "vpc"

  tags = {
    Name = "cycle-nat"
  }
}

resource "aws_nat_gateway" "nat" {
  allocation_id = aws_eip.nat.id
  subnet_id     = aws_subnet.public[0].id

  tags = {
    Name    = "cycle-nat"
    Variant = var.variant
  }

  # No depends_on on the internet gateway: the NAT gateway waits in
  # "pending" until one is attached, and the gateway can't be detached
  # while the NAT gateway's address is mapped, as in AWS.
}

resource "aws_route_table" "private" {
  vpc_id = aws_vpc.main.id

  route {
    cidr_block     = "0.0.0.0/0"
    nat_gateway_id = aws_nat_gateway.nat.id
  }

  # The variant moves this route to another destination.
  route {
    cidr_block     = var.variant == "a" ? "192.168.0.0/16" : "172.16.0.0/12"
    nat_gateway_id = aws_nat_gateway.nat.id
  }

  tags = {
    Name = "cycle-private"
  }
}

resource "aws_route_table_association" "private" {
  subnet_id      = aws_subnet.private.id
  route_table_id = aws_route_table.private.id
}

# A standalone route whose target the variant switches (ReplaceRoute).
resource "aws_route" "partner" {
  route_table_id         = aws_route_table.public.id
  destination_cidr_block = "100.64.0.0/16"
  nat_gateway_id         = var.variant == "a" ? aws_nat_gateway.nat.id : null
  gateway_id             = var.variant == "b" ? aws_internet_gateway.igw.id : null
}

resource "aws_network_acl" "public" {
  vpc_id     = aws_vpc.main.id
  subnet_ids = aws_subnet.public[*].id

  ingress {
    rule_no    = 100
    protocol   = "tcp"
    action     = "allow"
    cidr_block = "0.0.0.0/0"
    from_port  = 443
    to_port    = 443
  }

  ingress {
    rule_no    = 110
    protocol   = "tcp"
    action     = "allow"
    cidr_block = "0.0.0.0/0"
    from_port  = var.variant == "a" ? 80 : 8080
    to_port    = var.variant == "a" ? 80 : 8080
  }

  egress {
    rule_no    = 100
    protocol   = "-1"
    action     = "allow"
    cidr_block = "0.0.0.0/0"
    from_port  = 0
    to_port    = 0
  }

  tags = {
    Name = "cycle-public"
  }
}
