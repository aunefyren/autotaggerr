import { useEffect, useRef, useState } from "react";
import { Link } from "react-router-dom";
import { api, errMsg } from "../api";
import { useFetch } from "../hooks";
import { CollectionArtist, Manager, ScanStatus } from "../types";
import { EmptyState, ErrorNote, Pill, Skeleton } from "../components/ui";
import { useToast } from "../toast";
import { MBLink } from "../components/MBLink";
import { AddArtistModal } from "../components/AddArtistModal";
import { Artwork } from "../components/Artwork";
import { CoverageBar } from "../components/CoverageBar";
import { RunBar } from "../components/RunBar";
import { SyncLidarrDialog } from "../components/SyncLidarrDialog";
import { RefreshMetadataDialog } from "../components/RefreshMetadataDialog";
import { FilterChip, Pager, SortHeader, TableToolbar, matches, useBrowse, usePaging, useSorted } from "../components/browse";

function ManagedBy({ managed_by }: { managed_by: string }) {
  if (managed_by === "lidarr") return <Pill kind="scan">Lidarr</Pill>;
  if (managed_by === "mixed") return <Pill kind="scan">Lidarr + native</Pill>;
  if (managed_by === "autotaggerr") return <Pill kind="chg">Native</Pill>;
  // Provenance could not be determined — the library's manager row is gone. Shown
  // as its own state so missing information is not read as "natively managed".
  return (
    <Pill kind="off">
      <span title="This artist's library has no resolvable manager — reassign one on the Libraries page">Unknown</span>
    </Pill>
  );
}

/**
 * What this artist is set to want, in one glance. Following and picking are the two
 * ways to want something, so both are named here rather than only the toggle state.
 */
function WantedSummary({ artist }: { artist: CollectionArtist }) {
  const picked = artist.picked_count ?? 0;
  // A manager-owned artist is not "following", whatever the stored flag says — the
  // manager decides. Reporting Following here is what made albums look auto-wanted
  // by a follow the user never set.
  if (!artist.follow_governs) {
    return (
      <div className="row" style={{ gap: 6 }}>
        <Pill kind="off">Managed</Pill>
        {picked > 0 && <span className="dim" style={{ fontSize: 11 }}>+{picked} picked</span>}
      </div>
    );
  }
  if (artist.monitored) {
    return (
      <div className="row" style={{ gap: 6 }}>
        <Pill kind="ok">Following</Pill>
        {picked > 0 && <span className="dim" style={{ fontSize: 11 }}>+{picked} picked</span>}
      </div>
    );
  }
  if (picked > 0) return <Pill kind="chg">{picked} picked</Pill>;
  return <span className="dim">—</span>;
}

/** Enough to fill the fold. Not a guess at how many artists there are. */
const PLACEHOLDER_ROWS = 8;
/**
 * Varied so the name column reads as a list of names rather than as a grid of bars.
 * The range is the range a real name plus its MB link occupies (roughly 75–155px):
 * `table.data` is auto-layout, so a placeholder wider or narrower than the content it
 * stands in for hands the difference to another column and the headers slide sideways
 * as the rows land.
 */
const NAME_WIDTHS = [132, 84, 156, 104, 72, 144, 96, 118];

/**
 * The table while the collection is still being counted.
 *
 * `/artists` aggregates every release group, desire and artist credit in the collection
 * before it can answer, so on a real library the page has a visible wait — and it used
 * to spend that wait as blank space, then drop a toolbar, a table and a pager in all at
 * once. This is the same table with its values missing: the shared `<thead>`, the same
 * 34px rows, one placeholder per cell. Nothing below the table moves when the data
 * lands, and the columns settle by a few pixels rather than appearing from nowhere.
 *
 * It is deliberately not a spinner. A spinner says "wait" and nothing else; the shape
 * of the page says what is coming, and a shape that is already correct is the part that
 * makes the arrival feel instant.
 */
