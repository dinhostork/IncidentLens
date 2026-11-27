import json
import shutil
import threading
from collections.abc import Iterator
from datetime import datetime, timedelta
from http.server import BaseHTTPRequestHandler, HTTPServer
from pathlib import Path
from types import ModuleType

import pytest

from incidentlens.scenario import demo, spec
from incidentlens.scenario.runner import RunError
from incidentlens.scenario.spec import ScenarioError

ROOT = Path(__file__).resolve().parents[2]


@pytest.fixture
def faults(tmp_path: Path, monkeypatch: pytest.MonkeyPatch) -> ModuleType:
    """demo/faults.py pointed at a copy of the flag files, so the stack's flags are not touched."""
    module = spec.load_script(ROOT / "demo" / "faults.py", "faults_under_test")
    shutil.copy(ROOT / "deploy" / "flags" / "flags.json", tmp_path / "flags.json")
    monkeypatch.setattr(module, "FLAGS_DIR", tmp_path)
    monkeypatch.setattr(module, "BASE", tmp_path / "flags.json")
    monkeypatch.setattr(module, "ACTIVE", tmp_path / "active.json")
    return module


class FakeFlagd:
    """Answers OFREP bulk evaluation with whatever `served` holds."""

    def __init__(self) -> None:
        self.served: dict[str, object] = {}
        flagd = self

        class Handler(BaseHTTPRequestHandler):
            def do_POST(self) -> None:
                assert self.path == "/ofrep/v1/evaluate/flags"
                self.rfile.read(int(self.headers["Content-Length"]))
                flags = [{"key": k, "value": v} for k, v in flagd.served.items()]
                body = json.dumps({"flags": flags}).encode()
                self.send_response(200)
                self.send_header("Content-Type", "application/json")
                self.end_headers()
                self.wfile.write(body)

            def log_message(self, *args: object) -> None:
                pass

        self.server = HTTPServer(("127.0.0.1", 0), Handler)
        self.url = f"http://127.0.0.1:{self.server.server_port}"


@pytest.fixture
def flagd() -> Iterator[FakeFlagd]:
    f = FakeFlagd()
    t = threading.Thread(target=f.server.serve_forever, daemon=True)
    t.start()
    yield f
    f.server.shutdown()


def test_flags_go_through_faults_py(faults: ModuleType) -> None:
    flags = demo.DemoFlags(faults, "http://127.0.0.1:1")
    assert flags.on() == {}
    flags.set({"catalog.latency_ms.postgres": 15})
    flags.set({"catalog.cache_disabled": True, "payments.error_rate": 0.25})
    assert flags.on() == {
        "catalog.latency_ms.postgres": 15,
        "catalog.cache_disabled": True,
        "payments.error_rate": 0.25,
    }
    active = json.loads((faults.ACTIVE).read_text())["flags"]["catalog.cache_disabled"]
    assert active["defaultVariant"] == "on"
    assert active["variants"]["on"] is True
    assert flags.off("catalog.cache_disabled") is False

    flags.clear()
    assert flags.on() == {}


def test_flags_set_rejects_what_make_fault_rejects(faults: ModuleType) -> None:
    flags = demo.DemoFlags(faults, "http://127.0.0.1:1")
    with pytest.raises(RunError, match="faults.py set"):
        flags.set({"payments.error_rate": 2})
    assert flags.on() == {}


class StepClock:
    def __init__(self) -> None:
        self.t = demo.SystemClock().now()

    def now(self) -> datetime:
        return self.t

    def sleep(self, seconds: float) -> None:
        self.t += timedelta(seconds=seconds)


def test_wait_returns_once_flagd_serves_the_values(faults: ModuleType, flagd: FakeFlagd) -> None:
    flagd.served = {"catalog.cache_disabled": True, "catalog.latency_ms.postgres": 15}
    flags = demo.DemoFlags(faults, flagd.url, clock=StepClock())
    flags.wait({"catalog.cache_disabled": True, "catalog.latency_ms.postgres": 15})


