import { useEffect, useState } from "react";
import { api } from "../api";
import { useFetch } from "../hooks";
import { ItemsPage, Library, LibraryItem } from "../types";
import { EmptyState, ErrorNote, IdChip, StatusPill } from "../components/ui";
import { ItemDiffModal } from "../components/ItemDiff";
import { AttachModal } from "../components/AttachModal";
import { BulkAttachModal } from "../components/BulkAttachModal";
import { MBLink } from "../components/MBLink";

const PAGE = 50;

/** The directory holding a file — the unit an album is actually attached in. */
function folderOf(path: string): string {
  const parts = path.split(/[\\/]/);
  parts.pop();
  return parts.join("/");
}

export default function Items() {
  const libs = useFetch<Library[]>(() => api.get("/libraries"));
  const [libraryId, setLibraryId] = useState("");
  const [status, setStatus] = useState("");
  const [q, setQ] = useState("");
  const [offset, setOffset] = useState(0);
  const [selected, setSelected] = useState<LibraryItem | null>(null);
  const [attaching, setAttaching] = useState<LibraryItem | null>(null);
  const [picked, setPicked] = useState<string[]>([]);
  const [bulk, setBulk] = useState(false);

  const query = new URLSearchParams();
  if (libraryId) query.set("library_id", libraryId);
  if (status) query.set("status", status);
  if (q) query.set("q", q);
  query.set("limit", String(PAGE));
  query.set("offset", String(offset));

  const page = useFetch<ItemsPage>(() => api.get(`/library-items?${query.toString()}`), [libraryId, status, q, offset]);

  const resetAnd = (fn: () => void) => {
    setOffset(0);
    fn();
  };

  const total = page.data?.total ?? 0;
  const items = page.data?.items ?? [];

  // Selection is dropped whenever the view changes: a bulk attach writes tags to
  // every picked file, so it must never include files the user can no longer see.
  useEffect(() => setPicked([]), [libraryId, status, q, offset]);

  const toggle = (id: string) =>
    setPicked((current) => (current.includes(id) ? current.filter((x) => x !== id) : [...current, id]));

  // Bulk attach writes an identity, so it can only ever include files whose identity
  // is the user's to set. Select-all therefore means "every file here that can be
  // attached", not "every row" — otherwise the count promises work the API rejects.
  const attachable = items.filter((it) => it.identity_editable);
  const allPicked = attachable.length > 0 && picked.length === attachable.length;
  const pickedItems = items.filter((it) => picked.includes(it.id));

  return (
    <div className="stack">
      <div className="page-head">
        <h1>Items</h1>
        <div className="dim mono" style={{ fontSize: 12 }}>{total} indexed</div>
      </div>

      <div className="row" style={{ flexWrap: "wrap", gap: 8 }}>
        <select className="select" style={{ width: 200 }} value={libraryId} onChange={(e) => resetAnd(() => setLibraryId(e.target.value))}>
          <option value="">All libraries</option>
          {libs.data?.map((l) => <option key={l.id} value={l.id}>{l.name}</option>)}
        </select>
        <select className="select" style={{ width: 150 }} value={status} onChange={(e) => resetAnd(() => setStatus(e.target.value))}>
          <option value="">Any status</option>
          <option value="ok">Tagged</option>
          <option value="error">Error</option>
          <option value="unmatched">Unmatched</option>
        </select>
        <input className="input mono" style={{ width: 240 }} placeholder="Filter by path…" value={q} onChange={(e) => resetAnd(() => setQ(e.target.value))} />
      </div>

      {picked.length > 0 && (
        <div className="row" style={{ justifyContent: "space-between" }}>
          <span className="dim" style={{ fontSize: 12 }}>{picked.length} selected</span>
          <div className="row">
            <button className="btn btn-ghost btn-sm" onClick={() => setPicked([])}>Clear</button>
            <button className="btn btn-primary btn-sm" onClick={() => setBulk(true)}>
              Attach {picked.length} to one release…
            </button>
          </div>
        </div>
      )}

      {page.err && <ErrorNote message={page.err} />}
      {!page.err && !page.loading && items.length === 0 && (
        <EmptyState icon="≣" message="No items match. Run a scan to populate the index." />
      )}

      {items.length > 0 && (
        <>
          <div className="tablewrap">
            <table className="data">
              <thead>
                <tr>
                  <th style={{ width: 28 }}>
                    <input
                      type="checkbox"
                      checked={allPicked}
                      disabled={attachable.length === 0}
                      title={
                        attachable.length === 0
                          ? "Nothing here can be attached by hand — Lidarr owns these files' identity."
                          : "Select every file on this page that can be attached by hand"
                      }
                      onChange={() => setPicked(allPicked ? [] : attachable.map((it) => it.id))}
                    />
                  </th>
                  <th>Path</th>
                  <th>MB release id</th>
                  <th>Source</th>
                  <th>Status</th>
                  <th style={{ textAlign: "right" }}>Actions</th>
                </tr>
              </thead>
              <tbody>
                {items.map((it) => (
                  <tr key={it.id} style={{ cursor: "pointer" }} onClick={() => setSelected(it)}>
                    <td onClick={(e) => e.stopPropagation()}>
                      <input
                        type="checkbox"
                        checked={picked.includes(it.id)}
                        disabled={!it.identity_editable}
                        title={it.identity_editable ? undefined : "Lidarr owns this file's identity"}
                        onChange={() => toggle(it.id)}
                      />
                    </td>
                    <td><span className="path">{it.path}</span></td>
                    <td onClick={(e) => e.stopPropagation()}>
                      <div className="row" style={{ gap: 6 }}>
                        <IdChip value={it.mb_release_id} />
                        <MBLink entity="release" mbid={it.mb_release_id} />
                      </div>
                    </td>
                    <td className="mono dim" style={{ fontSize: 11 }}>
                      {it.correlation_source || "—"}
                      {it.pinned && <span title="Manually attached; automatic resolution will not override it"> 📌</span>}
                    </td>
                    <td><StatusPill status={it.status} /></td>
                    <td onClick={(e) => e.stopPropagation()}>
                      <div className="row" style={{ justifyContent: "flex-end" }}>
                        {/* Albums are attached per folder, so filtering to one is the
                            first step of the bulk workflow. */}
                        <button
                          className="btn btn-ghost btn-sm"
                          title="Filter to this folder, then select all"
                          onClick={() => resetAnd(() => setQ(folderOf(it.path)))}
                        >
                          Folder
                        </button>
                        {/* Disabled, not hidden, for a Lidarr-managed file: the API
                            rejects the attach (409) because Lidarr owns which release
                            and track a file is, and a control that vanishes per row
                            is harder to account for than a dimmed one. */}
                        <button
                          className="btn btn-secondary btn-sm"
                          disabled={!it.identity_editable}
                          title={
                            it.identity_editable
                              ? undefined
                              : "Lidarr owns this file's identity — set the release in Lidarr, then re-correlate."
                          }
                          onClick={() => setAttaching(it)}
                        >
                          {it.mb_release_id ? "Re-attach" : "Attach"}
                        </button>
                      </div>
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
          <div className="row" style={{ justifyContent: "flex-end" }}>
            <span className="dim mono" style={{ fontSize: 11 }}>
              {offset + 1}–{Math.min(offset + PAGE, total)} of {total}
            </span>
            <button className="btn btn-secondary btn-sm" disabled={offset === 0} onClick={() => setOffset(Math.max(0, offset - PAGE))}>Prev</button>
            <button className="btn btn-secondary btn-sm" disabled={offset + PAGE >= total} onClick={() => setOffset(offset + PAGE)}>Next</button>
          </div>
        </>
      )}

      {selected && <ItemDiffModal item={selected} onClose={() => setSelected(null)} />}

      {attaching && (
        <AttachModal
          item={attaching}
          onClose={() => setAttaching(null)}
          onAttached={() => { setAttaching(null); page.reload(); }}
        />
      )}

      {bulk && pickedItems.length > 0 && (
        <BulkAttachModal
          items={pickedItems}
          onClose={() => setBulk(false)}
          onAttached={() => { setBulk(false); setPicked([]); page.reload(); }}
        />
      )}
    </div>
  );
}
