package handler

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"hud-go/internal/config"
	"hud-go/internal/util"
)

// MapDataHandler provides endpoints for timetable map data.
type MapDataHandler struct {
	db *sql.DB
}

// GetAllTimetables returns all timetables with coordinate counts.
func (h *MapDataHandler) GetAllTimetables(w http.ResponseWriter, r *http.Request) {
	rows, err := h.db.Query(`
		SELECT t.id, t.service_name, t.route_id, r.name AS route_name,
			   COALESCE((SELECT COUNT(*) FROM timetable_coordinates tc WHERE tc.timetable_id = t.id), 0) AS coordinate_count
		FROM timetables t
		LEFT JOIN routes r ON t.route_id = r.id
		ORDER BY t.service_name
	`)
	if err != nil {
		util.Error(w, http.StatusInternalServerError, err.Error())
		return
	}
	defer rows.Close()

	items := make([]map[string]any, 0)
	for rows.Next() {
		var id int
		var serviceName string
		var routeID *int
		var routeName *string
		var coordCount int
		if err := rows.Scan(&id, &serviceName, &routeID, &routeName, &coordCount); err != nil {
			util.Error(w, http.StatusInternalServerError, err.Error())
			return
		}
		items = append(items, map[string]any{
			"id":               id,
			"service_name":     serviceName,
			"route_id":         routeID,
			"route_name":       routeName,
			"coordinate_count": coordCount,
			"hasCoordinates":   coordCount > 0,
		})
	}

	util.JSON(w, http.StatusOK, items)
}

// GetTimetablesWithData returns timetables that have coordinate data.
func (h *MapDataHandler) GetTimetablesWithData(w http.ResponseWriter, r *http.Request) {
	rows, err := h.db.Query(`
		SELECT t.id, t.service_name, t.route_id, r.name AS route_name
		FROM timetables t
		LEFT JOIN routes r ON t.route_id = r.id
		WHERE t.id IN (SELECT timetable_id FROM timetable_coordinates)
		ORDER BY t.service_name
	`)
	if err != nil {
		util.Error(w, http.StatusInternalServerError, err.Error())
		return
	}
	defer rows.Close()

	items := make([]map[string]any, 0)
	for rows.Next() {
		var id int
		var serviceName string
		var routeID *int
		var routeName *string
		if err := rows.Scan(&id, &serviceName, &routeID, &routeName); err != nil {
			util.Error(w, http.StatusInternalServerError, err.Error())
			return
		}
		items = append(items, map[string]any{
			"id":           id,
			"service_name": serviceName,
			"route_id":     routeID,
			"route_name":   routeName,
		})
	}

	util.JSON(w, http.StatusOK, items)
}

// GetTimetableData returns coordinates and markers for a timetable.
func (h *MapDataHandler) GetTimetableData(w http.ResponseWriter, r *http.Request) {
	idStr := chi.URLParam(r, "id")
	timetableID, err := strconv.Atoi(idStr)
	if err != nil {
		util.Error(w, http.StatusBadRequest, "invalid timetable ID")
		return
	}

	// Get timetable info
	var serviceName string
	var routeID *int
	err = h.db.QueryRow("SELECT service_name, route_id FROM timetables WHERE id = ?", timetableID).Scan(&serviceName, &routeID)
	if err == sql.ErrNoRows {
		util.Error(w, http.StatusNotFound, "Timetable not found")
		return
	}
	if err != nil {
		util.Error(w, http.StatusInternalServerError, err.Error())
		return
	}

	// Get coordinates
	coordinates := make([]any, 0)
	var coordJSON *string
	err = h.db.QueryRow("SELECT coordinates FROM timetable_coordinates WHERE timetable_id = ?", timetableID).Scan(&coordJSON)
	if err == nil && coordJSON != nil {
		json.Unmarshal([]byte(*coordJSON), &coordinates)
	}

	// Get markers
	markers := make([]map[string]any, 0)
	markerRows, err := h.db.Query("SELECT station_name, marker_type, latitude, longitude, platform_length FROM timetable_markers WHERE timetable_id = ?", timetableID)
	if err == nil {
		defer markerRows.Close()
		for markerRows.Next() {
			var stationName string
			var markerType *string
			var lat, lng, platLen *float64
			if err := markerRows.Scan(&stationName, &markerType, &lat, &lng, &platLen); err != nil {
				continue
			}
			markers = append(markers, map[string]any{
				"stationName":    stationName,
				"markerType":     markerType,
				"latitude":       lat,
				"longitude":      lng,
				"platformLength": platLen,
			})
		}
	}

	util.JSON(w, http.StatusOK, map[string]any{
		"timetableId": timetableID,
		"serviceName": serviceName,
		"routeId":     routeID,
		"coordinates": coordinates,
		"markers":     markers,
	})
}

