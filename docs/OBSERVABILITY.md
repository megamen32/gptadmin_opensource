# Observability

GPTAdmin keeps operational evidence bounded and secret-safe. The request path
uses the Hub as the correlation authority; it does not export MCP arguments,
command text, file contents, bearer values or full target URLs.

## Correlation contract

- `X-Request-ID` is accepted for a bounded request correlation identifier and
  is returned on the response.
- A valid W3C `traceparent` is accepted and propagated as a child span. An
  invalid value is replaced rather than reflected.
- Hub audit records retain the correlation fields across policy decisions,
  `mcp_enqueue`, queue polling and `mcp_result`/`shell_result` events.
- ShellMCP receives `trace_id` and `traceparent` with queued work and writes
  the same fields to its bounded poll and execution audit events.
- ProxyRelay exposes bounded session/pair/reset/queue counters. Its relay
  tickets and frame payloads are never telemetry attributes.

The correlation tests are the local evidence for this contract:

```bash
cd go-hub && go test ./internal/hub -run 'TestRequestTraceID|TestTraceParent|TestOTLPExporter' -count=1
cd go-shellmcp && go test ./internal/server -run TestQueueExecutesGenericMCPToolAndPostsResult -count=1
cd go-proxyrelay && go test ./blackbox -run TestProxyRelayProcessMetricsAndStreamRoundTrip -count=1
```

## Bounded metrics

| Component | Surface | Authentication | Intended contents |
| --- | --- | --- | --- |
| Hub | `GET /metrics` | public liveness/aggregate surface | bounded request, agent, queue and policy counters |
| ShellMCP | `GET /metrics` | ShellMCP credential | bounded runtime, queue, heartbeat and storage state |
| ProxyRelay | `GET /metrics` | deployment boundary | bounded authenticated-peer, session, pair, reset and queue counters |

Deployment should keep metrics on the private operator path or behind the
same access boundary as the service. A `200` response from a local test is not
evidence that a public endpoint is safe to expose.

## Optional OTLP logs

Set `GPTADMIN_OTLP_ENDPOINT` on the Hub to enable the bounded OTLP/HTTP log
exporter. A loopback `http://` collector is allowed for development; an
external collector must use `https://`. The endpoint may not contain
credentials, query parameters or fragments. The default path is `/v1/logs`.

Exported records contain only allowlisted fields such as event name, status,
job/profile/server identifiers, policy decision, retry outcome and trace
correlation. URL-like target values and sensitive payload fields are dropped.
The exporter is asynchronous and bounded; queue overflow is observable as a
drop in the Hub log rather than an unbounded memory allocation.

The repository proves exporter encoding and redaction with a loopback test. It
does not claim a retained production trace, collector availability, retention
policy or cross-host relay span until an authorized deployment artifact records
those facts.

## Operator evidence

For a release or incident, retain the exact build/commit, sanitized health and
metrics responses, the focused trace test result and the collector/run artifact
path in `docs/WORKLOG.md`. Never paste raw telemetry payloads, credentials or
customer data into the worklog. See [SLO and alert runbook](./SLO_ALERTS.md)
for owners, symptoms, diagnosis and recovery actions.
