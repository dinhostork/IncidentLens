# 0008. Recorded runs

- Status: Accepted
- Date: 2026-11-28

## Context

A scenario run takes 15 minutes and the whole stack. Phase 2 needs the same runs many times while the engine changes, and its bench has to run in CI, where there is no stack. A run's telemetry has to be saved to files and loaded back into ClickHouse, where the engine reads it with the same SQL it uses on live data ([ADR 0001](0001-clickhouse-as-telemetry-store.md)).

What has to be decided:

- The file format, and how it is compressed.
- Which rows belong to a run. The engine compares the fault against a period before it, so a recording needs more than the window. That period must not hold another run's fault.
- How a recording made against one version of the exporter's tables is kept from being loaded where the engine expects another. The exporter has no schema version of its own: its tables change when the Collector does.
- Where recordings live so CI can get them.
- How this relates to the Phase 4 snapshots.

## Decision

**Format.** A recording is a directory: one file per table in ClickHouse's Native format, compressed with zstd, and `recording.json` describing them (`incidentlens.recording/v0`, fields in [`bench/README.md`](../../bench/README.md#what-a-recording-holds)). The engine talks to ClickHouse over HTTP, as the changes API does. ClickHouse compresses the export itself when asked with `Accept-Encoding: zstd` and decompresses the insert from `Content-Encoding: zstd`, so the engine needs no client binary and no compression library. Each table is exported sorted (spans by trace ID, metrics by service, metric and attributes), because rows that look alike next to each other compress better: the spans of one run take 4.8 MB sorted by trace and 6.5 MB in the order ClickHouse reads them.

Alternatives considered:

- **Parquet.** Readable outside ClickHouse, which is its main advantage. On the 136,884 spans of a `payments-bad-deploy` run, in the same order and with zstd, it came to 5.6 MB against 4.8 MB for Native, and every column goes through an Arrow type on the way out and back. A test round trip of `otel_traces` came back with the same rows and checksum, but each new column type would be one more mapping to check. Nothing outside ClickHouse needs to read a recording; `clickhouse local` reads Native files if someone wants to look at one without a server.
- **CSV or JSON.** Several times larger, and the nanosecond timestamps and maps need care to survive the trip.
- **`BACKUP TABLE`.** Copies whole parts, not the rows of one window and one environment, and needs a backup disk configured on the server.

**Rows.** From `margin` (default 10 minutes) before the start of the window to its end, as the run manifest gives them ([ADR 0006](0006-scenario-runs.md)), and only rows of the run's environment: `deployment.environment.name` in the resource for spans, logs and metrics, the `environment` column for change events. `otel_traces_trace_id_ts` is filled on the stack by a materialized view; it is recorded for the trace IDs of the recorded spans. `changes` is read with `FINAL`. Timestamps are not shifted, so the manifest's window applies to the loaded data as it is.

- `save` refuses when another run under `runs/` has steps inside the range, and waits until 30 seconds after the end of the window, so spans and metrics still in the SDKs' and the Collector's buffers are included.
- It records each table's row count and a checksum: the sum of `cityHash64` over every exported column, which does not depend on the order rows are stored in. The count comes from the export itself and is checked against a count taken right after it.

**Tables the engine expects.** [`engine/src/incidentlens/recording/tables.json`](../../engine/src/incidentlens/recording/tables.json) lists every column, with its type, of the recorded tables: the exporter's tables of the pinned Collector release and our `changes` table. This is the schema version. `save` refuses when the tables in ClickHouse differ from it, and `load` refuses a recording whose tables differ from it. A Collector upgrade that changes the exporter's tables lands as a change to that file, together with the engine changes it needs. Recordings made before it stop loading and are recorded again. `recording.json` also keeps the `CREATE` statement of every table, the ClickHouse and Collector versions it was saved from, and a SHA-256 of the columns.

**Loading.** Each recording goes into a database of its own, `rec_<run id>` by default, with every table in it, `changes` included. The tables are created from the saved `CREATE` statements so the sorting keys, codecs and indexes the engine's queries run against are the same as on the stack. Their TTL is removed: the exporter's tables drop whole parts older than 3 days, which a recording soon is. The materialized view is not created, since nothing writes to a loaded recording. After the insert each table must have the recorded row count and checksum, or the database is dropped. An existing database is only replaced when asked.

**Where recordings live.** As assets of a GitHub release, one tar file per recording (a full run at the default load, 25 minutes with the margin, is 6 MB, of which 4.8 MB are spans). `bench/recordings.sha256` lists the file names and their SHA-256, and the Makefile has the release URL. `make fetch-recordings` downloads and checks them, and CI loads every one of them into a ClickHouse service container on each change to the engine.

- In the repository, each re-recording would stay in the history for good, about 30 MB per set of five.
- Git LFS's free quota is 1 GB of downloads a month, which CI would use up.
- Regenerating on demand needs the stack and 15 minutes per scenario, which is what recordings are meant to avoid.

**Snapshots are a different thing.** A recording is the engine's input: everything in the range, in a format only ClickHouse reads. A Phase 4 snapshot is the engine's output: an evidence bundle plus only the telemetry it references, read by the UI with no ClickHouse at all. A snapshot can be made from a loaded recording. They are kept apart because their readers have nothing in common, and the snapshot format is left to Phase 4.

## Consequences

- One code path: the engine reads a loaded recording with the SQL it uses on the stack, given a database name for telemetry and one for change events (the same name, for a recording).
- Saving the same rows twice usually gives the same bytes, but not always: `otel_traces_trace_id_ts` came out different on a second save, with the same rows. `sha256` identifies a file; `checksum` identifies the data.
- Native ties recordings to ClickHouse. The saved `CREATE` statements use what the stack's ClickHouse supports (`otel_logs` has text indexes), so an older server may not load them. CI loads into the stack's version.
- The margin before the window is the stack without the run's baseline flags. For scenarios with baseline flags (`catalog-cache-disabled`, `orders-pool-exhaustion`) the warmup and the margin are two different normals, and `baseline_applied` in the run manifest marks where one becomes the other.
- Back-to-back runs need at least the margin between them, or a shorter margin when saved.
- Publishing a new set is a manual step: run, save, tar, upload, update `bench/recordings.sha256`. The steps are in [`bench/README.md`](../../bench/README.md#published-recordings).
