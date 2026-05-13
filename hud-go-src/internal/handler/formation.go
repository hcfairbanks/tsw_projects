package handler

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"

	"hud-go/internal/models"
	"hud-go/internal/util"
)

// FormationHandler serves /api/formations/* endpoints (formerly /api/trains/*
// — see migrations.go for the 2026-05-10 rename rationale).
type FormationHandler struct {
	db *sql.DB
}

func (h *FormationHandler) GetAll(w http.ResponseWriter, r *http.Request) {
	// Include class_id + class_name so search/listings can show the
	// user-facing class label (e.g. "Isle Of Wight Class 483") rather than
	// just the formation name (e.g. "Class483_006").
	//
	// Supports `?search=` (matches name OR class_name, case-insensitive
	// substring) and `?limit=N` for typeahead callers that don't want the
	// full table dumped on every keystroke.
	q := r.URL.Query()
	search := strings.TrimSpace(q.Get("search"))
	limitStr := q.Get("limit")

	sb := "SELECT id, name, class_id, class_name FROM formations"
	var args []any
	if search != "" {
		sb += " WHERE name LIKE ? OR class_name LIKE ?"
		pat := "%" + search + "%"
		args = append(args, pat, pat)
	}
	sb += " ORDER BY COALESCE(class_name, name), name"
	if limit, _ := strconv.Atoi(limitStr); limit > 0 {
		sb += fmt.Sprintf(" LIMIT %d", limit)
	}

	rows, err := h.db.Query(sb, args...)
	if err != nil {
		util.Error(w, 500, err.Error())
		return
	}
	defer rows.Close()

	var items []models.Formation
	for rows.Next() {
		var t models.Formation
		if err := rows.Scan(&t.ID, &t.Name, &t.ClassID, &t.ClassName); err != nil {
			util.Error(w, 500, err.Error())
			return
		}
		items = append(items, t)
	}
	if items == nil {
		items = []models.Formation{}
	}
	util.JSON(w, 200, items)
}

func (h *FormationHandler) GetByName(w http.ResponseWriter, r *http.Request) {
	name := r.URL.Query().Get("name")
	if name == "" {
		util.Error(w, 400, "Missing required query parameter: name")
		return
	}

	var t models.Formation
	err := h.db.QueryRow("SELECT id, name FROM formations WHERE name = ?", name).Scan(&t.ID, &t.Name)
	if err == sql.ErrNoRows {
		util.Error(w, 404, "No formation found with name \""+name+"\"")
		return
	}
	if err != nil {
		util.Error(w, 500, err.Error())
		return
	}

	util.JSON(w, 200, t)
}

