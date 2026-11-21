import { PointerEvent, useEffect, useMemo, useRef, useState } from "react";
import { Link } from "react-router-dom";
import { forceCollide, forceLink, forceManyBody, forceRadial, forceSimulation, forceX, forceY, SimulationLinkDatum, SimulationNodeDatum } from "d3-force";
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
  radius: (tracks: number) => number;
  links: { source: SimNode; target: SimNode; weight: number }[];
  box: { x: number; y: number; w: number; h: number };
}

/**
 * Node radius from its track count, relative to the largest artist in view: area
 * follows the count, from 3 to 20. An absolute scale capped at the top made every
 * node the same size once performers and producers were in — each on hundreds of
 * recordings — and the graph packed into a uniform blob.
 */
function radiusScale(nodes: GraphNode[]): (tracks: number) => number {
  const most = Math.max(1, ...nodes.map((n) => n.tracks));
  return (tracks) => 3 + 17 * Math.sqrt(tracks / most);
}

/** Edge width from its weight, compressed so one heavy pair does not swamp the rest. */
function edgeWidth(weight: number): number {
  return Math.min(6, 0.75 + Math.log2(weight) * 0.9);
}

/** The most a graph is magnified to fill the canvas. */
const MAX_ZOOM = 2;

/** How many of the largest nodes carry a permanent label; the rest label on hover. */
const LABELLED = 24;

function layout(report: GraphReport): Laid {
  const radius = radiusScale(report.nodes);
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
    // Gentle gravity rather than a centring force: forceCenter only moves the average,
    // so a cluster with no edge to the rest drifted off and the frame spread to hold
    // it, shrinking everything else.
    sim.force("x", forceX<SimNode>(0).strength(0.08)).force("y", forceY<SimNode>(0).strength(0.08));
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
    radius,
    links: links as unknown as Laid["links"],
    box: { x: minX, y: minY, w: maxX - minX, h: maxY - minY },
  };
}

/** What nodes are coloured by. */
export type GraphColour = "genre" | "role" | "none";

/** Roles in the order they take the series slots; writing is the neutral. */
const ROLE_SLOTS: { role: string; label: string }[] = [
  { role: "credit", label: "Credited artist" },
  { role: "production", label: "Production" },
  { role: "performer", label: "Performer" },
];

export interface LegendEntry {
  key: string;
  label: string;
  colour: string;
  count: number;
}

/**
 * The colour of each node, and the legend for it. A category keeps its colour
 * whatever the filters show: genres are slotted by the library's own ranking
 * (report.genres), roles by a fixed order. Past three categories everything is the
 * neutral, because only three series stay apart when any two marks can touch.
 */
export function graphColours(report: GraphReport, by: GraphColour): { of: (n: GraphNode) => string | undefined; legend: LegendEntry[] } {
  if (by === "none") return { of: () => undefined, legend: [] };
  const slot = (i: number) => (i >= 0 && i < 3 ? `var(--series-${i + 1})` : "var(--series-other)");
  let keyOf: (n: GraphNode) => string;
  let labelOf: (key: string) => string;
  let order: string[];
  if (by === "genre") {
    order = report.genres.slice(0, 3);
    keyOf = (n) => (n.genre && order.includes(n.genre) ? n.genre : "");
    labelOf = (k) => k || "Other or unknown";
  } else {
    order = ROLE_SLOTS.map((r) => r.role);
    keyOf = (n) => (n.role && order.includes(n.role) ? n.role : n.role ? "writing" : "");
    labelOf = (k) => ROLE_SLOTS.find((r) => r.role === k)?.label ?? (k === "writing" ? "Writing" : "Unknown");
  }
  const colourOf = (key: string) => slot(order.indexOf(key));
  const counts = new Map<string, number>();
  for (const n of report.nodes) counts.set(keyOf(n), (counts.get(keyOf(n)) ?? 0) + 1);
  const keys = [...order.filter((k) => counts.has(k)), ...[...counts.keys()].filter((k) => !order.includes(k))];
  return {
    of: (n) => colourOf(keyOf(n)),
    legend: keys.map((k) => ({ key: k || "other", label: labelOf(k), colour: colourOf(k), count: counts.get(k) ?? 0 })),
  };
}

