// Pure helpers for deriving file diffs from TOOL_CALL payloads.
// Lives outside components/DiffView so EventCard can import these
// synchronously without dragging Monaco into the eager bundle —
// DiffView itself is React.lazy-loaded.

export type DiffSlice = {
  filePath: string;
  before: string;
  after: string;
  // Optional caption (e.g. "edit 2 of 3" for MultiEdit).
  note?: string;
};

// Convert a TOOL_CALL payload into zero or more diff slices. Returns
// [] for tools that aren't file mutators so EventCard can decide
// whether to show the diff affordance with a single truthy length
// check. Defensive about unexpected payload shapes — we'd rather
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
    // overwrites display as full insert, which is correct but loses
    // the implicit before — see #43 follow-ups.
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
