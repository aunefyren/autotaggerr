# Feature: loudness (ReplayGain) analysis

Measures each album's loudness with [rsgain](https://github.com/complexlogic/rsgain) and stores
it, for libraries that ask. The tagging pass then writes it as ReplayGain 2.0 tags: EBU R128
loudness against a -18 LUFS reference, a gain and a peak per track and per album. Jellyfin
normalises playback volume from `REPLAYGAIN_TRACK_GAIN`; Plex measures loudness itself and does not
need it (see [tagging.md](tagging.md#what-jellyfin-reads-measured)).

It is [tempo](tempo.md)'s design throughout: **it stores, tagging writes**, it has its own runner,
it pauses while files are being written, and it resumes by skipping what is current. The
differences all follow from one fact. Album gain is a property of an album's files together, so
**the unit is the album**, not the file.

## Setup

- **`rsgain` on the server.** It is bundled in the Docker image (Alpine's `rsgain` package; it links
  the ffmpeg libraries chromaprint already brings). Elsewhere, install rsgain and make sure it is on
  `PATH`. If it is missing, Autotaggerr logs that once and reports the feature as unavailable.
- **Per-library opt-in.** *Libraries → edit → Analyse loudness (ReplayGain)* (`analyse_loudness`).
  It is off by default, because analysis decodes every file.
- **Writing is a separate choice** on the same form: *Write ReplayGain* (`replaygain_write`). It has
  the BPM setting's three modes: don't write (store only), only where the file has none, or always.
- **Settings → Background jobs → Loudness analysis** holds the schedule (`autotaggerr_loudness_cron_schedule`, nightly
  at 02:00). There is no global switch; with no library opted in the pass does nothing.

## Albums, and when one is measured again

`loudness.albums` groups the indexed audio files of opted-in libraries **per library and release**.
A file with no release identified yet is grouped with the others in its folder, which is what its
album is until something names it. Each album is measured in one call, `rsgain custom --album
--tagmode=s --output --quiet <files>`, which reads every file and writes nothing.

Each file's row (`track_loudnesses`) records the album it was measured in and a hash of that album's
member paths at the time. An album is **fresh** when every member has a current row for that
album and membership. It is **re-stamped** when the members whose size or mtime changed still hash
to the stored audio (`modules.AudioPayloadHash`), meaning their tags changed, not their audio; that
is almost always the ReplayGain write itself. Otherwise the **whole album** is measured again:
new audio in any track, a track added or gone, or a row from another analysis version. A file that
only moved changes the membership, so its album is re-measured. Unlike tempo there is no carry-over,
because album gain has to be measured with the new membership anyway.

**A failed album is not retried until it changes.** Its rows store the error against that
membership and those files, as tempo does, so a corrupt file is not re-decoded every night.

### Reading rsgain

The output is tab-separated: a header, a row per file in the order given, then an `Album` row
(`modules.parseRsgainOutput`). A row names its file by base name only, which is ambiguous across
disc folders (`CD1/01.flac`, `CD2/01.flac`). So rows are matched **by position**, and the name is
checked, not trusted: a reordered or short answer is an error, never another file's gain.
`TestParseRsgainOutput` uses rsgain 3.5.3's real output. `TestAnalyseAlbumLoudnessWithRsgain` runs
the real binary when it is installed (CI installs it) on two files named `01.flac` on two discs.

**rsgain never writes a tag.** A second writer the diff does not control is how files come to
disagree with what Autotaggerr thinks they hold. Our engines write the four values, so the diff,
idempotency and `remove_values` rules hold. The test checks that rsgain left the file alone.

`loudnessAnalysisRevision` invalidates every row when how rsgain is run or read changes. rsgain's
own version is not part of it, for tempo's reason: a distro upgrade re-measuring a library
overnight would be a surprise, and EBU R128 does not drift.

## Reaching the file

`modules.ReplayGainTagValues` turns a stored row into the four tags, formatted as rsgain writes them:
`-6.50 dB` and `0.987654`. It returns nothing when the library does not write ReplayGain, the
analysis failed, or the row no longer describes the audio. Like `TempoTagValue`, it re-stamps a row
whose file changed only in its tags. The keys are `REPLAYGAIN_TRACK_GAIN`, `_TRACK_PEAK`,
`_ALBUM_GAIN` and `_ALBUM_PEAK`: Vorbis comments on FLAC, and `TXXX` frames under the same names
on MP3, as Picard and rsgain write them.

- **Fill-empty keeps another tool's four values together.** A file that already carries a track
  gain keeps all four tags as they are (`applyReplayGainPolicy`), so a track gain is never paired
  with another tool's album gain. Overwrite replaces them.
- **The keys are only present when there are values**, so a library that does not write
  ReplayGain cannot have `remove_values` clear another tool's.
- **Skip-unchanged** brings a file back once its album has been measured since it was last
  processed (`components.loudnessPending`, tempo's rule). The library's write mode is part of the
  tag-settings fingerprint (`TaggerSettings.ReplayGainWrite`, `omitempty`), so turning it on reaches
  every file on the next Process.

## Activity

One `loudness_analysis` event per pass, counted in albums: *Albums checked*, *Measured*, *Already
measured*, *Tags changed, audio did not* and *Failed*. Detail rows are failures only, one per album.

## API

`GET /loudness/status`, `POST /loudness/analyse` and `POST /loudness/cancel`, shaped like tempo's.
A press that could do nothing (no rsgain, nothing opted in) is refused with the reason. The MCP
`get_status` tool carries the pass's status, and `list_libraries` each library's opt-in and mode.

## Tests

- `loudness/loudness_test.go`: albums measured together, unchanged albums skipped, a tags-only
  change re-stamped, a changed or grown album re-measured whole, failures stored and not retried,
  opt-in, folder grouping, yielding, cancel, the queue and orphan pruning.
- `modules/loudness_test.go`: parsing, values, the policy on real FLAC and MP3, and the real binary.
- `components/tempo_test.go` (`TestLoudnessPending`) and `routers/loudness_test.go`.

## Related

- [tempo.md](tempo.md): the design this follows.
- [tagging.md](tagging.md): the other fields, and what Plex and Jellyfin read.
