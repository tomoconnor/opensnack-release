// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package logs_test

import (
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"opensnack/internal/api/logs"
	"opensnack/internal/resource"
)

const scope = "ns1@us-east-1"

type client struct {
	t *testing.T
	h *logs.Handler
}

func newClient(t *testing.T) (*client, resource.Store) {
	store := resource.NewMemStore()
	return &client{t: t, h: logs.NewHandler(store)}, store
}

// call runs an operation and returns the status and decoded body.
func (c *client) call(op string, body any) (int, map[string]any) {
	c.t.Helper()
	buf, _ := json.Marshal(body)
	req := httptest.NewRequest("POST", "/", strings.NewReader(string(buf)))
	req.Header.Set("Content-Type", "application/x-amz-json-1.1")
	req.Header.Set("X-Amz-Target", "Logs_20140328."+op)
	req.Header.Set("User-Agent", "opensnack-test custom-ns1")
	rec := httptest.NewRecorder()
	c.h.Dispatch(rec, req)
	out := map[string]any{}
	json.Unmarshal(rec.Body.Bytes(), &out)
	return rec.Code, out
}

func (c *client) must(op string, body any) map[string]any {
	c.t.Helper()
	code, out := c.call(op, body)
	if code != 200 {
		c.t.Fatalf("%s: %d %v", op, code, out)
	}
	return out
}

func (c *client) mustFail(op string, body any, errType string) {
	c.t.Helper()
	code, out := c.call(op, body)
	if code == 200 || out["__type"] != errType {
		c.t.Fatalf("%s: want %s, got %d %v", op, errType, code, out)
	}
}

func messages(out map[string]any) []string {
	var msgs []string
	for _, e := range out["events"].([]any) {
		msgs = append(msgs, e.(map[string]any)["message"].(string))
	}
	return msgs
}

func TestLogGroupLifecycle(t *testing.T) {
	c, store := newClient(t)
	c.must("CreateLogGroup", map[string]any{"logGroupName": "/app/web", "tags": map[string]string{"team": "team"}, "kmsKeyId": "arn:aws:kms:us-east-1:000000000000:key/abc"})
	c.mustFail("CreateLogGroup", map[string]any{"logGroupName": "/app/web"}, "ResourceAlreadyExistsException")
	c.must("PutRetentionPolicy", map[string]any{"logGroupName": "/app/web", "retentionInDays": 14})
	c.mustFail("PutRetentionPolicy", map[string]any{"logGroupName": "/app/web", "retentionInDays": 13}, "InvalidParameterException")

	out := c.must("DescribeLogGroups", map[string]any{"logGroupNamePrefix": "/app/web"})
	g := out["logGroups"].([]any)[0].(map[string]any)
	if g["retentionInDays"] != 14.0 || g["logGroupClass"] != "STANDARD" || g["kmsKeyId"] == nil {
		t.Fatalf("group: %v", g)
	}
	arn := g["logGroupArn"].(string)
	if !strings.HasSuffix(g["arn"].(string), ":*") || strings.HasSuffix(arn, ":*") {
		t.Fatalf("arns: %v", g)
	}

	c.must("DeleteRetentionPolicy", map[string]any{"logGroupName": "/app/web"})
	c.must("DisassociateKmsKey", map[string]any{"logGroupName": "/app/web"})
	g = c.must("DescribeLogGroups", map[string]any{})["logGroups"].([]any)[0].(map[string]any)
	if _, ok := g["retentionInDays"]; ok {
		t.Fatalf("retention not removed: %v", g)
	}
	if _, ok := g["kmsKeyId"]; ok {
		t.Fatalf("kms key not removed: %v", g)
	}

	c.must("TagResource", map[string]any{"resourceArn": arn, "tags": map[string]string{"env": "prod"}})
	c.must("UntagResource", map[string]any{"resourceArn": arn, "tagKeys": []string{"team"}})
	tags := c.must("ListTagsForResource", map[string]any{"resourceArn": arn})["tags"].(map[string]any)
	if len(tags) != 1 || tags["env"] != "prod" {
		t.Fatalf("tags: %v", tags)
	}
	c.must("TagLogGroup", map[string]any{"logGroupName": "/app/web", "tags": map[string]string{"old": "api"}})
	tags = c.must("ListTagsLogGroup", map[string]any{"logGroupName": "/app/web"})["tags"].(map[string]any)
	if len(tags) != 2 {
		t.Fatalf("legacy tags: %v", tags)
	}

	c.must("PutLogGroupDeletionProtection", map[string]any{"logGroupIdentifier": arn, "deletionProtectionEnabled": true})
	c.mustFail("DeleteLogGroup", map[string]any{"logGroupName": "/app/web"}, "InvalidOperationException")
	c.must("PutLogGroupDeletionProtection", map[string]any{"logGroupIdentifier": "/app/web", "deletionProtectionEnabled": false})

	c.must("CreateLogStream", map[string]any{"logGroupName": "/app/web", "logStreamName": "s1"})
	c.must("DeleteLogGroup", map[string]any{"logGroupName": "/app/web"})
	c.mustFail("DeleteLogGroup", map[string]any{"logGroupName": "/app/web"}, "ResourceNotFoundException")
	if streams, _ := store.List("logs", "log_stream", scope); len(streams) != 0 {
		t.Fatalf("streams left behind: %v", streams)
	}
}

