import hashlib
import shutil
from pathlib import Path

import pytest

from incidentlens.scenario import spec
from incidentlens.scenario.spec import ScenarioError

ROOT = Path(__file__).resolve().parents[2]


def test_find_root() -> None:
    assert spec.find_root(ROOT / "engine" / "src") == ROOT
    assert spec.find_root(ROOT) == ROOT


def test_find_root_outside_the_repo(tmp_path: Path) -> None:
    with pytest.raises(ScenarioError, match="run this from the repo"):
        spec.find_root(tmp_path)


@pytest.mark.parametrize(("raw", "seconds"), [("1s", 1), ("90s", 90), ("5m", 300), ("10m", 600)])
def test_parse_duration(raw: str, seconds: int) -> None:
    assert spec.parse_duration(raw) == seconds


@pytest.mark.parametrize("raw", ["", "0s", "5", "1h", "1.5m", "05m", "-1s", "5 m"])
def test_parse_duration_rejects(raw: str) -> None:
    with pytest.raises(ValueError, match="whole seconds or minutes"):
        spec.parse_duration(raw)


@pytest.mark.parametrize(("seconds", "text"), [(30, "30s"), (60, "1m"), (90, "90s"), (600, "10m")])
def test_format_duration(seconds: int, text: str) -> None:
    assert spec.format_duration(seconds) == text


def test_load() -> None:
    s = spec.load(ROOT, "catalog-cache-disabled")
    path = ROOT / "bench" / "scenarios" / "catalog-cache-disabled.yaml"
    assert s.id == "catalog-cache-disabled"
    assert s.file == "bench/scenarios/catalog-cache-disabled.yaml"
    assert s.sha256 == hashlib.sha256(path.read_bytes()).hexdigest()
    assert (s.warmup, s.duration) == (300, 600)
    assert s.baseline == {"catalog.latency_ms.postgres": 15}
    assert s.fault == {"catalog.cache_disabled": True}
    assert s.deploy is None
    assert s.change_event == "flag"
    assert s.expected["root_cause"]["kind"] == "cache_disabled"


def test_load_every_scenario() -> None:
    for path in sorted((ROOT / "bench" / "scenarios").glob("*.yaml")):
        s = spec.load(ROOT, path.stem)
        assert s.id == path.stem
        assert s.fault or s.deploy


def test_load_deploy() -> None:
    s = spec.load(ROOT, "payments-bad-deploy")
    assert s.deploy == {"service": "payments", "version": "1.0.1"}
    assert s.fault == {"payments.error_rate": 0.25}


@pytest.mark.parametrize("bad", ["../engine/pyproject", "Payments", "a/b", ""])
def test_load_rejects_ids(bad: str) -> None:
    with pytest.raises(ScenarioError, match="not a scenario id"):
        spec.load(ROOT, bad)


def test_load_unknown() -> None:
    with pytest.raises(ScenarioError, match="known: catalog-cache-disabled"):
        spec.load(ROOT, "no-such-scenario")


def test_load_checks_the_file(tmp_path: Path) -> None:
    # A copy of the parts of the repo the loader reads, with one broken scenario.
    for d in ("bench", "deploy/flags"):
        shutil.copytree(ROOT / d, tmp_path / d, ignore=shutil.ignore_patterns(".venv", "*cache*"))
    (tmp_path / "demo").mkdir()
    shutil.copy(ROOT / "demo" / "faults.py", tmp_path / "demo")
    path = tmp_path / "bench" / "scenarios" / "payments-db-latency.yaml"
    path.write_text(path.read_text().replace("payments.latency_ms.postgres", "payments.nope"))

    with pytest.raises(ScenarioError, match="unknown flag"):
        spec.load(tmp_path, "payments-db-latency")
