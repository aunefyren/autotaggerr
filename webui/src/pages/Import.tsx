import { FormEvent, useEffect, useMemo, useState } from "react";
import { Link, useSearchParams } from "react-router-dom";
import { api, errMsg } from "../api";
import { useFetch } from "../hooks";
import {
  ArtistSearchResult,
  ImportGroup,
  ImportPreview,
  ImportRow,
  ImportScan,
  Library,
  Manager,
  ReleaseSearchResult,
  ReleaseTrack,
} from "../types";
import { EmptyState, ErrorNote, Modal, Note, Pill } from "../components/ui";
import { MBLink } from "../components/MBLink";
import { ReleaseSearch, guessFields } from "../components/ReleaseSearch";
import { PathInput } from "../components/PathInput";
import { useToast } from "../toast";

/** A path relative to a folder above it, for display; the full path stays in the title. */
function relativeTo(path: string, folder: string): string {
  return path.startsWith(folder) ? path.slice(folder.length).replace(/^[\\/]+/, "") || path : path;
}

// Recently scanned source folders, kept in this browser only: a convenience for the
// person at this screen, not state anyone else needs. Every access is guarded —
// storage can be absent or refuse (private windows, blocked site data), and the page
// must work the same without it.
const RECENT_KEY = "autotaggerr.import.recent";
const RECENT_MAX = 5;

function loadRecent(): string[] {
  try {
    const parsed = JSON.parse(localStorage.getItem(RECENT_KEY) ?? "[]");
    return Array.isArray(parsed) ? parsed.filter((p) => typeof p === "string").slice(0, RECENT_MAX) : [];
  } catch {
    return [];
  }
}

function rememberRecent(path: string): string[] {
  const next = [path, ...loadRecent().filter((p) => p !== path)].slice(0, RECENT_MAX);
  try {
    localStorage.setItem(RECENT_KEY, JSON.stringify(next));
  } catch {
    // Not remembered; nothing else depends on it.
  }
  return next;
}

/** "from the files' tags", "from the folder name", or both when the fields differ. */
function hintSource(g: ImportGroup): string {
  const sources = new Set([g.artist_from, g.album_from, g.year_from].filter(Boolean));
  if (sources.size === 0) return "";
  if (sources.size > 1) return "from tags and the folder name";
  return sources.has("tags") ? "from the files' tags" : "from the folder name";
}

/**
 * File import: loose files on the server into a library, at the path the library's
 * naming templates give them. The scanned folder lives in the URL (so Back and refresh
 * keep the scan) and in a short per-browser recent list.
 */
