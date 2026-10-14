// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package secretsmanager

// Tag represents a SecretsManager tag
type Tag struct {
	Key   string `json:"Key"`
	Value string `json:"Value"`
}

// ReplicaRegion represents a replica region
type ReplicaRegion struct {
	Region   string `json:"Region"`
	KmsKeyId string `json:"KmsKeyId,omitempty"`
}

// CreateSecretInput represents the request to create a secret.
// SecretBinary is a blob, which the JSON protocol carries as base64; it is
// kept in that encoded form throughout.
type CreateSecretInput struct {
	Name                        string          `json:"Name"`
	Description                 string          `json:"Description,omitempty"`
	SecretString                *string         `json:"SecretString,omitempty"`
	SecretBinary                *string         `json:"SecretBinary,omitempty"`
	KmsKeyId                    string          `json:"KmsKeyId,omitempty"`
	Tags                        []Tag           `json:"Tags,omitempty"`
	ClientRequestToken          string          `json:"ClientRequestToken,omitempty"`
	AddReplicaRegions           []ReplicaRegion `json:"AddReplicaRegions,omitempty"`
	ForceOverwriteReplicaSecret bool            `json:"ForceOverwriteReplicaSecret,omitempty"`
	Type                        string          `json:"Type,omitempty"`
}

// CreateSecretOutput represents the response from creating a secret
type CreateSecretOutput struct {
	ARN       string `json:"ARN"`
	Name      string `json:"Name"`
	VersionId string `json:"VersionId,omitempty"`
}

// SecretIdInput is the request shape of every operation that only names a secret.
type SecretIdInput struct {
	SecretId string `json:"SecretId"`
}

// ARNNameOutput is the response of the operations that only echo the secret.
type ARNNameOutput struct {
	ARN  string `json:"ARN"`
	Name string `json:"Name"`
}

// DescribeSecretOutput represents the response from describing a secret
type DescribeSecretOutput struct {
	ARN                string              `json:"ARN"`
	Name               string              `json:"Name"`
	Description        string              `json:"Description,omitempty"`
	KmsKeyId           string              `json:"KmsKeyId,omitempty"`
	RotationEnabled    bool                `json:"RotationEnabled"`
	LastChangedDate    float64             `json:"LastChangedDate,omitempty"`
	LastAccessedDate   float64             `json:"LastAccessedDate,omitempty"`
	DeletedDate        float64             `json:"DeletedDate,omitempty"`
	Tags               []Tag               `json:"Tags,omitempty"`
	VersionIdsToStages map[string][]string `json:"VersionIdsToStages,omitempty"`
	CreatedDate        float64             `json:"CreatedDate"`
	Type               string              `json:"Type,omitempty"`
}

// GetSecretValueInput represents the request to get a secret value
type GetSecretValueInput struct {
	SecretId     string `json:"SecretId"`
	VersionId    string `json:"VersionId,omitempty"`
	VersionStage string `json:"VersionStage,omitempty"`
}

// GetSecretValueOutput represents the response from getting a secret value
type GetSecretValueOutput struct {
	ARN           string   `json:"ARN"`
	Name          string   `json:"Name"`
	VersionId     string   `json:"VersionId"`
	SecretBinary  *string  `json:"SecretBinary,omitempty"`
	SecretString  *string  `json:"SecretString,omitempty"`
	VersionStages []string `json:"VersionStages"`
	CreatedDate   float64  `json:"CreatedDate"`
}

// PutSecretValueInput represents the request to put a secret value
type PutSecretValueInput struct {
	SecretId           string   `json:"SecretId"`
	SecretString       *string  `json:"SecretString,omitempty"`
	SecretBinary       *string  `json:"SecretBinary,omitempty"`
	ClientRequestToken string   `json:"ClientRequestToken,omitempty"`
	VersionStages      []string `json:"VersionStages,omitempty"`
}

// PutSecretValueOutput represents the response from putting a secret value
type PutSecretValueOutput struct {
	ARN           string   `json:"ARN"`
	Name          string   `json:"Name"`
	VersionId     string   `json:"VersionId"`
	VersionStages []string `json:"VersionStages"`
}

// UpdateSecretInput changes a secret's metadata and, with a value, adds a version.
type UpdateSecretInput struct {
	SecretId           string  `json:"SecretId"`
	Description        *string `json:"Description,omitempty"`
	KmsKeyId           *string `json:"KmsKeyId,omitempty"`
	SecretString       *string `json:"SecretString,omitempty"`
	SecretBinary       *string `json:"SecretBinary,omitempty"`
	ClientRequestToken string  `json:"ClientRequestToken,omitempty"`
	Type               *string `json:"Type,omitempty"`
}

type UpdateSecretOutput struct {
	ARN       string `json:"ARN"`
	Name      string `json:"Name"`
	VersionId string `json:"VersionId,omitempty"`
}

