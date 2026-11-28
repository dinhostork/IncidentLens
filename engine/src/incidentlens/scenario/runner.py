"""The steps of a scenario run, and the manifest it leaves behind.

The flags, the changes API, the stack and the clock are passed in, so the
order of the steps and the cleanup can be tested without the stack. The real
implementations are in incidentlens.scenario.demo.
"""

import json
import os
import tempfile
from collections.abc import Callable, Mapping
from dataclasses import dataclass, field
from datetime import UTC, datetime, timedelta
from pathlib import Path
from typing import Any, Protocol

from incidentlens.scenario.spec import Scenario, ScenarioError, format_duration

MANIFEST_SCHEMA = "incidentlens.run/v0"
SOURCE = "scenario-runner"


class RunError(Exception):
    """A step of the run failed. Raised after the flags have been cleared."""


class Flags(Protocol):
    def on(self) -> dict[str, Any]:
        """Flags that are set to something other than off."""
        ...

    def off(self, key: str) -> Any:
        """The off value of a flag."""
        ...

    def set(self, values: Mapping[str, Any]) -> None:
        """Turn flags on, leaving the others as they are."""
        ...

    def clear(self) -> None:
        """Turn every flag off."""
        ...

    def wait(self, values: Mapping[str, Any]) -> None:
        """Return once the flags serve these values. Raises RunError on timeout."""
        ...


class Changes(Protocol):
    def post(self, event: Mapping[str, Any]) -> str:
        """Store a change event and return its id."""
        ...


class Clock(Protocol):
    def now(self) -> datetime: ...

    def sleep(self, seconds: float) -> None: ...


@dataclass(frozen=True)
class StackInfo:
    environment: str  # deployment.environment.name of the demo services
    load: dict[str, Any]  # LOADGEN_* and DB_POOL_MAX as the containers have them
    versions: dict[str, str]  # service.version per demo service


class Stack(Protocol):
    def check(self) -> StackInfo:
        """Raise ScenarioError unless the stack is up and the load generator is running."""
        ...

    def stop(self, service: str) -> None:
        """Stop a demo service and return once its container has exited. Raises RunError."""
        ...

    def deploy(self, service: str, version: str) -> None:
        """Start a demo service as `version` and return once it is healthy. Raises RunError."""
        ...


@dataclass
class Result:
    status: str  # completed, interrupted or failed
    run_dir: Path
    manifest: dict[str, Any]


@dataclass
class _Run:
    scenario: Scenario
    clock: Clock
    log: Callable[[str], None]
    steps: list[dict[str, str]] = field(default_factory=list)
    times: dict[str, datetime] = field(default_factory=dict)
    change_events: list[str] = field(default_factory=list)
    # Set before the old version is stopped: from then on cleanup must put it back.
    deployed: bool = False

    def mark(self, step: str) -> datetime:
        t = self.clock.now()
        self.steps.append({"step": step, "time": iso(t)})
        self.times[step] = t
        return t

    def sleep_until(self, t: datetime) -> None:
        left = (t - self.clock.now()).total_seconds()
        if left > 0:
            self.clock.sleep(left)


def iso(t: datetime) -> str:
    return t.astimezone(UTC).isoformat(timespec="milliseconds").replace("+00:00", "Z")


def flag_text(value: Any) -> str:
    """A flag value as text: true, 800, 0.25. The same text make fault takes."""
    return json.dumps(value)


def _show(values: Mapping[str, Any]) -> str:
    return " ".join(f"{k}={flag_text(v)}" for k, v in values.items())


