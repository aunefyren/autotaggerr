import { useEffect, useState } from "react";
import { api, errMsg } from "../api";
import { useDebounced, useFetch } from "../hooks";
import { Event, EventItem, EventStat, EventsPage, JobView, ScanStatus } from "../types";
import { EmptyState, ErrorNote, IdChip, Modal, Pill } from "../components/ui";
import { ProgressBar } from "../components/ProgressBar";
import { PHASE_LABELS, phaseDrivesProgress } from "../components/phases";
import { FilterChip, Pager, TableToolbar, useBrowse, usePaging } from "../components/browse";
import { useToast } from "../toast";

const TYPE_LABELS: Record<string, string> = {
  process: "Processing",
  // Runs recorded before the verbs were named apart. Startup rewrites them to
  // "process", so this only covers a feed read before that migration lands.
  scan: "Processing",
  count_files: "Counting files",
  mb_mirror: "Metadata refresh",
  // Every pass that writes tags, whether a user pressed Tag files or a run reached its
  // tagging stage. One name, because it is one kind of work — the row says which run it
  // came from, which is the part that actually differs.
  tag_files: "Tagging",
  // The run's walk, before tagging became one activity. Startup rewrites these to
  // tag_files; this covers a feed read before that migration lands.
  process_files: "Tagging",
  // drift_sync is only ever an event recorded before the refresh verb was split out
  // of the processing runner — those runs refreshed metadata and re-tagged in one
  // pass, so they keep their own name. The rows are still in the table, and an old
  // event rendering as a raw type string would look like a bug.
  drift_sync: "Metadata sync",
  lidarr_sync: "Lidarr sync",
  mb_migration: "Identity changes",
  plex_refresh: "Plex refresh",
  health_check: "Health check",
  collection_scan: "Collection scan",
};

// What a group of detail rows is, when an activity recorded more than one kind.
//
// These name the *rows*, not the stage that produced them, which is why they are not
// the phase labels a running job reports: "Re-tagging changed releases" describes work
// in progress, and this heading sits over the files it finished writing. A metadata
// pass's phases (artists, editions, …) fall through to the shared labels, where naming
// the phase and naming the rows happen to be the same thing.
const ITEM_PHASE_LABELS: Record<string, string> = {
  "": "Files found on disk",
  drift: "Files re-tagged after an upstream change",
  refresh: "Releases that changed upstream",
};

// Labels for the kinds of job the queue holds, so a pending row reads as words.
//
// Noun form throughout: a queue entry is a record of work, not a control. The
// buttons that start these say the verb ("Refresh metadata"); everything that
// reports on them says the thing ("Metadata refresh"). A pass that ignored the
// cache is the only one that reads differently, because it is the only one that
// did something different.
const JOB_KIND_LABELS: Record<string, string> = {
  process_all: "Processing",
  process_library: "Processing",
  process_artist: "Processing",
  retag_all: "Tag files",
  retag_library: "Tag files",
  retag_artist: "Tag files",
  refresh_all: "Metadata refresh",
  refresh_verify: "Full metadata refresh",
  refresh_artist: "Metadata refresh",
  refresh_library: "Metadata refresh",
};

