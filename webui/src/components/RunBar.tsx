import { ReactNode } from "react";
import { Link } from "react-router-dom";
import { ScanStatus } from "../types";
import { ProgressBar } from "./ProgressBar";
import { PHASE_LABELS, phaseDrivesProgress } from "./phases";

/**
 * The verbs a surface offers, and the one status they share.
 *
 * Every verb here is drained by a single serial job queue, so a single bar is their
 * honest shape: one row of controls with one state, rather than four buttons that dim
 * together for a reason stated in none of their labels. Controls dim for two different
 * reasons — a job holds them, or their input does not exist yet — and a disabled button
 * looks identical either way, so the bar says which it is in words.
 *
 * Shared by the collection and one artist because the verbs are the same four at both
 * scopes and the queue behind them is literally the same queue. What differs is only
 * what there is to say when nothing is running, which is the caller's `idle`.
 */
export function RunBar({
  status,
  idle,
  children,
}: {
  status?: ScanStatus | null;
  /** What the bar says when no job is in flight. Scope-specific, so the caller owns it. */
  idle: ReactNode;
  /** The verbs, in the documented cheapest-first order. */
  children: ReactNode;
}) {
  const running = status?.running ?? false;
  // Whether the running job's counters describe the stage it is actually in. A run
  // counts files, and only its walk moves that number, so the bar goes striped rather
  // than sitting frozen at 0% through minutes of rate-limited metadata work.
  const counted = phaseDrivesProgress(status?.phase);
  const total = status?.total ?? 0;

  return (
    <div className="runbar">
      <div className="runbar-state">
        <span className="eyebrow">Run</span>
        {running ? (
          <>
            {/* The job's own title and stage, in the same words the Activity banner
                uses — a run reads the same wherever it is reported. Naming the job
                rather than saying "working" is what makes the dimmed buttons legible:
                it is *that* run holding them. */}
            <Link to="/activity" className="runbar-status" title="A job is running — open Activity">
              {status?.current_job?.title ?? "Working…"}
            </Link>
            {status?.phase && (
              <span className="dim" style={{ fontSize: 11 }}>
                {PHASE_LABELS[status.phase] ?? status.phase}
              </span>
            )}
            {(!counted || total > 0) && (
              <ProgressBar
                done={status?.done ?? 0}
                total={total}
                width={120}
                showPercent={false}
                indeterminate={!counted}
              />
            )}
            {counted && total > 0 && (
              <span className="mono dim" style={{ fontSize: 11 }}>
                {status?.done ?? 0} / {total}
              </span>
            )}
          </>
        ) : (
          <span className="runbar-status">{idle}</span>
        )}
      </div>
      <div className="runbar-verbs">{children}</div>
    </div>
  );
}
