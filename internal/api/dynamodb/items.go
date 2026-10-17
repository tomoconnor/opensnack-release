// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package dynamodb

import (
	"encoding/json"
	"errors"
	"fmt"
	"hash/fnv"
	"net/http"
	"sort"
	"strings"

	"opensnack/internal/resource"
	"opensnack/internal/util"
)

const maxItemSize = 400 * 1024

func asAPI(err error, target **apiError) bool { return errors.As(err, target) }

// ── Storage ──────────────────────────────────────────────────────────────

type storedItem struct {
	Table string `json:"table"`
	Item  Item   `json:"item"`
}

// itemID is the store ID for the item with the given primary key.
func itemID(t *tableRecord, it Item) string {
	hash, rng := t.keys()
	parts := []string{it[hash].canonical()}
	if rng != "" {
		parts = append(parts, it[rng].canonical())
	}
	b, _ := json.Marshal(parts)
	return t.Name + "/" + string(b)
}

func (h *Handler) loadItem(ns string, t *tableRecord, key Item) (Item, error) {
	res, err := h.Store.Get(itemID(t, key), service, typeItem, ns)
	if err != nil || res == nil || len(res.Attributes) == 0 {
		return nil, nil
	}
	var s storedItem
	if err := json.Unmarshal(res.Attributes, &s); err != nil {
		return nil, err
	}
	return s.Item, nil
}

func (h *Handler) storeItem(ns string, t *tableRecord, it Item, existed bool) error {
	b, err := json.Marshal(storedItem{Table: t.Name, Item: it})
	if err != nil {
		return err
	}
	res := &resource.Resource{ID: itemID(t, it), Namespace: ns, Service: service, Type: typeItem, Attributes: b}
	if existed {
		return h.Store.Update(res)
	}
	return h.Store.Create(res)
}

func (h *Handler) removeItem(ns string, t *tableRecord, key Item) error {
	return h.Store.Delete(itemID(t, key), service, typeItem, ns)
}

// tableItems returns every item in a table, in primary-key order.
func (h *Handler) tableItems(ns, table string) []Item {
	res, err := h.Store.List(service, typeItem, ns)
	if err != nil {
		return nil
	}
	prefix := table + "/"
	sort.Slice(res, func(i, j int) bool { return res[i].ID < res[j].ID })
	var out []Item
	for _, r := range res {
		if !strings.HasPrefix(r.ID, prefix) {
			continue
		}
		var s storedItem
		if json.Unmarshal(r.Attributes, &s) == nil && s.Item != nil {
			out = append(out, s.Item)
		}
	}
	return out
}

// ── Keys and validation ──────────────────────────────────────────────────

// extractKey checks that key holds exactly the table's key attributes, with
// the right types.
func extractKey(t *tableRecord, key Item) (Item, error) {
	hash, rng := t.keys()
	want := 1
	if rng != "" {
		want = 2
	}
	if len(key) != want {
		return nil, validationErr("The provided key element does not match the schema")
	}
	for _, name := range []string{hash, rng} {
		if name == "" {
			continue
		}
		v := key[name]
		if v == nil || v.Type != t.attrType(name) {
			return nil, validationErr("The provided key element does not match the schema")
		}
		if v.S == "" && v.Type != "N" {
			return nil, validationErr("One or more parameter values are not valid. The AttributeValue for a key attribute cannot contain an empty " + map[string]string{"S": "string", "B": "binary"}[v.Type] + " value. Key: " + name)
		}
	}
	return key, nil
}

func keyOf(t *tableRecord, it Item) Item {
	hash, rng := t.keys()
	k := Item{hash: it[hash]}
	if rng != "" {
		k[rng] = it[rng]
	}
	return k
}

// validateItem checks a whole item before it is written: key attributes,
// index key types and DynamoDB's value rules.
func validateItem(t *tableRecord, it Item) error {
	hash, rng := t.keys()
	for _, name := range []string{hash, rng} {
		if name == "" {
			continue
		}
		v := it[name]
		if v == nil {
			return validationErr("One or more parameter values were invalid: Missing the key " + name + " in the item")
		}
		if want := t.attrType(name); v.Type != want {
			return validationErr(fmt.Sprintf("One or more parameter values were invalid: Type mismatch for key %s expected: %s actual: %s", name, want, v.Type))
		}
		if v.S == "" && v.Type != "N" {
			return validationErr("One or more parameter values are not valid. The AttributeValue for a key attribute cannot contain an empty string value. Key: " + name)
		}
	}
	for _, idx := range append(append([]indexRecord{}, t.GSIs...), t.LSIs...) {
		for _, k := range idx.KeySchema {
			v := it[k.AttributeName]
			if v == nil || k.AttributeName == hash || k.AttributeName == rng {
				continue
			}
			if want := t.attrType(k.AttributeName); v.Type != want {
				return validationErr(fmt.Sprintf("One or more parameter values were invalid: Type mismatch for Index Key %s Expected: %s Actual: %s IndexName: %s", k.AttributeName, want, v.Type, idx.Name))
			}
			if v.S == "" && v.Type != "N" {
				return validationErr("One or more parameter values are not valid. A value specified for a secondary index key is not supported. The AttributeValue for a key attribute cannot contain an empty string value. IndexName: " + idx.Name + ", IndexKey: " + k.AttributeName)
			}
		}
	}
	for _, v := range it {
		if v == nil {
			return validationErr("Supplied AttributeValue is empty, must contain exactly one of the supported datatypes")
		}
		if err := validateValue(v); err != nil {
			return err
		}
	}
	if itemSize(it) > maxItemSize {
		return validationErr("Item size has exceeded the maximum allowed size")
	}
	return nil
}

