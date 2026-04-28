# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Project status

M1–M3 have shipped: Claude Code hook → ingest → store → GraphQL → Lens UI is working end-to-end, with linking, in-toto/SLSA attestation, hash-chain `verify`, and audit-report export. §17 dogfood activation is live, so this repo is its own first dogfood. M2 session list (sessions GraphQL + UI list page) merged on 2026-04-28. See `SPEC.md` §14 for the milestone breakdown.

## What this project is

Agent Lens is a transparency & audit system for Coding Agents. It captures developer ↔ Coding Agent interactions, the agent's internal reasoning, and downstream artifacts (commit / PR / build / deploy), and stitches them into a verifiable evidence chain. See `SPEC.md` for the full design.

## Pinned decisions

These were settled during initial scoping and are referenced throughout `SPEC.md`. Don't relitigate without explicit user direction:

- **First-party agent**: Claude Code (via hooks). OpenCode is the second target. See `SPEC.md` §10.
- **Deployment**: self-hosted first (single-node Docker Compose for MVP, K8s Helm for HA). SaaS is out of scope for v1. See `SPEC.md` §13.
- **Supply chain compatibility**: internal event schema is custom and fine-grained; standard **in-toto / SLSA** attestations are emitted only at stage boundaries (commit / build / deploy). See `SPEC.md` §11.
- **Tech stack** (locked, see `SPEC.md` §16):
  - Backend: **Go** (chosen over Rust because the in-toto/SLSA toolchain is native Go).
  - Ingest: **HTTP + NDJSON** for v1; gRPC deferred. Protobuf is canonical schema regardless.
  - Storage: **Postgres** for events (with hash chain), **MinIO** (S3 protocol) for artifacts.
  - Frontend: **React + TS + Vite + Tailwind + shadcn/ui**, with **ReactFlow** for the causal graph and **Monaco** for code/diff.
  - Signing: **sigstore-go** library + local ed25519 keys (air-gap default); Sigstore/cosign optional when network is available.

## Working in this repo

- Treat `SPEC.md` as the source of truth for design. If a request conflicts with it, surface the conflict and ask before changing the spec.
- Common commands are wired in the `Makefile`: `make build`, `make proto`, `make gqlgen`, `make test`, `make test-integration`, `make migrate-up`, `make compose-up`, `make web-dev`, `make web-build`. `make help` lists them all.

## Local development with persistence

The collector defaults to `AGENT_LENS_STORE=postgres`. To run the dogfood loop with data that survives restarts:

```bash
# 1. Start Postgres + MinIO (only the services we actually use locally;
#    the agent-lens compose service is for production-style runs and
#    its Dockerfile lags behind go.mod, so skip it).
docker compose -f deploy/compose/docker-compose.yml up -d postgres minio

# 2. Apply migrations (requires `golang-migrate`; `brew install golang-migrate`).
make migrate-up

# 3. Run the collector. With no AGENT_LENS_STORE override it talks to
#    localhost:5432 using the default DSN baked into main.go.
go run ./cmd/agent-lens

# 4. Optional: Vite dev server proxies /v1 to 8787.
make web-dev
```

Set `AGENT_LENS_STORE=memory` for an ephemeral run (used by integration tests and for one-off smoke loops). Set `AGENT_LENS_PG_DSN=...` to point at a non-default Postgres.
