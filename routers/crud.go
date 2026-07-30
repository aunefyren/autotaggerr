package routers

import (
	"errors"
	"net/http"
	"net/url"
	"strings"

	"github.com/aunefyren/autotaggerr/auth"
	"github.com/aunefyren/autotaggerr/components"
	"github.com/aunefyren/autotaggerr/models"
	"github.com/aunefyren/autotaggerr/modules"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

// idParam parses the :id path parameter as a UUID, writing a 400 on failure.
func (a *API) idParam(c *gin.Context) (uuid.UUID, bool) {
	id, err := uuid.Parse(c.Param("id"))
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid id"})
		return uuid.Nil, false
	}
	return id, true
}

// getEntity fetches one row of T by :id. Shared by all typed get handlers.
func getEntity[T any](a *API, c *gin.Context) {
	id, ok := a.idParam(c)
	if !ok {
		return
	}
	var row T
	if err := a.DB.First(&row, "id = ?", id).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "not found"})
		return
	}
	c.JSON(http.StatusOK, row)
}

// deleteEntity deletes one row of T by :id. Shared by all typed delete handlers.
func deleteEntity[T any](a *API, c *gin.Context) {
	id, ok := a.idParam(c)
	if !ok {
		return
	}
	var row T
	res := a.DB.Where("id = ?", id).Delete(&row)
	if res.Error != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "delete failed"})
		return
	}
	if res.RowsAffected == 0 {
		c.JSON(http.StatusNotFound, gin.H{"error": "not found"})
		return
	}
	c.Status(http.StatusNoContent)
}

// --- Data sources -----------------------------------------------------------

// validDataSourceType lists the providers the app knows how to talk to. Kept as
// one function so the create and update paths cannot drift apart.
func validDataSourceType(t string) bool {
	switch t {
	case models.DataSourceTypeMusicBrainz,
		models.DataSourceTypeAcoustID,
		models.DataSourceTypeCoverArtArchive,
		models.DataSourceTypeFanart:
		return true
	}
	return false
}

type dataSourceInput struct {
	Name      *string  `json:"name"`
	Type      *string  `json:"type"`
	BaseURL   *string  `json:"base_url"`
	Contact   *string  `json:"contact"`
	RateLimit *float64 `json:"rate_limit"`
	Enabled   *bool    `json:"enabled"`
	// APIKey is write-only: settable here, never returned (json:"-" on the model).
	APIKey *string `json:"api_key"`
}

func (in dataSourceInput) apply(ds *models.DataSource) {
	if in.Name != nil {
		ds.Name = *in.Name
	}
	if in.Type != nil {
		ds.Type = *in.Type
	}
	if in.BaseURL != nil {
		ds.BaseURL = *in.BaseURL
	}
	if in.Contact != nil {
		ds.Contact = *in.Contact
	}
	if in.RateLimit != nil {
		ds.RateLimit = *in.RateLimit
	}
	if in.Enabled != nil {
		ds.Enabled = *in.Enabled
	}
	if in.APIKey != nil {
		ds.APIKey = *in.APIKey
	}
}

func (a *API) getDataSource(c *gin.Context)    { getEntity[models.DataSource](a, c) }
func (a *API) deleteDataSource(c *gin.Context) { deleteEntity[models.DataSource](a, c) }

