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

The split is the structural guarantee. Everything that reads the layout counts path segments: the
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

## Tests

`modules/naming_test.go` (defaults, Lidarr's defaults accepted, the video rule, token spellings,
the release-group authority, cleaning, truncation, every rejection, the widened recogniser),
`components/naming_test.go` (`ComputeItemPath`, the Lidarr note, the fingerprint),
`TestTaggerProfileNaming` in `routers/coverage_crud_test.go` (storage, 400s, info and preview).

## Related

- [attach.md](attach.md) — folder attach, which reads the leading track number and disc folder.
- [collection.md](collection.md) — the artist-folder chain and the video rule.
- [tagging.md](tagging.md) — the other half of a tagger profile.
