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
	// Extractor settings — paths used by the in-app pak extractor (the
	// /extractor page). The extractor itself is in-process Go code, but it
	// shells out to two third-party tools (UAssetGUI.exe, repak.exe) which
	// are auto-detected next to the hud-go binary or on PATH.
	ExtractorTswPath    string `json:"extractorTswPath"`    // TSW6 install dir (required)
	ExtractorOutputDir  string `json:"extractorOutputDir"`  // where per-route zips land (required)
	ExtractorTempDir    string `json:"extractorTempDir"`    // temp dir for tile unpacks; empty = system temp
	ExtractorAutoImport bool   `json:"extractorAutoImport"` // after each route extracts, wipe existing DB row + import the new zip
	// ExtractorBuildTimetableMaps controls whether the importer pre-builds
	// the per-timetable filtered map-features blob during import. The
	// per-timetable filter runs O(features × line_verts × path_verts) so on
	// big DLCs (GWE, Boston Sprinter) it dominates the import time —
	// 30+ sec per timetable × thousands of timetables = hours. Default off
	// so imports stay fast; the user kicks off building lazily from
	// /routes/{id}/edit, /timetables/{id}, or HUD load.
	ExtractorBuildTimetableMaps bool `json:"extractorBuildTimetableMaps"`
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

// ResourcesDir returns the runtime resources directory next to the exe
// (AppDir()/resources). All non-source runtime data lives under here:
// configuration.json, the SQLite DB, user-uploaded images, recording
// captures, the bundled tools (repak.exe, UAssetGUI.exe), and tesseract.
// Keeping these under one folder means the user's hud-go directory only
// shows tsw-hud-new.exe + resources/ at the top level — no ambiguity
// about what to double-click.
func ResourcesDir() string {
	return filepath.Join(AppDir(), "resources")
}

// DBDir returns the directory where SQLite databases live
// (resources/db). The main app DB plus any backup files are kept here so
// they don't litter the root.
func DBDir() string {
	return filepath.Join(ResourcesDir(), "db")
}

// Load reads configuration.json from resources/.
func Load() (*Config, error) {
	cfgPath = filepath.Join(ResourcesDir(), "configuration.json")
	// Best-effort: ensure resources/ exists so the first-run Save below
	// doesn't fail with "directory does not exist".
	_ = os.MkdirAll(ResourcesDir(), 0o755)

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
