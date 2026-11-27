# engine

Reconstruction engine and the `incidentlens` CLI. The reconstruction itself is not implemented yet. What exists:

- `incidentlens scenario run <id>`: runs a benchmark scenario on the local stack and writes a manifest under `runs/`. See [Running a scenario](../bench/README.md#running-a-scenario). It reads the scenario files, `demo/faults.py` and `bench/validate.py` from the checkout, so run it from inside the repo (`make scenario ID=<id>` from the root).

```sh
uv sync
uv run incidentlens --version
uv run pytest
```
