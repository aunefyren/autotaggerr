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
 */
const MAX_CELLS = 30;

export function CoverageBar({
  total,
  owned,
  partial = 0,
  label,
  width,
}: {
  total: number;
  /** Items fully on disk. */
  owned: number;
  /** Items partly on disk — counted after the owned ones. */
  partial?: number;
  /** Read out to assistive tech and shown on hover. Say what the items are. */
  label: string;
  width?: number | string;
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

  if (total > MAX_CELLS) {
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
