// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

// Package apigatewayv2 is the API Gateway v2 (HTTP and WebSocket APIs)
// control plane, a REST-JSON API under /v2/, and the data plane that serves
// {api_id}.execute-api.{region}.amazonaws.com.
//
// Resources are kept as the JSON objects the API returns, with the AWS
// defaults filled in on create, so reads hand back exactly what was written.
// Child resources are stored under "{apiId}/{childId}".
package apigatewayv2

import (
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"sync"
	"time"

	"opensnack/internal/awsresponses"
	"opensnack/internal/egress"
	"opensnack/internal/resource"
	"opensnack/internal/util"
)

const service = "apigatewayv2"

// obj is a resource as the API returns it. Keys starting with "_" are
// internal and never written out.
type obj = map[string]any

type Handler struct {
	Store resource.Store
	// Invoker runs Lambda proxy (AWS_PROXY) integrations. Nil answers 502.
	Invoker Invoker
	// Targets is where HTTP_PROXY integrations may forward to
	// (OPENSNACK_APIGW_ALLOWED_TARGETS, empty by default).
	Targets *egress.Allowlist
	// Caddy, when set, is told about every API so it can route the
	// execute-api host names (see caddy.go).
	Caddy *Caddy

	client *http.Client
	now    func() time.Time
	// mu serialises writes: auto-deploy rewrites stages while the provider
	// may be tagging or updating them in parallel.
	mu sync.Mutex
}

func NewHandler(store resource.Store) *Handler {
	targets := egress.FromEnv("OPENSNACK_APIGW_ALLOWED_TARGETS")
	h := &Handler{
		Store:   store,
		Targets: targets,
		client:  targets.Client(29 * time.Second),
		now:     time.Now,
	}
	if c := CaddyFromEnv(); c != nil {
		h.Caddy = c
	}
	return h
}

// apiErr is a REST-JSON error: the type goes in X-Amzn-ErrorType.
type apiErr struct {
	status int
	code   string
	msg    string
}

func (e *apiErr) Error() string { return e.code + ": " + e.msg }

func notFound(format string, a ...any) *apiErr {
	return &apiErr{http.StatusNotFound, "NotFoundException", fmt.Sprintf(format, a...)}
}

func badRequest(format string, a ...any) *apiErr {
	return &apiErr{http.StatusBadRequest, "BadRequestException", fmt.Sprintf(format, a...)}
}

func conflict(format string, a ...any) *apiErr {
	return &apiErr{http.StatusConflict, "ConflictException", fmt.Sprintf(format, a...)}
}

func writeErr(w http.ResponseWriter, err error) {
	var e *apiErr
	if !errors.As(err, &e) {
		e = &apiErr{http.StatusInternalServerError, "InternalServerError", err.Error()}
	}
	w.Header().Set("X-Amzn-ErrorType", e.code)
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("X-Amzn-Requestid", awsresponses.NextRequestID())
	w.WriteHeader(e.status)
	json.NewEncoder(w).Encode(map[string]string{"message": e.msg})
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("X-Amzn-Requestid", awsresponses.NextRequestID())
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(public(v))
}

// public drops the internal "_" keys from an object (or a list of them).
func public(v any) any {
	switch t := v.(type) {
	case obj:
		out := obj{}
		for k, x := range t {
			if !strings.HasPrefix(k, "_") {
				out[k] = x
			}
		}
		return out
	case []obj:
		out := make([]any, len(t))
		for i, o := range t {
			out[i] = public(o)
		}
		return out
	}
	return v
}

func newID(n int) string {
	const alphabet = "abcdefghijklmnopqrstuvwxyz0123456789"
	b := make([]byte, n)
	rand.Read(b)
	for i := range b {
		b[i] = alphabet[int(b[i])%len(alphabet)]
	}
	return string(b)
}

func (h *Handler) timestamp() string {
	return h.now().UTC().Format(time.RFC3339)
}

//
// STORE
//

func (h *Handler) load(scope, typ, id string) (obj, error) {
	res, err := h.Store.Get(id, service, typ, scope)
	if err != nil || res == nil || res.ID == "" {
		return nil, errNotStored
	}
	var o obj
	if err := json.Unmarshal(res.Attributes, &o); err != nil {
		return nil, err
	}
	return o, nil
}

var errNotStored = errors.New("not stored")

func (h *Handler) save(scope, typ, id string, o obj, create bool) error {
	b, err := json.Marshal(o)
	if err != nil {
		return err
	}
	res := &resource.Resource{ID: id, Namespace: scope, Service: service, Type: typ, Attributes: b}
	if create {
		return h.Store.Create(res)
	}
	return h.Store.Update(res)
}

