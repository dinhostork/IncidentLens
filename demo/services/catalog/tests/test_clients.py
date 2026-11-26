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
