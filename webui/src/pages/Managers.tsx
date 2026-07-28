import { FormEvent, useState } from "react";
import { api, errMsg } from "../api";
import { useFetch } from "../hooks";
import { Manager } from "../types";
import { EmptyState, ErrorNote, Modal, Pill } from "../components/ui";
import { useToast } from "../toast";

export default function Managers() {
  const toast = useToast();
  const { data, err, loading, reload } = useFetch<Manager[]>(() => api.get("/managers"));
  const [creating, setCreating] = useState(false);
  const [editing, setEditing] = useState<Manager | null>(null);

  const remove = async (m: Manager) => {
    if (!confirm(`Remove manager "${m.name}"?`)) return;
    try {
      await api.del(`/managers/${m.id}`);
      toast("ok", `Removed ${m.name}`);
      reload();
    } catch (e) {
      toast("err", errMsg(e));
    }
  };

  return (
    <div className="stack">
      <div className="page-head">
        <h1>Managers</h1>
        <button className="btn btn-primary btn-sm" onClick={() => setCreating(true)}>Add manager</button>
      </div>
      <p className="muted" style={{ margin: 0, maxWidth: "68ch" }}>
        A manager decides which MusicBrainz release each file maps to. Lidarr reads that decision from
        Lidarr; Autotaggerr resolves it natively from the files.
      </p>

      {err && <ErrorNote message={err} />}
      {!err && !loading && data && data.length === 0 && (
        <EmptyState icon="◇" message="No managers configured yet." />
      )}

      {data && data.length > 0 && (
        <div className="tablewrap">
          <table className="data">
            <thead>
              <tr><th>Name</th><th>Type</th><th>Base URL</th><th>State</th><th style={{ textAlign: "right" }}>Actions</th></tr>
            </thead>
            <tbody>
              {data.map((m) => (
                <tr key={m.id}>
                  <td style={{ color: "var(--text)" }}>{m.name}</td>
                  <td className="mono" style={{ fontSize: 12 }}>{m.type}</td>
                  <td><span className="path">{m.lidarr_base_url || "—"}</span></td>
                  <td>{m.enabled ? <Pill kind="ok">Enabled</Pill> : <Pill kind="off">Disabled</Pill>}</td>
                  <td>
                    <div className="row" style={{ justifyContent: "flex-end" }}>
                      <button className="btn btn-secondary btn-sm" onClick={() => setEditing(m)}>Edit</button>
                      <button className="btn btn-ghost btn-sm" onClick={() => remove(m)} style={{ color: "var(--danger-text)" }}>Remove</button>
                    </div>
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      )}

      {creating && (
        <CreateManager
          onClose={() => setCreating(false)}
          onCreated={() => { setCreating(false); reload(); toast("ok", "Manager added"); }}
        />
      )}

      {editing && (
        <EditManager
          manager={editing}
          onClose={() => setEditing(null)}
          onSaved={() => { setEditing(null); reload(); toast("ok", "Manager saved"); }}
        />
      )}
    </div>
  );
}

function EditManager({ manager, onClose, onSaved }: { manager: Manager; onClose: () => void; onSaved: () => void }) {
  const toast = useToast();
  const [name, setName] = useState(manager.name);
  const [enabled, setEnabled] = useState(manager.enabled);
  const [baseUrl, setBaseUrl] = useState(manager.lidarr_base_url ?? "");
  const [apiKey, setApiKey] = useState("");
  const [busy, setBusy] = useState(false);

  const submit = async (e: FormEvent) => {
    e.preventDefault();
    setBusy(true);
    try {
      const body: Record<string, unknown> = { name, enabled };
      if (manager.type === "lidarr") {
        body.lidarr_base_url = baseUrl;
        if (apiKey) body.lidarr_api_key = apiKey; // omit to keep the stored key
      }
      await api.put(`/managers/${manager.id}`, body);
      onSaved();
    } catch (e) {
      toast("err", errMsg(e));
      setBusy(false);
    }
  };

  return (
    <Modal title={`Edit ${manager.name}`} onClose={onClose}>
      <form onSubmit={submit} className="stack">
        <div className="field">
          <label className="flabel">Name</label>
          <input className="input" value={name} onChange={(e) => setName(e.target.value)} />
        </div>
        <label className="row" style={{ gap: 8, cursor: "pointer" }}>
          <input type="checkbox" checked={enabled} onChange={(e) => setEnabled(e.target.checked)} />
          <span>Enabled</span>
        </label>
        {manager.type === "lidarr" && (
          <>
            <div className="field">
              <label className="flabel">Lidarr base URL</label>
              <input className="input mono" value={baseUrl} onChange={(e) => setBaseUrl(e.target.value)} />
            </div>
            <div className="field">
              <label className="flabel">Lidarr API key</label>
              <input className="input mono" value={apiKey} onChange={(e) => setApiKey(e.target.value)} placeholder="leave blank to keep the current key" />
            </div>
          </>
        )}
        <div className="modal-actions">
          <button type="button" className="btn btn-ghost btn-sm" onClick={onClose}>Cancel</button>
          <button className="btn btn-primary btn-sm" disabled={busy || !name}>{busy ? "Saving…" : "Save changes"}</button>
        </div>
      </form>
    </Modal>
  );
}

function CreateManager({ onClose, onCreated }: { onClose: () => void; onCreated: () => void }) {
  const toast = useToast();
  const [name, setName] = useState("");
  const [type, setType] = useState("autotaggerr");
  const [baseUrl, setBaseUrl] = useState("");
  const [apiKey, setApiKey] = useState("");
  const [busy, setBusy] = useState(false);

  const submit = async (e: FormEvent) => {
    e.preventDefault();
    setBusy(true);
    try {
      const body: Record<string, unknown> = { name, type, enabled: true };
      if (type === "lidarr") {
        body.lidarr_base_url = baseUrl;
        body.lidarr_api_key = apiKey;
      }
      await api.post("/managers", body);
      onCreated();
    } catch (e) {
      toast("err", errMsg(e));
      setBusy(false);
    }
  };

  return (
    <Modal title="Add manager" onClose={onClose}>
      <form onSubmit={submit} className="stack">
        <div className="field">
          <label className="flabel">Name</label>
          <input className="input" value={name} onChange={(e) => setName(e.target.value)} autoFocus />
        </div>
        <div className="field">
          <label className="flabel">Type</label>
          <select className="select" value={type} onChange={(e) => setType(e.target.value)}>
            <option value="autotaggerr">Autotaggerr (native)</option>
            <option value="lidarr">Lidarr</option>
          </select>
        </div>
        {type === "lidarr" && (
          <>
            <div className="field">
              <label className="flabel">Lidarr base URL</label>
              <input className="input mono" value={baseUrl} onChange={(e) => setBaseUrl(e.target.value)} placeholder="https://lidarr.example.com" />
            </div>
            <div className="field">
              <label className="flabel">Lidarr API key</label>
              <input className="input mono" value={apiKey} onChange={(e) => setApiKey(e.target.value)} />
            </div>
          </>
        )}
        <div className="modal-actions">
          <button type="button" className="btn btn-ghost btn-sm" onClick={onClose}>Cancel</button>
          <button className="btn btn-primary btn-sm" disabled={busy || !name}>{busy ? "Adding…" : "Add manager"}</button>
        </div>
      </form>
    </Modal>
  );
}
