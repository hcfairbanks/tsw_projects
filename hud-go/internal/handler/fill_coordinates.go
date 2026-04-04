package handler

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"math"
	"strconv"
	"strings"
)

// ── Geo utilities ───────────────────────────────────────────────────────────

// haversineMeters returns the distance in meters between two lat/lng points.
func haversineMeters(lat1, lng1, lat2, lng2 float64) float64 {
	const R = 6371000.0
	toRad := func(d float64) float64 { return d * math.Pi / 180 }
	dLat := toRad(lat2 - lat1)
	dLng := toRad(lng2 - lng1)
	a := math.Sin(dLat/2)*math.Sin(dLat/2) +
		math.Cos(toRad(lat1))*math.Cos(toRad(lat2))*math.Sin(dLng/2)*math.Sin(dLng/2)
	return R * 2 * math.Atan2(math.Sqrt(a), math.Sqrt(1-a))
}

// ── Types (only types not already in timetable.go) ──────────────────────────

type coordPoint struct {
	Latitude  float64 `json:"latitude"`
	Longitude float64 `json:"longitude"`
}

type coordData struct {
	Latitude  string
	Longitude string
	APIName   string
	Priority  int
}

type pathSource struct {
	ID    int
	Bound string
	Coords []coordPoint
	Stops  []entryRow
}

type coordLookup struct {
	exact   map[string]coordData // direction|location|platform
	locOnly map[string]coordData // direction|location
	anyDir  map[string]coordData // location
}

// ── Helpers ─────────────────────────────────────────────────────────────────

func findClosestPointIndex(coords []coordPoint, lat, lng float64) (int, float64) {
	minDist := math.Inf(1)
	minIdx := -1
	for i, c := range coords {
		d := haversineMeters(lat, lng, c.Latitude, c.Longitude)
		if d < minDist {
			minDist = d
			minIdx = i
		}
	}
	return minIdx, minDist
}

func coordPriorityVal(source string) int {
	switch source {
	case "manual":
		return 3
	case "automatic", "imported":
		return 2
	default:
		return 1
	}
}

func fcHasCoords(e entryRow) bool {
	return e.Latitude != nil && *e.Latitude != "" &&
		e.Longitude != nil && *e.Longitude != ""
}

func fcParseLat(e entryRow) float64 {
	if e.Latitude == nil {
		return 0
	}
	v, _ := strconv.ParseFloat(*e.Latitude, 64)
	return v
}

func fcParseLng(e entryRow) float64 {
	if e.Longitude == nil {
		return 0
	}
	v, _ := strconv.ParseFloat(*e.Longitude, 64)
	return v
}

// ── Database helpers ────────────────────────────────────────────────────────

