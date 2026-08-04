import { FormEvent, useEffect, useState } from "react";
import { useAuth } from "../auth";
import { api, errMsg } from "../api";
import { LoginProvider } from "../types";
import { Logo } from "../components/Logo";

export default function Login() {
  const { login } = useAuth();
  const [username, setUsername] = useState("");
  const [password, setPassword] = useState("");
  const [err, setErr] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);
  const [providers, setProviders] = useState<LoginProvider[]>([]);

  // A failed external login redirects back here with a message to show.
  useEffect(() => {
    const message = new URLSearchParams(window.location.search).get("error");
    if (message) {
      setErr(message);
      window.history.replaceState(null, "", window.location.pathname);
    }
  }, []);

  // Enabled external providers, if any. A failure here is not worth surfacing —
  // password login still works and is the fallback.
  useEffect(() => {
    api
      .get<LoginProvider[]>("/auth/providers")
      .then(setProviders)
      .catch(() => setProviders([]));
  }, []);

  const submit = async (e: FormEvent) => {
    e.preventDefault();
    setBusy(true);
    setErr(null);
    try {
      await login(username, password);
    } catch (e) {
      setErr(errMsg(e));
    } finally {
      setBusy(false);
    }
  };

  return (
    <div className="login-wrap">
      <form className="card login-card" onSubmit={submit}>
        <div className="brand">
          <Logo /> Autotaggerr
        </div>
        <div className="sub">Sign in to manage your library</div>
        <div className="stack">
          <div className="field">
            <label className="flabel">Username</label>
            <input className="input" value={username} onChange={(e) => setUsername(e.target.value)} autoFocus />
          </div>
          <div className="field">
            <label className="flabel">Password</label>
            <input className="input" type="password" value={password} onChange={(e) => setPassword(e.target.value)} />
          </div>
          {err && <div className="help-err">{err}</div>}
          <button className="btn btn-primary" disabled={busy} style={{ justifyContent: "center" }}>
            {busy ? "Signing in…" : "Sign in"}
          </button>

          {providers.length > 0 && (
            <>
              <div className="or-divider">or</div>
              {providers.map((p) => (
                <a
                  key={p.id}
                  className="btn btn-secondary"
                  style={{ justifyContent: "center" }}
                  /* A full page navigation, not fetch: the provider redirect is a
                     top-level browser flow and must leave the SPA. */
                  href={`/api/v1/auth/oidc/${p.id}/start`}
                >
                  Continue with {p.name}
                </a>
              ))}
            </>
          )}

          <div className="dim" style={{ fontSize: 11, textAlign: "center" }}>
            The initial admin password is printed in the server log on first start.
          </div>
        </div>
      </form>
    </div>
  );
}
