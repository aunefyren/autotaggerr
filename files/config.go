package files

import (
	"bytes"
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

// SetConfigPaths points the package at a different config directory and returns a
// function that restores the previous one. It exists as a seam for tests in other
// packages — anything that saves config (the settings API) has to be exercised
// against a temp directory rather than the real ./config. Production never calls it.
func SetConfigPaths(dir string) (restore func()) {
	previousDir, previousFile := configDirectoryPath, configFilePath
	configDirectoryPath = dir
	configFilePath = filepath.Join(dir, "config.json")
	return func() {
		configDirectoryPath, configFilePath = previousDir, previousFile
	}
}

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

	if ConfigFile.SMTPTLS == "" {
		// Auto reproduces what the mailer did before the setting existed: implicit TLS
		// on 465, opportunistic STARTTLS elsewhere.
		ConfigFile.SMTPTLS = models.SMTPTLSAuto
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

	if ConfigFile.AutotaggerrArtworkCronSchedule == "" {
		// set new value — 05:00, after the metadata refresh has had two hours to find
		// albums added upstream. It is only a nicety: row creation warms its own
		// artwork, so this pass is a backstop for expiry rather than the thing that
		// catches up with the mirror, and the hour is not load-bearing.
		ConfigFile.AutotaggerrArtworkCronSchedule = "0 0 5 * * *"
		anythingChanged = true
	}

	if ConfigFile.AutotaggerrHealthCronSchedule == "" {
		// set new value — every five minutes; cheap, and only records an event when a
		// connection's health actually changes
		ConfigFile.AutotaggerrHealthCronSchedule = "0 */5 * * * *"
		anythingChanged = true
	}

	if ConfigFile.AutotaggerrProcessConcurrency < 1 {
		// set new value (number of files processed in parallel per scan)
		ConfigFile.AutotaggerrProcessConcurrency = 4
		anythingChanged = true
	}

	if ConfigFile.AutotaggerrEventRetention < 1 {
		// set new value (how many Activity runs are kept)
		ConfigFile.AutotaggerrEventRetention = models.DefaultEventRetention
		anythingChanged = true
	}

	if ConfigFile.AutotaggerrEventDetailRetention < 1 {
		// set new value (per-file detail rows kept per Activity event)
		ConfigFile.AutotaggerrEventDetailRetention = models.DefaultEventDetailRetention
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
	ConfigFile.SMTPTLS = models.SMTPTLSAuto
	ConfigFile.AutotaggerrVersion = autotaggerrVersionParameter
	ConfigFile.AutotaggerrProcessCronSchedule = "0 0 18 * * 7"
	ConfigFile.AutotaggerrProcessConcurrency = 4
	ConfigFile.AutotaggerrEventRetention = models.DefaultEventRetention
	ConfigFile.AutotaggerrEventDetailRetention = models.DefaultEventDetailRetention

	ConfigFile.AutotaggerrMirrorDisabled = false
	ConfigFile.AutotaggerrMirrorCronSchedule = "0 0 3 * * *"

	ConfigFile.AutotaggerrArtworkDisabled = false
	ConfigFile.AutotaggerrArtworkCronSchedule = "0 0 5 * * *"

	ConfigFile.AutotaggerrHealthCronSchedule = "0 */5 * * * *"

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

// Saves the given config struct as config.json, with the keys in alphabetical order.
//
// Encoding the struct directly would write the keys in *declaration* order, so the
// file's shape would follow how the Go struct happens to be grouped — and a field
// moved for readability would reshuffle everyone's config.json on the next save.
// Alphabetical is the one order a reader can rely on when hunting for a key by name,
// so the struct is round-tripped through a map, which encoding/json always writes
// sorted. UseNumber keeps whole numbers from being re-encoded through float64.
//
// Keys the struct no longer declares are dropped by the same round trip: decoding
// ignores what it does not know, so a config.json written by an older version is
// cleaned of retired keys the first time this runs.
func SaveConfig() error {
	err := os.MkdirAll(configDirectoryPath, os.ModePerm)
	if err != nil {
		fmt.Println("failed to create directory for config. error: " + err.Error())
		return errors.New("failed to create directory for config")
	}

	encoded, err := json.Marshal(ConfigFile)
	if err != nil {
		return err
	}
	sorter := json.NewDecoder(bytes.NewReader(encoded))
	sorter.UseNumber()
	var sorted map[string]any
	if err := sorter.Decode(&sorted); err != nil {
		return err
	}

	file, err := os.OpenFile(configFilePath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0644)
	if err != nil {
		return err
	}
	defer file.Close()

	encoder := json.NewEncoder(file)
	encoder.SetIndent("", "\t")
	encoder.SetEscapeHTML(false) // disable &/< > escaping

	return encoder.Encode(sorted)
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
