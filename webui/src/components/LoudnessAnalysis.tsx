import { useEffect } from "react";
import { api, errMsg } from "../api";
import { useFetch } from "../hooks";
import { LoudnessStatus } from "../types";
import { ProgressBar } from "./ProgressBar";
import { useToast } from "../toast";

/**
 * The *Analyse loudness* control, beside tempo's and shaped the same: one sentence, one
 * summary line, one button, for a background pass with its own schedule and Stop. It
 * counts albums, because album gain is measured an album at a time (docs/replaygain.md).
 * `version` re-reads it after a library's opt-in changes.
 */
export function LoudnessAnalysis({ version }: { version: number }) {
  const toast = useToast();
  const status = useFetch<LoudnessStatus>(() => api.get("/loudness/status"), [version]);
  const running = status.data?.running ?? false;

  useEffect(() => {
    if (!running) return;
    const t = setInterval(() => status.reload(), 3000);
    return () => clearInterval(t);
  }, [running, status.reload]);

  const start = async () => {
    try {
      await api.post("/loudness/analyse");
      toast("info", "Loudness analysis started");
      setTimeout(() => status.reload(), 400);
    } catch (e) {
      toast("err", errMsg(e));
    }
  };

  const stop = async () => {
    try {
      await api.post("/loudness/cancel");
      toast("info", "Stopping after the album being measured");
      setTimeout(() => status.reload(), 400);
    } catch (e) {
      toast("err", errMsg(e));
    }
  };

  const s = status.data;
  if (!s) return null;

  // Two different reasons the button cannot do anything, and each has a different
  // fix in a different place — so the sentence says which one, rather than a disabled
  // button that looks the same either way.
  const blocked = !s.analyser_available
    ? "rsgain is not installed on the server. Install rsgain — the Docker image already has it."
    : s.libraries === 0
      ? "No library is measured yet. Turn on Analyse loudness when editing the libraries you want measured."
      : null;

  return (
    <section className="stack" style={{ gap: 8 }}>
      <div className="eyebrow">Loudness analysis</div>
      <div className="card stack" style={{ gap: "var(--space-4)" }}>
        <div className="row" style={{ gap: "var(--space-5)", alignItems: "center" }}>
          <div className="stack" style={{ gap: "var(--space-2)", minWidth: 0 }}>
            <span style={{ color: "var(--text)", fontSize: "var(--text-sm)", maxWidth: "68ch" }}>
              {blocked ??
                `Measures ReplayGain — each track's gain and its album's — with rsgain, in ${s.libraries === 1 ? "the one library that opts in" : `the ${s.libraries} libraries that opt in`}. Each library decides whether the result is written to its files.`}
            </span>
            {!blocked && <SummaryLine status={s} />}
          </div>

          <div className="row" style={{ marginLeft: "auto", flexShrink: 0 }}>
            {running ? (
              <button
                className="btn btn-ghost btn-sm"
                onClick={stop}
                title="Stops once the album being measured now is done. Nothing is lost — the next pass skips every album already measured."
              >
                Stop
              </button>
            ) : (
              <button
                className="btn btn-secondary btn-sm"
                disabled={blocked !== null}
                onClick={start}
                title={
                  blocked ??
                  "Measures every album not measured yet, or changed since. Reads audio only: no file is written. Albums already measured are skipped, so after the first pass this costs only what is new."
                }
              >
                Analyse loudness
              </button>
            )}
          </div>
        </div>

        {s.last_error && (
          <span className="dim" style={{ fontSize: "var(--text-xs)" }} title={s.last_error}>
            Last error: {s.last_error}
          </span>
        )}
      </div>
      <span className="dim" style={{ fontSize: 11 }}>
        Runs on its own schedule (Settings → Loudness analysis) and pauses while files are being written. An
        album is measured again when a track's audio changes or a track is added or removed.
      </span>
    </section>
  );
}

/**
 * What the pass is doing, or what is on record. Figures in mono, words in sans, the
 * way every count in the app is set.
 */
function SummaryLine({ status }: { status: LoudnessStatus }) {
  if (status.running) {
    return (
      <div className="row" style={{ gap: "var(--space-5)", alignItems: "center" }}>
        <ProgressBar done={status.done} total={status.total} width={180} showPercent={false} />
        <span className="dim mono" style={{ fontSize: "var(--text-xs)" }}>
          {status.done.toLocaleString()} / {status.total.toLocaleString()}
        </span>
        {status.paused && (
          <span className="dim" style={{ fontSize: "var(--text-xs)" }}>
            paused while files are being written
          </span>
        )}
      </div>
    );
  }

  return (
    <span className="dim" style={{ fontSize: "var(--text-xs)" }}>
      <span className="mono">{status.stored.toLocaleString()}</span> files measured
      {" · "}
      {status.finished_at
        ? `last run ${new Date(status.finished_at).toLocaleString()}`
        : "not run since the service started — the schedule will start one"}
      {status.finished_at && status.analysed > 0 && (
        <>
          {" · "}
          <span className="mono">{status.analysed.toLocaleString()}</span> album(s) measured
        </>
      )}
      {status.failed > 0 && (
        <>
          {" · "}
          <span style={{ color: "var(--danger-text)" }}>
            <span className="mono">{status.failed}</span> album(s) failed
          </span>
        </>
      )}
    </span>
  );
}
