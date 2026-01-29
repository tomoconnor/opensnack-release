// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package secretsmanager

// CreateSecretInput represents the request to create a secret
type CreateSecretInput struct {
	Name                    string            `json:"Name"`
	Description             string            `json:"Description,omitempty"`
	SecretString            string            `json:"SecretString,omitempty"`
	SecretBinary            string            `json:"SecretBinary,omitempty"`
	KmsKeyId                string            `json:"KmsKeyId,omitempty"`
	Tags                    []Tag             `json:"Tags,omitempty"`
	ClientRequestToken      string            `json:"ClientRequestToken,omitempty"`
	ReplicationRegionIds    []string          `json:"ReplicationRegionIds,omitempty"`
	AddReplicaRegions       []ReplicaRegion   `json:"AddReplicaRegions,omitempty"`
	ForceOverwriteReplicaSecret bool          `json:"ForceOverwriteReplicaSecret,omitempty"`
}

// Tag represents a SecretsManager tag
type Tag struct {
	Key   string `json:"Key"`
	Value string `json:"Value"`
}

// ReplicaRegion represents a replica region
type ReplicaRegion struct {
	Region     string `json:"Region"`
	KmsKeyId   string `json:"KmsKeyId,omitempty"`
}

// CreateSecretOutput represents the response from creating a secret
type CreateSecretOutput struct {
	ARN       string `json:"ARN"`
	Name      string `json:"Name"`
	VersionId string `json:"VersionId,omitempty"`
}

// DescribeSecretInput represents the request to describe a secret
type DescribeSecretInput struct {
	SecretId string `json:"SecretId"`
}

// DescribeSecretOutput represents the response from describing a secret
type DescribeSecretOutput struct {
	ARN                string            `json:"ARN"`
	Name               string            `json:"Name"`
	Description        string            `json:"Description,omitempty"`
	KmsKeyId           string            `json:"KmsKeyId,omitempty"`
	RotationEnabled    bool              `json:"RotationEnabled,omitempty"`
	RotationLambdaARN  string            `json:"RotationLambdaARN,omitempty"`
	RotationRules      *RotationRules    `json:"RotationRules,omitempty"`
	LastRotatedDate    float64           `json:"LastRotatedDate,omitempty"`
	LastChangedDate    float64           `json:"LastChangedDate,omitempty"`
	LastAccessedDate   float64           `json:"LastAccessedDate,omitempty"`
	DeletedDate        float64           `json:"DeletedDate,omitempty"`
	NextRotationDate   float64           `json:"NextRotationDate,omitempty"`
	Tags               []Tag             `json:"Tags,omitempty"`
	VersionIdsToStages map[string][]string `json:"VersionIdsToStages,omitempty"`
	OwningService      string            `json:"OwningService,omitempty"`
	CreatedDate        float64           `json:"CreatedDate"`
	PrimaryRegion      string            `json:"PrimaryRegion,omitempty"`
	ReplicationStatus  []ReplicationStatus `json:"ReplicationStatus,omitempty"`
}

// RotationRules represents rotation rules
type RotationRules struct {
	AutomaticallyAfterDays int64 `json:"AutomaticallyAfterDays,omitempty"`
	Duration               string `json:"Duration,omitempty"`
	ScheduleExpression     string `json:"ScheduleExpression,omitempty"`
}

// ReplicationStatus represents replication status
type ReplicationStatus struct {
	Region         string `json:"Region"`
	KmsKeyId       string `json:"KmsKeyId,omitempty"`
	Status         string `json:"Status"`
	StatusMessage  string `json:"StatusMessage,omitempty"`
	LastAccessedDate float64 `json:"LastAccessedDate,omitempty"`
}

// GetSecretValueInput represents the request to get a secret value
type GetSecretValueInput struct {
	SecretId     string `json:"SecretId"`
	VersionId    string `json:"VersionId,omitempty"`
	VersionStage string `json:"VersionStage,omitempty"`
}

