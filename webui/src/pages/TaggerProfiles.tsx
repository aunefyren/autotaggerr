import { FormEvent, useEffect, useState } from "react";
import { api, errMsg } from "../api";
import { useDebounced, useFetch } from "../hooks";
import { NamingField, NamingInfo, NamingPreview, NamingTemplates, TaggerProfile } from "../types";
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
        How tags are written — artist delimiter, whether to remove stale values, and more — and how
        files are named under the library root. A library uses one profile. Whether a measured tempo becomes a BPM tag is set on the library, beside
        tempo analysis.
      </p>

      {err && <ErrorNote message={err} />}
      {!err && !loading && data && data.length === 0 && <EmptyState icon="✎" message="No tagger profiles yet." />}

      {data && data.length > 0 && (
        <div className="tablewrap">
          <table className="data">
            <thead>
              <tr><th>Name</th><th>Writes tags</th><th>Removes stale values</th><th>Artist delimiter</th><th>Naming</th><th style={{ textAlign: "right" }}>Actions</th></tr>
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
                  <td>{hasCustomNaming(p) ? <Pill kind="chg">Custom</Pill> : <Pill kind="off">Default</Pill>}</td>
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

function hasCustomNaming(p: TaggerProfile) {
  return !!(p.naming_artist_folder || p.naming_album_folder || p.naming_media_folder || p.naming_track_file);
}

function EditProfile({ profile, onClose, onSaved }: { profile: TaggerProfile; onClose: () => void; onSaved: () => void }) {
  const toast = useToast();
  const [p, setP] = useState<TaggerProfile>(profile);
  const [busy, setBusy] = useState(false);
  const set = (patch: Partial<TaggerProfile>) => setP((cur) => ({ ...cur, ...patch }));
  const naming = useFetch<NamingInfo>(() => api.get("/naming"));
  // The templates as edited. Null until the defaults arrive, because an empty stored
  // template *means* its default, and the editor has to open on what it means.
  const [templates, setTemplates] = useState<NamingTemplates | null>(null);
  useEffect(() => {
    if (!naming.data) return;
    const d = naming.data.defaults;
    setTemplates({
      artist_folder: profile.naming_artist_folder || d.artist_folder,
      album_folder: profile.naming_album_folder || d.album_folder,
      media_folder: profile.naming_media_folder || d.media_folder,
      track_file: profile.naming_track_file || d.track_file,
    });
  }, [naming.data, profile]);
  const settled = useDebounced(templates);
  const preview = useFetch<NamingPreview | null>(
    () => (settled ? api.post<NamingPreview>("/naming/preview", settled) : Promise.resolve(null)),
    [JSON.stringify(settled)],
  );
  const namingErrors = preview.data?.check.errors ?? {};
  const namingBlocked = Object.keys(namingErrors).length > 0;

  // A template equal to its default is stored empty, so the row keeps meaning "the
  // default" rather than freezing today's default into it.
  const stored = (field: NamingField) => {
    if (!templates || !naming.data) return undefined;
    const v = templates[field].trim();
    return v === naming.data.defaults[field] ? "" : v;
  };

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
        single_record_label: p.single_record_label,
        naming_artist_folder: stored("artist_folder"),
        naming_album_folder: stored("album_folder"),
        naming_media_folder: stored("media_folder"),
        naming_track_file: stored("track_file"),
      });
      onSaved();
    } catch (e) {
      toast("err", errMsg(e));
      setBusy(false);
    }
  };

  return (
    <Modal title={`Edit ${profile.name}`} onClose={onClose} wide>
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
          <Check
            label="Write only one record label"
            checked={p.single_record_label}
            onChange={(v) => set({ single_record_label: v })}
          />
          <p className="muted" style={{ margin: "4px 0 0", fontSize: 12 }}>
            Plex shows one record label and runs several together as one it cannot group by
            (<span className="mono">APESHIT;EMPIRE;Mass Appeal</span>). On, files get only the first label
            MusicBrainz lists — the order its editors entered them in, which usually leads with the label the
            release is known by. Applies to FLAC and MP3; catalogue numbers are kept in full.
          </p>
        </div>
        {naming.err && <ErrorNote message={naming.err} />}
        {naming.data && templates && (
          <NamingSection
            info={naming.data}
            templates={templates}
            onChange={(field, value) => setTemplates({ ...templates, [field]: value })}
            preview={preview.data}
          />
        )}
        <div className="modal-actions">
          <button type="button" className="btn btn-ghost btn-sm" onClick={onClose}>Cancel</button>
          <button
            className="btn btn-primary btn-sm"
            disabled={busy || !p.name || namingBlocked}
            title={namingBlocked ? "A naming template has a problem — see the message under it" : undefined}
          >
            {busy ? "Saving…" : "Save changes"}
          </button>
        </div>
      </form>
    </Modal>
  );
}

