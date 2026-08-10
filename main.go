package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io/fs"
	"log"
	"mime"
	"net/http"
	"os"
	"os/signal"
	"path"
	"path/filepath"
	"strings"
	"syscall"

	"strconv"
	"time"

	_ "time/tzdata"

	"codnect.io/chrono"

	"github.com/aunefyren/autotaggerr/collection"
	"github.com/aunefyren/autotaggerr/components"
	"github.com/aunefyren/autotaggerr/database"
	"github.com/aunefyren/autotaggerr/events"
	"github.com/aunefyren/autotaggerr/files"
	"github.com/aunefyren/autotaggerr/health"
	"github.com/aunefyren/autotaggerr/logger"
	"github.com/aunefyren/autotaggerr/mirror"
	"github.com/aunefyren/autotaggerr/models"
	"github.com/aunefyren/autotaggerr/modules"
	"github.com/aunefyren/autotaggerr/process"
	"github.com/aunefyren/autotaggerr/routers"
	"github.com/aunefyren/autotaggerr/settings"
	"github.com/aunefyren/autotaggerr/utilities"
	"github.com/aunefyren/autotaggerr/web"
	"github.com/gin-contrib/cors"
	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

var (
	plexClient   *modules.PlexClient
	db           *gorm.DB
	scanRunner   *process.Runner
	mirrorRunner *mirror.Runner
	// settingsRuntime owns the recurring schedules and re-applies settings saved in
	// the UI to this running process.
	settingsRuntime *settings.Runtime
)

