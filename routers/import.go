package routers

import (
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/aunefyren/autotaggerr/logger"
	"github.com/aunefyren/autotaggerr/models"
	"github.com/aunefyren/autotaggerr/modules"
	"github.com/aunefyren/autotaggerr/process"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

// File import (M6 pass E). Admin-only: the source is a path on the server, typed by
// the person importing, and the import reads, moves and deletes files there — the same
// trust as editing a library's path. See docs/import.md.

// importRoot validates the source path an import reads from: absolute, present, and
// neither inside a library nor containing one. Inside a library, files are already
// indexed — moving them is the Organise verb's job, not an import's — and a root
// containing a library would offer that library's own files for import.
func (a *API) importRoot(c *gin.Context, raw string) (string, bool) {
	root := strings.TrimSpace(raw)
	if root == "" || !filepath.IsAbs(root) {
		c.JSON(http.StatusBadRequest, gin.H{"error": "give the full path of a folder on the server, like /downloads/Album"})
		return "", false
	}
	root = filepath.Clean(root)
	if _, err := os.Stat(root); err != nil {
		c.JSON(http.StatusUnprocessableEntity, gin.H{"error": fmt.Sprintf("cannot read %s: %s", root, err.Error())})
		return "", false
	}
	var libraries []models.Library
	if err := a.DB.Find(&libraries).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to load libraries"})
		return "", false
	}
	for _, library := range libraries {
		switch {
		case modules.PathWithin(root, library.Path):
			c.JSON(http.StatusBadRequest, gin.H{"error": fmt.Sprintf("%s is inside the library %q — its files are already indexed there", root, library.Name)})
			return "", false
		case modules.PathWithin(library.Path, root):
			c.JSON(http.StatusBadRequest, gin.H{"error": fmt.Sprintf("%s contains the library %q — choose a folder outside it", root, library.Name)})
			return "", false
		}
	}
	return root, true
}

// importLibrary loads the library an import writes into and checks it can take one:
// enabled, mounted, and not managed by Lidarr — which decides identity and tracks its
// files by path, so files placed under it by anything else are files it does not know.
func (a *API) importLibrary(c *gin.Context, id uuid.UUID) (models.Library, bool) {
	var library models.Library
	if err := a.DB.First(&library, "id = ?", id).Error; err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "choose a library to import into"})
		return library, false
	}
	if !library.Enabled {
		c.JSON(http.StatusBadRequest, gin.H{"error": fmt.Sprintf("the library %q is disabled", library.Name)})
		return library, false
	}
	editable, err := a.libraryIdentityEditable(library.ID)
	if err != nil {
		logger.Log.Warnf("import: failed to resolve the manager of library %s: %s", library.ID, err.Error())
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to resolve the library manager"})
		return library, false
	}
	if !editable {
		c.JSON(http.StatusConflict, gin.H{"error": fmt.Sprintf("Lidarr manages %q — import into it through Lidarr", library.Name)})
		return library, false
	}
	if _, err := os.Stat(library.Path); err != nil {
		c.JSON(http.StatusUnprocessableEntity, gin.H{"error": fmt.Sprintf("the library folder %s is unavailable: %s", library.Path, err.Error())})
		return library, false
	}
	return library, true
}

// importScan lists the albums waiting under a source path.
func (a *API) importScan(c *gin.Context) {
	root, ok := a.importRoot(c, c.Query("path"))
	if !ok {
		return
	}
	groups, err := modules.ScanImportSource(root)
	if errors.Is(err, modules.ErrImportSourceTooLarge) {
		c.JSON(http.StatusUnprocessableEntity, gin.H{"error": err.Error()})
		return
	}
	if err != nil {
		c.JSON(http.StatusUnprocessableEntity, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"root": root, "groups": groups, "max_files": process.MaxImportFiles})
}

type importRequest struct {
	LibraryID   uuid.UUID           `json:"library_id"`
	MBReleaseID string              `json:"mb_release_id"`
	Root        string              `json:"root"`
	Mode        string              `json:"mode"`
	Sources     []string            `json:"sources"`
	Rows        []process.ImportRow `json:"rows"`
	// DownloadID and DownloadFolder name the download and the album of it the files
	// come from, when they come from the Downloads list.
	DownloadID     *uuid.UUID `json:"download_id"`
	DownloadFolder string     `json:"download_folder"`
	// ReleaseGroup is the download's release group as the review has it ("" for none),
	// for the {Release Group} naming token and the index.
	ReleaseGroup string `json:"release_group"`
}

