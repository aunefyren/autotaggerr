package process

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/aunefyren/autotaggerr/collection"
	"github.com/aunefyren/autotaggerr/components"
	"github.com/aunefyren/autotaggerr/events"
	"github.com/aunefyren/autotaggerr/logger"
	"github.com/aunefyren/autotaggerr/models"
	"github.com/aunefyren/autotaggerr/modules"
	"github.com/google/uuid"
	"gorm.io/gorm"
)

// File import: loose files into a library.
//
// The person importing does what folder attach asks of them — picks the release and
// reviews the file → track mapping — and the import adds where each file goes, from
// the library's naming templates (modules.RenderLibraryPath). What lands is a pinned,
// tagged library item, exactly what a manual attach would have produced had the file
// already been in the library: the identity is a person's decision, so it is pinned,
// and a file whose tags fail to write is still imported and indexed (the next Process
// tags it), because moving it back would be the worse surprise.
//
// Planning is separate from doing, and the plan is made twice: once for the review,
// and again by the job when it runs, because the library may have changed while the
// job waited in the queue. A replacement is only ever of something the reviewer saw.

// PhaseImporting is the import job's file stage.
const PhaseImporting = "importing"

// MaxImportFiles bounds one import: one album, or a boxed set, at a time — the same
// bound as a bulk attach, for the same reason.
const MaxImportFiles = 200

// Why a planned file cannot simply be placed at its target.
const (
	// ImportConflictExists: a file is already at the target path.
	ImportConflictExists = "exists"
	// ImportConflictDuplicate: the library already holds this track of this edition,
	// at another path — an upgrade, typically, from MP3 to FLAC.
	ImportConflictDuplicate = "duplicate"
	// ImportConflictClash: two files of this import render the same target. Unlike
	// the other two it cannot be resolved by replacing: one of the two mappings is
	// wrong, and the review has to fix it.
	ImportConflictClash = "clash"
)

// ImportRow is one file of an import: where it is, which track it is, and where it
// will go. An empty MBReleaseTrackID is a skip.
type ImportRow struct {
	Source           string `json:"source"`
	MBReleaseTrackID string `json:"mb_release_track_id"`
	TrackNumber      string `json:"track_number,omitempty"`
	TrackTitle       string `json:"track_title,omitempty"`
	Medium           int    `json:"medium,omitempty"`
	How              string `json:"how,omitempty"`

	// Target is the absolute path the file will have; TargetRel the same relative to
	// the library root, with forward slashes, for display.
	Target    string `json:"target,omitempty"`
	TargetRel string `json:"target_rel,omitempty"`
	Conflict  string `json:"conflict,omitempty"`
	// Replaces is every file importing this row removes: the one at Target, and any
	// indexed copy of the same track elsewhere in the library.
	Replaces []string `json:"replaces,omitempty"`
	// Replace is the reviewer's consent to Replaces. A row with a conflict and without
	// it is not imported.
	Replace bool   `json:"replace,omitempty"`
	Error   string `json:"error,omitempty"`
}

func (row ImportRow) skipped() bool { return strings.TrimSpace(row.MBReleaseTrackID) == "" }

// ImportPlan is a reviewed import, ready to queue.
type ImportPlan struct {
	Library models.Library
	Release models.MusicBrainzReleaseResponse
	// Root is the source path the files were found under. Empty folders left by a
	// move are removed up to it, never above it and never it.
	Root string
	Mode modules.TransferMode
	Rows []ImportRow
}

// ProposeImport maps sources onto a release's tracks by the folder-attach mapper and
// plans where each would go. It writes nothing.
func (r *Runner) ProposeImport(library models.Library, release models.MusicBrainzReleaseResponse, sources []string) []ImportRow {
	proposed := modules.MapFilesToTracks(sources, modules.ReleaseTracks(release))
	rows := make([]ImportRow, len(sources))
	for i, source := range sources {
		rows[i] = ImportRow{Source: source, MBReleaseTrackID: proposed[i].TrackID, How: proposed[i].How}
	}
	return r.PlanImport(library, release, rows)
}

