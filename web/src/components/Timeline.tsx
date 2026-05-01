import { useMemo, useState } from "react";
import { useQuery } from "@tanstack/react-query";
import { gql, eventsQuery } from "../api/client";
import type { Event, EventKind, EventsResponse, TokenUsage } from "../types";
import { EventCard } from "./EventCard";
import { styleFor } from "./kindStyle";
import {
  compactNum,
  tokenUsageAriaLabel,
  tokenUsageTooltip,
} from "../lib/tokenUsage";

export function Timeline({ sessionId }: { sessionId: string }) {
  const [activeKinds, setActiveKinds] = useState<Set<EventKind>>(new Set());

  const { data, error, isLoading, isFetching } = useQuery({
    queryKey: ["events", sessionId],
    queryFn: () => gql<EventsResponse>(eventsQuery, { sessionId, limit: 200 }),
    enabled: sessionId.length > 0,
    refetchInterval: 2000,
  });

  const counts = useMemo(() => countByKind(data?.events ?? []), [data?.events]);
  // Partial-total: client aggregation across only the events fetched
  // (limit=200). For sessions ≤ 200 events this is exact; for larger
  // ones it under-counts. Truth-of-record `Session.totalUsage` is on
  // the SessionList row.
  const usageTotal = useMemo(
    () => aggregateUsage(data?.events ?? []),
    [data?.events],
  );

  const visible = useMemo(() => {
    if (!data) return [];
    if (activeKinds.size === 0) return data.events;
    return data.events.filter((e) => activeKinds.has(e.kind));
  }, [data, activeKinds]);

  if (!sessionId) {
    return (
      <div className="text-sm text-zinc-500">
        Enter a session id above to view its timeline.
      </div>
    );
  }
  if (isLoading) {
    return <div className="text-sm text-zinc-500">Loading…</div>;
  }
  if (error) {
    return (
      <div className="text-sm text-rose-600">
        Error: {(error as Error).message}
      </div>
    );
  }
  if (!data) return null;

  const toggle = (k: EventKind) =>
    setActiveKinds((prev) => {
      const next = new Set(prev);
      if (next.has(k)) next.delete(k);
      else next.add(k);
      return next;
    });

  return (
    <div>
      <div className="sticky top-0 -mx-6 mb-4 border-b border-zinc-200 bg-zinc-50/90 px-6 py-3 backdrop-blur z-10">
        <div className="flex flex-wrap items-center gap-x-4 gap-y-2">
          <div className="text-sm text-zinc-700">
            <span className="font-medium">{data.events.length}</span> events
            {activeKinds.size > 0 && (
              <span className="text-zinc-500"> · {visible.length} shown</span>
            )}
          </div>
          <div className="text-xs text-zinc-500 font-mono">
            head {data.sessionHead ? data.sessionHead.slice(0, 16) : "(empty)"}
          </div>
          {usageTotal && (
            <div
              className="inline-flex items-center gap-1 rounded bg-violet-50 px-2 py-0.5 text-[11px] font-medium text-violet-900 ring-1 ring-violet-200 font-mono"
              title={tokenUsageTooltip(usageTotal) + "\n\n(across fetched events; SessionList shows session truth-of-record)"}
              aria-label={
                "fetched-events total: " + tokenUsageAriaLabel(usageTotal)
              }
            >
              <span aria-hidden>↑</span>
              <span>{compactNum(usageTotal.inputTokens)}</span>
              <span aria-hidden>↓</span>
              <span>{compactNum(usageTotal.outputTokens)}</span>
              {usageTotal.cacheReadTokens != null &&
                usageTotal.cacheReadTokens > 0 && (
                  <>
                    <span className="text-violet-400">·</span>
                    <span aria-label="cache read">◊</span>
                    <span>{compactNum(usageTotal.cacheReadTokens)}</span>
                  </>
                )}
            </div>
          )}
          {isFetching && (
            <div className="text-xs text-zinc-400">refreshing…</div>
          )}
          <div className="flex flex-wrap gap-1.5 ml-auto">
            {(Object.keys(counts) as EventKind[]).map((k) => (
              <FilterChip
                key={k}
                kind={k}
                count={counts[k] ?? 0}
                active={activeKinds.has(k)}
                onClick={() => toggle(k)}
              />
            ))}
            {activeKinds.size > 0 && (
              <button
                type="button"
                className="text-xs text-zinc-500 underline-offset-2 hover:underline"
                onClick={() => setActiveKinds(new Set())}
              >
                clear
              </button>
            )}
          </div>
        </div>
      </div>

      {visible.length === 0 ? (
        <div className="text-sm text-zinc-500">
          {data.events.length === 0
            ? "No events for this session yet."
            : "No events match the current filter."}
        </div>
      ) : (
        <Rail events={visible} />
      )}
    </div>
  );
}

