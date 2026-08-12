import { useEffect, useState } from "react";
import { api, errMsg } from "../api";
import { useFetch } from "../hooks";
import { ArtworkStatus } from "../types";
import { CoverageBar } from "./CoverageBar";
import { RefreshArtworkDialog } from "./RefreshArtworkDialog";
import { useToast } from "../toast";

/**
 * The *Refresh artwork* control, as the footer of the artwork providers card.
 *
 * It is a footer rather than a section because it is an action *on* those two
 * providers, not a peer of them. Given its own eyebrow and its own top-right button —
 * which is the page-head convention — it read as a second page heading, and the loudest
 * things on a settings page were two cache counts nobody came here to read.
 *
 * So it is deliberately quiet: one sentence, one summary line, one button. The style
 * guide's rule for this surface is that covers and their machinery earn their place on
 * browsing surfaces and stay off working ones, and settings is a working surface. The
 * numbers stay at --text-xs with the figures in mono, the way every other count in the
 * app is set, rather than as display numerals.
 */
export function ArtworkRefresh() {
  const toast = useToast();
  const status = useFetch<ArtworkStatus>(() => api.get("/artwork/status"));
  const running = status.data?.running ?? false;
  // No page state for which reading is meant: the dialog asks, and nothing is left
  // ticked afterwards.
  const [choosing, setChoosing] = useState(false);

  // Poll while a pass runs. Three seconds matches the Metadata page; the unit of work
  // is a throttled request either way.
  useEffect(() => {
    if (!running) return;
    const t = setInterval(() => status.reload(), 3000);
    return () => clearInterval(t);
  }, [running, status.reload]);

  const s = status.data;
  // Nothing to offer when no provider can serve an image: the button would start a
  // pass certain to fetch nothing. Saying why beats a disabled control with no
  // explanation, and the rows above are where the fix is.
  const capable = (s?.covers_enabled ?? false) || (s?.artist_enabled ?? false);

  const start = (force: boolean) => async () => {
    setChoosing(false);
    try {
      await api.post(`/artwork/refresh${force ? "?force=true" : ""}`);
      toast("info", force ? "Full artwork refresh started — cached images ignored" : "Artwork refresh started");
      setTimeout(() => status.reload(), 400);
    } catch (e) {
      toast("err", errMsg(e));
    }
  };

  const stop = async () => {
    try {
      await api.post("/artwork/cancel");
      toast("info", "Stopping after the current image");
      setTimeout(() => status.reload(), 400);
    } catch (e) {
      toast("err", errMsg(e));
    }
  };

  if (!s) return null;

  return (
    <div className="stack" style={{ gap: "var(--space-4)" }}>
      <div className="row" style={{ gap: "var(--space-5)", alignItems: "center" }}>
        <div className="stack" style={{ gap: "var(--space-2)", minWidth: 0 }}>
          <span style={{ color: "var(--text)", fontSize: "var(--text-sm)" }}>
            {capable
              ? "Fetched ahead of the pages that show them, so opening an artist does not download a hundred thumbnails while you wait."
              : "Set up a provider above to fetch covers and artist images."}
          </span>
          {capable && <SummaryLine status={s} running={running} />}
        </div>

        <div className="row" style={{ marginLeft: "auto", flexShrink: 0 }}>
          {running ? (
            <button
              className="btn btn-ghost btn-sm"
              onClick={stop}
              title="Stops at the next image. Nothing is lost — the next pass resumes by skipping whatever is already cached."
            >
              Stop
            </button>
          ) : (
            <button
              className="btn btn-secondary btn-sm"
              disabled={!capable}
              onClick={() => setChoosing(true)}
              title="Fetches covers and artist images for the collection. Reads only: none of your audio files are touched."
            >
              Refresh artwork
            </button>
          )}
        </div>
      </div>

      {choosing && (
        <RefreshArtworkDialog
          images={s.images}
          missing={s.missing_cached}
          onCancel={() => setChoosing(false)}
          onRefresh={start(false)}
          onForce={start(true)}
        />
      )}

      {s.last_error && (
        <span className="dim" style={{ fontSize: "var(--text-xs)" }} title={s.last_error}>
          Last error: {s.last_error}
        </span>
      )}
    </div>
  );
}

/**
 * What the cache holds, in one line.
 *
 * A derived summary rather than a row of stat blocks: these are supporting facts on a
 * settings surface, and as 18px numerals they were the first thing the eye landed on.
 * Figures in mono because that is how every count in this app is set; the words around
 * them in sans, because mono is reserved for identifiers and an English sentence in it
 * reads as debug output.
 *
 * "With no artwork" earns its place here — it is what explains a page of monogram
 * tiles, and without it "no image" is indistinguishable from "not fetched yet".
 */
function SummaryLine({ status, running }: { status: ArtworkStatus; running: boolean }) {
  if (running) {
    return (
      <div className="row" style={{ gap: "var(--space-5)", alignItems: "center" }}>
        <CoverageBar total={status.total} owned={status.done} label="images this pass" width={180} />
        <span className="dim mono" style={{ fontSize: "var(--text-xs)" }}>
          {status.done} / {status.total}
        </span>
      </div>
    );
  }

  return (
    <span className="dim" style={{ fontSize: "var(--text-xs)" }}>
      <span className="mono">{status.images.toLocaleString()}</span> cached
      {" · "}
      <span className="mono">{status.missing_cached.toLocaleString()}</span> with no artwork
      {" · "}
      {status.finished_at
        ? `last run ${new Date(status.finished_at).toLocaleString()}`
        : "never run — the schedule, or a new artist, will start one"}
      {status.errors > 0 && (
        <>
          {" · "}
          <span style={{ color: "var(--danger-text)" }}>
            <span className="mono">{status.errors}</span> failed
          </span>
        </>
      )}
    </span>
  );
}
