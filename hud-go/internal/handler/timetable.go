package handler

import (
	"archive/zip"
	"bytes"
	"context"
	"database/sql"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"math"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/go-chi/chi/v5"

	"hud-go/internal/catalog"
	"hud-go/internal/config"
	"hud-go/internal/output"
	"hud-go/internal/util"
)

// importError is the per-file error record returned in the bulk-import
// response body. Lifted to package scope (was a function-local type inside
// ImportZip) so cross-pak merge helpers can append to the same `errs`
// slice without redeclaring the shape.
type importError struct {
	File  string `json:"file"`
	Error string `json:"error"`
}

type TimetableHandler struct {
	db *sql.DB

	// In-memory tracking of in-progress map-features bulk builds, keyed by
	// route_id. Single-user app — at most one build per route at a time.
	// Lost on restart, which is fine: the worst case is the user has to
	// click "Build Maps" again.
	mapBuildMu   sync.Mutex
	mapBuildJobs map[int]*mapBuildJob
}

// mapBuildJob captures live progress of a per-route bulk build of
// timetable_map_features. Read by the status endpoint, mutated by the
// worker goroutine. Embed a sync.Mutex on TimetableHandler instead of one
// per job — total job count is small (≤ active routes) and reading is
// rare (poll-driven).
type mapBuildJob struct {
	Done        int        `json:"done"`
	Total       int        `json:"total"`
	StartedAt   time.Time  `json:"startedAt"`
	CompletedAt *time.Time `json:"completedAt,omitempty"`
	Err         string     `json:"err,omitempty"`
	Current     string     `json:"current,omitempty"` // service_name being built right now
}

// ---------- helpers ----------

// timetableRow holds all columns from the timetables table, with nullable fields as pointers.
// 2026-04-26: dropped tonnage / car_count / train_length — these now live on
// the linked formation (formations.length_m, formations.car_count) instead
// of inline.
type timetableRow struct {
	ID                     int     `json:"id"`
	ServiceName            string  `json:"service_name"`
	RouteID                *int    `json:"route_id"`
	FormationID                *int    `json:"formation_id"`
	ServiceType            string  `json:"service_type"`
	Contributor            *string `json:"contributor"`
	CoordinatesContributor *string `json:"coordinates_contributor"`
	CreatedAt              *string `json:"created_at"`
	StartTime              *string `json:"start_time"`
	Duration               *string `json:"duration"`
	ServiceImages          *string `json:"service_images"`
	SectionID              *int    `json:"section_id"`
	ConductorCompatible    *int    `json:"conductor_compatible"`
	Bound                  *string `json:"bound"`
	Service                *string `json:"service"`
	CurrentServiceName     *string `json:"current_service_name"`
	Source                 *string `json:"source"`
	Playable               *int    `json:"playable"`
}

func scanTimetable(s interface{ Scan(...any) error }) (timetableRow, error) {
	var t timetableRow
	err := s.Scan(
		&t.ID, &t.ServiceName, &t.RouteID, &t.FormationID, &t.ServiceType,
		&t.Contributor, &t.CoordinatesContributor, &t.CreatedAt,
		&t.StartTime, &t.Duration,
		&t.ServiceImages, &t.SectionID, &t.ConductorCompatible,
		&t.Bound, &t.Service, &t.CurrentServiceName,
		&t.Source, &t.Playable,
	)
	return t, err
}

const timetableCols = `id, service_name, route_id, formation_id, service_type,
	contributor, coordinates_contributor, created_at,
	start_time, duration,
	service_images, section_id, conductor_compatible,
	bound, service, current_service_name,
	source, playable`

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

type formationInfo struct {
	ID        int     `json:"id"`
	Name      string  `json:"name"`                  // canonical formation_name (e.g. "Class483_006")
	ClassName *string `json:"class_name,omitempty"`  // display name (e.g. "Isle Of Wight Class 483")
	LengthM   *float64 `json:"length_m,omitempty"`
	CarCount  *int    `json:"car_count,omitempty"`
}

type sectionInfo struct {
	ID   int    `json:"id"`
	Name string `json:"name"`
}

