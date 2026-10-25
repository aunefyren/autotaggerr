package process

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/aunefyren/autotaggerr/logger"
	"github.com/aunefyren/autotaggerr/models"
	"github.com/aunefyren/autotaggerr/modules"
	"github.com/google/uuid"
	"gorm.io/gorm"
)

// Downloads: finished downloads in a download client's watched categories, offered for
// import. See docs/downloads.md.
//
// The poller only finds them. It reads each enabled client's finished downloads in the
// categories mapped to a library, lists each new one's complete audio files as this
// host sees them, and records a row — nothing is imported, moved or tagged without
// someone reviewing it on the Import page, through the same flow as a folder import.
// Like the Lidarr follower it runs on the cron goroutine rather than the job queue:
// it is a few requests and a few rows, and the imports it leads to are queued jobs of
// their own.

// downloadSettle is how long a download has to have been complete before it is
// offered. qBittorrent moves a finished torrent out of its temporary folder and may
// recheck it; a download listed mid-move would list paths that are about to change.
// A var so tests need not wait.
var downloadSettle = time.Minute

// newDownloadClient builds a client for a row. A var so a test can stand in a fake
// for one that would talk to a real qBittorrent.
var newDownloadClient = modules.NewDownloadClient

// ImportDownload ties an import to the download its files came from: which item, and
// which of its album folders.
type ImportDownload struct {
	ItemID uuid.UUID
	Folder string
}

// ErrDownloadMustCopy is an import from a download the client still holds, asked to
// move its files.
var ErrDownloadMustCopy = errors.New("the download client still holds these files, so they are copied — moving them would break the torrent")

// PollDownloads reads every enabled download client once. An overlapping call is
// skipped rather than waited for; the next poll reads the same downloads.
//
// After the poll, auto-import looks at what it found, outside the poll's lock (see
// autoImportDownloads).
func (r *Runner) PollDownloads() {
	if r.db == nil || !r.downloadMu.TryLock() {
		return
	}
	r.pollAll()
	r.downloadMu.Unlock()
	r.autoImportDownloads()
}

// pollAll reads every enabled client. The caller holds downloadMu.
func (r *Runner) pollAll() {
	var clients []models.DownloadClient
	if err := r.db.Preload("Categories").Where("enabled = ?", true).Order("name").Find(&clients).Error; err != nil {
		logger.Log.Warnf("failed to load download clients: %s", err.Error())
		return
	}
	for _, row := range clients {
		r.pollClient(row)
	}
}

// pollClient reads one client's watched categories.
//
// A failed read is recorded on the client row, where the Downloads list shows it, and
// in no Activity event: the health check already records an unreachable client, and a
// row per poll would bury the feed.
func (r *Runner) pollClient(row models.DownloadClient) {
	now := time.Now()
	if len(row.Categories) == 0 {
		r.recordPoll(row.ID, now, "")
		return
	}
	client, err := newDownloadClient(row)
	if err != nil {
		r.recordPoll(row.ID, now, err.Error())
		return
	}
	libraryOf := map[string]uuid.UUID{}
	categories := make([]string, 0, len(row.Categories))
	for _, c := range row.Categories {
		libraryOf[c.Category] = c.LibraryID
		categories = append(categories, c.Category)
	}
	sort.Strings(categories)

	downloads, err := client.Finished(categories)
	if err != nil {
		logger.Log.Warnf("failed to read download client %q: %s", row.Name, err.Error())
		r.recordPoll(row.ID, now, err.Error())
		return
	}
	r.recordPoll(row.ID, now, "")

	var existing []models.DownloadItem
	if err := r.db.Where("client_id = ?", row.ID).Find(&existing).Error; err != nil {
		logger.Log.Warnf("failed to load downloads of %q: %s", row.Name, err.Error())
		return
	}
	known := make(map[string]models.DownloadItem, len(existing))
	for _, item := range existing {
		known[item.RemoteID] = item
	}
	var libraries []models.Library
	r.db.Find(&libraries)

	present := map[string]bool{}
	for _, d := range downloads {
		present[d.ID] = true
		item, seen := known[d.ID]
		if !seen {
			item = models.DownloadItem{ClientID: row.ID, RemoteID: d.ID}
		}
		item.Name, item.Category, item.LibraryID, item.Seeding = d.Name, d.Category, libraryOf[d.Category], d.Seeding
		item.RemotePath, item.Size, item.SeenAt = d.Path, d.Size, &now
		if !d.CompletedAt.IsZero() {
			completed := d.CompletedAt
			item.CompletedAt = &completed
		}

		stateChanged := false
		switch {
		case item.State == models.DownloadStateImported || item.State == models.DownloadStateDismissed || HasQueuedAlbum(item):
			// Decided, or being imported: its files are not read again.
		case d.Imported:
			// Imported before — by another Autotaggerr, or before this database. The
			// tag is the record; there is nothing to offer.
			item.State, item.Reason, stateChanged = models.DownloadStateImported, "", true
			if item.ImportedAt == nil {
				item.ImportedAt = &now
			}
		case !d.CompletedAt.IsZero() && now.Sub(d.CompletedAt) < downloadSettle:
			// Just finished: the client may still be moving it into place.
			if !seen {
				continue
			}
		case item.State == models.DownloadStateReady:
			// Its files were listed when it became ready; an import re-checks them.
		default:
			r.readDownloadFiles(client, row, d, &item, libraries)
			stateChanged = true
		}

		if err := r.saveDownloadItem(&item, stateChanged); err != nil {
			logger.Log.Warnf("failed to record download %q: %s", d.Name, err.Error())
		}
	}

	// Gone from the client — removed, or moved to a category nobody watches. One that
	// nothing was imported from has nothing left to offer; the rest are history, and
	// their files are no longer held by the client.
	for _, item := range existing {
		if present[item.RemoteID] {
			continue
		}
		if len(item.Groups) == 0 && (item.State == models.DownloadStateReady || item.State == models.DownloadStateUnavailable) {
			r.db.Delete(&item)
			continue
		}
		if item.Seeding {
			r.db.Model(&item).Update("seeding", false)
		}
	}
}

