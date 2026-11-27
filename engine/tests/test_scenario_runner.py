import json
from collections.abc import Mapping
from datetime import UTC, datetime, timedelta
from pathlib import Path
from typing import Any

import pytest

from incidentlens.scenario import runner
from incidentlens.scenario.runner import RunError, StackInfo
from incidentlens.scenario.spec import Scenario, ScenarioError

T0 = datetime(2026, 12, 1, 14, 0, 0, tzinfo=UTC)
OFF = {
    "catalog.cache_disabled": False,
    "catalog.latency_ms.postgres": 0,
    "payments.latency_ms.postgres": 0,
    "orders.pool_size": 0,
}
INFO = StackInfo(
    environment="demo",
    load={"LOADGEN_RPS": "20", "DB_POOL_MAX": {"orders": None}},
    versions={"payments": "1.0.0"},
)


class Clock:
    """Time moves only when the run sleeps or waits. interrupt_on_sleep: Ctrl-C on that sleep."""

    def __init__(self, log: list, interrupt_on_sleep: int | None = None) -> None:
        self.t = T0
        self.log = log
        self.sleeps = 0
        self.interrupt_on_sleep = interrupt_on_sleep

    def now(self) -> datetime:
        return self.t

    def sleep(self, seconds: float) -> None:
        self.sleeps += 1
        self.log.append(("sleep", seconds))
        if self.sleeps == self.interrupt_on_sleep:
            self.t += timedelta(seconds=seconds / 2)
            raise KeyboardInterrupt
        self.t += timedelta(seconds=seconds)

    def advance(self, seconds: float) -> None:
        self.t += timedelta(seconds=seconds)


class Flags:
    def __init__(self, log: list, clock: Clock, on: Mapping[str, Any] | None = None) -> None:
        self.state = dict(on or {})
        self.log = log
        self.clock = clock
        self.fail_wait: str | None = None
        self.clear_interrupts = 0
        self.off_wait_interrupts = 0

    def on(self) -> dict[str, Any]:
        return {k: v for k, v in self.state.items() if v != OFF[k]}

    def off(self, key: str) -> Any:
        return OFF[key]

    def set(self, values: Mapping[str, Any]) -> None:
        self.log.append(("set", dict(values)))
        self.state.update(values)
        self.clock.advance(0.01)

    def clear(self) -> None:
        if self.clear_interrupts:
            self.clear_interrupts -= 1
            raise KeyboardInterrupt
        self.log.append(("clear",))
        self.state = {}

    def wait(self, values: Mapping[str, Any]) -> None:
        self.log.append(("wait", dict(values)))
        if self.off_wait_interrupts and all(v == OFF[k] for k, v in values.items()):
            self.off_wait_interrupts -= 1
            raise KeyboardInterrupt
        if self.fail_wait and self.fail_wait in values:
            raise RunError(f"flagd serves {self.fail_wait}=0 after 30s")
        self.clock.advance(1)


class Changes:
    def __init__(self, log: list, fail: bool = False) -> None:
        self.log = log
        self.fail = fail
        self.events: list[dict] = []

    def post(self, event: Mapping[str, Any]) -> str:
        if self.fail:
            raise RunError("changes API answered 503")
        self.log.append(("post", event["id"]))
        self.events.append(dict(event))
        return event["id"]


class Stack:
    def __init__(self, problem: str | None = None) -> None:
        self.problem = problem

    def check(self) -> StackInfo:
        if self.problem:
            raise ScenarioError(self.problem)
        return INFO


def scenario(**kw: Any) -> Scenario:
    fields: dict[str, Any] = {
        "id": "catalog-cache-disabled",
        "file": "bench/scenarios/catalog-cache-disabled.yaml",
        "sha256": "ab" * 32,
        "warmup": 300,
        "duration": 600,
        "baseline": {"catalog.latency_ms.postgres": 15},
        "fault": {"catalog.cache_disabled": True},
        "deploy": None,
        "change_event": "flag",
        "expected": {"root_cause": {"service": "catalog", "kind": "cache_disabled"}},
    }
    fields.update(kw)
    return Scenario(**fields)


class Harness:
    def __init__(self, tmp_path: Path, **kw: Any) -> None:
        self.log: list = []
        self.clock = Clock(self.log, kw.pop("interrupt_on_sleep", None))
        self.flags = Flags(self.log, self.clock, kw.pop("on", None))
        self.changes = Changes(self.log, kw.pop("changes_fail", False))
        self.stack = Stack(kw.pop("stack_problem", None))
        self.runs = tmp_path / "runs"
        self.messages: list[str] = []
        self.kw = kw

    def run(self, s: Scenario, overrides: Mapping[str, int] | None = None) -> runner.Result:
        return runner.run(
            s,
            flags=self.flags,
            changes=self.changes,
            stack=self.stack,
            clock=self.clock,
            runs_dir=self.runs,
            git={"commit": "f8af89a", "dirty": False},
            overrides=overrides,
            log=self.messages.append,
        )

    def manifest(self) -> dict:
        (path,) = self.runs.glob("*/manifest.json")
        return json.loads(path.read_text())


