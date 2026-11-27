# Architecture decision records

Short records of decisions that would be costly to undo. One file per decision, numbered, never rewritten after acceptance. If a decision changes, add a new record that supersedes the old one.

| # | Decision | Status |
|---|---|---|
| [0001](0001-clickhouse-as-telemetry-store.md) | ClickHouse as the telemetry store, written by the stock Collector exporter | Accepted |
| [0002](0002-deterministic-core-optional-llm.md) | Deterministic reconstruction core, LLM only for narrative | Accepted |
| [0003](0003-benchmark-first.md) | Build the scenario benchmark before the engine | Accepted |
| [0004](0004-scenario-format.md) | Scenario format and ground truth | Accepted |
| [0005](0005-change-events.md) | Change event format and the `changes` table | Accepted |
| [0006](0006-scenario-runs.md) | Scenario runs and the run manifest | Accepted |

Template: [`template.md`](template.md)