const NAMING_FIELDS: { field: NamingField; label: string; help: string }[] = [
  { field: "artist_folder", label: "Artist folder", help: "One per artist; holds every album." },
  { field: "album_folder", label: "Album folder", help: "One per album, inside the artist folder." },
  { field: "media_folder", label: "Disc folder", help: "Only when an album has more than one disc of audio." },
  { field: "track_file", label: "Track file", help: "The file's own extension is kept." },
];

/**
 * The naming half of a profile: one template per level of the library layout, so a
 * template cannot change how many folders deep a file sits. The checks and the sample
 * paths come from the same Go renderer a save validates with, so the editor shows
 * exactly what a save would accept.
 */
function NamingSection({
  info,
  templates,
  onChange,
  preview,
}: {
  info: NamingInfo;
  templates: NamingTemplates;
  onChange: (field: NamingField, value: string) => void;
  preview: NamingPreview | null;
}) {
  const [showTokens, setShowTokens] = useState(false);
  const errors = preview?.check.errors ?? {};
  const warnings = preview?.check.warnings ?? {};

  return (
    <div className="stack" style={{ marginTop: 8 }}>
      <div>
        <div className="eyebrow">Naming</div>
        <p className="muted" style={{ margin: "4px 0 0", fontSize: 12, maxWidth: "68ch" }}>
          Where a file belongs under its library root. Token names follow Lidarr's, so a Lidarr naming
          format can be pasted in. Saving moves nothing.
        </p>
      </div>

      {NAMING_FIELDS.map(({ field, label, help }) => (
        <div className="field" key={field}>
          <label className="flabel" htmlFor={`naming-${field}`}>{label}</label>
          <input
            id={`naming-${field}`}
            className={`input mono${errors[field] ? " invalid" : ""}`}
            value={templates[field]}
            onChange={(e) => onChange(field, e.target.value)}
            placeholder={info.defaults[field]}
            spellCheck={false}
          />
          {errors[field] ? (
            <span className="help-err">{errors[field]}</span>
          ) : warnings[field] ? (
            <span className="help-warn">{warnings[field]}</span>
          ) : (
            <span className="dim" style={{ fontSize: 11 }}>{help}</span>
          )}
        </div>
      ))}

      <div className="field">
        <span className="flabel">Preview</span>
        <div className="diff">
          {(preview?.samples ?? []).map((s) => (
            <div className="diffrow" key={s.label}>
              <span className="diffkey">{s.label}</span>
              <div className="diffvals">
                {s.path ? <span className="diffv same">{s.path}</span> : <span className="diffv none">{s.error ?? "—"}</span>}
              </div>
            </div>
          ))}
        </div>
      </div>

      <div>
        <button type="button" className="btn btn-ghost btn-sm" aria-expanded={showTokens} onClick={() => setShowTokens(!showTokens)}>
          {showTokens ? "Hide tokens" : "Show tokens"}
        </button>
        {showTokens && (
          <div className="tablewrap" style={{ marginTop: 8 }}>
            <table className="data">
              <thead>
                <tr><th>Token</th><th>Value</th><th>From</th></tr>
              </thead>
              <tbody>
                {info.tokens.map((t) => (
                  <tr key={t.name}>
                    <td className="mono" style={{ fontSize: 12, whiteSpace: "nowrap" }}>{t.numeric ? `{${t.name}:00}` : `{${t.name}}`}</td>
                    <td style={{ maxWidth: "48ch" }}>{t.description}</td>
                    <td className="dim" style={{ whiteSpace: "nowrap" }}>{NAMING_FIELDS.find((f) => f.field === t.field)?.label}</td>
                  </tr>
                ))}
              </tbody>
            </table>
            <p className="dim" style={{ margin: "6px 0 0", fontSize: 11, maxWidth: "68ch" }}>
              A token can be used in its own folder and anything below it. Written in lower case it
              renders lower case; <span className="mono">{"{Artist.Name}"}</span> joins words with dots;{" "}
              <span className="mono">{"{ (Album Disambiguation)}"}</span> writes the brackets only when there is a value.
            </p>
          </div>
        )}
      </div>
    </div>
  );
}
