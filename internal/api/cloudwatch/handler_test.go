// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package cloudwatch

import (
	"bytes"
	"encoding/json"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	smithycbor "github.com/aws/smithy-go/encoding/cbor"

	"opensnack/internal/resource"
)

type published struct{ topic, subject, message string }

type fakeNotifier struct {
	mu   sync.Mutex
	msgs []published
}

func (f *fakeNotifier) Publish(scope, account, topic, subject, message string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.msgs = append(f.msgs, published{topic, subject, message})
	return nil
}

func newTestHandler(t *testing.T) (*Handler, *fakeNotifier) {
	h := NewHandler(resource.NewMemStore())
	n := &fakeNotifier{}
	h.SetNotifier(n)
	return h, n
}

// cborCall sends an RPCv2 CBOR request the way aws-sdk-go-v2 does.
func cborCall(t *testing.T, h *Handler, op string, in smithycbor.Map) (int, smithycbor.Map) {
	t.Helper()
	req := httptest.NewRequest("POST", cborPrefix+op, bytes.NewReader(smithycbor.Encode(in)))
	req.Header.Set("Content-Type", "application/cbor")
	req.Header.Set("Smithy-Protocol", "rpc-v2-cbor")
	req.Header.Set("User-Agent", "test custom-ns1")
	rec := httptest.NewRecorder()
	h.Dispatch(rec, req)
	if rec.Header().Get("Smithy-Protocol") != "rpc-v2-cbor" {
		t.Fatalf("%s: missing smithy-protocol header", op)
	}
	v, err := smithycbor.Decode(rec.Body.Bytes())
	if err != nil {
		t.Fatalf("%s: response is not CBOR: %v", op, err)
	}
	return rec.Code, v.(smithycbor.Map)
}

func mustCBOR(t *testing.T, h *Handler, op string, in smithycbor.Map) smithycbor.Map {
	t.Helper()
	code, out := cborCall(t, h, op, in)
	if code != 200 {
		t.Fatalf("%s: %d %v", op, code, out)
	}
	return out
}

func s(v string) smithycbor.String { return smithycbor.String(v) }

func strs(v ...string) smithycbor.List {
	l := smithycbor.List{}
	for _, x := range v {
		l = append(l, s(x))
	}
	return l
}

func alarmInput(threshold float64) smithycbor.Map {
	return smithycbor.Map{
		"AlarmName":          s("orders-cpu"),
		"AlarmDescription":   s("orders CPU is high"),
		"ActionsEnabled":     smithycbor.Bool(true),
		"AlarmActions":       strs("arn:aws:sns:us-east-1:000000000000:pager"),
		"MetricName":         s("CPUUtilization"),
		"Namespace":          s("Example/Orders"),
		"Statistic":          s("Average"),
		"Dimensions":         smithycbor.List{smithycbor.Map{"Name": s("Orders"), "Value": s("one")}},
		"Period":             smithycbor.Uint(60),
		"EvaluationPeriods":  smithycbor.Uint(1),
		"Threshold":          smithycbor.Float64(threshold),
		"ComparisonOperator": s("GreaterThanThreshold"),
		"TreatMissingData":   s("missing"),
		"Tags":               smithycbor.List{smithycbor.Map{"Key": s("team"), "Value": s("team")}},
	}
}

func describeOne(t *testing.T, h *Handler) smithycbor.Map {
	t.Helper()
	out := mustCBOR(t, h, "DescribeAlarms", smithycbor.Map{"AlarmNames": strs("orders-cpu"), "AlarmTypes": strs("MetricAlarm")})
	alarms := out["MetricAlarms"].(smithycbor.List)
	if len(alarms) != 1 {
		t.Fatalf("want one alarm, got %v", out)
	}
	return alarms[0].(smithycbor.Map)
}

