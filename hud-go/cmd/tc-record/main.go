// tc-record polls the TSW CommAPI at high frequency for the player's
// geoLocation + currentTile, converts lat/lng to UE world cm, and writes
// each sample as JSON Lines to a file on the Desktop.
//
// Why: clothoid-spiral curves on the rail network store a `PowersOfA`
// polynomial we can't decode without source. With dense (lat,lng,t)
// samples driven through clothoid sections, we can fit the polynomial
// empirically.
//
// Usage: tc-record [--out PATH] [--hz N]
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"hud-go/internal/geo"
)

const apiBase = "http://localhost:31270"

type playerInfo struct {
	Result string `json:"Result"`
	Values struct {
		GeoLocation struct {
			Longitude float64 `json:"longitude"`
			Latitude  float64 `json:"latitude"`
		} `json:"geoLocation"`
		CurrentTile struct {
			X int `json:"x"`
			Y int `json:"y"`
		} `json:"currentTile"`
		PlayerProfileName    string `json:"playerProfileName"`
		CameraMode           string `json:"cameraMode"`
		CurrentServiceName   string `json:"currentServiceName"`
	} `json:"Values"`
}

type sample struct {
	T       string  `json:"t"`        // RFC3339Nano timestamp
	Lat     float64 `json:"lat"`
	Lng     float64 `json:"lng"`
	TileX   int     `json:"tile_x"`
	TileY   int     `json:"tile_y"`
	WorldX  float64 `json:"world_x_cm"` // UE world cm via UTM (relative to TC origin)
	WorldY  float64 `json:"world_y_cm"`
	Service string  `json:"service,omitempty"`
}

// Training Centre origin (matches the value the extractor pulls from the
// route's persistent .uexp). Used to anchor lat/lng -> world cm so each
// recording is comparable to the ribbon network without re-running the
// extractor.
const (
	tcOriginLat = 51.11568832397461
	tcOriginLng = 6.209702968597412
)

func main() {
	defaultOut := filepath.Join(
		os.Getenv("USERPROFILE"), "Desktop",
		fmt.Sprintf("tc_recording_%s.jsonl", time.Now().Format("20060102_150405")),
	)
	out := flag.String("out", defaultOut, "output JSONL path")
	hz := flag.Float64("hz", 10.0, "polling rate in Hz")
	flag.Parse()

	keyPath := filepath.Join(os.Getenv("USERPROFILE"),
		"Documents", "My Games", "TrainSimWorld6", "Saved", "Config", "CommAPIKey.txt")
	keyData, err := os.ReadFile(keyPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "ERROR reading API key from %s: %v\n", keyPath, err)
		os.Exit(1)
	}
	apiKey := strings.TrimSpace(string(keyData))
	fmt.Fprintf(os.Stderr, "[tc-record] api key loaded (%d chars)\n", len(apiKey))

	anchor := geo.NewRouteAnchor(tcOriginLat, tcOriginLng)

	if err := os.MkdirAll(filepath.Dir(*out), 0o755); err != nil {
		fmt.Fprintf(os.Stderr, "ERROR mkdir: %v\n", err)
		os.Exit(1)
	}
	f, err := os.Create(*out)
	if err != nil {
		fmt.Fprintf(os.Stderr, "ERROR create: %v\n", err)
		os.Exit(1)
	}
	defer f.Close()
	enc := json.NewEncoder(f)
	enc.SetEscapeHTML(false)

	fmt.Fprintf(os.Stderr, "[tc-record] writing -> %s @ %.1f Hz (Ctrl-C to stop)\n", *out, *hz)

	httpClient := &http.Client{Timeout: 800 * time.Millisecond}
	tick := time.NewTicker(time.Duration(float64(time.Second) / *hz))
	defer tick.Stop()

	count := 0
	failures := 0
	lastLogged := time.Now()
	startedAt := time.Now()
	for now := range tick.C {
		req, _ := http.NewRequest("GET", apiBase+"/get/DriverAid.PlayerInfo", nil)
		req.Header.Set("DTGCommKey", apiKey)
		resp, err := httpClient.Do(req)
		if err != nil {
			failures++
			continue
		}
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		var pi playerInfo
		if err := json.Unmarshal(body, &pi); err != nil || pi.Result != "Success" {
			failures++
			continue
		}
		// UTM-anchored world cm. (lat,lng) -> (eastM, southM) -> *100 cm.
		eastM, southM := anchor.LatLngToWorld(pi.Values.GeoLocation.Latitude, pi.Values.GeoLocation.Longitude)
		s := sample{
			T:       now.UTC().Format(time.RFC3339Nano),
			Lat:     pi.Values.GeoLocation.Latitude,
			Lng:     pi.Values.GeoLocation.Longitude,
			TileX:   pi.Values.CurrentTile.X,
			TileY:   pi.Values.CurrentTile.Y,
			WorldX:  eastM * 100.0,
			WorldY:  southM * 100.0,
			Service: pi.Values.CurrentServiceName,
		}
		if err := enc.Encode(s); err != nil {
			fmt.Fprintf(os.Stderr, "ERROR write: %v\n", err)
			os.Exit(1)
		}
		count++
		if time.Since(lastLogged) >= 2*time.Second {
			elapsed := time.Since(startedAt).Round(time.Second)
			fmt.Fprintf(os.Stderr, "[tc-record] %s  samples=%d failures=%d  last=(%.6f, %.6f) tile=(%d,%d)\n",
				elapsed, count, failures, s.Lat, s.Lng, s.TileX, s.TileY)
			lastLogged = time.Now()
		}
	}
}
