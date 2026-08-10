# Refresh metadata (the MusicBrainz mirror)

Autotaggerr keeps a local copy of every MusicBrainz entity its collection refers to, refreshed on
a schedule, so browsing and tagging read the database instead of the network.

## The three verbs

Everything Autotaggerr does to a library is one of three verbs, and each runs at whatever scope
you point it at — one artist, one library, everything:

| Verb | Reads | Writes files |
| --- | --- | --- |
| **Process** | disk + MusicBrainz | **yes** |
| **Refresh metadata** | MusicBrainz | no |
| **Tag files** | the local database | **yes** |

Two modifiers apply to any of them: **scope** (one artist, one library, everything) and, for
refresh, **force** (ignore cached copies). Neither is a verb. Adding one for "check every ID"
would have meant a fourth name for something the refresh verb already does.

**No verb cascades into another.** Each does exactly what its label says and stops. This matters
because two of the three rewrite the user's audio files, and a button that quietly does more than
it claims is the wrong place to be clever — *Refresh metadata* used to re-tag every file of a
release that had changed, which meant a button about reading could rewrite hundreds of files.

What replaces the cascade is a handover. A refresh **reports** the releases whose metadata changed
upstream; the next processing run re-tags them in its drift stage, and a user who wants it immediately presses
*Tag files*. Nothing is lost, and nothing happens that was not asked for.

This package (`mirror`) is the refresh verb. `process.Runner` owns the two that write.

### One name, two forms

A verb the user can press needs exactly one name, or the same action read on two pages looks like
two actions. The rule:

- **A control says the verb**: *Refresh metadata*. Every button that starts one — Metadata,
  Activity, Libraries, the artist page — says those two words and nothing else.
- **A record says the noun**: *Metadata refresh*. Queue entries, event rows, log lines and job
  titles describe a thing that ran, not a thing to press. A pass that ignored the cache is a **Full
  metadata refresh**, the only variant that reads differently — because it is the only one that
  behaved differently.

Nothing says *sync*, *check for updates* or *verify identities*. Those were three separate names
this verb accumulated, each on a different page, and the last of them titled a queue entry whose
own event said "Full metadata refresh".

**"Mirror" is not a UI word.** It is the package name, the config keys (`autotaggerr_mirror_*`),
the route (`/mirror`) and this document's word for the local copy. The settings section that
configures it is titled *Metadata refresh*, and the nav item is *Metadata* — which names the area,
not the verb, and is the one place the two forms do not apply.

## Why a mirror rather than a cache

MusicBrainz is not slow — it is **rate limited to roughly one request per second**, and that
budget is spent by whoever asks first. With on-demand caching alone, the person who discovers
every expired entry is the *user*: they open an artist page, and the page blocks while five
paginated discography requests trickle in behind the limiter.

A mirror does the same work on a schedule, so the cost lands in a job nobody is waiting on. The
on-demand path is still there underneath, and deliberately so — a mirror pass warms what the
collection already knows about, and an artist added a minute ago is not in it yet. The mirror
makes a cold start rare; it does not replace the fallback.

## What is cached, and where

| Data | Store | TTL |
| --- | --- | --- |
| Release lookup (`/release/{id}`) | `musicbrainz_release_cache` | 7–14 d (jittered) |
| Artist lookup (`/artist/{id}`) | `musicbrainz_entity_cache`, entity `artist` | 7–10.5 d (jittered) |
| Artist discography (`/release-group?artist=`) | `musicbrainz_entity_cache`, entity `discography` | 7–10.5 d |
| Release-group editions (`/release?release-group=`) | `musicbrainz_entity_cache`, entity `editions` | 7–10.5 d |
| Cover art / artist images | files under `config/artwork/`, indexed by `artwork_cache_entries` | 30 d |
| "No artwork for this MBID" | `artwork_cache_entries` with `missing = true` | 7 d |
| Lidarr artists / albums / tracks / track files | `provider_cache`, one source each | 1 h |
| Plex album keys | `provider_cache`, source `plex_album_keys` | 1 h |
| AcoustID fingerprints and candidates | `acoustid_lookups` | by file size + mtime |
| Release / artist **search** | not cached | — |

Search is the deliberate exception: a query is one-off, its results are ranked rather than
authoritative, and the release a user picks out of them is fetched (and cached) separately.

