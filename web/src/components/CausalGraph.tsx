import { useMemo } from "react";
import { useQuery } from "@tanstack/react-query";
import ReactFlow, {
  Background,
  Controls,
  MarkerType,
  type Edge,
  type Node,
} from "reactflow";
import "reactflow/dist/style.css";
import dagre from "@dagrejs/dagre";

import { gql, eventsQuery } from "../api/client";
import type { Event, EventsResponse } from "../types";
import { styleFor } from "./kindStyle";

const NODE_W = 180;
const NODE_H = 56;

// Build the dagre layout for one session's worth of events. Returns a
// position map keyed by event id (top-left corner, since ReactFlow
// expects top-left while dagre returns center).
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

function buildGraph(events: Event[]): { nodes: Node[]; edges: Edge[] } {
  const hashToId: Record<string, string> = {};
  for (const e of events) {
    if (e.hash) hashToId[e.hash] = e.id;
  }

  const edges: Edge[] = [];
  const seenLinkIds = new Set<string>();

  for (const e of events) {
    // Explicit causal parents — the strongest semantic edge.
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
    // Hash chain — structural, not causal. Render lightly so it doesn't
    // dominate. Skip if the predecessor isn't in the rendered set
    // (graceful when limit truncates the head of the chain).
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
    // Linker-inferred edges. event.links includes both inbound and
    // outbound, so we'd see each edge twice if we didn't dedupe by a
    // canonical key.
    for (const l of e.links) {
      const key = `link:${l.fromEvent}->${l.toEvent}:${l.relation}`;
      if (seenLinkIds.has(key)) continue;
      seenLinkIds.add(key);
      edges.push({
        id: key,
        source: l.fromEvent,
        target: l.toEvent,
        type: "smoothstep",
        label: l.relation,
        labelStyle: { fontSize: 10, fill: "#4f46e5" },
        labelBgPadding: [4, 2],
        labelBgBorderRadius: 4,
        labelBgStyle: { fill: "#eef2ff" },
        markerEnd: { type: MarkerType.Arrow, color: "#6366f1" },
        style: { stroke: "#6366f1", strokeWidth: 1.5 },
      });
    }
  }

  const positions = layoutPositions(events, edges);

  const nodes: Node[] = events.map((e) => {
    const s = styleFor(e.kind);
    const idTail = e.id.length > 12 ? e.id.slice(-12) : e.id;
    return {
      id: e.id,
      position: positions[e.id] ?? { x: 0, y: 0 },
      data: {
        label: (
          <div className={`flex h-full w-full flex-col gap-0.5 rounded border ${s.container} px-2 py-1`}>
            <div className="flex items-center gap-1 text-[10px]">
              <span aria-hidden>{s.icon}</span>
              <span className="font-medium uppercase tracking-wide">{s.label}</span>
            </div>
            <div className="truncate font-mono text-[9px] text-zinc-500">{idTail}</div>
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

export function CausalGraph({ sessionId }: { sessionId: string }) {
  // Same queryKey as Timeline: react-query dedupes the fetch and both
  // views share the cache, so toggling Timeline ↔ Graph never re-fetches.
  const { data, error, isLoading } = useQuery({
    queryKey: ["events", sessionId],
    queryFn: () => gql<EventsResponse>(eventsQuery, { sessionId, limit: 200 }),
    enabled: sessionId.length > 0,
    refetchInterval: 2000,
  });

  const graph = useMemo(() => {
    if (!data?.events?.length) return { nodes: [], edges: [] };
    return buildGraph(data.events);
  }, [data]);

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
    <div className="h-[calc(100vh-180px)] overflow-hidden rounded border border-zinc-200 bg-white">
      <ReactFlow
        nodes={graph.nodes}
        edges={graph.edges}
        fitView
        nodesDraggable={false}
        nodesConnectable={false}
        elementsSelectable={false}
        proOptions={{ hideAttribution: true }}
      >
        <Background gap={16} size={1} color="#e4e4e7" />
        <Controls showInteractive={false} />
      </ReactFlow>
    </div>
  );
}
