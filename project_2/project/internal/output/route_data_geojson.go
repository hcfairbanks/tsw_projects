package output

import (
	"bytes"
	"encoding/json"
	"io"

	"tsw6-timetable/internal/pak/uasset"
)

// WriteRouteDataJSON emits the route-level file: a GeoJSON FeatureCollection
// (rails + track-feature overlays + platforms + signals + switches) plus
// route metadata and a `trains[]` list of every unique formation referenced
// by services on the route.
//
// `tts` is every timetable parsed for the route. Geometry is taken from the
// first one with usable ribbons; trains[] aggregates across all of them.
//
// Stored as `.json` (not `.geojson`) because the top-level `trains`,
// `route`, `country`, `origin_lat`, `origin_lng` fields are foreign
// extensions to GeoJSON. Strict GeoJSON readers ignore unknown top-level
// keys, so the file is still loadable as a FeatureCollection.
func WriteRouteDataJSON(w io.Writer, tts []*uasset.Timetable, opts RailsGeoJSONOptions) (int, error) {
	if len(tts) == 0 {
		return 0, nil
	}

	// Pick the timetable that owns the rail geometry (first one with ribbons).
	var geomTT *uasset.Timetable
	for _, tt := range tts {
		if tt != nil && (len(tt.RouteRibbons) > 0 || len(tt.Ribbons) > 0) {
			geomTT = tt
			break
		}
	}
	if geomTT == nil {
		geomTT = tts[0]
	}

	// Collect features from each component writer.
	railsFeats, err := serializeFeatures(func(b *bytes.Buffer) error {
		_, e := WriteRailsMergedArc(b, geomTT, opts)
		return e
	})
	if err != nil {
		return 0, err
	}
	platFeats, err := serializeFeatures(func(b *bytes.Buffer) error {
		_, e := WritePlatformsGeoJSON(b, geomTT)
		return e
	})
	if err != nil {
		return 0, err
	}
	sigFeats, err := serializeFeatures(func(b *bytes.Buffer) error {
		_, e := WriteSignalsGeoJSON(b, geomTT)
		return e
	})
	if err != nil {
		return 0, err
	}
	swFeats, err := serializeFeatures(func(b *bytes.Buffer) error {
		_, e := WriteSwitchesGeoJSON(b, geomTT)
		return e
	})
	if err != nil {
		return 0, err
	}
	tfFeats, err := serializeFeatures(func(b *bytes.Buffer) error {
		_, e := WriteTrackFeaturesGeoJSON(b, geomTT, opts)
		return e
	})
	if err != nil {
		return 0, err
	}
	cssFeats, err := serializeFeatures(func(b *bytes.Buffer) error {
		_, e := WriteCarStopSignsGeoJSON(b, geomTT)
		return e
	})
	if err != nil {
		return 0, err
	}
	rmFeats, err := serializeFeatures(func(b *bytes.Buffer) error {
		_, e := WriteRouteMarkersGeoJSON(b, geomTT)
		return e
	})
	if err != nil {
		return 0, err
	}

	// Render order: track-feature overlays FIRST so platform/siding colour
	// sits beneath the merged-rails MultiLineString (the base). Then point
	// overlays. CarStopSigns last so they sit on top of platform polygons.
	all := make([]json.RawMessage, 0,
		len(railsFeats)+len(tfFeats)+len(platFeats)+len(sigFeats)+len(swFeats)+len(cssFeats)+len(rmFeats))
	all = append(all, tfFeats...)
	all = append(all, railsFeats...)
	all = append(all, platFeats...)
	all = append(all, sigFeats...)
	all = append(all, swFeats...)
	all = append(all, rmFeats...)
	all = append(all, cssFeats...)

	doc := map[string]any{
		"type":       "FeatureCollection",
		"name":       RouteDisplayName(geomTT.Route),
		"route":      geomTT.Route,
		"country":    RouteCountry(geomTT.Route),
		"origin_lat": geomTT.OriginLat,
		"origin_lng": geomTT.OriginLng,
		"features":   all,
	}
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	enc.SetEscapeHTML(false)
	if err := enc.Encode(doc); err != nil {
		return 0, err
	}
	return len(all), nil
}

func serializeFeatures(write func(*bytes.Buffer) error) ([]json.RawMessage, error) {
	var buf bytes.Buffer
	if err := write(&buf); err != nil {
		return nil, err
	}
	return extractFeatures(buf.Bytes())
}

// extractFeatures pulls the `features` array out of a serialized GeoJSON
// FeatureCollection.
func extractFeatures(b []byte) ([]json.RawMessage, error) {
	if len(bytes.TrimSpace(b)) == 0 {
		return nil, nil
	}
	var doc struct {
		Features []json.RawMessage `json:"features"`
	}
	if err := json.Unmarshal(b, &doc); err != nil {
		return nil, err
	}
	return doc.Features, nil
}

// (Train aggregation removed: route_<Route>.json no longer carries a
// trains[] field. Per-service JSONs already carry full consist details
// via trains[].consists[]; the upload pipeline dedupes server-side from
// those.)
