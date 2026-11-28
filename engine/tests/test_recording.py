import json
import re
from collections.abc import Mapping
from datetime import UTC, datetime, timedelta
from pathlib import Path
from typing import Any

import pytest

from incidentlens.cli import main
from incidentlens.recording import recording as rec
from incidentlens.recording.recording import CheckFailed, RecordingError

START = datetime(2026, 12, 1, 14, 0, 0, tzinfo=UTC)
END = START + timedelta(minutes=15)


def iso(t: datetime) -> str:
    return t.isoformat(timespec="milliseconds").replace("+00:00", "Z")


class Clock:
    def __init__(self, t: datetime) -> None:
        self.t = t
        self.slept: list[float] = []

    def now(self) -> datetime:
        return self.t

    def sleep(self, seconds: float) -> None:
        self.slept.append(seconds)
        self.t += timedelta(seconds=seconds)


class Server:
    """ClickHouse with one fake table per recorded source, holding a row count."""

    def __init__(self) -> None:
        self.columns_by_table = rec.expected_columns()
        self.rows = {t.source: 10 * (i + 1) for i, t in enumerate(rec.TABLES)}
        self.rows["otel.otel_metrics_summary"] = 0
        self.databases = {"otel", "incidentlens"}
        self.loaded: dict[str, str] = {}  # db.table -> source it came from
        self.statements: list[str] = []
        self.exports: list[tuple[str, Mapping[str, str] | None]] = []
        self.count_drift = 0  # rows added between the export and the count

    def version(self) -> str:
        return "26.8.48.11"

    def columns(self, database: str, table: str) -> list[str]:
        return list(self.columns_by_table.get(table, []))

    def create_statement(self, database: str, table: str) -> str:
        return f"CREATE TABLE {database}.{table}\n(\n    `x` String\n)\nENGINE = MergeTree\nTTL x"

    def _checksum(self, source: str) -> tuple[int, str]:
        n = self.rows[source]
        return n, str(n * 7919)

    def checksum(self, source: str, params: Mapping[str, str] | None = None) -> tuple[int, str]:
        name = source.split()[0]
        if name in self.loaded:
            return self._checksum(self.loaded[name])
        if name.split(".")[0] not in ("otel", "incidentlens"):
            return 0, "0"
        n, h = self._checksum(name)
        return n + self.count_drift, h

    def export(self, sql: str, params: Mapping[str, str] | None, path: Path) -> int:
        self.exports.append((sql, params))
        source = re.search(r"FROM (\w+\.\w+)", sql).group(1)  # type: ignore[union-attr]
        n = self.rows[source]
        path.write_text(f"{source}\n" if n else "")
        return n

    def has_database(self, name: str) -> bool:
        return name in self.databases

    def execute(self, sql: str) -> None:
        self.statements.append(sql)
        if m := re.match(r"CREATE DATABASE (\w+)$", sql):
            self.databases.add(m.group(1))
        elif m := re.match(r"DROP DATABASE (?:IF EXISTS )?(\w+)", sql):
            self.databases.discard(m.group(1))
            self.loaded = {k: v for k, v in self.loaded.items() if not k.startswith(m.group(1))}

    def insert(self, table: str, path: Path) -> None:
        self.loaded[table] = path.read_text().strip()


def write_run(runs: Path, run_id: str, **fields: Any) -> Path:
    doc = {
        "schema": "incidentlens.run/v0",
        "id": run_id,
        "status": "completed",
        "environment": "demo",
        "window": {"start": iso(START), "end": iso(END)},
        "steps": [
            {"step": "started", "time": iso(START - timedelta(seconds=2))},
            {"step": "flags_off", "time": iso(END + timedelta(seconds=1))},
        ],
        "expected": {"root_cause": {"service": "payments"}},
    }
    doc.update(fields)
    path = runs / run_id / "manifest.json"
    path.parent.mkdir(parents=True)
    path.write_text(json.dumps(doc))
    return path


@pytest.fixture
def run(tmp_path: Path) -> Path:
    return write_run(tmp_path / "runs", "payments-db-latency-20261201T135958Z")


def save(run: Path, server: Server, clock: Clock | None = None, **kw: Any) -> dict:
    out = run.parent.parent.parent / "recordings" / run.parent.name
    return rec.save(run, server, out=out, clock=clock or Clock(END + timedelta(hours=1)), **kw)


def saved(run: Path) -> Path:
    return run.parent.parent.parent / "recordings" / run.parent.name


