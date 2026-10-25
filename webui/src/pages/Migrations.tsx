import { Fragment, useEffect, useState } from "react";
import { Link } from "react-router-dom";
import { api, errMsg } from "../api";
import { useDebounced, useFetch } from "../hooks";
import { MusicbrainzMigration, MigrationList, MigrationPolicy, MigrationRepairQueued } from "../types";
import { ConfirmDialog, EmptyState, ErrorNote, IdChip, Pill } from "../components/ui";
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
  // A manager moving an album to another valid ID: nothing was merged, the album's
  // identity changed hands.
  if (m.kind === "redirect" && m.entity_type === "release_group") return "re-keyed";
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

/** What a queue row's buttons post to: `/migrations/:id/<action>`. */
type MigrationAction = "approve" | "dismiss" | "release-files";

/** The collection page of a merge's or re-key's surviving entity, when there is one. */
function targetPath(m: MusicbrainzMigration): string | null {
  if (!m.target_artist_mb_id) return null;
  if (m.target_release_group_mb_id) return `/collection/${m.target_artist_mb_id}/${m.target_release_group_mb_id}`;
  return `/collection/${m.target_artist_mb_id}`;
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
        <Link className="railref" to={`/collection/${m.artist_mb_id}`}>
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
      {/* The surviving entity, as somewhere to go. "Re-keyed" and "merged into" are the
          right words and were the wrong affordance: the album under its new ID is
          usually in the collection, and the row only offered its ID to copy. */}
      {targetPath(m) && (
        <Link className="railref" to={targetPath(m)!}>
          <span>{m.entity_type === "artist" ? "Open the artist it became" : "Open the album under its new ID"}</span>
        </Link>
      )}
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
  const grouped = browse.sort === "artist";
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
  const act = (m: MusicbrainzMigration, what: MigrationAction) => async () => {
    setBusy(m.id);
    try {
      const res = await api.post<MigrationRepairQueued>(`/migrations/${m.id}/${what}`);
      if (res?.queued) {
        toast("info", res.message ?? "Asking the manager to re-read this artist.");
      } else if (what === "release-files") {
        toast("info", "Files marked for re-identification · migration applied");
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
        <h1>MusicBrainz changes</h1>
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
          <div className="row" style={{ justifyContent: "space-between", alignItems: "baseline" }}>
            <h2 style={{ fontSize: 14 }}>Awaiting review ({queueTotal})</h2>
            {/* A view, not a filter: the same rows, ordered so an artist's albums sit
                together under one header — which is how a manager refresh settles them. */}
            <button
              className={grouped ? "btn btn-primary btn-sm" : "btn btn-secondary btn-sm"}
              aria-pressed={grouped}
              title={grouped ? "Grouped by artist. Click to list by date found." : "Group the queue by artist"}
              onClick={() => browse.setSort(grouped ? "detected" : "artist", grouped ? "desc" : "asc")}
            >
              {grouped ? "Grouped by artist" : "Group by artist"}
            </button>
          </div>
          <QueueTable rows={queueRows} browse={browse} busy={busy} act={act} grouped={grouped} />
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
  grouped,
}: {
  rows: MusicbrainzMigration[];
  browse: ReturnType<typeof useBrowse>;
  busy: string | null;
  act: (m: MusicbrainzMigration, what: MigrationAction) => () => Promise<void>;
  grouped: boolean;
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
          {rows.map((m, i) => (
            <Fragment key={m.id}>
              {grouped && (i === 0 || (rows[i - 1].artist_mb_id ?? "") !== (m.artist_mb_id ?? "")) && (
                <ArtistGroupHead rows={rows.filter((r) => (r.artist_mb_id ?? "") === (m.artist_mb_id ?? ""))} busy={busy} act={act} />
              )}
              <QueueRow m={m} busy={busy} act={act} />
            </Fragment>
          ))}
        </tbody>
      </table>
    </div>
  );
}

/**
 * The header over one artist's rows in the grouped queue: who, how many are waiting
 * (across the whole queue, not this page — the page may cut a group in two), and the
 * one press that settles every album a refresh can, when any of them is waiting on the
 * manager. Pressing it approves one such row, which asks the manager about the artist;
 * the rest follow from the same answer.
 */
function ArtistGroupHead({
  rows,
  busy,
  act,
}: {
  rows: MusicbrainzMigration[];
  busy: string | null;
  act: (m: MusicbrainzMigration, what: MigrationAction) => () => Promise<void>;
}) {
  const first = rows[0];
  const refreshable = rows.find((r) => r.needs_manager_refresh && !isWorking(r));
  const working = rows.some(isWorking);
  const waiting = Math.max(first.artist_open ?? 0, rows.length);
  return (
    <tr className="group-row">
      <td colSpan={5}>
        <div className="row" style={{ justifyContent: "space-between", alignItems: "center", gap: 12, padding: "8px 12px" }}>
          <div className="row" style={{ gap: 8, alignItems: "baseline", fontSize: 12 }}>
            {first.artist_mb_id && first.artist_name ? (
              <Link to={`/collection/${first.artist_mb_id}`} style={{ fontWeight: 600 }}>{first.artist_name}</Link>
            ) : (
              <span className="dim" style={{ fontWeight: 600 }}>No artist in the collection</span>
            )}
            <span className="dim mono" style={{ fontSize: 11 }}>{waiting} waiting</span>
            {working && <span className="dim" style={{ fontSize: 11 }}>· the manager is re-reading this artist</span>}
          </div>
          {refreshable && (
            <button
              className="btn btn-secondary btn-sm"
              disabled={busy !== null}
              title="Ask the manager to re-read this artist once. Every album here it can correct settles from that one answer."
              onClick={act(refreshable, "approve")}
            >
              Ask the manager
            </button>
          )}
        </div>
      </td>
    </tr>
  );
}

function QueueRow({
  m,
  busy,
  act,
}: {
  m: MusicbrainzMigration;
  busy: string | null;
  act: (m: MusicbrainzMigration, what: MigrationAction) => () => Promise<void>;
}) {
  const [open, setOpen] = useState(false);
  const working = isWorking(m);
  // A press queues a manager refresh instead of applying anything, so the button says
  // so. "Approve" on a row whose approval asks a question somewhere else is the label
  // that made the whole interaction look broken.
  const primary = m.needs_manager_refresh ? "Ask the manager" : "Apply";
  // The third state, which had no label of its own: something claims this album that no
  // manager refresh can answer — files on disk, a want, an edition. The
  // press would fail with the sentence already shown in red beside it, so the control
  // is disabled rather than offering an action that cannot go anywhere. The blocker is
  // the title, because a disabled button is exactly where the reason gets asked for.
  const stuck = Boolean(m.blocker) && !m.needs_manager_refresh;
  // The one stuck state with a remedy here: files still resolving to an album whose ID
  // is gone. See releaseMigrationFiles for what the press does under each manager.
  const filesHold = stuck && m.owned && m.entity_type === "release_group" && m.kind === "deleted";
  const [releasing, setReleasing] = useState(false);

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
            {/* The fix the blocker names, as a control: the album is held because files
                still resolve to it. Confirmed first — it changes what those files are. */}
            {filesHold && (
              <button
                className="btn btn-secondary btn-sm"
                disabled={busy === m.id || working}
                onClick={() => setReleasing(true)}
                title="Re-identify the files that still resolve to this album, so the change can be applied"
              >
                Re-identify files
              </button>
            )}
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
            {releasing && (
              <ConfirmDialog
                title="Re-identify files"
                busy={busy === m.id}
                confirmLabel="Re-identify files"
                body={
                  <>
                    <span>
                      {m.files_on_disk} {m.files_on_disk === 1 ? "file still resolves" : "files still resolve"} to{" "}
                      <strong>{m.name || "this album"}</strong>, whose ID no longer exists.
                    </span>
                    <span>
                      In a library Autotaggerr manages, the files are marked as needing re-identification and the change is
                      applied now. Their tags are left alone, and they wait under Items → Unmatched to be attached. In a
                      library a manager governs, the manager is asked to re-read the artist and the files are read again from
                      it — the manager decides what they become.
                    </span>
                  </>
                }
                onConfirm={async () => {
                  await act(m, "release-files")();
                  setReleasing(false);
                }}
                onCancel={() => setReleasing(false)}
              />
            )}
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
