import { useEffect, useState } from "react";
import { Link, useParams } from "react-router-dom";
import { api, errMsg } from "../api";
import { useFetch } from "../hooks";
import {
  CollectionArtist,
  CollectionDesire,
  CollectionReleaseGroup,
  Edition,
  ReleaseTracks,
} from "../types";
import { ErrorNote, Pill } from "../components/ui";
import { MBLink } from "../components/MBLink";
import { mediaSummary } from "../components/mediaSummary";
import { useToast } from "../toast";

interface Detail {
  artist: CollectionArtist;
  release_group: CollectionReleaseGroup;
  editions: Edition[] | null;
  desires: CollectionDesire[];
}

/** The three shapes a want can take. Chosen explicitly so nothing is inferred. */
type Scope = "any-whole" | "any-tracks" | "editions";

/**
 * Seeds the scope from what is stored — and, when nothing is stored but the album
 * is wanted anyway (followed artist, or the manager monitors it), from what that
 * derived want *means*: any release, whole album. It has no desire rows, but it is
 * not "nothing wanted"; showing an empty scope next to a wanted album was the page
 * contradicting itself.
 */
function scopeOf(desires: CollectionDesire[], derivedWant: boolean): Scope | null {
  if (desires.length === 0) return derivedWant ? "any-whole" : null;
  const any = desires.find((d) => !d.release_mb_id);
  if (any) return (any.recording_mb_ids?.length ?? 0) > 0 ? "any-tracks" : "any-whole";
  return "editions";
}