Every MusicBrainz TTL is **jittered** — the stored expiry is the base plus a random fraction of it —
so entries warmed together by one pass do not all come due in the same minute a week later.

**Nothing durable is memory-only, and nothing is a JSON file.** Every cache above keeps an
in-memory front (that is the hot path — a run asks for the same artist's track files once per
track) over a database table warmed at startup by `modules.LoadAllCaches`, which `main` calls after
`modules.SetDB`. The maps that exist only in memory are single-flight bookkeeping and rate-limiter
timestamps: per-process facts that it would be wrong to persist.

### The provider cache

`models.ProviderCache` is keyed by `(source, key)` and holds what the services a library is managed
by answered: Lidarr's artists, albums, tracks and track files, and Plex's album keys. One table
rather than five, because all five are the same shape — a keyed blob with an expiry — so a sixth
endpoint is a new constant rather than a migration.

It replaced five JSON files under `config/`, and the format was the smaller half of the problem.
Those files were written by a **batched** flusher: a write marked the cache dirty and `FlushCaches`
rewrote the whole file later, and flushes ran only during a run, at the end of a refresh pass, or
in one-shot mode. With no shutdown handler anywhere, a restart between a lookup and the next flush
dropped the writes — a Lidarr sync triggered from the Collection page routinely never reached disk
at all. Every write now goes through as it happens, which is what let `registerCache`,
`markCacheDirty`, `FlushCaches` and the 30-second flush goroutine be deleted outright. (The batching
also carried a note that it was only safe because runs were single-threaded, which stopped being
true when the worker pool arrived.)

Each cache reads its legacy JSON file exactly once, while its source has no rows
(`providerCacheImportJSON`), so an upgrade does not re-ask Lidarr for everything it already knew.
Expired rows are **not** deleted: they are not restored into memory, they are overwritten by key on
the next fetch, and removing them would empty the source, which is the condition that one-time
import keys off.

**The import deletes its source** once the contents are in the database
(`removeLegacyCacheFile`), including on the boot where it finds the rows already there — so an
install that upgraded before this existed is cleaned up too. The five files
(`lidarr_{artists,albums,tracks,trackfiles}.json`, `mb_releases.json`, `plex_album_keys.json`) were
previously left behind on the reasoning that an import which deleted its own source would be
unrecoverable if it went wrong. What that actually left was a `config/` directory holding files
nothing reads, indistinguishable from live configuration, whose contents are stale within the hour
anyway — a 1 h provider TTL means there is nothing in them to recover. The one exception is a file
that **does not parse**: that is the case where the import really failed, so it is kept.

### The entity cache

`models.MusicbrainzEntityCache` is keyed by `(entity, mb_id)`, not by MBID alone. The same artist
ID is both an `artist` lookup and a `discography` browse, with unrelated payload shapes and
unrelated refresh costs. `Payload` is the raw JSON the lookup returned, so caching another
endpoint is a new constant rather than a schema change.

Reads go through an in-memory front warmed at startup (`modules.LoadAllCaches`); writes go
through to the database immediately. Write-through rather than batched, because these are single
rows fetched seconds apart — not the thousands-per-run churn that made the old release JSON file
expensive to rewrite.

A payload that no longer decodes into the caller's type counts as a **miss**, so a response shape
that changed between versions self-heals on the next fetch instead of surfacing as an error.

The `discography` entry is the one that stores more than the response: `{groups, complete}`, where
`complete` says the paging reached the end rather than stopping at
`maxArtistReleaseGroupPages`. Pruning release-groups on absence needs that flag, and it cannot be
recovered from the list — a truncated discography and a short one look identical — so before it was
cached the only caller that prunes had to bypass the cache to get it back. Entries written before
the flag hold the bare list and decode with `complete` false, which is the safe reading and empties
itself as they refresh (`decodeDiscography`). A stale copy served because MusicBrainz is down is
likewise never complete: it renders a page fine and must never delete rows, since anything added
upstream since it was cached is "absent" from it.

### An expiry is not an expiry date

