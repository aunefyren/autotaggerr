import { memo, PointerEvent, useEffect, useMemo, useRef, useState } from "react";
import { Link } from "react-router-dom";
import { GraphEdge, GraphNode, GraphReport } from "../types";
import { computeLayout, LayoutInput } from "./graphLayout";

/**
 * The collaboration graph: artists as nodes sized by how many recordings in scope
 * credit them, edges weighted by how many they share. Laid out once per answer with
 * d3-force (layout only — the drawing is plain SVG on the theme's tokens), so the
 * picture does not jiggle. Past a few hundred artists the layout runs in a Web Worker
 * (graphLayout.ts), and the edge and node layers are memoised on the layout and the
 * focus, so panning and zooming redraw only the labels.
 *
 * The side panel is the same data as a list, and is the non-visual way in: every
 * artist and pair the picture shows can be reached and acted on from there.
 */

type SimNode = GraphNode & { x: number; y: number };

interface Laid {
  nodes: SimNode[];
  radius: (n: GraphNode) => number;
  links: { source: SimNode; target: SimNode; weight: number; kind?: string }[];
  box: { x: number; y: number; w: number; h: number };
}

/**
 * Node radius from its track count, relative to the largest artist in view: area
 * follows the count, from 3 to 20. An absolute scale capped at the top made every
 * node the same size once performers and producers were in — each on hundreds of
 * recordings — and the graph packed into a uniform blob.
 */
function radiusScale(nodes: GraphNode[], size: GraphSize): (n: GraphNode) => number {
  const of = sizeOf(size);
  const most = Math.max(1, ...nodes.map(of));
  return (n) => 3 + 17 * Math.sqrt(of(n) / most);
}

/** What node size shows: recordings in scope, or the listener's plays. */
export type GraphSize = "tracks" | "plays";

const sizeOf = (size: GraphSize) => (n: GraphNode) => (size === "plays" ? (n.plays ?? 0) : n.tracks);

/** Edge width from its weight, compressed so one heavy pair does not swamp the rest. */
function edgeWidth(weight: number): number {
  return Math.min(6, 0.75 + Math.log2(weight) * 0.9);
}

/** The most a graph is magnified to fill the canvas. */
const MAX_ZOOM = 2;

/** How many of the largest nodes carry a permanent label; the rest label on hover. */
const LABELLED = 24;

/** At or below this many artists the layout runs inline: it settles in a few hundred ms. */
const INLINE_LAYOUT = 150;

function layoutInput(report: GraphReport, size: GraphSize): LayoutInput {
  const radius = radiusScale(report.nodes, size);
  // Co-listening drawn over credits (Edges: Both) does not pull the layout: the credits
  // chose and placed the artists. Asked once, not per edge.
  const mixed = report.edges.some((e) => e.kind !== "listening");
  return {
    nodes: report.nodes.map((n) => ({ id: n.mb_id, hop: n.hop, r: radius(n) })),
    links: report.edges.map((e: GraphEdge) => ({
      source: e.source,
      target: e.target,
      weight: e.weight,
      strength: e.kind === "listening" && mixed ? 0 : Math.min(1, 0.15 + e.weight / 20),
    })),
    centred: report.center != null,
  };
}

/** The drawable graph from the layout's positions (input order, see computeLayout). */
function assemble(report: GraphReport, size: GraphSize, positions: Float64Array): Laid {
  const radius = radiusScale(report.nodes, size);
  const nodes: SimNode[] = report.nodes.map((n, i) => ({ ...n, x: positions[2 * i], y: positions[2 * i + 1] }));
  const byId = new Map(nodes.map((n) => [n.mb_id, n]));
  const links: Laid["links"] = [];
  for (const e of report.edges) {
    const source = byId.get(e.source), target = byId.get(e.target);
    if (source && target) links.push({ source, target, weight: e.weight, kind: e.kind });
  }

  let minX = Infinity, minY = Infinity, maxX = -Infinity, maxY = -Infinity;
  for (const n of nodes) {
    const r = radius(n) + 40; // room for the label beside the node
    minX = Math.min(minX, n.x - r);
    minY = Math.min(minY, n.y - r);
    maxX = Math.max(maxX, n.x + r);
    maxY = Math.max(maxY, n.y + r);
  }
  if (!isFinite(minX)) (minX = -100), (minY = -100), (maxX = 100), (maxY = 100);
  return { nodes, radius, links, box: { x: minX, y: minY, w: maxX - minX, h: maxY - minY } };
}

/**
 * One worker, made on first need; null where there are none. A job still running when a
 * newer one arrives is cancelled by ending the worker: a superseded 2,000-artist layout
 * would otherwise hold the queue for seconds.
 */
