import { FormEvent, useState } from "react";
import { api, errMsg } from "../api";
import { useFetch } from "../hooks";
import { TaggerProfile } from "../types";
import { EmptyState, ErrorNote, Modal, Pill } from "../components/ui";
import { useToast } from "../toast";

export default function TaggerProfiles() {
  const { data, err, loading, reload } = useFetch<TaggerProfile[]>(() => api.get("/tagger-profiles"));
  const [editing, setEditing] = useState<TaggerProfile | null>(null);
  const toast = useToast();

  return (
    <div className="stack">
      <div className="page-head">
        <h1>Tagger profiles</h1>
      </div>
      <p className="muted" style={{ margin: 0, maxWidth: "68ch" }}>
        How tags are written — artist delimiter, whether to remove stale values, and more. A library uses one profile.
      </p>

      {err && <ErrorNote message={err} />}
      {!err && !loading && data && data.length === 0 && <EmptyState icon="✎" message="No tagger profiles yet." />}

      {data && data.length > 0 && (
        <div className="tablewrap">
          <table className="data">
            <thead>
              <tr><th>Name</th><th>Writes tags</th><th>Removes stale values</th><th>Artist delimiter</th><th style={{ textAlign: "right" }}>Actions</th></tr>
            </thead>
            <tbody>
              {data.map((p) => (
                <tr key={p.id}>
                  <td style={{ color: "var(--text)" }}>{p.name}</td>
                  <td>{p.write_tags ? <Pill kind="ok">Yes</Pill> : <Pill kind="off">No</Pill>}</td>
                  <td>{p.remove_values ? <Pill kind="warn">Yes</Pill> : <Pill kind="off">No</Pill>}</td>
                  <td className="mono" style={{ fontSize: 12 }}>
                    {p.use_custom_artist_delimiter ? `"${p.custom_artist_delimiter}"` : "MusicBrainz default"}
                  </td>
                  <td>
                    <div className="row" style={{ justifyContent: "flex-end" }}>
                      <button className="btn btn-secondary btn-sm" onClick={() => setEditing(p)}>Edit</button>
                    </div>
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      )}

      {editing && (
        <EditProfile
          profile={editing}
          onClose={() => setEditing(null)}
          onSaved={() => { setEditing(null); reload(); toast("ok", "Profile saved"); }}
        />
      )}
    </div>
  );
}

function Check({ label, checked, onChange }: { label: string; checked: boolean; onChange: (v: boolean) => void }) {
  return (
    <label className="row" style={{ gap: 8, cursor: "pointer" }}>
      <input type="checkbox" checked={checked} onChange={(e) => onChange(e.target.checked)} />
      <span>{label}</span>
    </label>
  );
}

function EditProfile({ profile, onClose, onSaved }: { profile: TaggerProfile; onClose: () => void; onSaved: () => void }) {
  const toast = useToast();
  const [p, setP] = useState<TaggerProfile>(profile);
  const [busy, setBusy] = useState(false);
  const set = (patch: Partial<TaggerProfile>) => setP((cur) => ({ ...cur, ...patch }));

  const submit = async (e: FormEvent) => {
    e.preventDefault();
    setBusy(true);
    try {
      await api.put(`/tagger-profiles/${profile.id}`, {
        name: p.name,
        write_tags: p.write_tags,
        remove_values: p.remove_values,
        use_current_artist_name: p.use_current_artist_name,
        use_custom_artist_delimiter: p.use_custom_artist_delimiter,
        custom_artist_delimiter: p.custom_artist_delimiter,
      });
      onSaved();
    } catch (e) {
      toast("err", errMsg(e));
      setBusy(false);
    }
  };

  return (
    <Modal title={`Edit ${profile.name}`} onClose={onClose}>
      <form onSubmit={submit} className="stack">
        <div className="field">
          <label className="flabel">Name</label>
          <input className="input" value={p.name} onChange={(e) => set({ name: e.target.value })} />
        </div>
        <Check label="Write tags to files" checked={p.write_tags} onChange={(v) => set({ write_tags: v })} />
        <Check label="Remove values that MusicBrainz doesn't provide" checked={p.remove_values} onChange={(v) => set({ remove_values: v })} />
        <Check label="Use the artist's current name" checked={p.use_current_artist_name} onChange={(v) => set({ use_current_artist_name: v })} />
        <Check label="Use a custom artist delimiter" checked={p.use_custom_artist_delimiter} onChange={(v) => set({ use_custom_artist_delimiter: v })} />
        {p.use_custom_artist_delimiter && (
          <div className="field">
            <label className="flabel">Artist delimiter</label>
            <input className="input mono" value={p.custom_artist_delimiter} onChange={(e) => set({ custom_artist_delimiter: e.target.value })} placeholder=" & " />
          </div>
        )}
        <div className="modal-actions">
          <button type="button" className="btn btn-ghost btn-sm" onClick={onClose}>Cancel</button>
          <button className="btn btn-primary btn-sm" disabled={busy || !p.name}>{busy ? "Saving…" : "Save changes"}</button>
        </div>
      </form>
    </Modal>
  );
}
