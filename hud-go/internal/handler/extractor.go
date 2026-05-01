package handler

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/go-chi/chi/v5"

	"hud-go/internal/catalog"
	"hud-go/internal/config"
	"hud-go/internal/extractor"
	"hud-go/internal/output"
	"hud-go/internal/pak"
	"hud-go/internal/pak/uasset"
	"hud-go/internal/util"
)

// ExtractorHandler drives in-process per-route extractions and broadcasts
// progress to /api/extractor/stream subscribers via SSE.
//
// The extractor runs as Go code (not a subprocess) — see hud-go/internal/extractor.
// Third-party tools it calls out to (UAssetGUI.exe, repak.exe) are still
// child processes, but the hud-go binary itself owns the orchestration.
//
// Concurrency model:
//   - At most one extraction job runs at a time, guarded by stateMu.
//   - The running job lives on its own goroutine; subscribers (SSE clients)
//     each get their own buffered channel; the job goroutine fans events
//     out under subsMu.
//   - Stop is cooperative: cancelling the context lets the loop exit at the
//     next route boundary. Mid-route cancellation is best-effort (the
//     extractor finishes the current route before checking ctx).
type ExtractorHandler struct {
	db *sql.DB // for the user-marked Completed Routes list

	// Used by the optional auto-import flow (extractorAutoImport setting):
	// after a route's zip is written, the extractor wipes any existing DB
	// row for the same route name and re-imports the zip via the same
	// HTTP handler the /api/routes/import-zip endpoint serves. We invoke
	// it via httptest.NewRecorder so there's no TCP round-trip and no
	// router dependency — just a direct in-process call with synthesized
	// request/response objects.
	tt *TimetableHandler

	stateMu sync.RWMutex
	state   extractorState

	subsMu sync.Mutex
	subs   []chan extractorEvent

	// hasTimetableMu guards hasTimetableCache: pakPath → "does this pak
	// ship any timetable/scenario assets". Populated lazily on the first
	// /api/extractor/routes request and reused for the lifetime of the
	// process. ~30 s warmup the first time, instant thereafter. Restart
	// hud-go to pick up paks that gained/lost timetables across a DLC
	// update.
	hasTimetableMu    sync.Mutex
	hasTimetableCache map[string]bool

	// routeDefMu guards routeDefCache: pakPath → parsed
	// <X>RouteDefinition asset (DisplayName + CountryCode). Populated
	// lazily on each /api/extractor/routes request — first call is slow
	// (~1–2 s per pak that has a RouteDefinition; cargo / wagon DLCs
	// that don't ship one short-circuit fast and cache nil).
	routeDefMu    sync.Mutex
	routeDefCache map[string]*uasset.RouteDefinition

	// dlcNameMu guards dlcNameCache: pakPath → display name pulled from
	// the pak's `_Gameplay.uplugin` Description. Used by /extractor's
	// row labelling for paks that ship no RouteDefinition (cargo / wagon
	// / train DLCs). Empty-string entries are cached too — they mean
	// "junk Description, fall back to CamelCase".
	dlcNameMu    sync.Mutex
	dlcNameCache map[string]string
}

type extractorJobStatus string

const (
	extStatusIdle     extractorJobStatus = "idle"
	extStatusRunning  extractorJobStatus = "running"
	extStatusStopping extractorJobStatus = "stopping"
)

type extractorState struct {
	status         extractorJobStatus
	currentRoute   string
	startedAt      time.Time
	totalRoutes    int
	doneCount      int
	skippedCount   int
	failedCount    int
	cancel         context.CancelFunc
	lastError      string
}

// extractorEvent is the wire shape for SSE messages. The same shape is
// used for both progress updates ("started", "route_started", etc.) and
// log lines ("log") so the client only needs one consumer.
type extractorEvent struct {
	Type      string `json:"type"`
	Route     string `json:"route,omitempty"`
	Message   string `json:"message,omitempty"`
	SizeBytes int64  `json:"size_bytes,omitempty"`
	ExitCode  int    `json:"exit_code,omitempty"`
	// Progress snapshot included on every event so reconnecting clients
	// can rebuild state from the latest message.
	Status       extractorJobStatus `json:"status"`
	TotalRoutes  int                `json:"total_routes,omitempty"`
	DoneCount    int                `json:"done_count,omitempty"`
	SkippedCount int                `json:"skipped_count,omitempty"`
	FailedCount  int                `json:"failed_count,omitempty"`
	Timestamp    int64              `json:"ts"`
}

