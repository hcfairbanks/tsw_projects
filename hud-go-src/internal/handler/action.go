package handler

import (
	"database/sql"
	"net/http"

	"hud-go/internal/util"
)

type ActionHandler struct {
	db *sql.DB
}

func (h *ActionHandler) GetAll(w http.ResponseWriter, r *http.Request) {
	rows, err := h.db.Query("SELECT id, name FROM timetable_actions ORDER BY id")
	if err != nil {
		util.Error(w, 500, err.Error())
		return
	}
	defer rows.Close()

	items := make([]map[string]any, 0)
	for rows.Next() {
		var id int
		var name string
		if err := rows.Scan(&id, &name); err != nil {
			util.Error(w, 500, err.Error())
			return
		}
		items = append(items, map[string]any{"id": id, "name": name})
	}
	util.JSON(w, 200, items)
}
