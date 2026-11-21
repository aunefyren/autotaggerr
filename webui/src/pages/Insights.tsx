import { useEffect, useMemo, useState } from "react";
import { Link, useSearchParams } from "react-router-dom";
import { api, errMsg } from "../api";
import { useFetch } from "../hooks";
import { useToast } from "../toast";
import { useLibraries } from "../components/Libraries";
import { formatBytes } from "../components/storage";
import { EmptyState, ErrorNote, IdChip, Skeleton } from "../components/ui";
import { ArtistLink, CollabGraph, CollaboratorRow, GraphColour } from "../components/CollabGraph";
import { StorageTab } from "./Storage";
import { DuplicateFile, DuplicateGroup, DuplicateLevel, DuplicatesReport, GraphNode, GraphReport, LibraryStats, ScanStatus, StatsBucket } from "../types";

/**
 * Insights: questions about the library as a whole. Duplicates and Stats read the
 * index and the release cache only, so they answer inline; checking whether two
 * copies are the same audio is the one thing that reads files, and it is a queued
 * job. See docs/insights.md.
 */
export default function Insights() {
  const [params, setParams] = useSearchParams();
  const tabParam = params.get("tab");
  const tab = tabParam === "stats" || tabParam === "graph" || tabParam === "storage" ? tabParam : "duplicates";
  const library = params.get("library") ?? "";
  const libraries = useLibraries();

  const set = (updates: Record<string, string | null>) => {
    const p = new URLSearchParams(params);
    for (const [key, value] of Object.entries(updates)) {
      if (value) p.set(key, value);
      else p.delete(key);
    }
    setParams(p, { replace: true });
  };

  return (
    <div className="stack">
      <div className="page-head">
        <h1>Insights</h1>
        {/* Disks are not per library, so the select is absent on Storage rather than ignored. */}
        {libraries.multi && tab !== "storage" && (
          <select className="input" style={{ width: "auto" }} value={library} aria-label="Library" onChange={(e) => set({ library: e.target.value || null })}>
            <option value="">All libraries</option>
            {libraries.list.map((l) => (
              <option key={l.id} value={l.id}>{l.name}</option>
            ))}
          </select>
        )}
      </div>
      <div className="tabs" role="tablist" aria-label="Insight">
        <button type="button" role="tab" className="tab" aria-selected={tab === "duplicates"} onClick={() => set({ tab: null })}>Duplicates</button>
        <button type="button" role="tab" className="tab" aria-selected={tab === "stats"} onClick={() => set({ tab: "stats" })}>Stats</button>
        <button type="button" role="tab" className="tab" aria-selected={tab === "graph"} onClick={() => set({ tab: "graph" })}>Graph</button>
        <button type="button" role="tab" className="tab" aria-selected={tab === "storage"} onClick={() => set({ tab: "storage" })}>Storage</button>
      </div>
      {tab === "duplicates" && <Duplicates library={library} multi={libraries.multi} params={params} set={set} />}
      {tab === "stats" && <Stats library={library} />}
      {tab === "graph" && <Graph library={library} params={params} set={set} />}
      {tab === "storage" && <StorageTab />}
    </div>
  );
}

const LEVEL_LABELS: Record<DuplicateLevel, string> = {
  identical: "Identical audio",
  mixed_format: "Lossy beside lossless",
  // Not "different audio": until the check has run, nobody knows.
  same_format: "Same format",
  single_on_album: "Single also on the album",
  across_releases: "Other album or version",
};

const LEVEL_NOTES: Record<DuplicateLevel, string> = {
  identical: "The same audio twice in one album. One copy can go.",
  mixed_format: "The same recording in one album in two formats.",
  same_format: "The same recording in one album twice in one format, with different audio or audio not checked yet: a re-rip, or another master.",
  single_on_album: "A single whose track you also hold on its album, as the same version. The album has it, so the single's copy can go.",
  across_releases: "The same recording on another album (a compilation, an EP) or on another version's edition (a remix or demo edition, a radio edit). Usually intended.",
};

const LEVEL_PILL: Record<DuplicateLevel, string> = {
  identical: "pill-warn",
  mixed_format: "pill-warn",
  same_format: "pill-off",
  single_on_album: "pill-off",
  across_releases: "pill-off",
};

const LEVELS: DuplicateLevel[] = ["identical", "mixed_format", "same_format", "single_on_album", "across_releases"];