func TestPutAndGetLogEvents(t *testing.T) {
	c, _ := newClient(t)
	c.must("CreateLogGroup", map[string]any{"logGroupName": "g"})
	c.mustFail("PutLogEvents", map[string]any{"logGroupName": "g", "logStreamName": "s", "logEvents": []any{map[string]any{"timestamp": 1, "message": "x"}}}, "ResourceNotFoundException")
	c.must("CreateLogStream", map[string]any{"logGroupName": "g", "logStreamName": "s"})

	base := time.Now().Add(-time.Hour).UnixMilli()
	var evs []any
	for i := 0; i < 25; i++ {
		evs = append(evs, map[string]any{"timestamp": base + int64(i)*1000, "message": fmt.Sprintf("line %02d", i)})
	}
	c.must("PutLogEvents", map[string]any{"logGroupName": "g", "logStreamName": "s", "logEvents": evs})

	c.mustFail("PutLogEvents", map[string]any{"logGroupName": "g", "logStreamName": "s", "logEvents": []any{
		map[string]any{"timestamp": base + 5, "message": "b"}, map[string]any{"timestamp": base, "message": "a"},
	}}, "InvalidParameterException")

	// From the head, in pages of 10, following the forward token.
	var all []string
	token := ""
	for i := 0; i < 5; i++ {
		req := map[string]any{"logGroupName": "g", "logStreamName": "s", "startFromHead": true, "limit": 10}
		if token != "" {
			req["nextToken"] = token
		}
		out := c.must("GetLogEvents", req)
		all = append(all, messages(out)...)
		if out["nextForwardToken"] == token {
			break
		}
		token = out["nextForwardToken"].(string)
	}
	if len(all) != 25 || all[0] != "line 00" || all[24] != "line 24" {
		t.Fatalf("forward paging: %v", all)
	}

	// Default is the tail; the backward token pages towards the start.
	out := c.must("GetLogEvents", map[string]any{"logGroupName": "g", "logStreamName": "s", "limit": 10})
	if m := messages(out); len(m) != 10 || m[0] != "line 15" || m[9] != "line 24" {
		t.Fatalf("tail: %v", m)
	}
	out = c.must("GetLogEvents", map[string]any{"logGroupName": "g", "logStreamName": "s", "limit": 10, "nextToken": out["nextBackwardToken"]})
	if m := messages(out); len(m) != 10 || m[0] != "line 05" || m[9] != "line 14" {
		t.Fatalf("backward page: %v", m)
	}

	// Time range: start inclusive, end exclusive.
	out = c.must("GetLogEvents", map[string]any{"logGroupName": "g", "logStreamName": "s", "startFromHead": true,
		"startTime": base + 3000, "endTime": base + 6000})
	if m := messages(out); len(m) != 3 || m[0] != "line 03" || m[2] != "line 05" {
		t.Fatalf("time range: %v", m)
	}

	streams := c.must("DescribeLogStreams", map[string]any{"logGroupName": "g"})["logStreams"].([]any)
	s := streams[0].(map[string]any)
	if s["firstEventTimestamp"] != float64(base) || s["lastEventTimestamp"] != float64(base+24000) {
		t.Fatalf("stream stats: %v", s)
	}
}

