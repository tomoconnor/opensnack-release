// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package kms

// CreateKeyInput represents the request to create a KMS key
type CreateKeyInput struct {
	Description                    string `json:"Description,omitempty"`
	CustomerMasterKeySpec          string `json:"CustomerMasterKeySpec,omitempty"`
	KeySpec                        string `json:"KeySpec,omitempty"`
	KeyUsage                       string `json:"KeyUsage,omitempty"`
	Origin                         string `json:"Origin,omitempty"`
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
	AWSAccountID                string                    `json:"AWSAccountId"`
	ARN                         string                    `json:"Arn"`
	CreationDate                float64                   `json:"CreationDate"`
	CustomerMasterKeySpec       string                    `json:"CustomerMasterKeySpec"`
	DeletionDate                float64                   `json:"DeletionDate,omitempty"`
	Description                 string                    `json:"Description"`
	Enabled                     bool                      `json:"Enabled"`
	EncryptionAlgorithms        []string                  `json:"EncryptionAlgorithms,omitempty"`
	KeyID                       string                    `json:"KeyId"`
	KeyManager                  string                    `json:"KeyManager"`
	KeySpec                     string                    `json:"KeySpec"`
	KeyState                    string                    `json:"KeyState"`
	KeyUsage                    string                    `json:"KeyUsage"`
	MacAlgorithms               []string                  `json:"MacAlgorithms,omitempty"`
	MultiRegion                 bool                      `json:"MultiRegion"`
	MultiRegionConfiguration    *MultiRegionConfiguration `json:"MultiRegionConfiguration,omitempty"`
	Origin                      string                    `json:"Origin"`
	PendingDeletionWindowInDays int32                     `json:"PendingDeletionWindowInDays,omitempty"`
	SigningAlgorithms           []string                  `json:"SigningAlgorithms,omitempty"`
}

// MultiRegionConfiguration describes a multi-Region key. Only primary keys
// are emulated, so ReplicaKeys is always empty.
type MultiRegionConfiguration struct {
	MultiRegionKeyType string           `json:"MultiRegionKeyType"`
	PrimaryKey         MultiRegionKey   `json:"PrimaryKey"`
	ReplicaKeys        []MultiRegionKey `json:"ReplicaKeys"`
}

type MultiRegionKey struct {
	Arn    string `json:"Arn"`
	Region string `json:"Region"`
}

// KeyIDInput is the request shape shared by every operation that only names a key.
type KeyIDInput struct {
	KeyID string `json:"KeyId"`
}

// DescribeKeyInput represents the request to describe a KMS key
type DescribeKeyInput struct {
	KeyID       string   `json:"KeyId"`
	GrantTokens []string `json:"GrantTokens,omitempty"`
}

// DescribeKeyOutput represents the response from describing a KMS key
type DescribeKeyOutput struct {
	KeyMetadata KeyMetadata `json:"KeyMetadata"`
}

// ListKeysInput represents the request to list keys
type ListKeysInput struct {
	Limit  int32  `json:"Limit,omitempty"`
	Marker string `json:"Marker,omitempty"`
}

type KeyListEntry struct {
	KeyArn string `json:"KeyArn"`
	KeyID  string `json:"KeyId"`
}

type ListKeysOutput struct {
	Keys       []KeyListEntry `json:"Keys"`
	NextMarker string         `json:"NextMarker,omitempty"`
	Truncated  bool           `json:"Truncated"`
}

// GetKeyPolicyInput represents the request to get a KMS key policy
type GetKeyPolicyInput struct {
	KeyID      string `json:"KeyId"`
	PolicyName string `json:"PolicyName,omitempty"`
}

// GetKeyPolicyOutput represents the response from getting a KMS key policy
type GetKeyPolicyOutput struct {
	Policy     string `json:"Policy"`
	PolicyName string `json:"PolicyName"`
}

type PutKeyPolicyInput struct {
	KeyID                          string `json:"KeyId"`
	Policy                         string `json:"Policy"`
	PolicyName                     string `json:"PolicyName,omitempty"`
	BypassPolicyLockoutSafetyCheck bool   `json:"BypassPolicyLockoutSafetyCheck,omitempty"`
}

type ListKeyPoliciesOutput struct {
	PolicyNames []string `json:"PolicyNames"`
	Truncated   bool     `json:"Truncated"`
}

type EnableKeyRotationInput struct {
	KeyID                string `json:"KeyId"`
	RotationPeriodInDays *int32 `json:"RotationPeriodInDays,omitempty"`
}

// GetKeyRotationStatusInput represents the request to get a KMS key rotation status
type GetKeyRotationStatusInput struct {
	KeyID string `json:"KeyId"`
}

// GetKeyRotationStatusOutput represents the response from getting a KMS key rotation status
type GetKeyRotationStatusOutput struct {
	KeyRotationEnabled   bool    `json:"KeyRotationEnabled"`
	KeyID                string  `json:"KeyId"`
	RotationPeriodInDays *int32  `json:"RotationPeriodInDays,omitempty"`
	NextRotationDate     float64 `json:"NextRotationDate,omitempty"`
}

