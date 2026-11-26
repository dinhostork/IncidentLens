# Run `make help` for the list of targets.

GO_MODULES := changes demo/services/gateway demo/services/orders demo/services/payments
PY_PROJECTS := engine

.DEFAULT_GOAL := help

.PHONY: help
help: ## Show this help
	@grep -hE '^[a-z0-9-]+:.*## ' $(MAKEFILE_LIST) | awk -F':.*## ' '{printf "  %-12s %s\n", $$1, $$2}'

## Local stack

.PHONY: up down
up: ## Start the local stack (not available until the compose stack lands, #4)
	@echo "make up: no compose stack yet, see issue #4" >&2; exit 1

down: ## Stop the local stack
	@echo "make down: no compose stack yet, see issue #4" >&2; exit 1

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

.PHONY: test test-go test-py test-web
test: test-go test-py test-web ## Run all tests

test-go: ## go test on every Go module
	@for m in $(GO_MODULES); do echo "==> $$m"; (cd $$m && go test ./...) || exit 1; done

test-py: ## pytest
	@for p in $(PY_PROJECTS); do echo "==> $$p"; (cd $$p && uv run pytest) || exit 1; done

test-web: ## Production build of the web app (no unit tests yet)
	cd web && pnpm build

## Format

.PHONY: fmt
fmt: ## Format Go, Python and web code
	@for m in $(GO_MODULES); do (cd $$m && golangci-lint fmt ./...) || exit 1; done
	@for p in $(PY_PROJECTS); do (cd $$p && uv run ruff check --fix && uv run ruff format) || exit 1; done
	cd web && pnpm lint --fix
