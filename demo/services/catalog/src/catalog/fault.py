"""Where fault injection plugs into catalog.

Every inbound request and every outbound call (Postgres, Redis) awaits
``Hook.before`` first. A hook can delay the call by sleeping or fail it by
raising. Outbound hooks run inside the client span, so injected latency and
errors show up on catalog's side of the edge, as they would for a real slow or
failing dependency.

Injected failures should look like real ones (a timeout, a refused
connection), so the engine cannot tell them apart by error type or message.
"""

from dataclasses import dataclass
from enum import StrEnum
from typing import Protocol


class Kind(StrEnum):
    INBOUND = "inbound"
    OUTBOUND = "outbound"


@dataclass(frozen=True)
class Point:
    kind: Kind
    dependency: str = ""
    """Called dependency ("postgres", "redis") for outbound calls, empty for inbound."""
    operation: str = ""
    """HTTP route or database operation, for context."""


class Hook(Protocol):
    async def before(self, point: Point) -> None: ...


class Nop:
    """Hook that never injects anything."""

    async def before(self, point: Point) -> None:
        return None
