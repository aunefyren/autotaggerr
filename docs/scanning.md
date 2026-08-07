# Feature: processing, drift sync and activity

How files get processed, why most of them get skipped, how upstream MusicBrainz changes are caught,
and how any of it is visible afterwards.

## The four verbs, and why none of them cascades

A library is acted on by exactly four verbs, each available at whatever scope you point it at:

| Verb | Reads | Writes files | Owner |
| --- | --- | --- | --- |
| **Process** | disk + MusicBrainz | **yes** | `scan.Runner` |
| **Scan** | the local database | no | `collection.Rebuild` |
| **Refresh metadata** | MusicBrainz | no | `mirror.Runner` |
| **Tag files** | the local database | **yes** | `scan.Runner` |

**Process** is the full pipeline the app exists for: walk the folders, resolve each file's
metadata, write its tags. It is the only verb that reads the disk and so the only one that can
discover a file that was added, moved or changed.

**Scan** re-derives what the collection holds from the files *already indexed* — no disk walk, no
network, no file writes. It is the cheapest of the four, and it is what makes the collection view
agree with the index when the two have drifted apart. Processing ends with one, so the button is
for when the view looks stale without a run having happened.

| | artist | library | everything |
| --- | --- | --- | --- |
| Process | `POST /artists/:mbid/process` | `POST /libraries/:id/process` | `POST /process` |
| Scan | `POST /artists/:mbid/scan` | — | `POST /scan` |
| Refresh metadata | `POST /artists/:mbid/refresh` | `POST /libraries/:id/refresh` | `POST /refresh` |
| Tag files | `POST /artists/:mbid/retag` | `POST /libraries/:id/retag` | `POST /retag` |

