# Development

Conventions and instructions for working in this repository. Read this alongside `CLAUDE.md`
(architecture) and `wip.md` (current work) before making changes.

## Local setup

- Go 1.25 (the `go.mod` toolchain target). Older toolchains cannot build the module.
- Runtime binaries must be on `PATH`: `metaflac` (FLAC) and `ffmpeg`/`ffprobe` (MP3). Without
  them, tag reads/writes fail at runtime even though the build succeeds.
- `./config` must be writable; `files.LoadConfig` creates `config/config.json` on first run.

```bash
go build ./...                     # compile everything
go run .                           # run the service (reads/writes ./config)
go vet ./...                       # must be clean — CI gate
gofmt -l .                         # must print nothing — CI gate
go test -race ./...                # run tests (none yet)
go run . --file "<path>" --fileRoot "<library-root>"   # one-shot single-file processing
```

## CI gates (`.github/workflows/go.yml`)

Every push/PR runs, in order: `gofmt` check → `go build` → `go vet` → `go test` with race +
coverage. The workflow installs `flac` + `ffmpeg` so the audio fixture tests run in CI too.
**`gofmt` and `go vet` are hard gates** — keep both clean or CI goes red. Commit
files with LF endings (`.gitattributes` normalizes to LF; the working tree may show CRLF on
Windows/WSL, which is fine — git stores LF).

## Git ownership

**The maintainer handles all git operations** — staging, commits, branches, pushes, PRs. When
working in this repo (including as an AI assistant), make and verify changes in the working tree
only; never run `git add`/`commit`/`push`/`branch` or otherwise mutate version-control state.

## Code conventions

- **camelCase for identifiers.** Preferred naming style throughout: unexported Go identifiers
  (locals, params, unexported funcs/fields) use `lowerCamelCase`; exported identifiers use
  `UpperCamelCase` (PascalCase). No snake_case or SCREAMING_CASE in Go code — snake_case is only
  for JSON config keys (the struct tags in `models.ConfigStruct`).
- **Config is a process global.** `files.ConfigFile` (type `models.ConfigStruct`) holds parsed
  config; pass it (or the pieces needed) into functions rather than re-reading from disk.
- **Defaults live in one place.** New config keys get their default in *both*
  `files.CreateConfigFile` and the back-fill block in `files.LoadConfig`, and a row in the
  README configuration reference table.
- **Flags use the `flag.Visit` pattern.** In `main.parseFlags`, only override a config value
  when the flag was actually provided (tracked via `flag.Visit`). Do not unconditionally assign
  from a flag's value — that clobbers config on every startup. New env-mapped flags also get a
  line in `entrypoint.sh`.
- **Errors**: return wrapped/annotated errors up the stack; log at the point of handling with
  `logger.Log` (logrus). Use the `...f` variants (`Errorf`/`Debugf`) when formatting — plain
  `Error`/`Debug` do not interpret `%` directives (`go vet` catches this).
- **External clients** (`modules/lidarr.go`, `modules/plex.go`) are only constructed when their
  config is present and may be `nil`. Always nil-check before use in the pipeline.
- **Caching**: MusicBrainz/Lidarr/Plex lookups cache to JSON under `./config/` with
  `*LoadCache`/`*SaveCache` helpers loaded at startup. MusicBrainz requests go through
  `RateLimit()` — route any new MusicBrainz call through it.

## Versioning & release

- The version string is the `{{RELEASE_TAG}}` placeholder in `files/config.go`, replaced by the
  release/tag workflows via `sed`. Do not hardcode a version there.
- Releases build multi-arch Docker images and Go binaries; source dirs shipped in the release
  archive are listed in `release.yaml`'s `extra_files` — update it if you add a top-level
  package directory.

## Documentation layout (`docs/`)

- `wip.md` — roadmap, ideas, known issues, in-flight work.
- `development.md` — this file.
- One `*.md` per feature as features grow. Keep feature docs focused; link from `wip.md` when a
  roadmap item graduates into a real feature doc.

Feature docs:
- `artist-credit-tagging.md` — how MusicBrainz artist credits (incl. featuring artists) become
  the track's artist tag.
