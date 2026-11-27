# Benchmark scenarios

Each scenario is a fault on the [demo system](../demo/README.md) with a known root cause. `incidentlens bench` (Phase 2) will run them all and report how often the engine's top candidates match. Why the benchmark comes first: [ADR 0003](../docs/adr/0003-benchmark-first.md). Why the format looks like this: [ADR 0004](../docs/adr/0004-scenario-format.md).

| Path | What |
|---|---|
| `scenarios/<id>.yaml` | One scenario per file |
| `schema/scenario.schema.json` | JSON Schema for a scenario file |
| `validate.py` | Checks files against the schema and against the demo's flags |
| `tests/` | Tests for the validator, plus one test per scenario file |

```sh
make check-scenarios   # validate every scenario
make scenario ID=<id>  # run one on the stack, see Running a scenario
make test-py           # includes the bench tests
```

## Format

```yaml
id: catalog-cache-disabled        # same as the file name
description: >
  A flag turns off catalog's Redis cache. ...
warmup: 5m                        # normal traffic before the fault
baseline:                         # optional, on for the whole run
  flags:
    catalog.latency_ms.postgres: 15
fault:
  flags:                          # set when the fault starts
    catalog.cache_disabled: true
  deploy:                         # optional, restart with a new service.version
    service: catalog
    version: 1.0.1
  duration: 10m
change_event: flag                # none | flag | deploy
expected:
  root_cause:
    service: catalog
    kind: cache_disabled
    dependency: redis
  symptoms:
    - service: catalog
      signal: latency
```

