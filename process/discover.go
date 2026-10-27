package process

import (
	"context"
	"errors"
	"fmt"

	"github.com/aunefyren/autotaggerr/collection"
	"github.com/aunefyren/autotaggerr/components"
	"github.com/aunefyren/autotaggerr/events"
	"github.com/aunefyren/autotaggerr/logger"
	"github.com/aunefyren/autotaggerr/models"
	"github.com/google/uuid"
)

// A Scan that walks the disk. Scan on its own re-derives the collection from the
// index and can only drop rows whose files have gone; with the disk walk switched on
// it first finds what the index is missing — moved files, new files, files with no
// identity — and records it, writing no audio file. See components.DiscoverLibraryRoots.
//
// It is queued rather than answered inline because a walk is minutes on a large
// library, where the plain Scan is milliseconds. The two are one verb: the ordinary
// collection scan runs as this job's last stage, under its event.

// DiscoverAll queues a disk-walking Scan of every enabled library.
func (r *Runner) DiscoverAll() error {
	var libraries []models.Library
	if err := r.db.Where("enabled = ?", true).Order("name").Find(&libraries).Error; err != nil {
		return err
	}
	if len(libraries) == 0 {
		return ErrNothingToProcess
	}
	scope := LibraryScope(libraries)
	scope.Title = "Scan with disk walk"
	r.enqueue(job{jobDiscoverAll, "discover_all", "Scan with disk walk", func() {
		r.discoverNow(scope, collection.RebuildScope{}, "Collection scan")
	}})
	return nil
}

// DiscoverArtist queues a disk-walking Scan of one artist's folders, derived from where
// their indexed files sit — the same folders an artist Process walks. It returns
// ErrNothingToProcess when the artist has no indexed files to locate a folder from.
func (r *Runner) DiscoverArtist(artistMBID string) error {
	return r.DiscoverArtistIn(artistMBID, nil)
}

// DiscoverArtistIn walks only one library's folders of the artist (nil: every
// library). The scan that follows is still the artist's: the collection counts an
// artist across libraries, so it has no narrower reading.
func (r *Runner) DiscoverArtistIn(artistMBID string, library *uuid.UUID) error {
	scope, err := r.ArtistScopeIn(artistMBID, library)
	if err != nil {
		return err
	}
	name, _ := scope.Detail["artist"].(string)
	scope.Title = "Scan with disk walk for " + name
	if lib, ok := scope.Detail["library"].(string); ok {
		scope.Title += " in " + lib
	}
	r.enqueue(job{jobDiscoverArtist, scopeKey("discover_artist:", scope), "Scan with disk walk", func() {
		r.discoverNow(scope, collection.RebuildScope{ArtistMBID: artistMBID}, "Collection scan for "+name)
	}})
	return nil
}

func (r *Runner) discoverNow(scope Scope, rebuild collection.RebuildScope, scanTitle string) {
	event := events.Begin(r.db, models.EventTypeDiscoverFiles, scope.Title)
	detail := components.NewDetailCollector(r.detailRetention)

	warnings := managerWarnings(scope)
	total := components.DiscoverStats{Failed: []string{}}
	var libraryErrors []string
	ctx := r.jobContext()
	stopped := false
	for _, target := range scope.Targets {
		if ctx.Err() != nil {
			stopped = true
			break
		}
		stats, err := components.DiscoverLibraryRoots(ctx, r.db, target.Library, target.Roots, detail, r.Concurrency())
		if errors.Is(err, context.Canceled) {
			total.Add(stats)
			stopped = true
			break
		}
		if err != nil {
			logger.Log.Warnf("disk walk of %q failed: %s", target.Library.Name, err.Error())
			libraryErrors = append(libraryErrors, fmt.Sprintf("%s: %s", target.Library.Name, err.Error()))
			continue
		}
		total.Add(stats)
	}

	// The Scan proper, on what the walk just recorded. It also prunes the rows whose
	// files are gone and were not carried anywhere — which is why the walk had to come
	// first: run the other way round, a moved file's row is deleted before the walk
	// could move it.
	//
	// Not after a stopped walk: rows it had not reached yet may be files that moved, and
	// the Scan's prune would delete them before a finished walk could carry them.
	var scanStats collection.RebuildStats
	var scanErr error
	if !stopped {
		scanStats, scanErr = collection.RecordScanUnder(r.db, event, scanTitle, rebuild, scope.Detail)
	}

	status := models.EventStatusOK
	if len(total.Failed) > 0 || len(libraryErrors) > 0 || scanErr != nil {
		status = models.EventStatusError
	}
	summary := fmt.Sprintf("%d files on disk · %d moved · %d new identities · %d unmatched · %d failed",
		total.Walked, total.Moved, total.Resolved, total.Unmatched, len(total.Failed))
	if scanStats.FilesRemoved > 0 {
		summary += fmt.Sprintf(" · %d gone", scanStats.FilesRemoved)
	}
	if total.Resolved > 0 {
		summary += " — the next Process tags the new ones"
	}
	if stopped {
		summary = "stopped early — " + summary + " — the Scan was skipped"
	}

	recorded := total.Failed
	if len(recorded) > maxErrorFilesRecorded {
		recorded = recorded[:maxErrorFilesRecorded]
	}
	event.Stats = []models.EventStat{
		{Label: "Files on disk", Value: total.Walked},
		{Label: "Files moved", Value: total.Moved, Kind: models.EventStatNotable},
		{Label: "New identities", Value: total.Resolved, Kind: models.EventStatNotable},
		{Label: "Unmatched", Value: total.Unmatched},
		{Label: "Files removed", Value: scanStats.FilesRemoved, Kind: models.EventStatBad},
		{Label: "Failed", Value: len(total.Failed), Kind: models.EventStatBad, Filter: models.EventItemStatusError},
	}
	details := map[string]any{
		"walked":        total.Walked,
		"moved":         total.Moved,
		"resolved":      total.Resolved,
		"unmatched":     total.Unmatched,
		"files_removed": scanStats.FilesRemoved,
		"errors":        len(total.Failed),
		"error_files":   recorded,
		"detail":        detailSummary(detail),
	}
	if len(libraryErrors) > 0 {
		details[models.EventDetailLibraryErrors] = libraryErrors
	}
	addWarnings(details, warnings...)
	for k, v := range scope.Detail {
		details[k] = v
	}
	events.Finish(r.db, event, status, summary, details)
	events.AddItems(r.db, event, detail.Items())
	logger.Log.Infof("scan with disk walk finished. %s", summary)
}
