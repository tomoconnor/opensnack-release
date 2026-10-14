// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package router_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"opensnack/internal/api/events"
	"opensnack/internal/resource"
	"opensnack/internal/router"
)

type fakeDeliverer struct {
	mu  sync.Mutex
	got []events.Delivery
	sc  []events.Scope
}

func (f *fakeDeliverer) Deliver(_ context.Context, scope events.Scope, d events.Delivery) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.got = append(f.got, d)
	f.sc = append(f.sc, scope)
	return nil
}

type fakeClock struct{ t time.Time }

func (c *fakeClock) Now() time.Time { return c.t }

func newEventsServer(t *testing.T) (*router.Server, *fakeDeliverer, *fakeClock) {
	t.Helper()
	fd := &fakeDeliverer{}
	clk := &fakeClock{t: time.Date(2026, 12, 13, 6, 0, 0, 0, time.UTC)}
	srv := router.NewServer(resource.NewMemStore(), router.Config{
		Clock:           clk.Now,
		EventDeliverers: map[string]events.Deliverer{"sqs": fd},
	})
	return srv, fd, clk
}

func eventsCall(t *testing.T, h http.Handler, op string, body any) (int, map[string]any) {
	t.Helper()
	b, _ := json.Marshal(body)
	req := httptest.NewRequest("POST", "/", strings.NewReader(string(b)))
	req.Header.Set("X-Amz-Target", "AWSEvents."+op)
	req.Header.Set("Content-Type", "application/x-amz-json-1.1")
	req.Header.Set("User-Agent", "test custom-team")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	var out map[string]any
	json.Unmarshal(rec.Body.Bytes(), &out)
	return rec.Code, out
}

func mustOK(t *testing.T, h http.Handler, op string, body any) map[string]any {
	t.Helper()
	code, out := eventsCall(t, h, op, body)
	if code != 200 {
		t.Fatalf("%s: HTTP %d %v", op, code, out)
	}
	return out
}

func TestEvents_PatternRuleDeliversTransformedEvent(t *testing.T) {
	srv, fd, _ := newEventsServer(t)
	mustOK(t, srv, "CreateEventBus", map[string]any{"Name": "orders"})
	pattern := `{"source":["example.orders"],"detail":{"weight":[{"numeric":[">",100]}]}}`
	out := mustOK(t, srv, "PutRule", map[string]any{
		"Name": "heavy", "EventBusName": "orders", "EventPattern": pattern, "Description": "heavy parcels",
	})
	if arn := out["RuleArn"]; arn != "arn:aws:events:us-east-1:000000000000:rule/orders/heavy" {
		t.Fatalf("RuleArn = %v", arn)
	}
	queueArn := "arn:aws:sqs:us-east-1:000000000000:parcels"
	mustOK(t, srv, "PutTargets", map[string]any{
		"Rule": "heavy", "EventBusName": "orders",
		"Targets": []any{map[string]any{
			"Id": "q", "Arn": queueArn,
			"InputTransformer": map[string]any{
				"InputPathsMap": map[string]string{"w": "$.detail.weight", "who": "$.detail.worker"},
				"InputTemplate": `{"weight": <w>, "msg": "<who> lifted <w>kg"}`,
			},
		}},
	})

	desc := mustOK(t, srv, "DescribeRule", map[string]any{"Name": "heavy", "EventBusName": "orders"})
	if desc["EventPattern"] != pattern || desc["State"] != "ENABLED" || desc["Description"] != "heavy parcels" {
		t.Fatalf("DescribeRule = %v", desc)
	}

	out = mustOK(t, srv, "PutEvents", map[string]any{"Entries": []any{
		map[string]any{"Source": "example.orders", "DetailType": "Parcel", "Detail": `{"weight":120,"worker":"Bob"}`, "EventBusName": "orders"},
		map[string]any{"Source": "example.orders", "DetailType": "Parcel", "Detail": `{"weight":5,"worker":"Ann"}`, "EventBusName": "orders"},
		map[string]any{"Source": "example.orders", "DetailType": "Parcel", "Detail": `{"weight":500}`}, // default bus: no rule
		map[string]any{"Source": "example.orders", "DetailType": "Parcel", "Detail": `not json`, "EventBusName": "orders"},
	}})
	if out["FailedEntryCount"] != float64(1) {
		t.Fatalf("PutEvents = %v", out)
	}
	entries := out["Entries"].([]any)
	if len(entries) != 4 || entries[0].(map[string]any)["EventId"] == nil || entries[3].(map[string]any)["ErrorCode"] != "MalformedDetail" {
		t.Fatalf("Entries = %v", entries)
	}

	if len(fd.got) != 1 {
		t.Fatalf("deliveries = %d, want 1", len(fd.got))
	}
	d := fd.got[0]
	if d.TargetArn != queueArn || d.Target.Id != "q" {
		t.Fatalf("delivery = %+v", d)
	}
	if want := `{"weight": 120, "msg": "Bob lifted 120kg"}`; string(d.Payload) != want {
		t.Fatalf("payload = %s, want %s", d.Payload, want)
	}
	if fd.sc[0].Namespace != "team" || fd.sc[0].Region != "us-east-1" {
		t.Fatalf("scope = %+v", fd.sc[0])
	}

	// Disabled rules don't match.
	mustOK(t, srv, "DisableRule", map[string]any{"Name": "heavy", "EventBusName": "orders"})
	mustOK(t, srv, "PutEvents", map[string]any{"Entries": []any{
		map[string]any{"Source": "example.orders", "DetailType": "Parcel", "Detail": `{"weight":120}`, "EventBusName": "orders"},
	}})
	if len(fd.got) != 1 {
		t.Fatalf("disabled rule delivered")
	}
}

