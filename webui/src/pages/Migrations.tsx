import { useEffect, useState } from "react";
import { Link } from "react-router-dom";
import { api, errMsg } from "../api";
import { useDebounced, useFetch } from "../hooks";
import { MusicbrainzMigration, MigrationList, MigrationPolicy, MigrationRepairQueued } from "../types";
import { EmptyState, ErrorNote, IdChip, Pill } from "../components/ui";
import { MBLink } from "../components/MBLink";
import { Pager, SortHeader, TableToolbar, useBrowse, usePaging } from "../components/browse";
import { useToast } from "../toast";

/**
 * Identity changes a metadata source reported, and what was done about them.
 *
 * This is a review table in the sense the style guide means: for anything held by
 * policy, the table *is* the confirmation — approving from a row applies it, with no
 * second dialog asking whether you meant it. Settled rows stay visible below, because
 * "what did it decide while I was not looking" is the question this page exists to
 * answer.
 *
 * **Two tables, two lists, one filter.** The queue is what needs a person and the
 * history is what does not, so they page and sort apart. The search box is shared: they
 * are two views of one table, and a filter that narrowed only one of them would answer
 * "where did that album go?" with silence.
 *
 * **The source is named, not assumed.** Nothing about a merged identity is particular
 * to MusicBrainz — the same queue would hold a second source's redirects tomorrow — so
 * the page talks about *metadata* and lets each row name the source that reported it.
 *
 * It offers no way to *start* a metadata refresh. Finding a merge is not an activity
 * of its own — it is what a refresh notices while fetching — so the verb belongs to
 * the Metadata page, and a second entry point here meant the same two words honoured
 * the cache everywhere except one page. The prose links there instead.
 */

const PAGE_SIZE = 25;

/** How often the table re-reads itself while a manager repair is in flight. */
const WORKING_POLL_MS = 5000;

const ENTITY_LABELS: Record<string, string> = {
  release: "Edition",
  artist: "Artist",
  release_group: "Album",
};

/** MusicBrainz's own path segment for an entity type, for the link out. */
const MB_ENTITY: Record<string, "release" | "artist" | "release-group"> = {
  release: "release",
  artist: "artist",
  release_group: "release-group",
};

/** The two statuses that mean a row is still in the queue rather than in the history. */
function isOpen(m: MusicbrainzMigration): boolean {
  return m.status === "pending" || m.status === "failed";
}

/**
 * Whether this row is waiting on a manager refresh that is already running.
 *
 * A settled row is never working, whatever mark it carries: an outcome is a statement
 * about work that has finished, and it outranks any claim that work is in progress.
 * The mark used to be checked on its own and a stale one could out-shout a recorded
 * result, so a retired album sat in the history saying "Asking the manager…".
 */
function isWorking(m: MusicbrainzMigration): boolean {
  return Boolean(m.repair_queued_at) && isOpen(m);
}

/**
 * What kind of change this is, in the words the evidence supports.
 *
 * A release-group deletion is deliberately not "deleted upstream": the ID usually
 * comes from a manager and was never the source's to delete. What is known is only
 * that it resolves nowhere.
 */
function kindLabel(m: MusicbrainzMigration): string {
  if (m.kind !== "deleted") return "merged";
  return m.entity_type === "release_group" ? "ID does not resolve" : "no longer exists";
}

function StatusCell({ m }: { m: MusicbrainzMigration }) {
  if (isWorking(m)) return <Pill kind="scan">Asking the manager…</Pill>;
  if (m.status === "pending") return <Pill kind="scan">Awaiting review</Pill>;
  if (m.status === "failed") return <Pill kind="err">Blocked</Pill>;
  if (m.status === "resolved") return <Pill kind="off">Resolved elsewhere</Pill>;
  if (m.status === "applied") {
    return <Pill kind="ok">{m.resolution === "approved" ? "Applied by you" : "Applied"}</Pill>;
  }
  return <Pill kind="off">Dismissed</Pill>;
}

/**
 * What the user owns under this entity — the question a retirement row most needs to
 * answer and the stored row cannot, since a release-group is never what a file is
 * keyed on.
 *
 * The count is the control, by the same rule as every other count in the app: it is
 * only ever read as a prelude to "show me which ones".
 */
