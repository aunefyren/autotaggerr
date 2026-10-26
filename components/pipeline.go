package components

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/aunefyren/autotaggerr/logger"
	"github.com/aunefyren/autotaggerr/models"
	"github.com/aunefyren/autotaggerr/modules"
	"github.com/google/uuid"
	"gorm.io/gorm"
)

// DetailCollector gathers the per-file outcomes of a run — which files changed, the
// exact fields that changed on each, and which failed — so the Activity feed can
// answer "what did that scan actually do to my files". Counters alone cannot: they
// say twelve files changed, never which twelve.
//
// It is concurrency-safe because scans process files on a worker pool, and bounded
// because a large library would otherwise produce a row per file: once `limit`
// entries are held, further ones are counted and dropped, so the UI can say "showing
// the first N of M" instead of the process growing without limit. A nil collector
// disables collection entirely, which is what DB-less and single-file callers want.
type DetailCollector struct {
	mu      sync.Mutex
	limit   int
	items   []models.EventItem
	changed int // total changed files seen, including any past the limit
	failed  int // total failed files seen, including any past the limit
	// noMetaflac counts the failures that were only metaflac being missing, so the
	// run can say so once instead of leaving it to be read off every row.
	noMetaflac int
	// transient counts the failures expected to pass on their own (TransientFailures).
	transient int
	// pinsKept and pinsReleased are the files whose replaced audio the automatic
	// pin policy decided about (ReplacedPinWarning), capped at maxReplacedPinPaths
	// each; pinsKeptTotal / pinsReleasedTotal keep counting past the cap.
	pinsKept, pinsReleased           []string
	pinsKeptTotal, pinsReleasedTotal int
}

// maxReplacedPinPaths caps how many files a replaced-pin warning names. Pins are
// hand-made and few, so this is rarely reached; past it the count still says how many.
const maxReplacedPinPaths = 10

