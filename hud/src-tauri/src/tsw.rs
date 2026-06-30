//! TSW CommAPI subscriber. Connects to the game's :31270 endpoint, registers
//! the subscriptions enumerated in hud-go/resources/api_calls.json, polls the
//! subscription bag every 100 ms, parses the telemetry into the same flat
//! shape the HUDs expect, and writes it into the shared Telemetry slot so
//! /stream emits it byte-for-byte the way hud-go does.
//!
//! HTTP is RAW TCP (not reqwest/ureq) per the documented CommAPI quirks: TSW's
//! embedded server is not RFC-strict and standard Rust clients trip on it.
//! Every POST/DELETE sends an explicit Content-Length, and Connection: close
//! is set so read_to_end terminates cleanly on EOF.

use std::collections::HashMap;
use std::path::PathBuf;
use std::time::Duration;

use serde::Deserialize;
use serde_json::{json, Map, Value};
use tokio::io::{AsyncReadExt, AsyncWriteExt};
use tokio::net::TcpStream;

use crate::config::Config;
use crate::server::Telemetry;

const COMM_API_ADDR: &str = "127.0.0.1:31270";
const REQ_TIMEOUT: Duration = Duration::from_secs(5);
const CORE_SECTION: &str = "Core";

// ---------------------------------------------------------------- raw HTTP

pub(crate) async fn do_request(
    method: &str,
    path: &str,
    body: Option<&[u8]>,
    api_key: &str,
) -> Result<(u16, Vec<u8>), String> {
    if api_key.is_empty() {
        return Err("no API key".into());
    }
    let stream_fut = TcpStream::connect(COMM_API_ADDR);
    let mut stream = tokio::time::timeout(REQ_TIMEOUT, stream_fut)
        .await
        .map_err(|_| "connect timeout".to_string())?
        .map_err(|e| format!("connect: {e}"))?;

    let body_len = body.map(|b| b.len()).unwrap_or(0);
    let mut head = String::new();
    head.push_str(&format!("{method} {path} HTTP/1.1\r\n"));
    head.push_str("Host: 127.0.0.1:31270\r\n");
    head.push_str(&format!("DTGCommKey: {api_key}\r\n"));
    head.push_str("Connection: close\r\n");
    if body_len > 0 {
        head.push_str("Content-Type: application/json\r\n");
    }
    // TSW's :31270 needs an explicit Content-Length even on empty POST/DELETE.
    head.push_str(&format!("Content-Length: {body_len}\r\n\r\n"));

    stream
        .write_all(head.as_bytes())
        .await
        .map_err(|e| format!("write head: {e}"))?;
    if let Some(b) = body {
        stream.write_all(b).await.map_err(|e| format!("write body: {e}"))?;
    }
    stream.flush().await.ok();

    let mut buf = Vec::new();
    tokio::time::timeout(REQ_TIMEOUT, stream.read_to_end(&mut buf))
        .await
        .map_err(|_| "read timeout".to_string())?
        .map_err(|e| format!("read: {e}"))?;

    parse_response(&buf)
}

fn parse_response(buf: &[u8]) -> Result<(u16, Vec<u8>), String> {
    let head_end = find_subsequence(buf, b"\r\n\r\n").ok_or("malformed: no header terminator")?;
    let head = std::str::from_utf8(&buf[..head_end]).map_err(|_| "head: non-utf8")?;
    let status_line = head.split("\r\n").next().ok_or("no status line")?;
    let mut parts = status_line.splitn(3, ' ');
    let _ver = parts.next();
    let code: u16 = parts
        .next()
        .ok_or("no status code")?
        .parse()
        .map_err(|e| format!("bad status: {e}"))?;
    Ok((code, buf[head_end + 4..].to_vec()))
}

fn find_subsequence(hay: &[u8], needle: &[u8]) -> Option<usize> {
    hay.windows(needle.len()).position(|w| w == needle)
}

// ------------------------------------------------------------ API key

pub fn resolve_api_key_pub(cfg: &Config) -> String { resolve_api_key(cfg) }

fn resolve_api_key(cfg: &Config) -> String {
    let cfg_key = cfg.api_key.trim();
    if !cfg_key.is_empty() {
        return cfg_key.to_string();
    }
    std::fs::read_to_string(comm_api_key_path(cfg))
        .unwrap_or_default()
        .trim()
        .to_string()
}