// projectForIndex returns the item as the index stores it, or nil when the
// item is not in the (sparse) index.
func projectForIndex(t *tableRecord, v *indexView, it Item) Item {
	if v.name == "" {
		return it
	}
	if it[v.hash] == nil || (v.rng != "" && it[v.rng] == nil) {
		return nil
	}
	if v.projection.ProjectionType == "ALL" {
		return it
	}
	hash, rng := t.keys()
	keep := map[string]bool{hash: true, rng: true, v.hash: true, v.rng: true}
	if v.projection.ProjectionType == "INCLUDE" {
		for _, a := range v.projection.NonKeyAttributes {
			keep[a] = true
		}
	}
	out := Item{}
	for k, val := range it {
		if keep[k] {
			out[k] = val
		}
	}
	return out
}

// ── Common request pieces ────────────────────────────────────────────────

type exprInput struct {
	ExpressionAttributeNames  map[string]string
	ExpressionAttributeValues map[string]*AttributeValue
}

func (e exprInput) context() (*ExprContext, error) {
	for k, v := range e.ExpressionAttributeValues {
		if !strings.HasPrefix(k, ":") {
			return nil, validationErr("ExpressionAttributeValues contains invalid key: Syntax error; key: \"" + k + "\"")
		}
		if v == nil {
			return nil, validationErr("ExpressionAttributeValues contains invalid value: Supplied AttributeValue is empty, must contain exactly one of the supported datatypes for key " + k)
		}
		if err := validateValue(v); err != nil {
			return nil, err
		}
	}
	for k := range e.ExpressionAttributeNames {
		if !strings.HasPrefix(k, "#") {
			return nil, validationErr("ExpressionAttributeNames contains invalid key: Syntax error; key: \"" + k + "\"")
		}
	}
	return NewExprContext(e.ExpressionAttributeNames, e.ExpressionAttributeValues), nil
}

// condition parses ConditionExpression or the legacy Expected map. A nil
// condition always passes.
func parseWriteCondition(ctx *ExprContext, expr string, expected map[string]legacyExpected, op string) (Condition, error) {
	if expr != "" && len(expected) > 0 {
		return nil, validationErr("Can not use both expression and non-expression parameters in the same request: Non-expression parameters: {Expected} Expression parameters: {ConditionExpression}")
	}
	if expr != "" {
		return ParseCondition(ctx, "ConditionExpression", expr)
	}
	if len(expected) > 0 {
		return expectedCondition(expected, op)
	}
	return nil, nil
}

func checkCondition(c Condition, old Item, returnOnFail string) error {
	if c == nil || EvalCondition(c, old) {
		return nil
	}
	e := conditionFailed()
	if returnOnFail == "ALL_OLD" && old != nil {
		e.Extra = map[string]any{"Item": old}
	}
	return e
}

func consumed(table, mode string, units float64) map[string]any {
	if mode != "TOTAL" && mode != "INDEXES" {
		return nil
	}
	return map[string]any{"TableName": table, "CapacityUnits": units}
}

func withConsumed(out map[string]any, table, mode string, units float64) map[string]any {
	if c := consumed(table, mode, units); c != nil {
		out["ConsumedCapacity"] = c
	}
	return out
}

// ── PutItem / GetItem / DeleteItem ───────────────────────────────────────

type putInput struct {
	exprInput
	TableName                           string
	Item                                Item
	ConditionExpression                 string
	Expected                            map[string]legacyExpected
	ConditionalOperator                 string
	ReturnValues                        string
	ReturnConsumedCapacity              string
	ReturnValuesOnConditionCheckFailure string
}

// preparedWrite is a validated write that has not been applied yet, so
// transactions can check everything before changing anything.
type preparedWrite struct {
	t       *tableRecord
	key     Item
	old     Item
	newItem Item // nil for a delete
	cond    Condition
	retFail string
	touched []string
	// checkOnly is a transaction ConditionCheck: nothing is written.
	checkOnly bool
}

func (h *Handler) preparePut(ns string, req *putInput) (*preparedWrite, error) {
	t, _, err := h.loadTable(ns, req.TableName)
	if err != nil {
		return nil, err
	}
	if req.Item == nil {
		return nil, validationErr("1 validation error detected: Value null at 'item' failed to satisfy constraint: Member must not be null")
	}
	if err := validateItem(t, req.Item); err != nil {
		return nil, err
	}
	ctx, err := req.context()
	if err != nil {
		return nil, err
	}
	cond, err := parseWriteCondition(ctx, req.ConditionExpression, req.Expected, req.ConditionalOperator)
	if err != nil {
		return nil, err
	}
	if err := ctx.CheckUnused(); err != nil {
		return nil, err
	}
	key := keyOf(t, req.Item)
	old, err := h.loadItem(ns, t, key)
	if err != nil {
		return nil, err
	}
	return &preparedWrite{t: t, key: key, old: old, newItem: req.Item, cond: cond, retFail: req.ReturnValuesOnConditionCheckFailure}, nil
}

func (h *Handler) apply(ns string, w *preparedWrite) error {
	var err error
	switch {
	case w.newItem != nil:
		err = h.storeItem(ns, w.t, w.newItem, w.old != nil)
	case w.old != nil:
		err = h.removeItem(ns, w.t, w.key)
	default:
		return nil
	}
	if err != nil {
		return err
	}
	return h.replicate(ns, w)
}