/**
 * A refetch in flight while the previous answer stays on screen: the striped bar that
 * means "working, unquantified" everywhere else, and the word, so it is never only a
 * picture. The previous answer is dimmed beside it (`.is-updating`).
 */
function Updating({ active }: { active: boolean }) {
  // Always rendered, hidden when idle: appearing would reflow the toolbar and push
  // the content below it down for the length of every refetch.
  return (
    <span className="row updating" role="status" style={{ gap: 6, visibility: active ? "visible" : "hidden" }} aria-hidden={!active}>
      <span className="coverage" style={{ width: 48 }} aria-hidden="true">
        <span className="coverage-track indeterminate" />
      </span>
      <span>Updating…</span>
    </span>
  );
}

/**
 * Groups fetched per page. The server pages them: at 100k tracks the whole list was
 * 14 MB of JSON for a page that draws a hundred at a time.
 */
const PAGE = 100;

function Duplicates({
  library,
  multi,
  params,
  set,
}: {
  library: string;
  multi: boolean;
  params: URLSearchParams;
  set: (updates: Record<string, string | null>) => void;
}) {
  const toast = useToast();
  const acrossLibraries = params.get("across_libraries") === "1";
  const acrossReleases = params.get("across_releases") === "1";
  const compilations = params.get("compilations") === "1";
  const query = new URLSearchParams();
  if (library) query.set("library", library);
  if (acrossLibraries) query.set("across_libraries", "1");
  if (acrossReleases) query.set("across_releases", "1");
  if (compilations) query.set("compilations", "1");
  const qs = query.toString();

  const report = useFetch<DuplicatesReport>(() => api.get(`/insights/duplicates?${qs}${qs ? "&" : ""}limit=${PAGE}`), [qs]);
  // Pages after the first, appended by "Show more"; dropped whenever the first page
  // is fetched again, so a filter change or a finished check never mixes two answers.
  const [more, setMore] = useState<DuplicateGroup[]>([]);
  const [loadingMore, setLoadingMore] = useState(false);
  useEffect(() => setMore([]), [report.data]);
  const loadMore = async () => {
    if (!report.data) return;
    setLoadingMore(true);
    try {
      const offset = report.data.groups.length + more.length;
      const page = await api.get<DuplicatesReport>(`/insights/duplicates?${qs}${qs ? "&" : ""}limit=${PAGE}&offset=${offset}`);
      setMore((m) => [...m, ...page.groups]);
    } catch (e) {
      toast("err", errMsg(e));
    } finally {
      setLoadingMore(false);
    }
  };

  // While the check runs, follow it and reload when it finishes, so the groups it
  // settles change in front of the person who asked.
  const [checking, setChecking] = useState(false);
  useEffect(() => {
    if (!checking) return;
    const t = setInterval(async () => {
      try {
        const s = await api.get<ScanStatus>("/process/status");
        const busy = s.current_job?.kind === "check_duplicates" || (s.queue ?? []).some((j) => j.kind === "check_duplicates");
        if (!busy) {
          setChecking(false);
          report.reload();
        }
      } catch {
        setChecking(false);
      }
    }, 3000);
    return () => clearInterval(t);
  }, [checking, report.reload]);

  const check = async () => {
    try {
      await api.post("/insights/duplicates/check", {});
      setChecking(true);
      toast("info", "Duplicate audio check queued — see Activity");
    } catch (e) {
      toast("err", errMsg(e));
    }
  };

  const data = report.data;
  return (
    <div className="stack">
      <p className="muted" style={{ margin: 0, maxWidth: "68ch", lineHeight: 1.55 }}>
        Files that are the same MusicBrainz recording. Copies are compared only within one album; the same
        recording on two albums is listed apart, because removing either copy loses an album.
      </p>

      <div className="table-toolbar">
        {multi && (
          <label className="row" style={{ gap: 6, cursor: "pointer", fontSize: 12 }}>
            <input type="checkbox" checked={acrossLibraries} onChange={(e) => set({ across_libraries: e.target.checked ? "1" : null })} />
            <span>Compare across libraries</span>
          </label>
        )}
        <label className="row" style={{ gap: 6, cursor: "pointer", fontSize: 12 }}>
          <input
            type="checkbox"
            checked={acrossReleases}
            onChange={(e) => set(e.target.checked ? { across_releases: "1" } : { across_releases: null, compilations: null })}
          />
          <span>Show other albums and versions</span>
        </label>
        {acrossReleases && (
          <label className="row" style={{ gap: 6, cursor: "pointer", fontSize: 12 }}>
            <input type="checkbox" checked={compilations} onChange={(e) => set({ compilations: e.target.checked ? "1" : null })} />
            <span>Include compilations</span>
          </label>
        )}
        {data && (
          <span className="toolbar-count dim mono row" style={{ gap: 10 }}>
            <Updating active={report.loading} />
            {data.matched} {data.matched === 1 ? "group" : "groups"}
          </span>
        )}
      </div>

      {report.err && <ErrorNote message={report.err} />}
      {!data && !report.err && (
        <div className="card stack" style={{ gap: 10 }}>
          <Skeleton w="40%" />
          <Skeleton w="70%" />
          <Skeleton w="55%" />
        </div>
      )}

      {data && (
        <div className={`stack${report.loading ? " is-updating" : ""}`}>
          <LevelSummary report={data} acrossReleases={acrossReleases} compilations={compilations} />

          {(data.unchecked > 0 || checking) && (
            <div className="card row" style={{ gap: 16, justifyContent: "space-between", flexWrap: "wrap" }}>
              <div className="stack" style={{ gap: 4, maxWidth: "68ch" }}>
                <span>
                  {checking ? "Checking audio…" : `${data.unchecked} ${data.unchecked === 1 ? "file has" : "files have"} audio not compared yet`}
                </span>
                <span className="dim" style={{ fontSize: 12 }}>
                  Copies in one album and one format may be the same audio. Checking reads each file once (no
                  tags, no decoding) and writes nothing to it. A tag write makes a file unchecked again.
                </span>
              </div>
              <button type="button" className="btn btn-secondary btn-sm" disabled={checking} onClick={check}>
                {checking ? "Checking…" : "Check audio"}
              </button>
            </div>
          )}

          {data.matched === 0 ? (
            <EmptyState icon="◎" message="No duplicates with these filters." />
          ) : (
            <div className="stack" style={{ gap: 10 }}>
              {[...data.groups, ...more].map((g) => (
                <GroupCard key={`${g.recording_mb_id}:${g.files[0]?.library_id}`} group={g} showLibrary={multi} />
              ))}
              {data.matched > data.groups.length + more.length && (
                <button type="button" className="btn btn-ghost btn-sm" style={{ alignSelf: "center" }} disabled={loadingMore} onClick={loadMore}>
                  {loadingMore
                    ? "Loading…"
                    : `Show ${Math.min(PAGE, data.matched - data.groups.length - more.length)} more of ${data.matched - data.groups.length - more.length}`}
                </button>
              )}
            </div>
          )}
        </div>
      )}
    </div>
  );
}