def steps(m: dict) -> dict[str, str]:
    return {s["step"]: s["time"] for s in m["steps"]}


def test_flag_scenario_runs_the_steps_in_order(tmp_path: Path) -> None:
    h = Harness(tmp_path)
    result = h.run(scenario())

    assert result.status == "completed"
    assert h.log == [
        ("set", {"catalog.latency_ms.postgres": 15}),
        ("wait", {"catalog.latency_ms.postgres": 15}),
        ("sleep", 300),
        ("set", {"catalog.cache_disabled": True}),
        ("post", "catalog-cache-disabled-20261201T140000Z.catalog.cache_disabled"),
        ("wait", {"catalog.latency_ms.postgres": 15, "catalog.cache_disabled": True}),
        # The fault is held for its duration from the moment it was set, so
        # the second it took flagd to serve it is not added on top.
        ("sleep", 599),
        ("clear",),
        ("wait", {"catalog.latency_ms.postgres": 0, "catalog.cache_disabled": False}),
    ]
    assert h.flags.on() == {}


def test_manifest(tmp_path: Path) -> None:
    h = Harness(tmp_path)
    result = h.run(scenario())
    m = h.manifest()
    assert m == result.manifest
    assert result.run_dir == h.runs / "catalog-cache-disabled-20261201T140000Z"

    t = steps(m)
    assert m["schema"] == "incidentlens.run/v0"
    assert m["id"] == "catalog-cache-disabled-20261201T140000Z"
    assert m["status"] == "completed"
    assert m["error"] is None
    assert m["scenario"] == {
        "id": "catalog-cache-disabled",
        "file": "bench/scenarios/catalog-cache-disabled.yaml",
        "sha256": "ab" * 32,
    }
    assert m["git"] == {"commit": "f8af89a", "dirty": False}
    assert m["environment"] == "demo"
    # From the start of the warmup, after the baseline is served, to the end of the fault.
    assert t["warmup_started"] == "2026-12-01T14:00:01.010Z"
    assert t["fault_set"] == "2026-12-01T14:05:01.020Z"
    assert m["window"] == {"start": "2026-12-01T14:00:01.010Z", "end": "2026-12-01T14:15:01.020Z"}
    assert t["fault_ended"] == m["window"]["end"]
    assert list(t) == [
        "started",
        "baseline_set",
        "baseline_applied",
        "warmup_started",
        "fault_set",
        "change_events_posted",
        "fault_applied",
        "fault_ended",
        "flags_cleared",
        "flags_off",
    ]
    assert (m["warmup"], m["duration"], m["overrides"]) == ("5m", "10m", {})
    assert m["flags"] == {
        "baseline": {"catalog.latency_ms.postgres": 15},
        "fault": {"catalog.cache_disabled": True},
    }
    assert m["change_events"] == ["catalog-cache-disabled-20261201T140000Z.catalog.cache_disabled"]
    assert m["expected"] == scenario().expected
    assert m["load"] == INFO.load
    assert m["versions"] == INFO.versions


def test_flag_change_event(tmp_path: Path) -> None:
    h = Harness(tmp_path)
    h.run(scenario())
    (event,) = h.changes.events
    assert event == {
        "id": "catalog-cache-disabled-20261201T140000Z.catalog.cache_disabled",
        "time": steps(h.manifest())["fault_set"],
        "kind": "flag",
        "environment": "demo",
        "service": "catalog",
        "attributes": {
            "flag.key": "catalog.cache_disabled",
            "flag.value.from": "false",
            "flag.value.to": "true",
        },
        "source": "scenario-runner",
    }


def test_flag_change_event_from_baseline_value(tmp_path: Path) -> None:
    h = Harness(tmp_path)
    h.run(
        scenario(
            fault={"catalog.latency_ms.postgres": 800, "catalog.cache_disabled": True},
        )
    )
    froms = {
        e["attributes"]["flag.key"]: e["attributes"]["flag.value.from"] for e in h.changes.events
    }
    assert froms == {"catalog.latency_ms.postgres": "15", "catalog.cache_disabled": "false"}