func TestMetricAlarmRoundTripCBOR(t *testing.T) {
	h, _ := newTestHandler(t)
	mustCBOR(t, h, "PutMetricAlarm", alarmInput(80))
	a := describeOne(t, h)

	if v, err := smithycbor.AsFloat64(a["Threshold"]); err != nil || v != 80 {
		t.Fatalf("Threshold: %v %v", a["Threshold"], err)
	}
	if v, err := smithycbor.AsInt32(a["EvaluationPeriods"]); err != nil || v != 1 {
		t.Fatalf("EvaluationPeriods must be a CBOR integer: %#v", a["EvaluationPeriods"])
	}
	if _, err := smithycbor.AsTime(a["StateUpdatedTimestamp"]); err != nil {
		t.Fatalf("StateUpdatedTimestamp: %v", err)
	}
	if a["StateValue"] != s("INSUFFICIENT_DATA") || a["AlarmArn"] != s("arn:aws:cloudwatch:us-east-1:000000000000:alarm:orders-cpu") {
		t.Fatalf("alarm: %v", a)
	}
	dims := a["Dimensions"].(smithycbor.List)
	if len(dims) != 1 || dims[0].(smithycbor.Map)["Value"] != s("one") {
		t.Fatalf("dimensions: %v", dims)
	}
	if _, ok := a["Tags"]; ok {
		t.Fatalf("tags leaked into the alarm shape")
	}

	// Update: threshold changes, tags in the request are ignored.
	in := alarmInput(90.5)
	in["Tags"] = smithycbor.List{}
	mustCBOR(t, h, "PutMetricAlarm", in)
	if v, _ := smithycbor.AsFloat64(describeOne(t, h)["Threshold"]); v != 90.5 {
		t.Fatalf("threshold not updated: %v", v)
	}

	arn := s("arn:aws:cloudwatch:us-east-1:000000000000:alarm:orders-cpu")
	tags := mustCBOR(t, h, "ListTagsForResource", smithycbor.Map{"ResourceARN": arn})["Tags"].(smithycbor.List)
	if len(tags) != 1 {
		t.Fatalf("tags: %v", tags)
	}
	mustCBOR(t, h, "TagResource", smithycbor.Map{"ResourceARN": arn, "Tags": smithycbor.List{smithycbor.Map{"Key": s("env"), "Value": s("prod")}}})
	mustCBOR(t, h, "UntagResource", smithycbor.Map{"ResourceARN": arn, "TagKeys": strs("team")})
	tags = mustCBOR(t, h, "ListTagsForResource", smithycbor.Map{"ResourceARN": arn})["Tags"].(smithycbor.List)
	if len(tags) != 1 || tags[0].(smithycbor.Map)["Key"] != s("env") {
		t.Fatalf("tags after update: %v", tags)
	}

	mustCBOR(t, h, "DeleteAlarms", smithycbor.Map{"AlarmNames": strs("orders-cpu")})
	out := mustCBOR(t, h, "DescribeAlarms", smithycbor.Map{"AlarmNames": strs("orders-cpu")})
	if len(out["MetricAlarms"].(smithycbor.List)) != 0 {
		t.Fatalf("alarm not deleted: %v", out)
	}
	code, errOut := cborCall(t, h, "ListTagsForResource", smithycbor.Map{"ResourceARN": arn})
	if code != 404 || !strings.HasSuffix(string(errOut["__type"].(smithycbor.String)), "#ResourceNotFoundException") {
		t.Fatalf("tags of a deleted alarm: %d %v", code, errOut)
	}
}

func TestPutMetricAlarmValidation(t *testing.T) {
	h, _ := newTestHandler(t)
	in := alarmInput(1)
	delete(in, "ComparisonOperator")
	if code, out := cborCall(t, h, "PutMetricAlarm", in); code != 400 || out["__type"] != s("com.amazonaws.cloudwatch#MissingParameter") {
		t.Fatalf("got %d %v", code, out)
	}
	in = alarmInput(1)
	in["ExtendedStatistic"] = s("p99")
	if code, _ := cborCall(t, h, "PutMetricAlarm", in); code != 400 {
		t.Fatalf("Statistic and ExtendedStatistic together should fail")
	}
}