def run(
    scenario: Scenario,
    *,
    flags: Flags,
    changes: Changes,
    stack: Stack,
    clock: Clock,
    runs_dir: Path,
    git: Mapping[str, Any] | None,
    overrides: Mapping[str, int] | None = None,
    log: Callable[[str], None] = lambda _: None,
) -> Result:
    """Run a scenario: the steps in bench/README.md, then clear every flag.

    Refuses with ScenarioError, before changing anything, when the stack is
    not ready, a flag is already on or the service to deploy already runs the
    new version. Once a flag has been set, every way out (normal end, Ctrl-C,
    an error) clears all flags, puts a deployed service back on its old
    version and writes the manifest. Ctrl-C returns a Result with status
    interrupted; any other error is raised again after cleanup.
    """
    overrides = dict(overrides or {})
    warmup = overrides.get("warmup", scenario.warmup)
    duration = overrides.get("duration", scenario.duration)

    info = stack.check()
    deploy = None
    if scenario.deploy is not None:
        service, to = scenario.deploy["service"], scenario.deploy["version"]
        if service not in info.versions:
            raise ScenarioError(f"{service} is not a demo service")
        old = info.versions[service]
        if old == to:
            raise ScenarioError(
                f"{service} already runs {to}, maybe left over from a killed run. "
                "Put it back with make up"
            )
        deploy = {"service": service, "version": {"from": old, "to": to}}
    already = flags.on()
    if already:
        raise ScenarioError(
            f"flags are already on: {_show(already)}. "
            "Clear them with make fault-clear if nothing else is using them"
        )

    r = _Run(scenario, clock, log)
    started = r.mark("started")
    run_id = f"{scenario.id}-{started.astimezone(UTC):%Y%m%dT%H%M%SZ}"
    run_dir = runs_dir / run_id
    run_dir.mkdir(parents=True)
    fault_state = {**scenario.baseline, **scenario.fault}

    def manifest(status: str, error: str | None) -> dict[str, Any]:
        start, end = r.times.get("warmup_started"), r.times.get("fault_ended")
        return {
            "schema": MANIFEST_SCHEMA,
            "id": run_id,
            "status": status,
            "error": error,
            "scenario": {"id": scenario.id, "file": scenario.file, "sha256": scenario.sha256},
            "git": dict(git) if git is not None else None,
            "environment": info.environment,
            "window": {"start": iso(start), "end": iso(end)} if start and end else None,
            "warmup": format_duration(warmup),
            "duration": format_duration(duration),
            "overrides": {k: format_duration(v) for k, v in overrides.items()},
            "steps": r.steps,
            "flags": {"baseline": scenario.baseline, "fault": scenario.fault},
            "change_events": r.change_events,
            "deploy": deploy,
            "expected": scenario.expected,
            "load": info.load,
            "versions": info.versions,
        }

    _write(run_dir / "manifest.json", manifest("running", None))
    log(f"run {run_id}")

    status, error, raised = "completed", None, None
    try:
        if scenario.baseline:
            flags.set(scenario.baseline)
            r.mark("baseline_set")
            log(f"baseline flags set: {_show(scenario.baseline)}")
            flags.wait(scenario.baseline)
            r.mark("baseline_applied")

        warmup_started = r.mark("warmup_started")
        log(f"warmup {format_duration(warmup)}, fault at {iso(warmup_started + _s(warmup))}")
        r.sleep_until(warmup_started + _s(warmup))

        if deploy is not None:
            fault_started = _roll_out(r, deploy, flags, changes, stack, fault_state, run_id, info)
        else:
            fault_started = _set_fault(r, flags, changes, fault_state, run_id, info)

        log(f"holding {format_duration(duration)}, until {iso(fault_started + _s(duration))}")
        r.sleep_until(fault_started + _s(duration))
    except KeyboardInterrupt:
        status = "interrupted"
        log("interrupted")
    except Exception as e:
        status, error, raised = "failed", str(e), e
        log(f"failed: {e}")
    finally:
        if "fault_set" in r.times or "deploy_started" in r.times:
            # The window ends when the fault is taken away, whatever the reason.
            r.mark("fault_ended")
        cleared = _clear(flags, r)
        # Written before waiting on flagd too, in case the process is killed
        # while it waits.
        _write(run_dir / "manifest.json", manifest(status, error))
        if cleared:
            _confirm_off(flags, r, fault_state)
        rolled_back = True
        if r.deployed and deploy is not None:
            rolled_back = _roll_back(stack, r, deploy["service"], deploy["version"]["from"])

    m = manifest(status, error)
    _write(run_dir / "manifest.json", m)
    log(f"manifest: {run_dir / 'manifest.json'}")
    if not cleared:
        raise RunError("could not clear the flags; run make fault-clear and check make faults")
    if not rolled_back and deploy is not None:
        service, old = deploy["service"], deploy["version"]["from"]
        raise RunError(
            f"could not put {service} back on {old}; run "
            f"{service.upper()}_VERSION={old} docker compose -f deploy/compose/compose.yaml "
            f"up -d --wait --no-deps {service}"
        )
    if raised is not None:
        raise RunError(str(raised)) from raised
    return Result(status, run_dir, m)


