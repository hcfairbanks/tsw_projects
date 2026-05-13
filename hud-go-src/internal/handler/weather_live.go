package handler

import (
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"time"

	"hud-go/internal/tsw"
	"hud-go/internal/util"
)

// LiveWeatherHandler fetches real-world weather from Open-Meteo and maps it
// to TSW weather parameters using the player's current GPS position.
type LiveWeatherHandler struct {
	client     *tsw.Client
	httpClient *http.Client
}

// openMeteoResponse matches the Open-Meteo API JSON structure.
type openMeteoResponse struct {
	Current struct {
		Time          string  `json:"time"`
		Temperature2m float64 `json:"temperature_2m"`
		Rain          float64 `json:"rain"`
		Precipitation float64 `json:"precipitation"`
		Showers       float64 `json:"showers"`
		Snowfall      float64 `json:"snowfall"`
		CloudCover    float64 `json:"cloud_cover"`
	} `json:"current"`
	Hourly struct {
		Time                     []string  `json:"time"`
		Temperature2m            []float64 `json:"temperature_2m"`
		SnowDepth                []float64 `json:"snow_depth"`
		Snowfall                 []float64 `json:"snowfall"`
		Showers                  []float64 `json:"showers"`
		Rain                     []float64 `json:"rain"`
		PrecipitationProbability []float64 `json:"precipitation_probability"`
	} `json:"hourly"`
}

// tswWeatherValues holds the mapped weather values ready for TSW.
type tswWeatherValues struct {
	Temperature   float64 `json:"temperature"`
	Cloudiness    float64 `json:"cloudiness"`
	Precipitation float64 `json:"precipitation"`
	Wetness       float64 `json:"wetness"`
	GroundSnow    float64 `json:"ground_snow"`
	PiledSnow     float64 `json:"piled_snow"`
	FogDensity    float64 `json:"fog_density"`
}

// Fetch gets the current real-world weather for the player's position,
// maps it to TSW values, and returns them without applying.
func (h *LiveWeatherHandler) Fetch(w http.ResponseWriter, r *http.Request) {
	lat, lng, err := h.getPlayerPosition()
	if err != nil {
		util.Error(w, http.StatusBadRequest, err.Error())
		return
	}

	meteo, err := h.fetchOpenMeteo(lat, lng)
	if err != nil {
		util.Error(w, http.StatusBadGateway, "failed to fetch weather: "+err.Error())
		return
	}

	mapped := mapToTSW(meteo)

	util.Success(w, map[string]any{
		"latitude":  lat,
		"longitude": lng,
		"weather":   mapped,
		"source":    "open-meteo",
	})
}

// Apply fetches real-world weather and immediately applies it to TSW.
func (h *LiveWeatherHandler) Apply(w http.ResponseWriter, r *http.Request) {
	lat, lng, err := h.getPlayerPosition()
	if err != nil {
		util.Error(w, http.StatusBadRequest, err.Error())
		return
	}

	meteo, err := h.fetchOpenMeteo(lat, lng)
	if err != nil {
		util.Error(w, http.StatusBadGateway, "failed to fetch weather: "+err.Error())
		return
	}

	mapped := mapToTSW(meteo)

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

	util.Success(w, map[string]any{
		"latitude":  lat,
		"longitude": lng,
		"weather":   mapped,
		"applied":   applied,
		"total":     len(params),
		"source":    "open-meteo",
	})
}

// getPlayerPosition reads the player's lat/lng from the latest TSW telemetry.
func (h *LiveWeatherHandler) getPlayerPosition() (float64, float64, error) {
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

// fetchOpenMeteo calls the Open-Meteo free API for the given coordinates.
func (h *LiveWeatherHandler) fetchOpenMeteo(lat, lng float64) (*openMeteoResponse, error) {
	url := fmt.Sprintf(
		"https://api.open-meteo.com/v1/forecast?latitude=%.6f&longitude=%.6f"+
			"&hourly=temperature_2m,snow_depth,snowfall,showers,rain,precipitation_probability"+
			"&current=temperature_2m,rain,precipitation,showers,snowfall,cloud_cover"+
			"&timezone=auto&forecast_days=1",
		lat, lng,
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

	var meteo openMeteoResponse
	if err := json.NewDecoder(resp.Body).Decode(&meteo); err != nil {
		return nil, fmt.Errorf("failed to parse response: %w", err)
	}

	return &meteo, nil
}

// mapToTSW converts Open-Meteo weather data into TSW weather parameter values.
// TSW only renders precipitation as snow when temperature <= 0C, so when the real
// weather has snowfall we cap the temperature accordingly.
func mapToTSW(meteo *openMeteoResponse) tswWeatherValues {
	cur := meteo.Current

	temperature := cur.Temperature2m
	isSnowing := cur.Snowfall > 0
	isRaining := cur.Rain > 0 || cur.Showers > 0

	// If it's snowing in reality, cap temp at -0.5C so TSW renders snow (threshold is 0C)
	if isSnowing && temperature > -0.5 {
		temperature = -0.5
	}

	// Cloudiness: cloud_cover is 0-100%, TSW wants 0.0-1.0
	cloudiness := clampF(cur.CloudCover/100.0, 0, 1)

	// Precipitation: TSW renders as rain (>0C) or snow (<=0C).
	var precipitation float64
	if isSnowing && !isRaining {
		// Pure snow: use snowfall intensity (cm/hr). 2cm+ = heavy = 1.0
		precipitation = clampF(cur.Snowfall/2.0, 0, 1)
	} else if isRaining && !isSnowing {
		// Pure rain: 5mm+ = heavy = 1.0
		precipitation = clampF((cur.Rain+cur.Showers)/5.0, 0, 1)
	} else if isSnowing && isRaining {
		// Mixed: use total precipitation
		precipitation = clampF(cur.Precipitation/5.0, 0, 1)
	}

	// Wetness: from rain only (snow doesn't make surfaces wet immediately)
	wetness := clampF((cur.Rain + cur.Showers) / 3.0, 0, 1)

	// Snow depth: from hourly forecast (closest hour), in meters.
	// 0.10m (10cm) = full coverage — even a few cm covers the ground visually.
	snowDepth := getClosestHourlySnowDepth(meteo)
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

// getClosestHourlySnowDepth finds the snow_depth value for the hour closest
// to the current time from the hourly forecast data.
func getClosestHourlySnowDepth(meteo *openMeteoResponse) float64 {
	if len(meteo.Hourly.Time) == 0 || len(meteo.Hourly.SnowDepth) == 0 {
		return 0
	}

	now := time.Now()
	bestIdx := 0
	bestDiff := time.Duration(math.MaxInt64)

	for i, ts := range meteo.Hourly.Time {
		t, err := time.Parse("2006-01-02T15:04", ts)
		if err != nil {
			continue
		}
		diff := now.Sub(t)
		if diff < 0 {
			diff = -diff
		}
		if diff < bestDiff {
			bestDiff = diff
			bestIdx = i
		}
	}

	if bestIdx < len(meteo.Hourly.SnowDepth) {
		return meteo.Hourly.SnowDepth[bestIdx]
	}
	return 0
}

// clampF clamps a float64 value between min and max.
func clampF(v, min, max float64) float64 {
	if v < min {
		return min
	}
	if v > max {
		return max
	}
	return v
}
