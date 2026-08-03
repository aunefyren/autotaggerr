# Feature: scanning, drift sync and activity

How files get processed, why most of them get skipped, how upstream MusicBrainz changes are caught,
and how any of it is visible afterwards.

## The three verbs, and why none of them cascades

A library is acted on by exactly three verbs, each available at whatever scope you point it at
(one artist, one library, everything):

| Verb | Reads | Writes files | Owner |
| --- | --- | --- | --- |
| **Scan** | disk + MusicBrainz | **yes** | `scan.Runner` |
| **Refresh metadata** | MusicBrainz | no | `mirror.Runner` |
| **Tag files** | the local database | **yes** | `scan.Runner` |

All three are available at all three scopes:

| | artist | library | everything |
| --- | --- | --- | --- |
| Scan | `POST /artists/:mbid/scan` | `POST /libraries/:id/scan` | `POST /scan` |
| Refresh metadata | `POST /artists/:mbid/refresh` | `POST /libraries/:id/refresh` | `POST /mirror/sync` |
| Tag files | `POST /artists/:mbid/retag` | `POST /libraries/:id/retag` | — |

Only the file-writing verbs are gated on a running scan (`409`). A metadata refresh is not: it
runs alongside and yields at entity boundaries, which is the point of keying mutual exclusion on
whether a job writes files rather than on which runner owns it.

**No verb triggers another.** Each does what its label says and stops. Two of the three rewrite
the user's audio files, so a button that quietly does more than it claims is the wrong place to be
clever: *Refresh metadata* used to re-tag every file of a release that had changed upstream, which
made a button about reading capable of rewriting hundreds of files.

The cascade is replaced by a handover. A refresh reports which releases changed; the scan re-tags
them in its drift stage, and a user who wants it now presses *Tag files*.

This also means the verbs behave identically whether a cron job or a person invoked them — there
is no "scheduled runs do more" mode to explain after the fact when reading the Activity feed.

See [mirror.md](mirror.md) for the refresh verb and the scan's drift stage.


## The scan runner

