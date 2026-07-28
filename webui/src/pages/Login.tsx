import { FormEvent, useState } from "react";
import { useAuth } from "../auth";
import { errMsg } from "../api";

export default function Login() {
  const { login } = useAuth();
  const [username, setUsername] = useState("");
  const [password, setPassword] = useState("");
  const [err, setErr] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);

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
          <span className="logo">🏷</span> Autotaggerr
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
          <div className="dim" style={{ fontSize: 11, textAlign: "center" }}>
            The initial admin password is printed in the server log on first start.
          </div>
        </div>
      </form>
    </div>
  );
}
