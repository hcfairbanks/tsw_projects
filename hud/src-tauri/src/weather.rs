//! Real-world weather fetch + mapping. Port of
//! hud-go/internal/handler/weather_live.go and weather_historical.go.
//!
//! Two flows:
//!   - LIVE: GET https://api.open-meteo.com/v1/forecast?…&current=… — used by
//!     /api/weather/live and /api/weather/live/apply. Reads player position
//!     from the cached TSW telemetry snapshot.
//!   - HISTORICAL: GET https://archive-api.open-meteo.com/v1/archive?…date=… —
//!     used by /api/weather/historical and /api/weather/historical/apply. Picks
//!     the hourly bucket closest to the in-game time-of-day.
//!
//! Both flows produce a `TswWeatherValues` payload (matches the JSON shape
//! hud-go ships); the *apply* variants additionally PATCH each value to TSW
//! via the existing tsw::do_request raw-TCP helper.

use serde::{Deserialize, Serialize};

const FORECAST_URL: &str = "https://api.open-meteo.com/v1/forecast";
const ARCHIVE_URL: &str = "https://archive-api.open-meteo.com/v1/archive";

/// JSON shape the front-end expects from /api/weather/live + /api/weather/historical.
// `Deserialize` + `#[serde(default)]` so the Weather widget can POST partial
// payloads when the user moves a slider; missing fields stay at 0.0.
#[derive(Serialize, Deserialize, Clone, Debug, Default)]
#[serde(rename_all = "snake_case")]
pub struct TswWeatherValues {
    #[serde(default)] pub temperature: f64,
    #[serde(default)] pub cloudiness: f64,
    #[serde(default)] pub precipitation: f64,
    #[serde(default)] pub wetness: f64,
    #[serde(default)] pub ground_snow: f64,
    #[serde(default)] pub piled_snow: f64,
    #[serde(default)] pub fog_density: f64,
}

#[derive(Deserialize)]
pub struct ForecastResp {
    #[serde(default)]
    current: ForecastCurrent,
    #[serde(default)]
    hourly: ForecastHourly,
}

#[derive(Deserialize, Default)]
struct ForecastCurrent {
    #[serde(default, rename = "temperature_2m")]
    temperature_2m: f64,
    #[serde(default)]
    rain: f64,
    #[serde(default)]
    precipitation: f64,
    #[serde(default)]
    showers: f64,
    #[serde(default)]
    snowfall: f64,
    #[serde(default, rename = "cloud_cover")]
    cloud_cover: f64,
}

#[derive(Deserialize, Default)]
struct ForecastHourly {
    #[serde(default)]
    time: Vec<String>,
    #[serde(default, rename = "snow_depth")]
    snow_depth: Vec<f64>,
}

#[derive(Deserialize, Default)]
pub struct ArchiveResp {
    #[serde(default)]
    pub hourly: ArchiveHourly,
}

#[derive(Deserialize, Default)]
pub struct ArchiveHourly {
    #[serde(default)]
    pub time: Vec<String>,
    #[serde(default, rename = "temperature_2m")]
    pub temperature_2m: Vec<f64>,
    #[serde(default, rename = "snow_depth")]
    pub snow_depth: Vec<f64>,
    #[serde(default)]
    pub snowfall: Vec<f64>,
    #[serde(default)]
    pub showers: Vec<f64>,
    #[serde(default)]
    pub rain: Vec<f64>,
    #[serde(default, rename = "cloud_cover")]
    pub cloud_cover: Vec<f64>,
    #[serde(default)]
    pub precipitation: Vec<f64>,
}

fn clamp_f64(v: f64, min: f64, max: f64) -> f64 {
    v.max(min).min(max)
}

/// Read player lat/lng from the latest TSW telemetry snapshot, mirroring
/// hud-go's LiveWeatherHandler.getPlayerPosition.
pub fn player_position(snap: &serde_json::Value) -> Result<(f64, f64), String> {
    let pos = snap
        .get("playerPosition")
        .and_then(|v| v.as_object())
        .ok_or("no player position available — is the game running?")?;
    let lat = pos.get("latitude").and_then(|v| v.as_f64()).unwrap_or(0.0);
    let lng = pos.get("longitude").and_then(|v| v.as_f64()).unwrap_or(0.0);
    if lat == 0.0 && lng == 0.0 {
        return Err("player position is (0,0) — waiting for valid GPS data".into());
    }
    Ok((lat, lng))
}

