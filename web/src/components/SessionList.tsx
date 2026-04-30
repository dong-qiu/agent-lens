import { useQuery } from "@tanstack/react-query";
import { gql, sessionsQuery } from "../api/client";
import type { Session, SessionsResponse } from "../types";
import { compactNum, tokenUsageTooltip } from "../lib/tokenUsage";

export function SessionList({
  onSelect,
}: {
  onSelect: (sessionId: string) => void;
}) {
  const { data, error, isLoading, isFetching } = useQuery({
    queryKey: ["sessions"],
    queryFn: () => gql<SessionsResponse>(sessionsQuery, { limit: 50 }),
    refetchInterval: 5000,
  });

  if (isLoading) {
    return <div className="text-sm text-zinc-500">Loading sessions…</div>;
  }
  if (error) {
    return (
      <div className="text-sm text-rose-600">
        Error: {(error as Error).message}
      </div>
    );
  }
  const sessions = data?.sessions ?? [];

  if (sessions.length === 0) {
    return (
      <div className="text-sm text-zinc-500">
        No sessions yet. Trigger a Claude Code prompt or post events to{" "}
        <code className="font-mono text-xs">/v1/events</code>.
      </div>
    );
  }

  return (
    <div>
      <div className="mb-3 flex items-baseline justify-between">
        <div className="text-sm text-zinc-700">
          <span className="font-medium">{sessions.length}</span> sessions
        </div>
        {isFetching && (
          <div className="text-xs text-zinc-400">refreshing…</div>
        )}
      </div>
      <ul className="divide-y divide-zinc-200 overflow-hidden rounded border border-zinc-200 bg-white">
        {sessions.map((s) => (
          <SessionRow key={s.id} session={s} onSelect={onSelect} />
        ))}
      </ul>
    </div>
  );
}

function SessionRow({
  session,
  onSelect,
}: {
  session: Session;
  onSelect: (id: string) => void;
}) {
  return (
    <li>
      <button
        type="button"
        onClick={() => onSelect(session.id)}
        className="flex w-full items-center justify-between gap-4 px-4 py-3 text-left transition hover:bg-zinc-50"
      >
        <div className="min-w-0">
          <div className="truncate font-mono text-sm text-zinc-900">
            {session.id}
          </div>
          <div className="mt-0.5 text-xs text-zinc-500">
            last activity {formatRelative(session.lastEventAt)} · started{" "}
            {formatAbsolute(session.firstEventAt)}
          </div>
        </div>
        <div className="flex shrink-0 items-center gap-2">
          {session.totalUsage && (
            <div
              className="inline-flex items-center gap-1 rounded bg-violet-50 px-2 py-0.5 text-[11px] font-medium text-violet-900 ring-1 ring-violet-200 font-mono"
              title={tokenUsageTooltip(session.totalUsage)}
            >
              <span aria-hidden>↑</span>
              <span>{compactNum(session.totalUsage.inputTokens)}</span>
              <span aria-hidden>↓</span>
              <span>{compactNum(session.totalUsage.outputTokens)}</span>
              {session.totalUsage.cacheReadTokens != null &&
                session.totalUsage.cacheReadTokens > 0 && (
                  <>
                    <span className="text-violet-400">·</span>
                    <span aria-label="cache read">◊</span>
                    <span>{compactNum(session.totalUsage.cacheReadTokens)}</span>
                  </>
                )}
            </div>
          )}
          <div className="rounded-full bg-zinc-100 px-2 py-0.5 text-xs font-medium text-zinc-700">
            {session.eventCount}{" "}
            {session.eventCount === 1 ? "event" : "events"}
          </div>
        </div>
      </button>
    </li>
  );
}

function formatRelative(iso: string): string {
  const t = new Date(iso).getTime();
  const diff = Date.now() - t;
  if (diff < 0) return "just now";
  const sec = Math.floor(diff / 1000);
  if (sec < 60) return `${sec}s ago`;
  const min = Math.floor(sec / 60);
  if (min < 60) return `${min}m ago`;
  const hr = Math.floor(min / 60);
  if (hr < 24) return `${hr}h ago`;
  const day = Math.floor(hr / 24);
  return `${day}d ago`;
}

function formatAbsolute(iso: string): string {
  const d = new Date(iso);
  return d.toLocaleString(undefined, {
    month: "short",
    day: "numeric",
    hour: "2-digit",
    minute: "2-digit",
  });
}
