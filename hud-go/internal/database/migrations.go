package database

import (
	"database/sql"
	"fmt"
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
		platform TEXT,
		time1 TEXT,
		time2 TEXT,
		latitude TEXT,
		longitude TEXT,
		api_name TEXT,
		sort_order INTEGER,
		coord_source TEXT,
		FOREIGN KEY (timetable_id) REFERENCES timetables(id) ON DELETE CASCADE
	)`,
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
}

// RunMigrations executes all migration statements in order.
func RunMigrations(db *sql.DB) error {
	for i, stmt := range migrations {
		if _, err := db.Exec(stmt); err != nil {
			return fmt.Errorf("migration %d failed: %w", i, err)
		}
	}
	return nil
}
