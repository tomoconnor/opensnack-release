// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package events

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"testing"

	"opensnack/internal/util"
)

func TestSQSDeliverer_UsesJSONAPIWithIdentity(t *testing.T) {
	var calls []string
	var sent map[string]any
	var ids []*util.Identity
	h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls = append(calls, r.Header.Get("X-Amz-Target"))
		ids = append(ids, util.IdentityFrom(r))
		body, _ := io.ReadAll(r.Body)
		var in map[string]any
		json.Unmarshal(body, &in)
		switch r.Header.Get("X-Amz-Target") {
		case "AmazonSQS.GetQueueUrl":
			if in["QueueName"] != "jobs.fifo" {
				w.WriteHeader(400)
				return
			}
			w.Write([]byte(`{"QueueUrl":"https://sqs.eu-west-1.amazonaws.com/111122223333/jobs.fifo"}`))
		case "AmazonSQS.SendMessage":
			sent = in
			w.Write([]byte(`{"MessageId":"m"}`))
		}
	})
	group := "g1"
	tgt := Target{Id: "t", Arn: "arn:aws:sqs:eu-west-1:111122223333:jobs.fifo"}
	tgt.SqsParameters = &struct {
		MessageGroupId *string `json:"MessageGroupId,omitempty"`
	}{&group}
	err := SQSDeliverer{Handler: h}.Deliver(context.Background(),
		Scope{Namespace: "team", Region: "us-east-1", AccountID: "111122223333"},
		Delivery{TargetArn: tgt.Arn, Payload: []byte(`{"a":1}`), Target: tgt, EventID: "ev-1"})
	if err != nil {
		t.Fatal(err)
	}
	if len(calls) != 2 || calls[0] != "AmazonSQS.GetQueueUrl" || calls[1] != "AmazonSQS.SendMessage" {
		t.Fatalf("calls = %v", calls)
	}
	if ids[0].Namespace != "team" || ids[0].Region != "eu-west-1" || ids[0].AccountID != "111122223333" {
		t.Fatalf("identity = %+v", ids[0])
	}
	if sent["MessageBody"] != `{"a":1}` || sent["MessageGroupId"] != "g1" || sent["MessageDeduplicationId"] != "ev-1" ||
		sent["QueueUrl"] != "https://sqs.eu-west-1.amazonaws.com/111122223333/jobs.fifo" {
		t.Fatalf("SendMessage = %v", sent)
	}
}

func TestSQSDeliverer_ReportsErrors(t *testing.T) {
	h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(400)
		w.Write([]byte(`{"__type":"AWS.SimpleQueueService.NonExistentQueue"}`))
	})
	err := SQSDeliverer{Handler: h}.Deliver(context.Background(), Scope{Namespace: "n", Region: "us-east-1"},
		Delivery{TargetArn: "arn:aws:sqs:us-east-1:000000000000:gone"})
	if err == nil {
		t.Fatal("want error")
	}
}

func TestSNSDeliverer_Publishes(t *testing.T) {
	var form map[string][]string
	h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r.ParseForm()
		form = r.PostForm
		w.Write([]byte(`<PublishResponse/>`))
	})
	arn := "arn:aws:sns:us-east-1:000000000000:alerts"
	if err := (SNSDeliverer{Handler: h}).Deliver(context.Background(), Scope{Namespace: "n", Region: "us-east-1"},
		Delivery{TargetArn: arn, Payload: []byte(`"hi"`)}); err != nil {
		t.Fatal(err)
	}
	if form["Action"][0] != "Publish" || form["TopicArn"][0] != arn || form["Message"][0] != `"hi"` {
		t.Fatalf("form = %v", form)
	}
}

func TestTransformer(t *testing.T) {
	event := []byte(`{"id":"e1","detail":{"instance":"i-1","state":"running","n":3,"tags":["a","b"],"q":"say \"hi\""}}`)
	tc := TransformContext{RuleArn: "arn:r", RuleName: "r", IngestionTime: "2026-12-13T06:00:00Z"}
	cases := []struct {
		paths    map[string]string
		template string
		want     string
	}{
		{map[string]string{"i": "$.detail.instance", "s": "$.detail.state"}, `"<i> is in state <s>"`, `"i-1 is in state running"`},
		{map[string]string{"s": "$.detail.state", "n": "$.detail.n"}, `{"state": <s>, "count": <n>}`, `{"state": "running", "count": 3}`},
		{map[string]string{"t": "$.detail.tags"}, `{"tags": <t>, "first": "<t>"}`, `{"tags": ["a","b"], "first": "[\"a\",\"b\"]"}`},
		{map[string]string{"q": "$.detail.q"}, `{"q": "<q>"}`, `{"q": "say \"hi\""}`},
		{map[string]string{"m": "$.detail.missing"}, `{"m": <m>}`, `{"m": null}`},
		{nil, `{"rule": "<aws.events.rule-name>", "at": "<aws.events.event.ingestion-time>"}`, `{"rule": "r", "at": "2026-12-13T06:00:00Z"}`},
		{map[string]string{"d": "$.detail"}, `<d>`, `{"instance":"i-1","state":"running","n":3,"tags":["a","b"],"q":"say \"hi\""}`},
		{nil, `{"left": "<not-a-var>"}`, `{"left": "<not-a-var>"}`},
	}
	for _, c := range cases {
		got, err := targetPayload(Target{InputTransformer: &InputTransformer{InputPathsMap: c.paths, InputTemplate: c.template}}, event, tc)
		if err != nil || string(got) != c.want {
			t.Errorf("template %s: got %s (%v), want %s", c.template, got, err, c.want)
		}
	}
}

func TestJSONPath(t *testing.T) {
	doc := []byte(`{"a":{"b":[{"c":1},{"c":2}]},"x-y":"z"}`)
	for path, want := range map[string]string{
		"$":           `{"a":{"b":[{"c":1},{"c":2}]},"x-y":"z"}`,
		"$.a.b[1].c":  `2`,
		"$['x-y']":    `"z"`,
		"$.a.b[0]":    `{"c":1}`,
		"$.a.missing": ``,
	} {
		v, ok, err := jsonPath(doc, path)
		if err != nil {
			t.Fatalf("%s: %v", path, err)
		}
		got := ""
		if ok {
			got = string(compact(v))
		}
		if got != want {
			t.Errorf("%s = %s, want %s", path, got, want)
		}
	}
	for _, bad := range []string{"a.b", "$.", "$[x]", "$['a"} {
		if _, _, err := jsonPath(doc, bad); err == nil {
			t.Errorf("%s: want error", bad)
		}
	}
}
