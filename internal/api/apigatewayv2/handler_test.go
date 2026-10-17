// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package apigatewayv2

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"opensnack/internal/egress"
	"opensnack/internal/resource"
)

func call(t *testing.T, h *Handler, method, path string, body any) (int, obj) {
	t.Helper()
	var r io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		r = strings.NewReader(string(b))
	}
	req := httptest.NewRequest(method, path, r)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	out := obj{}
	json.Unmarshal(rec.Body.Bytes(), &out)
	return rec.Code, out
}

func mustCall(t *testing.T, h *Handler, method, path string, body any, want int) obj {
	t.Helper()
	code, out := call(t, h, method, path, body)
	if code != want {
		t.Fatalf("%s %s: status %d, want %d: %v", method, path, code, want, out)
	}
	return out
}

func TestMatchRoute(t *testing.T) {
	routes := []obj{
		{"routeId": "default", "routeKey": "$default"},
		{"routeId": "pet", "routeKey": "GET /pets/{id}"},
		{"routeId": "petsAny", "routeKey": "ANY /pets/{id}"},
		{"routeId": "dogs", "routeKey": "GET /pets/dogs"},
		{"routeId": "greedy", "routeKey": "ANY /pets/{proxy+}"},
		{"routeId": "root", "routeKey": "GET /"},
		{"routeId": "files", "routeKey": "GET /files/{path+}"},
	}
	tests := []struct {
		method, path, want string
		params             map[string]string
	}{
		{"GET", "/pets/dogs", "dogs", map[string]string{}},
		{"GET", "/pets/7", "pet", map[string]string{"id": "7"}},
		{"POST", "/pets/7", "petsAny", map[string]string{"id": "7"}},
		{"GET", "/pets/7/toys", "greedy", map[string]string{"proxy": "7/toys"}},
		{"GET", "/", "root", map[string]string{}},
		{"POST", "/", "default", nil},
		{"GET", "/files/a/b/c.txt", "files", map[string]string{"path": "a/b/c.txt"}},
		{"GET", "/files", "default", nil},
		{"DELETE", "/nothing/here", "default", nil},
	}
	for _, tt := range tests {
		got, params := matchRoute(routes, tt.method, tt.path)
		if str(got, "routeId") != tt.want {
			t.Errorf("%s %s: matched %s, want %s", tt.method, tt.path, str(got, "routeId"), tt.want)
			continue
		}
		if len(params) != len(tt.params) {
			t.Errorf("%s %s: params %v, want %v", tt.method, tt.path, params, tt.params)
		}
		for k, v := range tt.params {
			if params[k] != v {
				t.Errorf("%s %s: param %s = %q, want %q", tt.method, tt.path, k, params[k], v)
			}
		}
	}

	if got, _ := matchRoute(routes[1:], "GET", "/other"); got != nil {
		t.Errorf("no $default: matched %v", got)
	}
}

func TestParseHost(t *testing.T) {
	t.Setenv("OPENSNACK_HOST_SUFFIXES", "localtest.me")
	for host, want := range map[string]string{
		"abc123.execute-api.us-east-1.amazonaws.com":     "abc123",
		"abc123.execute-api.eu-west-1.localtest.me:4566": "abc123",
		"execute-api.us-east-1.amazonaws.com":            "",
		"abc123.execute-api.us-east-1.example.com":       "",
		"sqs.us-east-1.amazonaws.com":                    "",
	} {
		id, _, ok := ParseHost(host)
		if id != want || ok != (want != "") {
			t.Errorf("ParseHost(%q) = %q, %v; want %q", host, id, ok, want)
		}
	}
}

type fakeInvoker struct {
	mu    sync.Mutex
	calls []LambdaInvocation
	reply string
}

func (f *fakeInvoker) InvokeLambda(_ context.Context, in LambdaInvocation) ([]byte, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, in)
	return []byte(f.reply), nil
}

