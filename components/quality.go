package components

import (
	"os"

	"github.com/aunefyren/autotaggerr/logger"
	"github.com/aunefyren/autotaggerr/models"
	"github.com/aunefyren/autotaggerr/modules"
	"github.com/aunefyren/autotaggerr/quality"
	"github.com/google/uuid"
	"gorm.io/gorm"
)

// qualityCurrent reports whether an item's measured quality still describes the file
// as indexed: measured, at the size and mtime the row records. The AudioHash rule — a
// tag write moves both, so it is re-measured, but a header read is all that costs.
func qualityCurrent(item models.LibraryItem) bool {
	if item.Quality == "" || item.ModTime == nil || item.QualityModTime == nil {
		return false
	}
	return item.QualitySize == item.Size && item.QualityModTime.Unix() == item.ModTime.Unix()
}

// measureQuality reads a file's headers into its item's quality columns, unless they
// are current. Audio that cannot be read is recorded as quality.Unknown at this size
// and mtime, so it is not re-read every run; Unknown is never read as low quality.
// Reports whether the columns changed.
func measureQuality(item *models.LibraryItem, filePath string) bool {
	if qualityCurrent(*item) {
		return false
	}
	props, err := modules.ReadAudioProperties(filePath)
	if err != nil {
		item.Quality = string(quality.Unknown)
		item.Bitrate, item.SampleRate, item.BitDepth = 0, 0, 0
	} else {
		item.Quality = string(props.Quality())
		item.Bitrate, item.SampleRate, item.BitDepth = props.BitrateKbps, props.SampleRate, props.BitsPerSample
	}
	item.QualitySize = item.Size
	if item.ModTime != nil {
		mod := *item.ModTime
		item.QualityModTime = &mod
	}
	return true
}

// measureQualityIfStale measures a file Process is skipping as unchanged. Skipping
// never reaches recordItem, so without this a library indexed before quality existed
// would stay unmeasured until each file changed. Only the quality columns are
// written: the skip's whole point is that nothing else about the file moved.
func measureQualityIfStale(db *gorm.DB, filePath string) {
	if db == nil {
		return
	}
	var item models.LibraryItem
	if err := db.Where("path = ?", filePath).Limit(1).Find(&item).Error; err != nil || item.ID == uuid.Nil {
		return
	}
	if item.ModTime == nil {
		if fi, err := os.Stat(filePath); err == nil {
			mod := fi.ModTime()
			item.ModTime = &mod
		}
	}
	if !measureQuality(&item, filePath) {
		return
	}
	if err := db.Model(&models.LibraryItem{}).Where("id = ?", item.ID).Updates(map[string]any{
		"quality":          item.Quality,
		"bitrate":          item.Bitrate,
		"sample_rate":      item.SampleRate,
		"bit_depth":        item.BitDepth,
		"quality_size":     item.QualitySize,
		"quality_mod_time": item.QualityModTime,
	}).Error; err != nil {
		logger.Log.Warnf("failed to record the quality of %q: %s", filePath, err.Error())
	}
}
