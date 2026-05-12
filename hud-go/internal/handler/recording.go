package handler

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/go-chi/chi/v5"
	"hud-go/internal/config"
	"hud-go/internal/util"
)

// Coordinate represents a GPS coordinate point.
type Coordinate struct {
	Latitude  float64 `json:"latitude"`
	Longitude float64 `json:"longitude"`
	TileX     *int    `json:"x,omitempty"`
	TileY     *int    `json:"y,omitempty"`
}

// Marker represents a station/marker discovered during recording.
type Marker struct {
	StationName        string   `json:"stationName"`
	MarkerType         string   `json:"markerType"`
	Lat                float64  `json:"latitude,omitempty"`
	Lng                float64  `json:"longitude,omitempty"`
	PlatformLength     float64  `json:"platformLength,omitempty"`
	DetectedAt         *Coordinate `json:"detectedAt,omitempty"`
	DistanceAheadM     float64  `json:"distanceAheadMeters,omitempty"`
	Timestamp          string   `json:"timestamp,omitempty"`
	OnspotLatitude     *float64 `json:"onspot_latitude,omitempty"`
	OnspotLongitude    *float64 `json:"onspot_longitude,omitempty"`
	OnspotTimestamp    string   `json:"onspot_timestamp,omitempty"`
	OnspotDistance     float64  `json:"onspot_distance,omitempty"`
}

// processedEntry represents a preprocessed timetable entry for stop detection.
type processedEntry struct {
	Index         int    `json:"index"`
	Location      string `json:"location"`
	Arrival       string `json:"arrival"`
	Departure     string `json:"departure"`
	StructureNumber string `json:"structure_number"`
	ApiName       string `json:"apiName"`
	IsPassThrough bool   `json:"isPassThrough,omitempty"`
	Action        string `json:"action"`
}

// RecordingHandler manages GPS coordinate recording sessions.
type RecordingHandler struct {
	db          *sql.DB
	mu          sync.Mutex
	isRecording bool
	isPaused    bool
	timetableID int
	coordinates []Coordinate
	markers     []Marker
	savedCoords map[int]Coordinate
	mode        string // "manual" or "automatic"
	outputFile  string

	// Stop detection fields
	stopSpeedThreshold    float64          // Speed below this = stopped (default 2)
	stopMovingReadings    int              // Consecutive moving readings to confirm movement (default 10)
	stopConfirmedReadings int              // Consecutive stopped readings to confirm stop (default 300)
	stoppedCount          int              // Current consecutive stopped reading count
	movingCount           int              // Current consecutive moving reading count
	isStopped             bool             // True when stop confirmed
	cachedEntries         []processedEntry // Preprocessed timetable entries

	// Auto-stop fields
	lastUniqueCoordTime  time.Time // Last time a new unique coordinate was seen
	autoStopEnabled      bool      // Whether auto-stop is active (mode == "automatic")
	autoStopped          bool      // Flag sent to frontend to trigger stop
	autoStopTimeoutMs    int       // Inactivity timeout in ms (default 120000)
	lastTimetableTimeSec *int      // Last scheduled time in timetable (seconds since midnight)
	gameTimeSec          *int      // Current in-game time (seconds since midnight)

	// Marker/position tracking
	currentPosition      *Coordinate     // Latest player position (for marker processing)
	currentTileX         *int            // Latest player tile X (from DriverAid.PlayerInfo.currentTile)
	currentTileY         *int            // Latest player tile Y
	processedMarkerNames map[string]bool // Track which markers have been seen

	// Save frequency
	saveFrequency int // Save to file every N coordinates
}

// recordingDataDir returns the path to the recording_data directory.
func recordingDataDir() string {
	return filepath.Join(config.ResourcesDir(), "recording_data")
}

// ensureRecordingDir creates the recording_data directory if it doesn't exist.
func ensureRecordingDir() error {
	dir := recordingDataDir()
	return os.MkdirAll(dir, 0755)
}

// timeStringToSeconds parses "HH:MM:SS" or "HH:MM" to seconds since midnight.
func timeStringToSeconds(timeStr string) *int {
	if timeStr == "" {
		return nil
	}
	parts := strings.Split(timeStr, ":")
	if len(parts) < 2 {
		return nil
	}
	hours, err := strconv.Atoi(parts[0])
	if err != nil {
		return nil
	}
	minutes, err := strconv.Atoi(parts[1])
	if err != nil {
		return nil
	}
	seconds := 0
	if len(parts) >= 3 {
		seconds, _ = strconv.Atoi(parts[2])
	}
	result := hours*3600 + minutes*60 + seconds
	return &result
}

// isoTimeToSeconds parses an ISO8601 time string to seconds since midnight.
// Example: "2024-01-27T05:47:56.149+00:00" -> seconds for 05:47:56
func isoTimeToSeconds(isoStr string) *int {
	if isoStr == "" {
		return nil
	}
	parts := strings.SplitN(isoStr, "T", 2)
	if len(parts) < 2 {
		return nil
	}
	timePart := parts[1]
	// Remove milliseconds and timezone
	dotIdx := strings.Index(timePart, ".")
	if dotIdx >= 0 {
		timePart = timePart[:dotIdx]
	} else {
		// Handle +00:00 or Z timezone suffix without milliseconds
		for _, sep := range []string{"+", "Z"} {
			if idx := strings.Index(timePart, sep); idx >= 0 {
				timePart = timePart[:idx]
				break
			}
		}
	}
	return timeStringToSeconds(timePart)
}

// calculateLastTimetableTime returns the latest arrival or departure time in seconds.
func calculateLastTimetableTime(entries []processedEntry) *int {
	var lastTime *int
	for _, entry := range entries {
		for _, t := range []string{entry.Arrival, entry.Departure} {
			sec := timeStringToSeconds(t)
			if sec != nil && (lastTime == nil || *sec > *lastTime) {
				lastTime = sec
			}
		}
	}
	return lastTime
}

// findNextUnrecordedStopIndex returns the index of the next non-passthrough entry
// that doesn't already have coordinates, or -1 if all are recorded.
func (h *RecordingHandler) findNextUnrecordedStopIndex() int {
	for i, entry := range h.cachedEntries {
		if entry.IsPassThrough {
			continue
		}
		if _, has := h.savedCoords[i]; has {
			continue
		}
		return i
	}
	return -1
}

// checkAllStopsRecorded checks if all non-passthrough stops have coordinates.
// If so and game time is past last timetable time, triggers auto-stop.
func (h *RecordingHandler) checkAllStopsRecorded() {
	if len(h.cachedEntries) == 0 {
		return
	}
	nextIdx := h.findNextUnrecordedStopIndex()
	if nextIdx != -1 {
		return
	}
	// All stops recorded
	nonPassCount := 0
	for _, e := range h.cachedEntries {
		if !e.IsPassThrough {
			nonPassCount++
		}
	}
	log.Printf("[Auto-coord] All %d station coordinates recorded", nonPassCount)

	if h.lastTimetableTimeSec != nil && h.gameTimeSec != nil && *h.gameTimeSec >= *h.lastTimetableTimeSec {
		log.Printf("[Auto-coord] Game time past last timetable time AND all stops recorded - triggering auto-stop")
		h.autoStopped = true
	}
}

