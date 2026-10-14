// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package apigatewayv2

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"opensnack/internal/awsresponses"
	"opensnack/internal/egress"
	"opensnack/internal/resource"
	"opensnack/internal/util"

	"go.uber.org/zap"
)

// Invoker runs a Lambda function for an AWS_PROXY integration. The router
// wires the Lambda service in; until then requests answer 502.
type Invoker interface {
	InvokeLambda(ctx context.Context, in LambdaInvocation) ([]byte, error)
}

type LambdaInvocation struct {
	// Namespace is the tenant that owns the API (and so the function).
	Namespace string
	Region    string
	AccountID string
	// Function is the function ARN (or name) from the integration URI.
	Function string
	Payload  []byte
}

// ErrNoInvoker is what the unwired invoker returns.
var ErrNoInvoker = errors.New("lambda invoke is not available")

// maxPayload is API Gateway's request and response payload limit.
const maxPayload = 10 << 20

// ParseHost recognises {api_id}.execute-api.{region}.{suffix}, where suffix
// is amazonaws.com or one of OPENSNACK_HOST_SUFFIXES.
func ParseHost(hostport string) (apiID, region string, ok bool) {
	host := strings.ToLower(hostport)
	if h, _, err := net.SplitHostPort(host); err == nil {
		host = h
	}
	labels := strings.SplitN(strings.TrimSuffix(host, "."), ".", 4)
	if len(labels) < 4 || labels[1] != "execute-api" || labels[0] == "" {
		return "", "", false
	}
	for _, s := range util.HostSuffixes() {
		if labels[3] == s {
			return labels[0], labels[2], true
		}
	}
	return "", "", false
}

// deployed is an API resolved for a data-plane request.
type deployed struct {
	scope     string
	namespace string
	region    string
	api       obj
}

// lookup finds an API by ID across tenants: execute-api requests carry no
// credentials, so the ID is all there is.
func (h *Handler) lookup(apiID string) (*deployed, error) {
	insp, ok := h.Store.(resource.Inspector)
	if !ok {
		return nil, errNotStored
	}
	rows, err := insp.Find(resource.Filter{Service: service, Type: "api", ID: apiID})
	if err != nil || len(rows) == 0 {
		return nil, errNotStored
	}
	row := rows[0]
	ns, region := resource.SplitScope(row.Namespace)
	if region == "" {
		region = row.Region
	}
	var api obj
	if err := json.Unmarshal(row.Attributes, &api); err != nil {
		return nil, err
	}
	return &deployed{scope: resource.JoinScope(ns, region), namespace: ns, region: region, api: api}, nil
}

func dpError(w http.ResponseWriter, status int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Apigw-Requestid", awsresponses.NextRequestID())
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(map[string]string{"message": msg})
}

// ServeDataPlane answers a request to an execute-api host name.
func (h *Handler) ServeDataPlane(w http.ResponseWriter, r *http.Request) {
	apiID, region, ok := ParseHost(r.Host)
	if !ok {
		dpError(w, http.StatusNotFound, "Not Found")
		return
	}
	d, err := h.lookup(apiID)
	if err != nil || (region != "" && region != d.region) {
		dpError(w, http.StatusNotFound, "Not Found")
		return
	}
	if boolean(d.api, "disableExecuteApiEndpoint") {
		dpError(w, http.StatusForbidden, "Forbidden")
		return
	}
	if str(d.api, "protocolType") != "HTTP" {
		dpError(w, http.StatusBadRequest, "WebSocket APIs are not served by opensnack")
		return
	}

	stages, _ := h.children(d.scope, "stage", apiID+"/")
	stage, path := pickStage(stages, r.URL.Path)
	if stage == nil || str(stage, "deploymentId") == "" {
		dpError(w, http.StatusNotFound, "Not Found")
		return
	}

	cors := mapOf(d.api, "corsConfiguration")
	if cors != nil && r.Method == http.MethodOptions && r.Header.Get("Origin") != "" &&
		r.Header.Get("Access-Control-Request-Method") != "" {
		preflight(w, r, cors)
		return
	}
	if cors != nil {
		corsHeaders(w, r, cors)
	}

	routes, _ := h.children(d.scope, "route", apiID+"/")
	route, params := matchRoute(routes, r.Method, path)
	if route == nil {
		dpError(w, http.StatusNotFound, "Not Found")
		return
	}
	integID, _ := strings.CutPrefix(str(route, "target"), "integrations/")
	integ, err := h.load(d.scope, "integration", apiID+"/"+integID)
	if integID == "" || err != nil {
		dpError(w, http.StatusInternalServerError, "Internal Server Error")
		return
	}

	body, err := io.ReadAll(io.LimitReader(r.Body, maxPayload+1))
	if err != nil || len(body) > maxPayload {
		dpError(w, http.StatusRequestEntityTooLarge, "Request Entity Too Large")
		return
	}
	call := &dpCall{h: h, w: w, r: r, d: d, stage: stage, route: route, integ: integ,
		path: path, params: params, body: body}
	switch str(integ, "integrationType") {
	case "HTTP_PROXY":
		call.httpProxy()
	case "AWS_PROXY":
		call.lambdaProxy()
	default:
		dpError(w, http.StatusInternalServerError, "Internal Server Error")
	}
}

