package database

import (
	"database/sql"
	"fmt"
	"strings"
)

var migrations = []string{
	// countries
	`CREATE TABLE IF NOT EXISTS countries (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		name TEXT NOT NULL UNIQUE,
		code TEXT
	)`,

	// routes
	`CREATE TABLE IF NOT EXISTS routes (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		name TEXT NOT NULL UNIQUE,
		country_id INTEGER NOT NULL,
		tsw_version INTEGER NOT NULL DEFAULT 3,
		FOREIGN KEY (country_id) REFERENCES countries(id) ON DELETE RESTRICT
	)`,

	// trains
	`CREATE TABLE IF NOT EXISTS trains (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		name TEXT NOT NULL UNIQUE
	)`,

	// route_trains
	`CREATE TABLE IF NOT EXISTS route_trains (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		route_id INTEGER NOT NULL,
		train_id INTEGER NOT NULL,
		FOREIGN KEY (route_id) REFERENCES routes(id) ON DELETE CASCADE,
		FOREIGN KEY (train_id) REFERENCES trains(id) ON DELETE CASCADE,
		UNIQUE(route_id, train_id)
	)`,

	// timetables
	`CREATE TABLE IF NOT EXISTS timetables (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		service_name TEXT NOT NULL,
		route_id INTEGER,
		train_id INTEGER,
		service_type TEXT NOT NULL DEFAULT 'passenger',
		contributor TEXT,
		coordinates_contributor TEXT,
		created_at TEXT DEFAULT CURRENT_TIMESTAMP,
		tonnage REAL,
		car_count INTEGER,
		train_length REAL,
		start_time TEXT,
		duration TEXT,
		service_images TEXT,
		section_id INTEGER,
		conductor_compatible INTEGER DEFAULT 0,
		bound TEXT,
		service TEXT,
		current_service_name TEXT,
		FOREIGN KEY (route_id) REFERENCES routes(id) ON DELETE SET NULL,
		FOREIGN KEY (train_id) REFERENCES trains(id) ON DELETE SET NULL
	)`,
	`CREATE UNIQUE INDEX IF NOT EXISTS uq_timetable_service_route ON timetables(service_name, route_id)`,

	// sections
	`CREATE TABLE IF NOT EXISTS sections (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		route_id INTEGER NOT NULL,
		name TEXT NOT NULL,
		FOREIGN KEY (route_id) REFERENCES routes(id) ON DELETE CASCADE,
		UNIQUE(route_id, name)
	)`,

	// section_trains
	`CREATE TABLE IF NOT EXISTS section_trains (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		section_id INTEGER NOT NULL,
		train_id INTEGER NOT NULL,
		FOREIGN KEY (section_id) REFERENCES sections(id) ON DELETE CASCADE,
		FOREIGN KEY (train_id) REFERENCES trains(id) ON DELETE CASCADE,
		UNIQUE(section_id, train_id)
	)`,

	// timetable_trains
	`CREATE TABLE IF NOT EXISTS timetable_trains (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		timetable_id INTEGER NOT NULL,
		train_id INTEGER NOT NULL,
		FOREIGN KEY (timetable_id) REFERENCES timetables(id) ON DELETE CASCADE,
		FOREIGN KEY (train_id) REFERENCES trains(id) ON DELETE CASCADE,
		UNIQUE(timetable_id, train_id)
	)`,

	// timetable_sections
	`CREATE TABLE IF NOT EXISTS timetable_sections (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		timetable_id INTEGER NOT NULL,
		section_id INTEGER NOT NULL,
		FOREIGN KEY (timetable_id) REFERENCES timetables(id) ON DELETE CASCADE,
		FOREIGN KEY (section_id) REFERENCES sections(id) ON DELETE CASCADE,
		UNIQUE(timetable_id, section_id)
	)`,

	// timetable_actions
	`CREATE TABLE IF NOT EXISTS timetable_actions (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		name TEXT NOT NULL UNIQUE
	)`,

	// locations
	`CREATE TABLE IF NOT EXISTS locations (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		route_id INTEGER NOT NULL,
		name TEXT NOT NULL,
		FOREIGN KEY (route_id) REFERENCES routes(id) ON DELETE CASCADE,
		UNIQUE(route_id, name)
	)`,
	`CREATE INDEX IF NOT EXISTS idx_locations_route_id ON locations(route_id)`,

	// timetable_entries
	`CREATE TABLE IF NOT EXISTS timetable_entries (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		timetable_id INTEGER NOT NULL,
		action_id INTEGER REFERENCES timetable_actions(id),
		details TEXT,
		location_id INTEGER REFERENCES locations(id),
		structure_number TEXT,
		structure TEXT,
		time1 TEXT,
		time2 TEXT,
		latitude TEXT,
		longitude TEXT,
		tile_x INTEGER,
		tile_y INTEGER,
		api_name TEXT,
		sort_order INTEGER,
		coord_source TEXT,
		FOREIGN KEY (timetable_id) REFERENCES timetables(id) ON DELETE CASCADE
	)`,
	// Add structure column for existing databases
	`ALTER TABLE timetable_entries ADD COLUMN structure TEXT`,
	`ALTER TABLE timetable_entries RENAME COLUMN platform TO structure_number`,
	`ALTER TABLE timetable_entries ADD COLUMN tile_x INTEGER`,
	`ALTER TABLE timetable_entries ADD COLUMN tile_y INTEGER`,
	`CREATE INDEX IF NOT EXISTS idx_timetable_entries_timetable_id ON timetable_entries(timetable_id)`,

	// station_name_mappings
	`CREATE TABLE IF NOT EXISTS station_name_mappings (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		route_id INTEGER,
		display_name TEXT NOT NULL,
		api_name TEXT NOT NULL,
		created_at TEXT DEFAULT CURRENT_TIMESTAMP,
		FOREIGN KEY (route_id) REFERENCES routes(id) ON DELETE CASCADE,
		UNIQUE(route_id, display_name)
	)`,

	// weather_presets
	`CREATE TABLE IF NOT EXISTS weather_presets (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		name TEXT NOT NULL UNIQUE,
		temperature REAL NOT NULL DEFAULT 20,
		cloudiness REAL NOT NULL DEFAULT 0,
		precipitation REAL NOT NULL DEFAULT 0,
		wetness REAL NOT NULL DEFAULT 0,
		ground_snow REAL NOT NULL DEFAULT 0,
		piled_snow REAL NOT NULL DEFAULT 0,
		fog_density REAL NOT NULL DEFAULT 0,
		created_at TEXT DEFAULT CURRENT_TIMESTAMP
	)`,

	// timetable_coordinates
	`CREATE TABLE IF NOT EXISTS timetable_coordinates (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		timetable_id INTEGER NOT NULL UNIQUE,
		coordinates TEXT NOT NULL,
		coord_source TEXT,
		FOREIGN KEY (timetable_id) REFERENCES timetables(id) ON DELETE CASCADE
	)`,
	`CREATE INDEX IF NOT EXISTS idx_timetable_coordinates_timetable_id ON timetable_coordinates(timetable_id)`,

	// timetable_markers
	`CREATE TABLE IF NOT EXISTS timetable_markers (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		timetable_id INTEGER NOT NULL,
		station_name TEXT NOT NULL,
		marker_type TEXT,
		latitude REAL,
		longitude REAL,
		platform_length REAL,
		FOREIGN KEY (timetable_id) REFERENCES timetables(id) ON DELETE CASCADE
	)`,
	`CREATE INDEX IF NOT EXISTS idx_timetable_markers_timetable_id ON timetable_markers(timetable_id)`,

	// route_coordinates
	`CREATE TABLE IF NOT EXISTS route_coordinates (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		route_id INTEGER NOT NULL UNIQUE,
		coordinates TEXT NOT NULL,
		updated_at TEXT DEFAULT CURRENT_TIMESTAMP,
		FOREIGN KEY (route_id) REFERENCES routes(id) ON DELETE CASCADE
	)`,
	`CREATE INDEX IF NOT EXISTS idx_route_coordinates_route_id ON route_coordinates(route_id)`,

	// route_markers
	`CREATE TABLE IF NOT EXISTS route_markers (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		route_id INTEGER NOT NULL,
		station_name TEXT NOT NULL,
		marker_type TEXT,
		latitude REAL,
		longitude REAL,
		platform_length REAL,
		FOREIGN KEY (route_id) REFERENCES routes(id) ON DELETE CASCADE,
		UNIQUE(route_id, station_name, marker_type)
	)`,
	`CREATE INDEX IF NOT EXISTS idx_route_markers_route_id ON route_markers(route_id)`,

	// train_consists
	`CREATE TABLE IF NOT EXISTS train_consists (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		timetable_id INTEGER NOT NULL,
		train_id INTEGER NOT NULL,
		weight REAL,
		car_count INTEGER,
		train_length REAL,
		train_number INTEGER,
		latitude REAL,
		longitude REAL,
		created_at TEXT DEFAULT CURRENT_TIMESTAMP,
		FOREIGN KEY (timetable_id) REFERENCES timetables(id) ON DELETE CASCADE,
		FOREIGN KEY (train_id) REFERENCES trains(id) ON DELETE CASCADE,
		UNIQUE(timetable_id, train_id, weight, car_count, train_length)
	)`,
	`CREATE INDEX IF NOT EXISTS idx_train_consists_timetable_id ON train_consists(timetable_id)`,

	// route_locations
	`CREATE TABLE IF NOT EXISTS route_locations (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		route_id INTEGER NOT NULL,
		location_id INTEGER,
		name TEXT NOT NULL,
		bound TEXT,
		platform TEXT,
		latitude REAL,
		longitude REAL,
		FOREIGN KEY (route_id) REFERENCES routes(id) ON DELETE CASCADE,
		FOREIGN KEY (location_id) REFERENCES locations(id) ON DELETE SET NULL,
		UNIQUE(route_id, name, bound, platform)
	)`,
	`CREATE INDEX IF NOT EXISTS idx_route_locations_route_id ON route_locations(route_id)`,

	// ---------------------------------------------------------------------
	// 2026-04-25 schema rework for the new TSW6 extractor output:
	//
	// Per-train consist data now lives on the trains table + a new
	// train_vehicles table, rather than denormalised into per-timetable
	// columns. timetables.tonnage / car_count / train_length are dropped
	// because TSW6 doesn't expose tonnage and the rest can be derived from
	// the linked train. train_consists is dropped as it overlapped with
	// the new train_vehicles structure.
	//
	// trains.name is the canonical formation_name (e.g. "Class483_006").
	// Per-service JSONs link via their formation_name field; the upload
	// pipeline dedupes any aliases by vehicle-GUID set.
	// ---------------------------------------------------------------------
	`ALTER TABLE trains ADD COLUMN class_name TEXT`,
	`ALTER TABLE trains ADD COLUMN livery_id TEXT`,
	`ALTER TABLE trains ADD COLUMN length_m REAL`,
	`ALTER TABLE trains ADD COLUMN car_count INTEGER`,

	// train_vehicles: one row per car in a train, in formation order.
	// vehicle_id is the GUID exposed by the TSW6 live API (CurrentFormation
	// /<i>.VehicleID, no dashes), used to recognise the running train.
	`CREATE TABLE IF NOT EXISTS train_vehicles (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		train_id INTEGER NOT NULL,
		position INTEGER NOT NULL,
		vehicle_id TEXT NOT NULL,
		class_name TEXT,
		friendly_name TEXT,
		livery_id TEXT,
		vehicle_category TEXT,
		length_m REAL,
		is_lead INTEGER DEFAULT 0,
		is_flipped INTEGER DEFAULT 0,
		FOREIGN KEY (train_id) REFERENCES trains(id) ON DELETE CASCADE,
		UNIQUE(train_id, position)
	)`,
	`CREATE INDEX IF NOT EXISTS idx_train_vehicles_train_id ON train_vehicles(train_id)`,
	`CREATE INDEX IF NOT EXISTS idx_train_vehicles_vehicle_id ON train_vehicles(vehicle_id)`,

	// Drop legacy per-timetable train spec columns. Requires SQLite 3.35+;
	// older versions will silently leave the columns (they just won't be
	// written to from the new code).
	`ALTER TABLE timetables DROP COLUMN tonnage`,
	`ALTER TABLE timetables DROP COLUMN car_count`,
	`ALTER TABLE timetables DROP COLUMN train_length`,

	// Replaced by train_vehicles + the existing timetables.train_id FK.
	`DROP TABLE IF EXISTS train_consists`,

	// ---------------------------------------------------------------------
	// 2026-04-26 train hierarchy: train_classes groups physical trains
	// (formations) by their TSW class. Two Class 483 sets on Isle of Wight
	// share the same class but have different vehicle GUIDs — searches and
	// listings should key on the class, while runtime VehicleID matching
	// stays on trains.id.
	//
	// trains.class_name is kept as a denormalised cache so existing
	// endpoints/templates that already read it don't need rewrites.
	// ---------------------------------------------------------------------
	`CREATE TABLE IF NOT EXISTS train_classes (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		name TEXT NOT NULL UNIQUE,
		livery_id TEXT,
		typical_length_m REAL,
		typical_car_count INTEGER,
		created_at TEXT DEFAULT CURRENT_TIMESTAMP
	)`,
	// Patch up older partial train_classes tables created before this revision
	// added the extra columns. ALTER failures are swallowed when the column
	// already exists, so these are safe to leave in place.
	`ALTER TABLE train_classes ADD COLUMN livery_id TEXT`,
	`ALTER TABLE train_classes ADD COLUMN typical_length_m REAL`,
	`ALTER TABLE train_classes ADD COLUMN typical_car_count INTEGER`,
	`ALTER TABLE train_classes ADD COLUMN created_at TEXT DEFAULT CURRENT_TIMESTAMP`,
	`ALTER TABLE trains ADD COLUMN class_id INTEGER REFERENCES train_classes(id)`,

	// cross_pak_reference_name is TSW's internal asset-mount path for a
	// route (e.g. "EustonMiltonKeynes" for WCML South). Cargo / scenario
	// DLCs reference parent routes via this name in their timetable
	// NameMaps; the importer matches by it before falling back to display
	// name. Populated lazily by ImportRouteZip.
	`ALTER TABLE routes ADD COLUMN cross_pak_reference_name TEXT`,
	`CREATE INDEX IF NOT EXISTS idx_routes_cross_pak_reference_name ON routes(cross_pak_reference_name)`,

	// pak_catalog: one row per pak file under the user's TSW6 install,
	// recording the metadata the /extractor list needs (display name,
	// country, parent route, cross-pak references) so the page renders
	// instantly instead of warming up by walking ~36 paks every load.
	//
	// Populated by `internal/catalog.ScanCatalog` on first /extractor
	// visit and refreshed on demand via POST /api/extractor/rescan.
	// Diffed by pak_mtime + pak_size — paks that haven't changed are
	// skipped on re-scan.
	//
	//   pak_path                  absolute path on disk; primary key
	//   codename                  filename-derived name ("WCMLSouth")
	//   display_name              from RouteDefinition.DisplayName, or
	//                             trimmed *_Gameplay.uplugin Description
	//   country_code              from RouteDetails.Country ("UK","US"…)
	//   has_route_definition      1 if pak ships its own RouteDefinition
	//                             (i.e. it's a parent route, not addon)
	//   cross_pak_references      JSON array of cross_pak_reference_names
	//                             discovered in this pak's timetables /
	//                             scenario definitions; multi-valued for
	//                             cargo DLCs that span several routes
	//   pak_mtime, pak_size       for invalidation
	//   scanned_at                Unix epoch seconds of last scan
	`CREATE TABLE IF NOT EXISTS pak_catalog (
		pak_path TEXT PRIMARY KEY,
		codename TEXT NOT NULL,
		display_name TEXT,
		country_code TEXT,
		has_route_definition INTEGER NOT NULL DEFAULT 0,
		cross_pak_references TEXT,
		pak_mtime INTEGER NOT NULL,
		pak_size INTEGER NOT NULL,
		scanned_at INTEGER NOT NULL
	)`,
	// Each pak's own mount (e.g. "EustonMiltonKeynes" for WCML South).
	// Set only on parent paks (has_route_definition=1); used by the tree
	// builder to attach addon children whose cross_pak_references list
	// includes this string. ALTER variant for DBs created before the
	// column existed.
	`ALTER TABLE pak_catalog ADD COLUMN cross_pak_reference_name TEXT`,
	`CREATE INDEX IF NOT EXISTS idx_pak_catalog_has_route_def ON pak_catalog(has_route_definition)`,
	`CREATE INDEX IF NOT EXISTS idx_pak_catalog_codename ON pak_catalog(codename)`,
	`CREATE INDEX IF NOT EXISTS idx_pak_catalog_cross_pak_reference_name ON pak_catalog(cross_pak_reference_name)`,

	// pak_rvds: every RailVehicleDefinition asset across the user's
	// installed paks, recorded once at catalog scan time so the
	// extractor doesn't re-walk the install directory looking for
	// RVDs on every route extraction (saved ~1–2 min per route on
	// a typical install).
	//
	//   pak_path                FK to pak_catalog; cascade-deletes when
	//                           the pak vanishes from disk
	//   asset_path              canonical reference path used by
	//                           CompiledRVMap entries inside timetables
	//                           (e.g. "/LIRREX_M7/Data/RailVehicleDefinition/RVD_LIRREX_M7-A")
	//   rail_vehicle_class…     fields mirror uasset.RVD; see ParseRVD
	//                           for what each one means
	//   regions                 JSON array (the asset stores a list)
	`CREATE TABLE IF NOT EXISTS pak_rvds (
		pak_path TEXT NOT NULL,
		asset_path TEXT NOT NULL,
		rail_vehicle_class TEXT,
		friendly_name TEXT,
		livery_id TEXT,
		vehicle_category TEXT,
		approximate_length_m REAL,
		drivable INTEGER NOT NULL DEFAULT 0,
		substitutable_unit INTEGER NOT NULL DEFAULT 0,
		has_guard_controls INTEGER NOT NULL DEFAULT 0,
		service_types INTEGER,
		regions TEXT,
		PRIMARY KEY (pak_path, asset_path),
		FOREIGN KEY (pak_path) REFERENCES pak_catalog(pak_path) ON DELETE CASCADE
	)`,
	`CREATE INDEX IF NOT EXISTS idx_pak_rvds_class ON pak_rvds(rail_vehicle_class)`,
	`CREATE INDEX IF NOT EXISTS idx_pak_rvds_livery ON pak_rvds(livery_id)`,
	`CREATE INDEX IF NOT EXISTS idx_pak_rvds_vehicle_category ON pak_rvds(vehicle_category)`,
	`CREATE INDEX IF NOT EXISTS idx_pak_rvds_friendly_name ON pak_rvds(friendly_name)`,

	// Backfill: one class row per distinct (class_name, livery_id) on
	// trains that have a class_name set. The IGNORE clause keeps the
	// migration idempotent across restarts.
	`INSERT OR IGNORE INTO train_classes (name, livery_id, typical_length_m, typical_car_count)
	 SELECT DISTINCT class_name, livery_id, length_m, car_count
	 FROM trains
	 WHERE class_name IS NOT NULL AND class_name != ''`,

	// Link existing trains to their class. (Trains created post-migration
	// are linked at insert time by the import handler.)
	`UPDATE trains SET class_id = (
		SELECT id FROM train_classes WHERE train_classes.name = trains.class_name
	) WHERE class_id IS NULL AND class_name IS NOT NULL AND class_name != ''`,

	`CREATE INDEX IF NOT EXISTS idx_trains_class_id ON trains(class_id)`,

	// ---------------------------------------------------------------------
	// 2026-04-26 timetable metadata: capture `source` (e.g. "Timetable",
	// "AICustom") and `playable` (boolean) from the per-service JSON so the
	// listings/search UIs can filter by them.
	// ---------------------------------------------------------------------
	`ALTER TABLE timetables ADD COLUMN source TEXT`,
	`ALTER TABLE timetables ADD COLUMN playable INTEGER DEFAULT 0`,
	`CREATE INDEX IF NOT EXISTS idx_timetables_source ON timetables(source)`,
	`CREATE INDEX IF NOT EXISTS idx_timetables_playable ON timetables(playable)`,

	// ---------------------------------------------------------------------
	// 2026-04-26 cab stop signs (the in-game "Car Stop" / green ring) and
	// pak track markers (TrackMarkerProperty: platform names + junction
	// routing markers like "Smallbrook Junction Line 1").
	//
	// Distinct from the existing route_markers table (which holds player-
	// recorded marker data with one row per (route, station, type)). Pak-
	// derived markers can have multiple rows per (route, name) — one per
	// ribbon they appear on (chained platform geometry).
	//
	// cab_stop_signs.platform_name is denormalised from track_markers at
	// import time (joined on ribbon_guid + marker_type='Platform') so the
	// HUD-side lookup can hit a single index without a join.
	// ---------------------------------------------------------------------
	`CREATE TABLE IF NOT EXISTS cab_stop_signs (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		route_id INTEGER NOT NULL,
		platform_name TEXT,
		ribbon_guid TEXT NOT NULL,
		location REAL NOT NULL,
		max_rail_vehicles INTEGER NOT NULL DEFAULT 0,
		latitude REAL NOT NULL,
		longitude REAL NOT NULL,
		FOREIGN KEY (route_id) REFERENCES routes(id) ON DELETE CASCADE
	)`,
	`CREATE INDEX IF NOT EXISTS idx_cab_stop_signs_lookup
		ON cab_stop_signs (route_id, platform_name, max_rail_vehicles)`,
	`CREATE INDEX IF NOT EXISTS idx_cab_stop_signs_ribbon
		ON cab_stop_signs (route_id, ribbon_guid)`,

	`CREATE TABLE IF NOT EXISTS track_markers (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		route_id INTEGER NOT NULL,
		name TEXT NOT NULL,
		marker_type TEXT,
		ribbon_guid TEXT NOT NULL,
		location REAL,
		start REAL,
		end REAL,
		line_side TEXT,
		latitude REAL NOT NULL,
		longitude REAL NOT NULL,
		FOREIGN KEY (route_id) REFERENCES routes(id) ON DELETE CASCADE
	)`,
	`CREATE INDEX IF NOT EXISTS idx_track_markers_lookup
		ON track_markers (route_id, name)`,
	`CREATE INDEX IF NOT EXISTS idx_track_markers_ribbon
		ON track_markers (route_id, ribbon_guid)`,

	// Pre-resolved cab-stop-sign per timetable_entries row (the single
	// CarStopSign that applies given the timetable's bound + train car
	// count). NULL when no match was found (e.g. depot/scenario services
	// with no platform context, or routes without pak-derived data).
	`ALTER TABLE timetable_entries ADD COLUMN cab_stop_sign_id INTEGER REFERENCES cab_stop_signs(id)`,
	`CREATE INDEX IF NOT EXISTS idx_timetable_entries_cab_stop_sign
		ON timetable_entries(cab_stop_sign_id)`,

	// Pre-resolved track-marker per timetable_entries row. Covers the
	// non-Platform routing markers (e.g. "Smallbrook Junction Line 1") that
	// drive the in-game "Go Via X" instructions. Distinct from
	// cab_stop_sign_id (which is Platform-only) — a single row will only
	// ever resolve to one or the other depending on its structure.
	`ALTER TABLE timetable_entries ADD COLUMN track_marker_id INTEGER REFERENCES track_markers(id)`,
	`CREATE INDEX IF NOT EXISTS idx_timetable_entries_track_marker
		ON timetable_entries(track_marker_id)`,

	// extractor_completed_routes: user-marked "I'm done with this route"
	// flags backing the Completed Routes list on /extractor. Survives
	// hud-go restarts and zip deletions — independent of whether the route
	// actually has a zip in the output dir.
	`CREATE TABLE IF NOT EXISTS extractor_completed_routes (
		codename TEXT PRIMARY KEY,
		completed_at TEXT DEFAULT CURRENT_TIMESTAMP
	)`,
}