function LoadingRows() {
  return (
    <tbody aria-hidden="true">
      {Array.from({ length: PLACEHOLDER_ROWS }, (_, i) => (
        <tr key={i}>
          <td><Skeleton w={24} h={24} /></td>
          <td><Skeleton w={NAME_WIDTHS[i % NAME_WIDTHS.length]} /></td>
          {/* 21px is a `.pill`'s own height (11px line + 3px padding + hairline), so a
              placeholder pill and the pill that replaces it are the same object. */}
          <td><Skeleton w={62} h={21} pill /></td>
          <td>
            <div className="row" style={{ gap: 8 }}>
              {/* The row's one moving part, and it is not a new idea: the indeterminate
                  meter already means "real work, counted in a unit this bar does not
                  have" wherever a run reports progress. A coverage that is not known
                  yet is exactly that, so the loading collection is drawn in the same
                  language as a loading run. */}
              <span className="coverage" style={{ width: 120 }}>
                <span className="coverage-track indeterminate" />
              </span>
              <Skeleton w={26} />
            </div>
          </td>
          <td className="num"><Skeleton w={14} /></td>
          <td className="num"><Skeleton w={14} /></td>
          <td><Skeleton w={88} h={21} pill /></td>
        </tr>
      ))}
    </tbody>
  );
}

/** Sort keys, kept next to the accessors so a header and its ordering cannot drift. */
const SORT: Record<string, (ar: CollectionArtist) => string | number> = {
  name: (ar) => ar.name,
  albums: (ar) => ar.owned_count ?? 0,
  missing: (ar) => ar.missing_count ?? 0,
  mismatch: (ar) => ar.mismatch_count ?? 0,
};