def test_save_writes_every_table_and_the_manifest(run: Path) -> None:
    server = Server()
    doc = save(run, server, collector="otel/opentelemetry-collector-contrib:0.165.0")
    out = saved(run)

    assert json.loads((out / "recording.json").read_text()) == doc
    assert doc["schema"] == "incidentlens.recording/v0"
    assert doc["id"] == run.parent.name
    assert doc["range"] == {"start": "2026-12-01T13:50:00.000Z", "end": iso(END)}
    assert doc["margin"] == "10m"
    assert doc["source"] == {
        "clickhouse": "26.8.48.11",
        "collector": "otel/opentelemetry-collector-contrib:0.165.0",
    }
    assert doc["run"] == json.loads(run.read_text())
    assert doc["columns_sha256"] == rec.columns_sha256(rec.expected_columns())
    assert list(doc["tables"]) == [t.name for t in rec.TABLES]
    traces = doc["tables"]["otel_traces"]
    assert traces["rows"] == 10
    assert traces["checksum"] == str(10 * 7919)
    assert traces["source"] == "otel.otel_traces"
    assert traces["columns"] == rec.expected_columns()["otel_traces"]
    assert traces["create"].startswith("CREATE TABLE otel.otel_traces\n")
    assert traces["bytes"] == (out / "otel_traces.native.zst").stat().st_size
    assert doc["tables"]["otel_metrics_summary"]["bytes"] == 0
    assert sorted(p.name for p in out.iterdir()) == sorted(
        ["recording.json", *(f"{t.name}.native.zst" for t in rec.TABLES)]
    )
    assert not [p for p in out.parent.iterdir() if p.name.startswith(".")]


def test_save_filters_by_range_and_environment(run: Path) -> None:
    server = Server()
    save(run, server, margin=120)
    sql = dict((re.search(r"FROM (\w+\.\w+)", s).group(1), s) for s, _ in server.exports)  # type: ignore[union-attr]
    params = {p["start"] for _, p in server.exports if p}
    assert params == {"2026-12-01 13:58:00.000"}
    assert {p["end"] for _, p in server.exports if p} == {"2026-12-01 14:15:00.000"}
    assert {p["environment"] for _, p in server.exports if p} == {"demo"}
    assert "Timestamp >= {start:DateTime64(3, 'UTC')}" in sql["otel.otel_traces"]
    assert "ResourceAttributes['deployment.environment.name']" in sql["otel.otel_traces"]
    assert "TimeUnix < {end:DateTime64(3, 'UTC')}" in sql["otel.otel_metrics_sum"]
    assert "TraceId IN (SELECT TraceId FROM otel.otel_traces" in sql["otel.otel_traces_trace_id_ts"]
    assert sql["incidentlens.changes"].startswith("SELECT * FROM incidentlens.changes FINAL WHERE")
    assert sql["incidentlens.changes"].endswith(" ORDER BY time, id")
    assert sql["otel.otel_traces"].endswith(" ORDER BY TraceId, Timestamp, SpanId")
    assert "environment = {environment:String}" in sql["incidentlens.changes"]


def test_save_waits_for_late_telemetry(run: Path) -> None:
    clock = Clock(END + timedelta(seconds=5))
    doc = save(run, Server(), clock)
    assert clock.slept == [25]
    assert doc["created"] == iso(END + timedelta(seconds=30))


def test_save_refuses_a_run_without_a_window(tmp_path: Path) -> None:
    run = write_run(tmp_path / "runs", "r", window=None, status="interrupted")
    with pytest.raises(RecordingError, match="no window"):
        save(run, Server())
    assert not saved(run).exists()


def test_save_refuses_another_run_inside_the_margin(run: Path) -> None:
    other = write_run(run.parent.parent, "catalog-cpu-burn-20261201T133000Z")
    doc = json.loads(other.read_text())
    doc["steps"][-1]["time"] = iso(START - timedelta(minutes=5))
    other.write_text(json.dumps(doc))
    server = Server()
    with pytest.raises(RecordingError, match="catalog-cpu-burn-20261201T133000Z"):
        save(run, server)
    assert server.exports == []
    save(run, server, margin=240)


def test_save_refuses_tables_the_engine_does_not_expect(run: Path) -> None:
    server = Server()
    server.columns_by_table["otel_traces"].append("NewColumn String")
    del server.columns_by_table["otel_metrics_summary"]
    with pytest.raises(RecordingError) as e:
        save(run, server)
    assert "otel_traces: unexpected NewColumn String" in str(e.value)
    assert "otel_metrics_summary: missing" in str(e.value)
    assert server.exports == []
    assert not saved(run).parent.exists()


def test_save_refuses_an_existing_recording(run: Path) -> None:
    saved(run).mkdir(parents=True)
    with pytest.raises(RecordingError, match="already exists"):
        save(run, Server())


def test_save_leaves_nothing_when_rows_keep_arriving(run: Path) -> None:
    server = Server()
    server.count_drift = 1
    with pytest.raises(CheckFailed, match="still arriving"):
        save(run, server)
    assert list(saved(run).parent.iterdir()) == []


