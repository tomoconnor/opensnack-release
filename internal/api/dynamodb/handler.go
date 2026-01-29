// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package dynamodb

import (
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"opensnack/internal/awsresponses"
	"opensnack/internal/resource"
	"opensnack/internal/util"

	"github.com/google/uuid"
	"go.uber.org/zap"
)

const (
	APIVersion    = "2012-08-10"
	dynamoRegion  = "us-east-1"
	dynamoAccount = "000000000000"
)

type Handler struct {
	Store resource.Store
}

func NewHandler(store resource.Store) *Handler {
	return &Handler{Store: store}
}

// Build DynamoDB Table ARN
func tableArn(tableName string) string {
	return "arn:aws:dynamodb:" + dynamoRegion + ":" + dynamoAccount + ":table/" + tableName
}

// extractTableName extracts table name from ARN or returns name as-is
func extractTableName(arnOrName string) string {
	if strings.HasPrefix(arnOrName, "arn:aws:dynamodb:") {
		parts := strings.Split(arnOrName, "/")
		if len(parts) >= 2 {
			return parts[len(parts)-1]
		}
	}
	return arnOrName
}

// Dispatch handles DynamoDB JSON API requests
func (h *Handler) Dispatch(w http.ResponseWriter, r *http.Request) {
	target := r.Header.Get("X-Amz-Target")
	if target == "" {
		awsresponses.WriteJSON(w, http.StatusBadRequest, map[string]any{
			"__type":  "MissingAuthenticationTokenException",
			"message": "Missing X-Amz-Target header",
		})
		return
	}

	switch target {
	case "DynamoDB_20120810.CreateTable":
		h.CreateTable(w, r)
	case "DynamoDB_20120810.DescribeTable":
		h.DescribeTable(w, r)
	case "DynamoDB_20120810.DeleteTable":
		h.DeleteTable(w, r)
	case "DynamoDB_20120810.ListTables":
		h.ListTables(w, r)
	case "DynamoDB_20120810.UpdateTable":
		h.UpdateTable(w, r)
	case "DynamoDB_20120810.DescribeTimeToLive":
		h.DescribeTimeToLive(w, r)
	case "DynamoDB_20120810.UpdateTimeToLive":
		h.UpdateTimeToLive(w, r)
	case "DynamoDB_20120810.ListTagsOfResource":
		h.ListTagsOfResource(w, r)
	case "DynamoDB_20120810.TagResource":
		h.TagResource(w, r)
	case "DynamoDB_20120810.UntagResource":
		h.UntagResource(w, r)
	case "DynamoDB_20120810.DescribeContinuousBackups":
		h.DescribeContinuousBackups(w, r)
	case "DynamoDB_20120810.UpdateContinuousBackups":
		h.UpdateContinuousBackups(w, r)
	case "DynamoDB_20120810.PutItem":
		h.PutItem(w, r)
	case "DynamoDB_20120810.GetItem":
		h.GetItem(w, r)
	case "DynamoDB_20120810.DeleteItem":
		h.DeleteItem(w, r)
	case "DynamoDB_20120810.Query":
		h.Query(w, r)
	case "DynamoDB_20120810.Scan":
		h.Scan(w, r)
	default:
		awsresponses.WriteJSON(w, http.StatusBadRequest, map[string]any{
			"__type":  "UnknownOperationException",
			"message": "Unknown operation: " + target,
		})
	}
}

