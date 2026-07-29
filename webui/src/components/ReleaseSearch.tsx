import { FormEvent, useState } from "react";
import { api, errMsg } from "../api";
import { ReleaseSearchPage, ReleaseSearchResult } from "../types";
import { MBLink } from "./MBLink";
import { useToast } from "../toast";

const PAGE = 25;

/** The fielded part of the query. Free text lives outside it, always visible. */
export interface ReleaseFields {
  artist: string;
  release: string;
  date: string;
  country: string;
  format: string;
  tracks: string;
  status: string;
  catno: string;
  barcode: string;
}

export const emptyFields: ReleaseFields = {
  artist: "", release: "", date: "", country: "",
  format: "", tracks: "", status: "", catno: "", barcode: "",
};

/** Artist credit joined the way MusicBrainz intends (respecting join phrases). */
function creditLine(credits: ReleaseSearchResult["artist-credit"]): string {
  if (!credits?.length) return "";
  return credits.map((c) => c.name + (c.joinphrase ?? "")).join("");
}

/** "1977 · UK · Official · 2×CD, 17 tracks" — enough to tell editions apart. */
function releaseMeta(r: ReleaseSearchResult): string {
  const bits: string[] = [];
  if (r.date) bits.push(r.date.slice(0, 4));
  if (r.country) bits.push(r.country);
  if (r.status) bits.push(r.status);
  const tracks = (r.media ?? []).reduce((n, m) => n + (m["track-count"] ?? 0), 0);
  const formats = (r.media ?? []).map((m) => m.format).filter(Boolean);
  if (formats.length) bits.push(formats.join(" + "));
  if (tracks) bits.push(`${tracks} tracks`);
  return bits.join(" · ");
}

/**
 * Reads artist, album and year out of the library layout
 * `<root>/<ARTIST>/<ALBUM> (<YEAR>)/[<MEDIA>]/<TRACK>` so the search starts from
 * what is already on disk, split across the right fields rather than mashed into
 * one string — an album title in the `release` field cannot be mistaken for part
 * of the artist name.
 */
export function guessFields(path: string): ReleaseFields {
  const parts = path.split(/[\\/]/).filter(Boolean);
  if (parts.length < 2) return emptyFields;

  // Skip a media subfolder ("CD1", "Disc 2") so the album folder is found.
  let albumIndex = parts.length - 2;
  if (/^(cd|disc|disk)\s*\d+$/i.test(parts[albumIndex] ?? "")) albumIndex -= 1;

  const folder = parts[albumIndex] ?? "";
  const year = folder.match(/\((\d{4})\)\s*$/)?.[1] ?? "";
  return {
    ...emptyFields,
    artist: parts[albumIndex - 1] ?? "",
    release: folder.replace(/\s*\(\d{4}\)\s*$/, "").trim(),
    date: year,
  };
}

/** Builds the query string for /search/releases from the current form state. */
function queryString(text: string, fields: ReleaseFields, offset: number): string {
  const params = new URLSearchParams();
  if (text.trim()) params.set("q", text.trim());
  for (const [key, value] of Object.entries(fields)) {
    if (value.trim()) params.set(key === "tracks" ? "tracks" : key, value.trim());
  }
  params.set("limit", String(PAGE));
  params.set("offset", String(offset));
  return params.toString();
}

/**
 * The release picker shared by single and bulk attach.
 *
 * Free-text search alone could not separate the editions that actually differ —
 * a common album title returns hundreds of releases — so the fields are the
 * point: artist + year + track count identifies one edition. Pasting a
 * musicbrainz.org URL (or a bare MBID) into the free-text box resolves it
 * directly, because MusicBrainz's own site will always be a better search engine
 * than this form.
 */
