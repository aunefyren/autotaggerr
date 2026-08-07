/**
 * What the stage a running job reports means, and whether that stage is the one the
 * progress bar is counting.
 *
 * Shared rather than local to Activity because three surfaces draw the same bar from
 * the same counters — the Activity banner and feed rows, the Dashboard run widget and
 * the Artist page — and a rule about whether the bar is meaningful has to hold on all
 * of them or it is not a rule.
 */

/** Human labels for the stage a running job reports, across runs and metadata passes. */
export const PHASE_LABELS: Record<string, string> = {
  // processing phases
  refresh: "Refreshing metadata",
  scanning: "Scanning files",
  drift: "Re-tagging changed releases",
  plex: "Refreshing Plex",
  migrations: "Applying identity changes",
  collection: "Updating the collection",
  // metadata-pass phases
  artists: "Artists",
  discographies: "Discographies",
  editions: "Editions",
  releases: "Releases",
  paused: "Paused",
};

/**
 * The phases whose own work advances the counters the bar is drawn from.
 *
 * A processing run counts **files**, and only the walk moves that number. Its other
 * five stages do real, sometimes long work in a different unit entirely — the refresh
 * stage counts releases against the MusicBrainz rate limit, the collection stage
 * re-derives the whole collection and mirrors the manager — while the file counters
 * sit wherever the walk left them. So the bar reads 0% for the minutes before the walk
 * and 100% for the minutes after it, which is the "stuck" that this set exists to stop
 * us drawing.
 *
 * A metadata pass counts **entities**, and every one of its phases advances them, so
 * all four are here. `paused` is too: a yielded pass has not moved, but its counters
 * still describe it truthfully.
 */
const PHASES_DRIVING_PROGRESS = new Set([
  "scanning",
  "artists",
  "discographies",
  "editions",
  "releases",
  "paused",
]);

/**
 * Whether total/done describe the stage the job is actually in.
 *
 * An unreported phase counts as driving it: event types with no phases at all (a Lidarr
 * sync, a health check) report counters that mean what they say, and there is nothing
 * to be honest about.
 */
export function phaseDrivesProgress(phase?: string): boolean {
  if (!phase) return true;
  return PHASES_DRIVING_PROGRESS.has(phase);
}
