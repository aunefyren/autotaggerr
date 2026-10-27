package routers

import (
	"errors"
	"net/http"

	"github.com/aunefyren/autotaggerr/artwork"
	"github.com/aunefyren/autotaggerr/auth"
	"github.com/aunefyren/autotaggerr/collection"
	"github.com/aunefyren/autotaggerr/components"
	"github.com/aunefyren/autotaggerr/logger"
	"github.com/aunefyren/autotaggerr/loudness"
	"github.com/aunefyren/autotaggerr/metadata"
	"github.com/aunefyren/autotaggerr/mirror"
	"github.com/aunefyren/autotaggerr/models"
	"github.com/aunefyren/autotaggerr/modules"
	"github.com/aunefyren/autotaggerr/process"
	"github.com/aunefyren/autotaggerr/settings"
	"github.com/aunefyren/autotaggerr/tempo"
	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

// API holds the dependencies shared by the JSON API handlers.
type API struct {
	DB     *gorm.DB
	Scan   *process.Runner
	Mirror *mirror.Runner
	// Artwork is the image-cache refresh verb. Separate from Mirror because artwork
	// providers are a different kind of data source spending a different budget; it
	// may be nil, in which case the artwork routes report unavailable.
	Artwork *artwork.Runner
	// Tempo is the BPM analysis verb. Its own runner for the reason artwork has one:
	// it spends nothing the scan queue serialises. May be nil, in which case the
	// tempo routes report unavailable.
	Tempo *tempo.Runner
	// Loudness runs the *Analyse loudness* verb (ReplayGain). May be nil, in which case
	// the loudness routes report unavailable.
	Loudness *loudness.Runner
	// Rebuilder re-derives the collection after a handler changes the file index.
	// May be nil, in which case requests are dropped (see collection.Rebuilder).
	Rebuilder *collection.Rebuilder
	// Meta is the MusicBrainz metadata source used by the MB-bound handlers. May be
	// nil: meta() falls back to the real modules-backed source, so production wiring
	// is optional and a test injects a fake by setting this field. This is the seam
	// that makes the MB-bound handlers coverable without touching musicbrainz.org.
	Meta metadata.MetadataSource
	// Settings applies saved settings to the running process (schedules, log level,
	// scan concurrency). May be nil: a nil *settings.Runtime saves to config.json and
	// reports everything as needing a restart, which is the correct behaviour for a
	// caller that owns no scheduler.
	Settings   *settings.Runtime
	SigningKey []byte
	AppName    string
	Version    string

	// artists memoises GET /artists against a fingerprint of the tables it reads
	// (see listArtists). Zero value is ready to use.
	artists artistListCache
}

// meta returns the injected metadata source, or the real MusicBrainz-backed one when
// none was wired. The fallback keeps every non-test caller working (main wires it
// explicitly), while a test sets Meta to a fake to exercise the MB-bound paths with
// zero network.
func (a *API) meta() metadata.MetadataSource {
	if a.Meta != nil {
		return a.Meta
	}
	return modules.NewMetadataSource()
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

	// External login. These must be public — they are how you authenticate — and
	// they are safe to expose: the provider list carries no secrets, and the flow
	// itself is protected by the signed state/nonce/PKCE cookie.
	rg.GET("/auth/providers", a.listLoginProviders)
	rg.GET("/auth/oidc/:id/start", a.startOIDCLogin)
	rg.GET("/auth/oidc/:id/callback", a.completeOIDCLogin)

	// Artwork is public, and has to be: it is loaded by <img> tags, which cannot
	// send an Authorization header. It leaks nothing — every response is an album
	// cover or artist photo keyed by a MusicBrainz ID, the endpoint answers for any
	// MBID rather than only ones in this collection, and the providers' credentials
	// stay server-side. See modules/artwork.go for the caching and rate limits that
	// keep it from being a useful proxy to abuse.
	rg.GET("/artwork/:entity/:mbid", a.artwork)

	// The MCP endpoint for AI assistants. Authenticated like everything below, but
	// mounted on its own because it takes an API key as a Bearer token too.
	a.registerMCP(rg)

	protected := rg.Group("")
	protected.Use(auth.Middleware(a.DB, a.SigningKey))
	{
		protected.GET("/auth/me", a.me)
		// API keys are managed from a signed-in session only: a key that could mint
		// keys would turn one leaked key into a permanent foothold that survives
		// revoking it.
		protected.GET("/auth/me/api-keys", requireSession, a.listAPIKeys)
		protected.POST("/auth/me/api-keys", requireSession, a.createAPIKey)
		protected.DELETE("/auth/me/api-keys/:id", requireSession, a.deleteAPIKey)
		protected.GET("/health", a.health)

		// Which artwork kinds can actually be served, so the UI skips requests that
		// would only 404. Behind auth because only the app reads it; the images
		// themselves stay public for <img>.
		protected.GET("/artwork-capabilities", a.artworkCapabilities)

		// Data sources
		protected.GET("/data-sources", a.listDataSources)
		protected.POST("/data-sources", a.createDataSource)
		protected.GET("/data-sources/:id", a.getDataSource)
		protected.PUT("/data-sources/:id", a.updateDataSource)
		protected.DELETE("/data-sources/:id", a.deleteDataSource)

		// Auth providers (admin configuration of external login)
		protected.GET("/auth-providers", a.listAuthProviders)
		protected.POST("/auth-providers", a.createAuthProvider)
		protected.GET("/auth-providers/:id", a.getAuthProvider)
		protected.PUT("/auth-providers/:id", a.updateAuthProvider)
		protected.DELETE("/auth-providers/:id", a.deleteAuthProvider)

		// Managers
		protected.GET("/managers", a.listManagers)
		protected.POST("/managers", a.createManager)
		protected.GET("/managers/:id", a.getManager)
		protected.PUT("/managers/:id", a.updateManager)
		protected.DELETE("/managers/:id", a.deleteManager)
		protected.POST("/managers/:id/test", a.testManager)

		// Tagger profiles
		protected.GET("/tagger-profiles", a.listTaggerProfiles)
		protected.POST("/tagger-profiles", a.createTaggerProfile)
		protected.GET("/tagger-profiles/:id", a.getTaggerProfile)
		protected.PUT("/tagger-profiles/:id", a.updateTaggerProfile)
		protected.DELETE("/tagger-profiles/:id", a.deleteTaggerProfile)
		protected.GET("/naming", a.namingInfo)
		protected.POST("/naming/preview", a.namingPreview)

		// Libraries
		protected.GET("/libraries", a.listLibraries)
		protected.POST("/libraries", a.createLibrary)
		protected.GET("/libraries/:id", a.getLibrary)
		protected.PUT("/libraries/:id", a.updateLibrary)
		protected.DELETE("/libraries/:id", a.deleteLibrary)
		protected.POST("/libraries/:id/process", a.processLibrary)
		protected.POST("/libraries/:id/refresh", a.refreshLibrary)
		protected.POST("/libraries/:id/retag", a.retagLibrary)
		protected.POST("/libraries/:id/recorrelate", a.recorrelateLibrary)
		// Media servers: listed (without tokens) and their sections read by anyone who
		// edits a library; created, edited and deleted by an admin.
		protected.GET("/media-servers", a.listMediaServers)
		protected.GET("/media-servers/:id/sections", a.mediaServerSections)
		protected.POST("/organise/preview", a.previewOrganise)
		protected.POST("/organise", a.organise)

		// Library items (the correlation index)
		protected.GET("/library-items", a.listLibraryItems)
		protected.GET("/library-items/:id/tags", a.itemTags)
		protected.GET("/library-items/:id/tempo", a.itemTempo)

		// Manual attach — identify a file MusicBrainz/Lidarr could not
		protected.GET("/search/releases", a.searchReleases)
		protected.GET("/releases/:mbid/tracks", a.releaseTracks)
		protected.POST("/library-items/:id/attach", a.attachItem)
		protected.DELETE("/library-items/:id/attach", a.detachItem)
		// A pin held because the file's audio changed is settled by confirming it for
		// the new audio (here) or releasing it (DELETE …/attach above).
		protected.POST("/library-items/:id/pin/confirm", a.confirmPin)
		// Bulk attach lives under its own prefix rather than /library-items/bulk,
		// which would collide with the :id parameter above.
		protected.POST("/attach/preview", a.previewBulkAttach)
		protected.POST("/attach/bulk", a.attachBulk)
		// What the selected files say about their edition, to mark in the release search.
		protected.POST("/attach/clues", a.attachClues)

		// The download's release group ({Release Group}), set by hand on one file or
		// many. Its own prefix for the same reason as bulk attach.
		protected.PUT("/item-release-group", a.setReleaseGroup)

		// AcoustID: suggests what an unmatched file is. Suggestion only — it never
		// writes a correlation, so it feeds the attach picker above.
		protected.GET("/identify", a.identifyAvailability)
		protected.POST("/library-items/:id/identify", a.identifyItem)

		// The four verbs at collection scope. Process walks the disk, Scan re-derives
		// the collection from the index, Refresh re-reads MusicBrainz, Retag rewrites
		// tags from what is already known. The same four are offered per library and
		// per artist below, minus the scopes that make no sense (see scan_items.go).
		protected.POST("/process", a.processAll)
		protected.POST("/scan", a.scanCollection)
		protected.POST("/refresh", a.refreshAll)
		protected.POST("/retag", a.retagAll)
		protected.GET("/process/status", a.processStatus)
		protected.POST("/process/cancel", a.cancelJob)

		// MusicBrainz mirror: the scheduled refresh of the local entity cache.
		protected.GET("/mirror/status", a.mirrorStatus)
		protected.POST("/mirror/sync", a.triggerMirror)
		protected.POST("/mirror/cancel", a.cancelMirror)

		// Artwork: the scheduled fetch of covers and artist images. Its own verb
		// rather than a stage of the mirror — see docs/artwork.md.
		protected.GET("/artwork/status", a.artworkStatus)
		protected.POST("/artwork/refresh", a.triggerArtworkRefresh)
		protected.POST("/artwork/cancel", a.cancelArtworkRefresh)

		// Tempo: BPM detection over opted-in libraries. Stores results only; tagging
		// writes them — see docs/tempo.md.
		protected.GET("/tempo/status", a.tempoStatus)
		protected.POST("/tempo/analyse", a.triggerTempoAnalysis)
		protected.POST("/tempo/cancel", a.cancelTempoAnalysis)
		// Loudness (ReplayGain) analysis: the same three routes — see docs/replaygain.md.
		protected.GET("/loudness/status", a.loudnessStatus)
		protected.POST("/loudness/analyse", a.triggerLoudnessAnalysis)
		protected.POST("/loudness/cancel", a.cancelLoudnessAnalysis)

		// Activity events
		protected.GET("/events", a.listEvents)
		protected.GET("/events/:id", a.getEvent)
		// The files behind one MusicBrainz identifier — which local audio an Activity
		// row's MBID actually stands for. Fetched on demand, per row, because a detail
		// list holds hundreds of identifiers and only the one you are looking at is
		// worth its file paths.
		protected.GET("/mb/:mbid/files", a.mbFiles)

		// MusicBrainz identity migrations (merges and deletions upstream).
		protected.GET("/migrations", a.listMigrations)
		protected.GET("/migrations/policy", a.migrationPolicy)
		protected.POST("/migrations/:id/approve", a.approveMigration)
		protected.POST("/migrations/:id/release-files", a.releaseMigrationFiles)
		protected.POST("/migrations/:id/dismiss", a.dismissMigration)
		protected.POST("/migrations/verify", a.verifyIdentities)

		// Collection (present vs wanted)
		protected.GET("/artists", a.listArtists)
		protected.GET("/artists/:mbid", a.getArtist)
		protected.POST("/artists/:mbid/monitor", a.setArtistMonitored)
		// Per-artist actions: the same four verbs as the collection-wide buttons,
		// narrowed to one artist (see routers/scan_items.go).
		protected.POST("/artists/:mbid/process", a.processArtist)
		protected.POST("/artists/:mbid/scan", a.scanArtist)
		protected.POST("/artists/:mbid/refresh", a.refreshArtist)
		protected.POST("/artists/:mbid/retag", a.retagArtist)
		protected.POST("/artists/:mbid/recorrelate", a.recorrelateArtist)
		protected.POST("/artists/:mbid/repair-albums", a.repairArtistAlbums)
		protected.GET("/artists/:mbid/manager-candidates", a.managerArtistCandidates)
		protected.POST("/artists/:mbid/same-as", a.markSameArtist)
		protected.GET("/search/artists", a.searchArtists)
		protected.GET("/search/artists/:mbid/release-groups", a.searchArtistReleaseGroups)
		protected.GET("/search/artists/:mbid/info", a.searchArtistInfo)
		protected.POST("/artists", a.addArtist)
		protected.GET("/release-groups/:mbid/releases", a.releaseGroupEditions)
		protected.POST("/release-groups/:mbid/recorrelate", a.recorrelateReleaseGroup)
		protected.GET("/artists/:mbid/info", a.artistInfo)
		protected.GET("/artists/:mbid/discography", a.discography)
		protected.GET("/artists/:mbid/release-groups/:rgid", a.releaseGroupDetail)
		protected.GET("/artists/:mbid/release-groups/:rgid/plex", a.albumPlexLinks)
		protected.POST("/artists/:mbid/release-groups/:rgid/same-as", a.markSameAlbum)
		protected.POST("/artists/:mbid/follow", a.updateFollow)
		protected.POST("/artists/:mbid/desires", a.setDesire)
		protected.DELETE("/artists/:mbid/desires", a.clearDesire)
		// Detaching is a change of authority over one artist: the manager stops
		// deciding what is wanted for it, and what the manager had decided is kept as
		// the user's own. DELETE is the undo, not a deletion of anything.
		protected.POST("/artists/:mbid/detach", a.detachArtist)
		protected.DELETE("/artists/:mbid/detach", a.reattachArtist)
		protected.POST("/collection/sync-lidarr", a.syncLidarr)
		protected.POST("/collection/follow", a.followArtists)
		protected.GET("/collection/policy", a.getCollectionPolicy)
		protected.PUT("/collection/policy", a.updateCollectionPolicy)
		protected.POST("/artists/:mbid/sync-lidarr", a.syncLidarrArtist)

		// Settings are admin-only: they carry the port, the schedules and the SMTP
		// credentials, which is a different kind of power from the rest of the API.
		admin := protected.Group("")
		admin.Use(auth.RequireAdmin())
		{
			admin.GET("/settings", a.getSettings)
			admin.PUT("/settings", a.updateSettings)
			admin.GET("/settings/secrets/:key", a.revealSecret)
			admin.POST("/settings/cron/preview", a.previewCron)
			admin.POST("/settings/email/test", a.sendTestEmail)

			// File import reads, moves and deletes files at a path the request names,
			// anywhere on the server — admin power for the same reason as settings.
			admin.GET("/import/scan", a.importScan)
			admin.GET("/import/folders", a.importFolders)
			admin.POST("/import/preview", a.importPreview)
			admin.POST("/import", a.importFiles)

			admin.POST("/media-servers", a.createMediaServer)
			admin.GET("/media-servers/:id", a.getMediaServer)
			admin.PUT("/media-servers/:id", a.updateMediaServer)
			admin.DELETE("/media-servers/:id", a.deleteMediaServer)
			admin.POST("/media-servers/:id/test", a.testMediaServer)

			// Download clients, and the finished downloads they offer for import.
			admin.GET("/download-clients", a.listDownloadClients)
			admin.POST("/download-clients", a.createDownloadClient)
			admin.GET("/download-clients/:id", a.getDownloadClient)
			admin.PUT("/download-clients/:id", a.updateDownloadClient)
			admin.DELETE("/download-clients/:id", a.deleteDownloadClient)
			admin.POST("/download-clients/:id/test", a.testDownloadClient)
			admin.GET("/import/downloads", a.listDownloads)
			admin.POST("/import/downloads/poll", a.pollDownloads)
			admin.GET("/import/downloads/:id/scan", a.scanDownload)
			admin.POST("/import/downloads/:id/dismiss", a.dismissDownload)
			admin.POST("/import/downloads/:id/restore", a.restoreDownload)
		}
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

// requireSession refuses a request authenticated with an API key.
func requireSession(c *gin.Context) {
	if cred, ok := auth.CurrentCredential(c); !ok || cred.Kind != auth.CredentialSession {
		c.AbortWithStatusJSON(http.StatusForbidden, gin.H{"error": "API keys are managed from a signed-in session, not with an API key"})
		return
	}
	c.Next()
}

// listAPIKeys returns the caller's keys: name, scope, prefix and when each was last
// used. Never the key itself — only its hash is stored.
func (a *API) listAPIKeys(c *gin.Context) {
	user, _ := auth.CurrentUser(c)
	keys := []models.APIKey{}
	if err := a.DB.Where("user_id = ?", user.ID).Order("created_at").Find(&keys).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to list API keys"})
		return
	}
	c.JSON(http.StatusOK, keys)
}

// createAPIKey mints a named, scoped key and returns it. This is the only time the
// key is ever shown.
func (a *API) createAPIKey(c *gin.Context) {
	user, _ := auth.CurrentUser(c)
	var body struct {
		Name  string `json:"name"`
		Scope string `json:"scope"`
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid body"})
		return
	}
	key, row, err := auth.CreateAPIKey(a.DB, user.ID, body.Name, body.Scope)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	logger.Log.Infof("API key %q (%s) created for user %s", row.Name, row.Scope, user.Username)
	c.JSON(http.StatusCreated, gin.H{"api_key": key, "key": row})
}

// deleteAPIKey revokes one of the caller's keys. It stops working at once.
func (a *API) deleteAPIKey(c *gin.Context) {
	user, _ := auth.CurrentUser(c)
	id, ok := a.idParam(c)
	if !ok {
		return
	}
	res := a.DB.Where("id = ? AND user_id = ?", id, user.ID).Delete(&models.APIKey{})
	if res.Error != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to revoke the key"})
		return
	}
	if res.RowsAffected == 0 {
		c.JSON(http.StatusNotFound, gin.H{"error": "API key not found"})
		return
	}
	logger.Log.Infof("API key %s revoked by user %s", id, user.Username)
	c.JSON(http.StatusOK, gin.H{"revoked": true})
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
		"attention": a.attention(),
		"setup":     a.setupState(),
	})
}