def test_change_event_none_posts_nothing(tmp_path: Path) -> None:
    h = Harness(tmp_path)
    h.run(scenario(change_event="none"))
    assert h.changes.events == []
    assert h.manifest()["change_events"] == []
    assert "change_events_posted" not in steps(h.manifest())


def test_no_baseline(tmp_path: Path) -> None:
    h = Harness(tmp_path)
    h.run(scenario(baseline={}, fault={"payments.latency_ms.postgres": 800}, change_event="none"))
    assert h.log[:2] == [("sleep", 300), ("set", {"payments.latency_ms.postgres": 800})]
    assert steps(h.manifest())["warmup_started"] == "2026-12-01T14:00:00.000Z"


def test_overrides(tmp_path: Path) -> None:
    h = Harness(tmp_path)
    h.run(scenario(), overrides={"warmup": 30, "duration": 90})
    sleeps = [e[1] for e in h.log if e[0] == "sleep"]
    assert sleeps == [30, 89]
    m = h.manifest()
    assert (m["warmup"], m["duration"]) == ("30s", "90s")
    assert m["overrides"] == {"warmup": "30s", "duration": "90s"}


@pytest.mark.parametrize("sleep", [1, 2], ids=["warmup", "hold"])
def test_ctrl_c_clears_the_flags(tmp_path: Path, sleep: int) -> None:
    h = Harness(tmp_path, interrupt_on_sleep=sleep)
    result = h.run(scenario())

    assert result.status == "interrupted"
    assert h.log[-2] == ("clear",)
    assert h.flags.on() == {}
    m = h.manifest()
    assert m["status"] == "interrupted"
    assert "flags_cleared" in steps(m)
    if sleep == 1:
        # Interrupted in the warmup: the fault never started, so there is no window.
        assert m["window"] is None
        assert h.changes.events == []
    else:
        assert m["window"]["end"] == steps(m)["fault_ended"]
        assert m["window"]["end"] == "2026-12-01T14:10:01.520Z"


def test_ctrl_c_during_cleanup_still_clears(tmp_path: Path) -> None:
    h = Harness(tmp_path, interrupt_on_sleep=2)
    h.flags.clear_interrupts = 2
    result = h.run(scenario())
    assert result.status == "interrupted"
    assert h.flags.on() == {}
    assert "clearing the flags first" in h.messages


def test_ctrl_c_while_confirming_the_flags_are_off(tmp_path: Path) -> None:
    # A terminal Ctrl-C under uv arrives twice: the second one lands here.
    h = Harness(tmp_path, interrupt_on_sleep=2)
    h.flags.off_wait_interrupts = 2
    h.run(scenario())
    assert "flags_off" in steps(h.manifest())
    assert not any("not confirmed" in m for m in h.messages)


def test_failed_post_clears_the_flags(tmp_path: Path) -> None:
    h = Harness(tmp_path, changes_fail=True)
    with pytest.raises(RunError, match="503"):
        h.run(scenario())
    assert h.flags.on() == {}
    m = h.manifest()
    assert m["status"] == "failed"
    assert m["error"] == "changes API answered 503"
    assert "fault_applied" not in steps(m)


def test_flags_not_served_clears_the_flags(tmp_path: Path) -> None:
    h = Harness(tmp_path)
    h.flags.fail_wait = "catalog.latency_ms.postgres"
    with pytest.raises(RunError, match="flagd serves"):
        h.run(scenario())
    assert ("clear",) in h.log
    assert h.flags.on() == {}
    m = h.manifest()
    assert m["status"] == "failed"
    assert m["window"] is None
    assert "flags_off" not in steps(m)


def test_refuses_when_a_flag_is_on(tmp_path: Path) -> None:
    h = Harness(tmp_path, on={"orders.pool_size": 1})
    with pytest.raises(ScenarioError, match="orders.pool_size=1"):
        h.run(scenario())
    # Someone else's flag is left alone.
    assert h.log == []
    assert h.flags.on() == {"orders.pool_size": 1}
    assert not h.runs.exists()


def test_refuses_when_the_stack_is_not_ready(tmp_path: Path) -> None:
    h = Harness(tmp_path, stack_problem="the stack is not ready (loadgen is exited)")
    with pytest.raises(ScenarioError, match="loadgen"):
        h.run(scenario())
    assert h.log == []
    assert not h.runs.exists()


def test_refuses_a_deploy(tmp_path: Path) -> None:
    h = Harness(tmp_path)
    s = scenario(
        id="payments-bad-deploy",
        deploy={"service": "payments", "version": "1.0.1"},
        change_event="deploy",
    )
    with pytest.raises(ScenarioError, match="#27"):
        h.run(s)
    assert h.log == []
    assert not h.runs.exists()
