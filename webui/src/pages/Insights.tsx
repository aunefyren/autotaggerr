import { useEffect, useMemo, useState } from "react";
import { Link, useSearchParams } from "react-router-dom";
import { api, errMsg } from "../api";
import { useFetch } from "../hooks";
import { useToast } from "../toast";
import { useLibraries } from "../components/Libraries";
import { formatBytes } from "../components/storage";
import { EmptyState, ErrorNote, IdChip, Skeleton } from "../components/ui";
import { DuplicateFile, DuplicateGroup, DuplicateLevel, DuplicatesReport, LibraryStats, ScanStatus, StatsBucket } from "../types";

/**
 * Insights: questions about the library as a whole. Duplicates and Stats read the
 * index and the release cache only, so they answer inline; checking whether two
 * copies are the same audio is the one thing that reads files, and it is a queued
 * job. See docs/insights.md.
 */
export default function Insights() {
  const [params, setParams] = useSearchParams();
  const tab = params.get("tab") === "stats" ? "stats" : "duplicates";
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
        {libraries.multi && (
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
      </div>
      {tab === "duplicates" ? (
        <Duplicates library={library} multi={libraries.multi} params={params} set={set} />
      ) : (
        <Stats library={library} />
      )}
    </div>
  );
}

const LEVEL_LABELS: Record<DuplicateLevel, string> = {
  identical: "Identical audio",
  mixed_format: "Lossy beside lossless",
  same_format: "Same format, different audio",
  across_releases: "Also on another album",
};

const LEVEL_NOTES: Record<DuplicateLevel, string> = {
  identical: "The same audio twice in one album. One copy can go.",
  mixed_format: "The same recording in one album in two formats.",
  same_format: "The same recording in one album twice in one format: a re-rip, or another master.",
  across_releases: "The same recording on two albums: a single that is also on the album, a compilation. Usually intended.",
};

const LEVEL_PILL: Record<DuplicateLevel, string> = {
  identical: "pill-warn",
  mixed_format: "pill-warn",
  same_format: "pill-off",
  across_releases: "pill-off",
};

const LEVELS: DuplicateLevel[] = ["identical", "mixed_format", "same_format", "across_releases"];

/** How many groups are drawn before "Show more": a list of thousands is a wall. */
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

  const report = useFetch<DuplicatesReport>(() => api.get(`/insights/duplicates${qs ? `?${qs}` : ""}`), [qs]);
  const [shown, setShown] = useState(PAGE);
  useEffect(() => setShown(PAGE), [qs]);

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
          <span>Show recordings also on another album</span>
        </label>
        {acrossReleases && (
          <label className="row" style={{ gap: 6, cursor: "pointer", fontSize: 12 }}>
            <input type="checkbox" checked={compilations} onChange={(e) => set({ compilations: e.target.checked ? "1" : null })} />
            <span>Include compilations</span>
          </label>
        )}
        {data && (
          <span className="toolbar-count dim mono">
            {data.groups.length} {data.groups.length === 1 ? "group" : "groups"}
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
        <>
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

          {data.groups.length === 0 ? (
            <EmptyState icon="◎" message="No duplicates with these filters." />
          ) : (
            <div className="stack" style={{ gap: 10 }}>
              {data.groups.slice(0, shown).map((g) => (
                <GroupCard key={`${g.recording_mb_id}:${g.files[0]?.library_id}`} group={g} showLibrary={multi} />
              ))}
              {data.groups.length > shown && (
                <button type="button" className="btn btn-ghost btn-sm" style={{ alignSelf: "center" }} onClick={() => setShown((n) => n + PAGE)}>
                  Show {Math.min(PAGE, data.groups.length - shown)} more of {data.groups.length - shown}
                </button>
              )}
            </div>
          )}
        </>
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
      <td className="mono" style={{ wordBreak: "break-all" }} title={file.path}>{file.path}</td>
      <td className="mono">{file.format}</td>
      <td className="mono tabnum" style={{ textAlign: "right", whiteSpace: "nowrap" }}>{formatBytes(file.size)}</td>
      <td>
        <Link to={`/items?mbid=${file.release_mb_id}`}>{file.release_title || file.release_mb_id}</Link>
        {file.release_type && <span className="dim"> · {file.release_type}</span>}
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
