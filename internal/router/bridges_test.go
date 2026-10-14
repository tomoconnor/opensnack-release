// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package router_test

import (
	"archive/zip"
	"bytes"
	"encoding/base64"
	"encoding/json"
	"net/http/httptest"
	"os/exec"
	"strings"
	"testing"
	"time"

	"opensnack/internal/resource"
	"opensnack/internal/router"
)

// End to end across services: API Gateway and EventBridge invoke a Lambda
// function, whose output lands in CloudWatch Logs.
func TestBridges_LambdaFromAPIGatewayAndEventBridge(t *testing.T) {
	if _, err := exec.LookPath("python3"); err != nil {
		t.Skip("python3 not available for the local Lambda executor")
	}
	t.Setenv("OPENSNACK_LAMBDA_EXECUTOR", "local")
	t.Setenv("OPENSNACK_OBJECT_ROOT", t.TempDir())
	h := router.New(resource.NewMemStore())

	call := func(method, host, path, target string, body any) (int, []byte) {
		t.Helper()
		var b []byte
		switch v := body.(type) {
		case string:
			b = []byte(v)
		case nil:
		default:
			b, _ = json.Marshal(v)
		}
		req := httptest.NewRequest(method, path, bytes.NewReader(b))
		req.Host = host
		req.Header.Set("User-Agent", "test custom-team")
		if target != "" {
			req.Header.Set("X-Amz-Target", target)
			req.Header.Set("Content-Type", "application/x-amz-json-1.1")
		}
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return rec.Code, rec.Body.Bytes()
	}

	// A function that logs what triggered it and answers in proxy format.
	var zipBuf bytes.Buffer
	zw := zip.NewWriter(&zipBuf)
	f, _ := zw.Create("index.py")
	f.Write([]byte(`import json
def handler(event, context):
    who = event.get("source") or event.get("rawPath")
    print("triggered by", who)
    return {"statusCode": 200, "body": "hello from " + str(who)}
`))
	zw.Close()
	code, out := call("POST", "lambda.us-east-1.amazonaws.com", "/2015-03-31/functions", "", map[string]any{
		"FunctionName": "app", "Runtime": "python3.12", "Handler": "index.handler",
		"Role": "arn:aws:iam::000000000000:role/r", "Code": map[string]any{"ZipFile": base64.StdEncoding.EncodeToString(zipBuf.Bytes())},
	})
	if code != 201 {
		t.Fatalf("CreateFunction: %d %s", code, out)
	}
	var fn struct{ FunctionArn string }
	json.Unmarshal(out, &fn)

	// API Gateway quick create: one $default route proxying to the function.
	code, out = call("POST", "apigateway.us-east-1.amazonaws.com", "/v2/apis", "", map[string]any{
		"name": "track-app", "protocolType": "HTTP", "target": fn.FunctionArn,
	})
	if code != 201 {
		t.Fatalf("CreateApi: %d %s", code, out)
	}
	var api struct {
		ApiId string `json:"apiId"`
	}
	json.Unmarshal(out, &api)

	code, out = call("GET", api.ApiId+".execute-api.us-east-1.amazonaws.com", "/where", "", nil)
	if code != 200 || string(out) != "hello from /where" {
		t.Fatalf("execute-api: %d %s", code, out)
	}

	// EventBridge rule targeting the function.
	if code, out := call("POST", "events.us-east-1.amazonaws.com", "/", "AWSEvents.PutRule",
		map[string]any{"Name": "fleet", "EventPattern": `{"source":["example.orders"]}`}); code != 200 {
		t.Fatalf("PutRule: %d %s", code, out)
	}
	if code, out := call("POST", "events.us-east-1.amazonaws.com", "/", "AWSEvents.PutTargets",
		map[string]any{"Rule": "fleet", "Targets": []any{map[string]any{"Id": "app", "Arn": fn.FunctionArn}}}); code != 200 {
		t.Fatalf("PutTargets: %d %s", code, out)
	}
	if code, out := call("POST", "events.us-east-1.amazonaws.com", "/", "AWSEvents.PutEvents",
		map[string]any{"Entries": []any{map[string]any{"Source": "example.orders", "DetailType": "launch", "Detail": "{}"}}}); code != 200 {
		t.Fatalf("PutEvents: %d %s", code, out)
	}

	// Both invocations' output reaches /aws/lambda/app. The EventBridge one
	// is asynchronous, so poll briefly.
	want := []string{"triggered by /where", "triggered by example.orders"}
	deadline := time.Now().Add(10 * time.Second)
	for {
		code, out = call("POST", "logs.us-east-1.amazonaws.com", "/", "Logs_20140328.FilterLogEvents",
			map[string]any{"logGroupName": "/aws/lambda/app"})
		missing := ""
		for _, w := range want {
			if !strings.Contains(string(out), w) {
				missing = w
			}
		}
		if code == 200 && missing == "" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("logs never showed %q: %d %s", missing, code, out)
		}
		time.Sleep(100 * time.Millisecond)
	}
}