fn comm_api_key_path(cfg: &Config) -> PathBuf {
    let (override_path, game) = match cfg.tsw_version.as_str() {
        "tsw5" => (cfg.tsw5_key_path.as_str(), "TrainSimWorld5"),
        _ => (cfg.tsw6_key_path.as_str(), "TrainSimWorld6"),
    };
    if !override_path.is_empty() {
        return PathBuf::from(override_path);
    }
    let user = std::env::var("USERPROFILE")
        .or_else(|_| std::env::var("HOME"))
        .unwrap_or_default();
    PathBuf::from(user)
        .join("Documents")
        .join("My Games")
        .join(game)
        .join("Saved")
        .join("Config")
        .join("CommAPIKey.txt")
}

// ------------------------------------------------------------ api_calls.json

#[derive(Deserialize, Default)]
struct ApiCall {
    path: String,
    #[serde(default)]
    key: String,
    #[serde(default)]
    enabled: bool,
}

#[derive(Deserialize, Default)]
struct ApiCallSection {
    name: String,
    #[serde(default)]
    calls: Vec<ApiCall>,
}

#[derive(Deserialize, Default)]
struct ApiCallsFile {
    sections: Vec<ApiCallSection>,
}

pub fn api_calls_path() -> PathBuf {
    // The catalog lives next to configuration.json in hud's own resources/.
    crate::config::resources_dir().join("api_calls.json")
}

fn load_api_calls() -> ApiCallsFile {
    std::fs::read_to_string(api_calls_path())
        .ok()
        .and_then(|s| serde_json::from_str(&s).ok())
        .unwrap_or_default()
}

fn enabled_subscription_paths(file: &ApiCallsFile) -> Vec<String> {
    let mut out = Vec::new();
    for s in &file.sections {
        for c in &s.calls {
            if c.enabled {
                out.push(c.path.clone());
            }
        }
    }
    out
}

fn passthrough_keys(file: &ApiCallsFile) -> HashMap<String, String> {
    let mut m = HashMap::new();
    for s in &file.sections {
        if s.name == CORE_SECTION {
            continue;
        }
        for c in &s.calls {
            if !c.enabled {
                continue;
            }
            let key = if c.key.is_empty() { c.path.clone() } else { c.key.clone() };
            m.insert(c.path.clone(), key);
        }
    }
    m
}

// ---------------------------------------------------- Subscription lifecycle

async fn create_subscriptions(api_key: &str, paths: &[String]) {
    for p in paths {
        let url = format!("/subscription/{p}?Subscription=1");
        if let Err(e) = do_request("POST", &url, None, api_key).await {
            eprintln!("[TSW] create {p}: {e}");
        }
        // hud-go paces creates by 250 ms so TSW doesn't drop them.
        tokio::time::sleep(Duration::from_millis(250)).await;
    }
}

async fn delete_subscription(api_key: &str) {
    let _ = do_request("DELETE", "/subscription/?Subscription=1", None, api_key).await;
}

async fn fetch_subscription_data(api_key: &str) -> Result<Value, String> {
    let (status, body) = do_request("GET", "/subscription/?Subscription=1", None, api_key).await?;
    if status >= 400 {
        return Err(format!("HTTP {status}"));
    }
    if body.is_empty() {
        return Ok(json!({}));
    }
    serde_json::from_slice(&body).map_err(|e| format!("json: {e}"))
}

// ------- Public wrappers used by the API Subscriptions admin page (IPC).
//
// The always-on connection_loop owns subscription state during normal use;
// these wrappers let the user manually intervene from the admin tab without
// fighting the poller (the next poll cycle reasserts whatever's in
// api_calls.json, so a manual delete reverses itself within ~5 s — that's
// expected and matches hud-go's behaviour).

pub async fn pub_create_from_catalog(api_key: &str) -> usize {
    let file = load_api_calls();
    let paths = enabled_subscription_paths(&file);
    let n = paths.len();
    create_subscriptions(api_key, &paths).await;
    n
}

pub async fn pub_delete_all(api_key: &str) {
    delete_subscription(api_key).await;
}

pub async fn pub_fetch_data(api_key: &str) -> Result<Value, String> {
    fetch_subscription_data(api_key).await
}