/** Every level's count, including the ones the filters hide, so a hidden one is said. */
function LevelSummary({ report, acrossReleases, compilations }: { report: DuplicatesReport; acrossReleases: boolean; compilations: boolean }) {
  return (
    <div className="grid-cards">
      {LEVELS.map((level) => {
        const n = report.counts[level] ?? 0;
        let hidden = "";
        if (level === "across_releases") {
          if (!acrossReleases && n > 0) hidden = "hidden";
          else if (!compilations && report.hidden_compilations > 0) hidden = `${report.hidden_compilations} compilations hidden`;
        }
        return (
          <div key={level} className="card stat" title={LEVEL_NOTES[level]}>
            <div className="n" style={{ color: n > 0 && LEVEL_PILL[level] === "pill-warn" ? "var(--warning-text)" : undefined }}>{n}</div>
            <div className="l">{LEVEL_LABELS[level]}</div>
            {hidden && <div className="dim" style={{ fontSize: 11, marginTop: 4 }}>{hidden}</div>}
          </div>
        );
      })}
    </div>
  );
}

function GroupCard({ group, showLibrary }: { group: DuplicateGroup; showLibrary: boolean }) {
  // Copies that share audio are numbered alike, so "same as which?" is answered in place.
  const hashLabels = useMemo(() => {
    const counts = new Map<string, number>();
    for (const f of group.files) if (f.audio_hash) counts.set(f.audio_hash, (counts.get(f.audio_hash) ?? 0) + 1);
    const labels = new Map<string, string>();
    for (const [h, n] of counts) if (n > 1) labels.set(h, String.fromCharCode(65 + labels.size));
    return labels;
  }, [group.files]);

  return (
    <div className="card stack" style={{ gap: 10 }}>
      <div className="row" style={{ justifyContent: "space-between", flexWrap: "wrap", gap: 8 }}>
        <div className="stack" style={{ gap: 2 }}>
          <span style={{ fontWeight: 600 }}>{group.title || "Unknown recording"}</span>
          {group.artist && <span className="muted" style={{ fontSize: 12 }}>{group.artist}</span>}
        </div>
        <div className="row" style={{ gap: 6, flexWrap: "wrap" }}>
          {group.audio_unchecked && <span className="dim" style={{ fontSize: 11 }}>audio not checked</span>}
          {group.identical_audio && <span className="pill pill-off">same audio</span>}
          {group.compilation && <span className="pill pill-off">compilation</span>}
          <span className={`pill ${LEVEL_PILL[group.level]}`} title={LEVEL_NOTES[group.level]}>
            <span className="dot" />
            {LEVEL_LABELS[group.level]}
          </span>
          <IdChip value={group.recording_mb_id} />
        </div>
      </div>
      <div style={{ overflowX: "auto" }}>
        <table className="data">
          <thead>
            <tr>
              {showLibrary && <th>Library</th>}
              <th>Path</th>
              <th>Format</th>
              <th style={{ textAlign: "right" }}>Size</th>
              <th>Release</th>
              <th>Audio</th>
            </tr>
          </thead>
          <tbody>
            {group.files.map((f) => (
              <FileRow key={f.item_id} file={f} showLibrary={showLibrary} same={f.audio_hash ? hashLabels.get(f.audio_hash) : undefined} />
            ))}
          </tbody>
        </table>
      </div>
    </div>
  );
}