@pytest.mark.parametrize(
    ("served", "want"),
    [
        ({"catalog.cache_disabled": False}, {"catalog.cache_disabled": True}),
        # True == 1 in Python; a flag served as 1 is not the boolean the scenario set.
        ({"catalog.cache_disabled": 1}, {"catalog.cache_disabled": True}),
        ({"payments.latency_ms.postgres": 0}, {"payments.latency_ms.postgres": 800}),
        ({}, {"payments.error_rate": 0.25}),
    ],
)
def test_wait_times_out(faults: ModuleType, flagd: FakeFlagd, served: dict, want: dict) -> None:
    flagd.served = served
    flags = demo.DemoFlags(faults, flagd.url, timeout=2, poll=0.5, clock=StepClock())
    with pytest.raises(RunError, match="after 2s"):
        flags.wait(want)


def test_wait_when_flagd_is_down(faults: ModuleType) -> None:
    flags = demo.DemoFlags(faults, "http://127.0.0.1:1", timeout=1, clock=StepClock())
    with pytest.raises(RunError, match="did not answer"):
        flags.wait({"catalog.cache_disabled": True})


def ps_row(service: str, state: str = "running", health: str = "healthy") -> str:
    return json.dumps({"Service": service, "State": state, "Health": health, "ID": f"id-{service}"})


def inspect(service: str, env: dict[str, str]) -> dict:
    return {
        "Config": {
            "Labels": {"com.docker.compose.service": service},
            "Env": [f"{k}={v}" for k, v in env.items()],
        }
    }


ATTRS = "deployment.environment.name=demo,service.namespace=shop"


class Docker:
    def __init__(self, rows: list[str], env: dict[str, dict[str, str]]) -> None:
        self.rows = rows
        self.env = env
        self.calls: list[list[str]] = []

    def __call__(self, args: list[str]) -> str:
        self.calls.append(args)
        if args[:2] == ["docker", "compose"]:
            return "\n".join(self.rows) + "\n"
        assert args[:2] == ["docker", "inspect"]
        return json.dumps(
            [inspect(i.removeprefix("id-"), self.env[i.removeprefix("id-")]) for i in args[2:]]
        )


def healthy_docker(**changes: dict[str, str]) -> Docker:
    rows = [ps_row(s, health="" if s == "loadgen" else "healthy") for s in demo.STACK_SERVICES]
    rows.append(ps_row("probe", state="exited", health=""))
    env = {
        s: {"OTEL_RESOURCE_ATTRIBUTES": ATTRS, "SERVICE_VERSION": "1.0.0"}
        for s in demo.DEMO_SERVICES
    }
    env["orders"]["DB_POOL_MAX"] = "4"
    env["loadgen"] = {"LOADGEN_RPS": "20", "LOADGEN_JITTER": "0.1", "PATH": "/usr/bin"}
    for s, e in changes.items():
        env[s].update(e)
    return Docker(rows, env)


def test_stack_check(tmp_path: Path) -> None:
    docker = healthy_docker(payments={"SERVICE_VERSION": "1.0.1"})
    info = demo.ComposeStack(tmp_path, run=docker).check()
    assert docker.calls[0] == [
        "docker", "compose", "-f", str(tmp_path / "deploy/compose/compose.yaml"),
        "ps", "--all", "--format", "json",
    ]  # fmt: skip
    assert info.environment == "demo"
    assert info.load == {
        "LOADGEN_JITTER": "0.1",
        "LOADGEN_RPS": "20",
        "DB_POOL_MAX": {"catalog": None, "orders": "4", "payments": None},
    }
    assert info.versions == {
        "gateway": "1.0.0",
        "catalog": "1.0.0",
        "orders": "1.0.0",
        "payments": "1.0.1",
    }


def test_stack_check_reads_the_old_array_format(tmp_path: Path) -> None:
    docker = healthy_docker()
    docker.rows = ["[" + ",".join(docker.rows) + "]"]
    assert demo.ComposeStack(tmp_path, run=docker).check().environment == "demo"


@pytest.mark.parametrize(
    ("row", "problem"),
    [
        (None, "loadgen is not created"),
        (ps_row("loadgen", state="exited", health=""), "loadgen is exited"),
        (ps_row("loadgen", state="restarting", health=""), "loadgen is restarting"),
    ],
)
def test_stack_check_needs_the_load_generator(
    tmp_path: Path, row: str | None, problem: str
) -> None:
    docker = healthy_docker()
    docker.rows = [r for r in docker.rows if '"loadgen"' not in r] + ([row] if row else [])
    with pytest.raises(ScenarioError, match=problem):
        demo.ComposeStack(tmp_path, run=docker).check()