func TestPutLogEventsRejectsOldEvents(t *testing.T) {
	c, _ := newClient(t)
	c.must("CreateLogGroup", map[string]any{"logGroupName": "g"})
	c.must("CreateLogStream", map[string]any{"logGroupName": "g", "logStreamName": "s"})
	now := time.Now()
	out := c.must("PutLogEvents", map[string]any{"logGroupName": "g", "logStreamName": "s", "logEvents": []any{
		map[string]any{"timestamp": now.Add(-15 * 24 * time.Hour).UnixMilli(), "message": "ancient"},
		map[string]any{"timestamp": now.Add(-15*24*time.Hour + time.Hour).UnixMilli(), "message": "fine?"},
	}})
	if out["rejectedLogEventsInfo"] == nil {
		t.Fatalf("expected rejected info: %v", out)
	}
}

func TestFilterLogEvents(t *testing.T) {
	c, store := newClient(t)
	now := time.Now().UnixMilli()
	write := func(stream string, msgs ...string) {
		var evs []logs.InputEvent
		for i, m := range msgs {
			evs = append(evs, logs.InputEvent{Timestamp: now + int64(i), Message: m})
		}
		if err := logs.Append(store, logs.Target{Scope: scope, AccountID: "123456789012", Group: "/example-orders/orders", Stream: stream}, evs); err != nil {
			t.Fatal(err)
		}
	}
	write("a",
		"INFO orders ready",
		"ERROR fleet Nightly missing",
		"ERROR disk full on orders",
		`{"level":"error","worker":"Bernard","latency":420,"tags":["night"]}`,
		`{"level":"info","worker":"Pepper","latency":12}`,
	)
	write("b",
		"WARN rain heavy",
		`127.0.0.1 - frank [10/Oct/2026:13:55:36 -0700] "GET /asset HTTP/1.0" 404 2326`,
		`127.0.0.1 - app [10/Oct/2026:13:55:37 -0700] "GET /list HTTP/1.0" 200 512`,
	)

	cases := []struct {
		pattern string
		want    []string
	}{
		{"", nil}, // all 8
		{"ERROR", []string{"ERROR fleet Nightly missing", "ERROR disk full on orders"}},
		{"ERROR orders", []string{"ERROR disk full on orders"}},
		{`"disk full"`, []string{"ERROR disk full on orders"}},
		{"ERROR -Nightly", []string{"ERROR disk full on orders"}},
		{"?WARN ?Nightly", []string{"WARN rain heavy", "ERROR fleet Nightly missing"}}, // ordered by timestamp across streams
		{"%Nig[a-z]+%", []string{"ERROR fleet Nightly missing"}},
		{`{ $.level = "error" }`, []string{`{"level":"error","worker":"Bernard","latency":420,"tags":["night"]}`}},
		{`{ $.latency > 100 && $.worker = "Bern*" }`, []string{`{"level":"error","worker":"Bernard","latency":420,"tags":["night"]}`}},
		{`{ ($.latency < 20) || $.tags[0] = "night" }`, []string{
			`{"level":"error","worker":"Bernard","latency":420,"tags":["night"]}`, `{"level":"info","worker":"Pepper","latency":12}`}},
		{`{ $.missing NOT EXISTS && $.level != "error" }`, []string{`{"level":"info","worker":"Pepper","latency":12}`}},
		{`[ip, id, user, timestamp, request, status_code = 4*, size]`, []string{
			`127.0.0.1 - frank [10/Oct/2026:13:55:36 -0700] "GET /asset HTTP/1.0" 404 2326`}},
		{`[..., status = 200, bytes]`, []string{
			`127.0.0.1 - app [10/Oct/2026:13:55:37 -0700] "GET /list HTTP/1.0" 200 512`}},
	}
	for _, tc := range cases {
		out := c.must("FilterLogEvents", map[string]any{"logGroupName": "/example-orders/orders", "filterPattern": tc.pattern})
		got := messages(out)
		if tc.want == nil {
			if len(got) != 8 {
				t.Errorf("%q: got %d events", tc.pattern, len(got))
			}
			continue
		}
		if strings.Join(got, "|") != strings.Join(tc.want, "|") {
			t.Errorf("%q:\n got %q\nwant %q", tc.pattern, got, tc.want)
		}
	}

	// Stream selection and paging.
	out := c.must("FilterLogEvents", map[string]any{"logGroupName": "/example-orders/orders", "logStreamNames": []string{"b"}, "limit": 2})
	if len(messages(out)) != 2 || out["nextToken"] == nil {
		t.Fatalf("page 1: %v", out)
	}
	out = c.must("FilterLogEvents", map[string]any{"logGroupName": "/example-orders/orders", "logStreamNames": []string{"b"}, "limit": 2, "nextToken": out["nextToken"]})
	if m := messages(out); len(m) != 1 || !strings.Contains(m[0], "app") {
		t.Fatalf("page 2: %v", out)
	}

	c.mustFail("FilterLogEvents", map[string]any{"logGroupName": "/example-orders/orders", "filterPattern": `{ $.x = }`}, "InvalidParameterException")
}

