//! App configuration — mirrors hud-go's internal/config Config and reads/writes
//! the same `resources/configuration.json` format so the two stay compatible.

use serde::{Deserialize, Serialize};
use std::path::PathBuf;

#[derive(Serialize, Deserialize, Clone, Debug)]
#[serde(default, rename_all = "camelCase")]
pub struct Config {
    pub development_mode: bool,
    pub api_key: String,
    pub theme: String,
    pub language: String,
    pub tsw_version: String,
    pub tsw5_key_path: String,
    pub tsw6_key_path: String,
    pub distance_units: String,
    pub temperature_units: String,
    pub contributor_name: String,
    pub simplify_epsilon: f64,
    pub min_stop_duration_seconds: i64,
    pub gps_noise_radius_meters: f64,
    pub min_points_for_stop: i64,
    pub auto_stop_timeout_seconds: i64,
    pub save_frequency: i64,
    pub enable_subscriptions: bool,
    pub color_scheme: String,
    pub extractor_tsw_path: String,
    pub extractor_output_dir: String,
    pub extractor_temp_dir: String,
    pub extractor_auto_import: bool,
    pub extractor_build_timetable_maps: bool,
}

impl Default for Config {
    fn default() -> Self {
        Config {
            development_mode: false,
            api_key: String::new(),
            theme: "dark".into(),
            language: "en".into(),
            tsw_version: String::new(),
            tsw5_key_path: String::new(),
            tsw6_key_path: String::new(),
            distance_units: String::new(),
            temperature_units: String::new(),
            contributor_name: String::new(),
            simplify_epsilon: 0.0,
            min_stop_duration_seconds: 0,
            gps_noise_radius_meters: 0.0,
            min_points_for_stop: 0,
            auto_stop_timeout_seconds: 0,
            save_frequency: 0,
            enable_subscriptions: true,
            color_scheme: "default".into(),
            extractor_tsw_path: String::new(),
            extractor_output_dir: String::new(),
            extractor_temp_dir: String::new(),
            extractor_auto_import: true,
            extractor_build_timetable_maps: false,
        }
    }
}

/// Pick the resources/ directory the running binary should read/write.
///
/// hud owns its own resources/ tree now (configuration.json, api_calls.json,
/// collections/, custom_huds/, flags/, images/, db/, views/). Legacy
/// hud-rust fallback removed — letting two projects share a resources/ tree
/// hid which one had stale data and made cleanup harder.
///
///   1. `<exe parent>/resources/` — release layout next to hud.exe.
///   2. `<crate>/../resources/`    — dev layout (sibling to src-tauri/).
pub fn resources_dir() -> PathBuf {
    if let Ok(exe) = std::env::current_exe() {
        if let Some(dir) = exe.parent() {
            let r = dir.join("resources");
            if r.join("configuration.json").exists() {
                return r;
            }
        }
    }
    PathBuf::from(env!("CARGO_MANIFEST_DIR")).join("..").join("resources")
}

pub fn config_path() -> PathBuf {
    resources_dir().join("configuration.json")
}

/// Convert a path to the Windows convention (back-slashes). No-op for empty
/// strings and for the `%VAR%`-only placeholder text. Forward slashes work on
/// Windows too (PathBuf/repak accept both), so this is purely for a consistent,
/// native-looking display + stored value.
pub fn to_win_path(s: &str) -> String {
    if s.is_empty() { return String::new(); }
    s.replace('/', "\\")
}

impl Config {
    /// Normalize every filesystem-path field to back-slashes so the settings /
    /// extractor / train-classes pages all show Windows-style paths and the
    /// stored configuration.json is consistent.
    fn normalize_paths(&mut self) {
        self.tsw5_key_path       = to_win_path(&self.tsw5_key_path);
        self.tsw6_key_path       = to_win_path(&self.tsw6_key_path);
        self.extractor_tsw_path  = to_win_path(&self.extractor_tsw_path);
        self.extractor_output_dir = to_win_path(&self.extractor_output_dir);
        self.extractor_temp_dir  = to_win_path(&self.extractor_temp_dir);
    }

    pub fn load() -> Self {
        let mut cfg: Self = std::fs::read_to_string(config_path())
            .ok()
            .and_then(|s| serde_json::from_str(&s).ok())
            .unwrap_or_default();
        cfg.normalize_paths();
        cfg
    }

    pub fn save(&self) -> Result<(), String> {
        let mut cfg = self.clone();
        cfg.normalize_paths();
        let dir = resources_dir();
        std::fs::create_dir_all(&dir).map_err(|e| e.to_string())?;
        let json = serde_json::to_string_pretty(&cfg).map_err(|e| e.to_string())?;
        std::fs::write(dir.join("configuration.json"), json).map_err(|e| e.to_string())
    }
}