func main() {
	utilities.PrintASCII()

	// Create files directory
	newPath := filepath.Join(".", "config")
	err := os.MkdirAll(newPath, os.ModePerm)
	if err != nil {
		fmt.Println("failed to create 'files' directory. error: " + err.Error())
		os.Exit(1)
	}
	fmt.Println("directory 'config' valid")

	// Load config file
	err = files.LoadConfig()
	if err != nil {
		fmt.Println("failed to load configuration file. error: " + err.Error())
		os.Exit(1)
	}
	fmt.Println("configuration file loaded")

	// Create and define file for logging
	logger.InitLogger(files.ConfigFile)

	logger.Log.Info("running Autotaggerr version: " + files.ConfigFile.AutotaggerrVersion)

	// Connect to the database and apply the schema. Domain config (managers,
	// libraries, data sources, tagger profiles) lives here; bootstrap config
	// (how to reach the DB) stays in config.json.
	db, err = database.Connect(files.ConfigFile.Database)
	if err != nil {
		logger.Log.Fatal("failed to connect to database. error: " + err.Error())
		os.Exit(1)
	}
	logger.Log.Info("database connected")

	// Wire the DB into the cache layer so the MusicBrainz release cache persists to
	// the database (write-through) instead of a JSON file. Must run before LoadAllCaches.
	modules.SetDB(db)

	// Bring an empty database up to a usable baseline: the metadata and artwork
	// sources, a default tagger profile, and an admin to sign in as. Idempotent —
	// safe to run every startup.
	adminCreds, err := database.Seed(db)
	if err != nil {
		logger.Log.Fatal("failed to seed database. error: " + err.Error())
		os.Exit(1)
	}
	if adminCreds != nil {
		logger.Log.Warnf("created initial admin user %q — password: %s | API key: %s (shown once; store it now)",
			adminCreds.Username, adminCreds.Password, adminCreds.APIKey)
	}

	// Link existing release-groups to their credited artist. Artist pages read the
	// link table, so rows written before it existed would show nothing until the next
	// rebuild. Idempotent; a no-op once every row is linked. (Called here rather than
	// from database.Seed: the collection package's tests import database, so database
	// cannot import collection back.)
	if err := collection.BackfillReleaseGroupArtists(db); err != nil {
		logger.Log.Warnf("failed to link existing release-groups to their artists: %s", err.Error())
	}

	// Give wants written before provenance existed a source. Until they have one the
	// reconciliation passes cannot tell a hand-pinned edition from one they may
	// re-point, so this runs before anything can enqueue a scan or a sync. Idempotent.
	if err := collection.BackfillDesireSources(db); err != nil {
		logger.Log.Warnf("failed to backfill want provenance: %s", err.Error())
	}

	// Set GIN mode
	if files.ConfigFile.AutotaggerrEnvironment != "test" {
		gin.SetMode(gin.ReleaseMode)
	}

	// Change the config to respect flags
	var filePath *string
	var fileRootPath *string
	files.ConfigFile, filePath, fileRootPath, err = parseFlags(files.ConfigFile)
	if err != nil {
		logger.Log.Fatal("failed to parse input flags. error: " + err.Error())
		os.Exit(1)
	}
	logger.Log.Info("flags parsed")

	err = files.SaveConfig()
	if err != nil {
		logger.Log.Fatal("failed to save new config. error: " + err.Error())
		os.Exit(1)
	}

	// Set time zone from config if it is not empty
	if files.ConfigFile.Timezone != "" {
		loc, err := time.LoadLocation(files.ConfigFile.Timezone)
		if err != nil {
			logger.Log.Info("failed to set time zone from config. error: " + err.Error())
			logger.Log.Info("removing value...")

			files.ConfigFile.Timezone = ""
			err = files.SaveConfig()
			if err != nil {
				logger.Log.Fatal("failed to set new time zone in the config. error: " + err.Error())
				os.Exit(1)
			}

		} else {
			time.Local = loc
		}
	}
	logger.Log.Info("timezone set")

	// Lidarr is NOT wired here. Its credentials live on the manager row (config.json
	// seeds that row once, on first run, and is never read for it again), and both the
	// pipeline and the health check build their client from the row via
	// components.NewManager. A client built here from files.ConfigFile would be a second
	// copy that silently diverges the moment either side is edited alone.

	// configure Plex client
	if files.ConfigFile.PlexBaseURL != "" && files.ConfigFile.PlexToken != "" {
		plexClient = modules.NewPlexClient(files.ConfigFile.PlexBaseURL, files.ConfigFile.PlexToken)
	}

	// load all on-disk caches into memory once; the per-file path works purely
	// in-memory from here on (see modules/cache.go)
	modules.LoadAllCaches()

	// Apply the configured MusicBrainz request rate to the limiter.
	components.ApplyDataSourceRateLimits(db)

	// Close out any event left "running" by a previous process. Must run before the
	// schedules and startup jobs below create new running events, so it only touches
	// orphans from an interrupted run — the "two scans stuck at running" after a
	// restart.
	events.ReconcileRunning(db)

	// Rewrite events recorded under a type name that has since been renamed, so the
	// feed describes old runs with the verb they would be called today.
	events.MigrateLegacyTypes(db)

	// Shared scan runner: the cron job, the startup run, and the API all drive
	// library processing through this one instance (single-run guard + status).
	scanRunner = process.NewRunner(db, plexClient, files.ConfigFile)

	// The metadata-refresh runner is owned by the scan runner and already wired to
	// yield to it: both spend the same one-request-per-second MusicBrainz budget,
	// and the file-writing job is the one with a user waiting on it.
	mirrorRunner = scanRunner.Refresher()

	// Scheduled health checks for the configured connections. A baseline runs at
	// startup (off the main goroutine, so a slow endpoint cannot stall boot); the cron
	// then re-checks and records an event only when a connection's health changes.
	// Managers are read from the database on every run, so this probes the same
	// credentials the pipeline uses and picks up an edit without a restart.
	healthChecker := health.NewChecker(db, plexClient)
	if filePath == nil {
		go healthChecker.Run()
	}

	// Every recurring job is described once and owned by the settings runtime, which
	// installs them here and re-installs them when a schedule is saved in the UI. The
	// mirror's refresh is enqueued through the scan runner (SyncDrift) rather than
	// started directly, so every background job shares one serial queue.
	settingsRuntime = settings.NewRuntime(
		chrono.NewDefaultTaskScheduler(),
		scanRunner.SetConcurrency,
		settings.CronJob{
			Name:     "scan",
			Run:      func() { scanRunner.RunAll() },
			Schedule: func(c models.ConfigStruct) string { return c.AutotaggerrProcessCronSchedule },
		},
		settings.CronJob{
			Name:     "metadata refresh",
			Run:      func() { scanRunner.SyncDrift() },
			Schedule: func(c models.ConfigStruct) string { return c.AutotaggerrMirrorCronSchedule },
			Enabled:  func(c models.ConfigStruct) bool { return !c.AutotaggerrMirrorDisabled },
		},
		settings.CronJob{
			Name:     "health check",
			Run:      healthChecker.Run,
			Schedule: func(c models.ConfigStruct) string { return c.AutotaggerrHealthCronSchedule },
		},
	)
	settingsRuntime.Schedule(files.ConfigFile)

	// Nothing is kicked off here. Both verbs used to have an "on start up" config key,
	// from before there was a UI to press: a scan and a metadata refresh are now a
	// button on the Activity page and a schedule that is installed above, so a restart
	// is a restart rather than an unattended pass over the whole library.

	// process file path
	if filePath != nil && fileRootPath != nil {
		refreshSet := modules.NewAlbumRefreshSet(nil)

		// Resolve the owning library's manager + tagger from the DB and run the
		// component pipeline, which also records the correlation to library_items.
		library, manager, tagger, buildErr := components.BuildForFile(db, *filePath, *fileRootPath)
		if buildErr != nil {
			logger.Log.Error("failed to build pipeline for file. error: " + buildErr.Error())
			// nil detail collector: a one-shot single-file run records no Activity event
			// for the detail to hang off.
		} else if _, _, err := components.ProcessFile(db, library, manager, tagger, plexClient, refreshSet, nil, *filePath, *fileRootPath, files.ConfigFile.AutotaggerrVersion); err != nil {
			logger.Log.Error("failed to process file. error: " + err.Error())
		}

		for albumName, albumKey := range refreshSet.Snapshot() {
			if err := plexClient.RefreshAlbum(albumKey); err != nil {
				logger.Log.Error("failed to inform Plex to refresh album. error: " + err.Error())
			}
			logger.Log.Debug("triggered Plex refresh for album: " + albumName)
		}
	}

	// Initialize Router
	router := initRouter(db, scanRunner, mirrorRunner, files.ConfigFile)

	logger.Log.Info("router initialized. starting Autotaggerr at http://*:" + strconv.Itoa(files.ConfigFile.AutotaggerrPort))

	server := &http.Server{
		Addr:    ":" + strconv.Itoa(files.ConfigFile.AutotaggerrPort),
		Handler: router,
	}
	go func() {
		if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Fatal(err)
		}
	}()

	shutdown(server, scanRunner, settingsRuntime)
}