// checkAutoStop checks if auto-stop conditions are met.
// Must be called with h.mu held.
func (h *RecordingHandler) checkAutoStop() {
	if !h.autoStopEnabled || !h.isRecording || h.isPaused {
		return
	}

	pastLastTime := false
	if h.lastTimetableTimeSec != nil && h.gameTimeSec != nil {
		pastLastTime = *h.gameTimeSec >= *h.lastTimetableTimeSec
	} else if h.lastTimetableTimeSec == nil {
		// No timetable time found, only use inactivity
		pastLastTime = true
	}

	if !pastLastTime {
		return
	}

	if h.lastUniqueCoordTime.IsZero() {
		return
	}

	elapsed := time.Since(h.lastUniqueCoordTime)
	if elapsed.Milliseconds() > int64(h.autoStopTimeoutMs) {
		log.Printf("Auto-stop triggered: train stationary for %v AND game time past last timetable time", elapsed.Round(time.Second))
		h.autoStopped = true
	}
}

// preprocessTimetableEntries queries timetable_entries and builds processed entries.
func (h *RecordingHandler) preprocessTimetableEntries(timetableID int) []processedEntry {
	rows, err := h.db.Query(`
		SELECT te.id, COALESCE(l.name, '') as location, te.structure_number,
			te.time1, te.time2, COALESCE(ta.name, '') as action, te.api_name
		FROM timetable_entries te
		LEFT JOIN timetable_actions ta ON ta.id = te.action_id
		LEFT JOIN locations l ON l.id = te.location_id
		WHERE te.timetable_id = ?
		ORDER BY te.sort_order`, timetableID)
	if err != nil {
		log.Printf("Error querying timetable entries: %v", err)
		return nil
	}
	defer rows.Close()

	type rawEntry struct {
		ID              int
		Location        string
		StructureNumber string
		Time1           *string
		Time2           *string
		Action          string
		ApiName         *string
	}

	var rawEntries []rawEntry
	for rows.Next() {
		var e rawEntry
		rows.Scan(&e.ID, &e.Location, &e.StructureNumber, &e.Time1, &e.Time2, &e.Action, &e.ApiName)
		rawEntries = append(rawEntries, e)
	}

	// Get station name mapping for this timetable's route
	stationMapping := h.getStationNameMapping(timetableID)

	var processed []processedEntry
	idx := 0

	for i := 0; i < len(rawEntries); i++ {
		e := rawEntries[i]
		action := strings.ToUpper(strings.TrimSpace(e.Action))

		switch action {
		case "WAIT FOR SERVICE":
			location := e.Location
			platform := e.StructureNumber
			arrival := ptrStr(e.Time2)
			departure := arrival

			// Look for following LOAD PASSENGERS to get departure time
			if i+1 < len(rawEntries) {
				nextAction := strings.ToUpper(strings.TrimSpace(rawEntries[i+1].Action))
				if nextAction == "LOAD PASSENGERS" {
					departure = ptrStr(rawEntries[i+1].Time1)
					i++ // Skip the LOAD PASSENGERS entry
				}
			}

			apiName := buildApiName(location, platform, stationMapping)
			processed = append(processed, processedEntry{
				Index:     idx,
				Location:  location,
				Arrival:   arrival,
				Departure: departure,
				StructureNumber:  platform,
				ApiName:   apiName,
				Action:    e.Action,
			})
			idx++

		case "STOP AT LOCATION":
			location := e.Location
			platform := e.StructureNumber
			arrival := ptrStr(e.Time1)
			departure := ""

			// Look for following LOAD PASSENGERS to get departure time
			if i+1 < len(rawEntries) {
				nextAction := strings.ToUpper(strings.TrimSpace(rawEntries[i+1].Action))
				if nextAction == "LOAD PASSENGERS" {
					departure = ptrStr(rawEntries[i+1].Time1)
					i++ // Skip the LOAD PASSENGERS entry
				}
			}

			apiName := buildApiName(location, platform, stationMapping)
			processed = append(processed, processedEntry{
				Index:     idx,
				Location:  location,
				Arrival:   arrival,
				Departure: departure,
				StructureNumber:  platform,
				ApiName:   apiName,
				Action:    e.Action,
			})
			idx++

		case "UNLOAD PASSENGERS":
			location := e.Location
			if location != "" && location != "-" {
				platform := e.StructureNumber
				arrival := ptrStr(e.Time1)
				apiName := buildApiName(location, platform, stationMapping)
				processed = append(processed, processedEntry{
					Index:     idx,
					Location:  location,
					Arrival:   arrival,
					Departure: "",
					StructureNumber:  platform,
					ApiName:   apiName,
					Action:    e.Action,
				})
				idx++
			}

		case "GO VIA LOCATION":
			location := e.Location
			if location != "" && location != "-" {
				platform := e.StructureNumber
				mappedLoc := stationMapping[location]
				if mappedLoc == "" {
					mappedLoc = location
				}
				processed = append(processed, processedEntry{
					Index:         idx,
					Location:      location,
					Arrival:       "",
					Departure:     "",
					StructureNumber:      platform,
					ApiName:       mappedLoc,
					IsPassThrough: true,
					Action:        e.Action,
				})
				idx++
			}
		}
	}

	return processed
}

// getStationNameMapping returns a map of location name -> API name for a timetable's route.
func (h *RecordingHandler) getStationNameMapping(timetableID int) map[string]string {
	mapping := make(map[string]string)

	// Get route_id for this timetable
	var routeID *int
	h.db.QueryRow("SELECT route_id FROM timetables WHERE id = ?", timetableID).Scan(&routeID)

	query := `SELECT display_name, api_name FROM station_name_mappings WHERE route_id IS NULL`
	args := []any{}
	if routeID != nil {
		query = `SELECT display_name, api_name FROM station_name_mappings WHERE route_id = ? OR route_id IS NULL`
		args = append(args, *routeID)
	}

	rows, err := h.db.Query(query, args...)
	if err != nil {
		return mapping
	}
	defer rows.Close()

	for rows.Next() {
		var locName, apiName string
		rows.Scan(&locName, &apiName)
		mapping[locName] = apiName
	}
	return mapping
}

// buildApiName builds the API name from location, platform, and station mapping.
func buildApiName(location, platform string, mapping map[string]string) string {
	mappedLoc := mapping[location]
	if mappedLoc == "" {
		mappedLoc = location
	}
	if mappedLoc != "" && platform != "" {
		return mappedLoc + " " + platform
	}
	return ""
}

// saveRecordingFile writes the current recording state to the output JSON file.
func (h *RecordingHandler) saveRecordingFile() {
	if h.outputFile == "" {
		return
	}

	// Build timetable entries with saved coordinates
	timetable := make([]map[string]any, 0, len(h.cachedEntries))
	for i, entry := range h.cachedEntries {
		te := map[string]any{
			"index":    entry.Index,
			"location": entry.Location,
			"arrival":  entry.Arrival,
			"departure": entry.Departure,
			"structure_number": entry.StructureNumber,
			"apiName":  entry.ApiName,
		}
		if entry.IsPassThrough {
			te["isPassThrough"] = true
		}
		if saved, ok := h.savedCoords[i]; ok {
			te["latitude"] = saved.Latitude
			te["longitude"] = saved.Longitude
			if saved.TileX != nil {
				te["x"] = *saved.TileX
			}
			if saved.TileY != nil {
				te["y"] = *saved.TileY
			}
		}
		timetable = append(timetable, te)
	}

	coords := h.coordinates
	if coords == nil {
		coords = make([]Coordinate, 0)
	}
	markers := h.markers
	if markers == nil {
		markers = make([]Marker, 0)
	}

	data := map[string]any{
		"timetableId":   h.timetableID,
		"coordinates":   coords,
		"markers":       markers,
		"timetable":     timetable,
		"completed":     false,
		"recordingMode": h.mode,
	}

	jsonData, err := json.MarshalIndent(data, "", "  ")
	if err != nil {
		log.Printf("Failed to marshal recording data: %v", err)
		return
	}
	if err := ensureRecordingDir(); err != nil {
		log.Printf("Failed to ensure recording dir: %v", err)
		return
	}
	if err := os.WriteFile(h.outputFile, jsonData, 0644); err != nil {
		log.Printf("Failed to save recording file: %v", err)
	}
}

