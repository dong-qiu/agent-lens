# ADR 0001: Tech stack for v1

- Status: Accepted
- Date: 2026-04-27
- Supersedes: —

## Context

Agent Lens is starting from a greenfield repo. Before scaffolding M1 we needed to settle the language, persistence, wire format, and frontend stack so subsequent ADRs only revisit them under explicit pressure.

The non-negotiable inputs:

- Self-hosted first, must work air-gapped.
- Stage-boundary attestations must conform to in-toto / SLSA (`SPEC.md` §11).
- Append-only event log with a hash chain.
- Hook authors range from Bash + curl to full Go services; ingest must accept both.

## Decision

- Backend in **Go** (chi + pgx + sqlc + golang-migrate).
- Canonical schema in **Protobuf** managed by `buf`. Wire format on the ingest path is **HTTP + NDJSON** for v1; gRPC may be added later without a schema change.
- Event store: **Postgres**. Object store: **MinIO** (S3 protocol).
- Signing: `sigstore-go` with local ed25519 keys by default; Fulcio/Rekor opt-in.
- Frontend: **React + TS + Vite + Tailwind + shadcn/ui**, with **ReactFlow** for the causal graph and **Monaco** for diff/code views.
- Hook binary: a single Go binary with subcommands (`claude`, `git-post-commit`, `verify`, `export`).

## Alternatives considered

- **Rust backend.** Technically viable but loses the supply-chain ecosystem advantage (in-toto-golang, sigstore-go, cosign, slsa-verifier are Go), and slows iteration during MVP. Reconsider only if (a) the team is already deeply Rust-fluent, (b) ingest demand exceeds Go's headroom, or (c) we need to ship the ingest core as an embeddable library.
- **gRPC for ingest.** Real wins (binary, bidirectional streaming) don't pay back at our event volume; debuggability and `curl`-friendliness do. Schema is shared, so this is a deferral, not a rejection.
- **ClickHouse as the canonical store.** Excellent for analytics but ordering semantics complicate the hash chain. Will be added later as a CDC-fed replica for analytical queries.
- **Svelte frontend.** Lighter and faster, but ReactFlow + Monaco are decisive components and their React ecosystems are stronger.

## Consequences

- The repo will need both a Go and a Node/TS toolchain. Hook authors only need Go (or a curl-capable shell).
- Postgres becomes the integrity-critical component: backup + recovery procedures must be written before M3.
- Choosing Go ties Agent Lens's release schedule loosely to Sigstore / cosign release cadence. We pin versions in `go.mod`.
- gRPC ingest, ClickHouse analytics replica, and Sigstore network signing are explicit follow-ups, not commitments — each will get its own ADR if/when introduced.
