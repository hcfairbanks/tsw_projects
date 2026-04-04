package handler

import (
	"database/sql"
	"encoding/json"
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"

	"hud-go/internal/models"
	"hud-go/internal/util"
)

type TrainHandler struct {
	db *sql.DB
}

func (h *TrainHandler) GetAll(w http.ResponseWriter, r *http.Request) {
	rows, err := h.db.Query("SELECT id, name FROM trains ORDER BY name")
	if err != nil {
		util.Error(w, 500, err.Error())
		return
	}
	defer rows.Close()

	var items []models.Train
	for rows.Next() {
		var t models.Train
		if err := rows.Scan(&t.ID, &t.Name); err != nil {
			util.Error(w, 500, err.Error())
			return
		}
		items = append(items, t)
	}
	if items == nil {
		items = []models.Train{}
	}
	util.JSON(w, 200, items)
}

func (h *TrainHandler) GetByName(w http.ResponseWriter, r *http.Request) {
	name := r.URL.Query().Get("name")
	if name == "" {
		util.Error(w, 400, "Missing required query parameter: name")
		return
	}

	var t models.Train
	err := h.db.QueryRow("SELECT id, name FROM trains WHERE name = ?", name).Scan(&t.ID, &t.Name)
	if err == sql.ErrNoRows {
		util.Error(w, 404, "No train found with name \""+name+"\"")
		return
	}
	if err != nil {
		util.Error(w, 500, err.Error())
		return
	}

	util.JSON(w, 200, t)
}

func (h *TrainHandler) Create(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Name string `json:"name"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		util.Error(w, 400, "invalid JSON body")
		return
	}

	if body.Name == "" {
		util.Error(w, 400, "Train name is required")
		return
	}

	// Check if train name already exists
	var existingID int
	err := h.db.QueryRow("SELECT id FROM trains WHERE name = ?", body.Name).Scan(&existingID)
	if err == nil {
		util.Error(w, 409, "A train with this name already exists")
		return
	}

	result, err := h.db.Exec("INSERT INTO trains (name) VALUES (?)", body.Name)
	if err != nil {
		util.Error(w, 500, err.Error())
		return
	}

	id, _ := result.LastInsertId()
	util.JSON(w, 201, map[string]any{"id": id, "name": body.Name})
}

func (h *TrainHandler) GetByID(w http.ResponseWriter, r *http.Request) {
	idStr := chi.URLParam(r, "id")
	id, err := strconv.Atoi(idStr)
	if err != nil {
		util.Error(w, 400, "invalid id")
		return
	}

	var t models.Train
	err = h.db.QueryRow("SELECT id, name FROM trains WHERE id = ?", id).Scan(&t.ID, &t.Name)
	if err == sql.ErrNoRows {
		util.Error(w, 404, "Train not found")
		return
	}
	if err != nil {
		util.Error(w, 500, err.Error())
		return
	}

	util.JSON(w, 200, t)
}

func (h *TrainHandler) Update(w http.ResponseWriter, r *http.Request) {
	idStr := chi.URLParam(r, "id")
	id, err := strconv.Atoi(idStr)
	if err != nil {
		util.Error(w, 400, "invalid id")
		return
	}

	// Fetch existing train
	var existing models.Train
	err = h.db.QueryRow("SELECT id, name FROM trains WHERE id = ?", id).Scan(&existing.ID, &existing.Name)
	if err == sql.ErrNoRows {
		util.Error(w, 404, "Train not found")
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

	// Check if another train with this name already exists (excluding current)
	if body.Name != "" {
		var dupID int
		err = h.db.QueryRow("SELECT id FROM trains WHERE name = ?", body.Name).Scan(&dupID)
		if err == nil && dupID != id {
			util.Error(w, 409, "A train with this name already exists")
			return
		}
	}

	name := body.Name
	if name == "" {
		name = existing.Name
	}

	_, err = h.db.Exec("UPDATE trains SET name = ? WHERE id = ?", name, id)
	if err != nil {
		util.Error(w, 500, err.Error())
		return
	}

	util.JSON(w, 200, map[string]any{"id": id, "name": name})
}

func (h *TrainHandler) Delete(w http.ResponseWriter, r *http.Request) {
	idStr := chi.URLParam(r, "id")
	id, err := strconv.Atoi(idStr)
	if err != nil {
		util.Error(w, 400, "invalid id")
		return
	}

	_, err = h.db.Exec("DELETE FROM trains WHERE id = ?", id)
	if err != nil {
		util.Error(w, 500, err.Error())
		return
	}

	util.JSON(w, 200, map[string]bool{"success": true})
}

func (h *TrainHandler) GetRoutes(w http.ResponseWriter, r *http.Request) {
	idStr := chi.URLParam(r, "id")
	id, err := strconv.Atoi(idStr)
	if err != nil {
		util.Error(w, 400, "invalid id")
		return
	}

	rows, err := h.db.Query(`
		SELECT DISTINCT r.id, r.name, r.country_id, r.tsw_version
		FROM routes r
		INNER JOIN route_trains rt ON r.id = rt.route_id
		WHERE rt.train_id = ?
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
