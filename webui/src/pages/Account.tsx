import { useState } from "react";
import { api, errMsg } from "../api";
import { useFetch } from "../hooks";
import { useAuth } from "../auth";
import { ConfirmDialog, ErrorNote, Note, Pill } from "../components/ui";
import { useToast } from "../toast";

/**
 * What belongs to the signed-in user rather than to the instance. Open to everyone:
 * the API keys used to live on the admin-only Settings page, which left everyone else
 * unable to make one.
 */
export default function Account() {
  const { user } = useAuth();
  return (
    <div className="stack">
      <div className="page-head">
        <h1>Account</h1>
      </div>
      <p className="muted" style={{ margin: 0, maxWidth: "72ch" }}>
        Signed in as <strong>{user?.username}</strong>
        {user?.role === "admin" ? ", an admin" : ""}. Keys made here act as you: a script or an AI assistant holding one can do
        what its scope allows, and nothing you could not.
      </p>
      <section className="card stack" style={{ gap: 14 }}>
        <h2 className="section-title">API keys</h2>
        <ApiAccess />
      </section>
    </div>
  );
}

/** One of the signed-in user's API keys, as listed — never the key itself. */
type ApiKeyRow = {
  id: string;
  name: string;
  scope: "read" | "write";
  prefix: string;
  created_at: string;
  last_used_at: string | null;
};

/**
 * The signed-in user's API keys, and everything needed to connect an assistant with one.
 * On its own page rather than under Settings → AI assistants because a key is personal:
 * the settings are admin-only, and every user needs keys of their own. The admin's
 * switch for the MCP endpoint stays on that settings page. A key is shown once, when it is made — only its hash is stored —
 * so the snippets carry the new key straight after creation and a placeholder before.
 *
 * Scope is chosen per key because it is a decision about who holds it: a read key can
 * answer questions and nothing more, which is the right default to hand an assistant.
 * Revoking is ghost-danger behind a confirmation, per the button spec: it breaks
 * whatever uses the key, at once.
 */
