// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package dynamodb

import (
	"encoding/json"
	"fmt"
	"net/http"
	"regexp"
	"sort"
	"strings"
	"time"

	"opensnack/internal/resource"
	"opensnack/internal/util"

	"github.com/google/uuid"
)

// tableRecord is what the store keeps for a table. DescribeTable and the
// other read APIs are rendered from it.
type tableRecord struct {
	Name                 string                  `json:"name"`
	ID                   string                  `json:"id"`
	Arn                  string                  `json:"arn"`
	CreatedAt            float64                 `json:"created_at"`
	AttributeDefinitions []AttributeDefinition   `json:"attribute_definitions"`
	KeySchema            []KeySchemaElement      `json:"key_schema"`
	BillingMode          string                  `json:"billing_mode"`
	LastPayPerRequest    *float64                `json:"last_pay_per_request,omitempty"`
	Throughput           ProvisionedThroughput   `json:"throughput"`
	LastIncrease         *float64                `json:"last_increase,omitempty"`
	LastDecrease         *float64                `json:"last_decrease,omitempty"`
	OnDemand             *OnDemandThroughput     `json:"on_demand,omitempty"`
	Warm                 *WarmThroughput         `json:"warm,omitempty"`
	GSIs                 []indexRecord           `json:"gsis,omitempty"`
	LSIs                 []indexRecord           `json:"lsis,omitempty"`
	Stream               *StreamSpecification    `json:"stream,omitempty"`
	StreamLabel          string                  `json:"stream_label,omitempty"`
	SSE                  *SSEDescription         `json:"sse,omitempty"`
	TableClass           string                  `json:"table_class"`
	DeletionProtection   bool                    `json:"deletion_protection"`
	TTL                  TimeToLiveSpecification `json:"ttl"`
	PITR                 bool                    `json:"pitr"`
	PITRDays             int32                   `json:"pitr_days,omitempty"`
	Tags                 []Tag                   `json:"tags,omitempty"`
	// Replicas are the regions of a global table's other copies.
	Replicas []string `json:"replicas,omitempty"`
}

type indexRecord struct {
	Name       string                `json:"name"`
	KeySchema  []KeySchemaElement    `json:"key_schema"`
	Projection Projection            `json:"projection"`
	Throughput ProvisionedThroughput `json:"throughput"`
	OnDemand   *OnDemandThroughput   `json:"on_demand,omitempty"`
	Warm       *WarmThroughput       `json:"warm,omitempty"`
}

func keyNames(ks []KeySchemaElement) (hash, rng string) {
	for _, k := range ks {
		switch k.KeyType {
		case "HASH":
			hash = k.AttributeName
		case "RANGE":
			rng = k.AttributeName
		}
	}
	return
}

func (t *tableRecord) keys() (string, string) { return keyNames(t.KeySchema) }

func (t *tableRecord) attrType(name string) string {
	for _, a := range t.AttributeDefinitions {
		if a.AttributeName == name {
			return a.AttributeType
		}
	}
	return ""
}

// indexView describes how to read a table or one of its indexes.
type indexView struct {
	name       string // "" for the base table
	global     bool
	hash, rng  string
	projection Projection
}

func (t *tableRecord) view(indexName string) (*indexView, error) {
	h, r := t.keys()
	if indexName == "" {
		return &indexView{hash: h, rng: r, projection: Projection{ProjectionType: "ALL"}}, nil
	}
	for _, idx := range t.GSIs {
		if idx.Name == indexName {
			ih, ir := keyNames(idx.KeySchema)
			return &indexView{name: idx.Name, global: true, hash: ih, rng: ir, projection: idx.Projection}, nil
		}
	}
	for _, idx := range t.LSIs {
		if idx.Name == indexName {
			ih, ir := keyNames(idx.KeySchema)
			return &indexView{name: idx.Name, hash: ih, rng: ir, projection: idx.Projection}, nil
		}
	}
	return nil, validationErr("The table does not have the specified index: " + indexName)
}

func nowSeconds() float64 {
	return float64(time.Now().UnixMilli()) / 1000
}

// ── Store access ─────────────────────────────────────────────────────────

func (h *Handler) loadTable(ns, name string) (*tableRecord, *resource.Resource, error) {
	if name == "" {
		return nil, nil, validationErr("1 validation error detected: Value null at 'tableName' failed to satisfy constraint: Member must not be null")
	}
	res, err := h.Store.Get(name, service, typeTable, ns)
	if err != nil || res == nil {
		return nil, nil, tableNotFound(name)
	}
	var t tableRecord
	if err := json.Unmarshal(res.Attributes, &t); err != nil || t.Name == "" {
		return nil, nil, tableNotFound(name)
	}
	return &t, res, nil
}

func (h *Handler) saveTable(res *resource.Resource, t *tableRecord) error {
	b, err := json.Marshal(t)
	if err != nil {
		return err
	}
	res.Attributes = b
	return h.Store.Update(res)
}

