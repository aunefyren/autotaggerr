// Command checkenv verifies the toolchain a build needs and reports it clearly, the
// same way on every OS. It exists because a missing prerequisite otherwise surfaces as
// a raw, platform-specific error — "'tsc' is not recognized" on Windows being the one
// that prompted it — with no hint of the cause or the fix. The Makefile's `check`
// target runs it ahead of the frontend build; it exits non-zero when something needed
// is missing, printing what and how to fix it.
package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
)

func main() {
	fmt.Println("Checking build prerequisites…")

	// go is implied — this program could not run without it — but report it so the
	// list is complete.
	report(true, "go", version("go", "version"))

	nodeOK := tool("node", "Install Node 18+ from https://nodejs.org")
	npmOK := tool("npm", "npm ships with Node — reinstall Node from https://nodejs.org")

	// tsc is a devDependency in webui/node_modules, not a system tool, so it is checked
	// on disk rather than on PATH. This is the usual Windows failure: node and npm are
	// fine, but the install skipped devDependencies.
	tscOK := true
	if nodeOK && npmOK {
		tscOK = reportTSC()
	}

	if !nodeOK || !npmOK || !tscOK {
		fmt.Fprintln(os.Stderr, "\nSome prerequisites are missing — see above.")
		os.Exit(1)
	}
	fmt.Println("\nAll prerequisites present.")
}

// tool checks a system command is on PATH, reporting its version or a fix.
func tool(name, fix string) bool {
	if _, err := exec.LookPath(name); err != nil {
		report(false, name, "not found on PATH — "+fix)
		return false
	}
	report(true, name, version(name, "--version"))
	return true
}

// reportTSC diagnoses the TypeScript compiler. It checks the platform-specific
// launcher npm creates, not just the package: `tsc.cmd` on Windows, the `tsc` symlink
// on Unix. That distinction matters because a node_modules installed on another OS (or
// copied across a WSL/Windows boundary) has the package but not this platform's
// launcher — the exact "'tsc' is not recognized" failure, which a package-only check
// would wave through.
//
// Three outcomes: not installed yet is fine (the build installs it); the launcher
// present is fine; a node_modules missing the launcher fails, because npm run build
// will otherwise die on `tsc`.
func reportTSC() bool {
	nm := filepath.Join("webui", "node_modules")
	if _, err := os.Stat(nm); err != nil {
		fmt.Println("  •  tsc          webui/node_modules not installed yet — `make ui` installs it")
		return true
	}

	launcher := filepath.Join(nm, ".bin", "tsc")
	if runtime.GOOS == "windows" {
		launcher = filepath.Join(nm, ".bin", "tsc.cmd")
	}
	if _, err := os.Stat(launcher); err == nil {
		report(true, "tsc", "installed in webui/node_modules")
		return true
	}

	// Distinguish "installed on another OS" from "devDependencies skipped": the package
	// present but the launcher absent is the cross-platform case.
	if _, err := os.Stat(filepath.Join(nm, "typescript", "package.json")); err == nil {
		report(false, "tsc", "the "+runtime.GOOS+" launcher is missing from webui/node_modules/.bin")
		fmt.Println("                 node_modules was installed on another OS (e.g. WSL vs Windows).")
		fmt.Println("                 Reinstall for this platform: run `make deps`.")
		return false
	}
	report(false, "tsc", "missing from webui/node_modules — devDependencies were skipped")
	fmt.Println("                 (usually NODE_ENV=production). Fix: run `make deps`")
	fmt.Println("                 which forces `npm ci --include=dev` in webui/.")
	return false
}

// version returns the trimmed first line of `name args…`, or "" if it cannot be read.
func version(name string, args ...string) string {
	out, err := exec.Command(name, args...).Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(strings.SplitN(string(out), "\n", 2)[0])
}

// report prints one aligned ✓/✗ line. ASCII marks, so a Windows console with no UTF-8
// still reads cleanly.
func report(ok bool, name, detail string) {
	mark := "OK "
	if !ok {
		mark = "!! "
	}
	fmt.Printf("  %s %-12s %s\n", mark, name, detail)
}