let worker: Worker | null | undefined;
let jobs = 0;
let running = 0;
function layoutWorker(): Worker | null {
  if (worker === undefined) {
    try {
      worker = new Worker(new URL("./graphLayout.worker.ts", import.meta.url), { type: "module" });
    } catch {
      worker = null;
    }
  }
  return worker;
}

/**
 * The laid-out graph for an answer. Small graphs are laid out inline; large ones in the
 * worker, and until it answers the previous picture stays up (pending says so) — or
 * nothing, on the first. A newer answer supersedes a job still running.
 */
function useLayout(report: GraphReport, size: GraphSize): { laid: Laid | null; pending: boolean } {
  const inline = report.nodes.length <= INLINE_LAYOUT || layoutWorker() === null;
  const sync = useMemo(() => (inline ? assemble(report, size, computeLayout(layoutInput(report, size))) : null), [inline, report, size]);
  const [async, setAsync] = useState<{ report: GraphReport; size: GraphSize; laid: Laid } | null>(null);
  useEffect(() => {
    if (inline) return;
    if (running !== 0 && worker) {
      worker.terminate();
      worker = undefined;
    }
    const w = layoutWorker();
    if (!w) return;
    const job = ++jobs;
    running = job;
    const onMessage = (e: MessageEvent<{ job: number; positions: Float64Array }>) => {
      if (e.data.job !== job) return;
      running = 0;
      setAsync({ report, size, laid: assemble(report, size, e.data.positions) });
    };
    w.addEventListener("message", onMessage);
    w.postMessage({ job, input: layoutInput(report, size) });
    return () => w.removeEventListener("message", onMessage);
  }, [inline, report, size]);
  if (sync) return { laid: sync, pending: false };
  const current = async?.report === report && async.size === size;
  return { laid: async?.laid ?? null, pending: !current };
}

/** What nodes are coloured by. */
export type GraphColour = "genre" | "role" | "plays" | "none";

/**
 * Play bins for Colour: Plays. Log-spaced, because plays are heavy-tailed: a few
 * artists hold most of them, and linear bins would put everyone else in the first.
 * The edges are relative to the most-played artist in view, so the ramp always
 * spans the picture.
 */
function playBins(most: number): number[] {
  if (most <= 1) return [1];
  const edges: number[] = [];
  for (let i = 0; i < 5; i++) edges.push(Math.max(1, Math.round(Math.pow(most, i / 5))));
  return [...new Set(edges)];
}

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
  /** Drawn as a ring rather than a disc: the category is "not known". */
  hollow?: boolean;
}

/** How many genres get a colour of their own (insights.ColouredGenres). */
const GENRE_SLOTS = 11;

/**
 * The colour of each node, and the legend for it. A category keeps its colour
 * whatever the filters show: genres are slotted by the library's own ranking
 * (report.genres), roles by a fixed order. Genres take all eleven series, then the
 * neutral for any other genre; an artist with no genre at all is a hollow ring, so
 * missing data reads apart from "some other genre". Roles take three.
 */