func (a *API) createDataSource(c *gin.Context) {
	var in dataSourceInput
	if err := c.ShouldBindJSON(&in); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid body"})
		return
	}
	if in.Name == nil || *in.Name == "" || in.Type == nil || *in.Type == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "name and type are required"})
		return
	}
	if !validDataSourceType(*in.Type) {
		c.JSON(http.StatusBadRequest, gin.H{"error": "unsupported data source type"})
		return
	}
	// One AcoustID, one Cover Art Archive, one fanart.tv. A second row of a singleton
	// type is never used — only the first is looked up — so it is rejected here rather
	// than accepted and silently ignored.
	if models.DataSourceIsSingleton(*in.Type) {
		var existing int64
		if err := a.DB.Model(&models.DataSource{}).Where("type = ?", *in.Type).Count(&existing).Error; err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "create failed"})
			return
		}
		if existing > 0 {
			c.JSON(http.StatusConflict, gin.H{"error": "a " + *in.Type + " source already exists — edit that one instead"})
			return
		}
	}

	ds := models.DataSource{Enabled: true}
	in.apply(&ds)
	if err := a.DB.Create(&ds).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "create failed"})
		return
	}
	c.JSON(http.StatusCreated, ds)
}

func (a *API) updateDataSource(c *gin.Context) {
	id, ok := a.idParam(c)
	if !ok {
		return
	}
	var ds models.DataSource
	if err := a.DB.First(&ds, "id = ?", id).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "not found"})
		return
	}
	var in dataSourceInput
	if err := c.ShouldBindJSON(&in); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid body"})
		return
	}
	if in.Type != nil && !validDataSourceType(*in.Type) {
		c.JSON(http.StatusBadRequest, gin.H{"error": "unsupported data source type"})
		return
	}
	in.apply(&ds)
	if err := a.DB.Save(&ds).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "update failed"})
		return
	}
	// Artwork lookups remember "no image for this MBID" for a day. Those answers
	// were recorded under the old settings — most likely before this source had a
	// key at all — so keeping them would make a freshly configured provider look
	// broken until tomorrow.
	if ds.Type == models.DataSourceTypeFanart || ds.Type == models.DataSourceTypeCoverArtArchive {
		modules.ResetArtworkNegativeCache()
	}
	// A changed request rate has to reach the limiter, which holds it in memory.
	if ds.Type == models.DataSourceTypeMusicBrainz {
		components.ApplyDataSourceRateLimits(a.DB)
	}
	c.JSON(http.StatusOK, ds)
}

// --- Managers ---------------------------------------------------------------

type managerInput struct {
	Name                *string    `json:"name"`
	Type                *string    `json:"type"`
	Enabled             *bool      `json:"enabled"`
	LidarrBaseURL       *string    `json:"lidarr_base_url"`
	LidarrAPIKey        *string    `json:"lidarr_api_key"`
	LidarrHeaderCookie  *string    `json:"lidarr_header_cookie"`
	DefaultDataSourceID *uuid.UUID `json:"default_data_source_id"`
}

func (in managerInput) apply(m *models.Manager) {
	if in.Name != nil {
		m.Name = *in.Name
	}
	if in.Type != nil {
		m.Type = *in.Type
	}
	if in.Enabled != nil {
		m.Enabled = *in.Enabled
	}
	if in.LidarrBaseURL != nil {
		m.LidarrBaseURL = *in.LidarrBaseURL
	}
	if in.LidarrAPIKey != nil {
		m.LidarrAPIKey = *in.LidarrAPIKey
	}
	if in.LidarrHeaderCookie != nil {
		m.LidarrHeaderCookie = *in.LidarrHeaderCookie
	}
	if in.DefaultDataSourceID != nil {
		m.DefaultDataSourceID = in.DefaultDataSourceID
	}
}

func validManagerType(t string) bool {
	return t == models.ManagerTypeLidarr || t == models.ManagerTypeAutotaggerr
}

func (a *API) getManager(c *gin.Context)    { getEntity[models.Manager](a, c) }
func (a *API) deleteManager(c *gin.Context) { deleteEntity[models.Manager](a, c) }

func (a *API) createManager(c *gin.Context) {
	var in managerInput
	if err := c.ShouldBindJSON(&in); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid body"})
		return
	}
	if in.Name == nil || *in.Name == "" || in.Type == nil || !validManagerType(*in.Type) {
		c.JSON(http.StatusBadRequest, gin.H{"error": "name and a valid type (lidarr|autotaggerr) are required"})
		return
	}
	m := models.Manager{Enabled: true}
	in.apply(&m)
	if err := a.DB.Create(&m).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "create failed"})
		return
	}
	c.JSON(http.StatusCreated, m)
}