export default function Import() {
  const toast = useToast();
  const [params, setParams] = useSearchParams();
  const libraries = useFetch<Library[]>(() => api.get("/libraries"));
  const managers = useFetch<Manager[]>(() => api.get("/managers"));
  const [path, setPath] = useState(params.get("path") ?? "");
  const [recent, setRecent] = useState<string[]>(loadRecent);
  const [scan, setScan] = useState<ImportScan | null>(null);
  const [scanning, setScanning] = useState(false);
  const [scanErr, setScanErr] = useState<string | null>(null);
  const [libraryId, setLibraryId] = useState(params.get("library") ?? "");
  const [open, setOpen] = useState<ImportGroup | null>(null);
  const [queued, setQueued] = useState<Set<string>>(new Set());

  // A library under Lidarr cannot take an import — Lidarr decides identity there and
  // tracks its files by path — so it is listed, disabled, with the reason.
  const lidarrManaged = (l: Library) =>
    !!l.manager_id && managers.data?.find((m) => m.id === l.manager_id)?.type === "lidarr";
  const eligible = (libraries.data ?? []).filter((l) => l.enabled && !lidarrManaged(l));
  useEffect(() => {
    if (eligible.length > 0 && !eligible.some((l) => l.id === libraryId)) setLibraryId(eligible[0].id);
  }, [eligible, libraryId]);
  const library = eligible.find((l) => l.id === libraryId) ?? null;

  const runScan = async (target: string) => {
    const trimmed = target.trim();
    if (!trimmed) return;
    setPath(trimmed);
    setScanning(true);
    setScanErr(null);
    try {
      const res = await api.get<ImportScan>(`/import/scan?path=${encodeURIComponent(trimmed)}`);
      setScan(res);
      setQueued(new Set());
      setRecent(rememberRecent(res.root));
      // Replace, not push: re-scanning is not a history entry, but the URL must say
      // what is on screen so returning to the page finds the scan again.
      const next = new URLSearchParams(params);
      next.set("path", res.root);
      setParams(next, { replace: true });
    } catch (err) {
      setScan(null);
      setScanErr(errMsg(err));
    } finally {
      setScanning(false);
    }
  };

  // A path in the URL is a scan someone already ran; run it again on arrival.
  useEffect(() => {
    const initial = params.get("path");
    if (initial) runScan(initial);
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, []);

  const chooseLibrary = (id: string) => {
    setLibraryId(id);
    const next = new URLSearchParams(params);
    next.set("library", id);
    setParams(next, { replace: true });
  };

  const groups = scan?.groups ?? [];
  const totalFiles = groups.reduce((n, g) => n + g.files.length, 0);

  return (
    <div className="stack">
      <div className="page-head">
        <h1>Import</h1>
      </div>
      <p className="muted" style={{ margin: 0, maxWidth: "68ch", lineHeight: 1.55 }}>
        Bring finished downloads into a library. Point at the folder your downloader or rip tool leaves
        albums in — it is only read from, and files leave it when you import them. Each album is identified
        once, every file is checked against its track, and files land where the library's tagger profile
        names them.
      </p>

      <form
        className="card stack"
        onSubmit={(e: FormEvent) => {
          e.preventDefault();
          runScan(path);
        }}
      >
        <div className="row" style={{ gap: 12, alignItems: "flex-end", flexWrap: "wrap" }}>
          <div className="field" style={{ flex: "1 1 380px" }}>
            <label className="flabel" htmlFor="import-path">Source folder (downloads)</label>
            <PathInput id="import-path" value={path} onChange={setPath} placeholder="/downloads/complete/music" />
          </div>
          <div className="field" style={{ flex: "0 1 240px" }}>
            <label className="flabel" htmlFor="import-library">Into library</label>
            <select id="import-library" className="select" value={libraryId} onChange={(e) => chooseLibrary(e.target.value)}>
              {eligible.length === 0 && <option value="">No library can take an import</option>}
              {(libraries.data ?? []).map((l) => (
                <option key={l.id} value={l.id} disabled={!eligible.includes(l)}>
                  {l.name}
                  {lidarrManaged(l) ? " — managed by Lidarr" : !l.enabled ? " — disabled" : ""}
                </option>
              ))}
            </select>
          </div>
          <button className="btn btn-primary" disabled={scanning || !path.trim()}>
            {scanning ? "Scanning…" : "Scan"}
          </button>
        </div>
        {recent.length > 0 && (
          <div className="row" style={{ gap: 8, flexWrap: "wrap" }}>
            <span className="eyebrow">Recent</span>
            {recent.map((p) => (
              <button
                key={p}
                type="button"
                className="chip mono"
                aria-pressed={p === scan?.root}
                onClick={() => runScan(p)}
                disabled={scanning}
              >
                {p}
              </button>
            ))}
            <span className="dim" style={{ fontSize: 11 }}>kept in this browser</span>
          </div>
        )}
        {library && (
          <span className="dim" style={{ fontSize: 11 }}>
            Files go under <span className="mono">{library.path}</span>.
          </span>
        )}
      </form>

      {scanErr && <ErrorNote message={scanErr} />}
      {libraries.data && eligible.length === 0 && (
        <Note kind="warn" title="No library">
          An import needs an enabled library that Autotaggerr manages — add one on{" "}
          <Link to="/libraries">Libraries</Link>, or set an existing one to the native manager.
        </Note>
      )}

      {scan && groups.length === 0 && <EmptyState icon="⇣" message={`No audio files under ${scan.root}.`} />}

      {scan && groups.length > 0 && (
        <>
          <div className="dim" style={{ fontSize: 11 }}>
            {groups.length} album folder{groups.length === 1 ? "" : "s"} · {totalFiles} file{totalFiles === 1 ? "" : "s"} under{" "}
            <span className="mono">{scan.root}</span>
          </div>
          <div className="tablewrap">
            <table className="data">
              <thead>
                <tr>
                  <th>Folder</th>
                  <th>Looks like</th>
                  <th style={{ textAlign: "right" }}>Files</th>
                  <th>Identity</th>
                  <th style={{ textAlign: "right" }}>Actions</th>
                </tr>
              </thead>
              <tbody>
                {groups.map((g) => {
                  const tooMany = g.files.length > scan.max_files;
                  const looksLike = [g.artist, g.album].filter(Boolean).join(" — ");
                  return (
                    <tr key={g.folder}>
                      <td><span className="path" title={g.folder}>{relativeTo(g.folder, scan.root) || "."}</span></td>
                      <td>
                        <div className="stack" style={{ gap: 2 }}>
                          {looksLike ? (
                            <span>
                              {looksLike}
                              {g.year ? <span className="dim"> ({g.year})</span> : null}
                            </span>
                          ) : (
                            <span className="dim">Nothing to go on — search by hand</span>
                          )}
                          {looksLike && <span className="dim" style={{ fontSize: 11 }}>{hintSource(g)}</span>}
                        </div>
                      </td>
                      <td className="mono" style={{ textAlign: "right" }}>{g.files.length}</td>
                      <td>
                        {queued.has(g.folder) ? (
                          <Pill kind="scan">Queued</Pill>
                        ) : g.release_id ? (
                          <Pill kind="ok">Release in tags</Pill>
                        ) : g.release_ids_disagree ? (
                          <Pill kind="warn">Tags disagree</Pill>
                        ) : (
                          <Pill kind="off">Not identified</Pill>
                        )}
                      </td>
                      <td>
                        <div className="row" style={{ justifyContent: "flex-end" }}>
                          {queued.has(g.folder) ? (
                            <Link to="/activity" style={{ fontSize: 12 }}>View in Activity</Link>
                          ) : (
                            <button
                              className="btn btn-secondary btn-sm"
                              disabled={!library || tooMany}
                              title={tooMany ? `Import at most ${scan.max_files} files at a time — scan a folder closer to the album` : !library ? "Choose a library first" : undefined}
                              onClick={() => setOpen(g)}
                            >
                              Import…
                            </button>
                          )}
                        </div>
                      </td>
                    </tr>
                  );
                })}
              </tbody>
            </table>
          </div>
        </>
      )}

      {open && scan && library && (
        <ImportModal
          group={open}
          root={scan.root}
          library={library}
          onClose={() => setOpen(null)}
          onQueued={() => {
            setQueued((cur) => new Set(cur).add(open.folder));
            setOpen(null);
            toast("ok", "Import queued — follow it on Activity");
          }}
        />
      )}
    </div>
  );
}

/**
 * A path in the row's library, shown relative to the library root. The root is not on
 * the row, but the target is, both absolute and relative, so it is their difference.
 */
function libraryRelative(path: string, row: ImportRow): string {
  const root = (row.target ?? "").slice(0, (row.target ?? "").length - (row.target_rel ?? "").length);
  return root ? relativeTo(path, root) : path;
}

/** "2-05 · Dreams", or just the number on a single-disc release. */
function trackLabel(track: ReleaseTrack, multiDisc: boolean): string {
  const position = multiDisc ? `${track.medium}-${track.number}` : track.number;
  return `${position} · ${track.title}${track.video ? " (video)" : ""}`;
}

const CONFLICT_LABELS: Record<string, { label: string; kind: string; replaceable: boolean }> = {
  exists: { label: "File exists", kind: "warn", replaceable: true },
  duplicate: { label: "Already in library", kind: "warn", replaceable: true },
  clash: { label: "Same target", kind: "err", replaceable: false },
};

type Step = "identify" | "review";

/** "1 Identify —— 2 Review"; a finished step is a way back to it. */
function Steps({ step, onIdentify }: { step: Step; onIdentify: () => void }) {
  return (
    <ol className="steps" aria-label="Steps">
      <li className={`step ${step === "identify" ? "current" : "done"}`} aria-current={step === "identify" ? "step" : undefined}>
        <span className="step-num" aria-hidden="true">{step === "identify" ? "1" : "✓"}</span>
        {step === "identify" ? "Identify" : <button type="button" className="step-link" onClick={onIdentify}>Identify</button>}
      </li>
      <li className="step-sep" aria-hidden="true" />
      <li className={`step ${step === "review" ? "current" : ""}`} aria-current={step === "review" ? "step" : undefined}>
        <span className="step-num" aria-hidden="true">2</span>Review
      </li>
    </ol>
  );
}

function ImportModal({
  group,
  root,
  library,
  onClose,
  onQueued,
}: {
  group: ImportGroup;
  root: string;
  library: Library;
  onClose: () => void;
  onQueued: () => void;
}) {
  const toast = useToast();
  const [step, setStep] = useState<Step>("identify");
  const [tab, setTab] = useState<"browse" | "search">("browse");
  const [preview, setPreview] = useState<ImportPreview | null>(null);
  const [loading, setLoading] = useState(false);
  const [busy, setBusy] = useState(false);
  const [mode, setMode] = useState<"move" | "copy">("move");

  const base = { library_id: library.id, root };

  const review = async (mbid: string) => {
    setLoading(true);
    try {
      setPreview(await api.post<ImportPreview>("/import/preview", { ...base, mb_release_id: mbid, sources: group.files }));
      setStep("review");
    } catch (e) {
      toast("err", errMsg(e));
    } finally {
      setLoading(false);
    }
  };

  // Files that already name their release go straight to the review; Identify is one
  // step back if that release is not the one.
  useEffect(() => {
    if (group.release_id) review(group.release_id);
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [group.folder]);

  // Every edit is planned again by the server, which owns where a file goes and what is
  // in its way — the table never computes a target or a conflict of its own.
  const replan = async (rows: ImportRow[]) => {
    if (!preview) return;
    setPreview({ ...preview, rows });
    try {
      setPreview(await api.post<ImportPreview>("/import/preview", { ...base, mb_release_id: preview.release.mb_id, rows }));
    } catch (e) {
      toast("err", errMsg(e));
    }
  };
  const editRow = (source: string, patch: Partial<ImportRow>) =>
    replan((preview?.rows ?? []).map((r) => (r.source === source ? { ...r, ...patch } : r)));

  const submit = async () => {
    if (!preview) return;
    setBusy(true);
    try {
      await api.post("/import", { ...base, mb_release_id: preview.release.mb_id, mode, rows: preview.rows });
      onQueued();
    } catch (e) {
      toast("err", errMsg(e));
      setBusy(false);
    }
  };

  const searchFields = {
    ...guessFields(""),
    artist: group.artist ?? "",
    release: group.album ?? "",
    date: group.year ? String(group.year) : "",
  };

  return (
    <Modal title={`Import ${group.folder.split(/[\\/]/).pop() || group.folder}`} titleHint={group.folder} onClose={onClose} xl>
      <div className="stack">
        <Steps step={step} onIdentify={() => setStep("identify")} />

        {/* Identify stays mounted under the review, so going back finds the search,
            the artist, the album and the edition exactly as they were left. */}
        <div className="stack" style={{ display: step === "identify" ? undefined : "none" }}>
          <FilesSay group={group} />
          <div className="tabs" role="tablist" aria-label="How to find the release">
            <button type="button" role="tab" className="tab" aria-selected={tab === "browse"} onClick={() => setTab("browse")}>Browse</button>
            <button type="button" role="tab" className="tab" aria-selected={tab === "search"} onClick={() => setTab("search")}>Search all releases</button>
          </div>
          <div style={{ display: tab === "browse" ? undefined : "none" }}>
            <Browse group={group} onCancel={onClose} onReview={review} reviewing={loading} />
          </div>
          <div className="stack" style={{ display: tab === "search" ? undefined : "none" }}>
            <ReleaseSearch initialFields={searchFields} onPick={review} picking={loading} />
            <div className="modal-actions">
              <button type="button" className="btn btn-ghost btn-sm" style={{ marginRight: "auto" }} onClick={onClose}>Cancel</button>
            </div>
          </div>
        </div>

        {step === "review" && preview && (
          <Review
            group={group}
            preview={preview}
            mode={mode}
            busy={busy}
            onMode={setMode}
            onEdit={editRow}
            onChangeRelease={() => setStep("identify")}
            onCancel={onClose}
            onSubmit={submit}
          />
        )}
      </div>
    </Modal>
  );
}

/** What the scan read from the files, and from where — the facts a pick is checked against. */
function FilesSay({ group }: { group: ImportGroup }) {
  const from = (source?: string) => (source === "tags" ? "tags" : "folder name");
  return (
    <div className="row" style={{ gap: 8, flexWrap: "wrap", fontSize: 12 }}>
      <span className="eyebrow">The files say</span>
      {group.artist && <span className="fact">Artist <b>{group.artist}</b> <span className="dim">· {from(group.artist_from)}</span></span>}
      {group.album && <span className="fact">Album <b>{group.album}</b> <span className="dim">· {from(group.album_from)}</span></span>}
      {group.year ? <span className="fact">Year <b className="mono">{group.year}</b> <span className="dim">· {from(group.year_from)}</span></span> : null}
      <span className="fact">
        <b className="mono">{group.files.length}</b> audio file{group.files.length === 1 ? "" : "s"} · {group.discs} disc{group.discs === 1 ? "" : "s"}
      </span>
    </div>
  );
}

/** Loose equality for "the album the tags name": case, accents and punctuation aside. */
function looseEq(a?: string, b?: string): boolean {
  const norm = (s?: string) =>
    (s ?? "").normalize("NFD").replace(/[̀-ͯ]/g, "").toLowerCase().replace(/[^\p{L}\p{N}]+/gu, "");
  return norm(a) !== "" && norm(a) === norm(b);
}

interface ReleaseGroupHit {
  id: string;
  title: string;
  "primary-type": string;
  "secondary-types": string[] | null;
  "first-release-date": string;
}

const TYPE_ORDER = ["Album", "EP", "Single", "Broadcast", "Other", ""];
const TYPE_LABELS: Record<string, string> = { Album: "Albums", EP: "EPs", Single: "Singles", Broadcast: "Broadcasts", Other: "Other", "": "Other" };

function editionTracks(e: ReleaseSearchResult): number {
  return (e.media ?? []).reduce((n, m) => n + (m["track-count"] ?? 0), 0);
}

function editionFormats(e: ReleaseSearchResult): string {
  const formats = (e.media ?? []).map((m) => m.format || "?");
  const counted = new Map<string, number>();
  formats.forEach((f) => counted.set(f, (counted.get(f) ?? 0) + 1));
  return [...counted].map(([f, n]) => (n > 1 ? `${n}×${f}` : f)).join(" + ") || "—";
}

/**
 * Artist → album → edition, each a column that narrows the next. It exists so the
 * person importing can be sure: same-named artists are told apart by MusicBrainz's
 * description, the album the tags name is marked, and an edition's track count is set
 * against the number of files — the one fact that separates a 6-track CD from the
 * 14-track box set of the same album.
 */
function Browse({
  group,
  onCancel,
  onReview,
  reviewing,
}: {
  group: ImportGroup;
  onCancel: () => void;
  onReview: (mbid: string) => void;
  reviewing: boolean;
}) {
  const toast = useToast();
  const [query, setQuery] = useState(group.artist ?? "");
  const [artists, setArtists] = useState<ArtistSearchResult[] | null>(null);
  const [artist, setArtist] = useState<ArtistSearchResult | null>(null);
  const [albums, setAlbums] = useState<ReleaseGroupHit[] | null>(null);
  const [albumFilter, setAlbumFilter] = useState("");
  const [album, setAlbum] = useState<ReleaseGroupHit | null>(null);
  const [editions, setEditions] = useState<ReleaseSearchResult[] | null>(null);
  const [edition, setEdition] = useState<ReleaseSearchResult | null>(null);
  const [onlyMatching, setOnlyMatching] = useState(true);
  const [loading, setLoading] = useState<"" | "artists" | "albums" | "editions">("");

  const searchArtists = async (q: string) => {
    if (!q.trim()) return;
    setLoading("artists");
    try {
      const hits = await api.get<ArtistSearchResult[]>(`/search/artists?q=${encodeURIComponent(q.trim())}`);
      setArtists(hits ?? []);
      // One exact name match is the artist the tags mean; several are a choice.
      const exact = (hits ?? []).filter((h) => looseEq(h.name, group.artist));
      if (exact.length === 1) pickArtist(exact[0]);
    } catch (e) {
      toast("err", errMsg(e));
    } finally {
      setLoading("");
    }
  };

  const pickArtist = async (a: ArtistSearchResult) => {
    setArtist(a);
    setAlbum(null);
    setEditions(null);
    setEdition(null);
    setAlbumFilter("");
    setLoading("albums");
    try {
      const groups = await api.get<ReleaseGroupHit[]>(`/search/artists/${a.id}/release-groups`);
      setAlbums(groups ?? []);
      const named = (groups ?? []).filter((g) => looseEq(g.title, group.album));
      if (named.length === 1) pickAlbum(named[0]);
    } catch (e) {
      toast("err", errMsg(e));
    } finally {
      setLoading("");
    }
  };

  const pickAlbum = async (rg: ReleaseGroupHit) => {
    setAlbum(rg);
    setEdition(null);
    setLoading("editions");
    try {
      setEditions((await api.get<ReleaseSearchResult[]>(`/release-groups/${rg.id}/releases`)) ?? []);
    } catch (e) {
      toast("err", errMsg(e));
    } finally {
      setLoading("");
    }
  };

  useEffect(() => {
    if (group.artist) searchArtists(group.artist);
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [group.folder]);

  const files = group.files.length;
  const albumSections = useMemo(() => {
    const shown = (albums ?? []).filter((a) => !albumFilter || a.title.toLowerCase().includes(albumFilter.toLowerCase()));
    return TYPE_ORDER.map((type) => ({
      type,
      rows: shown
        .filter((a) => (TYPE_ORDER.includes(a["primary-type"] ?? "") ? a["primary-type"] ?? "" : "") === type)
        .sort((x, y) => (x["first-release-date"] || "9999").localeCompare(y["first-release-date"] || "9999")),
    })).filter((s) => s.rows.length > 0);
  }, [albums, albumFilter]);

  const matching = (editions ?? []).filter((e) => editionTracks(e) === files);
  const shownEditions = onlyMatching ? matching : editions ?? [];
  const hidden = (editions?.length ?? 0) - shownEditions.length;

  return (
    <div className="stack">
      <div className="pick-grid">
        <div className="pick-col">
          <form
            className="stack"
            style={{ gap: 8 }}
            onSubmit={(e) => {
              e.preventDefault();
              searchArtists(query);
            }}
          >
            <label className="flabel" htmlFor="browse-artist">1 · Artist</label>
            <input id="browse-artist" className="input" value={query} onChange={(e) => setQuery(e.target.value)} placeholder="Search artists" />
          </form>
          {loading === "artists" && <span className="dim" style={{ fontSize: 11 }}>Searching…</span>}
          {artists && artists.length === 0 && <span className="dim" style={{ fontSize: 11 }}>No artist by that name.</span>}
          {artists && artists.length > 0 && (
            <ul className="pick-list">
              {artists.map((a) => (
                <li key={a.id}>
                  <button type="button" className="pick-row" aria-pressed={artist?.id === a.id} onClick={() => pickArtist(a)}>
                    <span>{a.name}</span>
                    <span className="pick-sub">
                      {[a.disambiguation, a.type, a.country].filter(Boolean).join(" · ") || "no description"}
                    </span>
                  </button>
                </li>
              ))}
            </ul>
          )}
          <span className="dim" style={{ fontSize: 11 }}>Same-named artists are told apart by MusicBrainz's description.</span>
        </div>

        <div className="pick-col">
          <label className="flabel" htmlFor="browse-album">2 · Album</label>
          <input
            id="browse-album"
            className="input"
            value={albumFilter}
            onChange={(e) => setAlbumFilter(e.target.value)}
            placeholder={artist ? `Filter ${artist.name}'s albums` : "Pick an artist first"}
            disabled={!artist}
          />
          {loading === "albums" && <span className="dim" style={{ fontSize: 11 }}>Loading albums…</span>}
          {albums && albumSections.length === 0 && <span className="dim" style={{ fontSize: 11 }}>No albums match.</span>}
          {albumSections.length > 0 && (
            <ul className="pick-list">
              {albumSections.map((s) => (
                <li key={s.type}>
                  <div className="pick-head">{TYPE_LABELS[s.type]}</div>
                  <ul>
                    {s.rows.map((rg) => (
                      <li key={rg.id}>
                        <button type="button" className="pick-row pick-row-inline" aria-pressed={album?.id === rg.id} onClick={() => pickAlbum(rg)}>
                          <span>
                            {rg.title}
                            {(rg["secondary-types"] ?? []).length > 0 && <span className="dim"> · {(rg["secondary-types"] ?? []).join(", ")}</span>}
                            {looseEq(rg.title, group.album) && <span className="pick-match"> · matches the {group.album_from === "tags" ? "tags" : "folder"}</span>}
                          </span>
                          <span className="mono dim">{rg["first-release-date"]?.slice(0, 4) || "—"}</span>
                        </button>
                      </li>
                    ))}
                  </ul>
                </li>
              ))}
            </ul>
          )}
        </div>

        <div className="pick-col">
          <span className="flabel">
            3 · Edition{album && editions ? <span style={{ textTransform: "none", letterSpacing: 0 }}> — {editions.length} of {album.title}</span> : null}
          </span>
          <label className="row" style={{ gap: 6, fontSize: 12, minHeight: 32, cursor: "pointer" }}>
            <input type="checkbox" checked={onlyMatching} onChange={(e) => setOnlyMatching(e.target.checked)} disabled={!editions} />
            Only editions with {files} track{files === 1 ? "" : "s"}
          </label>
          {loading === "editions" && <span className="dim" style={{ fontSize: 11 }}>Loading editions…</span>}
          {editions && shownEditions.length > 0 && (
            <ul className="pick-list">
              {shownEditions.map((e) => {
                const tracks = editionTracks(e);
                return (
                  <li key={e.id}>
                    <button type="button" className="pick-row" aria-pressed={edition?.id === e.id} onClick={() => setEdition(e)}>
                      <span className="row" style={{ justifyContent: "space-between", gap: 8 }}>
                        <span>{e.title}</span>
                        <span className="mono dim" style={{ fontSize: 12 }}>{[e.date || "no date", e.country].filter(Boolean).join(" · ")}</span>
                      </span>
                      <span className="pick-sub">
                        {editionFormats(e)} ·{" "}
                        {tracks === files ? (
                          <span className="pick-match" style={{ whiteSpace: "nowrap" }}>{tracks} tracks — matches {files} files</span>
                        ) : (
                          <span>{tracks} tracks</span>
                        )}
                        {e.disambiguation ? ` · ${e.disambiguation}` : ""}
                      </span>
                    </button>
                  </li>
                );
              })}
            </ul>
          )}
          {editions && hidden > 0 && (
            <span className="dim" style={{ fontSize: 11 }}>
              {hidden} edition{hidden === 1 ? "" : "s"} with a different track count hidden.
              {shownEditions.length === 0 && " Untick the filter to see them — the files may be partial, or carry bonus tracks."}
            </span>
          )}
          {editions && editions.length === 0 && <span className="dim" style={{ fontSize: 11 }}>No editions listed.</span>}
        </div>
      </div>

      {edition && (
        <div className="selected-bar">
          <span className="eyebrow">Selected</span>
          <span>{(edition["artist-credit"] ?? []).map((c) => c.name + c.joinphrase).join("") || artist?.name} — {edition.title}</span>
          <span className="mono dim">{[edition.date, edition.country, editionFormats(edition), `${editionTracks(edition)} tracks`].filter(Boolean).join(" · ")}</span>
          <span style={{ marginLeft: "auto" }}><MBLink entity="release" mbid={edition.id} /></span>
        </div>
      )}

      <div className="modal-actions">
        <button type="button" className="btn btn-ghost btn-sm" style={{ marginRight: "auto" }} onClick={onCancel}>Cancel</button>
        <button type="button" className="btn btn-secondary btn-sm" disabled title="This is the first step">← Back</button>
        <button type="button" className="btn btn-primary btn-sm" disabled={!edition || reviewing} onClick={() => edition && onReview(edition.id)}>
          {reviewing ? "Building the mapping…" : "Review mapping →"}
        </button>
      </div>
    </div>
  );
}

function Review({
  group,
  preview,
  mode,
  busy,
  onMode,
  onEdit,
  onChangeRelease,
  onCancel,
  onSubmit,
}: {
  group: ImportGroup;
  preview: ImportPreview;
  mode: "move" | "copy";
  busy: boolean;
  onMode: (m: "move" | "copy") => void;
  onEdit: (source: string, patch: Partial<ImportRow>) => void;
  onChangeRelease: () => void;
  onCancel: () => void;
  onSubmit: () => void;
}) {
  const rows = preview.rows;
  const multiDisc = preview.tracks.some((t) => t.medium !== preview.tracks[0]?.medium);
  const chosen = rows.filter((r) => r.mb_release_track_id);
  const replacing = chosen.filter((r) => r.conflict && r.replace).reduce((n, r) => n + (r.replaces?.length ?? 0), 0);
  const guessedByOrder = chosen.some((r) => r.how === "order");
  const summary = [
    `${chosen.length} file${chosen.length === 1 ? "" : "s"}`,
    mode === "move" ? "moved" : "copied",
    replacing > 0 ? `${replacing} replaced` : "",
    rows.length > chosen.length ? `${rows.length - chosen.length} skipped` : "",
  ]
    .filter(Boolean)
    .join(" · ");

  return (
    <div className="stack">
      <div className="release-card">
        <div className="stack" style={{ gap: 3, minWidth: 0 }}>
          <span className="eyebrow">Importing as</span>
          <span style={{ fontSize: 16, fontWeight: 600 }}>{preview.release.title}</span>
          <span className="mono dim" style={{ fontSize: 12 }}>
            {[preview.release.date, preview.release.country, preview.release.disambiguation, `${preview.tracks.length} tracks`].filter(Boolean).join(" · ")}
          </span>
        </div>
        <div className="row" style={{ gap: 8, marginLeft: "auto" }}>
          <MBLink entity="release" mbid={preview.release.mb_id} />
          <button type="button" className="btn btn-secondary btn-sm" onClick={onChangeRelease} disabled={busy}>Change release</button>
        </div>
      </div>

      <div className="dim" style={{ fontSize: 12, maxWidth: "68ch" }}>
        Check every row. Set a row to <em>Skip</em> if you are not sure what it is.
        {guessedByOrder && " Rows marked “order” were paired by sort order, not by a track number in the filename."}
      </div>

      <div className="tablewrap scroll">
        <table className="data">
          <thead>
            <tr>
              <th>File</th>
              <th style={{ width: 44 }}>How</th>
              <th style={{ width: 240 }}>Track</th>
              <th>Goes to</th>
            </tr>
          </thead>
          <tbody>
            {rows.map((r) => {
              const conflict = r.conflict ? CONFLICT_LABELS[r.conflict] : null;
              const cut = (r.target_rel ?? "").lastIndexOf("/") + 1;
              return (
                <tr key={r.source}>
                  <td><span className="path" title={r.source}>{relativeTo(r.source, group.folder)}</span></td>
                  <td className="dim mono" style={{ fontSize: 11 }}>
                    {r.mb_release_track_id ? (r.how === "order" ? "order" : r.how === "number" ? "№" : "—") : "—"}
                  </td>
                  <td>
                    <select
                      className="select"
                      style={{ width: "100%", borderColor: r.conflict === "clash" ? "var(--danger)" : undefined }}
                      value={r.mb_release_track_id}
                      disabled={busy}
                      aria-label={`Track for ${relativeTo(r.source, group.folder)}`}
                      onChange={(e) => onEdit(r.source, { mb_release_track_id: e.target.value, how: "" })}
                    >
                      <option value="">Skip this file</option>
                      {preview.tracks.map((t) => (
                        <option key={t.track_id} value={t.track_id}>{trackLabel(t, multiDisc)}</option>
                      ))}
                    </select>
                  </td>
                  <td>
                    {!r.mb_release_track_id ? (
                      <span className="dim">— stays in the source folder</span>
                    ) : r.error ? (
                      <span className="help-err">{r.error}</span>
                    ) : (
                      <div className="stack" style={{ gap: 4 }}>
                        <span className="path" title={r.target}>
                          <span className="dim">{(r.target_rel ?? "").slice(0, cut)}</span>
                          {(r.target_rel ?? "").slice(cut)}
                        </span>
                        {conflict && (
                          <div className="row" style={{ gap: 8, flexWrap: "wrap" }}>
                            <Pill kind={conflict.kind}>{conflict.label}</Pill>
                            {conflict.replaceable && (
                              <label className="row replace-consent" title={`Deletes ${(r.replaces ?? []).join(", ")}`}>
                                <input type="checkbox" checked={!!r.replace} disabled={busy} onChange={(e) => onEdit(r.source, { replace: e.target.checked })} />
                                Replace{" "}
                                {(r.replaces?.length ?? 0) > 1 ? (
                                  `${r.replaces?.length} files`
                                ) : (
                                  <span className="mono" style={{ fontSize: 11 }}>{libraryRelative(r.replaces?.[0] ?? "", r)}</span>
                                )}
                              </label>
                            )}
                          </div>
                        )}
                      </div>
                    )}
                  </td>
                </tr>
              );
            })}
          </tbody>
        </table>
      </div>

      <div className="row" style={{ gap: 16, flexWrap: "wrap" }} role="radiogroup" aria-label="How files are put into the library">
        {(["move", "copy"] as const).map((m) => (
          <label key={m} className="row" style={{ gap: 6, cursor: "pointer" }}>
            <input type="radio" name="import-mode" checked={mode === m} disabled={busy} onChange={() => onMode(m)} />
            <span>{m === "move" ? "Move" : "Copy"}</span>
          </label>
        ))}
        <span className="dim" style={{ fontSize: 11 }}>
          {mode === "move" ? "The source files are removed, and folders left empty are cleaned up." : "The source files are left where they are."}
        </span>
      </div>

      {preview.problem && <span className="help-err">{preview.problem}</span>}

      <div className="modal-actions">
        <button type="button" className="btn btn-ghost btn-sm" style={{ marginRight: "auto" }} onClick={onCancel} disabled={busy}>Cancel</button>
        <span className="dim" style={{ fontSize: 12 }}>{summary}</span>
        <button type="button" className="btn btn-secondary btn-sm" onClick={onChangeRelease} disabled={busy}>← Back</button>
        <button type="button" className="btn btn-primary btn-sm" disabled={busy || !!preview.problem} onClick={onSubmit}>
          {busy ? "Queuing…" : `Import ${chosen.length} file${chosen.length === 1 ? "" : "s"}`}
        </button>
      </div>
    </div>
  );
}