Every MusicBrainz fetch falls back to its **expired** cache entry when the request fails: artists,
discographies, editions and artwork through `mbCacheGetStale`, and releases through
`staleCachedRelease` (their own map and table, same job). An expiry says when data is worth
re-checking, not when it stops describing the entity — and between those two readings sat a real
bug. The release path was the one lookup without a fallback, so a release held for months with its
TTL lapsed by an hour was discarded over a single 503, taking with it the correlation for every file
on that album. For releases the fallback lands *before* the in-flight coalescing releases its
waiters, because an album's tracks miss the cache together and so must survive together.

The release path diverges from the artist path in one place, deliberately. `GetMusicBrainzArtist`
serves a stale artist through a 404 once the deletion is recorded; `GetMusicBrainzRelease` lets
`ErrEntityGone` propagate instead. The difference is what the answer is used for: an artist lookup
renders a page, while a release drives the tags written to disk, and re-tagging an album against a
release MusicBrainz has deleted is a write that would have to be undone. The deletion is recorded
either way, so the pending migration is what keeps the album visible and offers a way to repoint it.

The fallback asks "is it *gone*?" rather than "is it `ErrTransient`?" — `ErrTransient` (any 5xx, 429,
or a transport failure) marks what is *known* to be worth retrying, not a list of the only things
that may fail. A 400 or an unparseable body is still not MusicBrainz saying anything about the
entity, and week-old truth beats dropping an album out of the disk view over a failure mode nobody
anticipated.

### One retry comes first

A transiently-failed fetch is repeated **once** before any of the above applies (`retryTransient`,
`modules/musicbrainz_retry.go`). MusicBrainz is a busy public service and a lone 503 or dropped
connection on one request is its common failure mode; one repeat absorbs most of those, and the
stale fallback then covers what is left. Ordering them that way matters — the stale copy is the
answer of last resort, and serving it in place of a retry that would have succeeded means a run
quietly works from week-old data it did not need.

**The spacing is the rate limiter's.** Every retried fetch begins with `RateLimit()`, and the
attempt that just failed was itself a request, so the repeat is already spaced by exactly the
interval the limiter enforces. There is no backoff of its own to tune or to fight with.

Only `ErrTransient` is retried. `ErrEntityGone` is an answer and cannot change by asking twice; a
400 or an unparseable body is a request this client will keep getting wrong, and repeating it would
hide the bug rather than survive it.

Where it sits differs by call, and each placement is the cheap one:

| Fetch | Retry wraps | Why there |
|---|---|---|
| release (`GetMusicBrainzRelease`) | the fetch **inside** the in-flight map | one leader retries for every waiter on that album; outside it, a cold album's tracks would each retry separately |
| discography (`GetMusicBrainzArtistReleaseGroups`) | **one page** | restarting the walk would re-spend a request per page already read |
| artist, search, `musicbrainzGetJSON` | the whole call | everything before the request is a cache read, so a repeat costs two misses |

**One, not more.** A second failure a second later is evidence the service is actually down, and
past that point the useful behaviours are the ones already here. Every attempt is a rate-limited
request, so a higher count would multiply both the time a run takes to fail and the load put on a
service that is already struggling.

### Artwork

Image bytes stay on disk. The database row carries only what the filesystem cannot express: when
the image was fetched, and whether the providers said there is no image at all.

The cache key is `entity_mbid_kind_size`, and **the size in it is the one that distinguishes an
image, not the one that was asked for** (`artworkKeySize`). Covers round to the 250/500/1200 the
Cover Art Archive actually serves. Artist images use `0`: fanart.tv returns whatever it has and
ignores the requested size, so keying by it stored one portrait three times — 250 for a collection
row, 500 for the artist page, 1200 for the backdrop — and, far worse, recorded "this artist has no
image" separately at each, at one request apiece. Most artists have no fanart entry, so that was
three requests per artist to learn the same nothing.

`artworkMigrateArtistKeys` folds pre-existing artist rows onto the size-less key at startup, files
and all, so the change does not present as an empty cache and re-ask the provider about a
collection's worth of artists it had already declined.

Images have the longest TTL of anything here, which is the right shape for data that is expensive
to transfer and effectively immutable. It is not *infinite* — which is what it was before the
index existed — because the Cover Art Archive gets better over time: a release with no art, or
with a poor scan of the artwork, eventually gets a proper one, and without an expiry that improvement would
never reach an install that once cached the placeholder.

A failed *refresh* falls back to the copy already on disk. Giving images an expiry is about
picking up better scans over time, not about discarding a good one because a CDN is down — the
same "stale beats blank" rule the MusicBrainz lookups follow.

