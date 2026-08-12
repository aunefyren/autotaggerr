import { ConfirmDialog } from "./ui";

/**
 * The one confirm step for force re-correlate, at all three scopes.
 *
 * The verb repairs the case an ordinary Process cannot: a release selection changed in
 * Lidarr does not touch a byte on disk, so the unchanged-file skip means a normal run
 * never re-reads those files. This busts the manager caches and re-walks with the skip
 * disabled, so what the manager says now is what gets written.
 *
 * It is one component rather than three because the wording is the warning. The three
 * scopes differ only in *how much* they touch, and a copy per page is how a verb ends
 * up meaning three things — the same drift RefreshMetadataDialog exists to prevent.
 *
 * **`discardsPins` is not decoration.** The runner clears manual pins only for
 * libraries a *Lidarr* manager governs (prepareForceRecorrelate skips every other
 * manager type), so promising to discard them everywhere would be a warning about
 * something that does not happen — and the reverse, staying silent under Lidarr, would
 * lose work the user did by hand with no notice. The caller knows which manager
 * governs its scope; it says so here.
 */
export function RecorrelateDialog({
  scope,
  manager,
  discardsPins,
  busy,
  onConfirm,
  onCancel,
}: {
  /** What is being repaired, in the user's words: an artist, an album, a library. */
  scope: string;
  /** The manager that owns identity here, named so the dialog can say whose answer wins. */
  manager?: string;
  /** Whether hand-attached files in scope lose their pins. True only under Lidarr. */
  discardsPins?: boolean;
  busy?: boolean;
  onConfirm: () => void;
  onCancel: () => void;
}) {
  const authority = manager || "the manager";
  return (
    <ConfirmDialog
      title={`Re-correlate ${scope} from ${authority}?`}
      confirmLabel="Re-correlate"
      danger
      busy={busy}
      onCancel={onCancel}
      onConfirm={onConfirm}
      body={
        <>
          <p>
            Every file in {scope} is identified again from scratch — {authority} is asked
            what each one is, its cached answers are discarded first, and the tags are
            rewritten to match. <strong>This writes to your audio files.</strong>
          </p>
          <p>
            It is the repair for files that disagree with {authority}: an edition changed
            there, or an album was re-imported. An ordinary Process cannot fix that, because
            nothing on disk changed, so it skips the files as unchanged.
          </p>
          {discardsPins && (
            <p>
              <strong>Files you attached by hand lose that pin.</strong> Under {authority},
              identity is the manager&apos;s to decide, so anything you corrected manually in{" "}
              {scope} is re-answered by it — and if it still disagrees, it wins.
            </p>
          )}
          <p className="dim">
            Queues behind any job already running, and reports as a run in Activity.
          </p>
        </>
      }
    />
  );
}
