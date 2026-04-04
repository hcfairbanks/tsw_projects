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
	rows, err := h.db.Query(`
		SELECT r.*, c.name AS country_name
		FROM routes r
		LEFT JOIN countries c ON r.country_id = c.id
		ORDER BY r.id DESC`)
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

func (h *RouteHandler) Delete(w http.ResponseWriter, r *http.Request) {
	id, ok := parseID(w, r, "id")
	if !ok {
		return
	}

	// Check for FK references from timetables
	var ttCount int
	if err := h.db.QueryRow("SELECT COUNT(*) FROM timetables WHERE route_id = ?", id).Scan(&ttCount); err != nil {
		util.Error(w, http.StatusInternalServerError, err.Error())
		return
	}
	if ttCount > 0 {
		util.Error(w, http.StatusConflict, "Cannot delete route: timetables still reference it")
		return
	}

	_, err := h.db.Exec("DELETE FROM routes WHERE id = ?", id)
	if err != nil {
		util.Error(w, http.StatusInternalServerError, err.Error())
		return
	}
	util.Success(w, map[string]any{"success": true})
}

// ---------- 9. GetTrains ----------

func (h *RouteHandler) GetTrains(w http.ResponseWriter, r *http.Request) {
	id, ok := parseID(w, r, "id")
	if !ok {
		return
	}

	rows, err := h.db.Query(`
		SELECT t.* FROM trains t
		INNER JOIN route_trains rt ON t.id = rt.train_id
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

// ---------- 10. AddTrain ----------

func (h *RouteHandler) AddTrain(w http.ResponseWriter, r *http.Request) {
	id, ok := parseID(w, r, "id")
	if !ok {
		return
	}

	var body struct {
		TrainID int `json:"train_id"`
	}
	if !decodeBody(w, r, &body) {
		return
	}

	_, err := h.db.Exec("INSERT OR IGNORE INTO route_trains (route_id, train_id) VALUES (?, ?)", id, body.TrainID)
	if err != nil {
		util.Error(w, http.StatusInternalServerError, err.Error())
		return
	}
	util.JSON(w, http.StatusCreated, map[string]any{"success": true})
}

// ---------- 11. RemoveTrain ----------

func (h *RouteHandler) RemoveTrain(w http.ResponseWriter, r *http.Request) {
	id, ok := parseID(w, r, "id")
	if !ok {
		return
	}

	var body struct {
		TrainID int `json:"train_id"`
	}
	if !decodeBody(w, r, &body) {
		return
	}

	_, err := h.db.Exec("DELETE FROM route_trains WHERE route_id = ? AND train_id = ?", id, body.TrainID)
	if err != nil {
		util.Error(w, http.StatusInternalServerError, err.Error())
		return
	}
	util.Success(w, map[string]any{"success": true})
}

// ---------- 12. RemoveTrainByID ----------

func (h *RouteHandler) RemoveTrainByID(w http.ResponseWriter, r *http.Request) {
	routeID, ok := parseID(w, r, "id")
	if !ok {
		return
	}
	trainID, ok := parseID(w, r, "trainId")
	if !ok {
		return
	}

	_, err := h.db.Exec("DELETE FROM route_trains WHERE route_id = ? AND train_id = ?", routeID, trainID)
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

// ---------- 17. GetSectionTrains ----------

func (h *RouteHandler) GetSectionTrains(w http.ResponseWriter, r *http.Request) {
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
		SELECT t.* FROM trains t
		INNER JOIN section_trains st ON t.id = st.train_id
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

// ---------- 18. AddSectionTrain ----------

func (h *RouteHandler) AddSectionTrain(w http.ResponseWriter, r *http.Request) {
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
		TrainID int `json:"train_id"`
	}
	if !decodeBody(w, r, &body) {
		return
	}
	if body.TrainID == 0 {
		util.Error(w, http.StatusBadRequest, "train_id is required")
		return
	}

	_, err = h.db.Exec("INSERT OR IGNORE INTO section_trains (section_id, train_id) VALUES (?, ?)", sectionID, body.TrainID)
	if err != nil {
		util.Error(w, http.StatusInternalServerError, err.Error())
		return
	}
	util.JSON(w, http.StatusCreated, map[string]any{"success": true})
}

// ---------- 19. RemoveSectionTrain ----------

func (h *RouteHandler) RemoveSectionTrain(w http.ResponseWriter, r *http.Request) {
	routeID, ok := parseID(w, r, "id")
	if !ok {
		return
	}
	sectionID, ok := parseID(w, r, "sectionId")
	if !ok {
		return
	}
	trainID, ok := parseID(w, r, "trainId")
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

	_, err = h.db.Exec("DELETE FROM section_trains WHERE section_id = ? AND train_id = ?", sectionID, trainID)
	if err != nil {
		util.Error(w, http.StatusInternalServerError, err.Error())
		return
	}
	util.Success(w, map[string]any{"success": true})
}

// ---------- 20. GetTrainsWithCoordinates ----------

func (h *RouteHandler) GetTrainsWithCoordinates(w http.ResponseWriter, r *http.Request) {
	id, ok := parseID(w, r, "id")
	if !ok {
		return
	}

	rows, err := h.db.Query(`
		SELECT DISTINCT tr.* FROM trains tr
		INNER JOIN route_trains rt ON tr.id = rt.train_id
		INNER JOIN timetable_trains tt ON tt.train_id = tr.id
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
		h.db.Exec("DELETE FROM timetable_trains WHERE timetable_id = ?", ttID)
		h.db.Exec("DELETE FROM timetable_sections WHERE timetable_id = ?", ttID)
		h.db.Exec("DELETE FROM timetable_entries WHERE timetable_id = ?", ttID)
		h.db.Exec("DELETE FROM timetable_coordinates WHERE timetable_id = ?", ttID)
		h.db.Exec("DELETE FROM timetable_markers WHERE timetable_id = ?", ttID)
	}

	// Delete the timetables themselves
	h.db.Exec("DELETE FROM timetables WHERE route_id = ?", id)

	util.Success(w, map[string]any{"success": true, "deleted": len(ttIDs)})
}

// ---------- 24. FillCoordinates ----------

func (h *RouteHandler) FillCoordinates(w http.ResponseWriter, r *http.Request) {
	var body struct {
		RouteID json.Number `json:"routeId"`
	}
	if !decodeBody(w, r, &body) {
		return
	}
	rid, err := body.RouteID.Int64()
	if err != nil || rid == 0 {
		util.Error(w, http.StatusBadRequest, "routeId is required")
		return
	}
	routeID := int(rid)

	output, err := fillCoordinatesForRoute(h.db, routeID)
	if err != nil {
		util.Error(w, http.StatusInternalServerError, err.Error())
		return
	}

	// Build route map after filling coordinates
	if mapErr := buildRouteMap(h.db, routeID); mapErr != nil {
		output += fmt.Sprintf("\n\nWARNING: buildRouteMap failed: %s", mapErr.Error())
	} else {
		output += "\n\nRoute map rebuilt successfully."
	}

	util.Success(w, map[string]any{
		"success": true,
		"output":  output,
	})
}