function FileRow({ file, showLibrary, same }: { file: DuplicateFile; showLibrary: boolean; same?: string }) {
  return (
    <tr>
      {showLibrary && <td>{file.library_name}</td>}
      {/* Relative to the library root, the full path on hover — as the Items modal shows paths. */}
      <td className="mono" style={{ wordBreak: "break-all" }} title={file.path}>{file.relative_path || file.path}</td>
      <td className="mono">{file.format}</td>
      <td className="mono tabnum" style={{ textAlign: "right", whiteSpace: "nowrap" }}>{formatBytes(file.size)}</td>
      <td>
        <Link to={`/items?mbid=${file.release_mb_id}`}>{file.release_title || file.release_mb_id}</Link>
        {file.release_type && <span className="dim"> · {file.release_type}</span>}
        {file.version && <span className="dim" title="The version this copy is, from its edition or track title; copies of different versions are not compared"> · {file.version}</span>}
      </td>
      <td className="mono" style={{ whiteSpace: "nowrap" }}>
        {same ? (
          <span style={{ color: "var(--warning-text)" }} title={file.audio_hash}>same as {same}</span>
        ) : file.audio_hash ? (
          <span className="dim" title={file.audio_hash}>{file.audio_hash.slice(0, 8)}</span>
        ) : (
          <span className="dim">—</span>
        )}
      </td>
    </tr>
  );
}