func (h *TimetableHandler) getFormationsForTimetable(timetableID int) ([]formationInfo, error) {
	rows, err := h.db.Query(`
		SELECT t.id, t.name, t.class_name, t.length_m, t.car_count FROM formations t
		INNER JOIN timetable_formations tt ON t.id = tt.formation_id
		WHERE tt.timetable_id = ? ORDER BY t.name`, timetableID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []formationInfo
	for rows.Next() {
		var ti formationInfo
		if err := rows.Scan(&ti.ID, &ti.Name, &ti.ClassName, &ti.LengthM, &ti.CarCount); err != nil {
			return nil, err
		}
		out = append(out, ti)
	}
	if out == nil {
		out = []formationInfo{}
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
	StructureNumber *string `json:"structure_number"`
	Structure       *string `json:"structure"`
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
			te.structure_number, te.structure, te.time1, te.time2, te.latitude, te.longitude,
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
			&e.StructureNumber, &e.Structure, &e.Time1, &e.Time2, &e.Latitude, &e.Longitude,
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

// enrichTimetable adds formations, sections, entry_count, coordinate info to a timetable map.
func (h *TimetableHandler) enrichTimetable(t map[string]any) error {
	id := t["id"].(int)
	formations, err := h.getFormationsForTimetable(id)
	if err != nil {
		return err
	}
	sections, err := h.getSectionsForTimetable(id)
	if err != nil {
		return err
	}
	t["formations"] = formations
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
		"formation_id":                 t.FormationID,
		"service_type":             t.ServiceType,
		"contributor":              t.Contributor,
		"coordinates_contributor":  t.CoordinatesContributor,
		"created_at":               t.CreatedAt,
		"start_time":               t.StartTime,
		"duration":                 t.Duration,
		"service_images":           t.ServiceImages,
		"section_id":               t.SectionID,
		"conductor_compatible":     t.ConductorCompatible,
		"bound":                    t.Bound,
		"service":                  t.Service,
		"current_service_name":     t.CurrentServiceName,
		"source":                   t.Source,
		"playable":                 t.Playable,
	}
}

// ---------- Endpoints ----------

func (h *TimetableHandler) GetAll(w http.ResponseWriter, r *http.Request) {
	routeIDStr := r.URL.Query().Get("route_id")
	formationIDStr := r.URL.Query().Get("formation_id")

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

	// Filter by formation_id via timetable_formations junction table
	if formationIDStr != "" {
		formationID, _ := strconv.Atoi(formationIDStr)
		ttIDs := map[int]bool{}
		jRows, err := h.db.Query("SELECT timetable_id FROM timetable_formations WHERE formation_id = ?", formationID)
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
			if (t.FormationID != nil && *t.FormationID == formationID) || ttIDs[t.ID] {
				filtered = append(filtered, t)
			}
		}
		timetables = filtered
	}

	// Build results — skip expensive enrichment if not filtered by route/train
	result := make([]map[string]any, 0, len(timetables))
	enrich := routeIDStr != "" || formationIDStr != "" || len(timetables) <= 200
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
	formationIDStr := q.Get("formation_id")
	classIDStr := q.Get("class_id")
	section := q.Get("section")
	coordSource := q.Get("coord_source")
	conductor := q.Get("conductor")
	startTimeMin := q.Get("start_time_min")
	startTimeMax := q.Get("start_time_max")
	durationMin := q.Get("duration_min")
	durationMax := q.Get("duration_max")
	stopsMinStr := q.Get("stops_min")
	stopsMaxStr := q.Get("stops_max")
	serviceTypeFilter := q.Get("service_type")
	playableFilter := q.Get("playable")
	sourceFilter := q.Get("source")

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
	if formationIDStr != "" {
		joins = append(joins, "JOIN timetable_formations tt_train ON tt_train.timetable_id = t.id")
		conditions = append(conditions, "tt_train.formation_id = ?")
		params = append(params, formationIDStr)
	}
	// class_id filters timetables for any formation belonging to the class
	// — that's how the train typeahead returns "all timetables for Isle Of
	// Wight Class 483" with a single click rather than per-formation rows.
	if classIDStr != "" {
		joins = append(joins, "JOIN timetable_formations tt_class ON tt_class.timetable_id = t.id JOIN formations tr_class ON tr_class.id = tt_class.formation_id")
		conditions = append(conditions, "tr_class.class_id = ?")
		params = append(params, classIDStr)
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
	if serviceTypeFilter != "" {
		conditions = append(conditions, "t.service_type = ?")
		params = append(params, serviceTypeFilter)
	}
	// Outside development mode, AI-only services are hidden — only the
	// services the player can drive surface in normal listings. The dev
	// flag is server-side state, so end users can't bypass this by
	// hand-crafting a query string.
	devCfg := config.Get()
	if devCfg != nil && !devCfg.DevelopmentMode {
		conditions = append(conditions, "t.playable = 1")
	} else if playableFilter == "yes" {
		conditions = append(conditions, "t.playable = 1")
	} else if playableFilter == "no" {
		conditions = append(conditions, "(t.playable = 0 OR t.playable IS NULL)")
	}
	if sourceFilter != "" {
		conditions = append(conditions, "t.source = ?")
		params = append(params, sourceFilter)
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
	dataSQL := "SELECT DISTINCT t.id, t.service_name, t.route_id, t.formation_id, t.service_type, t.contributor, t.coordinates_contributor, t.created_at, t.start_time, t.duration, t.service_images, t.section_id, t.conductor_compatible, t.bound, t.service, t.current_service_name, t.source, t.playable " + fromClause + joinClause + whereClause + " ORDER BY t.id DESC LIMIT ? OFFSET ?"
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

// GetSources returns distinct non-empty source values across all timetables.
// Powers the "Source" filter dropdown in the timetables list pages.
func (h *TimetableHandler) GetSources(w http.ResponseWriter, r *http.Request) {
	rows, err := h.db.Query("SELECT DISTINCT source FROM timetables WHERE source IS NOT NULL AND source != '' ORDER BY source")
	if err != nil {
		util.Error(w, 500, err.Error())
		return
	}
	defer rows.Close()
	out := []string{}
	for rows.Next() {
		var s string
		if err := rows.Scan(&s); err != nil {
			util.Error(w, 500, err.Error())
			return
		}
		out = append(out, s)
	}
	util.JSON(w, 200, out)
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

func (h *TimetableHandler) GetServicesByFormation(w http.ResponseWriter, r *http.Request) {
	formationIDStr := r.URL.Query().Get("formation_id")
	if formationIDStr == "" {
		util.Error(w, 400, "Provide ?formation_id= query parameter")
		return
	}

	rows, err := h.db.Query(`
		SELECT DISTINCT t.service FROM timetables t
		INNER JOIN timetable_formations tt ON tt.timetable_id = t.id
		WHERE tt.formation_id = ? AND t.service IS NOT NULL`, formationIDStr)
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
		FormationID            *int     `json:"formation_id"`
		FormationIDs           []int    `json:"formation_ids"`
		ServiceType        string   `json:"service_type"`
		Bound              *string  `json:"bound"`
		Service            *string  `json:"service"`
		ServiceImages      *string  `json:"service_images"`
		SectionID          *int     `json:"section_id"`
		ConductorCompatible *bool   `json:"conductor_compatible"`
		CurrentServiceName *string  `json:"current_service_name"`
		StartTime          *string  `json:"start_time"`
		Duration           *string  `json:"duration"`
		Entries            []struct {
			Action      string  `json:"action"`
			ActionID    *int    `json:"action_id"`
			Details     string  `json:"details"`
			Location    string  `json:"location"`
			LocationID  *int    `json:"location_id"`
			StructureNumber string `json:"structure_number"`
			Structure       string `json:"structure"`
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

	formationIDs := body.FormationIDs
	if len(formationIDs) == 0 && body.FormationID != nil {
		formationIDs = []int{*body.FormationID}
	}
	if len(formationIDs) == 0 {
		util.Error(w, 400, "A timetable must have at least one formation. Please select a formation.")
		return
	}

	// Check for duplicate service name on route
	var existingID int
	err := h.db.QueryRow("SELECT id FROM timetables WHERE service_name = ? AND route_id = ?", serviceName, routeID).Scan(&existingID)
	if err == nil {
		// Timetable exists - add trains/sections to it
		formationsAdded := 0
		sectionsAdded := 0
		for _, tid := range formationIDs {
			var cnt int
			h.db.QueryRow("SELECT COUNT(*) FROM timetable_formations WHERE timetable_id = ? AND formation_id = ?", existingID, tid).Scan(&cnt)
			if cnt == 0 {
				h.db.Exec("INSERT OR IGNORE INTO timetable_formations (timetable_id, formation_id) VALUES (?, ?)", existingID, tid)
				formationsAdded++
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
		if formationsAdded > 0 || sectionsAdded > 0 {
			parts := []string{}
			if formationsAdded > 0 {
				parts = append(parts, fmt.Sprintf("%d new formation(s)", formationsAdded))
			}
			if sectionsAdded > 0 {
				parts = append(parts, fmt.Sprintf("%d new section(s)", sectionsAdded))
			}
			msg += ". Added " + strings.Join(parts, " and ") + " to it."
		} else {
			msg += " and all specified formations/sections are already linked to it."
		}
		util.JSON(w, 200, map[string]any{
			"message":        msg,
			"timetable_id":   existingID,
			"formations_added":   formationsAdded,
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

	formationID := formationIDs[0]
	serviceType := body.ServiceType
	if serviceType == "" {
		serviceType = "passenger"
	}
	conductorVal := 0
	if body.ConductorCompatible != nil && *body.ConductorCompatible {
		conductorVal = 1
	}

	res, err := h.db.Exec(`INSERT INTO timetables (service_name, route_id, formation_id, service_type, bound, service, service_images, section_id, conductor_compatible, current_service_name)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		serviceName, routeID, formationID, serviceType,
		body.Bound, body.Service, body.ServiceImages, body.SectionID,
		conductorVal, body.CurrentServiceName)
	if err != nil {
		util.Error(w, 500, err.Error())
		return
	}
	timetableID64, _ := res.LastInsertId()
	timetableID := int(timetableID64)

	// Insert trains into junction table
	for _, tid := range formationIDs {
		h.db.Exec("INSERT OR IGNORE INTO timetable_formations (timetable_id, formation_id) VALUES (?, ?)", timetableID, tid)
	}

	// Add section if provided
	if body.SectionID != nil {
		h.db.Exec("INSERT OR IGNORE INTO timetable_sections (timetable_id, section_id) VALUES (?, ?)", timetableID, *body.SectionID)
	}

	// Update timetable scheduling info if provided. tonnage / car_count /
	// train_length were dropped (now derived from the linked train).
	specUpdates := []string{}
	specArgs := []any{}
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
		h.db.Exec(`INSERT INTO timetable_entries (timetable_id, action_id, details, location_id, structure_number, structure, time1, time2, latitude, longitude, api_name, sort_order, coord_source)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			timetableID, actionID, entry.Details, locID, entry.StructureNumber, entry.Structure,
			entry.Time1, entry.Time2, entry.Latitude, entry.Longitude,
			entry.ApiName, i, entry.CoordSource)
		savedEntries = append(savedEntries, map[string]any{
			"timetable_id":    timetableID,
			"action":          entry.Action,
			"action_id":       actionID,
			"details":         entry.Details,
			"location":        locName,
			"structure_number": entry.StructureNumber,
			"structure":       entry.Structure,
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
		"formation_id":     formationID,
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
	formationNamesRaw, _ := body["formationNames"].([]any)
	var formationIDs []int
	var missingTrains []string
	formationNames := make([]string, 0)
	seen := map[string]bool{}
	for _, tn := range formationNamesRaw {
		name, ok := tn.(string)
		if !ok || strings.TrimSpace(name) == "" {
			continue
		}
		name = strings.TrimSpace(name)
		if seen[name] {
			continue
		}
		seen[name] = true
		formationNames = append(formationNames, name)
		var tid int
		err := h.db.QueryRow("SELECT id FROM formations WHERE name = ?", name).Scan(&tid)
		if err == nil {
			formationIDs = append(formationIDs, tid)
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
			"error":         fmt.Sprintf("The following formations do not exist: %s. Please create them first.", strings.Join(quoted, ", ")),
			"missingTrains": missingTrains,
		})
		return
	}

	var formationID *int
	if len(formationIDs) > 0 {
		formationID = &formationIDs[0]
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

	res, err := h.db.Exec(`INSERT INTO timetables (service_name, route_id, formation_id, service_type, contributor, bound, service, section_id, conductor_compatible, current_service_name)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		serviceName, routeID, formationID, importedServiceType,
		nilIfEmpty(importedContributor), nilIfEmpty(importedBound), nilIfEmpty(importedService),
		importedSectionID, conductorVal, nilIfEmpty(importedCurrentServiceName))
	if err != nil {
		util.Error(w, 500, err.Error())
		return
	}
	timetableID64, _ := res.LastInsertId()
	timetableID := int(timetableID64)

	// Insert trains
	for _, tid := range formationIDs {
		h.db.Exec("INSERT OR IGNORE INTO timetable_formations (timetable_id, formation_id) VALUES (?, ?)", timetableID, tid)
	}

	// Insert sections
	for _, sid := range importedSectionIDs {
		h.db.Exec("INSERT OR IGNORE INTO timetable_sections (timetable_id, section_id) VALUES (?, ?)", timetableID, sid)
	}

	// Import train specs
	specUpdates := []string{}
	specArgs := []any{}
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
			structureNumber, _ := entry["structure_number"].(string)
			if structureNumber == "" {
				structureNumber, _ = entry["platform"].(string)
			}
			structure, _ := entry["structure"].(string)
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

			h.db.Exec(`INSERT INTO timetable_entries (timetable_id, action_id, details, location_id, structure_number, structure, time1, time2, latitude, longitude, api_name, sort_order, coord_source)
				VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
				timetableID, actionID, details, locID, structureNumber, nilIfEmpty(structure),
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
		"formation_id":                 formationID,
		"formation_ids":                formationIDs,
		"formation_names":              formationNames,
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

	// Add formation_name
	if t.FormationID != nil {
		var formationName string
		if h.db.QueryRow("SELECT name FROM formations WHERE id = ?", *t.FormationID).Scan(&formationName) == nil {
			result["formation_name"] = formationName
		} else {
			result["formation_name"] = nil
		}
	} else {
		result["formation_name"] = nil
	}

	// Entries
	entries, err := h.getEntriesForTimetable(id)
	if err != nil {
		util.Error(w, 500, err.Error())
		return
	}
	result["entries"] = entries

	// Trains
	formations, err := h.getFormationsForTimetable(id)
	if err != nil {
		util.Error(w, 500, err.Error())
		return
	}
	result["formations"] = formations

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
		"formation_id":              "formation_id",
		"service_type":          "service_type",
		"contributor":           "contributor",
		"coordinates_contributor": "coordinates_contributor",
		"service":               "service",
		"bound":                 "bound",
		"start_time":            "start_time",
		"duration":              "duration",
		"current_service_name":  "current_service_name",
		"source":                "source",
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
	if v, ok := body["playable"]; ok {
		updates = append(updates, "playable = ?")
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

	// Handle formation_ids
	if tids, ok := body["formation_ids"].([]any); ok {
		h.db.Exec("DELETE FROM timetable_formations WHERE timetable_id = ?", id)
		for _, tid := range tids {
			if tidF, ok := tid.(float64); ok {
				h.db.Exec("INSERT OR IGNORE INTO timetable_formations (timetable_id, formation_id) VALUES (?, ?)", id, int(tidF))
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
	formations, _ := h.getFormationsForTimetable(id)
	sections, _ := h.getSectionsForTimetable(id)
	result["formations"] = formations
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

	h.db.Exec("DELETE FROM timetable_formations WHERE timetable_id = ?", id)
	h.db.Exec("DELETE FROM timetable_sections WHERE timetable_id = ?", id)
	h.db.Exec("DELETE FROM timetable_entries WHERE timetable_id = ?", id)
	h.db.Exec("DELETE FROM timetable_coordinates WHERE timetable_id = ?", id)
	h.db.Exec("DELETE FROM timetable_markers WHERE timetable_id = ?", id)
	h.db.Exec("DELETE FROM timetable_map_features WHERE timetable_id = ?", id)
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

// MapFeatures returns the pre-built filtered route GeoJSON blob for this
// timetable. The blob is computed at import time (or on the first read of
// a missing row) by FilterRouteFeaturesForTimetable; the runtime path is
// just one SELECT — no per-feature filtering. Falls back to a lazy
// rebuild + serve when the row is missing (re-extract dropped it).
//
// Response shape mirrors the relevant slice of /api/routes/{id}/map-data
// so frontend code can swap fetch URLs without changing render code:
//
//	{"timetable_id": N, "features": [GeoJSON Feature, ...]}
func (h *TimetableHandler) MapFeatures(w http.ResponseWriter, r *http.Request) {
	idStr := chi.URLParam(r, "id")
	id, err := strconv.Atoi(idStr)
	if err != nil {
		util.Error(w, 400, "invalid id")
		return
	}
	var blob string
	err = h.db.QueryRow("SELECT features FROM timetable_map_features WHERE timetable_id = ?", id).Scan(&blob)
	if err != nil {
		// Missing row: don't lazy-build inline. The build is 30+ sec on
		// big DLCs and would deadline the request. The frontend handles
		// 404 by surfacing a "Generate Map" button (see show.html); the
		// HUD load path triggers a background build via
		// BuildMapFeaturesForTimetable. Either way, the explicit POST is
		// the only way to populate.
		util.Error(w, 404, "no map features for timetable")
		return
	}

	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	// Hand-write so the embedded JSON blob is served verbatim — wrapping
	// it in another util.JSON would re-marshal the parsed features and
	// burn CPU + memory for no gain.
	w.Write([]byte(`{"timetable_id":`))
	w.Write([]byte(strconv.Itoa(id)))
	w.Write([]byte(`,"features":`))
	w.Write([]byte(blob))
	w.Write([]byte(`}`))
}

// ensureMapFeaturesAsync builds the per-timetable map-features blob for
// timetableID if it doesn't already exist. Designed to run in a goroutine
// from the HUD's UploadRoute handler so player service-selection silently
// primes the data the HUD will want a few minutes later.
//
// Skips quickly when:
//   - the row already exists (most common case after the first build)
//   - the timetable has no route_id (orphan; nothing to filter)
//
// Logs at warn level on real errors but never panics — caller is a
// background goroutine; nothing should crash the server here.
func (h *TimetableHandler) ensureMapFeaturesAsync(timetableID int) {
	defer func() {
		if r := recover(); r != nil {
			log.Printf("[map-features-async] tt=%d panic: %v", timetableID, r)
		}
	}()
	var exists int
	if err := h.db.QueryRow("SELECT 1 FROM timetable_map_features WHERE timetable_id = ?", timetableID).Scan(&exists); err == nil {
		return // already built
	}
	var routeID *int
	if err := h.db.QueryRow("SELECT route_id FROM timetables WHERE id = ?", timetableID).Scan(&routeID); err != nil || routeID == nil {
		return
	}
	h.buildTimetableMapFeatures(timetableID, *routeID)
}

// BuildMapFeaturesForTimetable triggers a single-timetable map-features
// build synchronously. Returns the freshly-built feature blob on success
// so the caller (e.g. the "Generate Map" button on /timetables/{id}) can
// re-render the map without an extra fetch.
//
// POST /api/timetables/{id}/build-map-features
func (h *TimetableHandler) BuildMapFeaturesForTimetable(w http.ResponseWriter, r *http.Request) {
	idStr := chi.URLParam(r, "id")
	id, err := strconv.Atoi(idStr)
	if err != nil {
		util.Error(w, 400, "invalid id")
		return
	}
	var routeID *int
	if err := h.db.QueryRow("SELECT route_id FROM timetables WHERE id = ?", id).Scan(&routeID); err != nil {
		util.Error(w, 404, "timetable not found")
		return
	}
	if routeID == nil {
		util.Error(w, 400, "timetable has no route_id (orphan); cannot build map features")
		return
	}
	h.buildTimetableMapFeatures(id, *routeID)
	var blob string
	if err := h.db.QueryRow("SELECT features FROM timetable_map_features WHERE timetable_id = ?", id).Scan(&blob); err != nil {
		util.Error(w, 500, "build failed (no row written)")
		return
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Write([]byte(`{"timetable_id":`))
	w.Write([]byte(strconv.Itoa(id)))
	w.Write([]byte(`,"features":`))
	w.Write([]byte(blob))
	w.Write([]byte(`}`))
}

// BuildMapFeaturesForRoute starts a background bulk build of map features
// for every timetable on the route that doesn't already have a row in
// timetable_map_features. Returns 202 immediately; clients poll the
// status endpoint for progress + ETA.
//
// POST /api/routes/{id}/build-timetable-maps
func (h *TimetableHandler) BuildMapFeaturesForRoute(w http.ResponseWriter, r *http.Request) {
	idStr := chi.URLParam(r, "id")
	routeID, err := strconv.Atoi(idStr)
	if err != nil {
		util.Error(w, 400, "invalid route id")
		return
	}

	h.mapBuildMu.Lock()
	if existing, running := h.mapBuildJobs[routeID]; running && existing.CompletedAt == nil {
		// A build is already in flight for this route. Return its current
		// state so the UI can resume polling without a duplicate worker.
		h.mapBuildMu.Unlock()
		util.JSON(w, 200, existing)
		return
	}
	rows, err := h.db.Query(`
		SELECT id FROM timetables
		WHERE route_id = ?
		  AND id NOT IN (SELECT timetable_id FROM timetable_map_features)
	`, routeID)
	if err != nil {
		h.mapBuildMu.Unlock()
		util.Error(w, 500, "query missing: "+err.Error())
		return
	}
	var missing []int
	for rows.Next() {
		var id int
		if err := rows.Scan(&id); err == nil {
			missing = append(missing, id)
		}
	}
	rows.Close()

	job := &mapBuildJob{
		Total:     len(missing),
		StartedAt: time.Now(),
	}
	h.mapBuildJobs[routeID] = job
	h.mapBuildMu.Unlock()

	// Empty case: nothing to do. Mark the job complete immediately so the
	// UI sees a fast "done, 0 of 0" response and stops polling.
	if len(missing) == 0 {
		now := time.Now()
		h.mapBuildMu.Lock()
		job.CompletedAt = &now
		h.mapBuildMu.Unlock()
		util.JSON(w, 200, job)
		return
	}

	go h.runMapBuildJob(routeID, missing, job)
	w.WriteHeader(http.StatusAccepted)
	util.JSON(w, http.StatusAccepted, job)
}

// runMapBuildJob is the worker that iterates the missing timetables and
// builds each one, updating the in-memory job state as it goes. Uses one
// shared routeFeatsCache so the route GeoJSON parses once for the whole
// batch (same optimisation the import loop uses).
func (h *TimetableHandler) runMapBuildJob(routeID int, ids []int, job *mapBuildJob) {
	routeFeatsCache := map[int][]map[string]any{}
	for _, ttID := range ids {
		// Best-effort service-name lookup for the "current" display. Tiny
		// query, no big deal if it fails.
		var name string
		h.db.QueryRow("SELECT service_name FROM timetables WHERE id = ?", ttID).Scan(&name)
		h.mapBuildMu.Lock()
		job.Current = name
		h.mapBuildMu.Unlock()

		h.buildTimetableMapFeaturesCached(ttID, routeID, routeFeatsCache)

		h.mapBuildMu.Lock()
		job.Done++
		h.mapBuildMu.Unlock()
	}
	now := time.Now()
	h.mapBuildMu.Lock()
	job.CompletedAt = &now
	job.Current = ""
	h.mapBuildMu.Unlock()
}

// BuildMapFeaturesForRouteStatus returns the live progress of a bulk
// build for this route (or 404 if none has run since server start).
//
// GET /api/routes/{id}/build-timetable-maps
func (h *TimetableHandler) BuildMapFeaturesForRouteStatus(w http.ResponseWriter, r *http.Request) {
	idStr := chi.URLParam(r, "id")
	routeID, err := strconv.Atoi(idStr)
	if err != nil {
		util.Error(w, 400, "invalid route id")
		return
	}
	h.mapBuildMu.Lock()
	job, ok := h.mapBuildJobs[routeID]
	h.mapBuildMu.Unlock()
	if !ok {
		util.Error(w, 404, "no build in progress or recently completed for this route")
		return
	}
	util.JSON(w, 200, job)
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
		writer.Write([]string{"action", "location", "structure", "structure_number", "time1", "time2", "details", "latitude", "longitude", "api_name", "coord_source"})
		if csvData, ok := exportData["csvData"].([]map[string]any); ok {
			for _, row := range csvData {
				writer.Write([]string{
					fmt.Sprintf("%v", row["action"]),
					fmt.Sprintf("%v", row["location"]),
					fmt.Sprintf("%v", row["structure"]),
					fmt.Sprintf("%v", row["structure_number"]),
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

func (h *TimetableHandler) GetFormations(w http.ResponseWriter, r *http.Request) {
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
	formations, err := h.getFormationsForTimetable(id)
	if err != nil {
		util.Error(w, 500, err.Error())
		return
	}
	util.JSON(w, 200, formations)
}

func (h *TimetableHandler) AddFormation(w http.ResponseWriter, r *http.Request) {
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
		FormationID int `json:"formation_id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		util.Error(w, 400, "invalid JSON body")
		return
	}
	if body.FormationID == 0 {
		util.Error(w, 400, "formation_id is required")
		return
	}
	h.db.Exec("INSERT OR IGNORE INTO timetable_formations (timetable_id, formation_id) VALUES (?, ?)", id, body.FormationID)
	formations, _ := h.getFormationsForTimetable(id)
	util.JSON(w, 201, map[string]any{"success": true, "formations": formations})
}

func (h *TimetableHandler) RemoveFormation(w http.ResponseWriter, r *http.Request) {
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
		FormationID int `json:"formation_id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		util.Error(w, 400, "invalid JSON body")
		return
	}
	if body.FormationID == 0 {
		util.Error(w, 400, "formation_id is required")
		return
	}
	h.db.Exec("DELETE FROM timetable_formations WHERE timetable_id = ? AND formation_id = ?", id, body.FormationID)
	formations, _ := h.getFormationsForTimetable(id)
	util.JSON(w, 200, map[string]any{"success": true, "formations": formations})
}

func (h *TimetableHandler) RemoveFormationByID(w http.ResponseWriter, r *http.Request) {
	idStr := chi.URLParam(r, "id")
	id, err := strconv.Atoi(idStr)
	if err != nil {
		util.Error(w, 400, "invalid id")
		return
	}
	formationIDStr := chi.URLParam(r, "formationId")
	formationID, err := strconv.Atoi(formationIDStr)
	if err != nil {
		util.Error(w, 400, "invalid formationId")
		return
	}
	t, _ := h.getTimetableByID(id)
	if t == nil {
		util.Error(w, 404, "Timetable not found")
		return
	}
	h.db.Exec("DELETE FROM timetable_formations WHERE timetable_id = ? AND formation_id = ?", id, formationID)
	formations, _ := h.getFormationsForTimetable(id)
	util.JSON(w, 200, map[string]any{"success": true, "formations": formations})
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

	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for _, exportData := range exports {
		sn := ""
		if v, ok := exportData["serviceName"].(string); ok {
			sn = sanitizeFilename(v)
		}
		if sn == "" {
			sn = "timetable"
		}
		fh, err := zw.Create(sn + ".json")
		if err != nil {
			continue
		}
		json.NewEncoder(fh).Encode(exportData)
	}
	zw.Close()

	safeName := sanitizeFilename(routeName)
	w.Header().Set("Content-Type", "application/zip")
	w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=\"%s.zip\"", safeName))
	w.Write(buf.Bytes())
}

// ImportRouteZip ingests a tsw6-timetable extractor zip. Two passes:
//
//  1. Find `route_<Route>.json` first. It carries the route metadata
//     (name, country, origin) and a GeoJSON FeatureCollection (rails +
//     track features + platforms + signals + switches). Insert/update the
//     route + country, replace route_coordinates / route_markers from the
//     features.
//
//  2. Iterate every per-service `<service>.json`. Each carries its own
//     formation_name + trains[] consist; resolve to a row in the trains
//     table (creating it + formation_vehicles on first sighting), then insert
//     the timetable + entries + coordinates.
//
// Different from the old format: route metadata no longer comes from the
// per-service files, tonnage/car_count/train_length live on the train (not
// the timetable), and there is no longer a top-level formationNames list.
// ImportProgressFunc receives one call per timetable file imported, with
// running and total counts so callers (typically the auto-import path) can
// surface "27 of 4000" progress to the user. `label` is the per-service
// JSON's display name when available, falling back to the filename.
type ImportProgressFunc func(done, total int, label string)

// importProgressKey is the context key the auto-import flow uses to
// inject a progress callback into ImportRouteZip without changing its
// HTTP-handler signature. Unset → no progress lines emitted.
type importProgressKey struct{}

// WithImportProgress returns a derived context that carries the given
// progress callback. Used by the auto-import path so the progress lines
// can be broadcast over the extractor's SSE log.
func WithImportProgress(ctx context.Context, fn ImportProgressFunc) context.Context {
	return context.WithValue(ctx, importProgressKey{}, fn)
}

func progressFromContext(ctx context.Context) ImportProgressFunc {
	if v, ok := ctx.Value(importProgressKey{}).(ImportProgressFunc); ok {
		return v
	}
	return nil
}

// extractClassThumbnailsFromZip pulls every `images/train_classes/*.png`
// entry out of an extract zip and writes it under <appDir>/images/
// train_classes/. Matches the path the writer packs them into (see
// internal/output/package_writer.go:addClassThumbnailsToZip) and the
// static-file route that serves /images/* (router/static.go).
//
// Best-effort: a write failure on one PNG doesn't stop the import.
// Existing files are overwritten so a re-import picks up updated
// textures.
func extractClassThumbnailsFromZip(zr *zip.Reader) {
	outDir := filepath.Join(config.ResourcesDir(), "images", "train_classes")
	for _, f := range zr.File {
		if f.FileInfo().IsDir() {
			continue
		}
		if !strings.HasPrefix(f.Name, "images/train_classes/") {
			continue
		}
		base := filepath.Base(f.Name)
		if !strings.HasSuffix(strings.ToLower(base), ".png") {
			continue
		}
		if err := os.MkdirAll(outDir, 0o755); err != nil {
			log.Printf("[import] mkdir thumbs dir: %v", err)
			return
		}
		rc, err := f.Open()
		if err != nil {
			log.Printf("[import] open thumb %s: %v", f.Name, err)
			continue
		}
		dst, err := os.Create(filepath.Join(outDir, base))
		if err != nil {
			rc.Close()
			log.Printf("[import] create thumb %s: %v", base, err)
			continue
		}
		if _, err := io.Copy(dst, rc); err != nil {
			log.Printf("[import] copy thumb %s: %v", base, err)
		}
		dst.Close()
		rc.Close()
	}
}

func (h *TimetableHandler) ImportRouteZip(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(r.Body)
	if err != nil {
		util.Error(w, 400, "failed to read request body: "+err.Error())
		return
	}

	zr, err := zip.NewReader(bytes.NewReader(body), int64(len(body)))
	if err != nil {
		util.Error(w, 400, "invalid zip file: "+err.Error())
		return
	}

	// Pull any class-thumbnail PNGs out of the zip first — the
	// extractor packs them under `images/train_classes/*.png` so a
	// receiver without TSW6 paks installed still gets the same
	// images the /train-classes page renders. We write to
	// <appDir>/images/train_classes/ to match the static-file route
	// in router/static.go that serves /images/.
	extractClassThumbnailsFromZip(zr)

	progress := progressFromContext(r.Context())
	// Pre-count per-service JSONs so the progress callback can show
	// "done/total". Same filter the per-service loop uses below — skip
	// route_*.json (handled in pass 1) and *_ribbons.json metadata.
	totalServices := 0
	if progress != nil {
		for _, f := range zr.File {
			if f.FileInfo().IsDir() || !strings.HasSuffix(strings.ToLower(f.Name), ".json") {
				continue
			}
			base := filepath.Base(f.Name)
			if strings.HasPrefix(base, "route_") || strings.HasSuffix(base, "_ribbons.json") {
				continue
			}
			totalServices++
		}
	}

	var (
		routeName      string
		routeCreated   bool
		routeID        *int
		countryName    string
		countryCreated bool
		countryID      *int
		formationsCreated  []string
		ttImported     int
		ttSkipped      int
		errs           []importError
		routeFileSeen  bool
	)
	formationIDByName := map[string]int{} // formation_name → trains.id
	seenFormations := map[string]bool{}

	// Per-entry route resolver. Each timetable file in a cargo / scenario
	// DLC zip can reference a different parent route via its
	// cross_pak_reference_name — e.g. CL-Nuclear ships scenarios across
	// WCMLPresCar, EustonMiltonKeynes, and TrainingCentre. Resolving
	// per-entry routes each timetable to the right route instead of
	// collapsing them all to the first one we see.
	//
	// Lookup order:
	//   1. existing route by cross_pak_reference_name (the canonical key)
	//   2. existing route by name == cross-pak ref (legacy: routes
	//      created before the column existed sometimes had the ref as
	//      their name)
	//   3. existing route by name == RouteDisplayName(ref)
	//   4. create new route with the display name + stamp the column
	//
	// Cached per-import so repeated refs in the same zip don't requery.
	// Declared before Pass 1 so Pass 1 can seed the cache from the
	// route_*.json's cross_pak_reference_name field.
	routeIDByCrossPakRef := map[string]*int{}
	resolveRouteByCrossPakRef := func(ref string) *int {
		if ref == "" {
			return nil
		}
		if rid, ok := routeIDByCrossPakRef[ref]; ok {
			return rid
		}
		displayName := output.RouteDisplayName(ref)
		var rid int
		// (1) existing match by canonical column.
		if h.db.QueryRow("SELECT id FROM routes WHERE cross_pak_reference_name = ?", ref).Scan(&rid) == nil {
			routeIDByCrossPakRef[ref] = &rid
			return &rid
		}
		// (2) legacy: route name happens to equal the ref.
		if h.db.QueryRow("SELECT id FROM routes WHERE name = ?", ref).Scan(&rid) == nil {
			h.db.Exec("UPDATE routes SET cross_pak_reference_name = ? WHERE id = ? AND (cross_pak_reference_name IS NULL OR cross_pak_reference_name = '')", ref, rid)
			routeIDByCrossPakRef[ref] = &rid
			return &rid
		}
		// (3) match by display name (e.g. existing "WCML South" row that
		// predates this column — stamp the ref so future imports hit
		// path (1) directly).
		if h.db.QueryRow("SELECT id FROM routes WHERE name = ?", displayName).Scan(&rid) == nil {
			h.db.Exec("UPDATE routes SET cross_pak_reference_name = ? WHERE id = ? AND (cross_pak_reference_name IS NULL OR cross_pak_reference_name = '')", ref, rid)
			routeIDByCrossPakRef[ref] = &rid
			return &rid
		}
		// (4) create.
		// routes.country_id is NOT NULL with FK to countries(id). If
		// Pass 1 didn't resolve a country (zip has no country fields
		// AND no codename override), there's nothing safe to insert
		// — bail and the per-entry timetable will get route_id=NULL
		// instead of a phantom route linked to a non-existent country.
		if countryID == nil {
			// Last-ditch: try the codename override on the ref name
			// itself (Training Centre's ref happens to equal its
			// codename, so this catches that case).
			if override := output.CountryOverrideForCodename(ref); override != "" {
				var cid int
				if err := h.db.QueryRow("SELECT id FROM countries WHERE code = ?", override).Scan(&cid); err == nil {
					countryID = &cid
				}
			}
		}
		if countryID == nil {
			errs = append(errs, importError{File: "(cross_pak_reference_name=" + ref + ")", Error: "cannot create parent route: no country resolved (re-extract or set the country override for this codename)"})
			routeIDByCrossPakRef[ref] = nil
			return nil
		}
		cid := *countryID
		res, err := h.db.Exec("INSERT INTO routes (name, country_id, tsw_version, cross_pak_reference_name) VALUES (?, ?, 6, ?)", displayName, cid, ref)
		if err != nil {
			errs = append(errs, importError{File: "(cross_pak_reference_name=" + ref + ")", Error: "create parent route: " + err.Error()})
			routeIDByCrossPakRef[ref] = nil
			return nil
		}
		id64, _ := res.LastInsertId()
		rid = int(id64)
		routeIDByCrossPakRef[ref] = &rid
		// Surface the first auto-created route in the response so the
		// user knows what was created. Subsequent autocreations are
		// silent — the UI will list them under timetablesImported.
		if routeName == "" {
			routeName = displayName
			routeCreated = true
		}
		return &rid
	}

	// Pass 1: route file. Identified by filename prefix "route_" so we don't
	// confuse it with per-service files (which can be named anything).
	for _, f := range zr.File {
		if f.FileInfo().IsDir() {
			continue
		}
		base := filepath.Base(f.Name)
		if !strings.HasPrefix(base, "route_") || !strings.HasSuffix(strings.ToLower(base), ".json") {
			continue
		}
		rc, err := f.Open()
		if err != nil {
			errs = append(errs, importError{File: f.Name, Error: "cannot open: " + err.Error()})
			continue
		}
		data, err := io.ReadAll(rc)
		rc.Close()
		if err != nil {
			errs = append(errs, importError{File: f.Name, Error: "cannot read: " + err.Error()})
			continue
		}
		var rf map[string]any
		if err := json.Unmarshal(data, &rf); err != nil {
			errs = append(errs, importError{File: f.Name, Error: "invalid JSON: " + err.Error()})
			continue
		}

		// Country. Prefer matching by ISO code (the canonical key the
		// flag CSS keys off) over by name — name lookups can drift to
		// stale duplicate rows like "USA" vs "United States". When
		// matching by name, also backfill the country's `code` column
		// if it was previously NULL but the JSON now carries one.
		cn, _ := rf["country"].(string)
		ccode, _ := rf["country_code"].(string)
		cn = strings.TrimSpace(cn)
		ccode = strings.TrimSpace(ccode)
		// Older zips were extracted before the country-override
		// landed and may have empty country / country_code fields
		// (Training Centre is the canonical case — the asset itself
		// has a blank Country). Fall back to the per-codename
		// override using the route codename in the JSON. This keeps
		// re-imports of stale zips working without forcing a
		// re-extract, and matches the catalog scan's behaviour.
		if ccode == "" && cn == "" {
			if codename, _ := rf["route"].(string); codename != "" {
				if override := output.CountryOverrideForCodename(codename); override != "" {
					ccode = override
					cn = output.CountryNameFromCode(override)
				}
			}
		}
		if cn != "" || ccode != "" {
			countryName = cn
			var cid int
			matched := false
			if ccode != "" {
				if err := h.db.QueryRow("SELECT id FROM countries WHERE code = ?", ccode).Scan(&cid); err == nil {
					countryID = &cid
					matched = true
				}
			}
			if !matched && cn != "" {
				if err := h.db.QueryRow("SELECT id FROM countries WHERE name = ?", cn).Scan(&cid); err == nil {
					countryID = &cid
					matched = true
					if ccode != "" {
						h.db.Exec("UPDATE countries SET code = ? WHERE id = ? AND (code IS NULL OR code = '')", ccode, cid)
					}
				}
			}
			if !matched {
				res, err := h.db.Exec("INSERT INTO countries (name, code) VALUES (?, ?)", cn, nilIfEmpty(ccode))
				if err != nil {
					errs = append(errs, importError{File: f.Name, Error: "create country: " + err.Error()})
					continue
				}
				id64, _ := res.LastInsertId()
				cid = int(id64)
				countryID = &cid
				countryCreated = true
			}
		}

		// Route. Prefer the display name ("Isle of Wight") for storage —
		// it's what users see in listings, the /start picker, and the
		// /extractor page. Fall back to the codename ("IsleOfWight") only
		// if the display name is missing (older zips from before the
		// extractor emitted both).
		rn, _ := rf["name"].(string)
		if rn == "" {
			rn, _ = rf["route"].(string)
		}
		if rn == "" {
			errs = append(errs, importError{File: f.Name, Error: "route file missing name/route"})
			continue
		}
		routeName = rn
		// cross_pak_reference_name is the canonical key we want to match
		// on. Prefer it over the human display name when looking up an
		// existing route — that way a re-import of WCML South will hit
		// the same row even if its display name was renamed in the UI.
		crossPakRef, _ := rf["cross_pak_reference_name"].(string)
		var rid int
		matched := false
		if crossPakRef != "" {
			if h.db.QueryRow("SELECT id FROM routes WHERE cross_pak_reference_name = ?", crossPakRef).Scan(&rid) == nil {
				routeID = &rid
				matched = true
			}
		}
		if !matched {
			if h.db.QueryRow("SELECT id FROM routes WHERE name = ?", rn).Scan(&rid) == nil {
				routeID = &rid
				matched = true
			}
		}
		if !matched {
			if countryID == nil {
				// routes.country_id is NOT NULL → INSERT with 0 trips
				// the FK to countries(id). Fail with a clear message
				// rather than a confusing FK error so the user knows
				// to set the country (or re-extract with the override).
				errs = append(errs, importError{File: f.Name, Error: "cannot create route " + rn + ": JSON has no country / country_code and no codename override matched. Re-extract the route or set its country manually."})
				continue
			}
			cid := *countryID
			res, err := h.db.Exec("INSERT INTO routes (name, country_id, tsw_version, cross_pak_reference_name) VALUES (?, ?, 6, ?)", rn, cid, nilIfEmpty(crossPakRef))
			if err != nil {
				errs = append(errs, importError{File: f.Name, Error: "create route: " + err.Error()})
				continue
			}
			id64, _ := res.LastInsertId()
			rid = int(id64)
			routeID = &rid
			routeCreated = true
		}
		// Backfill cross_pak_reference_name on existing rows that predate
		// the column or were created without it. Only writes when the
		// column is currently NULL/empty so we never clobber a stamped
		// value with a different one.
		if crossPakRef != "" && routeID != nil {
			h.db.Exec("UPDATE routes SET cross_pak_reference_name = ? WHERE id = ? AND (cross_pak_reference_name IS NULL OR cross_pak_reference_name = '')", crossPakRef, *routeID)
			// Seed the per-import resolver cache so per-entry refs to
			// the same key short-circuit the SQL lookup.
			rid := *routeID
			routeIDByCrossPakRef[crossPakRef] = &rid
		}

		// Replace route_coordinates with the rails GeoJSON FeatureCollection
		// itself (the whole `features` array — viewer-ready as a single blob).
		if feats, ok := rf["features"].([]any); ok && routeID != nil {
			featJSON, _ := json.Marshal(feats)
			h.db.Exec("DELETE FROM route_coordinates WHERE route_id = ?", *routeID)
			h.db.Exec("INSERT INTO route_coordinates (route_id, coordinates) VALUES (?, ?)", *routeID, string(featJSON))

			// Pre-built per-timetable feature blobs reference the OLD
			// route GeoJSON we just dropped. Delete them so the next
			// timetable build (later in this import) populates fresh ones,
			// and any timetable that doesn't get re-imported in this pass
			// rebuilds lazily on first read instead of returning stale data.
			h.db.Exec(`DELETE FROM timetable_map_features
				WHERE timetable_id IN (SELECT id FROM timetables WHERE route_id = ?)`,
				*routeID)
		}

		// Replace route_markers from platform / signal / switch Point features,
		// AND write the new pak-derived car_stop_signs + track_markers tables
		// from features tagged with `feature_kind`.
		if feats, ok := rf["features"].([]any); ok && routeID != nil {
			h.db.Exec("DELETE FROM route_markers WHERE route_id = ?", *routeID)
			h.db.Exec("DELETE FROM car_stop_signs WHERE route_id = ?", *routeID)
			h.db.Exec("DELETE FROM track_markers WHERE route_id = ?", *routeID)
			for _, raw := range feats {
				feat, ok := raw.(map[string]any)
				if !ok {
					continue
				}
				geom, _ := feat["geometry"].(map[string]any)
				if geom == nil {
					continue
				}
				if t, _ := geom["type"].(string); t != "Point" {
					continue
				}
				coords, _ := geom["coordinates"].([]any)
				if len(coords) < 2 {
					continue
				}
				lng, _ := coords[0].(float64)
				lat, _ := coords[1].(float64)
				props, _ := feat["properties"].(map[string]any)
				if props == nil {
					props = map[string]any{}
				}

				// New pak-derived feature kinds — branch BEFORE the legacy
				// classifyMarker fall-through so they don't pollute the
				// player-recorded route_markers table.
				kind, _ := props["feature_kind"].(string)
				switch kind {
				case "car_stop_sign":
					ribbonGUID, _ := props["ribbon_guid"].(string)
					location, _ := props["location"].(float64)
					maxRail := 0
					if v, ok := props["max_rail_vehicles"].(float64); ok {
						maxRail = int(v)
					}
					h.db.Exec(`INSERT INTO car_stop_signs
						(route_id, ribbon_guid, location, max_rail_vehicles, latitude, longitude)
						VALUES (?, ?, ?, ?, ?, ?)`,
						*routeID, ribbonGUID, location, maxRail, lat, lng)
					continue
				case "track_marker":
					name, _ := props["name"].(string)
					mtype, _ := props["marker_type"].(string)
					ribbonGUID, _ := props["ribbon_guid"].(string)
					lineSide, _ := props["line_side"].(string)
					location, _ := props["location"].(float64)
					start, _ := props["start"].(float64)
					end, _ := props["end"].(float64)
					if name == "" {
						continue
					}
					h.db.Exec(`INSERT INTO track_markers
						(route_id, name, marker_type, ribbon_guid, location, start, end, line_side, latitude, longitude)
						VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
						*routeID, name, mtype, ribbonGUID, location, start, end, lineSide, lat, lng)
					continue
				}

				// Legacy path: platforms / signals / switches → route_markers.
				stationName, mtype := classifyMarker(props)
				if stationName == "" {
					continue
				}
				h.db.Exec(`INSERT OR REPLACE INTO route_markers
					(route_id, station_name, marker_type, latitude, longitude)
					VALUES (?, ?, ?, ?, ?)`,
					*routeID, stationName, mtype, lat, lng)
			}

			// Denormalise platform_name onto each car_stop_sign by joining
			// to track_markers on shared ribbon_guid (where marker_type is
			// "Platform" — that's the marker kind tied to a stoppable
			// platform). This is what makes the runtime lookup
			// (route_id + platform_name + max_rail_vehicles) hit a single
			// index.
			h.db.Exec(`UPDATE car_stop_signs
				SET platform_name = (
					SELECT name FROM track_markers
					WHERE track_markers.route_id = car_stop_signs.route_id
					  AND track_markers.ribbon_guid = car_stop_signs.ribbon_guid
					  AND track_markers.marker_type = 'Platform'
					LIMIT 1
				)
				WHERE route_id = ?`, *routeID)
		}
		routeFileSeen = true
		break // exactly one route file per zip
	}

	// Pass 2: per-service files. Skip the route file (already handled above)
	// and any non-.json. Each per-service file carries its own formation_name
	// + trains[] consist details; resolve trains lazily.
	//
	// routeFeatsCache is per-import scratch state for buildTimetableMap-
	// FeaturesCached — every timetable on the same route shares one parse
	// of the 5-15 MB route GeoJSON. Without this Boston Sprinter (5,500
	// timetables × 12 MB blob × 50-200 ms parse) stalls the import for
	// 5-20 minutes after "[auto-import] starting…".
	routeFeatsCache := map[int][]map[string]any{}
	for _, f := range zr.File {
		if f.FileInfo().IsDir() || !strings.HasSuffix(strings.ToLower(f.Name), ".json") {
			continue
		}
		base := filepath.Base(f.Name)
		if strings.HasPrefix(base, "route_") {
			continue // already handled in pass 1
		}
		// Skip the lightweight ribbons.json metadata file (not a service)
		if strings.HasSuffix(base, "_ribbons.json") {
			continue
		}

		rc, err := f.Open()
		if err != nil {
			errs = append(errs, importError{File: f.Name, Error: "cannot open: " + err.Error()})
			continue
		}
		data, err := io.ReadAll(rc)
		rc.Close()
		if err != nil {
			errs = append(errs, importError{File: f.Name, Error: "cannot read: " + err.Error()})
			continue
		}
		var entry map[string]any
		if err := json.Unmarshal(data, &entry); err != nil {
			errs = append(errs, importError{File: f.Name, Error: "invalid JSON: " + err.Error()})
			continue
		}

		serviceName, _ := entry["serviceName"].(string)
		if serviceName == "" {
			errs = append(errs, importError{File: f.Name, Error: "missing serviceName"})
			continue
		}

		// Resolve this entry's parent route per-file via its
		// cross_pak_reference_name. Cargo / scenario DLCs ship timetables
		// across multiple parent routes in one pak (CL-Nuclear has 8
		// timetables split across WCMLPresCar, EustonMiltonKeynes, and
		// TrainingCentre); each goes to its own route. Falls back to the
		// zip-level routeID (from route_*.json, if any) for older zips
		// without the field.
		entryRouteID := routeID
		if ref, _ := entry["cross_pak_reference_name"].(string); ref != "" {
			if rid := resolveRouteByCrossPakRef(ref); rid != nil {
				entryRouteID = rid
			}
		}

		// Duplicate by service_name? Don't blindly skip — when a service is
		// split across multiple .pak files (e.g. Boston Sprinter ships its
		// main timetable in BostonProvidence.pak and the HSP-46 variant in
		// BostonProvidenceGameplayPack.pak), each pak imports separately and
		// the second one's section + train linkages would be lost. Merge
		// them into the existing row, then continue without re-inserting.
		//
		// Match rule: same service_name AND existing.route_id is either the
		// resolved route OR NULL. The NULL leg matters: a previous pass that
		// failed to resolve a route inserted an orphan (route_id NULL); the
		// current pass needs to find and UPGRADE that orphan rather than
		// insert a parallel canonical row beside it (cause of the GWE dupe
		// epidemic — 5,535 orphan/canonical pairs all on Great Western
		// Express). Canonical rows are preferred over orphans when both
		// exist, so the orphan stays for cleanup-by-delete tooling.
		var existingID int
		var existingRouteID *int
		err = h.db.QueryRow(`
			SELECT id, route_id FROM timetables
			WHERE service_name = ?
			  AND (route_id = ? OR route_id IS NULL)
			ORDER BY (route_id IS NULL) ASC
			LIMIT 1
		`, serviceName, entryRouteID).Scan(&existingID, &existingRouteID)
		if err == nil {
			// If we matched an orphan and we DO have a route_id this pass,
			// upgrade the orphan's route_id (and section_id when known) so
			// it stops being an orphan. Skip when entryRouteID is also nil
			// (orphan-merge-into-orphan: nothing to upgrade).
			if existingRouteID == nil && entryRouteID != nil {
				h.db.Exec("UPDATE timetables SET route_id = ? WHERE id = ? AND route_id IS NULL", *entryRouteID, existingID)
			}
			h.mergeServiceLinkagesIntoExisting(existingID, entryRouteID, entry, &errs, f.Name, formationIDByName, seenFormations, &formationsCreated)
			ttSkipped++
			if progress != nil {
				label := serviceName + " (merged)"
				progress(ttImported+ttSkipped, totalServices, label)
			}
			continue
		}

		// Resolve / create the train this service runs on. formation_name is
		// the canonical name in the route file's terms; fall back to the
		// per-service trains[].class (display name) when missing.
		formationName, _ := entry["formation_name"].(string)
		formationName = strings.TrimSpace(formationName)
		var formationID *int
		if formationName != "" {
			tid, created, err := h.resolveFormationFromService(formationName, entry)
			if err != nil {
				errs = append(errs, importError{File: f.Name, Error: "resolve train: " + err.Error()})
			} else {
				formationID = &tid
				formationIDByName[formationName] = tid
				if created && !seenFormations[formationName] {
					seenFormations[formationName] = true
					formationsCreated = append(formationsCreated, formationName)
				}
			}
		}

		serviceType, _ := entry["serviceType"].(string)
		if serviceType == "" {
			serviceType = "passenger"
		}
		contributor, _ := entry["contributor"].(string)
		service, _ := entry["service"].(string)
		bound, _ := entry["bound"].(string)
		conductorVal := 0
		if v, ok := entry["conductorCompatible"].(bool); ok && v {
			conductorVal = 1
		}
		currentServiceName, _ := entry["current_service_name"].(string)
		startTime, _ := entry["startTime"].(string)
		duration, _ := entry["duration"].(string)
		source, _ := entry["source"].(string)
		playableVal := 0
		if v, ok := entry["playable"].(bool); ok && v {
			playableVal = 1
		}

		// Sections (still per-route concept). New extractor emits sectionNames[].
		var sectionIDs []int
		if entryRouteID != nil {
			if sectionNames, ok := entry["sectionNames"].([]any); ok {
				for _, sn := range sectionNames {
					name, ok := sn.(string)
					if !ok || name == "" {
						continue
					}
					sid, err := h.findOrCreateSection(*entryRouteID, name)
					if err == nil {
						sectionIDs = append(sectionIDs, sid)
					}
				}
			}
		}
		var sectionID *int
		if len(sectionIDs) > 0 {
			sectionID = &sectionIDs[0]
		}

		res, err := h.db.Exec(`INSERT INTO timetables
			(service_name, route_id, formation_id, service_type, contributor, bound, service,
			 section_id, conductor_compatible, current_service_name, start_time, duration,
			 source, playable)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			serviceName, entryRouteID, formationID, serviceType,
			nilIfEmpty(contributor), nilIfEmpty(bound), nilIfEmpty(service),
			sectionID, conductorVal, nilIfEmpty(currentServiceName),
			nilIfEmpty(startTime), nilIfEmpty(duration),
			nilIfEmpty(source), playableVal)
		if err != nil {
			errs = append(errs, importError{File: f.Name, Error: "insert timetable: " + err.Error()})
			continue
		}
		ttID64, _ := res.LastInsertId()
		ttID := int(ttID64)

		if formationID != nil {
			h.db.Exec("INSERT OR IGNORE INTO timetable_formations (timetable_id, formation_id) VALUES (?, ?)", ttID, *formationID)
			// Also link the train to the route (powers /routes/<id>'s
			// "Trains on this Route" panel). INSERT OR IGNORE so re-imports
			// of the same train across services don't duplicate.
			if entryRouteID != nil {
				h.db.Exec("INSERT OR IGNORE INTO route_formations (route_id, formation_id) VALUES (?, ?)", *entryRouteID, *formationID)
			}
		}
		// additional_formations: extra trains contributed by sibling
		// timetable binaries that declared this same service. Each entry
		// has its own formation_name + trains[] consist data so we can
		// resolve each independently and link them all to the single
		// timetable row via timetable_formations.
		if extras, ok := entry["additional_formations"].([]any); ok {
			for _, e := range extras {
				em, ok := e.(map[string]any)
				if !ok {
					continue
				}
				extraFormation, _ := em["formation_name"].(string)
				extraFormation = strings.TrimSpace(extraFormation)
				if extraFormation == "" {
					continue
				}
				eid, created, err := h.resolveFormationFromService(extraFormation, em)
				if err != nil {
					errs = append(errs, importError{File: f.Name, Error: "resolve additional train (" + extraFormation + "): " + err.Error()})
					continue
				}
				formationIDByName[extraFormation] = eid
				if created && !seenFormations[extraFormation] {
					seenFormations[extraFormation] = true
					formationsCreated = append(formationsCreated, extraFormation)
				}
				h.db.Exec("INSERT OR IGNORE INTO timetable_formations (timetable_id, formation_id) VALUES (?, ?)", ttID, eid)
				if entryRouteID != nil {
					h.db.Exec("INSERT OR IGNORE INTO route_formations (route_id, formation_id) VALUES (?, ?)", *entryRouteID, eid)
				}
			}
		}
		for _, sid := range sectionIDs {
			h.db.Exec("INSERT OR IGNORE INTO timetable_sections (timetable_id, section_id) VALUES (?, ?)", ttID, sid)
		}

		// Coordinates: full physical path along the rail network. The new
		// extractor emits one object per point with latitude/longitude/height.
		if coords, ok := entry["coordinates"].([]any); ok && len(coords) > 0 {
			coordSource := "backend"
			if cs, ok := entry["coordinates_source"].(string); ok && cs != "" {
				coordSource = cs
			}
			coordJSON, _ := json.Marshal(coords)
			h.db.Exec("INSERT INTO timetable_coordinates (timetable_id, coordinates, coord_source) VALUES (?, ?, ?)", ttID, string(coordJSON), coordSource)
			if cc, ok := entry["coordinates_contributor"].(string); ok && cc != "" {
				h.db.Exec("UPDATE timetables SET coordinates_contributor = ? WHERE id = ?", cc, ttID)
			}
		}

		// Per-stop markers (legacy compat — new extractor doesn't emit
		// markers[] today, but accept them if present).
		if markers, ok := entry["markers"].([]any); ok {
			for _, m := range markers {
				mMap, ok := m.(map[string]any)
				if !ok {
					continue
				}
				stationName, _ := mMap["stationName"].(string)
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
					ttID, stationName, markerType, lat, lng, platLen)
			}
		}

		// Schedule rows from csvData.
		if csvData, ok := entry["csvData"].([]any); ok {
			for i, item := range csvData {
				e, ok := item.(map[string]any)
				if !ok {
					continue
				}
				action, _ := e["action"].(string)
				action = strings.ToUpper(strings.TrimSpace(action))
				details, _ := e["details"].(string)
				location, _ := e["location"].(string)
				location = strings.TrimSpace(location)
				structureNumber, _ := e["structure_number"].(string)
				structure, _ := e["structure"].(string)
				time1, _ := e["time1"].(string)
				time2, _ := e["time2"].(string)
				lat, _ := e["latitude"].(string)
				lng, _ := e["longitude"].(string)
				coordSrc, _ := e["coord_source"].(string)

				isUnload := action == "UNLOAD PASSENGERS"
				if isUnload {
					location = ""
					details = ""
				}

				actionID := h.resolveActionID(action)
				var locID *int
				if location != "" && entryRouteID != nil {
					lid, err := h.findOrCreateLocation(*entryRouteID, location)
					if err == nil {
						locID = &lid
					}
				}
				if isUnload {
					locID = nil
				}

				h.db.Exec(`INSERT INTO timetable_entries
					(timetable_id, action_id, details, location_id, structure_number, structure,
					 time1, time2, latitude, longitude, sort_order, coord_source)
					VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
					ttID, actionID, details, locID, structureNumber, nilIfEmpty(structure),
					time1, time2, lat, lng, i, nilIfEmpty(coordSrc))
			}
		}

		// Pre-resolve car_stop_sign_id for every entry in this timetable so
		// the HUD can do a single PK lookup at runtime rather than recomputing
		// the (platform, car-count, direction) match on every position update.
		if entryRouteID != nil && formationID != nil {
			h.resolveCarStopSignsForTimetable(ttID, *entryRouteID, *formationID, bound)
		}
		if entryRouteID != nil {
			h.resolveTrackMarkersForTimetable(ttID, *entryRouteID)
		}

		// Build the per-timetable filtered map-features blob now that the
		// path coords and stop-marker resolutions are in place. Doing it
		// here at import time means /api/timetables/{id}/map-features serves
		// a single SELECT instead of fetching the whole route GeoJSON and
		// running the per-feature filter on every page load. Pass the
		// per-import cache so the route GeoJSON parses once across all
		// timetables on the same route (the Boston Sprinter unstall).
		// Pre-build is opt-in via the ExtractorBuildTimetableMaps setting.
		// Default-off keeps the importer fast on big DLCs; the user builds
		// lazily on demand from /routes/{id}/edit, /timetables/{id}, or
		// implicitly when a HUD loads a timetable that lacks features.
		if entryRouteID != nil && config.Get().ExtractorBuildTimetableMaps {
			h.buildTimetableMapFeaturesCached(ttID, *entryRouteID, routeFeatsCache)
		}

		ttImported++
		if progress != nil {
			label := serviceName
			if label == "" {
				label = filepath.Base(f.Name)
			}
			progress(ttImported+ttSkipped, totalServices, label)
		}
	}

	// Only complain about a missing route_*.json if no per-entry
	// cross_pak_reference_name resolved a route either.
	if !routeFileSeen && routeID == nil && len(routeIDByCrossPakRef) == 0 {
		errs = append(errs, importError{File: "(none)", Error: "no route_*.json file in zip and no cross_pak_reference_name in per-service files — route metadata missing"})
	}
	if formationsCreated == nil {
		formationsCreated = []string{}
	}
	if errs == nil {
		errs = []importError{}
	}

	util.JSON(w, 200, map[string]any{
		"route": map[string]any{
			"name":    routeName,
			"created": routeCreated,
		},
		"country": map[string]any{
			"name":    countryName,
			"created": countryCreated,
		},
		"formationsCreated":      formationsCreated,
		"timetablesImported": ttImported,
		"timetablesSkipped":  ttSkipped,
		"errors":             errs,
	})
}

// mergeServiceLinkagesIntoExisting absorbs section + train info from a
// per-service JSON into an already-imported timetable row, then drops the
// rest of the entry. Used for cross-pak duplicates: when a service is
// declared in two .pak files (e.g. Boston Sprinter's main pak +
// BostonProvidenceGameplayPack with the HSP-46 timetable), the second
// import would otherwise be dropped wholesale by the (service_name,
// route_id) dedup, taking its sections + formations down with it.
//
// Other fields (schedule, coords, contributor, etc.) follow first-wins —
// we trust the canonical pak's data and don't try to reconcile differences.
//
// Errors are appended to `errs` (caller's slice, mutated through the
// pointer) so a single bad linkage doesn't abort the whole import.
func (h *TimetableHandler) mergeServiceLinkagesIntoExisting(
	existingID int,
	entryRouteID *int,
	entry map[string]any,
	errs *[]importError,
	fileName string,
	formationIDByName map[string]int,
	seenFormations map[string]bool,
	formationsCreated *[]string,
) {
	// Sections — link any from the JSON's sectionNames[] that aren't
	// already linked. INSERT OR IGNORE makes the loop idempotent across
	// re-imports.
	if entryRouteID != nil {
		if sectionNames, ok := entry["sectionNames"].([]any); ok {
			for _, sn := range sectionNames {
				name, ok := sn.(string)
				if !ok || strings.TrimSpace(name) == "" {
					continue
				}
				sid, err := h.findOrCreateSection(*entryRouteID, name)
				if err != nil {
					*errs = append(*errs, importError{File: fileName, Error: "merge section (" + name + "): " + err.Error()})
					continue
				}
				h.db.Exec("INSERT OR IGNORE INTO timetable_sections (timetable_id, section_id) VALUES (?, ?)", existingID, sid)
			}
		}
	}

	// Trains — resolve the canonical formation (entry["formation_name"])
	// AND every additional_formations[].formation_name, link each via
	// timetable_formations + route_formations. Same resolver the main path uses.
	resolveAndLink := func(formation string, payload map[string]any) {
		formation = strings.TrimSpace(formation)
		if formation == "" {
			return
		}
		tid, created, err := h.resolveFormationFromService(formation, payload)
		if err != nil {
			*errs = append(*errs, importError{File: fileName, Error: "merge train (" + formation + "): " + err.Error()})
			return
		}
		formationIDByName[formation] = tid
		if created && !seenFormations[formation] {
			seenFormations[formation] = true
			*formationsCreated = append(*formationsCreated, formation)
		}
		h.db.Exec("INSERT OR IGNORE INTO timetable_formations (timetable_id, formation_id) VALUES (?, ?)", existingID, tid)
		if entryRouteID != nil {
			h.db.Exec("INSERT OR IGNORE INTO route_formations (route_id, formation_id) VALUES (?, ?)", *entryRouteID, tid)
		}
	}

	formationName, _ := entry["formation_name"].(string)
	resolveAndLink(formationName, entry)

	if extras, ok := entry["additional_formations"].([]any); ok {
		for _, e := range extras {
			em, ok := e.(map[string]any)
			if !ok {
				continue
			}
			extraFormation, _ := em["formation_name"].(string)
			resolveAndLink(extraFormation, em)
		}
	}
}

// resolveFormationFromService finds-or-creates a trains row from a per-service
// JSON.
//
// Lookup priority:
//  1. Vehicle-GUID-set match (exact composition of the same physical
//     vehicles) — this is the canonical identity of a train.
//  2. Exact formation_name match — only when the service has no vehicle
//     data (legacy imports).
//
// The order matters: scenario services across paks reuse generic formation
// names like "PlayerFormation" / "AIFormation". Matching by name first
// would let an IoW PlayerFormation (Class 483 vehicles) collide with a
// Boston PlayerFormation (CTC-3 Cab Car vehicles). Matching by GUID set
// classMetaAgg holds the class-level aggregate values rolled up from a
// formation's vehicles list. Each field uses a pointer / nullable type so
// that "absent" (the lead vehicle didn't report it) is distinct from
// "false" / "zero" — that distinction matters when we COALESCE-update
// the train_classes row instead of overwriting good values with zeros.
type classMetaAgg struct {
	isElectric      any // *int (0/1) or nil
	maxSpeed        any // *float64 or nil
	maxPower        any // *float64 or nil
	manufacturer    any // *string or nil
	engineDesc      any // *string or nil
	typeDesc        any // *string or nil
	vehicleCategory any // *string or nil  — Locomotive / FreightWagon / etc.
	electrification []output.ElectrificationSpec
}

// aggregateClassMetadata rolls a formation's per-vehicle RVD fields up to
// a single class-level summary. Prefers the LEAD vehicle's values (the
// drivable power car / cab car), falls back to the first vehicle that
// actually populates each field. Electrification specs are unioned
// across every vehicle in the consist.
func aggregateClassMetadata(vehicles []map[string]any) classMetaAgg {
	var meta classMetaAgg
	// Two-pass: prefer lead's values, then fill blanks from any vehicle.
	for _, lead := range []bool{true, false} {
		for _, v := range vehicles {
			isLead, _ := v["is_lead"].(bool)
			if lead && !isLead {
				continue
			}
			if lead == false && isLead {
				continue
			}
			if meta.isElectric == nil {
				if b, ok := v["is_electric"].(bool); ok {
					n := 0
					if b {
						n = 1
					}
					meta.isElectric = n
				}
			}
			if meta.maxSpeed == nil {
				if f, ok := v["max_speed_kph"].(float64); ok && f > 0 {
					meta.maxSpeed = f
				}
			}
			if meta.maxPower == nil {
				if f, ok := v["max_power_kw"].(float64); ok && f > 0 {
					meta.maxPower = f
				}
			}
			if meta.manufacturer == nil {
				if s, ok := v["manufacturer_name"].(string); ok && s != "" {
					meta.manufacturer = s
				}
			}
			if meta.engineDesc == nil {
				if s, ok := v["engine_description"].(string); ok && s != "" {
					meta.engineDesc = s
				}
			}
			if meta.typeDesc == nil {
				if s, ok := v["type_description"].(string); ok && s != "" {
					meta.typeDesc = s
				}
			}
			if meta.vehicleCategory == nil {
				if s, ok := v["vehicle_category"].(string); ok && s != "" {
					meta.vehicleCategory = s
				}
			}
			if specs, ok := v["electrification"].([]any); ok {
				for _, sp := range specs {
					m, ok := sp.(map[string]any)
					if !ok {
						continue
					}
					cur, _ := m["current"].(string)
					side, _ := m["pickup_side"].(string)
					var volt, freq int32
					if f, ok := m["voltage_v"].(float64); ok {
						volt = int32(f)
					}
					if f, ok := m["frequency_hz"].(float64); ok {
						freq = int32(f)
					}
					meta.electrification = append(meta.electrification, output.ElectrificationSpec{
						Current: cur, PickupSide: side, VoltageV: volt, FrequencyHz: freq,
					})
				}
			}
		}
	}
	return meta
}

// gives each its own row.
//
// On creation, populates formation_vehicles from the service file's
// trains[].consists[0].vehicles[].
//
// Returns (formation_id, created, error). `created` is true only when a brand-new
// trains row was inserted on this call.
func (h *TimetableHandler) resolveFormationFromService(formationName string, svc map[string]any) (int, bool, error) {
	// Pull the default-class consist (one with is_default=true, or the only
	// entry if none flagged). That entry carries the per-vehicle GUIDs.
	defaultConsist := pickDefaultConsist(svc)
	vehicles := []map[string]any{}
	if defaultConsist != nil {
		if vs, ok := defaultConsist["vehicles"].([]any); ok {
			for _, v := range vs {
				if vm, ok := v.(map[string]any); ok {
					vehicles = append(vehicles, vm)
				}
			}
		}
	}

	// Step 1: vehicle-GUID-set match — the canonical identity. Trains with
	// the same vehicle composition are the same physical formation.
	if len(vehicles) > 0 {
		guids := vehicleGUIDSet(vehicles)
		if existing := h.findFormationByVehicleSet(guids); existing > 0 {
			return existing, false, nil
		}
		// No GUID match → genuinely a new formation, fall through to
		// create. Do NOT fall back to name match: a name collision with a
		// different-vehicle formation from another route would corrupt
		// this train's class.
	} else {
		// Step 2 (legacy): no vehicle data → name match is the only key.
		var existing int
		if h.db.QueryRow("SELECT id FROM formations WHERE name = ?", formationName).Scan(&existing) == nil {
			return existing, false, nil
		}
	}

	// New train: insert a row with display info from the default consist's
	// lead vehicle, then populate formation_vehicles.
	var className, livery string
	var lengthM float64
	if defaultConsist != nil {
		if v, ok := defaultConsist["length_m"].(float64); ok {
			lengthM = v
		}
		// Lead vehicle's class/livery for the train summary.
		for _, v := range vehicles {
			if isLead, _ := v["is_lead"].(bool); isLead {
				className, _ = v["friendly_name"].(string)
				if className == "" {
					className, _ = v["rail_vehicle_class"].(string)
				}
				livery, _ = v["livery_id"].(string)
				break
			}
		}
		if className == "" && len(vehicles) > 0 {
			className, _ = vehicles[0]["friendly_name"].(string)
			if className == "" {
				className, _ = vehicles[0]["rail_vehicle_class"].(string)
			}
		}
	}

	// Find-or-create the train class first; this train will FK into it.
	// Multiple trains can share a class (e.g. two Class 483 sets on IoW).
	var classID *int
	if className != "" {
		var cid int
		meta := aggregateClassMetadata(vehicles)
		if h.db.QueryRow("SELECT id FROM train_classes WHERE name = ?", className).Scan(&cid) == nil {
			classID = &cid
			// Class already exists; refresh aggregates only when the new
			// vehicles' lead carries data we previously didn't have. Cheap
			// COALESCE-style update — never overwrites a non-NULL with NULL.
			h.db.Exec(`UPDATE train_classes SET
				is_electric        = COALESCE(?, is_electric),
				max_speed_kph      = COALESCE(?, max_speed_kph),
				max_power_kw       = COALESCE(?, max_power_kw),
				manufacturer_name  = COALESCE(?, manufacturer_name),
				engine_description = COALESCE(?, engine_description),
				type_description   = COALESCE(?, type_description),
				vehicle_category   = COALESCE(?, vehicle_category)
				WHERE id = ?`,
				meta.isElectric, meta.maxSpeed, meta.maxPower,
				meta.manufacturer, meta.engineDesc, meta.typeDesc,
				meta.vehicleCategory, cid)
		} else {
			// thumbnail_path is the URL the web UI uses; the actual PNG
			// is written under <appDir>/images/train_classes/ during the
			// pak catalog scan. We always set this even when no file
			// exists yet — the page handles 404 with a placeholder.
			thumbURL := catalog.ThumbnailURLPath(className)
			cres, cerr := h.db.Exec(
				`INSERT INTO train_classes
					(name, livery_id, typical_length_m, typical_car_count,
					 is_electric, max_speed_kph, max_power_kw,
					 manufacturer_name, engine_description, type_description,
					 vehicle_category, thumbnail_path)
				 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
				className, nilIfEmpty(livery), lengthM, len(vehicles),
				meta.isElectric, meta.maxSpeed, meta.maxPower,
				meta.manufacturer, meta.engineDesc, meta.typeDesc,
				meta.vehicleCategory, thumbURL)
			if cerr == nil {
				id64, _ := cres.LastInsertId()
				cid = int(id64)
				classID = &cid
			}
		}
		// Union electrification specs into train_class_electrification.
		// INSERT OR IGNORE — the UNIQUE constraint dedupes silently when
		// the same (current, voltage, frequency) tuple already exists for
		// this class.
		if classID != nil {
			for _, e := range meta.electrification {
				h.db.Exec(`INSERT OR IGNORE INTO train_class_electrification
					(train_class_id, current, pickup_side, voltage_v, frequency_hz)
					VALUES (?, ?, ?, ?, ?)`,
					*classID, nilIfEmpty(e.Current), nilIfEmpty(e.PickupSide),
					e.VoltageV, e.FrequencyHz)
			}
		}
	}

	res, err := h.db.Exec(`INSERT INTO formations(name, class_name, livery_id, length_m, car_count, class_id) VALUES (?, ?, ?, ?, ?, ?)`,
		formationName, nilIfEmpty(className), nilIfEmpty(livery), lengthM, len(vehicles), classID)
	if err != nil {
		return 0, false, err
	}
	id64, _ := res.LastInsertId()
	tid := int(id64)
	for i, v := range vehicles {
		vehID, _ := v["vehicle_id"].(string)
		if vehID == "" {
			continue
		}
		fn, _ := v["friendly_name"].(string)
		cls, _ := v["rail_vehicle_class"].(string)
		liv, _ := v["livery_id"].(string)
		cat, _ := v["vehicle_category"].(string)
		var lm float64
		if v2, ok := v["length_m"].(float64); ok {
			lm = v2
		}
		isLead := 0
		if b, _ := v["is_lead"].(bool); b {
			isLead = 1
		}
		isFlipped := 0
		if b, _ := v["is_flipped"].(bool); b {
			isFlipped = 1
		}
		h.db.Exec(`INSERT INTO formation_vehicles
			(formation_id, position, vehicle_id, class_name, friendly_name, livery_id, vehicle_category, length_m, is_lead, is_flipped)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			tid, i, vehID, nilIfEmpty(cls), nilIfEmpty(fn), nilIfEmpty(liv), nilIfEmpty(cat), lm, isLead, isFlipped)
	}
	return tid, true, nil
}

// pickDefaultConsist returns the trains[].consists[0] entry whose class is
// flagged is_default, or the first non-empty consists[0] otherwise.
func pickDefaultConsist(svc map[string]any) map[string]any {
	formations, _ := svc["formations"].([]any)
	for _, t := range formations {
		te, ok := t.(map[string]any)
		if !ok {
			continue
		}
		isDefault, _ := te["is_default"].(bool)
		if !isDefault {
			continue
		}
		consists, _ := te["consists"].([]any)
		if len(consists) == 0 {
			continue
		}
		c, _ := consists[0].(map[string]any)
		if c != nil && len(c) > 0 {
			return c
		}
	}
	// Fallback: first non-empty consists[0]
	for _, t := range formations {
		te, ok := t.(map[string]any)
		if !ok {
			continue
		}
		consists, _ := te["consists"].([]any)
		if len(consists) == 0 {
			continue
		}
		c, _ := consists[0].(map[string]any)
		if c != nil && len(c) > 0 {
			return c
		}
	}
	return nil
}

// vehicleGUIDSet returns the canonical sorted-and-joined GUID list for a
// vehicle slice — used to identify a physical train independent of its
// formation name.
func vehicleGUIDSet(vehicles []map[string]any) string {
	guids := make([]string, 0, len(vehicles))
	for _, v := range vehicles {
		if g, ok := v["vehicle_id"].(string); ok && g != "" {
			guids = append(guids, g)
		}
	}
	sort.Strings(guids)
	return strings.Join(guids, ",")
}

// findFormationByVehicleSet returns an existing trains.id whose formation_vehicles
// rows have exactly the given GUID set, or 0 if none.
func (h *TimetableHandler) findFormationByVehicleSet(targetSig string) int {
	if targetSig == "" {
		return 0
	}
	rows, err := h.db.Query(`SELECT formation_id, GROUP_CONCAT(vehicle_id, ',') FROM (
		SELECT formation_id, vehicle_id FROM formation_vehicles ORDER BY formation_id, vehicle_id
	) GROUP BY formation_id`)
	if err != nil {
		return 0
	}
	defer rows.Close()
	for rows.Next() {
		var tid int
		var sig string
		if rows.Scan(&tid, &sig) != nil {
			continue
		}
		// GROUP_CONCAT order isn't guaranteed across SQLite versions; sort to
		// match our canonical signature.
		parts := strings.Split(sig, ",")
		sort.Strings(parts)
		if strings.Join(parts, ",") == targetSig {
			return tid
		}
	}
	return 0
}

// classifyMarker maps a Point feature's properties to a (station_name,
// marker_type) pair for route_markers. Handles platforms / signals /
// switches; returns ("", "") for unknown shapes (which the caller skips).
func classifyMarker(props map[string]any) (string, string) {
	if v, ok := props["name"].(string); ok && v != "" {
		// Platforms have a "name" field; structure/structure_number give the
		// platform / siding / line tag.
		struc, _ := props["structure"].(string)
		num, _ := props["structure_number"].(string)
		mtype := "Platform"
		if struc != "" {
			mtype = struc
			if num != "" {
				mtype = mtype + " " + num
			}
		}
		return v, mtype
	}
	if v, ok := props["display_label"].(string); ok && v != "" {
		// Signals + switches have display_label like "1 Shunt" / "2 Switch".
		if _, isSignal := props["signal_id"]; isSignal {
			return v, "Signal"
		}
		if _, isSwitch := props["jct_guid"]; isSwitch {
			return v, "Switch"
		}
	}
	return "", ""
}

// resolveCarStopSignsForTimetable populates timetable_entries.car_stop_sign_id
// for every row of the given timetable by joining to car_stop_signs on
// (route_id, platform_name, max_rail_vehicles) and disambiguating by direction
// using the timetable's bound field.
//
// platform_name is reconstructed at query time from
//
//	locations.name + ' ' + structure + ' ' + structure_number
//
// to match the same composition the extractor uses on TrackMarker.name
// (verified against IoW: "Lake Platform 1", "Sandown Siding 2", etc.).
//
// Direction picker:
//   - northbound → highest latitude
//   - southbound → lowest latitude
//   - eastbound  → highest longitude
//   - westbound  → lowest longitude
//
// When two CarStopSigns serve the same platform (one per arrival direction),
// the bound determines which one the train actually stops at. When only one
// sign exists, both directions resolve to the same row.
//
// Trains with car_count NULL fall through to max_rail_vehicles=0 ("any").
//
// Idempotent: re-running the same timetable rewrites the same FKs.
func (h *TimetableHandler) resolveCarStopSignsForTimetable(timetableID, routeID, formationID int, bound string) {
	bound = strings.ToLower(strings.TrimSpace(bound))

	// Pull the train's car count. NULL → 0 (which matches the "any" sentinel
	// in car_stop_signs.max_rail_vehicles).
	var carCount sql.NullInt64
	h.db.QueryRow("SELECT car_count FROM formations WHERE id = ?", formationID).Scan(&carCount)
	cars := 0
	if carCount.Valid {
		cars = int(carCount.Int64)
	}

	// One correlated subquery per row. The CASE in ORDER BY picks the
	// per-bound extreme; LIMIT 1 collapses to a single FK.
	_, err := h.db.Exec(`
		UPDATE timetable_entries
		SET car_stop_sign_id = (
			SELECT css.id FROM car_stop_signs css
			JOIN locations l ON l.id = timetable_entries.location_id
			WHERE css.route_id = ?
			  AND css.platform_name = TRIM(l.name || ' ' ||
			      COALESCE(timetable_entries.structure, '') || ' ' ||
			      COALESCE(timetable_entries.structure_number, ''))
			  AND (css.max_rail_vehicles = ? OR css.max_rail_vehicles = 0)
			ORDER BY
			  CASE ?
				WHEN 'northbound' THEN -css.latitude
				WHEN 'southbound' THEN  css.latitude
				WHEN 'eastbound'  THEN -css.longitude
				WHEN 'westbound'  THEN  css.longitude
				ELSE 0
			  END,
			  -- Prefer car-class-specific signs over the maxCars=0 fallback.
			  CASE WHEN css.max_rail_vehicles = 0 THEN 1 ELSE 0 END
			LIMIT 1
		)
		WHERE timetable_entries.timetable_id = ?
		  AND timetable_entries.location_id IS NOT NULL
	`, routeID, cars, bound, timetableID)
	if err != nil {
		log.Printf("[car-stop-sign-resolve] tt=%d: %v", timetableID, err)
	}
}

// resolveTrackMarkersForTimetable populates timetable_entries.track_marker_id
// for every row of the given timetable by joining to track_markers on
// (route_id, name) where the row's reconstructed name matches.
//
// Reconstructed name uses the same composition rule as the car_stop_sign
// resolver:
//
//	locations.name + ' ' + structure + ' ' + structure_number
//
// matching the extractor's TrackMarker.name field directly. Examples:
//
//	"Lake Platform 1"                  (also covered by car_stop_signs)
//	"Smallbrook Junction Line 1"       (Go Via routing marker — only here)
//	"Sandown Siding 2"                 (depot/yard marker)
//
// A platform row will resolve to BOTH car_stop_sign_id AND track_marker_id;
// downstream code prefers car_stop_sign coords when both exist (the green
// ring is the higher-precision target for stopping). A non-platform row
// (Line / Siding / Track) only resolves to track_marker_id and is what
// drives the in-game "Go Via X" instruction position.
//
// One ribbon GUID can carry the same TrackMarker for multiple ribbon
// segments, so multiple rows may exist with identical name+route_id; we
// LIMIT 1 and accept any of them — they're co-located on the same physical
// platform/junction within a few metres.
//
// Idempotent: re-running the same timetable rewrites the same FKs.
func (h *TimetableHandler) resolveTrackMarkersForTimetable(timetableID, routeID int) {
	_, err := h.db.Exec(`
		UPDATE timetable_entries
		SET track_marker_id = (
			SELECT tm.id FROM track_markers tm
			JOIN locations l ON l.id = timetable_entries.location_id
			WHERE tm.route_id = ?
			  AND tm.name = TRIM(l.name || ' ' ||
			      COALESCE(timetable_entries.structure, '') || ' ' ||
			      COALESCE(timetable_entries.structure_number, ''))
			LIMIT 1
		)
		WHERE timetable_entries.timetable_id = ?
		  AND timetable_entries.location_id IS NOT NULL
	`, routeID, timetableID)
	if err != nil {
		log.Printf("[track-marker-resolve] tt=%d: %v", timetableID, err)
	}
}

// buildTimetableMapFeatures pre-computes the per-timetable filtered route
// GeoJSON blob and stores it in `timetable_map_features`. Called from the
// importer right after car_stop_signs / track_markers are resolved.
//
// Replaces the request-time JS filter in views/js/service-map.js (and the
// duplicates in show.html / edit.html) — the runtime fetch becomes a
// single SELECT instead of pulling the whole route GeoJSON and walking
// every feature × every path vertex on each map render.
//
// Failure is logged and swallowed: a missing blob just means the runtime
// endpoint will return 404 and the frontend can fall back to the legacy
// route-fetch + filter path. We don't want to break the import for what's
// effectively a cache miss.
//
// Convenience wrapper around buildTimetableMapFeaturesCached for callers
// that build a single timetable (the lazy-rebuild path on /api/.../map-
// features). Bulk callers (the import loop) MUST use the cached version
// or every timetable re-parses the 12 MB route GeoJSON, which turns a
// 5,500-service Boston Sprinter import into a 5-20 minute stall.
func (h *TimetableHandler) buildTimetableMapFeatures(timetableID, routeID int) {
	h.buildTimetableMapFeaturesCached(timetableID, routeID, nil)
}

// buildTimetableMapFeaturesCached does the actual work. `routeFeatsCache`
// (nullable) lets a caller share parsed route features across many
// timetables on the same route — saves a JSON parse of the 5-15 MB blob
// per timetable. The cache is keyed by route_id; an empty/nil entry
// triggers a parse and stores it.
func (h *TimetableHandler) buildTimetableMapFeaturesCached(timetableID, routeID int, routeFeatsCache map[int][]map[string]any) {
	routeFeats, err := h.loadRouteFeatures(routeID, routeFeatsCache)
	if err != nil || len(routeFeats) == 0 {
		// No route_coordinates (extractor skipped rails phase) or
		// unparseable. Nothing to filter; leave the row absent. Errors
		// are logged inside the loader.
		return
	}

	// Path coords — read the same blob the renderer uses, decode just the
	// {latitude, longitude} we need for proximity math.
	var pathJSON string
	if err := h.db.QueryRow("SELECT coordinates FROM timetable_coordinates WHERE timetable_id = ?", timetableID).Scan(&pathJSON); err != nil {
		// Service has no path (some scenarios don't ship one). Filter still
		// runs — schedule-tuple matches still work, only proximity drops out.
		pathJSON = "[]"
	}
	var rawCoords []map[string]any
	json.Unmarshal([]byte(pathJSON), &rawCoords)
	pathCoords := make([]output.ServiceCoord, 0, len(rawCoords))
	for _, c := range rawCoords {
		lat, _ := c["latitude"].(float64)
		lng, _ := c["longitude"].(float64)
		pathCoords = append(pathCoords, output.ServiceCoord{Latitude: lat, Longitude: lng})
	}

	// Schedule entries — pull just the (location, structure, number) tuple
	// the filter needs. Mirrors the JS `entries.forEach` index-build loop.
	rows, err := h.db.Query(`
		SELECT COALESCE(l.name, ''),
		       COALESCE(te.structure, ''),
		       COALESCE(te.structure_number, '')
		FROM timetable_entries te
		LEFT JOIN locations l ON l.id = te.location_id
		WHERE te.timetable_id = ?
		ORDER BY te.sort_order
	`, timetableID)
	if err != nil {
		log.Printf("[map-features-build] tt=%d: query entries: %v", timetableID, err)
		return
	}
	var entryRefs []output.ScheduleEntryRef
	for rows.Next() {
		var ref output.ScheduleEntryRef
		if err := rows.Scan(&ref.Location, &ref.Structure, &ref.StructureNumber); err == nil {
			entryRefs = append(entryRefs, ref)
		}
	}
	rows.Close()

	filtered := output.FilterRouteFeaturesForTimetable(routeFeats, entryRefs, pathCoords, output.DefaultFilterOptions())
	blob, err := json.Marshal(filtered)
	if err != nil {
		log.Printf("[map-features-build] tt=%d: marshal: %v", timetableID, err)
		return
	}
	if _, err := h.db.Exec(`
		INSERT INTO timetable_map_features (timetable_id, features)
		VALUES (?, ?)
		ON CONFLICT(timetable_id) DO UPDATE SET
			features = excluded.features,
			built_at = CURRENT_TIMESTAMP
	`, timetableID, string(blob)); err != nil {
		log.Printf("[map-features-build] tt=%d: upsert: %v", timetableID, err)
	}
}

// loadRouteFeatures returns the parsed route GeoJSON features for routeID,
// reading from cache when present and otherwise SELECTing + parsing once
// and storing the result in cache for the rest of the import. The cache
// is per-import scratch state owned by the caller — pass nil to skip
// caching (one-off lazy-rebuild path).
//
// Returns (nil, nil) when route_coordinates has no row for routeID — that
// route's rails phase hasn't run yet. Caller treats this as "no work" and
// silently skips.
func (h *TimetableHandler) loadRouteFeatures(routeID int, cache map[int][]map[string]any) ([]map[string]any, error) {
	if cache != nil {
		if cached, ok := cache[routeID]; ok {
			return cached, nil
		}
	}
	var routeJSON string
	if err := h.db.QueryRow("SELECT coordinates FROM route_coordinates WHERE route_id = ?", routeID).Scan(&routeJSON); err != nil {
		if cache != nil {
			cache[routeID] = nil // negative-cache so we don't re-query per timetable
		}
		return nil, nil
	}
	var feats []map[string]any
	if err := json.Unmarshal([]byte(routeJSON), &feats); err != nil {
		log.Printf("[map-features-build] route=%d: parse route_coordinates: %v", routeID, err)
		if cache != nil {
			cache[routeID] = nil
		}
		return nil, err
	}
	if cache != nil {
		cache[routeID] = feats
	}
	return feats, nil
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
	formations, _ := h.getFormationsForTimetable(id)
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

	formationNames := make([]string, len(formations))
	for i, tr := range formations {
		formationNames[i] = tr.Name
	}

	sectionNames := make([]string, len(sections))
	for i, s := range sections {
		sectionNames[i] = s.Name
	}

	// Build csvData from raw entries
	csvData := make([]map[string]any, len(entries))
	for i, e := range entries {
		csvData[i] = map[string]any{
			"index":            i,
			"action":           ptrStr(e.Action),
			"location":         ptrStr(e.Location),
			"structure":        ptrStr(e.Structure),
			"structure_number": ptrStr(e.StructureNumber),
			"time1":            ptrStr(e.Time1),
			"time2":            ptrStr(e.Time2),
			"details":          ptrStr(e.Details),
			"latitude":         ptrStr(e.Latitude),
			"longitude":        ptrStr(e.Longitude),
			"api_name":         ptrStr(e.ApiName),
			"coord_source":     e.CoordSource,
		}
	}

	// Build timetable entries for export
	timetableEntries := make([]map[string]any, len(entries))
	for i, e := range entries {
		entry := map[string]any{
			"index":            i,
			"location":         ptrStr(e.Location),
			"arrival":          ptrStr(e.Time1),
			"departure":        ptrStr(e.Time2),
			"structure":        ptrStr(e.Structure),
			"structure_number": ptrStr(e.StructureNumber),
			"apiName":          ptrStr(e.ApiName),
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
		"formationNames":              formationNames,
		"serviceType":             t.ServiceType,
		"contributor":             t.Contributor,
		"coordinates_contributor": t.CoordinatesContributor,
		"bound":                   t.Bound,
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
