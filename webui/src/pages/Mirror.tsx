import { useEffect, useState } from "react";
import { api, errMsg } from "../api";
import { useFetch } from "../hooks";
import { MirrorStatus } from "../types";
import { CoverageBar } from "../components/CoverageBar";
import { ErrorNote, Pill } from "../components/ui";
import { ForceRefreshDialog } from "../components/ForceRefreshDialog";
import { useToast } from "../toast";

/**
 * Refresh metadata at collection scope: what is cached locally, and how the
 * current pass is going.
 *
 * A pass over a large collection runs for hours behind a one-request-per-second
 * limiter, so the page is built around making a long job legible rather than
 * around starting one. The progress meter and the phase are the primary content;
 * the trigger is secondary, because the nightly schedule is what normally does it.
 *
 * The no-writes contract is stated on the page rather than left to documentation.
 * It is the difference between this and processing, and a user deciding whether to
 * press a button during dinner needs to know it without reading `docs/`.
 *
 * `fetched` vs `fresh` is the number that actually tells you something: a healthy
 * steady state is almost all `fresh`, and a pass that is mostly `fetched` means the
 * cache is expiring faster than the schedule refreshes it.
 */

const PHASE_LABELS: Record<string, string> = {
  artists: "Artists",
  discographies: "Discographies",
  editions: "Editions",
  releases: "Releases",
  paused: "Paused — waiting for the running job to finish",
};

const ENTITY_LABELS: Record<string, string> = {
  artist: "Artists",
  discography: "Discographies",
  editions: "Edition lists",
  release: "Releases",
};

const ENTITY_ORDER = ["artist", "discography", "editions", "release"];

function PhasePill({ status }: { status: MirrorStatus }) {
  if (!status.running) {
    if (status.errors > 0) return <Pill kind="err">Finished with errors</Pill>;
    if (status.finished_at) return <Pill kind="ok">Up to date</Pill>;
    return <Pill kind="off">Never run</Pill>;
  }
  if (status.phase === "paused") return <Pill kind="off">Paused</Pill>;
  return <Pill kind="scan">Refreshing</Pill>;
}