// newAPI builds an HTTP API with one route to one integration and an
// auto-deployed stage, through the control plane.
func newAPI(t *testing.T, h *Handler, stage, routeKey string, integ obj) string {
	t.Helper()
	api := mustCall(t, h, "POST", "/v2/apis", obj{"name": "t", "protocolType": "HTTP",
		"corsConfiguration": obj{"allowOrigins": []string{"https://site.example"}, "allowMethods": []string{"GET"}}}, 201)
	id := str(api, "apiId")
	if want := "https://" + id + ".execute-api.us-east-1.amazonaws.com"; str(api, "apiEndpoint") != want {
		t.Fatalf("apiEndpoint = %s, want %s", str(api, "apiEndpoint"), want)
	}
	in := mustCall(t, h, "POST", "/v2/apis/"+id+"/integrations", integ, 201)
	mustCall(t, h, "POST", "/v2/apis/"+id+"/routes", obj{"routeKey": routeKey, "target": "integrations/" + str(in, "integrationId")}, 201)
	st := mustCall(t, h, "POST", "/v2/apis/"+id+"/stages", obj{"stageName": stage, "autoDeploy": true,
		"stageVariables": obj{"color": "red"}}, 201)
	if str(st, "deploymentId") == "" {
		t.Fatalf("auto-deploy stage has no deployment: %v", st)
	}
	return id
}

func dataPlane(h *Handler, method, url string, hdr map[string]string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, url, strings.NewReader(`{"hello":"world"}`))
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	h.ServeDataPlane(rec, req)
	return rec
}

func TestDataPlaneLambdaProxy(t *testing.T) {
	h := NewHandler(resource.NewMemStore())
	id := newAPI(t, h, "prod", "POST /pets/{id}", obj{"integrationType": "AWS_PROXY",
		"integrationUri": "arn:aws:lambda:us-east-1:000000000000:function:pets", "payloadFormatVersion": "2.0"})
	host := "https://" + id + ".execute-api.us-east-1.amazonaws.com"

	// Not wired yet: 502.
	if rec := dataPlane(h, "POST", host+"/prod/pets/42", nil); rec.Code != http.StatusBadGateway {
		t.Fatalf("unwired invoker: status %d, want 502", rec.Code)
	}

	inv := &fakeInvoker{reply: `{"statusCode":201,"headers":{"X-Pet":"rex"},"cookies":["a=b"],"body":"created"}`}
	h.Invoker = inv
	rec := dataPlane(h, "POST", host+"/prod/pets/42?q=1&q=2", map[string]string{"Origin": "https://site.example", "Cookie": "c=d"})
	if rec.Code != 201 || rec.Body.String() != "created" || rec.Header().Get("X-Pet") != "rex" ||
		rec.Header().Get("Set-Cookie") != "a=b" {
		t.Fatalf("response %d %q %v", rec.Code, rec.Body.String(), rec.Header())
	}
	if rec.Header().Get("Access-Control-Allow-Origin") != "https://site.example" {
		t.Errorf("CORS header missing: %v", rec.Header())
	}
	if len(inv.calls) != 1 {
		t.Fatalf("invoked %d times", len(inv.calls))
	}
	c := inv.calls[0]
	if c.Function != "arn:aws:lambda:us-east-1:000000000000:function:pets" || c.Namespace != "default" || c.Region != "us-east-1" {
		t.Errorf("invocation %+v", c)
	}
	var ev struct {
		Version, RouteKey, RawPath, RawQueryString, Body string
		PathParameters                                   map[string]string
		QueryStringParameters                            map[string]string
		StageVariables                                   map[string]string
		Cookies                                          []string
		RequestContext                                   struct {
			Stage string
			HTTP  struct{ Method, Path string }
		}
	}
	if err := json.Unmarshal(c.Payload, &ev); err != nil {
		t.Fatal(err)
	}
	if ev.Version != "2.0" || ev.RouteKey != "POST /pets/{id}" || ev.RawPath != "/prod/pets/42" ||
		ev.PathParameters["id"] != "42" || ev.QueryStringParameters["q"] != "1,2" || ev.RawQueryString != "q=1&q=2" ||
		ev.StageVariables["color"] != "red" || ev.Body != `{"hello":"world"}` || ev.RequestContext.Stage != "prod" ||
		ev.RequestContext.HTTP.Method != "POST" || len(ev.Cookies) != 1 {
		t.Errorf("event %s", c.Payload)
	}

	// A bare JSON result is the 200 body under format 2.0.
	inv.reply = `{"ok":true}`
	if rec := dataPlane(h, "POST", host+"/prod/pets/1", nil); rec.Code != 200 || rec.Body.String() != `{"ok":true}` {
		t.Errorf("bare result: %d %q", rec.Code, rec.Body.String())
	}
	// Unknown stage, unmatched route, unknown API.
	if rec := dataPlane(h, "POST", host+"/dev/pets/1", nil); rec.Code != 404 {
		t.Errorf("unknown stage: %d", rec.Code)
	}
	if rec := dataPlane(h, "GET", host+"/prod/pets/1", nil); rec.Code != 404 {
		t.Errorf("unmatched method: %d", rec.Code)
	}
	if rec := dataPlane(h, "GET", "https://nope.execute-api.us-east-1.amazonaws.com/", nil); rec.Code != 404 {
		t.Errorf("unknown api: %d", rec.Code)
	}
	// CORS preflight is answered by API Gateway itself.
	rec = dataPlane(h, "OPTIONS", host+"/prod/pets/1", map[string]string{"Origin": "https://site.example", "Access-Control-Request-Method": "GET"})
	if rec.Code != 204 || rec.Header().Get("Access-Control-Allow-Methods") != "GET" {
		t.Errorf("preflight: %d %v", rec.Code, rec.Header())
	}
}

