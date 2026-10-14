data "aws_ami" "al2023" {
  most_recent = true
  owners      = ["amazon"]

  filter {
    name   = "name"
    values = ["al2023-ami-2023.*-x86_64"]
  }

  filter {
    name   = "architecture"
    values = ["x86_64"]
  }
}

data "aws_availability_zones" "available" {
  state = "available"
}

resource "aws_vpc" "main" {
  cidr_block = "10.40.0.0/16"

  tags = {
    Name = "cycle-instance"
  }
}

resource "aws_subnet" "main" {
  vpc_id            = aws_vpc.main.id
  cidr_block        = "10.40.1.0/24"
  availability_zone = data.aws_availability_zones.available.names[0]
}

resource "aws_internet_gateway" "igw" {
  vpc_id = aws_vpc.main.id
}

resource "aws_security_group" "ssh" {
  name        = "cycle-ssh"
  description = "ssh in"
  vpc_id      = aws_vpc.main.id

  ingress {
    from_port   = 22
    to_port     = 22
    protocol    = "tcp"
    cidr_blocks = ["10.0.0.0/8"]
  }

  egress {
    from_port   = 0
    to_port     = 0
    protocol    = "-1"
    cidr_blocks = ["0.0.0.0/0"]
  }
}

resource "aws_key_pair" "deployer" {
  key_name   = "cycle-deployer"
  public_key = "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIFwcJ3fw1KHTk7B8Q/7DTNyp4GeBfJXrmlEY/AyN2H5H cycle@example"

  tags = {
    Name = "cycle-deployer"
  }
}

resource "aws_instance" "web" {
  ami                    = data.aws_ami.al2023.id
  instance_type          = var.variant == "a" ? "t3.micro" : "t3.small"
  subnet_id              = aws_subnet.main.id
  vpc_security_group_ids = [aws_security_group.ssh.id]
  key_name               = aws_key_pair.deployer.key_name

  root_block_device {
    volume_size = 10
  }

  tags = {
    Name    = "cycle-web"
    Variant = var.variant
  }
}

resource "aws_eip" "web" {
  domain   = "vpc"
  instance = aws_instance.web.id

  depends_on = [aws_internet_gateway.igw]
}

resource "aws_network_interface" "extra" {
  subnet_id       = aws_subnet.main.id
  private_ips     = ["10.40.1.50"]
  security_groups = [aws_security_group.ssh.id]
  description     = var.variant == "a" ? "spare interface" : "spare interface (b)"
}
