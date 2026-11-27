"""Check scenario files against the schema and against the demo's fault flags.

    uv run validate.py                  # every file in scenarios/
    uv run validate.py scenarios/x.yaml

The JSON Schema covers the shape of a file. The rest needs the demo: flag
keys must exist in deploy/flags/flags.json, values must suit the fault, and
service names must be ones the demo has. Exits 1 if any file has a problem.
"""

import json
import sys
from pathlib import Path

import yaml
from jsonschema import Draft202012Validator

BENCH = Path(__file__).resolve().parent
SCHEMA = BENCH / "schema" / "scenario.schema.json"
SCENARIOS = BENCH / "scenarios"
FLAGS = BENCH.parent / "deploy" / "flags" / "flags.json"


class _Loader(yaml.SafeLoader):
    """SafeLoader that rejects duplicate keys instead of keeping the last one."""


def _mapping(loader: _Loader, node: yaml.MappingNode, deep: bool = False) -> dict:
    seen = set()
    for key_node, _ in node.value:
        key = loader.construct_object(key_node, deep=deep)
        if key in seen:
            raise yaml.constructor.ConstructorError(
                None, None, f"duplicate key {key!r}", key_node.start_mark
            )
        seen.add(key)
    return loader.construct_mapping(node, deep=deep)


_Loader.add_constructor(yaml.resolver.BaseResolver.DEFAULT_MAPPING_TAG, _mapping)


def load_flags(path: Path = FLAGS) -> dict[str, object]:
    """Flag key to its off value."""
    with path.open() as f:
        flags = json.load(f)["flags"]
    return {k: v["variants"]["off"] for k, v in flags.items()}


def value_problem(flag: str, value: object, off: object) -> str | None:
    """Why value cannot be set on flag, or None. Mirrors demo/faults.py."""
    fault = flag.split(".")[1]
    if fault == "cache_disabled":
        if not isinstance(value, bool):
            return "want true or false"
    elif fault == "error_rate":
        if isinstance(value, bool) or not isinstance(value, int | float) or not 0 <= value <= 1:
            return "want a number from 0 to 1"
    elif isinstance(value, bool) or not isinstance(value, int) or value < 0:
        return "want a whole number >= 0"
    if value == off:
        return "same as off, so the flag does nothing"
    return None


class Checker:
    def __init__(self, schema_path: Path = SCHEMA, flags_path: Path = FLAGS) -> None:
        with schema_path.open() as f:
            self.schema = Draft202012Validator(json.load(f))
        self.flags = load_flags(flags_path)
        self.services = {k.split(".")[0] for k in self.flags}
        deps = {k.split(".")[2] for k in self.flags if k.count(".") == 2}
        self.dependencies = self.services | deps

    def check_file(self, path: Path) -> list[str]:
        try:
            with path.open() as f:
                doc = yaml.load(f, Loader=_Loader)  # a SafeLoader, see above
        except (OSError, yaml.YAMLError) as e:
            return [str(e)]
        return self.check(doc, path.stem)

    def check(self, doc: object, name: str) -> list[str]:
        problems = [
            f"{_where(e.absolute_path)}: {e.message}"
            for e in sorted(self.schema.iter_errors(doc), key=lambda e: list(e.absolute_path))
        ]
        if problems:
            # The checks below assume the shape the schema guarantees.
            return problems

        if doc["id"] != name:
            problems.append(f"id: {doc['id']!r} does not match the file name {name!r}")

        fault = doc["fault"]
        for where, flags in (
            ("baseline.flags", doc.get("baseline", {}).get("flags", {})),
            ("fault.flags", fault.get("flags", {})),
        ):
            for flag, value in flags.items():
                if flag not in self.flags:
                    problems.append(f"{where}.{flag}: unknown flag, see deploy/flags/flags.json")
                elif p := value_problem(flag, value, self.flags[flag]):
                    problems.append(f"{where}.{flag}: {p}, got {value!r}")

        deploy = fault.get("deploy")
        if deploy and deploy["service"] not in self.services:
            problems.append(f"fault.deploy.service: unknown service {deploy['service']!r}")

        cause = doc["expected"]["root_cause"]
        if cause["service"] not in self.services:
            problems.append(f"expected.root_cause.service: unknown service {cause['service']!r}")
        touched = {k.split(".")[0] for k in fault.get("flags", {})}
        if deploy:
            touched.add(deploy["service"])
        if cause["service"] not in touched:
            problems.append(
                "expected.root_cause.service: the fault does not touch "
                f"{cause['service']!r} (it touches {', '.join(sorted(touched))})"
            )
        if "dependency" in cause and cause["dependency"] not in self.dependencies:
            problems.append(
                f"expected.root_cause.dependency: unknown dependency {cause['dependency']!r}"
            )

        for i, s in enumerate(doc["expected"]["symptoms"]):
            if s["service"] not in self.services:
                problems.append(f"expected.symptoms.{i}.service: unknown service {s['service']!r}")
        return problems


def _where(path) -> str:
    return ".".join(str(p) for p in path) or "(top level)"


def main(argv: list[str]) -> int:
    paths = [Path(a) for a in argv] or sorted(SCENARIOS.glob("*.yaml"))
    if not paths:
        print(f"no scenarios in {SCENARIOS}", file=sys.stderr)
        return 1
    checker = Checker()
    bad = 0
    for path in paths:
        problems = checker.check_file(path)
        if problems:
            bad += 1
        for p in problems:
            print(f"{path}: {p}", file=sys.stderr)
    print(f"{len(paths) - bad} of {len(paths)} scenarios valid")
    return 1 if bad else 0


if __name__ == "__main__":
    sys.exit(main(sys.argv[1:]))
