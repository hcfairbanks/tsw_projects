// tc-mesh: walks SplineMeshComponent.SplineParams in every tile umap and
// Hermite-samples each segment to produce a high-fidelity rails GeoJSON.
//
// Why: the analytic-curve path in internal/output/rails_geojson.go treats
// NetworkCurveClothoidSpiral ribbons as straight chords (the curve asset has
// no Radius field, so ArcDelta degenerates to L*Tangent). SplineMeshComponents
// carry the actual cubic-Hermite spline UE renders the track mesh along —
// walking those gives the true rendered geometry for any curve type, including
// clothoids.
//
// Output: <Desktop>\TrainingCentre_mesh_<ts>.geojson — viewer-readable.
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

	"hud-go/internal/geo"
	"hud-go/internal/pak"
)

const (
	tswPath    = `D:\SteamLibrary\steamapps\common\Train Sim World 6`
	repakBin   = `C:\Users\hcfai\Desktop\applications_2\hud-go\repak.exe`
	uassetBin  = `C:\Users\hcfai\Desktop\applications_2\hud-go\UAssetGUI.exe`
	target     = "TrainingCentre"
	tileSizeCm = 100_000.0 // 1 km tile in UE cm
	// Hermite samples per spline segment — 32 keeps even tight S-curves smooth.
	samplesPerSeg = 32
)

func main() {
	defaultOut := filepath.Join(
		os.Getenv("USERPROFILE"), "Desktop",
		fmt.Sprintf("TrainingCentre_mesh_%s.geojson", time.Now().Format("20060102_150405")),
	)
	out := flag.String("out", defaultOut, "output GeoJSON path")
	flag.Parse()

	routes, err := pak.DiscoverRoutes(tswPath)
	if err != nil {
		log.Fatalf("discover routes: %v", err)
	}
	var route *pak.Route
	for i := range routes {
		if strings.EqualFold(routes[i].Name, target) {
			route = &routes[i]
			break
		}
	}
	if route == nil {
		log.Fatalf("route %q not found", target)
	}
	fmt.Fprintf(os.Stderr, "[tc-mesh] route=%s pak=%s\n", route.Name, route.PakPath)

	workDir, err := os.MkdirTemp("", "tc-mesh-*")
	if err != nil {
		log.Fatalf("mkdtemp: %v", err)
	}
	defer os.RemoveAll(workDir)
	fmt.Fprintf(os.Stderr, "[tc-mesh] workdir=%s\n", workDir)

	t0 := time.Now()
	if err := runRepak(route.PakPath, workDir); err != nil {
		log.Fatalf("repak unpack: %v", err)
	}
	fmt.Fprintf(os.Stderr, "[tc-mesh] repak unpack done in %s\n", time.Since(t0).Round(time.Millisecond))

	originLat, originLng := findOrigin(workDir)
	if originLat == 0 && originLng == 0 {
		log.Fatalf("origin lat/lng not found")
	}
	fmt.Fprintf(os.Stderr, "[tc-mesh] origin=(%v, %v)\n", originLat, originLng)
	anchor := geo.NewRouteAnchor(originLat, originLng)

	tileAssets := findTileUmaps(workDir)
	fmt.Fprintf(os.Stderr, "[tc-mesh] %d tile umaps\n", len(tileAssets))

	// Per-tile-prefix counts so we know which tile types actually carry
	// SplineMeshComponents. Training Centre's 4 prefixes (LT/ST/TS/TT)
	// don't all contain the same content.
	prefixSegs := map[string]int{}
	prefixWithMesh := map[string]int{}
	totalSegs := 0
	features := []map[string]any{}

	t0 = time.Now()
	for i, ua := range tileAssets {
		jsonPath := ua + ".json"
		if _, err := os.Stat(jsonPath); err != nil {
			if err := runUAssetGUI(ua, jsonPath); err != nil {
				continue
			}
		}
		base := strings.TrimSuffix(filepath.Base(ua), ".umap")
		tx, ty, prefix, ok := parseTileXY(base)
		if !ok {
			continue
		}
		segs := parseTileSplines(jsonPath)
		prefixSegs[prefix] += len(segs)
		if len(segs) > 0 {
			prefixWithMesh[prefix]++
		}
		totalSegs += len(segs)

		offX := float64(tx) * tileSizeCm
		offY := float64(ty) * tileSizeCm
		for _, s := range segs {
			pts := hermiteSample(s, samplesPerSeg)
			line := make([][2]float64, 0, len(pts))
			for _, p := range pts {
				wx := p[0] + offX
				wy := p[1] + offY
				lat, lng := anchor.WorldToLatLng(wx/100.0, wy/100.0)
				line = append(line, [2]float64{round7(lng), round7(lat)})
			}
			features = append(features, map[string]any{
				"type":     "Feature",
				"geometry": map[string]any{"type": "LineString", "coordinates": line},
				"properties": map[string]any{
					"tile":     base,
					"tile_x":   tx,
					"tile_y":   ty,
					"tile_kind": prefix,
				},
			})
		}
		if (i+1)%10 == 0 || i+1 == len(tileAssets) {
			fmt.Fprintf(os.Stderr, "[tc-mesh]   %d/%d tiles parsed, %d segs total (%s)\n",
				i+1, len(tileAssets), totalSegs, time.Since(t0).Round(time.Second))
		}
	}

	fmt.Fprintf(os.Stderr, "[tc-mesh] per-prefix segments: %v\n", prefixSegs)
	fmt.Fprintf(os.Stderr, "[tc-mesh] per-prefix tiles-with-mesh: %v\n", prefixWithMesh)

	gj := map[string]any{
		"type":     "FeatureCollection",
		"name":     "TrainingCentre_mesh",
		"features": features,
	}
	if err := os.MkdirAll(filepath.Dir(*out), 0o755); err != nil {
		log.Fatalf("mkdir output dir: %v", err)
	}
	f, err := os.Create(*out)
	if err != nil {
		log.Fatalf("create output: %v", err)
	}
	defer f.Close()
	enc := json.NewEncoder(f)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(gj); err != nil {
		log.Fatalf("encode: %v", err)
	}
	fmt.Fprintf(os.Stderr, "[tc-mesh] wrote %d feature(s) to %s\n", len(features), *out)
}

