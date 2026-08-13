# Work in progress

Living document for what is **not done yet**: roadmap, ideas, known issues, in-flight work. Add
items as they come up; when one ships, move what is worth keeping into its feature doc and delete
the rest from here.

Shipped features are documented in [media-manager.md](media-manager.md),
[collection.md](collection.md), [attach.md](attach.md), [scanning.md](scanning.md),
[tagging.md](tagging.md), [fingerprinting.md](fingerprinting.md),
[mb-migration.md](mb-migration.md), [mirror.md](mirror.md), [artwork.md](artwork.md),
[authentication.md](authentication.md) and [settings.md](settings.md).

## Open work

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
- **The follow cutoff is per artist only.** `follow_from_year` is set on each artist
  ([collection.md](collection.md#following-can-start-at-a-year)); a *global* default — "new artists
  I follow should start from now" — would layer on top of it without a schema change, read by the
  follow control when an artist has no cutoff of its own. It does **not** belong in `config.json`:
  that file is process config, and a default about what a collection wants is exactly the kind of
  key that was just removed from it (see [settings.md](settings.md#every-key-on-this-page-is-a-key-the-process-reads)).
  A row on whatever holds collection-wide policy is the right home.
- **Refresh coverage is collection-scoped.** A pass warms artists, release-groups and releases the
  collection already knows about. Artists reached only by browsing still fall back to the
  on-demand path. Artwork no longer has this gap — new rows fetch their own as they arrive
  ([artwork.md](artwork.md#new-rows-fetch-their-own-artwork)) — but the metadata half still does.
- **The 500 px album hero is not warmed**, only the 250 px size the lists use
  ([artwork.md](artwork.md#what-it-warms)). One image on a page opened one album at a time is one
  throttled request, so warming it would double the artwork pass and its disk for a page that is
  already fast. Worth revisiting only if the hero ever appears in a grid.
- **Artwork has no per-artist scope.** The refresh verb covers the whole collection or nothing; the
  create hooks cover everything that arrives. The gap between them is an artist whose covers went
  missing from the cache after they were added — recoverable today only by a full pass. If that
  turns out to matter it is a `Targets` constructor and a button, not new machinery.
- **A running job cannot be cancelled.** Graceful shutdown has shipped (see
  [scanning.md](scanning.md#stopping-on-purpose)): schedules stop, HTTP drains, pending jobs are
  dropped and the job in flight is given 30 seconds. What is missing is the ability to *stop* that
  job — there is no cancellation to thread through a tag write, so a long scan still outlasts the
  grace period and leaves its event to be reconciled on the next boot. A per-job context checked
  between files (where the walk already stops cleanly) would close that gap, and would give file
  work the counterpart to the metadata pass's `POST /mirror/cancel`.
- **A credit change still has no affordance.** `collection_scan` reports the count, but it is still
  the only identity change with no Migrations row to click through to — the count is the only way to
  notice one, and there is nothing to open. The four movement counters beside it
  ([collection.md](collection.md#what-a-pass-moved-not-just-where-it-ended-up)) have the same
  shape of gap: *3 albums gone* names no albums, because the pass compares MBID sets and keeps only
  the sizes. Keeping the two sets would make them `EventItem` rows like every other finding, and
  those rows already resolve to names and links.
- **`Related` resolution reaches two surfaces, not every one.** `events.ResolveRefs` and
  `GET /mb/:mbid/files` answer "what is this identifier, and which files depend on it"
  ([scanning.md](scanning.md#an-identifier-is-not-a-subject)). The Migrations page now answers the
  second half its own way — a file count per row, linking to `/items?mbid=`, which is the Items
  filter by release this entry used to ask for — and `ResolveRefs` falls back to the migration
  table so a retired album still names itself in the feed. What is left is everywhere else that
  prints an MBID: the *what failed?* entry above wants the same treatment, since "show me the files
  behind this MBID" and "show me the files that failed" are one query with different predicates.
- **A per-artist Lidarr lookup failure has no row.** `SyncStats.Failures` carries the sentence and
  the count, rendered as a plain list, because a manager-level failure is not *about* an entity. But
  the per-artist half of it is — the artist MBID is in hand at the point of the error — so those
  could be `unknown`-shaped entity rows with the failure on them, leaving the plain list for the
  genuinely entity-less case.
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
- **The disc guard has no signal but the folder.** `verifyDiscFolder` refuses a correlation only
  when the file's media folder names a disc number that disagrees with the resolved medium
  ([tagging.md](tagging.md#the-disc-guard)). A flat album folder, or one named something
  `discFolderPattern` does not recognise, leaves it with nothing to check — and the look-alike case
  it exists for is precisely where the two candidates are otherwise indistinguishable. MusicBrainz
  supplies `track.Length` (`models/musicbrainz.go:91`) and nothing reads it, which is the missing
  second signal: Jerry Goldsmith's *Alien* opens both discs with a "Main Title" of 4:12 and 3:34, a
  gap no tagger could confuse. The comparison has to be **relative, not absolute** — that same file
  is 4:19 on disk against a stated 4:12, so a tolerance tight enough to be useful would refuse the
  correct answer. What discriminates is which candidate is *closer*: refuse when the track the file
  resolved to is much further from the file's duration than the track at the same position on the
  disc the folder names. Needs the file's duration at the point of the check, which nothing on the
  tagging path reads today.
- **AAC/M4A is where the separator choice starts to matter.** ffmpeg never gained multi-value
  support for MP4, so a delimited single value is the only thing Plex can read there — the MP3
  setting's reasoning applies, and the format work should reuse it rather than re-litigate the
  separator.

## MusicBrainz entity migration — what is left

The feature has shipped, including release-group pruning, artist identity verification and the
manual sweep; see [mb-migration.md](mb-migration.md). Artist and release are covered for both merge
and deletion, release-group for deletion (and for merges by subtraction), and recordings need no row
— the coverage matrix and the reasoning for each *no row* live in
[mb-migration.md](mb-migration.md#coverage-which-entity-which-change-which-mechanism). What that
matrix leaves open:

- **A release-track that is no longer in its release is a dead end.** This is the one identity change
  with no recovery path at all, automatic or otherwise, short of a manual force re-correlate.

  `ProcessTrackFile` locates the track by exact ID inside the release payload
  (`modules/files.go:290`). When MusicBrainz rebuilds a release's tracklist without changing the
  *release* ID, that lookup misses and the function returns `ErrTrackNotInRelease`
  (`modules/files.go:29`, raised at `:312`) — **which nothing in the repo consumes.** No handler, no
  migration row, no fallback. It surfaces as a per-file error and stays one.

  Whether that matters depends on where the correlation came from, and the exposure is narrower than
  it first looks. Under a manager, `ResolveMetadataDetailsFromLidarr` (`modules/files.go:216`)
  supplies fresh track IDs on every scan, so the condition clears on its own within a cache TTL. The
  exposed case is a **manager-less install** — FLACs carrying their own MB IDs — because the fallback
  at `:240` reads the stale ID out of the tags of the very file it is trying to repair. That is the
  only place in the pipeline where the input is the artefact being corrected, and it is why the
  failure is permanent rather than merely slow: every subsequent scan reproduces it exactly.

  The fix is a positional fallback: when the ID match misses, re-match on disc plus track position
  and treat a hit as a re-correlation, writing the new track ID. `MapFilesToTracks`
  (`modules/track_mapping.go:56`) already implements exactly this logic for bulk attach, including
  the all-or-nothing rule that stops a partial number match from silently mixing strategies.

  It wants care rather than speed. A wrong positional match mistags a file, which is the one failure
  this codebase most consistently designs against — so the fallback should refuse ambiguity the way
  `mapByNumber` does, should record itself as a distinct `CorrelationSource` so it is auditable
  afterwards, and should almost certainly not apply to `Pinned` items, whose whole point is that a
  person chose them by hand.

- **A manager re-keying an album to another *valid* MusicBrainz ID is undetected.** The repair path
  shipped this week only catches the case where the old ID is *dead*
  ([mb-migration.md](mb-migration.md#groups-that-resolve-nowhere)). If Lidarr moves an album from
  valid ID A to valid ID B, nothing 404s: the sync clears `in_catalog` on A and creates B, and A
  survives — it is a real release-group in the artist's discography, so `PruneOrphanReleaseGroups`
  will not take it either. The result is a silent duplicate on the artist page, one of them with no
  files and no catalog entry.

  Detectable from the same before/after sync diff that would carry a want across a repair (the entry
  above), which is an argument for building the two together: an album vanishing from a manager's
  catalog in the same pass that an equivalent one appears is the signal in both cases.

- **A manager re-keying an *artist* is reported but not adopted.** The old MBID stops being listed,
  so it lands in `SyncStats.Unknown` and shows as *Not in Lidarr*; the new MBID is never picked up,
  because `SyncManagers` only asks about artists the collection already holds. Reporting it is
  arguably correct — the collection is not obliged to follow a manager's re-identification — but the
  user is left to work out that the two rows are the same artist. Lowest priority of the three: it is
  visible, it is explained, and nothing is lost.

Residual open work on what has shipped:

- **Release-group pruning only runs on a discography sync**, which is per-artist and user-triggered.
  An artist nobody syncs keeps their orphaned rows. The sweep verifies artist *identity* but does
  not prune their groups, because that would mean a discography fetch per artist on top of the
  lookup.
- **Repair by manager refresh has shipped**; see
  [mb-migration.md](mb-migration.md#repairing-through-the-manager). Residual:
  - **A repaired album's want does not follow it.** When a manager re-keys an album, the old ID's
    row is retired and the live-ID album arrives as a new row, so a `CollectionDesire` pointing at
    the old ID is stranded. MusicBrainz cannot help here — these IDs were never MusicBrainz's, so
    there is no merge to detect, and release-group merges are the one category detection does not
    cover anyway. The before/after diff across a refresh (dead ID gone, new ID present, same artist
    and title) is a far stronger basis for a `MigrationKindRedirect` than any search would be.
    Deferred because no desire in the collection measured pointed at a ghost — build it when one
    does.
  - **The repair stage has no button of its own.** Approving a blocked album now triggers a scoped
    repair (see [mb-migration.md](mb-migration.md#approving-a-blocked-album)), so the only gap left
    is the *artist-page* entry point: "3 albums here have IDs that do not resolve — ask Lidarr", for
    a user who has not opened the Migrations page. `Runner.RepairArtistAlbums` is the verb; it needs
    an endpoint and a button, not new machinery.
  - **A manager with artist refresh turned off leaves rows nobody can settle.** With
    `Manager.LidarrSkipArtistRefresh` set, an album the manager still lists can be neither repaired
    (the pass skips that manager) nor retired (`in_catalog` guards it), so the row is correctly stuck
    — but *Ask the manager* is still offered and silently does nothing. The row should say the
    setting is what is holding it, the way every other blocker names its cause. The hold itself is
    right: the manager is the authority and it still lists the album.

### Migrations page — what is left after the rewrite

The page now renders the review context, handles the 202, pages both tables on the server, shows a
repair in flight, and names its source rather than assuming one. What that left open:

- **The blocker names the fix but cannot perform it.** *"files on disk still resolve to this album"*
  is a refusal whose remedy is deleting or re-attaching those files, and the row links to them
  (`/items?mbid=`) but stops there. A **Detach** or **Re-identify these files** action from the
  migration row would close the loop; the attach machinery already exists
  ([attach.md](attach.md)).
- **The queue does not group by artist.** `artist_open` tells a row how many siblings one refresh
  would settle, and the row says so in words — but the eight rows still sit apart, sorted by whatever
  the table is sorted by. A grouped section per artist (the pattern the artist page's catalogue uses)
  would make "one press covers these" visible rather than stated.
- **`resolution_detail` is a sentence, not a link.** *"a manager re-keyed it"* is the right words and
  the wrong affordance: the album it was re-keyed *to* is usually in the collection under its new ID,
  and nothing points at it. Needs the before/after diff described under *A repaired album's want does
  not follow it* above — the same evidence would carry both.

## Known issues / limitations

- **`GET /artists` recomputes the whole collection on every request.** `listArtists`
  (`routers/collection.go:272`) loads every artist, every release group, every desire and every
  credit link, then aggregates in Go — deliberately, so the complete/discrepancy rules have one
  definition shared with the artist page, but it means the cost is the size of the collection and
  nothing is cached between calls. The /collection page now covers the wait with a skeleton table
  (see [style-guide.md](style-guide.md) → *Loading placeholder*), which makes it feel responsive
  without making it faster. If it needs to actually be faster, the counts are the thing to
  materialise — they change only when a scan or a sync does.
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

  The same-basename half is no longer hypothetical: with Lidarr's naming format putting the disc in
  the folder rather than the filename, both discs' track 1 are the same "Main Title" file name, and
  the pre-`285c7c3` matcher (album + basename, first hit wins) resolved a `CD 01` file to disc 2 and
  wrote disc 2's identity into it — `DISCNUMBER 2`, `TRACKTOTAL 17`, disc 2's track, recording and
  ISRC. The current matcher gets it right and repaired the file on the next pass, which is the
  behaviour a fixture would be pinning.

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
- **Multi user support?** Any need?
- **Password reset over email.** The mailer now exists and is proven by the *Send test message*
  button on Settings → Email (see [settings.md](settings.md#email-and-the-one-action-on-this-page)),
  but nothing sends mail on its own. A reset flow is the obvious first real consumer: a signed,
  single-use, expiring token mailed to the address on the account — which means `User.Email` has to
  start being populated and verified, since today it is stored and never used.
