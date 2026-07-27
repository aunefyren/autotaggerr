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
join-phrase rules.

## Related

- Plex only supports a single **album** artist, so multi-artist *album* credits still tag only
  the primary one — see the "Known issues" note in `wip.md`. This feature governs the per-track
  artist string, which is where featuring credits actually show up.
