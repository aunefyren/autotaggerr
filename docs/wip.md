# Work in progress

Living document for roadmap, ideas, known issues, and anything half-finished. Keep it
current — add items as they come up, and move them to a feature doc or delete them once done.

## Roadmap / ideas

- **Activity / events feed — core done.** Persisted `models.Event` (`type`/`status`/`started_at` sort
  key/`finished_at`/`title`/`summary`/`details` JSON via gorm `serializer:json`/optional
  `ref_type`+`ref_id`); `events` package with `Begin`/`Finish`/`Prune`. The scan runner emits a `scan`
  event (running→ok/error, details = counts + error files + library names + duration) and prunes to the
  newest 200. API: `GET /events` (filter type/status, paginate) + `GET /events/:id`. UI: the **Activity**
  page (replaced Scans) — reverse-chron feed with status pills, a live scanning banner, "Scan all"
  button, and a click-through detail modal (scan stat grid + error-file list). Live-verified.
  **Remaining ideas:** emit events for Plex refresh / health checks / (later) drift-sync + import;
  reuse the tag-diff component in a per-file `tag_write` event; optional time-based retention config.

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
## Standalone media manager (epic)

Evolve Autotaggerr from a stateless Lidarr-dependent tagger into a media *knowledge* manager that
can either keep leaning on Lidarr exactly as today **or** stand on its own. Full design in
`docs/media-manager.md` (to be written as milestones begin); approved plan summary below.

**Component model** (all user-configured, stored in the DB):
- **Managers** — the correlation authority (file → MB release/track) + library-state owner.
  `Lidarr` (reads Lidarr's decision, today's behavior) and `Autotaggerr` (native: embedded tags /
  manual pins now, fingerprint later; decisions persisted and never silently re-derived).
- **Data Sources** — decoupled metadata providers (`MusicBrainz` now; used by *both* managers for
  tag-payload data since even Lidarr mode fetches MB directly). `AcoustID`/fingerprint later.
- **Libraries** — a folder + which Manager, Data Source, and Tagger Profile govern it.
- **Tagger Profiles** — the tag-writing settings (current `autotaggerr_*` flags); one built-in
  engine, kept first-class so Plex/Jellyfin dialects and the NFO-sidecar writer can be added later.

**Infrastructure:**
- **DB:** SQLite via **GORM**, dialector pluggable (postgres/mysql later). Default driver
  `glebarez/sqlite` (pure-Go, keeps the build CGO-free for multi-arch releases). Domain models embed
  a shared `Base` with a **UUIDv7** primary key (assigned in a `BeforeCreate` hook — portable, no
  server-side UUID default needed); the MB cache keeps the MB ID as its key.
- **Config split:** bootstrap config (DB type/DSN, port, `private_key`, log level, timezone) stays in
  `config.json`/env; all domain config moves to the DB, edited via the API/UI. First run seeds the DB
  from the existing `config.json` so current Lidarr users are unchanged, and auto-creates one admin user.
- **API-first + embedded SPA:** JSON API under `/api/v1`; Vite/React SPA bundled via `go:embed`. Auth
  starts as a single auto-generated admin (JWT signed with `private_key` + API key), OAuth/OIDC later.
- **Style guide first:** before any UI, author `docs/style-guide.md` + shared tokens/components. Standing
  rule (see `docs/development.md`): every UI decision consults the guide and either follows or reshapes
  it in the same change; reuse elements/colors/principles, no one-off styles.

