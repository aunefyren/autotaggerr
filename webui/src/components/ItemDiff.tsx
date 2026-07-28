import { api } from "../api";
import { useFetch } from "../hooks";
import { ItemTags, LibraryItem } from "../types";
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
                    {t.current ? <span className="diffv rem">{t.current}</span> : <span className="diffv empty">(empty)</span>}
                    {t.desired ? <span className="diffv add">{t.desired}</span> : <span className="diffv empty">(removed)</span>}
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
