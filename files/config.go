package files

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/aunefyren/autotaggerr/models"
	"github.com/sirupsen/logrus"
)

var autotaggerrVersionParameter = "{{RELEASE_TAG}}"
var configDirectoryPath, _ = filepath.Abs("./config/")
var configFilePath = filepath.Join(configDirectoryPath, "config.json")
var ConfigFile = models.ConfigStruct{}

func LoadConfig() (err error) {
	ConfigFile = models.ConfigStruct{}

	// Create config.json if it doesn't exist
	if _, err := os.Stat(configFilePath); errors.Is(err, os.ErrNotExist) {
		fmt.Println("config file does not exist. creating...")

		err := CreateConfigFile()
		if err != nil {
			return err
		}
	}

	file, err := os.Open(configFilePath)
	if err != nil {
		fmt.Println("get config file threw error trying to open the file")
		return err
	}
	defer file.Close()
	decoder := json.NewDecoder(file)

	err = decoder.Decode(&ConfigFile)
	if err != nil {
		fmt.Println("get config file threw error trying to parse the file")
		return err
	}

	anythingChanged := false

	if ConfigFile.PrivateKey == "" {
		// Set new value
		newKey, err := GenerateSecureKey(64)
		if err != nil {
			return errors.New("failed to generate secure key. error: " + err.Error())
		}
		ConfigFile.PrivateKey = newKey
		anythingChanged = true
		fmt.Println("new private key set")
	}

	if ConfigFile.AutotaggerrName == "" {
		// Set new value
		ConfigFile.AutotaggerrName = "Autotaggerr"
		anythingChanged = true
	}

	if ConfigFile.AutotaggerrEnvironment == "" {
		// Set new value
		ConfigFile.AutotaggerrEnvironment = "prod"
		anythingChanged = true
	}

	if ConfigFile.Timezone == "" {
		// Set new value
		ConfigFile.Timezone = "Europe/Paris"
		anythingChanged = true
	}

	if ConfigFile.Database.Type == "" {
		// Default to the pure-Go sqlite driver (CGO-free build).
		ConfigFile.Database.Type = "sqlite"
		anythingChanged = true
	}

	if ConfigFile.Database.DSN == "" {
		// For sqlite the DSN is a file path under the config directory.
		ConfigFile.Database.DSN = "config/autotaggerr.db"
		anythingChanged = true
	}

	if ConfigFile.AutotaggerrPort == 0 {
		// Set new value
		ConfigFile.AutotaggerrPort = 8080
		anythingChanged = true
	}

	if ConfigFile.AutotaggerrProcessCronSchedule == "" {
		// set new value
		ConfigFile.AutotaggerrProcessCronSchedule = "0 0 18 * * 7"
		anythingChanged = true
	}

	if ConfigFile.AutotaggerrMirrorCronSchedule == "" {
		// set new value — nightly at 03:00, when nobody is browsing and the scan
		// (weekly, 18:00 Sunday) is not competing for the same rate-limit budget
		ConfigFile.AutotaggerrMirrorCronSchedule = "0 0 3 * * *"
		anythingChanged = true
	}

	if ConfigFile.AutotaggerrProcessConcurrency < 1 {
		// set new value (number of files processed in parallel per scan)
		ConfigFile.AutotaggerrProcessConcurrency = 4
		anythingChanged = true
	}

	if ConfigFile.AutotaggerrCustomArtistDelimiter == "" {
		// set new value
		ConfigFile.AutotaggerrCustomArtistDelimiter = " & "
		anythingChanged = true
	}

	if ConfigFile.AutotaggerrLibraries == nil {
		// Set new value
		ConfigFile.AutotaggerrLibraries = []string{}
		anythingChanged = true
	}

	if ConfigFile.AutotaggerrLogLevel == "" {
		level := logrus.InfoLevel
		ConfigFile.AutotaggerrLogLevel = level.String()
		anythingChanged = true
	} else {
		parsedLogLevel, err := logrus.ParseLevel(ConfigFile.AutotaggerrLogLevel)
		if err != nil {
			level := logrus.InfoLevel
			ConfigFile.AutotaggerrLogLevel = level.String()
			anythingChanged = true
		} else {
			logrus.SetLevel(parsedLogLevel)
		}
	}

	if anythingChanged {
		// Save new version of config json
		fmt.Println("saving new config file version")
		err = SaveConfig()
		if err != nil {
			return err
		}
	}

	ConfigFile.AutotaggerrVersion = autotaggerrVersionParameter

	// Return nil object
	return nil
}

