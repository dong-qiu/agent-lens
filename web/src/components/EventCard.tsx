import { lazy, Suspense, useMemo, useState } from "react";
import type { Event } from "../types";
import { styleFor, formatTimestamp } from "./kindStyle";
import { payloadToDiff } from "../lib/payloadToDiff";
import { TokenUsageChip } from "./TokenUsageChip";

// React.lazy keeps the ~600 KB Monaco bundle out of the eager chunk:
// users who never expand a diff never pay for it. The dynamic import
// triggers Vite to emit Monaco as its own chunk loaded on first
// expansion.
const DiffView = lazy(() => import("./DiffView"));

export function EventCard({ event }: { event: Event }) {
  const [open, setOpen] = useState(false);
  const [showRaw, setShowRaw] = useState(false);
  const style = styleFor(event.kind);

  const summary = summarize(event);
  const hasPayload = event.payload && Object.keys(event.payload).length > 0;
  const diffs = useMemo(
    () =>
      event.kind === "TOOL_CALL"
        ? payloadToDiff(event.payload as Record<string, unknown> | null | undefined)
        : [],
    [event.kind, event.payload],
  );
  const expandable = hasPayload || diffs.length > 0;

  return (
    <div className={`rounded-lg border px-4 py-3 ${style.container}`}>
      <button
        type="button"
        className="flex w-full items-start justify-between gap-3 text-left"
        onClick={() => setOpen((o) => !o)}
        disabled={!expandable}
      >
        <div className="flex-1 min-w-0">
          <div className="flex items-center gap-2 flex-wrap">
            <span
              className={`inline-flex items-center gap-1 rounded px-2 py-0.5 text-[11px] font-semibold uppercase tracking-wider ${style.badge}`}
            >
              <span>{style.icon}</span>
              <span>{style.label}</span>
            </span>
            <span className="text-xs text-zinc-500 font-mono">
              {formatTimestamp(event.ts)}
            </span>
            <span className="text-xs text-zinc-600">
              {event.actor.type.toLowerCase()} · {event.actor.id}
              {event.actor.model ? ` · ${event.actor.model}` : ""}
            </span>
            {diffs.length > 0 && (
              <span className="inline-flex items-center gap-0.5 rounded bg-sky-100 px-1.5 py-0.5 text-[10px] font-medium text-sky-900 ring-1 ring-sky-300">
                ✎ diff{diffs.length > 1 ? ` ×${diffs.length}` : ""}
              </span>
            )}
            {(() => {
              const badge = authorizationBadge(event);
              return badge ? (
                <span
                  className={`inline-flex items-center gap-1 rounded px-1.5 py-0.5 text-[10px] font-medium ring-1 ${badge.cls}`}
                  title={badge.title}
                >
                  <span>{badge.icon}</span>
                  <span>{badge.label}</span>
                </span>
              ) : null;
            })()}
            {(() => {
              const n = redactedThinkingCount(event);
              return n > 0 ? (
                <span
                  className="inline-flex items-center gap-1 rounded bg-zinc-100 px-1.5 py-0.5 text-[10px] font-medium text-zinc-700 ring-1 ring-zinc-300"
                  title={`Claude Code dropped ${n} thinking block${n === 1 ? "" : "s"} from this message — only the signature was preserved in the transcript. Capturing the original thinking content requires §10.4 proxy mode.`}
                >
                  <span>🚫</span>
                  <span>
                    {n} thinking redacted
                  </span>
                </span>
              ) : null;
            })()}
            {event.usage && (
              <TokenUsageChip
                usage={event.usage}
                stopReason={event.stopReason}
              />
            )}
            {event.links?.length > 0 && (
              <span
                className="inline-flex items-center gap-0.5 rounded bg-white px-1.5 py-0.5 text-[10px] font-medium text-zinc-700 ring-1 ring-zinc-300"
                title={event.links
                  .map((l) =>
                    l.fromEvent === event.id
                      ? `→ ${l.toEvent} (${l.relation})`
                      : `← ${l.fromEvent} (${l.relation})`,
                  )
                  .join("\n")}
              >
                ↔ {event.links.length}
              </span>
            )}
          </div>
          {summary && (
            <div className="mt-1.5 text-sm text-zinc-800 break-words">
              {summary}
            </div>
          )}
          <div className="mt-1.5 text-[11px] text-zinc-400 font-mono truncate">
            {event.id} · hash {event.hash.slice(0, 12)}
          </div>
        </div>
        {expandable && (
          <span className="text-xs text-zinc-400 select-none mt-0.5 shrink-0">
            {open ? "▼" : "▶"}
          </span>
        )}
      </button>

      {open && diffs.length > 0 && (
        <div className="mt-3 space-y-2">
          <Suspense
            fallback={
              <div className="rounded border border-zinc-200 bg-white px-3 py-6 text-center text-xs text-zinc-500">
                Loading diff editor…
              </div>
            }
          >
            {diffs.map((slice, i) => (
              <DiffView key={`${slice.filePath}:${i}`} slice={slice} />
            ))}
          </Suspense>
          {hasPayload && (
            <button
              type="button"
              onClick={(e) => {
                e.stopPropagation();
                setShowRaw((s) => !s);
              }}
              className="text-[11px] text-zinc-500 underline-offset-2 hover:underline"
            >
              {showRaw ? "Hide" : "Show"} raw payload
            </button>
          )}
          {showRaw && hasPayload && (
            <pre className="overflow-x-auto rounded border border-zinc-200 bg-white/70 p-2 text-[11px] text-zinc-800">
              {JSON.stringify(event.payload, null, 2)}
            </pre>
          )}
        </div>
      )}

      {open && diffs.length === 0 && hasPayload && (
        <pre className="mt-3 overflow-x-auto rounded border border-zinc-200 bg-white/70 p-2 text-[11px] text-zinc-800">
          {JSON.stringify(event.payload, null, 2)}
        </pre>
      )}
    </div>
  );
}

