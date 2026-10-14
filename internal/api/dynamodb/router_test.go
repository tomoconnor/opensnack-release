// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package dynamodb_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"opensnack/internal/resource"
	"opensnack/internal/router"
)

type client struct {
	t     *testing.T
	h     http.Handler
	ns    string
	store *resource.MemStore
}

func newClient(t *testing.T) *client {
	store := resource.NewMemStore()
	return &client{t: t, h: router.New(store), ns: "ns1", store: store}
}

// call sends one DynamoDB request through the full router and returns the
// status and decoded body.
func (c *client) call(op, body string) (int, map[string]any) {
	c.t.Helper()
	req := httptest.NewRequest("POST", "/", strings.NewReader(body))
	req.Host = "dynamodb.us-east-1.amazonaws.com"
	req.Header.Set("X-Amz-Target", "DynamoDB_20120810."+op)
	req.Header.Set("Content-Type", "application/x-amz-json-1.0")
	req.Header.Set("User-Agent", "aws-sdk-go-v2 custom-"+c.ns)
	rec := httptest.NewRecorder()
	c.h.ServeHTTP(rec, req)
	var out map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		c.t.Fatalf("%s: bad JSON %q: %v", op, rec.Body.String(), err)
	}
	return rec.Code, out
}

func (c *client) ok(op, body string) map[string]any {
	c.t.Helper()
	code, out := c.call(op, body)
	if code != 200 {
		c.t.Fatalf("%s: status %d: %v", op, code, out)
	}
	return out
}

func (c *client) fails(op, body, wantType string) map[string]any {
	c.t.Helper()
	code, out := c.call(op, body)
	typ, _ := out["__type"].(string)
	if code != 400 || !strings.HasSuffix(typ, "#"+wantType) {
		c.t.Fatalf("%s: want %s, got %d %v", op, wantType, code, out)
	}
	return out
}

func js(v any) string {
	b, _ := json.Marshal(v)
	return string(b)
}

const ordersTable = `{
	"TableName": "orders",
	"BillingMode": "PAY_PER_REQUEST",
	"AttributeDefinitions": [
		{"AttributeName": "customer", "AttributeType": "S"},
		{"AttributeName": "order_id", "AttributeType": "S"},
		{"AttributeName": "status", "AttributeType": "S"},
		{"AttributeName": "total", "AttributeType": "N"}
	],
	"KeySchema": [
		{"AttributeName": "customer", "KeyType": "HASH"},
		{"AttributeName": "order_id", "KeyType": "RANGE"}
	],
	"GlobalSecondaryIndexes": [{
		"IndexName": "by_status",
		"KeySchema": [
			{"AttributeName": "status", "KeyType": "HASH"},
			{"AttributeName": "total", "KeyType": "RANGE"}
		],
		"Projection": {"ProjectionType": "INCLUDE", "NonKeyAttributes": ["note"]}
	}],
	"LocalSecondaryIndexes": [{
		"IndexName": "by_total",
		"KeySchema": [
			{"AttributeName": "customer", "KeyType": "HASH"},
			{"AttributeName": "total", "KeyType": "RANGE"}
		],
		"Projection": {"ProjectionType": "KEYS_ONLY"}
	}]
}`

func putOrder(c *client, customer, id, status, total string) {
	c.t.Helper()
	c.ok("PutItem", `{"TableName":"orders","Item":{
		"customer":{"S":"`+customer+`"},"order_id":{"S":"`+id+`"},
		"status":{"S":"`+status+`"},"total":{"N":"`+total+`"},
		"note":{"S":"note-`+id+`"},"secret":{"S":"hidden"}}}`)
}

