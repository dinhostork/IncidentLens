# 0004. Scenario format and ground truth

- Status: Accepted
- Date: 2026-11-26

## Context

The benchmark ([ADR 0003](0003-benchmark-first.md)) needs a file per scenario that a runner can execute and that states the right answer. Once results are published, the meaning of that answer cannot drift: if "root cause" means one thing in December and another in March, the two accuracy numbers cannot be compared.

Two things make a naive format misleading. If every scenario records a change event, an engine that only looks at recent changes scores well. And the demo's dependencies are faster than real ones (Postgres answers in about a millisecond), so some faults, like a disabled cache or a smaller connection pool, produce no visible effect on their own.

## Decision

- One YAML file per scenario in `bench/scenarios/`, checked against `bench/schema/scenario.schema.json` and against the demo's flag list by `bench/validate.py`.
- A scenario has optional `baseline` flags that stay on for the whole run and count as normal, a `warmup`, and a `fault`: flags and optionally a deploy, held for a `duration`.
- `change_event` (`none`, `flag`, `deploy`) is separate from the fault. It only decides what the runner records, so the same fault can be run with or without a trace in the change log.
- The answer is `expected.root_cause`: the service whose behavior changed first, a `kind` from a closed list, and the dependency for the kinds that involve one. Accuracy is computed against this.
- `expected.symptoms` lists the services and signals (latency, errors) that were seen to change on the demo at the default load. They describe propagation and are not part of the accuracy score.
- Changing the kind list or what `root_cause` means requires a new ADR.

## Consequences

- Scenarios can leave out the change event, and the set is expected to keep a fair share that do.
- Baseline flags make each scenario's normal slightly different. An engine cannot assume one fixed baseline across scenarios, which is closer to real systems anyway.
- Fault values are tuned to the default load (`LOADGEN_RPS=20`, `DB_POOL_MAX=10`). Changing those can turn a scenario into a non-event, and symptoms have to be checked again by hand until the runner can verify them.
- A closed kind list will not cover every real failure. Faults that do not fit wait for a new ADR rather than a catch-all value.
- How partial answers are scored (right service, wrong kind; naming the database node instead of the service that calls it) is left to Phase 2. The file records enough to score either way.