func (a *API) updateManager(c *gin.Context) {
	id, ok := a.idParam(c)
	if !ok {
		return
	}
	var m models.Manager
	if err := a.DB.First(&m, "id = ?", id).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "not found"})
		return
	}
	var in managerInput
	if err := c.ShouldBindJSON(&in); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid body"})
		return
	}
	if in.Type != nil && !validManagerType(*in.Type) {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid manager type"})
		return
	}
	in.apply(&m)
	if err := a.DB.Save(&m).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "update failed"})
		return
	}
	c.JSON(http.StatusOK, m)
}

// --- Tagger profiles --------------------------------------------------------

type taggerProfileInput struct {
	Name                               *string `json:"name"`
	WriteTags                          *bool   `json:"write_tags"`
	RemoveValues                       *bool   `json:"remove_values"`
	UseCurrentArtistName               *bool   `json:"use_current_artist_name"`
	UseCustomArtistDelimiter           *bool   `json:"use_custom_artist_delimiter"`
	CustomArtistDelimiter              *string `json:"custom_artist_delimiter"`
	CustomArtistDelimiterCommas        *bool   `json:"custom_artist_delimiter_commas"`
	IgnoreRedundantContributingArtists *bool   `json:"ignore_redundant_contributing_artists"`
}

func (in taggerProfileInput) apply(p *models.TaggerProfile) {
	if in.Name != nil {
		p.Name = *in.Name
	}
	if in.WriteTags != nil {
		p.WriteTags = *in.WriteTags
	}
	if in.RemoveValues != nil {
		p.RemoveValues = *in.RemoveValues
	}
	if in.UseCurrentArtistName != nil {
		p.UseCurrentArtistName = *in.UseCurrentArtistName
	}
	if in.UseCustomArtistDelimiter != nil {
		p.UseCustomArtistDelimiter = *in.UseCustomArtistDelimiter
	}
	if in.CustomArtistDelimiter != nil {
		p.CustomArtistDelimiter = *in.CustomArtistDelimiter
	}
	if in.CustomArtistDelimiterCommas != nil {
		p.CustomArtistDelimiterCommas = *in.CustomArtistDelimiterCommas
	}
	if in.IgnoreRedundantContributingArtists != nil {
		p.IgnoreRedundantContributingArtists = *in.IgnoreRedundantContributingArtists
	}
}

func (a *API) getTaggerProfile(c *gin.Context)    { getEntity[models.TaggerProfile](a, c) }
func (a *API) deleteTaggerProfile(c *gin.Context) { deleteEntity[models.TaggerProfile](a, c) }

func (a *API) createTaggerProfile(c *gin.Context) {
	var in taggerProfileInput
	if err := c.ShouldBindJSON(&in); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid body"})
		return
	}
	if in.Name == nil || *in.Name == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "name is required"})
		return
	}
	p := models.TaggerProfile{WriteTags: true}
	in.apply(&p)
	if err := a.DB.Create(&p).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "create failed"})
		return
	}
	c.JSON(http.StatusCreated, p)
}

func (a *API) updateTaggerProfile(c *gin.Context) {
	id, ok := a.idParam(c)
	if !ok {
		return
	}
	var p models.TaggerProfile
	if err := a.DB.First(&p, "id = ?", id).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "not found"})
		return
	}
	var in taggerProfileInput
	if err := c.ShouldBindJSON(&in); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid body"})
		return
	}
	in.apply(&p)
	if err := a.DB.Save(&p).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "update failed"})
		return
	}
	c.JSON(http.StatusOK, p)
}

// --- Libraries --------------------------------------------------------------

