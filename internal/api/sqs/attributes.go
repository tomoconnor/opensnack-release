// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package sqs

import (
	"encoding/json"
	"strconv"
	"strings"
)

// Queue attribute defaults, as AWS fills them in for a queue created without
// them. MaximumMessageSize stays at the long-standing 256 KiB, which is also
// the AWS provider's schema default.
var attrDefaults = map[string]string{
	"DelaySeconds":                  "0",
	"MaximumMessageSize":            "262144",
	"MessageRetentionPeriod":        "345600",
	"ReceiveMessageWaitTimeSeconds": "0",
	"VisibilityTimeout":             "30",
}

var fifoDefaults = map[string]string{
	"ContentBasedDeduplication": "false",
	"DeduplicationScope":        "queue",
	"FifoThroughputLimit":       "perQueue",
}

var intAttrRanges = map[string][2]int{
	"DelaySeconds":                  {0, 900},
	"MaximumMessageSize":            {1024, 1048576},
	"MessageRetentionPeriod":        {60, 1209600},
	"ReceiveMessageWaitTimeSeconds": {0, 20},
	"VisibilityTimeout":             {0, 43200},
	"KmsDataKeyReusePeriodSeconds":  {60, 86400},
}

var boolAttrs = map[string]bool{
	"FifoQueue":                 true,
	"ContentBasedDeduplication": true,
	"SqsManagedSseEnabled":      true,
}

// fifoOnlyAttrs may only be set on FIFO queues.
var fifoOnlyAttrs = map[string]bool{
	"ContentBasedDeduplication": true,
	"DeduplicationScope":        true,
	"FifoThroughputLimit":       true,
}

var readOnlyAttrs = map[string]bool{
	"QueueArn":                              true,
	"ApproximateNumberOfMessages":           true,
	"ApproximateNumberOfMessagesNotVisible": true,
	"ApproximateNumberOfMessagesDelayed":    true,
	"CreatedTimestamp":                      true,
	"LastModifiedTimestamp":                 true,
}

func isSettableAttr(name string) bool {
	if _, ok := intAttrRanges[name]; ok {
		return true
	}
	switch name {
	case "Policy", "RedrivePolicy", "RedriveAllowPolicy", "KmsMasterKeyId",
		"FifoQueue", "ContentBasedDeduplication", "DeduplicationScope",
		"FifoThroughputLimit", "SqsManagedSseEnabled":
		return true
	}
	return false
}

func isKnownAttr(name string) bool {
	return name == "All" || isSettableAttr(name) || readOnlyAttrs[name]
}

// redrivePolicy is RedrivePolicy in the form AWS stores and returns it.
type redrivePolicy struct {
	DeadLetterTargetArn string `json:"deadLetterTargetArn"`
	MaxReceiveCount     int    `json:"maxReceiveCount"`
}

func parseRedrivePolicy(s string) (*redrivePolicy, *apiError) {
	var raw map[string]any
	if err := json.Unmarshal([]byte(s), &raw); err != nil {
		return nil, errInvalidAttrValue("RedrivePolicy", "Invalid value for the parameter RedrivePolicy. Reason: Redrive policy is not a valid JSON map.")
	}
	p := &redrivePolicy{}
	p.DeadLetterTargetArn, _ = raw["deadLetterTargetArn"].(string)
	switch v := raw["maxReceiveCount"].(type) {
	case float64:
		p.MaxReceiveCount = int(v)
	case string:
		p.MaxReceiveCount, _ = strconv.Atoi(v)
	}
	if p.DeadLetterTargetArn == "" || p.MaxReceiveCount == 0 {
		return nil, errInvalidAttrValue("RedrivePolicy", "Redrive policy does not contain mandatory attribute: deadLetterTargetArn and maxReceiveCount.")
	}
	if p.MaxReceiveCount < 1 || p.MaxReceiveCount > 1000 {
		return nil, errInvalidAttrValue("RedrivePolicy", "Value of maxReceiveCount must be between 1 and 1000.")
	}
	return p, nil
}

// normalizeAttr validates one attribute value and returns it as AWS would
// store it. An empty value clears the optional string attributes.
func normalizeAttr(name, value string) (string, *apiError) {
	if rng, ok := intAttrRanges[name]; ok {
		n, err := strconv.Atoi(strings.TrimSpace(value))
		if err != nil || n < rng[0] || n > rng[1] {
			return "", errInvalidAttrValue(name, "Must be an integer from "+strconv.Itoa(rng[0])+" to "+strconv.Itoa(rng[1])+".")
		}
		return strconv.Itoa(n), nil
	}
	if boolAttrs[name] {
		switch strings.ToLower(value) {
		case "true", "false":
			return strings.ToLower(value), nil
		}
		return "", errInvalidAttrValue(name, "Must be true or false.")
	}
	if value == "" {
		return "", nil
	}
	switch name {
	case "Policy", "RedriveAllowPolicy":
		if !json.Valid([]byte(value)) {
			return "", errInvalidAttrValue(name, "Invalid JSON.")
		}
		return value, nil
	case "RedrivePolicy":
		p, aerr := parseRedrivePolicy(value)
		if aerr != nil {
			return "", aerr
		}
		b, _ := json.Marshal(p)
		return string(b), nil
	case "DeduplicationScope":
		if value != "queue" && value != "messageGroup" {
			return "", errInvalidAttrValue(name, "Must be queue or messageGroup.")
		}
	case "FifoThroughputLimit":
		if value != "perQueue" && value != "perMessageGroupId" {
			return "", errInvalidAttrValue(name, "Must be perQueue or perMessageGroupId.")
		}
	}
	return value, nil
}

// applyAttrs merges validated changes into stored attributes, keeping the
// encryption attributes consistent the way SQS does: SSE-KMS and SSE-SQS
// exclude each other.
func applyAttrs(stored map[string]string, changes map[string]string) {
	for k, v := range changes {
		if v == "" {
			delete(stored, k)
			continue
		}
		stored[k] = v
	}
	if v, ok := changes["KmsMasterKeyId"]; ok && v != "" {
		stored["SqsManagedSseEnabled"] = "false"
	}
	if changes["SqsManagedSseEnabled"] == "true" {
		delete(stored, "KmsMasterKeyId")
		delete(stored, "KmsDataKeyReusePeriodSeconds")
	}
}

// effectiveAttrs is the stored attributes with AWS's defaults filled in,
// i.e. what GetQueueAttributes returns before the computed ones.
func effectiveAttrs(rec *queueRecord) map[string]string {
	out := map[string]string{}
	for k, v := range attrDefaults {
		out[k] = v
	}
	if rec.fifo() {
		out["FifoQueue"] = "true"
		for k, v := range fifoDefaults {
			out[k] = v
		}
	}
	out["SqsManagedSseEnabled"] = "true"
	for k, v := range rec.Attributes {
		out[k] = v
	}
	if out["KmsMasterKeyId"] != "" {
		out["SqsManagedSseEnabled"] = "false"
		if out["KmsDataKeyReusePeriodSeconds"] == "" {
			out["KmsDataKeyReusePeriodSeconds"] = "300"
		}
	}
	return out
}
