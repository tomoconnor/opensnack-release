// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package lambda

// Wire shapes of the Lambda REST-JSON API. Go field names are the AWS member
// names, so these marshal to exactly what the SDKs expect, and the store
// keeps them in the same form.

type FunctionConfiguration struct {
	FunctionName         string
	FunctionArn          string
	Runtime              string `json:",omitempty"`
	Role                 string
	Handler              string `json:",omitempty"`
	CodeSize             int64
	Description          string
	Timeout              int
	MemorySize           int
	LastModified         string
	CodeSha256           string
	Version              string
	VpcConfig            *VpcConfig            `json:",omitempty"`
	DeadLetterConfig     *DeadLetterConfig     `json:",omitempty"`
	Environment          *Environment          `json:",omitempty"`
	KMSKeyArn            string                `json:",omitempty"`
	TracingConfig        *TracingConfig        `json:",omitempty"`
	RevisionId           string                `json:",omitempty"`
	Layers               []Layer               `json:",omitempty"`
	State                string                `json:",omitempty"`
	StateReason          string                `json:",omitempty"`
	StateReasonCode      string                `json:",omitempty"`
	LastUpdateStatus     string                `json:",omitempty"`
	PackageType          string                `json:",omitempty"`
	ImageConfigResponse  *ImageConfigResponse  `json:",omitempty"`
	Architectures        []string              `json:",omitempty"`
	EphemeralStorage     *EphemeralStorage     `json:",omitempty"`
	SnapStart            *SnapStartResponse    `json:",omitempty"`
	RuntimeVersionConfig *RuntimeVersionConfig `json:",omitempty"`
	LoggingConfig        *LoggingConfig        `json:",omitempty"`
	FileSystemConfigs    []FileSystemConfig    `json:",omitempty"`
}

type VpcConfig struct {
	SubnetIds               []string
	SecurityGroupIds        []string
	VpcId                   string `json:",omitempty"`
	Ipv6AllowedForDualStack bool
}

type DeadLetterConfig struct {
	TargetArn string `json:",omitempty"`
}

type Environment struct {
	Variables map[string]string
}

type TracingConfig struct {
	Mode string `json:",omitempty"`
}

type Layer struct {
	Arn      string
	CodeSize int64
}

type ImageConfig struct {
	EntryPoint       []string `json:",omitempty"`
	Command          []string `json:",omitempty"`
	WorkingDirectory string   `json:",omitempty"`
}

type ImageConfigResponse struct {
	ImageConfig *ImageConfig `json:",omitempty"`
}

type EphemeralStorage struct {
	Size int
}

type SnapStart struct {
	ApplyOn string `json:",omitempty"`
}

type SnapStartResponse struct {
	ApplyOn            string
	OptimizationStatus string
}

type RuntimeVersionConfig struct {
	RuntimeVersionArn string
}

type LoggingConfig struct {
	LogFormat           string `json:",omitempty"`
	ApplicationLogLevel string `json:",omitempty"`
	SystemLogLevel      string `json:",omitempty"`
	LogGroup            string `json:",omitempty"`
}

type FileSystemConfig struct {
	Arn            string
	LocalMountPath string
}

type FunctionCode struct {
	ZipFile         []byte `json:",omitempty"`
	S3Bucket        string `json:",omitempty"`
	S3Key           string `json:",omitempty"`
	S3ObjectVersion string `json:",omitempty"`
	ImageUri        string `json:",omitempty"`
}

type FunctionCodeLocation struct {
	RepositoryType string
	Location       string `json:",omitempty"`
	ImageUri       string `json:",omitempty"`
}

type AliasConfiguration struct {
	AliasArn        string
	Name            string
	FunctionVersion string
	Description     string
	RoutingConfig   *AliasRoutingConfiguration `json:",omitempty"`
	RevisionId      string
}

type AliasRoutingConfiguration struct {
	AdditionalVersionWeights map[string]float64 `json:",omitempty"`
}

type FunctionUrlConfig struct {
	FunctionUrl      string
	FunctionArn      string
	AuthType         string
	Cors             *Cors `json:",omitempty"`
	CreationTime     string
	LastModifiedTime string
	InvokeMode       string
}

