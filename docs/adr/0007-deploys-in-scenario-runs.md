# 0007. Deploys in scenario runs

- Status: Accepted
- Date: 2026-11-27

## Context

A bad deploy scenario changes a service's `service.version` and its behavior together. In the demo the version comes from `SERVICE_VERSION`, read at startup, so a new version means a new container. The behavior comes from the same fault flags the other scenarios use, and those flags are global: flagd evaluates them with an empty context, so every running copy of the service reads the same value.

That leaves three things to decide, all of which change what the benchmark measures:

- How to keep the bug off the old version. If the flags go on while 1.0.0 still serves, its spans show errors and the engine sees a fault that started before the deploy.
- When the fault starts. The restart itself fails some calls before the new version has served anything.
- What time the deploy event carries.

Two ways were considered for the first point. Gating the flags on `service.version` with a flagd targeting rule ties the bug to the version even if both versions serve at once. It needs every demo service to pass `service.version` as evaluation context, `faults.py` to write targeting rules, and the runner to send that context when it checks what flagd serves. The other way is ordering: set the flags while neither version runs.

## Decision

- **Ordering, not targeting.** The runner stops the old container, sets the fault flags and waits until flagd serves them, then starts the new version and waits until it is healthy. The demo runs one container per service and Compose stops the old one before it starts the new one, so the two versions never serve together and no span of the old version can carry the bug. The new version has the bug from its first request. Targeting would only matter with more than one replica or a rolling deploy, which the demo does not have; revisit this if it gets them.
- **The fault starts with the rollout.** The hold is counted from `deploy_started`, when the runner starts stopping the old version. The restart gap belongs to the deploy, as it would in production, and the failed calls during it are part of the fault.
- **The deploy event is timed at the start of the rollout**, before the old version stops, the way a CI job posts it. It is posted at that point too, so a failed post stops the run before anything is restarted. The ID is `<run id>.deploy`. The fault flags of a deploy scenario are never posted as flag changes.
- **Cleanup puts the old version back.** On every exit the runner clears the flags first, waits until flagd serves them off, and then restarts the service with the version it had when the run started. The rollback comes after the end of the window and is not posted as a change event.
- **The manifest records the deploy:** a `deploy` field with the service and `version.from` and `version.to`, and the steps `deploy_started`, `old_version_stopped`, `new_version_healthy`, `rollback_started` and `rollback_healthy`. `versions` keeps the versions from the start of the run. The schema stays `incidentlens.run/v0`; the fields are new and readers of earlier runs see `deploy` missing.
- The runner refuses to start when the service already runs the version the scenario deploys, which is what a run killed with SIGKILL leaves behind.

## Consequences

- The scenario file has no say in how the new version is started; the runner always uses Compose with `<SERVICE>_VERSION`. A scenario that deploys another service works as long as `compose.yaml` reads that variable.
- Between `deploy_started` and `new_version_healthy` (about 6 to 8 seconds on the demo) callers see connection errors. The engine has to cope with that the way it would in a real incident.
- A deploy run is a few seconds longer than `warmup + duration`, for the rollback.
- If the rollback fails the runner exits 1 and prints the command that puts the old version back. The flags are already off at that point.
