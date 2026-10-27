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
one pass — see [tagging.md](tagging.md#the-recording-mbid-is-written-twice-on-purpose). The review
against Picard, Plex and Jellyfin has shipped its mapping half — sort names, the release-type and
original-date options, and Picard's release fields
([tagging.md](tagging.md#picards-fields-and-what-reads-them)). What is still open:

- **Composer and the other relationship credits are not written.** Lyricist, writer, arranger,
  conductor, producer, engineer, mixer, remixer, performers and the work ID too — Picard writes
  them by default, and Jellyfin reads `Composer`. It needs relationship includes on the release
  fetch (`recording-level-rels`, `work-rels`, `work-level-rels`, `artist-rels`), a heavier payload
  and a cache change, then the mapping.
- **ReplayGain is not written.** Jellyfin normalises volume from `REPLAYGAIN_TRACK_GAIN`; Plex
  measures loudness itself. It needs audio analysis by an external tool, the way tempo analysis
  uses aubio — its own optional runner, not part of tagging.
- **Does Jellyfin lose the track artist when `ARTIST` is blank?** The default profile's
  `ignore_redundant_contributing_artists` blanks `ARTIST` when it equals the album artist, and
  Jellyfin's prober builds a track's artists from `ARTIST` alone. Plex falls back to the album
  artist. Test on a real Jellyfin before deciding whether the default should change.
- **MP3s carry `TXXX:year`, `TXXX:originaldate` and `TXXX:originalyear`**, artefacts of the old
  ffmpeg writer that Picard does not write in ID3v2.4. Harmless; removing them re-tags every MP3
  once, so only worth it alongside another change that does.
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

- **Layout-independent matching — what is left.** Lidarr matching no longer trusts the layout
  ([media-manager.md](media-manager.md#matching-does-not-trust-where-a-file-sits)): track files by
  the path below the root, artists by folder and then by the file's tags, and an unmatched file
  says why. What still reads the layout: the collection's per-artist actions take the artist folder
  as the folder to rescan (`collection.ArtistTargets`, which falls back to the file's own folder),
  and folder attach and the disc guard read the disc folder. A file Lidarr has no track file for is
  Lidarr's to import; in a native library, Organise moves misplaced files where their profile names
  them ([naming.md](naming.md#organise)).

- **Links out — what is left.** Artist and album pages link into the Lidarr that holds them and the
  album page into Plex ([collection.md](collection.md#links-into-the-managers),
  [scanning.md](scanning.md#links-into-plex)). Not linked yet: the Plex *artist* (its key is the
  album's `parentKey` one level up, so it is one more field on the same lookup), an edition or track
  in either service, and anything from the collection list. The MCP `get_artist` and `get_album` tools carry the same links. Plex links are resolved on demand rather
  than for the whole collection; a pass that resolves every album would make them instant and let a
  refresh skip the lookup, at the cost of a few Plex requests per album once a month.

- **Jellyfin support.** Media servers are typed rows linked per library
  ([media-servers.md](media-servers.md)), so Jellyfin is a client implementing what `PlexClient`
  does — find the album holding a file, refresh it, list sections (libraries), scan, link — plus a
  `type` value; `PlexTargets` would become an interface over both. No table, page or migration.

- The default tagger is "Default" and you can select a default tagger in the drop down, meaning two "Default"
  - Do we need a default option?

- /settings must be reworked with /frontend-design & style guide. New design must make the categories more navigable.

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

- **Download clients — what is left** ([downloads.md](downloads.md)). qBittorrent, Transmission and
  SABnzbd, auto-import, the free-space check, the post-import category and removal once seeding is
  done have shipped. Still open:
  - *Deluge and NZBGet*, behind `modules.DownloadClient` like the rest. Deluge needs its Label plugin
    for categories; NZBGet would read like SABnzbd (`FilesOnDisk`, nothing seeds).
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
- **Organise — what is left** ([naming.md](naming.md#organise)). The dry run, the review table, the
  two-phase move and Lidarr's refusal have shipped. Not yet: an MCP tool (a dry run an assistant could
  show, and a commit behind the write scope), moving extra files (covers, cue sheets) with their album
  rather than leaving them in the old folder, and organising on a schedule or after each import —
  deliberately not done, since a move nobody reviewed is the thing the review step exists to prevent.
- **Several libraries — what is left** ([collection.md](collection.md#several-libraries)). Copies
  counted as tracks, the catalog comparison, the library on every collection surface, edition naming
  tokens, Clean/The tokens, the import's `other_edition` guard, authority per library (wants,
  editions and following for one library, library tabs on the artist and album pages), the attach
  gate honouring detach, `library` on the MCP read tools, Add artist needing a native library, the
  repair hold asking the artist's own managers, the catalog mirror merging several managers, the
  MusicBrainz source honoured in full and Plex per library (a section or none, albums matched by
  file), the collection list counted per library, unidentified files caught by the import and
  attachable to a detached artist, and following into several native libraries have shipped — every process-wide setting is now either genuinely
  process-wide or read from the library row. Still open:
  - *Follow settings are one shape for every library.* Following wants into any set of native
    libraries, but with one set of types, secondary types and cutoff
    ([collection.md](collection.md#authority-is-per-library)). Different settings per library would
    need a follow row per library, reaching every follow reader, the new-artist policy and merges —
    only worth it if someone asks for it.
- **Multi user support?** Any need?
- **Password reset over email.** The mailer now exists and is proven by the *Send test message*
  button on Settings → Email (see [settings.md](settings.md#email-and-the-one-action-on-this-page)),
  but nothing sends mail on its own. A reset flow is the obvious first real consumer: a signed,
  single-use, expiring token mailed to the address on the account — which means `User.Email` has to
  start being populated and verified, since today it is stored and never used.
