import { FormEvent, ReactNode, useState } from "react";
import { api, errMsg } from "../api";
import { useFetch } from "../hooks";
import { DataSource, DATA_SOURCE_LABEL, dataSourceCategory } from "../types";
import { EmptyState, ErrorNote, Modal, Pill } from "../components/ui";
import { ArtworkRefresh } from "../components/ArtworkRefresh";
import { useToast } from "../toast";

/** The one provider per non-metadata role, in the order the panels list them. */
const FINGERPRINT_TYPES = ["acoustid"];
const ARTWORK_TYPES = ["coverartarchive", "fanart"];

/** What each singleton provider is for — shown whether or not it is set up yet. */
const BLURB: Record<string, string> = {
  acoustid:
    "Identifies a file from its audio when you attach it by hand. Also needs fpcalc on the server and the per-library opt-in; it only ever suggests a match for you to confirm.",
  coverartarchive: "Album covers, matched by MusicBrainz ID. No key needed.",
  fanart:
    "Artist portraits and backdrops for the collection pages. MusicBrainz has no artist images, so this is the only source for them. Needs a personal key.",
};

export default function DataSources() {
  const { data, err, loading, reload } = useFetch<DataSource[]>(() => api.get("/data-sources"));
  const [editing, setEditing] = useState<DataSource | null>(null);
  const [addingType, setAddingType] = useState<string | null>(null);

  const toast = useToast();
  const sources = data ?? [];
  const metadata = sources.filter((d) => dataSourceCategory(d.type) === "metadata");
  const ofType = (type: string) => sources.filter((d) => d.type === type);

  const close = () => { setAddingType(null); setEditing(null); };
  const saved = () => { close(); reload(); };

  // Creating duplicates is refused now, but installs that already have them must not
  // have the extra rows quietly disappear behind a panel that shows only the first.
  const removeDuplicate = async (d: DataSource) => {
    if (!confirm(`Remove the unused duplicate "${d.name}"?`)) return;
    try {
      await api.del(`/data-sources/${d.id}`);
      toast("ok", `Removed ${d.name}`);
      reload();
    } catch (e) {
      toast("err", errMsg(e));
    }
  };

  return (
    <div className="stack">
      <div className="page-head">
        <h1>Data sources</h1>
        <button className="btn btn-primary btn-sm" onClick={() => setAddingType("musicbrainz")}>
          Add metadata source
        </button>
      </div>
      <p className="muted" style={{ margin: 0, maxWidth: "68ch" }}>
        Where release metadata comes from. Every manager reads MusicBrainz to fetch the full release
        for tagging, and a library can be pointed at a specific one. Fingerprinting and artwork are
        separate services with their own panels below — they never supply tags.
      </p>

      {err && <ErrorNote message={err} />}

      <section className="stack" style={{ gap: 8 }}>
        <div className="eyebrow">Metadata sources</div>
        {!err && !loading && metadata.length === 0 && (
          <EmptyState
            icon="⛃"
            message="No metadata source configured — MusicBrainz is needed for tagging."
            action={
              <button className="btn btn-primary btn-sm" onClick={() => setAddingType("musicbrainz")}>
                Add metadata source
              </button>
            }
          />
        )}
        {metadata.length > 0 && (
          <div className="tablewrap">
            <table className="data">
              <thead>
                <tr>
                  <th>Name</th><th>Type</th><th>Base URL</th><th>Rate limit</th><th>State</th>
                  <th style={{ textAlign: "right" }}>Actions</th>
                </tr>
              </thead>
              <tbody>
                {metadata.map((d) => (
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
        <span className="dim" style={{ fontSize: 11 }}>
          The request rate applies to MusicBrainz calls across the whole app. Raising it above 1/s is
          only appropriate against a local mirror.
        </span>
      </section>

      <ProviderPanel
        title="File identification"
        note="Fingerprinting recognises a recording from the audio itself. One service, so there is one row."
        types={FINGERPRINT_TYPES}
        ofType={ofType}
        loading={loading}
        onConfigure={setEditing}
        onSetUp={setAddingType}
        onRemoveDuplicate={removeDuplicate}
      />

      <ProviderPanel
        title="Artwork"
        note="Covers and artist images for the browsing pages. Nothing in the tagging pipeline depends on these."
        types={ARTWORK_TYPES}
        ofType={ofType}
        loading={loading}
        onConfigure={setEditing}
        onSetUp={setAddingType}
        onRemoveDuplicate={removeDuplicate}
        // The one action these two providers are for, so it lives in their card
        // rather than as a section beneath it.
        footer={<ArtworkRefresh />}
      />

      {(addingType || editing) && (
        <DataSourceForm
          initial={editing ?? undefined}
          fixedType={addingType ?? undefined}
          onClose={close}
          onSaved={saved}
        />
      )}
    </div>
  );
}

/**
 * One panel per non-metadata role. Each provider is a singleton, so a row is either
 * configured (state + Configure) or absent (Set up) — there is no list to grow, which
 * is exactly why these do not belong in the metadata table.
 *
 * `footer` is for an action that operates on these providers. It sits *inside* the
 * card, below a divider, because an action on the panel's contents is not a peer
 * section of it — given its own eyebrow and its own button row it reads as a second
 * page heading, which is what the artwork refresh looked like before it moved in here.
 */
function ProviderPanel({
  title,
  note,
  types,
  ofType,
  loading,
  onConfigure,
  onSetUp,
  onRemoveDuplicate,
  footer,
}: {
  title: string;
  note: string;
  types: string[];
  ofType: (type: string) => DataSource[];
  loading: boolean;
  onConfigure: (d: DataSource) => void;
  onSetUp: (type: string) => void;
  onRemoveDuplicate: (d: DataSource) => void;
  footer?: ReactNode;
}) {
  return (
    <section className="stack" style={{ gap: 8 }}>
      <div className="eyebrow">{title}</div>
      <div className="card stack" style={{ gap: 14 }}>
        {types.map((type) => {
          // The first row is the one the app actually uses; anything after it predates
          // the duplicate check and is dead weight.
          const [primary, ...duplicates] = ofType(type);
          return (
            <div key={type} className="stack" style={{ gap: 4 }}>
              <div className="row" style={{ gap: 10, alignItems: "center" }}>
                <span style={{ color: "var(--text)", fontSize: 13, fontWeight: 500 }}>
                  {DATA_SOURCE_LABEL[type] ?? type}
                </span>
                {loading ? (
                  <span className="dim" style={{ fontSize: 11 }}>…</span>
                ) : primary ? (
                  primary.enabled ? <Pill kind="ok">Enabled</Pill> : <Pill kind="off">Disabled</Pill>
                ) : (
                  <Pill kind="off">Not set up</Pill>
                )}
                <div className="row" style={{ marginLeft: "auto" }}>
                  {primary ? (
                    <button className="btn btn-secondary btn-sm" onClick={() => onConfigure(primary)}>
                      Configure
                    </button>
                  ) : (
                    <button className="btn btn-ghost btn-sm" onClick={() => onSetUp(type)}>Set up</button>
                  )}
                </div>
              </div>
              <span className="dim" style={{ fontSize: 11, maxWidth: "68ch" }}>{BLURB[type]}</span>

              {duplicates.map((d) => (
                <div key={d.id} className="row" style={{ gap: 10, alignItems: "center", paddingLeft: 12 }}>
                  <span className="dim" style={{ fontSize: 12 }}>{d.name}</span>
                  <Pill kind="off">Unused duplicate</Pill>
                  <button
                    className="btn btn-ghost btn-sm"
                    style={{ marginLeft: "auto", color: "var(--danger-text)" }}
                    onClick={() => onRemoveDuplicate(d)}
                  >
                    Remove
                  </button>
                </div>
              ))}
            </div>
          );
        })}
        {footer && (
          <>
            <div className="panel-rule" />
            {footer}
          </>
        )}
      </div>
      <span className="dim" style={{ fontSize: 11 }}>{note}</span>
    </section>
  );
}

/** Which providers authenticate. The Cover Art Archive and MusicBrainz do not. */
function needsKey(type: string): boolean {
  return type === "acoustid" || type === "fanart";
}

function DataSourceForm({
  initial,
  fixedType,
  onClose,
  onSaved,
}: {
  initial?: DataSource;
  /** Creating this exact provider. The type is not a choice — the panel picked it. */
  fixedType?: string;
  onClose: () => void;
  onSaved: () => void;
}) {
  const toast = useToast();
  const editing = !!initial;
  const type = initial?.type ?? fixedType ?? "musicbrainz";
  const [name, setName] = useState(initial?.name ?? DATA_SOURCE_LABEL[type] ?? "");
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

  const label = DATA_SOURCE_LABEL[type] ?? type;
  return (
    <Modal title={editing ? `Edit ${initial!.name}` : `Set up ${label}`} onClose={onClose}>
      <form onSubmit={submit} className="stack">
        {/* The type is never editable: it is fixed on create by whichever section you
            came from, and changing it afterwards would repurpose a configured row. */}
        <div className="field">
          <label className="flabel">Provider</label>
          <input className="input" value={label} disabled readOnly />
        </div>

        <div className="field">
          <label className="flabel">Name</label>
          <input className="input" value={name} onChange={(e) => setName(e.target.value)} autoFocus />
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
            {busy ? "Saving…" : editing ? "Save changes" : `Set up ${label}`}
          </button>
        </div>
      </form>
    </Modal>
  );
}