export default function ReleaseGroup() {
  const { mbid = "", rgid = "" } = useParams();
  const toast = useToast();
  const { data, err, loading, reload } = useFetch<Detail>(
    () => api.get(`/artists/${mbid}/release-groups/${rgid}`),
    [mbid, rgid]
  );

  const [busy, setBusy] = useState(false);

  const artist = data?.artist;
  const rg = data?.release_group;
  const editions = data?.editions ?? [];
  const desires = data?.desires ?? [];

  // Scope is *intent*, and intent has empty states the database cannot hold:
  // "specific tracks, none picked yet" stores exactly like "whole album", and
  // "specific editions, none marked yet" stores like nothing at all. Deriving it
  // from the rows made both choices un-selectable. So it is UI state, seeded once
  // per release-group from what is stored, and the user owns it after that.
  const [scope, setScope] = useState<Scope | null>(null);
  const [seededFor, setSeededFor] = useState<string>("");
  useEffect(() => {
    if (!data || seededFor === rgid) return;
    setScope(scopeOf(data.desires, data.release_group.wanted && data.desires.length === 0));
    setSeededFor(rgid);
  }, [data, rgid, seededFor]);

  // Which edition's tracklist is on screen. For "any release" this is only the
  // list songs are being *picked from* — it does not narrow the want.
  const [detailRelease, setDetailRelease] = useState<string | null>(null);

  // Default the detail pane to something useful: the first edition already wanted,
  // else the earliest release (the most canonical tracklist for the album).
  useEffect(() => {
    if (detailRelease || editions.length === 0) return;
    const wanted = desires.find((d) => d.release_mb_id)?.release_mb_id;
    setDetailRelease(wanted || earliest(editions)?.id || editions[0].id);
  }, [editions, desires, detailRelease]);

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
      });
      reload();
    } catch (e) {
      toast("err", errMsg(e));
    } finally {
      setBusy(false);
    }
  };

  const drop = async (releaseMbid: string) => {
    setBusy(true);
    try {
      const q = new URLSearchParams({ release_group_mb_id: rgid, release_mb_id: releaseMbid });
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
        const q = new URLSearchParams({ release_group_mb_id: rgid, release_mb_id: d.release_mb_id });
        await api.del(`/artists/${mbid}/desires?${q.toString()}`);
      }
      reload();
    } catch (e) {
      toast("err", errMsg(e));
    } finally {
      setBusy(false);
    }
  };

  // What is actually stored, so the helper text never claims a state the database
  // does not hold yet.
  const anyRelease = desires.find((d) => !d.release_mb_id);
  const hasAnyRelease = !!anyRelease;
  const anyRecordings = anyRelease?.recording_mb_ids?.length ?? 0;
  const editionCount = desires.filter((d) => d.release_mb_id).length;

  // A want with no desire rows behind it is derived — from following the artist or
  // from the manager — so this page can display and narrow it, but not switch it off.
  const derivedWant = !!rg?.wanted && desires.length === 0;
  const isWanted = desires.length > 0 || !!rg?.wanted;
  const managerLabel =
    artist?.managed_by === "lidarr" || artist?.managed_by === "mixed" ? "Lidarr" : "the manager";
  const derivedReason =
    rg?.wanted_source === "manager"
      ? `Wanted because ${managerLabel} monitors it.`
      : "Wanted because you follow this artist.";

  const chooseScope = async (next: Scope) => {
    setScope(next);
    // Only "any release · whole album" is fully expressed the moment it is chosen,
    // so it is the only one that writes immediately. The other two persist when the
    // user actually picks something — a track, or an edition. Writing on click
    // would either store a row identical to whole-album or throw away an existing
    // want before the replacement exists.
    if (next === "any-whole") await save("", []);
  };

  if (err) return <ErrorNote message={err} />;

  return (
    <div className="stack">
      <div>
        <Link to={`/collection/${mbid}`} className="dim mono" style={{ fontSize: 11 }}>
          ← {artist?.name ?? "Artist"}
        </Link>
      </div>

      <div className="page-head">
        <div className="row" style={{ gap: 10 }}>
          <h1>{rg?.title ?? "Release group"}</h1>
          <span className="dim mono" style={{ fontSize: 11 }}>
            {[rg?.primary_type, rg?.secondary_types, (rg?.first_release_date || "").slice(0, 4)]
              .filter(Boolean)
              .join(" · ")}
          </span>
          <MBLink entity="release-group" mbid={rgid} />
        </div>
        <div className="row" style={{ gap: 8 }}>
          {derivedWant && (
            <Pill kind="off">
              <span title={derivedReason}>{rg?.wanted_source === "manager" ? managerLabel : "auto"}</span>
            </Pill>
          )}
          {/* Frozen for a derived want, exactly as on the artist page: this control
              cannot switch off a want whose reason lives elsewhere. Narrowing it
              below (an edition, a track) still works and makes the want yours. */}
          <button
            className={isWanted ? "btn btn-primary btn-sm" : "btn btn-secondary btn-sm"}
            aria-pressed={isWanted}
            disabled={busy || loading || derivedWant}
            title={
              derivedWant
                ? `${derivedReason} Pin it, or narrow it below, to make it yours.`
                : isWanted
                  ? "Wanted. Click to remove."
                  : "Not wanted. Click to want any release, whole album."
            }
            onClick={() => {
              if (desires.length > 0) {
                setScope(null);
                dropAll();
              } else {
                setScope("any-whole");
                save("", []);
              }
            }}
          >
            {isWanted ? "Wanted" : "Want"}
          </button>
          {derivedWant && (
            <button
              className="btn btn-secondary btn-sm"
              disabled={busy || loading}
              title="Ask for this album explicitly, so it stays wanted even if the reason above goes away"
              onClick={() => { setScope("any-whole"); save("", []); }}
            >
              Pin
            </button>
          )}
        </div>
      </div>

      {rg?.owned && (
        <div className="dim" style={{ fontSize: 12 }}>
          On disk: {rg.owned_tracks}/{rg.total_tracks} tracks of the best-owned edition
          {rg.owned_editions > 1 &&
            `, across ${rg.owned_editions} editions — each is marked in the list below`}
          .
        </div>
      )}

      <div className="card">
        <div className="eyebrow" style={{ marginBottom: 8 }}>What you want</div>
        <div className="row" style={{ gap: 6, flexWrap: "wrap" }}>
          <ScopeChoice on={scope === "any-whole"} disabled={busy} onClick={() => chooseScope("any-whole")}>
            Any release · whole album
          </ScopeChoice>
          <ScopeChoice on={scope === "any-tracks"} disabled={busy} onClick={() => chooseScope("any-tracks")}>
            Any release · specific tracks
          </ScopeChoice>
          <ScopeChoice on={scope === "editions"} disabled={busy} onClick={() => chooseScope("editions")}>
            Specific editions
          </ScopeChoice>
        </div>
        <div className="dim" style={{ fontSize: 11, marginTop: 8 }}>
          {scope === null && "Nothing wanted yet."}
          {scope === "any-whole" &&
            (derivedWant
              ? `${derivedReason} That means any release, whole album. Narrowing it below records the choice as yours.`
              : "Whichever pressing turns up counts. This is what most people want.")}
          {scope === "any-tracks" &&
            (anyRecordings > 0
              ? `${anyRecordings} song${anyRecordings === 1 ? "" : "s"} wanted from any pressing that contains them.`
              : "No songs picked yet, so the whole album is still wanted. Tick tracks on the right to narrow it.")}
          {scope === "editions" &&
            (editionCount > 0
              ? `Only the ${editionCount} marked edition${editionCount === 1 ? "" : "s"} count.`
              : hasAnyRelease
                ? "No editions marked yet, so any release still counts. Marking one narrows it to that edition."
                : "Mark the editions you want on the left. Each carries its own choice of whole album or specific tracks.")}
        </div>
      </div>

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
            desires={desires}
            scope={scope}
            selected={detailRelease}
            busy={busy}
            onSelect={setDetailRelease}
            onWantWhole={(id) => save(id, [])}
            onDrop={drop}
          />
          <TrackPane
            releaseMbid={detailRelease}
            scope={scope}
            desires={desires}
            busy={busy}
            onSave={save}
            onDrop={drop}
          />
        </div>
      )}
    </div>
  );
}

