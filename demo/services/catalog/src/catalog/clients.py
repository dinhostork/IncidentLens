"""Traced Postgres and Redis access.

Client spans are created here rather than by the asyncpg and redis
instrumentations so that the fault hook runs inside the span, and so the
attributes match the Go services: db.system.name, db.namespace,
server.address. A Postgres span starts before a connection is taken from the
pool, so pool waits count as time spent calling Postgres.
"""

import asyncio
import os
from collections.abc import Awaitable, Callable, Iterable
from contextlib import asynccontextmanager
from typing import Any
from urllib.parse import urlsplit

import asyncpg
import redis.asyncio as redis
from opentelemetry import metrics, trace
from opentelemetry.metrics import CallbackOptions, Observation
from opentelemetry.semconv.attributes import db_attributes as db
from opentelemetry.semconv.attributes import error_attributes, server_attributes
from opentelemetry.trace import SpanKind, Status, StatusCode

from catalog.fault import Hook, Kind, Point

tracer = trace.get_tracer("catalog.clients")
meter = metrics.get_meter("catalog.clients")

POSTGRES = "postgres"
REDIS = "redis"


def _error_type(exc: BaseException) -> str:
    if isinstance(exc, asyncpg.PostgresError) and getattr(exc, "sqlstate", None):
        return exc.sqlstate
    if isinstance(exc, TimeoutError):
        return "timeout"
    return type(exc).__qualname__


@asynccontextmanager
async def _client_span(name: str, attrs: dict[str, Any], hook: Hook, point: Point):
    with tracer.start_as_current_span(
        name,
        kind=SpanKind.CLIENT,
        attributes=attrs,
        record_exception=False,
        set_status_on_exception=False,
    ) as span:
        try:
            await hook.before(point)
            yield span
        except asyncio.CancelledError:
            # Not an Exception subclass. Record it like the Go side does, then
            # let the cancellation continue.
            span.set_attribute(error_attributes.ERROR_TYPE, "canceled")
            span.set_status(Status(StatusCode.ERROR, "canceled"))
            raise
        except Exception as exc:
            span.set_attribute(error_attributes.ERROR_TYPE, _error_type(exc))
            if isinstance(exc, asyncpg.PostgresError) and getattr(exc, "sqlstate", None):
                span.set_attribute(db.DB_RESPONSE_STATUS_CODE, exc.sqlstate)
            span.set_status(Status(StatusCode.ERROR, str(exc)))
            raise


def _summarize(query: str) -> tuple[str, str]:
    words = query.split()
    if not words:
        return "", ""
    op = words[0].upper()
    after = {"SELECT": "FROM", "DELETE": "FROM", "INSERT": "INTO", "CREATE": "TABLE"}.get(op)
    if op == "UPDATE" and len(words) > 1:
        return op, words[1]
    if after:
        upper = [w.upper() for w in words]
        if after in upper:
            i = upper.index(after) + 1
            while i < len(words) and upper[i] in ("IF", "NOT", "EXISTS"):
                i += 1
            if i < len(words):
                return op, words[i].split("(")[0].rstrip(",;")
    return op, ""


DEFAULT_POOL_MAX = 10


def pool_max() -> int:
    raw = os.environ.get("DB_POOL_MAX", "")
    if not raw:
        return DEFAULT_POOL_MAX
    if not raw.isdigit() or int(raw) < 1:
        raise ValueError(f"DB_POOL_MAX: want a positive integer, got {raw!r}")
    return int(raw)