// children lists the objects of a type stored under prefix ("{apiId}/").
func (h *Handler) children(scope, typ, prefix string) ([]obj, error) {
	rows, err := h.Store.List(service, typ, scope)
	if err != nil {
		return nil, err
	}
	var out []obj
	for _, row := range rows {
		rest, ok := strings.CutPrefix(row.ID, prefix)
		if !ok || strings.Contains(rest, "/") {
			continue
		}
		var o obj
		if json.Unmarshal(row.Attributes, &o) == nil {
			out = append(out, o)
		}
	}
	return out, nil
}

func (h *Handler) deletePrefix(scope, typ, prefix string) {
	rows, _ := h.Store.List(service, typ, scope)
	for _, row := range rows {
		if strings.HasPrefix(row.ID, prefix) {
			h.Store.Delete(row.ID, service, typ, scope)
		}
	}
}

func readBody(r *http.Request) (obj, error) {
	b, err := io.ReadAll(r.Body)
	if err != nil {
		return nil, badRequest("could not read the request body")
	}
	o := obj{}
	if len(strings.TrimSpace(string(b))) == 0 {
		return o, nil
	}
	if err := json.Unmarshal(b, &o); err != nil {
		return nil, badRequest("Invalid request body: %v", err)
	}
	for k, v := range o {
		if v == nil {
			delete(o, k)
		}
	}
	return o, nil
}

func str(o obj, k string) string {
	s, _ := o[k].(string)
	return s
}

func boolean(o obj, k string) bool {
	b, _ := o[k].(bool)
	return b
}

func mapOf(o obj, k string) obj {
	m, _ := o[k].(map[string]any)
	return m
}

//
// ROUTING
//

func splitPath(escaped string) []string {
	var out []string
	for _, s := range strings.Split(strings.Trim(escaped, "/"), "/") {
		if u, err := url.PathUnescape(s); err == nil {
			s = u
		}
		out = append(out, s)
	}
	return out
}

// ServeHTTP is the control plane: every path under /v2/.
func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	p, ok := strings.CutPrefix(r.URL.EscapedPath(), "/v2/")
	if !ok {
		writeErr(w, notFound("Invalid request path %s; only the v2 (HTTP and WebSocket) API is supported", r.URL.Path))
		return
	}
	segs := splitPath(p)
	scope := util.NamespaceFromHeader(r)
	if r.Method != http.MethodGet {
		h.mu.Lock()
		defer h.mu.Unlock()
	}

	var err error
	switch {
	case segs[0] == "tags" && len(segs) >= 2:
		err = h.tags(w, r, scope, strings.Join(segs[1:], "/"))
	case segs[0] != "apis":
		err = notFound("Invalid request path %s", r.URL.Path)
	case len(segs) == 1:
		switch r.Method {
		case http.MethodGet:
			err = h.listAPIs(w, scope)
		case http.MethodPost:
			err = h.createAPI(w, r, scope)
		default:
			err = notFound("Invalid request path %s", r.URL.Path)
		}
	default:
		err = h.underAPI(w, r, scope, segs[1], segs[2:])
	}
	if err != nil {
		writeErr(w, err)
	}
}

func (h *Handler) underAPI(w http.ResponseWriter, r *http.Request, scope, apiID string, rest []string) error {
	api, err := h.load(scope, "api", apiID)
	if err != nil {
		return notFound("Invalid API identifier specified %s:%s", util.AccountID(r), apiID)
	}
	if len(rest) == 0 {
		switch r.Method {
		case http.MethodGet:
			writeJSON(w, http.StatusOK, api)
			return nil
		case http.MethodPatch:
			return h.updateAPI(w, r, scope, api)
		case http.MethodDelete:
			return h.deleteAPI(w, scope, api)
		}
		return notFound("Invalid request path %s", r.URL.Path)
	}
	if rest[0] == "cors" && r.Method == http.MethodDelete {
		delete(api, "corsConfiguration")
		if err := h.save(scope, "api", apiID, api, false); err != nil {
			return err
		}
		w.WriteHeader(http.StatusNoContent)
		return nil
	}
	k, ok := kinds[rest[0]]
	if !ok {
		return notFound("Invalid request path %s", r.URL.Path)
	}
	return h.child(w, r, scope, api, k, apiID+"/", rest[1:])
}

//
// APIS
//

