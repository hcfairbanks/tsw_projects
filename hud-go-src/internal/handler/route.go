package handler

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"

	"hud-go/internal/util"
)

type RouteHandler struct {
	db *sql.DB
}

// ---------- helpers ----------

// parseID extracts an int URL parameter or writes a 400 error.
func parseID(w http.ResponseWriter, r *http.Request, param string) (int, bool) {
	raw := chi.URLParam(r, param)
	id, err := strconv.Atoi(raw)
	if err != nil {
		util.Error(w, http.StatusBadRequest, fmt.Sprintf("invalid %s: %s", param, raw))
		return 0, false
	}
	return id, true
}

// decodeBody decodes a JSON request body into dst.
func decodeBody(w http.ResponseWriter, r *http.Request, dst any) bool {
	if err := json.NewDecoder(r.Body).Decode(dst); err != nil {
		util.Error(w, http.StatusBadRequest, "invalid JSON body: "+err.Error())
		return false
	}
	return true
}

// scanRows is a small helper that scans all rows into a slice of maps.
func scanRowsToMaps(rows *sql.Rows) ([]map[string]any, error) {
	cols, err := rows.Columns()
	if err != nil {
		return nil, err
	}
	var results []map[string]any
	for rows.Next() {
		vals := make([]any, len(cols))
		ptrs := make([]any, len(cols))
		for i := range vals {
			ptrs[i] = &vals[i]
		}
		if err := rows.Scan(ptrs...); err != nil {
			return nil, err
		}
		row := make(map[string]any, len(cols))
		for i, col := range cols {
			v := vals[i]
			// Convert []byte to string for JSON friendliness.
			if b, ok := v.([]byte); ok {
				row[col] = string(b)
			} else {
				row[col] = v
			}
		}
		results = append(results, row)
	}
	if results == nil {
		results = []map[string]any{}
	}
	return results, rows.Err()
}

// ---------- 1. GetAll ----------

func (h *RouteHandler) GetAll(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	search := strings.TrimSpace(q.Get("search"))
	countryIDStr := q.Get("country_id")
	limitStr := q.Get("limit")

	var conditions []string
	var args []any
	if search != "" {
		conditions = append(conditions, "(r.name LIKE ? OR c.name LIKE ?)")
		pat := "%" + search + "%"
		args = append(args, pat, pat)
	}
	if countryIDStr != "" {
		if cid, err := strconv.Atoi(countryIDStr); err == nil {
			conditions = append(conditions, "r.country_id = ?")
			args = append(args, cid)
		}
	}
	where := ""
	if len(conditions) > 0 {
		where = " WHERE " + strings.Join(conditions, " AND ")
	}
	limitClause := ""
	if limit, _ := strconv.Atoi(limitStr); limit > 0 {
		limitClause = fmt.Sprintf(" LIMIT %d", limit)
	}

	rows, err := h.db.Query(`
		SELECT r.*, c.name AS country_name
		FROM routes r
		LEFT JOIN countries c ON r.country_id = c.id`+where+`
		ORDER BY r.name`+limitClause, args...)
	if err != nil {
		util.Error(w, http.StatusInternalServerError, err.Error())
		return
	}
	defer rows.Close()

	results, err := scanRowsToMaps(rows)
	if err != nil {
		util.Error(w, http.StatusInternalServerError, err.Error())
		return
	}
	util.Success(w, results)
}

// ---------- 2. GetPaginated ----------

