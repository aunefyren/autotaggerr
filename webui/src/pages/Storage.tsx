import { ReactNode, useState } from "react";
import { Link } from "react-router-dom";
import { api, errMsg } from "../api";
import { useAuth } from "../auth";
import { useFetch } from "../hooks";
import { StorageApp, StorageOutcome, StorageRanked, StorageReport } from "../types";
import { ConfirmDialog, EmptyState, ErrorNote } from "../components/ui";
import { DiskRow, diskHolds, formatBytes } from "../components/storage";
import { useToast } from "../toast";

/**
 * What takes space, and where. Two scales, kept apart: the music (gigabytes to
 * terabytes, from the index as of the last scan) and Autotaggerr's own data in config/
 * (megabytes — the database by table, the artwork cache, the log). On one scale the
 * second would not be visible, and it is the one that grows without anyone noticing.
 *
 * Everyone sees the disks and the music; the breakdown of config/ and the actions that
 * change it are an admin's, as Settings is.
 */
export default function Storage() {
  const { user } = useAuth();
  const admin = user?.role === "admin";
  const [fresh, setFresh] = useState(0);
  const report = useFetch<StorageReport>(() => api.get(`/storage${fresh ? "?refresh=true" : ""}`), [fresh]);
  const data = report.data;

  return (
    <div className="stack">
      <div className="page-head">
        <h1>Storage</h1>
        <div className="row" style={{ gap: 10 }}>
          {data && (
            <span className="dim" style={{ fontSize: 12 }}>
              Measured {new Date(data.measured_at).toLocaleTimeString([], { hour: "2-digit", minute: "2-digit" })}
            </span>
          )}
          {admin && (
            <button className="btn btn-secondary btn-sm" disabled={report.loading} onClick={() => setFresh((n) => n + 1)}>
              {report.loading ? "Measuring…" : "Measure again"}
            </button>
          )}
        </div>
      </div>

      {report.err && <ErrorNote message={report.err} />}
      {!data && report.loading && <div className="muted">Measuring…</div>}

      {data && (
        <>
          <section className="card stack" style={{ gap: 16 }}>
            <h2 className="section-title">Disks</h2>
            {data.volumes.length === 0 ? (
              <span className="muted">No library is configured yet.</span>
            ) : (
              data.volumes.map((v, i) => <DiskRow key={i} volume={v} label={diskHolds(v)} />)
            )}
          </section>

          <Music report={data} />
          {data.app && <AppData app={data.app} onChanged={() => setFresh((n) => n + 1)} />}
        </>
      )}
    </div>
  );
}