**Milestones:** **M0 DB + config split + seed — done** (GORM + pure-Go sqlite in `database/`,
dialector by `config.database.type`; models in `models/db.go`; idempotent first-run seed from
`config.json` → MusicBrainz data source, Lidarr manager, libraries, default tagger profile, auto
admin user; wired in `main.go`, no pipeline behavior change) · **M1 component pipeline — done**
(`ProcessTrackFile` split into `modules.ResolveCorrelation` + `TagResolvedFile`; new `components/`
package with `DataSource`/`Manager` (Lidarr + Autotaggerr) / `Tagger` built from DB rows;
`components.ProcessFile` persists correlations to `library_items`; scan orchestration extracted to
`modules.WalkAndProcess` and reused by `components.ScanLibrary`, which **skips unchanged files**
(index row ok + same size/mtime + same app version, version-gated so upgrades re-process once);
`processLibraries` now iterates enabled DB libraries; the **MusicBrainz release cache moved into the
DB** (write-through on fetch, one-time JSON import at startup, JSON fallback when no DB); single-file
+ full-scan + MB-cache paths live-verified. Also fixed a GORM footgun — `default:true` on bool
columns silently overrode user-chosen `false` (now guarded by a test).) · **M2 API + auth — done**
(new `auth/` package splitting authentication from session issuance for OAuth-readiness: local
password login + JWT signed with `private_key`, `Middleware` accepting Bearer token *or* `X-Api-Key`;
`routers.API` mounts `/api/v1` with full CRUD for data-sources/managers/tagger-profiles/libraries via
pointer input DTOs (secrets settable but hidden via `json:"-"` on output), plus `auth/login`+`me`,
`health`, `library-items` browse (filter/paginate), and scan control — `POST /scan`,
`POST /libraries/:id/scan`, `GET /scan/status` — backed by a shared `scan.Runner` (single-run guard +
status) that also drives the cron/startup scans. Live-verified end to end. OAuth/OIDC slots in later
as another authenticate-then-`IssueToken` path.) · **M3 style guide + SPA — pass A done**
(`docs/style-guide.md` is the living design system — utilitarian *arr-style, dark-only, violet/indigo
accent, compact, with the monospace **tag-diff** as the signature; the consult-or-reshape rule is in
`docs/development.md`. The SPA is a Vite + React + TS app in `webui/` styled from those tokens
(`theme.css`), built to `web/dist` and embedded via `go:embed` (`web/embed.go`) — one binary, index.html
fallback for client routes, `/api` never hijacked. Pages: login, dashboard, libraries (full CRUD + scan),
managers (create/delete), data-sources, tagger-profiles, items browser (filter/paginate), scans (trigger
+ live status). CI builds the UI before the Go steps; `web/dist` is committed so `go build` needs no Node.
Pass B added: **self-hosted IBM Plex** (latin woff2, bundled — no CDN); the signature **tag-diff detail**
(clicking an item shows current→desired per tag, backed by pure `modules.BuildFileTags` + `DiffFileTags`
and `GET /library-items/:id/tags` — live-verified against real MusicBrainz data); **edit forms** for
libraries/managers/tagger-profiles (secrets settable, never returned); and a first-run onboarding hint.
**M3 done.**) · **M4 drift/sync — core done**
(catches upstream MusicBrainz changes and re-tags the files a scan would skip. `modules`:
`hashRelease` (sha256 of the payload), `MusicbrainzDueForRefresh` (expired cache entries),
`RefreshMusicBrainzRelease` (force-fetch + old/new hash compare → changed). `scan.Runner.SyncDrift`
(shares the scan run-guard) refreshes due releases and, for changed ones, re-tags affected
`library_items` from their stored correlation via `TagResolvedFile` + the library's tagger, refreshing
each item's on-disk identity so skip-unchanged stays correct; emits a `drift_sync` event
(releases checked/changed, files re-tagged, errors). API `POST /sync`; UI "Check for updates" on the
Activity page with a drift detail view. Live-verified end to end. **Remaining ideas:** schedule the
sync on its own cron; a `tag_write` event per re-tagged file; surface which fields changed.) ·
**M5 present-vs-wanted — pass A done** (design + status below) · M6 native fingerprint resolution +
OAuth. M0–M2 land the backbone with no behavior change for Lidarr users.

### M5 design — present vs wanted (collection)

Split the concept: **present** (your collection, organized artist → release-group) is universal and
computed from the library for every manager; **wanted** (the gaps) is manager-owned. Native
(Autotaggerr) manager *owns* wanted — you monitor artists and Autotaggerr computes have/missing from
MusicBrainz. Lidarr manager: Lidarr is the source of truth, so **don't replicate it** — show present +
a "managed by Lidarr" note/deep-link (pass A), optionally mirror Lidarr's missing read-only (pass B).
One UI mental model: an artist-completeness **Collection** page; the manager difference is just a
per-artist provenance badge + which actions are offered. Discography noise (every single/remix/live)
means "wanted" needs a **type filter** (default albums+EPs, official) or the missing list is unusable.
- **Pass A (native-first) — done.** `CollectionArtist`/`CollectionReleaseGroup` entities (named
  Collection* to avoid clashing with the MB response types); `collection.Rebuild` aggregates owned
  release-groups from `library_items` + *cached* releases (never fetches) with per-artist `managed_by`
  from the library's manager; `collection.SyncArtist` fetches the MB discography
  (`GetMusicBrainzArtistReleaseGroups`, paged/rate-limited) and marks wanted release-groups (default
  filter: studio albums + EPs, no secondary types). API: `GET /artists` (owned/missing counts),
  `GET /artists/:mbid` (have/missing), `POST /artists/:mbid/monitor` (toggle → sync),
  `POST /collection/rebuild`. UI: a **Collection** page (artist list + completeness + managed-by badge)
  and an artist modal (have/missing lists, monitor toggle, Lidarr provenance note). Live-verified
  against real MusicBrainz (Kendrick Lamar → 1 owned, 11 wanted).
