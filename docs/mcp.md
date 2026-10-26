# MCP server

Autotaggerr exposes the library to AI assistants over the
[Model Context Protocol](https://modelcontextprotocol.io). An assistant connected to it can answer
"what am I missing from Kate Bush?", "which files failed last night, and why?" or "is anything
waiting on me?" from the same data the UI reads — and, given a key that may write, act on the
answer: follow an artist, want an album, identify a file, approve a migration, run one of the four
verbs.

**What an assistant may do is the key's scope.** A *read* key is shown only the tools that read; a
*read & write* key is shown the actions too. Reading came first, and is still the default to hand
an assistant, because every write in Autotaggerr has a reason it is shaped the way it is — a pin
outlives a manager change, a want has three sources of which only one is editable, the verbs
deliberately do not cascade — and an agent that can read all of that is the prerequisite for one
that acts on it sensibly.

## Connecting

The endpoint is **`/api/v1/mcp`** (Streamable HTTP). It takes the same API keys as the REST API.

1. **Settings → AI assistants (MCP) → Create key**: a name, and *Read only* or *Read & write*. The key
   is shown once, there, with ready-to-paste client config.
2. Give the client the URL and the key. Claude Code:
   ```bash
   claude mcp add --transport http autotaggerr https://<host>/api/v1/mcp --header "Authorization: Bearer <key>"
   ```
   A client that only speaks stdio (older desktop clients) can bridge with
   `npx mcp-remote https://<host>/api/v1/mcp --header "Authorization: Bearer <key>"`.

The key can be sent as `Authorization: Bearer <key>` or `X-Api-Key: <key>`; a UI session token also
works, with write scope. Hosted clients that insist on OAuth (claude.ai connectors) cannot connect yet
— see [wip.md](wip.md#roadmap--ideas).

**Turning it off**: the *Allow AI assistants* switch on the same page (`autotaggerr_mcp_disabled`).
Off, the endpoint answers 404 before authenticating. It is read on every request, so the switch
applies without a restart. It is on by default because it opens nothing the same key does not already
open over REST — the switch is for an instance that wants no assistant talking to it, not a security
boundary. The scope is the boundary.

## API keys

Each user holds any number of named keys (`models.APIKey`, table `api_keys`), each `read` or
`write`. Only a SHA-256 of the key is stored — keys are 32 random bytes, so there is nothing to
guess and no salt is needed, and lookup stays one indexed equality — plus a six-character prefix so
a listed key can be matched to the one a client holds. `last_used_at` is recorded at most every five
minutes per key, off the request path: authentication must not wait on a write lock.

- `GET /api/v1/auth/me/api-keys` lists the caller's keys; `POST` creates one (`{name, scope}`) and
  returns it **once**; `DELETE /…/:id` revokes one, effective immediately.
- **Keys are managed from a signed-in session only.** A key that could mint keys would turn one
  leaked key into a foothold that survives revoking it, so these routes answer 403 to any key.
- **A read key is read-only on the REST API too**: the middleware refuses it on anything but
  GET/HEAD/OPTIONS (403, "this API key is read-only"). Otherwise a key handed out as "cannot write"
  could write by asking the other door. The MCP route opts out of the method rule
  (`auth.ScopeCheckedByEndpoint`) because it is all POSTs, and enforces scope by the tools it lists.
- Upgrading moved each user's old single plaintext key into `api_keys` as a write key named
  "default" and cleared the plaintext (`database.hashUserAPIKeys`), so nothing using it broke. The
  first admin of a new install gets the same, printed once to the first-boot log. A user created by
  OIDC signup gets no key until they make one.

## Tools

### Reading — every key

| Tool | Answers | Reaches MusicBrainz? |
|---|---|---|
| `get_overview` | version and sizes, files needing attention (errors / retrying / pins held / unmatched-waiting), the migration queue's length, the job queue and the three refresh passes | no |
| `get_configuration` | libraries with their manager, data source and tagger profile; managers; data sources; tagger profiles. Never credentials | no |
| `list_artists` | the collection with owned / complete / partial / missing / mismatch / picked counts; filter by name or `partial`, `mismatch`, `picked`, `monitored`, `unmonitored` | no |
| `get_artist` | one artist and every album: owned, complete, discrepancy, wanted and `wanted_source`, which libraries hold it, plus albums whose MBID no longer resolves. `library` (name or ID) reads one library; the answer lists the artist's libraries with who decides in each, and `edit_blocked` says why a want could not be written in this view | no |
| `list_missing` | wanted albums not owned, across the collection or one artist; `include_incomplete` adds owned albums with tracks missing | no |
| `get_album` | one album's state and every edition, which are owned, how complete, in which libraries, and how many files (more than tracks means copies). `library` reads it in one library | cache-first |
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

### Acting — write keys only

| Tool | Does | Writes audio? | MusicBrainz? |
|---|---|---|---|
| `search_artists` / `search_releases` / `browse_artist_albums` | find the MBIDs an action needs; browse lists any artist's albums, in the collection or not | — | yes |
| `process` | queue the full pipeline — everything, one library or one artist | yes | yes |
| `scan` | re-derive the collection from the index, inline; `walk_disk` walks the folders first (queued) | no | no |
| `refresh_metadata` | queue a MusicBrainz refresh; `force` at artist scope only | no | yes |
| `tag_files` | queue rewriting tags from stored identities | yes | no |
| `cancel_job` | ask the running job to stop between files | no | no |
| `add_artist` | add an artist by MBID (name looked up when omitted); idempotent. Refused when Lidarr manages every library ([collection.md](collection.md#in-the-ui)) | no | when unnamed |
| `follow_artist` | follow on/off, types, secondary types, year cutoff — then re-sync the discography; `library` names the native library to follow into | no | yes |
| `want_album` / `unwant_album` | an explicit want, of an album, one edition, or some songs; and removing one. `library` (name or ID) names the native library the want is for | no | for an album the collection has not seen |
| `attach_file` | pin a file to a release and track, validated against the release, and write its tags | yes | yes |
| `release_pin` / `confirm_pin` | hand a pinned file back to automatic identification; keep a held pin for the new audio | confirm writes | no |
| `approve_migration` / `dismiss_migration` | apply, or deliberately not apply, a detected identity change | no | no |

**Queued verbs return at once.** The job's event does not exist until the worker reaches it, so
nothing returns an event ID; the result says where to watch (`get_overview`'s jobs, then
`list_activity`). The queue's dedup makes asking twice harmless, which is why those tools are
`IdempotentHint`.

**Search is write-scoped although it only reads.** It exists to feed actions (`add_artist`,
`want_album`, `attach_file`), and an uncached search loop is the one read pattern that would starve
the job queue's MusicBrainz budget; a read key has no use for it that `get_*` does not answer from
the database.

**Annotations are honest.** Every action states `DestructiveHint`: true for what overwrites or
discards — anything that writes tags, a pin released, a want removed, a migration decided — and false
for what only adds or re-derives. Clients use these to decide what to confirm with the user; since
the server is stateless (below) it cannot ask itself, and does not need to.

**Lidarr's decisions stay Lidarr's.** `want_album` and `attach_file` are refused for a Lidarr
library with the same message the UI gets. Authority is per library
([collection.md](collection.md#authority-is-per-library)), so a want for an artist Lidarr governs
lands in the one native library there is when no `library` is named, and is refused only when there
is none — or more than one, when the assistant has to name it. `get_artist` and `get_album` read
every library at once unless given a `library`, and then read exactly as the artist page's tab for
it does (`viewScope`): that library's wants, its holdings, its authority. Without one, an artist
Lidarr governs in one library and Autotaggerr in another shows the union of both libraries' wants,
which is why the answer lists the libraries to choose from. `unwant_album` is not gated, like its
REST route, because a stale want needs a way out.

### Shapes

- **Projections, not views.** Each tool returns its own compact struct: MBIDs, names, the state,
  the counts. No UI-only flags (`identity_editable`, `detachable`), no GORM bookkeeping. An assistant
  pays for every byte in context.
- **Every row carries the ID the next tool takes** — an artist's `mbid` into `get_artist`, an
  album's into `get_album` or `want_album`, an edition's into `get_release_tracks` or `attach_file`, a
  track's `file_id` into `get_file`, an event's `parent_id` into `list_activity`.
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
  does not exist.

### Server instructions

Every session starts with a short glossary (`mcpGlossary` in `routers/mcp.go`): owned vs complete vs
discrepancy, the three `wanted_source` values and which one a user can change, what `transient`
means, what a held pin is, the four verbs and that they do not cascade, and that open-world tools
spend a shared rate limit. A write session's instructions add how acting behaves: verbs queue, which
tools write audio, that Lidarr-managed identities are refused, and to search for IDs first.

## How it is built

### One binary, Streamable HTTP, stateless

The server is mounted in the existing Gin service using the official Go SDK
(`github.com/modelcontextprotocol/go-sdk`), `mcp.NewStreamableHTTPHandler` wrapped with `gin.WrapH`.

**No stdio mode.** A stdio server is a subprocess the client spawns, and Autotaggerr's value is a
live process — the job queue, the cache fronts, the database connection. A spawned copy would either
be a thin HTTP proxy (which `mcp-remote` already is) or a second writer on the database, which the
single serial queue exists to prevent.

**Stateless, JSON responses** (`StreamableHTTPOptions{Stateless: true, JSONResponse: true}`). No tool
makes a server-to-client request, so a session would hold nothing. Stateless means no session map,
nothing lost across a restart, and nothing that breaks behind a proxy that buffers SSE. It rules out
elicitation, which is fine: clients already confirm a non-read-only tool with the user, and the
annotations tell them which. Long verbs are followed by polling `get_overview`, not by progress
notifications.

**Cross-origin browsers are refused** (`http.CrossOriginProtection`). The spec asks servers to
defend against DNS rebinding, and the app's CORS policy allows every origin — right for a
header-authenticated JSON API, but not a decision to inherit here. MCP clients send no `Origin`.

### Authentication and scope

The route is mounted beside the `protected` group rather than in it, because it takes two middleware
options the REST routes do not: `auth.Middleware(db, key, auth.BearerAPIKey(), auth.ScopeCheckedByEndpoint())`.

- **An API key is accepted as a Bearer token on this route only.** Many MCP clients can be given a
  bearer token but not an arbitrary header. A Bearer value is tried as a session token first; only
  one that does not verify is looked up as a key.
- **The user and the credential ride on the request context** (`auth.UserFromContext`,
  `auth.CredentialFromContext`) as well as the Gin context. Tool handlers receive a
  `context.Context`, never a `*gin.Context`.
- **Every 401 carries `WWW-Authenticate: Bearer realm="autotaggerr"`**, which MCP clients start
  their auth flow from. The 401-vs-503 split
  ([authentication.md](authentication.md#401-means-the-credential-is-bad--not-that-we-could-not-check-it))
  applies unchanged.

**Servers per role × scope.** `mcpHandler` builds four `*mcp.Server` values once — admin or not,
write or not — and `getServer` picks one per request from the context. A tool a caller may not use is
not *listed* for them, rather than listed and refused. Role changes nothing yet: no tool is
admin-only, and the admin-only reads (`/settings`, download clients, the import scan) are not exposed.

### Tools call commands and queries, never handlers

Calling the REST handlers in-process would have needed no refactoring, and was rejected: it returns
UI-shaped payloads, maps errors through status codes and back, runs authentication twice, and makes
the MCP surface a side-effect of whatever the UI wants this month.

Instead, each handler a tool needs was split: **handler = parse params → method → JSON; tool =
parse args → the same method → project.** Reads became query methods returning `(T, error)`; actions
became commands in `routers/commands.go` that return an `*apiError` carrying the HTTP status, so the
handler answers exactly as before (`respondErr`) and the tool passes the same message on. The guards
live in the commands — `filesIdentityEditable` and `wantTarget` (the Lidarr gates), the
nothing-to-tag refusals, the not-found checks — so they cannot be skipped by asking through the other
door. The existing REST tests passed unchanged across the extraction.

| Method | REST handler(s) | Tool |
|---|---|---|
| `artistList`, `artistDetail`, `releaseGroupDetailData` | `listArtists`, `getArtist`, `releaseGroupDetail` | `list_artists`, `get_artist`, `get_album` |
| `libraryItems`, `itemTagDiff`, `itemTempoInfo` | `listLibraryItems`, `itemTags`, `itemTempo` | `list_files`, `get_file` |
| `eventFeed`, `eventDetail` | `listEvents`, `getEvent` | `list_activity`, `get_activity` |
| `queueProcess`, `queueRefresh`, `queueTag`, `runScan` | the four verbs at all three scopes | `process`, `refresh_metadata`, `tag_files`, `scan` |
| `updateFollowSettings`, `addDesire` | `updateFollow`, `setDesire` | `follow_artist`, `want_album` |
| `attachFile`, `releasePin`, `confirmHeldPin` | `attachItem`, `detachItem`, `confirmPin` | `attach_file`, `release_pin`, `confirm_pin` |
| `approveMigrationByID`, `dismissMigrationByID` | `approveMigration`, `dismissMigration` | `approve_migration`, `dismiss_migration` |

`setArtistMonitored` deliberately stays its own: routed through `updateFollowSettings` it would gain
a MusicBrainz read when unmonitoring, which the toggle on the artist page never made.

Nothing outside `routers` imports the SDK. The code is `routers/mcp.go` (mount, toggle, servers,
instructions, paging), `mcp_collection.go`, `mcp_files.go`, `mcp_activity.go` and `mcp_actions.go`.

### Reads that spend the MusicBrainz budget

Tools marked `OpenWorldHint` may fetch, and every fetch waits on `RateLimit()` — the one limiter the
job queue also spends. Their descriptions say a call may take a second or more and the instructions
ask for no bulk calls. If an MCP-driven fetch ever visibly delays a run, the fix is a priority on the
limiter (queue work first), not a second limiter: MusicBrainz's limit is per client, and Autotaggerr
is one client.

## Tests

In `routers`, through a real client session over the SDK's in-memory transport, against a test
database and `fakeMeta`:

- **`TestMCPScopeSplit`** — a reader sees exactly the read tools, each `ReadOnlyHint`; a writer sees
  those plus the actions, each stating `DestructiveHint`. The guard that keeps a writing tool away
  from a read key, and any tool added without updating the expected lists.
- `mcp_test.go` — every read tool's happy path, filters, paging and not-found error; over real HTTP,
  authentication (no credential, `X-Api-Key`, Bearer key, session), cross-site refusal and the switch.
- `mcp_actions_test.go` — each action's refusals (nothing to tag, unknown artist, both scopes given,
  force without an artist, the Lidarr gate), the shapes of what it queues or returns, and the state it
  leaves (a want visible on `get_artist`, a pin on the file, a migration's status).
- **`TestAPIKeyManagement`** — create, list without the secret, a key cannot manage keys, a read key
  GETs but cannot POST on REST, the MCP tool list follows the key's scope, and revoking is immediate.

`auth/middleware_test.go` covers the method rule, `ScopeCheckedByEndpoint`, the credential on both
contexts, `last_used_at` and `CreateAPIKey`; `database` covers the seed's hashed key and
`hashUserAPIKeys`; `settings` the switch's inversion.