// pickStage resolves the stage from the first path segment, falling back
// to the $default stage, which serves the whole path.
func pickStage(stages []obj, path string) (obj, string) {
	first, rest, _ := strings.Cut(strings.TrimPrefix(path, "/"), "/")
	var def obj
	for _, s := range stages {
		name := str(s, "stageName")
		if name == "$default" {
			def = s
		} else if name == first && first != "" {
			return s, "/" + rest
		}
	}
	if def != nil {
		return def, path
	}
	return nil, ""
}

// matchRoute picks the route for a request the way HTTP APIs do: an exact
// path beats a greedy {proxy+} path, which beats $default; among those the
// most literal segments win, and a named method beats ANY.
func matchRoute(routes []obj, method, path string) (obj, map[string]string) {
	var best obj
	var bestParams map[string]string
	bestScore := -1
	for _, rt := range routes {
		key := str(rt, "routeKey")
		if key == "$default" {
			if bestScore < 0 {
				best, bestParams, bestScore = rt, nil, 0
			}
			continue
		}
		m, pattern, ok := strings.Cut(key, " ")
		if !ok || (m != "ANY" && !strings.EqualFold(m, method)) {
			continue
		}
		params, literals, greedy, ok := matchPath(pattern, path)
		if !ok {
			continue
		}
		score := 1 + literals*4
		if !greedy {
			score += 1 << 20
		}
		if m != "ANY" {
			score += 2
		}
		if score > bestScore {
			best, bestParams, bestScore = rt, params, score
		}
	}
	return best, bestParams
}

func matchPath(pattern, path string) (params map[string]string, literals int, greedy, ok bool) {
	ps := strings.Split(strings.Trim(pattern, "/"), "/")
	xs := strings.Split(strings.Trim(path, "/"), "/")
	if strings.Trim(pattern, "/") == "" {
		ps = nil
	}
	if strings.Trim(path, "/") == "" {
		xs = nil
	}
	params = map[string]string{}
	for i, p := range ps {
		if strings.HasPrefix(p, "{") && strings.HasSuffix(p, "+}") {
			if i != len(ps)-1 || i >= len(xs) {
				return nil, 0, false, false
			}
			params[p[1:len(p)-2]] = strings.Join(xs[i:], "/")
			return params, literals, true, true
		}
		if i >= len(xs) {
			return nil, 0, false, false
		}
		if strings.HasPrefix(p, "{") && strings.HasSuffix(p, "}") {
			if xs[i] == "" {
				return nil, 0, false, false
			}
			params[p[1:len(p)-1]] = xs[i]
			continue
		}
		if p != xs[i] {
			return nil, 0, false, false
		}
		literals++
	}
	if len(ps) != len(xs) {
		return nil, 0, false, false
	}
	return params, literals, false, true
}