type UpdateKeyDescriptionInput struct {
	KeyID       string `json:"KeyId"`
	Description string `json:"Description"`
}

// ListResourceTagsInput represents the request to list tags for a KMS key
type ListResourceTagsInput struct {
	KeyID  string `json:"KeyId"`
	Marker string `json:"Marker,omitempty"`
	Limit  int32  `json:"Limit,omitempty"`
}

// ListResourceTagsOutput represents the response from listing tags for a KMS key
type ListResourceTagsOutput struct {
	Tags       []Tag  `json:"Tags"`
	Truncated  bool   `json:"Truncated"`
	NextMarker string `json:"NextMarker,omitempty"`
}

type TagResourceInput struct {
	KeyID string `json:"KeyId"`
	Tags  []Tag  `json:"Tags"`
}

type UntagResourceInput struct {
	KeyID   string   `json:"KeyId"`
	TagKeys []string `json:"TagKeys"`
}

// ScheduleKeyDeletionInput represents the request to schedule key deletion
type ScheduleKeyDeletionInput struct {
	KeyID               string `json:"KeyId"`
	PendingWindowInDays int32  `json:"PendingWindowInDays,omitempty"`
}

// ScheduleKeyDeletionOutput represents the response from scheduling key deletion
type ScheduleKeyDeletionOutput struct {
	KeyID               string  `json:"KeyId"`
	DeletionDate        float64 `json:"DeletionDate"`
	KeyState            string  `json:"KeyState"`
	PendingWindowInDays int32   `json:"PendingWindowInDays"`
}

type CancelKeyDeletionOutput struct {
	KeyID string `json:"KeyId"`
}

// Aliases

type CreateAliasInput struct {
	AliasName   string `json:"AliasName"`
	TargetKeyID string `json:"TargetKeyId"`
}

type UpdateAliasInput = CreateAliasInput

type DeleteAliasInput struct {
	AliasName string `json:"AliasName"`
}

type ListAliasesInput struct {
	KeyID  string `json:"KeyId,omitempty"`
	Limit  int32  `json:"Limit,omitempty"`
	Marker string `json:"Marker,omitempty"`
}

type AliasListEntry struct {
	AliasArn        string  `json:"AliasArn"`
	AliasName       string  `json:"AliasName"`
	CreationDate    float64 `json:"CreationDate,omitempty"`
	LastUpdatedDate float64 `json:"LastUpdatedDate,omitempty"`
	TargetKeyID     string  `json:"TargetKeyId,omitempty"`
}

type ListAliasesOutput struct {
	Aliases    []AliasListEntry `json:"Aliases"`
	NextMarker string           `json:"NextMarker,omitempty"`
	Truncated  bool             `json:"Truncated"`
}

// EncryptInput represents the request to encrypt plaintext under a KMS key.
// Plaintext is a []byte so encoding/json handles the base64 blob encoding that
// the KMS wire format uses.
type EncryptInput struct {
	KeyID               string            `json:"KeyId"`
	Plaintext           []byte            `json:"Plaintext"`
	EncryptionContext   map[string]string `json:"EncryptionContext,omitempty"`
	EncryptionAlgorithm string            `json:"EncryptionAlgorithm,omitempty"`
	GrantTokens         []string          `json:"GrantTokens,omitempty"`
}

// EncryptOutput represents the response from encrypting plaintext
type EncryptOutput struct {
	CiphertextBlob      []byte `json:"CiphertextBlob"`
	KeyID               string `json:"KeyId"`
	EncryptionAlgorithm string `json:"EncryptionAlgorithm"`
}

// DecryptInput represents the request to decrypt a ciphertext blob. KeyId is
// optional for symmetric keys: the blob names the key it was sealed under.
type DecryptInput struct {
	CiphertextBlob      []byte            `json:"CiphertextBlob"`
	KeyID               string            `json:"KeyId,omitempty"`
	EncryptionContext   map[string]string `json:"EncryptionContext,omitempty"`
	EncryptionAlgorithm string            `json:"EncryptionAlgorithm,omitempty"`
	GrantTokens         []string          `json:"GrantTokens,omitempty"`
}

// DecryptOutput represents the response from decrypting a ciphertext blob
type DecryptOutput struct {
	KeyID               string `json:"KeyId"`
	Plaintext           []byte `json:"Plaintext"`
	EncryptionAlgorithm string `json:"EncryptionAlgorithm"`
}

// GenerateDataKeyInput is shared by GenerateDataKey and
// GenerateDataKeyWithoutPlaintext.
type GenerateDataKeyInput struct {
	KeyID             string            `json:"KeyId"`
	KeySpec           string            `json:"KeySpec,omitempty"`
	NumberOfBytes     *int32            `json:"NumberOfBytes,omitempty"`
	EncryptionContext map[string]string `json:"EncryptionContext,omitempty"`
	GrantTokens       []string          `json:"GrantTokens,omitempty"`
}

type GenerateDataKeyOutput struct {
	CiphertextBlob []byte `json:"CiphertextBlob"`
	KeyID          string `json:"KeyId"`
	Plaintext      []byte `json:"Plaintext,omitempty"`
}
