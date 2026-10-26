import { api } from "../api";
import { useFetch } from "../hooks";
import { ArtistLibrary, Authority, CollectionArtist, Library, LibraryCopy } from "../types";
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

/** The word for who decides in a library, as the managed-by pills say it. */
export const AUTHORITY_LABEL: Record<Authority, string> = {
  lidarr: "Lidarr",
  autotaggerr: "Native",
  unknown: "Unknown",
};

/** The pill kind each authority is drawn in everywhere, so its dot matches. */
export const AUTHORITY_PILL: Record<Authority, string> = {
  lidarr: "scan",
  autotaggerr: "chg",
  unknown: "off",
};

/**
 * Which library an artist's pages are read in, kept in the URL as `library`. Tabs
 * rather than the toolbar's library filter, because this does more than narrow rows:
 * authority is per library, so choosing one changes what is wanted, what is owned and
 * whether any of it is editable. Each tab names the library's authority with the dot
 * its managed-by pill uses. Rendered only when the artist can be read in more than
 * one library — otherwise there is nothing to choose between.
 */
export function LibraryTabs({
  libraries,
  active,
  onChoose,
}: {
  libraries: ArtistLibrary[];
  active: string | null;
  onChoose: (id: string | null) => void;
}) {
  if (libraries.length < 2) return null;
  return (
    <div className="tabs libview" role="tablist" aria-label="Library">
      <button
        type="button"
        role="tab"
        className="tab"
        aria-selected={!active}
        title="Every library at once: where each album lives and who wants it. Choose a library to edit what is wanted there."
        onClick={() => onChoose(null)}
      >
        All libraries
      </button>
      {libraries.map((l) => (
        <button
          key={l.library_id}
          type="button"
          role="tab"
          className="tab"
          aria-selected={active === l.library_id}
          title={libraryTabTitle(l)}
          onClick={() => onChoose(l.library_id)}
        >
          {l.name}
          <span className={`tab-auth pill-${AUTHORITY_PILL[l.authority]}`}>
            <span className="dot" />
            {AUTHORITY_LABEL[l.authority]}
          </span>
          {!l.holds && <span className="tab-auth">empty</span>}
        </button>
      ))}
    </div>
  );
}

function libraryTabTitle(l: ArtistLibrary): string {
  const holds = l.holds ? "" : " Nothing of this artist is here yet.";
  if (l.authority === "lidarr") return `Lidarr decides what is wanted in ${l.name}; read-only here.${holds}`;
  if (l.authority === "autotaggerr") return `Autotaggerr decides what is wanted in ${l.name}: want, choose editions and follow here.${holds}`;
  return `${l.name} has no resolvable manager — reassign one on the Libraries page.${holds}`;
}

/** The name of the library an edit in this view goes to, if it names one. */
export function editLibraryName(artist: CollectionArtist | undefined): string | null {
  if (!artist?.edit_library_id) return null;
  return artist.libraries?.find((l) => l.library_id === artist.edit_library_id)?.name ?? null;
}

/**
 * Why nothing here is editable, in one sentence naming where to go instead. Null
 * when it is editable.
 */
export function editBlockedReason(artist: CollectionArtist | undefined): string | null {
  if (!artist || artist.identity_editable) return null;
  switch (artist.edit_blocked) {
    case "choose_library":
      return "Lidarr manages this artist in its own library, so every library at once is read-only — choose a library above to edit there.";
    case "unknown":
      return "This library has no resolvable manager — reassign one on the Libraries page.";
    case "lidarr": {
      const native = artist.libraries?.filter((l) => l.authority === "autotaggerr") ?? [];
      const elsewhere = native.length > 0 ? ` To want it yourself, switch to ${native.map((l) => l.name).join(" or ")}.` : "";
      return `Lidarr decides what is wanted in this library and which edition it is. Change it in Lidarr.${elsewhere}`;
    }
    default:
      return "Lidarr decides what is wanted for this artist and which edition it is. Change it in Lidarr.";
  }
}

/**
 * The library ID an edit's request carries: the one this view edits, or nothing —
 * which is how a want tied to no library is asked for.
 */
export function editLibraryParam(artist: CollectionArtist | undefined): { library_id?: string } {
  return artist?.edit_library_id ? { library_id: artist.edit_library_id } : {};
}