type libraryInput struct {
	Name            *string    `json:"name"`
	Path            *string    `json:"path"`
	ManagerID       *uuid.UUID `json:"manager_id"`
	DataSourceID    *uuid.UUID `json:"data_source_id"`
	TaggerProfileID *uuid.UUID `json:"tagger_profile_id"`
	Enabled         *bool      `json:"enabled"`
	Cron            *string    `json:"cron"`
	UseAcoustID     *bool      `json:"use_acoustid"`
}

func (in libraryInput) apply(l *models.Library) {
	if in.Name != nil {
		l.Name = *in.Name
	}
	if in.Path != nil {
		l.Path = *in.Path
	}
	if in.ManagerID != nil {
		l.ManagerID = in.ManagerID
	}
	if in.DataSourceID != nil {
		l.DataSourceID = in.DataSourceID
	}
	if in.TaggerProfileID != nil {
		l.TaggerProfileID = in.TaggerProfileID
	}
	if in.Enabled != nil {
		l.Enabled = *in.Enabled
	}
	if in.Cron != nil {
		l.Cron = *in.Cron
	}
	if in.UseAcoustID != nil {
		l.UseAcoustID = *in.UseAcoustID
	}
}

func (a *API) getLibrary(c *gin.Context)    { getEntity[models.Library](a, c) }
func (a *API) deleteLibrary(c *gin.Context) { deleteEntity[models.Library](a, c) }

// checkLibraryDataSource validates a library's chosen data source: it must exist and
// must be a *metadata* provider. Assigning AcoustID or an artwork provider here was
// accepted before and then quietly ignored by the pipeline, because
// `resolveManagerRow`/tagging only ever want release metadata. Writing a 400 and
// reporting false keeps the handler bodies flat.
func (a *API) checkLibraryDataSource(c *gin.Context, id *uuid.UUID) bool {
	if id == nil {
		return true // unset means "use the default", which is always allowed
	}

	var ds models.DataSource
	if err := a.DB.First(&ds, "id = ?", *id).Error; err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "data source not found"})
		return false
	}
	if models.DataSourceCategory(ds.Type) != models.DataSourceCategoryMetadata {
		c.JSON(http.StatusBadRequest, gin.H{
			"error": "a library's data source must be a metadata provider, not " + ds.Type,
		})
		return false
	}
	return true
}

func (a *API) createLibrary(c *gin.Context) {
	var in libraryInput
	if err := c.ShouldBindJSON(&in); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid body"})
		return
	}
	if in.Name == nil || *in.Name == "" || in.Path == nil || *in.Path == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "name and path are required"})
		return
	}
	if !a.checkLibraryDataSource(c, in.DataSourceID) {
		return
	}
	l := models.Library{Enabled: true}
	in.apply(&l)
	if err := a.DB.Create(&l).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "create failed"})
		return
	}
	c.JSON(http.StatusCreated, l)
}

func (a *API) updateLibrary(c *gin.Context) {
	id, ok := a.idParam(c)
	if !ok {
		return
	}
	var l models.Library
	if err := a.DB.First(&l, "id = ?", id).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "not found"})
		return
	}
	var in libraryInput
	if err := c.ShouldBindJSON(&in); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid body"})
		return
	}
	if !a.checkLibraryDataSource(c, in.DataSourceID) {
		return
	}
	in.apply(&l)
	if err := a.DB.Save(&l).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "update failed"})
		return
	}
	c.JSON(http.StatusOK, l)
}

// --- Auth providers ---------------------------------------------------------

type authProviderInput struct {
	Name         *string `json:"name"`
	Type         *string `json:"type"`
	Enabled      *bool   `json:"enabled"`
	Issuer       *string `json:"issuer"`
	ClientID     *string `json:"client_id"`
	ClientSecret *string `json:"client_secret"`
	Scopes       *string `json:"scopes"`
	RedirectURL  *string `json:"redirect_url"`
	AllowSignup  *bool   `json:"allow_signup"`
	DefaultRole  *string `json:"default_role"`
}

