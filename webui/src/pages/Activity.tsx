import { useEffect, useState } from "react";
import { api, errMsg } from "../api";
import { useFetch } from "../hooks";
import { Event, EventsPage, ScanStatus } from "../types";
import { EmptyState, ErrorNote, Modal, Pill } from "../components/ui";
import { useToast } from "../toast";

const TYPE_LABELS: Record<string, string> = {
  scan: "Scan",
  drift_sync: "Metadata sync",
  lidarr_sync: "Lidarr sync",
  plex_refresh: "Plex refresh",
};

function EventStatus({ status }: { status: string }) {
  if (status === "running") return <Pill kind="scan">Running</Pill>;
  if (status === "error") return <Pill kind="err">Failed</Pill>;
  return <Pill kind="ok">Done</Pill>;
}

export default function Activity() {
  const toast = useToast();
  const events = useFetch<EventsPage>(() => api.get("/events?limit=50"));
  const status = useFetch<ScanStatus>(() => api.get("/scan/status"));
  const [selected, setSelected] = useState<Event | null>(null);

  // Poll while a scan is running so the feed and banner stay live.
  useEffect(() => {
    if (!status.data?.running) return;
    const t = setInterval(() => {
      status.reload();
      events.reload();
    }, 3000);
    return () => clearInterval(t);
  }, [status.data?.running, status.reload, events.reload]);

  // Refresh the feed whenever a scan starts or finishes.
  useEffect(() => {
    events.reload();
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [status.data?.running]);

  const start = (path: string, msg: string) => async () => {
    try {
      await api.post(path);
      toast("info", msg);
      setTimeout(() => {
        status.reload();
        events.reload();
      }, 300);
    } catch (e) {
      toast("err", errMsg(e));
    }
  };

  const rows = events.data?.events ?? [];

  return (
    <div className="stack">
      <div className="page-head">
        <h1>Activity</h1>
        <div className="row">
          <button className="btn btn-secondary btn-sm" onClick={start("/sync", "Metadata sync started")} disabled={status.data?.running}>
            Check for updates
          </button>
          <button className="btn btn-primary btn-sm" onClick={start("/scan", "Scan started")} disabled={status.data?.running}>
            {status.data?.running ? "Working…" : "Scan all libraries"}
          </button>
        </div>
      </div>

      {status.data?.running && (
        <div className="card" style={{ borderColor: "var(--border-strong)" }}>
          <div className="row" style={{ justifyContent: "space-between" }}>
            <div className="row" style={{ gap: 10 }}>
              <Pill kind="scan">Scanning</Pill>
              <span className="muted">
                {status.data.processed} processed · {status.data.changed} changed · {status.data.errors} errors
              </span>
            </div>
          </div>
        </div>
      )}

      {events.err && <ErrorNote message={events.err} />}
      {!events.err && !events.loading && rows.length === 0 && (
        <EmptyState icon="⟳" message="No activity yet. Run a scan to get started." />
      )}

      {rows.length > 0 && (
        <div className="tablewrap">
          <table className="data">
            <thead>
              <tr>
                <th>When</th>
                <th>Event</th>
                <th>Status</th>
                <th>Summary</th>
              </tr>
            </thead>
            <tbody>
              {rows.map((ev) => (
                <tr key={ev.id} style={{ cursor: "pointer" }} onClick={() => setSelected(ev)}>
                  <td className="mono dim" style={{ fontSize: 11, whiteSpace: "nowrap" }}>
                    {new Date(ev.started_at).toLocaleString()}
                  </td>
                  <td style={{ color: "var(--text)" }}>{ev.title || TYPE_LABELS[ev.type] || ev.type}</td>
                  <td><EventStatus status={ev.status} /></td>
                  <td className="muted">{ev.summary}</td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      )}

      {selected && <EventDetail event={selected} onClose={() => setSelected(null)} />}
    </div>
  );
}

function num(details: Record<string, unknown> | null, key: string): number {
  const v = details?.[key];
  return typeof v === "number" ? v : 0;
}

function EventDetail({ event, onClose }: { event: Event; onClose: () => void }) {
  const d = event.details;
  const errorFiles = Array.isArray(d?.error_files) ? (d!.error_files as string[]) : [];
  const libraries = Array.isArray(d?.libraries) ? (d!.libraries as string[]) : [];

  return (
    <Modal title={event.title || TYPE_LABELS[event.type] || event.type} onClose={onClose} wide>
      <div className="row" style={{ gap: 10, marginBottom: 14 }}>
        <EventStatus status={event.status} />
        <span className="dim mono" style={{ fontSize: 11 }}>
          {new Date(event.started_at).toLocaleString()}
          {typeof d?.duration === "string" ? ` · ${d.duration}` : ""}
        </span>
      </div>

      {event.type === "drift_sync" && d ? (
        <div className="stack">
          <div className="row" style={{ gap: 26, flexWrap: "wrap" }}>
            <Stat label="Releases checked" value={num(d, "releases_checked")} />
            <Stat label="Changed upstream" value={num(d, "releases_changed")} color="var(--accent-text)" />
            <Stat label="Files re-tagged" value={num(d, "files_retagged")} />
            <Stat label="Errors" value={num(d, "errors")} color={num(d, "errors") > 0 ? "var(--danger-text)" : undefined} />
          </div>
          {errorFiles.length > 0 && (
            <div>
              <div className="eyebrow" style={{ marginBottom: 6 }}>Files that failed</div>
              <div className="scroll mono" style={{ fontSize: 11, color: "var(--danger-text)", display: "flex", flexDirection: "column", gap: 2 }}>
                {errorFiles.map((f, i) => (<div key={i} style={{ wordBreak: "break-all" }}>{f}</div>))}
              </div>
            </div>
          )}
        </div>
      ) : event.type === "scan" && d ? (
        <div className="stack">
          <div className="row" style={{ gap: 26, flexWrap: "wrap" }}>
            <Stat label="Processed" value={num(d, "processed")} />
            <Stat label="Unchanged" value={num(d, "unchanged")} muted />
            <Stat label="Changed" value={num(d, "changed")} color="var(--accent-text)" />
            <Stat label="Tags written" value={num(d, "tags_written")} />
            <Stat label="Errors" value={num(d, "errors")} color={num(d, "errors") > 0 ? "var(--danger-text)" : undefined} />
          </div>
          {libraries.length > 0 && (
            <div className="dim" style={{ fontSize: 12 }}>
              Libraries: <span className="mono">{libraries.join(", ")}</span>
            </div>
          )}
          {errorFiles.length > 0 && (
            <div>
              <div className="eyebrow" style={{ marginBottom: 6 }}>Files that failed</div>
              <div className="scroll mono" style={{ fontSize: 11, color: "var(--danger-text)", display: "flex", flexDirection: "column", gap: 2 }}>
                {errorFiles.map((f, i) => (
                  <div key={i} style={{ wordBreak: "break-all" }}>{f}</div>
                ))}
              </div>
            </div>
          )}
        </div>
      ) : (
        <pre className="mono scroll" style={{ fontSize: 11, color: "var(--text-muted)", whiteSpace: "pre-wrap", wordBreak: "break-all" }}>
          {JSON.stringify(d ?? {}, null, 2)}
        </pre>
      )}
    </Modal>
  );
}

function Stat({ label, value, color, muted }: { label: string; value: number; color?: string; muted?: boolean }) {
  return (
    <div>
      <div className="tabnum" style={{ fontSize: 22, fontWeight: 600, color: color ?? (muted ? "var(--text-muted)" : undefined) }}>
        {value}
      </div>
      <div className="l">{label}</div>
    </div>
  );
}
