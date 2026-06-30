//! Embedded web server. Hosts the same HUD pages hud-go serves so users can
//! point a browser / Tauri / Electron at the HUDs. Phase 1: static pages +
//! /css /js /locales /images + custom HUDs. /stream + /api/hud/* come later.
//!
//! The server runs on its own thread with its own tokio runtime so it doesn't
//! interfere with the egui main loop. Start/Stop are driven from the Web HUD
//! tab; Drop on the Handle gracefully shuts the server down when the app exits.

use std::net::{IpAddr, Ipv4Addr, SocketAddr};
use std::path::PathBuf;
use std::sync::{Arc, RwLock};
use std::thread;
use std::time::Duration;

use axum::extract::{FromRef, Path, Query, State};
use axum::http::StatusCode;
use axum::response::sse::{Event, KeepAlive, Sse};
use axum::response::{IntoResponse, Response};
use axum::routing::{get, post};
use axum::{Json, Router};
use serde::{Deserialize, Serialize};
use serde_json::{json, Value};
use tokio::sync::oneshot;
use tokio_stream::wrappers::IntervalStream;
use tokio_stream::{Stream, StreamExt};
use tower_http::cors::CorsLayer;
use tower_http::services::ServeDir;

/// Latest TSW telemetry + connection status + rolling boot log, shared between
/// the TSW subscriber loop and /stream readers. Empty data means /stream falls
/// back to the "waiting for TSW" payload hud-go uses pre-connection.
#[derive(Default)]
pub struct TelemetryInner {
    pub data: Option<Value>,
    pub status: String,
    pub log: Vec<String>, // append-only boot log, capped at LOG_CAP
}

const LOG_CAP: usize = 50;

#[derive(Clone, Default)]
pub struct Telemetry(pub Arc<RwLock<TelemetryInner>>);

impl Telemetry {
    pub fn snapshot(&self) -> Option<Value> {
        self.0.read().ok().and_then(|g| g.data.clone())
    }
    pub fn status(&self) -> String {
        self.0.read().ok().map(|g| g.status.clone()).unwrap_or_default()
    }
    pub fn log(&self) -> Vec<String> {
        self.0.read().ok().map(|g| g.log.clone()).unwrap_or_default()
    }
    pub fn set_data(&self, d: Option<Value>) {
        if let Ok(mut g) = self.0.write() {
            g.data = d;
        }
    }
    /// Update status and append to the boot log iff the value changed — gives
    /// the UI a chronological trace ("Connecting…", "Subscribed to 48 paths"…)
    /// without duplicates while the same state persists.
    pub fn set_status(&self, s: impl Into<String>) {
        let s = s.into();
        if let Ok(mut g) = self.0.write() {
            if g.status != s {
                g.status = s.clone();
                g.log.push(s);
                if g.log.len() > LOG_CAP {
                    let drop = g.log.len() - LOG_CAP;
                    g.log.drain(0..drop);
                }
            }
        }
    }
    /// Push a line into the log without touching status — used for one-shot
    /// notes like "Server listening on http://…" emitted during start().
    pub fn push_log(&self, s: impl Into<String>) {
        if let Ok(mut g) = self.0.write() {
            g.log.push(s.into());
            if g.log.len() > LOG_CAP {
                let drop = g.log.len() - LOG_CAP;
                g.log.drain(0..drop);
            }
        }
    }
    /// Wipe state for a fresh Start press (so the previous run's log doesn't
    /// linger across restarts).
    pub fn reset(&self) {
        if let Ok(mut g) = self.0.write() {
            g.data = None;
            g.status.clear();
            g.log.clear();
        }
    }
}

/// Currently loaded route blob, file path, and timetable cursor — the in-memory
/// state behind /route-data, /api/upload-route, /api/timetable-items etc. Same
/// shape hud-go/internal/handler/hud.go keeps; we mutate it through HudState
/// rather than passing locks around in handler signatures.
#[derive(Default)]
pub struct HudStateInner {
    pub current_route: Option<Value>,
    pub route_file_path: String,
    pub timetable_index: i64,
}

#[derive(Clone)]
pub struct HudState(pub Arc<RwLock<HudStateInner>>);

impl Default for HudState {
    fn default() -> Self {
        Self(Arc::new(RwLock::new(HudStateInner {
            current_route: None,
            route_file_path: String::new(),
            timetable_index: -1,
        })))
    }
}

/// Root for file-browse + load-route. Same idea as hud-go's appDir —
/// restricts browse to a safe parent. Now scoped to hud's own crate root.
pub fn app_dir() -> PathBuf {
    PathBuf::from(env!("CARGO_MANIFEST_DIR"))
}

/// Bundled state passed to axum. FromRef lets each handler ask for just the
/// sub-state it needs (Telemetry or HudState) without manually plumbing both.
#[derive(Clone, Default)]
struct AppState {
    telemetry: Telemetry,
    hud: HudState,
}

impl FromRef<AppState> for Telemetry {
    fn from_ref(s: &AppState) -> Self { s.telemetry.clone() }
}
impl FromRef<AppState> for HudState {
    fn from_ref(s: &AppState) -> Self { s.hud.clone() }
}

/// Where the served HTML/CSS/JS/images live. Priority mirrors
/// `crate::config::resources_dir`: next to the exe in release, hud's own
/// Browser-served templates (HUDs, /map, /weather, /weather-presets, /data,
/// /record, asset roots) live under `resources/views/` now — same place as
/// every other piece of live data the app reads. Legacy hud-rust/views/
/// fallback removed; the unused admin templates (timetables/, formations/,
/// routes/, etc.) didn't come along with the copy.
///
///   1. `<exe parent>/resources/views/` — release layout next to hud.exe.
///   2. `<crate>/../resources/views/`   — dev layout (sibling to src-tauri/).
pub fn views_dir() -> PathBuf {
    crate::config::resources_dir().join("views")
}

/// User-editable HUDs (drop .html files here without rebuilding). Lives
/// inside whatever `resources_dir` resolves to.
pub fn custom_huds_dir() -> PathBuf {
    crate::config::resources_dir().join("custom_huds")
}

pub struct Handle {
    pub port: u16,
    shutdown: Option<oneshot::Sender<()>>,
    thread: Option<thread::JoinHandle<()>>,
}

impl Handle {
    /// Stop button: fire shutdown, briefly poll for the worker to finish so the
    /// listener drops before the user clicks Start again. If it hasn't exited
    /// within 500 ms, detach — the OS reaps the thread on process exit. Better
    /// than freezing the GUI on a stuck shutdown.
    pub fn stop(mut self) {
        if let Some(tx) = self.shutdown.take() {
            let _ = tx.send(());
        }
        if let Some(t) = self.thread.take() {
            let start = std::time::Instant::now();
            while !t.is_finished() && start.elapsed() < std::time::Duration::from_millis(500) {
                std::thread::sleep(std::time::Duration::from_millis(10));
            }
            if t.is_finished() {
                let _ = t.join();
            }
            // else: drop the JoinHandle, thread continues in background.
        }
    }
}