// One-shot data backfills run exactly once per database. Tracked via
// SQLite's built-in `PRAGMA user_version` — bump the constant when adding a
// new step. Heuristic-based backfills don't belong in the idempotent
// `migrations` list because better data may arrive later (full re-imports
// from the new extractor) and we don't want the heuristic re-firing on it.
const currentSchemaVersion = 2

type oneShot struct {
	version int
	name    string
	run     func(*sql.DB) error
}

var oneShots = []oneShot{
	{
		version: 1,
		name:    "legacy timetables: backfill source + playable via heuristic",
		run: func(db *sql.DB) error {
			// Legacy rows (imported before the extractor learned about
			// source/playable) have source IS NULL and playable=0
			// (column default).
			//
			// AI heuristic: TSW6 AI traffic uses `_AI_` / `AI_` markers
			// and PORTALOUT / PORTALIN names for trains spawned from
			// map-edge portals. Player-driveable services never use
			// these patterns. Verified against ~25k legacy rows.
			//
			// Order matters: set everything to playable, then flip AI
			// matches back to 0, then consume the `source IS NULL`
			// marker.
			steps := []string{
				`UPDATE timetables SET playable = 1 WHERE source IS NULL`,
				`UPDATE timetables SET playable = 0
				 WHERE source IS NULL AND (
				     current_service_name LIKE '%\_AI\_%' ESCAPE '\' OR
				     current_service_name LIKE 'AI\_%' ESCAPE '\' OR
				     current_service_name LIKE '%PORTALOUT%' OR
				     current_service_name LIKE '%PORTALIN%' OR
				     service_name LIKE '%\_AI\_%' ESCAPE '\' OR
				     service_name LIKE 'AI\_%' ESCAPE '\' OR
				     service_name LIKE '%PORTALOUT%' OR
				     service_name LIKE '%PORTALIN%'
				 )`,
				`UPDATE timetables SET source = 'Timetable' WHERE source IS NULL OR source = ''`,
			}
			for _, s := range steps {
				if _, err := db.Exec(s); err != nil {
					return err
				}
			}
			return nil
		},
	},
	{
		version: 2,
		name:    "trains: drop UNIQUE constraint on name (formations can share generic names)",
		run: func(db *sql.DB) error {
			// SQLite has no DROP CONSTRAINT — rebuild the table without
			// the UNIQUE on `name`. Generic formation names like
			// "PlayerFormation" and "AIFormation" appear in scenarios
			// across paks (IoW PlayerFormation has Class 483 vehicles,
			// Boston PlayerFormation has CTC-3 vehicles). Two rows with
			// the same name but different vehicle GUIDs are genuinely
			// different physical formations and must coexist.
			//
			// The columns mirror the trains table after all schema
			// migrations have run (id, name, class_name, livery_id,
			// length_m, car_count, class_id). Foreign keys from
			// route_trains / timetable_trains / section_trains /
			// train_vehicles all reference trains.id which we preserve,
			// so cross-table relationships survive the rebuild.
			steps := []string{
				`CREATE TABLE trains_v2 (
					id INTEGER PRIMARY KEY AUTOINCREMENT,
					name TEXT NOT NULL,
					class_name TEXT,
					livery_id TEXT,
					length_m REAL,
					car_count INTEGER,
					class_id INTEGER REFERENCES train_classes(id)
				)`,
				`INSERT INTO trains_v2 (id, name, class_name, livery_id, length_m, car_count, class_id)
				 SELECT id, name, class_name, livery_id, length_m, car_count, class_id FROM trains`,
				`DROP TABLE trains`,
				`ALTER TABLE trains_v2 RENAME TO trains`,
				`CREATE INDEX IF NOT EXISTS idx_trains_class_id ON trains(class_id)`,
				`CREATE INDEX IF NOT EXISTS idx_trains_name ON trains(name)`,
			}
			for _, s := range steps {
				if _, err := db.Exec(s); err != nil {
					return err
				}
			}
			return nil
		},
	},
}

