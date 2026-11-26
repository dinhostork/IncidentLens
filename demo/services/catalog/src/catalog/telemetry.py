"""OpenTelemetry setup for catalog, following docs/telemetry-conventions.md.

Exporters are configured from the standard OTEL_EXPORTER_OTLP_* variables. The
resource takes OTEL_SERVICE_NAME and OTEL_RESOURCE_ATTRIBUTES, and
service.version from SERVICE_VERSION.
"""

import logging
import os
import socket
import sys
import uuid
from collections.abc import Callable

from opentelemetry import metrics, trace
from opentelemetry._logs import set_logger_provider
from opentelemetry.exporter.otlp.proto.grpc._log_exporter import OTLPLogExporter
from opentelemetry.exporter.otlp.proto.grpc.metric_exporter import OTLPMetricExporter
from opentelemetry.exporter.otlp.proto.grpc.trace_exporter import OTLPSpanExporter
from opentelemetry.sdk._logs import LoggerProvider, LoggingHandler
from opentelemetry.sdk._logs.export import BatchLogRecordProcessor
from opentelemetry.sdk.metrics import MeterProvider
from opentelemetry.sdk.metrics.export import PeriodicExportingMetricReader
from opentelemetry.sdk.resources import Resource
from opentelemetry.sdk.trace import TracerProvider
from opentelemetry.sdk.trace.export import BatchSpanProcessor


def resource() -> Resource:
    attrs = {"service.instance.id": str(uuid.uuid4()), "host.name": socket.gethostname()}
    if version := os.environ.get("SERVICE_VERSION"):
        attrs["service.version"] = version
    # Resource.create reads OTEL_SERVICE_NAME and OTEL_RESOURCE_ATTRIBUTES;
    # explicit attributes win, so SERVICE_VERSION overrides the env value.
    return Resource.create(attrs)


def setup() -> Callable[[], None]:
    """Install global providers and route stdlib logging to OpenTelemetry.

    Must run before any instrumentation is applied. Returns a function that
    flushes and shuts everything down.
    """
    # Emit current HTTP semantic conventions (http.request.method,
    # server.address) instead of the pre-1.20 names.
    os.environ.setdefault("OTEL_SEMCONV_STABILITY_OPT_IN", "http")
    res = resource()

    tp = TracerProvider(resource=res)
    tp.add_span_processor(BatchSpanProcessor(OTLPSpanExporter()))
    trace.set_tracer_provider(tp)

    mp = MeterProvider(
        resource=res, metric_readers=[PeriodicExportingMetricReader(OTLPMetricExporter())]
    )
    metrics.set_meter_provider(mp)

    lp = LoggerProvider(resource=res)
    lp.add_log_record_processor(BatchLogRecordProcessor(OTLPLogExporter()))
    set_logger_provider(lp)

    root = logging.getLogger()
    root.setLevel(logging.INFO)
    stderr = logging.StreamHandler(sys.stderr)
    stderr.setFormatter(logging.Formatter("%(asctime)s %(levelname)s %(name)s %(message)s"))
    root.addHandler(stderr)
    root.addHandler(LoggingHandler(logger_provider=lp))

    def shutdown() -> None:
        lp.shutdown()
        mp.shutdown()
        tp.shutdown()

    return shutdown
