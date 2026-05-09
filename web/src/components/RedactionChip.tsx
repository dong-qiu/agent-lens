import { useState } from "react";
import { createPortal } from "react-dom";

// RedactionChip renders the small "redacted" badges in EventCard with a
// fast custom tooltip — replaces the native `title` attribute that
// previously sat on these chips, which had the ~700ms browser-imposed
// delay flagged in auto-memory after PR #61 (graph node) and PR #67
// (token chip) hit the same UX miss. Same fix as TokenUsageChip: track
// cursor position in React state, render the popup via a portal so
// ancestor overflow:hidden can't clip it.
//
// Two variants: "thinking" (🚫, zinc) for thinking blocks Claude Code
// dropped from transcript before they reached the hook; "secret" (🔒,
// amber) for secrets the hook's rule-based redactor caught and
// substituted with [REDACTED:<type>]. Distinct icons + colors so audit
// readers can tell the upstream redactions apart.
type Variant = "thinking" | "secret";

const variants: Record<Variant, { icon: string; cls: string }> = {
  thinking: {
    icon: "🚫",
    cls: "bg-zinc-100 text-zinc-700 ring-zinc-300",
  },
  secret: {
    icon: "🔒",
    cls: "bg-amber-50 text-amber-800 ring-amber-300",
  },
};

export function RedactionChip({
  variant,
  label,
  tooltip,
}: {
  variant: Variant;
  label: string;
  tooltip: string;
}) {
  const [hover, setHover] = useState<{ x: number; y: number } | null>(null);
  const v = variants[variant];
  return (
    <>
      <span
        onMouseEnter={(e) => setHover({ x: e.clientX, y: e.clientY })}
        onMouseMove={(e) => setHover({ x: e.clientX, y: e.clientY })}
        onMouseLeave={() => setHover(null)}
        className={`inline-flex items-center gap-1 rounded px-1.5 py-0.5 text-[10px] font-medium ring-1 ${v.cls}`}
        aria-label={tooltip}
      >
        <span aria-hidden>{v.icon}</span>
        <span>{label}</span>
      </span>
      {hover &&
        createPortal(
          <div
            className="pointer-events-none fixed z-[9999] max-w-[360px] rounded bg-zinc-900 px-2 py-1.5 text-left text-[11px] text-white shadow-lg ring-1 ring-zinc-700"
            style={{
              left: hover.x + 14,
              top: hover.y + 14,
              wordBreak: "break-word",
              whiteSpace: "pre-wrap",
            }}
          >
            {tooltip}
          </div>,
          document.body,
        )}
    </>
  );
}
