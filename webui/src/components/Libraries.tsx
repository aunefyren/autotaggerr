import { api } from "../api";
import { useFetch } from "../hooks";
import { Library, LibraryCopy } from "../types";
import { Browse } from "./browse";

/**
 * The configured libraries, and whether there is more than one. Library names are
 * only shown when there is: on a single-library install every row would carry the
 * same word, which says nothing.
 */
export function useLibraries() {
  const libs = useFetch<Library[]>(() => api.get("/libraries"));
  const list = libs.data ?? [];
  return { list, multi: list.length > 1 };
}

/**
 * Where something lives: one tag per library holding files of it. With `total`, a
 * library holding less than all of it says how much — "Music Sandbox 11/12" — so a
 * partial copy beside a complete one is not read as two complete ones.
 */
export function LibraryTags({ copies, total }: { copies: LibraryCopy[]; total?: number }) {
  if (copies.length === 0) return null;
  return (
    <span className="libtags">
      {copies.map((c) => {
        const partial = total !== undefined && total > 0 && c.owned_tracks < total;
        const dupes = c.files > c.owned_tracks;
        return (
          <span
            key={c.library_id}
            className="libtag"
            title={
              `${c.name}: ${c.owned_tracks} track${c.owned_tracks === 1 ? "" : "s"}` +
              (total ? ` of ${total}` : "") +
              (dupes ? `, ${c.files} files — some tracks are here more than once` : "")
            }
          >
            {c.name || "Unknown library"}
            {partial && (
              <span className="mono libtag-n">
                {c.owned_tracks}/{total}
              </span>
            )}
          </span>
        );
      })}
    </span>
  );
}

/**
 * The library filter for a browsing table, kept in the URL as `library`. A select
 * rather than chips, per the toolbar rule: libraries are a list you pick one from.
 * Only libraries something on the page is in are offered, plus the active one, so a
 * filter matching nothing can still be seen and undone.
 */
export function LibrarySelect({
  browse,
  libraries,
  counts,
}: {
  browse: Browse;
  libraries: Library[];
  /** How many rows each library holds, by library ID. */
  counts: Map<string, number>;
}) {
  const active = browse.flag("library") ?? "";
  const offered = libraries.filter((l) => (counts.get(l.id) ?? 0) > 0 || l.id === active);
  return (
    <select
      className="input"
      style={{ width: "auto" }}
      value={active}
      aria-label="Library"
      onChange={(e) => browse.setFlag("library", e.target.value || null)}
    >
      <option value="">All libraries</option>
      {offered.map((l) => (
        <option key={l.id} value={l.id}>
          {`${l.name} (${counts.get(l.id) ?? 0})`}
        </option>
      ))}
    </select>
  );
}
