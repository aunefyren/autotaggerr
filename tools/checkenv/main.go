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
	"strconv"
	"strings"
)

func main() {
	fmt.Println("Checking build prerequisites…")

	// go is implied — this program could not run without it — but report it so the
	// list is complete.
	report(true, "go", version("go", "version"))

	// Node is the one tool whose *presence* is not the question, so it does not go
	// through `tool`: that would print an OK line for the version about to be rejected
	// on the next, and a report that contradicts itself two lines apart is worse than
	// no report. reportNodeVersion owns the single verdict.
	nodeOK := true
	if _, err := exec.LookPath("node"); err != nil {
		report(false, "node", "not found on PATH — install Node "+nodeRequirement+" from https://nodejs.org")
		nodeOK = false
	} else {
		nodeOK = reportNodeVersion()
	}
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

// nodeRequirement is vite's `engines.node`, in the form npm prints it. Kept as one
// string so the message and the check below cannot drift apart.
const nodeRequirement = "^20.19.0 || >=22.12.0"

// reportNodeVersion enforces that requirement. It is checked here rather than left to
// npm because npm reports it as an `EBADENGINE` warning in the middle of an install
// that then fails later for a reason that looks unrelated — precisely the raw,
// easy-to-miss error this program exists to replace.
//
// Note that Node 21 does not satisfy it: `^20.19.0` stops at 21.0.0, and the other
// clause starts at 22.12.0. Odd-numbered Node releases are not LTS, so that is the
// requirement working as intended rather than an oversight.
func reportNodeVersion() bool {
	raw := version("node", "--version") // "v22.12.0"
	major, minor, ok := parseNodeVersion(raw)
	if !ok {
		// Unreadable rather than wrong: say so, but do not fail a build over it.
		report(true, "node", raw+" (could not read the version; wanted "+nodeRequirement+")")
		return true
	}

	if (major == 20 && minor >= 19) || (major == 22 && minor >= 12) || major >= 23 {
		report(true, "node", raw)
		return true
	}
	report(false, "node", raw+" is too old — vite needs "+nodeRequirement)
	fmt.Println("                 Install Node 22 LTS from https://nodejs.org (or via nvm/fnm).")
	return false
}

// parseNodeVersion pulls the major and minor out of `node --version` output.
func parseNodeVersion(raw string) (major, minor int, ok bool) {
	parts := strings.SplitN(strings.TrimPrefix(strings.TrimSpace(raw), "v"), ".", 3)
	if len(parts) < 2 {
		return 0, 0, false
	}
	major, err := strconv.Atoi(parts[0])
	if err != nil {
		return 0, 0, false
	}
	minor, err = strconv.Atoi(parts[1])
	if err != nil {
		return 0, 0, false
	}
	return major, minor, true
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
