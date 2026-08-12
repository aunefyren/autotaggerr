import { ConfirmDialog } from "./ui";

/**
 * The confirm step for refreshing artwork with the cache ignored.
 *
 * A sibling of ForceRefreshDialog rather than a reuse of it, because the two verbs
 * cost fundamentally different things and the dialog exists to state the cost. The
 * metadata one measures itself in rate-limited *requests* — that is honest for JSON
 * lookups and useless here, where the expensive half is re-downloading images. A
 * dialog that said "requests" for a few hundred megabytes of transfer would
 * understate it by orders of magnitude, which is the one failure a confirm step
 * cannot have.
 *
 * The two rules it shares with its sibling are the ones that make forcing
 * deliberate: only the forced pass confirms (confirming both would train people to
 * click through the one that matters), and the caller resets its checkbox once a pass
 * starts, so one considered decision does not become a setting.
 */
export function ForceArtworkDialog({
  images,
  missing,
  busy,
  onConfirm,
  onCancel,
}: {
  /** Images on disk — the half that costs a transfer, and what the estimate rests on. */
  images?: number;
  /** Remembered "no image" answers — re-asked too, but cheaply. */
  missing?: number;
  busy?: boolean;
  onConfirm: () => void;
  onCancel: () => void;
}) {
  // Two requests a second per host is the artwork throttle, and it is the honest unit
  // for "how long will this take" — the transfer, not the round trip, is what varies.
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
    <ConfirmDialog
      title="Refresh artwork, ignoring cached copies?"
      confirmLabel="Ignore cache and refresh"
      busy={busy}
      onCancel={onCancel}
      onConfirm={onConfirm}
      body={
        <>
          <p>
            Every cover and artist image is fetched again, however recently it was checked
            {total ? <> — about {total.toLocaleString()} of them, so {estimate}</> : null}.
          </p>
          <p>
            {/* Naming the split matters: the two halves differ by orders of magnitude,
                and the cheap one is the reason someone would press this at all. */}
            {images ? (
              <>
                <strong>{images.toLocaleString()} images already on disk are downloaded again</strong>,
                which is the slow part and the one that uses bandwidth.{" "}
              </>
            ) : null}
            {missing ? (
              <>
                The {missing.toLocaleString()} entries recorded as having no artwork are re-asked,
                which is quick — and is usually the reason to do this, since it picks up art added
                upstream since they were checked.
              </>
            ) : null}
          </p>
          <p>
            An ordinary refresh only fetches what is missing or expired, and the schedule already
            does that — as does adding an artist, which fetches its own artwork straight away.
          </p>
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
