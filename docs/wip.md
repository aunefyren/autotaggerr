# Work in progress

Living document for what is **not done yet**: roadmap, ideas, known issues, in-flight work. Add
items as they come up; when one ships, move what is worth keeping into its feature doc and delete
the rest from here.

Shipped features are documented in [media-manager.md](media-manager.md),
[collection.md](collection.md), [attach.md](attach.md), [scanning.md](scanning.md),
[tagging.md](tagging.md), [fingerprinting.md](fingerprinting.md),
[mb-migration.md](mb-migration.md), [mirror.md](mirror.md),
[authentication.md](authentication.md) and [settings.md](settings.md).

## Open work

- **M6 pass E — file import.** Move/copy loose files into the library layout, then hand off to
  manual attach. The last unbuilt piece of the native manager. It has no Activity event, and the
  event ships with the feature rather than before it — but it is not the only verb without one: the
  collection rebuild and the migration stage are silent too, which
  [Activity: one row per thing that happened](#activity-one-row-per-thing-that-happened) covers.
- **Follow has no date cutoff.** "Only future releases" is not implemented, so following always
  pulls the whole back catalogue of the chosen types. A global follow default could layer on later
  without a schema change.
- **Refresh coverage is collection-scoped.** A pass warms artists, release-groups and releases the
  collection already knows about. Artists reached only by browsing still fall back to the
  on-demand path.
- **The `scan` Go package owns the *Process* verb.** The buttons, routes and event types were
  renamed (Process / Scan / Refresh metadata / Tag files — see
  [scanning.md](scanning.md#the-four-verbs-and-why-none-of-them-cascades)) but the package was not,
  so `scan.Runner` is the processing runner and `collection.Rebuild` is the Scan verb. Renaming the
  package is mechanical and touches `main.go`, `routers/` and every file under `scan/`; it was left
  out of the rename to keep that diff reviewable.
- **A running job cannot be cancelled.** Graceful shutdown has shipped (see
  [scanning.md](scanning.md#stopping-on-purpose)): schedules stop, HTTP drains, pending jobs are
  dropped and the job in flight is given 30 seconds. What is missing is the ability to *stop* that
  job — there is no cancellation to thread through a tag write, so a long scan still outlasts the
  grace period and leaves its event to be reconciled on the next boot. A per-job context checked
  between files (where the walk already stops cleanly) would close that gap, and would give file
  work the counterpart to the metadata pass's `POST /mirror/cancel`.

## Activity: one row per thing that happened

In progress. A run used to do six things and report as one row — that row being the *walk's*
counters, so it read as a tagging event while the other five stages went into `details.*` keys
nothing rendered. Every stage is now its own event under the run, the progress bar no longer claims
a position it does not have, and the detail view renders whatever an event declares about itself
rather than a branch per type. What is left is how the relationship reads in the feed.

**Shipped so far:**

- *The bar no longer lies.* Indeterminate outside the phase that owns the counters, on all four
  surfaces that draw them. See [scanning.md](scanning.md#the-bar-belongs-to-one-phase) and the
  Progress component in [style-guide.md](style-guide.md#components).
- *A metadata pass records what it found.* Per-phase tallies and one `EventItem` per changed / gone
  / relinked / failed entity, instead of discarding `Result.ChangedReleases` to a `len()`. See
  [mirror.md](mirror.md#what-a-pass-records-about-itself).
- *Every stage is its own event, under the run.* `Event.ParentID`, seven stage types, the walk's
  counters moved off the run onto `process_files`, the Scan verb emitting at last, and retention
  counting runs rather than rows. See
  [scanning.md](scanning.md#a-run-is-a-parent-its-stages-are-the-rows).
- *The detail view renders what an event declares.* `Event.Stats` replaced the branch-per-type
  chain, counters that name a row status became filter chips over one unified list, `EventItem.Kind`
  separates a file row from an entity row, and each stage row carries its share of the run's time.
  See [scanning.md](scanning.md#an-event-declares-its-own-counters) and the *Event counters* /
  *Stage row* entries in [style-guide.md](style-guide.md#components).
- *The feed shows the cascade.* Runs expand into their stages in place, a running run opens itself,
  and a filtered feed returns stages with the run they belong to named on the row. See
  [scanning.md](scanning.md#browsing-groups-filtering-flattens).
- *Both long lists page.* One `usePaging` + `Pager` in `browse.tsx` serves Activity (server-paged,
  `offset` in the query) and the Collection (client-paged, `offset` as an array index), with the
  page in the URL and any narrowing resetting it. See *Paging* in
  [style-guide.md](style-guide.md#components).
- *The feed filters.* Status chips, a type select and title search, all server-side with facet
  counts so every control states its own result. See
  [scanning.md](scanning.md#browsing-groups-filtering-flattens).

**What is left:**

- **The Items page still cannot answer "what failed?"** for *files*. Activity answers it for events
  now, but the rows on the Items page carry `error`, `last_error_at` and `last_error_transient` —
  exactly the split needed to separate "MusicBrainz was down, this will retry" from "someone has to
  fix this" — and nothing reads them. The transient flag is also what would keep an outage from
  reading as hundreds of broken files. The faceted-filter shape the Activity feed now uses is the
  obvious model.
- **`EventItem` has no index on `status`.** The detail modal filters rows in the browser, which is
  right for the ≤500 a single event holds, but any future "every failed file across every run" view
  would want one.

The **stage timeline** that used to be listed here is largely done as part of the modal work: each
stage row carries a share-of-time bar (`.stagebar`), which answers "which stage ate the four hours"
by scanning down the list. A separate segmented band would be a second way to read the same fact —
worth doing only if the per-row bar turns out not to carry it.

Smaller things this work exposed rather than solved:

- **Retention is still hardcoded** — 200 runs, 500 detail rows per run, 500 entity rows per metadata
  pass. Now that it counts runs the number means something stable, but it could be configurable, and
  time-based retention would suit a feed better than a count.
- **The `refresh` phase covers two operations.** It is set before `CountSupportedFiles` walks every
  root to size the bar, so the phase spans the counting walk (with `Total` still 0) and then the
  due-release refresh. Splitting the count into its own phase is nearly free and makes the first
  minutes of a run legible.
- **A credit change still has no affordance.** `collection_scan` now reports the count, but it is
  still the only identity change with no Migrations row to click through to — the count is the only
  way to notice one, and there is nothing to open.
- **`RetagItems` (the interactive attach flow) opens no event**, so its Plex refresh is top-level
  rather than parented. That is the correct reading of the data, but it means one file-writing path
  is still invisible in the feed.
- **Stage events are not reconciled by name.** `events.ReconcileRunning` marks orphaned `running`
  rows failed at startup, which now includes stages; a crashed run leaves both the run and whatever
  stage was in flight marked failed, which is right, but neither says which stage it died in.

## A failed lookup must not erase what a file is

Shipped. A MusicBrainz outage mid-scan used to empty whole albums: the failed release fetch made
`recordItem` discard a correlation the manager had already resolved, the files left the disk view,
and the album reported `not_indexed` against a manager that could see them fine. Identity, ownership
and the outcome of the last attempt are now three separate facts — see
[collection.md](collection.md#the-disk-view-counts-files-not-successes) for the rule and
[mirror.md](mirror.md#an-expiry-is-not-an-expiry-date) for the stale-cache fallback that stops most
outages reaching the index at all. What is left:

- **The membership queries still filter on `status = OK`.** Seven of them answer "which files belong
  to this artist/album" that way (`collection/paths.go` ×2, `scan/runner.go` ×3,
  `routers/scan_items.go` ×2), feeding retag and the per-artist counts. By the same logic as the
  shipped fix they should follow identity instead: excluding an errored file from a retag is
  precisely what stops it recovering once the cause is fixed. The re-tag path itself now records
  outcomes like the pipeline does ([scanning.md](scanning.md#drift-sync)), so this is the last half
  of that idea — wider diff than the collection fix, so: own pass, own tests.
- **A retry with backoff** inside the fetch (one attempt spaced by the existing `RateLimit()`
  interval) would absorb most single 503s before any of this matters. Kept separate because it
  interacts with the in-flight coalescing and the limiter; the shipped work makes an outage
  *survivable*, which is the part that has to be true regardless.

## Every cache in the database

Shipped. Nothing durable is memory-only, nothing is a JSON file, and the batched flusher is gone —
see [mirror.md](mirror.md#what-is-cached-and-where) and
[mirror.md](mirror.md#the-provider-cache). What is left from that audit:

- **The legacy JSON files are left on disk** after their one-time import. Harmless, and deliberate —
  an import that had deleted its own source would be unrecoverable if it went wrong — but `config/`
  now holds five files nothing reads (`lidarr_{albums,artists,tracks}.json`, `mb_releases.json`,
  `plex_album_keys.json`). Worth a cleanup pass once the import has been in a release long enough to
  trust.

## Frontend follow-ups

The manager-authority boundary is now honoured end to end (see
[collection.md](collection.md#the-ui-under-a-manager)), and so is taking that authority back
([detach](collection.md#detaching-a-manager)). What is left:

- **Re-correlate buttons**, behind a confirm dialog that says it **discards manual pins** and rewrites
  files from Lidarr: per-artist (`POST /artists/:mbid/recorrelate`), per-album on the release-group
  page (`POST /release-groups/:mbid/recorrelate`) and per-library on library settings
  (`POST /libraries/:id/recorrelate`). This is what a user does when Lidarr's answer and the files
  disagree — the remaining action-half endpoint with no UI. Use the shared `ConfirmDialog`
  (`ui.tsx`, `danger`: it discards pins) rather than a new one.
- **A failure filter on the Items page.** The rows carry `error`, `last_error_at` and
  `last_error_transient` — exactly the split needed to separate "MusicBrainz was down, this will
  retry" from "someone has to fix this" — and nothing reads them. There is no way to ask *what
  failed?*, and Activity does not use the transient flag to keep an outage from reading as hundreds
  of broken files.
Surfacing the stages a run hides — the refresh stage, the collection stage, the unrendered
`details.*` keys — is no longer a list of rendering gaps. It is one piece of work; see
[Activity: one row per thing that happened](#activity-one-row-per-thing-that-happened).

## Roadmap / ideas

- **Additional audio formats** (OGG, M4A/AAC, …). Tagging covers FLAC (`metaflac`) and MP3
  (`bogem/id3v2`) only.
- **More MusicBrainz fields** written per track.
- **Write/normalize NFO sidecars** (`album.nfo` / `artist.nfo`). Autotaggerr already holds the full
  MusicBrainz release + artist data while tagging, so it could emit consistent sidecars (single
  `<albumartist>` + MB IDs) for NFO-first players like Jellyfin/Kodi. Open design questions:
  overwrite vs. merge vs. create-if-absent (Jellyfin-generated NFOs carry extra data like
  `<lockdata>`, `<dateadded>`, artwork paths, AudioDB IDs); Kodi-plain vs. Emby/Jellyfin dialect;
  only useful if Jellyfin's NFO *saver* is off, otherwise it rewrites the file.
- **Granular actions beyond the artist.** The three per-artist actions have shipped (see
  [scanning.md](scanning.md)) on a `scan.Scope` built to extend. A release-group or single-album
  scope needs a new constructor and UI, not new machinery — worth doing once the artist actions have
  been used against a real library.
- **Folder structure**
  Mapping to current content, creating folders, renaming and keeping up to date.
  Configurable structure? 
  Link to file importing feature?
- *Does the collection page work with several libraries?*
  The page seems very one dimensional, with dynamic buttons.
  What happens if I have multiple libraries, perhaps either Lidarr or Autotaggerr managed?
  What happens if I have multiple metadata managers? Do the global settings like migrations and such apply correctly?
- *I can add artists on a collection where Lidarr is the only manager*
  Or, at least the button is there.
  Does that make sense?
- **Retrofit the metadata port to AcoustID / artwork.** MusicBrainz fetches now route through
  `metadata.MetadataSource` (see [development.md](development.md#the-coverage-gate)). AcoustID
  (`acoustidBaseURL`) and cover art / fanart (`coverArtArchiveBaseURL`, `fanartBaseURL`) are still
  unexported-base-URL seams stubbable only inside `modules/`; the same port pattern would make their
  callers testable too.
- **Multi user support?**
  Any need?
- **Password reset over email.** The mailer now exists and is proven by the *Send test message*
  button on Settings → Email (see [settings.md](settings.md#email-and-the-one-action-on-this-page)),
  but nothing sends mail on its own. A reset flow is the obvious first real consumer: a signed,
  single-use, expiring token mailed to the address on the account — which means `User.Email` has to
  start being populated and verified, since today it is stored and never used.

## Tagging — what is left

Multi-value tags and the four "match what Lidarr writes" flags are both done; the reference lives
in [tagging.md](tagging.md#several-values-in-one-field). FLAC writes one Vorbis comment per value
unconditionally, MP3 writes ID3v2.4's null-separated form when the profile's `mp3_multi_value_tags`
says so, and the MP3 engine is `bogem/id3v2` rather than ffmpeg. What is still open:

- **`UFID` is reachable but not written.** Picard's canonical home for the recording MBID, and
  `tag.AddUFIDFrame` is right there now that the writer addresses frames directly. Additive, so the
  cost is one extra frame and a one-time write per file.
- **The ISRC frame is an artefact.** It lives in a `TXXX` frame *described* `TXXX`, whose value
  carries its own `ISRC:<value>` packing — the only way the old ffmpeg writer could reach a
  user-defined frame. `TSRC` is the standard frame for it. Both this and `UFID` are one-time
  rewrites of every MP3, so they belong in the same pass.
- **ID3v1 is no longer refreshed.** ffmpeg was passed `-write_id3v1 1` and rewrote the 128-byte
  trailer on every write; bogem does not manage ID3v1, so an existing trailer is preserved verbatim
  and goes stale. Nothing that matters here reads it (it cannot hold an MBID, and every consumer in
  [tagging.md](tagging.md) reads ID3v2), but a file tagged before and after the change disagrees
  with itself for a v1-only reader. Stripping it outright may be better than leaving it.
- **Composer and ASIN are not written at all.** The three dead `FileTags` fields are gone (they were
  hardcoded to `""` and read by neither tag map), so this is now a feature rather than a cleanup:
  MusicBrainz can supply composer via work relations and ASIN on the release. That is a fetch and a
  mapping — a field on `models.FileTags`, a key in both tag maps, and the work-relation include on
  the release fetch.
- **AAC/M4A is where the separator choice starts to matter.** ffmpeg never gained multi-value
  support for MP4, so a delimited single value is the only thing Plex can read there — the MP3
  setting's reasoning applies, and the format work should reuse it rather than re-litigate the
  separator.

## Known issues / limitations

- **Worker-count tuning.** Scan events now carry `mb_lookups` (cache hit / coalesced / fetched), so
  the cost of a run is finally measurable. The default `autotaggerr_process_concurrency` of 4 has
  never been tuned against those numbers, and a separate, higher cap for MP3s than for FLAC may be
  worth it — FLAC rewrites are more disk-bound.

## MusicBrainz entity migration — what is left

The feature has shipped, including release-group pruning, artist identity verification and the
manual sweep; see [mb-migration.md](mb-migration.md). Residual open work:

- **Release-group pruning only runs on a discography sync**, which is per-artist and user-triggered.
  An artist nobody syncs keeps their orphaned rows. The sweep verifies artist *identity* but does
  not prune their groups, because that would mean a discography fetch per artist on top of the
  lookup.
