import { useEffect, useState } from "react";
import { api, errMsg } from "../api";
import { useFetch } from "../hooks";
import { Event, EventItem, EventsPage, JobView, ScanStatus } from "../types";
import { EmptyState, ErrorNote, Modal, Pill } from "../components/ui";
import { ProgressBar } from "../components/ProgressBar";
import { useToast } from "../toast";

const TYPE_LABELS: Record<string, string> = {
  scan: "Scan",
  mb_mirror: "Metadata refresh",
  // drift_sync survives for events recorded before the refresh verb was split out
  // of the scan runner. The rows are still in the table, and an old event
  // rendering as a raw type string would look like a bug.
  drift_sync: "Metadata sync",
  lidarr_sync: "Lidarr sync",
  mb_migration: "Identity check",
  plex_refresh: "Plex refresh",
  health_check: "Health check",
};

// Human labels for the stage a running job reports, across scans and metadata passes.
const PHASE_LABELS: Record<string, string> = {
  // scan phases
  refresh: "Refreshing metadata",
  scanning: "Scanning files",
  drift: "Re-tagging changed releases",
  plex: "Refreshing Plex",
  migrations: "Applying identity changes",
  collection: "Updating the collection",
  // metadata-pass phases
  artists: "Artists",
  discographies: "Discographies",
  editions: "Editions",
  releases: "Releases",
  paused: "Paused",
};

// Labels for the kinds of job the queue holds, so a pending row reads as words.
const JOB_KIND_LABELS: Record<string, string> = {
  scan_all: "Scan",
  scan_library: "Scan",
  scan_artist: "Scan",
  retag_library: "Tag files",
  retag_artist: "Tag files",
  refresh_all: "Metadata refresh",
  refresh_verify: "Verify identities",
  refresh_artist: "Metadata refresh",
  refresh_library: "Metadata refresh",
};

// isScanJob distinguishes a file-walking scan (which reports file counters and a
// per-file progress bar) from a metadata refresh (which does not).
function isScanJob(job?: JobView): boolean {
  return job?.kind?.startsWith("scan") ?? false;
}

// A coarse duration string ("3m 20s", "1h 4m"). One formatter for both the live
// counter and a finished event's span, so a job reads the same while it works and
// afterwards — the two are the same measurement, and two formats for it would
// invite comparing a "1h 4m" against a "1h3m28.41s".
function formatDuration(ms: number): string {
  const secs = Math.max(0, Math.floor(ms / 1000));
  const h = Math.floor(secs / 3600);
  const m = Math.floor((secs % 3600) / 60);
  const s = secs % 60;
  if (h) return `${h}h ${m}m`;
  if (m) return `${m}m ${s}s`;
  return `${s}s`;
}

// How long a job has been running. Recomputed on each poll-driven re-render,
// which is close enough for a job measured in minutes to hours.
function elapsed(from?: string): string {
  if (!from) return "";
  return formatDuration(Date.now() - new Date(from).getTime());
}

/**
 * How long an event took, derived from the row's own timestamps rather than read
 * out of its details payload. Every event carries started_at and finished_at, so
 * deriving gives a duration to all of them — including the five event types whose
 * emitters never wrote one, and every row already in the database. A running event
 * counts up; one that ended without a finish stamp says so rather than ticking
 * forever.
 */
function eventDuration(ev: Event): string {
  if (ev.finished_at) {
    return formatDuration(new Date(ev.finished_at).getTime() - new Date(ev.started_at).getTime());
  }
  return ev.status === "running" ? elapsed(ev.started_at) : "—";
}

