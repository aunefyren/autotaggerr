import { useState } from "react";
import { api, errMsg } from "../api";
import { useFetch } from "../hooks";
import { ArtistDetail, CollectionArtist, CollectionReleaseGroup, Discrepancy } from "../types";
import { EmptyState, ErrorNote, Modal, Pill } from "../components/ui";
import { useToast } from "../toast";

function ManagedBy({ managed_by }: { managed_by: string }) {
  if (managed_by === "lidarr") return <Pill kind="scan">Lidarr</Pill>;
  if (managed_by === "mixed") return <Pill kind="scan">Lidarr + native</Pill>;
  return <Pill kind="chg">Native</Pill>;
}

/**
 * How a disk-vs-catalog disagreement reads. Autotaggerr walked the files, so the
 * disk count is the one to trust; the note explains what the manager thinks and
 * what to do about it. `manager` names the authority for the message.
 */
function discrepancyNote(
  g: CollectionReleaseGroup,
  manager: string
): { label: string; title: string } | null {
  const d: Discrepancy = g.discrepancy;
  const cat = `${g.catalog_owned_tracks}/${g.catalog_total_tracks}`;
  if (d === "stale_catalog")
    return {
      label: `${manager} ${cat}`,
      title: `${manager} reports ${cat} files, but ${g.owned_tracks} are on disk. ${manager} probably needs a rescan.`,
    };
  if (d === "not_indexed")
    return {
      label: `${manager} ${cat}`,
      title: `${manager} reports ${cat} files, but only ${g.owned_tracks} are indexed here — they may sit outside your configured libraries, or have not been scanned yet.`,
    };
  if (d === "unmapped")
    return {
      label: `not in ${manager}`,
      title: `These files are on disk but ${manager} has no matching album, so it is not tracking them.`,
    };
  return null;
}

