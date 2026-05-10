---
name: self-review
description: Pre-merge self-review checklist for the agent-lens repo. Runs the mechanical pass automatically (git staging hygiene, codegen drift, tests, vet, typecheck, debug-marker scan) for the current branch, walks through judgment-pass prompts (hostile-reviewer view, "fine for v1" rationalizations, UX coverage), and recommends `/review` or `/ultrareview` escalation when the PR touches public schema or spans multiple packages. Always ends with an explicit "what this review did not cover" disclaimer so manual smoke gets handed off rather than implied. Use BEFORE submitting a self-review summary or before merging any PR. Triggers on `/self-review`, "self review", "ready to merge", "before merge", "pre-merge check".
---

# /self-review

Pre-merge ritual codified from observed failure modes during ADR 0002 phase A/B (#63, #67), the graph/chip tooltip work (#61, #67), and the v0.1.0 first-tag-cut iterations ([#93](https://github.com/dong-qiu/agent-lens/issues/93)). Three passes plus a coverage disclaimer.

The mechanical pass MUST run before any human-judgment review — past misses (codegen drift, `git add -A` sweeping ADR drafts) were all binary checks dressed up as judgment problems.

## Phase 1 — mechanical pass (run automatically, gate on failure)

Pull the change scope first, then run only the checks that apply:

```bash
# Confirm the PR scope and that staging is clean
git status --short
git log --oneline origin/main..HEAD
CHANGED=$(git diff --name-only origin/main..HEAD)
echo "$CHANGED"
```

Then, conditionally:

| Trigger | Command | Pass criterion |
|---|---|---|
| Any `proto/*.proto` | `make proto && git diff --exit-code` | exit 0 |
| Any `internal/query/schema.graphql` or `gqlgen.yml` | `make gqlgen && git diff --exit-code` | exit 0 |
| Any `*.go` | `go test ./... -count=1 && go vet ./...` | both exit 0 |
| Any `web/**/*.{ts,tsx,css}` | `(cd web && npx tsc --noEmit)` | exit 0 |
| Any `deploy/compose/Dockerfile*` | `docker build -f deploy/compose/Dockerfile.server .` (CI also runs this per #94; local is fast-feedback) | exit 0, OR explicitly note "skipped — Docker unavailable, CI will catch" |
| Any `.github/workflows/*.yml` | `actionlint .github/workflows/<file>` (skip if not installed; flag as gap) | exit 0 |
| Any `.github/workflows/release.yml` | Trigger dry-run + watch to completion. Two steps: `gh workflow run release.yml --ref <branch> -f dry_run=true`, then look up the run id (`gh run list --workflow=release.yml --limit 1`) and `gh run watch <id> --exit-status`. REQUIRED before merge — actionlint above is static; this is the only dynamic check that exercises the YAML against real GitHub. | run completes green, no unexpected failures |
| Any `docs/RELEASE_NOTES_*.md` | Cross-check every quantitative claim ("byte-equal", "same sha256", "deterministic build", "X bytes") against actual dry-run artifact (`gh run download <id>` then `sha256sum`). Don't write hash claims you haven't verified. | every quantitative claim verified or rephrased |
| (always) | `git diff origin/main..HEAD \| grep -nE 'DEBUG\|console\.log\|debugger\|FIXME\|XXX'` | no matches |
| (always) | `git status --short \| grep '^??'` | confirm any untracked files are intentionally NOT in the PR |

Report each as ✅ or ❌ with exact context. **If any check fails, STOP and surface to the user — do not proceed to Phase 2.** Fixing a mechanical miss before the judgment pass keeps the two failure modes cleanly separated.

## Phase 2 — judgment prompts (answer honestly, in writing)

Walk through each prompt and produce a real answer; vague or absent answers are signal to look harder.

1. **Hostile reviewer.** "If a reviewer who wanted to reject this PR read it, what would they catch?" Name at least one concrete concern, even if you'd defend it.

2. **"Fine for v1" rationalizations.** Grep your own mental notes for "fine", "acceptable", "for now", "later", "v1", "good enough". Each occurrence resolves to either:
   - (a) **fix in this PR** — change the implementation now, or
   - (b) **open a follow-up issue** with explicit activation conditions ("revisit when X"), and link the issue from the PR description.

   Leaving any as an unowned comment guarantees it's forgotten.

3. **Comment-to-code ratio.** Find the longest comment in the diff. If it's longer than the code it documents, re-examine — over-long comments often signal unresolved confusion the author talked themselves into.

4. **UX / visual / perception scope.** Does this PR change tooltip behavior, animation, layout, color contrast, or anything a user perceives? **Self-review does NOT cover these.** Flag explicitly for manual smoke and list the specific surfaces to test.

## Phase 3 — external-view escalation

Inspect the changed paths to decide whether to recommend a fresh-context second opinion:

- Touches `internal/query/schema.graphql`, `proto/*.proto`, `cmd/*/main.go` (CLI surface), `internal/attest/*` (attestation predicates), or `internal/hashchain/*` (chain integrity): **recommend `/review`**.
- Touches `.github/workflows/release.yml` or `deploy/compose/Dockerfile*` (release pipeline; asymmetric blast radius — a broken release.yml costs hours of GHA runtime + tag-rebase friction): **recommend `/review` with a hostile-reviewer brief specifically asking "what fails if this runs against a non-default branch / dirty working tree / new platform?"** (per [#93](https://github.com/dong-qiu/agent-lens/issues/93) Item 7).
- Spans 3+ packages OR includes both Go backend and `web/`: **surface to the user about running `/ultrareview`** (it's user-triggered and billed; cannot be launched directly).

For < 100 LOC single-concern PRs, mechanical + judgment passes are sufficient.

## Phase 4 — coverage disclaimer (always output)

End the self-review summary with an explicit "this review did not cover" list. Pick the categories that apply:

- UX latency / animation / layout shift
- Visual rendering / color / theming / dark mode
- Load behavior / production scale (e.g., session size beyond dogfood)
- Real-user data scenarios (i18n, very long IDs, edge inputs)

Format: **"Manual smoke needed for: [...]"**. If genuinely none apply (pure backend / docs / tests / pure refactor), say so explicitly — silence is ambiguous.

---

## Why this skill exists (specific failure modes guarded)

If a new class of miss recurs, add a phase-1 check for it.

- **Codegen drift**: forgot `make gqlgen` after a `schema.graphql` doc-comment edit; CI rejected (#67). Mechanical, not judgment — phase 1's gqlgen check.
- **`git add -A` trap**: three unrelated ADR drafts in untracked working tree got swept into a commit; required soft-reset + force-push. Phase 1 explicitly lists untracked files so you choose what to stage.
- **UX-miss repetition**: 700 ms native-`title` tooltip delay was hit on graph node (#61), fixed via React state + portal, then **identically repeated** on token chip (#67) — same root cause, same fix, missed by self-review both times. Phase 2's UX prompt and Phase 4's coverage disclaimer push these onto manual smoke instead of pretending self-review covers them.
- **"Fine for v1" orphan comments**: deferred trade-offs that aren't tracked become invisible. Phase 2 forces the resolve-or-ticket discipline that produced #58 / #62 / #65 / #66.
- **Dockerfile breakage shipped to tag** (v0.1.0 iter 1+2 / [#90](https://github.com/dong-qiu/agent-lens/pull/90), [#91](https://github.com/dong-qiu/agent-lens/pull/91)): `docker build -f deploy/compose/Dockerfile.server .` locally would have caught both iterations (pnpm safety check), but self-review didn't watch Dockerfile changes. CI now also catches via [#94](https://github.com/dong-qiu/agent-lens/pull/94)'s `dockerfile-build` job; phase 1 still asks the local check for fast feedback.
- **release.yml self-test gap** (v0.1.0 iter 3 / [#92](https://github.com/dong-qiu/agent-lens/pull/92)): the QEMU arm64 emulation issue was structurally invisible to PR review because release.yml triggered only on tag push. PR [#94](https://github.com/dong-qiu/agent-lens/pull/94) added `workflow_dispatch dry_run`; phase 1 now requires running it on the PR branch BEFORE merge whenever release.yml is touched.
- **Release-notes overclaim** (v0.1.1): I wrote "v0.1.1 binaries are byte-equal to v0.1.0" in release notes, which was false — Go's `-buildvcs=true` (default) embeds the commit hash into `runtime/debug.BuildInfo`, so different commits ⇒ different binaries even with `-trimpath`. Caught by the pre-tag dry-run when I cross-checked the artifact sha256 against v0.1.0's. Phase 1's `RELEASE_NOTES_*.md` row exists to force this verification before tag.

## What this skill does NOT cover

This skill enforces the *checklist*. It does not replace:

- Manual UI smoke tests (run dev server, hover, click, watch latency, try keyboard nav).
- Load or soak testing under production-like data volume.
- The user's own validation on real dogfood — always preferred over self-claim of "looks good".