func runRepak(pakPath, dest string) error {
	cmd := exec.Command(repakBin, "unpack", "--output", dest, pakPath)
	cmd.Stdout = os.Stderr
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

func runUAssetGUI(uassetPath, jsonPath string) error {
	cmd := exec.Command(uassetBin, "tojson", uassetPath, jsonPath, "VER_UE4_27")
	return cmd.Run()
}

func findOrigin(extractRoot string) (lat, lng float64) {
	var candidates []string
	_ = filepath.WalkDir(extractRoot, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		if !strings.HasSuffix(strings.ToLower(p), ".uexp") {
			return nil
		}
		parts := strings.Split(filepath.ToSlash(p), "/")
		if len(parts) < 2 || !strings.EqualFold(parts[len(parts)-2], "Map") {
			return nil
		}
		candidates = append(candidates, p)
		return nil
	})
	for _, p := range candidates {
		la, ln, err := geo.ExtractOriginFromUExp(p)
		if err == nil && la != 0 && ln != 0 {
			return la, ln
		}
	}
	return 0, 0
}

func findTileUmaps(extractRoot string) []string {
	var out []string
	_ = filepath.WalkDir(extractRoot, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		if !strings.HasSuffix(strings.ToLower(p), ".umap") {
			return nil
		}
		parts := strings.Split(filepath.ToSlash(p), "/")
		if len(parts) < 2 || !strings.EqualFold(parts[len(parts)-2], "Tiles") {
			return nil
		}
		out = append(out, p)
		return nil
	})
	return out
}

// Tile filenames look like "LT_x0_y0", "TT_x-3_y2", etc.
var tileNameRE = regexp.MustCompile(`^([A-Za-z]+)_x(-?\d+)_y(-?\d+)$`)

func parseTileXY(base string) (x, y int, prefix string, ok bool) {
	m := tileNameRE.FindStringSubmatch(base)
	if m == nil {
		return 0, 0, "", false
	}
	xv, e1 := strconv.Atoi(m[2])
	yv, e2 := strconv.Atoi(m[3])
	if e1 != nil || e2 != nil {
		return 0, 0, "", false
	}
	return xv, yv, m[1], true
}

// hermiteSeg holds one cubic-Hermite spline segment (tile-local cm).
type hermiteSeg struct {
	P0, T0, P1, T1 [3]float64
}