function Stats({ library }: { library: string }) {
  const stats = useFetch<LibraryStats>(() => api.get(`/insights/stats${library ? `?library=${library}` : ""}`), [library]);
  if (stats.err) return <ErrorNote message={stats.err} />;
  const s = stats.data;
  if (!s) {
    return (
      <div className="grid-cards">
        {[0, 1, 2, 3, 4, 5].map((i) => (
          <div key={i} className="card stat stack" style={{ gap: 8 }}>
            <Skeleton w={60} h={22} />
            <Skeleton w={90} />
          </div>
        ))}
      </div>
    );
  }
  if (s.files === 0) return <EmptyState icon="◎" message="No files indexed yet. Process a library to see what it holds." />;

  const tiles: [string, string][] = [
    ["Files", s.files.toLocaleString()],
    ["Size", formatBytes(s.bytes)],
    ["Play time", formatDuration(s.duration_ms)],
    ["Recordings", s.recordings.toLocaleString()],
    ["Albums", s.albums.toLocaleString()],
    ["Artists", s.artists.toLocaleString()],
  ];
  const c = s.coverage;

  return (
    <div className="stack">
      <div className="grid-cards">
        {tiles.map(([label, value]) => (
          <div key={label} className="card stat">
            <div className="n">{value}</div>
            <div className="l">{label}</div>
          </div>
        ))}
      </div>
      {s.uncached > 0 && (
        <div className="dim" style={{ fontSize: 12 }}>
          {s.uncached} identified {s.uncached === 1 ? "file's release is" : "files' releases are"} not in the metadata
          cache, so play time, albums, artists and the breakdowns below leave {s.uncached === 1 ? "it" : "them"} out
          until the next metadata refresh.
        </div>
      )}

      <div className="insight-grid">
        <Breakdown title="Formats" buckets={s.formats} showBytes />
        <Breakdown title="Quality" buckets={s.qualities ?? []} showBytes />
        <Breakdown title="Release types" buckets={s.release_types} />
        <Breakdown title="Decades" buckets={s.decades} />
        <Breakdown title="Genres" buckets={s.genres} />
        <Breakdown title="Countries" buckets={s.countries} />
        <Breakdown title="Labels" buckets={s.labels} />
      </div>

      <div className="insight-grid">
        <div className="card stack" style={{ gap: 12 }}>
          <div className="eyebrow">Coverage</div>
          <div className="breakdown">
            <CoverageRow label="ISRC known" n={c.isrc} of={s.identified} />
            <CoverageRow label="Album cover cached" n={c.artwork} of={s.identified} />
            <CoverageRow label="ReplayGain measured" n={c.replaygain} of={s.files} />
            <CoverageRow label="BPM measured" n={c.bpm} of={s.files} />
            <CoverageRow label="Identified" n={s.identified} of={s.files} />
          </div>
          <div className="row" style={{ gap: 16, flexWrap: "wrap", fontSize: 12 }}>
            <CountLink n={c.unmatched} label="unmatched" status="unmatched" />
            <CountLink n={c.errors} label="failing" status="error" danger />
            <CountLink n={c.pinned} label="pinned" />
            <CountLink n={c.pins_held} label="pins need review" status="pin_held" />
          </div>
        </div>
        {s.catalogue && (
          <div className="card stack" style={{ gap: 12 }}>
            <div className="eyebrow">Catalogue against disk</div>
            <div className="dim" style={{ fontSize: 12 }}>Albums, across the whole collection.</div>
            <div className="stack" style={{ gap: 8 }}>
              <CatalogueRow n={s.catalogue.wanted} label="Wanted, no file on disk" />
              <CatalogueRow n={s.catalogue.incomplete} label="On disk with tracks missing" />
              <CatalogueRow n={s.catalogue.unlisted} label="On disk, in no catalogue" />
            </div>
            <Link to="/collection" className="mono" style={{ fontSize: 12 }}>View collection →</Link>
          </div>
        )}
      </div>
    </div>
  );
}

/**
 * One breakdown: a value per row, its bar against the largest, and its count. One
 * hue throughout — it is magnitude, not identity — and the count is always printed
 * beside the bar, so the bar is never the only carrier of the number.
 */
function Breakdown({ title, buckets, showBytes }: { title: string; buckets: StatsBucket[]; showBytes?: boolean }) {
  const max = Math.max(1, ...buckets.map((b) => b.files));
  return (
    <div className="card stack" style={{ gap: 12 }}>
      <div className="eyebrow">{title}</div>
      {buckets.length === 0 ? (
        <div className="dim" style={{ fontSize: 12 }}>Nothing known yet.</div>
      ) : (
        <div className="breakdown">
          {buckets.map((b) => (
            <BarRow
              key={b.key}
              label={b.key}
              value={b.files}
              max={max}
              muted={b.key === "Unknown" || b.key === "Other"}
              text={showBytes ? `${b.files.toLocaleString()} · ${formatBytes(b.bytes)}` : b.files.toLocaleString()}
              tooltip={`${b.key}: ${b.files.toLocaleString()} files, ${formatBytes(b.bytes)}`}
            />
          ))}
        </div>
      )}
    </div>
  );
}

function CoverageRow({ label, n, of }: { label: string; n: number; of: number }) {
  const pct = of > 0 ? Math.round((n / of) * 100) : 0;
  return <BarRow label={label} value={n} max={Math.max(1, of)} text={`${n.toLocaleString()}/${of.toLocaleString()}`} tooltip={`${label}: ${n} of ${of} files (${pct}%)`} />;
}

function BarRow({ label, value, max, text, tooltip, muted }: { label: string; value: number; max: number; text: string; tooltip: string; muted?: boolean }) {
  const width = `${Math.max(value > 0 ? 2 : 0, (value / max) * 100)}%`;
  return (
    <div className="breakdown-row" title={tooltip}>
      <span className={muted ? "dim" : "muted"} style={{ overflow: "hidden", textOverflow: "ellipsis", whiteSpace: "nowrap" }}>{label}</span>
      <span className="breakdown-bar" role="img" aria-label={tooltip}>
        <span className={muted ? "muted-fill" : undefined} style={{ width }} />
      </span>
      <span className="mono tabnum dim" style={{ textAlign: "right", whiteSpace: "nowrap" }}>{text}</span>
    </div>
  );
}