impl Drop for Handle {
    /// App-exit path: fire the shutdown signal and return immediately. Joining
    /// here would block the egui main thread; the runtime thread sees the
    /// signal and exits, and any laggards are reaped when the process exits.
    fn drop(&mut self) {
        if let Some(tx) = self.shutdown.take() {
            let _ = tx.send(());
        }
    }
}

/// Bind the listener up front so a port conflict is reported synchronously
/// instead of swallowed inside the worker thread.
pub fn start(
    port: u16,
    telemetry: Telemetry,
    hud: HudState,
    cfg: crate::config::Config,
) -> Result<Handle, String> {
    // Bind every interface (LAN included) so phones scanning the QR codes
    // can reach the server — same as hud-go. The Web HUD tab's "Click Here"
    // still uses 127.0.0.1 for local-machine clicks.
    // Fresh-press cleanup so the new boot log starts empty.
    telemetry.reset();
    telemetry.push_log(format!("Binding port {port}..."));
    let addr = SocketAddr::new(IpAddr::V4(Ipv4Addr::UNSPECIFIED), port);
    let std_listener = match std::net::TcpListener::bind(addr) {
        Ok(l) => l,
        Err(e) => {
            telemetry.push_log(format!("Bind failed: {e}"));
            return Err(format!("bind {addr}: {e}"));
        }
    };
    std_listener.set_nonblocking(true).map_err(|e| e.to_string())?;
    telemetry.push_log(format!("Listening on http://0.0.0.0:{port} (LAN reachable)"));

    let views = views_dir();
    if !views.exists() {
        telemetry.push_log(format!("Views dir missing: {}", views.display()));
        return Err(format!("views dir not found: {}", views.display()));
    }
    telemetry.push_log("Static assets ready (views/, /css, /js, /locales, /images)");
    telemetry.set_status(if cfg.enable_subscriptions {
        "Starting TSW subscriber…"
    } else {
        "TSW subscriptions disabled in Settings"
    });

    let (tx, rx) = oneshot::channel::<()>();
    let telemetry_for_tsw = telemetry.clone();
    let cfg_for_tsw = cfg.clone();
    let state = AppState { telemetry, hud };
    let join = thread::Builder::new()
        .name("hud-server".into())
        .spawn(move || {
            let rt = tokio::runtime::Builder::new_multi_thread()
                .enable_all()
                .build()
                .expect("tokio runtime");
            rt.block_on(async move {
                let listener = tokio::net::TcpListener::from_std(std_listener)
                    .expect("std listener -> tokio");
                let app = build_app(views, state);
                // No `with_graceful_shutdown`: SSE streams are long-lived requests
                // and graceful-shutdown would wait for every browser tab on /stream
                // to close, freezing Stop / app-exit. Dropping the runtime instead
                // cancels SSE tasks immediately; clients see connection-reset and
                // reconnect.
                let serve_fut = async {
                    let _ = axum::serve(listener, app).await;
                };
                if cfg_for_tsw.enable_subscriptions {
                    let tsw_fut = crate::tsw::connection_loop(telemetry_for_tsw, cfg_for_tsw);
                    tokio::select! {
                        _ = serve_fut => {},
                        _ = tsw_fut => {},
                        _ = rx => {},
                    }
                } else {
                    tokio::select! {
                        _ = serve_fut => {},
                        _ = rx => {},
                    }
                }
            });
        })
        .map_err(|e| e.to_string())?;

    Ok(Handle { port, shutdown: Some(tx), thread: Some(join) })
}

fn build_app(views: PathBuf, state: AppState) -> Router {
    let huds = views.join("huds");

    Router::new()
        // ── Browser-facing routes ────────────────────────────────────────────
        // Only the surfaces a phone / second-screen actually needs:
        //   * the HUDs themselves (desktop / tablet / mobile / experiment),
        //   * the live map + weather + data + record pages,
        //   * the weather-presets editor,
        //   * served custom-HUD content + the AI guide.
        // Everything else from hud-go (admin: routes / timetables / formations
        // / countries / locations / settings / extractor / api-subscriptions /
        // recording-settings / train-classes / custom-hud listing / start
        // landing) is intentionally *not* served — those live in the native
        // Tauri shell. A request for any of those paths gets a 404 (or hits
        // the /api/* routes below if it's an API path the HUDs share).
        //
        // Root and /start used to serve the desktop QR landing; the Tauri
        // shell's Web HUD tab now owns that role. We point / at the slim
        // mobile picker so a phone scanning the QR still lands on a useful
        // page when typed without a path.
        .route("/", get(serve_file(huds.join("start-mobile.html"))))
        .route("/start-mobile", get(serve_file(huds.join("start-mobile.html"))))
        .route("/find-timetable", get(serve_file(huds.join("start-mobile.html"))))
        .route("/desktop",   get(serve_file(huds.join("desktop.html"))))
        .route("/tablet",    get(serve_file(huds.join("tablet.html"))))
        .route("/mobile",    get(serve_file(huds.join("mobile.html"))))
        .route("/experiment",get(serve_file(huds.join("experiment.html"))))
        // Top-level browser pages (map / weather / data / record).
        .route("/map",     get(serve_file(views.join("map.html"))))
        .route("/weather", get(serve_file(views.join("weather.html"))))
        .route("/data",    get(serve_file(views.join("data.html"))))
        .route("/record",  get(serve_file(views.join("record-map.html"))))
        // Weather presets editor — part of the weather surface the user
        // expects in the browser.
        .route("/weather-presets",     get(serve_file(views.join("weather-presets").join("index.html"))))
        .route("/weather-presets/:id", get(serve_file(views.join("weather-presets").join("show.html"))))
        // Custom HUDs: only the served content + the docs. The list/admin
        // tab moved to the Tauri shell (src/custom-huds.html).
        .route("/api/custom-huds",      get(list_custom_huds))   // used internally by HUDs that enumerate
        .route("/custom-huds/ai-guide", get(serve_custom_guide))
        .route("/custom-huds/:name",    get(serve_custom_hud))
        // Collections are intentionally NOT exposed via the server — they run
        // standalone from the file system (see collections::launch_file_url),
        // so they work even when the user has no LAN access and the server
        // isn't running.
        // Live telemetry SSE.
        .route("/stream", get(stream_telemetry))
        // HUD route + timetable state (port of hud-go/internal/handler/hud.go).
        .route("/route-data", get(get_current_route))
        .route("/api/hud/browse", get(browse))
        .route("/api/hud/load-route", get(load_route))
        .route("/api/upload-route", post(upload_route))
        .route("/api/clear-route", post(clear_route))
        .route("/api/timetable-items", get(get_timetable_items))
        .route("/api/set-timetable-index", post(set_timetable_index))
        .route("/api/update-timetable-coordinates", post(update_timetable_coordinates))
        // HUD auto-detect from currentServiceName (+ optional player position
        // to disambiguate reused headcodes like "1S37" across routes).
        .route("/api/timetables/detect", get(detect_timetable))
        // Bundle the timetable's full route/coords/markers blob for the HUD
        // to POST back to /api/upload-route.
        .route("/api/map/route-data/:id", get(map_route_data))
        // Map layers: pre-built per-timetable feature blob (rails/signals/
        // switches/stations) — primary source for the HUD map.
        .route("/api/timetables/:id/map-features", get(timetable_map_features))
        // Synchronous builder for the "Generate Map" button. Returns the
        // freshly-built blob so the page doesn't need a second fetch.
        .route("/api/timetables/:id/build-map-features", post(build_map_features_for_timetable))
        // Fallback per-route GeoJSON when no per-timetable blob exists.
        .route("/api/routes/:id/map-data", get(route_map_data))
        // Config (HUD JS calls this at boot — without it loadConfig() throws
        // and a bunch of HUD init paths bail before rendering the schedule
        // and map-layer toggles).
        .route("/api/config", get(get_config))
        .route("/api/config/server-urls", get(get_server_urls))
        // /map picker endpoints — Route → Formation → Timetable typeahead.
        .route("/api/routes/with-coordinates", get(routes_with_coordinates))
        .route("/api/routes/:id/formations-with-coordinates", get(formations_with_coordinates))
        .route("/api/timetables", get(timetables_for_picker))
        .route("/api/timetables/:id", get(timetable_by_id))
        // Weather presets — full CRUD; same shape hud-go exposes.
        .route("/api/weather-presets", get(weather_presets_list).post(weather_preset_create))
        .route("/api/weather-presets/:id", get(weather_preset_get).put(weather_preset_update).delete(weather_preset_delete))
        // Live weather control — PATCH straight through to TSW CommAPI.
        .route("/api/weather/set", axum::routing::patch(weather_set))
        // Real-world weather via Open-Meteo (live + historical archive).
        .route("/api/weather/live", get(weather_live_fetch))
        .route("/api/weather/live/apply", post(weather_live_apply))
        .route("/api/weather/historical", get(weather_historical_fetch))
        .route("/api/weather/historical/apply", post(weather_historical_apply))
        // favicon: 204 so the browser stops asking.
        .route("/favicon.ico", get(|| async { StatusCode::NO_CONTENT }))
        // Static asset roots (same layout hud-go uses).
        .nest_service("/css", ServeDir::new(views.join("css")))
        .nest_service("/js", ServeDir::new(views.join("js")))
        .nest_service("/locales", ServeDir::new(views.join("locales")))
        .nest_service("/images", ServeDir::new(views.join("images")))
        .layer(CorsLayer::permissive())
        .with_state(state)
}