func (h *Handler) listAPIs(w http.ResponseWriter, scope string) error {
	rows, err := h.Store.List(service, "api", scope)
	if err != nil {
		return err
	}
	items := []obj{}
	for _, row := range rows {
		var o obj
		if json.Unmarshal(row.Attributes, &o) == nil {
			items = append(items, o)
		}
	}
	writeJSON(w, http.StatusOK, obj{"items": public(items)})
	return nil
}

func apiARN(region, apiID string) string {
	return fmt.Sprintf("arn:aws:apigateway:%s::/apis/%s", region, apiID)
}

func (h *Handler) createAPI(w http.ResponseWriter, r *http.Request, scope string) error {
	in, err := readBody(r)
	if err != nil {
		return err
	}
	name, proto := str(in, "name"), str(in, "protocolType")
	if name == "" {
		return badRequest("Invalid API name specified")
	}
	if proto != "HTTP" && proto != "WEBSOCKET" {
		return badRequest("Invalid protocol type specified: %q", proto)
	}
	if proto == "WEBSOCKET" && str(in, "routeSelectionExpression") == "" {
		return badRequest("RouteSelectionExpression is required for WEBSOCKET APIs")
	}

	region := util.Region(r)
	id := h.uniqueAPIID()
	scheme := "https"
	if proto == "WEBSOCKET" {
		scheme = "wss"
	}
	api := obj{
		"apiId":                     id,
		"apiEndpoint":               fmt.Sprintf("%s://%s.execute-api.%s.amazonaws.com", scheme, id, region),
		"apiKeySelectionExpression": "$request.header.x-api-key",
		"createdDate":               h.timestamp(),
		"disableExecuteApiEndpoint": false,
		"ipAddressType":             "ipv4",
		"name":                      name,
		"protocolType":              proto,
		"routeSelectionExpression":  "$request.method $request.path",
		"tags":                      obj{},
		"_accountId":                util.AccountID(r),
	}
	for _, k := range []string{"apiKeySelectionExpression", "corsConfiguration", "description",
		"disableExecuteApiEndpoint", "ipAddressType", "routeSelectionExpression", "tags", "version"} {
		if v, ok := in[k]; ok {
			api[k] = v
		}
	}
	// Quick create: an HTTP API with a target gets an integration, a route
	// and an auto-deployed $default stage, all marked as managed.
	target := str(in, "target")
	if target != "" && proto == "HTTP" {
		api["apiGatewayManaged"] = true
	}
	if err := h.save(scope, "api", id, api, true); err != nil {
		return err
	}
	if target != "" && proto == "HTTP" {
		h.quickCreate(scope, api, in)
	}
	h.sync(scope, id)
	writeJSON(w, http.StatusCreated, api)
	return nil
}

func (h *Handler) quickCreate(scope string, api obj, in obj) {
	apiID := str(api, "apiId")
	target := str(in, "target")
	integ := obj{
		"integrationId":     newID(7),
		"apiGatewayManaged": true,
		"connectionType":    "INTERNET",
		"integrationUri":    target,
		"timeoutInMillis":   30000.0,
	}
	if strings.HasPrefix(target, "arn:") {
		integ["integrationType"] = "AWS_PROXY"
		integ["integrationMethod"] = "POST"
		integ["payloadFormatVersion"] = "2.0"
	} else {
		integ["integrationType"] = "HTTP_PROXY"
		integ["integrationMethod"] = "ANY"
		integ["payloadFormatVersion"] = "1.0"
	}
	if v := str(in, "credentialsArn"); v != "" {
		integ["credentialsArn"] = v
	}
	h.save(scope, "integration", apiID+"/"+str(integ, "integrationId"), integ, true)

	routeKey := str(in, "routeKey")
	if routeKey == "" {
		routeKey = "$default"
	}
	route := obj{
		"routeId":           newID(7),
		"apiGatewayManaged": true,
		"apiKeyRequired":    false,
		"authorizationType": "NONE",
		"routeKey":          routeKey,
		"target":            "integrations/" + str(integ, "integrationId"),
	}
	h.save(scope, "route", apiID+"/"+str(route, "routeId"), route, true)

	stage := obj{
		"stageName":            "$default",
		"apiGatewayManaged":    true,
		"autoDeploy":           true,
		"createdDate":          h.timestamp(),
		"lastUpdatedDate":      h.timestamp(),
		"defaultRouteSettings": obj{"detailedMetricsEnabled": false},
		"routeSettings":        obj{},
		"stageVariables":       obj{},
		"tags":                 obj{},
	}
	h.save(scope, "stage", apiID+"/$default", stage, true)
	h.autoDeploy(scope, apiID)
}

