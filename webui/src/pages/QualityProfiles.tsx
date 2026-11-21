import { FormEvent, useState } from "react";
import { api, errMsg } from "../api";
import { useFetch } from "../hooks";
import { QualityDefinition, QualityID, QualityProfile, QualityTier } from "../types";
import { ConfirmDialog, EmptyState, ErrorNote, Modal, Pill } from "../components/ui";
import { useToast } from "../toast";

/**
 * Quality profiles: what a library (or an artist) wants its copies to be. A profile
 * ranks the qualities — some tied as equally good — says which are acceptable, and
 * which is good enough to stop at. Today it marks wanted albums held below the cutoff
 * as Upgrade; it is shaped to decide what to grab once Autotaggerr searches indexers.
 * See docs/quality.md.
 */
export default function QualityProfiles() {
  const toast = useToast();
  const { data, err, loading, reload } = useFetch<QualityProfile[]>(() => api.get("/quality-profiles"));
  const ladder = useFetch<{ qualities: QualityDefinition[] }>(() => api.get("/quality"));
  const [editing, setEditing] = useState<QualityProfile | "new" | null>(null);
  const [deleting, setDeleting] = useState<QualityProfile | null>(null);
  const [busy, setBusy] = useState(false);
  const names = nameMap(ladder.data?.qualities ?? []);

  const remove = async () => {
    if (!deleting) return;
    setBusy(true);
    try {
      await api.del(`/quality-profiles/${deleting.id}`);
      toast("ok", `${deleting.name} deleted`);
      setDeleting(null);
      reload();
    } catch (e) {
      toast("err", errMsg(e));
    } finally {
      setBusy(false);
    }
  };

  return (
    <div className="stack">
      <div className="page-head">
        <h1>Quality profiles</h1>
        <button className="btn btn-primary btn-sm" onClick={() => setEditing("new")} disabled={!ladder.data}>Add profile</button>
      </div>
      <p className="muted" style={{ margin: 0, maxWidth: "68ch" }}>
        The quality albums are wanted in. A profile ranks the qualities, says which are acceptable, and
        which is good enough to stop at. A wanted album held below that cutoff is marked <strong>Upgrade</strong> on
        the collection. A library uses one profile, or none, and an artist can override it. Lidarr decides
        quality in its own libraries.
      </p>

      {(err || ladder.err) && <ErrorNote message={err || ladder.err || ""} />}
      {!err && !loading && data && data.length === 0 && (
        <EmptyState icon="◈" message="No quality profiles. Without one, nothing is ever marked for an upgrade."
          action={<button className="btn btn-primary btn-sm" onClick={() => setEditing("new")} disabled={!ladder.data}>Add profile</button>} />
      )}

      {data && data.length > 0 && (
        <div className="tablewrap">
          <table className="data">
            <thead>
              <tr><th>Name</th><th>Accepts</th><th>Good enough at</th><th>Upgrades</th><th>Used by</th><th style={{ textAlign: "right" }}>Actions</th></tr>
            </thead>
            <tbody>
              {data.map((p) => (
                <tr key={p.id}>
                  <td style={{ color: "var(--text)" }}>{p.name}</td>
                  <td style={{ fontSize: 12 }}>{acceptsLine(p.tiers, names)}</td>
                  <td style={{ fontSize: 12 }}>{tierName(p.tiers[p.cutoff_tier], names)}</td>
                  <td>{p.upgrade_allowed ? <Pill kind="ok">On</Pill> : <Pill kind="off">Off</Pill>}</td>
                  <td className="dim" style={{ fontSize: 12 }}>{usedBy(p) || "—"}</td>
                  <td>
                    <div className="row" style={{ justifyContent: "flex-end" }}>
                      <button className="btn btn-secondary btn-sm" onClick={() => setEditing(p)} disabled={!ladder.data}>Edit</button>
                      <button className="btn btn-ghost btn-sm" onClick={() => setDeleting(p)} style={{ color: "var(--danger-text)" }}>Delete</button>
                    </div>
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      )}

      {editing && ladder.data && (
        <EditProfile
          profile={editing === "new" ? null : editing}
          ladder={ladder.data.qualities}
          onClose={() => setEditing(null)}
          onSaved={(name) => { setEditing(null); reload(); toast("ok", `${name} saved`); }}
        />
      )}
      {deleting && (
        <ConfirmDialog
          title={`Delete ${deleting.name}?`}
          danger
          busy={busy}
          confirmLabel="Delete profile"
          onConfirm={remove}
          onCancel={() => setDeleting(null)}
          body={
            usedBy(deleting) ? (
              <p style={{ margin: 0 }}>
                {usedBy(deleting)} use it. They are left with no quality profile, so their albums stop being
                marked for an upgrade until another is chosen. No file changes.
              </p>
            ) : (
              <p style={{ margin: 0 }}>Nothing uses it. No file changes.</p>
            )
          }
        />
      )}
    </div>
  );
}

function nameMap(ladder: QualityDefinition[]): Record<QualityID, string> {
  return Object.fromEntries(ladder.map((d) => [d.id, d.name]));
}

function tierName(tier: QualityTier | undefined, names: Record<QualityID, string>): string {
  if (!tier) return "—";
  return tier.qualities.map((q) => names[q] ?? q).join(" / ");
}

/** The acceptable qualities, best first: "FLAC 24bit / FLAC, MP3-320". */
function acceptsLine(tiers: QualityTier[], names: Record<QualityID, string>): string {
  const allowed = [...tiers].reverse().filter((t) => t.allowed);
  return allowed.length ? allowed.map((t) => tierName(t, names)).join(", ") : "Nothing";
}

/** "Music, Sandbox and 2 artists" — who a profile applies to. */
function usedBy(p: QualityProfile): string {
  const parts = [...(p.libraries ?? [])];
  if (p.artists) parts.push(`${p.artists} artist${p.artists === 1 ? "" : "s"}`);
  if (parts.length <= 1) return parts.join("");
  return `${parts.slice(0, -1).join(", ")} and ${parts[parts.length - 1]}`;
}

/** Every known quality in its own tier, worst first, none allowed: a new profile's start. */
function ladderTiers(ladder: QualityDefinition[]): QualityTier[] {
  return ladder.map((d) => ({ qualities: [d.id], allowed: false }));
}

function EditProfile({ profile, ladder, onClose, onSaved }: {
  profile: QualityProfile | null;
  ladder: QualityDefinition[];
  onClose: () => void;
  onSaved: (name: string) => void;
}) {
  const toast = useToast();
  const names = nameMap(ladder);
  const [name, setName] = useState(profile?.name ?? "");
  // Worst first, as stored; shown best first.
  const [tiers, setTiers] = useState<QualityTier[]>(profile?.tiers.length ? profile.tiers : ladderTiers(ladder));
  // The cutoff as a quality, not an index, so it follows its tier through every move,
  // merge and split instead of silently pointing at whatever lands in its slot.
  const [cutoff, setCutoff] = useState<QualityID>(profile ? profile.tiers[profile.cutoff_tier]?.qualities[0] ?? "" : "");
  const [upgrades, setUpgrades] = useState(profile?.upgrade_allowed ?? true);
  const [busy, setBusy] = useState(false);

  const cutoffIndex = tiers.findIndex((t) => t.qualities.includes(cutoff));
  const allowedCount = tiers.filter((t) => t.allowed).length;
  const problem =
    !name.trim() ? "Give the profile a name." :
    allowedCount === 0 ? "Allow at least one quality." :
    cutoffIndex < 0 || !tiers[cutoffIndex].allowed ? "Choose what is good enough — it has to be an allowed quality." :
    "";

  const swap = (i: number, j: number) => setTiers((cur) => {
    if (j < 0 || j >= cur.length) return cur;
    const next = [...cur];
    [next[i], next[j]] = [next[j], next[i]];
    return next;
  });
  // Ties a tier with the one below it (worse): both become equally good.
  const mergeDown = (i: number) => setTiers((cur) => {
    if (i <= 0) return cur;
    const next = [...cur];
    next[i - 1] = { qualities: [...next[i - 1].qualities, ...next[i].qualities], allowed: next[i - 1].allowed || next[i].allowed };
    next.splice(i, 1);
    return next;
  });
  // Unties a tier: each quality its own rank, in ladder order, where the tier stood.
  const split = (i: number) => setTiers((cur) => {
    const order = ladder.map((d) => d.id);
    const parts = [...cur[i].qualities]
      .sort((a, b) => order.indexOf(a) - order.indexOf(b))
      .map((q) => ({ qualities: [q], allowed: cur[i].allowed }));
    const next = [...cur];
    next.splice(i, 1, ...parts);
    return next;
  });
  const setAllowed = (i: number, allowed: boolean) => setTiers((cur) => cur.map((t, k) => (k === i ? { ...t, allowed } : t)));

  const submit = async (e: FormEvent) => {
    e.preventDefault();
    if (problem) return;
    setBusy(true);
    const body = { name: name.trim(), tiers, cutoff_tier: cutoffIndex, upgrade_allowed: upgrades };
    try {
      if (profile) await api.put(`/quality-profiles/${profile.id}`, body);
      else await api.post("/quality-profiles", body);
      onSaved(body.name);
    } catch (e) {
      toast("err", errMsg(e));
      setBusy(false);
    }
  };

  // Best first on screen; i is always the stored (worst-first) index.
  const shown = tiers.map((t, i) => ({ t, i })).reverse();
  const allowedShown = shown.filter(({ t }) => t.allowed);

  return (
    <Modal title={profile ? `Edit ${profile.name}` : "Add quality profile"} onClose={onClose} wide>
      <form onSubmit={submit} className="stack">
        <div className="field">
          <label className="flabel">Name</label>
          <input className="input" value={name} onChange={(e) => setName(e.target.value)} placeholder="Lossless" autoFocus={!profile} />
        </div>

        <div className="field">
          <label className="flabel">Qualities, best first</label>
          <div className="tablewrap">
            <table className="data">
              <thead>
                <tr><th style={{ width: 70 }}>Allowed</th><th>Quality</th><th style={{ textAlign: "right" }}>Rank</th></tr>
              </thead>
              <tbody>
                {shown.map(({ t, i }, row) => (
                  <tr key={t.qualities.join("|")}>
                    <td>
                      <input type="checkbox" checked={t.allowed} onChange={(e) => setAllowed(i, e.target.checked)}
                        aria-label={`Allow ${tierName(t, names)}`} />
                    </td>
                    <td style={{ color: t.allowed ? "var(--text)" : "var(--text-muted)" }}>
                      {tierName(t, names)}
                      {i === cutoffIndex && <> <Pill kind="ok">Good enough</Pill></>}
                    </td>
                    <td>
                      <div className="row" style={{ justifyContent: "flex-end", gap: 4 }}>
                        <button type="button" className="btn btn-ghost btn-sm" disabled={row === 0} onClick={() => swap(i, i + 1)}
                          title="Rank higher" aria-label={`Rank ${tierName(t, names)} higher`}>↑</button>
                        <button type="button" className="btn btn-ghost btn-sm" disabled={row === shown.length - 1} onClick={() => swap(i, i - 1)}
                          title="Rank lower" aria-label={`Rank ${tierName(t, names)} lower`}>↓</button>
                        {i > 0 && (
                          <button type="button" className="btn btn-ghost btn-sm" onClick={() => mergeDown(i)}
                            title={`Count ${tierName(t, names)} and ${tierName(tiers[i - 1], names)} as equally good`}>Tie with below</button>
                        )}
                        {t.qualities.length > 1 && (
                          <button type="button" className="btn btn-ghost btn-sm" onClick={() => split(i)}
                            title="Rank each of these on its own">Untie</button>
                        )}
                      </div>
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        </div>

        <div className="field">
          <label className="flabel">Good enough at</label>
          <select className="select" value={cutoffIndex >= 0 && tiers[cutoffIndex].allowed ? cutoff : ""}
            onChange={(e) => setCutoff(e.target.value)}>
            <option value="" disabled>Choose an allowed quality</option>
            {allowedShown.map(({ t }) => (
              <option key={t.qualities[0]} value={t.qualities[0]}>{tierName(t, names)}</option>
            ))}
          </select>
          <span className="dim" style={{ fontSize: 11 }}>
            An album held at this quality or better is done. Below it, it is worth replacing.
          </span>
        </div>

        <label className="row" style={{ gap: 8, cursor: "pointer" }}>
          <input type="checkbox" checked={upgrades} onChange={(e) => setUpgrades(e.target.checked)} />
          <span style={{ fontSize: 12 }}>Ask for upgrades</span>
        </label>

        {/* What the choices add up to, in one sentence (style guide: derived summary line). */}
        <p className="muted" style={{ margin: 0, fontSize: 12 }}>
          {problem || (
            <>
              Accepts {acceptsLine(tiers, names)}.{" "}
              {upgrades
                ? <>A wanted album below {tierName(tiers[cutoffIndex], names)} is marked Upgrade.</>
                : <>Never asks for an upgrade.</>}
            </>
          )}
        </p>

        <div className="modal-actions">
          <button type="button" className="btn btn-secondary btn-sm" onClick={onClose} disabled={busy}>Cancel</button>
          <button className="btn btn-primary btn-sm" disabled={busy || !!problem}>{busy ? "Saving…" : "Save profile"}</button>
        </div>
      </form>
    </Modal>
  );
}
