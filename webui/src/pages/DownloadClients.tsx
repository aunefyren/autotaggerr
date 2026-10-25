import { FormEvent, useState } from "react";
import { Link } from "react-router-dom";
import { api, errMsg } from "../api";
import { useFetch } from "../hooks";
import { DownloadClient, DownloadClientCategory, Library, Manager, PathMapping } from "../types";
import { EmptyState, ErrorNote, Modal, Note, Pill } from "../components/ui";
import { useToast } from "../toast";
import { useSetup } from "../setup";

// The verdict from POST /download-clients/:id/test. A refused login still answers 200
// — the request worked, the credentials did not — so `healthy` carries the result.
type ClientTest = {
  healthy: boolean;
  username_set: boolean;
  password_set: boolean;
  error?: string;
};

/** The copy-while-seeding cost, said wherever someone decides to rely on it. */
export function CopyWarning() {
  return (
    <Note kind="warn" title="Imported albums take space twice">
      While a torrent is in qBittorrent its files are copied into the library, never moved — moving them
      would break the torrent. Each imported album therefore takes its space twice until you remove the
      torrent in qBittorrent.
    </Note>
  );
}

/**
 * Download clients: where finished downloads are picked up for import. Each watched
 * category feeds one library; downloads in it are listed on the Import page and
 * imported only after a review there.
 */
