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
	r.Get("/api/routes/{id}/formations", h.Route.GetFormations)
	r.Post("/api/routes/{id}/formations", h.Route.AddFormation)
	r.Delete("/api/routes/{id}/formations", h.Route.RemoveFormation)
	r.Delete("/api/routes/{id}/formations/{formationId}", h.Route.RemoveFormationByID)
	r.Get("/api/routes/{id}/sections", h.Route.GetSections)
	r.Post("/api/routes/{id}/sections", h.Route.CreateSection)
	r.Put("/api/routes/{id}/sections/{sectionId}", h.Route.UpdateSection)
	r.Delete("/api/routes/{id}/sections/{sectionId}", h.Route.DeleteSection)
	r.Get("/api/routes/{id}/sections/{sectionId}/formations", h.Route.GetSectionFormations)
	r.Post("/api/routes/{id}/sections/{sectionId}/formations", h.Route.AddSectionFormation)
	r.Delete("/api/routes/{id}/sections/{sectionId}/formations/{formationId}", h.Route.RemoveSectionFormation)
	r.Get("/api/routes/{id}/formations-with-coordinates", h.Route.GetFormationsWithCoordinates)
	r.Get("/api/routes/{id}/map-data", h.Route.GetMapData)
	r.Delete("/api/routes/{id}/map-data", h.Route.ClearMapData)
	r.Delete("/api/routes/{id}/timetables", h.Route.DeleteAllTimetables)
	r.Get("/api/routes/{id}/locations", h.Location.GetByRouteID)
	r.Get("/api/routes/{id}/timetables/export", h.Timetable.ExportAllForRoute)
	r.Post("/api/routes/import-zip", h.Timetable.ImportRouteZip)

	// Formations
	r.Get("/api/formations", h.Formation.GetAll)
	r.Get("/api/formations/by-name", h.Formation.GetByName)
	r.Post("/api/formations", h.Formation.Create)
	r.Get("/api/formations/{id}", h.Formation.GetByID)
	r.Put("/api/formations/{id}", h.Formation.Update)
	r.Delete("/api/formations/{id}", h.Formation.Delete)
	r.Get("/api/formations/{id}/routes", h.Formation.GetRoutes)
	r.Get("/api/formations/{id}/vehicles", h.Formation.GetVehicles)

	// Train classes (groups physical formations by their TSW class)
	r.Get("/api/train-classes", h.Formation.ListClasses)
	r.Get("/api/train-classes/voltages", h.Formation.ListClassVoltages)
	r.Get("/api/train-classes/categories", h.Formation.ListClassCategories)
	r.Get("/api/train-classes/types", h.Formation.ListClassTypes)
	r.Post("/api/train-classes/refresh-metadata", h.Formation.RefreshClassMetadata)
	r.Get("/api/train-classes/{id}", h.Formation.GetClassByID)
	r.Get("/api/train-classes/{id}/routes", h.Formation.GetClassRoutes)
	r.Get("/api/routes/{id}/train-classes", h.Route.GetTrainClasses)

	// Timetables
	r.Get("/api/timetables", h.Timetable.GetAll)
	r.Get("/api/timetables/paginated", h.Timetable.GetPaginated)
	r.Get("/api/timetables/route-summary", h.Timetable.GetRouteSummary)
	r.Get("/api/timetables/sources", h.Timetable.GetSources)
	r.Get("/api/timetables/check-service", h.Timetable.CheckService)
	r.Get("/api/timetables/detect", h.Timetable.Detect)
	r.Get("/api/timetables/services-by-formation", h.Timetable.GetServicesByFormation)
	r.Post("/api/timetables", h.Timetable.Create)
	r.Post("/api/timetables/import", h.Timetable.Import)
	r.Get("/api/timetables/{id}", h.Timetable.GetByID)
	r.Put("/api/timetables/{id}", h.Timetable.Update)
	r.Delete("/api/timetables/{id}", h.Timetable.Delete)
	r.Delete("/api/timetables/{id}/path-data", h.Timetable.DeletePathData)
	r.Get("/api/timetables/{id}/export", h.Timetable.Export)
	r.Get("/api/timetables/{id}/export/download", h.Timetable.ExportDownload)
	r.Get("/api/timetables/{id}/map-features", h.Timetable.MapFeatures)
	r.Post("/api/timetables/{id}/build-map-features", h.Timetable.BuildMapFeaturesForTimetable)
	r.Post("/api/routes/{id}/build-timetable-maps", h.Timetable.BuildMapFeaturesForRoute)
	r.Get("/api/routes/{id}/build-timetable-maps", h.Timetable.BuildMapFeaturesForRouteStatus)
	r.Get("/api/timetables/{id}/entries", h.Entry.GetByTimetableID)
	r.Post("/api/timetables/{id}/entries", h.Entry.Create)
	r.Get("/api/timetables/{id}/formations", h.Timetable.GetFormations)
	r.Post("/api/timetables/{id}/formations", h.Timetable.AddFormation)
	r.Delete("/api/timetables/{id}/formations", h.Timetable.RemoveFormation)
	r.Delete("/api/timetables/{id}/formations/{formationId}", h.Timetable.RemoveFormationByID)
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

	// Extractor (in-app per-route pak extractor — see /extractor page)
	r.Get("/api/extractor/routes", h.Extractor.ListRoutes)
	r.Get("/api/extractor/status", h.Extractor.Status)
	r.Get("/api/extractor/stream", h.Extractor.Stream)
	r.Post("/api/extractor/start", h.Extractor.Start)
	r.Post("/api/extractor/stop", h.Extractor.Stop)
	r.Post("/api/extractor/rescan", h.Extractor.Rescan)
	r.Delete("/api/extractor/zip/{route}", h.Extractor.DeleteZip)
	r.Post("/api/extractor/completed/{route}", h.Extractor.MarkCompleted)
	r.Delete("/api/extractor/completed/{route}", h.Extractor.UnmarkCompleted)
	// Dev-mode "Nuke DB": wipe every user table except the three seed
	// tables (countries, weather_presets, timetable_actions). Front-end
	// gates the button on developmentMode; the endpoint itself is open
	// so it must only be invoked behind an explicit user click + confirm.
	r.Post("/api/extractor/nuke-db", h.Extractor.NukeDB)
	// Dev-mode "Rebuild Train Classes from RVDs": reconciles train_classes
	// against the canonical pak_rvds data — links ghost rows by friendly
	// name, inserts missing rail_vehicle_class values, backfills snapshot
	// fields (is_drivable etc.) from RVDs.
	r.Post("/api/extractor/rebuild-train-classes", h.Extractor.RebuildTrainClassesFromRVDs)


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

	// Route Processing
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