func (h *Handler) putItem(r *http.Request, body []byte) (any, error) {
	ns := util.NamespaceFromHeader(r)
	var req putInput
	if err := decode(body, &req); err != nil {
		return nil, err
	}
	switch req.ReturnValues {
	case "", "NONE", "ALL_OLD":
	default:
		return nil, validationErr("ReturnValues can only be ALL_OLD or NONE")
	}
	w, err := h.preparePut(ns, &req)
	if err != nil {
		return nil, err
	}
	if err := checkCondition(w.cond, w.old, w.retFail); err != nil {
		return nil, err
	}
	if err := h.apply(ns, w); err != nil {
		return nil, err
	}
	out := map[string]any{}
	if req.ReturnValues == "ALL_OLD" && w.old != nil {
		out["Attributes"] = w.old
	}
	return withConsumed(out, w.t.Name, req.ReturnConsumedCapacity, 1), nil
}

type getInput struct {
	exprInput
	TableName              string
	Key                    Item
	ProjectionExpression   string
	AttributesToGet        []string
	ConsistentRead         bool
	ReturnConsumedCapacity string
}

func projectionFor(ctx *ExprContext, expr string, attrs []string) ([]*PathExpr, error) {
	if expr != "" && len(attrs) > 0 {
		return nil, validationErr("Can not use both expression and non-expression parameters in the same request: Non-expression parameters: {AttributesToGet} Expression parameters: {ProjectionExpression}")
	}
	if expr != "" {
		return ParseProjection(ctx, expr)
	}
	var paths []*PathExpr
	for _, a := range attrs {
		paths = append(paths, &PathExpr{Elems: []PathElem{{Name: a}}})
	}
	return paths, nil
}

func (h *Handler) readItem(ns string, req *getInput) (*tableRecord, Item, error) {
	t, _, err := h.loadTable(ns, req.TableName)
	if err != nil {
		return nil, nil, err
	}
	key, err := extractKey(t, req.Key)
	if err != nil {
		return nil, nil, err
	}
	ctx, err := req.context()
	if err != nil {
		return nil, nil, err
	}
	proj, err := projectionFor(ctx, req.ProjectionExpression, req.AttributesToGet)
	if err != nil {
		return nil, nil, err
	}
	if err := ctx.CheckUnused(); err != nil {
		return nil, nil, err
	}
	it, err := h.loadItem(ns, t, key)
	if err != nil || it == nil {
		return t, nil, err
	}
	if proj != nil {
		it = Project(it, proj)
	}
	return t, it, nil
}

func (h *Handler) getItem(r *http.Request, body []byte) (any, error) {
	ns := util.NamespaceFromHeader(r)
	var req getInput
	if err := decode(body, &req); err != nil {
		return nil, err
	}
	t, it, err := h.readItem(ns, &req)
	if err != nil {
		return nil, err
	}
	out := map[string]any{}
	if it != nil {
		out["Item"] = it
	}
	return withConsumed(out, t.Name, req.ReturnConsumedCapacity, 0.5), nil
}

type deleteInput struct {
	exprInput
	TableName                           string
	Key                                 Item
	ConditionExpression                 string
	Expected                            map[string]legacyExpected
	ConditionalOperator                 string
	ReturnValues                        string
	ReturnConsumedCapacity              string
	ReturnValuesOnConditionCheckFailure string
}

func (h *Handler) prepareDelete(ns string, req *deleteInput) (*preparedWrite, error) {
	t, _, err := h.loadTable(ns, req.TableName)
	if err != nil {
		return nil, err
	}
	key, err := extractKey(t, req.Key)
	if err != nil {
		return nil, err
	}
	ctx, err := req.context()
	if err != nil {
		return nil, err
	}
	cond, err := parseWriteCondition(ctx, req.ConditionExpression, req.Expected, req.ConditionalOperator)
	if err != nil {
		return nil, err
	}
	if err := ctx.CheckUnused(); err != nil {
		return nil, err
	}
	old, err := h.loadItem(ns, t, key)
	if err != nil {
		return nil, err
	}
	return &preparedWrite{t: t, key: key, old: old, cond: cond, retFail: req.ReturnValuesOnConditionCheckFailure}, nil
}

func (h *Handler) deleteItem(r *http.Request, body []byte) (any, error) {
	ns := util.NamespaceFromHeader(r)
	var req deleteInput
	if err := decode(body, &req); err != nil {
		return nil, err
	}
	switch req.ReturnValues {
	case "", "NONE", "ALL_OLD":
	default:
		return nil, validationErr("ReturnValues can only be ALL_OLD or NONE")
	}
	w, err := h.prepareDelete(ns, &req)
	if err != nil {
		return nil, err
	}
	if err := checkCondition(w.cond, w.old, w.retFail); err != nil {
		return nil, err
	}
	if err := h.apply(ns, w); err != nil {
		return nil, err
	}
	out := map[string]any{}
	if req.ReturnValues == "ALL_OLD" && w.old != nil {
		out["Attributes"] = w.old
	}
	return withConsumed(out, w.t.Name, req.ReturnConsumedCapacity, 1), nil
}

// ── UpdateItem ───────────────────────────────────────────────────────────

type attributeUpdate struct {
	Action string
	Value  *AttributeValue
}