function FilesCell({ m }: { m: MusicbrainzMigration }) {
  if (!m.files_on_disk) {
    return <span className="dim" style={{ fontSize: 11 }}>no files</span>;
  }
  return (
    <div className="stack" style={{ gap: 2 }}>
      <Link className="railref" to={`/items?mbid=${encodeURIComponent(m.old_mb_id)}`}>
        <span>
          {m.files_on_disk} file{m.files_on_disk === 1 ? "" : "s"}
        </span>
      </Link>
      {(m.editions ?? 0) > 1 && (
        <span className="dim" style={{ fontSize: 11 }}>
          across {m.editions} editions
        </span>
      )}
      {m.touches_pinned && (
        <span
          className="dim"
          style={{ fontSize: 11 }}
          title="Includes a file you attached by hand; the manual choice will be re-pointed, not discarded"
        >
          📌 manual attachment
        </span>
      )}
    </div>
  );
}

/** The entity, named and placed: what it is called, what kind of thing it is, whose. */
function WhatCell({ m }: { m: MusicbrainzMigration }) {
  const entity = ENTITY_LABELS[m.entity_type] ?? m.entity_type;
  return (
    <div className="stack" style={{ gap: 2 }}>
      <span>{m.name || entity}</span>
      <span className="dim" style={{ fontSize: 11 }}>
        {entity} · {kindLabel(m)} · {m.source_label}
      </span>
      {m.artist_name && m.artist_mb_id && (
        <Link className="railref" to={`/artist/${m.artist_mb_id}`}>
          <span>{m.artist_name}</span>
        </Link>
      )}
      <div className="row" style={{ gap: 6, flexWrap: "wrap" }}>
        <IdChip value={m.old_mb_id} />
        {m.kind === "deleted" ? (
          <MBLink entity={MB_ENTITY[m.entity_type] ?? "release"} mbid={m.old_mb_id} />
        ) : (
          <>
            <span className="dim" style={{ fontSize: 11 }}>→</span>
            <IdChip value={m.new_mb_id} />
            <MBLink entity={MB_ENTITY[m.entity_type] ?? "release"} mbid={m.new_mb_id} />
          </>
        )}
      </div>
    </div>
  );
}

