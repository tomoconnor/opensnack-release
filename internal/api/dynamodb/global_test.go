// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package dynamodb_test

import (
	"net/http/httptest"
	"strings"
	"testing"
)

// in returns a client for the same namespace and store in another region.
func (c *client) in(region string) *client {
	cp := *c
	cp.region = region
	return &cp
}

const routeTable = `{
	"TableName": "route",
	"BillingMode": "PAY_PER_REQUEST",
	"AttributeDefinitions": [{"AttributeName": "stop", "AttributeType": "S"}],
	"KeySchema": [{"AttributeName": "stop", "KeyType": "HASH"}]
}`

func replicaRegions(desc map[string]any) []string {
	var out []string
	reps, _ := desc["Replicas"].([]any)
	for _, r := range reps {
		m := r.(map[string]any)
		if m["ReplicaStatus"] != "ACTIVE" {
			return nil
		}
		out = append(out, m["RegionName"].(string))
	}
	return out
}

func TestGlobalTableReplicas(t *testing.T) {
	east := newClient(t)
	west, tokyo := east.in("eu-west-1"), east.in("ap-northeast-1")
	east.ok("CreateTable", routeTable)
	east.ok("PutItem", `{"TableName":"route","Item":{"stop":{"S":"lapland"}}}`)

	// Adding a replica turns the stream on and copies existing items.
	out := east.ok("UpdateTable", `{"TableName":"route","ReplicaUpdates":[{"Create":{"RegionName":"eu-west-1"}}]}`)
	desc := out["TableDescription"].(map[string]any)
	if got := replicaRegions(desc); strings.Join(got, ",") != "eu-west-1" || desc["GlobalTableVersion"] != "2019.11.21" {
		t.Fatalf("replicas after create: %v", desc)
	}
	if spec, _ := desc["StreamSpecification"].(map[string]any); spec["StreamViewType"] != "NEW_AND_OLD_IMAGES" {
		t.Fatalf("stream not enabled: %v", desc)
	}
	wdesc := west.ok("DescribeTable", `{"TableName":"route"}`)["Table"].(map[string]any)
	if !strings.HasPrefix(wdesc["TableArn"].(string), "arn:aws:dynamodb:eu-west-1:") ||
		strings.Join(replicaRegions(wdesc), ",") != "us-east-1" || wdesc["ItemCount"].(float64) != 1 {
		t.Fatalf("replica table: %v", wdesc)
	}

	// Writes in either region reach the other.
	west.ok("PutItem", `{"TableName":"route","Item":{"stop":{"S":"dublin"}}}`)
	east.ok("UpdateItem", `{"TableName":"route","Key":{"stop":{"S":"lapland"}},"UpdateExpression":"SET visited = :t","ExpressionAttributeValues":{":t":{"BOOL":true}}}`)
	if item := east.ok("GetItem", `{"TableName":"route","Key":{"stop":{"S":"dublin"}}}`)["Item"]; item == nil {
		t.Fatal("west write missing in east")
	}
	item, _ := west.ok("GetItem", `{"TableName":"route","Key":{"stop":{"S":"lapland"}}}`)["Item"].(map[string]any)
	if item["visited"] == nil {
		t.Fatalf("east update missing in west: %v", item)
	}
	east.ok("DeleteItem", `{"TableName":"route","Key":{"stop":{"S":"dublin"}}}`)
	if item := west.ok("GetItem", `{"TableName":"route","Key":{"stop":{"S":"dublin"}}}`)["Item"]; item != nil {
		t.Fatal("east delete not replicated")
	}

	// A third replica knows about both others, and a new GSI reaches all three.
	east.ok("UpdateTable", `{"TableName":"route","ReplicaUpdates":[{"Create":{"RegionName":"ap-northeast-1"}}]}`)
	east.ok("UpdateTable", `{"TableName":"route","AttributeDefinitions":[{"AttributeName":"zone","AttributeType":"S"}],
		"GlobalSecondaryIndexUpdates":[{"Create":{"IndexName":"by-zone","KeySchema":[{"AttributeName":"zone","KeyType":"HASH"}],"Projection":{"ProjectionType":"ALL"}}}]}`)
	for _, c := range []*client{west, tokyo} {
		d := c.ok("DescribeTable", `{"TableName":"route"}`)["Table"].(map[string]any)
		if len(replicaRegions(d)) != 2 || d["GlobalSecondaryIndexes"] == nil {
			t.Fatalf("%s: %v", c.region, d)
		}
	}

	// Mistakes.
	east.fails("UpdateTable", `{"TableName":"route","ReplicaUpdates":[{"Create":{"RegionName":"us-east-1"}}]}`, "ValidationException")
	east.fails("UpdateTable", `{"TableName":"route","ReplicaUpdates":[{"Create":{"RegionName":"eu-west-1"}}]}`, "ValidationException")
	east.fails("UpdateTable", `{"TableName":"route","ReplicaUpdates":[{"Delete":{"RegionName":"sa-east-1"}}]}`, "ValidationException")
	east.fails("UpdateTable", `{"TableName":"route","StreamSpecification":{"StreamEnabled":false}}`, "ValidationException")

	// Removing a replica deletes that copy; deleting a table drops it from the rest.
	east.ok("UpdateTable", `{"TableName":"route","ReplicaUpdates":[{"Delete":{"RegionName":"ap-northeast-1"}}]}`)
	tokyo.fails("DescribeTable", `{"TableName":"route"}`, "ResourceNotFoundException")
	west.ok("DeleteTable", `{"TableName":"route"}`)
	d := east.ok("DescribeTable", `{"TableName":"route"}`)["Table"].(map[string]any)
	if d["Replicas"] != nil || d["GlobalTableVersion"] != nil {
		t.Fatalf("east still lists replicas: %v", d)
	}
}

func TestReplicaNeedsNewAndOldImages(t *testing.T) {
	c := newClient(t)
	c.ok("CreateTable", strings.Replace(routeTable, `"BillingMode"`,
		`"StreamSpecification":{"StreamEnabled":true,"StreamViewType":"KEYS_ONLY"},"BillingMode"`, 1))
	c.fails("UpdateTable", `{"TableName":"route","ReplicaUpdates":[{"Create":{"RegionName":"eu-west-1"}}]}`, "ValidationException")
	c.in("eu-west-1").ok("CreateTable", routeTable)
}

// SDKs send calls that name an account (a table ARN, or credentials that
// carry one) to {account}.ddb.{region}.amazonaws.com.
func TestAccountEndpoint(t *testing.T) {
	c := newClient(t)
	c.ok("CreateTable", routeTable)
	arn := c.ok("DescribeTable", `{"TableName":"route"}`)["Table"].(map[string]any)["TableArn"].(string)
	req := httptest.NewRequest("POST", "/", strings.NewReader(`{"ResourceArn":"`+arn+`"}`))
	req.Host = "000000000000.ddb.us-east-1.amazonaws.com"
	req.Header.Set("X-Amz-Target", "DynamoDB_20120810.ListTagsOfResource")
	req.Header.Set("Content-Type", "application/x-amz-json-1.0")
	req.Header.Set("User-Agent", "aws-sdk-go-v2 custom-"+c.ns)
	rec := httptest.NewRecorder()
	c.h.ServeHTTP(rec, req)
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"Tags"`) {
		t.Fatalf("account endpoint: %d %s", rec.Code, rec.Body.String())
	}
}
