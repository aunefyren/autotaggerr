import { useEffect, useState } from "react";
import { useSearchParams } from "react-router-dom";
import { api, errMsg } from "../api";
import { useFetch } from "../hooks";
import { ItemsPage, Library, LibraryItem } from "../types";
import { ConfirmDialog, EmptyState, ErrorNote, IdChip, Pill, StatusPill } from "../components/ui";
import { useToast } from "../toast";
import { ItemDiffModal } from "../components/ItemDiff";
import { AttachModal } from "../components/AttachModal";
import { BulkAttachModal } from "../components/BulkAttachModal";
import { MBLink } from "../components/MBLink";

const PAGE = 50;

/** The directory holding a file — the unit an album is actually attached in. */
function folderOf(path: string): string {
  const parts = path.split(/[\\/]/);
  parts.pop();
  return parts.join("/");
}

/**
 * The status filter's options, and the query each one asks. Failures are split by
 * whether they clear on their own: during an outage every file in a run fails at once,
 * and one "Error" option showed that as a library full of broken files.
 *
 * A held pin is its own filter, not a status: the row's status says what the last
 * attempt did, and what a held pin needs is a decision.
 */
const STATUS_FILTERS: Record<string, { label: string; query: Record<string, string> }> = {
  ok: { label: "Tagged", query: { status: "ok" } },
  error: { label: "Error · needs a look", query: { status: "error", transient: "false" } },
  retrying: { label: "Error · will retry", query: { status: "error", transient: "true" } },
  unmatched: { label: "Unmatched", query: { status: "unmatched" } },
  unmatched_wait: { label: "Unmatched · waiting", query: { status: "unmatched", transient: "true" } },
  pin_held: { label: "Pin needs review", query: { pin_held: "true" } },
};

