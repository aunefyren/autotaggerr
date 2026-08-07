import { ReactNode, useMemo } from "react";
import { useSearchParams } from "react-router-dom";

/**
 * Browsing state — what is being looked for, and in what order.
 *
 * It lives in the URL rather than in component state for one reason: opening an
 * album and coming back must not reset the list. Sorting a 300-album discography
 * by year, opening the third row, then landing back at the top of an unsorted list
 * is the single most annoying thing a library UI can do, and it costs nothing to
 * avoid.
 */
export type SortDir = "asc" | "desc";

export interface Browse {
  /** Free-text filter, matched against whatever the page decides is the text. */
  query: string;
  setQuery: (q: string) => void;
  sort: string;
  dir: SortDir;
  /** Clicking the active column flips direction; a new column starts at its own default. */
  toggleSort: (key: string, defaultDir?: SortDir) => void;
  /** Sets the key and direction outright — for a sort <select>, where re-picking the
   *  current option must not silently reverse the order. */
  setSort: (key: string, dir?: SortDir) => void;
  /** Arbitrary named flags kept in the URL alongside the rest (filters, open sections). */
  flag: (name: string) => string | null;
  setFlag: (name: string, value: string | null) => void;
  /** Which page of the result is being shown, 1-based. See {@link usePaging}. */
  page: number;
  /** The one update that does *not* reset paging, because it is the paging. */
  setPage: (page: number) => void;
}

export function useBrowse(defaultSort: string, defaultDir: SortDir = "asc"): Browse {
  const [params, setParams] = useSearchParams();

  const update = (mutate: (next: URLSearchParams) => void, keepPage = false) => {
    const next = new URLSearchParams(params);
    mutate(next);
    // Any change to *what* or *how* you are looking resets to the first page.
    // Filtering a list down to twelve rows while sitting on page four shows an empty
    // table and no indication why — the rows are not missing, you are past the end of
    // them. Only setPage itself opts out.
    if (!keepPage) next.delete("page");
    // Replace rather than push: sorting a table is not a place in history to go
    // back to, and it would take a dozen Back presses to leave the page.
    setParams(next, { replace: true });
  };

  const sort = params.get("sort") || defaultSort;
  const dir = (params.get("dir") as SortDir) || defaultDir;

  return {
    query: params.get("q") ?? "",
    setQuery: (q) =>
      update((next) => {
        if (q) next.set("q", q);
        else next.delete("q");
      }),
    sort,
    dir,
    setSort: (key, nextDir) =>
      update((next) => {
        next.set("sort", key);
        next.set("dir", nextDir ?? dir);
      }),
    toggleSort: (key, keyDefaultDir = "asc") =>
      update((next) => {
        if (key === sort) {
          next.set("dir", dir === "asc" ? "desc" : "asc");
        } else {
          next.set("sort", key);
          next.set("dir", keyDefaultDir);
        }
      }),
    flag: (name) => params.get(name),
    setFlag: (name, value) =>
      update((next) => {
        if (value === null) next.delete(name);
        else next.set(name, value);
      }),
    page: Math.max(1, parseInt(params.get("page") ?? "1", 10) || 1),
    setPage: (page) =>
      update((next) => {
        // Page one is the default, so it stays out of the URL — a shared link should
        // not carry state that means "nothing was changed".
        if (page <= 1) next.delete("page");
        else next.set("page", String(page));
      }, true),
  };
}

/**
 * Which slice of a list is on screen, and the arithmetic for saying so.
 *
 * Paging is shared, but **fetching is not, deliberately**. The Collection holds every
 * artist and sorts and filters them in the browser, so its pages are slices of what is
 * already there — server paging would sort one page rather than the list. Activity is
 * the other way round: it never holds the whole table, so its pages are `offset`/`limit`
 * on the API. One hook covers both because what they share is the *position*, not where
 * the rows come from: `offset` is equally an array index and a query parameter.
 *
 * The page number is clamped rather than corrected in place. A poll that shrinks the
 * total while you sit on the last page should show you the new last page, not rewrite
 * the URL underneath you mid-render.
 */
export interface Paging {
  page: number;
  pageSize: number;
  /** 0-based: an array index for a client-paged list, an API offset for a server-paged one. */
  offset: number;
  pageCount: number;
  total: number;
  /** 1-based bounds of what is on screen, for a "31–60 of 312" count. */
  from: number;
  to: number;
  setPage: (page: number) => void;
}

export function usePaging(browse: Browse, total: number, pageSize = 50): Paging {
  const pageCount = Math.max(1, Math.ceil(total / pageSize));
  const page = Math.min(Math.max(1, browse.page), pageCount);
  const offset = (page - 1) * pageSize;
  return {
    page,
    pageSize,
    offset,
    pageCount,
    total,
    from: total === 0 ? 0 : offset + 1,
    to: Math.min(offset + pageSize, total),
    setPage: browse.setPage,
  };
}

/**
 * The control for moving between pages, sitting under the table it pages.
 *
 * It renders nothing at all when everything fits on one page. A pager that cannot go
 * anywhere is furniture, and the range it would state ("1–12 of 12") is already the
 * toolbar's job.
 */