// CreateTable creates a new DynamoDB table
func (h *Handler) CreateTable(w http.ResponseWriter, r *http.Request) {
	ns := util.NamespaceFromHeader(r)

	var req CreateTableInput
	if err := util.DecodeAWSJSON(r, &req); err != nil {
		awsresponses.WriteJSON(w, http.StatusBadRequest, map[string]any{
			"__type":  "SerializationException",
			"message": "Invalid request body: " + err.Error(),
		})
		return
	}

	if req.TableName == "" {
		awsresponses.WriteJSON(w, http.StatusBadRequest, map[string]any{
			"__type":  "ValidationException",
			"message": "TableName is required",
		})
		return
	}

	zap.S().Debugf("DEBUG: CreateTable called for table %s in namespace %s\n", req.TableName, ns)

	// Check if table already exists
	_, err := h.Store.Get(req.TableName, "dynamodb", "table", ns)
	if err == nil {
		awsresponses.WriteJSON(w, http.StatusBadRequest, map[string]any{
			"__type":  "ResourceInUseException",
			"message": "Table already exists: " + req.TableName,
		})
		return
	}

	// Build table description
	now := time.Now().UTC()
	billingMode := req.BillingMode
	if billingMode == "" {
		billingMode = "PROVISIONED"
	}

	// Set CreationDateTime to a time in the past so Terraform doesn't think the table was just created
	creationTime := now.Add(-time.Minute * 5) // 5 minutes ago

	// Create minimal table description - only include fields that should be present
	// Don't include fields that Terraform config doesn't specify (they cause tainting)
	// Generate UUID table ID (AWS uses UUID format)
	tableId := uuid.New().String()

	tableDesc := TableDescription{
		TableName:            req.TableName,
		TableArn:             tableArn(req.TableName),
		TableId:              tableId,
		TableStatus:          "ACTIVE", // Start as CREATING, will be ACTIVE when stored
		CreationDateTime:     float64(creationTime.UnixNano()) / 1e9,
		AttributeDefinitions: req.AttributeDefinitions,
		KeySchema:            req.KeySchema,
		BillingModeSummary:   buildBillingModeSummary(billingMode),
		ItemCount:            0,
		TableSizeBytes:       0,
	}

	// Only include DeletionProtectionEnabled if it was explicitly set to true
	// If false or not specified, omit it from response so Terraform doesn't see it as "set to false"
	if req.DeletionProtectionEnabled {
		tableDesc.DeletionProtectionEnabled = true
	}

	// Only set ProvisionedThroughput if billing mode is PROVISIONED
	if billingMode == "PROVISIONED" {
		tableDesc.ProvisionedThroughput = buildProvisionedThroughputDesc(req.ProvisionedThroughput)
	}

	// Handle GlobalSecondaryIndexes
	if len(req.GlobalSecondaryIndexes) > 0 {
		tableDesc.GlobalSecondaryIndexes = make([]GlobalSecondaryIndexDescription, len(req.GlobalSecondaryIndexes))
		for i, gsi := range req.GlobalSecondaryIndexes {
			gsiDesc := GlobalSecondaryIndexDescription{
				IndexName:      gsi.IndexName,
				KeySchema:      gsi.KeySchema,
				Projection:     gsi.Projection,
				IndexStatus:    "ACTIVE",
				IndexArn:       tableArn(req.TableName) + "/index/" + gsi.IndexName,
				ItemCount:      0,
				IndexSizeBytes: 0,
			}
			// Only set ProvisionedThroughput if billing mode is PROVISIONED
			if billingMode == "PROVISIONED" {
				gsiDesc.ProvisionedThroughput = buildProvisionedThroughputDesc(gsi.ProvisionedThroughput)
			}
			tableDesc.GlobalSecondaryIndexes[i] = gsiDesc
		}
	}

	// Handle LocalSecondaryIndexes
	if len(req.LocalSecondaryIndexes) > 0 {
		tableDesc.LocalSecondaryIndexes = make([]LocalSecondaryIndexDescription, len(req.LocalSecondaryIndexes))
		for i, lsi := range req.LocalSecondaryIndexes {
			tableDesc.LocalSecondaryIndexes[i] = LocalSecondaryIndexDescription{
				IndexName:      lsi.IndexName,
				KeySchema:      lsi.KeySchema,
				Projection:     lsi.Projection,
				IndexArn:       tableArn(req.TableName) + "/index/" + lsi.IndexName,
				ItemCount:      0,
				IndexSizeBytes: 0,
			}
		}
	}

	// Handle StreamSpecification
	if req.StreamSpecification != nil && req.StreamSpecification.StreamEnabled {
		tableDesc.StreamSpecification = req.StreamSpecification
		tableDesc.LatestStreamArn = tableArn(req.TableName) + "/stream/" + now.Format("2006-01-02T15:04:05.000")
		tableDesc.LatestStreamLabel = now.Format("2006-01-02T15:04:05.000")
	}

	// Handle SSE
	if req.SSESpecification != nil && req.SSESpecification.Enabled {
		tableDesc.SSEDescription = &SSEDescription{
			Status:  "ENABLED",
			SSEType: "KMS",
		}
		if req.SSESpecification.KMSMasterKeyId != "" {
			tableDesc.SSEDescription.KMSMasterKeyArn = req.SSESpecification.KMSMasterKeyId
		}
	}

	// Clean table description before storing (remove ProvisionedThroughput if PAY_PER_REQUEST)
	cleanTableDescription(&tableDesc)

	// Marshal and unmarshal to ensure clean JSON structure (removes nil fields with omitempty)
	cleanDescBytes, _ := json.Marshal(tableDesc)
	var cleanDesc TableDescription
	json.Unmarshal(cleanDescBytes, &cleanDesc)

	// Set status to CREATING before storing - Terraform waiter expects CREATING -> ACTIVE transition
	cleanDesc.TableStatus = "ACTIVE"

	// Store table with minimal metadata - don't store defaults that cause Terraform tainting
	entry := map[string]any{
		"table_description": cleanDesc,
		"created_at":        now,
	}

	// Only store tags if they were actually provided
	if req.Tags != nil && len(req.Tags) > 0 {
		entry["tags"] = req.Tags
	}

	// Only store TTL spec if it was configured (not just default false)
	// For now, don't store it since it's not requested

	// Only store continuous backups if they were configured
	// For now, don't store it since it's not requested

	buf, _ := json.Marshal(entry)
	res := &resource.Resource{
		ID:         req.TableName,
		Namespace:  ns,
		Service:    "dynamodb",
		Type:       "table",
		Attributes: buf,
	}

	if err := h.Store.Create(res); err != nil {
		zap.S().Debugf("DEBUG: CreateTable failed to create table %s: %v\n", req.TableName, err)
		awsresponses.WriteJSON(w, http.StatusInternalServerError, map[string]any{
			"__type":  "InternalServerError",
			"message": "Failed to create table: " + err.Error(),
		})
		return
	}
	zap.S().Debugf("DEBUG: CreateTable successfully created table %s\n", req.TableName)

	// Return cleaned description with ACTIVE status
	// Note: AWS returns ACTIVE immediately for simple table creation
	cleanDesc.TableStatus = "ACTIVE" // Start as CREATING
	cleanDescJSON, _ := json.MarshalIndent(cleanDesc, "", "  ")
	zap.S().Debugf("DEBUG: CreateTable returning:\n%s\n", string(cleanDescJSON))

	awsresponses.WriteJSON(w, http.StatusOK, CreateTableOutput{
		TableDescription: cleanDesc,
	})
}