@pytest.fixture
def recording(run: Path) -> Path:
    save(run, Server())
    return saved(run)


def test_load_creates_the_tables_without_ttl_and_checks_them(recording: Path) -> None:
    server = Server()
    db = rec.load(recording, server)
    assert db == "rec_payments_db_latency_20261201t135958z"
    assert server.statements[0] == f"CREATE DATABASE {db}"
    assert server.statements[1].startswith(f"CREATE TABLE {db}.otel_traces\n(\n")
    assert server.statements[2] == f"ALTER TABLE {db}.otel_traces REMOVE TTL"
    assert server.loaded[f"{db}.otel_traces"] == "otel.otel_traces"
    assert server.loaded[f"{db}.changes"] == "incidentlens.changes"
    # Empty tables are created but nothing is inserted.
    assert f"{db}.otel_metrics_summary" not in server.loaded
    assert (
        f"CREATE TABLE {db}.otel_metrics_summary\n(\n    `x` String\n)\nENGINE = MergeTree\nTTL x"
        in (server.statements)
    )


def test_load_refuses_an_existing_database(recording: Path) -> None:
    server = Server()
    server.databases.add("mine")
    with pytest.raises(RecordingError, match="--replace"):
        rec.load(recording, server, database="mine")
    assert server.statements == []
    assert rec.load(recording, server, database="mine", replace=True) == "mine"
    assert server.statements[:2] == ["DROP DATABASE mine SYNC", "CREATE DATABASE mine"]


@pytest.mark.parametrize("name", ["otel", "incidentlens", "system", "Bad-Name", "x; DROP"])
def test_load_refuses_some_database_names(recording: Path, name: str) -> None:
    with pytest.raises(RecordingError, match="cannot load into"):
        rec.load(recording, Server(), database=name)


def test_load_refuses_a_changed_file(recording: Path) -> None:
    (recording / "otel_logs.native.zst").write_text("something else\n")
    server = Server()
    with pytest.raises(RecordingError, match="otel_logs.native.zst does not match its sha256"):
        rec.load(recording, server)
    assert server.statements == []


def test_load_refuses_a_recording_of_other_tables(recording: Path) -> None:
    path = recording / "recording.json"
    doc = json.loads(path.read_text())
    doc["tables"]["otel_logs"]["columns"].remove("EventName String")
    path.write_text(json.dumps(doc))
    with pytest.raises(RecordingError, match="otel_logs: missing EventName String"):
        rec.load(recording, Server())


def test_load_refuses_another_format(recording: Path) -> None:
    path = recording / "recording.json"
    doc = json.loads(path.read_text())
    doc["schema"] = "incidentlens.recording/v1"
    path.write_text(json.dumps(doc))
    with pytest.raises(RecordingError, match="not a incidentlens.recording/v0 recording"):
        rec.load(path, Server())


def test_load_drops_the_database_when_a_table_does_not_match(recording: Path) -> None:
    path = recording / "recording.json"
    doc = json.loads(path.read_text())
    doc["tables"]["otel_logs"]["checksum"] = "1"
    path.write_text(json.dumps(doc))
    server = Server()
    with pytest.raises(CheckFailed, match="otel_logs holds 30 rows"):
        rec.load(recording, server)
    db = rec.database_name(doc["id"])
    assert server.statements[-1] == f"DROP DATABASE IF EXISTS {db} SYNC"
    assert db not in server.databases


def test_column_differences() -> None:
    want = {"a": ["x Int8", "y String"], "b": ["z String"]}
    assert rec.column_differences(want, want) == []
    assert rec.column_differences(want, {"a": ["y String", "x Int8"], "c": []}) == [
        "a: columns in a different order",
        "b: missing",
        "c: not expected",
    ]
    assert rec.column_differences(want, {"a": ["x Int16", "y String"], "b": ["z String"]}) == [
        "a: missing x Int8; unexpected x Int16"
    ]


def test_expected_columns_cover_every_table() -> None:
    assert set(rec.expected_columns()) == {t.name for t in rec.TABLES}


def test_find_run(run: Path) -> None:
    runs = run.parent.parent
    assert rec.find_run(str(run), runs) == run
    assert rec.find_run(str(run.parent), runs) == run
    assert rec.find_run(run.parent.name, runs) == run
    with pytest.raises(RecordingError, match="no run manifest at nope"):
        rec.find_run("nope", runs)


def test_cli_load_missing_recording(tmp_path: Path, capsys: pytest.CaptureFixture[str]) -> None:
    assert main(["recording", "load", str(tmp_path / "nope")]) == 2
    assert "recording.json" in capsys.readouterr().err
