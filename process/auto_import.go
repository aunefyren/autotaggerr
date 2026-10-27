package process

import (
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/aunefyren/autotaggerr/components"
	"github.com/aunefyren/autotaggerr/logger"
	"github.com/aunefyren/autotaggerr/models"
	"github.com/aunefyren/autotaggerr/modules"
	"github.com/google/uuid"
)

// Auto-import: a download in a category set to import automatically is imported
// without a review — but only an album whose identity is not a judgement call. See
// docs/downloads.md#auto-import.
//
// The review exists because picking a release and pairing files with tracks are
// decisions. An album needs neither when its own tags already made them: every file
// names the same release, each pairs with a track by its embedded track ID or its
// filename's number, the counts agree, and nothing in the library is in the way.
// Anything short of that is left in the Downloads list with a note saying which part
// was missing, which is the same place it would have been without auto-import.

// errAutoRetry marks a reason to look at a download again on the next poll rather than
// leave it for review: MusicBrainz being unreachable says nothing about the album.
var errAutoRetry = errors.New("retry")

// autoImportDownloads looks once at every ready download in a category set to import
// automatically, and queues an import for each album of it that qualifies.
//
// It runs after a poll, outside the poll's lock: an import takes that lock to mark
// its album queued. An overlapping call is skipped; the next poll finds whatever this
// one did not reach, since a download is marked looked-at only when it has been.
func (r *Runner) autoImportDownloads() {
	if r.db == nil || !r.autoMu.TryLock() {
		return
	}
	defer r.autoMu.Unlock()

	var categories []models.DownloadClientCategory
	if err := r.db.Where("auto_import = ?", true).Find(&categories).Error; err != nil || len(categories) == 0 {
		return
	}
	auto := map[string]models.DownloadClientCategory{}
	for _, c := range categories {
		auto[c.ClientID.String()+"\x00"+c.Category] = c
	}

	var items []models.DownloadItem
	if err := r.db.Where("state = ? AND auto_checked_at IS NULL", models.DownloadStateReady).Order("completed_at").Find(&items).Error; err != nil {
		logger.Log.Warnf("auto-import: failed to load downloads: %s", err.Error())
		return
	}
	for _, item := range items {
		category, ok := auto[item.ClientID.String()+"\x00"+item.Category]
		if !ok {
			continue
		}
		var client models.DownloadClient
		if r.db.First(&client, "id = ?", item.ClientID).Error != nil || !client.Enabled {
			continue
		}
		r.autoImportDownload(item, category)
	}
}

// autoImportDownload queues each qualifying album of one download, and records why
// each other album was left for review.
func (r *Runner) autoImportDownload(item models.DownloadItem, category models.DownloadClientCategory) {
	notes := map[string]string{}
	retry := false

	library, problem := r.autoImportLibrary(category.LibraryID)
	for _, group := range modules.WithDownloadReleaseGroup(modules.GroupImportFiles(item.Path, item.Files, DownloadSourceName(item)), DownloadSourceName(item)) {
		if item.Groups[group.Folder] != "" {
			continue // queued, imported, or failed once already: a person's turn now
		}
		if problem != "" {
			notes[group.Folder] = problem
			continue
		}
		plan, err := r.autoImportPlan(library, item, group)
		if errors.Is(err, errAutoRetry) {
			retry = true
			continue
		}
		if err == nil {
			plan.Download = &ImportDownload{ItemID: item.ID, Folder: group.Folder}
			err = r.Import(plan)
		}
		if err != nil {
			notes[group.Folder] = strings.TrimPrefix(err.Error(), ErrImportNotReady.Error()+": ")
		}
	}

	update := models.DownloadItem{Notes: notes}
	columns := []string{"notes"}
	if !retry {
		now := time.Now()
		update.AutoCheckedAt = &now
		columns = append(columns, "auto_checked_at")
	}
	if err := r.db.Model(&models.DownloadItem{}).Where("id = ?", item.ID).Select(columns).Updates(&update).Error; err != nil {
		logger.Log.Warnf("auto-import: failed to record what it did with %q: %s", item.Name, err.Error())
	}
}

// autoImportLibrary loads the library a category feeds and checks it can take an
// import, the same gate the import routes apply. It returns why not, or "".
func (r *Runner) autoImportLibrary(id uuid.UUID) (models.Library, string) {
	var library models.Library
	if err := r.db.First(&library, "id = ?", id).Error; err != nil {
		return library, "the category's library no longer exists"
	}
	if !library.Enabled {
		return library, fmt.Sprintf("the library %q is disabled", library.Name)
	}
	manager, _, err := components.BuildForLibrary(r.db, library)
	if err != nil {
		return library, fmt.Sprintf("the manager of %q could not be resolved: %s", library.Name, err.Error())
	}
	if manager.Type() == models.ManagerTypeLidarr {
		return library, fmt.Sprintf("Lidarr manages %q", library.Name)
	}
	if _, err := os.Stat(library.Path); err != nil {
		return library, fmt.Sprintf("the library folder %s is unavailable", library.Path)
	}
	return library, ""
}

