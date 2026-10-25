import { useEffect, useMemo, useState } from "react";
import { api, errMsg } from "../api";
import { CollectionArtist, CollectionPolicy } from "../types";
import { Modal } from "./ui";
import { DEFAULT_FOLLOW, FollowFields, FollowShape, describeFollow } from "./FollowFields";
import { useToast } from "../toast";

/**
 * Follow several artists at once, with one follow shape. Lists the artists Autotaggerr
 * governs that nobody follows yet, all ticked: the common case is "the artists in my
 * files", and unticking the few you do not care about is less work than ticking the
 * many you do. Artists a manager governs are not offered — the manager decides what
 * is wanted for them — and the dialog says how many that leaves out.
 */
export function FollowArtistsDialog({
  artists,
  onClose,
  onDone,
}: {
  artists: CollectionArtist[];
  onClose: () => void;
  onDone: () => void;
}) {
  const toast = useToast();
  const candidates = useMemo(
    () => artists.filter((a) => a.follow_governs && !a.monitored).sort((x, y) => x.name.localeCompare(y.name)),
    [artists]
  );
  const managed = artists.filter((a) => !a.follow_governs).length;
  const [chosen, setChosen] = useState<Set<string>>(() => new Set(candidates.map((a) => a.mb_id)));
  const [query, setQuery] = useState("");
  const [shape, setShape] = useState<FollowShape>(DEFAULT_FOLLOW);
  const [busy, setBusy] = useState(false);

  const shown = candidates.filter((a) => a.name.toLowerCase().includes(query.trim().toLowerCase()));
  const toggle = (mbid: string) =>
    setChosen((cur) => {
      const next = new Set(cur);
      if (next.has(mbid)) next.delete(mbid);
      else next.add(mbid);
      return next;
    });
  const setShown = (on: boolean) =>
    setChosen((cur) => {
      const next = new Set(cur);
      shown.forEach((a) => (on ? next.add(a.mb_id) : next.delete(a.mb_id)));
      return next;
    });

  const submit = async () => {
    setBusy(true);
    try {
      await api.post("/collection/follow", { artist_mb_ids: [...chosen], ...shape });
      toast("ok", `Following ${chosen.size} artist${chosen.size === 1 ? "" : "s"} — their discographies are being read; follow it on Activity`);
      onDone();
    } catch (e) {
      toast("err", errMsg(e));
      setBusy(false);
    }
  };

  return (
    <Modal title="Follow artists" onClose={onClose} wide>
      <div className="stack">
        <p className="muted" style={{ margin: 0, fontSize: 12, maxWidth: "68ch" }}>
          Following an artist wants what they released in the shape below, and what they release next. It is
          what turns "the albums I have" into "the albums I am missing".
        </p>
        {candidates.length === 0 ? (
          <span className="dim">Every artist Autotaggerr governs is followed already.</span>
        ) : (
          <>
            <div className="row" style={{ gap: 8, flexWrap: "wrap" }}>
              <input className="input" style={{ flex: "1 1 200px" }} value={query} onChange={(e) => setQuery(e.target.value)} placeholder="Filter artists" aria-label="Filter artists" />
              <button type="button" className="btn btn-ghost btn-sm" onClick={() => setShown(true)}>Tick shown</button>
              <button type="button" className="btn btn-ghost btn-sm" onClick={() => setShown(false)}>Untick shown</button>
            </div>
            <ul className="pick-list" style={{ maxHeight: 240, overflowY: "auto" }}>
              {shown.map((a) => (
                <li key={a.mb_id}>
                  <label className="row" style={{ gap: 8, padding: "4px 8px", cursor: "pointer" }}>
                    <input type="checkbox" checked={chosen.has(a.mb_id)} onChange={() => toggle(a.mb_id)} />
                    <span>{a.name}</span>
                    {a.catalogue_read_at && <span className="dim mono" style={{ fontSize: 11, marginLeft: "auto" }}>{a.catalogue_count} in discography</span>}
                  </label>
                </li>
              ))}
            </ul>
            <span className="dim" style={{ fontSize: 11 }}>
              {chosen.size} of {candidates.length} chosen
              {managed > 0 && ` · ${managed} artist${managed === 1 ? "" : "s"} under a manager not offered — the manager decides what is wanted for them`}
            </span>
          </>
        )}
        <div className="stack" style={{ gap: 6 }}>
          <span className="flabel">Want</span>
          <FollowFields value={shape} onChange={(patch) => setShape((s) => ({ ...s, ...patch }))} disabled={busy} idPrefix="bulk-follow" />
        </div>
        <div className="modal-actions">
          <button type="button" className="btn btn-ghost btn-sm" style={{ marginRight: "auto" }} onClick={onClose}>Cancel</button>
          <span className="dim" style={{ fontSize: 12 }}>Wants {describeFollow(shape)}</span>
          <button type="button" className="btn btn-primary btn-sm" disabled={busy || chosen.size === 0} onClick={submit}>
            {busy ? "Following…" : `Follow ${chosen.size} artist${chosen.size === 1 ? "" : "s"}`}
          </button>
        </div>
      </div>
    </Modal>
  );
}

