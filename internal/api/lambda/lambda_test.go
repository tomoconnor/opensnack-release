// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package lambda_test

import (
	"archive/zip"
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os/exec"
	"strings"
	"testing"

	"opensnack/internal/resource"
	"opensnack/internal/router"
)

const host = "lambda.us-east-1.amazonaws.com"

type client struct {
	t *testing.T
	h http.Handler
}

func newClient(t *testing.T) *client {
	t.Helper()
	t.Setenv("OPENSNACK_OBJECT_ROOT", t.TempDir())
	return &client{t: t, h: router.New(resource.NewMemStore())}
}

// do sends a request and returns the recorder; body may be nil, a string or
// anything JSON-encodable.
func (c *client) do(method, path string, body any, headers ...string) *httptest.ResponseRecorder {
	c.t.Helper()
	var rd io.Reader
	switch b := body.(type) {
	case nil:
	case string:
		rd = strings.NewReader(b)
	default:
		j, _ := json.Marshal(b)
		rd = bytes.NewReader(j)
	}
	req := httptest.NewRequest(method, "http://"+host+path, rd)
	req.Host = host
	for i := 0; i+1 < len(headers); i += 2 {
		req.Header.Set(headers[i], headers[i+1])
	}
	rec := httptest.NewRecorder()
	c.h.ServeHTTP(rec, req)
	return rec
}

func (c *client) ok(rec *httptest.ResponseRecorder, status int) map[string]any {
	c.t.Helper()
	if rec.Code != status {
		c.t.Fatalf("status %d, want %d: %s", rec.Code, status, rec.Body.String())
	}
	out := map[string]any{}
	if rec.Body.Len() > 0 {
		_ = json.Unmarshal(rec.Body.Bytes(), &out)
	}
	return out
}

func (c *client) fails(rec *httptest.ResponseRecorder, status int, code string) {
	c.t.Helper()
	if rec.Code != status || rec.Header().Get("X-Amzn-ErrorType") != code {
		c.t.Fatalf("got %d %q, want %d %s: %s", rec.Code, rec.Header().Get("X-Amzn-ErrorType"), status, code, rec.Body.String())
	}
	var body map[string]string
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil || body["Type"] != "User" || body["Message"] == "" || body["message"] != body["Message"] {
		c.t.Fatalf("error body %s", rec.Body.String())
	}
}

func makeZip(t *testing.T, files map[string]string) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for name, content := range files {
		w, _ := zw.Create(name)
		w.Write([]byte(content))
	}
	zw.Close()
	return buf.Bytes()
}

func sha(b []byte) string {
	s := sha256.Sum256(b)
	return base64.StdEncoding.EncodeToString(s[:])
}

const role = "arn:aws:iam::000000000000:role/fn"

func (c *client) createFunction(name string, code []byte, extra map[string]any) map[string]any {
	c.t.Helper()
	body := map[string]any{
		"FunctionName": name,
		"Role":         role,
		"Runtime":      "python3.12",
		"Handler":      "app.handler",
		"Code":         map[string]any{"ZipFile": code},
	}
	for k, v := range extra {
		body[k] = v
	}
	return c.ok(c.do("POST", "/2015-03-31/functions", body), 201)
}

