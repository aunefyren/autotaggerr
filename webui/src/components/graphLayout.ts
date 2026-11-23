import { forceCollide, forceLink, forceManyBody, forceRadial, forceSimulation, forceX, forceY, SimulationLinkDatum, SimulationNodeDatum } from "d3-force";

/**
 * The collaboration graph's force layout, with no React and no DOM, so it runs the
 * same in a Web Worker (graphLayout.worker.ts) as on the main thread. A thousand
 * artists take seconds to settle; in a worker that is a "laying out" note, not a
 * frozen tab.
 */

export interface LayoutNode {
  id: string;
  hop: number;
  /** Drawn radius: the collide force keeps this much room around the node. */
  r: number;
}

export interface LayoutLink {
  source: string;
  target: string;
  weight: number;
  /** Pull, 0–1. Zero for an edge that is drawn but must not move anything. */
  strength: number;
}

export interface LayoutInput {
  nodes: LayoutNode[];
  links: LayoutLink[];
  /** Centred on an artist: hop 0 is pinned in the middle, each hop on its own ring. */
  centred: boolean;
}

type SimNode = LayoutNode & SimulationNodeDatum;
type SimLink = SimulationLinkDatum<SimNode> & LayoutLink;

/**
 * How many ticks to settle for. Deterministic rather than an animated settle, so the
 * same answer always draws the same picture. Large graphs take fewer per node: the
 * picture stops changing visibly well before 400 ticks, and each tick costs O(n log n).
 */
export function layoutTicks(n: number): number {
  if (n > 1000) return 250;
  return Math.min(400, 120 + n * 2);
}

/** Positions as [x0, y0, x1, y1, …] in input order. */
export function computeLayout(input: LayoutInput): Float64Array {
  const nodes: SimNode[] = input.nodes.map((n) => ({ ...n }));
  const links: SimLink[] = input.links.map((l) => ({ ...l }));
  const sim = forceSimulation(nodes)
    .force(
      "link",
      forceLink<SimNode, SimLink>(links)
        .id((d) => d.id)
        .distance((l) => 40 + 60 / Math.sqrt(l.weight))
        .strength((l) => l.strength),
    )
    .force("charge", forceManyBody().strength(-90))
    .force("collide", forceCollide<SimNode>((d) => d.r + 3))
    .stop();
  if (input.centred) {
    for (const n of nodes) if (n.hop === 0) (n.fx = 0), (n.fy = 0);
    sim.force("radial", forceRadial<SimNode>((d) => d.hop * 150, 0, 0).strength(0.6));
  } else {
    // Gentle gravity rather than a centring force: forceCenter only moves the average,
    // so a cluster with no edge to the rest drifted off and the frame spread to hold
    // it, shrinking everything else.
    sim.force("x", forceX<SimNode>(0).strength(0.08)).force("y", forceY<SimNode>(0).strength(0.08));
  }
  sim.tick(layoutTicks(nodes.length));
  const out = new Float64Array(nodes.length * 2);
  nodes.forEach((n, i) => {
    out[2 * i] = n.x ?? 0;
    out[2 * i + 1] = n.y ?? 0;
  });
  return out;
}
