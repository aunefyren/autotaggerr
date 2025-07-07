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

	"strconv"
	"time"

	_ "time/tzdata"

	"codnect.io/chrono"
	"github.com/aunefyren/autotaggerr/files"
	"github.com/aunefyren/autotaggerr/logger"
	"github.com/aunefyren/autotaggerr/models"
	"github.com/aunefyren/autotaggerr/routers"
	"github.com/aunefyren/autotaggerr/utilities"
	"github.com/gin-contrib/cors"
	"github.com/gin-gonic/gin"
)

func main() {
	utilities.PrintASCII()

	// Create files directory
	newPath := filepath.Join(".", "files")
	err := os.MkdirAll(newPath, os.ModePerm)
	if err != nil {
		fmt.Println("Failed to create 'files' directory. Error: " + err.Error())
		os.Exit(1)
	}
	fmt.Println("Directory 'files' valid.")

	// Load config file
	configFile, err := files.GetConfig()
	if err != nil {
		fmt.Println("Failed to load configuration file. Error: " + err.Error())
		os.Exit(1)
	}
	fmt.Println("Configuration file loaded.")

	// Create and define file for logging
	logger.InitLogger(configFile)

	// Set GIN mode
	if configFile.TreninghetenEnvironment != "test" {
		gin.SetMode(gin.ReleaseMode)
	}

	// Change the config to respect flags
	configFile, err = parseFlags(configFile)
	if err != nil {
		logger.Log.Fatal("Failed to parse input flags. Error: " + err.Error())
		os.Exit(1)
	}
	logger.Log.Info("Flags parsed.")

	// Set time zone from config if it is not empty
	if configFile.Timezone != "" {
		loc, err := time.LoadLocation(configFile.Timezone)
		if err != nil {
			logger.Log.Info("Failed to set time zone from config. Error: " + err.Error())
			logger.Log.Info("Removing value...")

			configFile.Timezone = ""
			err = files.SaveConfig(configFile)
			if err != nil {
				logger.Log.Fatal("Failed to set new time zone in the config. Error: " + err.Error())
				os.Exit(1)
			}

		} else {
			time.Local = loc
		}
	}
	logger.Log.Info("Timezone set.")

	// Create task scheduler for sunday reminders
	taskScheduler := chrono.NewDefaultTaskScheduler()

	_, err = taskScheduler.ScheduleWithCron(func(ctx context.Context) {
		logger.Log.Info("Sunday reminder task executing.")
	}, "0 0 18 * * 7")
	if err != nil {
		logger.Log.Info("Sunday reminder task was not scheduled successfully.")
	}

	flacPath := "C:\\Users\\oyste\\Downloads\\Petey Pablo - Still Writing in My Diary 2nd Entry - 04 Freek-A-Leek (CD FLAC 16bit) - FLAC.flac"
	controllers.extractMusicBrainzReleaseID(flacPath)

	// Initialize Router
	router := initRouter()

	logger.Log.Info("Router initialized.")

	log.Fatal(router.Run(":" + strconv.Itoa(configFile.TreninghetenPort)))
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

func parseFlags(configFile models.ConfigStruct) (models.ConfigStruct, error) {
	// Define flag variables with the configuration file as default values
	var port = flag.Int("port", configFile.TreninghetenPort, "The port Treningheten is listening on.")
	var externalURL = flag.String("externalurl", configFile.TreninghetenExternalURL, "The URL others would use to access Treningheten.")
	var timezone = flag.String("timezone", configFile.Timezone, "The timezone Treningheten is running in.")

	// SMTP flags
	var smtpDisabled = flag.String("disablesmtp", "false", "Disables user verification using e-mail.")
	var smtpHost = flag.String("smtphost", configFile.SMTPHost, "The SMTP server which sends e-mail.")
	var smtpPort = flag.Int("smtpport", configFile.SMTPPort, "The SMTP server port.")
	var smtpUsername = flag.String("smtpusername", configFile.SMTPUsername, "The username used to verify against the SMTP server.")
	var smtpPassword = flag.String("smtppassword", configFile.SMTPPassword, "The password used to verify against the SMTP server.")
	var smtpFrom = flag.String("smtpfrom", configFile.SMTPFrom, "The sender address when sending e-mail from Treningheten.")

	// Parse the flags from input
	flag.Parse()

	// Respect the flag if config is empty
	if port != nil {
		configFile.TreninghetenPort = *port
	}

	// Respect the flag if config is empty
	if externalURL == nil {
		configFile.TreninghetenExternalURL = *externalURL
	}

	// Respect the flag if config is empty
	if timezone == nil {
		configFile.Timezone = *timezone
	}

	// Respect the flag if string is true
	if smtpDisabled != nil && strings.ToLower(*smtpDisabled) == "true" {
		configFile.SMTPEnabled = false
	} else {
		configFile.SMTPEnabled = true
	}

	// Respect the flag if config is empty
	if smtpHost != nil {
		configFile.SMTPHost = *smtpHost
	}

	// Respect the flag if config is empty
	if smtpPort != nil {
		configFile.SMTPPort = *smtpPort
	}

	// Respect the flag if config is empty
	if smtpUsername != nil {
		configFile.SMTPUsername = *smtpUsername
	}

	// Respect the flag if config is empty
	if smtpPassword != nil {
		configFile.SMTPPassword = *smtpPassword
	}

	// Respect the flag if config is empty
	if smtpFrom != nil {
		configFile.SMTPFrom = *smtpFrom
	}

	// Failsafe, if port is 0, set to default 8080
	if configFile.TreninghetenPort == 0 {
		configFile.TreninghetenPort = 8080
	}

	// Save the new config
	err := files.SaveConfig(configFile)
	if err != nil {
		return models.ConfigStruct{}, err
	}

	return configFile, nil
}
