# IncidentLens

Reconstruct production incidents from telemetry and change events.

Pick a time window and IncidentLens builds a timeline of what happened, shows how the failure spread across services and ranks the most likely causes. Every claim in the output points back to the span, log line or change event it came from, so anyone can check it.

> **Status:** early development. Nothing runs yet. See the [roadmap](docs/ROADMAP.md) for what is being built and in which order.

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
| `changes/` | Change events API | Go |
| `web/` | Replay UI | TypeScript (Next.js) |
| `demo/` | Demo system used for development and benchmarks | Go, Python |
| `bench/scenarios/` | Fault scenarios with ground truth | YAML |
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