async fn stream_telemetry(
    State(telemetry): State<Telemetry>,
) -> Sse<impl Stream<Item = Result<Event, std::convert::Infallible>>> {
    // 100ms cadence matches hud-go/internal/handler/stream.go.
    let ticker = tokio::time::interval(Duration::from_millis(100));
    let stream = IntervalStream::new(ticker).map(move |_| {
        let payload = telemetry.snapshot().unwrap_or_else(|| {
            json!({
                "waiting": true,
                "message": "Waiting for TSW connection..."
            })
        });
        Ok(Event::default().data(payload.to_string()))
    });
    Sse::new(stream).keep_alive(KeepAlive::default())
}

fn serve_file(path: PathBuf) -> impl Fn() -> std::pin::Pin<Box<dyn std::future::Future<Output = Response> + Send>> + Clone + Send + 'static {
    move || {
        let path = path.clone();
        Box::pin(async move {
            match tokio::fs::read(&path).await {
                Ok(bytes) => ([("Content-Type", "text/html; charset=utf-8")], bytes).into_response(),
                Err(e) => (StatusCode::NOT_FOUND, format!("{}: {}", path.display(), e)).into_response(),
            }
        })
    }
}

#[derive(Serialize)]
struct CustomHud {
    name: String,
    title: String,
}

async fn list_custom_huds() -> Json<Vec<CustomHud>> {
    let dir = custom_huds_dir();
    let mut out = Vec::new();
    let Ok(mut rd) = tokio::fs::read_dir(&dir).await else {
        return Json(out);
    };
    while let Ok(Some(entry)) = rd.next_entry().await {
        let Ok(ft) = entry.file_type().await else { continue };
        if ft.is_dir() {
            continue;
        }
        let name = entry.file_name().to_string_lossy().to_string();
        let Some(stem) = name.strip_suffix(".html").or_else(|| name.strip_suffix(".HTML")) else {
            continue;
        };
        if !is_safe_hud_name(stem) {
            continue;
        }
        out.push(CustomHud { name: stem.to_string(), title: humanize(stem) });
    }
    Json(out)
}

async fn serve_custom_hud(Path(name): Path<String>) -> Response {
    if !is_safe_hud_name(&name) {
        return (StatusCode::BAD_REQUEST, "invalid HUD name").into_response();
    }
    let path = custom_huds_dir().join(format!("{name}.html"));
    match tokio::fs::read(&path).await {
        Ok(bytes) => ([("Content-Type", "text/html; charset=utf-8")], bytes).into_response(),
        Err(_) => (StatusCode::NOT_FOUND, "custom HUD not found").into_response(),
    }
}

async fn serve_custom_guide() -> Response {
    let path = custom_huds_dir().join("AI_GUIDE.md");
    match tokio::fs::read(&path).await {
        Ok(bytes) => ([("Content-Type", "text/plain; charset=utf-8")], bytes).into_response(),
        Err(_) => (StatusCode::NOT_FOUND, "AI guide not found").into_response(),
    }
}

// Letters, digits, dash, underscore only — same rule hud-go uses, blocks path traversal.
fn is_safe_hud_name(s: &str) -> bool {
    !s.is_empty() && s.chars().all(|c| c.is_ascii_alphanumeric() || c == '-' || c == '_')
}

// ---------------------------------- HUD route / timetable endpoints

fn err(status: StatusCode, msg: impl Into<String>) -> Response {
    (status, Json(json!({"error": msg.into()}))).into_response()
}

