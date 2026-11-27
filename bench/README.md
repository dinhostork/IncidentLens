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

1. Set the `baseline` flags and wait for them to apply.
2. Let the load generator run for `warmup`.
3. If there is a `deploy`, restart that service with `SERVICE_VERSION` set to `version` and wait until it is healthy.
4. Set the `fault` flags, and record the change event if `change_event` asks for one.
5. Hold for `duration`, then clear every flag and put back the original version.

The window to reconstruct runs from the start of the warmup to the end of the fault. The scenario runner (Phase 1) will do this. Until then it can be done by hand with `make fault` and the commands in [Simulating a deploy](../demo/README.md#simulating-a-deploy).

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

In a deploy scenario the fault flags stand for what the new version does wrong, so they are never recorded as flag changes. A deploy with `change_event: none` is still visible in telemetry, as a new `service.version`.

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

## Writing a scenario

- Start the stack, set the flags by hand and look at the effect in ClickHouse before writing the file. A fault that only moves numbers by a few percent will be scored as a miss for reasons that have nothing to do with the engine.
- List as symptoms only what you saw change.
- Values are tuned to the default load. If you change `LOADGEN_RPS` or `DB_POOL_MAX`, check that the scenarios still produce what they claim.
- Variety matters more than count: faults at the edge and deep in the graph, with and without change events, and ones whose symptoms point somewhere else (catalog's CPU burn makes its Redis calls look slow).
