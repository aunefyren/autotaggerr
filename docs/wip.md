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

- **Detach a manager, keep its decisions.** The point of the mirrored wants that just shipped (see
  [collection.md](collection.md#manager-derived-wants)): Lidarr's selections are now Autotaggerr's
  own rows, so detaching is a change of authority rather than a loss of data. The verb is: flip the
  artist's `managed_by` to native, convert its `manager` desires to `manual`, leave following off.
  Correlations are already in `library_items`, so nothing else is at risk. Deliberately does *not*
  invent follow settings — Lidarr monitors per album, not by rule, so a follow rule at detach time
  would be fabricating intent the user never expressed. Still needs a verb, a confirm dialog, and a
  decision about what happens to the `Manager` row itself when the last artist leaves it. Until it
  exists, removing a Lidarr manager leaves its mirrored wants in place (nothing reconciles them
  without a manager to sync), which is the safe half of the behaviour but shows a `manager`
  provenance for an authority that is gone.
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

## Metadata refresh: one name, one default

An audit of every surface that reaches MusicBrainz found two problems. The verb has five user-facing
names, and — worse — the same words mean different things on different pages: on the Metadata page
*Refresh metadata* honours the cache unless a box is ticked, while on Artist, Libraries and
Migrations the identical words are an unconditional forced re-read. A user who learns one meaning
misjudges the other three, and the expensive reading is the one that is silent.

Nothing on a schedule forces (nightly `SyncDrift`, the weekly scan's `DueScope` stage, both startup
runs) — that property is the thing to protect, not to fix.

**The two rules this work establishes:**

1. **One verb, two forms.** Shipped — the rule and what it renamed now live in
   [mirror.md](mirror.md#one-name-two-forms).
2. **Ignoring the cache happens in exactly one place, and only on purpose.** The *place* is done —
   `CollectionScope`'s `force` is now the only thing that sets `Scope.Force`, and a test holds that
   line; see [mirror.md](mirror.md#scopes) and
   [mb-migration.md](mb-migration.md#detection-is-separate-from-application). Making it *on purpose*
   is pass C.

### Pass C — forcing is always deliberate

- The Metadata page keeps *Ignore cached copies*, but a forced pass goes behind a **confirm dialog**
  that states the cost in the terms that matter (one rate-limited request per entity; hours on a
  large collection; reads only, no files written), and the checkbox **resets to off** after a pass
  starts so it cannot persist into a later click.
- The Migrations page's forced pass routes through the same dialog rather than duplicating the verb
  with different semantics — finding merges is why the option exists, so it keeps a way in. Its
  label already says **Refresh metadata (ignore cache)**; the dialog is what makes it deliberate.
- After B and C, forcing is reachable from two buttons, both of which say so and both of which
  confirm — and from nothing else at all.

## Frontend follow-ups

The manager-authority boundary is now honoured end to end (see
[collection.md](collection.md#the-ui-under-a-manager)). What is left:

- **Re-correlate buttons**, behind a confirm dialog that says it **discards manual pins** and rewrites
  files from Lidarr: per-artist (`POST /artists/:mbid/recorrelate`), per-album on the release-group
  page (`POST /release-groups/:mbid/recorrelate`) and per-library on library settings
  (`POST /libraries/:id/recorrelate`). These are the *action* half of the boundary — the read-only
  half (naming the authority, freezing what it owns) has shipped, and this is what a user does when
  Lidarr's answer and the files disagree.
- **Clear a stale want under a manager.** `clearDesire` (`DELETE /artists/:mbid/desires`) is
  deliberately ungated — it is a pure removal, like detach — but nothing in the UI offers it on a
  locked surface, so a `manager` want left behind by a removed manager cannot be dismissed from the
  page. Worth doing with the detach verb above, since they are the same need.
- **Surface the scan's metadata-refresh stage in Activity.** A scan runs an inline metadata refresh
  as its first phase (no separate event — see [scanning.md](scanning.md) / the runner's phases), and
  the backend now records it two ways on the `scan` event, both currently unrendered:
  - The event **summary** gains a clause when the refresh did something, e.g.
    `… · 4 releases refreshed, 2 changed upstream` (absent on a no-op scan). Already shown verbatim,
    so this needs nothing — noted for awareness.
  - `details.refresh` holds the counts (`checked`, `fetched`, `fresh`, `changed_releases`,
    `gone_releases`, `relinked`, `files_retagged`) — render it as a small "Metadata" section on the
    scan detail view.
  - Detail rows now include **release rows** (`status: "refreshed"`, `phase: "refresh"`), whose
    `path` is the release MBID and whose `tags_written` is how many of that release's files the drift
    stage re-tagged. Render these distinctly from file rows (a release changed upstream, not a file),
    ideally linking the MBID and grouping/omitting by `phase`. File rows keep `phase: ""`.
- **Surface the scan's collection stage in Activity.** Same shape as the refresh stage above: the
  summary line already gains a `· N credit change(s)` clause verbatim, but `details.credit_changes`
  (an album moved between artists upstream — see
  [collection.md](collection.md#saying-that-it-happened)), `details.files_removed` (index rows for
  files that are gone — [scanning.md](scanning.md#pruning-files-that-are-gone)) and `details.mirror`
  (`artists`/`albums` mirrored from Lidarr, absent on a narrowed scan) are all unrendered. A credit
  change is the one worth a real affordance: it is the only identity change with no Migrations row to
  click through to, so the count is currently the *only* way to notice one.

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
- *Multiple artists on album*
  We do not tag multiple artists on albums, because Plex does not allow it.
  We should make this configurable in the Tagger, default off.
- *MusicBrainz settings on /settings*
  Autotaggerr allows multiple metadata sources, so this does not make sense
  These are not tenant wide settings
  Should probably be moved to the data-sources page, applying only to the selected metadata source
- *Does the collection page work with several libraries?*
  The page seems very one dimensional, with dynamic buttons.
  What happens if I have multiple libraries, perhaps either Lidarr or Autotaggerr managed?
- *I can add artists on a collection where Lidarr is the only manager*
  Or, at least the button is there.
  Does that make sense?
  
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
