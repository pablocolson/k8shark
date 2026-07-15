import { useMemo } from "react";
import type { Entry, Endpoint } from "../types";

interface Node {
  id: string;
  label: string;
  ns: string;
  x: number;
  y: number;
}
interface Edge {
  from: string;
  to: string;
  count: number;
  errors: number;
}

function nodeId(ep: Endpoint): string {
  return ep.name ? (ep.namespace ? `${ep.name}.${ep.namespace}` : ep.name) : ep.ip;
}

// Build a node/edge graph from the most recent entries and lay nodes out on a
// circle. This is intentionally dependency-free (no d3) and recomputes cheaply.
function buildGraph(entries: Entry[]): { nodes: Node[]; edges: Edge[] } {
  const nodeMap = new Map<string, { ns: string }>();
  const edgeMap = new Map<string, Edge>();

  for (const e of entries.slice(0, 800)) {
    const s = nodeId(e.src);
    const d = nodeId(e.dst);
    if (!nodeMap.has(s)) nodeMap.set(s, { ns: e.src.namespace ?? "" });
    if (!nodeMap.has(d)) nodeMap.set(d, { ns: e.dst.namespace ?? "" });
    const key = `${s}→${d}`;
    const edge = edgeMap.get(key) ?? { from: s, to: d, count: 0, errors: 0 };
    edge.count++;
    if (e.status === "error") edge.errors++;
    edgeMap.set(key, edge);
  }

  const ids = [...nodeMap.keys()];
  const cx = 480;
  const cy = 320;
  const r = Math.min(260, 90 + ids.length * 16);
  const nodes: Node[] = ids.map((id, i) => {
    const a = (i / Math.max(ids.length, 1)) * Math.PI * 2 - Math.PI / 2;
    return {
      id,
      label: id,
      ns: nodeMap.get(id)!.ns,
      x: cx + r * Math.cos(a),
      y: cy + r * Math.sin(a),
    };
  });

  return { nodes, edges: [...edgeMap.values()] };
}

const NS_COLORS = ["#4aa8ff", "#b07cff", "#37c98b", "#ffb454", "#ff6b6b", "#22d3ee"];
function nsColor(ns: string): string {
  let h = 0;
  for (let i = 0; i < ns.length; i++) h = (h * 31 + ns.charCodeAt(i)) & 0xffff;
  return NS_COLORS[h % NS_COLORS.length];
}

export function ServiceMap({ entries }: { entries: Entry[] }) {
  const { nodes, edges } = useMemo(() => buildGraph(entries), [entries]);
  const pos = useMemo(() => new Map(nodes.map((n) => [n.id, n])), [nodes]);
  const maxCount = Math.max(1, ...edges.map((e) => e.count));

  if (nodes.length === 0) {
    return <div className="map-empty">No traffic yet — the service map builds itself from live flows.</div>;
  }

  return (
    <div className="map-wrap">
      <svg viewBox="0 0 960 640" className="map-svg" preserveAspectRatio="xMidYMid meet">
        <defs>
          <marker id="arrow" markerWidth="8" markerHeight="8" refX="7" refY="4" orient="auto">
            <path d="M0,0 L8,4 L0,8 Z" fill="#5a6577" />
          </marker>
          <marker id="arrow-err" markerWidth="8" markerHeight="8" refX="7" refY="4" orient="auto">
            <path d="M0,0 L8,4 L0,8 Z" fill="#ff6b6b" />
          </marker>
        </defs>

        {edges.map((e) => {
          const a = pos.get(e.from);
          const b = pos.get(e.to);
          if (!a || !b || a === b) return null;
          const err = e.errors > 0;
          const w = 0.6 + (e.count / maxCount) * 4;
          return (
            <line
              key={`${e.from}-${e.to}`}
              x1={a.x}
              y1={a.y}
              x2={b.x}
              y2={b.y}
              stroke={err ? "#ff6b6b" : "#3a4051"}
              strokeWidth={w}
              strokeOpacity={err ? 0.9 : 0.55}
              markerEnd={err ? "url(#arrow-err)" : "url(#arrow)"}
            />
          );
        })}

        {nodes.map((n) => (
          <g key={n.id} className="map-node">
            <circle cx={n.x} cy={n.y} r={22} fill="#151a25" stroke={nsColor(n.ns)} strokeWidth={2.5} />
            <text x={n.x} y={n.y + 38} textAnchor="middle" className="map-label">
              {n.label}
            </text>
          </g>
        ))}
      </svg>
    </div>
  );
}
