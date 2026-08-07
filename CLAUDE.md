# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Read on startup

At the start of every session, read these before doing work:
- **`docs/wip.md`** — what is *not done yet*: roadmap, known issues, ideas, in-flight work.
- **`docs/development.md`** — code conventions, CI gates, and dev instructions to follow.
- **`docs/style-guide.md`** — style guide for ANY UI work. Must be either followed or adapted.

`docs/` holds all project documentation, one `*.md` per feature — start with
**`docs/media-manager.md`** for how the components fit together; `docs/development.md` indexes the
rest. Keep `wip.md` current as work starts, and when something ships move what is worth keeping
into its feature doc and delete it from `wip.md` (it is a backlog, not a changelog).

## What this is

Autotaggerr is a Go service that enriches a Lidarr-managed music library with detailed
metadata from MusicBrainz, then optionally tells Plex to refresh. It runs as a long-lived
Gin HTTP service with a cron-scheduled background job, and can also process a single file
via a one-shot flag invocation.

## Commands

```bash
go build -o autotaggerr .          # build the binary (module: github.com/aunefyren/autotaggerr)
go run .                           # run locally (needs ./config to be writable)
go vet ./...                       # static checks
go test -race ./...                # run the test suite (race-enabled)
go run . --file "/music/Artist/Album (2020)/01 Track.flac" --fileRoot "/music"  # process one file and exit
```

The repo has a growing test suite (`modules/`, `utilities/`). CI (`.github/workflows/go.yml`)
runs `gofmt` → `go build` → `go vet` → `go test -race` with coverage; it installs `flac`/`ffmpeg`
so the audio fixture tests run there too (`ffmpeg` synthesizes the fixtures; only `metaflac` is a
runtime dependency). Go 1.25 is the module target.

**Git is owned by the human.** Do not stage, commit, push, branch, or otherwise touch git state
— the maintainer handles all version control. Make and verify changes in the working tree only.

**External binaries are required at runtime** (bundled in the Docker image, must be on PATH otherwise):
- `metaflac` (from FLAC) — reads/writes Vorbis comments on `.flac` files

MP3 needs nothing external: ID3 is read and written in-process with
`github.com/bogem/id3v2`. `ffmpeg` is still a *test* dependency (the fixtures are synthesized
with it) and `fpcalc` (chromaprint) is optional, for AcoustID.

## Configuration

Config lives in `./config/config.json` (auto-created on first run by `files.LoadConfig`).
`files.ConfigFile` is a process-global holding the parsed config. CLI flags in
`main.parseFlags` override config values; `entrypoint.sh` maps Docker env vars (`port`, `TZ`,
`file`) onto those flags. `autotaggerr_version` is injected at release time by replacing the
`{{RELEASE_TAG}}` placeholder in `files/config.go`.

## Architecture

Everything flows through **`modules/`** (the business logic). `main.go` wires clients and
scheduling; `models/` holds structs; `files/` handles config; `utilities/` has path/string helpers.

**The four verbs** — *Process* (walk, resolve, tag), *Scan* (re-derive the collection from the
index), *Refresh metadata*, *Tag files* — are named in
[docs/scanning.md](docs/scanning.md#the-four-verbs-and-why-none-of-them-cascades). The Go package
`scan` owns *Process*, not *Scan*; the *Scan* verb is `collection.Rebuild`.

**Two entry paths, one core:**
- Scheduled/startup: `main.processLibraries` → `modules.ScanFolderRecursive` walks each
  configured library and calls `ProcessTrackFile` per audio file.
- Single file: `--file` + `--fileRoot` flags → `modules.ProcessTrackFile` directly.

**Per-file pipeline (`modules/files.go`):**
1. `ProcessTrackFile` resolves the MusicBrainz **release ID** and **track ID** for the file.
   - Preferred source: Lidarr, via `ResolveMetadataDetailsFromLidarr` (needed for MP3s —
     Lidarr does not embed MB IDs in MP3 tags).
   - Fallback: read IDs directly from the file's existing tags (`ExtractMusicBrainzReleaseID`, etc).
2. `GetMusicBrainzRelease` fetches the full release (cached; see below), and the code finds the
   matching track within `response.Media[].Tracks[]`.
3. `ProcessTrackFileAfterMatch` builds a `models.FileTags` and writes it via `SetFileTags`,
   which dispatches to `SetFlacTags` (metaflac) or `SetMP3Tags` (bogem/id3v2) by extension.
   Tag writes are diffed first — unchanged files are skipped and reported separately.
4. Changed albums are collected into an `albumsWhoNeedMetadataRefresh` map (album name → Plex
   key) and, after the run, `plexClient.RefreshAlbum` is called for each.

**Path convention matters:** metadata resolution and Lidarr/Plex lookups assume the library
layout `<root>/<ARTIST>/<ALBUM> (<YEAR>)/[<MEDIA FOLDER>]/<TRACKS>`. `--fileRoot` is the
directory *containing* the artist folder. Path normalization is OS-specific
(`utilities/normalize_path_{unix,darwin,windows}.go`, build-tagged).

**External clients** (`modules/lidarr.go`, `modules/plex.go`) are constructed in `main` only
when their base URL + credential config is present, and each is health-checked at startup.
Both are passed as pointers into the pipeline and may be `nil` — always nil-check before use.

## Caching & rate limiting

Every cache is a **database table with an in-memory front**, warmed once at process start by
`modules.LoadAllCaches` (called after `modules.SetDB`). MusicBrainz releases and entities have their
own tables; Lidarr and Plex share `provider_cache`, keyed by `(source, key)`; artwork keeps its
bytes under `config/artwork/` with an index table. Writes are **write-through** — there is no dirty
flag, no flush, and no JSON cache file (the `config/*.json` ones are read once for a legacy import
and never written). A new cache follows that pattern; adding a batched writer back would reintroduce
the restart hole that removed it. See [docs/mirror.md](docs/mirror.md#what-is-cached-and-where).

MusicBrainz calls go through `RateLimit()` — respect it when adding new MusicBrainz requests.

## Concurrency

Every background verb (processing runs, re-tags, metadata refreshes) is enqueued onto a **single serial job
queue** in `scan.Runner`, drained by one worker goroutine that holds `jobMu` per job (`scan/queue.go`).
The cron job, the startup run and the API all enqueue; jobs dedup by key and file-writing jobs take
priority over pending metadata jobs. Interactive `RetagItems` stays synchronous and `TryLock`s
`jobMu`. On startup, `events.ReconcileRunning` closes out events left `running` by a crashed process.
See [docs/scanning.md](docs/scanning.md#the-job-queue).
