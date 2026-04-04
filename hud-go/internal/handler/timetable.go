package handler

import (
	"database/sql"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"

	"hud-go/internal/util"
)

type TimetableHandler struct {
	db *sql.DB
}

// ---------- helpers ----------

// timetableRow holds all columns from the timetables table, with nullable fields as pointers.
type timetableRow struct {
	ID                     int      `json:"id"`
	ServiceName            string   `json:"service_name"`
	RouteID                *int     `json:"route_id"`
	TrainID                *int     `json:"train_id"`
	ServiceType            string   `json:"service_type"`
	Contributor            *string  `json:"contributor"`
	CoordinatesContributor *string  `json:"coordinates_contributor"`
	CreatedAt              *string  `json:"created_at"`
	Tonnage                *float64 `json:"tonnage"`
	CarCount               *int     `json:"car_count"`
	TrainLength            *float64 `json:"train_length"`
	StartTime              *string  `json:"start_time"`
	Duration               *string  `json:"duration"`
	ServiceImages          *string  `json:"service_images"`
	SectionID              *int     `json:"section_id"`
	ConductorCompatible    *int     `json:"conductor_compatible"`
	Bound                  *string  `json:"bound"`
	Service                *string  `json:"service"`
	CurrentServiceName     *string  `json:"current_service_name"`
}

func scanTimetable(s interface{ Scan(...any) error }) (timetableRow, error) {
	var t timetableRow
	err := s.Scan(
		&t.ID, &t.ServiceName, &t.RouteID, &t.TrainID, &t.ServiceType,
		&t.Contributor, &t.CoordinatesContributor, &t.CreatedAt,
		&t.Tonnage, &t.CarCount, &t.TrainLength, &t.StartTime, &t.Duration,
		&t.ServiceImages, &t.SectionID, &t.ConductorCompatible,
		&t.Bound, &t.Service, &t.CurrentServiceName,
	)
	return t, err
}

const timetableCols = `id, service_name, route_id, train_id, service_type,
	contributor, coordinates_contributor, created_at,
	tonnage, car_count, train_length, start_time, duration,
	service_images, section_id, conductor_compatible,
	bound, service, current_service_name`

func (h *TimetableHandler) getTimetableByID(id int) (*timetableRow, error) {
	row := h.db.QueryRow("SELECT "+timetableCols+" FROM timetables WHERE id = ?", id)
	t, err := scanTimetable(row)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &t, nil
}

type trainInfo struct {
	ID   int    `json:"id"`
	Name string `json:"name"`
}

type sectionInfo struct {
	ID   int    `json:"id"`
	Name string `json:"name"`
}

