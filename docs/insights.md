# Feature: insights

The **Insights** page (Music → Insights) answers questions about the library as a whole, in four
tabs: **Duplicates**, **Stats**, **Graph** and **Storage**. The graph can carry the signed-in
user's plays ([listening overlay](#listening-overlay)). Storage has its own doc,
[storage.md](storage.md). It was a page of its own under System until it moved here; `/storage`
redirects to the tab. The library select is hidden on that tab, because disks are not per library.

**It reads; it does not write.** Duplicates, Stats and Graph come from the file index
(`library_items`), the collection tables and the release cache, with no MusicBrainz call and no file opened, so they
answer inline. Only one thing reads audio: checking whether two copies are the same audio. That is
a queued job, and it writes only to the index. The code lives in the `insights` package; the routes
are in `routers/insights.go`. Assistants read the same functions through two MCP tools,
`get_library_stats` and `list_duplicates` ([mcp.md](mcp.md)).

Those three read enabled libraries only (`models.InEnabledLibrary`), and the library select narrows
them to one.

## Duplicates

A duplicate is two or more files with the same **MusicBrainz recording ID**
(`LibraryItem.MBRecordingID`), among files with an identity (`models.InDiskView`). A group is one
recording. It is classified by its most actionable pair:

| Level | Test | Meaning |
|---|---|---|
| `identical` | same audio hash, same album | the same audio twice. One copy can go |
| `mixed_format` | different formats, same album | a lossy copy beside the lossless one (FLAC and MP3 are the only formats read today) |
| `same_format` | same format, same album, audio different or not checked | a re-rip, or another master |
| `single_on_album` | a copy on a single, and one of the same version on an album | the single's copy can go: the album has it |
| `across_releases` | the same recording on another album, or another version | a compilation, an EP, a remix or demo edition. Usually intended |

### Copies are compared within one album and one version

"Album" means the release group (`CollectionRelease.ReleaseGroupMBID`, falling back to the cached
release). Two editions of one album holding the same track are duplicates: a standard and a deluxe
edition, say.

**A copy of another version is not.** MusicBrainz files a remix or demo edition in the album's own
release group, often with the original recordings on it, and a radio edit single can carry the
album recording. Whoever holds both holds them on purpose. So each copy has a **version**
(`DuplicateFile.Version`, `versionKind`): remix, demo, live, acoustic, instrumental, a cappella,
karaoke or edit. It is read from the edition's title or MusicBrainz disambiguation, or else from
the file's own track title on that release, since MusicBrainz sometimes credits a version to the
plain recording and only the track title says so. A title only counts in its brackets, so
"Song (Demo)" marks a demo and "Live Forever" marks nothing. A disambiguation is read whole
("demo edition"). "Version" alone does not count, because "Album Version" is the original, and
neither does "remastered", because a remaster is another master of the same version (`same_format`).
Copies are compared within one album *and* one version. Two versions of a recording are at most
`across_releases`, and are never hashed against each other.

**A single on its album is the one cross-album case shown by default.** A copy on a single, beside
a copy of the same version on an album, is `single_on_album`. The album has the track, so the
single is redundant, which is often why someone opens this page. A radio edit or remix single is
another version and stays `across_releases`. Other cross-album copies (compilations, EPs) are
`across_releases` because deleting either copy loses an album. Both levels are flagged
`identical_audio` when two copies share a hash.

### Paging

`GET /insights/duplicates` returns a page of groups (`limit`, default 100, at most 500; `offset`)
and `matched`, the number of groups that pass the filters. On the large demo, the full list was
14 MB of JSON for a page that draws a hundred groups at a time. The page appends the next page on
*Show more*. The level counts and the unchecked count always describe every group.

### What is hidden by default

- **`across_releases` groups** (*Show other albums and versions*). They outnumber everything else
  in most libraries. `single_on_album` groups are not hidden. When they are
  shown, compilations stay hidden behind a second toggle.
- **Files in different libraries.** A lossless library beside a lossy mirror is a duplicate of every
  file, on purpose. *Compare across libraries* turns it on.

The level counts at the top always include the hidden groups, and say when some are hidden.

### The audio hash

`LibraryItem.AudioHash` is `modules.AudioPayloadHash`: SHA-256 of the file's audio with every tag
block left out, the same hash tempo, loudness and pins use. It is stored with the size and mtime it
was taken at (`AudioHashSize`, `AudioHashModTime`) and is **current** only while those still match
the index. A tag write moves both, so a hash that is not current means "not checked", never
"different". This is the same identity rule tempo and loudness use.

