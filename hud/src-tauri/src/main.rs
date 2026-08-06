// TSW HUD shell. One Tauri 2 process, multiple webview windows:
// - shell window (declared in tauri.conf.json) — main tabbed UI
// - widget windows (created dynamically by `widgets::open_collection`) — overlays
//
// Backend services live in this process too:
// - tsw poller (background thread, CommAPI raw TCP → shared Mutex snapshot)
// - axum web HUD server (toggleable; serves the LAN web HUD pages)
// All read paths surface to JS via `#[tauri::command]` functions.
//
// `windows_subsystem = "windows"` unconditionally so the user never sees a
// console window pop up next to the shell, even in debug builds.
#![windows_subsystem = "windows"]

// Ported from hud-rust/src/. Most modules carry unused-warnings until later
// phases wire the relevant `#[tauri::command]`s — that's expected.
#![allow(dead_code)]

mod codename;
mod collections;
mod config;
mod db;
mod extractor;
mod extractor_db_writer;
mod extractor_pipeline;
mod uasset_ribbon_cooked;
mod uasset_features_cooked;
mod uasset_clothoid;
mod uasset_texture;
mod geo;
mod cookedmap;
mod service_path;
mod service_path_graph;
mod zip_writer;
mod db_export;
mod db_import;
mod features;
mod output_format;
mod server;
mod tsw;
mod uasset;
mod uasset_datatrack;
mod uasset_route_definition;
mod uasset_rvd;
mod uasset_scenario;
mod uasset_timetable;
mod weather;
mod widget_cmds;
mod widget_library;
mod widgets;

use std::sync::Mutex;
use tauri::Manager;

/// Long-lived shared state managed by Tauri. The web-HUD server runs on a
/// dedicated background thread + its own tokio runtime; we keep its `Handle`
/// here so Start/Stop survive across IPC command calls. `Mutex` (not RwLock)
/// because Start/Stop both mutate the option.
pub struct AppShared {
    pub server: Mutex<Option<server::Handle>>,
    pub telemetry: server::Telemetry,
    pub hud: server::HudState,
    /// Cross-window shared state for the schedule\u{2192}dashboard hand-off
    /// (selected stop). Schedule's row click writes it; dashboard reads it
    /// each tick to render live distance to that stop.
    pub selected_stop: Mutex<Option<serde_json::Value>>,
}

impl Default for AppShared {
    fn default() -> Self {
        Self {
            server: Mutex::new(None),
            telemetry: server::Telemetry::default(),
            hud: server::HudState::default(),
            selected_stop: Mutex::new(None),
        }
    }
}

#[tauri::command]
fn ping() -> &'static str {
    "pong"
}

#[tauri::command]
fn get_config() -> config::Config {
    config::Config::load()
}

/// Save the supplied config to `resources/configuration.json`. The frontend
/// passes the full struct; `Config` derives `Deserialize` via serde so Tauri
/// hands us a typed value with no manual parsing.
#[tauri::command]
fn set_config(config: config::Config) -> Result<(), String> {
    config.save()
}

/// Where the running binary thinks `configuration.json` lives, shown RELATIVE
/// to the app/exe directory (e.g. `resources\configuration.json`) so the user's
/// home path (`C:\Users\<name>\…`) isn't exposed in the Settings footer.
#[tauri::command]
fn config_path() -> String {
    let abs = config::config_path();
    // Prefer a path relative to the executable's directory.
    if let Some(base) = std::env::current_exe().ok().and_then(|p| p.parent().map(|p| p.to_path_buf())) {
        if let Ok(rel) = abs.strip_prefix(&base) {
            return config::to_win_path(&rel.to_string_lossy());
        }
    }
    // Dev / out-of-tree fallback: trim everything before the `resources` segment.
    let s = abs.to_string_lossy().to_string();
    if let Some(i) = s.to_lowercase().find("resources") {
        return config::to_win_path(&s[i..]);
    }
    config::to_win_path(&s)
}

/// Return the parsed translation map for `lang` (e.g. "de", "en-US"), read from
/// `resources/views/locales/<lang>.json`. Falls back to `en.json` for an unknown
/// or missing language. The desktop shell uses this to translate its UI without
/// the axum server running (the browser HUD pages fetch /locales/ over HTTP).
#[tauri::command]
fn get_locale(lang: String) -> Result<serde_json::Value, String> {
    let dir = config::resources_dir().join("views").join("locales");
    let safe = !lang.is_empty()
        && lang.chars().all(|c| c.is_ascii_alphanumeric() || c == '-' || c == '_');
    let mut file = dir.join(format!("{lang}.json"));
    if !safe || !file.is_file() {
        file = dir.join("en.json");
    }
    let text =
        std::fs::read_to_string(&file).map_err(|e| format!("read {}: {e}", file.display()))?;
    serde_json::from_str(&text).map_err(|e| e.to_string())
}

