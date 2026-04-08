package handler

import (
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"strings"
	"time"

	"hud-go/internal/tsw"
	"hud-go/internal/util"
)

// HistoricalWeatherHandler fetches past weather from the Open-Meteo archive API
// and maps it to TSW weather parameters using the player's GPS position and
// the in-game time of day.
type HistoricalWeatherHandler struct {
	client     *tsw.Client
	httpClient *http.Client
}

// openMeteoArchiveResponse matches the Open-Meteo archive API JSON structure.
// The archive endpoint only returns hourly data (no "current" block).
type openMeteoArchiveResponse struct {
	Hourly struct {
		Time          []string  `json:"time"`
		Temperature2m []float64 `json:"temperature_2m"`
		SnowDepth     []float64 `json:"snow_depth"`
		Snowfall      []float64 `json:"snowfall"`
		Showers       []float64 `json:"showers"`
		Rain          []float64 `json:"rain"`
		CloudCover    []float64 `json:"cloud_cover"`
		Precipitation []float64 `json:"precipitation"`
	} `json:"hourly"`
}

// Apply fetches historical weather for the given date at the game's current
// time of day, maps it to TSW values, and applies them.
// Frontend calls: POST /api/weather/historical/apply?date=2024-01-15
func (h *HistoricalWeatherHandler) Apply(w http.ResponseWriter, r *http.Request) {
	dateStr := r.URL.Query().Get("date")
	if dateStr == "" {
		util.Error(w, http.StatusBadRequest, "date is required (YYYY-MM-DD)")
		return
	}

	// Validate date format
	if _, err := time.Parse("2006-01-02", dateStr); err != nil {
		util.Error(w, http.StatusBadRequest, "invalid date format, use YYYY-MM-DD")
		return
	}

	lat, lng, err := h.getPlayerPosition()
	if err != nil {
		util.Error(w, http.StatusBadRequest, err.Error())
		return
	}

	gameTime := h.getGameTime()

	archive, err := h.fetchArchive(lat, lng, dateStr)
	if err != nil {
		util.Error(w, http.StatusBadGateway, "failed to fetch historical weather: "+err.Error())
		return
	}

	idx := findClosestHourIndex(archive, gameTime.Hour)
	mapped := mapArchiveToTSW(archive, idx)

	// Apply each weather parameter to TSW
	params := []struct {
		key   string
		value float64
	}{
		{"Temperature", mapped.Temperature},
		{"Cloudiness", mapped.Cloudiness},
		{"Precipitation", mapped.Precipitation},
		{"Wetness", mapped.Wetness},
		{"GroundSnow", mapped.GroundSnow},
		{"PiledSnow", mapped.PiledSnow},
		{"FogDensity", mapped.FogDensity},
	}

	applied := 0
	for _, p := range params {
		path := fmt.Sprintf("/set/WeatherManager.%s?value=%v", p.key, p.value)
		_, err := h.client.Do("PATCH", path, "")
		if err == nil {
			applied++
		}
	}

	matchedTime := ""
	if idx < len(archive.Hourly.Time) {
		matchedTime = archive.Hourly.Time[idx]
	}

	util.Success(w, map[string]any{
		"latitude":     lat,
		"longitude":    lng,
		"date":         dateStr,
		"game_time":    gameTime.TimeStr,
		"matched_time": matchedTime,
		"weather":      mapped,
		"applied":      applied,
		"total":        len(params),
		"source":       "open-meteo-archive",
	})
}

