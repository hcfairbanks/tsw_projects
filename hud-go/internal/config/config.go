package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
)

type Config struct {
	DevelopmentMode        bool    `json:"developmentMode"`
	ApiKey                 string  `json:"apiKey"`
	Theme                  string  `json:"theme"`
	Language               string  `json:"language"`
	TswVersion             string  `json:"tswVersion"`
	Tsw5KeyPath            string  `json:"tsw5KeyPath"`
	Tsw6KeyPath            string  `json:"tsw6KeyPath"`
	DistanceUnits          string  `json:"distanceUnits"`
	TemperatureUnits       string  `json:"temperatureUnits"`
	ContributorName        string  `json:"contributorName"`
	SimplifyEpsilon        float64 `json:"simplifyEpsilon"`
	MinStopDurationSeconds int     `json:"minStopDurationSeconds"`
	GpsNoiseRadiusMeters   float64 `json:"gpsNoiseRadiusMeters"`
	MinPointsForStop       int     `json:"minPointsForStop"`
	AutoStopTimeoutSeconds int     `json:"autoStopTimeoutSeconds"`
	SaveFrequency          int     `json:"saveFrequency"`
	EnableSubscriptions    bool    `json:"enableSubscriptions"`
	ColorScheme            string  `json:"colorScheme"`
}

var (
	current *Config
	mu      sync.RWMutex
	cfgPath string
)

func defaults() Config {
	return Config{
		Theme:                  "dark",
		Language:               "en",
		TswVersion:             "tsw6",
		DistanceUnits:          "metric",
		TemperatureUnits:       "celsius",
		SimplifyEpsilon:        1,
		MinStopDurationSeconds: 30,
		GpsNoiseRadiusMeters:   10,
		MinPointsForStop:       10,
		AutoStopTimeoutSeconds: 120,
		SaveFrequency:          1,
		EnableSubscriptions:    true,
		ColorScheme:            "default",
	}
}

// AppDir returns the directory where the executable lives.
func AppDir() string {
	exe, err := os.Executable()
	if err != nil {
		return "."
	}
	return filepath.Dir(exe)
}

// Load reads configuration.json from next to the executable.
func Load() (*Config, error) {
	cfgPath = filepath.Join(AppDir(), "configuration.json")

	cfg := defaults()

	data, err := os.ReadFile(cfgPath)
	if err != nil {
		if os.IsNotExist(err) {
			current = &cfg
			return current, Save()
		}
		return nil, err
	}

	if err := json.Unmarshal(data, &cfg); err != nil {
		return nil, err
	}

	mu.Lock()
	current = &cfg
	mu.Unlock()

	return current, nil
}

// Get returns the current config (read-safe).
func Get() *Config {
	mu.RLock()
	defer mu.RUnlock()
	return current
}

// Update replaces the config and writes to disk.
func Update(cfg *Config) error {
	mu.Lock()
	current = cfg
	mu.Unlock()
	return Save()
}

// Save writes the current config to disk.
func Save() error {
	mu.RLock()
	c := current
	mu.RUnlock()

	data, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(cfgPath, data, 0644)
}
