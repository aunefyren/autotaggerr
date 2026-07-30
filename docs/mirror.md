# Refresh metadata (the MusicBrainz mirror)

Autotaggerr keeps a local copy of every MusicBrainz entity its collection refers to, refreshed on
a schedule, so browsing and tagging read the database instead of the network.

## The three verbs

Everything Autotaggerr does to a library is one of three verbs, and each runs at whatever scope
you point it at — one artist, one library, everything:

| Verb | Reads | Writes files |
| --- | --- | --- |
| **Scan** | disk + MusicBrainz | **yes** |
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
upstream; the scan re-tags them in its own drift stage, and a user who wants it immediately presses
*Tag files*. Nothing is lost, and nothing happens that was not asked for.

This package (`mirror`) is the refresh verb. `scan.Runner` owns the two that write.

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
| Release / artist **search** | not cached | — |

Search is the deliberate exception: a query is one-off, its results are ranked rather than
authoritative, and the release a user picks out of them is fetched (and cached) separately.

Every TTL is **jittered** — the stored expiry is the base plus a random fraction of it — so
entries warmed together by one pass do not all come due in the same minute a week later.

### The entity cache

`models.MusicbrainzEntityCache` is keyed by `(entity, mb_id)`, not by MBID alone. The same artist
ID is both an `artist` lookup and a `discography` browse, with unrelated payload shapes and
unrelated refresh costs. `Payload` is the raw JSON the lookup returned, so caching another
endpoint is a new constant rather than a schema change.

Reads go through an in-memory front warmed at startup (`modules.LoadAllCaches`); writes go
through to the database immediately. Write-through rather than batched, because these are single
rows fetched seconds apart — not the thousands-per-scan churn that made the old release JSON file
expensive to rewrite.

A payload that no longer decodes into the caller's type counts as a **miss**, so a response shape
that changed between versions self-heals on the next fetch instead of surfacing as an error.

### Artwork

Image bytes stay on disk. The database row carries only what the filesystem cannot express: when
the image was fetched, and whether the providers said there is no image at all.

Images have the longest TTL of anything here, which is the right shape for data that is expensive
to transfer and effectively immutable. It is not *infinite* — which is what it was before the
index existed — because the Cover Art Archive gets better over time: a release with no art, or
with a poor scan, eventually gets a proper one, and without an expiry that improvement would
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
| `ArtistScope` | one artist, their discography, editions and releases | **yes** |
| `DueScope` | only releases whose TTL has elapsed | no |

`CollectionScope` takes its release set from `collection.AllMBIDs`, which unions the **file index**
with the owned-editions table. Reading only `collection_releases` is what made a collection
refresh quietly skip releases that files on disk actually point at.

`Force` (ignore the TTL) is what asking by hand means. A user who suspects a release is wrong is
not helped by "it was checked recently, come back in a week". Scheduled passes leave it off and
re-read only what expired.

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
- **It yields.** While a library scan is running, the pass pauses and re-checks every 15 seconds.
  Both draw on the same one-request-per-second budget, and the scan is the one with a user
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

## Configuration

| Key | Default | Meaning |
| --- | --- | --- |
| `autotaggerr_mirror_disabled` | `false` | Turns the scheduled pass off entirely. |
| `autotaggerr_mirror_cron_schedule` | `0 0 3 * * *` | When the pass runs (nightly at 03:00). |
| `autotaggerr_mirror_on_start_up` | `false` | Run a pass at startup too. |

The key is `disabled` rather than `enabled` for the same reason the migration review flags are
phrased as opt-ins: a bool absent from an existing `config.json` decodes as `false`, and `false`
has to mean the default behaviour.

Off by default at startup because a first pass over a large collection is hours of rate-limited
fetching, and tying that to every restart would make restarting something to avoid. The nightly
cron is what gets a new install mirrored.

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
the same way `LibraryScope` and `ArtistScope` both emit `scan`.

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

## How the scan uses this

A scan reads files through the *cache*, so without a refresh stage it would tag from a week-old
copy and never notice a release that changed upstream. `scan.Runner.Run` therefore:

1. runs `RunInline` over `DueScope` — expired releases only, so a nightly scan does not re-fetch
   the collection;
2. walks and tags as before;
3. **re-tags the files of every release the refresh reported as changed.**

Step 3 is necessary because `components.shouldSkip` drops files whose size and mtime are
unchanged — which is every file of a release that changed only upstream. The walk cannot catch
them by construction. This is the one place drift turns into file writes, and it lives in the scan
because writing files is the scan's job.

`RunInline` deliberately skips the pass guard and the Activity event: the scan already holds the
file-writing guard and reports under its own event, and a scan blocked by a scheduled refresh it
could happily run alongside would be absurd.

## Concurrency

Mutual exclusion is keyed on **whether a job writes files**, not on which runner owns it:

- file-writing work (scan, tag) — exclusive, one at a time
- metadata-only work (refresh) — runs alongside, and yields at entity boundaries when
  file-writing work wants in

Without this, a collection-wide refresh — hours long — would hold a single shared guard and reject
a user's per-artist scan for that entire time. A long background job must not starve short
interactive ones.

## UI

The **Metadata** page (`/mirror`) is built around making a long job legible rather than around
starting one: the phase, a progress meter, and the fetched / already-cached / failed / changed-
upstream counters, with per-entity cache coverage below. It polls every three seconds while a pass
runs. The trigger is a secondary action — the nightly schedule is what normally does this — and
"Stop" needs no confirmation, because stopping costs nothing.

The **no-writes contract is stated on the page**, not left to this document. It is the difference
between this verb and a scan, and someone deciding whether to press a button needs to know it
without reading `docs/`. The artist page's three buttons carry the same information in their
tooltips: which of them write tags, and what *Refresh metadata* hands off to.

## Interaction with migrations

MBIDs are mutable: MusicBrainz merges and deletes entities. When a migration is applied, the
cache entries keyed on the retired ID are dropped — a release via `modules.DropCachedRelease`, an
artist via `modules.MusicbrainzForgetEntity` for *both* the `artist` and `discography` entries,
since two things are keyed on an artist MBID. Left in place they would expire and be re-fetched
on every pass, spending rate limit to re-learn a redirect already dealt with. See
[mb-migration.md](mb-migration.md).
