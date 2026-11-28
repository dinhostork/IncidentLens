"""Save a scenario run's telemetry and change events to files, and load them back.

A recording is a directory with one zstd-compressed Native file per table
and recording.json, which describes them. The format and the reasons for it
are in docs/adr/0008-recorded-runs.md.
"""

import hashlib
import json
import os
import re
import shutil
import tempfile
from collections.abc import Callable, Mapping
from dataclasses import dataclass
from datetime import UTC, datetime, timedelta
from importlib import resources
from pathlib import Path
from typing import Any, Protocol

from incidentlens.scenario.runner import Clock, iso
from incidentlens.scenario.spec import format_duration

RECORDING_SCHEMA = "incidentlens.recording/v0"
RUN_SCHEMA = "incidentlens.run/v0"
MANIFEST = "recording.json"

DEFAULT_MARGIN = 600
# How long after the end of the window telemetry can still be on its way: the
# SDKs export spans every 5s and metrics every 10s, then the Collector batches.
SETTLE = 30

# Names a recording is never loaded into.
RESERVED = {"otel", "incidentlens", "system", "default", "information_schema"}
_DATABASE = re.compile(r"^[a-z_][a-z0-9_]{0,63}$")


class RecordingError(Exception):
    """A recording cannot be made or loaded. Raised before anything is written."""


class CheckFailed(Exception):
    """The data did not match what was counted or recorded. Nothing is left behind."""


class Server(Protocol):
    """What save and load need from ClickHouse. The real one is clickhouse.ClickHouse."""

    def version(self) -> str: ...

    def columns(self, database: str, table: str) -> list[str]: ...

    def create_statement(self, database: str, table: str) -> str: ...

    def checksum(self, source: str, params: Mapping[str, str] | None = None) -> tuple[int, str]: ...

    def export(self, sql: str, params: Mapping[str, str] | None, path: Path) -> int: ...

    def has_database(self, name: str) -> bool: ...

    def execute(self, sql: str) -> None: ...

    def insert(self, table: str, path: Path) -> None: ...


def _between(column: str) -> str:
    return f"{column} >= {{start:DateTime64(3, 'UTC')}} AND {column} < {{end:DateTime64(3, 'UTC')}}"


_ENV = "ResourceAttributes['deployment.environment.name'] = {environment:String}"
_SPANS = f"{_between('Timestamp')} AND {_ENV}"


@dataclass(frozen=True)
class Table:
    name: str  # in the recording and in the database it is loaded into
    database: str  # on the stack
    where: str  # the rows that belong to a recording
    # Export order. Rows that look alike next to each other compress better:
    # sorting spans by trace takes a run's spans from 6.5 to 4.8 MB.
    order: str
    final: bool = False

    @property
    def source(self) -> str:
        return f"{self.database}.{self.name}"

    def select(self) -> str:
        return f"{self.source}{' FINAL' if self.final else ''} WHERE {self.where}"


TABLES = (
    Table("otel_traces", "otel", _SPANS, "TraceId, Timestamp, SpanId"),
    # Filled from otel_traces by a materialized view on the stack. A loaded
    # recording gets it as a plain table, since nothing writes to it.
    Table(
        "otel_traces_trace_id_ts",
        "otel",
        f"TraceId IN (SELECT TraceId FROM otel.otel_traces WHERE {_SPANS})",
        "TraceId, Start, End",
    ),
    Table("otel_logs", "otel", f"{_between('Timestamp')} AND {_ENV}", "ServiceName, Timestamp"),
    *(
        Table(
            f"otel_metrics_{kind}",
            "otel",
            f"{_between('TimeUnix')} AND {_ENV}",
            "ServiceName, MetricName, cityHash64(Attributes), TimeUnix",
        )
        for kind in ("gauge", "sum", "histogram", "exponential_histogram", "summary")
    ),
    # Read with FINAL like the changes API does, so a retried post is one row.
    Table(
        "changes",
        "incidentlens",
        f"{_between('time')} AND environment = {{environment:String}}",
        "time, id",
        final=True,
    ),
)


def expected_columns() -> dict[str, list[str]]:
    """The columns the engine is written against: the pinned Collector's exporter tables
    and IncidentLens's own. Regenerate with `incidentlens recording tables` after a
    Collector upgrade or a new deploy/clickhouse/init file."""
    text = resources.files("incidentlens.recording").joinpath("tables.json").read_text()
    return json.loads(text)


def columns_sha256(columns: Mapping[str, list[str]]) -> str:
    canonical = json.dumps({k: columns[k] for k in sorted(columns)}, separators=(",", ":"))
    return hashlib.sha256(canonical.encode()).hexdigest()