// ImportFromRecording imports recording data into a timetable's DB tables.
func (h *MapDataHandler) ImportFromRecording(w http.ResponseWriter, r *http.Request) {
	var body struct {
		TimetableID   int `json:"timetableId"`
		RecordingData struct {
			Coordinates []map[string]any `json:"coordinates"`
			Markers     []map[string]any `json:"markers"`
		} `json:"recordingData"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		util.Error(w, http.StatusBadRequest, "Invalid JSON data: "+err.Error())
		return
	}

	if body.TimetableID == 0 {
		util.Error(w, http.StatusBadRequest, "timetableId is required")
		return
	}

	// Validate timetable exists
	var serviceName string
	err := h.db.QueryRow("SELECT service_name FROM timetables WHERE id = ?", body.TimetableID).Scan(&serviceName)
	if err == sql.ErrNoRows {
		util.Error(w, http.StatusNotFound, "Timetable not found")
		return
	}
	if err != nil {
		util.Error(w, http.StatusInternalServerError, err.Error())
		return
	}

	coordinateCount := 0
	markerCount := 0

	// Import coordinates
	if len(body.RecordingData.Coordinates) > 0 {
		coordJSON, err := json.Marshal(body.RecordingData.Coordinates)
		if err == nil {
			_, err = h.db.Exec(
				"INSERT OR REPLACE INTO timetable_coordinates (timetable_id, coordinates, coord_source) VALUES (?, ?, ?)",
				body.TimetableID, string(coordJSON), "automatic",
			)
			if err == nil {
				coordinateCount = len(body.RecordingData.Coordinates)
			}
		}

		// Set coordinates_contributor from config
		cfg := config.Get()
		if cfg != nil && cfg.ContributorName != "" {
			h.db.Exec("UPDATE timetables SET coordinates_contributor = ? WHERE id = ?", cfg.ContributorName, body.TimetableID)
		}
	}

	// Import markers
	if len(body.RecordingData.Markers) > 0 {
		for _, m := range body.RecordingData.Markers {
			stationName, _ := m["stationName"].(string)
			markerType, _ := m["markerType"].(string)
			lat, _ := m["latitude"].(float64)
			lng, _ := m["longitude"].(float64)
			platLen, _ := m["platformLength"].(float64)

			if stationName == "" {
				continue
			}

			_, err := h.db.Exec(
				"INSERT INTO timetable_markers (timetable_id, station_name, marker_type, latitude, longitude, platform_length) VALUES (?, ?, ?, ?, ?, ?)",
				body.TimetableID, stationName, markerType, lat, lng, platLen,
			)
			if err == nil {
				markerCount++
			}
		}
	}

	util.JSON(w, http.StatusOK, map[string]any{
		"success":         true,
		"timetableId":     body.TimetableID,
		"serviceName":     serviceName,
		"coordinateCount": coordinateCount,
		"markerCount":     markerCount,
	})
}

// SaveProcessed saves processed route data (coordinates, markers, entry coords) to the database.
func (h *MapDataHandler) SaveProcessed(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Filename string `json:"filename"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		util.Error(w, http.StatusBadRequest, "Invalid JSON: "+err.Error())
		return
	}

	if body.Filename == "" {
		util.Error(w, http.StatusBadRequest, "filename is required")
		return
	}

	// Derive raw filename from processed filename
	rawFilename := strings.Replace(body.Filename, "processed_", "raw_data_", 1)
	rawFilePath := filepath.Join(config.AppDir(), "recording_data", rawFilename)

	// Read raw file
	rawData, err := os.ReadFile(rawFilePath)
	if err != nil {
		if os.IsNotExist(err) {
			util.Error(w, http.StatusNotFound, fmt.Sprintf("Raw file not found: %s", rawFilename))
		} else {
			util.Error(w, http.StatusInternalServerError, err.Error())
		}
		return
	}

	var parsed map[string]any
	if err := json.Unmarshal(rawData, &parsed); err != nil {
		util.Error(w, http.StatusBadRequest, "Invalid JSON in raw file: "+err.Error())
		return
	}

	// Get timetableId from parsed data
	timetableIDFloat, ok := parsed["timetableId"].(float64)
	if !ok || timetableIDFloat == 0 {
		util.Error(w, http.StatusBadRequest, "timetableId not found in JSON")
		return
	}
	timetableID := int(timetableIDFloat)

	// Validate timetable exists
	var serviceName string
	err = h.db.QueryRow("SELECT service_name FROM timetables WHERE id = ?", timetableID).Scan(&serviceName)
	if err == sql.ErrNoRows {
		util.Error(w, http.StatusNotFound, fmt.Sprintf("Timetable %d not found in database", timetableID))
		return
	}
	if err != nil {
		util.Error(w, http.StatusInternalServerError, err.Error())
		return
	}

	coordinateCount := 0
	markerCount := 0
	entryUpdates := 0

	// Save coordinates
	if coords, ok := parsed["coordinates"].([]any); ok && len(coords) > 0 {
		coordJSON, err := json.Marshal(coords)
		if err == nil {
			_, err = h.db.Exec(
				"INSERT OR REPLACE INTO timetable_coordinates (timetable_id, coordinates, coord_source) VALUES (?, ?, ?)",
				timetableID, string(coordJSON), "automatic",
			)
			if err == nil {
				coordinateCount = len(coords)
			}
		}

		cfg := config.Get()
		if cfg != nil && cfg.ContributorName != "" {
			h.db.Exec("UPDATE timetables SET coordinates_contributor = ? WHERE id = ?", cfg.ContributorName, timetableID)
		}
	}

	// Save markers
	if markers, ok := parsed["markers"].([]any); ok {
		for _, m := range markers {
			mm, ok := m.(map[string]any)
			if !ok {
				continue
			}
			stationName, _ := mm["stationName"].(string)
			markerType, _ := mm["markerType"].(string)
			lat, _ := mm["latitude"].(float64)
			lng, _ := mm["longitude"].(float64)
			platLen, _ := mm["platformLength"].(float64)

			if stationName == "" {
				continue
			}

			_, err := h.db.Exec(
				"INSERT INTO timetable_markers (timetable_id, station_name, marker_type, latitude, longitude, platform_length) VALUES (?, ?, ?, ?, ?, ?)",
				timetableID, stationName, markerType, lat, lng, platLen,
			)
			if err == nil {
				markerCount++
			}
		}
	}

	// Update timetable entries with lat/lng from timetable array
	if ttEntries, ok := parsed["timetable"].([]any); ok {
		for _, entry := range ttEntries {
			em, ok := entry.(map[string]any)
			if !ok {
				continue
			}

			lat, hasLat := em["latitude"].(float64)
			lng, hasLng := em["longitude"].(float64)
			if !hasLat || !hasLng {
				continue
			}

			location, _ := em["location"].(string)
			apiName, _ := em["apiName"].(string)
			indexFloat, _ := em["index"].(float64)
			index := int(indexFloat)

			if location != "" {
				_, err := h.db.Exec(
					`UPDATE timetable_entries SET latitude = ?, longitude = ?, api_name = ?, coord_source = ?
					 WHERE timetable_id = ? AND location_id IN (SELECT id FROM locations WHERE name = ?)`,
					fmt.Sprintf("%f", lat), fmt.Sprintf("%f", lng), apiName, "automatic",
					timetableID, location,
				)
				if err == nil {
					entryUpdates++
				}
			} else if index == 0 {
				// Special case: WAIT FOR SERVICE at sort_order 0
				_, err := h.db.Exec(
					`UPDATE timetable_entries SET latitude = ?, longitude = ?, api_name = ?, coord_source = ?
					 WHERE timetable_id = ? AND sort_order = 0 AND action_id IN (SELECT id FROM timetable_actions WHERE name = 'WAIT FOR SERVICE')`,
					fmt.Sprintf("%f", lat), fmt.Sprintf("%f", lng), apiName, "automatic",
					timetableID,
				)
				if err == nil {
					entryUpdates++
				}
			}
		}
	}

	util.JSON(w, http.StatusOK, map[string]any{
		"success":         true,
		"timetableId":     timetableID,
		"serviceName":     serviceName,
		"coordinateCount": coordinateCount,
		"markerCount":     markerCount,
		"entryUpdates":    entryUpdates,
		"filename":        body.Filename,
	})
}