func (h *TimetableHandler) getTrainsForTimetable(timetableID int) ([]trainInfo, error) {
	rows, err := h.db.Query(`
		SELECT t.id, t.name FROM trains t
		INNER JOIN timetable_trains tt ON t.id = tt.train_id
		WHERE tt.timetable_id = ? ORDER BY t.name`, timetableID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []trainInfo
	for rows.Next() {
		var ti trainInfo
		if err := rows.Scan(&ti.ID, &ti.Name); err != nil {
			return nil, err
		}
		out = append(out, ti)
	}
	if out == nil {
		out = []trainInfo{}
	}
	return out, nil
}

func (h *TimetableHandler) getSectionsForTimetable(timetableID int) ([]sectionInfo, error) {
	rows, err := h.db.Query(`
		SELECT s.id, s.name FROM sections s
		INNER JOIN timetable_sections ts ON s.id = ts.section_id
		WHERE ts.timetable_id = ? ORDER BY s.name`, timetableID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []sectionInfo
	for rows.Next() {
		var si sectionInfo
		if err := rows.Scan(&si.ID, &si.Name); err != nil {
			return nil, err
		}
		out = append(out, si)
	}
	if out == nil {
		out = []sectionInfo{}
	}
	return out, nil
}

type entryRow struct {
	ID          int     `json:"id"`
	TimetableID int     `json:"timetable_id"`
	ActionID    *int    `json:"action_id"`
	Details     *string `json:"details"`
	LocationID  *int    `json:"location_id"`
	Platform    *string `json:"platform"`
	Time1       *string `json:"time1"`
	Time2       *string `json:"time2"`
	Latitude    *string `json:"latitude"`
	Longitude   *string `json:"longitude"`
	ApiName     *string `json:"api_name"`
	SortOrder   *int    `json:"sort_order"`
	CoordSource *string `json:"coord_source"`
	Action      *string `json:"action"`
	Location    *string `json:"location"`
}

func (h *TimetableHandler) getEntriesForTimetable(timetableID int) ([]entryRow, error) {
	rows, err := h.db.Query(`
		SELECT te.id, te.timetable_id, te.action_id, te.details, te.location_id,
			te.platform, te.time1, te.time2, te.latitude, te.longitude,
			te.api_name, te.sort_order, te.coord_source,
			ta.name, l.name
		FROM timetable_entries te
		LEFT JOIN timetable_actions ta ON ta.id = te.action_id
		LEFT JOIN locations l ON l.id = te.location_id
		WHERE te.timetable_id = ? ORDER BY te.sort_order`, timetableID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []entryRow
	for rows.Next() {
		var e entryRow
		if err := rows.Scan(
			&e.ID, &e.TimetableID, &e.ActionID, &e.Details, &e.LocationID,
			&e.Platform, &e.Time1, &e.Time2, &e.Latitude, &e.Longitude,
			&e.ApiName, &e.SortOrder, &e.CoordSource,
			&e.Action, &e.Location,
		); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	if out == nil {
		out = []entryRow{}
	}
	return out, nil
}

// resolveActionID looks up an action by name and returns its ID, or nil.
func (h *TimetableHandler) resolveActionID(action string) *int {
	action = strings.ToUpper(strings.TrimSpace(action))
	if action == "" {
		return nil
	}
	var id int
	err := h.db.QueryRow("SELECT id FROM timetable_actions WHERE name = ?", action).Scan(&id)
	if err != nil {
		return nil
	}
	return &id
}

// findOrCreateLocation looks up a location by route_id + name, creating it if necessary.
func (h *TimetableHandler) findOrCreateLocation(routeID int, name string) (int, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return 0, fmt.Errorf("empty location name")
	}
	var id int
	err := h.db.QueryRow("SELECT id FROM locations WHERE route_id = ? AND name = ?", routeID, name).Scan(&id)
	if err == nil {
		return id, nil
	}
	res, err := h.db.Exec("INSERT OR IGNORE INTO locations (route_id, name) VALUES (?, ?)", routeID, name)
	if err != nil {
		return 0, err
	}
	insertedID, _ := res.LastInsertId()
	if insertedID > 0 {
		return int(insertedID), nil
	}
	// INSERT OR IGNORE may return 0 if it was ignored; re-query
	err = h.db.QueryRow("SELECT id FROM locations WHERE route_id = ? AND name = ?", routeID, name).Scan(&id)
	return id, err
}

// enrichTimetable adds trains, sections, entry_count, coordinate info to a timetable map.
func (h *TimetableHandler) enrichTimetable(t map[string]any) error {
	id := t["id"].(int)
	trains, err := h.getTrainsForTimetable(id)
	if err != nil {
		return err
	}
	sections, err := h.getSectionsForTimetable(id)
	if err != nil {
		return err
	}
	t["trains"] = trains
	t["sections"] = sections

	// stop count (only WAIT FOR SERVICE and STOP AT LOCATION)
	var entryCount int
	h.db.QueryRow(`SELECT COUNT(*) FROM timetable_entries te
		JOIN timetable_actions ta ON ta.id = te.action_id
		WHERE te.timetable_id = ? AND UPPER(ta.name) IN ('WAIT FOR SERVICE', 'STOP AT LOCATION')`, id).Scan(&entryCount)
	t["entry_count"] = entryCount

	// coordinate info
	var coordCount int
	var coordSource *string
	h.db.QueryRow("SELECT COUNT(*) FROM timetable_coordinates WHERE timetable_id = ?", id).Scan(&coordCount)
	if coordCount > 0 {
		h.db.QueryRow("SELECT coord_source FROM timetable_coordinates WHERE timetable_id = ? LIMIT 1", id).Scan(&coordSource)
	}
	t["coordinate_count"] = coordCount
	t["coordinates_complete"] = coordCount > 0
	t["coordinates_coord_source"] = coordSource

	if len(sections) > 0 {
		names := make([]string, len(sections))
		for i, s := range sections {
			names[i] = s.Name
		}
		t["section_name"] = strings.Join(names, ", ")
	} else {
		t["section_name"] = nil
	}
	return nil
}

func timetableToMap(t *timetableRow) map[string]any {
	return map[string]any{
		"id":                       t.ID,
		"service_name":             t.ServiceName,
		"route_id":                 t.RouteID,
		"train_id":                 t.TrainID,
		"service_type":             t.ServiceType,
		"contributor":              t.Contributor,
		"coordinates_contributor":  t.CoordinatesContributor,
		"created_at":               t.CreatedAt,
		"tonnage":                  t.Tonnage,
		"car_count":                t.CarCount,
		"train_length":             t.TrainLength,
		"start_time":               t.StartTime,
		"duration":                 t.Duration,
		"service_images":           t.ServiceImages,
		"section_id":               t.SectionID,
		"conductor_compatible":     t.ConductorCompatible,
		"bound":                    t.Bound,
		"service":                  t.Service,
		"current_service_name":     t.CurrentServiceName,
	}
}

// ---------- Endpoints ----------

func (h *TimetableHandler) GetAll(w http.ResponseWriter, r *http.Request) {
	routeIDStr := r.URL.Query().Get("route_id")
	trainIDStr := r.URL.Query().Get("train_id")

	query := "SELECT " + timetableCols + " FROM timetables"
	var args []any
	var conditions []string

	if routeIDStr != "" {
		conditions = append(conditions, "route_id = ?")
		args = append(args, routeIDStr)
	}
	if conditions != nil {
		query += " WHERE " + strings.Join(conditions, " AND ")
	}
	query += " ORDER BY id DESC"

	rows, err := h.db.Query(query, args...)
	if err != nil {
		util.Error(w, 500, err.Error())
		return
	}
	defer rows.Close()

	var timetables []timetableRow
	for rows.Next() {
		t, err := scanTimetable(rows)
		if err != nil {
			util.Error(w, 500, err.Error())
			return
		}
		timetables = append(timetables, t)
	}

	// Filter by train_id via timetable_trains junction table
	if trainIDStr != "" {
		trainID, _ := strconv.Atoi(trainIDStr)
		ttIDs := map[int]bool{}
		jRows, err := h.db.Query("SELECT timetable_id FROM timetable_trains WHERE train_id = ?", trainID)
		if err == nil {
			defer jRows.Close()
			for jRows.Next() {
				var tid int
				jRows.Scan(&tid)
				ttIDs[tid] = true
			}
		}
		var filtered []timetableRow
		for _, t := range timetables {
			if (t.TrainID != nil && *t.TrainID == trainID) || ttIDs[t.ID] {
				filtered = append(filtered, t)
			}
		}
		timetables = filtered
	}

	// Build results — skip expensive enrichment if not filtered by route/train
	result := make([]map[string]any, 0, len(timetables))
	enrich := routeIDStr != "" || trainIDStr != "" || len(timetables) <= 200
	for _, t := range timetables {
		m := timetableToMap(&t)
		if enrich {
			if err := h.enrichTimetable(m); err != nil {
				util.Error(w, 500, err.Error())
				return
			}
		}
		result = append(result, m)
	}

	util.JSON(w, 200, result)
}

func (h *TimetableHandler) GetPaginated(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	page, _ := strconv.Atoi(q.Get("page"))
	if page < 1 {
		page = 1
	}
	limit, _ := strconv.Atoi(q.Get("limit"))
	if limit < 1 {
		limit = 10
	}
	offset := (page - 1) * limit
	search := q.Get("search")
	routeIDStr := q.Get("route_id")
	countryIDStr := q.Get("country_id")
	trainIDStr := q.Get("train_id")
	section := q.Get("section")
	coordSource := q.Get("coord_source")
	conductor := q.Get("conductor")
	startTimeMin := q.Get("start_time_min")
	startTimeMax := q.Get("start_time_max")
	durationMin := q.Get("duration_min")
	durationMax := q.Get("duration_max")
	stopsMinStr := q.Get("stops_min")
	stopsMaxStr := q.Get("stops_max")

	fromClause := "FROM timetables t"
	var joins []string
	var conditions []string
	var params []any

	if countryIDStr != "" {
		joins = append(joins, "JOIN routes r ON r.id = t.route_id")
		conditions = append(conditions, "r.country_id = ?")
		params = append(params, countryIDStr)
	}
	if routeIDStr != "" {
		conditions = append(conditions, "t.route_id = ?")
		params = append(params, routeIDStr)
	}
	if trainIDStr != "" {
		joins = append(joins, "JOIN timetable_trains tt_train ON tt_train.timetable_id = t.id")
		conditions = append(conditions, "tt_train.train_id = ?")
		params = append(params, trainIDStr)
	}
	if section == "__none__" {
		conditions = append(conditions, "NOT EXISTS (SELECT 1 FROM timetable_sections ts WHERE ts.timetable_id = t.id)")
	} else if section != "" {
		joins = append(joins, "JOIN timetable_sections ts ON ts.timetable_id = t.id JOIN sections sec ON sec.id = ts.section_id")
		conditions = append(conditions, "sec.name = ?")
		params = append(params, section)
	}
	if coordSource == "none" {
		conditions = append(conditions, "NOT EXISTS (SELECT 1 FROM timetable_coordinates tc WHERE tc.timetable_id = t.id)")
	} else if coordSource != "" {
		joins = append(joins, "JOIN timetable_coordinates tc_cs ON tc_cs.timetable_id = t.id")
		conditions = append(conditions, "tc_cs.coord_source = ?")
		params = append(params, coordSource)
	}
	if conductor == "yes" {
		conditions = append(conditions, "t.conductor_compatible = 1")
	} else if conductor == "no" {
		conditions = append(conditions, "(t.conductor_compatible = 0 OR t.conductor_compatible IS NULL)")
	}
	if search != "" {
		conditions = append(conditions, "(t.service_name LIKE ? OR t.service LIKE ? OR t.current_service_name LIKE ?)")
		pat := "%" + search + "%"
		params = append(params, pat, pat, pat)
	}
	if startTimeMin != "" {
		conditions = append(conditions, "t.start_time >= ?")
		params = append(params, startTimeMin)
	}
	if startTimeMax != "" {
		conditions = append(conditions, "t.start_time <= ?")
		params = append(params, startTimeMax)
	}
	if durationMin != "" {
		conditions = append(conditions, "t.duration >= ?")
		params = append(params, durationMin)
	}
	if durationMax != "" {
		conditions = append(conditions, "t.duration <= ?")
		params = append(params, durationMax)
	}

	joinClause := ""
	if len(joins) > 0 {
		joinClause = " " + strings.Join(joins, " ")
	}
	whereClause := ""
	if len(conditions) > 0 {
		whereClause = " WHERE " + strings.Join(conditions, " AND ")
	}

	// Count
	countSQL := "SELECT COUNT(DISTINCT t.id) " + fromClause + joinClause + whereClause
	var total int
	h.db.QueryRow(countSQL, params...).Scan(&total)

	// Data
	dataSQL := "SELECT DISTINCT " + timetableCols + " " + fromClause + joinClause + whereClause + " ORDER BY t.id DESC LIMIT ? OFFSET ?"
	// need to prefix cols with t.
	dataSQL = "SELECT DISTINCT t.id, t.service_name, t.route_id, t.train_id, t.service_type, t.contributor, t.coordinates_contributor, t.created_at, t.tonnage, t.car_count, t.train_length, t.start_time, t.duration, t.service_images, t.section_id, t.conductor_compatible, t.bound, t.service, t.current_service_name " + fromClause + joinClause + whereClause + " ORDER BY t.id DESC LIMIT ? OFFSET ?"
	dataParams := append(params, limit, offset)
	dataRows, err := h.db.Query(dataSQL, dataParams...)
	if err != nil {
		util.Error(w, 500, err.Error())
		return
	}
	defer dataRows.Close()

	var timetables []timetableRow
	for dataRows.Next() {
		t, err := scanTimetable(dataRows)
		if err != nil {
			util.Error(w, 500, err.Error())
			return
		}
		timetables = append(timetables, t)
	}

	// Apply stops filter if needed
	stopsMin := -1
	stopsMax := -1
	if stopsMinStr != "" {
		stopsMin, _ = strconv.Atoi(stopsMinStr)
	}
	if stopsMaxStr != "" {
		stopsMax, _ = strconv.Atoi(stopsMaxStr)
	}

	result := make([]map[string]any, 0)
	for _, t := range timetables {
		m := timetableToMap(&t)
		if err := h.enrichTimetable(m); err != nil {
			util.Error(w, 500, err.Error())
			return
		}
		ec := m["entry_count"].(int)
		if stopsMin >= 0 && ec < stopsMin {
			continue
		}
		if stopsMax >= 0 && ec > stopsMax {
			continue
		}
		result = append(result, m)
	}

	totalPages := 0
	if limit > 0 {
		totalPages = int(math.Ceil(float64(total) / float64(limit)))
	}

	util.JSON(w, 200, map[string]any{
		"data": result,
		"pagination": map[string]any{
			"page":       page,
			"limit":      limit,
			"total":      total,
			"totalPages": totalPages,
		},
	})
}

func (h *TimetableHandler) GetRouteSummary(w http.ResponseWriter, r *http.Request) {
	rows, err := h.db.Query("SELECT route_id, COUNT(*) as count FROM timetables GROUP BY route_id")
	if err != nil {
		util.Error(w, 500, err.Error())
		return
	}
	defer rows.Close()

	result := make([]map[string]any, 0)
	for rows.Next() {
		var routeID *int
		var count int
		if err := rows.Scan(&routeID, &count); err != nil {
			util.Error(w, 500, err.Error())
			return
		}
		result = append(result, map[string]any{"route_id": routeID, "count": count})
	}
	util.JSON(w, 200, result)
}

func (h *TimetableHandler) CheckService(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	serviceCode := q.Get("service")
	serviceName := q.Get("service_name")

	if serviceCode != "" {
		var count int
		h.db.QueryRow("SELECT COUNT(*) FROM timetables WHERE service = ?", serviceCode).Scan(&count)
		util.JSON(w, 200, map[string]any{"exists": count > 0, "field": "service", "value": serviceCode})
		return
	}

	if serviceName != "" {
		routeIDStr := q.Get("route_id")
		query := "SELECT COUNT(*) FROM timetables WHERE service_name = ?"
		args := []any{serviceName}
		if routeIDStr != "" {
			query += " AND route_id = ?"
			args = append(args, routeIDStr)
		}
		var count int
		h.db.QueryRow(query, args...).Scan(&count)
		exists := count > 0

		var timetableID *int
		if exists {
			findQ := "SELECT id FROM timetables WHERE service_name = ?"
			findArgs := []any{serviceName}
			if routeIDStr != "" {
				findQ += " AND route_id = ?"
				findArgs = append(findArgs, routeIDStr)
			}
			findQ += " LIMIT 1"
			var tid int
			if h.db.QueryRow(findQ, findArgs...).Scan(&tid) == nil {
				timetableID = &tid
			}
		}

		util.JSON(w, 200, map[string]any{
			"exists":       exists,
			"field":        "service_name",
			"value":        serviceName,
			"timetable_id": timetableID,
		})
		return
	}

	util.Error(w, 400, "Provide ?service= or ?service_name= query parameter")
}

func (h *TimetableHandler) Detect(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	currentServiceName := q.Get("current_service_name")
	if currentServiceName == "" {
		util.Error(w, 400, "Provide ?current_service_name= query parameter")
		return
	}

	// Try to find timetable by current_service_name
	row := h.db.QueryRow(`
		SELECT t.id, t.service_name, t.current_service_name, t.route_id, r.name
		FROM timetables t
		LEFT JOIN routes r ON r.id = t.route_id
		WHERE t.current_service_name = ?
		LIMIT 1`, currentServiceName)

	var id int
	var sName string
	var csName *string
	var routeID *int
	var routeName *string
	err := row.Scan(&id, &sName, &csName, &routeID, &routeName)
	if err == sql.ErrNoRows {
		util.JSON(w, 200, map[string]any{"found": false, "current_service_name": currentServiceName})
		return
	}
	if err != nil {
		util.Error(w, 500, err.Error())
		return
	}
	util.JSON(w, 200, map[string]any{
		"found":                true,
		"timetable_id":         id,
		"service_name":         sName,
		"current_service_name": csName,
		"route_name":           routeName,
		"route_id":             routeID,
	})
}

func (h *TimetableHandler) GetServicesByTrain(w http.ResponseWriter, r *http.Request) {
	trainIDStr := r.URL.Query().Get("train_id")
	if trainIDStr == "" {
		util.Error(w, 400, "Provide ?train_id= query parameter")
		return
	}

	rows, err := h.db.Query(`
		SELECT DISTINCT t.service FROM timetables t
		INNER JOIN timetable_trains tt ON tt.timetable_id = t.id
		WHERE tt.train_id = ? AND t.service IS NOT NULL`, trainIDStr)
	if err != nil {
		util.Error(w, 500, err.Error())
		return
	}
	defer rows.Close()

	services := make([]string, 0)
	for rows.Next() {
		var s string
		rows.Scan(&s)
		services = append(services, s)
	}
	util.JSON(w, 200, map[string]any{"services": services})
}

func (h *TimetableHandler) Create(w http.ResponseWriter, r *http.Request) {
	var body struct {
		ServiceName        string   `json:"service_name"`
		RouteID            *int     `json:"route_id"`
		TrainID            *int     `json:"train_id"`
		TrainIDs           []int    `json:"train_ids"`
		ServiceType        string   `json:"service_type"`
		Bound              *string  `json:"bound"`
		Service            *string  `json:"service"`
		ServiceImages      *string  `json:"service_images"`
		SectionID          *int     `json:"section_id"`
		ConductorCompatible *bool   `json:"conductor_compatible"`
		CurrentServiceName *string  `json:"current_service_name"`
		Tonnage            *float64 `json:"tonnage"`
		CarCount           *int     `json:"car_count"`
		TrainLength        *float64 `json:"train_length"`
		StartTime          *string  `json:"start_time"`
		Duration           *string  `json:"duration"`
		Entries            []struct {
			Action      string  `json:"action"`
			ActionID    *int    `json:"action_id"`
			Details     string  `json:"details"`
			Location    string  `json:"location"`
			LocationID  *int    `json:"location_id"`
			Platform    string  `json:"platform"`
			Time1       string  `json:"time1"`
			Time2       string  `json:"time2"`
			Latitude    string  `json:"latitude"`
			Longitude   string  `json:"longitude"`
			ApiName     string  `json:"api_name"`
			CoordSource *string `json:"coord_source"`
		} `json:"entries"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		util.Error(w, 400, "invalid JSON body")
		return
	}

	serviceName := strings.TrimSpace(body.ServiceName)
	if serviceName == "" {
		util.Error(w, 400, "A timetable must have a service name.")
		return
	}
	if body.RouteID == nil {
		util.Error(w, 400, "A timetable must have a route. Please select a route.")
		return
	}
	routeID := *body.RouteID

	trainIDs := body.TrainIDs
	if len(trainIDs) == 0 && body.TrainID != nil {
		trainIDs = []int{*body.TrainID}
	}
	if len(trainIDs) == 0 {
		util.Error(w, 400, "A timetable must have at least one train. Please select a train.")
		return
	}

	// Check for duplicate service name on route
	var existingID int
	err := h.db.QueryRow("SELECT id FROM timetables WHERE service_name = ? AND route_id = ?", serviceName, routeID).Scan(&existingID)
	if err == nil {
		// Timetable exists - add trains/sections to it
		trainsAdded := 0
		sectionsAdded := 0
		for _, tid := range trainIDs {
			var cnt int
			h.db.QueryRow("SELECT COUNT(*) FROM timetable_trains WHERE timetable_id = ? AND train_id = ?", existingID, tid).Scan(&cnt)
			if cnt == 0 {
				h.db.Exec("INSERT OR IGNORE INTO timetable_trains (timetable_id, train_id) VALUES (?, ?)", existingID, tid)
				trainsAdded++
			}
		}
		if body.SectionID != nil {
			var cnt int
			h.db.QueryRow("SELECT COUNT(*) FROM timetable_sections WHERE timetable_id = ? AND section_id = ?", existingID, *body.SectionID).Scan(&cnt)
			if cnt == 0 {
				h.db.Exec("INSERT OR IGNORE INTO timetable_sections (timetable_id, section_id) VALUES (?, ?)", existingID, *body.SectionID)
				sectionsAdded++
			}
		}
		msg := "Timetable already exists"
		if trainsAdded > 0 || sectionsAdded > 0 {
			parts := []string{}
			if trainsAdded > 0 {
				parts = append(parts, fmt.Sprintf("%d new train(s)", trainsAdded))
			}
			if sectionsAdded > 0 {
				parts = append(parts, fmt.Sprintf("%d new section(s)", sectionsAdded))
			}
			msg += ". Added " + strings.Join(parts, " and ") + " to it."
		} else {
			msg += " and all specified trains/sections are already linked to it."
		}
		util.JSON(w, 200, map[string]any{
			"message":        msg,
			"timetable_id":   existingID,
			"trains_added":   trainsAdded,
			"sections_added": sectionsAdded,
		})
		return
	}

	// Validate first entry has a location
	if len(body.Entries) > 0 {
		first := body.Entries[0]
		if strings.TrimSpace(first.Location) == "" {
			util.Error(w, 400, "The first entry must have a location name.")
			return
		}
	}

	trainID := trainIDs[0]
	serviceType := body.ServiceType
	if serviceType == "" {
		serviceType = "passenger"
	}
	conductorVal := 0
	if body.ConductorCompatible != nil && *body.ConductorCompatible {
		conductorVal = 1
	}

	res, err := h.db.Exec(`INSERT INTO timetables (service_name, route_id, train_id, service_type, bound, service, service_images, section_id, conductor_compatible, current_service_name)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		serviceName, routeID, trainID, serviceType,
		body.Bound, body.Service, body.ServiceImages, body.SectionID,
		conductorVal, body.CurrentServiceName)
	if err != nil {
		util.Error(w, 500, err.Error())
		return
	}
	timetableID64, _ := res.LastInsertId()
	timetableID := int(timetableID64)

	// Insert trains into junction table
	for _, tid := range trainIDs {
		h.db.Exec("INSERT OR IGNORE INTO timetable_trains (timetable_id, train_id) VALUES (?, ?)", timetableID, tid)
	}

	// Add section if provided
	if body.SectionID != nil {
		h.db.Exec("INSERT OR IGNORE INTO timetable_sections (timetable_id, section_id) VALUES (?, ?)", timetableID, *body.SectionID)
	}

	// Update train specs if provided
	specUpdates := []string{}
	specArgs := []any{}
	if body.Tonnage != nil {
		specUpdates = append(specUpdates, "tonnage = ?")
		specArgs = append(specArgs, *body.Tonnage)
	}
	if body.CarCount != nil {
		specUpdates = append(specUpdates, "car_count = ?")
		specArgs = append(specArgs, *body.CarCount)
	}
	if body.TrainLength != nil {
		specUpdates = append(specUpdates, "train_length = ?")
		specArgs = append(specArgs, *body.TrainLength)
	}
	if body.StartTime != nil {
		specUpdates = append(specUpdates, "start_time = ?")
		specArgs = append(specArgs, *body.StartTime)
	}
	if body.Duration != nil {
		specUpdates = append(specUpdates, "duration = ?")
		specArgs = append(specArgs, *body.Duration)
	}
	if len(specUpdates) > 0 {
		specArgs = append(specArgs, timetableID)
		h.db.Exec("UPDATE timetables SET "+strings.Join(specUpdates, ", ")+" WHERE id = ?", specArgs...)
	}

	// Create entries
	savedEntries := make([]map[string]any, 0)
	for i, entry := range body.Entries {
		actionID := entry.ActionID
		if actionID == nil {
			actionID = h.resolveActionID(entry.Action)
		}
		locID := entry.LocationID
		locName := strings.TrimSpace(entry.Location)
		if locID == nil && locName != "" {
			lid, err := h.findOrCreateLocation(routeID, locName)
			if err == nil {
				locID = &lid
			}
		}
		h.db.Exec(`INSERT INTO timetable_entries (timetable_id, action_id, details, location_id, platform, time1, time2, latitude, longitude, api_name, sort_order, coord_source)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			timetableID, actionID, entry.Details, locID, entry.Platform,
			entry.Time1, entry.Time2, entry.Latitude, entry.Longitude,
			entry.ApiName, i, entry.CoordSource)
		savedEntries = append(savedEntries, map[string]any{
			"timetable_id": timetableID,
			"action":       entry.Action,
			"action_id":    actionID,
			"details":      entry.Details,
			"location":     locName,
			"platform":     entry.Platform,
			"time1":        entry.Time1,
			"time2":        entry.Time2,
			"latitude":     entry.Latitude,
			"longitude":    entry.Longitude,
			"sort_order":   i,
			"coord_source": entry.CoordSource,
		})
	}

	util.JSON(w, 201, map[string]any{
		"id":           timetableID,
		"service_name": serviceName,
		"service":      body.Service,
		"route_id":     routeID,
		"train_id":     trainID,
		"service_type": serviceType,
		"entries":      savedEntries,
	})
}

func (h *TimetableHandler) Import(w http.ResponseWriter, r *http.Request) {
	var body map[string]any
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		util.Error(w, 400, "invalid JSON body")
		return
	}

	serviceName := ""
	if v, ok := body["serviceName"].(string); ok && v != "" {
		serviceName = v
	} else if v, ok := body["routeName"].(string); ok && v != "" {
		serviceName = v
	}
	if serviceName == "" {
		util.Error(w, 400, "Missing required field: serviceName or routeName")
		return
	}

	// Check if already exists
	var existingID int
	if h.db.QueryRow("SELECT id FROM timetables WHERE service_name = ?", serviceName).Scan(&existingID) == nil {
		util.JSON(w, 409, map[string]any{
			"error":      fmt.Sprintf("A timetable with the service name \"%s\" already exists", serviceName),
			"existingId": existingID,
		})
		return
	}

	// Resolve country
	var countryID *int
	countryCreated := false
	countryName, _ := body["countryName"].(string)
	routeNameStr, _ := body["routeName"].(string)
	if routeNameStr != "" && countryName == "" {
		util.Error(w, 400, "Country of route missing in import file")
		return
	}
	if countryName != "" {
		var cid int
		err := h.db.QueryRow("SELECT id FROM countries WHERE name = ?", countryName).Scan(&cid)
		if err == nil {
			countryID = &cid
		} else {
			res, err := h.db.Exec("INSERT INTO countries (name) VALUES (?)", countryName)
			if err != nil {
				util.Error(w, 500, err.Error())
				return
			}
			id64, _ := res.LastInsertId()
			cid = int(id64)
			countryID = &cid
			countryCreated = true
		}
	}

	// Resolve route
	var routeID *int
	routeCreated := false
	if routeNameStr != "" {
		var rid int
		err := h.db.QueryRow("SELECT id FROM routes WHERE name = ?", routeNameStr).Scan(&rid)
		if err == nil {
			routeID = &rid
		} else {
			cid := 0
			if countryID != nil {
				cid = *countryID
			}
			res, err := h.db.Exec("INSERT INTO routes (name, country_id, tsw_version) VALUES (?, ?, 3)", routeNameStr, cid)
			if err != nil {
				util.Error(w, 500, err.Error())
				return
			}
			id64, _ := res.LastInsertId()
			rid = int(id64)
			routeID = &rid
			routeCreated = true
		}
	}

	// Resolve trains by name
	trainNamesRaw, _ := body["trainNames"].([]any)
	var trainIDs []int
	var missingTrains []string
	trainNames := make([]string, 0)
	seen := map[string]bool{}
	for _, tn := range trainNamesRaw {
		name, ok := tn.(string)
		if !ok || strings.TrimSpace(name) == "" {
			continue
		}
		name = strings.TrimSpace(name)
		if seen[name] {
			continue
		}
		seen[name] = true
		trainNames = append(trainNames, name)
		var tid int
		err := h.db.QueryRow("SELECT id FROM trains WHERE name = ?", name).Scan(&tid)
		if err == nil {
			trainIDs = append(trainIDs, tid)
		} else {
			missingTrains = append(missingTrains, name)
		}
	}
	if len(missingTrains) > 0 {
		quoted := make([]string, len(missingTrains))
		for i, t := range missingTrains {
			quoted[i] = fmt.Sprintf("\"%s\"", t)
		}
		util.JSON(w, 400, map[string]any{
			"error":         fmt.Sprintf("The following trains do not exist: %s. Please create them first.", strings.Join(quoted, ", ")),
			"missingTrains": missingTrains,
		})
		return
	}

	var trainID *int
	if len(trainIDs) > 0 {
		trainID = &trainIDs[0]
	}

	importedContributor, _ := body["contributor"].(string)
	importedServiceType, _ := body["serviceType"].(string)
	if importedServiceType == "" {
		importedServiceType = "passenger"
	}
	importedService, _ := body["service"].(string)
	importedBound, _ := body["bound"].(string)
	importedConductorCompatible := false
	if v, ok := body["conductorCompatible"].(bool); ok {
		importedConductorCompatible = v
	}
	importedCurrentServiceName, _ := body["current_service_name"].(string)

	// Resolve sections by name
	var importedSectionIDs []int
	if routeID != nil {
		if sectionNames, ok := body["sectionNames"].([]any); ok {
			for _, sn := range sectionNames {
				name, ok := sn.(string)
				if !ok || name == "" {
					continue
				}
				sid, err := h.findOrCreateSection(*routeID, name)
				if err == nil {
					importedSectionIDs = append(importedSectionIDs, sid)
				}
			}
		} else if sectionName, ok := body["sectionName"].(string); ok && sectionName != "" {
			sid, err := h.findOrCreateSection(*routeID, sectionName)
			if err == nil {
				importedSectionIDs = append(importedSectionIDs, sid)
			}
		}
	}
	var importedSectionID *int
	if len(importedSectionIDs) > 0 {
		importedSectionID = &importedSectionIDs[0]
	}

	conductorVal := 0
	if importedConductorCompatible {
		conductorVal = 1
	}

	res, err := h.db.Exec(`INSERT INTO timetables (service_name, route_id, train_id, service_type, contributor, bound, service, section_id, conductor_compatible, current_service_name)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		serviceName, routeID, trainID, importedServiceType,
		nilIfEmpty(importedContributor), nilIfEmpty(importedBound), nilIfEmpty(importedService),
		importedSectionID, conductorVal, nilIfEmpty(importedCurrentServiceName))
	if err != nil {
		util.Error(w, 500, err.Error())
		return
	}
	timetableID64, _ := res.LastInsertId()
	timetableID := int(timetableID64)

	// Insert trains
	for _, tid := range trainIDs {
		h.db.Exec("INSERT OR IGNORE INTO timetable_trains (timetable_id, train_id) VALUES (?, ?)", timetableID, tid)
	}

	// Insert sections
	for _, sid := range importedSectionIDs {
		h.db.Exec("INSERT OR IGNORE INTO timetable_sections (timetable_id, section_id) VALUES (?, ?)", timetableID, sid)
	}

	// Import train specs
	specUpdates := []string{}
	specArgs := []any{}
	if v, ok := body["tonnage"].(float64); ok {
		specUpdates = append(specUpdates, "tonnage = ?")
		specArgs = append(specArgs, v)
	}
	if v, ok := body["carCount"].(float64); ok {
		specUpdates = append(specUpdates, "car_count = ?")
		specArgs = append(specArgs, int(v))
	}
	if v, ok := body["trainLength"].(float64); ok {
		specUpdates = append(specUpdates, "train_length = ?")
		specArgs = append(specArgs, v)
	}
	if v, ok := body["startTime"].(string); ok && v != "" {
		specUpdates = append(specUpdates, "start_time = ?")
		specArgs = append(specArgs, v)
	}
	if v, ok := body["duration"].(string); ok && v != "" {
		specUpdates = append(specUpdates, "duration = ?")
		specArgs = append(specArgs, v)
	}
	if len(specUpdates) > 0 {
		specArgs = append(specArgs, timetableID)
		h.db.Exec("UPDATE timetables SET "+strings.Join(specUpdates, ", ")+" WHERE id = ?", specArgs...)
	}

	// Import coordinates if present
	coordsImported := 0
	if coords, ok := body["coordinates"].([]any); ok && len(coords) > 0 {
		coordSource := "automatic"
		if cs, ok := body["coordinates_source"].(string); ok && cs != "" {
			coordSource = cs
		}
		coordJSON, _ := json.Marshal(coords)
		h.db.Exec("INSERT INTO timetable_coordinates (timetable_id, coordinates, coord_source) VALUES (?, ?, ?)",
			timetableID, string(coordJSON), coordSource)
		coordsImported = len(coords)

		if cc, ok := body["coordinates_contributor"].(string); ok && cc != "" {
			h.db.Exec("UPDATE timetables SET coordinates_contributor = ? WHERE id = ?", cc, timetableID)
		}
	}

	// Import markers if present
	markersImported := 0
	if markers, ok := body["markers"].([]any); ok && len(markers) > 0 {
		for _, m := range markers {
			mMap, ok := m.(map[string]any)
			if !ok {
				continue
			}
			stationName := ""
			if v, ok := mMap["stationName"].(string); ok {
				stationName = v
			}
			markerType := "Station"
			if v, ok := mMap["markerType"].(string); ok {
				markerType = v
			}
			var lat, lng, platLen *float64
			if v, ok := mMap["latitude"].(float64); ok {
				lat = &v
			}
			if v, ok := mMap["longitude"].(float64); ok {
				lng = &v
			}
			if v, ok := mMap["platformLength"].(float64); ok {
				platLen = &v
			}
			h.db.Exec("INSERT INTO timetable_markers (timetable_id, station_name, marker_type, latitude, longitude, platform_length) VALUES (?, ?, ?, ?, ?, ?)",
				timetableID, stationName, markerType, lat, lng, platLen)
			markersImported++
		}
	}

	// Import entries from csvData
	entriesImported := 0
	if csvData, ok := body["csvData"].([]any); ok && len(csvData) > 0 {
		for i, item := range csvData {
			entry, ok := item.(map[string]any)
			if !ok {
				continue
			}
			action, _ := entry["action"].(string)
			action = strings.ToUpper(strings.TrimSpace(action))
			details, _ := entry["details"].(string)
			location, _ := entry["location"].(string)
			location = strings.TrimSpace(location)
			platform, _ := entry["platform"].(string)
			time1, _ := entry["time1"].(string)
			time2, _ := entry["time2"].(string)
			lat, _ := entry["latitude"].(string)
			lng, _ := entry["longitude"].(string)
			apiName, _ := entry["api_name"].(string)
			coordSrc, _ := entry["coord_source"].(string)

			// Fall back to arrival/departure for old format
			if time1 == "" {
				if v, ok := entry["arrival"].(string); ok {
					time1 = v
				}
			}
			if time2 == "" {
				if v, ok := entry["departure"].(string); ok {
					time2 = v
				}
			}

			isUnload := action == "UNLOAD PASSENGERS"
			if isUnload {
				location = ""
				details = ""
			}

			actionID := h.resolveActionID(action)
			var locID *int
			if location != "" && routeID != nil {
				lid, err := h.findOrCreateLocation(*routeID, location)
				if err == nil {
					locID = &lid
				}
			}
			if isUnload {
				locID = nil
			}

			h.db.Exec(`INSERT INTO timetable_entries (timetable_id, action_id, details, location_id, platform, time1, time2, latitude, longitude, api_name, sort_order, coord_source)
				VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
				timetableID, actionID, details, locID, platform,
				time1, time2, lat, lng, apiName, i, nilIfEmpty(coordSrc))
			entriesImported++
		}
	}

	util.JSON(w, 201, map[string]any{
		"id":                       timetableID,
		"service_name":             serviceName,
		"service":                  nilIfEmpty(importedService),
		"country_id":               countryID,
		"country_name":             nilIfEmpty(countryName),
		"country_created":          countryCreated,
		"route_id":                 routeID,
		"route_name":               nilIfEmpty(routeNameStr),
		"route_created":            routeCreated,
		"train_id":                 trainID,
		"train_ids":                trainIDs,
		"train_names":              trainNames,
		"service_type":             importedServiceType,
		"contributor":              nilIfEmpty(importedContributor),
		"coordinates_contributor":  nilIfEmpty(func() string { v, _ := body["coordinates_contributor"].(string); return v }()),
		"message":                  "Timetable imported successfully",
		"coordinatesImported":      coordsImported,
		"markersImported":          markersImported,
		"entriesImported":          entriesImported,
	})
}

func (h *TimetableHandler) GetByID(w http.ResponseWriter, r *http.Request) {
	idStr := chi.URLParam(r, "id")
	id, err := strconv.Atoi(idStr)
	if err != nil {
		util.Error(w, 400, "invalid id")
		return
	}

	t, err := h.getTimetableByID(id)
	if err != nil {
		util.Error(w, 500, err.Error())
		return
	}
	if t == nil {
		util.Error(w, 404, "Timetable not found")
		return
	}

	result := timetableToMap(t)

	// Add route_name
	if t.RouteID != nil {
		var routeName string
		if h.db.QueryRow("SELECT name FROM routes WHERE id = ?", *t.RouteID).Scan(&routeName) == nil {
			result["route_name"] = routeName
		} else {
			result["route_name"] = nil
		}
	} else {
		result["route_name"] = nil
	}

	// Add train_name
	if t.TrainID != nil {
		var trainName string
		if h.db.QueryRow("SELECT name FROM trains WHERE id = ?", *t.TrainID).Scan(&trainName) == nil {
			result["train_name"] = trainName
		} else {
			result["train_name"] = nil
		}
	} else {
		result["train_name"] = nil
	}

	// Entries
	entries, err := h.getEntriesForTimetable(id)
	if err != nil {
		util.Error(w, 500, err.Error())
		return
	}
	result["entries"] = entries

	// Trains
	trains, err := h.getTrainsForTimetable(id)
	if err != nil {
		util.Error(w, 500, err.Error())
		return
	}
	result["trains"] = trains

	// Sections
	sections, err := h.getSectionsForTimetable(id)
	if err != nil {
		util.Error(w, 500, err.Error())
		return
	}
	result["sections"] = sections
	if len(sections) > 0 {
		names := make([]string, len(sections))
		for i, s := range sections {
			names[i] = s.Name
		}
		result["section_name"] = strings.Join(names, ", ")
	} else {
		result["section_name"] = nil
	}

	// Coordinate info
	var coordCount int
	h.db.QueryRow("SELECT COUNT(*) FROM timetable_coordinates WHERE timetable_id = ?", id).Scan(&coordCount)
	var coordSource *string
	if coordCount > 0 {
		h.db.QueryRow("SELECT coord_source FROM timetable_coordinates WHERE timetable_id = ? LIMIT 1", id).Scan(&coordSource)
	}
	result["coordinates_coord_source"] = coordSource

	util.JSON(w, 200, result)
}

func (h *TimetableHandler) Update(w http.ResponseWriter, r *http.Request) {
	idStr := chi.URLParam(r, "id")
	id, err := strconv.Atoi(idStr)
	if err != nil {
		util.Error(w, 400, "invalid id")
		return
	}

	t, err := h.getTimetableByID(id)
	if err != nil {
		util.Error(w, 500, err.Error())
		return
	}
	if t == nil {
		util.Error(w, 404, "Timetable not found")
		return
	}

	var body map[string]any
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		util.Error(w, 400, "invalid JSON body")
		return
	}

	// Validate service_name if being updated
	if v, ok := body["service_name"].(string); ok {
		trimmed := strings.TrimSpace(v)
		if trimmed == "" {
			util.Error(w, 400, "A timetable must have a service name.")
			return
		}
		// Check uniqueness (excluding current)
		checkRouteID := t.RouteID
		if rid, ok := body["route_id"].(float64); ok {
			ridInt := int(rid)
			checkRouteID = &ridInt
		}
		var existingID int
		q := "SELECT id FROM timetables WHERE service_name = ? AND id != ?"
		args := []any{trimmed, id}
		if checkRouteID != nil {
			q += " AND route_id = ?"
			args = append(args, *checkRouteID)
		}
		if h.db.QueryRow(q, args...).Scan(&existingID) == nil {
			util.Error(w, 409, "A timetable with this service name already exists on this route")
			return
		}
	}

	// Build dynamic UPDATE
	updates := []string{}
	params := []any{}

	fieldMap := map[string]string{
		"service_name":          "service_name",
		"route_id":              "route_id",
		"train_id":              "train_id",
		"service_type":          "service_type",
		"contributor":           "contributor",
		"coordinates_contributor": "coordinates_contributor",
		"service":               "service",
		"bound":                 "bound",
		"tonnage":               "tonnage",
		"car_count":             "car_count",
		"train_length":          "train_length",
		"start_time":            "start_time",
		"duration":              "duration",
		"current_service_name":  "current_service_name",
	}
	for jsonKey, col := range fieldMap {
		if v, ok := body[jsonKey]; ok {
			updates = append(updates, col+" = ?")
			params = append(params, v)
		}
	}
	if v, ok := body["conductor_compatible"]; ok {
		updates = append(updates, "conductor_compatible = ?")
		val := 0
		if b, ok := v.(bool); ok && b {
			val = 1
		} else if f, ok := v.(float64); ok && f != 0 {
			val = 1
		}
		params = append(params, val)
	}

	// Handle section_ids
	if sids, ok := body["section_ids"].([]any); ok {
		h.db.Exec("DELETE FROM timetable_sections WHERE timetable_id = ?", id)
		for _, sid := range sids {
			if sidF, ok := sid.(float64); ok {
				h.db.Exec("INSERT OR IGNORE INTO timetable_sections (timetable_id, section_id) VALUES (?, ?)", id, int(sidF))
			}
		}
		if len(sids) > 0 {
			if sidF, ok := sids[0].(float64); ok {
				updates = append(updates, "section_id = ?")
				params = append(params, int(sidF))
			}
		} else {
			updates = append(updates, "section_id = ?")
			params = append(params, nil)
		}
	} else if v, ok := body["section_id"]; ok {
		updates = append(updates, "section_id = ?")
		params = append(params, v)
		h.db.Exec("DELETE FROM timetable_sections WHERE timetable_id = ?", id)
		if v != nil {
			if sidF, ok := v.(float64); ok && sidF > 0 {
				h.db.Exec("INSERT OR IGNORE INTO timetable_sections (timetable_id, section_id) VALUES (?, ?)", id, int(sidF))
			}
		}
	}

	// Handle train_ids
	if tids, ok := body["train_ids"].([]any); ok {
		h.db.Exec("DELETE FROM timetable_trains WHERE timetable_id = ?", id)
		for _, tid := range tids {
			if tidF, ok := tid.(float64); ok {
				h.db.Exec("INSERT OR IGNORE INTO timetable_trains (timetable_id, train_id) VALUES (?, ?)", id, int(tidF))
			}
		}
	}

	if len(updates) > 0 {
		params = append(params, id)
		_, err = h.db.Exec("UPDATE timetables SET "+strings.Join(updates, ", ")+" WHERE id = ?", params...)
		if err != nil {
			util.Error(w, 500, err.Error())
			return
		}
	}

	// Return updated timetable
	updated, _ := h.getTimetableByID(id)
	result := timetableToMap(updated)
	trains, _ := h.getTrainsForTimetable(id)
	sections, _ := h.getSectionsForTimetable(id)
	result["trains"] = trains
	result["sections"] = sections
	util.JSON(w, 200, result)
}

func (h *TimetableHandler) Delete(w http.ResponseWriter, r *http.Request) {
	idStr := chi.URLParam(r, "id")
	id, err := strconv.Atoi(idStr)
	if err != nil {
		util.Error(w, 400, "invalid id")
		return
	}

	h.db.Exec("DELETE FROM timetable_trains WHERE timetable_id = ?", id)
	h.db.Exec("DELETE FROM timetable_sections WHERE timetable_id = ?", id)
	h.db.Exec("DELETE FROM timetable_entries WHERE timetable_id = ?", id)
	h.db.Exec("DELETE FROM timetable_coordinates WHERE timetable_id = ?", id)
	h.db.Exec("DELETE FROM timetable_markers WHERE timetable_id = ?", id)
	h.db.Exec("DELETE FROM timetables WHERE id = ?", id)

	util.JSON(w, 200, map[string]bool{"success": true})
}

// DeletePathData removes path coordinate data for a timetable.
func (h *TimetableHandler) DeletePathData(w http.ResponseWriter, r *http.Request) {
	idStr := chi.URLParam(r, "id")
	id, err := strconv.Atoi(idStr)
	if err != nil {
		util.Error(w, 400, "invalid id")
		return
	}

	h.db.Exec("DELETE FROM timetable_coordinates WHERE timetable_id = ?", id)

	util.JSON(w, 200, map[string]any{"success": true})
}

func (h *TimetableHandler) Export(w http.ResponseWriter, r *http.Request) {
	idStr := chi.URLParam(r, "id")
	id, err := strconv.Atoi(idStr)
	if err != nil {
		util.Error(w, 400, "invalid id")
		return
	}

	exportData, err := h.buildExportData(id)
	if err != nil {
		if err.Error() == "not found" {
			util.Error(w, 404, "Timetable not found")
		} else {
			util.Error(w, 500, err.Error())
		}
		return
	}

	util.JSON(w, 200, exportData)
}

func (h *TimetableHandler) ExportDownload(w http.ResponseWriter, r *http.Request) {
	idStr := chi.URLParam(r, "id")
	id, err := strconv.Atoi(idStr)
	if err != nil {
		util.Error(w, 400, "invalid id")
		return
	}

	t, err := h.getTimetableByID(id)
	if err != nil {
		util.Error(w, 500, err.Error())
		return
	}
	if t == nil {
		util.Error(w, 404, "Timetable not found")
		return
	}

	format := r.URL.Query().Get("format")

	exportData, err := h.buildExportData(id)
	if err != nil {
		util.Error(w, 500, err.Error())
		return
	}

	safeServiceName := sanitizeFilename(t.ServiceName)
	if format == "csv" {
		filename := fmt.Sprintf("%s_%d.csv", safeServiceName, id)
		w.Header().Set("Content-Type", "text/csv")
		w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=\"%s\"", filename))

		writer := csv.NewWriter(w)
		writer.Write([]string{"action", "location", "platform", "time1", "time2", "details", "latitude", "longitude", "api_name", "coord_source"})
		if csvData, ok := exportData["csvData"].([]map[string]any); ok {
			for _, row := range csvData {
				writer.Write([]string{
					fmt.Sprintf("%v", row["action"]),
					fmt.Sprintf("%v", row["location"]),
					fmt.Sprintf("%v", row["platform"]),
					fmt.Sprintf("%v", row["time1"]),
					fmt.Sprintf("%v", row["time2"]),
					fmt.Sprintf("%v", row["details"]),
					fmt.Sprintf("%v", row["latitude"]),
					fmt.Sprintf("%v", row["longitude"]),
					fmt.Sprintf("%v", row["api_name"]),
					fmt.Sprintf("%v", row["coord_source"]),
				})
			}
		}
		writer.Flush()
		return
	}

	filename := fmt.Sprintf("%s_%d.json", safeServiceName, id)
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=\"%s\"", filename))
	json.NewEncoder(w).Encode(exportData)
}

func (h *TimetableHandler) GetTrains(w http.ResponseWriter, r *http.Request) {
	idStr := chi.URLParam(r, "id")
	id, err := strconv.Atoi(idStr)
	if err != nil {
		util.Error(w, 400, "invalid id")
		return
	}
	t, _ := h.getTimetableByID(id)
	if t == nil {
		util.Error(w, 404, "Timetable not found")
		return
	}
	trains, err := h.getTrainsForTimetable(id)
	if err != nil {
		util.Error(w, 500, err.Error())
		return
	}
	util.JSON(w, 200, trains)
}

func (h *TimetableHandler) AddTrain(w http.ResponseWriter, r *http.Request) {
	idStr := chi.URLParam(r, "id")
	id, err := strconv.Atoi(idStr)
	if err != nil {
		util.Error(w, 400, "invalid id")
		return
	}
	t, _ := h.getTimetableByID(id)
	if t == nil {
		util.Error(w, 404, "Timetable not found")
		return
	}
	var body struct {
		TrainID int `json:"train_id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		util.Error(w, 400, "invalid JSON body")
		return
	}
	if body.TrainID == 0 {
		util.Error(w, 400, "train_id is required")
		return
	}
	h.db.Exec("INSERT OR IGNORE INTO timetable_trains (timetable_id, train_id) VALUES (?, ?)", id, body.TrainID)
	trains, _ := h.getTrainsForTimetable(id)
	util.JSON(w, 201, map[string]any{"success": true, "trains": trains})
}

func (h *TimetableHandler) RemoveTrain(w http.ResponseWriter, r *http.Request) {
	idStr := chi.URLParam(r, "id")
	id, err := strconv.Atoi(idStr)
	if err != nil {
		util.Error(w, 400, "invalid id")
		return
	}
	t, _ := h.getTimetableByID(id)
	if t == nil {
		util.Error(w, 404, "Timetable not found")
		return
	}
	var body struct {
		TrainID int `json:"train_id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		util.Error(w, 400, "invalid JSON body")
		return
	}
	if body.TrainID == 0 {
		util.Error(w, 400, "train_id is required")
		return
	}
	h.db.Exec("DELETE FROM timetable_trains WHERE timetable_id = ? AND train_id = ?", id, body.TrainID)
	trains, _ := h.getTrainsForTimetable(id)
	util.JSON(w, 200, map[string]any{"success": true, "trains": trains})
}

func (h *TimetableHandler) RemoveTrainByID(w http.ResponseWriter, r *http.Request) {
	idStr := chi.URLParam(r, "id")
	id, err := strconv.Atoi(idStr)
	if err != nil {
		util.Error(w, 400, "invalid id")
		return
	}
	trainIDStr := chi.URLParam(r, "trainId")
	trainID, err := strconv.Atoi(trainIDStr)
	if err != nil {
		util.Error(w, 400, "invalid trainId")
		return
	}
	t, _ := h.getTimetableByID(id)
	if t == nil {
		util.Error(w, 404, "Timetable not found")
		return
	}
	h.db.Exec("DELETE FROM timetable_trains WHERE timetable_id = ? AND train_id = ?", id, trainID)
	trains, _ := h.getTrainsForTimetable(id)
	util.JSON(w, 200, map[string]any{"success": true, "trains": trains})
}

func (h *TimetableHandler) GetSections(w http.ResponseWriter, r *http.Request) {
	idStr := chi.URLParam(r, "id")
	id, err := strconv.Atoi(idStr)
	if err != nil {
		util.Error(w, 400, "invalid id")
		return
	}
	t, _ := h.getTimetableByID(id)
	if t == nil {
		util.Error(w, 404, "Timetable not found")
		return
	}
	sections, err := h.getSectionsForTimetable(id)
	if err != nil {
		util.Error(w, 500, err.Error())
		return
	}
	util.JSON(w, 200, sections)
}

func (h *TimetableHandler) AddSection(w http.ResponseWriter, r *http.Request) {
	idStr := chi.URLParam(r, "id")
	id, err := strconv.Atoi(idStr)
	if err != nil {
		util.Error(w, 400, "invalid id")
		return
	}
	t, _ := h.getTimetableByID(id)
	if t == nil {
		util.Error(w, 404, "Timetable not found")
		return
	}
	var body struct {
		SectionID int `json:"section_id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		util.Error(w, 400, "invalid JSON body")
		return
	}
	if body.SectionID == 0 {
		util.Error(w, 400, "section_id is required")
		return
	}
	h.db.Exec("INSERT OR IGNORE INTO timetable_sections (timetable_id, section_id) VALUES (?, ?)", id, body.SectionID)
	sections, _ := h.getSectionsForTimetable(id)
	util.JSON(w, 201, map[string]any{"success": true, "sections": sections})
}

func (h *TimetableHandler) RemoveSection(w http.ResponseWriter, r *http.Request) {
	idStr := chi.URLParam(r, "id")
	id, err := strconv.Atoi(idStr)
	if err != nil {
		util.Error(w, 400, "invalid id")
		return
	}
	sectionIDStr := chi.URLParam(r, "sectionId")
	sectionID, err := strconv.Atoi(sectionIDStr)
	if err != nil {
		util.Error(w, 400, "invalid sectionId")
		return
	}
	t, _ := h.getTimetableByID(id)
	if t == nil {
		util.Error(w, 404, "Timetable not found")
		return
	}
	h.db.Exec("DELETE FROM timetable_sections WHERE timetable_id = ? AND section_id = ?", id, sectionID)
	sections, _ := h.getSectionsForTimetable(id)
	util.JSON(w, 200, map[string]any{"success": true, "sections": sections})
}

func (h *TimetableHandler) ExportAllForRoute(w http.ResponseWriter, r *http.Request) {
	idStr := chi.URLParam(r, "id")
	routeID, err := strconv.Atoi(idStr)
	if err != nil {
		util.Error(w, 400, "invalid route id")
		return
	}

	// Verify route exists
	var routeName string
	err = h.db.QueryRow("SELECT name FROM routes WHERE id = ?", routeID).Scan(&routeName)
	if err == sql.ErrNoRows {
		util.Error(w, 404, "Route not found")
		return
	}
	if err != nil {
		util.Error(w, 500, err.Error())
		return
	}

	// Get all timetables for this route
	rows, err := h.db.Query("SELECT "+timetableCols+" FROM timetables WHERE route_id = ? ORDER BY id DESC", routeID)
	if err != nil {
		util.Error(w, 500, err.Error())
		return
	}
	defer rows.Close()

	var exports []map[string]any
	for rows.Next() {
		t, err := scanTimetable(rows)
		if err != nil {
			util.Error(w, 500, err.Error())
			return
		}
		exportData, err := h.buildExportData(t.ID)
		if err != nil {
			continue
		}
		exports = append(exports, exportData)
	}

	if exports == nil {
		exports = []map[string]any{}
	}

	util.JSON(w, 200, exports)
}

func (h *TimetableHandler) ImportRouteZip(w http.ResponseWriter, r *http.Request) {
	util.Error(w, http.StatusNotImplemented, "not implemented")
}

// ---------- internal helpers ----------

func (h *TimetableHandler) buildExportData(id int) (map[string]any, error) {
	t, err := h.getTimetableByID(id)
	if err != nil {
		return nil, err
	}
	if t == nil {
		return nil, fmt.Errorf("not found")
	}

	entries, _ := h.getEntriesForTimetable(id)
	trains, _ := h.getTrainsForTimetable(id)
	sections, _ := h.getSectionsForTimetable(id)

	// Route name
	var routeName *string
	var countryName *string
	if t.RouteID != nil {
		var rn string
		if h.db.QueryRow("SELECT name FROM routes WHERE id = ?", *t.RouteID).Scan(&rn) == nil {
			routeName = &rn
		}
		var countryID int
		if h.db.QueryRow("SELECT country_id FROM routes WHERE id = ?", *t.RouteID).Scan(&countryID) == nil {
			var cn string
			if h.db.QueryRow("SELECT name FROM countries WHERE id = ?", countryID).Scan(&cn) == nil {
				countryName = &cn
			}
		}
	}

	trainNames := make([]string, len(trains))
	for i, tr := range trains {
		trainNames[i] = tr.Name
	}

	sectionNames := make([]string, len(sections))
	for i, s := range sections {
		sectionNames[i] = s.Name
	}

	// Build csvData from raw entries
	csvData := make([]map[string]any, len(entries))
	for i, e := range entries {
		csvData[i] = map[string]any{
			"index":        i,
			"action":       ptrStr(e.Action),
			"location":     ptrStr(e.Location),
			"platform":     ptrStr(e.Platform),
			"time1":        ptrStr(e.Time1),
			"time2":        ptrStr(e.Time2),
			"details":      ptrStr(e.Details),
			"latitude":     ptrStr(e.Latitude),
			"longitude":    ptrStr(e.Longitude),
			"api_name":     ptrStr(e.ApiName),
			"coord_source": e.CoordSource,
		}
	}

	// Build timetable entries for export
	timetableEntries := make([]map[string]any, len(entries))
	for i, e := range entries {
		entry := map[string]any{
			"index":    i,
			"location": ptrStr(e.Location),
			"arrival":  ptrStr(e.Time1),
			"departure": ptrStr(e.Time2),
			"platform": ptrStr(e.Platform),
			"apiName":  ptrStr(e.ApiName),
		}
		if e.Latitude != nil && *e.Latitude != "" {
			entry["latitude"] = *e.Latitude
		}
		if e.Longitude != nil && *e.Longitude != "" {
			entry["longitude"] = *e.Longitude
		}
		entry["coord_source"] = e.CoordSource
		timetableEntries[i] = entry
	}

	// Coordinate info
	var coordSource *string
	var coordinates []map[string]any
	coordRow := h.db.QueryRow("SELECT coordinates, coord_source FROM timetable_coordinates WHERE timetable_id = ? LIMIT 1", id)
	var coordJSON string
	var cs *string
	if coordRow.Scan(&coordJSON, &cs) == nil {
		coordSource = cs
		var parsed []map[string]any
		if json.Unmarshal([]byte(coordJSON), &parsed) == nil {
			coordinates = parsed
		}
	}
	if coordinates == nil {
		coordinates = []map[string]any{}
	}

	// Markers
	markerRows, _ := h.db.Query("SELECT station_name, marker_type, latitude, longitude, platform_length FROM timetable_markers WHERE timetable_id = ?", id)
	markers := make([]map[string]any, 0)
	if markerRows != nil {
		defer markerRows.Close()
		for markerRows.Next() {
			var sName string
			var mType *string
			var lat, lng, platLen *float64
			markerRows.Scan(&sName, &mType, &lat, &lng, &platLen)
			m := map[string]any{
				"stationName": sName,
				"markerType":  ptrStr(mType),
			}
			if lat != nil {
				m["latitude"] = *lat
			}
			if lng != nil {
				m["longitude"] = *lng
			}
			if platLen != nil {
				m["platformLength"] = *platLen
			}
			markers = append(markers, m)
		}
	}

	var sectionName *string
	if len(sectionNames) > 0 {
		s := sectionNames[0]
		sectionName = &s
	}

	conductorCompat := false
	if t.ConductorCompatible != nil && *t.ConductorCompatible != 0 {
		conductorCompat = true
	}

	exportData := map[string]any{
		"timetableId":             t.ID,
		"serviceName":             t.ServiceName,
		"service":                 t.Service,
		"routeName":               routeName,
		"countryName":             countryName,
		"trainNames":              trainNames,
		"serviceType":             t.ServiceType,
		"contributor":             t.Contributor,
		"coordinates_contributor": t.CoordinatesContributor,
		"bound":                   t.Bound,
		"tonnage":                 t.Tonnage,
		"carCount":                t.CarCount,
		"trainLength":             t.TrainLength,
		"startTime":               t.StartTime,
		"duration":                t.Duration,
		"sectionName":             sectionName,
		"sectionNames":            sectionNames,
		"conductorCompatible":     conductorCompat,
		"current_service_name":    t.CurrentServiceName,
		"totalPoints":             len(coordinates),
		"totalMarkers":            len(markers),
		"coordinates_source":      coordSource,
		"coordinates":             coordinates,
		"markers":                 markers,
		"timetable":               timetableEntries,
		"csvData":                 csvData,
	}

	return exportData, nil
}

func (h *TimetableHandler) findOrCreateSection(routeID int, name string) (int, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return 0, fmt.Errorf("empty section name")
	}
	var id int
	err := h.db.QueryRow("SELECT id FROM sections WHERE route_id = ? AND name = ?", routeID, name).Scan(&id)
	if err == nil {
		return id, nil
	}
	res, err := h.db.Exec("INSERT OR IGNORE INTO sections (route_id, name) VALUES (?, ?)", routeID, name)
	if err != nil {
		return 0, err
	}
	insertedID, _ := res.LastInsertId()
	if insertedID > 0 {
		return int(insertedID), nil
	}
	err = h.db.QueryRow("SELECT id FROM sections WHERE route_id = ? AND name = ?", routeID, name).Scan(&id)
	return id, err
}

func nilIfEmpty(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

func ptrStr(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

func sanitizeFilename(name string) string {
	result := strings.Map(func(r rune) rune {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') {
			return r
		}
		return '_'
	}, name)
	// Collapse multiple underscores
	for strings.Contains(result, "__") {
		result = strings.ReplaceAll(result, "__", "_")
	}
	if len(result) > 50 {
		result = result[:50]
	}
	return result
}
