# Feature: Artist credit tagging

How Autotaggerr turns a MusicBrainz artist credit (which can list several artists with
"featuring" relationships) into the single artist string written to a track's tags. This is the
feature behind the "notice the featuring artists" result shown in the README demo.

## Where it lives

- `modules.MusicBrainzArtistsArrayToString` (`modules/musicbrainz.go`) — builds the string.
- `models.ArtistCredit` (`models/musicbrainz.go`) — one credited artist: `Name` (name as
  credited on the release), `Artist.Name` (the artist's current canonical name), and
  `Joinphrase` (the text MusicBrainz uses to join this artist to the next, e.g. `" feat. "`).
- Config knobs in `models.ConfigStruct` (see the README configuration reference for defaults).

## Input

A MusicBrainz release credits artists as an ordered list, each with a join phrase:

```
[ {Name: "Artist A", Joinphrase: " feat. "}, {Name: "Artist B", Joinphrase: ""} ]
```

## Behavior

For each credited artist the function appends a name followed by a join phrase. Two config
options decide *which name*, and three decide *which join phrase*.

### Which name

- `autotaggerr_use_current_artist_name` (default `true`): use `Artist.Name` (the artist's
  current canonical name). When `false`, use `Name` (the name exactly as credited on the
  release, which may be an alias or stylization).

### Which join phrase

Evaluated per artist, in this order:

1. `autotaggerr_use_custom_artist_delimiter` is `false` → use MusicBrainz's own `Joinphrase`
   verbatim (preserves `" feat. "`, `" & "`, etc. as MusicBrainz recorded them).
2. Otherwise the custom delimiter (`autotaggerr_custom_artist_delimiter`, default `" & "`) is
   the base join phrase, with two refinements:
   - The **last** artist gets an empty join phrase (nothing trails the final name).
   - When there are **more than two** artists and this is not the second-to-last, and
     `autotaggerr_custom_artist_delimiter_commas` is `true`, use `", "` instead — so the list
     reads `A, B & C` rather than `A & B & C`.

### Whether it is written at all

`autotaggerr_ignore_redundant_contributing_artists` (default `true`) drops the track artist when
it says nothing the album artist does not already say — i.e. when the two strings are equal under
`utilities.EqLoose` (case, accents and punctuation ignored). A single-artist album then carries
`ALBUMARTIST` and no `ARTIST`, and players fall back to the album artist for display.

It is a **string comparison, not a credit count**. The rule used to be "one credited artist ⇒
redundant", which reads "alone" as "same as the album artist" — so a compilation track, or a track
credited solely to a guest on someone else's release, lost its `ARTIST` even though the album artist
never names that artist. The comparison keeps those.

Two things are unaffected by this setting and always carry the full track credit: `ARTISTS` (every
credited artist) and `MUSICBRAINZ_ARTISTID` (the track credit's MBIDs). Both are genuinely
multi-valued — `FileTags.Artists` and `FileTags.MBArtistIDs` — and how that reaches the file is the
format's business, not this setting's: see
[tagging.md](tagging.md#several-values-in-one-field). Credited order is preserved, because the
release states it and the diff compares it.

Whether an emptied `ARTIST` is actually *removed* from the file is a separate decision: both
`DiffFlacTags` and `DiffID3Tags` only turn an empty desired value into a change when the tagger
profile's `remove_values` is on. With it off, the tag Lidarr (or anyone else) wrote is left alone.
See [tagging.md](tagging.md#removing-a-value).

## Examples

With defaults (`use_current_artist_name=true`, `use_custom_artist_delimiter=true`,
`custom_artist_delimiter=" & "`, `custom_artist_delimiter_commas=true`):

| Credited artists | Output |
|---|---|
| `A` | `A` |
| `A` feat. `B` | `A & B` |
| `A`, `B`, `C` | `A, B & C` |

With `use_custom_artist_delimiter=false`, MusicBrainz join phrases are kept:

| Credited artists | Output |
|---|---|
| `A` feat. `B` | `A feat. B` |

## Tests

`modules/musicbrainz_test.go` (`TestMusicBrainzArtistsArrayToString`) covers the single/two/
three-artist cases and the custom-delimiter-disabled fallback. Extend it when changing the
join-phrase rules. `modules/files_extra_test.go` (`TestBuildFileTagsRedundantArtist`) covers the
redundancy rule, including the compilation case the credit count got wrong.

## Related

- Plex only supports a single **album** artist, so multi-artist *album* credits still tag only
  the primary one. This feature governs the per-track artist string, which is where featuring
  credits actually show up.
- `tagging.md` — the write path this string ends up in.