// GetRouteDataFromDb returns route data for a timetable from the database.
func (h *MapDataHandler) GetRouteDataFromDb(w http.ResponseWriter, r *http.Request) {
	idStr := chi.URLParam(r, "timetableId")
	timetableID, err := strconv.Atoi(idStr)
	if err != nil {
		util.Error(w, http.StatusBadRequest, "timetableId is required")
		return
	}

	// Get timetable info
	var serviceName string
	err = h.db.QueryRow("SELECT service_name FROM timetables WHERE id = ?", timetableID).Scan(&serviceName)
	if err == sql.ErrNoRows {
		util.Error(w, http.StatusNotFound, fmt.Sprintf("Timetable %d not found", timetableID))
		return
	}
	if err != nil {
		util.Error(w, http.StatusInternalServerError, err.Error())
		return
	}

	// Get coordinates
	coordinates := make([]any, 0)
	var coordJSON *string
	err = h.db.QueryRow("SELECT coordinates FROM timetable_coordinates WHERE timetable_id = ?", timetableID).Scan(&coordJSON)
	if err == nil && coordJSON != nil {
		json.Unmarshal([]byte(*coordJSON), &coordinates)
	}

	// Filter out stale Chatham position
	filteredCoords := make([]any, 0, len(coordinates))
	for _, c := range coordinates {
		if cm, ok := c.(map[string]any); ok {
			lat, _ := cm["latitude"].(float64)
			lng, _ := cm["longitude"].(float64)
			if lat == 51.380108707397724 && lng == 0.5219243867730494 {
				continue
			}
			filteredCoords = append(filteredCoords, c)
		} else {
			filteredCoords = append(filteredCoords, c)
		}
	}

	// Get markers
	markers := make([]map[string]any, 0)
	markerRows, err := h.db.Query("SELECT station_name, marker_type, latitude, longitude, platform_length FROM timetable_markers WHERE timetable_id = ?", timetableID)
	if err == nil {
		defer markerRows.Close()
		for markerRows.Next() {
			var stationName string
			var markerType *string
			var lat, lng, platLen *float64
			if err := markerRows.Scan(&stationName, &markerType, &lat, &lng, &platLen); err != nil {
				continue
			}
			markers = append(markers, map[string]any{
				"stationName":    stationName,
				"markerType":     markerType,
				"platformLength": platLen,
				"latitude":       lat,
				"longitude":      lng,
			})
		}
	}

	// Get entries and build timetable array
	timetableArray := buildTimetableArrayFromEntries(h.db, timetableID)

	util.JSON(w, http.StatusOK, map[string]any{
		"routeName":   serviceName,
		"timetableId": timetableID,
		"totalPoints": len(filteredCoords),
		"coordinates": filteredCoords,
		"markers":     markers,
		"timetable":   timetableArray,
	})
}

