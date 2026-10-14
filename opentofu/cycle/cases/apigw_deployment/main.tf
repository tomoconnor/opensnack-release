resource "aws_apigatewayv2_api" "a" {
  name          = "cycle-deploy-api"
  protocol_type = "HTTP"
}

resource "aws_apigatewayv2_integration" "i" {
  api_id                 = aws_apigatewayv2_api.a.id
  integration_type       = "AWS_PROXY"
  integration_uri        = "arn:aws:lambda:us-east-1:000000000000:function:cycle-fn"
  payload_format_version = "2.0"
}

resource "aws_apigatewayv2_route" "r" {
  api_id    = aws_apigatewayv2_api.a.id
  route_key = "$default"
  target    = "integrations/${aws_apigatewayv2_integration.i.id}"
}

resource "aws_apigatewayv2_deployment" "d" {
  api_id      = aws_apigatewayv2_api.a.id
  description = "deploy ${var.variant}"

  depends_on = [aws_apigatewayv2_route.r]
}

resource "aws_apigatewayv2_stage" "s" {
  api_id        = aws_apigatewayv2_api.a.id
  name          = "$default"
  deployment_id = aws_apigatewayv2_deployment.d.id
  description   = "stage ${var.variant}"

  default_route_settings {
    throttling_burst_limit = var.variant == "a" ? 10 : 20
    throttling_rate_limit  = 5
  }
}
