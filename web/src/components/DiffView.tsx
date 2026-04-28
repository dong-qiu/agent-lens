// Self-bundled Monaco. Importing this module loads ~600 KB gzipped of
// Monaco core; keep it isolated and React.lazy it from the consumer
// (EventCard) so the cost is only paid when a user actually expands a
// diff. The CDN-loader fallback Monaco/react would otherwise use is
// fully bypassed — see issue #45.

// Vite's ?worker query bundles Monaco's editor worker into a separate
// chunk we can construct on demand. We deliberately do NOT bundle the
// language workers (typescript / json / css / html); DiffEditor uses
// only Monarch tokenization, which lives in the editor module itself.
import EditorWorker from "monaco-editor/esm/vs/editor/editor.worker?worker";

// MonacoEnvironment must be set before any Monaco editor instance is
// created. ES module imports are hoisted, but module *body* runs in
// source order and before any consumer can render <DiffEditor>, so
// this assignment is in time as long as nothing imports monaco-editor
// before this module.
type MonacoSelf = typeof self & {
  MonacoEnvironment?: { getWorker: (workerId: string, label: string) => Worker };
};
(self as MonacoSelf).MonacoEnvironment = {
  getWorker(_workerId, _label) {
    return new EditorWorker();
  },
};

import * as monaco from "monaco-editor";
import { DiffEditor, loader } from "@monaco-editor/react";

// Tell the React wrapper to use our bundled monaco instead of fetching
// it from jsdelivr. Once configured, all subsequent <DiffEditor> /
// <Editor> mounts use the local copy.
loader.config({ monaco });

import type { DiffSlice } from "../lib/payloadToDiff";

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
  if (LANGUAGE_BY_EXT[base]) return LANGUAGE_BY_EXT[base];
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
// the timeline page kilometers tall. Content scrolls inside the editor
// when it exceeds the viewport.
function heightFor(slice: DiffSlice): number {
  const lines = Math.max(
    slice.before.split("\n").length,
    slice.after.split("\n").length,
  );
  const px = Math.min(Math.max(lines, 4), 30) * 19 + 24;
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

// Default export so EventCard can React.lazy this module — React.lazy
// expects a function returning a module with a default export.
export default DiffView;
