// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package logs

// CreateLogGroup request
type CreateLogGroupRequest struct {
	LogGroupName string `json:"logGroupName"`
}

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

// PutLogEvents response
type PutLogEventsResponse struct {
	NextSequenceToken string `json:"nextSequenceToken"`
}

// DescribeLogGroups
type DescribeLogGroupsResponse struct {
	LogGroups []LogGroupElement `json:"logGroups"`
}

type LogGroupElement struct {
	LogGroupName string `json:"logGroupName"`
	Arn          string `json:"arn"`
	CreationTime int64  `json:"creationTime"`
}

// DescribeLogStreams
type DescribeLogStreamsResponse struct {
	LogStreams []LogStreamElement `json:"logStreams"`
}

type LogStreamElement struct {
	LogStreamName string `json:"logStreamName"`
	Arn           string `json:"arn"`
	CreationTime  int64  `json:"creationTime"`
}
