# Feature: manual attach

Tell Autotaggerr what an unmatched file is. This is the piece that makes the native manager usable
at all: `AutotaggerrManager.Correlate` resolves from embedded MusicBrainz tags only, so a FLAC that
already carries MB IDs resolves itself, but an untagged MP3 never will.

**Self-healing.** Once attached and tagged, the file carries the MB IDs itself and resolves
natively forever after. Attaching is a one-time cost per file, not an annotation to maintain.

## Attaching one file

*Items → Attach*. Search for the release, pick the track, done — tags are written immediately.

- The chosen track is **validated against the real release** rather than trusted from the request
  body. A wrong ID would otherwise be written straight into the file's tags.
- The recording ID is derived server-side from that release, never taken from the caller.
- The item is pinned (`CorrelationSource = manual`), which is what stops the next scan from undoing
  the decision.
- Tags are written through `scan.Runner.RetagItem`, which **refuses while a scan is running**
  rather than writing the same file from two goroutines.
- A tagging failure returns **202 and keeps the correlation** — it is a real decision the user made
   — with a warning, instead of discarding it.

`DELETE .../attach` removes the pin and hands the file back to automatic resolution. The written
tags are left alone: they are still the correct tags for whatever it was attached to.

## Finding the release

A single free-text box could not surface the right release — a common album title returns hundreds
of editions — which blocked live testing entirely until the search became fielded.

`modules.ReleaseSearchQuery` renders a MusicBrainz Lucene query from
artist / `arid` / release / date / country / format / tracks / status / catno / barcode, ANDed
together, paged (`count`/`offset`, up to 100 per page) so "not found" is distinguishable from "not
on this page".

- **Values are quoted, not escaped.** Inside a quoted phrase a colon or bracket is already literal,
  and escaping hyphens corrupts the MBIDs that must match exactly. Only `"` and `\` are escaped.
- **Free text passes through unescaped**, so anyone who knows the syntax can write their own clause
  (`artist:Bee AND date:1977`).
- **`ParseMBIDInput` accepts a pasted musicbrainz.org URL or a bare MBID** — a release resolves
  directly, a release-group lists its editions, an artist narrows the search by `arid`. MusicBrainz's
  own site will always out-search an in-app form, so this is the escape hatch for anything the
  fields cannot reach.

The UI (`ReleaseSearch`, shared by both attach flows) prefills artist/title/year *per field* from
the library layout, so an album title can never be mistaken for part of the artist name.

## Attaching a folder

Files arrive as albums, so identifying them one at a time is what made attach unusable at scale.
Select the files on the Items page (the per-row **Folder** button filters the list to one directory),
pick the release once, then review the mapping.

**The review step is mandatory.** Silent auto-mapping is the one way to mistag an entire album in a
single click, so the proposal is always rendered as an editable table — every file → track pairing,
a **Skip** option per row, and duplicate targets blocked — before anything is written.

`modules.MapFilesToTracks` proposes the mapping and is pure and table-tested:

- Reads a leading track number from the filename (`05`, `2-05`) plus a disc from a `CD2`/`Disc 2`
  folder.
- **All-or-nothing.** If any file lacks a number, two files claim one track, a number falls outside
  the tracklist, or a multi-disc release leaves the disc ambiguous, the *whole album* degrades to
  the sort-order fallback. Mixing two strategies within one folder is how a partially-numbered
  mapping ends up looking right and being wrong.
- Each pairing carries **how** it was reached (`number` / `order`), so the review table can flag the
  weaker signal.

`POST /attach/bulk` validates **every** pairing against the real release before writing **any** of
them: the user reviewed the mapping as a whole, so a half-applied album is worse than a rejected
one. Rows with an empty track ID are skips, not errors.

`scan.Runner.RetagItems` takes the scan run-guard **once for the batch** and reuses the resolved
libraries, so an album is one unit of work rather than N. `RetagItem` delegates to it, which also
upgrades its old check-then-act guard to a CAS.

## Identifying by audio

Optional, and only ever a suggestion — see [fingerprinting.md](fingerprinting.md).

## API

| Endpoint | Purpose |
|----------|---------|
| `GET /search/releases` | fielded search; returns `{count, offset, releases}` |
| `GET /releases/:mbid/tracks` | flattened tracklist, cache-backed |
| `POST\|DELETE /library-items/:id/attach` | attach / unpin one file |
| `POST /attach/preview` | propose a file → track mapping; **writes nothing** |
| `POST /attach/bulk` | apply a reviewed mapping |

The bulk endpoints live under `/attach/*` rather than `/library-items/*` to avoid colliding with
the `:id` route.

`modules.ReleaseTracks` flattens a release's media into one numbered list and carries the
**recording ID** alongside the release-scoped track ID — the identity that survives across releases,
which the collection and fingerprinting both depend on.

## Tests

`modules/track_mapping_test.go` (12 mapper cases), `modules/musicbrainz_search_test.go` (the Lucene
builder, MBID/URL parsing), `routers/attach_test.go` and `routers/attach_bulk_test.go` — including
the wrong-track guard, the tagging-failure path, that preview persists nothing, that a bad track
rejects the whole batch, and that an unknown item ID cannot partially apply.

## Related

- [collection.md](collection.md) — what attaching makes visible.
- [fingerprinting.md](fingerprinting.md) — autofill for the picker.
