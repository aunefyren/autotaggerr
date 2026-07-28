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
**M5 present-vs-wanted — pass A done** (design + status below) · **OAuth/OIDC — done**
(shipped ahead of schedule; it was only bundled into M6 by the original plan and shares nothing with
the native-manager work, so it now stands alone). `auth/oidc.go` adds authorization-code + PKCE login
against any OIDC provider: `StartLogin` issues a signed, HttpOnly, 10-minute flow cookie carrying
state/nonce/verifier (no server-side session store, so a restart mid-login fails closed);
`CompleteLogin` verifies cookie signature+expiry, provider match, state (CSRF), the code exchange,
ID-token signature/issuer/audience/expiry via JWKS, and nonce. `ResolveUser` matches by immutable
`(provider, sub)` -> *verified* email -> optional signup; unverified emails never match an existing
account (takeover guard). Providers are DB rows (`models.AuthProvider`) with full CRUD under
`/auth-providers` (client secret write-only), public `/auth/providers` +
`/auth/oidc/:id/{start,callback}`, and a **Login providers** admin page; the login page grows
"Continue with ..." buttons. The session token comes back in the URL *fragment* so it never reaches
a server log, and the SPA strips it on read. Password login can never be disabled, so a broken
provider cannot lock you out. Unit-tested incl. the takeover and forged-callback cases;
**not yet live-verified against a real IdP**. Full setup + gaps in `docs/authentication.md`.) ·
**M6 native manager — planned** (design + passes below). M0-M2 land the backbone with no behavior
change for Lidarr users.

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
  Live-verified against the real Lidarr instance. **Remaining:** a configurable wanted-type filter
  for native monitoring — superseded in part by M6 pass B, where an *explicit* desire overrides the
  filter outright.

### M6 design — native manager (standalone)

What the Autotaggerr manager needs in order to stand on its own. Lidarr manages *acquisition* at
album granularity; the native manager manages *knowledge* at whatever granularity the files
actually have. That difference — not feature parity — is the point.

**Explicitly out of scope: acquisition.** There are no indexers, download clients, or release
grabbing, and none are planned. Autotaggerr enriches files that already exist. Files arrive
manually today; a file-import helper is pass E.

**Where the native manager stands today:** `AutotaggerrManager.Correlate` is
`ResolveCorrelation(filePath, nil, rootDir)` — embedded MusicBrainz tags only. FLACs that already
carry MB IDs (including everything Autotaggerr tagged via Lidarr) resolve; untagged MP3s do not.
`LibraryItem.Pinned` exists and the pipeline honours it (`components/pipeline.go` never lets
automatic resolution downgrade a pin) but nothing can *set* it. Artists only ever materialise from
files on disk, so an artist you own nothing of cannot be added or monitored.

**Desire model** (settled). Desire is *authored* user intent, and it must express five cases:

| # | "I want…" | `release_mbid` | `recordings` |
|---|---|---|---|
| 1 | this album, any release (default) | empty | empty |
| 2 | these songs, any release (default) | empty | {a, b} |
| 3 | this specific release | X | empty |
| 4 | these songs from this release | X | {a, b} |
| 5 | these songs from release X *and* those from Y, same RG (niche) | two rows: X, Y | {a,b} / {c,d} |

Empty `release_mbid` means "any release will do"; an empty recording set means "the whole thing".
Case 5 falls out as multiple rows under one release-group, with `(release_group, release)` unique so
overlapping track sets merge rather than duplicating.

**Songs are identified by *recording* MBID, not track MBID.** A MusicBrainz *track* is a recording's
placement on one specific release, so track IDs are release-scoped and cannot express case 2, where
no release has been chosen yet. A *recording* is the audio and is stable across every release it
appears on. The data is already present — `LibraryItem` stores `MBRecordingID` and
`MBReleaseTrackID` separately, and `models.Track` carries its nested `Recording` — so this costs
modelling care, not new fetches.

**Satisfaction differs by intent, deliberately:**
- An *album* desire (empty recording set) is satisfied when **at least one acceptable release is
  complete**. Owning 5 tracks of the original and 7 of the remaster is two partial albums, not one
  complete one.
- A *song* desire is satisfied when **those recordings are owned, from any release**. The user asked
  for songs, not a coherent album, so requiring one edition would be wrong.