async fn get_current_route(State(hud): State<HudState>) -> Response {
    let inner = match hud.0.read() {
        Ok(g) => g,
        Err(_) => return err(StatusCode::INTERNAL_SERVER_ERROR, "lock poisoned"),
    };
    let Some(route) = inner.current_route.as_ref() else {
        return Json(json!({})).into_response();
    };
    let mut result = match route.as_object() {
        Some(o) => o.clone(),
        None => return Json(route.clone()).into_response(),
    };
    // hud-go appends timetableStations + currentRouteFile.
    if let Some(timetable) = route.get("timetable").and_then(|v| v.as_array()) {
        let stations: Vec<Value> = timetable
            .iter()
            .filter_map(|e| e.as_object().and_then(|m| m.get("station").cloned()))
            .collect();
        result.insert("timetableStations".into(), Value::Array(stations));
    }
    let route_file = if inner.route_file_path.is_empty() {
        "unknown".to_string()
    } else {
        std::path::Path::new(&inner.route_file_path)
            .file_name()
            .map(|s| s.to_string_lossy().to_string())
            .unwrap_or_else(|| "unknown".into())
    };
    result.insert("currentRouteFile".into(), json!(route_file));
    Json(Value::Object(result)).into_response()
}

#[derive(Deserialize)]
struct BrowseQuery {
    #[serde(default)]
    dir: String,
}

#[derive(Serialize)]
struct BrowseItem {
    name: String,
    path: String,
    #[serde(rename = "isDirectory")]
    is_directory: bool,
    #[serde(rename = "isRoute")]
    is_route: bool,
}

async fn browse(Query(q): Query<BrowseQuery>) -> Response {
    let base = app_dir();
    let browse_path = if q.dir.is_empty() {
        base.clone()
    } else {
        base.join(&q.dir)
    };

    // Security: stay within base.
    let abs_base = std::fs::canonicalize(&base).unwrap_or(base.clone());
    let abs_browse = match std::fs::canonicalize(&browse_path) {
        Ok(p) => p,
        Err(e) if e.kind() == std::io::ErrorKind::NotFound => {
            return err(StatusCode::NOT_FOUND, "Path not found");
        }
        Err(e) => return err(StatusCode::INTERNAL_SERVER_ERROR, e.to_string()),
    };
    if !abs_browse.starts_with(&abs_base) {
        return err(StatusCode::FORBIDDEN, "Access denied");
    }

    let meta = match tokio::fs::metadata(&abs_browse).await {
        Ok(m) => m,
        Err(_) => return err(StatusCode::NOT_FOUND, "Path not found"),
    };
    if !meta.is_dir() {
        return err(StatusCode::BAD_REQUEST, "Path is not a directory");
    }

    let mut rd = match tokio::fs::read_dir(&abs_browse).await {
        Ok(r) => r,
        Err(e) => return err(StatusCode::INTERNAL_SERVER_ERROR, e.to_string()),
    };
    let mut items: Vec<BrowseItem> = Vec::new();
    while let Ok(Some(entry)) = rd.next_entry().await {
        let name = entry.file_name().to_string_lossy().to_string();
        let full = entry.path();
        let rel = full.strip_prefix(&abs_base).unwrap_or(&full);
        let rel_str = rel.to_string_lossy().replace('\\', "/");
        let is_directory = entry.file_type().await.map(|t| t.is_dir()).unwrap_or(false);
        let is_route = !is_directory && name.ends_with(".json") && name.starts_with("route_");
        items.push(BrowseItem { name, path: rel_str, is_directory, is_route });
    }
    // Directories first, then alphabetical.
    items.sort_by(|a, b| match b.is_directory.cmp(&a.is_directory) {
        std::cmp::Ordering::Equal => a.name.to_lowercase().cmp(&b.name.to_lowercase()),
        ord => ord,
    });

    let current_path = if q.dir.is_empty() { ".".into() } else { q.dir.replace('\\', "/") };
    let parent_path = if q.dir.is_empty() {
        Value::Null
    } else {
        let p = std::path::Path::new(&q.dir)
            .parent()
            .map(|p| p.to_string_lossy().replace('\\', "/"))
            .unwrap_or_default();
        if p.is_empty() || p == "." { Value::Null } else { Value::String(p) }
    };

    Json(json!({
        "currentPath": current_path,
        "parentPath": parent_path,
        "items": items,
    }))
    .into_response()
}

#[derive(Deserialize)]
struct LoadRouteQuery {
    #[serde(default)]
    file: String,
}

async fn load_route(State(hud): State<HudState>, Query(q): Query<LoadRouteQuery>) -> Response {
    if q.file.is_empty() {
        return err(StatusCode::BAD_REQUEST, "Missing path parameter");
    }
    let base = app_dir();
    let full = base.join(&q.file);
    let abs_base = std::fs::canonicalize(&base).unwrap_or(base.clone());
    let abs_full = match std::fs::canonicalize(&full) {
        Ok(p) => p,
        Err(e) if e.kind() == std::io::ErrorKind::NotFound => {
            return err(StatusCode::NOT_FOUND, "Route file not found");
        }
        Err(e) => return err(StatusCode::INTERNAL_SERVER_ERROR, e.to_string()),
    };
    if !abs_full.starts_with(&abs_base) {
        return err(StatusCode::FORBIDDEN, "Access denied");
    }
    let bytes = match tokio::fs::read(&abs_full).await {
        Ok(b) => b,
        Err(e) => return err(StatusCode::INTERNAL_SERVER_ERROR, e.to_string()),
    };
    let route: Value = match serde_json::from_slice(&bytes) {
        Ok(v) => v,
        Err(e) => return err(StatusCode::INTERNAL_SERVER_ERROR, format!("Invalid JSON: {e}")),
    };
    let route_name = route.get("routeName").cloned().unwrap_or(Value::Null);
    let total_points = route.get("totalPoints").cloned().unwrap_or(Value::Null);
    let total_markers = route.get("totalMarkers").cloned().unwrap_or(Value::Null);
    if let Ok(mut g) = hud.0.write() {
        g.current_route = Some(route);
        g.route_file_path = abs_full.to_string_lossy().into_owned();
        g.timetable_index = -1;
    }
    let base_name = abs_full
        .file_name()
        .map(|s| s.to_string_lossy().into_owned())
        .unwrap_or_default();
    Json(json!({
        "success": true,
        "message": format!("Loaded {base_name}"),
        "routeName": route_name,
        "totalPoints": total_points,
        "totalMarkers": total_markers,
    }))
    .into_response()
}

#[derive(Deserialize)]
struct UploadRouteBody {
    #[serde(default)]
    filename: String,
    #[serde(rename = "routeData")]
    route_data: Option<Value>,
}

