---
name: self-review
description: Pre-merge self-review checklist for the agent-lens repo. Runs the mechanical pass automatically (git staging hygiene, codegen drift, tests, vet, typecheck, debug-marker scan) for the current branch, walks through judgment-pass prompts (hostile-reviewer view, "fine for v1" rationalizations, UX coverage), and recommends `/review` or `/ultrareview` escalation when the PR touches public schema or spans multiple packages. Always ends with an explicit "what this review did not cover" disclaimer so manual smoke gets handed off rather than implied. Use BEFORE submitting a self-review summary or before merging any PR. Triggers on `/self-review`, "self review", "ready to merge", "before merge", "pre-merge check".
---

# /self-review

Pre-merge ritual codified from observed failure modes during ADR 0002 phase A/B (#63, #67) and the graph/chip tooltip work (#61, #67). Three passes plus a coverage disclaimer.

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

## What this skill does NOT cover

This skill enforces the *checklist*. It does not replace:

- Manual UI smoke tests (run dev server, hover, click, watch latency, try keyboard nav).
- Load or soak testing under production-like data volume.
- The user's own validation on real dogfood — always preferred over self-claim of "looks good".