export function ReleaseSearch({
  initialFields,
  onPick,
  picking,
}: {
  initialFields?: ReleaseFields;
  onPick: (mbid: string) => void;
  picking?: boolean;
}) {
  const toast = useToast();
  const [text, setText] = useState("");
  const [fields, setFields] = useState<ReleaseFields>(initialFields ?? emptyFields);
  const [more, setMore] = useState(false);
  const [page, setPage] = useState<ReleaseSearchPage | null>(null);
  const [offset, setOffset] = useState(0);
  const [searching, setSearching] = useState(false);

  const set = (key: keyof ReleaseFields) => (e: { target: { value: string } }) =>
    setFields((f) => ({ ...f, [key]: e.target.value }));

  const run = async (at: number) => {
    setSearching(true);
    try {
      setPage(await api.get<ReleaseSearchPage>(`/search/releases?${queryString(text, fields, at)}`));
      setOffset(at);
    } catch (e) {
      toast("err", errMsg(e));
    } finally {
      setSearching(false);
    }
  };

  const submit = (e: FormEvent) => {
    e.preventDefault();
    void run(0);
  };

  const anyInput = text.trim() !== "" || Object.values(fields).some((v) => v.trim() !== "");
  const results = page?.releases ?? [];
  const total = page?.count ?? 0;

  return (
    <div className="stack">
      <form onSubmit={submit} className="stack">
        <div className="row" style={{ gap: 8 }}>
          <input
            className="input"
            style={{ flex: 1 }}
            value={text}
            onChange={(e) => setText(e.target.value)}
            placeholder="Free text, or paste a MusicBrainz URL / ID…"
            autoFocus
          />
          <button type="button" className="btn btn-ghost btn-sm" onClick={() => setMore((m) => !m)}>
            {more ? "Fewer fields" : "More fields"}
          </button>
          <button className="btn btn-primary btn-sm" disabled={searching || !anyInput}>
            {searching ? "Searching…" : "Search"}
          </button>
        </div>

        <div className="field-grid">
          <label className="field">
            <span className="eyebrow">Artist</span>
            <input className="input" value={fields.artist} onChange={set("artist")} />
          </label>
          <label className="field">
            <span className="eyebrow">Release title</span>
            <input className="input" value={fields.release} onChange={set("release")} />
          </label>
          <label className="field">
            <span className="eyebrow">Year</span>
            <input className="input" value={fields.date} onChange={set("date")} placeholder="1977" />
          </label>
        </div>

        {more && (
          <div className="field-grid">
            <label className="field">
              <span className="eyebrow">Tracks</span>
              <input className="input" value={fields.tracks} onChange={set("tracks")} placeholder="17" />
            </label>
            <label className="field">
              <span className="eyebrow">Format</span>
              <select className="select" value={fields.format} onChange={set("format")}>
                <option value="">Any</option>
                <option>CD</option>
                <option>Digital Media</option>
                <option>Vinyl</option>
                <option>Cassette</option>
              </select>
            </label>
            <label className="field">
              <span className="eyebrow">Country</span>
              <input className="input" value={fields.country} onChange={set("country")} placeholder="GB" />
            </label>
            <label className="field">
              <span className="eyebrow">Status</span>
              <select className="select" value={fields.status} onChange={set("status")}>
                <option value="">Any</option>
                <option>Official</option>
                <option>Promotion</option>
                <option>Bootleg</option>
                <option>Pseudo-Release</option>
              </select>
            </label>
            <label className="field">
              <span className="eyebrow">Catalogue no.</span>
              <input className="input mono" value={fields.catno} onChange={set("catno")} />
            </label>
            <label className="field">
              <span className="eyebrow">Barcode</span>
              <input className="input mono" value={fields.barcode} onChange={set("barcode")} />
            </label>
          </div>
        )}
      </form>

      {page && results.length === 0 && (
        <div className="dim" style={{ fontSize: 12 }}>
          Nothing matched. Drop a field or two — the year and track count are the usual culprits — or
          find it on musicbrainz.org and paste the URL above.
        </div>
      )}

      {results.length > 0 && (
        <>
          <div className="row" style={{ justifyContent: "space-between" }}>
            <span className="eyebrow">Releases</span>
            <span className="dim mono" style={{ fontSize: 11 }}>
              {offset + 1}–{offset + results.length} of {total}
            </span>
          </div>
          <div className="scroll">
            <div style={{ display: "flex", flexDirection: "column", gap: 2 }}>
              {results.map((r) => (
                <div
                  key={r.id}
                  className="row"
                  style={{
                    justifyContent: "space-between", padding: "6px 0",
                    borderBottom: "1px solid var(--border)",
                    cursor: picking ? "default" : "pointer", opacity: picking ? 0.5 : 1,
                  }}
                  onClick={() => !picking && onPick(r.id)}
                >
                  <div>
                    <div style={{ color: "var(--text)" }}>
                      {r.title}
                      {r.disambiguation && <span className="dim"> ({r.disambiguation})</span>}
                    </div>
                    <div className="dim" style={{ fontSize: 11 }}>{creditLine(r["artist-credit"])}</div>
                  </div>
                  <div className="row" style={{ gap: 8 }}>
                    <span className="dim mono" style={{ fontSize: 11, textAlign: "right" }}>{releaseMeta(r)}</span>
                    <div onClick={(e) => e.stopPropagation()}>
                      <MBLink entity="release" mbid={r.id} />
                    </div>
                  </div>
                </div>
              ))}
            </div>
          </div>
          {total > results.length && (
            <div className="row" style={{ justifyContent: "flex-end" }}>
              <button
                className="btn btn-secondary btn-sm"
                disabled={searching || offset === 0}
                onClick={() => void run(Math.max(0, offset - PAGE))}
              >
                Prev
              </button>
              <button
                className="btn btn-secondary btn-sm"
                disabled={searching || offset + results.length >= total}
                onClick={() => void run(offset + PAGE)}
              >
                Next
              </button>
            </div>
          )}
        </>
      )}
    </div>
  );
}
