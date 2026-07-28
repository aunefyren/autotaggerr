package routers

import (
	"errors"
	"net/http"

	"github.com/aunefyren/autotaggerr/auth"
	"github.com/aunefyren/autotaggerr/models"
	"github.com/aunefyren/autotaggerr/scan"
	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

// API holds the dependencies shared by the JSON API handlers.
type API struct {
	DB         *gorm.DB
	Scan       *scan.Runner
	SigningKey []byte
	AppName    string
	Version    string
}

// APIPing is the unauthenticated liveness probe.
func APIPing(context *gin.Context) {
	context.JSON(http.StatusOK, gin.H{"message": "Hello world!"})
}

// Register mounts the v1 API on the given group. Public routes (liveness, login)
// sit outside the auth middleware; everything else requires a session token or
// API key.
func (a *API) Register(rg *gin.RouterGroup) {
	rg.GET("/ping", APIPing)
	rg.POST("/auth/login", a.login)

	protected := rg.Group("")
	protected.Use(auth.Middleware(a.DB, a.SigningKey))
	{
		protected.GET("/auth/me", a.me)
		protected.GET("/health", a.health)

		// Data sources
		protected.GET("/data-sources", a.listDataSources)
		protected.POST("/data-sources", a.createDataSource)
		protected.GET("/data-sources/:id", a.getDataSource)
		protected.PUT("/data-sources/:id", a.updateDataSource)
		protected.DELETE("/data-sources/:id", a.deleteDataSource)

		// Managers
		protected.GET("/managers", a.listManagers)
		protected.POST("/managers", a.createManager)
		protected.GET("/managers/:id", a.getManager)
		protected.PUT("/managers/:id", a.updateManager)
		protected.DELETE("/managers/:id", a.deleteManager)

		// Tagger profiles
		protected.GET("/tagger-profiles", a.listTaggerProfiles)
		protected.POST("/tagger-profiles", a.createTaggerProfile)
		protected.GET("/tagger-profiles/:id", a.getTaggerProfile)
		protected.PUT("/tagger-profiles/:id", a.updateTaggerProfile)
		protected.DELETE("/tagger-profiles/:id", a.deleteTaggerProfile)

		// Libraries
		protected.GET("/libraries", a.listLibraries)
		protected.POST("/libraries", a.createLibrary)
		protected.GET("/libraries/:id", a.getLibrary)
		protected.PUT("/libraries/:id", a.updateLibrary)
		protected.DELETE("/libraries/:id", a.deleteLibrary)
		protected.POST("/libraries/:id/scan", a.scanLibrary)

		// Library items (the correlation index)
		protected.GET("/library-items", a.listLibraryItems)
		protected.GET("/library-items/:id/tags", a.itemTags)

		// Scans + metadata sync
		protected.POST("/scan", a.triggerScan)
		protected.POST("/sync", a.triggerSync)
		protected.GET("/scan/status", a.scanStatus)

		// Activity events
		protected.GET("/events", a.listEvents)
		protected.GET("/events/:id", a.getEvent)

		// Collection (present vs wanted)
		protected.GET("/artists", a.listArtists)
		protected.GET("/artists/:mbid", a.getArtist)
		protected.POST("/artists/:mbid/monitor", a.setArtistMonitored)
		protected.POST("/collection/rebuild", a.rebuildCollection)
		protected.POST("/collection/sync-lidarr", a.syncLidarr)
	}
}

// login authenticates a username/password and returns a session token. OAuth will
// add a sibling flow that resolves a user and calls auth.IssueToken the same way.
func (a *API) login(c *gin.Context) {
	var body struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid request body"})
		return
	}

	user, err := a.authenticateLocal(body.Username, body.Password)
	if err != nil {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "invalid credentials"})
		return
	}

	token, err := auth.IssueToken(user, a.SigningKey, auth.DefaultTokenTTL)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to issue token"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"token": token, "user": user})
}

// authenticateLocal verifies a username + password. A future authenticateOAuth
// returns a user the same way; both feed auth.IssueToken.
func (a *API) authenticateLocal(username, password string) (models.User, error) {
	var user models.User
	if err := a.DB.Where("username = ?", username).First(&user).Error; err != nil {
		return user, errors.New("invalid credentials")
	}
	if !auth.CheckPassword(user.PasswordHash, password) {
		return user, errors.New("invalid credentials")
	}
	return user, nil
}

func (a *API) me(c *gin.Context) {
	user, ok := auth.CurrentUser(c)
	if !ok {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "unauthorized"})
		return
	}
	c.JSON(http.StatusOK, user)
}

// health returns a small summary of the configured domain. It requires auth (the
// unauthenticated liveness probe is /api/v1/ping).
func (a *API) health(c *gin.Context) {
	count := func(model any) int64 {
		var n int64
		a.DB.Model(model).Count(&n)
		return n
	}
	c.JSON(http.StatusOK, gin.H{
		"name":    a.AppName,
		"version": a.Version,
		"counts": gin.H{
			"libraries":       count(&models.Library{}),
			"managers":        count(&models.Manager{}),
			"data_sources":    count(&models.DataSource{}),
			"tagger_profiles": count(&models.TaggerProfile{}),
			"library_items":   count(&models.LibraryItem{}),
		},
	})
}

func (a *API) listLibraries(c *gin.Context) {
	var rows []models.Library
	if err := a.DB.Order("name").Find(&rows).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to list libraries"})
		return
	}
	c.JSON(http.StatusOK, rows)
}

func (a *API) listManagers(c *gin.Context) {
	var rows []models.Manager
	if err := a.DB.Order("name").Find(&rows).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to list managers"})
		return
	}
	// Lidarr secrets are hidden via json:"-" on the model, so this is safe to return.
	c.JSON(http.StatusOK, rows)
}

func (a *API) listDataSources(c *gin.Context) {
	var rows []models.DataSource
	if err := a.DB.Order("name").Find(&rows).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to list data sources"})
		return
	}
	c.JSON(http.StatusOK, rows)
}

func (a *API) listTaggerProfiles(c *gin.Context) {
	var rows []models.TaggerProfile
	if err := a.DB.Order("name").Find(&rows).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to list tagger profiles"})
		return
	}
	c.JSON(http.StatusOK, rows)
}