/// Read the in-game hour (0-23) from the telemetry snapshot's `localTime`
/// ISO string. Returns 12 (noon) when unavailable so historical lookups still
/// pick a sensible bucket.
pub fn game_hour(snap: &serde_json::Value) -> u32 {
    snap.get("localTime")
        .and_then(|v| v.as_str())
        .and_then(|s| s.get(11..13))
        .and_then(|h| h.parse::<u32>().ok())
        .unwrap_or(12)
}

// ---- HTTP fetches ------------------------------------------------------

pub async fn fetch_live(lat: f64, lng: f64) -> Result<ForecastResp, String> {
    let url = format!(
        "{FORECAST_URL}?latitude={lat:.6}&longitude={lng:.6}\
         &hourly=temperature_2m,snow_depth,snowfall,showers,rain,precipitation_probability\
         &current=temperature_2m,rain,precipitation,showers,snowfall,cloud_cover\
         &timezone=auto&forecast_days=1"
    );
    let resp = reqwest::get(&url)
        .await
        .map_err(|e| format!("HTTP request failed: {e}"))?;
    let status = resp.status();
    if !status.is_success() {
        let body = resp.text().await.unwrap_or_default();
        return Err(format!("Open-Meteo returned {status}: {body}"));
    }
    resp.json::<ForecastResp>()
        .await
        .map_err(|e| format!("parse forecast: {e}"))
}

pub async fn fetch_archive(lat: f64, lng: f64, date: &str) -> Result<ArchiveResp, String> {
    let url = format!(
        "{ARCHIVE_URL}?latitude={lat:.6}&longitude={lng:.6}\
         &start_date={date}&end_date={date}\
         &hourly=temperature_2m,snow_depth,snowfall,showers,rain,cloud_cover,precipitation\
         &timezone=auto"
    );
    let resp = reqwest::get(&url)
        .await
        .map_err(|e| format!("HTTP request failed: {e}"))?;
    let status = resp.status();
    if !status.is_success() {
        let body = resp.text().await.unwrap_or_default();
        return Err(format!("Open-Meteo archive returned {status}: {body}"));
    }
    resp.json::<ArchiveResp>()
        .await
        .map_err(|e| format!("parse archive: {e}"))
}

// ---- Open-Meteo → TSW mapping ------------------------------------------

/// Maps the current-conditions block from /forecast to TSW weather values.
/// Logic mirrors hud-go's mapToTSW.
pub fn map_live_to_tsw(resp: &ForecastResp) -> TswWeatherValues {
    let cur = &resp.current;
    let mut temperature = cur.temperature_2m;
    let is_snowing = cur.snowfall > 0.0;
    let is_raining = cur.rain > 0.0 || cur.showers > 0.0;

    // TSW only renders precipitation as snow when temp <= 0C — cap if snowing.
    if is_snowing && temperature > -0.5 {
        temperature = -0.5;
    }

    let cloudiness = clamp_f64(cur.cloud_cover / 100.0, 0.0, 1.0);

    let precipitation = if is_snowing && !is_raining {
        clamp_f64(cur.snowfall / 2.0, 0.0, 1.0)
    } else if is_raining && !is_snowing {
        clamp_f64((cur.rain + cur.showers) / 5.0, 0.0, 1.0)
    } else if is_snowing && is_raining {
        clamp_f64(cur.precipitation / 5.0, 0.0, 1.0)
    } else {
        0.0
    };

    let wetness = clamp_f64((cur.rain + cur.showers) / 3.0, 0.0, 1.0);

    let snow_depth = closest_hourly_snow_depth(resp);
    let mut ground_snow = clamp_f64((snow_depth / 0.10) * 3.0, 0.0, 1.0);
    if is_snowing && ground_snow < 0.15 {
        ground_snow = 0.15;
    }
    let piled_snow = ground_snow;

    let fog_density = if cloudiness > 0.9 && precipitation > 0.0 { 0.1 } else { 0.0 };

    round_two_decimal(TswWeatherValues {
        temperature,
        cloudiness,
        precipitation,
        wetness,
        ground_snow,
        piled_snow,
        fog_density,
    })
}

