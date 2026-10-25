import { FormEvent, useEffect, useRef, useState } from "react";
import { Link } from "react-router-dom";
import { api, errMsg } from "../api";
import { useDebounced } from "../hooks";
import { ArtistInfo, ArtistSearchResult } from "../types";
import { Modal, Pill } from "./ui";
import { MBLink } from "./MBLink";
import { useToast } from "../toast";
import { DEFAULT_FOLLOW, FollowFields, FollowShape, describeFollow } from "./FollowFields";

interface ReleaseGroupHit {
  id: string;
  title: string;
  "primary-type": string;
  "secondary-types": string[] | null;
  "first-release-date": string;
}

interface Detail {
  info?: ArtistInfo;
  groups?: ReleaseGroupHit[];
  error?: string;
}

const TYPE_LABELS: [string, string, string][] = [
  ["Album", "album", "albums"],
  ["EP", "EP", "EPs"],
  ["Single", "single", "singles"],
];

/** "Group · Louisville, US · 1986–1992" — who the artist is, in one line. */
function factsLine(r: ArtistSearchResult, info?: ArtistInfo): string {
  const begin = info?.begin?.slice(0, 4);
  const end = info?.end?.slice(0, 4);
  const active = begin ? `${begin}–${end ?? (info?.ended ? "?" : "")}` : "";
  return [info?.type || r.type, info?.area || r.country, active].filter(Boolean).join(" · ");
}

/**
 * Adds an artist to the collection before any of their files are owned — the thing a
 * rebuild-from-disk can never do. Following the artist afterwards is what pulls in
 * their discography.
 *
 * A name search returns twenty artists called the same thing, and a description in
 * brackets is not enough to tell which is meant. So this is a master/detail choice,
 * the same narrowing Import uses: the list on the left, and on the right who the
 * selected artist is and what they released — the albums are what someone recognises.
 * One Add, for the selected artist, rather than an Add on every row.
 */