// shutdownGrace bounds how long a job already in flight is given to finish. A scan of
// a large library takes far longer than this; the point is not to wait it out but to
// let the common case — a job seconds from done, or none at all — end cleanly instead
// of being killed mid-write.
const shutdownGrace = 30 * time.Second

// shutdown blocks until the process is asked to stop, then stops it in the order that
// makes the stop mean something.
//
// A container restart used to be indistinguishable from a crash: the process was
// killed wherever it happened to be, and the only thing that noticed was
// events.ReconcileRunning closing the orphaned event on the next boot. That is a
// safety net, and a poor substitute for stopping on purpose.
//
// The order is the whole design. Schedules first, so no cron fires into a process
// that is leaving. Then HTTP, which both stops new requests arriving and waits for the
// ones in flight — including the synchronous re-tags that write files outside the
// queue. Only then the job runner, because until the API is closed it can still be
// handed work.
func shutdown(server *http.Server, runner *process.Runner, schedules *settings.Runtime) {
	stop := make(chan os.Signal, 1)
	signal.Notify(stop, os.Interrupt, syscall.SIGTERM)
	logger.Log.Infof("received %s; shutting down", <-stop)

	schedules.Stop()

	ctx, cancel := context.WithTimeout(context.Background(), shutdownGrace)
	defer cancel()
	if err := server.Shutdown(ctx); err != nil {
		logger.Log.Warnf("HTTP server did not shut down cleanly: %s", err.Error())
	}

	if err := runner.Shutdown(ctx); err != nil {
		// The job outlived the grace period. Its event stays `running` and is closed
		// out on the next boot, exactly as after a crash — the difference is that this
		// one is logged as the deliberate choice it is.
		logger.Log.Warnf("a background job was still running after %s; exiting anyway", shutdownGrace)
	}

	logger.Log.Info("Autotaggerr stopped")
}

func initRouter(db *gorm.DB, scanRunner *process.Runner, mirrorRunner *mirror.Runner, cfg models.ConfigStruct) *gin.Engine {
	router := gin.Default()

	router.Use(cors.New(cors.Config{
		AllowOrigins: []string{"*"},
		// AllowAllOrigins:  true,
		AllowMethods:     []string{"GET", "POST", "PATCH", "PUT", "DELETE"},
		AllowHeaders:     []string{"Origin", "Content-Type", "Authorization", "X-Api-Key", "Access-Control-Allow-Origin"},
		ExposeHeaders:    []string{"Content-Length"},
		AllowCredentials: true,
		AllowOriginFunc:  func(origin string) bool { return true },
		MaxAge:           12 * time.Hour,
	}))

	// Legacy liveness probe.
	router.GET("/api/ping", routers.APIPing)

	// Versioned JSON API (auth + domain endpoints).
	api := &routers.API{
		DB:         db,
		Scan:       scanRunner,
		Mirror:     mirrorRunner,
		Rebuilder:  collection.NewRebuilder(db),
		Meta:       modules.NewMetadataSource(),
		Settings:   settingsRuntime,
		SigningKey: files.GetPrivateKey(0),
		AppName:    cfg.AutotaggerrName,
		Version:    cfg.AutotaggerrVersion,
	}
	api.Register(router.Group("/api/v1"))

	// Serve the embedded single-page app. Real asset requests are served from the
	// build; everything else falls back to index.html so client-side routing works.
	// /api/* is never hijacked. Bytes are written directly (with the right MIME) to
	// avoid http.FileServer's directory-index redirects.
	distFS, err := fs.Sub(web.Dist, "dist")
	if err != nil {
		logger.Log.Fatal("failed to open embedded web assets. error: " + err.Error())
	}
	serveAsset := func(c *gin.Context, name string) {
		data, readErr := fs.ReadFile(distFS, name)
		if readErr != nil {
			c.Status(http.StatusNotFound)
			return
		}
		ctype := mime.TypeByExtension(path.Ext(name))
		if ctype == "" {
			ctype = "application/octet-stream"
		}
		c.Data(http.StatusOK, ctype, data)
	}
	router.NoRoute(func(c *gin.Context) {
		reqPath := c.Request.URL.Path
		if strings.HasPrefix(reqPath, "/api/") || reqPath == "/api" {
			c.JSON(http.StatusNotFound, gin.H{"error": "not found"})
			return
		}
		asset := strings.TrimPrefix(reqPath, "/")
		if asset != "" {
			if _, statErr := fs.Stat(distFS, asset); statErr == nil {
				serveAsset(c, asset)
				return
			}
		}
		serveAsset(c, "index.html") // client-side route → SPA entry
	})

	return router
}

