import { api } from "../api";
import { useFetch } from "../hooks";
import { ItemTags, ItemTempo, LibraryItem } from "../types";
import { Modal, StatusPill } from "./ui";

// ItemDiffModal is the signature view: one monospace row per tag, showing the
// current on-disk value vs what Autotaggerr would write (git-style old → new).
export function ItemDiffModal({ item, onClose }: { item: LibraryItem; onClose: () => void }) {
  const { data, err, loading } = useFetch<ItemTags>(() => api.get(`/library-items/${item.id}/tags`), [item.id]);
  const changed = data?.tags.filter((t) => t.changed).length ?? 0;

  return (
    <Modal title="File tags" onClose={onClose} wide>
      <div style={{ marginBottom: 14 }}>
        <div className="mono" style={{ color: "var(--text)", fontSize: 12, wordBreak: "break-all" }}>{item.path}</div>
        <div className="row" style={{ marginTop: 8, gap: 10 }}>
          <StatusPill status={item.status} />
          <span className="dim" style={{ fontSize: 11 }}>
            {changed > 0 ? `${changed} tag${changed > 1 ? "s" : ""} would change` : "all tags up to date"}
          </span>
        </div>
        <TempoLine itemId={item.id} />
      </div>

      {loading && <div className="muted">Loading…</div>}
      {err && <div className="help-err">{err}</div>}

      {data && (
        <div className="scroll diff">
          {data.tags.map((t) => (
            <div className="diffrow" key={t.key}>
              <span className="diffkey">{t.key}</span>
              <div className="diffvals">
                {t.changed ? (
                  <>
                    {t.current ? <span className="diffv rem">{t.current}</span> : <span className="diffv none">(empty)</span>}
                    {t.desired ? <span className="diffv add">{t.desired}</span> : <span className="diffv none">(removed)</span>}
                  </>
                ) : (
                  <span className="diffv same">{t.current || t.desired || "—"}</span>
                )}
              </div>
            </div>
          ))}
        </div>
      )}
    </Modal>
  );
}

/**
 * The file's measured tempo, and what becomes of it, in one line.
 *
 * Above the diff rather than in it: the diff is tags, and a stored tempo is not a tag
 * until a profile writes one — when it does, the BPM row appears below under the same
 * rules a write applies. This line answers the question the row cannot: *why* there is
 * no BPM row, which has three different answers (not analysed, nothing measured, not
 * written) with three different fixes.
 */
function TempoLine({ itemId }: { itemId: string }) {
  const { data } = useFetch<ItemTempo>(() => api.get(`/library-items/${itemId}/tempo`), [itemId]);
  if (!data) return null;
  const t = data.tempo;

  let body;
  if (!t) {
    body = data.library_analyses
      ? "not analysed yet — the next tempo analysis pass will measure it"
      : "not analysed — this library does not opt in to tempo analysis";
  } else if (t.bpm <= 0) {
    body = <>none measured{t.error ? ` — ${t.error}` : ""}</>;
  } else {
    const written = Math.round(t.bpm);
    let fate;
    if (!data.bpm_write) fate = "not written — the tagger profile does not write BPM";
    else if (t.confidence < data.min_confidence)
      fate = <>not written — below this profile's <span className="mono">{data.min_confidence.toFixed(2)}</span> floor</>;
    else
      fate = (
        <>
          written as <span className="mono">{written}</span>
          {data.bpm_write === "fill_empty" ? " where the file has none" : ""}
        </>
      );
    body = (
      <>
        <span className="mono" style={{ color: "var(--text-muted)" }}>{t.bpm.toFixed(2)}</span> BPM
        {" · confidence "}
        <span className="mono">{t.confidence.toFixed(2)}</span>
        {" · "}
        {fate}
      </>
    );
  }

  return (
    <div className="dim" style={{ fontSize: 11, marginTop: 6 }} title={t ? `${t.detector} ${t.detector_version}, ${t.beats} beats` : undefined}>
      Tempo: {body}
    </div>
  );
}