async fn upload_route(State(hud): State<HudState>, Json(body): Json<UploadRouteBody>) -> Response {
    let Some(route) = body.route_data else {
        return err(StatusCode::BAD_REQUEST, "Invalid route data");
    };
    if route.get("coordinates").is_none() || route.get("routeName").is_none() {
        return err(StatusCode::BAD_REQUEST, "Invalid route data");
    }
    let route_name = route.get("routeName").cloned().unwrap_or(Value::Null);
    let total_points = route.get("totalPoints").cloned().unwrap_or(Value::Null);
    let total_markers = route.get("totalMarkers").cloned().unwrap_or(Value::Null);
    // Pull timetableId out before we move `route` into hud state.
    let timetable_id = route
        .get("timetableId")
        .and_then(|v| v.as_i64().or_else(|| v.as_f64().map(|f| f as i64)));
    if let Ok(mut g) = hud.0.write() {
        g.current_route = Some(route);
        g.route_file_path = body.filename.clone();
        g.timetable_index = -1;
    }
    // Background prime of the per-timetable map-features blob. Skips quickly
    // when the row already exists. Mirrors hud-go's ensureMapFeaturesAsync —
    // player service-selection silently warms the data the HUD's next map
    // open will want, so /api/timetables/{id}/map-features serves a single
    // SELECT instead of the per-feature proximity filter every time.
    if let Some(tid) = timetable_id {
        if tid > 0 {
            tokio::task::spawn_blocking(move || {
                match crate::db::timetable_map_features_exists(tid) {
                    Ok(true) => {}
                    Ok(false) => {
                        if let Err(e) = crate::db::build_timetable_map_features(tid) {
                            eprintln!("[map-features-async] tt={tid}: {e}");
                        }
                    }
                    Err(e) => eprintln!("[map-features-async] tt={tid} exists check: {e}"),
                }
            });
        }
    }
    Json(json!({
        "success": true,
        "message": format!("Loaded {}", body.filename),
        "routeName": route_name,
        "totalPoints": total_points,
        "totalMarkers": total_markers,
    }))
    .into_response()
}

async fn clear_route(State(hud): State<HudState>) -> Response {
    if let Ok(mut g) = hud.0.write() {
        g.current_route = None;
        g.route_file_path.clear();
        g.timetable_index = -1;
    }
    Json(json!({"success": true, "message": "Route cleared"})).into_response()
}

async fn get_timetable_items(State(hud): State<HudState>) -> Response {
    let mut g = match hud.0.write() {
        Ok(g) => g,
        Err(_) => return err(StatusCode::INTERNAL_SERVER_ERROR, "lock poisoned"),
    };
    let Some(route) = g.current_route.as_mut() else {
        return Json(json!({"items": [], "currentIndex": g.timetable_index})).into_response();
    };
    let Some(route_obj) = route.as_object_mut() else {
        return Json(json!({"items": [], "currentIndex": g.timetable_index})).into_response();
    };

    let vehicle_count = route_obj.get("vehicleCount").cloned().unwrap_or(json!(0));

    // First WAIT FOR SERVICE row → snap to spawn vertex (first coordinate).
    // Same justification hud-go has: the head-of-train stop sign is one
    // train-length ahead of the actual spawn position; first coordinates[]
    // vertex IS the spawn.
    let spawn = route_obj
        .get("coordinates")
        .and_then(|v| v.as_array())
        .and_then(|arr| arr.first())
        .and_then(|v| v.as_object())
        .and_then(|o| {
            let lat = o.get("latitude").and_then(|v| v.as_f64())?;
            let lng = o.get("longitude").and_then(|v| v.as_f64())?;
            Some((lat, lng))
        });

    if let (Some((lat, lng)), Some(tt)) = (spawn, route_obj.get_mut("timetable").and_then(|v| v.as_array_mut())) {
        for entry in tt.iter_mut() {
            let Some(m) = entry.as_object_mut() else { continue };
            if m.get("action").and_then(|v| v.as_str()) == Some("WAIT FOR SERVICE") {
                m.insert("latitude".into(), json!(lat));
                m.insert("longitude".into(), json!(lng));
                break;
            }
        }
    }

    let items = route_obj.get("timetable").cloned().unwrap_or(json!([]));
    Json(json!({
        "items": items,
        "currentIndex": g.timetable_index,
        "vehicleCount": vehicle_count,
    }))
    .into_response()
}

#[derive(Deserialize)]
struct SetIndexBody {
    index: i64,
}

async fn set_timetable_index(State(hud): State<HudState>, Json(body): Json<SetIndexBody>) -> Response {
    let mut g = match hud.0.write() {
        Ok(g) => g,
        Err(_) => return err(StatusCode::INTERNAL_SERVER_ERROR, "lock poisoned"),
    };
    let max_index = g
        .current_route
        .as_ref()
        .and_then(|r| r.get("timetable"))
        .and_then(|v| v.as_array())
        .map(|a| a.len() as i64)
        .unwrap_or(0);
    if body.index < -1 || body.index >= max_index {
        return err(StatusCode::BAD_REQUEST, "Index out of range");
    }
    g.timetable_index = body.index;
    Json(json!({"success": true})).into_response()
}

#[derive(Deserialize)]
struct UpdateCoordsBody {
    #[serde(default, rename = "entryId")]
    entry_id: Option<Value>,
    #[serde(default)]
    index: Option<i64>,
    latitude: f64,
    longitude: f64,
    #[serde(default)]
    x: Option<i64>,
    #[serde(default)]
    y: Option<i64>,
}

async fn update_timetable_coordinates(
    State(hud): State<HudState>,
    Json(body): Json<UpdateCoordsBody>,
) -> Response {
    // Hold the write guard inside its own scope so it can't possibly span the
    // .await below. Returning early from the scope still drops the guard.
    let entry_id_for_db: Option<i64> = {
        let mut g = match hud.0.write() {
            Ok(g) => g,
            Err(_) => return err(StatusCode::INTERNAL_SERVER_ERROR, "lock poisoned"),
        };
        let Some(route) = g.current_route.as_mut() else {
            return err(StatusCode::BAD_REQUEST, "No route loaded");
        };
        let Some(tt) = route.get_mut("timetable").and_then(|v| v.as_array_mut()) else {
            return err(StatusCode::BAD_REQUEST, "No timetable in route data");
        };

        let apply = |m: &mut serde_json::Map<String, Value>| {
            m.insert("latitude".into(), json!(body.latitude));
            m.insert("longitude".into(), json!(body.longitude));
            if let Some(x) = body.x {
                m.insert("x".into(), json!(x));
            }
            if let Some(y) = body.y {
                m.insert("y".into(), json!(y));
            }
        };

        let mut found = false;
        if let Some(eid) = &body.entry_id {
            for entry in tt.iter_mut() {
                if let Some(m) = entry.as_object_mut() {
                    if m.get("id") == Some(eid) {
                        apply(m);
                        found = true;
                        break;
                    }
                }
            }
        } else if let Some(idx) = body.index {
            if idx >= 0 && (idx as usize) < tt.len() {
                if let Some(m) = tt[idx as usize].as_object_mut() {
                    apply(m);
                    found = true;
                }
            }
        }

        if !found {
            return err(StatusCode::NOT_FOUND, "Timetable entry not found");
        }
        body.entry_id
            .as_ref()
            .and_then(|v| v.as_i64().or_else(|| v.as_f64().map(|f| f as i64)))
    };

    // Persist to timetable_entries (coord_source='manual'). Same write hud-go
    // does so the edit survives a clear+reload. spawn_blocking keeps the
    // sqlite call off the async runtime.
    if let Some(eid) = entry_id_for_db {
        if eid > 0 {
            let lat = body.latitude;
            let lng = body.longitude;
            let x = body.x;
            let y = body.y;
            let res = tokio::task::spawn_blocking(move || crate::db::update_entry_coords(eid, lat, lng, x, y)).await;
            if let Ok(Err(e)) = res {
                eprintln!("[HUD] save coord failed: {e}");
            }
        }
    }
    Json(json!({"success": true})).into_response()
}