// readDownloadFiles lists a download's complete audio files as this host sees them,
// and makes the item ready — or unavailable, with the reason.
func (r *Runner) readDownloadFiles(client modules.DownloadClient, row models.DownloadClient, d modules.Download, item *models.DownloadItem, libraries []models.Library) {
	item.State, item.Reason, item.Files = models.DownloadStateUnavailable, "", nil
	item.Path = modules.MapRemotePath(d.Path, row.PathMappings)

	files, err := client.Files(d)
	if err != nil {
		item.Reason = "could not list its files: " + err.Error()
		return
	}
	var audio []string
	for _, f := range files {
		local := modules.MapRemotePath(f.Path, row.PathMappings)
		if f.Complete && modules.IsSupportedAudioFile(local) {
			audio = append(audio, local)
		}
	}
	if len(audio) == 0 {
		item.Reason = "holds no complete FLAC or MP3 files"
		return
	}
	if len(audio) > modules.MaxImportScanFiles {
		item.Reason = modules.ErrImportSourceTooLarge.Error()
		return
	}
	if _, err := os.Stat(item.Path); err != nil {
		item.Reason = fmt.Sprintf("its files are not at %s on this host — add a path mapping for %s", item.Path, remoteParent(d.Path))
		return
	}
	for _, file := range audio {
		if !modules.PathWithin(file, item.Path) {
			item.Reason = fmt.Sprintf("%s is outside the download's folder %s — check the path mappings", file, item.Path)
			return
		}
		if _, err := os.Stat(file); err != nil {
			item.Reason = fmt.Sprintf("%s is missing on this host: %s", file, err.Error())
			return
		}
	}
	for _, library := range libraries {
		if modules.PathWithin(item.Path, library.Path) || modules.PathWithin(library.Path, item.Path) {
			item.Reason = fmt.Sprintf("%s overlaps the library %q — a download must be outside every library", item.Path, library.Name)
			return
		}
	}
	sort.Strings(audio)
	item.Files = audio
	item.State = models.DownloadStateReady
}

