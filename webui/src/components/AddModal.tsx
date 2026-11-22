import { FormEvent, ReactNode, useEffect, useMemo, useRef, useState } from "react";
import { Link } from "react-router-dom";
import { api, errMsg } from "../api";
import { useDebounced } from "../hooks";
import {
  RecordingSearchHit,
  RecordingSearchRelease,
  ReleaseGroupSearchHit,
  ReleaseGroupSearchResponse,
  ReleaseSearchResult,
  ReleaseTracks,
  WantPick,
} from "../types";
import { Modal, Pill } from "./ui";
import { MBLink } from "./MBLink";
import { mediaSummary } from "./mediaSummary";
import { useToast } from "../toast";
import { DEFAULT_FOLLOW, FollowFields, FollowShape, describeFollow } from "./FollowFields";
import { AddArtistPanel } from "./AddArtistPanel";

type Tab = "artist" | "album" | "track";

/**
 * Add: put something into the collection that you own nothing of. An artist, one of
 * their albums, particular editions of it, or a few songs. Wanting an album or a
 * song adds its artist on the way, without following them. You asked for one album,
 * not their discography.
 *
 * One modal with three tabs, not three buttons. The question is always "find it on
 * MusicBrainz and say how much of it you want", and a pasted MusicBrainz link
 * switches to the tab that matches it. The panels stay mounted while hidden, so
 * switching tabs never throws away a search (style guide: Tabs).
 */
export function AddModal({
  existing,
  onClose,
  onChanged,
}: {
  /** Artist MBIDs already in the collection. */
  existing?: Set<string>;
  onClose: () => void;
  /** Something was added: reload the list behind the modal. */
  onChanged: () => void;
}) {
  const [tab, setTab] = useState<Tab>("artist");
  // A link pasted into the wrong tab is carried to the right one, query and all.
  const [handoff, setHandoff] = useState<{ tab: Tab; q: string } | null>(null);
  const routeLink = (q: string, here: Tab): boolean => {
    const target = linkTab(q);
    if (!target || target === here) return false;
    setHandoff({ tab: target, q });
    setTab(target);
    return true;
  };

  const tabs: [Tab, string][] = [["artist", "Artist"], ["album", "Album"], ["track", "Track"]];
  return (
    <Modal title="Add" onClose={onClose} xl>
      <div className="stack">
        <div className="tabs" role="tablist" aria-label="What to add">
          {tabs.map(([key, label]) => (
            <button key={key} type="button" role="tab" className="tab" aria-selected={tab === key} onClick={() => setTab(key)}>
              {label}
            </button>
          ))}
        </div>
        <div role="tabpanel" hidden={tab !== "artist"}>
          <AddArtistPanel existing={existing} onClose={onClose} onAdded={() => { onChanged(); onClose(); }} />
        </div>
        <div role="tabpanel" hidden={tab !== "album"}>
          <AlbumPanel
            active={tab === "album"}
            existing={existing}
            handoff={handoff?.tab === "album" ? handoff.q : undefined}
            routeLink={(q) => routeLink(q, "album")}
            onClose={onClose}
            onChanged={onChanged}
          />
        </div>
        <div role="tabpanel" hidden={tab !== "track"}>
          <TrackPanel
            active={tab === "track"}
            existing={existing}
            handoff={handoff?.tab === "track" ? handoff.q : undefined}
            routeLink={(q) => routeLink(q, "track")}
            onClose={onClose}
            onChanged={onChanged}
          />
        </div>
      </div>
    </Modal>
  );
}

