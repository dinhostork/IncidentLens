# Change events

Deploys, config changes, feature flag flips and notes, recorded so the engine can line them up against the telemetry. This directory has the event format and the HTTP service that stores events in ClickHouse and lists them by time window.

The format is defined by [`schema/change.schema.json`](schema/change.schema.json). The reasoning behind it is in [ADR 0005](../docs/adr/0005-change-events.md).

## Running it

`make up` starts it as the `changes` service on `127.0.0.1:8090` (`CHANGES_PORT` to change the host port). To run it outside the stack, against the stack's ClickHouse:

```sh
cd changes && go run .
```

| Variable | Default | |
|---|---|---|
| `CHANGES_ADDR` | `127.0.0.1:8090` | Listen address. The compose service sets `:8090`. |
| `CHANGES_TOKEN` | unset | Shared token. When set, every `/v1` request needs `Authorization: Bearer <token>`. When unset there is no auth. |
| `CLICKHOUSE_URL` | `http://127.0.0.1:8123` | ClickHouse HTTP interface. |
| `CLICKHOUSE_USER`, `CLICKHOUSE_PASSWORD` | `incidentlens`, `incidentlens` | Same defaults as the compose stack. |
| `CLICKHOUSE_DATABASE` | `incidentlens` | Database that holds the `changes` table. The service never creates it, see [Storage](#storage). |

The service sends no traces, metrics or logs to the Collector. In the demo its spans would show up as one more service in every reconstruction. It logs to stdout.

## API

### `POST /v1/changes`

Validates the body against the schema and stores it.

```sh
curl -X POST http://127.0.0.1:8090/v1/changes -H 'Content-Type: application/json' -d '{
    "id": "gha-8812345-1",
    "time": "2026-12-01T14:03:12Z",
    "kind": "deploy",
    "environment": "demo",
    "service": "payments",
    "attributes": {"version.from": "1.0.0", "version.to": "1.0.1"},
    "source": "github-actions"
  }'
```

```json
{"id":"gha-8812345-1","time":"2026-12-01T14:03:12Z","received_at":"2026-11-27T15:23:12.601Z","kind":"deploy","environment":"demo","service":"payments","attributes":{"version.from":"1.0.0","version.to":"1.0.1"},"source":"github-actions"}
```

The response is the event as stored: `time` in UTC to the millisecond, `received_at` added, and `id` filled in when the body had none (`chg_01M8ESMYC59W7CMGZ2VYS0RCJF`).

| Status | When |
|---|---|
| 201 | Stored. |
| 200 | Retry of an event already stored with the same content. The stored event is returned and nothing is written, see [Retries](#retries). |
| 400 | Not JSON, or does not match the schema. The body lists what is wrong. |
| 401 | `CHANGES_TOKEN` is set and the request does not carry it. |
| 409 | The `id` is already stored with different content. |
| 413 | Body larger than 1 MiB. |
| 503 | ClickHouse did not answer or returned an error. The details are in the service log. |

A schema failure says where:

```json
{"error":"event does not match the schema","problems":[{"location":"/attributes","message":"missing property 'version.to'"}]}
```

### `GET /v1/changes`

Events with `start <= time < end`, ordered by `time` and then `id`.

```sh
curl 'http://127.0.0.1:8090/v1/changes?start=2026-12-01T14:00:00Z&end=2026-12-01T15:00:00Z&environment=demo'
```

```json
{"changes":[{"id":"gha-8812345-1","time":"2026-12-01T14:03:12Z","received_at":"2026-11-27T15:23:12.601Z","kind":"deploy","environment":"demo","service":"payments","attributes":{"version.from":"1.0.0","version.to":"1.0.1"},"source":"github-actions"},{"id":"chg_01M8ESMYC59W7CMGZ2VYS0RCJF","time":"2026-12-01T14:05:00Z","received_at":"2026-11-27T15:23:12.645Z","kind":"flag","environment":"demo","attributes":{"flag.key":"payments.latency_ms.postgres","flag.value.from":"0","flag.value.to":"800"},"source":"alice"}]}
```

| Parameter | |
|---|---|
| `start`, `end` | Required, RFC 3339 with an offset. `end` must be after `start`. |
| `environment`, `service`, `kind` | Optional, exact match. `service=payments` does not return events without a service, such as the flag above; leave the filter out to get them. |

Unknown or repeated parameters are rejected with 400, so a typo does not silently return everything. A window with more than 10,000 events is rejected with 422; ask for a shorter one.

### `GET /healthz`

Answers `ok` while the process is up, without checking ClickHouse and without a token. Used by the compose health check.

## Format

```json
{
  "id": "gha-8812345-1",
  "time": "2026-12-01T14:03:12Z",
  "kind": "deploy",
  "environment": "demo",
  "service": "payments",
  "attributes": {
    "version.from": "1.0.0",
    "version.to": "1.0.1",
    "vcs.ref.head.revision": "4f2c9e1"
  },
  "source": "github-actions"
}
```

| Field | Required | |
|---|---|---|
| `id` | no | Up to 128 characters from `A-Z a-z 0-9 . _ : -`. The server assigns `chg_<ULID>` when it is missing. Set it if you want to retry a post safely. |
| `time` | yes | When the change took effect. RFC 3339 with an offset (`Z` or `-03:00`). Stored in UTC, to the millisecond. |
| `kind` | yes | `deploy`, `config`, `flag` or `note`. |
| `environment` | yes | The `deployment.environment.name` of the affected services. |
| `service` | deploy only | The `service.name` of the affected service. Leave it out for changes that are not tied to one service. |
| `description` | note only | Free text, up to 4096 characters. |
| `attributes` | depends on kind | String values only, up to 64 keys. |
| `source` | yes | What posted the event: `github-actions`, `scenario-runner`, a person's handle. |

The server also records `received_at`, the time the event was stored. Clients cannot set it.

Required attributes per kind:

| Kind | Attributes |
|---|---|
| `deploy` | `version.from` (empty for the first deploy of a service), `version.to`. `version.to` must be exactly the `service.version` the new version reports, see [telemetry conventions](../docs/telemetry-conventions.md#deploys). |
| `flag` | `flag.key`, `flag.value.from`, `flag.value.to`. Values are strings: send `"true"`, `"800"`, or a JSON object serialized to a string. |
| `config` | none |
| `note` | none, but `description` is required |

Other attributes are kept as they are. Where OpenTelemetry has a name for something, use it (`vcs.ref.head.revision` for a commit SHA).

More examples are in [`schema/testdata/valid/`](schema/testdata/valid/).

### Retries

An event whose `id` is already stored is a retry: if the content matches, the stored event is returned and nothing is written. If the content differs, the post is rejected. Without an `id` every post creates a new event.

Content is compared after normalizing: the same instant in another offset is the same `time`, differences below a millisecond are dropped, and `"attributes": {}` equals no attributes. `received_at` is not compared.

Concurrent posts with the same `id` are serialized inside one process. With several instances of the service two of them could still insert the same `id` at the same moment; identical rows are merged by ClickHouse, rows that differ would both stay. Run one instance.

## Storage

Events go to `incidentlens.changes` in ClickHouse, written through its HTTP interface. The DDL is in [`deploy/clickhouse/init/`](../deploy/clickhouse/init/) and is applied by `make up` (see [`deploy/README.md`](../deploy/README.md#tables)); the service does not create tables. Read the table with `FINAL`, since a duplicate insert is only merged away in the background:

```sql
SELECT * FROM incidentlens.changes FINAL
WHERE environment = 'demo' AND time >= '2026-12-01 14:00:00' AND time < '2026-12-01 15:00:00'
ORDER BY time
```

## Tests

```sh
cd changes && go test ./...   # also part of make test
make test-clickhouse          # needs make up
```

`schema/schema_test.go` checks every file in `schema/testdata/valid/` and a list of broken events, each of which has to fail for the expected reason. `api/api_test.go` runs the handlers over an in-memory store: validation, retries and conflicts, auth, window bounds and filters.

`make test-clickhouse` runs `clickhouse/store_clickhouse_test.go` (build tag `clickhouse`) against the stack's ClickHouse. It creates a throwaway `incidentlens_test_<n>` database from the files in `deploy/clickhouse/init/`, so it also checks that the DDL works, and drops it at the end. Set `CLICKHOUSE_URL`, `CLICKHOUSE_USER` and `CLICKHOUSE_PASSWORD` to use another server.

`make smoke` posts a note to the running service (environment `smoke`), retries it and reads it back.