func (h *RouteHandler) GetPaginated(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	page, _ := strconv.Atoi(q.Get("page"))
	if page < 1 {
		page = 1
	}
	limit, _ := strconv.Atoi(q.Get("limit"))
	if limit < 1 {
		limit = 10
	}
	search := q.Get("search")
	countryIDStr := q.Get("country_id")

	var conditions []string
	var args []any

	if search != "" {
		conditions = append(conditions, "name LIKE ?")
		args = append(args, "%"+search+"%")
	}
	if countryIDStr != "" {
		cid, err := strconv.Atoi(countryIDStr)
		if err == nil {
			conditions = append(conditions, "country_id = ?")
			args = append(args, cid)
		}
	}

	where := ""
	if len(conditions) > 0 {
		where = " WHERE " + strings.Join(conditions, " AND ")
	}

	// Total count
	var total int
	countQuery := "SELECT COUNT(*) FROM routes" + where
	if err := h.db.QueryRow(countQuery, args...).Scan(&total); err != nil {
		util.Error(w, http.StatusInternalServerError, err.Error())
		return
	}

	// Data
	offset := (page - 1) * limit
	dataArgs := append(append([]any{}, args...), limit, offset)
	dataQuery := "SELECT * FROM routes" + where + " ORDER BY id DESC LIMIT ? OFFSET ?"
	rows, err := h.db.Query(dataQuery, dataArgs...)
	if err != nil {
		util.Error(w, http.StatusInternalServerError, err.Error())
		return
	}
	defer rows.Close()

	results, err := scanRowsToMaps(rows)
	if err != nil {
		util.Error(w, http.StatusInternalServerError, err.Error())
		return
	}

	util.Success(w, map[string]any{
		"data": results,
		"pagination": map[string]any{
			"page":       page,
			"limit":      limit,
			"total":      total,
			"totalPages": int(math.Ceil(float64(total) / float64(limit))),
		},
	})
}

// ---------- 3. GetWithCoordinates ----------

func (h *RouteHandler) GetWithCoordinates(w http.ResponseWriter, r *http.Request) {
	rows, err := h.db.Query(`
		SELECT DISTINCT r.*, c.name AS country_name
		FROM routes r
		LEFT JOIN countries c ON r.country_id = c.id
		INNER JOIN timetables t ON t.route_id = r.id
		ORDER BY r.name`)
	if err != nil {
		util.Error(w, http.StatusInternalServerError, err.Error())
		return
	}
	defer rows.Close()

	results, err := scanRowsToMaps(rows)
	if err != nil {
		util.Error(w, http.StatusInternalServerError, err.Error())
		return
	}
	util.Success(w, results)
}

// ---------- 4. GetByName ----------

func (h *RouteHandler) GetByName(w http.ResponseWriter, r *http.Request) {
	name := r.URL.Query().Get("name")
	if name == "" {
		util.Error(w, http.StatusBadRequest, "Missing required query parameter: name")
		return
	}

	rows, err := h.db.Query("SELECT * FROM routes WHERE name = ?", name)
	if err != nil {
		util.Error(w, http.StatusInternalServerError, err.Error())
		return
	}
	defer rows.Close()

	results, err := scanRowsToMaps(rows)
	if err != nil {
		util.Error(w, http.StatusInternalServerError, err.Error())
		return
	}
	if len(results) == 0 {
		util.Error(w, http.StatusNotFound, fmt.Sprintf("No route found with name \"%s\"", name))
		return
	}
	util.Success(w, results[0])
}

// ---------- 5. Create ----------

func (h *RouteHandler) Create(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Name       string `json:"name"`
		CountryID  int    `json:"country_id"`
		TswVersion *int   `json:"tsw_version"`
	}
	if !decodeBody(w, r, &body) {
		return
	}

	tswVersion := 3
	if body.TswVersion != nil {
		tswVersion = *body.TswVersion
	}

	// Check uniqueness
	var exists int
	err := h.db.QueryRow("SELECT COUNT(*) FROM routes WHERE name = ?", body.Name).Scan(&exists)
	if err != nil {
		util.Error(w, http.StatusInternalServerError, err.Error())
		return
	}
	if exists > 0 {
		util.Error(w, http.StatusConflict, "A route with this name already exists")
		return
	}

	res, err := h.db.Exec("INSERT INTO routes (name, country_id, tsw_version) VALUES (?, ?, ?)",
		body.Name, body.CountryID, tswVersion)
	if err != nil {
		util.Error(w, http.StatusInternalServerError, err.Error())
		return
	}
	id, _ := res.LastInsertId()

	util.JSON(w, http.StatusCreated, map[string]any{
		"id":          id,
		"name":        body.Name,
		"country_id":  body.CountryID,
		"tsw_version": tswVersion,
	})
}