func TestRESTPaths(t *testing.T) {
	c := newClient(t)
	code := makeZip(t, map[string]string{"app.py": "def handler(e, c):\n    return e\n"})
	arn := "arn:aws:lambda:us-east-1:000000000000:function:fn"

	cfg := c.createFunction("fn", code, map[string]any{
		"Environment": map[string]any{"Variables": map[string]string{"A": "1"}},
		"Tags":        map[string]string{"team": "team"},
	})
	if cfg["FunctionArn"] != arn || cfg["CodeSha256"] != sha(code) || cfg["CodeSize"] != float64(len(code)) ||
		cfg["State"] != "Active" || cfg["LastUpdateStatus"] != "Successful" || cfg["Version"] != "$LATEST" ||
		cfg["Timeout"] != float64(3) || cfg["MemorySize"] != float64(128) {
		t.Fatalf("create: %v", cfg)
	}
	c.fails(c.do("POST", "/2015-03-31/functions", map[string]any{
		"FunctionName": "fn", "Role": role, "Runtime": "python3.12", "Handler": "app.handler",
		"Code": map[string]any{"ZipFile": code},
	}), 409, "ResourceConflictException")
	c.fails(c.do("POST", "/2015-03-31/functions", map[string]any{
		"FunctionName": "bad", "Role": role, "Runtime": "python3.12", "Handler": "app.handler",
		"Code": map[string]any{"ZipFile": []byte("not a zip")},
	}), 400, "InvalidParameterValueException")

	// GetFunction, with a working code download URL.
	got := c.ok(c.do("GET", "/2015-03-31/functions/fn", nil), 200)
	if got["Tags"].(map[string]any)["team"] != "team" {
		t.Fatalf("tags: %v", got)
	}
	loc := got["Code"].(map[string]any)["Location"].(string)
	u, _ := url.Parse(loc)
	dl := c.do("GET", u.RequestURI(), nil)
	if dl.Code != 200 || !bytes.Equal(dl.Body.Bytes(), code) {
		t.Fatalf("code download: %d", dl.Code)
	}
	q := u.Query()
	q.Set("ns", "someone-else")
	if rec := c.do("GET", u.Path+"?"+q.Encode(), nil); rec.Code != 403 {
		t.Fatalf("download with a forged namespace: %d", rec.Code)
	}
	c.fails(c.do("GET", "/2015-03-31/functions/missing", nil), 404, "ResourceNotFoundException")

	// The legacy /lambda prefix.
	c.ok(c.do("GET", "/lambda/2015-03-31/functions/fn/configuration", nil), 200)

	// Configuration update keeps what it isn't given.
	upd := c.ok(c.do("PUT", "/2015-03-31/functions/fn/configuration", map[string]any{"MemorySize": 256}), 200)
	if upd["MemorySize"] != float64(256) || upd["Environment"].(map[string]any)["Variables"].(map[string]any)["A"] != "1" {
		t.Fatalf("update configuration: %v", upd)
	}
	c.fails(c.do("PUT", "/2015-03-31/functions/fn/configuration", map[string]any{"Timeout": 901}), 400, "ValidationException")

	// Versions.
	v1 := c.ok(c.do("POST", "/2015-03-31/functions/fn/versions", map[string]any{}), 201)
	if v1["Version"] != "1" || v1["FunctionArn"] != arn+":1" {
		t.Fatalf("publish: %v", v1)
	}
	if again := c.ok(c.do("POST", "/2015-03-31/functions/fn/versions", map[string]any{}), 201); again["Version"] != "1" {
		t.Fatalf("publishing an unchanged function made version %v", again["Version"])
	}
	code2 := makeZip(t, map[string]string{"app.py": "def handler(e, c):\n    return 2\n"})
	up := c.ok(c.do("PUT", "/2015-03-31/functions/fn/code", map[string]any{"ZipFile": code2, "Publish": true}), 200)
	if up["Version"] != "2" || up["CodeSha256"] != sha(code2) {
		t.Fatalf("update code with publish: %v", up)
	}
	vs := c.ok(c.do("GET", "/2015-03-31/functions/fn/versions", nil), 200)["Versions"].([]any)
	if len(vs) != 3 || vs[0].(map[string]any)["FunctionArn"] != arn+":$LATEST" || vs[2].(map[string]any)["Version"] != "2" {
		t.Fatalf("versions: %v", vs)
	}
	if v := c.ok(c.do("GET", "/2015-03-31/functions/fn?Qualifier=1", nil), 200); v["Configuration"].(map[string]any)["CodeSha256"] != sha(code) {
		t.Fatalf("version 1 should keep the old code: %v", v)
	}

	// Aliases.
	a := c.ok(c.do("POST", "/2015-03-31/functions/fn/aliases", map[string]any{"Name": "live", "FunctionVersion": "1"}), 201)
	if a["AliasArn"] != arn+":live" {
		t.Fatalf("alias: %v", a)
	}
	c.fails(c.do("POST", "/2015-03-31/functions/fn/aliases", map[string]any{"Name": "live", "FunctionVersion": "1"}), 409, "ResourceConflictException")
	c.fails(c.do("POST", "/2015-03-31/functions/fn/aliases", map[string]any{"Name": "x", "FunctionVersion": "9"}), 404, "ResourceNotFoundException")
	c.ok(c.do("PUT", "/2015-03-31/functions/fn/aliases/live", map[string]any{
		"FunctionVersion": "2", "RoutingConfig": map[string]any{"AdditionalVersionWeights": map[string]float64{"1": 0.25}},
	}), 200)
	if al := c.ok(c.do("GET", "/2015-03-31/functions/fn/aliases", nil), 200)["Aliases"].([]any); len(al) != 1 {
		t.Fatalf("aliases: %v", al)
	}
	if v := c.ok(c.do("GET", "/2015-03-31/functions/fn:live/configuration", nil), 200); v["Version"] != "2" || v["FunctionArn"] != arn+":live" {
		t.Fatalf("configuration through an alias: %v", v)
	}
	c.fails(c.do("DELETE", "/2015-03-31/functions/fn?Qualifier=1", nil), 409, "ResourceConflictException")

	// Policy.
	st := c.ok(c.do("POST", "/2015-03-31/functions/fn/policy?Qualifier=live", map[string]any{
		"StatementId": "s3", "Action": "lambda:InvokeFunction", "Principal": "s3.amazonaws.com",
		"SourceArn": "arn:aws:s3:::bucket",
	}), 201)
	if !strings.Contains(st["Statement"].(string), `"ArnLike":{"AWS:SourceArn":"arn:aws:s3:::bucket"}`) {
		t.Fatalf("statement: %v", st)
	}
	c.fails(c.do("POST", "/2015-03-31/functions/fn/policy?Qualifier=live", map[string]any{
		"StatementId": "s3", "Action": "lambda:InvokeFunction", "Principal": "s3.amazonaws.com",
	}), 409, "ResourceConflictException")
	pol := c.ok(c.do("GET", "/2015-03-31/functions/fn/policy?Qualifier=live", nil), 200)
	var doc struct {
		Statement []struct {
			Sid       string
			Resource  string
			Principal map[string]string
		}
	}
	json.Unmarshal([]byte(pol["Policy"].(string)), &doc)
	if len(doc.Statement) != 1 || doc.Statement[0].Resource != arn+":live" || doc.Statement[0].Principal["Service"] != "s3.amazonaws.com" {
		t.Fatalf("policy: %v", pol)
	}
	c.fails(c.do("GET", "/2015-03-31/functions/fn/policy", nil), 404, "ResourceNotFoundException")
	c.ok(c.do("DELETE", "/2015-03-31/functions/fn/policy/s3?Qualifier=live", nil), 204)
	c.fails(c.do("DELETE", "/2015-03-31/functions/fn/policy/s3?Qualifier=live", nil), 404, "ResourceNotFoundException")

	// Tags.
	c.ok(c.do("POST", "/2017-03-31/tags/"+arn, map[string]any{"Tags": map[string]string{"env": "prod"}}), 204)
	c.ok(c.do("DELETE", "/2017-03-31/tags/"+arn+"?tagKeys=team", nil), 204)
	if tags := c.ok(c.do("GET", "/2017-03-31/tags/"+arn, nil), 200)["Tags"].(map[string]any); len(tags) != 1 || tags["env"] != "prod" {
		t.Fatalf("tags: %v", tags)
	}

	// Concurrency.
	c.ok(c.do("PUT", "/2017-10-31/functions/fn/concurrency", map[string]any{"ReservedConcurrentExecutions": 5}), 200)
	if cc := c.ok(c.do("GET", "/2019-09-30/functions/fn/concurrency", nil), 200); cc["ReservedConcurrentExecutions"] != float64(5) {
		t.Fatalf("concurrency: %v", cc)
	}
	c.ok(c.do("DELETE", "/2017-10-31/functions/fn/concurrency", nil), 204)
	if cc := c.ok(c.do("GET", "/2019-09-30/functions/fn/concurrency", nil), 200); len(cc) != 0 {
		t.Fatalf("concurrency after delete: %v", cc)
	}

	// Function URL.
	fu := c.ok(c.do("POST", "/2021-10-31/functions/fn/url", map[string]any{"AuthType": "NONE"}), 201)
	if !strings.HasSuffix(fu["FunctionUrl"].(string), ".lambda-url.us-east-1.on.aws/") || fu["InvokeMode"] != "BUFFERED" {
		t.Fatalf("url: %v", fu)
	}
	c.fails(c.do("POST", "/2021-10-31/functions/fn/url", map[string]any{"AuthType": "NONE"}), 409, "ResourceConflictException")
	c.ok(c.do("PUT", "/2021-10-31/functions/fn/url", map[string]any{"AuthType": "AWS_IAM"}), 200)
	if g := c.ok(c.do("GET", "/2021-10-31/functions/fn/url", nil), 200); g["AuthType"] != "AWS_IAM" || g["FunctionUrl"] != fu["FunctionUrl"] {
		t.Fatalf("url after update: %v", g)
	}
	c.ok(c.do("DELETE", "/2021-10-31/functions/fn/url", nil), 204)
	c.fails(c.do("GET", "/2021-10-31/functions/fn/url", nil), 404, "ResourceNotFoundException")

	// Event invoke config.
	c.ok(c.do("PUT", "/2019-09-25/functions/fn/event-invoke-config", map[string]any{"MaximumRetryAttempts": 1}), 200)
	c.ok(c.do("POST", "/2019-09-25/functions/fn/event-invoke-config", map[string]any{"MaximumEventAgeInSeconds": 120}), 200)
	eic := c.ok(c.do("GET", "/2019-09-25/functions/fn/event-invoke-config", nil), 200)
	if eic["MaximumRetryAttempts"] != float64(1) || eic["MaximumEventAgeInSeconds"] != float64(120) {
		t.Fatalf("event invoke config: %v", eic)
	}
	c.ok(c.do("DELETE", "/2019-09-25/functions/fn/event-invoke-config", nil), 204)

	// Event source mappings.
	queue := "arn:aws:sqs:us-east-1:000000000000:q"
	esm := c.ok(c.do("POST", "/2015-03-31/event-source-mappings/", map[string]any{"EventSourceArn": queue, "FunctionName": "fn"}), 202)
	id := esm["UUID"].(string)
	if esm["BatchSize"] != float64(10) || esm["State"] != "Enabled" || esm["FunctionArn"] != arn {
		t.Fatalf("esm: %v", esm)
	}
	c.fails(c.do("POST", "/2015-03-31/event-source-mappings/", map[string]any{"EventSourceArn": queue, "FunctionName": "fn"}), 409, "ResourceConflictException")
	c.ok(c.do("PUT", "/2015-03-31/event-source-mappings/"+id, map[string]any{"Enabled": false, "BatchSize": 5}), 202)
	if g := c.ok(c.do("GET", "/2015-03-31/event-source-mappings/"+id, nil), 200); g["State"] != "Disabled" || g["BatchSize"] != float64(5) {
		t.Fatalf("esm after update: %v", g)
	}
	if l := c.ok(c.do("GET", "/2015-03-31/event-source-mappings/?FunctionName=fn", nil), 200)["EventSourceMappings"].([]any); len(l) != 1 {
		t.Fatalf("esm list: %v", l)
	}
	c.ok(c.do("DELETE", "/2015-03-31/event-source-mappings/"+id, nil), 202)
	c.fails(c.do("GET", "/2015-03-31/event-source-mappings/"+id, nil), 404, "ResourceNotFoundException")

	if cs := c.ok(c.do("GET", "/2020-06-30/functions/fn/code-signing-config", nil), 200); cs["FunctionName"] != "fn" {
		t.Fatalf("code signing config: %v", cs)
	}
	if fs := c.ok(c.do("GET", "/2015-03-31/functions/", nil), 200)["Functions"].([]any); len(fs) != 1 {
		t.Fatalf("list: %v", fs)
	}

	// Delete takes the versions, aliases and code with it.
	c.ok(c.do("DELETE", "/2015-03-31/functions/fn", nil), 204)
	c.fails(c.do("GET", "/2015-03-31/functions/fn:1", nil), 404, "ResourceNotFoundException")
	c.fails(c.do("GET", "/2015-03-31/functions/fn/aliases/live", nil), 404, "ResourceNotFoundException")
	if rec := c.do("GET", u.RequestURI(), nil); rec.Code != 404 {
		t.Fatalf("code download after delete: %d", rec.Code)
	}
	c.fails(c.do("GET", "/2015-03-31/nothing-here", nil), 404, "UnknownOperationException")
}