type Cors struct {
	AllowCredentials bool     `json:",omitempty"`
	AllowHeaders     []string `json:",omitempty"`
	AllowMethods     []string `json:",omitempty"`
	AllowOrigins     []string `json:",omitempty"`
	ExposeHeaders    []string `json:",omitempty"`
	MaxAge           int      `json:",omitempty"`
}

type DestinationConfig struct {
	OnSuccess *Destination `json:",omitempty"`
	OnFailure *Destination `json:",omitempty"`
}

type Destination struct {
	Destination string `json:",omitempty"`
}

type FunctionEventInvokeConfig struct {
	LastModified             float64
	FunctionArn              string
	MaximumRetryAttempts     *int `json:",omitempty"`
	MaximumEventAgeInSeconds *int `json:",omitempty"`
	DestinationConfig        *DestinationConfig
}

type FilterCriteria struct {
	Filters []Filter `json:",omitempty"`
}

type Filter struct {
	Pattern string `json:",omitempty"`
}

type ScalingConfig struct {
	MaximumConcurrency int `json:",omitempty"`
}

// EventSourceMappingConfiguration covers the SQS and stream sources. Other
// source-specific blocks (Kafka, MQ, DocumentDB) are kept verbatim.
type EventSourceMappingConfiguration struct {
	UUID                           string
	EventSourceMappingArn          string
	EventSourceArn                 string `json:",omitempty"`
	FunctionArn                    string
	BatchSize                      int
	MaximumBatchingWindowInSeconds int
	ParallelizationFactor          *int               `json:",omitempty"`
	StartingPosition               string             `json:",omitempty"`
	StartingPositionTimestamp      *float64           `json:",omitempty"`
	MaximumRecordAgeInSeconds      *int               `json:",omitempty"`
	BisectBatchOnFunctionError     *bool              `json:",omitempty"`
	MaximumRetryAttempts           *int               `json:",omitempty"`
	TumblingWindowInSeconds        *int               `json:",omitempty"`
	DestinationConfig              *DestinationConfig `json:",omitempty"`
	FilterCriteria                 *FilterCriteria    `json:",omitempty"`
	FunctionResponseTypes          []string
	ScalingConfig                  *ScalingConfig `json:",omitempty"`
	KMSKeyArn                      string         `json:",omitempty"`
	Topics                         []string       `json:",omitempty"`
	Queues                         []string       `json:",omitempty"`
	LastModified                   float64
	LastProcessingResult           string `json:",omitempty"`
	State                          string
	StateTransitionReason          string
	MetricsConfig                  any `json:",omitempty"`
	SourceAccessConfigurations     any `json:",omitempty"`
	SelfManagedEventSource         any `json:",omitempty"`

	AmazonManagedKafkaEventSourceConfig any `json:",omitempty"`
	SelfManagedKafkaEventSourceConfig   any `json:",omitempty"`
	DocumentDBEventSourceConfig         any `json:",omitempty"`
	ProvisionedPollerConfig             any `json:",omitempty"`
}

//
// Stored records
//

// functionRecord is the "function" row: $LATEST plus everything that hangs
// off the function as a whole.
type functionRecord struct {
	Config               FunctionConfiguration
	CodeBlob             string            `json:"code_blob"`
	ImageUri             string            `json:"image_uri,omitempty"`
	Tags                 map[string]string `json:"tags,omitempty"`
	Concurrency          *int              `json:"concurrency,omitempty"`
	CodeSigningConfigArn string            `json:"code_signing_config_arn,omitempty"`
	RecursiveLoop        string            `json:"recursive_loop,omitempty"`
	LastVersion          int               `json:"last_version"`
}

// versionRecord is a published version ("name:N"): a frozen copy of the
// configuration and the code it pointed at.
type versionRecord struct {
	Config   FunctionConfiguration
	CodeBlob string `json:"code_blob"`
}

// policyRecord is the resource policy of a function or one qualifier of it.
type policyRecord struct {
	RevisionId string
	Statements []policyStatement
}

type policyStatement struct {
	Sid       string
	Effect    string
	Principal any
	Action    string
	Resource  string
	Condition map[string]map[string]string `json:",omitempty"`
}

type esmRecord struct {
	Config EventSourceMappingConfiguration
	Tags   map[string]string `json:"tags,omitempty"`
}
