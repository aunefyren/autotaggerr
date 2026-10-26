import { FormEvent, useState } from "react";
import { Link } from "react-router-dom";
import { api, errMsg } from "../api";
import { useFetch } from "../hooks";
import { BPMWrite, Library, Manager, TaggerProfile, TempoStatus } from "../types";
import { EmptyState, ErrorNote, Modal, Note, Pill } from "../components/ui";
import { RecorrelateDialog } from "../components/RecorrelateDialog";
import { TempoAnalysis } from "../components/TempoAnalysis";
import { useToast } from "../toast";
import { useSetup } from "../setup";

interface Options {
  managers: Manager[];
  profiles: TaggerProfile[];
}

export default function Libraries() {
  const toast = useToast();
  const libs = useFetch<Library[]>(() => api.get("/libraries"));
  const managers = useFetch<Manager[]>(() => api.get("/managers"));
  const profiles = useFetch<TaggerProfile[]>(() => api.get("/tagger-profiles"));

  const setup = useSetup();
  const [creating, setCreating] = useState(false);
  const [editing, setEditing] = useState<Library | null>(null);
  const [recorrelating, setRecorrelating] = useState<Library | null>(null);
  const [busy, setBusy] = useState(false);
  // Bumped whenever a library changes, so the tempo card re-reads how many libraries
  // opt in rather than contradicting the table above it.
  const [tempoVersion, setTempoVersion] = useState(0);
  const changed = () => { libs.reload(); setTempoVersion((v) => v + 1); setup.reload(); };

  const options: Options = {
    managers: managers.data ?? [],
    profiles: profiles.data ?? [],
  };
  const managerName = (id: string | null) => (id ? options.managers.find((m) => m.id === id)?.name : undefined);
  // Only once the managers have loaded: before that every assigned manager would read
  // as missing for a frame.
  const managerCell = (l: Library) => {
    if (!l.manager_id) {
      return (
        <span title="Files are identified only from MusicBrainz IDs already in their tags; the rest stay unmatched. Assign a manager under Edit.">
          <Pill kind="warn">No manager</Pill>
        </span>
      );
    }
    if (!managers.data) return null;
    const m = options.managers.find((x) => x.id === l.manager_id);
    if (!m) {
      return (
        <span title="The assigned manager has been deleted. Processing skips this library until it is assigned another.">
          <Pill kind="err">Manager missing</Pill>
        </span>
      );
    }
    if (!m.enabled) {
      return (
        <span title="The assigned manager is disabled. Processing skips this library until it is re-enabled or another is assigned.">
          {m.name} <Pill kind="err">Disabled</Pill>
        </span>
      );
    }
    return m.name;
  };

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
  // The widest of the three re-correlate scopes — every file in the library, and every
  // manager-governed pin in it. Confirmed rather than immediate for exactly that
  // reason; the artist and album forms exist so this is not the only way to repair one
  // album.
  const recorrelate = async () => {
    if (!recorrelating) return;
    setBusy(true);
    try {
      await api.post(`/libraries/${recorrelating.id}/recorrelate`);
      toast("info", `Re-correlate started for ${recorrelating.name} — see Activity`);
      setRecorrelating(null);
    } catch (e) {
      toast("err", errMsg(e));
    } finally {
      setBusy(false);
    }
  };

  const toggle = async (l: Library) => {
    try {
      await api.put(`/libraries/${l.id}`, { enabled: !l.enabled });
      changed();
    } catch (e) {
      toast("err", errMsg(e));
    }
  };
  const remove = async (l: Library) => {
    if (!confirm(`Remove library "${l.name}"? Its files are not touched.`)) return;
    try {
      await api.del(`/libraries/${l.id}`);
      toast("ok", `Removed ${l.name}`);
      changed();
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

      {/* Advice before the choice rather than a warning after it: a library picks its
          manager, so the manager comes first. Adding one without is still allowed. */}
      {managers.data && managers.data.length === 0 && (
        <Note kind="warn" title="Add a manager first">
          A library's manager decides which MusicBrainz release each of its files is. <Link to="/managers">Add one
          on Managers</Link> — Lidarr, or Autotaggerr's own — then come back. A library with no manager identifies
          files only from IDs already in their tags.
        </Note>
      )}
      {setup.next?.key === "process" && libs.data && libs.data.length > 0 && (
        <Note kind="ok" title="Process it">
          Press <strong>Process</strong> on a library to identify and tag its files. The first run reads every file, so
          a large library takes a while; follow it on <Link to="/activity">Activity</Link>.
        </Note>
      )}

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
                <th>Last processed</th>
                <th style={{ textAlign: "right" }}>Actions</th>
              </tr>
            </thead>
            <tbody>
              {libs.data.map((l) => (
                <tr key={l.id}>
                  <td style={{ color: "var(--text)" }}>{l.name}</td>
                  <td><span className="path">{l.path}</span></td>
                  <td>{managerCell(l)}</td>
                  <td>
                    {/* The opt-in is a fact about the library, so it is stated here rather
                        than only inside the edit form — "which of my libraries are
                        analysed?" should not take a modal per row to answer. */}
                    <div className="stack" style={{ gap: 2 }}>
                      {l.enabled ? <Pill kind="ok">Enabled</Pill> : <Pill kind="off">Disabled</Pill>}
                      {l.analyse_tempo && (
                        <span className="dim" style={{ fontSize: 11 }}>
                          Tempo analysed{l.bpm_write ? ` · BPM ${BPM_WRITE_SHORT[l.bpm_write]}` : " · BPM not written"}
                        </span>
                      )}
                    </div>
                  </td>
                  <td className="mono dim" style={{ fontSize: 11 }}>
                    {l.last_scan ? new Date(l.last_scan).toLocaleString() : "never"}
                  </td>
                  <td>
                    <div className="row" style={{ justifyContent: "flex-end" }}>
                      <button
                        className="btn btn-secondary btn-sm"
                        onClick={action(l, "process", "Processing started")}
                        title="Walk this library, resolve metadata and write tags — the full pipeline. This is what finds files added, moved or changed on disk."
                      >
                        Process
                      </button>
                      <button
                        className="btn btn-ghost btn-sm"
                        onClick={action(l, "refresh", "Metadata refresh started")}
                        title="Re-read MusicBrainz for everything this library's files point at, ignoring the cache. Reads only: no files are written. Anything that changed is reported, and Tag files (or the next Process) applies it."
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
                      <button
                        className="btn btn-ghost btn-sm"
                        style={{ color: "var(--danger-text)" }}
                        onClick={() => setRecorrelating(l)}
                        title="Ask the manager what every file in this library is, ignoring the cached answers and the unchanged-file skip, and rewrite the tags to match. The repair for a re-pointed Lidarr. Writes tags, and discards every manual pin the manager governs — the artist and album pages carry narrower forms of this."
                      >
                        Re-correlate
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

      {libs.data && libs.data.length > 0 && <TempoAnalysis version={tempoVersion} />}

      {recorrelating && (
        <RecorrelateDialog
          scope={recorrelating.name}
          manager={managerName(recorrelating.manager_id)}
          // Pins are only cleared under a Lidarr manager, so the warning follows the
          // library's actual manager rather than being printed on every library.
          discardsPins={
            options.managers.find((m) => m.id === recorrelating.manager_id)?.type === "lidarr"
          }
          busy={busy}
          onConfirm={recorrelate}
          onCancel={() => setRecorrelating(null)}
        />
      )}

      {creating && (
        <LibraryForm
          options={options}
          onClose={() => setCreating(false)}
          onSaved={() => { setCreating(false); changed(); toast("ok", "Library added"); }}
        />
      )}
      {editing && (
        <LibraryForm
          initial={editing}
          options={options}
          onClose={() => setEditing(null)}
          onSaved={() => { setEditing(null); changed(); toast("ok", "Library saved"); }}
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
  const [managerId, setManagerId] = useState(initial?.manager_id ?? "");
  const [taggerProfileId, setTaggerProfileId] = useState(initial?.tagger_profile_id ?? "");
  const [useAcoustID, setUseAcoustID] = useState(initial?.use_acoustid ?? false);
  const [analyseTempo, setAnalyseTempo] = useState(initial?.analyse_tempo ?? false);
  const [bpmWrite, setBPMWrite] = useState<BPMWrite>(initial?.bpm_write ?? "");
  const [bpmFloor, setBPMFloor] = useState(initial?.bpm_min_confidence ?? 0);
  const [busy, setBusy] = useState(false);
  // Only to say so when the server cannot act on the switch. The switch itself stays
  // live: a library can opt in before aubio is installed, and is picked up once it is.
  const tempo = useFetch<TempoStatus>(() => api.get("/tempo/status"));

  const submit = async (e: FormEvent) => {
    e.preventDefault();
    setBusy(true);
    try {
      const body: Record<string, unknown> = {
        name, path, use_acoustid: useAcoustID, analyse_tempo: analyseTempo,
        bpm_write: bpmWrite, bpm_min_confidence: bpmFloor,
      };
      // Null, not absent: "No manager" and "Default" are choices, and absent would keep
      // the old one — an edit back to Default used to be silently ignored.
      body.manager_id = managerId || null;
      body.tagger_profile_id = taggerProfileId || null;

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
            <option value="">No manager</option>
            {options.managers.map((m) => (
              <option key={m.id} value={m.id}>{m.name} ({m.type}){m.enabled ? "" : " — disabled"}</option>
            ))}
          </select>
          <span className="dim" style={{ fontSize: 11 }}>Decides which MusicBrainz release each file maps to.</span>
        </div>
        {/* Allowed, because embedded tags are a legitimate source — a library tagged
            with Picard needs nothing else. But it is the setting a fresh install lands
            on without choosing it, and processing then matches little and says nothing,
            so the cost is stated where the choice is made. */}
        {!managerId && (
          <Note kind="warn" title="No manager">
            Files are identified only from MusicBrainz IDs already in their tags, and the rest stay unmatched.
            Lidarr does not write those IDs into MP3s.{" "}
            {options.managers.length === 0 ? "No manager exists yet — add one under Managers." : "Choose one above to have it decide."}
          </Note>
        )}

        {/* No metadata source or schedule here. Both were once fields on the library and
            were stored without ever being read: MusicBrainz is one process-wide client
            (Data sources), and libraries are processed on the one schedule under Settings.
            A control that changes nothing reads as a setting that does not work. */}
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

        <label className="row" style={{ gap: 8, cursor: "pointer" }}>
          <input type="checkbox" checked={analyseTempo} onChange={(e) => setAnalyseTempo(e.target.checked)} />
          <span style={{ fontSize: 12 }}>Analyse tempo (BPM)</span>
        </label>
        <span className="dim" style={{ fontSize: 11, marginTop: -6 }}>
          Measures each file's BPM from the audio on the tempo schedule and stores it.
          {tempo.data && !tempo.data.detector_available && (
            <span style={{ color: "var(--warning-text)" }}>
              {" "}aubiotrack is not installed on the server, so nothing is analysed until it is.
            </span>
          )}
        </span>

        {/* Both halves of the feature in one place. Analysing and writing stay separate
            choices — a stored tempo is useful without touching a file — but they used to
            live on two pages, and a library analysed under a profile that wrote nothing
            looked like the feature not working. Disabled rather than hidden while analysis
            is off, so the dependency is visible. */}
        <div className="row" style={{ gap: 12, alignItems: "flex-start", marginTop: -4 }}>
          <div className="field" style={{ flex: 2 }}>
            <label className="flabel">Write BPM</label>
            <select
              className="select"
              value={bpmWrite}
              disabled={!analyseTempo}
              title={analyseTempo ? undefined : "Turn on tempo analysis first."}
              onChange={(e) => setBPMWrite(e.target.value as BPMWrite)}
            >
              <option value="">Don't write — store only</option>
              <option value="fill_empty">Only where the file has none</option>
              <option value="overwrite">Always, replacing what is there</option>
            </select>
          </div>
          <div className="field" style={{ flex: 1 }}>
            <label className="flabel">Minimum confidence</label>
            <input
              className="input"
              type="number"
              min={0.05}
              max={1}
              step={0.05}
              disabled={!analyseTempo || !bpmWrite}
              title={!bpmWrite ? "Only applies when BPM is written." : undefined}
              value={bpmFloor > 0 ? bpmFloor : ""}
              onChange={(e) => setBPMFloor(e.target.value === "" ? 0 : Number(e.target.value))}
              placeholder={DEFAULT_BPM_MIN_CONFIDENCE.toFixed(2)}
            />
          </div>
        </div>
        <span className="dim" style={{ fontSize: 11, marginTop: -6 }}>
          Written as <span className="mono">BPM</span> in FLAC and <span className="mono">TBPM</span> in MP3,
          rounded to a whole number, by the next Process or Tag files. Confidence (0–1) is how steady the
          beat grid was: steady dance and pop measures 0.85 and up, clearly wrong readings near 0.1. It
          cannot tell half or double time from the real tempo. A BPM another tool wrote is never cleared.
        </span>

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

/** The server reads 0 or less as its default floor. */
const DEFAULT_BPM_MIN_CONFIDENCE = 0.5;

const BPM_WRITE_SHORT: Record<BPMWrite, string> = {
  "": "not written",
  fill_empty: "written where missing",
  overwrite: "always written",
};
