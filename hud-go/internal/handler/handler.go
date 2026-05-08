package handler

import (
	"database/sql"
	"net/http"
	"time"

	"hud-go/internal/tsw"
)


// Handlers holds all sub-handlers for the application.
type Handlers struct {
	Country         *CountryHandler
	Route           *RouteHandler
	Train           *TrainHandler
	Timetable       *TimetableHandler
	Entry           *EntryHandler
	Location        *LocationHandler
	StationMapping  *StationMappingHandler
	WeatherPreset   *WeatherPresetHandler
	Weather         *WeatherHandler
	LiveWeather     *LiveWeatherHandler
	HistWeather     *HistoricalWeatherHandler
	Config          *ConfigHandler
	Stream          *StreamHandler
	HUD             *HUDHandler
	Recording       *RecordingHandler
	Processing      *ProcessingHandler
	MapData         *MapDataHandler
	Subscription    *SubscriptionHandler
	RouteProcessing *RouteProcessingHandler
	Action          *ActionHandler
	TrainConsist    *TrainConsistHandler
	Extractor       *ExtractorHandler
	ReloadDB        func(http.ResponseWriter, *http.Request)
}

// New creates a new Handlers instance with all sub-handlers initialized.
func New(db *sql.DB, tswClient *tsw.Client) *Handlers {
	rec := &RecordingHandler{db: db}

	// Build stream data callback that also feeds coordinates into the recording handler
	getData := func() map[string]any {
		data := tswClient.GetLastData()
		if data == nil || len(data) == 0 {
			data = map[string]any{}
		}

		// Feed GPS coordinates into active recording
		if rec.IsRecordingActive() {
			if pos, ok := data["playerPosition"].(map[string]any); ok {
				lat, _ := pos["latitude"].(float64)
				lng, _ := pos["longitude"].(float64)
				if lat != 0 || lng != 0 {
					speed, _ := data["speed"].(int)
					gameTime, _ := data["localTime"].(string)
					var tileX, tileY *int
					if tile, ok := data["currentTile"].(map[string]any); ok {
						if x, ok := tile["x"].(int); ok {
							xi := x
							tileX = &xi
						}
						if y, ok := tile["y"].(int); ok {
							yi := y
							tileY = &yi
						}
					}
					rec.ProcessCoordinate(lat, lng, float64(speed), gameTime, tileX, tileY)
				}
			}
			// Feed station markers
			if stations, ok := data["_stations"].([]any); ok {
				for _, s := range stations {
					if st, ok := s.(map[string]any); ok {
						distCM, _ := st["distanceToStationCM"].(float64)
						rec.ProcessMarker(st, distCM)
					}
				}
			}
			// Feed markers (separate from stations)
			if markers, ok := data["_markers"].([]any); ok {
				for _, m := range markers {
					if mk, ok := m.(map[string]any); ok {
						distCM, _ := mk["distanceToStationCM"].(float64)
						rec.ProcessMarker(mk, distCM)
					}
				}
			}
		}

		// Clean up internal fields
		delete(data, "_stations")
		delete(data, "_markers")

		// Include recording state
		if recState := rec.GetStreamState(); recState != nil {
			data["recording"] = recState
		}

		return data
	}

	// Build the TimetableHandler first so Extractor can hold the same
	// instance (shares the importer's logic without a 400-line duplication).
	tt := &TimetableHandler{db: db}
	return &Handlers{
		Country:         &CountryHandler{db: db},
		Route:           &RouteHandler{db: db},
		Train:           &TrainHandler{db: db},
		Timetable:       tt,
		Entry:           &EntryHandler{db: db},
		Location:        &LocationHandler{db: db},
		StationMapping:  &StationMappingHandler{db: db},
		WeatherPreset:   &WeatherPresetHandler{db: db},
		Weather:         &WeatherHandler{client: tswClient},
		LiveWeather:     &LiveWeatherHandler{client: tswClient, httpClient: &http.Client{Timeout: 10 * time.Second}},
		HistWeather:     &HistoricalWeatherHandler{client: tswClient, httpClient: &http.Client{Timeout: 10 * time.Second}},
		Config:          &ConfigHandler{db: db},
		Stream:          &StreamHandler{db: db, GetData: getData},
		HUD:             &HUDHandler{db: db},
		Recording:       rec,
		Processing:      &ProcessingHandler{db: db},
		MapData:         &MapDataHandler{db: db},
		Subscription:    &SubscriptionHandler{client: tswClient},
		RouteProcessing: &RouteProcessingHandler{db: db},
		Action:          &ActionHandler{db: db},
		TrainConsist:    &TrainConsistHandler{db: db},
		Extractor: &ExtractorHandler{
			db:    db,
			tt:    tt,
			state: extractorState{status: extStatusIdle},
		},
		ReloadDB: func(w http.ResponseWriter, r *http.Request) {
			http.Error(w, `{"error":"not implemented"}`, http.StatusNotImplemented)
		},
	}
}
