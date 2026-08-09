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
- **Library** — a folder plus which manager, data source and tagger profile govern it.
- **Tagger profile** — the tag-writing settings. One built-in engine consumes it, kept first-class
  so Plex/Jellyfin dialects and an NFO-sidecar writer can be added as siblings.

### Data sources have three roles, and they are not interchangeable

The four provider types share one table because they share every field — URL, credential, rate
limit, enabled, health — and the same health-check code. What they do *not* share is where they may
be used, so `models.DataSourceCategory` maps type → role:

| Category | Types | Used for |
|----------|-------|----------|
| `metadata` | `musicbrainz` | the tag payload; the only kind a library may point at |
| `fingerprint` | `acoustid` | suggesting an identity for an unmatched file ([fingerprinting.md](fingerprinting.md)) |
| `artwork` | `coverartarchive`, `fanart` | covers and artist images on the browsing pages |

Treating all four as one interchangeable list is what made the model confusing: a library's data
source select offered AcoustID and fanart.tv, which were then accepted and silently ignored, because
nothing but release metadata is ever read from that field. `checkLibraryDataSource` now rejects a
non-metadata source with a 400 on both create and update, and the SPA filters the select to match
(`dataSourceCategory` in `webui/src/types.ts` mirrors the Go map — keep the two in step).

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

A library with an **explicitly assigned** manager gets that manager or a **hard error** — never a
silent fallback — when the row is disabled or gone. The manager decides which MB IDs are written
into your files, so swapping it behind your back changes the tags themselves. A library with **no**
manager assigned keeps the permissive path but only ever resolves to an *enabled* manager, falling
back to the native default.

Guarded by `TestResolveManagerRowRejectsDisabled`, `…RejectsDanglingManager`,
`…SkipsDisabledFallback`.

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
*library*, not about Lidarr: the match is folder-to-folder (our artist directory against the last
segment of Lidarr's stored path), never name-to-name, so "Lidarr obviously has this artist" and "no
match" are entirely compatible. The message names both sides of that comparison.

Guarded by `TestLidarrClientReportsLoginPage`, `…ReportsCrossHostRedirect`,
`TestLidarrFindArtistByNameNotFound` and `TestResolveCorrelationKeepsLidarrCause`.

### One copy of the credentials, and one way to check them

A manager's credentials live on the **manager row**. `config.json` seeds that row once
(`database/seed.go:146` returns early the moment a Lidarr manager exists) and is never read for them
again, so editing `lidarr_header_cookie` there after first run changes nothing the pipeline sees.
The row is edited through `PUT /managers/:id` and the Managers page, which is why that page says so
in as many words.

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
| M2 | API + auth | `auth/` package, `/api/v1` CRUD, `library-items` browse, scan control behind a shared `scan.Runner`. |
| M3 | Style guide + SPA | The design system, the embedded SPA, the tag-diff detail view, edit forms, first-run onboarding. |
| M4 | Drift sync | Catching upstream MusicBrainz changes — see [scanning.md](scanning.md). |
| M5 | Present vs wanted | The collection — see [collection.md](collection.md). |
| M6 | Native manager | Manual attach, collection authoring, per-edition ownership, AcoustID. Pass E (file import) is still open; see [wip.md](wip.md). |
| — | OAuth/OIDC | Shipped ahead of schedule, standalone — see [authentication.md](authentication.md). |

## Related

- [scanning.md](scanning.md) — scans, drift sync, activity events, performance.
- [collection.md](collection.md) — present vs wanted, the desire model.
- [attach.md](attach.md) — identifying files by hand.
- [fingerprinting.md](fingerprinting.md) — optional AcoustID identification.
- [tagging.md](tagging.md) — what gets written to a file.
- [authentication.md](authentication.md) — local login, API keys, OIDC.
