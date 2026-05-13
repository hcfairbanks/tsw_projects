package handler

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"runtime"

	"hud-go/internal/config"
	"hud-go/internal/util"
)

type ConfigHandler struct {
	db *sql.DB
}

func (h *ConfigHandler) Get(w http.ResponseWriter, r *http.Request) {
	util.JSON(w, 200, config.Get())
}

func (h *ConfigHandler) Update(w http.ResponseWriter, r *http.Request) {
	// Decode partial update into a map to know which fields were sent
	var patch map[string]json.RawMessage
	if err := json.NewDecoder(r.Body).Decode(&patch); err != nil {
		util.Error(w, 400, "invalid JSON: "+err.Error())
		return
	}

	// Merge patch into current config: re-serialize current, overlay patch, decode back
	cfg := config.Get()
	currentBytes, _ := json.Marshal(cfg)
	var merged map[string]json.RawMessage
	json.Unmarshal(currentBytes, &merged)
	for k, v := range patch {
		merged[k] = v
	}
	mergedBytes, _ := json.Marshal(merged)
	var updated config.Config
	json.Unmarshal(mergedBytes, &updated)

	if err := config.Update(&updated); err != nil {
		util.Error(w, 500, err.Error())
		return
	}
	util.JSON(w, 200, map[string]any{
		"success": true,
		"config":  config.Get(),
	})
}

func (h *ConfigHandler) GetDefaultPaths(w http.ResponseWriter, r *http.Request) {
	home, _ := os.UserHomeDir()
	var tsw5, tsw6 string
	if runtime.GOOS == "windows" {
		docs := filepath.Join(home, "Documents", "My Games")
		tsw5 = filepath.Join(docs, "TrainSimWorld5", "Saved", "Config", "CommAPIKey.txt")
		tsw6 = filepath.Join(docs, "TrainSimWorld6", "Saved", "Config", "CommAPIKey.txt")
	}
	util.JSON(w, 200, map[string]string{
		"tsw5": tsw5,
		"tsw6": tsw6,
	})
}

func (h *ConfigHandler) GetCurrentKey(w http.ResponseWriter, r *http.Request) {
	cfg := config.Get()
	key := cfg.ApiKey
	source := "config"
	if key == "" {
		source = "file"
		keyPath := getKeyPath(cfg)
		if data, err := os.ReadFile(keyPath); err == nil {
			key = string(data)
		}
	}
	util.JSON(w, 200, map[string]any{
		"hasKey": key != "",
		"source": source,
	})
}

func (h *ConfigHandler) GetServerURLs(w http.ResponseWriter, r *http.Request) {
	ip := util.GetInternalIP()
	util.JSON(w, 200, map[string]string{
		"local":   "http://127.0.0.1:3000",
		"network": fmt.Sprintf("http://%s:3000", ip),
	})
}

func getKeyPath(cfg *config.Config) string {
	home, _ := os.UserHomeDir()
	docs := filepath.Join(home, "Documents", "My Games")
	if cfg.TswVersion == "tsw5" {
		if cfg.Tsw5KeyPath != "" {
			return cfg.Tsw5KeyPath
		}
		return filepath.Join(docs, "TrainSimWorld5", "Saved", "Config", "CommAPIKey.txt")
	}
	if cfg.Tsw6KeyPath != "" {
		return cfg.Tsw6KeyPath
	}
	return filepath.Join(docs, "TrainSimWorld6", "Saved", "Config", "CommAPIKey.txt")
}
