import { useState } from "react";
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
  const browse = useBrowse("name");

  // The queued verbs share one job runner, so the status says whether any of them
  // can be started — and keeps the three buttons honest about it.
  const status = useFetch<ScanStatus>(() => api.get("/process/status"));
  const running = status.data?.running ?? false;

  // Scan answers inline (it only reads the index), so it reports its own result
  // rather than sending the user to the Activity feed for it.
  const scan = async () => {
    setScanning(true);
    try {
      const r = await api.post<{ artists: number; owned_release_groups: number }>("/scan");
      toast("ok", `Scanned — ${r.artists} artists, ${r.owned_release_groups} albums`);
      reload();
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

  const syncLidarr = async () => {
    try {
      await api.post("/collection/sync-lidarr");
      toast("info", "Lidarr sync started — see Activity");
      setTimeout(reload, 3000);
    } catch (e) {
      toast("err", errMsg(e));
    }
  };

  const artists = data ?? [];
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
      <div className="page-head">
        <h1>Collection</h1>
        <div className="row">
          <button className="btn btn-primary btn-sm" onClick={() => setAdding(true)}>Add artist</button>
          {hasLidarr && (
            <button
              className="btn btn-secondary btn-sm"
              onClick={syncLidarr}
              title="Mirror what Lidarr says should exist for Lidarr-managed artists. Reads Lidarr, not MusicBrainz; writes no files."
            >
              Sync from Lidarr
            </button>
          )}
          {/* The four verbs at collection scope, the same four the artist page
              offers for one artist, in the same cheapest-first order. */}
          <span className="sep">·</span>
          <button
            className="btn btn-secondary btn-sm"
            onClick={scan}
            disabled={scanning}
            title="Re-derive what you own from the files already indexed. No disk walk, no MusicBrainz, no file writes — processing does this at the end of every run, so this is for when the view looks stale."
          >
            {scanning ? "Scanning…" : "Scan"}
          </button>
          <button
            className="btn btn-secondary btn-sm"
            onClick={start("/retag", "Tagging started — see Activity")}
            disabled={running}
            title="Rewrite the tags of every indexed file from the metadata already known. Writes tags. No disk walk, no MusicBrainz lookups."
          >
            Tag files
          </button>
          <button
            className="btn btn-secondary btn-sm"
            onClick={start("/refresh", "Metadata refresh started — see Activity")}
            disabled={running}
            title="Re-read MusicBrainz for everything that is due a check. Reads only: no files are written. What changed upstream is reported, and Tag files (or the next Process) applies it."
          >
            Refresh metadata
          </button>
          <button
            className="btn btn-primary btn-sm"
            onClick={start("/process", "Processing started — see Activity")}
            disabled={running}
            title="Walk every enabled library, resolve metadata and write tags — the full pipeline. This is what finds files added, moved or changed on disk."
          >
            {running ? "Working…" : "Process"}
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
                            <CoverageBar
                              total={total}
                              owned={complete}
                              partial={partial}
                              label={`${ar.name} albums`}
                              width={90}
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
    </div>
  );
}