/** Which tab a pasted MusicBrainz link belongs to, or null for plain text. */
function linkTab(q: string): Tab | null {
  const s = q.trim().toLowerCase();
  if (!/^https?:\/\/(\w+\.)?musicbrainz\.org\//.test(s)) return null;
  if (s.includes("/recording/")) return "track";
  if (s.includes("/release-group/") || s.includes("/release/")) return "album";
  if (s.includes("/artist/")) return "artist";
  return null;
}

function credit(ac: { name: string; joinphrase: string }[] | null | undefined): string {
  return (ac ?? []).map((c) => c.name + (c.joinphrase ?? "")).join("").trim();
}

function duration(ms: number): string {
  if (!ms) return "";
  const s = Math.round(ms / 1000);
  return `${Math.floor(s / 60)}:${String(s % 60).padStart(2, "0")}`;
}

function typeLine(primary: string | undefined, secondary: string[] | null | undefined): string {
  return [primary, ...(secondary ?? [])].filter(Boolean).join(" + ");
}

/**
 * The search box and its debounced fetch, shared by the two panels. A pasted link
 * that belongs to another tab is handed over there instead of searched here.
 */
function useSearch<T>(
  path: string,
  handoff: string | undefined,
  routeLink: (q: string) => boolean,
  onResult: (data: T) => void
) {
  const toast = useToast();
  const [query, setQuery] = useState("");
  const [searching, setSearching] = useState(false);
  const [searched, setSearched] = useState("");
  const settled = useDebounced(query, 350);
  const seq = useRef(0);

  const search = async (q: string) => {
    const trimmed = q.trim();
    if (trimmed.length < 2 || routeLink(trimmed)) return;
    const mine = ++seq.current;
    setSearching(true);
    try {
      const data = await api.get<T>(`${path}?q=${encodeURIComponent(trimmed)}`);
      if (mine !== seq.current) return;
      setSearched(trimmed);
      onResult(data);
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
  useEffect(() => {
    if (handoff) setQuery(handoff);
  }, [handoff]);

  return { query, setQuery, searching, searched, search };
}

function SearchBox({
  id,
  label,
  placeholder,
  active,
  s,
}: {
  id: string;
  label: string;
  placeholder: string;
  active: boolean;
  s: { query: string; setQuery: (q: string) => void; searching: boolean; search: (q: string) => void };
}) {
  const ref = useRef<HTMLInputElement>(null);
  useEffect(() => {
    if (active) ref.current?.focus();
  }, [active]);
  return (
    <form
      className="row"
      style={{ gap: 8 }}
      onSubmit={(e: FormEvent) => {
        e.preventDefault();
        s.search(s.query);
      }}
    >
      <label className="sr-only" htmlFor={id}>{label}</label>
      <input ref={ref} id={id} className="input" style={{ flex: 1 }} value={s.query} onChange={(e) => s.setQuery(e.target.value)} placeholder={placeholder} />
      {s.searching && <span className="dim" style={{ fontSize: 11 }}>Searching…</span>}
    </form>
  );
}

/**
 * Follow checkbox, Cancel and the one primary action, plus what the last add did.
 * Shared by the Album and Track panels. The follow is off by default, unlike on
 * the Artist tab: wanting one album is not asking for the artist's discography.
 */
function WantFooter({
  artistName,
  artistInCollection,
  follow,
  setFollow,
  shape,
  setShape,
  busy,
  idPrefix,
  done,
  onClose,
  action,
}: {
  artistName?: string;
  artistInCollection: boolean;
  follow: boolean;
  setFollow: (on: boolean) => void;
  shape: FollowShape;
  setShape: (fn: (cur: FollowShape) => FollowShape) => void;
  busy: boolean;
  idPrefix: string;
  done: ReactNode;
  onClose: () => void;
  action: ReactNode;
}) {
  return (
    <>
      {done}
      {artistName && !artistInCollection && (
        <div className="stack" style={{ gap: 8 }}>
          <label className="row" style={{ gap: 8, cursor: "pointer" }}>
            <input type="checkbox" checked={follow} onChange={(e) => setFollow(e.target.checked)} />
            <span>Also follow {artistName}</span>
            <span className="dim" style={{ fontSize: 11 }}>
              {follow ? `Wants ${describeFollow(shape)}, and what they release next` : "They are added without following; only what you pick is wanted"}
            </span>
          </label>
          {follow && <FollowFields value={shape} onChange={(patch) => setShape((cur) => ({ ...cur, ...patch }))} disabled={busy} idPrefix={idPrefix} />}
        </div>
      )}
      <div className="modal-actions">
        <button type="button" className="btn btn-ghost btn-sm" style={{ marginRight: "auto" }} onClick={onClose}>Cancel</button>
        {action}
      </div>
    </>
  );
}

interface WantBody {
  artist: { mb_id: string; name: string };
  release_group: { mb_id: string; title: string; primary_type: string; secondary_types: string; first_release_date: string };
  picks: WantPick[];
}

interface Added {
  artistMbid: string;
  rgMbid: string;
  label: string;
}

/**
 * Posts the wants, then the follow when one was asked for. The follow is its own
 * request, as after Add artist, so a want is kept even when the discography sync
 * behind the follow fails. The toast says which of the two happened.
 */
async function postWants(bodies: WantBody[], follow: FollowShape | null): Promise<{ addedArtist: boolean; followErr?: string }> {
  let addedArtist = false;
  for (const body of bodies) {
    const res = await api.post<{ artist_added: boolean }>("/wants", body);
    addedArtist = addedArtist || !!res?.artist_added;
  }
  if (follow && bodies.length > 0) {
    try {
      await api.post(`/artists/${bodies[0].artist.mb_id}/follow`, { monitored: true, ...follow });
    } catch (e) {
      return { addedArtist, followErr: errMsg(e) };
    }
  }
  return { addedArtist };
}

function DoneBar({ added, onClose }: { added: Added | null; onClose: () => void }) {
  if (!added) return null;
  return (
    <div className="selected-bar row" style={{ justifyContent: "space-between", gap: 8 }}>
      <span>Wanted {added.label}</span>
      <Link className="btn btn-secondary btn-sm" to={`/collection/${added.artistMbid}/${added.rgMbid}`} onClick={onClose}>
        Open album
      </Link>
    </div>
  );
}

// ---------------------------------------------------------------------------
// Album: a release-group, any or particular editions, some or all tracks.
// ---------------------------------------------------------------------------

/**
 * What is about to be wanted, as edition → recordings. The key "" is "any edition"
 * and an empty list is "every track". The same rules as the album page: "any" and
 * particular editions exclude each other, and emptying a track list drops the pick
 * rather than widening it to the whole edition.
 */
type Draft = Record<string, string[]>;

function AlbumPanel({
  active,
  existing,
  handoff,
  routeLink,
  onClose,
  onChanged,
}: {
  active: boolean;
  existing?: Set<string>;
  handoff?: string;
  routeLink: (q: string) => boolean;
  onClose: () => void;
  onChanged: () => void;
}) {
  const toast = useToast();
  const [hits, setHits] = useState<ReleaseGroupSearchHit[] | null>(null);
  const [selected, setSelected] = useState<ReleaseGroupSearchHit | null>(null);
  const [preselect, setPreselect] = useState<string>("");
  const s = useSearch<ReleaseGroupSearchResponse>("/search/release-groups", handoff, routeLink, (data) => {
    const list = data?.release_groups ?? [];
    setHits(list);
    setSelected(list[0] ?? null);
    setPreselect(data?.release_mb_id ?? "");
  });

  const [editions, setEditions] = useState<Record<string, ReleaseSearchResult[] | { error: string }>>({});
  const [draft, setDraft] = useState<Draft>({ "": [] });
  const [viewing, setViewing] = useState<string | null>(null);
  const [follow, setFollow] = useState(false);
  const [shape, setShape] = useState<FollowShape>(DEFAULT_FOLLOW);
  const [busy, setBusy] = useState(false);
  const [added, setAdded] = useState<Added | null>(null);

  // Editions of the selected album, fetched once per album (the browse is cached on
  // the server too). A pasted edition link opens with that edition picked.
  useEffect(() => {
    if (!selected) return;
    const id = selected.id;
    const pre = preselect;
    setDraft(pre ? { [pre]: [] } : { "": [] });
    setViewing(pre || null);
    if (editions[id]) return;
    api
      .get<ReleaseSearchResult[]>(`/release-groups/${id}/releases`)
      .then((list) => setEditions((m) => ({ ...m, [id]: list ?? [] })))
      .catch((e) => setEditions((m) => ({ ...m, [id]: { error: errMsg(e) } })));
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [selected]);

  const loaded = selected ? editions[selected.id] : undefined;
  const list = Array.isArray(loaded) ? loaded : [];
  const sorted = useMemo(() => [...list].sort((a, b) => (a.date || "9999").localeCompare(b.date || "9999")), [list]);
  // With nothing chosen to look at, the earliest edition has the most canonical tracklist.
  const shownEdition = viewing ?? sorted[0]?.id ?? null;

  const artistCredit = selected?.["artist-credit"]?.[0];
  const artistMbid = artistCredit?.artist?.id ?? "";
  const artistName = artistCredit?.artist?.name ?? artistCredit?.name ?? "";
  const inCollection = !!artistMbid && !!existing?.has(artistMbid);
  const picks = Object.entries(draft);

  const want = async () => {
    if (!selected || !artistMbid || picks.length === 0) return;
    setBusy(true);
    const label = summary(selected.title, draft, sorted);
    try {
      const { addedArtist, followErr } = await postWants(
        [{
          artist: { mb_id: artistMbid, name: artistName },
          release_group: {
            mb_id: selected.id, title: selected.title, primary_type: selected["primary-type"] ?? "",
            secondary_types: (selected["secondary-types"] ?? []).join(", "), first_release_date: selected["first-release-date"] ?? "",
          },
          picks: picks.map(([release, recs]) => ({ release_mb_id: release, recording_mb_ids: recs })),
        }],
        follow && !inCollection ? shape : null
      );
      if (followErr) toast("err", `Wanted ${label}, but following ${artistName} failed: ${followErr}`);
      else toast("ok", addedArtist ? `Added ${artistName} and wanted ${label}` : `Wanted ${label}`);
      setAdded({ artistMbid, rgMbid: selected.id, label });
      onChanged();
    } catch (e) {
      toast("err", errMsg(e));
    } finally {
      setBusy(false);
    }
  };

  return (
    <div className="stack">
      <SearchBox id="add-album-q" label="Album title" placeholder="Album title, or a MusicBrainz link…" active={active} s={s} />
      {hits && hits.length === 0 && <div className="dim" style={{ fontSize: 12 }}>No albums matched “{s.searched}”.</div>}
      {!hits && (
        <div className="dim" style={{ fontSize: 12 }}>
          Type a title. Add the artist's name to narrow it (<span className="mono">ok computer radiohead</span>), or paste a MusicBrainz album or release link.
        </div>
      )}

      {hits && hits.length > 0 && (
        <div className="pick-grid two">
          <div className="pick-col">
            <span className="flabel">{hits.length} album{hits.length === 1 ? "" : "s"}</span>
            <ul className="pick-list tall">
              {hits.map((h) => (
                <li key={h.id}>
                  <button type="button" className="pick-row" aria-pressed={selected?.id === h.id} onClick={() => { setPreselect(""); setSelected(h); setAdded(null); }}>
                    <span className="row" style={{ justifyContent: "space-between", gap: 8 }}>
                      <span>{h.title}{h.disambiguation && <span className="dim"> ({h.disambiguation})</span>}</span>
                      {existing?.has(h["artist-credit"]?.[0]?.artist?.id ?? "") && <Pill kind="chg">Artist in collection</Pill>}
                    </span>
                    <span className="pick-sub">
                      {[credit(h["artist-credit"]), typeLine(h["primary-type"], h["secondary-types"]), h["first-release-date"]?.slice(0, 4)].filter(Boolean).join(" · ")}
                    </span>
                  </button>
                </li>
              ))}
            </ul>
          </div>

          <div className="pick-col">
            {selected && (
              <>
                <div className="row" style={{ justifyContent: "space-between", alignItems: "baseline", gap: 8 }}>
                  <span style={{ fontSize: 16, fontWeight: 600 }}>{selected.title}</span>
                  <MBLink entity="release-group" mbid={selected.id} />
                </div>
                <span className="dim" style={{ fontSize: 12 }}>
                  {[credit(selected["artist-credit"]), typeLine(selected["primary-type"], selected["secondary-types"]), selected["first-release-date"]?.slice(0, 4)].filter(Boolean).join(" · ")}
                </span>
                {!loaded && <span className="dim" style={{ fontSize: 12 }}>Loading editions…</span>}
                {loaded && !Array.isArray(loaded) && <span className="help-err">{loaded.error}</span>}
                {Array.isArray(loaded) && (
                  <div className="stack" style={{ gap: 12 }}>
                    <EditionPicker editions={sorted} draft={draft} setDraft={setDraft} viewing={shownEdition} setViewing={setViewing} busy={busy} />
                    <TrackPicker releaseMbid={shownEdition} draft={draft} setDraft={setDraft} busy={busy} />
                  </div>
                )}
              </>
            )}
          </div>
        </div>
      )}

      <WantFooter
        artistName={selected ? artistName : undefined}
        artistInCollection={inCollection}
        follow={follow}
        setFollow={setFollow}
        shape={shape}
        setShape={setShape}
        busy={busy}
        idPrefix="add-album"
        done={<DoneBar added={added} onClose={onClose} />}
        onClose={onClose}
        action={
          <button type="button" className="btn btn-primary btn-sm" disabled={!selected || !artistMbid || picks.length === 0 || busy} onClick={want}>
            {busy ? "Adding…" : selected ? (picks.length === 0 ? "Pick what to want" : `Want ${summary(selected.title, draft, sorted)}`) : "Want album"}
          </button>
        }
      />
    </div>
  );
}

/** "OK Computer, any edition" · "1 track of OK Computer (1997 CD)" · "OK Computer, 2 editions". */
function summary(title: string, draft: Draft, editions: ReleaseSearchResult[]): string {
  const picks = Object.entries(draft);
  if (picks.length === 0) return title;
  const tracks = picks.reduce((n, [, recs]) => n + recs.length, 0);
  const songs = (n: number) => `${n} track${n === 1 ? "" : "s"}`;
  if (picks.length === 1) {
    const [release, recs] = picks[0];
    if (release === "") return recs.length ? `${songs(recs.length)} of ${title}, any edition` : `${title}, any edition`;
    const e = editions.find((x) => x.id === release);
    const facts = e ? [(e.date || "").slice(0, 4), mediaSummary(e.media)].filter(Boolean).join(" ") : "";
    const where = facts ? `${title} (${facts})` : title;
    return recs.length ? `${songs(recs.length)} of ${where}` : where;
  }
  return tracks ? `${songs(tracks)} across ${picks.length} editions of ${title}` : `${title}, ${picks.length} editions`;
}

/**
 * The editions, each with a checkbox (want it) and a row body (show its tracks).
 * Two jobs, two hit areas, as on the album page.
 */
function EditionPicker({
  editions,
  draft,
  setDraft,
  viewing,
  setViewing,
  busy,
}: {
  editions: ReleaseSearchResult[];
  draft: Draft;
  setDraft: (d: Draft) => void;
  viewing: string | null;
  setViewing: (id: string) => void;
  busy: boolean;
}) {
  const [filter, setFilter] = useState("");
  const anyOn = "" in draft;
  const f = filter.trim().toLowerCase();
  const shown = f
    ? editions.filter((e) => [e.title, e.disambiguation, e.country, e.status, (e.date || "").slice(0, 4), mediaSummary(e.media)].join(" ").toLowerCase().includes(f))
    : editions;

  const toggleEdition = (id: string) => {
    const next: Draft = { ...draft };
    if (id in next) delete next[id];
    else {
      delete next[""];
      next[id] = [];
    }
    setDraft(next);
  };

  return (
    <div className="card" style={{ padding: 0, overflow: "hidden" }}>
      <div style={{ padding: "10px 12px" }}>
        <div className="row" style={{ justifyContent: "space-between", marginBottom: editions.length > 6 ? 8 : 0 }}>
          <span className="eyebrow">Editions · {editions.length}</span>
        </div>
        {editions.length > 6 && (
          <input className="input" type="search" style={{ height: 28, fontSize: 12 }} placeholder="Filter by title, year, country, format" aria-label="Filter editions" value={filter} onChange={(e) => setFilter(e.target.value)} />
        )}
      </div>
      <label className="edition-row any-row" title="Any edition counts: whichever pressing turns up. This is what most people want.">
        <input type="checkbox" checked={anyOn} disabled={busy} onChange={() => setDraft(anyOn ? {} : { "": [] })} />
        <div style={{ minWidth: 0 }}>
          <div style={{ color: "var(--text)", fontSize: 12 }}>Any edition</div>
          <div className="dim" style={{ fontSize: 11 }}>Whichever pressing turns up counts</div>
        </div>
      </label>
      <div style={{ maxHeight: 260, overflowY: "auto" }}>
        {shown.length === 0 && <div className="dim" style={{ fontSize: 11, padding: 12 }}>{editions.length ? "No edition matches this filter." : "MusicBrainz lists no editions."}</div>}
        {shown.map((r) => {
          const picked = draft[r.id];
          return (
            <div key={r.id} className={`edition-row${viewing === r.id ? " active" : ""}`}>
              <input
                type="checkbox"
                checked={!!picked}
                disabled={busy}
                aria-label={`Want the ${r.title} edition${r.date ? ` from ${r.date.slice(0, 4)}` : ""}`}
                title={picked ? "Wanted. Untick to drop this edition." : "Want this edition. Only ticked editions will count."}
                onChange={() => toggleEdition(r.id)}
              />
              <button type="button" className="edition-pick" onClick={() => setViewing(r.id)}>
                <span style={{ color: "var(--text)", fontSize: 12 }}>
                  {r.title}
                  {r.disambiguation && <span className="dim"> ({r.disambiguation})</span>}
                </span>
                <div className="dim mono" style={{ fontSize: 11 }}>
                  {[(r.date || "").slice(0, 4), r.country, r.status, mediaSummary(r.media)].filter(Boolean).join(" · ")}
                </div>
                {picked && (
                  <div className="mono" style={{ fontSize: 11, marginTop: 2, color: "var(--accent-text)" }}>
                    {picked.length ? `${picked.length} track${picked.length === 1 ? "" : "s"} wanted` : "whole edition wanted"}
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
 * The tracklist of the edition being looked at. Which pick its ticks edit follows
 * the album page: this edition if it is ticked, else "any edition" if that is, and
 * with neither, ticking a track wants it from any edition.
 */
function TrackPicker({
  releaseMbid,
  draft,
  setDraft,
  busy,
}: {
  releaseMbid: string | null;
  draft: Draft;
  setDraft: (d: Draft) => void;
  busy: boolean;
}) {
  const [tracks, setTracks] = useState<Record<string, ReleaseTracks | { error: string }>>({});
  useEffect(() => {
    if (!releaseMbid || tracks[releaseMbid]) return;
    const id = releaseMbid;
    api
      .get<ReleaseTracks>(`/releases/${id}/tracks`)
      .then((t) => setTracks((m) => ({ ...m, [id]: t })))
      .catch((e) => setTracks((m) => ({ ...m, [id]: { error: errMsg(e) } })));
  }, [releaseMbid, tracks]);

  if (!releaseMbid) return null;
  const loaded = tracks[releaseMbid];
  const list = loaded && "tracks" in loaded ? loaded.tracks ?? [] : [];

  const key = releaseMbid in draft ? releaseMbid : "" in draft ? "" : null;
  const selected = key !== null ? draft[key] : [];
  const wholeThing = key !== null && selected.length === 0;
  const isOn = (rec: string) => wholeThing || selected.includes(rec);
  const write = (recs: string[]) => setDraft({ ...draft, [key ?? ""]: recs });
  const drop = () => {
    const next = { ...draft };
    if (key !== null) delete next[key];
    setDraft(next);
  };

  const toggle = (rec: string) => {
    if (wholeThing) {
      const rest = list.filter((t) => t.recording_id !== rec).map((t) => t.recording_id);
      if (rest.length === 0) drop();
      else write(rest);
      return;
    }
    const next = selected.includes(rec) ? selected.filter((x) => x !== rec) : [...selected, rec];
    if (next.length === 0) drop();
    else write(next.length === list.length ? [] : next);
  };
  const partial = key !== null && !wholeThing;
  const absent = selected.filter((id) => !list.some((t) => t.recording_id === id));
  const multiDisc = new Set(list.map((t) => t.medium)).size > 1;

  return (
    <div className="card" style={{ padding: 0, overflow: "hidden" }}>
      <div style={{ padding: "10px 12px 8px" }}>
        <div className="eyebrow">Tracks</div>
        <div className="dim" style={{ fontSize: 11, marginTop: 2 }}>
          {loaded && "release" in loaded ? loaded.release.title : ""}
          {key === releaseMbid ? " · ticks apply to this edition" : key === "" ? " · ticks apply to any edition" : " · ticking a track wants it from any edition"}
        </div>
        {absent.length > 0 && list.length > 0 && (
          <div style={{ fontSize: 11, marginTop: 4, color: "var(--warning-text)" }}>
            {absent.length} picked {absent.length === 1 ? "song is" : "songs are"} not on this edition.
          </div>
        )}
      </div>
      <label className="track-row all-row">
        <input
          type="checkbox"
          checked={wholeThing}
          ref={(el) => { if (el) el.indeterminate = partial; }}
          disabled={busy || list.length === 0}
          onChange={() => (wholeThing ? drop() : write([]))}
        />
        <span style={{ color: "var(--text)", fontSize: 12 }}>All tracks</span>
        {partial && (
          <span className="mono" style={{ fontSize: 11, color: "var(--accent-text)", marginLeft: "auto" }}>
            {selected.length} of {list.length}
          </span>
        )}
      </label>
      <div style={{ maxHeight: 260, overflowY: "auto", borderTop: "1px solid var(--border)" }}>
        {!loaded && <div className="dim" style={{ fontSize: 11, padding: 12 }}>Loading tracklist…</div>}
        {loaded && "error" in loaded && <div className="help-err" style={{ fontSize: 11, padding: 12 }}>{loaded.error}</div>}
        {list.map((t) => (
          <label key={t.track_id} className="track-row">
            <input type="checkbox" checked={isOn(t.recording_id)} disabled={busy} onChange={() => toggle(t.recording_id)} />
            <span className="dim mono" style={{ fontSize: 11, minWidth: 28, textAlign: "right" }}>{multiDisc ? `${t.medium}-${t.number}` : t.number}</span>
            <span style={{ color: "var(--text)", fontSize: 12 }}>{t.title}</span>
            <span className="dim mono" style={{ fontSize: 11, marginLeft: "auto" }}>{duration(t.length)}</span>
          </label>
        ))}
      </div>
    </div>
  );
}

// ---------------------------------------------------------------------------
// Track: one song, from any release of an album or from particular releases.
// ---------------------------------------------------------------------------

interface Group {
  id: string;
  title: string;
  primary: string;
  secondary: string[];
  date: string;
  releases: RecordingSearchRelease[];
}

/** The song's releases, grouped by album, oldest album first. */
function groupReleases(rec: RecordingSearchHit): Group[] {
  const byId = new Map<string, Group>();
  for (const r of rec.releases ?? []) {
    const rg = r["release-group"];
    if (!rg?.id) continue;
    let g = byId.get(rg.id);
    if (!g) {
      g = { id: rg.id, title: rg.title || r.title, primary: rg["primary-type"] ?? "", secondary: rg["secondary-types"] ?? [], date: "", releases: [] };
      byId.set(rg.id, g);
    }
    g.releases.push(r);
    if (r.date && (!g.date || r.date < g.date)) g.date = r.date;
  }
  return [...byId.values()].sort((a, b) => (a.date || "9999").localeCompare(b.date || "9999"));
}

/** Where a song most likely belongs: its earliest studio album, else its earliest album of any kind. */
function homeGroup(groups: Group[]): Group | undefined {
  return (
    groups.find((g) => g.primary === "Album" && g.secondary.length === 0 && g.releases.some((r) => r.status === "Official")) ??
    groups.find((g) => g.secondary.length === 0) ??
    groups[0]
  );
}

function TrackPanel({
  active,
  existing,
  handoff,
  routeLink,
  onClose,
  onChanged,
}: {
  active: boolean;
  existing?: Set<string>;
  handoff?: string;
  routeLink: (q: string) => boolean;
  onClose: () => void;
  onChanged: () => void;
}) {
  const toast = useToast();
  const [hits, setHits] = useState<RecordingSearchHit[] | null>(null);
  const [selected, setSelected] = useState<RecordingSearchHit | null>(null);
  const s = useSearch<{ recordings: RecordingSearchHit[] }>("/search/recordings", handoff, routeLink, (data) => {
    const list = data?.recordings ?? [];
    setHits(list);
    setSelected(list[0] ?? null);
  });

  // Album → releases picked; "" in the list is "any release of this album".
  const [picks, setPicks] = useState<Record<string, string[]>>({});
  const [follow, setFollow] = useState(false);
  const [shape, setShape] = useState<FollowShape>(DEFAULT_FOLLOW);
  const [busy, setBusy] = useState(false);
  const [added, setAdded] = useState<Added | null>(null);

  const groups = useMemo(() => (selected ? groupReleases(selected) : []), [selected]);
  useEffect(() => {
    const home = homeGroup(groups);
    setPicks(home ? { [home.id]: [""] } : {});
  }, [groups]);

  const chosen = groups.filter((g) => (picks[g.id] ?? []).length > 0);
  // The artist a want is stored under is the album's, and each album is posted on its
  // own, so a song picked from a compilation lands under Various Artists.
  const artistOf = (g: Group) => g.releases[0]?.["artist-credit"]?.[0] ?? selected?.["artist-credit"]?.[0];
  const firstArtist = chosen[0] ? artistOf(chosen[0]) : selected?.["artist-credit"]?.[0];
  const firstArtistMbid = firstArtist?.artist?.id ?? "";
  const firstArtistName = firstArtist?.artist?.name ?? firstArtist?.name ?? "";
  const inCollection = !!firstArtistMbid && !!existing?.has(firstArtistMbid);

  const toggle = (groupId: string, release: string) => {
    const cur = picks[groupId] ?? [];
    let next: string[];
    if (cur.includes(release)) next = cur.filter((x) => x !== release);
    else if (release === "") next = [""];
    else next = [...cur.filter((x) => x !== ""), release];
    setPicks({ ...picks, [groupId]: next });
  };

  const label = () => {
    if (!selected) return "";
    if (chosen.length === 1) {
      const g = chosen[0];
      const rel = picks[g.id];
      if (rel.includes("")) return `${selected.title} from ${g.title}, any release`;
      return `${selected.title} from ${g.title} (${rel.length} release${rel.length === 1 ? "" : "s"})`;
    }
    return `${selected.title} from ${chosen.length} albums`;
  };

  const want = async () => {
    if (!selected || chosen.length === 0) return;
    setBusy(true);
    const what = label();
    try {
      const bodies: WantBody[] = [];
      for (const g of chosen) {
        const ac = artistOf(g);
        if (!ac?.artist?.id) continue;
        bodies.push({
          artist: { mb_id: ac.artist.id, name: ac.artist.name ?? ac.name },
          release_group: { mb_id: g.id, title: g.title, primary_type: g.primary, secondary_types: g.secondary.join(", "), first_release_date: g.date },
          picks: picks[g.id].map((release) => ({ release_mb_id: release, recording_mb_ids: [selected.id] })),
        });
      }
      if (bodies.length === 0) throw new Error("MusicBrainz names no artist for that album");
      const { addedArtist, followErr } = await postWants(bodies, follow && !inCollection ? shape : null);
      if (followErr) toast("err", `Wanted ${what}, but following ${firstArtistName} failed: ${followErr}`);
      else toast("ok", addedArtist ? `Added ${firstArtistName} and wanted ${what}` : `Wanted ${what}`);
      setAdded({ artistMbid: bodies[0].artist.mb_id, rgMbid: bodies[0].release_group.mb_id, label: what });
      onChanged();
    } catch (e) {
      toast("err", errMsg(e));
    } finally {
      setBusy(false);
    }
  };

  return (
    <div className="stack">
      <SearchBox id="add-track-q" label="Song title" placeholder="Song title, or a MusicBrainz recording link…" active={active} s={s} />
      {hits && hits.length === 0 && <div className="dim" style={{ fontSize: 12 }}>No songs matched “{s.searched}”.</div>}
      {!hits && (
        <div className="dim" style={{ fontSize: 12 }}>
          Type a title. Add the artist's name to narrow it (<span className="mono">karma police radiohead</span>), or paste a MusicBrainz recording link.
        </div>
      )}

      {hits && hits.length > 0 && (
        <div className="pick-grid two">
          <div className="pick-col">
            <span className="flabel">{hits.length} song{hits.length === 1 ? "" : "s"}</span>
            <ul className="pick-list tall">
              {hits.map((h) => (
                <li key={h.id}>
                  <button type="button" className="pick-row" aria-pressed={selected?.id === h.id} onClick={() => { setSelected(h); setAdded(null); }}>
                    <span className="row" style={{ justifyContent: "space-between", gap: 8 }}>
                      <span>{h.title}{h.disambiguation && <span className="dim"> ({h.disambiguation})</span>}</span>
                      {h.video && <Pill kind="off">Video</Pill>}
                    </span>
                    <span className="pick-sub">
                      {[credit(h["artist-credit"]), duration(h.length), `on ${(h.releases ?? []).length} release${(h.releases ?? []).length === 1 ? "" : "s"}`].filter(Boolean).join(" · ")}
                    </span>
                  </button>
                </li>
              ))}
            </ul>
          </div>

          <div className="pick-col">
            {selected && (
              <>
                <div className="row" style={{ justifyContent: "space-between", alignItems: "baseline", gap: 8 }}>
                  <span style={{ fontSize: 16, fontWeight: 600 }}>{selected.title}</span>
                  <MBLink entity="recording" mbid={selected.id} />
                </div>
                <span className="dim" style={{ fontSize: 12 }}>{[credit(selected["artist-credit"]), duration(selected.length)].filter(Boolean).join(" · ")}</span>
                {groups.length === 0 && <span className="dim" style={{ fontSize: 12 }}>MusicBrainz lists this song on no release, so there is nothing to want it from.</span>}
                {groups.length > 0 && (
                  <div className="card" style={{ padding: 0, overflow: "hidden" }}>
                    <div style={{ padding: "10px 12px 8px" }}>
                      <div className="eyebrow">Appears on · {groups.length} album{groups.length === 1 ? "" : "s"}</div>
                      <div className="dim" style={{ fontSize: 11, marginTop: 2 }}>Tick an album to want the song from any of its releases, or tick particular releases</div>
                    </div>
                    <div style={{ maxHeight: 420, overflowY: "auto" }}>
                      {groups.map((g) => {
                        const cur = picks[g.id] ?? [];
                        return (
                          <div key={g.id} style={{ borderTop: "1px solid var(--border)" }}>
                            <label className="edition-row any-row">
                              <input type="checkbox" checked={cur.includes("")} disabled={busy} onChange={() => toggle(g.id, "")} />
                              <div style={{ minWidth: 0, flex: 1 }}>
                                <div style={{ color: "var(--text)", fontSize: 12 }}>{g.title}</div>
                                <div className="dim" style={{ fontSize: 11 }}>
                                  {[typeLine(g.primary, g.secondary), g.date.slice(0, 4), credit(g.releases[0]?.["artist-credit"])].filter(Boolean).join(" · ")} · any release
                                </div>
                              </div>
                              <MBLink entity="release-group" mbid={g.id} />
                            </label>
                            {g.releases.map((r) => (
                              <label key={r.id} className="track-row" style={{ paddingLeft: 28 }}>
                                <input type="checkbox" checked={cur.includes(r.id)} disabled={busy} onChange={() => toggle(g.id, r.id)} />
                                <span style={{ fontSize: 12, color: "var(--text)" }}>
                                  {r.title}{r.disambiguation && <span className="dim"> ({r.disambiguation})</span>}
                                </span>
                                <span className="dim mono" style={{ fontSize: 11, marginLeft: "auto" }}>
                                  {[(r.date || "").slice(0, 4), r.country, (r.media ?? []).map((m) => m.format).filter(Boolean).join("+"), r["track-count"] ? `${r["track-count"]} tracks` : ""].filter(Boolean).join(" · ")}
                                </span>
                              </label>
                            ))}
                          </div>
                        );
                      })}
                    </div>
                  </div>
                )}
              </>
            )}
          </div>
        </div>
      )}

      <WantFooter
        artistName={selected && chosen.length > 0 ? firstArtistName : undefined}
        artistInCollection={inCollection}
        follow={follow}
        setFollow={setFollow}
        shape={shape}
        setShape={setShape}
        busy={busy}
        idPrefix="add-track"
        done={<DoneBar added={added} onClose={onClose} />}
        onClose={onClose}
        action={
          <button type="button" className="btn btn-primary btn-sm" disabled={!selected || chosen.length === 0 || busy} onClick={want}>
            {busy ? "Adding…" : selected ? (chosen.length === 0 ? "Pick where to want it from" : `Want ${label()}`) : "Want song"}
          </button>
        }
      />
    </div>
  );
}
