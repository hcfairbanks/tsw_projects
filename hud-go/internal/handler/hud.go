package handler

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	"hud-go/internal/util"
)

// HUDHandler manages route data for the HUD display.
// It holds the currently loaded route and timetable state in memory,
// matching the behavior of the Node.js hudController and telemetryController.
type HUDHandler struct {
	db             *sql.DB
	mu             sync.RWMutex
	currentRoute   map[string]any
	timetableIndex int
	routeFilePath  string
	// tt is held so UploadRoute can kick off a background build of the
	// loaded timetable's map-features blob if it doesn't already exist.
	// Optional — may be nil in test setups; callers must nil-check.
	tt *TimetableHandler
}

// appDir returns the directory of the running executable (or working dir as fallback).
func appDir() string {
	exe, err := os.Executable()
	if err != nil {
		wd, _ := os.Getwd()
		return wd
	}
	return filepath.Dir(exe)
}

// GetCurrentRoute returns the currently loaded route data as JSON.
// GET /route-data
func (h *HUDHandler) GetCurrentRoute(w http.ResponseWriter, r *http.Request) {
	h.mu.RLock()
	defer h.mu.RUnlock()

	if h.currentRoute == nil {
		util.Success(w, map[string]any{})
		return
	}

	// Include timetable station names and current file, matching Node.js getRouteData()
	result := make(map[string]any, len(h.currentRoute)+2)
	for k, v := range h.currentRoute {
		result[k] = v
	}

	if timetable, ok := h.currentRoute["timetable"]; ok {
		if entries, ok := timetable.([]any); ok {
			stations := make([]any, 0, len(entries))
			for _, e := range entries {
				if m, ok := e.(map[string]any); ok {
					if s, ok := m["station"]; ok {
						stations = append(stations, s)
					}
				}
			}
			result["timetableStations"] = stations
		}
	}

	if h.routeFilePath != "" {
		result["currentRouteFile"] = filepath.Base(h.routeFilePath)
	} else {
		result["currentRouteFile"] = "unknown"
	}

	util.Success(w, result)
}

// Browse lists files and directories for the route file browser.
// GET /api/hud/browse?dir=...
func (h *HUDHandler) Browse(w http.ResponseWriter, r *http.Request) {
	requestedPath := r.URL.Query().Get("dir")
	base := appDir()

	var browsePath string
	if requestedPath == "" {
		browsePath = base
	} else {
		browsePath = filepath.Join(base, requestedPath)
		// Security check: must stay within app directory
		absBase, _ := filepath.Abs(base)
		absBrowse, _ := filepath.Abs(browsePath)
		if !strings.HasPrefix(absBrowse, absBase) {
			util.Error(w, http.StatusForbidden, "Access denied")
			return
		}
	}

	info, err := os.Stat(browsePath)
	if os.IsNotExist(err) {
		util.Error(w, http.StatusNotFound, "Path not found")
		return
	}
	if err != nil {
		util.Error(w, http.StatusInternalServerError, err.Error())
		return
	}
	if !info.IsDir() {
		util.Error(w, http.StatusBadRequest, "Path is not a directory")
		return
	}

	dirEntries, err := os.ReadDir(browsePath)
	if err != nil {
		util.Error(w, http.StatusInternalServerError, err.Error())
		return
	}

	type browseItem struct {
		Name        string `json:"name"`
		Path        string `json:"path"`
		IsDirectory bool   `json:"isDirectory"`
		IsRoute     bool   `json:"isRoute"`
	}

	items := make([]browseItem, 0, len(dirEntries))
	for _, de := range dirEntries {
		fullPath := filepath.Join(browsePath, de.Name())
		relPath, _ := filepath.Rel(base, fullPath)
		// Normalize to forward slashes for the frontend
		relPath = strings.ReplaceAll(relPath, "\\", "/")

		isDir := de.IsDir()
		isRoute := !isDir && strings.HasSuffix(de.Name(), ".json") && strings.HasPrefix(de.Name(), "route_")

		items = append(items, browseItem{
			Name:        de.Name(),
			Path:        relPath,
			IsDirectory: isDir,
			IsRoute:     isRoute,
		})
	}

	// Sort: directories first, then alphabetical
	sort.Slice(items, func(i, j int) bool {
		if items[i].IsDirectory != items[j].IsDirectory {
			return items[i].IsDirectory
		}
		return strings.ToLower(items[i].Name) < strings.ToLower(items[j].Name)
	})

	currentPath := requestedPath
	if currentPath == "" {
		currentPath = "."
	} else {
		currentPath = strings.ReplaceAll(currentPath, "\\", "/")
	}

	var parentPath *string
	if requestedPath != "" {
		p := filepath.Dir(requestedPath)
		p = strings.ReplaceAll(p, "\\", "/")
		if p == "." {
			parentPath = nil
		} else {
			parentPath = &p
		}
	}

	util.Success(w, map[string]any{
		"currentPath": currentPath,
		"parentPath":  parentPath,
		"items":       items,
	})
}

