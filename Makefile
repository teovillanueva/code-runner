SHELL := /bin/bash
.DEFAULT_GOAL := help

# Ensure Go (Homebrew) and Go tool bin are on PATH for make-invoked recipes.
export PATH := /opt/homebrew/bin:$(shell go env GOPATH 2>/dev/null)/bin:$(PATH)

.PHONY: help
help: ## Show this help
	@grep -E '^[a-zA-Z_-]+:.*?## .*$$' $(MAKEFILE_LIST) | sort | awk 'BEGIN{FS=":.*?## "}{printf "  \033[36m%-20s\033[0m %s\n", $$1, $$2}'

.PHONY: tools
tools: ## Install codegen tools (go-jsonschema)
	go install github.com/atombender/go-jsonschema@latest

.PHONY: install
install: ## Install JS deps
	pnpm install

.PHONY: contract
contract: ## Regenerate the wire contract (TS types + zod + Go structs)
	pnpm --filter @teovilla/code-runner-contract generate
	cd . && go mod tidy

.PHONY: contract-check
contract-check: ## Fail if generated contract artifacts drift from the schema
	pnpm --filter @teovilla/code-runner-contract generate
	git diff --exit-code -- packages/contract/gen || \
		( echo ""; echo "✗ contract drift: run 'make contract' and commit the result"; exit 1 )

.PHONY: build
build: ## Build everything (Go worker + JS)
	go build ./...
	pnpm -r build

.PHONY: test
test: ## Run all unit/integration tests (Go + JS)
	go test ./...
	pnpm -r test

.PHONY: test-go
test-go: ## Run Go tests
	go test ./...

.PHONY: abuse
abuse: ## Run the adversarial abuse/safety suite (requires Docker cgroup v2 + redis:7 on port 6381 + executor/python:3.12)
	go test -tags=abuse -timeout 600s ./internal/worker/... -run Abuse -v

.PHONY: test-docker
test-docker: ## Run guarded Docker integration tests (requires Docker daemon)
	go test -tags=docker -timeout 300s ./internal/runner/... -run Integration -v

.PHONY: up
up: ## Bring up the local dev stack
	docker compose up --build

.PHONY: down
down: ## Tear down the local dev stack
	docker compose down -v

.PHONY: test-redis
test-redis: ## Run Redis-dependent integration tests (requires TEST_REDIS_URL or local Redis on :6379)
	go test ./internal/redisx/... ./internal/stdintransport/... ./internal/jobstore/... -run Redis -v -count=1

.PHONY: test-worker
test-worker: ## Run guarded worker integration tests (requires Docker + redis:7 on port 6381 + executor/python:3.12)
	go test -tags=worker_integration -timeout 300s ./internal/worker/... -run Integration -v

.PHONY: reaper-test
reaper-test: ## Run reaper integration tests (requires Docker daemon + redis:7 on port 6381 + executor/python:3.12)
	go test -tags=reaper_integration -timeout 180s ./internal/reaper/... -run Reaper -v

.PHONY: python-image
python-image: ## Build the Python 3.12 sandbox image on the host Docker daemon
	docker build -t executor/python:3.12 languages/python-3.12

.PHONY: rust-image
rust-image: ## Build the Rust 1.83 sandbox image on the host Docker daemon (Wave 2)
	docker build -t executor/rust:1.83 languages/rust-1.83

.PHONY: r-image
r-image: ## Build the R 4.4 sandbox image on the host Docker daemon (Wave 2)
	docker build -t executor/r:4.4 languages/r-4.4

.PHONY: sqlite-image
sqlite-image: ## Build the SQLite 3 sandbox image on the host Docker daemon (Wave 2)
	docker build -t executor/sqlite:3 languages/sqlite-3

.PHONY: build-images
build-images: python-image rust-image r-image sqlite-image ## Build all language sandbox images on the host daemon
	@echo "All language images built."

.PHONY: langfanout
langfanout: ## Run language fan-out integration tests (requires Docker + all language images + redis:7 on port 6386)
	go test -tags=langfanout -timeout 600s ./internal/worker/... -run LangFanout -v

.PHONY: e2e
e2e: ## Run the end-to-end interactive demo against the local stack
	./scripts/e2e.sh
