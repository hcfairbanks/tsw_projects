// walls-per-tile — quick heat-map extractor. For each ST_xN_yM.umap in a
// route's pak workdir, count the BPC_WallCollision_* exports — each one
// is one placement of the route's "invisible wall" spline blueprint. Emit
// one GeoJSON Feature per non-empty tile: a 1km × 1km Polygon at the
// tile's world position, with `wall_count` in properties.
//
// "Per-tile" is a deliberate trade-off — getting the actual per-instance
// world transforms is doable but requires walking each BPC_WallCollision's
// owned InstancedStaticMeshComponents AND the level's PersistentLevel
// transform table. For a mockup, per-tile is fine: the user wants to see
// where the walls cluster, not the literal wall polylines.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io/fs"
	"log"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"hud-go/internal/devtools"
	"hud-go/internal/geo"
	"hud-go/internal/pak/uasset"
)

// tileSizeCm is the world-cm size of one UE world-composition tile. TSW6
// uses 1 km × 1 km tiles uniformly across every route shipped to date —
// `extract-ribbons-cooked` shares this constant.
const tileSizeCm = 100000.0

// tileRE captures (type, x, y) from a tile filename. Type is ST/TS/TT/LT.
var tileRE = regexp.MustCompile(`^([A-Z]{2})_x(-?\d+)_y(-?\d+)`)

var repakBin = devtools.MustFindBin("repak")

