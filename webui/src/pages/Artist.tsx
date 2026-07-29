import { useState } from "react";
import { Link, useParams } from "react-router-dom";
import { api, errMsg } from "../api";
import { useFetch } from "../hooks";
import { ArtistDetail, CollectionArtist, CollectionReleaseGroup } from "../types";
import { ErrorNote, Pill } from "../components/ui";
import { MBLink } from "../components/MBLink";
import { useToast } from "../toast";

function ManagedBy({ managed_by }: { managed_by: string }) {
  if (managed_by === "lidarr") return <Pill kind="scan">Lidarr</Pill>;
  if (managed_by === "mixed") return <Pill kind="scan">Lidarr + native</Pill>;
  if (managed_by === "autotaggerr") return <Pill kind="chg">Native</Pill>;
  return (
    <Pill kind="off">
      <span title="This artist's library has no resolvable manager — reassign one on the Libraries page">Unknown</span>
    </Pill>
  );
}

/** How a disk-vs-catalog disagreement reads. The disk count is the one to trust. */
function discrepancyNote(g: CollectionReleaseGroup, manager: string): { label: string; title: string } | null {
  const cat = `${g.catalog_owned_tracks}/${g.catalog_total_tracks}`;
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

type Filter = "all" | "disk" | "wanted" | "missing";

export default function Artist() {
  const { mbid = "" } = useParams();
  const toast = useToast();

  const detail = useFetch<ArtistDetail>(() => api.get(`/artists/${mbid}`), [mbid]);
  // Live MusicBrainz read: browsing a catalogue is not the same as wanting it, so
  // the discography is never persisted.
  const disco = useFetch<CollectionReleaseGroup[]>(() => api.get(`/artists/${mbid}/discography`), [mbid]);

  const [busy, setBusy] = useState(false);
  const [filter, setFilter] = useState<Filter>("all");
  const [settingsOpen, setSettingsOpen] = useState(false);

  const artist = detail.data?.artist;
  const groups = disco.data ?? detail.data?.release_groups ?? [];

  const onDisk = groups.filter((g) => g.owned);
  const partial = onDisk.filter((g) => !g.complete);
  const wanted = groups.filter((g) => g.wanted);
  const missing = groups.filter((g) => g.wanted && !g.owned);
  const mismatched = groups.filter((g) => g.discrepancy !== "");

  const isLidarr = artist?.managed_by === "lidarr" || artist?.managed_by === "mixed";
  const managerLabel = isLidarr ? "Lidarr" : "MusicBrainz";

  const refresh = () => { detail.reload(); disco.reload(); };

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

  const shown = groups.filter((g) => {
    if (filter === "disk") return g.owned;
    if (filter === "wanted") return g.wanted;
    if (filter === "missing") return g.wanted && !g.owned;
    return true;
  });

  if (detail.err) return <ErrorNote message={detail.err} />;

  return (
    <div className="stack">
      <div>
        <Link to="/collection" className="dim mono" style={{ fontSize: 11 }}>← Collection</Link>
      </div>

      <div className="page-head">
        <div className="row" style={{ gap: 10 }}>
          <h1>{artist?.name ?? "Artist"}</h1>
          {artist && <ManagedBy managed_by={artist.managed_by} />}
          {artist?.origin === "manual" && (
            <span className="dim" style={{ fontSize: 11 }} title="Added by hand">added</span>
          )}
          <MBLink entity="artist" mbid={mbid} />
        </div>
      </div>

      <div className="grid-cards">
        <div className="card stat">
          <div className="n">{onDisk.length}</div>
          <div className="l">On disk</div>
        </div>
        <div className="card stat">
          <div className="n" style={{ color: partial.length ? "var(--warning-text)" : undefined }}>{partial.length}</div>
          <div className="l">Partial</div>
        </div>
        <div className="card stat">
          <div className="n" style={{ color: wanted.length ? "var(--accent-text)" : undefined }}>{wanted.length}</div>
          <div className="l">Wanted</div>
        </div>
        <div className="card stat">
          <div className="n" style={{ color: missing.length ? "var(--accent-text)" : undefined }}>{missing.length}</div>
          <div className="l">Missing</div>
        </div>
        <div className="card stat">
          <div className="n" style={{ color: mismatched.length ? "var(--warning-text)" : undefined }}>{mismatched.length}</div>
          <div className="l">Mismatch</div>
        </div>
      </div>

      {/* Shown for every artist, including Lidarr-managed ones, where it renders
          frozen. Replacing it with a note hid the state that was still driving
          "auto" wants on the rows below — the control that explains a state has to
          be present even when it cannot be used. */}
      {artist && (
        <FollowPanel
          artist={artist}
          busy={busy}
          expanded={settingsOpen}
          onToggleExpanded={() => setSettingsOpen((v) => !v)}
          onChange={updateFollow}
          manager={managerLabel}
        />
      )}

      <div className="row" style={{ justifyContent: "space-between", flexWrap: "wrap", gap: 8 }}>
        <div className="row" style={{ gap: 6 }}>
          <FilterTab on={filter === "all"} onClick={() => setFilter("all")}>All · {groups.length}</FilterTab>
          <FilterTab on={filter === "disk"} onClick={() => setFilter("disk")}>On disk · {onDisk.length}</FilterTab>
          <FilterTab on={filter === "wanted"} onClick={() => setFilter("wanted")}>Wanted · {wanted.length}</FilterTab>
          <FilterTab on={filter === "missing"} onClick={() => setFilter("missing")}>Missing · {missing.length}</FilterTab>
        </div>
        {disco.loading && <span className="dim" style={{ fontSize: 11 }}>Loading discography from MusicBrainz…</span>}
        {disco.err && <span className="dim" style={{ fontSize: 11 }}>MusicBrainz unreachable — showing what is already known.</span>}
      </div>

      {shown.length === 0 ? (
        <div className="card">
          <div className="dim" style={{ fontSize: 12 }}>
            {filter === "all" ? "Nothing to show yet." : "Nothing matches this filter."}
          </div>
        </div>
      ) : (
        <div className="tablewrap">
          <table className="data">
            <thead>
              <tr>
                <th style={{ width: 24 }}></th>
                <th>Release group</th>
                <th>Type</th>
                <th style={{ textAlign: "right" }}>Year</th>
                <th style={{ textAlign: "right" }}>Tracks</th>
                <th>Wanted</th>
                <th style={{ textAlign: "right" }}>Actions</th>
              </tr>
            </thead>
            <tbody>
              {shown.map((g) => (
                <ReleaseGroupRow
                  key={g.mb_id}
                  g={g}
                  manager={managerLabel}
                  artistMbid={mbid}
                  onChanged={refresh}
                />
              ))}
            </tbody>
          </table>
        </div>
      )}
    </div>
  );
}

function FilterTab({ on, onClick, children }: { on: boolean; onClick: () => void; children: React.ReactNode }) {
  return (
    <button className={on ? "btn btn-primary btn-sm" : "btn btn-secondary btn-sm"} onClick={onClick}>
      {children}
    </button>
  );
}

function ReleaseGroupRow({
  g,
  manager,
  artistMbid,
  onChanged,
}: {
  g: CollectionReleaseGroup;
  manager: string;
  artistMbid: string;
  onChanged: () => void;
}) {
  const toast = useToast();
  const [busy, setBusy] = useState(false);

  const partial = g.owned && !g.complete;
  const marker = !g.owned ? "○" : partial ? "◐" : "●";
  const markerColor = !g.owned ? "var(--text-muted)" : partial ? "var(--warning-text)" : "var(--diff-add-text)";
  const note = discrepancyNote(g, manager);
  const explicitlyWanted = g.wanted && g.wanted_source === "explicit";
  // A want the row cannot edit: it follows from following the artist, or from the
  // manager. Rendering it as a pressed toggle claimed a control that does nothing —
  // clicking never un-wanted it, because the reason lives elsewhere.
  const derivedWant = g.wanted && !explicitlyWanted;

  // What was actually asked for, in plain words — a refined want should never be a
  // mystery from the list.
  const wantSummary = (() => {
    if (!g.wanted || derivedWant) return "";
    const editions = g.desired_releases?.length ?? 0;
    const tracks = g.desired_recordings?.length ?? 0;
    const where = editions > 0 ? `${editions} edition${editions === 1 ? "" : "s"}` : "any release";
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

  const toggleWanted = async () => {
    setBusy(true);
    try {
      if (explicitlyWanted) {
        for (const rel of ["", ...(g.desired_releases ?? [])]) {
          const q = new URLSearchParams({ release_group_mb_id: g.mb_id, release_mb_id: rel });
          await api.del(`/artists/${artistMbid}/desires?${q.toString()}`);
        }
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
      <td style={{ color: markerColor }} title={!g.owned ? "Not on disk" : partial ? "Partially on disk" : "Complete on disk"}>
        {marker}
      </td>
      <td style={{ color: "var(--text)" }}>
        <div className="row" style={{ gap: 8 }}>
          <span>{g.title}</span>
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
              g.wanted_source === "manager"
                ? `${manager} monitors this album. Change it in ${manager}, or pin it here to keep it regardless.`
                : "Wanted because you follow this artist. Change the follow settings above, or pin it to keep it if you stop following."
            }
          >
            <Pill kind="off">{g.wanted_source === "manager" ? manager : "auto"}</Pill>
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
            disabled={busy || derivedWant}
            title={
              explicitlyWanted
                ? "Wanted. Click to remove."
                : derivedWant
                  ? g.wanted_source === "manager"
                    ? `Wanted because ${manager} monitors it — not something this page can switch off. Pin it to keep it regardless.`
                    : "Wanted because you follow this artist — not something this row can switch off. Unfollow above, or pin it to keep it."
                  : "Not wanted. Click to want this album — any release, whole album."
            }
            onClick={toggleWanted}
          >
            {g.wanted ? "Wanted" : "Want"}
          </button>
          {derivedWant && (
            <button
              className="btn btn-secondary btn-sm"
              disabled={busy}
              title="Ask for this album explicitly, so it stays wanted even if the reason above goes away"
              onClick={pinWanted}
            >
              Pin
            </button>
          )}
          {/* The album page is now a browsing destination too — it lists every
              edition and what is owned of each — so owning it is reason enough to
              open it, not only wanting it. Still disabled rather than hidden when
              there is nothing to see: a vanishing control shifts the row. */}
          <Link
            className="btn btn-ghost btn-sm"
            to={`/collection/${artistMbid}/${g.mb_id}`}
            aria-label="Editions and tracks"
            title={
              g.wanted
                ? "Editions and tracks — choose specific ones"
                : g.owned
                  ? "Editions — see what is on disk"
                  : "Mark it wanted first"
            }
            style={g.wanted || g.owned ? undefined : { opacity: 0.5, pointerEvents: "none" }}
            aria-disabled={!g.wanted && !g.owned}
          >
            ⚙
          </Link>
        </div>
      </td>
    </tr>
  );
}

const FOLLOW_TYPES = ["Album", "EP", "Single", "Live", "Compilation", "Other"];

/**
 * Following is a convenience — it auto-wants a *shape* of release — never a
 * precondition for picking albums by hand. The panel states what will be wanted,
 * because "follow" on its own tells you nothing.
 */
function FollowPanel({
  artist,
  busy,
  expanded,
  onToggleExpanded,
  onChange,
  manager,
}: {
  artist: CollectionArtist;
  busy: boolean;
  expanded: boolean;
  onToggleExpanded: () => void;
  onChange: (body: Record<string, unknown>) => void;
  manager: string;
}) {
  const types = (artist.follow_types || "Album,EP").split(",").map((t) => t.trim()).filter(Boolean);

  // When a manager owns the artist it decides what is wanted, so these controls are
  // frozen rather than hidden: the settings are still real and still shown, they
  // just do not govern until the artist is natively managed. Hiding them is what
  // produced albums marked "auto" with nothing on the page to explain them.
  const frozen = !artist.follow_governs;

  const toggleType = (t: string) => {
    const next = types.includes(t) ? types.filter((x) => x !== t) : [...types, t];
    onChange({ follow_types: next.join(",") });
  };

  const summary = () => {
    if (frozen) {
      return `${manager} decides what is wanted for this artist; Autotaggerr mirrors it. ${
        artist.monitored
          ? "These follow settings are kept but do not apply."
          : "Following would apply if this artist were natively managed."
      }`;
    }
    return artist.monitored
      ? `Automatically wants: ${types.join(", ") || "nothing"}${artist.follow_secondary ? ", including live albums, compilations and remixes" : ""}, as they are released.`
      : "Automatically want new releases of a chosen kind. You can pick individual albums either way.";
  };

  return (
    <div className="card" style={frozen ? { opacity: 0.75 } : undefined}>
      <div className="row" style={{ justifyContent: "space-between", gap: 12, flexWrap: "wrap" }}>
        <div>
          <div className="row" style={{ gap: 8 }}>
            <span style={{ color: "var(--text)" }}>Follow this artist</span>
            {frozen && <Pill kind="off">Managed by {manager}</Pill>}
          </div>
          <div className="dim" style={{ fontSize: 11 }}>{summary()}</div>
        </div>
        <div className="row" style={{ gap: 8 }}>
          <button className="btn btn-ghost btn-sm" onClick={onToggleExpanded}>
            {expanded ? "Hide settings" : "Settings"}
          </button>
          <button
            className={artist.monitored ? "btn btn-primary btn-sm" : "btn btn-secondary btn-sm"}
            aria-pressed={artist.monitored}
            disabled={busy || frozen}
            title={
              frozen
                ? `${manager} decides what is wanted for this artist — following has no effect until it is managed natively.`
                : artist.monitored
                  ? "Following. Click to stop."
                  : "Not following. Click to follow."
            }
            onClick={() => onChange({ monitored: !artist.monitored })}
          >
            {/* The label reads the state when on and the action when off. A stable
                "Follow" label was read as an invitation to click even while
                pressed and accent-filled — the fill alone cannot carry the state. */}
            {busy ? "Syncing…" : artist.monitored ? "Following" : "Follow"}
          </button>
        </div>
      </div>

      {expanded && (
        <div style={{ marginTop: 12, paddingTop: 12, borderTop: "1px solid var(--border)" }}>
          <div className="eyebrow" style={{ marginBottom: 6 }}>Automatically want</div>
          <div className="row" style={{ gap: 6, flexWrap: "wrap" }}>
            {FOLLOW_TYPES.map((t) => (
              <button
                key={t}
                className={types.includes(t) ? "btn btn-primary btn-sm" : "btn btn-secondary btn-sm"}
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
      )}
    </div>
  );
}