// RunMigrations executes all migration statements in order, then runs any
// one-shot data backfills whose version is newer than the DB's current
// `PRAGMA user_version`. Idempotent across restarts: schema migrations
// re-run safely, one-shots only run once per DB.
func RunMigrations(db *sql.DB) error {
	for i, stmt := range migrations {
		if _, err := db.Exec(stmt); err != nil {
			// ALTER TABLE may fail if column already exists — that's OK
			if strings.HasPrefix(strings.TrimSpace(stmt), "ALTER TABLE") {
				continue
			}
			return fmt.Errorf("migration %d failed: %w", i, err)
		}
	}

	// One-shot backfills.
	var ver int
	if err := db.QueryRow("PRAGMA user_version").Scan(&ver); err != nil {
		return fmt.Errorf("read user_version: %w", err)
	}
	for _, os := range oneShots {
		if ver >= os.version {
			continue
		}
		if err := os.run(db); err != nil {
			return fmt.Errorf("one-shot %d (%s) failed: %w", os.version, os.name, err)
		}
		// SQLite's PRAGMA user_version doesn't accept a parameter binding
		// — interpolate. Version values are constants, so this is safe.
		if _, err := db.Exec(fmt.Sprintf("PRAGMA user_version = %d", os.version)); err != nil {
			return fmt.Errorf("bump user_version to %d: %w", os.version, err)
		}
	}
	return nil
}
