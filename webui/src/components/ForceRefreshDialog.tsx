import { ConfirmDialog } from "./ui";

/**
 * The one confirm step for refreshing metadata with the cache ignored.
 *
 * Forcing is reachable from exactly two buttons — the Metadata page's checkbox and
 * the Migrations page's *Refresh metadata (ignore cache)* — and both come through
 * here. A second copy of this wording is how the verb drifted in the first place:
 * the same words used to mean "honour the cache" on one page and "re-read
 * everything" on three others, and the expensive reading was the silent one.
 *
 * What it states is the cost, because that is what the button cannot: the work is
 * one rate-limited request per entity, which is hours on a large collection, and it
 * is the *reads only* half that makes the wait acceptable rather than alarming.
 * "Are you sure?" on its own would be a click, not a warning.
 */
export function ForceRefreshDialog({
  entities,
  busy,
  onConfirm,
  onCancel,
}: {
  /**
   * How many entities the pass will re-read, when the caller knows. Omitted on the
   * Migrations page, which has not counted them — an invented number would be worse
   * than none, since it is the number the time estimate rests on.
   */
  entities?: number;
  busy?: boolean;
  onConfirm: () => void;
  onCancel: () => void;
}) {
  // One request per second is the MusicBrainz limit the whole app is paced by, so it
  // is also the honest unit for "how long will this take".
  const hours = entities && entities > 0 ? entities / 3600 : 0;
  const estimate =
    hours >= 1
      ? `roughly ${hours < 2 ? "an hour" : `${Math.round(hours)} hours`}`
      : entities
        ? "a few minutes"
        : "";

  return (
    <ConfirmDialog
      title="Refresh metadata, ignoring cached copies?"
      confirmLabel="Ignore cache and refresh"
      busy={busy}
      onCancel={onCancel}
      onConfirm={onConfirm}
      body={
        <>
          <p>
            Every artist, release-group and release the collection refers to is read from
            MusicBrainz again, however recently it was checked.
          </p>
          <p>
            That is <strong>one rate-limited request per entity</strong>
            {entities ? (
              <>
                {" "}
                — about {entities.toLocaleString()} of them, so {estimate}
              </>
            ) : (
              " — hours on a large collection"
            )}
            . An ordinary refresh only re-reads what has expired, and the nightly schedule already
            does that.
          </p>
          <p>
            <strong>Reads only: no files are written.</strong> Anything that changed upstream is
            reported on the Metadata and Activity pages, and the next scan re-tags the files that
            use it.
          </p>
          <p className="dim">
            You can stop a running pass at any time; whatever it already refreshed is kept.
          </p>
        </>
      }
    />
  );
}