def column_differences(
    expected: Mapping[str, list[str]], actual: Mapping[str, list[str]]
) -> list[str]:
    problems = []
    for table in sorted(expected.keys() | actual.keys()):
        want, got = expected.get(table), actual.get(table)
        if want is None:
            problems.append(f"{table}: not expected")
        elif got is None:
            problems.append(f"{table}: missing")
        elif want != got:
            missing = [c for c in want if c not in got]
            extra = [c for c in got if c not in want]
            parts = [f"missing {', '.join(missing)}"] if missing else []
            parts += [f"unexpected {', '.join(extra)}"] if extra else []
            problems.append(f"{table}: {'; '.join(parts) or 'columns in a different order'}")
    return problems


def live_columns(server: Server) -> dict[str, list[str]]:
    """The recorded tables' columns on a server. Tables that do not exist are left out."""
    return {t.name: cols for t in TABLES if (cols := server.columns(t.database, t.name))}


def find_run(arg: str, runs_dir: Path | None) -> Path:
    """The manifest of a run given as a run directory, its manifest.json, or a run id."""
    path = Path(arg)
    if path.is_dir():
        path = path / "manifest.json"
    if not path.is_file() and runs_dir is not None and "/" not in arg:
        path = runs_dir / arg / "manifest.json"
    if not path.is_file():
        raise RecordingError(f"no run manifest at {arg}")
    return path


def _time(raw: str) -> datetime:
    return datetime.fromisoformat(raw).astimezone(UTC)


def _param(t: datetime) -> str:
    return t.astimezone(UTC).strftime("%Y-%m-%d %H:%M:%S.%f")[:-3]


def _overlaps(run_path: Path, start: datetime, end: datetime) -> list[str]:
    """Other runs next to this one whose steps fall between start and end."""
    found = []
    for other in sorted(run_path.parent.parent.glob("*/manifest.json")):
        if other.parent == run_path.parent:
            continue
        try:
            steps = json.loads(other.read_text())["steps"]
            first, last = _time(steps[0]["time"]), _time(steps[-1]["time"])
        except (OSError, ValueError, KeyError, IndexError):
            continue
        if first < end and last > start:
            found.append(f"{other.parent.name} ({iso(first)} to {iso(last)})")
    return found


def save(
    run_path: Path,
    server: Server,
    *,
    out: Path,
    margin: int = DEFAULT_MARGIN,
    collector: str | None = None,
    clock: Clock,
    log: Callable[[str], None] = lambda _: None,
) -> dict[str, Any]:
    """Export a run's window, plus `margin` seconds before it, to the directory `out`.

    Refuses before exporting anything when the run has no window, another run
    falls inside the margin, the live tables differ from the expected columns
    or `out` exists. Waits until SETTLE seconds after the window so late
    telemetry is included. The directory appears only once it is complete.
    """
    run = json.loads(run_path.read_text())
    if run.get("schema") != RUN_SCHEMA:
        raise RecordingError(f"{run_path}: not a {RUN_SCHEMA} manifest")
    if not run.get("window"):
        raise RecordingError(f"run {run['id']} has no window: it stopped before the fault started")
    if out.exists():
        raise RecordingError(f"{out} already exists")

    window_start, end = _time(run["window"]["start"]), _time(run["window"]["end"])
    start = window_start - timedelta(seconds=margin)
    others = _overlaps(run_path, start, end)
    if others:
        raise RecordingError(
            f"other runs fall inside {iso(start)} to {iso(end)}: {', '.join(others)}. "
            "Their faults would be part of this recording; pass a shorter --margin"
        )

    columns = live_columns(server)
    problems = column_differences(expected_columns(), columns)
    if problems:
        raise RecordingError(
            "the tables on this ClickHouse are not the ones the engine expects: "
            + "; ".join(problems)
            + ". After a Collector upgrade, update engine/src/incidentlens/recording/tables.json "
            "with incidentlens recording tables"
        )

    ready = end + timedelta(seconds=SETTLE)
    wait = (ready - clock.now()).total_seconds()
    if wait > 0:
        log(f"waiting {wait:.0f}s for telemetry from the end of the window")
        clock.sleep(wait)

    if run.get("status") != "completed":
        log(f"run status is {run.get('status')}: only completed runs are meant for scoring")
    params = {"start": _param(start), "end": _param(end), "environment": run["environment"]}
    doc: dict[str, Any] = {
        "schema": RECORDING_SCHEMA,
        "id": run["id"],
        "created": iso(clock.now()),
        "environment": run["environment"],
        "range": {"start": iso(start), "end": iso(end)},
        "margin": format_duration(margin),
        "source": {"clickhouse": server.version(), "collector": collector},
        "columns_sha256": columns_sha256(columns),
        "tables": {},
        "run": run,
    }

    out.parent.mkdir(parents=True, exist_ok=True)
    tmp = Path(tempfile.mkdtemp(dir=out.parent, prefix=f".{out.name}-"))
    try:
        for t in TABLES:
            file = f"{t.name}.native.zst"
            rows = server.export(
                f"SELECT * FROM {t.select()} ORDER BY {t.order}", params, tmp / file
            )
            n, checksum = server.checksum(t.select(), params)
            if n != rows:
                raise CheckFailed(
                    f"{t.source}: {rows} rows exported but {n} counted right after; "
                    "is telemetry for this window still arriving?"
                )
            doc["tables"][t.name] = {
                "source": t.source,
                "file": file,
                "rows": rows,
                "checksum": checksum,
                "bytes": (tmp / file).stat().st_size,
                "sha256": _sha256(tmp / file),
                "columns": columns[t.name],
                "create": server.create_statement(t.database, t.name),
            }
            log(f"{t.source}: {rows} rows, {doc['tables'][t.name]['bytes']} bytes")
        _write_json(tmp / MANIFEST, doc)
        tmp.chmod(0o755)  # mkdtemp creates it 0700
        os.rename(tmp, out)
    except BaseException:
        shutil.rmtree(tmp, ignore_errors=True)
        raise
    if doc["tables"]["otel_traces"]["rows"] == 0:
        log("no spans in the recording: was the stack sending telemetry?")
    return doc