// ---------- 6. GetByID ----------

func (h *RouteHandler) GetByID(w http.ResponseWriter, r *http.Request) {
	id, ok := parseID(w, r, "id")
	if !ok {
		return
	}

	rows, err := h.db.Query(`
		SELECT r.*, c.name AS country_name
		FROM routes r
		LEFT JOIN countries c ON r.country_id = c.id
		WHERE r.id = ?`, id)
	if err != nil {
		util.Error(w, http.StatusInternalServerError, err.Error())
		return
	}
	defer rows.Close()

	results, err := scanRowsToMaps(rows)
	if err != nil {
		util.Error(w, http.StatusInternalServerError, err.Error())
		return
	}
	if len(results) == 0 {
		util.Error(w, http.StatusNotFound, "Route not found")
		return
	}
	util.Success(w, results[0])
}

// ---------- 7. Update ----------

func (h *RouteHandler) Update(w http.ResponseWriter, r *http.Request) {
	id, ok := parseID(w, r, "id")
	if !ok {
		return
	}

	var body struct {
		Name       string `json:"name"`
		CountryID  int    `json:"country_id"`
		TswVersion int    `json:"tsw_version"`
	}
	if !decodeBody(w, r, &body) {
		return
	}

	// Check uniqueness (excluding self)
	var existingID int
	err := h.db.QueryRow("SELECT id FROM routes WHERE name = ?", body.Name).Scan(&existingID)
	if err == nil && existingID != id {
		util.Error(w, http.StatusConflict, "A route with this name already exists")
		return
	}

	_, err = h.db.Exec("UPDATE routes SET name = ?, country_id = ?, tsw_version = ? WHERE id = ?",
		body.Name, body.CountryID, body.TswVersion, id)
	if err != nil {
		util.Error(w, http.StatusInternalServerError, err.Error())
		return
	}

	util.Success(w, map[string]any{
		"id":          id,
		"name":        body.Name,
		"country_id":  body.CountryID,
		"tsw_version": body.TswVersion,
	})
}

// ---------- 8. Delete ----------

// Delete cascades the route deletion through every dependent table.
//
// `timetables.route_id` uses `ON DELETE SET NULL` (so timetables otherwise
// survive a route delete and orphan), but the user-facing intent of "delete
// route" is "wipe everything tied to this route" — so we explicitly delete
// the timetables here first. timetable_entries / timetable_formations /
// timetable_sections / timetable_coordinates / timetable_markers /
// train_consists all use `ON DELETE CASCADE` on timetable_id and clean up
// automatically.
//
// route_coordinates / route_markers / route_locations / route_formations /
// locations / station_name_mappings / sections / car_stop_signs /
// track_markers all use `ON DELETE CASCADE` on route_id and are removed
// when the route row itself is deleted at the end.
//
// The frontend gates this with a confirmation dialog naming the timetable
// count so a click-through delete is unambiguous.
func (h *RouteHandler) Delete(w http.ResponseWriter, r *http.Request) {
	id, ok := parseID(w, r, "id")
	if !ok {
		return
	}

	if _, err := h.db.Exec("DELETE FROM timetables WHERE route_id = ?", id); err != nil {
		util.Error(w, http.StatusInternalServerError, err.Error())
		return
	}

	if _, err := h.db.Exec("DELETE FROM routes WHERE id = ?", id); err != nil {
		util.Error(w, http.StatusInternalServerError, err.Error())
		return
	}

	deleteOrphanFormations(h.db)

	util.Success(w, map[string]any{"success": true})
}