// LoadRoute reads a JSON route file and stores it as the current route data.
// GET /api/hud/load-route?file=...
func (h *HUDHandler) LoadRoute(w http.ResponseWriter, r *http.Request) {
	filePath := r.URL.Query().Get("file")
	if filePath == "" {
		util.Error(w, http.StatusBadRequest, "Missing path parameter")
		return
	}

	base := appDir()
	fullPath := filepath.Join(base, filePath)

	// Security check
	absBase, _ := filepath.Abs(base)
	absFull, _ := filepath.Abs(fullPath)
	if !strings.HasPrefix(absFull, absBase) {
		util.Error(w, http.StatusForbidden, "Access denied")
		return
	}

	if _, err := os.Stat(fullPath); os.IsNotExist(err) {
		util.Error(w, http.StatusNotFound, "Route file not found")
		return
	}

	data, err := os.ReadFile(fullPath)
	if err != nil {
		util.Error(w, http.StatusInternalServerError, err.Error())
		return
	}

	var routeData map[string]any
	if err := json.Unmarshal(data, &routeData); err != nil {
		util.Error(w, http.StatusInternalServerError, fmt.Sprintf("Invalid JSON: %s", err.Error()))
		return
	}

	h.mu.Lock()
	h.currentRoute = routeData
	h.routeFilePath = fullPath
	h.timetableIndex = -1
	h.mu.Unlock()

	util.Success(w, map[string]any{
		"success":      true,
		"message":      fmt.Sprintf("Loaded %s", filepath.Base(fullPath)),
		"routeName":    routeData["routeName"],
		"totalPoints":  routeData["totalPoints"],
		"totalMarkers": routeData["totalMarkers"],
	})
}

// UploadRoute accepts route data from the client and stores it in memory.
// POST /api/upload-route
func (h *HUDHandler) UploadRoute(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Filename  string         `json:"filename"`
		RouteData map[string]any `json:"routeData"`
	}

	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		util.Error(w, http.StatusBadRequest, fmt.Sprintf("Invalid request body: %s", err.Error()))
		return
	}

	if body.RouteData == nil {
		util.Error(w, http.StatusBadRequest, "Invalid route data")
		return
	}
	if _, ok := body.RouteData["coordinates"]; !ok {
		util.Error(w, http.StatusBadRequest, "Invalid route data")
		return
	}
	if _, ok := body.RouteData["routeName"]; !ok {
		util.Error(w, http.StatusBadRequest, "Invalid route data")
		return
	}

	h.mu.Lock()
	h.currentRoute = body.RouteData
	h.routeFilePath = body.Filename
	h.timetableIndex = -1
	h.mu.Unlock()

	// If this route blob came from a timetable AND that timetable doesn't
	// have its map-features blob yet, kick off the build in the
	// background. The HUD will pick up the features whenever its periodic
	// /api/timetables/{id}/map-features fetch starts succeeding. Doing it
	// here means players never have to remember to click "Generate Map" —
	// the act of selecting a service in the HUD primes the data they'll
	// want a few minutes later.
	if h.tt != nil {
		if ttIDRaw, ok := body.RouteData["timetableId"]; ok {
			ttID := 0
			switch v := ttIDRaw.(type) {
			case float64:
				ttID = int(v)
			case int:
				ttID = v
			}
			if ttID > 0 {
				go h.tt.ensureMapFeaturesAsync(ttID)
			}
		}
	}

	util.Success(w, map[string]any{
		"success":      true,
		"message":      fmt.Sprintf("Loaded %s", body.Filename),
		"routeName":    body.RouteData["routeName"],
		"totalPoints":  body.RouteData["totalPoints"],
		"totalMarkers": body.RouteData["totalMarkers"],
	})
}

// ClearRoute clears the currently loaded route data.
// POST /api/clear-route
func (h *HUDHandler) ClearRoute(w http.ResponseWriter, r *http.Request) {
	h.mu.Lock()
	h.currentRoute = nil
	h.routeFilePath = ""
	h.timetableIndex = -1
	h.mu.Unlock()

	util.Success(w, map[string]any{
		"success": true,
		"message": "Route cleared",
	})
}

