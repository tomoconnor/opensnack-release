// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package router

import (
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"strconv"
	"strings"
	"time"

	"opensnack/internal/util"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/trace"
)

// apiRequests is every AWS API request's duration, by service, action and
// status. No tenant: that would be one series per tenant.
var apiRequests, _ = otel.Meter("opensnack/router").Float64Histogram("opensnack.api.request.duration",
	metric.WithUnit("s"), metric.WithDescription("AWS API requests, by service, action and status"),
	metric.WithExplicitBucketBoundaries(0.001, 0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1, 2.5, 5, 10))

// TelemetryMiddleware names the request's span "<service> <action>" and
// records its duration. It runs after authentication (the tenant is known)
// and reads the action after the handler, as DebugLoggerMiddleware does,
// so a form body is parsed once. Spans carry a hash of the tenant's
// namespace, never its keys.
func TelemetryMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rw := &responseWriter{ResponseWriter: w}
		start := time.Now()
		next.ServeHTTP(rw, r)
		status := rw.status
		if status == 0 {
			status = http.StatusOK
		}
		service, _ := serviceFor(r)
		action := actionFor(r)
		if service == "" {
			service = "unknown"
		}
		apiRequests.Record(r.Context(), time.Since(start).Seconds(), metric.WithAttributes(
			attribute.String("rpc.service", service), attribute.String("rpc.method", action),
			attribute.String("http.response.status_code", strconv.Itoa(status))))
		span := trace.SpanFromContext(r.Context())
		if !span.IsRecording() {
			return
		}
		span.SetName(service + " " + action)
		// The legacy namespace token travels in the User-Agent.
		span.SetAttributes(attribute.String("user_agent.original", redactUserAgent(r.UserAgent())))
		span.SetAttributes(attribute.String("rpc.system", "aws-api"), attribute.String("rpc.service", service), attribute.String("rpc.method", action))
		if ns := util.IdentityFrom(r).Namespace; ns != "" {
			span.SetAttributes(attribute.String("opensnack.namespace_hash", NamespaceHash(ns)))
		}
	})
}

// actionFor is the API operation: X-Amz-Target's (JSON protocols), the
// query or form Action, or for REST APIs (S3, Lambda, Route 53) the method.
func actionFor(r *http.Request) string {
	if t := r.Header.Get("X-Amz-Target"); t != "" {
		if i := strings.LastIndexByte(t, '.'); i >= 0 {
			return t[i+1:]
		}
		return t
	}
	if a, _ := extractQueryAction(r); a != "" {
		return a
	}
	return r.Method
}

// redactUserAgent drops "custom-<namespace>" tokens.
func redactUserAgent(ua string) string {
	parts := strings.Fields(ua)
	for i, p := range parts {
		if strings.HasPrefix(p, "custom-") {
			parts[i] = "custom-[redacted]"
		}
	}
	return strings.Join(parts, " ")
}

// NamespaceHash identifies a tenant in telemetry without naming it.
func NamespaceHash(ns string) string {
	sum := sha256.Sum256([]byte(ns))
	return hex.EncodeToString(sum[:6])
}