func TestEvents_WholeEnvelopeAndInputPath(t *testing.T) {
	srv, fd, _ := newEventsServer(t)
	mustOK(t, srv, "PutRule", map[string]any{"Name": "all", "EventPattern": `{"source":[{"prefix":"widget"}]}`})
	mustOK(t, srv, "PutTargets", map[string]any{"Rule": "all", "Targets": []any{
		map[string]any{"Id": "whole", "Arn": "arn:aws:sqs:us-east-1:000000000000:a"},
		map[string]any{"Id": "path", "Arn": "arn:aws:sqs:us-east-1:000000000000:b", "InputPath": "$.detail.widget"},
		map[string]any{"Id": "const", "Arn": "arn:aws:sqs:us-east-1:000000000000:c", "Input": `{"fixed":true}`},
	}})
	mustOK(t, srv, "PutEvents", map[string]any{"Entries": []any{
		map[string]any{"Source": "widget.factory", "DetailType": "Built", "Detail": `{"widget":{"kind":"train"}}`, "Resources": []string{"r1"}},
	}})
	if len(fd.got) != 3 {
		t.Fatalf("deliveries = %d", len(fd.got))
	}
	var env map[string]any
	if err := json.Unmarshal(fd.got[0].Payload, &env); err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{"version", "id", "detail-type", "source", "account", "time", "region", "resources", "detail"} {
		if _, ok := env[k]; !ok {
			t.Errorf("envelope lacks %q: %s", k, fd.got[0].Payload)
		}
	}
	if env["time"] != "2026-12-13T06:00:00Z" || env["account"] != "000000000000" {
		t.Errorf("envelope = %s", fd.got[0].Payload)
	}
	if string(fd.got[1].Payload) != `{"kind":"train"}` || string(fd.got[2].Payload) != `{"fixed":true}` {
		t.Errorf("payloads = %s / %s", fd.got[1].Payload, fd.got[2].Payload)
	}
}

