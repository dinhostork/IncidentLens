#!/bin/sh
# Runs one browse -> cart -> checkout flow through the gateway, then checks
# that server spans from all four services and client spans to Postgres and
# Redis reached ClickHouse. Called by `make smoke`; expects the stack to be up.
set -eu

GATEWAY=${GATEWAY:-http://127.0.0.1:${GATEWAY_PORT:-8080}}
COMPOSE=${COMPOSE:-docker compose -f deploy/compose/compose.yaml}

api() {
	if [ $# -gt 2 ]; then
		curl -fsS -X "$1" -H 'Content-Type: application/json' -d "$3" "$GATEWAY/api$2"
	else
		curl -fsS -X "$1" "$GATEWAY/api$2"
	fi
}

since=$(($(date -u +%s) - 1))

api GET /products >/dev/null
api GET /products/7 >/dev/null
cart=$(api POST /carts | sed -E 's/.*"id":"([^"]+)".*/\1/')
api POST "/carts/$cart/items" '{"product_id":7,"quantity":2}' >/dev/null
order=$(api POST "/carts/$cart/checkout")
case $order in
*'"status":"paid"'*) echo "demo smoke: checkout ok" ;;
*)
	echo "demo smoke: checkout failed: $order" >&2
	exit 1
	;;
esac

want="['catalog','gateway','orders','payments','postgresql','redis']"
query="SELECT arraySort(groupUniqArray(if(SpanKind = 'Server', ServiceName, SpanAttributes['db.system.name'])))
FROM otel.otel_traces
WHERE Timestamp >= toDateTime($since) AND (SpanKind = 'Server' OR SpanAttributes['db.system.name'] != '')"

got=
for _ in $(seq 1 30); do
	got=$($COMPOSE exec -T clickhouse clickhouse-client --query "$query" 2>/dev/null || true)
	if [ "$got" = "$want" ]; then
		echo "demo smoke: spans from $got in otel.otel_traces"
		exit 0
	fi
	sleep 1
done
echo "demo smoke: want spans from $want, got ${got:-nothing} after 30s" >&2
exit 1
