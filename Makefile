.PHONY: help build proto lint test migrate-up migrate-down compose-up compose-down clean

BIN_DIR := bin
PG_DSN ?= postgres://agentlens:agentlens@localhost:5432/agentlens?sslmode=disable

help:
	@grep -E '^[a-zA-Z_-]+:.*?##' $(MAKEFILE_LIST) | awk 'BEGIN {FS = ":.*?##"}; {printf "  \033[36m%-20s\033[0m %s\n", $$1, $$2}'

build: ## Build server + hook binaries
	@mkdir -p $(BIN_DIR)
	go build -o $(BIN_DIR)/agent-lens      ./cmd/agent-lens
	go build -o $(BIN_DIR)/agent-lens-hook ./cmd/agent-lens-hook

proto: ## Generate Go code from proto/
	buf generate

gqlgen: ## Generate GraphQL Go bindings
	cd internal/query && go run github.com/99designs/gqlgen generate

lint: ## Run linters
	golangci-lint run ./...
	cd web && pnpm lint

test: ## Run unit + handler tests (no Docker)
	go test ./...

test-integration: ## Run Postgres integration tests (requires Docker)
	@if [ -S "$$HOME/.colima/default/docker.sock" ] && [ -z "$$DOCKER_HOST" ]; then \
		export DOCKER_HOST=unix://$$HOME/.colima/default/docker.sock; \
	fi; \
	TESTCONTAINERS_RYUK_DISABLED=true go test -tags integration ./...

migrate-up: ## Apply DB migrations
	migrate -path migrations -database "$(PG_DSN)" up

migrate-down: ## Roll back last migration
	migrate -path migrations -database "$(PG_DSN)" down 1

compose-up: ## Start local Postgres + MinIO
	docker compose -f deploy/compose/docker-compose.yml up -d

compose-down: ## Stop local stack
	docker compose -f deploy/compose/docker-compose.yml down

web-install: ## Install web dependencies
	cd web && pnpm install

web-dev: ## Run Vite dev server (proxies /v1 to localhost:8787)
	cd web && pnpm dev

web-build: ## Production build of the web bundle
	cd web && pnpm build

clean: ## Remove build outputs
	rm -rf $(BIN_DIR) internal/pb/*.pb.go web/dist