// Creates empty config.json
func CreateConfigFile() error {
	ConfigFile = models.ConfigStruct{}

	ConfigFile.AutotaggerrPort = 8080
	ConfigFile.AutotaggerrName = "Autotaggerr"
	ConfigFile.AutotaggerrEnvironment = "prod"
	ConfigFile.Database = models.DatabaseConfig{Type: "sqlite", DSN: "config/autotaggerr.db"}
	ConfigFile.SMTPEnabled = true
	ConfigFile.AutotaggerrVersion = autotaggerrVersionParameter
	ConfigFile.AutotaggerrLibraries = []string{}
	ConfigFile.AutotaggerrProcessCronSchedule = "0 0 18 * * 7"
	ConfigFile.AutotaggerrProcessConcurrency = 4
	ConfigFile.AutotaggerrCustomArtistDelimiter = " & "
	ConfigFile.AutotaggerrUseCurrentArtistName = true
	ConfigFile.AutotaggerrUseCustomArtistDelimiter = true
	ConfigFile.AutotaggerrCustomArtistDelimiterCommas = true
	ConfigFile.AutotaggerrIgnoreRedundantContributingArtists = true
	ConfigFile.AutotaggerrRemoveValues = false

	// The MusicBrainz mirror runs nightly by default but not on startup: a first
	// pass over a large collection is hours of rate-limited fetching, and tying that
	// to every restart would make a restart something to avoid.
	ConfigFile.AutotaggerrMirrorDisabled = false
	ConfigFile.AutotaggerrMirrorCronSchedule = "0 0 3 * * *"
	ConfigFile.AutotaggerrMirrorOnStartUp = false

	// MusicBrainz migrations apply themselves by default; these hold a category back
	// for manual approval instead. Written explicitly so the keys are discoverable in
	// a fresh config.json rather than only in the README.
	ConfigFile.AutotaggerrMigrationReviewReleases = false
	ConfigFile.AutotaggerrMigrationReviewArtists = false
	ConfigFile.AutotaggerrMigrationReviewPinned = false
	ConfigFile.AutotaggerrMigrationReviewDeletions = false

	level := logrus.InfoLevel
	ConfigFile.AutotaggerrLogLevel = level.String()

	privateKey, err := GenerateSecureKey(64)
	if err != nil {
		fmt.Println("failed to generate private key. error: " + err.Error())
		return errors.New("failed to generate private key")
	}
	ConfigFile.PrivateKey = privateKey

	err = SaveConfig()
	if err != nil {
		fmt.Println("create config file threw error trying to save the file. error: " + err.Error())
		return errors.New("create config file threw error trying to save the file")
	}

	return nil
}

// Saves the given config struct as config.json
func SaveConfig() error {
	err := os.MkdirAll(configDirectoryPath, os.ModePerm)
	if err != nil {
		fmt.Println("failed to create directory for config. error: " + err.Error())
		return errors.New("failed to create directory for config")
	}

	file, err := os.OpenFile(configFilePath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0644)
	if err != nil {
		return err
	}
	defer file.Close()

	encoder := json.NewEncoder(file)
	encoder.SetIndent("", "\t")
	encoder.SetEscapeHTML(false) // disable &/< > escaping

	return encoder.Encode(ConfigFile)
}

func GetPrivateKey(epoch int) []byte {
	if epoch > 5 {
		fmt.Println("failed to load private key. exiting...")
		os.Exit(1)
	}

	secretKey, err := base64.StdEncoding.DecodeString(ConfigFile.PrivateKey)
	if err != nil {
		ResetSecureKey()
		return GetPrivateKey(epoch + 1)
	}

	return secretKey
}

// GenerateSecureKey creates a cryptographically secure random key of the given length (in bytes).
func GenerateSecureKey(length int) (string, error) {
	key := make([]byte, length)
	_, err := rand.Read(key)
	if err != nil {
		return "", err
	}
	// Encode to Base64 to make it easy to store
	return base64.StdEncoding.EncodeToString(key), nil
}

func ResetSecureKey() {
	privateKey, err := GenerateSecureKey(64)
	if err != nil {
		fmt.Println("failed to generate new secret key. exiting...")
		os.Exit(1)
	}
	ConfigFile.PrivateKey = privateKey
	SaveConfig()
	if err != nil {
		fmt.Println("failed to save new config. exiting...")
		os.Exit(1)
	}
	fmt.Println("new private key set")
}