// uniqueAPIID picks an ID no other tenant uses, since the data plane finds
// APIs by ID alone.
func (h *Handler) uniqueAPIID() string {
	insp, _ := h.Store.(resource.Inspector)
	for {
		id := newID(10)
		if insp == nil {
			return id
		}
		if rows, err := insp.Find(resource.Filter{Service: service, Type: "api", ID: id}); err == nil && len(rows) == 0 {
			return id
		}
	}
}

func (h *Handler) updateAPI(w http.ResponseWriter, r *http.Request, scope string, api obj) error {
	in, err := readBody(r)
	if err != nil {
		return err
	}
	for _, k := range []string{"apiKeySelectionExpression", "corsConfiguration", "description",
		"disableExecuteApiEndpoint", "ipAddressType", "name", "routeSelectionExpression", "version"} {
		if v, ok := in[k]; ok {
			api[k] = v
		}
	}
	// An empty CORS object clears it, like DeleteCorsConfiguration.
	if c, ok := api["corsConfiguration"].(map[string]any); ok && len(c) == 0 {
		delete(api, "corsConfiguration")
	}
	id := str(api, "apiId")
	if err := h.save(scope, "api", id, api, false); err != nil {
		return err
	}
	h.autoDeploy(scope, id)
	writeJSON(w, http.StatusOK, api)
	return nil
}

func (h *Handler) deleteAPI(w http.ResponseWriter, scope string, api obj) error {
	id := str(api, "apiId")
	for _, k := range kinds {
		h.deletePrefix(scope, k.typ, id+"/")
		if k.sub != nil {
			h.deletePrefix(scope, k.sub.typ, id+"/")
		}
	}
	if err := h.Store.Delete(id, service, "api", scope); err != nil {
		return err
	}
	if h.Caddy != nil {
		go h.Caddy.Remove(id)
	}
	w.WriteHeader(http.StatusNoContent)
	return nil
}

//
// TAGS
//

// tagTarget maps a resource ARN onto the stored object it tags.
func (h *Handler) tagTarget(scope, arn string) (typ, id string, o obj, err error) {
	_, path, ok := strings.Cut(arn, "::")
	if !ok {
		return "", "", nil, badRequest("Invalid resource ARN %s", arn)
	}
	segs := strings.Split(strings.Trim(path, "/"), "/")
	switch {
	case len(segs) == 2 && segs[0] == "apis":
		typ, id = "api", segs[1]
	case len(segs) == 4 && segs[0] == "apis" && segs[2] == "stages":
		typ, id = "stage", segs[1]+"/"+segs[3]
	default:
		return "", "", nil, badRequest("Tagging is not supported for %s", arn)
	}
	o, err = h.load(scope, typ, id)
	if err != nil {
		return "", "", nil, notFound("Unable to find resource %s", arn)
	}
	return typ, id, o, nil
}

func (h *Handler) tags(w http.ResponseWriter, r *http.Request, scope, arn string) error {
	typ, id, o, err := h.tagTarget(scope, arn)
	if err != nil {
		return err
	}
	tags := mapOf(o, "tags")
	if tags == nil {
		tags = obj{}
	}
	switch r.Method {
	case http.MethodGet:
		writeJSON(w, http.StatusOK, obj{"tags": tags})
		return nil
	case http.MethodPost:
		in, err := readBody(r)
		if err != nil {
			return err
		}
		for k, v := range mapOf(in, "tags") {
			tags[k] = v
		}
		o["tags"] = tags
		if err := h.save(scope, typ, id, o, false); err != nil {
			return err
		}
		writeJSON(w, http.StatusCreated, obj{})
		return nil
	case http.MethodDelete:
		for _, k := range r.URL.Query()["tagKeys"] {
			delete(tags, k)
		}
		o["tags"] = tags
		if err := h.save(scope, typ, id, o, false); err != nil {
			return err
		}
		w.WriteHeader(http.StatusNoContent)
		return nil
	}
	return notFound("Invalid request path %s", r.URL.Path)
}

//
// CHILD RESOURCES
//

// kind describes a child collection of an API (routes, integrations...).
type kind struct {
	typ     string // store type
	idField string // "routeId"
	idLen   int    // 0: the ID is the "name" field (stages)
	// sub is the nested collection (route responses, integration responses).
	sub     *kind
	subPath string
	// create fills defaults and validates; update merges a PATCH body.
	create func(h *Handler, scope string, api obj, parent string, in obj) (obj, error)
	update func(h *Handler, scope string, api obj, parent string, cur, in obj) error
	// remove checks a delete is allowed.
	remove func(h *Handler, scope string, api obj, parent string, cur obj) error
	// redeploys is true when a change should trigger auto-deploy.
	redeploys bool
}