func TestConditionalPutFails(t *testing.T) {
	c := newClient(t)
	c.ok("CreateTable", ordersTable)
	putOrder(c, "ann", "o1", "NEW", "10")

	cond := `{"TableName":"orders","Item":{"customer":{"S":"ann"},"order_id":{"S":"o1"}},
		"ConditionExpression":"attribute_not_exists(customer)",
		"ReturnValuesOnConditionCheckFailure":"ALL_OLD"}`
	out := c.fails("PutItem", cond, "ConditionalCheckFailedException")
	if out["message"] != "The conditional request failed" {
		t.Fatalf("message: %v", out["message"])
	}
	if item, _ := out["Item"].(map[string]any); item["status"] == nil {
		t.Fatalf("ALL_OLD on failure should return the item: %v", out)
	}

	// The failed write changed nothing.
	got := c.ok("GetItem", `{"TableName":"orders","Key":{"customer":{"S":"ann"},"order_id":{"S":"o1"}}}`)
	if js(got["Item"].(map[string]any)["status"]) != `{"S":"NEW"}` {
		t.Fatalf("item changed: %v", got)
	}

	// A comparison condition that holds lets the write through, and
	// ReturnValues ALL_OLD hands back the previous item.
	out = c.ok("PutItem", `{"TableName":"orders","Item":{"customer":{"S":"ann"},"order_id":{"S":"o1"},"status":{"S":"PAID"},"total":{"N":"10"}},
		"ConditionExpression":"#s = :new AND total < :max",
		"ExpressionAttributeNames":{"#s":"status"},
		"ExpressionAttributeValues":{":new":{"S":"NEW"},":max":{"N":"11"}},
		"ReturnValues":"ALL_OLD"}`)
	if js(out["Attributes"].(map[string]any)["status"]) != `{"S":"NEW"}` {
		t.Fatalf("ALL_OLD: %v", out)
	}

	// Conditional delete.
	c.fails("DeleteItem", `{"TableName":"orders","Key":{"customer":{"S":"ann"},"order_id":{"S":"o1"}},
		"ConditionExpression":"#s = :new","ExpressionAttributeNames":{"#s":"status"},
		"ExpressionAttributeValues":{":new":{"S":"NEW"}}}`, "ConditionalCheckFailedException")
	c.ok("DeleteItem", `{"TableName":"orders","Key":{"customer":{"S":"ann"},"order_id":{"S":"o1"}},
		"ConditionExpression":"#s = :paid","ExpressionAttributeNames":{"#s":"status"},
		"ExpressionAttributeValues":{":paid":{"S":"PAID"}}}`)

	// Unused placeholders are rejected, as in AWS.
	c.fails("PutItem", `{"TableName":"orders","Item":{"customer":{"S":"x"},"order_id":{"S":"y"}},
		"ExpressionAttributeValues":{":unused":{"S":"x"}}}`, "ValidationException")
}

func TestTerraformLockPattern(t *testing.T) {
	c := newClient(t)
	c.ok("CreateTable", `{"TableName":"tf-locks","BillingMode":"PAY_PER_REQUEST",
		"AttributeDefinitions":[{"AttributeName":"LockID","AttributeType":"S"}],
		"KeySchema":[{"AttributeName":"LockID","KeyType":"HASH"}]}`)

	lock := `{"TableName":"tf-locks","Item":{"LockID":{"S":"state-bucket/prod/terraform.tfstate"},"Info":{"S":"{\"ID\":\"one\"}"}},
		"ConditionExpression":"attribute_not_exists(LockID)"}`
	c.ok("PutItem", lock)
	// A second holder can't take the lock...
	c.fails("PutItem", strings.Replace(lock, "one", "two", 1), "ConditionalCheckFailedException")

	// ...and reads who holds it.
	got := c.ok("GetItem", `{"TableName":"tf-locks","Key":{"LockID":{"S":"state-bucket/prod/terraform.tfstate"}},
		"ProjectionExpression":"LockID, Info","ConsistentRead":true}`)
	if !strings.Contains(js(got), `\"ID\":\"one\"`) {
		t.Fatalf("lock info: %v", got)
	}

	// Unlock, then the other holder succeeds.
	c.ok("DeleteItem", `{"TableName":"tf-locks","Key":{"LockID":{"S":"state-bucket/prod/terraform.tfstate"}}}`)
	c.ok("PutItem", strings.Replace(lock, "one", "two", 1))

	// The digest item lives alongside the lock and is overwritten freely.
	c.ok("PutItem", `{"TableName":"tf-locks","Item":{"LockID":{"S":"state-bucket/prod/terraform.tfstate-md5"},"Digest":{"S":"abc"}}}`)
	c.ok("PutItem", `{"TableName":"tf-locks","Item":{"LockID":{"S":"state-bucket/prod/terraform.tfstate-md5"},"Digest":{"S":"def"}}}`)
	got = c.ok("GetItem", `{"TableName":"tf-locks","Key":{"LockID":{"S":"state-bucket/prod/terraform.tfstate-md5"}},"ProjectionExpression":"LockID, Digest"}`)
	if js(got["Item"].(map[string]any)["Digest"]) != `{"S":"def"}` {
		t.Fatalf("digest: %v", got)
	}
}