// autoImportPlan builds the import an album would get with no one reviewing it, or
// says which part of being certain it is missing.
func (r *Runner) autoImportPlan(library models.Library, item models.DownloadItem, group modules.ImportGroup) (ImportPlan, error) {
	files := group.Files
	if len(files) > MaxImportFiles {
		return ImportPlan{}, fmt.Errorf("more than %d files — import it by hand", MaxImportFiles)
	}

	// 1. Every file names one release. agreedReleaseID tolerates untagged files, which
	// is right for a hint and wrong here: an untagged file is one nobody identified.
	releaseID, named := "", 0
	for _, file := range files {
		id, err := modules.ExtractMusicBrainzReleaseID(file)
		if id = strings.TrimSpace(id); err != nil || id == "" {
			continue
		}
		if releaseID != "" && id != releaseID {
			return ImportPlan{}, errors.New("the files' tags name more than one release")
		}
		releaseID = id
		named++
	}
	if named < len(files) {
		return ImportPlan{}, fmt.Errorf("%d of %d files name their MusicBrainz release in their tags", named, len(files))
	}

	release, err := r.meta.GetRelease(releaseID)
	if err != nil {
		logger.Log.Warnf("auto-import: could not load release %s for %q, trying again next poll: %s", releaseID, item.Name, err.Error())
		return ImportPlan{}, errAutoRetry
	}

	// 2. The files are the whole release: no more, no fewer.
	var tracks []modules.ReleaseTrack
	for _, t := range modules.ReleaseTracks(release) {
		if !t.Video {
			tracks = append(tracks, t)
		}
	}
	if len(tracks) != len(files) {
		return ImportPlan{}, fmt.Errorf("the release has %d tracks and the album %d files", len(tracks), len(files))
	}

	// 3. Each file is one track, decided by the file rather than guessed. Track IDs in
	// the tags decide it when the files carry them — and when they carry them but
	// contradict each other, something is off that filenames should not paper over.
	// Only with no track IDs at all do the filenames' numbers decide.
	rows, tagged, ok := rowsByEmbeddedTrackID(files, release)
	switch {
	case tagged && !ok:
		return ImportPlan{}, errors.New("the files' track IDs do not each name a different track of this release")
	case !tagged:
		if rows, ok = rowsByTrackNumber(files, tracks); !ok {
			return ImportPlan{}, errors.New("the files could not be matched to tracks by their track numbers")
		}
	}

	// 4. Nothing in the library is in the way. A conflict — an upgrade, typically — is
	// exactly the decision Replace exists for, and only a person gives that consent.
	rows = r.PlanImport(library, release, group.ReleaseGroup, rows)
	if err := CheckImportPlan(rows); err != nil {
		return ImportPlan{}, err
	}
	return ImportPlan{
		Library:      library,
		Release:      release,
		ReleaseGroup: group.ReleaseGroup,
		Root:         item.Path,
		// Copied whatever the client says: an automatic import leaves the source as
		// it found it.
		Mode: modules.TransferCopy,
		Rows: rows,
		Auto: true,
	}, nil
}

// rowsByEmbeddedTrackID pairs each file with the release track its own tags name. It
// reports whether any file carries a track ID at all, and whether every file names a
// distinct audio track of this release.
func rowsByEmbeddedTrackID(files []string, release models.MusicBrainzReleaseResponse) (rows []ImportRow, tagged, ok bool) {
	rows = make([]ImportRow, len(files))
	seen := map[string]bool{}
	ok = true
	for i, file := range files {
		id, err := modules.ExtractMusicBrainzTrackID(file)
		if id = strings.TrimSpace(id); err != nil || id == "" {
			ok = false
			continue
		}
		tagged = true
		track, found := modules.FindReleaseTrack(release, id)
		if !found || track.Video || seen[id] {
			ok = false
			continue
		}
		seen[id] = true
		rows[i] = ImportRow{Source: file, MBReleaseTrackID: id, How: "tags"}
	}
	return rows, tagged, ok
}

// rowsByTrackNumber pairs files with tracks by the number in each filename. Sort
// order is never enough on its own: it is what the review marks for checking.
func rowsByTrackNumber(files []string, tracks []modules.ReleaseTrack) ([]ImportRow, bool) {
	mapped := modules.MapFilesToTracks(files, tracks)
	rows := make([]ImportRow, len(files))
	seen := map[string]bool{}
	for i, m := range mapped {
		if m.How != modules.MapByNumber || m.TrackID == "" || seen[m.TrackID] {
			return nil, false
		}
		seen[m.TrackID] = true
		rows[i] = ImportRow{Source: files[i], MBReleaseTrackID: m.TrackID, How: m.How}
	}
	return rows, true
}
