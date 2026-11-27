# Change events

Deploys, config changes, feature flag flips and notes, recorded so the engine can line them up against the telemetry. The HTTP API that accepts them is not written yet; this directory has the event format and its tests.

The format is defined by [`schema/change.schema.json`](schema/change.schema.json). The reasoning behind it is in [ADR 0005](../docs/adr/0005-change-events.md).

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

## Storage

Events go to `incidentlens.changes` in ClickHouse. The DDL is in [`deploy/clickhouse/init/`](../deploy/clickhouse/init/) and is applied by `make up` (see [`deploy/README.md`](../deploy/README.md#tables)). Read it with `FINAL`, since a duplicate insert is only merged away in the background:

```sql
SELECT * FROM incidentlens.changes FINAL
WHERE environment = 'demo' AND time >= '2026-12-01 14:00:00' AND time < '2026-12-01 15:00:00'
ORDER BY time
```

## Tests

```sh
cd changes && go test ./...
```

`schema/schema_test.go` checks every file in `schema/testdata/valid/` and a list of broken events, each of which has to fail for the expected reason.