type updateInput struct {
	exprInput
	TableName                           string
	Key                                 Item
	UpdateExpression                    string
	AttributeUpdates                    map[string]attributeUpdate
	ConditionExpression                 string
	Expected                            map[string]legacyExpected
	ConditionalOperator                 string
	ReturnValues                        string
	ReturnConsumedCapacity              string
	ReturnValuesOnConditionCheckFailure string
}

// legacyUpdate turns AttributeUpdates into the equivalent update expression.
func legacyUpdate(updates map[string]attributeUpdate) (*UpdateExpr, error) {
	u := &UpdateExpr{}
	for _, name := range sortedKeys(updates) {
		a := updates[name]
		path := &PathExpr{Elems: []PathElem{{Name: name}}}
		switch a.Action {
		case "", "PUT":
			if a.Value == nil {
				return nil, validationErr("One or more parameter values were invalid: Only DELETE action is allowed when no attribute value is specified")
			}
			u.Set = append(u.Set, SetAction{Path: path, Value: &ValueExpr{V: a.Value}})
		case "DELETE":
			if a.Value == nil {
				u.Remove = append(u.Remove, path)
			} else {
				u.Delete = append(u.Delete, AddAction{Path: path, Value: &ValueExpr{V: a.Value}})
			}
		case "ADD":
			if a.Value == nil {
				return nil, validationErr("One or more parameter values were invalid: ADD action requires a value")
			}
			u.Add = append(u.Add, AddAction{Path: path, Value: &ValueExpr{V: a.Value}})
		default:
			return nil, validationErr("1 validation error detected: Value '" + a.Action + "' at 'attributeUpdates' failed to satisfy constraint: Member must satisfy enum value set: [ADD, PUT, DELETE]")
		}
	}
	return u, nil
}

func (h *Handler) prepareUpdate(ns string, req *updateInput) (*preparedWrite, error) {
	t, _, err := h.loadTable(ns, req.TableName)
	if err != nil {
		return nil, err
	}
	key, err := extractKey(t, req.Key)
	if err != nil {
		return nil, err
	}
	ctx, err := req.context()
	if err != nil {
		return nil, err
	}
	var u *UpdateExpr
	switch {
	case req.UpdateExpression != "" && len(req.AttributeUpdates) > 0:
		return nil, validationErr("Can not use both expression and non-expression parameters in the same request: Non-expression parameters: {AttributeUpdates} Expression parameters: {UpdateExpression}")
	case req.UpdateExpression != "":
		if u, err = ParseUpdate(ctx, req.UpdateExpression); err != nil {
			return nil, err
		}
	default:
		if u, err = legacyUpdate(req.AttributeUpdates); err != nil {
			return nil, err
		}
	}
	cond, err := parseWriteCondition(ctx, req.ConditionExpression, req.Expected, req.ConditionalOperator)
	if err != nil {
		return nil, err
	}
	if err := ctx.CheckUnused(); err != nil {
		return nil, err
	}

	hash, rng := t.keys()
	for _, p := range updatePaths(u) {
		if n := p.topLevel(); n == hash || n == rng {
			return nil, validationErr("One or more parameter values were invalid: Cannot update attribute " + n + ". This attribute is part of the key")
		}
	}

	old, err := h.loadItem(ns, t, key)
	if err != nil {
		return nil, err
	}
	base := old
	if base == nil {
		base = key.clone()
	}
	newItem, touched, err := ApplyUpdate(u, base)
	if err != nil {
		return nil, err
	}
	if err := validateItem(t, newItem); err != nil {
		return nil, err
	}
	return &preparedWrite{t: t, key: key, old: old, newItem: newItem, cond: cond, retFail: req.ReturnValuesOnConditionCheckFailure, touched: touched}, nil
}

func updatePaths(u *UpdateExpr) []*PathExpr {
	var out []*PathExpr
	for _, a := range u.Set {
		out = append(out, a.Path)
	}
	out = append(out, u.Remove...)
	for _, a := range u.Add {
		out = append(out, a.Path)
	}
	for _, a := range u.Delete {
		out = append(out, a.Path)
	}
	return out
}

func pick(it Item, names []string) Item {
	out := Item{}
	for _, n := range names {
		if v, ok := it[n]; ok {
			out[n] = v
		}
	}
	return out
}

func (h *Handler) updateItem(r *http.Request, body []byte) (any, error) {
	ns := util.NamespaceFromHeader(r)
	var req updateInput
	if err := decode(body, &req); err != nil {
		return nil, err
	}
	switch req.ReturnValues {
	case "", "NONE", "ALL_OLD", "UPDATED_OLD", "ALL_NEW", "UPDATED_NEW":
	default:
		return nil, validationErr("1 validation error detected: Value '" + req.ReturnValues + "' at 'returnValues' failed to satisfy constraint: Member must satisfy enum value set: [ALL_NEW, UPDATED_OLD, ALL_OLD, NONE, UPDATED_NEW]")
	}
	w, err := h.prepareUpdate(ns, &req)
	if err != nil {
		return nil, err
	}
	if err := checkCondition(w.cond, w.old, w.retFail); err != nil {
		return nil, err
	}
	if err := h.apply(ns, w); err != nil {
		return nil, err
	}

	out := map[string]any{}
	var attrs Item
	switch req.ReturnValues {
	case "ALL_OLD":
		attrs = w.old
	case "UPDATED_OLD":
		if w.old != nil {
			attrs = pick(w.old, w.touched)
		}
	case "ALL_NEW":
		attrs = w.newItem
	case "UPDATED_NEW":
		attrs = pick(w.newItem, w.touched)
	}
	if len(attrs) > 0 {
		out["Attributes"] = attrs
	}
	return withConsumed(out, w.t.Name, req.ReturnConsumedCapacity, 1), nil
}