func TestMetricAndSubscriptionFilters(t *testing.T) {
	c, _ := newClient(t)
	c.must("CreateLogGroup", map[string]any{"logGroupName": "g"})
	mf := map[string]any{
		"logGroupName": "g", "filterName": "errors", "filterPattern": "ERROR",
		"metricTransformations": []any{map[string]any{"metricName": "Errors", "metricNamespace": "Team", "metricValue": "1", "unit": "None"}},
	}
	c.must("PutMetricFilter", mf)
	out := c.must("DescribeMetricFilters", map[string]any{"logGroupName": "g", "filterNamePrefix": "err"})
	f := out["metricFilters"].([]any)[0].(map[string]any)
	if f["filterPattern"] != "ERROR" || f["logGroupName"] != "g" {
		t.Fatalf("filter: %v", f)
	}
	tr := f["metricTransformations"].([]any)[0].(map[string]any)
	if tr["unit"] != "None" || tr["metricValue"] != "1" {
		t.Fatalf("transformation: %v", tr)
	}
	g := c.must("DescribeLogGroups", map[string]any{})["logGroups"].([]any)[0].(map[string]any)
	if g["metricFilterCount"] != 1.0 {
		t.Fatalf("metricFilterCount: %v", g)
	}

	tm := c.must("TestMetricFilter", map[string]any{"filterPattern": "[a, b = 5*]", "logEventMessages": []string{"x 500", "y 200"}})
	if m := tm["matches"].([]any); len(m) != 1 {
		t.Fatalf("test filter: %v", tm)
	}

	c.must("PutSubscriptionFilter", map[string]any{"logGroupName": "g", "filterName": "s", "filterPattern": "", "destinationArn": "arn:aws:lambda:us-east-1:000000000000:function:f"})
	sf := c.must("DescribeSubscriptionFilters", map[string]any{"logGroupName": "g"})["subscriptionFilters"].([]any)[0].(map[string]any)
	if sf["distribution"] != "ByLogStream" {
		t.Fatalf("subscription filter: %v", sf)
	}
	c.must("DeleteSubscriptionFilter", map[string]any{"logGroupName": "g", "filterName": "s"})
	c.must("DeleteMetricFilter", map[string]any{"logGroupName": "g", "filterName": "errors"})
	c.mustFail("DeleteMetricFilter", map[string]any{"logGroupName": "g", "filterName": "errors"}, "ResourceNotFoundException")
}

func TestAppendCreatesGroupAndStream(t *testing.T) {
	c, store := newClient(t)
	tgt := logs.Target{Scope: scope, AccountID: "123456789012", Group: "/aws/lambda/fn", Stream: "2026/10/14/[$LATEST]abc"}
	if err := logs.Append(store, tgt, []logs.InputEvent{{Message: "START"}, {Message: "END"}}); err != nil {
		t.Fatal(err)
	}
	if err := logs.Append(store, tgt, []logs.InputEvent{{Message: "again"}}); err != nil {
		t.Fatal(err)
	}
	g := c.must("DescribeLogGroups", map[string]any{})["logGroups"].([]any)[0].(map[string]any)
	if g["arn"] != "arn:aws:logs:us-east-1:123456789012:log-group:/aws/lambda/fn:*" {
		t.Fatalf("group: %v", g)
	}
	out := c.must("GetLogEvents", map[string]any{"logGroupName": tgt.Group, "logStreamName": tgt.Stream, "startFromHead": true})
	if m := messages(out); len(m) != 3 {
		t.Fatalf("events: %v", m)
	}
}

func TestPatternParseErrors(t *testing.T) {
	for _, p := range []string{`{ $.a = }`, `{ $.a > "x" }`, `[a, , b]`, `"unterminated`, `{ $.a = 1`} {
		if _, err := logs.ParsePattern(p); err == nil {
			t.Errorf("%q: expected an error", p)
		}
	}
}