def test_stack_check_needs_healthy_services(tmp_path: Path) -> None:
    docker = healthy_docker()
    docker.rows = [r for r in docker.rows if '"flagd"' not in r]
    docker.rows.append(ps_row("flagd", health="starting"))
    with pytest.raises(ScenarioError, match=r"flagd is starting\); run make up"):
        demo.ComposeStack(tmp_path, run=docker).check()


def test_stack_check_environment_must_agree(tmp_path: Path) -> None:
    docker = healthy_docker(catalog={"OTEL_RESOURCE_ATTRIBUTES": "service.namespace=shop"})
    with pytest.raises(ScenarioError, match="catalog=None"):
        demo.ComposeStack(tmp_path, run=docker).check()


def test_git_info(tmp_path: Path) -> None:
    out = {"rev-parse": "8f65b10\n", "status": " M engine/README.md\n"}
    assert demo.git_info(tmp_path, run=lambda a: out[a[3]]) == {"commit": "8f65b10", "dirty": True}
    out["status"] = ""
    assert demo.git_info(tmp_path, run=lambda a: out[a[3]]) == {"commit": "8f65b10", "dirty": False}


def test_git_info_without_git(tmp_path: Path) -> None:
    def fail(args: list[str]) -> str:
        raise ScenarioError("git not found")

    assert demo.git_info(tmp_path, run=fail) is None


class FakeChangesAPI:
    """Answers POST /v1/changes with the statuses in `answers`, then 201."""

    def __init__(self, answers: list[int]) -> None:
        self.answers = answers
        self.requests: list[tuple[dict, str | None]] = []
        api = self

        class Handler(BaseHTTPRequestHandler):
            def do_POST(self) -> None:
                assert self.path == "/v1/changes"
                event = json.loads(self.rfile.read(int(self.headers["Content-Length"])))
                api.requests.append((event, self.headers.get("Authorization")))
                status = api.answers.pop(0) if api.answers else 201
                body = event if status in (200, 201) else {"error": "nope"}
                self.send_response(status)
                self.send_header("Content-Type", "application/json")
                self.end_headers()
                self.wfile.write(json.dumps(body).encode())

            def log_message(self, *args: object) -> None:
                pass

        self.server = HTTPServer(("127.0.0.1", 0), Handler)
        self.url = f"http://127.0.0.1:{self.server.server_port}"


@pytest.fixture
def changes_api(request: pytest.FixtureRequest) -> Iterator[FakeChangesAPI]:
    api = FakeChangesAPI(list(getattr(request, "param", [])))
    t = threading.Thread(target=api.server.serve_forever, daemon=True)
    t.start()
    yield api
    api.server.shutdown()


EVENT = {"id": "run-1.catalog.cache_disabled", "kind": "flag"}


def test_post(changes_api: FakeChangesAPI) -> None:
    assert demo.ChangesClient(changes_api.url).post(EVENT) == EVENT["id"]
    assert changes_api.requests == [(EVENT, None)]


def test_post_with_token(changes_api: FakeChangesAPI) -> None:
    demo.ChangesClient(changes_api.url + "/", "s3cret").post(EVENT)
    assert changes_api.requests == [(EVENT, "Bearer s3cret")]


@pytest.mark.parametrize("changes_api", [[503]], indirect=True)
def test_post_retries_a_503_with_the_same_id(
    changes_api: FakeChangesAPI, monkeypatch: pytest.MonkeyPatch
) -> None:
    monkeypatch.setattr(demo.time, "sleep", lambda s: None)
    assert demo.ChangesClient(changes_api.url).post(EVENT) == EVENT["id"]
    assert [e for e, _ in changes_api.requests] == [EVENT, EVENT]


@pytest.mark.parametrize("changes_api", [[409]], indirect=True)
def test_post_does_not_retry_a_conflict(changes_api: FakeChangesAPI) -> None:
    with pytest.raises(RunError, match="answered 409 to run-1.catalog.cache_disabled"):
        demo.ChangesClient(changes_api.url).post(EVENT)
    assert len(changes_api.requests) == 1


def test_post_gives_up(monkeypatch: pytest.MonkeyPatch) -> None:
    monkeypatch.setattr(demo.time, "sleep", lambda s: None)
    with pytest.raises(RunError, match="changes API at"):
        demo.ChangesClient("http://127.0.0.1:1").post(EVENT)
