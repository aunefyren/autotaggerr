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
        {children}
      </div>
    </div>
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
