import { useEffect, useMemo, useRef, useState } from "react";
import { useQuery } from "@tanstack/react-query";
import { gql, eventsQuery } from "../api/client";
import type { EventsResponse } from "../types";
import { EventCard } from "./EventCard";
import { TokenUsageChip } from "./TokenUsageChip";
import {
  groupIntoTurns,
  summarizeTurn,
  formatDuration,
  turnBoundaries,
  type Turn,
  type TurnSummary,
  type Initiator,
  type BoundaryMark,
} from "../lib/turns";

// Per-initiator left-border colour + glyph: who drove the turn at a glance.
const initiatorStyle: Record<Initiator, { bar: string; icon: string }> = {
  human: { bar: "border-l-blue-400", icon: "👤" },
  agent: { bar: "border-l-amber-400", icon: "🤖" },
  subagent: { bar: "border-l-violet-400", icon: "🤖" },
  system: { bar: "border-l-zinc-300", icon: "⚙" },
};

// Open many turns by default only for short sessions; long ones start
// collapsed so the page is scannable.
const AUTO_OPEN_MAX = 8;

type Filter = "failures" | "produced" | "human";

// StoryTimeline groups the raw event stream into conversational turns so the
// session reads like a narrated transcript rather than event-by-event soup.
// Grouping + summarisation are pure (lib/turns.ts); expanding a turn drops to
// the existing EventCard for full fidelity.
export function StoryTimeline({ sessionId }: { sessionId: string }) {
  const { data, error, isLoading, isFetching } = useQuery({
    queryKey: ["events", sessionId, 5000],
    queryFn: () => gql<EventsResponse>(eventsQuery, { sessionId, limit: 5000 }),
    enabled: sessionId.length > 0,
    refetchInterval: 2000,
  });

  const turns = useMemo(() => groupIntoTurns(data?.events ?? []), [data?.events]);
  // Summaries are computed once here (not inside each card) so the toolbar can
  // filter on them.
  const summaries = useMemo(() => turns.map(summarizeTurn), [turns]);

  // Lifted open-state so the toolbar can expand/collapse all. Initialised once
  // when turns first arrive; refetch-appended turns stay collapsed so a 2 s
  // poll never clobbers the user's expand state.
  const [openKeys, setOpenKeys] = useState<Set<string>>(new Set());
  const inited = useRef(false);
  useEffect(() => {
    if (inited.current || turns.length === 0) return;
    inited.current = true;
    setOpenKeys(
      turns.length <= AUTO_OPEN_MAX
        ? new Set([turns[turns.length - 1].key])
        : new Set(),
    );
  }, [turns]);

  const setOpen = (key: string, val: boolean) =>
    setOpenKeys((prev) => {
      const next = new Set(prev);
      if (val) next.add(key);
      else next.delete(key);
      return next;
    });

  const [filters, setFilters] = useState<Set<Filter>>(new Set());
  const toggleFilter = (f: Filter) =>
    setFilters((prev) => {
      const next = new Set(prev);
      if (next.has(f)) next.delete(f);
      else next.add(f);
      return next;
    });

  // flashId briefly rings the event a "jump" landed on so it isn't lost.
  const [flashId, setFlashId] = useState<string | null>(null);
  const jumpTo = (turnKey: string, eventId: string) => {
    setOpen(turnKey, true);
    setFlashId(eventId);
    // Defer the scroll until the expanded body has mounted.
    window.setTimeout(() => {
      document
        .getElementById(`evt-${eventId}`)
        ?.scrollIntoView({ behavior: "smooth", block: "center" });
    }, 80);
    window.setTimeout(
      () => setFlashId((cur) => (cur === eventId ? null : cur)),
      1800,
    );
  };

  if (isLoading) return <div className="text-sm text-zinc-500">Loading…</div>;
  if (error)
    return (
      <div className="text-sm text-rose-600">Error: {(error as Error).message}</div>
    );
  if (!data) return null;

  const matches = (s: TurnSummary): boolean =>
    filters.size === 0 ||
    (filters.has("failures") && s.hasFailure) ||
    (filters.has("produced") && s.produced.length > 0) ||
    (filters.has("human") && s.initiator === "human");

  // Keep original turn numbers (#N) stable even when filtered.
  const rows = turns
    .map((turn, i) => ({ turn, summary: summaries[i], index: i + 1 }))
    .filter((r) => matches(r.summary));

  return (
    <div>
      <div className="mb-4 flex flex-wrap items-center gap-x-4 gap-y-2">
        <span className="text-sm text-zinc-700">
          <span className="font-medium">{turns.length}</span>{" "}
          {turns.length === 1 ? "turn" : "turns"}
          <span className="text-zinc-500"> · {data.events.length} events</span>
          {filters.size > 0 && (
            <span className="text-zinc-500"> · {rows.length} shown</span>
          )}
        </span>
        {isFetching && <span className="text-xs text-zinc-400">refreshing…</span>}

        <div className="flex items-center gap-1.5 text-xs">
          <FilterToggle active={filters.has("failures")} onClick={() => toggleFilter("failures")}>
            ✗ failures
          </FilterToggle>
          <FilterToggle active={filters.has("produced")} onClick={() => toggleFilter("produced")}>
            produced
          </FilterToggle>
          <FilterToggle active={filters.has("human")} onClick={() => toggleFilter("human")}>
            👤 human
          </FilterToggle>
        </div>

        {turns.length > 0 && (
          <div className="ml-auto flex items-center gap-2 text-xs">
            <button
              type="button"
              className="rounded border border-zinc-300 px-2 py-0.5 text-zinc-600 hover:bg-zinc-50"
              onClick={() => setOpenKeys(new Set(turns.map((t) => t.key)))}
            >
              Expand all
            </button>
            <button
              type="button"
              className="rounded border border-zinc-300 px-2 py-0.5 text-zinc-600 hover:bg-zinc-50"
              onClick={() => setOpenKeys(new Set())}
            >
              Collapse all
            </button>
          </div>
        )}
      </div>

      {turns.length === 0 ? (
        <div className="text-sm text-zinc-500">No events for this session yet.</div>
      ) : rows.length === 0 ? (
        <div className="text-sm text-zinc-500">No turns match the current filter.</div>
      ) : (
        <div className="space-y-3">
          {rows.map(({ turn, summary, index }) => {
            const bounds = turnBoundaries(turn);
            return (
              <div key={turn.key} className="space-y-3">
                <TurnCard
                  turn={turn}
                  summary={summary}
                  index={index}
                  open={openKeys.has(turn.key)}
                  flashId={flashId}
                  onToggle={() => setOpen(turn.key, !openKeys.has(turn.key))}
                  onJumpTo={(eventId) => jumpTo(turn.key, eventId)}
                />
                {bounds.map((b, i) => (
                  <SessionDivider key={`bound-${i}`} mark={b} />
                ))}
              </div>
            );
          })}
        </div>
      )}
    </div>
  );
}

