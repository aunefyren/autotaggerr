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
| **Artist** | migration — id comparison on lookup (`musicbrainz_artist.go:210`). A **manager re-key** gets a redirect row, always reviewed — see [below](#a-manager-re-keying-an-artist) | migration — 404 (`:104`, `:182`) |
| **Release** | migration — id comparison on lookup (`musicbrainz.go:366`) | migration — 404 (`:339`) |
| **Release-group** | *no row* for a MusicBrainz merge: handled by subtraction — see [below](#release-groups-re-linked-not-remapped). A **manager re-key** gets a redirect row — see [below](#a-manager-re-keying-an-album) | migration — confirmed 404 (`musicbrainz_search.go:322`) |
| **Recording** | *no row needed*: self-heals through the release payload | same |
| **Release-track** | *no row*: re-matched in place — see [tagging.md](tagging.md#a-track-that-left-its-release) | same |

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

**A release-track change needs no row either.** It is re-matched where it is found, by recording or
by position confirmed by title, for files identified by their own tags — the only ones it used to
strand ([tagging.md](tagging.md#a-track-that-left-its-release)). A manager supplies the fresh pair
itself.

**A release merge under a manager is matched by recording.** A manager does *not* always supply
fresh IDs: Lidarr's metadata server lags MusicBrainz, so after a release merge it keeps reporting
the merged-away release and that release's track IDs. MusicBrainz answers the old release ID with
the survivor, whose tracklist carries only the survivor's track IDs, so the exact match misses on
every pass — and applying the migration does not help, because the next pass reads the stale pair
from the manager again. When the fetched release's `id` differs from the one asked for,
`matchCorrelatedTrack` (`modules/files.go`) therefore falls back to the correlation's recording ID,
then its track title, each required to name exactly one track. The file is written with the
survivor's IDs. A miss under a redirect fails with its own message — re-correlating would only
fetch the same stale pair again. The index row still stores the manager's (old) IDs, since
`recordItem` records the correlation as the manager supplied it; that converges once the manager
catches up.

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
review, in which case it waits for a human on the **MusicBrainz changes** page (`/migrations`).

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

### Status says where it ended up; resolution says who put it there

`status` is the lifecycle — `pending`, `applied`, `dismissed`, `failed`, `resolved` — and
`resolution` is the second half of the same fact: `approved` (a person pressed the button),
`automatic` (policy did not hold it), `dismissed`, `external` (nothing here applied it).

The pair exists because the history was unreadable without it. An album a manager repaired and an
album somebody approved by hand both read as *Applied* with a timestamp, which answers neither of
the two questions a history is opened for — *what did it decide while I was not looking*, and *why
did this one settle itself*. `resolution_detail` carries the sentence for the cases where the word
is not enough, and `resolved_at` is when it happened: a dismissal recorded none of the three, so the
history had an empty date column and an ordering that fell back to detection time — which follows
the order a sweep walks artists in, and therefore reads as alphabetical.

**Every settled row carries a sentence, applications included.** `migration.appliedDetail` writes it
in the past tense from what the transaction actually did, because status and resolution between them
say where a row ended up and who put it there and neither says *what was done* — and an album
vanishing from the collection view reads very differently once the line beneath it says no file was
touched. A retirement's sentence names which of the two roads it took, since both end in the same
deletion: the manager was re-read and let go of the album, or no manager ever listed it. A reader who
cannot tell those apart has to go and check Lidarr. This is deliberately not the review payload's
`effect`, which is an offer composed before the fact ("Approving removes…") from counts that were
predictions.

**A failed row is not history.** Its refusal is usually *not yet* (see
[Groups that resolve nowhere](#groups-that-resolve-nowhere)), so it sits in the queue with the
pending ones, where the blocker it is stuck on is visible and re-tried. `migration.StatusOpen` and
`StatusClosed` are the two lists the page reads, and neither is a stored status.

### Rows that settle themselves

The queue is not the only thing that can end an identity change. A manager re-keys an album and the
row it was holding a dead ID for stops existing; a prune takes a release-group; a scan re-correlates
the last file pointing at a merged release. What is left is a migration describing a change to state
nobody holds — and applying it would rewrite nothing while reporting an application.

So `ProcessPending` asks `mootReason` before it does anything else with a row: is anything still
keyed on this ID? For an album that is its own collection row (a retirement's whole content is
deleting it); for an artist and a release, every table that references the ID plus the files. Absence
is read as *nothing to do*, never as *do something*, so being wrong leaves a row queued rather than
closing one that mattered. What survives is closed as `resolved` / `external` with the sentence
saying which, and the cached entity is dropped exactly as an application drops it.

Two refusals keep that from being too clever:

- **Malformed is not moot.** A redirect with no target can never be applied, and closing it because
  nothing references it today would file a data problem under *resolved itself*. It fails, and the
  row says why.
- **Re-detection re-opens it.** Closing drops the cache, so nothing should ask for that ID again —
  and if something does, the judgement that there was nothing to migrate no longer holds.
  `modules.reopenIfClosedExternally` puts the row back in the queue. Narrowly: an applied row must
  not re-open (the remap happened, and the old ID resolving is what a merge *means*), and a
  dismissed one must not either, since re-raising a declined change is the nagging that was declined.

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
  and writes the correct track IDs. Under a manager that re-correlation hands back the manager's
  IDs, which can still be the merged-away pair; the tag write then matches by recording instead
  ([coverage](#coverage-which-entity-which-change-which-mechanism)).
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
desire references it, and no edition row points at it.

**A collaboration loses one credit, not the row.** If another artist is still credited, prune
withdraws only *this* artist's link (moving the primary-credit column to the next credited artist
when this one held it, since `ReleaseGroupsForArtist` unions the column with the links). Absence from
one discography says nothing about the other's — but it does say this artist's claim is gone. It
used to skip the group outright, which deadlocked: each credited artist's prune deferred to the
other, so a dead collaboration stayed on both pages forever. Now the last credited artist's prune
removes the row.

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

**It runs on every metadata refresh, not only on a sync.** It used to be called from `SyncArtist`
alone — per artist, pressed by hand — so an artist nobody synced kept albums merged away upstream
indefinitely, and the reason given was that pruning everyone would cost a discography fetch per
artist. But the refresh pass already re-reads every collection artist's discography on its TTL
([mirror.md](mirror.md#the-refresh-pass)); the cost was never the fetch, only where the call was
made. `mirror.Runner.pruneDiscography` prunes against each discography the pass actually re-fetched
— a fresh cache hit is skipped, and a stale fallback served because MusicBrainz was down comes back
`complete=false` and prunes nothing — and the event counts it as *Albums pruned*.

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
so a single row can be retired on demand. It keeps prune's guards — files on disk, an authored
want, an owned edition, and `in_catalog` — but **not** the co-credit one: a 404 is about the ID, so
another artist's credit points at the same dead ID and protects nothing. The group goes with every
credit link. Each remaining blocker has its own sentence, so a held row names the one claim to
clear.

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

**Held for review only while a repair is possible.** This is the one deliberate exception to the
zero-value-means-apply convention under [Review and policy](#review-and-policy), and it expires. The
convention is safe where a deletion is MusicBrainz's own act, because then there is nothing to
recover; here there usually is, so applying unattended would remove an album one manager refresh
would have fixed.

Two things end the hold, and both have to, because it is on the *possibility* of a repair rather than
on the ceremony of having tried one:

- **The manager has been asked** (`RepairAttemptedAt`). It re-read the artist and either corrected
  the ID or stopped listing the album, so a row still pointing at an unresolvable ID is genuinely
  dead.
- **No manager lists the album** (`migration.repairable`, which is the live `in_catalog` flag). Then
  there is nobody to ask and nothing to recover.

The second condition is not a refinement; without it the rule **deadlocked**. The repair pass takes
its candidates from `collection.GhostReleaseGroups`, which selects albums a manager *still lists*, so
an album outside the catalog could never be stamped — and a hold that waited for the stamp waited for
an event that could not occur. The row sat in the queue until a person pressed Apply, which did
exactly what the drain would have done unattended. The commonest way to reach that state is the
repair working: the manager drops the album, `in_catalog` clears, and the row that was one refresh
from settling itself became one that could only be settled by hand.

The flag is read live rather than snapshotted at detection, because an album leaves a manager's
catalog between runs and a stored copy would hold a row on a claim that expired weeks ago. A read
error reports *repairable* — the answer that keeps the row queued: being wrong that way costs a
press, being wrong the other way retires an album on a database hiccup.

What is left in the queue is then only what a person can actually change: an album a manager still
lists (one press, which asks it), and one something else claims (a file, a want, an edition —
remove the claim). That is a queue worth reading.

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
- **Full runs only** *for the unattended pass*. A one-artist button must not set the whole collection
  refreshing in Lidarr. Approving a blocked album is the one scoped entry point — see
  [Approving a blocked album](#approving-a-blocked-album).
- **Opt-out per manager** (`Manager.LidarrSkipArtistRefresh`), for a deliberately read-only API key
  or where something else owns refresh scheduling. Phrased negatively so the zero value is "allowed"
  — see the field comment for why a `default:true` tag would not have worked. When **every** enabled
  Lidarr manager has opted out, there is nobody to ask, so the review stops offering *Ask the
  manager*: `collection.RefreshTurnedOff` names those managers, and the row becomes stuck with a
  blocker naming the setting — *artist refresh is turned off for "Main Lidarr"* — the way every
  other blocker names its cause. It used to queue a repair that skipped every manager and did
  nothing. The hold itself is right: the manager is the authority and still lists the album.

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

### A manager re-keying an album

A manager can move an album from one **valid** ID to another. Nothing 404s, so nothing above caught
it: the sync cleared `in_catalog` on the old row and created the new one, and the old row survived as
a real release-group with no files and no catalog entry — a silent duplicate on the artist page,
with any want for the album stranded on it.

`collection.detectRekeys` catches it in the Lidarr sync, from the one place both halves are visible:
the artist's catalog before the pass (`catalogSnapshot`, read just before it is cleared) against what
the manager lists now. An album that **left** while one with the **same title and type entered** —
title folded loosely, since a re-key is often the manager adopting a differently spelled record — is
a re-key, but only one-to-one: two albums leaving under one title could pair either way, and pairing
wrongly would hand one album's wants to another.

Each becomes a release-group **redirect** row (`Source = lidarr`, labelled *re-keyed* on the
Migrations page). It is a row rather than an immediate rewrite because it may have to wait: files
still carrying the old ID resolve to the old album until the next run re-correlates them. Applying it
(`applyReleaseGroupRedirect`) moves the wants to the new ID — deduplicated, authored intent
outranking derived — then retires the old row through `RetireReleaseGroup`, every guard included.
Files on disk refuse it, the transaction (wants and all) rolls back, and the row is retried on a
later run; the retry check (`ReleaseGroupRetirableAfterRekey`) looks past the want claim, since the
want is what the migration carries. A redirect is never moot while a want still points at the old ID,
even once the row is gone.

**What the sync cannot pair, a person can.** A re-key that lands while a sync fails, one that
renames the album at the same time, or one that collides with a second same-titled album is left
unpaired, and the duplicate stays. The album page offers **Same album as…** on exactly that shape —
an album the manager no longer lists, under an artist it manages — with the artist's albums the
manager does list to choose from. `collection.MarkSameAlbum` writes the same redirect row with
`Source = manual` (labelled *You*), refusing an album the manager still lists (the next sync would
bring it back), two different artists' albums, or an ID that already has a settled change, and
`POST /artists/:mbid/release-groups/:rgid/same-as` applies it at once. If files still resolve to the
old row, the row is recorded and waits on Migrations with that reason, like any other.

A pending or failed **deletion** of the same old ID is turned into the redirect. That is the repair
pass's success case — the ID resolved nowhere, the manager was asked, and it re-keyed the album — and
the deletion would have retired the row while stranding its wants. A settled row is left alone.
Ghost-only machinery (the repair cooldown, the *asking the manager* mark) ignores redirects.

### Approving a blocked album

`POST /migrations/:id/approve` on a release-group deletion the manager still lists **does not apply
anything**. It queues `process.Runner.RepairArtistAlbums` for that album's artist and answers **202**.

That is the only reading of the press that makes sense. The refusal it replaced — *the manager still
lists this album; refresh the artist there first* — stated the fix and then made the user go and
perform it by hand in Lidarr, which is the one step Autotaggerr can do for them and the only step
that can settle the question. Approve means "deal with this"; the thing that deals with it is the
manager.

The scoped repair differs from the nightly pass in exactly two ways, both because a person asked:

- **One artist** (`collection.RepairOptions.ArtistMBID`), narrowing both the ghost list and the
  artist list. `Repaired` is derived by re-reading the ghost set afterwards, so leaving other
  artists' ghosts in the candidate list would credit this pass with albums another run fixed.
- **The cooldown is ignored.** It exists to stop an unattended pass re-asking every run; a button
  press is not an unattended pass, and "come back in seven days" is not an answer to it.

The job then drains the queue and rebuilds, in the same order a full run uses, so the album is
retired in the same pass if the manager stopped listing it — and the row stays `pending`, untouched,
until the manager has actually answered. It is **queued rather than inline** because
`WaitForCommand` blocks for up to three minutes, far longer than an HTTP request should be held
open; the queue's dedup makes a second press a no-op, and the whole thing reports as one parent
event in Activity.

**The press has to show.** A 202 that leaves the row exactly as it was, with both buttons still
live, is indistinguishable from a button that does nothing — which is what it looked like. So
`RepairQueuedAt` is stamped before the job is enqueued (the worker may start before the handler
returns) and cleared in a `defer` when the job ends, whatever it concluded. It is stamped on **every
open row of that artist's**, not the one that was pressed: one refresh answers for all of them, and
the first thing anyone does with a queue of eight albums by one artist is press the second one too.
The review payload's `artist_open` says how many that is, so the page can state it before the press
rather than after.

The mark is durable rather than client-side, so a reload or a second tab shows it — and therefore
needs the same reconciliation an event does: `migration.ReconcileQueued` clears it at startup beside
`events.ReconcileRunning`, since nothing is running then and a surviving mark can only be a lie.

**Clearing it takes two writers, because the job cannot reach the rows it settled.**
`ClearRepairQueued` scopes by artist, and the only route from a migration row to an artist is the
album — which retiring deletes. So the rows the repair *succeeded* on were exactly the ones the
deferred clear missed, and they sat in the history saying *Asking the manager…* until the next
restart: success left the spinner. Settled rows therefore clear their own mark as they close (`apply`,
`closeExternally`, `Dismiss`), the job's `defer` clears the ones still open, and the page ignores the
mark on anything closed — an outcome is a statement about work that finished, and it outranks any
claim that work is in flight.

### A manager re-keying an artist

The same thing one level up: Lidarr moves an artist to a new MBID. The old one stops being listed
and the new one is never picked up, because `SyncLidarrWith` only asks about artists the collection
holds — so the artist used to show as *Not in Lidarr* and the user was left to work out that the
two were one act.

The sync now also collects every artist a manager lists that the collection does not hold, and
`detectArtistRekeys` pairs them with the pass's unknown artists **by name** (`CanonLoose`),
one-to-one. Each pair becomes an artist **redirect** row (`Source = lidarr`), and is dropped from
*Not in Lidarr*, since it is accounted for; the sync reports *Artists re-keyed*. Applying it is the
artist-merge path already described: albums, editions and wants re-pointed, follow settings merged.

**A re-key that renamed the artist too cannot be paired by name**, so the sync records the
finding on the artist instead (`CollectionArtist.ManagerMissingAt`, cleared by the next sync that
finds them, or by detaching them) and their page says so, with **Same artist as…**. The dialog asks
each Lidarr manager, on demand, for the artists it lists that the collection does not hold
(`GET /artists/:mbid/manager-candidates`); picking one writes the artist redirect with `Source =
manual` (taking over an open row if the sync had written one) and applies it at once
(`POST /artists/:mbid/same-as`) — a person chose the pair, so there is nothing left to review.

**It is always held for review**, whatever the policy says about artist merges. MusicBrainz merging
an artist is MusicBrainz stating a fact; this is a pairing read off a list by name, and two acts can
share one. Applied wrongly it would hand one artist's albums to another, so `heldForReview` holds
every artist redirect sourced from a manager, and the review says the two were paired by name.

### A row that settled itself still says what it became

A release-group deletion often closes *resolved elsewhere*: the manager re-keyed the album, the old
row went (a prune, or the sync), and there was nothing left to retire. The row used to close with
only *"a manager re-keyed it"*, because the one route from it to the new album — the old row's
artist — was deleted with the row. So the drain **remembers** whose album a release-group row is
about while the row still exists (`remember`, into `SubjectArtistMBID`, along with the title), and
when the row closes as moot, `rekeyTarget` looks under that artist for the one album of the same
title. Exactly one, and the row records it as its target and says so — the review then links it like
any re-key; none or two, and it closes as before, naming the artist. It only *describes* a row that is
closing anyway: nothing is moved on the strength of a title match.

### Clearing a blocker of files on disk

*"files on disk still resolve to this album"* named its remedy and offered none. A stuck release-group
deletion held by files now carries **Re-identify files** (`POST /migrations/:id/release-files`),
whose action depends on who decides those files' identity:

- **Autotaggerr.** Nobody else will, so the files are marked `unmatched` with the reason on them —
  what applying a release deletion does to its files — the artist's collection is re-derived, and
  the migration is applied, all before the response. Their tags are untouched; they wait under
  Items → Unmatched to be attached.
- **A manager.** Marking would last until the next run asked the manager and got the dead ID back,
  so the manager has to correct the album first. `Runner.RepairAlbumFiles` does both in one queued
  job — refresh and re-mirror the artist, force a re-correlate of that album's files, then drain the
  queue once more against the rebuilt collection (a run drains before it rebuilds, so the first
  drain still saw the album as owned). One job rather than two, because the queue would reorder
  them: a re-correlate writes files and jumps ahead. With artist refresh turned off on every
  manager the press is refused, saying so.

### When nobody can be asked

Every enabled Lidarr manager having artist refresh turned off used to leave *Ask the manager* on the
row, queueing a repair that skipped every manager and did nothing. `collection.RefreshTurnedOff`
names those managers, and the review turns the row into a stuck one whose blocker says so; the
artist page and the re-identify action refuse with the same sentence.

### Repairing from the artist page

The repair is offered where the albums are, too: the artist payload carries `repair` — the artist's
albums whose IDs resolve nowhere while a manager lists them, the managers holding it back if nobody
can be asked, and whether a refresh is already in flight — and the page shows a notice with **Ask
the manager** (`POST /artists/:mbid/repair-albums`, the same `RepairArtistAlbums` and the same marks
as the Migrations row).

## What the review queue shows

`migration.Review` decorates each row at read time, because the stored row cannot describe itself.
For a release-group deletion — the commonest row on a manager-fed install — every stored field reads
as a zero: no `new_mb_id` (a deletion has none), no `affected_files` (files are keyed by release,
never by release-group), usually no desires. Rendered literally that is *"Album · ID does not
resolve"* with nothing under it, which states the symptom and answers none of the three questions
the reader actually has.

| Field | Answers |
|-------|---------|
| `files_on_disk`, `editions` | **Do I have files for this?** Counted through the album's editions, since that is the only route from a release-group to a file. Deliberately *not* `affected_files`: a retirement rewrites no file, so "what this would touch" and "what you own" are different numbers and conflating them is what reported zero for an album sitting on disk |
| `owned`, `in_catalog` | the two authorities that can object to a retirement |
| `blocker` | why approving would not complete, read *before* the press instead of after the failure. Asked of `collection.ReleaseGroupRetirable`, not re-derived, so the queue and the apply path cannot disagree |
| `needs_manager_refresh` | the one blocker that is not final, and therefore the one where approve asks rather than applies |
| `artist_mbid`, `artist_name` | who a refresh would target — a bare album title does not tell the user where the fix lives |
| `artist_open` | how many rows one refresh of that artist would settle, so eight albums read as one decision rather than eight |
| `target_artist_mb_id`, `target_release_group_mb_id` | where the surviving entity of a merge or re-key sits in the collection, when it does — so *"re-keyed"* or *"merged into"* links to the album or artist it became rather than offering an ID to copy |
| `source_label` | which source reported it, written the way it spells itself |
| `problem`, `effect` | the same facts as sentences, safe to render verbatim |

Computed at read time rather than stored, because every one of these moves independently of the
migration: files arrive, a manager stops listing an album, a repair is attempted. A snapshot taken
at detection would be confidently wrong by the time anybody looked at it.

`artist_open` is the one exception to *per row*: it is one grouped query over the whole queue, run
after the page is decorated. The count is about rows this page may not contain — siblings on page two
are exactly the ones a per-page count would hide — and asking per row would be the same query fifty
times.

**Three states, three controls.** `blocker` and `needs_manager_refresh` between them say which of
three things a press means, and the row has to agree with itself about that:

| State | Primary control |
|-------|-----------------|
| nothing objects | **Apply** — approving applies it |
| `needs_manager_refresh` | **Ask the manager** — approving queues a refresh, and the row says how many siblings it settles |
| `blocker`, not refreshable | **Apply, disabled**, blocker in red beside it and as the title — a file, a want or an edition claims this album, or the only managers that could be asked have artist refresh turned off, and the press would fail with that same sentence |

A third-state row held by **files on disk** also carries *Re-identify files*
([above](#clearing-a-blocker-of-files-on-disk)) — the one blocker whose remedy this page can perform.

**Grouping by artist** is a view, not a filter: *Group by artist* orders the queue by the artist each
row concerns (`sort=artist`, an SQL expression mirroring the review's own derivation, so the order
holds across server pages) and puts a header over each artist's rows — the name, how many are
waiting across the whole queue, and one *Ask the manager* when any of them is waiting on a refresh.
Eight albums by one artist then read as the one decision they are.

The third had no control of its own: a blocked row offered a live primary button whose press returned
the refusal already printed beside it. The sibling count belongs to the second row of that table
alone — under an *Apply* it sat beside a sentence saying the album is about to be removed and a button
that refreshes nothing, which is two claims about one press with one of them false.

### The source is named, never assumed

Nothing about a merged identity is particular to MusicBrainz. The row carries a `source`
(`models.DataSourceType*`, empty meaning MusicBrainz for rows written before the column existed) and
every sentence takes the name from it, so the same queue reads correctly the day a second source
reports one. The page around them talks about *metadata* rather than about MusicBrainz for the same
reason the settings section does — see
[settings.md](settings.md#sections-are-named-for-what-they-govern-not-who-supplies-the-data).

The table is still `musicbrainz_migrations` and the event type is still `mb_migration`. Both are
stored strings, renaming them is a data migration, and neither is user-visible: the feed has called
that type *Identity changes* since it started emitting it.

## Where it runs

`process.Runner.applyMigrations` drains the queue at the end of a scan and of a drift sync — both fetch
releases, so both detect. It runs there rather than at the point of detection because applying one
rewrites MB IDs across several tables; doing that between files would interleave schema-wide
rewrites with tag writes. A run that detects nothing costs one indexed query over an empty set.

Counts ride the run's Activity event as `migrations`, alongside `releases_gone` and
`releases_relinked` — both of which are invisible in the file counts, since neither needs a single
file to change.

**Counts, and then which ones.** The stage also writes one entity `EventItem` per row it settled —
`migrated`, `resolved`, `dismissed` or `error` — because a count is enough to notice a run did
something and never enough to check it a day later, which is the whole reason a merge is worth
reporting: it rewrote what one of the user's albums *is*. A decision made by hand emits its own event
for the same reason; without one it was indistinguishable afterwards from the nightly pass having
made it.

Those rows name themselves through `events.ResolveRefs`, which falls back to the migration table
when the collection cannot answer. That fallback is the point: retiring an album deletes the only
row that knows its title, so the identifiers most in need of a name are exactly the ones the
collection lookups miss. `TagsWritten` stays zero even for a merge that re-pointed a dozen files —
on an entity row it renders as *"12 files re-tagged"*, and a migration writes no tags; it blanks the
processed marker so the *next* run does.

## API

| Endpoint | Purpose |
|----------|---------|
| `GET /migrations` | one page of migrations, each decorated with its [review context](#what-the-review-queue-shows). `?status=open` is the queue (pending + failed), `?status=closed` the history; `limit`/`offset`/`sort`/`dir`/`q` page, order and search it, and the response carries `total` |
| `GET /migrations/policy` | which categories are currently held for review |
| `POST /migrations/:id/approve` | apply one, then rebuild the collection — **202** instead when the album needs a manager refresh first ([above](#approving-a-blocked-album)) |
| `POST /migrations/:id/release-files` | clear a "files on disk" blocker: mark native files for re-identification and apply, or queue the manager refresh and re-read ([above](#clearing-a-blocker-of-files-on-disk)) |
| `POST /migrations/:id/dismiss` | record it as deliberately not applied |
| `POST /artists/:mbid/repair-albums` | ask the manager to re-read one artist whose albums hold dead IDs (202) |
| `GET /artists/:mbid/manager-candidates` | the artists a Lidarr manager lists that the collection does not hold, asked live |
| `POST /artists/:mbid/same-as` | join an artist the manager stopped listing to the one it lists now (`{"artist_mb_id"}`), applied at once |
| `POST /artists/:mbid/release-groups/:rgid/same-as` | record and apply that this album row is the same album as another of the artist's (`{"release_group_mb_id"}`) |
| `POST /migrations/verify` | sweep every stored MBID now (202; runs in the background) |

## Related

- [scanning.md](scanning.md) — the drift sync this rides on, and the skip-unchanged gate the
  blanked `processed_version` deliberately trips.
- [collection.md](collection.md) — the disk/catalog split that makes derived state safe to drop and
  authored state dangerous to lose.
- [attach.md](attach.md) — where an un-identified file goes to be re-attached.
