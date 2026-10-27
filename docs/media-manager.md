# Feature: the standalone media manager

Autotaggerr began as a stateless, Lidarr-dependent tagger. It is now a media *knowledge* manager
that can either keep leaning on Lidarr exactly as before **or** stand on its own. This document
covers the component model and the infrastructure underneath it; the individual features have their
own docs (see [Related](#related)).

**Explicitly out of scope: acquisition.** There are no indexers, download clients, or release
grabbing, and none are planned. Autotaggerr enriches files that already exist. Lidarr manages
*acquisition* at album granularity; Autotaggerr manages *knowledge* at whatever granularity the
files actually have. That difference — not feature parity — is the point.

## Component model

All four are user-configured DB rows, edited through the API/UI.

- **Manager** — the correlation authority: it decides which MusicBrainz release/track a file maps
  to, and owns library state. `lidarr` reads Lidarr's decision (the original behaviour);
  `autotaggerr` derives it natively from embedded tags and manual pins. Decisions are persisted and
  never silently re-derived.
- **Data source** — an external provider. `musicbrainz` is used by *both* managers for the tag
  payload, since even Lidarr mode fetches MB directly.
- **Library** — a folder plus which manager and tagger profile govern it.
- **Tagger profile** — the tag-writing settings. One built-in engine consumes it, kept first-class
  so Plex/Jellyfin dialects and an NFO-sidecar writer can be added as siblings.

### Data sources have three roles, and they are not interchangeable

The four provider types share one table because they share every field — URL, credential, rate
limit, enabled, health — and the same health-check code. What they do *not* share is where they may
be used, so `models.DataSourceCategory` maps type → role:

| Category | Types | Used for |
|----------|-------|----------|
| `metadata` | `musicbrainz` | the tag payload, for every library at once |
| `fingerprint` | `acoustid` | suggesting an identity for an unmatched file ([fingerprinting.md](fingerprinting.md)) |
| `artwork` | `coverartarchive`, `fanart` | covers and artist images on the browsing pages |

Treating all four as one interchangeable list is what made the model confusing: a library's data
source select offered AcoustID and fanart.tv, which were then accepted and silently ignored, because
nothing but release metadata is ever read from that field. `checkLibraryDataSource` still rejects a
non-metadata source with a 400 on both create and update (`dataSourceCategory` in
`webui/src/types.ts` mirrors the Go map — keep the two in step).

### MusicBrainz is one client

There is one MusicBrainz client for the whole process, and one `musicbrainz` row drives it: the
**oldest enabled** one (`components.MusicBrainzSourceInUse`). Its base URL, contact and request rate
are pushed into `modules` by `components.ApplyMusicBrainzSource` — at startup, and whenever a
MusicBrainz source is created, edited or deleted, so a change applies without a restart. A blank URL
is the public service, a blank contact sends the project URL in the User-Agent, and a non-positive
rate is 1 req/s. With no source enabled the client goes back to all three defaults rather than
keeping the last values, so disabling a mirror stops it being asked. The endpoint sits behind an
atomic pointer, since an edit swaps it while workers are mid-scan; nothing cached is dropped, as a
mirror serves the same IDs with the same data.

"Oldest" is the rule because with a local mirror beside the public service exactly one is read, and
it has to be the same one on every start. The Data sources page marks it *In use* (the list carries
`in_use`, so the SPA never re-derives the rule) and any other enabled row *Not used*. The URL and
contact were editable and stored for a long time without being read — the page invited pointing a
source at a mirror that was never asked — and the form offered no rate or contact field at all.

Three fields
once suggested otherwise — `Library.DataSourceID`, `Manager.DefaultDataSourceID` and `Library.Cron`
(every library runs on the one processing schedule) — and were stored without ever being read. The UI
no longer offers them and the MCP library list no longer reports one; the API still accepts them so
an older client does not fail. Honouring them would mean a MusicBrainz client and cache per source
and a scheduler per library — worth it only if a second metadata source is ever wanted for real.

**Singletons.** There is exactly one AcoustID service, one Cover Art Archive and one fanart.tv, and
only the first row of a type is ever looked up, so a second is dead config: `POST /data-sources`
returns 409 for a duplicate of a singleton type (`models.DataSourceIsSingleton`). MusicBrainz is
excluded — a local mirror alongside the public service is legitimate. Seeding was already
idempotent per type, so it agrees with the rule.

The **Data sources page** is shaped around this: a *Metadata sources* table you can add rows to, plus
one panel per other role listing its fixed provider(s) as configured-or-not. Rows that predate the
duplicate check still show, marked as unused duplicates with a Remove action, so nothing is hidden
behind a panel that renders only the first match.

### Manager resolution is strict on purpose

A library with an **explicitly assigned** manager gets that manager. If that manager is disabled or
has been deleted, the library fails with a **hard error** rather than falling back to something
else. The manager decides which MusicBrainz IDs are written into your files, so swapping it behind
your back would change the tags themselves. The run records the skipped library
([scanning.md](scanning.md#warnings-and-skipped-libraries)), and the Libraries table marks it.

### A library without a manager

**No manager means no manager.** A library with none assigned is not handed whichever manager is
enabled. Its files are identified only from the MusicBrainz IDs already in their tags, which is all
there is to go on without a manager. That is a legitimate setup for a library tagged with Picard,
and a costly one for a Lidarr library, because Lidarr does not write those IDs into MP3s.

It is also where a fresh install ends up without anyone choosing it: `database.Seed` creates no
manager. So the choice is allowed, but it is never silent:

- **Every *Process* run and disk-walking *Scan* records a warning** for each such library
  (`components.NoManagerWarning`). The run goes ahead and its status stays `ok`, but the feed reads
  *Done, with warnings*. The single-file `--file` path logs the same warning.
- **The Libraries table shows a *No manager* pill**, plus *Manager missing* or *Disabled* for the two
  states that make a library fail. The edit form names the option *No manager* and shows the cost
  below it whenever it is selected.
- **The form can actually choose it.** `PUT /libraries/:id` treats `"manager_id": null` as "clear"
  and an absent `manager_id` as "unchanged" (`routers.clearableUUID`). The form sends null. Before
  this, choosing the empty option while editing kept the old manager. The library's
  `data_source_id` works the same way, so *Default* is an edit that sticks, and so does a manager's
  `default_data_source_id`.

**A library always names its tagger profile.** There used to be a *Default* option that stored
none, and a library without one was tagged by the first profile by id — next to the seeded profile,
which is also called *Default*, so the dropdown offered two entries with one name and the first of
them meant whichever profile sorted first. Now:

- **The dropdown lists the profiles and nothing else.** A new library preselects the first by id,
  the oldest (IDs are UUIDv7) — the seeded one on most installs.
- **`POST /libraries` without a profile is given that same one**, so an older client or a script
  still gets a library that says what tags it. `"tagger_profile_id": null` on an edit is a 400, and an
  unknown ID on either is a 400.
- **A profile a library uses, and the last profile, cannot be deleted** (409, naming the libraries),
  so the reference cannot dangle again.
- **Upgrades keep behaviour**: the `pin_fallback_tagger_profile` data migration gives each library
  naming none, or a deleted profile, the profile it was already tagged by — as
  `pin_fallback_manager` did for managers. `resolveTaggerProfile` keeps its first-by-id fallback
  only as a guard; nothing should reach it.

Internally the library resolves to a manager row with no ID, of the native type. Native is the type
because the resolution is the same, embedded tags only, and because it is what files correlated
this way have always recorded. Assigning a native manager later therefore does not re-correlate a
whole library just to reach the same answer. The collection agrees: it has always treated a library
without a manager as native. Before this change, the tagger and the collection disagreed whenever a
Lidarr manager was enabled.

**Upgrading.** Until this change, such a library silently got the first enabled manager (by row id),
and the form called that option *Default (first configured manager)*. Dropping the fallback in place
would have re-correlated every such library from embedded tags, and stripped Lidarr's MP3s of their
identity. So a one-time data migration, `pin_fallback_manager` (`database/migrate.go`), assigns each
such library the manager it was already using. It is recorded in `data_migrations`, so it never runs
again: a library set to *No manager* afterwards is a choice the migration does not undo. On a
database with no enabled manager it assigns nothing, and it is still recorded.

Guarded by `TestResolveManagerRowRejectsDisabled`, `…RejectsDanglingManager`,
`…NoManagerIsNoManager`, `TestPinFallbackManagerRunsOnce`, `…WithoutManagers`,
`TestLibraryManagerCanBeCleared`, and the warning assertion in `TestRunnerRunAll`.

## Pipeline

`ProcessTrackFile` is split into two halves, which is what let managers become pluggable:

- `modules.ResolveCorrelation` — file → (release, track, recording) MBIDs.
- `modules.TagResolvedFile` — correlation → tags on disk.

`components.ProcessFile` runs the pair and persists the result to `library_items`, the owned
correlation index. `components.ScanLibrary` reuses `modules.WalkAndProcess` for orchestration, and
`processLibraries` iterates the enabled DB libraries.

`LibraryItem.Pinned` marks a manual correlation. The pipeline never lets automatic resolution
downgrade a pin (`components/pipeline.go`), which is what makes manual attach durable: `ProcessFile`
reuses a pinned correlation instead of asking the manager for one, and `recordItem` refuses to write
MB IDs or a source over a pinned row. Both halves are needed — guarding only the index row still let
a re-processed file get *tagged* to the manager's answer while the row claimed `manual`.

See [scanning.md](scanning.md) for skip-unchanged, drift sync and performance.

### A failed Lidarr lookup says which step failed

The error a file carries is not a label, it is the whole diagnosis: `recordItem` stores
`procErr.Error()` on the `library_items` row, and that string is what the Items page and the Activity
detail list render. Anything a wrapper drops is therefore unrecoverable from the UI — which is what
made `failed to retrieve track details from Lidarr for '<path>'` useless, since one sentence stood in
for a rejected cookie, a proxy login page, a folder Lidarr spells differently and a path that does
not fit the layout convention.

So every step of `ResolveMetadataDetailsFromLidarr` annotates its own failure (which artist, which
Lidarr ID, which album), `ResolveCorrelation` wraps rather than replaces, and `LidarrClient.getJSON`
answers the question a decoder cannot:

- **Where the request ended up.** A redirect is named, because Go strips the `Cookie` header when one
  crosses to another hostname — the reason a *valid* Authelia session can still never reach Lidarr,
  and a failure that is otherwise indistinguishable from a bad cookie.
- **What came back instead of JSON.** Content type plus the first bytes of the body, so an
  authentication portal answering `200 text/html` reads as a portal rather than as
  `invalid character '<' looking for beginning of value`.
- **Whether credentials were sent at all**, on 401/403.

`ErrLidarrArtistNotFound` is a sentinel because it is the one Lidarr failure that is about the
*library*, not about Lidarr. It is now the last word rather than the first: it is returned only when
neither the artist folder nor the file's own tags name a Lidarr artist (see below), and the message
says both were tried.

### Matching does not trust where a file sits

The path used to be read as `<root>/<ARTIST>/<ALBUM>/[<MEDIA>]/<TRACK>` twice over — the artist
folder to find the Lidarr artist, the album and disc folders to find the track file — so a file
under a misspelled artist folder, a level too deep or too shallow, or below a library root set one
level above Lidarr's (a folder per genre, each its own Lidarr root folder) was unmatched although
Lidarr has a track file at exactly that path. Both halves now degrade instead of failing
(`modules/lidarr_match.go`):

- **The track file is found by the file's path below the library root**, compared from the end of
  Lidarr's stored path, by whole segments, as `Canon` compares, with either separator
  (`trackFileByRelativePath`). It is the same file at the same place below the root, which
  identifies it at any depth and under any names, and needs no path mapping. Two track files
  ending with it — only possible for a file directly in the root — is no match, as before. The
  album/disc/name match stays behind it for a root that does not line up with Lidarr's, and a file
  too shallow for that is a miss rather than an error.
- **The artist is found by the folder first** — the cheap answer, right for every file Lidarr placed
  — and when that names no Lidarr artist, or one whose track files do not hold this file, by the
  file's own tags (`lidarrArtistsFromTags`): the MusicBrainz album-artist and artist IDs Lidarr
  keys its artists by (`MUSICBRAINZ_ALBUMARTISTID` in Vorbis, `MusicBrainz Album Artist Id` in
  ID3), then the album-artist and artist names against Lidarr's names and folder names, loosely.
  Artists already tried are skipped. The tags are read only on that miss, so a library Lidarr laid
  out costs nothing more.

A file still unmatched after all of that has no track file in Lidarr at that path, so where it sits
is no longer the reason, and the index says what is: an unmatched row keeps `error` set to the
reason (`modules.UnmatchedReason`) — Lidarr never imported it (its *Manual Import*), or it moved
since Lidarr last looked (refresh the artist). Only error rows read `error` as a failure; the Items
page shows the reason under an unmatched row in the muted colour. Both fixes are Lidarr's, since it
owns identity there; in a native library an unmatched file is attached by hand or imported. In a native
library, [Organise](naming.md#organise) moves misplaced files to where their profile names them.

Guarded by `TestLidarrClientReportsLoginPage`, `…ReportsCrossHostRedirect`,
`TestLidarrFindArtistByNameNotFound` and `TestResolveCorrelationKeepsLidarrCause`.

### One copy of the credentials, and one way to check them

A manager's credentials live on the **manager row**, and nowhere else. The row is created and edited
through `PUT /managers/:id` and the Managers page.

`config.json` used to hold a second copy in three `lidarr_*` keys, which seeded the row on the first
boot and were ignored on every boot after. That was a trap: an expired session makes editing
`lidarr_header_cookie` in the file look exactly like fixing it, while the manager keeps using the
stale value. It was mitigated for a while with a startup warning naming any key whose value diverged
from the manager's — but a key that exists, is documented, and does nothing is a trap whether or not
it is announced. The keys are gone from `models.ConfigStruct` entirely, so a stale copy in an old
`config.json` is inert and can be deleted.

Nothing remains of that seed. The last piece to go was `autotaggerr_libraries`, which created a
library row per configured folder and linked it to whichever Lidarr manager already existed — the
same read-once-then-ignored shape, one level up. `database.Seed` now takes no config at all: it
creates the two credential-free data sources, a default tagger profile and the admin user, and
stops. The cost is that a fresh install starts with no manager and no library, and the user makes
both in the UI; the gain is that there is exactly one place each of those lives, rather than two
where one silently wins.

Plex went the same way. Its two keys were the last connection still in `config.json`; Plex servers
are now media server rows, any number of them, linked per library
([media-servers.md](media-servers.md)). The keys an older file carries are turned into a row once
and then dropped.

Nothing else may build a Lidarr client. `main.go` used to build one from `files.ConfigFile` for the
health check — a second copy that diverged from the row the first time either side was edited alone,
and whose failure mode was the worst available: a green *Lidarr healthy* beside a library where every
file failed to resolve, because the two were authenticating with different cookies. `health.Checker`
now reads the enabled manager rows on **every run** and probes them through `components.NewManager`,
the same constructor the pipeline calls. Per run, not at boot, so a credential pasted into the UI is
checked on the next tick rather than the next restart — and so a manager added later is picked up at
all, which is why `NewChecker` no longer returns nil for "nothing configured".

`POST /managers/:id/test` is the same probe on demand, behind the **Test** button on each row. It
answers 200 whether or not the connection works: a rejected probe is a successful test, and a non-2xx
would make the UI report a failure of the test rather than of the credentials. The response carries
`api_key_set` and `cookie_set` — never the secrets — because the most expensive failure to diagnose
is not a wrong credential but an absent one. `Manager.HealthCheck` exists on both manager types and
this is what finally calls it.

Guarded by `TestCheckerProbesManagerRows`, `TestNewCheckerWithNothingToProbe` and
`TestManagerTestConnection`.

## Infrastructure

**Database.** SQLite via GORM, dialector pluggable (postgres/mysql later) chosen by
`config.database.type`. The default driver is `glebarez/sqlite` — pure Go, so the build stays
CGO-free for multi-arch releases. Domain models embed a shared `Base` with a **UUIDv7** primary key
assigned in a `BeforeCreate` hook; sqlite has no server-side UUID default, so generating it in Go
keeps the scheme portable. Caches keep their natural key (the MB ID) instead.

> **GORM footgun, guarded by a test:** never put `default:true` on a bool column. GORM omits a Go
> zero value from the INSERT, so a user-chosen `false` is silently overridden by the default.
> Callers set bools explicitly.

**SQLite is configured for a writer that runs for minutes.** `database.Connect` appends pragmas to a
bare DSN — `journal_mode(WAL)`, `busy_timeout(10000)`, `synchronous(NORMAL)` — and caps the pool at
`sqliteMaxConns`. This is not tuning; it is a fix. In the default rollback-journal mode a *committing*
writer takes an EXCLUSIVE lock and readers wait it out, and this app's write pattern is a Lidarr sync
committing per album and ending in one large `collection.Rebuild` transaction. Against that, the UI
polling `/process/status` and `/events` — plus the users-table read every authenticated request does —
met a near-continuous run of commit windows, until one exceeded the driver's own 5s busy timeout
(`glebarez/go-sqlite` runs `pragma BUSY_TIMEOUT(5000)` on every connection) and the request failed.
Under WAL a reader never waits for a writer at all. The pool cap follows from SQLite serialising
writers regardless: extra connections buy no write concurrency, only more contenders for one lock.

**Transactions open as `BEGIN IMMEDIATE`** (`_txlock=immediate`, `database.sqliteTxLock`). WAL fixes
readers-versus-writers; this fixes a writer against itself. A transaction that reads before it writes
starts as a read snapshot and has to upgrade on its first write, and SQLite refuses that upgrade
outright — `SQLITE_BUSY_SNAPSHOT`, no waiting, `busy_timeout` inapplicable — if anyone committed in
between. Taking the write lock at `BEGIN` removes the upgrade, so contention becomes a wait instead
of a failure. See [collection.md](collection.md#a-rebuild-that-loses-a-write-race-takes-the-lock-first-and-is-retried-second)
for the failure that produced it. The cost is that a read-only explicit transaction would take a lock
it does not need; all three in the codebase read then write, and plain reads open no transaction.

Two deliberate details. A DSN that already has a query string is left untouched — it is the escape
hatch, and appending to it would break it in the one case someone used it. And if opening with WAL
fails, `Connect` **falls back** to the bare DSN with a warning rather than refusing to start: WAL
needs shared memory, which network mounts and some Docker volume drivers do not provide, and that is
a supported way to run this. Because the pragma can also be accepted without taking effect, `Connect`
reads `PRAGMA journal_mode` back and warns when it is not WAL — otherwise the app would run with
exactly the contention this removes and nothing would say so.

**Config split.** Bootstrap config (DB type/DSN, port, `private_key`, log level, timezone) stays in
`config.json`/env. All domain config lives in the DB. First run seeds it idempotently from the
existing `config.json` — MusicBrainz data source, Lidarr manager, libraries, default tagger profile
— and creates one admin user, so existing Lidarr installs are unchanged by the upgrade.

**API-first + embedded SPA.** A JSON API under `/api/v1`, and a Vite + React + TS SPA in `webui/`
built to `web/dist` and embedded with `go:embed` (`web/embed.go`). One binary; client routes fall
back to `index.html`; `/api` is never hijacked. CI builds the UI before the Go steps, and
`web/dist` is committed so `go build` needs no Node. Authentication is in
[authentication.md](authentication.md).

**Design system first.** [style-guide.md](style-guide.md) is the living design system —
utilitarian *arr-style, dark-only, violet/indigo accent, compact, with the monospace **tag-diff** as
the signature element. The standing rule (see [development.md](development.md)): every UI decision
consults the guide and either follows it or reshapes it *in the same change*.

## Milestone history

| # | Milestone | Notes |
|---|-----------|-------|
| M0 | DB + config split + seed | GORM + pure-Go sqlite in `database/`, models in `models/db.go`, idempotent first-run seed. No pipeline behaviour change. |
| M1 | Component pipeline | The `ResolveCorrelation`/`TagResolvedFile` split, the `components/` package, skip-unchanged scanning, MB release cache moved into the DB. |
| M2 | API + auth | `auth/` package, `/api/v1` CRUD, `library-items` browse, scan control behind a shared `process.Runner`. |
| M3 | Style guide + SPA | The design system, the embedded SPA, the tag-diff detail view, edit forms, first-run onboarding. |
| M4 | Drift sync | Catching upstream MusicBrainz changes — see [scanning.md](scanning.md). |
| M5 | Present vs wanted | The collection — see [collection.md](collection.md). |
| M6 | Native manager | Manual attach, collection authoring, per-edition ownership, AcoustID, and pass E — file import ([import.md](import.md)). |
| — | OAuth/OIDC | Shipped ahead of schedule, standalone — see [authentication.md](authentication.md). |

## Related

- [scanning.md](scanning.md) — scans, drift sync, activity events, performance.
- [collection.md](collection.md) — present vs wanted, the desire model.
- [attach.md](attach.md) — identifying files by hand.
- [fingerprinting.md](fingerprinting.md) — optional AcoustID identification.
- [tagging.md](tagging.md) — what gets written to a file.
- [naming.md](naming.md) — where a file belongs: the tagger profile's naming templates.
- [import.md](import.md) — bringing loose files into a library.
- [downloads.md](downloads.md) — finished downloads from a download client, through the import.
- [authentication.md](authentication.md) — local login, API keys, OIDC.
