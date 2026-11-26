# 0001. ClickHouse as the telemetry store

- Status: Accepted
- Date: 2026-11-26

## Context

The engine needs to scan traces, logs and metrics for a time window, group them by service and time bucket, and join spans to their parents. That is a columnar, aggregation-heavy workload. The project also needs to be cheap to run locally and easy to point at an existing setup.

Options considered:

- **Postgres (+ Timescale).** Familiar and fine for metrics, but span volumes and wide log rows get slow to aggregate without a lot of tuning.
- **Tempo + Loki + Prometheus.** What many teams already run, but it means three query languages and joins across systems in application code.
- **ClickHouse.** Columnar, fast group-by over large windows, SQL. The Collector contrib distribution has a ClickHouse exporter with a known table layout.

## Decision

Use ClickHouse. Telemetry is written by the stock Collector ClickHouse exporter using its default schema. IncidentLens adds its own tables (`changes`, and later anything the engine caches) next to them and does not modify the exporter's tables.

## Consequences

- No custom ingestion code. Work goes into analysis instead.
- Anyone already using the ClickHouse exporter can run the engine against their data.
- The engine is tied to the exporter's schema. Schema changes upstream need to be tracked; pin the Collector version.
- Supporting other backends later means an adapter layer in the engine. Not planned for now.