export default function DownloadClients() {
  const toast = useToast();
  const { data, err, loading, reload } = useFetch<DownloadClient[]>(() => api.get("/download-clients"));
  const setup = useSetup();
  const libraries = useFetch<Library[]>(() => api.get("/libraries"));
  const managers = useFetch<Manager[]>(() => api.get("/managers"));
  const [editing, setEditing] = useState<DownloadClient | "new" | null>(null);
  const [tests, setTests] = useState<Record<string, ClientTest>>({});
  const [testing, setTesting] = useState<string | null>(null);

  const libraryName = (id: string) => libraries.data?.find((l) => l.id === id)?.name ?? "missing library";

  const remove = async (c: DownloadClient) => {
    if (!confirm(`Remove download client "${c.name}"? Its downloads leave the Import page; nothing in qBittorrent or on disk changes.`)) return;
    try {
      await api.del(`/download-clients/${c.id}`);
      toast("ok", `Removed ${c.name}`);
      reload();
    } catch (e) {
      toast("err", errMsg(e));
    }
  };

  const test = async (c: DownloadClient) => {
    setTesting(c.id);
    try {
      const res = await api.post<ClientTest>(`/download-clients/${c.id}/test`, {});
      setTests((prev) => ({ ...prev, [c.id]: res }));
      toast(res.healthy ? "ok" : "err", res.healthy ? `${c.name} is reachable` : `${c.name} could not be reached`);
    } catch (e) {
      toast("err", errMsg(e));
    } finally {
      setTesting(null);
    }
  };

  return (
    <div className="stack">
      <div className="page-head">
        <h1>Download clients</h1>
        <button className="btn btn-primary btn-sm" onClick={() => setEditing("new")}>Add download client</button>
      </div>
      <p className="muted" style={{ margin: 0, maxWidth: "68ch", lineHeight: 1.55 }}>
        Finished downloads in a watched category are listed on <Link to="/import">Import</Link>, ready to be
        identified and imported into that category's library. Nothing is imported without your review, and
        the only change Autotaggerr makes in the client is a <span className="mono">autotaggerr-imported</span> tag
        on a torrent once every album in it is in. Other categories — Lidarr's, say — are never read.
      </p>

      {err && <ErrorNote message={err} />}
      {!err && !loading && data && data.length === 0 && (
        <EmptyState
          icon="⇣"
          message="No download client yet. Add qBittorrent to have its finished downloads offered for import."
          action={<button className="btn btn-primary btn-sm" onClick={() => setEditing("new")}>Add download client</button>}
        />
      )}

      {data && data.length > 0 && (
        <div className="tablewrap">
          <table className="data">
            <thead>
              <tr><th>Name</th><th>Base URL</th><th>Watches</th><th>State</th><th style={{ textAlign: "right" }}>Actions</th></tr>
            </thead>
            <tbody>
              {data.map((c) => (
                <tr key={c.id}>
                  <td style={{ color: "var(--text)" }}>{c.name}</td>
                  <td><span className="path">{c.base_url}</span></td>
                  <td>
                    {c.categories.length === 0 ? (
                      <span className="dim">No category — nothing is read</span>
                    ) : (
                      <div className="stack" style={{ gap: 2 }}>
                        {c.categories.map((cat) => (
                          <span key={cat.category}>
                            <span className="mono">{cat.category}</span> <span className="dim">→ {libraryName(cat.library_id)}</span>
                            {cat.auto_import && <span className="dim"> · imports automatically</span>}
                          </span>
                        ))}
                      </div>
                    )}
                  </td>
                  <td>
                    <div className="row" style={{ gap: 6, flexWrap: "wrap" }}>
                      {c.enabled ? <Pill kind="ok">Enabled</Pill> : <Pill kind="off">Disabled</Pill>}
                      {c.last_poll_error && <Pill kind="err">Last check failed</Pill>}
                      {tests[c.id] && <Pill kind={tests[c.id].healthy ? "ok" : "err"}>{tests[c.id].healthy ? "Reachable" : "Unreachable"}</Pill>}
                    </div>
                  </td>
                  <td>
                    <div className="row" style={{ justifyContent: "flex-end" }}>
                      <button className="btn btn-secondary btn-sm" onClick={() => test(c)} disabled={testing === c.id} title="Log in and read the version, with the credentials a check uses">
                        {testing === c.id ? "Testing…" : "Test"}
                      </button>
                      <button className="btn btn-secondary btn-sm" onClick={() => setEditing(c)}>Edit</button>
                      <button className="btn btn-ghost btn-sm" onClick={() => remove(c)} style={{ color: "var(--danger-text)" }}>Remove</button>
                    </div>
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      )}

      {data?.map((c) => {
        const t = tests[c.id];
        const problem = t && !t.healthy ? t.error ?? "no further detail was reported" : !t ? c.last_poll_error : "";
        if (!problem) return null;
        return (
          <div key={c.id} className="card" style={{ borderColor: "var(--danger)" }}>
            <div className="row" style={{ justifyContent: "space-between", marginBottom: 8 }}>
              <strong>{t ? `${c.name} could not be reached` : `${c.name}: the last check failed`}</strong>
              {t && (
                <span className="muted" style={{ fontSize: 12 }}>
                  username {t.username_set ? "set" : "not set"} · password {t.password_set ? "set" : "not set"}
                </span>
              )}
            </div>
            <div className="mono" style={{ fontSize: 12, whiteSpace: "pre-wrap", wordBreak: "break-word", color: "var(--danger-text)" }}>{problem}</div>
          </div>
        );
      })}

      {data && data.length > 0 && <CopyWarning />}

      {editing && (
        <ClientForm
          client={editing === "new" ? null : editing}
          libraries={libraries.data ?? []}
          managers={managers.data ?? []}
          onClose={() => setEditing(null)}
          onSaved={(name) => {
            setEditing(null);
            reload();
            setup.reload();
            toast("ok", editing === "new" ? `Added ${name}` : `Saved ${name}`);
          }}
        />
      )}
    </div>
  );
}

function ClientForm({
  client,
  libraries,
  managers,
  onClose,
  onSaved,
}: {
  client: DownloadClient | null;
  libraries: Library[];
  managers: Manager[];
  onClose: () => void;
  onSaved: (name: string) => void;
}) {
  const toast = useToast();
  const [name, setName] = useState(client?.name ?? "qBittorrent");
  const [enabled, setEnabled] = useState(client?.enabled ?? true);
  const [baseUrl, setBaseUrl] = useState(client?.base_url ?? "");
  const [username, setUsername] = useState(client?.username ?? "");
  const [password, setPassword] = useState("");
  const [categories, setCategories] = useState<DownloadClientCategory[]>(client?.categories ?? []);
  const [mappings, setMappings] = useState<PathMapping[]>(client?.path_mappings ?? []);
  const [busy, setBusy] = useState(false);

  // A Lidarr-managed library cannot take an import, so it is listed, disabled, with why.
  const lidarrManaged = (l: Library) => !!l.manager_id && managers.find((m) => m.id === l.manager_id)?.type === "lidarr";
  const eligible = libraries.filter((l) => l.enabled && !lidarrManaged(l));

  const setCategory = (i: number, patch: Partial<DownloadClientCategory>) =>
    setCategories((cur) => cur.map((c, j) => (j === i ? { ...c, ...patch } : c)));
  const setMapping = (i: number, patch: Partial<PathMapping>) =>
    setMappings((cur) => cur.map((m, j) => (j === i ? { ...m, ...patch } : m)));

  const submit = async (e: FormEvent) => {
    e.preventDefault();
    setBusy(true);
    try {
      const body: Record<string, unknown> = {
        name,
        type: "qbittorrent",
        enabled,
        base_url: baseUrl,
        username,
        path_mappings: mappings,
        categories: categories.map((c) => ({ category: c.category, library_id: c.library_id, auto_import: !!c.auto_import })),
      };
      if (password || !client) body.password = password; // omit on edit to keep the stored one
      if (client) await api.put(`/download-clients/${client.id}`, body);
      else await api.post("/download-clients", body);
      onSaved(name);
    } catch (err) {
      toast("err", errMsg(err));
      setBusy(false);
    }
  };

  return (
    <Modal title={client ? `Edit ${client.name}` : "Add download client"} onClose={onClose} wide>
      <form onSubmit={submit} className="stack">
        <div className="row" style={{ gap: 12, flexWrap: "wrap", alignItems: "flex-end" }}>
          <div className="field" style={{ flex: "1 1 200px" }}>
            <label className="flabel" htmlFor="dc-name">Name</label>
            <input id="dc-name" className="input" value={name} onChange={(e) => setName(e.target.value)} autoFocus={!client} />
          </div>
          <div className="field" style={{ flex: "0 1 160px" }}>
            <label className="flabel" htmlFor="dc-type">Type</label>
            <select id="dc-type" className="select" value="qbittorrent" disabled>
              <option value="qbittorrent">qBittorrent</option>
            </select>
          </div>
        </div>
        {client && (
          <label className="row" style={{ gap: 8, cursor: "pointer" }}>
            <input type="checkbox" checked={enabled} onChange={(e) => setEnabled(e.target.checked)} />
            <span>Enabled</span>
          </label>
        )}
        <div className="field">
          <label className="flabel" htmlFor="dc-url">Web UI address</label>
          <input id="dc-url" className="input mono" value={baseUrl} onChange={(e) => setBaseUrl(e.target.value)} placeholder="http://192.168.1.10:8080" />
        </div>
        <div className="row" style={{ gap: 12, flexWrap: "wrap" }}>
          <div className="field" style={{ flex: "1 1 200px" }}>
            <label className="flabel" htmlFor="dc-user">Username</label>
            <input id="dc-user" className="input mono" value={username} onChange={(e) => setUsername(e.target.value)} autoComplete="off" />
          </div>
          <div className="field" style={{ flex: "1 1 200px" }}>
            <label className="flabel" htmlFor="dc-pass">Password</label>
            <input
              id="dc-pass"
              type="password"
              className="input mono"
              value={password}
              onChange={(e) => setPassword(e.target.value)}
              autoComplete="new-password"
              placeholder={client?.password_set ? "leave blank to keep the current password" : ""}
            />
          </div>
        </div>
        <p className="muted" style={{ margin: "-4px 0 0", fontSize: 12 }}>
          Leave both empty if qBittorrent lets this host skip authentication (<em>Bypass authentication for clients
          on localhost</em> or <em>in whitelisted IP subnets</em>).
        </p>

        <div className="stack" style={{ gap: 8 }}>
          <span className="flabel">Watched categories</span>
          {categories.length === 0 && <span className="dim" style={{ fontSize: 12 }}>None yet — without one, nothing is read from this client.</span>}
          {categories.map((c, i) => (
            <div key={i} className="row" style={{ gap: 8, flexWrap: "wrap" }}>
              <input
                className="input mono"
                style={{ flex: "1 1 160px" }}
                value={c.category}
                onChange={(e) => setCategory(i, { category: e.target.value })}
                placeholder="music"
                aria-label="Category"
              />
              <span className="dim" aria-hidden="true">→</span>
              <select className="select" style={{ flex: "1 1 200px" }} value={c.library_id} onChange={(e) => setCategory(i, { library_id: e.target.value })} aria-label={`Library for ${c.category || "this category"}`}>
                <option value="">Choose a library</option>
                {libraries.map((l) => (
                  <option key={l.id} value={l.id} disabled={!eligible.includes(l)}>
                    {l.name}
                    {lidarrManaged(l) ? " — managed by Lidarr" : !l.enabled ? " — disabled" : ""}
                  </option>
                ))}
              </select>
              <label className="row" style={{ gap: 6, fontSize: 12, cursor: "pointer" }}>
                <input type="checkbox" checked={!!c.auto_import} onChange={(e) => setCategory(i, { auto_import: e.target.checked })} />
                Import automatically
              </label>
              <button type="button" className="btn btn-ghost btn-sm" onClick={() => setCategories((cur) => cur.filter((_, j) => j !== i))}>Remove</button>
            </div>
          ))}
          <div>
            <button type="button" className="btn btn-secondary btn-sm" onClick={() => setCategories((cur) => [...cur, { category: "", library_id: eligible[0]?.id ?? "", auto_import: false }])}>
              Add category
            </button>
          </div>
          <span className="dim" style={{ fontSize: 11 }}>The category set on the torrent in qBittorrent, exactly. Use one Lidarr does not.</span>
          {categories.some((c) => c.auto_import) && (
            <p className="muted" style={{ margin: 0, fontSize: 12, maxWidth: "68ch" }}>
              <strong>Import automatically</strong> imports an album without a review only when its own tags decide
              everything: every file names the same MusicBrainz release, each file is a different track of it (by its
              track ID, or its filename's number when no file has one), the file count matches the release, and nothing
              in the library is in the way. Anything less certain — an upgrade of an album you already have, too — waits on
              the Import page with the reason, as it would with this off.
            </p>
          )}
        </div>

        <div className="stack" style={{ gap: 8 }}>
          <span className="flabel">Path mappings</span>
          {mappings.length === 0 && (
            <span className="dim" style={{ fontSize: 12 }}>None — qBittorrent's paths are read as they are, which is right when both run on one host.</span>
          )}
          {mappings.map((m, i) => (
            <div key={i} className="row" style={{ gap: 8, flexWrap: "wrap" }}>
              <input className="input mono" style={{ flex: "1 1 200px" }} value={m.remote} onChange={(e) => setMapping(i, { remote: e.target.value })} placeholder="/downloads" aria-label="Path in qBittorrent" />
              <span className="dim" aria-hidden="true">→</span>
              <input className="input mono" style={{ flex: "1 1 200px" }} value={m.local} onChange={(e) => setMapping(i, { local: e.target.value })} placeholder="/mnt/torrents" aria-label="Same folder on this host" />
              <button type="button" className="btn btn-ghost btn-sm" onClick={() => setMappings((cur) => cur.filter((_, j) => j !== i))}>Remove</button>
            </div>
          ))}
          <div>
            <button type="button" className="btn btn-secondary btn-sm" onClick={() => setMappings((cur) => [...cur, { remote: "", local: "" }])}>
              Add path mapping
            </button>
          </div>
          <span className="dim" style={{ fontSize: 11 }}>
            When qBittorrent runs in another container or on another machine: the folder as qBittorrent reports it, and
            where the same folder is on this host.
          </span>
        </div>

        <CopyWarning />

        <div className="modal-actions">
          <button type="button" className="btn btn-ghost btn-sm" onClick={onClose}>Cancel</button>
          <button className="btn btn-primary btn-sm" disabled={busy || !name.trim() || !baseUrl.trim()}>
            {busy ? "Saving…" : client ? "Save changes" : "Add download client"}
          </button>
        </div>
      </form>
    </Modal>
  );
}