var kinds map[string]*kind

func init() {
	routeResponses := &kind{typ: "routeresponse", idField: "routeResponseId", idLen: 6,
		create: createRouteResponse, update: mergeFields, redeploys: true}
	integrationResponses := &kind{typ: "integrationresponse", idField: "integrationResponseId", idLen: 6,
		create: createIntegrationResponse, update: mergeFields, redeploys: true}
	kinds = map[string]*kind{
		"routes": {typ: "route", idField: "routeId", idLen: 7, sub: routeResponses, subPath: "routeresponses",
			create: createRoute, update: updateRoute, redeploys: true},
		"integrations": {typ: "integration", idField: "integrationId", idLen: 7, sub: integrationResponses, subPath: "integrationresponses",
			create: createIntegration, update: updateIntegration, remove: removeIntegration, redeploys: true},
		"stages": {typ: "stage", idField: "stageName",
			create: createStage, update: updateStage},
		"deployments": {typ: "deployment", idField: "deploymentId", idLen: 6,
			create: createDeployment, update: mergeFields, remove: removeDeployment},
	}
}

func (h *Handler) child(w http.ResponseWriter, r *http.Request, scope string, api obj, k *kind, prefix string, rest []string) error {
	apiID := str(api, "apiId")
	if len(rest) == 0 {
		switch r.Method {
		case http.MethodGet:
			items, err := h.children(scope, k.typ, prefix)
			if err != nil {
				return err
			}
			sort.Slice(items, func(i, j int) bool { return str(items[i], k.idField) < str(items[j], k.idField) })
			if items == nil {
				items = []obj{}
			}
			writeJSON(w, http.StatusOK, obj{"items": public(items)})
			return nil
		case http.MethodPost:
			in, err := readBody(r)
			if err != nil {
				return err
			}
			o, err := k.create(h, scope, api, prefix, in)
			if err != nil {
				return err
			}
			if err := h.save(scope, k.typ, prefix+str(o, k.idField), o, true); err != nil {
				return conflict("%s already exists", str(o, k.idField))
			}
			h.afterChange(scope, apiID, k)
			if k.typ == "stage" && boolean(o, "autoDeploy") {
				h.autoDeploy(scope, apiID)
				o, _ = h.load(scope, k.typ, prefix+str(o, k.idField))
			}
			writeJSON(w, http.StatusCreated, o)
			return nil
		}
		return notFound("Invalid request path %s", r.URL.Path)
	}

	id := rest[0]
	cur, err := h.load(scope, k.typ, prefix+id)
	if err != nil {
		return notFound("Invalid %s identifier specified %s", strings.TrimSuffix(k.idField, "Id"), id)
	}

	if len(rest) > 1 {
		switch {
		case k.sub != nil && rest[1] == k.subPath:
			return h.child(w, r, scope, api, k.sub, prefix+id+"/", rest[2:])
		case r.Method == http.MethodDelete && k.typ == "route" && rest[1] == "requestparameters" && len(rest) == 3:
			if p := mapOf(cur, "requestParameters"); p != nil {
				delete(p, rest[2])
			}
		case r.Method == http.MethodDelete && k.typ == "stage" && rest[1] == "accesslogsettings":
			delete(cur, "accessLogSettings")
		case r.Method == http.MethodDelete && k.typ == "stage" && rest[1] == "routesettings" && len(rest) == 3:
			if s := mapOf(cur, "routeSettings"); s != nil {
				if _, ok := s[rest[2]]; !ok {
					return notFound("Unable to find route settings for %s", rest[2])
				}
				delete(s, rest[2])
			}
		default:
			return notFound("Invalid request path %s", r.URL.Path)
		}
		if err := h.save(scope, k.typ, prefix+id, cur, false); err != nil {
			return err
		}
		w.WriteHeader(http.StatusNoContent)
		return nil
	}

	switch r.Method {
	case http.MethodGet:
		writeJSON(w, http.StatusOK, cur)
		return nil
	case http.MethodPatch:
		in, err := readBody(r)
		if err != nil {
			return err
		}
		if err := k.update(h, scope, api, prefix, cur, in); err != nil {
			return err
		}
		if err := h.save(scope, k.typ, prefix+id, cur, false); err != nil {
			return err
		}
		h.afterChange(scope, apiID, k)
		if k.typ == "stage" && boolean(cur, "autoDeploy") && boolean(in, "autoDeploy") {
			h.autoDeploy(scope, apiID)
			cur, _ = h.load(scope, k.typ, prefix+id)
		}
		writeJSON(w, http.StatusOK, cur)
		return nil
	case http.MethodDelete:
		if k.remove != nil {
			if err := k.remove(h, scope, api, prefix, cur); err != nil {
				return err
			}
		}
		if k.sub != nil {
			h.deletePrefix(scope, k.sub.typ, prefix+id+"/")
		}
		if err := h.Store.Delete(prefix+id, service, k.typ, scope); err != nil {
			return err
		}
		h.afterChange(scope, apiID, k)
		w.WriteHeader(http.StatusNoContent)
		return nil
	}
	return notFound("Invalid request path %s", r.URL.Path)
}

