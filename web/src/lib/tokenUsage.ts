import type { TokenUsage } from "../types";

// compactNum renders a token count in a stable narrow format. We avoid
// `Intl.NumberFormat` here so the chip width stays predictable across
// locales (the audit chip is sized for 5-6 chars max).
//
//   123        →  "123"
//   1_234      →  "1.2k"
//   12_345     →  "12k"
//   123_456    →  "123k"
//   1_234_567  →  "1.2M"
export function compactNum(n: number | null | undefined): string {
  if (n == null) return "0";
  if (n < 1000) return String(n);
  if (n < 1_000_000) {
    return n < 10_000 ? `${(n / 1000).toFixed(1)}k` : `${Math.round(n / 1000)}k`;
  }
  if (n < 1_000_000_000) {
    return n < 10_000_000
      ? `${(n / 1_000_000).toFixed(1)}M`
      : `${Math.round(n / 1_000_000)}M`;
  }
  return `${(n / 1_000_000_000).toFixed(1)}B`;
}

// tokenUsageAriaLabel describes the chip in plain language for screen
// readers — the visible chip uses arrows + symbols (`↑↓◊`) hidden via
// `aria-hidden`, which would otherwise render as bare numbers without
// context. Audit / compliance users may rely on assistive tech, so this
// is load-bearing accessibility, not decoration.
export function tokenUsageAriaLabel(usage: TokenUsage): string {
  const parts = [
    `input ${usage.inputTokens.toLocaleString()} tokens`,
    `output ${usage.outputTokens.toLocaleString()} tokens`,
  ];
  if (usage.cacheReadTokens) {
    parts.push(`cache read ${usage.cacheReadTokens.toLocaleString()} tokens`);
  }
  return parts.join(", ");
}

// tokenUsageTooltip builds the full breakdown shown when hovering over
// the per-message token chip. Reads cleanly across two columns of
// label/value, never folds cache_read into input (per ADR 0002 line 47:
// hiding cache_read distorts audit dashboards by ~250×).
export function tokenUsageTooltip(
  usage: TokenUsage,
  stopReason?: string | null,
): string {
  const lines: string[] = [];
  if (usage.model) lines.push(`model: ${usage.model}`);
  if (usage.serviceTier) lines.push(`tier: ${usage.serviceTier}`);
  if (usage.vendor) lines.push(`vendor: ${usage.vendor}`);
  if (lines.length > 0) lines.push("");
  lines.push(`input:        ${usage.inputTokens.toLocaleString()}`);
  lines.push(`output:       ${usage.outputTokens.toLocaleString()}`);
  if (usage.cacheReadTokens) {
    lines.push(`cache read:   ${usage.cacheReadTokens.toLocaleString()}`);
  }
  if (usage.cacheWrite5mTokens) {
    lines.push(`cache write 5m: ${usage.cacheWrite5mTokens.toLocaleString()}`);
  }
  if (usage.cacheWrite1hTokens) {
    lines.push(`cache write 1h: ${usage.cacheWrite1hTokens.toLocaleString()}`);
  }
  if (usage.webSearchCalls) {
    lines.push(`web search:   ${usage.webSearchCalls}`);
  }
  if (usage.webFetchCalls) {
    lines.push(`web fetch:    ${usage.webFetchCalls}`);
  }
  if (stopReason) {
    lines.push("");
    lines.push(`stop_reason: ${stopReason}`);
  }
  return lines.join("\n");
}
