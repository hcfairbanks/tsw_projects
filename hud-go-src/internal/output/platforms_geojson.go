package output

import (
	"encoding/json"
	"io"
	"sort"
	"strings"

	"hud-go/internal/geo"
	"hud-go/internal/pak/uasset"
)

// platformKey identifies a unique platform across all services in a route.
// We dedupe on (location, structure, structure_number) — services calling at
// the same platform always use the same triple.
type platformKey struct {
	Location, Structure, StructureNumber string
}

// platformRecord stores the resolved coords + linkage for one platform.
type platformRecord struct {
	Key            platformKey
	RibbonGUID     string
	RibbonLocation float32
	Lat, Lng       float64
	Source         string // "schedule" or "tile" — where the entry was found
}

// WritePlatformsGeoJSON walks every service in the timetables, collects
// unique platforms (by name + structure), resolves each to lat/lng using the
// schedule item's ribbon reference, and emits one Point feature per platform.
//
// `byRoute` is the same grouping used by the package writer — one route's
// pairs at a time. Returns count of features written.
func WritePlatformsGeoJSON(w io.Writer, tt *uasset.Timetable) (int, error) {
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
	if len(ribbons) == 0 {
		return 0, nil
	}
	anchor := geo.NewRouteAnchor(tt.OriginLat, tt.OriginLng)

	// Collect unique platforms by (location, structure, structure_number).
	// First-write-wins for the ribbon reference (services usually agree, and
	// disagreements are sub-metre).
	seen := map[platformKey]*platformRecord{}
	for i := range tt.Services {
		svc := &tt.Services[i]
		for _, it := range svc.Schedule {
			loc := strings.TrimSpace(it.Location)
			if loc == "" || loc == "None" {
				continue
			}
			// Skip non-platform actions — only STOP and GO VIA reference real
			// trackside locations. WAIT / LOAD PASSENGERS / UNLOAD PASSENGERS
			// share the same location as their preceding STOP, so dedupe
			// catches them anyway, but skip explicitly to avoid noise.
			switch it.Action {
			case "STOP AT LOCATION", "GO VIA LOCATION":
			default:
				continue
			}
			if it.RibbonGUID == "" {
				continue
			}
			k := platformKey{Location: loc, Structure: it.Structure, StructureNumber: it.StructureNumber}
			if _, ok := seen[k]; ok {
				continue
			}
			lat, lng, ok := resolveRibbonOffset(ribbons, anchor, it.RibbonGUID, float64(it.RibbonLocation))
			if !ok {
				continue
			}
			seen[k] = &platformRecord{
				Key:            k,
				RibbonGUID:     it.RibbonGUID,
				RibbonLocation: it.RibbonLocation,
				Lat:            lat,
				Lng:            lng,
				Source:         "schedule",
			}
		}
	}

	// Supplement with LinkedPlatforms found in tile data — pick up physical
	// platforms that no service stops at (e.g. unused track-side platforms).
	// Schedule entries always win on dedupe.
	for _, lp := range tt.LinkedPlatforms {
		k := splitPlatformName(lp.Name)
		if k.Location == "" {
			continue
		}
		if _, ok := seen[k]; ok {
			continue
		}
		lat, lng, ok := resolveRibbonOffset(ribbons, anchor, lp.RibbonGUID, float64(lp.RibbonLocation))
		if !ok {
			continue
		}
		seen[k] = &platformRecord{
			Key:            k,
			RibbonGUID:     lp.RibbonGUID,
			RibbonLocation: lp.RibbonLocation,
			Lat:            lat,
			Lng:            lng,
			Source:         "tile",
		}
	}

	// Stable feature order: sort by location then structure then number.
	keys := make([]platformKey, 0, len(seen))
	for k := range seen {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool {
		if keys[i].Location != keys[j].Location {
			return keys[i].Location < keys[j].Location
		}
		if keys[i].Structure != keys[j].Structure {
			return keys[i].Structure < keys[j].Structure
		}
		return keys[i].StructureNumber < keys[j].StructureNumber
	})

	feats := make([]any, 0, len(keys))
	for _, k := range keys {
		p := seen[k]
		props := map[string]any{
			"name": p.Key.Location,
		}
		if p.Key.Structure != "" {
			props["structure"] = p.Key.Structure
		}
		if p.Key.StructureNumber != "" {
			props["structure_number"] = p.Key.StructureNumber
		}
		// Keep ribbon linkage so a viewer can highlight the underlying ribbon.
		props["ribbon_guid"] = p.RibbonGUID
		props["ribbon_location"] = p.RibbonLocation
		props["source"] = p.Source // "schedule" (a service stops here) or "tile" (physical only)
		feats = append(feats, map[string]any{
			"type":       "Feature",
			"properties": props,
			"geometry": map[string]any{
				"type":        "Point",
				"coordinates": []float64{round7(p.Lng), round7(p.Lat)},
			},
		})
	}

	doc := map[string]any{
		"type":     "FeatureCollection",
		"name":     "platforms-" + tt.Route,
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

// splitPlatformName breaks "Ryde Pier Head Platform 2" into
// (location, structure, number) using the same separator set the schedule
// parser uses (parse.go's splitLocation).
func splitPlatformName(loc string) platformKey {
	for _, sep := range []string{" Platform ", " Siding ", " Track ", " Line "} {
		if idx := strings.Index(loc, sep); idx >= 0 {
			return platformKey{
				Location:        loc[:idx],
				Structure:       strings.TrimSpace(sep),
				StructureNumber: loc[idx+len(sep):],
			}
		}
	}
	return platformKey{Location: loc}
}

func resolveRibbonOffset(ribbons map[string]*uasset.Ribbon, anchor *geo.RouteAnchor, guid string, ribbonLocCm float64) (lat, lng float64, ok bool) {
	key := uasset.NormalizeGUID(guid)
	if key == "" {
		key = guid
	}
	rib, found := ribbons[key]
	if !found || rib == nil || rib.Length <= 0 {
		return 0, 0, false
	}
	dx, dy := geo.ArcDelta(0, 0, rib.TangentX, rib.TangentY, rib.Radius, ribbonLocCm)
	originX, originY, _ := ribbonWorldOrigin(rib)
	lat, lng = anchor.WorldToLatLng((originX+dx)/100.0, (originY+dy)/100.0)
	return lat, lng, true
}
