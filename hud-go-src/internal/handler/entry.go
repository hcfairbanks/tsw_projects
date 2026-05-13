package handler

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"

	"hud-go/internal/util"
)

type EntryHandler struct {
	db *sql.DB
}

func (h *EntryHandler) GetByTimetableID(w http.ResponseWriter, r *http.Request) {
	idStr := chi.URLParam(r, "id")
	timetableID, err := strconv.Atoi(idStr)
	if err != nil {
		util.Error(w, 400, "invalid timetable id")
		return
	}

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
		util.Error(w, 500, err.Error())
		return
	}
	defer rows.Close()

	entries := make([]map[string]any, 0)
	for rows.Next() {
		var (
			id          int
			ttID        int
			actionID    *int
			details     *string
			locationID  *int
			structureNumber *string
			structure       *string
			time1       *string
			time2       *string
			latitude    *string
			longitude   *string
			apiName     *string
			sortOrder   *int
			coordSource *string
			action      *string
			location    *string
		)
		if err := rows.Scan(
			&id, &ttID, &actionID, &details, &locationID,
			&structureNumber, &structure, &time1, &time2, &latitude, &longitude,
			&apiName, &sortOrder, &coordSource,
			&action, &location,
		); err != nil {
			util.Error(w, 500, err.Error())
			return
		}
		entries = append(entries, map[string]any{
			"id":           id,
			"timetable_id": ttID,
			"action_id":    actionID,
			"details":      details,
			"location_id":  locationID,
			"structure_number": structureNumber,
			"structure":    structure,
			"time1":        time1,
			"time2":        time2,
			"latitude":     latitude,
			"longitude":    longitude,
			"api_name":     apiName,
			"sort_order":   sortOrder,
			"coord_source": coordSource,
			"action":       action,
			"location":     location,
		})
	}

	util.JSON(w, 200, entries)
}

func (h *EntryHandler) Create(w http.ResponseWriter, r *http.Request) {
	idStr := chi.URLParam(r, "id")
	timetableID, err := strconv.Atoi(idStr)
	if err != nil {
		util.Error(w, 400, "invalid timetable id")
		return
	}

	var body struct {
		ActionID    *int    `json:"action_id"`
		Action      string  `json:"action"`
		Details     string  `json:"details"`
		LocationID  *int    `json:"location_id"`
		Location    string  `json:"location"`
		StructureNumber string `json:"structure_number"`
		Structure       string `json:"structure"`
		Time1       string  `json:"time1"`
		Time2       string  `json:"time2"`
		Latitude    string  `json:"latitude"`
		Longitude   string  `json:"longitude"`
		ApiName     string  `json:"api_name"`
		SortOrder   *int    `json:"sort_order"`
		CoordSource *string `json:"coord_source"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		util.Error(w, 400, "invalid JSON body")
		return
	}

	// Normalize action
	action := strings.ToUpper(strings.TrimSpace(body.Action))
	if action == "UNLOAD PASSENGERS" {
		body.Location = ""
		body.LocationID = nil
		body.Details = ""
	}

	// Resolve action_id
	actionID := body.ActionID
	if actionID == nil && action != "" {
		var aid int
		if h.db.QueryRow("SELECT id FROM timetable_actions WHERE name = ?", action).Scan(&aid) == nil {
			actionID = &aid
		}
	}

	// Resolve location_id from location name if needed
	locID := body.LocationID
	if locID == nil && strings.TrimSpace(body.Location) != "" {
		// Get the timetable to find route_id
		var routeID *int
		h.db.QueryRow("SELECT route_id FROM timetables WHERE id = ?", timetableID).Scan(&routeID)
		if routeID != nil {
			locName := strings.TrimSpace(body.Location)
			var lid int
			err := h.db.QueryRow("SELECT id FROM locations WHERE route_id = ? AND name = ?", *routeID, locName).Scan(&lid)
			if err != nil {
				// Create location
				res, err := h.db.Exec("INSERT OR IGNORE INTO locations (route_id, name) VALUES (?, ?)", *routeID, locName)
				if err == nil {
					insertedID, _ := res.LastInsertId()
					if insertedID > 0 {
						lid = int(insertedID)
					} else {
						h.db.QueryRow("SELECT id FROM locations WHERE route_id = ? AND name = ?", *routeID, locName).Scan(&lid)
					}
				}
			}
			if lid > 0 {
				locID = &lid
			}
		}
	}

	// Determine sort_order
	sortOrder := body.SortOrder
	if sortOrder == nil {
		var count int
		h.db.QueryRow("SELECT COUNT(*) FROM timetable_entries WHERE timetable_id = ?", timetableID).Scan(&count)
		sortOrder = &count
	}

	res, err := h.db.Exec(`INSERT INTO timetable_entries (timetable_id, action_id, details, location_id, structure_number, structure, time1, time2, latitude, longitude, api_name, sort_order, coord_source)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		timetableID, actionID, body.Details, locID, body.StructureNumber, nilIfEmpty(body.Structure),
		body.Time1, body.Time2, body.Latitude, body.Longitude,
		body.ApiName, sortOrder, body.CoordSource)
	if err != nil {
		util.Error(w, 500, err.Error())
		return
	}

	insertedID, _ := res.LastInsertId()

	util.JSON(w, 201, map[string]any{
		"id":           insertedID,
		"timetable_id": timetableID,
		"action":       action,
		"action_id":    actionID,
		"details":      body.Details,
		"location":     body.Location,
		"location_id":  locID,
		"structure_number": body.StructureNumber,
		"structure":       body.Structure,
		"time1":        body.Time1,
		"time2":        body.Time2,
		"latitude":     body.Latitude,
		"longitude":    body.Longitude,
		"api_name":     body.ApiName,
		"sort_order":   sortOrder,
		"coord_source": body.CoordSource,
	})
}

