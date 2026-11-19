# Feature: Artist credit tagging

How Autotaggerr turns a MusicBrainz artist credit (which can list several artists with
"featuring" relationships) into the single artist string written to a track's tags. This is the
feature behind the "notice the featuring artists" result shown in the README demo.

## Where it lives

- `modules.MusicBrainzArtistsArrayToString` (`modules/musicbrainz.go`) — builds the string.
- `models.ArtistCredit` (`models/musicbrainz.go`) — one credited artist: `Name` (name as
  credited on the release), `Artist.Name` (the artist's current canonical name), and
  `Joinphrase` (the text MusicBrainz uses to join this artist to the next, e.g. `" feat. "`).
- Tagger-profile knobs in `models.TaggerSettings` (`models/db.go`), projected from the library's
  `TaggerProfile` row.

## Input

A MusicBrainz release credits artists as an ordered list, each with a join phrase:

```
[ {Name: "Artist A", Joinphrase: " feat. "}, {Name: "Artist B", Joinphrase: ""} ]
```

## Behavior

For each credited artist the function appends a name followed by a join phrase. The settings that
decide *which name* and *which join phrase* are fields on the library's **tagger profile**
(`models.TaggerSettings`, projected from the row by `components.Tagger.Settings()`), not global
config — one library can join artists differently from another. They were `autotaggerr_*` keys in
`config.json` before tagger profiles existed; the names below are the profile's.

### Which name

- `use_current_artist_name` (default `true`): use `Artist.Name` (the artist's
  current canonical name). When `false`, use `Name` (the name exactly as credited on the
  release, which may be an alias or stylization).

### Which join phrase

Evaluated per artist, in this order:

1. `use_custom_artist_delimiter` is `false` → use MusicBrainz's own `Joinphrase`
   verbatim (preserves `" feat. "`, `" & "`, etc. as MusicBrainz recorded them).
2. Otherwise the custom delimiter (`custom_artist_delimiter`, default `" & "`) is
   the base join phrase, with two refinements:
   - The **last** artist gets an empty join phrase (nothing trails the final name).
   - When there are **more than two** artists and this is not the second-to-last, and
     `custom_artist_delimiter_commas` is `true`, use `", "` instead — so the list
     reads `A, B & C` rather than `A & B & C`.

### Whether it is written at all

**In short:** *Leave out a track artist that repeats the album artist*
(`ignore_redundant_contributing_artists`, default **off**) decides one thing — whether a track whose
artist is the same as the album artist gets an `ARTIST` tag. It does **not** affect `ARTISTS`. The
name is older than the checkbox and undersells that: "contributing artists" means the track artist,
the `ARTIST` tag, and "redundant" means "says the same as `ALBUMARTIST`".

What each tag gets, for a track credited to the album artist alone (Bathtub, on Snoop Dogg's
*Doggystyle*):

| Tag | Setting off (default) | Setting on |
|---|---|---|
| `ARTIST` | `Snoop Dogg` | not written |
| `ARTISTSORT` | `Snoop Dogg` | not written — the sort name goes with the name it sorts |
| `ARTISTS` | `Snoop Dogg` | `Snoop Dogg` — never affected |
| `MUSICBRAINZ_ARTISTID` | the credit's MBIDs | the credit's MBIDs — never affected |
| `ALBUMARTIST` and the rest | unchanged | unchanged |

A track whose credit says anything more is never redundant and always gets `ARTIST`, setting on or
off: Gin and Juice, credited to "Snoop Dogg & Daz Dillinger" on the same album, or a compilation
track credited to its own artist.

**Leave it off for Plex and Jellyfin.** Players read the track artist from `ARTIST`; neither reads
`ARTISTS` for it, so writing `ARTISTS` alone does not stand in for it.

- **Plex** does not fall back to the album artist. A track with no `ARTIST`, or an empty `ARTIST=`,
  gets a track artist from Plex's own data, which is the name as credited on the release: with *use
  current artist name* on, Doggystyle's single-artist tracks read "Snoop Doggy Dogg" while the album
  read "Snoop Dogg". Writing `ARTIST=Snoop Dogg` and refreshing the album's metadata fixed it, and
  removing the tag again brought the old name back. Plex hides a track artist equal to the album
  artist, so writing it costs nothing on screen.
- **Jellyfin** lists such a track with no artist of its own (see
  [tagging.md](tagging.md#what-jellyfin-reads-measured)).

It used to default to **on**, with no checkbox, on the assumption that players fall back to the
album artist. The data migration `write_redundant_artist` turned it off once on every existing
profile; the next tag run writes `ARTIST` on those tracks. Turning it back on afterwards sticks.

#### How "the same" is decided

It is a **string comparison, not a credit count**: the track credit and the album artist are joined
into names exactly as they would be written (current or credited name, the profile's delimiter) and
compared with `utilities.EqLoose`, which ignores case, accents and punctuation. The rule used to be
"one credited artist ⇒ redundant", which reads "alone" as "same as the album artist" — so a
compilation track, or a track credited solely to a guest on someone else's release, lost its
`ARTIST` even though the album artist never names that artist. The comparison keeps those.

`ARTISTS` and `MUSICBRAINZ_ARTISTID` always carry the full track credit. Both are genuinely
multi-valued — `FileTags.Artists` and `FileTags.MBArtistIDs` — and how that reaches the file is the
format's business, not this setting's: see
[tagging.md](tagging.md#several-values-in-one-field). Credited order is preserved, because the
release states it and the diff compares it.

#### What happens to an `ARTIST` already in the file

With the setting on, a left-out `ARTIST` is "nothing to say", not "clear it": an `ARTIST` Lidarr (or
anyone else) wrote stays, unless the profile's `remove_values` is on (`DiffFlacTags` and
`DiffID3Tags` treat it the same way). An empty `ARTIST=` comment is the exception and is always
removed. See [tagging.md](tagging.md#removing-a-value).

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
