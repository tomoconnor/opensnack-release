// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

//go:build k8sintegration

// Package k8sit tests the Kubernetes executor end to end, against opensnack
// deployed in a cluster with deploy/k8s/lambda-it (see run.sh there):
//
//	OPENSNACK_IT_ENDPOINT    opensnack's AWS API, e.g. http://localhost:14566
//	OPENSNACK_IT_KUBECONFIG  the test cluster's kubeconfig, to inspect pods
package k8sit

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	v4 "github.com/aws/aws-sdk-go-v2/aws/signer/v4"

	"opensnack/internal/kube"
)

const region = "us-east-1"

type tenant struct {
	namespace, key, secret, account string
}

var (
	tenantA = tenant{"it-tenant-a", "AKIAITTENANTA0000001", "tenant-a-secret", "111111111111"}
	tenantB = tenant{"it-tenant-b", "AKIAITTENANTB0000001", "tenant-b-secret", "222222222222"}
)

type env struct {
	t        *testing.T
	endpoint string
	pods     kube.Pods
	http     *http.Client
}

func setup(t *testing.T) *env {
	endpoint, kc := os.Getenv("OPENSNACK_IT_ENDPOINT"), os.Getenv("OPENSNACK_IT_KUBECONFIG")
	if endpoint == "" || kc == "" {
		t.Skip("set OPENSNACK_IT_ENDPOINT and OPENSNACK_IT_KUBECONFIG (see deploy/k8s/lambda-it/run.sh)")
	}
	cfg, err := kube.FromKubeconfig(kc, "")
	if err != nil {
		t.Fatal(err)
	}
	e := &env{t: t, endpoint: endpoint, pods: kube.NewClient(cfg, "opensnack-lambda"), http: &http.Client{Timeout: 2 * time.Minute}}
	// Start from empty tenants, so the tests can run again on a kept cluster.
	e.deleteNamespace(tenantA)
	e.deleteNamespace(tenantB)
	return e
}