**It is computed only for candidates, never by Process.** The hash decodes nothing, but it reads
the whole file. Hashing during Process would cost a full read of the library on the first run after
an upgrade, and of every new file after that. The only files whose hash can change what a group
says are two or more files of one recording, on one album, **in one format**
(`insights.UncheckedCandidates`). Two formats never share a hash, and a pair across albums is
`across_releases` either way. Those candidates are a small fraction of the library.

**Check audio** (`POST /insights/duplicates/check`) queues `process.Runner.CheckDuplicates`. It is a
job on the serial queue at metadata priority, since it writes no file
([scanning.md](scanning.md#the-job-queue)), and it records a `duplicate_check` activity. It does
not cascade from any other verb. For each candidate it reuses before it reads:

1. **Already current** for the file as it is on disk now: nothing to do.
2. **A tempo or loudness row** for the path with the same size and mtime: its `audio_hash` is the
   same value reading the file would give, so it is copied. A pin's hash is not used, because it
   records no size or mtime to show it is still current.
3. Otherwise the file is read.

Results are stored as they are produced, so a stopped check loses nothing. A press with nothing to
check is refused with the reason (409).

`library_items.content_hash` was dropped (`drop_library_item_content_hash`). Nothing ever wrote it,
and its name promised a whole-file hash, which is exactly what this hash is not.

## Stats

`GET /insights/stats` (`insights.LibraryStats`):

- **Counts:** files, size, play time, recordings, releases, albums, artists. Albums and artists are
  release groups and their primary credited artist, read from the cached releases.
- **Breakdowns:** format (from the extension), quality (the file's rung on the quality ladder, read
  from its headers by Process, *Not measured* until then; see [quality.md](quality.md#measuring-files)), release type, decade (release-group first release
  date), genre (the album's strongest MusicBrainz genre, so each file counts once), country and
  label. Long breakdowns keep the top 12 and sum the rest into *Other*. Files whose release does not
  say are counted as *Unknown*.
- **Coverage:** ISRC known (MusicBrainz lists one for the recording, out of identified files),
  album cover cached (the album's or release's front cover is in the local artwork cache; covers are
  never embedded in files, so this is what the browsing pages can show),
  ReplayGain measured, BPM measured and identified (out of all files), plus counts of unmatched,
  failing, pinned and held files, each linking to its Items filter.
- **Catalogue against disk:** monitored catalogue albums with nothing on disk, albums on disk with
  tracks missing, and albums on disk that no catalogue lists. These read the collection tables,
  which are not split by library, so they are left out of a one-library view.

Breakdowns and play time read the **release cache only** (`modules.CachedRelease`), never the
network. A file whose release is not cached is left out of them and counted in `uncached`, and the
page says so instead of showing smaller numbers as if they were complete.

Bitrate, sample rate and bit depth are now stored with each file's quality, but are shown only
through the Quality breakdown and on Items. The quality rung is the question they were being read to
answer.

## Graph

Who works with whom. Artists are nodes; two are connected when **the same recording credits
both**, and the edge's weight is how many recordings in scope do. A node's size is how many
recordings in scope credit that artist.

### Where the credits come from

`insight_credits` holds one row per release, recording, artist and role, derived from the cached
release by `insights.ReleaseCredits`:

| Role | Read from |
|---|---|
| `credit` | the track's artist credit (the recording's, when the track has none) |
| `performer` | instrument, vocal and performer relationships, orchestras, conductors |
| `production` | producer, engineer, mixer, remixer, DJ-mixer |
| `writing` | composer, lyricist, writer, arranger, through the recording's works |

These are the same relationships `modules.TrackCredits` writes to a file. `modules.CreditGroup`
maps a relationship type to its group from `creditRelTypes`, so the graph and the tags cannot
disagree about what a credit is. Release-level relationships (a producer for the whole album)
apply to every track. Artists are keyed by MBID and named by their own name, not the
credited-as name, so one artist is one node. Video tracks are left out.

**Keeping it current** (`insights.SyncCredits`): credits are read for every release a file is on
that has none yet, or whose cache entry was fetched after they were read
(`insight_credit_releases.cached_at`), and dropped for releases no file is on any more. A pass where
nothing changed reads two short lists and writes nothing. It runs:

- after every collection rebuild, which means every Scan (`collection.RebuildScoped`). It runs
  after the transaction, never inside it: failing to derive credits must not undo a rebuild.
- once at startup, in the background, which fills the table after an upgrade.

It reads the cache only, never the network. A release that is not cached has no credits, and the
page counts those releases (`unsynced`). A release that leaves the cache keeps the credits it had.

### Assembling a graph

`GET /insights/graph` (`insights.Graph`) joins `insight_credits` to `library_items` on release and
recording, so the library filter is exact. It counts **distinct recordings**: a recording held on
two releases, or in two libraries, or crediting one artist in two roles, counts once.

| Parameter | Default | Effect |
|---|---|---|
| `roles` | `credit` | which credits connect artists |
| `min_weight` | 1 | fewest shared recordings per edge |
| `min_tracks` | 1 | fewest recordings per artist |
| `special` | off | let Various Artists and placeholders (`[unknown]`, `[traditional]`, …) in |
| `artist`, `hops` | — | centre on one artist: their collaborators (1) or theirs too (2) |
| `limit` | 150 (max 500) | node cap |
| `everyone` | off | every artist credited, not only [managed](collection.md#managed-and-present-only-artists) ones |

- **Various Artists is out by default.** It is credited on every compilation track and would
  connect everything.
- **Only your artists by default.** The credits name every featured artist, producer and writer, so
  the graph was mostly strangers. An edge is kept only when both of its artists are
  [managed](collection.md#managed-and-present-only-artists). The centred artist counts as managed,
  so a graph centred on anyone shows their managed collaborators. *Show everyone credited*
  (`everyone=1`) turns this off. The router reads the managed set per request
  (`collection.ManagedArtists`) and hands it in through `GraphOptions.Managed`, because `insights`
  cannot import `collection`. It is applied **after the cached count**, like the floors, so toggling
  it never recounts. Each node carries `managed`. In the everyone view, unmanaged nodes are drawn at
  lower fill opacity and their tooltip says so.
- **The node cap is what keeps the graph readable.** Uncentred, artists are picked by following
  the strongest edges: each edge, heaviest first, brings in both of its artists until the cap,
  and an edge is never split. Picking the strongest artists one by one chose artists that did not
  connect to each other (150 nodes with 71 edges on the 110k demo). Centred, it keeps the nearest
  ring first and the strongest within each ring.
  `truncated` says the cap cut some artists.
- **Only connected artists are drawn.** An artist sharing no recording with anyone is not part of a
  graph of who works with whom.

**Performance.** Measured on the large [demo](demo.md) (110k tracks, 670k credits, 223k connected
pairs), every role on: 3.5 s cold, 0.8 s while changing filters. Credited artists only: about 1 s.
Three things keep it there:

- **Two scans, no join.** The in-scope (release, recording) pairs are read once, and the credits for
  the asked roles are streamed past them with plain row scans. The join it replaced probed
  `library_items` by release for every credit, and scanning its result through GORM cost as much
  again (12 s).
- **The counted graph is cached** (one entry): per-pair weights and per-artist counts for a scope
  (library, roles, Various Artists). The key includes a fingerprint of the credits, the index and
  the libraries, so any change recounts. The node cap, the floors and centring are applied after,
  so moving them does not rescan.
- **Only what is shown is named.** The scan carries no names. The 25 strongest pairs are picked by
  weight before anything is named or sorted by name, and names and collection membership are
  then looked up for the artists drawn or listed.

The cold cost is now mostly SQLite stepping through the credits. Counting pairs at sync time
instead of per request is the next step if a library outgrows it.

**Adding a filter** is a field on `insights.GraphOptions`, a line in `routers/insights.go`, and an
entry in `GRAPH_FILTERS` (`webui/src/pages/Insights.tsx`), or `LISTEN_FILTERS` for one of the
[listening overlay](#listening-overlay).

### Finding an artist

The search box above the graph finds an artist by name. Artists already drawn are matched in the
browser and listed first. Picking one selects them, which lights up their connections, and pans and
zooms the canvas to them. Everyone else the credits name comes from
`GET /insights/graph/artists?q=` (`insights.SearchGraphArtists`: a name match over every credited
artist, in every role and library, most credited first, ten at most), because the node cap and the
filters leave most artists undrawn. The list it searches is built by one grouped scan of
`insight_credits` and kept in memory until the graph's fingerprint changes. Scanning per keystroke
took over a second on the medium demo. Picking one of those **centres** the graph on them, which draws them
with their collaborators. A centred artist counts as managed for the [managed-only](#assembling-a-graph)
filter, so someone who is not one of your artists can still be found. Enter takes the first
suggestion.

### Colour

*Colour* draws nodes by **genre** (the default), **role** or nothing. It is drawn rather than
asked for, so changing it costs no request.

- **Genre:** an artist's strongest genre across the recordings that credit them in the asked
  roles. Each release's genre (its release group's strongest MusicBrainz genre) is stored on
  `insight_credit_releases.genre` at sync time. Rows synced before the column existed carry an older
  `version`, and `SyncCredits` reads them again.
- **Role:** the role they hold on the most recordings in view.
- **Eight colours:** the library's eight most common genres get colours of their own
  (`report.genres`, `insights.ColouredGenres`, ranked library-wide so a filter never repaints a
  genre). Any other genre shares a neutral as *Other genres*, and an artist with no genre known
  is drawn as a neutral ring. Past three, not every pair of colours stays apart; the style guide
  says why eight was chosen anyway and how naming carries it.

### Listening overlay

With a listening account linked on the Account page ([listening.md](listening.md)), the graph also
shows what the signed-in user **plays**. The overlay is always the caller's own history
(`GraphOptions.Listener` comes from the session, never a parameter), and `report.listening` is
absent for a user with no account, which hides its controls.

| Parameter | Default | Effect |
|---|---|---|
| `since` | all | the period plays are counted over: `30d`, `90d`, `365d` (any number of days) or `all`; `off` draws the library alone (below) |
| `listened` | off | keep an edge only between artists with at least `min_plays` plays; the centred artist is exempt |
| `min_plays` | 1 | the floor for `listened` |
| `edges` | `credits` | `listening` connects artists played close together in a session; `both` lays out by credits and adds co-listening between the artists drawn |

- **Off is the library graph exactly as without an account.** *Period: Off (library only)*
  (`since=off`, `GraphOptions.ListeningOff`) counts nothing and ignores `listened`, `min_plays`
  and `edges`. The answer carries `listening: {off: true, accounts: N}` and no plays, so the page
  keeps the *Your listening* controls, greys out all but Period, and falls back from *Colour:
  Plays* and *Size by: Plays* to genre and recordings.
- **Plays per artist** (`GraphNode.Plays`) are every play, not distinct recordings, attributed
  through the credits in the roles asked for (see [listening.md](listening.md#counting-a-play)).
  `Tracks` stays recordings.
- **Listened-only is applied after the cached count**, where *only your artists* is, so toggling
  it never recounts.
- **Co-listening swaps the counted graph and nothing else.** It has the same shape as the credit
  count (weights per pair, a count per artist, which is plays), so the floors, the managed filter,
  centring, the node cap and naming run unchanged. Artists not on disk can appear, named from
  ListenBrainz. Co-listening keeps each artist's three strongest partners
  ([listening.md](listening.md#counting-a-play)). Under `both`, credits choose and place the artists, and co-listening edges
  (`GraphEdge.Kind = "listening"`, drawn dashed) are added between those already drawn; they do not
  pull the layout.
- **Cached like the count:** one entry per (credit count, user, plays fingerprint, period to the
  day, library, roles). The credit count keeps each recording's artists (`creditGraph.byRecording`)
  so the overlay costs one scan of the user's plays, not a second scan of the credits.

On the page, *Your listening* holds Period, Connect by, Only artists I play and Fewest plays, plus
*Size by* (Recordings or Plays, drawn rather than asked for). *Colour: Plays* is a one-hue
sequential ramp (`--seq-1`…`--seq-5`, five log-spaced bins relative to the most-played artist in
view; on the dark surface fewer plays sit darker), with unplayed artists as a neutral ring. The side
panel adds *Most played here*, and every tooltip names the plays.

### Drawing

`d3-force` lays the graph out, and only that: the drawing is plain SVG styled from the theme
(`CollabGraph.tsx`). The layout runs a fixed number of ticks per answer instead of animating, so
the same answer always draws the same picture. A centred graph pins its artist in the middle with
each hop on its own ring. Drag pans, the wheel zooms about the cursor, and hovering or selecting an
artist lights up their connections.

The side panel lists the same data: the strongest pairs, or the centred artist's collaborators, and
for a selected artist their connections, *Centre on this artist*, and a link to their collection
page when they have one. It is also the non-visual way in. Every artist the picture shows can be
reached from the list, and the SVG's label says so.
