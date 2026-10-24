// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package router_test

import (
	"context"
	"net/http/httptest"
	"strings"
	"testing"

	"opensnack/internal/router"

	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/propagation"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
)

// With parentbased_always_off, a request is traced only when it arrives with
// a sampled traceparent; every request counts in the metrics either way.
func TestTelemetry(t *testing.T) {
	rec := tracetest.NewSpanRecorder()
	tp := sdktrace.NewTracerProvider(sdktrace.WithSampler(sdktrace.ParentBased(sdktrace.NeverSample())), sdktrace.WithSpanProcessor(rec))
	reader := sdkmetric.NewManualReader()
	otel.SetMeterProvider(sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader)))
	t.Cleanup(func() { otel.SetMeterProvider(sdkmetric.NewMeterProvider()) })

	h := otelhttp.NewHandler(router.New(NewMockStore()), "opensnack",
		otelhttp.WithTracerProvider(tp), otelhttp.WithPropagators(propagation.TraceContext{}))
	send := func(queue, traceparent string) {
		req := httptest.NewRequest("POST", "/", strings.NewReader(`{"QueueName":"`+queue+`"}`))
		req.Header.Set("X-Amz-Target", "AmazonSQS.CreateQueue")
		req.Header.Set("Content-Type", "application/x-amz-json-1.0")
		req.Header.Set("User-Agent", "aws-cli/2.15.0 custom-ns1")
		if traceparent != "" {
			req.Header.Set("Traceparent", traceparent)
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, req)
		if w.Code != 200 {
			t.Fatalf("status %d: %s", w.Code, w.Body)
		}
	}
	send("untraced", "")
	send("traced", "00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01")

	spans := rec.Ended()
	if len(spans) != 1 {
		t.Fatalf("%d spans, want only the request with a traceparent", len(spans))
	}
	if spans[0].Name() != "sqs CreateQueue" {
		t.Errorf("span name %q", spans[0].Name())
	}
	var hash string
	for _, kv := range spans[0].Attributes() {
		if kv.Key == "opensnack.namespace_hash" {
			hash = kv.Value.AsString()
		}
		if strings.Contains(kv.Value.Emit(), "ns1") {
			t.Errorf("tenant name in %s=%s", kv.Key, kv.Value.Emit())
		}
	}
	if hash != router.NamespaceHash("ns1") {
		t.Errorf("namespace hash %q", hash)
	}

	var rm metricdata.ResourceMetrics
	if err := reader.Collect(context.Background(), &rm); err != nil {
		t.Fatal(err)
	}
	var count uint64
	for _, sm := range rm.ScopeMetrics {
		for _, m := range sm.Metrics {
			if m.Name == "opensnack.api.request.duration" {
				for _, dp := range m.Data.(metricdata.Histogram[float64]).DataPoints {
					if v, _ := dp.Attributes.Value("rpc.method"); v.AsString() == "CreateQueue" {
						count += dp.Count
					}
				}
			}
		}
	}
	if count != 2 {
		t.Errorf("CreateQueue requests counted: %d, want 2", count)
	}
}
