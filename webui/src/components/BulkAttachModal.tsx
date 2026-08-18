import { useState } from "react";
import { api, errMsg } from "../api";
import { BulkMapping, BulkPreview, LibraryItem } from "../types";
import { Modal } from "./ui";
import { MBLink } from "./MBLink";
import { ReleaseSearch, guessFields } from "./ReleaseSearch";
import { useToast } from "../toast";

/** The file's own name — the part that identifies which track it is. */
function fileName(path: string): string {
  return path.split(/[\\/]/).pop() ?? path;
}

/** "2-05 · Dreams", or just the number on a single-disc release. */
function trackLabel(
  track: { medium: number; number: string; title: string; video?: boolean },
  multiDisc: boolean,
): string {
  const position = multiDisc ? `${track.medium}-${track.number}` : track.number;
  // Video tracks are listed but never proposed, so the label has to say why one is
  // sitting there unmapped — otherwise a bonus DVD reads as tracks the mapping
  // simply missed.
  return `${position} · ${track.title}${track.video ? " (video)" : ""}`;
}

/**
 * Attaches a whole folder to one release in a single reviewed action.
 *
 * Files arrive as albums, so identifying them one at a time is what made manual
 * attach unusable at scale. The mapping is proposed by the server (filename track
 * numbers, falling back to sort order) but **never applied without review**:
 * silently accepting a guess is the one way to mistag an entire album in a single
 * click, so the review table is a required step rather than a confirmation dialog.
 */
export function BulkAttachModal({
  items,
  onClose,
  onAttached,
}: {
  items: LibraryItem[];
  onClose: () => void;
  onAttached: () => void;
}) {
  const toast = useToast();
  const [preview, setPreview] = useState<BulkPreview | null>(null);
  const [mappings, setMappings] = useState<BulkMapping[]>([]);
  const [loading, setLoading] = useState(false);
  const [busy, setBusy] = useState(false);

  const pickRelease = async (mbid: string) => {
    setLoading(true);
    try {
      const res = await api.post<BulkPreview>("/attach/preview", {
        mb_release_id: mbid,
        item_ids: items.map((i) => i.id),
      });
      setPreview(res);
      setMappings(res.mappings);
    } catch (e) {
      toast("err", errMsg(e));
    } finally {
      setLoading(false);
    }
  };

  const setTrack = (itemId: string, trackId: string) =>
    setMappings((current) =>
      current.map((m) => (m.item_id === itemId ? { ...m, mb_release_track_id: trackId } : m))
    );

  const attach = async () => {
    if (!preview) return;
    setBusy(true);
    try {
      const res = await api.post<{ attached: number; tags_written: number; warning?: string }>(
        "/attach/bulk",
        { mb_release_id: preview.release.mb_id, mappings }
      );
      if (res.warning) toast("info", res.warning);
      else toast("ok", `Attached ${res.attached} file${res.attached === 1 ? "" : "s"} — ${res.tags_written} tags written`);
      onAttached();
    } catch (e) {
      toast("err", errMsg(e));
      setBusy(false);
    }
  };

  const multiDisc = (preview?.tracks ?? []).some((t) => t.medium !== preview?.tracks[0]?.medium);
  const chosen = mappings.filter((m) => m.mb_release_track_id);
  const duplicates = new Set(
    chosen
      .map((m) => m.mb_release_track_id)
      .filter((id, i, all) => all.indexOf(id) !== i)
  );
  const guessedByOrder = mappings.some((m) => m.mb_release_track_id && m.how === "order");

  return (
    <Modal title={`Attach ${items.length} files to one release`} onClose={onClose} wide>
      <div className="stack">
        {!preview && (
          <>
            <div className="dim" style={{ fontSize: 11 }}>
              Find the release these files are from. You will map each file to a track before
              anything is written.
            </div>
            <ReleaseSearch initialFields={guessFields(items[0].path)} onPick={pickRelease} picking={loading} />
            <div className="scroll" style={{ maxHeight: "20vh" }}>
              {items.map((it) => (
                <div key={it.id} className="path" style={{ display: "block", fontSize: 11, padding: "2px 0" }}>
                  {fileName(it.path)}
                </div>
              ))}
            </div>
          </>
        )}

        {loading && <div className="muted">Building the mapping…</div>}

        {preview && (
          <>
            <div className="row" style={{ justifyContent: "space-between" }}>
              <div>
                <div style={{ color: "var(--text)" }}>{preview.release.title}</div>
                <div className="dim" style={{ fontSize: 11 }}>
                  {[preview.release.date?.slice(0, 4), preview.release.country, preview.release.disambiguation]
                    .filter(Boolean)
                    .join(" · ")}
                  {` · ${preview.tracks.length} tracks`}
                </div>
              </div>
              <div className="row" style={{ gap: 8 }}>
                <MBLink entity="release" mbid={preview.release.mb_id} />
                <button className="btn btn-ghost btn-sm" onClick={() => setPreview(null)} disabled={busy}>
                  Pick another release
                </button>
              </div>
            </div>

            <div className="dim" style={{ fontSize: 11 }}>
              Check every row before attaching — this writes tags to {chosen.length} file
              {chosen.length === 1 ? "" : "s"}. Set a row to <em>Skip</em> if you are not sure.
              {guessedByOrder && " Rows marked “by order” were guessed from sort order, not from a track number in the filename."}
            </div>

            <div className="tablewrap scroll">
              <table className="data">
                <thead>
                  <tr>
                    <th>File</th>
                    <th style={{ width: 44 }}>How</th>
                    <th style={{ width: 260 }}>Track</th>
                  </tr>
                </thead>
                <tbody>
                  {mappings.map((m) => {
                    const duplicate = m.mb_release_track_id !== "" && duplicates.has(m.mb_release_track_id);
                    return (
                      <tr key={m.item_id}>
                        <td><span className="path">{fileName(m.path)}</span></td>
                        <td className="dim mono" style={{ fontSize: 11 }}>
                          {m.mb_release_track_id ? (m.how === "order" ? "order" : "№") : "—"}
                        </td>
                        <td>
                          <select
                            className="select"
                            style={{ width: "100%", borderColor: duplicate ? "var(--danger)" : undefined }}
                            value={m.mb_release_track_id}
                            disabled={busy}
                            onChange={(e) => setTrack(m.item_id, e.target.value)}
                          >
                            <option value="">Skip this file</option>
                            {preview.tracks.map((t) => (
                              <option key={t.track_id} value={t.track_id}>
                                {trackLabel(t, multiDisc)}
                              </option>
                            ))}
                          </select>
                        </td>
                      </tr>
                    );
                  })}
                </tbody>
              </table>
            </div>

            {duplicates.size > 0 && (
              <div className="dim" style={{ fontSize: 11, color: "var(--danger-text)" }}>
                Two files are mapped to the same track. Fix the highlighted rows first.
              </div>
            )}

            <div className="modal-actions">
              <button className="btn btn-ghost btn-sm" onClick={onClose} disabled={busy}>Cancel</button>
              <button
                className="btn btn-primary btn-sm"
                disabled={busy || chosen.length === 0 || duplicates.size > 0}
                onClick={attach}
              >
                {busy ? "Attaching…" : `Attach ${chosen.length} file${chosen.length === 1 ? "" : "s"}`}
              </button>
            </div>
          </>
        )}
      </div>
    </Modal>
  );
}
