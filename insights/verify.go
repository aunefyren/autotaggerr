package insights

import (
	"context"
	"os"
	"time"

	"github.com/aunefyren/autotaggerr/models"
	"github.com/aunefyren/autotaggerr/modules"
	"gorm.io/gorm"
)

// VerifyResult is what one VerifyDuplicates pass did.
type VerifyResult struct {
	// Hashed files had their audio read.
	Hashed int
	// Reused files took a hash the tempo or loudness analysis had already taken of
	// the file as it is now, without reading it.
	Reused int
	// Fresh files already had a hash of the file as it is now; only the index was
	// behind (the file changed since the last scan, and the hash was taken after).
	Fresh int
	// Gone files are no longer on disk. Left to the scan to prune.
	Gone int
	// Failures are the files that could not be read, by path.
	Failures map[string]error
	// Cancelled is set when the context ended the pass early.
	Cancelled bool
}

// Hasher is modules.AudioPayloadHash, or a test's stand-in.
type Hasher func(path string) (string, error)

// VerifyDuplicates hashes the audio of each item and stores the hash with the size
// and mtime it was taken at. onFile is called after each file (may be nil), for
// progress. Results are stored as they are produced, so a cancelled pass loses
// nothing and the next one skips what this one finished.
func VerifyDuplicates(ctx context.Context, db *gorm.DB, items []models.LibraryItem, hash Hasher, onFile func(path string)) VerifyResult {
	if hash == nil {
		hash = modules.AudioPayloadHash
	}
	res := VerifyResult{Failures: map[string]error{}}
	for _, item := range items {
		if ctx.Err() != nil {
			res.Cancelled = true
			break
		}
		verifyOne(db, item, hash, &res)
		if onFile != nil {
			onFile(item.Path)
		}
	}
	return res
}

// verifyOne hashes one file into res: reusing a hash already taken of the file as it
// is now, reading the audio otherwise.
func verifyOne(db *gorm.DB, item models.LibraryItem, hash Hasher, res *VerifyResult) {
	info, err := os.Stat(item.Path)
	if err != nil {
		if os.IsNotExist(err) {
			res.Gone++
		} else {
			res.Failures[item.Path] = err
		}
		return
	}
	size, modTime := info.Size(), info.ModTime()

	if item.AudioHash != "" && item.AudioHashSize == size && item.AudioHashModTime != nil && item.AudioHashModTime.Equal(modTime) {
		res.Fresh++
		return
	}
	if h := analysedHash(db, item.Path, size, modTime); h != "" {
		if err := storeHash(db, item, h, size, modTime); err != nil {
			res.Failures[item.Path] = err
			return
		}
		res.Reused++
		return
	}
	h, err := hash(item.Path)
	if err != nil {
		res.Failures[item.Path] = err
		return
	}
	if err := storeHash(db, item, h, size, modTime); err != nil {
		res.Failures[item.Path] = err
		return
	}
	res.Hashed++
}

// analysedHash returns an audio hash the tempo or loudness analysis stored for the
// file as it is now (same size and mtime), or "". Both hash with
// modules.AudioPayloadHash, so the value is the same one reading the file would give.
// A pin's hash is not used: it carries no size or mtime to say it is still current.
func analysedHash(db *gorm.DB, path string, size int64, modTime time.Time) string {
	for _, model := range []any{&models.TrackTempo{}, &models.TrackLoudness{}} {
		var row struct {
			Size      int64
			ModTime   *time.Time
			AudioHash string
		}
		if db.Model(model).Select("size", "mod_time", "audio_hash").Where("path = ?", path).Limit(1).Scan(&row).RowsAffected == 0 {
			continue
		}
		if row.AudioHash != "" && row.Size == size && row.ModTime != nil && row.ModTime.Equal(modTime) {
			return row.AudioHash
		}
	}
	return ""
}

func storeHash(db *gorm.DB, item models.LibraryItem, hash string, size int64, modTime time.Time) error {
	return db.Model(&models.LibraryItem{}).Where("id = ?", item.ID).UpdateColumns(map[string]any{
		"audio_hash":          hash,
		"audio_hash_size":     size,
		"audio_hash_mod_time": modTime,
	}).Error
}
