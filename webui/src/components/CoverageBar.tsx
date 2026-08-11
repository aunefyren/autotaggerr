/**
 * The coverage meter: how much of a thing is on disk.
 *
 * This is the app's signature browsing element, and it is the same shape at every
 * level — albums of an artist, tracks of an album, tracks of one edition — so a
 * user learns to read it once. It reuses the existing diff colour language rather
 * than inventing a palette: owned is the "added" green, partial is warning, missing
 * is dim.
 *
 * Below the cell cap it draws one cell per item, because a segmented bar answers
 * "how many" as well as "how much" — 9 of 12 albums reads differently from 75%.
 * Above the cap it collapses to a proportional bar, since 200 two-pixel slivers
 * are noise.
 *
 * The cap is the smaller of 30 and **what the given width can hold**. A count-only
 * cap has no idea how much room it was given: cells are `flex: none`, so 20 of them
 * in a 90px box did not shrink or wrap, they simply drew 118px of meter and slid out
 * from under the number beside them. The shape follows the count *and* the space.
 */
const MAX_CELLS = 30;
/** `.coverage-cell` width and `.coverage` gap in theme.css — kept in step by hand. */
const CELL_PX = 4;
const GAP_PX = 2;

export function CoverageBar({
  total,
  owned,
  partial = 0,
  label,
  width,
  proportional = false,
}: {
  total: number;
  /** Items fully on disk. */
  owned: number;
  /** Items partly on disk — counted after the owned ones. */
  partial?: number;
  /** Read out to assistive tech and shown on hover. Say what the items are. */
  label: string;
  width?: number | string;
  /**
   * Force the proportional track, whatever the count. For a *column* of meters, where
   * one shape all the way down is worth more than the cell count on the short rows —
   * the mono `8/12` beside it already answers "how many".
   */
  proportional?: boolean;
}) {
  if (total <= 0) {
    return <span className="dim mono" style={{ fontSize: 11 }}>—</span>;
  }

  const full = Math.max(0, Math.min(owned, total));
  const half = Math.max(0, Math.min(partial, total - full));
  const title = `${label}: ${full} of ${total} on disk${half > 0 ? `, ${half} partial` : ""}`;

  const common = {
    className: "coverage",
    style: width !== undefined ? { width } : undefined,
    role: "img" as const,
    "aria-label": title,
    title,
  };

  // A non-numeric width (a percentage, or none at all) cannot be budgeted, so it keeps
  // the plain count cap — those call sites size themselves to their content.
  const fits =
    typeof width === "number" ? Math.max(1, Math.floor((width + GAP_PX) / (CELL_PX + GAP_PX))) : MAX_CELLS;

  if (proportional || total > Math.min(MAX_CELLS, fits)) {
    const pct = (n: number) => `${(n / total) * 100}%`;
    return (
      <span {...common}>
        <span className="coverage-track">
          <i className="coverage-fill full" style={{ width: pct(full) }} />
          <i className="coverage-fill half" style={{ width: pct(half) }} />
        </span>
      </span>
    );
  }

  return (
    <span {...common}>
      {Array.from({ length: total }, (_, i) => (
        <i key={i} className={`coverage-cell ${i < full ? "full" : i < full + half ? "half" : "none"}`} />
      ))}
    </span>
  );
}

/**
 * The per-item disk marker: ○ none, ◐ partial, ● complete. Paired with the
 * coverage bar so a single row states its own state without one — colour is never
 * the only signal, and the glyph carries it.
 */
export function DiskMarker({ owned, complete, what }: { owned: boolean; complete: boolean; what: string }) {
  const glyph = !owned ? "○" : complete ? "●" : "◐";
  const cls = !owned ? "none" : complete ? "full" : "half";
  const title = !owned ? `No ${what} on disk` : complete ? `Complete on disk` : `Partly on disk`;
  return (
    <span className={`disk-marker ${cls}`} title={title} role="img" aria-label={title}>
      {glyph}
    </span>
  );
}
