# Contributing

The project is early and the layout is still settling, so check the [roadmap](docs/ROADMAP.md) and open issues before starting on something big. For anything beyond a small fix, open an issue first.

## Workflow

- Branch from `main`: `feat/...`, `fix/...`, `docs/...`, `chore/...`
- Commit messages follow [Conventional Commits](https://www.conventionalcommits.org/): `feat(engine): add CUSUM onset detector`
- One logical change per pull request. Link the issue it closes.
- CI must pass before merge.

## Local setup

Not ready yet. It will be a single `make up` once the Phase 0 stack lands. This section will be updated then.

## Decisions

If a change affects storage, data formats, or how the engine reaches a conclusion, write an ADR in `docs/adr/` as part of the PR.

## Scenarios

New fault scenarios are very welcome, especially ones based on incidents you have seen in real systems. A scenario needs a clear description, a way to trigger it in the demo system and the expected root cause.
