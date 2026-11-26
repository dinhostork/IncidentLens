import pytest

from catalog.clients import _summarize
from catalog.seed import INSERT, SCHEMA, products


@pytest.mark.parametrize(
    ("query", "op", "coll"),
    [
        ("SELECT id, name FROM products WHERE id = $1", "SELECT", "products"),
        (INSERT, "INSERT", "products"),
        (SCHEMA, "CREATE", "products"),
        ("UPDATE products SET name = $1", "UPDATE", "products"),
        ("SELECT 1", "SELECT", ""),
        ("", "", ""),
    ],
)
def test_summarize(query, op, coll):
    assert _summarize(query) == (op, coll)


def test_seed_products():
    rows = products()
    assert len(rows) == 48
    assert [r[0] for r in rows] == list(range(1, 49))
    assert all(r[3] > 0 for r in rows)
    assert len({r[1] for r in rows}) == len(rows)


def test_pool_max(monkeypatch):
    from catalog.clients import DEFAULT_POOL_MAX, pool_max

    monkeypatch.delenv("DB_POOL_MAX", raising=False)
    assert pool_max() == DEFAULT_POOL_MAX
    monkeypatch.setenv("DB_POOL_MAX", "3")
    assert pool_max() == 3
    for bad in ("0", "-2", "ten"):
        monkeypatch.setenv("DB_POOL_MAX", bad)
        with pytest.raises(ValueError):
            pool_max()


def test_cancelled_call_marks_span():
    import asyncio

    from opentelemetry.sdk.trace import TracerProvider
    from opentelemetry.sdk.trace.export import SimpleSpanProcessor
    from opentelemetry.sdk.trace.export.in_memory_span_exporter import InMemorySpanExporter
    from opentelemetry.trace import StatusCode

    from catalog import clients
    from catalog.fault import Kind, Nop, Point

    exporter = InMemorySpanExporter()
    provider = TracerProvider()
    provider.add_span_processor(SimpleSpanProcessor(exporter))
    tracer = provider.get_tracer("test")

    async def run():
        async with clients._client_span("GET", {}, Nop(), Point(Kind.OUTBOUND, "redis")):
            await asyncio.sleep(10)

    async def main():
        task = asyncio.create_task(run())
        await asyncio.sleep(0)
        task.cancel()
        with pytest.raises(asyncio.CancelledError):
            await task

    original = clients.tracer
    clients.tracer = tracer
    try:
        asyncio.run(main())
    finally:
        clients.tracer = original

    (span,) = exporter.get_finished_spans()
    assert span.status.status_code == StatusCode.ERROR
    assert span.attributes["error.type"] == "canceled"
