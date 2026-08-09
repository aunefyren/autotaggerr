import { FormEvent, useState } from "react";
import { api, errMsg } from "../api";
import { useFetch } from "../hooks";
import { Manager } from "../types";
import { EmptyState, ErrorNote, Modal, Pill } from "../components/ui";
import { useToast } from "../toast";

// ManagerTest is the verdict from POST /managers/:id/test. A rejected probe still
// answers 200 — the request worked, the credentials did not — so `healthy` carries the
// result and `error` carries the reason verbatim.
type ManagerTest = {
  healthy: boolean;
  api_key_set: boolean;
  cookie_set: boolean;
  error?: string;
};

export default function Managers() {
  const toast = useToast();
  const { data, err, loading, reload } = useFetch<Manager[]>(() => api.get("/managers"));
  const [creating, setCreating] = useState(false);
  const [editing, setEditing] = useState<Manager | null>(null);
  const [tests, setTests] = useState<Record<string, ManagerTest>>({});
  const [testing, setTesting] = useState<string | null>(null);

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

  const test = async (m: Manager) => {
    setTesting(m.id);
    try {
      const res = await api.post<ManagerTest>(`/managers/${m.id}/test`, {});
      setTests((prev) => ({ ...prev, [m.id]: res }));
      // The failure text is the whole point of the button, and it is long — a proxy
      // redirect or a rejected cookie names a URL and quotes a body. It goes in the
      // panel under the table where it can be read and copied, not into a toast.
      toast(res.healthy ? "ok" : "err", res.healthy ? `${m.name} is reachable` : `${m.name} could not be reached`);
    } catch (e) {
      toast("err", errMsg(e));
    } finally {
      setTesting(null);
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
        Lidarr; Autotaggerr resolves it natively from the files. These credentials are the ones every
        scan uses — <span className="mono">config.json</span> only seeds them on first run and is not
        read for them again, so edit them here.
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
                  <td>
                    <div className="row" style={{ gap: 6 }}>
                      {m.enabled ? <Pill kind="ok">Enabled</Pill> : <Pill kind="off">Disabled</Pill>}
                      {tests[m.id] && (
                        <Pill kind={tests[m.id].healthy ? "ok" : "err"}>
                          {tests[m.id].healthy ? "Reachable" : "Unreachable"}
                        </Pill>
                      )}
                    </div>
                  </td>
                  <td>
                    <div className="row" style={{ justifyContent: "flex-end" }}>
                      <button
                        className="btn btn-secondary btn-sm"
                        onClick={() => test(m)}
                        disabled={testing === m.id}
                        title="Probe this manager with the credentials a scan would use"
                      >
                        {testing === m.id ? "Testing…" : "Test"}
                      </button>
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

      {data?.map((m) => {
        const t = tests[m.id];
        if (!t || t.healthy) return null;
        return (
          <div key={m.id} className="card" style={{ borderColor: "var(--danger)" }}>
            <div className="row" style={{ justifyContent: "space-between", marginBottom: 8 }}>
              <strong>{m.name} could not be reached</strong>
              <span className="muted" style={{ fontSize: 12 }}>
                API key {t.api_key_set ? "set" : "not set"} · cookie {t.cookie_set ? "set" : "not set"}
              </span>
            </div>
            <div className="mono" style={{ fontSize: 12, whiteSpace: "pre-wrap", wordBreak: "break-word", color: "var(--danger-text)" }}>
              {t.error ?? "no further detail was reported"}
            </div>
          </div>
        );
      })}

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
  const [cookie, setCookie] = useState("");
  const [busy, setBusy] = useState(false);

  const submit = async (e: FormEvent) => {
    e.preventDefault();
    setBusy(true);
    try {
      const body: Record<string, unknown> = { name, enabled };
      if (manager.type === "lidarr") {
        body.lidarr_base_url = baseUrl;
        if (apiKey) body.lidarr_api_key = apiKey; // omit to keep the stored key
        if (cookie) body.lidarr_header_cookie = cookie; // omit to keep the stored cookie
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
            <div className="field">
              <label className="flabel">Auth proxy cookie</label>
              <input className="input mono" value={cookie} onChange={(e) => setCookie(e.target.value)} placeholder="leave blank to keep the current cookie" />
              <p className="muted" style={{ margin: "6px 0 0", fontSize: 12 }}>
                Only needed when Lidarr sits behind an authentication proxy (Authelia and similar).
                One <span className="mono">name=value</span> pair, exactly as the browser sends it —
                and it is the cookie for <em>Lidarr's</em> hostname, whose name is often
                domain-specific. These sessions expire; use <strong>Test</strong> above to check.
              </p>
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
  const [cookie, setCookie] = useState("");
  const [busy, setBusy] = useState(false);

  const submit = async (e: FormEvent) => {
    e.preventDefault();
    setBusy(true);
    try {
      const body: Record<string, unknown> = { name, type, enabled: true };
      if (type === "lidarr") {
        body.lidarr_base_url = baseUrl;
        body.lidarr_api_key = apiKey;
        if (cookie) body.lidarr_header_cookie = cookie;
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
            <div className="field">
              <label className="flabel">Auth proxy cookie</label>
              <input className="input mono" value={cookie} onChange={(e) => setCookie(e.target.value)} placeholder="optional — authelia_session=…" />
              <p className="muted" style={{ margin: "6px 0 0", fontSize: 12 }}>
                Only needed when Lidarr sits behind an authentication proxy. One{" "}
                <span className="mono">name=value</span> pair, for Lidarr's own hostname.
              </p>
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
