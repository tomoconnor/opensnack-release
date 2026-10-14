resource "aws_vpc" "sg" {
  cidr_block = "10.30.0.0/16"

  tags = {
    Name = "cycle-sg"
  }
}

# Legacy inline rules.
resource "aws_security_group" "web" {
  name        = "cycle-web"
  description = "web tier"
  vpc_id      = aws_vpc.sg.id

  ingress {
    description = var.variant == "a" ? "http" : "http on the alt port"
    from_port   = var.variant == "a" ? 80 : 8080
    to_port     = var.variant == "a" ? 80 : 8080
    protocol    = "tcp"
    cidr_blocks = ["0.0.0.0/0"]
  }

  ingress {
    description = "https"
    from_port   = 443
    to_port     = 443
    protocol    = "tcp"
    cidr_blocks = ["10.0.0.0/8", "192.168.0.0/16"]
  }

  ingress {
    from_port = 0
    to_port   = 0
    protocol  = "-1"
    self      = true
  }

  egress {
    from_port   = 0
    to_port     = 0
    protocol    = "-1"
    cidr_blocks = ["0.0.0.0/0"]
  }

  tags = {
    Name = "cycle-web"
  }
}

# Rules as their own resources.
resource "aws_security_group" "app" {
  name        = "cycle-app"
  description = "app tier"
  vpc_id      = aws_vpc.sg.id

  tags = {
    Name = "cycle-app"
  }
}

resource "aws_vpc_security_group_ingress_rule" "app_from_web" {
  security_group_id            = aws_security_group.app.id
  referenced_security_group_id = aws_security_group.web.id
  ip_protocol                  = "tcp"
  from_port                    = var.variant == "a" ? 8080 : 9090
  to_port                      = var.variant == "a" ? 8080 : 9090
  description                  = var.variant == "a" ? "from web" : "from the web tier"

  tags = {
    Name = "app-from-web"
  }
}

resource "aws_vpc_security_group_ingress_rule" "app_ssh" {
  security_group_id = aws_security_group.app.id
  cidr_ipv4         = aws_vpc.sg.cidr_block
  ip_protocol       = "tcp"
  from_port         = 22
  to_port           = 22
}

resource "aws_vpc_security_group_ingress_rule" "app_ping" {
  security_group_id = aws_security_group.app.id
  cidr_ipv4         = "10.0.0.0/8"
  ip_protocol       = "icmp"
  from_port         = 8
  to_port           = 0
}

resource "aws_vpc_security_group_egress_rule" "app_all" {
  security_group_id = aws_security_group.app.id
  cidr_ipv4         = "0.0.0.0/0"
  ip_protocol       = "-1"
  description       = var.variant == "a" ? "anywhere" : "anywhere at all"
}
