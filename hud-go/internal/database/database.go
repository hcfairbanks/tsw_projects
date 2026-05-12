package database

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"

	"hud-go/internal/config"

	_ "modernc.org/sqlite"
)

// Open opens the SQLite database, enables WAL mode and foreign keys,
// runs migrations, and seeds initial data.
//
// Performance pragmas (set via the connection URI so every pooled
// connection inherits them):
//
//   - journal_mode=WAL — write-ahead log instead of rollback journal.
//     Smaller per-commit work, allows readers concurrent with writers,
//     and the mode is sticky in the file itself so it survives restart.
//   - synchronous=NORMAL — fsync at checkpoint boundaries instead of
//     every commit. Trades a ~1 sec window of crash-loss for ~10-100×
//     faster small-write throughput. Acceptable for a local dev tool;
//     before this fix a Boston Sprinter import (30,000+ tiny INSERTs)
//     took 20+ min because every write was a full fsync.
//   - foreign_keys=1 — same as the explicit PRAGMA we used to run
//     after Open; folded in here so it applies to every pool connection.
func Open() (*sql.DB, error) {
	if err := os.MkdirAll(config.DBDir(), 0o755); err != nil {
		return nil, fmt.Errorf("create db dir: %w", err)
	}
	dbPath := filepath.Join(config.DBDir(), "tsw_hud.db")
	uri := dbPath + "?_pragma=journal_mode(WAL)&_pragma=synchronous(NORMAL)&_pragma=foreign_keys(1)"
	db, err := sql.Open("sqlite", uri)
	if err != nil {
		return nil, fmt.Errorf("open database: %w", err)
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