func TestEvents_RuleLifecycle(t *testing.T) {
	srv, _, _ := newEventsServer(t)
	if code, out := eventsCall(t, srv, "PutRule", map[string]any{"Name": "x", "EventBusName": "nope", "EventPattern": `{"source":["a"]}`}); code != 400 || out["__type"] != "ResourceNotFoundException" {
		t.Fatalf("missing bus: %d %v", code, out)
	}
	if code, out := eventsCall(t, srv, "PutRule", map[string]any{"Name": "x", "EventPattern": `{"source":"a"}`}); code != 400 || out["__type"] != "InvalidEventPatternException" {
		t.Fatalf("bad pattern: %d %v", code, out)
	}
	if code, out := eventsCall(t, srv, "PutRule", map[string]any{"Name": "x", "ScheduleExpression": "rate(5 minute)"}); code != 400 || out["__type"] != "ValidationException" {
		t.Fatalf("bad schedule: %d %v", code, out)
	}
	mustOK(t, srv, "PutRule", map[string]any{"Name": "alpha-1", "EventPattern": `{"source":["a"]}`, "Tags": []any{map[string]string{"Key": "team", "Value": "team"}}})
	mustOK(t, srv, "PutRule", map[string]any{"Name": "alpha-2", "ScheduleExpression": "cron(0 6 * * ? *)", "State": "DISABLED"})
	mustOK(t, srv, "PutRule", map[string]any{"Name": "beta", "EventPattern": `{"source":["b"]}`})

	out := mustOK(t, srv, "ListRules", map[string]any{"NamePrefix": "alpha"})
	if rules := out["Rules"].([]any); len(rules) != 2 || rules[1].(map[string]any)["State"] != "DISABLED" {
		t.Fatalf("ListRules = %v", out)
	}

	arn := "arn:aws:events:us-east-1:000000000000:rule/alpha-1"
	mustOK(t, srv, "TagResource", map[string]any{"ResourceARN": arn, "Tags": []any{map[string]string{"Key": "day", "Value": "13"}}})
	mustOK(t, srv, "UntagResource", map[string]any{"ResourceARN": arn, "TagKeys": []string{"team"}})
	out = mustOK(t, srv, "ListTagsForResource", map[string]any{"ResourceARN": arn})
	if tags := out["Tags"].([]any); len(tags) != 1 || tags[0].(map[string]any)["Key"] != "day" {
		t.Fatalf("tags = %v", out)
	}

	mustOK(t, srv, "PutTargets", map[string]any{"Rule": "beta", "Targets": []any{map[string]any{"Id": "t1", "Arn": "arn:aws:sns:us-east-1:000000000000:topic"}}})
	if code, out := eventsCall(t, srv, "DeleteRule", map[string]any{"Name": "beta"}); code != 400 || !strings.Contains(out["message"].(string), "has targets") {
		t.Fatalf("DeleteRule with targets: %d %v", code, out)
	}
	out = mustOK(t, srv, "ListTargetsByRule", map[string]any{"Rule": "beta"})
	if len(out["Targets"].([]any)) != 1 {
		t.Fatalf("targets = %v", out)
	}
	mustOK(t, srv, "RemoveTargets", map[string]any{"Rule": "beta", "Ids": []string{"t1"}})
	mustOK(t, srv, "DeleteRule", map[string]any{"Name": "beta"})
	if code, _ := eventsCall(t, srv, "DescribeRule", map[string]any{"Name": "beta"}); code != 400 {
		t.Fatalf("rule still there")
	}

	out = mustOK(t, srv, "ListEventBuses", map[string]any{})
	if buses := out["EventBuses"].([]any); len(buses) != 1 || buses[0].(map[string]any)["Name"] != "default" {
		t.Fatalf("buses = %v", out)
	}
}