A file with **no index entry is adopted** rather than ignored, so an install that cached artwork
before the index existed does not re-download its whole library; adoption gives the file a normal
expiry from that point on.

Negative results are capped at `artworkNegativeMax` (20 000) and dropped wholesale when the cap
is hit. Only negatives are capped: a positive entry required a real image to come back, while the
artwork endpoint answers for any MBID anyone asks about and is reachable without a session (an
`<img>` tag cannot send an `Authorization` header).

## Scopes

One implementation driven by a `Scope`, so per-artist and collection-wide refresh are the same
code with a different set of MBIDs in it. They used to be separate functions and had silently
drifted apart: the per-artist path never fetched edition lists at all, so opening an album right
after refreshing its artist still blocked on the rate limiter — the exact stall the refresh was
pressed to avoid.

| Constructor | Covers | Ignores TTL |
| --- | --- | --- |
| `CollectionScope(db, false)` | everything the collection refers to | no |
| `CollectionScope(db, true)` | the same, re-read from scratch | **yes** |
| `ArtistScope` | one artist, their discography, editions and releases | no |
| `LibraryScope` | everything one library's files point at | no |
| `DueScope` | only releases whose TTL has elapsed | no |

`CollectionScope` takes its release set from `collection.AllMBIDs`, which unions the **file index**
with the owned-editions table. Reading only `collection_releases` is what made a collection
refresh quietly skip releases that files on disk actually point at.

**One argument, on one constructor, is the only way to ignore the cache.** `CollectionScope`'s
`force` sets `Scope.Force`; nothing else sets it, and `TestOnlyAnExplicitForceIgnoresTheCache`
holds that line. The failure it guards against is silent and expensive — a forced scope re-reads
every entity it covers at one rate-limited request each, and a running pass looks the same either
way.

`ArtistScope` and `LibraryScope` used to force, on the reading that asking by hand means "check
now". That is a fair description of the intent and a poor description of the cost: it made the
per-artist button the expensive reading of words that mean the cheap thing everywhere else, and the
per-library one — a library being collection-sized — as expensive as re-reading everything. What a
user wants from either is *up to date*, which is what an unforced pass delivers. Distrusting every
cached copy is a different request, and it is asked in one place.

### Forcing is always deliberate

One place is necessary but not sufficient: the argument still had to be *chosen* rather than
carried in. Two rules complete it, both in the UI (`components/ForceRefreshDialog.tsx`):

- **A forced pass confirms; an ordinary one does not.** The dialog states the cost in the terms that
  decide it — one rate-limited request per entity, an estimate in hours derived from the cached-entity
  count, and *reads only, no files written*, which is what makes the wait acceptable rather than
  alarming. Confirming the ordinary pass too would train people to click through the dialog that
  matters.
- **The checkbox resets to off once a pass starts.** *Ignore cached copies* is a modifier on a button
  that gets pressed again later, so leaving it ticked turns one considered decision into a setting,
  and the next press — days later, possibly by someone who did not tick it — silently costs hours. It
  resets whether or not the request succeeded: what must not persist is the intent, not the outcome.

Forcing is now reachable from exactly two buttons — the Metadata page's checkbox and the Migrations
page's *Refresh metadata (ignore cache)* — and both come through the same dialog. A second copy of
the wording is how the verb drifted in the first place.

Nothing unattended forces: the nightly `SyncDrift` and the weekly run's `DueScope` stage are both
unforced. That property is the one to protect.

## The refresh pass

A pass walks its scope in this order:

1. artist entities
2. discographies
3. edition lists
4. release payloads

The order matters for an interrupted first pass: identity before catalogs before the heavy
per-release payloads, so a half-finished mirror can still name what it holds rather than sitting
on a thousand release payloads for artists it cannot label.

Two properties make a multi-hour first pass tolerable:

- **It is resumable.** A pass fetches only what is missing or expired, so an interrupted run
  costs nothing on the next one and no cursor has to be persisted. "Where it left off" is just
  "what is still not fresh".
- **It yields.** While a processing run is in flight, the pass pauses and re-checks every 15 seconds.
  Both draw on the same one-request-per-second budget, and the run is the one with a user
  attached.