/** Earliest-dated release: the most canonical tracklist to pick songs from. */
/** Disk marker colour, matching the artist page: ○ none, ◐ partial, ● complete. */
function ownedColor(e: Edition): string {
  if (!e.owned) return "var(--text-muted)";
  return e.complete ? "var(--diff-add-text)" : "var(--warning-text)";
}

function earliest(editions: Edition[]): Edition | undefined {
  return [...editions].sort((a, b) => (a.date || "9999").localeCompare(b.date || "9999"))[0];
}

function ScopeChoice({
  on,
  disabled,
  onClick,
  children,
}: {
  on: boolean;
  disabled: boolean;
  onClick: () => void;
  children: React.ReactNode;
}) {
  return (
    <button
      className={on ? "btn btn-primary btn-sm" : "btn btn-secondary btn-sm"}
      aria-pressed={on}
      disabled={disabled}
      onClick={onClick}
    >
      {children}
    </button>
  );
}

/** Master: every edition, each carrying its own want state. */
function EditionList({
  editions,
  desires,
  scope,
  selected,
  busy,
  onSelect,
  onWantWhole,
  onDrop,
}: {
  editions: Edition[];
  desires: CollectionDesire[];
  scope: Scope | null;
  selected: string | null;
  busy: boolean;
  onSelect: (id: string) => void;
  onWantWhole: (id: string) => void;
  onDrop: (id: string) => void;
}) {
  const perEdition = scope === "editions";
  const ownedCount = editions.filter((e) => e.owned).length;

  return (
    <div className="card" style={{ padding: 0, overflow: "hidden" }}>
      <div className="row" style={{ justifyContent: "space-between", padding: "10px 12px 6px" }}>
        <span className="eyebrow">Editions · {editions.length}</span>
        {ownedCount > 0 && (
          <span className="dim mono" style={{ fontSize: 11 }}>{ownedCount} on disk</span>
        )}
      </div>
      <div style={{ maxHeight: 420, overflowY: "auto" }}>
        {editions.map((r) => {
          const desire = desires.find((d) => d.release_mb_id === r.id);
          const tracks = desire?.recording_mb_ids?.length ?? 0;
          const state = !desire ? "none" : tracks > 0 ? `${tracks} tracks` : "whole edition";
          const active = selected === r.id;
          return (
            <div
              key={r.id}
              onClick={() => onSelect(r.id)}
              style={{
                padding: "8px 12px",
                borderTop: "1px solid var(--border)",
                background: active ? "var(--accent-subtle)" : undefined,
                cursor: "pointer",
              }}
            >
              <div className="row" style={{ justifyContent: "space-between", gap: 8 }}>
                <div>
                  <div className="row" style={{ gap: 6 }}>
                    {/* Ownership is per edition: the release-group headline reports
                        only its best-owned one, which is exactly what hid a second
                        pressing before. */}
                    <span
                      style={{ color: ownedColor(r), fontSize: 12 }}
                      title={
                        !r.owned
                          ? "No files of this edition"
                          : r.complete
                            ? "Every track of this edition is on disk"
                            : "Some tracks of this edition are on disk"
                      }
                    >
                      {!r.owned ? "\u25cb" : r.complete ? "\u25cf" : "\u25d0"}
                    </span>
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
                    ]
                      .filter(Boolean)
                      .join(" \u00b7 ")}
                  </div>
                </div>
                <div className="row" style={{ gap: 6 }}>
                  <MBLink entity="release" mbid={r.id} />
                  {perEdition && (
                    <button
                      className={desire ? "btn btn-primary btn-sm" : "btn btn-secondary btn-sm"}
                      aria-pressed={!!desire}
                      disabled={busy}
                      title={desire ? "Wanted. Click to remove this edition." : "Want this whole edition."}
                      onClick={(e) => {
                        e.stopPropagation();
                        desire ? onDrop(r.id) : onWantWhole(r.id);
                      }}
                    >
                      Wanted
                    </button>
                  )}
                </div>
              </div>
              {perEdition && desire && (
                <div className="dim mono" style={{ fontSize: 11, marginTop: 2, color: "var(--accent-text)" }}>
                  {state}
                </div>
              )}
            </div>
          );
        })}
      </div>
    </div>
  );
}

