import { useEffect, useState } from "react";
import { api, errMsg } from "../../api";
import { useDebounced } from "../../hooks";
import { CronSchedule, SettingsField } from "../../types";
import { Pill } from "../../components/ui";
import { useToast } from "../../toast";

export type SettingValue = string | number | boolean;

/**
 * One setting: what it is on the left, the control on the right. The tier badge sits
 * with the label because it qualifies the setting itself, not the value — "this one
 * needs a restart" is true whether or not you are editing it.
 *
 * `switchedOff` names the switch that keeps a schedule's job from running, when it is
 * off: the schedule is still editable, but it should not read as if it ran.
 */
export function SettingRow({
  field,
  edited,
  dirty,
  onChange,
  switchedOff,
}: {
  field: SettingsField;
  edited: SettingValue | undefined;
  dirty: boolean;
  onChange: (value: SettingValue) => void;
  switchedOff?: string;
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
        {field.type === "cron" && field.editable ? (
          <CronControl field={field} value={String(value ?? "")} dirty={dirty} onChange={onChange} switchedOff={switchedOff} />
        ) : (
          <Control field={field} value={value} onChange={onChange} />
        )}
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
  value: SettingValue | string[] | undefined;
  onChange: (value: SettingValue) => void;
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
        <input id={id} type="checkbox" checked={Boolean(value)} onChange={(e) => onChange(e.target.checked)} />
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

  return (
    <input
      id={id}
      className="input"
      value={String(value ?? "")}
      placeholder={field.placeholder}
      onChange={(e) => onChange(e.target.value)}
    />
  );
}

/**
 * A schedule, with what it means under it. The stored value's reading comes with the
 * page; an edited one is read by the server as it is typed (debounced), so "0 0 18 * * 7"
 * says *Sundays at 18:00* before anyone saves it — and a typo says what is wrong while
 * it is still being typed, not after a save is refused.
 */
function CronControl({
  field,
  value,
  dirty,
  onChange,
  switchedOff,
}: {
  field: SettingsField;
  value: string;
  dirty: boolean;
  onChange: (value: SettingValue) => void;
  switchedOff?: string;
}) {
  const schedule = useSchedule(field, value, dirty);

  return (
    <>
      <input
        id={`set-${field.key}`}
        className={`input mono${schedule?.error ? " invalid" : ""}`}
        value={value}
        placeholder={field.placeholder}
        spellCheck={false}
        onChange={(e) => onChange(e.target.value)}
      />
      {schedule?.error ? (
        <span className="help-err">{schedule.error}</span>
      ) : schedule ? (
        <div className="cron-reading">
          {schedule.summary && <span className="cron-summary">{schedule.summary}</span>}
          {schedule.next.length > 0 && <span className="cron-next">Next {schedule.next.join(" · ")}</span>}
        </div>
      ) : null}
      {switchedOff && <span className="help-warn">Not running — {switchedOff} is off.</span>}
    </>
  );
}

/**
 * The reading of a schedule: the page's own for the stored value, the server's preview
 * for an edited one. Exported for the day strip, which draws the edited schedule too.
 */
export function useSchedule(field: SettingsField, value: string, dirty: boolean): CronSchedule | undefined {
  const settled = useDebounced(value, 300);
  const [preview, setPreview] = useState<{ expression: string; schedule: CronSchedule } | null>(null);

  useEffect(() => {
    if (!dirty) return;
    let live = true;
    api
      .post<CronSchedule>("/settings/cron/preview", { expression: settled })
      .then((schedule) => live && setPreview({ expression: settled, schedule }))
      .catch(() => live && setPreview(null));
    return () => {
      live = false;
    };
  }, [settled, dirty]);

  if (!dirty) return field.schedule;
  // Only the preview of what is in the box now: a stale one would describe the value
  // before the last keystroke.
  return preview?.expression === value ? preview.schedule : undefined;
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
    return <div className="setting-readonly mono">{field.secret_set ? "•••••••• stored" : "not set"}</div>;
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

function formatReadOnly(value: SettingsField["value"]): string {
  if (value === undefined || value === null || value === "") return "—";
  if (Array.isArray(value)) return value.join(", ");
  return String(value);
}