// SessionDivider marks a session-episode boundary between turns — a closing
// SessionEnd (grey) or a reopening resume/clear/compact SessionStart (amber) —
// so a resumed/cleared session_id reads as distinct episodes. See ADR 0012.
function SessionDivider({ mark }: { mark: BoundaryMark }) {
  const isEnd = mark.tone === "end";
  return (
    <div className="flex items-center gap-2 py-0.5" role="separator" aria-label={mark.label}>
      <span className="h-px flex-1 bg-zinc-200" />
      <span
        className={`rounded-full px-2 py-0.5 text-[11px] font-medium ${
          isEnd ? "bg-zinc-100 text-zinc-500" : "bg-amber-50 text-amber-700"
        }`}
      >
        {isEnd ? "■" : "▸"} {mark.label}
      </span>
      <span className="h-px flex-1 bg-zinc-200" />
    </div>
  );
}

function FilterToggle({
  active,
  onClick,
  children,
}: {
  active: boolean;
  onClick: () => void;
  children: React.ReactNode;
}) {
  return (
    <button
      type="button"
      onClick={onClick}
      aria-pressed={active}
      className={`rounded-full border px-2 py-0.5 transition ${
        active
          ? "border-zinc-900 bg-zinc-900 text-white"
          : "border-zinc-300 bg-white text-zinc-600 hover:bg-zinc-50"
      }`}
    >
      {children}
    </button>
  );
}

