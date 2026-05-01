package extractor

import (
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"hud-go/internal/geo"
)

// findRouteOrigin locates the persistent-level map file for a route and
// extracts the route's origin lat/lng from it. Returns (0, 0) if not found.
//
// The persistent map lives at <routeDLC>/Content/Map/<RouteName>Map.uasset
// (and its .uexp sibling). Filenames vary slightly — e.g. IsleOfWight uses
// "ISleOfWightMap.uasset" — so we walk the Content/Map folder and pick the
// first .umap/.uasset whose accompanying .uexp yields a valid origin.
func (e *Extractor) findRouteOrigin(extractRoot, routeName string) (lat, lng float64) {
	var candidates []string
	_ = filepath.WalkDir(extractRoot, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() {
			return nil
		}
		// Persistent level umap files sit directly under a "Map" folder
		// (tiles are under "Map/Tiles/", which we want to skip).
		if !strings.HasSuffix(strings.ToLower(path), ".uexp") {
			return nil
		}
		parts := strings.Split(filepath.ToSlash(path), "/")
		mapIdx := -1
		for i, p := range parts {
			if strings.EqualFold(p, "Map") {
				mapIdx = i
			}
		}
		if mapIdx < 0 || mapIdx == len(parts)-1 {
			return nil
		}
		// Only take the file directly under Content/Map/ (parent is "Map", not a nested folder).
		parent := parts[len(parts)-2]
		if !strings.EqualFold(parent, "Map") {
			return nil
		}
		candidates = append(candidates, path)
		return nil
	})
	for _, p := range candidates {
		lat, lng, err := geo.ExtractOriginFromUExp(p)
		if err == nil && lat != 0 && lng != 0 {
			if e.cfg.Debug {
				os.Stderr.WriteString("[origin] " + routeName + " -> " + filepath.Base(p) + "\n")
			}
			return lat, lng
		}
	}
	return 0, 0
}