export function CollabGraph({
  report,
  selected,
  onSelect,
  colour = "none",
}: {
  report: GraphReport;
  selected: string | null;
  onSelect: (mbid: string | null) => void;
  colour?: GraphColour;
}) {
  const colours = useMemo(() => graphColours(report, colour), [report, colour]);
  const laid = useMemo(() => layout(report), [report]);
  const [hover, setHover] = useState<string | null>(null);
  const [view, setView] = useState<Laid["box"] | null>(null);

  // Fit the graph to the canvas, but magnify at most 2×. Labels and strokes keep their
  // screen size at any zoom (below), so magnifying only spreads the nodes out; past 2×
  // a handful of artists would turn into a few huge discs. The canvas is measured, not
  // assumed: on a phone it is a third as wide.
  const wrap = useRef<HTMLDivElement>(null);
  const [canvas, setCanvas] = useState({ w: 860, h: 560 });
  useEffect(() => {
    const el = wrap.current;
    if (!el || typeof ResizeObserver === "undefined") return;
    const observer = new ResizeObserver(([entry]) => {
      const { width, height } = entry.contentRect;
      if (width > 0 && height > 0) setCanvas({ w: width, h: height });
    });
    observer.observe(el);
    return () => observer.disconnect();
  }, []);
  const fitted = useMemo(() => {
    const b = laid.box;
    const w = Math.max(b.w, canvas.w / MAX_ZOOM);
    const h = Math.max(b.h, canvas.h / MAX_ZOOM);
    return { x: b.x + b.w / 2 - w / 2, y: b.y + b.h / 2 - h / 2, w, h };
  }, [laid, canvas]);
  const box = view ?? fitted;
  // Graph units per screen pixel. Labels are sized by it so they read at the same size
  // at any zoom; strokes use non-scaling-stroke (app.css) for the same reason.
  const unit = Math.max(box.w / canvas.w, box.h / canvas.h);
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
    // Fewer standing labels in a crowded graph: past sixty artists, 24 overlap.
    const count = laid.nodes.length > 60 ? LABELLED / 2 : LABELLED;
    const top = [...laid.nodes].sort((a, b) => b.tracks - a.tracks).slice(0, count);
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
    <div className="graph-canvas" ref={wrap}>
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
            const r = laid.radius(n.tracks);
            const isFocus = n.mb_id === focus;
            const dim = focus != null && !neighbours.has(n.mb_id);
            // hop is 0 for every node of an uncentred graph, so it only names the
            // centre when there is one.
            const isCentre = report.center != null && n.hop === 0;
            const showLabel = isFocus || (focus ? neighbours.has(n.mb_id) : labelled.has(n.mb_id) || isCentre);
            return (
              <g
                key={n.mb_id}
                // Not one of your artists: drawn quieter, so with everyone shown your own
                // still stand out. The tooltip says it in words too.
                className={`g-node${isFocus || n.mb_id === selected ? " focus" : ""}${n.hop === 0 && report.center ? " center" : ""}${dim ? " faded" : ""}${n.managed ? "" : " unmanaged"}`}
                transform={`translate(${n.x},${n.y})`}
                onPointerEnter={() => setHover(n.mb_id)}
                onPointerLeave={() => setHover(null)}
                onPointerDown={(e) => e.stopPropagation()}
                onPointerUp={(e) => e.stopPropagation()}
                onClick={() => onSelect(n.mb_id === selected ? null : n.mb_id)}
              >
                {/* A hit target larger than the mark, so small nodes are clickable. */}
                <circle className="hit" r={Math.max(r, 8)} />
                <circle r={r} style={colours.of(n) ? { fill: colours.of(n) } : undefined} />
                <title>
                  {`${n.name}: ${n.tracks} ${n.tracks === 1 ? "recording" : "recordings"}`}
                  {colour === "genre" && n.genre ? ` · ${n.genre}` : ""}
                  {colour === "role" && n.role ? ` · mostly ${n.role}` : ""}
                  {n.managed ? "" : " · not one of your artists"}
                </title>
                {showLabel && (
                  <text x={r + 4 * unit} y={3.5 * unit} style={{ fontSize: 10 * unit, strokeWidth: 3 * unit }}>
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
      {colours.legend.length > 0 && (
        <ul className="graph-legend" aria-label={`Colour: ${colour}`}>
          {colours.legend.map((e) => (
            <li key={e.key}>
              <span className="swatch" style={{ background: e.colour }} />
              <span>{e.label}</span>
              <span className="mono dim">{e.count}</span>
            </li>
          ))}
        </ul>
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
