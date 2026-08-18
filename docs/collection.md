# Feature: the collection (present vs wanted)

Two different questions, deliberately answered by two different authorities:

- **Present** — what you own, organised artist → release-group → release. Universal, computed from
  the library for every manager.
- **Wanted** — the gaps. Manager-owned: the native manager computes them from MusicBrainz, while
  Lidarr is the source of truth for its own artists and is *mirrored*, never replicated.

One UI mental model — an artist-completeness **Collection** page — with the manager difference
reduced to a provenance badge and which actions are offered.

## The disk view is derived, so every writer re-derives

`Owned` / `OwnedTracks` / `TotalTracks` are computed from `library_items` by
`collection.Rebuild`. Nothing writes them directly, which means any code path that changes the
file index has to re-derive or the collection reports something the index no longer says.

Computed from the index does not mean trusting it outright. Before a rebuild aggregates anything,
`pruneGoneFiles` stats every row in its scope and deletes the ones no longer on disk — a stat per
row, not a walk, which is what lets Scan do it inline. See
[scanning.md](scanning.md#a-scan-proves-its-own-rows) for the guard that keeps an unmounted
library from reading as "every file gone".

Three paths do:

| Path | How |
| --- | --- |
| a processing run | `collection.Rebuild` at the end of the run |
| applying a migration | `collection.Rebuild` after the remap |
| manual attach | `collection.Rebuilder.Request()`, from `saveCorrelation` |

Attach was missing for a long time, so attaching files by hand left the collection stale until
the next processing run — which is the only reason the *Scan* button (once called *Rebuild from
library*) had to be something a user was expected to understand. It is now a repair affordance,
not a step in a workflow.

The trigger sits in `saveCorrelation` rather than in the handlers because that is the single
place both the single and bulk attach paths write a correlation. A writer that has to remember to
re-derive is a writer that will eventually forget.

`Rebuilder` is asynchronous and **coalescing**. Attaching a twelve-track folder calls the attach
path twelve times, and twelve full re-derivations for one logical action is absurd; a pass
already in flight covers work that arrived before it started, so a burst collapses to at most two
passes. It never blocks the caller — an attach is interactive, and making someone wait on a
re-derivation to see their file marked matched is the wrong trade — and its failures are logged
rather than surfaced, because the correlation is already committed and a stale derived view is a
display problem.

`Rebuild` itself runs in a transaction. Its first act is to clear the disk view before
re-establishing it, so without one a failure partway — or two overlapping passes — would leave
the collection claiming to own less than it does. Silent and wrong is the worst combination, so
the write helpers propagate their errors here and the whole pass rolls back. The Lidarr and
discography syncs keep the older log-and-continue behaviour: one unwritable album must not
abandon a whole sync.

### A rebuild that loses a write race takes the lock first, and is retried second

That transaction **reads before it writes**. Left to itself under SQLite's WAL mode that means it
opens as a read snapshot and upgrades when it clears the disk view — and if any other writer commits
in between, the upgrade cannot be granted against a snapshot that is now stale. SQLite answers
`SQLITE_BUSY_SNAPSHOT` (517) *immediately*; `busy_timeout` does not apply, because a stale snapshot
is not something waiting will fix. The transaction can never succeed and has to be run from the top.

**The fix is to never take that snapshot.** Connections open transactions with `BEGIN IMMEDIATE`
(`database.sqliteTxLock`, `_txlock=immediate` on the DSN), which takes the write lock up front, so
there is no upgrade left to refuse. A competing writer now makes this one *wait* on `busy_timeout`,
which is what that timeout was always for.

It is worth recording why retrying was not enough on its own, because it looks like it should be.
Each retry takes a *fresh* snapshot, which the competing writer can invalidate again — so against a
steady stream of writes the rebuild loses indefinitely. That is a livelock, not bad luck, and it
showed as `TestRebuildSurvivesAConcurrentWriter` failing on essentially every `-race` run while
passing without it, which reads as flakiness and is not.

`collection.retryBusy` remains, as the second line of defence, up to four times with a
doubling-plus-jitter backoff. What is left for it is the ordinary `SQLITE_BUSY` (5) — another
*process* holding the write lock past the timeout, which no locking mode prevents. The rebuild is
safe to re-run by construction: it derives the whole disk view from `library_items` and the release
cache, so the second attempt reads the newer state and produces the answer the first was trying to.
Only lock errors are retried — re-running a genuinely broken pass would turn one error into four and
a longer wait for the same message.

The symptom was the Rebuilder defeating itself: attach requests a rebuild from `saveCorrelation`
and then keeps writing — the tags, the Activity event — so the pass was racing the very request
that asked for it. It failed, logged a warning nobody reads, and the collection stayed stale until
the next scan, which is precisely the gap the Rebuilder exists to close. It is the *losing* writer
that must retry, and every caller here is either a background pass or an interactive action whose
real decision is already committed.

### Scoping a rebuild

`Rebuild` is the **Scan** verb (see [scanning.md](scanning.md#the-four-verbs-and-why-none-of-them-cascades)),
and `RebuildScoped` narrows it. The zero scope is the whole collection — what a processing run and
the collection-wide button ask for; `RebuildArtist` is the artist page's button.

Narrowing is about *writes*, not reads. A scoped pass reads the whole index exactly as a full one
does, because that is the only way to answer the question the button is actually pressed for: an
album whose files were processed since the last rebuild has no collection row yet, so a scope
resolved from existing rows alone could never find it. `bounds.inScope` therefore admits a file
two ways — an edition already filed under the artist, **or** a cached release that credits them.

What the bounds confine is every write that would otherwise be collection-wide:

| Step | Unbounded | Bounded to the scope |
| --- | --- | --- |
| clear the disk view | every `owned` release-group | only the scope's release-groups |
| prune owned editions | `mb_id NOT IN (keep)` | that, `AND mb_id IN (scope)` |
| reconcile auto wants | every desire row | only desires for the scope's release-groups |

Both prunes are the reason the scope exists at all: to a pass that only read one artist's files,
every other album in the collection looks like it owns nothing, and an unbounded clear or
`NOT IN` would take the lot.

Anything the pass *discovers* is admitted late (`bounds.extend`), after the clear and before the
prune — so it can only ever widen what gets written, never what got wiped. `pruneOrphanArtists`
stays unscoped deliberately: it asks whether anything anywhere still credits an artist, which is a
global fact and true regardless of which pass noticed.

**There is no library scope**, and the schema is why: `owned` is a flag on the release-group, not
a fact per library. An album with files in two libraries is one row, so a per-library pass would
have to read the other libraries' files to avoid clearing a shared album — at which point it is
the collection-wide pass with extra steps.


## Entities

| Model | Written by | Holds |
|-------|-----------|-------|
| `CollectionArtist` | `Rebuild` (library) / `AddArtist` (manual) | name, `ManagedBy`, `Origin`, follow settings |
| `CollectionReleaseGroup` | `Rebuild` (disk block) + `SyncLidarr`/`SyncArtist` (catalog block) | album-level ownership and catalog state |
| `CollectionReleaseGroupArtist` | every writer, additively | which artists a release-group is credited to |
| `CollectionRelease` | `Rebuild` only | one row per **owned edition**: per-edition track counts |
| `CollectionDesire` | the user via the API (`manual`), or a reconciliation pass (`auto`, `manager`) | what is wanted, and by whose authority — a `manual` row is never recomputed |

Named `Collection*` to avoid clashing with the MusicBrainz response types.

`Origin` (`library` \| `manual`) records how an artist *entered* the collection. `Rebuild` stamps
`library` on create and never overwrites `manual`, so an artist you added by hand keeps that
provenance once files for them appear — and a file-less artist is not treated as an anomaly.

## Whose album it is: the release-group's credit, not the release's

MusicBrainz credits releases and release-groups **independently**, and the two routinely disagree. An
artist migration is applied to the release-group and to whichever pressings an editor gets round to,
so older editions keep the previous credit indefinitely — a soundtrack moved from *Various Artists*
to its composers still has Various Artists on its original CD.

`Rebuild` is the sole writer of collection artists and of a release-group's primary credit, and it
used to take both from `release.ArtistCredit`. That filed the album under an artist the group itself
no longer named, and no amount of re-scanning fixed it, because the release was being read
*correctly*. Under Lidarr it compounded: the artist the manager knows never enters the collection at
all (`SyncLidarr` mirrors artists that are already `CollectionArtist` rows — it cannot introduce
one), so the album showed under the stale artist, flagged `unmapped`, while the manager had it under
the new one.

`albumArtistCredit` answers it instead: the release-group's own credit, **falling back** to the
release's when the group carries none. The fallback is what makes this safe rather than merely
different — an older cache entry or a payload without the field behaves exactly as before instead of
dropping the album out of the collection. It costs no fetches: the credit arrives inside every
release payload under the `inc=` string `QueryMusicBrainzReleaseData` already sends, and is already
in the persisted cache, so existing installs get the corrected attribution on their next rebuild.

The release's own credit is still the right answer for a *pressing*. It is simply not the answer to
"whose album is this", which is the only question `Rebuild` asks — including of `CollectionRelease`,
whose `ArtistMBID` follows the same primary credit. An owned edition claiming a different artist from
its release-group is what let `ArtistReleaseMBIDs` reach one artist's files from another artist's
page, and a re-tag or re-correlate scoped to the stale artist would have walked them.

One thing this deliberately does **not** do: special-case Various Artists. The placeholder is special
only in that upstream migrates groups off it often — the rule is about credits, not about that MBID.

## A release-group can have more than one artist

`CollectionReleaseGroup.ArtistMBID` holds a single artist, so a collaboration could only belong to
one. Worse, every writer overwrote that column with whatever artist it happened to be about, and the
writers disagree: `Rebuild` names the *first* credited artist, `SyncArtist` names the artist whose
discography it is syncing, `SyncLidarr` names Lidarr's artist. A collaboration therefore belonged to
whichever ran last — it appeared on one artist's page and vanished from the other's, flipping
between them as syncs ran, while the release page (keyed by release MBID, no artist involved) kept
showing it as owned the whole time.

`CollectionReleaseGroupArtist` is the fix: one row per (release-group, artist) with the credit
`Position`. `ArtistMBID` survives as the **primary** credit, for display and sorting.

Two rules make it work:

- **Links are additive for every writer but one.** Writers know different amounts — only `Rebuild`
  reads the release-group's real artist credit; everyone else knows just that *their* artist is
  credited somehow. If a partial writer could remove links, syncing the second artist would delete
  the first artist's claim, which is the same bug from the other end. See
  [removing a credit](#removing-a-credit) for the one writer that may subtract.
- **Only a caller that knows the credit order may write the primary credit.** `rgWrite.credits`
  carries the full ordered credit and is set by `Rebuild` alone; an empty `credits` means "this
  artist is credited", never "this is the only artist".

Reads go through `collection.ReleaseGroupsForArtist`, which is the **union** of the link table and
the primary-credit column. The column is a claim in its own right, so a row with one but no link
still shows — which makes `BackfillReleaseGroupArtists` (run at startup, idempotent) an optimisation
rather than something page correctness depends on. The backfill cannot recover the *second* artist
of a pre-existing collaboration, since nothing stored it; the next `Rebuild` does, from the cached
release.

Two related per-artist reads were keyed the same way and moved to release-group keys:
`OwnedReleaseCounts` (an edition is stored under its primary credit, so counting per artist reported
zero owned editions on the other artist's page) and `DesiresForArtist` (a desire records the page it
was created from, so wanting a collaboration from one artist left the other offering to want it
again).

### Removing a credit

Reading the release-group's credit puts a migrated album on the right artist's page. It does not take
it off the wrong one: reads **union** the link table, so a stale row is indistinguishable from a
collaborator, and a purely additive table meant an upstream migration was permanent in the other
direction.

`pruneReleaseGroupArtists` is the one subtractive path, and `Rebuild` is its only caller — for a
release-group it just re-derived from a cached release, with that group's full credit in hand. It is
the only caller that can tell an artist who is *no longer* credited from an artist it merely does not
know about, which is exactly the distinction the additive rule exists to protect.

Whose claim a link represents is therefore stored on it. `CollectionReleaseGroupArtist` carries
**`FromDisk`** (set by `Rebuild`, which read the real credit) and **`FromCatalog`** (set by
`SyncLidarr` and `SyncArtist`, which know only that their own artist is credited). Two flags rather
than one source column because both can be true — the row is uniquely indexed on
`(release_group, artist)`, so a single value would have to pick a winner. A link the manager also
wrote keeps the row and gives up only its disk half: Lidarr saying an album is this artist's is a
separate authority's answer, and MusicBrainz re-crediting the group does not overrule it.

A row with **neither** flag predates the columns and is read as a disk claim. That reading is what
makes a stale credit from before this existed removable at all — which is the entire point, since the
albums that need cleaning are the ones migrated before the flags were added. The cost is bounded and
self-repairing: a legacy *catalog* link caught by that reading is deleted and re-created by the next
sync.

Unlinking can leave an artist holding nothing, and an artist row with an empty page is not neutral —
it sits in the collection list claiming to be part of the library. `pruneOrphanArtists` runs at the
end of `Rebuild`, after the links have been re-derived, with the same subtraction discipline as
`PruneOrphanReleaseGroups`: only `origin = library`, not monitored, no desires, and no release-group
or owned edition claiming them by link or by primary credit. Anything else means there is still a
page worth opening.

### Saying that it happened

A merge leaves a `musicbrainz_migrations` row; a deletion leaves one too. A **re-credit leaves
nothing** — the release keeps its ID, the release-group keeps its ID, so nothing fails, nothing is
queued for review, and the album is simply under a different artist next time you look. It is the one
identity change with no trace anywhere, which is exactly why it took a production report to find.

`RebuildStats.CreditChanges` is that trace: release-groups whose primary credit moved between two
*named* artists, plus links dropped because MusicBrainz no longer names the artist. It rides the processing run's
event as `credit_changes` with a `· N credit change(s)` clause on the summary line (see
[scanning.md](scanning.md#the-collection-stage)), and it is reported **per run rather than stored** —
the state it describes is already correct by the time anyone reads it; what was missing is that it
changed.

Three things it deliberately does not count, because a number that fires on ordinary activity is one
nobody reads:

- **First sight of an album.** Filling in a blank credit is this writer learning something, not
  MusicBrainz changing its mind.
- **A manager write.** `creditChanges` is an accumulator passed *into* the release-group upsert, and
  only `Rebuild` sets it. A mirror rewriting its own catalog is not an upstream re-credit.
- **A settled move.** The run that makes the change reports it; the next rebuild over the same data
  reports nothing.

### What a pass moved, not just where it ended up

`RebuildStats.Artists` and `.Owned` describe the collection; four more counters describe the *pass*
— `ArtistsAdded` / `ArtistsRemoved` / `AlbumsAdded` / `AlbumsRemoved`. A total is a state, and a
nightly feed of identical states is unreadable: *42 artists · 39 albums on disk* three nights running
says nothing about the night one album left and another arrived. Credit changes were the only delta
the Scan reported, which meant the one identity change with no migration row was better instrumented
than the ordinary ones.

They come from a set comparison, not from arithmetic on the totals: `snapshotCollection` reads the
in-scope artist and owned release-group MBIDs inside the transaction and **before the clear**, which
is the only moment the previous answer still exists. A pass that drops one album and finds another
reports the same total either way. A scoped pass compares against its own scope, or an artist-scoped
Scan would report the rest of the collection as removed.

Nothing is appended to the summary when nothing moved (`changeClause`), and the detail view drops
zero-valued counters, so a quiet night still reads as the two totals. *Albums gone* is the one
coloured `bad`: an album leaving the disk view means files moved or a correlation broke, and neither
is something a Scan can fix on its own.

## The disk/catalog split

`CollectionReleaseGroup` carries two independently written blocks:

- **disk** — `owned`, `owned_tracks`, `total_tracks`. Written *only* by `collection.Rebuild`, which
  aggregates `library_items` against *cached* releases and never fetches.
- **catalog** — `in_catalog`, `catalog_owned_tracks`, `catalog_total_tracks`, `catalog_monitored`.
  Written *only* by `SyncLidarr` and native discography discovery.

This split exists because both authorities originally wrote the *same* columns, so whichever ran
last decided what the UI showed — and since `Rebuild` runs automatically after every processing run, a run
silently wiped the Lidarr mirror. Now neither can clobber the other and the order they run in does
not matter.

**Where the two disagree the row reports a `Discrepancy`** rather than letting one side silently
win:

- `stale_catalog` — more files on disk than the manager thinks; Lidarr needs a rescan.
- `not_indexed` — the manager has files Autotaggerr never indexed (outside a configured library, or
  not scanned yet).
- `unmapped` — files on disk with no manager album at all.
- `no_edition` — the counts disagree **and** the manager named no edition, which is the reason they
  disagree. See below.

### Mirroring Lidarr at two scopes

`SyncLidarrWith` takes a `SyncOptions`; `SyncLidarr` is the unscoped form the nightly run uses. An
artist scope exists because that is the granularity a repair needs — one album's catalog counts going
stale used to cost a mirror pass over every Lidarr artist in the collection. Both scopes emit the
same `lidarr_sync` event with the same counters, because they are the same work: a scoped pass that
reported differently would read as a different verb in the feed.

The per-artist endpoint refuses an artist the mirror does not govern (`managed_by` neither `lidarr`
nor `mixed`) rather than syncing nothing. A pass that reported *0 artists synced* would read as
Lidarr having failed, when the truth is that this artist is not Lidarr's to answer for.

Three shared emitters keep the three call sites — the collection verb, the artist verb and the run's
own stage — reporting identically: `SyncEventStats`, `SyncEventItems`, `SyncEventDetails` and
`SyncSummaryLine`. They existed as three copies of the same four lines, which is how one verb ends
up with three vocabularies.

#### A pass reports what it could not account for

`SyncStats` carries two findings beyond the counts, and both were previously log lines and nothing
else — so a Lidarr that was half down produced an Activity row identical to a healthy one with
smaller numbers in it.

- **`Unknown`** is the artists the collection files under Lidarr that no Lidarr listed. It is the
  pass's one real finding and the one its counters structurally could not carry, since the artists
  it means are precisely the ones missing from *artists synced*. Their wanted view has nothing
  behind it until they are matched in Lidarr or detached from the manager. Stored as MBIDs, so the
  rows resolve to a name and a link like every other entity row (see
  [scanning.md](scanning.md#an-identifier-is-not-a-subject)), under
  `EventItemStatusUnknown` — a complete answer that happens to be "no", distinct from *gone* (the
  source used to have it) and *error* (we could not ask).
- **`Failures`** is the lookups that errored: a manager listing that failed, one artist's albums
  that could not be read, a want reconciliation that did not commit. They have no detail rows
  because they are not *about* an entity, so they ride `details.failures` and render as their own
  list.

**An unknown artist's catalog view is retired with them.** The reset that clears `in_catalog` and
the catalog counts runs per artist the manager *listed*, immediately before re-mirroring their
albums — so an artist deleted from Lidarr outright never reached it, and kept the counts, the
monitored edition and `in_catalog` from the last pass that did find them. The artist page then
reported files the manager holds for an album the manager has never heard of, and no amount of
re-syncing could clear it, because clearing only happened where there was something to replace it
with. `clearCatalogView` therefore also runs for every artist that lands in `Unknown` — under the
same guard as the reporting, below.

**A failed listing suppresses the unknowns entirely.** An artist missing from a list that was never
fetched is not missing, it is unlooked-at — the same distinction the MusicBrainz path draws between
a 404 and a timeout. Reporting a whole collection as unknown because Lidarr was restarting would be
the most alarming possible way to say "try again".

**`IgnoreCache` is opt-in, and it does not change what the pass fetches.** `GetArtists` and
`GetArtistAlbums` — the only two calls a mirror makes — are the only *uncached* Lidarr calls there
are, so a mirror pass is always fresh. What goes stale is what the **pipeline** reads: the artist's
track file list, cached an hour and keyed by artist, which is what file paths are matched against. A
file imported into Lidarr after that list was cached cannot be matched until it expires, and the
album loses a track from its disk view in the meantime. So the option drops the artist's cached
Lidarr responses for the benefit of the *next scan*, not this pass — repair, not mirroring, which is
why it is a checkbox rather than the default.

`modules.LidarrInvalidateArtistCaches` is the scoped drop it uses. The whole-cache flush
(`LidarrInvalidateCaches`, what force re-correlate calls) exists because the album and track caches
are keyed by Lidarr's own album IDs, which cannot be derived from an artist — but a mirror pass has
*just listed the artist's albums*, so it holds exactly the mapping that is otherwise missing and
passes the IDs in.

### The disk view counts files, not successes

`ownedItemRows` selects every **correlated** file — `mb_release_id <> ''` — and excludes exactly one
status: `unmatched`. Not "every file that processed cleanly", which is what it used to be, and the
difference is the whole point of the block being called *disk*. A file is on disk whether or not the
last attempt to tag it worked; whether MusicBrainz answered, whether `metaflac` could write, whether
the volume was read-only. None of those are facts about the disk.

Requiring `status = ok` made every one of them empty the album instead. A scan interrupted by a
MusicBrainz outage failed each file it had already correlated, the index dropped their MB IDs, the
next rebuild found nothing on disk for that release-group, and the row reported `not_indexed` —
*"the manager has files Autotaggerr never indexed."* The files were indexed. The index had been told
to forget them, and then the discrepancy blamed the gap on never having scanned them.

So `library_items` now keeps three separate facts apart, and only the first feeds this view:

| Fact | Columns | Written when |
|---|---|---|
| **identity** — what the file is | `mb_release_id`, `mb_recording_id`, `correlation_source` | whenever a correlation resolves, whatever happens next |
| **owned** — is it on disk | derived here, from identity | follows identity, never the outcome of an attempt |
| **the last attempt** | `status`, `error`, `last_error_at`, `last_error_transient` | every run; this is the admin's surface, not this view's input |

`unmatched` is excluded because it is the one state that is *not* a failed attempt: the manager is
saying it does not know this file. Any MB ID still on the row is a leftover from before, not
identity, and counting it would restore the album on the strength of an answer that has been
withdrawn.

Nothing schedules a retry for a file that failed, and nothing needs to: `processed_version` is
stamped only by a success, and `shouldSkip` refuses to skip a file whose version does not match, so
the next run re-attempts it. The **absence** of that column is the retry mechanism — a test pins it,
because tidying the write onto the shared path would silently turn a transient outage into a
permanent skip.

### The same rule, applied to membership

The three-facts split above governs every query that asks *which files belong to this thing*, not
just the disk view. `models.TaggableItems` is where that lives — the scope every path that **writes**
selects with:

```
mb_release_id <> '' AND status <> 'unmatched'
```

It replaced `status = ok` at seven call sites (`collection.ArtistItems`/`ReleaseGroupItems`, the
three re-tag queries in `process.Runner`, and the two guards in `routers` that refuse a re-tag with
nothing to do). That predicate got both directions wrong:

- **An error is not a disqualification.** A file that failed to tag was dropped from its own artist,
  so the re-tag that would have fixed it could not see it. The failure kept the file out of the only
  verb able to clear it — self-perpetuating, and the exact inverse of what a repair is for.
- **`unmatched` is a disqualification, and a different one.** Not a failed attempt: the manager
  saying it does not know this file. The release ID still on the row is an answer that has been
  withdrawn, and writing tags from it would stamp the file with an identity its authority disclaimed.

The scope is shared rather than spelled out per site because the guard and the work have to agree —
the API refuses a re-tag that would touch no files by *counting* them, and a count that drifted from
the runner's query would either refuse work there was or queue a run that tagged nothing.

**Folder resolution deliberately reaches wider.** `ArtistTargets` and `ReleaseGroupTargets` also
admit *disowned* files (`unmatched`, with the release ID they last held) when deciding which
directories to walk, matching them to the artist or release-group through the release cache. Without
that the repair verbs were unavailable exactly when they were needed: one stale Lidarr trackfile
cache turns every file of an album `unmatched`, the next rebuild prunes the owned-edition rows those
files were reachable through, and the artist scope returned `ErrNothingToProcess` — so force
re-correlate, whose whole job is repairing an artist whose files diverged from what the manager says,
refused to start, leaving only the library-wide form that discards every manager-governed pin in the
library. Deciding a folder is worth walking is a far weaker claim than owning a release, and a
re-correlate re-resolves identity from scratch rather than writing the stale one.

### An album with no edition selected

Lidarr picks one release per album — the `monitored` flag on `album.releases[]` — and its
`statistics` describe *that* release. An album where none is monitored still reports statistics,
computed against an edition nobody chose, and they can disagree wildly with the files: the production
case was Lidarr reporting **7 of 7** for an album with **44** files on disk. Monitoring the right
release in Lidarr fixed it.

Two things follow from "no edition selected", and both are now said out loud:

- **Nothing can be tagged.** `GetMonitoredAlbumMBID` returns no release (it will never fall back to
  an unmonitored one — that would tag a whole album against an edition the user did not choose), so
  every file of the album resolves to `ErrUnmatched` under a Lidarr manager. The log line names the
  album and says to pick an edition in Lidarr, because this is the one Lidarr state that reads as a
  bug in Autotaggerr.
- **The counts cannot be trusted.** `Discrepancy` reports `no_edition` instead of `stale_catalog`,
  since "your manager needs a rescan" is advice that cannot fix it — a rescan reports the same
  numbers again.

`no_edition` *explains* a disagreement rather than raising one by itself: `CatalogReleaseMBID` is
also empty on rows written before that column existed, so a row whose counts agree with the disk
stays silent until its next sync fills the column in.

Suppressed when there is no manager answer to compare against — `collection.CatalogChecked`, the
single definition of that, which reads the artist's `LastSyncedAt`. Otherwise every album of an
unfollowed native artist would flag.

That predicate used to be "does any of this artist's release-groups carry catalog state", and the
difference matters: it let an album be reported absent from a catalogue *nothing had ever put it to*,
on the strength of the artist's **other** albums having been mirrored. The production case was an
album filed under the wrong artist (see [whose album it is](#whose-album-it-is-the-release-groups-credit-not-the-releases))
warning "not in Lidarr" about an album Lidarr had all along under a different artist — one nothing
had asked Lidarr about, because `SyncLidarr` only mirrors artists that are already collection rows
and so can never introduce one. **Absence of an answer is not a negative answer.** Both `SyncArtist`
and `SyncLidarr` stamp `LastSyncedAt`, so the rule is manager-agnostic; an artist no manager has
synced simply reports no discrepancies at all, which is also the right answer for a native artist
nobody follows.

### A video track is not a track you are missing

`releaseTrackTotal` decides how many tracks an edition has, and it is not
`len(medium.Tracks)`. Frank Ocean's *Endless* is why: the 2018 CD+DVD edition
(`c14006ec-8b09-4fcd-addd-e5a2960013d0`) is 19 audio tracks and 22 videos, so a library holding the
complete album read **19/41** and could never close the gap. Lidarr, which ignores video media,
said 19/19 and was right.

The rule is **per track, not per medium** — `models.Track.IsVideo`, reading `recording.video`. The
medium's format cannot answer it: an "enhanced CD" carrying one music video as its last track is the
same bug at a smaller scale, and there the format says plain `CD`. The flag is on every cached
release already, since the release fetch has always used `inc=recordings`, so nothing had to be
re-fetched for this to start working.

**An owned video track still counts.** The total is *audio tracks, plus any video track a file
actually resolves to* — not simply the audio ones. Someone who ripped the bonus DVD's audio has
files that legitimately point at those tracks, and excluding them regardless would report 41/19:
owning more of an album than it contains, with `Complete()` true for a reason nobody could read.
Counting them once owned makes the total describe what this library can hold, and makes it
impossible for the owned count to exceed it.

Two other places apply the same rule, and one deliberately does not:

- **Bulk attach never proposes one** (`MapFilesToTracks`). Beyond the obvious wrongness of pairing a
  video with an audio file, a DVD used to wreck both strategies: it made the release look
  multi-medium, so a bare `05` filename was ambiguous between the discs and the number strategy
  bailed, and the sort-order fallback then zipped 19 files against a 41-entry list.
- **The track list itself stays faithful** (`ReleaseTracks`). Video tracks are carried with a
  `Video` flag and labelled `(video)` in the attach pickers rather than filtered out, because
  filtering would also narrow `FindReleaseTrack`, which validates a hand-picked track before it is
  attached — turning a deliberate choice into "no such track" is the wrong way to say "we would not
  have suggested that".
- **Search results are left alone** (`SearchResultFromRelease`). Its contract is to project a full
  release onto the same shape the MusicBrainz *search* API returns, and search hits carry
  `track-count` per medium with no track list to filter by. Correcting the one source we could would
  make two rows in the same list count differently, which is worse than a faithful 41 sitting beside
  a format string that already says `CD + DVD`. The collection's own editions
  (`routers/collection.go`, projecting `CollectionRelease.TotalTracks`) do show the corrected
  number, because that value is ours.

`TRACKTOTAL` is not affected: it is written per medium (`modules/files.go`), so a CD file gets the
CD's own count, which is the physically correct answer even when a later medium is video.

## Per-edition ownership

`Rebuild` used to collapse each release-group to its single best-owned edition and discard the rest.
`CollectionRelease` keeps them all: owning 5 tracks of the 1977 original and 7 of the 2017 remaster
is **two partial editions**, not one album that is 12/17 complete.

The release-group **keeps** its best-edition summary — that is the useful headline ("how close am I
to having this album") — and the per-edition rows are the detail behind it, which is why
`Complete()` and `Discrepancy()` did not have to change.

`syncOwnedReleases` upserts what is owned and **prunes what is not**. Pruning matters more than
upserting here: re-attaching a file from the original to the remaster leaves the original owning
nothing, and owning *nothing at all* still has to clear the table — a `NOT IN ()` with no values
deletes nothing, so the empty case is handled explicitly.

Desires reference releases by MBID, never by a `CollectionRelease` row, so rebuilding the disk view
can never disturb authored intent.

## The desire model

Desire is **authored** user intent: sparse, typed by a human, and it must never be recomputed.
Ownership is **derived**, re-read from disk on every processing run. They live in separate tables so the
class of bug the disk/catalog split fixed is structurally impossible here — with the user's own
intent as the thing that would otherwise be at risk.

Five cases, all one shape:

| # | "I want…" | `release_mbid` | `recordings` |
|---|-----------|----------------|--------------|
| 1 | this album, any release (default) | empty | empty |
| 2 | these songs, any release | empty | {a, b} |
| 3 | this specific release | X | empty |
| 4 | these songs from this release | X | {a, b} |
| 5 | these songs from release X *and* those from Y, same group | two rows: X, Y | {a,b} / {c,d} |

Empty `release_mbid` means "any release will do"; an empty recording set means "the whole thing".
Case 5 falls out as multiple rows under one release-group, with `(release_group, release)` unique so
overlapping track sets merge rather than duplicating.

**Songs are identified by *recording* MBID, not track MBID.** A MusicBrainz *track* is a recording's
placement on one specific release, so track IDs are release-scoped and cannot express case 2, where
no release has been chosen yet. A *recording* is the audio, stable across every release it appears
on. `LibraryItem` already stores both IDs separately, so this cost modelling care, not new fetches.

**Satisfaction differs by intent, deliberately.** An *album* desire is satisfied when at least one
acceptable release is complete; a *song* desire is satisfied when those recordings are owned, from
any release. Do not "fix" one to match the other — the asymmetry follows from what was asked for.

Within one release-group a desire is *either* any-release *or* a set of specific releases; setting
one clears the other (`SetDesire` enforces it in both directions). Holding both at once is a
contradiction the schema would otherwise store happily.

**Every desire carries its provenance.** `CollectionDesire.Source` is `manual`, `auto` or `manager`,
and it is what makes "never recomputed" a guarantee about *authored* intent rather than about every
row. A derived row has to be re-pointed as the thing it derives from moves; the only way to keep both
properties is to know which kind a row is. Each reconciliation pass may delete or re-point **only its
own** rows, `SetDesire` stamps `manual` (so a hand re-assert takes a row back), and an unlabelled row
reads as `manual` — the reading that cannot lose intent. `BackfillDesireSources` labels rows written
before the column existed, from the legacy `auto` boolean AutoMigrate leaves behind.

**Auto-narrowing (native only).** An `any`-release want self-narrows to the edition you actually own:
when a file lands for an "any" desire, `reconcileAutoDesires` (last step of `rebuildTx`) replaces the
`any` row with one `auto` want per owned edition. Rebuild may re-point an `auto` want when the files
are replaced, but never touches a hand-pinned one. Own two editions ⇒ one `auto` want each. Edge case
(v1): deleting *every* file of a promoted album prunes its `auto` wants and drops it from wanted —
re-add to restore. Lidarr artists are skipped; their edition arrives the other way, below.

### Manager-derived wants

Lidarr's *album* want has always reached the collection through `catalog_monitored`, but its
*edition* want stopped there: `SyncLidarr` fetched each album's `releases[]` and kept only the
counts, so an album green in Lidarr on a specific release showed **no edition wanted** here — the
authority had decided and nothing said so. Two pieces close it:

- `CollectionReleaseGroup.CatalogReleaseMBID` — the manager's selected edition, written beside
  `catalog_monitored` and cleared with the rest of the catalog block. Storing it (rather than only
  reading it through at tagging time, as `GetMonitoredAlbumMBID` does) is also what makes "Lidarr
  monitors X, your files are on Y" comparable at all; that divergence is what force re-correlate
  exists to fix, and it was previously invisible until a file failed to tag.
- `reconcileManagerDesires` — run at the end of `SyncLidarr`, the manager-side sibling of
  `reconcileAutoDesires`. One `manager` row per monitored album that names a monitored release,
  re-pointed when Lidarr's selection moves, pruned when the album stops being monitored or leaves
  the catalog. Monitoring an album with *no* monitored release stays an album-level want: inventing
  an edition would be claiming a decision Lidarr has not taken.

Three rules keep it clear of authored intent: it writes only for artists a manager owns (a mirrored
want must never appear on a native artist's page), it touches only rows whose source is `manager`,
and it **skips any release-group that already carries a hand-authored want** — an explicit pick
outranks anything derived, and a group holding both an "any" want and a specific one is the
contradiction `SetDesire` exists to prevent. Ownership of the album is unaffected either way: what
is wanted is intent, what is owned is derived from disk, and keeping them apart is what lets the
page report that the two disagree.

Recording the mirror as rows rather than reading it through the catalog columns is deliberate: a
want that exists only as a mirrored column disappears with the mirror. As a row it is Autotaggerr's
own record of what Lidarr decided, which is what makes [detaching](#detaching-a-manager) a change of
authority instead of a loss of data.

## Detaching a manager

`collection.DetachArtist` takes authority over one artist back from its library's manager. It is
only possible because the manager's selections are already Autotaggerr's own rows, so it re-labels
what is stored rather than re-deriving anything. Three things change, and deliberately nothing else:

- **`ManagedBy` is held at native.** That is the whole mechanism: `SyncLidarr` and
  `reconcileManagerDesires` both select on `managed_by IN (lidarr, mixed)`, so a detached artist
  stops being asked about and stops having its rows maintained.
- **The manager's wants become `manual`.** They *must* — those passes prune rows they own, and a
  `manager` row on an artist no manager governs is exactly what the next `reconcileManagerDesires`
  deletes as an orphan. Re-labelling is what keeps the decisions.
- **Following is switched off.** The non-obvious one. Following is *stored* under a manager but does
  not govern (see `FollowGoverns`), so a Lidarr artist can be carrying a stale `Monitored` flag set
  before it was ever managed. Detaching makes following govern again, so leaving the flag alone
  would turn a detach into "and also auto-want the entire back catalogue" — from a control the page
  does not show. Following is offered right there afterwards.

It deliberately does **not** invent follow settings from what the manager wanted. Lidarr monitors
per album, not by rule, so there is no rule to recover; any follow-types guess would be fabricating
intent that was never expressed.

**`ManagerDetached` is stored, not derived.** `managed_by` is re-derived from the library's manager
by `rebuildTx` on every run, so a detach that only wrote `managed_by` would appear to work and then
silently undo itself. `upsertArtist` honours the flag, and `TestRebuildDoesNotRevertADetach` holds
that line.

`ReattachArtist` (`DELETE /artists/:mbid/detach`) clears the override and re-derives provenance
immediately via `deriveArtistManager`, rather than leaving the page wrong until the next run. It is
**not a perfect inverse**, and that is the safe direction: wants that detach made manual stay manual.
Handing them back would give rows the user may since have edited to a pass that can re-point or
prune them, and `reconcileManagerDesires` already treats a hand-authored want as a veto — so the
manager simply leaves those albums alone.

### Deleting a manager detaches its artists

`deleteManager` calls `DetachManagerArtists` first. Once the manager row is gone, nothing can
reconcile its mirrored wants: `SyncLidarr` returns early when no enabled Lidarr manager is
configured, and that early return is *correct* — a reconcile with zero managers cannot tell "Lidarr
unmonitored this album" from "Lidarr is gone", and would delete the decisions rather than keep them.
So deletion is the last moment at which the information is still there to keep. Without this the
artists fell to `ManagedByUnknown` while their wants kept a `manager` provenance naming an authority
that no longer existed.

**What happens to a `Manager` row that ends up governing nothing: nothing.** A manager is
configuration owned by *libraries* — a base URL and a credential — not a container of artists.
Deleting one because its last artist walked away would throw away what the user configured, and it
would be wrong on its own terms: its libraries are still its, so the next file to appear in one is
managed by it again. An empty manager is idle, not obsolete.

### Clearing a want whose authority is gone

`clearDesire` is ungated (see [the gate predicate](#manager-authority--lidarr-owns-identity)), but a
mirrored want renders as *derived* — frozen toggle, **Pin** offered — so until now there was no way
to remove one from the page. The artist row offers **Dismiss** when a `manager` want's authority no
longer governs the artist: the manager was deleted, or the artist was detached before that row was
re-labelled. The pill reads **was managed** rather than naming a manager that cannot be asked to
change its mind.

It is offered *only* in that case. While the manager still governs, dismissing would be undone by
its next sync, and a button that works and then silently reverts is worse than no button.

## Following

**Follow** an artist to auto-want a *shape* of release; **Add to wanted** picks one album by hand.
Following is a convenience, never a precondition for picking something.

`collection.FollowWants` is the single exported definition of "would following want this?" — the
discography sync, the "why is this wanted" label and the UI all route through it, so changing an
artist's settings changes every view at once. Settings live on `CollectionArtist`
(`FollowTypes` CSV + `FollowSecondary`) and default to studio albums + EPs; secondary types are off
because including live albums, compilations and remixes buries the missing list under reissues.

`collection.FollowGoverns` decides whether the native follow settings apply at all — they do not
when a manager owns the artist, since Lidarr is the authority there. An explicit desire always
overrides the type filter: the UI must never refuse to keep a single or live album you just asked
for.

### Following can start at a year

`FollowFromYear` is the back-catalogue half of the same problem the type filter solves. Following an
artist you already own the old records of lists twenty albums you deliberately have, and buries the
one new record — which is the thing you followed them for. Zero, the default and what every existing
row carries, means no cutoff and the behaviour following always had.

**A year, not a date.** MusicBrainz stores `FirstReleaseDate` as `YYYY`, `YYYY-MM` or `YYYY-MM-DD`
depending on what an editor knew, so a day-precision cutoff would be asking the data a question it
cannot answer. The year is the prefix of all three, which makes the comparison exact at every
precision instead of approximate at two of them. It also covers more than the "only future releases"
toggle this replaced: set it to the current year for that, or to 2010 for "I have the old stuff".

**An undated release-group is excluded once a cutoff is set.** This is a judgement call and the
other reading is defensible, so: a cutoff is opt-in and its whole purpose is to keep the back
catalogue out, and a release-group MusicBrainz has no date for cannot be shown to clear it. Anything
actually being released is dated upstream before it comes out, so an undated row is far more likely
an obscure old entry than a new record — and including them would let the noise the cutoff exists to
remove back in through the one gap nobody can see. Clearing the cutoff wants them all again;
nothing is lost permanently.

It applies at both ends, through the one definition: `SyncArtist` does not record a release-group
below the cutoff, and `FollowWantsStored` does not label a stored one as wanted. A page that said
"wanted" about an album the sync would never record is exactly the disagreement `FollowWants` exists
to prevent.

A **merge** takes the earlier of two cutoffs, and no cutoff on either side wins outright
(`migration.earlierCutoff`). Every other follow setting merges toward wanting more; this is the one
where the inclusive value is the smaller number rather than the larger, so it needs saying.

## Wanted sources

`wanted_source` is derived server-side and names *what could change it*:

- `explicit` — you picked this album; the row can unpick it.
- `auto` — it follows from following the artist, or from the rebuild narrowing your want to the
  edition you own.
- `manager` — the library's manager (Lidarr) monitors it, or selected this edition.

An explicit pick outranks anything derived, so it survives unfollowing, a manager change, or the
manager dropping the album. Precedence runs manual desire → manager desire → auto desire → catalog
monitoring → follow settings, and it is derived from the group's **desire rows themselves**:
`newReleaseGroupView` takes the rows rather than the three things previously split out of them at
each call site, because that split both threw away the provenance and was forgettable — one of the
three callers dropped the recordings, so `GET /artists/:mbid` reported "whole album" for a want that
had specific tracks while the discography endpoint reported it correctly. One argument cannot
disagree with itself. Guarded by `routers/wanted_source_test.go`.

Provenance has a real **unknown** state (`models.ManagedByUnknown`) for an artist whose library
manager cannot be resolved. Absence of information is never presented as a positive claim that an
artist is natively managed.

## Manager authority — Lidarr owns identity

When an artist is managed by Lidarr, **Lidarr is the sole authority over *identity*** — which albums
are wanted, which release/edition is selected, and which track a file maps to. Autotaggerr's job
shrinks to *tagging* files to match Lidarr's answer and *refreshing Plex*. `mixed` counts as Lidarr:
if Lidarr governs an artist at all, identity is Lidarr's, so the gate is one bit per artist, not a
per-release negotiation. *Authoring* a desire is therefore a **native-manager** act — for a Lidarr
artist the same facts arrive via `SyncLidarr` (`catalog_monitored` = the want, the monitored release
= the chosen edition), and running both authorities at once is what produces contradictions ("20/32
on an old release" while the album is green in Lidarr).

What Lidarr decides is still *recorded* as `CollectionDesire` rows — see
[manager-derived wants](#manager-derived-wants) — but they are written by the mirror and never by
the user, so there is still exactly one authority per artist. The table is the shared vocabulary for
"what is wanted"; who may write a row is what the manager boundary governs.

The boundary is enforced by a single predicate and four mechanisms:

- **One gate predicate.** `collection.IdentityEditable(artist)` / `ArtistIdentityEditable(db, mbID)`
  (`managedBy != lidarr && != mixed`; unknown artist = editable) — the identity-side sibling of
  `FollowGoverns`. Enforced in the API (`requireIdentityEditable` in `routers/attach.go` gates
  attach/bulk-attach on the file's *library* manager; `requireArtistIdentityEditable` gates
  `setDesire`), returning **409**, and surfaced as the `identity_editable` field on the artist,
  release-group and library-item views so the UI hides the control before the reject is ever hit.
  `clearDesire` is deliberately left ungated — clearing a stale want is a pure removal, like detach.
- **No tag fallback under Lidarr.** `ResolveCorrelation` (`modules/files.go`) normally falls back to a
  file's embedded MB tags when Lidarr returns nothing; under a Lidarr manager *with a client* that
  fallback would preserve a stale identity, so the file becomes `unmatched` (`modules.ErrUnmatched` →
  `LibraryItemStatusUnmatched`) instead — not an error, not tagged, re-attempted on the next run. A
  credential-less Lidarr row keeps the tag fallback so an outage cannot orphan a library; the native
  manager keeps it always (tags are its only source).
- **Force re-correlate** (per artist / release-group / library). Changing the selection in Lidarr is
  the source of truth, but two things keep a diverged file stuck: `shouldSkip` never re-processes on an
  upstream selection change, and a manual attach is `Pinned`. The verb defeats both — it busts the
  Lidarr caches (`modules.LidarrInvalidateCaches`), clears `Pinned` on Lidarr-governed items in scope,
  re-runs the pipeline **ignoring `shouldSkip`**, and writes Lidarr's current release, leaving anything
  Lidarr still cannot match `unmatched`. Exposed as `POST /{artists|release-groups}/:mbid/recorrelate`
  and `POST /libraries/:id/recorrelate` (see [scanning.md](scanning.md#per-artist-actions)).
- **Track-not-found is surfaced, not silent.** When Lidarr's release ID and track ID disagree
  (common mid-migration), tagging returns `modules.ErrTrackNotInRelease` wrapped with an actionable
  message pointing at force re-correlate, and the file lands as a visible `error` item on the next run
  re-attempts — rather than silently keeping its old tags.

### The UI under a manager

`identity_editable` reaches the artist view, the release-group view and the library-item view, and
the UI reads it as **locked**: a manager owns identity here, so nothing on this surface writes it.
That is deliberately stronger than *derived*. Derived freezes the want itself while still allowing a
narrowing (picking an edition or a track is how a followed album becomes yours); locked freezes the
narrowing too, because under Lidarr the edition and the track are its decisions as well.

Concretely: the artist header carries a **Managed by Lidarr** pill beside the frozen Follow button
(the reason used to live only in a `title`, which is state the page had but did not show); the album
row's **Want** is disabled with the authority named; **Pin** is not rendered, since pinning writes a
want that a locked artist rejects; the release-group page's edition and track checkboxes are readouts;
and **Attach** on a Lidarr-managed file is disabled, with select-all picking only the files that can
actually be attached.

The part that was a *bug* rather than a gap: an album Lidarr does **not** monitor showed a live
**Want** button, because the UI froze on "is this want derived" and an unwanted album has no want to
derive. Clicking it hit the 409. Locked is a property of the artist, not of the want, which is why
the flag rather than the want's shape is what the controls read.

One consequence worth stating: the page must decide "is this want the user's" from `wanted_source`,
**never** from whether desire rows exist. Since manager and auto wants are rows now, "has rows" means
nothing about authorship — the release-group page read it that way and would have offered live
controls on exactly the albums that reject them.

Locking is the read-only half of the boundary — naming the authority and freezing what it owns. The
**action** half is [detaching](#detaching-a-manager), which sits in the artist's Settings disclosure
under an **Authority** heading rather than in the header beside Scan and Tag files: it is a decision
made once, and a permanently visible *Detach* would read as an everyday command. The panel renders
in all three states (managed, detached, natively managed), because "Autotaggerr already decides this
one" answers the same question, and a panel that disappears once answered leaves the user unsure
whether they ever answered it. The header pill reads **Native · detached** rather than plain
*Native*: both mean Autotaggerr decides, but only one is a decision someone made, and the artist's
files do still live in a managed library.

## Hard-won UI rules

These came out of live testing and are recorded in [style-guide.md](style-guide.md); they are
repeated here because they are about *this* data model.

- **Make the default a control, and the unrepresentable states go away.** The release-group page used
  to carry three scope buttons (any release · whole album / any release · specific tracks / specific
  editions) above interactive panes that could express the same thing more precisely. Scope had to be
  component state, because two of the three had an empty state the DB cannot hold: "specific tracks,
  none picked" stores byte-identically to "whole album", and "specific editions, none marked" stores
  like no want at all.
  Both problems were the *modes*, not the data. Giving each default a real checkbox — an **Any
  edition** row above the edition list, an **All tracks** row above the tracklist — makes those states
  visible, selectable and returnable-to, and leaves nothing for a mode to remember. There is now no
  scope state at all: every checkbox reads straight off the stored desire rows, and a summary line
  under the header says what they add up to. Two controls for one intention is one too many, and the
  buttons were the weaker one.
- **Derived state is never a toggle.** An `auto` or `manager` want is shown as state (a pill naming
  the authority) with the toggle frozen, plus a separate **Pin** action to make it yours — where
  pinning is possible at all (see [the UI under a manager](#the-ui-under-a-manager)). A toggle whose
  off direction silently does nothing is worse than a disabled one.
- **A derived state still has a value.** A want with no *edition* rows means *any edition, whole
  album*, so the page shows **Any edition** and **All tracks** ticked (and frozen — narrowing is
  allowed, switching it off is not) rather than an unticked list next to a wanted album. When the
  derived want *does* name an edition (Lidarr's monitored release), that edition is ticked instead
  and the summary line says which pressing by name — "any edition" above a list with one ticked is
  the page arguing with itself.
- **The label carries the state** — `Following`/`Follow`, `Wanted`/`Want`. An accent fill alone was
  consistently read as an invitation to click.
- **One word per concept.** "Wanted" everywhere; never "monitor" in the UI (it is the DB field
  name, and two words for one idea made the first version unreadable).

## API

| Endpoint | Purpose |
|----------|---------|
| `GET /artists` | the collection list with owned/complete/partial/missing/mismatch counts |
| `GET /artists/:mbid` | artist detail: release-groups (with derived `complete`, `discrepancy`, `wanted*`) + desires |
| `GET /artists/:mbid/info` | who the artist is (kind, origin, active years, top genres) — a live MusicBrainz entity read for the page header, cached 24h. Its own endpoint so the page never waits on it; a failure means the header shows less, never an error. |
| `GET /artists/:mbid/discography` | live MusicBrainz read of *all* release-group types, **not stored** — browsing a catalogue must never require committing to it, or inflate the missing count. Cached 6h; a stale copy beats an empty page when MB is down. |
| `GET /artists/:mbid/release-groups/:rgid` | the group, every edition (annotated with owned state), and that group's desires, in one call |
| `POST /artists` · `GET /search/artists` | add an artist you own nothing of |
| `POST /artists/:mbid/follow` | follow settings, then re-sync with them |
| `POST\|DELETE /artists/:mbid/desires` | author or clear intent |
| `POST /artists/:mbid/detach` | [take authority back](#detaching-a-manager) from the manager, keeping its decisions as manual wants. **409** when no manager governs the artist — the request is well-formed and was true of an earlier state, so the page is out of date rather than wrong. Idempotent. |
| `DELETE /artists/:mbid/detach` | hand the artist back; provenance is re-derived at once. Kept wants stay manual. |
| `POST /scan` · `POST /artists/:mbid/scan` | re-derive the disk view, collection-wide or for one artist |
| `POST /collection/sync-lidarr` · `POST /artists/:mbid/sync-lidarr` | [mirror Lidarr](#mirroring-lidarr-at-two-scopes), collection-wide or for one artist. Both take an optional `{"ignore_cache": true}`. **400** without an enabled Lidarr manager; the per-artist form is also **404** for an unknown artist and **400** for one the mirror does not govern |

`Rebuild` also runs automatically at the end of every processing run and drift sync.

## UI

These three are browsing surfaces first and editors second, which is what decides how they look: an
artist avatar and album cover on every row, a coverage meter instead of columns of counts, and sort +
filter state kept in the URL so opening an album and coming back does not reset the list. See
[style-guide.md](style-guide.md) for the components (artwork, coverage meter, entity header, table
toolbar, grouped sections).

- `/collection` — artist list: avatar, provenance badge, coverage meter, missing/mismatch counts,
  wanted summary. Sortable by name, missing and mismatch; filterable by text and by "mismatched".
  The page head holds only the two actions that change *what the collection holds* — **Add artist**
  and **Sync from Lidarr**, neither of which queues anything — and the four verbs sit below in the
  **run bar** with the single status they share (see
  [style-guide.md](style-guide.md#components)). The split is by whether an action queues a job, not
  by which manager a user runs, which is the same line the artist page draws.
  The bar's state is polled while a job is in flight, and the artist list reloads once one
  finishes: a status fetched once at mount leaves four buttons disabled or enabled by a snapshot,
  and a run started from this page changes the ownership the whole table is drawn from.
  Its coverage meters are the **proportional** form on every row whatever the album count, because
  one shape down a column of artists reads better than a cell count on the short rows; the mono
  `8/12` beside each answers "how many".
- `/collection/:mbid` — the artist page: an entity header (portrait, backdrop, kind/origin/years/
  genres from `/info`, album coverage, Following toggle with the follow types behind a **Settings**
  disclosure), then the **run bar** with the four verbs and Re-correlate, a chip row that doubles as
  the counts and the filters, then the catalogue split into
  **Albums / EPs / Singles / Other** collapsible sections. Anything carrying a secondary type (live,
  compilation, remix, soundtrack) lands in *Other* — the same rule following already uses for what
  counts as an album, and what keeps a reissue-heavy catalogue from burying the six records a person
  thinks of as the discography. Singles and Other start closed, and **each section pages on its own**
  (50 rows, keyed `page-<section>` in the URL) — a prolific artist has six albums and three hundred
  singles, so the one long section pages while the rest render whole.
- `/collection/:mbid/:rgid` — the release-group page: an entity header (cover, type/year/artist, track
  coverage, the derived want summary, Wanted/Pin), then a `.rg-split` master/detail with editions on
  the left and the selected edition's tracklist on the right.
  Each edition has a **checkbox** (want this edition) and a separate row body (show me its tracks) —
  two jobs, two hit areas, so ticking never triggers a fetch. Above the list sits the **Any edition**
  default row; above the tracklist, **All tracks**. Editions are filterable and sortable too, since a
  reissued album can list forty. Recordings wanted but absent from the edition on screen are
  **reported, not dropped**.

All three are real routes, not modals: they are browsing destinations as much as editors, and they
want a URL and a back button.

**Sync from Lidarr appears on `/collection` and on the artist page**, gated on two conditions that
are not the same fact: the artist has to be Lidarr's to answer for (`managed_by`), *and* an enabled
Lidarr manager has to exist to ask. A row can still read "managed by Lidarr" after its manager was
disabled or deleted, and the endpoint answers that with a 400 — a button whose only outcome is an
error message is a button that should not be there.

Both open the same `SyncLidarrDialog`, and it is a dialog rather than a bare button because
`ignore_cache` is not guessable: dropping the cache changes nothing about the numbers *this* pass
fetches, it changes what the **next scan** matches files against (see
[above](#mirroring-lidarr-at-two-scopes)). That needs a sentence, and a sentence needs somewhere to
live. The box starts unticked.

On the artist page the button sits in the **header**, outside the run bar's four verbs, and is not
disabled while a job runs. Those four are Autotaggerr acting on this artist's files and metadata and
they share the one job queue; this one only re-reads what the manager says, and queues nothing —
the same reason it stays in the collection page's head rather than its run bar.

## Artwork

Covers come from the **Cover Art Archive** (keyed by release-group or release MBID, no credential,
seeded enabled); artist portraits and backdrops from **fanart.tv** (keyed by artist MBID, needs the
user's own API key, so it is not seeded — without it artists get monogram tiles and nothing else
changes). Both are `DataSource` rows, administered on the Data sources page like AcoustID.

`GET /artwork/:entity/:mbid` proxies them, and it is proxied rather than hot-linked for three
reasons: the fanart.tv key must not reach the browser, the disk cache means a cover is fetched once
per install instead of once per visitor, and a page of covers never reveals the user's IP to an
external host. Three properties make it safe to call from a table with a hundred rows — a disk cache
under `config/artwork/`, a **negative** cache so "no art for this MBID" is not re-asked on every
paint, and single-flight so N rows racing for one uncached image make one upstream request. Its own
per-host rate limiter, deliberately *not* the 1 req/s MusicBrainz one: a page of covers would take a
minute to fill for no reason.

The route is **public**, because an `<img>` tag cannot send an `Authorization` header. It leaks
nothing — every response is a cover or a photo keyed by an MBID, the endpoint answers for any MBID
rather than only ones in this collection, and the credentials stay server-side. The negative cache is
bounded (`artworkNegativeMax`) precisely because an unauthenticated caller can ask about any id.

**A missing image answers `204 No Content`, not `404`.** Most artists have no fanart.tv entry and
many releases have no cover, so one pass over a collection page asks for hundreds of images that
legitimately do not exist. As 404s that traffic is indistinguishable from a client probing for
files, and a log-watcher in front of the app (fail2ban and friends) bans the user for their own
browsing. 204 is also the more honest reading — the request was understood and answered, and the
answer is that there is no image for this entity.

The empty body still fires the `<img>` error event, so the monogram tile takes over exactly as
before. Serving a placeholder image instead would suppress that event and replace a tile showing the
artist's initials with a generic glyph, to fix something that is only about status codes.
`artworkCapabilities` still short-circuits the *whole-provider* case, so a disabled or keyless
provider produces no requests at all; the 204 is for the per-entity case, which cannot be known
without asking.

## Tests

`modules/artwork_test.go` covers the artwork path against a stub provider: that the disk cache and the
negative cache each hold (asserted by counting upstream requests, not by inspecting the caches), that
concurrent rows single-flight, that a non-UUID or an impossible entity/kind pair is a *bad request*
rather than a provider failure, and that an unconfigured provider is `ErrNoArtwork` rather than an
error — a keyless fanart.tv is opt-out, not broken.

`collection/` covers the rebuild, the disk/catalog separation, the Lidarr mapping (against a mock
Lidarr), per-edition ownership and pruning, and that desires survive the most destructive rebuild.
`desire_manager_test.go` covers the mirrored wants against a mock Lidarr whose album list a test can
change between syncs: the monitored release is stored and wanted, it re-points when Lidarr's
selection moves, it is pruned when monitoring stops, it is not invented when Lidarr selected no
release, and it never overwrites a hand-authored want, appears on a native artist, or is disturbed by
a rebuild that owns nothing.
`TestSyncLidarrScopedToArtist` pins the property the artist scope exists for — the artist outside the
scope is left untouched, not merely rewritten with the same values — and that an artist the mirror
does not govern syncs *nothing* rather than everything, a scope silently widening to the whole
collection being the worst failure the option could have.
`modules.TestLidarrInvalidateArtistCaches` pins the other half: the scoped drop takes the artist's
four entries and no one else's.

`routers/` covers the wanted-source rules and that recordings round-trip through the HTTP handler —
added after a field reached the model, the service and the UI but was silently dropped by the
handler in between. **A field is not wired until the handler is tested.**
`TestSyncLidarrArtistGating` covers the per-artist sync's three refusals (unknown artist, natively
managed artist, no Lidarr manager) and that the `ignore_cache` body is genuinely optional.

## Related

- [media-manager.md](media-manager.md) — the component model this sits on.
- [attach.md](attach.md) — how files become owned in the first place.
