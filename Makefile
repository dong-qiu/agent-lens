.PHONY: help build build-prod embed-webui proto lint test migrate-up migrate-down compose-up compose-down db-backup db-restore db-verify-backup clean

BIN_DIR := bin
PG_DSN ?= postgres://agentlens:agentlens@localhost:5432/agentlens?sslmode=disable

help:
	@grep -E '^[a-zA-Z_-]+:.*?##' $(MAKEFILE_LIST) | awk 'BEGIN {FS = ":.*?##"}; {printf "  \033[36m%-20s\033[0m %s\n", $$1, $$2}'

build: ## Build server + hook binaries (no UI embed; `go run` works for dev)
	@mkdir -p $(BIN_DIR)
	go build -o $(BIN_DIR)/agent-lens      ./cmd/agent-lens
	go build -o $(BIN_DIR)/agent-lens-hook ./cmd/agent-lens-hook

embed-webui: web-build ## Stage built UI into internal/webui/dist for embedding
	@rm -rf internal/webui/dist
	@mkdir -p internal/webui/dist
	@cp -r web/dist/. internal/webui/dist/
	@touch internal/webui/dist/.gitkeep

build-prod: embed-webui build ## Build with embedded UI (production / release)

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

migrate-up: ## (legacy) Apply DB migrations via external CLI. v0.1+ servers self-migrate on startup; this target is kept only for ops with AGENT_LENS_SKIP_MIGRATE=1.
	migrate -path internal/migrate/sql -database "$(PG_DSN)" up

migrate-down: ## (legacy) Roll back last migration via external CLI.
	migrate -path internal/migrate/sql -database "$(PG_DSN)" down 1

db-backup: ## Dump events / links / artifacts to ./backups/agentlens-<ts>.dump
	PG_DSN="$(PG_DSN)" scripts/pg-backup.sh

db-restore: ## Restore from a dump (DUMP=path/to/file.dump required)
	@if [ -z "$(DUMP)" ]; then echo "usage: make db-restore DUMP=backups/agentlens-...dump"; exit 2; fi
	PG_DSN="$(PG_DSN)" scripts/pg-restore.sh "$(DUMP)"

db-verify-backup: ## Round-trip backup + restore + hash-chain verify (SESSION=<id> required)
	@if [ -z "$(SESSION)" ]; then echo "usage: make db-verify-backup SESSION=<session-id>"; exit 2; fi
	PG_DSN="$(PG_DSN)" scripts/verify-backup-integrity.sh "$(SESSION)"

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
	@find internal/webui/dist -mindepth 1 ! -name .gitkeep -delete 2>/dev/null || true
