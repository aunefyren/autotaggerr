# Feature: tag writing

What Autotaggerr writes into a file once it knows which MusicBrainz release and track it is.

## Engines

Dispatched by extension in `SetFileTags`:

- **FLAC** — Vorbis comments via `metaflac`, read back with `github.com/mewkiz/flac`.
- **MP3** — ID3v2.4 via `github.com/bogem/id3v2`, in-process.

`metaflac` is the only binary required at runtime (bundled in the Docker image, otherwise on
`PATH`). Other formats are not supported yet — see [wip.md](wip.md).

**Without `metaflac`, FLAC writes fail on purpose.** Reading needs nothing external, so an unchanged
FLAC is unaffected; one whose tags need changing fails with `modules.ErrMetaflacMissing`. It is
recorded as an error, not as tagged, so the first run after installing it picks the file up again.
The absence is logged once at startup (`modules.MetaflacAvailable`), not per file, and every event
that writes tags (Process, Tag files, attaching) counts these failures in its `DetailCollector` and
carries one warning naming how many there were (`components.MetaflacWarning`;
[scanning.md](scanning.md#warnings-and-skipped-libraries)). Before this, a fresh setup without
`metaflac` logged exec's `executable file not found` twice for every FLAC in the library.

MP3 used to go through `ffmpeg` / `ffprobe`, which cost more than it looked. Writing one tag
demuxed and remuxed the whole file; frames could only be addressed through ffmpeg's metadata-key
translation, so `UFID` was unreachable and the ISRC ended up in a frame *described* `TXXX`; and the
`ffprobe` read flattened a multi-value frame to its first value, which is what made the
spec-correct representation unreachable rather than merely unwritten. The frame layout was kept
byte-for-byte across the engine swap itself, so that change re-tagged nothing
(`TestNewWriterReproducesTheLegacyFrames`).

Addressing frames directly is what later made `UFID` and `TSRC` reachable
([below](#the-recording-mbid-is-written-twice-on-purpose)). Those two *are* a one-time rewrite of
every MP3 — the deliberate kind, shipped together so the cost is paid once
(`TestLegacyFfmpegFilesConvergeAfterOneRewrite`).

Settings come from the library's **tagger profile** (`models.TaggerProfile`), the DB-backed
successor to the old `autotaggerr_*` config flags.

### ID3v1 is removed, not refreshed

An MP3 write ends by truncating any ID3v1 trailer (`stripID3v1`) — the 128-byte block at the end of
the file, plus the 227-byte "TAG+" enhanced block in front of it when one is there.

The ffmpeg writer was passed `-write_id3v1 1` and rebuilt that trailer on every pass. `bogem/id3v2`
does not manage ID3v1 at all, so it copies an existing one through verbatim, and a file tagged
before and after the engine change would disagree with itself. Refreshing it is not an option worth
having: a 30-character title and a genre from a fixed list of 80 cannot represent what gets written
here, and it cannot hold an MBID — the thing every consumer below actually reads. That leaves
keeping a tag that contradicts the file or removing it, and a v1-only reader has no way to tell
which half of a contradiction is current.

It is a truncation on a file that was being rewritten anyway, so it costs nothing on the
skip-unchanged path: a stale trailer is **not** a diff and does not cause a write
(`TestID3v1DoesNotForceARewrite`). Files converge one at a time, as each is next tagged for a real
reason.

## Several values in one field

A tag field can hold several values — genres, ISRCs, the artist MBIDs behind a credit, a
release's labels and catalogue numbers. `models.FileTags` carries them as the several values they
are, and each engine decides how they reach the file:

| | how several values are written | why |
|---|---|---|
| FLAC | one Vorbis comment per value | the spec-correct form, and it costs the ffmpeg readers nothing |
| MP3 | one frame, values joined with `"; "` | the spec-correct form would hide all but the first value from them |
| MP3, `mp3_multi_value_tags` on | one frame, values separated by a null byte | ID3v2.4's own form, for libraries that are not read through ffmpeg |

That asymmetry is measured, not assumed, and `TestFFmpegJoinsRepeatedVorbisComments` /
`TestEnginesRenderTheSameValuesDifferently` pin both halves.

**On FLAC there is no trade-off.** ffmpeg's FLAC demuxer joins repeated comments itself, so two
`GENRE` comments reach anything ffmpeg-backed — Plex included — as `hip hop;rap`. Picard,
foobar2000, MusicBee, Kodi and Navidrome read the discrete values natively. Both sides win, so
this is not configurable.

**On MP3 there is**, so it is the tagger profile's `mp3_multi_value_tags`. The only spec-correct
form is a null-separated ID3v2.4 text frame (repeated text frames are forbidden by the spec, and
`bogem/id3v2` collapses them anyway), and ffmpeg reads back only its *first* value. A spec-correct
MP3 therefore shows one genre to Plex where the joined string shows several. That is a genuine
either/or between two families of consumer, not a limitation to engineer around, which is exactly
what a setting is for.

**It is off by default**, and the reason is not that the joined form is better. Autotaggerr ships a
Plex client and refreshes Plex after a write, so Plex is the setup it is most often pointed at, and
turning this on by surprise would take genres away from those libraries. Users whose players read
ID3 properly should turn it on.

**Only the writer knows which form is in use.** `GetMP3Tags` splits on the null byte
unconditionally, so a half-converted library reads correctly either way and flipping the setting
re-tags each file exactly once before converging. Tests: `TestMP3MultiValueSetting`,
`TestMP3MultiValueSettingConvergesAfterAFlip`.

**A semicolon is the only separator that works** where one is needed. Navidrome splits genres on
`;`, `/` and `,` by default; `/` and `,` are unusable regardless, since `AC/DC` and `Crosby,
Stills & Nash` are single names containing them.

Blank entries are dropped rather than written, which is not cosmetic: a blank value or a dangling
separator never matches on read-back, and so would re-tag the file forever.

**Order is meaningful and the diff compares it.** Genres are ranked by community vote and an
artist credit reads in its credited order. Values used to be sorted before being compared, which
was harmless while every field was one joined string — with real multi-value it would hide a
re-ordering, so a re-ordering would never be written. Both sides are still deduplicated
case-insensitively, so a file another tagger wrote the same genre into twice still converges.

## The two engines write the same fields

They did not always. FLAC used to keep only the first genre while MP3 joined them all, and MP3
never received the recording MBID, barcode or catalogue number — so the same album carried less
information in one format than the other. Parity is about content: the engines carry the same
values in the same order, and how many values that becomes on disk is the format's business (see
above). The differences that remain are ones the formats force:

| | FLAC (Vorbis) | MP3 (ID3) |
|---|---|---|
| Genres | `GENRE` | `GENRE` → `TCON` |
| Recording MBID | `MUSICBRAINZ_TRACKID` | `TXXX:MusicBrainz Recording Id` **and** `UFID` |
| ISRC | `ISRC` | `TSRC` |
| Date | `DATE` + `YEAR` | `TDRC` + `TXXX:year` |
| The edition's date | `RELEASEDATE` | `TDRL` |
| Track total | `TRACKTOTAL` + `TOTALTRACKS` | half of the paired `TRCK` |
| Disc total | `DISCTOTAL` + `TOTALDISCS` | half of the paired `TPOS` |
| Sort names | `ARTISTSORT`, `ALBUMARTISTSORT` | `TSOP`, `TSO2` |
| Disc subtitle | `DISCSUBTITLE` | `TSST` |
| Compilation | `COMPILATION` | `TCMP` |
| Language | `LANGUAGE` | `TLAN` |
| ASIN | `ASIN` | `TXXX:ASIN` |
| Tempo | `BPM` | `TBPM` |
| ReplayGain | `REPLAYGAIN_TRACK_GAIN` etc. | `TXXX:REPLAYGAIN_TRACK_GAIN` etc. |

Dates are written at the precision MusicBrainz gives them — `1983`, `1983-10` or `1983-10-05` —
never padded to an invented day; both `DATE` and `TDRC` accept the shorter forms
(`ParseMusicBrainzDate`). Partial dates once failed to parse, which silently left such releases
with no date or year tags at all.

MP3s also carry `TXXX:year`, `TXXX:originaldate` and `TXXX:originalyear`, artefacts of the old
ffmpeg writer that Picard does not write in ID3v2.4. They are **kept on purpose**: older Navidrome and
Kodi mappings look for `TXXX:originaldate`, and removing them would re-tag every MP3 for nothing a
player gains.

Vorbis has no single spelling everyone agrees on, hence the duplicated keys; ID3 does. The `TXXX`
spelling for the recording MBID is the one `extractFromID3v2` already read back for the `recording`
type, so writing it repaired a lookup that had never had a source.

### The recording MBID is written twice, on purpose

MP3 carries it in both `TXXX:MusicBrainz Recording Id` and `UFID` — Picard's canonical home, and the
only one a reader can identify without first agreeing on a `TXXX` description, since the owner
string (`http://musicbrainz.org`) is part of the frame.

They are two frames with **two separate diff keys** (`MusicBrainz Recording Id` and `UFID`). One key
naming both would read the same MBID into one slot twice, report drift no write could settle, and
rewrite the file on every scan forever — the failure mode this file keeps coming back to.

`UFID` is also the one frame ID shared with other taggers, so only the MusicBrainz owner is ever
touched: a foreign `UFID` is a different identifier in a different namespace and is left exactly as
it is. The owner is matched over either scheme when reading and written over `http`, so a file
tagged by something that used `https` does not read as drift.

### A `TXXX` with no description is cleared

A `TXXX` frame's *description* is its key, so one written without a description is a value naming
nothing. It is unreachable from both directions: `GetMP3Tags` trims the key, finds it empty and drops
the value, so the diff cannot see it; and `deleteUserDefinedFrame` matches by description, so nothing
can address it. `dropUnkeyedUserDefinedFrames` removes them on a write that was already happening —
like `stripID3v1`, never as a reason to write, so a library full of them is not rewritten for
something no user asked about (`TestUnkeyedTXXXFrameIsClearedButNeverCausesAWrite`).

The frames are real: editing a file's properties in Windows Explorer rewrites the whole ID3 tag
through the Windows property handler, and one file came back with its MusicBrainz recording frame
mangled into an empty description carrying the text `MusicBrainz Recording Id` as the *value*.
That same edit dropped `TPE1`, `TPE2`, `TALB`, `TSRC`, every other `TXXX` and `UFID`, and appended an
ID3v1 trailer — the next scan restored 21 fields, which is the shape to expect when a file has been
through a foreign editor. Only the descriptionless frames are dropped; a foreign `TXXX` that names
its key is another tagger's data and is left alone.

### The ISRC moved to `TSRC`

It used to live in a `TXXX` frame *described* `TXXX`, whose value carried its own `ISRC:<value>`
packing — an artefact of `-metadata TXXX=ISRC:…`, the only way the ffmpeg writer could reach a
user-defined frame at all. It is now in `TSRC`, the standard frame.

The legacy frame is still **read**, and that support is not transitional: a library tagged by an
older Autotaggerr keeps those frames until something rewrites each file, and a file nothing ever
changes is never rewritten. Both spellings decode to the same `ISRC` key, so a file mid-migration
and a file after it are indistinguishable to everything above the frame layer.

A file still carrying the legacy frame is migrated on its next write, **whether or not the ISRC
itself changed** — otherwise a file whose ISRC is already correct would keep the artefact forever,
since nothing would ever mark it as drift. The value carried across is the one read *off the file*,
not the one in the desired tags: reading the desired value would silently delete the ISRC of any
track whose MusicBrainz data no longer supplies one, turning a frame migration into a deletion.

Adding `UFID` and moving the ISRC cost **one rewrite per MP3**, once. That is the whole reason they
shipped together rather than as two passes over the library.
`TestLegacyFfmpegFilesConvergeAfterOneRewrite` pins both halves: the migration happens, and it
happens exactly once.

`ASIN`, `COMPOSER` and `AUTHOR` are written by neither. `BuildFileTags` has never populated them,
so listing them only ever cleared another tagger's value once `remove_values` was on.

## Tempo is the one tag MusicBrainz does not supply

`BPM` (FLAC) and `TBPM` (MP3) come from the stored tempo analysis ([tempo.md](tempo.md)), not from
the release. `BuildFileTags` knows nothing about them; `modules.TempoTagValue` fills
`FileTags.BPM` afterwards, on the write path and in the tag-diff preview alike, so the preview shows
the BPM a scan would write.

The library decides (*Libraries → edit*, beside the analysis opt-in). These settings used to be on
the tagger profile; they moved so both halves of the feature are set in one place, and the
`move_bpm_to_library` data migration copied each profile's values onto its libraries. The tagger
still has the last word in one direction: a profile that writes no tags writes no BPM either.

| `bpm_write` | What happens |
|---|---|
| `""` (off, the default) | Nothing. The tempo is stored only. |
| `fill_empty` | Written only where the file has no BPM. `0` counts as none — several tools write it to mean "not analysed". |
| `overwrite` | Always written; another tool's value is replaced. |

`bpm_min_confidence` (default 0.5) is the confidence a result needs to be written at all.

- **A whole number**, on both engines. `TBPM` is defined as an integer, every reader expects one, and
  a stored 104.96 written as `105` reads back exactly, so the diff converges on the first write
  (`TestBPMRoundTrip`).
- **No value means no key**, not an empty one. The builders add `BPM` only when there is a value,
  and fill-empty works by *removing* the key once the file is seen to have one
  (`applyTempoPolicy`). Either way the diff never looks at a BPM it has nothing to say about — so
  `remove_values`, which turns an empty wanted value into a deletion, can never clear a BPM another
  tool wrote (`TestBPMWriteModesOnRealFiles`).
- **A stored result is checked against the audio** before it is written, when the file's size or
  mtime have moved since the analysis. That is almost always a tag write, and the value stands; but a
  replaced file would otherwise be tagged with its predecessor's tempo.

## Genres

`selectGenres` (`modules/files.go`) ranks a release group's genres by community vote and keeps the
top `max_genres` (tagger profile; default 5, matching Picard).

- **The sort is what makes the cap meaningful.** MusicBrainz returns genres unordered, so
  truncating the raw list would discard the popular genre as readily as the obscure one.
- **The tie-break is by name, and it is about idempotency, not taste.** Equally-voted genres
  returning in a different order on a later fetch would produce a different `GENRE` string and
  re-tag the whole release group for nothing.
- **Casing is MusicBrainz's.** Genres are canonically lower case there (`acid jazz`,
  `afro-cuban jazz`). Title-casing them — which is what Lidarr does — is a transformation away
  from the source that also mangles the names a naive implementation cannot know about: `UK garage`
  becomes `Uk Garage`, `R&B` becomes `R&b`. Deliberately not done, and not configurable.

## Album artists

`ALBUMARTIST` holds the **first** credited artist only, because Plex has no concept of several and
renders a joined string as one artist literally named `A; B`. `ALBUMARTISTS` carries the whole
credit beside it, exactly as `ARTISTS` sits beside `ARTIST` — players that understand it (Navidrome
reads `albumartists` natively and prefers it) get the full credit, and the rest ignore it.

**Multi-value is per field, and this is the field it stops at.** Writing several `ALBUMARTIST`
comments on a FLAC would not help: ffmpeg joins them back with `;` on read, which is exactly how
Plex ends up with one artist named `A;B`. So `ALBUMARTIST` stays single-valued on both engines
even though the format could carry more.

Before `ALBUMARTISTS` existed the names disagreed with `MUSICBRAINZ_ALBUMARTISTID`, which has
always listed every credited artist's MBID.

## Picard's fields, and what reads them

Every field Picard writes from the release itself, without relationship lookups, is written. The
spellings are Picard's tag mapping, compared side by side with what Jellyfin's prober and Plex
read:

- **Sort names.** `ARTISTSORT` is the track credit by each artist's MusicBrainz sort name, joined
  like `ARTIST` (`Nicks, Stevie & Buckingham, Lindsey`). `ALBUMARTISTSORT` is the first album
  artist's, because `ALBUMARTIST` is the first only. Plex reads both, but only when it first ingests
  an artist, so changing them later needs Plex to forget the artist. A blanked redundant `ARTIST`
  takes its sort name with it.
- **`DISCSUBTITLE`** is the medium's own title; **`LANGUAGE`** is the release's text language (ISO
  639-3); **`ASIN`** is the release's.
- **`COMPILATION`** is `1` for a release credited to Various Artists, which is Picard's meaning since
  1.3. It is not MusicBrainz's *compilation* secondary type, which a one-artist greatest-hits album
  also has. Otherwise it is empty, so `remove_values` clears a stale flag.
- **Both total spellings** on FLAC, `TRACKTOTAL` and `TOTALTRACKS`, as with the disc total.

ReplayGain is measured and written by its own verb; see [replaygain.md](replaygain.md).

## Credits from relationships

Composer, lyricist, writer, arranger, conductor, producer, engineer, mixer, remixer, DJ-mixer,
performers and the work come from MusicBrainz's relationships. `modules.TrackCredits` is a port of
Picard's `_relations_to_metadata` with **"Use track and release relationships" on**. That is
deliberate: Picard's default is release relationships only, which writes producer and engineer
when they are credited on the release, and almost never a composer, since composers live on the
work a recording performs. Jellyfin reads `Composer`.

- **Where credits come from.** The release lookup adds `artist-rels`, `recording-level-rels`,
  `work-rels` and `work-level-rels`. That gives the release's own credits, then each recording's,
  then the works it performs (a `performance` relationship) with the works' credits. Values keep
  that order, with exact duplicates dropped, as Picard's `add_unique` does.
- **Mapping.** Relationship types map to tags as in Picard's `_ARTIST_REL_TYPES`, without
  `director` (a video credit), and with Picard's commented-out `mastering` and `recording` left out
  too.
- **Performers.** A performance (`instrument`, `vocal`, `performer`) becomes `PERFORMER=Name
  (role)`, its role built as Picard builds it: qualifying attributes first, then instruments,
  "and" before the last (`guest bass guitar and lead vocals`). With no instrument the role is
  `instruments` or `vocals`. Instrument names are standardised, Picard's default.
- **Special cases.** A DJ-mix credit naming a medium (`medium 2`) applies to that disc only. A work
  performed as `instrumental` takes no lyricist. Names follow the profile: the artist's current
  name, or the name the relationship credits them as (`target-credit`).

| | FLAC (Vorbis) | MP3 (ID3v2.4) |
|---|---|---|
| Composer | `COMPOSER`, `COMPOSERSORT`, `MUSICBRAINZ_COMPOSERID` | `TCOM`, `TSOC`, `TXXX:MusicBrainz Composer Id` |
| Lyricist, writer | `LYRICIST`, `WRITER` | `TEXT`, `TXXX:Writer` |
| Conductor, remixer | `CONDUCTOR`, `REMIXER` | `TPE3`, `TPE4` |
| Arranger, engineer, producer, DJ-mixer, mixer | `ARRANGER`, `ENGINEER`, `PRODUCER`, `DJMIXER`, `MIXER` | `TIPL` pairs: `arranger`, `engineer`, `producer`, `DJ-mix`, `mix` |
| Performers | `PERFORMER=Name (role)` | `TMCL` pairs: instrument, name |
| Work | `MUSICBRAINZ_WORKID`, `WORK` | `TXXX:MusicBrainz Work Id`, `TXXX:WORK` |

**`TIPL` and `TMCL` are lists of pairs**, `role\0name\0role\0name`. Each pair is one desired value
joined by a null byte, so the diff compares whole pairs and its case-insensitive dedup never folds
the second producer into the first. They are always null-separated, whatever
`mp3_multi_value_tags` says: a joined string would be meaningless. `GetMP3Tags` pairs them up again
on the way back, and the diff shows a pair as `role: name`.

**A release cached before relationships were fetched writes no credit keys at all.** The cached
payload is marked when it was fetched with them (`RelationsIncluded`, kept in the cache). Without
the mark, its empty credits would let `remove_values` clear a composer another tagger wrote, on the
strength of data that was never asked for. Releases refresh within 7 to 14 days, and the refresh
compares the whole payload, so each counts as *changed upstream* once. That re-tag is how credits
reach the files.

MusicBrainz leaves relationships out of a release lookup for very large releases; Picard then
fetches each recording on its own. That is not done here: those tracks get no credits, rather than
a request per track.

Tests: `TestTrackCredits`, `TestCreditsOnlyFromARelationshipFetch` and `TestCreditsRoundTrip`
(real files, two producers kept as two pairs, both multi-value settings).

## What Jellyfin reads, measured

Jellyfin 12.4.0 was run in Docker against FLAC and MP3 files written with every tag in question,
and its API read back:

- **Track artist.** With no `ARTIST`, a track's own artist list is empty (`Artists: []`). It is
  still listed under its album artist: Jellyfin's artist filter matches album artists too. With
  *ignore redundant contributing artists* on (the default profile), single-artist albums have no
  track artist, and only the track row's artist column shows it. Turn the option off if that
  matters on Jellyfin; Plex falls back to the album artist.
- **Multiple artists.** Only `ALBUMARTIST` and `ARTIST` are read, so a joint `ARTIST` becomes one
  artist named `A & B`. With the library option *Prefer non-standard artists tag* on, `ALBUMARTISTS`
  and `ARTISTS` are read as separate artists, for both fields.
- **Dates.** `DATE` sets the album's and the song's year and premiere date; `ORIGINALDATE` is
  ignored. Hence `original_date_as_date` (see [Dates](#dates)).
- **Credits.** `COMPOSER` becomes a Composer person. `LYRICIST` and `PERFORMER` are not read.
- **ReplayGain.** `REPLAYGAIN_TRACK_GAIN` becomes the song's normalisation gain. The album gain
  showed nothing without Jellyfin's own LUFS scan.
- **Ignored.** Artist sort tags (Jellyfin derived its own `SortName`), `COMPILATION` and
  `DISCSUBTITLE` changed nothing in the API. They are written for Plex, Picard, Navidrome and the
  rest.

## Release type

Two forms, chosen per tagger profile:

- **The Plex shape (the default, recommended for Plex).** One value: the primary type when there
  is no secondary type (`album`, `ep`), and `album; live` (or soundtrack, compilation, remix, demo)
  when the first secondary type is one of those, whatever the primary type. That is how Plex sorts
  albums onto its Live, Compilations and Soundtracks shelves; it does not read `ep; live`. Only the
  first secondary type counts. `PlexReleaseType` is exactly what was always written, so profiles
  predating the choice re-tag nothing.
- **MusicBrainz's own (`musicbrainz_release_type`).** The primary type and every secondary type as
  separate values, `ep` and `live`, as Picard writes them. On FLAC that is one comment per value. On
  MP3 the values are joined like any multi-value field.

**Remix editions** are typed as remixes in both forms unless the profile turns it off
(`ignore_remix_editions`). The rule: an edition whose title or disambiguation says *remix*, for the
remix editions MusicBrainz files in a release group that is not itself typed remix. In the Plex
shape that is `album; remix`. In MusicBrainz's it is a `remix` value beside the others, added once.

Both are stored the other way up from how the editor shows them ("Write release types the way Plex
reads them", "Mark remix editions as remixes"), so a row predating them keeps its behaviour.

## Dates

`DATE` and `YEAR` are the edition's release date by default, as in Picard. `ORIGINALDATE` and
`ORIGINALYEAR` are the album's first. On MP3 those are `TDRC` and `TDOR`. `RELEASEDATE` is always
the edition's, on MP3 in `TDRL`.

**Players take the album's year from `DATE`.** Jellyfin reads no original date at all, and Plex
users report the reissue's year winning. So a 2011 remaster of a 1977 album shows as 2011. The
tagger profile's `original_date_as_date` writes the album's first date as `DATE` and `YEAR`
instead. Nothing is lost: `ORIGINALDATE` keeps it as well, and `RELEASEDATE` keeps the edition's.
A release group with no known first date keeps the edition's date rather than none. Picard users
get the same with `$set(date,%originaldate%)`.

**Settings added after profiles existed are `omitempty` in `TaggerSettings`** while they mean what
was already written: `single_record_label`, `musicbrainz_release_type`, `ignore_remix_editions` and
`original_date_as_date`. `Tagger.Fingerprint` hashes the settings as JSON, so a new field present
at its zero value would have changed every profile's fingerprint and re-processed every file.
Fields that add new tags (the sort names and the rest) need no such care. Skip-unchanged does not
look at the tag set, so they reach files the next time each is processed, which every release does
for every file. Tests: `TestPlexReleaseType`, `TestReleaseTypes`, `TestBuildFileTagsPicardFields`,
`TestPicardFieldsRoundTrip`, `TestSingleRecordLabelIsNotInTheFingerprintWhileOff`.

## Record labels

`LABEL` (FLAC) and `TPUB` (MP3) carry every label MusicBrainz lists for the release, **in
MusicBrainz's order**, which is the order its editors entered them. It is not alphabetical:
*Oxnard*'s CD lists Aftermath Entertainment, 12Tone Music, OBE. Catalogue numbers go to
`CATALOGNUMBER` the same way, collected separately so an entry with only one of the two never
shifts the other list.

**Plex has room for one.** Its Record Label is a single value, and given several labels ffmpeg
hands it the joined string, `APESHIT;EMPIRE;Mass Appeal`. Plex shows that as one label, and groups
by it as one label that no other album shares. Genres do not suffer this, because Plex splits a
joined genre string itself; it does not split a label. That is the `ALBUMARTIST` situation, and the
answer is the same shape. The tagger profile's `single_record_label` writes only the first label,
on FLAC and MP3 alike. That is the first one *named*: an entry with a catalogue number and no label
is skipped, not written as an empty tag. Catalogue numbers stay in full, since Plex does not show
them. It is off by default, because the full list is what every other reader wants and is what
MusicBrainz says.

`SingleRecordLabel` is `omitempty` in `TaggerSettings`, and the reason is the fingerprint.
`Tagger.Fingerprint` hashes the settings as JSON, so a new field present while off would have
changed every profile's fingerprint and re-processed the whole library for a setting nobody had
turned on. Switching it on changes the fingerprint, and files re-tag once. Tests:
`TestSingleRecordLabel`, `TestSingleRecordLabelIsNotInTheFingerprintWhileOff`.

## Diff before write

`modules.BuildFileTags` computes the desired tags and `DiffFileTags` compares them with what is on
disk. Unchanged files are skipped and reported separately, so a scan's "N changed" count means
something.

The diff is also the UI's signature element: `GET /library-items/:id/tags` powers the tag-diff
detail view (current → desired, per tag) described in [style-guide.md](style-guide.md).

### A reported change renders its value count

Both sides of a reported change go through `utilities.DescribeTagValues`, which leaves a single
value bare and renders several as a quoted list. It is display only — nothing it returns is written
to a file — and it exists because the alternative is unreadable on exactly the file this section is
about.

**Only a changed row is rendered that way.** The tag-diff view lists every tag, changed or not, and
on an unchanged row the count cannot differ — both sides hold the same values in the same shape — so
those stay joined rather than putting brackets around every multi-value tag the file already agrees
about (`TestDiffFileTagsListsValuesOnlyWhereTheyChanged`). A write reports only what it changed, so
`SetFlacTags`/`SetMP3Tags` have no such split.

A field's value *count* is part of the change. One Vorbis comment holding
`Universal Music Special Markets; Intrada` and two comments holding one label each are a real
difference, and the difference the first pass over a library tagged by something else makes.
Rendering both sides with `JoinTagValues` put them back into the same string, so that change
reported as `LABEL: "Universal Music Special Markets; Intrada" → "Universal Music Special Markets;
Intrada"` — a write that looks like a diff which cannot settle, on a file that in fact converges on
the next pass. It applies to MP3 for the same reason: with `mp3_multi_value_tags` on, the move from
a joined frame to a null-separated one is invisible once both sides are joined back up. Tests:
`TestDescribeTagValues*`, `TestJoinedMultiValueTagIsReportedLegiblyAndConvergesOnce`.

### Removing a value

A desired value that is empty means "Autotaggerr has nothing to say about this tag", not "clear it".
Both `DiffFlacTags` and `DiffID3Tags` therefore skip empty values unless the tagger profile's
`remove_values` is on; with it on, the empty value becomes a change and the tag is removed
(`metaflac --remove-tag`, or deleting the ID3 frame). The two engines are deliberately kept in
step — the profile is one promise, and it used to hold on FLAC while ID3 quietly ignored it.

**Every key in the change set must be written.** The MP3 writer's reported diff is derived from the
change set rather than from what it did, so a key that is reported but skipped is reported as
written while nothing happens, and is re-reported on every scan forever. The paired frames (`TRCK`,
`TPOS`) each hold a number and a total in one frame and are cleared together. Deleting one `TXXX`
means reading the others out and putting them back, since a frame ID can only be deleted whole —
`deleteUserDefinedFrame` exists for that and not by preference, and
`deleteMusicBrainzUFIDFrame` does the same for `UFID`, where the thing being preserved is other
taggers' identifiers rather than other descriptions. Regression test:
`TestMP3RemoveValuesClearsAndConverges`.

## Idempotency is the property that matters

If the desired tags never equal what is read back, the file is rewritten on **every** scan forever.
That has happened once, and the shape of the bug is worth remembering:

> **Multi-value ISRC on MP3.** ISRC used to be packed into a single `TXXX` frame as `ISRC:<value>`
> (it is in `TSRC` now, but the legacy frame is still read), and the
> decoder split the frame value on `;` *before* the `KEY:value` split — so a `"; "`-joined
> multi-ISRC string (common on singles and featured tracks) read back as only its first value. The
> diff never converged. Fixed by splitting on the **first colon only** (`SplitN(val, ":", 2)`); the
> on-disk format never changed, so existing files converged with no migration write. Regression
> test: `TestMP3MultiISRCIdempotent`.

When adding a tag, write a round-trip test: set it, read it back, and assert the diff is empty.

## The disc guard

The other way tags fail to converge is not an encoding bug but a **correlation** one, and it has
one recurring shape: a multi-disc release that carries the same recording at the same position on
two mediums (an expanded soundtrack whose disc 1 is the long score and whose disc 2 is the original
album, both opening with the same cue). The two candidate tracks are indistinguishable by title,
position, recording and length — the file's `CD 02` folder is the only evidence which one it is.
Resolve it to the other disc and `DISCNUMBER` / `TRACKTOTAL` / `MUSICBRAINZ_RELEASETRACKID` are
written against a file that keeps resolving back the other way: an endless retag.

Two defences, at different depths:

- **`FindTrackFileByPath` matches on the media folder** (`modules/lidarr.go`), not just album +
  basename, because a multi-disc release routinely repeats a basename across discs. Folder names are
  compared by their **disc number** when both carry one, so `CD 02` on disk still matches `CD2` in
  Lidarr's stored path. If two trackfiles still fit, it returns no match rather than taking the
  first — a coin flip between two discs is the failure it exists to prevent, and an unmatched file
  is visible and fixable. Lidarr's paths are split with the `utilities.*PathAny` helpers, which
  accept either separator: a Windows-hosted Lidarr stores `D:\Music\Artist\Album\01.flac`, and on
  Linux `filepath` reads that as one segment, so no folder (and, earlier, no artist) ever matched.
- **`verifyDiscFolder` is the last check before a write** (`modules/files.go`). Whatever resolved the
  file — Lidarr, embedded tags, a stale index row — the medium of the chosen track is compared with
  the file's own disc folder, and the write is refused with `ErrDiscMismatch` when they disagree.

The guard is deliberately narrow, because folder numbering does **not** have to agree with
MusicBrainz medium numbering: a release whose medium 1 is a bonus DVD legitimately has its `CD 1`
folder on medium 2. So a bare number disagreement is not evidence of anything. It refuses only when
the disagreement comes with a look-alike — the medium the folder names holds a track at the same
position with the same recording (or the same title). Manual correlations are exempt: someone looked
at the file and said which track it is, and that outranks the folder it sits in.

**A folder that names no disc** — a flat album folder, or one `discFolderPattern` does not read —
used to leave the guard with nothing to check, and the look-alike case is precisely where nothing
else tells the candidates apart. There the guard falls back to the audio
(`verifyDiscByDuration`): MusicBrainz states each track's length, and the file's own duration
(`modules/duration.go` — FLAC's STREAMINFO, or a count of MP3 Layer III frames) says which look-alike
it is. The comparison is **relative, never a tolerance**: encodes drift from the stated length by
seconds (Jerry Goldsmith's *Alien* plays 4:19 against a stated 4:12), so a tolerance tight enough
to matter would refuse right answers. The resolved track is refused only when a look-alike is
clearly closer — under half the gap, and at least 5 seconds nearer. *Alien*'s two "Main Title"
cues, 4:12 and 3:34, are that case exactly. Every unknown passes: no look-alike, no stated length,
or a file whose duration cannot be read. A folder that does name a disc keeps deciding by itself.

A refusal surfaces as a failed file in Activity naming both discs and the release, which is the
point: the fix is in the manager's link (or the folder's name), and neither is Autotaggerr's to
guess. Tests: `modules/disc_guard_test.go`, `modules/disc_duration_test.go`,
`TestLidarrFindTrackFileByPath*`.

## A track that left its release

When MusicBrainz rebuilds a release's tracklist without changing the release ID, the track ID a file
carries is in the payload no more. Under a manager that clears itself — the manager hands out the
new pair on the next pass. A file identified by its **own tags** had no way out: every pass re-read
the vanished ID from the very file it was trying to repair, and failed identically forever.

`rematchRebuiltTrack` (`modules/files.go`) re-matches those files, and only those —
`CorrelationSourceTags`, `Fingerprint`, or a previous re-match. Never a manual correlation (a person
chose it) and never a manager's. Two ways, each refusing anything short of one answer:

1. **By recording**, when exactly one track has the file's recording. A rebuild re-issues track IDs
   and keeps recordings.
2. **By position, confirmed by title**: the disc and track number in the file's own tags name a
   track, and that track has the file's title. Position alone is not enough — a rebuild can insert
   or reorder tracks — and title alone repeats on plenty of albums.

A hit is written under the new IDs and recorded as `CorrelationSourceRematched`, so re-matches can be
found and checked afterwards. `TagCorrelatedFile` hands the updated correlation back, which is what
lets the index (Process and Tag files alike) store the identity now in the file rather than the one
that no longer exists. A refusal fails with a message saying neither signal named one track, and to
attach the file by hand. Tests: `modules/track_rematch_test.go`.

## Related

- [artist-credit-tagging.md](artist-credit-tagging.md) — how a MusicBrainz artist credit (including
  featuring artists) becomes the artist string.
- [scanning.md](scanning.md) — when tagging runs.