func fcGetTimetables(db *sql.DB, routeID int) ([]timetableRow, error) {
	rows, err := db.Query("SELECT "+timetableCols+" FROM timetables WHERE route_id = ? ORDER BY id", routeID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []timetableRow
	for rows.Next() {
		t, err := scanTimetable(rows)
		if err != nil {
			return nil, err
		}
		result = append(result, t)
	}
	return result, rows.Err()
}

func fcLoadEntries(db *sql.DB, timetableID int) ([]entryRow, error) {
	rows, err := db.Query(`
		SELECT te.id, te.timetable_id, te.action_id, te.details, te.location_id,
		       te.platform, te.time1, te.time2, te.latitude, te.longitude,
		       te.api_name, te.sort_order, te.coord_source,
		       ta.name as action, l.name as location
		FROM timetable_entries te
		LEFT JOIN timetable_actions ta ON ta.id = te.action_id
		LEFT JOIN locations l ON l.id = te.location_id
		WHERE te.timetable_id = ?
		ORDER BY te.sort_order`, timetableID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []entryRow
	for rows.Next() {
		var e entryRow
		if err := rows.Scan(&e.ID, &e.TimetableID, &e.ActionID, &e.Details, &e.LocationID,
			&e.Platform, &e.Time1, &e.Time2, &e.Latitude, &e.Longitude,
			&e.ApiName, &e.SortOrder, &e.CoordSource,
			&e.Action, &e.Location); err != nil {
			return nil, err
		}
		result = append(result, e)
	}
	return result, rows.Err()
}

func fcGetLocationEntries(db *sql.DB, timetableID int) ([]entryRow, error) {
	all, err := fcLoadEntries(db, timetableID)
	if err != nil {
		return nil, err
	}
	var result []entryRow
	for _, e := range all {
		loc := strings.TrimSpace(ptrStr(e.Location))
		if loc == "" {
			continue
		}
		if ptrStr(e.Action) == "UNLOAD PASSENGERS" {
			continue
		}
		result = append(result, e)
	}
	return result, nil
}

func fcLoadTimetableCoordinates(db *sql.DB, timetableID int) ([]coordPoint, string, error) {
	var coordStr, sourceStr sql.NullString
	err := db.QueryRow("SELECT coordinates, coord_source FROM timetable_coordinates WHERE timetable_id = ?", timetableID).Scan(&coordStr, &sourceStr)
	if err == sql.ErrNoRows {
		return nil, "", nil
	}
	if err != nil {
		return nil, "", err
	}
	if !coordStr.Valid || coordStr.String == "" {
		return nil, sourceStr.String, nil
	}
	var coords []coordPoint
	if err := json.Unmarshal([]byte(coordStr.String), &coords); err != nil {
		return nil, sourceStr.String, nil
	}
	return coords, sourceStr.String, nil
}

// ── Phase 1: Direction templates ────────────────────────────────────────────

func fcBuildDirectionTemplates(db *sql.DB, allTimetables []timetableRow) (map[string][]string, error) {
	templates := map[string][]string{}
	for _, tt := range allTimetables {
		bound := ptrStr(tt.Bound)
		if bound == "" {
			continue
		}
		entries, err := fcGetLocationEntries(db, tt.ID)
		if err != nil {
			return nil, err
		}
		var locs []string
		for _, e := range entries {
			loc := strings.ToLower(strings.TrimSpace(ptrStr(e.Location)))
			if loc == "" || loc == "start" {
				continue
			}
			locs = append(locs, loc)
		}
		existing, ok := templates[bound]
		if !ok || len(locs) > len(existing) {
			templates[bound] = locs
		}
	}
	return templates, nil
}

func fcInferDirection(entries []entryRow, directionTemplates map[string][]string) string {
	var targetLocs []string
	for _, e := range entries {
		loc := strings.ToLower(strings.TrimSpace(ptrStr(e.Location)))
		if loc == "" || loc == "start" {
			continue
		}
		targetLocs = append(targetLocs, loc)
	}
	if len(targetLocs) == 0 {
		return ""
	}

	matchScore := func(refLocs []string) int {
		score := 0
		refIdx := 0
		for _, loc := range targetLocs {
			for j := refIdx; j < len(refLocs); j++ {
				if refLocs[j] == loc {
					score++
					refIdx = j + 1
					break
				}
			}
		}
		return score
	}

	bestDir := ""
	bestScore := 2

	dirs := make([]string, 0, len(directionTemplates))
	for d := range directionTemplates {
		dirs = append(dirs, d)
	}

	for _, dir := range dirs {
		refLocs := directionTemplates[dir]
		fwd := matchScore(refLocs)
		rev := make([]string, len(refLocs))
		copy(rev, refLocs)
		for i, j := 0, len(rev)-1; i < j; i, j = i+1, j-1 {
			rev[i], rev[j] = rev[j], rev[i]
		}
		revScore := matchScore(rev)

		if fwd > bestScore {
			bestScore = fwd
			bestDir = dir
		}
		if revScore > bestScore {
			bestScore = revScore
			for _, d := range dirs {
				if d != dir {
					bestDir = d
					break
				}
			}
			if bestDir == "" {
				bestDir = dir
			}
		}
	}
	return bestDir
}

// ── Phase 2: Build coordinate lookup ────────────────────────────────────────

func fcBuildLookup(db *sql.DB, allTimetables []timetableRow, dirTemplates map[string][]string, report *strings.Builder) (*coordLookup, error) {
	lookup := &coordLookup{
		exact:   map[string]coordData{},
		locOnly: map[string]coordData{},
		anyDir:  map[string]coordData{},
	}

	for _, tt := range allTimetables {
		entries, err := fcGetLocationEntries(db, tt.ID)
		if err != nil {
			return nil, err
		}
		direction := ptrStr(tt.Bound)
		if direction == "" {
			direction = fcInferDirection(entries, dirTemplates)
		}
		if direction == "" {
			direction = "unknown"
		}

		for _, entry := range entries {
			if !fcHasCoords(entry) {
				continue
			}
			locName := strings.ToLower(strings.TrimSpace(ptrStr(entry.Location)))
			if locName == "" || locName == "start" {
				continue
			}
			platform := strings.TrimSpace(ptrStr(entry.Platform))
			priority := coordPriorityVal(ptrStr(entry.CoordSource))
			cd := coordData{
				Latitude:  *entry.Latitude,
				Longitude: *entry.Longitude,
				APIName:   ptrStr(entry.ApiName),
				Priority:  priority,
			}

			if platform != "" {
				exactKey := fmt.Sprintf("%s|%s|%s", direction, locName, platform)
				if existing, ok := lookup.exact[exactKey]; !ok || priority > existing.Priority {
					lookup.exact[exactKey] = cd
				}
			}
			locKey := fmt.Sprintf("%s|%s", direction, locName)
			if existing, ok := lookup.locOnly[locKey]; !ok || priority > existing.Priority {
				lookup.locOnly[locKey] = cd
			}
			if existing, ok := lookup.anyDir[locName]; !ok || priority > existing.Priority {
				lookup.anyDir[locName] = cd
			}
		}
	}

	fmt.Fprintf(report, "Lookup built from %d timetables\n", len(allTimetables))
	fmt.Fprintf(report, "  Exact keys (dir+loc+platform): %d\n", len(lookup.exact))
	fmt.Fprintf(report, "  Location keys (dir+loc): %d\n", len(lookup.locOnly))
	fmt.Fprintf(report, "  Cross-direction keys (loc only): %d\n\n", len(lookup.anyDir))
	return lookup, nil
}

// ── Phase 3: Resolve "start" locations ──────────────────────────────────────

type knownLoc struct {
	Name string
	Lat  float64
	Lng  float64
}

func fcBuildKnownLocations(lookup *coordLookup) []knownLoc {
	var result []knownLoc
	for name, cd := range lookup.anyDir {
		lat, _ := strconv.ParseFloat(cd.Latitude, 64)
		lng, _ := strconv.ParseFloat(cd.Longitude, 64)
		result = append(result, knownLoc{Name: name, Lat: lat, Lng: lng})
	}
	return result
}

func fcGetLocationIDByName(db *sql.DB, routeID int) (map[string]int64, error) {
	m := map[string]int64{}
	rows, err := db.Query("SELECT id, LOWER(name) as name FROM locations WHERE route_id = ?", routeID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var id int64
		var name string
		if err := rows.Scan(&id, &name); err != nil {
			return nil, err
		}
		m[name] = id
	}
	return m, rows.Err()
}

func fcResolveStartLocations(db *sql.DB, routeID int, lookup *coordLookup, report *strings.Builder) (int, error) {
	const threshold = 300.0
	resolved := 0

	knownLocs := fcBuildKnownLocations(lookup)
	locIDByName, err := fcGetLocationIDByName(db, routeID)
	if err != nil {
		return 0, err
	}

	rows, err := db.Query(`
		SELECT te.id, te.timetable_id, te.latitude, te.longitude
		FROM timetable_entries te
		INNER JOIN timetables t ON t.id = te.timetable_id
		LEFT JOIN locations l ON l.id = te.location_id
		WHERE t.route_id = ?
		  AND LOWER(l.name) = 'start'
		  AND te.latitude IS NOT NULL AND te.latitude != ''
		  AND te.longitude IS NOT NULL AND te.longitude != ''
	`, routeID)
	if err != nil {
		return 0, err
	}
	defer rows.Close()

	type startEntry struct {
		ID, TtID int
		Lat, Lng float64
	}
	var entries []startEntry
	for rows.Next() {
		var se startEntry
		var latStr, lngStr string
		if err := rows.Scan(&se.ID, &se.TtID, &latStr, &lngStr); err != nil {
			return 0, err
		}
		se.Lat, _ = strconv.ParseFloat(latStr, 64)
		se.Lng, _ = strconv.ParseFloat(lngStr, 64)
		entries = append(entries, se)
	}
	if err := rows.Err(); err != nil {
		return 0, err
	}

	for _, se := range entries {
		var best *knownLoc
		bestDist := math.Inf(1)
		for i := range knownLocs {
			d := haversineMeters(se.Lat, se.Lng, knownLocs[i].Lat, knownLocs[i].Lng)
			if d < bestDist {
				bestDist = d
				best = &knownLocs[i]
			}
		}
		if best != nil && bestDist <= threshold {
			if locID, ok := locIDByName[best.Name]; ok {
				db.Exec("UPDATE timetable_entries SET location_id = ? WHERE id = ?", locID, se.ID)
				resolved++
				fmt.Fprintf(report, "  RESOLVED START: entry %d -> \"%s\" (%.1fm away)\n", se.ID, best.Name, bestDist)
			}
		} else {
			info := "no known locations"
			if best != nil {
				info = fmt.Sprintf("nearest: \"%s\" at %.1fm", best.Name, bestDist)
			}
			fmt.Fprintf(report, "  UNRESOLVED START: entry %d timetable %d - %s\n", se.ID, se.TtID, info)
		}
	}
	return resolved, nil
}

// ── Phase 3b: Resolve empty first entries ───────────────────────────────────

func fcResolveEmptyFirst(db *sql.DB, routeID int, lookup *coordLookup, allTT []timetableRow, report *strings.Builder) (int, error) {
	const threshold = 200.0
	resolved := 0

	knownLocs := fcBuildKnownLocations(lookup)
	if len(knownLocs) == 0 {
		return 0, nil
	}
	locIDByName, err := fcGetLocationIDByName(db, routeID)
	if err != nil {
		return 0, err
	}

	for _, tt := range allTT {
		entries, err := fcLoadEntries(db, tt.ID)
		if err != nil {
			return resolved, err
		}
		if len(entries) == 0 {
			continue
		}
		first := entries[0]
		if strings.TrimSpace(ptrStr(first.Location)) != "" {
			continue
		}
		if !fcHasCoords(first) {
			continue
		}
		lat := fcParseLat(first)
		lng := fcParseLng(first)

		var best *knownLoc
		bestDist := math.Inf(1)
		for i := range knownLocs {
			d := haversineMeters(lat, lng, knownLocs[i].Lat, knownLocs[i].Lng)
			if d < bestDist {
				bestDist = d
				best = &knownLocs[i]
			}
		}
		if best != nil && bestDist <= threshold {
			if locID, ok := locIDByName[best.Name]; ok {
				db.Exec("UPDATE timetable_entries SET location_id = ?, coord_source = 'predictive' WHERE id = ?", locID, first.ID)
				resolved++
				fmt.Fprintf(report, "  RESOLVED EMPTY FIRST: timetable %d entry %d -> \"%s\" (%.1fm away)\n", tt.ID, first.ID, best.Name, bestDist)
			}
		} else {
			info := "no known locations"
			if best != nil {
				info = fmt.Sprintf("nearest: \"%s\" at %.1fm", best.Name, bestDist)
			}
			fmt.Fprintf(report, "  UNRESOLVED EMPTY FIRST: timetable %d entry %d - %s\n", tt.ID, first.ID, info)
		}
	}
	return resolved, nil
}

// ── Phase 4: Fill missing entry coordinates ─────────────────────────────────

func fcFillEntryCoords(db *sql.DB, tt timetableRow, direction string, lookup *coordLookup, report *strings.Builder) (int, error) {
	entries, err := fcGetLocationEntries(db, tt.ID)
	if err != nil {
		return 0, err
	}
	updated := 0

	for _, entry := range entries {
		cs := ptrStr(entry.CoordSource)
		if cs == "automatic" && fcHasCoords(entry) {
			continue
		}
		if fcHasCoords(entry) && cs != "predictive" {
			continue
		}

		locName := strings.ToLower(strings.TrimSpace(ptrStr(entry.Location)))
		if locName == "" {
			continue
		}
		platform := strings.TrimSpace(ptrStr(entry.Platform))

		if locName == "track" && platform == "" {
			fmt.Fprintf(report, "  SKIP: timetable %d entry %d - \"track\" without platform number\n", tt.ID, entry.ID)
			continue
		}

		var match *coordData
		matchType := ""

		if platform != "" {
			if cd, ok := lookup.exact[fmt.Sprintf("%s|%s|%s", direction, locName, platform)]; ok {
				match = &cd
				matchType = "exact (dir+loc+platform)"
			}
		}
		if match == nil && locName != "track" {
			if cd, ok := lookup.locOnly[fmt.Sprintf("%s|%s", direction, locName)]; ok {
				match = &cd
				matchType = "fallback (dir+loc, different platform)"
			}
		}
		if match == nil && locName != "track" {
			if cd, ok := lookup.anyDir[locName]; ok {
				match = &cd
				matchType = "cross-direction fallback (any dir, same loc)"
			}
		}

		if match != nil {
			_, err := db.Exec(
				"UPDATE timetable_entries SET latitude = ?, longitude = ?, api_name = ?, coord_source = 'predictive' WHERE id = ?",
				match.Latitude, match.Longitude, match.APIName, entry.ID)
			if err != nil {
				return updated, err
			}
			updated++
			fmt.Fprintf(report, "  FILLED: timetable %d entry %d \"%s\" platform \"%s\" -> %s\n",
				tt.ID, entry.ID, ptrStr(entry.Location), ptrStr(entry.Platform), matchType)
		} else {
			fmt.Fprintf(report, "  MISSING: timetable %d entry %d \"%s\" platform \"%s\" - no match found for direction \"%s\"\n",
				tt.ID, entry.ID, ptrStr(entry.Location), ptrStr(entry.Platform), direction)
		}
	}
	return updated, nil
}

// ── Phase 4b: Infer platform numbers ────────────────────────────────────────

func fcInferPlatforms(db *sql.DB, allTT []timetableRow, lookup *coordLookup, report *strings.Builder) (int, error) {
	const threshold = 2.0

	type platRef struct {
		LocName, Platform string
		Lat, Lng          float64
	}
	var refs []platRef
	for key, data := range lookup.exact {
		parts := strings.SplitN(key, "|", 3)
		if len(parts) < 3 || parts[2] == "" {
			continue
		}
		lat, _ := strconv.ParseFloat(data.Latitude, 64)
		lng, _ := strconv.ParseFloat(data.Longitude, 64)
		refs = append(refs, platRef{LocName: parts[1], Platform: parts[2], Lat: lat, Lng: lng})
	}
	if len(refs) == 0 {
		return 0, nil
	}

	inferred := 0
	for _, tt := range allTT {
		entries, err := fcGetLocationEntries(db, tt.ID)
		if err != nil {
			return inferred, err
		}
		for _, entry := range entries {
			if strings.TrimSpace(ptrStr(entry.Platform)) != "" {
				continue
			}
			if !fcHasCoords(entry) {
				continue
			}
			locName := strings.ToLower(strings.TrimSpace(ptrStr(entry.Location)))
			if locName == "" || locName == "start" {
				continue
			}
			lat := fcParseLat(entry)
			lng := fcParseLng(entry)

			bestPlat := ""
			bestDist := math.Inf(1)
			for _, ref := range refs {
				if ref.LocName != locName {
					continue
				}
				d := haversineMeters(lat, lng, ref.Lat, ref.Lng)
				if d < bestDist {
					bestDist = d
					bestPlat = ref.Platform
				}
			}
			if bestPlat != "" && bestDist <= threshold {
				db.Exec("UPDATE timetable_entries SET platform = ? WHERE id = ?", bestPlat, entry.ID)
				inferred++
				fmt.Fprintf(report, "  PLATFORM: timetable %d entry %d \"%s\" -> platform %s (%.2fm)\n",
					tt.ID, entry.ID, ptrStr(entry.Location), bestPlat, bestDist)
			}
		}
	}
	return inferred, nil
}

// ── Phase 5: Build coordinate paths ─────────────────────────────────────────

func fcExtractLeg(refCoords []coordPoint, latA, lngA, latB, lngB float64) []coordPoint {
	idxA, _ := findClosestPointIndex(refCoords, latA, lngA)
	idxB, _ := findClosestPointIndex(refCoords, latB, lngB)
	if idxA == -1 || idxB == -1 {
		return nil
	}
	lo, hi := idxA, idxB
	if lo > hi {
		lo, hi = hi, lo
	}
	if hi-lo < 2 {
		return nil
	}
	leg := make([]coordPoint, hi-lo+1)
	copy(leg, refCoords[lo:hi+1])
	if idxA > idxB {
		for i, j := 0, len(leg)-1; i < j; i, j = i+1, j-1 {
			leg[i], leg[j] = leg[j], leg[i]
		}
	}
	return leg
}

func fcTrimPath(path []coordPoint, stops []entryRow) []coordPoint {
	if len(path) == 0 || len(stops) < 2 {
		return path
	}
	sIdx, _ := findClosestPointIndex(path, fcParseLat(stops[0]), fcParseLng(stops[0]))
	eIdx, _ := findClosestPointIndex(path, fcParseLat(stops[len(stops)-1]), fcParseLng(stops[len(stops)-1]))
	if sIdx == -1 || eIdx == -1 {
		return path
	}
	lo, hi := sIdx, eIdx
	if lo > hi {
		lo, hi = hi, lo
	}
	trimmed := make([]coordPoint, hi-lo+1)
	copy(trimmed, path[lo:hi+1])
	if sIdx > eIdx {
		for i, j := 0, len(trimmed)-1; i < j; i, j = i+1, j-1 {
			trimmed[i], trimmed[j] = trimmed[j], trimmed[i]
		}
	}
	return trimmed
}

func fcIdenticalStops(a, b []entryRow) bool {
	var la, lb []string
	for _, e := range a {
		loc := strings.TrimSpace(ptrStr(e.Location))
		if loc == "" {
			continue
		}
		la = append(la, fmt.Sprintf("%s|%s", strings.ToLower(loc), ptrStr(e.Platform)))
	}
	for _, e := range b {
		loc := strings.TrimSpace(ptrStr(e.Location))
		if loc == "" {
			continue
		}
		lb = append(lb, fmt.Sprintf("%s|%s", strings.ToLower(loc), ptrStr(e.Platform)))
	}
	if len(la) != len(lb) {
		return false
	}
	for i := range la {
		if la[i] != lb[i] {
			return false
		}
	}
	return true
}

func fcBuildCoordPath(db *sql.DB, tt timetableRow, direction string, sources []pathSource, report *strings.Builder) ([]coordPoint, error) {
	entries, err := fcGetLocationEntries(db, tt.ID)
	if err != nil {
		return nil, err
	}
	var stops []entryRow
	for _, e := range entries {
		if fcHasCoords(e) {
			stops = append(stops, e)
		}
	}
	if len(stops) < 2 {
		fmt.Fprintf(report, "  COORDS: timetable %d - not enough stops with coordinates (%d), skipping\n", tt.ID, len(stops))
		return nil, nil
	}
	if len(sources) == 0 {
		fmt.Fprintf(report, "  COORDS: timetable %d - no timetables with coordinate paths available\n", tt.ID)
		return nil, nil
	}

	var fullPath []coordPoint
	gold, silver, bronze, missed := 0, 0, 0, 0

	for i := 0; i < len(stops)-1; i++ {
		sA, sB := stops[i], stops[i+1]
		locA := strings.ToLower(ptrStr(sA.Location))
		locB := strings.ToLower(ptrStr(sB.Location))
		platA := strings.TrimSpace(ptrStr(sA.Platform))
		platB := strings.TrimSpace(ptrStr(sB.Platform))
		latA, lngA := fcParseLat(sA), fcParseLng(sA)
		latB, lngB := fcParseLat(sB), fcParseLng(sB)

		var bestLeg []coordPoint
		tier := ""

		// Gold
		for _, src := range sources {
			if src.Bound != direction {
				continue
			}
			fA, fB := false, false
			for _, s := range src.Stops {
				if strings.ToLower(ptrStr(s.Location)) == locA && strings.TrimSpace(ptrStr(s.Platform)) == platA {
					fA = true
				}
				if strings.ToLower(ptrStr(s.Location)) == locB && strings.TrimSpace(ptrStr(s.Platform)) == platB {
					fB = true
				}
			}
			if !fA || !fB {
				continue
			}
			if leg := fcExtractLeg(src.Coords, latA, lngA, latB, lngB); leg != nil && len(leg) > 1 {
				bestLeg = leg
				tier = "gold"
				break
			}
		}
		// Silver
		if bestLeg == nil {
			for _, src := range sources {
				if src.Bound != direction {
					continue
				}
				fA, fB := false, false
				for _, s := range src.Stops {
					if strings.ToLower(ptrStr(s.Location)) == locA {
						fA = true
					}
					if strings.ToLower(ptrStr(s.Location)) == locB {
						fB = true
					}
				}
				if !fA || !fB {
					continue
				}
				if leg := fcExtractLeg(src.Coords, latA, lngA, latB, lngB); leg != nil && len(leg) > 1 {
					bestLeg = leg
					tier = "silver"
					break
				}
			}
		}
		// Bronze
		if bestLeg == nil {
			for _, src := range sources {
				if src.Bound == direction {
					continue
				}
				fA, fB := false, false
				for _, s := range src.Stops {
					if strings.ToLower(ptrStr(s.Location)) == locA {
						fA = true
					}
					if strings.ToLower(ptrStr(s.Location)) == locB {
						fB = true
					}
				}
				if !fA || !fB {
					continue
				}
				if leg := fcExtractLeg(src.Coords, latA, lngA, latB, lngB); leg != nil && len(leg) > 1 {
					bestLeg = leg
					tier = "bronze"
					break
				}
			}
		}

		if bestLeg != nil {
			off := 0
			if len(fullPath) > 0 {
				off = 1
			}
			for j := off; j < len(bestLeg); j++ {
				fullPath = append(fullPath, bestLeg[j])
			}
			switch tier {
			case "gold":
				gold++
			case "silver":
				silver++
			case "bronze":
				bronze++
			}
		} else {
			missed++
			fmt.Fprintf(report, "  LEG MISS: timetable %d \"%s\" -> \"%s\" - no source path covers both stops\n",
				tt.ID, ptrStr(sA.Location), ptrStr(sB.Location))
		}
	}

	fmt.Fprintf(report, "  COORDS: timetable %d - gold: %d, silver: %d, bronze: %d, missed: %d, %d points\n",
		tt.ID, gold, silver, bronze, missed, len(fullPath))

	if len(fullPath) == 0 {
		return nil, nil
	}
	return fcTrimPath(fullPath, stops), nil
}

// ── Phase 6: Missing stops report ───────────────────────────────────────────

func fcMissingReport(db *sql.DB, routeID int, routeName string, allTT []timetableRow, dirTemplates map[string][]string, report *strings.Builder) error {
	report.WriteString("\n=== MISSING STOPS REPORT ===\n")
	fmt.Fprintf(report, "Route: %s (id: %d)\n\n", routeName, routeID)

	missingByDir := map[string]map[string]bool{}
	for _, tt := range allTT {
		entries, err := fcGetLocationEntries(db, tt.ID)
		if err != nil {
			return err
		}
		dir := ptrStr(tt.Bound)
		if dir == "" {
			dir = fcInferDirection(entries, dirTemplates)
		}
		if dir == "" {
			dir = "unknown"
		}
		if missingByDir[dir] == nil {
			missingByDir[dir] = map[string]bool{}
		}
		for _, e := range entries {
			if !fcHasCoords(e) {
				key := fmt.Sprintf("%s|%s", strings.ToLower(ptrStr(e.Location)), ptrStr(e.Platform))
				missingByDir[dir][key] = true
			}
		}
	}

	for dir, missing := range missingByDir {
		if len(missing) == 0 {
			fmt.Fprintf(report, "Direction \"%s\": All stops have coordinates!\n", dir)
		} else {
			fmt.Fprintf(report, "Direction \"%s\": %d missing stop(s):\n", dir, len(missing))
			for key := range missing {
				parts := strings.SplitN(key, "|", 2)
				loc := parts[0]
				plat := ""
				if len(parts) > 1 {
					plat = parts[1]
				}
				if plat != "" {
					fmt.Fprintf(report, "  - \"%s\" platform %s\n", loc, plat)
				} else {
					fmt.Fprintf(report, "  - \"%s\"\n", loc)
				}
			}
		}
		report.WriteString("\n")
	}

	var noCoords []timetableRow
	for _, tt := range allTT {
		coords, _, err := fcLoadTimetableCoordinates(db, tt.ID)
		if err != nil {
			return err
		}
		if len(coords) == 0 {
			noCoords = append(noCoords, tt)
		}
	}
	if len(noCoords) > 0 {
		report.WriteString("Timetables with NO coordinate path:\n")
		for _, tt := range noCoords {
			fmt.Fprintf(report, "  - ID %d: \"%s\"\n", tt.ID, tt.ServiceName)
		}
	}
	return nil
}

// ── Main fill coordinates function ──────────────────────────────────────────

func fillCoordinatesForRoute(db *sql.DB, routeID int) (string, error) {
	var report strings.Builder

	report.WriteString("=== Fill Coordinates Script ===\n")
	fmt.Fprintf(&report, "Route ID: %d\n\n", routeID)

	var routeName string
	err := db.QueryRow("SELECT name FROM routes WHERE id = ?", routeID).Scan(&routeName)
	if err == sql.ErrNoRows {
		return "", fmt.Errorf("route %d not found", routeID)
	}
	if err != nil {
		return "", err
	}

	allTT, err := fcGetTimetables(db, routeID)
	if err != nil {
		return "", fmt.Errorf("loading timetables: %w", err)
	}
	fmt.Fprintf(&report, "Route: %s (id: %d)\n", routeName, routeID)
	fmt.Fprintf(&report, "Total timetables: %d\n\n", len(allTT))

	// Phase 1
	report.WriteString("=== PHASE 1: DIRECTION TEMPLATES ===\n")
	dirTemplates, err := fcBuildDirectionTemplates(db, allTT)
	if err != nil {
		return "", fmt.Errorf("building direction templates: %w", err)
	}
	dirs := make([]string, 0, len(dirTemplates))
	for d := range dirTemplates {
		dirs = append(dirs, d)
	}
	fmt.Fprintf(&report, "Directions: %s\n\n", strings.Join(dirs, ", "))

	// Phase 2
	report.WriteString("=== PHASE 2: BUILD COORDINATE LOOKUP ===\n")
	lookup, err := fcBuildLookup(db, allTT, dirTemplates, &report)
	if err != nil {
		return "", fmt.Errorf("building coordinate lookup: %w", err)
	}

	// Phase 3
	report.WriteString("=== PHASE 3: RESOLVE \"start\" LOCATIONS ===\n")
	startResolved, err := fcResolveStartLocations(db, routeID, lookup, &report)
	if err != nil {
		return "", fmt.Errorf("resolving start locations: %w", err)
	}
	fmt.Fprintf(&report, "Resolved: %d\n\n", startResolved)

	// Phase 3b
	report.WriteString("=== PHASE 3b: RESOLVE EMPTY FIRST ENTRIES ===\n")
	emptyResolved, err := fcResolveEmptyFirst(db, routeID, lookup, allTT, &report)
	if err != nil {
		return "", fmt.Errorf("resolving empty first entries: %w", err)
	}
	fmt.Fprintf(&report, "Resolved: %d\n\n", emptyResolved)

	// Phase 4
	report.WriteString("=== PHASE 4: FILL ENTRY COORDINATES ===\n")
	totalUpdates := 0
	inferredDirs := map[int]string{}

	for _, tt := range allTT {
		entries, err := fcGetLocationEntries(db, tt.ID)
		if err != nil {
			return "", err
		}
		direction := ptrStr(tt.Bound)
		if direction == "" {
			direction = fcInferDirection(entries, dirTemplates)
		}
		if direction == "" {
			direction = "unknown"
		}
		inferredDirs[tt.ID] = direction

		if ptrStr(tt.Bound) == "" && direction != "unknown" {
			db.Exec("UPDATE timetables SET bound = ? WHERE id = ?", direction, tt.ID)
			fmt.Fprintf(&report, "Timetable %d \"%s\" -> direction: %s (inferred and saved)\n", tt.ID, tt.ServiceName, direction)
		} else {
			fmt.Fprintf(&report, "Timetable %d \"%s\" -> direction: %s\n", tt.ID, tt.ServiceName, direction)
		}

		count, err := fcFillEntryCoords(db, tt, direction, lookup, &report)
		if err != nil {
			return "", err
		}
		totalUpdates += count
	}
	fmt.Fprintf(&report, "\nTotal entry coordinates filled: %d\n\n", totalUpdates)

	// Phase 4b
	report.WriteString("=== PHASE 4b: INFER PLATFORM NUMBERS ===\n")
	platInferred, err := fcInferPlatforms(db, allTT, lookup, &report)
	if err != nil {
		return "", fmt.Errorf("inferring platform numbers: %w", err)
	}
	fmt.Fprintf(&report, "Platforms inferred: %d\n\n", platInferred)

	// Phase 5
	report.WriteString("=== PHASE 5: BUILD COORDINATE PATHS ===\n")
	var sources []pathSource
	for _, tt := range allTT {
		coords, cs, err := fcLoadTimetableCoordinates(db, tt.ID)
		if err != nil {
			return "", err
		}
		if len(coords) == 0 || (cs != "automatic" && cs != "manual" && cs != "imported") {
			continue
		}
		entries, err := fcGetLocationEntries(db, tt.ID)
		if err != nil {
			return "", err
		}
		var stops []entryRow
		for _, e := range entries {
			if fcHasCoords(e) {
				stops = append(stops, e)
			}
		}
		bound := ptrStr(tt.Bound)
		if bound == "" {
			bound = inferredDirs[tt.ID]
		}
		if bound == "" {
			bound = "unknown"
		}
		sources = append(sources, pathSource{ID: tt.ID, Bound: bound, Coords: coords, Stops: stops})
	}
	fmt.Fprintf(&report, "Path sources: %d timetables with automatic/manual/imported paths\n", len(sources))

	pathsBuilt := 0
	for _, tt := range allTT {
		existing, existSrc, err := fcLoadTimetableCoordinates(db, tt.ID)
		if err != nil {
			return "", err
		}
		if len(existing) > 0 && (existSrc == "automatic" || existSrc == "manual" || existSrc == "imported") {
			continue
		}

		entries, err := fcGetLocationEntries(db, tt.ID)
		if err != nil {
			return "", err
		}
		direction := inferredDirs[tt.ID]
		if direction == "" {
			direction = ptrStr(tt.Bound)
		}
		if direction == "" {
			direction = "unknown"
		}

		copied := false
		for _, src := range sources {
			if src.Bound != direction {
				continue
			}
			srcEntries, err := fcGetLocationEntries(db, src.ID)
			if err != nil {
				return "", err
			}
			if fcIdenticalStops(entries, srcEntries) {
				trimmed := fcTrimPath(src.Coords, entries)
				cJSON, _ := json.Marshal(trimmed)
				db.Exec("DELETE FROM timetable_coordinates WHERE timetable_id = ?", tt.ID)
				db.Exec("INSERT INTO timetable_coordinates (timetable_id, coordinates, coord_source) VALUES (?, ?, 'predictive')",
					tt.ID, string(cJSON))
				pathsBuilt++
				copied = true
				fmt.Fprintf(&report, "  COPIED: timetable %d <- timetable %d (identical stops, %d points)\n", tt.ID, src.ID, len(trimmed))
				break
			}
		}
		if copied {
			continue
		}

		coordPath, err := fcBuildCoordPath(db, tt, direction, sources, &report)
		if err != nil {
			return "", err
		}
		if coordPath != nil {
			cJSON, _ := json.Marshal(coordPath)
			db.Exec("DELETE FROM timetable_coordinates WHERE timetable_id = ?", tt.ID)
			db.Exec("INSERT INTO timetable_coordinates (timetable_id, coordinates, coord_source) VALUES (?, ?, 'predictive')",
				tt.ID, string(cJSON))
			pathsBuilt++
		}
	}
	fmt.Fprintf(&report, "\nTotal coordinate paths built: %d\n\n", pathsBuilt)

	// Phase 6
	if err := fcMissingReport(db, routeID, routeName, allTT, dirTemplates, &report); err != nil {
		return "", fmt.Errorf("generating missing report: %w", err)
	}

	return report.String(), nil
}

// ── Build Route Map ─────────────────────────────────────────────────────────

func filterChatham(coords []coordPoint) []coordPoint {
	var result []coordPoint
	for _, c := range coords {
		if c.Latitude == 51.380108707397724 && c.Longitude == 0.5219243867730494 {
			continue
		}
		result = append(result, c)
	}
	return result
}

func isPointNew(knownPoints []coordPoint, lat, lng, thresholdMeters float64) bool {
	degThreshold := thresholdMeters / 111000.0
	for _, kp := range knownPoints {
		if math.Abs(kp.Latitude-lat) < degThreshold && math.Abs(kp.Longitude-lng) < degThreshold {
			if haversineMeters(lat, lng, kp.Latitude, kp.Longitude) < thresholdMeters {
				return false
			}
		}
	}
	return true
}

func mergeCoordinates(allArrays [][]coordPoint) [][]coordPoint {
	if len(allArrays) == 0 {
		return nil
	}
	// Sort by length descending (bubble sort is fine for small N)
	for i := 0; i < len(allArrays)-1; i++ {
		for j := i + 1; j < len(allArrays); j++ {
			if len(allArrays[j]) > len(allArrays[i]) {
				allArrays[i], allArrays[j] = allArrays[j], allArrays[i]
			}
		}
	}
	base := allArrays[0]
	if len(base) == 0 {
		return nil
	}

	const newTrackThreshold = 2.0
	const minSegLen = 5

	segments := [][]coordPoint{base}
	known := make([]coordPoint, len(base))
	copy(known, base)

	for i := 1; i < len(allArrays); i++ {
		p := allArrays[i]
		if len(p) < minSegLen {
			continue
		}
		var run []coordPoint
		for _, pt := range p {
			if isPointNew(known, pt.Latitude, pt.Longitude, newTrackThreshold) {
				run = append(run, pt)
			} else {
				if len(run) >= minSegLen {
					segments = append(segments, run)
					known = append(known, run...)
				}
				run = nil
			}
		}
		if len(run) >= minSegLen {
			segments = append(segments, run)
			known = append(known, run...)
		}
	}
	return segments
}

func toFloat64(v any) (float64, bool) {
	switch val := v.(type) {
	case float64:
		return val, true
	case float32:
		return float64(val), true
	case int64:
		return float64(val), true
	case int:
		return float64(val), true
	case string:
		f, err := strconv.ParseFloat(val, 64)
		return f, err == nil
	default:
		return 0, false
	}
}

func buildRouteMap(db *sql.DB, routeID int) error {
	rows, err := db.Query("SELECT id FROM timetables WHERE route_id = ?", routeID)
	if err != nil {
		return err
	}
	defer rows.Close()
	var ttIDs []int
	for rows.Next() {
		var id int
		if err := rows.Scan(&id); err != nil {
			return err
		}
		ttIDs = append(ttIDs, id)
	}
	if err := rows.Err(); err != nil {
		return err
	}
	if len(ttIDs) == 0 {
		return nil
	}

	// 1. Coordinates
	type coordEntry struct {
		coords []coordPoint
		source string
	}
	var allCoords []coordEntry
	for _, ttID := range ttIDs {
		coords, source, err := fcLoadTimetableCoordinates(db, ttID)
		if err != nil {
			continue
		}
		filtered := filterChatham(coords)
		if len(filtered) > 0 {
			allCoords = append(allCoords, coordEntry{coords: filtered, source: source})
		}
	}

	var nonPred, allArr [][]coordPoint
	for _, ce := range allCoords {
		allArr = append(allArr, ce.coords)
		if ce.source != "predictive" {
			nonPred = append(nonPred, ce.coords)
		}
	}
	ca := nonPred
	if len(ca) == 0 {
		ca = allArr
	}
	merged := mergeCoordinates(ca)
	cJSON, _ := json.Marshal(merged)

	db.Exec("DELETE FROM route_coordinates WHERE route_id = ?", routeID)
	if merged != nil {
		db.Exec("INSERT INTO route_coordinates (route_id, coordinates) VALUES (?, ?)", routeID, string(cJSON))
	}

	// 2. Markers
	var allMarkerArrays [][]map[string]any
	for _, ttID := range ttIDs {
		mRows, err := db.Query("SELECT station_name, marker_type, latitude, longitude, platform_length FROM timetable_markers WHERE timetable_id = ?", ttID)
		if err != nil {
			continue
		}
		var markers []map[string]any
		for mRows.Next() {
			var sn string
			var mt sql.NullString
			var lat, lng, pl sql.NullFloat64
			if err := mRows.Scan(&sn, &mt, &lat, &lng, &pl); err != nil {
				continue
			}
			m := map[string]any{"station_name": sn}
			if mt.Valid {
				m["marker_type"] = mt.String
			}
			if lat.Valid {
				m["latitude"] = lat.Float64
			}
			if lng.Valid {
				m["longitude"] = lng.Float64
			}
			if pl.Valid {
				m["platform_length"] = pl.Float64
			}
			markers = append(markers, m)
		}
		mRows.Close()
		if len(markers) > 0 {
			allMarkerArrays = append(allMarkerArrays, markers)
		}
	}

	// Merge markers
	type markerAcc struct {
		StationName, MarkerType string
		Lats, Lngs              []float64
		PlatformLength          float64
	}
	mm := map[string]*markerAcc{}
	for _, markers := range allMarkerArrays {
		for _, m := range markers {
			sn, _ := m["station_name"].(string)
			mt, _ := m["marker_type"].(string)
			key := fmt.Sprintf("%s|%s", sn, mt)
			if mm[key] == nil {
				mm[key] = &markerAcc{StationName: sn, MarkerType: mt}
			}
			acc := mm[key]
			if lat, ok := toFloat64(m["latitude"]); ok && lat != 0 {
				if lng, ok2 := toFloat64(m["longitude"]); ok2 && lng != 0 {
					acc.Lats = append(acc.Lats, lat)
					acc.Lngs = append(acc.Lngs, lng)
				}
			}
			if pl, ok := toFloat64(m["platform_length"]); ok && pl > acc.PlatformLength {
				acc.PlatformLength = pl
			}
		}
	}

	db.Exec("DELETE FROM route_markers WHERE route_id = ?", routeID)
	for _, acc := range mm {
		var latVal, lngVal, plVal interface{}
		if len(acc.Lats) > 0 {
			s := 0.0
			for _, v := range acc.Lats {
				s += v
			}
			latVal = s / float64(len(acc.Lats))
		}
		if len(acc.Lngs) > 0 {
			s := 0.0
			for _, v := range acc.Lngs {
				s += v
			}
			lngVal = s / float64(len(acc.Lngs))
		}
		if acc.PlatformLength != 0 {
			plVal = acc.PlatformLength
		}
		db.Exec("INSERT OR REPLACE INTO route_markers (route_id, station_name, marker_type, latitude, longitude, platform_length) VALUES (?, ?, ?, ?, ?, ?)",
			routeID, acc.StationName, acc.MarkerType, latVal, lngVal, plVal)
	}

	// 3. Locations
	type locAcc struct {
		LocationID *int64
		Name       string
		Bound      string
		Platform   string
		Lats, Lngs []float64
	}
	lm := map[string]*locAcc{}
	for _, ttID := range ttIDs {
		var bound sql.NullString
		db.QueryRow("SELECT bound FROM timetables WHERE id = ?", ttID).Scan(&bound)
		boundStr := ""
		if bound.Valid {
			boundStr = bound.String
		}

		eRows, err := db.Query(`
			SELECT te.location_id, l.name, te.platform, te.latitude, te.longitude
			FROM timetable_entries te
			LEFT JOIN locations l ON l.id = te.location_id
			WHERE te.timetable_id = ?
			ORDER BY te.sort_order`, ttID)
		if err != nil {
			continue
		}
		for eRows.Next() {
			var locID sql.NullInt64
			var locName, platform, latStr, lngStr sql.NullString
			if err := eRows.Scan(&locID, &locName, &platform, &latStr, &lngStr); err != nil {
				continue
			}
			name := strings.TrimSpace("")
			if locName.Valid {
				name = strings.TrimSpace(locName.String)
			}
			if name == "" {
				continue
			}
			platStr := ""
			if platform.Valid {
				platStr = platform.String
			}
			key := fmt.Sprintf("%s|%s|%s", name, boundStr, platStr)
			if lm[key] == nil {
				lm[key] = &locAcc{Name: name, Bound: boundStr, Platform: platStr}
			}
			acc := lm[key]
			if locID.Valid && acc.LocationID == nil {
				id := locID.Int64
				acc.LocationID = &id
			}
			if latStr.Valid && lngStr.Valid {
				lat, e1 := strconv.ParseFloat(latStr.String, 64)
				lng, e2 := strconv.ParseFloat(lngStr.String, 64)
				if e1 == nil && e2 == nil && (lat != 0 || lng != 0) {
					acc.Lats = append(acc.Lats, lat)
					acc.Lngs = append(acc.Lngs, lng)
				}
			}
		}
		eRows.Close()
	}

	db.Exec("DELETE FROM route_locations WHERE route_id = ?", routeID)
	for _, acc := range lm {
		var lat, lng interface{}
		if len(acc.Lats) > 0 {
			s := 0.0
			for _, v := range acc.Lats {
				s += v
			}
			lat = s / float64(len(acc.Lats))
		}
		if len(acc.Lngs) > 0 {
			s := 0.0
			for _, v := range acc.Lngs {
				s += v
			}
			lng = s / float64(len(acc.Lngs))
		}
		var locIDVal, boundVal, platVal interface{}
		if acc.LocationID != nil {
			locIDVal = *acc.LocationID
		}
		if acc.Bound != "" {
			boundVal = acc.Bound
		}
		if acc.Platform != "" {
			platVal = acc.Platform
		}
		db.Exec("INSERT OR REPLACE INTO route_locations (route_id, location_id, name, bound, platform, latitude, longitude) VALUES (?, ?, ?, ?, ?, ?, ?)",
			routeID, locIDVal, acc.Name, boundVal, platVal, lat, lng)
	}

	return nil
}
