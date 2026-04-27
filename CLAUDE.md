# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Project status

Agent Lens is in the **planning phase**. There is no source code, build system, or test suite yet — only `SPEC.md`, the project specification. Future work will scaffold the implementation based on that spec.

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
- No build / test / lint commands exist yet — don't fabricate them. The repo layout in `SPEC.md` §16.3 is the planned structure but nothing has been scaffolded.
