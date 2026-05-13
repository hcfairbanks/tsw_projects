package output

import (
	"math"
	"strings"
)

// FilterOptions controls the proximity thresholds used by
// FilterRouteFeaturesForTimetable. The defaults match the JS filter (3 m
// for rail-bound items, 50 m for collectables) — see DefaultFilterOptions.
type FilterOptions struct {
	// MarkerProxM is the segment distance threshold (metres) for rail-bound
	// items: signals, switches, car_stop_signs, track_markers, and the
	// proximity fallback on track-feature LineStrings + legacy platform
	// Points.
	MarkerProxM float64
	// SignalProxM is the segment distance threshold (metres) for signals
	// specifically. Currently equals MarkerProxM but kept separate so the
	// two can diverge (signals at parallel-track stations are within ~5 m
	// so the threshold is intentionally tight).
	SignalProxM float64
	// CollectableProxM is the segment distance threshold (metres) for
	// collectables. Wider than the rail-bound items because pickups sit on
	// platforms / in stations, naturally further from the rail.
	CollectableProxM float64
}

// DefaultFilterOptions returns the threshold set used by the JS filter and
// by the importer's per-timetable pre-build. Override via FilterOptions for
// tests or unusual data.
func DefaultFilterOptions() FilterOptions {
	return FilterOptions{
		MarkerProxM:      3,
		SignalProxM:      3,
		CollectableProxM: 50,
	}
}

// ScheduleEntryRef is the minimum subset of a timetable_entries row needed
// to compute the schedule-match indices. Pulled out as a struct so the
// importer doesn't have to import the entire ScheduleItem type and so
// tests can hand-construct one.
type ScheduleEntryRef struct {
	Location        string
	Structure       string
	StructureNumber string
}

// FilterRouteFeaturesForTimetable returns the subset of `routeFeatures`
// (parsed GeoJSON Feature objects from route_coordinates.coordinates) that
// should render on a timetable map for the given service.
//
// Mirrors the request-time JS filter in views/js/service-map.js and the
// duplicates in views/timetables/show.html / edit.html. Run once at import
// time so the runtime fetch is a single pre-built blob.
//
// Match rules by feature shape:
//
//   - Track LineStrings (feature_type: platform_track / siding_track /
//     line_track / running_track) — keep if the feature's
//     (location, structure, structure_number) tuple appears in the
//     schedule, OR if any vertex is within MarkerProxM of the path.
//   - Untyped LineStrings (the merged-rails layer) — drop. Those are only
//     shown in route-only mode.
//   - Point feature_kind=car_stop_sign — strict usedNames(platform_name)
//     match. No proximity fallback (signs at adjacent platforms are within
//     ~5 m so proximity catches the wrong one).
//   - Point feature_kind=track_marker — only marker_type=Platform survives
//     the kind filter; then strict usedNames(name) match.
//   - Point feature_kind=collectable — proximity only (CollectableProxM).
//   - Legacy platform Points (no feature_kind, no signal_id, no jct_guid) —
//     strict schedule-tuple match. No proximity.
//   - Points with signal_id — proximity (SignalProxM, segment distance).
//   - Points with jct_guid — proximity (MarkerProxM, segment distance).
//
// Path bbox pre-cull: features whose bbox doesn't intersect the path's
// inflated bbox skip the per-vertex proximity scan. Typically drops 80-95%
// of features at zero distance-math cost.
func FilterRouteFeaturesForTimetable(
	routeFeatures []map[string]any,
	entries []ScheduleEntryRef,
	pathCoords []ServiceCoord,
	opts FilterOptions,
) []map[string]any {
	usedStructuresFull, usedStructuresLoose, usedNames := buildScheduleIndexes(entries)

	hasPath := len(pathCoords) > 0
	pathBBox := pathBoundingBox(pathCoords)
	// The widest threshold in play — anything with a bbox outside this
	// inflated box can't possibly satisfy any proximity check, so the
	// pre-cull is safe at this radius.
	cullM := math.Max(opts.MarkerProxM, math.Max(opts.SignalProxM, opts.CollectableProxM))

	out := make([]map[string]any, 0, len(routeFeatures))
	for _, feat := range routeFeatures {
		if keep := keepFeature(
			feat, usedStructuresFull, usedStructuresLoose, usedNames,
			pathCoords, pathBBox, cullM, hasPath, opts,
		); keep {
			out = append(out, feat)
		}
	}
	return out
}

