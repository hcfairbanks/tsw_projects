package output

import (
	"encoding/json"
	"io"
	"sort"

	"tsw6-timetable/internal/geo"
	"tsw6-timetable/internal/pak/uasset"
)

// WriteSwitchesGeoJSON resolves every track switch (NetworkTurnoutJunction)
// to a lat/lng using its OutgoingRibbon's CachedStartPosition (the switch
// physically sits at the start of the outgoing ribbon = end of the ingoing
// = start of the diverging branch). Emits one Point feature per switch.
func WriteSwitchesGeoJSON(w io.Writer, tt *uasset.Timetable) (int, error) {
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
	if len(ribbons) == 0 || len(tt.Switches) == 0 {
		return writeEmptyFC(w, "switches-"+tt.Route)
	}
	anchor := geo.NewRouteAnchor(tt.OriginLat, tt.OriginLng)

	type res struct {
		sw  *uasset.Switch
		lat float64
		lng float64
	}
	resolved := make([]res, 0, len(tt.Switches))
	for _, s := range tt.Switches {
		// Try OutgoingRibbon first (its start is the switch). Fall back to
		// IngoingRibbon's end (= switch position). Last resort: TurnoutRibbon
		// start.
		if lat, lng, ok := switchPositionFromOutgoing(s, ribbons, anchor); ok {
			resolved = append(resolved, res{sw: s, lat: lat, lng: lng})
			continue
		}
		if lat, lng, ok := switchPositionFromIngoing(s, ribbons, anchor); ok {
			resolved = append(resolved, res{sw: s, lat: lat, lng: lng})
			continue
		}
		if lat, lng, ok := switchPositionFromTurnout(s, ribbons, anchor); ok {
			resolved = append(resolved, res{sw: s, lat: lat, lng: lng})
			continue
		}
	}

	sort.Slice(resolved, func(i, j int) bool {
		return resolved[i].sw.JctGUID < resolved[j].sw.JctGUID
	})

	feats := make([]any, 0, len(resolved))
	for i, r := range resolved {
		props := map[string]any{
			"jct_guid":            r.sw.JctGUID,
			"node_guid":           r.sw.NodeGUID,
			"ingoing_ribbon":      r.sw.IngoingRibbon,
			"outgoing_ribbon":     r.sw.OutgoingRibbon,
			"turnout_ribbon":      r.sw.TurnoutRibbon,
			"manually_controlled": r.sw.ManuallyControlled,
			"display_label":       fmtSwitchLabel(i + 1, r.sw.ManuallyControlled),
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
		"name":     "switches-" + tt.Route,
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

func switchPositionFromOutgoing(s *uasset.Switch, ribbons map[string]*uasset.Ribbon, anchor *geo.RouteAnchor) (lat, lng float64, ok bool) {
	if s.OutgoingRibbon == "" {
		return 0, 0, false
	}
	key := uasset.NormalizeGUID(s.OutgoingRibbon)
	if key == "" {
		key = s.OutgoingRibbon
	}
	rib, found := ribbons[key]
	if !found || rib == nil {
		return 0, 0, false
	}
	originX, originY, _ := ribbonWorldOrigin(rib)
	lat, lng = anchor.WorldToLatLng(originX/100.0, originY/100.0)
	return lat, lng, true
}

func switchPositionFromTurnout(s *uasset.Switch, ribbons map[string]*uasset.Ribbon, anchor *geo.RouteAnchor) (lat, lng float64, ok bool) {
	if s.TurnoutRibbon == "" {
		return 0, 0, false
	}
	key := uasset.NormalizeGUID(s.TurnoutRibbon)
	if key == "" {
		key = s.TurnoutRibbon
	}
	rib, found := ribbons[key]
	if !found || rib == nil {
		return 0, 0, false
	}
	originX, originY, _ := ribbonWorldOrigin(rib)
	lat, lng = anchor.WorldToLatLng(originX/100.0, originY/100.0)
	return lat, lng, true
}

// switchPositionFromIngoing computes the END of the ingoing ribbon (which is
// where the switch blades live).
func switchPositionFromIngoing(s *uasset.Switch, ribbons map[string]*uasset.Ribbon, anchor *geo.RouteAnchor) (lat, lng float64, ok bool) {
	if s.IngoingRibbon == "" {
		return 0, 0, false
	}
	key := uasset.NormalizeGUID(s.IngoingRibbon)
	if key == "" {
		key = s.IngoingRibbon
	}
	rib, found := ribbons[key]
	if !found || rib == nil || rib.Length <= 0 {
		return 0, 0, false
	}
	dx, dy := geo.ArcDelta(0, 0, rib.TangentX, rib.TangentY, rib.Radius, rib.Length)
	originX, originY, _ := ribbonWorldOrigin(rib)
	lat, lng = anchor.WorldToLatLng((originX+dx)/100.0, (originY+dy)/100.0)
	return lat, lng, true
}

func fmtSwitchLabel(n int, manual bool) string {
	if manual {
		return labelf(n, "Manual Switch")
	}
	return labelf(n, "Switch")
}

func labelf(n int, kind string) string {
	return fmtSignalLabel(n, kind) // reuses the "<n> <kind>" formatter
}

func writeEmptyFC(w io.Writer, name string) (int, error) {
	doc := map[string]any{
		"type":     "FeatureCollection",
		"name":     name,
		"features": []any{},
	}
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	enc.SetEscapeHTML(false)
	if err := enc.Encode(doc); err != nil {
		return 0, err
	}
	return 0, nil
}
