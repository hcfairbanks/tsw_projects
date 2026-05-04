// tc-map: standalone Training-Centre map-only extractor.
//
// Why this exists: the full extractor takes too long for iteration when all
// we want is the rails GeoJSON to load in the viewer. This program runs the
// minimum slice of the pipeline:
//
//   pak unpack (repak) -> origin uexp -> tile umap convert (UAssetGUI)
//   -> ParseTileFeaturesFromUmap -> WriteRailsMergedArc -> Desktop file.
//
// It always sets TSW6_DEBUG_INSTR=1 so unknown destination-property warnings
// from the parser surface on stderr — that was the original ask.
//
// Usage: tc-map [--out PATH]
//
// Defaults: writes to %USERPROFILE%\Desktop\TrainingCentre_rails_<ts>.geojson.
package main

import (
	"flag"
	"fmt"
	"io/fs"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"hud-go/internal/geo"
	"hud-go/internal/output"
	"hud-go/internal/pak"
	"hud-go/internal/pak/uasset"
)

const (
	tswPath   = `D:\SteamLibrary\steamapps\common\Train Sim World 6`
	repakBin  = `C:\Users\hcfai\Desktop\applications_2\hud-go\repak.exe`
	uassetBin = `C:\Users\hcfai\Desktop\applications_2\hud-go\UAssetGUI.exe`
	target    = "TrainingCentre"
)

func main() {
	defaultOut := filepath.Join(
		os.Getenv("USERPROFILE"), "Desktop",
		fmt.Sprintf("TrainingCentre_rails_%s.geojson", time.Now().Format("20060102_150405")),
	)
	out := flag.String("out", defaultOut, "output GeoJSON path")
	flag.Parse()

	// The whole point of this CLI is to surface unknown-dest-prop warnings.
	os.Setenv("TSW6_DEBUG_INSTR", "1")

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
		log.Fatalf("route %q not found under %s", target, tswPath)
	}
	fmt.Fprintf(os.Stderr, "[tc-map] route=%s pak=%s\n", route.Name, route.PakPath)

	workDir, err := os.MkdirTemp("", "tc-map-*")
	if err != nil {
		log.Fatalf("mkdtemp: %v", err)
	}
	defer os.RemoveAll(workDir)
	fmt.Fprintf(os.Stderr, "[tc-map] workdir=%s\n", workDir)

	t0 := time.Now()
	if err := runRepak(route.PakPath, workDir); err != nil {
		log.Fatalf("repak unpack: %v", err)
	}
	fmt.Fprintf(os.Stderr, "[tc-map] repak unpack done in %s\n", time.Since(t0).Round(time.Millisecond))

	lat, lng := findOrigin(workDir)
	if lat == 0 && lng == 0 {
		log.Fatalf("origin lat/lng not found")
	}
	fmt.Fprintf(os.Stderr, "[tc-map] origin=(%v, %v)\n", lat, lng)

	ribbons := scanTileRibbons(workDir)
	if len(ribbons) == 0 {
		log.Fatalf("no ribbons parsed")
	}
	fmt.Fprintf(os.Stderr, "[tc-map] ribbons=%d\n", len(ribbons))

	tt := &uasset.Timetable{
		Route:        target,
		OriginLat:    lat,
		OriginLng:    lng,
		Ribbons:      ribbons,
		RouteRibbons: ribbons,
	}

	if err := os.MkdirAll(filepath.Dir(*out), 0o755); err != nil {
		log.Fatalf("mkdir output dir: %v", err)
	}
	f, err := os.Create(*out)
	if err != nil {
		log.Fatalf("create output: %v", err)
	}
	defer f.Close()
	n, err := output.WriteRailsMergedArc(f, tt, output.DefaultRailsOptions())
	if err != nil {
		log.Fatalf("write rails geojson: %v", err)
	}
	fmt.Fprintf(os.Stderr, "[tc-map] wrote %d path(s) to %s\n", n, *out)
}

func runRepak(pakPath, dest string) error {
	cmd := exec.Command(repakBin, "unpack", "--output", dest, pakPath)
	cmd.Stdout = os.Stderr
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

func runUAssetGUI(uassetPath, jsonPath string) error {
	cmd := exec.Command(uassetBin, "tojson", uassetPath, jsonPath, "VER_UE4_27")
	// Discard the per-file noise UAssetGUI emits — keep only the parser's
	// own [unknown-dest-prop] lines visible on stderr.
	cmd.Stdout = nil
	cmd.Stderr = nil
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

func scanTileRibbons(extractRoot string) map[string]*uasset.Ribbon {
	out := map[string]*uasset.Ribbon{}
	var tileAssets []string
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
		tileAssets = append(tileAssets, p)
		return nil
	})
	fmt.Fprintf(os.Stderr, "[tc-map] %d tile umaps\n", len(tileAssets))

	t0 := time.Now()
	for i, ua := range tileAssets {
		jsonPath := ua + ".json"
		if _, err := os.Stat(jsonPath); err != nil {
			if err := runUAssetGUI(ua, jsonPath); err != nil {
				continue
			}
		}
		tileName := strings.TrimSuffix(filepath.Base(ua), ".umap")
		rs, err := uasset.ParseRibbonsFromUmap(jsonPath, tileName)
		if err != nil {
			continue
		}
		for _, r := range rs {
			key := uasset.NormalizeGUID(r.GUID)
			if key == "" {
				key = r.GUID
			}
			existing, ok := out[key]
			if !ok || (r.HasAnchor && !existing.HasAnchor) {
				out[key] = r
			}
		}
		if (i+1)%10 == 0 || i+1 == len(tileAssets) {
			fmt.Fprintf(os.Stderr, "[tc-map]   %d/%d tiles parsed (%s)\n",
				i+1, len(tileAssets), time.Since(t0).Round(time.Second))
		}
	}
	return out
}
