package routers

import (
	"fmt"
	"net/http"
	"net/url"
	"path/filepath"
	"slices"
	"strings"

	"github.com/aunefyren/autotaggerr/logger"
	"github.com/aunefyren/autotaggerr/models"
	"github.com/aunefyren/autotaggerr/modules"
	"github.com/aunefyren/autotaggerr/process"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"gorm.io/gorm"
)

// Download clients and the Downloads list on the Import page. Admin-only, for the
// import's reason: a client row names where files are read from and what library they
// go into, and the list leads straight to an import. See docs/downloads.md.

// --- Download clients -------------------------------------------------------

type downloadCategoryInput struct {
	Category   string    `json:"category"`
	LibraryID  uuid.UUID `json:"library_id"`
	AutoImport bool      `json:"auto_import"`
}

type downloadClientInput struct {
	Name     *string `json:"name"`
	Type     *string `json:"type"`
	Enabled  *bool   `json:"enabled"`
	BaseURL  *string `json:"base_url"`
	Username *string `json:"username"`
	// Password is write-only: settable here, never returned (json:"-" on the model).
	// Absent keeps the stored one.
	Password     *string                  `json:"password"`
	PathMappings *[]models.PathMapping    `json:"path_mappings"`
	Categories   *[]downloadCategoryInput `json:"categories"`
}

func (in downloadClientInput) apply(row *models.DownloadClient) {
	if in.Name != nil {
		row.Name = strings.TrimSpace(*in.Name)
	}
	if in.Type != nil {
		row.Type = *in.Type
	}
	if in.Enabled != nil {
		row.Enabled = *in.Enabled
	}
	if in.BaseURL != nil {
		row.BaseURL = strings.TrimRight(strings.TrimSpace(*in.BaseURL), "/")
	}
	if in.Username != nil {
		row.Username = strings.TrimSpace(*in.Username)
	}
	if in.Password != nil {
		row.Password = *in.Password
	}
	if in.PathMappings != nil {
		mappings := make([]models.PathMapping, 0, len(*in.PathMappings))
		for _, m := range *in.PathMappings {
			mappings = append(mappings, models.PathMapping{Remote: strings.TrimSpace(m.Remote), Local: strings.TrimSpace(m.Local)})
		}
		row.PathMappings = mappings
	}
}

// validateDownloadClient checks a client row and the categories it would have, and
// returns what is wrong in the interface's voice, or "".
func (a *API) validateDownloadClient(row models.DownloadClient, categories []downloadCategoryInput) string {
	if row.Name == "" {
		return "give the download client a name"
	}
	if !slices.Contains(models.DownloadClientTypes, row.Type) {
		return "unsupported download client type"
	}
	if u, err := url.Parse(row.BaseURL); err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return "the base URL must be a full http:// or https:// address, like http://192.168.1.10:8080"
	}
	for _, m := range row.PathMappings {
		if m.Remote == "" || m.Local == "" {
			return "a path mapping needs both the client's path and this host's"
		}
		if !filepath.IsAbs(m.Local) {
			return fmt.Sprintf("%s is not a full path on this host", m.Local)
		}
	}
	seen := map[string]bool{}
	for _, c := range categories {
		name := strings.TrimSpace(c.Category)
		if name == "" {
			return "a watched category needs a name — the category set on the downloads in the client"
		}
		if seen[name] {
			return fmt.Sprintf("the category %q is listed twice", name)
		}
		seen[name] = true
		var library models.Library
		if err := a.DB.First(&library, "id = ?", c.LibraryID).Error; err != nil {
			return fmt.Sprintf("choose a library for the category %q", name)
		}
		editable, err := a.libraryIdentityEditable(library.ID)
		if err != nil {
			return fmt.Sprintf("could not resolve the manager of %q: %s", library.Name, err.Error())
		}
		if !editable {
			return fmt.Sprintf("Lidarr manages %q, so it cannot take an import — choose a library Autotaggerr manages", library.Name)
		}
	}
	return ""
}

