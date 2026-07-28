import { api } from "../api";
import { useFetch } from "../hooks";
import { DataSource } from "../types";
import { EmptyState, ErrorNote, Pill } from "../components/ui";

export default function DataSources() {
  const { data, err, loading } = useFetch<DataSource[]>(() => api.get("/data-sources"));

  return (
    <div className="stack">
      <div className="page-head">
        <h1>Data sources</h1>
      </div>
      <p className="muted" style={{ margin: 0, maxWidth: "68ch" }}>
        Where metadata comes from. MusicBrainz is used by every manager to fetch the full release for tagging.
      </p>

      {err && <ErrorNote message={err} />}
      {!err && !loading && data && data.length === 0 && <EmptyState icon="⛃" message="No data sources configured." />}

      {data && data.length > 0 && (
        <div className="tablewrap">
          <table className="data">
            <thead>
              <tr><th>Name</th><th>Type</th><th>Base URL</th><th>Rate limit</th><th>State</th></tr>
            </thead>
            <tbody>
              {data.map((d) => (
                <tr key={d.id}>
                  <td style={{ color: "var(--text)" }}>{d.name}</td>
                  <td className="mono" style={{ fontSize: 12 }}>{d.type}</td>
                  <td><span className="path">{d.base_url || "—"}</span></td>
                  <td className="num">{d.rate_limit}/s</td>
                  <td>{d.enabled ? <Pill kind="ok">Enabled</Pill> : <Pill kind="off">Disabled</Pill>}</td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      )}
    </div>
  );
}
