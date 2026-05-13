// backfill-route-ids — assigns route_id to timetables that were imported
// without one, by matching each orphan's path-coordinate bbox against every
// route's coordinate bbox.
//
// 31% of timetables (~12.8k of 41.5k) currently have route_id NULL because
// the importer fails to set it for cross-pak / cross-section imports. Without
// route_id the timetable's show page can't fetch /api/routes/{id}/map-data,
// so layers like Platform Tracks / Platforms / Signals stay empty.
//
// Match rule: a timetable matches a route iff EVERY one of the timetable's
// path coordinates lies within the route's bbox (inflated by `--margin-deg`,
// default ~50 m at 51°N). Single-match rows are linked. Multi-match rows are
// reported but skipped (ambiguous; needs manual review or stricter rule).
//
// Usage:
//
//	backfill-route-ids                    # dry run, prints what it would do
//	backfill-route-ids --apply            # actually writes route_id
//	backfill-route-ids --db=/path/to.db   # override default DB path
//	backfill-route-ids --margin-deg=0.001 # change bbox tolerance
package main

import (
	"database/sql"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"math"

	_ "modernc.org/sqlite"
)

type bbox struct{ minLat, maxLat, minLng, maxLng float64 }

func newBbox() bbox {
	return bbox{minLat: math.Inf(1), maxLat: math.Inf(-1), minLng: math.Inf(1), maxLng: math.Inf(-1)}
}

func (b *bbox) extend(lat, lng float64) {
	if lat < b.minLat {
		b.minLat = lat
	}
	if lat > b.maxLat {
		b.maxLat = lat
	}
	if lng < b.minLng {
		b.minLng = lng
	}
	if lng > b.maxLng {
		b.maxLng = lng
	}
}

func (b bbox) valid() bool {
	return b.minLat <= b.maxLat && b.minLng <= b.maxLng
}

// containsBbox returns true when `inner` lies entirely within `b`, expanded
// by `margin` degrees on every side. Margin lets us absorb the few-metre
// drift between extraction passes (per-stop snapping vs rail-builder slicing
// can move a coord ~10 m even when both belong to the same route).
func (b bbox) containsBbox(inner bbox, margin float64) bool {
	return inner.minLat >= b.minLat-margin &&
		inner.maxLat <= b.maxLat+margin &&
		inner.minLng >= b.minLng-margin &&
		inner.maxLng <= b.maxLng+margin
}

// routeBboxFromBlob parses a route_coordinates.coordinates blob and returns
// the bbox of every coordinate it contains. Two stored shapes exist:
//
//  1. NEW: a JSON array of GeoJSON Feature objects with .geometry.type +
//     .geometry.coordinates (Point / LineString / MultiLineString). Written
//     by the cooked-map writer for newly-extracted routes.
//
//  2. LEGACY: a JSON array of arrays of {latitude, longitude} objects. Each
//     inner array is one LineString (e.g. one rail segment). Written by the
//     pre-cookedmap importer for older imports.
//
// We probe by inspecting the first element: a map with a "geometry" key →
// shape 1; an array → shape 2. Empty blob → no bbox.
func routeBboxFromBlob(raw string) (bbox, int) {
	b := newBbox()
	var arr []any
	if err := json.Unmarshal([]byte(raw), &arr); err != nil {
		return b, 0
	}
	if len(arr) == 0 {
		return b, 0
	}
	if _, isFeatureMap := arr[0].(map[string]any); isFeatureMap {
		return routeBboxFromFeatureArray(arr)
	}
	if _, isLineArray := arr[0].([]any); isLineArray {
		return routeBboxFromLatLngArrays(arr)
	}
	return b, 0
}

func routeBboxFromFeatureArray(feats []any) (bbox, int) {
	b := newBbox()
	count := 0
	for _, f := range feats {
		feat, _ := f.(map[string]any)
		geom, _ := feat["geometry"].(map[string]any)
		if geom == nil {
			continue
		}
		gtype, _ := geom["type"].(string)
		coords := geom["coordinates"]
		switch gtype {
		case "Point":
			if c, ok := coords.([]any); ok && len(c) >= 2 {
				lng, _ := c[0].(float64)
				lat, _ := c[1].(float64)
				b.extend(lat, lng)
				count++
			}
		case "LineString":
			if pts, ok := coords.([]any); ok {
				for _, p := range pts {
					if c, ok := p.([]any); ok && len(c) >= 2 {
						lng, _ := c[0].(float64)
						lat, _ := c[1].(float64)
						b.extend(lat, lng)
						count++
					}
				}
			}
		case "MultiLineString":
			if lines, ok := coords.([]any); ok {
				for _, ln := range lines {
					if pts, ok := ln.([]any); ok {
						for _, p := range pts {
							if c, ok := p.([]any); ok && len(c) >= 2 {
								lng, _ := c[0].(float64)
								lat, _ := c[1].(float64)
								b.extend(lat, lng)
								count++
							}
						}
					}
				}
			}
		}
	}
	return b, count
}

func routeBboxFromLatLngArrays(lines []any) (bbox, int) {
	b := newBbox()
	count := 0
	for _, ln := range lines {
		pts, ok := ln.([]any)
		if !ok {
			continue
		}
		for _, p := range pts {
			pm, ok := p.(map[string]any)
			if !ok {
				continue
			}
			lat, _ := pm["latitude"].(float64)
			lng, _ := pm["longitude"].(float64)
			if lat == 0 && lng == 0 {
				continue
			}
			b.extend(lat, lng)
			count++
		}
	}
	return b, count
}

