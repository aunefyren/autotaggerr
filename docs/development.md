# Development

Conventions and instructions for working in this repository. Read this alongside `CLAUDE.md`
(architecture), `docs/media-manager.md` (how the components fit together) and `wip.md` (what is
still open) before making changes.

## Local setup

- Go 1.25 (the `go.mod` toolchain target). Older toolchains cannot build the module.
- Node `^20.19.0 || >=22.12.0` — vite 8's engine requirement, and a hard one: npm refuses the
  install on anything older. CI and the Docker `web` stage both run Node 22. `make check` reports a
  version that will not work rather than letting npm say it in passing.
- Runtime binaries must be on `PATH`: `metaflac` (FLAC). Without it, FLAC tag reads/writes fail at
  runtime even though the build succeeds. MP3 needs nothing — ID3 is read and written in-process.
  `ffmpeg` is still needed to *run the tests*, which synthesize their audio fixtures with it.
- `./config` must be writable; `files.LoadConfig` creates `config/config.json` on first run.

```bash
go build ./...                     # compile everything (embeds the current web/dist as-is)
go run .                           # run the service (reads/writes ./config)
go vet ./...                       # must be clean — CI gate
gofmt -l .                         # must print nothing — CI gate
go test -race ./...                # run tests
go run . --file "<path>" --fileRoot "<library-root>"   # one-shot single-file processing
```

