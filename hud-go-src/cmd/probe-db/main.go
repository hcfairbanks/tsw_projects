// probe-db — emit two GeoJSON layers for visual overlay:
//
//   tt189_db_coords.geojson        the polyline the DB has stored for
//                                   timetable_id=189 (one LineString)
//   tt189_breadcrumb_ribbons.geojson the rails-features LineStrings whose
//                                   ribbon GUIDs match the DataTrack
//                                   breadcrumbs for "MBTA Providence #805
//                                   (Outbound)" (one Feature per ribbon)
//
// Args:
//   1. tsw_hud.db path
//   2. Boston_Sprinter.zip path (for route_*.json with rails features)
//   3. BPE_Timetable_MasterDataTrack.uasset path (any DT file with the
//      service)
//   4. output dir (e.g. C:/Users/.../Desktop)
package main

import (
	"archive/zip"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	_ "modernc.org/sqlite"

	"hud-go/internal/pak/uasset"
)

type rawCoord struct {
	Latitude  float64 `json:"latitude"`
	Longitude float64 `json:"longitude"`
}

func main() {
	dbPath := os.Args[1]
	zipPath := os.Args[2]
	dtPath := os.Args[3]
	outDir := os.Args[4]

	// --- 1. DB coords for timetable 189 ---
	db, err := sql.Open("sqlite", dbPath)
	must(err)
	defer db.Close()
	var coordsJSON string
	err = db.QueryRow(`SELECT coordinates FROM timetable_coordinates WHERE timetable_id = ?`, 189).Scan(&coordsJSON)
	must(err)
	var coords []rawCoord
	must(json.Unmarshal([]byte(coordsJSON), &coords))
	fmt.Printf("DB coords for tt=189: %d points\n", len(coords))
	lineCoords := make([][]float64, 0, len(coords))
	for _, c := range coords {
		lineCoords = append(lineCoords, []float64{c.Longitude, c.Latitude})
	}
	dbGeoJSON := map[string]any{
		"type": "FeatureCollection",
		"features": []any{
			map[string]any{
				"type": "Feature",
				"properties": map[string]any{
					"name":  "DB path tt=189",
					"color": "#ff0000",
				},
				"geometry": map[string]any{
					"type":        "LineString",
					"coordinates": lineCoords,
				},
			},
		},
	}
	writeJSON(filepath.Join(outDir, "tt189_db_coords.geojson"), dbGeoJSON)
	fmt.Printf("wrote %s\n", filepath.Join(outDir, "tt189_db_coords.geojson"))

	// --- 2. DataTrack breadcrumb ribbon GUIDs ---
	dt, err := uasset.ParseCookedDataTrack(dtPath)
	must(err)
	std, ok := dt.Services["MBTA Providence #805 (Outbound)"]
	if !ok {
		fmt.Fprintln(os.Stderr, "service key not in DataTrack")
		os.Exit(1)
	}
	wanted := map[string]struct{}{}
	for _, td := range std.TrackData {
		norm := uasset.NormalizeGUID(td.RibbonGUID)
		if norm == "" {
			norm = strings.ToLower(td.RibbonGUID)
		}
		wanted[norm] = struct{}{}
	}
	fmt.Printf("DataTrack distinct ribbons for #805 Outbound: %d\n", len(wanted))

	// --- 3. Filter the route GeoJSON's rails features by those GUIDs ---
	zr, err := zip.OpenReader(zipPath)
	must(err)
	defer zr.Close()
	var routeFile *zip.File
	for _, f := range zr.File {
		if strings.HasPrefix(filepath.Base(f.Name), "route_") && strings.HasSuffix(f.Name, ".json") {
			routeFile = f
			break
		}
	}
	if routeFile == nil {
		fmt.Fprintln(os.Stderr, "no route_*.json in zip")
		os.Exit(1)
	}
	rc, _ := routeFile.Open()
	rb, _ := io.ReadAll(rc)
	rc.Close()
	var rdoc struct {
		Features []map[string]any `json:"features"`
	}
	must(json.Unmarshal(rb, &rdoc))

	matchedFeats := []map[string]any{}
	for _, feat := range rdoc.Features {
		props, _ := feat["properties"].(map[string]any)
		if props == nil {
			continue
		}
		guidStr, _ := props["ribbon_guid"].(string)
		norm := uasset.NormalizeGUID(guidStr)
		if _, hit := wanted[norm]; !hit {
			continue
		}
		props["color"] = "#00ff00"
		props["name"] = "DT ribbon " + guidStr
		matchedFeats = append(matchedFeats, feat)
	}
	fmt.Printf("matched %d rails-features for those GUIDs\n", len(matchedFeats))

	dtGeoJSON := map[string]any{
		"type":     "FeatureCollection",
		"features": matchedFeats,
	}
	writeJSON(filepath.Join(outDir, "tt189_breadcrumb_ribbons.geojson"), dtGeoJSON)
	fmt.Printf("wrote %s\n", filepath.Join(outDir, "tt189_breadcrumb_ribbons.geojson"))
}

func must(err error) {
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func writeJSON(path string, v any) {
	f, err := os.Create(path)
	must(err)
	defer f.Close()
	enc := json.NewEncoder(f)
	enc.SetEscapeHTML(false)
	must(enc.Encode(v))
}
