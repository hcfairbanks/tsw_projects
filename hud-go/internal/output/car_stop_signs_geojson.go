package output

import (
	"encoding/json"
	"io"
	"sort"

	"hud-go/internal/geo"
	"hud-go/internal/pak/uasset"
)

// WriteCarStopSignsGeoJSON resolves every CarStopSign in the timetable to a
// lat/lng (parent ribbon's geometry walked by Location × arc_length) and
// emits one Point feature per sign. Properties carry max_rail_vehicles so the
// HUD can pick the right sign for the formation actually running a service.
//
// max_rail_vehicles=0 means "default/any train length"; non-zero values mean
// the sign is specifically for trains of that car count or shorter.
func WriteCarStopSignsGeoJSON(w io.Writer, tt *uasset.Timetable) (int, error) {
	if tt == nil || (tt.OriginLat == 0 && tt.OriginLng == 0) {
		return writeEmptyCarStopSigns(w, "")
	}
	ribbons := tt.RouteRibbons
	if len(ribbons) == 0 {
		ribbons = tt.Ribbons
	}
	if len(ribbons) == 0 || len(tt.CarStopSigns) == 0 {
		return writeEmptyCarStopSigns(w, tt.Route)
	}
	anchor := geo.NewRouteAnchor(tt.OriginLat, tt.OriginLng)

	// ribbon_guid → platform_name lookup. Mirrors the import-time DB JOIN
	// (car_stop_signs.platform_name ← track_markers.name on shared
	// ribbon_guid where marker_type='Platform'). Surfacing platform_name on
	// each car_stop_sign feature lets downstream consumers (the timetable
	// webpage) match signs against a service's schedule by name — same way
	// platform_track + platform_marker layers are filtered.
	platformByRibbon := map[string]string{}
	for _, m := range tt.RouteMarkers {
		if m == nil || m.MarkerType != "Platform" || m.Name == "" || m.RibbonGUID == "" {
			continue
		}
		key := uasset.NormalizeGUID(m.RibbonGUID)
		if key == "" {
			key = m.RibbonGUID
		}
		// First-seen wins — chained Platform markers on the same ribbon
		// share a name (verified against IoW), so collisions are no-ops.
		if _, exists := platformByRibbon[key]; !exists {
			platformByRibbon[key] = m.Name
		}
	}

	type res struct {
		css *uasset.CarStopSign
		lat float64
		lng float64
	}
	resolved := make([]res, 0, len(tt.CarStopSigns))
	for _, c := range tt.CarStopSigns {
		key := uasset.NormalizeGUID(c.RibbonGUID)
		if key == "" {
			key = c.RibbonGUID
		}
		rib, ok := ribbons[key]
		if !ok || rib == nil || rib.Length <= 0 {
			continue
		}
		atLen := float64(c.Location) * rib.Length
		dx, dy := geo.ArcDelta(0, 0, rib.TangentX, rib.TangentY, rib.Radius, atLen)
		originX, originY, _ := ribbonWorldOrigin(rib)
		lat, lng := anchor.WorldToLatLng((originX+dx)/100.0, (originY+dy)/100.0)
		resolved = append(resolved, res{css: c, lat: lat, lng: lng})
	}
	// Sort for deterministic output: by ribbon GUID, then scalar.
	sort.Slice(resolved, func(i, j int) bool {
		if resolved[i].css.RibbonGUID != resolved[j].css.RibbonGUID {
			return resolved[i].css.RibbonGUID < resolved[j].css.RibbonGUID
		}
		return resolved[i].css.Location < resolved[j].css.Location
	})

	feats := make([]any, 0, len(resolved))
	for _, r := range resolved {
		props := map[string]any{
			"feature_kind":      "car_stop_sign",
			"max_rail_vehicles": r.css.MaxNumberOfRailVehicles,
			"ribbon_guid":       r.css.RibbonGUID,
			"location":          r.css.Location,
			"tile":              r.css.TileName,
		}
		// Attach the resolved platform name when we found a matching Platform
		// marker on the same ribbon. Omitted when there's no platform marker
		// for this ribbon (e.g. cab-stop signs at non-platform stop points).
		if key := uasset.NormalizeGUID(r.css.RibbonGUID); key != "" {
			if name, ok := platformByRibbon[key]; ok {
				props["platform_name"] = name
			}
		} else if name, ok := platformByRibbon[r.css.RibbonGUID]; ok {
			props["platform_name"] = name
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
		"name":     "car-stop-signs-" + tt.Route,
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

func writeEmptyCarStopSigns(w io.Writer, route string) (int, error) {
	return writeEmptyFC(w, "car-stop-signs-"+route)
}