// isProcessJob distinguishes a file-walking processing run (which reports file
// counters and a per-file progress bar) from a metadata refresh (which does not).
function isProcessJob(job?: JobView): boolean {
  return job?.kind?.startsWith("process") ?? false;
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

const PAGE_SIZE = 50;

export default function Activity() {
  const toast = useToast();
  // Paging is URL state like every other browse flag, so a link to page three of the
  // feed is a link to page three. The feed is server-paged — unlike the Collection, it
  // never holds the whole table — so the offset goes into the query, not a slice.
  const browse = useBrowse("started_at", "desc");
  const offset = (Math.max(1, browse.page) - 1) * PAGE_SIZE;
  const typeFilter = browse.flag("type") ?? "";
  const statusFilter = browse.flag("status") ?? "";
  // One cascade: a run and everything it spawned. The feed is flat and chronological,
  // so this is how you read a single run's activities together — the counterpart to the
  // rail, for when the rows are not adjacent because something else ran between them.
  const parentFilter = browse.flag("parent") ?? "";
  const query = browse.query;

  // The Collection filters in the browser, so its box costs nothing per keystroke.
  // This one is a query, so typing "talk talk" unthrottled is nine round trips and a
  // list that flickers through nine intermediate answers. The input stays immediate —
  // it is the *fetch* that waits.
  const search = useDebounced(query, 250);

  const events = useFetch<EventsPage>(() => {
    const params = new URLSearchParams({ limit: String(PAGE_SIZE), offset: String(offset) });
    if (typeFilter) params.set("type", typeFilter);
    if (statusFilter) params.set("status", statusFilter);
    if (parentFilter) params.set("parent", parentFilter);
    if (search.trim()) params.set("q", search.trim());
    return api.get(`/events?${params}`);
  }, [offset, typeFilter, statusFilter, parentFilter, search]);
  const status = useFetch<ScanStatus>(() => api.get("/process/status"));
  const [selected, setSelected] = useState<Event | null>(null);
  // The cascade under the pointer, lit across every row that belongs to it. Hover state
  // rather than a per-run colour: a feed of two hundred rows would otherwise need two
  // hundred hues, and the style guide spends colour on status, not on identity.
  const [lit, setLit] = useState<string | null>(null);

  // Poll while anything is running — a processing run (via its status) or any other
  // job that left a running event in the feed, such as a metadata sweep with no run
  // in flight.
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

  // Refresh the feed whenever a job starts or finishes.
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
  const paging = usePaging(browse, events.data?.total ?? 0, PAGE_SIZE);
  const filtering = !!(typeFilter || statusFilter || parentFilter || query.trim());

  // The run a `parent` filter names. It is in the response — the cascade includes the
  // run itself — so the chip can say which run without a second fetch.
  const parentRun = parentFilter ? rows.find((ev) => ev.id === parentFilter) : undefined;

  const facet = (kind: "type" | "status", key: string) => events.data?.facets?.[kind]?.[key] ?? 0;
  // Only the types that have happened, most frequent first — a menu of every type the
  // app can emit would offer a dozen dead ends on a fresh install. The active one stays
  // listed even at zero, so a filter that matches nothing can still be seen and undone.
  const typeOptions = Object.entries(events.data?.facets?.type ?? {})
    .filter(([type, n]) => n > 0 || type === typeFilter)
    .sort((a, b) => b[1] - a[1] || a[0].localeCompare(b[0]));

  return (
    <div className="stack">
      <div className="page-head">
        <h1>Activity</h1>
        <div className="row">
          {/* Activity reports work; it is not where work is chosen. The verbs live
              where their scope does — per artist, per library, or collection-wide on
              the Collection page — and only the widest one is repeated here, because
              "nothing has happened yet" is a state this page has to answer for. */}
          <button className="btn btn-primary btn-sm" onClick={start("/process", "Processing started")} disabled={status.data?.running}>
            {status.data?.running ? "Working…" : "Process all libraries"}
          </button>
        </div>
      </div>

      {status.data?.running && (
        <div className="card stack" style={{ borderColor: "var(--border-strong)", gap: 8 }}>
          <div className="row" style={{ justifyContent: "space-between", alignItems: "center" }}>
            <div className="row" style={{ gap: 10, alignItems: "center" }}>
              <Pill kind="scan">{isProcessJob(status.data.current_job) ? "Processing" : "Working"}</Pill>
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

          {/* The counters belong to one stage of the job. In any other stage they are
              stale rather than wrong, so the bar goes indeterminate and the numbers go
              away — a "0 / 12000" held for five minutes of rate-limited metadata work
              is what reads as a hang. */}
          {(() => {
            const counted = phaseDrivesProgress(status.data.phase);
            if (counted && (status.data.total ?? 0) <= 0) return null;
            return (
              <div className="row" style={{ gap: 10, alignItems: "center" }}>
                <ProgressBar
                  done={status.data.done ?? 0}
                  total={status.data.total ?? 0}
                  width={260}
                  indeterminate={!counted}
                />
                {counted && (
                  <span className="mono dim" style={{ fontSize: 11 }}>
                    {status.data.done ?? 0} / {status.data.total}
                  </span>
                )}
              </div>
            );
          })()}

          {/* The file counters describe a processing run; a metadata refresh has
              none, so they are shown only for that kind of job. */}
          {isProcessJob(status.data.current_job) && (
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

      {/* Shown whenever anything has ever happened, not only when the current filter
          matched — hiding the controls when a filter empties the list is how a user gets
          stranded with no way to widen it again. */}
      {(rows.length > 0 || filtering) && (
        <TableToolbar
          browse={browse}
          placeholder="Filter by title"
          showing={paging.total > 0 ? `${paging.from}–${paging.to} of ${paging.total}` : undefined}
        >
          {/* Narrowed to one run. It states which one and how to leave, because a feed
              showing six rows when the app has run for weeks needs to say why. */}
          {parentFilter && (
            <FilterChip
              on
              count={paging.total}
              label={parentRun ? `In ${parentRun.title || TYPE_LABELS[parentRun.type] || parentRun.type}` : "In one run"}
              title="Showing one run and everything it spawned — click to show every activity"
              onClick={() => browse.setFlag("parent", null)}
            />
          )}
          <FilterChip
            on={statusFilter === "error"}
            count={facet("status", "error")}
            label="Failed"
            tone="warn"
            title="Only events that failed"
            onClick={() => browse.setFlag("status", statusFilter === "error" ? null : "error")}
          />
          <FilterChip
            on={statusFilter === "running"}
            count={facet("status", "running")}
            label="Running"
            title="Only events still in flight"
            onClick={() => browse.setFlag("status", statusFilter === "running" ? null : "running")}
          />
          {/* A select rather than ten more chips: the types are a list you pick one
              from, and a row of ten would out-weigh the table it narrows. The counts
              ride the option labels so the choice still states its own result. */}
          <select
            className="input"
            style={{ width: "auto" }}
            value={typeFilter}
            aria-label="Event type"
            onChange={(e) => browse.setFlag("type", e.target.value || null)}
          >
            <option value="">All types</option>
            {typeOptions.map(([type, count]) => (
              <option key={type} value={type}>
                {(TYPE_LABELS[type] ?? type) + ` (${count})`}
              </option>
            ))}
          </select>
        </TableToolbar>
      )}

      {!events.err && !events.loading && rows.length === 0 && (
        filtering ? (
          <div className="card">
            <div className="dim" style={{ fontSize: 12 }}>No event matches this filter.</div>
          </div>
        ) : (
          <EmptyState icon="⟳" message="No activity yet. Process a library to get started." />
        )
      )}

      {rows.length > 0 && (
        <div className="tablewrap">
          <table className="data">
            <thead>
              <tr>
                {/* The rail's gutter. No header: it labels nothing, it draws a relation. */}
                <th className="rail" aria-hidden="true" />
                <th>When</th>
                <th>Activity</th>
                <th>Status</th>
                <th style={{ textAlign: "right" }}>Duration</th>
                <th>Summary</th>
              </tr>
            </thead>
            <tbody>
              {rows.map((ev, i) => (
                <FeedRow
                  key={ev.id}
                  event={ev}
                  run={runOf(ev)}
                  joinsAbove={runOf(ev) !== null && runOf(rows[i - 1]) === runOf(ev)}
                  joinsBelow={runOf(ev) !== null && runOf(rows[i + 1]) === runOf(ev)}
                  lit={lit !== null && runOf(ev) === lit}
                  // Narrowed to this run already: the toolbar says so once, and
                  // repeating it on every row states the filter as if it were news.
                  namesItsRun={ev.parent_id !== parentFilter}
                  onLight={setLit}
                  onSelect={setSelected}
                  onOpenRun={(id) => browse.setFlag("parent", id)}
                />
              ))}
            </tbody>
          </table>
        </div>
      )}

      <Pager paging={paging} unit="activities" />

      {/* One modal, one activity: the thing you clicked, its own counters, its own
          detail. Opening a run used to list its stages and swap itself for whichever
          you picked, which is two levels of navigation to reach a row the feed can
          simply hold. */}
      {selected && (
        <EventDetail
          event={selected}
          onClose={() => setSelected(null)}
          onOpenRun={(id) => {
            setSelected(null);
            browse.setFlag("parent", id);
          }}
        />
      )}
    </div>
  );
}

/**
 * The Summary cell of a feed row: a live bar while the event runs, its summary line
 * once it has finished.
 *
 * The counters an event carries belong to one stage of it, so in any other stage the
 * bar goes indeterminate and the numbers are dropped rather than shown stale — the
 * phase label is the honest half and stays either way.
 */
function RunningCell({ event }: { event: Event }) {
  if (event.status !== "running") return <>{event.summary}</>;

  const counted = phaseDrivesProgress(event.phase);
  const label = event.phase ? PHASE_LABELS[event.phase] ?? event.phase : "";
  if (counted && (event.total ?? 0) <= 0) return <>{event.summary}</>;

  return (
    <div className="row" style={{ gap: 8, alignItems: "center" }}>
      <ProgressBar
        done={event.done ?? 0}
        total={event.total ?? 0}
        width={120}
        showPercent={false}
        indeterminate={!counted}
      />
      <span className="dim mono" style={{ fontSize: 11 }}>
        {[counted ? `${event.done ?? 0}/${event.total}` : "", label].filter(Boolean).join(" · ")}
      </span>
    </div>
  );
}

/**
 * The cascade an activity belongs to, or null for one that stands alone.
 *
 * A run and everything it spawned share one key — the run's id — so adjacency in the
 * feed is a comparison rather than a tree walk. An activity nobody spawned and that
 * spawned nothing has no rail at all: a line through a single row would claim a
 * relationship it does not have.
 */
function runOf(ev?: Event): string | null {
  if (!ev) return null;
  if (ev.parent_id) return ev.parent_id;
  return (ev.child_count ?? 0) > 0 ? ev.id : null;
}

/**
 * One activity: one row, at the time it started, whatever started it.
 *
 * Every row is the same row. A stage of a run and a verb somebody pressed are the same
 * work — the run only changes where it came from — so the feed renders them identically
 * and says the relationship instead of building it into the structure. It used to nest:
 * stages hid behind a disclosure on the run, rendered as a stripped-down variant with no
 * timestamp of its own, which made a cascading activity look like a lesser kind of thing
 * and put its detail two modals deep.
 *
 * The relation is the **rail** in the gutter (a line joining the rows of one cascade,
 * capped by a dot on the run itself) plus the run's name under the title. The rail
 * carries it when the rows are adjacent, which they usually are — one job runs at a
 * time — and the name carries it when something else ran in between.
 */
function FeedRow({
  event,
  run,
  joinsAbove,
  joinsBelow,
  lit,
  namesItsRun,
  onLight,
  onSelect,
  onOpenRun,
}: {
  event: Event;
  run: string | null;
  joinsAbove: boolean;
  joinsBelow: boolean;
  lit: boolean;
  namesItsRun: boolean;
  onLight: (run: string | null) => void;
  onSelect: (ev: Event) => void;
  onOpenRun: (id: string) => void;
}) {
  const isRun = !!run && !event.parent_id;
  const count = event.child_count ?? 0;

  return (
    <tr
      className={lit ? "lit" : undefined}
      style={{ cursor: "pointer" }}
      onClick={() => onSelect(event)}
      onMouseEnter={() => onLight(run)}
      onMouseLeave={() => onLight(null)}
    >
      <td className="rail" aria-hidden="true">
        {run && (
          <>
            {joinsAbove && <i className="up" />}
            {joinsBelow && <i className="down" />}
            {isRun ? <i className="dot" /> : <i className="tick" />}
          </>
        )}
      </td>
      <td className="mono dim" style={{ fontSize: 11, whiteSpace: "nowrap" }}>
        {new Date(event.started_at).toLocaleString()}
      </td>
      <td style={{ color: "var(--text)" }}>
        {event.title || TYPE_LABELS[event.type] || event.type}
        {/* Where this came from, or what it started. Both narrow the feed to the one
            cascade, which is what answers "show me this run" when a health check or a
            hand-pressed refresh has broken the rail's run of adjacent rows. */}
        {namesItsRun && event.parent_id && event.parent_title && (
          <div>
            <button
              className="railref"
              title={`Show only ${event.parent_title}`}
              onClick={(e) => {
                e.stopPropagation();
                onOpenRun(event.parent_id!);
              }}
            >
              ↳ <span>{event.parent_title}</span>
            </button>
          </div>
        )}
        {isRun && count > 0 && (
          <div>
            <button
              className="railref"
              title="Show only this run and what it spawned"
              onClick={(e) => {
                e.stopPropagation();
                onOpenRun(event.id);
              }}
            >
              <span>
                └ {count} activit{count === 1 ? "y" : "ies"}
              </span>
            </button>
          </div>
        )}
      </td>
      <td><EventStatus status={event.status} /></td>
      <td className="num dim" style={{ fontSize: 11, whiteSpace: "nowrap" }} title={durationTitle(event)}>
        {eventDuration(event)}
      </td>
      <td className="muted">
        <RunningCell event={event} />
      </td>
    </tr>
  );
}

function num(details: Record<string, unknown> | null, key: string): number {
  const v = details?.[key];
  return typeof v === "number" ? v : 0;
}

/**
 * What a metadata pass found, one row per entity: which releases changed upstream,
 * which are gone, which moved release-group, and what it could not read.
 *
 * Separate from FileDetail because the rows are not files. FileDetail's non-error row
 * reads "N tags written", which on a verb that writes nothing would be a false claim
 * about the user's audio — and the identifier here is an MBID, which is a thing to copy
 * and look up rather than a path to read.
 */
/**
 * A metadata pass's per-phase split. `Fetched` is what cost a rate-limit slot, and the
 * four entity kinds cost wildly different amounts — one total cannot say whether the
 * hours went on discographies or on release payloads.
 *
 * Rendered whenever an event carries the key, rather than for a named event type: the
 * detail view knows how to draw a phase breakdown, not which events have one.
 */
function PhaseBreakdown({ details }: { details: Record<string, unknown> | null }) {
  const phases = details?.phases;
  if (!Array.isArray(phases) || phases.length === 0) return null;

  return (
    <div className="stack" style={{ gap: 4 }}>
      <div className="eyebrow">By entity</div>
      {(phases as Record<string, unknown>[]).map((p, i) => (
        <div key={i} className="row" style={{ gap: 10, alignItems: "baseline", fontSize: 12 }}>
          <span style={{ minWidth: 110, color: "var(--text-muted)" }}>
            {PHASE_LABELS[String(p.phase)] ?? String(p.phase)}
          </span>
          <span className="mono dim">
            {num(p, "checked")} checked · {num(p, "fetched")} fetched · {num(p, "fresh")} cached
            {num(p, "errors") > 0 ? ` · ${num(p, "errors")} failed` : ""}
          </span>
        </div>
      ))}
    </div>
  );
}

/**
 * The counters an event declared about itself.
 *
 * A stat that names an EventItem status becomes a **chip**: a count is almost always
 * read as a prelude to "show me which ones", and the numbers used to sit above a list
 * they had no relationship with. Ones with nothing behind them stay plain figures —
 * making a dead number look pressable is worse than leaving it alone.
 *
 * Zero-valued stats are dropped entirely. An emitter declares the same set every time
 * so its events are comparable, which means most events carry several counters that
 * did not happen, and "0 gone upstream · 0 re-linked · 0 failed" is noise in front of
 * the two numbers that did.
 */
function StatRow({
  stats,
  items,
  active,
  onFilter,
}: {
  stats: EventStat[];
  items: EventItem[];
  active: string | null;
  onFilter: (status: string | null) => void;
}) {
  const shown = stats.filter((s) => s.value > 0 || (s.filter && s.filter === active));
  if (shown.length === 0) return null;

  return (
    <div className="row" style={{ gap: 10, flexWrap: "wrap", alignItems: "center" }}>
      {shown.map((stat, i) => {
        const selectable = !!stat.filter && items.some((it) => it.status === stat.filter);
        if (!selectable) {
          return (
            <Stat
              key={i}
              label={stat.label}
              value={stat.value}
              muted={stat.kind === "muted"}
              color={statColor(stat)}
            />
          );
        }
        const on = active === stat.filter;
        return (
          <FilterChip
            key={i}
            on={on}
            count={stat.value}
            label={stat.label}
            tone={stat.kind === "bad" ? "warn" : undefined}
            title={on ? "Showing only these — click to show everything" : `Show only ${stat.label.toLowerCase()}`}
            onClick={() => onFilter(on ? null : stat.filter!)}
          />
        );
      })}
    </div>
  );
}

// Semantic emphasis to the status colour language. `bad` only colours when it happened:
// a red 0 claims a problem that is not there.
function statColor(stat: EventStat): string | undefined {
  if (stat.kind === "notable") return "var(--accent-text)";
  if (stat.kind === "bad") return stat.value > 0 ? "var(--danger-text)" : undefined;
  return undefined;
}

/**
 * One activity, on its own terms: its status, its own span, the counters it declared and
 * the rows it recorded.
 *
 * It shows nothing about its siblings. A run's modal used to list its stages and swap
 * itself for whichever one you picked, so reading what a run wrote meant a row, a modal,
 * a list and a second modal — for data the feed can hold directly. The one thing kept is
 * a way back out to the run, and it goes to the *feed* filtered to that run rather than
 * to another modal.
 */
function EventDetail({
  event,
  onClose,
  onOpenRun,
}: {
  event: Event;
  onClose: () => void;
  onOpenRun: (id: string) => void;
}) {
  // The feed's copy of an event carries no per-file rows — they only come with the
  // single-event fetch, so opening one loads it.
  const full = useFetch<Event>(() => api.get(`/events/${event.id}`), [event.id]);
  const [filter, setFilter] = useState<string | null>(null);

  // Reset the filter when the modal moves to another event: a chip left active would
  // silently hide most of the activity you just opened.
  useEffect(() => setFilter(null), [event.id]);

  const d = full.data?.details ?? event.details;
  const items = full.data?.items ?? [];
  const stats = full.data?.stats ?? event.stats ?? [];
  const errorFiles = Array.isArray(d?.error_files) ? (d!.error_files as string[]) : [];
  const libraries = Array.isArray(d?.libraries) ? (d!.libraries as string[]) : [];

  return (
    <Modal title={event.title || TYPE_LABELS[event.type] || event.type} onClose={onClose} wide>
      <div className="row" style={{ gap: 10, marginBottom: 14, flexWrap: "wrap" }}>
        <EventStatus status={event.status} />
        <span className="dim mono" style={{ fontSize: 11 }} title={durationTitle(event)}>
          {new Date(event.started_at).toLocaleString()} · {eventDuration(event)}
        </span>
        {/* Always stated here, even when the feed behind is already narrowed to this
            run: a modal carries no toolbar to have said it. */}
        {event.parent_id && event.parent_title && (
          <button className="railref" onClick={() => onOpenRun(event.parent_id!)}>
            ↳ <span>Part of {event.parent_title}</span>
          </button>
        )}
        {!event.parent_id && (event.child_count ?? 0) > 0 && (
          <button className="railref" onClick={() => onOpenRun(event.id)}>
            <span>
              └ {event.child_count} activit{event.child_count === 1 ? "y" : "ies"} in this run
            </span>
          </button>
        )}
      </div>

      <div className="stack">
        <StatRow stats={stats} items={items} active={filter} onFilter={setFilter} />

        <PhaseBreakdown details={d} />

        {/* Stated wherever a metadata refresh is read, not only on the Metadata page:
            an event listing releases that changed reads like something happened to the
            files, and nothing did. */}
        {event.type === "mb_mirror" && (
          <div className="dim" style={{ fontSize: 12 }}>
            A metadata refresh writes no files. Releases that changed upstream are re-tagged by the
            next processing run, or immediately with <em>Tag files</em>.
          </div>
        )}

        {libraries.length > 0 && (
          <div className="dim" style={{ fontSize: 12 }}>
            Libraries: <span className="mono">{libraries.join(", ")}</span>
          </div>
        )}

        <ItemList
          items={items}
          details={d}
          loading={full.loading}
          filter={filter}
          fallbackErrors={errorFiles}
        />

        {/* The escape hatch, closed by default. Everything above is what an emitter
            chose to surface; this is everything it recorded, for the times those differ
            and the difference is the bug. */}
        {d && Object.keys(d).length > 0 && (
          <details>
            <summary className="dim" style={{ fontSize: 11, cursor: "pointer" }}>Raw details</summary>
            <pre className="mono scroll" style={{ fontSize: 11, color: "var(--text-muted)", whiteSpace: "pre-wrap", wordBreak: "break-all", marginTop: 8 }}>
              {JSON.stringify(d, null, 2)}
            </pre>
          </details>
        )}
      </div>
    </Modal>
  );
}

// What each per-entity outcome is called, and the status colour it carries. "Changed
// upstream" rather than "Changed": nothing here changed the entity, MusicBrainz did.
const ENTITY_OUTCOMES: Record<string, { label: string; kind: string }> = {
  refreshed: { label: "Changed upstream", kind: "scan" },
  gone: { label: "Gone upstream", kind: "warn" },
  relinked: { label: "Re-linked", kind: "off" },
  error: { label: "Failed", kind: "err" },
};

/**
 * The rows an event recorded: which files changed and exactly what changed on each,
 * which releases moved upstream, and what failed.
 *
 * One list for both, because they answer the same question — *which ones?* — and were
 * two components only because a file row and an entity row draw differently. That
 * difference is now a field on the row (`kind`) rather than a component boundary, which
 * is what lets an event carrying both kinds render at all.
 *
 * Grouped by phase when the rows come from more than one, so a run's walk output is not
 * interleaved with the releases its drift stage found.
 */
function ItemList({
  items,
  details,
  loading,
  filter,
  fallbackErrors,
}: {
  items: EventItem[];
  details: Record<string, unknown> | null;
  loading: boolean;
  filter: string | null;
  fallbackErrors: string[];
}) {
  if (loading) return <div className="muted" style={{ fontSize: 12 }}>Loading detail…</div>;

  // Nothing recorded: either an older event, or one where nothing interesting happened.
  // The error list is the pre-detail-rows fallback and still worth showing.
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

  const shown = filter ? items.filter((it) => it.status === filter) : items;

  // "Showing 500 of 3120" — the cap bounds what was stored, and without the pair a
  // reader takes the stored count for the whole truth.
  const summary = (details?.detail ?? null) as Record<string, unknown> | null;
  const recorded = typeof summary?.recorded === "number" ? summary.recorded : items.length;
  const total =
    typeof summary?.total === "number"
      ? summary.total
      : num(summary, "changed_files") + num(summary, "failed_files");
  const truncated = total > recorded && recorded > 0;

  // Group only when there is something to group: one phase is a list, not a structure.
  const phases = Array.from(new Set(shown.map((it) => it.phase ?? "")));
  const grouped = phases.length > 1;

  return (
    <div>
      <div className="row" style={{ marginBottom: 6, gap: 10, alignItems: "baseline" }}>
        <div className="eyebrow">Detail</div>
        <span className="dim" style={{ fontSize: 11 }}>
          {filter ? `${shown.length} of ${items.length} shown` : truncated ? `showing ${recorded} of ${total}` : ""}
        </span>
      </div>
      <div className="scroll stack" style={{ gap: grouped ? 14 : 10 }}>
        {phases.map((phase) => {
          const rows = shown.filter((it) => (it.phase ?? "") === phase);
          if (rows.length === 0) return null;
          return (
            <div key={phase} className="stack" style={{ gap: 8 }}>
              {grouped && (
                <div className="eyebrow">
                  {ITEM_PHASE_LABELS[phase] ?? PHASE_LABELS[phase] ?? phase}
                </div>
              )}
              {rows.map((item) => <ItemRow key={item.id} item={item} />)}
            </div>
          );
        })}
        {shown.length === 0 && (
          <div className="muted" style={{ fontSize: 12 }}>Nothing matches that filter.</div>
        )}
      </div>
    </div>
  );
}

/**
 * One recorded row. An entity row leads with a copyable MBID and an outcome pill; a file
 * row leads with its path and the diff of what was written to it.
 *
 * The split is not cosmetic: a file row reports how many tags were written, and a
 * metadata refresh writes none — "0 tags written" beside a release MBID would be a
 * claim about the user's audio from the one verb that promises not to touch it.
 */
function ItemRow({ item }: { item: EventItem }) {
  if (item.kind === "entity") {
    const outcome = ENTITY_OUTCOMES[item.status] ?? { label: item.status, kind: "off" };
    return (
      <div className="stack" style={{ gap: 4 }}>
        <div className="row" style={{ gap: 8, alignItems: "baseline", flexWrap: "wrap" }}>
          <IdChip value={item.path} />
          <Pill kind={outcome.kind}>{outcome.label}</Pill>
          {item.tags_written > 0 && (
            <span className="dim" style={{ fontSize: 11 }}>
              {item.tags_written} file{item.tags_written === 1 ? "" : "s"} re-tagged
            </span>
          )}
        </div>
        {item.error && (
          <div className="mono" style={{ fontSize: 11, color: "var(--danger-text)", wordBreak: "break-all" }}>
            {item.error}
          </div>
        )}
      </div>
    );
  }

  return (
    <div className="stack" style={{ gap: 5 }}>
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
