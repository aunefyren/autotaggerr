import { useState } from "react";
import { api, errMsg } from "../api";
import { useFetch } from "../hooks";
import { MusicbrainzMigration, MigrationList, MigrationPolicy } from "../types";
import { EmptyState, ErrorNote, IdChip, Pill } from "../components/ui";
import { MBLink } from "../components/MBLink";
import { useToast } from "../toast";

/**
 * MusicBrainz identity changes and what was done about them.
 *
 * This is a review table in the sense the style guide means: for anything held by
 * policy, the table *is* the confirmation — approving from a row applies it, with no
 * second dialog asking whether you meant it. Applied and dismissed rows stay
 * visible below, because "what did it decide while I was not looking" is the
 * question this page exists to answer.
 */

const ENTITY_LABELS: Record<string, string> = {
  release: "Release",
  artist: "Artist",
};

function StatusPill({ status }: { status: string }) {
  if (status === "pending") return <Pill kind="scan">Awaiting review</Pill>;
  if (status === "applied") return <Pill kind="ok">Applied</Pill>;
  if (status === "failed") return <Pill kind="err">Failed</Pill>;
  return <Pill kind="off">Dismissed</Pill>;
}

/** What approving this row would change, in words rather than raw counts. */
function Impact({ m }: { m: MusicbrainzMigration }) {
  const parts: string[] = [];
  if (m.affected_files > 0) {
    parts.push(`${m.affected_files} file${m.affected_files === 1 ? "" : "s"}`);
  }
  if (m.affected_desires > 0) {
    parts.push(`${m.affected_desires} want${m.affected_desires === 1 ? "" : "s"}`);
  }
  if (parts.length === 0) return <span className="dim">—</span>;

  return (
    <span>
      {parts.join(" · ")}
      {m.touches_pinned && (
        <span title="Includes a file you attached by hand; the manual choice will be re-pointed, not discarded">
          {" "}
          📌
        </span>
      )}
    </span>
  );
}

export default function Migrations() {
  const toast = useToast();
  const list = useFetch<MigrationList>(() => api.get("/migrations"));
  const policy = useFetch<MigrationPolicy>(() => api.get("/migrations/policy"));
  const [busy, setBusy] = useState<string | null>(null);
  const [verifying, setVerifying] = useState(false);

  // Finding a merge is not an activity of its own — it is what a metadata refresh
  // notices while fetching. So this is the ordinary refresh verb with the cache
  // ignored, not a fourth thing with its own name. It runs for as long as the
  // collection is large, so this only reports that it started; the Metadata and
  // Activity pages are where it is watched.
  const verify = async () => {
    setVerifying(true);
    try {
      await api.post("/migrations/verify");
      toast("info", "Metadata refresh started — follow it on the Metadata page");
    } catch (e) {
      toast("err", errMsg(e));
    } finally {
      setVerifying(false);
    }
  };

  const act = (m: MusicbrainzMigration, what: "approve" | "dismiss") => async () => {
    setBusy(m.id);
    try {
      await api.post(`/migrations/${m.id}/${what}`);
      toast("info", what === "approve" ? "Migration applied" : "Migration dismissed");
      list.reload();
    } catch (e) {
      toast("err", errMsg(e));
    } finally {
      setBusy(null);
    }
  };

  const rows = list.data?.migrations ?? [];
  const pending = rows.filter((m) => m.status === "pending");
  const history = rows.filter((m) => m.status !== "pending");
  const p = policy.data;
  const holding = p
    ? [
        p.review_releases && "release merges",
        p.review_artists && "artist merges",
        p.review_deletions && "deletions",
        p.review_pinned && "anything touching a manual attachment",
      ].filter(Boolean)
    : [];

  return (
    <div className="stack">
      <div className="page-head">
        <h1>MusicBrainz migrations</h1>
        <div className="row">
          {/* Ghost, not secondary: the button beside it is hours of rate-limited
              work, and giving a free list reload the same visual weight invites
              pressing the wrong one. */}
          <button
            className="btn btn-ghost btn-sm"
            onClick={() => list.reload()}
            title="Re-read this list. Does not contact MusicBrainz."
          >
            Reload
          </button>
          <button
            className="btn btn-secondary btn-sm"
            disabled={verifying}
            onClick={verify}
            title="Refresh every artist, release-group and release in the collection, ignoring cached copies — which is how merges and deletions are found. One rate-limited request each, so a large collection takes hours; watch it on the Metadata page. Reads only: no files are written, and anything found is queued under your approval policy."
          >
            {verifying ? "Refreshing…" : "Refresh metadata"}
          </button>
        </div>
      </div>

      <p className="dim" style={{ fontSize: 12, maxWidth: "68ch" }}>
        MusicBrainz merges and deletes entities over time. Autotaggerr notices when an ID it
        stores has moved — the service answers for the old ID but returns a different one — and
        re-points its own records to follow. This is noticed by any metadata refresh, so the
        nightly one keeps this list current on its own; the button above only forces the issue by
        ignoring cached copies. Changes are applied as they are found unless a category is held
        for review.
        {holding.length > 0
          ? ` Currently held for review: ${holding.join(", ")}.`
          : " Nothing is currently held for review."}
      </p>

      {list.err && <ErrorNote message={list.err} />}

      {!list.err && !list.loading && rows.length === 0 && (
        <EmptyState
          icon="⇄"
          message="No MusicBrainz identity changes have been detected. This is the normal state — merges are found during scans and metadata syncs."
        />
      )}

      {pending.length > 0 && (
        <div className="stack">
          <h2 style={{ fontSize: 14 }}>Awaiting review ({pending.length})</h2>
          <MigrationTable rows={pending} busy={busy} act={act} />
        </div>
      )}

      {history.length > 0 && (
        <div className="stack">
          <h2 style={{ fontSize: 14 }}>History</h2>
          <MigrationTable rows={history} busy={busy} act={act} />
        </div>
      )}
    </div>
  );
}