/// POST a single subscription path, wait briefly for TSW to populate, fetch
/// just that node's value, then DELETE the temp sub so we don't leak it into
/// the main bag. Returns the raw Entry value (or "no data" sentinel).
pub async fn pub_test_path(api_key: &str, path: &str) -> Result<Value, String> {
    let post_url = format!("/subscription/{path}?Subscription=2");
    do_request("POST", &post_url, None, api_key).await?;
    // Pace + sample. 600 ms is enough for most paths; longer for first-time
    // subscribers, but we'd rather respond fast than block the UI.
    tokio::time::sleep(Duration::from_millis(600)).await;
    let (status, body) = do_request("GET", "/subscription/?Subscription=2", None, api_key).await?;
    // Always tear the temp sub down, even on error, so probes don't accumulate.
    let _ = do_request("DELETE", "/subscription/?Subscription=2", None, api_key).await;
    if status >= 400 {
        return Err(format!("HTTP {status}"));
    }
    if body.is_empty() {
        return Ok(json!({ "ok": false, "reason": "no data returned (path invalid or not active in current actor)" }));
    }
    let parsed: Value = serde_json::from_slice(&body).map_err(|e| format!("json: {e}"))?;
    // Pull the matching Entry out of the response so the UI doesn't have to.
    if let Some(entries) = parsed.get("Entries").and_then(|v| v.as_array()) {
        for e in entries {
            if e.get("Path").and_then(|v| v.as_str()) == Some(path) {
                return Ok(json!({ "ok": true, "entry": e }));
            }
        }
    }
    Ok(json!({ "ok": false, "reason": "path subscribed but no Entry matched in response", "raw": parsed }))
}

// --------------------------------------- Telemetry parser (port of Go)