// deleteOrphanFormations removes formations rows that no other table references.
// Cleans up after route/timetable deletion so re-imports don't reuse stale
// formation rows by name.
func deleteOrphanFormations(db *sql.DB) {
	db.Exec(`
		DELETE FROM formations
		WHERE id NOT IN (SELECT formation_id FROM route_formations WHERE formation_id IS NOT NULL)
		  AND id NOT IN (SELECT formation_id FROM timetable_formations WHERE formation_id IS NOT NULL)
		  AND id NOT IN (SELECT formation_id FROM section_formations WHERE formation_id IS NOT NULL)
		  AND id NOT IN (SELECT formation_id FROM timetables WHERE formation_id IS NOT NULL)
	`)
	// Classes whose only members were just-deleted orphans are now empty —
	// drop them too so the typeahead doesn't surface dead class entries.
	db.Exec(`
		DELETE FROM train_classes
		WHERE id NOT IN (SELECT class_id FROM formations WHERE class_id IS NOT NULL)
	`)
}

// GetTrainClasses returns the train CLASSES used on a route, deduped from
// the per-formation route_formations rows. Each item carries the class summary
// and the list of formation IDs (so the UI can drill from class → formation
// without an extra round-trip per class).
func (h *RouteHandler) GetTrainClasses(w http.ResponseWriter, r *http.Request) {
	id, ok := parseID(w, r, "id")
	if !ok {
		return
	}
	rows, err := h.db.Query(`
		SELECT
			COALESCE(tc.id, -t.id) AS class_key,
			COALESCE(tc.name, t.name) AS class_name,
			COALESCE(tc.livery_id, t.livery_id) AS livery_id,
			COALESCE(tc.typical_length_m, t.length_m) AS length_m,
			COALESCE(tc.typical_car_count, t.car_count) AS car_count,
			t.id AS formation_id, t.name AS formation_name
		FROM formations t
		INNER JOIN route_formations rt ON t.id = rt.formation_id
		LEFT JOIN train_classes tc ON tc.id = t.class_id
		WHERE rt.route_id = ?
		ORDER BY class_name, formation_name`, id)
	if err != nil {
		util.Error(w, http.StatusInternalServerError, err.Error())
		return
	}
	defer rows.Close()
	type formation struct {
		ID   int    `json:"id"`
		Name string `json:"name"`
	}
	type classGroup struct {
		ID         *int        `json:"id"` // null when no train_classes row exists yet (legacy data)
		Name       string      `json:"name"`
		LiveryID   *string     `json:"livery_id,omitempty"`
		LengthM    *float64    `json:"length_m,omitempty"`
		CarCount   *int        `json:"car_count,omitempty"`
		Formations []formation `json:"formations"`
	}
	groups := map[int]*classGroup{}
	order := []int{}
	for rows.Next() {
		var key int
		var name string
		var livery *string
		var lengthM *float64
		var carCount *int
		var fid int
		var fname string
		if err := rows.Scan(&key, &name, &livery, &lengthM, &carCount, &fid, &fname); err != nil {
			util.Error(w, http.StatusInternalServerError, err.Error())
			return
		}
		g, ok := groups[key]
		if !ok {
			g = &classGroup{Name: name, LiveryID: livery, LengthM: lengthM, CarCount: carCount, Formations: []formation{}}
			if key > 0 {
				k := key
				g.ID = &k
			}
			groups[key] = g
			order = append(order, key)
		}
		g.Formations = append(g.Formations, formation{ID: fid, Name: fname})
	}
	out := make([]*classGroup, 0, len(order))
	for _, k := range order {
		out = append(out, groups[k])
	}
	util.JSON(w, http.StatusOK, out)
}

// ---------- 9. GetFormations ----------

func (h *RouteHandler) GetFormations(w http.ResponseWriter, r *http.Request) {
	id, ok := parseID(w, r, "id")
	if !ok {
		return
	}

	rows, err := h.db.Query(`
		SELECT t.* FROM formations t
		INNER JOIN route_formations rt ON t.id = rt.formation_id
		WHERE rt.route_id = ?
		ORDER BY t.name`, id)
	if err != nil {
		util.Error(w, http.StatusInternalServerError, err.Error())
		return
	}
	defer rows.Close()

	results, err := scanRowsToMaps(rows)
	if err != nil {
		util.Error(w, http.StatusInternalServerError, err.Error())
		return
	}
	util.Success(w, results)
}

// ---------- 10. AddFormation ----------

