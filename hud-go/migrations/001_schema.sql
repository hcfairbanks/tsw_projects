-- Migration 001: Schema
-- Auto-generated from tsw_hud.db

PRAGMA foreign_keys = OFF;

CREATE TABLE countries (
            id INTEGER PRIMARY KEY AUTOINCREMENT,
            name TEXT NOT NULL UNIQUE,
            code TEXT
        );

CREATE TABLE train_classes (
            id INTEGER PRIMARY KEY AUTOINCREMENT,
            name TEXT NOT NULL UNIQUE
        , in_game_name TEXT);

CREATE TABLE trains (
            id INTEGER PRIMARY KEY AUTOINCREMENT,
            name TEXT NOT NULL UNIQUE,
            class_id INTEGER, in_game_name TEXT,
            FOREIGN KEY (class_id) REFERENCES train_classes(id) ON DELETE SET NULL
        );

CREATE TABLE timetable_actions (
            id INTEGER PRIMARY KEY AUTOINCREMENT,
            name TEXT NOT NULL UNIQUE
        );

CREATE TABLE weather_presets (
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
        );

CREATE TABLE routes (
            id INTEGER PRIMARY KEY AUTOINCREMENT,
            name TEXT NOT NULL UNIQUE,
            country_id INTEGER NOT NULL,
            tsw_version INTEGER NOT NULL DEFAULT 3,
            FOREIGN KEY (country_id) REFERENCES countries(id) ON DELETE RESTRICT
        );

CREATE TABLE locations (
            id INTEGER PRIMARY KEY AUTOINCREMENT,
            route_id INTEGER NOT NULL,
            name TEXT NOT NULL,
            FOREIGN KEY (route_id) REFERENCES routes(id) ON DELETE CASCADE,
            UNIQUE(route_id, name)
        );

CREATE TABLE sections (
            id INTEGER PRIMARY KEY AUTOINCREMENT,
            route_id INTEGER NOT NULL,
            name TEXT NOT NULL,
            FOREIGN KEY (route_id) REFERENCES routes(id) ON DELETE CASCADE,
            UNIQUE(route_id, name)
        );

CREATE TABLE route_train_classes (
            id INTEGER PRIMARY KEY AUTOINCREMENT,
            route_id INTEGER NOT NULL,
            class_id INTEGER NOT NULL,
            FOREIGN KEY (route_id) REFERENCES routes(id) ON DELETE CASCADE,
            FOREIGN KEY (class_id) REFERENCES train_classes(id) ON DELETE CASCADE,
            UNIQUE(route_id, class_id)
        );

CREATE TABLE route_trains (
            id INTEGER PRIMARY KEY AUTOINCREMENT,
            route_id INTEGER NOT NULL,
            train_id INTEGER NOT NULL,
            FOREIGN KEY (route_id) REFERENCES routes(id) ON DELETE CASCADE,
            FOREIGN KEY (train_id) REFERENCES trains(id) ON DELETE CASCADE,
            UNIQUE(route_id, train_id)
        );

CREATE TABLE route_coordinates (
            id INTEGER PRIMARY KEY AUTOINCREMENT,
            route_id INTEGER NOT NULL UNIQUE,
            coordinates TEXT NOT NULL,
            updated_at TEXT DEFAULT CURRENT_TIMESTAMP,
            FOREIGN KEY (route_id) REFERENCES routes(id) ON DELETE CASCADE
        );

CREATE TABLE route_markers (
            id INTEGER PRIMARY KEY AUTOINCREMENT,
            route_id INTEGER NOT NULL,
            station_name TEXT NOT NULL,
            marker_type TEXT,
            latitude REAL,
            longitude REAL,
            platform_length REAL,
            FOREIGN KEY (route_id) REFERENCES routes(id) ON DELETE CASCADE,
            UNIQUE(route_id, station_name, marker_type)
        );

CREATE TABLE route_locations (
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
        );

CREATE TABLE station_name_mappings (
            id INTEGER PRIMARY KEY AUTOINCREMENT,
            route_id INTEGER,
            display_name TEXT NOT NULL,
            api_name TEXT NOT NULL,
            created_at TEXT DEFAULT CURRENT_TIMESTAMP,
            FOREIGN KEY (route_id) REFERENCES routes(id) ON DELETE CASCADE,
            UNIQUE(route_id, display_name)
        );