export default function Mirror() {
  const toast = useToast();
  const status = useFetch<MirrorStatus>(() => api.get("/mirror/status"));
  const running = status.data?.running ?? false;
  // Ignoring the cache is a modifier on the verb, not a second verb — the same
  // choice the Migrations page makes when it asks for a full re-read.
  const [force, setForce] = useState(false);
  const [confirming, setConfirming] = useState(false);

  // Poll while a pass runs. Every three seconds is plenty for a job whose unit of
  // work is a rate-limited request.
  useEffect(() => {
    if (!running) return;
    const t = setInterval(() => status.reload(), 3000);
    return () => clearInterval(t);
  }, [running, status.reload]);

  const act = (path: string, msg: string) => async () => {
    try {
      await api.post(path);
      toast("info", msg);
      setTimeout(() => status.reload(), 400);
    } catch (e) {
      toast("err", errMsg(e));
    }
  };

  /**
   * Starts a pass, and puts the forced one behind the shared confirm dialog.
   *
   * The reset afterwards is the other half of making forcing deliberate: the
   * checkbox is a modifier on a button that is pressed again later, so leaving it
   * ticked turns one considered decision into a setting, and the *next* press —
   * possibly days later, possibly by someone who did not tick it — silently costs
   * hours. It resets whether or not the request succeeded, because what must not
   * persist is the intent, not the outcome.
   */
  const startRefresh = async () => {
    setConfirming(false);
    try {
      await api.post(`/mirror/sync${force ? "?force=true" : ""}`);
      toast("info", force ? "Full metadata refresh started — cached copies ignored" : "Metadata refresh started");
      setTimeout(() => status.reload(), 400);
    } catch (e) {
      toast("err", errMsg(e));
    } finally {
      setForce(false);
    }
  };

  const s = status.data;
  const cached = s?.cached ?? {};
  const cachedTotal = ENTITY_ORDER.reduce((n, k) => n + (cached[k] ?? 0), 0);

  return (
    <div className="stack">
      <div className="page-head">
        <h1>Metadata refresh</h1>
        <div className="row">
          {/* "Reload", not "Refresh": on this page refresh is the *verb* — hours of
              rate-limited fetching — and two adjacent buttons a word apart, one free
              and one expensive, is a trap. */}
          <button
            className="btn btn-ghost btn-sm"
            onClick={() => status.reload()}
            title="Re-read the figures on this page. Does not contact MusicBrainz."
          >
            Reload
          </button>
          {running ? (
            <button
              className="btn btn-secondary btn-sm"
              onClick={act("/mirror/cancel", "Stopping after the current entity")}
              title="Stops at the next entity. Nothing is lost — no files were being written, and the next pass resumes by skipping whatever is already cached."
            >
              Stop
            </button>
          ) : (
            <button
              className="btn btn-primary btn-sm"
              // Only the forced pass confirms. Adding a dialog to the ordinary one
              // would train people to click through the dialog that matters.
              onClick={() => (force ? setConfirming(true) : startRefresh())}
              title="Re-reads MusicBrainz for everything the collection refers to. One rate-limited request per entity, so a first pass over a large collection takes hours. Reads only: no files are written."
            >
              Refresh metadata
            </button>
          )}
        </div>
      </div>

      {confirming && (
        <ForceRefreshDialog
          entities={cachedTotal}
          onCancel={() => setConfirming(false)}
          onConfirm={startRefresh}
        />
      )}

      <div className="row" style={{ gap: 8, alignItems: "center" }}>
        <label className="row" style={{ gap: 6, alignItems: "center", fontSize: 12 }}>
          <input
            type="checkbox"
            checked={force}
            disabled={running}
            onChange={(e) => setForce(e.target.checked)}
          />
          Ignore cached copies
        </label>
        <span className="dim" style={{ fontSize: 11 }}>
          {force
            ? "Re-reads everything, however recently it was checked — much slower, and how merges and deletions are found."
            : "Only re-reads entries whose cached copy has expired."}
        </span>
      </div>

      <p className="dim" style={{ fontSize: 12, maxWidth: "68ch" }}>
        Autotaggerr keeps a local copy of every MusicBrainz entity your collection refers to, so
        browsing reads the database instead of an API limited to about one request per second. The
        copy is refreshed on a schedule, which moves that cost off the pages you are looking at.
        This <strong>never writes to your files</strong> — when a release has changed upstream it is
        reported here and in Activity, and the next processing run re-tags the files that use it. To apply a
        change immediately, use <em>Tag files</em> on the artist.
      </p>

      {status.err && <ErrorNote message={status.err} />}

      {s && (
        <div className="card stack">
          <div className="row" style={{ justifyContent: "space-between", alignItems: "center" }}>
            <div className="row" style={{ gap: 8, alignItems: "center" }}>
              <PhasePill status={s} />
              {s.running && s.phase && (
                <span className="dim" style={{ fontSize: 12 }}>
                  {PHASE_LABELS[s.phase] ?? s.phase}
                </span>
              )}
            </div>
            <span className="dim mono" style={{ fontSize: 11 }}>
              {s.running
                ? s.started_at && `started ${new Date(s.started_at).toLocaleString()}`
                : s.finished_at && `last run ${new Date(s.finished_at).toLocaleString()}`}
            </span>
          </div>

          {s.total > 0 && (
            <div className="row" style={{ gap: 10, alignItems: "center" }}>
              <CoverageBar
                total={s.total}
                owned={s.done}
                label="entities refreshed this pass"
                width={240}
              />
              <span className="mono dim" style={{ fontSize: 11 }}>
                {s.done} / {s.total}
              </span>
            </div>
          )}

          <div className="row" style={{ gap: 18, flexWrap: "wrap" }}>
            <Stat n={s.fetched} l="fetched" hint="Cost a MusicBrainz request" />
            <Stat n={s.fresh} l="already cached" hint="Answered from the local copy, without a MusicBrainz request" />
            <Stat n={s.errors} l="failed" hint="Logged and skipped; the pass continues" />
            <Stat
              n={s.changed_releases}
              l="changed upstream"
              hint="Releases whose metadata differs from the cached copy. The next processing run re-tags the files that use them."
            />
          </div>

          {s.last_error && (
            <div className="dim" style={{ fontSize: 11 }} title={s.last_error}>
              Last error: {s.last_error}
            </div>
          )}
        </div>
      )}

      <div className="stack">
        <h2 style={{ fontSize: 14 }}>Cached locally</h2>
        <div className="tablewrap">
          <table className="data">
            <thead>
              <tr>
                <th>Entity</th>
                <th style={{ textAlign: "right" }}>Entries</th>
              </tr>
            </thead>
            <tbody>
              {ENTITY_ORDER.map((k) => (
                <tr key={k}>
                  <td>{ENTITY_LABELS[k] ?? k}</td>
                  <td className="mono" style={{ textAlign: "right" }}>{cached[k] ?? 0}</td>
                </tr>
              ))}
              <tr>
                <td className="dim">Total</td>
                <td className="mono dim" style={{ textAlign: "right" }}>{cachedTotal}</td>
              </tr>
            </tbody>
          </table>
        </div>
      </div>
    </div>
  );
}

function Stat({ n, l, hint }: { n: number; l: string; hint: string }) {
  return (
    <div className="stack" style={{ gap: 2 }} title={hint}>
      <span className="mono" style={{ fontSize: 18 }}>{n}</span>
      <span className="dim" style={{ fontSize: 11 }}>{l}</span>
    </div>
  );
}
