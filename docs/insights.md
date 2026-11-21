# Feature: insights

The **Insights** page (Music → Insights) answers questions about the library as a whole. It has two
tabs today, **Duplicates** and **Stats**. A collaboration graph is planned as a third (#46).

**It reads; it does not write.** Both tabs come from the file index (`library_items`), the
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