CREATE TABLE "timetables" (
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
                service TEXT, current_service_name TEXT,
                FOREIGN KEY (route_id) REFERENCES routes(id) ON DELETE SET NULL,
                FOREIGN KEY (train_id) REFERENCES trains(id) ON DELETE SET NULL
            );

CREATE TABLE timetable_trains (
            id INTEGER PRIMARY KEY AUTOINCREMENT,
            timetable_id INTEGER NOT NULL,
            train_id INTEGER NOT NULL,
            FOREIGN KEY (timetable_id) REFERENCES timetables(id) ON DELETE CASCADE,
            FOREIGN KEY (train_id) REFERENCES trains(id) ON DELETE CASCADE,
            UNIQUE(timetable_id, train_id)
        );

CREATE TABLE timetable_entries (
            id INTEGER PRIMARY KEY AUTOINCREMENT,
            timetable_id INTEGER NOT NULL,
            details TEXT,
            structure_number TEXT,
            structure TEXT,
            time1 TEXT,
            time2 TEXT,
            latitude TEXT,
            longitude TEXT,
            api_name TEXT,
            sort_order INTEGER, coord_source TEXT DEFAULT 'automatic', location_id INTEGER REFERENCES locations(id), action_id INTEGER REFERENCES timetable_actions(id),
            FOREIGN KEY (timetable_id) REFERENCES timetables(id) ON DELETE CASCADE
        );

CREATE TABLE timetable_coordinates (
            id INTEGER PRIMARY KEY AUTOINCREMENT,
            timetable_id INTEGER NOT NULL UNIQUE,
            coordinates TEXT NOT NULL, coord_source TEXT DEFAULT 'automatic',
            FOREIGN KEY (timetable_id) REFERENCES timetables(id) ON DELETE CASCADE
        );

CREATE TABLE timetable_markers (
            id INTEGER PRIMARY KEY AUTOINCREMENT,
            timetable_id INTEGER NOT NULL,
            station_name TEXT NOT NULL,
            marker_type TEXT,
            latitude REAL,
            longitude REAL,
            platform_length REAL,
            FOREIGN KEY (timetable_id) REFERENCES timetables(id) ON DELETE CASCADE
        );

CREATE TABLE timetable_sections (
            id INTEGER PRIMARY KEY AUTOINCREMENT,
            timetable_id INTEGER NOT NULL,
            section_id INTEGER NOT NULL,
            FOREIGN KEY (timetable_id) REFERENCES timetables(id) ON DELETE CASCADE,
            FOREIGN KEY (section_id) REFERENCES sections(id) ON DELETE CASCADE,
            UNIQUE(timetable_id, section_id)
        );

CREATE TABLE section_trains (
            id INTEGER PRIMARY KEY AUTOINCREMENT,
            section_id INTEGER NOT NULL,
            train_id INTEGER NOT NULL,
            FOREIGN KEY (section_id) REFERENCES sections(id) ON DELETE CASCADE,
            FOREIGN KEY (train_id) REFERENCES trains(id) ON DELETE CASCADE,
            UNIQUE(section_id, train_id)
        );

CREATE INDEX idx_timetable_coordinates_timetable_id ON timetable_coordinates(timetable_id);

CREATE INDEX idx_timetable_markers_timetable_id ON timetable_markers(timetable_id);

CREATE INDEX idx_locations_route_id ON locations(route_id);

CREATE INDEX idx_route_coordinates_route_id ON route_coordinates(route_id);

CREATE INDEX idx_route_markers_route_id ON route_markers(route_id);

CREATE INDEX idx_route_locations_route_id ON route_locations(route_id);

CREATE UNIQUE INDEX uq_timetable_csn_service_route ON timetables(current_service_name, service_name, route_id);

CREATE UNIQUE INDEX uq_timetable_service_route ON timetables(service_name, route_id);

CREATE INDEX idx_timetable_entries_timetable_id ON timetable_entries(timetable_id);

PRAGMA foreign_keys = ON;
