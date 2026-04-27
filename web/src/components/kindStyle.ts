import type { EventKind } from "../types";

export type KindStyle = {
  container: string;
  badge: string;
  dot: string;
  icon: string;
  label: string;
};

export const kindStyles: Record<EventKind, KindStyle> = {
  PROMPT: {
    container: "border-blue-300 bg-blue-50",
    badge: "bg-blue-100 text-blue-900",
    dot: "bg-blue-500 ring-blue-200",
    icon: "💬",
    label: "prompt",
  },
  THOUGHT: {
    container: "border-purple-300 bg-purple-50",
    badge: "bg-purple-100 text-purple-900",
    dot: "bg-purple-500 ring-purple-200",
    icon: "🧠",
    label: "thought",
  },
  TOOL_CALL: {
    container: "border-amber-300 bg-amber-50",
    badge: "bg-amber-100 text-amber-900",
    dot: "bg-amber-500 ring-amber-200",
    icon: "🔧",
    label: "tool call",
  },
  TOOL_RESULT: {
    container: "border-emerald-300 bg-emerald-50",
    badge: "bg-emerald-100 text-emerald-900",
    dot: "bg-emerald-500 ring-emerald-200",
    icon: "✓",
    label: "tool result",
  },
  CODE_CHANGE: {
    container: "border-sky-300 bg-sky-50",
    badge: "bg-sky-100 text-sky-900",
    dot: "bg-sky-500 ring-sky-200",
    icon: "✎",
    label: "code change",
  },
  COMMIT: {
    container: "border-zinc-300 bg-zinc-50",
    badge: "bg-zinc-200 text-zinc-900",
    dot: "bg-zinc-600 ring-zinc-300",
    icon: "📦",
    label: "commit",
  },
  PR: {
    container: "border-fuchsia-300 bg-fuchsia-50",
    badge: "bg-fuchsia-100 text-fuchsia-900",
    dot: "bg-fuchsia-500 ring-fuchsia-200",
    icon: "⇪",
    label: "pr",
  },
  TEST_RUN: {
    container: "border-teal-300 bg-teal-50",
    badge: "bg-teal-100 text-teal-900",
    dot: "bg-teal-500 ring-teal-200",
    icon: "✓✗",
    label: "test run",
  },
  BUILD: {
    container: "border-orange-300 bg-orange-50",
    badge: "bg-orange-100 text-orange-900",
    dot: "bg-orange-500 ring-orange-200",
    icon: "🛠",
    label: "build",
  },
  DEPLOY: {
    container: "border-red-300 bg-red-50",
    badge: "bg-red-100 text-red-900",
    dot: "bg-red-500 ring-red-200",
    icon: "🚀",
    label: "deploy",
  },
  REVIEW: {
    container: "border-indigo-300 bg-indigo-50",
    badge: "bg-indigo-100 text-indigo-900",
    dot: "bg-indigo-500 ring-indigo-200",
    icon: "👁",
    label: "review",
  },
  DECISION: {
    container: "border-rose-300 bg-rose-50",
    badge: "bg-rose-100 text-rose-900",
    dot: "bg-rose-500 ring-rose-200",
    icon: "·",
    label: "decision",
  },
};

export const fallbackStyle: KindStyle = {
  container: "border-zinc-300 bg-zinc-50",
  badge: "bg-zinc-100 text-zinc-900",
  dot: "bg-zinc-400 ring-zinc-200",
  icon: "•",
  label: "unknown",
};

export function styleFor(kind: EventKind): KindStyle {
  return kindStyles[kind] ?? fallbackStyle;
}

export function formatTimestamp(iso: string, ref: Date = new Date()): string {
  const d = new Date(iso);
  if (isNaN(d.getTime())) return iso;
  const sameDay = d.toDateString() === ref.toDateString();
  if (sameDay) {
    return d.toLocaleTimeString(undefined, {
      hour: "2-digit",
      minute: "2-digit",
      second: "2-digit",
    });
  }
  return d.toLocaleString(undefined, {
    month: "short",
    day: "numeric",
    hour: "2-digit",
    minute: "2-digit",
  });
}
