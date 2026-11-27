#!/bin/sh
# Posts a note through the changes API, reads it back by time window and
# retries the post. Called by `make smoke`; expects the stack to be up.
set -eu

URL=${CHANGES_URL:-http://127.0.0.1:${CHANGES_PORT:-8090}}
auth=
if [ -n "${CHANGES_TOKEN:-}" ]; then auth="Authorization: Bearer $CHANGES_TOKEN"; fi

now=$(date -u +%Y-%m-%dT%H:%M:%SZ)
id="smoke-$(date -u +%s)-$$"
event="{\"id\":\"$id\",\"time\":\"$now\",\"kind\":\"note\",\"environment\":\"smoke\",\"description\":\"make smoke\",\"source\":\"smoke\"}"

post() {
	curl -sS -o /dev/null -w '%{http_code}' ${auth:+-H "$auth"} -H 'Content-Type: application/json' -d "$event" "$URL/v1/changes"
}

if [ "$(post)" != 201 ]; then echo "changes smoke: post was not stored" >&2; exit 1; fi
if [ "$(post)" != 200 ]; then echo "changes smoke: retry was not recognized" >&2; exit 1; fi

# The event is at a whole second, so [now, now + 1 ms) holds it.
found=$(curl -fsS ${auth:+-H "$auth"} "$URL/v1/changes?environment=smoke&start=$now&end=${now%Z}.001Z")
case $found in
*"\"id\":\"$id\""*) echo "changes smoke: $id stored and listed" ;;
*)
	echo "changes smoke: $id not listed: $found" >&2
	exit 1
	;;
esac
