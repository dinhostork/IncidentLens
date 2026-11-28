"""The few ClickHouse calls a recording needs, over the HTTP interface.

No driver: queries go in the request body or the URL, values as query
parameters ({name:Type} in the SQL, param_<name> in the URL), and table data
moves as zstd-compressed Native, which ClickHouse compresses and decompresses
itself (Accept-Encoding and Content-Encoding).
"""

import json
import os
import urllib.error
import urllib.parse
import urllib.request
from collections.abc import Mapping
from pathlib import Path

CHUNK = 1 << 20


class ClickHouseError(Exception):
    """ClickHouse answered with an error, or could not be reached."""


class ClickHouse:
    def __init__(
        self, url: str, user: str = "", password: str = "", *, timeout: float = 300
    ) -> None:
        self.url = url.rstrip("/") + "/"
        self._headers = {}
        if user:
            self._headers["X-ClickHouse-User"] = user
            self._headers["X-ClickHouse-Key"] = password
        self._timeout = timeout

    @classmethod
    def from_env(cls) -> "ClickHouse":
        """CLICKHOUSE_URL, CLICKHOUSE_USER and CLICKHOUSE_PASSWORD, defaulting to the stack's."""
        return cls(
            os.environ.get("CLICKHOUSE_URL", "http://127.0.0.1:8123"),
            os.environ.get("CLICKHOUSE_USER", "incidentlens"),
            os.environ.get("CLICKHOUSE_PASSWORD", "incidentlens"),
        )

    def _open(
        self,
        settings: Mapping[str, str],
        body: object,
        headers: Mapping[str, str] | None = None,
    ):
        # wait_end_of_query: an error halfway through a result becomes an HTTP
        # error instead of a truncated body with status 200.
        query = urllib.parse.urlencode({"wait_end_of_query": "1", **settings})
        req = urllib.request.Request(
            self.url + "?" + query,
            data=body,  # type: ignore[arg-type]
            headers={**self._headers, **(headers or {})},
            method="POST",
        )
        try:
            return urllib.request.urlopen(req, timeout=self._timeout)
        except urllib.error.HTTPError as e:
            msg = e.read().decode(errors="replace").strip()
            raise ClickHouseError(f"ClickHouse answered {e.code}: {msg}") from e
        except OSError as e:
            raise ClickHouseError(f"ClickHouse at {self.url}: {e}") from e

    @staticmethod
    def _params(params: Mapping[str, str] | None) -> dict[str, str]:
        return {f"param_{k}": v for k, v in (params or {}).items()}

    def query(self, sql: str, params: Mapping[str, str] | None = None) -> str:
        """Run a statement and return its output as text."""
        with self._open(self._params(params), sql.encode()) as resp:
            return resp.read().decode()

    def rows(self, sql: str, params: Mapping[str, str] | None = None) -> list[dict]:
        """Run a SELECT and return its rows as dicts. Numbers come back as numbers."""
        out = self.query(sql + "\nFORMAT JSONEachRow", params)
        return [json.loads(line) for line in out.splitlines() if line]

    def execute(self, sql: str) -> None:
        self.query(sql)

    def version(self) -> str:
        return self.query("SELECT version()").strip()

    def columns(self, database: str, table: str) -> list[str]:
        """Columns in table order as `name type`, with MATERIALIZED, ALIAS or DEFAULT appended."""
        rows = self.rows(
            "SELECT name, type, default_kind FROM system.columns"
            " WHERE database = {db:String} AND table = {table:String} ORDER BY position",
            {"db": database, "table": table},
        )
        return [" ".join(x for x in (r["name"], r["type"], r["default_kind"]) if x) for r in rows]

    def create_statement(self, database: str, table: str) -> str:
        return self.query(f"SHOW CREATE TABLE {database}.{table} FORMAT TSVRaw").strip()

    def has_database(self, name: str) -> bool:
        rows = self.rows(
            "SELECT 1 FROM system.databases WHERE name = {name:String}", {"name": name}
        )
        return bool(rows)

    def checksum(self, source: str, params: Mapping[str, str] | None = None) -> tuple[int, str]:
        """Row count and a hash of every exported column for `SELECT * FROM <source>`.

        sum() of a per-row hash does not depend on row order, so the same rows
        give the same value wherever they are stored.
        """
        (row,) = self.rows(
            f"SELECT count() AS n, toString(sum(cityHash64(*))) AS h FROM {source}", params
        )
        return int(row["n"]), row["h"]

    def export(self, sql: str, params: Mapping[str, str] | None, path: Path) -> int:
        """Write the result of a SELECT to path as zstd-compressed Native. Returns the row count.

        An empty result is an empty file: ClickHouse sends no body for it, compressed or not.
        """
        settings = {**self._params(params), "enable_http_compression": "1"}
        with self._open(
            settings, (sql + "\nFORMAT Native").encode(), {"Accept-Encoding": "zstd"}
        ) as resp:
            summary = json.loads(resp.headers.get("X-ClickHouse-Summary") or "{}")
            if "result_rows" not in summary:
                raise ClickHouseError("ClickHouse did not report how many rows it exported")
            rows = int(summary["result_rows"])
            if rows and resp.headers.get("Content-Encoding") != "zstd":
                raise ClickHouseError("ClickHouse did not compress the export with zstd")
            with open(path, "wb") as f:
                while chunk := resp.read(CHUNK):
                    f.write(chunk)
        if not rows and path.stat().st_size:
            raise ClickHouseError("ClickHouse sent data for an empty result")
        return rows

    def insert(self, table: str, path: Path) -> None:
        """Insert a zstd-compressed Native file into table (database.name)."""
        size = path.stat().st_size
        with open(path, "rb") as f:
            with self._open(
                {"query": f"INSERT INTO {table} FORMAT Native"},
                f,
                {"Content-Encoding": "zstd", "Content-Length": str(size)},
            ) as resp:
                resp.read()