func TestEvents_SchedulerFiresWithFakeClock(t *testing.T) {
	srv, fd, clk := newEventsServer(t)
	mustOK(t, srv, "PutRule", map[string]any{"Name": "daily", "ScheduleExpression": "rate(5 minutes)"})
	mustOK(t, srv, "PutRule", map[string]any{"Name": "sleepy", "ScheduleExpression": "rate(1 minute)", "State": "DISABLED"})
	mustOK(t, srv, "PutRule", map[string]any{"Name": "nightly", "ScheduleExpression": "cron(0 6 * * ? *)"})
	for _, rule := range []string{"daily", "sleepy", "nightly"} {
		mustOK(t, srv, "PutTargets", map[string]any{"Rule": rule, "Targets": []any{
			map[string]any{"Id": "q", "Arn": "arn:aws:sqs:us-east-1:000000000000:" + rule},
		}})
	}
	ctx := context.Background()
	sched := srv.Scheduler

	// First sight schedules the first fire one period out.
	if n := sched.Tick(ctx); n != 0 {
		t.Fatalf("first tick fired %d", n)
	}
	clk.t = clk.t.Add(4 * time.Minute)
	if n := sched.Tick(ctx); n != 0 {
		t.Fatalf("fired early: %d", n)
	}
	clk.t = clk.t.Add(time.Minute) // 06:05
	if n := sched.Tick(ctx); n != 1 {
		t.Fatalf("at 06:05 fired %d, want 1", n)
	}
	d := fd.got[0]
	var env map[string]any
	json.Unmarshal(d.Payload, &env)
	if d.TargetArn != "arn:aws:sqs:us-east-1:000000000000:daily" || env["detail-type"] != "Scheduled Event" || env["source"] != "aws.events" {
		t.Fatalf("delivery = %s to %s", d.Payload, d.TargetArn)
	}
	if res := env["resources"].([]any); len(res) != 1 || res[0] != "arn:aws:events:us-east-1:000000000000:rule/daily" {
		t.Fatalf("resources = %v", res)
	}
	if env["time"] != "2026-12-13T06:05:00Z" {
		t.Fatalf("time = %v", env["time"])
	}
	if n := sched.Tick(ctx); n != 0 {
		t.Fatalf("fired twice in one period")
	}

	// A day later both enabled rules have come due; missed periods fire once.
	clk.t = time.Date(2026, 12, 14, 6, 0, 30, 0, time.UTC)
	if n := sched.Tick(ctx); n != 2 {
		t.Fatalf("next day fired %d, want 2", n)
	}
	for _, d := range fd.got {
		if strings.HasSuffix(d.TargetArn, "sleepy") {
			t.Fatalf("disabled rule fired")
		}
	}

	// Deleting the rule stops it.
	mustOK(t, srv, "RemoveTargets", map[string]any{"Rule": "daily", "Ids": []string{"q"}})
	mustOK(t, srv, "DeleteRule", map[string]any{"Name": "daily"})
	clk.t = clk.t.Add(time.Hour)
	before := len(fd.got)
	sched.Tick(ctx)
	if len(fd.got) != before {
		t.Fatalf("deleted rule fired")
	}
}

func TestEvents_DeliveryLogAndLambdaTarget(t *testing.T) {
	srv, _, _ := newEventsServer(t)
	mustOK(t, srv, "PutRule", map[string]any{"Name": "r", "EventPattern": `{"source":["s"]}`})
	mustOK(t, srv, "PutTargets", map[string]any{"Rule": "r", "Targets": []any{
		map[string]any{"Id": "fn", "Arn": "arn:aws:lambda:us-east-1:000000000000:function:f"},
	}})
	mustOK(t, srv, "PutEvents", map[string]any{"Entries": []any{map[string]any{"Source": "s", "DetailType": "d", "Detail": "{}"}}})

	req := httptest.NewRequest("GET", "/_opensnack/v1/events/deliveries?namespace=team", nil)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	var out struct {
		Deliveries []events.Attempt `json:"deliveries"`
	}
	json.Unmarshal(rec.Body.Bytes(), &out)
	// The target reaches Lambda's Invoke, which reports the missing function.
	if rec.Code != 200 || len(out.Deliveries) != 1 || out.Deliveries[0].Status != "failed" ||
		out.Deliveries[0].TargetID != "fn" || !strings.Contains(out.Deliveries[0].Error, "Function not found") {
		t.Fatalf("deliveries: %d %s", rec.Code, rec.Body.String())
	}
}