// ── Rendering ────────────────────────────────────────────────────────────

func throughputDesc(t *tableRecord, pt ProvisionedThroughput, withDates bool) *ProvisionedThroughputDescription {
	d := &ProvisionedThroughputDescription{ReadCapacityUnits: pt.ReadCapacityUnits, WriteCapacityUnits: pt.WriteCapacityUnits}
	if t.BillingMode == "PAY_PER_REQUEST" {
		d.ReadCapacityUnits, d.WriteCapacityUnits = 0, 0
	} else if withDates {
		d.LastIncreaseDateTime, d.LastDecreaseDateTime = t.LastIncrease, t.LastDecrease
	}
	return d
}

// warmDesc reports warm throughput. AWS fills in its minimums for tables and
// indexes that don't configure it, and the provider waits on the status.
func warmDesc(w *WarmThroughput) *WarmThroughputDescription {
	d := &WarmThroughputDescription{ReadUnitsPerSecond: 12000, WriteUnitsPerSecond: 4000, Status: "ACTIVE"}
	if w != nil {
		if w.ReadUnitsPerSecond != nil {
			d.ReadUnitsPerSecond = *w.ReadUnitsPerSecond
		}
		if w.WriteUnitsPerSecond != nil {
			d.WriteUnitsPerSecond = *w.WriteUnitsPerSecond
		}
	}
	return d
}

func (h *Handler) describe(ns string, t *tableRecord, status string) TableDescription {
	items := h.tableItems(ns, t.Name)
	var size int64
	for _, it := range items {
		size += int64(itemSize(it))
	}

	d := TableDescription{
		AttributeDefinitions:      t.AttributeDefinitions,
		BillingModeSummary:        &BillingModeSummary{BillingMode: t.BillingMode, LastUpdateToPayPerRequestDateTime: t.LastPayPerRequest},
		CreationDateTime:          t.CreatedAt,
		DeletionProtectionEnabled: t.DeletionProtection,
		ItemCount:                 int64(len(items)),
		KeySchema:                 t.KeySchema,
		OnDemandThroughput:        t.OnDemand,
		ProvisionedThroughput:     throughputDesc(t, t.Throughput, true),
		SSEDescription:            t.SSE,
		TableArn:                  t.Arn,
		TableClassSummary:         &TableClassSummary{TableClass: t.TableClass},
		TableId:                   t.ID,
		TableName:                 t.Name,
		TableSizeBytes:            size,
		TableStatus:               status,
		WarmThroughput:            warmDesc(t.Warm),
	}
	if len(t.Replicas) > 0 {
		d.GlobalTableVersion = globalTableVersion
		d.Replicas = h.replicaDescriptions(ns, t)
	}
	if t.Stream != nil && t.Stream.StreamEnabled {
		d.StreamSpecification = t.Stream
		d.LatestStreamLabel = t.StreamLabel
		d.LatestStreamArn = t.Arn + "/stream/" + t.StreamLabel
	}
	for _, g := range t.GSIs {
		n, sz := indexStats(t, g, items)
		d.GlobalSecondaryIndexes = append(d.GlobalSecondaryIndexes, GlobalSecondaryIndexDescription{
			IndexName:             g.Name,
			KeySchema:             g.KeySchema,
			Projection:            g.Projection,
			IndexStatus:           "ACTIVE",
			IndexArn:              t.Arn + "/index/" + g.Name,
			ProvisionedThroughput: throughputDesc(t, g.Throughput, false),
			OnDemandThroughput:    g.OnDemand,
			WarmThroughput:        warmDesc(g.Warm),
			ItemCount:             n,
			IndexSizeBytes:        sz,
		})
	}
	for _, l := range t.LSIs {
		n, sz := indexStats(t, l, items)
		d.LocalSecondaryIndexes = append(d.LocalSecondaryIndexes, LocalSecondaryIndexDescription{
			IndexName:      l.Name,
			KeySchema:      l.KeySchema,
			Projection:     l.Projection,
			IndexArn:       t.Arn + "/index/" + l.Name,
			ItemCount:      n,
			IndexSizeBytes: sz,
		})
	}
	return d
}

func indexStats(t *tableRecord, idx indexRecord, items []Item) (int64, int64) {
	v := &indexView{global: true, projection: idx.Projection}
	v.hash, v.rng = keyNames(idx.KeySchema)
	var n, size int64
	for _, it := range items {
		if p := projectForIndex(t, v, it); p != nil {
			n++
			size += int64(itemSize(p))
		}
	}
	return n, size
}

// ── Validation ───────────────────────────────────────────────────────────

var tableNameRe = regexp.MustCompile(`^[a-zA-Z0-9_.-]{3,255}$`)

