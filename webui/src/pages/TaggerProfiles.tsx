import { FormEvent, useState } from "react";
import { api, errMsg } from "../api";
import { useFetch } from "../hooks";
import { BPMWrite, TaggerProfile } from "../types";
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
        How tags are written — artist delimiter, whether to remove stale values, whether a measured
        tempo becomes a BPM tag, and more. A library uses one profile.
      </p>

      {err && <ErrorNote message={err} />}
      {!err && !loading && data && data.length === 0 && <EmptyState icon="✎" message="No tagger profiles yet." />}

      {data && data.length > 0 && (
        <div className="tablewrap">
          <table className="data">
            <thead>
              <tr><th>Name</th><th>Writes tags</th><th>Removes stale values</th><th>Artist delimiter</th><th>BPM</th><th style={{ textAlign: "right" }}>Actions</th></tr>
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
                    {p.bpm_write ? (
                      <span style={{ fontSize: 12 }}>
                        {BPM_WRITE_LABEL[p.bpm_write]}
                        <span className="dim mono" style={{ fontSize: 11 }}> ≥ {effectiveFloor(p).toFixed(2)}</span>
                      </span>
                    ) : (
                      <Pill kind="off">Not written</Pill>
                    )}
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

const BPM_WRITE_LABEL: Record<BPMWrite, string> = {
  "": "Not written",
  fill_empty: "Only where missing",
  overwrite: "Always",
};

/** The server reads 0 or less as its default floor; show what will actually apply. */
const DEFAULT_BPM_MIN_CONFIDENCE = 0.5;
const effectiveFloor = (p: TaggerProfile) =>
  p.bpm_min_confidence > 0 ? p.bpm_min_confidence : DEFAULT_BPM_MIN_CONFIDENCE;

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
        max_genres: p.max_genres,
        mp3_multi_value_tags: p.mp3_multi_value_tags,
        bpm_write: p.bpm_write ?? "",
        bpm_min_confidence: p.bpm_min_confidence,
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
        <div className="field">
          <label className="flabel">Maximum genres</label>
          <input
            className="input"
            type="number"
            min={1}
            max={50}
            value={p.max_genres || ""}
            onChange={(e) => set({ max_genres: Number(e.target.value) })}
            placeholder="5"
          />
          <p className="muted" style={{ margin: "4px 0 0", fontSize: 12 }}>
            MusicBrainz returns every genre that cleared its vote threshold, ranked here by vote count. Blank uses the default of 5.
          </p>
        </div>
        <div className="field">
          <Check
            label="Write true multi-value tags in MP3s"
            checked={p.mp3_multi_value_tags}
            onChange={(v) => set({ mp3_multi_value_tags: v })}
          />
          <p className="muted" style={{ margin: "4px 0 0", fontSize: 12 }}>
            Uses ID3v2.4's own multi-value form, which Picard, MusicBee, foobar2000 and Navidrome read
            natively. Plex reads tags through ffmpeg, which only sees the first value — so leave this
            off if Plex is your player. FLAC always uses the multi-value form; it costs nothing there.
          </p>
        </div>
        <div className="field">
          <label className="flabel">Write BPM</label>
          <select className="select" value={p.bpm_write ?? ""} onChange={(e) => set({ bpm_write: e.target.value as BPMWrite })}>
            <option value="">Don't write</option>
            <option value="fill_empty">Only where the file has none</option>
            <option value="overwrite">Always, replacing what is there</option>
          </select>
          <p className="muted" style={{ margin: "4px 0 0", fontSize: 12 }}>
            Writes the tempo measured by tempo analysis (turned on per library) as <span className="mono">BPM</span> in
            FLAC and <span className="mono">TBPM</span> in MP3, rounded to a whole number. A BPM another tool
            wrote is never cleared when there is nothing to write. Files analysed before you turn this on
            get their BPM on the next <em>Tag files</em>; Process picks up the ones analysed after.
          </p>
        </div>
        <div className="field">
          <label className="flabel">Minimum confidence</label>
          <input
            className="input"
            type="number"
            min={0.05}
            max={1}
            step={0.05}
            disabled={!p.bpm_write}
            title={p.bpm_write ? undefined : "Only applies when BPM is written."}
            value={p.bpm_min_confidence > 0 ? p.bpm_min_confidence : ""}
            onChange={(e) => set({ bpm_min_confidence: e.target.value === "" ? 0 : Number(e.target.value) })}
            placeholder={DEFAULT_BPM_MIN_CONFIDENCE.toFixed(2)}
          />
          <p className="muted" style={{ margin: "4px 0 0", fontSize: 12 }}>
            How regular the beat has to be (0–1) for the tempo to be written. Steady dance and pop
            measures 0.85 and up; clearly wrong readings sit near 0.1. Blank uses 0.50. It measures how
            steady the beat grid is, not whether it is the right one — half- and double-time readings
            can still score high.
          </p>
        </div>
        <div className="modal-actions">
          <button type="button" className="btn btn-ghost btn-sm" onClick={onClose}>Cancel</button>
          <button className="btn btn-primary btn-sm" disabled={busy || !p.name}>{busy ? "Saving…" : "Save changes"}</button>
        </div>
      </form>
    </Modal>
  );
}
