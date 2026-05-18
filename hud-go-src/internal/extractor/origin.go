package extractor

import (
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"hud-go/internal/geo"
)

// hardcodedOrigins is the per-codename fallback table for routes whose
// persistent-map .uexp byte layout has historically been hard to scan
// reliably. The Training Centre is the canonical case — it ships with
// every copy of TSW (always at the same in-game location), the asset
// path is stable, and a wrong origin makes every tutorial map land in
// the Pacific. We extract the values once via the find-origin CLI and
// commit them here so any future scanner regression has a known-good
// floor for these specific routes.
//
// Keys are the pak codename (case-sensitive — matches route.Name as
// passed in by the extractor caller). Values are (lat, lng) in degrees.
var hardcodedOrigins = map[string][2]float64{
	"TrainingCentre": {51.11568832397461, 6.20970296859741},
}
// findRouteOrigin locates the persistent-level map file for a route and
// extracts the route's origin lat/lng from it. Returns (0, 0) if no
// strategy can resolve it.
//
// Strategies are tried in priority order:
//
//  1. Hard-coded origin map (TrainingCentre etc.) — always wins when
//     the codename matches. The base-game routes are known-stable so
//     we never want to depend on a heuristic to find their origin.
//  2. TSW6 modern layout: `Content/Map/<X>Map.uexp` — the file's
//     grandparent is "Content" and its parent is exactly "Map".
//  3. TSW2 / early-TSW3 legacy layout: `Content/<RouteName>Map/<X>Map.uexp`
//     — grandparent is "Content" and parent equals the route codename
//     suffixed with "Map" (case-insensitive). Sand Patch Grade is the
//     reference pak that ships this layout in the modern install.
//
// Each strategy walks the extracted tree separately and rejects:
//   - any path containing a "Tiles" segment (per-tile umaps live under
//     Content/Map/Tiles/, never the persistent-level file)
//   - any folder whose name happens to end in "Map" but lives deeper
//     than Content/ (Audio/.../LCDMap/, Collectables/.../RouteMap/,
//     View/.../Heightmap/) — their .uexp bytes can spuriously pass the
//     lat/lng plausibility filter and yield Pacific-ocean coords.
//
// Strategy 1 is consulted first, then 2, then 3. We return the first
// origin that produces a plausible lat/lng pair (per geo.ExtractOrigin
// FromUExp) so a misnamed file in the modern slot doesn't preclude
// finding a valid one in the legacy slot.
func (e *Extractor) findRouteOrigin(extractRoot, routeName string) (lat, lng float64) {
	if v, ok := hardcodedOrigins[routeName]; ok {
		if e.cfg.Debug {
			os.Stderr.WriteString("[origin] " + routeName + " -> hardcoded\n")
		}
		return v[0], v[1]
	}

	// Strategy 2: TSW6 modern. Parent is exactly "Map".
	if lat, lng, ok := scanOriginByLayout(extractRoot, func(parts []string) bool {
		if len(parts) < 3 {
			return false
		}
		return strings.EqualFold(parts[len(parts)-3], "Content") &&
			strings.EqualFold(parts[len(parts)-2], "Map")
	}); ok {
		if e.cfg.Debug {
			os.Stderr.WriteString("[origin] " + routeName + " -> TSW6 modern Content/Map/\n")
		}
		return lat, lng
	}

	// Strategy 3: TSW2 / legacy. Parent ends in "Map" (matches
	// "<RouteName>Map") AND grandparent is "Content".
	if lat, lng, ok := scanOriginByLayout(extractRoot, func(parts []string) bool {
		if len(parts) < 3 {
			return false
		}
		parent := strings.ToLower(parts[len(parts)-2])
		return strings.EqualFold(parts[len(parts)-3], "Content") &&
			parent != "map" && // already handled by strategy 2
			strings.HasSuffix(parent, "map")
	}); ok {
		if e.cfg.Debug {
			os.Stderr.WriteString("[origin] " + routeName + " -> legacy Content/<X>Map/\n")
		}
		return lat, lng
	}

	return 0, 0
}

// scanOriginByLayout walks extractRoot for .uexp files whose path parts
// match the supplied predicate (after stripping "Tiles" subtree files),
// returning the first one that produces a plausible origin.
func scanOriginByLayout(extractRoot string, match func(parts []string) bool) (lat, lng float64, ok bool) {
	_ = filepath.WalkDir(extractRoot, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || ok {
			return nil
		}
		if !strings.HasSuffix(strings.ToLower(path), ".uexp") {
			return nil
		}
		parts := strings.Split(filepath.ToSlash(path), "/")
		for _, p := range parts {
			if strings.EqualFold(p, "Tiles") {
				return nil
			}
		}
		if !match(parts) {
			return nil
		}
		la, ln, err := geo.ExtractOriginFromUExp(path)
		if err == nil && la != 0 && ln != 0 {
			lat, lng, ok = la, ln, true
		}
		return nil
	})
	return
}