func validateKeySchema(ks []KeySchemaElement, what string) error {
	if len(ks) == 0 || len(ks) > 2 {
		return validationErr(fmt.Sprintf("1 validation error detected: Value at '%s' failed to satisfy constraint: Member must have length less than or equal to 2", what))
	}
	if ks[0].KeyType != "HASH" {
		return validationErr("Invalid KeySchema: The first KeySchemaElement is not a HASH key type")
	}
	if len(ks) == 2 && ks[1].KeyType != "RANGE" {
		return validationErr("Invalid KeySchema: The second KeySchemaElement is not a RANGE key type")
	}
	if len(ks) == 2 && ks[0].AttributeName == ks[1].AttributeName {
		return validationErr("Both the Hash Key and the Range Key element in the KeySchema have the same name")
	}
	return nil
}

func validateProjection(p Projection) error {
	switch p.ProjectionType {
	case "ALL", "KEYS_ONLY":
		if len(p.NonKeyAttributes) > 0 {
			return validationErr("One or more parameter values were invalid: ProjectionType is " + p.ProjectionType + ", but NonKeyAttributes is specified")
		}
	case "INCLUDE":
		if len(p.NonKeyAttributes) == 0 {
			return validationErr("One or more parameter values were invalid: ProjectionType is INCLUDE, but NonKeyAttributes is not specified")
		}
	default:
		return validationErr("One or more parameter values were invalid: Unknown ProjectionType: " + p.ProjectionType)
	}
	return nil
}

// checkAttributeDefinitions makes sure every key attribute is defined and
// every definition is used by some key, as DynamoDB requires.
func checkAttributeDefinitions(t *tableRecord) error {
	used := map[string]bool{}
	mark := func(ks []KeySchemaElement) {
		for _, k := range ks {
			used[k.AttributeName] = true
		}
	}
	mark(t.KeySchema)
	for _, g := range t.GSIs {
		mark(g.KeySchema)
	}
	for _, l := range t.LSIs {
		mark(l.KeySchema)
	}
	defined := map[string]bool{}
	var names []string
	for _, a := range t.AttributeDefinitions {
		switch a.AttributeType {
		case "S", "N", "B":
		default:
			return validationErr("1 validation error detected: Value '" + a.AttributeType + "' at 'attributeDefinitions.member.attributeType' failed to satisfy constraint: Member must satisfy enum value set: [B, N, S]")
		}
		defined[a.AttributeName] = true
		names = append(names, a.AttributeName)
	}
	for k := range used {
		if !defined[k] {
			return validationErr("One or more parameter values were invalid: Some index key attributes are not defined in AttributeDefinitions. Keys: [" + k + "], AttributeDefinitions: [" + strings.Join(names, ", ") + "]")
		}
	}
	if len(defined) != len(used) {
		return validationErr("One or more parameter values were invalid: Number of attributes in KeySchema does not exactly match number of attributes defined in AttributeDefinitions")
	}
	return nil
}

func validateThroughput(billing string, pt *ProvisionedThroughput, what string) error {
	if billing == "PAY_PER_REQUEST" {
		if pt != nil && (pt.ReadCapacityUnits != 0 || pt.WriteCapacityUnits != 0) {
			return validationErr("One or more parameter values were invalid: Neither ReadCapacityUnits nor WriteCapacityUnits can be specified when BillingMode is PAY_PER_REQUEST")
		}
		return nil
	}
	if pt == nil {
		if what == "" {
			return validationErr("One or more parameter values were invalid: ReadCapacityUnits and WriteCapacityUnits must both be specified when BillingMode is PROVISIONED")
		}
		return validationErr("One or more parameter values were invalid: ProvisionedThroughput must be specified for index: " + what)
	}
	if pt.ReadCapacityUnits < 1 || pt.WriteCapacityUnits < 1 {
		return validationErr("One or more parameter values were invalid: Provisioned throughput for ReadCapacityUnits and WriteCapacityUnits must be greater than 0")
	}
	return nil
}

func throughputOrZero(pt *ProvisionedThroughput) ProvisionedThroughput {
	if pt == nil {
		return ProvisionedThroughput{}
	}
	return *pt
}

func newGSI(g GlobalSecondaryIndex, billing string) (indexRecord, error) {
	if err := validateKeySchema(g.KeySchema, "globalSecondaryIndexes.member.keySchema"); err != nil {
		return indexRecord{}, err
	}
	if err := validateProjection(g.Projection); err != nil {
		return indexRecord{}, err
	}
	if err := validateThroughput(billing, g.ProvisionedThroughput, g.IndexName); err != nil {
		return indexRecord{}, err
	}
	return indexRecord{
		Name: g.IndexName, KeySchema: g.KeySchema, Projection: g.Projection,
		Throughput: throughputOrZero(g.ProvisionedThroughput), OnDemand: g.OnDemandThroughput, Warm: g.WarmThroughput,
	}, nil
}

func (t *tableRecord) hasIndex(name string) bool {
	for _, g := range t.GSIs {
		if g.Name == name {
			return true
		}
	}
	for _, l := range t.LSIs {
		if l.Name == name {
			return true
		}
	}
	return false
}

