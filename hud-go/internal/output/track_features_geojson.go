package output

import (
	"encoding/json"
	"io"
	"sort"
	"strings"

	"hud-go/internal/geo"
	"hud-go/internal/pak/uasset"
)

// WriteTrackFeaturesGeoJSON walks LinkedPlatforms and emits one LineString
// feature per ribbon they reference, tagged with feature_type so a viewer
// can colour platforms vs sidings vs lines.
//
// Approximation: each LinkedPlatform points at one ribbon; we treat that
// whole ribbon as belonging to the platform's structure. Often a ribbon's
// length closely matches the actual platform extent (~50-100m on IoW); for
// cases where it doesn't, parsing PlatformWaitArea collision boxes would
// give pixel-precise extents — deferred until we need it.
func WriteTrackFeaturesGeoJSON(w io.Writer, tt *uasset.Timetable, opts RailsGeoJSONOptions) (int, error) {
	if tt == nil {
		return 0, nil
	}
	if tt.OriginLat == 0 && tt.OriginLng == 0 {
		return 0, nil
	}
	ribbons := tt.RouteRibbons
	if len(ribbons) == 0 {
		ribbons = tt.Ribbons
	}
	if len(ribbons) == 0 || len(tt.LinkedPlatforms) == 0 {
		return writeEmptyFC(w, "track_features-"+tt.Route)
	}
	if opts.SampleStepMeters <= 0 {
		opts.SampleStepMeters = 5
	}
	if opts.MinSamples < 2 {
		opts.MinSamples = 2
	}
	if opts.MaxSamples < opts.MinSamples {
		opts.MaxSamples = 400
	}
	anchor := geo.NewRouteAnchor(tt.OriginLat, tt.OriginLng)

	type ribFeat struct {
		FeatureType string
		Location    string
		Structure   string
		Number      string
	}
	byRibbon := map[string]ribFeat{}
	addEntry := func(name, ribbonGUID string, location, structure, number string) {
		var k platformKey
		if structure != "" {
			k = platformKey{Location: location, Structure: structure, StructureNumber: number}
		} else {
			k = splitPlatformName(name)
		}
		if k.Structure == "" {
			return
		}
		ft := featureTypeForStructure(k.Structure)
		if ft == "" {
			return
		}
		canon := uasset.NormalizeGUID(ribbonGUID)
		if canon == "" {
			canon = ribbonGUID
		}
		if _, exists := byRibbon[canon]; exists {
			return // first-write-wins
		}
		byRibbon[canon] = ribFeat{
			FeatureType: ft,
			Location:    k.Location,
			Structure:   k.Structure,
			Number:      k.StructureNumber,
		}
	}
	// Schedule data is the primary source — every platform/siding/line a
	// service touches is here, with structure already split.
	for i := range tt.Services {
		svc := &tt.Services[i]
		for _, it := range svc.Schedule {
			if it.RibbonGUID == "" || it.Structure == "" {
				continue
			}
			addEntry("", it.RibbonGUID, it.Location, it.Structure, it.StructureNumber)
		}
	}
	// LinkedPlatforms supplements with tile-only entries (physically present
	// platforms no service calls at).
	for _, lp := range tt.LinkedPlatforms {
		addEntry(lp.Name, lp.RibbonGUID, "", "", "")
	}

	keys := make([]string, 0, len(byRibbon))
	for k := range byRibbon {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	feats := make([]any, 0, len(keys))
	for _, k := range keys {
		rib, ok := ribbons[k]
		if !ok || rib.Length <= 0 {
			continue
		}
		coords := sampleRibbonLatLng(rib, anchor, opts)
		if len(coords) < 2 {
			continue
		}
		rf := byRibbon[k]
		props := map[string]any{
			"feature_type":     rf.FeatureType,
			"location":         rf.Location,
			"structure":        rf.Structure,
			"structure_number": rf.Number,
			"ribbon_guid":      rib.GUID,
			"length_m":         round1(rib.Length / 100.0),
		}
		feats = append(feats, map[string]any{
			"type":       "Feature",
			"properties": props,
			"geometry": map[string]any{
				"type":        "LineString",
				"coordinates": coords,
			},
		})
	}

	doc := map[string]any{
		"type":     "FeatureCollection",
		"name":     "track_features-" + tt.Route,
		"features": feats,
	}
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	enc.SetEscapeHTML(false)
	if err := enc.Encode(doc); err != nil {
		return 0, err
	}
	return len(feats), nil
}

// featureTypeForStructure maps the parsed structure word to a stable feature
// type a viewer can switch on.
func featureTypeForStructure(structure string) string {
	switch strings.ToLower(structure) {
	case "platform":
		return "platform_track"
	case "siding":
		return "siding_track"
	case "line":
		return "line_track"
	case "track":
		return "running_track"
	}
	return ""
}
