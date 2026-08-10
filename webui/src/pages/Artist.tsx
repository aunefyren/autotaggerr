import { useEffect, useRef, useState } from "react";
import { Link, useParams } from "react-router-dom";
import { api, errMsg } from "../api";
import { useFetch } from "../hooks";
import { ArtistDetail, ArtistInfo, CollectionArtist, CollectionReleaseGroup, Manager, ScanStatus } from "../types";
import { ConfirmDialog, ErrorNote, Pill } from "../components/ui";
import { MBLink } from "../components/MBLink";
import { useToast } from "../toast";
import { SyncLidarrDialog } from "../components/SyncLidarrDialog";
import { Artwork, ArtistBackdrop } from "../components/Artwork";
import { CoverageBar, DiskMarker } from "../components/CoverageBar";
import { ProgressBar } from "../components/ProgressBar";
import { phaseDrivesProgress } from "../components/phases";
import {
  FilterChip,
  SortHeader,
  TableToolbar,
  matches,
  sortRows,
  useBrowse,
} from "../components/browse";

function ManagedBy({ artist }: { artist: CollectionArtist }) {
  // Detached reads as its own state rather than plain "Native". Both mean Autotaggerr
  // decides, but only one of them is a decision *someone made* — and the artist's
  // files still sit in a managed library, which the plain label would deny.
  if (artist.manager_detached)
    return (
      <Pill kind="chg">
        <span title="Detached from its manager: Autotaggerr decides what is wanted for this artist, even though its files live in a managed library.">
          Native · detached
        </span>
      </Pill>
    );
  if (artist.managed_by === "lidarr") return <Pill kind="scan">Lidarr</Pill>;
  if (artist.managed_by === "mixed") return <Pill kind="scan">Lidarr + native</Pill>;
  if (artist.managed_by === "autotaggerr") return <Pill kind="chg">Native</Pill>;
  return (
    <Pill kind="off">
      <span title="This artist's library has no resolvable manager — reassign one on the Libraries page">Unknown</span>
    </Pill>
  );
}

/** How a disk-vs-catalog disagreement reads. The disk count is the one to trust. */
function discrepancyNote(g: CollectionReleaseGroup, manager: string): { label: string; title: string } | null {
  const cat = `${g.catalog_owned_tracks}/${g.catalog_total_tracks}`;
  // Checked first: when no edition is selected, the counts below describe an edition
  // nobody chose, so reading them as "the manager is out of date" sends the user to
  // rescan something that will report the same numbers again.
  if (g.discrepancy === "no_edition")
    return {
      label: `no edition in ${manager}`,
      title:
        `${manager} has no edition selected for this album, so its ${cat} count describes an edition nobody chose ` +
        `(${g.owned_tracks} tracks are on disk). Pick a release for this album in ${manager} — until then its files cannot be matched.`,
    };
  if (g.discrepancy === "stale_catalog")
    return {
      label: `${manager} ${cat}`,
      title: `${manager} reports ${cat} files, but ${g.owned_tracks} are on disk. ${manager} probably needs a rescan.`,
    };
  if (g.discrepancy === "not_indexed")
    return {
      label: `${manager} ${cat}`,
      title: `${manager} reports ${cat} files, but only ${g.owned_tracks} are indexed here — they may sit outside your configured libraries, or have not been scanned yet.`,
    };
  if (g.discrepancy === "unmapped")
    return {
      label: `not in ${manager}`,
      title: `These files are on disk but ${manager} has no matching album, so it is not tracking them.`,
    };
  return null;
}

/**
 * The catalog sections. A discography is not one list — an artist with six albums
 * and ninety singles reads as ninety-six identical rows unless the type is used for
 * what it is: structure.
 *
 * Anything carrying a secondary type (live, compilation, remix, soundtrack) lands
 * in "Other" rather than under Albums. That matches what following already means
 * by an album, and it keeps a reissue-heavy catalogue from burying the six records
 * a person actually thinks of as the discography.
 */
type Category = "album" | "ep" | "single" | "other";

const CATEGORIES: { id: Category; label: string; hint: string }[] = [
  { id: "album", label: "Albums", hint: "Studio albums" },
  { id: "ep", label: "EPs", hint: "EPs" },
  { id: "single", label: "Singles", hint: "Singles" },
  { id: "other", label: "Other", hint: "Live albums, compilations, remixes, soundtracks and broadcasts" },
];

/** Sections that start closed: numerous, and rarely what you opened the page for. */
const CLOSED_BY_DEFAULT = "single,other";

function category(g: CollectionReleaseGroup): Category {
  if ((g.secondary_types || "").trim() !== "") return "other";
  switch (g.primary_type) {
    case "Album":
      return "album";
    case "EP":
      return "ep";
    case "Single":
      return "single";
    default:
      return "other";
  }
}

const year = (g: CollectionReleaseGroup) => (g.first_release_date || "").slice(0, 4);

