import copy
from pathlib import Path

import pytest

import validate

CHECKER = validate.Checker()

VALID = {
    "id": "x",
    "description": "Something breaks.",
    "warmup": "5m",
    "fault": {"flags": {"payments.latency_ms.postgres": 800}, "duration": "10m"},
    "change_event": "none",
    "expected": {
        "root_cause": {
            "service": "payments",
            "kind": "dependency_latency",
            "dependency": "postgres",
        },
        "symptoms": [{"service": "payments", "signal": "latency"}],
    },
}


def with_(path: str, value: object) -> dict:
    """VALID with the dotted path set to value, or removed if value is None."""
    doc = copy.deepcopy(VALID)
    *parents, last = path.split(".")
    node = doc
    for p in parents:
        node = node.setdefault(p, {})
    if value is None:
        del node[last]
    else:
        node[last] = value
    return doc


@pytest.mark.parametrize("path", sorted(validate.SCENARIOS.glob("*.yaml")), ids=lambda p: p.stem)
def test_scenarios_are_valid(path: Path) -> None:
    assert CHECKER.check_file(path) == []


def test_valid_example() -> None:
    assert CHECKER.check(VALID, "x") == []


def test_deploy_with_flags() -> None:
    doc = with_("fault.deploy", {"service": "payments", "version": "1.0.1"})
    doc["change_event"] = "deploy"
    assert CHECKER.check(doc, "x") == []


@pytest.mark.parametrize(
    ("doc", "problem"),
    [
        (with_("id", "y"), "does not match the file name"),
        (with_("warmup", "5 minutes"), "does not match"),
        (with_("extra", 1), "Additional properties"),
        (with_("fault.flags", {"payments.latency_ms.redis": 10}), "unknown flag"),
        (with_("fault.flags", {"payments.latency_ms.postgres": -1}), "whole number"),
        (with_("fault.flags", {"payments.latency_ms.postgres": 0}), "same as off"),
        (with_("fault.flags", {"payments.error_rate": 1.5}), "from 0 to 1"),
        (with_("fault.flags", {"payments.error_rate": True}), "from 0 to 1"),
        (with_("fault.flags", {"catalog.cache_disabled": 1}), "true or false"),
        (with_("baseline.flags", {"orders.pool_size": 1.5}), "whole number"),
        (with_("fault.flags", None), "is not valid"),
        (with_("change_event", "deploy"), "'deploy' is a required property"),
        (with_("expected.root_cause.kind", "network_partition"), "is not one of"),
        (with_("expected.root_cause.dependency", None), "'dependency' is a required property"),
        (with_("expected.root_cause.service", "orders"), "does not touch 'orders'"),
        (with_("expected.root_cause.service", "billing"), "unknown service"),
        (with_("expected.root_cause.dependency", "kafka"), "unknown dependency"),
        (with_("expected.symptoms", []), "should be non-empty"),
        (with_("expected.symptoms", [{"service": "billing", "signal": "errors"}]), "unknown"),
    ],
)
def test_rejects(doc: dict, problem: str) -> None:
    problems = CHECKER.check(doc, "x")
    assert any(problem in p for p in problems), problems


def test_flag_change_cannot_also_deploy() -> None:
    doc = with_("fault.deploy", {"service": "payments", "version": "1.0.1"})
    doc["change_event"] = "flag"
    assert CHECKER.check(doc, "x")


def test_kind_without_dependency_rejects_one() -> None:
    doc = with_("expected.root_cause", {"service": "payments", "kind": "cpu_saturation"})
    assert CHECKER.check(doc, "x") == []
    doc["expected"]["root_cause"]["dependency"] = "postgres"
    assert CHECKER.check(doc, "x")


def test_duplicate_keys(tmp_path: Path) -> None:
    path = tmp_path / "dup.yaml"
    path.write_text("id: dup\nid: dup\n")
    assert any("duplicate key" in p for p in CHECKER.check_file(path))


def test_main_exit_code(tmp_path: Path, capsys: pytest.CaptureFixture[str]) -> None:
    bad = tmp_path / "bad.yaml"
    bad.write_text("id: bad\n")
    assert validate.main([str(bad)]) == 1
    assert "bad.yaml" in capsys.readouterr().err
    assert validate.main([]) == 0
