import { useEffect, useState } from "react";
import { api, errMsg } from "../api";
import { useFetch } from "../hooks";
import { ArtworkStatus } from "../types";
import { CoverageBar } from "./CoverageBar";
import { Pill } from "./ui";
import { ForceArtworkDialog } from "./ForceArtworkDialog";
import { useToast } from "../toast";

/**
 * The *Refresh artwork* control, and how the current pass is going.
 *
 * It lives on Data sources rather than on a page of its own because the trigger
 * belongs next to the thing it configures — artwork providers are set up here, and
 * this is the one action that uses them. It is also genuinely niche: new artists and
 * albums fetch their own images as they arrive, and the schedule covers expiry, so
 * this is for kicking off the first pass on an existing install rather than something
 * to press regularly. Hence a secondary button and a quiet strip, not a page.
 */
export function ArtworkRefresh() {
  const toast = useToast();
  const status = useFetch<ArtworkStatus>(() => api.get("/artwork/status"));
  const running = status.data?.running ?? false;
  const [force, setForce] = useState(false);
  const [confirming, setConfirming] = useState(false);

  // Poll while a pass runs. Three seconds matches the Metadata page; the unit of work
  // is a throttled request either way.
  useEffect(() => {
    if (!running) return;
    const t = setInterval(() => status.reload(), 3000);
    return () => clearInterval(t);
  }, [running, status.reload]);

  const s = status.data;
  // Nothing to offer when no provider can serve an image: the button would start a
  // pass that is certain to fetch nothing. Saying why beats a disabled control with
  // no explanation, and the panels above are where the fix is.
  const capable = (s?.covers_enabled ?? false) || (s?.artist_enabled ?? false);

  /**
   * Starts a pass, and puts the forced one behind the confirm dialog.
   *
   * The reset afterwards is the other half of making forcing deliberate — the
   * checkbox is a modifier on a button pressed again later, so leaving it ticked
   * turns one considered decision into a setting. It resets whether or not the
   * request succeeded, because what must not persist is the intent.
   */
  const start = async () => {
    setConfirming(false);
    try {
      await api.post(`/artwork/refresh${force ? "?force=true" : ""}`);
      toast("info", force ? "Full artwork refresh started — cached images ignored" : "Artwork refresh started");
      setTimeout(() => status.reload(), 400);
    } catch (e) {
      toast("err", errMsg(e));
    } finally {
      setForce(false);
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
    <section className="stack" style={{ gap: 8 }}>
      <div className="row" style={{ justifyContent: "space-between", alignItems: "center" }}>
        <div className="eyebrow">Artwork refresh</div>
        <div className="row">
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
              // Only the forced pass confirms. A dialog on the ordinary one would
              // train people to click through the dialog that matters.
              onClick={() => (force ? setConfirming(true) : start())}
              title="Fetches covers and artist images for everything in the collection, so pages paint from disk. Reads only: none of your audio files are touched."
            >
              Refresh artwork
            </button>
          )}
        </div>
      </div>

      <p className="muted" style={{ margin: 0, maxWidth: "68ch", fontSize: 12 }}>
        {capable ? (
          <>
            Images are fetched ahead of the pages that show them, so opening an artist does not
            download a hundred thumbnails while you wait. New artists and albums fetch their own as
            they arrive and the rest is topped up on a schedule — this button is for filling a cold
            cache now.
          </>
        ) : (
          <>Set up an artwork provider above to fetch covers and artist images.</>
        )}
      </p>

      {confirming && (
        <ForceArtworkDialog
          images={s.images}
          missing={s.missing_cached}
          onCancel={() => setConfirming(false)}
          onConfirm={start}
        />
      )}

      {capable && (
        <div className="row" style={{ gap: 8, alignItems: "center" }}>
          <label className="row" style={{ gap: 6, alignItems: "center", fontSize: 12 }}>
            <input
              type="checkbox"
              checked={force}
              disabled={running}
              onChange={(e) => setForce(e.target.checked)}
            />
            Ignore cached images
          </label>
          <span className="dim" style={{ fontSize: 11 }}>
            {force
              ? "Downloads every image again and re-asks about the ones with none — much slower, and how newly uploaded art is found."
              : "Only fetches what is missing or expired."}
          </span>
        </div>
      )}

      <div className="row" style={{ gap: 18, flexWrap: "wrap", alignItems: "center" }}>
        {running ? (
          <>
            <Pill kind="scan">Fetching</Pill>
            <CoverageBar total={s.total} owned={s.done} label="images this pass" width={200} />
            <span className="mono dim" style={{ fontSize: 11 }}>
              {s.done} / {s.total}
            </span>
          </>
        ) : (
          <span className="dim mono" style={{ fontSize: 11 }}>
            {s.finished_at
              ? `last run ${new Date(s.finished_at).toLocaleString()}`
              : "never run — the schedule or a new artist will start one"}
          </span>
        )}
      </div>

      <div className="row" style={{ gap: 18, flexWrap: "wrap" }}>
        <Stat n={s.images} l="images cached" hint="On disk and served without a request" />
        {/* The figure that explains a screen full of monogram tiles: the provider was
            asked and had nothing. Without it, "no image" is indistinguishable from
            "not fetched yet". */}
        <Stat
          n={s.missing_cached}
          l="with no artwork"
          hint="The provider was asked and has no image for these. Re-checked weekly, and immediately by a forced refresh."
        />
        {s.errors > 0 && <Stat n={s.errors} l="failed" hint="Logged and skipped; the pass continues" />}
      </div>

      {s.last_error && (
        <div className="dim" style={{ fontSize: 11 }} title={s.last_error}>
          Last error: {s.last_error}
        </div>
      )}
    </section>
  );
}

function Stat({ n, l, hint }: { n: number; l: string; hint?: string }) {
  return (
    <div title={hint}>
      <div className="mono" style={{ fontSize: 18 }}>
        {n.toLocaleString()}
      </div>
      <div className="dim" style={{ fontSize: 11 }}>
        {l}
      </div>
    </div>
  );
}
