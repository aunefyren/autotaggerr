import { useEffect, useRef, useState } from "react";
import { Link } from "react-router-dom";
import { api, errMsg } from "../api";
import { useFetch } from "../hooks";
import { CollectionArtist, Manager, ScanStatus } from "../types";
import { EmptyState, ErrorNote, Pill } from "../components/ui";
import { useToast } from "../toast";
import { MBLink } from "../components/MBLink";
import { AddArtistModal } from "../components/AddArtistModal";
import { Artwork } from "../components/Artwork";
import { CoverageBar } from "../components/CoverageBar";
import { ProgressBar } from "../components/ProgressBar";
import { PHASE_LABELS, phaseDrivesProgress } from "../components/phases";
import { SyncLidarrDialog } from "../components/SyncLidarrDialog";
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

  // Whether the running job's counters describe the stage it is actually in. A run
  // counts files, and only its walk moves that number, so the bar goes striped rather
  // than sitting frozen at 0% through minutes of rate-limited metadata work.
  const counted = phaseDrivesProgress(status.data?.phase);

  // Scan answers inline (it only reads the index), so it reports its own result
  // rather than sending the user to the Activity feed for it. An empty pass comes back
  // with the reason it found nothing, which is the part worth showing.
  const scan = async () => {
    setScanning(true);
    try {
      const r = await api.post<{
        artists: number;
        owned_release_groups: number;
        empty_reason?: string;
      }>("/scan");
      if (r.empty_reason) {
        toast("info", `Nothing to scan — ${r.empty_reason}`);
      } else {
        toast("ok", `Scanned — ${r.artists} artists, ${r.owned_release_groups} albums`);
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
          one artist, in the same cheapest-first order — and the one status all four
          share, because one serial job queue drains them. Buttons here dim for two
          different reasons (a job is running, or nothing is indexed yet) and the two
          are indistinguishable in a disabled button, so the bar says which it is in
          words instead of leaving it to four tooltips. */}
      <div className="runbar">
        <div className="runbar-state">
          <span className="eyebrow">Run</span>
          {running ? (
            <>
              {/* The job's own title and stage, in the same words the Activity banner
                  uses — a run reads the same wherever it is reported. Naming the job
                  rather than saying "working" is what makes the dimmed buttons legible:
                  it is *that* run holding them. */}
              <Link to="/activity" className="runbar-status" title="A job is running — open Activity">
                {status.data?.current_job?.title ?? "Working…"}
              </Link>
              {status.data?.phase && (
                <span className="dim" style={{ fontSize: 11 }}>
                  {PHASE_LABELS[status.data.phase] ?? status.data.phase}
                </span>
              )}
              {/* Indeterminate outside the walk: these counters are files, and the
                  stages either side of it count something else entirely. */}
              {(!counted || (status.data?.total ?? 0) > 0) && (
                <ProgressBar
                  done={status.data?.done ?? 0}
                  total={status.data?.total ?? 0}
                  width={120}
                  showPercent={false}
                  indeterminate={!counted}
                />
              )}
              {counted && (status.data?.total ?? 0) > 0 && (
                <span className="mono dim" style={{ fontSize: 11 }}>
                  {status.data?.done ?? 0} / {status.data?.total}
                </span>
              )}
            </>
          ) : noFiles ? (
            <span className="runbar-status">Nothing indexed yet — Process walks your libraries first.</span>
          ) : (
            <span className="runbar-status">
              Idle
              {indexed !== undefined && (
                <>
                  {" · "}
                  <span className="mono">{indexed.toLocaleString()}</span> files indexed
                </>
              )}
            </span>
          )}
        </div>
        <div className="runbar-verbs">
          <button
            className="btn btn-ghost btn-sm"
            onClick={scan}
            disabled={scanning || noFiles}
            title={
              noFiles
                ? `Nothing to re-derive — ${needsProcess}`
                : "Re-derive what you own from the files already indexed. No disk walk, no MusicBrainz, no file writes — processing does this at the end of every run, so this is for when the view looks stale."
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
            onClick={start("/refresh", "Metadata refresh started — see Activity")}
            disabled={running}
            title="Re-read MusicBrainz for everything that is due a check. Reads only: no files are written. What changed upstream is reported, and Tag files (or the next Process) applies it."
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
        </div>
      </div>

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

      {artists.length > 0 && (
        <>
          <TableToolbar
            browse={browse}
            placeholder="Filter artists"
            showing={
              paging.pageCount > 1
                ? `${paging.from}–${paging.to} of ${shown.length}`
                : `${shown.length} of ${artists.length}`
            }
          >
            <FilterChip
              on={onlyMismatched}
              count={mismatchedCount}
              label="Mismatched"
              tone="warn"
              title="Only artists where disk and manager disagree about some album"
              onClick={() => browse.setFlag("mismatch", onlyMismatched ? null : "1")}
            />
          </TableToolbar>

          {shown.length === 0 ? (
            <div className="card">
              <div className="dim" style={{ fontSize: 12 }}>No artist matches this filter.</div>
            </div>
          ) : (
            <div className="tablewrap">
              <table className="data">
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
