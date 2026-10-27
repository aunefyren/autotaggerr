package storage

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/aunefyren/autotaggerr/events"
	"github.com/aunefyren/autotaggerr/models"
)

// Outcome is what an action did, as figures before and after and one sentence.
type Outcome struct {
	Before  int64  `json:"before"`
	After   int64  `json:"after"`
	Message string `json:"message"`
}

// ErrNotSQLite refuses the database actions on another engine.
var ErrNotSQLite = errors.New("only a SQLite database can be compacted or checkpointed here")

// Compact rewrites the database without its free pages (VACUUM), returning them to the
// disk. It needs room for a full copy of the database while it runs, so it refuses
// when the disk holding it has less free than the file is large — failing halfway
// through for want of space is the one outcome worse than not compacting.
func (m *Measurer) Compact() (Outcome, error) {
	defer m.Invalidate()
	db := m.database()
	if db.Error != "" && db.Path == "" {
		return Outcome{}, ErrNotSQLite
	}
	if d, err := diskUsage(filepath.Dir(db.Path)); err == nil && d.free < uint64(db.File+db.WAL) {
		return Outcome{}, fmt.Errorf("compacting needs %s free beside the database, and the disk has %s", human(db.File+db.WAL), human(int64(d.free)))
	}
	before := db.File + db.WAL
	if err := m.db.Exec("VACUUM").Error; err != nil {
		return Outcome{}, err
	}
	// VACUUM writes through the WAL; fold it back so the saving shows in the file.
	m.db.Exec("PRAGMA wal_checkpoint(TRUNCATE)")
	after := fileSize(db.Path) + fileSize(db.Path+"-wal")
	return Outcome{Before: before, After: after, Message: fmt.Sprintf("Database compacted from %s to %s", human(before), human(after))}, nil
}

// Checkpoint folds the write-ahead log back into the database file and truncates it.
// SQLite does this on its own as the log fills, but only partway while anything is
// reading, so on a busy install the log can sit larger than the database itself.
func (m *Measurer) Checkpoint() (Outcome, error) {
	defer m.Invalidate()
	db := m.database()
	if db.Path == "" {
		return Outcome{}, ErrNotSQLite
	}
	before := db.WAL
	var result struct{ Busy, Log, Checkpointed int64 }
	if err := m.db.Raw("PRAGMA wal_checkpoint(TRUNCATE)").Scan(&result).Error; err != nil {
		return Outcome{}, err
	}
	after := fileSize(db.Path + "-wal")
	msg := fmt.Sprintf("Write-ahead log shrunk from %s to %s", human(before), human(after))
	if result.Busy != 0 {
		msg = fmt.Sprintf("Write-ahead log partly folded back (%s → %s): something was reading the database — try again when nothing is running", human(before), human(after))
	}
	return Outcome{Before: before, After: after, Message: msg}, nil
}

// PruneActivity applies the Activity retention now rather than at the next finished
// run. The rows go at once; the file does not shrink until it is compacted, because
// SQLite keeps freed pages for reuse — the message says so.
func (m *Measurer) PruneActivity(keep int) (Outcome, error) {
	defer m.Invalidate()
	count := func() int64 {
		var events, items int64
		m.db.Model(&models.Event{}).Count(&events)
		m.db.Model(&models.EventItem{}).Count(&items)
		return events + items
	}
	before := count()
	events.Prune(m.db, keep)
	after := count()
	return Outcome{Before: before, After: after,
		Message: fmt.Sprintf("Removed %d Activity rows. The space is reused by new rows; compact the database to return it to the disk.", before-after)}, nil
}

// RemoveArtworkLeftovers deletes the artwork files nothing will serve (isLeftover)
// that are older than LeftoverAge. Younger ones are left: an image is written before
// its index row, so one may be mid-fetch.
func (m *Measurer) RemoveArtworkLeftovers() (Outcome, error) {
	defer m.Invalidate()
	var noImage []string
	if err := m.db.Model(&models.ArtworkCacheEntry{}).Where("missing = ?", true).Pluck("key", &noImage).Error; err != nil {
		return Outcome{}, err
	}
	missing := map[string]bool{}
	for _, k := range noImage {
		missing[k] = true
	}
	entries, err := os.ReadDir(m.paths.ArtworkDir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return Outcome{Message: "There is no artwork cache yet"}, nil
		}
		return Outcome{}, err
	}
	cutoff := m.now().Add(-LeftoverAge)
	var files, bytes int64
	for _, e := range entries {
		if e.IsDir() || !isLeftover(e.Name(), missing) {
			continue
		}
		info, err := e.Info()
		if err != nil || !info.ModTime().Before(cutoff) {
			continue
		}
		if err := os.Remove(filepath.Join(m.paths.ArtworkDir, e.Name())); err == nil {
			files++
			bytes += info.Size()
		}
	}
	return Outcome{Before: bytes, After: 0, Message: fmt.Sprintf("Removed %d leftover artwork files (%s)", files, human(bytes))}, nil
}

// human is a size for a sentence. The page formats its own figures; this is only for
// the messages the actions send back.
func human(b int64) string {
	const unit = 1024
	if b < unit {
		return fmt.Sprintf("%d B", b)
	}
	div, exp := int64(unit), 0
	for n := b / unit; n >= unit; n /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %cB", float64(b)/float64(div), "KMGTPE"[exp])
}
