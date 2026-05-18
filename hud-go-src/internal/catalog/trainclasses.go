// Package catalog — train_classes reconciliation.
//
// ReconcileTrainClasses synchronises the train_classes table with the
// canonical pak_rvds data:
//
//  1. Link "ghost" rows (NULL rvc) by friendly_name → pak_rvds, skipping
//     any whose target rvc is already in use by another row.
//  2. INSERT new train_classes for rail_vehicle_classes present in
//     pak_rvds but missing from train_classes.
//  3. Backfill snapshot fields (is_drivable / vehicle_category / etc.)
//     onto rows that have an rvc link, preferring the populated
//     pak_rvds value when train_classes' own value is NULL.
//  4. Reconcile thumbnail_path with on-disk PNGs.
//
// Called from:
//   - ScanCatalog (post-loop) so a Scan/Rescan alone yields the full
//     train-class set with no manual Rebuild step.
//   - handler.RebuildTrainClassesFromRVDs (the user-triggered drift
//     recovery button), which still exists for cases like a partial
//     re-extract leaving classes stale.
//
// Idempotent — running it twice is a no-op on a healthy DB.
package catalog

import (
	"database/sql"
	"log"
	"os"
	"path/filepath"

	"hud-go/internal/config"
	"hud-go/internal/pak"
)

// ReconcileResult is the per-step row count summary from
// ReconcileTrainClasses. Surfaces back to the user via the Rebuild
// endpoint and the catalog scan's log line.
type ReconcileResult struct {
	Linked            int64
	DuplicatesSkipped int64
	Inserted          int64
	Backfilled        int64
	ThumbsFixed       int
}