**Building with the frontend.** The Go build embeds `web/dist` but does *not* rebuild it — the
frontend is a separate Node build (see [Web UI](#web-ui-webui--webdist)). This is true of the
*local* build only; the Docker and release workflows build the SPA themselves (see
[who builds the SPA](#who-builds-the-spa-and-what-the-committed-copy-is-still-for)). Rebuild the UI
whenever `webui/` source changed, then build the binary:

```bash
cd webui && npm ci && npm run build && cd ..   # refresh web/dist (npm ci only needed once / on dep change)
go build .                                       # produces ./autotaggerr (autotaggerr.exe on Windows)
```

**Updating dependencies** spans two ecosystems — `go get -u` never touches the frontend:

```bash
go get -u ./... && go mod tidy                 # Go modules
cd webui && npm update && npm run build && cd ..   # npm packages, then rebuild the bundle
```

### Makefile shortcuts

A [`Makefile`](../Makefile) wraps these flows so the frontend step is never forgotten:

| Target | Does |
|--------|------|
| `make build` | verify prereqs, rebuild `web/dist`, then `go build .` (the safe default — UI never stale) |
| `make go` | `go build .` only, skipping the frontend (when the UI is unchanged) |
| `make ui` | verify prereqs, then rebuild `web/dist` only (installs deps first if missing/changed) |
| `make run` | verify prereqs, rebuild `web/dist`, then `go run .` |
| `make check` | verify the toolchain (Go, Node, npm, `tsc`) and print a clear report |
| `make deps` | force a clean `npm ci --include=dev` in `webui/` (fixes a missing `tsc`); installs for the platform you run it on — see [below](#building-the-same-checkout-from-windows-and-wsl) |
| `make update` | `go get -u` + `go mod tidy` + `npm update` + rebuild the bundle |
| `make test` / `make fmt` / `make vet` | the CI gates locally |

Every recipe uses only `go`/`npm`/`cd`, so the same Makefile runs on Linux/macOS and Windows —
but `make` is not bundled with Windows. Install it once (`choco install make` or
`scoop install make`), or use the raw commands above, which need no extra tooling.

`make build`/`ui`/`run` run `make check` first (`tools/checkenv`, a small cross-platform Go program),
so a missing Node, npm, or TypeScript compiler fails with an explanation and a fix rather than a raw
platform error. **`tsc` is not a system tool** — it is a devDependency in `webui/node_modules`, so a
"'tsc' is not recognized" failure means the frontend deps were not installed, or were installed
without devDependencies (the common Windows cause is `NODE_ENV=production`, which makes `npm ci` skip
them). The fix is `make deps` — or, without make, `cd webui && npm ci --include=dev`.

### Building the same checkout from Windows and WSL

`webui/node_modules` is **platform-specific**, so a tree installed on one side of the WSL boundary
cannot build on the other without help. vite bundles with **rolldown** and compiles CSS with
**lightningcss**, and both ship their compiler as a native executable in a per-platform package —
`@rolldown/binding-linux-x64-gnu`, `lightningcss-win32-x64-msvc` — installed as an optionalDependency
that npm selects from the host's os/cpu. Build with the wrong ones and the build dies in a wall of
napi stack frames. There is no wasm fallback installed to soften it.

This is handled: `npm run build` and `npm run dev` run **`webui/tools/ensure-native.mjs`** first
(via npm's `prebuild`/`predev`), which installs the current platform's binaries if they are absent.
Alternating between Windows and WSL therefore costs a two-second install on the first build after
each switch, not a failure. It only ever adds, uses `--no-save --no-package-lock` so neither
`package.json` nor the lockfile moves, and is a no-op on the happy path — including in CI, where
`npm ci` already installed the right ones. `make ui`/`build`/`run` inherit it, since they all end in
`npm run build`.

Three caveats worth knowing:

- **npm evicts the other platform's binaries** when it installs one set, so the two genuinely
  alternate rather than coexisting. Installing both is not possible declaratively: npm applies the
  os/cpu filter to direct dependencies too, silently skipping a foreign optionalDependency and
  erroring with `EBADPLATFORM` on a foreign regular one. Forcing both in needs `--force` or a second
  `--os`/`--cpu` install, neither of which belongs in a lockfile — hence repairing on demand.
- **The check is `existsSync`, not `require.resolve`.** `lightningcss` does not export its own
  `package.json`, so asking the resolver for it fails even when the package is right there — which
  made an earlier version of the script skip lightningcss silently and *look* like it had worked,
  because npm re-resolves optional dependencies as a side effect of any other install. The script
  reads `node_modules` off disk for exactly that reason.
- **`make check` cannot see this problem.** `checkenv` checks for the platform's `tsc` *launcher*
  (`.bin/tsc.cmd` vs `.bin/tsc`), but npm writes both of those regardless of platform, and the
  native binaries it does not look at. So a cross-platform tree reports "All prerequisites present"
  and then fails in the bundler. The `prebuild` guard is what actually fixes it; `make deps` (a full
  `npm ci`) also works, but only for the platform you run it on.

## CI gates (`.github/workflows/go.yml`)

Every push/PR runs, in order: **build the web UI** (`npm ci && npm run build` in `webui/`) →
`gofmt` check → `go build` → `go vet` → `go test` with race + coverage → **coverage gate**. The workflow installs
`flac` + `ffmpeg` so the audio fixture tests run in CI too. **`gofmt` and `go vet` are hard
gates** — keep both clean or CI goes red. Commit files with LF endings (`.gitattributes`
normalizes to LF; the working tree may show CRLF on Windows/WSL, which is fine — git stores LF).

### The coverage gate

`COVERAGE_MIN` in the workflow (currently **75**) fails the build when total statement coverage
drops below it. Two things about it are easy to get wrong:

- **Coverage is per package.** `go test ./...` without `-coverpkg` credits a package only for what
  its *own* tests execute. `auth.Middleware` read 0% for a long time even though every `routers`
  test went through it — a handler test proves the route is wired, not that the boundary holds. So
  test a package from inside that package, and do not expect an integration test to raise the
  number somewhere else.
- **Anything talking to MusicBrainz needs its stub.** Two seams, depending on where the test lives:
  - *Inside `modules/`*: `musicbrainzBaseURL` is a package var; `withMockMB`
    (`modules/musicbrainz_http_test.go`) points it at an `httptest` server and resets the caches and
    rate limiter. Unexported, so it only works in `modules/`. Use it for the real adapter's
    HTTP/parse/cache/rate-limit behaviour.
  - *Everywhere else* (`routers`, `collection`, `components`, `mirror`): inject a fake
    `metadata.MetadataSource`. Every non-`modules` MB fetch routes through that port — the concrete
    one is `modules.NewMetadataSource()`; a test supplies a fake with zero network. `routers.API.Meta`
    (nil ⇒ `API.meta()` falls back to the real source), `process.Runner`/`mirror.Runner` carry a
    defaulted `meta` field, and `collection.SyncArtist`/`ReleaseGroupEditions` /
    `components.ComputeItemDiff` take the port as a parameter. See `routers/metadata_source_test.go`
    (`fakeMeta`) and `collection/sync_artist_test.go` for the pattern.
- **AcoustID and artwork need no port.** Their base URLs are already values a caller supplies:
  `LookupAcoustID(apiKey, baseURL, fp)` takes one as a parameter, and cover art / fanart read
  `ArtworkProviders.CoverArtBaseURL` / `.FanartBaseURL` (exported fields, filled from the data
  source's config in `routers/artwork.go`). The package-level `acoustidBaseURL`,
  `coverArtArchiveBaseURL` and `fanartBaseURL` are only the fallbacks used when the caller passes
  nothing. A test anywhere points them at an `httptest` server — see `modules/artwork_test.go`.

To find the cheapest remaining gaps, sum the uncovered statements per file rather than reading
percentages — a 40%-covered 300-statement file matters more than a 0%-covered 10-statement one:

```bash
go test -covermode=atomic -coverprofile=cover.out ./...
awk -F'[: ]' 'NR>1 {n=$(NF-1); c=$NF; t[$1]+=n; if(c=="0") u[$1]+=n}
  END {for (f in t) printf "%5d %5d %s\n", u[f]+0, t[f], f}' cover.out | sort -rn | head -20
```

## Web UI (`webui/` → `web/dist`)

The frontend is a Vite + React + TypeScript SPA in `webui/`, styled entirely from the design
tokens in `docs/style-guide.md` (see the "UI follows the style guide" rule above). It builds into
`web/dist`, which the Go binary embeds via `go:embed` (`web/embed.go`) and serves with an
index.html fallback for client-side routes — so the whole app ships in one binary.

```bash
cd webui
npm ci            # install (needs Node ^20.19 || >=22.12 — vite 8's engine requirement)
npm run dev       # dev server on :5173, proxies /api to the Go service on :8080
npm run build     # type-check + bundle into ../web/dist
```

**`web/dist` is committed** (not gitignored) so `go build ./...` works without a Node toolchain;
rebuild and commit it when the UI changes. `webui/node_modules` is ignored. After changing the UI,
run `npm run build` (or `make ui` / `make build`) before building/running the Go binary, or the
embedded assets will be stale — a rebuilt binary serving an old bundle is the classic "my UI change
didn't show up".

**The build is deterministic**, so a rebuild that changes nothing leaves `git status` clean. Vite
derives each asset's filename from a hash of its contents, so identical sources produce
byte-identical output under the same filenames. If `web/dist` shows up as modified, the UI genuinely
changed and the diff belongs in the commit with the source that caused it — it is not build noise to
be discarded. (`git checkout -- web/dist` after a real UI change is how you get a binary that serves
the previous UI.)

### Why it is not gitignored

Every CI producer builds the bundle itself, so the obvious next step is to stop committing it. Don't
— it is load-bearing for the local build in a way that is easy to miss:

- **`//go:embed all:dist` is a compile-time pattern.** With no `web/dist` at all, package `web` fails
  with `pattern all:dist: no matching files found`, and that is not just `go build` — `go vet`,
  `go test ./...` and gopls all stop working on a fresh clone. A committed placeholder
  (`web/dist/.gitkeep`, which `all:` would match) fixes the compile...
- **...but not the tests.** `TestRouterFallsBackToTheSPA`, `TestRouterFallbackIgnoresQueryStrings`
  and `TestRouterServesFavicon` in [`router_test.go`](../router_test.go) assert against a real
  `index.html` and a real `favicon.svg`. Against a placeholder they fail, so `go test ./...` would
  require npm — and teaching them to skip would silence them in exactly the case they exist for.

The cost of committing it is review noise, and that is handled in
[`.gitattributes`](../.gitattributes) instead: `web/dist/**` is `linguist-generated=true` so GitHub
collapses it, and `web/dist/assets/**` is `-diff` so `git diff` does not print a minified bundle over
the change you are trying to read.

### Who builds the SPA, and what the committed copy is still for

**Nothing that ships depends on the committed bundle.** Every workflow that compiles Go builds the
UI itself first — there are no exceptions left:

| Workflow | Builds the SPA | How |
|---|---|---|
| Docker images (`docker-image.yml`, `docker-image-beta.yml`) | yes | the `web` stage in [`Dockerfile`](../Dockerfile) |
| Release binaries (`release.yaml`) | yes | a dedicated `web` job, shared with the build matrix as an artifact |
| CI (`go.yml`) | yes | into its own workspace, then discarded |
| CodeQL (`codeql-analysis.yml`) | yes | a `setup-node` + `npm run build` step before `init`, so autobuild traces a current tree |

This used to be untrue of the first two, and the failure was quiet: CI rebuilt the UI into a
workspace it threw away, so a green run said nothing about the bundle in the commit, and the image —
which had no Node in it — embedded whatever `web/dist` the commit carried. A stale committed bundle
shipped a stale UI from a passing build.

**`release.yaml` builds the bundle once, not once per binary.** The matrix is six legs
(linux × 386/amd64/arm64/arm, windows × 386/amd64) and the bundle is byte-identical for all of them,
so a build step inside the matrix would run the same `npm ci` and vite build six times for six
identical directories — the same waste the Dockerfile's `--platform=$BUILDPLATFORM` pin exists to
avoid. A `web` job builds it and uploads it; each leg deletes the committed `web/dist` and restores
the artifact in its place. **The delete matters:** vite's `emptyOutDir` clears the directory on a
real build, but `download-artifact` only overwrites the files it brings, so without the `rm -rf` a
hashed asset that no longer exists would linger beside the new bundle and be embedded. That the
restore has to happen in the matrix leg at all is because `go-release-action` compiles from
`$GITHUB_WORKSPACE` rather than checking out again.

The committed copy therefore now serves exactly one purpose: `go build` / `go run .` without a Node
toolchain. That is a development convenience, and it is the one that still bites, because **a stale
`web/dist` will still make your local `go run .` serve an old UI**. That is what `make build` and
`make run` exist to prevent. Nothing in CI fails when the committed bundle is out of date — by
design, since every producer rebuilds it — so keep rebuilding and committing it when the UI changes.

`web/dist` is in [`.dockerignore`](../.dockerignore) so the committed copy cannot reach an image even
by accident: the `web` stage is the only thing that supplies it, and if that stage ever fails to,
`//go:embed all:dist` fails the build rather than embedding something old.

The `web` stage is pinned to `--platform=$BUILDPLATFORM`. The bundle is byte-identical for every
architecture, so without it buildx would emulate a full `npm ci` once per target — four of them,
including `arm/v7` and `386` — to produce four identical directories.

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
  `files.CreateConfigFile` and the back-fill block in `files.LoadConfig`, a field in
  `settings.Sections()` (`TestEveryConfigKeyHasAHome` fails otherwise), and a row in the README
  configuration reference table.
- **`config.json` holds process config only.** Anything that describes a *library* — its manager,
  its data source, its tag-writing settings — is a database row edited on its own page, and does
  not get a config key that seeds it. A key read once on the first boot and ignored afterwards is a
  trap: editing it looks like configuring something. Retired keys are simply deleted from
  `models.ConfigStruct`; `files.SaveConfig` round-trips the struct through a map, so it writes the
  keys alphabetically and drops any the struct no longer declares the first time it runs.
- **Flags use the `flag.Visit` pattern.** In `main.parseFlags`, only override a config value
  when the flag was actually provided (tracked via `flag.Visit`). Do not unconditionally assign
  from a flag's value — that clobbers config on every startup. New env-mapped flags also get a
  line in `entrypoint.sh`.
- **Errors**: return wrapped/annotated errors up the stack; log at the point of handling with
  `logger.Log` (logrus). Use the `...f` variants (`Errorf`/`Debugf`) when formatting — plain
  `Error`/`Debug` do not interpret `%` directives (`go vet` catches this).
- **External clients** (`modules/lidarr.go`, `modules/plex.go`) are only constructed when their
  config is present and may be `nil`. Always nil-check before use in the pipeline.
- **Caching**: every cache is a database table with an in-memory front, warmed once by
  `modules.LoadAllCaches` at startup and **written through** as entries are fetched — there is no
  batched flusher and no JSON cache file. MusicBrainz releases and entities have their own tables;
  Lidarr and Plex share `provider_cache` via `modules/provider_cache.go`, keyed by `(source, key)`.
  The legacy `config/*.json` files are read once for a one-time import
  (`providerCacheImportJSON`), never written, and deleted once their contents are in the database. MusicBrainz requests go through `RateLimit()` — route any new MusicBrainz call through
  it, and cache the result via `mbCachePut` so the mirror can keep it warm. See `docs/mirror.md`.
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
- `mb-migration.md` — following MusicBrainz merges and deletions across Autotaggerr's own records.
- `mirror.md` — the local MusicBrainz mirror: what is cached where, TTLs, and the refresh pass.
- `artwork.md` — covers and artist images: warming them ahead of the page, and why it is its own verb.
- `fingerprinting.md` — optional AcoustID identification.
- `authentication.md` — local login, API keys, OIDC.
- `settings.md` — the /settings page: the config surface, which edits apply live, and the admin gate.
