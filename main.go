package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"

	"strconv"
	"time"

	_ "time/tzdata"

	"codnect.io/chrono"

	"github.com/aunefyren/autotaggerr/files"
	"github.com/aunefyren/autotaggerr/logger"
	"github.com/aunefyren/autotaggerr/models"
	"github.com/aunefyren/autotaggerr/modules"
	"github.com/aunefyren/autotaggerr/routers"
	"github.com/aunefyren/autotaggerr/utilities"
	"github.com/gin-contrib/cors"
	"github.com/gin-gonic/gin"
)

// jobMu ensures only one scheduled job runs at a time, so the two cron jobs
// cannot overlap each other even if their schedules fire simultaneously.
var jobMu sync.Mutex

// Per-job running flags prevent the same job from being queued twice while a
// previous run is still in progress. CompareAndSwap makes this check atomic.
var (
	libraryScanRunning atomic.Bool
)

var (
	lidarrClient *modules.LidarrClient
	plexClient   *modules.PlexClient
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

	// configure Lidarr client
	if files.ConfigFile.LidarrBaseURL != "" && files.ConfigFile.LidarrAPIKey != "" {
		lidarrClient = modules.NewLidarrClient(files.ConfigFile.LidarrBaseURL, files.ConfigFile.LidarrAPIKey, &files.ConfigFile.LidarrHeaderCookie)
		health, err := lidarrClient.HealthCheck()
		if err != nil {
			logger.Log.Error("failed to health check Lidarr. error: " + err.Error())
		} else if !health {
			logger.Log.Error("Lidarr connection is unhealthy")
		} else {
			logger.Log.Info("Lidarr connection is healthy")
		}
	}

	// configure Plex client
	if files.ConfigFile.PlexBaseURL != "" && files.ConfigFile.PlexToken != "" {
		plexClient = modules.NewPlexClient(files.ConfigFile.PlexBaseURL, files.ConfigFile.PlexToken)
		health, err := plexClient.HealthCheck()
		if err != nil {
			logger.Log.Error("failed to health check Plex. error: " + err.Error())
		} else if !health {
			logger.Log.Error("Plex connection is unhealthy")
		} else {
			logger.Log.Info("Plex connection is healthy")
		}
	}

	// load all on-disk caches into memory once; the per-file path works purely
	// in-memory from here on (see modules/cache.go)
	modules.LoadAllCaches()

	// Create task scheduler for sunday reminders
	taskScheduler := chrono.NewDefaultTaskScheduler()

	_, err = taskScheduler.ScheduleWithCron(func(ctx context.Context) {
		processLibraries(files.ConfigFile.AutotaggerrLibraries, lidarrClient, plexClient, files.ConfigFile)
	}, files.ConfigFile.AutotaggerrProcessCronSchedule)
	if err != nil {
		logger.Log.Error("library process task was not scheduled successfully.")
	}

	// start library process if no file is configured and the feature is enabled
	if files.ConfigFile.AutotaggerrProcessOnStartUp && filePath == nil {
		go processLibraries(files.ConfigFile.AutotaggerrLibraries, lidarrClient, plexClient, files.ConfigFile)
	}

	// process file path
	if filePath != nil && fileRootPath != nil {
		refreshSet := modules.NewAlbumRefreshSet(nil)

		_, _, err := modules.ProcessTrackFile(*filePath, lidarrClient, plexClient, refreshSet, *fileRootPath, files.ConfigFile)
		if err != nil {
			logger.Log.Error("failed to process file. error: " + err.Error())
		}

		// persist any cache changes made while processing this file (writes are batched)
		modules.FlushCaches()
		for albumName, albumKey := range refreshSet.Snapshot() {
			if err := plexClient.RefreshAlbum(albumKey); err != nil {
				logger.Log.Error("failed to inform Plex to refresh album. error: " + err.Error())
			}
			logger.Log.Debug("triggered Plex refresh for album: " + albumName)
		}
	}

	// Initialize Router
	router := initRouter()

	logger.Log.Info("router initialized. starting Autotaggerr at http://*:" + strconv.Itoa(files.ConfigFile.AutotaggerrPort))

	log.Fatal(router.Run(":" + strconv.Itoa(files.ConfigFile.AutotaggerrPort)))
}