// ListRoutes returns the /extractor list as a route tree: each top-level
// row is a parent route the user has installed (a pak with its own
// RouteDefinition), and `children` is the addon paks (cargo / wagon /
// train DLCs) that ship timetables for that parent. An addon that
// references multiple installed parents appears under each. Addons
// whose only references are to unowned routes are dropped silently.
//
// Top-level rows carry zip_exists / zip_bytes / zip_mtime / completed
// for the per-route extraction zip the user clicks "Run" to produce.
// Addon rows don't have their own zip — they're bundled into the
// parent's.
//
// GET /api/extractor/routes
func (h *ExtractorHandler) ListRoutes(w http.ResponseWriter, r *http.Request) {
	cfg := config.Get()
	tswPath := strings.TrimSpace(cfg.ExtractorTswPath)
	outDir := strings.TrimSpace(cfg.ExtractorOutputDir)
	if tswPath == "" {
		util.Error(w, http.StatusBadRequest, "extractorTswPath setting is empty")
		return
	}

	paks, err := catalog.LoadAll(h.db)
	if err != nil {
		util.Error(w, http.StatusInternalServerError, err.Error())
		return
	}
	// No auto-scan on cold start — the user explicitly clicks Scan to
	// populate the catalog. An empty catalog returns an empty tree;
	// the UI shows "Scan" instead of "Rescan" on the toolbar button.

	tree := catalog.BuildTree(paks)
	completed := h.loadCompletedRoutes()

	// Stamp parent rows with zip-presence + completed flag.
	for _, parent := range tree {
		parent.Country = output.CountryNameFromCode(parent.Country)
		parent.Completed = completed[parent.Codename]
		if outDir != "" {
			zipName := strings.ReplaceAll(parent.DisplayName, " ", "_") + ".zip"
			zipPath := filepath.Join(outDir, zipName)
			if st, err := os.Stat(zipPath); err == nil && !st.IsDir() {
				parent.ZipExists = true
				parent.ZipBytes = st.Size()
				parent.ZipMTime = st.ModTime().Unix()
			}
		}
		// Addon children carry country codes; expand the same way for
		// display consistency. They don't get zip / completed stamps.
		for _, child := range parent.Children {
			child.Country = output.CountryNameFromCode(child.Country)
		}
	}

	util.JSON(w, http.StatusOK, map[string]any{
		"items":      tree,
		"output_dir": outDir,
	})
}

// Status returns the current job snapshot — used by the UI on initial load
// before the SSE stream connects.
//
// GET /api/extractor/status
func (h *ExtractorHandler) Status(w http.ResponseWriter, r *http.Request) {
	util.JSON(w, http.StatusOK, h.snapshotStatus())
}

func (h *ExtractorHandler) snapshotStatus() map[string]any {
	h.stateMu.RLock()
	defer h.stateMu.RUnlock()
	return map[string]any{
		"status":        h.state.status,
		"current_route": h.state.currentRoute,
		"started_at":    h.state.startedAt.Unix(),
		"total_routes":  h.state.totalRoutes,
		"done_count":    h.state.doneCount,
		"skipped_count": h.state.skippedCount,
		"failed_count":  h.state.failedCount,
		"last_error":    h.state.lastError,
	}
}

// Start kicks off an extraction job. Optional body:
//
//	{ "routes": ["BostonProvidence", ...],   // empty = all known routes
//	  "skip_existing": true }                // default true
//
// POST /api/extractor/start
func (h *ExtractorHandler) Start(w http.ResponseWriter, r *http.Request) {
	cfg := config.Get()
	tswPath := strings.TrimSpace(cfg.ExtractorTswPath)
	outDir := strings.TrimSpace(cfg.ExtractorOutputDir)
	if tswPath == "" {
		util.Error(w, http.StatusBadRequest, "extractorTswPath setting is empty")
		return
	}
	if outDir == "" {
		util.Error(w, http.StatusBadRequest, "extractorOutputDir setting is empty")
		return
	}

	var body struct {
		Routes       []string `json:"routes"`
		SkipExisting *bool    `json:"skip_existing"`
	}
	// Body is optional; ignore decode errors on empty body.
	_ = json.NewDecoder(r.Body).Decode(&body)
	skipExisting := true
	if body.SkipExisting != nil {
		skipExisting = *body.SkipExisting
	}

	// Reject if a job is already running.
	h.stateMu.Lock()
	if h.state.status != extStatusIdle {
		h.stateMu.Unlock()
		util.Error(w, http.StatusConflict, "an extraction job is already running")
		return
	}
	// Resolve which routes to run before we flip status to running.
	selected, err := h.resolveTargetRoutes(tswPath, body.Routes)
	if err != nil {
		h.stateMu.Unlock()
		util.Error(w, http.StatusInternalServerError, err.Error())
		return
	}

	ctx, cancel := context.WithCancel(context.Background())
	h.state = extractorState{
		status:      extStatusRunning,
		startedAt:   time.Now(),
		totalRoutes: len(selected),
		cancel:      cancel,
	}
	h.stateMu.Unlock()

	go h.runJob(ctx, tswPath, outDir, cfg.ExtractorTempDir, selected, skipExisting)

	util.JSON(w, http.StatusAccepted, map[string]any{
		"status":       "running",
		"total_routes": len(selected),
	})
}