// Start begins or resumes a recording session for a timetable.
func (h *RecordingHandler) Start(w http.ResponseWriter, r *http.Request) {
	idStr := chi.URLParam(r, "timetableId")
	timetableID, err := strconv.Atoi(idStr)
	if err != nil {
		util.Error(w, http.StatusBadRequest, "invalid timetable ID")
		return
	}

	h.mu.Lock()
	defer h.mu.Unlock()

	// If already recording the same timetable, return success
	if h.isRecording && !h.isPaused && h.timetableID == timetableID {
		util.JSON(w, http.StatusOK, map[string]any{
			"success":         true,
			"message":         "Recording already active",
			"timetableId":     timetableID,
			"coordinateCount": len(h.coordinates),
			"markerCount":     len(h.markers),
		})
		return
	}

	// If recording a different timetable, reject
	if h.isRecording && !h.isPaused && h.timetableID != timetableID {
		util.Error(w, http.StatusBadRequest, "Recording already in progress for a different timetable")
		return
	}

	// Validate timetable exists
	var serviceName string
	err = h.db.QueryRow("SELECT service_name FROM timetables WHERE id = ?", timetableID).Scan(&serviceName)
	if err == sql.ErrNoRows {
		util.Error(w, http.StatusNotFound, "Timetable not found")
		return
	}
	if err != nil {
		util.Error(w, http.StatusInternalServerError, err.Error())
		return
	}

	// If resuming a paused recording for the same timetable
	if h.isPaused && h.timetableID == timetableID {
		h.isPaused = false
		h.isRecording = true

		// Reset stop detection counters on resume
		h.stoppedCount = 0
		h.movingCount = 0
		h.isStopped = false

		// Ensure cached entries exist on resume
		if len(h.cachedEntries) == 0 {
			h.cachedEntries = h.preprocessTimetableEntries(timetableID)
		}

		// Reset auto-stop timer on resume
		if h.autoStopEnabled {
			h.lastUniqueCoordTime = time.Now()
		}

		log.Printf("Resumed recording for timetable %d", timetableID)
		util.JSON(w, http.StatusOK, map[string]any{
			"success":         true,
			"message":         "Recording resumed",
			"timetableId":     timetableID,
			"coordinateCount": len(h.coordinates),
			"markerCount":     len(h.markers),
		})
		return
	}

	// Ensure recording_data directory exists
	if err := ensureRecordingDir(); err != nil {
		util.Error(w, http.StatusInternalServerError, "failed to create recording directory: "+err.Error())
		return
	}

	// Start new recording
	h.timetableID = timetableID
	if h.mode == "" {
		h.mode = "automatic"
	}
	h.autoStopEnabled = (h.mode == "automatic")

	// Load recording settings from config
	cfg := config.Get()
	minStopSec := cfg.MinStopDurationSeconds
	if minStopSec <= 0 {
		minStopSec = 30
	}
	autoStopSec := cfg.AutoStopTimeoutSeconds
	if autoStopSec <= 0 {
		autoStopSec = 120
	}
	h.stopSpeedThreshold = 2
	h.stopMovingReadings = 10
	h.stopConfirmedReadings = minStopSec * 10 // 10 readings per second at 100ms
	h.autoStopTimeoutMs = autoStopSec * 1000
	h.saveFrequency = cfg.SaveFrequency
	if h.saveFrequency <= 0 {
		h.saveFrequency = 1
	}

	// Reset stop detection state
	h.stoppedCount = 0
	h.movingCount = 0
	h.isStopped = false
	h.autoStopped = false
	h.gameTimeSec = nil
	h.currentPosition = nil
	h.processedMarkerNames = make(map[string]bool)

	// Generate output file path
	timestamp := time.Now().Format("2006-01-02_15-04-05")
	fileName := fmt.Sprintf("raw_data_timetable_%d_%s.json", timetableID, timestamp)
	h.outputFile = filepath.Join(recordingDataDir(), fileName)

	// Check for existing incomplete recording file to resume from
	foundIncomplete := false
	dir := recordingDataDir()
	if entries, err := os.ReadDir(dir); err == nil {
		prefix := fmt.Sprintf("raw_data_timetable_%d_", timetableID)
		for i := len(entries) - 1; i >= 0; i-- {
			entry := entries[i]
			if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") || !strings.HasPrefix(entry.Name(), prefix) {
				continue
			}
			filePath := filepath.Join(dir, entry.Name())
			data, err := os.ReadFile(filePath)
			if err != nil {
				continue
			}
			var parsed map[string]any
			if json.Unmarshal(data, &parsed) != nil {
				continue
			}
			if completed, ok := parsed["completed"].(bool); ok && completed {
				continue
			}

			// Found incomplete file - resume from it
			h.outputFile = filePath
			foundIncomplete = true
			h.coordinates = make([]Coordinate, 0)
			if coordsRaw, ok := parsed["coordinates"].([]any); ok {
				for _, c := range coordsRaw {
					if cm, ok := c.(map[string]any); ok {
						lat, _ := cm["latitude"].(float64)
						lng, _ := cm["longitude"].(float64)
						coord := Coordinate{Latitude: lat, Longitude: lng}
						if x, ok := cm["x"].(float64); ok {
							xi := int(x)
							coord.TileX = &xi
						}
						if y, ok := cm["y"].(float64); ok {
							yi := int(y)
							coord.TileY = &yi
						}
						h.coordinates = append(h.coordinates, coord)
					}
				}
				log.Printf("Resuming recording: %d existing coordinates loaded", len(h.coordinates))
			}
			h.markers = make([]Marker, 0)
			if markersRaw, ok := parsed["markers"].([]any); ok {
				for _, m := range markersRaw {
					if mm, ok := m.(map[string]any); ok {
						marker := Marker{}
						if sn, ok := mm["stationName"].(string); ok {
							marker.StationName = sn
							h.processedMarkerNames[sn] = true
						}
						if mt, ok := mm["markerType"].(string); ok {
							marker.MarkerType = mt
						}
						if pl, ok := mm["platformLength"].(float64); ok {
							marker.PlatformLength = pl
						}
						h.markers = append(h.markers, marker)
					}
				}
				log.Printf("Resuming recording: %d existing markers loaded", len(h.markers))
			}
			h.savedCoords = make(map[int]Coordinate)
			if ttRaw, ok := parsed["timetable"].([]any); ok {
				for idx, te := range ttRaw {
					if em, ok := te.(map[string]any); ok {
						lat, hasLat := em["latitude"].(float64)
						lng, hasLng := em["longitude"].(float64)
						if hasLat && hasLng {
							coord := Coordinate{Latitude: lat, Longitude: lng}
							if x, ok := em["x"].(float64); ok {
								xi := int(x)
								coord.TileX = &xi
							}
							if y, ok := em["y"].(float64); ok {
								yi := int(y)
								coord.TileY = &yi
							}
							h.savedCoords[idx] = coord
						}
					}
				}
				log.Printf("Resuming recording: %d saved station coordinates loaded", len(h.savedCoords))
			}
			break
		}
	}

	if !foundIncomplete {
		// Fresh start
		h.coordinates = make([]Coordinate, 0)
		h.markers = make([]Marker, 0)
		h.savedCoords = make(map[int]Coordinate)
		h.processedMarkerNames = make(map[string]bool)
	}

	// Cache preprocessed entries for stop detection
	h.cachedEntries = h.preprocessTimetableEntries(timetableID)
	stopCount := 0
	passCount := 0
	for _, e := range h.cachedEntries {
		if e.IsPassThrough {
			passCount++
		} else {
			stopCount++
		}
	}
	log.Printf("Cached %d timetable entries (%d stops, %d pass-through)", len(h.cachedEntries), stopCount, passCount)

	// Calculate the last time from the timetable for auto-stop logic
	h.lastTimetableTimeSec = calculateLastTimetableTime(h.cachedEntries)
	if h.lastTimetableTimeSec != nil {
		secs := *h.lastTimetableTimeSec
		log.Printf("Last timetable time: %02d:%02d:%02d", secs/3600, (secs%3600)/60, secs%60)
	}

	// Initialize auto-stop timer
	if h.autoStopEnabled {
		h.lastUniqueCoordTime = time.Now()
	}

	h.isRecording = true
	h.isPaused = false

	// Save initial data
	h.saveRecordingFile()

	log.Printf("Started recording for timetable %d: %s", timetableID, serviceName)

	util.JSON(w, http.StatusOK, map[string]any{
		"success":     true,
		"message":     "Recording started",
		"timetableId": timetableID,
		"serviceName": serviceName,
		"outputFile":  filepath.Base(h.outputFile),
		"mode":        h.mode,
	})
}

