import { useEffect, useState } from "react";
import { Link } from "react-router-dom";
import { api, errMsg } from "../api";
import { useDebounced, useFetch } from "../hooks";
import { EntityRef, Event, EventItem, EventStat, EventsPage, JobView, MBFilesPage, ScanStatus } from "../types";
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
  artwork_refresh: "Artwork refresh",
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

/**
 * What a kind of activity is, and — where a reader would otherwise have to ask — why it
 * runs where it runs.
 *
 * A stage in a flat feed has to explain itself: three of these land *after* the tagging
 * that a reader thinks of as the point of the run, and "did that happen in the wrong
 * order?" is the question the order itself provokes. The answer is a property of the
 * work, so it belongs beside the work.
 *
 * A map rather than a branch per type in the detail view: this is the one thing the UI
 * legitimately knows that an emitter cannot (an emitter would be writing English into
 * the database), and a type with nothing to explain simply has no entry.
 */
const TYPE_NOTES: Record<string, string> = {
  mb_mirror:
    "A metadata refresh re-reads MusicBrainz and writes no files. Releases that changed upstream are re-tagged by the next processing run, or immediately with Tag files.",
  artwork_refresh:
    "Fetches covers and artist images into the local cache so the browsing pages paint from disk. Its own activity rather than part of a metadata refresh: artwork comes from separate providers on a separate budget, so none of its numbers is comparable with the MusicBrainz ones. Most rows come from a new artist or album fetching its own artwork as it arrives; the scheduled pass tops up what has expired. Nothing in your library is written.",
  count_files:
    "Sizes the run before it starts: every folder walked once to count the files it will visit. It reads no tags and changes nothing.",
  collection_scan:
    "Re-derives the collection from the files already indexed — no disk walk, no network, no file writes. It runs after tagging on purpose: it can only describe what this run has already recorded.",
  lidarr_sync:
    "Mirrors the manager's catalogue over the collection. It runs after the collection scan on purpose: the mirror only covers artists the collection already knows about, including any this run just discovered. Artists Lidarr did not list are reported rather than assumed away — their wanted view has nothing behind it until they are matched or detached.",
  plex_refresh:
    "Tells Plex to re-read the albums this run touched. One event per run rather than per album, which would flood the feed — the albums themselves are listed below.",
  tag_files:
    "Everything this pass wrote to disk. The walk finds files whose tags no longer match what Autotaggerr knows; the drift half rewrites files whose release changed upstream, which the walk cannot see because the file itself has not moved.",
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

/**
 * How many changed files an activity may hold before their diffs start collapsed.
 *
 * A handful of diffs is what the modal was opened to read; fifty is a wall to scroll
 * past on the way to anything else. The threshold is the same kind of rule as the
 * coverage meter's segmented-below-30: the shape follows the count because the reading
 * does.
 */
const EXPANDED_FILE_LIMIT = 10;

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
              {/* Same sentence the finished activity stores as its summary, so a run
                  reads the same while it works as it does afterwards. */}
              {status.data.processed} files processed · {status.data.changed} changed · {status.data.errors} errors
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
  const failures = Array.isArray(d?.failures) ? (d!.failures as string[]) : [];

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
        {/* The line the feed row states, restated here. It was the one thing an activity
            always has — every emitter writes one — and opening a row used to *lose* it,
            which is why a stage with nothing but zero counters opened onto a blank
            modal. It leads, because for the short activities it is the whole story. */}
        {(full.data?.summary ?? event.summary) && (
          <div style={{ fontSize: 13, color: "var(--text)" }}>{full.data?.summary ?? event.summary}</div>
        )}

        <StatRow stats={stats} items={items} active={filter} onFilter={setFilter} />

        <PhaseBreakdown details={d} />

        {/* What this kind of activity is, and why it runs where it runs. */}
        {TYPE_NOTES[event.type] && (
          <div className="dim" style={{ fontSize: 12, maxWidth: "70ch" }}>{TYPE_NOTES[event.type]}</div>
        )}

        {libraries.length > 0 && (
          <div className="dim" style={{ fontSize: 12 }}>
            Libraries: <span className="mono">{libraries.join(", ")}</span>
          </div>
        )}

        {/* Lookups that did not complete. They have no detail rows because they are
            not *about* an entity — the manager was unreachable, or one artist's albums
            could not be read — and every one of them used to be a log line and nothing
            else, so a Lidarr that was half down produced a row identical to a healthy
            one with smaller numbers in it. */}
        {failures.length > 0 && (
          <div>
            <div className="eyebrow" style={{ marginBottom: 6 }}>Lookups that failed</div>
            <div className="stack" style={{ gap: 2 }}>
              {failures.map((f, i) => (
                <div key={i} className="mono" style={{ fontSize: 11, color: "var(--danger-text)", wordBreak: "break-word" }}>{f}</div>
              ))}
            </div>
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

/**
 * What each per-entity outcome is called, the status colour it carries, and — the part
 * a pill cannot hold — what it actually means.
 *
 * "Changed upstream" rather than "Changed": nothing here changed the entity,
 * MusicBrainz did. But that phrasing still leaves the question it was written to
 * answer only half-answered, because "changed" reads as *some specific edit was made*
 * when what happened is coarser: the payload was re-fetched and no longer matches the
 * copy the cache held. No field-level comparison exists, so the note says so rather
 * than letting the label imply one.
 *
 * The notes are rendered as a legend under the detail list, and only for the outcomes
 * an event actually recorded — the same rule that drops zero-valued counters. A
 * glossary of four states in front of a list containing one of them is noise.
 */
const ENTITY_OUTCOMES: Record<string, { label: string; kind: string; note: string }> = {
  refreshed: {
    label: "Changed upstream",
    kind: "scan",
    note: "MusicBrainz's copy no longer matches the one Autotaggerr had cached. The payload is compared whole, not field by field — what differs is not known here, only that something does. The files using it are re-tagged by the drift stage of a processing run, or by Tag files.",
  },
  gone: {
    label: "Gone upstream",
    kind: "warn",
    note: "MusicBrainz answered 404. Usually a merge — the entity moved into another one — which is recorded as an identity change on the Migrations page rather than treated as a failure. Files still pointing at it keep their tags until the merge is applied.",
  },
  relinked: {
    label: "Re-linked",
    kind: "off",
    note: "The release moved to a different release-group upstream. Its own content may be unchanged; what moved is where it belongs, so a row was rewritten and no file was touched.",
  },
  unknown: {
    label: "Not in Lidarr",
    kind: "warn",
    note: "The collection files this artist under Lidarr, and Lidarr did not list them. Nothing fills their catalogue until they are matched in Lidarr or detached from the manager, so their wanted view is empty rather than accurate.",
  },
  error: {
    label: "Failed",
    kind: "err",
    note: "The lookup itself did not complete — a timeout, a rate-limit rejection, or a malformed response. Nothing is known about the entity either way, and the next pass tries again.",
  },
};

/**
 * What kind of MusicBrainz identifier a detail row is about, from the phase that
 * produced it.
 *
 * The phase is the honest source, and the only one that survives a 404: the pass read
 * artists in the artist phase and release-groups in the editions phase, so it knows
 * what the ID *is* even when nothing local can name it — which is exactly the case
 * where the label is most needed. `related.kind` is preferred when the collection did
 * resolve the row, since that is the answer rather than an inference.
 *
 * The value doubles as musicbrainz.org's own path segment, which is what makes the
 * row's identifier openable rather than only copyable.
 */
const PHASE_ENTITY_KIND: Record<string, string> = {
  artists: "artist",
  discographies: "artist",
  editions: "release-group",
  releases: "release",
  refresh: "release",
};

const ENTITY_KIND_LABELS: Record<string, string> = {
  artist: "Artist",
  "release-group": "Release group",
  release: "Release",
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
  // Which files are showing their diff. Ahead of every early return, because the
  // list's own loading and empty states are exits and a hook after one of them runs a
  // different number of times per render — the modal's first paint has no rows and its
  // second does, which is precisely the transition that breaks.
  //
  // Null means "nobody has clicked anything", which is not the same as "everything is
  // closed": the default is derived from how many files there are, and a map seeded
  // with it would have to be rebuilt whenever the filter changed the count.
  const [openFiles, setOpenFiles] = useState<Record<string, boolean> | null>(null);

  if (loading) return <div className="muted" style={{ fontSize: 12 }}>Loading detail…</div>;

  // Nothing recorded: either an older event, or one where nothing interesting happened.
  // The error list is the pre-detail-rows fallback and still worth showing.
  if (items.length === 0) {
    if (fallbackErrors.length === 0) return null;
    return (
      <div>
        <div className="eyebrow" style={{ marginBottom: 6 }}>Files that failed</div>
        <div className="mono" style={{ fontSize: 11, color: "var(--danger-text)", display: "flex", flexDirection: "column", gap: 2 }}>
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

  // A file row's diff is a group of its own, and every group in the app collapses.
  // Whether they *start* collapsed is a property of how many there are: a handful is
  // the diff you opened the activity to read, fifty is a wall you scroll past to reach
  // anything else. Same reasoning as the coverage meter's segmented/proportional
  // switch — the shape follows the count because the reading does.
  const fileRows = shown.filter((it) => (it.kind ?? "") === "" && (it.changes?.length ?? 0) > 0);
  const openByDefault = fileRows.length > 0 && fileRows.length <= EXPANDED_FILE_LIMIT;
  const isOpen = (id: string) => openFiles?.[id] ?? openByDefault;
  const toggleFile = (id: string) =>
    setOpenFiles((prev) => ({ ...(prev ?? {}), [id]: !(prev?.[id] ?? openByDefault) }));
  const setAll = (open: boolean) =>
    setOpenFiles(Object.fromEntries(fileRows.map((it) => [it.id, open])));
  const anyOpen = fileRows.some((it) => isOpen(it.id));

  return (
    <div>
      <div className="row" style={{ marginBottom: 6, gap: 10, alignItems: "baseline" }}>
        <div className="eyebrow">Detail</div>
        <span className="dim" style={{ fontSize: 11 }}>
          {filter ? `${shown.length} of ${items.length} shown` : truncated ? `showing ${recorded} of ${total}` : ""}
        </span>
        {/* Only where there is more than one diff to act on: a control that collapses
            a single file is a button that does what clicking the file does. */}
        {fileRows.length > 1 && (
          <button
            className="railref"
            style={{ marginLeft: "auto" }}
            onClick={() => setAll(!anyOpen)}
          >
            <span>{anyOpen ? "Collapse all" : "Expand all"}</span>
          </button>
        )}
      </div>
      {/* No inner scroller: the modal body is one now, and a list with nothing pinned
          above it has no reason to own a second. Two nested scrollbars for one list is
          worse than a long one — an inner `.scroll` is for keeping a search field or a
          filter visible while its results move, which this is not. */}
      <div className="stack" style={{ gap: grouped ? 14 : 10 }}>
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
              {rows.map((item) => (
                <ItemRow
                  key={item.id}
                  item={item}
                  open={isOpen(item.id)}
                  onToggle={() => toggleFile(item.id)}
                />
              ))}
            </div>
          );
        })}
        {shown.length === 0 && (
          <div className="muted" style={{ fontSize: 12 }}>Nothing matches that filter.</div>
        )}
      </div>

      {/* What the outcomes in this list mean. Only the ones present, by the same rule
          that drops zero-valued counters: a glossary of five states in front of a list
          holding one of them is noise. */}
      <OutcomeLegend items={shown} />
    </div>
  );
}

/**
 * One recorded row, in one of three shapes: an entity (a MusicBrainz identifier), an
 * album (a Plex refresh target), or a file.
 *
 * The split is not cosmetic: a file row reports how many tags were written, and a
 * metadata refresh writes none — "0 tags written" beside a release MBID would be a
 * claim about the user's audio from the one verb that promises not to touch it.
 */
function ItemRow({ item, open, onToggle }: { item: EventItem; open: boolean; onToggle: () => void }) {
  if (item.kind === "entity") return <EntityItemRow item={item} />;
  if (item.kind === "album") return <AlbumItemRow item={item} />;

  const changes = item.changes ?? [];
  // A file with no diff — a failure, or a write the emitter counted without recording
  // fields — has nothing to collapse, so it stays a plain row rather than gaining a
  // caret that opens onto nothing.
  if (changes.length === 0) {
    return (
      <div className="stack" style={{ gap: 5 }}>
        <div className="row" style={{ gap: 8, alignItems: "baseline" }}>
          <FileHeading item={item} />
        </div>
        {item.error && (
          <div className="mono" style={{ fontSize: 11, color: "var(--danger-text)", wordBreak: "break-all" }}>{item.error}</div>
        )}
      </div>
    );
  }

  return (
    <div className="filegroup">
      <button className="filehead" aria-expanded={open} onClick={onToggle}>
        <i className="twisty">{open ? "▼" : "▶"}</i>
        <FileHeading item={item} />
      </button>
      {open && (
        <>
          {item.error && (
            <div className="stack">
              <div className="mono" style={{ fontSize: 11, color: "var(--danger-text)", wordBreak: "break-all" }}>{item.error}</div>
            </div>
          )}
          {/* Same old → new language as the file-tags view, so it is learned once. */}
          <div className="diff">
            {changes.map((c) => (
              <div className="diffrow" key={c.field}>
                <span className="diffkey">{c.field}</span>
                <div className="diffvals">
                  {c.old ? <span className="diffv rem">{c.old}</span> : <span className="diffv none">(empty)</span>}
                  {c.new ? <span className="diffv add">{c.new}</span> : <span className="diffv none">(removed)</span>}
                </div>
              </div>
            ))}
          </div>
        </>
      )}
    </div>
  );
}

// The line that identifies a file row: its path and what happened to it. Shared by the
// collapsible and the plain shape so a file reads the same either way — the caret is
// the only difference between them, which is the only difference there is.
function FileHeading({ item }: { item: EventItem }) {
  return (
    <>
      <span
        className="filepath"
        style={{ color: item.status === "error" ? "var(--danger-text)" : "var(--text)" }}
      >
        {item.path}
      </span>
      {item.status === "error" ? (
        <Pill kind="err">Failed</Pill>
      ) : (
        <span className="dim" style={{ fontSize: 11, whiteSpace: "nowrap" }}>
          {item.tags_written} tag{item.tags_written === 1 ? "" : "s"} written
        </span>
      )}
    </>
  );
}

/**
 * One album a Plex refresh was asked for.
 *
 * The identifier is a title Plex knows, not a path and not an MBID, so it is set in
 * the UI face rather than in mono — the mono rule is for identifiers, and treating an
 * album title as one would say it is something to copy and look up.
 */
function AlbumItemRow({ item }: { item: EventItem }) {
  return (
    <div className="stack" style={{ gap: 4 }}>
      <div className="row" style={{ gap: 8, alignItems: "baseline", flexWrap: "wrap" }}>
        <span style={{ fontSize: 12, color: item.status === "error" ? "var(--danger-text)" : "var(--text)" }}>
          {item.path}
        </span>
        {item.status === "error" ? <Pill kind="err">Failed</Pill> : <Pill kind="ok">Refreshed</Pill>}
      </div>
      {item.error && (
        <div className="mono" style={{ fontSize: 11, color: "var(--danger-text)", wordBreak: "break-all" }}>
          {item.error}
        </div>
      )}
    </div>
  );
}

/**
 * One MusicBrainz identifier and what happened to it.
 *
 * Three things a bare MBID could not say, in the order they answer "what am I looking
 * at": **what kind** of identifier it is (a release and a release-group are both
 * UUIDs, and the fix for a bad one differs), **what Autotaggerr calls it** and, behind
 * a click, **which of your files depend on it**. A page of forty UUIDs that returned
 * 404 says something is wrong and nothing about what; the same page saying "OK
 * Computer — Radiohead · 12 files" is a list of albums to go and look at.
 */
function EntityItemRow({ item }: { item: EventItem }) {
  const outcome = ENTITY_OUTCOMES[item.status] ?? { label: item.status, kind: "off", note: "" };
  const ref = item.related;
  // Resolved beats inferred, but the phase still answers when the collection cannot —
  // which is exactly the 404 case.
  const kind = ref?.kind || PHASE_ENTITY_KIND[item.phase ?? ""] || "";
  const [showFiles, setShowFiles] = useState(false);

  return (
    <div className="stack" style={{ gap: 4 }}>
      <div className="row" style={{ gap: 8, alignItems: "baseline", flexWrap: "wrap" }}>
        <IdChip value={item.path} />
        {/* What kind of identifier it is, and — the same fact, so the same element —
            where it lives. A release and a release-group are both UUIDs and the fix
            for a bad one differs, so the type has to be on the row; and a 404 is
            answered by looking at MusicBrainz, which otherwise means retyping a UUID
            into a search box.

            MusicBrainz's own vocabulary here, not the collection's ("edition",
            "album"): this row is a report of what MusicBrainz said about a MusicBrainz
            identifier, and the link goes there. Translating the type would make the
            label disagree with the page it opens. */}
        {kind ? (
          <a
            className="railref"
            href={`https://musicbrainz.org/${kind}/${item.path}`}
            target="_blank"
            rel="noreferrer noopener"
            title={`Open this ${kind} on musicbrainz.org`}
          >
            <span>{ENTITY_KIND_LABELS[kind] ?? kind} ↗</span>
          </a>
        ) : null}
        <Pill kind={outcome.kind}>{outcome.label}</Pill>
        {item.tags_written > 0 && (
          <span className="dim" style={{ fontSize: 11 }}>
            {item.tags_written} file{item.tags_written === 1 ? "" : "s"} re-tagged
          </span>
        )}
      </div>

      {ref && (
        <div className="row" style={{ gap: 8, alignItems: "baseline", flexWrap: "wrap", paddingLeft: 12 }}>
          <EntityName entity={ref} mbid={item.path} />
          {/* The count is the control, by the same rule as every other count in the
              app: it is only ever read as a prelude to "show me which ones". */}
          {ref.files > 0 ? (
            <button className="railref" onClick={() => setShowFiles((v) => !v)}>
              <span>{showFiles ? "▼" : "▶"} {ref.files} file{ref.files === 1 ? "" : "s"} on disk</span>
            </button>
          ) : (
            <span className="dim" style={{ fontSize: 11 }}>no files on disk</span>
          )}
        </div>
      )}

      {showFiles && <EntityFiles mbid={item.path} />}

      {item.error && (
        <div className="mono" style={{ fontSize: 11, color: "var(--danger-text)", wordBreak: "break-all" }}>
          {item.error}
        </div>
      )}
    </div>
  );
}

/**
 * What the collection calls an identifier, linked to the page that shows it.
 *
 * A name with nowhere to go is half an answer: the reason to know an MBID is *Kid A*
 * is to open Kid A. Which link is possible depends on what resolved — an album page
 * needs its artist too, since that is how the route is shaped — so the link degrades
 * to plain text rather than disappearing.
 *
 * A row with a file count and no name is not a gap in this component: it is the
 * finding. Files point at an identifier the collection cannot name, which is a broken
 * collection row rather than a broken lookup.
 *
 * The prop is `entity`, not `ref`. **`ref` is reserved**: React strips it from the
 * props of a function component and routes it to the ref machinery instead, so the
 * parameter arrives `undefined` and the first property read throws — taking the whole
 * tree down, which renders as a blank page rather than as a broken row. TypeScript
 * does not catch it, because the prop type is perfectly valid; only the runtime knows
 * the name is spoken for. It only fired on rows the collection could resolve, so
 * events whose identifiers were all unresolvable opened normally and the crash looked
 * like it belonged to certain activity types.
 */
function EntityName({ entity, mbid }: { entity: EntityRef; mbid: string }) {
  if (!entity.name) {
    return <span className="dim" style={{ fontSize: 12 }}>not in the collection</span>;
  }

  const artistLink = entity.kind === "artist" ? mbid : entity.artist_mb_id;
  const to =
    entity.kind === "artist"
      ? `/collection/${mbid}`
      : entity.artist_mb_id && entity.group_mb_id
        ? `/collection/${entity.artist_mb_id}/${entity.group_mb_id}`
        : artistLink
          ? `/collection/${artistLink}`
          : null;

  const name = <span style={{ fontSize: 12, color: "var(--text)" }}>{entity.name}</span>;
  return (
    <span className="row" style={{ gap: 6, alignItems: "baseline" }}>
      {to ? <Link to={to} style={{ color: "var(--accent-text)" }}>{name}</Link> : name}
      {entity.artist && entity.kind !== "artist" && (
        <span className="dim" style={{ fontSize: 11 }}>— {entity.artist}</span>
      )}
    </span>
  );
}

/**
 * The files behind one identifier, fetched when asked for.
 *
 * Lazily, per row: a metadata pass records hundreds of identifiers and only the one
 * being looked at is worth its paths — the same rule the master/detail split follows
 * everywhere else. The library is named beside each path because "which of my two
 * copies is this" is the next question whenever there are two.
 */
function EntityFiles({ mbid }: { mbid: string }) {
  const files = useFetch<MBFilesPage>(() => api.get(`/mb/${mbid}/files`), [mbid]);

  if (files.loading) return <div className="dim" style={{ fontSize: 11 }}>Loading files…</div>;
  if (files.err) return <ErrorNote message={files.err} />;

  const rows = files.data?.files ?? [];
  if (rows.length === 0) {
    return <div className="dim" style={{ fontSize: 11 }}>No indexed file points at this identifier.</div>;
  }

  const total = files.data?.total ?? rows.length;
  return (
    <div className="stack" style={{ gap: 2, paddingLeft: 12, borderLeft: "1px solid var(--border)" }}>
      {rows.map((f) => (
        <div key={f.path} className="row" style={{ gap: 8, alignItems: "baseline" }}>
          <span
            className="mono"
            style={{ fontSize: 11, wordBreak: "break-all", color: f.status === "error" ? "var(--danger-text)" : "var(--text-muted)" }}
          >
            {f.path}
          </span>
          {f.library && <span className="dim" style={{ fontSize: 11, whiteSpace: "nowrap" }}>{f.library}</span>}
        </div>
      ))}
      {total > rows.length && (
        <div className="dim" style={{ fontSize: 11 }}>showing {rows.length} of {total}</div>
      )}
    </div>
  );
}

/**
 * What the outcomes in this list mean — the sentence a pill has no room for.
 *
 * "Changed upstream" is the one this exists for: it reads as *a particular edit was
 * made*, when what it records is that the whole payload no longer matches the cached
 * copy. The label cannot be made to carry that without becoming a sentence, so the
 * sentence goes here.
 *
 * Only outcomes present in the list, and only when the list has entity rows at all —
 * the same rule that drops zero-valued counters, for the same reason.
 */
function OutcomeLegend({ items }: { items: EventItem[] }) {
  const present = Array.from(
    new Set(items.filter((it) => it.kind === "entity").map((it) => it.status)),
  ).filter((s) => ENTITY_OUTCOMES[s]);
  if (present.length === 0) return null;

  return (
    <div className="stack" style={{ gap: 6, marginTop: 12 }}>
      <div className="eyebrow">What these mean</div>
      {present.map((status) => {
        const outcome = ENTITY_OUTCOMES[status];
        return (
          <div key={status} className="row" style={{ gap: 8, alignItems: "baseline" }}>
            <span style={{ flex: "none" }}><Pill kind={outcome.kind}>{outcome.label}</Pill></span>
            <span className="dim" style={{ fontSize: 12, maxWidth: "70ch" }}>{outcome.note}</span>
          </div>
        );
      })}
    </div>
  );
}

/**
 * A counter with no rows behind it.
 *
 * Shaped like a FilterChip and deliberately inert — no hover, no pointer, no
 * aria-pressed. It used to be a 22px hero figure, which put two sizes of counter in
 * one row: the one chip among them looked like a stray control rather than the one
 * counter you can act on, and the row read as a dashboard header instead of a set of
 * facts about one activity. Same size means the difference between them is the
 * affordance, which is the difference that is actually there.
 */
function Stat({ label, value, color, muted }: { label: string; value: number; color?: string; muted?: boolean }) {
  return (
    <span className="statpill" style={muted ? { opacity: 0.7 } : undefined}>
      {label}
      <span className="statpill-n" style={color ? { color } : undefined}>{value}</span>
    </span>
  );
}
