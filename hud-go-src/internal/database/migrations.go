package database

import (
	"context"
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

	// ---------------------------------------------------------------------
	// 2026-05-10 rename trains hierarchy to formations.
	//
	// The `trains` table holds assembled formations (one row per arrangement
	// of vehicles, e.g. "Class483_006"). Class lives separately in
	// `train_classes`, and the per-car list lives in `train_vehicles`. The
	// table name "trains" caused user confusion with the live-API "train
	// you're driving" concept, so the entity is renamed to "formation"
	// across the schema, code, URLs, and UI. `train_classes` is left alone
	// because "train class" is the standard railway term.
	//
	// These ALTERs are idempotent via RunMigrations' failure tolerance: on
	// legacy DBs they perform the rename once; on fresh DBs (and on already-
	// renamed DBs) they fail with "no such table/column" and are skipped.
	// They run before every CREATE TABLE below that uses the new names, so
	// IF NOT EXISTS doesn't accidentally create empty duplicate tables next
	// to the populated originals.
	//
	// SQLite >=3.25 RENAME TO propagates the new parent name into the FK
	// definitions of referencing tables, so the FKs in route_formations /
	// section_formations / timetable_formations / formation_vehicles follow
	// `trains` to `formations` automatically.
	// ---------------------------------------------------------------------
	`ALTER TABLE trains RENAME TO formations`,
	`ALTER TABLE route_trains RENAME TO route_formations`,
	`ALTER TABLE route_formations RENAME COLUMN train_id TO formation_id`,
	`ALTER TABLE section_trains RENAME TO section_formations`,
	`ALTER TABLE section_formations RENAME COLUMN train_id TO formation_id`,
	`ALTER TABLE timetable_trains RENAME TO timetable_formations`,
	`ALTER TABLE timetable_formations RENAME COLUMN train_id TO formation_id`,
	`ALTER TABLE train_vehicles RENAME TO formation_vehicles`,
	`ALTER TABLE formation_vehicles RENAME COLUMN train_id TO formation_id`,
	`ALTER TABLE timetables RENAME COLUMN train_id TO formation_id`,
	// Indexes were created under the old `trains_…` / `train_vehicles_…`
	// names. They keep working after a table rename (SQLite preserves them),
	// but the names become misleading. Drop the old names; the new-named
	// CREATE INDEX statements further down in this slice rebuild them.
	`DROP INDEX IF EXISTS idx_trains_class_id`,
	`DROP INDEX IF EXISTS idx_trains_name`,
	`DROP INDEX IF EXISTS idx_train_vehicles_train_id`,
	`DROP INDEX IF EXISTS idx_train_vehicles_vehicle_id`,

	// routes
	`CREATE TABLE IF NOT EXISTS routes (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		name TEXT NOT NULL UNIQUE,
		country_id INTEGER NOT NULL,
		tsw_version INTEGER NOT NULL DEFAULT 3,
		FOREIGN KEY (country_id) REFERENCES countries(id) ON DELETE RESTRICT
	)`,

	// formations (was: trains; renamed 2026-05-10)
	`CREATE TABLE IF NOT EXISTS formations (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		name TEXT NOT NULL UNIQUE
	)`,

	// route_formations (was: route_trains)
	`CREATE TABLE IF NOT EXISTS route_formations (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		route_id INTEGER NOT NULL,
		formation_id INTEGER NOT NULL,
		FOREIGN KEY (route_id) REFERENCES routes(id) ON DELETE CASCADE,
		FOREIGN KEY (formation_id) REFERENCES formations(id) ON DELETE CASCADE,
		UNIQUE(route_id, formation_id)
	)`,

	// timetables (FK column train_id renamed to formation_id)
	`CREATE TABLE IF NOT EXISTS timetables (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		service_name TEXT NOT NULL,
		route_id INTEGER,
		formation_id INTEGER,
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
		FOREIGN KEY (formation_id) REFERENCES formations(id) ON DELETE SET NULL
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

	// section_formations (was: section_trains)
	`CREATE TABLE IF NOT EXISTS section_formations (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		section_id INTEGER NOT NULL,
		formation_id INTEGER NOT NULL,
		FOREIGN KEY (section_id) REFERENCES sections(id) ON DELETE CASCADE,
		FOREIGN KEY (formation_id) REFERENCES formations(id) ON DELETE CASCADE,
		UNIQUE(section_id, formation_id)
	)`,

	// timetable_formations (was: timetable_trains)
	`CREATE TABLE IF NOT EXISTS timetable_formations (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		timetable_id INTEGER NOT NULL,
		formation_id INTEGER NOT NULL,
		FOREIGN KEY (timetable_id) REFERENCES timetables(id) ON DELETE CASCADE,
		FOREIGN KEY (formation_id) REFERENCES formations(id) ON DELETE CASCADE,
		UNIQUE(timetable_id, formation_id)
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

	// ---------------------------------------------------------------------
	// Seed data: countries + weather_presets.
	//
	// Countries cover every locale TSW6 routes ship in (ISO 3166-1 alpha-2
	// codes — matches the catalog scan's country_code field and the
	// importer's country lookup). The importer auto-creates missing
	// countries during route ingest, but seeding the canonical list up
	// front means a wipe-and-restart restores the same set without
	// re-importing, and downstream code can rely on `code` being
	// populated. INSERT OR IGNORE keeps re-running safe.
	//
	// The UPDATE rewrites a legacy `name='USA'` row (auto-created by older
	// imports before this seed existed) to the canonical 'United States'.
	// No-op once already renamed.
	// ---------------------------------------------------------------------
	`UPDATE countries SET name = 'United States' WHERE code = 'US' AND name = 'USA'`,
	`INSERT OR IGNORE INTO countries (name, code) VALUES ('Austria', 'AT')`,
	`INSERT OR IGNORE INTO countries (name, code) VALUES ('Canada', 'CA')`,
	`INSERT OR IGNORE INTO countries (name, code) VALUES ('Czech Republic', 'CZ')`,
	`INSERT OR IGNORE INTO countries (name, code) VALUES ('France', 'FR')`,
	`INSERT OR IGNORE INTO countries (name, code) VALUES ('Germany', 'DE')`,
	`INSERT OR IGNORE INTO countries (name, code) VALUES ('Italy', 'IT')`,
	`INSERT OR IGNORE INTO countries (name, code) VALUES ('Netherlands', 'NL')`,
	`INSERT OR IGNORE INTO countries (name, code) VALUES ('Switzerland', 'CH')`,
	`INSERT OR IGNORE INTO countries (name, code) VALUES ('United Kingdom', 'GB')`,
	`INSERT OR IGNORE INTO countries (name, code) VALUES ('United States', 'US')`,

	// Weather presets — three baseline scenarios the user can extend
	// from /weather. INSERT OR IGNORE preserves any presets the user has
	// renamed or tuned (only re-adds them when missing).
	`INSERT OR IGNORE INTO weather_presets
		(name, temperature, cloudiness, precipitation, wetness, ground_snow, piled_snow, fog_density)
		VALUES ('Sunny Day', 25.0, 0.1, 0.0, 0.0, 0.0, 0.0, 0.0)`,
	`INSERT OR IGNORE INTO weather_presets
		(name, temperature, cloudiness, precipitation, wetness, ground_snow, piled_snow, fog_density)
		VALUES ('Rainy Day', 12.0, 0.9, 0.8, 0.7, 0.0, 0.0, 0.3)`,
	`INSERT OR IGNORE INTO weather_presets
		(name, temperature, cloudiness, precipitation, wetness, ground_snow, piled_snow, fog_density)
		VALUES ('Snowy Day', -5.0, 0.8, 0.7, 0.3, 0.8, 0.6, 0.2)`,

	// timetable_actions seed — TSW6's canonical action verbs. Without these
	// rows, every imported entry's action_id is NULL (resolveActionID
	// fails silently) which breaks the /timetables Stops column count
	// and downstream HUD action rendering. INSERT OR IGNORE keeps re-runs
	// safe; the importer also self-heals via INSERT-OR-LOOKUP.
	`INSERT OR IGNORE INTO timetable_actions (name) VALUES ('WAIT FOR SERVICE')`,
	`INSERT OR IGNORE INTO timetable_actions (name) VALUES ('WAIT')`,
	`INSERT OR IGNORE INTO timetable_actions (name) VALUES ('STOP AT LOCATION')`,
	`INSERT OR IGNORE INTO timetable_actions (name) VALUES ('LOAD PASSENGERS')`,
	`INSERT OR IGNORE INTO timetable_actions (name) VALUES ('UNLOAD PASSENGERS')`,
	`INSERT OR IGNORE INTO timetable_actions (name) VALUES ('GO VIA LOCATION')`,
	`INSERT OR IGNORE INTO timetable_actions (name) VALUES ('UNCOUPLE VEHICLES')`,
	`INSERT OR IGNORE INTO timetable_actions (name) VALUES ('COUPLE TO FORMATION')`,

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

	// (legacy `train_consists` table is dropped further down; its CREATE
	// block has been removed because (a) fresh DBs don't need it and (b)
	// its FK to `trains` would now be dangling. The DROP TABLE IF EXISTS
	// below covers legacy DBs that still have the table.)

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
	// Per-formation consist data now lives on the formations table + a new
	// formation_vehicles table, rather than denormalised into per-timetable
	// columns. timetables.tonnage / car_count / train_length are dropped
	// because TSW6 doesn't expose tonnage and the rest can be derived from
	// the linked formation. train_consists is dropped as it overlapped
	// with the new formation_vehicles structure.
	//
	// formations.name is the canonical formation_name (e.g. "Class483_006").
	// Per-service JSONs link via their formation_name field; the upload
	// pipeline dedupes any aliases by vehicle-GUID set.
	// ---------------------------------------------------------------------
	`ALTER TABLE formations ADD COLUMN class_name TEXT`,
	`ALTER TABLE formations ADD COLUMN livery_id TEXT`,
	`ALTER TABLE formations ADD COLUMN length_m REAL`,
	`ALTER TABLE formations ADD COLUMN car_count INTEGER`,

	// formation_vehicles: one row per car in a formation, in order.
	// vehicle_id is the GUID exposed by the TSW6 live API
	// (CurrentFormation/<i>.VehicleID, no dashes), used to recognise the
	// running train.
	`CREATE TABLE IF NOT EXISTS formation_vehicles (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		formation_id INTEGER NOT NULL,
		position INTEGER NOT NULL,
		vehicle_id TEXT NOT NULL,
		class_name TEXT,
		friendly_name TEXT,
		livery_id TEXT,
		vehicle_category TEXT,
		length_m REAL,
		is_lead INTEGER DEFAULT 0,
		is_flipped INTEGER DEFAULT 0,
		FOREIGN KEY (formation_id) REFERENCES formations(id) ON DELETE CASCADE,
		UNIQUE(formation_id, position)
	)`,
	`CREATE INDEX IF NOT EXISTS idx_formation_vehicles_formation_id ON formation_vehicles(formation_id)`,
	`CREATE INDEX IF NOT EXISTS idx_formation_vehicles_vehicle_id ON formation_vehicles(vehicle_id)`,

	// Drop legacy per-timetable train spec columns. Requires SQLite 3.35+;
	// older versions will silently leave the columns (they just won't be
	// written to from the new code).
	`ALTER TABLE timetables DROP COLUMN tonnage`,
	`ALTER TABLE timetables DROP COLUMN car_count`,
	`ALTER TABLE timetables DROP COLUMN train_length`,

	// Replaced by formation_vehicles + the existing timetables.formation_id FK.
	`DROP TABLE IF EXISTS train_consists`,

	// ---------------------------------------------------------------------
	// 2026-04-26 formation hierarchy: train_classes groups physical
	// formations by their TSW class. Two Class 483 sets on Isle of Wight
	// share the same class but have different vehicle GUIDs — searches and
	// listings should key on the class, while runtime VehicleID matching
	// stays on formations.id. The class table keeps its `train_classes`
	// name because "train class" is the standard railway term and is a
	// distinct concept from the formation itself.
	//
	// formations.class_name is kept as a denormalised cache so existing
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
	`ALTER TABLE formations ADD COLUMN class_id INTEGER REFERENCES train_classes(id)`,

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
	// formations that have a class_name set. The IGNORE clause keeps the
	// migration idempotent across restarts.
	`INSERT OR IGNORE INTO train_classes (name, livery_id, typical_length_m, typical_car_count)
	 SELECT DISTINCT class_name, livery_id, length_m, car_count
	 FROM formations
	 WHERE class_name IS NOT NULL AND class_name != ''`,

	// Link existing formations to their class. (Formations created
	// post-migration are linked at insert time by the import handler.)
	`UPDATE formations SET class_id = (
		SELECT id FROM train_classes WHERE train_classes.name = formations.class_name
	) WHERE class_id IS NULL AND class_name IS NOT NULL AND class_name != ''`,

	`CREATE INDEX IF NOT EXISTS idx_formations_class_id ON formations(class_id)`,
	`CREATE INDEX IF NOT EXISTS idx_formations_name ON formations(name)`,

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
	// 2026-05-06 rename legacy `cab_stop_*` SQL identifiers to `car_stop_*`
	// to match the UE source name (CarStopSignProperty). On fresh DBs these
	// ALTERs no-op (no such table/column) — the loop tolerates ALTER TABLE
	// failures. On legacy DBs they rename the table + column so the new
	// CREATE TABLE / CREATE INDEX statements below find them already in
	// place. Old indexes are dropped explicitly so they don't linger after
	// the table they pointed at was renamed.
	// ---------------------------------------------------------------------
	`ALTER TABLE cab_stop_signs RENAME TO car_stop_signs`,
	`ALTER TABLE timetable_entries RENAME COLUMN cab_stop_sign_id TO car_stop_sign_id`,
	`DROP INDEX IF EXISTS idx_cab_stop_signs_lookup`,
	`DROP INDEX IF EXISTS idx_cab_stop_signs_ribbon`,
	`DROP INDEX IF EXISTS idx_timetable_entries_cab_stop_sign`,

	// ---------------------------------------------------------------------
	// 2026-04-26 car stop signs (the in-game "Car Stop" / green ring) and
	// pak track markers (TrackMarkerProperty: platform names + junction
	// routing markers like "Smallbrook Junction Line 1").
	//
	// Distinct from the existing route_markers table (which holds player-
	// recorded marker data with one row per (route, station, type)). Pak-
	// derived markers can have multiple rows per (route, name) — one per
	// ribbon they appear on (chained platform geometry).
	//
	// car_stop_signs.platform_name is denormalised from track_markers at
	// import time (joined on ribbon_guid + marker_type='Platform') so the
	// HUD-side lookup can hit a single index without a join.
	// ---------------------------------------------------------------------
	`CREATE TABLE IF NOT EXISTS car_stop_signs (
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
	`CREATE INDEX IF NOT EXISTS idx_car_stop_signs_lookup
		ON car_stop_signs (route_id, platform_name, max_rail_vehicles)`,
	`CREATE INDEX IF NOT EXISTS idx_car_stop_signs_ribbon
		ON car_stop_signs (route_id, ribbon_guid)`,

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

	// Pre-resolved car-stop-sign per timetable_entries row (the single
	// CarStopSign that applies given the timetable's bound + train car
	// count). NULL when no match was found (e.g. depot/scenario services
	// with no platform context, or routes without pak-derived data).
	`ALTER TABLE timetable_entries ADD COLUMN car_stop_sign_id INTEGER REFERENCES car_stop_signs(id)`,
	`CREATE INDEX IF NOT EXISTS idx_timetable_entries_car_stop_sign
		ON timetable_entries(car_stop_sign_id)`,

	// Pre-resolved track-marker per timetable_entries row. Covers the
	// non-Platform routing markers (e.g. "Smallbrook Junction Line 1") that
	// drive the in-game "Go Via X" instructions. Distinct from
	// car_stop_sign_id (which is Platform-only) — a single row will only
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

	// timetable_map_features: per-timetable pre-filtered route GeoJSON
	// blob, computed once at import time and served verbatim on map render.
	// Replaces the request-time per-feature filter loop that walked every
	// route feature × every path vertex. Invalidated by deleting rows when
	// the parent route's `route_coordinates` is rewritten on re-extract.
	`CREATE TABLE IF NOT EXISTS timetable_map_features (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		timetable_id INTEGER NOT NULL UNIQUE,
		features TEXT NOT NULL,
		built_at TEXT DEFAULT CURRENT_TIMESTAMP,
		FOREIGN KEY (timetable_id) REFERENCES timetables(id) ON DELETE CASCADE
	)`,
	`CREATE INDEX IF NOT EXISTS idx_timetable_map_features_tt
		ON timetable_map_features(timetable_id)`,

	// ---------------------------------------------------------------------
	// 2026-05-11 train-class metadata: propulsion / max speed / max power /
	// electrification voltages / thumbnails. Captured from RVD assets
	// during extraction; surfaces in the Train Classes search UI.
	// ---------------------------------------------------------------------
	// Per-vehicle source values live on pak_rvds (one row per RVD asset).
	`ALTER TABLE pak_rvds ADD COLUMN is_electric INTEGER`,
	`ALTER TABLE pak_rvds ADD COLUMN max_speed_kph REAL`,
	`ALTER TABLE pak_rvds ADD COLUMN max_power_kw REAL`,
	`ALTER TABLE pak_rvds ADD COLUMN powered_axle_count INTEGER`,
	`ALTER TABLE pak_rvds ADD COLUMN manufacturer_name TEXT`,
	`ALTER TABLE pak_rvds ADD COLUMN engine_description TEXT`,
	`ALTER TABLE pak_rvds ADD COLUMN type_description TEXT`,
	`ALTER TABLE pak_rvds ADD COLUMN thumbnail_asset_ref TEXT`,
	// JSON-encoded []ElectrificationSpec. The rollup helper unmarshals
	// at class-link time to build train_class_electrification rows. Not
	// queried directly per-RVD by the UI, so a JSON blob is cheaper
	// than a separate per-RVD child table.
	`ALTER TABLE pak_rvds ADD COLUMN electrification_json TEXT`,

	// Class-level aggregates, denormalised onto train_classes so the
	// Train Classes search page can query without JOINs. Re-derived from
	// pak_rvds whenever the importer rebuilds class linkages (any
	// drivable RVD in the class wins for is_electric / max_speed / etc).
	`ALTER TABLE train_classes ADD COLUMN is_electric INTEGER`,
	`ALTER TABLE train_classes ADD COLUMN max_speed_kph REAL`,
	`ALTER TABLE train_classes ADD COLUMN max_power_kw REAL`,
	`ALTER TABLE train_classes ADD COLUMN manufacturer_name TEXT`,
	`ALTER TABLE train_classes ADD COLUMN engine_description TEXT`,
	`ALTER TABLE train_classes ADD COLUMN type_description TEXT`,
	// vehicle_category mirrors the RVD's enum: Locomotive,
	// MultipleUnitCar, PassengerCabCar, PassengerCoach, FreightWagon,
	// CabooseBrakeVan, NPCCS. The Train Classes page filters on it so
	// freight wagons ("50Ft Box Car") don't get treated like engines.
	`ALTER TABLE train_classes ADD COLUMN vehicle_category TEXT`,
	// thumbnail_path is the relative path under views/images/ where the
	// RVD's ThumbnailTexture was rendered to PNG. Set by the importer
	// when it can resolve the asset; left NULL otherwise.
	`ALTER TABLE train_classes ADD COLUMN thumbnail_path TEXT`,
	`CREATE INDEX IF NOT EXISTS idx_train_classes_is_electric
		ON train_classes(is_electric)`,
	`CREATE INDEX IF NOT EXISTS idx_train_classes_vehicle_category
		ON train_classes(vehicle_category)`,

	// rail_vehicle_class is the canonical identity key (stable across AI
	// and player formations in TSW's per-service JSONs). Populated by the
	// importer from route_*.json's train_classes[] section, which sources
	// it from the pak's RVD assets. NULL on legacy rows that predate this
	// column; the importer ON CONFLICT clause keys off this so an index is
	// required to make the upsert work.
	`ALTER TABLE train_classes ADD COLUMN rail_vehicle_class TEXT`,
	// Full (non-partial) UNIQUE index. SQLite treats NULLs as distinct
	// in UNIQUE indexes, so legacy rows with NULL rail_vehicle_class
	// don't collide. A partial index (WHERE rail_vehicle_class IS NOT
	// NULL) wouldn't satisfy the upsert's ON CONFLICT(rail_vehicle_class)
	// clause — SQLite requires a non-partial constraint there.
	//
	// DROP first to replace any existing partial-index version of the
	// same name on DBs created by earlier builds of this migration.
	`DROP INDEX IF EXISTS uq_train_classes_rail_vehicle_class`,
	`CREATE UNIQUE INDEX IF NOT EXISTS uq_train_classes_rail_vehicle_class
		ON train_classes(rail_vehicle_class)`,
	// is_drivable rolls up the per-vehicle pak_rvds.drivable flag — the
	// /train-classes UI filters non-drivable classes (wagons / coaches)
	// off the default view.
	`ALTER TABLE train_classes ADD COLUMN is_drivable INTEGER NOT NULL DEFAULT 0`,
	// powered_axle_count mirrors uasset.RVD.PoweredAxleCount — number of
	// driven axles on the class's drivable variant. Rolled into the class
	// row because TSW propulsion calculations consult it; consumers
	// (route-info pages, future physics widgets) read from train_classes
	// rather than walking pak_rvds.
	`ALTER TABLE train_classes ADD COLUMN powered_axle_count INTEGER`,

	// One-to-many electrification compatibility table. Each row is one
	// (current, pickup, voltage, frequency) entry from an RVD's
	// ElectrificationRequirements list, rolled up to the class. The
	// Train Classes search filters by voltage_v / frequency_hz over a
	// JOIN on this table.
	`CREATE TABLE IF NOT EXISTS train_class_electrification (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		train_class_id INTEGER NOT NULL,
		current TEXT,         -- "OverheadWires", "ThirdRail", "FourthRail"…
		pickup_side TEXT,     -- "TopContact", "BottomContact", …
		voltage_v INTEGER,
		frequency_hz INTEGER, -- 0 == DC
		FOREIGN KEY (train_class_id) REFERENCES train_classes(id) ON DELETE CASCADE,
		UNIQUE (train_class_id, current, voltage_v, frequency_hz)
	)`,
	`CREATE INDEX IF NOT EXISTS idx_train_class_electrification_class
		ON train_class_electrification(train_class_id)`,
	`CREATE INDEX IF NOT EXISTS idx_train_class_electrification_voltage
		ON train_class_electrification(voltage_v)`,
}

// One-shot data backfills run exactly once per database. Tracked via
// SQLite's built-in `PRAGMA user_version` — bump the constant when adding a
// new step. Heuristic-based backfills don't belong in the idempotent
// `migrations` list because better data may arrive later (full re-imports
// from the new extractor) and we don't want the heuristic re-firing on it.
const currentSchemaVersion = 3

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
		name:    "formations: drop UNIQUE constraint on name (formations can share generic names)",
		run: func(db *sql.DB) error {
			// SQLite has no DROP CONSTRAINT — rebuild the table without
			// the UNIQUE on `name`. Generic formation names like
			// "PlayerFormation" and "AIFormation" appear in scenarios
			// across paks (IoW PlayerFormation has Class 483 vehicles,
			// Boston PlayerFormation has CTC-3 vehicles). Two rows with
			// the same name but different vehicle GUIDs are genuinely
			// different physical formations and must coexist.
			//
			// The columns mirror the formations table after all schema
			// migrations have run (id, name, class_name, livery_id,
			// length_m, car_count, class_id). Foreign keys from
			// route_formations / timetable_formations / section_formations
			// / formation_vehicles all reference formations.id which we
			// preserve, so cross-table relationships survive the rebuild.
			//
			// (Pre-rename DBs ran an earlier version of this one-shot
			// against the `trains` table; by the time this version runs
			// in a fresh install, the rename ALTERs at the top of the
			// schema migration list have already produced `formations`.)
			steps := []string{
				`CREATE TABLE formations_v2 (
					id INTEGER PRIMARY KEY AUTOINCREMENT,
					name TEXT NOT NULL,
					class_name TEXT,
					livery_id TEXT,
					length_m REAL,
					car_count INTEGER,
					class_id INTEGER REFERENCES train_classes(id)
				)`,
				`INSERT INTO formations_v2 (id, name, class_name, livery_id, length_m, car_count, class_id)
				 SELECT id, name, class_name, livery_id, length_m, car_count, class_id FROM formations`,
				`DROP TABLE formations`,
				`ALTER TABLE formations_v2 RENAME TO formations`,
				`CREATE INDEX IF NOT EXISTS idx_formations_class_id ON formations(class_id)`,
				`CREATE INDEX IF NOT EXISTS idx_formations_name ON formations(name)`,
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
		version: 3,
		name:    "timetables: route_id ON DELETE SET NULL → CASCADE (prevents orphan timetables on raw route deletes)",
		run: func(db *sql.DB) error {
			// The original FK was SET NULL because route deletion was
			// considered "soft" — kill the route, keep the timetables.
			// In practice a timetable with no route is useless: no map,
			// no listing, no association. Every code path that deletes
			// a route already explicitly deletes its timetables first,
			// so CASCADE preserves observed behaviour while preventing
			// silent orphaning if anything ever bypasses the handler.
			//
			// SQLite has no ALTER...DROP CONSTRAINT — rebuild the table.
			// Column list mirrors the timetables CREATE in `migrations`
			// after all ALTERs. FKs on child tables (timetable_entries,
			// timetable_coordinates, …) still point at timetables.id
			// which we preserve.
			//
			// CRITICAL: we MUST disable foreign_keys for the rebuild.
			// The connection URI sets foreign_keys=ON, which means
			// `DROP TABLE timetables` would fire every child table's
			// `ON DELETE CASCADE` action and wipe all
			// timetable_entries / timetable_coordinates / etc rows on
			// the way out. The rename pattern below preserves the
			// existing timetables.id values, so the child rows still
			// have valid parents after the swap — we just have to keep
			// SQLite from cleaning up under us mid-swap.
			//
			// PRAGMA foreign_keys is a per-connection setting. The
			// database/sql pool may route subsequent Execs to different
			// connections, so pin one Conn for the entire rebuild.
			ctx := context.Background()
			conn, err := db.Conn(ctx)
			if err != nil {
				return err
			}
			defer conn.Close()
			if _, err := conn.ExecContext(ctx, `PRAGMA foreign_keys = OFF`); err != nil {
				return err
			}
			defer conn.ExecContext(ctx, `PRAGMA foreign_keys = ON`)
			steps := []string{
				`CREATE TABLE timetables_v3 (
					id INTEGER PRIMARY KEY AUTOINCREMENT,
					service_name TEXT NOT NULL,
					route_id INTEGER,
					formation_id INTEGER,
					service_type TEXT NOT NULL DEFAULT 'passenger',
					contributor TEXT,
					coordinates_contributor TEXT,
					created_at TEXT DEFAULT CURRENT_TIMESTAMP,
					start_time TEXT,
					duration TEXT,
					service_images TEXT,
					section_id INTEGER,
					conductor_compatible INTEGER DEFAULT 0,
					bound TEXT,
					service TEXT,
					current_service_name TEXT,
					source TEXT,
					playable INTEGER DEFAULT 0,
					FOREIGN KEY (route_id) REFERENCES routes(id) ON DELETE CASCADE,
					FOREIGN KEY (formation_id) REFERENCES formations(id) ON DELETE SET NULL
				)`,
				`INSERT INTO timetables_v3
				   (id, service_name, route_id, formation_id, service_type, contributor,
				    coordinates_contributor, created_at, start_time, duration, service_images,
				    section_id, conductor_compatible, bound, service, current_service_name,
				    source, playable)
				 SELECT id, service_name, route_id, formation_id, service_type, contributor,
				        coordinates_contributor, created_at, start_time, duration, service_images,
				        section_id, conductor_compatible, bound, service, current_service_name,
				        source, playable
				   FROM timetables`,
				`DROP TABLE timetables`,
				`ALTER TABLE timetables_v3 RENAME TO timetables`,
				`CREATE UNIQUE INDEX IF NOT EXISTS uq_timetable_service_route ON timetables(service_name, route_id)`,
				`CREATE INDEX IF NOT EXISTS idx_timetables_source ON timetables(source)`,
				`CREATE INDEX IF NOT EXISTS idx_timetables_playable ON timetables(playable)`,
			}
			for _, s := range steps {
				if _, err := conn.ExecContext(ctx, s); err != nil {
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
