// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package dynamodb

type Tag struct {
	Key   string `json:"Key"`
	Value string `json:"Value"`
}

type AttributeDefinition struct {
	AttributeName string `json:"AttributeName"`
	AttributeType string `json:"AttributeType"`
}

type KeySchemaElement struct {
	AttributeName string `json:"AttributeName"`
	KeyType       string `json:"KeyType"`
}

type Projection struct {
	ProjectionType   string   `json:"ProjectionType,omitempty"`
	NonKeyAttributes []string `json:"NonKeyAttributes,omitempty"`
}

type ProvisionedThroughput struct {
	ReadCapacityUnits  int64 `json:"ReadCapacityUnits"`
	WriteCapacityUnits int64 `json:"WriteCapacityUnits"`
}

type ProvisionedThroughputDescription struct {
	NumberOfDecreasesToday int64    `json:"NumberOfDecreasesToday"`
	ReadCapacityUnits      int64    `json:"ReadCapacityUnits"`
	WriteCapacityUnits     int64    `json:"WriteCapacityUnits"`
	LastIncreaseDateTime   *float64 `json:"LastIncreaseDateTime,omitempty"`
	LastDecreaseDateTime   *float64 `json:"LastDecreaseDateTime,omitempty"`
}

type OnDemandThroughput struct {
	MaxReadRequestUnits  *int64 `json:"MaxReadRequestUnits,omitempty"`
	MaxWriteRequestUnits *int64 `json:"MaxWriteRequestUnits,omitempty"`
}

type WarmThroughput struct {
	ReadUnitsPerSecond  *int64 `json:"ReadUnitsPerSecond,omitempty"`
	WriteUnitsPerSecond *int64 `json:"WriteUnitsPerSecond,omitempty"`
}

type WarmThroughputDescription struct {
	ReadUnitsPerSecond  int64  `json:"ReadUnitsPerSecond"`
	WriteUnitsPerSecond int64  `json:"WriteUnitsPerSecond"`
	Status              string `json:"Status"`
}

type GlobalSecondaryIndex struct {
	IndexName             string                 `json:"IndexName"`
	KeySchema             []KeySchemaElement     `json:"KeySchema"`
	Projection            Projection             `json:"Projection"`
	ProvisionedThroughput *ProvisionedThroughput `json:"ProvisionedThroughput,omitempty"`
	OnDemandThroughput    *OnDemandThroughput    `json:"OnDemandThroughput,omitempty"`
	WarmThroughput        *WarmThroughput        `json:"WarmThroughput,omitempty"`
}

type LocalSecondaryIndex struct {
	IndexName  string             `json:"IndexName"`
	KeySchema  []KeySchemaElement `json:"KeySchema"`
	Projection Projection         `json:"Projection"`
}

type GlobalSecondaryIndexUpdate struct {
	Create *GlobalSecondaryIndex `json:"Create,omitempty"`
	Update *struct {
		IndexName             string                 `json:"IndexName"`
		ProvisionedThroughput *ProvisionedThroughput `json:"ProvisionedThroughput,omitempty"`
		OnDemandThroughput    *OnDemandThroughput    `json:"OnDemandThroughput,omitempty"`
		WarmThroughput        *WarmThroughput        `json:"WarmThroughput,omitempty"`
	} `json:"Update,omitempty"`
	Delete *struct {
		IndexName string `json:"IndexName"`
	} `json:"Delete,omitempty"`
}

type GlobalSecondaryIndexDescription struct {
	IndexName             string                            `json:"IndexName"`
	KeySchema             []KeySchemaElement                `json:"KeySchema"`
	Projection            Projection                        `json:"Projection"`
	IndexStatus           string                            `json:"IndexStatus"`
	IndexArn              string                            `json:"IndexArn"`
	ProvisionedThroughput *ProvisionedThroughputDescription `json:"ProvisionedThroughput,omitempty"`
	OnDemandThroughput    *OnDemandThroughput               `json:"OnDemandThroughput,omitempty"`
	WarmThroughput        *WarmThroughputDescription        `json:"WarmThroughput,omitempty"`
	ItemCount             int64                             `json:"ItemCount"`
	IndexSizeBytes        int64                             `json:"IndexSizeBytes"`
}

type LocalSecondaryIndexDescription struct {
	IndexName      string             `json:"IndexName"`
	KeySchema      []KeySchemaElement `json:"KeySchema"`
	Projection     Projection         `json:"Projection"`
	IndexArn       string             `json:"IndexArn"`
	ItemCount      int64              `json:"ItemCount"`
	IndexSizeBytes int64              `json:"IndexSizeBytes"`
}

type StreamSpecification struct {
	StreamEnabled  bool   `json:"StreamEnabled"`
	StreamViewType string `json:"StreamViewType,omitempty"`
}

type SSESpecification struct {
	Enabled        *bool  `json:"Enabled,omitempty"`
	SSEType        string `json:"SSEType,omitempty"`
	KMSMasterKeyId string `json:"KMSMasterKeyId,omitempty"`
}

