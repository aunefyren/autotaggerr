import { useEffect, useMemo, useState } from "react";
import { api, errMsg } from "../api";
import { OrganisePlan, OrganiseRow, OrganiseScope } from "../types";
import { ErrorNote, Modal, Pill } from "./ui";
import { useToast } from "../toast";

const CONFLICT_LABEL: Record<string, string> = {
  clash: "Same target",
  exists: "Target exists",
};

/**
 * Organise: move files to where their library's naming templates put them. The dry run
 * is the review — every proposed move as a row, From struck and To added in the diff's
 * colours, each with its own Skip — and the commit is the one button under it. A row
 * that would collide blocks the commit until it is skipped, rather than a winner being
 * picked. Nothing moves until Organise is pressed, and the job moves only what was
 * reviewed.
 */
export function OrganiseDialog({
  scope,
  title,
  onClose,
}: {
  scope: OrganiseScope;
  /** What is being organised, for the heading: a library, an artist or an album name. */
  title: string;
  onClose: () => void;
}) {
  const toast = useToast();
  const [plan, setPlan] = useState<OrganisePlan | null>(null);
  const [rows, setRows] = useState<OrganiseRow[]>([]);
  const [err, setErr] = useState("");
  const [busy, setBusy] = useState(false);
  const [showSkipped, setShowSkipped] = useState(false);

  useEffect(() => {
    let live = true;
    api
      .post<OrganisePlan>("/organise/preview", scope)
      .then((p) => {
        if (!live) return;
        setPlan(p);
        setRows(p.moves);
      })
      .catch((e) => live && setErr(errMsg(e)));
    return () => {
      live = false;
    };
  }, [scope.library_id, scope.artist_mb_id, scope.release_group_mb_id]);

  // Conflicts are recomputed as rows are skipped: skipping one side of a clash frees
  // the other. The server checks again on commit; this is so the table says it first.
  const marked = useMemo(() => {
    const targets = new Map<string, number>();
    const leaving = new Set<string>();
    for (const r of rows) {
      if (r.skip) continue;
      targets.set(r.to!.toLowerCase(), (targets.get(r.to!.toLowerCase()) ?? 0) + 1);
      leaving.add(r.from.toLowerCase());
    }
    return rows.map((r) => {
      if (r.skip) return { ...r, conflict: "" };
      const key = r.to!.toLowerCase();
      if ((targets.get(key) ?? 0) > 1) return { ...r, conflict: "clash" };
      if (leaving.has(key)) return { ...r, conflict: "" };
      return r; // "exists" comes from the server, which can see the disk
    });
  }, [rows]);

  const moving = marked.filter((r) => !r.skip);
  const blocked = moving.filter((r) => r.conflict);

  const commit = async () => {
    setBusy(true);
    try {
      await api.post("/organise", { ...scope, rows, move_extras: true });
      toast("info", `Organising ${moving.length} file${moving.length === 1 ? "" : "s"} — see Activity`);
      onClose();
    } catch (e) {
      toast("err", errMsg(e));
      setBusy(false);
    }
  };

  const setSkip = (i: number, skip: boolean) => setRows((cur) => cur.map((r, j) => (j === i ? { ...r, skip } : r)));

  return (
    <Modal title={`Organise ${title}`} onClose={onClose} xl>
      <div className="stack">
        {err && <ErrorNote message={err} />}
        {!plan && !err && <span className="dim">Working out where every file belongs…</span>}
        {plan && (
          <>
            {/* "Already in place" is said only of files that were checked and are. It used to
                open every plan with no moves, so a plan where every file was skipped read
                as "nothing to do" with the reason one click away. */}
            <p className="muted" style={{ margin: 0, fontSize: 12 }}>
              {plan.moves.length > 0
                ? `${plan.moves.length} file${plan.moves.length === 1 ? "" : "s"} would move`
                : plan.skipped.length > 0
                  ? "Nothing here can be moved"
                  : plan.in_place > 0
                    ? "Every file is already where its library's naming templates put it"
                    : "No identified files to organise here"}
              {plan.in_place > 0 && ` · ${plan.in_place} already in place`}
              {plan.skipped.length > 0 && ` · ${plan.skipped.length} cannot be moved`}
              . Files are renamed within the library; folders left empty are removed, and Plex is asked to rescan.
            </p>

            {/* Why files cannot be moved, by reason — stated up front, since a plan with
                skipped files is one whose answer is in the reasons. */}
            {plan.skipped.length > 0 && (
              <ul className="stack" style={{ gap: 2, margin: 0, paddingLeft: 18, fontSize: 12 }}>
                {skippedReasons(plan.skipped).map(([reason, count]) => (
                  <li key={reason}>
                    <span className="mono">{count}</span> file{count === 1 ? "" : "s"}: {reason}
                  </li>
                ))}
              </ul>
            )}

            {rows.length > 0 && (
              <div className="tablewrap" style={{ maxHeight: "55vh", overflow: "auto" }}>
                <table className="data">
                  <thead>
                    <tr>
                      <th style={{ width: 60 }}>Skip</th>
                      <th>From → To</th>
                      <th style={{ width: 140 }}>Status</th>
                    </tr>
                  </thead>
                  <tbody>
                    {marked.map((r, i) => (
                      <tr key={r.item_id} className={r.conflict ? "row-conflict" : r.skip ? "row-skipped" : undefined}>
                        <td>
                          <input type="checkbox" checked={!!r.skip} disabled={busy} onChange={(e) => setSkip(i, e.target.checked)} aria-label={`Skip ${r.from_rel}`} />
                        </td>
                        <td>
                          <div className="stack" style={{ gap: 3 }}>
                            <span className="diffv rem mono" style={{ fontSize: 12 }}>{r.from_rel}</span>
                            <span className="diffv add mono" style={{ fontSize: 12 }}>{r.to_rel}</span>
                          </div>
                        </td>
                        <td>
                          {r.skip ? (
                            <Pill kind="off">Skipped</Pill>
                          ) : r.conflict ? (
                            <span title={r.conflict === "exists" ? "Something not moved by this plan already sits there. Organise never deletes — move or remove it first, or skip this file." : "Another file of this plan would be given the same name. Skip one of them."}>
                              <Pill kind="err">{CONFLICT_LABEL[r.conflict] ?? r.conflict}</Pill>
                            </span>
                          ) : (
                            <Pill kind="ok">Move</Pill>
                          )}
                        </td>
                      </tr>
                    ))}
                  </tbody>
                </table>
              </div>
            )}

            {plan.skipped.length > 0 && (
              <div className="stack" style={{ gap: 6 }}>
                <button type="button" className="btn btn-ghost btn-sm" style={{ alignSelf: "flex-start" }} onClick={() => setShowSkipped((v) => !v)}>
                  {showSkipped ? "Hide" : "Show"} the {plan.skipped.length} file{plan.skipped.length === 1 ? "" : "s"} that cannot be moved
                </button>
                {showSkipped && (
                  <div className="stack" style={{ gap: 2, maxHeight: "25vh", overflow: "auto" }}>
                    {plan.skipped.map((s) => (
                      <div key={s.item_id} style={{ fontSize: 11 }}>
                        <span className="path">{s.from_rel}</span> <span className="dim">— {s.reason}</span>
                      </div>
                    ))}
                  </div>
                )}
              </div>
            )}

            {blocked.length > 0 && (
              <span className="help-err">
                {blocked.length} move{blocked.length === 1 ? "" : "s"} would collide — skip {blocked.length === 1 ? "it" : "them"}, or fix what is in the way, to organise.
              </span>
            )}
          </>
        )}

        <div className="modal-actions">
          <button type="button" className="btn btn-ghost btn-sm" onClick={onClose}>Cancel</button>
          <button className="btn btn-primary btn-sm" disabled={busy || !plan || moving.length === 0 || blocked.length > 0} onClick={commit}>
            {busy ? "Queuing…" : `Organise ${moving.length} file${moving.length === 1 ? "" : "s"}`}
          </button>
        </div>
      </div>
    </Modal>
  );
}

/** Skipped files counted by reason, most common first. */
function skippedReasons(skipped: OrganiseRow[]): [string, number][] {
  const counts = new Map<string, number>();
  for (const row of skipped) {
    const reason = row.reason ?? "no reason given";
    counts.set(reason, (counts.get(reason) ?? 0) + 1);
  }
  return [...counts].sort((a, b) => b[1] - a[1]);
}
