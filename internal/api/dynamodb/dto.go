// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package dynamodb

// Tag represents a DynamoDB tag
type Tag struct {
	Key   string `json:"Key"`
	Value string `json:"Value"`
}

// AttributeDefinition defines an attribute for the table
type AttributeDefinition struct {
	AttributeName string `json:"AttributeName"`
	AttributeType string `json:"AttributeType"` // S, N, B
}

// KeySchemaElement defines key schema
type KeySchemaElement struct {
	AttributeName string `json:"AttributeName"`
	KeyType       string `json:"KeyType"` // HASH, RANGE
}

// Projection defines what attributes are projected
type Projection struct {
	ProjectionType   string   `json:"ProjectionType"` // ALL, KEYS_ONLY, INCLUDE
	NonKeyAttributes []string `json:"NonKeyAttributes,omitempty"`
}

// ProvisionedThroughput defines throughput settings
type ProvisionedThroughput struct {
	ReadCapacityUnits      int64   `json:"ReadCapacityUnits"`
	WriteCapacityUnits     int64   `json:"WriteCapacityUnits"`
	NumberOfDecreasesToday int64   `json:"NumberOfDecreasesToday"`
	LastIncreaseDateTime   float64 `json:"LastIncreaseDateTime,omitempty"`
	LastDecreaseDateTime   float64 `json:"LastDecreaseDateTime,omitempty"`
}

// ProvisionedThroughputDescription describes throughput
type ProvisionedThroughputDescription struct {
	ReadCapacityUnits      int64   `json:"ReadCapacityUnits"`
	WriteCapacityUnits     int64   `json:"WriteCapacityUnits"`
	NumberOfDecreasesToday int64   `json:"NumberOfDecreasesToday"`
	LastIncreaseDateTime   float64 `json:"LastIncreaseDateTime,omitempty"`
	LastDecreaseDateTime   float64 `json:"LastDecreaseDateTime,omitempty"`
}

// GlobalSecondaryIndex defines a GSI
type GlobalSecondaryIndex struct {
	IndexName             string                 `json:"IndexName"`
	KeySchema             []KeySchemaElement     `json:"KeySchema"`
	Projection            Projection             `json:"Projection"`
	ProvisionedThroughput *ProvisionedThroughput `json:"ProvisionedThroughput,omitempty"`
}

// GlobalSecondaryIndexDescription describes a GSI
type GlobalSecondaryIndexDescription struct {
	IndexName             string                            `json:"IndexName"`
	KeySchema             []KeySchemaElement                `json:"KeySchema"`
	Projection            Projection                        `json:"Projection"`
	IndexStatus           string                            `json:"IndexStatus"`
	IndexArn              string                            `json:"IndexArn"`
	ProvisionedThroughput *ProvisionedThroughputDescription `json:"ProvisionedThroughput,omitempty"`
	ItemCount             int64                             `json:"ItemCount"`
	IndexSizeBytes        int64                             `json:"IndexSizeBytes"`
}

// LocalSecondaryIndex defines an LSI
type LocalSecondaryIndex struct {
	IndexName  string             `json:"IndexName"`
	KeySchema  []KeySchemaElement `json:"KeySchema"`
	Projection Projection         `json:"Projection"`
}

// LocalSecondaryIndexDescription describes an LSI
type LocalSecondaryIndexDescription struct {
	IndexName      string             `json:"IndexName"`
	KeySchema      []KeySchemaElement `json:"KeySchema"`
	Projection     Projection         `json:"Projection"`
	IndexArn       string             `json:"IndexArn"`
	ItemCount      int64              `json:"ItemCount"`
	IndexSizeBytes int64              `json:"IndexSizeBytes"`
}

// StreamSpecification defines stream settings
type StreamSpecification struct {
	StreamEnabled  bool   `json:"StreamEnabled"`
	StreamViewType string `json:"StreamViewType,omitempty"` // KEYS_ONLY, NEW_IMAGE, OLD_IMAGE, NEW_AND_OLD_IMAGES
}

// SSESpecification defines server-side encryption settings
type SSESpecification struct {
	Enabled        bool   `json:"Enabled"`
	SSEType        string `json:"SSEType,omitempty"` // AES256, KMS
	KMSMasterKeyId string `json:"KMSMasterKeyId,omitempty"`
}

// SSEDescription describes SSE status
type SSEDescription struct {
	Status          string `json:"Status"`
	SSEType         string `json:"SSEType,omitempty"`
	KMSMasterKeyArn string `json:"KMSMasterKeyArn,omitempty"`
}

// BillingModeSummary describes billing mode
type BillingModeSummary struct {
	BillingMode                       string  `json:"BillingMode"`
	LastUpdateToPayPerRequestDateTime float64 `json:"LastUpdateToPayPerRequestDateTime,omitempty"`
}

// TableDescription describes a DynamoDB table
type TableDescription struct {
	TableName                 string                            `json:"TableName"`
	TableArn                  string                            `json:"TableArn"`
	TableId                   string                            `json:"TableId"`
	TableStatus               string                            `json:"TableStatus"`
	CreationDateTime          float64                           `json:"CreationDateTime"`
	AttributeDefinitions      []AttributeDefinition             `json:"AttributeDefinitions"`
	KeySchema                 []KeySchemaElement                `json:"KeySchema"`
	ProvisionedThroughput     *ProvisionedThroughputDescription `json:"ProvisionedThroughput,omitempty"`
	BillingModeSummary        *BillingModeSummary               `json:"BillingModeSummary,omitempty"`
	GlobalSecondaryIndexes    []GlobalSecondaryIndexDescription `json:"GlobalSecondaryIndexes,omitempty"`
	LocalSecondaryIndexes     []LocalSecondaryIndexDescription  `json:"LocalSecondaryIndexes,omitempty"`
	StreamSpecification       *StreamSpecification              `json:"StreamSpecification,omitempty"`
	LatestStreamArn           string                            `json:"LatestStreamArn,omitempty"`
	LatestStreamLabel         string                            `json:"LatestStreamLabel,omitempty"`
	SSEDescription            *SSEDescription                   `json:"SSEDescription,omitempty"`
	ItemCount                 int64                             `json:"ItemCount"`
	TableSizeBytes            int64                             `json:"TableSizeBytes"`
	DeletionProtectionEnabled bool                              `json:"DeletionProtectionEnabled,omitempty"`
}