function CountLink({ n, label, status, danger }: { n: number; label: string; status?: string; danger?: boolean }) {
  const body = (
    <>
      <span className="tabnum" style={{ fontWeight: 600, color: danger && n > 0 ? "var(--danger-text)" : undefined }}>{n.toLocaleString()}</span>{" "}
      <span className="muted">{label}</span>
    </>
  );
  if (!status || n === 0) return <span>{body}</span>;
  return <Link to={`/items?status=${status}`} style={{ textDecoration: "none" }}>{body} →</Link>;
}

function CatalogueRow({ n, label }: { n: number; label: string }) {
  return (
    <div className="row" style={{ gap: 12, alignItems: "baseline" }}>
      <span className="tabnum" style={{ fontSize: 18, fontWeight: 600, minWidth: 48, textAlign: "right" }}>{n.toLocaleString()}</span>
      <span className="muted">{label}</span>
    </div>
  );
}

/** Play time as days and hours, or hours and minutes under a day. */
function formatDuration(ms: number): string {
  const minutes = Math.round(ms / 60000);
  const days = Math.floor(minutes / 1440);
  const hours = Math.floor((minutes % 1440) / 60);
  if (days > 0) return `${days}d ${hours}h`;
  return `${hours}h ${minutes % 60}m`;
}

/** The credits that can connect two artists (models.InsightRole*). */
const GRAPH_ROLES: { key: string; label: string }[] = [
  { key: "credit", label: "Credited artists" },
  { key: "performer", label: "Performers" },
  { key: "production", label: "Production" },
  { key: "writing", label: "Writing" },
];

/**
 * The graph's filters, one entry each. Every key is the query parameter the API reads
 * (routers/insights.go) and the page's own URL parameter, so a filter is added by adding
 * an entry here and a field on insights.GraphOptions — no plumbing in between.
 */
type GraphFilter =
  | { kind: "select"; key: string; label: string; options: { value: string; label: string }[]; fallback: string }
  | { kind: "check"; key: string; label: string };

const GRAPH_FILTERS: GraphFilter[] = [
  {
    kind: "select",
    key: "min_weight",
    label: "Shared recordings",
    fallback: "1",
    options: ["1", "2", "3", "5", "10"].map((v) => ({ value: v, label: `${v}+` })),
  },
  {
    kind: "select",
    key: "min_tracks",
    label: "Artist recordings",
    fallback: "1",
    options: ["1", "2", "5", "10", "25"].map((v) => ({ value: v, label: `${v}+` })),
  },
  {
    kind: "select",
    key: "limit",
    label: "Artists shown",
    fallback: "150",
    options: ["50", "150", "300", "500"].map((v) => ({ value: v, label: v })),
  },
  { kind: "check", key: "special", label: "Include Various Artists" },
];

