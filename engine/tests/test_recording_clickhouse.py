import json
import threading
import urllib.parse
from collections.abc import Iterator
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from pathlib import Path

import pytest

from incidentlens.recording.clickhouse import ClickHouse, ClickHouseError


class Fake:
    """Answers every request with the next prepared response and keeps what it received."""

    def __init__(self) -> None:
        self.requests: list[dict] = []
        self.responses: list[tuple[int, dict[str, str], bytes]] = []


@pytest.fixture
def fake() -> Iterator[tuple[Fake, ClickHouse]]:
    state = Fake()

    class Handler(BaseHTTPRequestHandler):
        def do_POST(self) -> None:
            length = int(self.headers.get("Content-Length", 0))
            url = urllib.parse.urlparse(self.path)
            state.requests.append(
                {
                    "params": dict(urllib.parse.parse_qsl(url.query)),
                    "headers": self.headers,  # case-insensitive, like HTTP
                    "body": self.rfile.read(length),
                }
            )
            status, headers, body = state.responses.pop(0)
            self.send_response(status)
            for k, v in headers.items():
                self.send_header(k, v)
            self.send_header("Content-Length", str(len(body)))
            self.end_headers()
            self.wfile.write(body)

        def log_message(self, *args: object) -> None:
            pass

    server = ThreadingHTTPServer(("127.0.0.1", 0), Handler)
    thread = threading.Thread(target=server.serve_forever, daemon=True)
    thread.start()
    try:
        yield state, ClickHouse(f"http://127.0.0.1:{server.server_port}", "u", "p", timeout=5)
    finally:
        server.shutdown()


def summary(rows: int) -> dict[str, str]:
    return {"X-ClickHouse-Summary": json.dumps({"result_rows": str(rows)})}


def test_rows_sends_parameters_and_credentials(fake: tuple[Fake, ClickHouse]) -> None:
    state, ch = fake
    state.responses.append((200, {}, b'{"n":"3","h":"42"}\n'))
    assert ch.checksum("otel.t WHERE x = {x:String}", {"x": "a b"}) == (3, "42")
    req = state.requests[0]
    assert req["params"] == {"wait_end_of_query": "1", "param_x": "a b"}
    assert req["headers"]["X-ClickHouse-User"] == "u"
    assert req["headers"]["X-ClickHouse-Key"] == "p"
    assert req["body"].decode() == (
        "SELECT count() AS n, toString(sum(cityHash64(*))) AS h FROM otel.t WHERE x = {x:String}"
        "\nFORMAT JSONEachRow"
    )


def test_export_writes_the_compressed_body(fake: tuple[Fake, ClickHouse], tmp_path: Path) -> None:
    state, ch = fake
    state.responses.append((200, {**summary(2), "Content-Encoding": "zstd"}, b"\x28\xb5data"))
    path = tmp_path / "t.native.zst"
    assert ch.export("SELECT * FROM otel.t", {"start": "s"}, path) == 2
    assert path.read_bytes() == b"\x28\xb5data"
    req = state.requests[0]
    assert req["headers"]["Accept-Encoding"] == "zstd"
    assert req["params"]["enable_http_compression"] == "1"
    assert req["params"]["param_start"] == "s"
    assert req["body"] == b"SELECT * FROM otel.t\nFORMAT Native"


def test_export_of_nothing_is_an_empty_file(fake: tuple[Fake, ClickHouse], tmp_path: Path) -> None:
    state, ch = fake
    state.responses.append((200, summary(0), b""))
    path = tmp_path / "t.native.zst"
    assert ch.export("SELECT * FROM otel.t", None, path) == 0
    assert path.read_bytes() == b""


def test_export_refuses_an_uncompressed_body(fake: tuple[Fake, ClickHouse], tmp_path: Path) -> None:
    state, ch = fake
    state.responses.append((200, summary(2), b"plain"))
    with pytest.raises(ClickHouseError, match="did not compress"):
        ch.export("SELECT 1", None, tmp_path / "t")


def test_insert_sends_the_file_as_zstd(fake: tuple[Fake, ClickHouse], tmp_path: Path) -> None:
    state, ch = fake
    state.responses.append((200, {}, b""))
    path = tmp_path / "t.native.zst"
    path.write_bytes(b"\x28\xb5data")
    ch.insert("rec_x.otel_traces", path)
    req = state.requests[0]
    assert req["params"]["query"] == "INSERT INTO rec_x.otel_traces FORMAT Native"
    assert req["headers"]["Content-Encoding"] == "zstd"
    assert req["body"] == b"\x28\xb5data"


def test_errors_carry_the_server_message(fake: tuple[Fake, ClickHouse]) -> None:
    state, ch = fake
    state.responses.append((404, {}, b"Code: 60. DB::Exception: Unknown table\n"))
    with pytest.raises(ClickHouseError, match="answered 404: Code: 60. DB::Exception"):
        ch.execute("SELECT * FROM nope")


def test_unreachable() -> None:
    with pytest.raises(ClickHouseError, match="ClickHouse at http://127.0.0.1:1/"):
        ClickHouse("http://127.0.0.1:1", timeout=1).version()