func sseFromSpec(r *http.Request, s *SSESpecification) *SSEDescription {
	if s == nil || s.Enabled == nil || !*s.Enabled {
		return nil
	}
	d := &SSEDescription{Status: "ENABLED", SSEType: "KMS"}
	if k := s.KMSMasterKeyId; k != "" {
		if !strings.HasPrefix(k, "arn:") {
			k = "arn:aws:kms:" + util.Region(r) + ":" + util.AccountID(r) + ":key/" + k
		}
		d.KMSMasterKeyArn = k
	}
	return d
}

func streamLabel() string {
	return time.Now().UTC().Format("2006-01-02T15:04:05.000")
}

func validateStream(s *StreamSpecification) error {
	if s == nil {
		return nil
	}
	if s.StreamEnabled {
		switch s.StreamViewType {
		case "KEYS_ONLY", "NEW_IMAGE", "OLD_IMAGE", "NEW_AND_OLD_IMAGES":
		default:
			return validationErr("One or more parameter values were invalid: StreamViewType must be specified when StreamEnabled is true")
		}
	}
	return nil
}

// ── Operations ───────────────────────────────────────────────────────────

func (h *Handler) createTable(r *http.Request, body []byte) (any, error) {
	ns := util.NamespaceFromHeader(r)
	var req CreateTableInput
	if err := decode(body, &req); err != nil {
		return nil, err
	}
	if !tableNameRe.MatchString(req.TableName) {
		return nil, validationErr("1 validation error detected: Value '" + req.TableName + "' at 'tableName' failed to satisfy constraint: Member must satisfy regular expression pattern: [a-zA-Z0-9_.-]+")
	}
	if _, err := h.Store.Get(req.TableName, service, typeTable, ns); err == nil {
		return nil, newErr("ResourceInUseException", "Table already exists: "+req.TableName)
	}
	if err := validateKeySchema(req.KeySchema, "keySchema"); err != nil {
		return nil, err
	}

	billing := req.BillingMode
	if billing == "" {
		billing = "PROVISIONED"
	}
	if billing != "PROVISIONED" && billing != "PAY_PER_REQUEST" {
		return nil, validationErr("1 validation error detected: Value '" + billing + "' at 'billingMode' failed to satisfy constraint: Member must satisfy enum value set: [PROVISIONED, PAY_PER_REQUEST]")
	}
	if err := validateThroughput(billing, req.ProvisionedThroughput, ""); err != nil {
		return nil, err
	}
	if err := validateStream(req.StreamSpecification); err != nil {
		return nil, err
	}
	class := req.TableClass
	if class == "" {
		class = "STANDARD"
	}

	now := nowSeconds()
	t := &tableRecord{
		Name:                 req.TableName,
		ID:                   uuid.New().String(),
		Arn:                  "arn:aws:dynamodb:" + util.Region(r) + ":" + util.AccountID(r) + ":table/" + req.TableName,
		CreatedAt:            now,
		AttributeDefinitions: req.AttributeDefinitions,
		KeySchema:            req.KeySchema,
		BillingMode:          billing,
		Throughput:           throughputOrZero(req.ProvisionedThroughput),
		OnDemand:             req.OnDemandThroughput,
		Warm:                 req.WarmThroughput,
		SSE:                  sseFromSpec(r, req.SSESpecification),
		TableClass:           class,
		DeletionProtection:   req.DeletionProtectionEnabled,
		Tags:                 req.Tags,
	}
	if billing == "PAY_PER_REQUEST" {
		t.LastPayPerRequest = &now
	}
	if s := req.StreamSpecification; s != nil && s.StreamEnabled {
		t.Stream = s
		t.StreamLabel = streamLabel()
	}

	hash, rng := t.keys()
	for _, g := range req.GlobalSecondaryIndexes {
		if t.hasIndex(g.IndexName) {
			return nil, validationErr("One or more parameter values were invalid: Duplicate index name: " + g.IndexName)
		}
		idx, err := newGSI(g, billing)
		if err != nil {
			return nil, err
		}
		t.GSIs = append(t.GSIs, idx)
	}
	for _, l := range req.LocalSecondaryIndexes {
		if t.hasIndex(l.IndexName) {
			return nil, validationErr("One or more parameter values were invalid: Duplicate index name: " + l.IndexName)
		}
		if err := validateKeySchema(l.KeySchema, "localSecondaryIndexes.member.keySchema"); err != nil {
			return nil, err
		}
		if err := validateProjection(l.Projection); err != nil {
			return nil, err
		}
		lh, lr := keyNames(l.KeySchema)
		if rng == "" {
			return nil, validationErr("One or more parameter values were invalid: Table KeySchema does not have a range key, which is required when specifying a LocalSecondaryIndex")
		}
		if lh != hash || lr == "" {
			return nil, validationErr("One or more parameter values were invalid: Index KeySchema does not have the same leading hash key as table KeySchema for index: " + l.IndexName)
		}
		t.LSIs = append(t.LSIs, indexRecord{Name: l.IndexName, KeySchema: l.KeySchema, Projection: l.Projection})
	}
	if err := checkAttributeDefinitions(t); err != nil {
		return nil, err
	}
	if err := validateTags(t.Tags); err != nil {
		return nil, err
	}

	b, _ := json.Marshal(t)
	if err := h.Store.Create(&resource.Resource{
		ID: t.Name, Namespace: ns, Service: service, Type: typeTable, Attributes: b,
	}); err != nil {
		return nil, err
	}
	// The table is usable at once, so it is reported ACTIVE straight away;
	// the provider's waiter accepts that.
	return map[string]any{"TableDescription": h.describe(ns, t, "ACTIVE")}, nil
}