func (h *RouteHandler) AddFormation(w http.ResponseWriter, r *http.Request) {
	id, ok := parseID(w, r, "id")
	if !ok {
		return
	}

	var body struct {
		FormationID int `json:"formation_id"`
	}
	if !decodeBody(w, r, &body) {
		return
	}

	_, err := h.db.Exec("INSERT OR IGNORE INTO route_formations (route_id, formation_id) VALUES (?, ?)", id, body.FormationID)
	if err != nil {
		util.Error(w, http.StatusInternalServerError, err.Error())
		return
	}
	util.JSON(w, http.StatusCreated, map[string]any{"success": true})
}

// ---------- 11. RemoveFormation ----------

func (h *RouteHandler) RemoveFormation(w http.ResponseWriter, r *http.Request) {
	id, ok := parseID(w, r, "id")
	if !ok {
		return
	}

	var body struct {
		FormationID int `json:"formation_id"`
	}
	if !decodeBody(w, r, &body) {
		return
	}

	_, err := h.db.Exec("DELETE FROM route_formations WHERE route_id = ? AND formation_id = ?", id, body.FormationID)
	if err != nil {
		util.Error(w, http.StatusInternalServerError, err.Error())
		return
	}
	util.Success(w, map[string]any{"success": true})
}

// ---------- 12. RemoveFormationByID ----------

func (h *RouteHandler) RemoveFormationByID(w http.ResponseWriter, r *http.Request) {
	routeID, ok := parseID(w, r, "id")
	if !ok {
		return
	}
	formationID, ok := parseID(w, r, "formationId")
	if !ok {
		return
	}

	_, err := h.db.Exec("DELETE FROM route_formations WHERE route_id = ? AND formation_id = ?", routeID, formationID)
	if err != nil {
		util.Error(w, http.StatusInternalServerError, err.Error())
		return
	}
	util.Success(w, map[string]any{"success": true})
}

// ---------- 13. GetSections ----------

func (h *RouteHandler) GetSections(w http.ResponseWriter, r *http.Request) {
	id, ok := parseID(w, r, "id")
	if !ok {
		return
	}

	rows, err := h.db.Query("SELECT * FROM sections WHERE route_id = ? ORDER BY name", id)
	if err != nil {
		util.Error(w, http.StatusInternalServerError, err.Error())
		return
	}
	defer rows.Close()

	results, err := scanRowsToMaps(rows)
	if err != nil {
		util.Error(w, http.StatusInternalServerError, err.Error())
		return
	}
	util.Success(w, results)
}

// ---------- 14. CreateSection ----------

func (h *RouteHandler) CreateSection(w http.ResponseWriter, r *http.Request) {
	routeID, ok := parseID(w, r, "id")
	if !ok {
		return
	}

	var body struct {
		Name string `json:"name"`
	}
	if !decodeBody(w, r, &body) {
		return
	}

	name := strings.TrimSpace(body.Name)
	if name == "" {
		util.Error(w, http.StatusBadRequest, "Section name is required")
		return
	}

	// Check uniqueness on this route
	var exists int
	err := h.db.QueryRow("SELECT COUNT(*) FROM sections WHERE route_id = ? AND name = ?", routeID, name).Scan(&exists)
	if err != nil {
		util.Error(w, http.StatusInternalServerError, err.Error())
		return
	}
	if exists > 0 {
		util.Error(w, http.StatusConflict, "A section with this name already exists on this route")
		return
	}

	res, err := h.db.Exec("INSERT INTO sections (route_id, name) VALUES (?, ?)", routeID, name)
	if err != nil {
		util.Error(w, http.StatusInternalServerError, err.Error())
		return
	}
	secID, _ := res.LastInsertId()

	util.JSON(w, http.StatusCreated, map[string]any{
		"id":       secID,
		"route_id": routeID,
		"name":     name,
	})
}

// ---------- 15. UpdateSection ----------

