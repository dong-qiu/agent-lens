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

## Repository layout & branching

The checkout is a **bare-repo worktree container**, not a normal clone:

```
agent-lens/
├── .bare/    # bare git dir — the shared object store
├── .git      # file: "gitdir: ./.bare"
├── main/     # permanent worktree, always on `main`
└── <task>/   # one short-lived worktree per in-flight task
```

**Branching is trunk-based.** `main` is the single trunk and is always
releasable — there is no long-lived `develop` branch.

- **One task = one branch = one worktree = one PR.** Cut a short-lived branch
  off `origin/main`, work in its own worktree, open a PR, squash-merge to
  `main`, then delete the branch + worktree. Run tasks in parallel by having
  several worktrees checked out at once — never by stashing or long-lived
  branches.
- Branch names: `feat/…`, `fix/…`, `refactor/…`, `docs/…`, `chore/…`. The
  worktree directory mirrors the branch (`feat/x` → `feat-x/`).
- `main/` is never developed in — it is for releases, tagging, reading
  `SPEC.md` / ADRs, and hosting the dogfood stack (`deploy/compose/.data`).
- Never run `git` / `make` / `go` at the container root — it is bare; `cd`
  into a worktree first.

**Keeping parallel work safe:**

- Branches stay short-lived (merge within a day or two) and small.
- Rebase on `origin/main` before opening a PR so parallel branches don't drift.
- Split refactors into small, independently-mergeable steps — no weeks-long
  refactor branch; it would conflict with every other in-flight stream.
- After a squash-merge, delete the local branch with `git branch -D` (`-d`
  refuses because squash leaves the branch looking unmerged).

## Self-review before merge

**Run `/self-review` before submitting any self-review summary or merging a PR.** The skill (in `.claude/skills/self-review/SKILL.md`) runs the mechanical pass automatically (git staging hygiene, codegen drift, tests, typecheck, debug-marker scan, Dockerfile build, actionlint, release.yml dry-run, RELEASE_NOTES quantitative-claim cross-check), walks the judgment-pass prompts, recommends `/review` or `/ultrareview` escalation when the PR warrants it, and ends with an explicit "what this review didn't cover" disclaimer.

The skill exists because passive checklists (this file alone) didn't prevent the failure modes that motivated it — codegen drift, accidental file inclusion, repeated UX misses, Dockerfile breakage shipped to tag, release-notes hash overclaims. Mechanical hygiene is enforced by code that runs, not docs that have to be remembered.

**Self-review structurally cannot cover** UX latency, visual rendering, layout shift, or perception. Static analysis won't see a 700 ms tooltip delay or a misaligned chip. The skill surfaces these as explicit "manual smoke needed" handoffs to the user — not as items the review claims to subsume.

**Post-merge calibration**: if CI catches something `/self-review` missed, add a check to the skill's Phase 1. The next contributor (or future-me) inherits the lesson via skill update, not by reading docs.

## Risk-asymmetric files require `/review`

Per [#93](https://github.com/dong-qiu/agent-lens/issues/93) Item 7. Some files have asymmetric blast radius: a missed bug propagates to all users (release pipeline) or breaks the integrity guarantees the project exists to provide (attestation, hash chain). Self-review alone is insufficient for these.

Any PR touching the following **must have `/review` invoked** before merge (the `/self-review` skill's Phase 3 escalates this automatically):

- `.github/workflows/release.yml` — broken release.yml = hours of GHA + tag-rebase friction (v0.1.0 ate 4 iterations / ~6h on this lesson)
- `deploy/compose/Dockerfile*` — ships in every container image; PR-time CI catches build breakage but not runtime semantics
- `internal/attest/*` — DSSE envelope / in-toto predicate construction; signing or verifying wrong = silently-broken supply chain
- `internal/hashchain/*` — chain integrity primitive; bug here invalidates `verify` claims

For these paths, `/review` is project policy, not a soft suggestion.

## Release versioning + RC tag pattern

Per [#93](https://github.com/dong-qiu/agent-lens/issues/93) Item 5:

- **Stable release**: tag `vX.Y.Z` (e.g. `v0.1.1`). Marked latest; `/releases/latest/...` redirects, `:latest` ghcr tag advances.
- **Release candidate**: tag `vX.Y.Z-rcN` (e.g. `v0.2.0-rc1`). Marked **prerelease** automatically; `/releases/latest/...` and `:latest` skip it. Use when the dispatch dry-run path can't cover the test (e.g. asking external testers to install via stable URL pattern, or testing the full `gh release create` codepath end-to-end).
- **Pre-tag validation**: prefer `gh workflow run release.yml --ref <branch> -f dry_run=true` (cheaper than RC tags; no public artifact). RC tags are for the cases dry-run can't reach.
- **RC suffix is lowercase `-rc`** (not `-RC`, `-Rc`). The release.yml RC detection uses case-sensitive `contains(github.ref_name, '-rc')` and `[[ ... == *-rc* ]]`. A tag like `v1.0.0-RC1` would be incorrectly published as **stable** and advance `:latest`. Stick to lowercase.

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