func parseTileSplines(jsonPath string) []hermiteSeg {
	raw, err := os.ReadFile(jsonPath)
	if err != nil {
		return nil
	}
	type prop struct {
		Name  string          `json:"Name"`
		Type  string          `json:"$type"`
		Value json.RawMessage `json:"Value"`
	}
	type export struct {
		ObjectName string          `json:"ObjectName"`
		Data       []json.RawMessage `json:"Data"`
	}
	var doc struct {
		Exports []export `json:"Exports"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		return nil
	}
	out := []hermiteSeg{}
	for _, e := range doc.Exports {
		if !strings.Contains(e.ObjectName, "SplineMeshComponent") {
			continue
		}
		var sp, rl json.RawMessage
		for _, raw := range e.Data {
			var p prop
			if json.Unmarshal(raw, &p) != nil {
				continue
			}
			switch p.Name {
			case "SplineParams":
				sp = p.Value
			case "RelativeLocation":
				rl = p.Value
			}
		}
		if len(sp) == 0 {
			continue
		}
		// SplineParams.Value is directly a list of nested struct properties
		// (StartPos, StartTangent, StartScale, StartRoll, StartOffset, EndPos,
		// EndScale, EndTangent, EndRoll, EndOffset). Each sub-prop's own Value
		// is a single-element list wrapping a VectorPropertyData (read by
		// readFVector below). No outer wrapper to peel off.
		var subs []prop
		if err := json.Unmarshal(sp, &subs); err != nil {
			continue
		}
		var sPos, sTan, ePos, eTan [3]float64
		var haveS, haveST, haveE, haveET bool
		for _, p := range subs {
			v, ok := readFVector(p.Value)
			if !ok {
				continue
			}
			switch p.Name {
			case "StartPos":
				sPos = v
				haveS = true
			case "StartTangent":
				sTan = v
				haveST = true
			case "EndPos":
				ePos = v
				haveE = true
			case "EndTangent":
				eTan = v
				haveET = true
			}
		}
		if !(haveS && haveST && haveE && haveET) {
			continue
		}
		// Component RelativeLocation: SplineParams are authored relative to
		// the SplineMeshComponent; translation moves them into the tile frame.
		var rx, ry, rz float64
		if len(rl) > 0 {
			if v, ok := readFVector(rl); ok {
				rx, ry, rz = v[0], v[1], v[2]
			}
		}
		out = append(out, hermiteSeg{
			P0: [3]float64{sPos[0] + rx, sPos[1] + ry, sPos[2] + rz},
			T0: sTan, // tangents aren't translated
			P1: [3]float64{ePos[0] + rx, ePos[1] + ry, ePos[2] + rz},
			T1: eTan,
		})
	}
	return out
}

// readFVector decodes either UAssetGUI's "flat" FVector shape
// ([{Name:"X", Value:..}, {Name:"Y", Value:..}, ...]) or the "wrapped"
// shape ([{Value:{X:..,Y:..,Z:..}}]).
func readFVector(v json.RawMessage) ([3]float64, bool) {
	// Wrapped: list with one element whose Value is {X,Y,Z}
	// (UAssetGUI's VectorPropertyData shape — the common case for both
	// SplineParams sub-vectors and component RelativeLocation).
	type xyz struct {
		X float64 `json:"X"`
		Y float64 `json:"Y"`
		Z float64 `json:"Z"`
	}
	var nested []struct {
		Value xyz `json:"Value"`
	}
	if json.Unmarshal(v, &nested) == nil && len(nested) > 0 {
		nz := nested[0].Value
		return [3]float64{nz.X, nz.Y, nz.Z}, true
	}
	// Flat: list of named scalars
	var flat []struct {
		Name  string  `json:"Name"`
		Value float64 `json:"Value"`
	}
	if json.Unmarshal(v, &flat) == nil && len(flat) > 0 {
		var x, y, z float64
		seen := false
		for _, e := range flat {
			switch e.Name {
			case "X":
				x = e.Value
				seen = true
			case "Y":
				y = e.Value
				seen = true
			case "Z":
				z = e.Value
				seen = true
			}
		}
		if seen {
			return [3]float64{x, y, z}, true
		}
	}
	// Direct nested {X,Y,Z} (no list wrapping)
	var direct struct {
		X float64 `json:"X"`
		Y float64 `json:"Y"`
		Z float64 `json:"Z"`
	}
	if json.Unmarshal(v, &direct) == nil && (direct.X != 0 || direct.Y != 0 || direct.Z != 0) {
		return [3]float64{direct.X, direct.Y, direct.Z}, true
	}
	return [3]float64{}, false
}

// hermiteSample produces n+1 evenly-spaced points along a cubic Hermite spline.
func hermiteSample(s hermiteSeg, n int) [][3]float64 {
	out := make([][3]float64, 0, n+1)
	for i := 0; i <= n; i++ {
		t := float64(i) / float64(n)
		t2 := t * t
		t3 := t2 * t
		h00 := 2*t3 - 3*t2 + 1
		h10 := t3 - 2*t2 + t
		h01 := -2*t3 + 3*t2
		h11 := t3 - t2
		p := [3]float64{
			h00*s.P0[0] + h10*s.T0[0] + h01*s.P1[0] + h11*s.T1[0],
			h00*s.P0[1] + h10*s.T0[1] + h01*s.P1[1] + h11*s.T1[1],
			h00*s.P0[2] + h10*s.T0[2] + h01*s.P1[2] + h11*s.T1[2],
		}
		out = append(out, p)
	}
	return out
}

func round7(v float64) float64 {
	return math.Round(v*1e7) / 1e7
}