func TestAlarmFiresOnMetricData(t *testing.T) {
	h, n := newTestHandler(t)
	mustCBOR(t, h, "PutMetricAlarm", alarmInput(80))
	put := func(v float64) {
		mustCBOR(t, h, "PutMetricData", smithycbor.Map{
			"Namespace": s("Example/Orders"),
			"MetricData": smithycbor.List{smithycbor.Map{
				"MetricName": s("CPUUtilization"),
				"Dimensions": smithycbor.List{smithycbor.Map{"Name": s("Orders"), "Value": s("one")}},
				"Value":      smithycbor.Float64(v),
				"Timestamp":  &smithycbor.Tag{ID: 1, Value: smithycbor.Float64(float64(time.Now().Unix()))},
			}},
		})
	}
	put(95)
	if st := describeOne(t, h)["StateValue"]; st != s("ALARM") {
		t.Fatalf("state after a breaching point: %v", st)
	}
	if len(n.msgs) != 1 || n.msgs[0].topic != "arn:aws:sns:us-east-1:000000000000:pager" || !strings.HasPrefix(n.msgs[0].subject, "ALARM: ") {
		t.Fatalf("notifications: %+v", n.msgs)
	}
	var msg map[string]any
	if err := json.Unmarshal([]byte(n.msgs[0].message), &msg); err != nil || msg["NewStateValue"] != "ALARM" || msg["OldStateValue"] != "INSUFFICIENT_DATA" {
		t.Fatalf("message: %s", n.msgs[0].message)
	}

	// Data from another dimension doesn't touch it.
	mustCBOR(t, h, "SetAlarmState", smithycbor.Map{"AlarmName": s("orders-cpu"), "StateValue": s("OK"), "StateReason": s("manual")})
	if st := describeOne(t, h)["StateValue"]; st != s("OK") {
		t.Fatalf("SetAlarmState: %v", st)
	}
	// OK has no actions, so still one message.
	if len(n.msgs) != 1 {
		t.Fatalf("unexpected notifications: %+v", n.msgs)
	}

	stats := mustCBOR(t, h, "GetMetricStatistics", smithycbor.Map{
		"Namespace": s("Example/Orders"), "MetricName": s("CPUUtilization"),
		"Dimensions": smithycbor.List{smithycbor.Map{"Name": s("Orders"), "Value": s("one")}},
		"StartTime":  &smithycbor.Tag{ID: 1, Value: smithycbor.Float64(float64(time.Now().Add(-time.Hour).Unix()))},
		"EndTime":    &smithycbor.Tag{ID: 1, Value: smithycbor.Float64(float64(time.Now().Add(time.Hour).Unix()))},
		"Period":     smithycbor.Uint(3600), "Statistics": strs("Maximum", "SampleCount"),
	})
	dps := stats["Datapoints"].(smithycbor.List)
	if len(dps) == 0 {
		t.Fatalf("no datapoints: %v", stats)
	}
	if v, _ := smithycbor.AsFloat64(dps[len(dps)-1].(smithycbor.Map)["Maximum"]); v != 95 {
		t.Fatalf("maximum: %v", dps)
	}
	metrics := mustCBOR(t, h, "ListMetrics", smithycbor.Map{"Namespace": s("Example/Orders")})["Metrics"].(smithycbor.List)
	if len(metrics) != 1 {
		t.Fatalf("ListMetrics: %v", metrics)
	}
}