function TurnCard({
  turn,
  summary: s,
  index,
  open,
  flashId,
  onToggle,
  onJumpTo,
}: {
  turn: Turn;
  summary: TurnSummary;
  index: number;
  open: boolean;
  flashId: string | null;
  onToggle: () => void;
  onJumpTo: (eventId: string) => void;
}) {
  const init = initiatorStyle[s.initiator];

  return (
    <div
      className={`rounded-lg border border-l-4 border-zinc-200 bg-white shadow-sm ${init.bar} ${
        s.initiator === "subagent" ? "ml-6" : ""
      }`}
    >
      <button
        type="button"
        onClick={onToggle}
        className="flex w-full items-start gap-3 px-4 py-3 text-left hover:bg-zinc-50/60"
      >
        <span className="mt-0.5 shrink-0 font-mono text-xs text-zinc-400">#{index}</span>
        <div className="min-w-0 flex-1">
          <div className="flex flex-wrap items-center gap-2 text-xs text-zinc-500">
            <span>
              {init.icon} {s.humanId ?? s.initiator}
            </span>
            <span aria-hidden>→</span>
            <span>🤖 {s.model ?? "agent"}</span>
            <span className="text-zinc-400">· {s.steps} steps</span>
            {s.durationMs != null && (
              <span className="text-zinc-400">· {formatDuration(s.durationMs)}</span>
            )}
            {s.usage && <TokenUsageChip usage={s.usage} />}
          </div>

          <div className="mt-1 break-words text-sm font-medium text-zinc-800">
            {s.title}
          </div>

          {s.highlights.length > 0 && (
            <div className="mt-1.5 flex flex-wrap gap-1.5">
              {s.highlights.map((h, j) => (
                <span
                  key={j}
                  role="button"
                  tabIndex={0}
                  title="jump to this step"
                  onClick={(e) => {
                    e.stopPropagation();
                    onJumpTo(h.firstEventId);
                  }}
                  onKeyDown={(e) => {
                    if (e.key === "Enter" || e.key === " ") {
                      e.preventDefault();
                      e.stopPropagation();
                      onJumpTo(h.firstEventId);
                    }
                  }}
                  className={`inline-flex cursor-pointer items-center gap-1 rounded px-1.5 py-0.5 text-[11px] ${
                    h.error
                      ? "bg-rose-100 text-rose-800 ring-1 ring-rose-300 hover:bg-rose-200"
                      : "bg-zinc-100 text-zinc-700 hover:bg-zinc-200"
                  }`}
                >
                  <span aria-hidden>{h.icon}</span>
                  <span>
                    {h.text}
                    {h.error ? " ✗" : ""}
                    {h.count > 1 ? ` ×${h.count}` : ""}
                  </span>
                </span>
              ))}
            </div>
          )}

          {s.produced.length > 0 && (
            <div className="mt-1.5 flex flex-wrap items-center gap-1.5">
              <span className="text-[11px] text-zinc-400">produced</span>
              {s.produced.map((p) => (
                <span
                  key={p.eventId}
                  role="button"
                  tabIndex={0}
                  title="jump to this event"
                  onClick={(e) => {
                    e.stopPropagation();
                    onJumpTo(p.eventId);
                  }}
                  onKeyDown={(e) => {
                    if (e.key === "Enter" || e.key === " ") {
                      e.preventDefault();
                      e.stopPropagation();
                      onJumpTo(p.eventId);
                    }
                  }}
                  className="inline-flex cursor-pointer items-center gap-1 rounded bg-emerald-100 px-1.5 py-0.5 text-[11px] font-medium text-emerald-900 ring-1 ring-emerald-300 hover:bg-emerald-200"
                >
                  <span aria-hidden>{p.icon}</span>
                  <span>{p.label}</span>
                </span>
              ))}
            </div>
          )}
        </div>
        <span className="mt-0.5 shrink-0 select-none text-xs text-zinc-400">
          {open ? "▼" : "▶"}
        </span>
      </button>

      {open && (
        <div className="space-y-2 border-t border-zinc-100 px-4 py-3">
          {turn.events.map((e) => (
            <div
              key={e.id}
              id={`evt-${e.id}`}
              className={
                flashId === e.id
                  ? "rounded-md ring-2 ring-amber-400 ring-offset-2"
                  : ""
              }
            >
              <EventCard event={e} />
            </div>
          ))}
        </div>
      )}
    </div>
  );
}