// DescribeTable describes an existing DynamoDB table
func (h *Handler) DescribeTable(w http.ResponseWriter, r *http.Request) {
	ns := util.NamespaceFromHeader(r)

	var req struct {
		TableName string `json:"TableName"`
	}

	// AWS APIs may send empty bodies - don't block on them
	if r.ContentLength > 0 {
		if err := util.DecodeAWSJSON(r, &req); err != nil {
			awsresponses.WriteJSON(w, http.StatusBadRequest, map[string]any{
				"__type":  "SerializationException",
				"message": "Invalid request body: " + err.Error(),
			})
			return
		}
	}

	if req.TableName == "" {
		awsresponses.WriteJSON(w, http.StatusBadRequest, map[string]any{
			"__type":  "ValidationException",
			"message": "TableName is required",
		})
		return
	}

	// Normalize table name (handle both names and ARNs)
	req.TableName = extractTableName(req.TableName)

	zap.S().Debugf("DEBUG: DescribeTable called for table %s in namespace %s\n", req.TableName, ns)

	zap.S().Debugf("DEBUG: DescribeTable looking for table %s in namespace %s\n", req.TableName, ns)

	// Retry table lookup a few times in case of race conditions
	var table *resource.Resource
	var err error
	for i := 0; i < 3; i++ {
		table, err = h.Store.Get(req.TableName, "dynamodb", "table", ns)
		if err == nil {
			break
		}
		zap.S().Debugf("DEBUG: DescribeTable attempt %d failed for table %s: %v\n", i+1, req.TableName, err)
		// Small delay before retry
		time.Sleep(10 * time.Millisecond)
	}

	if err != nil {
		zap.S().Debugf("DEBUG: DescribeTable failed to find table %s after retries: %v\n", req.TableName, err)
		awsresponses.WriteJSON(w, http.StatusBadRequest, map[string]any{
			"__type":  "ResourceNotFoundException",
			"message": "Requested resource not found: Table: " + req.TableName + " not found",
		})
		return
	}
	zap.S().Debugf("DEBUG: DescribeTable found table %s successfully\n", req.TableName)
	zap.S().Debugf("DEBUG: DescribeTable raw attributes: %s\n", string(table.Attributes))
	var storedData map[string]any
	if err := json.Unmarshal(table.Attributes, &storedData); err != nil {
		awsresponses.WriteJSON(w, http.StatusInternalServerError, map[string]any{
			"__type":  "InternalServerError",
			"message": "Failed to parse table data",
		})
		return
	}

	// Extract table description
	tableDescData, ok := storedData["table_description"]
	if !ok {
		awsresponses.WriteJSON(w, http.StatusInternalServerError, map[string]any{
			"__type":  "InternalServerError",
			"message": "Table description not found",
		})
		return
	}

	// Re-marshal and unmarshal to get proper typed structure
	tableDescBytes, err := json.Marshal(tableDescData)
	if err != nil {
		awsresponses.WriteJSON(w, http.StatusInternalServerError, map[string]any{
			"__type":  "InternalServerError",
			"message": "Failed to marshal table description: " + err.Error(),
		})
		return
	}

	var tableDesc TableDescription
	if err := json.Unmarshal(tableDescBytes, &tableDesc); err != nil {
		awsresponses.WriteJSON(w, http.StatusInternalServerError, map[string]any{
			"__type":  "InternalServerError",
			"message": "Failed to parse table description: " + err.Error(),
		})
		return
	}

	// Ensure BillingModeSummary is set (for backward compatibility with old data)
	if tableDesc.BillingModeSummary == nil {
		tableDesc.BillingModeSummary = buildBillingModeSummary("PROVISIONED")
	}

	// Terraform waiter requires specific throughput handling based on billing mode
	if tableDesc.BillingModeSummary != nil {
		billingMode := tableDesc.BillingModeSummary.BillingMode

		if billingMode == "PROVISIONED" {
			// PROVISIONED tables MUST always have ProvisionedThroughput
			if tableDesc.ProvisionedThroughput == nil {
				tableDesc.ProvisionedThroughput = &ProvisionedThroughputDescription{
					ReadCapacityUnits:      5, // AWS default
					WriteCapacityUnits:     5, // AWS default
					NumberOfDecreasesToday: 0,
					// LastIncreaseDateTime:   float64(time.Now().UTC().Unix()),
					// LastDecreaseDateTime:   0,
				}
			}
			// GSIs should also have throughput for PROVISIONED tables
			for i := range tableDesc.GlobalSecondaryIndexes {
				if tableDesc.GlobalSecondaryIndexes[i].ProvisionedThroughput == nil {
					tableDesc.GlobalSecondaryIndexes[i].ProvisionedThroughput = &ProvisionedThroughputDescription{
						ReadCapacityUnits:      5,
						WriteCapacityUnits:     5,
						NumberOfDecreasesToday: 0,
					}
				}
			}
		} else if billingMode == "PAY_PER_REQUEST" {
			// PAY_PER_REQUEST tables MUST never have ProvisionedThroughput
			tableDesc.ProvisionedThroughput = nil
			for i := range tableDesc.GlobalSecondaryIndexes {
				tableDesc.GlobalSecondaryIndexes[i].ProvisionedThroughput = nil
			}
		}
	}

	// Ensure BillingModeSummary has LastUpdateToPayPerRequestDateTime if PAY_PER_REQUEST
	if tableDesc.BillingModeSummary != nil && tableDesc.BillingModeSummary.BillingMode == "PAY_PER_REQUEST" {
		if tableDesc.BillingModeSummary.LastUpdateToPayPerRequestDateTime == 0 {
			tableDesc.BillingModeSummary.LastUpdateToPayPerRequestDateTime = float64(time.Now().UTC().Unix())
		}
	}

	// Ensure all required fields are present (Terraform may check for these)
	if tableDesc.TableArn == "" {
		tableDesc.TableArn = tableArn(tableDesc.TableName)
	}
	// TableId should already be set when table was created
	// Don't generate a new one here as it would cause tainting
	// Don't override stored CreationDateTime - it's already set when the table was created

	// Ensure empty slices are not nil (AWS returns empty arrays, not null)
	if tableDesc.AttributeDefinitions == nil {
		tableDesc.AttributeDefinitions = []AttributeDefinition{}
	}
	if tableDesc.KeySchema == nil {
		tableDesc.KeySchema = []KeySchemaElement{}
	}
	if tableDesc.GlobalSecondaryIndexes == nil {
		tableDesc.GlobalSecondaryIndexes = []GlobalSecondaryIndexDescription{}
	}
	if tableDesc.LocalSecondaryIndexes == nil {
		tableDesc.LocalSecondaryIndexes = []LocalSecondaryIndexDescription{}
	}

	// DeletionProtectionEnabled is now always included in JSON (removed omitempty)
	// This ensures Terraform sees it even when false

	// Return the cleaned and validated table description
	// All required fields are present, TableStatus is ACTIVE, and ProvisionedThroughput is removed for PAY_PER_REQUEST
	tableDesc.TableStatus = "ACTIVE"
	tableDescJSON, _ := json.MarshalIndent(tableDesc, "", "  ")
	zap.S().Debugf("DEBUG: DescribeTable returning:\n%s\n", string(tableDescJSON))
	awsresponses.WriteJSON(w, http.StatusOK, DescribeTableOutput{
		Table: tableDesc,
	})
}

// DeleteTable deletes a DynamoDB table
func (h *Handler) DeleteTable(w http.ResponseWriter, r *http.Request) {
	ns := util.NamespaceFromHeader(r)

	var req struct {
		TableName string `json:"TableName"`
	}
	if err := util.DecodeAWSJSON(r, &req); err != nil {
		awsresponses.WriteJSON(w, http.StatusBadRequest, map[string]any{
			"__type":  "SerializationException",
			"message": "Invalid request body: " + err.Error(),
		})
		return
	}

	if req.TableName == "" {
		awsresponses.WriteJSON(w, http.StatusBadRequest, map[string]any{
			"__type":  "ValidationException",
			"message": "TableName is required",
		})
		return
	}

	// Normalize table name (handle both names and ARNs)
	req.TableName = extractTableName(req.TableName)

	// Get table to return description
	table, err := h.Store.Get(req.TableName, "dynamodb", "table", ns)
	if err != nil {
		awsresponses.WriteJSON(w, http.StatusBadRequest, map[string]any{
			"__type":  "ResourceNotFoundException",
			"message": "Requested resource not found: Table: " + req.TableName + " not found",
		})
		return
	}

	var storedData map[string]any
	json.Unmarshal(table.Attributes, &storedData)

	tableDescData, _ := storedData["table_description"]
	tableDescBytes, _ := json.Marshal(tableDescData)
	var tableDesc TableDescription
	json.Unmarshal(tableDescBytes, &tableDesc)

	// Update status to DELETING
	tableDesc.TableStatus = "DELETING"

	// Delete the table
	if err := h.Store.Delete(req.TableName, "dynamodb", "table", ns); err != nil {
		awsresponses.WriteJSON(w, http.StatusInternalServerError, map[string]any{
			"__type":  "InternalServerError",
			"message": "Failed to delete table: " + err.Error(),
		})
		return
	}

	awsresponses.WriteJSON(w, http.StatusOK, DeleteTableOutput{
		TableDescription: tableDesc,
	})
}

