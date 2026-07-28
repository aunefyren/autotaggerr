import { FormEvent, useState } from "react";
import { api, errMsg } from "../api";
import { useFetch } from "../hooks";
import { AuthProvider } from "../types";
import { EmptyState, ErrorNote, Modal, Pill } from "../components/ui";
import { useToast } from "../toast";

export default function AuthProviders() {
  const toast = useToast();
  const { data, err, loading, reload } = useFetch<AuthProvider[]>(() => api.get("/auth-providers"));
  const [creating, setCreating] = useState(false);
  const [editing, setEditing] = useState<AuthProvider | null>(null);

  const remove = async (p: AuthProvider) => {
    if (!confirm(`Remove login provider "${p.name}"? Users linked to it will only be able to sign in with a password.`)) return;
    try {
      await api.del(`/auth-providers/${p.id}`);
      toast("ok", `Removed ${p.name}`);
      reload();
    } catch (e) {
      toast("err", errMsg(e));
    }
  };

  return (
    <div className="stack">
      <div className="page-head">
        <h1>Login providers</h1>
        <button className="btn btn-primary btn-sm" onClick={() => setCreating(true)}>Add provider</button>
      </div>
      <p className="muted" style={{ margin: 0, maxWidth: "68ch" }}>
        OpenID Connect sign-in, shown alongside password login. Password login always stays
        available, so a misconfigured provider can never lock you out.
      </p>

      {err && <ErrorNote message={err} />}
      {!err && !loading && data && data.length === 0 && (
        <EmptyState icon="⚿" message="No external login providers configured." />
      )}

      {data && data.length > 0 && (
        <div className="tablewrap">
          <table className="data">
            <thead>
              <tr><th>Name</th><th>Issuer</th><th>Signup</th><th>State</th><th style={{ textAlign: "right" }}>Actions</th></tr>
            </thead>
            <tbody>
              {data.map((p) => (
                <tr key={p.id}>
                  <td style={{ color: "var(--text)" }}>{p.name}</td>
                  <td><span className="path">{p.issuer || "—"}</span></td>
                  <td>{p.allow_signup ? <Pill kind="chg">Auto-create</Pill> : <span className="dim">Linked only</span>}</td>
                  <td>{p.enabled ? <Pill kind="ok">Enabled</Pill> : <Pill kind="off">Disabled</Pill>}</td>
                  <td>
                    <div className="row" style={{ justifyContent: "flex-end" }}>
                      <button className="btn btn-secondary btn-sm" onClick={() => setEditing(p)}>Edit</button>
                      <button className="btn btn-ghost btn-sm" onClick={() => remove(p)} style={{ color: "var(--danger-text)" }}>Remove</button>
                    </div>
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      )}

      {(creating || editing) && (
        <ProviderForm
          provider={editing}
          onClose={() => { setCreating(false); setEditing(null); }}
          onSaved={() => {
            toast("ok", editing ? "Provider saved" : "Provider added");
            setCreating(false);
            setEditing(null);
            reload();
          }}
        />
      )}
    </div>
  );
}

/**
 * One form for create and edit. The only difference that matters is the client
 * secret: on edit it is never sent back to the browser, so an empty field means
 * "keep the stored one" rather than "clear it".
 */
function ProviderForm({
  provider,
  onClose,
  onSaved,
}: {
  provider: AuthProvider | null;
  onClose: () => void;
  onSaved: () => void;
}) {
  const toast = useToast();
  const editing = provider !== null;
  const [name, setName] = useState(provider?.name ?? "");
  const [issuer, setIssuer] = useState(provider?.issuer ?? "");
  const [clientId, setClientId] = useState(provider?.client_id ?? "");
  const [clientSecret, setClientSecret] = useState("");
  const [scopes, setScopes] = useState(provider?.scopes ?? "");
  const [redirectUrl, setRedirectUrl] = useState(provider?.redirect_url ?? "");
  const [allowSignup, setAllowSignup] = useState(provider?.allow_signup ?? false);
  const [enabled, setEnabled] = useState(provider?.enabled ?? true);
  const [busy, setBusy] = useState(false);

  const submit = async (e: FormEvent) => {
    e.preventDefault();
    setBusy(true);
    try {
      const body: Record<string, unknown> = {
        name,
        type: "oidc",
        issuer,
        client_id: clientId,
        scopes,
        redirect_url: redirectUrl,
        allow_signup: allowSignup,
        enabled,
      };
      if (clientSecret) body.client_secret = clientSecret;

      if (editing) await api.put(`/auth-providers/${provider.id}`, body);
      else await api.post("/auth-providers", body);
      onSaved();
    } catch (e) {
      toast("err", errMsg(e));
      setBusy(false);
    }
  };

  // Shown so the value can be pasted into the provider's allowed-callback list.
  const defaultCallback = `${window.location.origin}/api/v1/auth/oidc/${provider?.id ?? "<id>"}/callback`;

  return (
    <Modal title={editing ? `Edit ${provider.name}` : "Add login provider"} onClose={onClose}>
      <form onSubmit={submit} className="stack">
        <div className="field">
          <label className="flabel">Display name</label>
          <input className="input" value={name} onChange={(e) => setName(e.target.value)} placeholder="Authentik" autoFocus />
          <div className="dim" style={{ fontSize: 11 }}>Shown on the button: “Continue with {name || "…"}”.</div>
        </div>
        <div className="field">
          <label className="flabel">Issuer URL</label>
          <input className="input mono" value={issuer} onChange={(e) => setIssuer(e.target.value)} placeholder="https://id.example.com/application/o/autotaggerr" />
          <div className="dim" style={{ fontSize: 11 }}>
            The base URL, without <span className="mono">/.well-known/openid-configuration</span>.
          </div>
        </div>
        <div className="field">
          <label className="flabel">Client ID</label>
          <input className="input mono" value={clientId} onChange={(e) => setClientId(e.target.value)} />
        </div>
        <div className="field">
          <label className="flabel">Client secret</label>
          <input
            className="input mono"
            type="password"
            value={clientSecret}
            onChange={(e) => setClientSecret(e.target.value)}
            placeholder={editing ? "leave blank to keep the current secret" : ""}
          />
        </div>
        <div className="field">
          <label className="flabel">Scopes</label>
          <input className="input mono" value={scopes} onChange={(e) => setScopes(e.target.value)} placeholder="openid profile email" />
        </div>
        <div className="field">
          <label className="flabel">Redirect URL override</label>
          <input className="input mono" value={redirectUrl} onChange={(e) => setRedirectUrl(e.target.value)} placeholder={defaultCallback} />
          <div className="dim" style={{ fontSize: 11 }}>
            Leave blank to derive it from the request. Register this callback with your provider.
          </div>
        </div>

        <label className="row" style={{ gap: 8, cursor: "pointer" }}>
          <input type="checkbox" checked={allowSignup} onChange={(e) => setAllowSignup(e.target.checked)} />
          <span>Create an account on first sign-in</span>
        </label>
        <div className="dim" style={{ fontSize: 11, marginTop: -6 }}>
          Off: only existing users can sign in, matched by a verified email address. On: anyone your
          provider lets through gets an account here.
        </div>

        <label className="row" style={{ gap: 8, cursor: "pointer" }}>
          <input type="checkbox" checked={enabled} onChange={(e) => setEnabled(e.target.checked)} />
          <span>Enabled</span>
        </label>

        <div className="modal-actions">
          <button type="button" className="btn btn-ghost btn-sm" onClick={onClose}>Cancel</button>
          <button className="btn btn-primary btn-sm" disabled={busy || !name || !issuer || !clientId || (!editing && !clientSecret)}>
            {busy ? "Saving…" : editing ? "Save changes" : "Add provider"}
          </button>
        </div>
      </form>
    </Modal>
  );
}
