package cookedmap

import (
	"fmt"
	"io"

	"hud-go/internal/output"
	"hud-go/internal/pak/uasset"
)

// WriteRouteMap is an output.RouteMapWriter that drives Build using metadata
// from the geometry-owning timetable. The web extractor wires this in via
// output.WritePackageWith so the per-route zip's `route_<X>.json` is built
// from the cooked-pak binaries instead of the legacy JSON-conversion path.
//
// Requires tt.ExtractDir to be set (extractor.Config.KeepWorkDir = true).
// Returns a descriptive error otherwise rather than silently producing an
// empty FeatureCollection.
func WriteRouteMap(w io.Writer, tt *uasset.Timetable, _ []*uasset.Timetable, _ output.RailsGeoJSONOptions) error {
	if tt == nil {
		return fmt.Errorf("cookedmap: nil timetable")
	}
	if tt.ExtractDir == "" {
		return fmt.Errorf("cookedmap: tt.ExtractDir is empty (set Config.KeepWorkDir before calling Extract)")
	}

	displayName := tt.RouteDisplayName
	if displayName == "" {
		displayName = output.RouteDisplayName(tt.Route)
	}

	_, err := Build(Options{
		Workdir:               tt.ExtractDir,
		RouteName:             tt.Route,
		OriginLat:             tt.OriginLat,
		OriginLng:             tt.OriginLng,
		DisplayName:           displayName,
		Country:               output.CountryNameFromCode(tt.CountryCode),
		CountryCode:           output.CountryISOFromCode(tt.CountryCode),
		CrossPakReferenceName: tt.CrossPakReferenceName,
	}, w)
	return err
}
