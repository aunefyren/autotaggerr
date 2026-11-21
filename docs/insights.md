# Feature: insights

The **Insights** page (Music → Insights) answers questions about the library as a whole, in three
tabs: **Duplicates**, **Stats** and **Graph**.

**It reads; it does not write.** All three tabs come from the file index (`library_items`), the
collection tables and the release cache, with no MusicBrainz call and no file opened, so they
answer inline. Only one thing reads audio: checking whether two copies are the same audio. That is
a queued job, and it writes only to the index. The code lives in the `insights` package; the routes
are in `routers/insights.go`.

Both tabs read enabled libraries only (`models.InEnabledLibrary`), and the library select narrows
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
| `across_releases` | the same recording on different albums | a single also on the album, a compilation. Usually intended |

### Copies are compared within one album only

"Album" means the release group (`CollectionRelease.ReleaseGroupMBID`, falling back to the cached
release). Two editions of one album holding the same track are duplicates. A single and the album
it came from are not, even when the audio is byte-identical, because deleting either copy loses an
album. Those groups are `across_releases`, flagged `identical_audio` when two copies do share a
hash.

### What is hidden by default

- **`across_releases` groups.** They outnumber everything else in most libraries. When they are
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
- **Breakdowns:** format (from the extension), release type, decade (release-group first release
  date), genre (the album's strongest MusicBrainz genre, so each file counts once), country and
  label. Long breakdowns keep the top 12 and sum the rest into *Other*. Files whose release does not
  say are counted as *Unknown*.
- **Coverage:** ISRC known (MusicBrainz lists one for the recording, out of identified files),
  ReplayGain measured, BPM measured and identified (out of all files), plus counts of unmatched,
  failing, pinned and held files, each linking to its Items filter.
- **Catalogue against disk:** monitored catalogue albums with nothing on disk, albums on disk with
  tracks missing, and albums on disk that no catalogue lists. These read the collection tables,
  which are not split by library, so they are left out of a one-library view.

Breakdowns and play time read the **release cache only** (`modules.CachedRelease`), never the
network. A file whose release is not cached is left out of them and counted in `uncached`, and the
page says so instead of showing smaller numbers as if they were complete.

Bitrate, sample rate and bit depth are not stored anywhere, so they are not broken down. Adding them
would mean reading every file's headers at index time.

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

- **Various Artists is out by default.** It is credited on every compilation track and would
  connect everything.
- **The node cap is what keeps the graph readable.** Uncentred, it keeps the artists with the
  heaviest connections. Centred, it keeps the nearest ring first and the strongest within each ring.
  `truncated` says the cap cut some artists.
- **Only connected artists are drawn.** An artist sharing no recording with anyone is not part of a
  graph of who works with whom.

**Adding a filter** is a field on `insights.GraphOptions`, a line in `routers/insights.go`, and an
entry in `GRAPH_FILTERS` (`webui/src/pages/Insights.tsx`).

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