Durations are whole seconds or minutes (`90s`, `5m`). Flag keys and values are the ones `make fault` takes, see [the flag list](../demo/README.md#flags).

### A run

A run sets the `baseline` flags, waits for `warmup`, starts the fault, records the change event if `change_event` asks for one, holds the fault for `duration` and then clears every flag. A fault with `deploy` also restarts the service with the new version, and puts the old one back at the end. The window to reconstruct runs from the start of the warmup to the end of the fault. [Running a scenario](#running-a-scenario) has the details.

### Baseline flags

The demo's Postgres answers in about a millisecond. At that speed a cache saves nothing and a pool of one connection is enough for the default load, so some faults would not show. `baseline` flags give a scenario a slower, more realistic dependency for the whole run. They are part of normal in that scenario: the warmup runs with them, and they are never recorded as a change.

Use them only when a scenario needs them, and say why in a comment.

### Change events

`change_event` decides what the runner records, separately from what the fault does:

| Value | Recorded |
|---|---|
| `none` | Nothing. The engine has only telemetry to go on. |
| `flag` | One `flag` change event per fault flag. Not allowed together with `deploy`. |
| `deploy` | One `deploy` change event for the service and version in `fault.deploy`. Requires `deploy`. |

In a deploy scenario the fault flags stand for what the new version does wrong, so they are never recorded as flag changes. The `deploy` event is timed at the start of the rollout, as a CI job would post it. A deploy with `change_event: none` is still visible in telemetry, as a new `service.version`.

Real incidents often have no recorded change, and an engine that leans on change events would look better than it is if every scenario had one. Keep a fair share of scenarios at `none`.

### Expected answer

`root_cause.service` is the service whose behavior changed first. `kind` says what went wrong there, from this list:

| Kind | Meaning | `dependency` |
|---|---|---|
| `dependency_latency` | The service's calls to a dependency got slower | the dependency |
| `dependency_errors` | The service's calls to a dependency fail | the dependency |
| `pool_exhaustion` | The service runs out of connections to a dependency, and requests wait for one | the dependency |
| `cache_disabled` | The service stopped using its cache and sends every read to the backing store | the cache |
| `cpu_saturation` | The service spends its time on the CPU, and requests queue | none |
| `handler_errors` | The service's own code fails requests | none |

The list is closed on purpose: a reconstruction is scored against it, and new kinds make old results hard to compare. Adding one means a new ADR.

A dependency fault belongs to the service that sees it. In `payments-db-latency` only payments' queries are slow, so the root cause is payments with dependency postgres, not Postgres itself. How an answer that names the `postgresql@postgres` node is scored is left to Phase 2.

`symptoms` lists the services whose own requests change, and whether in latency or errors. Each one was checked on the demo at the default load (`LOADGEN_RPS=20`, `DB_POOL_MAX=10`). A symptom can be confined to one route: checkout is about 2% of gateway requests, so a slow checkout shows on that route and on the gateway to orders edge, but not in the gateway's overall p95.

## Scenarios

| Id | Fault | Change event | Root cause |
|---|---|---|---|
| [`payments-db-latency`](scenarios/payments-db-latency.yaml) | payments to Postgres +800 ms | none | payments, `dependency_latency` postgres |
| [`catalog-cache-disabled`](scenarios/catalog-cache-disabled.yaml) | catalog skips Redis, Postgres at 15 ms | flag | catalog, `cache_disabled` redis |
| [`orders-pool-exhaustion`](scenarios/orders-pool-exhaustion.yaml) | orders pool 10 to 1, queries at 80 ms | none | orders, `pool_exhaustion` postgres |
| [`payments-bad-deploy`](scenarios/payments-bad-deploy.yaml) | payments 1.0.1 fails 25% of requests | deploy | payments, `handler_errors` |
| [`catalog-cpu-burn`](scenarios/catalog-cpu-burn.yaml) | catalog spends 30 ms of CPU per request | none | catalog, `cpu_saturation` |

## Running a scenario

```sh
make up
make scenario ID=catalog-cache-disabled
make scenario ID=catalog-cache-disabled WARMUP=30s DURATION=1m   # shorter, while developing
```

`make scenario` runs `incidentlens scenario run <id>` from [`engine/`](../engine/). The runner:

1. Checks the scenario as `make check-scenarios` does, and refuses to start unless every service in the stack is running and healthy, the load generator is running and no fault flag is on. It does not touch flags it did not set. A deploy scenario is also refused when the service already runs the new version.
2. Sets the `baseline` flags and waits until flagd serves them.
3. Waits for `warmup`.
4. Starts the fault:
   - Without `deploy`, sets the `fault` flags. With `change_event: flag` it posts one `flag` change event per fault flag to the [changes API](../changes/README.md), timed when the flags were written. Then it waits until flagd serves them.
   - With `deploy`, posts the `deploy` change event if `change_event: deploy`, stops the service, sets the `fault` flags and waits until flagd serves them, then starts the service with `<SERVICE>_VERSION` set to the new version and waits until it is healthy. Nothing else in the stack is restarted.
5. Holds the fault for `duration`, counted from when the flags were written, or from the start of the rollout for a deploy.
6. Clears every flag and waits until flagd serves them off. After a deploy, restarts the service with the version it had before.

Flags are written by `demo/faults.py`, the same code `make fault` runs. Whether flagd serves a value is checked over its OFREP endpoint (port 8016); the services get the same update from flagd's sync stream, usually within a second of the write.

Step 6 also runs on Ctrl-C, on SIGTERM and when a step fails. Further Ctrl-C presses during cleanup do not stop it. If the flags cannot be cleared the runner says so and exits 1; run `make fault-clear` and check `make faults`. If the old version cannot be put back it exits 1 and prints the command that does it.

The flags of a deploy go on while neither version runs, so spans of the old version never show the bug and the new version has it from its first request. Stopping one and starting the other takes about 6 to 8 seconds, during which calls to the service fail, as they would in a real deploy without a rolling update. Why it is done this way: [ADR 0007](../docs/adr/0007-deploys-in-scenario-runs.md).

| Variable | Default | |
|---|---|---|
| `CHANGES_URL` | `http://127.0.0.1:8090` | Changes API. |
| `CHANGES_TOKEN` | unset | Sent as a bearer token when set, as the API's `CHANGES_TOKEN`. |
| `FLAGD_OFREP_URL` | `http://127.0.0.1:8016` | flagd's OFREP endpoint. |

Exit status: 0 for a completed run, 130 when interrupted, 1 when a step failed, 2 when the runner refused to start.

### The manifest

Each run writes `runs/<scenario id>-<UTC start time>/manifest.json` at the repo root (gitignored). It is written when the run starts, with status `running`, and again at the end. The reasoning behind its contents is in [ADR 0006](../docs/adr/0006-scenario-runs.md).

| Field | |
|---|---|
| `schema` | `incidentlens.run/v0` |
| `id` | Run id, the directory name. |
| `status` | `completed`, `interrupted`, `failed`, or `running` if the process died without cleaning up. Only completed runs hold the fault for the full duration. |
| `error` | What failed, for `failed` runs. |
| `scenario` | `id`, `file` and the `sha256` of the scenario file. |
| `git` | `commit` and `dirty` (uncommitted or untracked files in the checkout). |
| `environment` | The demo's `deployment.environment.name`. |
| `window` | `start` and `end` of the window to reconstruct. `null` if the run stopped before the fault started. |
| `warmup`, `duration`, `overrides` | Effective values, and the ones given on the command line. |
| `steps` | Each step with its time: `started`, `baseline_set`, `baseline_applied`, `warmup_started`, `fault_set`, `change_events_posted`, `fault_applied`, `fault_ended`, `flags_cleared`, `flags_off`. Deploys add `deploy_started` (the start of the rollout, before `change_events_posted`), `old_version_stopped` and `new_version_healthy`, and after `flags_off`, `rollback_started` and `rollback_healthy`. Steps that did not happen are left out. |
| `flags` | `baseline` and `fault` flags from the scenario. |
| `change_events` | IDs of the change events posted: `<run id>.<flag key>` for flags, `<run id>.deploy` for a deploy. |
| `deploy` | `service` and `version` (`from`, `to`) of the deploy, or `null`. |
| `expected` | Copied from the scenario. |
| `load` | `LOADGEN_*` from the load generator and `DB_POOL_MAX` per service, as the containers have them. `null` means unset, so the service default of 10. |
| `versions` | `service.version` of each demo service when the run started. |

The window starts at `warmup_started` and ends at `fault_ended`. For a deploy it includes the restart. Times are UTC with millisecond precision.

## Writing a scenario

- Start the stack, set the flags by hand and look at the effect in ClickHouse before writing the file. A fault that only moves numbers by a few percent will be scored as a miss for reasons that have nothing to do with the engine.
- List as symptoms only what you saw change.
- Values are tuned to the default load. If you change `LOADGEN_RPS` or `DB_POOL_MAX`, check that the scenarios still produce what they claim.
- Variety matters more than count: faults at the edge and deep in the graph, with and without change events, and ones whose symptoms point somewhere else (catalog's CPU burn makes its Redis calls look slow).
