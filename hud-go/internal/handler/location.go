package handler

import (
	"database/sql"
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"
	"hud-go/internal/util"
)

type LocationHandler struct {
	db *sql.DB
}

func (h *LocationHandler) GetByRouteID(w http.ResponseWriter, r *http.Request) {
	routeID, _ := strconv.Atoi(chi.URLParam(r, "id"))
	rows, err := h.db.Query("SELECT id, route_id, name FROM locations WHERE route_id = ? ORDER BY name", routeID)
	if err != nil {
		util.Error(w, 500, err.Error())
		return
	}
	defer rows.Close()

	items := make([]map[string]any, 0)
	for rows.Next() {
		var id, rid int
		var name string
		if err := rows.Scan(&id, &rid, &name); err != nil {
			util.Error(w, 500, err.Error())
			return
		}
		items = append(items, map[string]any{"id": id, "route_id": rid, "name": name})
	}
	util.JSON(w, 200, items)
}
