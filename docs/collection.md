# Feature: the collection (present vs wanted)

Two different questions, deliberately answered by two different authorities:

- **Present** — what you own, organised artist → release-group → release. Universal, computed from
  the library for every manager.
- **Wanted** — the gaps. Manager-owned: the native manager computes them from MusicBrainz, while
  Lidarr is the source of truth for its own artists and is *mirrored*, never replicated.

One UI mental model — an artist-completeness **Collection** page — with the manager difference
reduced to a provenance badge and which actions are offered.

## Entities

| Model | Written by | Holds |
|-------|-----------|-------|
| `CollectionArtist` | `Rebuild` (library) / `AddArtist` (manual) | name, `ManagedBy`, `Origin`, follow settings |
| `CollectionReleaseGroup` | `Rebuild` (disk block) + `SyncLidarr`/`SyncArtist` (catalog block) | album-level ownership and catalog state |
| `CollectionRelease` | `Rebuild` only | one row per **owned edition**: per-edition track counts |
| `CollectionDesire` | the user, via the API | authored intent — never recomputed |

Named `Collection*` to avoid clashing with the MusicBrainz response types.

`Origin` (`library` \| `manual`) records how an artist *entered* the collection. `Rebuild` stamps
`library` on create and never overwrites `manual`, so an artist you added by hand keeps that
provenance once files for them appear — and a file-less artist is not treated as an anomaly.

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

Suppressed when the artist has no catalog to compare against, since otherwise every album of an
unfollowed native artist would flag.

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
- `auto` — it follows from following the artist.
- `manager` — the library's manager (Lidarr) monitors it.

An explicit pick outranks anything derived, so it survives unfollowing, a manager change, or the
manager dropping the album. Guarded by `routers/wanted_source_test.go`.

Provenance has a real **unknown** state (`models.ManagedByUnknown`) for an artist whose library
manager cannot be resolved. Absence of information is never presented as a positive claim that an
artist is natively managed.

## Hard-won UI rules

These came out of live testing and are recorded in [style-guide.md](style-guide.md); they are
repeated here because they are about *this* data model.

- **Intent is UI state; only outcomes go in the database.** Two of the three desire scopes have an
  empty state the DB cannot represent — "specific tracks, none picked" stores byte-identically to
  "whole album", and "specific editions, none marked" stores like no want at all. Scope is therefore
  component state, seeded once from what is stored, and only fully-expressed choices persist.
- **Derived state is never a toggle.** An `auto` or `manager` want is shown as state (a pill naming
  the authority) with the toggle frozen, plus a separate **Pin** action to make it yours. A toggle
  whose off direction silently does nothing is worse than a disabled one.
- **A derived state still has a value.** A want with no desire rows means *any release, whole
  album*, so the editor opens on that rather than on "nothing wanted".
- **The label carries the state** — `Following`/`Follow`, `Wanted`/`Want`. An accent fill alone was
  consistently read as an invitation to click.
- **One word per concept.** "Wanted" everywhere; never "monitor" in the UI (it is the DB field
  name, and two words for one idea made the first version unreadable).

## API

| Endpoint | Purpose |
|----------|---------|
| `GET /artists` | the collection list with owned/complete/partial/missing/mismatch counts |
| `GET /artists/:mbid` | artist detail: release-groups (with derived `complete`, `discrepancy`, `wanted*`) + desires |
| `GET /artists/:mbid/discography` | live MusicBrainz read of *all* release-group types, **not stored** — browsing a catalogue must never require committing to it, or inflate the missing count. Cached 6h; a stale copy beats an empty page when MB is down. |
| `GET /artists/:mbid/release-groups/:rgid` | the group, every edition (annotated with owned state), and that group's desires, in one call |
| `POST /artists` · `GET /search/artists` | add an artist you own nothing of |
| `POST /artists/:mbid/follow` | follow settings, then re-sync with them |
| `POST\|DELETE /artists/:mbid/desires` | author or clear intent |
| `POST /collection/rebuild` · `POST /collection/sync-lidarr` | re-derive the disk view / mirror Lidarr |

`Rebuild` also runs automatically after every scan and drift sync.

## UI

- `/collection` — artist list: completeness, provenance badge, wanted summary.
- `/collection/:mbid` — the artist page: stat row, follow panel, discography table with filter tabs.
- `/collection/:mbid/:rgid` — the release-group page: scope choice, then a `.rg-split` master/detail
  with editions on the left and the selected edition's tracklist on the right. Each edition carries
  its own Wanted toggle and its own track selection, mapping 1:1 onto desire rows. Recordings wanted
  but absent from the edition on screen are **reported, not dropped**.

Both are real routes, not modals: they are browsing destinations as much as editors, and they want
a URL and a back button.

## Tests

`collection/` covers the rebuild, the disk/catalog separation, the Lidarr mapping (against a mock
Lidarr), per-edition ownership and pruning, and that desires survive the most destructive rebuild.
`routers/` covers the wanted-source rules and that recordings round-trip through the HTTP handler —
added after a field reached the model, the service and the UI but was silently dropped by the
handler in between. **A field is not wired until the handler is tested.**

## Related

- [media-manager.md](media-manager.md) — the component model this sits on.
- [attach.md](attach.md) — how files become owned in the first place.