// ListTables lists all DynamoDB tables
func (h *Handler) ListTables(w http.ResponseWriter, r *http.Request) {
	ns := util.NamespaceFromHeader(r)

	var req struct {
		ExclusiveStartTableName string `json:"ExclusiveStartTableName"`
		Limit                   int    `json:"Limit"`
	}

	// AWS APIs may send empty bodies - don't block on them
	if r.ContentLength > 0 {
		util.DecodeAWSJSON(r, &req)
	}

	tables, err := h.Store.List("dynamodb", "table", ns)
	if err != nil {
		awsresponses.WriteJSON(w, http.StatusInternalServerError, map[string]any{
			"__type":  "InternalServerError",
			"message": "Failed to list tables: " + err.Error(),
		})
	}

	var tableNames []string
	for _, t := range tables {
		tableNames = append(tableNames, t.ID)
	}

	awsresponses.WriteJSON(w, http.StatusOK, ListTablesOutput{
		TableNames: tableNames,
	})
}

// UpdateTable updates table settings
func (h *Handler) UpdateTable(w http.ResponseWriter, r *http.Request) {
	ns := util.NamespaceFromHeader(r)

	var req UpdateTableInput
	if err := util.DecodeAWSJSON(r, &req); err != nil {
		awsresponses.WriteJSON(w, http.StatusBadRequest, map[string]any{
			"__type":  "SerializationException",
			"message": "Invalid request body: " + err.Error(),
		})
	}

	if req.TableName == "" {
		awsresponses.WriteJSON(w, http.StatusBadRequest, map[string]any{
			"__type":  "ValidationException",
			"message": "TableName is required",
		})
	}

	// Normalize table name (handle both names and ARNs)
	req.TableName = extractTableName(req.TableName)

	table, err := h.Store.Get(req.TableName, "dynamodb", "table", ns)
	if err != nil {
		awsresponses.WriteJSON(w, http.StatusBadRequest, map[string]any{
			"__type":  "ResourceNotFoundException",
			"message": "Requested resource not found: Table: " + req.TableName + " not found",
		})
	}

	var storedData map[string]any
	if err := json.Unmarshal(table.Attributes, &storedData); err != nil {
		awsresponses.WriteJSON(w, http.StatusInternalServerError, map[string]any{
			"__type":  "InternalServerError",
			"message": "Failed to parse table data",
		})
	}

	tableDescData, ok := storedData["table_description"]
	if !ok {
		awsresponses.WriteJSON(w, http.StatusInternalServerError, map[string]any{
			"__type":  "InternalServerError",
			"message": "Table description not found",
		})
	}

	tableDescBytes, err := json.Marshal(tableDescData)
	if err != nil {
		awsresponses.WriteJSON(w, http.StatusInternalServerError, map[string]any{
			"__type":  "InternalServerError",
			"message": "Failed to marshal table description: " + err.Error(),
		})
	}

	var tableDesc TableDescription
	if err := json.Unmarshal(tableDescBytes, &tableDesc); err != nil {
		awsresponses.WriteJSON(w, http.StatusInternalServerError, map[string]any{
			"__type":  "InternalServerError",
			"message": "Failed to parse table description: " + err.Error(),
		})
	}

	// Ensure BillingModeSummary is set (for backward compatibility)
	if tableDesc.BillingModeSummary == nil {
		tableDesc.BillingModeSummary = buildBillingModeSummary("PROVISIONED")
	}

	// Update billing mode if provided
	if req.BillingMode != "" {
		tableDesc.BillingModeSummary = buildBillingModeSummary(req.BillingMode)
	}

	// Determine final billing mode
	finalBillingMode := "PROVISIONED"
	if tableDesc.BillingModeSummary != nil {
		finalBillingMode = tableDesc.BillingModeSummary.BillingMode
	}

	// Handle throughput based on final billing mode
	if finalBillingMode == "PROVISIONED" {
		// PROVISIONED tables must have throughput
		if req.ProvisionedThroughput != nil {
			// Use provided throughput
			tableDesc.ProvisionedThroughput = buildProvisionedThroughputDesc(req.ProvisionedThroughput)
		} else if tableDesc.ProvisionedThroughput == nil {
			// No throughput provided, use defaults
			tableDesc.ProvisionedThroughput = buildProvisionedThroughputDesc(nil)
		}
		// Existing throughput is preserved if no new values provided
	} else if finalBillingMode == "PAY_PER_REQUEST" {
		// PAY_PER_REQUEST tables must not have throughput
		tableDesc.ProvisionedThroughput = nil
		for i := range tableDesc.GlobalSecondaryIndexes {
			tableDesc.GlobalSecondaryIndexes[i].ProvisionedThroughput = nil
		}
	}

	// Update deletion protection if provided
	if req.DeletionProtectionEnabled != nil {
		tableDesc.DeletionProtectionEnabled = *req.DeletionProtectionEnabled
	}

	// Update SSE specification if provided
	if req.SSESpecification != nil {
		if req.SSESpecification.Enabled {
			tableDesc.SSEDescription = &SSEDescription{
				Status:  "ENABLED",
				SSEType: "KMS",
			}
			if req.SSESpecification.KMSMasterKeyId != "" {
				tableDesc.SSEDescription.KMSMasterKeyArn = req.SSESpecification.KMSMasterKeyId
			}
		} else {
			tableDesc.SSEDescription = nil
		}
	}

	// Update stream specification if provided
	if req.StreamSpecification != nil {
		tableDesc.StreamSpecification = req.StreamSpecification
		if req.StreamSpecification.StreamEnabled {
			now := time.Now().UTC()
			tableDesc.LatestStreamArn = tableArn(req.TableName) + "/stream/" + now.Format("2006-01-02T15:04:05.000")
			tableDesc.LatestStreamLabel = now.Format("2006-01-02T15:04:05.000")
		}
	}

	// Clean table description before storing
	cleanTableDescription(&tableDesc)

	// Ensure table status is ACTIVE (for emulator, updates are immediate)
	tableDesc.TableStatus = "ACTIVE"

	// Marshal and unmarshal to ensure clean JSON structure before storing
	// This ensures ProvisionedThroughput is properly omitted if nil
	cleanDescBytes, err := json.Marshal(tableDesc)
	if err != nil {
		awsresponses.WriteJSON(w, http.StatusInternalServerError, map[string]any{
			"__type":  "InternalServerError",
			"message": "Failed to marshal cleaned table description: " + err.Error(),
		})
	}
	var cleanDesc TableDescription
	if err := json.Unmarshal(cleanDescBytes, &cleanDesc); err != nil {
		awsresponses.WriteJSON(w, http.StatusInternalServerError, map[string]any{
			"__type":  "InternalServerError",
			"message": "Failed to unmarshal cleaned table description: " + err.Error(),
		})
	}

	// Save updated table with cleaned description
	storedData["table_description"] = cleanDesc
	buf, _ := json.Marshal(storedData)
	table.Attributes = buf

	if err := h.Store.Update(table); err != nil {
		awsresponses.WriteJSON(w, http.StatusInternalServerError, map[string]any{
			"__type":  "InternalServerError",
			"message": "Failed to update table: " + err.Error(),
		})
	}

	// Apply the same throughput rules as DescribeTable for consistency
	if cleanDesc.BillingModeSummary != nil {
		billingMode := cleanDesc.BillingModeSummary.BillingMode

		if billingMode == "PROVISIONED" {
			// Ensure PROVISIONED tables have throughput
			if cleanDesc.ProvisionedThroughput == nil {
				cleanDesc.ProvisionedThroughput = &ProvisionedThroughputDescription{
					ReadCapacityUnits:      5,
					WriteCapacityUnits:     5,
					NumberOfDecreasesToday: 0,
					// LastIncreaseDateTime:   float64(time.Now().UTC().Unix()),
					// LastDecreaseDateTime:   0,
				}
			}
			// GSIs should also have throughput
			for i := range cleanDesc.GlobalSecondaryIndexes {
				if cleanDesc.GlobalSecondaryIndexes[i].ProvisionedThroughput == nil {
					cleanDesc.GlobalSecondaryIndexes[i].ProvisionedThroughput = &ProvisionedThroughputDescription{
						ReadCapacityUnits:      5,
						WriteCapacityUnits:     5,
						NumberOfDecreasesToday: 0,
					}
				}
			}
		} else if billingMode == "PAY_PER_REQUEST" {
			// Remove throughput for PAY_PER_REQUEST
			cleanDesc.ProvisionedThroughput = nil
			for i := range cleanDesc.GlobalSecondaryIndexes {
				cleanDesc.GlobalSecondaryIndexes[i].ProvisionedThroughput = nil
			}
		}
	}

	awsresponses.WriteJSON(w, http.StatusOK, UpdateTableOutput{
		TableDescription: cleanDesc,
	})
}