func TestUpdateItemExpression(t *testing.T) {
	c := newClient(t)
	c.ok("CreateTable", ordersTable)
	putOrder(c, "bob", "o1", "NEW", "5")

	out := c.ok("UpdateItem", `{"TableName":"orders","Key":{"customer":{"S":"bob"},"order_id":{"S":"o1"}},
		"UpdateExpression":"SET total = total + :inc, visits = if_not_exists(visits, :zero) + :one, history = list_append(if_not_exists(history, :empty), :h) REMOVE secret ADD labels :l",
		"ConditionExpression":"attribute_exists(customer) AND #s IN (:new, :paid)",
		"ExpressionAttributeNames":{"#s":"status"},
		"ExpressionAttributeValues":{":inc":{"N":"2.5"},":zero":{"N":"0"},":one":{"N":"1"},":empty":{"L":[]},":h":{"L":[{"S":"created"}]},":l":{"SS":["asset"]},":new":{"S":"NEW"},":paid":{"S":"PAID"}},
		"ReturnValues":"UPDATED_NEW"}`)
	attrs := out["Attributes"].(map[string]any)
	if js(attrs["total"]) != `{"N":"7.5"}` || js(attrs["visits"]) != `{"N":"1"}` ||
		js(attrs["history"]) != `{"L":[{"S":"created"}]}` || js(attrs["labels"]) != `{"SS":["asset"]}` {
		t.Fatalf("UPDATED_NEW: %v", attrs)
	}
	if _, ok := attrs["status"]; ok {
		t.Fatal("UPDATED_NEW must only return updated attributes")
	}

	out = c.ok("UpdateItem", `{"TableName":"orders","Key":{"customer":{"S":"bob"},"order_id":{"S":"o1"}},
		"UpdateExpression":"DELETE labels :l SET #s = :paid",
		"ExpressionAttributeNames":{"#s":"status"},
		"ExpressionAttributeValues":{":l":{"SS":["asset"]},":paid":{"S":"PAID"}},
		"ReturnValues":"ALL_NEW"}`)
	attrs = out["Attributes"].(map[string]any)
	if _, ok := attrs["labels"]; ok {
		t.Fatalf("deleting the last set member should remove the attribute: %v", attrs)
	}
	if _, ok := attrs["secret"]; ok {
		t.Fatalf("REMOVE did not stick: %v", attrs)
	}
	if js(attrs["status"]) != `{"S":"PAID"}` {
		t.Fatalf("ALL_NEW: %v", attrs)
	}

	// Upsert: UpdateItem on a missing key creates the item.
	c.ok("UpdateItem", `{"TableName":"orders","Key":{"customer":{"S":"bob"},"order_id":{"S":"o2"}},
		"UpdateExpression":"SET note = :n","ExpressionAttributeValues":{":n":{"S":"fresh"}}}`)
	got := c.ok("GetItem", `{"TableName":"orders","Key":{"customer":{"S":"bob"},"order_id":{"S":"o2"}}}`)
	if js(got["Item"]) != `{"customer":{"S":"bob"},"note":{"S":"fresh"},"order_id":{"S":"o2"}}` {
		t.Fatalf("upsert: %v", got)
	}

	// Failed conditions, key updates and type errors are rejected.
	c.fails("UpdateItem", `{"TableName":"orders","Key":{"customer":{"S":"bob"},"order_id":{"S":"o1"}},
		"UpdateExpression":"SET note = :n","ConditionExpression":"total > :big",
		"ExpressionAttributeValues":{":n":{"S":"x"},":big":{"N":"100"}}}`, "ConditionalCheckFailedException")
	c.fails("UpdateItem", `{"TableName":"orders","Key":{"customer":{"S":"bob"},"order_id":{"S":"o1"}},
		"UpdateExpression":"SET order_id = :n","ExpressionAttributeValues":{":n":{"S":"x"}}}`, "ValidationException")
	c.fails("UpdateItem", `{"TableName":"orders","Key":{"customer":{"S":"bob"},"order_id":{"S":"o1"}},
		"UpdateExpression":"SET total = :n","ExpressionAttributeValues":{":n":{"S":"not a number"}}}`, "ValidationException")

	// The legacy AttributeUpdates form, which aws_dynamodb_table_item uses.
	c.ok("UpdateItem", `{"TableName":"orders","Key":{"customer":{"S":"bob"},"order_id":{"S":"o2"}},
		"AttributeUpdates":{"note":{"Action":"DELETE"},"extra":{"Action":"PUT","Value":{"N":"1"}}}}`)
	got = c.ok("GetItem", `{"TableName":"orders","Key":{"customer":{"S":"bob"},"order_id":{"S":"o2"}}}`)
	if js(got["Item"]) != `{"customer":{"S":"bob"},"extra":{"N":"1"},"order_id":{"S":"o2"}}` {
		t.Fatalf("AttributeUpdates: %v", got)
	}
}