export function graphColours(report: GraphReport, by: GraphColour): {
  of: (n: GraphNode) => string | undefined;
  hollow: (n: GraphNode) => boolean;
  legend: LegendEntry[];
} {
  if (by === "none") return { of: () => undefined, hollow: () => false, legend: [] };
  if (by === "plays") {
    // Sequential: one hue, darker for fewer plays. Unplayed is a neutral ring, so
    // "none" reads apart from "a few".
    const most = Math.max(0, ...report.nodes.map((n) => n.plays ?? 0));
    const bins = playBins(most);
    const step = (p: number) => {
      let i = 0;
      while (i + 1 < bins.length && p >= bins[i + 1]) i++;
      return i + (5 - bins.length);
    };
    const counts = new Array(5).fill(0);
    let unplayed = 0;
    for (const n of report.nodes) {
      if (!n.plays) unplayed++;
      else counts[step(n.plays)]++;
    }
    const legend: LegendEntry[] = [];
    bins.forEach((lo, i) => {
      const k = i + (5 - bins.length);
      const hi = i + 1 < bins.length ? bins[i + 1] - 1 : most;
      legend.push({ key: `p${k}`, label: lo === hi ? `${lo} ${lo === 1 ? "play" : "plays"}` : `${lo}–${hi} plays`, colour: `var(--seq-${k + 1})`, count: counts[k] });
    });
    legend.reverse();
    if (unplayed > 0) legend.push({ key: "unplayed", label: "Not played", colour: "var(--series-other)", count: unplayed, hollow: true });
    return {
      of: (n) => (n.plays ? `var(--seq-${step(n.plays) + 1})` : "var(--series-other)"),
      hollow: (n) => !n.plays,
      legend,
    };
  }
  const slots = by === "genre" ? GENRE_SLOTS : 3;
  const slot = (i: number) => (i >= 0 && i < slots ? `var(--series-${i + 1})` : "var(--series-other)");
  let keyOf: (n: GraphNode) => string;
  let labelOf: (key: string) => string;
  let order: string[];
  // The key of "no genre known", which only genre colouring has.
  const UNKNOWN = "\u0000unknown";
  if (by === "genre") {
    order = report.genres.slice(0, GENRE_SLOTS);
    keyOf = (n) => (!n.genre ? UNKNOWN : order.includes(n.genre) ? n.genre : "");
    labelOf = (k) => (k === UNKNOWN ? "No genre known" : k || "Other genres");
  } else {
    order = ROLE_SLOTS.map((r) => r.role);
    keyOf = (n) => (n.role && order.includes(n.role) ? n.role : n.role ? "writing" : "");
    labelOf = (k) => ROLE_SLOTS.find((r) => r.role === k)?.label ?? (k === "writing" ? "Writing" : "Unknown");
  }
  const colourOf = (key: string) => slot(order.indexOf(key));
  const counts = new Map<string, number>();
  for (const n of report.nodes) counts.set(keyOf(n), (counts.get(keyOf(n)) ?? 0) + 1);
  // The coloured categories in slot order, then Other, then unknown last.
  const rest = [...counts.keys()].filter((k) => !order.includes(k)).sort((a, b) => (a === UNKNOWN ? 1 : b === UNKNOWN ? -1 : 0));
  const keys = [...order.filter((k) => counts.has(k)), ...rest];
  return {
    of: (n) => colourOf(keyOf(n)),
    hollow: (n) => keyOf(n) === UNKNOWN,
    legend: keys.map((k) => ({
      key: k === UNKNOWN ? "unknown" : k || "other",
      label: labelOf(k),
      colour: colourOf(k),
      count: counts.get(k) ?? 0,
      hollow: k === UNKNOWN,
    })),
  };
}

interface GraphProps {
  report: GraphReport;
  selected: string | null;
  onSelect: (mbid: string | null) => void;
  colour?: GraphColour;
  /** What node size shows. */
  size?: GraphSize;
  /** Bring an artist into view: pan to them and zoom in. A new `at` repeats it. */
  reveal?: { mbid: string; at: number } | null;
}

export function CollabGraph(props: GraphProps) {
  const { laid, pending } = useLayout(props.report, props.size ?? "tracks");
  if (!laid) {
    return (
      <div className="graph-canvas graph-pending">
        <span className="dim">Laying out {props.report.nodes.length.toLocaleString()} artists…</span>
      </div>
    );
  }
  return <GraphCanvas {...props} laid={laid} pending={pending} />;
}

function GraphCanvas({
  report,
  selected,
  onSelect,
  colour = "none",
  size = "tracks",
  reveal = null,
  laid,
  pending,
}: GraphProps & { laid: Laid; pending: boolean }) {
  const colours = useMemo(() => graphColours(report, colour), [report, colour]);
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

  // Found by the search: centre on the artist, zoomed in to about a third of the
  // fitted graph so their neighbourhood is readable, never past the zoom cap.
  useEffect(() => {
    if (!reveal) return;
    const n = laid.nodes.find((x) => x.mb_id === reveal.mbid);
    if (!n || n.x === undefined || n.y === undefined) return;
    const w = Math.max(canvas.w / MAX_ZOOM, fitted.w / 3);
    const h = Math.max(canvas.h / MAX_ZOOM, fitted.h / 3);
    setView({ x: n.x - w / 2, y: n.y - h / 2, w, h });
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [reveal]);

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
    const of = sizeOf(size);
    const top = [...laid.nodes].sort((a, b) => of(b) - of(a)).slice(0, count);
    return new Set(top.map((n) => n.mb_id));
  }, [laid, size]);

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
    <div className={`graph-canvas${pending ? " is-laying-out" : ""}`} ref={wrap}>
      {pending && (
        <span className="graph-note dim" role="status">
          Laying out {report.nodes.length.toLocaleString()} artists…
        </span>
      )}
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
        <EdgeLayer laid={laid} focus={focus} />
        <NodeLayer
          laid={laid}
          focus={focus}
          selected={selected}
          neighbours={neighbours}
          colours={colours}
          colour={colour}
          centred={report.center != null}
          playsShown={!!report.listening && !report.listening.off}
          onHover={setHover}
          onSelect={onSelect}
        />
        <g className="g-labels">
          {laid.nodes.map((n) => {
            const isFocus = n.mb_id === focus;
            // hop is 0 for every node of an uncentred graph, so it only names the
            // centre when there is one.
            const isCentre = report.center != null && n.hop === 0;
            const showLabel = isFocus || (focus ? neighbours.has(n.mb_id) : labelled.has(n.mb_id) || isCentre);
            if (!showLabel) return null;
            return (
              <g key={n.mb_id} className={`g-node${isFocus || n.mb_id === selected ? " focus" : ""}`} transform={`translate(${n.x},${n.y})`}>
                <text x={laid.radius(n) + 4 * unit} y={3.5 * unit} style={{ fontSize: 10 * unit, strokeWidth: 3 * unit }}>
                  {n.name}
                </text>
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
              <span className={`swatch${e.hollow ? " hollow" : ""}`} style={e.hollow ? { borderColor: e.colour } : { background: e.colour }} />
              <span>{e.label}</span>
              <span className="mono dim">{e.count}</span>
            </li>
          ))}
        </ul>
      )}
    </div>
  );
}