def _set_fault(
    r: _Run,
    flags: Flags,
    changes: Changes,
    fault_state: Mapping[str, Any],
    run_id: str,
    info: StackInfo,
) -> datetime:
    """Set the fault flags and post flag change events. Returns when the fault started."""
    scenario = r.scenario
    flags.set(scenario.fault)
    fault_set = r.mark("fault_set")
    r.log(f"fault flags set: {_show(scenario.fault)}")
    if scenario.change_event == "flag":
        for key, value in scenario.fault.items():
            event = {
                "id": f"{run_id}.{key}",
                "time": iso(fault_set),
                "kind": "flag",
                "environment": info.environment,
                # Demo flag keys start with the one service that reads them.
                "service": key.split(".")[0],
                "attributes": {
                    "flag.key": key,
                    "flag.value.from": flag_text(scenario.baseline.get(key, flags.off(key))),
                    "flag.value.to": flag_text(value),
                },
                "source": SOURCE,
            }
            r.change_events.append(changes.post(event))
        r.mark("change_events_posted")
        r.log(f"change events posted: {', '.join(r.change_events)}")
    flags.wait(fault_state)
    r.mark("fault_applied")
    return fault_set


def _roll_out(
    r: _Run,
    deploy: Mapping[str, Any],
    flags: Flags,
    changes: Changes,
    stack: Stack,
    fault_state: Mapping[str, Any],
    run_id: str,
    info: StackInfo,
) -> datetime:
    """Replace the service with the new version, which has the fault flags' bug from the start.

    The flags go on while neither version runs: after the old container has
    exited and before the new one starts. The demo runs one container per
    service, so no span of the old version can show the bug (ADR 0007).
    Returns when the rollout started, which is also when the fault started.
    """
    service, old, new = deploy["service"], deploy["version"]["from"], deploy["version"]["to"]
    started = r.mark("deploy_started")
    r.log(f"deploying {service} {new} (from {old})")
    if r.scenario.change_event == "deploy":
        # Timed at the start of the rollout, the way a CI job posts it.
        event = {
            "id": f"{run_id}.deploy",
            "time": iso(started),
            "kind": "deploy",
            "environment": info.environment,
            "service": service,
            "attributes": {"version.from": old, "version.to": new},
            "source": SOURCE,
        }
        r.change_events.append(changes.post(event))
        r.mark("change_events_posted")
        r.log(f"change event posted: {r.change_events[-1]}")

    r.deployed = True
    stack.stop(service)
    r.mark("old_version_stopped")
    r.log(f"{service} {old} stopped")
    if r.scenario.fault:
        flags.set(r.scenario.fault)
        r.mark("fault_set")
        r.log(f"fault flags set: {_show(r.scenario.fault)}")
        flags.wait(fault_state)
        r.mark("fault_applied")
    stack.deploy(service, new)
    r.mark("new_version_healthy")
    r.log(f"{service} {new} healthy")
    return started


def _roll_back(stack: Stack, r: _Run, service: str, version: str) -> bool:
    """Put the service back on its old version, ignoring Ctrl-C until it is done."""
    r.mark("rollback_started")
    r.log(f"putting {service} back on {version}")
    for attempt in range(3):
        try:
            stack.deploy(service, version)
            break
        except KeyboardInterrupt:
            r.log(f"putting {service} back on {version} first")
        except Exception as e:
            r.log(f"putting {service} back on {version} failed: {e}")
            if attempt == 2:
                return False
    else:
        return False
    r.mark("rollback_healthy")
    r.log(f"{service} {version} healthy")
    return True


def _clear(flags: Flags, r: _Run) -> bool:
    """Clear every flag, ignoring Ctrl-C until it is done. False if clearing failed."""
    for attempt in range(3):
        try:
            flags.clear()
            break
        except KeyboardInterrupt:
            r.log("clearing the flags first")
        except Exception as e:
            r.log(f"clearing the flags failed: {e}")
            if attempt == 2:
                return False
    else:
        return False
    r.mark("flags_cleared")
    r.log("flags cleared")
    return True


def _confirm_off(flags: Flags, r: _Run, state: Mapping[str, Any]) -> None:
    """Wait until flagd serves the flags off. Only warns: the file is already written.

    A Ctrl-C in a terminal usually arrives twice, once from the terminal and
    once forwarded by uv, so a few interruptions are absorbed here too.
    """
    for _ in range(3):
        try:
            flags.wait({k: flags.off(k) for k in state})
            r.mark("flags_off")
            return
        except KeyboardInterrupt:
            continue
        except Exception as e:
            r.log(f"flagd has not confirmed the flags are off: {e}")
            return
    r.log("flagd has not confirmed the flags are off: interrupted")


def _s(seconds: int) -> timedelta:
    return timedelta(seconds=seconds)


def _write(path: Path, doc: Mapping[str, Any]) -> None:
    fd, tmp = tempfile.mkstemp(dir=path.parent, prefix=".manifest-", suffix=".json")
    try:
        with os.fdopen(fd, "w") as f:
            json.dump(doc, f, indent=2)
            f.write("\n")
        os.replace(tmp, path)
    except BaseException:
        os.unlink(tmp)
        raise