// ---------- Collections / overlay-widget commands ----------

#[tauri::command]
fn list_collections() -> Vec<collections::Collection> {
    collections::list()
}

/// Open every widget declared in collection `slug` as its own webview window
/// in this process. Returns the list of window labels that were opened (or
/// brought to front if already loaded).
/// `async` so Tauri schedules it on the tokio worker pool instead of the
/// main thread. Sync `#[tauri::command]`s run on the main thread, and
/// `WebviewWindowBuilder::build()` needs the main thread to be free to
/// dispatch window-creation messages — so a sync command building >1 window
/// deadlocks on the 2nd window. Async commands free the main thread.
#[tauri::command]
async fn open_collection(
    app: tauri::AppHandle,
    slug: String,
) -> Result<Vec<String>, String> {
    let c = collections::get(&slug).ok_or_else(|| format!("collection not found: {slug}"))?;
    widgets::open_collection(&app, &c)
}

#[tauri::command]
async fn close_collection(app: tauri::AppHandle, slug: String) -> Result<usize, String> {
    widgets::close_collection(&app, &slug)
}

/// Create or overwrite a collection's manifest. `slug` is the folder name; for
/// a brand-new collection the folder is created. Returns nothing on success.
#[tauri::command]
fn save_collection(
    slug: String,
    manifest: collections::CollectionManifest,
) -> Result<(), String> {
    collections::save(&slug, &manifest)
}

/// Delete a collection folder (manifest + widget files). The frontend confirms
/// before calling this.
#[tauri::command]
fn delete_collection(slug: String) -> Result<(), String> {
    collections::delete(&slug)
}

// ---------- Widget library commands ----------

/// List the standalone widget HTML files under `resources/widgets/`.
#[tauri::command]
fn widgets_list() -> Vec<widget_library::WidgetFile> {
    widget_library::list()
}

/// Rewrite a widget's display name (its `<title>`); filename is unchanged.
#[tauri::command]
fn widget_set_title(filename: String, title: String) -> Result<(), String> {
    widget_library::set_title(&filename, &title)
}

/// Save an uploaded single HTML file as a new widget. Returns the final
/// on-disk filename (may differ from `filename` if there was a collision).
#[tauri::command]
fn widget_upload(filename: String, contents: String) -> Result<String, String> {
    widget_library::save(&filename, &contents)
}

/// Delete a widget file. The frontend confirms before calling this.
#[tauri::command]
fn widget_delete(filename: String) -> Result<(), String> {
    widget_library::delete(&filename)
}

// ---------- Web HUD server commands ----------

#[derive(serde::Serialize)]
struct WebHudStatus {
    running: bool,
    port: Option<u16>,
    /// LAN IPv4 the server is reachable on (or null if no LAN). Frontend uses
    /// this to build the per-card endpoint URLs (e.g.
    /// `http://{lan}:{port}/find-timetable?hud=mobile`) without re-parsing
    /// the `urls` strings.
    lan_ip: Option<String>,
    status: String,
    log: Vec<String>,
    /// `true` once TSW telemetry has actually delivered a snapshot — the
    /// frontend uses this to "unlock" the card grid (matches the
    /// cards_unlocked flag in hud-rust's web_hud_ui).
    telemetry_live: bool,
    /// `enable_subscriptions` from the config. Lets the UI show the
    /// "Subscriptions disabled in Settings" notice without a second IPC.
    subscriptions_enabled: bool,
    /// Reachable URLs — [loopback, lan]. Both forms so the user can copy/paste
    /// or share via the QR card on another device.
    urls: Vec<String>,
}

/// Best-effort LAN IP (the first non-loopback IPv4 the OS hands us). Doesn't
/// touch the network — just iterates local interfaces.
fn lan_ipv4() -> Option<std::net::Ipv4Addr> {
    use std::net::{IpAddr, UdpSocket};
    // Trick: bind a UDP socket and "connect" it to a public address to ask
    // the OS which local interface it would route through. No packets sent.
    let s = UdpSocket::bind("0.0.0.0:0").ok()?;
    s.connect("8.8.8.8:53").ok()?;
    match s.local_addr().ok()?.ip() {
        IpAddr::V4(v4) if !v4.is_loopback() => Some(v4),
        _ => None,
    }
}

