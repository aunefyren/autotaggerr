import { ReactNode, useMemo, useState } from "react";
import { Link } from "react-router-dom";
import { api, errMsg } from "../api";
import { useFetch } from "../hooks";
import { ManagedElsewhere, SettingsField, SettingsSaveResult, SettingsView } from "../types";
import { ErrorNote, Pill } from "../components/ui";
import { useToast } from "../toast";

/** A pending edit. Only touched fields are held, so a save sends only what changed. */
type Edits = Record<string, string | number | boolean>;

export default function Settings() {
  const { data, err, loading, reload } = useFetch<SettingsView>(() => api.get("/settings"));
  const [edits, setEdits] = useState<Edits>({});
  const [busy, setBusy] = useState(false);
  const [lastSave, setLastSave] = useState<SettingsSaveResult | null>(null);
  const toast = useToast();

  const dirtyKeys = useMemo(() => Object.keys(edits), [edits]);
  const dirty = dirtyKeys.length > 0;

  const set = (key: string, value: string | number | boolean) =>
    setEdits((cur) => ({ ...cur, [key]: value }));

  const discard = () => {
    setEdits({});
    setLastSave(null);
  };

  const save = async () => {
    setBusy(true);
    try {
      const result = await api.put<SettingsSaveResult>("/settings", { values: edits });
      setEdits({});
      setLastSave(result);
      reload();
      toast(
        "ok",
        result.changed.length === 0
          ? "Nothing to save — those values were already stored"
          : `Saved ${result.changed.length} setting${result.changed.length === 1 ? "" : "s"}`,
      );
    } catch (e) {
      // The server names the field and what it expected, so show that verbatim.
      toast("err", errMsg(e));
    } finally {
      setBusy(false);
    }
  };

  return (
    <div className="stack">
      <div className="page-head">
        <h1>Settings</h1>
      </div>
      <p className="muted" style={{ margin: 0, maxWidth: "72ch" }}>
        Everything Autotaggerr can be told at startup — a flag, an environment variable, a key in
        config.json. Saving writes the file; settings marked <em>on restart</em> are read the next time
        the service starts.
      </p>

      {err && <ErrorNote message={err} />}
      {loading && !data && <div className="muted">Loading…</div>}

      {lastSave && lastSave.applied.length > 0 && (
        <Note kind="ok" title="Applied now">
          {lastSave.applied.join(" · ")}
        </Note>
      )}
      {lastSave && lastSave.restart_required.length > 0 && (
        <Note kind="warn" title="Waiting for a restart">
          {lastSave.restart_required.join(", ")} — saved to config.json, read at the next start.
        </Note>
      )}

      {data?.sections.map((section) => (
        <section key={section.id} className="card stack" style={{ gap: 14 }}>
          <div>
            <h2 className="section-title">{section.title}</h2>
            {section.description && (
              <p className="muted" style={{ margin: "4px 0 0", fontSize: 12, maxWidth: "76ch" }}>
                {section.description}
              </p>
            )}
          </div>
          {section.fields.map((field) => (
            <SettingRow
              key={field.key}
              field={field}
              edited={edits[field.key]}
              dirty={field.key in edits}
              onChange={(v) => set(field.key, v)}
            />
          ))}
        </section>
      ))}

      {data && data.managed.length > 0 && <ManagedSection managed={data.managed} />}

      {dirty && (
        <div className="savebar" role="status">
          <span className="savebar-count">
            {dirtyKeys.length} unsaved change{dirtyKeys.length === 1 ? "" : "s"}
          </span>
          <button className="btn btn-ghost btn-sm" onClick={discard} disabled={busy}>
            Discard
          </button>
          <button className="btn btn-primary btn-sm" onClick={save} disabled={busy}>
            {busy ? "Saving…" : "Save changes"}
          </button>
        </div>
      )}
    </div>
  );
}

/**
 * One setting: what it is on the left, the control on the right. The tier badge sits
 * with the label because it qualifies the setting itself, not the value — "this one
 * needs a restart" is true whether or not you are editing it.
 */
function SettingRow({
  field,
  edited,
  dirty,
  onChange,
}: {
  field: SettingsField;
  edited: string | number | boolean | undefined;
  dirty: boolean;
  onChange: (value: string | number | boolean) => void;
}) {
  const value = dirty ? edited : field.value;

  return (
    <div className={`setting-row${dirty ? " is-dirty" : ""}`}>
      <div className="setting-label">
        <label className="setting-name" htmlFor={`set-${field.key}`}>
          {field.label}
        </label>
        <div className="setting-meta">
          <code className="setting-key">{field.key}</code>
          {field.tier === "restart" && <Pill kind="off">On restart</Pill>}
          {field.tier === "readonly" && <Pill kind="off">Read-only</Pill>}
        </div>
        {field.help && <p className="setting-help">{field.help}</p>}
      </div>
      <div className="setting-control">
        <Control field={field} value={value} onChange={onChange} />
      </div>
    </div>
  );
}

