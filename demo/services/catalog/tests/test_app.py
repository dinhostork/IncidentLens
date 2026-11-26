from typing import Any

import pytest
from fastapi.testclient import TestClient

from catalog.app import create_app
from catalog.fault import Kind, Nop, Point


class FakeStore:
    def __init__(self, products: list[dict[str, Any]], fail: bool = False):
        self.products = products
        self.fail = fail

    async def list_products(self):
        if self.fail:
            raise ConnectionRefusedError("postgres down")
        return self.products

    async def get_product(self, product_id: int):
        if self.fail:
            raise ConnectionRefusedError("postgres down")
        return next((p for p in self.products if p["id"] == product_id), None)


PRODUCTS = [{"id": 1, "name": "Basic kettle", "category": "kitchen", "price_cents": 1873}]


def client(store, hook=None) -> TestClient:
    app = create_app(hook or Nop())
    app.state.store = store
    return TestClient(app, raise_server_exceptions=False)


def test_list_and_get():
    c = client(FakeStore(PRODUCTS))
    assert c.get("/products").json() == {"products": PRODUCTS}
    assert c.get("/products/1").json() == PRODUCTS[0]


def test_unknown_product_is_404():
    r = client(FakeStore(PRODUCTS)).get("/products/99")
    assert r.status_code == 404
    assert "99" in r.json()["error"]


@pytest.mark.parametrize("pid", ["abc", "0", "-1", str(2**63)])
def test_invalid_id_is_422(pid):
    assert client(FakeStore(PRODUCTS)).get(f"/products/{pid}").status_code == 422


@pytest.mark.parametrize("path", ["/products", "/products/1"])
def test_store_failure_is_500(path):
    r = client(FakeStore(PRODUCTS, fail=True)).get(path)
    assert r.status_code == 500
    assert "postgres" not in r.text, "internal error details leaked into the response"


def test_healthz():
    assert client(FakeStore([])).get("/healthz").status_code == 204


class FailingHook:
    def __init__(self):
        self.seen: list[Point] = []

    async def before(self, point: Point) -> None:
        self.seen.append(point)
        raise TimeoutError("timed out")


def test_inbound_hook_runs_with_route():
    hook = FailingHook()
    c = client(FakeStore(PRODUCTS), hook)
    assert c.get("/products/1").status_code == 500
    assert hook.seen == [Point(Kind.INBOUND, operation="GET /products/{product_id}")]
    assert c.get("/healthz").status_code == 204
    assert len(hook.seen) == 1