function Graph({
  library,
  params,
  set,
}: {
  library: string;
  params: URLSearchParams;
  set: (updates: Record<string, string | null>) => void;
}) {
  const roles = (params.get("roles") ?? "credit").split(",").filter(Boolean);
  const artist = params.get("artist") ?? "";
  const hops = params.get("hops") === "2" ? "2" : "1";
  // Colour is drawn, not asked for: it never reaches the API, so changing it is instant.
  const colourParam = params.get("colour");
  const colour: GraphColour = colourParam === "role" || colourParam === "none" ? colourParam : "genre";
  const [selected, setSelected] = useState<string | null>(null);

  const query = new URLSearchParams();
  if (library) query.set("library", library);
  query.set("roles", roles.join(","));
  for (const f of GRAPH_FILTERS) {
    const v = params.get(f.key);
    if (v) query.set(f.key, v);
  }
  if (artist) {
    query.set("artist", artist);
    query.set("hops", hops);
  }
  const qs = query.toString();
  const graph = useFetch<GraphReport>(() => api.get(`/insights/graph?${qs}`), [qs]);
  useEffect(() => setSelected(null), [qs]);

  const toggleRole = (key: string, on: boolean) => {
    const next = on ? [...roles, key] : roles.filter((r) => r !== key);
    // Never none: no role connects nobody, which would read as an empty library.
    if (next.length === 0) return;
    set({ roles: next.join(",") === "credit" ? null : GRAPH_ROLES.map((r) => r.key).filter((k) => next.includes(k)).join(",") });
  };
  const centre = (mbid: string | null) => set({ artist: mbid, hops: null });

  const data = graph.data;
  const nodes = useMemo(() => new Map((data?.nodes ?? []).map((n) => [n.mb_id, n])), [data]);
  const picked = selected ? nodes.get(selected) : undefined;

  return (
    <div className="stack">
      <p className="muted" style={{ margin: 0, maxWidth: "68ch", lineHeight: 1.55 }}>
        Who works with whom in your library. Two artists are connected when the same recording credits both; the
        line is thicker the more recordings they share. Credits come from MusicBrainz, read from the releases
        already cached.
      </p>

      <div className="table-toolbar">
        {GRAPH_ROLES.map((r) => (
          <label key={r.key} className="row" style={{ gap: 6, cursor: "pointer", fontSize: 12 }}>
            <input type="checkbox" checked={roles.includes(r.key)} onChange={(e) => toggleRole(r.key, e.target.checked)} />
            <span>{r.label}</span>
          </label>
        ))}
      </div>
      <div className="table-toolbar">
        {GRAPH_FILTERS.map((f) =>
          f.kind === "select" ? (
            <label key={f.key} className="row" style={{ gap: 6, fontSize: 12 }}>
              <span className="muted">{f.label}</span>
              <select
                className="input"
                style={{ width: "auto" }}
                value={params.get(f.key) ?? f.fallback}
                onChange={(e) => set({ [f.key]: e.target.value === f.fallback ? null : e.target.value })}
              >
                {f.options.map((o) => (
                  <option key={o.value} value={o.value}>{o.label}</option>
                ))}
              </select>
            </label>
          ) : (
            <label key={f.key} className="row" style={{ gap: 6, cursor: "pointer", fontSize: 12 }}>
              <input type="checkbox" checked={params.get(f.key) === "1"} onChange={(e) => set({ [f.key]: e.target.checked ? "1" : null })} />
              <span>{f.label}</span>
            </label>
          ),
        )}
        <label className="row" style={{ gap: 6, fontSize: 12 }}>
          <span className="muted">Colour</span>
          <select className="input" style={{ width: "auto" }} value={colour} onChange={(e) => set({ colour: e.target.value === "genre" ? null : e.target.value })}>
            <option value="genre">Genre</option>
            <option value="role">Role</option>
            <option value="none">None</option>
          </select>
        </label>
        {data && (
          <span className="toolbar-count dim mono row" style={{ gap: 10 }}>
            <Updating active={graph.loading} />
            {data.nodes.length} of {data.connected} artists · {data.edges.length} connections
          </span>
        )}
      </div>

      {data?.center && (
        <div className="selected-bar row" style={{ gap: 10, flexWrap: "wrap" }}>
          <span>
            Centred on <strong>{data.center.name}</strong>
          </span>
          <div className="tabs" role="tablist" aria-label="Distance" style={{ borderBottom: "none" }}>
            <button type="button" role="tab" className="tab" aria-selected={hops === "1"} onClick={() => set({ hops: null })}>Collaborators</button>
            <button type="button" role="tab" className="tab" aria-selected={hops === "2"} onClick={() => set({ hops: "2" })}>And theirs</button>
          </div>
          <button type="button" className="btn btn-secondary btn-sm" style={{ marginLeft: "auto" }} onClick={() => centre(null)}>
            Show everyone
          </button>
        </div>
      )}

      {graph.err && <ErrorNote message={graph.err} />}
      {!data && !graph.err && (
        <div className="graph-split">
          <div className="graph-canvas"><Skeleton w="100%" h={520} /></div>
          <div className="card stack" style={{ gap: 10 }}>
            <Skeleton w="60%" />
            <Skeleton w="80%" />
            <Skeleton w="70%" />
          </div>
        </div>
      )}

      {data && data.nodes.length === 0 && (
        <EmptyState
          icon="◔"
          message={
            artist && !data.center
              ? "That artist has no credits in scope."
              : data.unsynced > 0 && data.connected === 0
                ? "No credits read yet. They come from cached releases, which the next Scan or metadata refresh fills in."
                : "No two artists share a recording with these filters."
          }
        />
      )}

      {data && data.nodes.length > 0 && (
        <div className={`graph-split${graph.loading ? " is-updating" : ""}`}>
          <CollabGraph report={data} selected={selected} onSelect={setSelected} colour={colour} />
          <div className="card stack" style={{ gap: 12, alignSelf: "start" }}>
            {picked ? (
              <SelectedArtist node={picked} report={data} nodes={nodes} onSelect={setSelected} onCentre={centre} centred={artist === picked.mb_id} />
            ) : (
              <>
                <div className="eyebrow">{data.center ? `${data.center.name}'s collaborators` : "Strongest collaborations"}</div>
                {data.collaborators.length === 0 ? (
                  <div className="dim" style={{ fontSize: 12 }}>None with these filters.</div>
                ) : (
                  <div className="stack" style={{ gap: 2 }}>
                    {data.collaborators.map((c) =>
                      data.center ? (
                        <CollaboratorRow key={c.b.mb_id} node={c.b} weight={c.weight} onFocus={setSelected} />
                      ) : (
                        <PairRow key={`${c.a.mb_id}-${c.b.mb_id}`} a={c.a} b={c.b} weight={c.weight} onFocus={setSelected} />
                      ),
                    )}
                  </div>
                )}
                <div className="dim" style={{ fontSize: 11 }}>Select an artist in the graph or the list; drag to pan, scroll to zoom.</div>
              </>
            )}
            {data.truncated && (
              <div className="dim" style={{ fontSize: 11 }}>
                Showing the {data.nodes.length} most connected of {data.connected}. Raise the floors or centre on an artist to see the rest.
              </div>
            )}
            {data.unsynced > 0 && (
              <div className="dim" style={{ fontSize: 11 }}>
                {data.unsynced} {data.unsynced === 1 ? "release has" : "releases have"} no credits read yet, because{" "}
                {data.unsynced === 1 ? "it is" : "they are"} not cached.
              </div>
            )}
          </div>
        </div>
      )}
    </div>
  );
}

