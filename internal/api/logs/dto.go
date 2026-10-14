// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package logs

// CreateLogStream request
type CreateLogStreamRequest struct {
	LogGroupName  string `json:"logGroupName"`
	LogStreamName string `json:"logStreamName"`
}

// PutLogEvents request
type PutLogEventsRequest struct {
	LogGroupName  string                        `json:"logGroupName"`
	LogStreamName string                        `json:"logStreamName"`
	LogEvents     []PutLogEventsRequestLogEvent `json:"logEvents"`
}

type PutLogEventsRequestLogEvent struct {
	Timestamp int64  `json:"timestamp"`
	Message   string `json:"message"`
}

// MetricTransformation is stored and returned exactly as the client sent it.
type MetricTransformation struct {
	MetricName      string            `json:"metricName"`
	MetricNamespace string            `json:"metricNamespace"`
	MetricValue     string            `json:"metricValue"`
	DefaultValue    *float64          `json:"defaultValue,omitempty"`
	Dimensions      map[string]string `json:"dimensions,omitempty"`
	Unit            string            `json:"unit,omitempty"`
}

// MetricFilter is the DescribeMetricFilters shape, which is also what the
// store keeps.
type MetricFilter struct {
	FilterName             string                 `json:"filterName"`
	FilterPattern          string                 `json:"filterPattern"`
	LogGroupName           string                 `json:"logGroupName"`
	MetricTransformations  []MetricTransformation `json:"metricTransformations"`
	CreationTime           int64                  `json:"creationTime"`
	ApplyOnTransformedLogs bool                   `json:"applyOnTransformedLogs"`
}

// SubscriptionFilter is the DescribeSubscriptionFilters shape.
type SubscriptionFilter struct {
	FilterName             string   `json:"filterName"`
	FilterPattern          string   `json:"filterPattern"`
	LogGroupName           string   `json:"logGroupName"`
	DestinationArn         string   `json:"destinationArn"`
	RoleArn                string   `json:"roleArn,omitempty"`
	Distribution           string   `json:"distribution"`
	EmitSystemFields       []string `json:"emitSystemFields,omitempty"`
	ApplyOnTransformedLogs bool     `json:"applyOnTransformedLogs"`
	CreationTime           int64    `json:"creationTime"`
}
