package router

import (
	"io/fs"
	"net/http"
	"os"
	"path/filepath"

	"github.com/go-chi/chi/v5"

	"hud-go/internal/config"
	"hud-go/views"
)

// servePage reads the named file from the embedded FS and writes it as text/html.
func servePage(file string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		data, err := fs.ReadFile(views.Files, file)
		if err != nil {
			http.Error(w, "page not found", http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Write(data)
	}
}

// registerStaticRoutes registers all page routes and static asset routes.
func registerStaticRoutes(r *chi.Mux) {
	// Favicon - return empty 204 to avoid 404 noise
	r.Get("/favicon.ico", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})

	// Named page routes
	r.Get("/", servePage("huds/start.html"))
	r.Get("/extract", servePage("extract.html"))
	r.Get("/experiment", servePage("huds/experiment.html"))
	r.Get("/tablet", servePage("huds/tablet.html"))
	r.Get("/mobile", servePage("huds/mobile.html"))
	r.Get("/start", servePage("huds/start.html"))
	r.Get("/find-timetable", servePage("huds/start-mobile.html"))
	r.Get("/start-mobile", servePage("huds/start-mobile.html"))
	r.Get("/desktop", servePage("huds/desktop.html"))
	r.Get("/data", servePage("data.html"))
	r.Get("/map", servePage("map.html"))
	r.Get("/weather", servePage("weather.html"))
	r.Get("/record", servePage("record-map.html"))
	r.Get("/settings", servePage("settings.html"))
	r.Get("/api-subscriptions", servePage("api-subscriptions.html"))
	r.Get("/recording-settings", servePage("recording-settings.html"))
	r.Get("/locations", servePage("locations/index.html"))
	r.Get("/extractor", servePage("extractor/index.html"))

	// Dynamic page routes - routes
	r.Get("/routes", servePage("routes/index.html"))
	r.Get("/routes/{id}", servePage("routes/show.html"))
	r.Get("/routes/{id}/edit", servePage("routes/show.html"))

	// Dynamic page routes - formations (was: /trains; renamed 2026-05-10)
	r.Get("/formations", servePage("formations/index.html"))
	r.Get("/formations/{id}", servePage("formations/show.html"))
	r.Get("/formations/{id}/edit", servePage("formations/show.html"))

	// Dynamic page routes - train classes (the class concept stays "class")
	r.Get("/train-classes", servePage("formations/classes.html"))
	r.Get("/train-classes/{id}", servePage("formations/class.html"))

	// Dynamic page routes - timetables
	// URL convention (matches /formations, /routes): /<id> = read-only view,
	// /<id>/edit = editable form. /<id>/view kept as a legacy alias for any
	// bookmarks that still reference the old path.
	r.Get("/timetables", servePage("timetables/index.html"))
	r.Get("/timetables/create", servePage("timetables/create.html"))
	r.Get("/timetables/{id}", servePage("timetables/show.html"))
	r.Get("/timetables/{id}/edit", servePage("timetables/edit.html"))
	// Legacy alias — earlier the read-only page was view.html and was
	// served at /timetables/{id}/view alongside /timetables/{id}. Both
	// now point at show.html so old bookmarks keep working.
	r.Get("/timetables/{id}/view", servePage("timetables/show.html"))

	// Dynamic page routes - countries
	r.Get("/countries", servePage("countries/index.html"))
	r.Get("/countries/{id}", servePage("countries/show.html"))

	// Dynamic page routes - weather presets
	r.Get("/weather-presets", servePage("weather-presets/index.html"))
	r.Get("/weather-presets/{id}", servePage("weather-presets/show.html"))

	// Static assets from embedded FS
	cssFS, _ := fs.Sub(views.Files, "css")
	r.Handle("/css/*", http.StripPrefix("/css/", http.FileServer(http.FS(cssFS))))

	jsFS, _ := fs.Sub(views.Files, "js")
	r.Handle("/js/*", http.StripPrefix("/js/", http.FileServer(http.FS(jsFS))))

	localesFS, _ := fs.Sub(views.Files, "locales")
	r.Handle("/locales/*", http.StripPrefix("/locales/", http.FileServer(http.FS(localesFS))))

	// Images: try user-uploaded images from appDir first, fall back to embedded
	r.Handle("/images/*", http.StripPrefix("/images/", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Check resources/images/ for user-uploaded images first
		resourcesPath := filepath.Join(config.ResourcesDir(), "images", r.URL.Path)
		if _, err := os.Stat(resourcesPath); err == nil {
			http.ServeFile(w, r, resourcesPath)
			return
		}

		// Fall back to embedded FS
		imagesFS, err := fs.Sub(views.Files, "images")
		if err != nil {
			http.NotFound(w, r)
			return
		}
		http.FileServer(http.FS(imagesFS)).ServeHTTP(w, r)
	})))
}