// ── Query / Scan ─────────────────────────────────────────────────────────

type readInput struct {
	exprInput
	TableName              string
	IndexName              string
	KeyConditionExpression string
	KeyConditions          map[string]legacyCondition
	FilterExpression       string
	QueryFilter            map[string]legacyCondition
	ScanFilter             map[string]legacyCondition
	ConditionalOperator    string
	ProjectionExpression   string
	AttributesToGet        []string
	Select                 string
	Limit                  *int
	ExclusiveStartKey      Item
	ScanIndexForward       *bool
	ConsistentRead         bool
	Segment                *int
	TotalSegments          *int
	ReturnConsumedCapacity string
}

func (h *Handler) query(r *http.Request, body []byte) (any, error) {
	return h.read(r, body, true)
}

func (h *Handler) scan(r *http.Request, body []byte) (any, error) {
	return h.read(r, body, false)
}

func (h *Handler) read(r *http.Request, body []byte, isQuery bool) (any, error) {
	ns := util.NamespaceFromHeader(r)
	var req readInput
	if err := decode(body, &req); err != nil {
		return nil, err
	}
	t, _, err := h.loadTable(ns, req.TableName)
	if err != nil {
		return nil, err
	}
	view, err := t.view(req.IndexName)
	if err != nil {
		return nil, err
	}
	if view.global && req.ConsistentRead {
		return nil, validationErr("Consistent reads are not supported on global secondary indexes")
	}
	if req.Limit != nil && *req.Limit < 1 {
		return nil, validationErr("1 validation error detected: Value '" + fmt.Sprint(*req.Limit) + "' at 'limit' failed to satisfy constraint: Member must have value greater than or equal to 1")
	}
	ctx, err := req.context()
	if err != nil {
		return nil, err
	}

	var keyCond Condition
	if isQuery {
		switch {
		case req.KeyConditionExpression != "" && len(req.KeyConditions) > 0:
			return nil, validationErr("Can not use both expression and non-expression parameters in the same request: Non-expression parameters: {KeyConditions} Expression parameters: {KeyConditionExpression}")
		case req.KeyConditionExpression != "":
			if keyCond, err = ParseCondition(ctx, "KeyConditionExpression", req.KeyConditionExpression); err != nil {
				return nil, err
			}
		case len(req.KeyConditions) > 0:
			if keyCond, err = legacyConditions(req.KeyConditions, "AND"); err != nil {
				return nil, err
			}
		default:
			return nil, validationErr("Either the KeyConditions or KeyConditionExpression parameter must be specified in the request.")
		}
		if err := validateKeyCondition(keyCond, view); err != nil {
			return nil, err
		}
	}

	legacyFilter := req.ScanFilter
	if isQuery {
		legacyFilter = req.QueryFilter
	}
	var filter Condition
	switch {
	case req.FilterExpression != "" && len(legacyFilter) > 0:
		return nil, validationErr("Can not use both expression and non-expression parameters in the same request: Non-expression parameters: {QueryFilter} Expression parameters: {FilterExpression}")
	case req.FilterExpression != "":
		if filter, err = ParseCondition(ctx, "FilterExpression", req.FilterExpression); err != nil {
			return nil, err
		}
		if isQuery {
			for _, p := range conditionPaths(filter) {
				if n := p.topLevel(); n == view.hash || n == view.rng {
					return nil, validationErr("Filter Expression can only contain non-primary key attributes: Primary key attribute: " + n)
				}
			}
		}
	case len(legacyFilter) > 0:
		if filter, err = legacyConditions(legacyFilter, req.ConditionalOperator); err != nil {
			return nil, err
		}
	}

	proj, err := projectionFor(ctx, req.ProjectionExpression, req.AttributesToGet)
	if err != nil {
		return nil, err
	}
	switch req.Select {
	case "", "ALL_ATTRIBUTES", "ALL_PROJECTED_ATTRIBUTES", "COUNT":
		if proj != nil && req.Select != "" {
			return nil, validationErr("Cannot specify the ProjectionExpression or AttributesToGet when choosing to get " + req.Select)
		}
		if req.Select == "ALL_PROJECTED_ATTRIBUTES" && view.name == "" {
			return nil, validationErr("ALL_PROJECTED_ATTRIBUTES can be used only when Querying using an IndexName")
		}
		if req.Select == "ALL_ATTRIBUTES" && view.name != "" && view.projection.ProjectionType != "ALL" {
			return nil, validationErr("One or more parameter values were invalid: Select type ALL_ATTRIBUTES is not supported for global secondary index " + view.name + " because its projection type is not ALL")
		}
	case "SPECIFIC_ATTRIBUTES":
		if proj == nil {
			return nil, validationErr("SPECIFIC_ATTRIBUTES requires a ProjectionExpression or AttributesToGet")
		}
	default:
		return nil, validationErr("1 validation error detected: Value '" + req.Select + "' at 'select' failed to satisfy constraint: Member must satisfy enum value set: [SPECIFIC_ATTRIBUTES, COUNT, ALL_ATTRIBUTES, ALL_PROJECTED_ATTRIBUTES]")
	}
	if err := ctx.CheckUnused(); err != nil {
		return nil, err
	}

	if !isQuery && (req.Segment != nil || req.TotalSegments != nil) {
		if req.Segment == nil || req.TotalSegments == nil || *req.TotalSegments < 1 || *req.Segment < 0 || *req.Segment >= *req.TotalSegments {
			return nil, validationErr("The Segment parameter is required but was not present in the request when parameter TotalSegments is present")
		}
	}

	// Gather the candidate items, as the index stores them.
	tableHash, tableRange := t.keys()
	var rows []Item
	for _, it := range h.tableItems(ns, t.Name) {
		p := projectForIndex(t, view, it)
		if p == nil {
			continue
		}
		if keyCond != nil && !EvalCondition(keyCond, p) {
			continue
		}
		if req.TotalSegments != nil && *req.TotalSegments > 1 {
			f := fnv.New32a()
			f.Write([]byte(it[tableHash].canonical()))
			if int(f.Sum32()%uint32(*req.TotalSegments)) != *req.Segment {
				continue
			}
		}
		rows = append(rows, p)
	}

	// Order: hash, then range key of the index, then the table's own key
	// to break ties in a GSI.
	order := []string{view.hash, view.rng, tableHash, tableRange}
	cmp := func(a, b Item) int {
		for _, k := range order {
			if k == "" {
				continue
			}
			if c := compareKeyValues(a[k], b[k]); c != 0 {
				return c
			}
		}
		return 0
	}
	forward := req.ScanIndexForward == nil || *req.ScanIndexForward || !isQuery
	sort.SliceStable(rows, func(i, j int) bool {
		if forward {
			return cmp(rows[i], rows[j]) < 0
		}
		return cmp(rows[i], rows[j]) > 0
	})

	if req.ExclusiveStartKey != nil {
		start := 0
		for start < len(rows) {
			c := cmp(rows[start], req.ExclusiveStartKey)
			if forward && c > 0 || !forward && c < 0 {
				break
			}
			start++
		}
		rows = rows[start:]
	}

	var lastKey Item
	if req.Limit != nil && len(rows) >= *req.Limit {
		rows = rows[:*req.Limit]
		last := rows[len(rows)-1]
		lastKey = Item{}
		for _, k := range order {
			if k != "" && last[k] != nil {
				lastKey[k] = last[k]
			}
		}
	}

	items := []Item{}
	for _, it := range rows {
		if filter != nil && !EvalCondition(filter, it) {
			continue
		}
		if proj != nil {
			it = Project(it, proj)
		}
		items = append(items, it)
	}

	out := map[string]any{"Count": len(items), "ScannedCount": len(rows)}
	if req.Select != "COUNT" {
		out["Items"] = items
	}
	if lastKey != nil {
		out["LastEvaluatedKey"] = lastKey
	}
	return withConsumed(out, t.Name, req.ReturnConsumedCapacity, 0.5), nil
}

