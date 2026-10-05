//go:build !windows

package main

import (
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The checks shell out to node/npm and look at webui/node_modules relative to the
// working directory, so every test runs in a temp directory with PATH pointed at fake
// tools it writes there. Nothing here depends on what the host has installed.

// fakeTool writes an executable shell script named name into dir that prints out.
func fakeTool(t *testing.T, dir, name, out string) {
	t.Helper()
	script := "#!/bin/sh\necho '" + out + "'\n"
	if err := os.WriteFile(filepath.Join(dir, name), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
}

// sandbox moves into a fresh directory and makes bin the whole PATH.
func sandbox(t *testing.T) (root, bin string) {
	t.Helper()
	root = t.TempDir()
	bin = filepath.Join(root, "bin")
	if err := os.MkdirAll(bin, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Chdir(root)
	t.Setenv("PATH", bin)
	return root, bin
}

// captureStdout runs fn and returns what it printed.
func captureStdout(t *testing.T, fn func()) string {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	old := os.Stdout
	os.Stdout = w
	defer func() { os.Stdout = old }()

	outc := make(chan string)
	go func() {
		b, _ := io.ReadAll(r)
		outc <- string(b)
	}()
	fn()
	w.Close()
	return <-outc
}

func mkdirs(t *testing.T, paths ...string) {
	t.Helper()
	for _, p := range paths {
		if err := os.MkdirAll(p, 0o755); err != nil {
			t.Fatal(err)
		}
	}
}

func touch(t *testing.T, p string) {
	t.Helper()
	mkdirs(t, filepath.Dir(p))
	if err := os.WriteFile(p, nil, 0o755); err != nil {
		t.Fatal(err)
	}
}

func TestParseNodeVersion(t *testing.T) {
	cases := []struct {
		raw          string
		major, minor int
		ok           bool
	}{
		{"v22.12.0", 22, 12, true},
		{" v20.19.1\n", 20, 19, true},
		{"23.0", 23, 0, true},
		{"v22", 0, 0, false},
		{"", 0, 0, false},
		{"vX.1.0", 0, 0, false},
		{"v22.x.0", 0, 0, false},
	}
	for _, c := range cases {
		major, minor, ok := parseNodeVersion(c.raw)
		if major != c.major || minor != c.minor || ok != c.ok {
			t.Errorf("parseNodeVersion(%q) = %d, %d, %v; want %d, %d, %v", c.raw, major, minor, ok, c.major, c.minor, c.ok)
		}
	}
}

// TestReportNodeVersion pins the requirement's edges, Node 21 above all: it sits
// between the two clauses and fails, which is easy to "fix" by accident.
func TestReportNodeVersion(t *testing.T) {
	cases := []struct {
		version string
		ok      bool
		want    string
	}{
		{"v20.19.0", true, "OK "},
		{"v20.18.9", false, "too old"},
		{"v21.7.0", false, "too old"},
		{"v22.11.0", false, "too old"},
		{"v22.12.0", true, "OK "},
		{"v24.1.0", true, "OK "},
		{"garbage", true, "could not read the version"},
	}
	for _, c := range cases {
		t.Run(c.version, func(t *testing.T) {
			_, bin := sandbox(t)
			fakeTool(t, bin, "node", c.version)

			var ok bool
			out := captureStdout(t, func() { ok = reportNodeVersion() })
			if ok != c.ok {
				t.Errorf("reportNodeVersion() = %v, want %v; output:\n%s", ok, c.ok, out)
			}
			if !strings.Contains(out, c.want) {
				t.Errorf("output missing %q:\n%s", c.want, out)
			}
		})
	}
}

func TestToolReportsVersionOrFix(t *testing.T) {
	_, bin := sandbox(t)
	fakeTool(t, bin, "npm", "10.9.2\nsecond line ignored")

	var ok bool
	out := captureStdout(t, func() { ok = tool("npm", "reinstall") })
	if !ok || !strings.Contains(out, "10.9.2") || strings.Contains(out, "second line") {
		t.Errorf("tool(npm) = %v, output:\n%s", ok, out)
	}

	out = captureStdout(t, func() { ok = tool("absent", "install it") })
	if ok || !strings.Contains(out, "!! ") || !strings.Contains(out, "install it") {
		t.Errorf("tool(absent) = %v, output:\n%s", ok, out)
	}
}

func TestVersionUnreadable(t *testing.T) {
	sandbox(t)
	if got := version("nope", "--version"); got != "" {
		t.Errorf("version of a missing tool = %q, want empty", got)
	}
}

// TestReportTSC walks the four states of webui/node_modules the doc comment names.
func TestReportTSC(t *testing.T) {
	cases := []struct {
		name  string
		setup func(t *testing.T)
		ok    bool
		want  string
	}{
		{"not installed", func(t *testing.T) {}, true, "not installed yet"},
		{"launcher present", func(t *testing.T) {
			touch(t, filepath.Join("webui", "node_modules", ".bin", "tsc"))
		}, true, "installed in webui/node_modules"},
		{"installed on another OS", func(t *testing.T) {
			touch(t, filepath.Join("webui", "node_modules", "typescript", "package.json"))
		}, false, "installed on another OS"},
		{"devDependencies skipped", func(t *testing.T) {
			mkdirs(t, filepath.Join("webui", "node_modules"))
		}, false, "devDependencies were skipped"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			sandbox(t)
			c.setup(t)
			var ok bool
			out := captureStdout(t, func() { ok = reportTSC() })
			if ok != c.ok || !strings.Contains(out, c.want) {
				t.Errorf("reportTSC() = %v, want %v with %q; output:\n%s", ok, c.ok, c.want, out)
			}
		})
	}
}

// TestMainAllPresent runs the whole check on a toolchain that passes. The failing
// outcome ends in os.Exit, so it is covered through its parts above instead.
func TestMainAllPresent(t *testing.T) {
	_, bin := sandbox(t)
	fakeTool(t, bin, "go", "go version go1.26.0 linux/amd64")
	fakeTool(t, bin, "node", "v22.12.0")
	fakeTool(t, bin, "npm", "10.9.2")
	touch(t, filepath.Join("webui", "node_modules", ".bin", "tsc"))

	out := captureStdout(t, main)
	for _, want := range []string{"go1.26.0", "v22.12.0", "10.9.2", "installed in webui/node_modules", "All prerequisites present."} {
		if !strings.Contains(out, want) {
			t.Errorf("output missing %q:\n%s", want, out)
		}
	}
}
