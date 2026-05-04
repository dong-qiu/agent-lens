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
- Architecture decisions live in `docs/ADR/` (see `docs/ADR/README.md` for the SPEC vs ADR vs Patch mechanism). Accepted ADRs are append-only — propose a new ADR rather than editing one in place. Non-trivial design choices (new EventKind, schema change, irreversible tech selection) should land as a draft ADR before code.
- Common commands are wired in the `Makefile`: `make build`, `make build-prod` (with embedded UI), `make proto`, `make gqlgen`, `make test`, `make test-integration`, `make compose-up`, `make web-dev`, `make web-build`, `make embed-webui`. `make help` lists them all. (`make migrate-up` still exists but is legacy — server self-migrates on startup unless `AGENT_LENS_SKIP_MIGRATE=1`.)

## Self-review before merge

**Run `/self-review` before submitting any self-review summary or merging a PR.** The skill (in `.claude/skills/self-review/SKILL.md`) runs the mechanical pass automatically (git staging hygiene, codegen drift, tests, typecheck, debug-marker scan), walks the judgment-pass prompts, recommends `/review` or `/ultrareview` escalation when the PR warrants it, and ends with an explicit "what this review didn't cover" disclaimer.

The skill exists because passive checklists (this file alone) didn't prevent the failure modes that motivated it — codegen drift, accidental file inclusion, repeated UX misses. Mechanical hygiene is enforced by code that runs, not docs that have to be remembered.

**Self-review structurally cannot cover** UX latency, visual rendering, layout shift, or perception. Static analysis won't see a 700 ms tooltip delay or a misaligned chip. The skill surfaces these as explicit "manual smoke needed" handoffs to the user — not as items the review claims to subsume.

**Post-merge calibration**: if CI catches something `/self-review` missed, add a check to the skill's Phase 1. The next contributor (or future-me) inherits the lesson via skill update, not by reading docs.

## Local development with persistence

The collector defaults to `AGENT_LENS_STORE=postgres`. To run the dogfood loop with data that survives restarts:

```bash
# 1. Start Postgres + MinIO. (`make compose-up` also builds the
#    agent-lens server image; for the local dogfood loop we want to run
#    the collector via `go run` so we get hot rebuilds, so bring up only
#    the data services.)
docker compose -f deploy/compose/docker-compose.yml up -d postgres minio

# 2. (Migrations apply automatically on agent-lens startup; no separate
#     CLI install needed for the dogfood loop.)

# 3. Run the collector. With no AGENT_LENS_STORE override it talks to
#    localhost:5432 using the default DSN baked into main.go.
go run ./cmd/agent-lens

# 4. Optional: Vite dev server proxies /v1 to 8787.
make web-dev
```

Set `AGENT_LENS_STORE=memory` for an ephemeral run (used by integration tests and for one-off smoke loops). Set `AGENT_LENS_PG_DSN=...` to point at a non-default Postgres.
