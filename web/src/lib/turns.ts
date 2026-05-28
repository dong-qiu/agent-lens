import type { Event, TokenUsage } from "../types";
import { parsePrompt } from "./prompts";

// A Turn is one conversational exchange: a human prompt and everything the
// agent did in response, up to the next prompt.
export interface Turn {
  key: string; // stable React key (first event id)
  events: Event[];
  prompt: Event | null; // the PROMPT that opened the turn, if any
}

// groupIntoTurns folds an append-ordered event list into turns. Claude Code
// does NOT populate turn_id (it's null on the wire — verified against real
// dogfood capture), so the boundary is the human PROMPT: a turn runs from one
// PROMPT up to (but not including) the next. Events before the first prompt —
// SessionStart markers, config snapshots — form a leading group.
export function groupIntoTurns(events: Event[]): Turn[] {
  const turns: Turn[] = [];
  let cur: Event[] = [];
  const flush = () => {
    if (cur.length === 0) return;
    turns.push({
      key: cur[0].id,
      events: cur,
      prompt: cur.find((e) => e.kind === "PROMPT") ?? null,
    });
    cur = [];
  };
  for (const e of events) {
    if (e.kind === "PROMPT" && cur.length > 0) flush();
    cur.push(e);
  }
  flush();
  return turns;
}

// Highlight is one notable step in a turn's story line, with a repeat count
// (e.g. "🔧 Edit ×2"). error marks a tool whose result reported failure;
// firstEventId is the event the chip links to (first occurrence).
export interface Highlight {
  icon: string;
  text: string;
  count: number;
  error?: boolean;
  firstEventId: string;
}

// Who drove the turn — drives the card's colour + indent.
export type Initiator = "human" | "agent" | "subagent" | "system";

// Produced is a downstream artifact a turn caused (commit / PR / push / build
// / deploy), surfaced on its own row and click-to-scroll to the event.
export interface Produced {
  icon: string;
  label: string;
  eventId: string;
}

export interface TurnSummary {
  title: string;
  initiator: Initiator;
  humanId: string | null;
  model: string | null;
  steps: number; // meaningful actions (excludes tool_result + structural markers + the opening prompt)
  durationMs: number | null;
  usage: TokenUsage | null;
  highlights: Highlight[];
  produced: Produced[];
  hasFailure: boolean;
}

const asString = (v: unknown): string => (typeof v === "string" ? v : "");

// Prompt classification (human vs system-injected block) is shared with the
// EventCard via lib/prompts.ts.

// isErrorResult reads a tool_result payload for a clear failure signal.
// Conservative: only the structured markers Claude Code sets, plus an
// error-prefixed string — so a successful tool whose output merely mentions
// "error" isn't mislabelled.
function isErrorResult(pl: Record<string, unknown>): boolean {
  const r = pl.response;
  if (r && typeof r === "object") {
    const o = r as Record<string, unknown>;
    if (o.is_error === true || o.success === false) return true;
  }
  if (typeof r === "string" && /^\s*error\b|\berror:/i.test(r)) return true;
  return false;
}

// isMeaningfulStep excludes plumbing from the step count: tool_result (paired
// with its call), the opening prompt (shown as the title), and the structural
// session_start / turn_end decision markers.
function isMeaningfulStep(e: Event): boolean {
  if (e.kind === "TOOL_RESULT" || e.kind === "PROMPT") return false;
  if (e.kind === "DECISION") {
    const pl = (e.payload ?? {}) as Record<string, unknown>;
    const m = asString(pl.marker);
    if (m === "session_start" || m === "turn_end") return false;
    // Empty assistant_message events are synthesized metadata carriers
    // (redacted-thinking / usage), not actions — same rule as the reply chip.
    if (m === "assistant_message" && asString(pl.text).trim() === "") return false;
  }
  return true;
}

export function formatDuration(ms: number): string {
  const s = Math.round(ms / 1000);
  if (s < 1) return "<1s";
  if (s < 60) return `${s}s`;
  const m = Math.floor(s / 60);
  const rs = s % 60;
  if (m < 60) return rs ? `${m}m ${rs}s` : `${m}m`;
  const h = Math.floor(m / 60);
  const rm = m % 60;
  return rm ? `${h}h ${rm}m` : `${h}h`;
}