func (h *RouteHandler) UpdateSection(w http.ResponseWriter, r *http.Request) {
	routeID, ok := parseID(w, r, "id")
	if !ok {
		return
	}
	sectionID, ok := parseID(w, r, "sectionId")
	if !ok {
		return
	}

	var body struct {
		Name string `json:"name"`
	}
	if !decodeBody(w, r, &body) {
		return
	}

	name := strings.TrimSpace(body.Name)
	if name == "" {
		util.Error(w, http.StatusBadRequest, "Section name is required")
		return
	}

	// Verify section exists and belongs to route
	var secRouteID int
	err := h.db.QueryRow("SELECT route_id FROM sections WHERE id = ?", sectionID).Scan(&secRouteID)
	if err == sql.ErrNoRows || secRouteID != routeID {
		util.Error(w, http.StatusNotFound, "Section not found")
		return
	}
	if err != nil {
		util.Error(w, http.StatusInternalServerError, err.Error())
		return
	}

	// Check uniqueness (excluding self)
	var existingID int
	err = h.db.QueryRow("SELECT id FROM sections WHERE route_id = ? AND name = ?", routeID, name).Scan(&existingID)
	if err == nil && existingID != sectionID {
		util.Error(w, http.StatusConflict, "A section with this name already exists on this route")
		return
	}

	_, err = h.db.Exec("UPDATE sections SET name = ? WHERE id = ?", name, sectionID)
	if err != nil {
		util.Error(w, http.StatusInternalServerError, err.Error())
		return
	}

	util.Success(w, map[string]any{
		"id":       sectionID,
		"route_id": routeID,
		"name":     name,
	})
}

// ---------- 16. DeleteSection ----------

func (h *RouteHandler) DeleteSection(w http.ResponseWriter, r *http.Request) {
	routeID, ok := parseID(w, r, "id")
	if !ok {
		return
	}
	sectionID, ok := parseID(w, r, "sectionId")
	if !ok {
		return
	}

	// Verify section exists and belongs to route
	var secRouteID int
	err := h.db.QueryRow("SELECT route_id FROM sections WHERE id = ?", sectionID).Scan(&secRouteID)
	if err == sql.ErrNoRows || secRouteID != routeID {
		util.Error(w, http.StatusNotFound, "Section not found")
		return
	}
	if err != nil {
		util.Error(w, http.StatusInternalServerError, err.Error())
		return
	}

	// Clear section_id from timetables referencing this section
	h.db.Exec("UPDATE timetables SET section_id = NULL WHERE section_id = ?", sectionID)

	_, err = h.db.Exec("DELETE FROM sections WHERE id = ?", sectionID)
	if err != nil {
		util.Error(w, http.StatusInternalServerError, err.Error())
		return
	}
	util.Success(w, map[string]any{"success": true})
}

// ---------- 17. GetSectionFormations ----------

func (h *RouteHandler) GetSectionFormations(w http.ResponseWriter, r *http.Request) {
	routeID, ok := parseID(w, r, "id")
	if !ok {
		return
	}
	sectionID, ok := parseID(w, r, "sectionId")
	if !ok {
		return
	}

	// Verify section belongs to route
	var secRouteID int
	err := h.db.QueryRow("SELECT route_id FROM sections WHERE id = ?", sectionID).Scan(&secRouteID)
	if err == sql.ErrNoRows || secRouteID != routeID {
		util.Error(w, http.StatusNotFound, "Section not found")
		return
	}
	if err != nil {
		util.Error(w, http.StatusInternalServerError, err.Error())
		return
	}

	rows, err := h.db.Query(`
		SELECT t.* FROM formations t
		INNER JOIN section_formations st ON t.id = st.formation_id
		WHERE st.section_id = ?
		ORDER BY t.name`, sectionID)
	if err != nil {
		util.Error(w, http.StatusInternalServerError, err.Error())
		return
	}
	defer rows.Close()

	results, err := scanRowsToMaps(rows)
	if err != nil {
		util.Error(w, http.StatusInternalServerError, err.Error())
		return
	}
	util.Success(w, results)
}

// ---------- 18. AddSectionFormation ----------

