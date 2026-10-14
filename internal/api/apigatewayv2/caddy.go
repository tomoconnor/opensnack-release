// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package apigatewayv2

// Caddy as the execute-api front door.
//
// When OPENSNACK_CADDY_ADMIN_URL is set, every API gets one route in a Caddy
// server (pushed through Caddy's admin API, tagged "@id": opensnack-apigw-{id})
// that matches its {api_id}.execute-api.{region}.* host names:
//
//   - Normally the route reverse-proxies to opensnack itself
//     (OPENSNACK_CADDY_UPSTREAM), and ServeDataPlane does the route matching,
//     stages, CORS and integrations. Caddy then only supplies TLS, the host
//     names and access logs.
//   - An API whose only route is $default on the $default stage, with a
//     plain HTTP_PROXY integration to an allowlisted target, is proxied by
//     Caddy straight to that target, without passing through opensnack.
//
// The trade-off: compiling every API into native Caddy routes would take
// opensnack out of the request path entirely, but Caddy's matchers can't
// express API Gateway's route precedence, stage variables, payload format
// 2.0 events or Lambda invocation without a plugin, and two
// implementations of the semantics would drift. So Caddy carries the
// traffic and opensnack keeps the semantics; the direct case is the one
// where the two are trivially the same. Without Caddy, opensnack serves the
// execute-api hosts itself (the router sends them to ServeDataPlane), which
// is also what the in-cluster DNS can point at directly.
//
// Pushes are best-effort and asynchronous: a failure is logged, and the
// next change to the API (or a restart, via SyncAll) pushes again.

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"opensnack/internal/egress"
	"opensnack/internal/resource"
	"opensnack/internal/util"

	"go.uber.org/zap"
)

type Caddy struct {
	// AdminURL is Caddy's admin endpoint, e.g. http://caddy:2019.
	AdminURL string
	// Server is the apps.http.servers entry the routes go into.
	Server string
	// Upstream is how Caddy reaches opensnack (host:port).
	Upstream string
	Client   *http.Client
}

// CaddyFromEnv reads OPENSNACK_CADDY_ADMIN_URL, OPENSNACK_CADDY_SERVER
// (default srv0) and OPENSNACK_CADDY_UPSTREAM (default localhost:4566).
// It returns nil when no admin URL is set.
func CaddyFromEnv() *Caddy {
	admin := os.Getenv("OPENSNACK_CADDY_ADMIN_URL")
	if admin == "" {
		return nil
	}
	c := &Caddy{
		AdminURL: strings.TrimRight(admin, "/"),
		Server:   os.Getenv("OPENSNACK_CADDY_SERVER"),
		Upstream: os.Getenv("OPENSNACK_CADDY_UPSTREAM"),
		Client:   &http.Client{Timeout: 5 * time.Second},
	}
	if c.Server == "" {
		c.Server = "srv0"
	}
	if c.Upstream == "" {
		c.Upstream = "localhost:4566"
	}
	return c
}

func caddyID(apiID string) string { return "opensnack-apigw-" + apiID }

// Route is the Caddy route object for an API.
func (c *Caddy) Route(api obj, routes, integrations, stages []obj, targets *egress.Allowlist) map[string]any {
	apiID := str(api, "apiId")
	region := ""
	if u, err := url.Parse(str(api, "apiEndpoint")); err == nil {
		_, region, _ = ParseHost(u.Host)
	}
	var hosts []string
	for _, s := range util.HostSuffixes() {
		hosts = append(hosts, fmt.Sprintf("%s.execute-api.%s.%s", apiID, region, s))
	}

	handle := []any{map[string]any{
		"handler":   "reverse_proxy",
		"upstreams": []any{map[string]any{"dial": c.Upstream}},
	}}
	if target, ok := directTarget(routes, integrations, stages, targets); ok {
		handle = target
	}
	return map[string]any{
		"@id":      caddyID(apiID),
		"match":    []any{map[string]any{"host": hosts}},
		"handle":   handle,
		"terminal": true,
	}
}