def database_name(recording_id: str) -> str:
    return "rec_" + re.sub(r"[^a-z0-9]+", "_", recording_id.lower()).strip("_")[:59]


def read(path: Path) -> tuple[Path, dict[str, Any]]:
    """A recording's directory and recording.json, checked against what this engine reads."""
    if path.is_file():
        path = path.parent
    try:
        doc = json.loads((path / MANIFEST).read_text())
    except (OSError, ValueError) as e:
        raise RecordingError(f"{path / MANIFEST}: {e}") from e
    if doc.get("schema") != RECORDING_SCHEMA:
        raise RecordingError(f"{path}: not a {RECORDING_SCHEMA} recording")
    problems = column_differences(
        expected_columns(), {k: v["columns"] for k, v in doc["tables"].items()}
    )
    if problems:
        raise RecordingError(
            f"{path} was recorded from different tables than this engine reads ("
            + "; ".join(problems)
            + "). Load it with the commit it was made from, or record the run again"
        )
    for name, t in doc["tables"].items():
        if not t["create"].startswith(_create_head(t)):
            raise RecordingError(f"{name}: unexpected CREATE statement in {path / MANIFEST}")
    return path, doc


def _create_head(t: Mapping[str, Any]) -> str:
    return f"CREATE TABLE {t['source']}\n"


def load(
    path: Path,
    server: Server,
    *,
    database: str | None = None,
    replace: bool = False,
    log: Callable[[str], None] = lambda _: None,
) -> str:
    """Load a recording into its own database and return the database name.

    The tables are created from the statements saved with the recording,
    without their TTL, since recorded data is older than the stack keeps it.
    After loading, each table must hold the recorded number of rows and the
    same checksum, or the database is dropped and CheckFailed raised.
    """
    path, doc = read(path)
    db = database or database_name(doc["id"])
    if not _DATABASE.match(db) or db in RESERVED:
        raise RecordingError(f"cannot load into a database named {db!r}")
    for t in doc["tables"].values():
        if _sha256(path / t["file"]) != t["sha256"]:
            raise RecordingError(f"{path / t['file']} does not match its sha256 in {MANIFEST}")
    if server.has_database(db):
        if not replace:
            raise RecordingError(f"database {db} exists; pass --replace to drop and reload it")
        server.execute(f"DROP DATABASE {db} SYNC")

    server.execute(f"CREATE DATABASE {db}")
    try:
        for name, t in doc["tables"].items():
            head = _create_head(t)
            server.execute(f"CREATE TABLE {db}.{name}\n" + t["create"][len(head) :])
            if "\nTTL " in t["create"]:
                server.execute(f"ALTER TABLE {db}.{name} REMOVE TTL")
            if t["rows"]:
                server.insert(f"{db}.{name}", path / t["file"])
            got = server.checksum(f"{db}.{name}")
            if got != (t["rows"], t["checksum"]):
                raise CheckFailed(
                    f"{db}.{name} holds {got[0]} rows (checksum {got[1]}) after loading, "
                    f"recorded {t['rows']} (checksum {t['checksum']})"
                )
            log(f"{db}.{name}: {t['rows']} rows")
    except BaseException:
        server.execute(f"DROP DATABASE IF EXISTS {db} SYNC")
        raise
    return db


def _sha256(path: Path) -> str:
    h = hashlib.sha256()
    with open(path, "rb") as f:
        while chunk := f.read(1 << 20):
            h.update(chunk)
    return h.hexdigest()


def _write_json(path: Path, doc: Mapping[str, Any]) -> None:
    with open(path, "w") as f:
        json.dump(doc, f, indent=2)
        f.write("\n")
