// Shared parsing for prompt events. Claude Code fires UserPromptSubmit for
// genuine human input AND for system-injected blocks (a backgrounded task /
// sub-agent completion arrives as <task-notification>, a system nudge as
// <system-reminder>), and the hook captures them all as kind=prompt /
// actor=human. parsePrompt distinguishes the two and, for notifications,
// extracts the readable fields instead of dumping raw XML. (The capture-side
// mis-attribution is tracked in issue #118; this is the display-layer parse.)

const clip = (s: string, n: number): string =>
  s.length > n ? s.slice(0, n) + "…" : s;

// Only well-known injected tags are treated as non-human, so a human prompt
// that merely starts with "<" (e.g. "<div> is broken") isn't misclassified.
const INJECTION_TAG =
  /^<(task-notification|system-reminder|task-reminder|command-message|command-name|local-command-stdout|local-command-stderr)\b/;

function tagContent(text: string, name: string): string | null {
  const m = text.match(new RegExp(`<${name}>([\\s\\S]*?)</${name}>`));
  return m ? m[1].trim() : null;
}

export interface ParsedPrompt {
  human: boolean;
  title: string; // one-line label for summaries
  fields: { label: string; value: string }[]; // structured detail (notifications)
}

export function parsePrompt(text: string): ParsedPrompt {
  const t = text.replace(/^\s+/, "");
  const tag = t.match(INJECTION_TAG)?.[1];
  if (!tag) {
    return {
      human: true,
      title: clip(text.replace(/\s+/g, " ").trim(), 100) || "(empty prompt)",
      fields: [],
    };
  }
  if (tag === "task-notification") {
    const status = tagContent(text, "status");
    const summary = tagContent(text, "summary");
    const outputFile = tagContent(text, "output-file");
    const taskId = tagContent(text, "task-id");
    const toolUseId = tagContent(text, "tool-use-id");
    const fields: { label: string; value: string }[] = [];
    if (status) fields.push({ label: "status", value: status });
    if (summary) fields.push({ label: "summary", value: summary });
    if (outputFile)
      fields.push({ label: "output", value: outputFile.split("/").pop() || outputFile });
    if (taskId) fields.push({ label: "task-id", value: taskId });
    if (toolUseId) fields.push({ label: "tool-use-id", value: toolUseId });
    const title = summary
      ? `🔔 ${clip(summary, 90)}`
      : `🔔 task notification${taskId ? ` · ${taskId}` : ""}`;
    return { human: false, title, fields };
  }
  if (tag === "system-reminder") {
    return { human: false, title: "🔔 system reminder", fields: [] };
  }
  return { human: false, title: `🔔 ${tag}`, fields: [] };
}