// Stop ends the current recording and saves data to file.
func (h *RecordingHandler) Stop(w http.ResponseWriter, r *http.Request) {
	h.mu.Lock()
	defer h.mu.Unlock()

	if !h.isRecording && !h.isPaused {
		util.Error(w, http.StatusBadRequest, "No recording in progress")
		return
	}

	// Save final data to file (mark as completed)
	coordCount := len(h.coordinates)
	markerCount := len(h.markers)
	outputFile := h.outputFile

	if outputFile != "" {
		// Build timetable with saved coords for final save
		timetable := make([]map[string]any, 0, len(h.cachedEntries))
		for i, entry := range h.cachedEntries {
			te := map[string]any{
				"index":     entry.Index,
				"location":  entry.Location,
				"arrival":   entry.Arrival,
				"departure": entry.Departure,
				"structure_number": entry.StructureNumber,
				"apiName":   entry.ApiName,
			}
			if entry.IsPassThrough {
				te["isPassThrough"] = true
			}
			if saved, ok := h.savedCoords[i]; ok {
				te["latitude"] = saved.Latitude
				te["longitude"] = saved.Longitude
				if saved.TileX != nil {
					te["x"] = *saved.TileX
				}
				if saved.TileY != nil {
					te["y"] = *saved.TileY
				}
			}
			timetable = append(timetable, te)
		}

		data := map[string]any{
			"timetableId":   h.timetableID,
			"coordinates":   h.coordinates,
			"markers":       h.markers,
			"timetable":     timetable,
			"completed":     true,
			"recordingMode": h.mode,
		}

		jsonData, err := json.MarshalIndent(data, "", "  ")
		if err == nil {
			if err := ensureRecordingDir(); err == nil {
				os.WriteFile(outputFile, jsonData, 0644)
			}
		}
	}

	// Get service name before releasing lock
	var serviceName string
	h.db.QueryRow("SELECT service_name FROM timetables WHERE id = ?", h.timetableID).Scan(&serviceName)

	// Save to database
	savedTimetableID := h.timetableID
	savedCoordsCopy := make(map[int]Coordinate)
	for k, v := range h.savedCoords {
		savedCoordsCopy[k] = v
	}
	cachedEntriesCopy := make([]processedEntry, len(h.cachedEntries))
	copy(cachedEntriesCopy, h.cachedEntries)
	coordsCopy := make([]Coordinate, len(h.coordinates))
	copy(coordsCopy, h.coordinates)
	markersCopy := make([]Marker, len(h.markers))
	copy(markersCopy, h.markers)

	// Reset state before DB operations (unlock not needed since we hold the lock)
	h.isRecording = false
	h.isPaused = false
	h.stoppedCount = 0
	h.movingCount = 0
	h.isStopped = false
	h.autoStopped = false
	h.cachedEntries = nil
	h.lastTimetableTimeSec = nil
	h.gameTimeSec = nil
	h.currentPosition = nil
	h.processedMarkerNames = nil

	// Release lock for DB operations
	h.mu.Unlock()

	dbStats := h.saveToDatabase(savedTimetableID, coordsCopy, markersCopy, cachedEntriesCopy, savedCoordsCopy)

	// Delete raw file after successful DB save
	if outputFile != "" {
		os.Remove(outputFile)
	}

	// Re-lock for the deferred unlock
	h.mu.Lock()

	util.JSON(w, http.StatusOK, map[string]any{
		"success":     true,
		"message":     "Recording saved successfully",
		"timetableId": savedTimetableID,
		"serviceName": serviceName,
		"stats": map[string]any{
			"rawCoordinates":       coordCount,
			"rawMarkers":           markerCount,
			"processedCoordinates": dbStats["coordinatesSaved"],
			"processedMarkers":     dbStats["markersSaved"],
			"entriesSavedToDb":     dbStats["entriesUpdated"],
			"detectedStops":        0,
		},
	})
}

// saveToDatabase writes recording data to the database tables.
func (h *RecordingHandler) saveToDatabase(timetableID int, coords []Coordinate, markers []Marker, entries []processedEntry, savedCoords map[int]Coordinate) map[string]any {
	stats := map[string]any{
		"coordinatesSaved": 0,
		"markersSaved":     0,
		"entriesUpdated":   0,
		"errors":           []string{},
	}
	var errors []string

	// 1. Save coordinates to timetable_coordinates
	if len(coords) > 0 {
		coordJSON, err := json.Marshal(coords)
		if err == nil {
			// Delete existing then insert
			h.db.Exec("DELETE FROM timetable_coordinates WHERE timetable_id = ?", timetableID)
			_, err = h.db.Exec(
				"INSERT INTO timetable_coordinates (timetable_id, coordinates, coord_source) VALUES (?, ?, 'automatic')",
				timetableID, string(coordJSON),
			)
			if err != nil {
				errors = append(errors, "coordinates: "+err.Error())
			} else {
				stats["coordinatesSaved"] = len(coords)
				log.Printf("[Recording] Saved %d coordinates for timetable %d", len(coords), timetableID)

				// Set coordinates_contributor from config
				cfg := config.Get()
				if cfg.ContributorName != "" {
					h.db.Exec("UPDATE timetables SET coordinates_contributor = ? WHERE id = ?", cfg.ContributorName, timetableID)
				}
			}
		}
	}

	// 2. Save markers to timetable_markers
	if len(markers) > 0 {
		h.db.Exec("DELETE FROM timetable_markers WHERE timetable_id = ?", timetableID)
		markerCount := 0
		for _, m := range markers {
			_, err := h.db.Exec(
				"INSERT INTO timetable_markers (timetable_id, station_name, marker_type, latitude, longitude, platform_length) VALUES (?, ?, ?, ?, ?, ?)",
				timetableID, m.StationName, m.MarkerType, m.OnspotLatitude, m.OnspotLongitude, m.PlatformLength,
			)
			if err == nil {
				markerCount++
			}
		}
		stats["markersSaved"] = markerCount
		log.Printf("[Recording] Saved %d markers for timetable %d", markerCount, timetableID)
	}

	// 3. Update timetable_entries with saved coordinates
	if len(savedCoords) > 0 {
		// Get DB entries ordered by sort_order to match indices
		rows, err := h.db.Query(`
			SELECT te.id, COALESCE(l.name, '') as location, COALESCE(ta.name, '') as action, te.sort_order
			FROM timetable_entries te
			LEFT JOIN locations l ON l.id = te.location_id
			LEFT JOIN timetable_actions ta ON ta.id = te.action_id
			WHERE te.timetable_id = ?
			ORDER BY te.sort_order`, timetableID)
		if err != nil {
			errors = append(errors, "query entries: "+err.Error())
		} else {
			defer rows.Close()
			type dbEntry struct {
				ID       int
				Location string
				Action   string
				Order    int
			}
			var dbEntries []dbEntry
			for rows.Next() {
				var e dbEntry
				rows.Scan(&e.ID, &e.Location, &e.Action, &e.Order)
				dbEntries = append(dbEntries, e)
			}

			entriesUpdated := 0
			for idx, coord := range savedCoords {
				if idx >= len(entries) {
					continue
				}
				entry := entries[idx]

				// Match by location name against DB entries
				for _, dbe := range dbEntries {
					if dbe.Location == entry.Location || (idx == 0 && dbe.Action == "WAIT FOR SERVICE") {
						var tileXArg, tileYArg any
						if coord.TileX != nil {
							tileXArg = *coord.TileX
						}
						if coord.TileY != nil {
							tileYArg = *coord.TileY
						}
						_, err := h.db.Exec(
							"UPDATE timetable_entries SET latitude = ?, longitude = ?, tile_x = ?, tile_y = ?, coord_source = 'automatic' WHERE id = ?",
							fmt.Sprintf("%f", coord.Latitude), fmt.Sprintf("%f", coord.Longitude), tileXArg, tileYArg, dbe.ID,
						)
						if err == nil {
							entriesUpdated++
							log.Printf("[Recording] Updated entry %d (%s): %.6f, %.6f", dbe.ID, entry.Location, coord.Latitude, coord.Longitude)
						} else {
							errors = append(errors, fmt.Sprintf("update entry %d: %s", dbe.ID, err.Error()))
						}
						break
					}
				}
			}
			stats["entriesUpdated"] = entriesUpdated
		}
	}

	if len(errors) > 0 {
		stats["errors"] = errors
	}
	return stats
}