func (h *RouteHandler) AddSectionFormation(w http.ResponseWriter, r *http.Request) {
	routeID, ok := parseID(w, r, "id")
	if !ok {
		return
	}
	sectionID, ok := parseID(w, r, "sectionId")
	if !ok {
		return
	}

	// Verify section belongs to route
	var secRouteID int
	err := h.db.QueryRow("SELECT route_id FROM sections WHERE id = ?", sectionID).Scan(&secRouteID)
	if err == sql.ErrNoRows || secRouteID != routeID {
		util.Error(w, http.StatusNotFound, "Section not found")
		return
	}
	if err != nil {
		util.Error(w, http.StatusInternalServerError, err.Error())
		return
	}

	var body struct {
		FormationID int `json:"formation_id"`
	}
	if !decodeBody(w, r, &body) {
		return
	}
	if body.FormationID == 0 {
		util.Error(w, http.StatusBadRequest, "formation_id is required")
		return
	}

	_, err = h.db.Exec("INSERT OR IGNORE INTO section_formations (section_id, formation_id) VALUES (?, ?)", sectionID, body.FormationID)
	if err != nil {
		util.Error(w, http.StatusInternalServerError, err.Error())
		return
	}
	util.JSON(w, http.StatusCreated, map[string]any{"success": true})
}

// ---------- 19. RemoveSectionFormation ----------

func (h *RouteHandler) RemoveSectionFormation(w http.ResponseWriter, r *http.Request) {
	routeID, ok := parseID(w, r, "id")
	if !ok {
		return
	}
	sectionID, ok := parseID(w, r, "sectionId")
	if !ok {
		return
	}
	formationID, ok := parseID(w, r, "formationId")
	if !ok {
		return
	}

	// Verify section belongs to route
	var secRouteID int
	err := h.db.QueryRow("SELECT route_id FROM sections WHERE id = ?", sectionID).Scan(&secRouteID)
	if err == sql.ErrNoRows || secRouteID != routeID {
		util.Error(w, http.StatusNotFound, "Section not found")
		return
	}
	if err != nil {
		util.Error(w, http.StatusInternalServerError, err.Error())
		return
	}

	_, err = h.db.Exec("DELETE FROM section_formations WHERE section_id = ? AND formation_id = ?", sectionID, formationID)
	if err != nil {
		util.Error(w, http.StatusInternalServerError, err.Error())
		return
	}
	util.Success(w, map[string]any{"success": true})
}

// ---------- 20. GetFormationsWithCoordinates ----------

func (h *RouteHandler) GetFormationsWithCoordinates(w http.ResponseWriter, r *http.Request) {
	id, ok := parseID(w, r, "id")
	if !ok {
		return
	}

	rows, err := h.db.Query(`
		SELECT DISTINCT tr.* FROM formations tr
		INNER JOIN route_formations rt ON tr.id = rt.formation_id
		INNER JOIN timetable_formations tt ON tt.formation_id = tr.id
		INNER JOIN timetables t ON t.id = tt.timetable_id AND t.route_id = ?
		WHERE rt.route_id = ?
		ORDER BY tr.name`, id, id)
	if err != nil {
		util.Error(w, http.StatusInternalServerError, err.Error())
		return
	}
	defer rows.Close()

	results, err := scanRowsToMaps(rows)
	if err != nil {
		util.Error(w, http.StatusInternalServerError, err.Error())
		return
	}
	util.Success(w, results)
}

// ---------- 21. GetMapData ----------

