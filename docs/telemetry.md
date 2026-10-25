# Telemetry

OpenSnack can export OpenTelemetry traces and metrics. It's off unless
`OTEL_EXPORTER_OTLP_ENDPOINT` is set, and then it's configured by the
standard variables:

| Variable | Example | Meaning |
|---|---|---|
| `OTEL_EXPORTER_OTLP_ENDPOINT` | `http://collector:4318` | OTLP/HTTP endpoint for traces and metrics. |
| `OTEL_TRACES_SAMPLER` | `parentbased_always_off` | Which requests are traced; the default is `parentbased_always_on`. |
| `OTEL_SERVICE_NAME` | `opensnack` | The default. |
| `OTEL_RESOURCE_ATTRIBUTES` | `deployment.environment=prod` | Extra resource attributes. |

## Traces

Each AWS API request gets a server span named `<service> <action>`, such as
`sqs CreateQueue` or `s3 PUT` for REST calls. It has:

- `rpc.system` (`aws-api`), `rpc.service` and `rpc.method`;
- the usual HTTP attributes (method, status, path);
- `opensnack.namespace_hash`: the first 12 hex digits of the SHA-256 of the
  tenant's namespace. It's enough to group a tenant's requests, and it never
  names the tenant.

Credentials never appear in spans. The legacy `custom-<namespace>` token in
the User-Agent is replaced with `custom-[redacted]`.

A request that arrives with a W3C `traceparent` continues that trace, so a
service that calls OpenSnack sees OpenSnack's work inside its own traces.
On a shared deployment, where tenants' tools make thousands of calls, use
`OTEL_TRACES_SAMPLER=parentbased_always_off`. Then only requests from your
own instrumented services, which send a sampled `traceparent`, are traced.
Tenants can send a `traceparent` too, so cap trace volume in your collector
as well.

Health checks (`/_opensnack/health`) aren't traced.

## Metrics

`opensnack.api.request.duration` (seconds, a histogram) covers every AWS API
request, traced or not, with `rpc.service`, `rpc.method` and
`http.response.status_code`. There's no tenant attribute, so the number of
series stays bounded by services and actions.