func TestQueryIndexes(t *testing.T) {
	c := newClient(t)
	c.ok("CreateTable", ordersTable)
	putOrder(c, "ann", "o1", "NEW", "30")
	putOrder(c, "ann", "o2", "PAID", "10")
	putOrder(c, "ann", "o3", "NEW", "20")
	putOrder(c, "bob", "o4", "NEW", "25")
	putOrder(c, "bob", "o5", "SHIPPED", "5")
	// Not in the GSI: no status.
	c.ok("PutItem", `{"TableName":"orders","Item":{"customer":{"S":"cat"},"order_id":{"S":"o6"},"total":{"N":"1"}}}`)

	ids := func(out map[string]any, attr string) string {
		var s []string
		for _, it := range out["Items"].([]any) {
			m := it.(map[string]any)
			v, _ := m[attr].(map[string]any)
			for _, x := range v {
				s = append(s, x.(string))
			}
		}
		return strings.Join(s, ",")
	}

	// GSI query, sorted by the index range key, with INCLUDE projection.
	out := c.ok("Query", `{"TableName":"orders","IndexName":"by_status",
		"KeyConditionExpression":"#s = :new AND total BETWEEN :lo AND :hi",
		"ExpressionAttributeNames":{"#s":"status"},
		"ExpressionAttributeValues":{":new":{"S":"NEW"},":lo":{"N":"20"},":hi":{"N":"30"}}}`)
	if got := ids(out, "order_id"); got != "o3,o4,o1" {
		t.Fatalf("GSI order: %s", got)
	}
	first := out["Items"].([]any)[0].(map[string]any)
	if first["note"] == nil || first["secret"] != nil {
		t.Fatalf("INCLUDE projection wrong: %v", first)
	}

	// Descending, with a filter and a projection.
	out = c.ok("Query", `{"TableName":"orders","IndexName":"by_status",
		"KeyConditionExpression":"#s = :new","ScanIndexForward":false,
		"FilterExpression":"customer = :ann","ProjectionExpression":"order_id, total",
		"ExpressionAttributeNames":{"#s":"status"},
		"ExpressionAttributeValues":{":new":{"S":"NEW"},":ann":{"S":"ann"}}}`)
	if got := ids(out, "order_id"); got != "o1,o3" {
		t.Fatalf("descending filtered: %s", got)
	}
	if out["ScannedCount"].(float64) != 3 || out["Count"].(float64) != 2 {
		t.Fatalf("counts: %v", out)
	}

	// Pagination on the GSI with Limit and ExclusiveStartKey.
	var pages []string
	start := ""
	for i := 0; i < 5; i++ {
		body := `{"TableName":"orders","IndexName":"by_status","KeyConditionExpression":"#s = :new","Limit":2,
			"ExpressionAttributeNames":{"#s":"status"},"ExpressionAttributeValues":{":new":{"S":"NEW"}}` + start + `}`
		out = c.ok("Query", body)
		pages = append(pages, ids(out, "order_id"))
		lek, ok := out["LastEvaluatedKey"]
		if !ok {
			break
		}
		start = `,"ExclusiveStartKey":` + js(lek)
	}
	if got := strings.Join(pages, "|"); got != "o3,o4|o1|" && got != "o3,o4|o1" {
		t.Fatalf("pages: %s", got)
	}

	// LSI query with begins_with on the base table and KEYS_ONLY on the LSI.
	out = c.ok("Query", `{"TableName":"orders","IndexName":"by_total",
		"KeyConditionExpression":"customer = :ann AND total > :n",
		"ExpressionAttributeValues":{":ann":{"S":"ann"},":n":{"N":"15"}}}`)
	if got := ids(out, "order_id"); got != "o3,o1" {
		t.Fatalf("LSI: %s", got)
	}
	if out["Items"].([]any)[0].(map[string]any)["note"] != nil {
		t.Fatal("KEYS_ONLY must not project note")
	}
	out = c.ok("Query", `{"TableName":"orders",
		"KeyConditionExpression":"customer = :ann AND begins_with(order_id, :o)",
		"ExpressionAttributeValues":{":ann":{"S":"ann"},":o":{"S":"o"}},"Select":"COUNT"}`)
	if out["Count"].(float64) != 3 || out["Items"] != nil {
		t.Fatalf("COUNT: %v", out)
	}

	// Bad key conditions.
	c.fails("Query", `{"TableName":"orders","KeyConditionExpression":"order_id = :o",
		"ExpressionAttributeValues":{":o":{"S":"o1"}}}`, "ValidationException")
	c.fails("Query", `{"TableName":"orders","IndexName":"nope","KeyConditionExpression":"customer = :o",
		"ExpressionAttributeValues":{":o":{"S":"o1"}}}`, "ValidationException")

	// Scan with a filter.
	out = c.ok("Scan", `{"TableName":"orders","FilterExpression":"total >= :n","ExpressionAttributeValues":{":n":{"N":"25"}}}`)
	if out["Count"].(float64) != 2 || out["ScannedCount"].(float64) != 6 {
		t.Fatalf("scan: %v", out)
	}
}