// buildTimetableArrayFromEntries builds a timetable array from DB entries.
func buildTimetableArrayFromEntries(db *sql.DB, timetableID int) []map[string]any {
	rows, err := db.Query(`
		SELECT te.id, te.sort_order, te.structure_number, te.time1, te.time2,
		       te.latitude, te.longitude, te.api_name,
		       COALESCE(l.name, '') AS location,
		       COALESCE(ta.name, '') AS action
		FROM timetable_entries te
		LEFT JOIN locations l ON te.location_id = l.id
		LEFT JOIN timetable_actions ta ON te.action_id = ta.id
		WHERE te.timetable_id = ?
		ORDER BY te.sort_order
	`, timetableID)
	if err != nil {
		return make([]map[string]any, 0)
	}
	defer rows.Close()

	type entryRow struct {
		ID        int
		SortOrder *int
		StructureNumber *string
		Time1     *string
		Time2     *string
		Latitude  *string
		Longitude *string
		ApiName   *string
		Location  string
		Action    string
	}

	var entries []entryRow
	for rows.Next() {
		var e entryRow
		if err := rows.Scan(&e.ID, &e.SortOrder, &e.StructureNumber, &e.Time1, &e.Time2, &e.Latitude, &e.Longitude, &e.ApiName, &e.Location, &e.Action); err != nil {
			continue
		}
		entries = append(entries, e)
	}

	result := make([]map[string]any, 0)
	entryIndex := 0

	for i, entry := range entries {
		// Only include entries with a location
		if strings.TrimSpace(entry.Location) == "" {
			continue
		}

		arrival := ""
		departure := ""

		// GO VIA LOCATION entries have no times
		if entry.Action != "GO VIA LOCATION" {
			if entry.Action == "WAIT FOR SERVICE" {
				if entry.Time2 != nil {
					arrival = *entry.Time2
				}
			} else {
				if entry.Time1 != nil {
					arrival = *entry.Time1
				}
			}

			if i+1 < len(entries) && entries[i+1].Time1 != nil {
				departure = *entries[i+1].Time1
			}
		}

		structureNumber := ""
		if entry.StructureNumber != nil {
			structureNumber = *entry.StructureNumber
		}
		apiName := ""
		if entry.ApiName != nil {
			apiName = *entry.ApiName
		}

		item := map[string]any{
			"id":        entry.ID,
			"index":     entryIndex,
			"location":  entry.Location,
			"arrival":   arrival,
			"departure": departure,
			"structure_number": structureNumber,
			"apiName":   apiName,
		}

		// Only include coordinates if they exist
		if entry.Latitude != nil && entry.Longitude != nil && *entry.Latitude != "" && *entry.Longitude != "" {
			lat, errLat := strconv.ParseFloat(*entry.Latitude, 64)
			lng, errLng := strconv.ParseFloat(*entry.Longitude, 64)
			if errLat == nil && errLng == nil {
				item["latitude"] = lat
				item["longitude"] = lng
			}
		}

		// Include isPassThrough flag for GO VIA LOCATION entries
		if entry.Action == "GO VIA LOCATION" {
			item["isPassThrough"] = true
		}

		result = append(result, item)
		entryIndex++
	}

	return result
}

