# 0005. Change event format and the `changes` table

- Status: Accepted
- Date: 2026-11-27

## Context

Change events are one of the three things the engine ranks candidates on, and evidence bundles reference them by ID. Several components will depend on their shape: the changes API writes them, the scenario runner posts them, the engine reads them by time window, and snapshots copy them. Both the format and the table have to be settled before the first of these is built.

A few properties matter more than the rest:

- A deploy event has to match the deploy seen in telemetry, which only works if `version.to` is the exact `service.version` string ([telemetry conventions](../telemetry-conventions.md#deploys)).
- Deploy tools retry. A retried post must not produce a second event, or the timeline shows two deploys.
- The time a change took effect and the time it was reported can differ by minutes when a pipeline posts at the end of a job, or by days when someone backfills a note.
- The engine reads the table whenever it reconstructs a window, whether or not the API is running.

## Decision

**Format.** [`changes/schema/change.schema.json`](../../changes/schema/change.schema.json) (JSON Schema 2020-12) defines the body a client posts. [`changes/README.md`](../../changes/README.md) describes it for people. In short:

- Required: `time`, `kind`, `environment`, `source`. Optional: `id`, `service`, `description`, `attributes`. Nothing else is accepted.
- `kind` is one of `deploy`, `config`, `flag`, `note`.
- `deploy` requires `service`, `version.from` and `version.to`. `version.from` may be empty for the first deploy of a service. `flag` requires `flag.key`, `flag.value.from` and `flag.value.to`. `note` requires `description`. `config` has no extra requirements.
- `service` is left out when a change is not tied to one service, such as a flag read by several services. The engine treats such an event as applying to the whole environment.
- Attribute values are strings. Flag values of other types are sent in their string form.

**IDs.** A client may send its own `id` (1 to 128 characters from `A-Z a-z 0-9 . _ : -`, starting with a letter or digit). Without one, the server assigns `chg_` followed by a [ULID](https://github.com/ulid/spec). IDs are unique across the whole store, not per environment. A post whose `id` is already stored is a retry if the content is the same, and the API answers with the stored event; with different content it is rejected. A client that wants safe retries sets the ID.

**Times.** `time` is when the change took effect, sent as RFC 3339 with any offset and stored in UTC with millisecond precision. `received_at` is set by the server when the event is stored and cannot be sent by clients. The engine uses `time`; `received_at` is kept to spot late or backfilled events.

**Storage.** One table, `incidentlens.changes`, in a database of its own rather than next to the exporter's tables in `otel`. Pointing IncidentLens at an existing exporter database then needs no write access to it. Queries join across the two databases without trouble.

| Column | Type |
|---|---|
| `id` | `String` |
| `time`, `received_at` | `DateTime64(3, 'UTC')` |
| `kind` | `Enum8('deploy', 'config', 'flag', 'note')` |
| `environment`, `service`, `source` | `LowCardinality(String)`, `service` empty when absent |
| `description` | `String`, empty when absent |
| `attributes` | `Map(LowCardinality(String), String)` |

- Engine `ReplacingMergeTree`, `ORDER BY (environment, time, id)`, partitioned by month. Lookups are always by environment and time window, with service as an optional filter on top. The table is small enough that a service filter does not need its own place in the key.
- A bloom filter index on `id` for lookups by ID.
- TTL of 90 days on `time`, against 72 hours for telemetry. A change that matters to a window can be days older than the window, and the rows cost almost nothing.

**Creation.** The DDL is in [`deploy/clickhouse/init/`](../../deploy/clickhouse/init/), one idempotent SQL file per step, applied in name order by a one-shot `clickhouse-init` service on every `make up`. The API does not create tables. It would need DDL rights it otherwise does not, and the engine would find no table on a stack where the API has not started yet. The ClickHouse image's own init directory was not used because it only runs on an empty data volume.

## Consequences

- Retries are only safe for clients that set an ID. Server-assigned IDs are new on every post.
- Duplicate inserts that get past the API (two retries arriving at the same moment) share `(environment, time, id)` and are merged away by ClickHouse, but only in the background. Readers use `FINAL` or `LIMIT 1 BY id`.
- `CREATE TABLE IF NOT EXISTS` never updates a table that exists. A column change goes in a new numbered SQL file with an `ALTER`, and a merged file is not edited afterwards, the same rule as for ADRs.
- Adding a kind means changing the schema, the `Enum8` (an `ALTER` that only touches metadata) and a new ADR. The `Enum8` makes ClickHouse reject unknown kinds even from a writer that skipped validation.
- Flag values as strings lose their type. The engine compares them as strings, which is enough to tell that a flag changed.
- Anyone running against their own ClickHouse applies the files in `deploy/clickhouse/init/` by hand.
- In the compose stack something has to depend on `clickhouse-init` or `docker compose up --wait` fails when it exits. For now that is the Collector, which does not use the table; the changes API will depend on it for real.