// AddReplacedPin records that the automatic pin policy kept or released a pin.
func (d *DetailCollector) AddReplacedPin(path string, kept bool) {
	if d == nil {
		return
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if kept {
		d.pinsKeptTotal++
		if len(d.pinsKept) < maxReplacedPinPaths {
			d.pinsKept = append(d.pinsKept, path)
		}
		return
	}
	d.pinsReleasedTotal++
	if len(d.pinsReleased) < maxReplacedPinPaths {
		d.pinsReleased = append(d.pinsReleased, path)
	}
}

// ReplacedPins returns how many pins the automatic policy kept and released, and the
// files it names for each, with a trailing "and N more" when there were more.
func (d *DetailCollector) ReplacedPins() (keptTotal, releasedTotal int, kept, released []string) {
	if d == nil {
		return 0, 0, nil, nil
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	withMore := func(paths []string, total int) []string {
		out := append([]string(nil), paths...)
		if total > len(paths) {
			out = append(out, fmt.Sprintf("and %d more", total-len(paths)))
		}
		return out
	}
	return d.pinsKeptTotal, d.pinsReleasedTotal,
		withMore(d.pinsKept, d.pinsKeptTotal), withMore(d.pinsReleased, d.pinsReleasedTotal)
}

// NewDetailCollector returns a collector holding at most limit entries. A limit < 1
// disables collection (the collector stays usable, so callers need no nil checks).
func NewDetailCollector(limit int) *DetailCollector {
	return &DetailCollector{limit: limit}
}

// AddChanged records a file whose tags were rewritten, with its field-level diff.
func (d *DetailCollector) AddChanged(path string, tagsWritten int, changes []models.TagChange) {
	if d == nil {
		return
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	d.changed++
	d.append(models.EventItem{
		Path:        path,
		Status:      models.EventItemStatusChanged,
		TagsWritten: tagsWritten,
		Changes:     changes,
	})
}

// AddError records a file that could not be processed.
func (d *DetailCollector) AddError(path string, err error) {
	if d == nil || err == nil {
		return
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	d.failed++
	if errors.Is(err, modules.ErrMetaflacMissing) {
		d.noMetaflac++
	}
	if errors.Is(err, modules.ErrTransient) {
		d.transient++
	}
	d.append(models.EventItem{
		Path:   path,
		Status: models.EventItemStatusError,
		Error:  err.Error(),
	})
}

// Adopt folds another collector's rows into this one under the given phase, and adds
// its totals to this one's. It is how a run's tagging stage reports both halves of what
// it wrote — the files the walk found changed on disk, and the files re-tagged because
// their release changed upstream — as one event whose detail list still keeps the two
// apart.
//
// The adopted rows are kept **whole, past this collector's limit**: they carry their
// own bound already (the caller's collector has one), and there are a handful of them
// against a walk that can fill 500 rows on its own. Letting a big walk starve them would
// drop precisely the rows nothing else records — the run's counters say a file was
// re-tagged, only these say which.
func (d *DetailCollector) Adopt(other *DetailCollector, phase string) {
	if d == nil || other == nil {
		return
	}
	items := other.Items()
	changed, failed := other.Totals()
	noMetaflac := other.MetaflacMissing()
	transient := other.TransientFailures()

	d.mu.Lock()
	defer d.mu.Unlock()
	d.changed += changed
	d.failed += failed
	d.noMetaflac += noMetaflac
	d.transient += transient
	for _, item := range items {
		item.Phase = phase
		d.items = append(d.items, item)
	}
}

// append stores an entry if there is room. Callers hold the lock.
func (d *DetailCollector) append(item models.EventItem) {
	if d.limit < 1 || len(d.items) >= d.limit {
		return
	}
	d.items = append(d.items, item)
}

// Items returns the collected entries, safe to use after the run.
func (d *DetailCollector) Items() []models.EventItem {
	if d == nil {
		return nil
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	return append([]models.EventItem(nil), d.items...)
}

// Limit is the cap Items was collected under, so a caller reporting "showing 500 of
// 3120" can name the figure without being told it separately — the collector is
// already the thing that knows.
func (d *DetailCollector) Limit() int {
	if d == nil {
		return 0
	}
	return d.limit
}

// Totals reports how many changed and failed files were seen in total — including
// those past the limit, which is what makes a truncated list honest about it.
func (d *DetailCollector) Totals() (changed, failed int) {
	if d == nil {
		return 0, 0
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.changed, d.failed
}

// TransientFailures is how many of the failed files failed for a reason expected to
// pass on its own (modules.ErrTransient) — the part of a run's failures the next run
// retries with nothing done about them.
func (d *DetailCollector) TransientFailures() int {
	if d == nil {
		return 0
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.transient
}

// MetaflacMissing is how many of the failed files failed only because metaflac is
// not installed.
func (d *DetailCollector) MetaflacMissing() int {
	if d == nil {
		return 0
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.noMetaflac
}

// MetaflacWarning is the run-level warning for those files, or "" when there are none.
func MetaflacWarning(d *DetailCollector) string {
	n := d.MetaflacMissing()
	if n == 0 {
		return ""
	}
	return fmt.Sprintf("metaflac is not installed, so %d FLAC file(s) that needed new tags were not written. Install the flac package (apt install flac; the Docker image already has it) — the next run retries them. MP3s are unaffected.", n)
}

// ProcessFile runs the per-file pipeline for one library: the Manager resolves
// the correlation, the Tagger writes tags (via the shared engine), and the
// result is recorded into the library_items index. A nil db skips indexing (used
// by tests / DB-less callers); a nil plexClient/refreshSet skips Plex queuing, and a
// nil detail collector skips per-file Activity detail.
//
// A pinned item is the exception: its correlation was chosen by hand, so it is
// reused as-is instead of being resolved again. That governs the tags written to the
// file as well as the index row — resolving would otherwise tag a pinned file to
// whatever the manager thinks it is, contradicting the pin the index still reports.
func ProcessFile(
	db *gorm.DB,
	library models.Library,
	manager Manager,
	tagger *Tagger,
	plexClient *modules.PlexClient,
	refreshSet *modules.AlbumRefreshSet,
	detail *DetailCollector,
	filePath, rootDir, processedVersion string,
) (unchanged bool, tagsWritten int, err error) {
	managerType := manager.Type()
	// The shared client narrowed to this library's Plex section, or nil when the
	// library is not refreshed in Plex.
	plexClient = plexClient.ForLibrary(library)

	item, indexed := indexedItem(db, filePath)
	changed := bytesChanged(item, indexed, filePath)

	correlation, pinned := pinnedCorrelationOf(item, indexed)
	var pinHash string
	if pinned {
		var hold bool
		if hold, pinHash = checkPin(item, changed, filePath); hold {
			// The audio under the pin is not what it was made for. The policy decides:
			// keep it for the new audio, release it to the manager, or (the default)
			// hold it for a person. Keeping falls back to holding when the audio
			// cannot be read, since there would be nothing to record the pin against.
			switch currentReplacedPinPolicy() {
			case models.ReplacedPinKeep:
				if pinHash = keepReplacedPin(db, filePath); pinHash != "" {
					hold = false
					detail.AddReplacedPin(filePath, true)
				}
			case models.ReplacedPinRelease:
				releaseReplacedPin(db, filePath)
				detail.AddReplacedPin(filePath, false)
				hold, pinned = false, false
				correlation = models.Correlation{}
				// The audio is new to whatever the manager has cached, whether or not
				// the index already recorded its size when the pin was held.
				changed = true
			}
		}
		if hold {
			// Neither applied nor dropped: the file is left untagged, keeps the
			// identity on its row, and waits for someone to confirm or release it.
			err = fmt.Errorf("%w (%s)", ErrPinHeld, filepath.Base(filePath))
			recordItem(db, library.ID, filePath, models.Correlation{}, false, processedVersion, managerType, err)
			holdPin(db, filePath)
			detail.AddError(filePath, err)
			return false, 0, err
		}
	}
	if !pinned {
		// A file the index has not seen in this state is the one a cached manager
		// answer may not describe, so it is resolved against fresh answers.
		correlate := manager.Correlate
		if fc, ok := manager.(freshCorrelator); ok && changed {
			correlate = fc.CorrelateFresh
		}
		correlation, err = correlate(filePath, rootDir)
		if err != nil {
			// An unmatched file is a state, not a failure: the manager owns identity and
			// does not know this file. Record it as unmatched (so it drops out of the
			// collection and the next scan re-attempts it) but do not count it as an
			// error or abort — the rest of the library is fine.
			if errors.Is(err, modules.ErrUnmatched) {
				recordItem(db, library.ID, filePath, models.Correlation{}, true, processedVersion, managerType, err)
				return true, 0, nil
			}
			recordItem(db, library.ID, filePath, models.Correlation{}, false, processedVersion, managerType, err)
			detail.AddError(filePath, err)
			return false, 0, err
		}
	}

	unchanged = true // no tag write unless the profile enables it
	if tagger.WriteEnabled() {
		var changes []models.TagChange
		// By pointer: a track re-matched in a rebuilt release comes back with its new
		// IDs, and the index records those rather than the ones that no longer exist.
		unchanged, tagsWritten, changes, err = modules.TagCorrelatedFile(filePath, &correlation, plexClient, refreshSet, rootDir, tagger.Settings())
		if err != nil {
			recordItem(db, library.ID, filePath, correlation, false, processedVersion, managerType, err)
			detail.AddError(filePath, err)
			return unchanged, tagsWritten, err
		}
		if !unchanged {
			detail.AddChanged(filePath, tagsWritten, changes)
		}
	}

	recordItemTagged(db, library.ID, filePath, correlation, unchanged, processedVersion, managerType, nil, tagger.Fingerprint())
	recordPinHash(db, filePath, pinHash)
	return unchanged, tagsWritten, nil
}

// ScanLibrary processes one library end-to-end: it builds the library's manager
// and tagger, then walks the folder (shared worker pool), skipping files whose
// index entry shows they are unchanged since the last successful scan and
// processing the rest through ProcessFile. Counters match modules.ScanFolderRecursive.
func ScanLibrary(
	db *gorm.DB,
	library models.Library,
	plexClient *modules.PlexClient,
	refreshSet *modules.AlbumRefreshSet,
	detail *DetailCollector,
	processedVersion string,
	workers int,
) (counter, unchangedFiles, tagsWritten int, errorFiles []string, err error) {
	return ScanLibraryRoots(context.Background(), db, library, nil, plexClient, refreshSet, detail, processedVersion, workers, false, nil)
}

// ScanLibraryRoots is ScanLibrary narrowed to part of a library: it walks each of
// `roots` instead of the library folder, and an empty roots walks the whole library
// (which is exactly what ScanLibrary asks for).
//
// The distinction that makes a partial scan safe is between the folder *walked* and
// the folder files are *resolved against*. Only the walk narrows; `library.Path`
// stays the correlation root, because the path convention the manager reads
// (<root>/<ARTIST>/<ALBUM>/...) is anchored there. Passing the narrowed folder as the
// root instead would make every file one segment too shallow and correlate the whole
// scope to the wrong album.
//
// Roots are walked in sequence, each with the full worker pool, and their counters
// summed. A root that no longer exists on disk is walked as an empty folder — the
// zero counters that produces are the honest report for "the folder is gone", and it
// keeps one stale target from abandoning the others.
//
// onFile, if non-nil, is called once per processed file with its path; it is passed
// straight through to WalkAndProcess so the scan runner can advance a live progress
// counter. It must be cheap and concurrency-safe.
//
// force re-processes every file in scope even when shouldSkip would drop it as
// unchanged on disk. It exists for the manager repair path: a release selection
// changed in Lidarr does not touch a byte on disk, so the only way to pull the new
// correlation down onto already-tagged files is to ignore the skip cache for one run.
func ScanLibraryRoots(
	ctx context.Context,
	db *gorm.DB,
	library models.Library,
	roots []string,
	plexClient *modules.PlexClient,
	refreshSet *modules.AlbumRefreshSet,
	detail *DetailCollector,
	processedVersion string,
	workers int,
	force bool,
	onFile func(path string),
) (counter, unchangedFiles, tagsWritten int, errorFiles []string, err error) {
	manager, tagger, err := BuildForLibrary(db, library)
	if err != nil {
		return 0, 0, 0, nil, err
	}
	if len(roots) == 0 {
		roots = []string{library.Path}
	}

	managerType := manager.Type()
	taggedWith := tagger.Fingerprint()
	writesTempo := tagger.WritesBPM()
	errorFiles = []string{}
	for _, root := range roots {
		c, u, tw, errs, walkErr := modules.WalkAndProcessContext(ctx, root, workers, func(path string) (bool, int, error) {
			if !force && shouldSkip(db, path, processedVersion, managerType, taggedWith) && !(writesTempo && tempoPending(db, path)) {
				return true, 0, nil // counts as unchanged
			}
			return ProcessFile(db, library, manager, tagger, plexClient, refreshSet, detail, path, library.Path, processedVersion)
		}, onFile)
		counter += c
		unchangedFiles += u
		tagsWritten += tw
		errorFiles = append(errorFiles, errs...)
		if walkErr != nil {
			return counter, unchangedFiles, tagsWritten, errorFiles, walkErr
		}
	}
	return counter, unchangedFiles, tagsWritten, errorFiles, nil
}

// shouldSkip reports whether a file can be skipped this scan: its index row exists
// and is healthy (status ok with a correlation), the running app version still
// matches the one that tagged it, the correlation came from the library's current
// manager, and the file is byte-identical on disk (same size and modification
// second). It deliberately does not detect upstream MusicBrainz changes — that is
// the drift sync's job (M4).
//
// The tag-settings check does the same for the tagger: a profile edit, a library
// moved to another profile, or the library's BPM settings changing all leave the
// file byte-identical, so without it Process walked past every file and only Tag
// files applied the change.
//
// The manager check is what makes a manager swap take effect: without it a file
// keeps reporting the old correlation_source forever, because nothing else about
// it changed. Pinned items are exempt — a manual correlation is not the manager's
// to redo, and re-correlating one would overwrite the pin's MB IDs.
func shouldSkip(db *gorm.DB, filePath, processedVersion, managerType, taggedWith string) bool {
	if db == nil {
		return false
	}

	var item models.LibraryItem
	if err := db.Where("path = ?", filePath).First(&item).Error; err != nil {
		return false // not indexed yet
	}
	// A held pin waits on a person, and nothing a run could do would change that, so
	// it is passed over quietly instead of being reported as failed again every run.
	// The run that held it reported it once; Items lists it until it is settled.
	//
	// Under an automatic policy it is processed instead, so switching the setting also
	// settles the pins already waiting.
	if item.PinHeld {
		return currentReplacedPinPolicy() == models.ReplacedPinReview
	}
	if item.Status != models.LibraryItemStatusOK || item.MBReleaseID == "" {
		return false
	}
	if item.ProcessedVersion != processedVersion || item.ModTime == nil {
		return false
	}
	if !item.Pinned && item.CorrelatedByManager != managerType {
		return false // manager changed (or predates this column) -> re-correlate
	}
	if item.TaggedWith != taggedWith {
		return false // tag settings changed (or predate this column) -> re-tag
	}

	fi, err := os.Stat(filePath)
	if err != nil {
		return false
	}
	// Compare to the second: SQLite time round-tripping can drop sub-second precision.
	return item.Size == fi.Size() && item.ModTime.Unix() == fi.ModTime().Unix()
}

// tempoPending reports whether a file has a tempo analysed since it was last
// processed. Nothing about the file changes when the tempo analysis stores a result —
// that verb only reads audio — so without this an unchanged file would be skipped
// forever and its BPM never written. It is the drift stage's handover in miniature:
// one verb records a fact, the next processing run puts it in the file, and the
// re-stamped LastScannedAt makes the run after that skip it again.
//
// Only asked when the library writes BPM, so a library that analyses but
// does not write pays nothing for it.
func tempoPending(db *gorm.DB, filePath string) bool {
	if db == nil {
		return false
	}
	var stored models.TrackTempo
	if db.Where("path = ? AND bpm > 0", filePath).Limit(1).Find(&stored).RowsAffected == 0 {
		return false
	}
	var item models.LibraryItem
	if db.Where("path = ?", filePath).Limit(1).Find(&item).RowsAffected == 0 || item.LastScannedAt == nil {
		return true
	}
	return stored.AnalysedAt.After(*item.LastScannedAt)
}

// TaggerForLibrary returns just the tagger a library is configured with (no
// manager construction) — used by the drift sync's re-tag path.
func TaggerForLibrary(db *gorm.DB, library models.Library) *Tagger {
	return NewTagger(resolveTaggerProfile(db, library, true)).ForLibrary(library)
}

// BuildForLibrary assembles the manager and tagger a library is configured with,
// falling back to the first configured (or a native/default) component when the
// library has no explicit assignment.
func BuildForLibrary(db *gorm.DB, library models.Library) (Manager, *Tagger, error) {
	managerRow, err := resolveManagerRow(db, library, true)
	if err != nil {
		return nil, nil, err
	}
	manager, err := NewManager(managerRow)
	if err != nil {
		return nil, nil, err
	}
	return manager, TaggerForLibrary(db, library), nil
}

// recordItem upserts the library_items row for a file: its correlation, on-disk
// identity (size/mtime), and scan/tag timestamps. Failures to record are logged
// but never abort processing — the index is a cache of decisions, not the source
// of truth for whether a file was tagged.
func recordItem(db *gorm.DB, libraryID uuid.UUID, filePath string, correlation models.Correlation, unchanged bool, processedVersion, managerType string, procErr error) {
	recordItemTagged(db, libraryID, filePath, correlation, unchanged, processedVersion, managerType, procErr, "")
}

// recordItemTagged is recordItem for a file that went through the tagger: on success
// it also stamps the tag settings it was processed under (LibraryItem.TaggedWith),
// which skip-unchanged compares. Empty leaves the stamp alone — discovery records
// files without tagging them.
func recordItemTagged(db *gorm.DB, libraryID uuid.UUID, filePath string, correlation models.Correlation, unchanged bool, processedVersion, managerType string, procErr error, taggedWith string) {
	if db == nil {
		return
	}

	item := models.LibraryItem{Path: filePath}
	if err := db.Where("path = ?", filePath).FirstOrInit(&item).Error; err != nil {
		logger.Log.Warnf("failed to load library item for %q: %s", filePath, err.Error())
		return
	}

	now := time.Now()
	item.LibraryID = libraryID
	item.LastScannedAt = &now

	if fi, statErr := os.Stat(filePath); statErr == nil {
		item.Size = fi.Size()
		mod := fi.ModTime()
		item.ModTime = &mod
	}

	// Identity first, and unconditionally: what a file *is* was resolved before
	// anything was attempted on it, and it stays true whether or not the attempt
	// succeeded. This used to live in the success branch, which meant a MusicBrainz
	// outage mid-scan discarded a correlation the manager had just resolved — the
	// file then had no release to aggregate against, left the disk view, and its
	// album reported `not_indexed` against the manager. The files were indexed. The
	// index had been told to forget them.
	if correlation.MBReleaseID != "" {
		// Never override a manual/pinned correlation with an automatic one — the MB
		// IDs included, not just the source label. Guarding only the label left a
		// re-processed pinned file (version bump, edited file) pointing at the
		// manager's release while still reporting "manual".
		if !item.Pinned {
			item.MBReleaseID = correlation.MBReleaseID
			item.MBRecordingID = correlation.MBRecordingID
			item.MBReleaseTrackID = correlation.MBReleaseTrackID
			item.CorrelationSource = correlation.Source
			item.CorrelatedByManager = managerType
		}
		if item.CorrelatedAt == nil {
			item.CorrelatedAt = &now
		}
	}

	// Then the outcome of this attempt, which is a separate fact about a separate
	// thing. Nothing below is input to the collection — it is what an admin reads.
	switch {
	case procErr == nil:
		item.Status = models.LibraryItemStatusOK
		item.Error = ""
		item.LastErrorAt = nil
		item.LastErrorTransient = false
		// ProcessedVersion is stamped *only* here, and that is what makes a failed
		// file retry for free: shouldSkip refuses to skip a file whose version does
		// not match, so anything that failed is re-attempted on the very next run
		// with no queue, no backoff and no bookkeeping. Stamping it on a failure
		// path would silently turn a transient outage into a permanent skip.
		item.ProcessedVersion = processedVersion
		if taggedWith != "" {
			item.TaggedWith = taggedWith
		}
		if !unchanged {
			item.LastTaggedAt = &now
		}
	case errors.Is(procErr, modules.ErrUnmatched):
		// ErrUnmatched is its own state: the manager owns identity and does not know
		// this file. It is not an error the user must fix, so it carries no error text
		// and does not read as a failed file in the feed.
		//
		// The first miss on a file the manager *did* know is given the benefit of the
		// doubt (models.InDiskView): the commonest cause is Lidarr mid-import, between
		// replacing a file and re-listing it, and the follower's Process or the next run
		// settles it either way. A second miss in a row is the manager's answer.
		firstMiss := item.MBReleaseID != "" && item.Status != models.LibraryItemStatusUnmatched
		item.Status = models.LibraryItemStatusUnmatched
		item.Error = ""
		item.LastErrorAt = nil
		item.LastErrorTransient = firstMiss
		if firstMiss {
			item.LastErrorAt = &now
		}
	default:
		item.Status = models.LibraryItemStatusError
		item.Error = procErr.Error()
		item.LastErrorAt = &now
		item.LastErrorTransient = errors.Is(procErr, modules.ErrTransient)
	}

	if err := db.Save(&item).Error; err != nil {
		logger.Log.Warnf("failed to record library item for %q: %s", filePath, err.Error())
	}
}