**Scan has no library scope**, and cannot have a useful one: `owned` is a flag on the
release-group, not a fact per library, so an album held in two libraries is one row. A pass
narrowed to one library would have to read the other libraries' files anyway to avoid clearing a
shared album's disk view — at which point it is the collection-wide pass with extra steps. An
artist owns their release-groups outright, which is why that scope does work. See
[the scoped rebuild](collection.md#scoping-a-rebuild).

Only the file-writing verbs are gated on a running job (`409`). A metadata refresh is not: it
queues alongside, which is the point of keying mutual exclusion on whether a job writes files
rather than on which runner owns it. Scan is not queued at all — it is a database pass measured in
milliseconds, so it answers inline with what it found instead of sending the caller to the
Activity feed.

**No verb triggers another.** Each does what its label says and stops. Two of the four rewrite
the user's audio files, so a button that quietly does more than it claims is the wrong place to be
clever: *Refresh metadata* used to re-tag every file of a release that had changed upstream, which
made a button about reading capable of rewriting hundreds of files.

The cascade is replaced by a handover. A refresh reports which releases changed; the next
processing run re-tags them in its drift stage, and a user who wants it now presses *Tag files*.

This also means the verbs behave identically whether a cron job or a person invoked them — there
is no "scheduled runs do more" mode to explain after the fact when reading the Activity feed.

See [mirror.md](mirror.md) for the refresh verb and the drift stage.

### Where the buttons are

Every scope offers its full set, so the same four words mean the same four things wherever they
appear:

- **Collection** — all four, collection-wide.
- **Artist** — all four, narrowed to that artist.
- **Libraries** — Process, Refresh metadata, Tag files per library (Scan has no library scope).
- **Activity** — *Process all libraries* only. Activity reports work; it is not where work is
  chosen. The one exception earns its place because "nothing has happened yet" is a state that
  page has to answer for.

The word for the full pipeline used to be *Scan*, and *Scan* used to be called *Rebuild from
library*. Runs recorded under the old event type (`scan`) are rewritten to `process` once, at
startup, by `events.MigrateLegacyTypes` — otherwise the feed would file old runs under a verb that
now means something much cheaper.

## The processing runner

`scan.Runner` is shared by the cron job, the startup run and the API. Every background verb —
processing runs, re-tags, and metadata refreshes — is enqueued onto **one serial job queue**
drained by a single worker (see [the queue](#the-job-queue)), so exactly one runs at a time and the
rest are shown as pending rather than dropped. `Status()` reports that queue alongside the last
run's summary.

The Go package is still called `scan`: it owns *Process*, whose middle stage is the folder walk
the package is named for. Renaming it is a follow-up in [wip.md](wip.md), not a silent side effect
of renaming the buttons.

| Endpoint | Purpose |
|----------|---------|
| `POST /process` | process every enabled library |
| `POST /libraries/:id/process` | one library |
| `GET /process/status` | the job queue plus the current/last run summary |
| `POST /refresh` | drift sync (below) |
| `POST /retag` | re-tag every indexed file |
| `POST /artists/:mbid/process` | one artist's folders (below) |
| `POST /artists/:mbid/scan` | re-derive one artist from the index (below) |
| `POST /artists/:mbid/refresh` | one artist's metadata (below) |
| `POST /artists/:mbid/retag` | one artist's files (below) |

`collection.Rebuild` — the *Scan* verb — runs automatically at the end of every processing run and
drift sync, so the collection view stays current without anyone pressing anything.

## Scopes

Every processing run goes through a `scan.Scope`: a title, per-library `Target`s, and any detail worth
recording about what narrowed it. A whole-library run is a scope whose targets have no roots, so a
partial run is not a second code path — it is the same run with fewer folders in it.

The distinction that makes it safe is between the folder **walked** and the folder files are
**resolved against**. `components.ScanLibraryRoots` narrows only the walk; `library.Path` stays the
correlation root, because the path convention the manager reads
(`<root>/<ARTIST>/<ALBUM>/…`) is anchored there. Passing the narrowed folder as the root instead
would make every file one segment too shallow.

A partial run does **not** stamp the library's `last_scan`. It says nothing about the rest of the
library, and letting it bump the timestamp would make a library look freshly processed while most of
it had not been read for weeks.

Scopes narrower than an artist (a release-group, one album folder) need a new constructor, not new
machinery.

### The stages that never see a folder

A processing run is defined by the folders it walks, but two of its phases work from the **index** instead:
the refresh stage picks releases off the metadata cache by TTL, and the
[drift stage](#drift-sync) re-tags every indexed file of a release that changed. Neither has a
path in hand, so for a long time neither consulted the scope at all — pressing *Process* on one artist
force-fetched every expired release in the collection and then rewrote whatever had changed
anywhere in it. The everyday symptom was a log full of writes to artists the user had not asked
about.

`scan.scopeFilter` is what both now go through:

- A scope where **no target names any roots** — a full run, or a whole-library one — admits
  everything, exactly as before. That run is the one that is *supposed* to carry the collection's
  drift; narrowing it would leave releases nothing ever refreshed.
- A **narrowed** scope admits a file only in a library the run covers, and within it only under the
  walked roots (`pathInScope`, the same predicate the prune and force-recorrelate paths use). A
  library named with no roots is in scope in its entirety, so a mixed scope behaves per library.

The refresh stage narrows by resolving the scope's own releases from `library_items` and
intersecting them with the due list (`narrowDue`). That is the cost half of the fix: on a cold
cache every entry on that list is a second of the global MusicBrainz rate limit, which made the
cheapest-looking button in the app quietly the most expensive. If the scope cannot be resolved the
run falls back to the **full** due list — refreshing too much wastes rate limit, refreshing nothing
silently disables the drift detection the stage exists for — and the file writes stay gated by the
same filter regardless, so the fallback cannot widen what gets written.

The drift stage filters **per file, not per release**: the same edition can be held twice, in two
libraries, and only one copy may be in scope. A release out of scope is still counted as checked
and changed, with no files against it — that is true of the metadata whatever folders were walked.

## Per-artist actions

The four verbs on the artist page, all narrowed versions of work the app already does to the whole
collection. They exist because the library is the wrong unit for a fix: noticing one artist is
wrong should not cost a cold run. They differ in what they re-read, cheapest first:

| Action | Walks disk | Hits MusicBrainz | Use when |
|--------|-----------|------------------|----------|
| **Scan** (`scan`) | no | no | the albums shown for this artist look out of date |
| **Tag files** (`retag`) | no | no | the tagger profile changed, or a file was edited outside Autotaggerr |
| **Refresh metadata** (`refresh`) | no | yes, TTL ignored | a release looks wrong upstream, or a new album should have appeared |
| **Process** (`process`) | yes | as needed | files were added, moved or changed on disk |

The three queued ones go through the same [job queue](#the-job-queue) as a collection-wide run,
report through the same `GET /process/status`, and record the same `process` / `tag_files` events
with the artist named in the payload. `scan` is not queued: it re-derives from the index and
answers with what it found (see [the scoped rebuild](collection.md#scoping-a-rebuild)).

`refresh` ignores the cache TTL on purpose. Asking by hand is not helped by "it was checked
recently"; narrowing to one artist is what keeps that affordable within the MusicBrainz rate limit.

A fifth, heavier action — **force re-correlate** (`recorrelate`) — exists for Lidarr-managed files
that diverged from Lidarr's current selection. It is `process` with `Scope.Force`: it busts the Lidarr
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

An artist with no indexed files therefore resolves to nothing, and `process`/`retag` refuse with a 409
rather than reporting a silent zero. `refresh` still runs: there is a catalogue to re-read even
when nothing is owned.

## Skip-unchanged

A file is skipped when its index row is `ok`, its size and mtime are unchanged, the running app
version still matches `ProcessedVersion`, **and** the correlation came from the library's current
manager (`CorrelatedByManager`). This is what makes repeat runs cheap.

Two of those four are deliberate escape hatches, because nothing about the *file* changes when the
app's behaviour does:

- **Version gate** — an upgrade that changes tag logic re-processes everything exactly once.
- **Manager gate** — swapping or disabling a library's manager re-correlates its files, so
  `correlation_source` stops reporting the manager that is no longer in the loop. Without it a
  run walked straight past those files forever.

Both gates cost one full re-process of the library the first time they trip, which is the intended
trade: correctness once, speed thereafter. Adding the manager column has the same effect as a
version bump on first run after upgrading, since existing rows have no manager recorded.

**Pinned items are exempt from the manager gate.** A manual attachment (`routers.saveCorrelation`
sets `pinned`) is not the manager's to redo, and re-correlating one would overwrite the MB IDs the
user chose by hand.

## Pruning files that are gone

`library_items` is keyed by **path**, and a run only ever writes rows for files it finds — so for a
long time nothing removed one. A file that a manager moved, renamed or deleted left its row behind
with `status = ok` and its release still set, and `collection.Rebuild` counts exactly those rows
(`status = ok AND mb_release_id <> ''`, no existence check). The library therefore kept owning albums
it no longer had, under whatever artist the *old* path had resolved to. Lidarr moving an album
between artist folders is the everyday way to produce one.

Nothing self-healed it, either: `collection.ArtistTargets` derives the folder to re-walk *from the
stale path*, and walking a folder that no longer exists yields an empty walk rather than an error, so
every later run confirmed the ghost by never visiting it.

`scan.pruneMissingItems` runs per library at the end of its walk. It is an **existence check over the
index**, not a diff against what the walk visited — deliberately, because the narrowed scopes (one
artist, one release-group) visit a subset of a library and must conclude nothing about the rest of
it. Rows outside the walked roots are never candidates; `pathInScope` is the same predicate the force
re-correlate verb uses.

Three guards are the whole design, because the failure mode is deleting a library's index rather
than a few rows:

- **It runs only after a successful walk.** A walk that failed partway is exactly the case where
  "the file is not there" means "we could not look".
- **Every root must exist.** An unmounted library, or one whose path moved, stats as "every file is
  gone" and would otherwise empty its own index. An unavailable root refuses the whole pass.
- **Only `fs.ErrNotExist` counts as gone.** A permission error, an I/O error or a dead network mount
  leaves the row alone. Absence has to be proven, not assumed — a wrong deletion is the one reading
  that cannot be recovered from.

A **pinned** row is a manual attachment, so pruning one loses authored state. It still goes — the pin
identifies a file that no longer exists — but it is counted and logged separately, because "your
manual attachments went away" is not something a user should have to infer from a file count.

The count rides the run's event as `files_removed` and appends a `· N removed` clause to the summary
line only when something actually went. A move is delete-plus-create to a path-keyed index, so a
moved file loses its pin; carrying pins across a move (matching on size + mtime) would be a separate
feature.

## The collection stage

A run ends by re-deriving the collection and then refreshing the manager mirror —
`collection.Rebuild` followed by `collection.SyncLidarr`, reported as the `collection` phase.

Both now run **before the event is finished**, which is the point. A rebuild that moved an album
between artists is news, and it used to happen after `events.Finish` had already written the run —
so the run that caused it could not report it. The event carries `credit_changes` (see
[collection.md](collection.md#removing-a-credit)) and a `· N credit change(s)` clause on the summary
line when something moved.

The **order is load-bearing**: the mirror only syncs artists that are already collection rows, so an
artist the rebuild just discovered — which is how an album re-credited upstream reaches its new
artist — is in the mirror's scope only once that rebuild has committed. Reversed, every such artist
would wait a full extra cycle for their catalog, looking unmanaged in the meantime.

The mirror had been reachable only from `POST /collection/sync-lidarr`, which meant the catalog block
was stale by default: every disk/catalog comparison on an artist page was against whenever somebody
last pressed a button, and `CatalogChecked` had never been true for an artist nobody had synced by
hand.

**Only full-library runs mirror.** `scopeIsFull` reuses `Target.full`, the same predicate that
decides whether a library may claim it was scanned — a run too narrow to update a library's processed time
is too narrow to be worth a whole-collection mirror. `SyncLidarr` has no scope narrower than "every
Lidarr artist in the collection", so a per-artist or per-release-group button would otherwise wait on
one. The nightly run and the manual button both still cover it.

## Drift sync

Catches upstream MusicBrainz changes and re-tags the files a processing run would skip.

- `MusicbrainzDueForRefresh` finds expired cache entries.
- `RefreshMusicBrainzRelease` force-fetches and compares the old and new `hashRelease` (sha256 of
  the payload) → changed or not.
- `scan.Runner.SyncDrift` enqueues onto the shared [job queue](#the-job-queue), refreshes what is due, and for changed releases
  re-tags the affected `library_items` from their stored correlation via `TagResolvedFile` plus the
  library's tagger — refreshing each item's on-disk identity afterwards so skip-unchanged stays
  correct.
- Inside a processing run, the same re-tag is confined to that run's scope; see
  [the stages that never see a folder](#the-stages-that-never-see-a-folder).

Surfaced as "Check for updates" on the Activity page, with a `tag_files` event carrying releases
checked/changed, files re-tagged and errors.

**A re-tag records its outcome, like any other attempt on a file.** `retagItem` writes the same
three facts `components.recordItem` does: a successful write sets `status = ok` and clears `error`,
`last_error_at` and `last_error_transient`; a failed one records them. It has to, because the
re-tag is frequently *the thing that repairs* a file that failed a scan — a path that updated only
the timestamps left the row reporting a failure that no longer existed, and said nothing at all
when the re-tag itself failed. Two details follow the pipeline exactly: `processed_version` is not
stamped on a failure, so the next run re-attempts the file for free, and a file with no correlation
(an unmatched one) is left alone rather than relabelled as an error.

## Activity events

`models.Event` (`type`, `status`, `started_at` as the sort key, `finished_at`, `title`, `summary`,
`details` JSON via a gorm serializer, optional `ref_type`+`ref_id`), with `events.Begin` /
`Finish` / `Prune`. Processing runs prune to the newest 200.

`GET /events` (filter by type/status, paginated) and `GET /events/:id`. The **Activity** page is a
reverse-chronological feed with status pills, a live banner, a "Process all libraries" button and a
click-through detail modal (run stat grid, per-file detail, drift detail).

**Duration is derived, never stored.** The feed's Duration column and the detail modal both compute
it from the row's own `started_at`/`finished_at`, so every event type has one — including the ones
whose emitter never thought about it, and every row already in the table. `process` and `mb_mirror` used
to also write a `details.duration` string; that was the only reason those two had a figure and the
other five did not, and it has been removed rather than copied into the rest. A running event counts
up in the same format as the banner's elapsed counter; an event with no finish stamp reads `—`.

Emitted today: `process`, `tag_files`, `lidarr_sync`, `mb_mirror`, `mb_migration`, `plex_refresh`,
`health_check`.

**`drift_sync` is a read-only legacy type.** The Tag files verb recorded its events under that name
until the verbs were named apart, which made a Tag files run read as "Metadata sync" — the name of
the verb that was *split out* of it. New runs record `tag_files`; the old rows keep `drift_sync`
rather than being migrated, because a pre-split drift sync genuinely did both jobs and relabelling
it would misreport history. The Activity page renders both with the same stat grid.

- **`plex_refresh`** — one event per run wrapping `flushPlex`, summarising albums refreshed and
  failed (`albums_refreshed` / `albums_failed` / `failed_albums`). One per album would flood the feed
  when a run touches hundreds; one per run keeps it readable. Emitted only when Plex is configured
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

- **Processing runs** keep the live figures in lock-free atomics on the `scan.Runner`, so the per-file callback
  (`WalkAndProcess`'s `onFile`) never contends on the status mutex across the worker pool. `Total` is
  every supported file, counted up front across all targets (`modules.CountSupportedFiles`); `Phase`
  moves through `refresh` → `scanning` → `drift` → `plex` → `migrations` → `collection`; `Current` is the artist
  folder of the most recently started file — a liveness indicator, not a strict cursor under
  concurrency. `Status()` overlays the atomics onto the summary while a run is in flight, so `/process/status`
  and the event row agree.
- **Metadata passes** (`mb_mirror`, including the identity sweep) already tracked `Total`/`Done`/
  `Phase` in `mirror.Summary`; the same flusher mirrors them onto the event row through
  `mirror.Runner.Progress()`, so a sweep that runs for hours shows progress in the feed rather than
  only on `/mirror/status`.
- **`/process/status` reports whichever counters belong to the running job.** The atomics are written by
  processing runs alone, so `Status()` reads them for a processing job and calls `mirror.Runner.Progress()` for a
  refresh one — the same counters the refresh flushes onto its event, which is what stops the status
  banner and that event's row in the feed describing the same pass differently. Every job also clears
  the atomics as it starts (`resetProgress`, in the queue worker and in `RetagItems`), so a job that
  publishes no progress of its own is never drawn with the previous run's finished bar.
- The **Activity** feed polls while any event is running (not only during a run), shows the bar +
  phase + current + elapsed in the banner and inline on running rows; the **Dashboard** and **Artist**
  run widgets show the bar too.

### Per-file detail

Counters say twelve files changed; they never say *which* twelve. `models.EventItem` is one row per
interesting file within an event — path, outcome, tags written, error, and the field-level
`[]TagChange` (`field`, `old`, `new`) stored as JSON on the row.

- **A child table, not more `Details` JSON.** A large library produces tens of thousands of results;
  a single blob per run would be written and read whole, and would grow the event row without
  bound. Rows also let retention cascade — `events.Prune` deletes the detail of the events it drops,
  because nothing in the schema cascades and the capped events table would otherwise sit next to an
  `event_items` table that only grew.
- **Only changed and failed files get a row.** Recording the unchanged majority would multiply the
  table by the size of the library to say "nothing happened", which the counters already say.
- **Bounded per run** at `maxDetailItemsRecorded` (500). Entries past the limit are counted but not
  stored, and the event's `details.detail` block carries `changed_files` / `failed_files` /
  `recorded` / `limit` so the UI can say "showing 500 of 3120" instead of implying 500 was all of it.
- **Collected, not streamed.** `components.DetailCollector` is filled from the run's worker pool
  (mutex-guarded, like `AlbumRefreshSet`) and written in one batch by `events.AddItems` after the run
  — a run would otherwise interleave thousands of small inserts with the tag writes it is timing.
- **The diff comes from the writers.** `SetFlacTags` / `SetMP3Tags` already computed it to decide
  what to write and discarded it; they now return it, and it rides up through `SetFileTags` →
  `TagResolvedFile` → `ProcessFile`. FLAC records per key as each `metaflac` call succeeds; MP3
  derives it from the change set, because the ID3 tag is saved in one pass and there is no
  per-field success to report. That is also why an MP3's `tags_written` can exceed its change count —
  a changed `DISCNUMBER` rewrites its paired `DISCTOTAL`.
- `GET /events/:id` attaches the rows as `items`; the feed never loads them.

Both processing runs and drift syncs record detail. The UI renders it with the same old → new diff language as
the file-tags view (`.diff` / `.diffrow`), so it is learned once.

## Caching and rate limits

- The **MusicBrainz release cache lives in the DB** (`musicbrainz_release_caches`), write-through on
  fetch, with a one-time import from the legacy JSON file at startup.
- Cache expiry is **jittered 7–14 days** so entries fetched together in one run do not all expire
  at once.
- The **Lidarr and Plex caches live in the DB** too (`provider_cache`, one source per endpoint),
  loaded into memory at startup and written through as they are populated. They used to be JSON
  files flushed in batches from inside a run; with no shutdown handler, anything cached outside one
  was lost on restart. Nothing is marked dirty and nothing is flushed any more — see
  [mirror.md](mirror.md#the-provider-cache).
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
- Every run reports what its lookups cost upstream, as `mb_lookups` on the `process` event and in the
  log: served from cache, coalesced onto another goroutine's fetch, or actually fetched. Only the
  last of those pays the limiter, which is what makes a cold-vs-warm comparison meaningful.
- `FindTrackFileByPath` is cached per artist rather than refetching an artist's whole track-file
  list per track.

## Concurrency

Processing runs walk files with a bounded worker pool
(`autotaggerr_process_concurrency`, default 4; `1` = serial). Caches, the album-refresh collector
and the run counters are all concurrency-safe. The MusicBrainz limiter is global, so a cold-cache
run is floored at roughly one *distinct release* per second no matter how many workers run —
concurrency mainly helps the warm-cache steady state, which is subprocess- and disk-bound. Fetch
coalescing (above) is what makes that floor "per release" rather than "per release, times the number
of workers that happened to start on the same album".

That floor only binds on a *cold* cache. Measured against a large personal library, a full run went
from ~7 hours to ~14 minutes once the release cache was warm — the steady state is subprocess- and
disk-bound, not rate-limited, which is what the worker pool and fetch coalescing are for. A cold
first run is still paced by MusicBrainz, and a local mirror is the only way around that.

## The job queue

Every background verb the runner exposes — `RunAll` / `RunLibrary` / `RunArtist` (processing),
`RetagAll` / `RetagLibrary` / `RetagArtist` (re-tags), `SyncDrift` / `VerifyIdentities` / `RefreshArtist` /
`RefreshLibrary` (metadata refreshes) — is now an **enqueue**, not an inline run. A single worker
goroutine (`scan/queue.go`, started in `NewRunner`) drains the queue one job at a time, holding
`jobMu` for the whole of each. This replaced the old "atomic CAS that dropped overlapping runs",
which is what left a user's second run silently vanishing and, after a crash, events stuck at
`running` forever (the latter is also swept on startup — see [reconciliation](#restart-reconciliation)).

- **Dedup.** Enqueuing a `key` already running or pending is a no-op, so a restart storm or a
  double-click cannot stack redundant runs. Keys are per-scope: `process_all`, `process_library:<id>`,
  `process_artist:<mbid>`, `retag_all`, `refresh_all`, and so on.
- **Priority.** File-writing jobs (processing, re-tags) slot ahead of pending metadata jobs, so a run a
  user asked for is not stuck behind a hours-long refresh — but a job already *running* is never
  preempted. Cancelling a running metadata pass (`POST /mirror/cancel`) is the way to jump it.
- **Serialisation replaces the yield.** Because nothing overlaps any more, the metadata runner drops
  its old cooperative "yield to file work" dance (`mirror.NewRunner` is now wired with a nil
  `yieldTo`). That also removes a latent self-deadlock: a run's own inline refresh used to wait on
  the running flag the run itself held.
- **Interactive re-tags stay synchronous.** `RetagItems` (the attach flow) must return per-file
  results to its HTTP caller, so it is not queued; it `TryLock`s `jobMu` and refuses immediately if a
  background job holds it, rather than blocking the request behind a job that could run for hours.
- **API.** Trigger endpoints no longer 409 on "already running" — they enqueue and return `202`
  (`processing queued`, etc.). The remaining 409s are "nothing to process / no indexed files" refusals, which
  are unchanged. `Status()` carries `current_job` and `queue`, which the Activity page renders as a
  live banner plus a pending list.

Within a single run, files are still processed by a bounded worker pool
(`autotaggerr_process_concurrency`); the queue serialises *jobs*, the pool parallelises *files inside
a job*.

## Stopping on purpose

`main.shutdown` waits on `SIGINT`/`SIGTERM` and then stops the process in an order chosen so that
stopping means something:

1. **`settings.Runtime.Stop`** cancels every cron task, so nothing new fires into a process that is
   leaving.
2. **`http.Server.Shutdown`** stops accepting requests and waits for the ones in flight — which
   includes the synchronous re-tags (`RetagItems`), the only file writes that happen outside the
   queue.
3. **`scan.Runner.Shutdown`** stops the queue and waits for the job already executing.

The third step is deliberately *not* `Wait`. Draining the whole queue would hold the process open
for however long a full scan queued behind the current job takes; the pending jobs have started
nothing, hold no event, and every verb here is re-runnable, so they are dropped and logged. Only the
running job is given time — it has written files and opened an event. From `stopping` onward the
queue refuses new work rather than accepting it to drop it seconds later.

Nothing is cancelled mid-job. There is no cancellation to thread through a tag write, and
interrupting one is how a file ends up half-written, so a job that outlasts `shutdownGrace`
(30 seconds) is left to the process exiting under it. That is the crash case, which the next boot
already repairs — see below.

## Restart reconciliation

`events.ReconcileRunning` runs once at startup, before any schedule or auto-start fires, and marks
every event still in the `running` state as failed ("interrupted — the service restarted"). A running
event whose process is gone can never finish itself, so without this an interrupted run or sweep
shows as running in the feed forever. It is startup-only by contract: run later, it could not tell a
previous process's orphan from a job this process just began.

It remains the safety net for a kill or a crash. A graceful stop no longer needs it: the running job
finishes and closes its own event, and only a job that outran the grace period leaves an orphan.

## Plex refresh

Changed albums are collected during a run (album name → Plex key) and `plexClient.RefreshAlbum` is
called for each afterwards. The Plex client is only constructed when its URL + token config is
present and may be `nil` — always nil-check.

## Related

- [media-manager.md](media-manager.md) — the pipeline processing drives.
- [tagging.md](tagging.md) — what a "changed" file actually gets written.
