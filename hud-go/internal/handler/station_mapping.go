package handler

import (
	"database/sql"
	"encoding/json"
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"
	"hud-go/internal/util"
)

type StationMappingHandler struct {
	db *sql.DB
}

func (h *StationMappingHandler) GetAll(w http.ResponseWriter, r *http.Request) {
	query := "SELECT id, route_id, display_name, api_name, created_at FROM station_name_mappings"
	var args []any
	if rid := r.URL.Query().Get("routeId"); rid != "" {
		query += " WHERE route_id = ?"
		id, _ := strconv.Atoi(rid)
		args = append(args, id)
	}
	query += " ORDER BY display_name"

	rows, err := h.db.Query(query, args...)
	if err != nil {
		util.Error(w, 500, err.Error())
		return
	}
	defer rows.Close()

	items := make([]map[string]any, 0)
	for rows.Next() {
		var id int
		var routeID *int
		var displayName, apiName string
		var createdAt *string
		if err := rows.Scan(&id, &routeID, &displayName, &apiName, &createdAt); err != nil {
			util.Error(w, 500, err.Error())
			return
		}
		items = append(items, map[string]any{
			"id": id, "route_id": routeID, "display_name": displayName,
			"api_name": apiName, "created_at": createdAt,
		})
	}
	util.JSON(w, 200, items)
}

func (h *StationMappingHandler) getLookup(w http.ResponseWriter, routeID *int) {
	query := `SELECT display_name, api_name FROM station_name_mappings WHERE route_id IS NULL`
	var args []any
	if routeID != nil {
		query = `SELECT display_name, api_name FROM station_name_mappings WHERE route_id = ? OR route_id IS NULL`
		args = append(args, *routeID)
	}

	rows, err := h.db.Query(query, args...)
	if err != nil {
		util.Error(w, 500, err.Error())
		return
	}
	defer rows.Close()

	lookup := make(map[string]string)
	for rows.Next() {
		var display, api string
		if err := rows.Scan(&display, &api); err != nil {
			util.Error(w, 500, err.Error())
			return
		}
		lookup[display] = api
	}
	util.JSON(w, 200, lookup)
}

func (h *StationMappingHandler) GetLookup(w http.ResponseWriter, r *http.Request) {
	h.getLookup(w, nil)
}

func (h *StationMappingHandler) GetLookupByRoute(w http.ResponseWriter, r *http.Request) {
	id, _ := strconv.Atoi(chi.URLParam(r, "routeId"))
	h.getLookup(w, &id)
}

func (h *StationMappingHandler) GetByRouteID(w http.ResponseWriter, r *http.Request) {
	routeID, _ := strconv.Atoi(chi.URLParam(r, "routeId"))
	rows, err := h.db.Query(
		"SELECT id, route_id, display_name, api_name, created_at FROM station_name_mappings WHERE route_id = ? ORDER BY display_name", routeID,
	)
	if err != nil {
		util.Error(w, 500, err.Error())
		return
	}
	defer rows.Close()

	items := make([]map[string]any, 0)
	for rows.Next() {
		var id int
		var rid *int
		var displayName, apiName string
		var createdAt *string
		if err := rows.Scan(&id, &rid, &displayName, &apiName, &createdAt); err != nil {
			util.Error(w, 500, err.Error())
			return
		}
		items = append(items, map[string]any{
			"id": id, "route_id": rid, "display_name": displayName,
			"api_name": apiName, "created_at": createdAt,
		})
	}
	util.JSON(w, 200, items)
}

func (h *StationMappingHandler) Create(w http.ResponseWriter, r *http.Request) {
	var body struct {
		RouteID     *int   `json:"route_id"`
		DisplayName string `json:"display_name"`
		ApiName     string `json:"api_name"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		util.Error(w, 400, err.Error())
		return
	}
	res, err := h.db.Exec(
		"INSERT INTO station_name_mappings (route_id, display_name, api_name) VALUES (?, ?, ?)",
		body.RouteID, body.DisplayName, body.ApiName,
	)
	if err != nil {
		util.Error(w, 500, err.Error())
		return
	}
	id, _ := res.LastInsertId()
	util.JSON(w, 201, map[string]any{"id": id, "display_name": body.DisplayName, "api_name": body.ApiName})
}

func (h *StationMappingHandler) BulkImport(w http.ResponseWriter, r *http.Request) {
	var items []struct {
		RouteID     *int   `json:"route_id"`
		DisplayName string `json:"display_name"`
		ApiName     string `json:"api_name"`
	}
	if err := json.NewDecoder(r.Body).Decode(&items); err != nil {
		util.Error(w, 400, err.Error())
		return
	}
	count := 0
	for _, m := range items {
		_, err := h.db.Exec(
			"INSERT OR IGNORE INTO station_name_mappings (route_id, display_name, api_name) VALUES (?, ?, ?)",
			m.RouteID, m.DisplayName, m.ApiName,
		)
		if err == nil {
			count++
		}
	}
	util.JSON(w, 200, map[string]any{"imported": count})
}

func (h *StationMappingHandler) ImportFromObject(w http.ResponseWriter, r *http.Request) {
	var body struct {
		RouteID  *int              `json:"route_id"`
		Mappings map[string]string `json:"mappings"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		util.Error(w, 400, err.Error())
		return
	}
	count := 0
	for display, api := range body.Mappings {
		_, err := h.db.Exec(
			"INSERT OR IGNORE INTO station_name_mappings (route_id, display_name, api_name) VALUES (?, ?, ?)",
			body.RouteID, display, api,
		)
		if err == nil {
			count++
		}
	}
	util.JSON(w, 200, map[string]any{"imported": count})
}

func (h *StationMappingHandler) GetByID(w http.ResponseWriter, r *http.Request) {
	id, _ := strconv.Atoi(chi.URLParam(r, "id"))
	var routeID *int
	var displayName, apiName string
	var createdAt *string
	err := h.db.QueryRow(
		"SELECT route_id, display_name, api_name, created_at FROM station_name_mappings WHERE id = ?", id,
	).Scan(&routeID, &displayName, &apiName, &createdAt)
	if err == sql.ErrNoRows {
		util.Error(w, 404, "not found")
		return
	}
	if err != nil {
		util.Error(w, 500, err.Error())
		return
	}
	util.JSON(w, 200, map[string]any{
		"id": id, "route_id": routeID, "display_name": displayName,
		"api_name": apiName, "created_at": createdAt,
	})
}

func (h *StationMappingHandler) Update(w http.ResponseWriter, r *http.Request) {
	id, _ := strconv.Atoi(chi.URLParam(r, "id"))
	var body struct {
		RouteID     *int   `json:"route_id"`
		DisplayName string `json:"display_name"`
		ApiName     string `json:"api_name"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		util.Error(w, 400, err.Error())
		return
	}
	_, err := h.db.Exec(
		"UPDATE station_name_mappings SET route_id=?, display_name=?, api_name=? WHERE id=?",
		body.RouteID, body.DisplayName, body.ApiName, id,
	)
	if err != nil {
		util.Error(w, 500, err.Error())
		return
	}
	util.JSON(w, 200, map[string]string{"status": "updated"})
}

func (h *StationMappingHandler) Delete(w http.ResponseWriter, r *http.Request) {
	id, _ := strconv.Atoi(chi.URLParam(r, "id"))
	_, err := h.db.Exec("DELETE FROM station_name_mappings WHERE id = ?", id)
	if err != nil {
		util.Error(w, 500, err.Error())
		return
	}
	util.JSON(w, 200, map[string]string{"status": "deleted"})
}