export default function Items() {
  const libs = useFetch<Library[]>(() => api.get("/libraries"));
  // "Show me the files behind this identifier", asked by a link from somewhere that
  // knows an MBID — a migration row, an activity detail. It lives in the URL rather
  // than in a filter control because it is a destination, not something you would ever
  // type here: any of the three kinds resolves, and the server does the resolving.
  const [params, setParams] = useSearchParams();
  const mbid = params.get("mbid") ?? "";
  const [libraryId, setLibraryId] = useState("");
  // Seeded from the URL so the dashboard's counts can link straight to their rows.
  const [status, setStatus] = useState(() => {
    const s = params.get("status") ?? "";
    return s in STATUS_FILTERS ? s : "";
  });
  const [q, setQ] = useState("");
  const [offset, setOffset] = useState(0);
  const [selected, setSelected] = useState<LibraryItem | null>(null);
  const [attaching, setAttaching] = useState<LibraryItem | null>(null);
  const [picked, setPicked] = useState<string[]>([]);
  const [bulk, setBulk] = useState(false);
  const [releasing, setReleasing] = useState<LibraryItem | null>(null);
  const [busy, setBusy] = useState("");
  const toast = useToast();

  const query = new URLSearchParams();
  if (libraryId) query.set("library_id", libraryId);
  for (const [k, v] of Object.entries(STATUS_FILTERS[status]?.query ?? {})) query.set(k, v);
  if (q) query.set("q", q);
  if (mbid) query.set("mbid", mbid);
  query.set("limit", String(PAGE));
  query.set("offset", String(offset));

  const page = useFetch<ItemsPage>(() => api.get(`/library-items?${query.toString()}`), [libraryId, status, q, mbid, offset]);

  const resetAnd = (fn: () => void) => {
    setOffset(0);
    fn();
  };

  const total = page.data?.total ?? 0;
  const items = page.data?.items ?? [];

  // Selection is dropped whenever the view changes: a bulk attach writes tags to
  // every picked file, so it must never include files the user can no longer see.
  useEffect(() => setPicked([]), [libraryId, status, q, offset]);

  const toggle = (id: string) =>
    setPicked((current) => (current.includes(id) ? current.filter((x) => x !== id) : [...current, id]));

  // Bulk attach writes an identity, so it can only ever include files whose identity
  // is the user's to set. Select-all therefore means "every file here that can be
  // attached", not "every row" — otherwise the count promises work the API rejects.
  const attachable = items.filter((it) => it.identity_editable);
  const allPicked = attachable.length > 0 && picked.length === attachable.length;
  const pickedItems = items.filter((it) => picked.includes(it.id));

  // Keeping a held pin confirms the hand-chosen identity for the audio the file holds
  // now, and writes it — the same outcome as attaching again, without re-picking.
  const keepPin = async (it: LibraryItem) => {
    setBusy(it.id);
    try {
      const res = await api.post<{ tags_written: number; warning?: string }>(`/library-items/${it.id}/pin/confirm`);
      toast(res.warning ? "err" : "info", res.warning ?? `Pin kept · ${res.tags_written} tags written`);
      page.reload();
    } catch (e) {
      toast("err", errMsg(e));
    } finally {
      setBusy("");
    }
  };

  const releasePin = async (it: LibraryItem) => {
    setBusy(it.id);
    try {
      await api.del(`/library-items/${it.id}/attach`);
      toast("info", "Pin released · the next processing run identifies the file");
      setReleasing(null);
      page.reload();
    } catch (e) {
      toast("err", errMsg(e));
    } finally {
      setBusy("");
    }
  };

  return (
    <div className="stack">
      <div className="page-head">
        <h1>Items</h1>
        <div className="dim mono" style={{ fontSize: 12 }}>{total} indexed</div>
      </div>

      <div className="row" style={{ flexWrap: "wrap", gap: 8 }}>
        <select className="select" style={{ width: 200 }} value={libraryId} onChange={(e) => resetAnd(() => setLibraryId(e.target.value))}>
          <option value="">All libraries</option>
          {libs.data?.map((l) => <option key={l.id} value={l.id}>{l.name}</option>)}
        </select>
        <select className="select" style={{ width: 190 }} value={status} onChange={(e) => resetAnd(() => setStatus(e.target.value))}>
          <option value="">Any status</option>
          {Object.entries(STATUS_FILTERS).map(([value, f]) => (
            <option key={value} value={value}>{f.label}</option>
          ))}
        </select>
        <input className="input mono" style={{ width: 240 }} placeholder="Filter by path…" value={q} onChange={(e) => resetAnd(() => setQ(e.target.value))} />
      </div>

      {/* The identifier filter states itself and offers its own way out. A narrowing
          arrived at by following a link is the one a user is most likely to forget is
          on — an empty result here would otherwise read as an empty library. */}
      {mbid && (
        <div className="row" style={{ gap: 8, alignItems: "baseline", flexWrap: "wrap" }}>
          <span className="dim" style={{ fontSize: 12 }}>Files under</span>
          <IdChip value={mbid} />
          <button
            className="btn btn-ghost btn-sm"
            onClick={() =>
              resetAnd(() => {
                const next = new URLSearchParams(params);
                next.delete("mbid");
                setParams(next, { replace: true });
              })
            }
          >
            Show everything
          </button>
        </div>
      )}

      {picked.length > 0 && (
        <div className="row" style={{ justifyContent: "space-between" }}>
          <span className="dim" style={{ fontSize: 12 }}>{picked.length} selected</span>
          <div className="row">
            <button className="btn btn-ghost btn-sm" onClick={() => setPicked([])}>Clear</button>
            <button className="btn btn-primary btn-sm" onClick={() => setBulk(true)}>
              Attach {picked.length} to one release…
            </button>
          </div>
        </div>
      )}

      {page.err && <ErrorNote message={page.err} />}
      {!page.err && !page.loading && items.length === 0 && (
        <EmptyState icon="≣" message="No items match. Process a library to populate the index." />
      )}

      {items.length > 0 && (
        <>
          <div className="tablewrap">
            <table className="data">
              <thead>
                <tr>
                  <th style={{ width: 28 }}>
                    <input
                      type="checkbox"
                      checked={allPicked}
                      disabled={attachable.length === 0}
                      title={
                        attachable.length === 0
                          ? "Nothing here can be attached by hand — Lidarr owns these files' identity."
                          : "Select every file on this page that can be attached by hand"
                      }
                      onChange={() => setPicked(allPicked ? [] : attachable.map((it) => it.id))}
                    />
                  </th>
                  <th>Path</th>
                  <th>MB release id</th>
                  <th>Source</th>
                  <th>Status</th>
                  <th style={{ textAlign: "right" }}>Actions</th>
                </tr>
              </thead>
              <tbody>
                {items.map((it) => (
                  <tr key={it.id} style={{ cursor: "pointer" }} onClick={() => setSelected(it)}>
                    <td onClick={(e) => e.stopPropagation()}>
                      <input
                        type="checkbox"
                        checked={picked.includes(it.id)}
                        disabled={!it.identity_editable}
                        title={it.identity_editable ? undefined : "Lidarr owns this file's identity"}
                        onChange={() => toggle(it.id)}
                      />
                    </td>
                    <td>
                      <div className="stack" style={{ gap: 2 }}>
                        <span className="path">{it.path}</span>
                        <FailureLine item={it} />
                      </div>
                    </td>
                    <td onClick={(e) => e.stopPropagation()}>
                      <div className="row" style={{ gap: 6 }}>
                        <IdChip value={it.mb_release_id} />
                        <MBLink entity="release" mbid={it.mb_release_id} />
                      </div>
                    </td>
                    <td className="mono dim" style={{ fontSize: 11 }}>
                      {it.correlation_source || "—"}
                      {it.pinned && <span title="Manually attached; automatic resolution will not override it"> 📌</span>}
                    </td>
                    <td>
                      {it.pin_held ? (
                        <span title="This file's audio changed since it was attached by hand. It is not tagged until the pin is kept or released.">
                          <Pill kind="warn">Pin needs review</Pill>
                        </span>
                      ) : it.last_error_transient ? (
                        <span title={RETRY_TITLES[it.status]}>
                          <Pill kind={it.status === "error" ? "warn" : "off"}>
                            {it.status === "error" ? "Will retry" : "Unmatched · waiting"}
                          </Pill>
                        </span>
                      ) : (
                        <StatusPill status={it.status} />
                      )}
                    </td>
                    <td onClick={(e) => e.stopPropagation()}>
                      <div className="row" style={{ justifyContent: "flex-end" }}>
                        {/* A held pin is settled one of two ways: kept for the new
                            audio, or released to automatic identification. Re-attach
                            beside them is the third — a different identity. */}
                        {it.pin_held && (
                          <>
                            <button
                              className="btn btn-secondary btn-sm"
                              disabled={busy === it.id}
                              title="The new audio is still the recording you attached: keep the pin and write it"
                              onClick={() => keepPin(it)}
                            >
                              Keep pin
                            </button>
                            <button
                              className="btn btn-ghost btn-sm"
                              style={{ color: "var(--danger-text)" }}
                              disabled={busy === it.id}
                              onClick={() => setReleasing(it)}
                            >
                              Release pin
                            </button>
                          </>
                        )}
                        {/* Albums are attached per folder, so filtering to one is the
                            first step of the bulk workflow. */}
                        <button
                          className="btn btn-ghost btn-sm"
                          title="Filter to this folder, then select all"
                          onClick={() => resetAnd(() => setQ(folderOf(it.path)))}
                        >
                          Folder
                        </button>
                        {/* Disabled, not hidden, for a Lidarr-managed file: the API
                            rejects the attach (409) because Lidarr owns which release
                            and track a file is, and a control that vanishes per row
                            is harder to account for than a dimmed one. */}
                        <button
                          className="btn btn-secondary btn-sm"
                          disabled={!it.identity_editable}
                          title={
                            it.identity_editable
                              ? undefined
                              : "Lidarr owns this file's identity — set the release in Lidarr, then re-correlate."
                          }
                          onClick={() => setAttaching(it)}
                        >
                          {it.mb_release_id ? "Re-attach" : "Attach"}
                        </button>
                      </div>
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
          <div className="row" style={{ justifyContent: "flex-end" }}>
            <span className="dim mono" style={{ fontSize: 11 }}>
              {offset + 1}–{Math.min(offset + PAGE, total)} of {total}
            </span>
            <button className="btn btn-secondary btn-sm" disabled={offset === 0} onClick={() => setOffset(Math.max(0, offset - PAGE))}>Prev</button>
            <button className="btn btn-secondary btn-sm" disabled={offset + PAGE >= total} onClick={() => setOffset(offset + PAGE)}>Next</button>
          </div>
        </>
      )}

      {selected && <ItemDiffModal item={selected} onClose={() => setSelected(null)} />}

      {releasing && (
        <ConfirmDialog
          title="Release pin"
          danger
          busy={busy === releasing.id}
          confirmLabel="Release pin"
          body={
            <>
              <span className="path">{releasing.path}</span>
              <span>
                The identity you attached by hand is dropped, and the next processing run identifies this file the
                way it identifies any other — through its library's manager, or its own tags.
              </span>
            </>
          }
          onConfirm={() => releasePin(releasing)}
          onCancel={() => setReleasing(null)}
        />
      )}

      {attaching && (
        <AttachModal
          item={attaching}
          onClose={() => setAttaching(null)}
          onAttached={() => { setAttaching(null); page.reload(); }}
        />
      )}

      {bulk && pickedItems.length > 0 && (
        <BulkAttachModal
          items={pickedItems}
          onClose={() => setBulk(false)}
          onAttached={() => { setBulk(false); setPicked([]); page.reload(); }}
        />
      )}
    </div>
  );
}

/** Why a transient row is not a problem yet, for the pill's title. */
const RETRY_TITLES: Record<string, string> = {
  error:
    "This failure is expected to clear by itself — MusicBrainz was unreachable or throttled. The next processing run retries the file.",
  unmatched:
    "The manager did not recognise a file it knew, which is what Lidarr looks like part-way through replacing it. The file stays in the collection on its old identity until the next run either matches it or misses it again.",
};

/**
 * The failure a row carries, and when it happened, under its path. Kept to one line:
 * the full message is in the row's detail, and a wrapped stack trace in every row of a
 * failed run would push the list off the screen.
 */
function FailureLine({ item }: { item: LibraryItem }) {
  if (item.status !== "error" || !item.error) return null;
  const when = item.last_error_at ? new Date(item.last_error_at).toLocaleString() : "";
  return (
    <span
      className="mono"
      title={item.error}
      style={{
        fontSize: 11,
        color: item.last_error_transient ? "var(--text-muted)" : "var(--danger-text)",
        overflow: "hidden",
        textOverflow: "ellipsis",
        whiteSpace: "nowrap",
        maxWidth: "68ch",
      }}
    >
      {when && <span className="dim">{when} · </span>}
      {item.error}
    </span>
  );
}