func (h *FormationHandler) Create(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Name string `json:"name"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		util.Error(w, 400, "invalid JSON body")
		return
	}

	if body.Name == "" {
		util.Error(w, 400, "Formation name is required")
		return
	}

	// Check if formation name already exists
	var existingID int
	err := h.db.QueryRow("SELECT id FROM formations WHERE name = ?", body.Name).Scan(&existingID)
	if err == nil {
		util.Error(w, 409, "A formation with this name already exists")
		return
	}

	result, err := h.db.Exec("INSERT INTO formations (name) VALUES (?)", body.Name)
	if err != nil {
		util.Error(w, 500, err.Error())
		return
	}

	id, _ := result.LastInsertId()
	util.JSON(w, 201, map[string]any{"id": id, "name": body.Name})
}

func (h *FormationHandler) GetByID(w http.ResponseWriter, r *http.Request) {
	idStr := chi.URLParam(r, "id")
	id, err := strconv.Atoi(idStr)
	if err != nil {
		util.Error(w, 400, "invalid id")
		return
	}

	var t models.Formation
	err = h.db.QueryRow(
		"SELECT id, name, class_id, class_name, livery_id, length_m, car_count FROM formations WHERE id = ?", id,
	).Scan(&t.ID, &t.Name, &t.ClassID, &t.ClassName, &t.LiveryID, &t.LengthM, &t.CarCount)
	if err == sql.ErrNoRows {
		util.Error(w, 404, "Formation not found")
		return
	}
	if err != nil {
		util.Error(w, 500, err.Error())
		return
	}

	util.JSON(w, 200, t)
}

// trainClass is the JSON shape returned by GetClassByID and ListClasses.
// Lifted to package scope so both handlers can share it.
type trainClass struct {
	ID                int      `json:"id"`
	Name              string   `json:"name"`
	LiveryID          *string  `json:"livery_id,omitempty"`
	TypicalLengthM    *float64 `json:"typical_length_m,omitempty"`
	TypicalCarCount   *int     `json:"typical_car_count,omitempty"`
	IsElectric        *bool    `json:"is_electric,omitempty"`
	MaxSpeedKph       *float64 `json:"max_speed_kph,omitempty"`
	MaxPowerKw        *float64 `json:"max_power_kw,omitempty"`
	ManufacturerName  *string  `json:"manufacturer_name,omitempty"`
	EngineDescription *string  `json:"engine_description,omitempty"`
	TypeDescription   *string  `json:"type_description,omitempty"`
	VehicleCategory   *string  `json:"vehicle_category,omitempty"`
	ThumbnailPath     *string  `json:"thumbnail_path,omitempty"`
}

// classElec is one electrification spec rolled up to a class.
type classElec struct {
	Current     *string `json:"current,omitempty"`
	PickupSide  *string `json:"pickup_side,omitempty"`
	VoltageV    *int    `json:"voltage_v,omitempty"`
	FrequencyHz *int    `json:"frequency_hz,omitempty"`
}

const trainClassSelectCols = `id, name, livery_id, typical_length_m, typical_car_count,
	is_electric, max_speed_kph, max_power_kw,
	manufacturer_name, engine_description, type_description,
	vehicle_category, thumbnail_path`

func scanTrainClass(scanner interface{ Scan(...any) error }, c *trainClass) error {
	var isElec sql.NullInt64
	if err := scanner.Scan(&c.ID, &c.Name, &c.LiveryID, &c.TypicalLengthM, &c.TypicalCarCount,
		&isElec, &c.MaxSpeedKph, &c.MaxPowerKw,
		&c.ManufacturerName, &c.EngineDescription, &c.TypeDescription,
		&c.VehicleCategory, &c.ThumbnailPath); err != nil {
		return err
	}
	if isElec.Valid {
		b := isElec.Int64 != 0
		c.IsElectric = &b
	}
	return nil
}

// GetClassByID returns a train_classes row plus the list of formations that
// belong to it. The class table is intentionally still named `train_classes`
// — "train class" is the standard railway term and a separate concept from
// the formation itself.
func (h *FormationHandler) GetClassByID(w http.ResponseWriter, r *http.Request) {
	idStr := chi.URLParam(r, "id")
	id, err := strconv.Atoi(idStr)
	if err != nil {
		util.Error(w, 400, "invalid id")
		return
	}
	var c trainClass
	err = scanTrainClass(h.db.QueryRow(
		"SELECT "+trainClassSelectCols+" FROM train_classes WHERE id = ?", id), &c)
	if err == sql.ErrNoRows {
		util.Error(w, 404, "Train class not found")
		return
	}
	if err != nil {
		util.Error(w, 500, err.Error())
		return
	}
	// Electrification specs for this class.
	elecRows, _ := h.db.Query(
		`SELECT current, pickup_side, voltage_v, frequency_hz
		 FROM train_class_electrification WHERE train_class_id = ?
		 ORDER BY voltage_v`, id)
	var elec []classElec
	if elecRows != nil {
		for elecRows.Next() {
			var e classElec
			if err := elecRows.Scan(&e.Current, &e.PickupSide, &e.VoltageV, &e.FrequencyHz); err == nil {
				elec = append(elec, e)
			}
		}
		elecRows.Close()
	}

	// Formations under this class.
	rows, err := h.db.Query(
		"SELECT id, name, length_m, car_count FROM formations WHERE class_id = ? ORDER BY name", id)
	if err != nil {
		util.Error(w, 500, err.Error())
		return
	}
	defer rows.Close()
	type formation struct {
		ID       int      `json:"id"`
		Name     string   `json:"name"`
		LengthM  *float64 `json:"length_m,omitempty"`
		CarCount *int     `json:"car_count,omitempty"`
	}
	formations := []formation{}
	for rows.Next() {
		var f formation
		if err := rows.Scan(&f.ID, &f.Name, &f.LengthM, &f.CarCount); err != nil {
			util.Error(w, 500, err.Error())
			return
		}
		formations = append(formations, f)
	}
	util.JSON(w, 200, map[string]any{
		"class":          c,
		"formations":     formations,
		"electrification": elec,
	})
}

// ListClasses returns the full set of train classes with optional filters
// applied. Powers the /train-classes search page.
//
// Query params (all optional, AND-combined):
//
//	q             substring match on class name (case-insensitive)
//	route_id      only classes that appear in at least one formation on
//	              this route (via formations.class_id ← route_formations)
//	country_id    only classes whose routes' country matches
//	propulsion    "electric" or "non-electric"
//	voltage_v     only classes whose electrification list contains this
//	              voltage (in volts; matches any frequency)
//
// Sorted by name. No paging — total class count is bounded (~100s).
func (h *FormationHandler) ListClasses(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	var (
		conditions []string
		args       []any
		joins      []string
	)

	if s := strings.TrimSpace(q.Get("q")); s != "" {
		conditions = append(conditions, "LOWER(tc.name) LIKE ?")
		args = append(args, "%"+strings.ToLower(s)+"%")
	}
	switch strings.ToLower(strings.TrimSpace(q.Get("propulsion"))) {
	case "electric":
		conditions = append(conditions, "tc.is_electric = 1")
	case "non-electric", "non_electric", "diesel":
		conditions = append(conditions, "(tc.is_electric = 0 OR tc.is_electric IS NULL)")
	}
	if v := strings.TrimSpace(q.Get("category")); v != "" {
		conditions = append(conditions, "tc.vehicle_category = ?")
		args = append(args, v)
	}
	if v := strings.TrimSpace(q.Get("voltage_v")); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			conditions = append(conditions, "EXISTS (SELECT 1 FROM train_class_electrification e WHERE e.train_class_id = tc.id AND e.voltage_v = ?)")
			args = append(args, n)
		}
	}
	if v := strings.TrimSpace(q.Get("route_id")); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			// Class is on a route when ANY formation in the class is
			// linked to that route via route_formations.
			conditions = append(conditions, `EXISTS (
				SELECT 1 FROM formations f
				JOIN route_formations rf ON rf.formation_id = f.id
				WHERE f.class_id = tc.id AND rf.route_id = ?
			)`)
			args = append(args, n)
		}
	}
	if v := strings.TrimSpace(q.Get("country_id")); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			conditions = append(conditions, `EXISTS (
				SELECT 1 FROM formations f
				JOIN route_formations rf ON rf.formation_id = f.id
				JOIN routes r ON r.id = rf.route_id
				WHERE f.class_id = tc.id AND r.country_id = ?
			)`)
			args = append(args, n)
		}
	}

	where := ""
	if len(conditions) > 0 {
		where = " WHERE " + strings.Join(conditions, " AND ")
	}
	joinClause := ""
	if len(joins) > 0 {
		joinClause = " " + strings.Join(joins, " ")
	}
	rows, err := h.db.Query("SELECT "+trainClassSelectCols+" FROM train_classes tc"+joinClause+where+" ORDER BY tc.name", args...)
	if err != nil {
		util.Error(w, 500, err.Error())
		return
	}
	defer rows.Close()
	out := []trainClass{}
	for rows.Next() {
		var c trainClass
		if err := scanTrainClass(rows, &c); err != nil {
			util.Error(w, 500, err.Error())
			return
		}
		out = append(out, c)
	}
	util.JSON(w, 200, out)
}

// RefreshClassMetadata backfills the extended fields on existing
// train_classes rows by JOINing against pak_rvds.friendly_name. Used
// after a one-time schema bump so classes created by older imports get
// their is_electric / max_speed_kph / max_power_kw / vehicle_category /
// thumbnail_path / manufacturer / descriptions filled in without
// re-importing every route's zip.
//
// Three operations, all bounded by pak_rvds and train_classes row counts
// (~hundreds each), so it runs in milliseconds.
//
// 1) For each train_classes row, pick the "richest" matching pak_rvds row
//    by FriendlyName + drivable, and COALESCE its fields onto the class.
// 2) Always (re-)set train_classes.thumbnail_path from the sanitised
//    class name, even when no PNG exists yet — the web UI handles 404.
// 3) For each class, INSERT-OR-IGNORE the matching RVD's electrification
//    specs into train_class_electrification.
//
// POST /api/train-classes/refresh-metadata
func (h *FormationHandler) RefreshClassMetadata(w http.ResponseWriter, r *http.Request) {
	type rvdRow struct {
		IsElectric        sql.NullInt64
		MaxSpeedKph       sql.NullFloat64
		MaxPowerKw        sql.NullFloat64
		ManufacturerName  sql.NullString
		EngineDescription sql.NullString
		TypeDescription   sql.NullString
		VehicleCategory   sql.NullString
		ElectrificationJSON sql.NullString
		PakPath           string
		AssetPath         string
	}
	updated := 0
	elecAdded := 0
	thumbsSet := 0

	classes, err := h.db.Query(`SELECT id, name FROM train_classes WHERE name IS NOT NULL`)
	if err != nil {
		util.Error(w, 500, err.Error())
		return
	}
	type pending struct {
		id   int
		name string
	}
	var todo []pending
	for classes.Next() {
		var p pending
		if err := classes.Scan(&p.id, &p.name); err == nil {
			todo = append(todo, p)
		}
	}
	classes.Close()

	for _, c := range todo {
		// Prefer drivable rows so the lead vehicle's traits win when a
		// class includes coaches with their own pak_rvds entries.
		var row rvdRow
		err := h.db.QueryRow(`
			SELECT is_electric, max_speed_kph, max_power_kw,
			       manufacturer_name, engine_description, type_description,
			       vehicle_category, electrification_json
			FROM pak_rvds
			WHERE friendly_name = ?
			ORDER BY drivable DESC,
			         CASE WHEN electrification_json IS NOT NULL THEN 0 ELSE 1 END,
			         CASE WHEN max_power_kw IS NOT NULL THEN 0 ELSE 1 END
			LIMIT 1`, c.name).Scan(
			&row.IsElectric, &row.MaxSpeedKph, &row.MaxPowerKw,
			&row.ManufacturerName, &row.EngineDescription, &row.TypeDescription,
			&row.VehicleCategory, &row.ElectrificationJSON)
		thumbURL := "/images/train_classes/" + pakSanitisedName(c.name) + ".png"
		if err != nil {
			// No matching pak_rvds row — still set thumbnail_path so the
			// page tries to load (and 404s gracefully) instead of showing
			// a permanent placeholder.
			if _, e := h.db.Exec(`UPDATE train_classes SET thumbnail_path = ? WHERE id = ?`, thumbURL, c.id); e == nil {
				thumbsSet++
			}
			continue
		}
		var isElec any
		if row.IsElectric.Valid {
			isElec = row.IsElectric.Int64
		}
		var maxSpd, maxPwr any
		if row.MaxSpeedKph.Valid {
			maxSpd = row.MaxSpeedKph.Float64
		}
		if row.MaxPowerKw.Valid {
			maxPwr = row.MaxPowerKw.Float64
		}
		var mfr, eng, typ, cat any
		if row.ManufacturerName.Valid {
			mfr = row.ManufacturerName.String
		}
		if row.EngineDescription.Valid {
			eng = row.EngineDescription.String
		}
		if row.TypeDescription.Valid {
			typ = row.TypeDescription.String
		}
		if row.VehicleCategory.Valid {
			cat = row.VehicleCategory.String
		}
		if _, e := h.db.Exec(`UPDATE train_classes SET
			is_electric        = COALESCE(?, is_electric),
			max_speed_kph      = COALESCE(?, max_speed_kph),
			max_power_kw       = COALESCE(?, max_power_kw),
			manufacturer_name  = COALESCE(?, manufacturer_name),
			engine_description = COALESCE(?, engine_description),
			type_description   = COALESCE(?, type_description),
			vehicle_category   = COALESCE(?, vehicle_category),
			thumbnail_path     = ?
			WHERE id = ?`,
			isElec, maxSpd, maxPwr, mfr, eng, typ, cat, thumbURL, c.id); e == nil {
			updated++
			thumbsSet++
		}
		// Electrification specs.
		if row.ElectrificationJSON.Valid && row.ElectrificationJSON.String != "" {
			var specs []struct {
				Current     string `json:"current"`
				PickupSide  string `json:"pickup_side"`
				VoltageV    int    `json:"voltage_v"`
				FrequencyHz int    `json:"frequency_hz"`
			}
			if err := json.Unmarshal([]byte(row.ElectrificationJSON.String), &specs); err == nil {
				for _, s := range specs {
					if _, e := h.db.Exec(`INSERT OR IGNORE INTO train_class_electrification
						(train_class_id, current, pickup_side, voltage_v, frequency_hz)
						VALUES (?, ?, ?, ?, ?)`,
						c.id, nilIfEmpty(s.Current), nilIfEmpty(s.PickupSide),
						s.VoltageV, s.FrequencyHz); e == nil {
						elecAdded++
					}
				}
			}
		}
	}

	util.JSON(w, 200, map[string]any{
		"classes_total":           len(todo),
		"classes_with_metadata":   updated,
		"thumbnail_paths_set":     thumbsSet,
		"electrification_rows":    elecAdded,
	})
}

// pakSanitisedName mirrors pak.SanitiseThumbnailName without importing
// the pak package (avoids a circular dep — pak imports handler today via
// the test harness). Same rules: keep [A-Za-z0-9-], turn space/underscore
// into underscore, drop the rest.
func pakSanitisedName(name string) string {
	var b strings.Builder
	for _, r := range name {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-':
			b.WriteRune(r)
		case r == ' ' || r == '_':
			b.WriteByte('_')
		}
	}
	if b.Len() == 0 {
		return "unknown"
	}
	return b.String()
}

// ListClassCategories returns the distinct vehicle_category values
// present on train_classes, sorted. Populates the Category filter
// dropdown on /train-classes so users can pick "Locomotive",
// "MultipleUnitCar", "FreightWagon", etc.
func (h *FormationHandler) ListClassCategories(w http.ResponseWriter, r *http.Request) {
	rows, err := h.db.Query(`SELECT DISTINCT vehicle_category FROM train_classes
		WHERE vehicle_category IS NOT NULL AND vehicle_category != ''
		ORDER BY vehicle_category`)
	if err != nil {
		util.Error(w, 500, err.Error())
		return
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var v string
		if err := rows.Scan(&v); err == nil {
			out = append(out, v)
		}
	}
	util.JSON(w, 200, out)
}

// ListClassVoltages returns the distinct voltages present in the
// electrification table, sorted ascending. Populates the voltage filter
// dropdown on the /train-classes page.
func (h *FormationHandler) ListClassVoltages(w http.ResponseWriter, r *http.Request) {
	rows, err := h.db.Query(`SELECT DISTINCT voltage_v FROM train_class_electrification
		WHERE voltage_v IS NOT NULL AND voltage_v > 0 ORDER BY voltage_v`)
	if err != nil {
		util.Error(w, 500, err.Error())
		return
	}
	defer rows.Close()
	var out []int
	for rows.Next() {
		var v int
		if err := rows.Scan(&v); err == nil {
			out = append(out, v)
		}
	}
	util.JSON(w, 200, out)
}

// GetVehicles returns the per-car list (consist) for a formation — from
// formation_vehicles, populated during route import. Each row is one car in
// the formation with its position, GUID (matchable to the live API), class,
// length, and flags. Empty list when the formation has no consist data.
func (h *FormationHandler) GetVehicles(w http.ResponseWriter, r *http.Request) {
	idStr := chi.URLParam(r, "id")
	id, err := strconv.Atoi(idStr)
	if err != nil {
		util.Error(w, 400, "invalid id")
		return
	}
	rows, err := h.db.Query(`
		SELECT id, position, vehicle_id, class_name, friendly_name, livery_id,
		       vehicle_category, length_m, is_lead, is_flipped
		FROM formation_vehicles
		WHERE formation_id = ?
		ORDER BY position`, id)
	if err != nil {
		util.Error(w, 500, err.Error())
		return
	}
	defer rows.Close()
	type vehicle struct {
		ID              int      `json:"id"`
		Position        int      `json:"position"`
		VehicleID       string   `json:"vehicle_id"`
		ClassName       *string  `json:"class_name,omitempty"`
		FriendlyName    *string  `json:"friendly_name,omitempty"`
		LiveryID        *string  `json:"livery_id,omitempty"`
		VehicleCategory *string  `json:"vehicle_category,omitempty"`
		LengthM         *float64 `json:"length_m,omitempty"`
		IsLead          int      `json:"is_lead"`
		IsFlipped       int      `json:"is_flipped"`
	}
	out := []vehicle{}
	for rows.Next() {
		var v vehicle
		if err := rows.Scan(&v.ID, &v.Position, &v.VehicleID, &v.ClassName, &v.FriendlyName,
			&v.LiveryID, &v.VehicleCategory, &v.LengthM, &v.IsLead, &v.IsFlipped); err != nil {
			util.Error(w, 500, err.Error())
			return
		}
		out = append(out, v)
	}
	util.JSON(w, 200, out)
}

func (h *FormationHandler) Update(w http.ResponseWriter, r *http.Request) {
	idStr := chi.URLParam(r, "id")
	id, err := strconv.Atoi(idStr)
	if err != nil {
		util.Error(w, 400, "invalid id")
		return
	}

	// Fetch existing formation
	var existing models.Formation
	err = h.db.QueryRow("SELECT id, name FROM formations WHERE id = ?", id).Scan(&existing.ID, &existing.Name)
	if err == sql.ErrNoRows {
		util.Error(w, 404, "Formation not found")
		return
	}
	if err != nil {
		util.Error(w, 500, err.Error())
		return
	}

	var body struct {
		Name string `json:"name"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		util.Error(w, 400, "invalid JSON body")
		return
	}

	// Check if another formation with this name already exists (excluding current)
	if body.Name != "" {
		var dupID int
		err = h.db.QueryRow("SELECT id FROM formations WHERE name = ?", body.Name).Scan(&dupID)
		if err == nil && dupID != id {
			util.Error(w, 409, "A formation with this name already exists")
			return
		}
	}

	name := body.Name
	if name == "" {
		name = existing.Name
	}

	_, err = h.db.Exec("UPDATE formations SET name = ? WHERE id = ?", name, id)
	if err != nil {
		util.Error(w, 500, err.Error())
		return
	}

	util.JSON(w, 200, map[string]any{"id": id, "name": name})
}