func (h *Handler) describeTable(r *http.Request, body []byte) (any, error) {
	ns := util.NamespaceFromHeader(r)
	var req struct{ TableName string }
	if err := decode(body, &req); err != nil {
		return nil, err
	}
	t, _, err := h.loadTable(ns, tableNameFromArn(req.TableName))
	if err != nil {
		return nil, err
	}
	return map[string]any{"Table": h.describe(ns, t, "ACTIVE")}, nil
}

func (h *Handler) deleteTable(r *http.Request, body []byte) (any, error) {
	ns := util.NamespaceFromHeader(r)
	var req struct{ TableName string }
	if err := decode(body, &req); err != nil {
		return nil, err
	}
	t, _, err := h.loadTable(ns, tableNameFromArn(req.TableName))
	if err != nil {
		return nil, err
	}
	if t.DeletionProtection {
		return nil, validationErr("Resource cannot be deleted as it is currently protected against deletion. Disable deletion protection first.")
	}
	desc := h.describe(ns, t, "DELETING")
	if err := h.dropTable(ns, t); err != nil {
		return nil, err
	}
	// Deleting one copy of a global table leaves the others running.
	_, own := resource.SplitScope(ns)
	if err := h.forgetRegion(ns, t, own); err != nil {
		return nil, err
	}
	return map[string]any{"TableDescription": desc}, nil
}

// dropTable deletes a table and its items from ns.
func (h *Handler) dropTable(ns string, t *tableRecord) error {
	res, err := h.Store.List(service, typeItem, ns)
	if err != nil {
		return err
	}
	prefix := t.Name + "/"
	for _, it := range res {
		if strings.HasPrefix(it.ID, prefix) {
			if err := h.Store.Delete(it.ID, service, typeItem, ns); err != nil {
				return err
			}
		}
	}
	return h.Store.Delete(t.Name, service, typeTable, ns)
}

func (h *Handler) listTables(r *http.Request, body []byte) (any, error) {
	ns := util.NamespaceFromHeader(r)
	var req struct {
		ExclusiveStartTableName string
		Limit                   int
	}
	if err := decode(body, &req); err != nil {
		return nil, err
	}
	res, err := h.Store.List(service, typeTable, ns)
	if err != nil {
		return nil, err
	}
	names := []string{}
	for _, t := range res {
		if t.ID > req.ExclusiveStartTableName {
			names = append(names, t.ID)
		}
	}
	sort.Strings(names)
	limit := req.Limit
	if limit <= 0 || limit > 100 {
		limit = 100
	}
	out := map[string]any{}
	if len(names) > limit {
		names = names[:limit]
		out["LastEvaluatedTableName"] = names[limit-1]
	}
	out["TableNames"] = names
	return out, nil
}

