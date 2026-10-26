# Work in progress

Living document for what is **not done yet**: roadmap, ideas, known issues, in-flight work. Add
items as they come up; when one ships, move what is worth keeping into its feature doc and delete
the rest from here.

Shipped features are documented in [media-manager.md](media-manager.md),
[collection.md](collection.md), [attach.md](attach.md), [scanning.md](scanning.md),
[tagging.md](tagging.md), [fingerprinting.md](fingerprinting.md),
[mb-migration.md](mb-migration.md), [mirror.md](mirror.md), [artwork.md](artwork.md),
[authentication.md](authentication.md), [settings.md](settings.md), [tempo.md](tempo.md),
[naming.md](naming.md), [import.md](import.md), [downloads.md](downloads.md) and [mcp.md](mcp.md).

## Open work

- **Lidarr replacing files — what is left.** Recheck-on-miss, fresh resolution of changed files, held
  pins, the history follower (including artists new to Lidarr) and the tag-setting warning have
  shipped ([scanning.md](scanning.md#following-lidarr), [attach.md](attach.md#a-pin-belongs-to-the-audio-not-the-path)).
  - **A replacement at a new path can drop its album for a few minutes, by coincidence.** The
    one-miss grace ([collection.md](collection.md#the-disk-view-counts-files-not-successes)) holds a
    file the manager knew; a file Lidarr imports under a *new* name has no identity to hold. Lidarr
    deleting the old files prunes nothing by itself — rows only go when a run or Scan reaches the
    artist — so the album drops only if one lands between Lidarr's import and the follower's
    Process, and the follower's Process restores it. Not worth machinery unless it is seen.
  - **Webhook.** Lidarr's *Connect → Webhook* would let an import be followed in seconds rather than
    within ten minutes. Same history, same actions; only worth it if the polling delay is ever felt.
- **The Lidarr mirror cannot introduce an artist**, so it cannot populate a cold collection — the
  button a Lidarr-first user reaches for first is the one that can do nothing. `CollectionArtist`
  rows do not require files (*Add artist* creates one), so nothing structural forbids it; it is a
  decision about what the collection means, since importing from Lidarr would fill "present vs
  wanted" with artists no file has ever been seen for. If it happens it belongs behind its own option
  on the sync dialog or a separate *Import artists from Lidarr* action, not as a change to what the
  plain Sync button does.
- **File import — what is left** ([import.md](import.md#known-limits)). Extra files (covers, cue
  sheets, logs) are not carried along, so the source folder survives a move; a brand-new album
  reaches Plex only through Plex's own scan; and identification offers no AcoustID suggestion yet,
  though the per-file fingerprint lookup already exists ([fingerprinting.md](fingerprinting.md)).
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

- **Worker-count tuning.** Scan events now carry `mb_lookups` (cache hit / coalesced / fetched), so
  the cost of a run is finally measurable. The default `autotaggerr_process_concurrency` of 4 has
  never been tuned against those numbers, and a separate, higher cap for MP3s than for FLAC may be
  worth it — FLAC rewrites are more disk-bound.

- Files are expected to have a certain path, mostly because we are trying to match with Lidarr path/files. This needs to change. We should attempt to match (if Lidarr managed), but also accept that files might be placed wrong/badly named. The move/rename module can fix this, but we must attempt to match the files meanwhile. Not being able to match is another beast. That should be fixed using an import/fix function.

- **Links out — what is left.** Artist and album pages link into the Lidarr that holds them and the
  album page into Plex ([collection.md](collection.md#links-into-the-managers),
  [scanning.md](scanning.md#links-into-plex)). Not linked yet: the Plex *artist* (its key is the
  album's `parentKey` one level up, so it is one more field on the same lookup), an edition or track
  in either service, and anything from the collection list. The MCP `get_artist` and `get_album` tools carry the same links. Plex links are resolved on demand rather
  than for the whole collection; a pass that resolves every album would make them instant and let a
  refresh skip the lookup, at the cost of a few Plex requests per album once a month.

- If Plex is reworked/decoupled, Jellyfin support should be considered. Plex is now per library
  (a section or none, on the library row) and matched by file path, which is the shape a second
  media server would take: a client with `ForLibrary` and a file-verified album lookup, chosen on the
  library. The server itself is still two keys in `config.json`; a second kind of server is the point
  at which it becomes a component row like a manager.

## Roadmap / ideas

- **MCP server — what is left** ([mcp.md](mcp.md)). Reading, scoped keys and the core actions have
  shipped.
  - **Remote connectors (OAuth).** claude.ai and other hosted clients connect only via OAuth: protected
    resource metadata (RFC 9728) named from `WWW-Authenticate`, client registration (dynamic, or a
    client ID metadata document), and an authorization code flow with PKCE. Autotaggerr would be the
    authorization server, its login (password or the OIDC providers) the consent screen, and the token
    would carry the key scopes. Its own security review; nothing else depends on it.
  - **Actions not exposed yet**: detach / reattach an artist, mark same artist / album, re-correlate,
    repair albums, the Lidarr sync, bulk attach and AcoustID identify, the mirror / artwork / tempo
    passes, and everything admin-only (settings, import, download clients). Each is one command
    extraction plus a tool; none was needed to answer or act on the common questions.
  - **Non-admins cannot manage their keys in the UI**: the panel lives on the admin-only Settings
    page. The routes are open to any signed-in user, so this only matters once there is a second
    role — see *Multi user support* below.
  - **Resources and prompts.** Resources (`autotaggerr://artist/{mbid}`) would duplicate the tools for
    clients that browse them poorly; a "library health report" prompt that walks overview → missing →
    failures → suggested actions is the natural first prompt.
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

- **Download clients — what is left** ([downloads.md](downloads.md)). The inbox and auto-import
  have shipped: qBittorrent, category → library mappings with a per-category auto-import switch,
  path mappings, the poller, the Downloads tab, copy while seeding, and the `autotaggerr-imported`
  tag. Still open:
  - *A free-space check before copying*, since a copy doubles an album's space.
  - *A post-import category*, as an option beside the tag.
  - *Removing the torrent once seeding is done*, then the copy-twice cost goes away. It needs the
    client's share-limit state and a careful delete.
  - *Other clients* (Transmission, Deluge, SABnzbd/NZBGet), behind `modules.DownloadClient`. A usenet
    client holds nothing after it finishes, so `Seeding` is false and Move is allowed.
  - Extra files and AcoustID suggestions are the file import's open items, and apply here unchanged.
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
- **Organise — moving files to where their profile names them.** The templates, the renderer and
  the per-file `PATH` preview have shipped ([naming.md](naming.md)); nothing moves a file yet. What
  is left is the verb: a dry run over a scope listing every proposed move as a review table (the
  style guide's *Review step*), conflicts — two files rendering one path — blocking the commit,
  then the move itself, keeping `library_items` rows, pins and tempo keyed to the new path, and
  pruning folders left empty. It must refuse under a Lidarr manager, which tracks its files by
  path. A move also needs a Plex refresh of both the old and new album.
- **Several libraries — what is left** ([collection.md](collection.md#several-libraries)). Copies
  counted as tracks, the catalog comparison, the library on every collection surface, edition naming
  tokens, Clean/The tokens, the import's `other_edition` guard, authority per library (wants,
  editions and following for one library, library tabs on the artist and album pages), the attach
  gate honouring detach, `library` on the MCP read tools, Add artist needing a native library, the
  repair hold asking the artist's own managers, the catalog mirror merging several managers, the
  MusicBrainz source honoured in full and Plex per library (a section or none, albums matched by
  file) have shipped — every process-wide setting is now either genuinely process-wide or read from
  the library row. Still open:
  - *The collection list does not recount per library.* Its library filter narrows which artists are
    listed; the owned/missing/mismatch columns stay all-library. The artist page reads one library
    on its tabs. Recounting the list needs `LibraryHoldings` per artist, which nothing has asked for.
  - *`other_edition` only sees indexed files.* A folder of files never processed is not caught by
    the import. A directory read of the target album folder would close it, at the cost of not
    knowing what edition those files are.
  - *A detached artist's unidentified files stay Lidarr's.* The attach gate reads the artist from the
    file's current edition, so a file in a Lidarr library with no identity yet has no artist to have
    been detached and cannot be attached by hand. Reading the artist from the attach *target* would
    close it, at the cost of a gate that depends on the request.
  - *Following is one library at a time.* `FollowLibraryID` names a single native library (or all,
    for an artist no manager governs). Following the same artist into two native libraries would need
    the follow settings per library.
- **Multi user support?** Any need?
- **Password reset over email.** The mailer now exists and is proven by the *Send test message*
  button on Settings → Email (see [settings.md](settings.md#email-and-the-one-action-on-this-page)),
  but nothing sends mail on its own. A reset flow is the obvious first real consumer: a signed,
  single-use, expiring token mailed to the address on the account — which means `User.Email` has to
  start being populated and verified, since today it is stored and never used.
