// merge-db — merge route-attached data from a source SQLite DB into a
// copy of a base SQLite DB.
//
// Usage:
//
//   merge-db <base.db> <src.db> <out.db>
//
// Strategy: copy base → out, attach src, walk src.routes. For each
// route whose cross_pak_reference_name is set AND matches a row already
// in out, skip (canonical duplicate). Everything else copies in,
// including routes with empty cpr — the user dedupes those manually
// afterwards.
//
// Everything route-attached migrates with full ID remapping:
//
//   - routes              (FK country_id remapped via country code)
//   - locations           (per route_id)
//   - sections            (per route_id)
//   - car_stop_signs      (per route_id)
//   - track_markers       (per route_id)
//   - route_coordinates   (per route_id)
//   - route_markers       (per route_id)
//   - route_locations     (per route_id; references locations.id)
//   - route_formations    (per route_id; references formations.id)
//   - station_name_mappings (per route_id)
//   - timetables          (per route_id; references formation_id, section_id)
//   - timetable_entries   (per timetable_id; refs location/action/car_stop_sign/track_marker)
//   - timetable_coordinates (per timetable_id)
//   - timetable_formations  (per timetable_id, formation_id)
//   - timetable_markers     (per timetable_id)
//   - timetable_sections    (per timetable_id, section_id)
//   - timetable_map_features (per timetable_id)
//   - formations            (referenced by timetable.formation_id et al; class_id → train_classes.id)
//   - formation_vehicles    (per formation_id)
//   - section_formations    (per section_id, formation_id)
//   - train_classes         (name-matched against out; missing classes inserted fresh; src's `in_game_name` dropped)
//   - train_class_electrification (per train_class_id)
//
// Skipped (catalog / install-state / seed): pak_catalog, pak_rvds,
// timetable_actions, weather_presets, extractor_completed_routes,
// countries (mapped by code instead of copied).
//
// action_id values in timetable_entries are passed through as-is —
// timetable_actions is a stable seed table and IDs match across DBs.
package main

import (
	"database/sql"
	"fmt"
	"io"
	"log"
	"os"

	_ "modernc.org/sqlite"
)

func main() {
	if len(os.Args) != 4 {
		log.Fatalf("usage: %s <base.db> <src.db> <out.db>", os.Args[0])
	}
	basePath := os.Args[1]
	srcPath := os.Args[2]
	outPath := os.Args[3]

	// 1. Copy base → out
	if err := copyFile(basePath, outPath); err != nil {
		log.Fatalf("copy base: %v", err)
	}
	fmt.Printf("copied %s → %s\n", basePath, outPath)

	// 2. Open out, attach src
	db, err := sql.Open("sqlite", outPath)
	if err != nil {
		log.Fatalf("open out: %v", err)
	}
	defer db.Close()
	if _, err := db.Exec(`ATTACH DATABASE ? AS src`, srcPath); err != nil {
		log.Fatalf("attach src: %v", err)
	}
	if _, err := db.Exec(`PRAGMA foreign_keys = OFF`); err != nil {
		log.Fatalf("disable FK: %v", err)
	}

	// 3. country_id remap: src.country_id → main.country_id (by code).
	countryMap := buildCountryMap(db)
	fmt.Printf("country_id map: %d entries\n", len(countryMap))

	// 4. train_class_id remap: built lazily as we encounter formations.
	classMap := map[int64]int64{} // src class_id → main class_id

	// 5. Decide which routes to migrate (cpr not in main, OR cpr empty).
	mainCprs := loadCprs(db, "main")
	srcRoutes := []srcRoute{}
	rows, err := db.Query(`SELECT id, name, country_id, tsw_version, cross_pak_reference_name FROM src.routes ORDER BY id`)
	if err != nil {
		log.Fatalf("list src routes: %v", err)
	}
	for rows.Next() {
		var r srcRoute
		var cpr sql.NullString
		if err := rows.Scan(&r.ID, &r.Name, &r.CountryID, &r.TSWVersion, &cpr); err != nil {
			log.Fatalf("scan: %v", err)
		}
		r.CPR = cpr.String
		srcRoutes = append(srcRoutes, r)
	}
	rows.Close()
	fmt.Printf("src has %d routes\n", len(srcRoutes))

	skipped := 0
	migrated := 0
	for _, r := range srcRoutes {
		if r.CPR != "" && mainCprs[r.CPR] {
			skipped++
			continue
		}
		// Build remaps + insert one route per transaction so a partial
		// failure on one route doesn't trash everything migrated so far.
		tx, err := db.Begin()
		if err != nil {
			log.Fatalf("begin tx: %v", err)
		}
		newRouteID, err := migrateOneRoute(tx, r, countryMap, classMap)
		if err != nil {
			tx.Rollback()
			fmt.Printf("  ✗ src route %d %q: %v\n", r.ID, r.Name, err)
			continue
		}
		if err := tx.Commit(); err != nil {
			fmt.Printf("  ✗ src route %d commit: %v\n", r.ID, err)
			continue
		}
		fmt.Printf("  ✓ src route %d %q → out route %d\n", r.ID, r.Name, newRouteID)
		migrated++
	}

	fmt.Printf("\nDone. Migrated %d routes; skipped %d (cpr collision).\n", migrated, skipped)
}

