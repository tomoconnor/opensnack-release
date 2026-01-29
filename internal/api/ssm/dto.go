// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package ssm

// PutParameterInput represents the request to put a parameter
type PutParameterInput struct {
	Name        string `json:"Name"`
	Value       string `json:"Value"`
	Type        string `json:"Type,omitempty"`
	Description string `json:"Description,omitempty"`
	KeyId       string `json:"KeyId,omitempty"`
	Overwrite   bool   `json:"Overwrite,omitempty"`
	AllowedPattern string `json:"AllowedPattern,omitempty"`
	Tags        []Tag  `json:"Tags,omitempty"`
	Tier        string `json:"Tier,omitempty"`
	Policies    string `json:"Policies,omitempty"`
	DataType    string `json:"DataType,omitempty"`
}

// Tag represents an SSM tag
type Tag struct {
	Key   string `json:"Key"`
	Value string `json:"Value"`
}

// PutParameterOutput represents the response from putting a parameter
type PutParameterOutput struct {
	Version int64 `json:"Version"`
	Tier    string `json:"Tier,omitempty"`
}

// GetParameterInput represents the request to get a parameter
type GetParameterInput struct {
	Name            string `json:"Name"`
	WithDecryption bool   `json:"WithDecryption,omitempty"`
}

// GetParameterOutput represents the response from getting a parameter
type GetParameterOutput struct {
	Parameter Parameter `json:"Parameter"`
}

// Parameter represents an SSM parameter
type Parameter struct {
	Name        string  `json:"Name"`
	Type        string  `json:"Type"`
	Value       string  `json:"Value"`
	Version     int64   `json:"Version"`
	Selector    string  `json:"Selector,omitempty"`
	SourceResult string `json:"SourceResult,omitempty"`
	LastModifiedDate float64 `json:"LastModifiedDate"`
	ARN         string  `json:"ARN"`
	DataType    string  `json:"DataType,omitempty"`
}

// GetParametersInput represents the request to get multiple parameters
type GetParametersInput struct {
	Names           []string `json:"Names"`
	WithDecryption  bool     `json:"WithDecryption,omitempty"`
}

// GetParametersOutput represents the response from getting multiple parameters
type GetParametersOutput struct {
	Parameters            []Parameter `json:"Parameters"`
	InvalidParameters     []string    `json:"InvalidParameters,omitempty"`
}

// DescribeParametersInput represents the request to describe parameters
type DescribeParametersInput struct {
	ParameterFilters []ParameterFilter `json:"ParameterFilters,omitempty"`
	MaxResults       int32             `json:"MaxResults,omitempty"`
	NextToken        string            `json:"NextToken,omitempty"`
	Filters          []ParameterFilter `json:"Filters,omitempty"`
}

// ParameterFilter represents a filter for describing parameters
type ParameterFilter struct {
	Key    string   `json:"Key"`
	Values []string `json:"Values"`
	Option string   `json:"Option,omitempty"`
}

// DescribeParametersOutput represents the response from describing parameters
type DescribeParametersOutput struct {
	Parameters []ParameterMetadata `json:"Parameters"`
	NextToken  string              `json:"NextToken,omitempty"`
}

// ParameterMetadata represents parameter metadata (without value)
type ParameterMetadata struct {
	Name             string   `json:"Name"`
	Type             string   `json:"Type"`
	KeyId            string   `json:"KeyId,omitempty"`
	LastModifiedDate float64  `json:"LastModifiedDate"`
	LastModifiedUser string   `json:"LastModifiedUser,omitempty"`
	Description      string   `json:"Description,omitempty"`
	AllowedPattern   string   `json:"AllowedPattern,omitempty"`
	Version          int64    `json:"Version"`
	Tier             string   `json:"Tier,omitempty"`
	Policies         []Policy `json:"Policies,omitempty"`
	DataType         string   `json:"DataType,omitempty"`
	ARN              string   `json:"ARN,omitempty"`
}

// Policy represents a parameter policy
type Policy struct {
	PolicyText string `json:"PolicyText"`
	PolicyType string `json:"PolicyType"`
	PolicyStatus string `json:"PolicyStatus,omitempty"`
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

// DeleteParameterInput represents the request to delete a parameter
type DeleteParameterInput struct {
	Name string `json:"Name"`
}

// DeleteParameterOutput represents the response from deleting a parameter
type DeleteParameterOutput struct {
}

