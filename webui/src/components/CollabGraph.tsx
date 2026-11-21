import { PointerEvent, useEffect, useMemo, useRef, useState } from "react";
import { Link } from "react-router-dom";
import { forceCenter, forceCollide, forceLink, forceManyBody, forceRadial, forceSimulation, SimulationLinkDatum, SimulationNodeDatum } from "d3-force";
import { GraphEdge, GraphNode, GraphReport } from "../types";

/**
 * The collaboration graph: artists as nodes sized by how many recordings in scope
 * credit them, edges weighted by how many they share. Laid out once per answer with
 * d3-force (layout only — the drawing is plain SVG on the theme's tokens), so the
 * picture does not jiggle and a re-render costs nothing.
 *
 * The side panel is the same data as a list, and is the non-visual way in: every
 * artist and pair the picture shows can be reached and acted on from there.
 */

type SimNode = GraphNode & SimulationNodeDatum;
type SimLink = SimulationLinkDatum<SimNode> & { weight: number };

interface Laid {
  nodes: SimNode[];
  links: { source: SimNode; target: SimNode; weight: number }[];
  box: { x: number; y: number; w: number; h: number };
}

/** Node radius from its track count: area, not radius, follows the count. */
function radius(tracks: number): number {
  return Math.min(22, 3 + Math.sqrt(tracks) * 1.6);
}

/** Edge width from its weight, compressed so one heavy pair does not swamp the rest. */
function edgeWidth(weight: number): number {
  return Math.min(6, 0.75 + Math.log2(weight) * 0.9);
}

/** How many of the largest nodes carry a permanent label; the rest label on hover. */
const LABELLED = 24;

function layout(report: GraphReport): Laid {
  const nodes: SimNode[] = report.nodes.map((n) => ({ ...n }));
  const links: SimLink[] = report.edges.map((e: GraphEdge) => ({ source: e.source, target: e.target, weight: e.weight }));
  const centred = report.center != null;
  const sim = forceSimulation(nodes)
    .force(
      "link",
      forceLink<SimNode, SimLink>(links)
        .id((d) => d.mb_id)
        .distance((l) => 40 + 60 / Math.sqrt(l.weight))
        .strength((l) => Math.min(1, 0.15 + l.weight / 20)),
    )
    .force("charge", forceManyBody().strength(-90))
    .force("collide", forceCollide<SimNode>((d) => radius(d.tracks) + 3))
    .stop();
  if (centred) {
    // The centred artist pinned in the middle, each hop on its own ring.
    for (const n of nodes) if (n.hop === 0) (n.fx = 0), (n.fy = 0);
    sim.force("radial", forceRadial<SimNode>((d) => d.hop * 150, 0, 0).strength(0.6));
  } else {
    sim.force("center", forceCenter(0, 0));
  }
  // Deterministic tick count rather than an animated settle: the same answer always
  // draws the same picture.
  sim.tick(Math.min(400, 120 + nodes.length * 2));

  let minX = Infinity, minY = Infinity, maxX = -Infinity, maxY = -Infinity;
  for (const n of nodes) {
    const r = radius(n.tracks) + 40; // room for the label beside the node
    minX = Math.min(minX, (n.x ?? 0) - r);
    minY = Math.min(minY, (n.y ?? 0) - r);
    maxX = Math.max(maxX, (n.x ?? 0) + r);
    maxY = Math.max(maxY, (n.y ?? 0) + r);
  }
  if (!isFinite(minX)) (minX = -100), (minY = -100), (maxX = 100), (maxY = 100);
  return {
    nodes,
    links: links as unknown as Laid["links"],
    box: { x: minX, y: minY, w: maxX - minX, h: maxY - minY },
  };
}