// summarizeTurn derives the header + story line shown on a collapsed turn —
// what happened, who drove it, what it produced — without expanding to raw
// events.
export function summarizeTurn(turn: Turn): TurnSummary {
  const { events, prompt } = turn;
  const payloadOf = (e: Event) => (e.payload ?? {}) as Record<string, unknown>;
  const first = events[0];
  const firstMarker = asString(payloadOf(first).marker);

  const promptInfo = prompt
    ? parsePrompt(asString(payloadOf(prompt).text))
    : null;

  const initiator: Initiator = promptInfo
    ? promptInfo.human
      ? "human"
      : "system" // injected notification/reminder — not a human turn
    : firstMarker === "subagent_start"
      ? "subagent"
      : events.some((e) => e.actor.type === "AGENT")
        ? "agent"
        : "system";

  const humanId = promptInfo?.human ? (prompt as Event).actor.id : null;
  const model =
    events.find((e) => e.actor.type === "AGENT" && e.actor.model)?.actor.model ??
    null;

  // Ordered, de-duplicated highlight list. bump() merges repeats (same key)
  // into a count, keeps first-appearance order, and records the event the
  // chip links to (first occurrence).
  const order: string[] = [];
  const byKey = new Map<string, Highlight>();
  const bump = (
    key: string,
    icon: string,
    text: string,
    eventId: string,
    error?: boolean,
  ) => {
    const existing = byKey.get(key);
    if (existing) {
      existing.count++;
      return;
    }
    byKey.set(key, { icon, text, count: 1, error, firstEventId: eventId });
    order.push(key);
  };

  const produced: Produced[] = [];

  for (let i = 0; i < events.length; i++) {
    const e = events[i];
    const pl = payloadOf(e);
    switch (e.kind) {
      case "PROMPT":
        break; // shown as the title
      case "THOUGHT":
        bump("thinking", "💭", "thinking", e.id);
        break;
      case "TOOL_CALL": {
        const next = events[i + 1];
        const errored =
          !!next && next.kind === "TOOL_RESULT" && isErrorResult(payloadOf(next));
        const tag = errored ? "err" : "ok";
        const skill = pl.skill as Record<string, unknown> | undefined;
        if (skill && typeof skill.name === "string") {
          bump(`skill:${skill.name}:${tag}`, "⌘", skill.name, e.id, errored);
        } else {
          const name = asString(pl.name) || "tool";
          bump(`tool:${name}:${tag}`, "🔧", name, e.id, errored);
        }
        break;
      }
      case "TOOL_RESULT":
        break; // folded into the call above
      case "DECISION": {
        const marker = asString(pl.marker);
        // Only count real replies. Empty assistant_message events are
        // synthesized carriers for redacted-thinking / usage metadata
        // (transcript reader.go), not actual agent prose.
        if (marker === "assistant_message" && asString(pl.text).trim() !== "")
          bump("reply", "💬", "reply", e.id);
        else if (marker === "subagent_start")
          bump(`sub:${asString(pl.agent_type)}`, "🤖", asString(pl.agent_type) || "sub-agent", e.id);
        break; // session_start / turn_end are structural — omit
      }
      case "REVIEW":
        bump(`review:${e.id}`, "👁", asString(pl.action) || "review", e.id);
        break;
      case "CODE_CHANGE":
        bump("diff", "✎", "diff", e.id);
        break;
      // Downstream artifacts get their own "produced" row, not a story chip.
      case "COMMIT":
        produced.push({ icon: "📦", label: asString(pl.sha).slice(0, 7) || "commit", eventId: e.id });
        break;
      case "PR": {
        const n = pl.number;
        produced.push({ icon: "⇪", label: typeof n === "number" ? `#${n}` : "PR", eventId: e.id });
        break;
      }
      case "PUSH":
        produced.push({ icon: "↥", label: asString(pl.ref).replace(/^refs\/(heads|tags)\//, "") || "push", eventId: e.id });
        break;
      case "BUILD":
        produced.push({ icon: "🛠", label: asString(pl.workflow) || "build", eventId: e.id });
        break;
      case "DEPLOY":
        produced.push({ icon: "🚀", label: asString(pl.environment) || "deploy", eventId: e.id });
        break;
    }
  }

  const highlights = order.map((k) => byKey.get(k) as Highlight);
  const title = promptInfo
    ? promptInfo.title
    : nonHumanTitle(initiator, firstMarker, payloadOf(first), highlights, produced);

  return {
    title,
    initiator,
    humanId,
    model,
    steps: events.filter(isMeaningfulStep).length,
    durationMs: turnDuration(events),
    usage: sumUsage(events),
    highlights,
    produced,
    hasFailure: highlights.some((h) => h.error),
  };
}

// nonHumanTitle builds an informative label for turns with no opening prompt,
// instead of a bare "session start".
function nonHumanTitle(
  initiator: Initiator,
  firstMarker: string,
  firstPayload: Record<string, unknown>,
  highlights: Highlight[],
  produced: Produced[],
): string {
  if (initiator === "subagent") {
    const t = asString(firstPayload.agent_type);
    return `sub-agent${t ? `: ${t}` : ""}`;
  }
  if (initiator === "system") {
    return firstMarker === "session_start" ? "session start" : "system";
  }
  // agent: summarize what it did
  const toolN = highlights
    .filter((h) => h.icon === "🔧" || h.icon === "⌘")
    .reduce((a, h) => a + h.count, 0);
  const bits: string[] = [];
  if (toolN) bits.push(`${toolN} tool${toolN > 1 ? "s" : ""}`);
  if (highlights.some((h) => h.text === "reply")) bits.push("reply");
  for (const p of produced) bits.push(`${p.icon} ${p.label}`);
  return bits.length ? `agent · ${bits.join(" · ")}` : "agent activity";
}

function turnDuration(events: Event[]): number | null {
  if (events.length < 2) return null;
  const a = new Date(events[0].ts).getTime();
  const b = new Date(events[events.length - 1].ts).getTime();
  if (isNaN(a) || isNaN(b) || b < a) return null;
  return b - a;
}

// sumUsage aggregates per-event token counters across a turn. Mirrors the
// server-side aggregateSessionUsage / Timeline's aggregateUsage: numeric
// counters sum; vendor/model/tier collapse to a single value only when the
// turn is homogeneous.
function sumUsage(events: Event[]): TokenUsage | null {
  let any = false;
  let input = 0,
    output = 0,
    cacheR = 0,
    c5 = 0,
    c1 = 0,
    ws = 0,
    wf = 0;
  const vendors = new Set<string>();
  const models = new Set<string>();
  const tiers = new Set<string>();
  for (const e of events) {
    if (!e.usage) continue;
    any = true;
    input += e.usage.inputTokens;
    output += e.usage.outputTokens;
    if (e.usage.cacheReadTokens) cacheR += e.usage.cacheReadTokens;
    if (e.usage.cacheWrite5mTokens) c5 += e.usage.cacheWrite5mTokens;
    if (e.usage.cacheWrite1hTokens) c1 += e.usage.cacheWrite1hTokens;
    if (e.usage.webSearchCalls) ws += e.usage.webSearchCalls;
    if (e.usage.webFetchCalls) wf += e.usage.webFetchCalls;
    if (e.usage.vendor) vendors.add(e.usage.vendor);
    if (e.usage.model) models.add(e.usage.model);
    if (e.usage.serviceTier) tiers.add(e.usage.serviceTier);
  }
  if (!any) return null;
  return {
    vendor: vendors.size === 1 ? [...vendors][0] : "",
    model: models.size === 1 ? [...models][0] : "",
    serviceTier: tiers.size === 1 ? [...tiers][0] : null,
    inputTokens: input,
    outputTokens: output,
    cacheReadTokens: cacheR || null,
    cacheWrite5mTokens: c5 || null,
    cacheWrite1hTokens: c1 || null,
    webSearchCalls: ws || null,
    webFetchCalls: wf || null,
  };
}
