package catalog

import (
	"database/sql"
	"encoding/json"

	"hud-go/internal/pak/uasset"
)

// ReplaceRVDs swaps out the catalog's RVD rows for one pak. Used after
// a successful pak scan: the previous rows are wiped and the freshly-
// parsed slice is inserted in one transaction.
//
// All-or-nothing: a partial write would leave the catalog in a
// confusing state (some old rows + some new), so a failure on the
// insert path rolls back the delete too.
func ReplaceRVDs(db *sql.DB, pakPath string, rvds []*uasset.RVD) error {
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback() // no-op after Commit

	if _, err := tx.Exec("DELETE FROM pak_rvds WHERE pak_path = ?", pakPath); err != nil {
		return err
	}
	if len(rvds) == 0 {
		return tx.Commit()
	}
	stmt, err := tx.Prepare(`INSERT INTO pak_rvds
		(pak_path, asset_path, rail_vehicle_class, friendly_name, livery_id,
		 vehicle_category, approximate_length_m, drivable, substitutable_unit,
		 has_guard_controls, service_types, regions)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`)
	if err != nil {
		return err
	}
	defer stmt.Close()

	for _, r := range rvds {
		if r == nil || r.AssetPath == "" {
			continue
		}
		var regions sql.NullString
		if len(r.Regions) > 0 {
			b, _ := json.Marshal(r.Regions)
			regions = sql.NullString{String: string(b), Valid: true}
		}
		if _, err := stmt.Exec(
			pakPath,
			r.AssetPath,
			nilIfEmpty(r.RailVehicleClass),
			nilIfEmpty(r.FriendlyName),
			nilIfEmpty(r.LiveryID),
			nilIfEmpty(r.VehicleCategory),
			r.ApproximateLenM,
			boolToInt(r.Drivable),
			boolToInt(r.SubstitutableUnit),
			boolToInt(r.HasGuardControls),
			r.ServiceTypes,
			regions,
		); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// LoadAllRVDs returns every RVD in the catalog keyed by its canonical
// `asset_path`. Same map shape `extractor.ScanAllRVDs` produces, drop-in
// replacement so the writer's lookup logic doesn't change.
func LoadAllRVDs(db *sql.DB) (map[string]*uasset.RVD, error) {
	rows, err := db.Query(`SELECT asset_path, rail_vehicle_class, friendly_name, livery_id,
		vehicle_category, approximate_length_m, drivable, substitutable_unit,
		has_guard_controls, service_types, regions
		FROM pak_rvds`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make(map[string]*uasset.RVD, 512)
	for rows.Next() {
		var (
			r           uasset.RVD
			class       sql.NullString
			friendly    sql.NullString
			livery      sql.NullString
			category    sql.NullString
			lengthM     sql.NullFloat64
			drivable    int
			subUnit     int
			hasGuard    int
			serviceTypes sql.NullInt64
			regionsJSON sql.NullString
		)
		if err := rows.Scan(&r.AssetPath, &class, &friendly, &livery, &category,
			&lengthM, &drivable, &subUnit, &hasGuard, &serviceTypes, &regionsJSON); err != nil {
			return nil, err
		}
		r.RailVehicleClass = class.String
		r.FriendlyName = friendly.String
		r.LiveryID = livery.String
		r.VehicleCategory = category.String
		if lengthM.Valid {
			r.ApproximateLenM = float32(lengthM.Float64)
		}
		r.Drivable = drivable != 0
		r.SubstitutableUnit = subUnit != 0
		r.HasGuardControls = hasGuard != 0
		if serviceTypes.Valid {
			r.ServiceTypes = int(serviceTypes.Int64)
		}
		if regionsJSON.Valid && regionsJSON.String != "" {
			_ = json.Unmarshal([]byte(regionsJSON.String), &r.Regions)
		}
		out[r.AssetPath] = &r
	}
	return out, rows.Err()
}

func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}