// Reset clears all recording state.
func (h *RecordingHandler) Reset(w http.ResponseWriter, r *http.Request) {
	h.mu.Lock()
	defer h.mu.Unlock()

	// Delete the incomplete recording file so Start doesn't resume from it
	if h.outputFile != "" {
		os.Remove(h.outputFile)
	}

	h.isRecording = false
	h.isPaused = false
	h.timetableID = 0
	h.coordinates = nil
	h.markers = nil
	h.savedCoords = nil
	h.outputFile = ""
	h.stoppedCount = 0
	h.movingCount = 0
	h.isStopped = false
	h.autoStopped = false
	h.cachedEntries = nil
	h.lastTimetableTimeSec = nil
	h.gameTimeSec = nil
	h.currentPosition = nil
	h.processedMarkerNames = nil

	util.JSON(w, http.StatusOK, map[string]any{
		"success": true,
		"message": "Recording state reset",
	})
}

// Pause pauses the current recording.
func (h *RecordingHandler) Pause(w http.ResponseWriter, r *http.Request) {
	h.mu.Lock()
	defer h.mu.Unlock()

	if !h.isRecording {
		util.Error(w, http.StatusBadRequest, "No recording in progress")
		return
	}

	if h.isPaused {
		util.Error(w, http.StatusBadRequest, "Recording already paused")
		return
	}

	h.isPaused = true

	util.JSON(w, http.StatusOK, map[string]any{
		"success":         true,
		"message":         "Recording paused",
		"timetableId":     h.timetableID,
		"coordinateCount": len(h.coordinates),
		"markerCount":     len(h.markers),
	})
}

// Resume resumes a paused recording.
func (h *RecordingHandler) Resume(w http.ResponseWriter, r *http.Request) {
	h.mu.Lock()
	defer h.mu.Unlock()

	if !h.isPaused {
		util.Error(w, http.StatusBadRequest, "Recording is not paused")
		return
	}

	h.isPaused = false

	util.JSON(w, http.StatusOK, map[string]any{
		"success":         true,
		"message":         "Recording resumed",
		"timetableId":     h.timetableID,
		"coordinateCount": len(h.coordinates),
		"markerCount":     len(h.markers),
	})
}

// GetRouteData returns the current recording state.
func (h *RecordingHandler) GetRouteData(w http.ResponseWriter, r *http.Request) {
	h.mu.Lock()
	defer h.mu.Unlock()

	coords := h.coordinates
	if coords == nil {
		coords = make([]Coordinate, 0)
	}
	markers := h.markers
	if markers == nil {
		markers = make([]Marker, 0)
	}

	// Build timetable entries array with saved coordinates (matching Node behavior)
	timetable := make([]map[string]any, 0, len(h.cachedEntries))
	for i, entry := range h.cachedEntries {
		te := map[string]any{
			"index":     entry.Index,
			"location":  entry.Location,
			"arrival":   entry.Arrival,
			"departure": entry.Departure,
			"structure_number": entry.StructureNumber,
			"apiName":   entry.ApiName,
		}
		if entry.IsPassThrough {
			te["isPassThrough"] = true
		}
		// Priority: savedCoords from current recording session, then database coordinates
		if saved, ok := h.savedCoords[i]; ok {
			te["latitude"] = saved.Latitude
			te["longitude"] = saved.Longitude
			if saved.TileX != nil {
				te["x"] = *saved.TileX
			}
			if saved.TileY != nil {
				te["y"] = *saved.TileY
			}
		} else if h.timetableID > 0 {
			// Check database for existing coordinates
			te["latitude"] = nil
			te["longitude"] = nil
		}
		timetable = append(timetable, te)
	}

	// Get route name
	routeName := "No Recording"
	if h.timetableID > 0 {
		var sn string
		if err := h.db.QueryRow("SELECT service_name FROM timetables WHERE id = ?", h.timetableID).Scan(&sn); err == nil {
			routeName = sn
		}
	}

	var currentPos *Coordinate
	if h.currentPosition != nil {
		currentPos = h.currentPosition
	}

	util.JSON(w, http.StatusOK, map[string]any{
		"isRecording":     h.isRecording,
		"isPaused":        h.isPaused,
		"timetableId":     h.timetableID,
		"routeName":       routeName,
		"coordinates":     coords,
		"markers":         markers,
		"timetable":       timetable,
		"currentPosition": currentPos,
	})
}