func TestBatchAndTransactions(t *testing.T) {
	c := newClient(t)
	c.ok("CreateTable", ordersTable)
	putOrder(c, "ann", "gone", "NEW", "1")

	out := c.ok("BatchWriteItem", `{"RequestItems":{"orders":[
		{"PutRequest":{"Item":{"customer":{"S":"ann"},"order_id":{"S":"b1"},"total":{"N":"1"}}}},
		{"PutRequest":{"Item":{"customer":{"S":"ann"},"order_id":{"S":"b2"},"total":{"N":"2"}}}},
		{"DeleteRequest":{"Key":{"customer":{"S":"ann"},"order_id":{"S":"gone"}}}}
	]}}`)
	if js(out["UnprocessedItems"]) != `{}` {
		t.Fatalf("unprocessed: %v", out)
	}
	out = c.ok("BatchGetItem", `{"RequestItems":{"orders":{"Keys":[
		{"customer":{"S":"ann"},"order_id":{"S":"b1"}},
		{"customer":{"S":"ann"},"order_id":{"S":"b2"}},
		{"customer":{"S":"ann"},"order_id":{"S":"gone"}}
	],"ProjectionExpression":"order_id"}}}`)
	if n := len(out["Responses"].(map[string]any)["orders"].([]any)); n != 2 {
		t.Fatalf("BatchGetItem returned %d items: %v", n, out)
	}

	// Duplicate keys and wrong key types are rejected.
	c.fails("BatchWriteItem", `{"RequestItems":{"orders":[
		{"PutRequest":{"Item":{"customer":{"S":"ann"},"order_id":{"S":"b1"}}}},
		{"DeleteRequest":{"Key":{"customer":{"S":"ann"},"order_id":{"S":"b1"}}}}
	]}}`, "ValidationException")
	c.fails("BatchWriteItem", `{"RequestItems":{"orders":[
		{"PutRequest":{"Item":{"customer":{"N":"1"},"order_id":{"S":"b1"}}}}
	]}}`, "ValidationException")

	// A transaction is all or nothing.
	out = c.fails("TransactWriteItems", `{"TransactItems":[
		{"Put":{"TableName":"orders","Item":{"customer":{"S":"ann"},"order_id":{"S":"t1"}}}},
		{"ConditionCheck":{"TableName":"orders","Key":{"customer":{"S":"ann"},"order_id":{"S":"b1"}},
			"ConditionExpression":"total > :n","ExpressionAttributeValues":{":n":{"N":"5"}}}}
	]}`, "TransactionCanceledException")
	reasons := out["CancellationReasons"].([]any)
	if len(reasons) != 2 || reasons[0].(map[string]any)["Code"] != "None" || reasons[1].(map[string]any)["Code"] != "ConditionalCheckFailed" {
		t.Fatalf("reasons: %v", reasons)
	}
	got := c.ok("GetItem", `{"TableName":"orders","Key":{"customer":{"S":"ann"},"order_id":{"S":"t1"}}}`)
	if _, ok := got["Item"]; ok {
		t.Fatal("a cancelled transaction wrote an item")
	}
	c.ok("TransactWriteItems", `{"TransactItems":[
		{"Put":{"TableName":"orders","Item":{"customer":{"S":"ann"},"order_id":{"S":"t1"}}}},
		{"Update":{"TableName":"orders","Key":{"customer":{"S":"ann"},"order_id":{"S":"b1"}},
			"UpdateExpression":"SET total = total + :n","ExpressionAttributeValues":{":n":{"N":"5"}}}},
		{"Delete":{"TableName":"orders","Key":{"customer":{"S":"ann"},"order_id":{"S":"b2"}}}}
	]}`)
	out = c.ok("TransactGetItems", `{"TransactItems":[
		{"Get":{"TableName":"orders","Key":{"customer":{"S":"ann"},"order_id":{"S":"b1"}}}},
		{"Get":{"TableName":"orders","Key":{"customer":{"S":"ann"},"order_id":{"S":"b2"}}}}
	]}`)
	resp := out["Responses"].([]any)
	if js(resp[0].(map[string]any)["Item"].(map[string]any)["total"]) != `{"N":"6"}` || len(resp[1].(map[string]any)) != 0 {
		t.Fatalf("TransactGetItems: %v", resp)
	}
}