type srcRoute struct {
	ID         int64
	Name       string
	CountryID  int64
	TSWVersion sql.NullInt64
	CPR        string
}

func buildCountryMap(db *sql.DB) map[int64]int64 {
	out := map[int64]int64{}
	// out.countries by code
	mainByCode := map[string]int64{}
	rows, _ := db.Query(`SELECT id, code FROM main.countries`)
	for rows.Next() {
		var id int64
		var code sql.NullString
		rows.Scan(&id, &code)
		if code.Valid && code.String != "" {
			mainByCode[code.String] = id
		}
	}
	rows.Close()

	// For each src country, map to main by code. If main lacks a matching
	// code, INSERT a new country row.
	rows, _ = db.Query(`SELECT id, name, code FROM src.countries`)
	for rows.Next() {
		var id int64
		var name, code sql.NullString
		rows.Scan(&id, &name, &code)
		if !code.Valid || code.String == "" {
			continue
		}
		if mid, ok := mainByCode[code.String]; ok {
			out[id] = mid
			continue
		}
		res, err := db.Exec(`INSERT INTO main.countries (name, code) VALUES (?, ?)`, name.String, code.String)
		if err != nil {
			log.Printf("insert country %q: %v", code.String, err)
			continue
		}
		nid, _ := res.LastInsertId()
		mainByCode[code.String] = nid
		out[id] = nid
	}
	rows.Close()
	return out
}

func loadCprs(db *sql.DB, schema string) map[string]bool {
	out := map[string]bool{}
	rows, _ := db.Query(fmt.Sprintf(`SELECT cross_pak_reference_name FROM %s.routes WHERE cross_pak_reference_name IS NOT NULL AND cross_pak_reference_name != ''`, schema))
	for rows.Next() {
		var s string
		rows.Scan(&s)
		out[s] = true
	}
	rows.Close()
	return out
}

