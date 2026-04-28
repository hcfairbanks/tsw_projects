package handler

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"

	"hud-go/internal/models"
	"hud-go/internal/util"
)

type TrainHandler struct {
	db *sql.DB
}

func (h *TrainHandler) GetAll(w http.ResponseWriter, r *http.Request) {
	// Include class_id + class_name so search/listings can show the
	// user-facing class label (e.g. "Isle Of Wight Class 483") rather than
	// just the formation name (e.g. "Class483_006").
	//
	// Supports `?search=` (matches name OR class_name, case-insensitive
	// substring) and `?limit=N` for typeahead callers that don't want the
	// full table dumped on every keystroke.
	q := r.URL.Query()
	search := strings.TrimSpace(q.Get("search"))
	limitStr := q.Get("limit")

	sb := "SELECT id, name, class_id, class_name FROM trains"
	var args []any
	if search != "" {
		sb += " WHERE name LIKE ? OR class_name LIKE ?"
		pat := "%" + search + "%"
		args = append(args, pat, pat)
	}
	sb += " ORDER BY COALESCE(class_name, name), name"
	if limit, _ := strconv.Atoi(limitStr); limit > 0 {
		sb += fmt.Sprintf(" LIMIT %d", limit)
	}

	rows, err := h.db.Query(sb, args...)
	if err != nil {
		util.Error(w, 500, err.Error())
		return
	}
	defer rows.Close()

	var items []models.Train
	for rows.Next() {
		var t models.Train
		if err := rows.Scan(&t.ID, &t.Name, &t.ClassID, &t.ClassName); err != nil {
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
	err = h.db.QueryRow(
		"SELECT id, name, class_id, class_name, livery_id, length_m, car_count FROM trains WHERE id = ?", id,
	).Scan(&t.ID, &t.Name, &t.ClassID, &t.ClassName, &t.LiveryID, &t.LengthM, &t.CarCount)
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

// GetClassByID returns a train_classes row plus the list of formations
// (trains) that belong to it.
func (h *TrainHandler) GetClassByID(w http.ResponseWriter, r *http.Request) {
	idStr := chi.URLParam(r, "id")
	id, err := strconv.Atoi(idStr)
	if err != nil {
		util.Error(w, 400, "invalid id")
		return
	}
	type trainClass struct {
		ID              int      `json:"id"`
		Name            string   `json:"name"`
		LiveryID        *string  `json:"livery_id,omitempty"`
		TypicalLengthM  *float64 `json:"typical_length_m,omitempty"`
		TypicalCarCount *int     `json:"typical_car_count,omitempty"`
	}
	var c trainClass
	err = h.db.QueryRow(
		"SELECT id, name, livery_id, typical_length_m, typical_car_count FROM train_classes WHERE id = ?", id,
	).Scan(&c.ID, &c.Name, &c.LiveryID, &c.TypicalLengthM, &c.TypicalCarCount)
	if err == sql.ErrNoRows {
		util.Error(w, 404, "Train class not found")
		return
	}
	if err != nil {
		util.Error(w, 500, err.Error())
		return
	}

	// Formations under this class.
	rows, err := h.db.Query(
		"SELECT id, name, length_m, car_count FROM trains WHERE class_id = ? ORDER BY name", id)
	if err != nil {
		util.Error(w, 500, err.Error())
		return
	}
	defer rows.Close()
	type formation struct {
		ID       int      `json:"id"`
		Name     string   `json:"name"`
		LengthM  *float64 `json:"length_m,omitempty"`
		CarCount *int     `json:"car_count,omitempty"`
	}
	formations := []formation{}
	for rows.Next() {
		var f formation
		if err := rows.Scan(&f.ID, &f.Name, &f.LengthM, &f.CarCount); err != nil {
			util.Error(w, 500, err.Error())
			return
		}
		formations = append(formations, f)
	}
	util.JSON(w, 200, map[string]any{
		"class":      c,
		"formations": formations,
	})
}

// GetVehicles returns the per-car list for a train (from the train_vehicles
// table populated during route import). Each row is one car in the formation
// with its position, GUID (matchable to the live API), class, length, and
// flags. Empty list when the train has no consist data.
func (h *TrainHandler) GetVehicles(w http.ResponseWriter, r *http.Request) {
	idStr := chi.URLParam(r, "id")
	id, err := strconv.Atoi(idStr)
	if err != nil {
		util.Error(w, 400, "invalid id")
		return
	}
	rows, err := h.db.Query(`
		SELECT id, position, vehicle_id, class_name, friendly_name, livery_id,
		       vehicle_category, length_m, is_lead, is_flipped
		FROM train_vehicles
		WHERE train_id = ?
		ORDER BY position`, id)
	if err != nil {
		util.Error(w, 500, err.Error())
		return
	}
	defer rows.Close()
	type vehicle struct {
		ID              int      `json:"id"`
		Position        int      `json:"position"`
		VehicleID       string   `json:"vehicle_id"`
		ClassName       *string  `json:"class_name,omitempty"`
		FriendlyName    *string  `json:"friendly_name,omitempty"`
		LiveryID        *string  `json:"livery_id,omitempty"`
		VehicleCategory *string  `json:"vehicle_category,omitempty"`
		LengthM         *float64 `json:"length_m,omitempty"`
		IsLead          int      `json:"is_lead"`
		IsFlipped       int      `json:"is_flipped"`
	}
	out := []vehicle{}
	for rows.Next() {
		var v vehicle
		if err := rows.Scan(&v.ID, &v.Position, &v.VehicleID, &v.ClassName, &v.FriendlyName,
			&v.LiveryID, &v.VehicleCategory, &v.LengthM, &v.IsLead, &v.IsFlipped); err != nil {
			util.Error(w, 500, err.Error())
			return
		}
		out = append(out, v)
	}
	util.JSON(w, 200, out)
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
