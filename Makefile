# Run `make help` for the list of targets.

GO_MODULES := changes demo/internal demo/services/gateway demo/services/orders demo/services/payments
PY_PROJECTS := engine demo/services/catalog bench

.DEFAULT_GOAL := help

.PHONY: help
help: ## Show this help
	@grep -hE '^[a-z0-9-]+:.*## ' $(MAKEFILE_LIST) | awk -F':.*## ' '{printf "  %-16s %s\n", $$1, $$2}'

## Local stack

COMPOSE_FILES := -f deploy/compose/compose.yaml
ifeq ($(DEBUG),1)
COMPOSE_FILES += -f deploy/compose/compose.debug.yaml
endif
COMPOSE := docker compose $(COMPOSE_FILES)
CH_QUERY = $(COMPOSE) exec -T clickhouse clickhouse-client --query

.PHONY: up down reset ps logs smoke
up: ## Build the demo services, start the stack and wait until it is healthy (DEBUG=1 adds the debug exporter)
	@$(FAULTS) init
	$(COMPOSE) up -d --build --wait

down: ## Stop the local stack, keep data
	$(COMPOSE) down

reset: ## Stop the local stack and delete its volumes
	$(COMPOSE) down --volumes

ps: ## Show stack status
	$(COMPOSE) ps

logs: ## Follow stack logs (SERVICE=name for one service)
	$(COMPOSE) logs -f $(SERVICE)

smoke: ## Check that test spans and a demo checkout flow reach ClickHouse and that the changes API stores events
	$(COMPOSE) --profile smoke run --rm telemetrygen
	@for i in $$(seq 1 30); do \
	  n=$$($(CH_QUERY) "SELECT count() FROM otel.otel_traces WHERE ServiceName = 'smoke-test' AND Timestamp > now() - INTERVAL 5 MINUTE" 2>/dev/null); \
	  if [ "$${n:-0}" -gt 0 ]; then echo "smoke: $$n spans in otel.otel_traces"; exit 0; fi; \
	  sleep 1; \
	done; echo "smoke: no spans reached otel.otel_traces after 30s" >&2; exit 1
	@if [ "$$($(CH_QUERY) 'EXISTS TABLE incidentlens.changes' < /dev/null)" = 1 ]; then \
	  echo "smoke: incidentlens.changes exists"; \
	else echo "smoke: incidentlens.changes is missing" >&2; exit 1; fi
	@COMPOSE="$(COMPOSE)" demo/smoke.sh
	@changes/smoke.sh

## Faults

FAULTS := uv run --quiet demo/faults.py

.PHONY: faults fault fault-clear
faults: ## List the demo faults that are on
	@$(FAULTS) list

fault: ## Turn demo faults on: make fault SET="payments.latency_ms.postgres=800 ..."
	@test -n "$(SET)" || { echo 'usage: make fault SET="flag=value ..."' >&2; exit 2; }
	@$(FAULTS) set $(SET)

fault-clear: ## Turn demo faults off (FLAG="flag ..." for some, all by default)
	@$(FAULTS) clear $(FLAG)

## Scenarios

.PHONY: check-scenarios scenario
check-scenarios: ## Validate the benchmark scenario files
	cd bench && uv run --locked validate.py

scenario: ## Run a scenario on the stack: make scenario ID=payments-db-latency [WARMUP=30s DURATION=1m]
	@test -n "$(ID)" || { echo 'usage: make scenario ID=<scenario id> [WARMUP=30s] [DURATION=1m]' >&2; exit 2; }
	@uv run --quiet --project engine incidentlens scenario run $(ID) $(if $(WARMUP),--warmup $(WARMUP)) $(if $(DURATION),--duration $(DURATION))

## Recordings

# Release that holds the recordings listed in bench/recordings.sha256.
RECORDINGS_URL := https://github.com/dinhostork/IncidentLens/releases/download/recordings-2026-11-28
RECORDING := uv run --quiet --project engine incidentlens recording

.PHONY: record load-recording fetch-recordings check-recordings
record: ## Save a scenario run to recordings/<run id>: make record RUN=<run id> [MARGIN=10m]
	@test -n "$(RUN)" || { echo 'usage: make record RUN=<run id> [MARGIN=10m]' >&2; exit 2; }
	@$(RECORDING) save $(RUN) $(if $(MARGIN),--margin $(MARGIN))

load-recording: ## Load a recording into its own ClickHouse database: make load-recording REC=recordings/<run id> [DB=name]
	@test -n "$(REC)" || { echo 'usage: make load-recording REC=recordings/<run id> [DB=name]' >&2; exit 2; }
	@$(RECORDING) load $(REC) $(if $(DB),--database $(DB))

fetch-recordings: ## Download and unpack the recordings listed in bench/recordings.sha256 into recordings/
	@mkdir -p recordings
	@while read -r sum file; do \
	  test -f "recordings/$$file" || curl -fsSL -o "recordings/$$file" "$(RECORDINGS_URL)/$$file" || exit 1; \
	done < bench/recordings.sha256
	@cd recordings && sha256sum --check --strict --quiet ../bench/recordings.sha256
	@while read -r sum file; do \
	  test -d "recordings/$${file%.tar}" || tar -xf "recordings/$$file" -C recordings || exit 1; \
	done < bench/recordings.sha256

check-recordings: fetch-recordings ## Load every listed recording into ClickHouse (replacing it) and check rows and checksums
	@while read -r sum file; do \
	  $(RECORDING) load "recordings/$${file%.tar}" --replace < /dev/null || exit 1; \
	done < bench/recordings.sha256

## Setup

.PHONY: setup
setup: ## Install Python and web dependencies
	@for p in $(PY_PROJECTS); do (cd $$p && uv sync --locked) || exit 1; done
	cd web && pnpm install --frozen-lockfile

## Lint

.PHONY: lint lint-go lint-py lint-web
lint: lint-go lint-py lint-web ## Run all linters

lint-go: ## go vet and golangci-lint on every Go module
	@for m in $(GO_MODULES); do echo "==> $$m"; (cd $$m && go vet ./... && golangci-lint run ./...) || exit 1; done

lint-py: ## ruff check and format check
	@for p in $(PY_PROJECTS); do echo "==> $$p"; (cd $$p && uv run ruff check && uv run ruff format --check) || exit 1; done

lint-web: ## eslint and type check
	cd web && pnpm lint && pnpm typecheck

## Test

.PHONY: test test-go test-py test-loadgen test-web test-clickhouse
test: test-go test-py test-loadgen test-web ## Run all tests

test-go: ## go test on every Go module
	@for m in $(GO_MODULES); do echo "==> $$m"; (cd $$m && go test ./...) || exit 1; done

test-py: ## pytest
	@for p in $(PY_PROJECTS); do echo "==> $$p"; (cd $$p && uv run pytest) || exit 1; done

test-loadgen: ## Tests for the load generator's rate model (Node)
	cd demo/loadgen && node --test

test-web: ## Production build of the web app (no unit tests yet)
	cd web && pnpm build

test-clickhouse: ## Changes API store tests against the stack's ClickHouse (needs make up, not part of make test)
	cd changes && go test -tags clickhouse -count=1 ./clickhouse/

## Format

.PHONY: fmt
fmt: ## Format Go, Python and web code
	@for m in $(GO_MODULES); do (cd $$m && golangci-lint fmt ./...) || exit 1; done
	@for p in $(PY_PROJECTS); do (cd $$p && uv run ruff check --fix && uv run ruff format) || exit 1; done
	cd web && pnpm lint --fix
