import { LibraryItem } from "../types";
import { Pill, StatusPill } from "./ui";

// How an indexed file's state reads, shared by the Items page and the Items modal on
// the artist and album pages, so a file says the same thing wherever it is listed.

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
export function FailureLine({ item }: { item: LibraryItem }) {
  // An unmatched file is not a failure, but its manager may have said what would match
  // it — said quietly, in the muted colour, since nothing here went wrong.
  if (item.status === "unmatched" && item.error) {
    return (
      <span
        className="dim"
        title={item.error}
        style={{ fontSize: 11, overflow: "hidden", textOverflow: "ellipsis", whiteSpace: "nowrap", maxWidth: "68ch" }}
      >
        {item.error}
      </span>
    );
  }
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

/**
 * A file's state as one pill: a pin held for review first (it needs a person), then a
 * failure or miss expected to clear by itself, then the status.
 */
export function ItemStatusPill({ item }: { item: LibraryItem }) {
  if (item.pin_held) {
    return (
      <span title="This file's audio changed since it was attached by hand. It is not tagged until the pin is kept or released.">
        <Pill kind="warn">Pin needs review</Pill>
      </span>
    );
  }
  if (item.last_error_transient) {
    return (
      <span title={RETRY_TITLES[item.status]}>
        <Pill kind={item.status === "error" ? "warn" : "off"}>{item.status === "error" ? "Will retry" : "Unmatched · waiting"}</Pill>
      </span>
    );
  }
  return <StatusPill status={item.status} />;
}
