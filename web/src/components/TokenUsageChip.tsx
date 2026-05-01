import { useState } from "react";
import { createPortal } from "react-dom";
import type { TokenUsage } from "../types";
import {
  compactNum,
  tokenUsageAriaLabel,
  tokenUsageTooltip,
} from "../lib/tokenUsage";

// TokenUsageChip is the per-event / per-session violet token-count
// pill plus its hover popup. Replaces the native `title` attribute
// previously used inline at three call sites — that path had a
// ~700 ms browser delay and reset on every micro-mouse-move, making
// the audit breakdown feel sluggish or invisible.
//
// The popup is rendered to document.body via a portal so it can't be
// clipped by ancestors with `overflow: hidden` (SessionList's row
// container, Timeline's sticky header, etc.); position: fixed pegs it
// to the cursor's viewport coordinates.
export function TokenUsageChip({
  usage,
  stopReason,
  ariaLabelPrefix,
  caveatNote,
}: {
  usage: TokenUsage;
  stopReason?: string | null;
  /** Prepended to the screen-reader label, e.g. "session total: ". */
  ariaLabelPrefix?: string;
  /** Appended to the visible tooltip body, e.g. partial-total caveat. */
  caveatNote?: string;
}) {
  const [hover, setHover] = useState<{ x: number; y: number } | null>(null);
  const body = tokenUsageTooltip(usage, stopReason);
  const tooltipText = caveatNote ? `${body}\n\n${caveatNote}` : body;
  return (
    <>
      <span
        onMouseEnter={(e) => setHover({ x: e.clientX, y: e.clientY })}
        onMouseMove={(e) => setHover({ x: e.clientX, y: e.clientY })}
        onMouseLeave={() => setHover(null)}
        className="inline-flex items-center gap-1 rounded bg-violet-50 px-1.5 py-0.5 text-[10px] font-medium text-violet-900 ring-1 ring-violet-200 font-mono"
        aria-label={(ariaLabelPrefix ?? "") + tokenUsageAriaLabel(usage)}
      >
        <span aria-hidden>↑</span>
        <span>{compactNum(usage.inputTokens)}</span>
        <span aria-hidden>↓</span>
        <span>{compactNum(usage.outputTokens)}</span>
        {usage.cacheReadTokens != null && usage.cacheReadTokens > 0 && (
          <>
            <span className="text-violet-400">·</span>
            <span aria-label="cache read">◊</span>
            <span>{compactNum(usage.cacheReadTokens)}</span>
          </>
        )}
      </span>
      {hover &&
        createPortal(
          <div
            className="pointer-events-none fixed z-[9999] max-w-[320px] rounded bg-zinc-900 px-2 py-1.5 text-left text-[11px] text-white shadow-lg ring-1 ring-zinc-700"
            style={{
              left: hover.x + 14,
              top: hover.y + 14,
              wordBreak: "break-all",
              whiteSpace: "pre-wrap",
            }}
          >
            {tooltipText}
          </div>,
          document.body,
        )}
    </>
  );
}
