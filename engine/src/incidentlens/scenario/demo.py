"""The real flags, changes API, stack and clock behind a scenario run."""

import json
import subprocess
import time
import urllib.error
import urllib.request
from collections.abc import Callable, Mapping
from datetime import UTC, datetime
from pathlib import Path
from types import ModuleType
from typing import Any

from incidentlens.scenario.runner import Clock, RunError, StackInfo, flag_text
from incidentlens.scenario.spec import ScenarioError

# Everything a run needs. loadgen has no health check, the rest must be healthy.
STACK_SERVICES = (
    "clickhouse",
    "otel-collector",
    "changes",
    "flagd",
    "postgres",
    "redis",
    "gateway",
    "catalog",
    "orders",
    "payments",
    "loadgen",
)
DEMO_SERVICES = ("gateway", "catalog", "orders", "payments")
DB_SERVICES = ("catalog", "orders", "payments")


class SystemClock:
    def now(self) -> datetime:
        return datetime.now(UTC)

    def sleep(self, seconds: float) -> None:
        time.sleep(seconds)


def _same(a: Any, b: Any) -> bool:
    # True == 1 in Python, but not for a flag.
    if isinstance(a, bool) or isinstance(b, bool):
        return a is b
    return a == b


class DemoFlags:
    """Flags written by demo/faults.py, the same code make fault runs, and read back from flagd.

    flagd serves the flags it loaded over OFREP (HTTP). wait() polls that until
    flagd serves the new values; the services get them from flagd's sync stream
    at the same time.
    """

    def __init__(
        self,
        faults: ModuleType,
        ofrep_url: str,
        *,
        timeout: float = 30,
        poll: float = 0.25,
        clock: Clock | None = None,
    ) -> None:
        self._faults = faults
        self._ofrep = ofrep_url.rstrip("/") + "/ofrep/v1/evaluate/flags"
        self._timeout = timeout
        self._poll = poll
        self._clock = clock or SystemClock()

    def _base(self) -> dict:
        return self._faults.load(self._faults.BASE)

    def on(self) -> dict[str, Any]:
        return self._faults.current(self._base())

    def off(self, key: str) -> Any:
        return self._faults.off_value(self._base(), key)

    def set(self, values: Mapping[str, Any]) -> None:
        self._faults_cli("set", *(f"{k}={flag_text(v)}" for k, v in values.items()))

    def clear(self) -> None:
        self._faults_cli("clear")

    def _faults_cli(self, *args: str) -> None:
        # faults.py prints what is wrong to stderr itself.
        if self._faults.main(list(args)) != 0:
            raise RunError(f"demo/faults.py {' '.join(args)} failed")

    def served(self) -> dict[str, Any]:
        req = urllib.request.Request(
            self._ofrep,
            data=b'{"context":{}}',
            headers={"Content-Type": "application/json"},
            method="POST",
        )
        with urllib.request.urlopen(req, timeout=5) as resp:
            body = json.load(resp)
        return {f["key"]: f.get("value") for f in body.get("flags", [])}

    def wait(self, values: Mapping[str, Any]) -> None:
        deadline = self._clock.now().timestamp() + self._timeout
        while True:
            try:
                served = self.served()
            except (OSError, ValueError) as e:
                problem = f"did not answer: {e}"
            else:
                wrong = [k for k, v in values.items() if not _same(served.get(k), v)]
                if not wrong:
                    return
                problem = "serves " + " ".join(f"{k}={json.dumps(served.get(k))}" for k in wrong)
            if self._clock.now().timestamp() >= deadline:
                raise RunError(f"flagd at {self._ofrep} {problem} after {self._timeout:g}s")
            self._clock.sleep(self._poll)


