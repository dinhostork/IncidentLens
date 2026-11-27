"""Scenario files: finding the repository, loading and checking a scenario."""

import hashlib
import importlib.util
import re
from dataclasses import dataclass
from pathlib import Path
from types import ModuleType
from typing import Any

_DURATION = re.compile(r"^([1-9][0-9]*)([sm])$")
_ID = re.compile(r"^[a-z0-9]+(-[a-z0-9]+)*$")


class ScenarioError(Exception):
    """A scenario cannot be found, does not validate, or cannot be run yet."""


def find_root(start: Path) -> Path:
    """The repository root at or above start, recognized by bench/scenarios and demo/faults.py."""
    for d in (start, *start.parents):
        if (d / "bench" / "scenarios").is_dir() and (d / "demo" / "faults.py").is_file():
            return d
    raise ScenarioError(f"no IncidentLens checkout at or above {start}; run this from the repo")


def load_script(path: Path, name: str) -> ModuleType:
    """Import a standalone script from the repo (demo/faults.py, bench/validate.py) by path.

    Both are plain scripts rather than packages, and the runner uses them as
    they are so that flags and scenario checks have one implementation each.
    """
    spec = importlib.util.spec_from_file_location(name, path)
    if spec is None or spec.loader is None:
        raise ScenarioError(f"cannot import {path}")
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    return module


def parse_duration(raw: str) -> int:
    """Seconds in a scenario duration: whole seconds or minutes, 90s or 5m."""
    m = _DURATION.match(raw)
    if not m:
        raise ValueError(f"want whole seconds or minutes such as 90s or 5m, got {raw!r}")
    return int(m.group(1)) * (60 if m.group(2) == "m" else 1)


def format_duration(seconds: int) -> str:
    return f"{seconds // 60}m" if seconds % 60 == 0 else f"{seconds}s"


@dataclass(frozen=True)
class Scenario:
    id: str
    file: str  # relative to the repo root
    sha256: str
    warmup: int  # seconds
    duration: int  # seconds
    baseline: dict[str, Any]
    fault: dict[str, Any]
    deploy: dict[str, str] | None
    change_event: str
    expected: dict[str, Any]


def load(root: Path, scenario_id: str) -> Scenario:
    """Load bench/scenarios/<id>.yaml and check it the way make check-scenarios does."""
    if not _ID.match(scenario_id):
        raise ScenarioError(f"{scenario_id!r} is not a scenario id")
    path = root / "bench" / "scenarios" / f"{scenario_id}.yaml"
    if not path.is_file():
        known = sorted(p.stem for p in path.parent.glob("*.yaml"))
        raise ScenarioError(f"no scenario {scenario_id!r}; known: {', '.join(known)}")

    validate = load_script(root / "bench" / "validate.py", "incidentlens_bench_validate")
    problems = validate.Checker().check_file(path)
    if problems:
        raise ScenarioError(f"{path.name} is not valid: " + "; ".join(problems))
    doc = validate.load_file(path)
    fault = doc["fault"]
    return Scenario(
        id=doc["id"],
        file=str(path.relative_to(root)),
        sha256=hashlib.sha256(path.read_bytes()).hexdigest(),
        warmup=parse_duration(doc["warmup"]),
        duration=parse_duration(fault["duration"]),
        baseline=dict(doc.get("baseline", {}).get("flags", {})),
        fault=dict(fault.get("flags", {})),
        deploy=fault.get("deploy"),
        change_event=doc["change_event"],
        expected=doc["expected"],
    )