func preflight(w http.ResponseWriter, r *http.Request, cors obj) {
	origin := r.Header.Get("Origin")
	if !corsAllows(cors, "allowOrigins", origin) || !corsAllows(cors, "allowMethods", r.Header.Get("Access-Control-Request-Method")) {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	corsHeaders(w, r, cors)
	w.Header().Set("Access-Control-Allow-Methods", strings.Join(stringList(cors["allowMethods"]), ","))
	if hs := stringList(cors["allowHeaders"]); len(hs) > 0 {
		w.Header().Set("Access-Control-Allow-Headers", strings.Join(hs, ","))
	}
	if age, ok := cors["maxAge"].(float64); ok {
		w.Header().Set("Access-Control-Max-Age", strconv.Itoa(int(age)))
	}
	w.WriteHeader(http.StatusNoContent)
}

func corsHeaders(w http.ResponseWriter, r *http.Request, cors obj) {
	origin := r.Header.Get("Origin")
	if origin == "" || !corsAllows(cors, "allowOrigins", origin) {
		return
	}
	if contains(stringList(cors["allowOrigins"]), "*") && !boolean(cors, "allowCredentials") {
		w.Header().Set("Access-Control-Allow-Origin", "*")
	} else {
		w.Header().Set("Access-Control-Allow-Origin", origin)
		w.Header().Add("Vary", "Origin")
	}
	if boolean(cors, "allowCredentials") {
		w.Header().Set("Access-Control-Allow-Credentials", "true")
	}
	if hs := stringList(cors["exposeHeaders"]); len(hs) > 0 {
		w.Header().Set("Access-Control-Expose-Headers", strings.Join(hs, ","))
	}
}

func corsAllows(cors obj, field, v string) bool {
	for _, a := range stringList(cors[field]) {
		if a == "*" || strings.EqualFold(a, v) {
			return true
		}
	}
	return false
}

func stringList(v any) []string {
	var out []string
	if l, ok := v.([]any); ok {
		for _, x := range l {
			if s, ok := x.(string); ok {
				out = append(out, s)
			}
		}
	}
	return out
}

func contains(l []string, s string) bool {
	for _, x := range l {
		if x == s {
			return true
		}
	}
	return false
}

// dpCall is one data-plane request on its way to an integration.
type dpCall struct {
	h      *Handler
	w      http.ResponseWriter
	r      *http.Request
	d      *deployed
	stage  obj
	route  obj
	integ  obj
	path   string
	params map[string]string
	body   []byte
}

var uriVar = regexp.MustCompile(`\$\{stageVariables\.([A-Za-z0-9_]+)\}|\{([A-Za-z0-9_.+-]+)\}`)

// targetURL fills in the integration URI's {path} and stage variables and
// keeps the query string.
func (c *dpCall) targetURL() string {
	vars := mapOf(c.stage, "stageVariables")
	u := uriVar.ReplaceAllStringFunc(str(c.integ, "integrationUri"), func(m string) string {
		sub := uriVar.FindStringSubmatch(m)
		if sub[1] != "" {
			s, _ := vars[sub[1]].(string)
			return s
		}
		name := strings.TrimSuffix(sub[2], "+")
		if v, ok := c.params[name]; ok {
			return v
		}
		return m
	})
	if q := c.r.URL.RawQuery; q != "" {
		if strings.Contains(u, "?") {
			u += "&" + q
		} else {
			u += "?" + q
		}
	}
	return u
}

func (c *dpCall) httpProxy() {
	target := c.targetURL()
	if !c.h.Targets.Allows(target) {
		zap.L().Info("apigateway: HTTP_PROXY target not on OPENSNACK_APIGW_ALLOWED_TARGETS",
			zap.String("api", str(c.d.api, "apiId")), zap.String("target", target))
		dpError(c.w, http.StatusServiceUnavailable, "Service Unavailable")
		return
	}
	method := str(c.integ, "integrationMethod")
	if method == "" || method == "ANY" {
		method = c.r.Method
	}
	timeout := 30 * time.Second
	if ms, ok := c.integ["timeoutInMillis"].(float64); ok && ms > 0 {
		timeout = time.Duration(ms) * time.Millisecond
	}
	ctx, cancel := context.WithTimeout(c.r.Context(), timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, method, target, bytes.NewReader(c.body))
	if err != nil {
		dpError(c.w, http.StatusInternalServerError, "Internal Server Error")
		return
	}
	req.Header = c.r.Header.Clone()
	for _, hh := range egress.HopHeaders {
		req.Header.Del(hh)
	}
	req.Header.Set("Forwarded", "for="+clientIP(c.r)+";host="+c.r.Host+";proto=https")
	resp, err := c.h.client.Do(req)
	if err != nil {
		if errors.Is(err, context.DeadlineExceeded) {
			dpError(c.w, http.StatusGatewayTimeout, "Service Unavailable")
			return
		}
		dpError(c.w, http.StatusServiceUnavailable, "Service Unavailable")
		return
	}
	defer resp.Body.Close()
	for k, vs := range resp.Header {
		for _, v := range vs {
			c.w.Header().Add(k, v)
		}
	}
	for _, hh := range egress.HopHeaders {
		c.w.Header().Del(hh)
	}
	c.w.Header().Set("Apigw-Requestid", awsresponses.NextRequestID())
	c.w.WriteHeader(resp.StatusCode)
	io.Copy(c.w, io.LimitReader(resp.Body, maxPayload))
}

func clientIP(r *http.Request) string {
	if f := r.Header.Get("X-Forwarded-For"); f != "" {
		ip, _, _ := strings.Cut(f, ",")
		return strings.TrimSpace(ip)
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

// functionFromURI accepts a function ARN or the apigateway invocation URI
// that wraps one.
func functionFromURI(uri string) string {
	if _, rest, ok := strings.Cut(uri, "/functions/"); ok {
		return strings.TrimSuffix(rest, "/invocations")
	}
	return uri
}

func (c *dpCall) lambdaProxy() {
	if c.h.Invoker == nil {
		dpError(c.w, http.StatusBadGateway, "Bad Gateway")
		return
	}
	v2 := str(c.integ, "payloadFormatVersion") == "2.0"
	var event any
	if v2 {
		event = c.eventV2()
	} else {
		event = c.eventV1()
	}
	payload, _ := json.Marshal(event)
	out, err := c.h.Invoker.InvokeLambda(c.r.Context(), LambdaInvocation{
		Namespace: c.d.namespace,
		Region:    c.d.region,
		AccountID: str(c.d.api, "_accountId"),
		Function:  functionFromURI(str(c.integ, "integrationUri")),
		Payload:   payload,
	})
	if err != nil {
		zap.L().Info("apigateway: lambda proxy failed", zap.String("api", str(c.d.api, "apiId")), zap.Error(err))
		if errors.Is(err, ErrNoInvoker) {
			dpError(c.w, http.StatusBadGateway, "Bad Gateway")
			return
		}
		dpError(c.w, http.StatusInternalServerError, "Internal Server Error")
		return
	}
	writeLambdaResponse(c.w, out, v2)
}

func (c *dpCall) requestContext(v2 bool) obj {
	now := time.Now().UTC()
	apiID := str(c.d.api, "apiId")
	host := c.r.Host
	if h, _, err := net.SplitHostPort(host); err == nil {
		host = h
	}
	rc := obj{
		"accountId":  str(c.d.api, "_accountId"),
		"apiId":      apiID,
		"domainName": host,
		"domainPrefix": func() string {
			p, _, _ := strings.Cut(host, ".")
			return p
		}(),
		"requestId":        awsresponses.NextRequestID(),
		"stage":            str(c.stage, "stageName"),
		"requestTimeEpoch": now.UnixMilli(),
	}
	if v2 {
		delete(rc, "requestTimeEpoch")
		rc["routeKey"] = str(c.route, "routeKey")
		rc["time"] = now.Format("02/Jan/2006:15:04:05 -0700")
		rc["timeEpoch"] = now.UnixMilli()
		rc["http"] = obj{
			"method":    c.r.Method,
			"path":      c.r.URL.Path,
			"protocol":  c.r.Proto,
			"sourceIp":  clientIP(c.r),
			"userAgent": c.r.UserAgent(),
		}
	} else {
		rc["resourcePath"] = c.resourcePath()
		rc["httpMethod"] = c.r.Method
		rc["path"] = c.r.URL.Path
		rc["protocol"] = c.r.Proto
		rc["identity"] = obj{"sourceIp": clientIP(c.r), "userAgent": c.r.UserAgent()}
		rc["requestTime"] = now.Format("02/Jan/2006:15:04:05 -0700")
	}
	return rc
}

func (c *dpCall) resourcePath() string {
	key := str(c.route, "routeKey")
	if _, p, ok := strings.Cut(key, " "); ok {
		return p
	}
	return key
}

func (c *dpCall) encodedBody() (any, bool) {
	if len(c.body) == 0 {
		return nil, false
	}
	if utf8.Valid(c.body) {
		return string(c.body), false
	}
	return base64.StdEncoding.EncodeToString(c.body), true
}

func nilIfEmpty[M ~map[string]V, V any](m M) any {
	if len(m) == 0 {
		return nil
	}
	return m
}

func (c *dpCall) eventV2() obj {
	headers := map[string]string{}
	var cookies []string
	for k, vs := range c.r.Header {
		lk := strings.ToLower(k)
		if lk == "cookie" {
			for _, v := range vs {
				for _, part := range strings.Split(v, ";") {
					if part = strings.TrimSpace(part); part != "" {
						cookies = append(cookies, part)
					}
				}
			}
			continue
		}
		headers[lk] = strings.Join(vs, ",")
	}
	query := map[string]string{}
	for k, vs := range c.r.URL.Query() {
		query[k] = strings.Join(vs, ",")
	}
	body, b64 := c.encodedBody()
	ev := obj{
		"version":               "2.0",
		"routeKey":              str(c.route, "routeKey"),
		"rawPath":               c.r.URL.Path,
		"rawQueryString":        c.r.URL.RawQuery,
		"headers":               headers,
		"queryStringParameters": nilIfEmpty(query),
		"pathParameters":        nilIfEmpty(c.params),
		"stageVariables":        nilIfEmpty(mapOf(c.stage, "stageVariables")),
		"requestContext":        c.requestContext(true),
		"isBase64Encoded":       b64,
	}
	if cookies != nil {
		ev["cookies"] = cookies
	}
	if body != nil {
		ev["body"] = body
	}
	for k, v := range ev {
		if v == nil {
			delete(ev, k)
		}
	}
	return ev
}

func (c *dpCall) eventV1() obj {
	headers := map[string]string{}
	multi := map[string][]string{}
	for k, vs := range c.r.Header {
		headers[k] = vs[len(vs)-1]
		multi[k] = vs
	}
	query := map[string]string{}
	multiQuery := map[string][]string{}
	for k, vs := range c.r.URL.Query() {
		query[k] = vs[len(vs)-1]
		multiQuery[k] = vs
	}
	body, b64 := c.encodedBody()
	return obj{
		"version":                         "1.0",
		"resource":                        c.resourcePath(),
		"path":                            c.r.URL.Path,
		"httpMethod":                      c.r.Method,
		"headers":                         headers,
		"multiValueHeaders":               multi,
		"queryStringParameters":           nilIfEmpty(query),
		"multiValueQueryStringParameters": nilIfEmpty(multiQuery),
		"pathParameters":                  nilIfEmpty(c.params),
		"stageVariables":                  nilIfEmpty(mapOf(c.stage, "stageVariables")),
		"requestContext":                  c.requestContext(false),
		"body":                            body,
		"isBase64Encoded":                 b64,
	}
}

// writeLambdaResponse turns a proxy integration's result into the HTTP
// response. Format 2.0 also accepts any JSON that isn't a response object
// and returns it as the body.
func writeLambdaResponse(w http.ResponseWriter, out []byte, v2 bool) {
	var resp struct {
		StatusCode        *int                `json:"statusCode"`
		Headers           map[string]any      `json:"headers"`
		MultiValueHeaders map[string][]string `json:"multiValueHeaders"`
		Cookies           []string            `json:"cookies"`
		Body              *string             `json:"body"`
		IsBase64Encoded   bool                `json:"isBase64Encoded"`
	}
	trimmed := bytes.TrimSpace(out)
	isObject := len(trimmed) > 0 && trimmed[0] == '{'
	if !isObject || json.Unmarshal(trimmed, &resp) != nil || resp.StatusCode == nil {
		if !v2 {
			dpError(w, http.StatusBadGateway, "Internal Server Error")
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Apigw-Requestid", awsresponses.NextRequestID())
		w.WriteHeader(http.StatusOK)
		w.Write(trimmed)
		return
	}
	for k, v := range resp.Headers {
		w.Header().Set(k, toString(v))
	}
	for k, vs := range resp.MultiValueHeaders {
		for _, v := range vs {
			w.Header().Add(k, v)
		}
	}
	for _, c := range resp.Cookies {
		w.Header().Add("Set-Cookie", c)
	}
	var body []byte
	if resp.Body != nil {
		body = []byte(*resp.Body)
		if resp.IsBase64Encoded {
			dec, err := base64.StdEncoding.DecodeString(*resp.Body)
			if err != nil {
				dpError(w, http.StatusBadGateway, "Internal Server Error")
				return
			}
			body = dec
		}
	}
	if w.Header().Get("Content-Type") == "" && v2 {
		w.Header().Set("Content-Type", "application/json")
	}
	w.Header().Set("Apigw-Requestid", awsresponses.NextRequestID())
	w.WriteHeader(*resp.StatusCode)
	w.Write(body)
}

func toString(v any) string {
	switch t := v.(type) {
	case string:
		return t
	case nil:
		return ""
	}
	b, _ := json.Marshal(v)
	return string(b)
}