fn parse_telemetry(raw: &Value, cfg: &Config, passthrough: &HashMap<String, String>) -> Value {
    let speed_factor = if cfg.distance_units == "imperial" { 2.23694 } else { 3.6 };
    let units = if cfg.distance_units == "imperial" { "imperial" } else { "metric" };
    let temp_units = if cfg.temperature_units.is_empty() { "celsius" } else { cfg.temperature_units.as_str() };

    let mut data: Map<String, Value> = Map::new();
    // Defaults (match the Go shape exactly so HUDs see the keys they expect).
    for (k, v) in [
        ("playerPosition", Value::Null),
        ("currentTile", Value::Null),
        ("localTime", Value::Null),
        ("speed", json!(0)),
        ("direction", json!(0)),
        ("limit", json!(0)),
        ("isSlipping", Value::Bool(false)),
        ("powerHandle", json!(0)),
        ("incline", json!(0)),
        ("nextSpeedLimit", json!(0)),
        ("distanceToNextSpeedLimit", json!(0)),
        ("trainBreak", json!(0)),
        ("trainBrakeActive", Value::Bool(false)),
        ("locomotiveBrakeHandle", json!(0)),
        ("locomotiveBrakeActive", Value::Bool(false)),
        ("electricDynamicBrake", json!(0)),
        ("electricBrakeActive", Value::Bool(false)),
        ("isTractionLocked", Value::Bool(false)),
        ("weather", json!({})),
        ("doorFrontRight", Value::Null),
        ("doorFrontLeft", Value::Null),
        ("reverser", Value::Null),
        ("distanceUnits", json!(units)),
        ("temperatureUnits", json!(temp_units)),
        ("cameraMode", Value::Null),
        ("distanceToSignal", Value::Null),
        ("signalAspectClass", Value::Null),
        ("distanceToStation", Value::Null),
        ("nextStation", Value::Null),
        ("timetableTime", Value::Null),
        ("timetableLabel", Value::Null),
    ] {
        data.insert(k.into(), v);
    }

    let Some(entries) = raw.get("Entries").and_then(|v| v.as_array()) else {
        return Value::Object(data);
    };

    for entry in entries {
        let Some(path) = entry.get("Path").and_then(|v| v.as_str()) else {
            continue;
        };

        // TEMP door diagnostic: capture every PassengerDoor entry's raw
        // NodeValid + Values BEFORE the validity gate, so we can see what TSW
        // actually returns for the door paths (the door indicator was stuck at
        // no-data). Remove once diagnosed.
        if path.contains("PassengerDoor") {
            let dbg = data.entry("_doorDebug").or_insert_with(|| Value::Array(vec![]));
            if let Some(arr) = dbg.as_array_mut() {
                arr.push(json!({
                    "path": path,
                    "nodeValid": entry.get("NodeValid").cloned().unwrap_or(Value::Null),
                    "values": entry.get("Values").cloned().unwrap_or(Value::Null),
                }));
            }
        }

        // Raw passthrough first (every non-Core entry, valid or not).
        if let Some(key) = passthrough.get(path) {
            data.insert(key.clone(), entry.clone());
        }

        let valid = entry.get("NodeValid").and_then(|v| v.as_bool()).unwrap_or(false);
        if !valid {
            continue;
        }
        let Some(values) = entry.get("Values").and_then(|v| v.as_object()) else {
            continue;
        };

        match path {
            "DriverAid.PlayerInfo" => {
                if let Some(geo) = values.get("geoLocation").and_then(|v| v.as_object()) {
                    let lat = geo.get("latitude").and_then(|v| v.as_f64()).unwrap_or(0.0);
                    let lng = geo.get("longitude").and_then(|v| v.as_f64()).unwrap_or(0.0);
                    // Stale Chatham sentinel that hud-go filters out.
                    if !(lat == 51.380108707397724 && lng == 0.5219243867730494) {
                        data.insert("playerPosition".into(), json!({"latitude": lat, "longitude": lng}));
                    }
                }
                if let Some(tile) = values.get("currentTile").and_then(|v| v.as_object()) {
                    let x = tile.get("x").and_then(|v| v.as_f64());
                    let y = tile.get("y").and_then(|v| v.as_f64());
                    if let (Some(x), Some(y)) = (x, y) {
                        data.insert("currentTile".into(), json!({"x": x as i64, "y": y as i64}));
                    }
                }
                if let Some(cm) = values.get("cameraMode").and_then(|v| v.as_str()) {
                    data.insert("cameraMode".into(), json!(cm));
                }
                if let Some(csn) = values.get("currentServiceName").and_then(|v| v.as_str()) {
                    data.insert("currentServiceName".into(), json!(csn));
                }
            }
            "DriverAid.TrackData" => {
                if let Some(stations) = values.get("stations").cloned() {
                    data.insert("_stations".into(), stations);
                }
                if let Some(markers) = values.get("markers").cloned() {
                    data.insert("_markers".into(), markers);
                }
            }
            "DriverAid.Data" => {
                if let Some(limit) = values.get("speedLimit").and_then(|v| v.as_object()) {
                    if let Some(v) = limit.get("value").and_then(|v| v.as_f64()) {
                        if v < 1e10 {
                            data.insert("limit".into(), json!((v * speed_factor).round() as i64));
                        }
                    }
                }
                if let Some(nl) = values.get("nextSpeedLimit").and_then(|v| v.as_object()) {
                    if let Some(v) = nl.get("value").and_then(|v| v.as_f64()) {
                        if v < 1e10 {
                            data.insert("nextSpeedLimit".into(), json!((v * speed_factor).round() as i64));
                        }
                    }
                }
                if let Some(d) = values.get("distanceToNextSpeedLimit").and_then(|v| v.as_f64()) {
                    data.insert("distanceToNextSpeedLimit".into(), json!(d.round() as i64));
                }
                if let Some(g) = values.get("gradient").and_then(|v| v.as_f64()) {
                    data.insert("incline".into(), json!(g));
                }
                if let Some(d) = values.get("distanceToSignal").and_then(|v| v.as_f64()) {
                    data.insert("distanceToSignal".into(), json!(d.round() as i64));
                }
                if let Some(a) = values.get("signalAspectClass").and_then(|v| v.as_str()) {
                    data.insert("signalAspectClass".into(), json!(a));
                }
            }
            "TimeOfDay.Data" => {
                if let Some(t) = values.get("LocalTimeISO8601").and_then(|v| v.as_str()) {
                    data.insert("localTime".into(), json!(t));
                }
            }
            "WeatherManager.Data" => {
                let keys = ["Temperature", "Cloudiness", "Precipitation", "Wetness", "GroundSnow", "PiledSnow", "FogDensity"];
                let mut w = Map::new();
                for k in keys {
                    if let Some(v) = values.get(k) {
                        w.insert(k.into(), v.clone());
                    }
                }
                data.insert("weather".into(), Value::Object(w));
            }
            "CurrentDrivableActor.Function.HUD_GetSpeed" => {
                if let Some(v) = values.get("Speed (ms)").and_then(|v| v.as_f64()) {
                    data.insert("speed".into(), json!((v * speed_factor).round() as i64));
                }
            }
            "CurrentDrivableActor.Function.HUD_GetDirection" => {
                if let Some(dir) = values.get("Direction").and_then(|v| v.as_f64()) {
                    data.insert("direction".into(), json!(dir));
                    let is_active = values.get("IsActive").and_then(|v| v.as_bool());
                    let reverser = match (is_active, dir) {
                        (Some(false), _) => json!(-1),     // Handle removed
                        (_, d) if d < 0.0 => json!(0),     // Reverse
                        (_, d) if d == 0.0 => json!(1),    // Neutral
                        _ => json!(2),                     // Forward
                    };
                    data.insert("reverser".into(), reverser);
                }
            }
            "CurrentDrivableActor.Function.HUD_GetPowerHandle" => {
                if let Some(v) = values.get("Power").and_then(|v| v.as_f64()) {
                    let mut rounded = if v < 0.0 { v.floor() } else { v.ceil() };
                    if values.get("IsNegative").and_then(|v| v.as_bool()).unwrap_or(false) {
                        rounded = -rounded.abs();
                    }
                    data.insert("powerHandle".into(), json!(rounded as i64));
                }
            }
            "CurrentDrivableActor.Function.HUD_GetIsSlipping" => {
                if let Some(v) = values.get("IsSlipping").and_then(|v| v.as_bool()) {
                    data.insert("isSlipping".into(), Value::Bool(v));
                }
            }
            "CurrentDrivableActor.Function.HUD_GetTrainBrakeHandle" => {
                if let Some(v) = values.get("HandlePosition").and_then(|v| v.as_f64()) {
                    data.insert("trainBreak".into(), json!((v * 100.0).round() as i64));
                }
                if let Some(v) = values.get("IsActive").and_then(|v| v.as_bool()) {
                    data.insert("trainBrakeActive".into(), Value::Bool(v));
                }
            }
            "CurrentDrivableActor.Function.HUD_GetLocomotiveBrakeHandle" => {
                if let Some(v) = values.get("HandlePosition").and_then(|v| v.as_f64()) {
                    data.insert("locomotiveBrakeHandle".into(), json!(v));
                }
                if let Some(v) = values.get("IsActive").and_then(|v| v.as_bool()) {
                    data.insert("locomotiveBrakeActive".into(), Value::Bool(v));
                }
            }
            "CurrentDrivableActor.Function.HUD_GetElectricBrakeHandle" => {
                if let Some(v) = values.get("HandlePosition").and_then(|v| v.as_f64()) {
                    data.insert("electricDynamicBrake".into(), json!((v * 100.0).round() as i64));
                }
                if let Some(v) = values.get("IsActive").and_then(|v| v.as_bool()) {
                    data.insert("electricBrakeActive".into(), Value::Bool(v));
                }
            }
            "CurrentDrivableActor.Function.HUD_GetIsTractionLocked" => {
                if let Some(v) = values.get("IsTractionLocked").and_then(|v| v.as_bool()) {
                    data.insert("isTractionLocked".into(), Value::Bool(v));
                }
            }
            "CurrentDrivableActor/PassengerDoor_FR.Function.GetCurrentInputValue" => {
                if let Some(v) = values.get("ReturnValue").and_then(|v| v.as_f64()) {
                    data.insert("doorFrontRight".into(), Value::Bool(v > 0.0));
                }
            }
            "CurrentDrivableActor/PassengerDoor_FL.Function.GetCurrentInputValue" => {
                if let Some(v) = values.get("ReturnValue").and_then(|v| v.as_f64()) {
                    data.insert("doorFrontLeft".into(), Value::Bool(v > 0.0));
                }
            }
            "CurrentFormation.FormationLength" => {
                if let Some(v) = values.get("FormationLength").and_then(|v| v.as_f64()) {
                    data.insert("vehicleCount".into(), json!(v as i64));
                }
            }
            "CurrentFormation/1/Door_PassengerDoor_BR.Function.GetCurrentOutputValue" => {
                if let Some(v) = values.get("ReturnValue").and_then(|v| v.as_f64()) {
                    if data.get("doorFrontRight").map_or(true, |v| v.is_null()) {
                        data.insert("doorFrontRight".into(), Value::Bool(v > 0.0));
                    }
                }
            }
            "CurrentFormation/1/Door_PassengerDoor_BL.Function.GetCurrentOutputValue" => {
                if let Some(v) = values.get("ReturnValue").and_then(|v| v.as_f64()) {
                    if data.get("doorFrontLeft").map_or(true, |v| v.is_null()) {
                        data.insert("doorFrontLeft".into(), Value::Bool(v > 0.0));
                    }
                }
            }
            _ => {}
        }
    }

    // Derive distance-to-next-station from DriverAid.TrackData stations. Each
    // station carries `distanceToStationCM`; the nearest positive distance is
    // the next stop. This is the game's own authoritative figure — the catalog
    // has no per-stop coordinates to compute it from (car_stop_signs lack a
    // name/FK link to timetable entries), so the overlay widgets rely on this.
    // Stored in metres to match the haversine-based path the widgets already use.
    if let Some(stations) = data.get("_stations").and_then(|v| v.as_array()) {
        let mut best_cm: Option<f64> = None;
        let mut best_name: Option<String> = None;
        for s in stations {
            let Some(obj) = s.as_object() else { continue };
            let Some(d) = obj.get("distanceToStationCM").and_then(|v| v.as_f64()) else { continue };
            if d <= 0.0 {
                continue;
            }
            if best_cm.map_or(true, |b| d < b) {
                best_cm = Some(d);
                best_name = ["stationName", "name", "StationName", "DisplayName", "displayName"]
                    .iter()
                    .find_map(|k| obj.get(*k).and_then(|v| v.as_str()))
                    .map(|s| s.to_string());
            }
        }
        if let Some(cm) = best_cm {
            data.insert("distanceToStation".into(), json!((cm / 100.0).round() as i64));
            if let Some(n) = best_name {
                data.insert("nextStation".into(), json!(n));
            }
        }
    }

    Value::Object(data)
}

