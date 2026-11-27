"""Product lookups: Redis first, Postgres on a miss (cache-aside).

A failing cache is not fatal. Reads fall through to Postgres and a warning is
logged, which is how a Redis problem turns into extra load on Postgres. When
the cache is turned off (the ``catalog.cache_disabled`` fault) every read goes
to Postgres and Redis is not called at all.
"""

import json
import logging
from collections.abc import Callable
from typing import Any, Protocol

from catalog.clients import Postgres, Redis

log = logging.getLogger(__name__)

LIST_KEY = "catalog:products"
LIST_TTL = 30
ITEM_TTL = 60


def item_key(product_id: int) -> str:
    return f"catalog:product:{product_id}"


class Store(Protocol):
    async def list_products(self) -> list[dict[str, Any]]: ...
    async def get_product(self, product_id: int) -> dict[str, Any] | None: ...


def _row(r: Any) -> dict[str, Any]:
    return {
        "id": r["id"],
        "name": r["name"],
        "category": r["category"],
        "price_cents": r["price_cents"],
    }


class CachedStore:
    def __init__(
        self, pg: Postgres, cache: Redis, cache_disabled: Callable[[], bool] = lambda: False
    ):
        self._pg = pg
        self._cache = cache
        self._disabled = cache_disabled

    async def _cache_get(self, key: str) -> Any | None:
        if self._disabled():
            return None
        try:
            raw = await self._cache.get(key)
        except Exception as exc:
            log.warning(
                "cache read failed, falling back to postgres",
                extra={"error.type": type(exc).__name__},
            )
            return None
        return json.loads(raw) if raw is not None else None

    async def _cache_set(self, key: str, value: Any, ttl: int) -> None:
        if self._disabled():
            return
        try:
            await self._cache.set(key, json.dumps(value), ttl)
        except Exception as exc:
            log.warning("cache write failed", extra={"error.type": type(exc).__name__})

    async def list_products(self) -> list[dict[str, Any]]:
        if (cached := await self._cache_get(LIST_KEY)) is not None:
            return cached
        rows = await self._pg.fetch(
            "SELECT id, name, category, price_cents FROM products ORDER BY id"
        )
        products = [_row(r) for r in rows]
        await self._cache_set(LIST_KEY, products, LIST_TTL)
        return products

    async def get_product(self, product_id: int) -> dict[str, Any] | None:
        key = item_key(product_id)
        if (cached := await self._cache_get(key)) is not None:
            return cached
        r = await self._pg.fetchrow(
            "SELECT id, name, category, price_cents FROM products WHERE id = $1", product_id
        )
        if r is None:
            return None
        product = _row(r)
        await self._cache_set(key, product, ITEM_TTL)
        return product
