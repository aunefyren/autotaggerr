# Autotaggerr build helpers.
#
# Cross-platform by construction: every recipe uses only `go`, `npm`, and `cd`, so
# GNU make drives it the same on Linux/macOS and on Windows (cmd.exe or Git Bash).
# `make` itself is NOT bundled with Windows — install it once with `choco install make`
# or `scoop install make` — so the equivalent raw commands are also documented in
# docs/development.md for anyone without it.
#
# The frontend (webui/) is a separate Node build that emits web/dist, which the Go
# binary embeds. `go build` knows nothing about it, which is why `make build` refreshes
# the bundle first — the embedded UI is otherwise silently stale.

.PHONY: build go ui run update test fmt vet check deps

# Default: refresh the embedded frontend, then compile the binary. Use this so web/dist
# is never stale.
build: ui
	go build .

# Compile the Go binary only, skipping the frontend — for when the UI has not changed.
go:
	go build .

# Verify the toolchain and print a clear report. Runs before every frontend build so a
# missing prerequisite (Node, npm, or the TypeScript compiler) fails with an
# explanation and a fix rather than a raw "'tsc' is not recognized".
check:
	go run ./tools/checkenv

# Build the frontend into web/dist. Prerequisites are verified first, then npm deps are
# installed if missing or package-lock.json changed (see the node_modules rule).
ui: check webui/node_modules
	cd webui && npm run build

# Force a clean, dev-included frontend install. `--include=dev` guarantees the build
# tools (tsc, vite) are installed even when NODE_ENV=production would otherwise skip
# devDependencies — the usual cause of a missing `tsc`.
deps:
	cd webui && npm ci --include=dev

webui/node_modules: webui/package-lock.json
	cd webui && npm ci --include=dev

# Refresh the frontend, then run the service (reads/writes ./config).
run: ui
	go run .

# Update dependencies in BOTH ecosystems — `go get -u` never touches npm — then rebuild
# the frontend bundle so the embedded UI matches the updated source.
update:
	go get -u ./...
	go mod tidy
	cd webui && npm update
	cd webui && npm run build

# CI gates, runnable locally.
test:
	go test -race ./...

fmt:
	gofmt -w .

vet:
	go vet ./...