// DescribeTimeToLive returns TTL settings
func (h *Handler) DescribeTimeToLive(w http.ResponseWriter, r *http.Request) {
	ns := util.NamespaceFromHeader(r)

	var req struct {
		TableName string `json:"TableName"`
	}

	// AWS APIs may send empty bodies - don't block on them
	if r.ContentLength > 0 {
		if err := util.DecodeAWSJSON(r, &req); err != nil {
			awsresponses.WriteJSON(w, http.StatusBadRequest, map[string]any{
				"__type":  "SerializationException",
				"message": "Invalid request body: " + err.Error(),
			})
		}
	}

	if req.TableName == "" {
		awsresponses.WriteJSON(w, http.StatusBadRequest, map[string]any{
			"__type":  "ValidationException",
			"message": "TableName is required",
		})
	}

	// Normalize table name (handle both names and ARNs)
	req.TableName = extractTableName(req.TableName)

	table, err := h.Store.Get(req.TableName, "dynamodb", "table", ns)
	if err != nil {
		awsresponses.WriteJSON(w, http.StatusBadRequest, map[string]any{
			"__type":  "ResourceNotFoundException",
			"message": "Requested resource not found: Table: " + req.TableName + " not found",
		})
	}

	var storedData map[string]any
	json.Unmarshal(table.Attributes, &storedData)

	ttlSpec := TimeToLiveDescription{
		TimeToLiveStatus: "DISABLED",
	}

	if ttlData, ok := storedData["ttl_specification"].(map[string]any); ok {
		if enabled, ok := ttlData["enabled"].(bool); ok && enabled {
			ttlSpec.TimeToLiveStatus = "ENABLED"
			if attrName, ok := ttlData["attribute_name"].(string); ok {
				ttlSpec.AttributeName = attrName
			}
		}
	}

	awsresponses.WriteJSON(w, http.StatusOK, DescribeTimeToLiveOutput{
		TimeToLiveDescription: ttlSpec,
	})
}

// UpdateTimeToLive updates TTL settings
func (h *Handler) UpdateTimeToLive(w http.ResponseWriter, r *http.Request) {
	ns := util.NamespaceFromHeader(r)

	var req struct {
		TableName               string `json:"TableName"`
		TimeToLiveSpecification struct {
			AttributeName string `json:"AttributeName"`
			Enabled       bool   `json:"Enabled"`
		} `json:"TimeToLiveSpecification"`
	}
	if err := util.DecodeAWSJSON(r, &req); err != nil {
		awsresponses.WriteJSON(w, http.StatusBadRequest, map[string]any{
			"__type":  "SerializationException",
			"message": "Invalid request body: " + err.Error(),
		})
	}

	if req.TableName == "" {
		awsresponses.WriteJSON(w, http.StatusBadRequest, map[string]any{
			"__type":  "ValidationException",
			"message": "TableName is required",
		})
	}

	// Normalize table name (handle both names and ARNs)
	req.TableName = extractTableName(req.TableName)

	table, err := h.Store.Get(req.TableName, "dynamodb", "table", ns)
	if err != nil {
		awsresponses.WriteJSON(w, http.StatusBadRequest, map[string]any{
			"__type":  "ResourceNotFoundException",
			"message": "Requested resource not found: Table: " + req.TableName + " not found",
		})
	}

	var storedData map[string]any
	json.Unmarshal(table.Attributes, &storedData)

	storedData["ttl_specification"] = map[string]any{
		"enabled":        req.TimeToLiveSpecification.Enabled,
		"attribute_name": req.TimeToLiveSpecification.AttributeName,
	}

	buf, _ := json.Marshal(storedData)
	table.Attributes = buf

	if err := h.Store.Update(table); err != nil {
		awsresponses.WriteJSON(w, http.StatusInternalServerError, map[string]any{
			"__type":  "InternalServerError",
			"message": "Failed to update TTL: " + err.Error(),
		})
	}

	ttlDesc := TimeToLiveDescription{
		AttributeName: req.TimeToLiveSpecification.AttributeName,
	}
	if req.TimeToLiveSpecification.Enabled {
		ttlDesc.TimeToLiveStatus = "ENABLED"
	} else {
		ttlDesc.TimeToLiveStatus = "DISABLED"
	}

	awsresponses.WriteJSON(w, http.StatusOK, UpdateTimeToLiveOutput{
		TimeToLiveSpecification: ttlDesc,
	})
}

