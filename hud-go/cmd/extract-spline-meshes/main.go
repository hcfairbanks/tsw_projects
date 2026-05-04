// extract-spline-meshes — render the rail map straight from the engine's
// SplineMeshComponent SplineParams (one cubic Hermite curve per visible
// rail-bed segment), bypassing all NetworkRibbon arc/clothoid math.
//
// Pipeline:
//   1. Walk every ST_*.umap tile in the route's pak (or a workdir).
//   2. For each tile, find every SplineMeshComponent and pull its
//      SplineParams (StartPos/StartTangent/EndPos/EndTangent) plus the
//      component's inherited SceneComponent transform.
//   3. Combine: world position = tile origin + RelativeLocation + rotated
//      SplineParams positions.
//   4. Sample each segment as a cubic Hermite curve (1m steps), project
//      world (cm) → lat/lng using the route's origin from the pak metadata.
//   5. Emit a single FeatureCollection with one LineString per segment.
//
// Why ST tiles: TC's inventory shows SplineMeshComponent is overwhelmingly
// in ST tiles (7308 vs 567 in LT, 0 in TT/TS). LT contains landscape stuff;
// for now we only pull from ST.
//
// We make NO attempt to filter out non-track spline meshes on this first
// pass. If the output is mostly track but contains some clutter (fences,
// cables, walls), we can add a static-mesh-name filter later. Goal of this
// run: see whether the geometry comes out at correct world positions.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io/fs"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"hud-go/internal/geo"
	"hud-go/internal/pak"
	"hud-go/internal/pak/uasset"
)

const (
	tswPath  = `D:\SteamLibrary\steamapps\common\Train Sim World 6`
	repakBin = `C:\Users\hcfai\Desktop\applications_2\hud-go\repak.exe`
	// TSW world tiles are 1km × 1km. Each tile's origin in world (cm) is
	// (X * 100000, Y * 100000) where X,Y come from the filename TS_xN_yM.
	tileSizeCm = 100000.0
	// Sample step for Hermite curves. ST splines are typically 5-15 m long
	// so even a coarse step gives visually-smooth curves.
	sampleStepCm     = 100.0
	hermiteSamplesMin = 8
	hermiteSamplesMax = 64
)

var tileNameRE = regexp.MustCompile(`^([A-Z]+)_x(-?\d+)_y(-?\d+)\.umap$`)

func main() {
	routeFlag := flag.String("route", "", "route codename (used only for logging + pak discovery if --workdir is omitted)")
	workdir := flag.String("workdir", "", "directory of unpacked .umap tiles (recursive). If empty, unpack the pak.")
	outPath := flag.String("out", "", "output GeoJSON path")
	tileFamily := flag.String("tile-family", "ST", "which tile family to pull from: ST (track-bed mesh), LT (landscape), TS (track scenery), TT (track skeleton)")
	originLat := flag.Float64("origin-lat", 0, "route origin latitude (e.g. TC = 51.11568832397461)")
	originLng := flag.Float64("origin-lng", 0, "route origin longitude (e.g. TC = 6.209702968597412)")
	limit := flag.Int("limit", 0, "stop after N tiles (0 = no limit)")
	flag.Parse()

	if *routeFlag == "" {
		log.Fatal("missing --route")
	}
	if *originLat == 0 || *originLng == 0 {
		log.Fatal("missing --origin-lat / --origin-lng (find with tc-hermite log)")
	}
	if *outPath == "" {
		*outPath = filepath.Join(os.Getenv("USERPROFILE"), "Desktop",
			fmt.Sprintf("%s_splines_%s_%s.geojson", *routeFlag, *tileFamily,
				time.Now().Format("20060102_150405")))
	}

	anchor := geo.NewRouteAnchor(*originLat, *originLng)
	fmt.Fprintf(os.Stderr, "[extract-spline-meshes] route=%s origin=(%.6f,%.6f)\n",
		*routeFlag, *originLat, *originLng)

	var rt *pak.Route
	if *workdir == "" {
		routes, err := pak.DiscoverRoutes(tswPath)
		if err != nil {
			log.Fatalf("discover: %v", err)
		}
		for i := range routes {
			if strings.EqualFold(routes[i].Name, *routeFlag) {
				rt = &routes[i]
				break
			}
		}
		if rt == nil {
			log.Fatalf("route %q not found", *routeFlag)
		}
	}
	tiles, err := locateTiles(rt, *workdir, *tileFamily)
	if err != nil {
		log.Fatalf("locate: %v", err)
	}
	fmt.Fprintf(os.Stderr, "[extract-spline-meshes] %s tiles: %d\n", *tileFamily, len(tiles))

	t0 := time.Now()
	var features []feature
	totalSegs := 0
	tilesWithData := 0
	for i, tilePath := range tiles {
		if *limit > 0 && i >= *limit {
			break
		}
		tx, ty := tileXY(filepath.Base(tilePath))
		segs, err := uasset.ParseSplineMeshesFromUmap(tilePath)
		if err != nil {
			fmt.Fprintf(os.Stderr, "  skip %s: %v\n", filepath.Base(tilePath), err)
			continue
		}
		if len(segs) > 0 {
			tilesWithData++
		}
		for _, seg := range segs {
			coords := segmentToCoords(seg, tx, ty, anchor)
			if len(coords) < 2 {
				continue
			}
			features = append(features, feature{
				Type: "Feature",
				Geometry: geometry{
					Type:        "LineString",
					Coordinates: coords,
				},
				Properties: map[string]any{
					"tile":   filepath.Base(tilePath),
					"comp":   seg.CompName,
					"length": uasset.HermiteLength(
						seg.StartX, seg.StartY, seg.StartZ,
						seg.StartTanX, seg.StartTanY, seg.StartTanZ,
						seg.EndX, seg.EndY, seg.EndZ,
						seg.EndTanX, seg.EndTanY, seg.EndTanZ,
					) / 100.0, // metres
				},
			})
			totalSegs++
		}
		if (i+1)%20 == 0 || i+1 == len(tiles) {
			fmt.Fprintf(os.Stderr, "  %d/%d tiles, %d segments (%.0fs)\n",
				i+1, len(tiles), totalSegs, time.Since(t0).Seconds())
		}
	}

	fmt.Fprintf(os.Stderr, "\nTotal segments: %d\n", totalSegs)
	fmt.Fprintf(os.Stderr, "Tiles with data: %d\n", tilesWithData)

	out := struct {
		Type     string    `json:"type"`
		Features []feature `json:"features"`
	}{"FeatureCollection", features}
	data, err := json.Marshal(out)
	if err != nil {
		log.Fatalf("marshal: %v", err)
	}
	if err := os.WriteFile(*outPath, data, 0o644); err != nil {
		log.Fatalf("write: %v", err)
	}
	fmt.Fprintf(os.Stderr, "wrote %s (%d bytes)\n", *outPath, len(data))
}

