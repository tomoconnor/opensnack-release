// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package ssm

// PutParameterInput represents the request to put a parameter. The optional
// fields are pointers because an overwrite keeps whatever it does not name.
type PutParameterInput struct {
	Name           string  `json:"Name"`
	Value          *string `json:"Value"`
	Type           string  `json:"Type,omitempty"`
	Description    *string `json:"Description,omitempty"`
	KeyId          string  `json:"KeyId,omitempty"`
	Overwrite      bool    `json:"Overwrite,omitempty"`
	AllowedPattern *string `json:"AllowedPattern,omitempty"`
	Tags           []Tag   `json:"Tags,omitempty"`
	Tier           string  `json:"Tier,omitempty"`
	Policies       *string `json:"Policies,omitempty"`
	DataType       string  `json:"DataType,omitempty"`
}

// Tag represents an SSM tag
type Tag struct {
	Key   string `json:"Key"`
	Value string `json:"Value"`
}

// PutParameterOutput represents the response from putting a parameter
type PutParameterOutput struct {
	Version int64  `json:"Version"`
	Tier    string `json:"Tier,omitempty"`
}

// GetParameterInput represents the request to get a parameter
type GetParameterInput struct {
	Name           string `json:"Name"`
	WithDecryption bool   `json:"WithDecryption,omitempty"`
}

// GetParameterOutput represents the response from getting a parameter
type GetParameterOutput struct {
	Parameter Parameter `json:"Parameter"`
}

// Parameter represents an SSM parameter
type Parameter struct {
	Name             string  `json:"Name"`
	Type             string  `json:"Type"`
	Value            string  `json:"Value"`
	Version          int64   `json:"Version"`
	Selector         string  `json:"Selector,omitempty"`
	SourceResult     string  `json:"SourceResult,omitempty"`
	LastModifiedDate float64 `json:"LastModifiedDate"`
	ARN              string  `json:"ARN"`
	DataType         string  `json:"DataType,omitempty"`
}

// GetParametersInput represents the request to get multiple parameters
type GetParametersInput struct {
	Names          []string `json:"Names"`
	WithDecryption bool     `json:"WithDecryption,omitempty"`
}

// GetParametersOutput represents the response from getting multiple parameters
type GetParametersOutput struct {
	Parameters        []Parameter `json:"Parameters"`
	InvalidParameters []string    `json:"InvalidParameters"`
}

type GetParametersByPathInput struct {
	Path             string            `json:"Path"`
	Recursive        bool              `json:"Recursive,omitempty"`
	ParameterFilters []ParameterFilter `json:"ParameterFilters,omitempty"`
	WithDecryption   bool              `json:"WithDecryption,omitempty"`
	MaxResults       int32             `json:"MaxResults,omitempty"`
	NextToken        string            `json:"NextToken,omitempty"`
}

type GetParametersByPathOutput struct {
	Parameters []Parameter `json:"Parameters"`
	NextToken  string      `json:"NextToken,omitempty"`
}

// DescribeParametersInput represents the request to describe parameters
type DescribeParametersInput struct {
	ParameterFilters []ParameterFilter `json:"ParameterFilters,omitempty"`
	MaxResults       int32             `json:"MaxResults,omitempty"`
	NextToken        string            `json:"NextToken,omitempty"`
	Filters          []LegacyFilter    `json:"Filters,omitempty"`
}

// ParameterFilter is a ParameterStringFilter
type ParameterFilter struct {
	Key    string   `json:"Key"`
	Values []string `json:"Values,omitempty"`
	Option string   `json:"Option,omitempty"`
}

// LegacyFilter is the older ParametersFilter (Name, Type or KeyId, Equals)
type LegacyFilter struct {
	Key    string   `json:"Key"`
	Values []string `json:"Values"`
}

// DescribeParametersOutput represents the response from describing parameters
type DescribeParametersOutput struct {
	Parameters []ParameterMetadata `json:"Parameters"`
	NextToken  string              `json:"NextToken,omitempty"`
}

