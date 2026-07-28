import { useState } from "react";
import { api } from "../api";
import { useFetch } from "../hooks";
import { ItemsPage, Library, LibraryItem } from "../types";
import { EmptyState, ErrorNote, IdChip, StatusPill } from "../components/ui";
import { ItemDiffModal } from "../components/ItemDiff";

const PAGE = 50;

export default function Items() {
  const libs = useFetch<Library[]>(() => api.get("/libraries"));
  const [libraryId, setLibraryId] = useState("");
  const [status, setStatus] = useState("");
  const [q, setQ] = useState("");
  const [offset, setOffset] = useState(0);
  const [selected, setSelected] = useState<LibraryItem | null>(null);

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

      {page.err && <ErrorNote message={page.err} />}
      {!page.err && !page.loading && items.length === 0 && (
        <EmptyState icon="≣" message="No items match. Run a scan to populate the index." />
      )}

      {items.length > 0 && (
        <>
          <div className="tablewrap">
            <table className="data">
              <thead>
                <tr><th>Path</th><th>MB release id</th><th>Source</th><th>Status</th></tr>
              </thead>
              <tbody>
                {items.map((it) => (
                  <tr key={it.id} style={{ cursor: "pointer" }} onClick={() => setSelected(it)}>
                    <td><span className="path">{it.path}</span></td>
                    <td onClick={(e) => e.stopPropagation()}><IdChip value={it.mb_release_id} /></td>
                    <td className="mono dim" style={{ fontSize: 11 }}>{it.correlation_source || "—"}</td>
                    <td><StatusPill status={it.status} /></td>
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
    </div>
  );
}
