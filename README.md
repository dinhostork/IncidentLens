# IncidentLens
[![CI](https://github.com/dinhostork/IncidentLens/actions/workflows/ci.yml/badge.svg)](https://github.com/dinhostork/IncidentLens/actions/workflows/ci.yml)

Reconstruct production incidents from telemetry and change events.

Pick a time window and IncidentLens builds a timeline of what happened, shows how the failure spread across services and ranks the most likely causes. Every claim in the output points back to the span, log line or change event it came from, so anyone can check it.

> **Status:** early development. The lab runs, the analysis does not exist yet.
>
> `make up` starts the local stack (Collector, ClickHouse, flagd, changes API), the [demo shop](demo/README.md) and a load generator. Faults are switched on with `make fault`, and the five [benchmark scenarios](bench/README.md) each produce a visible change in latency or errors on their target service. `make scenario ID=<id>` runs one, including the bad deploy, and records its window and expected answer. `make record` saves a run's telemetry to files that load back into ClickHouse without the rest of the stack. The [change events API](changes/README.md) records deploys, config changes and flag flips. The engine has no analysis yet and the UI is an empty skeleton. See the [roadmap](docs/ROADMAP.md) for what comes next.

## Why

During an incident there are dashboards and alerts. After it there is a postmortem, usually written days later from memory, chat history and a few screenshots. The data needed to rebuild what actually happened is often still sitting in the telemetry backend, but nobody has time to dig through it.

There are good open-source agents for live troubleshooting already. IncidentLens does a narrower job: after the fact, rebuild the incident in a form someone else can verify, and measure how often that reconstruction is right.

## How it works (planned)

1. Services send OTLP to an OpenTelemetry Collector, which writes traces, logs and metrics to ClickHouse.
2. Deploys, config changes and feature flag flips are posted to a small change events API.
3. For a given window, the engine:
   - builds the service graph from parent/child spans
   - computes request rate, error rate and latency per service and per edge
   - detects when each service started misbehaving
   - ranks root cause candidates by onset order, position in the graph and nearby changes
   - groups error logs into templates
4. The output is an **evidence bundle**: a timeline, a propagation graph and a ranked candidate list, each item referencing raw data by ID.
5. Optionally, an LLM writes a postmortem draft from the bundle. Every sentence must cite evidence IDs. Sentences that cite nothing, or cite IDs that are not in the bundle, are dropped.

The engine works without an LLM. The narrative is a convenience, not the analysis.

## Measuring it

The repo includes a small demo system and a set of fault scenarios with known root causes. `incidentlens bench` runs them and reports top-1 and top-3 accuracy. Numbers go here once there are numbers.

## Repository layout (planned)

| Path | What | Language |
|---|---|---|
| `engine/` | Reconstruction engine and `incidentlens` CLI | Python |
| `changes/` | Change events API and event format ([README](changes/README.md)) | Go |
| `web/` | Replay UI | TypeScript (Next.js) |
| `demo/` | Demo system used for development and benchmarks ([README](demo/README.md)) | Go, Python |
| `bench/` | Fault scenarios with ground truth, and their schema ([README](bench/README.md)) | YAML, Python |
| `deploy/` | Docker Compose stack, Collector config | |
| `docs/` | Roadmap, architecture, decision records | |

## Docs

- [Roadmap](docs/ROADMAP.md)
- [Architecture](docs/architecture.md)
- [Decision records](docs/adr/)
- [Telemetry conventions](docs/telemetry-conventions.md)
- [Contributing](CONTRIBUTING.md)

## License

[Apache 2.0](LICENSE)