func TestItemsScopedAndDeletedWithTable(t *testing.T) {
	c := newClient(t)
	c.ok("CreateTable", ordersTable)
	putOrder(c, "ann", "o1", "NEW", "1")

	// The same table name in another namespace is a different table.
	other := &client{t: t, h: c.h, ns: "ns2", store: c.store}
	other.ok("CreateTable", ordersTable)
	got := other.ok("GetItem", `{"TableName":"orders","Key":{"customer":{"S":"ann"},"order_id":{"S":"o1"}}}`)
	if _, ok := got["Item"]; ok {
		t.Fatal("item leaked across namespaces")
	}
	desc := c.ok("DescribeTable", `{"TableName":"orders"}`)["Table"].(map[string]any)
	if desc["ItemCount"].(float64) != 1 {
		t.Fatalf("ItemCount: %v", desc["ItemCount"])
	}

	items, _ := c.store.Find(resource.Filter{Namespace: "ns1", Service: "dynamodb", Type: "item"})
	if len(items) != 1 || items[0].Region != "us-east-1" {
		t.Fatalf("stored items: %+v", items)
	}
	c.ok("DeleteTable", `{"TableName":"orders"}`)
	items, _ = c.store.Find(resource.Filter{Namespace: "ns1", Service: "dynamodb"})
	if len(items) != 0 {
		t.Fatalf("items survived DeleteTable: %+v", items)
	}
	// Recreating the table starts empty.
	c.ok("CreateTable", ordersTable)
	got = c.ok("GetItem", `{"TableName":"orders","Key":{"customer":{"S":"ann"},"order_id":{"S":"o1"}}}`)
	if _, ok := got["Item"]; ok {
		t.Fatal("item came back after the table was recreated")
	}
}

