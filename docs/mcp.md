# MCP server

Autotaggerr exposes what it knows about the library to AI assistants over the
[Model Context Protocol](https://modelcontextprotocol.io). An assistant connected to it can answer
"what am I missing from Kate Bush?", "which files failed last night, and why?" or "is anything
waiting on me?" from the same data the UI reads — without scraping the UI, and without a second
copy of the rules that decide what *owned*, *complete* or *wanted* mean.

**It only reads.** Nothing an assistant can call starts a run, changes a want or writes a file.
Acting is the next phase ([wip.md](wip.md#roadmap--ideas)); reading came first because every write in
Autotaggerr has a reason it is shaped the way it is — a pin outlives a manager change, a want has
three sources of which only one is editable, the verbs deliberately do not cascade — and an agent
that can read all of that is the prerequisite for one that acts on it sensibly.

## Connecting

The endpoint is **`/api/v1/mcp`** (Streamable HTTP). It takes the same credentials as the REST API.

1. **Settings → AI assistants (MCP) → Generate new API key.** The key is shown once, there, with
   ready-to-paste client config. Generating replaces the previous key everywhere it was used.
2. Give the client the URL and the key. Claude Code:
   ```bash
   claude mcp add --transport http autotaggerr https://<host>/api/v1/mcp --header "Authorization: Bearer <key>"
   ```
   A client that only speaks stdio (older desktop clients) can bridge with
   `npx mcp-remote https://<host>/api/v1/mcp --header "Authorization: Bearer <key>"`.

The key can be sent as `Authorization: Bearer <key>` or `X-Api-Key: <key>`; a UI session token also
works. Hosted clients that insist on OAuth (claude.ai connectors) cannot connect yet — that is the
remote-connector phase in [wip.md](wip.md#roadmap--ideas).

**Turning it off**: the *Allow AI assistants* switch on the same page (`autotaggerr_mcp_disabled`).
Off, the endpoint answers 404 before authenticating. It is read on every request, so the switch
applies without a restart. It is on by default because it opens nothing the same key does not already
open over REST — the switch is for an instance that wants no assistant talking to it, not a security
boundary.

## Tools

| Tool | Answers | Reaches MusicBrainz? |
|---|---|---|
| `get_overview` | version and sizes, files needing attention (errors / retrying / pins held / unmatched-waiting), the migration queue's length, the job queue and the three refresh passes | no |
| `get_configuration` | libraries with their manager, data source and tagger profile; managers; data sources; tagger profiles. Never credentials | no |
| `list_artists` | the collection with owned / complete / partial / missing / mismatch / picked counts; filter by name or `partial`, `mismatch`, `picked`, `monitored`, `unmonitored` | no |
| `get_artist` | one artist and every album: owned, complete, discrepancy, wanted and `wanted_source`, plus albums whose MBID no longer resolves | no |
| `list_missing` | wanted albums not owned, across the collection or one artist; `include_incomplete` adds owned albums with tracks missing | no |
| `get_album` | one album's state and every edition, which are owned and how complete | cache-first |
| `get_release_tracks` | one edition's tracklist, with the file identified as each track | cache-first |
| `list_files` | indexed files: filter by library, status, `transient`, path, an MBID (any kind), held pins | no |
| `get_file` | one file in full; `include_tag_diff` adds what a write would change, `include_tempo` its BPM | only with `include_tag_diff` |
| `list_activity` | the Activity feed: filter by type, status, title, one run (`parent_id`), runs only | no |
| `get_activity` | one event with its per-file rows (capped by `item_limit`) and its stages | no |
| `list_migrations` | MusicBrainz merges / re-keys / deletions, with what is wrong and what approving would do; `status=open` is the queue | no |

`list_missing` is the one query the REST API does not have. The UI answers it per artist, so an
assistant would otherwise call `get_artist` once per artist to assemble it. It applies the artist
page's rules (`newReleaseGroupView`) to every album under each credited artist in the collection —
a collaboration can be wanted by following one artist and not the other, exactly as on their two
pages — and lists each album once, under the first artist that wants it.

`GET /mb/:mbid/files` has no tool of its own: `list_files` with `mbid` answers the same question
through the same `releasesUnder` resolution, and returns rows the next call can act on.

### Shapes

- **Projections, not views.** Each tool returns its own compact struct: MBIDs, names, the state,
  the counts. No UI-only flags (`identity_editable`, `detachable`), no GORM bookkeeping. An assistant
  pays for every byte in context.
- **Every row carries the ID the next tool takes** — an artist's `mbid` into `get_artist`, an
  album's into `get_album`, an edition's into `get_release_tracks`, a track's `file_id` into
  `get_file`, an event's `parent_id` into `list_activity` — so drilling down needs no guessing.
- **Every list pages**: `limit` (default 50, capped at the REST maximum for that list, or 200 for
  the two paged in Go) and `offset`; every response carries `total` and `next_offset`, which is
  absent on the last page. An empty list is `[]`, never `null`.
- **Typed in and out.** Tools are registered with `mcp.AddTool[In, Out]`, so the SDK derives both
  schemas and returns `structuredContent` plus a text copy. The `jsonschema` tags on the input
  structs are the parameter documentation the model reads. A field with `omitempty` is optional;
  anything else is required. **Output structs avoid `uuid.UUID`** — the SDK validates output against
  the inferred schema, which reads a `[16]byte` as an array while the JSON is a string — so IDs are
  strings in every projection.
- **Errors are tool results, not protocol errors**, and name what to do next: `artist not found:
  <mbid> — list_artists returns the artist MBIDs in this collection`. A store failure ends with
  "the database may be busy, retry in a moment", so an agent waits rather than concluding the data
  does not exist. A missing tag diff on `get_file` is reported beside the file
  (`tag_diff_error`) rather than failing the call.

### Server instructions

Every session starts with a short glossary (`mcpInstructions` in `routers/mcp.go`): owned vs
complete vs discrepancy, the three `wanted_source` values and which one a user can change, what
`transient` means, what a held pin is, the four verbs and that they do not cascade, and a warning
that open-world tools spend a shared rate limit. The vocabulary is the hard part of reading this
data correctly, so it is said once rather than in every description.

## How it is built

### One binary, Streamable HTTP, stateless

The server is mounted in the existing Gin service using the official Go SDK
(`github.com/modelcontextprotocol/go-sdk`), `mcp.NewStreamableHTTPHandler` wrapped with `gin.WrapH`.

**No stdio mode.** A stdio server is a subprocess the client spawns, and Autotaggerr's value is a
live process — the job queue, the cache fronts, the database connection. A spawned copy would either
be a thin HTTP proxy (which `mcp-remote` already is) or a second writer on the database, which the
single serial queue exists to prevent.

**Stateless, JSON responses** (`StreamableHTTPOptions{Stateless: true, JSONResponse: true}`). Read
tools make no server-to-client requests, so a session would hold nothing. Stateless means no session
map, nothing lost across a restart, and nothing that breaks behind a proxy that buffers SSE. GET and
DELETE answer 405, as the transport specifies for a stateless server.

**Cross-origin browsers are refused** (`http.CrossOriginProtection`). The spec asks servers to
defend against DNS rebinding, and the app's CORS policy allows every origin — right for a
header-authenticated JSON API, but not a decision to inherit here. MCP clients send no `Origin` and
are unaffected.

### Authentication

The route is mounted beside the `protected` group rather than in it, because it needs one
middleware option the REST routes do not get: `auth.Middleware(db, key, auth.BearerAPIKey())`.

- **An API key is accepted as a Bearer token on this route only.** Many MCP clients can be given a
  bearer token but not an arbitrary header. A Bearer value is tried as a session token first; only
  one that does not verify is looked up as a key, so a session still works here too. On every other
  route a key in `Authorization` is refused, as before.
- **The user rides on the request context** (`auth.WithUser` / `auth.UserFromContext`) as well as the
  Gin context. Tool handlers receive a `context.Context`, never a `*gin.Context`, and the SDK passes
  the request's context through to them.
- **Every 401 carries `WWW-Authenticate: Bearer realm="autotaggerr"`** — RFC 9110 requires the
  challenge, and MCP clients start their auth flow from it. Browsers show no prompt for the Bearer
  scheme, so the SPA is unaffected. The 401-vs-503 split
  ([authentication.md](authentication.md#401-means-the-credential-is-bad--not-that-we-could-not-check-it))
  applies here unchanged.

**Role-split servers.** The handler's `getServer` picks one of two `*mcp.Server` values per request
— one for admins, one for everyone else — so a tool a user may not call is not *listed* for them,
rather than listed and refused. Today the two hold the same tools: nothing here is admin-only, and
the admin-only reads (`/settings`, download clients, the import scan) are deliberately not exposed.
The split exists so the first admin-only tool is one registration, not a rework.

### Tools call query methods, never handlers

Calling the REST handlers in-process (a synthetic request through the Gin engine) would have needed
no refactoring, and was rejected: it returns UI-shaped payloads (`GET /artists` is the whole
collection, unpaged), maps errors through status codes and back, runs authentication twice, and
makes the MCP surface a side-effect of whatever the UI wants this month.

Instead each handler a tool needs was split: **handler = parse params → query method → JSON; tool =
parse args → the same query method → project.** The query method holds the rules; the projection is
the tool's own shape. The extracted methods, all in `routers`:

| Query method | Handler | Tool |
|---|---|---|
| `artistList` (memoised) / `computeArtistList` | `listArtists` | `list_artists` |
| `artistDetail` | `getArtist` | `get_artist` |
| `releaseGroupDetailData` | `releaseGroupDetail` | `get_album` |
| `libraryItems(itemFilter)` | `listLibraryItems` | `list_files` |
| `itemTagDiff` / `itemTempoInfo` | `itemTags` / `itemTempo` | `get_file` |
| `eventFeed(eventFilter)` / `eventDetail` | `listEvents` / `getEvent` | `list_activity` / `get_activity` |

Their errors are written to be shown to a client (the store's own error is logged where it
happens), so the handler and the tool can both pass them through. The existing REST tests passed
unchanged across the extraction, which is what shows it did not move the API.

Nothing outside `routers` imports the SDK. The code is `routers/mcp.go` (mount, toggle, role split,
instructions, paging), `mcp_collection.go`, `mcp_files.go` and `mcp_activity.go`.

### Reads that spend the MusicBrainz budget

`get_album`, `get_release_tracks` and `get_file` with `include_tag_diff` go through `a.meta()`,
which is cache-first but may fetch, and every fetch waits on `RateLimit()` — the one limiter the job
queue also spends. They carry `OpenWorldHint: true`, their descriptions say a call may take a second
or more, and the instructions ask for no bulk calls. Nothing here *searches* MusicBrainz: search is
uncached by nature, and a search loop is the pattern that would starve the queue.

If an MCP-driven fetch ever visibly delays a run, the fix is a priority on the limiter (queue work
first), not a second limiter — MusicBrainz's limit is per client, and Autotaggerr is one client.

### API keys

`POST /api/v1/auth/me/api-key` replaces the caller's key and returns the new one. It is the only
time a key is shown — nothing reads one back, the posture of the write-only credentials on managers
and data sources — so rotating is also how a lost key is recovered. Before it existed, a user's key
was printed once to the first-boot log and was unreachable afterwards. The *Generate new API key*
button sits in the MCP section of Settings (behind a confirmation saying the old key stops working)
because connecting an assistant is what most needs it; there is no separate account page.

## Tests

In `routers/mcp_test.go`, through a real client session over the SDK's in-memory transport, against
a test database and `fakeMeta`:

- **`TestMCPToolsAreReadOnly`** lists the tools on both servers and fails on any without
  `ReadOnlyHint`, and on any tool added or removed without updating the expected list. It is the
  guard that keeps a write tool out of the read scope.
- **`TestMCPOpenWorldMarksMusicBrainzReads`** — exactly the three MusicBrainz-reaching tools say so.
- Every tool's happy path, its filters, paging, and its not-found error naming the tool to use.
- Over real HTTP: no credential → 401 with the challenge; `X-Api-Key`, a Bearer key and a session
  token all work; a Bearer key on a REST route does not; a cross-site browser request is refused;
  the switch answers 404 and takes effect without a restart; a rotated key replaces the old one and
  is never readable from `/auth/me`.

`auth/middleware_test.go` covers `BearerAPIKey`, the context user and the challenge from inside
`auth`, since coverage is counted per package. `settings/schema_test.go` covers the switch's
inversion and its applied-now message.
