package components

import (
	"errors"
	"fmt"
	"os"
	"strings"
	"sync/atomic"

	"github.com/aunefyren/autotaggerr/logger"
	"github.com/aunefyren/autotaggerr/models"
	"github.com/aunefyren/autotaggerr/modules"
	"github.com/google/uuid"
	"gorm.io/gorm"
)

// ErrPinHeld means a pinned file's audio is no longer the audio the pin was made for,
// so the pin is held for review rather than applied (see LibraryItem.PinHeld).
var ErrPinHeld = errors.New("manual identification held for review: the file's audio changed since it was attached")

// replacedPinPolicy is the process's models.ReplacedPin* policy. A package value set
// by the runner at the start of each run (SetReplacedPinPolicy), the way the
// MusicBrainz rate limit is pushed in, rather than a parameter on every ProcessFile
// caller: it is process config, and a settings edit applies from the next run.
var replacedPinPolicy atomic.Value

// SetReplacedPinPolicy sets what ProcessFile does with a pin whose audio was replaced.
// Anything that is not a known policy reads as review.
func SetReplacedPinPolicy(policy string) {
	replacedPinPolicy.Store(policy)
}

func currentReplacedPinPolicy() string {
	p, _ := replacedPinPolicy.Load().(string)
	switch p {
	case models.ReplacedPinKeep, models.ReplacedPinRelease:
		return p
	}
	return models.ReplacedPinReview
}

// audioPayloadHash is modules.AudioPayloadHash behind a seam, so the pin tests can tell
// two files apart without synthesising audio.
var audioPayloadHash = modules.AudioPayloadHash

// indexedItem loads a file's index row; ok is false when the file is not indexed (or
// there is no database to ask).
func indexedItem(db *gorm.DB, filePath string) (models.LibraryItem, bool) {
	if db == nil {
		return models.LibraryItem{}, false
	}
	var item models.LibraryItem
	if err := db.Where("path = ?", filePath).Limit(1).Find(&item).Error; err != nil || item.ID == uuid.Nil {
		return models.LibraryItem{}, false
	}
	return item, true
}

// bytesChanged reports whether the file on disk is not the one the index last
// recorded: not indexed at all, or a different size or modification second. Every
// write of ours is recorded straight after it, so this is true only for a file
// something else put or changed there — exactly the file a cached manager answer may
// not describe.
func bytesChanged(item models.LibraryItem, indexed bool, filePath string) bool {
	if !indexed || item.ModTime == nil {
		return true
	}
	fi, err := os.Stat(filePath)
	if err != nil {
		return true
	}
	return item.Size != fi.Size() || item.ModTime.Unix() != fi.ModTime().Unix()
}

// pinnedCorrelationOf returns the stored correlation of a pinned row that holds a
// release. Anything else reports false so the caller resolves normally.
func pinnedCorrelationOf(item models.LibraryItem, indexed bool) (models.Correlation, bool) {
	if !indexed || !item.Pinned || item.MBReleaseID == "" {
		return models.Correlation{}, false
	}
	return models.Correlation{
		MBReleaseID:      item.MBReleaseID,
		MBReleaseTrackID: item.MBReleaseTrackID,
		MBRecordingID:    item.MBRecordingID,
		Source:           item.CorrelationSource,
	}, true
}

// checkPin decides whether a pinned file's pin still applies. hold is true when the
// file's audio is not the audio the pin was made for; hash is the audio hash to record
// against the pin when it applies and one was computed ("" when nothing new is known).
//
// The hash is only read when it can say something: when the bytes changed (a tag
// write and a replaced file both move size and mtime; only the second moves the audio
// hash), or when the pin has no hash yet. An unchanged file with a recorded hash costs
// nothing.
//
// A pin with no recorded hash adopts whatever audio the file has now. That is a blind
// spot for a file replaced before its pin was ever hashed, and there is no avoiding
// it: nothing recorded what the audio used to be. BackfillPinnedAudioHashes shrinks it
// to the files replaced before the first boot that had this check.
//
// A file whose audio cannot be read is not held. The tag write that follows will fail
// on the same file and say why, and holding it would park a read error on the review
// list as if it were a question about identity.
func checkPin(item models.LibraryItem, changed bool, filePath string) (hold bool, hash string) {
	if item.PinHeld {
		return true, ""
	}
	if !changed && item.PinnedAudioHash != "" {
		return false, ""
	}
	h, err := audioPayloadHash(filePath)
	if err != nil {
		logger.Log.Warnf("could not hash the audio of pinned file %q, applying its pin unchecked: %s", filePath, err.Error())
		return false, ""
	}
	if item.PinnedAudioHash != "" && h != item.PinnedAudioHash {
		logger.Log.Warnf("pinned file %q holds different audio than when it was attached; holding its pin for review", filePath)
		return true, ""
	}
	return false, h
}

