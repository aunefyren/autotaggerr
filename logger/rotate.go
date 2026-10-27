package logger

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
)

// LogPath is where the log file is written, relative to the working directory like
// the rest of config/.
const LogPath = "config/autotaggerr.log"

// rotatingFile is the log file with a size cap. Past the cap the file is renamed to
// <path>.1 (shifting .1 to .2 and so on), and only keep rotated files are kept, so the
// log takes at most (keep+1) × maxBytes. It used to be appended to forever — on an
// install with debug logging that is the largest thing in config/.
//
// Rotation happens before a write that would cross the cap, never in the middle of
// one, so a line is never split across two files. The limits are swapped under the
// mutex by SetRotation, which is how a settings save applies them without a restart.
type rotatingFile struct {
	mu       sync.Mutex
	path     string
	file     *os.File
	size     int64
	maxBytes int64
	keep     int
}

func openRotating(path string, maxBytes int64, keep int) (*rotatingFile, error) {
	r := &rotatingFile{path: path, maxBytes: maxBytes, keep: keep}
	if err := r.open(); err != nil {
		return nil, err
	}
	return r, nil
}

func (r *rotatingFile) open() error {
	f, err := os.OpenFile(r.path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o666)
	if err != nil {
		return err
	}
	info, err := f.Stat()
	if err != nil {
		_ = f.Close()
		return err
	}
	r.file, r.size = f, info.Size()
	return nil
}

func (r *rotatingFile) Write(p []byte) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.maxBytes > 0 && r.size > 0 && r.size+int64(len(p)) > r.maxBytes {
		if err := r.rotate(); err != nil {
			// Keep logging to the file we have rather than losing the line: a log
			// that grows past its cap is better than one that goes quiet.
			fmt.Fprintf(os.Stderr, "log rotation failed: %v\n", err)
		}
	}
	n, err := r.file.Write(p)
	r.size += int64(n)
	return n, err
}

// rotate shifts path.N-1 → path.N … path → path.1, drops what is past keep, and opens a
// fresh file. Called with the mutex held.
func (r *rotatingFile) rotate() error {
	if err := r.file.Close(); err != nil {
		return err
	}
	for i := r.keep; i >= 1; i-- {
		from := r.path
		if i > 1 {
			from = fmt.Sprintf("%s.%d", r.path, i-1)
		}
		if _, err := os.Stat(from); err == nil {
			_ = os.Rename(from, fmt.Sprintf("%s.%d", r.path, i))
		}
	}
	r.removeBeyondKeep()
	return r.open()
}

// removeBeyondKeep deletes rotated files numbered past keep — the ones a lower keep,
// saved since they were made, no longer wants.
func (r *rotatingFile) removeBeyondKeep() {
	for _, f := range rotatedFiles(r.path) {
		if f.index > r.keep {
			_ = os.Remove(f.path)
		}
	}
}

func (r *rotatingFile) setLimits(maxBytes int64, keep int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.maxBytes, r.keep = maxBytes, keep
	r.removeBeyondKeep()
}

type rotated struct {
	path  string
	index int
}

// rotatedFiles lists <path>.N files, lowest N first.
func rotatedFiles(path string) []rotated {
	matches, _ := filepath.Glob(path + ".*")
	out := []rotated{}
	for _, m := range matches {
		n, err := strconv.Atoi(strings.TrimPrefix(m, path+"."))
		if err == nil && n > 0 {
			out = append(out, rotated{path: m, index: n})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].index < out[j].index })
	return out
}

var active *rotatingFile

// SetRotation applies new limits to the running log, in megabytes and files kept.
// A no-op before InitLogger has opened the file.
func SetRotation(maxSizeMB, keep int) {
	if active == nil {
		return
	}
	active.setLimits(int64(maxSizeMB)<<20, keep)
}

// LogFiles is the log and its rotated files, for the storage overview.
func LogFiles() []string {
	out := []string{LogPath}
	for _, f := range rotatedFiles(LogPath) {
		out = append(out, f.path)
	}
	return out
}