`scan.Runner` is shared by the cron job, the startup run and the API. Every background verb — scans,
re-tags, and metadata refreshes — is enqueued onto **one serial job queue** drained by a single
worker (see [the queue](#the-job-queue)), so exactly one runs at a time and the rest are shown as
pending rather than dropped. `Status()` reports that queue alongside the last run's summary.

| Endpoint | Purpose |
|----------|---------|
| `POST /scan` | scan every enabled library |
| `POST /libraries/:id/scan` | one library |
| `GET /scan/status` | current/last run summary |
| `POST /sync` | drift sync (below) |
| `POST /artists/:mbid/scan` | one artist's folders (below) |
| `POST /artists/:mbid/refresh` | one artist's metadata (below) |
| `POST /artists/:mbid/retag` | one artist's files (below) |

`collection.Rebuild` runs automatically after every scan and drift sync, so the collection view
stays current without a manual button.

## Scopes

Every scan goes through a `scan.Scope`: a title, per-library `Target`s, and any detail worth
recording about what narrowed it. A whole-library run is a scope whose targets have no roots, so a
partial scan is not a second code path — it is the same run with fewer folders in it.

The distinction that makes it safe is between the folder **walked** and the folder files are
**resolved against**. `components.ScanLibraryRoots` narrows only the walk; `library.Path` stays the
correlation root, because the path convention the manager reads
(`<root>/<ARTIST>/<ALBUM>/…`) is anchored there. Passing the narrowed folder as the root instead
would make every file one segment too shallow.

A partial scan does **not** stamp the library's `last_scan`. It says nothing about the rest of the
library, and letting it bump the timestamp would make a library look freshly scanned while most of
it had not been read for weeks.

Scopes narrower than an artist (a release-group, one album folder) need a new constructor, not new
machinery.

## Per-artist actions

Three commands on the artist page, all narrowed versions of work the app already does to a whole
library. They exist because the library is the wrong unit for a fix: noticing one artist is wrong
should not cost a cold scan. They differ in what they re-read, cheapest first:

| Action | Walks disk | Hits MusicBrainz | Use when |
|--------|-----------|------------------|----------|
| **Tag files** (`retag`) | no | no | the tagger profile changed, or a file was edited outside Autotaggerr |
| **Refresh metadata** (`refresh`) | no | yes, TTL ignored | a release looks wrong upstream, or a new album should have appeared |
| **Scan** (`scan`) | yes | as needed | files were added, moved or changed on disk |

All three go through the same [job queue](#the-job-queue) as a full scan, report through the same
`GET /scan/status`, and record the same `scan` / `drift_sync` events with the artist named in the
payload.

`refresh` ignores the cache TTL on purpose. Asking by hand is not helped by "it was checked
recently"; narrowing to one artist is what keeps that affordable within the MusicBrainz rate limit.

A fourth, heavier action — **force re-correlate** (`recorrelate`) — exists for Lidarr-managed files
that diverged from Lidarr's current selection. It is `scan` with `Scope.Force`: it busts the Lidarr
caches, clears `Pinned` on Lidarr-governed items in scope, and re-walks **ignoring skip-unchanged**,
so a healthy `ok` item whose upstream release changed is actually rewritten. Scoped per artist,
release-group (`collection.ReleaseGroupTargets`, narrowed to album/disc folders so one album's repair
does not re-walk the discography) or whole library, via
`POST /{artists|release-groups}/:mbid/recorrelate` and `POST /libraries/:id/recorrelate`. See
[collection.md](collection.md#manager-authority--lidarr-owns-identity) for why it is needed.

### Finding an artist on disk

Nothing stores an artist → file link — `library_items` know their release, not who made it — so
`collection.ArtistTargets` derives it:

```
artist -> release-groups (credit links, so collaborations count)
       -> owned editions (collection_releases)
       -> files          (library_items.mb_release_id)
       -> folders        (the artist segment of each file's path, via
                          utilities.ExtractArtistNameFromTrackFilePath)
```

Derived is the right answer, not a shortcut: a stored column would need backfilling, would go stale
the moment a file is re-attached to a different release, and still would not know about the second
artist on a collaboration.

The folder step **reads** the artist segment from real files rather than guessing it from the
artist's name — the folder is whatever the user called it (`Beatles, The`), which no amount of
normalising a MusicBrainz name reproduces. An artist split across two libraries yields one target
per library; a whole discography under one folder collapses to a single walk.

An artist with no indexed files therefore resolves to nothing, and `scan`/`retag` refuse with a 409
rather than reporting a silent zero. `refresh` still runs: there is a catalogue to re-read even
when nothing is owned.

## Skip-unchanged

A file is skipped when its index row is `ok`, its size and mtime are unchanged, the running app
version still matches `ProcessedVersion`, **and** the correlation came from the library's current
manager (`CorrelatedByManager`). This is what makes repeat scans cheap.

Two of those four are deliberate escape hatches, because nothing about the *file* changes when the
app's behaviour does:

- **Version gate** — an upgrade that changes tag logic re-processes everything exactly once.
- **Manager gate** — swapping or disabling a library's manager re-correlates its files, so
  `correlation_source` stops reporting the manager that is no longer in the loop. Without it a
  scan walked straight past those files forever.

Both gates cost one full re-process of the library the first time they trip, which is the intended
trade: correctness once, speed thereafter. Adding the manager column has the same effect as a
version bump on first run after upgrading, since existing rows have no manager recorded.

**Pinned items are exempt from the manager gate.** A manual attachment (`routers.saveCorrelation`
sets `pinned`) is not the manager's to redo, and re-correlating one would overwrite the MB IDs the
user chose by hand.

## Drift sync

Catches upstream MusicBrainz changes and re-tags the files a scan would skip.

- `MusicbrainzDueForRefresh` finds expired cache entries.
- `RefreshMusicBrainzRelease` force-fetches and compares the old and new `hashRelease` (sha256 of
  the payload) → changed or not.
- `scan.Runner.SyncDrift` enqueues onto the shared [job queue](#the-job-queue), refreshes what is due, and for changed releases
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
click-through detail modal (scan stat grid, per-file detail, drift detail).

Emitted today: `scan`, `drift_sync`, `lidarr_sync`, `mb_mirror`, `mb_migration`, `plex_refresh`,
`health_check`.

- **`plex_refresh`** — one event per run wrapping `flushPlex`, summarising albums refreshed and
  failed (`albums_refreshed` / `albums_failed` / `failed_albums`). One per album would flood the feed
  when a scan touches hundreds; one per run keeps it readable. Emitted only when Plex is configured
  and the run actually touched an album.
- **`health_check`** — the configured Lidarr/Plex connections, probed at startup and on a cron
  (`autotaggerr_health_cron_schedule`, default every five minutes). Recorded **only when a
  connection's health changes** (with a baseline on the first check per process): a frequent cadence
  otherwise buries the feed under identical "healthy" rows. `details.services` carries per-connection
  `healthy` + `error`. Lives in `health.Checker`, which holds the last-seen state to gate the write.

### Live progress

Long events carry live progress so the feed can draw a bar instead of an indefinite "running":
`Event.Total` / `Done` / `Phase` / `Current` (`current_item` column). They are written on a throttled
ticker by `events.StartProgress`, which **polls** a snapshot of the job's own counters every two
seconds — the per-item hot path never touches the database — and lands a final snapshot on `stop()`,
called before `Finish` so its `Save` keeps the values rather than racing them.

- **Scans** keep the live figures in lock-free atomics on the `scan.Runner`, so the per-file callback
  (`WalkAndProcess`'s `onFile`) never contends on the status mutex across the worker pool. `Total` is
  every supported file, counted up front across all targets (`modules.CountSupportedFiles`); `Phase`
  moves through `refresh` → `scanning` → `drift` → `plex` → `migrations`; `Current` is the artist
  folder of the most recently started file — a liveness indicator, not a strict cursor under
  concurrency. `Status()` overlays the atomics onto the summary while a scan runs, so `/scan/status`
  and the event row agree.
- **Metadata passes** (`mb_mirror`, including the identity sweep) already tracked `Total`/`Done`/
  `Phase` in `mirror.Summary`; the same flusher now mirrors them onto the event row, so a sweep that
  runs for hours shows progress in the feed rather than only on `/mirror/status`.
- The **Activity** feed polls while any event is running (not only during a scan), shows the bar +
  phase + current + elapsed in the banner and inline on running rows; the **Dashboard** and **Artist**
  scan widgets show the bar too.

### Per-file detail

Counters say twelve files changed; they never say *which* twelve. `models.EventItem` is one row per
interesting file within an event — path, outcome, tags written, error, and the field-level
`[]TagChange` (`field`, `old`, `new`) stored as JSON on the row.

- **A child table, not more `Details` JSON.** A large library produces tens of thousands of results;
  a single blob per scan would be written and read whole, and would grow the event row without
  bound. Rows also let retention cascade — `events.Prune` deletes the detail of the events it drops,
  because nothing in the schema cascades and the capped events table would otherwise sit next to an
  `event_items` table that only grew.
- **Only changed and failed files get a row.** Recording the unchanged majority would multiply the
  table by the size of the library to say "nothing happened", which the counters already say.
- **Bounded per run** at `maxDetailItemsRecorded` (500). Entries past the limit are counted but not
  stored, and the event's `details.detail` block carries `changed_files` / `failed_files` /
  `recorded` / `limit` so the UI can say "showing 500 of 3120" instead of implying 500 was all of it.
- **Collected, not streamed.** `components.DetailCollector` is filled from the scan's worker pool
  (mutex-guarded, like `AlbumRefreshSet`) and written in one batch by `events.AddItems` after the run
  — a scan would otherwise interleave thousands of small inserts with the tag writes it is timing.
- **The diff comes from the writers.** `SetFlacTags` / `SetMP3Tags` already computed it to decide
  what to write and discarded it; they now return it, and it rides up through `SetFileTags` →
  `TagResolvedFile` → `ProcessFile`. FLAC records per key as each `metaflac` call succeeds; MP3
  derives it from the change set, because ffmpeg rewrites the file in one pass and there is no
  per-field success to report. That is also why an MP3's `tags_written` can exceed its change count —
  a changed `DISCNUMBER` rewrites its paired `DISCTOTAL`.
- `GET /events/:id` attaches the rows as `items`; the feed never loads them.

Both scans and drift syncs record detail. The UI renders it with the same old → new diff language as
the file-tags view (`.diff` / `.diffrow`), so it is learned once.

## Caching and rate limits

- The **MusicBrainz release cache lives in the DB** (`musicbrainz_release_caches`), write-through on
  fetch, with a one-time import from the legacy JSON file at startup and a JSON fallback when no DB
  is wired.
- Cache expiry is **jittered 7–14 days** so entries fetched together in one scan do not all expire
  at once.
- The remaining JSON caches (Lidarr artists/albums/track-files, Plex album keys) are loaded once at
  startup, kept warm in memory, and written back in batches — marked dirty and flushed periodically
  and at scan end, instead of rewriting a growing JSON file on every miss.
- **Concurrent fetches of the same release are coalesced.** The cache is only written once a fetch
  completes, so several workers starting on tracks of the same album all miss it and would each
  issue the same request — every one of them serialized behind the global limiter. An album's tracks
  are adjacent in walk order, so this was the common case, not a rare one:
  `GetMusicBrainzRelease` now keeps an in-flight map keyed by release MBID, and the first caller
  fetches while the rest wait on its result. A failed fetch is not cached, so the next call retries.
- All MusicBrainz calls go through `RateLimit()` (default 1 req/s, global). Respect it when adding
  new ones. AcoustID has its own, separate limiter.
- The rate is **read from the MusicBrainz data source row's `rate_limit`** by
  `components.ApplyDataSourceRateLimits`, applied at startup and again when the source is edited
  (no restart needed). A non-positive value is ignored rather than treated as "unlimited", so a
  blank config cannot remove the throttle. Raising it above ~1 req/s is only appropriate against a
  local MusicBrainz mirror — the public service will start returning 503s.
- Every scan reports what its lookups cost upstream, as `mb_lookups` on the `scan` event and in the
  log: served from cache, coalesced onto another goroutine's fetch, or actually fetched. Only the
  last of those pays the limiter, which is what makes a cold-vs-warm comparison meaningful.
- `FindTrackFileByPath` is cached per artist rather than refetching an artist's whole track-file
  list per track.

## Concurrency

Library scans process files with a bounded worker pool
(`autotaggerr_process_concurrency`, default 4; `1` = serial). Caches, the album-refresh collector
and the scan counters are all concurrency-safe. The MusicBrainz limiter is global, so a cold-cache
scan is floored at roughly one *distinct release* per second no matter how many workers run —
concurrency mainly helps the warm-cache steady state, which is subprocess- and disk-bound. Fetch
coalescing (above) is what makes that floor "per release" rather than "per release, times the number
of workers that happened to start on the same album".

That floor only binds on a *cold* cache. Measured against a large personal library, a full scan went
from ~7 hours to ~14 minutes once the release cache was warm — the steady state is subprocess- and
disk-bound, not rate-limited, which is what the worker pool and fetch coalescing are for. A cold
first scan is still paced by MusicBrainz, and a local mirror is the only way around that.

## The job queue

Every background verb the runner exposes — `RunAll` / `RunLibrary` / `RunArtist` (scans),
`RetagLibrary` / `RetagArtist` (re-tags), `SyncDrift` / `VerifyIdentities` / `RefreshArtist` /
`RefreshLibrary` (metadata refreshes) — is now an **enqueue**, not an inline run. A single worker
goroutine (`scan/queue.go`, started in `NewRunner`) drains the queue one job at a time, holding
`jobMu` for the whole of each. This replaced the old "atomic CAS that dropped overlapping runs",
which is what left a user's second scan silently vanishing and, after a crash, events stuck at
`running` forever (the latter is also swept on startup — see [reconciliation](#restart-reconciliation)).

- **Dedup.** Enqueuing a `key` already running or pending is a no-op, so a restart storm or a
  double-click cannot stack redundant runs. Keys are per-scope: `scan_all`, `scan_library:<id>`,
  `scan_artist:<mbid>`, `refresh_all`, and so on.
- **Priority.** File-writing jobs (scans, re-tags) slot ahead of pending metadata jobs, so a scan a
  user asked for is not stuck behind a hours-long refresh — but a job already *running* is never
  preempted. Cancelling a running metadata pass (`POST /mirror/cancel`) is the way to jump it.
- **Serialisation replaces the yield.** Because nothing overlaps any more, the metadata runner drops
  its old cooperative "yield to file work" dance (`mirror.NewRunner` is now wired with a nil
  `yieldTo`). That also removes a latent self-deadlock: a scan's own inline refresh used to wait on
  the running flag the scan itself held.
- **Interactive re-tags stay synchronous.** `RetagItems` (the attach flow) must return per-file
  results to its HTTP caller, so it is not queued; it `TryLock`s `jobMu` and refuses immediately if a
  background job holds it, rather than blocking the request behind a job that could run for hours.
- **API.** Trigger endpoints no longer 409 on "already running" — they enqueue and return `202`
  (`scan queued`, etc.). The remaining 409s are "nothing to scan / no indexed files" refusals, which
  are unchanged. `Status()` carries `current_job` and `queue`, which the Activity page renders as a
  live banner plus a pending list.

Within a single scan, files are still processed by a bounded worker pool
(`autotaggerr_process_concurrency`); the queue serialises *jobs*, the pool parallelises *files inside
a job*.

## Restart reconciliation

`events.ReconcileRunning` runs once at startup, before any schedule or auto-start fires, and marks
every event still in the `running` state as failed ("interrupted — the service restarted"). A running
event whose process is gone can never finish itself, so without this an interrupted scan or sweep
shows as running in the feed forever. It is startup-only by contract: run later, it could not tell a
previous process's orphan from a job this process just began.

## Plex refresh

Changed albums are collected during a scan (album name → Plex key) and `plexClient.RefreshAlbum` is
called for each afterwards. The Plex client is only constructed when its URL + token config is
present and may be `nil` — always nil-check.

## Related

- [media-manager.md](media-manager.md) — the pipeline the scan drives.
- [tagging.md](tagging.md) — what a "changed" file actually gets written.
