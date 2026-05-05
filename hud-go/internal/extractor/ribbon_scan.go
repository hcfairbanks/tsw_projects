package extractor

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"hud-go/internal/pak/uasset"
)

// scanRibbons walks every tile .umap under <extractRoot>/.../Content/Map/Tiles
// and extracts NetworkRibbon records.
//
// Returns a map keyed by RibbonGuid so the package writer can resolve a
// schedule item's ribbon to its geometry.
func (e *Extractor) scanRibbons(extractRoot string) map[string]*uasset.Ribbon {
	out, _, _, _, _, _ := e.scanTileFeatures(extractRoot)
	return out
}

// scanTileFeatures walks every tile .umap under .../Map/Tiles and extracts
// NetworkRibbon records plus all per-tile features (LinkedPlatforms,
// Signals, Switches, CarStopSigns, RouteMarkers) in a single pass.
//
// Reads the cooked .umap binaries directly via the in-process
// uasset.ParseCookedXxxFromUmap walkers — no UAssetGUI conversion, no
// intermediate .umap.json files. ~200-500x faster than the legacy
// JSON-roundtrip path it replaced.
//
// Returns the same data shape as before so callers (extractor.Extract,
// global ribbon-index merge, etc.) are unaffected. CookedRibbons are
// adapted to the existing *uasset.Ribbon struct populating CachedStartX/Y
// + HasCachedStart=true so downstream lat/lng resolution lights up the
// world-frame fast path.
func (e *Extractor) scanTileFeatures(extractRoot string) (map[string]*uasset.Ribbon, []*uasset.LinkedPlatform, []*uasset.Signal, []*uasset.Switch, []*uasset.CarStopSign, []*uasset.RouteMarker) {
	out := map[string]*uasset.Ribbon{}
	platforms := []*uasset.LinkedPlatform{}
	signals := []*uasset.Signal{}
	switches := []*uasset.Switch{}
	carStopSigns := []*uasset.CarStopSign{}
	routeMarkers := []*uasset.RouteMarker{}
	var tilePaths []string
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
		tilePaths = append(tilePaths, path)
		return nil
	})
	if len(tilePaths) == 0 {
		return out, platforms, signals, switches, carStopSigns, routeMarkers
	}
	if e.cfg.Debug {
		fmt.Fprintf(os.Stderr, "[ribbon-scan] %d tile maps to scan (cooked)\n", len(tilePaths))
	}
	parsed := 0
	for _, ua := range tilePaths {
		tileName := strings.TrimSuffix(filepath.Base(ua), ".umap")
		// Ribbons (TT_*.umap typically; non-TT tiles return empty silently).
		if rs, err := uasset.ParseCookedRibbonsFromUmap(ua, tileName); err == nil {
			for i := range rs {
				cr := &rs[i]
				key := uasset.NormalizeGUID(cr.RibbonGUID)
				if key == "" {
					key = cr.RibbonGUID
				}
				r := &uasset.Ribbon{
					GUID:           cr.RibbonGUID,
					TileName:       cr.TileName,
					StartNodeGUID:  cr.StartNodeGUID,
					EndNodeGUID:    cr.EndNodeGUID,
					TangentX:       cr.TangentX,
					TangentY:       cr.TangentY,
					Radius:         cr.Radius,
					Length:         cr.Length,
					CachedStartX:   cr.StartX,
					CachedStartY:   cr.StartY,
					CachedStartZ:   cr.StartZ,
					HasCachedStart: true,
					IsClothoid:     cr.CurveClass == "NetworkCurveClothoidSpiral",
				}
				// Prefer ribbons that already have CachedStart populated.
				// All cooked-parsed ribbons do, so de-dup by first-seen.
				if _, ok := out[key]; !ok {
					out[key] = r
					parsed++
				}
			}
		}
		// Platforms / signals / switches / cab-stops / route-markers.
		if fts, err := uasset.ParseCookedFeaturesFromUmap(ua, tileName); err == nil && fts != nil {
			platforms = append(platforms, fts.Platforms...)
			signals = append(signals, fts.Signals...)
			switches = append(switches, fts.Switches...)
			carStopSigns = append(carStopSigns, fts.CarStopSigns...)
			routeMarkers = append(routeMarkers, fts.RouteMarkers...)
		}
	}
	if e.cfg.Debug {
		fmt.Fprintf(os.Stderr, "[ribbon-scan] indexed %d unique ribbons across %d tiles (cooked)\n", parsed, len(tilePaths))
		fmt.Fprintf(os.Stderr, "[ribbon-scan] found %d LinkedPlatforms, %d signals, %d switches, %d car-stop-signs, %d route-markers\n",
			len(platforms), len(signals), len(switches), len(carStopSigns), len(routeMarkers))
	}
	return out, platforms, signals, switches, carStopSigns, routeMarkers
}
