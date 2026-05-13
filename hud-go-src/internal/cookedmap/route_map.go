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
//
// SIDE EFFECT — populates `RibbonVertices` on `tt` AND every entry in
// `allTTs` after the rails finish sampling. The per-service path-builder
// reads this map to slice each ribbon's polyline directly from the rails-
// builder's output, guaranteeing the timetable path overlaps the rendered
// rail line bit-identically. The mutation is safe because `package_writer`
// runs the rails phase BEFORE per-service writes for exactly this reason.
func WriteRouteMap(w io.Writer, tt *uasset.Timetable, allTTs []*uasset.Timetable, _ output.RailsGeoJSONOptions) error {
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

	stats, err := Build(Options{
		Workdir:               tt.ExtractDir,
		RouteName:             tt.Route,
		OriginLat:             tt.OriginLat,
		OriginLng:             tt.OriginLng,
		DisplayName:           displayName,
		Country:               output.CountryNameFromCode(tt.CountryCode),
		CountryCode:           output.CountryISOFromCode(tt.CountryCode),
		CrossPakReferenceName: tt.CrossPakReferenceName,
	}, w)
	if err != nil {
		return err
	}
	// Propagate the per-ribbon vertex map to every timetable on this route
	// so all service-path builds see it, regardless of which tt instance
	// they happened to be parsed from.
	if stats.RibbonVertices != nil {
		tt.RibbonVertices = stats.RibbonVertices
		for _, t := range allTTs {
			if t != nil {
				t.RibbonVertices = stats.RibbonVertices
			}
		}
	}
	return nil
}