// GetTimetableItems returns the timetable entries from the currently loaded route data.
// GET /api/timetable-items
func (h *HUDHandler) GetTimetableItems(w http.ResponseWriter, r *http.Request) {
	h.mu.RLock()
	defer h.mu.RUnlock()

	if h.currentRoute == nil {
		util.Success(w, map[string]any{
			"items":        []any{},
			"currentIndex": h.timetableIndex,
		})
		return
	}

	timetable, _ := h.currentRoute["timetable"]
	if timetable == nil {
		timetable = []any{}
	}

	util.Success(w, map[string]any{
		"items":        timetable,
		"currentIndex": h.timetableIndex,
	})
}

// SetTimetableIndex sets which timetable entry is currently active.
// POST /api/set-timetable-index
// Body: {"index": N}
func (h *HUDHandler) SetTimetableIndex(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Index int `json:"index"`
	}

	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		util.Error(w, http.StatusBadRequest, fmt.Sprintf("Invalid request body: %s", err.Error()))
		return
	}

	h.mu.Lock()
	defer h.mu.Unlock()

	// Validate index range: allow -1 (not started) through len(timetable)-1
	maxIndex := 0
	if h.currentRoute != nil {
		if timetable, ok := h.currentRoute["timetable"]; ok {
			if entries, ok := timetable.([]any); ok {
				maxIndex = len(entries)
			}
		}
	}

	if body.Index < -1 || body.Index >= maxIndex {
		util.Error(w, http.StatusBadRequest, "Index out of range")
		return
	}

	h.timetableIndex = body.Index

	util.Success(w, map[string]any{
		"success": true,
	})
}

// UpdateTimetableCoordinates updates coordinates on a specific timetable entry.
// POST /api/update-timetable-coordinates
// Body: {"entryId": "...", "latitude": N, "longitude": N, "x": N, "y": N}
func (h *HUDHandler) UpdateTimetableCoordinates(w http.ResponseWriter, r *http.Request) {
	var body struct {
		EntryID   any     `json:"entryId"`
		Index     *int    `json:"index"`
		Latitude  float64 `json:"latitude"`
		Longitude float64 `json:"longitude"`
		X         *int    `json:"x"`
		Y         *int    `json:"y"`
	}

	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		util.Error(w, http.StatusBadRequest, fmt.Sprintf("Invalid request body: %s", err.Error()))
		return
	}

	h.mu.Lock()
	defer h.mu.Unlock()

	if h.currentRoute == nil {
		util.Error(w, http.StatusBadRequest, "No route loaded")
		return
	}

	timetable, ok := h.currentRoute["timetable"]
	if !ok {
		util.Error(w, http.StatusBadRequest, "No timetable in route data")
		return
	}

	entries, ok := timetable.([]any)
	if !ok {
		util.Error(w, http.StatusBadRequest, "Invalid timetable format")
		return
	}

	applyCoords := func(m map[string]any) {
		m["latitude"] = body.Latitude
		m["longitude"] = body.Longitude
		if body.X != nil {
			m["x"] = *body.X
		}
		if body.Y != nil {
			m["y"] = *body.Y
		}
	}

	// Find the entry by entryId or index
	found := false
	if body.EntryID != nil {
		for i, e := range entries {
			if m, ok := e.(map[string]any); ok {
				if m["id"] == body.EntryID {
					applyCoords(m)
					entries[i] = m
					found = true
					break
				}
			}
		}
	} else if body.Index != nil {
		idx := *body.Index
		if idx >= 0 && idx < len(entries) {
			if m, ok := entries[idx].(map[string]any); ok {
				applyCoords(m)
				entries[idx] = m
				found = true
			}
		}
	}

	if !found {
		util.Error(w, http.StatusNotFound, "Timetable entry not found")
		return
	}

	h.currentRoute["timetable"] = entries

	// Save to database
	if body.EntryID != nil {
		var entryID int
		switch v := body.EntryID.(type) {
		case float64:
			entryID = int(v)
		case int:
			entryID = v
		}
		if entryID > 0 {
			var tileXArg, tileYArg any
			if body.X != nil {
				tileXArg = *body.X
			}
			if body.Y != nil {
				tileYArg = *body.Y
			}
			_, err := h.db.Exec(
				"UPDATE timetable_entries SET latitude = ?, longitude = ?, tile_x = ?, tile_y = ?, coord_source = 'manual' WHERE id = ?",
				body.Latitude, body.Longitude, tileXArg, tileYArg, entryID,
			)
			if err != nil {
				log.Printf("[SAVE LOC] DB error updating entry %d: %v", entryID, err)
			} else {
				log.Printf("[SAVE LOC] Updated entry %d coordinates: lat=%f, lng=%f x=%v y=%v", entryID, body.Latitude, body.Longitude, tileXArg, tileYArg)
			}
		}
	}

	util.Success(w, map[string]any{
		"success": true,
	})
}