func initRouter() *gin.Engine {
	router := gin.Default()

	router.LoadHTMLGlob("web/*/*.html")

	// API endpoint
	api := router.Group("/api")
	{
		api.GET("/ping", routers.APIPing)

	}

	router.Use(cors.New(cors.Config{
		AllowOrigins: []string{"*"},
		// AllowAllOrigins:  true,
		AllowMethods:     []string{"GET", "POST", "PATCH"},
		AllowHeaders:     []string{"Origin", "Content-Type", "Authorization", "Access-Control-Allow-Origin"},
		ExposeHeaders:    []string{"Content-Length"},
		AllowCredentials: true,
		AllowOriginFunc:  func(origin string) bool { return true },
		MaxAge:           12 * time.Hour,
	}))

	// Static endpoint for different directories
	router.Static("/txt", "./web/txt")

	// Static endpoint for homepage
	router.GET("/", func(c *gin.Context) {
		c.HTML(http.StatusOK, "frontpage.html", nil)
	})

	// Static endpoint for robots.txt
	router.GET("/robots.txt", func(c *gin.Context) {
		TXTfile, err := os.ReadFile("./web/txt/robots.txt")
		if err != nil {
			logger.Log.Info("Reading manifest threw error trying to open the file. Error: " + err.Error())
		}
		c.Data(http.StatusOK, "text/plain", TXTfile)
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

func processLibraries(libraries []string, lidarrClient *modules.LidarrClient, plexClient *modules.PlexClient, configFile models.ConfigStruct) {
	if !libraryScanRunning.CompareAndSwap(false, true) {
		logger.Log.Warn("library scan skipped: previous run still in progress")
		return
	}
	defer libraryScanRunning.Store(false)

	jobMu.Lock()
	defer jobMu.Unlock()

	logger.Log.Info("library process task starting...")
	startTime := time.Now()
	count := 0
	allUnchangedFiles := 0
	allTagsWritten := 0
	allErrorFiles := []string{}
	allAlbumsWhoNeedMetadataRefresh := map[string]string{}

	for _, library := range libraries {
		albumsWhoNeedMetadataRefresh := allAlbumsWhoNeedMetadataRefresh
		logger.Log.Info("processing library: " + library)
		libraryCount, unchangedFiles, tagsWritten, errorFiles, albumsWhoNeedMetadataRefresh, err := modules.ScanFolderRecursive(library, lidarrClient, plexClient, albumsWhoNeedMetadataRefresh, configFile)
		if err != nil {
			logger.Log.Error("failed to process library '" + library + "'. error: " + err.Error())
		} else {
			count += libraryCount
			allUnchangedFiles += unchangedFiles
			allTagsWritten += tagsWritten
			allErrorFiles = append(allErrorFiles, errorFiles...)
			allAlbumsWhoNeedMetadataRefresh = albumsWhoNeedMetadataRefresh
			logger.Log.Info("processed library: " + library)
		}
	}

	for albumName, albumKey := range allAlbumsWhoNeedMetadataRefresh {
		if err := plexClient.RefreshAlbum(albumKey); err != nil {
			logger.Log.Error("failed to inform Plex to refresh album. error: " + err.Error())
		}
		logger.Log.Info("triggered Plex refresh for album: " + albumName)
	}

	endTime := time.Now()
	durationTime := endTime.Sub(startTime)
	filesChanged := count - allUnchangedFiles

	logger.Log.Info("library process task finished. " + strconv.Itoa(count) + " files processed. " + strconv.Itoa(len(allErrorFiles)) + " files not processed because of errors. " + strconv.Itoa(filesChanged) + " files changed. " + strconv.Itoa(allTagsWritten) + " tags written")

	if len(allErrorFiles) > 0 {
		logString := "files that failed to be processed: "
		for _, filePath := range allErrorFiles {
			logString += "\n" + filePath
		}
		logger.Log.Warn(logString)
	}

	logger.Log.Info("process took: " + durationTime.String())
}