class Postgres:
    """asyncpg pool wrapper that traces each statement."""

    def __init__(self, pool: asyncpg.Pool, dsn: str, hook: Hook):
        self._pool = pool
        self._hook = hook
        u = urlsplit(dsn)
        self._attrs = {
            db.DB_SYSTEM_NAME: "postgresql",
            db.DB_NAMESPACE: u.path.lstrip("/"),
            server_attributes.SERVER_ADDRESS: u.hostname or "",
            server_attributes.SERVER_PORT: u.port or 5432,
        }
        pool_name = f"{u.hostname}/{u.path.lstrip('/')}"
        meter.create_observable_up_down_counter(
            "db.client.connection.count",
            callbacks=[self._observe_count(pool_name)],
            unit="{connection}",
            description="Connections in the pool, by state.",
        )
        meter.create_observable_up_down_counter(
            "db.client.connection.max",
            callbacks=[
                lambda _: [
                    Observation(
                        self._pool.get_max_size(), {"db.client.connection.pool.name": pool_name}
                    )
                ]
            ],
            unit="{connection}",
            description="Maximum number of connections the pool allows.",
        )

    @classmethod
    async def connect(cls, dsn: str, hook: Hook) -> "Postgres":
        """Open a pool sized by DB_POOL_MAX (default 10), like the Go services."""
        max_size = pool_max()
        pool = await asyncpg.create_pool(dsn, min_size=min(2, max_size), max_size=max_size)
        return cls(pool, dsn, hook)

    def _observe_count(self, pool_name: str):
        def cb(_: CallbackOptions) -> Iterable[Observation]:
            size, idle = self._pool.get_size(), self._pool.get_idle_size()
            base = {"db.client.connection.pool.name": pool_name}
            return [
                Observation(idle, {**base, "db.client.connection.state": "idle"}),
                Observation(size - idle, {**base, "db.client.connection.state": "used"}),
            ]

        return cb

    async def _run(self, query: str, call: Callable[[asyncpg.Connection], Awaitable[Any]]) -> Any:
        op, coll = _summarize(query)
        attrs = {**self._attrs, db.DB_QUERY_TEXT: query}
        if op:
            attrs[db.DB_OPERATION_NAME] = op
        if coll:
            attrs[db.DB_COLLECTION_NAME] = coll
        name = f"{op} {coll}".strip() or "postgresql"
        point = Point(Kind.OUTBOUND, POSTGRES, op)
        async with _client_span(name, attrs, self._hook, point):
            async with self._pool.acquire() as conn:
                return await call(conn)

    async def fetch(self, query: str, *args: Any) -> list[asyncpg.Record]:
        return await self._run(query, lambda c: c.fetch(query, *args))

    async def fetchrow(self, query: str, *args: Any) -> asyncpg.Record | None:
        return await self._run(query, lambda c: c.fetchrow(query, *args))

    async def execute(self, query: str, *args: Any) -> str:
        return await self._run(query, lambda c: c.execute(query, *args))

    async def executemany(self, query: str, args: Iterable[tuple]) -> None:
        await self._run(query, lambda c: c.executemany(query, args))

    async def close(self) -> None:
        await self._pool.close()


class Redis:
    """redis-py asyncio client wrapper that traces each command."""

    def __init__(self, url: str, hook: Hook, timeout: float = 0.5):
        u = urlsplit(url)
        self._client = redis.Redis.from_url(
            url, socket_timeout=timeout, socket_connect_timeout=timeout
        )
        self._hook = hook
        self._attrs = {
            db.DB_SYSTEM_NAME: "redis",
            db.DB_NAMESPACE: (u.path.lstrip("/") or "0"),
            server_attributes.SERVER_ADDRESS: u.hostname or "",
            server_attributes.SERVER_PORT: u.port or 6379,
        }

    async def _run(self, command: str, key: str, call: Callable[[], Awaitable[Any]]) -> Any:
        attrs = {**self._attrs, db.DB_OPERATION_NAME: command, db.DB_QUERY_TEXT: f"{command} {key}"}
        async with _client_span(command, attrs, self._hook, Point(Kind.OUTBOUND, REDIS, command)):
            return await call()

    async def get(self, key: str) -> bytes | None:
        return await self._run("GET", key, lambda: self._client.get(key))

    async def set(self, key: str, value: str, ttl_seconds: int) -> None:
        await self._run("SET", key, lambda: self._client.set(key, value, ex=ttl_seconds))

    async def close(self) -> None:
        await self._client.aclose()
