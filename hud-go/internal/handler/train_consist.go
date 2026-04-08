package handler

import (
	"database/sql"
	"encoding/json"
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"
)

// TrainConsistHandler handles train consist endpoints.
type TrainConsistHandler struct {
	db *sql.DB
}

type trainConsistRequest struct {
	TimetableID int      `json:"timetable_id"`
	TrainID     int      `json:"train_id"`
	Weight      *float64 `json:"weight"`
	CarCount    *int     `json:"car_count"`
	TrainLength *float64 `json:"train_length"`
	TrainNumber *int     `json:"train_number"`
	Latitude    *float64 `json:"latitude"`
	Longitude   *float64 `json:"longitude"`
}

// Create inserts a new train consist record.
func (h *TrainConsistHandler) Create(w http.ResponseWriter, r *http.Request) {
	var req trainConsistRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, `{"error":"invalid JSON"}`, http.StatusBadRequest)
		return
	}
	if req.TimetableID == 0 || req.TrainID == 0 {
		http.Error(w, `{"error":"timetable_id and train_id are required"}`, http.StatusBadRequest)
		return
	}

	res, err := h.db.Exec(
		`INSERT OR IGNORE INTO train_consists (timetable_id, train_id, weight, car_count, train_length, train_number, latitude, longitude)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		req.TimetableID, req.TrainID, req.Weight, req.CarCount, req.TrainLength, req.TrainNumber, req.Latitude, req.Longitude,
	)
	if err != nil {
		http.Error(w, `{"error":"`+err.Error()+`"}`, http.StatusInternalServerError)
		return
	}

	id, _ := res.LastInsertId()
	rows, _ := res.RowsAffected()
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	json.NewEncoder(w).Encode(map[string]any{
		"id":           id,
		"timetable_id": req.TimetableID,
		"train_id":     req.TrainID,
		"inserted":     rows > 0,
	})
}

// BulkCreate inserts multiple train consist records at once.
func (h *TrainConsistHandler) BulkCreate(w http.ResponseWriter, r *http.Request) {
	var reqs []trainConsistRequest
	if err := json.NewDecoder(r.Body).Decode(&reqs); err != nil {
		http.Error(w, `{"error":"invalid JSON, expected array"}`, http.StatusBadRequest)
		return
	}

	inserted := 0
	for _, req := range reqs {
		if req.TimetableID == 0 || req.TrainID == 0 {
			continue
		}
		res, err := h.db.Exec(
			`INSERT OR IGNORE INTO train_consists (timetable_id, train_id, weight, car_count, train_length, train_number)
			 VALUES (?, ?, ?, ?, ?, ?)`,
			req.TimetableID, req.TrainID, req.Weight, req.CarCount, req.TrainLength, req.TrainNumber, req.Latitude, req.Longitude,
		)
		if err == nil {
			rows, _ := res.RowsAffected()
			if rows > 0 {
				inserted++
			}
		}
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	json.NewEncoder(w).Encode(map[string]any{"inserted": inserted})
}

// GetByTimetableID returns all consist records for a timetable.
func (h *TrainConsistHandler) GetByTimetableID(w http.ResponseWriter, r *http.Request) {
	idStr := chi.URLParam(r, "timetableId")
	timetableID, err := strconv.Atoi(idStr)
	if err != nil {
		http.Error(w, `{"error":"invalid timetable id"}`, http.StatusBadRequest)
		return
	}

	rows, err := h.db.Query(
		`SELECT tc.id, tc.timetable_id, tc.train_id, tc.weight, tc.car_count,
		        tc.train_length, tc.train_number, tc.latitude, tc.longitude, tc.created_at, t.name as train_name
		 FROM train_consists tc
		 JOIN trains t ON t.id = tc.train_id
		 WHERE tc.timetable_id = ?
		 ORDER BY tc.train_number`, timetableID)
	if err != nil {
		http.Error(w, `{"error":"`+err.Error()+`"}`, http.StatusInternalServerError)
		return
	}
	defer rows.Close()

	type consistRow struct {
		ID          int      `json:"id"`
		TimetableID int      `json:"timetable_id"`
		TrainID     int      `json:"train_id"`
		Weight      *float64 `json:"weight"`
		CarCount    *int     `json:"car_count"`
		TrainLength *float64 `json:"train_length"`
		TrainNumber *int     `json:"train_number"`
		Latitude    *float64 `json:"latitude"`
		Longitude   *float64 `json:"longitude"`
		CreatedAt   *string  `json:"created_at"`
		TrainName   string   `json:"train_name"`
	}

	var results []consistRow
	for rows.Next() {
		var row consistRow
		if err := rows.Scan(&row.ID, &row.TimetableID, &row.TrainID, &row.Weight,
			&row.CarCount, &row.TrainLength, &row.TrainNumber, &row.Latitude, &row.Longitude, &row.CreatedAt, &row.TrainName); err != nil {
			continue
		}
		results = append(results, row)
	}
	if results == nil {
		results = []consistRow{}
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(results)
}

// GetAll returns all consist records.
func (h *TrainConsistHandler) GetAll(w http.ResponseWriter, r *http.Request) {
	rows, err := h.db.Query(
		`SELECT tc.id, tc.timetable_id, tc.train_id, tc.weight, tc.car_count,
		        tc.train_length, tc.train_number, tc.latitude, tc.longitude, tc.created_at, t.name as train_name
		 FROM train_consists tc
		 JOIN trains t ON t.id = tc.train_id
		 ORDER BY tc.timetable_id, tc.train_number`)
	if err != nil {
		http.Error(w, `{"error":"`+err.Error()+`"}`, http.StatusInternalServerError)
		return
	}
	defer rows.Close()

	type consistRow struct {
		ID          int      `json:"id"`
		TimetableID int      `json:"timetable_id"`
		TrainID     int      `json:"train_id"`
		Weight      *float64 `json:"weight"`
		CarCount    *int     `json:"car_count"`
		TrainLength *float64 `json:"train_length"`
		TrainNumber *int     `json:"train_number"`
		Latitude    *float64 `json:"latitude"`
		Longitude   *float64 `json:"longitude"`
		CreatedAt   *string  `json:"created_at"`
		TrainName   string   `json:"train_name"`
	}

	var results []consistRow
	for rows.Next() {
		var row consistRow
		if err := rows.Scan(&row.ID, &row.TimetableID, &row.TrainID, &row.Weight,
			&row.CarCount, &row.TrainLength, &row.TrainNumber, &row.Latitude, &row.Longitude, &row.CreatedAt, &row.TrainName); err != nil {
			continue
		}
		results = append(results, row)
	}
	if results == nil {
		results = []consistRow{}
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(results)
}
