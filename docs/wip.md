# Work in progress

Living document for roadmap, ideas, known issues, and anything half-finished. Keep it
current — add items as they come up, and move them to a feature doc or delete them once done.

## Roadmap / ideas

- Support for additional audio formats (OGG, M4A/AAC, etc). Tagging currently only covers
  FLAC (via `metaflac`) and MP3 (via `ffmpeg`).
- Expand the metadata written per track (more MusicBrainz fields).
- **Write/normalize NFO sidecars** (`album.nfo` / `artist.nfo`). Autotaggerr already holds the
  full MusicBrainz release + artist data while tagging, so it could emit consistent sidecars
  (single `<albumartist>` + MB IDs) for NFO-first players like Jellyfin/Kodi. Open design
  questions: overwrite vs. merge vs. create-if-absent (Jellyfin-generated NFOs carry extra data
  like `<lockdata>`, `<dateadded>`, artwork paths, AudioDB IDs); Kodi-plain vs.
  Emby/Jellyfin dialect; only useful if Jellyfin's NFO *saver* is off (otherwise it rewrites the
  file). Would fix the duplicate-artist issue below at the source.
- This project fixes the metadata tagging by Lidarr, but we still need to get the MB data Lidarr has decided on. If we replicated more essential functionality, could we replace Lidarr?

## Recently fixed

- **Plex album-key was never populated on a cache miss.** In `PlexRefreshForFile`, the resolved
  album key was assigned to a shadowed inner variable (`albumKey, err := ...` inside the `else`),
  so the outer `albumKey` stayed empty and the album was queued for refresh with an empty key
  (only cache *hits* worked). Fixed while making the Plex cache concurrency-safe.

## Known issues / limitations

- Lidarr does unreliable tagging of files, and we need the exact MB ID of the track. It can also drift from the metadata to the Lidarr assigned MB ID. Solution now is to retrieve the exact Lidarr assigned MB ID from Lidarr using the API and track file matching. My personal library can take 7 hours to process.
  - **Scan-time work done so far:** (1) `FindTrackFileByPath` is now cached per artist (`lidarr_trackfiles.json`) instead of refetching the artist's whole track-file list per track; (2) MusicBrainz release cache expiry is jittered 7–14 days so entries fetched together in one scan don't all expire at once; (3) cache writes are batched (marked dirty + flushed periodically / at scan end) instead of rewriting each JSON file on every miss, and the per-fetch reload-from-disk was removed; (4) all caches are loaded once at startup and kept warm in memory; (5) library scans now process files with a bounded worker pool (`autotaggerr_process_concurrency`, default 4; `1` = serial). Caches, the album-refresh collector, and scan counters are all concurrency-safe. The MusicBrainz 1 req/s limiter is global, so a cold-cache scan is still floored by it; concurrency mainly helps the warm-cache steady state (subprocess + disk bound).
  - **Next idea:** measure the real warm-scan speedup and tune the default worker count; consider a separate (higher) cap for MP3s vs FLAC since FLAC rewrites are more disk-bound.
- **Jellyfin duplicate artists via NFO / online providers.** Jellyfin keys artist identity on the
  *name string*, not the MB ID (which Autotaggerr does tag). When an online provider like
  TheAudioDB spells an artist differently from MusicBrainz (e.g. straight `'` vs curly `’`
  apostrophe), Jellyfin creates two artists and can persist both into `album.nfo` as repeated
  `<albumartist>` lines. Autotaggerr's tags are correct (they mirror MusicBrainz); the split
  originates in Jellyfin. Workaround: disable the NFO reader and/or TheAudioDB in Jellyfin so it
  trusts the embedded tags. See the NFO-sidecar roadmap item above for a possible in-app fix.