#[tauri::command]
async fn web_hud_status(state: tauri::State<'_, AppShared>) -> Result<WebHudStatus, String> {
    let port = state.server.lock().unwrap().as_ref().map(|h| h.port);
    let running = port.is_some();
    let lan = lan_ipv4().map(|ip| ip.to_string());
    let mut urls = Vec::new();
    if let Some(p) = port {
        urls.push(format!("http://127.0.0.1:{p}"));
        if let Some(ip) = &lan {
            urls.push(format!("http://{ip}:{p}"));
        }
    }
    Ok(WebHudStatus {
        running,
        port,
        lan_ip: lan,
        status: state.telemetry.status(),
        log: state.telemetry.log(),
        telemetry_live: state.telemetry.snapshot().is_some(),
        subscriptions_enabled: config::Config::load().enable_subscriptions,
        urls,
    })
}

#[tauri::command]
async fn web_hud_start(
    port: u16,
    state: tauri::State<'_, AppShared>,
) -> Result<WebHudStatus, String> {
    // Scope the lock so the MutexGuard is dropped before any await — std
    // Mutex guards are !Send, and Tauri's async-command harness requires
    // the future to be Send.
    {
        let mut guard = state.server.lock().unwrap();
        if guard.is_some() {
            return Err("server already running \u{2014} stop it first".into());
        }
        // The always-on poller (spawned in setup) already owns the TSW
        // subscriptions, so ask server::start not to spawn its own — two
        // pollers would fight over POST/DELETE /subscription/.
        let mut cfg = config::Config::load();
        cfg.enable_subscriptions = false;
        let handle =
            server::start(port, state.telemetry.clone(), state.hud.clone(), cfg)?;
        *guard = Some(handle);
    }
    // Re-use the status query so the response shape is identical to polling
    // — keeps the JS side simpler.
    web_hud_status(state).await
}

#[tauri::command]
async fn web_hud_stop(state: tauri::State<'_, AppShared>) -> Result<WebHudStatus, String> {
    let taken = { state.server.lock().unwrap().take() };
    if let Some(h) = taken {
        h.stop();
    }
    web_hud_status(state).await
}

/// Hand an external URL off to the OS to open in the user's default browser.
/// Used by the Web HUD cards so click-through goes to Chrome/Edge instead of
/// opening inside this Tauri webview.
#[tauri::command]
fn open_external(url: String) -> Result<(), String> {
    open::that(url).map_err(|e| e.to_string())
}

/// Show a native "Save As" dialog and write `contents` to the chosen path.
/// Returns the saved path, or `None` if the user cancelled. Used by the Widgets
/// tab's "Download LLM guide" so the user picks where the file lands.
#[tauri::command]
async fn save_text_file(
    default_name: String,
    contents: String,
) -> Result<Option<String>, String> {
    let chosen = tokio::task::spawn_blocking(move || {
        rfd::FileDialog::new()
            .set_file_name(&default_name)
            .add_filter("Text file", &["txt"])
            .save_file()
    })
    .await
    .map_err(|e| e.to_string())?;

    match chosen {
        Some(path) => {
            std::fs::write(&path, contents)
                .map_err(|e| format!("write {}: {e}", path.display()))?;
            Ok(Some(path.to_string_lossy().to_string()))
        }
        None => Ok(None),
    }
}

