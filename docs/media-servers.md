# Feature: media servers

The servers Autotaggerr tells to refresh an album after its files' tags change. Plex is the only
type today. There may be none, one or several, and each library names the ones that scan it: a
**Media servers** page (admin-only) holds the servers, and the library form links them.

## The shape, and why it is rows

`models.MediaServer` is a row like a manager or a download client: name, type, enabled, base URL,
and a write-only token (`json:"-"`; the API says only `token_set`). `models.LibraryMediaServer`
links a library to a server **in one section** of it, or in every music section when `SectionKey`
is empty. A library has any number of links, and a server any number of libraries. Two Plex servers
scanning one share is two links on one library. It is not two libraries.

This replaced two `config.json` keys, `plex_base_url` and `plex_token`, which made Plex one server
for the whole process. Each library's section and opt-out sat on its own row (`PlexSectionKey`,
`PlexSkip`). That was already the right half: which servers scan a library is a fact about the
library. The server being process config was the wrong half. It meant one server at most, a restart
to change it, and a `Settings → Plex` section unlike every other connection, which are all rows. The
row is typed (`type = "plex"`) so a second kind of server (Jellyfin) is a new client and a new value.
It does not need a second table, page or migration.

**Clients are built from the rows per use**, never once at startup:
`components.MediaServersForLibrary` reads a library's links to enabled servers when a pass reaches
the library, the way `BuildForLibrary` reads its manager. `components.MediaServerClients` reads
every enabled server when a refresh is flushed, and the health check reads them per run. A server
added, re-tokened, disabled or linked therefore takes effect on the next run, with no restart and
no second copy of the token to drift. `modules.PlexTargets` is what a library's pass carries: one
client per link, each narrowed to its section (`PlexClient.InSection`). An empty list refreshes
nothing.

## Refreshing

After a file's tags change, each of its library's servers is asked, on its own, for the album
holding that file (`PlexRefreshForFile`, by file path; see
[scanning.md](scanning.md#which-album-in-which-section)). A server that is down costs its own
refresh only, and the other servers still get theirs. The album goes into the run's
`AlbumRefreshSet` keyed by **server and album key**, not by title. Two albums can share a title, and
one album on two servers is two refreshes.

The flush (`Runner.flushPlex`) sends each album to the server it was found on. Servers are read at
flush time, so a server removed or disabled during the run fails its rows ("removed or disabled
before the refresh") rather than sending them elsewhere. It records one *Plex refresh* event per
run with one row per album and server. A row names its server only when the run reached more than
one. The keys go to `details.album_keys`, grouped by server.

Everything cached about a server's albums is scoped by the server: the folder cache key hashes
server, section and folder, and `InvalidatePlexAlbum(server, key)` drops only that server's entries.
A rating key means nothing on another server, and the same number there is a different album.
Entries cached before servers were rows carry no server and simply age out.

Organise asks each linked server to scan its sections after moving files
([naming.md](naming.md#organise)). The album page links into each server holding the album, one
link per library and server, named by server once there is more than one.

**Where a link opens** is the server's **Link address** (`WebURL`), like a Lidarr manager's. With
it set, a link opens that server's own web app (`<address>/web/index.html#!/server/<machine
id>/details?key=…`). That is the right server for certain, even for an account with several, and
it skips the round trip through plex.tv. Left empty, links go through `app.plex.tv`, which reaches
the server from any browser signed in to the account. That matters because the base URL Autotaggerr
uses is often one only it can reach. Both forms address the album by the server's machine
identifier, so the link address is only where the web app is, never which server.

## The API

| Endpoint | Who | Purpose |
|---|---|---|
| `GET /media-servers` | signed in | the servers, without tokens, with how many libraries each scans |
| `GET /media-servers/:id/sections` | signed in | one server's music sections, for the library form; a server that does not answer is reported in the body (200), so *every section* can still be chosen |
| `POST /media-servers`, `GET\|PUT\|DELETE /media-servers/:id` | admin | manage servers (`name`, `base_url`, `web_url`, `token`, `enabled`); a token absent on edit keeps the stored one; delete removes the server's library links |
| `POST /media-servers/:id/test` | admin | `/identity` with the token a refresh uses; 200 either way, `healthy` says which |
| `PUT /libraries/:id` with `media_servers` | signed in | replace the library's links as a set: `[{media_server_id, section_key}]`. Absent leaves them alone, `[]` removes them all, and a server may appear once |

Reading is open to anyone who edits a library, because the library form lists the servers to tick.
Writing is admin-only for the reason download clients are: a row holds a credential.

## Upgrading from the config keys

`files.LoadConfig` reads `plex_base_url` / `plex_token` from the raw file into `files.LegacyPlex`.
They are no longer fields of `models.ConfigStruct`, so `TestEveryConfigKeyHasAHome` has nothing to
ask about. At startup `database.MigrateLegacyPlex` does the following:

1. Creates a server named *Plex* from the keys, unless one already has that base URL.
2. Links it to every library that did not have `plex_skip`, keeping `plex_section_key`.
3. Drops both columns from `libraries`.

Only then does `files.ClearLegacyPlex` rewrite `config.json` without the keys. Until that point,
`SaveConfig` keeps writing them, so a boot that fails in between loses nothing. Libraries are linked
only when the row is **created** by this run. A rerun after such a failure finds the row, and must
not link again, because `plex_skip` is gone by then and the libraries it skipped would be linked.
Half a pair (a URL without a token) never refreshed anything; it is dropped with a warning.

It is not a data migration (`database/migrate.go`) because it needs the config file, which only
`main` has.

## Tests

`database/legacy_plex_test.go` covers the upgrade, rerun included, and the columns dropped without
keys. `routers/plex_test.go` covers CRUD, the write-only token, sections, library links and their
validation, cleanup on delete, and album links per linked library.
`process/plex_flush_test.go` covers per-server flushing, server names on rows, and a removed
server. `health/probes_test.go` covers server rows being probed and disabled ones not.
`main_boot_test.go` checks that the keys become a row and leave `config.json`.

## Related

- [scanning.md](scanning.md#plex-refresh): how the album is found, the folder cache, and the event rows.
- [media-manager.md](media-manager.md): the component model this is one more row of.