func (h *Handler) afterChange(scope, apiID string, k *kind) {
	if k.redeploys {
		h.autoDeploy(scope, apiID)
	}
	h.sync(scope, apiID)
}

func (h *Handler) sync(scope, apiID string) {
	if h.Caddy != nil {
		h.pushAPI(scope, apiID, true)
	}
}

// mergeFields applies a PATCH body field by field.
func mergeFields(_ *Handler, _ string, _ obj, _ string, cur, in obj) error {
	for k, v := range in {
		cur[k] = v
	}
	return nil
}

// mergeMap merges a map-valued field, deleting the keys whose new value is
// "empty" (how the provider removes stage variables and parameters).
func mergeMap(cur obj, field string, in obj, empty func(any) bool) {
	add, ok := in[field].(map[string]any)
	if !ok {
		return
	}
	m := mapOf(cur, field)
	if m == nil {
		m = obj{}
	}
	for k, v := range add {
		if empty(v) {
			delete(m, k)
		} else {
			m[k] = v
		}
	}
	cur[field] = m
	delete(in, field)
}

func emptyString(v any) bool { s, ok := v.(string); return ok && s == "" }
func emptyMap(v any) bool    { m, ok := v.(map[string]any); return ok && len(m) == 0 }
func never(any) bool         { return false }

//
// ROUTES
//

func (h *Handler) checkTarget(scope, apiID, target string) error {
	if target == "" {
		return nil
	}
	id, ok := strings.CutPrefix(target, "integrations/")
	if !ok {
		return badRequest("Invalid target %s; expected integrations/{integrationId}", target)
	}
	if _, err := h.load(scope, "integration", apiID+"/"+id); err != nil {
		return badRequest("Unable to find integration %s", id)
	}
	return nil
}

func (h *Handler) checkRouteKey(scope string, api obj, prefix, key, self string) error {
	if key == "" {
		return badRequest("RouteKey is required")
	}
	if str(api, "protocolType") == "HTTP" && key != "$default" {
		method, path, ok := strings.Cut(key, " ")
		if !ok || method == "" || !strings.HasPrefix(path, "/") {
			return badRequest("Invalid route key %q; expected \"METHOD /path\" or \"$default\"", key)
		}
	}
	routes, _ := h.children(scope, "route", prefix)
	for _, rt := range routes {
		if str(rt, "routeKey") == key && str(rt, "routeId") != self {
			return conflict("Route with key %s already exists for this API", key)
		}
	}
	return nil
}

func createRoute(h *Handler, scope string, api obj, prefix string, in obj) (obj, error) {
	key := str(in, "routeKey")
	if err := h.checkRouteKey(scope, api, prefix, key, ""); err != nil {
		return nil, err
	}
	if err := h.checkTarget(scope, str(api, "apiId"), str(in, "target")); err != nil {
		return nil, err
	}
	o := obj{
		"routeId":           newID(7),
		"apiKeyRequired":    false,
		"authorizationType": "NONE",
	}
	for k, v := range in {
		o[k] = v
	}
	return o, nil
}

func updateRoute(h *Handler, scope string, api obj, prefix string, cur, in obj) error {
	if key, ok := in["routeKey"].(string); ok {
		if err := h.checkRouteKey(scope, api, prefix, key, str(cur, "routeId")); err != nil {
			return err
		}
	}
	if t, ok := in["target"].(string); ok {
		if err := h.checkTarget(scope, str(api, "apiId"), t); err != nil {
			return err
		}
		if t == "" {
			delete(cur, "target")
			delete(in, "target")
		}
	}
	mergeMap(cur, "requestParameters", in, never)
	return mergeFields(h, scope, api, prefix, cur, in)
}