// ReconcileTrainClasses applies all four reconciliation steps and
// returns row counts. Errors on individual steps are logged but don't
// halt the run — the caller wants partial-progress more than strict
// atomicity here.
func ReconcileTrainClasses(db *sql.DB) ReconcileResult {
	var res ReconcileResult

	// Step 1: link ghost rows (NULL rvc) by friendly_name.
	if r, err := db.Exec(`
		UPDATE train_classes
		SET rail_vehicle_class = (
		    SELECT MAX(pv.rail_vehicle_class) FROM pak_rvds pv
		    WHERE pv.friendly_name = train_classes.name
		      AND pv.rail_vehicle_class IS NOT NULL
		      AND pv.rail_vehicle_class != ''
		)
		WHERE (train_classes.rail_vehicle_class IS NULL OR train_classes.rail_vehicle_class = '')
		  AND train_classes.name IN (
		    SELECT DISTINCT friendly_name FROM pak_rvds
		    WHERE friendly_name IS NOT NULL AND friendly_name != ''
		  )
		  AND (
		    SELECT MAX(pv.rail_vehicle_class) FROM pak_rvds pv
		    WHERE pv.friendly_name = train_classes.name
		      AND pv.rail_vehicle_class IS NOT NULL
		      AND pv.rail_vehicle_class != ''
		  ) NOT IN (
		    SELECT rail_vehicle_class FROM train_classes
		    WHERE rail_vehicle_class IS NOT NULL AND rail_vehicle_class != ''
		  )
	`); err == nil {
		res.Linked, _ = r.RowsAffected()
	} else {
		log.Printf("[catalog.ReconcileTrainClasses] step1 link: %v", err)
	}

	_ = db.QueryRow(`
		SELECT COUNT(*) FROM train_classes tc
		WHERE (tc.rail_vehicle_class IS NULL OR tc.rail_vehicle_class = '')
		  AND tc.name IN (
		    SELECT DISTINCT friendly_name FROM pak_rvds
		    WHERE friendly_name IS NOT NULL AND friendly_name != ''
		  )
		  AND (
		    SELECT MAX(pv.rail_vehicle_class) FROM pak_rvds pv
		    WHERE pv.friendly_name = tc.name
		      AND pv.rail_vehicle_class IS NOT NULL
		      AND pv.rail_vehicle_class != ''
		  ) IN (
		    SELECT rail_vehicle_class FROM train_classes
		    WHERE rail_vehicle_class IS NOT NULL AND rail_vehicle_class != ''
		  )
	`).Scan(&res.DuplicatesSkipped)

	// Step 2: insert pak_rvds rows whose rvc isn't yet in train_classes.
	if r, err := db.Exec(`
		INSERT INTO train_classes
		    (name, rail_vehicle_class, livery_id, typical_length_m,
		     is_electric, max_speed_kph, max_power_kw, powered_axle_count,
		     manufacturer_name, engine_description, type_description,
		     vehicle_category, is_drivable, thumbnail_path)
		SELECT
		    MAX(friendly_name)            AS name,
		    rail_vehicle_class            AS rvc,
		    MAX(livery_id)                AS livery_id,
		    MAX(approximate_length_m)     AS typ_len,
		    MAX(is_electric)              AS is_electric,
		    MAX(max_speed_kph)            AS max_speed,
		    MAX(max_power_kw)             AS max_power,
		    MAX(powered_axle_count)       AS pax,
		    MAX(manufacturer_name)        AS mfr,
		    MAX(engine_description)       AS eng,
		    MAX(type_description)         AS typ,
		    MAX(vehicle_category)         AS cat,
		    MAX(drivable)                 AS drv,
		    '/images/train_classes/' || REPLACE(REPLACE(rail_vehicle_class, '/', '_'), ' ', '_') || '.png'
		                                 AS thumb
		FROM pak_rvds
		WHERE rail_vehicle_class IS NOT NULL
		  AND rail_vehicle_class != ''
		  AND rail_vehicle_class NOT IN (
		    SELECT rail_vehicle_class FROM train_classes
		    WHERE rail_vehicle_class IS NOT NULL AND rail_vehicle_class != ''
		  )
		GROUP BY rail_vehicle_class
	`); err == nil {
		res.Inserted, _ = r.RowsAffected()
	} else {
		log.Printf("[catalog.ReconcileTrainClasses] step2 insert: %v", err)
	}

	// Step 3: backfill snapshot fields from pak_rvds.
	if r, err := db.Exec(`
		UPDATE train_classes
		SET is_drivable = COALESCE(
		      (SELECT MAX(drivable) FROM pak_rvds WHERE rail_vehicle_class = train_classes.rail_vehicle_class),
		      is_drivable),
		    is_electric = COALESCE(
		      (SELECT MAX(is_electric) FROM pak_rvds WHERE rail_vehicle_class = train_classes.rail_vehicle_class),
		      is_electric),
		    vehicle_category = COALESCE(
		      (SELECT MAX(vehicle_category) FROM pak_rvds WHERE rail_vehicle_class = train_classes.rail_vehicle_class),
		      vehicle_category),
		    type_description = COALESCE(
		      (SELECT MAX(type_description) FROM pak_rvds WHERE rail_vehicle_class = train_classes.rail_vehicle_class),
		      type_description),
		    manufacturer_name = COALESCE(
		      (SELECT MAX(manufacturer_name) FROM pak_rvds WHERE rail_vehicle_class = train_classes.rail_vehicle_class),
		      manufacturer_name),
		    engine_description = COALESCE(
		      (SELECT MAX(engine_description) FROM pak_rvds WHERE rail_vehicle_class = train_classes.rail_vehicle_class),
		      engine_description),
		    max_speed_kph = COALESCE(
		      (SELECT MAX(max_speed_kph) FROM pak_rvds WHERE rail_vehicle_class = train_classes.rail_vehicle_class),
		      max_speed_kph),
		    max_power_kw = COALESCE(
		      (SELECT MAX(max_power_kw) FROM pak_rvds WHERE rail_vehicle_class = train_classes.rail_vehicle_class),
		      max_power_kw),
		    powered_axle_count = COALESCE(
		      (SELECT MAX(powered_axle_count) FROM pak_rvds WHERE rail_vehicle_class = train_classes.rail_vehicle_class),
		      powered_axle_count)
		WHERE rail_vehicle_class IS NOT NULL AND rail_vehicle_class != ''
	`); err == nil {
		res.Backfilled, _ = r.RowsAffected()
	} else {
		log.Printf("[catalog.ReconcileTrainClasses] step3 backfill: %v", err)
	}

	// Step 4: reconcile thumbnail_path with on-disk PNGs.
	res.ThumbsFixed = FixTrainClassThumbnails(db)

	return res
}