// PlanImport works out, for rows whose track is already chosen, where each file goes
// and what is in the way. It keeps each row's Replace and reads nothing but the disk
// and the index.
func (r *Runner) PlanImport(library models.Library, release models.MusicBrainzReleaseResponse, rows []ImportRow) []ImportRow {
	naming := components.TaggerForLibrary(r.db, library).Naming()
	planned := make([]ImportRow, len(rows))
	byTarget := map[string]int{}

	for i, in := range rows {
		row := ImportRow{Source: in.Source, MBReleaseTrackID: strings.TrimSpace(in.MBReleaseTrackID), How: in.How, Replace: in.Replace}
		planned[i] = row
		if row.skipped() {
			continue
		}
		track, found := modules.FindReleaseTrack(release, row.MBReleaseTrackID)
		if !found {
			planned[i].Error = "mapped to a track that is not part of this release"
			continue
		}
		row.TrackNumber, row.TrackTitle, row.Medium = track.Number, track.Title, track.Medium

		path, err := modules.RenderLibraryPath(naming, release, track.TrackID, filepath.Ext(row.Source))
		if err != nil {
			row.Error = err.Error()
			planned[i] = row
			continue
		}
		row.Target = filepath.Join(library.Path, path.Rel())
		row.TargetRel = filepath.ToSlash(path.Rel())

		// Compared without case: on a case-insensitive filesystem two targets that
		// differ only in case are one file, and over-reporting a clash costs a skip
		// where under-reporting it costs a file.
		byTarget[strings.ToLower(row.Target)]++

		if _, err := os.Lstat(row.Target); err == nil {
			row.Conflict = ImportConflictExists
			row.Replaces = append(row.Replaces, row.Target)
		}
		var others []string
		r.db.Model(&models.LibraryItem{}).
			Where("library_id = ? AND mb_release_track_id = ? AND path <> ?", library.ID, track.TrackID, row.Target).
			Order("path").Pluck("path", &others)
		if len(others) > 0 {
			if row.Conflict == "" {
				row.Conflict = ImportConflictDuplicate
			}
			row.Replaces = append(row.Replaces, others...)
		}
		planned[i] = row
	}

	for i := range planned {
		if planned[i].Target != "" && byTarget[strings.ToLower(planned[i].Target)] > 1 {
			planned[i].Conflict = ImportConflictClash
		}
	}
	return planned
}

// ErrImportNotReady is a plan with a row that cannot be imported as it stands.
var ErrImportNotReady = errors.New("the import has unresolved rows")

// CheckImportPlan reports the first row that blocks the import: a planning error, a
// clash, or a conflict the reviewer did not agree to replace.
func CheckImportPlan(rows []ImportRow) error {
	files := 0
	for _, row := range rows {
		if row.skipped() {
			continue
		}
		files++
		name := filepath.Base(row.Source)
		switch {
		case row.Error != "":
			return fmt.Errorf("%w: %s — %s", ErrImportNotReady, name, row.Error)
		case row.Conflict == ImportConflictClash:
			return fmt.Errorf("%w: %s goes to the same place as another file — fix the mapping or skip one", ErrImportNotReady, name)
		case row.Conflict != "" && !row.Replace:
			return fmt.Errorf("%w: %s would replace %s — choose Replace or skip it", ErrImportNotReady, name, strings.Join(row.Replaces, ", "))
		}
	}
	if files == 0 {
		return fmt.Errorf("%w: every file is skipped", ErrImportNotReady)
	}
	if files > MaxImportFiles {
		return fmt.Errorf("import at most %d files at a time", MaxImportFiles)
	}
	return nil
}

// Import queues a reviewed import. The plan must pass CheckImportPlan.
func (r *Runner) Import(plan ImportPlan) error {
	if err := CheckImportPlan(plan.Rows); err != nil {
		return err
	}
	title := importTitle(plan)
	r.enqueue(job{jobImport, "import:" + uuid.NewString(), title, func() { r.importNow(plan, title) }})
	return nil
}

func importTitle(plan ImportPlan) string {
	return fmt.Sprintf("Import %s into %s", plan.Release.Title, plan.Library.Name)
}

