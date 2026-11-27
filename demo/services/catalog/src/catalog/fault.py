"""Where fault injection plugs into catalog.

Every inbound request and every outbound call (Postgres, Redis) awaits
``Hook.before`` first. A hook can delay the call by sleeping or fail it by
raising. Outbound hooks run inside the client span, so injected latency and
errors show up on catalog's side of the edge, as they would for a real slow or
failing dependency.

``Injector`` is the hook driven by flags in flagd, the same flags and the same
behavior as the Go services (``demo/internal/fault``). Injected failures should
look like real ones (a timeout, a dropped connection), so the engine cannot
tell them apart by error type or message.
"""

import asyncio
import logging
import random
import sys
import time
from collections.abc import Callable
from dataclasses import dataclass
from enum import StrEnum
from typing import Protocol

import asyncpg
import redis.exceptions


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
    address: str = ""
    """Dependency's host:port for outbound calls, used in injected error messages."""


class Hook(Protocol):
    async def before(self, point: Point) -> None: ...

    def pool_size(self) -> int:
        """Postgres connections catalog may use at once, 0 for the configured pool."""
        ...

    def cache_disabled(self) -> bool: ...


class Nop:
    """Hook that never injects anything."""

    async def before(self, point: Point) -> None:
        return None

    def pool_size(self) -> int:
        return 0

    def cache_disabled(self) -> bool:
        return False


# Fault names, the middle part of a flag key "<service>.<fault>[.<dependency>]".
LATENCY_MS = "latency_ms"
ERROR_RATE = "error_rate"
POOL_SIZE = "pool_size"
CPU_BURN_MS = "cpu_burn_ms"
CACHE_DISABLED = "cache_disabled"


def key(service: str, fault: str, dependency: str = "") -> str:
    return f"{service}.{fault}.{dependency}" if dependency else f"{service}.{fault}"


class Flags(Protocol):
    """Reads fault flags. Off, unknown or unreadable flags read as zero/False."""

    def get_int(self, key: str) -> int: ...
    def get_float(self, key: str) -> float: ...
    def get_bool(self, key: str) -> bool: ...


class Injector:
    """Hook that applies the faults its flags turn on, read on every call.

    Inbound: burn CPU for ``cpu_burn_ms``, then fail ``error_rate`` of requests.
    Outbound: wait ``latency_ms.<dependency>``, then fail
    ``error_rate.<dependency>`` of calls.
    """

    def __init__(self, service: str, flags: Flags, rand: Callable[[], float] = random.random):
        self._service = service
        self._flags = flags
        self._rand = rand

    def _key(self, fault: str, dependency: str = "") -> str:
        return key(self._service, fault, dependency)

    def _hit(self, flag: str) -> bool:
        rate = self._flags.get_float(flag)
        return rate > 0 and self._rand() < rate

    async def before(self, point: Point) -> None:
        if point.kind == Kind.INBOUND:
            _burn_cpu(self._flags.get_int(self._key(CPU_BURN_MS)))
            if self._hit(self._key(ERROR_RATE)):
                raise _handler_error()
            return
        ms = self._flags.get_int(self._key(LATENCY_MS, point.dependency))
        if ms > 0:
            await asyncio.sleep(ms / 1000)
        if self._hit(self._key(ERROR_RATE, point.dependency)):
            raise _connection_error(point)

    def pool_size(self) -> int:
        return max(self._flags.get_int(self._key(POOL_SIZE)), 0)

    def cache_disabled(self) -> bool:
        return self._flags.get_bool(self._key(CACHE_DISABLED))


def _burn_cpu(ms: int) -> None:
    """Spin for ms milliseconds. This blocks the event loop, as CPU-bound work
    in an async handler would, so every request in flight waits too."""
    if ms <= 0:
        return
    end = time.perf_counter() + ms / 1000
    while time.perf_counter() < end:
        pass


def _handler_error() -> Exception:
    # A typical bug in a Python handler.
    return AttributeError("'NoneType' object has no attribute 'get'")


def _connection_error(point: Point) -> Exception:
    """What the client library raises when the server drops the connection."""
    if point.dependency == "postgres":
        return asyncpg.ConnectionDoesNotExistError(
            "connection was closed in the middle of operation"
        )
    if point.dependency == "redis":
        return redis.exceptions.ConnectionError(
            f"Error while reading from {point.address} : (104, 'Connection reset by peer')"
        )
    return ConnectionResetError(104, "Connection reset by peer")


class OpenFeatureFlags:
    """Flags read through the OpenFeature client. Errors fall back to the default."""

    def __init__(self, client):
        self._client = client

    def get_int(self, key: str) -> int:
        return self._client.get_integer_value(key, 0)

    def get_float(self, key: str) -> float:
        return self._client.get_float_value(key, 0.0)

    def get_bool(self, key: str) -> bool:
        return self._client.get_boolean_value(key, False)


def connect_flagd() -> tuple[Flags, Callable[[], None]]:
    """Set flagd as the OpenFeature provider and return flags read from it.

    The provider runs in-process: it syncs the flag definitions from flagd
    (FLAGD_HOST, port 8015) and evaluates locally, so an evaluation never
    blocks the event loop on the network. Changes arrive over the sync stream
    within a second. Until the first sync every flag reads as off, and if flagd
    goes away the last synced values stay in effect.

    The provider's loggers write to stderr only. The root logger exports to
    OpenTelemetry, and nothing about fault injection should reach catalog's
    telemetry.
    """
    from openfeature import api
    from openfeature.contrib.provider.flagd import FlagdProvider
    from openfeature.contrib.provider.flagd.config import ResolverType

    of_log = logging.getLogger("openfeature")
    of_log.propagate = False
    of_log.setLevel(logging.WARNING)
    handler = logging.StreamHandler(sys.stderr)
    handler.setFormatter(logging.Formatter("%(asctime)s %(levelname)s %(name)s %(message)s"))
    of_log.addHandler(handler)

    api.set_provider(FlagdProvider(resolver_type=ResolverType.IN_PROCESS))
    return OpenFeatureFlags(api.get_client()), api.shutdown
