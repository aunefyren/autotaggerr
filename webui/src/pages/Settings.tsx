import { ReactNode, useMemo, useState } from "react";
import { api, errMsg } from "../api";
import { useFetch } from "../hooks";
import { SettingsField, SettingsSaveResult, SettingsView } from "../types";
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

  // A test instance redirects every outgoing message to the test recipient, so the
  // Email section has to say so where the sending happens — a redirect the admin only
  // discovers from the toast is a surprise, and on a test box it is *the* thing to know.
  const testEnvironment = useMemo(() => {
    const field = data?.sections.flatMap((s) => s.fields).find((f) => f.key === "autotaggerr_environment");
    return String(field?.value ?? "").toLowerCase() === "test";
  }, [data]);

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
          {section.id === "email" && (
            <EmailTest
              pendingEdits={dirtyKeys.some(
                (key) => key.startsWith("smtp_") || key === "autotaggerr_test_email",
              )}
              testEnvironment={testEnvironment}
            />
          )}
        </section>
      ))}

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
 * The Email section's action. Sending one message is the only way to find out whether
 * SMTP settings work, and until now there was nothing to find out with: the settings
 * shipped before any code sent mail.
 *
 * It sends through the *stored* configuration, so pending edits are called out rather
 * than quietly tested — typing a new host, pressing Test, and being told it works
 * because the old one does is the failure this line exists to prevent.
 */
function EmailTest({
  pendingEdits,
  testEnvironment,
}: {
  pendingEdits: boolean;
  testEnvironment: boolean;
}) {
  const [busy, setBusy] = useState(false);
  const toast = useToast();

  const sendTest = async () => {
    setBusy(true);
    try {
      const result = await api.post<{ sent_to: string }>("/settings/email/test");
      toast("ok", `Test message sent to ${result.sent_to}`);
    } catch (e) {
      // The SMTP server's own words: "535 authentication failed" is the answer the
      // admin came for, and a friendlier summary would throw it away.
      toast("err", errMsg(e));
    } finally {
      setBusy(false);
    }
  };

  return (
    <div className="stack" style={{ gap: 8 }}>
      <div className="row" style={{ gap: 10, alignItems: "center", flexWrap: "wrap" }}>
        <button className="btn btn-secondary btn-sm" onClick={sendTest} disabled={busy}>
          {busy ? "Sending…" : "Send test message"}
        </button>
        <span className="muted" style={{ fontSize: 12 }}>
          {pendingEdits
            ? "Unsaved changes above — the test uses the stored settings, so save first."
            : "Sends one message to the test recipient, using the stored settings."}
        </span>
      </div>
      {testEnvironment && (
        <p className="muted" style={{ margin: 0, fontSize: 12, maxWidth: "76ch" }}>
          This instance is in the <strong>test</strong> environment, so <em>every</em> message it
          sends goes to the test recipient — never to the address it was addressed to. Nothing here
          can override that.
        </p>
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