// timetableBboxFromArray parses a timetable_coordinates.coordinates blob (a
// JSON array of {latitude, longitude, ...} objects, the per-service path
// emitted by BuildServicePath) and returns its bbox.
func timetableBboxFromArray(raw string) (bbox, int) {
	b := newBbox()
	var arr []map[string]any
	if err := json.Unmarshal([]byte(raw), &arr); err != nil {
		return b, 0
	}
	count := 0
	for _, c := range arr {
		lat, _ := c["latitude"].(float64)
		lng, _ := c["longitude"].(float64)
		if lat == 0 && lng == 0 {
			continue
		}
		b.extend(lat, lng)
		count++
	}
	return b, count
}

func main() {
	dbPath := flag.String("db", "tsw_hud.db", "path to the SQLite DB")
	apply := flag.Bool("apply", false, "actually write UPDATE statements (default: dry run)")
	marginDeg := flag.Float64("margin-deg", 0.001, "bbox tolerance in degrees (~0.001° ≈ 70 m at 51°N latitude)")
	verbose := flag.Bool("v", false, "print one line per orphan timetable")
	flag.Parse()

	db, err := sql.Open("sqlite", *dbPath)
	if err != nil {
		log.Fatalf("open db: %v", err)
	}
	defer db.Close()

	// Build per-route bbox table.
	type routeInfo struct {
		id   int
		name string
		bb   bbox
	}
	var routes []routeInfo
	rrows, err := db.Query(`
		SELECT r.id, r.name, rc.coordinates
		FROM routes r
		JOIN route_coordinates rc ON rc.route_id = r.id
	`)
	if err != nil {
		log.Fatalf("query routes: %v", err)
	}
	for rrows.Next() {
		var id int
		var name, coords string
		if err := rrows.Scan(&id, &name, &coords); err != nil {
			log.Printf("scan route: %v", err)
			continue
		}
		bb, n := routeBboxFromBlob(coords)
		if !bb.valid() || n == 0 {
			continue
		}
		routes = append(routes, routeInfo{id, name, bb})
	}
	rrows.Close()
	fmt.Printf("loaded %d routes with bboxes\n", len(routes))

	// Iterate orphan timetables.
	rows, err := db.Query(`
		SELECT t.id, tc.coordinates
		FROM timetables t
		JOIN timetable_coordinates tc ON tc.timetable_id = t.id
		WHERE t.route_id IS NULL
	`)
	if err != nil {
		log.Fatalf("query orphans: %v", err)
	}

	type updateOp struct {
		ttID    int
		routeID int
	}
	var updates []updateOp
	var noMatch, ambiguous int

	for rows.Next() {
		var ttID int
		var coords string
		if err := rows.Scan(&ttID, &coords); err != nil {
			log.Printf("scan timetable: %v", err)
			continue
		}
		ttBB, ttN := timetableBboxFromArray(coords)
		if !ttBB.valid() || ttN == 0 {
			noMatch++
			continue
		}
		var matches []routeInfo
		for _, r := range routes {
			if r.bb.containsBbox(ttBB, *marginDeg) {
				matches = append(matches, r)
			}
		}
		switch len(matches) {
		case 0:
			noMatch++
			if *verbose {
				fmt.Printf("  no-match: tt=%d bbox=(%.4f,%.4f)→(%.4f,%.4f)\n",
					ttID, ttBB.minLat, ttBB.minLng, ttBB.maxLat, ttBB.maxLng)
			}
		case 1:
			updates = append(updates, updateOp{ttID, matches[0].id})
			if *verbose {
				fmt.Printf("  match: tt=%d → route=%d (%s)\n", ttID, matches[0].id, matches[0].name)
			}
		default:
			ambiguous++
			if *verbose {
				names := ""
				for i, m := range matches {
					if i > 0 {
						names += ", "
					}
					names += fmt.Sprintf("%d (%s)", m.id, m.name)
				}
				fmt.Printf("  ambiguous: tt=%d → %s\n", ttID, names)
			}
		}
	}
	rows.Close()

	fmt.Printf("\nsummary:\n")
	fmt.Printf("  unique-match  : %d\n", len(updates))
	fmt.Printf("  no-match      : %d\n", noMatch)
	fmt.Printf("  ambiguous     : %d\n", ambiguous)

	if !*apply {
		fmt.Printf("\ndry run — re-run with --apply to write %d UPDATEs.\n", len(updates))
		return
	}

	// Apply updates inside a single transaction.
	tx, err := db.Begin()
	if err != nil {
		log.Fatalf("begin tx: %v", err)
	}
	stmt, err := tx.Prepare(`UPDATE timetables SET route_id = ? WHERE id = ? AND route_id IS NULL`)
	if err != nil {
		log.Fatalf("prepare: %v", err)
	}
	written := 0
	for _, op := range updates {
		if _, err := stmt.Exec(op.routeID, op.ttID); err != nil {
			log.Printf("update tt=%d: %v", op.ttID, err)
			continue
		}
		written++
	}
	stmt.Close()
	if err := tx.Commit(); err != nil {
		log.Fatalf("commit: %v", err)
	}
	fmt.Printf("\nwrote %d route_id updates.\n", written)
}
