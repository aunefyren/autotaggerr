/**
 * A task-progress meter: how far through a long job (a scan, a metadata pass) we are.
 *
 * Deliberately separate from CoverageBar even though they share the fill visuals:
 * coverage answers "how much is on disk", progress answers "how far along". Reusing
 * the "on disk" language for a running job would read as a claim about the library.
 *
 * Reads out done/total to assistive tech. A zero or unknown total collapses to
 * nothing — a job with no counted work has no bar to draw.
 */
export function ProgressBar({
  done,
  total,
  width,
  showPercent = true,
}: {
  done: number;
  total: number;
  width?: number | string;
  showPercent?: boolean;
}) {
  if (total <= 0) return null;

  const clamped = Math.max(0, Math.min(done, total));
  const pct = Math.round((clamped / total) * 100);
  const title = `${clamped} of ${total} done (${pct}%)`;

  return (
    <span className="row" style={{ gap: 8, alignItems: "center" }}>
      <span
        className="coverage"
        style={width !== undefined ? { width } : undefined}
        role="progressbar"
        aria-valuenow={clamped}
        aria-valuemin={0}
        aria-valuemax={total}
        aria-label={title}
        title={title}
      >
        <span className="coverage-track">
          <i className="coverage-fill full" style={{ width: `${pct}%` }} />
        </span>
      </span>
      {showPercent && (
        <span className="mono dim" style={{ fontSize: 11 }}>
          {pct}%
        </span>
      )}
    </span>
  );
}
