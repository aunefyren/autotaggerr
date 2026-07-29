import { useState } from "react";
import { Link } from "react-router-dom";
import { api, errMsg } from "../api";
import { useFetch } from "../hooks";
import { CollectionArtist } from "../types";
import { EmptyState, ErrorNote, Pill } from "../components/ui";
import { useToast } from "../toast";
import { MBLink } from "../components/MBLink";
import { AddArtistModal } from "../components/AddArtistModal";

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

export default function Collection() {
  const toast = useToast();
  const { data, err, loading, reload } = useFetch<CollectionArtist[]>(() => api.get("/artists"));
  const [rebuilding, setRebuilding] = useState(false);
  const [adding, setAdding] = useState(false);

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
  // A missing count only means something when something decides what is wanted:
  // the manager, or a follow that actually governs.
  const hasWanted = (ar: CollectionArtist) =>
    !ar.follow_governs || ar.monitored || (ar.picked_count ?? 0) > 0;

  return (
    <div className="stack">
      <div className="page-head">
        <h1>Collection</h1>
        <div className="row">
          <button className="btn btn-primary btn-sm" onClick={() => setAdding(true)}>Add artist</button>
          <button className="btn btn-secondary btn-sm" onClick={syncLidarr}>Sync from Lidarr</button>
          <button className="btn btn-secondary btn-sm" onClick={rebuild} disabled={rebuilding}>
            {rebuilding ? "Rebuilding…" : "Rebuild from library"}
          </button>
        </div>
      </div>
      <p className="muted" style={{ margin: 0, maxWidth: "70ch" }}>
        Album counts are what Autotaggerr found on disk. <strong>Wanted</strong> is what you asked
        for but do not have yet — either by following an artist, or by picking individual albums.
        Where disk and manager disagree, the album is flagged as a mismatch rather than one side
        silently winning.
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
                <th>Wanted</th>
              </tr>
            </thead>
            <tbody>
              {artists.map((ar) => (
                <tr key={ar.mb_id}>
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
                  <td><WantedSummary artist={ar} /></td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      )}

      {adding && <AddArtistModal onClose={() => setAdding(false)} onAdded={() => { setAdding(false); reload(); }} />}
    </div>
  );
}
