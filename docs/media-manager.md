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

## Infrastructure

**Database.** SQLite via GORM, dialector pluggable (postgres/mysql later) chosen by
`config.database.type`. The default driver is `glebarez/sqlite` — pure Go, so the build stays
CGO-free for multi-arch releases. Domain models embed a shared `Base` with a **UUIDv7** primary key
assigned in a `BeforeCreate` hook; sqlite has no server-side UUID default, so generating it in Go
keeps the scheme portable. Caches keep their natural key (the MB ID) instead.

> **GORM footgun, guarded by a test:** never put `default:true` on a bool column. GORM omits a Go
> zero value from the INSERT, so a user-chosen `false` is silently overridden by the default.
> Callers set bools explicitly.

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