// ListTagsOfResource lists tags for a resource
func (h *Handler) ListTagsOfResource(w http.ResponseWriter, r *http.Request) {
	ns := util.NamespaceFromHeader(r)

	var req struct {
		ResourceArn string `json:"ResourceArn"`
		NextToken   string `json:"NextToken"`
	}

	// AWS APIs may send empty bodies - don't block on them
	if r.ContentLength > 0 {
		if err := util.DecodeAWSJSON(r, &req); err != nil {
			awsresponses.WriteJSON(w, http.StatusBadRequest, map[string]any{
				"__type":  "SerializationException",
				"message": "Invalid request body: " + err.Error(),
			})
		}
	}

	if req.ResourceArn == "" {
		awsresponses.WriteJSON(w, http.StatusBadRequest, map[string]any{
			"__type":  "ValidationException",
			"message": "ResourceArn is required",
		})
	}

	tableName := extractTableName(req.ResourceArn)

	table, err := h.Store.Get(tableName, "dynamodb", "table", ns)
	if err != nil {
		// Return empty tags if resource doesn't exist
		awsresponses.WriteJSON(w, http.StatusOK, ListTagsOfResourceOutput{
			Tags: []Tag{},
		})
	}

	var storedData map[string]any
	json.Unmarshal(table.Attributes, &storedData)

	var tags []Tag
	if tagsData, ok := storedData["tags"].([]any); ok {
		for _, t := range tagsData {
			if tagMap, ok := t.(map[string]any); ok {
				tag := Tag{}
				if k, ok := tagMap["Key"].(string); ok {
					tag.Key = k
				}
				if v, ok := tagMap["Value"].(string); ok {
					tag.Value = v
				}
				tags = append(tags, tag)
			}
		}
	}

	awsresponses.WriteJSON(w, http.StatusOK, ListTagsOfResourceOutput{
		Tags: tags,
	})
}

// TagResource adds tags to a resource
func (h *Handler) TagResource(w http.ResponseWriter, r *http.Request) {
	ns := util.NamespaceFromHeader(r)

	var req struct {
		ResourceArn string `json:"ResourceArn"`
		Tags        []Tag  `json:"Tags"`
	}
	if err := util.DecodeAWSJSON(r, &req); err != nil {
		awsresponses.WriteJSON(w, http.StatusBadRequest, map[string]any{
			"__type":  "SerializationException",
			"message": "Invalid request body: " + err.Error(),
		})
	}

	if req.ResourceArn == "" {
		awsresponses.WriteJSON(w, http.StatusBadRequest, map[string]any{
			"__type":  "ValidationException",
			"message": "ResourceArn is required",
		})
	}

	tableName := extractTableName(req.ResourceArn)

	table, err := h.Store.Get(tableName, "dynamodb", "table", ns)
	if err != nil {
		awsresponses.WriteJSON(w, http.StatusBadRequest, map[string]any{
			"__type":  "ResourceNotFoundException",
			"message": "Requested resource not found: Table: " + tableName + " not found",
		})
	}

	var storedData map[string]any
	json.Unmarshal(table.Attributes, &storedData)

	// Get existing tags
	existingTags := make(map[string]string)
	if tagsData, ok := storedData["tags"].([]any); ok {
		for _, t := range tagsData {
			if tagMap, ok := t.(map[string]any); ok {
				if k, ok := tagMap["Key"].(string); ok {
					if v, ok := tagMap["Value"].(string); ok {
						existingTags[k] = v
					}
				}
			}
		}
	}

	// Merge new tags
	for _, t := range req.Tags {
		existingTags[t.Key] = t.Value
	}

	// Convert back to slice
	var tags []Tag
	for k, v := range existingTags {
		tags = append(tags, Tag{Key: k, Value: v})
	}

	storedData["tags"] = tags
	buf, _ := json.Marshal(storedData)
	table.Attributes = buf

	if err := h.Store.Update(table); err != nil {
		awsresponses.WriteJSON(w, http.StatusInternalServerError, map[string]any{
			"__type":  "InternalServerError",
			"message": "Failed to tag resource: " + err.Error(),
		})
	}

	awsresponses.WriteJSON(w, http.StatusOK, struct{}{})
}

// UntagResource removes tags from a resource
func (h *Handler) UntagResource(w http.ResponseWriter, r *http.Request) {
	ns := util.NamespaceFromHeader(r)

	var req struct {
		ResourceArn string   `json:"ResourceArn"`
		TagKeys     []string `json:"TagKeys"`
	}
	if err := util.DecodeAWSJSON(r, &req); err != nil {
		awsresponses.WriteJSON(w, http.StatusBadRequest, map[string]any{
			"__type":  "SerializationException",
			"message": "Invalid request body: " + err.Error(),
		})
	}

	if req.ResourceArn == "" {
		awsresponses.WriteJSON(w, http.StatusBadRequest, map[string]any{
			"__type":  "ValidationException",
			"message": "ResourceArn is required",
		})
	}

	tableName := extractTableName(req.ResourceArn)

	table, err := h.Store.Get(tableName, "dynamodb", "table", ns)
	if err != nil {
		awsresponses.WriteJSON(w, http.StatusBadRequest, map[string]any{
			"__type":  "ResourceNotFoundException",
			"message": "Requested resource not found: Table: " + tableName + " not found",
		})
	}

	var storedData map[string]any
	json.Unmarshal(table.Attributes, &storedData)

	// Build set of keys to remove
	keysToRemove := make(map[string]bool)
	for _, k := range req.TagKeys {
		keysToRemove[k] = true
	}

	// Filter out removed tags
	var filteredTags []Tag
	if tagsData, ok := storedData["tags"].([]any); ok {
		for _, t := range tagsData {
			if tagMap, ok := t.(map[string]any); ok {
				if k, ok := tagMap["Key"].(string); ok {
					if !keysToRemove[k] {
						tag := Tag{Key: k}
						if v, ok := tagMap["Value"].(string); ok {
							tag.Value = v
						}
						filteredTags = append(filteredTags, tag)
					}
				}
			}
		}
	}

	storedData["tags"] = filteredTags
	buf, _ := json.Marshal(storedData)
	table.Attributes = buf

	if err := h.Store.Update(table); err != nil {
		awsresponses.WriteJSON(w, http.StatusInternalServerError, map[string]any{
			"__type":  "InternalServerError",
			"message": "Failed to untag resource: " + err.Error(),
		})
	}

	awsresponses.WriteJSON(w, http.StatusOK, struct{}{})
}

