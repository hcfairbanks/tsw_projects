package handler

import (
	"fmt"
	"net/http"
	"strings"

	"hud-go/internal/tsw"
	"hud-go/internal/util"
)

// validWeatherKeys are the accepted weather parameter keys (case-insensitive lookup).
var validWeatherKeys = map[string]bool{
	"temperature":   true,
	"cloudiness":    true,
	"precipitation": true,
	"wetness":       true,
	"groundsnow":    true,
	"piledsnow":     true,
	"fogdensity":    true,
	"reset":         true,
}

// WeatherHandler handles weather control endpoints via the TSW CommAPI.
type WeatherHandler struct {
	client *tsw.Client
}

// Set sends a weather parameter change to the TSW CommAPI.
// Frontend calls: PATCH /api/weather/set?key=Temperature&value=25
// TSW API:        PATCH /set/WeatherManager.Temperature?value=25
func (h *WeatherHandler) Set(w http.ResponseWriter, r *http.Request) {
	key := r.URL.Query().Get("key")
	valueStr := r.URL.Query().Get("value")

	if key == "" {
		util.Error(w, http.StatusBadRequest, "key is required")
		return
	}

	if valueStr == "" {
		util.Error(w, http.StatusBadRequest, "value is required")
		return
	}

	// Validate key (case-insensitive)
	if !validWeatherKeys[strings.ToLower(key)] {
		util.Error(w, http.StatusBadRequest, fmt.Sprintf("unknown weather key: %s", key))
		return
	}

	// Send PATCH to TSW API: /set/WeatherManager.{Key}?value={value}
	// Pass the raw value string to avoid float formatting issues
	path := fmt.Sprintf("/set/WeatherManager.%s?value=%s", key, valueStr)
	_, err := h.client.Do("PATCH", path, "")
	if err != nil {
		util.Error(w, http.StatusInternalServerError, err.Error())
		return
	}

	util.Success(w, map[string]any{"success": true, "key": key, "value": valueStr})
}
