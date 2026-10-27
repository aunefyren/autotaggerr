# Feature: naming templates

Where a file belongs under its library root, rendered from MusicBrainz data by the library's
**tagger profile**. Lidarr names and moves its files; this is the half of that job Autotaggerr
needs before it can do the same. [File import](import.md) places files by it; an *Organise* verb
over the existing library will be its second consumer and does not exist yet. Beyond import, the
templates are validated, previewed in the profile editor, and shown as a `PATH` row on an item's
detail — **nothing moves a file already in a library.**

## One template per level, not one template with slashes

A profile holds four templates, each one segment of the layout the rest of the app assumes,
`<root>/<ARTIST>/<ALBUM> (<YEAR>)/[<MEDIA FOLDER>]/<TRACKS>`:

| Field | Default | Renders |
|---|---|---|
| `naming_artist_folder` | `{Artist Name}` | `Fleetwood Mac` |
| `naming_album_folder` | `{Album Title} ({Release Year})` | `Rumours (1977)` |
| `naming_media_folder` | `CD {medium:00}` | `CD 02` — only for 2+ discs of audio |
| `naming_track_file` | `{track:00} {Track Title}` | `02 Dreams.flac` — extension kept |

The split is the structural guarantee. Matching a file to Lidarr or Plex no longer depends on it —
both compare the file's path below the root, at any depth
([media-manager.md](media-manager.md#matching-does-not-trust-where-a-file-sits)) — but the layout is
still what the rest reads. Everything that reads it counts path segments: the
collection's artist folders and the per-artist actions read segment one
([collection.md](collection.md)), Lidarr's track lookup compares album and disc folders
(`FindTrackFileByPath`), folder attach and the disc guard read the disc folder. A template that may
not contain a separator cannot change how many segments there are, so a free-form `{Release Year}/
{Album Title}` that would put the year where the album is expected is rejected rather than
supported. An empty stored template means its default, and the editor stores a template equal to
its default as empty, so a profile row predating the columns names files as the layout always has.

**Naming is not part of `TaggerSettings`.** Those feed `Tagger.Fingerprint`, which skip-unchanged
compares per file; where a file lives changes nothing about its tags, so folding naming in would
make one template edit re-tag the whole library. `TestNamingIsNotPartOfTheFingerprint` holds it.

## Lidarr's tokens, with Lidarr's meanings

Token names are Lidarr's wherever Lidarr has the token, so a Lidarr naming format pastes in and
renders what Lidarr would — a library Lidarr named previews as *unchanged*. That decides two
meanings that would otherwise read differently in MusicBrainz's vocabulary:

- **`{Album Title}` and `{Release Year}` are the release-group's** (Lidarr's album is a
  release-group). The edition's own are `{Edition Title}` and `{Edition Year}`, in the UI's word for
  a release.
- **`{Artist Name}` is the release-group's first credited artist, by current name**, falling back
  to the release's credit only when the group has none — the same authority as
  [whose album it is](collection.md#whose-album-it-is-the-release-groups-credit-not-the-releases).
  A pressing still carrying an old credit mid-migration must not land in a second artist folder.

The spelling rules are Lidarr's too: `{artist name}` renders lower case and `{ARTIST NAME}` upper;
`{Artist.Name}` joins the words with the separator; a prefix and suffix inside the braces
(`{ (Album Disambiguation)}`) are written only when the value is not empty; numbers take a padding
format of zeros (`{track:00}`). The full list is `modules.NamingTokens()`, served by `GET /naming`.
It is not every Lidarr token — an unknown one is a save error naming it, never a silent blank.

### Clean and The forms

Lidarr's `{Artist CleanName}`, `{Album CleanTitle}`, `{Edition CleanTitle}` and `{Track CleanTitle}`
are the name without punctuation (`cleanTitle`): `&` spelled "and", slashes as spaces, brackets,
apostrophes and quotes removed, a dash standing alone dropped, and a colon, comma, `?` or `!`
ending a word dropped — *Guns N' Roses* is `Guns N Roses`, *Title: Sub* is `Title Sub`. Accented
letters are kept. `{Artist NameThe}` and `{Album TitleThe}` move a leading *The*, *A* or *An* to the
end, ahead of any trailing parenthetical (`Tribe Called Quest, A (live)`). Both follow the
Sonarr-family `FileNameBuilder`, like the cleaning rules below, and the same caveat applies.

## Two editions of one album

`{Album Title}` and `{Release Year}` are the album's, so the default album folder renders the same
for every edition: a library holding two pressings has to name the edition to keep them apart.
`{ (Edition Disambiguation)}` does when MusicBrainz has a comment, which a plain reissue often lacks,
so the album level also has `{Edition Year}`, `{Edition Country}` (MusicBrainz's code: `US`, `XE`),
`{Edition Format}` (`CD`, `2xCD`, `CD + DVD` — ASCII `x`, since a path gets typed into a shell) and
`{Edition CatalogNumber}` (the first label's, skipping MusicBrainz's `[none]`). For example
`{Album Title} ({Release Year}){ [Edition Year]}{ [Edition Country]}`.

Nothing is added to the default: it is Lidarr's, a Lidarr-named library has to preview as unchanged,
and one edition per album is what most libraries hold. Instead the import refuses to put a second
edition in a folder that already holds one (`other_edition`, see
[import.md](import.md#conflicts-and-what-replace-means)), naming the tokens above as the fix.

## Tokens read from the file

Three tokens are not MusicBrainz's. They describe the file being named, so `RenderLibraryPath`
takes a `NamingFile` beside the release: its extension, its audio, and its release group.

| Token | Renders | From |
|---|---|---|
| `{MediaInfo AudioCodec}` | `FLAC`, `MP3` | the file's headers (`ReadAudioProperties`): FLAC's STREAMINFO, an MP3's first frame |
| `{MediaInfo AudioBitsPerSample}` | `16`, `24`; **empty for MP3**, which has no depth | FLAC's STREAMINFO |
| `{Release Group}` | `GRP` | the download the file came from (`LibraryItem.ReleaseGroup`) |

`{Release Group}` keeps **Lidarr's** meaning: the group that put out the download, as in
`Artist-Album-WEB-FLAC-2014-GRP`. It is not MusicBrainz's release group, which is the album and
is `{Album Title}`. The codec and depth are read from the file rather than told by its extension,
so a misnamed file is named for what it is. Only the headers are read, and only when a template
uses a MediaInfo token (`NamingUsesAudio`), so a library whose templates do not use one opens no
file to plan an Organise.

**All three belong to the track file.** One album folder can hold files that differ in each of
them, so a folder named by one would split an album. Validation refuses them in the folder
templates, as it does `{track}`.

**Unknown is not empty.** An MP3's depth and a download with no group are known to be empty. They
render empty, and their brackets collapse like any empty token's. A value nobody knows is
different. Files indexed before release groups were recorded have a nil `ReleaseGroup`, and some
files' headers cannot be read. Rendering either as empty would rename `02 Song-GRP.flac` to
`02 Song.flac`. So a template that uses such a token fails for that file with a
`NamingUnknownError`. Organise lists the file as skipped ("its {Release Group} is not known, so it
is not renamed"). The item's `PATH` row shows the reason instead of a path.

Where the release group comes from:

- **An import** parses it from the album folder's name, or from the download's when the folder's
  gives none (`ParseReleaseGroup`, `WithDownloadReleaseGroup`). The review shows it in an editable
  **Release group** field, because it is a guess, and the confirmed value is what the plan renders
  and the index records (`""` when cleared). The parser errs towards nothing. For a scene name it
  takes the last hyphenated part, and needs three parts, so *Jay-Z* is an artist. For a spaced name
  it takes a part hyphenated onto the last bracket (`… [FLAC]-GRP`). Years and format words such as
  `WEB` or `FLAC` are never a group.
- **Lidarr** reports it on each track file (`releaseGroup`). The correlation carries it, and
  processing records it, pinned files included, since it describes where a file came from rather
  than which track it is. A track file cached before this was read has none, which reads as
  unknown.
- A source that does not know the group leaves the recorded one alone. Nothing clears it.

## A token may only be used at its own level or below

`{Album Title}` in the artist folder would give one artist a folder per album, and `{track}` in
the album folder a folder per track. Each token declares the first template it may appear in, and
validation rejects it above that. This is what keeps "an artist folder holds every album" true
whatever the templates say.

## The disc folder is read back by the code that reads disc folders

The disc template must contain `{medium}`, and validation renders it for **disc 2** and reads it
back through `discNumberFromFolderName` — the one recogniser Lidarr's disc matching
(`mediaFoldersAgree`), folder attach (`MapFilesToTracks`), the disc guard and the AcoustID hint all
use. Disc 2 rather than disc 1, so a folder that only reads as "1" by accident does not pass. A
template the app could not read back as the right disc cannot be saved.

That recogniser was widened for this from `cd|disc|disk` to MusicBrainz's medium formats (vinyl,
cassette, digital media, SACD, DVD, Blu-ray, …), because Lidarr's default disc folder is
`{Medium Format} {medium:00}` — rejecting it would have refused the one format a Lidarr user is
most likely to paste. `lp` is deliberately not on the list: an album called *LP1* is not disc one
of anything. `isMediaFolder` (the AcoustID path hint) now uses the same recogniser rather than its
own prefix list.

**Only discs that carry audio count** towards "more than one disc", by the
[video rule](collection.md#a-video-track-is-not-a-track-you-are-missing): a CD+DVD whose DVD is all
video is a one-disc album and stays flat. The file's own medium always counts, so a file attached
to a video track still gets a folder of its own rather than sitting among disc one's tracks.

The track file must contain `{track}`, or two tracks of one disc share a name. A track template
that does not *start* with the number is accepted with a warning: folder attach reads a leading
number and otherwise pairs by sort order — and Lidarr's own default
(`{Artist Name} - {Album Title} - {track:00} - {Track Title}`) is exactly that case.

## Cleaning a rendered name

`modules.CleanPathSegment`, applied to each rendered segment, follows Lidarr's defaults so a
Lidarr-named library is not renamed by them:

- `\` and `/` become `+` (`AC+DC`), `?` becomes `!`, `*` becomes `-`, `< > | "` are dropped;
- `: ` becomes ` - ` and a bare `:` becomes `-`; control characters are dropped; whitespace runs
  collapse;
- **leading and trailing dots and spaces are trimmed.** Trailing ones Windows refuses; leading ones
  make a folder hidden on Linux and macOS, and Plex skips hidden folders — *...And Justice for All*
  would vanish from the player. Dots inside a name (`02 ...And Justice for All.flac`) stay;
- a Windows reserved name (`CON`, `NUL`, `COM1`…) gets a trailing `_`;
- each segment is capped at 255 bytes, cut on a rune boundary, with the extension kept whole.

Empty brackets and dangling separators are removed **only when a token rendered empty**, because
`()` or a trailing dash in a real title is the title. A segment that renders empty altogether is an
error (`NamingEmptyError`), never an empty path segment; validation renders every template against
the built-in samples to catch a template made only of tokens a release may lack.

Whether `/ → +` and the colon rule match a given Lidarr install was taken from the Sonarr-family
`FileNameBuilder` defaults, not from a Lidarr library side by side; if a Lidarr-named library
previews moves for these characters, this list is what to correct.

## Where it is shown

- **Profile editor** (*Tagger profiles → Edit → Naming*): the four templates, opened on their
  effective values, with errors and warnings under each and a live preview. The preview is
  `POST /naming/preview`, which validates and renders unsaved templates against four built-in
  sample releases (a plain album, a disc of a double album, a name with a slash, a name with
  leading dots) — built in so a keystroke costs no MusicBrainz request and validation does not
  depend on the cache. It answers 200 for a template that would be rejected. Save is disabled while
  the preview reports an error, and `POST`/`PUT /tagger-profiles` reject the same templates with a
  400 (`checkProfileNaming`), so the API holds the rule without the UI.
- **Item detail**: a `PATH` row above the tags, in the tag diff's colours —
  `components.ComputeItemPath`, the file's own correlation rendered through the same function.
  Under a **Lidarr manager** it carries a note that the move will not be made: Lidarr tracks its
  files by path, and moving one would orphan Lidarr's record of it. The proposal is still shown,
  because it is what the profile says.

## Organise

**Organise** moves a library's, an artist's or an album's files to where their library's naming
templates put them (*Libraries → Organise*, and the same on the artist and album pages). It is two
steps, always, and the first moves nothing:

1. **The dry run** (`POST /organise/preview`, `process.Runner.PlanOrganise`) renders every
   identified file of the scope through `RenderLibraryPath` — the renderer the import and the
   item's `PATH` row use, so the three cannot disagree — and lists each file whose path would change
   as a row of the review table: from, struck, and to, in the diff's colours, each with its own
   **Skip**. Files that cannot be moved are listed apart with the reason (no identity, a release that
   cannot be read, a Lidarr library), and the files already in place are counted. Releases are read
   cache-first, so a library processed recently costs no request.
2. **The commit** (`POST /organise`, a queued file-writing job) plans again when it runs and makes
   only the moves that are still exactly what was reviewed; anything that changed meanwhile is
   reported in Activity rather than guessed at.

**Conflicts block the commit**, as the review step asks: two files rendering one path (`clash` —
one is misidentified, or the templates do not tell them apart) and a target something outside the
plan already occupies (`exists` — Organise never deletes). Compared without case, since on a
case-insensitive filesystem they are one file. A target another move of the plan is leaving is
*not* a conflict: the job moves in **two phases**, every file first to a hidden temporary name
beside its target and then into place, so a swap or a chain within the plan cannot land on a file
it is about to move. The index follows in the same two phases, since a path is unique there too.

**What moves with a file**: its `library_items` row — identity, pin and all — and the rows keyed by
its path, the tempo analysis and the fingerprint lookup, in one transaction per phase. Folders the
moves emptied are removed (deepest first, never the library root, never a folder still holding a
cover or cue sheet), the Plex folder cache forgets them, and Plex is asked to **scan** the library's
section — a moved file is found by a scan; refreshing the album it left would only re-read files no
longer there.

**A Lidarr library is never organised.** Lidarr tracks its files by path, so a file moved under it
is a file Lidarr has lost: the dry run of such a library is refused (409), and an artist or album
with files in one lists those files as not movable. The buttons are disabled where they could only
refuse. Lidarr renames its own files (*Media Management → Rename Tracks*).

## Tests

`modules/naming_test.go` (defaults, Lidarr's defaults accepted, the video rule, token spellings,
the release-group authority, cleaning, truncation, every rejection, the widened recogniser, the
Clean and The forms, the edition tokens), `TestImportRefusesToMixEditions` in `process/import_test.go`,
`components/naming_test.go` (`ComputeItemPath`, the Lidarr note, the fingerprint),
`TestTaggerProfileNaming` in `routers/coverage_crud_test.go` (storage, 400s, info and preview).

## Related

- [attach.md](attach.md) — folder attach, which reads the leading track number and disc folder.
- [collection.md](collection.md) — the artist-folder chain and the video rule.
- [tagging.md](tagging.md) — the other half of a tagger profile.
