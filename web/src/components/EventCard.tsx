import { useState } from "react";
import type { Event } from "../types";
import { styleFor, formatTimestamp } from "./kindStyle";

export function EventCard({ event }: { event: Event }) {
  const [open, setOpen] = useState(false);
  const style = styleFor(event.kind);

  const summary = summarize(event);
  const hasPayload = event.payload && Object.keys(event.payload).length > 0;

  return (
    <div className={`rounded-lg border px-4 py-3 ${style.container}`}>
      <button
        type="button"
        className="flex w-full items-start justify-between gap-3 text-left"
        onClick={() => setOpen((o) => !o)}
        disabled={!hasPayload}
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
        {hasPayload && (
          <span className="text-xs text-zinc-400 select-none mt-0.5 shrink-0">
            {open ? "▼" : "▶"}
          </span>
        )}
      </button>

      {open && hasPayload && (
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
