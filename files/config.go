package files

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/aunefyren/autotaggerr/logger"
	"github.com/aunefyren/autotaggerr/models"
	"github.com/sirupsen/logrus"
)

var autotaggerrVersionParameter = "{{RELEASE_TAG}}"
var configPath, _ = filepath.Abs("./config")
var configFile = filepath.Join(configPath, "config.json")

func GetConfig() (config models.ConfigStruct, err error) {
	config = models.ConfigStruct{}

	// Create config.json if it doesn't exist
	if _, err := os.Stat(configFile); errors.Is(err, os.ErrNotExist) {
		fmt.Println("Config file does not exist. Creating...")

		err := CreateConfigFile()
		if err != nil {
			return config, err
		}
	}

	file, err := os.Open(configFile)
	if err != nil {
		fmt.Println("Get config file threw error trying to open the file.")
		return config, err
	}
	defer file.Close()
	decoder := json.NewDecoder(file)

	err = decoder.Decode(&config)
	if err != nil {
		fmt.Println("Get config file threw error trying to parse the file.")
		return config, err
	}

	anythingChanged := false

	if config.PrivateKey == "" {
		// Set new value
		newKey, err := GenerateSecureKey(64)
		if err != nil {
			return config, errors.New("Failed to generate secure key. Error: " + err.Error())
		}
		config.PrivateKey = newKey
		anythingChanged = true
		logger.Log.Info("New private key set.")
	}

	if config.TreninghetenName == "" {
		// Set new value
		config.TreninghetenName = "Treningheten"
		anythingChanged = true
	}

	if config.TreninghetenEnvironment == "" {
		// Set new value
		config.TreninghetenEnvironment = "prod"
		anythingChanged = true
	}

	if config.Timezone == "" {
		// Set new value
		config.Timezone = "Europe/Paris"
		anythingChanged = true
	}

	if config.TreninghetenPort == 0 {
		// Set new value
		config.TreninghetenPort = 8080
		anythingChanged = true
	}

	if config.TreninghetenLogLevel == "" {
		level := logrus.InfoLevel
		config.TreninghetenLogLevel = level.String()
		anythingChanged = true
	} else {
		_, err := logrus.ParseLevel(config.TreninghetenLogLevel)
		if err != nil {
			level := logrus.InfoLevel
			config.TreninghetenLogLevel = level.String()
			anythingChanged = true
		}
	}

	if anythingChanged {
		// Save new version of config json
		err = SaveConfig(config)
		if err != nil {
			return config, err
		}
	}

	config.TreninghetenVersion = autotaggerrVersionParameter

	// Return config object
	return config, nil
}

// Creates empty config.json
func CreateConfigFile() error {
	var config models.ConfigStruct

	config.TreninghetenPort = 8080
	config.TreninghetenName = "Treningheten"
	config.TreninghetenEnvironment = "prod"
	config.SMTPEnabled = true
	config.TreninghetenVersion = autotaggerrVersionParameter

	level := logrus.InfoLevel
	config.TreninghetenLogLevel = level.String()

	privateKey, err := GenerateSecureKey(64)
	if err != nil {
		fmt.Println("Failed to generate private key. Error: " + err.Error())
		return err
	}
	config.PrivateKey = privateKey

	err = SaveConfig(config)
	if err != nil {
		fmt.Println("Create config file threw error trying to save the file.")
		return err
	}

	return nil
}

// Saves the given config struct as config.json
func SaveConfig(config models.ConfigStruct) error {

	err := os.MkdirAll(configPath, os.ModePerm)
	if err != nil {
		logger.Log.Info("Failed to create directory for config. Error: " + err.Error())
		return errors.New("Failed to create directory for config.")
	}

	file, err := json.MarshalIndent(config, "", "	")
	if err != nil {
		return err
	}

	err = os.WriteFile(configFile, file, 0644)
	if err != nil {
		return err
	}

	return nil
}

func GetPrivateKey(epoch int) []byte {
	if epoch > 5 {
		logger.Log.Info("Failed to load private key. Exiting...")
		os.Exit(1)
	}

	configFile, err := GetConfig()
	if err != nil {
		logger.Log.Info("Failed to load config for private key. Exiting...")
		os.Exit(1)
	}

	secretKey, err := base64.StdEncoding.DecodeString(configFile.PrivateKey)
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
	configFile, err := GetConfig()
	if err != nil {
		logger.Log.Info("Failed to load config for private key. Exiting...")
		os.Exit(1)
	}
	configFile.PrivateKey, err = GenerateSecureKey(64)
	if err != nil {
		logger.Log.Info("Failed to generate new secret key. Exiting...")
		os.Exit(1)
	}
	SaveConfig(configFile)
	if err != nil {
		logger.Log.Info("Failed to save new config. Exiting...")
		os.Exit(1)
	}
	logger.Log.Info("New private key set.")
}
