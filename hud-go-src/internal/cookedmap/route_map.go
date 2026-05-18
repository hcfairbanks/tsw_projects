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

	// Collect train classes from EVERY pak this route is composed of.
	// allTTs may include sibling-pak timetables (e.g. Boston Sprinter
	// spans BostonProvidence + BostonProvidenceGameplayPack + BPEAcela);
	// each carries its own ExtractDir whose RVD_*.uasset files contribute
	// to the route's canonical class set.
	seen := map[string]bool{tt.ExtractDir: true}
	workdirs := []string{tt.ExtractDir}
	for _, t := range allTTs {
		if t == nil || t.ExtractDir == "" || seen[t.ExtractDir] {
			continue
		}
		seen[t.ExtractDir] = true
		workdirs = append(workdirs, t.ExtractDir)
	}
	trainClasses := CollectTrainClasses(workdirs)

	// Per-service pre-baked path data from the route's RouteTimetableDataTrack
	// uassets. When present, the per-service path-builder uses these exact
	// (ribbon, fraction) breadcrumbs instead of routing schedule stops
	// through the proximity-weighted ribbon graph — which silently mispicks
	// at parallel-ribbon stretches. Propagated to every timetable on this
	// route via the same fan-out as RibbonVertices below.
	dataTracks := CollectDataTracks(workdirs)
	if dataTracks != nil {
		tt.ServiceTrackData = dataTracks
		for _, t := range allTTs {
			if t != nil {
				t.ServiceTrackData = dataTracks
			}
		}
	}

	// route_*.json carries the ROUTE-LEVEL CPR (this pak's own mount,
	// from RouteDefinition). Per-asset CPRs travel in each per-service
	// JSON instead. Fall back to the per-asset CPR only if the
	// RouteDefinition didn't supply one (cargo DLCs without their own
	// RouteDefinition asset).
	routeCPR := tt.RouteCrossPakReferenceName
	if routeCPR == "" {
		routeCPR = tt.CrossPakReferenceName
	}
	stats, err := Build(Options{
		Workdir:               tt.ExtractDir,
		RouteName:             tt.Route,
		OriginLat:             tt.OriginLat,
		OriginLng:             tt.OriginLng,
		DisplayName:           displayName,
		Country:               output.CountryNameFromCode(tt.CountryCode),
		CountryCode:           output.CountryISOFromCode(tt.CountryCode),
		CrossPakReferenceName: routeCPR,
		TrainClasses:          trainClasses,
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