/**
 * The edges. Memoised on the layout and the focus alone, so panning and zooming —
 * which change only the viewBox and the label size — do not redraw thousands of lines.
 */
const EdgeLayer = memo(function EdgeLayer({ laid, focus }: { laid: Laid; focus: string | null }) {
  return (
    <g>
      {laid.links.map((l) => {
        const lit = focus != null && (l.source.mb_id === focus || l.target.mb_id === focus);
        return (
          <line
            key={`${l.kind ?? "credit"}-${l.source.mb_id}-${l.target.mb_id}`}
            className={`g-edge${lit ? " lit" : focus ? " faded" : ""}${l.kind === "listening" ? " listening" : ""}`}
            x1={l.source.x}
            y1={l.source.y}
            x2={l.target.x}
            y2={l.target.y}
            strokeWidth={edgeWidth(l.weight)}
          >
            <title>
              {l.kind === "listening"
                ? `${l.source.name} × ${l.target.name}: played close together in ${l.weight} ${l.weight === 1 ? "session" : "sessions"}`
                : `${l.source.name} × ${l.target.name}: ${l.weight} shared ${l.weight === 1 ? "recording" : "recordings"}`}
            </title>
          </line>
        );
      })}
    </g>
  );
});

/** The nodes, without labels (those follow the zoom, in their own layer). Memoised like EdgeLayer. */
const NodeLayer = memo(function NodeLayer({
  laid,
  focus,
  selected,
  neighbours,
  colours,
  colour,
  centred,
  playsShown,
  onHover,
  onSelect,
}: {
  laid: Laid;
  focus: string | null;
  selected: string | null;
  neighbours: Set<string>;
  colours: ReturnType<typeof graphColours>;
  colour: GraphColour;
  centred: boolean;
  playsShown: boolean;
  onHover: (mbid: string | null) => void;
  onSelect: (mbid: string | null) => void;
}) {
  return (
    <g>
      {laid.nodes.map((n) => {
        const r = laid.radius(n);
        const isFocus = n.mb_id === focus;
        const dim = focus != null && !neighbours.has(n.mb_id);
        return (
          <g
            key={n.mb_id}
            // Not one of your artists: drawn quieter, so with everyone shown your own
            // still stand out. The tooltip says it in words too.
            className={`g-node${isFocus || n.mb_id === selected ? " focus" : ""}${n.hop === 0 && centred ? " center" : ""}${dim ? " faded" : ""}${n.managed ? "" : " unmanaged"}`}
            transform={`translate(${n.x},${n.y})`}
            onPointerEnter={() => onHover(n.mb_id)}
            onPointerLeave={() => onHover(null)}
            onPointerDown={(e) => e.stopPropagation()}
            onPointerUp={(e) => e.stopPropagation()}
            onClick={() => onSelect(n.mb_id === selected ? null : n.mb_id)}
          >
            {/* A hit target larger than the mark, so small nodes are clickable. */}
            <circle className="hit" r={Math.max(r, 8)} />
            {colours.hollow(n) ? (
              <circle className="unknown" r={r} style={{ stroke: colours.of(n) }} />
            ) : (
              <circle r={r} style={colours.of(n) ? { fill: colours.of(n) } : undefined} />
            )}
            <title>
              {`${n.name}: ${n.tracks} ${n.tracks === 1 ? "recording" : "recordings"}`}
              {colour === "genre" && n.genre ? ` · ${n.genre}` : ""}
              {colour === "role" && n.role ? ` · mostly ${n.role}` : ""}
              {playsShown ? ` · ${n.plays ?? 0} ${n.plays === 1 ? "play" : "plays"}` : ""}
              {n.managed ? "" : " · not one of your artists"}
            </title>
          </g>
        );
      })}
    </g>
  );
});

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