// Fetch returns historical weather data without applying it.
func (h *HistoricalWeatherHandler) Fetch(w http.ResponseWriter, r *http.Request) {
	dateStr := r.URL.Query().Get("date")
	if dateStr == "" {
		util.Error(w, http.StatusBadRequest, "date is required (YYYY-MM-DD)")
		return
	}

	if _, err := time.Parse("2006-01-02", dateStr); err != nil {
		util.Error(w, http.StatusBadRequest, "invalid date format, use YYYY-MM-DD")
		return
	}

	lat, lng, err := h.getPlayerPosition()
	if err != nil {
		util.Error(w, http.StatusBadRequest, err.Error())
		return
	}

	gameTime := h.getGameTime()

	archive, err := h.fetchArchive(lat, lng, dateStr)
	if err != nil {
		util.Error(w, http.StatusBadGateway, "failed to fetch historical weather: "+err.Error())
		return
	}

	idx := findClosestHourIndex(archive, gameTime.Hour)
	mapped := mapArchiveToTSW(archive, idx)

	matchedTime := ""
	if idx < len(archive.Hourly.Time) {
		matchedTime = archive.Hourly.Time[idx]
	}

	util.Success(w, map[string]any{
		"latitude":     lat,
		"longitude":    lng,
		"date":         dateStr,
		"game_time":    gameTime.TimeStr,
		"matched_time": matchedTime,
		"weather":      mapped,
		"source":       "open-meteo-archive",
	})
}

// getPlayerPosition reads the player's lat/lng from the latest TSW telemetry.
func (h *HistoricalWeatherHandler) getPlayerPosition() (float64, float64, error) {
	data := h.client.GetLastData()
	pos, ok := data["playerPosition"].(map[string]any)
	if !ok || pos == nil {
		return 0, 0, fmt.Errorf("no player position available — is the game running?")
	}

	lat, _ := pos["latitude"].(float64)
	lng, _ := pos["longitude"].(float64)
	if lat == 0 && lng == 0 {
		return 0, 0, fmt.Errorf("player position is (0,0) — waiting for valid GPS data")
	}

	return lat, lng, nil
}

// gameTimeInfo holds both the parsed hour and the original time string for display.
type gameTimeInfo struct {
	Hour     int
	TimeStr  string // e.g. "09:27" for display
}

// getGameTime extracts the time from the game's in-game clock.
// Falls back to current real-world time if game time is unavailable.
func (h *HistoricalWeatherHandler) getGameTime() gameTimeInfo {
	data := h.client.GetLastData()
	if lt, ok := data["localTime"].(string); ok && lt != "" {
		// localTime is ISO8601, e.g. "2024-01-15T14:30:00"
		for _, layout := range []string{
			time.RFC3339,
			"2006-01-02T15:04:05",
			"2006-01-02T15:04",
		} {
			if t, err := time.Parse(layout, lt); err == nil {
				return gameTimeInfo{Hour: t.Hour(), TimeStr: t.Format("15:04")}
			}
		}
		// Try parsing just the time portion after T
		if idx := strings.Index(lt, "T"); idx >= 0 {
			timePart := lt[idx+1:]
			for _, layout := range []string{"15:04:05", "15:04"} {
				if t, err := time.Parse(layout, timePart); err == nil {
					return gameTimeInfo{Hour: t.Hour(), TimeStr: t.Format("15:04")}
				}
			}
		}
	}
	// Fallback to real-world time
	now := time.Now()
	return gameTimeInfo{Hour: now.Hour(), TimeStr: now.Format("15:04")}
}

