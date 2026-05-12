package handler

import (
	"database/sql"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"hud-go/internal/config"
	"hud-go/internal/util"
)

// ProcessingHandler provides endpoints for route processing.
type ProcessingHandler struct {
	db *sql.DB
}

// processedRoutesDir returns the path to the processed_routes directory.
func processedRoutesDir() string {
	return filepath.Join(config.ResourcesDir(), "processed_routes")
}

// Process is a stub - processing is not available in the Go version yet.
func (h *ProcessingHandler) Process(w http.ResponseWriter, r *http.Request) {
	util.JSON(w, http.StatusOK, map[string]any{
		"success": false,
		"error":   "Processing not available in Go version yet",
	})
}

// List returns all files in the processed_routes directory.
func (h *ProcessingHandler) List(w http.ResponseWriter, r *http.Request) {
	dir := processedRoutesDir()

	if _, err := os.Stat(dir); os.IsNotExist(err) {
		util.JSON(w, http.StatusOK, []any{})
		return
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		util.Error(w, http.StatusInternalServerError, err.Error())
		return
	}

	files := make([]map[string]any, 0)
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}

		info, err := entry.Info()
		if err != nil {
			continue
		}

		fileEntry := map[string]any{
			"filename": entry.Name(),
			"size":     info.Size(),
			"modified": info.ModTime(),
		}

		// Try to parse for metadata
		filePath := filepath.Join(dir, entry.Name())
		data, err := os.ReadFile(filePath)
		if err == nil {
			var parsed map[string]any
			if json.Unmarshal(data, &parsed) == nil {
				if rn, ok := parsed["routeName"]; ok {
					fileEntry["routeName"] = rn
				}
				if tid, ok := parsed["timetableId"]; ok {
					fileEntry["timetableId"] = tid
				}
			}
		}

		files = append(files, fileEntry)
	}

	util.JSON(w, http.StatusOK, files)
}

// GetFile reads and returns a processed route JSON file.
func (h *ProcessingHandler) GetFile(w http.ResponseWriter, r *http.Request) {
	filename := r.URL.Query().Get("filename")
	if filename == "" {
		util.Error(w, http.StatusBadRequest, "Filename is required")
		return
	}

	// Prevent directory traversal
	filename = filepath.Base(filename)
	filePath := filepath.Join(processedRoutesDir(), filename)

	data, err := os.ReadFile(filePath)
	if err != nil {
		if os.IsNotExist(err) {
			util.Error(w, http.StatusNotFound, "File not found")
		} else {
			util.Error(w, http.StatusInternalServerError, "Could not read file")
		}
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	w.Write(data)
}

// ProcessTimetable is a stub - processing is not available in the Go version yet.
func (h *ProcessingHandler) ProcessTimetable(w http.ResponseWriter, r *http.Request) {
	util.JSON(w, http.StatusOK, map[string]any{
		"success": false,
		"error":   "Processing not available in Go version yet",
	})
}
