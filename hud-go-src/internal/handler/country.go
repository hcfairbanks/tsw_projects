package handler

import (
	"database/sql"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"

	"hud-go/internal/models"
	"hud-go/internal/util"
)

type CountryHandler struct {
	db *sql.DB
}

func (h *CountryHandler) GetAll(w http.ResponseWriter, r *http.Request) {
	rows, err := h.db.Query("SELECT id, name, code FROM countries ORDER BY name")
	if err != nil {
		util.Error(w, 500, err.Error())
		return
	}
	defer rows.Close()

	var items []models.Country
	for rows.Next() {
		var c models.Country
		if err := rows.Scan(&c.ID, &c.Name, &c.Code); err != nil {
			util.Error(w, 500, err.Error())
			return
		}
		items = append(items, c)
	}
	if items == nil {
		items = []models.Country{}
	}
	util.JSON(w, 200, items)
}

func (h *CountryHandler) Create(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Name string  `json:"name"`
		Code *string `json:"code"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		util.Error(w, 400, "invalid JSON body")
		return
	}

	if body.Name == "" {
		util.Error(w, 400, "Country name is required")
		return
	}

	if body.Code == nil || *body.Code == "" {
		util.Error(w, 400, "Country code is required (e.g., GB, US, DE)")
		return
	}

	// Check if country already exists
	var existing models.Country
	err := h.db.QueryRow("SELECT id, name, code FROM countries WHERE name = ?", body.Name).Scan(&existing.ID, &existing.Name, &existing.Code)
	if err == nil {
		util.JSON(w, 409, map[string]any{"error": "Country already exists", "id": existing.ID})
		return
	}

	result, err := h.db.Exec("INSERT INTO countries (name, code) VALUES (?, ?)", body.Name, body.Code)
	if err != nil {
		util.Error(w, 500, err.Error())
		return
	}

	id, _ := result.LastInsertId()
	util.JSON(w, 201, map[string]any{"id": id, "name": body.Name, "code": body.Code})
}

func (h *CountryHandler) GetByID(w http.ResponseWriter, r *http.Request) {
	idStr := chi.URLParam(r, "id")
	id, err := strconv.Atoi(idStr)
	if err != nil {
		util.Error(w, 400, "invalid id")
		return
	}

	var c models.Country
	err = h.db.QueryRow("SELECT id, name, code FROM countries WHERE id = ?", id).Scan(&c.ID, &c.Name, &c.Code)
	if err == sql.ErrNoRows {
		util.Error(w, 404, "Country not found")
		return
	}
	if err != nil {
		util.Error(w, 500, err.Error())
		return
	}

	util.JSON(w, 200, c)
}

func (h *CountryHandler) Update(w http.ResponseWriter, r *http.Request) {
	idStr := chi.URLParam(r, "id")
	id, err := strconv.Atoi(idStr)
	if err != nil {
		util.Error(w, 400, "invalid id")
		return
	}

	var body struct {
		Name string  `json:"name"`
		Code *string `json:"code"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		util.Error(w, 400, "invalid JSON body")
		return
	}

	if body.Name == "" {
		util.Error(w, 400, "Country name is required")
		return
	}

	if body.Code == nil || *body.Code == "" {
		util.Error(w, 400, "Country code is required (e.g., GB, US, DE)")
		return
	}

	// Check if another country with this name already exists (excluding current)
	var existingID int
	err = h.db.QueryRow("SELECT id FROM countries WHERE name = ?", body.Name).Scan(&existingID)
	if err == nil && existingID != id {
		util.Error(w, 409, "A country with this name already exists")
		return
	}

	_, err = h.db.Exec("UPDATE countries SET name = ?, code = ? WHERE id = ?", body.Name, body.Code, id)
	if err != nil {
		util.Error(w, 500, err.Error())
		return
	}

	util.JSON(w, 200, map[string]any{"id": id, "name": body.Name, "code": body.Code})
}

func (h *CountryHandler) Delete(w http.ResponseWriter, r *http.Request) {
	idStr := chi.URLParam(r, "id")
	id, err := strconv.Atoi(idStr)
	if err != nil {
		util.Error(w, 400, "invalid id")
		return
	}

	_, err = h.db.Exec("DELETE FROM countries WHERE id = ?", id)
	if err != nil {
		if strings.Contains(err.Error(), "FOREIGN KEY constraint") {
			util.Error(w, 409, "Cannot delete country: it is referenced by one or more routes")
			return
		}
		util.Error(w, 500, err.Error())
		return
	}

	util.JSON(w, 200, map[string]bool{"success": true})
}

func (h *CountryHandler) GetRoutes(w http.ResponseWriter, r *http.Request) {
	idStr := chi.URLParam(r, "id")
	id, err := strconv.Atoi(idStr)
	if err != nil {
		util.Error(w, 400, "invalid id")
		return
	}

	rows, err := h.db.Query("SELECT id, name, country_id, tsw_version FROM routes WHERE country_id = ? ORDER BY name", id)
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