func (r *Runner) importNow(plan ImportPlan, title string) {
	event := events.Begin(r.db, models.EventTypeFileImport, title)

	// Planned again, now. Anything at a target that was not there for the review is
	// not something the reviewer agreed to replace.
	approved := map[string]bool{}
	for _, row := range plan.Rows {
		for _, path := range row.Replaces {
			approved[path] = true
		}
	}
	rows := r.PlanImport(plan.Library, plan.Release, plan.Rows)

	var todo []ImportRow
	for _, row := range rows {
		if !row.skipped() {
			todo = append(todo, row)
		}
	}
	r.progTotal.Store(int64(len(todo)))
	r.setPhase(PhaseImporting)
	stopProgress := events.StartProgress(r.db, event, r.progressSnapshot)

	ctx := r.jobContext()
	libraries := map[uuid.UUID]models.Library{plan.Library.ID: plan.Library}
	refreshSet := modules.NewAlbumRefreshSet(nil)
	var items []models.EventItem
	imported, replaced, tagFailures, stopped := 0, 0, 0, false
	var failed []string
	sourceDirs := map[string]bool{}

	for _, row := range todo {
		if ctx.Err() != nil {
			stopped = true
			break
		}
		r.setCurrent(filepath.Base(row.Source))
		item := models.EventItem{Path: row.Target, From: row.Source, Phase: models.EventItemPhaseImport}
		if row.Target == "" {
			item.Path = row.Source
		}

		fail := func(err error) {
			item.Status = models.EventItemStatusError
			item.Error = err.Error()
			failed = append(failed, row.Source)
			items = append(items, item)
			r.progDone.Add(1)
		}

		if err := importRowReady(row, approved); err != nil {
			fail(err)
			continue
		}
		if err := modules.TransferFile(row.Source, row.Target, plan.Mode); err != nil {
			fail(fmt.Errorf("could not %s the file into the library: %w", plan.Mode, err))
			continue
		}
		if plan.Mode == modules.TransferMove {
			sourceDirs[filepath.Dir(row.Source)] = true
		}
		replaced += r.removeReplaced(row)

		track, _ := modules.FindReleaseTrack(plan.Release, row.MBReleaseTrackID)
		indexed, err := r.recordImportedItem(plan.Library, plan.Release.ID, track, row.Target)
		if err != nil {
			fail(fmt.Errorf("imported to %s, but it could not be indexed: %w", row.Target, err))
			continue
		}
		imported++

		written, changes, err := r.retagItem(indexed, libraries, refreshSet)
		item.Status = models.EventItemStatusChanged
		item.TagsWritten = written
		item.Changes = changes
		if err != nil {
			// Imported and indexed either way: the file is where it belongs and its
			// identity is pinned, so the next Process tags it.
			item.Status = models.EventItemStatusError
			item.Error = "imported, but tags were not written: " + err.Error()
			tagFailures++
		}
		items = append(items, item)
		r.progDone.Add(1)
	}
	stopProgress()

	for dir := range sourceDirs {
		modules.RemoveEmptyDirs(dir, plan.Root)
	}

	// The album is new to the collection's disk view; re-derive the artist it belongs
	// to, by the same authority the naming used for the artist folder.
	artistMBID := ""
	if credit := plan.Release.ReleaseGroup.ArtistCredit; len(credit) > 0 {
		artistMBID = credit[0].Artist.ID
	} else if len(plan.Release.ArtistCredit) > 0 {
		artistMBID = plan.Release.ArtistCredit[0].Artist.ID
	}
	var scanErr error
	if imported > 0 {
		r.setPhase(PhaseCollection)
		_, scanErr = collection.RecordScanUnder(r.db, event, "Collection scan for "+plan.Release.Title, collection.RebuildScope{ArtistMBID: artistMBID}, nil)
		r.setPhase(PhasePlex)
		r.flushPlex(refreshSet, event)
	}

	status := models.EventStatusOK
	if len(failed) > 0 || tagFailures > 0 || scanErr != nil {
		status = models.EventStatusError
	}
	summary := fmt.Sprintf("%d of %d files imported into %s", imported, len(todo), plan.Library.Name)
	if replaced > 0 {
		summary += fmt.Sprintf(" · %d replaced", replaced)
	}
	if len(failed) > 0 {
		summary += fmt.Sprintf(" · %d failed", len(failed))
	}
	if tagFailures > 0 {
		summary += fmt.Sprintf(" · %d not tagged yet", tagFailures)
	}
	if stopped {
		summary = "stopped early — " + summary
	}
	event.Stats = []models.EventStat{
		{Label: "Files imported", Value: imported, Kind: models.EventStatNotable, Filter: models.EventItemStatusChanged},
		{Label: "Files replaced", Value: replaced},
		{Label: "Failed", Value: len(failed) + tagFailures, Kind: models.EventStatBad, Filter: models.EventItemStatusError},
	}
	details := map[string]any{
		"library":        plan.Library.Name,
		"release":        plan.Release.Title,
		"mb_release_id":  plan.Release.ID,
		"source":         plan.Root,
		"mode":           string(plan.Mode),
		"files_imported": imported,
		"files_replaced": replaced,
		"errors":         len(failed) + tagFailures,
	}
	if scanErr != nil {
		addWarnings(details, "The files were imported, but the collection could not be updated: "+scanErr.Error())
	}
	if len(items) > r.detailRetention {
		items = items[:r.detailRetention]
	}
	events.Finish(r.db, event, status, summary, details)
	events.AddItems(r.db, event, items)
	events.Prune(r.db, r.eventRetention)
	logger.Log.Infof("file import finished. %s", summary)
}

