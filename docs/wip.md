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

## Manager authority boundary — Lidarr owns identity

**The principle.** When an artist is managed by Lidarr, Lidarr is the sole authority over
*identity* — which albums are wanted, which release/edition is selected, and which track a file
maps to. Autotaggerr's job is reduced to *tagging* files to match Lidarr's answer and *refreshing
Plex*. Everything below flows from that one line. `mixed` counts as Lidarr: if Lidarr governs an
artist at all, identity is Lidarr's, so the gate is a single bit per artist, not a per-release
negotiation. The `CollectionDesire` model (wanted albums, "any release vs. this edition" at
`collection/desire.go:121`) is therefore a **native-manager construct** — for a Lidarr artist the
same facts already arrive via `SyncLidarr` (`catalog_monitored` = the want, the monitored release =
the chosen edition), and running both authorities is what produces the contradictions below.

**Why this came up.** A file's collection ownership follows its *stored correlation*
(`collection/collection.go:108`), and a Lidarr-selected release (`e398f34d`, 20 tracks) failed to
flow down to files still correlated to an old release (`8bf89110`, 32 tracks) — so the collection
read "20/32 on `8bf89110`" and the release-group showed "not in Lidarr" (owned RG ≠ the RG Lidarr
monitors) even though the album is green in Lidarr. Manually re-attaching the MB ID in Items then
crashed on a nil Plex refresh set (fixed: `RetagItems` now allocates + flushes one, and
`AlbumRefreshSet.Add`/`Snapshot` nil-guard). The manual attach was the user reaching for a lever
that shouldn't exist under Lidarr — and a scan would have reverted it anyway, since the scan
re-resolves from Lidarr while attach/drift write the stored correlation.

Work, roughly in order:

- **Identity gate (`collection.IdentityEditable(db, releaseGroupMBID) bool`).** False when the
  governing artist is `lidarr` or `mixed`. Enforce in **both** layers: API endpoints (`SetDesire`,
  `ClearDesire`, Items attach/reattach, any select-edition endpoint) reject with a clear message
  ("this artist is managed by Lidarr — change the release in Lidarr"); the UI hides/greys the same
  controls so the reject is never hit in normal use. One predicate, no scattered
  `if managedBy == lidarr` checks.
- **No tag fallback under Lidarr management.** `ResolveCorrelation` (`modules/files.go:179`)
  currently falls back to the file's embedded MB tags when Lidarr returns nothing. For a
  Lidarr-managed library that fallback preserves a stale identity Lidarr no longer agrees with, so
  instead: a Lidarr-managed file Lidarr cannot match becomes **unmatched/orphaned**, surfaced as
  such, rather than tags-resolved. (The native manager keeps the tags path — that is its only
  source.)
- **Lidarr force-refresh.** Changing the selected release in Lidarr is the source of truth, but
  Autotaggerr caches Lidarr albums for 1h (`modules/lidarr.go:27`, `GetMonitoredAlbumMBID`) and
  nothing invalidates that when the selection changes. Add a force-refresh (re-sync) that busts the
  album cache, re-resolves the monitored release, and re-tags the affected files so the new release
  actually flows downstream.