// holdPin marks a file's pin as held. Written as a column update after recordItem has
// stored the attempt, so the two cannot overwrite each other.
func holdPin(db *gorm.DB, filePath string) {
	if db == nil {
		return
	}
	if err := db.Model(&models.LibraryItem{}).Where("path = ?", filePath).Update("pin_held", true).Error; err != nil {
		logger.Log.Warnf("failed to hold the pin on %q: %s", filePath, err.Error())
	}
}

// keepReplacedPin confirms a pin for the audio its file holds now — the automatic form
// of *Keep pin*. It returns the new hash; "" means the audio could not be read, and the
// caller holds the pin instead, since there would be nothing to record it against.
func keepReplacedPin(db *gorm.DB, filePath string) string {
	h, err := audioPayloadHash(filePath)
	if err != nil {
		logger.Log.Warnf("could not hash the audio of pinned file %q to keep its pin: %s", filePath, err.Error())
		return ""
	}
	if db != nil {
		if err := db.Model(&models.LibraryItem{}).Where("path = ?", filePath).Updates(map[string]any{
			"pin_held": false, "pinned_audio_hash": h,
		}).Error; err != nil {
			logger.Log.Warnf("failed to keep the pin on %q: %s", filePath, err.Error())
		}
	}
	return h
}

// releaseReplacedPin drops a pin and its hold — the automatic form of *Release pin*.
// The file is then identified like any other, in the same pass.
func releaseReplacedPin(db *gorm.DB, filePath string) {
	if db == nil {
		return
	}
	if err := db.Model(&models.LibraryItem{}).Where("path = ?", filePath).Updates(map[string]any{
		"pinned": false, "pin_held": false, "pinned_audio_hash": "",
	}).Error; err != nil {
		logger.Log.Warnf("failed to release the pin on %q: %s", filePath, err.Error())
	}
}

// ReplacedPinWarning says what the automatic policies did in a run, or "" when they
// did nothing. A pin is a decision someone made by hand, so changing it without asking
// is reported on the run even though nothing failed — with the files named, so the
// decision can be undone (re-attach, or attach again after a release).
func ReplacedPinWarning(d *DetailCollector) string {
	keptTotal, releasedTotal, kept, released := d.ReplacedPins()
	var parts []string
	if keptTotal > 0 {
		parts = append(parts, fmt.Sprintf("%d pinned file(s) had new audio and kept their pin: %s", keptTotal, strings.Join(kept, ", ")))
	}
	if releasedTotal > 0 {
		parts = append(parts, fmt.Sprintf("%d pinned file(s) had new audio and were released to their manager: %s", releasedTotal, strings.Join(released, ", ")))
	}
	if len(parts) == 0 {
		return ""
	}
	return strings.Join(parts, ". ") + " (Settings → Manual identifications)."
}

// recordPinHash stores the audio hash a pin now stands for.
func recordPinHash(db *gorm.DB, filePath, hash string) {
	if db == nil || hash == "" {
		return
	}
	if err := db.Model(&models.LibraryItem{}).Where("path = ?", filePath).Update("pinned_audio_hash", hash).Error; err != nil {
		logger.Log.Warnf("failed to record the audio hash of pinned file %q: %s", filePath, err.Error())
	}
}

// BackfillPinnedAudioHashes records the audio hash of every pin that has none, for
// files that are still byte-identical to what the index last recorded — the only ones
// whose current audio is known to be the audio the pin was made for. A file that
// changed since is left alone; checkPin adopts its audio the next time it is processed,
// which is the blind spot described there.
//
// Pins are hand-made and few, so this reads a few files once at startup rather than
// waiting for each pinned file to change — by which point there would be nothing left
// to compare it against. Idempotent: a pin with a hash is never read again here.
func BackfillPinnedAudioHashes(db *gorm.DB) int {
	if db == nil {
		return 0
	}
	var items []models.LibraryItem
	if err := db.Where("pinned = ? AND pinned_audio_hash = ?", true, "").Find(&items).Error; err != nil {
		logger.Log.Warnf("failed to load pins to hash: %s", err.Error())
		return 0
	}
	filled := 0
	for _, item := range items {
		if bytesChanged(item, true, item.Path) {
			continue
		}
		h, err := audioPayloadHash(item.Path)
		if err != nil {
			logger.Log.Debugf("could not hash pinned file %q: %s", item.Path, err.Error())
			continue
		}
		recordPinHash(db, item.Path, h)
		filled++
	}
	if filled > 0 {
		logger.Log.Infof("recorded the audio hash of %d pinned file(s)", filled)
	}
	return filled
}

// PinAudioHash hashes a file's audio for recording against a pin — when someone
// attaches the file, or confirms a held pin for the audio it now holds.
func PinAudioHash(filePath string) (string, error) {
	return audioPayloadHash(filePath)
}