// Remake rebuilds processed data from database data and exports as JSON file.
func (h *MapDataHandler) Remake(w http.ResponseWriter, r *http.Request) {
	var body struct {
		TimetableID int `json:"timetableId"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		util.Error(w, http.StatusBadRequest, "Invalid JSON: "+err.Error())
		return
	}

	if body.TimetableID == 0 {
		util.Error(w, http.StatusBadRequest, "timetableId is required")
		return
	}

	// Get timetable info
	var serviceName string
	err := h.db.QueryRow("SELECT service_name FROM timetables WHERE id = ?", body.TimetableID).Scan(&serviceName)
	if err == sql.ErrNoRows {
		util.Error(w, http.StatusNotFound, fmt.Sprintf("Timetable %d not found", body.TimetableID))
		return
	}
	if err != nil {
		util.Error(w, http.StatusInternalServerError, err.Error())
		return
	}

	// Get coordinates
	coordinates := make([]any, 0)
	var coordJSON *string
	err = h.db.QueryRow("SELECT coordinates FROM timetable_coordinates WHERE timetable_id = ?", body.TimetableID).Scan(&coordJSON)
	if err == nil && coordJSON != nil {
		json.Unmarshal([]byte(*coordJSON), &coordinates)
	}

	// Filter out stale Chatham position
	filteredCoords := make([]any, 0, len(coordinates))
	for _, c := range coordinates {
		if cm, ok := c.(map[string]any); ok {
			lat, _ := cm["latitude"].(float64)
			lng, _ := cm["longitude"].(float64)
			if lat == 51.380108707397724 && lng == 0.5219243867730494 {
				continue
			}
			filteredCoords = append(filteredCoords, c)
		} else {
			filteredCoords = append(filteredCoords, c)
		}
	}

	// Get markers
	markers := make([]map[string]any, 0)
	markerRows, err := h.db.Query("SELECT station_name, marker_type, latitude, longitude, platform_length FROM timetable_markers WHERE timetable_id = ?", body.TimetableID)
	if err == nil {
		defer markerRows.Close()
		for markerRows.Next() {
			var stationName string
			var markerType *string
			var lat, lng, platLen *float64
			if err := markerRows.Scan(&stationName, &markerType, &lat, &lng, &platLen); err != nil {
				continue
			}
			markers = append(markers, map[string]any{
				"stationName":    stationName,
				"markerType":     markerType,
				"platformLength": platLen,
				"latitude":       lat,
				"longitude":      lng,
			})
		}
	}

	// Build timetable array
	timetableArray := buildTimetableArrayFromEntries(h.db, body.TimetableID)

	// Build the exported JSON
	exportedJSON := map[string]any{
		"routeName":   serviceName,
		"timetableId": body.TimetableID,
		"totalPoints": len(filteredCoords),
		"coordinates": filteredCoords,
		"markers":     markers,
		"timetable":   timetableArray,
	}

	// Ensure output folder exists
	exportDir := filepath.Join(config.AppDir(), "Remake JSON from DB")
	os.MkdirAll(exportDir, 0755)

	// Create filename with timestamp
	timestamp := time.Now().UTC().Format("2006-01-02T15-04-05")
	filename := fmt.Sprintf("exported_timetable_%d_%s.json", body.TimetableID, timestamp)
	filePath := filepath.Join(exportDir, filename)

	// Write the file
	jsonData, err := json.MarshalIndent(exportedJSON, "", "  ")
	if err != nil {
		util.Error(w, http.StatusInternalServerError, "Failed to marshal JSON: "+err.Error())
		return
	}

	if err := os.WriteFile(filePath, jsonData, 0644); err != nil {
		util.Error(w, http.StatusInternalServerError, "Failed to write file: "+err.Error())
		return
	}

	util.JSON(w, http.StatusOK, map[string]any{
		"success":     true,
		"filename":    filename,
		"timetableId": body.TimetableID,
		"serviceName": serviceName,
		"stats": map[string]any{
			"coordinates":      len(filteredCoords),
			"markers":          len(markers),
			"timetableEntries": len(timetableArray),
		},
	})
}
