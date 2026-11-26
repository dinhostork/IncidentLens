"""HTTP API: GET /products, GET /products/{id}, GET /healthz.

The store is read from ``app.state.store``, set by the lifespan in main once
connections are open (or directly by tests).
"""

import logging
from typing import Annotated, Any

from fastapi import APIRouter, Depends, FastAPI, HTTPException, Path, Request
from fastapi.responses import JSONResponse, Response

from catalog.fault import Hook, Kind, Point
from catalog.store import Store

log = logging.getLogger(__name__)

MAX_BIGINT = 2**63 - 1


def _store(request: Request) -> Store:
    return request.app.state.store


StoreDep = Annotated[Store, Depends(_store)]


def create_app(hook: Hook, lifespan: Any = None) -> FastAPI:
    async def inbound_fault(request: Request) -> None:
        route = request.scope.get("route")
        op = f"{request.method} {route.path}" if route else request.method
        try:
            await hook.before(Point(Kind.INBOUND, operation=op))
        except Exception as exc:
            raise _failed("handler failed", exc) from exc

    api = APIRouter(dependencies=[Depends(inbound_fault)])

    @api.get("/products")
    async def list_products(store: StoreDep) -> dict[str, Any]:
        try:
            return {"products": await store.list_products()}
        except Exception as exc:
            raise _failed("product lookup failed", exc) from exc

    @api.get("/products/{product_id}")
    async def get_product(
        product_id: Annotated[int, Path(ge=1, le=MAX_BIGINT)], store: StoreDep
    ) -> dict[str, Any]:
        # The bounds keep out-of-range IDs at 422: unbounded, they reach
        # asyncpg and fail inside the Postgres span as a database error.
        try:
            product = await store.get_product(product_id)
        except Exception as exc:
            raise _failed("product lookup failed", exc) from exc
        if product is None:
            raise HTTPException(status_code=404, detail=f"product {product_id} not found")
        return product

    app = FastAPI(title="catalog", lifespan=lifespan, openapi_url=None)
    app.include_router(api)

    @app.get("/healthz", include_in_schema=False)
    async def healthz() -> Response:
        return Response(status_code=204)

    @app.exception_handler(HTTPException)
    async def http_error(_: Request, exc: HTTPException) -> JSONResponse:
        return JSONResponse({"error": exc.detail}, status_code=exc.status_code)

    return app


def _failed(msg: str, exc: Exception) -> HTTPException:
    """Log a failed request and turn it into a 500 with a generic body.

    ``msg`` is the log record body and names the failure without variable
    parts, so log templates can tell failures apart. The exception goes into
    the record's exception attributes, not into the response.
    """
    log.error(msg, exc_info=exc)
    return HTTPException(status_code=500, detail="Internal Server Error")