// segmentToCoords lifts a SplineMeshSegment from component-local cm into
// tile-local cm (combining the component's RelativeLocation + yaw),
// samples the Hermite curve at fixed step, and projects each sample to
// [lng, lat] using the route anchor.
//
// UE convention: X+ east, Y+ south, both in cm. The route anchor's
// TileAndOffsetToLatLng wants tile indices + east-metres + south-metres.
func segmentToCoords(seg uasset.SplineMeshSegment, tileX, tileY int, anchor *geo.RouteAnchor) [][2]float64 {
	sx, sy, _, stx, sty, _, ex, ey, _, etx, ety, _ := seg.TileLocalEnds()

	length := uasset.HermiteLength(sx, sy, 0, stx, sty, 0, ex, ey, 0, etx, ety, 0)
	n := int(length/sampleStepCm) + 1
	if n < hermiteSamplesMin {
		n = hermiteSamplesMin
	}
	if n > hermiteSamplesMax {
		n = hermiteSamplesMax
	}
	coords := make([][2]float64, 0, n+1)
	for i := 0; i <= n; i++ {
		t := float64(i) / float64(n)
		x := uasset.HermiteSample(sx, stx, ex, etx, t)
		y := uasset.HermiteSample(sy, sty, ey, ety, t)
		// tile-local cm → metres for the anchor projection. X is east, Y is
		// south in UE. (TileAndOffsetToLatLng takes within-tile east/south.)
		lat, lng := anchor.TileAndOffsetToLatLng(tileX, tileY, x/100.0, y/100.0)
		coords = append(coords, [2]float64{lng, lat})
	}
	return coords
}

func tileXY(name string) (int, int) {
	m := tileNameRE.FindStringSubmatch(name)
	if m == nil {
		return 0, 0
	}
	x, _ := strconv.Atoi(m[2])
	y, _ := strconv.Atoi(m[3])
	return x, y
}

func locateTiles(rt *pak.Route, workdir, family string) ([]string, error) {
	if workdir != "" {
		var tiles []string
		err := filepath.WalkDir(workdir, func(p string, d fs.DirEntry, err error) error {
			if err != nil || d.IsDir() {
				return nil
			}
			if !strings.HasSuffix(d.Name(), ".umap") {
				return nil
			}
			if !strings.HasPrefix(d.Name(), family+"_") {
				return nil
			}
			tiles = append(tiles, p)
			return nil
		})
		if err != nil {
			return nil, err
		}
		sort.Strings(tiles)
		return tiles, nil
	}
	// No workdir — unpack from pak.
	tmp := filepath.Join(os.Getenv("USERPROFILE"), "Desktop",
		fmt.Sprintf("splines-%s-%d", rt.Name, time.Now().Unix()))
	if err := os.MkdirAll(tmp, 0o755); err != nil {
		return nil, err
	}
	prefix := strings.TrimPrefix(filepath.Base(rt.PakPath), "TS2Prototype-WindowsNoEditor-")
	prefix = strings.TrimSuffix(prefix, "-coredata.pak")
	prefix = strings.TrimSuffix(prefix, ".pak")
	include := "TS2Prototype/Plugins/DLC/" + prefix + "/Content/Map/Tiles"
	cmd := exec.Command(repakBin, "unpack", "-f", "-o", tmp, "-i", include, rt.PakPath)
	cmd.Stdout = os.Stderr
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("repak: %w", err)
	}
	return locateTiles(rt, tmp, family)
}

type feature struct {
	Type       string         `json:"type"`
	Geometry   geometry       `json:"geometry"`
	Properties map[string]any `json:"properties"`
}
type geometry struct {
	Type        string       `json:"type"`
	Coordinates [][2]float64 `json:"coordinates"`
}