function summarize(event: Event): string {
  const p = (event.payload ?? {}) as Record<string, unknown>;
  switch (event.kind) {
    case "PROMPT":
    case "THOUGHT":
      return clip(asString(p.text));
    case "TOOL_CALL":
    case "TOOL_RESULT":
      return asString(p.name);
    case "COMMIT": {
      const sha = asString(p.sha).slice(0, 7);
      const subject = asString(p.subject);
      const files = Array.isArray(p.files) ? p.files.length : 0;
      const filesPart = files > 0 ? ` · ${files} file${files === 1 ? "" : "s"}` : "";
      return [sha, subject].filter(Boolean).join(" · ") + filesPart;
    }
    case "DECISION": {
      const marker = asString(p.marker);
      const text = asString(p.text);
      return text ? `${marker} · ${clip(text)}` : marker;
    }
    case "REVIEW": {
      const review = (p.review ?? {}) as Record<string, unknown>;
      const action = asString(p.action);
      const state = asString(review.state);
      const body = asString(review.body);
      const head = [action, state].filter(Boolean).join(" · ");
      return body ? `${head} · ${clip(body)}` : head;
    }
    case "PUSH": {
      const ref = asString(p.ref).replace(/^refs\/(heads|tags)\//, "");
      const after = asString(p.after).slice(0, 7);
      const commits = Array.isArray(p.commits) ? p.commits.length : 0;
      const parts = [ref];
      if (after) parts.push(after);
      if (commits > 0) parts.push(`${commits} commit${commits === 1 ? "" : "s"}`);
      return parts.join(" · ");
    }
    case "DEPLOY": {
      const env = asString(p.environment);
      const status = asString(p.status);
      const platform = asString(p.platform);
      const sha = asString(p.git_sha).slice(0, 7);
      const parts = [env].filter(Boolean);
      if (status) parts.push(status);
      if (sha) parts.push(sha);
      if (platform) parts.push(platform);
      return parts.join(" · ");
    }
    case "BUILD": {
      // Two payload shapes: workflow_run webhook (nested workflow_run
      // object) vs. composite-action (flat fields with source flag).
      const run = (p.workflow_run ?? {}) as Record<string, unknown>;
      const isWebhook = Object.keys(run).length > 0;
      if (isWebhook) {
        const name = asString(run.name);
        const status = asString(run.status);
        const conclusion = asString(run.conclusion);
        const sha = asString(run.head_sha).slice(0, 7);
        const parts = [name].filter(Boolean);
        const state = conclusion || status;
        if (state) parts.push(state);
        if (sha) parts.push(sha);
        return parts.join(" · ");
      }
      const workflow = asString(p.workflow);
      const status = asString(p.status);
      const sha = asString(p.sha).slice(0, 7);
      const artifacts = Array.isArray(p.artifacts) ? p.artifacts.length : 0;
      const parts = [workflow].filter(Boolean);
      if (status) parts.push(status);
      if (sha) parts.push(sha);
      if (artifacts > 0) parts.push(`${artifacts} artifact${artifacts === 1 ? "" : "s"}`);
      return parts.join(" · ");
    }
    default:
      return "";
  }
}

function asString(v: unknown): string {
  return typeof v === "string" ? v : "";
}

function clip(s: string, n = 280): string {
  return s.length > n ? s.slice(0, n) + "…" : s;
}

// redactedThinkingCount reads the per-message count of `thinking`
// content blocks Claude Code dropped from the transcript. Set by the
// hook on `assistant_message` DECISION events; absent / 0 elsewhere.
function redactedThinkingCount(event: Event): number {
  if (event.kind !== "DECISION") return 0;
  const p = (event.payload ?? {}) as Record<string, unknown>;
  if (p.marker !== "assistant_message") return 0;
  const v = p.thinking_redacted_by_claude_code;
  return typeof v === "number" && v > 0 ? v : 0;
}

// authorizationBadge classifies a TOOL_CALL by the authorization
// context the hook attached at PreToolUse:
//   allowlist_match != null          → auto-allowed by policy (green)
//   allowlist_match == null + risk   → user-approved a risky call (red)
//   allowlist_match == null + clean  → user-approved interactively (yellow)
// PreToolUse only fires after Claude Code has granted permission, so a
// missing allowlist match means the user must have clicked yes in the
// permission UI; that's the audit-worthy event security cares about.
type Badge = { icon: string; label: string; cls: string; title: string };
function authorizationBadge(event: Event): Badge | null {
  if (event.kind !== "TOOL_CALL") return null;
  const p = (event.payload ?? {}) as Record<string, unknown>;
  const auth = p.authorization as
    | {
        allowlist_match?: string;
        risk_signals?: string[];
      }
    | undefined;
  if (!auth) return null;
  const matched = !!auth.allowlist_match;
  const risks = auth.risk_signals ?? [];
  if (matched && risks.length === 0) {
    return {
      icon: "🟢",
      label: "auto",
      cls: "bg-emerald-50 text-emerald-900 ring-emerald-200",
      title: `auto-allowed by:\n${auth.allowlist_match}`,
    };
  }
  if (!matched && risks.length === 0) {
    return {
      icon: "🟡",
      label: "user",
      cls: "bg-amber-50 text-amber-900 ring-amber-200",
      title: "no allowlist rule matched — user approved interactively",
    };
  }
  // !matched + risks (or matched+risks; treat any risk as red).
  return {
    icon: "🔴",
    label: `risky · ${matched ? "auto" : "user"}`,
    cls: "bg-rose-50 text-rose-900 ring-rose-300",
    title:
      `risk signals: ${risks.join(", ") || "(none)"}\n` +
      (matched
        ? `auto-allowed by: ${auth.allowlist_match}`
        : "no allowlist rule matched — user approved interactively"),
  };
}
