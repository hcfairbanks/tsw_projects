package output

import (
	"encoding/json"
	"fmt"
	"io"
	"sort"

	"tsw6-timetable/internal/geo"
	"tsw6-timetable/internal/pak/uasset"
)

// WriteSignalsGeoJSON resolves every signal in the timetable to a lat/lng
// (using its parent ribbon's geometry + LocationFraction × Length) and emits
// one Point feature per signal. Properties carry SignalID + SignalType so a
// viewer can label / colour-code them.
func WriteSignalsGeoJSON(w io.Writer, tt *uasset.Timetable) (int, error) {
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
	if len(ribbons) == 0 || len(tt.Signals) == 0 {
		// Still emit an empty FeatureCollection so consumers can rely on the
		// file existing.
		return writeEmptySignals(w, tt.Route)
	}
	anchor := geo.NewRouteAnchor(tt.OriginLat, tt.OriginLng)

	type res struct {
		sig *uasset.Signal
		lat float64
		lng float64
	}
	resolved := make([]res, 0, len(tt.Signals))
	for _, s := range tt.Signals {
		key := uasset.NormalizeGUID(s.RibbonGUID)
		if key == "" {
			key = s.RibbonGUID
		}
		rib, ok := ribbons[key]
		if !ok || rib == nil || rib.Length <= 0 {
			continue
		}
		// LocationFraction is 0..1 along the ribbon — multiply by Length to
		// get arc-distance in cm before walking the curve.
		atLen := float64(s.LocationFraction) * rib.Length
		dx, dy := geo.ArcDelta(0, 0, rib.TangentX, rib.TangentY, rib.Radius, atLen)
		originX, originY, _ := ribbonWorldOrigin(rib)
		lat, lng := anchor.WorldToLatLng((originX+dx)/100.0, (originY+dy)/100.0)
		resolved = append(resolved, res{sig: s, lat: lat, lng: lng})
	}
	sort.Slice(resolved, func(i, j int) bool {
		return resolved[i].sig.SignalID < resolved[j].sig.SignalID
	})

	// Per-type sequential numbering for human-readable labels. Untyped
	// signals share a "Signal" bucket. Format: "1 Shunt" / "2 YardExit" /
	// "3 Signal". The raw SignalID stays in properties for reference.
	typeCounters := map[string]int{}
	feats := make([]any, 0, len(resolved))
	for _, r := range resolved {
		bucket := r.sig.SignalType
		if bucket == "" {
			bucket = "Signal"
		}
		typeCounters[bucket]++
		var label string
		if r.sig.SignalType != "" {
			label = fmtSignalLabel(typeCounters[bucket], r.sig.SignalType)
		} else {
			label = fmtSignalLabel(typeCounters[bucket], "Signal")
		}
		props := map[string]any{
			"signal_id":         r.sig.SignalID,
			"signal_type":       r.sig.SignalType,
			"display_label":     label,
			"ribbon_guid":       r.sig.RibbonGUID,
			"location_fraction": r.sig.LocationFraction,
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
		"name":     "signals-" + tt.Route,
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

// fmtSignalLabel formats "<n> <Type>" with no colon — produces "1 Shunt",
// "2 YardExit", "3 Signal", etc.
func fmtSignalLabel(n int, kind string) string {
	return fmt.Sprintf("%d %s", n, kind)
}

func writeEmptySignals(w io.Writer, route string) (int, error) {
	return writeEmptyFC(w, "signals-"+route)
}
