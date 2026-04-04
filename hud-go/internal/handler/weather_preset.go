package handler

import (
	"database/sql"
	"encoding/json"
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"
	"hud-go/internal/util"
)

type WeatherPresetHandler struct {
	db *sql.DB
}

func (h *WeatherPresetHandler) GetAll(w http.ResponseWriter, r *http.Request) {
	rows, err := h.db.Query("SELECT id, name, temperature, cloudiness, precipitation, wetness, ground_snow, piled_snow, fog_density, created_at FROM weather_presets ORDER BY name")
	if err != nil {
		util.Error(w, 500, err.Error())
		return
	}
	defer rows.Close()

	items := make([]map[string]any, 0)
	for rows.Next() {
		var id int
		var name string
		var temp, cloud, precip, wet, gsnow, psnow, fog float64
		var createdAt *string
		if err := rows.Scan(&id, &name, &temp, &cloud, &precip, &wet, &gsnow, &psnow, &fog, &createdAt); err != nil {
			util.Error(w, 500, err.Error())
			return
		}
		items = append(items, map[string]any{
			"id": id, "name": name, "temperature": temp, "cloudiness": cloud,
			"precipitation": precip, "wetness": wet, "ground_snow": gsnow,
			"piled_snow": psnow, "fog_density": fog, "created_at": createdAt,
		})
	}
	util.JSON(w, 200, items)
}

func (h *WeatherPresetHandler) Create(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Name          string  `json:"name"`
		Temperature   float64 `json:"temperature"`
		Cloudiness    float64 `json:"cloudiness"`
		Precipitation float64 `json:"precipitation"`
		Wetness       float64 `json:"wetness"`
		GroundSnow    float64 `json:"ground_snow"`
		PiledSnow     float64 `json:"piled_snow"`
		FogDensity    float64 `json:"fog_density"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		util.Error(w, 400, err.Error())
		return
	}
	res, err := h.db.Exec(
		"INSERT INTO weather_presets (name, temperature, cloudiness, precipitation, wetness, ground_snow, piled_snow, fog_density) VALUES (?,?,?,?,?,?,?,?)",
		body.Name, body.Temperature, body.Cloudiness, body.Precipitation, body.Wetness, body.GroundSnow, body.PiledSnow, body.FogDensity,
	)
	if err != nil {
		util.Error(w, 500, err.Error())
		return
	}
	id, _ := res.LastInsertId()
	util.JSON(w, 201, map[string]any{"id": id, "name": body.Name})
}

func (h *WeatherPresetHandler) GetByID(w http.ResponseWriter, r *http.Request) {
	id, _ := strconv.Atoi(chi.URLParam(r, "id"))
	var name string
	var temp, cloud, precip, wet, gsnow, psnow, fog float64
	var createdAt *string
	err := h.db.QueryRow(
		"SELECT name, temperature, cloudiness, precipitation, wetness, ground_snow, piled_snow, fog_density, created_at FROM weather_presets WHERE id = ?", id,
	).Scan(&name, &temp, &cloud, &precip, &wet, &gsnow, &psnow, &fog, &createdAt)
	if err == sql.ErrNoRows {
		util.Error(w, 404, "not found")
		return
	}
	if err != nil {
		util.Error(w, 500, err.Error())
		return
	}
	util.JSON(w, 200, map[string]any{
		"id": id, "name": name, "temperature": temp, "cloudiness": cloud,
		"precipitation": precip, "wetness": wet, "ground_snow": gsnow,
		"piled_snow": psnow, "fog_density": fog, "created_at": createdAt,
	})
}

func (h *WeatherPresetHandler) Update(w http.ResponseWriter, r *http.Request) {
	id, _ := strconv.Atoi(chi.URLParam(r, "id"))
	var body struct {
		Name          string  `json:"name"`
		Temperature   float64 `json:"temperature"`
		Cloudiness    float64 `json:"cloudiness"`
		Precipitation float64 `json:"precipitation"`
		Wetness       float64 `json:"wetness"`
		GroundSnow    float64 `json:"ground_snow"`
		PiledSnow     float64 `json:"piled_snow"`
		FogDensity    float64 `json:"fog_density"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		util.Error(w, 400, err.Error())
		return
	}
	_, err := h.db.Exec(
		"UPDATE weather_presets SET name=?, temperature=?, cloudiness=?, precipitation=?, wetness=?, ground_snow=?, piled_snow=?, fog_density=? WHERE id=?",
		body.Name, body.Temperature, body.Cloudiness, body.Precipitation, body.Wetness, body.GroundSnow, body.PiledSnow, body.FogDensity, id,
	)
	if err != nil {
		util.Error(w, 500, err.Error())
		return
	}
	util.JSON(w, 200, map[string]string{"status": "updated"})
}

func (h *WeatherPresetHandler) Delete(w http.ResponseWriter, r *http.Request) {
	id, _ := strconv.Atoi(chi.URLParam(r, "id"))
	_, err := h.db.Exec("DELETE FROM weather_presets WHERE id = ?", id)
	if err != nil {
		util.Error(w, 500, err.Error())
		return
	}
	util.JSON(w, 200, map[string]string{"status": "deleted"})
}
