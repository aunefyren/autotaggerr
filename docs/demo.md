# Demo instance and browser tour

A **demo instance** is Autotaggerr running on a synthetic library of any size (up to about 100k
tracks) with no real music and no network: tiny but valid audio files, a pre-filled database, and a
stand-in MusicBrainz. It exists to try a feature at scale (the Insights graph at 100k tracks, a page
with thousands of rows) and to test new features end to end without anyone's real library.

The **browser tour** signs in to a running instance, visits every page, and saves full-page
screenshots at desktop and phone widths. It fails on a console error, an uncaught exception or a
5xx, so it is a smoke test as well as a picture. Claude reads the PNGs to check UI changes.

```bash
make demo SIZE=medium          # build the UI, generate ~/autotaggerr-demo if needed, run on :8090
make tour                      # (another terminal) screenshots into tools/browser/out/
make tour TOUR="--only graph,stats --width 1440"
make demo-reset SIZE=large     # throw it away and generate again
```

Sign in as **admin / admin** at http://localhost:8090. Without make:
`go run ./tools/demo run -size medium` and `cd tools/browser && npm ci && node tour.mjs`.

## Sizes

| `SIZE` | Track files | Artists (incl. producers etc.) | Disk | Generate |
|---|---|---|---|---|
| `tiny` | ~300 | ~25 | 1 MB | <1 s |
| `small` | ~2k | ~120 | 5 MB | ~2 s |
| `medium` | ~20k | ~1.2k | 50 MB | ~15 s |
| `large` | ~110k | ~5.9k | 500 MB + 330 MB database | ~2 min |

The same `-seed` and size always generate the same library: same names, same MBIDs, same
duplicates. Keep the demo directory on a Linux filesystem (the default, `~/autotaggerr-demo`). A
hundred thousand small files on `/mnt/c` is many times slower.

## What is generated (`tools/demo`)