func (h *Handler) updateTable(r *http.Request, body []byte) (any, error) {
	ns := util.NamespaceFromHeader(r)
	var req UpdateTableInput
	if err := decode(body, &req); err != nil {
		return nil, err
	}
	t, res, err := h.loadTable(ns, tableNameFromArn(req.TableName))
	if err != nil {
		return nil, err
	}
	if req.BillingMode != "" && req.BillingMode != t.BillingMode {
		switch req.BillingMode {
		case "PAY_PER_REQUEST":
			now := nowSeconds()
			t.LastPayPerRequest = &now
			t.Throughput = ProvisionedThroughput{}
			for i := range t.GSIs {
				t.GSIs[i].Throughput = ProvisionedThroughput{}
			}
		case "PROVISIONED":
			if req.ProvisionedThroughput == nil {
				return nil, validationErr("One or more parameter values were invalid: ProvisionedThroughput must be specified when BillingMode is PROVISIONED")
			}
		default:
			return nil, validationErr("1 validation error detected: Value '" + req.BillingMode + "' at 'billingMode' failed to satisfy constraint: Member must satisfy enum value set: [PROVISIONED, PAY_PER_REQUEST]")
		}
		t.BillingMode = req.BillingMode
	}
	if pt := req.ProvisionedThroughput; pt != nil {
		if err := validateThroughput(t.BillingMode, pt, ""); err != nil {
			return nil, err
		}
		now := nowSeconds()
		if pt.ReadCapacityUnits > t.Throughput.ReadCapacityUnits || pt.WriteCapacityUnits > t.Throughput.WriteCapacityUnits {
			t.LastIncrease = &now
		}
		if pt.ReadCapacityUnits < t.Throughput.ReadCapacityUnits || pt.WriteCapacityUnits < t.Throughput.WriteCapacityUnits {
			t.LastDecrease = &now
		}
		t.Throughput = *pt
	}
	if req.OnDemandThroughput != nil {
		t.OnDemand = req.OnDemandThroughput
	}
	if req.WarmThroughput != nil {
		t.Warm = req.WarmThroughput
	}
	if req.TableClass != "" {
		t.TableClass = req.TableClass
	}
	if req.DeletionProtectionEnabled != nil {
		t.DeletionProtection = *req.DeletionProtectionEnabled
	}
	if s := req.StreamSpecification; s != nil {
		if err := validateStream(s); err != nil {
			return nil, err
		}
		enabled := t.Stream != nil && t.Stream.StreamEnabled
		switch {
		case s.StreamEnabled && enabled:
			return nil, validationErr("Table already has an enabled stream: " + t.Arn + "/stream/" + t.StreamLabel)
		case s.StreamEnabled:
			t.Stream = s
			t.StreamLabel = streamLabel()
		case enabled && len(t.Replicas) > 0:
			return nil, validationErr("Table stream cannot be disabled while the table has replicas")
		case enabled:
			t.Stream = nil
		default:
			return nil, validationErr("Table already has no stream enabled")
		}
	}
	if req.SSESpecification != nil {
		t.SSE = sseFromSpec(r, req.SSESpecification)
	}

	// New definitions merge with the old; ones no key uses are dropped below.
	for _, a := range req.AttributeDefinitions {
		found := false
		for i := range t.AttributeDefinitions {
			if t.AttributeDefinitions[i].AttributeName == a.AttributeName {
				t.AttributeDefinitions[i] = a
				found = true
			}
		}
		if !found {
			t.AttributeDefinitions = append(t.AttributeDefinitions, a)
		}
	}

	for _, u := range req.GlobalSecondaryIndexUpdates {
		switch {
		case u.Create != nil:
			if t.hasIndex(u.Create.IndexName) {
				return nil, validationErr("One or more parameter values were invalid: Index with name " + u.Create.IndexName + " already exists")
			}
			idx, err := newGSI(*u.Create, t.BillingMode)
			if err != nil {
				return nil, err
			}
			t.GSIs = append(t.GSIs, idx)
		case u.Update != nil:
			i := t.gsiIndex(u.Update.IndexName)
			if i < 0 {
				return nil, newErr("ResourceNotFoundException", "Requested resource not found: Index: "+u.Update.IndexName+" not found")
			}
			if pt := u.Update.ProvisionedThroughput; pt != nil {
				if err := validateThroughput(t.BillingMode, pt, u.Update.IndexName); err != nil {
					return nil, err
				}
				t.GSIs[i].Throughput = *pt
			}
			if u.Update.OnDemandThroughput != nil {
				t.GSIs[i].OnDemand = u.Update.OnDemandThroughput
			}
			if u.Update.WarmThroughput != nil {
				t.GSIs[i].Warm = u.Update.WarmThroughput
			}
		case u.Delete != nil:
			i := t.gsiIndex(u.Delete.IndexName)
			if i < 0 {
				return nil, newErr("ResourceNotFoundException", "Requested resource not found: Index: "+u.Delete.IndexName+" not found")
			}
			t.GSIs = append(t.GSIs[:i], t.GSIs[i+1:]...)
		}
	}
	if t.BillingMode == "PROVISIONED" {
		for _, g := range t.GSIs {
			if g.Throughput.ReadCapacityUnits < 1 || g.Throughput.WriteCapacityUnits < 1 {
				return nil, validationErr("One or more parameter values were invalid: ProvisionedThroughput must be specified for index: " + g.Name)
			}
		}
	}
	t.pruneAttributeDefinitions()
	if err := checkAttributeDefinitions(t); err != nil {
		return nil, err
	}
	if err := h.syncReplicas(ns, t); err != nil {
		return nil, err
	}
	if err := h.replicaUpdates(r, ns, t, req.ReplicaUpdates); err != nil {
		return nil, err
	}

	if err := h.saveTable(res, t); err != nil {
		return nil, err
	}
	return map[string]any{"TableDescription": h.describe(ns, t, "ACTIVE")}, nil
}

func (t *tableRecord) gsiIndex(name string) int {
	for i, g := range t.GSIs {
		if g.Name == name {
			return i
		}
	}
	return -1
}