// ---------------------------- /api/timetables/detect

#[derive(Deserialize)]
struct DetectQuery {
    #[serde(default)]
    current_service_name: String,
    #[serde(default)]
    lat: Option<f64>,
    #[serde(default)]
    lng: Option<f64>,
}

async fn detect_timetable(Query(q): Query<DetectQuery>) -> Response {
    if q.current_service_name.is_empty() {
        return err(StatusCode::BAD_REQUEST, "Provide ?current_service_name= query parameter");
    }
    let service = q.current_service_name.clone();
    let candidates = match tokio::task::spawn_blocking(move || crate::db::detect_candidates(&service)).await {
        Ok(Ok(v)) => v,
        Ok(Err(e)) => return err(StatusCode::INTERNAL_SERVER_ERROR, e),
        Err(e) => return err(StatusCode::INTERNAL_SERVER_ERROR, e.to_string()),
    };
    if candidates.is_empty() {
        return Json(json!({"found": false, "current_service_name": q.current_service_name})).into_response();
    }

    let has_pos = q.lat.is_some() && q.lng.is_some();
    let (player_lat, player_lng) = (q.lat.unwrap_or(0.0), q.lng.unwrap_or(0.0));

    // Snug-fit by distance when we have player position; else first row wins
    // (insertion order = the legacy LIMIT 1 fallback).
    let (best_idx, best_dist) = candidates
        .iter()
        .enumerate()
        .map(|(i, c)| {
            let d = if has_pos {
                crate::db::parse_first_coord(&c.first_coord_prefix)
                    .map(|(lat, lng)| crate::db::haversine_m(player_lat, player_lng, lat, lng))
                    .unwrap_or(f64::INFINITY)
            } else {
                f64::INFINITY
            };
            (i, d)
        })
        .min_by(|a, b| a.1.partial_cmp(&b.1).unwrap_or(std::cmp::Ordering::Equal))
        .unwrap_or((0, f64::INFINITY));
    let best = &candidates[best_idx];

    let mut out = json!({
        "found": true,
        "timetable_id": best.id,
        "service_name": best.service_name,
        "current_service_name": best.current_service_name,
        "route_id": best.route_id,
        "route_name": best.route_name,
        "candidate_count": candidates.len(),
    });
    if has_pos && best_dist.is_finite() {
        out.as_object_mut().unwrap().insert("match_distance_m".into(), json!(best_dist.round() as i64));
    }
    Json(out).into_response()
}

// ---------------------------- /api/config (HUD boot dependency)
async fn get_config() -> Response {
    // Re-read each request: the user edits the native Settings tab in egui,
    // not via the web UI, so the server doesn't hold a live Config handle.
    // Config::load is a small JSON read.
    let cfg = crate::config::Config::load();
    Json(serde_json::to_value(&cfg).unwrap_or(serde_json::json!({}))).into_response()
}

async fn get_server_urls() -> Response {
    Json(serde_json::json!({
        "local": "http://127.0.0.1:3000",
        "network": "http://127.0.0.1:3000"
    })).into_response()
}

// ---------------------------- /map picker endpoints

#[derive(Deserialize)]
struct PickerTimetablesQuery {
    #[serde(default)]
    route_id: Option<i64>,
    #[serde(default)]
    formation_id: Option<i64>,
}

async fn routes_with_coordinates() -> Response {
    match tokio::task::spawn_blocking(crate::db::routes_with_coordinates).await {
        Ok(Ok(v)) => Json(v).into_response(),
        Ok(Err(e)) => err(StatusCode::INTERNAL_SERVER_ERROR, e),
        Err(e) => err(StatusCode::INTERNAL_SERVER_ERROR, e.to_string()),
    }
}

async fn formations_with_coordinates(Path(id): Path<i64>) -> Response {
    match tokio::task::spawn_blocking(move || crate::db::formations_with_coordinates(id)).await {
        Ok(Ok(v)) => Json(v).into_response(),
        Ok(Err(e)) => err(StatusCode::INTERNAL_SERVER_ERROR, e),
        Err(e) => err(StatusCode::INTERNAL_SERVER_ERROR, e.to_string()),
    }
}

async fn timetables_for_picker(Query(q): Query<PickerTimetablesQuery>) -> Response {
    let r = q.route_id;
    let f = q.formation_id;
    match tokio::task::spawn_blocking(move || crate::db::timetables_for_picker(r, f)).await {
        Ok(Ok(v)) => Json(v).into_response(),
        Ok(Err(e)) => err(StatusCode::INTERNAL_SERVER_ERROR, e),
        Err(e) => err(StatusCode::INTERNAL_SERVER_ERROR, e.to_string()),
    }
}

async fn timetable_by_id(Path(id): Path<i64>) -> Response {
    match tokio::task::spawn_blocking(move || crate::db::timetable_by_id(id)).await {
        Ok(Ok(Some(v))) => Json(v).into_response(),
        Ok(Ok(None)) => err(StatusCode::NOT_FOUND, format!("Timetable {id} not found")),
        Ok(Err(e)) => err(StatusCode::INTERNAL_SERVER_ERROR, e),
        Err(e) => err(StatusCode::INTERNAL_SERVER_ERROR, e.to_string()),
    }
}

// ---------------------------- /api/weather-presets CRUD

#[derive(Deserialize)]
struct PresetBody {
    name: String,
    #[serde(default)] temperature: f64,
    #[serde(default)] cloudiness: f64,
    #[serde(default)] precipitation: f64,
    #[serde(default)] wetness: f64,
    #[serde(default)] ground_snow: f64,
    #[serde(default)] piled_snow: f64,
    #[serde(default)] fog_density: f64,
}

async fn weather_presets_list() -> Response {
    match tokio::task::spawn_blocking(crate::db::weather_presets_list).await {
        Ok(Ok(v)) => Json(v).into_response(),
        Ok(Err(e)) => err(StatusCode::INTERNAL_SERVER_ERROR, e),
        Err(e) => err(StatusCode::INTERNAL_SERVER_ERROR, e.to_string()),
    }
}