type UpdateSecretVersionStageInput struct {
	SecretId            string `json:"SecretId"`
	VersionStage        string `json:"VersionStage"`
	MoveToVersionId     string `json:"MoveToVersionId,omitempty"`
	RemoveFromVersionId string `json:"RemoveFromVersionId,omitempty"`
}

type ListSecretVersionIdsInput struct {
	SecretId          string `json:"SecretId"`
	MaxResults        int32  `json:"MaxResults,omitempty"`
	NextToken         string `json:"NextToken,omitempty"`
	IncludeDeprecated bool   `json:"IncludeDeprecated,omitempty"`
}

type SecretVersionsListEntry struct {
	VersionId     string   `json:"VersionId"`
	VersionStages []string `json:"VersionStages,omitempty"`
	CreatedDate   float64  `json:"CreatedDate"`
	KmsKeyIds     []string `json:"KmsKeyIds,omitempty"`
}

type ListSecretVersionIdsOutput struct {
	ARN       string                    `json:"ARN"`
	Name      string                    `json:"Name"`
	Versions  []SecretVersionsListEntry `json:"Versions"`
	NextToken string                    `json:"NextToken,omitempty"`
}

// ListSecretsInput represents the request to list secrets
type ListSecretsInput struct {
	MaxResults             int32    `json:"MaxResults,omitempty"`
	NextToken              string   `json:"NextToken,omitempty"`
	Filters                []Filter `json:"Filters,omitempty"`
	SortOrder              string   `json:"SortOrder,omitempty"`
	IncludePlannedDeletion bool     `json:"IncludePlannedDeletion,omitempty"`
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
	ARN                    string              `json:"ARN"`
	Name                   string              `json:"Name"`
	Description            string              `json:"Description,omitempty"`
	KmsKeyId               string              `json:"KmsKeyId,omitempty"`
	RotationEnabled        bool                `json:"RotationEnabled"`
	LastChangedDate        float64             `json:"LastChangedDate,omitempty"`
	DeletedDate            float64             `json:"DeletedDate,omitempty"`
	Tags                   []Tag               `json:"Tags,omitempty"`
	SecretVersionsToStages map[string][]string `json:"SecretVersionsToStages,omitempty"`
	CreatedDate            float64             `json:"CreatedDate"`
	Type                   string              `json:"Type,omitempty"`
}

// DeleteSecretInput represents the request to delete a secret
type DeleteSecretInput struct {
	SecretId                   string `json:"SecretId"`
	RecoveryWindowInDays       *int64 `json:"RecoveryWindowInDays,omitempty"`
	ForceDeleteWithoutRecovery *bool  `json:"ForceDeleteWithoutRecovery,omitempty"`
}

// DeleteSecretOutput represents the response from deleting a secret
type DeleteSecretOutput struct {
	ARN          string  `json:"ARN"`
	Name         string  `json:"Name"`
	DeletionDate float64 `json:"DeletionDate"`
}

type PutResourcePolicyInput struct {
	SecretId          string `json:"SecretId"`
	ResourcePolicy    string `json:"ResourcePolicy"`
	BlockPublicPolicy *bool  `json:"BlockPublicPolicy,omitempty"`
}

// GetResourcePolicyOutput represents the response from getting a secret
// resource policy. ResourcePolicy is absent when the secret has none.
type GetResourcePolicyOutput struct {
	ARN            string `json:"ARN"`
	Name           string `json:"Name"`
	ResourcePolicy string `json:"ResourcePolicy,omitempty"`
}

type ValidateResourcePolicyInput struct {
	SecretId       string `json:"SecretId,omitempty"`
	ResourcePolicy string `json:"ResourcePolicy"`
}

type ValidationErrorsEntry struct {
	CheckName    string `json:"CheckName"`
	ErrorMessage string `json:"ErrorMessage"`
}

type ValidateResourcePolicyOutput struct {
	PolicyValidationPassed bool                    `json:"PolicyValidationPassed"`
	ValidationErrors       []ValidationErrorsEntry `json:"ValidationErrors"`
}

type TagResourceInput struct {
	SecretId string `json:"SecretId"`
	Tags     []Tag  `json:"Tags"`
}

type UntagResourceInput struct {
	SecretId string   `json:"SecretId"`
	TagKeys  []string `json:"TagKeys"`
}

type GetRandomPasswordInput struct {
	PasswordLength          *int64 `json:"PasswordLength,omitempty"`
	ExcludeCharacters       string `json:"ExcludeCharacters,omitempty"`
	ExcludeNumbers          bool   `json:"ExcludeNumbers,omitempty"`
	ExcludePunctuation      bool   `json:"ExcludePunctuation,omitempty"`
	ExcludeUppercase        bool   `json:"ExcludeUppercase,omitempty"`
	ExcludeLowercase        bool   `json:"ExcludeLowercase,omitempty"`
	IncludeSpace            bool   `json:"IncludeSpace,omitempty"`
	RequireEachIncludedType *bool  `json:"RequireEachIncludedType,omitempty"`
}