// SaveTimetableCoords stores a coordinate for a timetable entry index.
// NOTE: Only saves to the recording JSON file, NOT to the database (matching Node behavior).
func (h *RecordingHandler) SaveTimetableCoords(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Index     int     `json:"index"`
		Latitude  float64 `json:"latitude"`
		Longitude float64 `json:"longitude"`
		X         *int    `json:"x"`
		Y         *int    `json:"y"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		util.Error(w, http.StatusBadRequest, "Invalid JSON: "+err.Error())
		return
	}

	h.mu.Lock()
	defer h.mu.Unlock()

	if h.timetableID == 0 {
		util.Error(w, http.StatusBadRequest, "No recording in progress")
		return
	}

	// Validate index against cached entries
	if len(h.cachedEntries) == 0 {
		util.Error(w, http.StatusBadRequest, "No timetable entries cached")
		return
	}
	if body.Index < 0 || body.Index >= len(h.cachedEntries) {
		util.Error(w, http.StatusBadRequest, fmt.Sprintf("Invalid index %d. Must be between 0 and %d", body.Index, len(h.cachedEntries)-1))
		return
	}

	// Save coordinates to in-memory map
	if h.savedCoords == nil {
		h.savedCoords = make(map[int]Coordinate)
	}
	h.savedCoords[body.Index] = Coordinate{
		Latitude:  body.Latitude,
		Longitude: body.Longitude,
		TileX:     body.X,
		TileY:     body.Y,
	}

	log.Printf("Saved timetable entry %d coordinates to recording: %f, %f", body.Index, body.Latitude, body.Longitude)

	// Save to the route file
	h.saveRecordingFile()

	location := h.cachedEntries[body.Index].Location

	util.JSON(w, http.StatusOK, map[string]any{
		"success":  true,
		"message":  fmt.Sprintf("Saved coordinates for index %d", body.Index),
		"location": location,
	})
}

// List returns all JSON files in the recording_data directory.
func (h *RecordingHandler) List(w http.ResponseWriter, r *http.Request) {
	dir := recordingDataDir()

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

		// Try to parse the file for additional metadata
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
				if tp, ok := parsed["totalPoints"]; ok {
					fileEntry["coordinateCount"] = tp
				} else if coords, ok := parsed["coordinates"].([]any); ok {
					fileEntry["coordinateCount"] = len(coords)
				}
				if tm, ok := parsed["totalMarkers"]; ok {
					fileEntry["markerCount"] = tm
				} else if markers, ok := parsed["markers"].([]any); ok {
					fileEntry["markerCount"] = len(markers)
				}
			}
		}

		files = append(files, fileEntry)
	}

	util.JSON(w, http.StatusOK, files)
}

// GetFile reads and returns a recording JSON file.
func (h *RecordingHandler) GetFile(w http.ResponseWriter, r *http.Request) {
	filename := r.URL.Query().Get("filename")
	if filename == "" {
		util.Error(w, http.StatusBadRequest, "Filename is required")
		return
	}

	// Prevent directory traversal
	filename = filepath.Base(filename)
	filePath := filepath.Join(recordingDataDir(), filename)

	data, err := os.ReadFile(filePath)
	if err != nil {
		if os.IsNotExist(err) {
			util.Error(w, http.StatusNotFound, "File not found")
		} else {
			util.Error(w, http.StatusInternalServerError, "Could not read file")
		}
		return
	}

	// Return as raw JSON
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	w.Write(data)
}

// CheckExisting checks if any incomplete recording file exists for a timetable ID.
func (h *RecordingHandler) CheckExisting(w http.ResponseWriter, r *http.Request) {
	timetableID := chi.URLParam(r, "timetableId")

	dir := recordingDataDir()
	if _, err := os.Stat(dir); os.IsNotExist(err) {
		util.JSON(w, http.StatusOK, map[string]any{"exists": false})
		return
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		util.JSON(w, http.StatusOK, map[string]any{"exists": false})
		return
	}

	// Look for the first JSON file (there will only ever be one)
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}

		filePath := filepath.Join(dir, entry.Name())
		data, err := os.ReadFile(filePath)
		if err != nil {
			continue
		}

		var parsed map[string]any
		if json.Unmarshal(data, &parsed) != nil {
			continue
		}

		// Skip completed files
		if completed, ok := parsed["completed"].(bool); ok && completed {
			util.JSON(w, http.StatusOK, map[string]any{"exists": false})
			return
		}

		// Check if timetableId matches (for informational purposes, but return the file regardless)
		_ = timetableID

		coordCount := 0
		if coords, ok := parsed["coordinates"].([]any); ok {
			coordCount = len(coords)
		}
		markerCount := 0
		if markers, ok := parsed["markers"].([]any); ok {
			markerCount = len(markers)
		}
		ttCount := 0
		if tt, ok := parsed["timetable"].([]any); ok {
			ttCount = len(tt)
		}

		util.JSON(w, http.StatusOK, map[string]any{
			"exists":              true,
			"filename":            entry.Name(),
			"routeName":           parsed["routeName"],
			"coordinateCount":     coordCount,
			"markerCount":         markerCount,
			"timetableEntryCount": ttCount,
			"completed":           false,
		})
		return
	}

	util.JSON(w, http.StatusOK, map[string]any{"exists": false})
}

// CheckAnyExisting checks if any incomplete recording files exist.
func (h *RecordingHandler) CheckAnyExisting(w http.ResponseWriter, r *http.Request) {
	dir := recordingDataDir()
	if _, err := os.Stat(dir); os.IsNotExist(err) {
		util.JSON(w, http.StatusOK, map[string]any{"exists": false})
		return
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		util.JSON(w, http.StatusOK, map[string]any{"exists": false})
		return
	}

	// Look for the first JSON file
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}

		filePath := filepath.Join(dir, entry.Name())
		data, err := os.ReadFile(filePath)
		if err != nil {
			continue
		}

		var parsed map[string]any
		if json.Unmarshal(data, &parsed) != nil {
			continue
		}

		// Skip completed files
		if completed, ok := parsed["completed"].(bool); ok && completed {
			util.JSON(w, http.StatusOK, map[string]any{"exists": false})
			return
		}

		coordCount := 0
		if coords, ok := parsed["coordinates"].([]any); ok {
			coordCount = len(coords)
		}
		markerCount := 0
		if markers, ok := parsed["markers"].([]any); ok {
			markerCount = len(markers)
		}
		ttCount := 0
		if tt, ok := parsed["timetable"].([]any); ok {
			ttCount = len(tt)
		}

		util.JSON(w, http.StatusOK, map[string]any{
			"exists":              true,
			"filename":            entry.Name(),
			"timetableId":         parsed["timetableId"],
			"routeName":           parsed["routeName"],
			"serviceName":         parsed["serviceName"],
			"coordinateCount":     coordCount,
			"markerCount":         markerCount,
			"timetableEntryCount": ttCount,
			"completed":           false,
		})
		return
	}

	util.JSON(w, http.StatusOK, map[string]any{"exists": false})
}

// LoadFile loads a recording file back into memory state.
func (h *RecordingHandler) LoadFile(w http.ResponseWriter, r *http.Request) {
	// Accept filename from query param or JSON body
	fname := r.URL.Query().Get("file")
	if fname == "" {
		var body struct {
			Filename string `json:"filename"`
		}
		json.NewDecoder(r.Body).Decode(&body)
		fname = body.Filename
	}

	if fname == "" {
		util.Error(w, http.StatusBadRequest, "Filename is required")
		return
	}

	// Prevent directory traversal
	filename := filepath.Base(fname)
	filePath := filepath.Join(recordingDataDir(), filename)

	data, err := os.ReadFile(filePath)
	if err != nil {
		if os.IsNotExist(err) {
			util.Error(w, http.StatusNotFound, "File not found")
		} else {
			util.Error(w, http.StatusInternalServerError, "Could not read file: "+err.Error())
		}
		return
	}

	var parsed map[string]any
	if err := json.Unmarshal(data, &parsed); err != nil {
		util.Error(w, http.StatusInternalServerError, "Could not parse file: "+err.Error())
		return
	}

	// Extract timetable ID
	timetableID := 0
	if tid, ok := parsed["timetableId"].(float64); ok {
		timetableID = int(tid)
	}
	if timetableID == 0 {
		util.Error(w, http.StatusBadRequest, "Could not determine timetable ID from file")
		return
	}

	h.mu.Lock()
	defer h.mu.Unlock()

	h.timetableID = timetableID
	h.outputFile = filePath

	// Load coordinates
	h.coordinates = make([]Coordinate, 0)
	if coordsRaw, ok := parsed["coordinates"].([]any); ok {
		for _, c := range coordsRaw {
			if cm, ok := c.(map[string]any); ok {
				lat, _ := cm["latitude"].(float64)
				lng, _ := cm["longitude"].(float64)
				coord := Coordinate{Latitude: lat, Longitude: lng}
				if x, ok := cm["x"].(float64); ok {
					xi := int(x)
					coord.TileX = &xi
				}
				if y, ok := cm["y"].(float64); ok {
					yi := int(y)
					coord.TileY = &yi
				}
				h.coordinates = append(h.coordinates, coord)
			}
		}
	}

	// Load markers
	h.markers = make([]Marker, 0)
	if markersRaw, ok := parsed["markers"].([]any); ok {
		for _, m := range markersRaw {
			if mm, ok := m.(map[string]any); ok {
				marker := Marker{}
				if sn, ok := mm["stationName"].(string); ok {
					marker.StationName = sn
				}
				if mt, ok := mm["markerType"].(string); ok {
					marker.MarkerType = mt
				}
				if lat, ok := mm["latitude"].(float64); ok {
					marker.Lat = lat
				}
				if lng, ok := mm["longitude"].(float64); ok {
					marker.Lng = lng
				}
				if pl, ok := mm["platformLength"].(float64); ok {
					marker.PlatformLength = pl
				}
				h.markers = append(h.markers, marker)
			}
		}
	}

	// Load saved timetable coordinates
	h.savedCoords = make(map[int]Coordinate)
	if ttRaw, ok := parsed["timetable"].([]any); ok {
		for idx, entry := range ttRaw {
			if em, ok := entry.(map[string]any); ok {
				lat, hasLat := em["latitude"].(float64)
				lng, hasLng := em["longitude"].(float64)
				if hasLat && hasLng {
					coord := Coordinate{Latitude: lat, Longitude: lng}
					if x, ok := em["x"].(float64); ok {
						xi := int(x)
						coord.TileX = &xi
					}
					if y, ok := em["y"].(float64); ok {
						yi := int(y)
						coord.TileY = &yi
					}
					h.savedCoords[idx] = coord
				}
			}
		}
	}

	// Cache preprocessed entries for stop detection
	h.cachedEntries = h.preprocessTimetableEntries(timetableID)
	h.lastTimetableTimeSec = calculateLastTimetableTime(h.cachedEntries)

	// Load config for stop detection threshold
	cfg := config.Get()
	minStopSec := cfg.MinStopDurationSeconds
	if minStopSec <= 0 {
		minStopSec = 30
	}
	h.stopSpeedThreshold = 2
	h.stopMovingReadings = 10
	h.stopConfirmedReadings = minStopSec * 10
	autoStopSec := cfg.AutoStopTimeoutSeconds
	if autoStopSec <= 0 {
		autoStopSec = 120
	}
	h.autoStopTimeoutMs = autoStopSec * 1000
	h.saveFrequency = cfg.SaveFrequency
	if h.saveFrequency <= 0 {
		h.saveFrequency = 1
	}

	// Reset stop detection counters
	h.stoppedCount = 0
	h.movingCount = 0
	h.isStopped = false
	h.autoStopped = false
	h.gameTimeSec = nil
	h.currentPosition = nil
	h.processedMarkerNames = make(map[string]bool)
	for _, m := range h.markers {
		h.processedMarkerNames[m.StationName] = true
	}

	// Set state to paused (ready to resume)
	h.isRecording = true
	h.isPaused = true

	log.Printf("Loaded recording file: %s (coords=%d, markers=%d, stations=%d)",
		filename, len(h.coordinates), len(h.markers), len(h.savedCoords))

	util.JSON(w, http.StatusOK, map[string]any{
		"success":           true,
		"message":           "Recording file loaded",
		"timetableId":       timetableID,
		"filename":          filename,
		"coordinateCount":   len(h.coordinates),
		"markerCount":       len(h.markers),
		"savedStationCount": len(h.savedCoords),
	})
}

// DeleteFile deletes a recording file.
func (h *RecordingHandler) DeleteFile(w http.ResponseWriter, r *http.Request) {
	filename := chi.URLParam(r, "filename")
	if filename == "" {
		util.Error(w, http.StatusBadRequest, "Filename is required")
		return
	}

	// Prevent directory traversal
	filename = filepath.Base(filename)
	filePath := filepath.Join(recordingDataDir(), filename)

	if _, err := os.Stat(filePath); os.IsNotExist(err) {
		// File already deleted or doesn't exist - that's ok
		util.JSON(w, http.StatusOK, map[string]any{
			"success": true,
			"message": "File not found (may already be deleted)",
		})
		return
	}

	if err := os.Remove(filePath); err != nil {
		util.Error(w, http.StatusInternalServerError, err.Error())
		return
	}

	util.JSON(w, http.StatusOK, map[string]any{
		"success": true,
		"message": "File deleted",
	})
}

// SetMode sets the recording mode (manual or automatic).
func (h *RecordingHandler) SetMode(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Mode string `json:"mode"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		util.Error(w, http.StatusBadRequest, "Invalid JSON: "+err.Error())
		return
	}

	if body.Mode != "manual" && body.Mode != "automatic" {
		util.Error(w, http.StatusBadRequest, `Invalid mode. Must be "manual" or "automatic"`)
		return
	}

	h.mu.Lock()
	h.mode = body.Mode
	h.mu.Unlock()

	util.JSON(w, http.StatusOK, map[string]any{
		"success":         true,
		"mode":            body.Mode,
		"autoStopEnabled": body.Mode == "automatic",
		"message":         fmt.Sprintf("Recording mode set to %s", body.Mode),
	})
}