- **Surface the silent "track not found in release" failure.** When the release ID
  (`GetMonitoredAlbumMBID`) and the track ID (Lidarr's track list) disagree — common mid-migration —
  `TagResolvedFile` returns "track not found in release data" (`modules/files.go:254`) and leaves the
  file's old tags untouched with no visible signal. This is the most likely reason a specific file
  stays stuck on an old release, so it must become a per-file event/warning. Needed alongside the
  force-refresh: without it, a force-refresh can "succeed" while the file never actually moves.
- **Keep the "any release → this edition" migration, scoped to native.** When Autotaggerr is the
  manager and a file appears for an "any release" desire, promoting the desire to that concrete
  release (and marking sibling editions missing) is correct — the file *is* the act of choosing.
  Under Lidarr the same click manufactures a false "missing" state, so gate it behind
  `IdentityEditable`.

- **Repair files already stuck on an old release.** The gate stops *new* divergence; it does not
  move files that already diverged (the `8bf89110`/`e398f34d` case). Two mechanisms keep them stuck,
  and the recovery verb has to defeat both: (1) `shouldSkip` (`components/pipeline.go:234`)
  re-processes only when the file's bytes, the app version, or the manager change — *never* when
  Lidarr's release selection changes upstream, so a healthy `ok` item is skipped forever; (2) a
  manually-attached file is `Pinned` (`routers/attach.go:215`), and the pipeline reuses the pin
  (`pinnedCorrelation`, `pipeline.go:264`) instead of asking Lidarr. So the repair is a **force
  re-correlate** scope (per artist / release-group / library) that: busts the Lidarr caches,
  clears `Pinned` on Lidarr-governed items in scope, re-runs the pipeline **ignoring `shouldSkip`**,
  and writes Lidarr's current release — leaving anything Lidarr still can't match `unmatched`.

### Implementation plan (sequenced)

Landed already (working tree, uncommitted):
- The `RetagItems` nil-`AlbumRefreshSet` crash fix (`scan/runner.go`) plus `Add`/`Snapshot`
  nil-guards (`modules/files.go`).
- **Step 4 — Lidarr cache-bust.** `modules.LidarrInvalidateCaches()` (`modules/lidarr.go`) drops all
  four Lidarr caches so the next lookup re-fetches the current release selection.
- **Step 5 — force re-correlate verb.** `Scope.Force` + a `force` param threaded through
  `components.ScanLibraryRoots` → the walk closure (bypasses `shouldSkip`).
  `Runner.ForceRecorrelateArtist` (`scan/runner.go`) busts the Lidarr caches and clears `Pinned` on
  the artist's Lidarr-governed files (`prepareForceRecorrelate`/`pathInScope`, unit-tested) before a
  forced re-walk. Exposed as `POST /artists/:mbid/recorrelate` (`routers/scan_items.go`,
  `routers/api.go`). **UI button is a separate change — the web frontend is a built bundle
  (`web/dist`), no source in this repo.**
- **Step 6 — track-not-found classification.** `modules.ErrTrackNotInRelease` (`modules/files.go`),
  wrapped with an actionable message pointing at force re-correlate; the file still lands as a
  visible `error` item that the next scan re-attempts.
- **Step 1 — identity gate predicate.** `collection.IdentityEditable(artist)` +
  `collection.ArtistIdentityEditable(db, mbID)` (`collection/collection.go`), the identity-side
  sibling of `FollowGoverns` (`managedBy != lidarr && != mixed`; unknown artist = editable).
  Table-tested.
- **Step 2 — enforce the gate.** `requireIdentityEditable` (`routers/attach.go`, gates
  `attachItem`/`previewBulkAttach`/`attachBulk` on the file's **library** manager type — always
  resolvable even for an unmatched file) and `requireArtistIdentityEditable` (`routers/collection.go`,
  gates `setDesire` on the artist's `managed_by`). Both return 409. **Deviation from the plan:**
  `clearDesire` is left *ungated* — clearing is a pure removal, and a want left from before an artist
  became Lidarr-managed needs a way out (same reasoning that keeps detach allowed). Flag for review.
  UI hide/grey is a separate frontend-repo change (built bundle here).
- **Step 3 — no tag fallback under Lidarr.** `modules.ErrUnmatched` + an `allowTagFallback` mode on
  `ResolveCorrelation` (`modules/files.go`). `LidarrManager.Correlate` passes `false` **only when it
  has a client** (a credential-less Lidarr row keeps the legacy tag fallback, so an outage/misconfig
  does not orphan a library); `AutotaggerrManager` passes `true`. `ProcessFile`/`recordItem`
  (`components/pipeline.go`) map `ErrUnmatched` to `LibraryItemStatusUnmatched` — not counted as an
  error, not tagged, dropped from the collection, re-attempted next scan. A real transport error
  stays `error`. Unit-tested (`TestResolveCorrelationNoFallbackUnmatched`).

All six steps of the plan are implemented in the working tree. Three follow-ups remain, each
planned below.

### Follow-up A — expose editability + the frontend controls

The backend gate (step 2) rejects a forbidden action, but the UI still *offers* it and only learns
it is forbidden from a 409. The fix is to hand the frontend the same predicate as data, then have it
hide the controls and add the repair button. The backend half lives here; the frontend half is a
separate repo (this one ships `web/dist` only), so its deliverable is the API contract plus a
checklist.

Backend (this repo) — **DONE** (working tree):
- `identity_editable` added to the artist view (`newArtistView`), release-group view
  (`newReleaseGroupView`) and library-item rows (`listLibraryItems`, now wrapped in
  `libraryItemView`), all in `routers/`. The item + attach paths share one resolver,
  `API.libraryIdentityEditable` (`routers/attach.go`), so the listing flag and the attach 409 can
  never disagree; the item listing fails closed to not-editable if a manager cannot be resolved.
  Tested (`routers/identity_editable_test.go`): Lidarr artist/library ⇒ `false`, native ⇒ `true`.

Frontend (separate repo — checklist, not code here):
- When `identity_editable` is false: hide/disable manual attach, "choose edition", and the
  want/desire controls; show a short "Managed by Lidarr — change it in Lidarr" hint.
- Add a per-artist **Re-correlate** button calling `POST /artists/:mbid/recorrelate`, behind a
  confirm dialog that says it **discards manual pins** and rewrites the files from Lidarr.
- `clearDesire` (`DELETE /artists/:mbid/desires`) stays available even when locked — it only removes
  a stale want (mirrors detach).

### Follow-up B — force re-correlate at release-group and library scope — **DONE** (working tree)

Backend shipped; no new machinery, just targeting + verbs + endpoints on top of the existing
`Scope.Force` / `prepareForceRecorrelate` / `force`-flag plumbing.

- **`collection.ReleaseGroupTargets`** (`collection/paths.go`, with `ReleaseGroupReleaseMBIDs` /
  `ReleaseGroupItems`): release-group → owned editions → items → **each file's own directory**
  (album / per-disc folder, via `filepath.Dir`), deduped and sorted. Narrower than `ArtistTargets`
  on purpose, so one album's repair does not re-walk the discography.
- **`Runner.ReleaseGroupScope`** (returns `ErrNothingToScan` when nothing owned, the DB error when
  the group is unknown) + verbs **`ForceRecorrelateReleaseGroup`** and **`ForceRecorrelateLibrary`**.
  `ArtistScope` and `ReleaseGroupScope` now share a `buildScope` helper; the three force verbs share
  `enqueueForceRecorrelate` + `forceTitle`. `prepareForceRecorrelate` clears pins per-target scoped
  by `pathInScope` unchanged, so it works for album folders and whole libraries.
- **Endpoints**: `POST /release-groups/:mbid/recorrelate` (param named `:mbid` to match the sibling
  `/release-groups/:mbid/releases`; the value is the release-group MB ID) and
  `POST /libraries/:id/recorrelate` (via `libraryAction`).
- **Tests**: `ReleaseGroupTargets` narrows to album/disc folders and never widens to the artist
  folder (`collection/paths_test.go`); `ReleaseGroupScope` resolves targets/roots and returns
  `ErrNothingToScan` when empty (`scan/runner_test.go`); endpoint contract — 404 unknown, 409 nothing
  owned, 401 unauthenticated (`routers/recorrelate_test.go`).

Frontend (separate repo): expose the two new actions where they belong — a "Re-correlate album"
button on the release-group detail page and a "Re-correlate library" button on the library settings
page — reusing the same discard-pins confirm as the artist button.

### Follow-up C — "any release → this edition" desire migration (native only)

Today a want is either "any edition" (`ReleaseMBID == ""`) or a set of specific editions
(`collection/desire.go:121`). **Decision (locked): silent promotion, self-correcting.** When you
wanted "any" and a file lands, that owned edition *is* the one you have — you accepted any, and this
is the one you got. The narrowed want is not a one-time snapshot: it **reflects what the files
represent**, so if the files are later replaced with a different edition, the want re-points to the
new one. Wanting an *additional* edition is a separate, explicit want layered on top; it does not
disturb the auto-narrowed one. Native-only — a Lidarr artist's edition comes from the monitored
release, not a desire.

The load-bearing piece is that an auto-selected want must be **distinguishable from a hand-pinned
one**, so Rebuild may re-point the former but never clobber the latter. That is the entire difference
between "tracks the files" and "goes stale".

**DONE** (working tree):
- **Schema**: `CollectionDesire.Auto bool` (`models/db.go`, `json:"auto"`, exposed so the UI can tell
  an auto-selected edition from a hand-pinned one). Existing rows migrate to `auto=false` = manual,
  which is correct — they were user-authored.
- **`reconcileAutoDesires`** (`collection/collection.go`) runs last inside `rebuildTx`, after the
  artist/RG upserts so it reads the freshly-computed `managed_by` and owned editions. Algorithm:
  (1) compute which release-groups are "auto-managed" — a native artist with an `any` want *or* a
  prior `auto` want — evaluated **before** any pruning so a re-point doesn't lose its trigger;
  (2) prune `auto` wants whose edition is no longer owned; (3) for each auto-managed group that owns
  something, delete the `any` want and add one `auto` want per owned edition lacking a desire.
- **`SetDesire`** (`collection/desire.go`) clears `Auto` on a hand re-assert, so pinning an edition
  by hand takes it out of the auto path.
- **Multi-edition sub-question — resolved**: own two editions ⇒ one `auto` want each (each represents
  what you have). Tested.
- **Tests** (`collection/desire_auto_test.go`): promote `any`→owned edition; re-point on replaced
  files; leave a `manual` edition untouched; skip a Lidarr artist; one `auto` want per owned edition.

Known edge (documented, acceptable for v1): if **every** file of a promoted album is deleted, its
`auto` wants are pruned and the album becomes un-wanted — the original `any` intent was consumed by
promotion and is not restored. Re-add the want to bring it back. A future refinement could revert to
`any` instead of dropping it.

Frontend (separate repo): the `auto` flag is on each desire; render an auto-selected edition as
state ("you have this edition") rather than an unpick toggle, and keep the explicit "want a specific
edition" / "want another edition" controls behind `identity_editable`.