// saveDownloadClient writes a client row and, when categories is non-nil, replaces its
// categories, in one transaction. The poller's columns are never written from here.
func (a *API) saveDownloadClient(row *models.DownloadClient, categories *[]downloadCategoryInput) error {
	return a.DB.Transaction(func(tx *gorm.DB) error {
		row.Categories = nil
		var err error
		if row.ID == uuid.Nil {
			err = tx.Omit("Categories").Create(row).Error
		} else {
			err = tx.Omit("Categories", "last_poll_at", "last_poll_error").Save(row).Error
		}
		if err != nil {
			return err
		}
		if categories == nil {
			return nil
		}
		if err := tx.Where("client_id = ?", row.ID).Delete(&models.DownloadClientCategory{}).Error; err != nil {
			return err
		}
		for _, c := range *categories {
			category := models.DownloadClientCategory{ClientID: row.ID, Category: strings.TrimSpace(c.Category), LibraryID: c.LibraryID, AutoImport: c.AutoImport}
			if err := tx.Create(&category).Error; err != nil {
				return err
			}
		}
		return nil
	})
}

func (a *API) loadDownloadClient(c *gin.Context) (models.DownloadClient, bool) {
	var row models.DownloadClient
	id, ok := a.idParam(c)
	if !ok {
		return row, false
	}
	if err := a.DB.Preload("Categories").First(&row, "id = ?", id).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "not found"})
		return row, false
	}
	return row, true
}

// downloadClientView is a client row as the API returns it: the row, plus whether a
// password is stored — never the password.
type downloadClientView struct {
	models.DownloadClient
	PasswordSet bool `json:"password_set"`
}

func viewDownloadClient(row models.DownloadClient) downloadClientView {
	if row.PathMappings == nil {
		row.PathMappings = []models.PathMapping{}
	}
	if row.Categories == nil {
		row.Categories = []models.DownloadClientCategory{}
	}
	return downloadClientView{DownloadClient: row, PasswordSet: row.Password != ""}
}

func (a *API) listDownloadClients(c *gin.Context) {
	var rows []models.DownloadClient
	if err := a.DB.Preload("Categories").Order("name").Find(&rows).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to list download clients"})
		return
	}
	out := make([]downloadClientView, 0, len(rows))
	for _, row := range rows {
		out = append(out, viewDownloadClient(row))
	}
	c.JSON(http.StatusOK, out)
}

func (a *API) getDownloadClient(c *gin.Context) {
	if row, ok := a.loadDownloadClient(c); ok {
		c.JSON(http.StatusOK, viewDownloadClient(row))
	}
}

func (a *API) createDownloadClient(c *gin.Context) {
	var in downloadClientInput
	if err := c.ShouldBindJSON(&in); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid body"})
		return
	}
	row := models.DownloadClient{Enabled: true, Type: models.DownloadClientTypeQBittorrent}
	in.apply(&row)
	var categories []downloadCategoryInput
	if in.Categories != nil {
		categories = *in.Categories
	}
	if problem := a.validateDownloadClient(row, categories); problem != "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": problem})
		return
	}
	var existing int64
	a.DB.Model(&models.DownloadClient{}).Where("name = ?", row.Name).Count(&existing)
	if existing > 0 {
		c.JSON(http.StatusConflict, gin.H{"error": fmt.Sprintf("a download client named %q already exists", row.Name)})
		return
	}
	if err := a.saveDownloadClient(&row, &categories); err != nil {
		logger.Log.Errorf("failed to create download client: %s", err.Error())
		c.JSON(http.StatusInternalServerError, gin.H{"error": "create failed"})
		return
	}
	a.DB.Preload("Categories").First(&row, "id = ?", row.ID)
	c.JSON(http.StatusCreated, viewDownloadClient(row))
}

