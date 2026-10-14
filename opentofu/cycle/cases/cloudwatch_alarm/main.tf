resource "aws_sns_topic" "alerts" {
  name = "cycle-orders-alerts"
}

resource "aws_cloudwatch_metric_alarm" "cpu" {
  alarm_name          = "orders-cpu-high"
  alarm_description   = "Orders CPU is running hot"
  comparison_operator = "GreaterThanThreshold"
  evaluation_periods  = 2
  datapoints_to_alarm = 2
  metric_name         = "CPUUtilization"
  namespace           = "AWS/EC2"
  period              = 300
  statistic           = "Average"
  threshold           = var.variant == "a" ? 80 : 92.5
  treat_missing_data  = "notBreaching"

  dimensions = {
    InstanceId = "i-0123456789abcdef0"
    Orders     = "one"
  }

  alarm_actions = [aws_sns_topic.alerts.arn]
  ok_actions    = [aws_sns_topic.alerts.arn]

  tags = {
    Team    = "team"
    Variant = var.variant
  }
}

resource "aws_cloudwatch_composite_alarm" "release" {
  alarm_name        = "release-at-risk"
  alarm_description = "Any orders trouble"
  alarm_rule        = "ALARM(${aws_cloudwatch_metric_alarm.cpu.alarm_name})"
  alarm_actions     = [aws_sns_topic.alerts.arn]

  tags = {
    Variant = var.variant
  }
}
