# Feature: listening history

Each user can link where they listen — an account on a **Plex** server, or a **ListenBrainz**
user — and their plays are read into the database and overlaid on the collaboration graph
(Insights → Graph, see [insights.md](insights.md#listening-overlay)). Plays are **per Autotaggerr
user**: every row names its user, every route is scoped to the caller, and nobody — admins
included — reads another user's history.

The code: `models/listening.go` (rows), `modules/listenbrainz.go` and `modules/plex_history.go`
(clients), `process/listening.go` (the sync), `routers/listening.go` (API), `insights/listening.go`
(the overlay), and the *Listening* card on the Account page (`ListeningAccounts.tsx`).

## The rows

| Table | One row per | Notes |
|---|---|---|
| `listening_accounts` | linked account | user, source (`plex` / `listenbrainz`), the Plex server and server-local account id, or the ListenBrainz user name and optional token (write-only, `json:"-"`); `cursor`, `last_synced_at`, `last_error` |
| `listens` | play | user, account, time, the recording and release MBIDs (empty when unidentified), the matched library path for Plex, raw title/artist/album; unique on (account, external key) so reading a window twice stores it once |
| `listen_artists` | artist of a play | ListenBrainz's MBID mapping artists; read only for recordings the credits do not cover (music not on disk) |

Unlinking an account deletes its plays. Changing a ListenBrainz account's user name does too, and
starts its history again.

## Reading plays

**ListenBrainz** (`GET /1/user/{name}/listens`): pages backwards from now by `max_ts`, 1,000 at a
time, down to the cursor (`min_ts`), because the API answers newest first. The recording, release
and artists come from `track_metadata.mbid_mapping`, falling back to `additional_info`. A token is
sent when set; it is only needed for private listens. The rate-limit headers are honoured
(`X-RateLimit-Remaining` / `X-RateLimit-Reset-In`), and a 429 is waited out once. ListenBrainz is
not MusicBrainz, so `RateLimit()` does not apply.

**Plex** (`/status/sessions/history/all`, the server's own history): one account's plays from the
cursor, oldest first, 500 a page, tracks only, read with the **server's** token. A play names a
rating key, not a file, so the file is asked for once (`/library/metadata/{key}`) and cached in
`provider_cache` (source `plex_track_files`, a month; a track Plex no longer has, a day). The file
is then matched to the index by its **last path segments** (`modules.TrailingPathIndex`: three
segments, four or five to tell apart files that share three). It is the same idea as
`plexPathEndsWith` for album refresh: Plex may mount the library elsewhere, or run on Windows, and
nothing needs configuring. A play whose file is not in the index is kept with its raw strings and
no MBID, and counted as unidentified.

**Storing** is a page at a time, as it is read, and idempotent by key. So a first read of years of
history that fails part-way keeps what it read, and the next pass skips what it already has.

## The sync

`process.Runner.SyncListening` reads every enabled account from its cursor. It is modelled on
`FollowLidarr`: it runs on the **cron goroutine, not the job queue**. It writes no file and costs a
few requests per account, and queued behind an hours-long Process the overlay would only learn of
plays hours later. Two passes never overlap (`listenMu.TryLock`; a manual sync while one runs is
refused with `ErrListeningBusy`).

- A **cursor moves only when its account's pass completes.** A failure records `last_error`,
  leaves the cursor where it was, and records no Activity event: the window is read again next
  time, the plays stored before the failure are kept, and a row every half hour saying a
  server is down would bury the feed (the health check already says so).
- **One account failing does not stop the others.**
- The schedule is `autotaggerr_listening_cron_schedule` (Settings → Background jobs → *Listening
  history*, every 30 minutes by default). There is no on/off switch: with no account linked, a pass
  does nothing. **Read new plays now** on the Account page (`POST /listening/sync`) reads the
  caller's accounts at once, in the background.

## Linking Plex: proving whose account it is

A Plex server's history covers every account on it, and the server token can read all of it. Letting
any user pick "account 7" from a list would let them read someone else's history. So a user links
Plex by **signing in at plex.tv** (the PIN flow):

1. `POST /listening/plex/pin` creates a pin at plex.tv and returns the `app.plex.tv/auth` URL. The
   pin is **bound to the user who started it** (in memory, 15 minutes). Whoever approves a pin links
   *their* Plex account to the pin's user, so a pin claimed by someone else would hand over a
   stranger's history.
2. The page polls `POST /listening/plex/pin/:id/claim`: 202 while waiting; once approved, the plex.tv
   token says who signed in (`/api/v2/user`) and which servers they reach (`/api/v2/resources`).
3. For every enabled Plex server: if plex.tv lists it as **owned**, the account is the server's
   owner, which is always server account `1` (Plex does not tie it to the plex.tv id). Otherwise the
   plex.tv user id is matched against the server's `/accounts`. A server the account cannot reach is
   reported and skipped.
4. The plex.tv token is then dropped. Plays are read with each server's own token.

An **admin** may also link any account by hand (*Link without signing in*): they hold the server's
token anyway, so the proof proves nothing they could not already read. `GET
/media-servers/:id/accounts` (admin) lists a server's accounts for that picker.

## Counting a play

What `insights/listening.go` does with the rows:

- **Cross-source duplicates count once.** Many people scrobble Plex to ListenBrainz, so the same play
  arrives twice. Two plays by one user of one recording **from different sources within 10
  minutes** are one play. The same source twice is two plays. Raw rows are kept, so this is only
  how they are counted.
- **A play reaches artists through the graph's credits**: the recording's artists in
  `insight_credits`, in the roles the graph asks for. With *Production* ticked, a producer's node
  counts plays of what they produced. A recording with no credits (not on disk) falls back to the
  artists ListenBrainz named for the play, but only without a library filter, since a library's
  view is of what that library holds.
- **Co-listening:** plays are split into sessions on a 30-minute gap. Within a session, each play
  is paired with the next five plays' artists (a sliding window: a ten-hour session would otherwise
  connect everything in it, at n² cost), and a pair counts **once per session**.
  Then each artist keeps only its **three strongest partners** (a pair stays when it is among the
  top three of either artist). Someone who plays a few favourites daily has played every pair of
  them together; drawn whole, that was 562 edges among 35 artists on the small demo.

## The API

| Endpoint | Purpose |
|---|---|
| `GET /listening/accounts` | the caller's accounts, with plays and identified plays per account |
| `POST /listening/accounts` | link ListenBrainz (`username`, `token`), or, for an admin, Plex by hand (`media_server_id`, `plex_account_id`, `plex_account_name`) |
| `PUT /listening/accounts/:id` | `enabled`; for ListenBrainz `username` (forgets the old plays), `token` (absent keeps, empty clears) |
| `DELETE /listening/accounts/:id` | unlink, and forget its plays |
| `POST /listening/accounts/:id/test` | can it be read? 200 either way, `healthy` says which |
| `POST /listening/plex/pin`, `POST /listening/plex/pin/:id/claim` | the plex.tv sign-in above |
| `POST /listening/sync` | read the caller's accounts now (202) |
| `GET /media-servers/:id/accounts` | admin: a server's accounts |

Someone else's account is a 404, the same as one that does not exist.

## Tests

`modules/listening_test.go`: ListenBrainz paging, mapping, token and 429; Plex history, the
cached track file, accounts, the path index, and the sign-in match (owner and shared).
`process/listening_test.go`: both sources stored, Plex matched by path, idempotent re-reads, the
cursor, one broken account, a missing server. `insights/listening_test.go`: plays through credits,
cross-source dedupe, the period, listened-only with the centred exemption, co-listening sessions,
both. `routers/listening_test.go`: accounts scoped to the caller, the write-only token, the admin
gate on Plex by hand, the pin bound to its user, and the graph's parameters.
