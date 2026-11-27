# Telemetry conventions

What IncidentLens expects from the telemetry it reads. The demo services follow this, and so should any service you point at IncidentLens.

Most of it is plain [OpenTelemetry semantic conventions](https://opentelemetry.io/docs/specs/semconv/). This page does not repeat them. It says which parts the engine depends on, and what goes wrong in a reconstruction when they are missing. It was written against semantic conventions 1.45.

Column names below refer to the tables the Collector's ClickHouse exporter creates (see [ADR 0001](adr/0001-clickhouse-as-telemetry-store.md) and [`deploy/README.md`](../deploy/README.md#tables)).

## Resource attributes

Set on the SDK resource, so they apply to every span, log record and metric a process emits.

### Required

| Attribute | Used for |
|---|---|
| `service.name` | Node identity in the service graph. Also the value of `service` in change events and scenario files, so it has to match exactly. |
| `service.version` | Telling deploys apart. See [Deploys](#deploys). |
| `deployment.environment.name` | Keeping environments that share a backend out of each other's reconstructions. |

`service.name` is a logical name (`payments`), not a host, pod or container name. All replicas of a service share it. If the SDK falls back to `unknown_service:<process>`, that service will show up in the graph under that name and nothing else will match it.

`service.version` can be any string, as long as it changes when the code or config being deployed changes and only then. Semver, a git SHA or a build number all work. A value that changes on every restart, like a start timestamp, turns each restart into a deploy. A constant like `latest` hides every deploy.

`deployment.environment.name` replaced `deployment.environment` in semantic conventions 1.27. If only the old attribute is present, the engine reads it as a fallback.

References: [service](https://opentelemetry.io/docs/specs/semconv/registry/attributes/service/), [deployment environment](https://opentelemetry.io/docs/specs/semconv/resource/deployment-environment/).

### Recommended

| Attribute | Used for |
|---|---|
| `service.namespace` | Grouping and filtering in the UI. |
| `host.name` | Telling whether a problem is on one instance or all of them. |
| `service.instance.id` | Same as `host.name` when several instances share a host. Also helps tracking a rolling deploy. |

The engine identifies a service by `service.name` within an environment. `service.namespace` is not part of that identity yet, so two services with the same name in different namespaces will be merged. Keep names unique per environment.

Reference: [host](https://opentelemetry.io/docs/specs/semconv/resource/host/).

### Setting them

The SDKs read the standard [environment variables](https://opentelemetry.io/docs/specs/otel/configuration/sdk-environment-variables/):

```sh
OTEL_SERVICE_NAME=payments
OTEL_RESOURCE_ATTRIBUTES=service.version=1.4.1,deployment.environment.name=demo,service.namespace=shop
```

In the demo, every service sets `deployment.environment.name=demo` and `service.namespace=shop`, and takes `service.version` from `SERVICE_VERSION` so a deploy can be simulated by restarting with a different value.

## Spans and the service graph

The graph is built from spans, not from configuration. An edge from service A to service B exists when a span from A has a child span from B, joined on `ParentSpanId = SpanId` within the same `TraceId`.

[Span kind](https://opentelemetry.io/docs/specs/otel/trace/api/#spankind) is what makes that edge measurable:

- The caller records a `CLIENT` span around the outbound request.
- The callee records a `SERVER` span for handling it, with the client span as parent.

Each edge then has two views of the same request. The client span measures what the caller experienced, including network time, connection pool waits and retries. The server span measures what the callee did. The gap between them is often where the answer is: a slow client span over a fast server span points at the network, the caller's pool or a queue in front of the callee, not at the callee's own code.

What breaks when part of the pair is missing:

- **No client span.** The edge can still be inferred from the parent span's service, but the caller's view is lost. Pool exhaustion in the caller looks like latency in the callee.
- **No server span** (uninstrumented callee, or a database). The client span is the only view. That is expected for Postgres, Redis and third-party APIs: they appear as leaf nodes named from the client span's attributes (below).
- **No context propagation.** Every service starts its own trace, nothing has a parent in another service, and the graph has no edges. Services must propagate [W3C Trace Context](https://www.w3.org/TR/trace-context/) (`traceparent`), which is the SDK default.

`INTERNAL` spans are not used for edges. They are kept and can show up in exemplar traces. An outbound call wrapped only in an `INTERNAL` span produces no edge, so HTTP and database clients need their own instrumentation.

`PRODUCER` and `CONSUMER` spans are treated like `CLIENT` and `SERVER`. The demo does not use messaging.

In ClickHouse, `SpanKind` holds `Server`, `Client`, `Internal`, `Producer` or `Consumer`.

### Naming dependencies without their own telemetry

A client span to something that does not report its own spans becomes a leaf node. Its name comes from, in order:

1. `server.service.name` (or the older `peer.service`), if the instrumentation or your code sets it
2. `db.system.name` plus `server.address`, for database calls (`postgresql@postgres`, `redis@redis`)
3. `server.address`

Database client spans therefore need `db.system.name` and `server.address`. Without them every database the service talks to collapses into one unnamed node, and a slow Postgres cannot be told apart from a slow Redis.

References: [HTTP spans](https://opentelemetry.io/docs/specs/semconv/http/http-spans/), [database spans](https://opentelemetry.io/docs/specs/semconv/database/database-spans/), [PostgreSQL](https://opentelemetry.io/docs/specs/semconv/database/postgresql/), [Redis](https://opentelemetry.io/docs/specs/semconv/database/redis/).

### Sampling

Rates and error ratios are computed from spans, so sampling changes them.

- No sampling (`always_on`) is what the demo uses and what the benchmark assumes.
- Parent-based trace ID ratio sampling keeps rates proportional and error ratios roughly unbiased. Rates are low by the sampling factor.
- Tail sampling that keeps errors and drops a share of successes inflates error ratios. Onsets can still be found, but the magnitudes in the bundle are wrong.

See the [trace SDK sampling section](https://opentelemetry.io/docs/specs/otel/trace/sdk/#sampling).

## Errors

A span counts as an error when its status is `Error` (`StatusCode = 'Error'` in ClickHouse). Nothing else makes a span an error.

That means the status has to be set correctly where the error happens. The semantic conventions say how, per protocol ([recording errors](https://opentelemetry.io/docs/specs/semconv/general/recording-errors/)). For HTTP, which is what the demo uses:

| Response | `SERVER` span | `CLIENT` span |
|---|---|---|
| 2xx, 3xx | Unset | Unset |
| 4xx | Unset | Error |
| 5xx | Error | Error |
| No response (timeout, connection refused) | n/a | Error |

The asymmetry for 4xx matters: a 404 is the client's mistake from the server's point of view, so it does not raise the server's error rate.

Other signals are kept for context but do not decide whether a span failed:

- `http.response.status_code` and `error.type` are shown in the bundle and used to describe the failure ("timeouts from payments to Postgres").
- Exceptions are evidence, not error markers. A handled exception, such as one caught before a successful retry, is not counted as an error. Exceptions used to be recorded as `exception` events on spans (`Events.Name` in ClickHouse). That form is [deprecated](https://opentelemetry.io/docs/specs/semconv/exceptions/exceptions-spans/) in favor of [log records](https://opentelemetry.io/docs/specs/semconv/exceptions/exceptions-logs/). The engine reads both, since SDKs will emit either for a while.

A retried call shows up as several client spans under one parent. Each failed attempt is an error on the edge, even if the parent request succeeds. That is intended: the edge was unhealthy even when the caller hid it from its own callers.

## Logs

Log records are used for error templates and attached to the services and spans they came from.

- **Trace and span IDs.** A log record emitted while a span is active should carry its `TraceId` and `SpanId`. The OpenTelemetry log bridges do this when they get the active context. In Go that means the `...Context` variants: `slog.ErrorContext(ctx, ...)`, not `slog.Error(...)`. Python's `logging` bridge picks the context up implicitly. See [trace context in non-OTLP logs](https://opentelemetry.io/docs/specs/otel/compatibility/logging_trace_context/) if logs reach the Collector some other way.
- **Severity.** Records are selected by `SeverityNumber`, not `SeverityText`: 13 to 16 is warning, 17 and above is error ([data model](https://opentelemetry.io/docs/specs/otel/logs/data-model/#field-severitynumber)). A bridge that maps everything to `INFO`, or leaves the number at 0, hides those logs from the engine.
- **Same resource.** Logs must have the same `service.name`, `service.version` and `deployment.environment.name` as the spans. Exporting logs through the same SDK resource does this.

Logs without a trace ID are still used. They are grouped into templates and attributed by `service.name`, but they cannot be tied to a request. Background work such as pool maintenance or cache refreshes falls in this group, and is often where the useful message is, so it is worth logging.

Keep variable parts out of the message where the logging API allows it, and put them in attributes instead. Template mining copes with numbers and IDs in the body, but `timeout after 812 ms` with `db.pool=orders` as an attribute groups better than one long formatted string.

## Deploys

A deploy can reach IncidentLens in two ways.

**From telemetry.** A new `service.version` value appears for a service. The time of the deploy is the first span carrying the new value. During a rolling deploy both versions are present for a while, and `service.instance.id` shows how far the rollout got. Nothing extra has to be sent, but nothing else is known either: no author, no source, and no deploy at all if the version string did not change.

**From a change event.** The deploy tool posts a `deploy` event to the changes API with `version.from` and `version.to` (format in [`changes/README.md`](../changes/README.md)). This works even when no traffic reaches the new version, and it records where the change came from.

Both is best. When a change event and an observed version change refer to the same service and the same `version.to`, they are the same deploy and appear once in the bundle, with both as evidence. A version change with no matching event still appears, marked as inferred from telemetry. An event with no matching version change also appears. That usually means the new version never received traffic, which can be part of the story.

For this to work, the version in the change event has to be the same string as `service.version`. Config changes and flag flips do not change `service.version`, so they are only visible as change events.