// Stop cancels the running job. The active subprocess gets killed via the
// exec.CommandContext; partial zips are removed at the route boundary.
//
// POST /api/extractor/stop
func (h *ExtractorHandler) Stop(w http.ResponseWriter, r *http.Request) {
	h.stateMu.Lock()
	if h.state.status != extStatusRunning {
		h.stateMu.Unlock()
		util.JSON(w, http.StatusOK, map[string]any{"status": "idle"})
		return
	}
	h.state.status = extStatusStopping
	cancel := h.state.cancel
	h.stateMu.Unlock()

	if cancel != nil {
		cancel()
	}
	util.JSON(w, http.StatusOK, map[string]any{"status": "stopping"})
}

// MarkCompleted records a route as user-completed (moves it from the
// "Routes" list to the "Completed Routes" list on /extractor). Persistent
// across restarts via the extractor_completed_routes table.
//
// POST /api/extractor/completed/{route}
func (h *ExtractorHandler) MarkCompleted(w http.ResponseWriter, r *http.Request) {
	route := chi.URLParam(r, "route")
	if route == "" || strings.ContainsAny(route, `/\`) {
		util.Error(w, http.StatusBadRequest, "invalid route name")
		return
	}
	if h.db == nil {
		util.Error(w, http.StatusInternalServerError, "no db")
		return
	}
	if _, err := h.db.Exec(
		`INSERT OR IGNORE INTO extractor_completed_routes (codename) VALUES (?)`,
		route,
	); err != nil {
		util.Error(w, http.StatusInternalServerError, err.Error())
		return
	}
	util.JSON(w, http.StatusOK, map[string]any{"success": true, "route": route, "completed": true})
}

// UnmarkCompleted removes a route from the Completed Routes list, moving
// it back to the regular Routes list.
//
// DELETE /api/extractor/completed/{route}
func (h *ExtractorHandler) UnmarkCompleted(w http.ResponseWriter, r *http.Request) {
	route := chi.URLParam(r, "route")
	if route == "" || strings.ContainsAny(route, `/\`) {
		util.Error(w, http.StatusBadRequest, "invalid route name")
		return
	}
	if h.db == nil {
		util.Error(w, http.StatusInternalServerError, "no db")
		return
	}
	if _, err := h.db.Exec(
		`DELETE FROM extractor_completed_routes WHERE codename = ?`,
		route,
	); err != nil {
		util.Error(w, http.StatusInternalServerError, err.Error())
		return
	}
	util.JSON(w, http.StatusOK, map[string]any{"success": true, "route": route, "completed": false})
}

// loadCompletedRoutes returns the set of codenames currently flagged as
// completed. Used by ListRoutes to populate the per-row `completed`
// field. Empty set on any DB error — better to render the page without
// the flag than to fail the whole request.
func (h *ExtractorHandler) loadCompletedRoutes() map[string]bool {
	out := map[string]bool{}
	if h.db == nil {
		return out
	}
	rows, err := h.db.Query(`SELECT codename FROM extractor_completed_routes`)
	if err != nil {
		log.Printf("[extractor] loadCompletedRoutes: %v", err)
		return out
	}
	defer rows.Close()
	for rows.Next() {
		var c string
		if err := rows.Scan(&c); err == nil {
			out[c] = true
		}
	}
	return out
}

// DeleteZip removes a single route's output zip. Used by the per-row "×"
// button in the UI so the next run re-extracts.
//
// DELETE /api/extractor/zip/{route}
func (h *ExtractorHandler) DeleteZip(w http.ResponseWriter, r *http.Request) {
	route := chi.URLParam(r, "route")
	if route == "" {
		util.Error(w, http.StatusBadRequest, "missing route")
		return
	}
	cfg := config.Get()
	outDir := strings.TrimSpace(cfg.ExtractorOutputDir)
	if outDir == "" {
		util.Error(w, http.StatusBadRequest, "extractorOutputDir setting is empty")
		return
	}
	// Path-traversal guard: route name must be a single component, no separators.
	if strings.ContainsAny(route, `/\`) || route == "." || route == ".." {
		util.Error(w, http.StatusBadRequest, "invalid route name")
		return
	}
	// `route` here is the codename (the API key the UI uses). Translate to
	// the display-name-based zip filename so we delete the actual file.
	zip := filepath.Join(outDir, zipFilenameForCodename(route))
	if err := os.Remove(zip); err != nil && !os.IsNotExist(err) {
		util.Error(w, http.StatusInternalServerError, err.Error())
		return
	}
	util.JSON(w, http.StatusOK, map[string]any{"success": true, "route": route})
}

// Stream is an SSE endpoint that pushes extractorEvent messages to the
// browser. Each connected client gets its own buffered channel; if the
// channel fills (slow client), the broadcaster drops the event for that
// client only.
//
// GET /api/extractor/stream
func (h *ExtractorHandler) Stream(w http.ResponseWriter, r *http.Request) {
	rc := http.NewResponseController(w)

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)

	ch := make(chan extractorEvent, 256)
	h.subscribe(ch)
	defer h.unsubscribe(ch)

	// Send a snapshot so the UI immediately reflects current state without
	// having to also hit /status.
	snap := h.snapshotEvent("snapshot", "", "")
	writeSSE(w, rc, snap)

	// Keepalive every 15 s so proxies don't time out the connection.
	keepalive := time.NewTicker(15 * time.Second)
	defer keepalive.Stop()

	for {
		select {
		case <-r.Context().Done():
			return
		case <-keepalive.C:
			fmt.Fprint(w, ": keepalive\n\n")
			if err := rc.Flush(); err != nil {
				return
			}
		case ev := <-ch:
			if !writeSSE(w, rc, ev) {
				return
			}
		}
	}
}

// ---------- internal job runner ----------

func (h *ExtractorHandler) runJob(ctx context.Context, tswPath, outDir, tempDir string,
	routes []routeListEntry, skipExisting bool) {
	defer func() {
		h.stateMu.Lock()
		h.state.status = extStatusIdle
		h.state.currentRoute = ""
		h.state.cancel = nil
		h.stateMu.Unlock()
	}()

	if err := os.MkdirAll(outDir, 0o755); err != nil {
		h.broadcast(h.snapshotEvent("finished", "", "mkdir output: "+err.Error()))
		return
	}
	if tempDir != "" {
		_ = os.MkdirAll(tempDir, 0o755)
	}

	h.broadcast(h.snapshotEvent("started", "", ""))

	for _, rt := range routes {
		if ctx.Err() != nil {
			break
		}

		zip := filepath.Join(outDir, rt.zipFilename())
		if skipExisting {
			if st, err := os.Stat(zip); err == nil && !st.IsDir() && st.Size() > 0 {
				h.stateMu.Lock()
				h.state.skippedCount++
				h.stateMu.Unlock()
				h.broadcast(h.snapshotEvent("route_skipped", rt.codename, "zip already exists"))
				continue
			}
		}

		h.stateMu.Lock()
		h.state.currentRoute = rt.codename
		h.stateMu.Unlock()
		h.broadcast(h.snapshotEvent("route_started", rt.codename, ""))

		exitCode, runErr := h.runSingleRoute(ctx, tswPath, tempDir, rt.codename, zip)

		// Don't leave a partial zip on cancel/failure — the next run should
		// re-attempt this route from scratch.
		if runErr != nil || exitCode != 0 {
			_ = os.Remove(zip)
			h.stateMu.Lock()
			h.state.failedCount++
			h.state.lastError = fmt.Sprintf("%s: %s", rt.codename, errorOrCode(runErr, exitCode))
			h.stateMu.Unlock()
			ev := h.snapshotEvent("route_failed", rt.codename, errorOrCode(runErr, exitCode))
			ev.ExitCode = exitCode
			h.broadcast(ev)
			continue
		}

		var size int64
		if st, err := os.Stat(zip); err == nil {
			size = st.Size()
		}
		h.stateMu.Lock()
		h.state.doneCount++
		h.stateMu.Unlock()
		ev := h.snapshotEvent("route_done", rt.codename, "")
		ev.SizeBytes = size
		h.broadcast(ev)

		// Auto-import: if the user has the toggle on in /extractor settings,
		// wipe the existing route from the DB (cascades timetables / coords
		// / markers / cab_stop_signs / track_markers) and re-import the
		// freshly-written zip. Failures here don't fail the route — the zip
		// is on disk and can be uploaded manually.
		if config.Get().ExtractorAutoImport {
			h.broadcast(h.snapshotEvent("log", rt.codename, "[auto-import] starting…"))
			if err := h.autoImportZip(rt.displayName, zip); err != nil {
				h.broadcast(h.snapshotEvent("log", rt.codename, "[auto-import] failed: "+err.Error()))
			} else {
				h.broadcast(h.snapshotEvent("log", rt.codename, "[auto-import] done"))
				// Auto-mark the row as Completed so it slides into the
				// "Completed Routes" section without the user having to
				// click the ✓ button. Idempotent — INSERT OR IGNORE.
				if h.db != nil {
					if _, err := h.db.Exec(
						`INSERT OR IGNORE INTO extractor_completed_routes (codename) VALUES (?)`,
						rt.codename,
					); err != nil {
						log.Printf("[extractor] auto-mark-completed %s: %v", rt.codename, err)
					}
				}
			}
		}
	}

	if ctx.Err() != nil {
		h.broadcast(h.snapshotEvent("stopped", "", ""))
	} else {
		h.broadcast(h.snapshotEvent("finished", "", ""))
	}
}

// runSingleRoute drives one route end-to-end through the in-process
// extractor: parse the pak's timetables, then write a per-route zip via
// the package writer. Progress lines flow into the SSE broadcaster via the
// extractor's Logger callback.
//
// Returns (0, nil) on success and (-1, err) on failure. We keep the int
// return for signature compatibility with the previous subprocess-based
// implementation; with in-process extraction, the only meaningful values
// are 0 (success) and -1 (an error occurred).
//
// ctx is observed at the route boundary by the caller (runJob); inside
// this function the in-process work runs to completion. Mid-route
// cancellation is a follow-up — the extractor doesn't currently take a
// context, and adding one means threading it through a couple of layers
// of subprocess invocations (repak/UAssetGUI). Best-effort Stop is
// adequate for the use case (per-route runs are minutes, not hours).
func (h *ExtractorHandler) runSingleRoute(ctx context.Context, tswPath, tempDir, route, outZip string) (int, error) {
	cfg := extractor.Config{
		TSWPath:     tswPath,
		RouteFilter: route,
		WorkDir:     tempDir,
		Verbose:     true,
		Logger: func(format string, args ...any) {
			line := strings.TrimRight(fmt.Sprintf(format, args...), "\r\n")
			if line == "" {
				return
			}
			h.broadcast(h.snapshotEvent("log", route, line))
		},
	}
	// Pre-load the RVD map from the catalog (populated once at scan
	// time) so the extractor skips its slow per-extraction
	// "Scanning for trains" walk. Empty result is fine — the
	// extractor will fall back to ScanAllRVDs on the fly.
	if rvds, err := catalog.LoadAllRVDs(h.db); err == nil && len(rvds) > 0 {
		cfg.PreloadedRVDs = rvds
	} else if err != nil {
		log.Printf("[extractor] LoadAllRVDs: %v", err)
	}
	if err := cfg.AutoDetect(); err != nil {
		return -1, fmt.Errorf("auto-detect tools: %w", err)
	}
	timetables, err := extractor.New(cfg).Extract()
	if err != nil {
		return -1, fmt.Errorf("extract: %w", err)
	}
	if len(timetables) == 0 {
		// No timetables in this pak — write nothing rather than an empty
		// zip so resumability ("zip exists → skip") doesn't think we did
		// the work. Treat as a successful no-op.
		return 0, nil
	}
	if _, err := output.WritePackageFiltered(outZip, timetables, ""); err != nil {
		return -1, fmt.Errorf("write package: %w", err)
	}
	return 0, nil
}

// ---------- subscriber book-keeping ----------

func (h *ExtractorHandler) subscribe(ch chan extractorEvent) {
	h.subsMu.Lock()
	h.subs = append(h.subs, ch)
	h.subsMu.Unlock()
}

func (h *ExtractorHandler) unsubscribe(ch chan extractorEvent) {
	h.subsMu.Lock()
	defer h.subsMu.Unlock()
	for i, c := range h.subs {
		if c == ch {
			h.subs = append(h.subs[:i], h.subs[i+1:]...)
			return
		}
	}
}

// broadcast pushes an event to every subscriber. Non-blocking — if a
// client's channel is full (slow consumer), we drop the event for that
// client only.
func (h *ExtractorHandler) broadcast(ev extractorEvent) {
	h.subsMu.Lock()
	defer h.subsMu.Unlock()
	for _, c := range h.subs {
		select {
		case c <- ev:
		default:
			// drop on backpressure
		}
	}
}

// snapshotEvent constructs an event populated with the current job
// counters so a reconnecting client can rebuild state from any single
// message.
func (h *ExtractorHandler) snapshotEvent(typ, route, msg string) extractorEvent {
	h.stateMu.RLock()
	defer h.stateMu.RUnlock()
	return extractorEvent{
		Type:         typ,
		Route:        route,
		Message:      msg,
		Status:       h.state.status,
		TotalRoutes:  h.state.totalRoutes,
		DoneCount:    h.state.doneCount,
		SkippedCount: h.state.skippedCount,
		FailedCount:  h.state.failedCount,
		Timestamp:    time.Now().Unix(),
	}
}

// ---------- auto-import ----------

// autoImportZip wipes any existing DB row for `routeName` (cascading
// timetables / route_coordinates / route_markers / cab_stop_signs /
// track_markers via the FK constraints we set up in migrations) and
// re-imports the just-written zip via the same handler the
// /api/routes/import-zip endpoint serves.
//
// The import is invoked via httptest.NewRecorder so we get a direct
// in-process call — no TCP, no chi router lookup, no port-discovery
// dance. The recorded response is logged on failure but otherwise
// discarded; the on-disk zip remains regardless of import outcome so the
// user can re-upload manually if something goes wrong.
//
// Lookup is by exact route name match. We try the display name first
// ("Isle of Wight") because that's what the importer now stores after
// the recent flip; fall back to a whitespace-stripped compare so older
// codename rows ("IsleOfWight") still get caught and removed.
func (h *ExtractorHandler) autoImportZip(routeName, zipPath string) error {
	if h.tt == nil || h.db == nil {
		return fmt.Errorf("auto-import not configured (tt or db nil)")
	}

	// 1. Wipe existing route. Match tolerant of the display-vs-codename
	//    flip that happened mid-development.
	rows, err := h.db.Query(`
		SELECT id, name FROM routes
		WHERE name = ?
		   OR REPLACE(LOWER(name), ' ', '') = REPLACE(LOWER(?), ' ', '')`,
		routeName, routeName)
	if err != nil {
		return fmt.Errorf("lookup existing route: %w", err)
	}
	defer rows.Close()
	var ids []int
	var names []string
	for rows.Next() {
		var id int
		var name string
		if err := rows.Scan(&id, &name); err == nil {
			ids = append(ids, id)
			names = append(names, name)
		}
	}
	rows.Close()
	for i, id := range ids {
		if _, err := h.db.Exec(`DELETE FROM timetables WHERE route_id = ?`, id); err != nil {
			return fmt.Errorf("delete timetables for %q: %w", names[i], err)
		}
		if _, err := h.db.Exec(`DELETE FROM routes WHERE id = ?`, id); err != nil {
			return fmt.Errorf("delete route %q: %w", names[i], err)
		}
		log.Printf("[extractor] auto-import: wiped existing route id=%d name=%q", id, names[i])
	}
	deleteOrphanTrains(h.db)

	// 2. Read the zip and POST it to the existing import handler via an
	//    in-process httptest recorder. Same handler a normal user upload
	//    flows through — no logic duplication.
	data, err := os.ReadFile(zipPath)
	if err != nil {
		return fmt.Errorf("read zip: %w", err)
	}
	req := httptest.NewRequest(http.MethodPost, "/api/routes/import-zip", bytes.NewReader(data))
	req.Header.Set("Content-Type", "application/zip")
	rr := httptest.NewRecorder()
	h.tt.ImportRouteZip(rr, req)
	if rr.Code >= 400 {
		return fmt.Errorf("import handler returned %d: %s", rr.Code, strings.TrimSpace(rr.Body.String()))
	}
	return nil
}

// ---------- helpers ----------

type routeListEntry struct {
	codename    string
	displayName string
	country     string
	pakPath     string
}

// zipFilenameForCodename returns the per-route zip's basename. We use
// the display name with spaces → underscores ("Isle_of_Wight.zip") so
// the file is readable in Explorer / a file picker. When the codename
// has no display-name mapping, output.RouteDisplayName falls back to a
// CamelCase split.
//
// IMPORTANT: must run output.SanitizeForFilename, not just a space →
// underscore swap. Canonical DisplayNames from the data can contain
// colons and other Windows-illegal chars (e.g. "London Overground:
// Gospel Oak - Barking Riverside") — Windows treats ':' in a path as
// the alternate-data-stream separator, so os.Create("...foo:bar.zip")
// silently writes the zip bytes into an ADS attached to a 0-byte
// "...foo" file. Sanitising drops the colon up front.
//
// Codename remains the internal/API key everywhere else (selection,
// delete, SSE events). Only the filename uses the human form.
func zipFilenameForCodename(codename string) string {
	name := strings.TrimSpace(output.RouteDisplayName(codename))
	if name == "" {
		name = codename
	}
	return output.SanitizeForFilename(name) + ".zip"
}

func (rt routeListEntry) zipFilename() string {
	if name := strings.TrimSpace(rt.displayName); name != "" {
		return output.SanitizeForFilename(name) + ".zip"
	}
	return zipFilenameForCodename(rt.codename)
}

// listRoutes returns the route catalogue for the install at tswPath,
// reading from the persistent pak_catalog table. If the catalog is
// empty (first /extractor visit on a new DB), it kicks off an inline
// scan first — that's a one-time ~1-3 minute warmup. Subsequent calls
// are instant.
//
// Use POST /api/extractor/rescan to force a refresh after installing
// or updating a DLC.
func (h *ExtractorHandler) listRoutes(tswPath string) ([]routeListEntry, error) {
	all, err := catalog.LoadAll(h.db)
	if err != nil {
		return nil, fmt.Errorf("load catalog: %w", err)
	}
	if len(all) == 0 {
		// Cold start: do a one-shot scan so the page renders something
		// useful instead of an empty list.
		if _, err := h.runCatalogScan(tswPath, false); err != nil {
			return nil, err
		}
		all, err = catalog.LoadAll(h.db)
		if err != nil {
			return nil, fmt.Errorf("load catalog post-scan: %w", err)
		}
	}
	out := make([]routeListEntry, 0, len(all))
	for _, p := range all {
		dn := p.DisplayName
		if dn == "" {
			dn = output.RouteDisplayName(p.Codename)
		}
		out = append(out, routeListEntry{
			codename:    p.Codename,
			displayName: dn,
			country:     output.CountryNameFromCode(p.CountryCode),
			pakPath:     p.PakPath,
		})
	}
	return out, nil
}

// runCatalogScan is the shared entry point for both the cold-start
// auto-scan in listRoutes and the user-triggered /rescan endpoint.
// `force=true` re-scans every pak regardless of mtime; otherwise the
// scan is incremental.
func (h *ExtractorHandler) runCatalogScan(tswPath string, force bool) (catalog.ScanResult, error) {
	cfg := extractor.Config{TSWPath: tswPath}
	if err := cfg.AutoDetect(); err != nil {
		return catalog.ScanResult{}, fmt.Errorf("auto-detect tools: %w", err)
	}
	scratch := strings.TrimSpace(config.Get().ExtractorTempDir)
	if scratch == "" {
		scratch = os.TempDir()
	}
	tools := catalog.Tools{
		RepakPath:     cfg.RepakPath,
		UAssetGUIPath: cfg.UAssetGUIPath,
		ScratchDir:    scratch,
		// Pipe per-pak progress into the same SSE stream the
		// extractor's job log uses, so /extractor's live log shows
		// what the rescan is chewing on. The UI handles
		// `catalog_log` events outside its per-route routing block.
		Logger: func(format string, args ...any) {
			h.broadcast(extractorEvent{
				Type:      "catalog_log",
				Message:   fmt.Sprintf(format, args...),
				Timestamp: time.Now().Unix(),
			})
		},
	}
	return catalog.ScanCatalog(h.db, tswPath, tools, force)
}

// Rescan rebuilds the pak_catalog table from scratch — empties it
// first, then walks the install dir and inserts fresh rows. Used after
// the user installs or updates a DLC, or whenever the scanner's
// behavior has changed and we want a clean slate. ?force=1 still
// honours diff scans against any rows already present (a no-op after
// the purge); leaving it as a flag in case the rebuild semantics need
// to be turned off later via a separate endpoint.
//
// Synchronous — first scan takes ~1–3 min; later rescans of an
// unchanged install are still slow because we always start from empty.
// Returns counts of paks added / updated / skipped / removed.
func (h *ExtractorHandler) Rescan(w http.ResponseWriter, r *http.Request) {
	cfg := config.Get()
	tswPath := strings.TrimSpace(cfg.ExtractorTswPath)
	if tswPath == "" {
		util.Error(w, http.StatusBadRequest, "extractorTswPath setting is empty")
		return
	}
	// Purge first so the next scan inserts every row fresh. Catches
	// stale rows whose underlying pak was renamed (mtime+size diff
	// would have re-scanned them, but the old row by old pak_path
	// would have been left behind).
	if _, err := h.db.Exec("DELETE FROM pak_catalog"); err != nil {
		util.Error(w, http.StatusInternalServerError, "purge catalog: "+err.Error())
		return
	}
	force := r.URL.Query().Get("force") == "1"
	res, err := h.runCatalogScan(tswPath, force)
	if err != nil {
		util.Error(w, http.StatusInternalServerError, err.Error())
		return
	}
	util.JSON(w, http.StatusOK, map[string]any{
		"added":   res.Added,
		"updated": res.Updated,
		"skipped": res.Skipped,
		"removed": res.Removed,
	})
}

// cachedDLCName memoises PakDLCDisplayName per pak path. Empty-string
// results are also cached — they signal "Description was junk, fall
// back to CamelCase" — so we don't keep re-running repak for the same
// broken paks on every ListRoutes call.
func (h *ExtractorHandler) cachedDLCName(pakPath, repakPath, scratchDir string) string {
	h.dlcNameMu.Lock()
	defer h.dlcNameMu.Unlock()
	if h.dlcNameCache == nil {
		h.dlcNameCache = map[string]string{}
	}
	if v, ok := h.dlcNameCache[pakPath]; ok {
		return v
	}
	name, err := pak.PakDLCDisplayName(pakPath, repakPath, scratchDir)
	if err != nil {
		log.Printf("[extractor] PakDLCDisplayName(%s): %v", pakPath, err)
	}
	h.dlcNameCache[pakPath] = name
	return name
}

// cachedRouteDefinition memoises PakRouteDefinition per pak path so the
// (slow) extract+convert+parse cycle runs at most once per pak per
// process. nil cache entries are kept so repeat lookups for cargo /
// wagon DLCs (which have no RouteDefinition) short-circuit without
// re-running repak.
func (h *ExtractorHandler) cachedRouteDefinition(pakPath, repakPath, uassetGUIPath, scratchDir string) *uasset.RouteDefinition {
	h.routeDefMu.Lock()
	defer h.routeDefMu.Unlock()
	if h.routeDefCache == nil {
		h.routeDefCache = map[string]*uasset.RouteDefinition{}
	}
	if v, ok := h.routeDefCache[pakPath]; ok {
		return v
	}
	rd, err := pak.PakRouteDefinition(pakPath, repakPath, uassetGUIPath, scratchDir)
	if err != nil {
		log.Printf("[extractor] PakRouteDefinition(%s): %v", pakPath, err)
		// Cache nil so we don't keep retrying a broken pak every
		// ListRoutes call.
		h.routeDefCache[pakPath] = nil
		return nil
	}
	h.routeDefCache[pakPath] = rd
	return rd
}

// resolveTargetRoutes returns the routes to process. When the request body
// asks for a specific subset, we filter the master list by codename;
// otherwise we run every route discoverable on disk that has tile maps
// (train-only DLCs are skipped — they don't have anything to extract).
func (h *ExtractorHandler) resolveTargetRoutes(tswPath string, requested []string) ([]routeListEntry, error) {
	all, err := h.listRoutes(tswPath)
	if err != nil {
		return nil, err
	}
	all = h.filterRoutesOnly(all)
	if len(requested) == 0 {
		return all, nil
	}
	want := map[string]bool{}
	for _, r := range requested {
		want[r] = true
	}
	out := make([]routeListEntry, 0, len(requested))
	for _, r := range all {
		if want[r.codename] {
			out = append(out, r)
		}
	}
	return out, nil
}

// filterRoutesOnly drops paks that don't ship any timetable / scenario
// assets (UI, audio, menu paks). Wagon packs and cargo DLCs without
// their own map tiles are kept — they have services to extract.
// Memoised per pakPath; first call may take ~30 s for ~36 paks.
//
// If repak isn't available for any reason, we fall through and return the
// full list — better to show a too-long list than break the page entirely.
func (h *ExtractorHandler) filterRoutesOnly(in []routeListEntry) []routeListEntry {
	cfg := extractor.Config{TSWPath: config.Get().ExtractorTswPath}
	if err := cfg.AutoDetect(); err != nil || cfg.RepakPath == "" {
		return in
	}
	out := make([]routeListEntry, 0, len(in))
	for _, rt := range in {
		if h.pakHasTimetable(rt.pakPath, cfg.RepakPath) {
			out = append(out, rt)
		}
	}
	return out
}

func (h *ExtractorHandler) pakHasTimetable(pakPath, repakPath string) bool {
	h.hasTimetableMu.Lock()
	defer h.hasTimetableMu.Unlock()
	if h.hasTimetableCache == nil {
		h.hasTimetableCache = map[string]bool{}
	}
	if v, ok := h.hasTimetableCache[pakPath]; ok {
		return v
	}
	ok, err := pak.PakHasTimetable(pakPath, repakPath)
	if err != nil {
		// On error, fall through positive — the user can still try to
		// extract; they'll just see "no timetable assets found" if it's
		// truly a content-free DLC. Better than silently hiding.
		log.Printf("[extractor] PakHasTimetable(%s): %v", pakPath, err)
		ok = true
	}
	h.hasTimetableCache[pakPath] = ok
	return ok
}

func errorOrCode(err error, code int) string {
	if err != nil {
		return err.Error()
	}
	if code != 0 {
		return fmt.Sprintf("exit %d", code)
	}
	return ""
}

// writeSSE serialises an event in SSE format. Returns false if the write
// or flush fails (client disconnected) so the caller can return.
func writeSSE(w http.ResponseWriter, rc *http.ResponseController, ev extractorEvent) bool {
	b, err := json.Marshal(ev)
	if err != nil {
		log.Printf("[extractor] marshal event: %v", err)
		return true
	}
	if _, err := fmt.Fprintf(w, "data: %s\n\n", b); err != nil {
		return false
	}
	return rc.Flush() == nil
}
