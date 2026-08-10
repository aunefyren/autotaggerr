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

- **A verb that does nothing cannot say why.** On an empty install *Scan* answers
  `0 artists, 0 albums` and *Sync from Lidarr* records `0 artists synced · 0 albums`, and neither
  states its cause. The mirror's is the worse one: `SyncLidarrWith` returns before making a single
  HTTP call when no collection artist is `managed_by` lidarr/mixed, so "there was nothing here to
  mirror" and "Lidarr was asked and had nothing" render as the same Activity row. Each verb reads the
  output of the one before it (Process → `library_items` → Scan → collection rows → Sync → catalog
  columns), so on a cold install only Process has an input, and the other two are honest zeroes that
  read as duds. The fix is for each to report which input was empty — as the inline result for Scan
  and as the event summary for the sync — and to disable the buttons with a title saying what is
  needed first. `no_edition` is the precedent: it exists so "the counts disagree because nobody chose
  an edition" cannot read as "the manager is stale". Same idea, one level up.
- **What failed?** cannot be asked about *files*. Activity answers it for events, but the rows on the
  Items page carry `error`, `last_error_at` and `last_error_transient` — exactly the split needed to
  separate "MusicBrainz was down, this will retry" from "someone has to fix this" — and nothing reads
  them, in the API or the UI. The transient flag is also what would keep an outage from reading as
  hundreds of broken files, in Activity as well as on Items. The faceted-filter shape the Activity
  feed now uses is the obvious model.
