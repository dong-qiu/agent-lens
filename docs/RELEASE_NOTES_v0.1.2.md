# Agent Lens v0.1.2 — Lens UI actually shipped in binary

v0.1.0 / v0.1.1 release notes promised: "Lens Web UI（embed 在 server binary 里）". It wasn't true for the binary path. v0.1.2 fixes that.

## What v0.1.0 / v0.1.1 binary actually did

Run `agent-lens-darwin-arm64` (or any platform binary), open `:8787`, you got:

```
Lens UI not embedded in this build.
For development: run `make web-dev` for the Vite dev server (proxies /v1 to :8787).
For production: rebuild with `make build` (which runs make web-build && make embed-webui first).
```

…instead of the React app. Discovered while end-to-end smoke-testing the v0.1.1 release on a real arm64 Mac after #93 closed.

## Root cause

`release.yml` build-binaries job ran `go build` directly without first building the web bundle. `internal/webui/dist/` had only a `.gitkeep` in the checked-out source, so `//go:embed all:dist` produced an empty filesystem. The dev-mode stub message is the graceful fallback in `internal/webui/embed.go`.

The container path was unaffected — `Dockerfile.server` already does the multi-stage `web-build → go build` correctly, which is why v0.1.0 / v0.1.1 / v0.1.2-rc1 container images all served the UI fine.

## Fix

New `web-build` job in release.yml builds the bundle once, uploads as `web-dist` artifact. Each of the 4 binary jobs `needs: web-build` and downloads the artifact into `internal/webui/dist/` before `go build`. Same artifact pattern release.yml already uses for the binaries themselves.

`Dockerfile.server` is unchanged — it has a working multi-stage build since v0.1.0; not touching it preserves the proven container path. There's now a minor duplication (web-build runs twice: once in the new shared job, once in the Dockerfile). [#93](https://github.com/dong-qiu/agent-lens/issues/93) Item 4 (v0.2) will consolidate by switching the container build to use the shared artifact too.

## What's different from v0.1.1

- **Binary `:8787` `/`**: now serves the React UI (HTML), not a stub message.
- **Binary sha256**: changes — embedded web bundle adds bytes, plus `-buildvcs=true` already changes hashes per commit.
- **Container `:8787` `/`**: unchanged behavior (was already serving the UI).
- **API / GraphQL / event schema / hash chain / signing keys**: zero change. Source code untouched (only `release.yml`).
- **Public ed25519 key**: same as v0.1.0 / v0.1.1 (`agent-lens-public.pem` re-derived in-workflow but the key itself is unchanged).

## Upgrade

If you were following the README's "60-second try" with the binary path on v0.1.0 or v0.1.1 and the UI was broken — that's the bug. Pull v0.1.2:

```bash
curl -fsSL https://github.com/dong-qiu/agent-lens/releases/latest/download/agent-lens-darwin-arm64 \
  -o ./agent-lens
chmod +x ./agent-lens
```

Container users on v0.1.x: no action needed.

## Verification

This release was validated by:

1. Pre-merge dispatch dry-run on the PR branch (per the [#93](https://github.com/dong-qiu/agent-lens/issues/93) Item 2 / [#94](https://github.com/dong-qiu/agent-lens/pull/94) workflow_dispatch path) — confirmed the new web-build job produces a non-empty `web-dist` artifact and the binary jobs successfully embed it.
2. Post-merge dispatch dry-run on `main` — same as above.
3. Real tag push: artifact downloaded, run, `curl localhost:8787/` returned HTML containing the Lens app marker.

Per the [`/self-review`](https://github.com/dong-qiu/agent-lens/blob/main/.claude/skills/self-review/SKILL.md) skill's RELEASE_NOTES Phase 1 row added in [#96](https://github.com/dong-qiu/agent-lens/pull/96): "any quantitative claim about binary contents must be cross-checked against the actual dry-run artifact." The lesson cycle: v0.1.0 promised UI in binary → v0.1.1 carried the same false promise → v0.1.1 verification on a real laptop discovered it → v0.1.2 fixes + the next batch of release notes can't make the same claim without actually running the binary.

## Container `--platform` UX (not fixed in v0.1.2)

Apple Silicon users running `docker pull ghcr.io/dong-qiu/agent-lens:v0.1.2` may hit "no matching manifest for linux/arm64/v8" depending on Docker version. Workaround: `docker pull --platform linux/amd64 ...` or set `DOCKER_DEFAULT_PLATFORM=linux/amd64`. Multi-arch container is [#93](https://github.com/dong-qiu/agent-lens/issues/93) Item 4 / v0.2.

## Still open (#93 Items 3+4, v0.2)

- ADR explicitly scoping container platforms separately from binary platforms
- Native arm64 runner (`ubuntu-24.04-arm`) OR consolidating Dockerfile to use the shared `web-dist` artifact (eliminates duplication this PR introduces) — both eliminate the container `--platform linux/amd64` papercut.