func (a *API) updateDownloadClient(c *gin.Context) {
	row, ok := a.loadDownloadClient(c)
	if !ok {
		return
	}
	var in downloadClientInput
	if err := c.ShouldBindJSON(&in); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid body"})
		return
	}
	in.apply(&row)
	categories := make([]downloadCategoryInput, 0, len(row.Categories))
	for _, existing := range row.Categories {
		categories = append(categories, downloadCategoryInput{Category: existing.Category, LibraryID: existing.LibraryID, AutoImport: existing.AutoImport})
	}
	if in.Categories != nil {
		categories = *in.Categories
	}
	if problem := a.validateDownloadClient(row, categories); problem != "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": problem})
		return
	}
	var clash int64
	a.DB.Model(&models.DownloadClient{}).Where("name = ? AND id <> ?", row.Name, row.ID).Count(&clash)
	if clash > 0 {
		c.JSON(http.StatusConflict, gin.H{"error": fmt.Sprintf("a download client named %q already exists", row.Name)})
		return
	}
	if err := a.saveDownloadClient(&row, in.Categories); err != nil {
		logger.Log.Errorf("failed to update download client %s: %s", row.ID, err.Error())
		c.JSON(http.StatusInternalServerError, gin.H{"error": "update failed"})
		return
	}
	a.DB.Preload("Categories").First(&row, "id = ?", row.ID)
	c.JSON(http.StatusOK, viewDownloadClient(row))
}

// deleteDownloadClient removes a client with its categories and its downloads. Nothing
// on disk is touched: the downloads are the client's, and imported files are the
// library's.
func (a *API) deleteDownloadClient(c *gin.Context) {
	id, ok := a.idParam(c)
	if !ok {
		return
	}
	var deleted int64
	err := a.DB.Transaction(func(tx *gorm.DB) error {
		if err := tx.Where("client_id = ?", id).Delete(&models.DownloadItem{}).Error; err != nil {
			return err
		}
		if err := tx.Where("client_id = ?", id).Delete(&models.DownloadClientCategory{}).Error; err != nil {
			return err
		}
		res := tx.Where("id = ?", id).Delete(&models.DownloadClient{})
		deleted = res.RowsAffected
		return res.Error
	})
	switch {
	case err != nil:
		c.JSON(http.StatusInternalServerError, gin.H{"error": "delete failed"})
	case deleted == 0:
		c.JSON(http.StatusNotFound, gin.H{"error": "not found"})
	default:
		c.Status(http.StatusNoContent)
	}
}

// testDownloadClient probes a client with the credentials the poller uses. Like a
// manager's test it answers 200 either way — a refused login is a test that worked.
func (a *API) testDownloadClient(c *gin.Context) {
	row, ok := a.loadDownloadClient(c)
	if !ok {
		return
	}
	client, err := modules.NewDownloadClient(row)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	healthy, probeErr := client.HealthCheck()
	out := gin.H{"healthy": healthy, "username_set": row.Username != "", "password_set": row.Password != ""}
	if probeErr != nil {
		out["error"] = probeErr.Error()
		logger.Log.Warnf("download client %q failed its connection test: %s", row.Name, probeErr.Error())
	}
	c.JSON(http.StatusOK, out)
}

// --- The Downloads list -----------------------------------------------------

// maxDecidedDownloads bounds the imported and dismissed downloads the list carries: the
// recent ones are worth seeing, the whole history is not worth sending every load.
const maxDecidedDownloads = 50

// downloadView is a download as the list shows it.
type downloadView struct {
	models.DownloadItem
	ClientName string `json:"client_name"`
	// Albums is how many album folders the download's files make; Imported how many of
	// them have been imported.
	Albums   int `json:"albums"`
	Imported int `json:"albums_imported"`
}

func viewDownload(item models.DownloadItem, clientName string) downloadView {
	if item.Files == nil {
		item.Files = []string{}
	}
	if item.Groups == nil {
		item.Groups = map[string]string{}
	}
	if item.Notes == nil {
		item.Notes = map[string]string{}
	}
	view := downloadView{DownloadItem: item, ClientName: clientName}
	for _, folder := range modules.ImportGroupFolders(item.Path, item.Files) {
		view.Albums++
		if item.Groups[folder] == models.DownloadGroupImported {
			view.Imported++
		}
	}
	return view
}

type downloadClientSummary struct {
	ID            uuid.UUID `json:"id"`
	Name          string    `json:"name"`
	Enabled       bool      `json:"enabled"`
	Categories    int       `json:"categories"`
	LastPollAt    any       `json:"last_poll_at"`
	LastPollError string    `json:"last_poll_error,omitempty"`
}

