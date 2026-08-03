# Work in progress

Living document for what is **not done yet**: roadmap, ideas, known issues, in-flight work. Add
items as they come up; when one ships, move what is worth keeping into its feature doc and delete
the rest from here.

Shipped features are documented in [media-manager.md](media-manager.md),
[collection.md](collection.md), [attach.md](attach.md), [scanning.md](scanning.md),
[tagging.md](tagging.md), [fingerprinting.md](fingerprinting.md),
[mb-migration.md](mb-migration.md), [mirror.md](mirror.md) and
[authentication.md](authentication.md).

## Open work

- **M6 pass E — file import.** Move/copy loose files into the library layout, then hand off to
  manual attach. The last unbuilt piece of the native manager.
- **Follow has no date cutoff.** "Only future releases" is not implemented, so following always
  pulls the whole back catalogue of the chosen types. A global follow default could layer on later
  without a schema change.
- **File-import events.** Plex refresh and scheduled health checks now emit events, and long jobs
  carry live progress (see [scanning.md](scanning.md)); the remaining gap is an event for file import,
  which ships with that feature (M6 pass E) rather than before it.
- **Refresh coverage is collection-scoped.** A pass warms artists, release-groups and releases the
  collection already knows about. Artists reached only by browsing still fall back to the
  on-demand path.
- **Tag files has no collection-wide scope.** Artist and library have it; "re-tag everything" does
  not, because it is what a scan already does — worth a button only if re-tagging without a disk
  walk turns out to be wanted at that size.
- **Event retention is fixed** at the newest 200 events (detail rows cascade with them), and the
  per-run detail cap is a hardcoded 500. Both could be configurable, and time-based retention would
  suit a feed better than a count.

## Frontend follow-ups (separate repo)

The backend for the manager-authority boundary and the auto-desire model has shipped (see
[collection.md](collection.md#manager-authority--lidarr-owns-identity) and its desire-model section).
The web frontend is a built bundle here (`web/dist`, no source in this repo), so these are handoff
notes — the API contract exists and is tested; the UI has to catch up:

- **Honour `identity_editable`.** When false on the artist / release-group / library-item view, hide
  or disable manual attach, "choose edition" and the want/desire controls, with a short "Managed by
  Lidarr — change it in Lidarr" hint. `clearDesire` (`DELETE /artists/:mbid/desires`) stays available
  even when locked — it only removes a stale want (mirrors detach).
- **Re-correlate buttons**, behind a confirm dialog that says it **discards manual pins** and rewrites
  files from Lidarr: per-artist (`POST /artists/:mbid/recorrelate`), per-album on the release-group
  page (`POST /release-groups/:mbid/recorrelate`) and per-library on library settings
  (`POST /libraries/:id/recorrelate`).
- **Render an `auto` desire as state**, not an unpick toggle ("you have this edition"), and keep the
  explicit "want a specific / another edition" controls behind `identity_editable`.

## Roadmap / ideas

- **Additional audio formats** (OGG, M4A/AAC, …). Tagging covers FLAC (`metaflac`) and MP3
  (`ffmpeg`) only.
- **More MusicBrainz fields** written per track.
- **Write/normalize NFO sidecars** (`album.nfo` / `artist.nfo`). Autotaggerr already holds the full
  MusicBrainz release + artist data while tagging, so it could emit consistent sidecars (single
  `<albumartist>` + MB IDs) for NFO-first players like Jellyfin/Kodi. Open design questions:
  overwrite vs. merge vs. create-if-absent (Jellyfin-generated NFOs carry extra data like
  `<lockdata>`, `<dateadded>`, artwork paths, AudioDB IDs); Kodi-plain vs. Emby/Jellyfin dialect;
  only useful if Jellyfin's NFO *saver* is off, otherwise it rewrites the file. Would fix the
  duplicate-artist issue below at the source.
- **Granular actions beyond the artist.** The three per-artist actions have shipped (see
  [scanning.md](scanning.md)) on a `scan.Scope` built to extend. A release-group or single-album
  scope needs a new constructor and UI, not new machinery — worth doing once the artist actions have
  been used against a real library.
- **Folder structure**
  Mapping to current content, creating folders, renaming and keeping up to date.
  Configurable structure? 
  Link to file importing feature?

- **Retrofit the metadata port to AcoustID / artwork.** MusicBrainz fetches now route through
  `metadata.MetadataSource` (see [development.md](development.md#the-coverage-gate)). AcoustID
  (`acoustidBaseURL`) and cover art / fanart (`coverArtArchiveBaseURL`, `fanartBaseURL`) are still
  unexported-base-URL seams stubbable only inside `modules/`; the same port pattern would make their
  callers testable too.

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
