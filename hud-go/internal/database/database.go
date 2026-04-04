package database

import (
	"database/sql"
	"fmt"
	"path/filepath"

	"hud-go/internal/config"

	_ "modernc.org/sqlite"
)

// Open opens the SQLite database, enables WAL mode and foreign keys,
// runs migrations, and seeds initial data.
func Open() (*sql.DB, error) {
	dbPath := filepath.Join(config.AppDir(), "tsw_hud.db")
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		return nil, fmt.Errorf("open database: %w", err)
	}

	// Enable foreign key enforcement.
	if _, err := db.Exec("PRAGMA foreign_keys=ON"); err != nil {
		db.Close()
		return nil, fmt.Errorf("enable foreign keys: %w", err)
	}

	if err := migrate(db); err != nil {
		db.Close()
		return nil, fmt.Errorf("migrate: %w", err)
	}

	return db, nil
}

// migrate runs all CREATE TABLE IF NOT EXISTS statements.
func migrate(db *sql.DB) error {
	return RunMigrations(db)
}
