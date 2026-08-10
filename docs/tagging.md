# Feature: tag writing

What Autotaggerr writes into a file once it knows which MusicBrainz release and track it is.

## Engines

Dispatched by extension in `SetFileTags`:

- **FLAC** — Vorbis comments via `metaflac`, read back with `github.com/mewkiz/flac`.
- **MP3** — ID3v2.4 via `github.com/bogem/id3v2`, in-process.

`metaflac` is the only binary required at runtime (bundled in the Docker image, otherwise on
`PATH`). Other formats are not supported yet — see [wip.md](wip.md).

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
| Release date | `DATE` + `RELEASEDATE` | `TDRC` |
| Disc total | `DISCTOTAL` + `TOTALDISCS` | half of the paired `TPOS` |

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

## Diff before write

`modules.BuildFileTags` computes the desired tags and `DiffFileTags` compares them with what is on
disk. Unchanged files are skipped and reported separately, so a scan's "N changed" count means
something.

The diff is also the UI's signature element: `GET /library-items/:id/tags` powers the tag-diff
detail view (current → desired, per tag) described in [style-guide.md](style-guide.md).

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
  is visible and fixable.
- **`verifyDiscFolder` is the last check before a write** (`modules/files.go`). Whatever resolved the
  file — Lidarr, embedded tags, a stale index row — the medium of the chosen track is compared with
  the file's own disc folder, and the write is refused with `ErrDiscMismatch` when they disagree.

The guard is deliberately narrow, because folder numbering does **not** have to agree with
MusicBrainz medium numbering: a release whose medium 1 is a bonus DVD legitimately has its `CD 1`
folder on medium 2. So a bare number disagreement is not evidence of anything. It refuses only when
the disagreement comes with a look-alike — the medium the folder names holds a track at the same
position with the same recording (or the same title). Manual correlations are exempt: someone looked
at the file and said which track it is, and that outranks the folder it sits in.

A refusal surfaces as a failed file in Activity naming both discs and the release, which is the
point: the fix is in the manager's link (or the folder's name), and neither is Autotaggerr's to
guess. Tests: `modules/disc_guard_test.go`, `TestLidarrFindTrackFileByPath*`.

## Related

- [artist-credit-tagging.md](artist-credit-tagging.md) — how a MusicBrainz artist credit (including
  featuring artists) becomes the artist string.
- [scanning.md](scanning.md) — when tagging runs.
