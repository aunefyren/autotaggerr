import { ReactNode } from "react";

const STATUS: Record<string, [string, string]> = {
  ok: ["pill-ok", "Tagged"],
  error: ["pill-err", "Error"],
  unmatched: ["pill-off", "Unmatched"],
  changed: ["pill-chg", "Changed"],
  scanning: ["pill-scan", "Scanning"],
};

export function StatusPill({ status }: { status: string }) {
  const [cls, label] = STATUS[status] ?? ["pill-off", status || "—"];
  return (
    <span className={`pill ${cls}`}>
      <span className="dot" />
      {label}
    </span>
  );
}

export function Pill({ kind, children }: { kind: string; children: ReactNode }) {
  return (
    <span className={`pill pill-${kind}`}>
      <span className="dot" />
      {children}
    </span>
  );
}

// IdChip shows a middle-truncated identifier; click copies the full value.
export function IdChip({ value }: { value: string }) {
  if (!value) return <span className="dim mono" style={{ fontSize: 11 }}>—</span>;
  const short = value.length > 16 ? `${value.slice(0, 8)}…${value.slice(-6)}` : value;
  return (
    <span className="idchip" title={`${value} — click to copy`} onClick={() => navigator.clipboard?.writeText(value)}>
      {short}
    </span>
  );
}

export function Modal({ title, children, onClose, wide }: { title: string; children: ReactNode; onClose: () => void; wide?: boolean }) {
  return (
    <div className="backdrop" onClick={onClose}>
      <div className={`modal${wide ? " wide" : ""}`} role="dialog" aria-modal="true" onClick={(e) => e.stopPropagation()}>
        <h2>{title}</h2>
        {/* The body is a scroller so the modal can be bounded by the viewport without
            the title scrolling away with it — see `.modal` in app.css. */}
        <div className="modal-body">{children}</div>
      </div>
    </div>
  );
}

/**
 * A confirm step for an action whose cost is not visible from its button.
 *
 * The `body` is the whole point and is required: a dialog that only asks "are you
 * sure?" adds a click without adding information, and people learn to dismiss it
 * without reading. What goes in it is what the button cannot say — how long the
 * work takes, what it overwrites, what it leaves alone.
 *
 * `confirmLabel` restates the verb rather than saying "OK", so the last thing read
 * before committing names the thing being committed to.
 */
export function ConfirmDialog({
  title,
  body,
  confirmLabel,
  danger,
  busy,
  onConfirm,
  onCancel,
}: {
  title: string;
  body: ReactNode;
  confirmLabel: string;
  /** Use for anything that overwrites or discards. Not for merely expensive work. */
  danger?: boolean;
  busy?: boolean;
  onConfirm: () => void;
  onCancel: () => void;
}) {
  return (
    <Modal title={title} onClose={onCancel}>
      <div className="stack" style={{ fontSize: 12, color: "var(--text-dim)", gap: 8 }}>
        {body}
      </div>
      <div className="modal-actions">
        {/* Cancel first and Cancel is the plain one: the escape from a dialog you
            opened by accident should not be the button styled to be pressed. */}
        <button className="btn btn-secondary btn-sm" onClick={onCancel} disabled={busy}>
          Cancel
        </button>
        <button
          className={`btn btn-sm ${danger ? "btn-danger" : "btn-primary"}`}
          onClick={onConfirm}
          disabled={busy}
        >
          {busy ? "Working…" : confirmLabel}
        </button>
      </div>
    </Modal>
  );
}

/**
 * One blank shape standing in for a value that has not arrived yet.
 *
 * Drawn the way this app already draws an empty slot — `--surface-2` with a hairline,
 * exactly like `.coverage-cell.none` — rather than as a shimmering grey block. A
 * placeholder is a slot with nothing in it, and the app has a way of saying that.
 *
 * Sizes are the caller's, because the point of a placeholder is that the real thing
 * lands in the same place: give it the geometry of the element it replaces.
 */
export function Skeleton({ w, h = 10, pill }: { w: number | string; h?: number; pill?: boolean }) {
  return (
    <span
      className={`skel${pill ? " skel-pill" : ""}`}
      style={{ width: w, height: h }}
      // Decoration: the shapes say nothing an assistive reader wants, and the
      // surface they are on carries aria-busy plus a status line instead.
      aria-hidden="true"
    />
  );
}

export function EmptyState({ icon, message, action }: { icon: string; message: string; action?: ReactNode }) {
  return (
    <div className="empty">
      <div className="ei">{icon}</div>
      <div>{message}</div>
      {action}
    </div>
  );
}

export function ErrorNote({ message }: { message: string }) {
  return (
    <div className="card" style={{ borderColor: "var(--danger)", color: "var(--danger-text)" }}>
      {message}
    </div>
  );
}