func parseFlags(configFile models.ConfigStruct) (models.ConfigStruct, *string, *string, error) {
	// Define flag variables with the configuration file as default values
	var port = flag.Int("port", configFile.AutotaggerrPort, "The port Autotaggerr is listening on.")
	var externalURL = flag.String("externalurl", configFile.AutotaggerrExternalURL, "The URL others would use to access Autotaggerr.")
	var timezone = flag.String("tz", configFile.Timezone, "The timezone Autotaggerr is running in.")
	var concurrency = flag.Int("concurrency", configFile.AutotaggerrProcessConcurrency, "Number of files processed in parallel per library scan.")

	// SMTP flags
	var smtpDisabled = flag.String("disablesmtp", "false", "Disables user verification using e-mail.")
	var smtpHost = flag.String("smtphost", configFile.SMTPHost, "The SMTP server which sends e-mail.")
	var smtpPort = flag.Int("smtpport", configFile.SMTPPort, "The SMTP server port.")
	var smtpUsername = flag.String("smtpusername", configFile.SMTPUsername, "The username used to verify against the SMTP server.")
	var smtpPassword = flag.String("smtppassword", configFile.SMTPPassword, "The password used to verify against the SMTP server.")
	var smtpFrom = flag.String("smtpfrom", configFile.SMTPFrom, "The sender address when sending e-mail from Autotaggerr.")

	//file
	var filePath = flag.String("file", "", "A single file to process")
	var fileRootPath = flag.String("fileRoot", "", "What directory is the root of the file, the folder containing the artist folder")

	// Parse the flags from input
	flag.Parse()

	// Track which flags were actually provided on the command line, so we only
	// override config values that the user explicitly set instead of clobbering
	// the whole config on every startup.
	provided := map[string]bool{}
	flag.Visit(func(f *flag.Flag) { provided[f.Name] = true })

	if provided["port"] {
		configFile.AutotaggerrPort = *port
	}

	if provided["externalurl"] {
		configFile.AutotaggerrExternalURL = *externalURL
	}

	if provided["tz"] {
		configFile.Timezone = *timezone
	}

	if provided["concurrency"] {
		configFile.AutotaggerrProcessConcurrency = *concurrency
	}

	// Respect the flag only if it was passed; "true" disables SMTP.
	if provided["disablesmtp"] {
		configFile.SMTPEnabled = strings.ToLower(*smtpDisabled) != "true"
	}

	if provided["smtphost"] {
		configFile.SMTPHost = *smtpHost
	}

	if provided["smtpport"] {
		configFile.SMTPPort = *smtpPort
	}

	if provided["smtpusername"] {
		configFile.SMTPUsername = *smtpUsername
	}

	if provided["smtppassword"] {
		configFile.SMTPPassword = *smtpPassword
	}

	if provided["smtpfrom"] {
		configFile.SMTPFrom = *smtpFrom
	}

	// Only treat single-file processing as requested when both flags are set to
	// a non-empty value; otherwise return nil so the service runs normally.
	if !provided["file"] || *filePath == "" || !provided["fileRoot"] || *fileRootPath == "" {
		filePath = nil
		fileRootPath = nil
	}

	// Failsafe, if port is 0, set to default 8080
	if configFile.AutotaggerrPort == 0 {
		configFile.AutotaggerrPort = 8080
	}

	return configFile, filePath, fileRootPath, nil
}