// importRowReady re-checks a row the job is about to import against what was
// reviewed: it must still plan cleanly, and anything it would replace must have been
// in the plan the reviewer agreed to.
func importRowReady(row ImportRow, approved map[string]bool) error {
	if row.Error != "" {
		return errors.New(row.Error)
	}
	if row.Conflict == ImportConflictClash {
		return errors.New("goes to the same place as another file of this import")
	}
	for _, path := range row.Replaces {
		if !approved[path] {
			return fmt.Errorf("%s appeared since the import was reviewed — review the import again", path)
		}
	}
	if _, err := os.Stat(row.Source); err != nil {
		return fmt.Errorf("the source file is gone: %w", err)
	}
	return nil
}

// removeReplaced deletes what a row replaced, other than the file now at its target
// (the transfer already replaced that one in place), and the index rows of the files
// it deleted. It returns how many files the row replaced in all.
//
// It runs only after the new file is in place, so a failed transfer never costs the
// file it would have replaced.
func (r *Runner) removeReplaced(row ImportRow) int {
	count := 0
	for _, path := range row.Replaces {
		count++
		if path == row.Target {
			continue
		}
		if err := os.Remove(path); err != nil && !errors.Is(err, fs.ErrNotExist) {
			logger.Log.Warnf("import: could not remove %s, which %s replaced: %s", path, row.Target, err.Error())
			count--
			continue
		}
		r.db.Where("path = ?", path).Delete(&models.LibraryItem{})
		r.db.Where("path = ?", path).Delete(&models.TrackTempo{})
	}
	return count
}

// recordImportedItem writes the index row for an imported file: pinned to the track
// the reviewer chose, with the audio it was chosen for, exactly as a manual attach
// records one (routers.saveCorrelation). A row already at the path — the file this
// one replaced — is re-used, so it keeps its ID.
func (r *Runner) recordImportedItem(library models.Library, releaseID string, track modules.ReleaseTrack, path string) (models.LibraryItem, error) {
	audioHash, err := components.PinAudioHash(path)
	if err != nil {
		logger.Log.Warnf("import: could not hash the audio of %s: %s", path, err.Error())
	}
	now := time.Now()

	var item models.LibraryItem
	err = r.db.Where("path = ?", path).First(&item).Error
	if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
		return item, err
	}
	item.LibraryID = library.ID
	item.Path = path
	item.MBReleaseID = releaseID
	item.MBReleaseTrackID = track.TrackID
	item.MBRecordingID = track.RecordingID
	item.CorrelationSource = models.CorrelationSourceManual
	item.CorrelatedByManager = models.ManagerTypeAutotaggerr
	item.CorrelatedAt = &now
	item.Pinned = true
	item.PinnedAudioHash = audioHash
	item.PinHeld = false
	item.Status = models.LibraryItemStatusOK
	item.Error = ""
	item.LastErrorAt = nil
	item.LastErrorTransient = false
	// Blank until the tag write stamps it, so a file whose write failed is processed
	// by the next run rather than skipped as unchanged.
	item.ProcessedVersion = ""
	item.TaggedWith = ""
	if info, statErr := os.Stat(path); statErr == nil {
		mod := info.ModTime()
		item.Size = info.Size()
		item.ModTime = &mod
	}
	if item.ID == uuid.Nil {
		err = r.db.Create(&item).Error
	} else {
		err = r.db.Save(&item).Error
	}
	return item, err
}

// ImportSourceFiles returns the sources of rows, in order and with repeats kept, so
// a caller validating them sees a file listed twice — two tracks claiming one file is
// a mapping error, not something to collapse.
func ImportSourceFiles(rows []ImportRow) []string {
	out := make([]string, len(rows))
	for i, row := range rows {
		out[i] = row.Source
	}
	return out
}