function ApiAccess() {
  const keys = useFetch<ApiKeyRow[]>(() => api.get("/auth/me/api-keys"));
  const [name, setName] = useState("");
  const [scope, setScope] = useState<"read" | "write">("read");
  const [created, setCreated] = useState<{ key: string; row: ApiKeyRow } | null>(null);
  const [revoking, setRevoking] = useState<ApiKeyRow | null>(null);
  const [busy, setBusy] = useState(false);
  const toast = useToast();

  const url = `${window.location.origin}/api/v1/mcp`;
  const shownKey = created?.key ?? "<your API key>";
  const claudeCode = `claude mcp add --transport http autotaggerr ${url} --header "Authorization: Bearer ${shownKey}"`;
  const json = JSON.stringify(
    { mcpServers: { autotaggerr: { type: "http", url, headers: { Authorization: `Bearer ${shownKey}` } } } },
    null,
    2,
  );

  const create = async () => {
    setBusy(true);
    try {
      const res = await api.post<{ api_key: string; key: ApiKeyRow }>("/auth/me/api-keys", { name, scope });
      setCreated({ key: res.api_key, row: res.key });
      setName("");
      keys.reload();
      toast("ok", `Key "${res.key.name}" created — copy it now, it is not shown again`);
    } catch (e) {
      toast("err", errMsg(e));
    } finally {
      setBusy(false);
    }
  };

  const revoke = async () => {
    if (!revoking) return;
    setBusy(true);
    try {
      await api.del(`/auth/me/api-keys/${revoking.id}`);
      if (created?.row.id === revoking.id) setCreated(null);
      toast("ok", `Key "${revoking.name}" revoked`);
      setRevoking(null);
      keys.reload();
    } catch (e) {
      toast("err", errMsg(e));
    } finally {
      setBusy(false);
    }
  };

  return (
    <div className="stack" style={{ gap: 12 }}>
      {keys.err && <ErrorNote message={keys.err} />}
      {keys.data && keys.data.length === 0 && (
        <span className="muted" style={{ fontSize: 12 }}>
          No keys yet. Create one to connect an assistant or a script.
        </span>
      )}
      {keys.data && keys.data.length > 0 && (
        <div className="tablewrap">
          <table className="data">
            <thead>
              <tr>
                <th>Name</th>
                <th>Scope</th>
                <th>Key</th>
                <th>Last used</th>
                <th style={{ textAlign: "right" }}>Actions</th>
              </tr>
            </thead>
            <tbody>
              {keys.data.map((k) => (
                <tr key={k.id}>
                  <td style={{ color: "var(--text)" }}>{k.name}</td>
                  <td>{k.scope === "write" ? <Pill kind="warn">Read & write</Pill> : <Pill kind="ok">Read only</Pill>}</td>
                  <td className="mono">{k.prefix}…</td>
                  <td>{k.last_used_at ? new Date(k.last_used_at).toLocaleString() : "never"}</td>
                  <td style={{ textAlign: "right" }}>
                    <button
                      className="btn btn-ghost btn-sm"
                      style={{ color: "var(--danger-text)" }}
                      onClick={() => setRevoking(k)}
                      disabled={busy}
                    >
                      Revoke
                    </button>
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      )}

      <div className="row" style={{ gap: 8, alignItems: "center", flexWrap: "wrap" }}>
        <input
          className="input"
          placeholder="Name, e.g. Claude"
          value={name}
          onChange={(e) => setName(e.target.value)}
          aria-label="New key name"
          style={{ maxWidth: 220 }}
        />
        <select
          className="select"
          value={scope}
          onChange={(e) => setScope(e.target.value as "read" | "write")}
          aria-label="New key scope"
          style={{ width: "auto" }}
        >
          <option value="read">Read only</option>
          <option value="write">Read & write</option>
        </select>
        <button className="btn btn-secondary btn-sm" onClick={create} disabled={busy || name.trim() === ""}>
          Create key
        </button>
        <span className="muted" style={{ fontSize: 12 }}>
          {scope === "write"
            ? "Can start runs, change wants and write tags — the same as you."
            : "Can read everything and change nothing."}
        </span>
      </div>

      {created && (
        <Note kind="warn" title={`Copy "${created.row.name}" now`}>
          This is the only time the key is shown. The snippets below include it.
        </Note>
      )}
      {created && <Snippet label="API key" value={created.key} />}
      <Snippet label="MCP endpoint" value={url} />
      <Snippet label="Claude Code" value={claudeCode} />
      <Snippet label="Client config (JSON)" value={json} />

      {revoking && (
        <ConfirmDialog
          title={`Revoke "${revoking.name}"?`}
          body={
            <p style={{ margin: 0 }}>
              The key stops working immediately — for every assistant, script or tool that uses it. This cannot be
              undone; a new key has to be created and handed out instead.
            </p>
          }
          confirmLabel="Revoke key"
          danger
          busy={busy}
          onConfirm={revoke}
          onCancel={() => setRevoking(null)}
        />
      )}
    </div>
  );
}

/** A read-only value to copy: identifiers and commands, so it is set in mono. */
function Snippet({ label, value }: { label: string; value: string }) {
  const toast = useToast();
  const copy = async () => {
    try {
      await navigator.clipboard.writeText(value);
      toast("ok", `${label} copied`);
    } catch {
      toast("err", "Could not copy — select the text instead");
    }
  };
  return (
    <div className="stack" style={{ gap: 4 }}>
      <div className="row" style={{ gap: 8, alignItems: "center" }}>
        <span className="setting-name">{label}</span>
        <button className="btn btn-ghost btn-sm" onClick={copy}>
          Copy
        </button>
      </div>
      <div className="setting-readonly mono" style={{ whiteSpace: "pre-wrap" }}>
        {value}
      </div>
    </div>
  );
}
