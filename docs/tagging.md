# Feature: tag writing

What Autotaggerr writes into a file once it knows which MusicBrainz release and track it is.

## Engines

Dispatched by extension in `SetFileTags`:

- **FLAC** — Vorbis comments via `metaflac`.
- **MP3** — ID3 via `ffmpeg` / `ffprobe`.

Both binaries are required at runtime (bundled in the Docker image, otherwise on `PATH`). Other
formats are not supported yet — see [wip.md](wip.md).

Settings come from the library's **tagger profile** (`models.TaggerProfile`), the DB-backed
successor to the old `autotaggerr_*` config flags.

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
(`metaflac --remove-tag`, or ffmpeg's `-metadata key=`). The two engines are deliberately kept in
step — the profile is one promise, and it used to hold on FLAC while ID3 quietly ignored it.

**Every key in the change set must be written.** The MP3 writer's reported diff is derived from the
change set rather than from its per-field write blocks, so a key that is reported but skipped —
which is what a `desired[x] != ""` guard does once empties are allowed through — is reported as
written while nothing happens, and is re-reported on every scan forever. The paired frames (`track`,
`disc`) share one ID3 frame and are cleared together; the ISRC `TXXX` frame is cleared as `TXXX=`
rather than `TXXX=ISRC:`, which would leave an empty payload behind. Regression test:
`TestMP3RemoveValuesClearsAndConverges`.

## Idempotency is the property that matters

If the desired tags never equal what is read back, the file is rewritten on **every** scan forever.
That has happened once, and the shape of the bug is worth remembering:

> **Multi-value ISRC on MP3.** ISRC is packed into a single `TXXX=ISRC:<value>` frame, but the
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
