// delete-orphan-duplicates — removes timetables that are duplicates of an
// existing properly-linked row.
//
// An orphan is a row with route_id IS NULL. A duplicate-orphan has another
// row with the same service_name AND route_id IS NOT NULL — i.e. the
// canonical version exists, the orphan is a zombie left over from a
// cross-pak / cross-section import bug.
//
// All known duplicate-orphans live on a single route (Great Western Express);
// see cmd/_peek9 for the breakdown. After deletion, that route should be
// re-extracted from its source paks to verify the canonical rows are intact.
//
// FK cascades clean up timetable_entries / timetable_coordinates /
// timetable_markers / timetable_section_links / etc. (every child table
// declares ON DELETE CASCADE on its timetable_id ref).
//
// Usage:
//
//	delete-orphan-duplicates                   # dry run
//	delete-orphan-duplicates --apply           # actually delete
//	delete-orphan-duplicates --by-route        # break the count down per
//	                                           # route to confirm scope
package main

import (
	"database/sql"
	"flag"
	"fmt"
	"log"

	_ "modernc.org/sqlite"
)

func main() {
	dbPath := flag.String("db", "tsw_hud.db", "path to the SQLite DB")
	apply := flag.Bool("apply", false, "actually DELETE rows (default: dry run)")
	byRoute := flag.Bool("by-route", false, "print counts grouped by canonical row's route")
	flag.Parse()

	db, err := sql.Open("sqlite", *dbPath)
	if err != nil {
		log.Fatalf("open db: %v", err)
	}
	defer db.Close()

	// CRITICAL: SQLite needs foreign_keys=ON per-connection for cascades.
	// Without this, DELETE FROM timetables silently leaves orphan child
	// rows in timetable_entries / timetable_coordinates / etc.
	if _, err := db.Exec("PRAGMA foreign_keys = ON"); err != nil {
		log.Fatalf("enable FK: %v", err)
	}

	const countSQL = `
		SELECT COUNT(*) FROM timetables
		WHERE route_id IS NULL
		  AND EXISTS (
		    SELECT 1 FROM timetables t2
		    WHERE t2.service_name = timetables.service_name
		      AND t2.route_id IS NOT NULL
		  )
	`
	const deleteSQL = `
		DELETE FROM timetables
		WHERE route_id IS NULL
		  AND EXISTS (
		    SELECT 1 FROM timetables t2
		    WHERE t2.service_name = timetables.service_name
		      AND t2.route_id IS NOT NULL
		  )
	`

	var total int
	if err := db.QueryRow(countSQL).Scan(&total); err != nil {
		log.Fatalf("count: %v", err)
	}
	fmt.Printf("orphan duplicates to remove: %d\n", total)

	if *byRoute {
		// Group by the canonical row's route — gives a "which routes hold
		// the dupes" summary so we know what to re-extract afterwards.
		rows, err := db.Query(`
			SELECT r.id, COALESCE(r.name, '<unknown>'), COUNT(*) AS n
			FROM timetables t1
			JOIN timetables t2 ON t2.service_name = t1.service_name AND t2.route_id IS NOT NULL
			JOIN routes r ON r.id = t2.route_id
			WHERE t1.route_id IS NULL
			GROUP BY r.id, r.name
			ORDER BY n DESC
		`)
		if err != nil {
			log.Fatalf("by-route: %v", err)
		}
		defer rows.Close()
		fmt.Printf("\nbreakdown by canonical row's route:\n")
		fmt.Printf("  %-6s  %-7s  %s\n", "route", "count", "name")
		for rows.Next() {
			var rid, n int
			var rname string
			rows.Scan(&rid, &rname, &n)
			fmt.Printf("  %-6d  %-7d  %s\n", rid, n, rname)
		}
	}

	if !*apply {
		fmt.Printf("\ndry run — re-run with --apply to delete %d rows.\n", total)
		return
	}

	tx, err := db.Begin()
	if err != nil {
		log.Fatalf("begin tx: %v", err)
	}
	res, err := tx.Exec(deleteSQL)
	if err != nil {
		tx.Rollback()
		log.Fatalf("delete: %v", err)
	}
	if err := tx.Commit(); err != nil {
		log.Fatalf("commit: %v", err)
	}
	n, _ := res.RowsAffected()
	fmt.Printf("\ndeleted %d timetable rows (cascades dropped child rows).\n", n)
}
