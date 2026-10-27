package logger

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestRotatingFile: a write that would cross the cap rotates first, so the live file
// never passes it and no line is split; rotated files shift down and only keep of them
// stay; a lower keep saved later removes the extras at once.
func TestRotatingFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "app.log")
	r, err := openRotating(path, 30, 2)
	if err != nil {
		t.Fatal(err)
	}
	line := strings.Repeat("x", 9) + "\n" // 10 bytes
	for i := 0; i < 12; i++ {
		if _, err := r.Write([]byte(line)); err != nil {
			t.Fatal(err)
		}
	}
	read := func(p string) string {
		t.Helper()
		b, err := os.ReadFile(p)
		if err != nil {
			t.Fatalf("read %s: %v", p, err)
		}
		return string(b)
	}
	if got := len(read(path)); got > 30 || got%10 != 0 {
		t.Errorf("live file is %d bytes, want at most the cap and whole lines", got)
	}
	got := rotatedFiles(path)
	if len(got) != 2 || got[0].index != 1 || got[1].index != 2 {
		t.Fatalf("rotated = %+v, want .1 and .2 only", got)
	}
	if len(read(path+".1")) != 30 {
		t.Errorf(".1 = %d bytes, want a full file", len(read(path+".1")))
	}

	r.setLimits(30, 1)
	if got := rotatedFiles(path); len(got) != 1 || got[0].index != 1 {
		t.Errorf("after keep=1, rotated = %+v", got)
	}
	if err := r.file.Close(); err != nil {
		t.Fatal(err)
	}
}

// TestRotatedFilesIgnoresOthers: only <path>.N counts as a rotated log.
func TestRotatedFilesIgnoresOthers(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "app.log")
	for _, name := range []string{"app.log", "app.log.1", "app.log.bak", "app.log.0", "app.log.10"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	got := rotatedFiles(path)
	if len(got) != 2 || got[0].index != 1 || got[1].index != 10 {
		t.Errorf("rotated = %+v, want .1 and .10", got)
	}
}