- **A universe** (`universe.go`), deterministic from the seed. Artists and bands with power-law
  output, albums, singles (whose lead track is the album's recording), EPs, live albums, deluxe
  editions, two-disc albums and Various Artists compilations. Recordings carry credits: featured
  artists, a shared pool of producers, engineers and session players (the graph's hubs),
  composers and lyricists through works, release-level producers. Genres, decades, labels,
  countries and ISRCs on most recordings. MBIDs are UUIDv5 under a fixed demo namespace, so they
  are stable and never real, except Various Artists, which uses MusicBrainz's own ID because the
  app treats it specially.
- **Holdings**, which say what is on disk: most albums, some singles, EPs and compilations, and
  the duplicate scenarios Insights looks for. These are both editions of an album held (identical
  audio, or a remaster), an album in FLAC and MP3, a byte-identical copy in a second folder, and
  singles also on their album. A few files carry no MusicBrainz IDs at all (`Unsorted/`).
- **Files** (`audio.go`), about 1.7 KB each: `music/<Artist>/<Album> (<Year>)[ suffix]/NN Title.ext`.
  - **FLAC** is a real stream. STREAMINFO claims the track's full length (the duration the disc
    guard reads), Vorbis comments carry the MusicBrainz IDs, padding leaves room for metaflac, and
    one short frame of samples is drawn from an audio seed.
  - **MP3** is an ID3v2.4 tag (the TXXX MusicBrainz frames the app reads) followed by four MPEG-1
    Layer III frames.

  The seed belongs to the recording, so two copies of a recording are the same audio for
  `modules.AudioPayloadHash` whatever their tags, and a remaster is not. Nothing decodes the audio
  (`flac -t` complains that the stream is shorter than it claims, which is expected), so tempo and
  loudness analysis have nothing real to measure.
- **The instance** (`instance.go`):
  - `config/config.json` on port 8090.
  - `config/autotaggerr.db` with the seeded defaults and admin/admin.
  - The MusicBrainz data source pointed at the stand-in (`http://127.0.0.1:8091/ws/2`, 50 req/s)
    and the Cover Art Archive at `/caa`.
  - A native manager and one library on `music/`.
- **The pre-fill** (the default): the release cache for every held release, an index row per file
  as a first Process would record it, then `collection.Rebuild` and `insights.SyncCredits`. The
  instance is ready the moment it starts. `-raw` skips this and leaves the library unindexed, so
  the first **Process** identifies and tags every file through the stand-in (small: 2.3k files in
  about a minute). Use it to exercise the real pipeline.
- **A marker**, `.autotaggerr-demo`, records the seed and size. `generate` refuses any non-empty
  directory without it, and the repository itself. It writes only inside the demo directory,
  and logs to the terminal, never to `config/autotaggerr.log`.

## The stand-in MusicBrainz (`stub.go`)

`go run ./tools/demo serve` (or `run`, which starts it in-process) rebuilds the universe from the
marker and answers every request the app makes, from memory:

| Request | Answer |
|---|---|
| `/ws/2/release/{id}?inc=…` | the full lookup, with recordings, labels, release group, ISRCs and relationships |
| `/ws/2/artist/{id}`, `/ws/2/release-group/{id}` | lookups |
| `/ws/2/release-group?artist=` | an artist's discography, paged |
| `/ws/2/release?release-group=` | an album's editions |
| `/ws/2/release?query=`, `/ws/2/artist?query=` | word search over titles, artist names, barcodes and catalog numbers |
| `/caa/{release-group\|release}/{id}/front-{size}` | a generated PNG cover |

Unknown IDs answer 404, as MusicBrainz does. That matters: the app records a 404 as a deletion and
queues a migration, which is correct here too, because every ID the demo files carry exists in the
universe. `DEMO_STUB_LOG=1` prints every request, and `run` reports the total on exit.

Scheduled jobs run as usual. The scan, metadata refresh, discography reads and artwork all go to
the stand-in, so they can only touch the demo directory.

## The tour (`tools/browser`)

A separate package from `webui/`, so CI and Docker builds are unchanged. It depends only on
`playwright-core` (no browser download, no install scripts) and drives a Chromium that is already
installed: `CHROMIUM_PATH`, or the newest `chromium_headless_shell-*` in Playwright's cache
(`npx playwright-core install chromium-headless-shell` puts one there).

It signs in through the API, hands the token to the SPA through the URL fragment
(`consumeTokenFromUrl`), then visits Overview, Collection, an artist, Items, the four Insights
tabs, Activity and Settings at 1440 px and 390 px. On the graph it also hovers and clicks the
largest artist. Options: `--base`, `--user`, `--password`, `--only`, `--width`, `--out`. To add a
page, add a stop to `stops` in `tour.mjs`. Give it a `then` for an interaction worth a picture.

## What it has caught

Worth knowing, because it is why the harness exists. These were all found on the demo, and none
by the unit tests:

- **The graph magnified small libraries.** Labels drew at 28px and collided with nodes. It now
  never frames less than the canvas holds at 1:1, keeps labels a constant on-screen size, and
  pulls disconnected clusters in with gentle gravity.
- **At 4,000 connected artists the graph drew a cloud of dots.** Picking the strongest artists one
  by one chose artists that did not connect to each other (150 nodes, 71 edges). Nodes now follow
  the strongest edges ([insights.md](insights.md#assembling-a-graph)).
- **Every node of an uncentred graph was labelled.** The rule for labelling the centred artist
  matched every node.
- **Duplicates showed absolute paths**, against the style guide. They are now relative to the
  library root.
- **At 110k tracks** the duplicates API returned 14 MB per page view (now paged, 150 KB), and the
  every-role graph took 12 s (now 3.5 s cold and 0.8 s while changing filters). The fixes are
  described in [insights.md](insights.md#assembling-a-graph).
- **Generated album titles collided** at 100k tracks, which broke the unique path index. Those
  folders are now numbered, as they would be on disk.
