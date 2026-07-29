# Feature: scanning, drift sync and activity

How files get processed, why most of them get skipped, how upstream MusicBrainz changes are caught,
and how any of it is visible afterwards.

## The scan runner

`scan.Runner` is shared by the cron job, the startup run and the API, so there is exactly one
single-run guard (`running` atomic CAS, dropping overlapping runs) and one status summary.

| Endpoint | Purpose |
|----------|---------|
| `POST /scan` | scan every enabled library |
| `POST /libraries/:id/scan` | one library |
| `GET /scan/status` | current/last run summary |
| `POST /sync` | drift sync (below) |

`collection.Rebuild` runs automatically after every scan and drift sync, so the collection view
stays current without a manual button.

## Skip-unchanged

A file is skipped when its index row is `ok`, its size and mtime are unchanged, **and** the running
app version still matches `ProcessedVersion`. The version gate means an upgrade that changes tag
logic re-processes everything exactly once.

This is what makes repeat scans cheap, and it is also why some state looks stale after a config
change — see the known issue about `correlation_source` in [wip.md](wip.md).

## Drift sync

Catches upstream MusicBrainz changes and re-tags the files a scan would skip.

- `MusicbrainzDueForRefresh` finds expired cache entries.
- `RefreshMusicBrainzRelease` force-fetches and compares the old and new `hashRelease` (sha256 of
  the payload) → changed or not.
- `scan.Runner.SyncDrift` shares the scan run-guard, refreshes what is due, and for changed releases
  re-tags the affected `library_items` from their stored correlation via `TagResolvedFile` plus the
  library's tagger — refreshing each item's on-disk identity afterwards so skip-unchanged stays
  correct.

Surfaced as "Check for updates" on the Activity page, with a `drift_sync` event carrying releases
checked/changed, files re-tagged and errors.

## Activity events

`models.Event` (`type`, `status`, `started_at` as the sort key, `finished_at`, `title`, `summary`,
`details` JSON via a gorm serializer, optional `ref_type`+`ref_id`), with `events.Begin` /
`Finish` / `Prune`. Scans prune to the newest 200.

`GET /events` (filter by type/status, paginated) and `GET /events/:id`. The **Activity** page is a
reverse-chronological feed with status pills, a live scanning banner, a "Scan all" button and a
click-through detail modal (scan stat grid, error-file list, drift detail).

Emitted today: `scan`, `drift_sync`, `lidarr_sync`.

## Caching and rate limits

- The **MusicBrainz release cache lives in the DB** (`musicbrainz_release_caches`), write-through on
  fetch, with a one-time import from the legacy JSON file at startup and a JSON fallback when no DB
  is wired.
- Cache expiry is **jittered 7–14 days** so entries fetched together in one scan do not all expire
  at once.
- The remaining JSON caches (Lidarr artists/albums/track-files, Plex album keys) are loaded once at
  startup, kept warm in memory, and written back in batches — marked dirty and flushed periodically
  and at scan end, instead of rewriting a growing JSON file on every miss.
- All MusicBrainz calls go through `RateLimit()` (1 req/s, global). Respect it when adding new ones.
  AcoustID has its own, separate limiter.
- `FindTrackFileByPath` is cached per artist rather than refetching an artist's whole track-file
  list per track.

## Concurrency

Library scans process files with a bounded worker pool
(`autotaggerr_process_concurrency`, default 4; `1` = serial). Caches, the album-refresh collector
and the scan counters are all concurrency-safe. The 1 req/s MusicBrainz limiter is global, so a
cold-cache scan is still floored by it — concurrency mainly helps the warm-cache steady state,
which is subprocess- and disk-bound.

`processLibraries` is guarded by the runner's CAS plus `jobMu`, which serialises the job body; the
cron job and the startup run share that guard.

## Plex refresh

Changed albums are collected during a scan (album name → Plex key) and `plexClient.RefreshAlbum` is
called for each afterwards. The Plex client is only constructed when its URL + token config is
present and may be `nil` — always nil-check.

## Related

- [media-manager.md](media-manager.md) — the pipeline the scan drives.
- [tagging.md](tagging.md) — what a "changed" file actually gets written.