/// Rebuild a route's shareable export zip from the database and save it via a
/// native "Save As" dialog. Returns the saved path, or `None` if cancelled.
#[tauri::command]
async fn export_route(route_id: i64) -> Result<Option<String>, String> {
    // Default filename = sanitised route display name (same stem the extractor uses).
    let default_name = tokio::task::spawn_blocking(move || {
        crate::db::with_read(|c| {
            let name: String = c
                .query_row("SELECT COALESCE(name,'') FROM routes WHERE id=?1", [route_id], |r| r.get(0))
                .unwrap_or_default();
            Ok(name)
        })
        .unwrap_or_default()
    })
    .await
    .map_err(|e| e.to_string())?;
    let stem = crate::zip_writer::sanitise_filename(&default_name);
    let stem = if stem.is_empty() { format!("route_{route_id}") } else { stem };

    let chosen = tokio::task::spawn_blocking(move || {
        rfd::FileDialog::new()
            .set_file_name(format!("{stem}.zip"))
            .add_filter("Route export (zip)", &["zip"])
            .save_file()
    })
    .await
    .map_err(|e| e.to_string())?;

    let Some(dest) = chosen else { return Ok(None) };

    tokio::task::spawn_blocking(move || {
        // Build into a temp dir (the writer derives its own filename), then move
        // the single zip to the user's chosen path.
        let tmp = std::env::temp_dir().join(format!("hud_export_{route_id}"));
        let _ = std::fs::remove_dir_all(&tmp);
        std::fs::create_dir_all(&tmp).map_err(|e| e.to_string())?;
        let res = crate::db_export::export_route_zip(route_id, &tmp)?;
        let built = std::path::PathBuf::from(&res.zip_path);
        std::fs::rename(&built, &dest)
            .or_else(|_| std::fs::copy(&built, &dest).map(|_| ()))
            .map_err(|e| format!("save zip: {e}"))?;
        let _ = std::fs::remove_dir_all(&tmp);
        Ok(Some(dest.to_string_lossy().to_string()))
    })
    .await
    .map_err(|e| e.to_string())?
}

/// Show a native "Open" dialog for a route export zip and return the chosen
/// path (or `None` if cancelled). Split out from [`import_route_zip`] so the
/// frontend can keep its busy/timer overlay off-screen during file browsing
/// and only start the timer once the actual import begins.
#[tauri::command]
async fn pick_route_zip() -> Result<Option<String>, String> {
    let chosen = tokio::task::spawn_blocking(move || {
        rfd::FileDialog::new()
            .add_filter("Route export (zip)", &["zip"])
            .pick_file()
    })
    .await
    .map_err(|e| e.to_string())?;
    Ok(chosen.map(|p| p.to_string_lossy().to_string()))
}

/// Import a route export zip (already chosen via [`pick_route_zip`]) into the
/// catalog. The zip's own route file decides which route is created/replaced
/// (matched by name / cross-pak ref) — it imports an entire route. Returns a
/// short summary string.
#[tauri::command]
async fn import_route_zip(path: String) -> Result<String, String> {
    tokio::task::spawn_blocking(move || {
        let res = crate::db_import::import_route_zip(&path)?;
        let mut summary = format!(
            "Imported {} — {} services, {} formations, {} classes, {} thumbnails",
            res.route_name, res.timetables_imported, res.formations_created,
            res.train_classes_ingested, res.thumbnails_written,
        );
        if !res.errors.is_empty() {
            summary.push_str(&format!(" ({} warnings)", res.errors.len()));
            for e in res.errors.iter().take(20) { eprintln!("[import] {e}"); }
        }
        Ok(summary)
    })
    .await
    .map_err(|e| e.to_string())?
}

/// Generate a PNG QR-code for the given URL and return it as a data URL the
/// `<img src>` can render directly. `qrcode` builds an `image::Luma<u8>`
/// buffer; we PNG-encode it in-memory.
#[tauri::command]
fn qr_png_data_url(text: String) -> Result<String, String> {
    use base64::Engine as _;
    let code = qrcode::QrCode::new(text.as_bytes()).map_err(|e| e.to_string())?;
    let img = code
        .render::<image::Luma<u8>>()
        .min_dimensions(220, 220)
        .quiet_zone(true)
        .build();
    let mut bytes: Vec<u8> = Vec::new();
    image::DynamicImage::ImageLuma8(img)
        .write_to(&mut std::io::Cursor::new(&mut bytes), image::ImageFormat::Png)
        .map_err(|e| e.to_string())?;
    let b64 = base64::engine::general_purpose::STANDARD.encode(&bytes);
    Ok(format!("data:image/png;base64,{b64}"))
}

