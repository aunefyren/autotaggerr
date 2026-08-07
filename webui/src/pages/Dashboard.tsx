import { useEffect } from "react";
import { Link } from "react-router-dom";
import { api } from "../api";
import { useFetch } from "../hooks";
import { Health, ScanStatus } from "../types";
import { ErrorNote } from "../components/ui";
import { ProgressBar } from "../components/ProgressBar";
import { PHASE_LABELS, phaseDrivesProgress } from "../components/phases";

const LABELS: Record<string, string> = {
  libraries: "Libraries",
  managers: "Managers",
  data_sources: "Data sources",
  tagger_profiles: "Tagger profiles",
  library_items: "Indexed files",
};

export default function Dashboard() {
  const health = useFetch<Health>(() => api.get("/health"));
  const scan = useFetch<ScanStatus>(() => api.get("/process/status"));

  // Keep the run card live while a job is in progress.
  const running = scan.data?.running ?? false;
  useEffect(() => {
    if (!running) return;
    const t = setInterval(() => scan.reload(), 3000);
    return () => clearInterval(t);
  }, [running, scan.reload]);

  if (health.err) return <ErrorNote message={health.err} />;

  const counts = health.data?.counts ?? {};
  const order = ["libraries", "managers", "data_sources", "tagger_profiles", "library_items"];
  const noLibraries = health.data && (counts.libraries ?? 0) === 0;

  return (
    <div className="stack">
      <div className="page-head">
        <h1>Overview</h1>
        <div className="dim mono" style={{ fontSize: 12 }}>
          {health.data?.name} · {health.data?.version}
        </div>
      </div>

      {noLibraries && (
        <div className="card" style={{ borderColor: "var(--border-strong)" }}>
          <div className="eyebrow" style={{ marginBottom: 8 }}>Get started</div>
          <div className="muted" style={{ marginBottom: 12 }}>
            Add a music folder as a library, then process it to correlate and tag its files.
          </div>
          <Link to="/libraries" className="btn btn-primary btn-sm" style={{ textDecoration: "none" }}>
            Add your first library
          </Link>
        </div>
      )}

      <div className="grid-cards">
        {order.map((k) => (
          <Link key={k} to={k === "library_items" ? "/items" : `/${k.replace("_", "-")}`} className="card stat" style={{ textDecoration: "none", color: "inherit" }}>
            <div className="n">{counts[k] ?? 0}</div>
            <div className="l">{LABELS[k] ?? k}</div>
          </Link>
        ))}
      </div>

      <div className="card">
        <div className="row" style={{ justifyContent: "space-between", marginBottom: 10 }}>
          <div className="eyebrow">Latest run</div>
          <Link to="/activity" className="mono" style={{ fontSize: 12 }}>
            View activity →
          </Link>
        </div>
        {scan.data && (scan.data.finished_at || scan.data.running) ? (
          <div className="stack" style={{ gap: 12 }}>
            <div className="row" style={{ gap: 24, flexWrap: "wrap" }}>
              <ScanStat label="Processed" value={scan.data.processed} />
              <ScanStat label="Changed" value={scan.data.changed} accent="var(--accent-text)" />
              <ScanStat label="Tags written" value={scan.data.tags_written} />
              <ScanStat label="Errors" value={scan.data.errors} accent={scan.data.errors ? "var(--danger-text)" : undefined} />
              <div style={{ marginLeft: "auto" }}>
                {scan.data.running ? (
                  <span className="pill pill-scan"><span className="dot" />Running</span>
                ) : (
                  <span className="pill pill-ok"><span className="dot" />Idle</span>
                )}
              </div>
            </div>
            {/* The counters describe the walk. In the stages either side of it they are
                stale, so the bar says "working" without claiming a position — see
                phaseDrivesProgress. */}
            {scan.data.running &&
              (() => {
                const counted = phaseDrivesProgress(scan.data!.phase);
                if (counted && (scan.data!.total ?? 0) <= 0) return null;
                return (
                  <div className="row" style={{ gap: 10, alignItems: "center" }}>
                    <ProgressBar
                      done={scan.data!.done ?? 0}
                      total={scan.data!.total ?? 0}
                      width={260}
                      indeterminate={!counted}
                    />
                    <span className="mono dim" style={{ fontSize: 11 }}>
                      {counted ? `${scan.data!.done ?? 0} / ${scan.data!.total}` : PHASE_LABELS[scan.data!.phase ?? ""] ?? "Working"}
                      {scan.data!.current ? ` · ${scan.data!.current}` : ""}
                    </span>
                  </div>
                );
              })()}
          </div>
        ) : (
          <div className="muted">Nothing processed yet. Start a run from the Collection page.</div>
        )}
      </div>
    </div>
  );
}

function ScanStat({ label, value, accent }: { label: string; value: number; accent?: string }) {
  return (
    <div>
      <div className="tabnum" style={{ fontSize: 22, fontWeight: 600, color: accent }}>{value}</div>
      <div className="l">{label}</div>
    </div>
  );
}