// directTarget returns the handlers that proxy straight to an HTTP_PROXY
// target, when the API is simple enough for Caddy to serve on its own.
func directTarget(routes, integrations, stages []obj, targets *egress.Allowlist) ([]any, bool) {
	if len(routes) != 1 || str(routes[0], "routeKey") != "$default" || len(stages) != 1 ||
		str(stages[0], "stageName") != "$default" || str(stages[0], "deploymentId") == "" {
		return nil, false
	}
	id, _ := strings.CutPrefix(str(routes[0], "target"), "integrations/")
	for _, in := range integrations {
		if str(in, "integrationId") != id || str(in, "integrationType") != "HTTP_PROXY" {
			continue
		}
		uri := str(in, "integrationUri")
		u, err := url.Parse(uri)
		if err != nil || strings.Contains(uri, "{") || !targets.Allows(uri) {
			return nil, false
		}
		port := u.Port()
		if port == "" {
			port = map[string]string{"http": "80", "https": "443"}[u.Scheme]
		}
		path := u.EscapedPath()
		if path == "" {
			path = "/"
		}
		proxy := map[string]any{
			"handler":   "reverse_proxy",
			"upstreams": []any{map[string]any{"dial": net.JoinHostPort(u.Hostname(), port)}},
			"headers": map[string]any{"request": map[string]any{
				"set": map[string]any{"Host": []any{u.Host}},
			}},
		}
		if u.Scheme == "https" {
			proxy["transport"] = map[string]any{"protocol": "http", "tls": map[string]any{}}
		}
		return []any{
			map[string]any{"handler": "rewrite", "uri": path + "?{http.request.uri.query}"},
			proxy,
		}, true
	}
	return nil, false
}

func (c *Caddy) do(method, path string, body any) (int, error) {
	var buf bytes.Buffer
	if body != nil {
		json.NewEncoder(&buf).Encode(body)
	}
	req, err := http.NewRequest(method, c.AdminURL+path, &buf)
	if err != nil {
		return 0, err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.Client.Do(req)
	if err != nil {
		return 0, err
	}
	resp.Body.Close()
	return resp.StatusCode, nil
}

// Push replaces the API's route in place, or inserts it at the front of
// the server's routes the first time.
func (c *Caddy) Push(api obj, routes, integrations, stages []obj, targets *egress.Allowlist) {
	route := c.Route(api, routes, integrations, stages, targets)
	id := caddyID(str(api, "apiId"))
	status, err := c.do(http.MethodPatch, "/id/"+id, route)
	if err == nil && status >= 300 {
		status, err = c.do(http.MethodPut, "/config/apps/http/servers/"+c.Server+"/routes/0", route)
	}
	if err == nil && status >= 300 {
		err = fmt.Errorf("caddy admin answered %d", status)
	}
	if err != nil {
		zap.L().Warn("apigateway: caddy push failed", zap.String("api", str(api, "apiId")), zap.Error(err))
	}
}

func (c *Caddy) Remove(apiID string) {
	status, err := c.do(http.MethodDelete, "/id/"+caddyID(apiID), nil)
	if err == nil && status >= 300 && status != http.StatusNotFound {
		err = fmt.Errorf("caddy admin answered %d", status)
	}
	if err != nil {
		zap.L().Warn("apigateway: caddy remove failed", zap.String("api", apiID), zap.Error(err))
	}
}

// SyncAll pushes every stored API, so a restarted Caddy (or opensnack)
// converges.
func (h *Handler) SyncAll() {
	insp, ok := h.Store.(resource.Inspector)
	if h.Caddy == nil || !ok {
		return
	}
	rows, err := insp.Find(resource.Filter{Service: service, Type: "api"})
	if err != nil {
		return
	}
	for _, row := range rows {
		h.pushAPI(resource.JoinScope(row.Namespace, row.Region), row.ID, false)
	}
}

func (h *Handler) pushAPI(scope, apiID string, async bool) {
	api, err := h.load(scope, "api", apiID)
	if err != nil {
		return
	}
	routes, _ := h.children(scope, "route", apiID+"/")
	integrations, _ := h.children(scope, "integration", apiID+"/")
	stages, _ := h.children(scope, "stage", apiID+"/")
	if async {
		go h.Caddy.Push(api, routes, integrations, stages, h.Targets)
	} else {
		h.Caddy.Push(api, routes, integrations, stages, h.Targets)
	}
}