/** Detail: the selected edition's tracklist, with per-edition selection. */
function TrackPane({
  releaseMbid,
  scope,
  desires,
  busy,
  onSave,
  onDrop,
}: {
  releaseMbid: string | null;
  scope: Scope | null;
  desires: CollectionDesire[];
  busy: boolean;
  onSave: (releaseMbid: string, recordings: string[]) => void;
  onDrop: (releaseMbid: string) => void;
}) {
  const tracks = useFetch<ReleaseTracks>(
    () => (releaseMbid ? api.get(`/releases/${releaseMbid}/tracks`) : Promise.resolve(null as never)),
    [releaseMbid]
  );

  if (!releaseMbid) return <div className="card"><div className="dim" style={{ fontSize: 12 }}>Pick an edition.</div></div>;

  // Which desire row this pane edits: the edition's own when picking editions, the
  // any-release row when songs are wanted from any pressing.
  const target = scope === "editions" ? releaseMbid : "";
  const desire = desires.find((d) => d.release_mb_id === target);
  const selected = desire?.recording_mb_ids ?? [];
  const list = tracks.data?.tracks ?? [];

  // Ticking a track on an edition that is not yet marked simply wants it, with that
  // track — requiring two clicks to express one intention is just friction.
  const editable = scope === "any-tracks" || scope === "editions";

  const toggle = (recordingId: string) => {
    const next = selected.includes(recordingId)
      ? selected.filter((x) => x !== recordingId)
      : [...selected, recordingId];
    // Clearing every track means the edition is no longer wanted at all — not
    // "the whole edition", which would silently widen what was asked for.
    if (next.length === 0 && scope === "editions") onDrop(releaseMbid);
    else onSave(target, next);
  };

  // Recordings asked for that this edition does not carry — a deluxe and a
  // standard pressing do not share a tracklist. Surfaced rather than dropped.
  const absent = selected.filter((id) => !list.some((t) => t.recording_id === id));

  return (
    <div className="card" style={{ padding: 0, overflow: "hidden" }}>
      <div style={{ padding: "10px 12px 6px" }}>
        <div className="eyebrow">Tracks</div>
        <div className="dim" style={{ fontSize: 11, marginTop: 2 }}>
          {tracks.data?.release.title}
          {scope === "any-tracks" && " · picking from this edition does not narrow the want"}
          {scope === "editions" && !desire && " · tick a track to want this edition"}
        </div>
        {absent.length > 0 && (
          <div style={{ fontSize: 11, marginTop: 4, color: "var(--warning-text)" }}>
            {absent.length} wanted {absent.length === 1 ? "song is" : "songs are"} not on this edition.
          </div>
        )}
      </div>
      <div style={{ maxHeight: 420, overflowY: "auto", borderTop: "1px solid var(--border)" }}>
        {tracks.loading && <div className="dim" style={{ fontSize: 11, padding: 12 }}>Loading tracklist…</div>}
        {tracks.err && <div className="dim" style={{ fontSize: 11, padding: 12 }}>Could not load this tracklist.</div>}
        {list.map((t) => {
          const multiDisc = list.some((x) => x.medium !== t.medium);
          return (
            <label
              key={t.track_id}
              className="row"
              style={{
                gap: 8, padding: "4px 12px", cursor: editable ? "pointer" : "default",
                opacity: editable ? 1 : 0.5,
              }}
            >
              <input
                type="checkbox"
                checked={selected.includes(t.recording_id)}
                disabled={busy || !editable}
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
