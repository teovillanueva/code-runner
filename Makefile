SHELL := /bin/bash
.DEFAULT_GOAL := help

# Ensure Go (Homebrew) and Go tool bin are on PATH for make-invoked recipes.
export PATH := /opt/homebrew/bin:$(shell go env GOPATH 2>/dev/null)/bin:$(PATH)

.PHONY: help
help: ## Show this help
	@grep -E '^[a-zA-Z_-]+:.*?## .*$$' $(MAKEFILE_LIST) | sort | awk 'BEGIN{FS=":.*?## "}{printf "  \033[36m%-20s\033[0m %s\n", $$1, $$2}'

.PHONY: tools
tools: ## Install codegen tools (go-jsonschema)
	go install github.com/omissis/go-jsonschema/cmd/go-jsonschema@latest

.PHONY: install
install: ## Install JS deps
	pnpm install

.PHONY: contract
contract: ## Regenerate the wire contract (TS types + zod + Go structs)
	pnpm --filter @code-runner/contract generate
	cd . && go mod tidy

.PHONY: contract-check
contract-check: ## Fail if generated contract artifacts drift from the schema
	pnpm --filter @code-runner/contract generate
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
abuse: ## Run the abuse/safety test suite (requires Docker)
	go test -tags=abuse -timeout 600s ./apps/worker/... -run Abuse -v

.PHONY: up
up: ## Bring up the local dev stack
	docker compose up --build

.PHONY: down
down: ## Tear down the local dev stack
	docker compose down -v

.PHONY: e2e
e2e: ## Run the end-to-end interactive demo against the local stack
	./scripts/e2e.sh