type SSEDescription struct {
	Status          string `json:"Status"`
	SSEType         string `json:"SSEType,omitempty"`
	KMSMasterKeyArn string `json:"KMSMasterKeyArn,omitempty"`
}

type BillingModeSummary struct {
	BillingMode                       string   `json:"BillingMode"`
	LastUpdateToPayPerRequestDateTime *float64 `json:"LastUpdateToPayPerRequestDateTime,omitempty"`
}

type TableClassSummary struct {
	TableClass string `json:"TableClass"`
}

type TableDescription struct {
	AttributeDefinitions      []AttributeDefinition             `json:"AttributeDefinitions"`
	BillingModeSummary        *BillingModeSummary               `json:"BillingModeSummary,omitempty"`
	CreationDateTime          float64                           `json:"CreationDateTime"`
	DeletionProtectionEnabled bool                              `json:"DeletionProtectionEnabled"`
	GlobalSecondaryIndexes    []GlobalSecondaryIndexDescription `json:"GlobalSecondaryIndexes,omitempty"`
	GlobalTableVersion        string                            `json:"GlobalTableVersion,omitempty"`
	ItemCount                 int64                             `json:"ItemCount"`
	KeySchema                 []KeySchemaElement                `json:"KeySchema"`
	LatestStreamArn           string                            `json:"LatestStreamArn,omitempty"`
	LatestStreamLabel         string                            `json:"LatestStreamLabel,omitempty"`
	LocalSecondaryIndexes     []LocalSecondaryIndexDescription  `json:"LocalSecondaryIndexes,omitempty"`
	OnDemandThroughput        *OnDemandThroughput               `json:"OnDemandThroughput,omitempty"`
	ProvisionedThroughput     *ProvisionedThroughputDescription `json:"ProvisionedThroughput,omitempty"`
	Replicas                  []ReplicaDescription              `json:"Replicas,omitempty"`
	SSEDescription            *SSEDescription                   `json:"SSEDescription,omitempty"`
	StreamSpecification       *StreamSpecification              `json:"StreamSpecification,omitempty"`
	TableArn                  string                            `json:"TableArn"`
	TableClassSummary         *TableClassSummary                `json:"TableClassSummary,omitempty"`
	TableId                   string                            `json:"TableId"`
	TableName                 string                            `json:"TableName"`
	TableSizeBytes            int64                             `json:"TableSizeBytes"`
	TableStatus               string                            `json:"TableStatus"`
	WarmThroughput            *WarmThroughputDescription        `json:"WarmThroughput,omitempty"`
}

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
	TableClass                string                 `json:"TableClass,omitempty"`
	OnDemandThroughput        *OnDemandThroughput    `json:"OnDemandThroughput,omitempty"`
	WarmThroughput            *WarmThroughput        `json:"WarmThroughput,omitempty"`
	Tags                      []Tag                  `json:"Tags,omitempty"`
	DeletionProtectionEnabled bool                   `json:"DeletionProtectionEnabled,omitempty"`
}

type UpdateTableInput struct {
	TableName                   string                       `json:"TableName"`
	AttributeDefinitions        []AttributeDefinition        `json:"AttributeDefinitions,omitempty"`
	BillingMode                 string                       `json:"BillingMode,omitempty"`
	ProvisionedThroughput       *ProvisionedThroughput       `json:"ProvisionedThroughput,omitempty"`
	GlobalSecondaryIndexUpdates []GlobalSecondaryIndexUpdate `json:"GlobalSecondaryIndexUpdates,omitempty"`
	StreamSpecification         *StreamSpecification         `json:"StreamSpecification,omitempty"`
	SSESpecification            *SSESpecification            `json:"SSESpecification,omitempty"`
	TableClass                  string                       `json:"TableClass,omitempty"`
	OnDemandThroughput          *OnDemandThroughput          `json:"OnDemandThroughput,omitempty"`
	WarmThroughput              *WarmThroughput              `json:"WarmThroughput,omitempty"`
	DeletionProtectionEnabled   *bool                        `json:"DeletionProtectionEnabled,omitempty"`
	ReplicaUpdates              []ReplicationGroupUpdate     `json:"ReplicaUpdates,omitempty"`
}

type TimeToLiveDescription struct {
	TimeToLiveStatus string `json:"TimeToLiveStatus"`
	AttributeName    string `json:"AttributeName,omitempty"`
}

type TimeToLiveSpecification struct {
	AttributeName string `json:"AttributeName"`
	Enabled       bool   `json:"Enabled"`
}

type PointInTimeRecoveryDescription struct {
	PointInTimeRecoveryStatus  string   `json:"PointInTimeRecoveryStatus"`
	RecoveryPeriodInDays       *int32   `json:"RecoveryPeriodInDays,omitempty"`
	EarliestRestorableDateTime *float64 `json:"EarliestRestorableDateTime,omitempty"`
	LatestRestorableDateTime   *float64 `json:"LatestRestorableDateTime,omitempty"`
}

type ContinuousBackupsDescription struct {
	ContinuousBackupsStatus        string                         `json:"ContinuousBackupsStatus"`
	PointInTimeRecoveryDescription PointInTimeRecoveryDescription `json:"PointInTimeRecoveryDescription"`
}
