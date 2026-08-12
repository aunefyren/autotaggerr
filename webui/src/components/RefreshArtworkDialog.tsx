import { ChoiceDialog } from "./ui";

/**
 * Both readings of *Refresh artwork*, chosen here rather than on the page.
 *
 * A sibling of RefreshMetadataDialog rather than a reuse of it, because the two verbs
 * cost fundamentally different things and stating the cost is what a dialog is for.
 * The metadata one measures itself in rate-limited *requests* — honest for JSON
 * lookups, useless here, where the expensive half is re-downloading images. Copy that
 * said "requests" for a few hundred megabytes of transfer would understate it by
 * orders of magnitude, which is the one failure a dialog like this cannot have.
 *
 * The two halves of the cache are named separately because they differ by orders of
 * magnitude, and the cheap one is usually the reason to press this at all: art gets
 * uploaded, and a remembered "no image" is what stands between that and the page.
 */
export function RefreshArtworkDialog({
  images,
  missing,
  busy,
  onRefresh,
  onForce,
  onCancel,
}: {
  /** Images on disk — the half that costs a transfer, and what the estimate rests on. */
  images?: number;
  /** Remembered "no image" answers — re-asked too, but cheaply. */
  missing?: number;
  busy?: boolean;
  /** The routine reading: fetch what is missing or expired. */
  onRefresh: () => void;
  /** The expensive reading: fetch everything again. */
  onForce: () => void;
  onCancel: () => void;
}) {
  // Two requests a second per host is the artwork throttle, and the honest unit for
  // "how long will this take" — the transfer, not the round trip, is what varies.
  const total = (images ?? 0) + (missing ?? 0);
  const minutes = total > 0 ? total / 120 : 0;
  const estimate =
    minutes >= 60
      ? `roughly ${Math.round(minutes / 60)} hours`
      : minutes >= 1
        ? `roughly ${Math.round(minutes)} minutes`
        : total
          ? "a moment"
          : "";

  return (
    <ChoiceDialog
      title="Refresh artwork"
      primaryLabel="Refresh"
      alternateLabel="Ignore cache and refresh"
      busy={busy}
      onCancel={onCancel}
      onPrimary={onRefresh}
      onAlternate={onForce}
      body={
        <>
          <p>
            <strong>Refresh</strong> fetches only what is missing or expired. New artists and
            albums already fetch their own artwork as they arrive, so this is mainly for
            filling a cold cache.
          </p>
          <p>
            <strong>Ignore cache and refresh</strong> fetches everything again
            {total ? <> — about {total.toLocaleString()} images, so {estimate}</> : null}:
          </p>
          <ul style={{ margin: 0, paddingLeft: "var(--space-7)" }}>
            {images ? (
              <li>
                <strong>{images.toLocaleString()} already on disk</strong> are downloaded again.
                This is the slow part, and the one that uses bandwidth.
              </li>
            ) : null}
            {missing ? (
              <li>
                <strong>{missing.toLocaleString()} recorded as having no artwork</strong> are
                re-asked, which is quick — and is usually the reason to do this, since it picks
                up art added upstream since they were checked.
              </li>
            ) : null}
          </ul>
          <p>
            <strong>Reads only: none of your audio files are touched.</strong> Artwork is stored
            beside the app's configuration, never in your library.
          </p>
          <p className="dim">
            You can stop a running pass at any time; whatever it already fetched is kept.
          </p>
        </>
      }
    />
  );
}