export function Pager({ paging, unit = "rows" }: { paging: Paging; unit?: string }) {
  if (paging.pageCount <= 1) return null;

  return (
    <nav className="pager" aria-label={`${unit} pages`}>
      <button
        className="btn btn-secondary btn-sm"
        onClick={() => paging.setPage(paging.page - 1)}
        disabled={paging.page <= 1}
      >
        Previous
      </button>
      <span className="dim mono" style={{ fontSize: 11 }}>
        {paging.from}–{paging.to} of {paging.total}
      </span>
      <button
        className="btn btn-secondary btn-sm"
        onClick={() => paging.setPage(paging.page + 1)}
        disabled={paging.page >= paging.pageCount}
      >
        Next
      </button>
      <span className="dim" style={{ fontSize: 11 }}>
        Page {paging.page} of {paging.pageCount}
      </span>
    </nav>
  );
}

/**
 * A sortable column header. A real button inside the th, so it is reachable by
 * keyboard and announces its state; aria-sort carries the state to assistive tech
 * and the caret carries it visually.
 */
export function SortHeader({
  browse,
  sortKey,
  defaultDir = "asc",
  align = "left",
  children,
  title,
}: {
  browse: Browse;
  sortKey: string;
  defaultDir?: SortDir;
  align?: "left" | "right";
  children: ReactNode;
  title?: string;
}) {
  const active = browse.sort === sortKey;
  return (
    <th
      aria-sort={active ? (browse.dir === "asc" ? "ascending" : "descending") : "none"}
      style={{ textAlign: align }}
    >
      <button
        type="button"
        className={`sortbtn${active ? " active" : ""}`}
        style={align === "right" ? { flexDirection: "row-reverse" } : undefined}
        onClick={() => browse.toggleSort(sortKey, defaultDir)}
        title={title ?? `Sort by ${typeof children === "string" ? children.toLowerCase() : sortKey}`}
      >
        {children}
        <span className="caret" aria-hidden="true">
          {active ? (browse.dir === "asc" ? "▲" : "▼") : "•"}
        </span>
      </button>
    </th>
  );
}

/** The bar above a table: what is being searched, and what is being shown. */
export function TableToolbar({
  browse,
  placeholder,
  children,
  showing,
}: {
  browse: Browse;
  placeholder: string;
  /** Filter chips, view switches — anything that narrows the same list. */
  children?: ReactNode;
  /** "12 of 41" style count, so an empty result is distinguishable from an empty list. */
  showing?: string;
}) {
  return (
    <div className="table-toolbar">
      <div className="toolbar-search">
        <input
          className="input"
          type="search"
          value={browse.query}
          placeholder={placeholder}
          aria-label={placeholder}
          onChange={(e) => browse.setQuery(e.target.value)}
        />
      </div>
      {children}
      {showing && <span className="dim mono toolbar-count">{showing}</span>}
    </div>
  );
}

/**
 * A filter chip that is also a count. The numbers on these pages were already
 * asking to be clickable — "3 partial" is only ever read as a prelude to "show me
 * which three".
 */
export function FilterChip({
  on,
  count,
  label,
  tone,
  onClick,
  title,
}: {
  on: boolean;
  count: number;
  label: string;
  /** Matches the status colour language: warn for drift, chg for wanted. */
  tone?: "warn" | "chg" | "ok";
  onClick: () => void;
  title?: string;
}) {
  return (
    <button
      type="button"
      className={`chip${on ? " on" : ""}${tone ? ` chip-${tone}` : ""}`}
      aria-pressed={on}
      onClick={onClick}
      title={title}
      disabled={count === 0 && !on}
    >
      {label}
      <span className="chip-n mono">{count}</span>
    </button>
  );
}

/** Case-insensitive substring match, used by every text filter on these pages. */
export function matches(query: string, ...fields: (string | undefined)[]): boolean {
  const q = query.trim().toLowerCase();
  if (!q) return true;
  return fields.some((f) => (f ?? "").toLowerCase().includes(q));
}

/**
 * Sorts a copy by a keyed accessor. Strings compare with localeCompare so
 * "Ólafur" files where a reader expects it; empty values always sink to the bottom
 * regardless of direction, because a missing year is not "earliest".
 */
export function sortRows<T>(rows: T[], key: (row: T) => string | number, dir: SortDir): T[] {
  const factor = dir === "asc" ? 1 : -1;
  return [...rows].sort((a, b) => {
    const av = key(a);
    const bv = key(b);
    const aEmpty = av === "";
    const bEmpty = bv === "";
    if (aEmpty !== bEmpty) return aEmpty ? 1 : -1;
    if (typeof av === "number" && typeof bv === "number") return (av - bv) * factor;
    return String(av).localeCompare(String(bv), undefined, { numeric: true }) * factor;
  });
}

/** Memoised {@link sortRows}, for lists long enough that re-sorting on every keystroke shows. */
export function useSorted<T>(rows: T[], key: (row: T) => string | number, dir: SortDir): T[] {
  // eslint-disable-next-line react-hooks/exhaustive-deps
  return useMemo(() => sortRows(rows, key, dir), [rows, dir, key]);
}
