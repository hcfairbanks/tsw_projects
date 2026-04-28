package extractor

import (
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"tsw6-timetable/internal/pak/uasset"
)

// scanRibbons walks every tile .umap under <extractRoot>/.../Content/Map/Tiles,
// converts each to JSON via UAssetGUI, and extracts NetworkRibbon records.
//
// Returns a map keyed by RibbonGuid so the package writer can resolve a
// schedule item's ribbon to its geometry.
//
// Only ribbons with curve geometry (Length > 0) are retained. Ribbons without
// a direct WorldLocation anchor are still included (HasAnchor=false); the
// caller can skip them or attempt node-graph propagation later.
func (e *Extractor) scanRibbons(extractRoot string) map[string]*uasset.Ribbon {
	out, _, _, _, _, _ := e.scanTileFeatures(extractRoot)
	return out
}

// scanTileFeatures is the same per-tile walk as scanRibbons but ALSO collects
// LinkedPlatforms, Signals, Switches, CarStopSigns, and RouteMarkers from each
// tile in a single pass. These supplement schedule-derived data with any
// physical track features (platforms, junction routing markers, cab stop signs)
// that the timetable doesn't reference directly.
func (e *Extractor) scanTileFeatures(extractRoot string) (map[string]*uasset.Ribbon, []*uasset.LinkedPlatform, []*uasset.Signal, []*uasset.Switch, []*uasset.CarStopSign, []*uasset.RouteMarker) {
	out := map[string]*uasset.Ribbon{}
	platforms := []*uasset.LinkedPlatform{}
	signals := []*uasset.Signal{}
	switches := []*uasset.Switch{}
	carStopSigns := []*uasset.CarStopSign{}
	routeMarkers := []*uasset.RouteMarker{}
	var tileAssets []string
	_ = filepath.WalkDir(extractRoot, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		if !strings.HasSuffix(strings.ToLower(path), ".umap") {
			return nil
		}
		// Under .../Content/Map/Tiles/<TileName>.umap
		parts := strings.Split(filepath.ToSlash(path), "/")
		if len(parts) < 3 {
			return nil
		}
		if !strings.EqualFold(parts[len(parts)-2], "Tiles") {
			return nil
		}
		tileAssets = append(tileAssets, path)
		return nil
	})
	if len(tileAssets) == 0 {
		return out, platforms, signals, switches, carStopSigns, routeMarkers
	}
	if e.cfg.Debug {
		fmt.Fprintf(os.Stderr, "[ribbon-scan] %d tile maps to scan\n", len(tileAssets))
	}
	converted := 0
	parsed := 0
	for _, ua := range tileAssets {
		jsonPath := ua + ".json"
		alreadyConverted := false
		// Skip re-conversion if already present (idempotent across retries).
		if _, err := os.Stat(jsonPath); err == nil {
			alreadyConverted = true
		} else {
			if err := e.runUAssetGUI(ua, jsonPath); err != nil {
				continue
			}
			converted++
		}
		tileName := strings.TrimSuffix(filepath.Base(ua), ".umap")
		rs, plats, sigs, sws, css, rms, err := uasset.ParseTileFeaturesFromUmap(jsonPath, tileName)
		// Stream-delete the JSON we just produced so disk usage stays flat
		// across the scan (each tile JSON can be 5-50 MB; 1700+ tiles for BP
		// would otherwise consume ~30-50 GB just in JSON output). In --debug
		// mode keep them around so post-mortem inspection still works.
		// Only delete what we just converted, not pre-existing files.
		if !e.cfg.Debug && !alreadyConverted {
			_ = os.Remove(jsonPath)
		}
		if err != nil {
			continue
		}
		platforms = append(platforms, plats...)
		signals = append(signals, sigs...)
		switches = append(switches, sws...)
		carStopSigns = append(carStopSigns, css...)
		routeMarkers = append(routeMarkers, rms...)
		for _, r := range rs {
			// Key by canonical (lowercase, no-separators) GUID so schedule-side
			// lookups (which come via fmtGUID in uppercase 8-8-8-8 form) match
			// regardless of the raw case/format UAssetGUI emits for ribbons.
			key := uasset.NormalizeGUID(r.GUID)
			if key == "" {
				key = r.GUID
			}
			// Prefer anchored copies if a ribbon appears in multiple tiles.
			existing, ok := out[key]
			if !ok {
				out[key] = r
				parsed++
				continue
			}
			if r.HasAnchor && !existing.HasAnchor {
				out[key] = r
			}
		}
	}
	if e.cfg.Debug {
		fmt.Fprintf(os.Stderr, "[ribbon-scan] converted %d new tiles, indexed %d unique ribbons\n", converted, parsed)
		// Dump full ribbon map so we can inspect HasAnchor / node links post-run.
		dumpPath := filepath.Join(extractRoot, "_ribbon_map.json")
		if f, err := os.Create(dumpPath); err == nil {
			enc := json.NewEncoder(f)
			enc.SetIndent("", "  ")
			_ = enc.Encode(out)
			_ = f.Close()
			fmt.Fprintf(os.Stderr, "[ribbon-scan] dumped ribbon map to %s\n", dumpPath)
		}
		fmt.Fprintf(os.Stderr, "[ribbon-scan] found %d LinkedPlatforms, %d signals, %d switches, %d car-stop-signs, %d route-markers\n",
			len(platforms), len(signals), len(switches), len(carStopSigns), len(routeMarkers))
	}
	return out, platforms, signals, switches, carStopSigns, routeMarkers
}
