# Audio fingerprinting (AcoustID)

Identifies a file from the audio itself, for when nothing else can: an untagged MP3 that Lidarr
does not know about, a rip with no usable metadata, a file whose folder name is a mystery.

**It never tags anything on its own.** AcoustID identifies a *recording*, and a recording appears
on many releases — the album, the single, every compilation, every remaster. Choosing the wrong one
writes a plausible wrong album into the file's tags, and because attaching is self-healing (the
file then carries those MB IDs and resolves natively forever after), a wrong answer would look
correct from then on. So identification produces **ranked suggestions inside the attach picker**,
and a human confirms the track.

## Setup

Three independent switches. Any one of them off and Autotaggerr behaves exactly as it did before
the feature existed — no errors, no degraded paths.

1. **A data source.** *Data sources -> Add data source -> AcoustID*, with a client key from
   [acoustid.org/new-application](https://acoustid.org/new-application). The key is write-only:
   stored, never returned by the API, never shown again.
2. **`fpcalc` on the server.** Bundled in the Docker image (Alpine's `chromaprint` package).
   Elsewhere, install chromaprint-tools and make sure `fpcalc` is on `PATH`. If it is missing,
   Autotaggerr logs that once at first use and reports the feature as unavailable — it is a fact
   about the deployment, not a per-file error.
3. **Per-library opt-in.** *Libraries -> edit -> Allow audio fingerprint identification*. Off by
   default, so upgrading changes nothing until you ask for it.

`GET /api/v1/identify` reports which of these is missing, which is how the attach modal explains a
disabled button instead of offering an action that always fails.

## Using it

Open **Attach** on any item, then **Identify by audio**. Suggestions are listed with a confidence
percentage and the reasons behind it ("strong audio match", "album folder matches the release
title", "year matches"). Picking one opens that release's tracklist with the suggested track
marked — matched on the recording MBID, so the mark survives choosing a different edition. Nothing
is written until you press Attach.

## How a suggestion is scored

`modules.PickAcoustIDMatch` is pure and table-tested, because this is the function that decides
which album a fingerprint means.

- **The fingerprint score is a hard gate.** Below `AcoustIDConfidenceFloor` (0.55) a candidate is
  dropped outright. Folder agreement is evidence about *which release*, not about whether the audio
  matches, so it can never lift a weak match over the bar.
- **The folder breaks the tie.** Album title similarity (ignoring edition noise like
  "(Deluxe Edition)"), release year, and how many audio files sit in the folder versus the
  release's track count. This is what separates the album from the single and the compilation at
  identical fingerprint scores, and the original pressing from the remaster.
- **Folder evidence only moves a candidate within the headroom above its own score**, capped by
  `folderWeight`, so a strong folder match never turns a middling fingerprint into a certainty.
- A candidate that disagrees with the folder is **ranked lower, not dropped** — the folder may
  simply be named differently.
- A recording with no release still identifies the song; it is offered, but never above a candidate
  that names a release.

## Cost

Fingerprinting decodes the whole file, so results are cached in `acoustid_lookups`, keyed by path
and invalidated by size/mtime — the same identity rule scans use to skip unchanged files. A second
identification of the same file costs no subprocess and no network call. Re-encoding a file
re-fingerprints it.

AcoustID has its own rate limiter (~3 req/s, its documented ceiling), deliberately separate from
the MusicBrainz one: they are different services with different budgets, and sharing a limiter
would slow both for no reason.

## Not done

- No bulk identification. It is per file, from the attach modal. Fingerprinting a whole library is
  a different (much more expensive) feature, and would need its own progress reporting and an
  Activity event.
- The scan pipeline never calls it. `CorrelationSourceFingerprint` exists in the model but nothing
  writes it — by design, since automatic fingerprint correlation is exactly the silent-mistagging
  path this pass refuses.