// deleteNamespace empties a tenant through the inspection API.
func (e *env) deleteNamespace(who tenant) {
	e.t.Helper()
	token := os.Getenv("OPENSNACK_IT_INTERNAL_TOKEN")
	if token == "" {
		token = "it-internal-token"
	}
	req, _ := http.NewRequest("DELETE", e.endpoint+"/_opensnack/v1/namespaces/"+who.namespace, nil)
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := e.http.Do(req)
	if err != nil {
		e.t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode >= 300 {
		e.t.Fatalf("deleting namespace %s: %s", who.namespace, resp.Status)
	}
}

// call sends a SigV4-signed request as the tenant.
func (e *env) call(who tenant, service, method, path string, hdr map[string]string, body []byte) (int, http.Header, []byte) {
	e.t.Helper()
	req, _ := http.NewRequest(method, e.endpoint+path, bytes.NewReader(body))
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	sum := sha256.Sum256(body)
	creds := aws.Credentials{AccessKeyID: who.key, SecretAccessKey: who.secret}
	if err := v4.NewSigner().SignHTTP(context.Background(), creds, req, hex.EncodeToString(sum[:]), service, region, time.Now()); err != nil {
		e.t.Fatal(err)
	}
	resp, err := e.http.Do(req)
	if err != nil {
		e.t.Fatalf("%s %s: %v", method, path, err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, resp.Header, b
}

func (e *env) json(who tenant, service, method, path string, in any, want int) map[string]any {
	e.t.Helper()
	var body []byte
	if in != nil {
		body, _ = json.Marshal(in)
	}
	status, _, out := e.call(who, service, method, path, map[string]string{"Content-Type": "application/json"}, body)
	if status != want {
		e.t.Fatalf("%s %s %s: %d %s", service, method, path, status, out)
	}
	m := map[string]any{}
	json.Unmarshal(out, &m)
	return m
}

// target calls a JSON-protocol (X-Amz-Target) API.
func (e *env) target(who tenant, service, target string, in any) (int, map[string]any) {
	e.t.Helper()
	body, _ := json.Marshal(in)
	status, _, out := e.call(who, service, "POST", "/", map[string]string{"X-Amz-Target": target, "Content-Type": "application/x-amz-json-1.0"}, body)
	m := map[string]any{}
	json.Unmarshal(out, &m)
	return status, m
}

// query calls a Query-protocol API.
func (e *env) query(who tenant, service string, params url.Values) string {
	e.t.Helper()
	status, _, out := e.call(who, service, "POST", "/", map[string]string{"Content-Type": "application/x-www-form-urlencoded"}, []byte(params.Encode()))
	if status != 200 {
		e.t.Fatalf("%s %s: %d %s", service, params.Get("Action"), status, out)
	}
	return string(out)
}

func zipOf(files map[string]string) []byte {
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for name, content := range files {
		w, _ := zw.Create(name)
		io.WriteString(w, content)
	}
	zw.Close()
	return buf.Bytes()
}

func roleARN(who tenant) string { return "arn:aws:iam::" + who.account + ":role/fn-role" }

func fnARN(who tenant, name string) string {
	return "arn:aws:lambda:" + region + ":" + who.account + ":function:" + name
}

func (e *env) createRole(who tenant) {
	e.query(who, "iam", url.Values{"Action": {"CreateRole"}, "Version": {"2010-05-08"}, "RoleName": {"fn-role"},
		"AssumeRolePolicyDocument": {`{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Principal":{"Service":"lambda.amazonaws.com"},"Action":"sts:AssumeRole"}]}`}})
}

func (e *env) createFunction(who tenant, name, runtime, handler string, code []byte, extra map[string]any) {
	e.t.Helper()
	in := map[string]any{"FunctionName": name, "Role": roleARN(who), "Runtime": runtime, "Handler": handler,
		"Code": map[string]any{"ZipFile": code}, "Timeout": 10}
	for k, v := range extra {
		in[k] = v
	}
	e.json(who, "lambda", "POST", "/2015-03-31/functions", in, 201)
}

type result struct {
	status        int
	functionError string
	payload       []byte
	logs          string
	took          time.Duration
}

func (e *env) invoke(who tenant, name, payload string) result {
	e.t.Helper()
	start := time.Now()
	status, hdr, body := e.call(who, "lambda", "POST", "/2015-03-31/functions/"+name+"/invocations",
		map[string]string{"X-Amz-Log-Type": "Tail", "Content-Type": "application/json"}, []byte(payload))
	logs, _ := base64.StdEncoding.DecodeString(hdr.Get("X-Amz-Log-Result"))
	return result{status, hdr.Get("X-Amz-Function-Error"), body, string(logs), time.Since(start)}
}

func (r result) doc() map[string]any {
	m := map[string]any{}
	json.Unmarshal(r.payload, &m)
	return m
}

func functionLabel(who tenant, name string) string {
	h := sha256.New()
	for _, p := range []string{"function", who.namespace, region, name} {
		h.Write([]byte(p))
		h.Write([]byte{0})
	}
	return hex.EncodeToString(h.Sum(nil))[:16]
}

// livePods lists a function's pods that aren't being deleted.
func (e *env) livePods(who tenant, name string) []kube.Pod {
	e.t.Helper()
	pods, err := e.pods.List(context.Background(), "opensnack/function="+functionLabel(who, name))
	if err != nil {
		e.t.Fatal(err)
	}
	var out []kube.Pod
	for _, p := range pods {
		if p.Metadata.DeletionTimestamp == nil && p.Status.Phase != kube.PodFailed && p.Status.Phase != kube.PodSucceeded {
			out = append(out, p)
		}
	}
	return out
}

func eventually(t *testing.T, timeout time.Duration, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(500 * time.Millisecond)
	}
}

const pyEcho = `import json, os

def handler(event, context):
    print("hello from python", json.dumps(event))
    return {
        "echo": event,
        "function": context.function_name,
        "region": os.environ["AWS_REGION"],
        "key_prefix": os.environ.get("AWS_ACCESS_KEY_ID", "")[:4],
        "remaining_ok": context.get_remaining_time_in_millis() > 0,
    }
`

const pyMisc = `import time

def fail(event, context):
    raise ValueError("bad input: " + str(event.get("n")))

def slow(event, context):
    time.sleep(event.get("sleep", 30))
    return "finished"

def hog(event, context):
    chunks = []
    for _ in range(64):
        chunks.append(b"x" * (16 << 20))
    return len(chunks)
`

const pyWriter = `import os
import boto3

ddb = boto3.client("dynamodb")
sts = boto3.client("sts")

def handler(event, context):
    caller = sts.get_caller_identity()["Arn"]
    for r in event.get("Records", []):
        if r.get("eventSource") == "aws:sqs":
            body, source = r["body"], "sqs"
        else:
            body, source = r["Sns"]["Message"], "sns"
        ddb.put_item(TableName=os.environ["TABLE"], Item={"id": {"S": body}, "source": {"S": source}, "caller": {"S": caller}})
    return {"ok": True}
`

const pyHTTP = `import json

def handler(event, context):
    return {"statusCode": 200, "headers": {"Content-Type": "application/json"},
            "body": json.dumps({"path": event["rawPath"], "method": event["requestContext"]["http"]["method"]})}
`

const pyEscape = `import json, os, socket, urllib.request
import boto3

def attempt(fn):
    try:
        return {"blocked": False, "detail": str(fn())[:300]}
    except Exception as e:
        return {"blocked": True, "detail": (type(e).__name__ + ": " + str(e))[:300]}

def handler(event, context):
    api = os.environ["AWS_LAMBDA_RUNTIME_API"]

    def runtime(token):
        req = urllib.request.Request("http://%s/2018-06-01/runtime/invocation/next" % api,
                                     headers={"X-Opensnack-Runtime-Token": token})
        return urllib.request.urlopen(req, timeout=5).status

    def connect(host, port):
        s = socket.create_connection((host, port), timeout=3)
        s.close()
        return "connected to %s:%d" % (host, port)

    ddb = boto3.client("dynamodb")
    return {
        "internet_ip": attempt(lambda: urllib.request.urlopen("http://1.1.1.1/", timeout=3).status),
        "internet_name": attempt(lambda: urllib.request.urlopen("https://example.com/", timeout=3).status),
        "kubernetes_api": attempt(lambda: connect("kubernetes.default.svc", 443)),
        "runtime_stolen_token": attempt(lambda: runtime(event["stolen_token"])),
        "runtime_guessed_token": attempt(lambda: runtime(event["stolen_token"].split(".")[0] + ".00")),
        "other_table": attempt(lambda: ddb.get_item(TableName=event["victim_table"], Key={"id": {"S": "secret"}})),
        "other_tables_listed": event["victim_table"] in ddb.list_tables()["TableNames"],
        "other_role": attempt(lambda: boto3.client("sts").assume_role(RoleArn=event["victim_role"], RoleSessionName="escape")),
    }
`

const nodeEcho = `exports.handler = async (event, context) => {
  console.log("hello from node", JSON.stringify(event));
  return { echo: event, function: context.functionName, node: process.version };
};
`

func TestKubernetesExecutor(t *testing.T) {
	e := setup(t)
	e.createRole(tenantA)
	e.createRole(tenantB)

	e.createFunction(tenantA, "py-echo", "python3.12", "app.handler", zipOf(map[string]string{"app.py": pyEcho}), nil)
	e.createFunction(tenantA, "node-echo", "nodejs22.x", "index.handler", zipOf(map[string]string{"index.js": nodeEcho}), nil)
	misc := zipOf(map[string]string{"misc.py": pyMisc})
	e.createFunction(tenantA, "py-fail", "python3.12", "misc.fail", misc, nil)
	e.createFunction(tenantA, "py-slow", "python3.12", "misc.slow", misc, map[string]any{"Timeout": 2})
	e.createFunction(tenantA, "py-hog", "python3.12", "misc.hog", misc, map[string]any{"MemorySize": 128})

	t.Run("python payload, cold then warm", func(t *testing.T) {
		cold := e.invoke(tenantA, "py-echo", `{"n":1}`)
		if cold.status != 200 || cold.functionError != "" {
			t.Fatalf("cold: %d %s %s", cold.status, cold.functionError, cold.payload)
		}
		d := cold.doc()
		if d["function"] != "py-echo" || d["region"] != region || d["key_prefix"] != "ASIA" || d["remaining_ok"] != true {
			t.Fatalf("payload: %s", cold.payload)
		}
		if !strings.Contains(cold.logs, "hello from python") || !strings.Contains(cold.logs, "REPORT RequestId:") {
			t.Fatalf("logs: %q", cold.logs)
		}
		var warm []time.Duration
		for i := 0; i < 5; i++ {
			r := e.invoke(tenantA, "py-echo", fmt.Sprintf(`{"n":%d}`, i+2))
			if r.status != 200 || r.functionError != "" {
				t.Fatalf("warm: %d %s", r.status, r.payload)
			}
			warm = append(warm, r.took)
		}
		t.Logf("python cold start invoke: %s; warm invokes: %v", cold.took, warm)
		if warm[0]*2 > cold.took {
			t.Errorf("warm invoke (%s) not much faster than cold (%s)", warm[0], cold.took)
		}
		if n := len(e.livePods(tenantA, "py-echo")); n != 1 {
			t.Errorf("py-echo has %d pods, want 1 warm pod", n)
		}
	})

	t.Run("node payload", func(t *testing.T) {
		cold := e.invoke(tenantA, "node-echo", `{"hello":"node"}`)
		if cold.status != 200 || cold.functionError != "" || cold.doc()["function"] != "node-echo" {
			t.Fatalf("node: %d %s %s", cold.status, cold.functionError, cold.payload)
		}
		warm := e.invoke(tenantA, "node-echo", `{"hello":"again"}`)
		t.Logf("node cold start invoke: %s; warm: %s", cold.took, warm.took)
		if !strings.Contains(cold.logs, "hello from node") {
			t.Errorf("logs: %q", cold.logs)
		}
	})

	t.Run("function error", func(t *testing.T) {
		r := e.invoke(tenantA, "py-fail", `{"n":7}`)
		d := r.doc()
		if r.status != 200 || r.functionError != "Unhandled" || d["errorType"] != "ValueError" || d["errorMessage"] != "bad input: 7" {
			t.Fatalf("error: %d %q %s", r.status, r.functionError, r.payload)
		}
		if _, ok := d["stackTrace"]; !ok {
			t.Errorf("no stackTrace in %s", r.payload)
		}
	})

	t.Run("timeout kills and replaces the pod", func(t *testing.T) {
		r := e.invoke(tenantA, "py-slow", `{"sleep":30}`)
		d := r.doc()
		if r.functionError != "Unhandled" || d["errorType"] != "Sandbox.Timedout" ||
			!strings.Contains(fmt.Sprint(d["errorMessage"]), "Task timed out after 2.00 seconds") {
			t.Fatalf("timeout: %q %s", r.functionError, r.payload)
		}
		eventually(t, 30*time.Second, "the timed-out pod to go", func() bool { return len(e.livePods(tenantA, "py-slow")) == 0 })
		r = e.invoke(tenantA, "py-slow", `{"sleep":0}`)
		if r.functionError != "" || string(r.payload) != `"finished"` {
			t.Fatalf("after timeout: %q %s", r.functionError, r.payload)
		}
	})

	t.Run("memory over the limit", func(t *testing.T) {
		r := e.invoke(tenantA, "py-hog", `{}`)
		d := r.doc()
		if r.status != 200 || r.functionError != "Unhandled" || d["errorType"] != "Runtime.ExitError" {
			t.Fatalf("OOM: %d %q %s", r.status, r.functionError, r.payload)
		}
		t.Logf("OOM error: %s", r.payload)
		if r := e.invoke(tenantA, "py-echo", `{"after":"oom"}`); r.status != 200 || r.functionError != "" {
			t.Fatalf("executor unhealthy after OOM: %d %s", r.status, r.payload)
		}
	})

	t.Run("event sources", func(t *testing.T) {
		if status, out := e.target(tenantA, "dynamodb", "DynamoDB_20120810.CreateTable", map[string]any{
			"TableName": "items", "BillingMode": "PAY_PER_REQUEST",
			"AttributeDefinitions": []any{map[string]any{"AttributeName": "id", "AttributeType": "S"}},
			"KeySchema":            []any{map[string]any{"AttributeName": "id", "KeyType": "HASH"}},
		}); status != 200 {
			t.Fatalf("create table: %d %v", status, out)
		}
		e.createFunction(tenantA, "py-writer", "python3.12", "app.handler", zipOf(map[string]string{"app.py": pyWriter}),
			map[string]any{"Environment": map[string]any{"Variables": map[string]string{"TABLE": "items"}}, "Timeout": 30})

		item := func(id string) map[string]any {
			_, out := e.target(tenantA, "dynamodb", "DynamoDB_20120810.GetItem", map[string]any{
				"TableName": "items", "Key": map[string]any{"id": map[string]string{"S": id}}})
			it, _ := out["Item"].(map[string]any)
			return it
		}
		attr := func(it map[string]any, k string) string {
			v, _ := it[k].(map[string]any)
			s, _ := v["S"].(string)
			return s
		}

		// SQS → Lambda through an event source mapping.
		status, out := e.target(tenantA, "sqs", "AmazonSQS.CreateQueue", map[string]any{"QueueName": "jobs"})
		if status != 200 {
			t.Fatalf("create queue: %d %v", status, out)
		}
		queueURL := out["QueueUrl"].(string)
		e.json(tenantA, "lambda", "POST", "/2015-03-31/event-source-mappings", map[string]any{
			"EventSourceArn": "arn:aws:sqs:" + region + ":" + tenantA.account + ":jobs", "FunctionName": "py-writer", "BatchSize": 1}, 202)
		if status, out := e.target(tenantA, "sqs", "AmazonSQS.SendMessage", map[string]any{"QueueUrl": queueURL, "MessageBody": "from-sqs"}); status != 200 {
			t.Fatalf("send: %d %v", status, out)
		}
		eventually(t, 90*time.Second, "the SQS message to reach DynamoDB", func() bool { return item("from-sqs") != nil })
		it := item("from-sqs")
		if attr(it, "source") != "sqs" || !strings.Contains(attr(it, "caller"), ":assumed-role/fn-role/") ||
			!strings.Contains(attr(it, "caller"), tenantA.account) {
			t.Fatalf("item written with the wrong identity: %v", it)
		}

		// SNS → Lambda.
		topic := e.query(tenantA, "sns", url.Values{"Action": {"CreateTopic"}, "Name": {"events"}})
		topicARN := between(topic, "<TopicArn>", "</TopicArn>")
		e.query(tenantA, "sns", url.Values{"Action": {"Subscribe"}, "TopicArn": {topicARN}, "Protocol": {"lambda"}, "Endpoint": {fnARN(tenantA, "py-writer")}})
		e.query(tenantA, "sns", url.Values{"Action": {"Publish"}, "TopicArn": {topicARN}, "Message": {"from-sns"}})
		eventually(t, 60*time.Second, "the SNS message to reach DynamoDB", func() bool { return item("from-sns") != nil })
		if it := item("from-sns"); attr(it, "source") != "sns" {
			t.Fatalf("SNS item: %v", it)
		}
	})

	t.Run("API Gateway", func(t *testing.T) {
		e.createFunction(tenantA, "py-http", "python3.12", "app.handler", zipOf(map[string]string{"app.py": pyHTTP}), nil)
		api := e.json(tenantA, "apigateway", "POST", "/v2/apis", map[string]any{"name": "it", "protocolType": "HTTP"}, 201)
		apiID := api["apiId"].(string)
		integ := e.json(tenantA, "apigateway", "POST", "/v2/apis/"+apiID+"/integrations", map[string]any{
			"integrationType": "AWS_PROXY", "integrationUri": fnARN(tenantA, "py-http"), "payloadFormatVersion": "2.0"}, 201)
		e.json(tenantA, "apigateway", "POST", "/v2/apis/"+apiID+"/routes", map[string]any{
			"routeKey": "GET /hello", "target": "integrations/" + integ["integrationId"].(string)}, 201)
		e.json(tenantA, "apigateway", "POST", "/v2/apis/"+apiID+"/stages", map[string]any{"stageName": "$default", "autoDeploy": true}, 201)

		req, _ := http.NewRequest("GET", e.endpoint+"/hello", nil)
		req.Host = apiID + ".execute-api." + region + ".amazonaws.com"
		start := time.Now()
		resp, err := e.http.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.StatusCode != 200 || !strings.Contains(string(body), `"path": "/hello"`) {
			t.Fatalf("execute-api: %d %s", resp.StatusCode, body)
		}
		t.Logf("API Gateway → Lambda, cold: %s", time.Since(start))
	})

	t.Run("escape attempts fail", func(t *testing.T) {
		// Tenant B has a table, a role and a warm pod to steal a token from.
		if status, out := e.target(tenantB, "dynamodb", "DynamoDB_20120810.CreateTable", map[string]any{
			"TableName": "secret-b", "BillingMode": "PAY_PER_REQUEST",
			"AttributeDefinitions": []any{map[string]any{"AttributeName": "id", "AttributeType": "S"}},
			"KeySchema":            []any{map[string]any{"AttributeName": "id", "KeyType": "HASH"}},
		}); status != 200 {
			t.Fatalf("create table: %d %v", status, out)
		}
		e.createFunction(tenantB, "victim", "python3.12", "app.handler", zipOf(map[string]string{"app.py": pyEcho}), nil)
		if r := e.invoke(tenantB, "victim", `{}`); r.functionError != "" {
			t.Fatalf("victim: %s", r.payload)
		}
		victims := e.livePods(tenantB, "victim")
		if len(victims) != 1 {
			t.Fatalf("victim pods: %d", len(victims))
		}
		var stolen string
		for _, v := range victims[0].Spec.Containers[0].Env {
			if v.Name == "_OPENSNACK_RUNTIME_TOKEN" {
				stolen = v.Value
			}
		}

		e.createFunction(tenantA, "py-escape", "python3.12", "app.handler", zipOf(map[string]string{"app.py": pyEscape}), map[string]any{"Timeout": 60})
		ev, _ := json.Marshal(map[string]string{"stolen_token": stolen, "victim_table": "secret-b", "victim_role": roleARN(tenantB)})
		r := e.invoke(tenantA, "py-escape", string(ev))
		if r.functionError != "" {
			t.Fatalf("escape function failed: %s", r.payload)
		}
		var report map[string]json.RawMessage
		json.Unmarshal(r.payload, &report)
		t.Logf("escape report: %s", r.payload)
		for _, k := range []string{"internet_ip", "internet_name", "kubernetes_api", "runtime_stolen_token", "runtime_guessed_token", "other_table", "other_role"} {
			var a struct {
				Blocked bool
				Detail  string
			}
			json.Unmarshal(report[k], &a)
			if !a.Blocked {
				t.Errorf("%s was not blocked: %s", k, a.Detail)
			}
			switch k {
			case "runtime_stolen_token", "runtime_guessed_token":
				if !strings.Contains(a.Detail, "403") {
					t.Errorf("%s: want a 403 from the Runtime API, got %s", k, a.Detail)
				}
			case "other_table":
				if !strings.Contains(a.Detail, "ResourceNotFound") {
					t.Errorf("%s: %s", k, a.Detail)
				}
			case "other_role":
				if !strings.Contains(a.Detail, "AccessDenied") {
					t.Errorf("%s: %s", k, a.Detail)
				}
			}
		}
		if string(report["other_tables_listed"]) != "false" {
			t.Errorf("tenant B's table is visible to tenant A's function")
		}
		// The victim still works.
		if r := e.invoke(tenantB, "victim", `{"still":"fine"}`); r.functionError != "" {
			t.Fatalf("victim after the attempt: %s", r.payload)
		}
	})

	t.Run("deleting a namespace kills its pods", func(t *testing.T) {
		if len(e.livePods(tenantB, "victim")) == 0 {
			e.invoke(tenantB, "victim", `{}`)
		}
		e.deleteNamespace(tenantB)
		eventually(t, 30*time.Second, "tenant B's pods to go", func() bool { return len(e.livePods(tenantB, "victim")) == 0 })
	})

	t.Run("deleting a function retires its pods", func(t *testing.T) {
		if len(e.livePods(tenantA, "py-echo")) == 0 {
			e.invoke(tenantA, "py-echo", `{}`)
		}
		status, _, body := e.call(tenantA, "lambda", "DELETE", "/2015-03-31/functions/py-echo", nil, nil)
		if status != 204 {
			t.Fatalf("delete: %d %s", status, body)
		}
		eventually(t, 30*time.Second, "py-echo's pods to go", func() bool { return len(e.livePods(tenantA, "py-echo")) == 0 })
	})
}

func between(s, a, b string) string {
	_, rest, _ := strings.Cut(s, a)
	v, _, _ := strings.Cut(rest, b)
	return v
}
