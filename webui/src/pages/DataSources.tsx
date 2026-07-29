import { FormEvent, useState } from "react";
import { api, errMsg } from "../api";
import { useFetch } from "../hooks";
import { DataSource } from "../types";
import { EmptyState, ErrorNote, Modal, Pill } from "../components/ui";
import { useToast } from "../toast";

export default function DataSources() {
  const { data, err, loading, reload } = useFetch<DataSource[]>(() => api.get("/data-sources"));
  const [editing, setEditing] = useState<DataSource | null>(null);
  const [adding, setAdding] = useState(false);

  return (
    <div className="stack">
      <div className="page-head">
        <h1>Data sources</h1>
        <button className="btn btn-primary btn-sm" onClick={() => setAdding(true)}>Add data source</button>
      </div>
      <p className="muted" style={{ margin: 0, maxWidth: "68ch" }}>
        Where metadata comes from. MusicBrainz is used by every manager to fetch the full release for
        tagging. AcoustID is optional: it identifies a file from its audio, as a suggestion when you
        attach one by hand.
      </p>

      {err && <ErrorNote message={err} />}
      {!err && !loading && data && data.length === 0 && <EmptyState icon="⛃" message="No data sources configured." />}

      {data && data.length > 0 && (
        <div className="tablewrap">
          <table className="data">
            <thead>
              <tr>
                <th>Name</th><th>Type</th><th>Base URL</th><th>Rate limit</th><th>State</th>
                <th style={{ textAlign: "right" }}>Actions</th>
              </tr>
            </thead>
            <tbody>
              {data.map((d) => (
                <tr key={d.id}>
                  <td style={{ color: "var(--text)" }}>{d.name}</td>
                  <td className="mono" style={{ fontSize: 12 }}>{d.type}</td>
                  <td><span className="path">{d.base_url || "—"}</span></td>
                  <td className="num">{d.rate_limit}/s</td>
                  <td>{d.enabled ? <Pill kind="ok">Enabled</Pill> : <Pill kind="off">Disabled</Pill>}</td>
                  <td>
                    <div className="row" style={{ justifyContent: "flex-end" }}>
                      <button className="btn btn-secondary btn-sm" onClick={() => setEditing(d)}>Edit</button>
                    </div>
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      )}

      {(adding || editing) && (
        <DataSourceForm
          initial={editing ?? undefined}
          onClose={() => { setAdding(false); setEditing(null); }}
          onSaved={() => { setAdding(false); setEditing(null); reload(); }}
        />
      )}
    </div>
  );
}

/** Which providers authenticate. The Cover Art Archive and MusicBrainz do not. */
function needsKey(type: string): boolean {
  return type === "acoustid" || type === "fanart";
}

function DataSourceForm({
  initial,
  onClose,
  onSaved,
}: {
  initial?: DataSource;
  onClose: () => void;
  onSaved: () => void;
}) {
  const toast = useToast();
  const editing = !!initial;
  const [name, setName] = useState(initial?.name ?? "AcoustID");
  const [type, setType] = useState(initial?.type ?? "acoustid");
  const [baseUrl, setBaseUrl] = useState(initial?.base_url ?? "");
  const [apiKey, setApiKey] = useState("");
  const [enabled, setEnabled] = useState(initial?.enabled ?? true);
  const [busy, setBusy] = useState(false);

  const submit = async (e: FormEvent) => {
    e.preventDefault();
    setBusy(true);
    try {
      const body: Record<string, unknown> = { name, type, base_url: baseUrl, enabled };
      // Only send the key when one was typed: the field is write-only, so an empty
      // box means "leave it as it is", never "clear it".
      if (apiKey.trim()) body.api_key = apiKey.trim();

      if (editing) await api.put(`/data-sources/${initial!.id}`, body);
      else await api.post("/data-sources", body);
      onSaved();
    } catch (e) {
      toast("err", errMsg(e));
      setBusy(false);
    }
  };

  return (
    <Modal title={editing ? `Edit ${initial!.name}` : "Add data source"} onClose={onClose}>
      <form onSubmit={submit} className="stack">
        <div className="field">
          <label className="flabel">Name</label>
          <input className="input" value={name} onChange={(e) => setName(e.target.value)} autoFocus />
        </div>

        <div className="field">
          <label className="flabel">Type</label>
          <select className="select" value={type} onChange={(e) => setType(e.target.value)} disabled={editing}>
            <option value="musicbrainz">MusicBrainz</option>
            <option value="acoustid">AcoustID</option>
            <option value="coverartarchive">Cover Art Archive</option>
            <option value="fanart">fanart.tv</option>
          </select>
          {type === "acoustid" && (
            <span className="dim" style={{ fontSize: 11 }}>
              Identifies files from their audio. Also needs fpcalc on the server and the per-library
              opt-in; it only ever suggests a match for you to confirm.
            </span>
          )}
          {type === "coverartarchive" && (
            <span className="dim" style={{ fontSize: 11 }}>
              Album covers, matched by MusicBrainz ID. No key needed. One is added for you already —
              a second does nothing.
            </span>
          )}
          {type === "fanart" && (
            <span className="dim" style={{ fontSize: 11 }}>
              Artist portraits and backdrops for the collection pages. MusicBrainz has no artist
              images, so this is the only source for them.
            </span>
          )}
        </div>

        {/* Only the keyed providers show a key field. Offering one for the Cover Art
            Archive would imply a credential it does not have. */}
        {needsKey(type) && (
          <div className="field">
            <label className="flabel">API key</label>
            <input
              className="input mono"
              type="password"
              value={apiKey}
              onChange={(e) => setApiKey(e.target.value)}
              placeholder={editing ? "Unchanged" : type === "fanart" ? "Your fanart.tv personal key" : "Your AcoustID client key"}
            />
            <span className="dim" style={{ fontSize: 11 }}>
              {type === "fanart"
                ? "Stored, never shown again. Get one free at fanart.tv — sign in, then Add API key."
                : "Stored, never shown again. Get one free at acoustid.org/new-application."}
            </span>
          </div>
        )}

        <div className="field">
          <label className="flabel">Base URL</label>
          <input
            className="input mono"
            value={baseUrl}
            onChange={(e) => setBaseUrl(e.target.value)}
            placeholder="Leave empty for the default"
          />
        </div>

        <label className="row" style={{ gap: 8, cursor: "pointer" }}>
          <input type="checkbox" checked={enabled} onChange={(e) => setEnabled(e.target.checked)} />
          <span style={{ fontSize: 12 }}>Enabled</span>
        </label>

        <div className="modal-actions">
          <button type="button" className="btn btn-ghost btn-sm" onClick={onClose}>Cancel</button>
          <button className="btn btn-primary btn-sm" disabled={busy || !name}>
            {busy ? "Saving…" : editing ? "Save changes" : "Add data source"}
          </button>
        </div>
      </form>
    </Modal>
  );
}