Errors are counted and logged per entity, never fatal: one artist MusicBrainz cannot answer for
must not end a pass with a thousand others to warm. A pass that hit some still finishes `ok` with
a count — it is not a failed job.

### TTL tiering

Freshness is tiered by how much the collection actually cares:

| Release | TTL |
| --- | --- |
| owned (files on disk) | 7–14 d |
| catalogue only (pulled in by following an artist) | 30 d |

A release with files on disk drives the tags on those files. A release that only exists to answer
"what could I have" is reference data, and re-reading it weekly spends the rate limit that owned
releases need. Most of a followed artist's back catalogue is the second kind, so this is the
difference between a pass that finishes overnight and one that does not.

A pass records an Activity event (`mb_mirror`) and reports live status through
`mirror.Runner.Status()`. `Fetched` and `Fresh` are the pair worth reading — `Fetched` is what
actually cost a rate-limit slot. **A healthy steady state is almost all `Fresh`; a pass that is
mostly `Fetched` means the TTLs are shorter than the schedule.**

### What a pass records about itself

Counters say twelve releases changed upstream; they never say *which* twelve. Since that list is
the entire handover to the verbs that write files, a pass records two things beyond its totals:

- **`details.phases`** — the same counters split per phase (`checked` / `fetched` / `fresh` /
  `errors`), in the order a pass walks them. The four entity kinds cost wildly different amounts,
  and one total cannot say whether four hours went on discographies or on release payloads. A phase
  with nothing in scope is omitted rather than written as a zero, which would read as "we looked and
  found none".
- **`models.EventItem` rows**, one per entity worth naming: a release whose payload `changed`
  upstream, one that is `gone`, one that was `relinked` to a different release-group, and any entity
  the pass could not read (`error`, with the message). `Path` is the MBID and `Phase` is the
  mirror's own phase name, so a reader can group release payloads apart from discographies.

**One row per entity, not one per counter it bumped.** A release can both change and move
release-group; two rows for one MBID would be read as two releases, so the more consequential
outcome wins — a changed payload is what the file-writing verbs act on, a re-link rewrites a row
here and nothing else.

Rows are bounded at 500 (`maxDetailItemsRecorded`, matching the scan runner — they write the same
table), and `details.detail` carries `recorded` / `total` / `limit` so the UI can say "showing 500
of 3120" rather than implying 500 was all of it. They are written in one batch by `events.AddItems`
after the pass: a pass would otherwise interleave single inserts with the rate-limited fetches it is
pacing.

