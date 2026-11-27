# /// script
# requires-python = ">=3.12"
# dependencies = []
# ///
"""Turn demo faults on and off.

flagd serves deploy/flags/active.json. This script keeps that file in step
with deploy/flags/flags.json, which defines every fault flag and has them all
off, and writes it atomically so flagd never reads half a file. The services
pick up a change within a second or two.

    uv run demo/faults.py list
    uv run demo/faults.py set payments.latency_ms.postgres=800 catalog.cache_disabled=true
    uv run demo/faults.py clear payments.latency_ms.postgres
    uv run demo/faults.py clear
    uv run demo/faults.py init

Flag keys are <service>.<fault>[.<dependency>]. See demo/README.md for the list.
"""

import argparse
import json
import os
import sys
import tempfile
from pathlib import Path

FLAGS_DIR = Path(__file__).resolve().parent.parent / "deploy" / "flags"
BASE = FLAGS_DIR / "flags.json"
ACTIVE = FLAGS_DIR / "active.json"


def parse_value(flag: str, raw: str) -> int | float | bool:
    """Parse raw for flag, checked against what its fault accepts."""
    fault = flag.split(".")[1]
    if fault == "cache_disabled":
        if raw not in ("true", "false"):
            raise ValueError(f"{flag}: want true or false, got {raw!r}")
        return raw == "true"
    if fault == "error_rate":
        try:
            rate = float(raw)
        except ValueError:
            rate = -1.0
        if not 0 <= rate <= 1:
            raise ValueError(f"{flag}: want a number from 0 to 1, got {raw!r}")
        return rate
    if not raw.isdigit():
        raise ValueError(f"{flag}: want a whole number >= 0, got {raw!r}")
    return int(raw)


def load(path: Path) -> dict:
    with path.open() as f:
        return json.load(f)


def current(base: dict) -> dict[str, object]:
    """Values set in active.json that are not off, for flags that still exist."""
    if not ACTIVE.exists():
        return {}
    flags = load(ACTIVE).get("flags", {})
    values = {}
    for k, f in flags.items():
        if k not in base["flags"]:
            continue
        v = f.get("variants", {}).get(f.get("defaultVariant"))
        if v is not None and v != off_value(base, k):
            values[k] = v
    return values


def off_value(base: dict, flag: str) -> object:
    return base["flags"][flag]["variants"]["off"]


def write(base: dict, values: dict[str, object]) -> None:
    """Write active.json: base with each flag in values switched to an "on" variant."""
    out = json.loads(json.dumps(base))
    for k, v in values.items():
        f = out["flags"][k]
        f["variants"]["on"] = v
        f["defaultVariant"] = "on"
    fd, tmp = tempfile.mkstemp(dir=FLAGS_DIR, prefix=".active-", suffix=".json")
    try:
        with os.fdopen(fd, "w") as f:
            json.dump(out, f, indent=2)
            f.write("\n")
        # flagd runs as another user in its container.
        os.chmod(tmp, 0o644)
        os.replace(tmp, ACTIVE)
    except BaseException:
        os.unlink(tmp)
        raise


def main(argv: list[str]) -> int:
    p = argparse.ArgumentParser(description="Turn demo faults on and off.")
    sub = p.add_subparsers(dest="cmd", required=True)
    sub.add_parser("list", help="show the faults that are on")
    s = sub.add_parser("set", help="turn faults on: flag=value ...")
    s.add_argument("assignments", nargs="+", metavar="flag=value")
    c = sub.add_parser("clear", help="turn faults off, all of them if no flag is given")
    c.add_argument("flags", nargs="*", metavar="flag")
    sub.add_parser("init", help="create or refresh active.json from flags.json")
    args = p.parse_args(argv)

    base = load(BASE)
    values = current(base)

    def check(flag: str) -> None:
        if flag not in base["flags"]:
            raise ValueError(f"unknown flag {flag!r}, see deploy/flags/flags.json")

    try:
        if args.cmd == "list":
            if not values:
                print("no faults on")
            for k in sorted(values):
                print(f"{k}={json.dumps(values[k])}")
            return 0
        if args.cmd == "set":
            for a in args.assignments:
                flag, sep, raw = a.partition("=")
                if not sep:
                    raise ValueError(f"want flag=value, got {a!r}")
                check(flag)
                v = parse_value(flag, raw)
                if v == off_value(base, flag):
                    values.pop(flag, None)
                else:
                    values[flag] = v
        elif args.cmd == "clear":
            for flag in args.flags:
                check(flag)
                values.pop(flag, None)
            if not args.flags:
                values = {}
    except ValueError as e:
        print(f"faults: {e}", file=sys.stderr)
        return 2
    # init, set and clear all rewrite the file, which also drops flags that
    # flags.json no longer has and adds new ones.
    write(base, values)
    return 0


if __name__ == "__main__":
    sys.exit(main(sys.argv[1:]))