// importContext validates everything a preview and an import share, and loads the
// release. It returns false with the response written when the request cannot go on.
func (a *API) importContext(c *gin.Context, in importRequest, sources []string) (models.Library, models.MusicBrainzReleaseResponse, string, bool) {
	var release models.MusicBrainzReleaseResponse
	root, ok := a.importRoot(c, in.Root)
	if !ok {
		return models.Library{}, release, "", false
	}
	library, ok := a.importLibrary(c, in.LibraryID)
	if !ok {
		return library, release, "", false
	}
	if len(sources) == 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "no files selected"})
		return library, release, "", false
	}
	if len(sources) > process.MaxImportFiles {
		c.JSON(http.StatusBadRequest, gin.H{"error": fmt.Sprintf("import at most %d files at a time", process.MaxImportFiles)})
		return library, release, "", false
	}
	seen := map[string]bool{}
	for _, source := range sources {
		clean := filepath.Clean(source)
		if seen[clean] {
			c.JSON(http.StatusBadRequest, gin.H{"error": fmt.Sprintf("%s appears twice", filepath.Base(source))})
			return library, release, "", false
		}
		seen[clean] = true
		if clean != source || !modules.PathWithin(source, root) {
			c.JSON(http.StatusBadRequest, gin.H{"error": fmt.Sprintf("%s is not under %s", source, root)})
			return library, release, "", false
		}
		if !modules.IsSupportedAudioFile(source) {
			c.JSON(http.StatusBadRequest, gin.H{"error": fmt.Sprintf("%s is not a supported audio file", filepath.Base(source))})
			return library, release, "", false
		}
	}

	releaseID := strings.TrimSpace(in.MBReleaseID)
	if releaseID == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "mb_release_id is required"})
		return library, release, "", false
	}
	release, err := a.meta().GetRelease(releaseID)
	if err != nil {
		logger.Log.Errorf("import: failed to load release %s: %s", releaseID, err.Error())
		c.JSON(http.StatusBadGateway, gin.H{"error": "could not load that release from MusicBrainz"})
		return library, release, "", false
	}
	return library, release, root, true
}

// importPreview proposes and plans an import without writing anything. Given only
// sources, it maps them onto the release; given rows — the review table after someone
// changed a track — it plans those as they are, so every edit is re-checked by the
// same code the import runs.
func (a *API) importPreview(c *gin.Context) {
	var in importRequest
	if err := c.ShouldBindJSON(&in); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid body"})
		return
	}
	sources := in.Sources
	if len(in.Rows) > 0 {
		sources = process.ImportSourceFiles(in.Rows)
	}
	library, release, _, ok := a.importContext(c, in, sources)
	if !ok {
		return
	}

	var rows []process.ImportRow
	if len(in.Rows) > 0 {
		rows = a.Scan.PlanImport(library, release, in.ReleaseGroup, in.Rows)
	} else {
		rows = a.Scan.ProposeImport(library, release, in.ReleaseGroup, in.Sources)
	}
	problem := ""
	if err := process.CheckImportPlan(rows); err != nil {
		problem = err.Error()
	}
	c.JSON(http.StatusOK, gin.H{
		"release": gin.H{
			"mb_id":          release.ID,
			"title":          release.Title,
			"date":           release.Date,
			"country":        release.Country,
			"disambiguation": release.Disambiguation,
		},
		"tracks":  modules.ReleaseTracks(release),
		"rows":    rows,
		"problem": problem,
	})
}

// importFiles queues a reviewed import. The rows are planned again here — the body is
// still the client's — and once more by the job when it runs.
func (a *API) importFiles(c *gin.Context) {
	var in importRequest
	if err := c.ShouldBindJSON(&in); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid body"})
		return
	}
	mode := modules.TransferMode(in.Mode)
	if mode == "" {
		mode = modules.TransferMove
		if in.DownloadID != nil {
			// A download is the client's until the torrent is removed.
			mode = modules.TransferCopy
		}
	}
	if mode != modules.TransferMove && mode != modules.TransferCopy {
		c.JSON(http.StatusBadRequest, gin.H{"error": `mode is "move" or "copy"`})
		return
	}
	library, release, root, ok := a.importContext(c, in, process.ImportSourceFiles(in.Rows))
	if !ok {
		return
	}

	plan := process.ImportPlan{
		Library: library,
		Release: release,
		Root:    root,
		Mode:    mode,
		Rows:    a.Scan.PlanImport(library, release, in.ReleaseGroup, in.Rows),
		// Trimmed by the plan; kept as sent so the job records what was reviewed.
		ReleaseGroup: strings.TrimSpace(in.ReleaseGroup),
	}
	if in.DownloadID != nil {
		plan.Download = &process.ImportDownload{ItemID: *in.DownloadID, Folder: filepath.Clean(in.DownloadFolder)}
	}
	if err := a.Scan.Import(plan); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error(), "rows": plan.Rows})
		return
	}
	c.JSON(http.StatusAccepted, gin.H{"queued": true})
}

type folderSuggestion struct {
	Path string `json:"path"`
	// Library names the library this folder is inside of or contains, when it is one a
	// scan would refuse — offered anyway, since it is the way *through* to its
	// neighbours, but marked so picking it is not a surprise.
	Library string `json:"library,omitempty"`
}

// importFolders completes a partly typed source path to the folders it could name, for
// the path field's autocomplete. Same admin gate as the scan: a folder listing of the
// server is the scan's power in a smaller dose.
func (a *API) importFolders(c *gin.Context) {
	var libraries []models.Library
	if err := a.DB.Find(&libraries).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to load libraries"})
		return
	}
	folders := []folderSuggestion{}
	for _, path := range modules.SuggestFolders(strings.TrimSpace(c.Query("path")), modules.MaxFolderSuggestions) {
		suggestion := folderSuggestion{Path: path}
		for _, library := range libraries {
			if modules.PathWithin(path, library.Path) || modules.PathWithin(library.Path, path) {
				suggestion.Library = library.Name
				break
			}
		}
		folders = append(folders, suggestion)
	}
	c.JSON(http.StatusOK, gin.H{"folders": folders, "separator": string(filepath.Separator)})
}