// slowInvoker is a function that outlasts any caller.
type slowInvoker struct{}

func (slowInvoker) InvokeLambda(ctx context.Context, _ LambdaInvocation) ([]byte, error) {
	<-ctx.Done()
	return nil, ctx.Err()
}

// The integration timeout bounds the whole Lambda call, cold start and all.
func TestDataPlaneLambdaTimeout(t *testing.T) {
	h := NewHandler(resource.NewMemStore())
	id := newAPI(t, h, "prod", "GET /slow", obj{"integrationType": "AWS_PROXY", "timeoutInMillis": 50,
		"integrationUri": "arn:aws:lambda:us-east-1:000000000000:function:slow", "payloadFormatVersion": "2.0"})
	h.Invoker = slowInvoker{}
	start := time.Now()
	rec := dataPlane(h, "GET", "https://"+id+".execute-api.us-east-1.amazonaws.com/prod/slow", nil)
	if rec.Code != http.StatusGatewayTimeout || time.Since(start) > 5*time.Second {
		t.Fatalf("timeout: %d after %s", rec.Code, time.Since(start))
	}
}

func TestDataPlaneHTTPProxyAllowlist(t *testing.T) {
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Backend", "yes")
		w.WriteHeader(202)
		io.WriteString(w, r.Method+" "+r.URL.RequestURI())
	}))
	defer backend.Close()

	h := NewHandler(resource.NewMemStore())
	id := newAPI(t, h, "$default", "ANY /api/{proxy+}", obj{"integrationType": "HTTP_PROXY",
		"integrationMethod": "ANY", "integrationUri": backend.URL + "/v1/{proxy}"})
	host := "https://" + id + ".execute-api.us-east-1.amazonaws.com"

	if rec := dataPlane(h, "GET", host+"/api/a/b?x=1", nil); rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("target off the allowlist: status %d, want 503", rec.Code)
	}

	h.Targets = egress.New([]string{backend.URL})
	rec := dataPlane(h, "PUT", host+"/api/a/b?x=1", nil)
	if rec.Code != 202 || rec.Body.String() != "PUT /v1/a/b?x=1" || rec.Header().Get("X-Backend") != "yes" {
		t.Fatalf("proxied: %d %q %v", rec.Code, rec.Body.String(), rec.Header())
	}
}

