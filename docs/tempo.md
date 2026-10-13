# Feature: tempo (BPM) analysis

Measures each file's tempo from the audio itself and stores it, for libraries that ask. The point
is a number you can build on — running playlists at a cadence, say — so the result is stored
**raw**, with a confidence, and nothing about it is guessed or corrected on the way in.

**It stores; tagging writes.** *Analyse tempo* reads audio and writes one database table. It never
touches a file. A stored tempo reaches a file's tags through the next tagging pass, the same
handover a release that changed upstream uses
([scanning.md](scanning.md#the-four-verbs-and-why-none-of-them-cascades)) — and only if the
library's tagger profile writes BPM. How it is written, and when it is not, is in
[tagging.md](tagging.md#tempo-is-the-one-tag-musicbrainz-does-not-supply).

### Reaching the file

Analysis changes nothing about a file, so skip-unchanged would walk past it forever.
`components.tempoPending` is the one exception added to the skip test: a file whose stored tempo is
newer than its `last_scanned_at` is processed once more, which writes the BPM and re-stamps
`last_scanned_at`, so the run after that skips it again. It is only asked when the library's profile
writes BPM, so a library that analyses without writing pays nothing for it. *Tag files* needs no such
rule — it re-tags every indexed file anyway.

The BPM write itself then changes the file's size and mtime, which the next analysis pass recognises
as *tags changed, audio did not* and re-stamps — the loop closes without a second decode.

## Setup

Two independent switches. Either one off and Autotaggerr behaves exactly as it did before the
feature existed.

1. **`aubiotrack` on the server.** Bundled in the Docker image (Alpine's `aubio` package).
   Elsewhere, install aubio (`aubio-tools` on Debian/Ubuntu) and make sure `aubiotrack` is on
   `PATH`. If it is missing, Autotaggerr logs that once and reports the feature as unavailable.
2. **Per-library opt-in.** *Libraries → edit → Analyse tempo* (`analyse_tempo`). Off by default:
   analysis decodes every file, which a library should ask for rather than be surprised by.

Writing the result into files is a third, separate decision: the tagger profile's `bpm_write`
(off by default) and `bpm_min_confidence` — see
[tagging.md](tagging.md#tempo-is-the-one-tag-musicbrainz-does-not-supply).

**Settings → Tempo analysis** holds the schedule (`autotaggerr_tempo_cron_schedule`, nightly at
01:00) and the worker count (`autotaggerr_tempo_concurrency`, 2). There is no global on/off: with
no library opted in, the scheduled pass finds nothing and records nothing.

## The detector

`aubiotrack`, from [aubio](https://aubio.org). It was chosen over Essentia's music extractor on
evidence rather than reputation:

| | Essentia `streaming_extractor_music` | aubio `aubiotrack` |
|---|---|---|
| Builds | prebuilt v2.1_beta2 (2020), x86_64/i686 only | Alpine package on every architecture the image targets |
| Size | tens of MB, static | ~5 MiB with its libraries; ffmpeg's are already in the image for `fpcalc` |
| Confidence | none documented in `rhythm.*` | none — computed here from the beats |
| Licence | AGPL-3.0 | GPL-3.0, as Autotaggerr |

Essentia would break the multi-arch image (no ARM build) and brings nothing the confidence needs.
`aubio tempo` — aubio's Python front end — was ruled out too: it needs Python and numpy, and prints
only a *mean* of the per-beat tempos.

`aubiotrack -i <file>` prints one beat time per line. `modules.TempoFromBeats` reduces them:

- **BPM is 60 over the median inter-beat interval.** A tracker that drops a beat leaves one
  interval twice as long; a mean is dragged by every one of them, the median by none.
- **Confidence is the share of intervals within ±5% of that median** — how regular the beat grid
  was, 0 to 1. It is a statement about the grid, *not* about whether the grid is the right one: a
  tracker locked steadily onto double time scores as high as one locked onto the true tempo.
- **Fewer than 16 beats is no tempo**, stored as BPM 0 with the reason. Silence, a skit, an
  ambient intro: `aubiotrack` answers these with few or no beats and a successful exit.

### How good the numbers are

Measured against a real library (172 files: disco, hip-hop, Elton John, pop):

- **Steady dance and pop is accurate.** *Stayin' Alive* 105.0 and 105.1 across two editions,
  *Night Fever* 110.5, *You Should Be Dancing* 125.3, *Disco Inferno* 129.7 — all at confidence
  ≥ 0.86.
- **Garbage is visible.** The clearly wrong results scored 0.10–0.14.
- **Hip-hop around 88 BPM is often read at double time** (~176). Expected, and the reason the
  result is never corrected here: whoever uses the number knows whether 80 or 160 is wanted.
- **Ballads are unreliable, and confidence does not always catch it.** Three editions of *Candle in
  the Wind* read 126, 155 and 132, each at about 0.8. A regular grid at the wrong period is still a
  regular grid.

About one second per track, most of it reading the file.

## A file is analysed once

Analysis decodes the whole file, so the pass is built around not repeating it. Per file, cheapest
test first (`tempo.Runner.analyseOne`):

1. **Size and mtime match the stored row** → fresh. Nothing is read.
2. **The audio hash matches** → the tags changed, the audio did not. The row is re-stamped.
3. **Another path has the same audio hash** → a moved or copied file. The result is carried over.
4. Otherwise, run the detector.

Step 2 is what makes the feature affordable. The BPM tag write changes size and mtime, so with a
size/mtime key alone every write would cost a re-analysis — of every file, once, the first time
tags are written. `modules.AudioPayloadHash` hashes the bytes between the tags instead: a leading
ID3v2 tag and FLAC's metadata blocks (where `metaflac` writes) are skipped, as are a trailing ID3v1
tag — which `stripID3v1` removes on our own MP3 writes — and an APEv2 tag. It decodes nothing, and
is only computed once step 1 has already failed. Tests: `TestAudioPayloadHashIgnoresMP3Tags`,
`…IgnoresFLACMetadata`, and `…SurvivesOurOwnTagWrites` against real files.

**A failure is not retried until the file changes.** A corrupt file failing every night would be
noise. A stored row carries its error, and a new file or a new detector version clears it.

**Changing the analysis redoes the library.** Each row records `detector` and `detector_version`
(the method plus `tempoAnalysisRevision`); a row from another version is never fresh. `aubiotrack`
has no version flag, so an aubio upgrade on its own invalidates nothing — deliberately, since a
distro bump re-analysing a whole library overnight would be a surprise. Bump the revision when the
reduction changes.

Rows whose file has left the index are pruned after a pass completes — after, so a file moved during
the pass has already been carried to its new path.

## Why it has its own runner

Every background verb that writes files shares one serial queue
([scanning.md](scanning.md#the-job-queue)). A first analysis pass over a large library is hours, and
on that queue every scan and re-tag would wait behind it — for no reason, since analysis spends
nothing the queue serialises: no MusicBrainz budget, no file writes. So it has its own worker, as
artwork does ([artwork.md](artwork.md#why-this-is-its-own-verb)).

It does not ignore file work, though. Analysis is CPU-bound decoding on the same machine, so
**a pass pauses between files while a file-writing job runs** (`process.Runner.WritingFiles`) —
the job someone is waiting on gets the CPU and the disk. A metadata refresh does not count: it is
network-bound and shares nothing with a decode.

**Resuming is skipping.** A pass keeps no cursor. Every result is written as it is produced, so a
cancelled or crashed pass resumes on the next one at the cost of a stat per file it already did.
Cancelling stores nothing for the file in flight.

Targets are the **indexed** files of enabled, opted-in libraries — not a directory walk. Discovering
files is *Process*'s job, and a file the index does not hold has nowhere for a tag to come from.

## Activity

One `tempo_analysis` event per pass. Its counters separate what cost a decode from what did not:
*Analysed*, *Already analysed*, *Tags changed, audio did not*, *Moved, result kept*, *No measurable
tempo* and *Failed*. Detail rows are **failures only** — a row per analysed file would be thousands
of rows on a first pass saying the expected thing happened. A press with nothing to do is refused
with the reason rather than recorded.

## UI

- **Libraries → edit → Analyse tempo (BPM)** is the opt-in, and the table says *Tempo analysed*
  under a library's state so the question "which of my libraries are analysed?" does not take a modal
  per row. The checkbox stays live when `aubiotrack` is missing — a library can opt in first — and
  the help under it says that nothing will happen until the binary is installed.
- **A *Tempo analysis* card under the libraries table** carries the verb: one sentence, one summary
  line (files with a tempo, the last run, failures) or a progress bar while a pass runs, and *Analyse
  tempo* / *Stop*. It is shaped like the artwork control on Data sources because it is the same kind
  of thing. When the pass could do nothing, the sentence says which of the two reasons applies —
  no detector, or no library opted in — since each has its fix somewhere else. No confirm dialog: a
  pass writes no file and stopping it loses nothing.
- **Tagger profiles** gain *Write BPM* and *Minimum confidence* (disabled, not hidden, while BPM is
  not written), and a *BPM* column.
- **The item view** states the file's tempo above its tag diff — `104.96 BPM · confidence 0.96 ·
  written as 105`, or why there is none: not analysed, nothing measured, or not written and by which
  rule. The BPM row in the diff itself appears only when a write would carry one. Served by
  `GET /api/v1/library-items/:id/tempo`.
- **Settings → Tempo analysis** renders from the schema like every other section.

## API

| Route | Purpose |
| --- | --- |
| `GET /api/v1/tempo/status` | Current/last pass, files with a tempo on record, and whether a pass could do anything (`detector_available`, `libraries`). |
| `POST /api/v1/tempo/analyse` | Queue a pass. `202`; `409` with the reason when the detector is missing or nothing is opted in. |
| `POST /api/v1/tempo/cancel` | Stop the running pass. |

`analyse_tempo` is an ordinary field on `PUT /api/v1/libraries/:id`, and `bpm_write` /
`bpm_min_confidence` on `PUT /api/v1/tagger-profiles/:id` (a 400 for an unknown mode or a confidence
outside 0–1). `GET /api/v1/library-items/:id/tempo` returns one file's stored result with
`library_analyses`, `bpm_write` (off when the profile writes no tags at all) and the effective
`min_confidence`.

## Related

- [scanning.md](scanning.md) — the verbs, the job queue, Activity.
- [artwork.md](artwork.md) — the other verb with its own runner, and why.
- [fingerprinting.md](fingerprinting.md) — the other optional binary, detected the same way.
