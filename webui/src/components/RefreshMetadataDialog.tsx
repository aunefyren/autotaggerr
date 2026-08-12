import { ChoiceDialog } from "./ui";

/**
 * The one place a metadata refresh is started, and the one place the cache can be
 * ignored.
 *
 * Both readings of the verb live here rather than one being a checkbox on the page.
 * The old shape — a persistent "Ignore cached copies" box that changed what the button
 * did — was a mode switch, and it needed a rule of its own ("the box resets once a pass
 * starts") to stop one considered decision becoming a setting. A choice made in the
 * dialog needs no such rule: there is nothing left ticked afterwards.
 *
 * Forcing is reachable from **exactly one control** now. It used to be two, here and on
 * the Migrations page, which meant the same words meant the expensive thing on one page
 * and the cheap thing everywhere else — the drift this dialog exists to prevent.
 *
 * What it states is the cost, because that is what a button cannot: one rate-limited
 * request per entity, hours on a large collection, and the *reads only* half that makes
 * the wait acceptable rather than alarming.
 */
export function RefreshMetadataDialog({
  entities,
  artist,
  busy,
  onRefresh,
  onForce,
  onCancel,
}: {
  /**
   * How many entities a forced pass would re-read, when the caller knows. Omitted
   * where it has not been counted — an invented number would be worse than none,
   * since it is what the time estimate rests on.
   */
  entities?: number;
  /**
   * The artist this pass covers, when it is scoped to one. Absent means the whole
   * collection. It changes the copy rather than only the title, because the two
   * differ in the figure that decides it: forcing one artist is minutes, forcing the
   * collection is hours, and a dialog whose whole job is stating the cost cannot
   * describe them in the same sentence.
   */
  artist?: string;
  busy?: boolean;
  /** The routine reading: re-read only what has expired. */
  onRefresh: () => void;
  /** The expensive reading: re-read everything. */
  onForce: () => void;
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

  const scoped = Boolean(artist);

  return (
    <ChoiceDialog
      title={artist ? `Refresh metadata for ${artist}` : "Refresh metadata"}
      primaryLabel="Refresh"
      alternateLabel="Ignore cache and refresh"
      busy={busy}
      onCancel={onCancel}
      onPrimary={onRefresh}
      onAlternate={onForce}
      body={
        <>
          <p>
            <strong>Refresh</strong> re-reads only what has expired
            {scoped
              ? " — this artist, their discography, the editions of each album and the releases you hold."
              : " across the artists, release-groups and releases the collection refers to."}{" "}
            This is what the schedule does, and it is usually all you need.
          </p>
          <p>
            <strong>Ignore cache and refresh</strong> re-reads every one of them, however
            recently it was checked — <strong>one rate-limited request each</strong>
            {scoped ? (
              ", so a few minutes for one artist"
            ) : entities ? (
              <>
                , about {entities.toLocaleString()} of them, so {estimate}
              </>
            ) : (
              ", so hours on a large collection"
            )}
            . That is how merges and deletions upstream are found
            {scoped ? " for this artist" : ""}.
          </p>
          <p>
            <strong>Reads only: no files are written.</strong> Anything that changed upstream is
            reported on the Metadata and Activity pages, and the next processing run re-tags the
            files that use it.
          </p>
          <p className="dim">
            You can stop a running pass at any time; whatever it already refreshed is kept.
          </p>
        </>
      }
    />
  );
}
