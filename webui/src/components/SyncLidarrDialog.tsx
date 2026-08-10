import { useState } from "react";
import { Modal } from "./ui";

/**
 * The options dialog both Sync-from-Lidarr buttons open — collection-wide and
 * per-artist. One component so the option is explained once and the two scopes cannot
 * drift into describing the same work differently.
 *
 * It is a dialog rather than a checkbox beside the button because "ignore cache" is
 * not guessable: dropping the cache changes nothing about the numbers this pass
 * fetches (the mirror's own two calls are the only uncached Lidarr calls there are) —
 * it changes what the *next scan* matches files against. That needs a sentence, and a
 * sentence needs somewhere to live.
 *
 * The box starts unticked. Mirroring is the routine action; dropping the cache is
 * repair, and repair should be chosen rather than arrived at.
 */
export function SyncLidarrDialog({
  scope,
  busy,
  onConfirm,
  onCancel,
}: {
  /** What the pass covers, named for the dialog body. */
  scope: string;
  busy?: boolean;
  onConfirm: (ignoreCache: boolean) => void;
  onCancel: () => void;
}) {
  const [ignoreCache, setIgnoreCache] = useState(false);

  return (
    <Modal title="Sync from Lidarr" onClose={onCancel}>
      <div className="stack" style={{ fontSize: 12, color: "var(--text-dim)", gap: 10 }}>
        <p style={{ margin: 0 }}>
          Read what Lidarr says should exist for {scope} and mirror it — which albums it
          tracks, which it monitors, and how many files it has of each. Reads Lidarr, not
          MusicBrainz. No files are written.
        </p>

        <label className="row" style={{ gap: 8, cursor: "pointer", alignItems: "flex-start" }}>
          <input
            type="checkbox"
            checked={ignoreCache}
            onChange={(e) => setIgnoreCache(e.target.checked)}
            style={{ marginTop: 2 }}
          />
          <span>
            <span style={{ color: "var(--text)" }}>Drop cached Lidarr data</span>
            <br />
            Autotaggerr caches Lidarr's artist, album, track and track-file responses for
            an hour. This pass does not use them — the <em>next scan</em> does, to match
            files to Lidarr's tracks. Tick this if you have just imported or moved files
            in Lidarr and do not want to wait for the cache to expire.
          </span>
        </label>
      </div>

      <div className="modal-actions">
        <button className="btn btn-secondary btn-sm" onClick={onCancel} disabled={busy}>
          Cancel
        </button>
        <button
          className="btn btn-primary btn-sm"
          onClick={() => onConfirm(ignoreCache)}
          disabled={busy}
        >
          {busy ? "Starting…" : "Sync"}
        </button>
      </div>
    </Modal>
  );
}