// GetSecretValueOutput represents the response from getting a secret value
type GetSecretValueOutput struct {
	ARN           string            `json:"ARN"`
	Name          string            `json:"Name"`
	VersionId     string            `json:"VersionId"`
	SecretBinary  string            `json:"SecretBinary,omitempty"`
	SecretString  string            `json:"SecretString,omitempty"`
	VersionStages []string          `json:"VersionStages"`
	CreatedDate   float64           `json:"CreatedDate"`
}

// PutSecretValueInput represents the request to put a secret value
type PutSecretValueInput struct {
	SecretId          string `json:"SecretId"`
	SecretString      string `json:"SecretString,omitempty"`
	SecretBinary      string `json:"SecretBinary,omitempty"`
	ClientRequestToken string `json:"ClientRequestToken,omitempty"`
	VersionStages     []string `json:"VersionStages,omitempty"`
}

// PutSecretValueOutput represents the response from putting a secret value
type PutSecretValueOutput struct {
	ARN       string `json:"ARN"`
	Name      string `json:"Name"`
	VersionId string `json:"VersionId"`
	VersionStages []string `json:"VersionStages,omitempty"`
}

// ListSecretsInput represents the request to list secrets
type ListSecretsInput struct {
	MaxResults int32  `json:"MaxResults,omitempty"`
	NextToken  string `json:"NextToken,omitempty"`
	Filters    []Filter `json:"Filters,omitempty"`
	SortOrder  string `json:"SortOrder,omitempty"`
}

// Filter represents a filter for listing secrets
type Filter struct {
	Key    string   `json:"Key"`
	Values []string `json:"Values"`
}

// ListSecretsOutput represents the response from listing secrets
type ListSecretsOutput struct {
	SecretList []SecretListEntry `json:"SecretList"`
	NextToken  string            `json:"NextToken,omitempty"`
}

// SecretListEntry represents a secret in the list
type SecretListEntry struct {
	ARN                string            `json:"ARN"`
	Name               string            `json:"Name"`
	Description        string            `json:"Description,omitempty"`
	KmsKeyId           string            `json:"KmsKeyId,omitempty"`
	RotationEnabled    bool              `json:"RotationEnabled,omitempty"`
	RotationLambdaARN  string            `json:"RotationLambdaARN,omitempty"`
	RotationRules      *RotationRules    `json:"RotationRules,omitempty"`
	LastRotatedDate    float64           `json:"LastRotatedDate,omitempty"`
	LastChangedDate    float64           `json:"LastChangedDate,omitempty"`
	LastAccessedDate   float64           `json:"LastAccessedDate,omitempty"`
	DeletedDate        float64           `json:"DeletedDate,omitempty"`
	NextRotationDate   float64           `json:"NextRotationDate,omitempty"`
	Tags               []Tag             `json:"Tags,omitempty"`
	SecretVersionsToStages map[string][]string `json:"SecretVersionsToStages,omitempty"`
	OwningService      string            `json:"OwningService,omitempty"`
	CreatedDate        float64           `json:"CreatedDate"`
	PrimaryRegion      string            `json:"PrimaryRegion,omitempty"`
}

// DeleteSecretInput represents the request to delete a secret
type DeleteSecretInput struct {
	SecretId                   string `json:"SecretId"`
	RecoveryWindowInDays       int64  `json:"RecoveryWindowInDays,omitempty"`
	ForceDeleteWithoutRecovery bool   `json:"ForceDeleteWithoutRecovery,omitempty"`
}

// DeleteSecretOutput represents the response from deleting a secret
type DeleteSecretOutput struct {
	ARN       string  `json:"ARN"`
	Name      string  `json:"Name"`
	DeletionDate float64 `json:"DeletionDate,omitempty"`
}

// GetResourcePolicyInput represents the request to get a secret resource policy
type GetResourcePolicyInput struct {
	SecretId string `json:"SecretId"`
}

// GetResourcePolicyOutput represents the response from getting a secret resource policy
type GetResourcePolicyOutput struct {
	ARN    string `json:"ARN"`
	Name   string `json:"Name"`
	Policy string `json:"ResourcePolicy"`
}