func (in authProviderInput) apply(p *models.AuthProvider) {
	if in.Name != nil {
		p.Name = *in.Name
	}
	if in.Type != nil {
		p.Type = *in.Type
	}
	if in.Enabled != nil {
		p.Enabled = *in.Enabled
	}
	if in.Issuer != nil {
		p.Issuer = strings.TrimSuffix(strings.TrimSpace(*in.Issuer), "/")
	}
	if in.ClientID != nil {
		p.ClientID = *in.ClientID
	}
	// Omitting client_secret keeps the stored one, so the UI can edit a provider
	// without ever receiving (or resending) the secret.
	if in.ClientSecret != nil {
		p.ClientSecret = *in.ClientSecret
	}
	if in.Scopes != nil {
		p.Scopes = *in.Scopes
	}
	if in.RedirectURL != nil {
		p.RedirectURL = *in.RedirectURL
	}
	if in.AllowSignup != nil {
		p.AllowSignup = *in.AllowSignup
	}
	if in.DefaultRole != nil {
		p.DefaultRole = *in.DefaultRole
	}
}

func (a *API) getAuthProvider(c *gin.Context) { getEntity[models.AuthProvider](a, c) }

func (a *API) deleteAuthProvider(c *gin.Context) {
	deleteEntity[models.AuthProvider](a, c)
	auth.ResetProviderCache()
}

func (a *API) listAuthProviders(c *gin.Context) {
	var rows []models.AuthProvider
	if err := a.DB.Order("name").Find(&rows).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to list auth providers"})
		return
	}
	// ClientSecret is hidden via json:"-" on the model.
	c.JSON(http.StatusOK, rows)
}

func (a *API) createAuthProvider(c *gin.Context) {
	var in authProviderInput
	if err := c.ShouldBindJSON(&in); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid body"})
		return
	}
	p := models.AuthProvider{Type: models.AuthProviderTypeOIDC, Enabled: true, DefaultRole: models.UserRoleAdmin}
	in.apply(&p)
	if err := validateAuthProvider(p); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	if err := a.DB.Create(&p).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "create failed"})
		return
	}
	auth.ResetProviderCache()
	c.JSON(http.StatusCreated, p)
}

func (a *API) updateAuthProvider(c *gin.Context) {
	id, ok := a.idParam(c)
	if !ok {
		return
	}
	var p models.AuthProvider
	if err := a.DB.First(&p, "id = ?", id).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "not found"})
		return
	}
	var in authProviderInput
	if err := c.ShouldBindJSON(&in); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid body"})
		return
	}
	in.apply(&p)
	if err := validateAuthProvider(p); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	if err := a.DB.Save(&p).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "update failed"})
		return
	}
	auth.ResetProviderCache()
	c.JSON(http.StatusOK, p)
}

// validateAuthProvider rejects configurations that cannot possibly complete a
// login, so the failure surfaces at save time rather than as a broken login button.
func validateAuthProvider(p models.AuthProvider) error {
	if strings.TrimSpace(p.Name) == "" {
		return errors.New("name is required")
	}
	if p.Type != models.AuthProviderTypeOIDC {
		return errors.New("type must be oidc")
	}
	if strings.TrimSpace(p.ClientID) == "" || strings.TrimSpace(p.ClientSecret) == "" {
		return errors.New("client_id and client_secret are required")
	}
	u, err := url.Parse(strings.TrimSpace(p.Issuer))
	if err != nil || u.Scheme == "" || u.Host == "" {
		return errors.New("issuer must be an absolute URL, e.g. https://id.example.com/application/o/autotaggerr")
	}
	if u.Scheme != "https" && u.Hostname() != "localhost" && u.Hostname() != "127.0.0.1" {
		return errors.New("issuer must use https")
	}
	return nil
}
