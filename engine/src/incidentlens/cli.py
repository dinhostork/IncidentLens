"""Command line entry point for incidentlens."""

import argparse
import json
import os
import signal
import sys
from collections.abc import Sequence
from datetime import datetime
from pathlib import Path

from incidentlens import __version__


def build_parser() -> argparse.ArgumentParser:
    parser = argparse.ArgumentParser(
        prog="incidentlens",
        description="Reconstruct production incidents from telemetry and change events.",
    )
    parser.add_argument("--version", action="version", version=f"%(prog)s {__version__}")
    commands = parser.add_subparsers(dest="command", metavar="command")

    scenario = commands.add_parser("scenario", help="benchmark scenarios on the local stack")
    scenario_commands = scenario.add_subparsers(dest="scenario_command", metavar="command")
    scenario_commands.required = True
    run = scenario_commands.add_parser(
        "run",
        help="run a scenario from bench/scenarios/ and write its manifest under runs/",
        description=(
            "Run a scenario on the stack started by make up: baseline flags, warmup, fault "
            "(and deploy), change events, hold, then clear every flag and put back the old "
            "version. Writes runs/<id>-<time>/manifest.json. "
            "Environment: CHANGES_URL (default http://127.0.0.1:8090), CHANGES_TOKEN, "
            "FLAGD_OFREP_URL (default http://127.0.0.1:8016)."
        ),
    )
    run.add_argument("id", help="scenario id, the file name in bench/scenarios/ without .yaml")
    run.add_argument("--warmup", metavar="DURATION", help="override the warmup (90s, 5m)")
    run.add_argument("--duration", metavar="DURATION", help="override the fault duration")

    recording = commands.add_parser(
        "recording", help="save scenario runs to files and load them into ClickHouse"
    )
    recording_commands = recording.add_subparsers(dest="recording_command", metavar="command")
    recording_commands.required = True
    clickhouse_env = (
        "Environment: CLICKHOUSE_URL (default http://127.0.0.1:8123), CLICKHOUSE_USER and "
        "CLICKHOUSE_PASSWORD (default incidentlens)."
    )
    save = recording_commands.add_parser(
        "save",
        help="export a run's window from ClickHouse to recordings/<run id>/",
        description=(
            "Export the telemetry and change events of a scenario run: its window plus a "
            "margin before it, filtered to the run's environment. " + clickhouse_env
        ),
    )
    save.add_argument("run", help="run id under runs/, or a run directory or manifest.json")
    save.add_argument(
        "--margin",
        metavar="DURATION",
        default="10m",
        help="time before the window to include (default 10m)",
    )
    save.add_argument("--out", metavar="DIR", help="where to write (default recordings/<run id>)")
    load = recording_commands.add_parser(
        "load",
        help="load a recording into its own ClickHouse database",
        description=(
            "Create a database with the recorded tables, insert the recording and check row "
            "counts and checksums. Prints the database name. " + clickhouse_env
        ),
    )
    load.add_argument("path", help="recording directory or its recording.json")
    load.add_argument("--database", help="database to create (default rec_<run id>)")
    load.add_argument("--replace", action="store_true", help="drop the database first if it exists")
    recording_commands.add_parser(
        "tables",
        help="print the recorded tables' columns on ClickHouse, in the format of tables.json",
        description=(
            "Print the columns of the tables a recording holds, as the ClickHouse in "
            "CLICKHOUSE_URL has them. Used to update engine/src/incidentlens/recording/"
            "tables.json after a Collector upgrade."
        ),
    )
    return parser


def main(argv: Sequence[str] | None = None) -> int:
    parser = build_parser()
    args = parser.parse_args(argv)
    if args.command == "scenario":
        return scenario_run(parser, args)
    if args.command == "recording":
        return recording(parser, args)
    print(f"incidentlens {__version__}")
    return 0


def _log(msg: str) -> None:
    print(f"{datetime.now():%H:%M:%S} {msg}", file=sys.stderr, flush=True)


def _sigterm(signum: int, frame: object) -> None:
    raise KeyboardInterrupt


def scenario_run(parser: argparse.ArgumentParser, args: argparse.Namespace) -> int:
    from incidentlens.scenario import demo, runner, spec

    overrides = {}
    for name in ("warmup", "duration"):
        raw = getattr(args, name)
        if raw is not None:
            try:
                overrides[name] = spec.parse_duration(raw)
            except ValueError as e:
                parser.error(f"--{name}: {e}")

    try:
        root = spec.find_root(Path.cwd())
        scenario = spec.load(root, args.id)
        faults = spec.load_script(root / "demo" / "faults.py", "incidentlens_demo_faults")
    except spec.ScenarioError as e:
        print(f"incidentlens: {e}", file=sys.stderr)
        return 2

    # docker stop, kill and CI cancellations send SIGTERM: clean up as for Ctrl-C.
    signal.signal(signal.SIGTERM, _sigterm)
    try:
        result = runner.run(
            scenario,
            flags=demo.DemoFlags(
                faults, os.environ.get("FLAGD_OFREP_URL", "http://127.0.0.1:8016")
            ),
            changes=demo.ChangesClient(
                os.environ.get("CHANGES_URL", "http://127.0.0.1:8090"),
                os.environ.get("CHANGES_TOKEN", ""),
            ),
            stack=demo.ComposeStack(root),
            clock=demo.SystemClock(),
            runs_dir=root / "runs",
            git=demo.git_info(root),
            overrides=overrides,
            log=_log,
        )
    except spec.ScenarioError as e:
        print(f"incidentlens: {e}", file=sys.stderr)
        return 2
    except runner.RunError as e:
        print(f"incidentlens: {e}", file=sys.stderr)
        return 1
    except KeyboardInterrupt:
        # Before the run started: nothing was changed.
        return 130
    if result.status == "interrupted":
        return 130
    print(result.run_dir / "manifest.json")
    return 0


def recording(parser: argparse.ArgumentParser, args: argparse.Namespace) -> int:
    from incidentlens.recording import recording as rec
    from incidentlens.recording.clickhouse import ClickHouse, ClickHouseError
    from incidentlens.scenario import demo, spec

    server = ClickHouse.from_env()
    try:
        root: Path | None = spec.find_root(Path.cwd())
    except spec.ScenarioError:
        root = None

    try:
        if args.recording_command == "tables":
            print(json.dumps(rec.live_columns(server), indent=2))
            return 0
        if args.recording_command == "load":
            print(
                rec.load(
                    Path(args.path), server, database=args.database, replace=args.replace, log=_log
                )
            )
            return 0

        try:
            margin = spec.parse_duration(args.margin)
        except ValueError as e:
            parser.error(f"--margin: {e}")
        manifest = rec.find_run(args.run, root / "runs" if root else None)
        run_id = json.loads(manifest.read_text()).get("id", manifest.parent.name)
        out = Path(args.out) if args.out else (root or Path.cwd()) / "recordings" / run_id
        collector = None
        if root is not None:
            try:
                collector = demo.ComposeStack(root).image("otel-collector")
            except spec.ScenarioError as e:
                _log(f"cannot tell the Collector version: {e}")
        rec.save(
            manifest,
            server,
            out=out,
            margin=margin,
            collector=collector,
            clock=demo.SystemClock(),
            log=_log,
        )
        print(out)
        return 0
    except rec.RecordingError as e:
        print(f"incidentlens: {e}", file=sys.stderr)
        return 2
    except (rec.CheckFailed, ClickHouseError) as e:
        print(f"incidentlens: {e}", file=sys.stderr)
        return 1
    except KeyboardInterrupt:
        return 130
