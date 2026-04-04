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

type RouteProcessingHandler struct {
	db *sql.DB
}

func (h *RouteProcessingHandler) ProcessLatest(w http.ResponseWriter, r *http.Request) {
	util.Error(w, http.StatusNotImplemented, "Route processing not yet available in Go version")
}

func (h *RouteProcessingHandler) List(w http.ResponseWriter, r *http.Request) {
	dir := filepath.Join(config.AppDir(), "processed_routes")
	entries, err := os.ReadDir(dir)
	if err != nil {
		util.JSON(w, 200, map[string]any{"files": []any{}})
		return
	}

	files := make([]map[string]any, 0)
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		info, _ := e.Info()
		files = append(files, map[string]any{
			"name":     e.Name(),
			"size":     info.Size(),
			"modified": info.ModTime(),
		})
	}
	util.JSON(w, 200, map[string]any{"files": files})
}

func (h *RouteProcessingHandler) GetFile(w http.ResponseWriter, r *http.Request) {
	filename := r.URL.Query().Get("filename")
	if filename == "" {
		util.Error(w, 400, "filename is required")
		return
	}
	// Sanitize
	filename = filepath.Base(filename)
	filePath := filepath.Join(config.AppDir(), "processed_routes", filename)

	data, err := os.ReadFile(filePath)
	if err != nil {
		util.Error(w, 404, "file not found")
		return
	}

	var result any
	if err := json.Unmarshal(data, &result); err != nil {
		util.Error(w, 500, "invalid JSON in file")
		return
	}
	util.JSON(w, 200, result)
}