// listDownloads is the Downloads list: every client's state, every download waiting,
// and the most recent ones decided.
func (a *API) listDownloads(c *gin.Context) {
	var clients []models.DownloadClient
	if err := a.DB.Preload("Categories").Order("name").Find(&clients).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to load download clients"})
		return
	}
	names := map[uuid.UUID]string{}
	summaries := make([]downloadClientSummary, 0, len(clients))
	for _, row := range clients {
		names[row.ID] = row.Name
		summaries = append(summaries, downloadClientSummary{
			ID: row.ID, Name: row.Name, Enabled: row.Enabled, Categories: len(row.Categories),
			LastPollAt: row.LastPollAt, LastPollError: row.LastPollError,
		})
	}

	var waiting, decided []models.DownloadItem
	open := []string{models.DownloadStateReady, models.DownloadStateUnavailable}
	if err := a.DB.Where("state IN ?", open).Order("completed_at DESC, name").Find(&waiting).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to load downloads"})
		return
	}
	if err := a.DB.Where("state NOT IN ?", open).Order("updated_at DESC").Limit(maxDecidedDownloads).Find(&decided).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to load downloads"})
		return
	}
	items := make([]downloadView, 0, len(waiting)+len(decided))
	for _, item := range append(waiting, decided...) {
		items = append(items, viewDownload(item, names[item.ClientID]))
	}
	c.JSON(http.StatusOK, gin.H{"clients": summaries, "downloads": items})
}

func (a *API) loadDownload(c *gin.Context) (models.DownloadItem, bool) {
	var item models.DownloadItem
	id, ok := a.idParam(c)
	if !ok {
		return item, false
	}
	if err := a.DB.First(&item, "id = ?", id).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "that download is no longer listed"})
		return item, false
	}
	return item, true
}

// scanDownload groups a ready download's files into albums, in the shape a folder scan
// answers in, so the Import page opens it with the same modal. The tags are read here
// rather than by the poller: only a download someone opens is worth reading.
func (a *API) scanDownload(c *gin.Context) {
	item, ok := a.loadDownload(c)
	if !ok {
		return
	}
	if item.State != models.DownloadStateReady {
		c.JSON(http.StatusConflict, gin.H{"error": fmt.Sprintf("%q is %s, not ready to import", item.Name, item.State)})
		return
	}
	groups := modules.GroupImportFiles(item.Path, item.Files, process.DownloadSourceName(item))
	states := item.Groups
	if states == nil {
		states = map[string]string{}
	}
	c.JSON(http.StatusOK, gin.H{
		"root":      item.Path,
		"groups":    groups,
		"states":    states,
		"notes":     item.Notes,
		"max_files": process.MaxImportFiles,
		"seeding":   item.Seeding,
	})
}

// dismissDownload sets a download aside so it is not offered again. Nothing in the
// client or on disk changes.
func (a *API) dismissDownload(c *gin.Context) {
	item, ok := a.loadDownload(c)
	if !ok {
		return
	}
	if process.HasQueuedAlbum(item) {
		c.JSON(http.StatusConflict, gin.H{"error": "an album of this download is being imported — wait for it to finish"})
		return
	}
	if item.State == models.DownloadStateImported {
		c.JSON(http.StatusConflict, gin.H{"error": "this download has been imported"})
		return
	}
	a.DB.Model(&item).Update("state", models.DownloadStateDismissed)
	c.Status(http.StatusNoContent)
}

// restoreDownload offers a dismissed download again. It goes back as unavailable, so
// the next poll reads its files afresh rather than trusting a list that may be stale.
func (a *API) restoreDownload(c *gin.Context) {
	item, ok := a.loadDownload(c)
	if !ok {
		return
	}
	if item.State != models.DownloadStateDismissed {
		c.JSON(http.StatusConflict, gin.H{"error": "only a dismissed download can be restored"})
		return
	}
	a.DB.Model(&item).Updates(map[string]any{"state": models.DownloadStateUnavailable, "reason": "waiting for the next check"})
	if a.Scan != nil {
		a.Scan.PollDownloads()
	}
	c.Status(http.StatusNoContent)
}

// pollDownloads checks every client now, for the list's Check now button. Synchronous:
// a poll is a few small requests, and the answer is the list it refreshes.
func (a *API) pollDownloads(c *gin.Context) {
	if a.Scan != nil {
		a.Scan.PollDownloads()
	}
	a.listDownloads(c)
}