async fn weather_preset_get(Path(id): Path<i64>) -> Response {
    match tokio::task::spawn_blocking(move || crate::db::weather_preset_get(id)).await {
        Ok(Ok(Some(v))) => Json(v).into_response(),
        Ok(Ok(None)) => err(StatusCode::NOT_FOUND, "not found"),
        Ok(Err(e)) => err(StatusCode::INTERNAL_SERVER_ERROR, e),
        Err(e) => err(StatusCode::INTERNAL_SERVER_ERROR, e.to_string()),
    }
}

async fn weather_preset_create(Json(b): Json<PresetBody>) -> Response {
    let res = tokio::task::spawn_blocking(move || {
        crate::db::weather_preset_create(
            &b.name, b.temperature, b.cloudiness, b.precipitation,
            b.wetness, b.ground_snow, b.piled_snow, b.fog_density,
        )
    }).await;
    match res {
        Ok(Ok(id)) => (StatusCode::CREATED, Json(json!({"id": id}))).into_response(),
        Ok(Err(e)) => err(StatusCode::INTERNAL_SERVER_ERROR, e),
        Err(e) => err(StatusCode::INTERNAL_SERVER_ERROR, e.to_string()),
    }
}

async fn weather_preset_update(Path(id): Path<i64>, Json(b): Json<PresetBody>) -> Response {
    let res = tokio::task::spawn_blocking(move || {
        crate::db::weather_preset_update(
            id, &b.name, b.temperature, b.cloudiness, b.precipitation,
            b.wetness, b.ground_snow, b.piled_snow, b.fog_density,
        )
    }).await;
    match res {
        Ok(Ok(())) => Json(json!({"status": "updated"})).into_response(),
        Ok(Err(e)) => err(StatusCode::INTERNAL_SERVER_ERROR, e),
        Err(e) => err(StatusCode::INTERNAL_SERVER_ERROR, e.to_string()),
    }
}

async fn weather_preset_delete(Path(id): Path<i64>) -> Response {
    match tokio::task::spawn_blocking(move || crate::db::weather_preset_delete(id)).await {
        Ok(Ok(())) => Json(json!({"status": "deleted"})).into_response(),
        Ok(Err(e)) => err(StatusCode::INTERNAL_SERVER_ERROR, e),
        Err(e) => err(StatusCode::INTERNAL_SERVER_ERROR, e.to_string()),
    }
}

// ---------------------------- /api/weather/set
// Whitelist + dispatch to TSW: PATCH /set/WeatherManager.{Key}?value=N

const VALID_WEATHER_KEYS: &[&str] = &[
    "temperature", "cloudiness", "precipitation", "wetness",
    "groundsnow", "piledsnow", "fogdensity", "reset",
];

#[derive(Deserialize)]
struct WeatherSetQuery {
    #[serde(default)] key: String,
    #[serde(default)] value: String,
}

async fn weather_set(Query(q): Query<WeatherSetQuery>) -> Response {
    if q.key.is_empty() {
        return err(StatusCode::BAD_REQUEST, "key is required");
    }
    if q.value.is_empty() {
        return err(StatusCode::BAD_REQUEST, "value is required");
    }
    if !VALID_WEATHER_KEYS.contains(&q.key.to_lowercase().as_str()) {
        return err(StatusCode::BAD_REQUEST, format!("unknown weather key: {}", q.key));
    }
    // Resolve key fresh in case the user just rotated CommAPIKey.txt.
    let cfg = crate::config::Config::load();
    let api_key = crate::tsw::resolve_api_key_pub(&cfg);
    if api_key.is_empty() {
        return err(StatusCode::INTERNAL_SERVER_ERROR, "no TSW API key available");
    }
    let path = format!("/set/WeatherManager.{}?value={}", q.key, q.value);
    match crate::tsw::do_request("PATCH", &path, None, &api_key).await {
        Ok(_) => Json(json!({"success": true, "key": q.key, "value": q.value})).into_response(),
        Err(e) => err(StatusCode::INTERNAL_SERVER_ERROR, e),
    }
}

// ---------------------------- /api/weather/live + /api/weather/historical

#[derive(Deserialize)]
struct HistoricalQuery {
    #[serde(default)]
    date: String,
}

fn player_pos_from_telemetry(t: &Telemetry) -> Result<(f64, f64), String> {
    let snap = t.snapshot().ok_or("no player position available — is the game running?")?;
    crate::weather::player_position(&snap)
}

async fn weather_live_fetch(State(t): State<Telemetry>) -> Response {
    let (lat, lng) = match player_pos_from_telemetry(&t) {
        Ok(p) => p,
        Err(e) => return err(StatusCode::BAD_REQUEST, e),
    };
    let resp = match crate::weather::fetch_live(lat, lng).await {
        Ok(r) => r,
        Err(e) => return err(StatusCode::BAD_GATEWAY, format!("failed to fetch weather: {e}")),
    };
    let mapped = crate::weather::map_live_to_tsw(&resp);
    Json(json!({
        "latitude": lat,
        "longitude": lng,
        "weather": mapped,
        "source": "open-meteo",
    }))
    .into_response()
}

async fn weather_live_apply(State(t): State<Telemetry>) -> Response {
    let (lat, lng) = match player_pos_from_telemetry(&t) {
        Ok(p) => p,
        Err(e) => return err(StatusCode::BAD_REQUEST, e),
    };
    let resp = match crate::weather::fetch_live(lat, lng).await {
        Ok(r) => r,
        Err(e) => return err(StatusCode::BAD_GATEWAY, format!("failed to fetch weather: {e}")),
    };
    let mapped = crate::weather::map_live_to_tsw(&resp);
    let cfg = crate::config::Config::load();
    let api_key = crate::tsw::resolve_api_key_pub(&cfg);
    if api_key.is_empty() {
        return err(StatusCode::INTERNAL_SERVER_ERROR, "no TSW API key available");
    }
    let (applied, total) = crate::weather::apply_to_tsw(&api_key, &mapped).await;
    Json(json!({
        "latitude": lat,
        "longitude": lng,
        "weather": mapped,
        "applied": applied,
        "total": total,
        "source": "open-meteo",
    }))
    .into_response()
}