// compareKeyValues orders key attribute values; a missing value sorts first.
func compareKeyValues(a, b *AttributeValue) int {
	switch {
	case a == nil && b == nil:
		return 0
	case a == nil:
		return -1
	case b == nil:
		return 1
	}
	if c, ok := compareScalar(a, b); ok {
		return c
	}
	return strings.Compare(a.canonical(), b.canonical())
}

// validateKeyCondition checks that a key condition is an equality on the
// hash key, optionally ANDed with one supported condition on the range key.
func validateKeyCondition(c Condition, v *indexView) error {
	var parts []Condition
	var flatten func(Condition) error
	flatten = func(c Condition) error {
		switch x := c.(type) {
		case *LogicCond:
			if x.Op != "AND" {
				return validationErr("Invalid operator used in KeyConditionExpression: OR")
			}
			if err := flatten(x.L); err != nil {
				return err
			}
			return flatten(x.R)
		case *NotCond:
			return validationErr("Invalid operator used in KeyConditionExpression: NOT")
		}
		parts = append(parts, c)
		return nil
	}
	if err := flatten(c); err != nil {
		return err
	}
	if len(parts) > 2 {
		return validationErr("Conditions can be of length 1 or 2 only")
	}

	seen := map[string]bool{}
	for _, p := range parts {
		var attr string
		var op string
		switch x := p.(type) {
		case *CmpCond:
			op = x.Op
			path, lok := x.L.(*PathExpr)
			_, rval := x.R.(*ValueExpr)
			if !lok || !rval {
				if rp, ok := x.R.(*PathExpr); ok {
					if _, lval := x.L.(*ValueExpr); lval {
						path, lok = rp, true
						rval = true
					}
				}
			}
			if !lok || !rval || len(path.Elems) != 1 {
				return validationErr("Invalid KeyConditionExpression: The key conditions must be on key attributes and compare with a value")
			}
			if op == "<>" {
				return validationErr("Unsupported operator in KeyConditionExpression: <>")
			}
			attr = path.Elems[0].Name
		case *BetweenCond:
			op = "BETWEEN"
			path, ok := x.X.(*PathExpr)
			if !ok || len(path.Elems) != 1 {
				return validationErr("Invalid KeyConditionExpression: BETWEEN must be on a key attribute")
			}
			attr = path.Elems[0].Name
		case *FuncCond:
			if x.Name != "begins_with" {
				return validationErr("Invalid KeyConditionExpression: Invalid function name; function: " + x.Name)
			}
			op = "begins_with"
			attr = x.Args[0].(*PathExpr).Elems[0].Name
		default:
			return validationErr("Invalid KeyConditionExpression: Unsupported operator")
		}
		if attr != v.hash && attr != v.rng {
			return validationErr("Query condition missed key schema element: " + v.hash)
		}
		if seen[attr] {
			return validationErr("KeyConditionExpressions must only contain one condition per key")
		}
		seen[attr] = true
		if attr == v.hash && op != "=" {
			return validationErr("Query key condition not supported")
		}
	}
	if !seen[v.hash] {
		return validationErr("Query condition missed key schema element: " + v.hash)
	}
	return nil
}

