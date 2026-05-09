import { useState } from "react";
import { createPortal } from "react-dom";

// AgentDispatchChip surfaces sub-agent (Task tool / Agent tool)
// dispatch metadata on parent-side TOOL_RESULT events. v0.1 ships
// without automatic parent→child linking (per ADR 0008): audit
// readers correlate manually via timestamp + prompt text. This chip
// is the audit reader's "you are looking at an agent dispatch"
// signal — without it the dispatch hides as a generic TOOL_RESULT.
//
// Same fast-tooltip pattern as RedactionChip + TokenUsageChip:
// React state for cursor position + portal-rendered popup, no native
// `title` (which has the ~700 ms browser delay flagged in #61 / #67).
export function AgentDispatchChip({
  agentId,
  status,
  outputFile,
  prompt,
}: {
  agentId: string;
  status?: string;
  outputFile?: string;
  prompt?: string;
}) {
  const [hover, setHover] = useState<{ x: number; y: number } | null>(null);
  const short = agentId.slice(0, 8);
  const lines = [`agent: ${agentId}`];
  if (status) lines.push(`status: ${status}`);
  if (outputFile) lines.push(`output: ${outputFile}`);
  if (prompt) {
    const promptPreview =
      prompt.length > 200 ? prompt.slice(0, 200) + "…" : prompt;
    lines.push("");
    lines.push("prompt preview:");
    lines.push(promptPreview);
  }
  lines.push("");
  lines.push(
    "v0.1: parent→child auto-link is deferred (ADR 0008). " +
      "Use timestamp + prompt to correlate with the child session captured under its own UUID.",
  );
  const tooltip = lines.join("\n");

  return (
    <>
      <span
        onMouseEnter={(e) => setHover({ x: e.clientX, y: e.clientY })}
        onMouseMove={(e) => setHover({ x: e.clientX, y: e.clientY })}
        onMouseLeave={() => setHover(null)}
        className="inline-flex items-center gap-1 rounded bg-indigo-50 px-1.5 py-0.5 text-[10px] font-medium text-indigo-800 ring-1 ring-indigo-300 font-mono"
        aria-label={tooltip}
      >
        <span aria-hidden>🤖</span>
        <span>agent {short}</span>
        {status && status !== "completed" && (
          <span className="text-indigo-500">· {status}</span>
        )}
      </span>
      {hover &&
        createPortal(
          <div
            className="pointer-events-none fixed z-[9999] max-w-[420px] rounded bg-zinc-900 px-2 py-1.5 text-left text-[11px] text-white shadow-lg ring-1 ring-zinc-700"
            style={{
              left: hover.x + 14,
              top: hover.y + 14,
              wordBreak: "break-all",
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
