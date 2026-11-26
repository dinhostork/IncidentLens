"""Command line entry point for incidentlens."""

import argparse
from collections.abc import Sequence

from incidentlens import __version__


def build_parser() -> argparse.ArgumentParser:
    parser = argparse.ArgumentParser(
        prog="incidentlens",
        description="Reconstruct production incidents from telemetry and change events.",
    )
    parser.add_argument("--version", action="version", version=f"%(prog)s {__version__}")
    return parser


def main(argv: Sequence[str] | None = None) -> int:
    build_parser().parse_args(argv)
    print(f"incidentlens {__version__}")
    return 0
