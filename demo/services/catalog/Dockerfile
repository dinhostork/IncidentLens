# Build context is demo/, like the Go services.
FROM ghcr.io/astral-sh/uv:0.12.13 AS uv

FROM python:3.12.16-slim-trixie
COPY --from=uv /uv /usr/local/bin/uv
ENV UV_COMPILE_BYTECODE=1 UV_LINK_MODE=copy UV_PYTHON_DOWNLOADS=never PYTHONUNBUFFERED=1
WORKDIR /app
COPY services/catalog/pyproject.toml services/catalog/uv.lock services/catalog/.python-version ./
RUN uv sync --locked --no-dev --no-install-project
COPY services/catalog/src src
RUN uv sync --locked --no-dev
USER nobody
EXPOSE 8080
ENTRYPOINT ["/app/.venv/bin/catalog"]