func TestFunctionNameForms(t *testing.T) {
	c := newClient(t)
	code := makeZip(t, map[string]string{"app.py": "def handler(e, c):\n    return e\n"})
	c.createFunction("names", code, map[string]any{"Publish": true})
	arn := "arn:aws:lambda:us-east-1:000000000000:function:names"

	for path, want := range map[string]string{
		"names":                               arn,
		arn:                                   arn,
		"000000000000:function:names":         arn,
		"names:1":                             arn + ":1",
		arn + ":1":                            arn + ":1",
		"000000000000:function:names:$LATEST": arn + ":$LATEST",
		"names?Qualifier=1":                   arn + ":1",
		"names:1?Qualifier=1":                 arn + ":1",
	} {
		got := c.ok(c.do("GET", "/2015-03-31/functions/"+path, nil), 200)
		if a := got["Configuration"].(map[string]any)["FunctionArn"]; a != want {
			t.Errorf("%s: FunctionArn %v, want %s", path, a, want)
		}
	}
	// Escaped colons and $ in the path, as the SDKs send them.
	if rec := c.do("GET", "/2015-03-31/functions/"+url.PathEscape(arn+":$LATEST"), nil); rec.Code != 200 {
		t.Errorf("escaped ARN: %d %s", rec.Code, rec.Body.String())
	}

	c.fails(c.do("GET", "/2015-03-31/functions/names:1?Qualifier=2", nil), 400, "InvalidParameterValueException")
	c.fails(c.do("GET", "/2015-03-31/functions/names:9", nil), 404, "ResourceNotFoundException")
	c.fails(c.do("GET", "/2015-03-31/functions/names:nope", nil), 404, "ResourceNotFoundException")
	c.fails(c.do("GET", "/2015-03-31/functions/arn:aws:lambda:us-east-1:111111111111:function:names", nil), 404, "ResourceNotFoundException")
	c.fails(c.do("GET", "/2015-03-31/functions/arn:aws:lambda:eu-west-1:000000000000:function:names", nil), 404, "ResourceNotFoundException")
	c.fails(c.do("GET", "/2015-03-31/functions/bad%20name", nil), 400, "ValidationException")
	c.fails(c.do("PUT", "/2015-03-31/functions/names:1/configuration", map[string]any{"Timeout": 5}), 400, "InvalidParameterValueException")
}