// FixTrainClassThumbnails reconciles every train_classes.thumbnail_path
// with what's actually on disk under <appDir>/images/train_classes/.
// For each class it builds an ordered candidate-stem list:
//
//  1. Every distinct FriendlyName from pak_rvds with the same
//     rail_vehicle_class, ordered to prefer paks OTHER than the
//     Training Centre. Multiple RVDs commonly share an rvc (Spirit of
//     Steam's "LMS Stanier 8F" and Training Centre's "Stanier 8F TTC"
//     both have rvc="Stanier 8F"); the canonical/main livery's PNG is
//     the one users actually want to see, not the gray tutorial
//     render. The TrainingCentre pak is the one base-game pak we know
//     ships training/placeholder renders rather than canonical
//     marketing thumbnails — same rule as the hardcoded TC origin.
//  2. The rvc itself (catches MTA/LIRR-style DLCs where FriendlyName
//     is "M3A" but the row's name is "M3A MTA").
//  3. The current row name (last-ditch — covers ghost rows whose rvc
//     never got linked).
//
// Each candidate is sanitised via pak.SanitiseThumbnailName and probed
// against the on-disk thumbnails dir. The first PNG that actually
// exists wins. Returns the count of rows that got a new thumbnail_path.
//
// Idempotent and safe to call after every auto-import — does not write
// when no candidate matches an existing PNG.
func FixTrainClassThumbnails(db *sql.DB) int {
	thumbsDir := filepath.Join(config.ResourcesDir(), "images", "train_classes")
	tcRows, err := db.Query(`SELECT id, name, rail_vehicle_class FROM train_classes`)
	if err != nil {
		log.Printf("[train-classes] FixTrainClassThumbnails query: %v", err)
		return 0
	}
	defer tcRows.Close()
	fixed := 0
	for tcRows.Next() {
		var id int
		var nm, rvc sql.NullString
		if err := tcRows.Scan(&id, &nm, &rvc); err != nil {
			continue
		}
		var friendlies []string
		if rvc.Valid && rvc.String != "" {
			fnRows, ferr := db.Query(`
				SELECT pv.friendly_name
				FROM pak_rvds pv
				LEFT JOIN pak_catalog pc ON pc.pak_path = pv.pak_path
				WHERE pv.rail_vehicle_class = ?
				  AND pv.friendly_name IS NOT NULL AND pv.friendly_name != ''
				ORDER BY CASE WHEN pc.codename = 'TrainingCentre' THEN 1 ELSE 0 END,
				         pv.friendly_name`, rvc.String)
			if ferr == nil {
				seen := map[string]bool{}
				for fnRows.Next() {
					var f sql.NullString
					if fnRows.Scan(&f) == nil && f.Valid && f.String != "" && !seen[f.String] {
						seen[f.String] = true
						friendlies = append(friendlies, f.String)
					}
				}
				fnRows.Close()
			}
		}
		candidates := []string{}
		for _, f := range friendlies {
			candidates = append(candidates, pak.SanitiseThumbnailName(f))
		}
		if rvc.Valid && rvc.String != "" {
			candidates = append(candidates, pak.SanitiseThumbnailName(rvc.String))
		}
		if nm.Valid && nm.String != "" {
			candidates = append(candidates, pak.SanitiseThumbnailName(nm.String))
		}
		for _, stem := range candidates {
			if stem == "" {
				continue
			}
			if _, statErr := os.Stat(filepath.Join(thumbsDir, stem+".png")); statErr == nil {
				db.Exec(`UPDATE train_classes SET thumbnail_path=? WHERE id=?`,
					"/images/train_classes/"+stem+".png", id)
				fixed++
				break
			}
		}
	}
	return fixed
}
