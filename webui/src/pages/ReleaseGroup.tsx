import { useEffect, useRef, useState } from "react";
import { Link, useNavigate, useParams } from "react-router-dom";
import { api, errMsg } from "../api";
import { useFetch } from "../hooks";
import {
  AlbumCredit,
  ArtistDetail,
  CollectionArtist,
  CollectionDesire,
  CollectionReleaseGroup,
  Edition,
  ExternalLink,
  ReleaseTracks,
  ScanStatus,
} from "../types";
import { ConfirmDialog, ErrorNote, Pill } from "../components/ui";
import { RecorrelateDialog } from "../components/RecorrelateDialog";
import { OrganiseDialog } from "../components/OrganiseDialog";
import { ItemsModal } from "../components/ItemsModal";
import { MBLink } from "../components/MBLink";
import { ExternalLinks } from "../components/ExternalLinks";
import { mediaSummary } from "../components/mediaSummary";
import { useToast } from "../toast";
import { Artwork } from "../components/Artwork";
import { CoverageBar, DiskMarker } from "../components/CoverageBar";
import { Browse, FilterChip, matches, sortRows, useBrowse } from "../components/browse";
import {
  LibraryTabs,
  LibraryTags,
  editBlockedReason,
  editLibraryName,
  editLibraryParam,
  useLibraries,
} from "../components/Libraries";

interface Detail {
  artist: CollectionArtist;
  release_group: CollectionReleaseGroup;
  editions: Edition[] | null;
  desires: CollectionDesire[];
  /** Every credited artist the collection knows, in credit order. */
  credits?: AlbumCredit[] | null;
  /** The managers holding this album, as links into each one's UI. */
  links?: ExternalLink[];
}

/**
 * What you want, on this album, is expressed by ticking things — not by first
 * choosing a mode and then ticking things.
 *
 * An earlier version put three scope buttons above the panes ("any release · whole
 * album", "any release · specific tracks", "specific editions") and left the panes
 * interactive underneath. Two controls for one intention, and the buttons were the
 * weaker of the two: they could not say *which* edition or *which* track, so the
 * panes had to be used anyway. They are gone. The checkboxes are the only input,
 * and the summary line under the header reports what they add up to.
 *
 * Both defaults are now visible rather than implied: "Any edition" is a real row at
 * the top of the edition list, and "All tracks" is a real row at the top of the
 * tracklist. That is what the modes were standing in for — and unlike the modes,
 * a checkbox cannot be in a state the stored rows do not hold.
 */