function MigrationTable({
  rows,
  busy,
  act,
}: {
  rows: MusicbrainzMigration[];
  busy: string | null;
  act: (m: MusicbrainzMigration, what: "approve" | "dismiss") => () => Promise<void>;
}) {
  return (
    <div className="tablewrap">
      <table className="data">
        <thead>
          <tr>
            <th>What</th>
            <th>Change</th>
            <th>Affects</th>
            <th>Status</th>
            <th style={{ textAlign: "right" }}>Actions</th>
          </tr>
        </thead>
        <tbody>
          {rows.map((m) => (
            <tr key={m.id}>
              <td>
                <div className="stack" style={{ gap: 2 }}>
                  <span>{m.name || ENTITY_LABELS[m.entity_type] || m.entity_type}</span>
                  <span className="dim" style={{ fontSize: 11 }}>
                    {ENTITY_LABELS[m.entity_type] ?? m.entity_type}
                    {m.kind === "deleted" ? " · deleted upstream" : " · merged"}
                  </span>
                </div>
              </td>
              <td>
                <div className="row" style={{ gap: 6, flexWrap: "wrap" }}>
                  <IdChip value={m.old_mb_id} />
                  {m.kind === "deleted" ? (
                    <span className="dim" style={{ fontSize: 11 }}>→ gone</span>
                  ) : (
                    <>
                      <span className="dim" style={{ fontSize: 11 }}>→</span>
                      <IdChip value={m.new_mb_id} />
                      <MBLink
                        entity={m.entity_type === "artist" ? "artist" : "release"}
                        mbid={m.new_mb_id}
                      />
                    </>
                  )}
                </div>
              </td>
              <td><Impact m={m} /></td>
              <td>
                <StatusPill status={m.status} />
                {m.error && (
                  <div className="dim" style={{ fontSize: 11 }} title={m.error}>
                    {m.error}
                  </div>
                )}
              </td>
              <td>
                <div className="row" style={{ justifyContent: "flex-end" }}>
                  {m.status === "pending" || m.status === "failed" ? (
                    <>
                      <button
                        className="btn btn-ghost btn-sm"
                        disabled={busy === m.id}
                        onClick={act(m, "dismiss")}
                        title="Leave Autotaggerr's records on the old ID. The change is remembered so it will not be raised again."
                      >
                        Dismiss
                      </button>
                      <button
                        className="btn btn-primary btn-sm"
                        disabled={busy === m.id}
                        onClick={act(m, "approve")}
                      >
                        {busy === m.id ? "Applying…" : "Apply"}
                      </button>
                    </>
                  ) : (
                    <span className="dim mono" style={{ fontSize: 11 }}>
                      {m.applied_at ? new Date(m.applied_at).toLocaleString() : "—"}
                    </span>
                  )}
                </div>
              </td>
            </tr>
          ))}
        </tbody>
      </table>
    </div>
  );
}
