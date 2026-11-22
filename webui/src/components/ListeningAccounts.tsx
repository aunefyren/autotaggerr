import { useEffect, useRef, useState } from "react";
import { api, errMsg } from "../api";
import { useFetch } from "../hooks";
import { useAuth } from "../auth";
import { ConfirmDialog, ErrorNote, Pill } from "./ui";
import { useToast } from "../toast";
import type { ListeningAccount } from "../types";

type MediaServerRow = { id: string; name: string; enabled: boolean };
type PlexAccount = { id: string; name: string };

/**
 * The signed-in user's listening accounts: where the plays on Insights → Graph come
 * from. Personal, like API keys: nobody else's accounts are listed, admins included.
 *
 * Plex is linked by signing in at plex.tv, which proves which account on a shared
 * server is yours; an admin may also pick an account by hand, since they hold the
 * server's token anyway. ListenBrainz needs only a user name (and a token for private
 * listens).
 */
export default function ListeningAccounts() {
  const { user } = useAuth();
  const isAdmin = user?.role === "admin";
  const accounts = useFetch<ListeningAccount[]>(() => api.get("/listening/accounts"));
  const [lbName, setLbName] = useState("");
  const [lbToken, setLbToken] = useState("");
  const [busy, setBusy] = useState(false);
  const [removing, setRemoving] = useState<ListeningAccount | null>(null);
  const [pinWaiting, setPinWaiting] = useState(false);
  const pinTimer = useRef<number | null>(null);
  const toast = useToast();

  useEffect(() => () => {
    if (pinTimer.current) window.clearInterval(pinTimer.current);
  }, []);

  const run = async (fn: () => Promise<void>) => {
    setBusy(true);
    try {
      await fn();
    } catch (e) {
      toast("err", errMsg(e));
    } finally {
      setBusy(false);
    }
  };

  const addListenBrainz = () =>
    run(async () => {
      await api.post("/listening/accounts", { source: "listenbrainz", username: lbName, token: lbToken || undefined });
      setLbName("");
      setLbToken("");
      accounts.reload();
      toast("ok", "ListenBrainz linked — reading its history now");
      await api.post("/listening/sync");
    });

  const signInWithPlex = () =>
    run(async () => {
      // The window opens before the request so a popup blocker sees a click.
      const win = window.open("about:blank", "_blank");
      const pin = await api.post<{ id: number; auth_url: string }>("/listening/plex/pin");
      if (win) win.location.href = pin.auth_url;
      else window.open(pin.auth_url, "_blank");
      setPinWaiting(true);
      let tries = 0;
      pinTimer.current = window.setInterval(async () => {
        tries++;
        try {
          const res = await api.post<{ pending?: boolean; servers?: { server: string; linked: boolean; error?: string }[] }>(
            `/listening/plex/pin/${pin.id}/claim`,
          );
          if (res.pending && tries < 150) return;
          window.clearInterval(pinTimer.current!);
          setPinWaiting(false);
          if (res.pending) {
            toast("err", "The Plex sign-in timed out");
            return;
          }
          const linked = (res.servers ?? []).filter((s) => s.linked).map((s) => s.server);
          if (linked.length === 0) toast("err", "That Plex account has no access to any server set up here");
          else toast("ok", `Linked on ${linked.join(", ")} — reading its history now`);
          accounts.reload();
          await api.post("/listening/sync");
        } catch (e) {
          window.clearInterval(pinTimer.current!);
          setPinWaiting(false);
          toast("err", errMsg(e));
        }
      }, 2000);
    });

  const toggle = (a: ListeningAccount) =>
    run(async () => {
      await api.put(`/listening/accounts/${a.id}`, { enabled: !a.enabled });
      accounts.reload();
    });

  const test = (a: ListeningAccount) =>
    run(async () => {
      const res = await api.post<{ healthy: boolean; error?: string }>(`/listening/accounts/${a.id}/test`);
      toast(res.healthy ? "ok" : "err", res.healthy ? "Readable" : (res.error ?? "Not readable"));
    });

  const syncNow = () =>
    run(async () => {
      await api.post("/listening/sync");
      toast("ok", "Reading new plays — refresh in a moment to see the counts");
      window.setTimeout(accounts.reload, 4000);
    });

  const remove = () =>
    run(async () => {
      if (!removing) return;
      await api.del(`/listening/accounts/${removing.id}`);
      setRemoving(null);
      accounts.reload();
      toast("ok", "Account unlinked and its plays forgotten");
    });

  const rows = accounts.data ?? [];
  return (
    <div className="stack" style={{ gap: 12 }}>
      {accounts.err && <ErrorNote message={accounts.err} />}
      {accounts.data && rows.length === 0 && (
        <span className="muted" style={{ fontSize: 12 }}>
          Nothing linked yet. Link Plex or ListenBrainz to see what you play on Insights → Graph.
        </span>
      )}
      {rows.length > 0 && (
        <div className="tablewrap">
          <table className="data">
            <thead>
              <tr>
                <th>Source</th>
                <th>Account</th>
                <th style={{ textAlign: "right" }}>Plays</th>
                <th style={{ textAlign: "right" }}>Identified</th>
                <th>Last read</th>
                <th style={{ textAlign: "right" }}>Actions</th>
              </tr>
            </thead>
            <tbody>
              {rows.map((a) => (
                <tr key={a.id}>
                  <td>{a.source === "plex" ? "Plex" : "ListenBrainz"}</td>
                  <td style={{ color: "var(--text)" }}>
                    {a.source === "plex" ? `${a.plex_account_name || `account ${a.plex_account_id}`} on ${a.server_name ?? "a removed server"}` : a.username}
                    {!a.enabled && (
                      <>
                        {" "}
                        <Pill kind="off">Off</Pill>
                      </>
                    )}
                  </td>
                  <td className="mono" style={{ textAlign: "right" }}>
                    {a.plays.toLocaleString()}
                  </td>
                  <td className="mono" style={{ textAlign: "right" }}>
                    {a.plays > 0 ? `${Math.round((100 * a.matched) / a.plays)}%` : "—"}
                  </td>
                  <td>
                    {a.last_error ? (
                      <span style={{ color: "var(--danger-text)" }} title={a.last_error}>
                        Failed
                      </span>
                    ) : a.last_synced_at ? (
                      new Date(a.last_synced_at).toLocaleString()
                    ) : (
                      "not yet"
                    )}
                  </td>
                  <td style={{ textAlign: "right", whiteSpace: "nowrap" }}>
                    <button className="btn btn-ghost btn-sm" onClick={() => test(a)} disabled={busy}>
                      Test
                    </button>
                    <button className="btn btn-ghost btn-sm" onClick={() => toggle(a)} disabled={busy}>
                      {a.enabled ? "Turn off" : "Turn on"}
                    </button>
                    <button
                      className="btn btn-ghost btn-sm"
                      style={{ color: "var(--danger-text)" }}
                      onClick={() => setRemoving(a)}
                      disabled={busy}
                    >
                      Unlink
                    </button>
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      )}

      <div className="row" style={{ gap: 8, alignItems: "center", flexWrap: "wrap" }}>
        <button className="btn btn-secondary btn-sm" onClick={signInWithPlex} disabled={busy || pinWaiting}>
          {pinWaiting ? "Waiting for Plex…" : "Sign in with Plex"}
        </button>
        {rows.length > 0 && (
          <button className="btn btn-ghost btn-sm" onClick={syncNow} disabled={busy}>
            Read new plays now
          </button>
        )}
        <span className="muted" style={{ fontSize: 12 }}>
          Links your account on every Plex server set up here that it can reach.
        </span>
      </div>

      <div className="row" style={{ gap: 8, alignItems: "center", flexWrap: "wrap" }}>
        <input
          className="input"
          placeholder="ListenBrainz user name"
          value={lbName}
          onChange={(e) => setLbName(e.target.value)}
          aria-label="ListenBrainz user name"
          style={{ maxWidth: 220 }}
        />
        <input
          className="input"
          type="password"
          placeholder="Token (optional)"
          value={lbToken}
          onChange={(e) => setLbToken(e.target.value)}
          aria-label="ListenBrainz token"
          style={{ maxWidth: 220 }}
        />
        <button className="btn btn-secondary btn-sm" onClick={addListenBrainz} disabled={busy || lbName.trim() === ""}>
          Link ListenBrainz
        </button>
        <span className="muted" style={{ fontSize: 12 }}>
          A token is only needed when your listens are private.
        </span>
      </div>

      {isAdmin && <AdminPlexPicker onLinked={accounts.reload} />}

      {removing && (
        <ConfirmDialog
          title="Unlink this account?"
          body={
            <p style={{ margin: 0 }}>
              Every play read from it is forgotten here. Linking it again reads its whole history again.
            </p>
          }
          confirmLabel="Unlink"
          danger
          busy={busy}
          onConfirm={remove}
          onCancel={() => setRemoving(null)}
        />
      )}
    </div>
  );
}

/** An admin picks any account on a server by hand: they hold its token already. */
function AdminPlexPicker({ onLinked }: { onLinked: () => void }) {
  const servers = useFetch<MediaServerRow[]>(() => api.get("/media-servers"));
  const [server, setServer] = useState("");
  const [accounts, setAccounts] = useState<PlexAccount[]>([]);
  const [account, setAccount] = useState("");
  const toast = useToast();

  useEffect(() => {
    setAccounts([]);
    setAccount("");
    if (!server) return;
    api
      .get<{ accounts: PlexAccount[]; error?: string }>(`/media-servers/${server}/accounts`)
      .then((r) => {
        setAccounts(r.accounts);
        if (r.error) toast("err", r.error);
      })
      .catch((e) => toast("err", errMsg(e)));
  }, [server, toast]);

  const enabled = (servers.data ?? []).filter((s) => s.enabled);
  if (enabled.length === 0) return null;
  const link = async () => {
    const picked = accounts.find((a) => a.id === account);
    try {
      await api.post("/listening/accounts", {
        source: "plex",
        media_server_id: server,
        plex_account_id: account,
        plex_account_name: picked?.name,
      });
      toast("ok", "Plex account linked — reading its history now");
      onLinked();
      await api.post("/listening/sync");
    } catch (e) {
      toast("err", errMsg(e));
    }
  };
  return (
    <div className="row" style={{ gap: 8, alignItems: "center", flexWrap: "wrap" }}>
      <select className="select" value={server} onChange={(e) => setServer(e.target.value)} aria-label="Plex server" style={{ width: "auto" }}>
        <option value="">Plex server…</option>
        {enabled.map((s) => (
          <option key={s.id} value={s.id}>
            {s.name}
          </option>
        ))}
      </select>
      <select
        className="select"
        value={account}
        onChange={(e) => setAccount(e.target.value)}
        aria-label="Plex account"
        style={{ width: "auto" }}
        disabled={accounts.length === 0}
      >
        <option value="">Account…</option>
        {accounts.map((a) => (
          <option key={a.id} value={a.id}>
            {a.name || `account ${a.id}`}
          </option>
        ))}
      </select>
      <button className="btn btn-ghost btn-sm" onClick={link} disabled={!server || !account}>
        Link without signing in
      </button>
      <span className="muted" style={{ fontSize: 12 }}>
        Admins only. Link the account that is yours: its plays become your history.
      </span>
    </div>
  );
}
