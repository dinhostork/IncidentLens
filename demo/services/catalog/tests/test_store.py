import asyncio
import json

from catalog.store import LIST_KEY, CachedStore, item_key


class FakePG:
    def __init__(self, rows):
        self.rows = rows
        self.queries = 0

    async def fetch(self, query, *args):
        self.queries += 1
        return self.rows

    async def fetchrow(self, query, *args):
        self.queries += 1
        return next((r for r in self.rows if r["id"] == args[0]), None)


class FakeRedis:
    def __init__(self, broken=False):
        self.data = {}
        self.broken = broken

    async def get(self, key):
        if self.broken:
            raise ConnectionError("redis down")
        return self.data.get(key)

    async def set(self, key, value, ttl_seconds):
        if self.broken:
            raise ConnectionError("redis down")
        self.data[key] = value


ROWS = [{"id": 3, "name": "Basic knife", "category": "kitchen", "price_cents": 4619}]


def test_cache_aside():
    pg, cache = FakePG(ROWS), FakeRedis()
    store = CachedStore(pg, cache)

    assert asyncio.run(store.get_product(3)) == ROWS[0]
    assert asyncio.run(store.get_product(3)) == ROWS[0]
    assert pg.queries == 1
    assert json.loads(cache.data[item_key(3)]) == ROWS[0]

    assert asyncio.run(store.list_products()) == ROWS
    assert LIST_KEY in cache.data


def test_missing_product_not_cached():
    pg, cache = FakePG(ROWS), FakeRedis()
    assert asyncio.run(CachedStore(pg, cache).get_product(42)) is None
    assert cache.data == {}


def test_broken_cache_falls_through_to_postgres(caplog):
    pg = FakePG(ROWS)
    store = CachedStore(pg, FakeRedis(broken=True))
    assert asyncio.run(store.get_product(3)) == ROWS[0]
    assert asyncio.run(store.get_product(3)) == ROWS[0]
    assert pg.queries == 2
    assert any("falling back to postgres" in r.message for r in caplog.records)