// pruneAttributeDefinitions drops definitions no key schema uses any more,
// which is what DynamoDB reports after an index is deleted.
func (t *tableRecord) pruneAttributeDefinitions() {
	used := map[string]bool{}
	for _, k := range t.KeySchema {
		used[k.AttributeName] = true
	}
	for _, g := range t.GSIs {
		for _, k := range g.KeySchema {
			used[k.AttributeName] = true
		}
	}
	for _, l := range t.LSIs {
		for _, k := range l.KeySchema {
			used[k.AttributeName] = true
		}
	}
	var keep []AttributeDefinition
	for _, a := range t.AttributeDefinitions {
		if used[a.AttributeName] {
			keep = append(keep, a)
		}
	}
	t.AttributeDefinitions = keep
}

func (h *Handler) describeTimeToLive(r *http.Request, body []byte) (any, error) {
	ns := util.NamespaceFromHeader(r)
	var req struct{ TableName string }
	if err := decode(body, &req); err != nil {
		return nil, err
	}
	t, _, err := h.loadTable(ns, tableNameFromArn(req.TableName))
	if err != nil {
		return nil, err
	}
	d := TimeToLiveDescription{TimeToLiveStatus: "DISABLED"}
	if t.TTL.Enabled {
		d = TimeToLiveDescription{TimeToLiveStatus: "ENABLED", AttributeName: t.TTL.AttributeName}
	}
	return map[string]any{"TimeToLiveDescription": d}, nil
}

func (h *Handler) updateTimeToLive(r *http.Request, body []byte) (any, error) {
	ns := util.NamespaceFromHeader(r)
	var req struct {
		TableName               string
		TimeToLiveSpecification *TimeToLiveSpecification
	}
	if err := decode(body, &req); err != nil {
		return nil, err
	}
	t, res, err := h.loadTable(ns, tableNameFromArn(req.TableName))
	if err != nil {
		return nil, err
	}
	spec := req.TimeToLiveSpecification
	if spec == nil || spec.AttributeName == "" {
		return nil, validationErr("1 validation error detected: Value null at 'timeToLiveSpecification.attributeName' failed to satisfy constraint: Member must not be null")
	}
	switch {
	case spec.Enabled && t.TTL.Enabled && spec.AttributeName != t.TTL.AttributeName:
		return nil, validationErr("TimeToLive is active on a different AttributeName: current AttributeName is " + t.TTL.AttributeName)
	case spec.Enabled && t.TTL.Enabled:
		return nil, validationErr("TimeToLive is already enabled")
	case !spec.Enabled && !t.TTL.Enabled:
		return nil, validationErr("TimeToLive is already disabled")
	}
	t.TTL = *spec
	if err := h.saveTable(res, t); err != nil {
		return nil, err
	}
	return map[string]any{"TimeToLiveSpecification": spec}, nil
}

func (t *tableRecord) backups() ContinuousBackupsDescription {
	p := PointInTimeRecoveryDescription{PointInTimeRecoveryStatus: "DISABLED"}
	if t.PITR {
		days := t.PITRDays
		if days == 0 {
			days = 35
		}
		latest := nowSeconds()
		earliest := max(t.CreatedAt, latest-float64(days)*86400)
		p = PointInTimeRecoveryDescription{
			PointInTimeRecoveryStatus:  "ENABLED",
			RecoveryPeriodInDays:       &days,
			EarliestRestorableDateTime: &earliest,
			LatestRestorableDateTime:   &latest,
		}
	}
	return ContinuousBackupsDescription{ContinuousBackupsStatus: "ENABLED", PointInTimeRecoveryDescription: p}
}

func (h *Handler) loadTableForBackups(ns, name string) (*tableRecord, *resource.Resource, error) {
	t, res, err := h.loadTable(ns, name)
	if err != nil {
		var ae *apiError
		if asAPI(err, &ae) && ae.Code == "ResourceNotFoundException" {
			return nil, nil, newErr("TableNotFoundException", "Table not found: "+name)
		}
	}
	return t, res, err
}

func (h *Handler) describeContinuousBackups(r *http.Request, body []byte) (any, error) {
	ns := util.NamespaceFromHeader(r)
	var req struct{ TableName string }
	if err := decode(body, &req); err != nil {
		return nil, err
	}
	t, _, err := h.loadTableForBackups(ns, tableNameFromArn(req.TableName))
	if err != nil {
		return nil, err
	}
	return map[string]any{"ContinuousBackupsDescription": t.backups()}, nil
}

