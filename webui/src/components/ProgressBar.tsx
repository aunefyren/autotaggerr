/**
 * A task-progress meter: how far through a long job (a processing run, a metadata pass) we are.
 *
 * Deliberately separate from CoverageBar even though they share the fill visuals:
 * coverage answers "how much is on disk", progress answers "how far along". Reusing
 * the "on disk" language for a running job would read as a claim about the library.
 *
 * Reads out done/total to assistive tech. A zero or unknown total collapses to
 * nothing — a job with no counted work has no bar to draw.
 *
 * `indeterminate` is for a stage doing real work the counters do not describe: a run's
 * refresh, migration and collection stages count entities while total/done count files,
 * so a proportional bar there would sit frozen at 0% or 100% for minutes and read as a
 * hang. Striped-and-moving says "working, not counted", which is the truth.
 * See phaseDrivesProgress in ./phases.
 */
export function ProgressBar({
  done,
  total,
  width,
  showPercent = true,
  indeterminate = false,
}: {
  done: number;
  total: number;
  width?: number | string;
  showPercent?: boolean;
  indeterminate?: boolean;
}) {
  if (indeterminate) {
    return (
      <span
        className="coverage"
        style={width !== undefined ? { width } : undefined}
        role="progressbar"
        // No aria-valuenow: that is what marks a progressbar indeterminate, and it is
        // the same claim the stripes make visually.
        aria-label="Working — this stage is not counted"
        title="Working — this stage is not counted"
      >
        <span className="coverage-track indeterminate" />
      </span>
    );
  }

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