export function AddArtistModal({
  existing,
  onClose,
  onAdded,
}: {
  /** MusicBrainz IDs already in the collection: marked, and opened rather than added. */
  existing?: Set<string>;
  onClose: () => void;
  onAdded: () => void;
}) {
  const toast = useToast();
  const [query, setQuery] = useState("");
  const [results, setResults] = useState<ArtistSearchResult[] | null>(null);
  const [searching, setSearching] = useState(false);
  const [selected, setSelected] = useState<ArtistSearchResult | null>(null);
  const [details, setDetails] = useState<Record<string, Detail>>({});
  const [busy, setBusy] = useState(false);
  const settled = useDebounced(query, 350);
  const seq = useRef(0);

  // A server-backed box debounces the fetch, not the input (style guide: Table toolbar).
  const search = async (q: string) => {
    const trimmed = q.trim();
    if (trimmed.length < 2) return;
    const mine = ++seq.current;
    setSearching(true);
    try {
      const hits = await api.get<ArtistSearchResult[]>(`/search/artists?q=${encodeURIComponent(trimmed)}`);
      if (mine !== seq.current) return;
      setResults(hits ?? []);
      setSelected((hits ?? [])[0] ?? null);
    } catch (e) {
      if (mine === seq.current) toast("err", errMsg(e));
    } finally {
      if (mine === seq.current) setSearching(false);
    }
  };
  useEffect(() => {
    search(settled);
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [settled]);

  // Who the selected artist is and what they released, fetched once per artist: moving
  // back to one already looked at costs nothing.
  useEffect(() => {
    if (!selected || details[selected.id]) return;
    const id = selected.id;
    setDetails((d) => ({ ...d, [id]: {} }));
    Promise.all([
      api.get<ArtistInfo>(`/search/artists/${id}/info`).catch(() => undefined),
      api.get<ReleaseGroupHit[]>(`/search/artists/${id}/release-groups`),
    ])
      .then(([info, groups]) => setDetails((d) => ({ ...d, [id]: { info, groups: groups ?? [] } })))
      .catch((e) => setDetails((d) => ({ ...d, [id]: { error: errMsg(e) } })));
  }, [selected, details]);

  const add = async () => {
    if (!selected) return;
    setBusy(true);
    try {
      await api.post("/artists", { mb_id: selected.id, name: selected.name });
      if (follow) {
        // The follow is its own request, so an artist is added even when the
        // discography sync behind the follow fails; the toast says which happened.
        try {
          await api.post(`/artists/${selected.id}/follow`, { monitored: true, ...shape });
          toast("ok", `Added and following ${selected.name}`);
        } catch (e) {
          toast("err", `Added ${selected.name}, but following failed: ${errMsg(e)}`);
        }
      } else {
        toast("ok", `Added ${selected.name}`);
      }
      onAdded();
    } catch (e) {
      toast("err", errMsg(e));
      setBusy(false);
    }
  };

  // Adding someone you own nothing of is nearly always wanting their records, so the
  // follow is on by default; the shape is the same one an artist's page sets.
  const [follow, setFollow] = useState(true);
  const [shape, setShape] = useState<FollowShape>(DEFAULT_FOLLOW);

  const detail = selected ? details[selected.id] : undefined;
  const inCollection = !!selected && !!existing?.has(selected.id);

  return (
    <Modal title="Add artist" onClose={onClose} xl>
      <div className="stack">
        <form
          className="row"
          style={{ gap: 8 }}
          onSubmit={(e: FormEvent) => {
            e.preventDefault();
            search(query);
          }}
        >
          <label className="sr-only" htmlFor="add-artist-q">Artist name</label>
          <input
            id="add-artist-q"
            className="input"
            style={{ flex: 1 }}
            value={query}
            onChange={(e) => setQuery(e.target.value)}
            placeholder="Artist name…"
            autoFocus
          />
          {searching && <span className="dim" style={{ fontSize: 11 }}>Searching…</span>}
        </form>

        {results && results.length === 0 && <div className="dim" style={{ fontSize: 12 }}>No artists matched “{settled.trim()}”.</div>}
        {!results && <div className="dim" style={{ fontSize: 12 }}>Type a name — results appear as you type.</div>}

        {results && results.length > 0 && (
          <div className="pick-grid two">
            <div className="pick-col">
              <span className="flabel">{results.length} artist{results.length === 1 ? "" : "s"}</span>
              <ul className="pick-list tall">
                {results.map((r) => (
                  <li key={r.id}>
                    <button type="button" className="pick-row" aria-pressed={selected?.id === r.id} onClick={() => setSelected(r)}>
                      <span className="row" style={{ justifyContent: "space-between", gap: 8 }}>
                        <span>{r.name}</span>
                        {existing?.has(r.id) && <Pill kind="chg">In collection</Pill>}
                      </span>
                      <span className="pick-sub">{[r.disambiguation, r.type, r.country].filter(Boolean).join(" · ") || "no description"}</span>
                    </button>
                  </li>
                ))}
              </ul>
            </div>

            <div className="pick-col">
              {selected && (
                <div className="artist-preview">
                  <div className="row" style={{ justifyContent: "space-between", alignItems: "baseline", gap: 8 }}>
                    <span style={{ fontSize: 16, fontWeight: 600 }}>{selected.name}</span>
                    <MBLink entity="artist" mbid={selected.id} />
                  </div>
                  {selected.disambiguation && <span className="muted">{selected.disambiguation}</span>}
                  <span className="dim" style={{ fontSize: 12 }}>{factsLine(selected, detail?.info) || "No details on MusicBrainz"}</span>
                  {(detail?.info?.genres ?? []).length > 0 && (
                    <div className="row" style={{ gap: 6, flexWrap: "wrap" }}>
                      {(detail?.info?.genres ?? []).map((g) => <span key={g} className="fact" style={{ fontSize: 11 }}>{g}</span>)}
                    </div>
                  )}
                  <ArtistReleases detail={detail} />
                </div>
              )}
            </div>
          </div>
        )}

        {selected && !inCollection && (
          <div className="stack" style={{ gap: 8 }}>
            <label className="row" style={{ gap: 8, cursor: "pointer" }}>
              <input type="checkbox" checked={follow} onChange={(e) => setFollow(e.target.checked)} />
              <span>Follow {selected.name}</span>
              <span className="dim" style={{ fontSize: 11 }}>
                {follow ? `— wants ${describeFollow(shape)}, and what they release next` : "— only their page is added; nothing is wanted"}
              </span>
            </label>
            {follow && <FollowFields value={shape} onChange={(patch) => setShape((cur) => ({ ...cur, ...patch }))} disabled={busy} idPrefix="add-artist" />}
          </div>
        )}
        <div className="modal-actions">
          <button type="button" className="btn btn-ghost btn-sm" style={{ marginRight: "auto" }} onClick={onClose}>Cancel</button>
          {inCollection && selected ? (
            <Link className="btn btn-secondary btn-sm" to={`/collection/${selected.id}`} onClick={onClose}>
              Open {selected.name}
            </Link>
          ) : (
            <button type="button" className="btn btn-primary btn-sm" disabled={!selected || busy} onClick={add}>
              {busy ? "Adding…" : selected ? `${follow ? "Add and follow" : "Add"} ${selected.name}` : "Add artist"}
            </button>
          )}
        </div>
      </div>
    </Modal>
  );
}

/**
 * What the artist released: a count per kind and their studio albums by year — the
 * thing someone actually recognises an artist by.
 */
function ArtistReleases({ detail }: { detail?: Detail }) {
  if (!detail || (!detail.groups && !detail.error)) return <span className="dim" style={{ fontSize: 12 }}>Loading releases…</span>;
  if (detail.error) return <span className="help-err">{detail.error}</span>;
  const groups = detail.groups ?? [];
  if (groups.length === 0) return <span className="dim" style={{ fontSize: 12 }}>Nothing released on MusicBrainz.</span>;

  const studio = groups
    .filter((g) => g["primary-type"] === "Album" && (g["secondary-types"] ?? []).length === 0)
    .sort((a, b) => (a["first-release-date"] || "9999").localeCompare(b["first-release-date"] || "9999"));
  const years = groups.map((g) => g["first-release-date"]?.slice(0, 4)).filter(Boolean).sort();
  const counts = TYPE_LABELS.map(([type, one, many]) => {
    const n = groups.filter((g) => g["primary-type"] === type).length;
    return n > 0 ? `${n} ${n === 1 ? one : many}` : "";
  }).filter(Boolean);
  const shown = studio.slice(0, 10);

  return (
    <div className="stack" style={{ gap: 6 }}>
      <span className="dim" style={{ fontSize: 12 }}>
        {counts.join(" · ") || `${groups.length} releases`}
        {years.length > 0 && <> · <span className="mono">{years[0]}{years.length > 1 && years[years.length - 1] !== years[0] ? `–${years[years.length - 1]}` : ""}</span></>}
      </span>
      {shown.length > 0 && (
        <>
          <span className="eyebrow">Studio albums</span>
          <ul className="pick-list">
            {shown.map((g) => (
              <li key={g.id} className="pick-static">
                <span>{g.title}</span>
                <span className="mono dim">{g["first-release-date"]?.slice(0, 4) || "—"}</span>
              </li>
            ))}
          </ul>
          {studio.length > shown.length && <span className="dim" style={{ fontSize: 11 }}>and {studio.length - shown.length} more</span>}
        </>
      )}
    </div>
  );
}
