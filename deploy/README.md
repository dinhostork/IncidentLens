# Local stack

Docker Compose stack for development: an OpenTelemetry Collector that writes to ClickHouse, flagd for the fault flags, the [demo system](../demo/README.md) with its Postgres and Redis, and a k6 [load generator](../demo/loadgen/README.md) that keeps traffic on it.

```sh
make up       # build the demo images, start, wait until every service is healthy
make smoke    # send test spans, run a demo checkout, check both land in otel.otel_traces
make down     # stop, keep data
make reset    # stop and delete the ClickHouse and Postgres volumes
make fault SET="payments.latency_ms.postgres=800"   # turn a demo fault on, see demo/README.md
make fault-clear                                     # all faults off
```

`make up DEBUG=1` adds the Collector's `debug` exporter to every pipeline, so received telemetry is also printed to `make logs SERVICE=otel-collector`.

## Layout

| Path | What |
|---|---|
| `compose/compose.yaml` | The stack |
| `compose/compose.debug.yaml` | Override that loads `otel/debug.yaml` on top of the main Collector config |
| `otel/collector.yaml` | Collector config: OTLP in, ClickHouse out |
| `otel/debug.yaml` | Adds the `debug` exporter |
| `clickhouse/config.d/low-memory.xml` | Smaller caches, noisy system logs turned off |
| `flags/flags.json` | Fault flag definitions for the demo services, all off |
| `flags/active.json` | The flags flagd serves: a copy of `flags.json` with the faults currently on. Not tracked, created by `make up`, changed with `make fault` (see [`demo/README.md`](../demo/README.md#faults)). |
| `postgres/initdb/` | Creates one database and role per demo service on first start |

The demo service images are built from `demo/` (see [`demo/README.md`](../demo/README.md#layout)).

## Services and ports

All ports are bound to `127.0.0.1`.

| Service | Image | Ports | Health check |
|---|---|---|---|
| `otel-collector` | `otel/opentelemetry-collector-contrib:0.165.0` | 4317 OTLP gRPC, 4318 OTLP HTTP | `health_check` extension on 13133 |
| `clickhouse` | `clickhouse/clickhouse-server:26.8.48.11` (LTS) | 8123 HTTP, 9000 native | `/ping` |
| `flagd` | `ghcr.io/open-feature/flagd:v0.18.2` | 8013 evaluation (gRPC), 8016 OFREP (HTTP). The demo services use the sync service on 8015 inside the compose network. | `/readyz` on 8014 |
| `postgres` | `postgres:18.7-alpine` | none | `pg_isready` |
| `redis` | `redis:8.12.0-alpine` | none | `redis-cli ping` |
| `gateway` | built from `demo/services/gateway` | 8080 (`GATEWAY_PORT`) | `/healthz` |
| `catalog`, `orders`, `payments` | built from `demo/services/<name>` | none | `/healthz` |
| `loadgen` | `grafana/k6:2.4.0`, runs `demo/loadgen/script.js` | none | none. Starts after the gateway is healthy, `LOADGEN_RPS` sets the rate (default 20, `0` for none). |

The Collector, flagd and Go service images have no shell or HTTP client. A one-shot `probe` service copies a static busybox into a shared volume, and their health checks run `busybox wget` from there. This keeps the upstream images unmodified.

Redis runs without persistence. Postgres keeps its data in the `postgres-data` volume, credentials are in `compose.yaml` and only matter inside the compose network.

Versions are pinned. The ClickHouse exporter's table layout can change between Collector releases (see [ADR 0001](../docs/adr/0001-clickhouse-as-telemetry-store.md)), so check the exporter changelog before bumping it.

## ClickHouse

Credentials default to `incidentlens` / `incidentlens` and can be overridden with `CLICKHOUSE_USER` and `CLICKHOUSE_PASSWORD`. Data lives in the `clickhouse-data` named volume.

```sh
docker compose -f deploy/compose/compose.yaml exec clickhouse clickhouse-client
```

Memory: the container is limited to 1.5 GB and ClickHouse caps itself at 75% of that. With the load generator at its default rate, the whole stack uses about 650 MB.

### Tables

The `otel` database is created by the ClickHouse image (`CLICKHOUSE_DB`). The Collector's exporter creates the tables on startup (`create_schema: true`) with its default schema:

| Table | Contents | TTL |
|---|---|---|
| `otel_traces` | Spans | 3 days on `Timestamp` |
| `otel_traces_trace_id_ts` | Trace ID to time range, filled by `otel_traces_trace_id_ts_mv` | 3 days |
| `otel_logs` | Log records | 3 days on `Timestamp` |
| `otel_metrics_gauge` | Gauge points | 3 days on `TimeUnix` |
| `otel_metrics_sum` | Sum points | 3 days on `TimeUnix` |
| `otel_metrics_histogram` | Histogram points | 3 days on `TimeUnix` |
| `otel_metrics_exponential_histogram` | Exponential histogram points | 3 days on `TimeUnix` |
| `otel_metrics_summary` | Summary points | 3 days on `TimeUnix` |

The TTL comes from `ttl: 72h` in `otel/collector.yaml` and is applied only when a table is created. Changing it later needs an `ALTER TABLE ... MODIFY TTL`, or `make reset`.

## Troubleshooting

- `make up` fails on a health check: `make logs SERVICE=<name>`. ClickHouse writes its own logs to `/var/log/clickhouse-server/` inside the container.
- Nothing shows up in ClickHouse: run with `DEBUG=1` to see whether the Collector receives anything at all.
- Port already in use: another ClickHouse or Collector is probably running on the host. For the gateway, set `GATEWAY_PORT`.