Do not "fix" one of these to match the other; the asymmetry follows from what was asked for.

**Desire lives in its own table, never as flags on the collection rows.** Desire is authored
(sparse, typed by a human, must never be recomputed); ownership is derived (rebuilt from disk on
every scan). M5 already produced one bug from exactly that mixture — `Rebuild` and `SyncLidarr`
writing the same columns, so a scan silently wiped the Lidarr mirror. Separating the tables makes
the same class of bug structurally impossible, with the user's own intent as the thing that would
otherwise be at risk.

**Passes:**

- **Pass A — manual attach.** Tell Autotaggerr what an unmatched file is. This is the piece that
  makes the native manager usable at all, and it is independent of the collection rework.
  API: browse unmatched items (the `library-items` filter already exists), MB release search +
  tracklist (rate-limited, cache-backed), and `POST /library-items/:id/attach`
  {`mb_release_id`, `mb_release_track_id`} setting the IDs, `Pinned`, and
  `CorrelationSource = manual`; plus an un-attach. UI: an Attach action on unmatched items —
  search a release, pick the track (prefilled from folder/filename), preview the **tag-diff**
  before writing. *Self-healing:* once attached and tagged, the file carries embedded MB IDs and
  resolves natively forever after, so attaching is a one-time cost per file rather than an
  annotation to maintain.
- **Pass B — collection authoring.** Add artists and release-groups you own nothing of, and mark
  them desired. MB artist search; `CollectionArtist` gains an origin (library|manual) so a
  file-less artist is not an anomaly and `Rebuild` never treats it as one. Desire is written to
  `CollectionDesire` (see the desire model above) — *not* as a flag on `CollectionReleaseGroup`,
  which stays derived. Pass B only needs the album-level shape (cases 1 and 3); cases 2/4/5 arrive
  with pass C, so the table is introduced here and filled out there. An explicit desire is distinct
  from discovered-from-discography and **must override the `wantedType` filter** — otherwise the UI
  refuses to keep a live album or single the user just asked for. Monitoring stops 404-ing for
  artists with no files.
- **Pass C — multiple releases + partial monitoring.** Two separate concerns, kept in separate
  tables (see the desire model above).
  *Ownership:* `Rebuild` currently collapses each release-group to `rgBest` (the single best-owned
  edition) and discards the others; that stops. A new `CollectionRelease` child row per owned
  edition holds per-edition track counts, so owning the 1977 original and the 2017 remaster shows
  as two editions rather than one.
  *Desire:* a new `CollectionDesire` row — `artist_mb_id`, `release_group_mb_id`, optional
  `release_mb_id`, optional serialized `recording_mb_ids` — covering all five cases in one shape.
  Owned recordings and owned per-edition tracks are both derivable from `LibraryItem`
  (`MBRecordingID` / `MBReleaseTrackID`) plus the cached release payload, so this needs **no track
  table and no extra MusicBrainz fetches**.
  UI: an RG expands to its editions with per-edition completeness; song selection offers "any
  release" (default) or a specific edition.
- **Pass D — AcoustID.** Detachable at three levels: a DataSource row (no row, no feature),
  `fpcalc` presence (missing → unavailable, logged once, native manager behaves exactly as before),
  and per-library opt-in. `modules/acoustid.go` is self-contained: `fpcalc -json`, `/v2/lookup`,
  its own rate limiter (separate from MusicBrainz's), and fingerprint results cached in the DB —
  fingerprinting decodes the whole file, so re-doing it per scan is not viable. It returns
  *recording* MBIDs, so a scored disambiguation step picks the release (folder album/year as the
  signal), built as a pure, table-tested function. **Fails closed:** below a confidence threshold
  the file stays unmatched rather than being mistagged — a wrong match is worse than no match.
  Surfaces as autofill in the pass-A attach UI, not as a silent auto-tagger, which keeps it a
  suggestion engine rather than a second resolution pipeline.
- **Pass E — file import** (later). Move/copy loose files into the library layout, then hand off to
  pass A.

**Ordering rationale:** A before B/C because it is self-contained and immediately useful; D last
because by then it is an autofill button on an existing screen instead of new machinery.

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