func createRouteResponse(_ *Handler, _ string, _ obj, _ string, in obj) (obj, error) {
	if str(in, "routeResponseKey") == "" {
		return nil, badRequest("RouteResponseKey is required")
	}
	o := obj{"routeResponseId": newID(6)}
	for k, v := range in {
		o[k] = v
	}
	return o, nil
}

//
// INTEGRATIONS
//

var integrationTypes = map[string]bool{"AWS": true, "AWS_PROXY": true, "HTTP": true, "HTTP_PROXY": true, "MOCK": true}

func createIntegration(_ *Handler, _ string, api obj, _ string, in obj) (obj, error) {
	t := str(in, "integrationType")
	if !integrationTypes[t] {
		return nil, badRequest("Invalid integration type %q", t)
	}
	http := str(api, "protocolType") == "HTTP"
	if http && t != "AWS_PROXY" && t != "HTTP_PROXY" {
		return nil, badRequest("Integration type %s is not supported for HTTP APIs", t)
	}
	if (t == "HTTP_PROXY" || t == "AWS_PROXY") && str(in, "integrationUri") == "" && str(in, "integrationSubtype") == "" {
		return nil, badRequest("IntegrationUri is required for %s integrations", t)
	}
	o := obj{
		"integrationId":  newID(7),
		"connectionType": "INTERNET",
	}
	if http {
		o["timeoutInMillis"] = 30000.0
		o["payloadFormatVersion"] = "1.0"
	} else {
		o["timeoutInMillis"] = 29000.0
		o["passthroughBehavior"] = "WHEN_NO_MATCH"
		o["integrationResponseSelectionExpression"] = "${integration.response.statuscode}"
		if t == "AWS_PROXY" || t == "HTTP_PROXY" {
			delete(o, "integrationResponseSelectionExpression")
		}
	}
	for k, v := range in {
		o[k] = v
	}
	if http && t == "AWS_PROXY" && str(in, "integrationMethod") == "" {
		o["integrationMethod"] = "POST"
	}
	dropEmptyMaps(o, "requestParameters", "requestTemplates", "responseParameters")
	return o, nil
}

func dropEmptyMaps(o obj, fields ...string) {
	for _, f := range fields {
		if m, ok := o[f].(map[string]any); ok && len(m) == 0 {
			delete(o, f)
		}
	}
}

func updateIntegration(h *Handler, scope string, api obj, prefix string, cur, in obj) error {
	if t, ok := in["integrationType"].(string); ok && t != str(cur, "integrationType") {
		return badRequest("The integration type cannot be changed")
	}
	mergeMap(cur, "requestParameters", in, emptyString)
	mergeMap(cur, "responseParameters", in, emptyMap)
	if err := mergeFields(h, scope, api, prefix, cur, in); err != nil {
		return err
	}
	dropEmptyMaps(cur, "requestParameters", "requestTemplates", "responseParameters")
	return nil
}

func removeIntegration(h *Handler, scope string, api obj, prefix string, cur obj) error {
	routes, _ := h.children(scope, "route", prefix)
	for _, rt := range routes {
		if str(rt, "target") == "integrations/"+str(cur, "integrationId") {
			return conflict("Cannot delete Integration %s because it is in use by Route %s",
				str(cur, "integrationId"), str(rt, "routeId"))
		}
	}
	return nil
}

func createIntegrationResponse(_ *Handler, _ string, _ obj, _ string, in obj) (obj, error) {
	if str(in, "integrationResponseKey") == "" {
		return nil, badRequest("IntegrationResponseKey is required")
	}
	o := obj{"integrationResponseId": newID(6)}
	for k, v := range in {
		o[k] = v
	}
	return o, nil
}

//
// STAGES
//

func createStage(h *Handler, scope string, api obj, prefix string, in obj) (obj, error) {
	name := str(in, "stageName")
	if name == "" {
		return nil, badRequest("StageName is required")
	}
	if _, err := h.load(scope, "stage", prefix+name); err == nil {
		return nil, conflict("Stage already exists")
	}
	if d := str(in, "deploymentId"); d != "" {
		if _, err := h.load(scope, "deployment", prefix+d); err != nil {
			return nil, badRequest("Deployment %s does not exist", d)
		}
	}
	now := h.timestamp()
	o := obj{
		"stageName":       name,
		"autoDeploy":      false,
		"createdDate":     now,
		"lastUpdatedDate": now,
		"routeSettings":   obj{},
		"stageVariables":  obj{},
		"tags":            obj{},
	}
	if str(api, "protocolType") == "HTTP" {
		o["defaultRouteSettings"] = obj{"detailedMetricsEnabled": false}
	} else {
		o["defaultRouteSettings"] = obj{"dataTraceEnabled": false, "detailedMetricsEnabled": false, "loggingLevel": "OFF"}
	}
	for k, v := range in {
		o[k] = v
	}
	stripEmptyVars(o)
	return o, nil
}