func TestInvokeWithoutExecutor(t *testing.T) {
	t.Setenv("OPENSNACK_LAMBDA_EXECUTOR", "")
	c := newClient(t)
	c.createFunction("fn", makeZip(t, map[string]string{"app.py": "def handler(e, c):\n    return e\n"}), nil)
	c.ok(c.do("POST", "/2015-03-31/functions/fn/invocations", "{}", "X-Amz-Invocation-Type", "DryRun"), 204)
	c.fails(c.do("POST", "/2015-03-31/functions/fn/invocations", "{}"), 501, "ServiceException")
	c.fails(c.do("POST", "/2015-03-31/functions/missing/invocations", "{}"), 404, "ResourceNotFoundException")
}

func TestInvokeLocalPython(t *testing.T) {
	if _, err := exec.LookPath("python3"); err != nil {
		t.Skip("python3 not installed")
	}
	t.Setenv("OPENSNACK_LAMBDA_EXECUTOR", "local")
	c := newClient(t)
	code := makeZip(t, map[string]string{
		"app.py": `import os, time

def handler(event, context):
    print("got", event["name"])
    if event.get("fail"):
        raise ValueError("nothing for " + event["name"])
    if event.get("sleep"):
        time.sleep(event["sleep"])
    return {
        "hello": event["name"],
        "greeting": os.environ["GREETING"],
        "function": context.function_name,
        "arn": context.invoked_function_arn,
        "remaining": context.get_remaining_time_in_millis() > 0,
        "secret": os.environ.get("OPENSNACK_PG_DSN"),
    }
`,
	})
	t.Setenv("OPENSNACK_PG_DSN", "must-not-leak")
	c.createFunction("py", code, map[string]any{
		"Timeout":     1,
		"Environment": map[string]any{"Variables": map[string]string{"GREETING": "hello"}},
		"Publish":     true,
	})
	c.ok(c.do("POST", "/2015-03-31/functions/py/aliases", map[string]any{"Name": "live", "FunctionVersion": "1"}), 201)

	rec := c.do("POST", "/2015-03-31/functions/py:live/invocations", `{"name":"nightly"}`, "X-Amz-Log-Type", "Tail")
	out := c.ok(rec, 200)
	if out["hello"] != "nightly" || out["greeting"] != "hello" || out["function"] != "py" ||
		out["arn"] != "arn:aws:lambda:us-east-1:000000000000:function:py:live" || out["remaining"] != true || out["secret"] != nil {
		t.Fatalf("result: %v", out)
	}
	if rec.Header().Get("X-Amz-Executed-Version") != "1" || rec.Header().Get("X-Amz-Function-Error") != "" {
		t.Fatalf("headers: %v", rec.Header())
	}
	tail, _ := base64.StdEncoding.DecodeString(rec.Header().Get("X-Amz-Log-Result"))
	if !strings.Contains(string(tail), "got nightly") || !strings.Contains(string(tail), "REPORT RequestId:") {
		t.Fatalf("log tail: %q", tail)
	}

	rec = c.do("POST", "/2015-03-31/functions/py/invocations", `{"name":"intruder","fail":true}`)
	errDoc := c.ok(rec, 200)
	if rec.Header().Get("X-Amz-Function-Error") != "Unhandled" || errDoc["errorType"] != "ValueError" ||
		errDoc["errorMessage"] != "nothing for intruder" {
		t.Fatalf("function error: %v %v", rec.Header(), errDoc)
	}

	rec = c.do("POST", "/2015-03-31/functions/py/invocations", `{"name":"slow","sleep":5}`)
	errDoc = c.ok(rec, 200)
	if rec.Header().Get("X-Amz-Function-Error") != "Unhandled" || errDoc["errorType"] != "Sandbox.Timedout" ||
		!strings.Contains(errDoc["errorMessage"].(string), "Task timed out after 1.00 seconds") {
		t.Fatalf("timeout: %v %v", rec.Header(), errDoc)
	}

	c.ok(c.do("POST", "/2015-03-31/functions/py/invocations", `{"name":"later"}`, "X-Amz-Invocation-Type", "Event"), 202)
	c.fails(c.do("POST", "/2015-03-31/functions/py/invocations", `not json`), 400, "InvalidRequestContentException")

	// A handler that doesn't exist is the function's error, as in AWS.
	c.ok(c.do("PUT", "/2015-03-31/functions/py/configuration", map[string]any{"Handler": "nope.handler"}), 200)
	rec = c.do("POST", "/2015-03-31/functions/py/invocations", `{}`)
	if errDoc = c.ok(rec, 200); errDoc["errorType"] != "Runtime.ImportModuleError" {
		t.Fatalf("missing module: %v", errDoc)
	}
}