// GetMode returns the current recording mode.
func (h *RecordingHandler) GetMode(w http.ResponseWriter, r *http.Request) {
	h.mu.Lock()
	mode := h.mode
	h.mu.Unlock()

	if mode == "" {
		mode = "automatic"
	}

	util.JSON(w, http.StatusOK, map[string]any{
		"mode":            mode,
		"autoStopEnabled": mode == "automatic",
	})
}

// GetStreamState returns recording state to be merged into the SSE stream data.
// Returns nil if not recording.
func (h *RecordingHandler) GetStreamState() map[string]any {
	h.mu.Lock()
	defer h.mu.Unlock()

	if !h.isRecording && !h.isPaused {
		return nil
	}

	// Build timetable from cached entries (preprocessed) with saved coordinates
	var timetable []map[string]any
	if len(h.cachedEntries) > 0 {
		for i, entry := range h.cachedEntries {
			te := map[string]any{
				"index":     entry.Index,
				"location":  entry.Location,
				"arrival":   entry.Arrival,
				"departure": entry.Departure,
				"structure_number": entry.StructureNumber,
				"apiName":   entry.ApiName,
			}
			if entry.IsPassThrough {
				te["isPassThrough"] = true
			}
			if saved, ok := h.savedCoords[i]; ok {
				te["latitude"] = saved.Latitude
				te["longitude"] = saved.Longitude
				if saved.TileX != nil {
					te["x"] = *saved.TileX
				}
				if saved.TileY != nil {
					te["y"] = *saved.TileY
				}
			}
			timetable = append(timetable, te)
		}
	} else if h.timetableID > 0 {
		// Fallback: query DB directly if cachedEntries not available
		rows, err := h.db.Query(`
			SELECT te.id, COALESCE(l.name, '') as location, te.structure_number,
				te.time1, te.time2, COALESCE(ta.name, '') as action, te.api_name
			FROM timetable_entries te
			LEFT JOIN timetable_actions ta ON ta.id = te.action_id
			LEFT JOIN locations l ON l.id = te.location_id
			WHERE te.timetable_id = ?
			ORDER BY te.sort_order`, h.timetableID)
		if err == nil {
			defer rows.Close()
			idx := 0
			for rows.Next() {
				var id int
				var location, structureNumber, action string
				var time1, time2, apiName *string
				rows.Scan(&id, &location, &structureNumber, &time1, &time2, &action, &apiName)

				entry := map[string]any{
					"index":            idx,
					"location":         location,
					"structure_number": structureNumber,
					"arrival":  time1,
					"departure": time2,
					"action":   action,
					"apiName":  apiName,
				}

				if saved, ok := h.savedCoords[idx]; ok {
					entry["latitude"] = saved.Latitude
					entry["longitude"] = saved.Longitude
					if saved.TileX != nil {
						entry["x"] = *saved.TileX
					}
					if saved.TileY != nil {
						entry["y"] = *saved.TileY
					}
				}

				timetable = append(timetable, entry)
				idx++
			}
		}
	}

	// Check if auto-stop was triggered (one-time notification)
	wasAutoStopped := h.autoStopped
	if h.autoStopped {
		h.autoStopped = false
	}

	// Stop detection stats
	totalStops := 0
	for _, e := range h.cachedEntries {
		if !e.IsPassThrough {
			totalStops++
		}
	}

	return map[string]any{
		"isRecording":     h.isRecording,
		"isPaused":        h.isPaused,
		"timetableId":     h.timetableID,
		"coordinateCount": len(h.coordinates),
		"markerCount":     len(h.markers),
		"timetable":       timetable,
		"autoStopped":     wasAutoStopped,
		"autoStopEnabled": h.autoStopEnabled,
		"stopDetection": map[string]any{
			"totalStops":    totalStops,
			"recordedStops": len(h.savedCoords),
		},
	}
}