function Rail({ events }: { events: Event[] }) {
  return (
    <div className="relative pl-8">
      <div className="absolute left-3 top-2 bottom-2 w-px bg-zinc-200" />
      <div className="space-y-2">
        {events.map((e) => {
          const style = styleFor(e.kind);
          return (
            <div key={e.id} className="relative">
              <span
                className={`absolute -left-[22px] top-3 h-3 w-3 rounded-full ring-4 ${style.dot}`}
                aria-hidden
              />
              <EventCard event={e} />
            </div>
          );
        })}
      </div>
    </div>
  );
}

function FilterChip({
  kind,
  count,
  active,
  onClick,
}: {
  kind: EventKind;
  count: number;
  active: boolean;
  onClick: () => void;
}) {
  const style = styleFor(kind);
  return (
    <button
      type="button"
      onClick={onClick}
      className={`inline-flex items-center gap-1 rounded-full border px-2 py-0.5 text-[11px] font-medium transition ${
        active
          ? `${style.badge} border-current shadow-sm`
          : "border-zinc-200 bg-white text-zinc-600 hover:bg-zinc-50"
      }`}
      aria-pressed={active}
    >
      <span>{style.icon}</span>
      <span className="lowercase">{style.label}</span>
      <span className="text-zinc-400">{count}</span>
    </button>
  );
}

function countByKind(events: Event[]): Partial<Record<EventKind, number>> {
  const out: Partial<Record<EventKind, number>> = {};
  for (const e of events) {
    out[e.kind] = (out[e.kind] ?? 0) + 1;
  }
  return out;
}

// aggregateUsage sums per-message token counters across the events the
// caller has on hand. Mirrors the server-side `aggregateSessionUsage`
// shape but operates on already-fetched events; see Timeline's caveat
// on partial totals.
function aggregateUsage(events: Event[]): TokenUsage | null {
  let any = false;
  let inputT = 0;
  let outputT = 0;
  let cacheR = 0;
  let cache5m = 0;
  let cache1h = 0;
  let webSearch = 0;
  let webFetch = 0;
  const vendors = new Set<string>();
  const models = new Set<string>();
  const tiers = new Set<string>();
  for (const e of events) {
    if (!e.usage) continue;
    any = true;
    inputT += e.usage.inputTokens;
    outputT += e.usage.outputTokens;
    if (e.usage.cacheReadTokens) cacheR += e.usage.cacheReadTokens;
    if (e.usage.cacheWrite5mTokens) cache5m += e.usage.cacheWrite5mTokens;
    if (e.usage.cacheWrite1hTokens) cache1h += e.usage.cacheWrite1hTokens;
    if (e.usage.webSearchCalls) webSearch += e.usage.webSearchCalls;
    if (e.usage.webFetchCalls) webFetch += e.usage.webFetchCalls;
    if (e.usage.vendor) vendors.add(e.usage.vendor);
    if (e.usage.model) models.add(e.usage.model);
    if (e.usage.serviceTier) tiers.add(e.usage.serviceTier);
  }
  if (!any) return null;
  return {
    vendor: vendors.size === 1 ? [...vendors][0] : "",
    model: models.size === 1 ? [...models][0] : "",
    serviceTier: tiers.size === 1 ? [...tiers][0] : null,
    inputTokens: inputT,
    outputTokens: outputT,
    cacheReadTokens: cacheR > 0 ? cacheR : null,
    cacheWrite5mTokens: cache5m > 0 ? cache5m : null,
    cacheWrite1hTokens: cache1h > 0 ? cache1h : null,
    webSearchCalls: webSearch > 0 ? webSearch : null,
    webFetchCalls: webFetch > 0 ? webFetch : null,
  };
}
