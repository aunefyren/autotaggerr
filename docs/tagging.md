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

## Related

- [artist-credit-tagging.md](artist-credit-tagging.md) — how a MusicBrainz artist credit (including
  featuring artists) becomes the artist string.
- [scanning.md](scanning.md) — when tagging runs.
