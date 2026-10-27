import { useEffect } from "react";
import { api, errMsg } from "../api";
import { useFetch } from "../hooks";
import { TempoStatus } from "../types";
import { ProgressBar } from "./ProgressBar";
import { useToast } from "../toast";

/**
 * The *Analyse tempo* control, in its own card under the libraries table.
 *
 * On the Libraries page because that is where the opt-in is: each library says whether
 * it is analysed, and this acts on the ones that do. It is shaped like the artwork
 * control on Data sources — one sentence, one summary line, one button — because it is
 * the same kind of thing: a background pass with its own schedule, its own runner and
 * its own Stop, reporting to Activity.
 *
 * No confirm dialog. A first pass is long but it reads audio and writes only a table,
 * it can be stopped at any point, and stopping loses nothing — the style guide keeps
 * confirms for what overwrites or discards.
 *
 * `version` lets the page ask for a re-read after a library's opt-in changes, so the
 * sentence here never disagrees with the table above it.
 */
export function TempoAnalysis({ version }: { version: number }) {
  const toast = useToast();
  const status = useFetch<TempoStatus>(() => api.get("/tempo/status"), [version]);
  const running = status.data?.running ?? false;

  useEffect(() => {
    if (!running) return;
    const t = setInterval(() => status.reload(), 3000);
    return () => clearInterval(t);
  }, [running, status.reload]);

  const start = async () => {
    try {
      await api.post("/tempo/analyse");
      toast("info", "Tempo analysis started");
      setTimeout(() => status.reload(), 400);
    } catch (e) {
      toast("err", errMsg(e));
    }
  };

  const stop = async () => {
    try {
      await api.post("/tempo/cancel");
      toast("info", "Stopping after the files being analysed");
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
  const blocked = !s.detector_available
    ? "aubiotrack is not installed on the server. Install aubio (aubio-tools on Debian/Ubuntu) — the Docker image already has it."
    : s.libraries === 0
      ? "No library is analysed yet. Turn on Analyse tempo when editing the libraries you want measured."
      : null;

  return (
    <section className="stack" style={{ gap: 8 }}>
      <div className="eyebrow">Tempo analysis</div>
      <div className="card stack" style={{ gap: "var(--space-4)" }}>
        <div className="row" style={{ gap: "var(--space-5)", alignItems: "center" }}>
          <div className="stack" style={{ gap: "var(--space-2)", minWidth: 0 }}>
            <span style={{ color: "var(--text)", fontSize: "var(--text-sm)", maxWidth: "68ch" }}>
              {blocked ??
                `Measures each file's BPM from the audio, in ${s.libraries === 1 ? "the one library that opts in" : `the ${s.libraries} libraries that opt in`}. Each library decides whether the result is written to its files.`}
            </span>
            {!blocked && <SummaryLine status={s} />}
          </div>

          <div className="row" style={{ marginLeft: "auto", flexShrink: 0 }}>
            {running ? (
              <button
                className="btn btn-ghost btn-sm"
                onClick={stop}
                title="Stops once the files being analysed now are done. Nothing is lost — the next pass skips every file already measured."
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
                  "Analyses every file not measured yet. Reads audio only: no file is written. Files already analysed are skipped, so after the first pass this costs only what is new."
                }
              >
                Analyse tempo
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
        Runs on its own schedule (Settings → Background jobs) and pauses while files are being written.
        Half- and double-time readings are stored as measured, not corrected.
      </span>
    </section>
  );
}

/**
 * What the pass is doing, or what is on record. Figures in mono, words in sans, the
 * way every count in the app is set.
 */
function SummaryLine({ status }: { status: TempoStatus }) {
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
      <span className="mono">{status.stored.toLocaleString()}</span> files with a tempo
      {" · "}
      {status.finished_at
        ? `last run ${new Date(status.finished_at).toLocaleString()}`
        : "not run since the service started — the schedule will start one"}
      {status.finished_at && status.analysed > 0 && (
        <>
          {" · "}
          <span className="mono">{status.analysed.toLocaleString()}</span> analysed
        </>
      )}
      {status.failed > 0 && (
        <>
          {" · "}
          <span style={{ color: "var(--danger-text)" }}>
            <span className="mono">{status.failed}</span> failed
          </span>
        </>
      )}
    </span>
  );
}