func TestInvokeLocalNode(t *testing.T) {
	if _, err := exec.LookPath("node"); err != nil {
		t.Skip("node not installed")
	}
	t.Setenv("OPENSNACK_LAMBDA_EXECUTOR", "local")
	c := newClient(t)
	code := makeZip(t, map[string]string{
		"index.mjs": "export const handler = async (event, context) => ({ hello: event.name, fn: context.functionName, env: process.env.GREETING });\n",
		"cb.js":     "exports.handler = (event, context, cb) => cb(new Error('scrap'));\n",
	})
	c.createFunction("js", code, map[string]any{
		"Runtime":     "nodejs22.x",
		"Handler":     "index.handler",
		"Environment": map[string]any{"Variables": map[string]string{"GREETING": "ho"}},
	})
	if out := c.ok(c.do("POST", "/2015-03-31/functions/js/invocations", `{"name":"comet"}`), 200); out["hello"] != "comet" || out["fn"] != "js" || out["env"] != "ho" {
		t.Fatalf("result: %v", out)
	}
	c.ok(c.do("PUT", "/2015-03-31/functions/js/configuration", map[string]any{"Handler": "cb.handler"}), 200)
	rec := c.do("POST", "/2015-03-31/functions/js/invocations", `{}`)
	if out := c.ok(rec, 200); rec.Header().Get("X-Amz-Function-Error") != "Unhandled" || out["errorMessage"] != "scrap" {
		t.Fatalf("callback error: %v", out)
	}
}