export default function Collection() {
  const toast = useToast();
  const { data, err, loading, reload } = useFetch<CollectionArtist[]>(() => api.get("/artists"));
  const [scanning, setScanning] = useState(false);
  // Sync from Lidarr is only offered when there is a Lidarr to sync from. The
  // endpoint already rejects the call with 400, but a button whose only outcome is
  // an error message is a button that should not be there.
  const managers = useFetch<Manager[]>(() => api.get("/managers"));
  const hasLidarr = (managers.data ?? []).some((m) => m.type === "lidarr" && m.enabled);
  const [adding, setAdding] = useState(false);
  const [syncAsk, setSyncAsk] = useState(false);
  const [choosingRefresh, setChoosingRefresh] = useState(false);
  const browse = useBrowse("name");

  // The queued verbs share one job runner, so the status says whether any of them
  // can be started — and keeps the three buttons honest about it.
  const status = useFetch<ScanStatus>(() => api.get("/process/status"));
  const running = status.data?.running ?? false;

  // Fetched once, the status is a snapshot from page load: four buttons would keep
  // whatever disabled state the mount happened to see, and the run bar would go on
  // claiming "Idle" through a run someone started from here. Polled only while a job
  // is in flight — the same pattern, and the same interval, as the artist page.
  useEffect(() => {
    if (!running) return;
    const t = setInterval(() => status.reload(), 3000);
    return () => clearInterval(t);
  }, [running, status.reload]);

  // Reload the artists once a run ends rather than on every poll: processing and
  // re-tagging change the ownership and coverage this whole table is drawn from.
  const wasRunning = useRef(false);
  useEffect(() => {
    if (wasRunning.current && !running) reload();
    wasRunning.current = running;
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [running]);

  // Every verb but Process reads the output of the one before it, so on a cold install
  // they answer honest zeroes that read as duds. Rather than let someone press a button
  // whose only possible outcome is "0 · 0", say what is needed first. The status is
  // fetched once on mount, so `undefined` means "not known yet" and must not disable
  // anything — only a fetched zero does.
  const indexed = status.data?.indexed;
  const noFiles = indexed === 0;
  const needsProcess = "Nothing is indexed yet — run Process to walk your libraries first.";

  // Scan answers inline (it reads the index and stats each indexed file — no
  // directory walk, no MusicBrainz), so it reports its own result rather than
  // sending the user to the Activity feed for it. An empty pass comes back with the
  // reason it found nothing, which is the part worth showing.
  const scan = async () => {
    setScanning(true);
    try {
      const r = await api.post<{
        artists: number;
        owned_release_groups: number;
        files_removed: number;
        empty_reason?: string;
      }>("/scan");
      if (r.empty_reason) {
        toast("info", `Nothing to scan — ${r.empty_reason}`);
      } else {
        // The removed count matters more than the totals when it is nonzero: it is
        // the answer to "why did this album just disappear".
        const removed = r.files_removed > 0 ? ` — ${r.files_removed} file(s) gone from disk` : "";
        toast("ok", `Scanned — ${r.artists} artists, ${r.owned_release_groups} albums${removed}`);
      }
      reload();
      status.reload();
    } catch (e) {
      toast("err", errMsg(e));
    } finally {
      setScanning(false);
    }
  };

  // The other three are queued: they report through Activity, so the toast says the
  // work started rather than what it found.
  const start = (path: string, msg: string) => async () => {
    try {
      await api.post(path);
      toast("info", msg);
      setTimeout(() => status.reload(), 300);
    } catch (e) {
      toast("err", errMsg(e));
    }
  };

  // Which reading of the refresh verb was chosen is an argument, never page state —
  // see RefreshMetadataDialog.
  const startRefresh = (force: boolean) => async () => {
    setChoosingRefresh(false);
    await start(
      `/mirror/sync${force ? "?force=true" : ""}`,
      force ? "Full metadata refresh started — cached copies ignored" : "Metadata refresh started — see Activity",
    )();
  };

  const syncLidarr = async (ignoreCache: boolean) => {
    try {
      await api.post("/collection/sync-lidarr", { ignore_cache: ignoreCache });
      toast("info", "Lidarr sync started — see Activity");
      setSyncAsk(false);
      setTimeout(reload, 3000);
    } catch (e) {
      toast("err", errMsg(e));
    }
  };

  const artists = data ?? [];
  // The first load only — the one with nothing on screen to keep. A reload has data
  // already (after a run ends, and after Scan), and replacing a populated table with
  // placeholders would be a step backwards from the wait this exists to soften: the
  // rows on screen are still true, and the ones that change are a handful.
  const first = loading && !data;
  // Mirroring cannot introduce an artist — it reads the catalogue of artists the
  // collection already has and says are Lidarr's. With none of those, the pass returns
  // before its first HTTP call, so the button's only outcome is a zero in the feed.
  const lidarrArtists = artists.filter(
    (ar) => ar.managed_by === "lidarr" || ar.managed_by === "mixed"
  ).length;
  const syncBlocked = !loading && lidarrArtists === 0;
  // A missing count only means something when something decides what is wanted:
  // the manager, or a follow that actually governs.
  const hasWanted = (ar: CollectionArtist) =>
    !ar.follow_governs || ar.monitored || (ar.picked_count ?? 0) > 0;

  const onlyMismatched = browse.flag("mismatch") === "1";
  const mismatchedCount = artists.filter((ar) => (ar.mismatch_count ?? 0) > 0).length;

  const filtered = artists.filter(
    (ar) => matches(browse.query, ar.name) && (!onlyMismatched || (ar.mismatch_count ?? 0) > 0)
  );
  const shown = useSorted(filtered, SORT[browse.sort] ?? SORT.name, browse.dir);

  // Paged after sorting, so a page is a slice of the order the user chose rather than
  // of whatever the API returned. A large collection was rendering every artist row at
  // once, and the page a row is on has to survive opening that artist and coming back —
  // hence the URL, like the rest of the browse state.
  const paging = usePaging(browse, shown.length);
  const page = shown.slice(paging.offset, paging.offset + paging.pageSize);

  return (
    <div className="stack">
      {/* The head carries the two actions that change *what the collection holds*.
          Neither queues a job, and neither is one of the four verbs — the same line
          the artist page draws when it puts Sync from Lidarr outside the group of
          four. The verbs live in the run bar below, where their shared state can be
          stated once. */}
      <div className="page-head">
        <h1>Collection</h1>
        <div className="row">
          <button className="btn btn-secondary btn-sm" onClick={() => setAdding(true)}>Add artist</button>
          {hasLidarr && (
            <button
              className="btn btn-secondary btn-sm"
              onClick={() => setSyncAsk(true)}
              disabled={syncBlocked}
              title={
                syncBlocked
                  ? artists.length === 0
                    ? "Nothing to mirror — the collection has no artists yet. Run Process, or add an artist."
                    : "Nothing to mirror — no artist in the collection is managed by Lidarr. Mirroring reads Lidarr for artists you already have; it cannot introduce one."
                  : "Mirror what Lidarr says should exist for Lidarr-managed artists. Reads Lidarr, not MusicBrainz; writes no files."
              }
            >
              Sync from Lidarr
            </button>
          )}
        </div>
      </div>

      {/* The four verbs at collection scope, the same four the artist page offers for
          one artist, in the same cheapest-first order. */}
      <RunBar
        status={status.data}
        idle={
          noFiles ? (
            "Nothing indexed yet — Process walks your libraries first."
          ) : (
            <>
              Idle
              {indexed !== undefined && (
                <>
                  {" · "}
                  <span className="mono">{indexed.toLocaleString()}</span> files indexed
                </>
              )}
            </>
          )
        }
      >
        <button
          className="btn btn-ghost btn-sm"
          onClick={scan}
          disabled={scanning || noFiles}
          title={
            noFiles
              ? `Nothing to re-derive — ${needsProcess}`
              : "Re-derive what you own from the files already indexed, dropping any that are no longer there. No directory walk, no MusicBrainz, no file writes — processing does this at the end of every run, so this is for when the view looks stale."
          }
        >
          {scanning ? "Scanning…" : "Scan"}
        </button>
        <button
          className="btn btn-ghost btn-sm"
          onClick={start("/retag", "Tagging started — see Activity")}
          disabled={running || noFiles}
          title={
            noFiles
              ? `Nothing to tag — ${needsProcess}`
              : "Rewrite the tags of every indexed file from the metadata already known. Writes tags. No disk walk, no MusicBrainz lookups."
          }
        >
          Tag files
        </button>
        <button
          className="btn btn-ghost btn-sm"
          // Same verb as the Metadata page's, so it opens the same dialog rather than
          // firing directly: `/refresh` and `/mirror/sync` are the same call, and one
          // of them offering the cache choice while the other did not is exactly the
          // drift that made these two words mean different things on different pages.
          onClick={() => setChoosingRefresh(true)}
          disabled={running}
          title="Re-read MusicBrainz for what the collection refers to. Reads only: no files are written. What changed upstream is reported, and Tag files (or the next Process) applies it."
        >
          Refresh metadata
        </button>
        {/* The one primary on the page: the full pipeline, and the only verb that
            reads the disk. Its label does not change while a job runs — the bar's
            own state says that, and an action keeps its name through the flow. */}
        <button
          className="btn btn-primary btn-sm"
          onClick={start("/process", "Processing started — see Activity")}
          disabled={running}
          title="Walk every enabled library, resolve metadata and write tags — the full pipeline. This is what finds files added, moved or changed on disk."
        >
          Process
        </button>
      </RunBar>

      <p className="muted" style={{ margin: 0, maxWidth: "70ch" }}>
        The bar is what Autotaggerr found on disk. <strong>Wanted</strong> is what you asked
        for but do not have yet — either by following an artist, or by picking individual albums.
        Where disk and manager disagree, the album is flagged as a mismatch rather than one side
        silently winning.
      </p>

      {err && <ErrorNote message={err} />}
      {!err && !loading && artists.length === 0 && (
        <EmptyState
          icon="♫"
          message="No artists yet. Process your libraries to read the files on disk — the collection is derived from what that finds."
          action={
            <button className="btn btn-primary btn-sm" onClick={start("/process", "Processing started — see Activity")} disabled={running}>
              Process
            </button>
          }
        />
      )}

      {(first || artists.length > 0) && (
        <>
          {/* The filter box is live through the wait — typing while the collection
              loads narrows the list the moment it arrives, which is the other half of
              making the page usable before it is finished. The chip and the count are
              not: "Mismatched 0" and "0 of 0" are facts, and neither is known yet. */}
          <TableToolbar
            browse={browse}
            placeholder="Filter artists"
            showing={
              first
                ? undefined
                : paging.pageCount > 1
                  ? `${paging.from}–${paging.to} of ${shown.length}`
                  : `${shown.length} of ${artists.length}`
            }
          >
            {!first && (
              <FilterChip
                on={onlyMismatched}
                count={mismatchedCount}
                label="Mismatched"
                tone="warn"
                title="Only artists where disk and manager disagree about some album"
                onClick={() => browse.setFlag("mismatch", onlyMismatched ? null : "1")}
              />
            )}
          </TableToolbar>

          {!first && shown.length === 0 ? (
            <div className="card">
              <div className="dim" style={{ fontSize: 12 }}>No artist matches this filter.</div>
            </div>
          ) : (
            <div className="tablewrap" aria-busy={first || undefined}>
              {/* The placeholder rows say "loading" to everyone who can see them and to
                  nobody who cannot, so the same fact is stated once in words here. */}
              {first && <span className="sr-only" role="status">Loading the collection…</span>}
              <table className="data">
                {/* One header for both states, so a column and its placeholder cannot
                    drift apart. Sorting stays live: a sort chosen during the wait is
                    the order the rows arrive in. */}
                <thead>
                  <tr>
                    <th style={{ width: 28 }}></th>
                    <SortHeader browse={browse} sortKey="name">Artist</SortHeader>
                    <th>Managed by</th>
                    {/* One bar replaces the four bare count columns: "9 of 12 on disk"
                        is the question those numbers were being read to answer. */}
                    <th style={{ width: 190 }}>On disk</th>
                    <SortHeader browse={browse} sortKey="missing" align="right" defaultDir="desc">Missing</SortHeader>
                    <SortHeader browse={browse} sortKey="mismatch" align="right" defaultDir="desc">Mismatch</SortHeader>
                    <th>Wanted</th>
                  </tr>
                </thead>
                {first && <LoadingRows />}
                <tbody>
                  {page.map((ar) => {
                    const owned = ar.owned_count ?? 0;
                    const complete = ar.complete_count ?? 0;
                    const partial = ar.partial_count ?? 0;
                    const total = owned + (hasWanted(ar) ? ar.missing_count ?? 0 : 0);
                    return (
                      <tr key={ar.mb_id}>
                        <td>
                          <Artwork entity="artist" mbid={ar.mb_id} name={ar.name} px={24} />
                        </td>
                        <td style={{ color: "var(--text)" }}>
                          <div className="row" style={{ gap: 8 }}>
                            <Link to={`/collection/${ar.mb_id}`} style={{ color: "var(--text)" }}>{ar.name}</Link>
                            {ar.origin === "manual" && (
                              <span className="dim" style={{ fontSize: 11 }} title="Added by hand; no files owned yet">added</span>
                            )}
                            <MBLink entity="artist" mbid={ar.mb_id} />
                          </div>
                        </td>
                        <td><ManagedBy managed_by={ar.managed_by} /></td>
                        <td>
                          <div className="row" style={{ gap: 8 }}>
                            {/* Proportional down the whole column, whatever the count:
                                one shape all the way down a page of artists is worth
                                more than the cell count on the short rows, and the
                                number beside it already answers "how many". */}
                            <CoverageBar
                              total={total}
                              owned={complete}
                              partial={partial}
                              label={`${ar.name} albums`}
                              width={120}
                              proportional
                            />
                            <span className="mono" style={{ fontSize: 11, color: "var(--text-muted)" }}>
                              {owned}
                              {total > owned && <span className="dim">/{total}</span>}
                            </span>
                          </div>
                        </td>
                        <td className="num" style={{ color: (ar.missing_count ?? 0) > 0 ? "var(--accent-text)" : "var(--text-dim)" }}>
                          {hasWanted(ar) ? (ar.missing_count ?? 0) : "—"}
                        </td>
                        <td
                          className="num"
                          style={{ color: (ar.mismatch_count ?? 0) > 0 ? "var(--warning-text)" : "var(--text-dim)" }}
                          title={(ar.mismatch_count ?? 0) > 0 ? "Disk and manager disagree on some albums — open the artist for details" : undefined}
                        >
                          {(ar.mismatch_count ?? 0) > 0 ? ar.mismatch_count : "—"}
                        </td>
                        <td><WantedSummary artist={ar} /></td>
                      </tr>
                    );
                  })}
                </tbody>
              </table>
            </div>
          )}

          <Pager paging={paging} unit="artists" />
        </>
      )}

      {choosingRefresh && (
        <RefreshMetadataDialog
          onCancel={() => setChoosingRefresh(false)}
          onRefresh={startRefresh(false)}
          onForce={startRefresh(true)}
        />
      )}
      {adding && <AddArtistModal onClose={() => setAdding(false)} onAdded={() => { setAdding(false); reload(); }} />}
      {syncAsk && (
        <SyncLidarrDialog
          scope="every Lidarr-managed artist"
          onConfirm={syncLidarr}
          onCancel={() => setSyncAsk(false)}
        />
      )}
    </div>
  );
}