/// Picks the hourly bucket closest to the in-game hour and produces the TSW
/// mapping for it. Same rules as map_live_to_tsw, applied to the hourly arrays.
pub fn map_archive_to_tsw(archive: &ArchiveResp, idx: usize) -> TswWeatherValues {
    let h = &archive.hourly;
    if idx >= h.time.len() {
        return TswWeatherValues::default();
    }
    let temp = h.temperature_2m.get(idx).copied().unwrap_or(0.0);
    let rain = h.rain.get(idx).copied().unwrap_or(0.0);
    let showers = h.showers.get(idx).copied().unwrap_or(0.0);
    let snowfall = h.snowfall.get(idx).copied().unwrap_or(0.0);
    let cloud_cover = h.cloud_cover.get(idx).copied().unwrap_or(0.0);
    let precip = h.precipitation.get(idx).copied().unwrap_or(0.0);
    let snow_depth = h.snow_depth.get(idx).copied().unwrap_or(0.0);

    let mut temperature = temp;
    let is_snowing = snowfall > 0.0;
    let is_raining = rain > 0.0 || showers > 0.0;
    if is_snowing && temperature > -0.5 {
        temperature = -0.5;
    }
    let cloudiness = clamp_f64(cloud_cover / 100.0, 0.0, 1.0);
    let precipitation = if is_snowing && !is_raining {
        clamp_f64(snowfall / 2.0, 0.0, 1.0)
    } else if is_raining && !is_snowing {
        clamp_f64((rain + showers) / 5.0, 0.0, 1.0)
    } else if is_snowing && is_raining {
        clamp_f64(precip / 5.0, 0.0, 1.0)
    } else {
        0.0
    };
    let wetness = clamp_f64((rain + showers) / 3.0, 0.0, 1.0);
    let mut ground_snow = clamp_f64((snow_depth / 0.10) * 3.0, 0.0, 1.0);
    if is_snowing && ground_snow < 0.15 {
        ground_snow = 0.15;
    }
    let piled_snow = ground_snow;
    let fog_density = if cloudiness > 0.9 && precipitation > 0.0 { 0.1 } else { 0.0 };
    round_two_decimal(TswWeatherValues {
        temperature,
        cloudiness,
        precipitation,
        wetness,
        ground_snow,
        piled_snow,
        fog_density,
    })
}

/// Find the index of the hourly time bucket closest to `target_hour` (0-23).
pub fn closest_hour_index(archive: &ArchiveResp, target_hour: u32) -> usize {
    let mut best_idx = 0usize;
    let mut best_diff = i32::MAX;
    for (i, ts) in archive.hourly.time.iter().enumerate() {
        let Some(hh) = ts.get(11..13).and_then(|h| h.parse::<i32>().ok()) else { continue };
        let mut d = (hh - target_hour as i32).abs();
        // Wrap-around (handles 23 vs 1 = 2 not 22).
        if d > 12 { d = 24 - d; }
        if d < best_diff {
            best_diff = d;
            best_idx = i;
        }
    }
    best_idx
}

fn closest_hourly_snow_depth(resp: &ForecastResp) -> f64 {
    let h = &resp.hourly;
    if h.time.is_empty() || h.snow_depth.is_empty() {
        return 0.0;
    }
    let now_hour = chrono::Local::now().format("%H").to_string().parse::<i32>().unwrap_or(12);
    let mut best_idx = 0usize;
    let mut best_diff = i32::MAX;
    for (i, ts) in h.time.iter().enumerate() {
        let Some(hh) = ts.get(11..13).and_then(|h| h.parse::<i32>().ok()) else { continue };
        let mut d = (hh - now_hour).abs();
        if d > 12 { d = 24 - d; }
        if d < best_diff {
            best_diff = d;
            best_idx = i;
        }
    }
    h.snow_depth.get(best_idx).copied().unwrap_or(0.0)
}

fn round_two_decimal(mut v: TswWeatherValues) -> TswWeatherValues {
    v.temperature = (v.temperature * 10.0).round() / 10.0;
    v.cloudiness = (v.cloudiness * 100.0).round() / 100.0;
    v.precipitation = (v.precipitation * 100.0).round() / 100.0;
    v.wetness = (v.wetness * 100.0).round() / 100.0;
    v.ground_snow = (v.ground_snow * 100.0).round() / 100.0;
    v.piled_snow = (v.piled_snow * 100.0).round() / 100.0;
    v.fog_density = (v.fog_density * 100.0).round() / 100.0;
    v
}

/// Helper used by /api/weather/*/apply: PATCH each weather value to TSW.
/// Returns (applied_count, total_params).
pub async fn apply_to_tsw(api_key: &str, w: &TswWeatherValues) -> (usize, usize) {
    let params: [(&str, f64); 7] = [
        ("Temperature", w.temperature),
        ("Cloudiness", w.cloudiness),
        ("Precipitation", w.precipitation),
        ("Wetness", w.wetness),
        ("GroundSnow", w.ground_snow),
        ("PiledSnow", w.piled_snow),
        ("FogDensity", w.fog_density),
    ];
    let total = params.len();
    let mut applied = 0;
    for (key, val) in params {
        let path = format!("/set/WeatherManager.{key}?value={val}");
        if crate::tsw::do_request("PATCH", &path, None, api_key).await.is_ok() {
            applied += 1;
        }
    }
    (applied, total)
}