// SNS subscriptions and SQS event source mappings invoke Lambda; a failing
// SQS batch is retried and ends up in the DLQ.
func TestBridges_LambdaFromSNSAndSQS(t *testing.T) {
	if _, err := exec.LookPath("python3"); err != nil {
		t.Skip("python3 not available for the local Lambda executor")
	}
	t.Setenv("OPENSNACK_LAMBDA_EXECUTOR", "local")
	t.Setenv("OPENSNACK_OBJECT_ROOT", t.TempDir())
	srv := router.NewServer(resource.NewMemStore(), router.Config{})

	call := func(host, path, target, contentType string, body any) (int, []byte) {
		t.Helper()
		var b []byte
		if s, ok := body.(string); ok {
			b = []byte(s)
		} else if body != nil {
			b, _ = json.Marshal(body)
		}
		req := httptest.NewRequest("POST", path, bytes.NewReader(b))
		req.Host = host
		req.Header.Set("User-Agent", "test custom-team")
		req.Header.Set("Content-Type", contentType)
		if target != "" {
			req.Header.Set("X-Amz-Target", target)
		}
		rec := httptest.NewRecorder()
		srv.ServeHTTP(rec, req)
		return rec.Code, rec.Body.Bytes()
	}
	sqsCall := func(op string, body any) map[string]any {
		t.Helper()
		code, out := call("sqs.us-east-1.amazonaws.com", "/", "AmazonSQS."+op, "application/x-amz-json-1.0", body)
		if code != 200 {
			t.Fatalf("%s: %d %s", op, code, out)
		}
		var m map[string]any
		json.Unmarshal(out, &m)
		return m
	}

	var zipBuf bytes.Buffer
	zw := zip.NewWriter(&zipBuf)
	f, _ := zw.Create("index.py")
	f.Write([]byte(`def handler(event, context):
    for r in event["Records"]:
        if r.get("EventSource") == "aws:sns":
            print("sns says", r["Sns"]["Message"])
        else:
            if r["body"] == "boom":
                raise Exception("bad input")
            print("sqs says", r["body"])
`))
	zw.Close()
	code, out := call("lambda.us-east-1.amazonaws.com", "/2015-03-31/functions", "", "application/json", map[string]any{
		"FunctionName": "worker", "Runtime": "python3.12", "Handler": "index.handler",
		"Role": "arn:aws:iam::000000000000:role/r", "Code": map[string]any{"ZipFile": base64.StdEncoding.EncodeToString(zipBuf.Bytes())},
	})
	if code != 201 {
		t.Fatalf("CreateFunction: %d %s", code, out)
	}
	var fn struct{ FunctionArn string }
	json.Unmarshal(out, &fn)

	// SNS → Lambda.
	code, out = call("sns.us-east-1.amazonaws.com", "/", "", "application/x-www-form-urlencoded", "Action=CreateTopic&Name=requests&Version=2010-03-31")
	if code != 200 {
		t.Fatalf("CreateTopic: %d %s", code, out)
	}
	topic := "arn:aws:sns:us-east-1:000000000000:requests"
	call("sns.us-east-1.amazonaws.com", "/", "", "application/x-www-form-urlencoded",
		"Action=Subscribe&TopicArn="+topic+"&Protocol=lambda&Endpoint="+fn.FunctionArn+"&Version=2010-03-31")
	call("sns.us-east-1.amazonaws.com", "/", "", "application/x-www-form-urlencoded",
		"Action=Publish&TopicArn="+topic+"&Message=ping&Version=2010-03-31")

	// SQS → Lambda through an event source mapping, with a DLQ after 2 tries.
	sqsCall("CreateQueue", map[string]any{"QueueName": "failed"})
	sqsCall("CreateQueue", map[string]any{"QueueName": "jobs", "Attributes": map[string]string{
		"VisibilityTimeout": "0",
		"RedrivePolicy":     `{"deadLetterTargetArn":"arn:aws:sqs:us-east-1:000000000000:failed","maxReceiveCount":"2"}`,
	}})
	code, out = call("lambda.us-east-1.amazonaws.com", "/2015-03-31/event-source-mappings", "", "application/json", map[string]any{
		"FunctionName": "worker", "EventSourceArn": "arn:aws:sqs:us-east-1:000000000000:jobs", "BatchSize": 1,
	})
	if code != 202 {
		t.Fatalf("CreateEventSourceMapping: %d %s", code, out)
	}
	jobs := "https://sqs.us-east-1.amazonaws.com/000000000000/jobs"
	sqsCall("SendMessage", map[string]any{"QueueUrl": jobs, "MessageBody": "widget-a"})
	sqsCall("SendMessage", map[string]any{"QueueUrl": jobs, "MessageBody": "boom"})

	for range 6 {
		srv.ESM.Poll(t.Context())
	}

	attrs := sqsCall("GetQueueAttributes", map[string]any{"QueueUrl": jobs, "AttributeNames": []string{"All"}})["Attributes"].(map[string]any)
	if attrs["ApproximateNumberOfMessages"] != "0" || attrs["ApproximateNumberOfMessagesNotVisible"] != "0" {
		t.Fatalf("jobs not drained: %v", attrs)
	}
	got := sqsCall("ReceiveMessage", map[string]any{"QueueUrl": "https://sqs.us-east-1.amazonaws.com/000000000000/failed"})
	if msgs, _ := got["Messages"].([]any); len(msgs) != 1 || msgs[0].(map[string]any)["Body"] != "boom" {
		t.Fatalf("DLQ: %v", got)
	}

	want := []string{"sns says ping", "sqs says widget-a"}
	deadline := time.Now().Add(10 * time.Second)
	for {
		code, out = call("logs.us-east-1.amazonaws.com", "/", "Logs_20140328.FilterLogEvents", "application/x-amz-json-1.1",
			map[string]any{"logGroupName": "/aws/lambda/worker"})
		missing := ""
		for _, w := range want {
			if !strings.Contains(string(out), w) {
				missing = w
			}
		}
		if missing == "" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("logs never showed %q: %d %s", missing, code, out)
		}
		time.Sleep(100 * time.Millisecond)
	}
}
