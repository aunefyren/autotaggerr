import { FormEvent, useState } from "react";
import { Link } from "react-router-dom";
import { api, errMsg } from "../api";
import { useFetch } from "../hooks";
import { MediaServer } from "../types";
import { EmptyState, ErrorNote, Modal, Pill } from "../components/ui";
import { useToast } from "../toast";

// The verdict from POST /media-servers/:id/test. A refused token still answers 200 —
// the request worked, the token did not — so `healthy` carries the result.
type ServerTest = { healthy: boolean; error?: string };

/**
 * Media servers: the Plex servers told to refresh an album after its files' tags
 * change. None, one or several; each library says which of them scan it, and in which
 * section, on its own form.
 */
export default function MediaServers() {
  const toast = useToast();
  const { data, err, loading, reload } = useFetch<MediaServer[]>(() => api.get("/media-servers"));
  const [editing, setEditing] = useState<MediaServer | "new" | null>(null);
  const [tests, setTests] = useState<Record<string, ServerTest>>({});
  const [testing, setTesting] = useState<string | null>(null);

  const remove = async (s: MediaServer) => {
    const scans = s.libraries === 1 ? "1 library stops" : `${s.libraries} libraries stop`;
    if (!confirm(`Remove media server "${s.name}"? ${s.libraries ? `${scans} being refreshed on it. ` : ""}Nothing on the server changes.`)) return;
    try {
      await api.del(`/media-servers/${s.id}`);
      toast("ok", `Removed ${s.name}`);
      reload();
    } catch (e) {
      toast("err", errMsg(e));
    }
  };

  const test = async (s: MediaServer) => {
    setTesting(s.id);
    try {
      const res = await api.post<ServerTest>(`/media-servers/${s.id}/test`, {});
      setTests((prev) => ({ ...prev, [s.id]: res }));
      toast(res.healthy ? "ok" : "err", res.healthy ? `${s.name} is reachable` : `${s.name} could not be reached`);
    } catch (e) {
      toast("err", errMsg(e));
    } finally {
      setTesting(null);
    }
  };

  return (
    <div className="stack">
      <div className="page-head">
        <h1>Media servers</h1>
        <button className="btn btn-primary btn-sm" onClick={() => setEditing("new")}>Add media server</button>
      </div>
      <p className="muted" style={{ margin: 0, maxWidth: "68ch", lineHeight: 1.55 }}>
        After a run changes an album's tags, every media server that scans its library is asked to re-read that
        album — only the album the server lists the file in, never one that merely shares its title. Which servers
        scan a library, and in which section, is set on <Link to="/libraries">Libraries</Link>. A server no library
        names is never sent anything.
      </p>

      {err && <ErrorNote message={err} />}
      {!err && !loading && data && data.length === 0 && (
        <EmptyState
          icon="▷"
          message="No media server yet. Add Plex to have albums refreshed after their tags change — optional; without one, nothing is notified."
          action={<button className="btn btn-primary btn-sm" onClick={() => setEditing("new")}>Add media server</button>}
        />
      )}

      {data && data.length > 0 && (
        <div className="tablewrap">
          <table className="data">
            <thead>
              <tr><th>Name</th><th>Type</th><th>Base URL</th><th>Libraries</th><th>State</th><th style={{ textAlign: "right" }}>Actions</th></tr>
            </thead>
            <tbody>
              {data.map((s) => (
                <tr key={s.id}>
                  <td style={{ color: "var(--text)" }}>{s.name}</td>
                  <td>Plex</td>
                  <td><span className="path">{s.base_url}</span></td>
                  <td className="mono">
                    {s.libraries > 0 ? s.libraries : <span className="dim" style={{ fontFamily: "var(--font-sans)" }}>None — nothing is sent</span>}
                  </td>
                  <td>
                    <div className="row" style={{ gap: 6, flexWrap: "wrap" }}>
                      {s.enabled ? <Pill kind="ok">Enabled</Pill> : <Pill kind="off">Disabled</Pill>}
                      {tests[s.id] && <Pill kind={tests[s.id].healthy ? "ok" : "err"}>{tests[s.id].healthy ? "Reachable" : "Unreachable"}</Pill>}
                    </div>
                  </td>
                  <td>
                    <div className="row" style={{ justifyContent: "flex-end" }}>
                      <button className="btn btn-secondary btn-sm" onClick={() => test(s)} disabled={testing === s.id} title="Ask the server who it is, with the token a refresh uses">
                        {testing === s.id ? "Testing…" : "Test"}
                      </button>
                      <button className="btn btn-secondary btn-sm" onClick={() => setEditing(s)}>Edit</button>
                      <button className="btn btn-ghost btn-sm" onClick={() => remove(s)} style={{ color: "var(--danger-text)" }}>Remove</button>
                    </div>
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      )}

      {data?.map((s) => {
        const t = tests[s.id];
        if (!t || t.healthy) return null;
        return (
          <div key={s.id} className="card" style={{ borderColor: "var(--danger)" }}>
            <strong style={{ display: "block", marginBottom: 8 }}>{s.name} could not be reached</strong>
            <div className="mono" style={{ fontSize: 12, whiteSpace: "pre-wrap", wordBreak: "break-word", color: "var(--danger-text)" }}>
              {t.error ?? "no further detail was reported"}
            </div>
          </div>
        );
      })}

      {editing && (
        <ServerForm
          server={editing === "new" ? null : editing}
          onClose={() => setEditing(null)}
          onSaved={(name) => {
            setEditing(null);
            reload();
            toast("ok", editing === "new" ? `Added ${name}` : `Saved ${name}`);
          }}
        />
      )}
    </div>
  );
}

function ServerForm({ server, onClose, onSaved }: { server: MediaServer | null; onClose: () => void; onSaved: (name: string) => void }) {
  const toast = useToast();
  const [name, setName] = useState(server?.name ?? "Plex");
  const [enabled, setEnabled] = useState(server?.enabled ?? true);
  const [baseUrl, setBaseUrl] = useState(server?.base_url ?? "");
  const [webUrl, setWebUrl] = useState(server?.web_url ?? "");
  const [token, setToken] = useState("");
  const [busy, setBusy] = useState(false);

  const submit = async (e: FormEvent) => {
    e.preventDefault();
    setBusy(true);
    try {
      const body: Record<string, unknown> = { name, type: "plex", enabled, base_url: baseUrl, web_url: webUrl };
      if (token || !server) body.token = token; // omit on edit to keep the stored one
      if (server) await api.put(`/media-servers/${server.id}`, body);
      else await api.post("/media-servers", body);
      onSaved(name);
    } catch (err) {
      toast("err", errMsg(err));
      setBusy(false);
    }
  };

  return (
    <Modal title={server ? `Edit ${server.name}` : "Add media server"} onClose={onClose}>
      <form onSubmit={submit} className="stack">
        <div className="row" style={{ gap: 12, flexWrap: "wrap", alignItems: "flex-end" }}>
          <div className="field" style={{ flex: "1 1 200px" }}>
            <label className="flabel" htmlFor="ms-name">Name</label>
            <input id="ms-name" className="input" value={name} onChange={(e) => setName(e.target.value)} autoFocus={!server} />
          </div>
          <div className="field" style={{ flex: "0 1 140px" }}>
            <label className="flabel" htmlFor="ms-type">Type</label>
            {/* One type today; the select says what this is and where Jellyfin would go. */}
            <select id="ms-type" className="select" value="plex" disabled>
              <option value="plex">Plex</option>
            </select>
          </div>
        </div>
        {server && (
          <label className="row" style={{ gap: 8, cursor: "pointer" }}>
            <input type="checkbox" checked={enabled} onChange={(e) => setEnabled(e.target.checked)} />
            <span>Enabled</span>
          </label>
        )}
        <div className="field">
          <label className="flabel" htmlFor="ms-url">Base URL</label>
          <input id="ms-url" className="input mono" value={baseUrl} onChange={(e) => setBaseUrl(e.target.value)} placeholder="http://192.168.1.10:32400" />
          <span className="dim" style={{ fontSize: 11 }}>The address Autotaggerr reaches the server at.</span>
        </div>
        <div className="field">
          <label className="flabel" htmlFor="ms-web">Link address</label>
          <input id="ms-web" className="input mono" value={webUrl} onChange={(e) => setWebUrl(e.target.value)} placeholder="app.plex.tv" />
          <span className="dim" style={{ fontSize: 11 }}>
            Where your browser reaches this server, for the Plex links on album pages — they then open its own web app
            rather than app.plex.tv. Leave empty to link through app.plex.tv, which works from anywhere you are signed in.
          </span>
        </div>
        <div className="field">
          <label className="flabel" htmlFor="ms-token">Token</label>
          <input
            id="ms-token"
            className="input mono"
            type="password"
            autoComplete="off"
            value={token}
            onChange={(e) => setToken(e.target.value)}
            placeholder={server?.token_set ? "Stored — leave empty to keep it" : "X-Plex-Token"}
          />
        </div>
        <div className="modal-actions">
          <button type="button" className="btn btn-ghost btn-sm" onClick={onClose}>Cancel</button>
          <button type="submit" className="btn btn-primary btn-sm" disabled={busy || !name.trim() || !baseUrl.trim() || (!server && !token)}>
            {busy ? "Saving…" : server ? "Save" : "Add media server"}
          </button>
        </div>
      </form>
    </Modal>
  );
}
