package output

import (
	"encoding/json"
	"io"
	"sort"

	"tsw6-timetable/internal/geo"
	"tsw6-timetable/internal/pak/uasset"
)

// WriteRouteMarkersGeoJSON resolves every TrackMarkerProperty (platforms,
// junction routing markers, etc.) to a lat/lng and emits one Point feature
// per marker. For markers with a Location scalar, that's the position; for
// markers with Start+End spans, we use the midpoint.
//
// Properties carry name + marker_type so the HUD can render "Go Via X" hints
// when a marker matches a service's routing instructions.
func WriteRouteMarkersGeoJSON(w io.Writer, tt *uasset.Timetable) (int, error) {
	if tt == nil || (tt.OriginLat == 0 && tt.OriginLng == 0) {
		return writeEmptyRouteMarkers(w, "")
	}
	ribbons := tt.RouteRibbons
	if len(ribbons) == 0 {
		ribbons = tt.Ribbons
	}
	if len(ribbons) == 0 || len(tt.RouteMarkers) == 0 {
		return writeEmptyRouteMarkers(w, tt.Route)
	}
	anchor := geo.NewRouteAnchor(tt.OriginLat, tt.OriginLng)

	type res struct {
		m   *uasset.RouteMarker
		lat float64
		lng float64
	}
	resolved := make([]res, 0, len(tt.RouteMarkers))
	for _, m := range tt.RouteMarkers {
		key := uasset.NormalizeGUID(m.RibbonGUID)
		if key == "" {
			key = m.RibbonGUID
		}
		rib, ok := ribbons[key]
		if !ok || rib == nil || rib.Length <= 0 {
			continue
		}
		// Use Location if set; otherwise the midpoint of Start..End.
		scalar := m.Location
		if scalar == 0 && (m.Start != 0 || m.End != 0) {
			scalar = (m.Start + m.End) / 2
		}
		atLen := float64(scalar) * rib.Length
		dx, dy := geo.ArcDelta(0, 0, rib.TangentX, rib.TangentY, rib.Radius, atLen)
		originX, originY, _ := ribbonWorldOrigin(rib)
		lat, lng := anchor.WorldToLatLng((originX+dx)/100.0, (originY+dy)/100.0)
		resolved = append(resolved, res{m: m, lat: lat, lng: lng})
	}
	sort.Slice(resolved, func(i, j int) bool {
		if resolved[i].m.Name != resolved[j].m.Name {
			return resolved[i].m.Name < resolved[j].m.Name
		}
		return resolved[i].m.RibbonGUID < resolved[j].m.RibbonGUID
	})

	feats := make([]any, 0, len(resolved))
	for _, r := range resolved {
		props := map[string]any{
			"feature_kind": "track_marker",
			"name":         r.m.Name,
			"marker_type":  r.m.MarkerType,
			"line_side":    r.m.LineSide,
			"ribbon_guid":  r.m.RibbonGUID,
			"location":     r.m.Location,
			"start":        r.m.Start,
			"end":          r.m.End,
			"tile":         r.m.TileName,
		}
		feats = append(feats, map[string]any{
			"type":       "Feature",
			"properties": props,
			"geometry": map[string]any{
				"type":        "Point",
				"coordinates": []float64{round7(r.lng), round7(r.lat)},
			},
		})
	}
	doc := map[string]any{
		"type":     "FeatureCollection",
		"name":     "route-markers-" + tt.Route,
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

func writeEmptyRouteMarkers(w io.Writer, route string) (int, error) {
	return writeEmptyFC(w, "route-markers-"+route)
}
