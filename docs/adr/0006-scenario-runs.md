# 0006. Scenario runs and the run manifest

- Status: Accepted
- Date: 2026-11-27

## Context

[ADR 0004](0004-scenario-format.md) fixed what a scenario says. It left open what a run of one produces. Phase 2 scores reconstructions against runs, and [#28](https://github.com/dinhostork/IncidentLens/issues/28) records them to files, so both need the same record of each run: the window to reconstruct, when each step happened, which change events were posted and the expected answer.

A few of these choices decide what the benchmark measures, and changing them later would make old and new results incomparable:

- Where the window starts. If it starts at the fault, the engine has no normal period inside the window to compare against.
- What time a change event carries. Stamping it when the effect became visible, instead of when the change was made, hands the engine a cleaner signal than a real flag system or CI job would.
- Which runs count. A run cut short by Ctrl-C has a shorter fault, and one made from uncommitted scenario edits may not match the file in the repo.

## Decision

- `incidentlens scenario run <id>` (`make scenario ID=<id>`) runs a scenario on the compose stack and writes `runs/<id>-<UTC start time>/manifest.json`, with `schema: incidentlens.run/v0`. Its fields are described in [`bench/README.md`](../../bench/README.md#the-manifest).
- **Window.** From the start of the warmup to the end of the fault. The warmup starts once flagd serves the baseline flags. The fault starts when its flags are written and ends when the runner clears them. The hold is counted from the write, so the time flagd takes to pick up the fault is part of the fault, not added after it.
- **Flag change events.** One per fault flag, with `time` set to when the flags were written, `source: scenario-runner`, and `service` set to the service in the flag key, since each demo flag is read by that service only ([ADR 0005](0005-change-events.md)). `flag.value.from` is the baseline value of that flag, or its off value. The ID is `<run id>.<flag key>`, so a retried post cannot store the event twice.
- **Applied means served by flagd.** The runner checks flagd's OFREP endpoint for the new values instead of sleeping a fixed time. The services read the same update from flagd's sync stream.
- **What the manifest records.** Step times, `expected` copied from the scenario, the SHA-256 of the scenario file, the git commit and whether the checkout had changes, `LOADGEN_*` and `DB_POOL_MAX` as the containers have them, each demo service's `service.version`, and a status: `completed`, `interrupted` or `failed`. Only `completed` runs are meant for scoring.
- **Cleanup.** Every flag is cleared on every exit the process can handle: normal end, Ctrl-C, SIGTERM and errors. The runner refuses to start if any fault flag is already on, so it never clears a flag someone else set.
- **Reuse.** Flags are written by importing `demo/faults.py`, and scenarios are checked by importing `bench/validate.py`, both from the checkout. The runner only works inside the repository.

## Consequences

- Every run has a normal period of known length at the start of its window. Onset detection can use it or read further back.
- A run takes `warmup + duration` plus a second or two. Overriding either for development is recorded under `overrides`, so short runs can be told apart from real ones.
- Manifests from a dirty checkout are written anyway. The bench can reject them with `git.dirty`, and `scenario.sha256` shows whether the scenario file matched.
- A run killed with SIGKILL leaves its flags on and its manifest at `running`. The next run refuses to start until someone runs `make fault-clear`.
- `v0` will grow with [#27](https://github.com/dinhostork/IncidentLens/issues/27) (restart times, versions before and after a deploy) and [#28](https://github.com/dinhostork/IncidentLens/issues/28) (what a recording carries over). Readers check `schema`.
