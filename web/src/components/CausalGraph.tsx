import { useCallback, useMemo, useState } from "react";
import { useQuery } from "@tanstack/react-query";
import ReactFlow, {
  Background,
  Controls,
  MarkerType,
  MiniMap,
  ReactFlowProvider,
  useReactFlow,
  type Edge,
  type Node,
} from "reactflow";
import "reactflow/dist/style.css";
import dagre from "@dagrejs/dagre";

import { gql, eventsQuery, linkedEventsQuery } from "../api/client";
import type {
  Event,
  EventKind,
  EventsResponse,
  LinkedEventsResponse,
} from "../types";
import { styleFor } from "./kindStyle";

// Hex colors mirroring the Tailwind 500-shade in kindStyle.ts. The
// MiniMap renders nodes with raw SVG fills, so we need real hex
// strings rather than utility classes.
const MINIMAP_HEX: Record<EventKind, string> = {
  PROMPT: "#3b82f6",
  THOUGHT: "#a855f7",
  TOOL_CALL: "#f59e0b",
  TOOL_RESULT: "#10b981",
  CODE_CHANGE: "#0ea5e9",
  COMMIT: "#52525b",
  PR: "#d946ef",
  TEST_RUN: "#14b8a6",
  BUILD: "#f97316",
  DEPLOY: "#ef4444",
  REVIEW: "#6366f1",
  DECISION: "#f43f5e",
  PUSH: "#06b6d4",
};

const NODE_W = 180;
const NODE_H = 56;

// Build the dagre layout for one or more sessions' worth of events.
function layoutPositions(
  events: Event[],
  edges: Array<{ source: string; target: string }>,
): Record<string, { x: number; y: number }> {
  const g = new dagre.graphlib.Graph();
  g.setDefaultEdgeLabel(() => ({}));
  g.setGraph({ rankdir: "TB", nodesep: 24, ranksep: 56 });
  for (const e of events) {
    g.setNode(e.id, { width: NODE_W, height: NODE_H });
  }
  for (const e of edges) {
    if (g.hasNode(e.source) && g.hasNode(e.target)) {
      g.setEdge(e.source, e.target);
    }
  }
  dagre.layout(g);
  const out: Record<string, { x: number; y: number }> = {};
  for (const id of g.nodes()) {
    const n = g.node(id);
    out[id] = { x: n.x - NODE_W / 2, y: n.y - NODE_H / 2 };
  }
  return out;
}

function shortenSessionId(sid: string): string {
  if (sid.length <= 14) return sid;
  // Anchor on the head ("git-…", "github-pr:…") or the tail (UUID).
  if (sid.includes(":") || sid.startsWith("git-") || sid.startsWith("github-")) {
    return sid.length > 22 ? sid.slice(0, 22) + "…" : sid;
  }
  return sid.slice(0, 8) + "…" + sid.slice(-4);
}

// GraphNodeLabel renders one node's interior — pure presentation, no
// hover state. We observed empirically that React onMouseEnter on
// descendants of a ReactFlow node does NOT fire reliably (likely because
// the library wraps each node with its own pointer/d3-zoom handlers);
// the working fix is to use ReactFlow's own `onNodeMouseEnter` /
// `onNodeMouseLeave` props, which live one level up in GraphCanvas.
function GraphNodeLabel({
  kind,
  isAnchor,
  caption,
  captionMono,
  containerCls,
}: {
  kind: EventKind;
  isAnchor: boolean;
  caption: string;
  captionMono: boolean;
  containerCls: string;
}) {
  const s = styleFor(kind);
  return (
    <div
      className={`flex h-full w-full flex-col gap-0.5 rounded border ${containerCls} px-2 py-1 ${
        isAnchor ? "" : "ring-2 ring-purple-300 ring-offset-1"
      }`}
    >
      <div className="flex items-center gap-1 text-[10px]">
        <span aria-hidden>{s.icon}</span>
        <span className="font-medium uppercase tracking-wide">{s.label}</span>
      </div>
      <div
        className={`truncate text-[9px] text-zinc-500 ${
          captionMono ? "font-mono" : ""
        }`}
      >
        {caption}
      </div>
    </div>
  );
}