async fn weather_historical_fetch(
    State(t): State<Telemetry>,
    Query(q): Query<HistoricalQuery>,
) -> Response {
    if q.date.is_empty() {
        return err(StatusCode::BAD_REQUEST, "date is required (YYYY-MM-DD)");
    }
    if chrono::NaiveDate::parse_from_str(&q.date, "%Y-%m-%d").is_err() {
        return err(StatusCode::BAD_REQUEST, "invalid date format, use YYYY-MM-DD");
    }
    let (lat, lng) = match player_pos_from_telemetry(&t) {
        Ok(p) => p,
        Err(e) => return err(StatusCode::BAD_REQUEST, e),
    };
    let snap = t.snapshot().unwrap_or(json!({}));
    let game_hour = crate::weather::game_hour(&snap);
    let archive = match crate::weather::fetch_archive(lat, lng, &q.date).await {
        Ok(a) => a,
        Err(e) => return err(StatusCode::BAD_GATEWAY, format!("failed to fetch historical weather: {e}")),
    };
    let idx = crate::weather::closest_hour_index(&archive, game_hour);
    let mapped = crate::weather::map_archive_to_tsw(&archive, idx);
    Json(json!({
        "latitude": lat,
        "longitude": lng,
        "date": q.date,
        "game_hour": game_hour,
        "weather": mapped,
        "source": "open-meteo-archive",
    }))
    .into_response()
}

async fn weather_historical_apply(
    State(t): State<Telemetry>,
    Query(q): Query<HistoricalQuery>,
) -> Response {
    if q.date.is_empty() {
        return err(StatusCode::BAD_REQUEST, "date is required (YYYY-MM-DD)");
    }
    if chrono::NaiveDate::parse_from_str(&q.date, "%Y-%m-%d").is_err() {
        return err(StatusCode::BAD_REQUEST, "invalid date format, use YYYY-MM-DD");
    }
    let (lat, lng) = match player_pos_from_telemetry(&t) {
        Ok(p) => p,
        Err(e) => return err(StatusCode::BAD_REQUEST, e),
    };
    let snap = t.snapshot().unwrap_or(json!({}));
    let game_hour = crate::weather::game_hour(&snap);
    let archive = match crate::weather::fetch_archive(lat, lng, &q.date).await {
        Ok(a) => a,
        Err(e) => return err(StatusCode::BAD_GATEWAY, format!("failed to fetch historical weather: {e}")),
    };
    let idx = crate::weather::closest_hour_index(&archive, game_hour);
    let mapped = crate::weather::map_archive_to_tsw(&archive, idx);
    let cfg = crate::config::Config::load();
    let api_key = crate::tsw::resolve_api_key_pub(&cfg);
    if api_key.is_empty() {
        return err(StatusCode::INTERNAL_SERVER_ERROR, "no TSW API key available");
    }
    let (applied, total) = crate::weather::apply_to_tsw(&api_key, &mapped).await;
    Json(json!({
        "latitude": lat,
        "longitude": lng,
        "date": q.date,
        "game_hour": game_hour,
        "weather": mapped,
        "applied": applied,
        "total": total,
        "source": "open-meteo-archive",
    }))
    .into_response()
}

// ---------------------------- /api/timetables/{id}/map-features
// Serves the pre-built features blob VERBATIM so we don't re-parse a 500KB
// JSON document on every HUD map open (same hot-path optimization hud-go has).
async fn timetable_map_features(Path(id): Path<i64>) -> Response {
    match tokio::task::spawn_blocking(move || crate::db::timetable_map_features(id)).await {
        Ok(Ok(Some(blob))) => {
            let body = format!(r#"{{"timetable_id":{id},"features":{blob}}}"#);
            ([("content-type", "application/json; charset=utf-8")], body).into_response()
        }
        Ok(Ok(None)) => err(StatusCode::NOT_FOUND, "no map features for timetable"),
        Ok(Err(e)) => err(StatusCode::INTERNAL_SERVER_ERROR, e),
        Err(e) => err(StatusCode::INTERNAL_SERVER_ERROR, e.to_string()),
    }
}

// ---------------------------- POST /api/timetables/{id}/build-map-features
// Synchronous build (~5–30s on big DLCs). Returns the freshly-built feature
// blob so the caller (e.g. "Generate Map" button on /timetables/{id}) renders
// without a second fetch.
async fn build_map_features_for_timetable(Path(id): Path<i64>) -> Response {
    let built = match tokio::task::spawn_blocking(move || crate::db::build_timetable_map_features(id)).await {
        Ok(Ok(b)) => b,
        Ok(Err(e)) => return err(StatusCode::INTERNAL_SERVER_ERROR, e),
        Err(e) => return err(StatusCode::INTERNAL_SERVER_ERROR, e.to_string()),
    };
    if !built {
        return err(
            StatusCode::BAD_REQUEST,
            "timetable has no route or no route_coordinates; cannot build",
        );
    }
    // Read it back so the response mirrors the GET shape.
    match tokio::task::spawn_blocking(move || crate::db::timetable_map_features(id)).await {
        Ok(Ok(Some(blob))) => {
            let body = format!(r#"{{"timetable_id":{id},"features":{blob}}}"#);
            ([("content-type", "application/json; charset=utf-8")], body).into_response()
        }
        Ok(Ok(None)) => err(StatusCode::INTERNAL_SERVER_ERROR, "build reported success but blob missing"),
        Ok(Err(e)) => err(StatusCode::INTERNAL_SERVER_ERROR, e),
        Err(e) => err(StatusCode::INTERNAL_SERVER_ERROR, e.to_string()),
    }
}

// ---------------------------- /api/routes/{id}/map-data
async fn route_map_data(Path(id): Path<i64>) -> Response {
    match tokio::task::spawn_blocking(move || crate::db::route_map_data(id)).await {
        Ok(Ok(Some(value))) => Json(value).into_response(),
        Ok(Ok(None)) => err(StatusCode::NOT_FOUND, "Route not found"),
        Ok(Err(e)) => err(StatusCode::INTERNAL_SERVER_ERROR, e),
        Err(e) => err(StatusCode::INTERNAL_SERVER_ERROR, e.to_string()),
    }
}

// ---------------------------- /api/map/route-data/:id

async fn map_route_data(Path(id): Path<i64>) -> Response {
    match tokio::task::spawn_blocking(move || crate::db::map_route_data(id)).await {
        Ok(Ok(value)) => Json(value).into_response(),
        Ok(Err(crate::db::MapRouteErr::NotFound)) => {
            err(StatusCode::NOT_FOUND, format!("Timetable {id} not found"))
        }
        Ok(Err(crate::db::MapRouteErr::Db(e))) => err(StatusCode::INTERNAL_SERVER_ERROR, e),
        Err(e) => err(StatusCode::INTERNAL_SERVER_ERROR, e.to_string()),
    }
}

fn humanize(slug: &str) -> String {
    slug.split(|c| c == '_' || c == '-')
        .filter(|p| !p.is_empty())
        .map(|p| {
            let mut chars = p.chars();
            match chars.next() {
                Some(c) => c.to_uppercase().collect::<String>() + chars.as_str(),
                None => String::new(),
            }
        })
        .collect::<Vec<_>>()
        .join(" ")
}
