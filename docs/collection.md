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

Three paths do:

| Path | How |
| --- | --- |
| a scan | `collection.Rebuild` at the end of the run |
| applying a migration | `collection.Rebuild` after the remap |
| manual attach | `collection.Rebuilder.Request()`, from `saveCorrelation` |

Attach was missing for a long time, so attaching files by hand left the collection stale until
the next scan — which is the only reason *Rebuild from library* had to be a button a user was
expected to understand. It is now a repair affordance, not a step in a workflow.

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

`Rebuild` itself runs in a transaction. Its first act is to clear the disk view wholesale before
re-establishing it, so without one a failure partway — or two overlapping passes — would leave
the collection claiming to own less than it does. Silent and wrong is the worst combination, so
the write helpers propagate their errors here and the whole pass rolls back. The Lidarr and
discography syncs keep the older log-and-continue behaviour: one unwritable album must not
abandon a whole sync.


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
*named* artists, plus links dropped because MusicBrainz no longer names the artist. It rides the scan
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

## The disk/catalog split

`CollectionReleaseGroup` carries two independently written blocks:

- **disk** — `owned`, `owned_tracks`, `total_tracks`. Written *only* by `collection.Rebuild`, which
  aggregates `library_items` against *cached* releases and never fetches.
- **catalog** — `in_catalog`, `catalog_owned_tracks`, `catalog_total_tracks`, `catalog_monitored`.
  Written *only* by `SyncLidarr` and native discography discovery.

This split exists because both authorities originally wrote the *same* columns, so whichever ran
last decided what the UI showed — and since `Rebuild` runs automatically after every scan, a scan
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
Ownership is **derived**, rebuilt from disk on every scan. They live in separate tables so the
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
own record of what Lidarr decided, which is what makes detaching the manager later a change of
authority instead of a loss of data (see [wip.md](wip.md) for the detach verb itself).

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
  `LibraryItemStatusUnmatched`) instead — not an error, not tagged, re-attempted next scan. A
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
  message pointing at force re-correlate, and the file lands as a visible `error` item the next scan
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
| `POST /collection/rebuild` · `POST /collection/sync-lidarr` | re-derive the disk view / mirror Lidarr |

`Rebuild` also runs automatically after every scan and drift sync.

## UI

These three are browsing surfaces first and editors second, which is what decides how they look: an
artist avatar and album cover on every row, a coverage meter instead of columns of counts, and sort +
filter state kept in the URL so opening an album and coming back does not reset the list. See
[style-guide.md](style-guide.md) for the components (artwork, coverage meter, entity header, table
toolbar, grouped sections).

- `/collection` — artist list: avatar, provenance badge, coverage meter, missing/mismatch counts,
  wanted summary. Sortable by name, missing and mismatch; filterable by text and by "mismatched".
- `/collection/:mbid` — the artist page: an entity header (portrait, backdrop, kind/origin/years/
  genres from `/info`, album coverage, Following toggle with the follow types behind a **Settings**
  disclosure), a chip row that doubles as the counts and the filters, then the catalogue split into
  **Albums / EPs / Singles / Other** collapsible sections. Anything carrying a secondary type (live,
  compilation, remix, soundtrack) lands in *Other* — the same rule following already uses for what
  counts as an album, and what keeps a reissue-heavy catalogue from burying the six records a person
  thinks of as the discography. Singles and Other start closed.
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
`routers/` covers the wanted-source rules and that recordings round-trip through the HTTP handler —
added after a field reached the model, the service and the UI but was silently dropped by the
handler in between. **A field is not wired until the handler is tested.**

## Related

- [media-manager.md](media-manager.md) — the component model this sits on.
- [attach.md](attach.md) — how files become owned in the first place.