/// Headless single-route re-extraction:  `hud.exe extract-route "Boston Sprinter"`.
/// Runs the exact same pipeline as the Extraction tab's per-route button, but
/// from the CLI so a route can be rebuilt without driving the UI. Writes into
/// the same catalog DB (db_path() is compile-time-anchored). TSW6 paks need no
/// AES key. Matches every route whose name contains the argument.
fn run_extract_route_cli(name: &str) {
    let cfg = config::Config::load();
    let tsw = std::path::PathBuf::from(cfg.extractor_tsw_path.trim());
    let routes = match extractor::discover_routes(&tsw) {
        Ok(r) => r,
        Err(e) => { eprintln!("discover_routes failed: {e}"); std::process::exit(1); }
    };
    let needle = name.to_lowercase();
    // Prefer an exact (case-insensitive) codename match so "BostonProvidence"
    // doesn't also drag in "BostonProvidenceGameplayPack" (which is an overlay
    // pulled in automatically by resolve_overlay_paks anyway). Fall back to a
    // substring match only when there's no exact hit.
    let exact: Vec<&extractor::DiscoveredRoute> =
        routes.iter().filter(|r| r.name.to_lowercase() == needle).collect();
    let matches: Vec<&extractor::DiscoveredRoute> = if !exact.is_empty() {
        exact
    } else {
        routes.iter().filter(|r| r.name.to_lowercase().contains(&needle)).collect()
    };
    if matches.is_empty() {
        eprintln!("no route matching '{name}'. Available:");
        for r in &routes { eprintln!("  {}", r.name); }
        std::process::exit(1);
    }
    if matches.len() > 1 {
        eprintln!("'{name}' matched {} routes — be more specific:", matches.len());
        for r in &matches { eprintln!("  {}", r.name); }
        std::process::exit(1);
    }
    let base_temp = if !cfg.extractor_temp_dir.trim().is_empty() {
        std::path::PathBuf::from(cfg.extractor_temp_dir.trim())
    } else {
        std::env::current_exe().ok().and_then(|p| p.parent().map(|p| p.to_path_buf()))
            .map(|p| p.join("extractor_temp")).unwrap_or_else(std::env::temp_dir)
    };
    let sink: extractor_pipeline::LogSink = Box::new(|kind, msg| {
        if kind.is_empty() { println!("{msg}"); } else { println!("[{kind}] {msg}"); }
    });
    for r in &matches {
        println!("=== extracting: {} ({}) ===", r.name, r.pak_path);
        let pak = std::path::PathBuf::from(&r.pak_path);
        let stem = pak.file_stem().and_then(|s| s.to_str()).unwrap_or("pak");
        let dest = base_temp.join(stem);
        let overlays = extractor::resolve_overlay_paks(&pak);
        if !overlays.is_empty() { println!("  {} overlay pak(s)", overlays.len()); }
        match extractor_pipeline::run_pak(&pak, &dest, "", &overlays, Some(&sink)) {
            Ok(c) => println!("DONE: route_coords={} tt_paths={} signals={} switches={} collectables={}",
                c.route_coords_written, c.timetable_paths_written, c.signals_written, c.switches_written, c.collectables_written),
            Err(e) => eprintln!("run_pak failed: {e}"),
        }
        let _ = std::fs::remove_dir_all(&dest);
    }
    println!("extract-route complete.");
}

