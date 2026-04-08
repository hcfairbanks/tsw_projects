package router

import (
	"database/sql"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"

	"hud-go/internal/handler"
	"hud-go/internal/tsw"
)

// New creates and configures the chi router with all routes.
func New(db *sql.DB, tswClient *tsw.Client) *chi.Mux {
	h := handler.New(db, tswClient)
	r := chi.NewRouter()

	r.Use(middleware.Recoverer)

	// --- Static / page routes ---
	registerStaticRoutes(r)

	// --- API routes ---

	// Countries
	r.Get("/api/countries", h.Country.GetAll)
	r.Post("/api/countries", h.Country.Create)
	r.Get("/api/countries/{id}", h.Country.GetByID)
	r.Put("/api/countries/{id}", h.Country.Update)
	r.Delete("/api/countries/{id}", h.Country.Delete)
	r.Get("/api/countries/{id}/routes", h.Country.GetRoutes)

	// Routes
	r.Get("/api/routes", h.Route.GetAll)
	r.Get("/api/routes/paginated", h.Route.GetPaginated)
	r.Get("/api/routes/with-coordinates", h.Route.GetWithCoordinates)
	r.Get("/api/routes/by-name", h.Route.GetByName)
	r.Post("/api/routes", h.Route.Create)
	r.Get("/api/routes/{id}", h.Route.GetByID)
	r.Put("/api/routes/{id}", h.Route.Update)
	r.Delete("/api/routes/{id}", h.Route.Delete)
	r.Get("/api/routes/{id}/trains", h.Route.GetTrains)
	r.Post("/api/routes/{id}/trains", h.Route.AddTrain)
	r.Delete("/api/routes/{id}/trains", h.Route.RemoveTrain)
	r.Delete("/api/routes/{id}/trains/{trainId}", h.Route.RemoveTrainByID)
	r.Get("/api/routes/{id}/sections", h.Route.GetSections)
	r.Post("/api/routes/{id}/sections", h.Route.CreateSection)
	r.Put("/api/routes/{id}/sections/{sectionId}", h.Route.UpdateSection)
	r.Delete("/api/routes/{id}/sections/{sectionId}", h.Route.DeleteSection)
	r.Get("/api/routes/{id}/sections/{sectionId}/trains", h.Route.GetSectionTrains)
	r.Post("/api/routes/{id}/sections/{sectionId}/trains", h.Route.AddSectionTrain)
	r.Delete("/api/routes/{id}/sections/{sectionId}/trains/{trainId}", h.Route.RemoveSectionTrain)
	r.Get("/api/routes/{id}/trains-with-coordinates", h.Route.GetTrainsWithCoordinates)
	r.Get("/api/routes/{id}/map-data", h.Route.GetMapData)
	r.Delete("/api/routes/{id}/map-data", h.Route.ClearMapData)
	r.Delete("/api/routes/{id}/timetables", h.Route.DeleteAllTimetables)
	r.Get("/api/routes/{id}/locations", h.Location.GetByRouteID)
	r.Get("/api/routes/{id}/timetables/export", h.Timetable.ExportAllForRoute)
	r.Post("/api/routes/import-zip", h.Timetable.ImportRouteZip)
	r.Post("/api/routes/fill-coordinates", h.Route.FillCoordinates)

	// Trains
	r.Get("/api/trains", h.Train.GetAll)
	r.Get("/api/trains/by-name", h.Train.GetByName)
	r.Post("/api/trains", h.Train.Create)
	r.Get("/api/trains/{id}", h.Train.GetByID)
	r.Put("/api/trains/{id}", h.Train.Update)
	r.Delete("/api/trains/{id}", h.Train.Delete)
	r.Get("/api/trains/{id}/routes", h.Train.GetRoutes)

	// Timetables
	r.Get("/api/timetables", h.Timetable.GetAll)
	r.Get("/api/timetables/paginated", h.Timetable.GetPaginated)
	r.Get("/api/timetables/route-summary", h.Timetable.GetRouteSummary)
	r.Get("/api/timetables/check-service", h.Timetable.CheckService)
	r.Get("/api/timetables/detect", h.Timetable.Detect)
	r.Get("/api/timetables/services-by-train", h.Timetable.GetServicesByTrain)
	r.Post("/api/timetables", h.Timetable.Create)
	r.Post("/api/timetables/import", h.Timetable.Import)
	r.Get("/api/timetables/{id}", h.Timetable.GetByID)
	r.Put("/api/timetables/{id}", h.Timetable.Update)
	r.Delete("/api/timetables/{id}", h.Timetable.Delete)
	r.Delete("/api/timetables/{id}/path-data", h.Timetable.DeletePathData)
	r.Get("/api/timetables/{id}/export", h.Timetable.Export)
	r.Get("/api/timetables/{id}/export/download", h.Timetable.ExportDownload)
	r.Get("/api/timetables/{id}/entries", h.Entry.GetByTimetableID)
	r.Post("/api/timetables/{id}/entries", h.Entry.Create)
	r.Get("/api/timetables/{id}/trains", h.Timetable.GetTrains)
	r.Post("/api/timetables/{id}/trains", h.Timetable.AddTrain)
	r.Delete("/api/timetables/{id}/trains", h.Timetable.RemoveTrain)
	r.Delete("/api/timetables/{id}/trains/{trainId}", h.Timetable.RemoveTrainByID)
	r.Get("/api/timetables/{id}/sections", h.Timetable.GetSections)
	r.Post("/api/timetables/{id}/sections", h.Timetable.AddSection)
	r.Delete("/api/timetables/{id}/sections/{sectionId}", h.Timetable.RemoveSection)

	// Entries
	r.Put("/api/entries/{id}", h.Entry.Update)
	r.Delete("/api/entries/{id}", h.Entry.Delete)

	// Station Mappings
	r.Get("/api/station-mappings", h.StationMapping.GetAll)
	r.Get("/api/station-mappings/lookup", h.StationMapping.GetLookup)
	r.Get("/api/station-mappings/lookup/{routeId}", h.StationMapping.GetLookupByRoute)
	r.Get("/api/station-mappings/route/{routeId}", h.StationMapping.GetByRouteID)
	r.Post("/api/station-mappings", h.StationMapping.Create)
	r.Post("/api/station-mappings/bulk", h.StationMapping.BulkImport)
	r.Post("/api/station-mappings/import-object", h.StationMapping.ImportFromObject)
	r.Get("/api/station-mappings/{id}", h.StationMapping.GetByID)
	r.Put("/api/station-mappings/{id}", h.StationMapping.Update)
	r.Delete("/api/station-mappings/{id}", h.StationMapping.Delete)

	// Weather
	r.Patch("/api/weather/set", h.Weather.Set)
	r.Get("/api/weather/live", h.LiveWeather.Fetch)
	r.Post("/api/weather/live/apply", h.LiveWeather.Apply)
	r.Get("/api/weather/historical", h.HistWeather.Fetch)
	r.Post("/api/weather/historical/apply", h.HistWeather.Apply)

	// Weather Presets
	r.Get("/api/weather-presets", h.WeatherPreset.GetAll)
	r.Post("/api/weather-presets", h.WeatherPreset.Create)
	r.Get("/api/weather-presets/{id}", h.WeatherPreset.GetByID)
	r.Put("/api/weather-presets/{id}", h.WeatherPreset.Update)
	r.Delete("/api/weather-presets/{id}", h.WeatherPreset.Delete)

	// Config
	r.Get("/api/config", h.Config.Get)
	r.Put("/api/config", h.Config.Update)
	r.Get("/api/config/default-paths", h.Config.GetDefaultPaths)
	r.Get("/api/config/current-key", h.Config.GetCurrentKey)
	r.Get("/api/config/server-urls", h.Config.GetServerURLs)

	// OCR
	r.Get("/api/ocr-status", h.OCR.GetStatus)
	r.Post("/api/extract", h.OCR.Extract)

	// Stream / HUD
	r.Get("/stream", h.Stream.Handle)
	r.Get("/route-data", h.HUD.GetCurrentRoute)
	r.Get("/api/hud/browse", h.HUD.Browse)
	r.Get("/api/hud/load-route", h.HUD.LoadRoute)
	r.Post("/api/upload-route", h.HUD.UploadRoute)
	r.Post("/api/clear-route", h.HUD.ClearRoute)
	r.Get("/api/timetable-items", h.HUD.GetTimetableItems)
	r.Post("/api/set-timetable-index", h.HUD.SetTimetableIndex)
	r.Post("/api/update-timetable-coordinates", h.HUD.UpdateTimetableCoordinates)

	// Recording
	r.Post("/api/recording/start/{timetableId}", h.Recording.Start)
	r.Post("/api/recording/stop", h.Recording.Stop)
	r.Post("/api/recording/reset", h.Recording.Reset)
	r.Post("/api/recording/pause", h.Recording.Pause)
	r.Post("/api/recording/resume", h.Recording.Resume)
	r.Get("/api/recording/route-data", h.Recording.GetRouteData)
	r.Post("/api/recording/save-timetable-coords", h.Recording.SaveTimetableCoords)
	r.Get("/api/recording/list", h.Recording.List)
	r.Get("/api/recording/file", h.Recording.GetFile)
	r.Get("/api/recording/check-existing/{timetableId}", h.Recording.CheckExisting)
	r.Get("/api/recording/check-any-existing", h.Recording.CheckAnyExisting)
	r.Post("/api/recording/load-file", h.Recording.LoadFile)
	r.Delete("/api/recording/delete-file/{filename}", h.Recording.DeleteFile)
	r.Post("/api/recording/mode", h.Recording.SetMode)
	r.Get("/api/recording/mode", h.Recording.GetMode)

	// Processing
	r.Post("/api/processing/process", h.Processing.Process)
	r.Get("/api/processing/list", h.Processing.List)
	r.Get("/api/processing/file", h.Processing.GetFile)
	r.Post("/api/processing/timetable", h.Processing.ProcessTimetable)

	// Map Data
	r.Get("/api/map/timetables", h.MapData.GetAllTimetables)
	r.Get("/api/map/timetables-with-data", h.MapData.GetTimetablesWithData)
	r.Get("/api/map/timetables/{id}/data", h.MapData.GetTimetableData)
	r.Post("/api/map/import-recording", h.MapData.ImportFromRecording)
	r.Post("/api/map/save-processed", h.MapData.SaveProcessed)
	r.Get("/api/map/route-data/{timetableId}", h.MapData.GetRouteDataFromDb)
	r.Post("/api/map/remake", h.MapData.Remake)

	// Subscriptions
	r.Get("/api/subscription/status", h.Subscription.GetStatus)
	r.Get("/api/subscription/data", h.Subscription.GetData)
	r.Post("/api/subscription/reset", h.Subscription.Reset)
	r.Post("/api/subscription/delete", h.Subscription.Delete)
	r.Post("/api/subscription/create", h.Subscription.Create)

	// Analysis & Route Processing
	r.Get("/api/analysis", h.Analysis.Analyze)
	r.Post("/api/route-processing/process-latest", h.RouteProcessing.ProcessLatest)
	r.Get("/api/route-processing/list", h.RouteProcessing.List)
	r.Get("/api/route-processing/file", h.RouteProcessing.GetFile)

	// Train Consists
	r.Get("/api/train-consists", h.TrainConsist.GetAll)
	r.Post("/api/train-consists", h.TrainConsist.Create)
	r.Post("/api/train-consists/bulk", h.TrainConsist.BulkCreate)
	r.Get("/api/timetables/{timetableId}/consists", h.TrainConsist.GetByTimetableID)

	// Misc
	r.Post("/api/reload-db", h.ReloadDB)
	r.Get("/api/actions", h.Action.GetAll)

	return r
}
