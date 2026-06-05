package config

import (
	"encoding/json"
	"os"
	"path/filepath"
)

const configFileName = ".gatorconfig.json"

// Config represents the structure of our JSON configuration file.
// Struct tags are required so the json package knows how to map the keys.
type Config struct {
	DBUrl           string `json:"db_url"`
	CurrentUserName string `json:"current_user_name"`
}

// SetUser updates the current user name in memory and writes the whole struct back to disk.
func (cfg *Config) SetUser(username string) error {
	cfg.CurrentUserName = username
	return write(*cfg)
}

// Read reads the JSON file from the home directory and returns a Config struct.
func Read() (Config, error) {
	filePath, err := getConfigFilePath()
	if err != nil {
		return Config{}, err
	}

	// Open and read the file data
	data, err := os.ReadFile(filePath)
	if err != nil {
		return Config{}, err
	}

	// Unmarshal (decode) the JSON into a Config struct
	var cfg Config
	err = json.Unmarshal(data, &cfg)
	if err != nil {
		return Config{}, err
	}

	return cfg, nil
}

// getConfigFilePath is a helper function to safely target ~/.gatorconfig.json
func getConfigFilePath() (string, error) {
	homeDir, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(homeDir, configFileName), nil
}

// write is a helper function that serializes the Config struct and writes it to disk.
func write(cfg Config) error {
	filePath, err := getConfigFilePath()
	if err != nil {
		return err
	}

	// Marshal (encode) the struct into prettified JSON
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}

	// Write the data to the file (0644 gives read/write permissions to the owner)
	err = os.WriteFile(filePath, data, 0644)
	if err != nil {
		return err
	}

	return nil
}