func TestTableControlPlane(t *testing.T) {
	c := newClient(t)
	c.ok("CreateTable", ordersTable)
	c.fails("CreateTable", ordersTable, "ResourceInUseException")

	out := c.ok("UpdateTable", `{"TableName":"orders",
		"AttributeDefinitions":[{"AttributeName":"region","AttributeType":"S"}],
		"GlobalSecondaryIndexUpdates":[{"Create":{"IndexName":"by_region",
			"KeySchema":[{"AttributeName":"region","KeyType":"HASH"}],
			"Projection":{"ProjectionType":"ALL"}}}]}`)
	gsis := out["TableDescription"].(map[string]any)["GlobalSecondaryIndexes"].([]any)
	if len(gsis) != 2 || gsis[1].(map[string]any)["IndexStatus"] != "ACTIVE" {
		t.Fatalf("GSI create: %v", gsis)
	}
	out = c.ok("UpdateTable", `{"TableName":"orders","GlobalSecondaryIndexUpdates":[{"Delete":{"IndexName":"by_status"}}]}`)
	td := out["TableDescription"].(map[string]any)
	if strings.Contains(js(td["AttributeDefinitions"]), `"status"`) {
		t.Fatalf("attribute definition of a deleted index should go: %v", td["AttributeDefinitions"])
	}

	// TTL, PITR and tags round-trip.
	c.ok("UpdateTimeToLive", `{"TableName":"orders","TimeToLiveSpecification":{"AttributeName":"expires","Enabled":true}}`)
	c.fails("UpdateTimeToLive", `{"TableName":"orders","TimeToLiveSpecification":{"AttributeName":"expires","Enabled":true}}`, "ValidationException")
	ttl := c.ok("DescribeTimeToLive", `{"TableName":"orders"}`)
	if js(ttl) != `{"TimeToLiveDescription":{"AttributeName":"expires","TimeToLiveStatus":"ENABLED"}}` {
		t.Fatalf("TTL: %v", ttl)
	}
	c.ok("UpdateContinuousBackups", `{"TableName":"orders","PointInTimeRecoverySpecification":{"PointInTimeRecoveryEnabled":true}}`)
	pitr := c.ok("DescribeContinuousBackups", `{"TableName":"orders"}`)["ContinuousBackupsDescription"].(map[string]any)["PointInTimeRecoveryDescription"].(map[string]any)
	if pitr["PointInTimeRecoveryStatus"] != "ENABLED" || pitr["RecoveryPeriodInDays"].(float64) != 35 {
		t.Fatalf("PITR: %v", pitr)
	}
	arn := td["TableArn"].(string)
	if arn != "arn:aws:dynamodb:us-east-1:000000000000:table/orders" {
		t.Fatalf("arn %s", arn)
	}
	c.ok("TagResource", `{"ResourceArn":"`+arn+`","Tags":[{"Key":"a","Value":"1"},{"Key":"b","Value":"2"}]}`)
	c.ok("UntagResource", `{"ResourceArn":"`+arn+`","TagKeys":["a"]}`)
	tags := c.ok("ListTagsOfResource", `{"ResourceArn":"`+arn+`"}`)
	if js(tags["Tags"]) != `[{"Key":"b","Value":"2"}]` {
		t.Fatalf("tags: %v", tags)
	}

	c.ok("UpdateTable", `{"TableName":"orders","DeletionProtectionEnabled":true}`)
	c.fails("DeleteTable", `{"TableName":"orders"}`, "ValidationException")
	c.ok("UpdateTable", `{"TableName":"orders","DeletionProtectionEnabled":false}`)
	c.ok("DeleteTable", `{"TableName":"orders"}`)
	c.fails("DescribeTable", `{"TableName":"orders"}`, "ResourceNotFoundException")
	c.fails("DescribeContinuousBackups", `{"TableName":"orders"}`, "TableNotFoundException")
}