// conditionPaths lists the document paths a condition refers to.
func conditionPaths(c Condition) []*PathExpr {
	var out []*PathExpr
	var ops func(...Operand)
	ops = func(list ...Operand) {
		for _, o := range list {
			switch x := o.(type) {
			case *PathExpr:
				out = append(out, x)
			case *FuncExpr:
				ops(x.Args...)
			case *ArithExpr:
				ops(x.L, x.R)
			}
		}
	}
	var walk func(Condition)
	walk = func(c Condition) {
		switch x := c.(type) {
		case *LogicCond:
			walk(x.L)
			walk(x.R)
		case *NotCond:
			walk(x.C)
		case *CmpCond:
			ops(x.L, x.R)
		case *BetweenCond:
			ops(x.X, x.Lo, x.Hi)
		case *InCond:
			ops(x.X)
			ops(x.List...)
		case *FuncCond:
			ops(x.Args...)
		}
	}
	walk(c)
	return out
}

// ── Batch operations ─────────────────────────────────────────────────────

func (h *Handler) batchWriteItem(r *http.Request, body []byte) (any, error) {
	ns := util.NamespaceFromHeader(r)
	var req struct {
		RequestItems map[string][]struct {
			PutRequest    *struct{ Item Item }
			DeleteRequest *struct{ Key Item }
		}
		ReturnConsumedCapacity string
	}
	if err := decode(body, &req); err != nil {
		return nil, err
	}
	total := 0
	for _, reqs := range req.RequestItems {
		total += len(reqs)
	}
	if total == 0 {
		return nil, validationErr("1 validation error detected: Value at 'requestItems' failed to satisfy constraint: Member must have length greater than or equal to 1")
	}
	if total > 25 {
		return nil, validationErr("Too many items requested for the BatchWriteItem call")
	}

	var writes []*preparedWrite
	var capacity []map[string]any
	for _, table := range sortedKeys(req.RequestItems) {
		t, _, err := h.loadTable(ns, table)
		if err != nil {
			return nil, err
		}
		seen := map[string]bool{}
		for _, wr := range req.RequestItems[table] {
			var w *preparedWrite
			switch {
			case wr.PutRequest != nil && wr.DeleteRequest == nil:
				if err := validateItem(t, wr.PutRequest.Item); err != nil {
					return nil, err
				}
				w = &preparedWrite{t: t, key: keyOf(t, wr.PutRequest.Item), newItem: wr.PutRequest.Item}
			case wr.DeleteRequest != nil && wr.PutRequest == nil:
				key, err := extractKey(t, wr.DeleteRequest.Key)
				if err != nil {
					return nil, err
				}
				w = &preparedWrite{t: t, key: key}
			default:
				return nil, validationErr("Supplied AttributeValue has more than one datatypes set, must contain exactly one of the supported datatypes")
			}
			id := itemID(t, w.key)
			if seen[id] {
				return nil, validationErr("Provided list of item keys contains duplicates")
			}
			seen[id] = true
			writes = append(writes, w)
		}
		if c := consumed(table, req.ReturnConsumedCapacity, float64(len(req.RequestItems[table]))); c != nil {
			capacity = append(capacity, c)
		}
	}
	for _, w := range writes {
		old, err := h.loadItem(ns, w.t, w.key)
		if err != nil {
			return nil, err
		}
		w.old = old
		if err := h.apply(ns, w); err != nil {
			return nil, err
		}
	}
	out := map[string]any{"UnprocessedItems": map[string]any{}}
	if capacity != nil {
		out["ConsumedCapacity"] = capacity
	}
	return out, nil
}

func (h *Handler) batchGetItem(r *http.Request, body []byte) (any, error) {
	ns := util.NamespaceFromHeader(r)
	var req struct {
		RequestItems map[string]struct {
			exprInput
			Keys                 []Item
			ProjectionExpression string
			AttributesToGet      []string
			ConsistentRead       bool
		}
		ReturnConsumedCapacity string
	}
	if err := decode(body, &req); err != nil {
		return nil, err
	}
	total := 0
	for _, ka := range req.RequestItems {
		total += len(ka.Keys)
	}
	if total == 0 {
		return nil, validationErr("1 validation error detected: Value at 'requestItems' failed to satisfy constraint: Member must have length greater than or equal to 1")
	}
	if total > 100 {
		return nil, validationErr("Too many items requested for the BatchGetItem call")
	}
	responses := map[string][]Item{}
	var capacity []map[string]any
	for _, table := range sortedKeys(req.RequestItems) {
		ka := req.RequestItems[table]
		responses[table] = []Item{}
		seen := map[string]bool{}
		for _, key := range ka.Keys {
			g := getInput{exprInput: ka.exprInput, TableName: table, Key: key, ProjectionExpression: ka.ProjectionExpression, AttributesToGet: ka.AttributesToGet}
			t, it, err := h.readItem(ns, &g)
			if err != nil {
				return nil, err
			}
			id := itemID(t, key)
			if seen[id] {
				return nil, validationErr("Provided list of item keys contains duplicates")
			}
			seen[id] = true
			if it != nil {
				responses[table] = append(responses[table], it)
			}
		}
		if c := consumed(table, req.ReturnConsumedCapacity, float64(len(ka.Keys))/2); c != nil {
			capacity = append(capacity, c)
		}
	}
	out := map[string]any{"Responses": responses, "UnprocessedKeys": map[string]any{}}
	if capacity != nil {
		out["ConsumedCapacity"] = capacity
	}
	return out, nil
}