// --------------------------------------------------- Connection loop

pub async fn connection_loop(telemetry: Telemetry, cfg: Config) {
    // Brief settle so TSW (if launched concurrently) can come up first.
    tokio::time::sleep(Duration::from_secs(3)).await;

    let calls = load_api_calls();
    let paths = enabled_subscription_paths(&calls);
    let passthrough = passthrough_keys(&calls);

    if paths.is_empty() {
        telemetry.set_status("api_calls.json had no enabled paths");
        eprintln!("[TSW] api_calls.json had no enabled paths; not subscribing");
        return;
    }

    loop {
        let key = resolve_api_key(&cfg);
        if key.is_empty() {
            telemetry.set_status("No API key (CommAPIKey.txt missing?)");
            tokio::time::sleep(Duration::from_secs(30)).await;
            continue;
        }

        // Smoke test the connection.
        telemetry.set_status("Connecting…");
        if let Err(e) = do_request("GET", "/subscription/?Subscription=1", None, &key).await {
            telemetry.set_status(format!("Connection failed: {e}"));
            tokio::time::sleep(Duration::from_secs(30)).await;
            continue;
        }

        telemetry.set_status("Initializing subscriptions…");
        delete_subscription(&key).await;
        tokio::time::sleep(Duration::from_millis(500)).await;
        create_subscriptions(&key, &paths).await;
        telemetry.set_status(format!("Subscribed to {} paths", paths.len()));

        // Poll every 100 ms — same cadence as hud-go.
        let mut consecutive_errors = 0;
        let mut ticker = tokio::time::interval(Duration::from_millis(100));
        loop {
            ticker.tick().await;
            match fetch_subscription_data(&key).await {
                Ok(raw) => {
                    consecutive_errors = 0;
                    // Reload config each tick so unit changes (distance /
                    // temperature) saved in Settings apply live, without a
                    // restart. The file is tiny + OS-cached, so this is cheap.
                    let live_cfg = Config::load();
                    let parsed = parse_telemetry(&raw, &live_cfg, &passthrough);
                    telemetry.set_data(Some(parsed));
                }
                Err(e) => {
                    consecutive_errors += 1;
                    if consecutive_errors <= 3 {
                        continue;
                    }
                    if consecutive_errors <= 10 {
                        telemetry.set_status(format!("Subscription dropped, recreating ({consecutive_errors} errors)"));
                        delete_subscription(&key).await;
                        tokio::time::sleep(Duration::from_millis(500)).await;
                        create_subscriptions(&key, &paths).await;
                        consecutive_errors = 0;
                        continue;
                    }
                    telemetry.set_status(format!("Connection lost: {e}"));
                    telemetry.set_data(None);
                    break;
                }
            }
        }

        tokio::time::sleep(Duration::from_secs(30)).await;
    }
}
