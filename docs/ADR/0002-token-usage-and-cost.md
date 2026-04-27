# ADR 0002: Token usage in the evidence chain

- Status: Accepted
- Date: 2026-04-28
- Supersedes: —
- Amends: SPEC §5, §7, §10.1, §15, §17

## Context

SPEC v0.4 §10.1 declared, as a known limitation, that the Claude Code hook +
transcript path "does not capture token usage or stop_reason; switch to the
proxy deep-mode (§10.4) when those are needed." §17 repeated the claim. That
parked token accounting behind M4+ and made the proxy deep-mode the gating
dependency for any usage dashboard.

A new requirement: the auditable evidence chain must record token consumption
per human↔Coding-Agent turn.

Before accepting the cost of M4+ proxy work, we re-checked the transcript
itself. The result invalidates the §10.1 / §17 limitation.

## Validation

We inspected two real Claude Code transcripts under
`~/.claude/projects/-Users-dongqiu-Dev-code-agent-lens/` (both from this
repo's own development — same user, same model `claude-opus-4-7`, similar
workflow with extended thinking enabled):

- Small session (`2e5479d3-...jsonl`): **19 / 19** assistant messages carry a
  populated `message.usage`.
- Large session (`330c2b60-...jsonl`): **1080 / 1080** real assistant messages
  carry a populated `message.usage`. One additional `<synthetic>` message
  appears with all-zero usage and `stop_reason=stop_sequence` — easy to
  identify and skip.

`message.usage` contains every dimension we need for usage accounting:

| Field | Meaning |
|---|---|
| `input_tokens` | Non-cached input tokens |
| `output_tokens` | Output tokens |
| `cache_read_input_tokens` | Cache-hit reads |
| `cache_creation_input_tokens` | Total tokens written to cache |
| `cache_creation.ephemeral_5m_input_tokens` | 5-minute TTL portion |
| `cache_creation.ephemeral_1h_input_tokens` | 1-hour TTL portion |
| `service_tier` | `standard` / `priority` / `batch` |
| `server_tool_use.web_search_requests` | Server-side web-search invocations |
| `server_tool_use.web_fetch_requests` | Server-side web-fetch invocations |
| `iterations` | Sub-message iterations when `stop_reason=tool_use` |

Sibling fields `message.model` (e.g. `claude-opus-4-7`) and
`message.stop_reason` (`end_turn` / `tool_use` / `stop_sequence` / null) are
adjacent to `usage` and equally accessible. Both were also listed as "not
captured" by §10.1; both are in fact present.

Aggregate from the large session (1080 messages):

- input: 1,660
- output: 1,174,295
- cache write: 3,346,839
- **cache read: 416,961,148**

Cache-read dominates by two orders of magnitude. Any usage dashboard that
collapses cache reads into "input" would distort the picture by ~250×; the
breakdown matters even when no money is involved.

**Coverage caveat:** these two transcripts share user, model, and workflow
shape. Cross-project, cross-model (haiku / sonnet / non-Anthropic), and
no-cache-hit transcripts have not been exercised. The first M2-E
implementation should re-confirm both the `usage` schema and the
`<synthetic>` shape against transcripts from a different repo and at
least one different model before the observations below are treated as
universally true.

## Why not cost

We considered including a cost estimate (price table × usage). After review
we deferred it indefinitely:

- Vendor pricing differs in structure, not just numbers: Anthropic bills
  cache writes with TTL-dependent multipliers; OpenAI bills cached input as
  a flat discount; per-call billing for server-side tools (web_search /
  web_fetch) has no analogue on most other vendors. A unified `$` figure
  hides decisions an auditor would want to see.
- Maintaining a multi-vendor price table is operational work (rate changes,
  new tiers, regional differences) that distracts from Agent Lens's core
  job — recording what happened.
- Token counts are the audit-relevant primitive. Cost is a derived,
  organisation-specific concern (billing accounts, negotiated rates, BYOK
  vs API) that downstream tooling can compute against the raw counts when
  it actually needs to.

The capture path stays cost-ready: events store everything a future cost
calculation would need (tokens by dimension, model, service_tier, vendor).
Re-introducing cost is a query/UI/config layer addition, not a schema
change.

Reconsider this decision if downstream tooling consistently demands a
unified cost view that the raw-token contract cannot satisfy — for
example, if auditors need an `est_cost_usd` column they cannot compute
themselves, or if multi-tenant deployments need centrally-governed price
tables. The "out of scope" framing here is operational (avoid building a
distracting price-table service in v1), not principled.

## Decision

### D1. Embed usage in existing event payloads, do not introduce a new EventKind.

