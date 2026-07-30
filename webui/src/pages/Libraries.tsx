import { FormEvent, useState } from "react";
import { api, errMsg } from "../api";
import { useFetch } from "../hooks";
import { DataSource, Library, Manager, TaggerProfile, dataSourceCategory } from "../types";
import { EmptyState, ErrorNote, Modal, Pill } from "../components/ui";
import { useToast } from "../toast";

interface Options {
  managers: Manager[];
  dataSources: DataSource[];
  profiles: TaggerProfile[];
}

export default function Libraries() {
  const toast = useToast();
  const libs = useFetch<Library[]>(() => api.get("/libraries"));
  const managers = useFetch<Manager[]>(() => api.get("/managers"));
  const dataSources = useFetch<DataSource[]>(() => api.get("/data-sources"));
  const profiles = useFetch<TaggerProfile[]>(() => api.get("/tagger-profiles"));

  const [creating, setCreating] = useState(false);
  const [editing, setEditing] = useState<Library | null>(null);

  const options: Options = {
    managers: managers.data ?? [],
    dataSources: dataSources.data ?? [],
    profiles: profiles.data ?? [],
  };
  const managerName = (id: string | null) => (id ? options.managers.find((m) => m.id === id)?.name : undefined);

  // The same three verbs the artist page offers, aimed at one library. None of
  // them cascades into another: each does what its label says and stops.
  const action = (l: Library, verb: string, started: string) => async () => {
    try {
      await api.post(`/libraries/${l.id}/${verb}`);
      toast("info", `${started} for ${l.name}`);
    } catch (e) {
      toast("err", errMsg(e));
    }
  };
  const toggle = async (l: Library) => {
    try {
      await api.put(`/libraries/${l.id}`, { enabled: !l.enabled });
      libs.reload();
    } catch (e) {
      toast("err", errMsg(e));
    }
  };
  const remove = async (l: Library) => {
    if (!confirm(`Remove library "${l.name}"? Its files are not touched.`)) return;
    try {
      await api.del(`/libraries/${l.id}`);
      toast("ok", `Removed ${l.name}`);
      libs.reload();
    } catch (e) {
      toast("err", errMsg(e));
    }
  };

  return (
    <div className="stack">
      <div className="page-head">
        <h1>Libraries</h1>
        <button className="btn btn-primary btn-sm" onClick={() => setCreating(true)}>Add library</button>
      </div>

      {libs.err && <ErrorNote message={libs.err} />}
      {!libs.err && !libs.loading && libs.data && libs.data.length === 0 && (
        <EmptyState
          icon="▤"
          message="No libraries yet — add your first music folder to start tagging."
          action={<button className="btn btn-primary btn-sm" onClick={() => setCreating(true)}>Add library</button>}
        />
      )}

      {libs.data && libs.data.length > 0 && (
        <div className="tablewrap">
          <table className="data">
            <thead>
              <tr>
                <th>Name</th>
                <th>Path</th>
                <th>Manager</th>
                <th>State</th>
                <th>Last scan</th>
                <th style={{ textAlign: "right" }}>Actions</th>
              </tr>
            </thead>
            <tbody>
              {libs.data.map((l) => (
                <tr key={l.id}>
                  <td style={{ color: "var(--text)" }}>{l.name}</td>
                  <td><span className="path">{l.path}</span></td>
                  <td>{managerName(l.manager_id) ?? <span className="dim">— fallback</span>}</td>
                  <td>{l.enabled ? <Pill kind="ok">Enabled</Pill> : <Pill kind="off">Disabled</Pill>}</td>
                  <td className="mono dim" style={{ fontSize: 11 }}>
                    {l.last_scan ? new Date(l.last_scan).toLocaleString() : "never"}
                  </td>
                  <td>
                    <div className="row" style={{ justifyContent: "flex-end" }}>
                      <button
                        className="btn btn-secondary btn-sm"
                        onClick={action(l, "scan", "Scan started")}
                        title="Walk this library and process new or changed files. Writes tags."
                      >
                        Scan
                      </button>
                      <button
                        className="btn btn-ghost btn-sm"
                        onClick={action(l, "refresh", "Metadata refresh started")}
                        title="Re-read MusicBrainz for everything this library's files point at, ignoring the cache. Reads only: no files are written. Anything that changed is reported, and Tag files (or the next scan) applies it."
                      >
                        Refresh metadata
                      </button>
                      <button
                        className="btn btn-ghost btn-sm"
                        onClick={action(l, "retag", "Tagging started")}
                        title="Rewrite the tags of this library's indexed files from the metadata already known. Writes tags. No disk walk, no MusicBrainz lookups."
                      >
                        Tag files
                      </button>
                      <button className="btn btn-ghost btn-sm" onClick={() => setEditing(l)}>Edit</button>
                      <button className="btn btn-ghost btn-sm" onClick={() => toggle(l)}>{l.enabled ? "Disable" : "Enable"}</button>
                      <button className="btn btn-ghost btn-sm" onClick={() => remove(l)} style={{ color: "var(--danger-text)" }}>Remove</button>
                    </div>
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      )}

      {creating && (
        <LibraryForm
          options={options}
          onClose={() => setCreating(false)}
          onSaved={() => { setCreating(false); libs.reload(); toast("ok", "Library added"); }}
        />
      )}
      {editing && (
        <LibraryForm
          initial={editing}
          options={options}
          onClose={() => setEditing(null)}
          onSaved={() => { setEditing(null); libs.reload(); toast("ok", "Library saved"); }}
        />
      )}
    </div>
  );
}

function LibraryForm({ initial, options, onClose, onSaved }: { initial?: Library; options: Options; onClose: () => void; onSaved: () => void }) {
  const toast = useToast();
  const editing = !!initial;
  const [name, setName] = useState(initial?.name ?? "");
  const [path, setPath] = useState(initial?.path ?? "");
  const [cron, setCron] = useState(initial?.cron ?? "");
  const [managerId, setManagerId] = useState(initial?.manager_id ?? "");
  const [dataSourceId, setDataSourceId] = useState(initial?.data_source_id ?? "");
  const [taggerProfileId, setTaggerProfileId] = useState(initial?.tagger_profile_id ?? "");
  const [useAcoustID, setUseAcoustID] = useState(initial?.use_acoustid ?? false);
  const [busy, setBusy] = useState(false);

  const submit = async (e: FormEvent) => {
    e.preventDefault();
    setBusy(true);
    try {
      const body: Record<string, unknown> = { name, path, use_acoustid: useAcoustID };
      if (editing) body.cron = cron;
      // Only send an ID when one is chosen; "None" leaves the field unset.
      if (managerId) body.manager_id = managerId;
      if (dataSourceId) body.data_source_id = dataSourceId;
      if (taggerProfileId) body.tagger_profile_id = taggerProfileId;

      if (editing) await api.put(`/libraries/${initial!.id}`, body);
      else await api.post("/libraries", { ...body, enabled: true });
      onSaved();
    } catch (e) {
      toast("err", errMsg(e));
      setBusy(false);
    }
  };

  return (
    <Modal title={editing ? `Edit ${initial!.name}` : "Add library"} onClose={onClose}>
      <form onSubmit={submit} className="stack">
        <div className="field">
          <label className="flabel">Name</label>
          <input className="input" value={name} onChange={(e) => setName(e.target.value)} placeholder="Music" autoFocus />
        </div>
        <div className="field">
          <label className="flabel">Folder path</label>
          <input className="input mono" value={path} onChange={(e) => setPath(e.target.value)} placeholder="/music" />
          <span className="dim" style={{ fontSize: 11 }}>The folder that contains your artist folders.</span>
        </div>

        <div className="field">
          <label className="flabel">Manager</label>
          <select className="select" value={managerId} onChange={(e) => setManagerId(e.target.value)}>
            <option value="">Default (first configured manager)</option>
            {options.managers.map((m) => (
              <option key={m.id} value={m.id}>{m.name} ({m.type})</option>
            ))}
          </select>
          <span className="dim" style={{ fontSize: 11 }}>Decides which MusicBrainz release each file maps to.</span>
        </div>

        {/* Metadata providers only. Fingerprinting and artwork sources live in the same
            table but cannot supply tags, and offering them here was the reason this
            field read as nonsense. The API rejects them too. */}
        <div className="field">
          <label className="flabel">Metadata source</label>
          <select className="select" value={dataSourceId} onChange={(e) => setDataSourceId(e.target.value)}>
            <option value="">Default (first configured)</option>
            {options.dataSources
              .filter((d) => dataSourceCategory(d.type) === "metadata")
              .map((d) => (
                <option key={d.id} value={d.id}>{d.name}</option>
              ))}
          </select>
          <span className="dim" style={{ fontSize: 11 }}>Where this library's release metadata is fetched from.</span>
        </div>

        <div className="field">
          <label className="flabel">Tagger profile</label>
          <select className="select" value={taggerProfileId} onChange={(e) => setTaggerProfileId(e.target.value)}>
            <option value="">Default</option>
            {options.profiles.map((p) => (
              <option key={p.id} value={p.id}>{p.name}</option>
            ))}
          </select>
        </div>

        <label className="row" style={{ gap: 8, cursor: "pointer" }}>
          <input type="checkbox" checked={useAcoustID} onChange={(e) => setUseAcoustID(e.target.checked)} />
          <span style={{ fontSize: 12 }}>Allow audio fingerprint identification</span>
        </label>
        <span className="dim" style={{ fontSize: 11, marginTop: -6 }}>
          Lets you identify an unmatched file from its audio when attaching it. Needs an AcoustID
          data source and fpcalc on the server; it only ever suggests, never tags on its own.
        </span>

        {editing && (
          <div className="field">
            <label className="flabel">Scan schedule (cron)</label>
            <input className="input mono" value={cron} onChange={(e) => setCron(e.target.value)} placeholder="0 0 18 * * 7" />
          </div>
        )}

        <div className="modal-actions">
          <button type="button" className="btn btn-ghost btn-sm" onClick={onClose}>Cancel</button>
          <button className="btn btn-primary btn-sm" disabled={busy || !name || !path}>
            {busy ? "Saving…" : editing ? "Save changes" : "Add library"}
          </button>
        </div>
      </form>
    </Modal>
  );
}