export default function ReleaseGroup() {
  const { mbid = "", rgid = "" } = useParams();
  const toast = useToast();
  const browse = useBrowse("date", "asc");
  const libraries = useLibraries();
  // The library the album is read in — the same choice as on the artist page, and
  // carried between them, because it decides what is wanted and owned here too.
  const viewLibrary = browse.flag("library");
  const inLibrary = viewLibrary ? `?library=${encodeURIComponent(viewLibrary)}` : "";
  const { data, err, loading, reload } = useFetch<Detail>(
    () => api.get(`/artists/${mbid}/release-groups/${rgid}${inLibrary}`),
    [mbid, rgid, inLibrary]
  );

  // Asked separately: a Plex link may need Plex asked, and the page should not wait.
  // Read in a library, only that library's servers are linked.
  const plexLinks = useFetch<ExternalLink[]>(
    () => api.get(`/artists/${mbid}/release-groups/${rgid}/plex${inLibrary}`),
    [mbid, rgid, inLibrary]
  );

  const [busy, setBusy] = useState(false);
  const [recorrelateAsk, setRecorrelateAsk] = useState(false);
  const [organiseAsk, setOrganiseAsk] = useState(false);
  const [sameAsk, setSameAsk] = useState(false);
  // Whose files the Items modal lists: the album's (its release-group) or one edition's.
  const [itemsOf, setItemsOf] = useState<{ mbid: string; title: string } | null>(null);

  // Re-correlate queues on the shared job runner, so the global status is what says
  // whether it can be started — the same poll the artist page runs, for the same
  // reason. Polling only continues while something is running.
  const status = useFetch<ScanStatus>(() => api.get("/process/status"));
  const running = status.data?.running ?? false;
  useEffect(() => {
    if (!running) return;
    const t = setInterval(() => status.reload(), 3000);
    return () => clearInterval(t);
  }, [running, status.reload]);

  // A re-correlate rewrites this album's tags and re-answers what its files are, so
  // the page it happened on has to stop showing the state from before it.
  const wasRunning = useRef(false);
  useEffect(() => {
    if (wasRunning.current && !running) reload();
    wasRunning.current = running;
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [running]);

  const artist = data?.artist;
  const rg = data?.release_group;
  // A collaboration is everyone's album: the page is reached through one artist and
  // the back link returns there, but the others are named and linked, since Lidarr
  // may hold the album under any of them.
  const credits = data?.credits ?? [];
  const shared = credits.length > 1;
  const editions = data?.editions ?? [];
  const desires = data?.desires ?? [];

  // The two shapes a stored want takes. "Any edition" and "these editions" are
  // mutually exclusive: marking an edition is a narrowing of the want, not a second
  // unrelated one.
  const anyDesire = desires.find((d) => !d.release_mb_id);
  const editionDesires = desires.filter((d) => d.release_mb_id);

  // Derived means "wanted for a reason this page cannot switch off" — following the
  // artist, or the manager. It is read from wanted_source, *not* from the absence of
  // desire rows: a derived want has rows too now (the edition Lidarr selected, or the
  // one the rebuild narrowed to), and treating rows as authorship offered controls
  // that the API rejects with a 409.
  const derivedWant = !!rg?.wanted && rg.wanted_source !== "explicit";
  const isWanted = desires.length > 0 || !!rg?.wanted;

  // Locked is the stronger condition: a manager owns identity for this artist, so
  // *nothing* here may be written by hand — not the want, not the edition, not the
  // tracks. Derived only freezes the want itself; locked freezes the narrowing too.
  const locked = !!artist && !artist.identity_editable;

  // A derived want with no edition marked *means* any edition, so the row reads as
  // ticked even though nothing is stored. Showing it unticked next to a wanted album
  // was the page contradicting itself.
  const anyEditionOn = !!anyDesire || (derivedWant && editionDesires.length === 0);

  // Lidarr governs some of this artist's files (re-correlate and "Same album as…"
  // act on those); lidarrView is whether it decides what this view shows as wanted.
  const isLidarr = artist?.managed_by === "lidarr" || artist?.managed_by === "mixed";
  const viewAuthority = artist?.libraries?.find((l) => l.library_id === artist.view_library_id)?.authority;
  // The album's actions follow the library it is read in, as the artist page's do.
  const inLibraryName = viewLibrary ? artist?.libraries?.find((l) => l.library_id === viewLibrary)?.name : undefined;
  const lidarrNamesFiles = viewLibrary ? viewAuthority === "lidarr" : artist?.managed_by === "lidarr";
  const lidarrView = viewAuthority ? viewAuthority === "lidarr" : isLidarr;
  const managerLabel = isLidarr ? "Lidarr" : "the manager";
  const target = editLibraryParam(artist);
  const where = editLibraryName(artist);
  const derivedReason =
    rg?.wanted_source === "manager"
      ? `${managerLabel} monitors this album${editionDesires.length > 0 ? " on this edition" : ""}.`
      : "Wanted because you follow this artist.";
  // Why a control is frozen, in one sentence, wherever one is frozen. Locked outranks
  // derived: under a manager the reason is the same for every control on the page.
  const frozenReason = locked
    ? editBlockedReason(artist) ?? `${managerLabel} decides what is wanted for this artist and which edition it is. Change it in ${managerLabel}.`
    : derivedReason;

  // Which edition's tracklist is on screen. Kept in the URL so coming back from
  // elsewhere returns to the same one.
  const selected = browse.flag("edition");
  const detailRelease =
    selected ||
    editionDesires[0]?.release_mb_id ||
    earliest(editions)?.id ||
    editions[0]?.id ||
    null;

  /**
   * Records a want. Widening back to "any edition" and narrowing to a specific one
   * are both just this call: the server holds the rule that a release-group is
   * either "any edition" or a set of specific ones, and clears the other side
   * itself (see collection.SetDesire). Doing it here too would be a second copy of
   * a rule that only needs one.
   */
  const save = async (releaseMbid: string, recordings: string[]) => {
    if (!rg) return;
    setBusy(true);
    try {
      await api.post(`/artists/${mbid}/desires`, {
        release_group_mb_id: rg.mb_id,
        release_mb_id: releaseMbid,
        recording_mb_ids: recordings,
        title: rg.title,
        primary_type: rg.primary_type,
        secondary_types: rg.secondary_types,
        first_release_date: rg.first_release_date,
        ...target,
      });
      reload();
    } catch (e) {
      toast("err", errMsg(e));
    } finally {
      setBusy(false);
    }
  };

  // The narrowest repair: one album's folders, rather than the artist's whole
  // discography. Same verb, same confirm step, one level down.
  const recorrelate = async () => {
    setBusy(true);
    try {
      await api.post(`/release-groups/${rgid}/recorrelate${inLibrary}`);
      toast("info", "Re-correlate started — see Activity");
      setRecorrelateAsk(false);
      setTimeout(() => status.reload(), 300);
    } catch (e) {
      toast("err", errMsg(e));
    } finally {
      setBusy(false);
    }
  };

  const drop = async (releaseMbid: string) => {
    setBusy(true);
    try {
      const q = new URLSearchParams({ release_group_mb_id: rgid, release_mb_id: releaseMbid, ...target });
      await api.del(`/artists/${mbid}/desires?${q.toString()}`);
      reload();
    } catch (e) {
      toast("err", errMsg(e));
    } finally {
      setBusy(false);
    }
  };

  const dropAll = async () => {
    setBusy(true);
    try {
      for (const d of desires) {
        const q = new URLSearchParams({ release_group_mb_id: rgid, release_mb_id: d.release_mb_id, ...target });
        await api.del(`/artists/${mbid}/desires?${q.toString()}`);
      }
      reload();
    } catch (e) {
      toast("err", errMsg(e));
    } finally {
      setBusy(false);
    }
  };

  if (err) return <ErrorNote message={err} />;

  const summary = wantSummary({ isWanted, anyEditionOn, anyDesire, editionDesires, derivedWant, derivedReason, editions });

  return (
    <div className="stack">
      <div>
        <Link to={`/collection/${mbid}${inLibrary}`} className="dim mono" style={{ fontSize: 11 }}>
          ← {artist?.name ?? "Artist"}
        </Link>
      </div>

      <div className="entity-head">
        <Artwork
          entity="release-group"
          mbid={rgid}
          name={rg?.title ?? "Album"}
          px={120}
          size={500}
          className="artwork-lg"
        />
        <div className="entity-body">
          <div className="eyebrow">
            {[rg?.primary_type, rg?.secondary_types, (rg?.first_release_date || "").slice(0, 4), shared ? "" : artist?.name]
              .filter(Boolean)
              .join(" · ") || "Album"}
          </div>
          <div className="entity-title">
            <h1>{rg?.title ?? "Release group"}</h1>
          </div>
          {shared && (
            <div className="entity-meta">
              <span>
                By{" "}
                {credits.map((c, i) => (
                  <span key={c.mb_id}>
                    {i > 0 && (i === credits.length - 1 ? " & " : ", ")}
                    {c.mb_id === mbid ? (
                      <span style={{ color: "var(--text)" }}>{c.name}</span>
                    ) : (
                      <Link to={`/collection/${c.mb_id}/${rgid}${inLibrary}`} title={`This album as ${c.name}'s`}>
                        {c.name}
                      </Link>
                    )}
                  </span>
                ))}
              </span>
            </div>
          )}

          {rg && rg.total_tracks > 0 && (
            <div className="entity-coverage">
              <CoverageBar
                total={rg.total_tracks}
                owned={rg.owned_tracks}
                label="Tracks"
                width={220}
              />
              <span className="cov-label">
                {rg.owned_tracks}/{rg.total_tracks} tracks on disk
                {rg.owned_editions > 1 && ` · ${rg.owned_editions} editions`}
              </span>
            </div>
          )}

          <div className="entity-meta">
            {/* What is stored, in one sentence. This replaces the scope buttons:
                the checkboxes below are the control, and this is the readout. */}
            <span style={{ color: isWanted ? "var(--accent-text)" : "var(--text-dim)" }}>{summary}</span>
            {libraries.multi && (rg?.libraries?.length ?? 0) > 0 && (
              <>
                <span className="sep">·</span>
                <span className="dim">In {rg!.libraries.map((l) => l.name).join(", ")}</span>
              </>
            )}
            <span className="sep">·</span>
            <MBLink entity="release-group" mbid={rgid} />
            <ExternalLinks links={data?.links} />
            <ExternalLinks links={plexLinks.data} />
          </div>
        </div>

        <div className="entity-actions">
          {/* One pill, naming whichever authority applies — the manager if it owns
              the artist, otherwise the follow. Both at once would be two labels for
              one fact, and under a manager the follow is not the reason anyway. */}
          {locked ? (
            <Pill kind="off">
              {artist?.edit_blocked === "choose_library" ? "Choose a library" : lidarrView ? "Managed by Lidarr" : `Managed by ${managerLabel}`}
            </Pill>
          ) : (
            derivedWant && <Pill kind="off">{rg?.wanted_source === "manager" ? "Lidarr" : "auto"}</Pill>
          )}
          {/* Frozen for a derived want, exactly as on the artist page: this control
              cannot switch off a want whose reason lives elsewhere. Narrowing it
              below (an edition, a track) still works and makes the want yours —
              unless a manager owns identity, where nothing here is writable. */}
          <button
            className={isWanted ? "btn btn-primary btn-sm" : "btn btn-secondary btn-sm"}
            aria-pressed={isWanted}
            disabled={busy || loading || derivedWant || locked}
            title={
              locked
                ? frozenReason
                : derivedWant
                  ? `${derivedReason} Pin it, or narrow it below, to make it yours.`
                  : isWanted
                    ? "Wanted. Click to remove."
                    : `Not wanted. Click to want any edition, whole album${where ? `, in ${where}` : ""}.`
            }
            onClick={() => (desires.length > 0 ? dropAll() : save("", []))}
          >
            {isWanted ? "Wanted" : "Want"}
          </button>
          {/* Pinning writes a want, so it is not offered under a manager — the API
              rejects it, and a button that always fails is worse than no button. */}
          {derivedWant && !locked && (
            <button
              className="btn btn-secondary btn-sm"
              disabled={busy || loading}
              title="Ask for this album explicitly, so it stays wanted even if the reason above goes away"
              onClick={() => save("", [])}
            >
              Pin
            </button>
          )}
          {/* The narrowest of the three re-correlate scopes, and the one worth
              reaching for first: repairing one album after a Lidarr re-import used to
              mean the library-wide form, which discards every manager-governed pin in
              it. Stays visible with nothing on disk — it is the control that explains
              why it cannot run — and disabled, because there are no folders to walk. */}
          {/* Only on the shape a manager re-key leaves behind: an album the manager no
              longer lists, beside the artist's ones it does. The sync pairs most of
              these itself; this is for the ones it could not (a renamed album, two of
              the same title, a re-key while a sync was failing). */}
          {isLidarr && rg && !rg.in_catalog && (
            <button
              className="btn btn-ghost btn-sm"
              disabled={busy || loading}
              title={`${managerLabel} no longer lists this album. If it is a leftover from ${managerLabel} moving the album to a new ID, say which album it became.`}
              onClick={() => setSameAsk(true)}
            >
              Same album as…
            </button>
          )}
          <button
            className="btn btn-ghost btn-sm"
            disabled={loading || !rg?.owned}
            title={
              rg?.owned
                ? `The files of this album${inLibraryName ? ` in ${inLibraryName}` : ""}, with what tagging would change in each`
                : "Nothing of this album is on disk."
            }
            onClick={() => rg && setItemsOf({ mbid: rgid, title: rg.title })}
          >
            Items
          </button>
          <button
            className="btn btn-ghost btn-sm"
            disabled={busy || loading || running || !rg?.owned || lidarrNamesFiles}
            title={
              lidarrNamesFiles
                ? "Lidarr names these files — rename them in Lidarr."
                : rg?.owned
                  ? `Move this album's files${inLibraryName ? ` in ${inLibraryName}` : ""} to where their library's naming templates put them. Shows every move for review first.`
                  : "Nothing of this album is on disk, so there is nothing to organise."
            }
            onClick={() => setOrganiseAsk(true)}
          >
            Organise
          </button>
          <button
            className="btn btn-ghost btn-sm"
            style={{ color: "var(--danger-text)" }}
            disabled={busy || loading || running || !rg?.owned}
            title={
              rg?.owned
                ? `Ask ${managerLabel} what this album's files${inLibraryName ? ` in ${inLibraryName}` : ""} are, ignoring the cached answers and the unchanged-file skip, and rewrite their tags to match. The repair for an album that disagrees with ${managerLabel} — a changed edition, a re-import — which an ordinary Process cannot fix because nothing on disk changed. Writes tags.`
                : "Nothing of this album is on disk, so there are no files to re-correlate."
            }
            onClick={() => setRecorrelateAsk(true)}
          >
            Re-correlate
          </button>
        </div>
      </div>

      <LibraryTabs
        libraries={libraries.multi ? artist?.libraries ?? [] : []}
        active={viewLibrary}
        onChoose={(id) => browse.setFlag("library", id)}
      />

      {sameAsk && rg && (
        <SameAlbumDialog
          artistMbid={mbid}
          rg={rg}
          manager={managerLabel}
          onCancel={() => setSameAsk(false)}
        />
      )}

      {itemsOf && (
        <ItemsModal
          mbid={itemsOf.mbid}
          title={itemsOf.title}
          libraryId={viewLibrary || null}
          onClose={() => setItemsOf(null)}
        />
      )}

      {organiseAsk && (
        <OrganiseDialog
          scope={viewLibrary ? { release_group_mb_id: rgid, library_id: viewLibrary } : { release_group_mb_id: rgid }}
          title={`${rg?.title ?? "this album"}${inLibraryName ? ` in ${inLibraryName}` : ""}`}
          onClose={() => setOrganiseAsk(false)}
        />
      )}

      {recorrelateAsk && (
        <RecorrelateDialog
          scope={`${rg ? rg.title : "this album"}${inLibraryName ? ` in ${inLibraryName}` : ""}`}
          manager={managerLabel}
          discardsPins={isLidarr}
          busy={busy}
          onConfirm={recorrelate}
          onCancel={() => setRecorrelateAsk(false)}
        />
      )}

      {loading && <div className="muted">Loading…</div>}

      {!loading && editions.length === 0 && (
        <div className="card">
          <div className="dim" style={{ fontSize: 12 }}>
            MusicBrainz lists no editions for this release-group.
          </div>
        </div>
      )}

      {editions.length > 0 && (
        <div className="rg-split">
          <EditionList
            editions={editions}
            editionDesires={editionDesires}
            anyEditionOn={anyEditionOn}
            derivedWant={derivedWant}
            frozenReason={frozenReason}
            locked={locked}
            selected={detailRelease}
            busy={busy}
            browse={browse}
            showLibraries={libraries.multi && !viewLibrary}
            onSelect={(id) => browse.setFlag("edition", id)}
            onChooseAny={() => save("", [])}
            onWantEdition={(id) => save(id, [])}
            onDrop={drop}
          />
          <TrackPane
            releaseMbid={detailRelease}
            owned={!!editions.find((e) => e.id === detailRelease)?.owned}
            onShowItems={(mbid, title) => setItemsOf({ mbid, title })}
            anyDesire={anyDesire}
            anyEditionOn={anyEditionOn}
            editionDesires={editionDesires}
            derivedWant={derivedWant}
            frozenReason={frozenReason}
            locked={locked}
            busy={busy}
            onSaveAny={(recordings) => save("", recordings)}
            onSaveEdition={save}
            onDrop={drop}
          />
        </div>
      )}
    </div>
  );
}

/**
 * The stored want in one sentence, in the same vocabulary as the checkboxes. The
 * empty case says what the album *is* rather than what it is not.
 */
function wantSummary({
  isWanted,
  anyEditionOn,
  anyDesire,
  editionDesires,
  derivedWant,
  derivedReason,
  editions,
}: {
  isWanted: boolean;
  anyEditionOn: boolean;
  anyDesire?: CollectionDesire;
  editionDesires: CollectionDesire[];
  derivedWant: boolean;
  derivedReason: string;
  editions: Edition[];
}): string {
  if (!isWanted) return "Not wanted";
  if (derivedWant) {
    // A derived want can name an edition — Lidarr's monitored release does — so the
    // line has to say which, or the page states "any edition" above a list with one
    // ticked. Named by title where the edition list has it, since an MB ID here
    // would say less than the pressing's name.
    if (editionDesires.length === 1) {
      const chosen = editions.find((e) => e.id === editionDesires[0].release_mb_id);
      const named = chosen ? `the ${chosen.title}${chosen.date ? ` (${chosen.date.slice(0, 4)})` : ""} edition` : "one edition";
      return `${derivedReason} Wanted: ${named}.`;
    }
    if (editionDesires.length > 1) return `${derivedReason} Wanted: ${editionDesires.length} editions.`;
    return `${derivedReason} Any edition, whole album.`;
  }
  if (anyEditionOn) {
    const picked = anyDesire?.recording_mb_ids?.length ?? 0;
    return picked > 0
      ? `Wanted: ${picked} track${picked === 1 ? "" : "s"} from any edition`
      : "Wanted: any edition, whole album";
  }
  const tracks = editionDesires.reduce((n, d) => n + (d.recording_mb_ids?.length ?? 0), 0);
  const where = `${editionDesires.length} edition${editionDesires.length === 1 ? "" : "s"}`;
  return tracks > 0 ? `Wanted: ${where}, ${tracks} track${tracks === 1 ? "" : "s"}` : `Wanted: ${where}, whole edition`;
}

/** Earliest-dated release: the most canonical tracklist to pick songs from. */
function earliest(editions: Edition[]): Edition | undefined {
  return [...editions].sort((a, b) => (a.date || "9999").localeCompare(b.date || "9999"))[0];
}

const EDITION_SORT: Record<string, (e: Edition) => string | number> = {
  date: (e) => e.date || "",
  title: (e) => e.title,
  tracks: (e) => e.media?.reduce((n, m) => n + (m["track-count"] ?? 0), 0) ?? 0,
};

/**
 * Master: every edition, each with a checkbox that *is* the want for it, and a row
 * body that selects it for inspection. Two different jobs, so two different hit
 * areas — conflating them is what made the old page need a mode switch to explain
 * whether a click meant "want" or "look at".
 */
function EditionList({
  editions,
  editionDesires,
  anyEditionOn,
  derivedWant,
  frozenReason,
  locked,
  selected,
  busy,
  browse,
  showLibraries,
  onSelect,
  onChooseAny,
  onWantEdition,
  onDrop,
}: {
  editions: Edition[];
  editionDesires: CollectionDesire[];
  anyEditionOn: boolean;
  derivedWant: boolean;
  frozenReason: string;
  /** A manager owns the edition choice: every checkbox here is state, not input. */
  locked: boolean;
  selected: string | null;
  busy: boolean;
  browse: Browse;
  /** Name the libraries holding each edition — only when there is more than one library. */
  showLibraries: boolean;
  onSelect: (id: string) => void;
  onChooseAny: () => void;
  onWantEdition: (id: string) => void;
  onDrop: (id: string) => void;
}) {
  const ownedOnly = browse.flag("owned") === "1";
  const ownedCount = editions.filter((e) => e.owned).length;

  const filtered = editions.filter(
    (e) =>
      matches(browse.query, e.title, e.disambiguation, e.country, e.status, (e.date || "").slice(0, 4)) &&
      (!ownedOnly || e.owned)
  );
  const shown = sortRows(filtered, EDITION_SORT[browse.sort] ?? EDITION_SORT.date, browse.dir);

  return (
    <div className="card" style={{ padding: 0, overflow: "hidden" }}>
      <div style={{ padding: "10px 12px" }}>
        <div className="row" style={{ justifyContent: "space-between", marginBottom: 8 }}>
          <span className="eyebrow">Editions · {editions.length}</span>
          <div className="row" style={{ gap: 6 }}>
            <select
              className="select"
              style={{ height: 28, width: "auto", fontSize: 12 }}
              aria-label="Sort editions by"
              value={browse.sort}
              onChange={(e) => browse.setSort(e.target.value)}
            >
              <option value="date">Year</option>
              <option value="title">Title</option>
              <option value="tracks">Tracks</option>
            </select>
            <button
              type="button"
              className="btn btn-ghost btn-sm"
              title={browse.dir === "asc" ? "Ascending — click for descending" : "Descending — click for ascending"}
              aria-label={`Sort direction: ${browse.dir === "asc" ? "ascending" : "descending"}`}
              onClick={() => browse.setSort(browse.sort, browse.dir === "asc" ? "desc" : "asc")}
            >
              {browse.dir === "asc" ? "▲" : "▼"}
            </button>
            <FilterChip
              on={ownedOnly}
              count={ownedCount}
              label="On disk"
              title="Only editions you have files for"
              onClick={() => browse.setFlag("owned", ownedOnly ? null : "1")}
            />
          </div>
        </div>
        <input
          className="input"
          type="search"
          style={{ height: 28, fontSize: 12 }}
          placeholder="Filter by title, year, country"
          aria-label="Filter editions"
          value={browse.query}
          onChange={(e) => browse.setQuery(e.target.value)}
        />
      </div>

      {/* The default, as a row you can see and click. It used to be implied by the
          absence of marked editions, which meant the most common want was the one
          state the page never showed. */}
      <label
        className="edition-row any-row"
        title={
          locked || derivedWant
            ? `${frozenReason}${locked ? "" : " Any edition counts. Mark a specific edition below to narrow it."}`
            : "Any edition counts — whichever pressing turns up. This is what most people want."
        }
      >
        <input
          type="checkbox"
          checked={anyEditionOn}
          disabled={busy || derivedWant || locked}
          onChange={() => onChooseAny()}
        />
        <div style={{ minWidth: 0 }}>
          <div style={{ color: "var(--text)", fontSize: 12 }}>Any edition</div>
          <div className="dim" style={{ fontSize: 11 }}>Whichever pressing turns up counts</div>
        </div>
      </label>

      <div style={{ maxHeight: 420, overflowY: "auto" }}>
        {shown.length === 0 && (
          <div className="dim" style={{ fontSize: 11, padding: 12 }}>No edition matches this filter.</div>
        )}
        {shown.map((r) => {
          const desire = editionDesires.find((d) => d.release_mb_id === r.id);
          const tracks = desire?.recording_mb_ids?.length ?? 0;
          const active = selected === r.id;
          return (
            <div key={r.id} className={`edition-row${active ? " active" : ""}`}>
              {/* Checkbox = want this edition. Deliberately outside the button
                  below, so ticking never also re-selects and re-fetches. */}
              <input
                type="checkbox"
                checked={!!desire}
                disabled={busy || locked}
                aria-label={`Want the ${r.title} edition${r.date ? ` from ${r.date.slice(0, 4)}` : ""}`}
                title={
                  locked
                    ? frozenReason
                    : desire
                      ? "Wanted. Untick to drop this edition."
                      : "Want this edition — only marked editions will count."
                }
                onChange={() => (desire ? onDrop(r.id) : onWantEdition(r.id))}
              />
              {/* Row body = inspect. A button so it is keyboard-reachable; the
                  tracklist on the right follows it. */}
              <button type="button" className="edition-pick" onClick={() => onSelect(r.id)}>
                <div className="row" style={{ gap: 6 }}>
                  {/* Ownership is per edition: the release-group headline reports
                      only its best-owned one, which is exactly what hid a second
                      pressing before. */}
                  <DiskMarker owned={r.owned} complete={r.complete} what="files of this edition" />
                  <span style={{ color: "var(--text)", fontSize: 12 }}>
                    {r.title}
                    {r.disambiguation && <span className="dim"> ({r.disambiguation})</span>}
                  </span>
                </div>
                <div className="dim mono" style={{ fontSize: 11 }}>
                  {[
                    (r.date || "").slice(0, 4),
                    r.country,
                    r.status,
                    mediaSummary(r.media),
                    r.owned ? `${r.owned_tracks}/${r.owned_total_tracks} on disk` : "",
                    // Copies are not completeness: two libraries holding the same
                    // twelve tracks is 12/12, not 24/12, but the extra files are worth
                    // knowing about, so they are counted here rather than lost.
                    r.owned && r.files > r.owned_tracks ? `${r.files} files` : "",
                  ]
                    .filter(Boolean)
                    .join(" · ")}
                </div>
                {showLibraries && (r.libraries?.length ?? 0) > 0 && (
                  <div style={{ marginTop: 3 }}>
                    <LibraryTags copies={r.libraries} total={r.owned_total_tracks} />
                  </div>
                )}
                {desire && (
                  <div className="mono" style={{ fontSize: 11, marginTop: 2, color: "var(--accent-text)" }}>
                    {tracks > 0 ? `${tracks} tracks wanted` : "whole edition wanted"}
                  </div>
                )}
              </button>
              <MBLink entity="release" mbid={r.id} />
            </div>
          );
        })}
      </div>
    </div>
  );
}

/**
 * Detail: the selected edition's tracklist.
 *
 * Which want it edits depends on what is ticked on the left, and the header says
 * which — the pane never silently writes to a row the user is not looking at:
 *
 *   this edition is marked  -> that edition's tracks
 *   any edition is marked   -> the any-edition want's tracks
 *   nothing is marked       -> ticking a track wants it from any edition
 */
function TrackPane({
  releaseMbid,
  owned,
  onShowItems,
  anyDesire,
  anyEditionOn,
  editionDesires,
  derivedWant,
  frozenReason,
  locked,
  busy,
  onSaveAny,
  onSaveEdition,
  onDrop,
}: {
  releaseMbid: string | null;
  /** Files of this edition are on disk, so it has items to show. */
  owned: boolean;
  onShowItems: (releaseMbid: string, title: string) => void;
  anyDesire?: CollectionDesire;
  anyEditionOn: boolean;
  editionDesires: CollectionDesire[];
  derivedWant: boolean;
  frozenReason: string;
  /** A manager owns which track a file is: the tracklist is a readout here. */
  locked: boolean;
  busy: boolean;
  onSaveAny: (recordings: string[]) => void;
  onSaveEdition: (releaseMbid: string, recordings: string[]) => void;
  onDrop: (releaseMbid: string) => void;
}) {
  const tracks = useFetch<ReleaseTracks>(
    () => (releaseMbid ? api.get(`/releases/${releaseMbid}/tracks`) : Promise.resolve(null as never)),
    [releaseMbid]
  );

  if (!releaseMbid) {
    return <div className="card"><div className="dim" style={{ fontSize: 12 }}>Pick an edition.</div></div>;
  }

  const editionDesire = editionDesires.find((d) => d.release_mb_id === releaseMbid);
  const target: CollectionDesire | undefined = editionDesire ?? (anyEditionOn ? anyDesire : undefined);
  const perEdition = !!editionDesire;
  const selected = target?.recording_mb_ids ?? [];
  const list = tracks.data?.tracks ?? [];

  // Nothing picked means the whole thing — which is why "All tracks" is ticked and
  // every track under it reads as ticked too. A derived want has no rows at all, yet
  // it *means* the whole thing, so it reads the same: showing an unticked tracklist
  // beside a header claiming "whole album" was the page arguing with itself.
  const wholeThing = derivedWant || (!!target && selected.length === 0);
  const isOn = (recordingId: string) => wholeThing || selected.includes(recordingId);

  const write = (recordings: string[]) =>
    perEdition ? onSaveEdition(releaseMbid, recordings) : onSaveAny(recordings);

  const dropTarget = () => (perEdition ? onDrop(releaseMbid) : onDrop(""));

  const toggle = (recordingId: string) => {
    if (wholeThing) {
      // First narrowing: everything was wanted, so unticking one track means "all
      // the others" — unless there are no others, on a single-track release, where
      // it means the same as unticking "All tracks".
      const rest = list.filter((t) => t.recording_id !== recordingId).map((t) => t.recording_id);
      if (rest.length === 0) dropTarget();
      else write(rest);
      return;
    }
    const next = selected.includes(recordingId)
      ? selected.filter((x) => x !== recordingId)
      : [...selected, recordingId];
    // Clearing every track means it is no longer wanted at all — not "the whole
    // thing", which would silently widen what was asked for.
    if (next.length === 0) {
      dropTarget();
      return;
    }
    // A subset that happens to cover the entire tracklist is stored as the whole
    // thing, so the want does not quietly become pressing-specific.
    write(next.length === list.length ? [] : next);
  };

  const toggleAll = () => {
    // Unticking "All tracks" leaves nothing asked for, so the want goes.
    if (wholeThing) dropTarget();
    else write([]);
  };

  const partial = !!target && !wholeThing && selected.length > 0;
  // Recordings asked for that this edition does not carry — a deluxe and a
  // standard pressing do not share a tracklist. Surfaced rather than dropped.
  const absent = selected.filter((id) => !list.some((t) => t.recording_id === id));

  return (
    <div className="card" style={{ padding: 0, overflow: "hidden" }}>
      <div style={{ padding: "10px 12px 8px" }}>
        <div style={{ display: "flex", alignItems: "baseline", gap: 8 }}>
          <div className="eyebrow">Tracks</div>
          {/* This edition's files — the album-wide button above lists every edition's. */}
          {owned && (
            <button
              type="button"
              className="railref"
              style={{ marginLeft: "auto" }}
              title="The files of this edition, with what tagging would change in each"
              onClick={() => onShowItems(releaseMbid, tracks.data?.release.title ?? "this edition")}
            >
              <span>Items</span>
            </button>
          )}
        </div>
        <div className="dim" style={{ fontSize: 11, marginTop: 2 }}>
          {tracks.data?.release.title}
          {locked
            ? " · what is wanted, not something to edit here"
            : perEdition
              ? " · ticks apply to this edition"
              : anyEditionOn
                ? " · ticks apply to any edition"
                : " · ticking a track wants it from any edition"}
        </div>
        {absent.length > 0 && (
          <div style={{ fontSize: 11, marginTop: 4, color: "var(--warning-text)" }}>
            {absent.length} wanted {absent.length === 1 ? "song is" : "songs are"} not on this edition.
          </div>
        )}
      </div>

      {/* The other default made visible. "Whole album" was previously the absence
          of track rows — a state with no control of its own. */}
      <label
        className="track-row all-row"
        title={
          locked
            ? frozenReason
            : derivedWant
              ? `${frozenReason} That means every track. Untick individual tracks to narrow it and make it yours.`
              : wholeThing
                ? "Every track is wanted. Untick to stop wanting this."
                : "Want every track, rather than the ones ticked below."
        }
      >
        <input
          type="checkbox"
          checked={wholeThing}
          ref={(el) => {
            // Indeterminate is the honest state for a subset: neither all nor none.
            if (el) el.indeterminate = partial;
          }}
          // Frozen for a derived want, like every other control that would otherwise
          // claim to switch off a want whose reason lives elsewhere. Narrowing by
          // individual track still works, and that is what makes the want yours —
          // except under a manager, which owns the track a file maps to as well.
          disabled={busy || list.length === 0 || derivedWant || locked}
          onChange={toggleAll}
        />
        <span style={{ color: "var(--text)", fontSize: 12 }}>All tracks</span>
        {partial && (
          <span className="mono" style={{ fontSize: 11, color: "var(--accent-text)", marginLeft: "auto" }}>
            {selected.length} of {list.length}
          </span>
        )}
      </label>

      <div style={{ maxHeight: 420, overflowY: "auto", borderTop: "1px solid var(--border)" }}>
        {tracks.loading && <div className="dim" style={{ fontSize: 11, padding: 12 }}>Loading tracklist…</div>}
        {tracks.err && <div className="dim" style={{ fontSize: 11, padding: 12 }}>Could not load this tracklist.</div>}
        {list.map((t) => {
          const multiDisc = list.some((x) => x.medium !== t.medium);
          return (
            <label key={t.track_id} className="track-row" title={locked ? frozenReason : undefined}>
              <input
                type="checkbox"
                checked={isOn(t.recording_id)}
                disabled={busy || locked}
                onChange={() => toggle(t.recording_id)}
              />
              <span className="dim mono" style={{ fontSize: 11, minWidth: 28, textAlign: "right" }}>
                {multiDisc ? `${t.medium}-${t.number}` : t.number}
              </span>
              <span style={{ color: "var(--text)", fontSize: 12 }}>{t.title}</span>
            </label>
          );
        })}
      </div>
    </div>
  );
}

/**
 * Says that this album row is the same album as another of the artist's, under the ID
 * the manager moved it to. The candidates are the artist's albums the manager does
 * list; applying moves any want across and retires this row, the same as a re-key the
 * sync pairs on its own. If something still claims this row — files on disk resolving
 * to it — the change is recorded and waits on the Migrations page with that reason.
 */
function SameAlbumDialog({
  artistMbid,
  rg,
  manager,
  onCancel,
}: {
  artistMbid: string;
  rg: CollectionReleaseGroup;
  manager: string;
  onCancel: () => void;
}) {
  const toast = useToast();
  const navigate = useNavigate();
  const artist = useFetch<ArtistDetail>(() => api.get(`/artists/${artistMbid}`), [artistMbid]);
  const candidates = (artist.data?.release_groups ?? [])
    .filter((g) => g.in_catalog && g.mb_id !== rg.mb_id)
    .sort((a, b) => a.title.localeCompare(b.title));
  const [target, setTarget] = useState("");
  const [busy, setBusy] = useState(false);

  const confirm = async () => {
    if (!target) return;
    setBusy(true);
    try {
      const res = await api.post<{ applied: boolean; reason?: string }>(
        `/artists/${artistMbid}/release-groups/${rg.mb_id}/same-as`,
        { release_group_mb_id: target },
      );
      if (res.applied) {
        toast("info", "Merged into the album under its new ID");
        navigate(`/collection/${artistMbid}/${target}`, { replace: true });
      } else {
        toast("err", `Recorded, not applied yet: ${res.reason} — see Migrations`);
        onCancel();
      }
    } catch (e) {
      toast("err", errMsg(e));
    } finally {
      setBusy(false);
    }
  };

  return (
    <ConfirmDialog
      title="Same album as…"
      busy={busy}
      confirmLabel="Merge into this album"
      confirmDisabled={!target}
      body={
        <>
          <span>
            <strong>{rg.title}</strong> is no longer listed by {manager}. If {manager} moved it to a new ID, pick the
            album it became. Wants move to that album and this row is removed. No files are touched.
          </span>
          {artist.loading ? (
            <span className="muted">Loading albums…</span>
          ) : candidates.length === 0 ? (
            <span className="muted">This artist has no other album {manager} lists.</span>
          ) : (
            <select className="select" value={target} onChange={(e) => setTarget(e.target.value)}>
              <option value="">Choose the album…</option>
              {candidates.map((g) => (
                <option key={g.mb_id} value={g.mb_id}>
                  {g.title}
                  {g.first_release_date ? ` (${g.first_release_date.slice(0, 4)})` : ""}
                </option>
              ))}
            </select>
          )}
        </>
      }
      onConfirm={confirm}
      onCancel={onCancel}
    />
  );
}