func (h *RouteHandler) GetMapData(w http.ResponseWriter, r *http.Request) {
	id, ok := parseID(w, r, "id")
	if !ok {
		return
	}

	// Verify route exists
	var routeName string
	err := h.db.QueryRow("SELECT name FROM routes WHERE id = ?", id).Scan(&routeName)
	if err == sql.ErrNoRows {
		util.Error(w, http.StatusNotFound, "Route not found")
		return
	}
	if err != nil {
		util.Error(w, http.StatusInternalServerError, err.Error())
		return
	}

	// Coordinates - stored as JSON string, parse it
	var coordinates any
	var coordStr sql.NullString
	err = h.db.QueryRow("SELECT coordinates FROM route_coordinates WHERE route_id = ?", id).Scan(&coordStr)
	if err == sql.ErrNoRows || !coordStr.Valid {
		coordinates = []any{}
	} else if err != nil {
		util.Error(w, http.StatusInternalServerError, err.Error())
		return
	} else {
		if jsonErr := json.Unmarshal([]byte(coordStr.String), &coordinates); jsonErr != nil {
			coordinates = []any{}
		}
	}

	// Markers
	markerRows, err := h.db.Query("SELECT * FROM route_markers WHERE route_id = ? ORDER BY station_name", id)
	if err != nil {
		util.Error(w, http.StatusInternalServerError, err.Error())
		return
	}
	defer markerRows.Close()
	markers, err := scanRowsToMaps(markerRows)
	if err != nil {
		util.Error(w, http.StatusInternalServerError, err.Error())
		return
	}

	// Locations
	locRows, err := h.db.Query("SELECT * FROM route_locations WHERE route_id = ? ORDER BY name, bound, platform", id)
	if err != nil {
		util.Error(w, http.StatusInternalServerError, err.Error())
		return
	}
	defer locRows.Close()
	locations, err := scanRowsToMaps(locRows)
	if err != nil {
		util.Error(w, http.StatusInternalServerError, err.Error())
		return
	}

	util.Success(w, map[string]any{
		"route_id":    id,
		"route_name":  routeName,
		"coordinates": coordinates,
		"markers":     markers,
		"locations":   locations,
	})
}

// ---------- 22. ClearMapData ----------

func (h *RouteHandler) ClearMapData(w http.ResponseWriter, r *http.Request) {
	id, ok := parseID(w, r, "id")
	if !ok {
		return
	}

	// Verify route exists
	var exists int
	err := h.db.QueryRow("SELECT COUNT(*) FROM routes WHERE id = ?", id).Scan(&exists)
	if err != nil {
		util.Error(w, http.StatusInternalServerError, err.Error())
		return
	}
	if exists == 0 {
		util.Error(w, http.StatusNotFound, "Route not found")
		return
	}

	h.db.Exec("DELETE FROM route_coordinates WHERE route_id = ?", id)
	h.db.Exec("DELETE FROM route_markers WHERE route_id = ?", id)
	h.db.Exec("DELETE FROM route_locations WHERE route_id = ?", id)

	util.Success(w, map[string]any{"success": true})
}

// ---------- 23. DeleteAllTimetables ----------

func (h *RouteHandler) DeleteAllTimetables(w http.ResponseWriter, r *http.Request) {
	id, ok := parseID(w, r, "id")
	if !ok {
		return
	}

	// Collect timetable IDs for this route
	rows, err := h.db.Query("SELECT id FROM timetables WHERE route_id = ?", id)
	if err != nil {
		util.Error(w, http.StatusInternalServerError, err.Error())
		return
	}
	var ttIDs []int
	for rows.Next() {
		var ttID int
		rows.Scan(&ttID)
		ttIDs = append(ttIDs, ttID)
	}
	rows.Close()

	// Delete related rows for each timetable
	for _, ttID := range ttIDs {
		h.db.Exec("DELETE FROM timetable_formations WHERE timetable_id = ?", ttID)
		h.db.Exec("DELETE FROM timetable_sections WHERE timetable_id = ?", ttID)
		h.db.Exec("DELETE FROM timetable_entries WHERE timetable_id = ?", ttID)
		h.db.Exec("DELETE FROM timetable_coordinates WHERE timetable_id = ?", ttID)
		h.db.Exec("DELETE FROM timetable_markers WHERE timetable_id = ?", ttID)
		h.db.Exec("DELETE FROM timetable_map_features WHERE timetable_id = ?", ttID)
	}

	// Delete the timetables themselves
	h.db.Exec("DELETE FROM timetables WHERE route_id = ?", id)

	// Sweep orphan formations: rows no longer reachable from any route_formations,
	// timetable_formations, section_formations, or timetables.formation_id. Generic
	// formation names like "PlayerFormation" become available for the
	// next route's import without colliding by name.
	deleteOrphanFormations(h.db)

	util.Success(w, map[string]any{"success": true, "deleted": len(ttIDs)})
}