// keepFeature applies the per-feature filter rules. Returns true when the
// feature should be included in the output blob.
func keepFeature(
	feat map[string]any,
	usedStructuresFull, usedStructuresLoose, usedNames map[string]struct{},
	pathCoords []ServiceCoord,
	pathBBox bbox,
	cullM float64,
	hasPath bool,
	opts FilterOptions,
) bool {
	geom, _ := feat["geometry"].(map[string]any)
	if geom == nil {
		return false
	}
	gtype, _ := geom["type"].(string)
	props, _ := feat["properties"].(map[string]any)
	if props == nil {
		props = map[string]any{}
	}

	// Track-feature LineStrings.
	if gtype == "LineString" || gtype == "MultiLineString" {
		featType := strProp(props["feature_type"])
		if featType == "" {
			// Untyped LineString = the merged-rails layer. Only relevant
			// in route-only mode; never on a timetable map.
			return false
		}
		// Schedule match first (cheap).
		if structureMatchesSchedule(props, usedStructuresFull, usedStructuresLoose) {
			return true
		}
		// Proximity fallback. Bbox pre-cull skips features that can't
		// possibly be near the path.
		if !hasPath {
			return false
		}
		featBB, ok := lineBBox(geom)
		if !ok || !featBB.intersectsInflated(pathBBox, cullM) {
			return false
		}
		return lineNearPath(geom, pathCoords, opts.MarkerProxM)
	}

	if gtype != "Point" {
		return false
	}
	coords, _ := geom["coordinates"].([]any)
	if len(coords) < 2 {
		return false
	}
	flng, _ := coords[0].(float64)
	flat, _ := coords[1].(float64)

	switch strProp(props["feature_kind"]) {
	case "car_stop_sign":
		// Strict usedNames(platform_name) match. No proximity fallback.
		name := strProp(props["platform_name"])
		_, ok := usedNames[name]
		return name != "" && ok
	case "track_marker":
		// Only Platform pins, and only when their composed name appears in
		// the schedule.
		if strProp(props["marker_type"]) != "Platform" {
			return false
		}
		name := strProp(props["name"])
		_, ok := usedNames[name]
		return name != "" && ok
	case "collectable":
		// Proximity only — collectables don't carry schedule identity.
		if !hasPath {
			return false
		}
		featBB := pointBBox(flat, flng)
		if !featBB.intersectsInflated(pathBBox, opts.CollectableProxM) {
			return false
		}
		return minDistanceToPathSegmentsM(flat, flng, pathCoords) <= opts.CollectableProxM
	}

	// Legacy / typeless point branch — same dispatch the JS does.
	switch {
	case props["signal_id"] != nil && props["signal_id"] != "":
		// Signals: tight segment-distance proximity.
		if !hasPath {
			return false
		}
		featBB := pointBBox(flat, flng)
		if !featBB.intersectsInflated(pathBBox, opts.SignalProxM) {
			return false
		}
		return minDistanceToPathSegmentsM(flat, flng, pathCoords) <= opts.SignalProxM
	case props["jct_guid"] != nil:
		// Switches: nearest-vertex proximity.
		if !hasPath {
			return false
		}
		featBB := pointBBox(flat, flng)
		if !featBB.intersectsInflated(pathBBox, opts.MarkerProxM) {
			return false
		}
		return minDistanceToPathM(flat, flng, pathCoords) <= opts.MarkerProxM
	default:
		// Legacy platform Point. Strict schedule match, no proximity.
		return structureMatchesSchedule(props, usedStructuresFull, usedStructuresLoose)
	}
}