func (h *FormationHandler) Delete(w http.ResponseWriter, r *http.Request) {
	idStr := chi.URLParam(r, "id")
	id, err := strconv.Atoi(idStr)
	if err != nil {
		util.Error(w, 400, "invalid id")
		return
	}

	_, err = h.db.Exec("DELETE FROM formations WHERE id = ?", id)
	if err != nil {
		util.Error(w, 500, err.Error())
		return
	}

	util.JSON(w, 200, map[string]bool{"success": true})
}

func (h *FormationHandler) GetRoutes(w http.ResponseWriter, r *http.Request) {
	idStr := chi.URLParam(r, "id")
	id, err := strconv.Atoi(idStr)
	if err != nil {
		util.Error(w, 400, "invalid id")
		return
	}

	rows, err := h.db.Query(`
		SELECT DISTINCT r.id, r.name, r.country_id, r.tsw_version
		FROM routes r
		INNER JOIN route_formations rt ON r.id = rt.route_id
		WHERE rt.formation_id = ?
		ORDER BY r.name
	`, id)
	if err != nil {
		util.Error(w, 500, err.Error())
		return
	}
	defer rows.Close()

	var items []models.Route
	for rows.Next() {
		var rt models.Route
		if err := rows.Scan(&rt.ID, &rt.Name, &rt.CountryID, &rt.TswVersion); err != nil {
			util.Error(w, 500, err.Error())
			return
		}
		items = append(items, rt)
	}
	if items == nil {
		items = []models.Route{}
	}
	util.JSON(w, 200, items)
}
