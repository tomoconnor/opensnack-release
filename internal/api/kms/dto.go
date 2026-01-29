// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package kms

// CreateKeyInput represents the request to create a KMS key
type CreateKeyInput struct {
	Description                    string `json:"Description,omitempty"`
	CustomerMasterKeySpec          string `json:"CustomerMasterKeySpec,omitempty"`
	KeyUsage                       string `json:"KeyUsage,omitempty"`
	Policy                         string `json:"Policy,omitempty"`
	BypassPolicyLockoutSafetyCheck bool   `json:"BypassPolicyLockoutSafetyCheck,omitempty"`
	Tags                           []Tag  `json:"Tags,omitempty"`
	MultiRegion                    *bool  `json:"MultiRegion,omitempty"`
}

// Tag represents a KMS tag
type Tag struct {
	TagKey   string `json:"TagKey"`
	TagValue string `json:"TagValue"`
}

// CreateKeyOutput represents the response from creating a KMS key
type CreateKeyOutput struct {
	KeyMetadata KeyMetadata `json:"KeyMetadata"`
}

// KeyMetadata represents KMS key metadata
type KeyMetadata struct {
	AWSAccountID          string   `json:"AWSAccountId"`
	ARN                   string   `json:"Arn"`
	CreationDate          float64  `json:"CreationDate"`
	CustomerMasterKeySpec string   `json:"CustomerMasterKeySpec"`
	Description           string   `json:"Description,omitempty"`
	Enabled               bool     `json:"Enabled"`
	EncryptionAlgorithms  []string `json:"EncryptionAlgorithms,omitempty"`
	KeyID                 string   `json:"KeyId"`
	KeyManager            string   `json:"KeyManager"`
	KeySpec               string   `json:"KeySpec"`
	KeyState              string   `json:"KeyState"`
	KeyUsage              string   `json:"KeyUsage"`
	MultiRegion           bool     `json:"MultiRegion"`
	Origin                string   `json:"Origin"`
	SigningAlgorithms     []string `json:"SigningAlgorithms,omitempty"`
}

// DescribeKeyInput represents the request to describe a KMS key
type DescribeKeyInput struct {
	KeyID   string `json:"KeyId"`
	GrantID string `json:"GrantId,omitempty"`
}

// DescribeKeyOutput represents the response from describing a KMS key
type DescribeKeyOutput struct {
	KeyMetadata KeyMetadata `json:"KeyMetadata"`
}

// GetKeyPolicyInput represents the request to get a KMS key policy
type GetKeyPolicyInput struct {
	KeyID      string `json:"KeyId"`
	PolicyName string `json:"PolicyName,omitempty"`
}

// GetKeyPolicyOutput represents the response from getting a KMS key policy
type GetKeyPolicyOutput struct {
	Policy string `json:"Policy"`
}

// GetKeyRotationStatusInput represents the request to get a KMS key rotation status
type GetKeyRotationStatusInput struct {
	KeyID string `json:"KeyId"`
}

// GetKeyRotationStatusOutput represents the response from getting a KMS key rotation status
type GetKeyRotationStatusOutput struct {
	KeyRotationEnabled bool   `json:"KeyRotationEnabled"`
	KeyID              string `json:"KeyId"`
}

// ListResourceTagsInput represents the request to list tags for a KMS key
type ListResourceTagsInput struct {
	KeyID   string `json:"KeyId"`
	Marker  string `json:"Marker,omitempty"`
	Limit   int32  `json:"Limit,omitempty"`
}

// ListResourceTagsOutput represents the response from listing tags for a KMS key
type ListResourceTagsOutput struct {
	Tags      []Tag `json:"Tags"`
	Truncated bool  `json:"Truncated"`
	NextMarker string `json:"NextMarker,omitempty"`
}

// ScheduleKeyDeletionInput represents the request to schedule key deletion
type ScheduleKeyDeletionInput struct {
	KeyID              string `json:"KeyId"`
	PendingWindowInDays int32 `json:"PendingWindowInDays,omitempty"`
}

// ScheduleKeyDeletionOutput represents the response from scheduling key deletion
type ScheduleKeyDeletionOutput struct {
	KeyID              string  `json:"KeyId"`
	DeletionDate       float64 `json:"DeletionDate"`
	KeyState           string  `json:"KeyState"`
	PendingWindowInDays int32  `json:"PendingWindowInDays"`
}