export default function Collection() {
  const toast = useToast();
  const { data, err, loading, reload } = useFetch<CollectionArtist[]>(() => api.get("/artists"));
  const [selected, setSelected] = useState<string | null>(null);
  const [rebuilding, setRebuilding] = useState(false);

  const rebuild = async () => {
    setRebuilding(true);
    try {
      const r = await api.post<{ artists: number; owned_release_groups: number }>("/collection/rebuild");
      toast("ok", `Collection rebuilt — ${r.artists} artists, ${r.owned_release_groups} albums`);
      reload();
    } catch (e) {
      toast("err", errMsg(e));
    } finally {
      setRebuilding(false);
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
  const hasWanted = (ar: CollectionArtist) => ar.monitored || ar.managed_by === "lidarr" || ar.managed_by === "mixed";

  return (
    <div className="stack">
      <div className="page-head">
        <h1>Collection</h1>
        <div className="row">
          <button className="btn btn-secondary btn-sm" onClick={syncLidarr}>Sync from Lidarr</button>
          <button className="btn btn-secondary btn-sm" onClick={rebuild} disabled={rebuilding}>
            {rebuilding ? "Rebuilding…" : "Rebuild from library"}
          </button>
        </div>
      </div>
      <p className="muted" style={{ margin: 0, maxWidth: "70ch" }}>
        Artists in your library. Album counts are what Autotaggerr found on disk; Lidarr and
        monitored discographies supply what <em>should</em> be there. Where the two disagree the
        album is flagged as a mismatch rather than one side silently winning.
      </p>

      {err && <ErrorNote message={err} />}
      {!err && !loading && artists.length === 0 && (
        <EmptyState
          icon="♫"
          message="No artists yet. Run a scan, then rebuild the collection from your library."
          action={<button className="btn btn-primary btn-sm" onClick={rebuild} disabled={rebuilding}>Rebuild from library</button>}
        />
      )}

      {artists.length > 0 && (
        <div className="tablewrap">
          <table className="data">
            <thead>
              <tr>
                <th>Artist</th>
                <th>Managed by</th>
                <th style={{ textAlign: "right" }}>Albums</th>
                <th style={{ textAlign: "right" }}>Partial</th>
                <th style={{ textAlign: "right" }}>Missing</th>
                <th style={{ textAlign: "right" }}>Mismatch</th>
                <th>Monitored</th>
              </tr>
            </thead>
            <tbody>
              {artists.map((ar) => (
                <tr key={ar.mb_id} style={{ cursor: "pointer" }} onClick={() => setSelected(ar.mb_id)}>
                  <td style={{ color: "var(--text)" }}>{ar.name}</td>
                  <td><ManagedBy managed_by={ar.managed_by} /></td>
                  <td className="num">{ar.owned_count ?? 0}</td>
                  <td className="num" style={{ color: (ar.partial_count ?? 0) > 0 ? "var(--warning-text)" : "var(--text-dim)" }}>
                    {(ar.partial_count ?? 0) > 0 ? ar.partial_count : "—"}
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
                  <td>{ar.monitored ? <Pill kind="ok">Monitored</Pill> : <span className="dim">no</span>}</td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      )}

      {selected && <ArtistModal mbid={selected} onClose={() => setSelected(null)} onChanged={reload} />}
    </div>
  );
}

function ArtistModal({ mbid, onClose, onChanged }: { mbid: string; onClose: () => void; onChanged: () => void }) {
  const toast = useToast();
  const { data, err, loading, reload } = useFetch<ArtistDetail>(() => api.get(`/artists/${mbid}`), [mbid]);
  const [busy, setBusy] = useState(false);

  const artist = data?.artist;
  const groups = data?.release_groups ?? [];
  const owned = groups.filter((g) => g.owned);
  const missing = groups.filter((g) => !g.owned);
  const partial = owned.filter((g) => !g.complete).length;
  const mismatched = groups.filter((g) => g.discrepancy !== "").length;
  const isLidarr = artist?.managed_by === "lidarr" || artist?.managed_by === "mixed";
  const showMissing = !!artist && (artist.monitored || isLidarr);
  const managerLabel = isLidarr ? "Lidarr" : "MusicBrainz";

  const setMonitored = async (monitored: boolean) => {
    setBusy(true);
    try {
      await api.post(`/artists/${mbid}/monitor`, { monitored });
      toast("ok", monitored ? "Monitoring — discography synced" : "Stopped monitoring");
      reload();
      onChanged();
    } catch (e) {
      toast("err", errMsg(e));
    } finally {
      setBusy(false);
    }
  };

  return (
    <Modal title={artist?.name ?? "Artist"} onClose={onClose} wide>
      {loading && <div className="muted">Loading…</div>}
      {err && <div className="help-err">{err}</div>}
      {artist && (
        <div className="stack">
          <div className="row" style={{ justifyContent: "space-between", flexWrap: "wrap", gap: 10 }}>
            <div className="row" style={{ gap: 10 }}>
              <ManagedBy managed_by={artist.managed_by} />
              <span className="muted">
                {owned.length} on disk{partial > 0 ? ` (${partial} partial)` : ""}
                {showMissing ? ` · ${missing.length} missing` : ""}
              </span>
              {mismatched > 0 && <Pill kind="warn">{mismatched} mismatch</Pill>}
            </div>
            {isLidarr ? (
              <span className="dim" style={{ fontSize: 12 }}>Monitoring is managed by Lidarr.</span>
            ) : (
              <button
                className={artist.monitored ? "btn btn-ghost btn-sm" : "btn btn-primary btn-sm"}
                disabled={busy}
                onClick={() => setMonitored(!artist.monitored)}
              >
                {busy ? "Syncing…" : artist.monitored ? "Stop monitoring" : "Monitor artist"}
              </button>
            )}
          </div>

          {!artist.monitored && !isLidarr && (
            <div className="dim" style={{ fontSize: 12 }}>Monitor this artist to discover the studio albums and EPs you're missing.</div>
          )}

          <div className="scroll">
            <ReleaseGroupList title="On disk" groups={owned} manager={managerLabel} owned />
            {showMissing && <ReleaseGroupList title="Missing" groups={missing} manager={managerLabel} />}
          </div>
        </div>
      )}
    </Modal>
  );
}

function ReleaseGroupList({
  title,
  groups,
  manager,
  owned,
}: {
  title: string;
  groups: CollectionReleaseGroup[];
  manager: string;
  owned?: boolean;
}) {
  if (groups.length === 0) return null;
  return (
    <div style={{ marginTop: 8 }}>
      <div className="eyebrow" style={{ marginBottom: 6 }}>{title} · {groups.length}</div>
      <div style={{ display: "flex", flexDirection: "column", gap: 2 }}>
        {groups.map((g) => {
          const partial = owned && !g.complete;
          const marker = !owned ? "○" : partial ? "◐" : "●";
          const markerColor = !owned ? "var(--text-muted)" : partial ? "var(--warning-text)" : "var(--diff-add-text)";
          const note = discrepancyNote(g, manager);
          return (
            <div key={g.mb_id} className="row" style={{ justifyContent: "space-between", padding: "4px 0", borderBottom: "1px solid var(--border)" }}>
              <div className="row" style={{ gap: 8 }}>
                <span style={{ color: markerColor }}>{marker}</span>
                <span style={{ color: "var(--text)" }}>{g.title}</span>
                <span className="dim mono" style={{ fontSize: 11 }}>{g.primary_type}</span>
              </div>
              <div className="row" style={{ gap: 10 }}>
                {note && (
                  <span className="mono" style={{ fontSize: 11, color: "var(--warning-text)" }} title={note.title}>
                    ⚠ {note.label}
                  </span>
                )}
                {owned && g.total_tracks > 0 && (
                  <span className="mono" style={{ fontSize: 11, color: partial ? "var(--warning-text)" : "var(--text-dim)" }}>
                    {g.owned_tracks}/{g.total_tracks}
                  </span>
                )}
                <span className="dim mono" style={{ fontSize: 11 }}>{(g.first_release_date || "").slice(0, 4)}</span>
              </div>
            </div>
          );
        })}
      </div>
    </div>
  );
}
