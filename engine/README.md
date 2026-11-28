# engine

Reconstruction engine and the `incidentlens` CLI. The reconstruction itself is not implemented yet. What exists:

- `incidentlens scenario run <id>`: runs a benchmark scenario on the local stack and writes a manifest under `runs/`. See [Running a scenario](../bench/README.md#running-a-scenario). It reads the scenario files, `demo/faults.py` and `bench/validate.py` from the checkout, so run it from inside the repo (`make scenario ID=<id>` from the root).
- `incidentlens recording save <run>` and `incidentlens recording load <path>`: save a run's telemetry and change events from ClickHouse to files, and load them into a database of their own. See [Recordings](../bench/README.md#recordings). `src/incidentlens/recording/tables.json` lists the ClickHouse columns the engine is written against.

```sh
uv sync
uv run incidentlens --version
uv run pytest
```
