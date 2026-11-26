# Roadmap

Five phases. Each one ends with something that runs and can be shown. Phases map to [milestones](https://github.com/dinhostork/IncidentLens/milestones) and each milestone has an epic issue that lists its work.

The order matters more than the dates. The benchmark comes before the engine on purpose: without scenarios that have a known answer there is no way to tell whether a change to the engine made it better or worse.

## Phase 0: Foundation

Goal: a local environment where a demo system produces realistic telemetry and faults can be injected on demand.

- Monorepo layout and tooling for Go, Python and TypeScript
- Docker Compose stack: OpenTelemetry Collector, ClickHouse, flagd
- Demo system: four small services instrumented with OpenTelemetry, plus Postgres and Redis
- Baseline traffic from a k6 load generator
- Fault injection through feature flags in the demo services
- Scenario file format with ground truth, and a first set of scenarios
- Telemetry conventions (required resource attributes, how versions and environments are tagged)
- CI for lint and tests

Done when: `make up` starts everything, traffic flows, and flipping a scenario's flag visibly breaks something in the telemetry.

## Phase 1: Ingestion and storage

Goal: telemetry and change events land in ClickHouse and can be queried by time window.

- Collector writing traces, logs and metrics to ClickHouse
- Change events API (Go): deploys, config changes, flag flips
- Scenario runner posts change events when a scenario includes them
- Queries for a window: service graph, per-service and per-edge rate/error/latency series, error logs
- Retention and table settings documented

Done when: for any window, a single CLI call returns the service graph and the time series behind it.

## Phase 2: Reconstruction engine

Goal: given a window, produce a ranked list of root cause candidates with evidence, and measure it.

- Onset detection per service and edge (start with EWMA and CUSUM, compare)
- Candidate ranking: earliest onset, depth in the graph, proximity to change events
- Log template mining (Drain) for error logs
- Evidence bundle format (JSON schema, versioned)
- `incidentlens bench`: runs every scenario, reports top-1 and top-3 accuracy and time to reconstruct
- First published results in the README, including the scenarios it gets wrong

Done when: the bench runs end to end in CI on a reduced scenario set and the full results are in the README.

## Phase 3: Replay UI and narrative

Goal: look at a reconstruction in the browser and get a postmortem draft from it.

- Timeline view with change events and onsets
- Service map that replays how errors and latency spread over the window
- Candidate list with links into the raw evidence
- LLM provider interface (Ollama, Gemini, Anthropic, OpenAI-compatible)
- Postmortem draft where every sentence cites evidence IDs; uncited or invalid claims are removed before display
- Works fully with the LLM disabled

Done when: someone who did not build it can open a reconstruction and follow the evidence for each claim.

## Phase 4: Snapshots and public demo

Goal: share a reconstruction without running the stack.

- Snapshot format: one file with the telemetry subset, the evidence bundle and the narrative
- Export and import from the CLI and the UI
- Static demo site that replays snapshots of the benchmark scenarios
- Optional live demo on a small VM with a scheduled fault
- Write-up: design, results, what did not work

Done when: the demo site is public and the write-up is out.

## Not planned

These are out of scope. Mature tools already cover them, and building them here would only spread the project thin.

- On-call schedules, paging, escalation
- Chat bots (Slack, Teams)
- Kubernetes operator
- Multi-tenancy, user accounts, auth beyond a single shared token
- Being a general observability backend or replacing Grafana

## Cost

Everything here is meant to run for free: open-source components, local LLMs through Ollama or a free API tier, and a static demo site. The live demo is optional.