// migrateOneRoute does the full per-route migration inside one tx.
// classMap is shared across all routes (train_classes are global).
func migrateOneRoute(tx *sql.Tx, r srcRoute, countryMap map[int64]int64, classMap map[int64]int64) (int64, error) {
	// 1. INSERT route
	cid := countryMap[r.CountryID]
	if cid == 0 {
		return 0, fmt.Errorf("no country mapping for src country_id=%d", r.CountryID)
	}
	var ver any
	if r.TSWVersion.Valid {
		ver = r.TSWVersion.Int64
	}
	cpr := any(nil)
	if r.CPR != "" {
		cpr = r.CPR
	}
	// main.routes has a UNIQUE constraint on `name`. When the src
	// route's name collides with one already in main (same name,
	// different content from an older extraction), append a "#N"
	// disambiguator suffix so the import goes through. Without this we
	// silently drop routes whose names happen to overlap.
	name := r.Name
	res, err := tx.Exec(`INSERT INTO main.routes (name, country_id, tsw_version, cross_pak_reference_name) VALUES (?, ?, ?, ?)`,
		name, cid, ver, cpr)
	if err != nil {
		for suffix := 2; suffix <= 20; suffix++ {
			candidate := fmt.Sprintf("%s #%d", r.Name, suffix)
			res, err = tx.Exec(`INSERT INTO main.routes (name, country_id, tsw_version, cross_pak_reference_name) VALUES (?, ?, ?, ?)`,
				candidate, cid, ver, cpr)
			if err == nil {
				name = candidate
				break
			}
		}
	}
	if err != nil {
		return 0, fmt.Errorf("insert route (after suffix retries): %w", err)
	}
	_ = name
	newRouteID, _ := res.LastInsertId()

	// Per-route id maps
	locMap := map[int64]int64{}
	secMap := map[int64]int64{}
	cssMap := map[int64]int64{}
	tmkMap := map[int64]int64{}
	fmtMap := map[int64]int64{}
	ttMap := map[int64]int64{}

	// 2. locations (route_id FK)
	if err := remapInsert(tx, "locations",
		[]string{"name"},
		[]string{"route_id"}, []int64{newRouteID},
		"route_id", r.ID, locMap, nil); err != nil {
		return 0, fmt.Errorf("locations: %w", err)
	}

	// 3. sections
	if err := remapInsert(tx, "sections",
		[]string{"name"},
		[]string{"route_id"}, []int64{newRouteID},
		"route_id", r.ID, secMap, nil); err != nil {
		return 0, fmt.Errorf("sections: %w", err)
	}

	// 4. car_stop_signs
	if err := remapInsert(tx, "car_stop_signs",
		[]string{"platform_name", "ribbon_guid", "location", "max_rail_vehicles", "latitude", "longitude"},
		[]string{"route_id"}, []int64{newRouteID},
		"route_id", r.ID, cssMap, nil); err != nil {
		return 0, fmt.Errorf("car_stop_signs: %w", err)
	}

	// 5. track_markers
	if err := remapInsert(tx, "track_markers",
		[]string{"name", "marker_type", "ribbon_guid", "location", "start", "end", "line_side", "latitude", "longitude"},
		[]string{"route_id"}, []int64{newRouteID},
		"route_id", r.ID, tmkMap, nil); err != nil {
		return 0, fmt.Errorf("track_markers: %w", err)
	}

	// 6. route_coordinates (no PK to map; route_id FK only)
	if err := simpleCopy(tx, "route_coordinates",
		[]string{"coordinates", "updated_at"},
		[]string{"route_id"}, []int64{newRouteID},
		"route_id", r.ID); err != nil {
		return 0, fmt.Errorf("route_coordinates: %w", err)
	}

	// 7. route_markers
	if err := simpleCopy(tx, "route_markers",
		[]string{"station_name", "marker_type", "latitude", "longitude", "platform_length"},
		[]string{"route_id"}, []int64{newRouteID},
		"route_id", r.ID); err != nil {
		return 0, fmt.Errorf("route_markers: %w", err)
	}

	// 8. route_locations (uses locations.id — needs remap)
	if err := copyWithRemap(tx, "route_locations",
		[]string{"name", "bound", "platform", "latitude", "longitude"},
		map[string]int64{"route_id": newRouteID},
		map[string]map[int64]int64{"location_id": locMap},
		"route_id", r.ID); err != nil {
		return 0, fmt.Errorf("route_locations: %w", err)
	}

	// 9. station_name_mappings
	if err := simpleCopy(tx, "station_name_mappings",
		[]string{"display_name", "api_name", "created_at"},
		[]string{"route_id"}, []int64{newRouteID},
		"route_id", r.ID); err != nil {
		return 0, fmt.Errorf("station_name_mappings: %w", err)
	}

	// 10. Walk timetables on this route
	ttRows, err := tx.Query(`SELECT id, service_name, formation_id, service_type, contributor, coordinates_contributor, created_at, start_time, duration, service_images, section_id, conductor_compatible, bound, service, current_service_name, source, playable FROM src.timetables WHERE route_id = ?`, r.ID)
	if err != nil {
		return 0, fmt.Errorf("list src timetables: %w", err)
	}
	type ttRow struct {
		id              int64
		serviceName     sql.NullString
		formationID     sql.NullInt64
		serviceType     sql.NullString
		contributor     sql.NullString
		coordsContrib   sql.NullString
		createdAt       sql.NullString
		startTime       sql.NullString
		duration        sql.NullString
		serviceImages   sql.NullString
		sectionID       sql.NullInt64
		conductor       sql.NullInt64
		bound           sql.NullString
		service         sql.NullString
		curServiceName  sql.NullString
		source          sql.NullString
		playable        sql.NullInt64
	}
	var tts []ttRow
	for ttRows.Next() {
		var t ttRow
		if err := ttRows.Scan(&t.id, &t.serviceName, &t.formationID, &t.serviceType, &t.contributor, &t.coordsContrib, &t.createdAt, &t.startTime, &t.duration, &t.serviceImages, &t.sectionID, &t.conductor, &t.bound, &t.service, &t.curServiceName, &t.source, &t.playable); err != nil {
			ttRows.Close()
			return 0, fmt.Errorf("scan timetable: %w", err)
		}
		tts = append(tts, t)
	}
	ttRows.Close()

	for _, t := range tts {
		// Ensure the formation referenced by this timetable is migrated
		var newFormationID any
		if t.formationID.Valid {
			fid, err := ensureFormation(tx, t.formationID.Int64, fmtMap, classMap)
			if err != nil {
				return 0, fmt.Errorf("ensure formation %d: %w", t.formationID.Int64, err)
			}
			newFormationID = fid
		}
		// Section
		var newSectionID any
		if t.sectionID.Valid {
			if mapped, ok := secMap[t.sectionID.Int64]; ok {
				newSectionID = mapped
			}
		}

		res, err := tx.Exec(`INSERT INTO main.timetables
			(service_name, route_id, formation_id, service_type, contributor, coordinates_contributor, created_at, start_time, duration, service_images, section_id, conductor_compatible, bound, service, current_service_name, source, playable)
			VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
			nullStrAny(t.serviceName), newRouteID, newFormationID, nullStrAny(t.serviceType), nullStrAny(t.contributor), nullStrAny(t.coordsContrib), nullStrAny(t.createdAt), nullStrAny(t.startTime), nullStrAny(t.duration), nullStrAny(t.serviceImages), newSectionID, nullIntAny(t.conductor), nullStrAny(t.bound), nullStrAny(t.service), nullStrAny(t.curServiceName), nullStrAny(t.source), nullIntAny(t.playable))
		if err != nil {
			return 0, fmt.Errorf("insert timetable: %w", err)
		}
		newTTID, _ := res.LastInsertId()
		ttMap[t.id] = newTTID

		// timetable_entries — remap location_id, car_stop_sign_id, track_marker_id (action_id passes through)
		if err := copyWithRemap(tx, "timetable_entries",
			[]string{"action_id", "details", "structure_number", "structure", "time1", "time2", "latitude", "longitude", "tile_x", "tile_y", "api_name", "sort_order", "coord_source"},
			map[string]int64{"timetable_id": newTTID},
			map[string]map[int64]int64{"location_id": locMap, "car_stop_sign_id": cssMap, "track_marker_id": tmkMap},
			"timetable_id", t.id); err != nil {
			return 0, fmt.Errorf("timetable_entries: %w", err)
		}
		// timetable_coordinates
		if err := simpleCopy(tx, "timetable_coordinates",
			[]string{"coordinates", "coord_source"},
			[]string{"timetable_id"}, []int64{newTTID},
			"timetable_id", t.id); err != nil {
			return 0, fmt.Errorf("timetable_coordinates: %w", err)
		}
		// timetable_markers
		if err := simpleCopy(tx, "timetable_markers",
			[]string{"station_name", "marker_type", "latitude", "longitude", "platform_length"},
			[]string{"timetable_id"}, []int64{newTTID},
			"timetable_id", t.id); err != nil {
			return 0, fmt.Errorf("timetable_markers: %w", err)
		}
		// timetable_sections
		if err := copyWithRemap(tx, "timetable_sections",
			nil,
			map[string]int64{"timetable_id": newTTID},
			map[string]map[int64]int64{"section_id": secMap},
			"timetable_id", t.id); err != nil {
			return 0, fmt.Errorf("timetable_sections: %w", err)
		}
		// timetable_formations
		if err := copyWithRemapFormation(tx, newTTID, t.id, fmtMap, classMap); err != nil {
			return 0, fmt.Errorf("timetable_formations: %w", err)
		}
		// timetable_map_features
		if err := simpleCopy(tx, "timetable_map_features",
			[]string{"features", "built_at"},
			[]string{"timetable_id"}, []int64{newTTID},
			"timetable_id", t.id); err != nil {
			return 0, fmt.Errorf("timetable_map_features: %w", err)
		}
	}

	// 11. route_formations
	rfRows, err := tx.Query(`SELECT formation_id FROM src.route_formations WHERE route_id = ?`, r.ID)
	if err != nil {
		return 0, fmt.Errorf("list route_formations: %w", err)
	}
	for rfRows.Next() {
		var fid int64
		rfRows.Scan(&fid)
		nfid, err := ensureFormation(tx, fid, fmtMap, classMap)
		if err != nil {
			rfRows.Close()
			return 0, fmt.Errorf("ensure formation for route_formations: %w", err)
		}
		tx.Exec(`INSERT INTO main.route_formations (route_id, formation_id) VALUES (?, ?)`, newRouteID, nfid)
	}
	rfRows.Close()

	// 12. section_formations — only the ones whose section_id was migrated above
	for srcSecID, newSecID := range secMap {
		sfRows, _ := tx.Query(`SELECT formation_id FROM src.section_formations WHERE section_id = ?`, srcSecID)
		for sfRows.Next() {
			var fid int64
			sfRows.Scan(&fid)
			nfid, err := ensureFormation(tx, fid, fmtMap, classMap)
			if err != nil {
				sfRows.Close()
				return 0, fmt.Errorf("ensure formation for section_formations: %w", err)
			}
			tx.Exec(`INSERT INTO main.section_formations (section_id, formation_id) VALUES (?, ?)`, newSecID, nfid)
		}
		sfRows.Close()
	}

	return newRouteID, nil
}

// ensureFormation: returns the main.formations.id for a given src formation_id,
// inserting the formation (and its train_class) if not yet migrated. Caches in fmtMap.
func ensureFormation(tx *sql.Tx, srcFmtID int64, fmtMap map[int64]int64, classMap map[int64]int64) (int64, error) {
	if v, ok := fmtMap[srcFmtID]; ok {
		return v, nil
	}
	var (
		name          sql.NullString
		className     sql.NullString
		liveryID      sql.NullString
		lengthM       sql.NullFloat64
		carCount      sql.NullInt64
		classID       sql.NullInt64
	)
	err := tx.QueryRow(`SELECT name, class_name, livery_id, length_m, car_count, class_id FROM src.formations WHERE id = ?`, srcFmtID).Scan(&name, &className, &liveryID, &lengthM, &carCount, &classID)
	if err == sql.ErrNoRows {
		return 0, fmt.Errorf("src formation %d not found", srcFmtID)
	}
	if err != nil {
		return 0, err
	}

	var newClassID any
	if classID.Valid {
		nc, err := ensureTrainClass(tx, classID.Int64, classMap)
		if err != nil {
			return 0, fmt.Errorf("ensure class %d: %w", classID.Int64, err)
		}
		newClassID = nc
	}
	res, err := tx.Exec(`INSERT INTO main.formations (name, class_name, livery_id, length_m, car_count, class_id) VALUES (?,?,?,?,?,?)`,
		nullStrAny(name), nullStrAny(className), nullStrAny(liveryID), nullFloatAny(lengthM), nullIntAny(carCount), newClassID)
	if err != nil {
		return 0, fmt.Errorf("insert formation: %w", err)
	}
	newID, _ := res.LastInsertId()
	fmtMap[srcFmtID] = newID

	// Copy formation_vehicles for this formation
	vrows, _ := tx.Query(`SELECT position, vehicle_id, class_name, friendly_name, livery_id, vehicle_category, length_m, is_lead, is_flipped FROM src.formation_vehicles WHERE formation_id = ?`, srcFmtID)
	for vrows.Next() {
		var pos sql.NullInt64
		var vid, cn, fn, liv, cat sql.NullString
		var lm sql.NullFloat64
		var lead, flip sql.NullInt64
		vrows.Scan(&pos, &vid, &cn, &fn, &liv, &cat, &lm, &lead, &flip)
		tx.Exec(`INSERT INTO main.formation_vehicles (formation_id, position, vehicle_id, class_name, friendly_name, livery_id, vehicle_category, length_m, is_lead, is_flipped) VALUES (?,?,?,?,?,?,?,?,?,?)`,
			newID, nullIntAny(pos), nullStrAny(vid), nullStrAny(cn), nullStrAny(fn), nullStrAny(liv), nullStrAny(cat), nullFloatAny(lm), nullIntAny(lead), nullIntAny(flip))
	}
	vrows.Close()
	return newID, nil
}

// ensureTrainClass: matches src class against main by name. If exists, reuse.
// Otherwise insert new. Schema diff: src has in_game_name (dropped); main has
// rail_vehicle_class / is_drivable / powered_axle_count / created_at (left NULL
// for migrated classes — Rebuild Train Classes can backfill later).
func ensureTrainClass(tx *sql.Tx, srcClassID int64, classMap map[int64]int64) (int64, error) {
	if v, ok := classMap[srcClassID]; ok {
		return v, nil
	}
	var (
		name        sql.NullString
		liveryID    sql.NullString
		typLen      sql.NullFloat64
		typCar      sql.NullInt64
		isElectric  sql.NullInt64
		maxSpeed    sql.NullFloat64
		maxPower    sql.NullFloat64
		manufact    sql.NullString
		engineDesc  sql.NullString
		typeDesc    sql.NullString
		thumb       sql.NullString
		category    sql.NullString
	)
	err := tx.QueryRow(`SELECT name, livery_id, typical_length_m, typical_car_count, is_electric, max_speed_kph, max_power_kw, manufacturer_name, engine_description, type_description, thumbnail_path, vehicle_category FROM src.train_classes WHERE id = ?`, srcClassID).Scan(&name, &liveryID, &typLen, &typCar, &isElectric, &maxSpeed, &maxPower, &manufact, &engineDesc, &typeDesc, &thumb, &category)
	if err != nil {
		return 0, err
	}
	// Match by name
	if name.Valid && name.String != "" {
		var existing int64
		row := tx.QueryRow(`SELECT id FROM main.train_classes WHERE name = ? LIMIT 1`, name.String)
		if err := row.Scan(&existing); err == nil {
			classMap[srcClassID] = existing
			return existing, nil
		}
	}
	res, err := tx.Exec(`INSERT INTO main.train_classes (name, livery_id, typical_length_m, typical_car_count, is_electric, max_speed_kph, max_power_kw, manufacturer_name, engine_description, type_description, thumbnail_path, vehicle_category) VALUES (?,?,?,?,?,?,?,?,?,?,?,?)`,
		nullStrAny(name), nullStrAny(liveryID), nullFloatAny(typLen), nullIntAny(typCar), nullIntAny(isElectric), nullFloatAny(maxSpeed), nullFloatAny(maxPower), nullStrAny(manufact), nullStrAny(engineDesc), nullStrAny(typeDesc), nullStrAny(thumb), nullStrAny(category))
	if err != nil {
		return 0, fmt.Errorf("insert train_class: %w", err)
	}
	newID, _ := res.LastInsertId()
	classMap[srcClassID] = newID

	// Copy electrification rows
	erows, _ := tx.Query(`SELECT current, pickup_side, voltage_v, frequency_hz FROM src.train_class_electrification WHERE train_class_id = ?`, srcClassID)
	for erows.Next() {
		var cur, pks sql.NullString
		var vv, fz sql.NullInt64
		erows.Scan(&cur, &pks, &vv, &fz)
		tx.Exec(`INSERT INTO main.train_class_electrification (train_class_id, current, pickup_side, voltage_v, frequency_hz) VALUES (?,?,?,?,?)`,
			newID, nullStrAny(cur), nullStrAny(pks), nullIntAny(vv), nullIntAny(fz))
	}
	erows.Close()
	return newID, nil
}

// remapInsert: for each src row matching (whereCol = whereVal), INSERT into
// main.<table> using fixed columns (passColumns are passed through as-is from
// src), with extraFixedCols set to a single value. Captures src.id → main.id
// in idMap.
//
// passColumns lists the COLUMNS to copy from src (besides id and the where col).
// extraFixedCols + extraVals: extra columns to inject with fixed values (typically the new FK like route_id=newRouteID).
// idMap: filled with src.id → main.id mappings.
// preRow: optional callback to inspect/modify row data before insert.
func remapInsert(tx *sql.Tx, table string, passCols []string, extraFixedCols []string, extraVals []int64, whereCol string, whereVal int64, idMap map[int64]int64, preRow func(srcID int64, vals []any) error) error {
	selectCols := append([]string{"id"}, passCols...)
	sel := joinCols(selectCols)
	rows, err := tx.Query(fmt.Sprintf(`SELECT %s FROM src.%s WHERE %s = ?`, sel, table, whereCol), whereVal)
	if err != nil {
		return err
	}
	defer rows.Close()

	insertCols := append([]string{}, passCols...)
	insertCols = append(insertCols, extraFixedCols...)
	insertSQL := fmt.Sprintf(`INSERT INTO main.%s (%s) VALUES (%s)`, table, joinCols(insertCols), placeholders(len(insertCols)))

	for rows.Next() {
		vals := make([]any, len(selectCols))
		ptrs := make([]any, len(selectCols))
		for i := range vals {
			ptrs[i] = &vals[i]
		}
		if err := rows.Scan(ptrs...); err != nil {
			return err
		}
		srcID := vals[0].(int64)
		passVals := vals[1:]
		if preRow != nil {
			if err := preRow(srcID, passVals); err != nil {
				return err
			}
		}
		args := append([]any{}, passVals...)
		for _, v := range extraVals {
			args = append(args, v)
		}
		res, err := tx.Exec(insertSQL, args...)
		if err != nil {
			return err
		}
		newID, _ := res.LastInsertId()
		idMap[srcID] = newID
	}
	return nil
}

// simpleCopy: no PK remap. INSERTs each src row with a fixed extra column
// (e.g. new route_id). passCols are the columns to copy verbatim from src.
func simpleCopy(tx *sql.Tx, table string, passCols []string, extraFixedCols []string, extraVals []int64, whereCol string, whereVal int64) error {
	rows, err := tx.Query(fmt.Sprintf(`SELECT %s FROM src.%s WHERE %s = ?`, joinCols(passCols), table, whereCol), whereVal)
	if err != nil {
		return err
	}
	defer rows.Close()
	insertCols := append([]string{}, passCols...)
	insertCols = append(insertCols, extraFixedCols...)
	insertSQL := fmt.Sprintf(`INSERT INTO main.%s (%s) VALUES (%s)`, table, joinCols(insertCols), placeholders(len(insertCols)))
	for rows.Next() {
		vals := make([]any, len(passCols))
		ptrs := make([]any, len(passCols))
		for i := range vals {
			ptrs[i] = &vals[i]
		}
		if err := rows.Scan(ptrs...); err != nil {
			return err
		}
		args := append([]any{}, vals...)
		for _, v := range extraVals {
			args = append(args, v)
		}
		if _, err := tx.Exec(insertSQL, args...); err != nil {
			return err
		}
	}
	return nil
}

// copyWithRemap: pass passCols verbatim, set fixedCols to given values, and
// remap each column in remaps (col → idMap) before insert. Drops rows whose
// mapped id is missing from any required map (warns but continues).
func copyWithRemap(tx *sql.Tx, table string, passCols []string, fixed map[string]int64, remaps map[string]map[int64]int64, whereCol string, whereVal int64) error {
	// Build select cols: pass + remap cols (in deterministic order)
	remapCols := []string{}
	for k := range remaps {
		remapCols = append(remapCols, k)
	}
	selectCols := append([]string{}, passCols...)
	selectCols = append(selectCols, remapCols...)
	rows, err := tx.Query(fmt.Sprintf(`SELECT %s FROM src.%s WHERE %s = ?`, joinCols(selectCols), table, whereCol), whereVal)
	if err != nil {
		return err
	}
	defer rows.Close()

	insertCols := append([]string{}, selectCols...)
	for k := range fixed {
		insertCols = append(insertCols, k)
	}
	insertSQL := fmt.Sprintf(`INSERT INTO main.%s (%s) VALUES (%s)`, table, joinCols(insertCols), placeholders(len(insertCols)))

	for rows.Next() {
		vals := make([]any, len(selectCols))
		ptrs := make([]any, len(selectCols))
		for i := range vals {
			ptrs[i] = &vals[i]
		}
		if err := rows.Scan(ptrs...); err != nil {
			return err
		}
		// Remap the remap-cols (last len(remapCols) of selectCols)
		offset := len(passCols)
		for i, col := range remapCols {
			v := vals[offset+i]
			if v == nil {
				continue
			}
			srcID, ok := v.(int64)
			if !ok {
				continue
			}
			if newID, ok := remaps[col][srcID]; ok {
				vals[offset+i] = newID
			} else {
				// Missing mapping — write NULL rather than dangling FK
				vals[offset+i] = nil
			}
		}
		// Append fixed
		args := append([]any{}, vals...)
		for _, k := range orderedKeys(fixed) {
			args = append(args, fixed[k])
		}
		// Rebuild insertCols in same order as args
		insertColsOrdered := append([]string{}, selectCols...)
		insertColsOrdered = append(insertColsOrdered, orderedKeys(fixed)...)
		_ = insertColsOrdered
		if _, err := tx.Exec(insertSQL, args...); err != nil {
			return err
		}
	}
	return nil
}

// copyWithRemapFormation copies src.timetable_formations rows for one
// timetable_id, ensuring each referenced formation is migrated first.
func copyWithRemapFormation(tx *sql.Tx, newTTID int64, srcTTID int64, fmtMap map[int64]int64, classMap map[int64]int64) error {
	rows, err := tx.Query(`SELECT formation_id FROM src.timetable_formations WHERE timetable_id = ?`, srcTTID)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var fid int64
		rows.Scan(&fid)
		nfid, err := ensureFormation(tx, fid, fmtMap, classMap)
		if err != nil {
			return err
		}
		tx.Exec(`INSERT INTO main.timetable_formations (timetable_id, formation_id) VALUES (?, ?)`, newTTID, nfid)
	}
	return nil
}

// --- helpers ---

func nullStrAny(s sql.NullString) any {
	if !s.Valid {
		return nil
	}
	return s.String
}
func nullIntAny(i sql.NullInt64) any {
	if !i.Valid {
		return nil
	}
	return i.Int64
}
func nullFloatAny(f sql.NullFloat64) any {
	if !f.Valid {
		return nil
	}
	return f.Float64
}

func joinCols(cols []string) string {
	out := ""
	for i, c := range cols {
		if i > 0 {
			out += ", "
		}
		out += c
	}
	return out
}
func placeholders(n int) string {
	out := ""
	for i := 0; i < n; i++ {
		if i > 0 {
			out += ","
		}
		out += "?"
	}
	return out
}
func orderedKeys(m map[string]int64) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	defer out.Close()
	_, err = io.Copy(out, in)
	return err
}
