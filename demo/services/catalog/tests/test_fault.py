import asyncio
import time

import asyncpg
import pytest
import redis.exceptions

from catalog import clients
from catalog.fault import Injector, Kind, Nop, Point, key
from catalog.store import CachedStore


class MapFlags:
    def __init__(self, **values):
        # Flag keys have dots, so take them as catalog__latency_ms__redis.
        self.values = {k.replace("__", "."): v for k, v in values.items()}
        self.read: list[str] = []

    def _get(self, k, default):
        self.read.append(k)
        return self.values.get(k, default)

    def get_int(self, k):
        return self._get(k, 0)

    def get_float(self, k):
        return self._get(k, 0.0)

    def get_bool(self, k):
        return self._get(k, False)


PG = Point(Kind.OUTBOUND, "postgres", "SELECT", "postgres:5432")
REDIS = Point(Kind.OUTBOUND, "redis", "GET", "redis:6379")
INBOUND = Point(Kind.INBOUND, operation="GET /products")


def test_key():
    assert key("catalog", "latency_ms", "redis") == "catalog.latency_ms.redis"
    assert key("catalog", "cache_disabled") == "catalog.cache_disabled"


def test_no_flags_no_faults():
    flags = MapFlags()
    hook = Injector("catalog", flags)
    asyncio.run(hook.before(INBOUND))
    asyncio.run(hook.before(PG))
    assert hook.pool_size() == 0
    assert hook.cache_disabled() is False
    assert flags.read == [
        "catalog.cpu_burn_ms",
        "catalog.error_rate",
        "catalog.latency_ms.postgres",
        "catalog.error_rate.postgres",
        "catalog.pool_size",
        "catalog.cache_disabled",
    ]


def test_outbound_latency():
    hook = Injector("catalog", MapFlags(catalog__latency_ms__postgres=50))
    start = time.perf_counter()
    asyncio.run(hook.before(PG))
    assert time.perf_counter() - start >= 0.05
    start = time.perf_counter()
    asyncio.run(hook.before(REDIS))
    assert time.perf_counter() - start < 0.02


def test_outbound_errors_look_like_client_errors():
    flags = MapFlags(catalog__error_rate__postgres=0.3, catalog__error_rate__redis=1.0)
    asyncio.run(Injector("catalog", flags, rand=lambda: 0.5).before(PG))

    with pytest.raises(asyncpg.ConnectionDoesNotExistError):
        asyncio.run(Injector("catalog", flags, rand=lambda: 0.1).before(PG))
    with pytest.raises(redis.exceptions.ConnectionError, match="redis:6379"):
        asyncio.run(Injector("catalog", flags, rand=lambda: 0.99).before(REDIS))


def test_inbound_faults():
    flags = MapFlags(catalog__cpu_burn_ms=30, catalog__error_rate=1.0)
    start = time.perf_counter()
    with pytest.raises(AttributeError):
        asyncio.run(Injector("catalog", flags, rand=lambda: 0.99).before(INBOUND))
    assert time.perf_counter() - start >= 0.03

    # A dependency's error rate does not fail inbound requests.
    flags = MapFlags(catalog__error_rate__postgres=1.0)
    asyncio.run(Injector("catalog", flags).before(INBOUND))


def test_pool_size_and_cache_flags():
    hook = Injector("catalog", MapFlags(catalog__pool_size=2, catalog__cache_disabled=True))
    assert hook.pool_size() == 2
    assert hook.cache_disabled() is True
    assert Injector("catalog", MapFlags(catalog__pool_size=-3)).pool_size() == 0


def test_limiter_caps_and_follows_changes():
    cap = {"n": 1}
    limiter = clients._Limiter(lambda: cap["n"])
    order: list[str] = []

    async def hold(name, seconds):
        async with limiter.slot():
            order.append(f"{name} in")
            await asyncio.sleep(seconds)
            order.append(f"{name} out")

    async def main():
        await asyncio.gather(hold("a", 0.03), hold("b", 0))
        cap["n"] = 0
        start = time.perf_counter()
        await asyncio.gather(hold("c", 0.03), hold("d", 0.03))
        return time.perf_counter() - start

    uncapped = asyncio.run(main())
    assert order[:4] == ["a in", "a out", "b in", "b out"]
    assert uncapped < 0.055, "with no cap both holders should run at once"


def test_redis_latency_hits_socket_timeout():
    hook = clients._SocketTimeout(
        Injector("catalog", MapFlags(catalog__latency_ms__redis=1000)), 0.05, "redis:6379"
    )
    start = time.perf_counter()
    with pytest.raises(redis.exceptions.TimeoutError, match="Timeout reading from redis:6379"):
        asyncio.run(hook.before(REDIS))
    assert time.perf_counter() - start < 0.5

    # Shorter than the timeout: just slow.
    hook = clients._SocketTimeout(
        Injector("catalog", MapFlags(catalog__latency_ms__redis=10)), 0.05, "redis:6379"
    )
    asyncio.run(hook.before(REDIS))


class CountingRedis:
    def __init__(self):
        self.calls = 0

    async def get(self, k):
        self.calls += 1

    async def set(self, k, v, ttl_seconds):
        self.calls += 1


class OnePG:
    def __init__(self):
        self.queries = 0

    async def fetchrow(self, query, *args):
        self.queries += 1
        return {"id": 1, "name": "x", "category": "y", "price_cents": 1}


def test_cache_disabled_skips_redis():
    pg, cache = OnePG(), CountingRedis()
    store = CachedStore(pg, cache, cache_disabled=lambda: True)
    asyncio.run(store.get_product(1))
    asyncio.run(store.get_product(1))
    assert cache.calls == 0
    assert pg.queries == 2


def test_nop():
    hook = Nop()
    asyncio.run(hook.before(PG))
    assert hook.pool_size() == 0
    assert hook.cache_disabled() is False