// fetchArchive calls the Open-Meteo archive API for a specific date.
func (h *HistoricalWeatherHandler) fetchArchive(lat, lng float64, date string) (*openMeteoArchiveResponse, error) {
	url := fmt.Sprintf(
		"https://archive-api.open-meteo.com/v1/archive?latitude=%.6f&longitude=%.6f"+
			"&start_date=%s&end_date=%s"+
			"&hourly=temperature_2m,snow_depth,snowfall,showers,rain,cloud_cover,precipitation"+
			"&timezone=auto",
		lat, lng, date, date,
	)

	resp, err := h.httpClient.Get(url)
	if err != nil {
		return nil, fmt.Errorf("HTTP request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("Open-Meteo returned %d: %s", resp.StatusCode, string(body))
	}

	var archive openMeteoArchiveResponse
	if err := json.NewDecoder(resp.Body).Decode(&archive); err != nil {
		return nil, fmt.Errorf("failed to parse response: %w", err)
	}

	return &archive, nil
}

// findClosestHourIndex finds the hourly index closest to the given hour (0-23).
func findClosestHourIndex(archive *openMeteoArchiveResponse, targetHour int) int {
	if len(archive.Hourly.Time) == 0 {
		return 0
	}

	bestIdx := 0
	bestDiff := 24

	for i, ts := range archive.Hourly.Time {
		t, err := time.Parse("2006-01-02T15:04", ts)
		if err != nil {
			continue
		}
		diff := absDiffInt(t.Hour(), targetHour)
		if diff < bestDiff {
			bestDiff = diff
			bestIdx = i
		}
	}

	return bestIdx
}

// mapArchiveToTSW converts historical hourly data at a given index into TSW values.
// TSW only renders precipitation as snow when temperature <= 0C, so when the real
// weather had snowfall we cap the temperature accordingly.
func mapArchiveToTSW(archive *openMeteoArchiveResponse, idx int) tswWeatherValues {
	h := archive.Hourly

	temperature := safeIndex(h.Temperature2m, idx)
	cloudCover := safeIndex(h.CloudCover, idx)
	rain := safeIndex(h.Rain, idx)
	showers := safeIndex(h.Showers, idx)
	snowfall := safeIndex(h.Snowfall, idx)
	snowDepth := safeIndex(h.SnowDepth, idx)
	precip := safeIndex(h.Precipitation, idx)

	isSnowing := snowfall > 0
	isRaining := rain > 0 || showers > 0

	// Temperature: if it was snowing in reality, cap at -0.5C so TSW renders snow.
	// TSW's rain/snow threshold is 0C.
	if isSnowing && temperature > -0.5 {
		temperature = -0.5
	}

	// Cloudiness: 0-100% → 0-1
	cloudiness := clampF(cloudCover/100.0, 0, 1)

	// Precipitation: TSW renders this as rain (above 0C) or snow (at/below 0C).
	// Use rain+showers when raining, snowfall intensity when snowing, or both.
	var precipitation float64
	if isSnowing && !isRaining {
		// Pure snow: use snowfall intensity (cm/hr). 2cm+ = heavy snow = 1.0
		precipitation = clampF(snowfall/2.0, 0, 1)
	} else if isRaining && !isSnowing {
		// Pure rain: use rain+showers (mm/hr). 5mm+ = heavy rain = 1.0
		precipitation = clampF((rain+showers)/5.0, 0, 1)
	} else if isSnowing && isRaining {
		// Mixed: use total precipitation. Scale 5mm+ = 1.0
		precipitation = clampF(precip/5.0, 0, 1)
	}

	// Wetness: from rain/liquid precipitation only (snow doesn't make surfaces wet immediately)
	wetness := clampF((rain + showers) / 3.0, 0, 1)

	// GroundSnow: snow_depth in meters. 0.10m (10cm) = full coverage.
	// Even a few cm of snow covers the ground visually.
	groundSnow := clampF((snowDepth/0.10)*3, 0, 1)
	if isSnowing && groundSnow < 0.15 {
		groundSnow = 0.15
	}

	// PiledSnow: tracks ground snow — snow piles up everywhere
	piledSnow := groundSnow

	// FogDensity: slight fog when very overcast + precip
	fogDensity := 0.0
	if cloudiness > 0.9 && precipitation > 0 {
		fogDensity = 0.1
	}

	return tswWeatherValues{
		Temperature:   math.Round(temperature*10) / 10,
		Cloudiness:    math.Round(cloudiness*100) / 100,
		Precipitation: math.Round(precipitation*100) / 100,
		Wetness:       math.Round(wetness*100) / 100,
		GroundSnow:    math.Round(groundSnow*100) / 100,
		PiledSnow:     math.Round(piledSnow*100) / 100,
		FogDensity:    math.Round(fogDensity*100) / 100,
	}
}

// safeIndex returns the value at idx, or 0 if out of bounds.
func safeIndex(s []float64, idx int) float64 {
	if idx >= 0 && idx < len(s) {
		return s[idx]
	}
	return 0
}

// absDiffInt returns the absolute difference between two ints.
func absDiffInt(a, b int) int {
	if a > b {
		return a - b
	}
	return b - a
}
