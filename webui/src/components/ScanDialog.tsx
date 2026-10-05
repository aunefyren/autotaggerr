import { useState } from "react";
import { Modal } from "./ui";

/**
 * The options dialog both Scan buttons open — collection-wide and per-artist. Same
 * shape as the Lidarr sync dialog, for the same reason: the option is not guessable from
 * a label, and it changes what the press costs.
 *
 * Unticked, Scan is what it always was: an instant pass over the files already indexed,
 * answered inline. Ticked, it walks the folders first, so a file a manager renamed or
 * moved is found at its new path instead of being dropped as gone — and the press
 * becomes a queued job reported in Activity, because a walk is minutes rather than
 * milliseconds. Either way no audio file is written.
 *
 * The box starts unticked: the instant pass is the routine action, and the walk is for
 * when something is missing that should not be.
 */
export function ScanDialog({
  scope,
  busy,
  onConfirm,
  onCancel,
}: {
  /** What the pass covers, named for the dialog body. */
  scope: string;
  busy?: boolean;
  onConfirm: (walkDisk: boolean) => void;
  onCancel: () => void;
}) {
  const [walkDisk, setWalkDisk] = useState(false);

  return (
    <Modal title="Scan" onClose={onCancel}>
      <div className="stack" style={{ fontSize: 12, color: "var(--text-dim)", gap: 10 }}>
        <p style={{ margin: 0 }}>
          Re-derive what you own for {scope} from the files already indexed, dropping any that
          are no longer at their indexed path. No MusicBrainz, no file writes — this is for
          when the view looks stale.
        </p>

        <label className="row" style={{ gap: 8, cursor: "pointer", alignItems: "flex-start" }}>
          <input
            type="checkbox"
            checked={walkDisk}
            onChange={(e) => setWalkDisk(e.target.checked)}
            style={{ marginTop: 2 }}
          />
          <span>
            <span style={{ color: "var(--text)" }}>Look for new and moved files on disk</span>
            <br />
            Walk the folders first. A file that was renamed or moved — by Lidarr after a re-import,
            say — keeps its identity at the new path instead of being dropped, and files with no
            identity are looked up with the manager. Still writes no tags: newly found files are
            tagged by the next Process. Queued, and reported in Activity, because walking takes
            minutes on a large library.
          </span>
        </label>
      </div>

      <div className="modal-actions">
        <button className="btn btn-secondary btn-sm" onClick={onCancel} disabled={busy}>
          Cancel
        </button>
        <button className="btn btn-primary btn-sm" onClick={() => onConfirm(walkDisk)} disabled={busy}>
          {busy ? "Starting…" : "Scan"}
        </button>
      </div>
    </Modal>
  );
}