`RunStage` is `RunInline` plus an event of its own, owned by the run that called it — that is how a
processing run's refresh stage reports. It neither takes the pass guard nor publishes into the
shared `Summary`, because the run already holds the file-writing guard and is reporting its own
progress; a stage that reset the metadata runner's status would make `/mirror/status` describe a
pass nobody started. See [scanning.md](scanning.md#a-run-spawns-activities-each-one-is-a-row).

## Configuration

| Key | Default | Meaning |
| --- | --- | --- |
| `autotaggerr_mirror_disabled` | `false` | Turns the scheduled pass off entirely. |
| `autotaggerr_mirror_cron_schedule` | `0 0 3 * * *` | When the pass runs (nightly at 03:00). |

The key is `disabled` rather than `enabled` for the same reason the migration review flags are
phrased as opt-ins: a bool absent from an existing `config.json` decodes as `false`, and `false`
has to mean the default behaviour.

**Nothing runs at startup.** There was an `autotaggerr_mirror_on_start_up` key, from before the UI
had a button to press; it is gone. A first pass over a large collection is hours of rate-limited
fetching, and tying that to every restart makes restarting something to avoid. The nightly cron is
what gets a new install mirrored, and `POST /mirror/sync` is what starts one on demand.

## API

| Route | Purpose |
| --- | --- |
| `GET /api/v1/mirror/status` | Current/last pass, plus live cache coverage per entity kind. |
| `POST /api/v1/mirror/sync` | Start a pass in the background. `409` if one is already running. |
| `POST /api/v1/mirror/cancel` | Stop the running pass at the next entity boundary. |

`sync` returns `202` immediately — the pass is far too long to hold a request open for — and the
single-run guard is taken synchronously, so the response tells the truth about whether this
request started anything. Cancelling is safe at any point precisely because a pass keeps no
cursor.

## There is no "verify identities" verb

Detecting that MusicBrainz merged or deleted an entity is not an activity of its own. Merges and
deletions are recorded on the **HTTP path** — `RecordRedirect` when a payload comes back under a
different ID, `RecordDeletion` on a 404 — by whatever fetch happens to see them. So any refresh
that reaches the network finds them, and there is nothing for a separate sweep to do that a
forced refresh does not.

What used to be *Check every ID now* is therefore `CollectionScope(db, force: true)`. It records a
`mb_mirror` event titled **Full metadata refresh**, distinguished by title rather than by type —
the same way `LibraryScope` and `ArtistScope` both emit `process`. The queue entry
`Runner.VerifyIdentities` enqueues carries that same title, so the pass is named identically
wherever it is watched; `VerifyIdentities` survives as the Go name only.

Whatever a refresh finds is queued through `migration.ProcessPending` under
`migration.PolicyFromConfig`, so **a category held for review stays held**. A background job that
quietly re-pointed a user's records against their stated policy would be the worst kind of bug:
silent, and about identity. There are tests on both directions of that.

Forcing also drops the TTL tiering below. Forcing is the user saying they do not trust any cached
copy; honouring a 30-day "nobody owns this" TTL then would answer a different question.

**Cost:** a forced pass reads artists, discographies, edition lists *and* releases, where the old
identity check read releases and artists only — roughly double the requests. That is more correct
(forcing a discography is how a release added upstream is found), but it is a real increase, and
the button says so.

## How processing uses this

A processing run reads files through the *cache*, so without a refresh stage it would tag from a week-old
copy and never notice a release that changed upstream. `process.Runner.Run` therefore:

1. runs `RunInline` over `DueScope` — expired releases only, so a nightly run does not re-fetch
   the collection;
2. walks and tags as before;
3. **re-tags the files of every release the refresh reported as changed.**

Step 3 is necessary because `components.shouldSkip` drops files whose size and mtime are
unchanged — which is every file of a release that changed only upstream. The walk cannot catch
them by construction. This is the one place drift turns into file writes, and it lives in the processing run
because writing files is the processing run's job.

`RunInline` deliberately skips the pass guard and the Activity event: the run already holds the
file-writing guard and reports under its own event, and a run blocked by a scheduled refresh it
could happily run alongside would be absurd.

## Concurrency

Mutual exclusion is keyed on **whether a job writes files**, not on which runner owns it:

- file-writing work (process, tag) — exclusive, one at a time
- metadata-only work (refresh) — runs alongside, and yields at entity boundaries when
  file-writing work wants in

Without this, a collection-wide refresh — hours long — would hold a single shared guard and reject
a user's per-artist run for that entire time. A long background job must not starve short
interactive ones.

## UI

The **Metadata** page (`/mirror`) is built around making a long job legible rather than around
starting one: the phase, a progress meter, and the fetched / already-cached / failed / changed-
upstream counters, with per-entity cache coverage below. It polls every three seconds while a pass
runs. The trigger is a secondary action — the nightly schedule is what normally does this — and
"Stop" needs no confirmation, because stopping costs nothing.

The **no-writes contract is stated on the page**, not left to this document. It is the difference
between this verb and processing, and someone deciding whether to press a button needs to know it
without reading `docs/`. The artist page's three buttons carry the same information in their
tooltips: which of them write tags, and what *Refresh metadata* hands off to.

The Migrations page offers the forced pass as **Refresh metadata (ignore cache)**. The
parenthetical is not decoration: everywhere else those two words honour the cache, and a button
that quietly means the expensive reading of a shared verb is the trap this naming exists to avoid.
Finding merges is *why* forcing exists, so that page keeps a way in — it routes through the shared
confirm dialog rather than duplicating the verb with semantics of its own (see
[forcing is always deliberate](#forcing-is-always-deliberate)).

## Interaction with migrations

MBIDs are mutable: MusicBrainz merges and deletes entities. When a migration is applied, the
cache entries keyed on the retired ID are dropped — a release via `modules.DropCachedRelease`, an
artist via `modules.MusicbrainzForgetEntity` for *both* the `artist` and `discography` entries,
since two things are keyed on an artist MBID. Left in place they would expire and be re-fetched
on every pass, spending rate limit to re-learn a redirect already dealt with. See
[mb-migration.md](mb-migration.md).
