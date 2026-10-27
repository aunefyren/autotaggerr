# Feature: importing from a download client

Finished downloads offered for import, without typing a path. A **download client** (qBittorrent,
Transmission or SABnzbd) is watched for finished downloads in the categories mapped to a library, and each one is
listed on **Import → Downloads**. Opening one runs the same identify-and-review modal as a folder
import ([import.md](import.md)). Nothing is imported without that review, unless the category is
set to [import automatically](#auto-import) and the album's own tags leave nothing to decide.

## How this differs from Lidarr's

Lidarr knows what a download is before it starts, because it grabbed it: it searched an indexer for
an album it wanted, sent the torrent to the client under its own category, and stored the hash
against the album. Its import is mostly a check against what it already expected.

Autotaggerr grabs nothing. The client is only a **source and a trigger**: it says which folder holds
a finished download and when it is ready. Identification is still the files' own business, by the
import's hints and the release ID in the tags. What Lidarr also does and this does not, deliberately:
search indexers, unpack archives (Lidarr leaves that to Unpackerr too), hardlink, import extra files.
Removing a torrent after seeding it does do, but only when asked — see
[once every album is imported](#once-every-album-is-imported).

## Download clients

Configured on the **Download clients** page (admin-only, like Import: a row names where files are
read from and what library they go into). The table is `download_clients`. It is not a data source,
because a data source answers questions about music and shares nothing else with a client that has
a login, watched categories and path mappings.

- **Type**: `qbittorrent`, `transmission` or `sabnzbd` (`models.DownloadClientTypes`), fixed once
  saved — a different type is a different client, and its categories, mappings and history would
  not carry over. `models.DownloadClientSeeds` tells the torrent clients, which hold a download's
  files after it finishes, from the usenet one, which does not.
- **Base URL, username, password.** Both credentials may be empty for a qBittorrent that bypasses
  authentication for this host, or a Transmission whose RPC asks for none. SABnzbd has no login:
  its API key is stored in the password field, required, and the form calls it that. The password
  is write-only (`json:"-"`); the API returns `password_set` instead. An edit that omits it keeps
  the stored one.
- **Watched categories** (`download_client_categories`), each mapped to one library. Only mapped
  categories are ever read, so a Lidarr using the same client under its own category is left alone.
  The target library must pass Import's gate, which means it is not Lidarr-managed. That is checked
  when the client is saved, not only when an import runs.
- **Path mappings**: the folder as the client reports it, and the same folder on this host, for a
  client in another container or on another machine. `modules.MapRemotePath` uses the longest
  matching remote prefix and compares whole segments, so `/downloads` does not rewrite
  `/downloads2`. Either separator is accepted, and a drive letter compares without case, since a
  Windows-hosted qBittorrent reports `D:\Torrents`. A path no mapping matches is used as reported.
- **Test** logs in and reads `/api/v2/app/version`, using the credentials the poller uses. Like a
  manager's test, it answers 200 either way. Enabled clients are also probed by `health.Checker`.

## The poller

`process.Runner.PollDownloads` runs on its own schedule
(`autotaggerr_download_poll_cron_schedule`, default every two minutes, on **Settings → Background jobs**),
and once more whenever someone presses **Check now**. Like the Lidarr follower, it runs on the cron
goroutine rather than the job queue. A poll is a few small requests and a few rows; only the imports
it leads to are queued jobs. A `TryLock` skips a poll that would overlap the previous one.

For each enabled client, it asks for the finished downloads in each watched category. qBittorrent is
asked once per category, because a seedbox can hold thousands of torrents. A torrent counts only in a
settled state: `uploading`, `stalledUP`, `pausedUP`/`stoppedUP`, `queuedUP` or `forcedUP`, with
progress 1. Anything checking, moving, allocating or missing files is left for the next poll.

Each download becomes a `download_items` row keyed by `(client, info-hash)`, so a download is offered
once however often it is polled. Its state is one of:

| State | Meaning |
|---|---|
| `ready` | its complete audio files are listed and readable here |
| `unavailable` | there is nothing here to import yet. `reason` says why: no complete FLAC/MP3, files not at the mapped path (with the remote folder a mapping would need), or a folder overlapping a library. Read again on every poll, so fixing a mapping clears it. |
| `imported` | every album in it was imported, and the client was told |
| `dismissed` | someone set it aside. **Restore** offers it again, read afresh. |

Further rules:

- **A download has to settle first.** It is listed only once it has been complete for
  `downloadSettle` (a minute), because qBittorrent moves a finished torrent out of its temporary
  folder and may recheck it.
- **The file list comes from the client, not the disk** (`/torrents/files`). A file with priority 0
  (deselected) or progress below 1 is never offered, and neither is a `.!qB` partial.
  `modules.GroupImportFiles` is the import scan's grouping, but over that list rather than a walk.
  For a single-file torrent, whose file sits in the category's folder, it reads the download's own
  name in place of a folder name that names nothing.
- **Tags are read only when someone opens a download** (`GET /import/downloads/:id/scan`), not
  every poll.
- **A failed read is recorded on the client row** (`last_poll_error`, shown on both pages), not in
  Activity. The health check already records an unreachable client.
- **A download removed from the client**, or moved to an unwatched category, is forgotten if nothing
  was imported from it. Otherwise it is kept as history, with `seeding` cleared.

## Importing one

**Import…** on a ready download opens its album straight into the modal. A download holding several
albums (a discography) lists them, and each is identified and imported on its own. Each album's
progress is kept on the row (`groups`, by album folder: `queued`, `imported` or `failed`).

- **Copy, always, while the client holds the files.** No hardlinks (tagging one rewrites the
  torrent's file) and no move, which would pull the file out from under the client. The review shows
  a warning in place of the Move/Copy choice: the album takes its space twice until the torrent is
  removed. `Runner.Import` refuses a move with `ErrDownloadMustCopy`, whoever asks. A SABnzbd
  download is held by nothing (`seeding` false), so its review offers Move as a folder import does.
- **An import that would not fit is refused before anything is copied** — when it is queued, and
  again when the job starts, since the queue may have run something else first
  ([import.md](import.md#free-space)). A seeding album needs its full size again, which is the case
  this exists for.
- **One album is queued at a time.** `POST /import` takes `download_id` and `download_folder`. The
  check (the folder is an album of this download, and it is not already queued or imported) and the
  queued mark happen under one lock, so a double-click cannot queue an album twice.
- **When the job ends, the album is settled.** It becomes `imported`, or `failed` if any file failed
  or the job was stopped. A failed album is offered again, and a second import plans around what the
  first one placed. When every album is imported, the download becomes `imported` and the client
  is told.
- **The one write to the client** is the `autotaggerr-imported` tag (`MarkImported`). It is also a
  second record: a torrent carrying it is listed as imported even by a database that has never seen
  it. If tagging fails, the download is still imported, and the failure is shown as its `reason`.
- **A restart forgets queued albums** (`process.ReconcileDownloads`, at startup). The queue does not
  survive a restart, so a surviving queued mark is a job that will never run.

The `file_import` event carries the download's name and hash.

## Once every album is imported

The imported tag is always written (torrent clients). Two more things a torrent client row can ask
for, both off unless set, and both refused for SABnzbd, whose history keeps nothing to move or remove:

- **Move to a category** (`imported_category`): the download is moved there beside the tag, out of
  the watched category — into one the client seeds or stores differently. It may not be a watched
  category itself. qBittorrent is asked to create it first (a 409 for one it has is not a failure);
  in Transmission it swaps the watched label for this one and keeps the rest. Moved out of a watched
  category, the download is history here, as any download that leaves one is.
- **Remove once it finishes seeding** (`remove_after_seeding`): the end of the copy's second space.
  After each poll, `Runner.removeFinishedSeeds` asks the client (`modules.SeedRemover`) about the
  downloads it imported and removes, files and all, those the client stopped *for reaching a share
  limit set there*. It is careful by construction, since these are files Autotaggerr did not put
  there:
  - only downloads imported here in full, every album (`Groups` all imported) — one known only by
    another database's tag is never touched;
  - only a limit, never a pause: qBittorrent's torrent has to be stopped *and* have reached its
    effective `max_ratio` or `max_seeding_time` (no limit, never done); Transmission's `isFinished`
    is set only by a seed ratio or idle limit;
  - looked up by ID, so a download moved to the post-import category is still found;
  - each removal is a `download_removed` Activity row, failures included, and the download keeps
    `removed_at` and says why.

## Auto-import

A watched category can be set to **Import automatically** (`auto_import` on its
`download_client_categories` row). It is off by default, so a row that predates the column keeps
the behaviour it always had, and it is turned off again by unticking it. With it on, an album is
imported **without a review only when its own tags already made every decision the review asks
for** (`process.autoImportPlan`):

1. **Every file names the same MusicBrainz release.** This is stricter than the import's hint
   (`agreedReleaseID`), which tolerates untagged files. Here, an untagged file is one nobody
   identified.
2. **The files are the whole release.** The file count equals the release's audio-track count
   (video tracks excluded, as the mapper does).
3. **Each file is a different track, decided by the file.** When the files carry release-track
   IDs, those decide, and they must name distinct audio tracks of this release. Tags that carry
   track IDs but contradict each other are a reason to look, not something for filenames to paper
   over. Only when no file has a track ID do the filenames' numbers decide (`MapByNumber`). Pairing
   by sort order, which the review marks for checking, never qualifies.
4. **Nothing in the library is in the way.** `CheckImportPlan` must pass with no Replace given.
   An upgrade (`duplicate`) or a file at the target (`exists`) is a Replace decision, and Replace
   is a person's consent.
5. **The category's library can take an import:** enabled, not Lidarr-managed, and mounted, the
   import routes' gate.

An album that qualifies is queued as an ordinary import job, always **Copy**, titled
*Auto-import … into …*, with `auto: true` in the event's details. It then goes through everything
a reviewed import does, including the re-plan when the job runs. An album that does not qualify
stays in the Downloads list with a note (`notes`, by album folder) naming the step it missed:
*1 of 12 files name their MusicBrainz release*, *the release has 14 tracks and the album 12
files*, *… would replace … — choose Replace or skip it*.

Auto-import runs after each poll, outside the poll's lock (the imports it queues take that lock),
behind its own `TryLock`. **Each download is looked at once** (`auto_checked_at`):
- An album left for review is not re-tried.
- A failed auto-import is offered for review like any failed album.
- A switch turned on later still reaches downloads that were waiting, since those were never looked
  at.
- MusicBrainz being unreachable is the one reason to look again on the next poll. It says nothing
  about the album.

## Transmission specifics

`modules.TransmissionClient`, the RPC at `/transmission/rpc` (a base URL already ending in `/rpc`
is used as it is). Basic auth when the daemon asks for it, and Transmission's CSRF guard: a request
without the current `X-Transmission-Session-Id` is answered 409 with the id, and sent once more with
it — a guard that keeps refusing is reported as a proxy stripping the header. Transmission has no
categories, only **labels** (3.0 on), so a watched category is a label, the imported mark is a label,
and the post-import category swaps labels. A torrent counts finished at 100% with nothing left, no
local error, and stopped, queued to seed or seeding; files come from `torrent-get` with `fileStats`,
a deselected one never complete. A 403 is the RPC whitelist refusing this host, and said so.

## SABnzbd specifics

`modules.SABnzbdClient`, the `/api` with an API key. A finished job is a **Completed** history entry
in a watched category (compared without case — SABnzbd lowercases them), read 200 newest at a time;
its `storage` is the final folder. SABnzbd cannot list a job's files, so the download is marked
`FilesOnDisk` and the poller reads its folder here, after the path mapping, as it would a folder
import. Nothing seeds: the download is not `seeding`, the import may move it, and there is nothing
to mark, move or remove in the client — the history has no tags, so the database row is the only
record. A refused key is a 200 with `status: false`, reported as the key.

## qBittorrent specifics

`modules.QBittorrentClient`, Web API v2:

- **Login** is a form `POST /api/v2/auth/login`, which answers with a session cookie in a cookie
  jar. Before 5.2 a success is `200 Ok.` and a failure `200 Fails.`; from 5.2 a success is `204`
  with no body and a failure `401`. All four are read, and a set cookie counts as a success
  whatever the body says (`loginSucceeded`). A login answered by an HTML page — an authentication
  proxy in front of qBittorrent — is reported as that, not as a wrong password. A 403 is answered by **one** fresh login, because a session ends when qBittorrent restarts.
  A rejected login is reported and not retried, because qBittorrent bans an address after repeated
  failures.
- **Every request carries `Referer: <base URL>`.** qBittorrent's CSRF protection refuses requests
  without it, which is the classic "works in curl, fails in code" failure. A 403 that comes right
  after a successful login is reported as a reverse proxy not passing the Host header through.
- **A file's path is the save path plus the file's name**, joined with the client's own separator.
- **Seeding is done** when a torrent is `pausedUP`/`stoppedUP` and its effective share limit was
  reached: `ratio >= max_ratio` or `seeding_time >= max_seeding_time` (minutes against seconds),
  with `-1` meaning no limit. A torrent stopped by hand, or with no limits, is never done.

## Tests

- `modules/qbittorrent_test.go`: against a fake Web API that enforces the Referer and sessions.
  Covers login, the single re-login after an expired session, the no-retry on a rejected login, the
  auth bypass, settled-state and category filtering, the imported tag, file completeness, Windows
  paths, `MapRemotePath`, and the single-file name hint.
- `process/auto_import_test.go`: auto-import on real tagged FLACs. It covers an album identified
  by its track IDs and one by its filenames' numbers, and the untagged, two-release, duplicate- and
  partial-track-ID cases left with their notes. It also checks that a file already in the library
  is never replaced, and that the switch is off by default and still reaches waiting downloads
  once turned on.
- `modules/transmission_test.go` and `modules/sabnzbd_test.go`: each against a fake that enforces its
  handshake (Transmission's session id and basic auth; SABnzbd's key). Labels as categories, the
  label swap, settled states, file completeness, `isFinished` versus a pause; SABnzbd's completed
  history only, and a refused key.
- `process/downloads_test.go`: the poller against a fake client (`newDownloadClient` is a var).
  Includes the post-import category reaching the client, removal only once a limit stopped it (once,
  never a foreign download, recorded in Activity), and a usenet download read from disk and moved.
  Covers ready, unavailable and settling downloads, the tag on an unseen download, a fixed mapping,
  forgetting, and a failed read recording no event. End to end: copy only, no double queue, album
  and download marked imported, and the client told. Also `ReconcileDownloads`.
- `routers/downloads_test.go`: admin gate, validation (a Lidarr library, a bad URL, a relative
  mapping), the password never returned, and *Check now* → list → scan → dismiss → restore against
  a stubbed qBittorrent.

## Related

- [import.md](import.md): the identification, review and job this feeds.
- [scanning.md](scanning.md#following-lidarr): the Lidarr follower, the same shape of poller.
- [wip.md](wip.md): what is left.