Each assistant-message-derived event (`DECISION` for the `text` block,
`THOUGHT` for the `thinking` block) carries an optional `usage` sub-object in
its payload. Turn-level and session-level totals are computed in the query
layer, not stored as separate events.

**Rejected alternative:** a dedicated `EVENT_KIND_USAGE`. Usage is metadata
*about* a message, not a behaviour in its own right. A dedicated kind would
require an extra `Link` back to the producing message and bloat the event
stream by ~2× for no consumer benefit.

### D2. Standardize a vendor-neutral `TokenUsage` shape. Vendor-specific schemas are normalised at ingest.

```
TokenUsage {
  vendor:                  "anthropic" | "openai" | ...
  model:                   string                       // raw vendor model id
  service_tier?:           string
  input_tokens:            int
  output_tokens:           int
  cache_read_tokens?:      int
  cache_write_5m_tokens?:  int                          // anthropic-specific today
  cache_write_1h_tokens?:  int                          // anthropic-specific today
  web_search_calls?:       int
  web_fetch_calls?:        int
  raw?:                    object                       // verbatim vendor block, for forensic re-parse
}
```

Claude Code mapping (from `message.usage`):

- `input_tokens`            → `input_tokens`
- `output_tokens`           → `output_tokens`
- `cache_read_input_tokens` → `cache_read_tokens`
- `cache_creation.ephemeral_5m_input_tokens` → `cache_write_5m_tokens`
- `cache_creation.ephemeral_1h_input_tokens` → `cache_write_1h_tokens`
- `server_tool_use.web_search_requests`      → `web_search_calls`
- `server_tool_use.web_fetch_requests`       → `web_fetch_calls`
- whole `usage` block                         → `raw` (so future re-derivation
  is possible if we discover we under-captured a field)

The optional fields are frankly Anthropic-shaped today. When OpenCode /
Cursor / OpenAI vendors land, we extend the shape rather than coerce
foreign concepts into these names — adding e.g. `cached_tokens` for
OpenAI's flat-discount model, kept under the same `TokenUsage` umbrella.

`raw` is intentionally redundant. It costs a few hundred bytes per event and
buys us insurance against vendor schema drift and our own normalisation
mistakes.

### D3. Messages without usable usage are treated as metadata-only and logged at INFO.

The `<synthetic>` model marker (Claude Code's own injected stop-sequence,
observed once in the validation sample with an all-zero usage block) is the
prompting case, but the rule generalises. If any of the following hold —
`message.model == "<synthetic>"`, `message.usage` absent, or every numeric
field in `usage` is zero — treat the message as metadata-only: emit any
non-usage event content normally, skip the `TokenUsage` extraction, and
**log at INFO with the offending shape so we can revisit if the assumption
breaks**. Do not fail-error and do not drop the event.

The `<synthetic>` shape is observed n=1 today; INFO-level logging is the
explicit hedge against silently dropping richer data if a future Claude
Code version starts populating `usage` for these messages.

## Scope (this ADR)

This ADR is documentation only. It updates SPEC and records decisions. The
concrete implementation lands in a follow-up milestone (M2-E or M3 sub-item):

- transcript usage extraction in `internal/transcript/`
- `payload.usage` shape contract (no proto enum changes needed; payload is
  `google.protobuf.Struct`)
- GraphQL exposure: `Event.usage`, `Turn.totalUsage`, `Session.totalUsage`
  resolvers
- Lens UI: per-turn token breakdown, session-level totals

No code changes ship with this ADR.

## Consequences

- §10.1 / §17 limitation language is wrong and is being removed in the same
  changeset. Hook path is now declared as the source of truth for usage and
  stop_reason; §10.4 proxy deep-mode is no longer the gating dependency for
  usage features.
- The v1 evidence chain gains a token-usage dimension without adding M4+
  scope.
- Cost / pricing is explicitly out of scope. Re-introducing it later is a
  pure additive change (price-table config + query/UI layer); no event
  schema migration would be required.
- Cross-vendor support (OpenCode, Cursor, custom agents) is now schema-ready
  via D2 but each new vendor still needs a mapping function. That work is
  per-integration and tracked as part of §10.2 / §10.3.
- Attestation-predicate inclusion of token totals is intentionally NOT
  decided here. The data is captured in events and turn-level totals are
  computable from the query layer; whoever next revises
  `agent-lens.dev/code-provenance/v1` (shipped in M3-B-2) or its successors
  picks what to embed and whether to extend v1 vs. bump to v2. That call
  belongs to the attestation-revision PR, not this ADR.