// CreateTableInput is the input for CreateTable
type CreateTableInput struct {
	TableName                 string                 `json:"TableName"`
	AttributeDefinitions      []AttributeDefinition  `json:"AttributeDefinitions"`
	KeySchema                 []KeySchemaElement     `json:"KeySchema"`
	ProvisionedThroughput     *ProvisionedThroughput `json:"ProvisionedThroughput,omitempty"`
	BillingMode               string                 `json:"BillingMode,omitempty"`
	GlobalSecondaryIndexes    []GlobalSecondaryIndex `json:"GlobalSecondaryIndexes,omitempty"`
	LocalSecondaryIndexes     []LocalSecondaryIndex  `json:"LocalSecondaryIndexes,omitempty"`
	StreamSpecification       *StreamSpecification   `json:"StreamSpecification,omitempty"`
	SSESpecification          *SSESpecification      `json:"SSESpecification,omitempty"`
	Tags                      []Tag                  `json:"Tags,omitempty"`
	DeletionProtectionEnabled bool                   `json:"DeletionProtectionEnabled,omitempty"`
}

// CreateTableOutput is the output for CreateTable
type CreateTableOutput struct {
	TableDescription TableDescription `json:"TableDescription"`
}

// DescribeTableOutput is the output for DescribeTable
type DescribeTableOutput struct {
	Table TableDescription `json:"Table"`
}

// DeleteTableOutput is the output for DeleteTable
type DeleteTableOutput struct {
	TableDescription TableDescription `json:"TableDescription"`
}

// ListTablesOutput is the output for ListTables
type ListTablesOutput struct {
	TableNames             []string `json:"TableNames"`
	LastEvaluatedTableName string   `json:"LastEvaluatedTableName,omitempty"`
}

// UpdateTableInput is the input for UpdateTable
type UpdateTableInput struct {
	TableName                   string                 `json:"TableName"`
	AttributeDefinitions        []AttributeDefinition  `json:"AttributeDefinitions,omitempty"`
	BillingMode                 string                 `json:"BillingMode,omitempty"`
	ProvisionedThroughput       *ProvisionedThroughput `json:"ProvisionedThroughput,omitempty"`
	GlobalSecondaryIndexUpdates []any                  `json:"GlobalSecondaryIndexUpdates,omitempty"`
	StreamSpecification         *StreamSpecification   `json:"StreamSpecification,omitempty"`
	SSESpecification            *SSESpecification      `json:"SSESpecification,omitempty"`
	DeletionProtectionEnabled   *bool                  `json:"DeletionProtectionEnabled,omitempty"`
}

// UpdateTableOutput is the output for UpdateTable
type UpdateTableOutput struct {
	TableDescription TableDescription `json:"TableDescription"`
}

// TimeToLiveDescription describes TTL settings
type TimeToLiveDescription struct {
	TimeToLiveStatus string `json:"TimeToLiveStatus"`
	AttributeName    string `json:"AttributeName,omitempty"`
}

// DescribeTimeToLiveOutput is the output for DescribeTimeToLive
type DescribeTimeToLiveOutput struct {
	TimeToLiveDescription TimeToLiveDescription `json:"TimeToLiveDescription"`
}

// UpdateTimeToLiveOutput is the output for UpdateTimeToLive
type UpdateTimeToLiveOutput struct {
	TimeToLiveSpecification TimeToLiveDescription `json:"TimeToLiveSpecification"`
}

// ListTagsOfResourceOutput is the output for ListTagsOfResource
type ListTagsOfResourceOutput struct {
	Tags      []Tag  `json:"Tags"`
	NextToken string `json:"NextToken,omitempty"`
}

// PointInTimeRecoveryDescription describes PITR status
type PointInTimeRecoveryDescription struct {
	PointInTimeRecoveryStatus  string  `json:"PointInTimeRecoveryStatus"`
	EarliestRestorableDateTime float64 `json:"EarliestRestorableDateTime,omitempty"`
	LatestRestorableDateTime   float64 `json:"LatestRestorableDateTime,omitempty"`
}

// ContinuousBackupsDescription describes continuous backup status
type ContinuousBackupsDescription struct {
	ContinuousBackupsStatus        string                         `json:"ContinuousBackupsStatus"`
	PointInTimeRecoveryDescription PointInTimeRecoveryDescription `json:"PointInTimeRecoveryDescription"`
}

// DescribeContinuousBackupsOutput is the output for DescribeContinuousBackups
type DescribeContinuousBackupsOutput struct {
	ContinuousBackupsDescription ContinuousBackupsDescription `json:"ContinuousBackupsDescription"`
}

// UpdateContinuousBackupsOutput is the output for UpdateContinuousBackups
type UpdateContinuousBackupsOutput struct {
	ContinuousBackupsDescription ContinuousBackupsDescription `json:"ContinuousBackupsDescription"`
}
