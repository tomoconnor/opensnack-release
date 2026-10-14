resource "aws_apigatewayv2_api" "a" {
  name          = "cycle-http-api"
  protocol_type = "HTTP"
  description   = "variant ${var.variant}"

  cors_configuration {
    allow_origins = ["https://example.com"]
    allow_methods = ["GET", "POST"]
    max_age       = 300
  }

  tags = {
    Variant = var.variant
  }
}

resource "aws_apigatewayv2_integration" "i" {
  api_id               = aws_apigatewayv2_api.a.id
  integration_type     = "HTTP_PROXY"
  integration_method   = "ANY"
  integration_uri      = "https://backend.example.com/{proxy}"
  timeout_milliseconds = 10000
}

resource "aws_apigatewayv2_route" "r" {
  api_id    = aws_apigatewayv2_api.a.id
  route_key = var.variant == "a" ? "GET /app/{proxy+}" : "ANY /app/{proxy+}"
  target    = "integrations/${aws_apigatewayv2_integration.i.id}"
}

resource "aws_apigatewayv2_stage" "s" {
  api_id      = aws_apigatewayv2_api.a.id
  name        = "live"
  auto_deploy = true

  stage_variables = var.variant == "a" ? {
    backend = "one"
    color   = "red"
    } : {
    backend = "two"
  }

  tags = {
    Variant = var.variant
  }
}
