// Package catalog persists per-pak metadata (display name, country,
// parent-route flag, cross-pak references) so the /extractor page can
// render the route tree instantly instead of warming up by walking
// ~36 paks every page load. Refreshed on demand via the rescan
// endpoint, diffed by pak file mtime + size so unchanged paks are
// skipped.
package catalog

import (
	"database/sql"
	"encoding/json"
	"strings"
)

// Pak is one row in the pak_catalog table.
type Pak struct {
	PakPath               string   // absolute on-disk path; primary key
	Codename              string   // pak-filename-derived name, e.g. "WCMLSouth"
	DisplayName           string   // canonical name from data: "WCML South - London Euston to Milton Keynes"
	CountryCode           string   // short code from RouteDetails.Country ("UK","US",…); "" for non-route paks
	HasRouteDef           bool     // true when this pak ships its own RouteDefinition (i.e. it IS a parent route)
	CrossPakReferenceName string   // this pak's OWN mount, e.g. "EustonMiltonKeynes"; only set when HasRouteDef=true
	CrossPakRefs          []string // every cross_pak_reference_name found in the pak's timetables / scenario defs
	PakMtime              int64    // for invalidation: filesystem ModTime in Unix seconds
	PakSize               int64    // for invalidation: file size in bytes
	ScannedAt             int64    // Unix seconds of the last scan
}

// LoadAll returns every cataloged pak ordered by codename.
func LoadAll(db *sql.DB) ([]*Pak, error) {
	rows, err := db.Query(`SELECT pak_path, codename, display_name, country_code,
		has_route_definition, cross_pak_reference_name, cross_pak_references,
		pak_mtime, pak_size, scanned_at
		FROM pak_catalog ORDER BY codename ASC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*Pak
	for rows.Next() {
		p, err := scanRow(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// Lookup returns the catalog entry for one pak path, or (nil, nil) if no
// row exists.
func Lookup(db *sql.DB, pakPath string) (*Pak, error) {
	row := db.QueryRow(`SELECT pak_path, codename, display_name, country_code,
		has_route_definition, cross_pak_reference_name, cross_pak_references,
		pak_mtime, pak_size, scanned_at
		FROM pak_catalog WHERE pak_path = ?`, pakPath)
	p, err := scanRow(row)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	return p, err
}

// Upsert writes (or overwrites) the catalog entry for one pak.
func Upsert(db *sql.DB, p *Pak) error {
	refsJSON := "[]"
	if len(p.CrossPakRefs) > 0 {
		// Sort + dedupe for stable storage; callers can be sloppy.
		set := make(map[string]struct{}, len(p.CrossPakRefs))
		for _, r := range p.CrossPakRefs {
			r = strings.TrimSpace(r)
			if r == "" {
				continue
			}
			set[r] = struct{}{}
		}
		uniq := make([]string, 0, len(set))
		for r := range set {
			uniq = append(uniq, r)
		}
		// stable order — sort.Strings would import sort; use a tiny manual sort.
		for i := range uniq {
			for j := i + 1; j < len(uniq); j++ {
				if uniq[j] < uniq[i] {
					uniq[i], uniq[j] = uniq[j], uniq[i]
				}
			}
		}
		b, _ := json.Marshal(uniq)
		refsJSON = string(b)
	}
	hasRouteDef := 0
	if p.HasRouteDef {
		hasRouteDef = 1
	}
	_, err := db.Exec(`INSERT INTO pak_catalog
		(pak_path, codename, display_name, country_code, has_route_definition,
		 cross_pak_reference_name, cross_pak_references,
		 pak_mtime, pak_size, scanned_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(pak_path) DO UPDATE SET
			codename = excluded.codename,
			display_name = excluded.display_name,
			country_code = excluded.country_code,
			has_route_definition = excluded.has_route_definition,
			cross_pak_reference_name = excluded.cross_pak_reference_name,
			cross_pak_references = excluded.cross_pak_references,
			pak_mtime = excluded.pak_mtime,
			pak_size = excluded.pak_size,
			scanned_at = excluded.scanned_at`,
		p.PakPath, p.Codename, p.DisplayName, p.CountryCode, hasRouteDef,
		nilIfEmpty(p.CrossPakReferenceName), refsJSON,
		p.PakMtime, p.PakSize, p.ScannedAt)
	return err
}

// nilIfEmpty turns "" into a NULL parameter so the column is properly
// blank instead of an empty-string. Helps with index lookups and
// uniqueness checks downstream.
func nilIfEmpty(s string) any {
	if s == "" {
		return nil
	}
	return s
}

// Delete removes a row (used when a pak is no longer on disk).
func Delete(db *sql.DB, pakPath string) error {
	_, err := db.Exec(`DELETE FROM pak_catalog WHERE pak_path = ?`, pakPath)
	return err
}

// rowScanner is the common interface satisfied by *sql.Row and *sql.Rows.
type rowScanner interface {
	Scan(dest ...any) error
}

func scanRow(r rowScanner) (*Pak, error) {
	var (
		p           Pak
		displayName sql.NullString
		country     sql.NullString
		ownRef      sql.NullString
		refsJSON    sql.NullString
		hasRouteDef int
	)
	if err := r.Scan(&p.PakPath, &p.Codename, &displayName, &country, &hasRouteDef,
		&ownRef, &refsJSON, &p.PakMtime, &p.PakSize, &p.ScannedAt); err != nil {
		return nil, err
	}
	p.DisplayName = displayName.String
	p.CountryCode = country.String
	p.HasRouteDef = hasRouteDef != 0
	p.CrossPakReferenceName = ownRef.String
	if refsJSON.Valid && refsJSON.String != "" {
		_ = json.Unmarshal([]byte(refsJSON.String), &p.CrossPakRefs)
	}
	return &p, nil
}