// buildScheduleIndexes builds the three lookup sets used by feature
// matching, with the same composition rule the JS uses.
//
// `usedStructuresFull` keys on the full `loc|structure|number` tuple.
// `usedStructuresLoose` drops the structure word so a schedule entry that
// omits "Platform" still matches a feature that includes it (and vice
// versa). `usedNames` is the space-joined composed name used by features
// that ship `name`/`platform_name`/`location` as a single string instead
// of split fields.
func buildScheduleIndexes(entries []ScheduleEntryRef) (
	usedStructuresFull, usedStructuresLoose, usedNames map[string]struct{},
) {
	usedStructuresFull = make(map[string]struct{})
	usedStructuresLoose = make(map[string]struct{})
	usedNames = make(map[string]struct{})
	for _, e := range entries {
		loc := strings.TrimSpace(e.Location)
		num := strings.TrimSpace(e.StructureNumber)
		if loc == "" || num == "" {
			continue
		}
		st := strings.TrimSpace(e.Structure)
		usedStructuresFull[loc+"|"+st+"|"+num] = struct{}{}
		usedStructuresLoose[loc+"|"+num] = struct{}{}
		parts := []string{loc}
		if st != "" {
			parts = append(parts, st)
		}
		parts = append(parts, num)
		usedNames[strings.Join(parts, " ")] = struct{}{}
	}
	return
}

// structureMatchesSchedule reports whether a feature's
// (location, structure, structure_number) properties appear in the
// schedule's tuple sets. Mirror of the JS function of the same name.
func structureMatchesSchedule(
	props map[string]any,
	usedStructuresFull, usedStructuresLoose map[string]struct{},
) bool {
	loc := strings.TrimSpace(strProp(props["location"]))
	num := strings.TrimSpace(strProp(props["structure_number"]))
	if loc == "" || num == "" {
		return false
	}
	st := strings.TrimSpace(strProp(props["structure"]))
	if _, ok := usedStructuresFull[loc+"|"+st+"|"+num]; ok {
		return true
	}
	_, ok := usedStructuresLoose[loc+"|"+num]
	return ok
}

// strProp normalises a JSON property value to a string, matching the JS
// `strProp` helper. Numbers and bools fall through to "" (no use case
// matches them today; matches JS behaviour of skipping non-string keys).
func strProp(v any) string {
	if s, ok := v.(string); ok {
		return s
	}
	return ""
}

// bbox is a min/max lat-lng rectangle used for the pre-cull cheap
// rejection. Inflation is in metres, converted to a degree delta via a
// rough-equator approximation — good enough at the scales we care about
// (the route bboxes span 0.1-1° each).
type bbox struct {
	minLat, maxLat, minLng, maxLng float64
	valid                          bool
}

func (b *bbox) extend(lat, lng float64) {
	if !b.valid {
		b.minLat, b.maxLat = lat, lat
		b.minLng, b.maxLng = lng, lng
		b.valid = true
		return
	}
	if lat < b.minLat {
		b.minLat = lat
	}
	if lat > b.maxLat {
		b.maxLat = lat
	}
	if lng < b.minLng {
		b.minLng = lng
	}
	if lng > b.maxLng {
		b.maxLng = lng
	}
}

// intersectsInflated reports whether `b` (inflated by `inflateM` metres on
// every side) overlaps `other`. Conservatively over-inflates (~111 km/°
// for lat, same for lng) so a feature near the cull threshold isn't
// dropped by a longitude-cosine miscount. The cost of a false positive is
// one wasted distance check; a false negative would lose a real feature.
func (b bbox) intersectsInflated(other bbox, inflateM float64) bool {
	if !b.valid || !other.valid {
		return false
	}
	deg := inflateM / 111000.0
	return b.maxLat+deg >= other.minLat-deg &&
		b.minLat-deg <= other.maxLat+deg &&
		b.maxLng+deg >= other.minLng-deg &&
		b.minLng-deg <= other.maxLng+deg
}

func pathBoundingBox(coords []ServiceCoord) bbox {
	var b bbox
	for _, c := range coords {
		b.extend(c.Latitude, c.Longitude)
	}
	return b
}

func pointBBox(lat, lng float64) bbox {
	return bbox{minLat: lat, maxLat: lat, minLng: lng, maxLng: lng, valid: true}
}

// lineBBox walks a LineString or MultiLineString geometry and returns its
// bbox. Returns ok=false when the geometry has no usable coords (which
// also means no proximity check can pass).
func lineBBox(geom map[string]any) (bbox, bool) {
	var b bbox
	gtype, _ := geom["type"].(string)
	if gtype == "MultiLineString" {
		lines, _ := geom["coordinates"].([]any)
		for _, ln := range lines {
			pts, _ := ln.([]any)
			extendBboxFromPts(&b, pts)
		}
	} else { // LineString
		pts, _ := geom["coordinates"].([]any)
		extendBboxFromPts(&b, pts)
	}
	return b, b.valid
}

