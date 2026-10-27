# Feature: storage overview

What takes space, and where: the music in each library, Autotaggerr's own data under `config/`,
and the free space of the disks they sit on. A **Storage** card on the overview answers "is a disk
running out"; the **Storage** page (`/storage`, under System) has the breakdown and the actions
that shrink what can be shrunk. The `storage` package measures; `GET /storage` serves it.

## Two scales, kept apart

Music is hundreds of gigabytes to terabytes. Autotaggerr's own data is megabytes. Drawn on one
scale the second is invisible, and it is the one that grows without anyone noticing — an unrotated
log, a write-ahead log larger than the database, Activity detail on a large library. So the page
reports them separately: **your music** by library, artist and album; **Autotaggerr's data** by
part. Only the per-disk bar puts them side by side, and there each is drawn at least 3px wide when
it exists — a small library or the app's data on a terabyte disk would otherwise be no pixels, and a
segment that is not drawn says there is nothing.

## What is measured, and from where

| Figure | Source | Notes |
|---|---|---|
| Music per library, per format, unidentified | `library_items.size` | As of each file's last scan — one query, not a walk of the disk. FLAC/MP3/other by extension. |
| Largest artists and albums | items joined to `collection_releases` | Ranked by the **release-group's** artist, the same authority as the collection's pages. |
| Albums kept more than once | the same join, per *copy* | A copy is a release in a library: two editions, or one edition in two libraries. The figure is the size beyond the largest copy of each. Often deliberate, so it is information, not an action. |
| Disks | `statfs` / `GetDiskFreeSpaceEx` (`storage/disk_*.go`) | Libraries and `config/` on one disk are one row. Free is what an unprivileged process can use. |
| Database | `pragma_database_list`, `dbstat`, `freelist_count` | The file, its WAL and SHM, the free pages inside it, and every table with its indexes, grouped by what they hold (`tableGroup`). SQLite only. |
| Artwork | the files in the artwork directory, and `artwork_cache_entries` | By kind (album covers per size, artist portraits, backdrops), how many lookups are remembered as having no image, and leftovers (below). |
| Logs | `logger.LogFiles()` | The log and its rotated files. |
| Other files | the top level of `config/` | `config.json` and anything left there — a hand-made database backup shows here. |

**A measurement is cached for ten minutes** (`storage.CacheFor`). Sizing the database by table
reads every page of it, and nothing here changes by the minute. *Measure again* forces one and is
an admin's: it is work any signed-in user should not be able to make the server repeat. Every
action drops the cache, so its result shows at once.

## Who sees what

Everyone sees the disks and the music. **The breakdown of `config/` and the actions are an
admin's**, like Settings: paths, database internals, and buttons that delete. A non-admin's report
has no `app` part, and a disk holding only `config/` keeps its total of Autotaggerr's data but not
its path.

## Leftover artwork

An artwork file with no index row is **not** a leftover: the cache adopts those
(`modules.readArtworkCache`), because installs that cached images before the index existed would
otherwise download every cover again. Nor is an image for something no longer in the collection:
discography browsing fetches covers for albums the collection does not store, and calling those
waste would be wrong. A leftover is only what nothing will serve — an interrupted download
(`.tmp`), or an image for a key the index now records as having none.

Leftovers younger than an hour are neither counted nor removed (`storage.LeftoverAge`): an image is
written to disk before its index row, so a young file may be one being fetched.

## The actions

Each sits beside the figure it changes, and all are `POST /storage/*`, admin-only:

- **Compact** (`compact`) — `VACUUM`, then a WAL checkpoint so the saving shows in the file. Offered
  only when there are free pages. Refused when the disk has less free than the database is large
  (it writes a full copy as it goes; failing halfway for want of space is worse than not compacting)
  and while a job is running (it holds the database for its whole duration). Behind a confirmation
  that says both.
- **Shrink** (`checkpoint`) — `wal_checkpoint(TRUNCATE)`. SQLite checkpoints on its own, but only
  partway while anything reads, so on a busy install the log can sit larger than the database. A
  checkpoint that could not finish says so rather than claiming the space.
- **Prune now** (`prune-activity`) — the configured Activity retention, applied now rather than when
  the next run finishes (`events.Prune`). The rows go at once; the file does not shrink until it is
  compacted, because SQLite keeps freed pages for reuse, and the message says so.
- **Remove** (`artwork-leftovers`) — deletes the leftovers above.

## The log is rotated

`config/autotaggerr.log` used to be appended to forever. It is now capped (`logger/rotate.go`): past
`autotaggerr_log_max_size_mb` (default 20) it is renamed to `autotaggerr.log.1`, shifting older ones
down, and `autotaggerr_log_keep` (default 3) rotated files are kept — at most 80 MB by default. A
write that would cross the cap rotates first, so no line is split across files. Both settings are on
**Settings → Logs & Activity** and apply live: `logger.SetRotation` swaps the limits under the
writer's mutex, and lowering the count deletes the extra files at once.

## Tests

- `storage/storage_test.go` — against a real SQLite file and temp directories: library totals,
  formats (case-insensitive), unidentified files, the largest lists by the release-group's credit,
  a duplicate's space beyond its largest copy, one volume for two libraries and `config/` on one
  disk; the database by group with SQLite's own tables left out; artwork kinds, with only *old*
  leftovers counted; the rest of `config/` without the database, WAL or logs; the cache and its
  invalidation; and each action, including that a served image survives the leftover removal.
- `routers/storage_test.go` — the role split (no `app`, no config path, for a non-admin) and the
  actions' admin gate.
- `logger/rotate_test.go` — rotation at the cap without splitting a line, the shift, the keep
  count, and a lowered keep applied at once.

## Related

- [settings.md](settings.md) — the log rotation settings, and the Activity retention the prune uses.
- [artwork.md](artwork.md) — what the artwork cache holds and why.
- [scanning.md](scanning.md) — where `library_items.size` comes from.