const SORT: Record<string, (g: CollectionReleaseGroup) => string | number> = {
  year: (g) => year(g),
  title: (g) => g.title,
  // Ordered by how complete it is, so "nearly there" and "nothing yet" separate.
  tracks: (g) => (g.total_tracks > 0 ? g.owned_tracks / g.total_tracks : -1),
};

type Filter = "all" | "disk" | "partial" | "wanted" | "missing" | "mismatch";

export default function Artist() {
  const { mbid = "" } = useParams();
  const toast = useToast();

  const detail = useFetch<ArtistDetail>(() => api.get(`/artists/${mbid}`), [mbid]);
  // Live MusicBrainz read: browsing a catalogue is not the same as wanting it, so
  // the discography is never persisted.
  const disco = useFetch<CollectionReleaseGroup[]>(() => api.get(`/artists/${mbid}/discography`), [mbid]);
  // Its own request so the page never waits on a rate-limited external call for
  // decoration. Failure is silent: the header simply shows less.
  const info = useFetch<ArtistInfo>(() => api.get(`/artists/${mbid}/info`), [mbid]);

  const [busy, setBusy] = useState(false);
  const [settingsOpen, setSettingsOpen] = useState(false);
  // Which authority dialog is open, if any. One piece of state rather than two
  // booleans: detaching and reattaching are opposite answers to one question, and
  // both being open at once is not a state that should be representable.
  const [authorityAsk, setAuthorityAsk] = useState<"detach" | "reattach" | null>(null);
  const browse = useBrowse("year", "desc");

  const artist = detail.data?.artist;
  const groups = disco.data ?? detail.data?.release_groups ?? [];

  const onDisk = groups.filter((g) => g.owned);
  const partial = onDisk.filter((g) => !g.complete);
  const wanted = groups.filter((g) => g.wanted);
  const missing = groups.filter((g) => g.wanted && !g.owned);
  const mismatched = groups.filter((g) => g.discrepancy !== "");

  const isLidarr = artist?.managed_by === "lidarr" || artist?.managed_by === "mixed";
  const managerLabel = isLidarr ? "Lidarr" : "MusicBrainz";

  // Two conditions, both required, and they are not the same fact: this artist has to
  // be Lidarr's to answer for, *and* there has to be an enabled Lidarr to ask. A row
  // can still read "managed by Lidarr" after its manager was disabled or removed, and
  // the endpoint rejects that with a 400 — a button whose only outcome is an error
  // message is a button that should not be there. Same rule as the Collection page.
  const managers = useFetch<Manager[]>(() => api.get("/managers"));
  const canSyncLidarr = isLidarr && (managers.data ?? []).some((m) => m.type === "lidarr" && m.enabled);
  const [syncAsk, setSyncAsk] = useState(false);

  const refresh = () => { detail.reload(); disco.reload(); };

  // The queued per-artist actions run on the same single-run guard as a full run, so
  // the global job status is what says whether they can be started — and polling it
  // is what turns a fire-and-forget POST into visible progress.
  const status = useFetch<ScanStatus>(() => api.get("/process/status"));
  const running = status.data?.running ?? false;

  useEffect(() => {
    if (!running) return;
    const t = setInterval(() => status.reload(), 3000);
    return () => clearInterval(t);
  }, [running, status.reload]);

  // Reload once a run finishes rather than on every poll: processing or re-tagging
  // changes the coverage and ownership this whole page is built from.
  const wasRunning = useRef(false);
  useEffect(() => {
    if (wasRunning.current && !running) refresh();
    wasRunning.current = running;
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [running]);

  const action = (path: string, started: string) => async () => {
    try {
      await api.post(`/artists/${mbid}/${path}`);
      toast("info", started);
      setTimeout(() => status.reload(), 300);
    } catch (e) {
      toast("err", errMsg(e));
    }
  };

  // Not one of the four verbs: it reads Lidarr rather than the files, so it neither
  // queues on the job runner nor is disabled while one is running. It reports through
  // Activity like the collection-wide sync does.
  const syncLidarr = async (ignoreCache: boolean) => {
    try {
      await api.post(`/artists/${mbid}/sync-lidarr`, { ignore_cache: ignoreCache });
      toast("info", "Lidarr sync started — see Activity");
      setSyncAsk(false);
      setTimeout(refresh, 3000);
    } catch (e) {
      toast("err", errMsg(e));
    }
  };

  // Scan is the odd one of the four: it is a database pass, so it answers with what
  // it found instead of queueing. Reporting the counts is what makes pressing it
  // feel like it did something, since nothing appears in the Activity feed.
  const scan = async () => {
    try {
      const r = await api.post<{ owned_release_groups: number }>(`/artists/${mbid}/scan`);
      toast("ok", `Scanned — ${r.owned_release_groups} album(s) on disk`);
      refresh();
    } catch (e) {
      toast("err", errMsg(e));
    }
  };

  const detach = async () => {
    setBusy(true);
    try {
      const r = await api.post<{ wants_kept: number; follow_cleared: boolean }>(`/artists/${mbid}/detach`);
      // The counts are the reassurance: the whole promise of detaching is that the
      // manager's decisions survive it, so the toast says how many did.
      const kept = r.wants_kept === 1 ? "1 want kept" : `${r.wants_kept} wants kept`;
      toast("info", r.follow_cleared ? `Detached — ${kept}, following switched off` : `Detached — ${kept}`);
      setAuthorityAsk(null);
      refresh();
    } catch (e) {
      toast("err", errMsg(e));
    } finally {
      setBusy(false);
    }
  };

  const reattach = async () => {
    setBusy(true);
    try {
      await api.del(`/artists/${mbid}/detach`);
      toast("info", "Reattached — the manager decides what is wanted again");
      setAuthorityAsk(null);
      refresh();
    } catch (e) {
      toast("err", errMsg(e));
    } finally {
      setBusy(false);
    }
  };

  const updateFollow = async (body: Record<string, unknown>) => {
    setBusy(true);
    try {
      await api.post(`/artists/${mbid}/follow`, body);
      refresh();
    } catch (e) {
      toast("err", errMsg(e));
    } finally {
      setBusy(false);
    }
  };

  const filter = (browse.flag("show") as Filter) || "all";
  const setFilter = (next: Filter) => browse.setFlag("show", next === "all" ? null : next);

  const shown = groups.filter((g) => {
    if (!matches(browse.query, g.title, g.primary_type, g.secondary_types, year(g))) return false;
    switch (filter) {
      case "disk": return g.owned;
      case "partial": return g.owned && !g.complete;
      case "wanted": return g.wanted;
      case "missing": return g.wanted && !g.owned;
      case "mismatch": return g.discrepancy !== "";
      default: return true;
    }
  });

  // Closed sections live in the URL like the rest of the browsing state. "-" means
  // "every section closed", which an empty string cannot express — it would read as
  // "unset" and spring the defaults back open.
  const closedRaw = browse.flag("closed") ?? CLOSED_BY_DEFAULT;
  const closed = new Set(closedRaw === "-" ? [] : closedRaw.split(",").filter(Boolean));
  const toggleSection = (id: Category) => {
    const next = new Set(closed);
    if (next.has(id)) next.delete(id);
    else next.add(id);
    browse.setFlag("closed", next.size === 0 ? "-" : [...next].join(","));
  };

  if (detail.err) return <ErrorNote message={detail.err} />;

  const facts = info.data ?? undefined;
  const catalogTotal = onDisk.length + missing.length;

  return (
    <div className="stack">
      <div>
        <Link to="/collection" className="dim mono" style={{ fontSize: 11 }}>← Collection</Link>
      </div>

      <div className="entity-head">
        <ArtistBackdrop mbid={mbid} />
        <Artwork entity="artist" mbid={mbid} name={artist?.name ?? "Artist"} px={96} size={500} className="artwork-lg" />
        <div className="entity-body">
          {/* Eyebrow: what kind of act, from where, active when. None of it is on
              disk — it is why the page opens with facts instead of a bare name. */}
          <div className="eyebrow">
            {[
              facts?.genres?.[0],
              facts?.type,
              facts?.area || facts?.country,
              lifeSpan(facts),
            ].filter(Boolean).join(" · ") || "Artist"}
          </div>
          <div className="entity-title">
            <h1>{artist?.name ?? "Artist"}</h1>
            {facts?.disambiguation && (
              <span className="muted" style={{ fontSize: 12 }}>({facts.disambiguation})</span>
            )}
          </div>

          <div className="entity-coverage">
            <CoverageBar
              total={catalogTotal}
              owned={onDisk.length - partial.length}
              partial={partial.length}
              label="Albums"
              width={220}
            />
            <span className="cov-label">
              {onDisk.length} of {catalogTotal || onDisk.length} on disk
            </span>
          </div>

          <div className="entity-meta">
            {artist && <ManagedBy artist={artist} />}
            {artist?.origin === "manual" && (
              <span className="dim" style={{ fontSize: 11 }} title="Added by hand">added</span>
            )}
            {(facts?.genres?.length ?? 0) > 1 && (
              <>
                <span className="sep">·</span>
                <span className="dim" style={{ fontSize: 11 }}>{facts?.genres?.slice(1).join(", ")}</span>
              </>
            )}
            <span className="sep">·</span>
            <MBLink entity="artist" mbid={mbid} />
          </div>
        </div>

        {/* Following is state, not a command, and it stays on the page even when a
            manager owns the artist — where it renders frozen. Hiding it is what
            once left albums marked "auto" with nothing on the page to explain
            them. */}
        {artist && (
          <div className="entity-actions">
            {/* Why the controls beside this are frozen, stated rather than left to a
                tooltip. A disabled toggle whose cause is only in its title is state
                the page has but does not show — the same failure the "Following"
                label fixed for the toggle itself. It sits here, not only inside the
                Settings disclosure, because that disclosure starts closed. */}
            {!artist.follow_governs && <Pill kind="off">Managed by {managerLabel}</Pill>}
            <button
              className={artist.monitored ? "btn btn-primary btn-sm" : "btn btn-secondary btn-sm"}
              aria-pressed={artist.monitored}
              disabled={busy || !artist.follow_governs}
              title={
                !artist.follow_governs
                  ? `${managerLabel} decides what is wanted for this artist — following has no effect until it is managed natively.`
                  : artist.monitored
                    ? "Following. Click to stop."
                    : "Not following. Click to follow."
              }
              onClick={() => updateFollow({ monitored: !artist.monitored })}
            >
              {busy ? "Syncing…" : artist.monitored ? "Following" : "Follow"}
            </button>
            <button
              className="btn btn-ghost btn-sm"
              aria-expanded={settingsOpen}
              onClick={() => setSettingsOpen((v) => !v)}
            >
              {settingsOpen ? "Hide settings" : "Settings"}
            </button>

            {/* The four verbs, scoped to this artist so none of them costs a
                collection-wide pass. Ordered cheapest first by what they touch: the
                database, then the files, then MusicBrainz, then the disk.

                None of them cascades into another. Each does exactly what its label
                says and stops, because two of the four rewrite the user's audio
                files and a button that quietly does more than it claims is the wrong
                place to be clever. Refresh metadata reports what changed upstream;
                acting on it is Tag files, or the next Process. */}
            <span className="sep">·</span>
            <button
              className="btn btn-ghost btn-sm"
              disabled={running}
              title="Re-derive this artist's albums from the files already indexed. No disk walk, no MusicBrainz, no file writes — press this when the albums shown here look out of date."
              onClick={scan}
            >
              Scan
            </button>
            <button
              className="btn btn-ghost btn-sm"
              disabled={running}
              title="Rewrite the tags of this artist's indexed files from the metadata already known. Writes tags. No disk walk, no MusicBrainz lookups."
              onClick={action("retag", "Tagging started")}
            >
              Tag files
            </button>
            <button
              className="btn btn-ghost btn-sm"
              disabled={running}
              title="Re-read this artist from MusicBrainz — who they are, their discography, every edition and release — ignoring the cache. Reads only: no files are written. If anything changed upstream it is reported, and Tag files (or the next Process) applies it."
              onClick={action("refresh", "Metadata refresh started")}
            >
              Refresh metadata
            </button>
            <button
              className="btn btn-ghost btn-sm"
              disabled={running}
              title="Walk this artist's folders, resolve their metadata and write tags — the full pipeline, narrowed to this artist. Finds files that were added, moved or changed on disk. Writes tags."
              onClick={action("process", "Processing started")}
            >
              Process
            </button>
            {/* Outside the group of four on purpose: those four are Autotaggerr acting
                on this artist's files and metadata, and this one only re-reads what the
                manager says. It is also the only action here that does not queue, so it
                stays available while a job runs. */}
            {canSyncLidarr && (
              <>
                <span className="sep">·</span>
                <button
                  className="btn btn-ghost btn-sm"
                  title="Mirror what Lidarr says should exist for this artist — which albums it tracks, monitors, and how many files it has of each. Reads Lidarr, not MusicBrainz; writes no files."
                  onClick={() => setSyncAsk(true)}
                >
                  Sync from Lidarr
                </button>
              </>
            )}
            {running && (
              <span className="row" style={{ gap: 8, alignItems: "center" }}>
                <Link to="/activity" className="dim mono" style={{ fontSize: 11 }} title="A job is running">
                  Working…
                </Link>
                {/* Indeterminate outside the walk: these counters are files, and the
                    stages either side of it count something else. */}
                {(!phaseDrivesProgress(status.data?.phase) || (status.data?.total ?? 0) > 0) && (
                  <ProgressBar
                    done={status.data?.done ?? 0}
                    total={status.data?.total ?? 0}
                    width={100}
                    indeterminate={!phaseDrivesProgress(status.data?.phase)}
                  />
                )}
              </span>
            )}
          </div>
        )}
      </div>

      {artist && settingsOpen && (
        <>
          <FollowSettings artist={artist} busy={busy} onChange={updateFollow} manager={managerLabel} />
          {/* Authority sits under the follow settings because it is the question
              those settings depend on: they are frozen above until this is answered
              natively. Inside the disclosure, not the header — it is a decision you
              make once, and a permanently visible "Detach" beside Process and Tag files
              would read as an everyday command. */}
          <ManagerAuthority
            artist={artist}
            busy={busy}
            manager={managerLabel}
            onDetach={() => setAuthorityAsk("detach")}
            onReattach={() => setAuthorityAsk("reattach")}
          />
        </>
      )}

      {authorityAsk === "detach" && (
        <ConfirmDialog
          title="Detach this artist from its manager?"
          confirmLabel="Detach"
          busy={busy}
          onCancel={() => setAuthorityAsk(null)}
          onConfirm={detach}
          body={
            <>
              <p>
                Autotaggerr takes over deciding what is wanted for this artist. Nothing on disk
                changes, and nothing is removed from {managerLabel}.
              </p>
              <p>
                <strong>What {managerLabel} already decided is kept.</strong> Every album it monitors
                for this artist becomes your own want, on the same edition, so the list above reads
                the same afterwards — it just stops being {managerLabel}&apos;s to change.
              </p>
              <p>
                Following is switched off, so detaching cannot quietly start wanting the whole back
                catalogue. Turn it on above if you want that. {managerLabel} stops being asked about
                this artist at all, so its albums will no longer appear or disappear on their own.
              </p>
              <p className="dim">You can hand the artist back at any time.</p>
            </>
          }
        />
      )}

      {authorityAsk === "reattach" && (
        <ConfirmDialog
          title="Hand this artist back to its manager?"
          confirmLabel="Reattach"
          busy={busy}
          onCancel={() => setAuthorityAsk(null)}
          onConfirm={reattach}
          body={
            <>
              <p>
                Whatever manages this artist&apos;s libraries decides what is wanted for it again,
                and the follow settings above stop applying.
              </p>
              <p>
                The wants detaching kept stay yours — they are not handed back with the artist. The
                manager leaves those albums alone, because an album you have asked for explicitly is
                never something it overrides.
              </p>
            </>
          }
        />
      )}

      {syncAsk && (
        <SyncLidarrDialog
          scope={artist ? artist.name : "this artist"}
          onConfirm={syncLidarr}
          onCancel={() => setSyncAsk(false)}
        />
      )}

      <TableToolbar
        browse={browse}
        placeholder="Filter releases"
        showing={`${shown.length} of ${groups.length}`}
      >
        <FilterChip on={filter === "disk"} count={onDisk.length} label="On disk"
          title="Albums with at least one file on disk"
          onClick={() => setFilter(filter === "disk" ? "all" : "disk")} />
        <FilterChip on={filter === "partial"} count={partial.length} label="Partial" tone="warn"
          title="Albums where some tracks are missing from disk"
          onClick={() => setFilter(filter === "partial" ? "all" : "partial")} />
        <FilterChip on={filter === "wanted"} count={wanted.length} label="Wanted"
          title="Albums you asked for, whether or not you have them"
          onClick={() => setFilter(filter === "wanted" ? "all" : "wanted")} />
        <FilterChip on={filter === "missing"} count={missing.length} label="Missing"
          title="Wanted, and nothing on disk yet"
          onClick={() => setFilter(filter === "missing" ? "all" : "missing")} />
        <FilterChip on={filter === "mismatch"} count={mismatched.length} label="Mismatch" tone="warn"
          title="Disk and manager disagree about these albums"
          onClick={() => setFilter(filter === "mismatch" ? "all" : "mismatch")} />
      </TableToolbar>

      {disco.loading && <span className="dim" style={{ fontSize: 11 }}>Loading the catalogue from MusicBrainz…</span>}
      {disco.err && <span className="dim" style={{ fontSize: 11 }}>MusicBrainz unreachable — showing what is already known.</span>}

      {shown.length === 0 ? (
        <div className="card">
          <div className="dim" style={{ fontSize: 12 }}>
            {groups.length === 0 ? "Nothing to show yet." : "Nothing matches this filter."}
          </div>
        </div>
      ) : (
        <div className="tablewrap">
          {/* One table, one header, a tbody per section. Separate tables per section
              would each size their own columns, so Year and Tracks would not line up
              between Albums and Singles — and a column you cannot read down is not
              worth having. */}
          <table className="data">
            <thead>
              <tr>
                <th style={{ width: 38 }}></th>
                <th style={{ width: 22 }}></th>
                <SortHeader browse={browse} sortKey="title">Release</SortHeader>
                <th>Type</th>
                <SortHeader browse={browse} sortKey="year" align="right" defaultDir="desc">Year</SortHeader>
                <SortHeader browse={browse} sortKey="tracks" align="right" defaultDir="desc">Tracks</SortHeader>
                <th>Wanted</th>
                <th style={{ textAlign: "right" }}>Actions</th>
              </tr>
            </thead>
            {CATEGORIES.map(({ id, label, hint }) => {
              const rows = sortRows(shown.filter((g) => category(g) === id), SORT[browse.sort] ?? SORT.year, browse.dir);
              if (rows.length === 0) return null;
              const isClosed = closed.has(id);
              const owned = rows.filter((g) => g.owned);
              return (
                <tbody key={id}>
                  <tr className="group-row">
                    <td colSpan={8}>
                      <button
                        type="button"
                        className="group-head"
                        aria-expanded={!isClosed}
                        title={hint}
                        onClick={() => toggleSection(id)}
                      >
                        <span className="twisty">{isClosed ? "▶" : "▼"}</span>
                        {label}
                        <span className="group-n">{rows.length}</span>
                        <span className="group-cov">
                          <CoverageBar
                            total={rows.length}
                            owned={owned.filter((g) => g.complete).length}
                            partial={owned.filter((g) => !g.complete).length}
                            label={label}
                          />
                        </span>
                      </button>
                    </td>
                  </tr>
                  {!isClosed &&
                    rows.map((g) => (
                      <ReleaseGroupRow
                        key={g.mb_id}
                        g={g}
                        manager={managerLabel}
                        managerGoverns={isLidarr}
                        artistMbid={mbid}
                        onChanged={refresh}
                      />
                    ))}
                </tbody>
              );
            })}
          </table>
        </div>
      )}
    </div>
  );
}

/** "1976–1996", "1976–", or nothing. A dash with no end date means still active. */
function lifeSpan(info?: ArtistInfo): string {
  if (!info?.begin) return "";
  const begin = info.begin.slice(0, 4);
  if (info.end) return `${begin}–${info.end.slice(0, 4)}`;
  return info.ended ? begin : `${begin}–`;
}

function ReleaseGroupRow({
  g,
  manager,
  managerGoverns,
  artistMbid,
  onChanged,
}: {
  g: CollectionReleaseGroup;
  manager: string;
  /** Whether a manager still decides for this artist — see `stale` below. */
  managerGoverns: boolean;
  artistMbid: string;
  onChanged: () => void;
}) {
  const toast = useToast();
  const [busy, setBusy] = useState(false);

  const partial = g.owned && !g.complete;
  const note = discrepancyNote(g, manager);
  const explicitlyWanted = g.wanted && g.wanted_source === "explicit";
  // A want the row cannot edit: it follows from following the artist, or from the
  // manager. Rendering it as a pressed toggle claimed a control that does nothing —
  // clicking never un-wanted it, because the reason lives elsewhere.
  const derivedWant = g.wanted && !explicitlyWanted;
  // Stronger than derived, and true whether or not the album is wanted: a manager
  // owns identity for this artist, so every write here is a 409. An album Lidarr
  // does *not* monitor used to show a live "Want" button that only failed on click.
  const locked = !g.identity_editable;

  // A mirrored want whose authority no longer governs the artist: the manager was
  // deleted, or the artist was detached before this row was re-labelled. Nothing
  // reconciles it — the Lidarr sync returns early when no manager is configured, and
  // that is deliberate, since it cannot tell "unmonitored" from "gone" — so without an
  // affordance here the row is permanently stuck: derived, so the toggle is frozen,
  // and pointing at an authority that cannot be asked to change its mind.
  const stale = derivedWant && g.wanted_source === "manager" && !managerGoverns;

  // What was actually asked for, in plain words — a refined want should never be a
  // mystery from the list.
  const wantSummary = (() => {
    if (!g.wanted || derivedWant) return "";
    const editions = g.desired_releases?.length ?? 0;
    const tracks = g.desired_recordings?.length ?? 0;
    const where = editions > 0 ? `${editions} edition${editions === 1 ? "" : "s"}` : "any edition";
    const what = tracks > 0 ? `${tracks} track${tracks === 1 ? "" : "s"}` : "whole album";
    return `${where} · ${what}`;
  })();

  /**
   * Turns a derived want into an explicit one, so it survives unfollowing or a
   * change of manager. Kept as its own labelled action rather than as a second
   * meaning for the Wanted toggle: "pressing an on toggle" that leaves it on and
   * quietly changes *why* is not a state change a user can predict.
   */
  const pinWanted = async () => {
    setBusy(true);
    try {
      await api.post(`/artists/${artistMbid}/desires`, {
        release_group_mb_id: g.mb_id,
        release_mb_id: "",
        title: g.title,
        primary_type: g.primary_type,
        secondary_types: g.secondary_types,
        first_release_date: g.first_release_date,
      });
      onChanged();
    } catch (e) {
      toast("err", errMsg(e));
    } finally {
      setBusy(false);
    }
  };

  // Both forms of "stop wanting this" remove the same rows: the "any edition" want and
  // every pinned edition of the group. One function, so an edition can never be left
  // behind by whichever button was pressed.
  const clearWants = async () => {
    for (const rel of ["", ...(g.desired_releases ?? [])]) {
      const q = new URLSearchParams({ release_group_mb_id: g.mb_id, release_mb_id: rel });
      await api.del(`/artists/${artistMbid}/desires?${q.toString()}`);
    }
  };

  /**
   * Removes a want whose authority is gone. Its own action rather than un-freezing
   * the toggle: the toggle means "I want this / I do not", and this row's want was
   * never the user's answer to that — it is someone else's answer that outlived them.
   */
  const dismissWant = async () => {
    setBusy(true);
    try {
      await clearWants();
      onChanged();
    } catch (e) {
      toast("err", errMsg(e));
    } finally {
      setBusy(false);
    }
  };

  const toggleWanted = async () => {
    setBusy(true);
    try {
      if (explicitlyWanted) {
        await clearWants();
      } else {
        await api.post(`/artists/${artistMbid}/desires`, {
          release_group_mb_id: g.mb_id,
          release_mb_id: "",
          title: g.title,
          primary_type: g.primary_type,
          secondary_types: g.secondary_types,
          first_release_date: g.first_release_date,
        });
      }
      onChanged();
    } catch (e) {
      toast("err", errMsg(e));
    } finally {
      setBusy(false);
    }
  };

  return (
    <tr>
      <td>
        <Artwork entity="release-group" mbid={g.mb_id} name={g.title} px={26} />
      </td>
      <td>
        <DiskMarker owned={g.owned} complete={g.complete} what="tracks" />
      </td>
      <td style={{ color: "var(--text)" }}>
        <div className="row" style={{ gap: 8 }}>
          {/* The album page lists every edition and what is owned of each, so it is
              a browsing destination for anything in the catalogue — not only for
              what is already wanted. */}
          <Link to={`/collection/${artistMbid}/${g.mb_id}`} style={{ color: "var(--text)" }}>{g.title}</Link>
          <MBLink entity="release-group" mbid={g.mb_id} />
          {note && (
            <span className="mono" style={{ fontSize: 11, color: "var(--warning-text)" }} title={note.title}>
              ⚠ {note.label}
            </span>
          )}
        </div>
      </td>
      <td className="mono dim" style={{ fontSize: 11 }}>
        {[g.primary_type, g.secondary_types].filter(Boolean).join(" · ") || "—"}
      </td>
      <td className="num dim mono" style={{ fontSize: 11 }}>{(g.first_release_date || "").slice(0, 4) || "—"}</td>
      <td className="num mono" style={{ fontSize: 11, color: partial ? "var(--warning-text)" : "var(--text-dim)" }}>
        {g.total_tracks > 0 ? `${g.owned_tracks}/${g.total_tracks}` : "—"}
        {/* The counts describe the best-owned edition only, so owning a second
            pressing would otherwise be invisible here. */}
        {g.owned_editions > 1 && (
          <span
            className="dim"
            title={`${g.owned_editions} editions on disk — the count is the best-owned one. Open the album to see each.`}
          >
            {" "}×{g.owned_editions}
          </span>
        )}
      </td>
      <td>
        {derivedWant ? (
          // Derived state, so it reads as a fact about the album rather than as
          // something to click. The pill names the authority; the tooltip says
          // where to change it.
          <span
            title={
              stale
                ? "Left behind by a manager that no longer governs this artist. Pin it to keep it, or dismiss it."
                : g.wanted_source === "manager"
                  ? `${manager} monitors this album. Change it in ${manager}, or pin it here to keep it regardless.`
                  : "Wanted because you follow this artist. Change the follow settings above, or pin it to keep it if you stop following."
            }
          >
            <Pill kind="off">{stale ? "was managed" : g.wanted_source === "manager" ? manager : "auto"}</Pill>
          </span>
        ) : wantSummary ? (
          <span className="mono" style={{ fontSize: 11, color: "var(--accent-text)" }} title="Explicitly wanted">
            {wantSummary}
          </span>
        ) : (
          <span className="dim">—</span>
        )}
      </td>
      <td>
        <div className="row" style={{ justifyContent: "flex-end", gap: 6 }}>
          {/* A toggle, not a command: "Want" when off, "Wanted" when on, so the
              label carries the state rather than leaving it to the accent fill.
              A derived want is not this control's to switch off, so it is frozen
              (disabled, still pressed) and Pin is offered instead — a toggle whose
              off direction silently does nothing is worse than a disabled one. */}
          <button
            className={g.wanted ? "btn btn-primary btn-sm" : "btn btn-secondary btn-sm"}
            aria-pressed={g.wanted}
            disabled={busy || derivedWant || locked}
            title={
              locked
                ? `${manager} decides what is wanted for this artist. Add it in ${manager}; Autotaggerr mirrors what it monitors.`
                : explicitlyWanted
                  ? "Wanted. Click to remove."
                  : derivedWant
                    ? g.wanted_source === "manager"
                      ? `Wanted because ${manager} monitors it — not something this page can switch off. Pin it to keep it regardless.`
                      : "Wanted because you follow this artist — not something this row can switch off. Unfollow above, or pin it to keep it."
                    : "Not wanted. Click to want this album — any edition, whole album."
            }
            onClick={toggleWanted}
          >
            {g.wanted ? "Wanted" : "Want"}
          </button>
          {/* Pinning writes a want, which a manager-owned artist rejects — so it is
              offered only where it can succeed. */}
          {derivedWant && !locked && (
            <button
              className="btn btn-secondary btn-sm"
              disabled={busy}
              title="Ask for this album explicitly, so it stays wanted even if the reason above goes away"
              onClick={pinWanted}
            >
              Pin
            </button>
          )}
          {/* Offered only when nothing can recreate the row. While the manager still
              governs, dismissing would be undone by its next sync — a button that
              works and then silently reverts is worse than no button. */}
          {stale && (
            <button
              className="btn btn-ghost btn-sm"
              disabled={busy}
              title="Remove this want. It came from a manager that no longer decides for this artist, and nothing will bring it back. Files on disk are not touched."
              onClick={dismissWant}
            >
              Dismiss
            </button>
          )}
        </div>
      </td>
    </tr>
  );
}

/**
 * Who decides what is wanted for this artist, and how to change that.
 *
 * It renders in all three states rather than only where there is a button, because
 * "Autotaggerr decides this one" is the answer to the same question and a panel that
 * disappears once answered leaves the user unsure whether they ever answered it. The
 * natively-managed case is a sentence with no control, which is the honest shape:
 * there is no authority to take back.
 */
function ManagerAuthority({
  artist,
  busy,
  manager,
  onDetach,
  onReattach,
}: {
  artist: CollectionArtist;
  busy: boolean;
  manager: string;
  onDetach: () => void;
  onReattach: () => void;
}) {
  return (
    <div className="card">
      <div className="row" style={{ gap: 8, marginBottom: 8 }}>
        <span className="eyebrow">Authority</span>
      </div>
      <div className="row" style={{ justifyContent: "space-between", gap: 16, alignItems: "flex-start" }}>
        <div className="dim" style={{ fontSize: 11, maxWidth: "58ch" }}>
          {artist.manager_detached
            ? "Detached. Autotaggerr decides what is wanted for this artist, even though its files live in a managed library. The wants it kept from the manager are yours now."
            : artist.detachable
              ? `${manager} decides what is wanted for this artist, and Autotaggerr mirrors it. Detaching takes that over and keeps what ${manager} already decided.`
              : "Autotaggerr already decides what is wanted for this artist — no manager governs it, so there is nothing to detach."}
        </div>
        {artist.manager_detached ? (
          <button className="btn btn-secondary btn-sm" disabled={busy} onClick={onReattach}>
            Reattach
          </button>
        ) : artist.detachable ? (
          <button className="btn btn-secondary btn-sm" disabled={busy} onClick={onDetach}>
            Detach
          </button>
        ) : null}
      </div>
    </div>
  );
}

const FOLLOW_TYPES = ["Album", "EP", "Single", "Live", "Compilation", "Other"];

/**
 * Following is a convenience — it auto-wants a *shape* of release — never a
 * precondition for picking albums by hand. The panel states what will be wanted,
 * because "follow" on its own tells you nothing.
 *
 * It is a disclosure below the header rather than a permanent card: the toggle is
 * the part you need every visit, and the type checkboxes are the part you set once.
 */
function FollowSettings({
  artist,
  busy,
  onChange,
  manager,
}: {
  artist: CollectionArtist;
  busy: boolean;
  onChange: (body: Record<string, unknown>) => void;
  manager: string;
}) {
  const types = (artist.follow_types || "Album,EP").split(",").map((t) => t.trim()).filter(Boolean);

  // When a manager owns the artist it decides what is wanted, so these controls are
  // frozen rather than hidden: the settings are still real and still shown, they
  // just do not govern until the artist is natively managed.
  const frozen = !artist.follow_governs;

  const toggleType = (t: string) => {
    const next = types.includes(t) ? types.filter((x) => x !== t) : [...types, t];
    onChange({ follow_types: next.join(",") });
  };

  return (
    <div className="card" style={frozen ? { opacity: 0.75 } : undefined}>
      {/* No "Managed by" pill here: the header carries it, and two pills for one
          fact in one viewport is the duplication the style guide warns about. The
          sentence below says the same thing with the detail this panel needs. */}
      <div className="row" style={{ gap: 8, marginBottom: 8 }}>
        <span className="eyebrow">Automatically want</span>
      </div>
      <div className="dim" style={{ fontSize: 11, marginBottom: 10 }}>
        {frozen
          ? `${manager} decides what is wanted for this artist; Autotaggerr mirrors it. These settings are kept but do not apply.`
          : artist.monitored
            ? `Following wants ${types.join(", ") || "nothing"}${artist.follow_secondary ? ", including live albums, compilations and remixes" : ""}, as they are released.`
            : "Not following. These are what a follow would want."}
      </div>
      <div className="row" style={{ gap: 6, flexWrap: "wrap" }}>
        {FOLLOW_TYPES.map((t) => (
          <button
            key={t}
            className={types.includes(t) ? "btn btn-primary btn-sm" : "btn btn-secondary btn-sm"}
            aria-pressed={types.includes(t)}
            disabled={busy || frozen}
            onClick={() => toggleType(t)}
          >
            {t}
          </button>
        ))}
      </div>
      <label className="row" style={{ gap: 8, cursor: frozen ? "default" : "pointer", marginTop: 10 }}>
        <input
          type="checkbox"
          checked={artist.follow_secondary}
          disabled={busy || frozen}
          onChange={(e) => onChange({ follow_secondary: e.target.checked })}
        />
        <span style={{ fontSize: 12 }}>Include live albums, compilations and remixes</span>
      </label>
      <div className="dim" style={{ fontSize: 11, marginTop: 4 }}>
        Off by default: a full discography is mostly reissues, which buries what you actually lack.
      </div>
    </div>
  );
}