function Control({
  field,
  value,
  onChange,
}: {
  field: SettingsField;
  value: string | number | boolean | string[] | undefined;
  onChange: (value: string | number | boolean) => void;
}) {
  const id = `set-${field.key}`;

  if (field.type === "secret") return <SecretControl field={field} onChange={onChange} />;

  if (!field.editable) {
    // A read-only value is still data, so it is shown as data — verbatim, in mono —
    // rather than as a disabled input pretending to be editable.
    return <div className="setting-readonly mono">{formatReadOnly(field.value)}</div>;
  }

  if (field.type === "bool") {
    return (
      <label className="row" style={{ gap: 8, cursor: "pointer" }}>
        <input
          id={id}
          type="checkbox"
          checked={Boolean(value)}
          onChange={(e) => onChange(e.target.checked)}
        />
        <span className="muted" style={{ fontSize: 12 }}>
          {value ? "On" : "Off"}
        </span>
      </label>
    );
  }

  if (field.type === "select") {
    return (
      <select id={id} className="select" value={String(value ?? "")} onChange={(e) => onChange(e.target.value)}>
        {field.options?.map((option) => (
          <option key={option} value={option}>
            {option}
          </option>
        ))}
      </select>
    );
  }

  if (field.type === "int") {
    return (
      <input
        id={id}
        className="input mono"
        type="number"
        value={value === undefined || value === null ? "" : String(value)}
        onChange={(e) => onChange(e.target.value === "" ? 0 : Number(e.target.value))}
      />
    );
  }

  // cron expressions and paths are identifiers, so they are set in mono per the guide.
  const mono = field.type === "cron";
  return (
    <input
      id={id}
      className={`input${mono ? " mono" : ""}`}
      value={String(value ?? "")}
      placeholder={field.placeholder}
      onChange={(e) => onChange(e.target.value)}
    />
  );
}

/**
 * A secret is never part of the settings response, so this control has three states:
 * it says whether one is stored, it can fetch the stored value on request (a
 * deliberate, logged call), and it takes a new one. Typing replaces; clearing the box
 * after typing stores an empty value, which is how a secret is removed.
 */
function SecretControl({ field, onChange }: { field: SettingsField; onChange: (v: string) => void }) {
  const [draft, setDraft] = useState<string | null>(null);
  const [shown, setShown] = useState(false);
  const [busy, setBusy] = useState(false);
  const toast = useToast();

  const reveal = async () => {
    setBusy(true);
    try {
      const res = await api.get<{ value: string }>(`/settings/secrets/${field.key}`);
      setDraft(res.value);
      setShown(true);
    } catch (e) {
      toast("err", errMsg(e));
    } finally {
      setBusy(false);
    }
  };

  if (!field.editable) {
    return (
      <div className="row" style={{ gap: 8 }}>
        <div className="setting-readonly mono">{field.secret_set ? "•••••••• stored" : "not set"}</div>
      </div>
    );
  }

  return (
    <div className="stack" style={{ gap: 6 }}>
      <div className="row" style={{ gap: 6 }}>
        <input
          id={`set-${field.key}`}
          className="input mono"
          type={shown ? "text" : "password"}
          value={draft ?? ""}
          placeholder={field.secret_set ? "•••••••• stored — type to replace" : "not set"}
          autoComplete="new-password"
          onChange={(e) => {
            setDraft(e.target.value);
            onChange(e.target.value);
          }}
        />
        <button
          type="button"
          className="btn btn-secondary btn-sm"
          disabled={busy}
          title={shown ? "Hide the value" : "Show the stored value"}
          onClick={() => (shown ? setShown(false) : draft !== null ? setShown(true) : reveal())}
        >
          {shown ? "Hide" : "Show"}
        </button>
      </div>
      {draft === "" && field.secret_set && (
        <span className="setting-help" style={{ color: "var(--warning-text)" }}>
          Saving an empty box removes the stored value.
        </span>
      )}
    </div>
  );
}

/**
 * Keys that are still in config.json but are no longer read at runtime. Named rather
 * than hidden: the keys are in the file, and a page that omits them invites someone to
 * edit the file and wonder why nothing changed.
 */
function ManagedSection({ managed }: { managed: ManagedElsewhere[] }) {
  return (
    <section className="card stack" style={{ gap: 14 }}>
      <div>
        <h2 className="section-title">Managed elsewhere</h2>
        <p className="muted" style={{ margin: "4px 0 0", fontSize: 12, maxWidth: "76ch" }}>
          These keys may still be in config.json, but they only seeded the database on the first
          start. Editing the file changes nothing — change them where they live now.
        </p>
      </div>
      {managed.map((group) => (
        <div key={group.path} className="setting-row">
          <div className="setting-label">
            <span className="setting-name">
              <Link to={group.path}>{group.label}</Link>
            </span>
            <p className="setting-help">{group.note}</p>
          </div>
          <div className="setting-control">
            <div className="row" style={{ flexWrap: "wrap", gap: 4 }}>
              {group.keys.map((key) => (
                <code key={key} className="setting-key">
                  {key}
                </code>
              ))}
            </div>
          </div>
        </div>
      ))}
    </section>
  );
}

function Note({ kind, title, children }: { kind: string; title: string; children: ReactNode }) {
  return (
    <div className={`note note-${kind}`}>
      <strong>{title}</strong>
      <span>{children}</span>
    </div>
  );
}

function formatReadOnly(value: SettingsField["value"]): string {
  if (value === undefined || value === null || value === "") return "—";
  if (Array.isArray(value)) return value.join(", ");
  return String(value);
}