// ProcessCoordinate adds a GPS coordinate to the active recording.
// Called by the telemetry stream, not an HTTP handler.
func (h *RecordingHandler) ProcessCoordinate(lat, lng, speed float64, gameTimeISO string, tileX, tileY *int) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if !h.isRecording || h.isPaused {
		return
	}

	// Update game time from ISO string
	if gameTimeISO != "" {
		h.gameTimeSec = isoTimeToSeconds(gameTimeISO)
	}

	// Skip stale Chatham position
	if lat == 51.380108707397724 && lng == 0.5219243867730494 {
		return
	}
	if lat == 0 && lng == 0 {
		return
	}

	// Copy tile pointers so their memory is stable for downstream consumers.
	var tx, ty *int
	if tileX != nil {
		v := *tileX
		tx = &v
	}
	if tileY != nil {
		v := *tileY
		ty = &v
	}

	// Update current position/tile for marker processing
	h.currentPosition = &Coordinate{Latitude: lat, Longitude: lng, TileX: tx, TileY: ty}
	h.currentTileX = tx
	h.currentTileY = ty

	// Only add if coordinate is different from last one
	isDuplicate := false
	if len(h.coordinates) > 0 {
		last := h.coordinates[len(h.coordinates)-1]
		if last.Latitude == lat && last.Longitude == lng {
			isDuplicate = true
		}
	}

	if !isDuplicate {
		// Update last unique coordinate time for auto-stop tracking
		h.lastUniqueCoordTime = time.Now()

		h.coordinates = append(h.coordinates, Coordinate{Latitude: lat, Longitude: lng, TileX: tx, TileY: ty})

		// Save to file periodically based on save frequency setting
		if h.saveFrequency > 0 && len(h.coordinates)%h.saveFrequency == 0 {
			h.saveRecordingFile()
		}
	}

	// === Automatic stop detection for coordinate assignment ===
	if h.autoStopEnabled && len(h.cachedEntries) > 0 {
		if speed < h.stopSpeedThreshold {
			// Train appears stopped
			h.movingCount = 0
			h.stoppedCount++

			if h.stoppedCount >= h.stopConfirmedReadings && !h.isStopped {
				// Stop confirmed - assign coordinates to next unrecorded non-passthrough entry
				h.isStopped = true

				nextIdx := h.findNextUnrecordedStopIndex()
				if nextIdx >= 0 {
					entry := h.cachedEntries[nextIdx]
					h.savedCoords[nextIdx] = Coordinate{
						Latitude:  lat,
						Longitude: lng,
						TileX:     tx,
						TileY:     ty,
					}

					// Persist to file
					h.saveRecordingFile()

					log.Printf("[Auto-coord] Stop detected - assigned to %q (index %d): %.6f, %.6f",
						entry.Location, nextIdx, lat, lng)

					// Check if all stops are now recorded
					h.checkAllStopsRecorded()
				} else {
					log.Printf("[Auto-coord] Stop detected but all timetable entries already have coordinates")
				}
			}
		} else {
			// Train is moving
			h.stoppedCount = 0
			h.movingCount++

			if h.movingCount >= h.stopMovingReadings && h.isStopped {
				// Movement confirmed - ready for next stop
				h.isStopped = false
			}
		}
	}

	// Check auto-stop conditions periodically (every ~300 readings = ~30sec)
	if h.autoStopEnabled && h.stoppedCount > 0 && h.stoppedCount%300 == 0 {
		h.checkAutoStop()
	}
}

// ProcessMarker adds or updates a discovered station marker in the active recording.
// Called by the telemetry stream, not an HTTP handler.
// station is the raw station/marker map from telemetry, distanceCM is the distance in centimeters.
func (h *RecordingHandler) ProcessMarker(station map[string]any, distanceCM float64) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if !h.isRecording || h.isPaused || h.currentPosition == nil {
		return
	}

	markerName, _ := station["stationName"].(string)
	if markerName == "" {
		markerName, _ = station["markerName"].(string)
	}
	if markerName == "" {
		return
	}

	distanceMeters := distanceCM / 100.0

	// Check if marker already exists
	for i := range h.markers {
		if h.markers[i].StationName == markerName {
			// Update onspot data (continuously updated as we get closer)
			lat := h.currentPosition.Latitude
			lng := h.currentPosition.Longitude
			h.markers[i].OnspotLatitude = &lat
			h.markers[i].OnspotLongitude = &lng
			h.markers[i].OnspotTimestamp = time.Now().UTC().Format(time.RFC3339)
			h.markers[i].OnspotDistance = distanceMeters
			return
		}
	}

	// First time seeing this marker - record initial detection data
	markerType, _ := station["markerType"].(string)
	if markerType == "" {
		markerType = "Station"
	}
	platformLen, _ := station["platformLength"].(float64)

	curLat := h.currentPosition.Latitude
	curLng := h.currentPosition.Longitude

	marker := Marker{
		StationName: markerName,
		MarkerType:  markerType,
		DetectedAt: &Coordinate{
			Latitude:  curLat,
			Longitude: curLng,
		},
		DistanceAheadM:  distanceMeters,
		Timestamp:       time.Now().UTC().Format(time.RFC3339),
		OnspotLatitude:  &curLat,
		OnspotLongitude: &curLng,
		OnspotTimestamp:  time.Now().UTC().Format(time.RFC3339),
		OnspotDistance:   distanceMeters,
	}

	if platformLen > 0 {
		marker.PlatformLength = platformLen
	}

	h.markers = append(h.markers, marker)
	h.processedMarkerNames[markerName] = true
	log.Printf("Found marker: %s (%.0fm ahead)", markerName, distanceMeters)
}

// IsRecordingActive returns true if a recording is in progress.
func (h *RecordingHandler) IsRecordingActive() bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.isRecording
}

