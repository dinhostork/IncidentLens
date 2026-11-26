"""Entry point: telemetry, connections, schema and seed, then serve."""

import logging
import os
from contextlib import asynccontextmanager

import uvicorn
from fastapi import FastAPI
from opentelemetry.instrumentation.fastapi import FastAPIInstrumentor

from catalog import seed, telemetry
from catalog.app import create_app
from catalog.clients import Postgres, Redis
from catalog.fault import Hook, Nop
from catalog.store import CachedStore

log = logging.getLogger("catalog")


def build(hook: Hook) -> FastAPI:
    dsn = os.environ["DATABASE_URL"]
    redis_url = os.environ.get("REDIS_URL", "redis://redis:6379/0")

    @asynccontextmanager
    async def lifespan(app: FastAPI):
        pg = await Postgres.connect(dsn, hook)
        cache = Redis(redis_url, hook)
        await pg.execute(seed.SCHEMA)
        await pg.executemany(seed.INSERT, seed.products())
        app.state.store = CachedStore(pg, cache)
        log.info("catalog ready")
        try:
            yield
        finally:
            await cache.close()
            await pg.close()

    app = create_app(hook, lifespan=lifespan)
    # Leave out the per-message ASGI "http send/receive" spans: they add a span
    # per response chunk and say nothing about the call graph.
    FastAPIInstrumentor.instrument_app(
        app, excluded_urls="healthz", exclude_spans=["receive", "send"]
    )
    return app


def main() -> None:
    shutdown = telemetry.setup()
    # Fault injection plugs in here. Nop until the flag-driven hook exists.
    hook: Hook = Nop()
    try:
        uvicorn.run(
            build(hook),
            host="0.0.0.0",
            port=int(os.environ.get("PORT", "8080")),
            log_config=None,
            access_log=False,
        )
    finally:
        shutdown()


if __name__ == "__main__":
    main()
