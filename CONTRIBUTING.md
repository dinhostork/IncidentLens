# Contributing

The project is early and the layout is still settling, so check the [roadmap](docs/ROADMAP.md) and open issues before starting on something big. For anything beyond a small fix, open an issue first.

## Workflow

- Branch from `main`: `feat/...`, `fix/...`, `docs/...`, `chore/...`
- Commit messages follow [Conventional Commits](https://www.conventionalcommits.org/): `feat(engine): add CUSUM onset detector`
- One logical change per pull request. Link the issue it closes.
- CI must pass before merge.

## Local setup

You need Go 1.26+ (required by the OpenTelemetry Go SDK in the demo services), [uv](https://docs.astral.sh/uv/) (it fetches Python 3.12 if needed), Node 22 with pnpm (`corepack enable`), [golangci-lint](https://golangci-lint.run/) v2 and Docker Compose.

```sh
make setup   # Python and web dependencies
make lint
make test
```

`make up` builds the demo services and starts the local stack (Collector, ClickHouse, flagd, the demo system), see [`deploy/README.md`](deploy/README.md). `make help` lists the other targets.

## Decisions

If a change affects storage, data formats, or how the engine reaches a conclusion, write an ADR in `docs/adr/` as part of the PR.

## Scenarios

New fault scenarios are very welcome, especially ones based on incidents you have seen in real systems. A scenario needs a clear description, a way to trigger it in the demo system and the expected root cause.