// ParameterMetadata represents parameter metadata (without value)
type ParameterMetadata struct {
	Name             string   `json:"Name"`
	ARN              string   `json:"ARN"`
	Type             string   `json:"Type"`
	KeyId            string   `json:"KeyId,omitempty"`
	LastModifiedDate float64  `json:"LastModifiedDate"`
	LastModifiedUser string   `json:"LastModifiedUser,omitempty"`
	Description      string   `json:"Description,omitempty"`
	AllowedPattern   string   `json:"AllowedPattern,omitempty"`
	Version          int64    `json:"Version"`
	Tier             string   `json:"Tier"`
	Policies         []Policy `json:"Policies"`
	DataType         string   `json:"DataType"`
}

// Policy represents a parameter policy
type Policy struct {
	PolicyText   string `json:"PolicyText"`
	PolicyType   string `json:"PolicyType"`
	PolicyStatus string `json:"PolicyStatus,omitempty"`
}

type GetParameterHistoryInput struct {
	Name           string `json:"Name"`
	WithDecryption bool   `json:"WithDecryption,omitempty"`
	MaxResults     int32  `json:"MaxResults,omitempty"`
	NextToken      string `json:"NextToken,omitempty"`
}

type ParameterHistory struct {
	Name             string   `json:"Name"`
	Type             string   `json:"Type"`
	KeyId            string   `json:"KeyId,omitempty"`
	LastModifiedDate float64  `json:"LastModifiedDate"`
	LastModifiedUser string   `json:"LastModifiedUser,omitempty"`
	Description      string   `json:"Description,omitempty"`
	Value            string   `json:"Value"`
	AllowedPattern   string   `json:"AllowedPattern,omitempty"`
	Version          int64    `json:"Version"`
	Labels           []string `json:"Labels"`
	Tier             string   `json:"Tier"`
	Policies         []Policy `json:"Policies"`
	DataType         string   `json:"DataType"`
}

type GetParameterHistoryOutput struct {
	Parameters []ParameterHistory `json:"Parameters"`
	NextToken  string             `json:"NextToken,omitempty"`
}

type LabelParameterVersionInput struct {
	Name             string   `json:"Name"`
	ParameterVersion *int64   `json:"ParameterVersion,omitempty"`
	Labels           []string `json:"Labels"`
}

type LabelParameterVersionOutput struct {
	InvalidLabels    []string `json:"InvalidLabels"`
	ParameterVersion int64    `json:"ParameterVersion"`
}

type UnlabelParameterVersionInput struct {
	Name             string   `json:"Name"`
	ParameterVersion int64    `json:"ParameterVersion"`
	Labels           []string `json:"Labels"`
}

type UnlabelParameterVersionOutput struct {
	InvalidLabels []string `json:"InvalidLabels"`
	RemovedLabels []string `json:"RemovedLabels"`
}

// ListTagsForResourceInput represents the request to list tags for a resource
type ListTagsForResourceInput struct {
	ResourceId   string `json:"ResourceId"`
	ResourceType string `json:"ResourceType"`
}

// ListTagsForResourceOutput represents the response from listing tags for a resource
type ListTagsForResourceOutput struct {
	TagList []Tag `json:"TagList"`
}

type AddTagsToResourceInput struct {
	ResourceId   string `json:"ResourceId"`
	ResourceType string `json:"ResourceType"`
	Tags         []Tag  `json:"Tags"`
}

type RemoveTagsFromResourceInput struct {
	ResourceId   string   `json:"ResourceId"`
	ResourceType string   `json:"ResourceType"`
	TagKeys      []string `json:"TagKeys"`
}

// DeleteParameterInput represents the request to delete a parameter
type DeleteParameterInput struct {
	Name string `json:"Name"`
}

type DeleteParametersInput struct {
	Names []string `json:"Names"`
}

type DeleteParametersOutput struct {
	DeletedParameters []string `json:"DeletedParameters"`
	InvalidParameters []string `json:"InvalidParameters"`
}
