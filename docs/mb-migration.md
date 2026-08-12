# Feature: MusicBrainz entity migration

MusicBrainz entities are not immutable. Releases and artists get merged into one another
and, more rarely, deleted outright. Every MBID Autotaggerr stores is a key into its *own*
state — `library_items`, the `collection_*` tables, authored desires, the release cache — so an
upstream identity change that goes unnoticed leaves the app keyed on an ID the service no longer
serves.

This is how that is detected and repaired.

## The two upstream cases

MusicBrainz never says "this ID moved" in so many words. It does two things:

| Upstream | Over the wire | How it looks without handling |
|----------|---------------|-------------------------------|
| **Merged** | **200**, and the payload's `id` is the *surviving* entity, not the one requested | Completely silent. The data is correct, the key is dead. Duplicate rows accumulate for one entity, forever |
| **Deleted** | **404 / 410** | A file marked `error`, indistinguishable from an outage, and a cache entry that expires and is re-fetched on every sync from then on |

The merged case is the dangerous one precisely because nothing fails. It is caught by comparing
the payload's `id` against the ID that was asked for — the only signal that exists.

The deleted case is caught by status code, and the distinction that matters is *not* 404-versus-200
but 404-versus-503: treating an outage as a deletion would un-identify a library because
MusicBrainz had a bad afternoon. `modules.ErrEntityGone` (and `GoneEntity`) exist so callers can
tell the two apart without parsing an error string.

## Coverage: which entity, which change, which mechanism

Not every identity change needs a migration row, and reading the four `RecordRedirect` /
`RecordDeletion` call sites as the whole picture undercounts what is handled. Some cases are covered
by mechanisms that predate this package and are the better answer for them.