function Music({ report }: { report: StorageReport }) {
  if (report.libraries.length === 0) {
    return <EmptyState icon="▤" message="No libraries yet. Add one under Libraries, and its size shows here after it is processed." />;
  }
  return (
    <>
      <section className="card stack" style={{ gap: 12 }}>
        <div>
          <h2 className="section-title">Your music</h2>
          <p className="muted storage-intro">Sizes as of each file's last scan.</p>
        </div>
        <div className="tablewrap">
          <table className="data">
            <thead>
              <tr>
                <th>Library</th>
                <th style={{ textAlign: "right" }}>Files</th>
                <th style={{ textAlign: "right" }}>Size</th>
                <th>Formats</th>
                <th style={{ textAlign: "right" }}>Unidentified</th>
              </tr>
            </thead>
            <tbody>
              {report.libraries.map((l) => (
                <tr key={l.id}>
                  <td>
                    <div className="stack" style={{ gap: 2 }}>
                      <span style={{ color: "var(--text)" }}>{l.name}</span>
                      <span className="path dim">{l.path}</span>
                    </div>
                  </td>
                  <td className="num">{l.files.toLocaleString()}</td>
                  <td className="num">{formatBytes(l.bytes)}</td>
                  <td className="dim">
                    {l.formats.length === 0
                      ? "—"
                      : l.formats.map((f) => `${f.format} ${formatBytes(f.bytes)}`).join(" · ")}
                  </td>
                  <td className="num">
                    {l.unidentified.files > 0 ? (
                      <Link to={`/items?library_id=${l.id}&status=unmatched`} title="Files no release is known for">
                        {l.unidentified.files.toLocaleString()} · {formatBytes(l.unidentified.bytes)}
                      </Link>
                    ) : (
                      <span className="dim">—</span>
                    )}
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      </section>

      <div className="storage-pair">
        <RankedList
          title="Largest artists"
          rows={report.largest_artists}
          to={(r) => `/collection/${r.mb_id}`}
          sub={(r) => `${r.files.toLocaleString()} files`}
        />
        <RankedList
          title="Largest albums"
          rows={report.largest_albums}
          to={(r) => `/collection/${r.artist_mb_id}/${r.mb_id}`}
          sub={(r) => `${r.artist ?? ""}${r.copies && r.copies > 1 ? ` · ${r.copies} copies` : ""}`}
        />
      </div>

      {report.duplicates.albums > 0 && (
        <section className="card stack" style={{ gap: 12 }}>
          <div>
            <h2 className="section-title">Albums kept more than once</h2>
            <p className="muted storage-intro">
              {report.duplicates.albums.toLocaleString()} album{report.duplicates.albums === 1 ? " is" : "s are"} on disk
              as more than one edition, or in more than one library. Beyond the largest copy of each, that is{" "}
              <strong style={{ color: "var(--text)" }}>{formatBytes(report.duplicates.extra)}</strong>. Often deliberate — a
              deluxe beside the original — but it is the space on this page you could get back.
            </p>
          </div>
          <RankedList
            rows={report.duplicates.top}
            to={(r) => `/collection/${r.artist_mb_id}/${r.mb_id}`}
            sub={(r) => `${r.artist ?? ""} · ${r.copies} copies`}
            value={(r) => r.extra ?? 0}
            valueLabel="beyond one copy"
          />
        </section>
      )}
    </>
  );
}

/**
 * A largest-first list with a bar per row, scaled to the first. The bar is the
 * comparison; the figure is the answer.
 */
function RankedList({
  title,
  rows,
  to,
  sub,
  value = (r) => r.bytes,
  valueLabel,
}: {
  title?: string;
  rows: StorageRanked[];
  to: (r: StorageRanked) => string;
  sub: (r: StorageRanked) => string;
  value?: (r: StorageRanked) => number;
  valueLabel?: string;
}) {
  const max = Math.max(1, ...rows.map(value));
  const body =
    rows.length === 0 ? (
      <span className="muted" style={{ fontSize: 12 }}>
        Nothing identified yet.
      </span>
    ) : (
      <ol className="ranked">
        {rows.map((r) => (
          <li key={r.mb_id}>
            <div className="ranked-text">
              <Link to={to(r)} className="ranked-name">
                {r.name || r.mb_id}
              </Link>
              <span className="dim ranked-sub">{sub(r)}</span>
            </div>
            <span className="ranked-value mono" title={valueLabel}>
              {formatBytes(value(r))}
            </span>
            <span className="ranked-bar" style={{ width: `${(value(r) / max) * 100}%` }} aria-hidden="true" />
          </li>
        ))}
      </ol>
    );
  if (!title) return body;
  return (
    <section className="card stack" style={{ gap: 12 }}>
      <h2 className="section-title">{title}</h2>
      {body}
    </section>
  );
}

type Action = "compact" | "checkpoint" | "prune-activity" | "artwork-leftovers";

/**
 * Autotaggerr's own data: one bar for what config/ holds, then each part with the action
 * that shrinks it beside its figure — compact where the free pages are counted, the
 * checkpoint where the write-ahead log is, removal where the leftovers are.
 */
function AppData({ app, onChanged }: { app: StorageApp; onChanged: () => void }) {
  const toast = useToast();
  const [busy, setBusy] = useState<Action | null>(null);
  const [confirming, setConfirming] = useState<Action | null>(null);
  const db = app.database;
  const dbTotal = db.file + db.wal + db.shm;
  const other = app.other.reduce((n, f) => n + f.bytes, 0);
  const parts = [
    { key: "db", label: "Database", bytes: dbTotal },
    { key: "art", label: "Artwork", bytes: app.artwork.bytes },
    { key: "log", label: "Logs", bytes: app.logs.bytes },
    { key: "other", label: "Other files", bytes: other },
  ];

  const run = async (action: Action) => {
    setBusy(action);
    try {
      const out = await api.post<StorageOutcome>(`/storage/${action}`);
      toast("ok", out.message);
      setConfirming(null);
      onChanged();
    } catch (e) {
      toast("err", errMsg(e));
    } finally {
      setBusy(null);
    }
  };

  return (
    <section className="card stack" style={{ gap: 18 }}>
      <div>
        <h2 className="section-title">Autotaggerr's data · {formatBytes(app.total)}</h2>
        <p className="muted storage-intro">
          Everything under <span className="mono">{app.dir}</span>.
        </p>
      </div>
      <div className="app-bar" role="img" aria-label={parts.map((p) => `${p.label} ${formatBytes(p.bytes)}`).join(", ")}>
        {parts.map((p) =>
          p.bytes > 0 ? <span key={p.key} className={`app-seg ${p.key}`} style={{ flexGrow: p.bytes }} title={`${p.label} ${formatBytes(p.bytes)}`} /> : null,
        )}
      </div>
      <div className="disk-legend">
        {parts.map((p) => (
          <span key={p.key}>
            <i className={`app-key ${p.key}`} /> {p.label} {formatBytes(p.bytes)}
          </span>
        ))}
      </div>

      <Part title="Database" size={dbTotal}>
        <Figures>
          <Figure label="File" value={formatBytes(db.file)} />
          <Figure
            label="Write-ahead log"
            value={formatBytes(db.wal)}
            note={db.wal > db.file ? "larger than the database itself" : undefined}
            action={
              db.wal > 0 && (
                <button className="btn btn-ghost btn-sm" disabled={busy !== null} onClick={() => run("checkpoint")}>
                  {busy === "checkpoint" ? "Shrinking…" : "Shrink"}
                </button>
              )
            }
          />
          <Figure
            label="Free inside the file"
            value={formatBytes(db.reclaimable)}
            action={
              db.reclaimable > 0 && (
                <button className="btn btn-ghost btn-sm" disabled={busy !== null} onClick={() => setConfirming("compact")}>
                  Compact
                </button>
              )
            }
          />
        </Figures>
        {db.error && <span className="help-warn">{db.error}</span>}
        {db.groups.length > 0 && (
          <div className="tablewrap storage-db">
            <table className="data">
              <thead>
                <tr>
                  <th>Holds</th>
                  <th style={{ textAlign: "right" }}>Rows</th>
                  <th style={{ textAlign: "right" }}>Size</th>
                  <th style={{ textAlign: "right" }} />
                </tr>
              </thead>
              <tbody>
                {db.groups.map((g) => (
                  <tr key={g.label}>
                    <td title={g.tables.map((t) => `${t.name}: ${formatBytes(t.bytes)}, ${t.rows.toLocaleString()} rows`).join("\n")}>
                      <span style={{ color: "var(--text)" }}>{g.label}</span>
                      <span className="dim mono" style={{ fontSize: 11 }}>
                        {" "}
                        {g.tables.map((t) => t.name).join(", ")}
                      </span>
                    </td>
                    <td className="num">{g.rows.toLocaleString()}</td>
                    <td className="num">{formatBytes(g.bytes)}</td>
                    <td style={{ textAlign: "right" }}>
                      {g.label === "Activity history" && (
                        <button className="btn btn-ghost btn-sm" disabled={busy !== null} onClick={() => setConfirming("prune-activity")}>
                          Prune now
                        </button>
                      )}
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        )}
      </Part>

      <Part title="Artwork" size={app.artwork.bytes}>
        <Figures>
          <Figure label="Images" value={app.artwork.files.toLocaleString()} />
          <Figure label="Looked for, none exists" value={app.artwork.no_image.toLocaleString()} note="remembered, so they are not asked again" />
          <Figure
            label="Leftovers"
            value={app.artwork.leftovers.files > 0 ? `${app.artwork.leftovers.files} · ${formatBytes(app.artwork.leftovers.bytes)}` : "none"}
            note="interrupted downloads, and images for lookups now recorded as having none"
            action={
              app.artwork.leftovers.files > 0 && (
                <button className="btn btn-ghost btn-sm" style={{ color: "var(--danger-text)" }} disabled={busy !== null} onClick={() => setConfirming("artwork-leftovers")}>
                  Remove
                </button>
              )
            }
          />
        </Figures>
        {app.artwork.kinds.length > 0 && (
          <ul className="storage-list">
            {app.artwork.kinds.map((k) => (
              <li key={k.label}>
                <span>{k.label}</span>
                <span className="dim">{k.files.toLocaleString()} files</span>
                <span className="mono">{formatBytes(k.bytes)}</span>
              </li>
            ))}
          </ul>
        )}
      </Part>

      <Part title="Logs" size={app.logs.bytes}>
        <ul className="storage-list">
          {app.logs.files.map((f) => (
            <li key={f.name}>
              <span className="mono">{f.name}</span>
              <span />
              <span className="mono">{formatBytes(f.bytes)}</span>
            </li>
          ))}
        </ul>
        <span className="dim" style={{ fontSize: 12 }}>
          The log is rotated past a size you set under{" "}
          <Link to="/settings/logs">Settings → Logs &amp; Activity</Link>.
        </span>
      </Part>

      {app.other.length > 0 && (
        <Part title="Other files" size={other}>
          <ul className="storage-list">
            {app.other.map((f) => (
              <li key={f.name}>
                <span className="mono">{f.name}</span>
                <span />
                <span className="mono">{formatBytes(f.bytes)}</span>
              </li>
            ))}
          </ul>
          <span className="dim" style={{ fontSize: 12 }}>
            Autotaggerr creates none of these but <span className="mono">config.json</span>. Copies of the database
            left here by hand are worth removing once they are no longer needed.
          </span>
        </Part>
      )}

      {confirming === "compact" && (
        <ConfirmDialog
          title="Compact the database"
          confirmLabel="Compact"
          busy={busy === "compact"}
          body={
            <p style={{ margin: 0 }}>
              Rewrites the database without its {formatBytes(db.reclaimable)} of free pages and gives that space back to the
              disk. It needs about {formatBytes(db.file)} free while it runs, and holds the database for that time — pages
              and jobs wait until it is done.
            </p>
          }
          onConfirm={() => run("compact")}
          onCancel={() => setConfirming(null)}
        />
      )}
      {confirming === "prune-activity" && (
        <ConfirmDialog
          title="Prune Activity now"
          confirmLabel="Prune"
          danger
          busy={busy === "prune-activity"}
          body={
            <p style={{ margin: 0 }}>
              Removes the runs your Activity retention would remove after the next run finishes, with their detail. They
              cannot be brought back. The space is reused by new rows; compact the database to return it to the disk.
            </p>
          }
          onConfirm={() => run("prune-activity")}
          onCancel={() => setConfirming(null)}
        />
      )}
      {confirming === "artwork-leftovers" && (
        <ConfirmDialog
          title="Remove leftover artwork"
          confirmLabel="Remove"
          danger
          busy={busy === "artwork-leftovers"}
          body={
            <p style={{ margin: 0 }}>
              Deletes {app.artwork.leftovers.files} files ({formatBytes(app.artwork.leftovers.bytes)}) that nothing will
              serve: interrupted downloads, and images for lookups now recorded as having none. Files younger than an hour
              are left alone, as they may still be downloading.
            </p>
          }
          onConfirm={() => run("artwork-leftovers")}
          onCancel={() => setConfirming(null)}
        />
      )}
    </section>
  );
}

function Part({ title, size, children }: { title: string; size: number; children: ReactNode }) {
  return (
    <div className="storage-part stack" style={{ gap: 10 }}>
      <h3 className="storage-part-title">
        {title} <span className="dim mono">{formatBytes(size)}</span>
      </h3>
      {children}
    </div>
  );
}

function Figures({ children }: { children: ReactNode }) {
  return <div className="storage-figures">{children}</div>;
}

function Figure({ label, value, note, action }: { label: string; value: string; note?: string; action?: ReactNode }) {
  return (
    <div className="storage-figure">
      <span className="dim storage-figure-label">{label}</span>
      <div className="row" style={{ gap: 8 }}>
        <span className="mono storage-figure-value">{value}</span>
        {action}
      </div>
      {note && <span className="dim storage-figure-note">{note}</span>}
    </div>
  );
}