export function CollabGraph({
  report,
  selected,
  onSelect,
}: {
  report: GraphReport;
  selected: string | null;
  onSelect: (mbid: string | null) => void;
}) {
  const laid = useMemo(() => layout(report), [report]);
  const [hover, setHover] = useState<string | null>(null);
  const [view, setView] = useState<Laid["box"] | null>(null);
  const box = view ?? laid.box;
  const svg = useRef<SVGSVGElement>(null);
  const drag = useRef<{ x: number; y: number; box: Laid["box"]; moved: boolean } | null>(null);

  // A new answer starts from its own framing.
  const [framed, setFramed] = useState(laid);
  if (framed !== laid) {
    setFramed(laid);
    setView(null);
  }

  const focus = hover ?? selected;
  const neighbours = useMemo(() => {
    const set = new Set<string>();
    if (!focus) return set;
    set.add(focus);
    for (const l of laid.links) {
      if (l.source.mb_id === focus) set.add(l.target.mb_id);
      if (l.target.mb_id === focus) set.add(l.source.mb_id);
    }
    return set;
  }, [focus, laid]);
  const labelled = useMemo(() => {
    const top = [...laid.nodes].sort((a, b) => b.tracks - a.tracks).slice(0, LABELLED);
    return new Set(top.map((n) => n.mb_id));
  }, [laid]);

  // Pointer position in graph coordinates, for zooming about the cursor.
  const toGraph = (clientX: number, clientY: number) => {
    const rect = svg.current!.getBoundingClientRect();
    const scale = Math.max(box.w / rect.width, box.h / rect.height);
    const offX = (rect.width * scale - box.w) / 2;
    const offY = (rect.height * scale - box.h) / 2;
    return { x: box.x - offX + (clientX - rect.left) * scale, y: box.y - offY + (clientY - rect.top) * scale, scale };
  };

  // Wheel zooms about the cursor. A native listener, because React's is passive and
  // cannot stop the page scrolling underneath.
  const zoom = useRef<(e: globalThis.WheelEvent) => void>(() => {});
  zoom.current = (e) => {
    e.preventDefault();
    const p = toGraph(e.clientX, e.clientY);
    const k = e.deltaY > 0 ? 1.15 : 1 / 1.15;
    setView({ x: p.x - (p.x - box.x) * k, y: p.y - (p.y - box.y) * k, w: box.w * k, h: box.h * k });
  };
  useEffect(() => {
    const el = svg.current;
    if (!el) return;
    const handler = (e: globalThis.WheelEvent) => zoom.current(e);
    el.addEventListener("wheel", handler, { passive: false });
    return () => el.removeEventListener("wheel", handler);
  }, []);
  const onPointerDown = (e: PointerEvent<SVGSVGElement>) => {
    drag.current = { x: e.clientX, y: e.clientY, box, moved: false };
    (e.target as Element).setPointerCapture?.(e.pointerId);
  };
  const onPointerMove = (e: PointerEvent<SVGSVGElement>) => {
    const d = drag.current;
    if (!d) return;
    const dx = e.clientX - d.x, dy = e.clientY - d.y;
    if (Math.abs(dx) + Math.abs(dy) > 3) d.moved = true;
    if (!d.moved) return;
    const scale = toGraph(e.clientX, e.clientY).scale;
    setView({ ...d.box, x: d.box.x - dx * scale, y: d.box.y - dy * scale });
  };
  const onPointerUp = () => {
    const d = drag.current;
    drag.current = null;
    if (d && !d.moved) onSelect(null);
  };

  return (
    <div className="graph-canvas">
      <svg
        ref={svg}
        viewBox={`${box.x} ${box.y} ${box.w} ${box.h}`}
        preserveAspectRatio="xMidYMid meet"
        role="img"
        aria-label={`Collaboration graph: ${report.nodes.length} artists, ${report.edges.length} connections. The list beside it holds the same data.`}
        onPointerDown={onPointerDown}
        onPointerMove={onPointerMove}
        onPointerUp={onPointerUp}
      >
        <g>
          {laid.links.map((l) => {
            const lit = focus != null && (l.source.mb_id === focus || l.target.mb_id === focus);
            return (
              <line
                key={`${l.source.mb_id}-${l.target.mb_id}`}
                className={`g-edge${lit ? " lit" : focus ? " faded" : ""}`}
                x1={l.source.x}
                y1={l.source.y}
                x2={l.target.x}
                y2={l.target.y}
                strokeWidth={edgeWidth(l.weight)}
              >
                <title>{`${l.source.name} × ${l.target.name}: ${l.weight} shared ${l.weight === 1 ? "recording" : "recordings"}`}</title>
              </line>
            );
          })}
        </g>
        <g>
          {laid.nodes.map((n) => {
            const r = radius(n.tracks);
            const isFocus = n.mb_id === focus;
            const dim = focus != null && !neighbours.has(n.mb_id);
            const showLabel = isFocus || (focus ? neighbours.has(n.mb_id) : labelled.has(n.mb_id) || n.hop === 0);
            return (
              <g
                key={n.mb_id}
                className={`g-node${isFocus || n.mb_id === selected ? " focus" : ""}${n.hop === 0 && report.center ? " center" : ""}${dim ? " faded" : ""}`}
                transform={`translate(${n.x},${n.y})`}
                onPointerEnter={() => setHover(n.mb_id)}
                onPointerLeave={() => setHover(null)}
                onPointerDown={(e) => e.stopPropagation()}
                onPointerUp={(e) => e.stopPropagation()}
                onClick={() => onSelect(n.mb_id === selected ? null : n.mb_id)}
              >
                {/* A hit target larger than the mark, so small nodes are clickable. */}
                <circle className="hit" r={Math.max(r, 8)} />
                <circle r={r} />
                <title>{`${n.name}: ${n.tracks} ${n.tracks === 1 ? "recording" : "recordings"}`}</title>
                {showLabel && (
                  <text x={r + 4} y={3.5}>
                    {n.name}
                  </text>
                )}
              </g>
            );
          })}
        </g>
      </svg>
      {view && (
        <button type="button" className="btn btn-ghost btn-sm graph-reset" onClick={() => setView(null)}>
          Reset view
        </button>
      )}
    </div>
  );
}

/** One collaborator row: the other artist, the shared count, and where it leads. */
export function CollaboratorRow({
  node,
  weight,
  label,
  onFocus,
}: {
  node: GraphNode;
  weight: number;
  label?: string;
  onFocus: (mbid: string) => void;
}) {
  return (
    <div className="collab-row">
      <button type="button" className="collab-name" onClick={() => onFocus(node.mb_id)} title={`Select ${node.name}`}>
        {label ?? node.name}
      </button>
      <span className="mono tabnum dim">{weight}</span>
    </div>
  );
}

/** A link to an artist's collection page, when they have one. */
export function ArtistLink({ node }: { node: GraphNode }) {
  if (!node.in_collection) return null;
  return (
    <Link to={`/collection/${node.mb_id}`} className="mono" style={{ fontSize: 12 }}>
      Open artist →
    </Link>
  );
}