func (h *Handler) updateContinuousBackups(r *http.Request, body []byte) (any, error) {
	ns := util.NamespaceFromHeader(r)
	var req struct {
		TableName                        string
		PointInTimeRecoverySpecification *struct {
			PointInTimeRecoveryEnabled *bool
			RecoveryPeriodInDays       *int32
		}
	}
	if err := decode(body, &req); err != nil {
		return nil, err
	}
	t, res, err := h.loadTableForBackups(ns, tableNameFromArn(req.TableName))
	if err != nil {
		return nil, err
	}
	spec := req.PointInTimeRecoverySpecification
	if spec == nil || spec.PointInTimeRecoveryEnabled == nil {
		return nil, validationErr("1 validation error detected: Value null at 'pointInTimeRecoverySpecification.pointInTimeRecoveryEnabled' failed to satisfy constraint: Member must not be null")
	}
	if d := spec.RecoveryPeriodInDays; d != nil {
		if !*spec.PointInTimeRecoveryEnabled {
			return nil, validationErr("RecoveryPeriodInDays can only be specified when PointInTimeRecoveryEnabled is true")
		}
		if *d < 1 || *d > 35 {
			return nil, validationErr("RecoveryPeriodInDays must be between 1 and 35")
		}
	}
	t.PITR = *spec.PointInTimeRecoveryEnabled
	t.PITRDays = 0
	if t.PITR && spec.RecoveryPeriodInDays != nil {
		t.PITRDays = *spec.RecoveryPeriodInDays
	}
	if err := h.saveTable(res, t); err != nil {
		return nil, err
	}
	return map[string]any{"ContinuousBackupsDescription": t.backups()}, nil
}

func validateTags(tags []Tag) error {
	if len(tags) > 50 {
		return validationErr("One or more parameter values were invalid: Too many tags")
	}
	for _, t := range tags {
		if t.Key == "" || len(t.Key) > 128 || len(t.Value) > 256 {
			return validationErr("One or more parameter values were invalid: Invalid tag key or value")
		}
		if strings.HasPrefix(strings.ToLower(t.Key), "aws:") {
			return validationErr("One or more parameter values were invalid: Tag keys starting with 'aws:' are reserved for system use")
		}
	}
	return nil
}

func (h *Handler) tableForArn(r *http.Request, arn string) (*tableRecord, *resource.Resource, error) {
	if arn == "" {
		return nil, nil, validationErr("1 validation error detected: Value null at 'resourceArn' failed to satisfy constraint: Member must not be null")
	}
	if !strings.HasPrefix(arn, "arn:") {
		return nil, nil, validationErr("Invalid TableArn: " + arn)
	}
	name := tableNameFromArn(arn)
	t, res, err := h.loadTable(util.NamespaceFromHeader(r), name)
	if err != nil {
		return nil, nil, newErr("ResourceNotFoundException", "Requested resource not found: ResourcArn: "+arn+" not found")
	}
	return t, res, nil
}

func (h *Handler) listTagsOfResource(r *http.Request, body []byte) (any, error) {
	var req struct{ ResourceArn string }
	if err := decode(body, &req); err != nil {
		return nil, err
	}
	t, _, err := h.tableForArn(r, req.ResourceArn)
	if err != nil {
		return nil, err
	}
	tags := t.Tags
	if tags == nil {
		tags = []Tag{}
	}
	return map[string]any{"Tags": tags}, nil
}

func (h *Handler) tagResource(r *http.Request, body []byte) (any, error) {
	var req struct {
		ResourceArn string
		Tags        []Tag
	}
	if err := decode(body, &req); err != nil {
		return nil, err
	}
	t, res, err := h.tableForArn(r, req.ResourceArn)
	if err != nil {
		return nil, err
	}
	if err := validateTags(req.Tags); err != nil {
		return nil, err
	}
	for _, nt := range req.Tags {
		replaced := false
		for i := range t.Tags {
			if t.Tags[i].Key == nt.Key {
				t.Tags[i].Value = nt.Value
				replaced = true
			}
		}
		if !replaced {
			t.Tags = append(t.Tags, nt)
		}
	}
	if err := validateTags(t.Tags); err != nil {
		return nil, err
	}
	return map[string]any{}, h.saveTable(res, t)
}

func (h *Handler) untagResource(r *http.Request, body []byte) (any, error) {
	var req struct {
		ResourceArn string
		TagKeys     []string
	}
	if err := decode(body, &req); err != nil {
		return nil, err
	}
	t, res, err := h.tableForArn(r, req.ResourceArn)
	if err != nil {
		return nil, err
	}
	drop := map[string]bool{}
	for _, k := range req.TagKeys {
		drop[k] = true
	}
	var keep []Tag
	for _, tg := range t.Tags {
		if !drop[tg.Key] {
			keep = append(keep, tg)
		}
	}
	t.Tags = keep
	return map[string]any{}, h.saveTable(res, t)
}

func (h *Handler) describeLimits(r *http.Request, body []byte) (any, error) {
	return map[string]any{
		"AccountMaxReadCapacityUnits":  80000,
		"AccountMaxWriteCapacityUnits": 80000,
		"TableMaxReadCapacityUnits":    40000,
		"TableMaxWriteCapacityUnits":   40000,
	}, nil
}

func (h *Handler) describeEndpoints(r *http.Request, body []byte) (any, error) {
	return map[string]any{"Endpoints": []map[string]any{{"Address": r.Host, "CachePeriodInMinutes": 1440}}}, nil
}
