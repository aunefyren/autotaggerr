import { Fragment, useState } from "react";
import { Link } from "react-router-dom";
import { api } from "../api";
import { useFetch } from "../hooks";
import { ItemsPage, LibraryItem } from "../types";
import { ErrorNote, Modal } from "./ui";
import { ItemDiffBody } from "./ItemDiff";
import { FailureLine, ItemStatusPill } from "./ItemStatus";
import { useLibraries } from "./Libraries";

/** More than any album has files; past it the modal says so and links to Items. */
const LIMIT = 500;

/**
 * The files behind an album or one edition of it, from the artist and album pages.
 *
 * It **follows the page's library**: read in one library, it lists that library's files
 * only, and says so first — the same scope the page's actions take
 * (docs/collection.md, "The library view scopes the actions"). On the overview it lists
 * every library's, each row tagged with its library.
 *
 * A file opens its tag diff in a row disclosure underneath it rather than a second
 * modal over this one — the style guide's rule against detail two modals deep. The
 * actions on files (attach, pins, the folder filter) live on the Items page, which the
 * footer opens on the same files and library.
 *
 * `mbid` is a release-group or a release; the server resolves either (GET
 * /library-items?mbid=).
 */
export function ItemsModal({
  mbid,
  title,
  libraryId,
  onClose,
}: {
  mbid: string;
  title: string;
  /** The page's library; empty for the overview. */
  libraryId?: string | null;
  onClose: () => void;
}) {
  const libraries = useLibraries();
  const query = new URLSearchParams({ mbid, limit: String(LIMIT) });
  if (libraryId) query.set("library_id", libraryId);
  const page = useFetch<ItemsPage>(() => api.get(`/library-items?${query.toString()}`), [mbid, libraryId]);
  const [open, setOpen] = useState<string | null>(null);

  const byId = new Map(libraries.list.map((l) => [l.id, l]));
  const items = page.data?.items ?? [];
  const total = page.data?.total ?? 0;
  const showLibrary = libraries.multi && !libraryId;

  // The scope, said first: which library these are, or which libraries they are in.
  const scope = (() => {
    if (!libraries.multi) return "";
    if (libraryId) return `In ${byId.get(libraryId)?.name ?? "this library"}`;
    const names = [...new Set(items.map((it) => byId.get(it.library_id)?.name).filter(Boolean))];
    if (names.length === 0) return "In all libraries";
    return names.length === 1 ? `In ${names[0]}` : `In all ${names.length} libraries: ${names.join(", ")}`;
  })();

  const relative = (it: LibraryItem) => {
    const root = byId.get(it.library_id)?.path;
    if (!root || !it.path.startsWith(root)) return it.path;
    return it.path.slice(root.length).replace(/^[\\/]+/, "") || it.path;
  };

  const itemsLink = `/items?mbid=${encodeURIComponent(mbid)}${libraryId ? `&library_id=${encodeURIComponent(libraryId)}` : ""}`;

  return (
    <Modal title={`Items · ${title}`} titleHint={title} onClose={onClose} wide>
      <div className="stack">
        <p className="muted" style={{ margin: 0, fontSize: 12 }}>
          {scope && <>{scope} · </>}
          {page.data
            ? total === 0
              ? "no files"
              : `${total} file${total === 1 ? "" : "s"}${total > items.length ? `, the first ${items.length} shown` : ""}`
            : "Loading…"}
          {page.data && total > 0 && " · select a file to see its tags"}
        </p>

        {page.err && <ErrorNote message={page.err} />}

        {page.data && total === 0 && (
          <span className="dim" style={{ fontSize: 12 }}>
            {libraryId
              ? "This library holds no files of it. Choose All libraries on the page to see the others, or Process to pick up files added on disk."
              : "No files of it are indexed. Process the artist to pick up files added on disk."}
          </span>
        )}

        {items.length > 0 && (
          <div className="tablewrap">
            <table className="data">
              <thead>
                <tr>
                  <th>File</th>
                  {showLibrary && <th>Library</th>}
                  <th>Status</th>
                </tr>
              </thead>
              <tbody>
                {items.map((it) => {
                  const expanded = open === it.id;
                  return (
                    <Fragment key={it.id}>
                      <tr>
                        <td>
                          <div className="stack" style={{ gap: 2 }}>
                            <button
                              type="button"
                              className="railref"
                              style={{ textAlign: "left", maxWidth: "100%" }}
                              onClick={() => setOpen(expanded ? null : it.id)}
                              aria-expanded={expanded}
                              title={it.path}
                            >
                              <span>{expanded ? "▼" : "▶"}</span> <span className="path">{relative(it)}</span>
                            </button>
                            <FailureLine item={it} />
                          </div>
                        </td>
                        {showLibrary && (
                          <td>
                            <span className="libtag">{byId.get(it.library_id)?.name ?? "—"}</span>
                          </td>
                        )}
                        <td>
                          <ItemStatusPill item={it} />
                        </td>
                      </tr>
                      {expanded && (
                        <tr className="detail-row">
                          <td colSpan={showLibrary ? 3 : 2}>
                            <ItemDiffBody item={it} showPath={false} />
                          </td>
                        </tr>
                      )}
                    </Fragment>
                  );
                })}
              </tbody>
            </table>
          </div>
        )}

        <div className="modal-actions">
          {total > 0 && (
            <Link
              className="btn btn-ghost btn-sm"
              style={{ marginRight: "auto" }}
              to={itemsLink}
              title="The same files on the Items page, where they can be attached, filtered by folder and their pins settled"
            >
              Open in Items
            </Link>
          )}
          <button type="button" className="btn btn-secondary btn-sm" onClick={onClose}>
            Close
          </button>
        </div>
      </div>
    </Modal>
  );
}