func TestCompositeAlarm(t *testing.T) {
	h, n := newTestHandler(t)
	mustCBOR(t, h, "PutMetricAlarm", alarmInput(80))
	mustCBOR(t, h, "PutCompositeAlarm", smithycbor.Map{
		"AlarmName":    s("release-at-risk"),
		"AlarmRule":    s(`ALARM("orders-cpu") AND NOT OK(fleet)`),
		"AlarmActions": strs("arn:aws:sns:us-east-1:000000000000:app"),
	})
	out := mustCBOR(t, h, "DescribeAlarms", smithycbor.Map{"AlarmTypes": strs("CompositeAlarm")})
	comps := out["CompositeAlarms"].(smithycbor.List)
	if len(comps) != 1 || len(out["MetricAlarms"].(smithycbor.List)) != 0 {
		t.Fatalf("describe composite: %v", out)
	}
	mustCBOR(t, h, "SetAlarmState", smithycbor.Map{"AlarmName": s("orders-cpu"), "StateValue": s("ALARM"), "StateReason": s("test")})
	out = mustCBOR(t, h, "DescribeAlarms", smithycbor.Map{"AlarmNames": strs("release-at-risk"), "AlarmTypes": strs("CompositeAlarm")})
	if st := out["CompositeAlarms"].(smithycbor.List)[0].(smithycbor.Map)["StateValue"]; st != s("ALARM") {
		t.Fatalf("composite state: %v", st)
	}
	var app int
	for _, m := range n.msgs {
		if strings.HasSuffix(m.topic, ":app") {
			app++
		}
	}
	if app != 1 {
		t.Fatalf("composite notifications: %+v", n.msgs)
	}
	if code, _ := cborCall(t, h, "PutCompositeAlarm", smithycbor.Map{"AlarmName": s("bad"), "AlarmRule": s("ALARM(")}); code != 400 {
		t.Fatalf("bad rule accepted")
	}
}

func TestQueryAndJSONProtocols(t *testing.T) {
	h, _ := newTestHandler(t)
	form := url.Values{
		"Action": {"PutMetricAlarm"}, "Version": {APIVersion},
		"AlarmName": {"q-alarm"}, "MetricName": {"Errors"}, "Namespace": {"App"},
		"Statistic": {"Sum"}, "Period": {"300"}, "EvaluationPeriods": {"2"}, "Threshold": {"5"},
		"ComparisonOperator":        {"GreaterThanOrEqualToThreshold"},
		"Dimensions.member.1.Name":  {"Service"},
		"Dimensions.member.1.Value": {"api"},
		"AlarmActions.member.1":     {"arn:aws:sns:us-east-1:000000000000:t"},
		"Tags.member.1.Key":         {"k"},
		"Tags.member.1.Value":       {"v"},
	}
	req := httptest.NewRequest("POST", "/", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	h.Dispatch(rec, req)
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), "<PutMetricAlarmResponse") {
		t.Fatalf("query put: %d %s", rec.Code, rec.Body.String())
	}

	req = httptest.NewRequest("POST", "/", strings.NewReader(`{"AlarmNames":["q-alarm"]}`))
	req.Header.Set("X-Amz-Target", "GraniteServiceVersion20100801.DescribeAlarms")
	req.Header.Set("Content-Type", "application/x-amz-json-1.0")
	rec = httptest.NewRecorder()
	h.Dispatch(rec, req)
	var out struct {
		MetricAlarms []struct {
			EvaluationPeriods     int
			Threshold             float64
			Dimensions            []map[string]string
			AlarmActions          []string
			StateUpdatedTimestamp float64
		}
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil || len(out.MetricAlarms) != 1 {
		t.Fatalf("json describe: %v %s", err, rec.Body.String())
	}
	a := out.MetricAlarms[0]
	if a.EvaluationPeriods != 2 || a.Threshold != 5 || a.Dimensions[0]["Value"] != "api" || len(a.AlarmActions) != 1 || a.StateUpdatedTimestamp == 0 {
		t.Fatalf("json alarm: %+v", a)
	}

	form = url.Values{"Action": {"DescribeAlarms"}, "Version": {APIVersion}, "AlarmNames.member.1": {"q-alarm"}}
	req = httptest.NewRequest("POST", "/", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec = httptest.NewRecorder()
	h.Dispatch(rec, req)
	body := rec.Body.String()
	for _, want := range []string{"<MetricAlarms><member>", "<AlarmName>q-alarm</AlarmName>", "<Threshold>5</Threshold>", "<Dimensions><member><Name>Service</Name>"} {
		if !strings.Contains(body, want) {
			t.Fatalf("query describe missing %s: %s", want, body)
		}
	}
}