fn main() {
    // Headless CLI entry: `hud.exe extract-route "<name substring>"`.
    let argv: Vec<String> = std::env::args().collect();
    if argv.len() >= 3 && argv[1] == "extract-route" {
        run_extract_route_cli(&argv[2]);
        return;
    }
    // Headless validation hook: `hud.exe export-route <route_id> [dest_dir]`.
    if argv.len() >= 3 && argv[1] == "export-route" {
        let rid: i64 = argv[2].parse().unwrap_or(0);
        let dest = std::path::PathBuf::from(argv.get(3).map(|s| s.as_str()).unwrap_or("."));
        match db_export::export_route_zip(rid, &dest) {
            Ok(r) => println!("DONE: {} ({} services, {} thumbs, {} bytes)",
                r.zip_path, r.services_written, r.thumbnails_packed, r.bytes),
            Err(e) => { eprintln!("export failed: {e}"); std::process::exit(1); }
        }
        return;
    }
    // Headless import hook: `hud.exe import-zip <path-to-zip>`.
    if argv.len() >= 3 && argv[1] == "import-zip" {
        // Schema top-ups first (the new scenario_display_name column etc.).
        let _ = db::ensure_schema();
        match db_import::import_route_zip(&argv[2]) {
            Ok(r) => {
                println!("DONE: route='{}' created={} | services={} skipped={} formations={} classes={} thumbs={}",
                    r.route_name, r.route_created, r.timetables_imported, r.timetables_skipped,
                    r.formations_created, r.train_classes_ingested, r.thumbnails_written);
                if !r.errors.is_empty() {
                    eprintln!("{} warning(s):", r.errors.len());
                    for e in r.errors.iter().take(40) { eprintln!("  {e}"); }
                }
            }
            Err(e) => { eprintln!("import failed: {e}"); std::process::exit(1); }
        }
        return;
    }

    tauri::Builder::default()
        // Remember the shell window's last size + position so it relaunches at
        // whatever the user last left it (their "current" size becomes default).
        .plugin(tauri_plugin_window_state::Builder::default().build())
        .manage(AppShared::default())
        .register_uri_scheme_protocol("widget", widgets::widget_protocol_handler)
        .register_uri_scheme_protocol("thumb", widgets::thumb_protocol_handler)
        .setup(|app| {
            // Schema top-ups for columns added after the shipped DB was baked
            // (idempotent; non-fatal so a read-only DB doesn't block startup).
            if let Err(e) = db::ensure_schema() {
                eprintln!("[db] ensure_schema: {e}");
            }

            // The shell window is declared `visible: false` so it doesn't flash
            // at the tauri.conf default size and then visibly snap to the
            // remembered size. Restore the saved size/position while it's still
            // hidden, THEN reveal it — one clean appearance at the right size.
            if let Some(win) = app.get_webview_window("shell") {
                use tauri_plugin_window_state::{StateFlags, WindowExt};
                let _ = win.restore_state(StateFlags::all());
                let _ = win.show();
            }

            // Always-on TSW telemetry poller. Runs on its own thread + tokio
            // runtime so widget windows (and the optional web-HUD server) all
            // see live data without needing to flip the Web HUD tab first.
            // Skipped only when the user disabled subscriptions in Settings.
            {
                let cfg = config::Config::load();
                if cfg.enable_subscriptions {
                    let state: tauri::State<'_, AppShared> = app.state();
                    let telemetry = state.telemetry.clone();
                    std::thread::Builder::new()
                        .name("hud-tsw-poller".into())
                        .spawn(move || {
                            let rt = tokio::runtime::Builder::new_multi_thread()
                                .enable_all()
                                .build()
                                .expect("tokio runtime");
                            rt.block_on(tsw::connection_loop(telemetry, cfg));
                        })
                        .expect("spawn tsw poller");
                } else {
                    eprintln!("[tsw] subscriptions disabled in Settings; widgets stay OFFLINE");
                }
            }

            // Dev convenience: if HUD_AUTO_OPEN_COLLECTION=<slug> is set, open
            // that collection's widgets ~600 ms after the shell appears. Lets
            // us drive a screenshot or smoke test without clicking through the
            // UI. Harmless in production — the env var is off by default.
            if let Ok(slug) = std::env::var("HUD_AUTO_OPEN_COLLECTION") {
                if !slug.is_empty() {
                    let handle = app.handle().clone();
                    std::thread::spawn(move || {
                        std::thread::sleep(std::time::Duration::from_millis(600));
                        if let Some(c) = collections::get(&slug) {
                            if let Err(e) = widgets::open_collection(&handle, &c) {
                                eprintln!("[auto-open] {e}");
                            }
                        } else {
                            eprintln!("[auto-open] collection not found: {slug}");
                        }
                    });
                }
            }

            // Similar convenience for the Web HUD server: HUD_AUTO_START_WEB_HUD=<port>
            // boots axum on the given port at launch. Useful for verifying
            // server::start() end-to-end without driving the UI.
            if let Ok(port_str) = std::env::var("HUD_AUTO_START_WEB_HUD") {
                if let Ok(port) = port_str.parse::<u16>() {
                    let handle = app.handle().clone();
                    std::thread::spawn(move || {
                        std::thread::sleep(std::time::Duration::from_millis(400));
                        let state: tauri::State<'_, AppShared> = handle.state();
                        let mut cfg = config::Config::load();
                        cfg.enable_subscriptions = false; // always-on poller owns subs
                        match server::start(port, state.telemetry.clone(), state.hud.clone(), cfg) {
                            Ok(h) => {
                                *state.server.lock().unwrap() = Some(h);
                                eprintln!("[auto-start] web HUD on :{port}");
                            }
                            Err(e) => eprintln!("[auto-start] web HUD failed: {e}"),
                        }
                    });
                }
            }

            // Dev-only: force telemetry into the "live" state without TSW
            // actually running, so we can screenshot the unlocked card grid
            // on a workstation that doesn't have TSW open. Stamps a sentinel
            // payload; widgets that poll real telemetry will see a stub and
            // typically render their OFFLINE state, which is the correct
            // "no real data" behaviour per the no-demo-fallback rule.
            if std::env::var("HUD_FAKE_TELEMETRY_LIVE").ok().as_deref() == Some("1") {
                let state: tauri::State<'_, AppShared> = app.state();
                state.telemetry.set_data(Some(serde_json::json!({
                    "connected": false,
                    "_dev_sentinel": true,
                    "isSlipping": true,
                    "isTractionLocked": false,
                    "speed": 42,
                    "limit": 80
                })));
                state.telemetry.set_status("DEV: fake telemetry live");
            }
            Ok(())
        })
        .invoke_handler(tauri::generate_handler![
            ping,
            get_config,
            set_config,
            config_path,
            get_locale,
            list_collections,
            open_collection,
            close_collection,
            save_collection,
            delete_collection,
            widgets_list,
            widget_set_title,
            widget_upload,
            widget_delete,
            web_hud_status,
            web_hud_start,
            web_hud_stop,
            qr_png_data_url,
            open_external,
            save_text_file,
            export_route,
            pick_route_zip,
            import_route_zip,
            widget_cmds::get_telemetry,
            widget_cmds::get_selected_stop,
            widget_cmds::set_selected_stop,
            widget_cmds::get_active_timetable,
            widget_cmds::get_timetable_entries,
            widget_cmds::get_route_markers,
            widget_cmds::get_timetable_markers,
            widget_cmds::debug_dump,
            widget_cmds::get_route_coordinates,
            widget_cmds::get_map_features,
            widget_cmds::get_route_map_data,
            widget_cmds::weather_presets,
            widget_cmds::weather_apply,
            widget_cmds::weather_apply_preset,
            widget_cmds::weather_preset_save,
            widget_cmds::weather_preset_delete,
            widget_cmds::weather_live_apply,
            widget_cmds::weather_historical_apply,
            widget_cmds::timetable_filter_options,
            widget_cmds::timetable_search,
            widget_cmds::timetable_detail,
            widget_cmds::timetable_update,
            widget_cmds::entry_update,
            widget_cmds::entry_create,
            widget_cmds::entry_delete,
            widget_cmds::entries_save,
            widget_cmds::actions_list,
            widget_cmds::route_search,
            widget_cmds::route_detail,
            widget_cmds::route_update,
            widget_cmds::delete_route,
            widget_cmds::timetables_for_route,
            widget_cmds::classes_for_route,
            widget_cmds::route_geometry,
            widget_cmds::train_classes_list,
            widget_cmds::train_class_detail,
            widget_cmds::delete_train_class,
            widget_cmds::train_class_thumbnails,
            widget_cmds::custom_huds_list,
            // dev pages
            widget_cmds::countries_list_full,
            widget_cmds::country_save,
            widget_cmds::country_delete,
            widget_cmds::locations_search,
            widget_cmds::location_save,
            widget_cmds::location_delete,
            widget_cmds::formations_search,
            widget_cmds::formation_save,
            widget_cmds::formation_delete,
            widget_cmds::formation_detail,
            widget_cmds::api_calls_get,
            widget_cmds::api_calls_set,
            widget_cmds::subscription_action,
            widget_cmds::subscription_data,
            widget_cmds::subscription_test_path,
            widget_cmds::subscription_status,
            widget_cmds::db_refresh_status,
            widget_cmds::db_refresh_copy,
            widget_cmds::extractor_list_routes,
            widget_cmds::extractor_find_repak,
            widget_cmds::extractor_unpack_pak,
            widget_cmds::extractor_parse_route_definition,
            widget_cmds::extractor_parse_rvd,
            widget_cmds::extractor_parse_scenario,
            widget_cmds::extractor_parse_timetable,
            widget_cmds::extractor_parse_datatrack,
            widget_cmds::extractor_nuke_db,
            widget_cmds::extractor_run_pak,
            widget_cmds::extractor_rebuild_thumbnails,
            widget_cmds::extractor_autodetect_tsw_root,
            widget_cmds::extractor_rebuild_train_classes,
            widget_cmds::extractor_mark_completed,
            widget_cmds::extractor_unmark_completed,
            widget_cmds::extractor_completed_list,
            widget_cmds::extractor_delete_zip,
            widget_cmds::extractor_zip_exists,
        ])
        .run(tauri::generate_context!())
        .expect("error while running tauri application");
}