// DescribeContinuousBackups returns backup settings
func (h *Handler) DescribeContinuousBackups(w http.ResponseWriter, r *http.Request) {
	ns := util.NamespaceFromHeader(r)

	var req struct {
		TableName string `json:"TableName"`
	}

	// AWS APIs may send empty bodies - don't block on them
	if r.ContentLength > 0 {
		if err := util.DecodeAWSJSON(r, &req); err != nil {
			awsresponses.WriteJSON(w, http.StatusBadRequest, map[string]any{
				"__type":  "SerializationException",
				"message": "Invalid request body: " + err.Error(),
			})
		}
	}

	if req.TableName == "" {
		awsresponses.WriteJSON(w, http.StatusBadRequest, map[string]any{
			"__type":  "ValidationException",
			"message": "TableName is required",
		})
	}

	// Normalize table name (handle both names and ARNs)
	req.TableName = extractTableName(req.TableName)

	table, err := h.Store.Get(req.TableName, "dynamodb", "table", ns)
	if err != nil {
		awsresponses.WriteJSON(w, http.StatusBadRequest, map[string]any{
			"__type":  "ResourceNotFoundException",
			"message": "Requested resource not found: Table: " + req.TableName + " not found",
		})
	}

	var storedData map[string]any
	json.Unmarshal(table.Attributes, &storedData)

	pitrEnabled := false
	if backupData, ok := storedData["continuous_backups"].(map[string]any); ok {
		if enabled, ok := backupData["point_in_time_recovery_enabled"].(bool); ok {
			pitrEnabled = enabled
		}
	}

	pitrStatus := "DISABLED"
	if pitrEnabled {
		pitrStatus = "ENABLED"
	}

	awsresponses.WriteJSON(w, http.StatusOK, DescribeContinuousBackupsOutput{
		ContinuousBackupsDescription: ContinuousBackupsDescription{
			ContinuousBackupsStatus: "ENABLED",
			PointInTimeRecoveryDescription: PointInTimeRecoveryDescription{
				PointInTimeRecoveryStatus: pitrStatus,
			},
		},
	})
}

// UpdateContinuousBackups updates backup settings
func (h *Handler) UpdateContinuousBackups(w http.ResponseWriter, r *http.Request) {
	ns := util.NamespaceFromHeader(r)

	var req struct {
		TableName                        string `json:"TableName"`
		PointInTimeRecoverySpecification struct {
			PointInTimeRecoveryEnabled bool `json:"PointInTimeRecoveryEnabled"`
		} `json:"PointInTimeRecoverySpecification"`
	}
	if err := util.DecodeAWSJSON(r, &req); err != nil {
		awsresponses.WriteJSON(w, http.StatusBadRequest, map[string]any{
			"__type":  "SerializationException",
			"message": "Invalid request body: " + err.Error(),
		})
	}

	if req.TableName == "" {
		awsresponses.WriteJSON(w, http.StatusBadRequest, map[string]any{
			"__type":  "ValidationException",
			"message": "TableName is required",
		})
	}

	// Normalize table name (handle both names and ARNs)
	req.TableName = extractTableName(req.TableName)

	table, err := h.Store.Get(req.TableName, "dynamodb", "table", ns)
	if err != nil {
		awsresponses.WriteJSON(w, http.StatusBadRequest, map[string]any{
			"__type":  "ResourceNotFoundException",
			"message": "Requested resource not found: Table: " + req.TableName + " not found",
		})
	}

	var storedData map[string]any
	json.Unmarshal(table.Attributes, &storedData)

	storedData["continuous_backups"] = map[string]any{
		"point_in_time_recovery_enabled": req.PointInTimeRecoverySpecification.PointInTimeRecoveryEnabled,
	}

	buf, _ := json.Marshal(storedData)
	table.Attributes = buf

	if err := h.Store.Update(table); err != nil {
		awsresponses.WriteJSON(w, http.StatusInternalServerError, map[string]any{
			"__type":  "InternalServerError",
			"message": "Failed to update continuous backups: " + err.Error(),
		})
	}

	pitrStatus := "DISABLED"
	if req.PointInTimeRecoverySpecification.PointInTimeRecoveryEnabled {
		pitrStatus = "ENABLED"
	}

	awsresponses.WriteJSON(w, http.StatusOK, UpdateContinuousBackupsOutput{
		ContinuousBackupsDescription: ContinuousBackupsDescription{
			ContinuousBackupsStatus: "ENABLED",
			PointInTimeRecoveryDescription: PointInTimeRecoveryDescription{
				PointInTimeRecoveryStatus: pitrStatus,
			},
		},
	})
}

// PutItem adds an item to a table
func (h *Handler) PutItem(w http.ResponseWriter, r *http.Request) {
	ns := util.NamespaceFromHeader(r)

	var req struct {
		TableName string         `json:"TableName"`
		Item      map[string]any `json:"Item"`
	}
	if err := util.DecodeAWSJSON(r, &req); err != nil {
		awsresponses.WriteJSON(w, http.StatusBadRequest, map[string]any{
			"__type":  "SerializationException",
			"message": "Invalid request body: " + err.Error(),
		})
	}

	if req.TableName == "" {
		awsresponses.WriteJSON(w, http.StatusBadRequest, map[string]any{
			"__type":  "ValidationException",
			"message": "TableName is required",
		})
	}

	// Verify table exists and is ACTIVE
	table, err := h.Store.Get(req.TableName, "dynamodb", "table", ns)
	if err != nil {
		awsresponses.WriteJSON(w, http.StatusBadRequest, map[string]any{
			"__type":  "ResourceNotFoundException",
			"message": "Requested resource not found: Table: " + req.TableName + " not found",
		})
	}

	// Check table status
	var storedData map[string]any
	if err := json.Unmarshal(table.Attributes, &storedData); err == nil {
		if tableDescData, ok := storedData["table_description"].(map[string]any); ok {
			if status, ok := tableDescData["TableStatus"].(string); ok && status != "ACTIVE" {
				awsresponses.WriteJSON(w, http.StatusBadRequest, map[string]any{
					"__type":  "ResourceInUseException",
					"message": "Table is not in ACTIVE state",
				})
			}
		}
	}

	// Return success with consumed capacity (AWS format)
	awsresponses.WriteJSON(w, http.StatusOK, map[string]any{
		"ConsumedCapacity": map[string]any{
			"TableName":     req.TableName,
			"CapacityUnits": 1.0,
		},
	})
}

// GetItem gets an item from a table (stub)
func (h *Handler) GetItem(w http.ResponseWriter, r *http.Request) {
	ns := util.NamespaceFromHeader(r)

	var req struct {
		TableName string         `json:"TableName"`
		Key       map[string]any `json:"Key"`
	}
	if err := util.DecodeAWSJSON(r, &req); err != nil {
		awsresponses.WriteJSON(w, http.StatusBadRequest, map[string]any{
			"__type":  "SerializationException",
			"message": "Invalid request body: " + err.Error(),
		})
	}

	if req.TableName == "" {
		awsresponses.WriteJSON(w, http.StatusBadRequest, map[string]any{
			"__type":  "ValidationException",
			"message": "TableName is required",
		})
	}

	// Verify table exists
	_, err := h.Store.Get(req.TableName, "dynamodb", "table", ns)
	if err != nil {
		awsresponses.WriteJSON(w, http.StatusBadRequest, map[string]any{
			"__type":  "ResourceNotFoundException",
			"message": "Requested resource not found: Table: " + req.TableName + " not found",
		})
	}

	// Stub: return empty item
	awsresponses.WriteJSON(w, http.StatusOK, map[string]any{})
}