func extendBboxFromPts(b *bbox, pts []any) {
	for _, p := range pts {
		c, _ := p.([]any)
		if len(c) < 2 {
			continue
		}
		lng, _ := c[0].(float64)
		lat, _ := c[1].(float64)
		b.extend(lat, lng)
	}
}

// lineNearPath scans every vertex of the LineString/MultiLineString and
// returns true on the first one within `proxM` segment-distance of the
// path. Same iteration order as the JS so behaviour matches exactly.
func lineNearPath(geom map[string]any, pathCoords []ServiceCoord, proxM float64) bool {
	gtype, _ := geom["type"].(string)
	if gtype == "MultiLineString" {
		lines, _ := geom["coordinates"].([]any)
		for _, ln := range lines {
			pts, _ := ln.([]any)
			if scanPtsNearPath(pts, pathCoords, proxM) {
				return true
			}
		}
		return false
	}
	pts, _ := geom["coordinates"].([]any)
	return scanPtsNearPath(pts, pathCoords, proxM)
}

func scanPtsNearPath(pts []any, pathCoords []ServiceCoord, proxM float64) bool {
	for _, p := range pts {
		c, _ := p.([]any)
		if len(c) < 2 {
			continue
		}
		lng, _ := c[0].(float64)
		lat, _ := c[1].(float64)
		if minDistanceToPathM(lat, lng, pathCoords) <= proxM {
			return true
		}
	}
	return false
}

// minDistanceToPathM returns the shortest distance (metres) from a point
// to any vertex of the path. Mirrors the JS `minDistanceToPath`.
func minDistanceToPathM(lat, lng float64, pathCoords []ServiceCoord) float64 {
	best := math.Inf(1)
	for _, c := range pathCoords {
		d := equirectMeters(lat, lng, c.Latitude, c.Longitude)
		if d < best {
			best = d
			if best < 1 {
				return best
			}
		}
	}
	return best
}

// minDistanceToPathSegmentsM returns the shortest distance (metres) from a
// point to any segment of the path. Mirrors the JS
// `minDistanceToPathSegments` — used for signals where the segment-level
// metric matters (a point can sit between two vertices).
func minDistanceToPathSegmentsM(lat, lng float64, pathCoords []ServiceCoord) float64 {
	if len(pathCoords) == 0 {
		return math.Inf(1)
	}
	if len(pathCoords) == 1 {
		return equirectMeters(lat, lng, pathCoords[0].Latitude, pathCoords[0].Longitude)
	}
	best := math.Inf(1)
	for i := 0; i+1 < len(pathCoords); i++ {
		a, b := pathCoords[i], pathCoords[i+1]
		// Project the point onto segment AB in lat/lng-space (the same
		// approximation the JS uses). Good enough at our scales.
		ax, ay := a.Longitude, a.Latitude
		bx, by := b.Longitude, b.Latitude
		dx, dy := bx-ax, by-ay
		len2 := dx*dx + dy*dy
		fx, fy := ax, ay
		if len2 > 0 {
			t := ((lng-ax)*dx + (lat-ay)*dy) / len2
			if t < 0 {
				t = 0
			} else if t > 1 {
				t = 1
			}
			fx = ax + t*dx
			fy = ay + t*dy
		}
		d := equirectMeters(lat, lng, fy, fx)
		if d < best {
			best = d
			if best < 0.3 {
				return best
			}
		}
	}
	return best
}

// equirectMeters is the equirectangular distance approximation used
// throughout the codebase's distance helpers (JS `distMeters`, the
// trim-coords fallback, etc.). At rail scales the error vs haversine is
// well under 0.1%, plenty for proximity filtering.
func equirectMeters(lat1, lng1, lat2, lng2 float64) float64 {
	const earthRadiusM = 6371000.0
	dLat := (lat2 - lat1) * math.Pi / 180
	dLng := (lng2 - lng1) * math.Pi / 180
	mid := (lat1 + lat2) * 0.5 * math.Pi / 180
	x := dLng * math.Cos(mid)
	return math.Hypot(dLat, x) * earthRadiusM
}