class ChangesClient:
    """POST /v1/changes on the changes API. Retries are safe because events carry an id."""

    def __init__(self, url: str, token: str = "", *, attempts: int = 3) -> None:
        self._url = url.rstrip("/") + "/v1/changes"
        self._token = token
        self._attempts = attempts

    def post(self, event: Mapping[str, Any]) -> str:
        headers = {"Content-Type": "application/json"}
        if self._token:
            headers["Authorization"] = f"Bearer {self._token}"
        data = json.dumps(event).encode()
        for attempt in range(1, self._attempts + 1):
            req = urllib.request.Request(self._url, data=data, headers=headers, method="POST")
            try:
                with urllib.request.urlopen(req, timeout=10) as resp:
                    return json.load(resp)["id"]
            except urllib.error.HTTPError as e:
                body = e.read().decode(errors="replace").strip()
                if e.code != 503 or attempt == self._attempts:
                    raise RunError(f"changes API answered {e.code} to {event['id']}: {body}") from e
            except OSError as e:
                if attempt == self._attempts:
                    raise RunError(f"changes API at {self._url}: {e}") from e
            time.sleep(attempt)
        raise AssertionError("unreachable")


Command = Callable[[list[str]], str]


def _run(args: list[str]) -> str:
    try:
        out = subprocess.run(args, capture_output=True, text=True, check=True)
    except FileNotFoundError as e:
        raise ScenarioError(f"{args[0]} not found") from e
    except subprocess.CalledProcessError as e:
        raise ScenarioError(f"{' '.join(args[:3])} failed: {e.stderr.strip()}") from e
    return out.stdout


class ComposeStack:
    """The compose stack from deploy/compose/compose.yaml, as docker compose sees it."""

    def __init__(self, root: Path, run: Command = _run) -> None:
        self._compose = ["docker", "compose", "-f", str(root / "deploy/compose/compose.yaml")]
        self._run = run

    def check(self) -> StackInfo:
        out = self._run([*self._compose, "ps", "--all", "--format", "json"]).strip()
        # Compose prints one object per line; older versions printed one array.
        rows = json.loads(out) if out.startswith("[") else [json.loads(x) for x in out.splitlines()]
        by_service = {row["Service"]: row for row in rows}

        problems = []
        for s in STACK_SERVICES:
            row = by_service.get(s)
            if row is None:
                problems.append(f"{s} is not created")
            elif row["State"] != "running":
                problems.append(f"{s} is {row['State']}")
            elif row.get("Health") not in ("", "healthy"):
                problems.append(f"{s} is {row['Health']}")
        if problems:
            raise ScenarioError(f"the stack is not ready ({', '.join(problems)}); run make up")

        ids = [by_service[s]["ID"] for s in (*DEMO_SERVICES, "loadgen")]
        env = {}
        for c in json.loads(self._run(["docker", "inspect", *ids])):
            service = c["Config"]["Labels"]["com.docker.compose.service"]
            env[service] = dict(e.split("=", 1) for e in c["Config"].get("Env") or [])

        environments = {_environment(env[s]) for s in DEMO_SERVICES}
        if len(environments) != 1 or None in environments:
            raise ScenarioError(
                "the demo services do not agree on deployment.environment.name: "
                + ", ".join(f"{s}={_environment(env[s])}" for s in DEMO_SERVICES)
            )
        load: dict[str, Any] = {
            k: v for k, v in sorted(env["loadgen"].items()) if k.startswith("LOADGEN_")
        }
        # None means unset, so the service uses its default (10).
        load["DB_POOL_MAX"] = {s: env[s].get("DB_POOL_MAX") for s in DB_SERVICES}
        return StackInfo(
            environment=environments.pop(),
            load=load,
            versions={s: env[s].get("SERVICE_VERSION", "") for s in DEMO_SERVICES},
        )


def _environment(env: Mapping[str, str]) -> str | None:
    for attr in env.get("OTEL_RESOURCE_ATTRIBUTES", "").split(","):
        key, _, value = attr.partition("=")
        if key.strip() == "deployment.environment.name":
            return value.strip()
    return None


def git_info(root: Path, run: Command = _run) -> dict[str, Any] | None:
    """The commit the run was made from, and whether the working tree had changes."""
    try:
        commit = run(["git", "-C", str(root), "rev-parse", "HEAD"]).strip()
        dirty = bool(run(["git", "-C", str(root), "status", "--porcelain"]).strip())
    except ScenarioError:
        return None
    return {"commit": commit, "dirty": dirty}