// The exact span behind the coarse figure, for when "1h 4m" is not precise enough.
function durationTitle(ev: Event): string {
  const from = new Date(ev.started_at).toLocaleString();
  if (ev.finished_at) return `${from} → ${new Date(ev.finished_at).toLocaleString()}`;
  return ev.status === "running" ? `Running since ${from}` : `Started ${from}, never finished`;
}

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

  // Poll while anything is running — a scan (via its status) or any other job that
  // left a running event in the feed, such as a metadata sweep with no scan in flight.
  const anyEventRunning = (events.data?.events ?? []).some((e) => e.status === "running");
  const shouldPoll = (status.data?.running ?? false) || anyEventRunning;
  useEffect(() => {
    if (!shouldPoll) return;
    const t = setInterval(() => {
      status.reload();
      events.reload();
    }, 3000);
    return () => clearInterval(t);
  }, [shouldPoll, status.reload, events.reload]);

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
        <div className="card stack" style={{ borderColor: "var(--border-strong)", gap: 8 }}>
          <div className="row" style={{ justifyContent: "space-between", alignItems: "center" }}>
            <div className="row" style={{ gap: 10, alignItems: "center" }}>
              <Pill kind="scan">{isScanJob(status.data.current_job) ? "Scanning" : "Working"}</Pill>
              <span className="muted">
                {status.data.current_job?.title ?? "Working…"}
              </span>
              {status.data.phase && (
                <span className="dim" style={{ fontSize: 12 }}>{PHASE_LABELS[status.data.phase] ?? status.data.phase}</span>
              )}
              {status.data.current && (
                <span className="dim mono" style={{ fontSize: 12 }} title="Currently processing">
                  {status.data.current}
                </span>
              )}
            </div>
            <span className="dim mono" style={{ fontSize: 11 }}>{elapsed(status.data.started_at)}</span>
          </div>

          {(status.data.total ?? 0) > 0 && (
            <div className="row" style={{ gap: 10, alignItems: "center" }}>
              <ProgressBar done={status.data.done ?? 0} total={status.data.total ?? 0} width={260} />
              <span className="mono dim" style={{ fontSize: 11 }}>
                {status.data.done ?? 0} / {status.data.total}
              </span>
            </div>
          )}

          {/* The file counters describe a scan; a metadata refresh has none, so they are
              shown only for a scan job. */}
          {isScanJob(status.data.current_job) && (
            <span className="muted" style={{ fontSize: 12 }}>
              {status.data.processed} processed · {status.data.changed} changed · {status.data.errors} errors
            </span>
          )}
        </div>
      )}

      {(status.data?.queue?.length ?? 0) > 0 && (
        <div className="card stack" style={{ gap: 6 }}>
          <div className="eyebrow">Queued ({status.data!.queue!.length})</div>
          {status.data!.queue!.map((j, i) => (
            <div key={i} className="row" style={{ gap: 8, alignItems: "center" }}>
              <span className="dim mono" style={{ fontSize: 11, minWidth: 16, textAlign: "right" }}>{i + 1}</span>
              <span style={{ fontSize: 13 }}>{j.title}</span>
              <span className="dim" style={{ fontSize: 11 }}>{JOB_KIND_LABELS[j.kind] ?? j.kind}</span>
            </div>
          ))}
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
                <th style={{ textAlign: "right" }}>Duration</th>
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
                  <td className="num dim" style={{ fontSize: 11, whiteSpace: "nowrap" }} title={durationTitle(ev)}>
                    {eventDuration(ev)}
                  </td>
                  <td className="muted">
                    {ev.status === "running" && (ev.total ?? 0) > 0 ? (
                      <div className="row" style={{ gap: 8, alignItems: "center" }}>
                        <ProgressBar done={ev.done ?? 0} total={ev.total ?? 0} width={120} showPercent={false} />
                        <span className="dim mono" style={{ fontSize: 11 }}>
                          {ev.done ?? 0}/{ev.total}
                          {ev.phase ? ` · ${PHASE_LABELS[ev.phase] ?? ev.phase}` : ""}
                        </span>
                      </div>
                    ) : (
                      ev.summary
                    )}
                  </td>
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
  // The feed's copy of an event carries no per-file rows — they only come with the
  // single-event fetch, so opening one loads it.
  const full = useFetch<Event>(() => api.get(`/events/${event.id}`), [event.id]);
  const d = event.details;
  const items = full.data?.items ?? [];
  const errorFiles = Array.isArray(d?.error_files) ? (d!.error_files as string[]) : [];
  const libraries = Array.isArray(d?.libraries) ? (d!.libraries as string[]) : [];

  return (
    <Modal title={event.title || TYPE_LABELS[event.type] || event.type} onClose={onClose} wide>
      <div className="row" style={{ gap: 10, marginBottom: 14 }}>
        <EventStatus status={event.status} />
        <span className="dim mono" style={{ fontSize: 11 }} title={durationTitle(event)}>
          {new Date(event.started_at).toLocaleString()} · {eventDuration(event)}
        </span>
      </div>

      {event.type === "mb_mirror" && d ? (
        <div className="stack">
          <div className="row" style={{ gap: 26, flexWrap: "wrap" }}>
            <Stat label="Entities checked" value={num(d, "done")} />
            <Stat label="Fetched" value={num(d, "fetched")} />
            <Stat label="Already cached" value={num(d, "fresh")} muted />
            <Stat label="Changed upstream" value={num(d, "changed_releases")} color="var(--accent-text)" />
            <Stat label="Errors" value={num(d, "errors")} color={num(d, "errors") > 0 ? "var(--danger-text)" : undefined} />
          </div>
          {/* Stated here as well as on the Metadata page: an event listing releases
              that changed reads like something happened to the files, and nothing
              did. */}
          <div className="dim" style={{ fontSize: 12 }}>
            A metadata refresh writes no files. Releases that changed upstream are re-tagged by the
            next scan, or immediately with <em>Tag files</em>.
          </div>
        </div>
      ) : event.type === "drift_sync" && d ? (
        <div className="stack">
          <div className="row" style={{ gap: 26, flexWrap: "wrap" }}>
            <Stat label="Releases checked" value={num(d, "releases_checked")} />
            <Stat label="Changed upstream" value={num(d, "releases_changed")} color="var(--accent-text)" />
            <Stat label="Files re-tagged" value={num(d, "files_retagged")} />
            <Stat label="Errors" value={num(d, "errors")} color={num(d, "errors") > 0 ? "var(--danger-text)" : undefined} />
          </div>
          <FileDetail items={items} details={d} loading={full.loading} fallbackErrors={errorFiles} />
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
          <FileDetail items={items} details={d} loading={full.loading} fallbackErrors={errorFiles} />
        </div>
      ) : (
        <pre className="mono scroll" style={{ fontSize: 11, color: "var(--text-muted)", whiteSpace: "pre-wrap", wordBreak: "break-all" }}>
          {JSON.stringify(d ?? {}, null, 2)}
        </pre>
      )}
    </Modal>
  );
}

/**
 * The per-file half of an event: which files changed, the exact fields that changed
 * on each, and which failed. Counters say twelve files changed; this says which
 * twelve and what happened to them.
 *
 * The list is capped server-side, so when rows were dropped it says so rather than
 * letting the first N read as the whole story. `fallbackErrors` renders events
 * recorded before detail rows existed, which still carry an error_files array.
 */
function FileDetail({
  items,
  details,
  loading,
  fallbackErrors,
}: {
  items: EventItem[];
  details: Record<string, unknown> | null;
  loading: boolean;
  fallbackErrors: string[];
}) {
  const summary = (details?.detail ?? null) as Record<string, unknown> | null;
  const totalChanged = typeof summary?.changed_files === "number" ? summary.changed_files : 0;
  const totalFailed = typeof summary?.failed_files === "number" ? summary.failed_files : 0;
  const truncated = items.length > 0 && items.length < totalChanged + totalFailed;

  if (loading) return <div className="muted" style={{ fontSize: 12 }}>Loading file detail…</div>;

  // Nothing recorded: either an older event, or a run where no file changed.
  if (items.length === 0) {
    if (fallbackErrors.length === 0) return null;
    return (
      <div>
        <div className="eyebrow" style={{ marginBottom: 6 }}>Files that failed</div>
        <div className="scroll mono" style={{ fontSize: 11, color: "var(--danger-text)", display: "flex", flexDirection: "column", gap: 2 }}>
          {fallbackErrors.map((f, i) => (<div key={i} style={{ wordBreak: "break-all" }}>{f}</div>))}
        </div>
      </div>
    );
  }

  return (
    <div>
      <div className="row" style={{ marginBottom: 6, gap: 10, alignItems: "baseline" }}>
        <div className="eyebrow">Files</div>
        {truncated && (
          <span className="dim" style={{ fontSize: 11 }}>
            showing {items.length} of {totalChanged + totalFailed}
          </span>
        )}
      </div>
      <div className="scroll stack" style={{ gap: 12 }}>
        {items.map((item) => (
          <div key={item.id} className="stack" style={{ gap: 5 }}>
            <div className="row" style={{ gap: 8, alignItems: "baseline", flexWrap: "wrap" }}>
              <span
                className="mono"
                style={{ fontSize: 11, wordBreak: "break-all", color: item.status === "error" ? "var(--danger-text)" : "var(--text)" }}
              >
                {item.path}
              </span>
              {item.status === "error" ? (
                <Pill kind="err">Failed</Pill>
              ) : (
                <span className="dim" style={{ fontSize: 11 }}>
                  {item.tags_written} tag{item.tags_written === 1 ? "" : "s"} written
                </span>
              )}
            </div>

            {item.error && (
              <div className="mono" style={{ fontSize: 11, color: "var(--danger-text)", wordBreak: "break-all" }}>{item.error}</div>
            )}

            {/* Same old → new language as the file-tags view, so it is learned once. */}
            {item.changes && item.changes.length > 0 && (
              <div className="diff">
                {item.changes.map((c) => (
                  <div className="diffrow" key={c.field}>
                    <span className="diffkey">{c.field}</span>
                    <div className="diffvals">
                      {c.old ? <span className="diffv rem">{c.old}</span> : <span className="diffv empty">(empty)</span>}
                      {c.new ? <span className="diffv add">{c.new}</span> : <span className="diffv empty">(removed)</span>}
                    </div>
                  </div>
                ))}
              </div>
            )}
          </div>
        ))}
      </div>
    </div>
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
