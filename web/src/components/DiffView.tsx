import { DiffEditor } from "@monaco-editor/react";

export type DiffSlice = {
  // Path is shown as a header above the editor and used to infer
  // syntax highlighting language. Stays raw (absolute path is fine —
  // basename is shown in the header for readability).
  filePath: string;
  before: string;
  after: string;
  // Optional caption (e.g. "edit 2 of 3" for MultiEdit), rendered next
  // to the basename.
  note?: string;
};

// Minimal extension → Monaco language map. Falls back to plaintext for
// anything not listed; the user still sees a clean diff, just without
// syntax highlighting. We deliberately do not pull all of Monaco's
// language registry — covering the languages this repo touches is
// enough for the dogfood loop.
const LANGUAGE_BY_EXT: Record<string, string> = {
  ts: "typescript",
  tsx: "typescript",
  js: "javascript",
  jsx: "javascript",
  mjs: "javascript",
  cjs: "javascript",
  go: "go",
  py: "python",
  rs: "rust",
  java: "java",
  rb: "ruby",
  md: "markdown",
  json: "json",
  jsonl: "json",
  yaml: "yaml",
  yml: "yaml",
  toml: "ini",
  ini: "ini",
  sh: "shell",
  bash: "shell",
  zsh: "shell",
  sql: "sql",
  css: "css",
  scss: "scss",
  html: "html",
  xml: "xml",
  proto: "proto",
  graphql: "graphql",
  Dockerfile: "dockerfile",
};

function languageFor(filePath: string): string {
  const base = filePath.split("/").pop() ?? filePath;
  if (LANGUAGE_BY_EXT[base]) return LANGUAGE_BY_EXT[base]; // exact basename match (e.g. Dockerfile)
  const dot = base.lastIndexOf(".");
  if (dot < 0) return "plaintext";
  const ext = base.slice(dot + 1).toLowerCase();
  return LANGUAGE_BY_EXT[ext] ?? "plaintext";
}

function basename(filePath: string): string {
  return filePath.split("/").pop() || filePath;
}

// Heuristic editor height: every diff has at least header + a few
// lines, but very large diffs get capped so a single Edit can't push
// the timeline page kilometers tall. The user can still scroll inside
// the editor when content exceeds the viewport.
function heightFor(slice: DiffSlice): number {
  const lines = Math.max(
    slice.before.split("\n").length,
    slice.after.split("\n").length,
  );
  const px = Math.min(Math.max(lines, 4), 30) * 19 + 24; // 19px line + padding
  return px;
}

export function DiffView({ slice }: { slice: DiffSlice }) {
  const language = languageFor(slice.filePath);
  return (
    <div className="overflow-hidden rounded border border-zinc-300 bg-white">
      <div className="flex items-baseline justify-between border-b border-zinc-200 bg-zinc-50 px-3 py-1.5">
        <div className="flex items-baseline gap-2">
          <span className="font-mono text-xs font-medium text-zinc-800">
            {basename(slice.filePath)}
          </span>
          <span className="text-[10px] text-zinc-400">{language}</span>
          {slice.note && (
            <span className="text-[10px] text-zinc-500">{slice.note}</span>
          )}
        </div>
        <span
          className="truncate font-mono text-[10px] text-zinc-400"
          title={slice.filePath}
        >
          {slice.filePath}
        </span>
      </div>
      <DiffEditor
        height={heightFor(slice)}
        original={slice.before}
        modified={slice.after}
        language={language}
        theme="vs"
        options={{
          readOnly: true,
          renderSideBySide: true,
          renderOverviewRuler: false,
          minimap: { enabled: false },
          scrollBeyondLastLine: false,
          fontSize: 12,
          lineNumbers: "on",
          folding: false,
          automaticLayout: true,
          scrollbar: { alwaysConsumeMouseWheel: false },
        }}
      />
    </div>
  );
}

// Convert a TOOL_CALL payload into zero or more diff slices. Returns
// [] for tools that aren't file mutators (so the EventCard can decide
// whether to show the diff affordance with a single truthy check on
// length). Defensive about unexpected payload shapes — we'd rather
// return [] than throw and break Timeline rendering.
export function payloadToDiff(
  payload: Record<string, unknown> | null | undefined,
): DiffSlice[] {
  if (!payload) return [];
  const name = typeof payload.name === "string" ? payload.name : "";
  const input = (payload.input ?? {}) as Record<string, unknown>;
  const filePath =
    typeof input.file_path === "string" ? input.file_path : "";
  if (!filePath) return [];

  if (name === "Edit") {
    const before = stringField(input.old_string);
    const after = stringField(input.new_string);
    if (before === null && after === null) return [];
    return [{ filePath, before: before ?? "", after: after ?? "" }];
  }
  if (name === "Write") {
    const after = stringField(input.content);
    if (after === null) return [];
    // Treat Write as a file creation: before is empty. Existing-file
    // overwrites will display as full insert, which is technically
    // correct but loses the implicit before — see issue #43 follow-ups.
    return [{ filePath, before: "", after }];
  }
  if (name === "MultiEdit") {
    const edits = Array.isArray(input.edits) ? input.edits : [];
    const slices: DiffSlice[] = [];
    edits.forEach((raw, i) => {
      if (!raw || typeof raw !== "object") return;
      const e = raw as Record<string, unknown>;
      const before = stringField(e.old_string);
      const after = stringField(e.new_string);
      if (before === null && after === null) return;
      slices.push({
        filePath,
        before: before ?? "",
        after: after ?? "",
        note: `edit ${i + 1} of ${edits.length}`,
      });
    });
    return slices;
  }
  return [];
}

function stringField(v: unknown): string | null {
  return typeof v === "string" ? v : null;
}