- **An unmatched file keeps its identity and is dropped from the disk view anyway.** `recordItem`
  preserves `mb_release_id` through a failure (`components/pipeline.go:391`) — Autotaggerr still
  knows exactly which release those files are — but `ownedItemRows` excludes `unmatched`, so a
  Lidarr hiccup empties the album from the collection regardless. This is deliberate and documented
  ([collection.md](collection.md#the-disk-view-counts-files-not-successes)): under a manager,
  "unmatched" means the authority does not know the file, and it should leave the collection. The
  repair verbs no longer suffer for it — folder resolution admits these files
  ([collection.md](collection.md#the-same-rule-applied-to-membership)) — so what is left is the
  *collection* half: the transient Lidarr case has exactly the shape the MusicBrainz fix was written
  for, and an hour-old cache is not the manager changing its mind. `last_error_transient` already
  exists to carry that distinction and nothing reads it.
- **A retry with backoff** inside the MusicBrainz fetch (one attempt spaced by the existing
  `RateLimit()` interval) would absorb most single 503s before any of the above matters. Kept
  separate because it interacts with the in-flight coalescing and the limiter; the shipped work makes
  an outage *survivable*, which is the part that has to be true regardless.
- **The Lidarr mirror cannot introduce an artist**, so it cannot populate a cold collection — the
  button a Lidarr-first user reaches for first is the one that can do nothing. `CollectionArtist`
  rows do not require files (*Add artist* creates one), so nothing structural forbids it; it is a
  decision about what the collection means, since importing from Lidarr would fill "present vs
  wanted" with artists no file has ever been seen for. If it happens it belongs behind its own option
  on the sync dialog or a separate *Import artists from Lidarr* action, not as a change to what the
  plain Sync button does.
- **M6 pass E — file import.** Move/copy loose files into the library layout, then hand off to
  manual attach. The last unbuilt piece of the native manager. It has no Activity event, and the
  event ships with the feature rather than before it — every other verb has one now, so an import
  that reports nothing would be the only silent thing in the feed.
- **Follow has no date cutoff.** "Only future releases" is not implemented, so following always
  pulls the whole back catalogue of the chosen types. A global follow default could layer on later
  without a schema change.
- **Refresh coverage is collection-scoped.** A pass warms artists, release-groups and releases the
  collection already knows about. Artists reached only by browsing still fall back to the
  on-demand path.
- **A running job cannot be cancelled.** Graceful shutdown has shipped (see
  [scanning.md](scanning.md#stopping-on-purpose)): schedules stop, HTTP drains, pending jobs are
  dropped and the job in flight is given 30 seconds. What is missing is the ability to *stop* that
  job — there is no cancellation to thread through a tag write, so a long scan still outlasts the
  grace period and leaves its event to be reconciled on the next boot. A per-job context checked
  between files (where the walk already stops cleanly) would close that gap, and would give file
  work the counterpart to the metadata pass's `POST /mirror/cancel`.
- **`RetagItems` (the interactive attach flow) opens no event**, so its Plex refresh is top-level
  rather than parented. That is the correct reading of the data, but it means one file-writing path
  is still invisible in the feed.
- **A credit change still has no affordance.** `collection_scan` reports the count, but it is still
  the only identity change with no Migrations row to click through to — the count is the only way to
  notice one, and there is nothing to open.
- **Retention is a count, not a duration.** The two figures are configurable now
  (`autotaggerr_event_retention` / `autotaggerr_event_detail_retention`, see
  [scanning.md](scanning.md#a-run-spawns-activities-each-one-is-a-row)), but time-based retention
  would suit a feed better than a count — "keep 90 days" is what someone actually wants, and a
  count means a busy week silently evicts a quiet month. Note that a tagging activity can exceed
  the per-event limit by design: the drift rows are adopted whole (`DetailCollector.Adopt`) so a
  big walk cannot starve them.

## Tagging — what is left

Multi-value tags and the four "match what Lidarr writes" flags are both done; the reference lives
in [tagging.md](tagging.md#several-values-in-one-field). FLAC writes one Vorbis comment per value
unconditionally, MP3 writes ID3v2.4's null-separated form when the profile's `mp3_multi_value_tags`
says so, and the MP3 engine is `bogem/id3v2` rather than ffmpeg. `UFID` and `TSRC` have shipped as
one pass — see [tagging.md](tagging.md#the-recording-mbid-is-written-twice-on-purpose). What is
still open:

- **Composer and ASIN are not written at all.** The three dead `FileTags` fields are gone (they were
  hardcoded to `""` and read by neither tag map), so this is now a feature rather than a cleanup:
  MusicBrainz can supply composer via work relations and ASIN on the release. That is a fetch and a
  mapping — a field on `models.FileTags`, a key in both tag maps, and the work-relation include on
  the release fetch.
- **AAC/M4A is where the separator choice starts to matter.** ffmpeg never gained multi-value
  support for MP4, so a delimited single value is the only thing Plex can read there — the MP3
  setting's reasoning applies, and the format work should reuse it rather than re-litigate the
  separator.

## MusicBrainz entity migration — what is left

The feature has shipped, including release-group pruning, artist identity verification and the
manual sweep; see [mb-migration.md](mb-migration.md). Residual open work:

- **Release-group pruning only runs on a discography sync**, which is per-artist and user-triggered.
  An artist nobody syncs keeps their orphaned rows. The sweep verifies artist *identity* but does
  not prune their groups, because that would mean a discography fetch per artist on top of the
  lookup.

## Known issues / limitations

- **Worker-count tuning.** Scan events now carry `mb_lookups` (cache hit / coalesced / fetched), so
  the cost of a run is finally measurable. The default `autotaggerr_process_concurrency` of 4 has
  never been tuned against those numbers, and a separate, higher cap for MP3s than for FLAC may be
  worth it — FLAC rewrites are more disk-bound.
- **`FindTrackFileByPath` wants a real multi-disc fixture.** A production soundtrack (Jerry
  Goldsmith's *Alien*, 30 + 17 tracks over `CD 01`/`CD 02`) carries the same basename on both discs
  *and* a typographic apostrophe in others. Both resolve correctly today — disc numbers disambiguate
  the first, `Canon`'s NFC pass the second — but nothing in the suite pins either, and both are one
  careless change away from silently matching the wrong disc. Better than the synthetic fixture
  already there, because it is shaped like a real release rather than like the test.

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
  [scanning.md](scanning.md)) on a `process.Scope` built to extend. A release-group or single-album
  scope needs a new constructor and UI, not new machinery — worth doing once the artist actions have
  been used against a real library.
- **Folder structure.** Mapping to current content, creating folders, renaming and keeping up to
  date. Configurable structure? Links to the file-import feature above.
- *Does the collection page work with several libraries?* The page seems very one-dimensional, with
  dynamic buttons. What happens with multiple libraries, some Lidarr- and some Autotaggerr-managed?
  With multiple metadata managers — do the global settings like migrations apply correctly?
- *I can add artists on a collection where Lidarr is the only manager.* Or, at least, the button is
  there. Does that make sense?
- **Retrofit the metadata port to AcoustID / artwork.** MusicBrainz fetches now route through
  `metadata.MetadataSource` (see [development.md](development.md#the-coverage-gate)). AcoustID
  (`acoustidBaseURL`) and cover art / fanart (`coverArtArchiveBaseURL`, `fanartBaseURL`) are still
  unexported-base-URL seams stubbable only inside `modules/`; the same port pattern would make their
  callers testable too.
- **Multi user support?** Any need?
- **Password reset over email.** The mailer now exists and is proven by the *Send test message*
  button on Settings → Email (see [settings.md](settings.md#email-and-the-one-action-on-this-page)),
  but nothing sends mail on its own. A reset flow is the obvious first real consumer: a signed,
  single-use, expiring token mailed to the address on the account — which means `User.Email` has to
  start being populated and verified, since today it is stored and never used.
