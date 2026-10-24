// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

// Package telemetry is OpenSnack's optional OpenTelemetry: traces and
// metrics over OTLP/HTTP. It's off unless OTEL_EXPORTER_OTLP_ENDPOINT is
// set, and otherwise configured by the standard OTEL_* variables
// (docs/telemetry.md).
package telemetry

import (
	"context"
	"errors"
	"os"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/exporters/otlp/otlpmetric/otlpmetrichttp"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp"
	"go.opentelemetry.io/otel/propagation"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.37.0"
)

// Enabled reports whether an OTLP endpoint is configured.
func Enabled() bool {
	return os.Getenv("OTEL_EXPORTER_OTLP_ENDPOINT") != "" || os.Getenv("OTEL_EXPORTER_OTLP_TRACES_ENDPOINT") != ""
}

// Setup installs the global providers and the W3C trace-context propagator.
// The sampler comes from OTEL_TRACES_SAMPLER (default parentbased_always_on);
// parentbased_always_off traces only requests that arrive with a sampled
// traceparent, such as those from your own instrumented services.
func Setup(ctx context.Context, version string) (shutdown func(context.Context) error, err error) {
	otel.SetTextMapPropagator(propagation.TraceContext{})
	if !Enabled() {
		return func(context.Context) error { return nil }, nil
	}
	res, err := resource.Merge(resource.Default(), resource.NewSchemaless(
		semconv.ServiceName("opensnack"), semconv.ServiceVersion(version)))
	if err != nil {
		return nil, err
	}
	if env, err := resource.New(ctx, resource.WithFromEnv()); err == nil {
		res, _ = resource.Merge(res, env)
	}
	te, err := otlptracehttp.New(ctx)
	if err != nil {
		return nil, err
	}
	me, err := otlpmetrichttp.New(ctx)
	if err != nil {
		return nil, err
	}
	tp := sdktrace.NewTracerProvider(sdktrace.WithResource(res), sdktrace.WithBatcher(te))
	mp := sdkmetric.NewMeterProvider(sdkmetric.WithResource(res),
		sdkmetric.WithReader(sdkmetric.NewPeriodicReader(me, sdkmetric.WithInterval(30*time.Second))))
	otel.SetTracerProvider(tp)
	otel.SetMeterProvider(mp)
	return func(ctx context.Context) error { return errors.Join(tp.Shutdown(ctx), mp.Shutdown(ctx)) }, nil
}