const NEW_ARTIST_CHOICES: { value: CollectionPolicy["new_artist_follow"]; label: string; help: string }[] = [
  { value: "off", label: "Don't follow", help: "Only what you own is listed. Follow artists one by one, or with Follow artists." },
  { value: "new", label: "Follow new releases", help: "Wants what they release from the year they were found — not the back catalogue you chose not to own." },
  { value: "all", label: "Follow the whole discography", help: "Wants everything in the shape below, back catalogue included. On a large library that is a long missing list." },
];

/**
 * What happens to an artist the collection first meets in your files — a scan, an
 * import, a Lidarr sync. A collection-wide decision, stored on the collection policy
 * rather than in config.json, which is for how the process runs. Artists a manager
 * governs are never followed by it.
 */
export function NewArtistsDialog({ onClose }: { onClose: () => void }) {
  const toast = useToast();
  const [policy, setPolicy] = useState<CollectionPolicy | null>(null);
  const [busy, setBusy] = useState(false);

  useEffect(() => {
    api.get<CollectionPolicy>("/collection/policy").then(setPolicy).catch((e) => toast("err", errMsg(e)));
  }, [toast]);

  const save = async () => {
    if (!policy) return;
    setBusy(true);
    try {
      await api.put("/collection/policy", policy);
      toast("ok", "Saved — applies to artists found from now on");
      onClose();
    } catch (e) {
      toast("err", errMsg(e));
      setBusy(false);
    }
  };

  const shape: FollowShape = policy
    ? { follow_types: policy.follow_types, follow_secondary: policy.follow_secondary, follow_from_year: 0 }
    : DEFAULT_FOLLOW;

  return (
    <Modal title="New artists" onClose={onClose}>
      {!policy ? (
        <span className="dim">Loading…</span>
      ) : (
        <div className="stack">
          <p className="muted" style={{ margin: 0, fontSize: 12 }}>
            When a scan, an import or a Lidarr sync brings in an artist the collection does not have yet. Artists
            already in the collection keep what they have, and artists a manager governs are left to it.
          </p>
          <div className="stack" style={{ gap: 8 }} role="radiogroup" aria-label="New artists">
            {NEW_ARTIST_CHOICES.map((c) => (
              <label key={c.value} className="row" style={{ gap: 8, alignItems: "flex-start", cursor: "pointer" }}>
                <input
                  type="radio"
                  name="new-artist-follow"
                  checked={policy.new_artist_follow === c.value}
                  onChange={() => setPolicy({ ...policy, new_artist_follow: c.value })}
                  style={{ marginTop: 3 }}
                />
                <span className="stack" style={{ gap: 2 }}>
                  <span>{c.label}</span>
                  <span className="dim" style={{ fontSize: 11 }}>{c.help}</span>
                </span>
              </label>
            ))}
          </div>
          {policy.new_artist_follow !== "off" && (
            <div className="stack" style={{ gap: 6 }}>
              <span className="flabel">Want</span>
              <FollowFields
                value={shape}
                showYear={false}
                idPrefix="new-artists"
                onChange={(patch) =>
                  setPolicy({
                    ...policy,
                    ...(patch.follow_types !== undefined ? { follow_types: patch.follow_types } : {}),
                    ...(patch.follow_secondary !== undefined ? { follow_secondary: patch.follow_secondary } : {}),
                  })
                }
              />
            </div>
          )}
          <div className="modal-actions">
            <button type="button" className="btn btn-ghost btn-sm" onClick={onClose}>Cancel</button>
            <button type="button" className="btn btn-primary btn-sm" disabled={busy} onClick={save}>{busy ? "Saving…" : "Save changes"}</button>
          </div>
        </div>
      )}
    </Modal>
  );
}