func (h *EntryHandler) Update(w http.ResponseWriter, r *http.Request) {
	idStr := chi.URLParam(r, "id")
	id, err := strconv.Atoi(idStr)
	if err != nil {
		util.Error(w, 400, "invalid id")
		return
	}

	// Get existing entry
	var existingTimetableID int
	var existingSortOrder *int
	var existingActionID *int
	var existingAction *string
	err = h.db.QueryRow(`
		SELECT te.timetable_id, te.sort_order, te.action_id, ta.name
		FROM timetable_entries te
		LEFT JOIN timetable_actions ta ON ta.id = te.action_id
		WHERE te.id = ?`, id).Scan(&existingTimetableID, &existingSortOrder, &existingActionID, &existingAction)
	if err == sql.ErrNoRows {
		util.Error(w, 404, "Entry not found")
		return
	}
	if err != nil {
		util.Error(w, 500, err.Error())
		return
	}

	var body map[string]any
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		util.Error(w, 400, "invalid JSON body")
		return
	}

	// First entry validation
	if existingSortOrder != nil && *existingSortOrder == 0 {
		if loc, ok := body["location"].(string); ok {
			if strings.TrimSpace(loc) == "" {
				util.Error(w, 400, "The first entry must have a location name.")
				return
			}
		}
	}

	// Normalize action if provided
	action := ""
	if v, ok := body["action"].(string); ok {
		action = strings.ToUpper(strings.TrimSpace(v))
	}
	effectiveAction := action
	if effectiveAction == "" && existingAction != nil {
		effectiveAction = *existingAction
	}
	if effectiveAction == "UNLOAD PASSENGERS" {
		body["location"] = ""
		body["location_id"] = nil
		body["details"] = ""
	}

	// Resolve action_id
	var actionID *int
	if _, ok := body["action_id"]; ok {
		if v, ok := body["action_id"].(float64); ok {
			aid := int(v)
			actionID = &aid
		}
	} else if action != "" {
		var aid int
		if h.db.QueryRow("SELECT id FROM timetable_actions WHERE name = ?", action).Scan(&aid) == nil {
			actionID = &aid
		}
	} else {
		actionID = existingActionID
	}

	// Resolve location_id if location changed
	if loc, ok := body["location"].(string); ok {
		loc = strings.TrimSpace(loc)
		if loc != "" {
			var routeID *int
			h.db.QueryRow("SELECT route_id FROM timetables WHERE id = ?", existingTimetableID).Scan(&routeID)
			if routeID != nil {
				var lid int
				err := h.db.QueryRow("SELECT id FROM locations WHERE route_id = ? AND name = ?", *routeID, loc).Scan(&lid)
				if err != nil {
					res, err := h.db.Exec("INSERT OR IGNORE INTO locations (route_id, name) VALUES (?, ?)", *routeID, loc)
					if err == nil {
						insertedID, _ := res.LastInsertId()
						if insertedID > 0 {
							lid = int(insertedID)
						} else {
							h.db.QueryRow("SELECT id FROM locations WHERE route_id = ? AND name = ?", *routeID, loc).Scan(&lid)
						}
					}
				}
				if lid > 0 {
					body["location_id"] = lid
				}
			}
		}
	}

	// Build dynamic UPDATE
	updates := []string{}
	params := []any{}

	if actionID != nil {
		updates = append(updates, "action_id = ?")
		params = append(params, *actionID)
	} else if action != "" {
		updates = append(updates, "action_id = ?")
		params = append(params, nil)
	}

	stringFields := map[string]string{
		"details":          "details",
		"structure_number": "structure_number",
		"structure":        "structure",
		"time1":     "time1",
		"time2":     "time2",
		"latitude":  "latitude",
		"longitude": "longitude",
		"api_name":  "api_name",
	}
	for jsonKey, col := range stringFields {
		if v, ok := body[jsonKey]; ok {
			updates = append(updates, col+" = ?")
			params = append(params, v)
		}
	}

	if v, ok := body["location_id"]; ok {
		updates = append(updates, "location_id = ?")
		params = append(params, v)
	}
	if v, ok := body["sort_order"]; ok {
		updates = append(updates, "sort_order = ?")
		params = append(params, v)
	}
	if v, ok := body["coord_source"]; ok {
		updates = append(updates, "coord_source = ?")
		params = append(params, v)
	}

	if len(updates) > 0 {
		params = append(params, id)
		_, err = h.db.Exec(fmt.Sprintf("UPDATE timetable_entries SET %s WHERE id = ?", strings.Join(updates, ", ")), params...)
		if err != nil {
			util.Error(w, 500, err.Error())
			return
		}
	}

	body["id"] = id
	util.JSON(w, 200, body)
}

func (h *EntryHandler) Delete(w http.ResponseWriter, r *http.Request) {
	idStr := chi.URLParam(r, "id")
	id, err := strconv.Atoi(idStr)
	if err != nil {
		util.Error(w, 400, "invalid id")
		return
	}

	_, err = h.db.Exec("DELETE FROM timetable_entries WHERE id = ?", id)
	if err != nil {
		util.Error(w, 500, err.Error())
		return
	}

	util.JSON(w, 200, map[string]bool{"success": true})
}