// nodeSummary derives a short, human-meaningful caption from an event's
// payload, sized to fit the 180-px graph tile. Mirrors EventCard's
// `summarize()` logic but trimmed to a single short string — the timeline
// has a full row of width to play with, the graph node does not.
//
// Returns "" when no caption can be derived; callers fall back to the
// ULID tail or session shorthand to preserve a stable identifier.
function nodeSummary(event: Event): string {
  const p = (event.payload ?? {}) as Record<string, unknown>;
  const asStr = (v: unknown) => (typeof v === "string" ? v : "");
  const clip = (s: string, n: number) => (s.length > n ? s.slice(0, n) + "…" : s);
  switch (event.kind) {
    case "TOOL_CALL":
    case "TOOL_RESULT":
      return asStr(p.name);
    case "PROMPT":
    case "THOUGHT":
      return clip(asStr(p.text).replace(/\s+/g, " ").trim(), 24);
    case "COMMIT": {
      const sha = asStr(p.sha).slice(0, 7);
      const subject = asStr(p.subject);
      return subject ? `${sha} · ${clip(subject, 16)}` : sha;
    }
    case "DECISION":
      return asStr(p.marker);
    case "PUSH":
      return asStr(p.ref).replace(/^refs\/(heads|tags)\//, "");
    case "BUILD": {
      const run = (p.workflow_run ?? {}) as Record<string, unknown>;
      return asStr(run.name) || asStr(p.workflow);
    }
    case "DEPLOY":
      return asStr(p.environment);
    case "REVIEW":
      return asStr(p.action);
    case "PR": {
      const n = p.number;
      return typeof n === "number" ? `#${n}` : "";
    }
    case "CODE_CHANGE":
      return "diff";
    case "TEST_RUN":
      return asStr(p.status);
    default:
      return "";
  }
}

function buildGraph(
  events: Event[],
  anchorSessionID: string,
): { nodes: Node[]; edges: Edge[] } {
  const hashToId: Record<string, string> = {};
  const eventById: Record<string, Event> = {};
  for (const e of events) {
    if (e.hash) hashToId[e.hash] = e.id;
    eventById[e.id] = e;
  }

  const edges: Edge[] = [];
  const seenLinkIds = new Set<string>();

  for (const e of events) {
    // Explicit causal parents — strongest semantic edge.
    for (const p of e.parents) {
      edges.push({
        id: `parent:${p}->${e.id}`,
        source: p,
        target: e.id,
        type: "smoothstep",
        markerEnd: { type: MarkerType.ArrowClosed, color: "#1f2937" },
        style: { stroke: "#1f2937", strokeWidth: 2 },
      });
    }
    // Hash chain — structural; only render when both endpoints rendered.
    if (e.prevHash && hashToId[e.prevHash]) {
      edges.push({
        id: `chain:${e.id}`,
        source: hashToId[e.prevHash],
        target: e.id,
        type: "smoothstep",
        markerEnd: { type: MarkerType.ArrowClosed, color: "#9ca3af" },
        style: { stroke: "#9ca3af", strokeWidth: 1, strokeDasharray: "4 3" },
      });
    }
    // Linker-inferred edges. event.links surfaces both inbound and
    // outbound, so dedupe by canonical key. Cross-session links get
    // emphasised because they're the whole point of this view.
    for (const l of e.links) {
      const key = `link:${l.fromEvent}->${l.toEvent}:${l.relation}`;
      if (seenLinkIds.has(key)) continue;
      seenLinkIds.add(key);
      const fromEv = eventById[l.fromEvent];
      const toEv = eventById[l.toEvent];
      const isCrossSession =
        fromEv && toEv && fromEv.sessionId !== toEv.sessionId;
      edges.push({
        id: key,
        source: l.fromEvent,
        target: l.toEvent,
        type: "smoothstep",
        label: l.relation,
        labelStyle: {
          fontSize: 10,
          fill: isCrossSession ? "#9333ea" : "#4f46e5",
          fontWeight: isCrossSession ? 600 : 400,
        },
        labelBgPadding: [4, 2],
        labelBgBorderRadius: 4,
        labelBgStyle: { fill: isCrossSession ? "#f3e8ff" : "#eef2ff" },
        markerEnd: {
          type: MarkerType.Arrow,
          color: isCrossSession ? "#9333ea" : "#6366f1",
        },
        style: {
          stroke: isCrossSession ? "#9333ea" : "#6366f1",
          strokeWidth: isCrossSession ? 2 : 1.5,
        },
      });
    }
  }

  const positions = layoutPositions(events, edges);

  const nodes: Node[] = events.map((e) => {
    const s = styleFor(e.kind);
    const idTail = e.id.length > 12 ? e.id.slice(-12) : e.id;
    const isAnchor = e.sessionId === anchorSessionID;
    const summary = nodeSummary(e);
    // Prefer the kind-aware summary; fall back to the ULID tail (anchor)
    // or session shorthand (cross-session) so unknown payload shapes still
    // get a stable identifier on the tile.
    const caption =
      summary || (isAnchor ? idTail : shortenSessionId(e.sessionId));
    // Mono for ULIDs / SHAs; sans-serif for prose so truncated subjects
    // and prompts are easier to scan.
    const captionMono =
      !summary || /^[0-9a-f]{7,}( ·|$)/.test(summary) || /^git-/.test(summary);
    return {
      id: e.id,
      position: positions[e.id] ?? { x: 0, y: 0 },
      data: {
        kind: e.kind,
        // Stash the source event + derived summary so GraphCanvas can
        // resolve them in onNodeMouseEnter without a lookup back through
        // the events array.
        event: e,
        summary,
        label: (
          <GraphNodeLabel
            kind={e.kind}
            isAnchor={isAnchor}
            caption={caption}
            captionMono={captionMono}
            containerCls={s.container}
          />
        ),
      },
      style: {
        width: NODE_W,
        height: NODE_H,
        padding: 0,
        border: "none",
        background: "transparent",
      },
    };
  });

  return { nodes, edges };
}

// Inner canvas — must live under <ReactFlowProvider> so useReactFlow()
// can read/write the viewport. MiniMap onClick → setCenter for
// reliable navigation under React 18 StrictMode (d3-zoom path is
// flaky there).
function GraphCanvas({ nodes, edges }: { nodes: Node[]; edges: Edge[] }) {
  const flow = useReactFlow();
  const onMiniMapClick = useCallback(
    (_evt: React.MouseEvent, position: { x: number; y: number }) => {
      flow.setCenter(position.x, position.y, { zoom: 1, duration: 250 });
    },
    [flow],
  );

  // We observed React onMouseEnter on descendants of a ReactFlow node
  // doesn't fire reliably; ReactFlow's `onNodeMouseEnter` / `Leave`
  // props are the working contract. Tooltip is rendered as a child of
  // <ReactFlow> using `position: fixed` so it escapes both the parent's
  // and the viewport's `overflow: hidden`.
  const [hover, setHover] = useState<{
    event: Event;
    summary: string;
    x: number;
    y: number;
  } | null>(null);
  const onNodeEnter = useCallback(
    (evt: React.MouseEvent, node: Node) => {
      const data = node.data as
        | { event?: Event; summary?: string }
        | undefined;
      const ev = data?.event;
      if (!ev) return;
      setHover({
        event: ev,
        summary: data?.summary ?? "",
        x: evt.clientX,
        y: evt.clientY,
      });
    },
    [],
  );
  const onNodeMove = useCallback((evt: React.MouseEvent) => {
    // Skip identical-position updates so jitter / sub-pixel events
    // don't churn React; on a 200-300 node graph the cumulative cost
    // of pointless re-renders matters more than the branch.
    setHover((h) => {
      if (!h) return h;
      if (h.x === evt.clientX && h.y === evt.clientY) return h;
      return { ...h, x: evt.clientX, y: evt.clientY };
    });
  }, []);
  const onNodeLeave = useCallback(() => setHover(null), []);

  return (
    <ReactFlow
      nodes={nodes}
      edges={edges}
      fitView
      nodesDraggable={false}
      nodesConnectable={false}
      elementsSelectable={false}
      onNodeMouseEnter={onNodeEnter}
      onNodeMouseMove={onNodeMove}
      onNodeMouseLeave={onNodeLeave}
      proOptions={{ hideAttribution: true }}
    >
      <Background gap={16} size={1} color="#e4e4e7" />
      <Controls showInteractive={false} />
      <MiniMap
        pannable
        zoomable
        onClick={onMiniMapClick}
        ariaLabel="Causal graph minimap"
        nodeColor={(n) => {
          const k = (n.data as { kind?: EventKind } | undefined)?.kind;
          return (k && MINIMAP_HEX[k]) || "#a1a1aa";
        }}
        nodeStrokeWidth={0}
        maskColor="rgba(255, 255, 255, 0.6)"
      />
      {hover && (
        <div
          className="pointer-events-none fixed z-[9999] max-w-[320px] rounded bg-zinc-900 px-2 py-1.5 text-left text-[11px] text-white shadow-lg ring-1 ring-zinc-700"
          style={{
            left: hover.x + 14,
            top: hover.y + 14,
            wordBreak: "break-all",
            whiteSpace: "pre-wrap",
          }}
        >
          <div className="font-semibold uppercase tracking-wide">
            {hover.event.kind}
          </div>
          <div className="mt-0.5 font-mono text-[10px] text-zinc-300">
            id: {hover.event.id}
          </div>
          <div className="font-mono text-[10px] text-zinc-300">
            session: {hover.event.sessionId}
          </div>
          {hover.summary && (
            <div className="mt-1 text-zinc-100">{hover.summary}</div>
          )}
        </div>
      )}
    </ReactFlow>
  );
}

export function CausalGraph({
  sessionId,
  linked,
}: {
  sessionId: string;
  linked: boolean;
}) {
  // CRITICAL: this hook MUST share its queryKey + queryFn shape with
  // the Timeline view's hook so the react-query cache is consistent.
  // Earlier we transformed via `.then((d) => d.events)` here, which
  // wrote the un-transformed EventsResponse object into the cache
  // (react-query keys on queryKey, not on what queryFn returns post-
  // transform). Timeline reads the EventsResponse fine; this graph
  // hook then read it back as `Event[]` and `for…of` threw on the
  // object, blanking the React tree.
  const { data, error, isLoading } = useQuery<
    EventsResponse | LinkedEventsResponse
  >({
    queryKey: linked ? ["linkedEvents", sessionId] : ["events", sessionId],
    queryFn: linked
      ? () =>
          gql<LinkedEventsResponse>(linkedEventsQuery, {
            sessionId,
            depth: 1,
            perSessionLimit: 200,
          })
      : () => gql<EventsResponse>(eventsQuery, { sessionId, limit: 200 }),
    enabled: sessionId.length > 0,
    // Audit / graph view doesn't need 2s polling like Timeline — every
    // refetch reruns the dagre layout (O(N) on 200-300 nodes) which
    // blocks the main thread enough to make Graph→Timeline toggling
    // feel sticky. 15s is a reasonable freshness for an "investigate
    // what already happened" view.
    refetchInterval: 15_000,
  });

  // Unify the two response shapes into a single Event[] for downstream
  // graph building. Both Timeline and CausalGraph populate the same
  // cache cell, so this stays consistent across view toggles.
  const events: Event[] = useMemo(() => {
    if (!data) return [];
    if ("linkedEvents" in data) return data.linkedEvents;
    return data.events;
  }, [data]);

  const graph = useMemo(() => {
    if (!events.length) return { nodes: [], edges: [], crossSessionCount: 0 };
    const built = buildGraph(events, sessionId);
    const crossSessionCount = events.filter(
      (e) => e.sessionId !== sessionId,
    ).length;
    return { ...built, crossSessionCount };
  }, [events, sessionId]);

  if (isLoading) return <div className="text-sm text-zinc-500">Loading…</div>;
  if (error) {
    return (
      <div className="text-sm text-rose-600">
        Error: {(error as Error).message}
      </div>
    );
  }
  if (!data) return null;

  if (graph.nodes.length === 0) {
    return (
      <div className="text-sm text-zinc-500">
        No events for this session yet.
      </div>
    );
  }

  return (
    <div>
      {linked && (
        <div className="mb-2 text-xs text-zinc-600">
          Showing {events.length} events across linked sessions
          {graph.crossSessionCount > 0 && (
            <>
              {" "}
              ·{" "}
              <span className="font-medium text-purple-700">
                {graph.crossSessionCount} from neighbouring sessions
              </span>
            </>
          )}
          .
        </div>
      )}
      <div className="h-[calc(100vh-200px)] overflow-hidden rounded border border-zinc-200 bg-white">
        <ReactFlowProvider>
          <GraphCanvas nodes={graph.nodes} edges={graph.edges} />
        </ReactFlowProvider>
      </div>
    </div>
  );
}