export default function Migrations() {
  const toast = useToast();
  // Sorting is the server's: the page holds 25 rows of a table that can hold thousands,
  // so sorting what arrived would sort the page rather than the list.
  const browse = useBrowse("detected", "desc");
  const search = useDebounced(browse.query, 250);
  const [busy, setBusy] = useState<string | null>(null);

  const queuePage = Math.max(1, browse.pageFor("queue"));
  const historyPage = Math.max(1, browse.pageFor("history"));

  const params = (status: string, page: number, sort: string) => {
    const p = new URLSearchParams({
      status,
      limit: String(PAGE_SIZE),
      offset: String((page - 1) * PAGE_SIZE),
      sort,
      dir: browse.dir,
    });
    if (search.trim()) p.set("q", search.trim());
    return p.toString();
  };

  // Two requests, not one filtered in the browser: each row is decorated server-side
  // with the queries that say what approving it would do, so "fetch everything and
  // split" would pay for the whole table to show one page of it.
  const queue = useFetch<MigrationList>(
    () => api.get(`/migrations?${params("open", queuePage, browse.sort)}`),
    [queuePage, browse.sort, browse.dir, search],
  );
  const history = useFetch<MigrationList>(
    () => api.get(`/migrations?${params("closed", historyPage, browse.sort === "detected" ? "resolved" : browse.sort)}`),
    [historyPage, browse.sort, browse.dir, search],
  );
  const policy = useFetch<MigrationPolicy>(() => api.get("/migrations/policy"));

  const queueRows = queue.data?.migrations ?? [];
  const historyRows = history.data?.migrations ?? [];
  const working = queueRows.some(isWorking);

  const reload = () => {
    queue.reload();
    history.reload();
  };

  // A manager refresh runs for minutes on a worker goroutine, and the row it will
  // settle is on screen the whole time. Polling only while something is in flight keeps
  // the page still when nothing is happening — the state is on the row, so a reload or
  // a second tab shows it too.
  useEffect(() => {
    if (!working) return;
    const timer = setInterval(reload, WORKING_POLL_MS);
    return () => clearInterval(timer);
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [working]);

  // Approving from a row is the confirmation: for anything held by policy, this table
  // *is* the review step, so there is no second dialog asking whether you meant it.
  //
  // One press has two possible meanings and the server decides which: applying the
  // change, or asking the manager to re-read the artist so the change stops being
  // necessary. The second answers 202 and leaves the row where it is — reporting that
  // as "applied" is what made the button look like it did nothing.
  const act = (m: MusicbrainzMigration, what: "approve" | "dismiss") => async () => {
    setBusy(m.id);
    try {
      const res = await api.post<MigrationRepairQueued>(`/migrations/${m.id}/${what}`);
      if (what === "approve" && res?.queued) {
        toast("info", res.message ?? "Asking the manager to re-read this artist.");
      } else {
        toast("info", what === "approve" ? "Migration applied" : "Migration dismissed");
      }
      reload();
    } catch (e) {
      toast("err", errMsg(e));
    } finally {
      setBusy(null);
    }
  };

  const p = policy.data;
  const holding = p
    ? [
        p.review_releases && "edition merges",
        p.review_artists && "artist merges",
        p.review_deletions && "deletions",
        p.review_pinned && "anything touching a manual attachment",
      ].filter(Boolean)
    : [];

  const queueTotal = queue.data?.total ?? 0;
  const historyTotal = history.data?.total ?? 0;
  const queuePaging = usePaging(browse, queueTotal, PAGE_SIZE, "queue");
  const historyPaging = usePaging(browse, historyTotal, PAGE_SIZE, "history");
  const err = queue.err || history.err;
  const loading = queue.loading || history.loading;

  return (
    <div className="stack">
      <div className="page-head">
        <h1>Metadata migrations</h1>
        <div className="row">
          {/* Ghost: re-reading a list costs nothing, and this page's only control
              should not carry the weight of an action that does. */}
          <button
            className="btn btn-ghost btn-sm"
            onClick={reload}
            title="Re-read this list. Does not contact any metadata source."
          >
            Reload
          </button>
        </div>
      </div>

      <p className="dim" style={{ fontSize: 12, maxWidth: "68ch" }}>
        Metadata sources merge and delete entities over time. Autotaggerr notices when an ID it
        stores has moved — the source answers for the old ID but returns a different one — and
        re-points its own records to follow. This is noticed by any metadata refresh, so the
        nightly one keeps this list current on its own. To look now, run a refresh from{" "}
        <Link to="/mirror">Metadata</Link> — ignoring cached copies there is what re-reads every
        ID rather than only the expired ones. Changes are applied as they are found unless a
        category is held for review.
        {holding.length > 0
          ? ` Currently held for review: ${holding.join(", ")}.`
          : " Nothing is currently held for review."}
      </p>

      {err && <ErrorNote message={err} />}

      {/* One filter for both tables: they are two views of one list, and narrowing only
          the queue would answer "where did that album go?" with silence. */}
      <TableToolbar
        browse={browse}
        placeholder="Find a name or an identifier"
        showing={`${queueTotal} awaiting · ${historyTotal} settled`}
      />

      {!err && !loading && queueTotal === 0 && historyTotal === 0 && (
        <EmptyState
          icon="⇄"
          message={
            browse.query
              ? "No identity change matches that."
              : "No identity changes have been detected. This is the normal state — merges are found while processing and during metadata refreshes."
          }
        />
      )}

      {queueTotal > 0 && (
        <div className="stack">
          <h2 style={{ fontSize: 14 }}>Awaiting review ({queueTotal})</h2>
          <QueueTable rows={queueRows} browse={browse} busy={busy} act={act} />
          <Pager paging={queuePaging} unit="changes" />
        </div>
      )}

      {historyTotal > 0 && (
        <div className="stack">
          <h2 style={{ fontSize: 14 }}>History</h2>
          <HistoryTable rows={historyRows} browse={browse} />
          <Pager paging={historyPaging} unit="changes" />
        </div>
      )}
    </div>
  );
}

/**
 * The queue: everything still awaiting a decision, including rows whose application was
 * refused.
 *
 * A failed row belongs here rather than in the history because its refusal is usually
 * "not yet" — an album the manager still lists becomes retirable the moment it stops
 * listing it, and the run re-tries it. Filing those under history would hide the one
 * list where a person can see what is stuck and why.
 */
function QueueTable({
  rows,
  browse,
  busy,
  act,
}: {
  rows: MusicbrainzMigration[];
  browse: ReturnType<typeof useBrowse>;
  busy: string | null;
  act: (m: MusicbrainzMigration, what: "approve" | "dismiss") => () => Promise<void>;
}) {
  return (
    <div className="tablewrap">
      <table className="data">
        <thead>
          <tr>
            <SortHeader browse={browse} sortKey="name">What</SortHeader>
            <th>On disk</th>
            <th>What happens</th>
            <SortHeader browse={browse} sortKey="detected" defaultDir="desc">Found</SortHeader>
            <th style={{ textAlign: "right" }}>Actions</th>
          </tr>
        </thead>
        <tbody>
          {rows.map((m) => (
            <QueueRow key={m.id} m={m} busy={busy} act={act} />
          ))}
        </tbody>
      </table>
    </div>
  );
}

function QueueRow({
  m,
  busy,
  act,
}: {
  m: MusicbrainzMigration;
  busy: string | null;
  act: (m: MusicbrainzMigration, what: "approve" | "dismiss") => () => Promise<void>;
}) {
  const [open, setOpen] = useState(false);
  const working = isWorking(m);
  // A press queues a manager refresh instead of applying anything, so the button says
  // so. "Approve" on a row whose approval asks a question somewhere else is the label
  // that made the whole interaction look broken.
  const primary = m.needs_manager_refresh ? "Ask the manager" : "Apply";
  // The third state, which had no label of its own: something claims this album that no
  // manager refresh can answer — files on disk, a want, another credited artist. The
  // press would fail with the sentence already shown in red beside it, so the control
  // is disabled rather than offering an action that cannot go anywhere. The blocker is
  // the title, because a disabled button is exactly where the reason gets asked for.
  const stuck = Boolean(m.blocker) && !m.needs_manager_refresh;

  return (
    <>
      <tr>
        <td><WhatCell m={m} /></td>
        <td><FilesCell m={m} /></td>
        <td>
          <div className="stack" style={{ gap: 2 }}>
            <StatusCell m={m} />
            {working && (
              <span className="dim" style={{ fontSize: 11 }}>
                The manager is re-reading this artist. The row settles itself when it answers.
              </span>
            )}
            {/* The refusal, in full and on its own. It is the one thing on the row a
                person can act on — usually by removing the files it names — so it is
                never truncated, and it replaces the effect line rather than sitting
                under it: the effect for a blocked row *is* the blocker, wrapped in
                "Approving would not remove anything right now", so showing both printed
                the same sentence twice. */}
            {!working && stuck && (
              <span style={{ fontSize: 11, color: "var(--danger-text)" }}>{m.blocker}</span>
            )}
            {!working && !stuck && (
              <span className="dim" style={{ fontSize: 11 }}>
                {firstSentence(m.effect)}
              </span>
            )}
            {/* Only where a refresh is what the press does. On a row that applies
                directly, "one refresh settles them all" sat under a sentence saying the
                album is about to be removed and beside a button that refreshes nothing
                — two claims about one press, one of them false. */}
            {!working && m.needs_manager_refresh && (m.artist_open ?? 0) > 1 && (
              <span className="dim" style={{ fontSize: 11 }}>
                {m.artist_open} albums by this artist are waiting — one refresh settles them all.
              </span>
            )}
            <button className="railref" onClick={() => setOpen((v) => !v)} aria-expanded={open}>
              <span>{open ? "▼" : "▶"} Why</span>
            </button>
          </div>
        </td>
        <td className="dim mono" style={{ fontSize: 11 }}>
          {m.detected_at ? new Date(m.detected_at).toLocaleString() : "—"}
        </td>
        <td>
          <div className="row" style={{ justifyContent: "flex-end" }}>
            <button
              className="btn btn-ghost btn-sm"
              disabled={busy === m.id || working}
              onClick={act(m, "dismiss")}
              title="Leave Autotaggerr's records on the old ID. The change is remembered so it will not be raised again."
            >
              Dismiss
            </button>
            <button
              className="btn btn-primary btn-sm"
              disabled={busy === m.id || working || stuck}
              onClick={act(m, "approve")}
              title={stuck ? m.blocker : undefined}
            >
              {busy === m.id ? "Working…" : primary}
            </button>
          </div>
        </td>
      </tr>
      {/* The explanation belongs to the row above it, so it is styled as part of that
          row rather than as another row of the table: its own surface, indented past
          the hairline, and padded like the prose it is. Inheriting the table's cell
          rule gave it a 34px line box and no vertical padding, which pressed three
          lines of text against the borders on both sides. */}
      {open && (
        <tr className="detail-row">
          <td colSpan={5}>
            <div className="detail-body">
              <p>{m.problem}</p>
              <p className="dim">{m.effect}</p>
              {m.error && !m.blocker && (
                <p style={{ color: "var(--danger-text)" }}>{m.error}</p>
              )}
            </div>
          </td>
        </tr>
      )}
    </>
  );
}

/**
 * The history: what happened, when, and — the part a count cannot carry — who or what
 * decided it. An album a manager repaired and an album someone approved by hand are not
 * the same event, and reading them both as "Applied" is what made this table useless
 * for the question it exists to answer.
 */
function HistoryTable({
  rows,
  browse,
}: {
  rows: MusicbrainzMigration[];
  browse: ReturnType<typeof useBrowse>;
}) {
  return (
    <div className="tablewrap">
      <table className="data">
        <thead>
          <tr>
            <SortHeader browse={browse} sortKey="name">What</SortHeader>
            <th>On disk</th>
            <th>Outcome</th>
            <SortHeader browse={browse} sortKey="resolved" defaultDir="desc">Settled</SortHeader>
          </tr>
        </thead>
        <tbody>
          {rows.map((m) => (
            <tr key={m.id}>
              <td><WhatCell m={m} /></td>
              <td><FilesCell m={m} /></td>
              <td>
                <div className="stack" style={{ gap: 2 }}>
                  <StatusCell m={m} />
                  {/* Why it closed, and — since applications carry one now too — what was
                      done. This is the sentence the page was missing: a row that resolved
                      itself said "Applied" and left the reader to guess what had happened
                      upstream. Measured rather than left to fill the column, which on a
                      wide screen ran one 11px sentence across the best part of 900px. */}
                  {m.resolution_detail && (
                    <span className="dim" style={{ fontSize: 11, maxWidth: "80ch", lineHeight: 1.45 }}>
                      {m.resolution_detail}
                    </span>
                  )}
                  {m.error && (
                    <span style={{ fontSize: 11, color: "var(--danger-text)" }}>{m.error}</span>
                  )}
                </div>
              </td>
              <td className="dim mono" style={{ fontSize: 11 }}>
                {settledAt(m)}
              </td>
            </tr>
          ))}
        </tbody>
      </table>
    </div>
  );
}

/**
 * When a row stopped needing attention. Falls back through the stamps older rows have,
 * because a dismissal recorded none of them until this shipped — and a blank date
 * column is what made a history ordered by detection read as ordered by nothing.
 */
function settledAt(m: MusicbrainzMigration): string {
  const when = m.resolved_at ?? m.applied_at;
  return when ? new Date(when).toLocaleString() : "—";
}

/**
 * The first sentence of a paragraph, for a table cell. The full text is a click away on
 * the same row, so this does not have to carry the whole explanation — only enough that
 * a reader scanning the column knows which rows need the rest.
 */
function firstSentence(text: string): string {
  if (!text) return "";
  const end = text.indexOf(". ");
  return end === -1 ? text : text.slice(0, end + 1);
}
