# Development

Conventions and instructions for working in this repository. Read this alongside `CLAUDE.md`
(architecture), `docs/media-manager.md` (how the components fit together) and `wip.md` (what is
still open) before making changes.

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

Every push/PR runs, in order: **build the web UI** (`npm ci && npm run build` in `webui/`) →
`gofmt` check → `go build` → `go vet` → `go test` with race + coverage. The workflow installs
`flac` + `ffmpeg` so the audio fixture tests run in CI too. **`gofmt` and `go vet` are hard
gates** — keep both clean or CI goes red. Commit files with LF endings (`.gitattributes`
normalizes to LF; the working tree may show CRLF on Windows/WSL, which is fine — git stores LF).

## Web UI (`webui/` → `web/dist`)

The frontend is a Vite + React + TypeScript SPA in `webui/`, styled entirely from the design
tokens in `docs/style-guide.md` (see the "UI follows the style guide" rule above). It builds into
`web/dist`, which the Go binary embeds via `go:embed` (`web/embed.go`) and serves with an
index.html fallback for client-side routes — so the whole app ships in one binary.

```bash
cd webui
npm ci            # install (Node 18+; Vite 4/React 18 are pinned for older Node too)
npm run dev       # dev server on :5173, proxies /api to the Go service on :8080
npm run build     # type-check + bundle into ../web/dist
```

**`web/dist` is committed** (not gitignored) so `go build ./...` works without a Node toolchain;
rebuild and commit it when the UI changes. `webui/node_modules` is ignored. After changing the UI,
run `npm run build` before building/running the Go binary, or the embedded assets will be stale.

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
- **UI follows the style guide.** Once `docs/style-guide.md` exists, *every* UI change must consult it
  and either follow it or deliberately reshape it (updating the guide in the same change). Reuse the
  shared design tokens, colors, elements, and principles as much as possible — do not introduce
  one-off styles. See `docs/media-manager.md` for the component model the UI is built around.

## Versioning & release

- The version string is the `{{RELEASE_TAG}}` placeholder in `files/config.go`, replaced by the
  release/tag workflows via `sed`. Do not hardcode a version there.
- Releases build multi-arch Docker images and Go binaries; source dirs shipped in the release
  archive are listed in `release.yaml`'s `extra_files` — update it if you add a top-level
  package directory.

## Documentation layout (`docs/`)

- `wip.md` — **only what is not done yet**: roadmap, ideas, known issues, in-flight work. When
  something ships, move what is worth keeping into its feature doc and delete the rest from `wip.md`
  — it is not a changelog, and git already remembers the history.
- `development.md` — this file.
- `style-guide.md` — the design system every UI change consults (see above).
- One `*.md` per feature as features grow. Keep feature docs focused and written as *reference*
  ("how it works, and why it is that way"), not as a diary of passes.

Feature docs:
- `media-manager.md` — the component model (managers, data sources, libraries, tagger profiles),
  the pipeline, and the DB/config/SPA infrastructure. Start here.
- `collection.md` — present vs wanted: ownership, the disk/catalog split, and the desire model.
- `attach.md` — identifying files by hand, single and per folder, plus release search.
- `scanning.md` — scans, skip-unchanged, drift sync, activity events, caching and concurrency.
- `tagging.md` — what gets written to a file, and why idempotency is the property that matters.
- `artist-credit-tagging.md` — how MusicBrainz artist credits (incl. featuring artists) become
  the track's artist tag.
- `fingerprinting.md` — optional AcoustID identification.
- `authentication.md` — local login, API keys, OIDC.