// ── Transactions ─────────────────────────────────────────────────────────

type cancellationReason struct {
	Code    string `json:"Code"`
	Message string `json:"Message,omitempty"`
	Item    Item   `json:"Item,omitempty"`
}

func (h *Handler) transactWriteItems(r *http.Request, body []byte) (any, error) {
	ns := util.NamespaceFromHeader(r)
	var req struct {
		TransactItems []struct {
			ConditionCheck *struct {
				exprInput
				TableName                           string
				Key                                 Item
				ConditionExpression                 string
				ReturnValuesOnConditionCheckFailure string
			}
			Put    *putInput
			Delete *deleteInput
			Update *updateInput
		}
		ReturnConsumedCapacity string
		ClientRequestToken     string
	}
	if err := decode(body, &req); err != nil {
		return nil, err
	}
	if len(req.TransactItems) == 0 || len(req.TransactItems) > 100 {
		return nil, validationErr("1 validation error detected: Value at 'transactItems' failed to satisfy constraint: Member must have length less than or equal to 100 and greater than or equal to 1")
	}

	writes := make([]*preparedWrite, len(req.TransactItems))
	seen := map[string]bool{}
	for i, ti := range req.TransactItems {
		var w *preparedWrite
		var err error
		n := 0
		if c := ti.ConditionCheck; c != nil {
			n++
			w, err = h.prepareConditionCheck(ns, c.TableName, c.Key, c.exprInput, c.ConditionExpression, c.ReturnValuesOnConditionCheckFailure)
		}
		if ti.Put != nil {
			n++
			if ti.Put.ReturnValues != "" && ti.Put.ReturnValues != "NONE" {
				return nil, validationErr("ReturnValues is not supported in TransactWriteItems")
			}
			w, err = h.preparePut(ns, ti.Put)
		}
		if ti.Delete != nil {
			n++
			w, err = h.prepareDelete(ns, ti.Delete)
		}
		if ti.Update != nil {
			n++
			w, err = h.prepareUpdate(ns, ti.Update)
		}
		if n != 1 {
			return nil, validationErr("TransactItems can only contain one of Check, Put, Update or Delete")
		}
		if err != nil {
			return nil, err
		}
		id := w.t.Name + "|" + itemID(w.t, w.key)
		if seen[id] {
			return nil, validationErr("Transaction request cannot include multiple operations on one item")
		}
		seen[id] = true
		writes[i] = w
	}

	reasons := make([]cancellationReason, len(writes))
	failed := false
	var codes []string
	for i, w := range writes {
		reasons[i] = cancellationReason{Code: "None"}
		if w.cond != nil && !EvalCondition(w.cond, w.old) {
			failed = true
			reasons[i] = cancellationReason{Code: "ConditionalCheckFailed", Message: "The conditional request failed"}
			if w.retFail == "ALL_OLD" && w.old != nil {
				reasons[i].Item = w.old
			}
		}
		codes = append(codes, reasons[i].Code)
	}
	if failed {
		e := newErr("TransactionCanceledException", "Transaction cancelled, please refer cancellation reasons for specific reasons ["+strings.Join(codes, ", ")+"]")
		e.Extra = map[string]any{"CancellationReasons": reasons}
		return nil, e
	}
	for _, w := range writes {
		if w.checkOnly {
			continue
		}
		if err := h.apply(ns, w); err != nil {
			return nil, err
		}
	}
	return map[string]any{}, nil
}

// prepareConditionCheck builds a write that only checks its condition.
func (h *Handler) prepareConditionCheck(ns, table string, key Item, ei exprInput, expr, retFail string) (*preparedWrite, error) {
	if expr == "" {
		return nil, validationErr("1 validation error detected: Value null at 'transactItems.1.member.conditionCheck.conditionExpression' failed to satisfy constraint: Member must not be null")
	}
	d := deleteInput{exprInput: ei, TableName: table, Key: key, ConditionExpression: expr, ReturnValuesOnConditionCheckFailure: retFail}
	w, err := h.prepareDelete(ns, &d)
	if err != nil {
		return nil, err
	}
	w.checkOnly = true
	return w, nil
}

func (h *Handler) transactGetItems(r *http.Request, body []byte) (any, error) {
	ns := util.NamespaceFromHeader(r)
	var req struct {
		TransactItems []struct {
			Get *getInput
		}
	}
	if err := decode(body, &req); err != nil {
		return nil, err
	}
	if len(req.TransactItems) == 0 || len(req.TransactItems) > 100 {
		return nil, validationErr("1 validation error detected: Value at 'transactItems' failed to satisfy constraint: Member must have length less than or equal to 100 and greater than or equal to 1")
	}
	var responses []map[string]any
	for _, ti := range req.TransactItems {
		if ti.Get == nil {
			return nil, validationErr("TransactItems must contain Get")
		}
		_, it, err := h.readItem(ns, ti.Get)
		if err != nil {
			return nil, err
		}
		resp := map[string]any{}
		if it != nil {
			resp["Item"] = it
		}
		responses = append(responses, resp)
	}
	return map[string]any{"Responses": responses}, nil
}