func stripEmptyVars(o obj) {
	for k, v := range mapOf(o, "stageVariables") {
		if emptyString(v) {
			delete(mapOf(o, "stageVariables"), k)
		}
	}
}

func updateStage(h *Handler, scope string, api obj, prefix string, cur, in obj) error {
	if d, ok := in["deploymentId"].(string); ok && d != "" {
		if _, err := h.load(scope, "deployment", prefix+d); err != nil {
			return badRequest("Deployment %s does not exist", d)
		}
	}
	mergeMap(cur, "stageVariables", in, emptyString)
	mergeMap(cur, "routeSettings", in, never)
	if err := mergeFields(h, scope, api, prefix, cur, in); err != nil {
		return err
	}
	cur["lastUpdatedDate"] = h.timestamp()
	return nil
}

//
// DEPLOYMENTS
//

func (h *Handler) newDeployment(description string, auto bool) obj {
	o := obj{
		"deploymentId":     newID(6),
		"autoDeployed":     auto,
		"createdDate":      h.timestamp(),
		"deploymentStatus": "DEPLOYED",
	}
	if description != "" {
		o["description"] = description
	}
	return o
}

func createDeployment(h *Handler, scope string, api obj, prefix string, in obj) (obj, error) {
	routes, _ := h.children(scope, "route", prefix)
	if len(routes) == 0 {
		return nil, badRequest("Unable to deploy API because no routes exist in this API")
	}
	var stage obj
	if name := str(in, "stageName"); name != "" {
		var err error
		if stage, err = h.load(scope, "stage", prefix+name); err != nil {
			return nil, notFound("Invalid stage identifier specified")
		}
		if boolean(stage, "autoDeploy") {
			return nil, badRequest("Stage %s has autoDeploy enabled; deployments are created automatically", name)
		}
	}
	o := h.newDeployment(str(in, "description"), false)
	if stage != nil {
		stage["deploymentId"] = o["deploymentId"]
		stage["lastUpdatedDate"] = h.timestamp()
		h.save(scope, "stage", prefix+str(stage, "stageName"), stage, false)
	}
	return o, nil
}

func removeDeployment(h *Handler, scope string, _ obj, prefix string, cur obj) error {
	stages, _ := h.children(scope, "stage", prefix)
	for _, s := range stages {
		if str(s, "deploymentId") == str(cur, "deploymentId") {
			return badRequest("Active stages pointing to this deployment must be moved or deleted")
		}
	}
	return nil
}

// autoDeploy gives every auto-deploy stage of the API a fresh deployment,
// as API Gateway does after each change, and drops the one it replaces.
func (h *Handler) autoDeploy(scope, apiID string) {
	prefix := apiID + "/"
	stages, _ := h.children(scope, "stage", prefix)
	routes, _ := h.children(scope, "route", prefix)
	if len(routes) == 0 {
		return
	}
	for _, s := range stages {
		if !boolean(s, "autoDeploy") {
			continue
		}
		old := str(s, "deploymentId")
		d := h.newDeployment("Automatic deployment triggered by changes to the Api configuration", true)
		if h.save(scope, "deployment", prefix+str(d, "deploymentId"), d, true) != nil {
			continue
		}
		s["deploymentId"] = d["deploymentId"]
		s["lastDeploymentStatusMessage"] = fmt.Sprintf("Successfully deployed stage with deployment ID '%s'", d["deploymentId"])
		s["lastUpdatedDate"] = h.timestamp()
		h.save(scope, "stage", prefix+str(s, "stageName"), s, false)
		if old != "" {
			h.dropAutoDeployment(scope, prefix, old)
		}
	}
}

func (h *Handler) dropAutoDeployment(scope, prefix, id string) {
	d, err := h.load(scope, "deployment", prefix+id)
	if err != nil || !boolean(d, "autoDeployed") {
		return
	}
	stages, _ := h.children(scope, "stage", prefix)
	for _, s := range stages {
		if str(s, "deploymentId") == id {
			return
		}
	}
	h.Store.Delete(prefix+id, service, "deployment", scope)
}