func TestControlPlaneDefaultsAndStageVariables(t *testing.T) {
	h := NewHandler(resource.NewMemStore())
	api := mustCall(t, h, "POST", "/v2/apis", obj{"name": "x", "protocolType": "HTTP"}, 201)
	id := str(api, "apiId")
	for k, want := range map[string]any{
		"apiKeySelectionExpression": "$request.header.x-api-key",
		"routeSelectionExpression":  "$request.method $request.path",
		"ipAddressType":             "ipv4",
		"disableExecuteApiEndpoint": false,
	} {
		if api[k] != want {
			t.Errorf("%s = %v, want %v", k, api[k], want)
		}
	}
	mustCall(t, h, "POST", "/v2/apis/"+id+"/stages", obj{"stageName": "s", "stageVariables": obj{"a": "1", "b": "2"}}, 201)
	st := mustCall(t, h, "PATCH", "/v2/apis/"+id+"/stages/s", obj{"stageVariables": obj{"a": "", "c": "3"}}, 200)
	if vars := mapOf(st, "stageVariables"); len(vars) != 2 || vars["b"] != "2" || vars["c"] != "3" {
		t.Errorf("stage variables after patch: %v", vars)
	}
	// Deployments need routes.
	mustCall(t, h, "POST", "/v2/apis/"+id+"/deployments", obj{}, 400)

	arn := "arn:aws:apigateway:us-east-1::/apis/" + id
	mustCall(t, h, "POST", "/v2/tags/"+strings.ReplaceAll(arn, "/", "%2F"), obj{"tags": obj{"k": "v"}}, 201)
	tags := mustCall(t, h, "GET", "/v2/tags/"+strings.ReplaceAll(arn, "/", "%2F"), nil, 200)
	if mapOf(tags, "tags")["k"] != "v" {
		t.Errorf("tags %v", tags)
	}
	mustCall(t, h, "DELETE", "/v2/apis/"+id, nil, 204)
	mustCall(t, h, "GET", "/v2/apis/"+id, nil, 404)
	mustCall(t, h, "GET", "/v2/apis/"+id+"/stages/s", nil, 404)
}

func TestCaddyPush(t *testing.T) {
	var mu sync.Mutex
	var seen []string
	var body obj
	admin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		seen = append(seen, r.Method+" "+r.URL.Path)
		if r.Method == http.MethodPatch {
			http.Error(w, "unknown object ID", 404)
			return
		}
		json.NewDecoder(r.Body).Decode(&body)
	}))
	defer admin.Close()

	c := &Caddy{AdminURL: admin.URL, Server: "srv0", Upstream: "opensnack:4566", Client: admin.Client()}
	api := obj{"apiId": "abc", "apiEndpoint": "https://abc.execute-api.eu-west-1.amazonaws.com"}
	c.Push(api, nil, nil, nil, egress.New(nil))
	if len(seen) != 2 || seen[0] != "PATCH /id/opensnack-apigw-abc" || seen[1] != "PUT /config/apps/http/servers/srv0/routes/0" {
		t.Fatalf("admin calls %v", seen)
	}
	b, _ := json.Marshal(body)
	if !strings.Contains(string(b), `"abc.execute-api.eu-west-1.amazonaws.com"`) || !strings.Contains(string(b), `"dial":"opensnack:4566"`) {
		t.Errorf("route %s", b)
	}

	// A lone $default HTTP_PROXY to an allowed target goes straight there.
	routes := []obj{{"routeKey": "$default", "target": "integrations/i1"}}
	integs := []obj{{"integrationId": "i1", "integrationType": "HTTP_PROXY", "integrationUri": "http://app.internal:8080/track"}}
	stages := []obj{{"stageName": "$default", "deploymentId": "d1"}}
	r := c.Route(api, routes, integs, stages, egress.New([]string{"app.internal"}))
	b, _ = json.Marshal(r)
	if !strings.Contains(string(b), `"dial":"app.internal:8080"`) || !strings.Contains(string(b), `"/track?{http.request.uri.query}"`) {
		t.Errorf("direct route %s", b)
	}
}
