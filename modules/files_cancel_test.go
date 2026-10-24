package modules

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// TestWalkAndProcessStopsWhenCancelled: a cancelled walk starts no file and says it was
// stopped, so the caller can tell it from a finished walk and prune nothing on it.
func TestWalkAndProcessStopsWhenCancelled(t *testing.T) {
	root := t.TempDir()
	for _, name := range []string{"a.flac", "b.flac", "c.mp3"} {
		if err := os.WriteFile(filepath.Join(root, name), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	calls := 0
	n, _, _, _, err := WalkAndProcessContext(ctx, root, 1, func(string) (bool, int, error) {
		calls++
		return false, 0, nil
	}, nil)
	if !errors.Is(err, context.Canceled) {
		t.Errorf("err = %v, want context.Canceled", err)
	}
	if calls != 0 || n != 0 {
		t.Errorf("processed %d file(s) after the stop", calls)
	}
}
