import { useCallback, useMemo } from "react";
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
    return {
      id: e.id,
      position: positions[e.id] ?? { x: 0, y: 0 },
      data: {
        kind: e.kind,
        label: (
          <div
            className={`flex h-full w-full flex-col gap-0.5 rounded border ${s.container} px-2 py-1 ${
              isAnchor ? "" : "ring-2 ring-purple-300 ring-offset-1"
            }`}
          >
            <div className="flex items-center gap-1 text-[10px]">
              <span aria-hidden>{s.icon}</span>
              <span className="font-medium uppercase tracking-wide">
                {s.label}
              </span>
            </div>
            <div className="truncate font-mono text-[9px] text-zinc-500">
              {isAnchor ? idTail : shortenSessionId(e.sessionId)}
            </div>
          </div>
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

  return (
    <ReactFlow
      nodes={nodes}
      edges={edges}
      fitView
      nodesDraggable={false}
      nodesConnectable={false}
      elementsSelectable={false}
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
  // The two queries share queryKey so React Query treats them as the
  // same cache cell. Switching the toggle invalidates and refetches
  // because we change queryFn under the same key — but for v1 we
  // accept that (a single `events` query that always returned cross-
  // session is server-side-cheaper but loses the "single-session
  // anchor" semantic the URL conveys).
  const { data, error, isLoading } = useQuery({
    queryKey: linked ? ["linkedEvents", sessionId] : ["events", sessionId],
    queryFn: () =>
      linked
        ? gql<LinkedEventsResponse>(linkedEventsQuery, {
            sessionId,
            depth: 1,
            perSessionLimit: 200,
          }).then((d): Event[] => d.linkedEvents)
        : gql<EventsResponse>(eventsQuery, { sessionId, limit: 200 }).then(
            (d): Event[] => d.events,
          ),
    enabled: sessionId.length > 0,
    refetchInterval: 2000,
  });

  const graph = useMemo(() => {
    if (!data?.length) return { nodes: [], edges: [], crossSessionCount: 0 };
    const built = buildGraph(data, sessionId);
    const crossSessionCount = data.filter(
      (e) => e.sessionId !== sessionId,
    ).length;
    return { ...built, crossSessionCount };
  }, [data, sessionId]);

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
          Showing {data.length} events across linked sessions
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