// setupState is what the sidebar's setup steps and the overview's Get started card
// read: what a fresh install still lacks, in the order it has to be added. Each field
// is a count, so a step undone later (the only library deleted) shows as undone again.
func (a *API) setupState() gin.H {
	count := func(model any, where string, args ...any) int64 {
		var n int64
		q := a.DB.Model(model)
		if where != "" {
			q = q.Where(where, args...)
		}
		q.Count(&n)
		return n
	}
	return gin.H{
		"managers":  count(&models.Manager{}, ""),
		"libraries": count(&models.Library{}, ""),
		// Processed, rather than merely added: a library none of whose files has been
		// indexed is one nobody has run yet.
		"indexed_files": count(&models.LibraryItem{}, ""),
		// Libraries a download can be imported into. A library with no manager is
		// native, so it counts; one under Lidarr does not, and a setup with only those
		// has no use for a download client.
		"importable_libraries": count(&models.Library{},
			"manager_id IS NULL OR manager_id NOT IN (?)",
			a.DB.Model(&models.Manager{}).Select("id").Where("type = ?", models.ManagerTypeLidarr)),
		"download_clients": count(&models.DownloadClient{}, ""),
	}
}

// attention counts the files waiting on something, split by what they wait on — the
// dashboard's answer to "is anything wrong?". Each figure is one Items filter, so the
// card can link straight to the rows behind it.
//
// A failure that will retry by itself is counted apart from one that will not: during
// a MusicBrainz outage every file in a run fails at once, and one number for both read
// as a library full of broken files.
func (a *API) attention() gin.H {
	count := func(where string, args ...any) int64 {
		var n int64
		a.DB.Model(&models.LibraryItem{}).Where(where, args...).Count(&n)
		return n
	}
	return gin.H{
		"errors":         count("status = ? AND last_error_transient = ?", models.LibraryItemStatusError, false),
		"retrying":       count("status = ? AND last_error_transient = ?", models.LibraryItemStatusError, true),
		"pins_held":      count("pin_held = ?", true),
		"unmatched_wait": count("status = ? AND last_error_transient = ?", models.LibraryItemStatusUnmatched, true),
	}
}

