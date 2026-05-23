package router

import (
	"encoding/json"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"

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
	// Custom HUDs: the tab page is embedded, but each HUD is a standalone
	// HTML file served from disk (resources/custom_huds) so users can add
	// their own without rebuilding the exe. See serveCustomHUD.
	r.Get("/custom-huds", servePage("custom-huds.html"))
	// AI_GUIDE.md is markdown (not a HUD), so it can't go through the
	// {name}.html serving route — give it its own path. Registered before the
	// {name} param route; chi matches the static segment first regardless.
	r.Get("/custom-huds/ai-guide", serveCustomHUDGuide)
	r.Get("/custom-huds/{name}", serveCustomHUD)
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

// customHUDsDir is the runtime folder where standalone custom HUD pages live
// (resources/custom_huds). Users drop *.html files here and they show up on
// the Custom HUDs tab automatically — no rebuild needed.
func customHUDsDir() string {
	return filepath.Join(config.ResourcesDir(), "custom_huds")
}

// customHUDNameRe validates a HUD slug: letters, digits, dash, underscore
// only. It rejects path separators and dots so a request can't escape
// custom_huds/ or pull a file other than <slug>.html.
var customHUDNameRe = regexp.MustCompile(`^[A-Za-z0-9_-]+$`)

// serveCustomHUD serves resources/custom_huds/{name}.html from disk (not the
// embedded FS) so users can add their own HUDs without rebuilding. Asset paths
// inside a HUD are absolute (/css, /js, /api), so serving under the
// /custom-huds/ prefix works unchanged.
func serveCustomHUD(w http.ResponseWriter, r *http.Request) {
	name := chi.URLParam(r, "name")
	if !customHUDNameRe.MatchString(name) {
		http.Error(w, "invalid HUD name", http.StatusBadRequest)
		return
	}
	path := filepath.Join(customHUDsDir(), name+".html")
	if _, err := os.Stat(path); err != nil {
		http.Error(w, "custom HUD not found", http.StatusNotFound)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	http.ServeFile(w, r, path)
}

// serveCustomHUDGuide serves resources/custom_huds/AI_GUIDE.md as plain text so
// it renders (and copy-pastes) in the browser. The guide tells an AI how to
// build a custom HUD page.
func serveCustomHUDGuide(w http.ResponseWriter, r *http.Request) {
	path := filepath.Join(customHUDsDir(), "AI_GUIDE.md")
	data, err := os.ReadFile(path)
	if err != nil {
		http.Error(w, "AI guide not found", http.StatusNotFound)
		return
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Write(data)
}

// listCustomHUDs returns the HUDs found in resources/custom_huds as JSON:
// [{name, title}]. `name` is the filename without .html (the URL slug); `title`
// is a humanized version for display. A missing folder yields an empty list so
// the tab renders empty rather than erroring.
func listCustomHUDs(w http.ResponseWriter, r *http.Request) {
	type hud struct {
		Name  string `json:"name"`
		Title string `json:"title"`
	}
	huds := []hud{}
	if entries, err := os.ReadDir(customHUDsDir()); err == nil {
		for _, e := range entries {
			if e.IsDir() || !strings.EqualFold(filepath.Ext(e.Name()), ".html") {
				continue
			}
			name := strings.TrimSuffix(e.Name(), filepath.Ext(e.Name()))
			if !customHUDNameRe.MatchString(name) {
				continue
			}
			huds = append(huds, hud{Name: name, Title: humanizeHUDName(name)})
		}
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(huds)
}

// humanizeHUDName turns a slug like "german_tablet" into "German Tablet" for
// the card heading on the Custom HUDs tab.
func humanizeHUDName(name string) string {
	parts := strings.FieldsFunc(name, func(r rune) bool { return r == '_' || r == '-' })
	for i, p := range parts {
		parts[i] = strings.ToUpper(p[:1]) + p[1:]
	}
	return strings.Join(parts, " ")
}