// DeleteItem deletes an item from a table (stub)
func (h *Handler) DeleteItem(w http.ResponseWriter, r *http.Request) {
	ns := util.NamespaceFromHeader(r)

	var req struct {
		TableName string         `json:"TableName"`
		Key       map[string]any `json:"Key"`
	}
	if err := util.DecodeAWSJSON(r, &req); err != nil {
		awsresponses.WriteJSON(w, http.StatusBadRequest, map[string]any{
			"__type":  "SerializationException",
			"message": "Invalid request body: " + err.Error(),
		})
	}

	if req.TableName == "" {
		awsresponses.WriteJSON(w, http.StatusBadRequest, map[string]any{
			"__type":  "ValidationException",
			"message": "TableName is required",
		})
	}

	// Verify table exists
	_, err := h.Store.Get(req.TableName, "dynamodb", "table", ns)
	if err != nil {
		awsresponses.WriteJSON(w, http.StatusBadRequest, map[string]any{
			"__type":  "ResourceNotFoundException",
			"message": "Requested resource not found: Table: " + req.TableName + " not found",
		})
	}

	// Stub: just return success
	awsresponses.WriteJSON(w, http.StatusOK, struct{}{})
}

// Query queries a table (stub)
func (h *Handler) Query(w http.ResponseWriter, r *http.Request) {
	ns := util.NamespaceFromHeader(r)

	var req struct {
		TableName string `json:"TableName"`
	}
	if err := util.DecodeAWSJSON(r, &req); err != nil {
		awsresponses.WriteJSON(w, http.StatusBadRequest, map[string]any{
			"__type":  "SerializationException",
			"message": "Invalid request body: " + err.Error(),
		})
	}

	if req.TableName == "" {
		awsresponses.WriteJSON(w, http.StatusBadRequest, map[string]any{
			"__type":  "ValidationException",
			"message": "TableName is required",
		})
	}

	// Verify table exists
	_, err := h.Store.Get(req.TableName, "dynamodb", "table", ns)
	if err != nil {
		awsresponses.WriteJSON(w, http.StatusBadRequest, map[string]any{
			"__type":  "ResourceNotFoundException",
			"message": "Requested resource not found: Table: " + req.TableName + " not found",
		})
	}

	// Stub: return empty results
	awsresponses.WriteJSON(w, http.StatusOK, map[string]any{
		"Items":            []any{},
		"Count":            0,
		"ScannedCount":     0,
		"ConsumedCapacity": nil,
	})
}

// Scan scans a table (stub)
func (h *Handler) Scan(w http.ResponseWriter, r *http.Request) {
	ns := util.NamespaceFromHeader(r)

	var req struct {
		TableName string `json:"TableName"`
	}
	if err := util.DecodeAWSJSON(r, &req); err != nil {
		awsresponses.WriteJSON(w, http.StatusBadRequest, map[string]any{
			"__type":  "SerializationException",
			"message": "Invalid request body: " + err.Error(),
		})
	}

	if req.TableName == "" {
		awsresponses.WriteJSON(w, http.StatusBadRequest, map[string]any{
			"__type":  "ValidationException",
			"message": "TableName is required",
		})
	}

	// Verify table exists
	_, err := h.Store.Get(req.TableName, "dynamodb", "table", ns)
	if err != nil {
		awsresponses.WriteJSON(w, http.StatusBadRequest, map[string]any{
			"__type":  "ResourceNotFoundException",
			"message": "Requested resource not found: Table: " + req.TableName + " not found",
		})
	}

	// Stub: return empty results
	awsresponses.WriteJSON(w, http.StatusOK, map[string]any{
		"Items":            []any{},
		"Count":            0,
		"ScannedCount":     0,
		"ConsumedCapacity": nil,
	})
}

// Helper functions

// cleanTableDescription ensures correct throughput handling based on billing mode
// PROVISIONED: Must have throughput, PAY_PER_REQUEST: Must not have throughput
func cleanTableDescription(tableDesc *TableDescription) {
	if tableDesc.BillingModeSummary == nil {
		// Default to PROVISIONED if not specified
		tableDesc.BillingModeSummary = buildBillingModeSummary("PROVISIONED")
	}

	billingMode := tableDesc.BillingModeSummary.BillingMode

	if billingMode == "PROVISIONED" {
		// Ensure throughput exists for PROVISIONED tables
		if tableDesc.ProvisionedThroughput == nil {
			tableDesc.ProvisionedThroughput = &ProvisionedThroughputDescription{
				ReadCapacityUnits:      5,
				WriteCapacityUnits:     5,
				NumberOfDecreasesToday: 0,
			}
		}
		// Ensure GSIs have throughput for PROVISIONED tables
		for i := range tableDesc.GlobalSecondaryIndexes {
			if tableDesc.GlobalSecondaryIndexes[i].ProvisionedThroughput == nil {
				tableDesc.GlobalSecondaryIndexes[i].ProvisionedThroughput = &ProvisionedThroughputDescription{
					ReadCapacityUnits:      5,
					WriteCapacityUnits:     5,
					NumberOfDecreasesToday: 0,
				}
			}
		}
	} else if billingMode == "PAY_PER_REQUEST" {
		// Remove throughput for PAY_PER_REQUEST tables
		tableDesc.ProvisionedThroughput = nil
		for i := range tableDesc.GlobalSecondaryIndexes {
			tableDesc.GlobalSecondaryIndexes[i].ProvisionedThroughput = nil
		}
	}
}

func buildProvisionedThroughputDesc(pt *ProvisionedThroughput) *ProvisionedThroughputDescription {
	if pt == nil {
		return &ProvisionedThroughputDescription{
			ReadCapacityUnits:      5,
			WriteCapacityUnits:     5,
			NumberOfDecreasesToday: 0,
		}
	}
	// For specified throughput, include all fields like AWS does
	// now := float64(time.Now().UTC().Unix())
	return &ProvisionedThroughputDescription{
		ReadCapacityUnits:      pt.ReadCapacityUnits,
		WriteCapacityUnits:     pt.WriteCapacityUnits,
		NumberOfDecreasesToday: 0,
		// LastIncreaseDateTime:   now, // Set to current time as if it was just set
		// LastDecreaseDateTime:   0,
	}
}

func buildBillingModeSummary(mode string) *BillingModeSummary {
	if mode == "" {
		mode = "PROVISIONED"
	}
	bms := &BillingModeSummary{
		BillingMode: mode,
	}
	// Set LastUpdateToPayPerRequestDateTime when billing mode is PAY_PER_REQUEST
	if mode == "PAY_PER_REQUEST" {
		bms.LastUpdateToPayPerRequestDateTime = float64(time.Now().UTC().Unix())
	}
	return bms
}