- **Pass B — partial albums + auto-rebuild done.** `CollectionReleaseGroup` now records
  `OwnedTracks`/`TotalTracks` (owned files vs the best-owned edition's track count); Rebuild computes
  them per release-group, and `GET /artists` exposes complete/partial/missing counts. The Collection
  page shows a **Partial** column and the artist modal shows per-album completeness (`8/12`, ◐ partial
  vs ● complete). `collection.Rebuild` now runs automatically after every scan and drift sync, so the
  view stays current without a manual button. Live-verified (owning 2 of 10 tracks → PARTIAL).
- **Pass B — Lidarr mirror done.** For Lidarr-managed artists, `collection.SyncLidarr` reads each
  artist's Lidarr albums (`GetArtists`/`GetArtistAlbums` by `foreignArtistId`/`foreignAlbumId`) and
  upserts them as owned/wanted release-groups using Lidarr's own `statistics` (trackFileCount vs
  trackCount → owned/partial) — Lidarr is authoritative (`upsertReleaseGroup` gained a `setOwnership`
  flag so native discovery never clobbers it). `POST /collection/sync-lidarr` runs it in the
  background and records a `lidarr_sync` Activity event; the Collection page has a "Sync from Lidarr"
  button, and Lidarr/mixed artists show missing (no native monitor toggle, "monitoring managed by
  Lidarr" note). Mapping unit-tested against a mock Lidarr; endpoint/event wiring live-verified.
- **Pass B — disk/catalog split done.** The Lidarr mirror and `Rebuild` were writing the *same*
  ownership columns, so whichever ran last decided what the UI showed — and since `Rebuild` runs
  automatically after every scan, a scan silently wiped the Lidarr mirror. (`Rebuild`'s reset also
  cleared `owned` but not the track counts, leaving rows rendering as "missing, 10/12".)
  `CollectionReleaseGroup` now carries two independently-written blocks: **disk**
  (`owned`/`owned_tracks`/`total_tracks`, written only by `Rebuild`) and **catalog**
  (`in_catalog`/`catalog_owned_tracks`/`catalog_total_tracks`/`catalog_monitored`, written only by
  `SyncLidarr` + native discography discovery). Neither authority can clobber the other, and the
  order they run in no longer matters. Where they disagree the row reports a `Discrepancy` —
  `stale_catalog` (more files on disk than Lidarr thinks; Lidarr needs a rescan — the real
  *Saturday Night Fever* 3/21 case), `not_indexed` (Lidarr has files Autotaggerr never indexed), or
  `unmapped` (files with no manager album at all). Suppressed when the artist has no catalog to
  compare against, or every album of an unmonitored native artist would flag. `GET /artists` gained
  `mismatch_count`; `GET /artists/:mbid` returns server-derived `complete` + `discrepancy` so the
  rules have one definition. UI: a **Mismatch** column, a mismatch pill in the artist modal, and a
  per-album `⚠ Lidarr 3/21` note with a hover explanation of what to do.
  **Remaining:** a configurable wanted-type filter for native monitoring; the split is unit-tested
  but **not yet live-verified** against the real Lidarr instance.

## Recently fixed

- **MP3 tracks with multiple ISRCs were re-tagged on every scan.** ISRC is packed into one
  `TXXX=ISRC:<value>` frame, but `GetMP3Tags` decoded it by splitting the frame value on `;`
  *before* the `KEY:value` split — so a `"; "`-joined multi-ISRC string (common on singles and
  featured tracks) read back as only its first value. Desired never equaled read-back, so the
  diff never converged and the file was rewritten every run (visible as "N files / N tags", one
  ISRC tag per file — reproduced live on *The Heart Pt. 3*). Fixed the decoder to split on the
  *first colon only* (`SplitN(val, ":", 2)`) so the full multi-value string survives; the on-disk
  format is unchanged, so existing files converge immediately with no migration write. Regression
  test: `TestMP3MultiISRCIdempotent`.
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
