"""Command line entry point for incidentlens."""

import argparse
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
            "Run a scenario on the stack started by make up: baseline flags, warmup, fault, "
            "change events, hold, then clear every flag. Writes runs/<id>-<time>/manifest.json. "
            "Environment: CHANGES_URL (default http://127.0.0.1:8090), CHANGES_TOKEN, "
            "FLAGD_OFREP_URL (default http://127.0.0.1:8016)."
        ),
    )
    run.add_argument("id", help="scenario id, the file name in bench/scenarios/ without .yaml")
    run.add_argument("--warmup", metavar="DURATION", help="override the warmup (90s, 5m)")
    run.add_argument("--duration", metavar="DURATION", help="override the fault duration")
    return parser


def main(argv: Sequence[str] | None = None) -> int:
    parser = build_parser()
    args = parser.parse_args(argv)
    if args.command == "scenario":
        return scenario_run(parser, args)
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