func (a *API) listLibraries(c *gin.Context) {
	var rows []models.Library
	if err := a.DB.Preload("MediaServers").Order("name").Find(&rows).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to list libraries"})
		return
	}
	for i := range rows {
		if rows[i].MediaServers == nil {
			rows[i].MediaServers = []models.LibraryMediaServer{}
		}
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

// dataSourceView is a data source as the Data sources page lists it. InUse marks the
// one MusicBrainz source the client reads; the rule that picks it lives in
// components.MusicBrainzSourceInUse and is not repeated in the SPA.
type dataSourceView struct {
	models.DataSource
	InUse bool `json:"in_use,omitempty"`
	// FixedRateLimit is the pace the app holds itself to for a provider whose rate is
	// not a setting (requests per second; per image host for artwork). Absent for
	// MusicBrainz, whose RateLimit is the row's own.
	FixedRateLimit float64 `json:"fixed_rate_limit,omitempty"`
}

// fixedRateLimit is FixedRateLimit for a data source type: 0 where the row's own
// RateLimit is what the client reads.
func fixedRateLimit(dsType string) float64 {
	switch dsType {
	case models.DataSourceTypeCoverArtArchive, models.DataSourceTypeFanart:
		return modules.ArtworkRequestsPerSecond()
	case models.DataSourceTypeAcoustID:
		return modules.AcoustIDRequestsPerSecond()
	}
	return 0
}

func (a *API) listDataSources(c *gin.Context) {
	var rows []models.DataSource
	if err := a.DB.Order("name").Find(&rows).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to list data sources"})
		return
	}
	inUse, ok, err := components.MusicBrainzSourceInUse(a.DB)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to list data sources"})
		return
	}
	out := make([]dataSourceView, len(rows))
	for i, r := range rows {
		out[i] = dataSourceView{DataSource: r, InUse: ok && r.ID == inUse.ID, FixedRateLimit: fixedRateLimit(r.Type)}
	}
	c.JSON(http.StatusOK, out)
}

func (a *API) listTaggerProfiles(c *gin.Context) {
	var rows []models.TaggerProfile
	if err := a.DB.Order("name").Find(&rows).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to list tagger profiles"})
		return
	}
	c.JSON(http.StatusOK, rows)
}