// remoteParent is the folder above a remote path, in the client's own spelling: the
// prefix a path mapping would most likely need.
func remoteParent(p string) string {
	trimmed := strings.TrimRight(p, `/\`)
	if i := strings.LastIndexAny(trimmed, `/\`); i > 0 {
		return trimmed[:i]
	}
	return p
}

// HasQueuedAlbum reports whether an album of a download is waiting to be imported.
func HasQueuedAlbum(item models.DownloadItem) bool {
	for _, state := range item.Groups {
		if state == models.DownloadGroupQueued {
			return true
		}
	}
	return false
}

// saveDownloadItem writes what a poll learned about a download. An existing row gets
// only the poller's columns, and its state only when the poll decided it: a Dismiss
// pressed while a poll ran must not be written back to the state the poll loaded.
func (r *Runner) saveDownloadItem(item *models.DownloadItem, stateChanged bool) error {
	if item.ID == uuid.Nil {
		return r.db.Create(item).Error
	}
	columns := []string{"name", "category", "library_id", "seeding", "remote_path", "size", "seen_at", "completed_at"}
	if stateChanged {
		columns = append(columns, "state", "reason", "files", "path", "imported_at")
	}
	return r.db.Model(item).Select(columns).Updates(item).Error
}

// recordPoll stamps a client row with its last poll, touching nothing else — a form
// save between the load and this write must not be overwritten.
func (r *Runner) recordPoll(id uuid.UUID, at time.Time, problem string) {
	r.db.Model(&models.DownloadClient{}).Where("id = ?", id).
		Updates(map[string]any{"last_poll_at": at, "last_poll_error": problem})
}

// DownloadSourceName is the name a download's albums are read by when its files sit
// directly in the client's folder — a single-file torrent — so the folder above names
// nothing. The file extension is not part of it.
func DownloadSourceName(item models.DownloadItem) string {
	if ext := filepath.Ext(item.Name); ext != "" && modules.IsSupportedAudioFile(item.Name) {
		return strings.TrimSuffix(item.Name, ext)
	}
	return item.Name
}

// checkImportDownload validates an import's download against the item: the folder is
// one of its albums, the import is a copy while the client holds the files, and the
// album is not already queued or imported.
func (r *Runner) checkImportDownload(plan ImportPlan) error {
	ref := plan.Download
	var item models.DownloadItem
	if err := r.db.First(&item, "id = ?", ref.ItemID).Error; err != nil {
		return fmt.Errorf("that download is no longer listed: %w", err)
	}
	if item.State != models.DownloadStateReady {
		return fmt.Errorf("the download %q is %s, not ready to import", item.Name, item.State)
	}
	if filepath.Clean(plan.Root) != filepath.Clean(item.Path) {
		return fmt.Errorf("the import's source is not the download's folder %s", item.Path)
	}
	found := false
	for _, folder := range modules.ImportGroupFolders(item.Path, item.Files) {
		found = found || folder == ref.Folder
	}
	if !found {
		return fmt.Errorf("%s is not an album of the download %q", ref.Folder, item.Name)
	}
	switch item.Groups[ref.Folder] {
	case models.DownloadGroupQueued:
		return fmt.Errorf("that album of %q is already being imported", item.Name)
	case models.DownloadGroupImported:
		return fmt.Errorf("that album of %q has already been imported", item.Name)
	}
	if item.Seeding && plan.Mode != modules.TransferCopy {
		return ErrDownloadMustCopy
	}
	return nil
}

// setDownloadGroup records what happened to one album of a download, and when every
// album has been imported, marks the download imported and tells its client.
func (r *Runner) setDownloadGroup(ref ImportDownload, state string) {
	r.downloadMu.Lock()
	defer r.downloadMu.Unlock()
	r.setDownloadGroupLocked(ref, state)
}

// setDownloadGroupLocked is setDownloadGroup for a caller already holding downloadMu.
func (r *Runner) setDownloadGroupLocked(ref ImportDownload, state string) {
	var item models.DownloadItem
	if err := r.db.First(&item, "id = ?", ref.ItemID).Error; err != nil {
		logger.Log.Warnf("an import finished for a download that is no longer listed (%s): %s", ref.ItemID, err.Error())
		return
	}
	if item.Groups == nil {
		item.Groups = map[string]string{}
	}
	item.Groups[ref.Folder] = state

	if state != models.DownloadGroupQueued && allGroupsImported(item) {
		now := time.Now()
		item.State, item.ImportedAt, item.Reason = models.DownloadStateImported, &now, ""
		if err := r.tellClientImported(item); err != nil {
			// Imported regardless: the files are in the library. The tag is only the
			// second record, and the row here is the first.
			item.Reason = "imported, but the download client could not be told: " + err.Error()
			logger.Log.Warnf("download %q: %s", item.Name, item.Reason)
		}
	}
	if err := r.db.Save(&item).Error; err != nil {
		logger.Log.Warnf("failed to record the import of download %q: %s", item.Name, err.Error())
	}
}

func allGroupsImported(item models.DownloadItem) bool {
	for _, folder := range modules.ImportGroupFolders(item.Path, item.Files) {
		if item.Groups[folder] != models.DownloadGroupImported {
			return false
		}
	}
	return true
}

func (r *Runner) tellClientImported(item models.DownloadItem) error {
	var row models.DownloadClient
	if err := r.db.First(&row, "id = ?", item.ClientID).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return errors.New("the client has been removed")
		}
		return err
	}
	client, err := newDownloadClient(row)
	if err != nil {
		return err
	}
	return client.MarkImported(item.RemoteID)
}

// ReconcileDownloads forgets album imports left queued by a process that has since
// exited. The queue does not survive a restart, so a queued mark that did is a job
// that will never run, and would keep its album from being offered again. Startup-only,
// for the reason events.ReconcileRunning is.
func ReconcileDownloads(db *gorm.DB) {
	var items []models.DownloadItem
	if err := db.Where("state = ?", models.DownloadStateReady).Find(&items).Error; err != nil {
		logger.Log.Warnf("failed to reconcile downloads: %s", err.Error())
		return
	}
	for _, item := range items {
		if !HasQueuedAlbum(item) {
			continue
		}
		for folder, state := range item.Groups {
			if state == models.DownloadGroupQueued {
				delete(item.Groups, folder)
			}
		}
		db.Model(&item).Select("groups").Updates(&item)
	}
}