function PairRow({ a, b, weight, onFocus }: { a: GraphNode; b: GraphNode; weight: number; onFocus: (mbid: string) => void }) {
  return (
    <div className="collab-row">
      <span className="collab-pair">
        <button type="button" className="collab-name" onClick={() => onFocus(a.mb_id)}>{a.name}</button>
        <span className="dim"> × </span>
        <button type="button" className="collab-name" onClick={() => onFocus(b.mb_id)}>{b.name}</button>
      </span>
      <span className="mono tabnum dim">{weight}</span>
    </div>
  );
}

function SelectedArtist({
  node,
  report,
  nodes,
  onSelect,
  onCentre,
  centred,
}: {
  node: GraphNode;
  report: GraphReport;
  nodes: Map<string, GraphNode>;
  onSelect: (mbid: string | null) => void;
  onCentre: (mbid: string) => void;
  centred: boolean;
}) {
  const collaborators = report.edges
    .filter((e) => e.source === node.mb_id || e.target === node.mb_id)
    .map((e) => ({ node: nodes.get(e.source === node.mb_id ? e.target : e.source)!, weight: e.weight }))
    .filter((c) => c.node)
    .sort((x, y) => y.weight - x.weight);
  return (
    <>
      <div className="row" style={{ justifyContent: "space-between", gap: 8 }}>
        <div className="stack" style={{ gap: 2 }}>
          <span style={{ fontWeight: 600 }}>{node.name}</span>
          <span className="dim mono" style={{ fontSize: 11 }}>
            {node.tracks} {node.tracks === 1 ? "recording" : "recordings"}
          </span>
        </div>
        <button type="button" className="btn btn-ghost btn-sm" onClick={() => onSelect(null)} aria-label="Clear selection">✕</button>
      </div>
      <div className="row" style={{ gap: 10, flexWrap: "wrap" }}>
        {!centred && (
          <button type="button" className="btn btn-secondary btn-sm" onClick={() => onCentre(node.mb_id)}>
            Centre on this artist
          </button>
        )}
        <ArtistLink node={node} />
        <IdChip value={node.mb_id} />
      </div>
      <div className="eyebrow">Connected here</div>
      {collaborators.length === 0 ? (
        <div className="dim" style={{ fontSize: 12 }}>No connections among the artists shown.</div>
      ) : (
        <div className="stack" style={{ gap: 2 }}>
          {collaborators.map((c) => (
            <CollaboratorRow key={c.node.mb_id} node={c.node} weight={c.weight} onFocus={onSelect} />
          ))}
        </div>
      )}
    </>
  );
}
