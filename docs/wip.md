# Work in progress

Living document for what is **not done yet**: roadmap, ideas, known issues, in-flight work. Add
items as they come up; when one ships, move what is worth keeping into its feature doc and delete
the rest from here.

Shipped features are documented in [media-manager.md](media-manager.md),
[collection.md](collection.md), [attach.md](attach.md), [scanning.md](scanning.md),
[tagging.md](tagging.md), [fingerprinting.md](fingerprinting.md),
[mb-migration.md](mb-migration.md), [mirror.md](mirror.md), [artwork.md](artwork.md),
[authentication.md](authentication.md), [settings.md](settings.md) and [tempo.md](tempo.md).

## Open work

- **Lidarr replacing files — what is left.** Recheck-on-miss, fresh resolution of changed files, held
  pins, the history follower (including artists new to Lidarr) and the tag-setting warning have
  shipped ([scanning.md](scanning.md#following-lidarr), [attach.md](attach.md#a-pin-belongs-to-the-audio-not-the-path)).
  - **Not yet checked against a live Lidarr.** `/api/v1/history/since` and
    `/api/v1/config/metadataprovider` were written from Lidarr's API shape and tested against mocks;
    both decoders accept an enum as name or number for that reason. One real response of each would
    settle it.
  - **A replacement at a new path still drops its album for a few minutes.** The one-miss grace
    ([collection.md](collection.md#the-disk-view-counts-files-not-successes)) holds a file the
    manager knew; a file Lidarr imports under a *new* name has no identity to hold, and the old
    row's path is gone, so until the follower's Process resolves the new files the album is out of
    the disk view. Carrying identity across by size/mtime does not apply — an upgrade changes both.
  - **Webhook.** Lidarr's *Connect → Webhook* would let an import be followed in seconds rather than
    within ten minutes. Same history, same actions; only worth it if the polling delay is ever felt.
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
- **`Related` resolution reaches two surfaces, not every one.** `events.ResolveRefs` and
  `GET /mb/:mbid/files` answer "what is this identifier, and which files depend on it"
  ([scanning.md](scanning.md#an-identifier-is-not-a-subject)). The Migrations page now answers the
  second half its own way — a file count per row, linking to `/items?mbid=`, which is the Items
  filter by release this entry used to ask for — and `ResolveRefs` falls back to the migration
  table so a retired album still names itself in the feed. What is left is everywhere else that
  prints an MBID — the Scan's new *added / left / credit moved* rows among them, which name an album
  but do not yet link to its files.
- **Retention is a count, not a duration.** The two figures are configurable now
  (`autotaggerr_event_retention` / `autotaggerr_event_detail_retention`, see
  [scanning.md](scanning.md#a-run-spawns-activities-each-one-is-a-row)), but time-based retention
  would suit a feed better than a count — "keep 90 days" is what someone actually wants, and a
  count means a busy week silently evicts a quiet month. Note that a tagging activity can exceed
  the per-event limit by design: the drift rows are adopted whole (`DetailCollector.Adopt`) so a
  big walk cannot starve them.

- **A disk-walking Scan only finds moves inside its own scope.** The artist-scoped walk covers the
  artist's folders as indexed (see [scanning.md](scanning.md#scan-can-walk-the-disk-when-asked)), so
  an album a manager moved under a *different* artist folder is pruned there and found only by a
  collection-wide walk or a Process — where it is resolved again rather than carried, since the
  vanished row was deleted first. Move detection also rests on size and modification time, so a
  cross-filesystem move that resets the mtime is resolved through the manager instead of carried.
  Both fail safe (a lookup, not a mistag); neither has been seen in production.

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
manual sweep; see [mb-migration.md](mb-migration.md). Artist and release are covered for both merge
and deletion, release-group for deletion (and for merges by subtraction), and recordings need no row
— the coverage matrix and the reasoning for each *no row* live in
[mb-migration.md](mb-migration.md#coverage-which-entity-which-change-which-mechanism). What that
matrix leaves open:

- **A manager re-keying an *artist* is reported but not adopted.** The old MBID stops being listed,
  so it lands in `SyncStats.Unknown` and shows as *Not in Lidarr*; the new MBID is never picked up,
  because `SyncManagers` only asks about artists the collection already holds. Reporting it is
  arguably correct — the collection is not obliged to follow a manager's re-identification — but the
  user is left to work out that the two rows are the same artist. Low priority: it is visible, it is
  explained, and nothing is lost.
- **Album re-keys are seen only in the pass that makes them.** `detectRekeys` diffs one artist's
  catalog across a single sync, one-to-one by title and type. A re-key that lands while a sync fails,
  or that renames the album at the same time, or that collides with a second same-titled album, is
  not paired and leaves the old duplicate as before. Fails safe (no wrong pairing); a manual
  "these two are the same album" action on the artist page would cover the rest.

Residual open work on what has shipped:

- **Release-group pruning only runs on a discography sync**, which is per-artist and user-triggered.
  An artist nobody syncs keeps their orphaned rows. The sweep verifies artist *identity* but does
  not prune their groups, because that would mean a discography fetch per artist on top of the
  lookup.
- **Repair by manager refresh has shipped**; see
  [mb-migration.md](mb-migration.md#repairing-through-the-manager). Residual:
  - **The repair stage has no button of its own.** Approving a blocked album now triggers a scoped
    repair (see [mb-migration.md](mb-migration.md#approving-a-blocked-album)), so the only gap left
    is the *artist-page* entry point: "3 albums here have IDs that do not resolve — ask Lidarr", for
    a user who has not opened the Migrations page. `Runner.RepairArtistAlbums` is the verb; it needs
    an endpoint and a button, not new machinery.

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
  and nothing points at it. A re-key the Lidarr sync observed is now a redirect row carrying the new
  ID ([mb-migration.md](mb-migration.md#a-manager-re-keying-an-album)), so those rows could link it;
  a ghost closed as resolved-elsewhere still has no target to point at.

## Known issues / limitations

- **A search hit's track count still includes video tracks.** The video rule
  ([collection.md](collection.md#a-video-track-is-not-a-track-you-are-missing)) governs the
  collection's own counts, but a row that came from the MusicBrainz *search* API carries
  `track-count` per medium with no track list behind it, so there is nothing to filter — the release
  search list totals those and shows *Endless* as `CD + DVD · 41 tracks`. Correcting only
  `SearchResultFromRelease` (which does have the tracks) would make two rows in the same list count
  by different rules, which is why it was left faithful. Closing it properly means fetching each hit
  to count its audio tracks — a rate-limited request per search result, for a number the format
  string half-answers already. Worth it only if the count is ever used for more than a glance.

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

- **BPM playlists in Plex** (postponed from the tempo work, optional). Plex ignores BPM tags, so
  running playlists at a cadence (e.g. 160 steps/min) have to reach Plexamp as playlists. The design
  as agreed so far:
  - *Definitions are database rows on their own page* (name, target BPM, tolerance default ±4,
    include half-time default on — `target/2 ± tolerance/2`, minimum confidence, optional max
    track count), not `config.json` keys — they describe a collection, not the process.
  - *Match by file path only*, through a `plex_path_mappings` key (Autotaggerr prefix → Plex
    prefix) on **Settings → Plex**. `/library/sections/:id/all?type=10` carries each track's
    `Media/Part@file` and `viewCount`. The existing client matches by *names*
    (`ResolveAlbumKeyInSection`), and Plex does not reliably expose recording MBIDs, so there is no
    fallback — an unmatched track is listed in Activity rather than guessed.
  - *Ownership by ID.* A row stores the Plex playlist `ratingKey` it created; sync touches only
    that. A same-named playlist Autotaggerr did not create is reported, not adopted. Updates are an
    add/remove diff (`PUT /playlists/:id/items?uri=…`, `DELETE /playlists/:id/items/:itemID`) so
    the playlist keeps its ID in Plexamp.
  - *Its own verb* with a preview (the diff, applied to nothing) — not a cascade off analysis.
  - *Raw values are never corrected* at analysis time; half/double-tempo is handled here. Measured
    on a real library, hip-hop near 88 BPM is often detected at ~176, and ballads can land anywhere
    with a regular beat grid — see [tempo.md](tempo.md#how-good-the-numbers-are).
  - Ordering by play count was asked about and deferred with the rest: it needs Plex's move-item
    API on top of add/remove.

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