| Entity | Merged | Deleted |
|--------|--------|---------|
| **Artist** | migration — id comparison on lookup (`musicbrainz_artist.go:210`) | migration — 404 (`:104`, `:182`) |
| **Release** | migration — id comparison on lookup (`musicbrainz.go:366`) | migration — 404 (`:339`) |
| **Release-group** | *no row*: handled by subtraction — see [below](#release-groups-re-linked-not-remapped) | migration — confirmed 404 (`musicbrainz_search.go:322`) |
| **Recording** | *no row needed*: self-heals through the release payload | same |
| **Release-track** | *not handled* — see [wip.md](wip.md) | same |

Three of those deserve their reasoning stated, because "no migration row" means something different
in each.

**A recording change needs no row, and that is not a shortcut.** Nothing in Autotaggerr is *keyed*
on a recording MBID — it is written to files, never used to look anything up. `hashRelease`
(`musicbrainz_drift.go:16`) marshals the entire release payload, so a changed `track.Recording.ID`
flips the hash, the release is reported as changed, and the drift re-tag rewrites the file from the
new payload. The track lookup that gets it there keys on `track.ID`, which a recording merge does not
move. A migration row would add an approval step to something already correct by the time anyone
could read it.

**A release-group merge cannot be observed at all**, so subtraction is not a preference but the only
option: the merged ID still answers 200, and the editions browse asks for a group's releases rather
than for the group, so there is no `id` to compare. What happens instead is
[re-linking plus pruning](#release-groups-re-linked-not-remapped).

**A release-track change is the gap.** `files.go:290` matches by exact track ID inside the payload,
and `ErrTrackNotInRelease` — the error it raises when that ID is gone — currently has no consumer.
Under a manager the correlation supplies fresh IDs each scan and the problem never appears; without
one, the fallback reads the stale ID from the tags of the very file it is trying to repair, and
fails identically forever. Recorded in [wip.md](wip.md); the remedy the error message names (force
re-correlate) works, but it is manual.

## Detection is separate from application

Detection runs on the fetch path — mid-scan, on a worker goroutine, holding the rate limiter — so
it does one thing: write a `musicbrainz_migrations` row (`RecordRedirect` / `RecordDeletion`).
Deciding what a merge *means* and rewriting rows accordingly happens at a run boundary, where a
transaction and a collection rebuild are affordable.

Re-detection is the normal case, not an edge case: every subsequent fetch of the old ID sees the
same redirect until the migration is applied. The unique index on `(entity_type, old_mb_id)` makes
the repeat a no-op.

A merged release is cached under **both** keys. The caller is mid-scan holding a file that claims
the old ID, and failing its lookup to make a point about identity would break tagging for a file
whose metadata is in hand. The old key is dropped when the migration is applied.

**Detection is opportunistic by default.** Everything above rides fetches the app already makes.
Since the drift sync re-reads every cached release on its 7–14 day TTL, full coverage of releases
arrives within a fortnight at zero rate-limit cost.

Artists used to be the exception. Nothing re-read an artist on a schedule the way the drift sync
walks releases, so a merge could sit undetected until somebody opened their page — and
`modules.VerifyArtistIdentity` closed that gap by dropping an artist's cached lookup and re-reading
it, called from `collection.SyncArtist`.

**That gap is closed by the refresh pass instead, and the function is gone.** `CollectionScope`
covers every artist in the collection (`PhaseArtists`), so each is re-read on its TTL like any
release, and the redirect is recorded on the HTTP path by that fetch. What remained of the old
mechanism was a follow toggle spending a rate-limited request and discarding the stale fallback with
it — a cache reset nothing in the UI announced, for coverage that already existed. A deliberate
re-read is still available and is the forced pass, which **expires** rather than forgets, so a
failed re-read still leaves something to serve.

### The manual sweep

`POST /migrations/verify` ("Check every ID now") re-reads every MBID the collection is keyed on:
every distinct release in `library_items` and `collection_releases`, and every `collection_artists`
row. Nothing is deduplicated against the cache — whether an ID still resolves is precisely what a
cached answer cannot settle.

It is a button rather than a schedule because of what it costs: one rate-limited request per
release plus one per artist, so a large collection is measured in hours. It shares the scan
run-guard (it saturates the same limiter), returns **202** immediately, and reports through the
Activity feed as an `mb_migration` event. Releases go through the same path a drift sync uses, so a
release found to have *changed* while being verified is re-tagged rather than merely noted —
verification and refresh are the same request, and splitting them would double the cost to learn
less.

## Review and policy

A detected migration is `pending`. It is applied immediately unless its category is held for
review, in which case it waits for a human on the **Migrations** page.

These four live on **/settings** under *Metadata migrations*, not on the data source that reported
the change. Holding a merge for approval is a decision about this library, so it holds across every
configured metadata source and stays meaningful with none — see
[settings.md](settings.md#sections-are-named-for-what-they-govern-not-who-supplies-the-data).

| Config key | Holds |
|------------|-------|
| `autotaggerr_migration_review_releases` | merged releases |
| `autotaggerr_migration_review_artists` | merged artists |
| `autotaggerr_migration_review_pinned` | anything touching a manual attachment, whatever its type |
| `autotaggerr_migration_review_deletions` | deletions |

All four default to **false — apply**. They are phrased as *review* opt-ins rather than auto-apply
opt-outs for a specific reason: a bool absent from an existing `config.json` decodes as `false`, so
upgrading cannot silently start queueing every merge for an approval nobody knows to give.

Approving from the UI does not re-consult the policy — an explicit approval *is* the decision the
policy was deferring. Dismissing keeps the row rather than deleting it, because a deleted row would
let the next fetch of the same old ID re-detect the identical move and re-queue it.

## What applying one does

Everything happens in a single transaction. A half-remapped merge would leave some tables keyed on
the old ID and some on the new, which is worse than not having started.

The tables divide into two kinds, and the division drives every rule below:

- **Derived state** (ownership counts, which release-group is owned) is rebuilt from disk by
  `collection.Rebuild` after every scan and sync. It does not need careful merging; it needs the
  stale row *gone* so Rebuild can recompute cleanly.
- **Authored state** (desires, monitoring, follow types) exists nowhere else. Losing it is the one
  genuinely unrecoverable outcome available here, so merges always **union** rather than pick a
  winner.

### A merged release

- `library_items.mb_release_id` is re-pointed, **including pinned rows**. A merge renames an entity
  rather than substituting a different one, so the release the user chose by hand *is* the survivor;
  leaving the pin on a dead ID would quietly break the one file they took trouble over.
- `library_items.processed_version` is **blanked** at the same time. Track and recording MBIDs are
  scoped to the release they came from, so a merge leaves those columns pointing into a release that
  no longer exists — and nothing in the transaction can derive replacements without fetching the new
  release and re-matching every track, under the rate limiter, mid-sync. Blanking the version trips
  the existing skip-unchanged escape hatch instead: the next scan re-correlates exactly these files
  and writes the correct track IDs.
- `collection_releases` moves to the new MBID, or is **dropped** if a row already exists there — the
  common case, since files were owned under both IDs. The column is uniquely indexed, so this is a
  merge, not an update.
- `collection_desires.release_mb_id` follows, then duplicates collapse (below).
- The old cache row is dropped.

### A merged artist

- `collection_artists` merge with every authored field unioned: monitored if **either** side was,
  follow-secondary likewise, follow types set-unioned, and a `manual` origin outranking a
  library-derived one. A merge is not an occasion to quietly stop following someone.
- `collection_release_groups`, `collection_releases` and `collection_desires` re-point their
  `artist_mb_id`.
- `collection_release_group_artists` is the one table where a blind `UPDATE` *errors* rather than
  merely being wrong: its composite unique index on `(release_group, artist)` collides on a
  collaboration credited to both sides of the merge — which is what a merge means. Rows are moved
  individually and a colliding one is dropped in favour of the existing row, keeping the more
  prominent (lower) credit position of the two.

### A deletion

Nothing is destroyed:

- Files become **`unmatched`** — the status that already means "this needs identifying", so they
  land in the same queue as a file that never matched, rather than in the error bucket next to
  genuine failures. (This is the first writer of that status; it existed as a filter with nothing
  setting it.)
- The MB IDs on the file are **kept**. A dead ID is still the best available record of what the file
  was thought to be, and it is what makes the deletion diagnosable afterwards.
- The owned-edition row goes, so a release that no longer exists stops counting toward a complete
  album.
- **Desires are never touched.** A release disappearing upstream is exactly the moment the user's
  stated want becomes the only surviving record of what they were after.

A deleted *artist* orphans no file — files are keyed by release, and MusicBrainz re-credits releases
rather than deleting them alongside the artist — so only the artist's own collection row goes.

### Duplicate desires

Wanting an album under two IDs that turn out to be one album is one want. Recording selections are
unioned, and an empty selection ("the whole thing") subsumes a partial one rather than being
narrowed by it.

## Release-groups: re-linked, not remapped

A release payload naming a different release-group than the one on record is **ambiguous**: it can
mean the two groups were merged, or that this single release moved between groups. The payload
cannot tell them apart.

So `migration.RelinkRelease` re-points only the release in hand, which is correct under both
readings, and lets `collection.Rebuild` recompute the group's ownership from there. Remapping the
group globally would be right for a merge and destructive for a move. This case therefore produces
no migration row and needs no approval — it is the same class of thing as re-tagging from a changed
payload.

### Pruning groups that merged away

Re-linking moves the *releases*, but the old release-group row stays behind. There is no redirect to
observe when two release-groups are merged: a merged group still resolves, because MusicBrainz
follows its own redirect internally and answers 200 with the survivor. The old group simply stops
appearing in the artist's discography.

(Release-groups are fetched by ID in exactly one place — the confirmation probe described in
[Groups that resolve nowhere](#groups-that-resolve-nowhere) — and that probe cannot see a merge for
the reason just given. A 200 is a 200 whichever group answered it.)

`collection.PruneOrphanReleaseGroups` therefore works by **subtraction**, which makes its guards the
whole design. Absence is weak evidence, so a row is removed only when every innocent reading is
ruled out: it is absent from the live discography, nothing on disk owns it, no manager lists it, no
desire references it, no *other* artist is credited on it, and no edition row points at it. What
survives all six is an album nobody owns, no manager knows, nobody asked for and no artist is
credited on.

Two refusals matter as much as the guards:

- **A truncated discography is not evidence.** Paging stops at
  `maxArtistReleaseGroupPages` (500 groups), and against a truncated list "absent" means "past page
  five". `GetMusicBrainzArtistReleaseGroups` returns a `complete` flag for exactly this caller; the
  prune is skipped unless it is true, and an error yields `complete=false` too, since a discography
  that failed halfway is indistinguishable from a short one. The flag is **cached with the list**
  (see [mirror.md](mirror.md#the-entity-cache)) so that needing it is not a reason to bypass the
  cache — which is what it used to be, at 1–5 rate-limited requests per follow toggle.
- **An empty discography is not evidence either.** It is far more likely a service quirk than an
  artist whose entire catalogue was merged away, so it prunes nothing.

Since the discography is read through the cache, the list a prune runs against may be days old, and
a release-group added upstream in the meantime is "absent" from it. The six guards are what make
that safe rather than the freshness of the list: such a group is owned, or listed by a manager, or
desired, or credited elsewhere — any one of which stops the prune. A group that satisfies none of
them is one nobody has heard of, cached copy or not.

The prune also runs against the **unfiltered** discography, not the follow-filtered subset
`SyncArtist` upserts — comparing against the filtered set would delete every release-group of a type
the user does not follow.

### Groups that resolve nowhere

A separate case, on much stronger evidence: a release-group whose ID returns **404**.

Most of these are not MusicBrainz's doing. A manager mirrors its albums into the collection keyed by
whatever ID it holds (`collection.SyncManagers` writes Lidarr's `foreignAlbumId` verbatim), and an ID
that manager's own metadata service has since dropped or re-keyed is indistinguishable here from one
MusicBrainz deleted. Both mean the group cannot be read. The album itself is often alive and well
upstream under a *different* ID.

**Detection.** The signal comes from the editions browse,
`modules.GetMusicBrainzReleaseGroupReleases`. A 404 there is not enough on its own — a browse is a
query, and a query returning nothing is not testimony about its filter — so
`confirmReleaseGroupGone` asks `/release-group/<id>` directly and records a
`MigrationEntityReleaseGroup` / `MigrationKindDeleted` row only if that also 404s. One extra
rate-limited request per suspect, spent once: the migration row is what stops the next pass asking
again. A confirmation that fails *transiently* records nothing, since an outage is not evidence.

**Suppression.** Recording is only half of not re-asking. Every scope is built from the collection's
own rows, and a group awaiting review is still one of those rows, so `mirror.retiredGroups` excludes
any group with a recorded deletion from `CollectionScope`, `ArtistScope` and `LibraryScope`. Every
status counts, dismissed and failed included: both mean "do not retire this row", which is a
decision about the collection and not a claim that the ID resolves.

**Repair comes before retirement**, because most of these albums are not gone — they are *re-keyed*,
and the manager already knows the new key. See [Repairing through the
manager](#repairing-through-the-manager) below; nothing is retired until that has been tried.

**Application.** `collection.RetireReleaseGroup` is `PruneOrphanReleaseGroups`' sibling, separated by
the strength of the evidence rather than by what it deletes: a direct 404 needs no discography fetch,
so a single row can be retired on demand. It keeps every guard prune applies — files on disk, an
authored want, another credited artist, an owned edition, and `in_catalog`.

`in_catalog` is there for a blunter reason than prune's. Prune defers to the manager as a competing
authority on what exists; this does not, because an ID that resolves nowhere cannot be read whoever
lists it. It defers because `SyncManagers` upserts a row for every album the manager reports, so
deleting one the manager still lists achieves nothing — the next sync restores it. The album has to
stop being listed *there* first. The refusal says so, since the user's next move is in the manager.

**A refused retirement is "not yet", not "no".** `ProcessPending` re-picks failed release-group
deletions alongside pending ones, and re-attempts them once `collection.ReleaseGroupRetirable` says
the blocker has cleared — which is exactly what a manager refresh a run or a week later brings about.
The check and the retirement share `releaseGroupRetirementBlock`, so a retry cannot test different
conditions from the act and end up either looping on a row it can never remove or skipping one it
could. A row still blocked is left untouched: no counter movement, no rewritten reason, no churn on
every nightly pass.

The retry is scoped to *retirements*, deliberately. Every other migration fails for a reason a retry
cannot change — a redirect with no target stays targetless — so re-attempting those would be pure
noise.

**Held for review until a repair has been tried.** This is the one deliberate exception to the
zero-value-means-apply convention under [Review and policy](#review-and-policy), and it expires. The
convention is safe where a deletion is MusicBrainz's own act, because then there is nothing to
recover; here there usually is, so applying unattended would remove an album one manager refresh
would have fixed. Once `RepairAttemptedAt` is set the objection is spent — the manager has re-read
the artist and either corrected the ID or stopped listing the album — and the row auto-applies. What
is left in the queue is the genuinely unidentifiable minority, which is a queue worth reading.

**Reporting.** `collection.GhostReleaseGroups` counts the groups a manager still lists whose ID does
not resolve, and the Lidarr sync reports them as *Not in MusicBrainz* beside its existing *Not in
Lidarr* finding. The metadata refresh can only show the symptom, one failed row at a time; the sync
is where the IDs came from.

### Repairing through the manager

`collection.RepairGhostReleaseGroups` asks the manager to re-read the artists holding unresolvable
IDs, then re-mirrors them. Measured against a live Lidarr, this is what actually fixes them:
`Heatstroke` moved from an ID that 404s to `f71cd67f-bf59-48e6-a89f-359a26e7e977`, which resolves,
and three Sabrina Carpenter `Alien` variants likewise picked up live MBIDs. Albums that genuinely
exist nowhere are dropped instead. Either outcome is progress; both are the manager's call, not a
guess made here.

The order is not negotiable — **refresh, wait for the command, re-sync**. Re-syncing early mirrors
the same stale catalog back and concludes nothing changed; retiring early deletes the row that was
about to be corrected. This is why the stage runs before `applyMigrations` in a run.

Constraints, all of which are the point rather than caution for its own sake:

- **One refresh per artist**, not per album. A single artist held eight dead IDs on the instance this
  was built against, and one refresh answers for all of them.
- **A cooldown** (`repairCooldown`, 7 days) keyed on `RepairAttemptedAt`, stamped whatever the
  outcome. Without it, an album absent everywhere triggers a manager refresh on every run forever. A
  newly discovered ghost on an already-asked artist ignores the cooldown, since the refresh covers
  the whole artist anyway.
- **Full runs only.** A one-artist button must not set the whole collection refreshing in Lidarr.
- **Opt-out per manager** (`Manager.LidarrSkipArtistRefresh`), for a deliberately read-only API key
  or where something else owns refresh scheduling. Phrased negatively so the zero value is "allowed"
  — see the field comment for why a `default:true` tag would not have worked.

This is the only write Autotaggerr makes to a manager, and `modules/lidarr_command.go` explains why
it is the only one that earns it: it does not tell Lidarr what its data should be, it asks Lidarr to
reconcile against its own metadata source — the same operation its scheduled task and its Refresh
button perform. Autotaggerr supplies only the timing, and the timing is the whole value, because
Lidarr's scheduled refresh is throttled per artist. On the instance measured it ran at 05:04 and left
two artists holding dead IDs hours later.

Two things deliberately **not** built. Deleting the album from the manager would destroy exactly the
rows a refresh repairs. Searching MusicBrainz by title and taking the best hit would infer an answer
the manager can state — and badly: on these rows the correct matches scored 78–90, below any
sensible confidence floor, with titles differing by Unicode punctuation and case (`Alien (M‐22
remix)` with U+2010 against `Alien (M-22 Remix)`).

## Where it runs

`process.Runner.applyMigrations` drains the queue at the end of a scan and of a drift sync — both fetch
releases, so both detect. It runs there rather than at the point of detection because applying one
rewrites MB IDs across several tables; doing that between files would interleave schema-wide
rewrites with tag writes. A run that detects nothing costs one indexed query over an empty set.

Counts ride the run's Activity event as `migrations`, alongside `releases_gone` and
`releases_relinked` — both of which are invisible in the file counts, since neither needs a single
file to change.

## API

| Endpoint | Purpose |
|----------|---------|
| `GET /migrations` | all migrations, newest first (`?status=pending` for the queue) |
| `GET /migrations/policy` | which categories are currently held for review |
| `POST /migrations/:id/approve` | apply one, then rebuild the collection |
| `POST /migrations/:id/dismiss` | record it as deliberately not applied |
| `POST /migrations/verify` | sweep every stored MBID now (202; runs in the background) |

## Related

- [scanning.md](scanning.md) — the drift sync this rides on, and the skip-unchanged gate the
  blanked `processed_version` deliberately trips.
- [collection.md](collection.md) — the disk/catalog split that makes derived state safe to drop and
  authored state dangerous to lose.
- [attach.md](attach.md) — where an un-identified file goes to be re-attached.