func main() {
	pakPath := flag.String("pak", "", "route .pak (will be unpacked to a temp dir)")
	workdir := flag.String("workdir", "", "directory of already-unpacked tiles")
	out := flag.String("out", "", "output GeoJSON path")
	originLat := flag.Float64("origin-lat", 0, "route origin latitude (auto if 0)")
	originLng := flag.Float64("origin-lng", 0, "route origin longitude (auto if 0)")
	tileType := flag.String("tile-type", "ST", "tile prefix to scan (ST / TT / TS / LT)")
	bpPrefix := flag.String("bp-prefix", "BPC_WallCollision", "export-name prefix that counts as one wall placement")
	flag.Parse()
	if *out == "" || (*pakPath == "" && *workdir == "") {
		log.Fatal("usage: walls-per-tile --pak <pak> --out <geojson>  (or --workdir <dir>)")
	}

	t0 := time.Now()
	root := *workdir
	if root == "" {
		tmp, err := os.MkdirTemp("", "walls-per-tile-*")
		if err != nil {
			log.Fatalf("mkdtemp: %v", err)
		}
		root = tmp
		fmt.Fprintf(os.Stderr, "[walls-per-tile] unpacking %s ...\n", *pakPath)
		cmd := exec.Command(repakBin, "unpack",
			"--include", "Content/Map/Tiles",
			"--output", root, *pakPath)
		cmd.Stdout = os.Stderr
		cmd.Stderr = os.Stderr
		if err := cmd.Run(); err != nil {
			log.Fatalf("repak: %v", err)
		}
		defer os.RemoveAll(root)
	}

	// Origin auto-discovery (same logic as ribbons-with-rule).
	if *originLat == 0 || *originLng == 0 {
		_ = filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
			if err != nil || d.IsDir() || *originLat != 0 {
				return nil
			}
			if !strings.HasSuffix(strings.ToLower(p), ".uexp") {
				return nil
			}
			parts := strings.Split(filepath.ToSlash(p), "/")
			for _, pp := range parts {
				if strings.EqualFold(pp, "Tiles") {
					return nil
				}
			}
			if len(parts) < 3 || !strings.EqualFold(parts[len(parts)-3], "Content") {
				return nil
			}
			parent := strings.ToLower(parts[len(parts)-2])
			if parent != "map" && !strings.HasSuffix(parent, "map") {
				return nil
			}
			lat, lng, err := geo.ExtractOriginFromUExp(p)
			if err == nil && lat != 0 && lng != 0 {
				*originLat, *originLng = lat, lng
			}
			return nil
		})
	}
	if *originLat == 0 || *originLng == 0 {
		log.Fatal("could not discover origin")
	}
	anchor := geo.NewRouteAnchor(*originLat, *originLng)

	// Walk every <TILE_TYPE>_*.umap.
	var tiles []string
	_ = filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		base := d.Name()
		if !strings.HasSuffix(strings.ToLower(base), ".umap") {
			return nil
		}
		if !strings.HasPrefix(base, *tileType+"_") {
			return nil
		}
		tiles = append(tiles, p)
		return nil
	})
	fmt.Fprintf(os.Stderr, "[walls-per-tile] %d %s tiles\n", len(tiles), *tileType)

	type tileRec struct {
		x, y   int
		count  int
	}
	var recs []tileRec
	totalWalls, tilesWithWalls := 0, 0
	for i, tilePath := range tiles {
		m := tileRE.FindStringSubmatch(filepath.Base(tilePath))
		if m == nil {
			continue
		}
		tx, _ := strconv.Atoi(m[2])
		ty, _ := strconv.Atoi(m[3])
		u, err := uasset.ReadUmap(tilePath)
		if err != nil {
			continue
		}
		count := 0
		for _, e := range u.Exports {
			if strings.HasPrefix(e.ObjectName, *bpPrefix) &&
				!strings.HasPrefix(e.ObjectName, "Default__") {
				count++
			}
		}
		if count > 0 {
			recs = append(recs, tileRec{tx, ty, count})
			totalWalls += count
			tilesWithWalls++
		}
		if (i+1)%50 == 0 || i+1 == len(tiles) {
			fmt.Fprintf(os.Stderr, "  %d/%d tiles (%s)\n",
				i+1, len(tiles), time.Since(t0).Round(time.Second))
		}
	}

	// Build GeoJSON. One Polygon per non-empty tile.
	features := make([]any, 0, len(recs))
	for _, r := range recs {
		// World cm bounds of this tile: [tx*100km .. (tx+1)*100km] etc.
		// In TSW's coord system X increases east, Y increases south (cm).
		x0, y0 := float64(r.x)*tileSizeCm, float64(r.y)*tileSizeCm
		x1, y1 := x0+tileSizeCm, y0+tileSizeCm
		// Project corners to lat/lng.
		toLL := func(x, y float64) [2]float64 {
			lat, lng := anchor.WorldToLatLng(x/100.0, y/100.0)
			return [2]float64{round7(lng), round7(lat)}
		}
		// GeoJSON ring (closed): NW, NE, SE, SW, NW. The world's Y axis
		// is south-positive so y0 = north, y1 = south.
		ring := [][2]float64{
			toLL(x0, y0), toLL(x1, y0), toLL(x1, y1), toLL(x0, y1), toLL(x0, y0),
		}
		features = append(features, map[string]any{
			"type": "Feature",
			"properties": map[string]any{
				"tile_x":     r.x,
				"tile_y":     r.y,
				"wall_count": r.count,
			},
			"geometry": map[string]any{
				"type":        "Polygon",
				"coordinates": [][][2]float64{ring},
			},
		})
	}
	doc := map[string]any{
		"type":     "FeatureCollection",
		"name":     "walls-per-tile",
		"features": features,
		"metadata": map[string]any{
			"tile_type":         *tileType,
			"bp_prefix":         *bpPrefix,
			"tiles_scanned":     len(tiles),
			"tiles_with_walls":  tilesWithWalls,
			"total_wall_placements": totalWalls,
			"origin_lat": *originLat, "origin_lng": *originLng,
		},
	}
	if err := os.MkdirAll(filepath.Dir(*out), 0o755); err != nil {
		log.Fatalf("mkdir: %v", err)
	}
	f, err := os.Create(*out)
	if err != nil {
		log.Fatalf("create: %v", err)
	}
	defer f.Close()
	enc := json.NewEncoder(f)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(doc); err != nil {
		log.Fatalf("encode: %v", err)
	}
	fmt.Fprintf(os.Stderr, "[walls-per-tile] %d non-empty %s tiles, %d total %s placements -> %s\n",
		tilesWithWalls, *tileType, totalWalls, *bpPrefix, *out)
	fmt.Fprintf(os.Stderr, "[walls-per-tile] total: %s\n", time.Since(t0).Round(time.Millisecond))
}

func round7(v float64) float64 { return math.Round(v*1e7) / 1e7 }
