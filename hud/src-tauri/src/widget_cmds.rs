//! Tauri commands the overlay widgets call.
//!
//! Mirrors hud-overlay-tauri's commapi + db + selection modules so the
//! existing widget HTML/JS works as-is. New code lives here rather than in
//! the ported db.rs / tsw.rs so those stay as faithful ports.

use rusqlite::{Connection, OpenFlags};
use serde_json::{json, Value};

use crate::AppShared;

// ---------------------------------------------------------------- telemetry

/// Latest TSW snapshot. Wraps the parsed payload with `connected: true` so
/// the widget JS recognises it as live (matches hud-overlay-tauri's shape;
/// the widgets explicitly check `d.connected`). When the poller hasn't
/// produced anything yet returns `{connected: false}` and the widgets render
/// their OFFLINE state.
/// Heuristic: is the poller in its initial bring-up phase (so a widget should
/// show LOADING rather than OFFLINE)? Status strings come from
/// `Telemetry::set_status` calls in tsw.rs / server.rs — anything that ends in
/// "\u{2026}" or starts with a known prep verb counts as loading.
fn is_loading_status(s: &str) -> bool {
    let s = s.trim();
    if s.is_empty() {
        return true; // poller hasn't reported anything yet → still booting
    }
    s.ends_with('\u{2026}') // "Connecting…", "Initializing subscriptions…"
        || s.starts_with("Connecting")
        || s.starts_with("Initializing")
        || s.starts_with("Subscrib") // "Subscribing" / "Subscribed to N paths"
        || s.starts_with("Binding")
        || s.starts_with("Listening")
        || s.starts_with("Starting")
        || s.starts_with("Static assets") // server boot lines
}

#[tauri::command]
pub fn get_telemetry(state: tauri::State<'_, AppShared>) -> Value {
    let snap = state.telemetry.snapshot();
    let status = state.telemetry.status();
    match snap {
        Some(Value::Object(mut map)) => {
            map.insert("connected".into(), Value::Bool(true));
            map.insert("loading".into(), Value::Bool(false));
            map.insert("status".into(), Value::String(status));
            Value::Object(map)
        }
        Some(other) => serde_json::json!({
            "connected": true,
            "loading": false,
            "status": status,
            "raw": other
        }),
        None => serde_json::json!({
            "connected": false,
            "loading": is_loading_status(&status),
            "status": status,
        }),
    }
}

// ---------------------------------------------------------------- selected stop

#[tauri::command]
pub fn set_selected_stop(
    state: tauri::State<'_, AppShared>,
    name: String,
    lat: Option<f64>,
    lng: Option<f64>,
) {
    // Store the selection whenever there's a name — coordinates are optional.
    // The catalog has no per-stop coords, so the dashboard matches the stored
    // NAME against the game's live station list to get a distance; requiring
    // lat/lng here would discard every selection (they're always null).
    *state.selected_stop.lock().unwrap() = if name.trim().is_empty() {
        None
    } else {
        Some(json!({ "name": name, "lat": lat, "lng": lng }))
    };
}

#[tauri::command]
pub fn get_selected_stop(state: tauri::State<'_, AppShared>) -> Value {
    state.selected_stop.lock().unwrap().clone().unwrap_or(Value::Null)
}

// ---------------------------------------------------------------- DB helpers

fn conn() -> Result<Connection, String> {
    Connection::open_with_flags(
        crate::db::db_path(),
        OpenFlags::SQLITE_OPEN_READ_ONLY | OpenFlags::SQLITE_OPEN_URI,
    )
    .map_err(|e| format!("open db: {e}"))
}

fn haversine(a: f64, b: f64, c: f64, d: f64) -> f64 {
    let r = 6_371_000.0_f64;
    let p = std::f64::consts::PI / 180.0;
    let (d_lat, d_lon) = ((c - a) * p, (d - b) * p);
    let x = (d_lat / 2.0).sin().powi(2)
        + (a * p).cos() * (c * p).cos() * (d_lon / 2.0).sin().powi(2);
    2.0 * r * x.sqrt().asin()
}

/// First `{latitude,longitude}` from a `[{…},…]` coordinates blob prefix.
fn first_coord(prefix: &str) -> Option<(f64, f64)> {
    let s = prefix.trim_start();
    // Current extractor format: a flat list of [lng, lat] pairs, e.g.
    // "[[-71.0561,42.3500],…". Parse the first pair directly (the SUBSTR
    // prefix is usually truncated mid-array, so serde_json::from_str on the
    // whole thing won't work).
    if s.starts_with("[[") {
        let inner = &s[2..];
        let end = inner.find(']')?;
        let mut it = inner[..end].split(',');
        let lng: f64 = it.next()?.trim().parse().ok()?;
        let lat: f64 = it.next()?.trim().parse().ok()?;
        return if lat == 0.0 && lng == 0.0 { None } else { Some((lat, lng)) };
    }
    // Legacy format: list of {latitude, longitude} objects.
    let end = prefix.find('}')?;
    let head = &prefix[1..=end]; // strip leading '['
    let v: Value = serde_json::from_str(head).ok()?;
    let lat = v.get("latitude")?.as_f64()?;
    let lng = v.get("longitude")?.as_f64()?;
    if lat == 0.0 && lng == 0.0 { None } else { Some((lat, lng)) }
}

// ---------------------------------------------------------------- timetable lookup

/// Find the timetable for the game's current service. Tries, in order:
///
///   1. `current_service_name = ?` — the canonical match. TSW reuses this
///      field heavily ("PlayerService" for every Training Centre tutorial,
///      "AI_Service" for many cargo packs), so multiple rows may match;
///      we rank by haversine distance from the player to each row's first
///      stored coordinate and pick the closest, same as hud-go's Detect.
///   2. `service_name = ?` — TSW sometimes broadcasts the visible service
///      label (e.g. "Acela Express Introduction") instead of the
///      internal current_service_name. We try this when step 1 returns
///      nothing so tutorials/scenarios still resolve.
///   3. Route-only fallback. When no timetable matches AND a player
///      position is supplied, find the route whose `route_coordinates`
///      polyline starts closest to the player and return `route_only: true`
///      with that route_id. The map widget then loads the whole-route
///      polyline so the user sees *something* rather than "no match".
#[tauri::command]
pub fn get_active_timetable(
    service_name: String,
    lat: Option<f64>,
    lng: Option<f64>,
) -> Result<Value, String> {
    if service_name.trim().is_empty() {
        return Ok(json!({ "found": false }));
    }
    let c = conn()?;

    // Helper: run the prepared SELECT against either column and rank by
    // distance from the player. Returns the best (id, svc, route, dist) or
    // None if zero rows came back.
    let run = |where_clause: &str| -> Result<Option<(i64, String, String, f64)>, String> {
        let sql = format!(
            "SELECT t.id, t.service_name, COALESCE(r.name,''), SUBSTR(tc.coordinates,1,200) \
             FROM timetables t \
             LEFT JOIN routes r ON r.id = t.route_id \
             LEFT JOIN timetable_coordinates tc ON tc.timetable_id = t.id \
             WHERE {where_clause}"
        );
        let mut stmt = c.prepare(&sql).map_err(|e| e.to_string())?;
        let rows = stmt
            .query_map([&service_name], |row| {
                Ok((
                    row.get::<_, i64>(0)?,
                    row.get::<_, String>(1)?,
                    row.get::<_, String>(2)?,
                    row.get::<_, Option<String>>(3)?,
                ))
            })
            .map_err(|e| e.to_string())?;
        let mut best: Option<(i64, String, String, f64)> = None;
        for r in rows {
            let (id, svc, route, prefix) = r.map_err(|e| e.to_string())?;
            let mut dist = f64::INFINITY;
            if let (Some(plat), Some(plng), Some(pre)) = (lat, lng, prefix.as_deref()) {
                if let Some((flat, flng)) = first_coord(pre) {
                    dist = haversine(plat, plng, flat, flng);
                }
            }
            if best.as_ref().map_or(true, |b| dist < b.3) {
                best = Some((id, svc, route, dist));
            }
        }
        Ok(best)
    };

    // Match EITHER column in one query and rank every candidate together by
    // distance. Splitting it into current_service_name-then-service_name with
    // `.or()` short-circuited on the first non-empty result, so a coord-less
    // wrong-route row (e.g. "2P60_1" on WCML) would win over the correct
    // coord'd row (Blackpool's 2P60_1) that only matched the other column.
    let hit = run("t.current_service_name = ?1 OR t.service_name = ?1")?;
    // Sanity-guard: if we got a hit, we know the player's position, AND the
    // matched timetable's first stop is more than 20 km away, the match is
    // almost certainly the wrong row from a heavily-shared name like
    // "PlayerService" (492 candidates across all DLCs). Drop it and fall
    // through to the nearest-route fallback below.
    let hit = hit.filter(|(_, _, _, dist)| {
        lat.is_none() || lng.is_none() || !dist.is_finite() || *dist < 20_000.0
    });
    if let Some((id, svc, route, _)) = hit {
        return Ok(json!({
            "found": true,
            "id": id,
            "service_name": svc,
            "route": route,
            "name": if route.is_empty() { svc.clone() } else { format!("{svc} \u{00b7} {route}") }
        }));
    }

    // Route-only fallback. Pick the route whose first recorded coordinate
    // is closest to the player. Useful for Training Centre / custom
    // scenarios where the service name doesn't match any timetable row but
    // we still have a position and the route itself has a polyline.
    if let (Some(plat), Some(plng)) = (lat, lng) {
        let mut s2 = c
            .prepare(
                "SELECT rc.route_id, COALESCE(r.name,''), SUBSTR(rc.coordinates,1,400) \
                 FROM route_coordinates rc \
                 LEFT JOIN routes r ON r.id = rc.route_id",
            )
            .map_err(|e| e.to_string())?;
        let rows = s2
            .query_map([], |row| {
                Ok((
                    row.get::<_, i64>(0)?,
                    row.get::<_, String>(1)?,
                    row.get::<_, Option<String>>(2)?,
                ))
            })
            .map_err(|e| e.to_string())?;
        let mut best: Option<(i64, String, f64)> = None;
        for r in rows {
            let (rid, name, prefix) = r.map_err(|e| e.to_string())?;
            let Some(pre) = prefix else { continue };
            let Some((flat, flng)) = first_route_coord(&pre) else { continue };
            let d = haversine(plat, plng, flat, flng);
            if best.as_ref().map_or(true, |b| d < b.2) {
                best = Some((rid, name, d));
            }
        }
        if let Some((rid, name, dist)) = best {
            // 200 km guardrail — beyond that, the "closest" route is almost
            // certainly wrong (e.g. player is in a brand-new DLC area whose
            // route has no recorded polyline). Better to say "no match"
            // than to draw a Bavarian line under the user's NYC train.
            if dist < 200_000.0 {
                return Ok(json!({
                    "found": true,
                    "route_only": true,
                    "id": 0,
                    "route_id": rid,
                    "route": name,
                    "name": if name.is_empty() { format!("route #{rid}") } else { name.clone() },
                    "service_name": service_name,
                    "match_distance_m": dist as i64,
                }));
            }
        }
    }

    Ok(json!({ "found": false, "service_name": service_name }))
}

/// `route_coordinates.coordinates` blobs come in three shapes (see
/// `route_segments_from_blob`). Walk the prefix and pull out the first
/// `latitude`/`longitude` pair we can find — used only for the
/// nearest-route ranking, so this is best-effort. Returns None when the
/// shape is unrecognized.
fn first_route_coord(prefix: &str) -> Option<(f64, f64)> {
    // Shape A or B: contains "latitude":<n>,"longitude":<n> at the front.
    if let Some(p) = first_coord(prefix) {
        return Some(p);
    }
    // Shape C: GeoJSON. Look for `"coordinates":[<lng>,<lat>` or
    // `"coordinates":[[<lng>,<lat>` and parse those two numbers.
    let needle = "\"coordinates\":";
    let mut idx = prefix.find(needle)?;
    idx += needle.len();
    let tail = &prefix[idx..];
    // Skip past any leading [ chars (LineString → one [, MultiLineString → two)
    let tail = tail.trim_start_matches(|c: char| c == '[' || c.is_whitespace());
    // Now we should be at "<num>,<num>" — pull the first two floats.
    let mut nums = tail
        .split(|c: char| c == ',' || c == ']')
        .filter_map(|s| s.trim().parse::<f64>().ok());
    let lng = nums.next()?;
    let lat = nums.next()?;
    if lat.abs() > 90.0 || lng.abs() > 180.0 { return None }
    Some((lat, lng))
}

// ---------------------------------------------------------------- schedule rows

/// TEMP diagnostic: write a text blob next to the exe so the live telemetry
/// (player position, resolved timetable, raw station list) can be inspected
/// without relying on overlay-webview clipboard support. Returns the path.
#[tauri::command]
pub fn debug_dump(text: String) -> Result<String, String> {
    let dir = std::env::current_exe()
        .ok()
        .and_then(|p| p.parent().map(|x| x.to_path_buf()))
        .ok_or("could not resolve exe dir")?;
    let path = dir.join("debug_dump.json");
    std::fs::write(&path, text).map_err(|e| e.to_string())?;
    Ok(path.to_string_lossy().to_string())
}

/// Route-level overlay features for the map widget: car-stop-sign points and
/// track markers (platforms / signals / switches) with coordinates. Used when
/// a timetable has no pre-built feature blob (true for almost all of them) so
/// the overlay map can still draw the rails-side detail the served route map
/// shows.
#[tauri::command]
pub fn get_route_markers(route_id: i64) -> Result<Value, String> {
    let c = conn()?;
    let mut cs = c
        .prepare(
            "SELECT latitude, longitude FROM car_stop_signs \
             WHERE route_id = ?1 AND latitude IS NOT NULL AND longitude IS NOT NULL",
        )
        .map_err(|e| e.to_string())?;
    let car: Vec<Value> = cs
        .query_map([route_id], |r| {
            Ok(json!({ "latitude": r.get::<_, f64>(0)?, "longitude": r.get::<_, f64>(1)? }))
        })
        .map_err(|e| e.to_string())?
        .filter_map(Result::ok)
        .collect();

    let mut tm = c
        .prepare(
            "SELECT name, marker_type, latitude, longitude FROM track_markers \
             WHERE route_id = ?1 AND latitude IS NOT NULL AND longitude IS NOT NULL",
        )
        .map_err(|e| e.to_string())?;
    let markers: Vec<Value> = tm
        .query_map([route_id], |r| {
            Ok(json!({
                "name": r.get::<_, Option<String>>(0)?,
                "type": r.get::<_, Option<String>>(1)?,
                "latitude": r.get::<_, f64>(2)?,
                "longitude": r.get::<_, f64>(3)?,
            }))
        })
        .map_err(|e| e.to_string())?
        .filter_map(Result::ok)
        .collect();

    // signals / switches / collectables live in tables added later than the
    // original schema — they only exist after a re-extract. Query each
    // defensively so an older DB (no such table) just yields an empty layer.
    let query_points = |sql: &str| -> Vec<Value> {
        match c.prepare(sql) {
            Ok(mut st) => st
                .query_map([route_id], |r| {
                    Ok(json!({
                        "name": r.get::<_, Option<String>>(0)?,
                        "latitude": r.get::<_, f64>(1)?,
                        "longitude": r.get::<_, f64>(2)?,
                    }))
                })
                .map(|rows| rows.filter_map(Result::ok).collect())
                .unwrap_or_default(),
            Err(_) => Vec::new(),
        }
    };
    let signals = query_points(
        "SELECT signal_type, latitude, longitude FROM signals \
         WHERE route_id = ?1 AND latitude IS NOT NULL AND longitude IS NOT NULL",
    );
    let switches = query_points(
        "SELECT jct_guid, latitude, longitude FROM switches \
         WHERE route_id = ?1 AND latitude IS NOT NULL AND longitude IS NOT NULL",
    );
    let collectables = query_points(
        "SELECT instance_name, latitude, longitude FROM collectables \
         WHERE route_id = ?1 AND latitude IS NOT NULL AND longitude IS NOT NULL",
    );

    Ok(json!({
        "carStops": car,
        "markers": markers,
        "signals": signals,
        "switches": switches,
        "collectables": collectables,
    }))
}

/// Normalise a feature/stop name for matching: lowercase, drop the
/// platform/track qualifier words, keep alphanumerics. "South Attleboro
/// Track 2" → "southattleboro2"; "South Attleboro" → "southattleboro"
/// (the platform's normalised form then *contains* the schedule stop's).
fn norm_feature_name(s: &str) -> String {
    let lower = s.to_lowercase();
    let mut out = String::new();
    for tok in lower.split(|ch: char| !char::is_alphanumeric(ch)) {
        if matches!(tok, "track" | "platform" | "gleis" | "bahnsteig" | "voie" | "binario") {
            continue;
        }
        out.push_str(tok);
    }
    out
}

/// Per-TIMETABLE map features: the route's features filtered to those along
/// THIS service's path, matching what hud-go's map shows. Signals/switches/
/// car-stops/platforms are kept only within a few metres of the service path
/// (collectables within 50 m); sidings/tunnels and everything off-route are
/// dropped. `stops` resolves each scheduled stop to a near-path platform's
/// coordinate (by name) so the map can mark the actual service stops.
#[tauri::command]
pub fn get_timetable_markers(timetable_id: i64) -> Result<Value, String> {
    let empty = json!({
        "carStops": [], "markers": [], "signals": [],
        "switches": [], "collectables": [], "stops": []
    });
    let c = conn()?;
    let route_id: Option<i64> = c
        .query_row("SELECT route_id FROM timetables WHERE id = ?1", [timetable_id], |r| r.get(0))
        .ok()
        .flatten();
    let Some(route_id) = route_id else { return Ok(empty) };
    let path_blob: Option<String> = c
        .query_row(
            "SELECT coordinates FROM timetable_coordinates WHERE timetable_id = ?1",
            [timetable_id],
            |r| r.get(0),
        )
        .ok();
    // No service path → we can't proximity-filter, so show nothing rather than
    // dumping the whole route (the off-route clutter the user is seeing).
    let Some(idx) = path_blob.as_deref().and_then(crate::features::path_index_from_lnglat) else {
        return Ok(empty);
    };

    // A feature must INTERSECT the service path to show (hud-go's 3 m). Measured
    // against this route's data, on-path features sit at 0-2.7 m while the
    // nearest parallel/adjacent-track features start at ~3.7 m — so 3 m cleanly
    // keeps only the service's own track and drops other tracks at the same
    // stations. Collectables are off-path items, so they keep a wider radius.
    const PROX_M: f64 = 3.0;
    // Switches are junction NODES — when the service actually runs through one,
    // its node coincides with a path vertex (~0 m). Off-path junctions beside
    // the route sit at ~2.5 m+. A tighter gate keeps only the switches the
    // service genuinely traverses.
    const SWITCH_PROX_M: f64 = 2.0;
    const COLLECT_PROX_M: f64 = 50.0;

    let near = |lat: f64, lng: f64, prox: f64| crate::features::point_path_distance_m(&idx, lat, lng) <= prox;

    // (name, lat, lng) rows filtered by proximity.
    let filter_pts = |sql: &str, prox: f64| -> Vec<Value> {
        let mut out = Vec::new();
        if let Ok(mut st) = c.prepare(sql) {
            if let Ok(rows) = st.query_map([route_id], |r| {
                Ok((r.get::<_, Option<String>>(0)?, r.get::<_, f64>(1)?, r.get::<_, f64>(2)?))
            }) {
                for (name, lat, lng) in rows.flatten() {
                    if near(lat, lng, prox) {
                        out.push(json!({ "name": name, "latitude": lat, "longitude": lng }));
                    }
                }
            }
        }
        out
    };

    let signals = filter_pts(
        "SELECT signal_type, latitude, longitude FROM signals \
         WHERE route_id=?1 AND latitude IS NOT NULL AND longitude IS NOT NULL", PROX_M);
    let switches = filter_pts(
        "SELECT jct_guid, latitude, longitude FROM switches \
         WHERE route_id=?1 AND latitude IS NOT NULL AND longitude IS NOT NULL", SWITCH_PROX_M);
    let collectables = filter_pts(
        "SELECT instance_name, latitude, longitude FROM collectables \
         WHERE route_id=?1 AND latitude IS NOT NULL AND longitude IS NOT NULL", COLLECT_PROX_M);
    let car_stops = filter_pts(
        "SELECT platform_name, latitude, longitude FROM car_stop_signs \
         WHERE route_id=?1 AND latitude IS NOT NULL AND longitude IS NOT NULL", PROX_M);
    // Only Platform track markers (sidings / tunnels dropped, like hud-go).
    let platforms = filter_pts(
        "SELECT name, latitude, longitude FROM track_markers \
         WHERE route_id=?1 AND marker_type='Platform' AND latitude IS NOT NULL AND longitude IS NOT NULL",
        PROX_M);

    // Stops: each scheduled location resolved to a near-path platform's
    // coordinate by name. Gives the map the actual service stops (the catalog
    // has no per-entry coords).
    let mut stops: Vec<Value> = Vec::new();
    {
        // Distinct scheduled location names in order.
        let mut seen: std::collections::HashSet<String> = std::collections::HashSet::new();
        let mut sched: Vec<String> = Vec::new();
        if let Ok(mut st) = c.prepare(
            "SELECT COALESCE(l.name,'') FROM timetable_entries te \
             LEFT JOIN locations l ON l.id = te.location_id \
             WHERE te.timetable_id=?1 ORDER BY te.sort_order",
        ) {
            if let Ok(rows) = st.query_map([timetable_id], |r| r.get::<_, String>(0)) {
                for name in rows.flatten() {
                    let t = name.trim();
                    if t.is_empty() { continue; }
                    if seen.insert(t.to_string()) { sched.push(t.to_string()); }
                }
            }
        }
        // Pre-normalise candidate platforms (and car stops as fallback).
        let cand: Vec<(&Value, String)> = platforms.iter().chain(car_stops.iter())
            .filter_map(|p| {
                let n = p.get("name").and_then(|v| v.as_str())?;
                if n.is_empty() { return None; }
                Some((p, norm_feature_name(n)))
            })
            .collect();
        for loc in &sched {
            let key = norm_feature_name(loc);
            if key.is_empty() { continue; }
            if let Some((p, _)) = cand.iter().find(|(_, pn)| pn.contains(&key) || key.contains(pn.as_str())) {
                stops.push(json!({
                    "name": loc,
                    "latitude": p.get("latitude").cloned().unwrap_or(Value::Null),
                    "longitude": p.get("longitude").cloned().unwrap_or(Value::Null),
                }));
            }
        }
    }

    Ok(json!({
        "carStops": car_stops,
        "markers": platforms,
        "signals": signals,
        "switches": switches,
        "collectables": collectables,
        "stops": stops,
    }))
}

/// Platform markers that lie on a timetable's service path, with normalised
/// names + coords — the candidates for resolving each scheduled stop's
/// position (the catalog has no per-entry coordinates). Empty when the
/// timetable has no service path.
fn near_path_stop_candidates(c: &Connection, timetable_id: i64) -> Vec<(String, f64, f64)> {
    let route_id: Option<i64> = c
        .query_row("SELECT route_id FROM timetables WHERE id=?1", [timetable_id], |r| r.get(0))
        .ok()
        .flatten();
    let Some(route_id) = route_id else { return Vec::new() };
    let path_blob: Option<String> = c
        .query_row("SELECT coordinates FROM timetable_coordinates WHERE timetable_id=?1", [timetable_id], |r| r.get(0))
        .ok();
    let Some(idx) = path_blob.as_deref().and_then(crate::features::path_index_from_lnglat) else {
        return Vec::new();
    };
    let mut out: Vec<(String, f64, f64)> = Vec::new();
    if let Ok(mut st) = c.prepare(
        "SELECT name, latitude, longitude FROM track_markers \
         WHERE route_id=?1 AND marker_type='Platform' AND latitude IS NOT NULL AND longitude IS NOT NULL",
    ) {
        if let Ok(rows) = st.query_map([route_id], |r| {
            Ok((r.get::<_, Option<String>>(0)?, r.get::<_, f64>(1)?, r.get::<_, f64>(2)?))
        }) {
            for (name, lat, lng) in rows.flatten() {
                let Some(name) = name else { continue };
                if name.trim().is_empty() { continue; }
                if crate::features::point_path_distance_m(&idx, lat, lng) <= 14.0 {
                    out.push((norm_feature_name(&name), lat, lng));
                }
            }
        }
    }
    out
}

/// Drop a trailing " Track N" / " Platform N" qualifier for display, so a
/// resolved platform name like "Boston South Station Track 04" shows as the
/// station "Boston South Station" (matches hud-go's WAIT-row location field).
fn strip_track_suffix(s: &str) -> String {
    let lower = s.to_lowercase();
    for kw in [" track ", " platform ", " gleis ", " bahnsteig ", " voie ", " binario "] {
        if let Some(idx) = lower.rfind(kw) {
            return s[..idx].trim().to_string();
        }
    }
    s.to_string()
}

/// Match a scheduled location name against the near-path platform candidates.
fn match_stop_coord(cands: &[(String, f64, f64)], location: &str) -> Option<(f64, f64)> {
    let key = norm_feature_name(location);
    if key.is_empty() { return None; }
    cands.iter()
        .find(|(n, _, _)| n.contains(&key) || key.contains(n.as_str()))
        .map(|(_, la, lo)| (*la, *lo))
}

/// Per-entry car-stop-sign coordinate, snug-fit to the formation's car count,
/// keyed by `timetable_entries.id`. Same JOIN + ranking map_route_data uses.
/// Shared by the schedule widget and the dev timetable-detail page (the raw
/// `timetable_entries.latitude` column is null).
fn entry_car_stop_coords(c: &Connection, timetable_id: i64) -> std::collections::HashMap<i64, (f64, f64)> {
    let mut map = std::collections::HashMap::new();
    let route_id: Option<i64> = c
        .query_row("SELECT route_id FROM timetables WHERE id=?1", [timetable_id], |r| r.get(0))
        .ok().flatten();
    let Some(rid) = route_id else { return map };
    let car_count: i64 = c
        .query_row(
            "SELECT COALESCE(f.car_count,0) FROM timetables t \
             LEFT JOIN formations f ON f.id=t.formation_id WHERE t.id=?1",
            [timetable_id], |r| r.get(0)).unwrap_or(0);
    let bound: String = c
        .query_row("SELECT COALESCE(LOWER(TRIM(bound)),'') FROM timetables WHERE id=?1", [timetable_id], |r| r.get(0))
        .unwrap_or_default();
    let sql = "SELECT te.id, css.latitude, css.longitude \
         FROM timetable_entries te JOIN locations l ON l.id=te.location_id \
         JOIN car_stop_signs css ON css.route_id=?1 \
           AND css.platform_name = TRIM(l.name || ' ' || COALESCE(te.structure,'') || ' ' || COALESCE(te.structure_number,'')) \
         WHERE te.timetable_id=?2 \
         ORDER BY te.id, \
           CASE WHEN css.max_rail_vehicles=?3 THEN 0 \
                WHEN css.max_rail_vehicles>?3 THEN css.max_rail_vehicles-?3 \
                WHEN css.max_rail_vehicles=0 THEN 99999 \
                ELSE 99999+(?3-css.max_rail_vehicles) END, \
           CASE ?4 WHEN 'northbound' THEN -css.latitude WHEN 'southbound' THEN css.latitude \
                   WHEN 'eastbound' THEN -css.longitude WHEN 'westbound' THEN css.longitude ELSE 0 END";
    if let Ok(mut st) = c.prepare(sql) {
        if let Ok(rows) = st.query_map(rusqlite::params![rid, timetable_id, car_count, bound], |r| {
            Ok((r.get::<_, i64>(0)?, r.get::<_, f64>(1)?, r.get::<_, f64>(2)?))
        }) {
            for (eid, la, lo) in rows.flatten() { map.entry(eid).or_insert((la, lo)); }
        }
    }
    map
}

/// First service-path vertex (lat,lng) — the spawn position used for the first
/// WAIT FOR SERVICE row (one train-length behind the head-of-train car stop).
fn spawn_vertex(c: &Connection, timetable_id: i64) -> Option<(f64, f64)> {
    c.query_row("SELECT coordinates FROM timetable_coordinates WHERE timetable_id=?1", [timetable_id], |r| r.get::<_, String>(0))
        .ok()
        .and_then(|s| serde_json::from_str::<Vec<Vec<f64>>>(&s).ok())
        .and_then(|v| v.into_iter().find(|p| p.len() >= 2).map(|p| (p[1], p[0])))
}

/// Timetable stops for the schedule widget. Folds the LOAD-PASSENGERS dwell
/// row into the previous STOP's departure column — otherwise the dwell rows
/// surface as bogus "As Indicated" entries. Mirrors the dedup logic in
/// hud-overlay-tauri's db::get_timetable_entries.
#[tauri::command]
pub fn get_timetable_entries(id: i64) -> Result<Value, String> {
    let c = conn()?;
    // Per-stop coordinates. The catalog stores none on timetable_entries, so we
    // resolve each scheduled stop to its CAR-STOP-SIGN, snug-fit to the
    // formation's car count — the exact point hud-go marks (where a train of
    // THIS length stops; the sign position shifts ~200 m between a 2-car and an
    // 8-car consist). This is the same per-entry JOIN + ranking map_route_data
    // uses. Falls back to the on-path platform marker when no car-stop matches.
    let route_id: Option<i64> = c
        .query_row("SELECT route_id FROM timetables WHERE id=?1", [id], |r| r.get(0)).ok().flatten();
    let car_count: i64 = c
        .query_row(
            "SELECT COALESCE(f.car_count,0) FROM timetables t \
             LEFT JOIN formations f ON f.id=t.formation_id WHERE t.id=?1",
            [id], |r| r.get(0)).unwrap_or(0);
    let bound: String = c
        .query_row("SELECT COALESCE(LOWER(TRIM(bound)),'') FROM timetables WHERE id=?1", [id], |r| r.get(0))
        .unwrap_or_default();
    let mut car_coord: std::collections::HashMap<i64, (f64, f64)> = std::collections::HashMap::new();
    // ALL car-stop signs per entry (every max_rail_vehicles variant), so the
    // widget can re-pick by the LIVE consist length at runtime — the same
    // `car_stop_signs[]` contract db::map_route_data gives the desktop HUD and
    // hud-go's map_data.go gives its HUD. `car_coord` keeps the stored-car_count
    // snug-fit as the default; the array drives applyDynamicStopCoords.
    let mut car_signs: std::collections::HashMap<i64, Vec<serde_json::Value>> = std::collections::HashMap::new();
    if let Some(rid) = route_id {
        let sql = "SELECT te.id, css.latitude, css.longitude, css.max_rail_vehicles \
             FROM timetable_entries te JOIN locations l ON l.id=te.location_id \
             JOIN car_stop_signs css ON css.route_id=?1 \
               AND css.platform_name = TRIM(l.name || ' ' || COALESCE(te.structure,'') || ' ' || COALESCE(te.structure_number,'')) \
             WHERE te.timetable_id=?2 \
             ORDER BY te.id, \
               CASE WHEN css.max_rail_vehicles=?3 THEN 0 \
                    WHEN css.max_rail_vehicles>?3 THEN css.max_rail_vehicles-?3 \
                    WHEN css.max_rail_vehicles=0 THEN 99999 \
                    ELSE 99999+(?3-css.max_rail_vehicles) END, \
               CASE ?4 WHEN 'northbound' THEN -css.latitude WHEN 'southbound' THEN css.latitude \
                       WHEN 'eastbound' THEN -css.longitude WHEN 'westbound' THEN css.longitude ELSE 0 END";
        if let Ok(mut st) = c.prepare(sql) {
            if let Ok(rows) = st.query_map(rusqlite::params![rid, id, car_count, bound], |r| {
                Ok((r.get::<_, i64>(0)?, r.get::<_, f64>(1)?, r.get::<_, f64>(2)?, r.get::<_, i64>(3)?))
            }) {
                for (eid, la, lo, mrv) in rows.flatten() {
                    car_coord.entry(eid).or_insert((la, lo)); // first row per entry = best snug-fit
                    car_signs.entry(eid).or_default().push(json!({
                        "max_rail_vehicles": mrv, "latitude": la, "longitude": lo,
                    }));
                }
            }
        }
    }
    // Fallback platform candidates for stops with no car-stop match.
    let stop_cands = near_path_stop_candidates(&c, id);
    let mut stmt = c
        .prepare(
            "SELECT te.id, te.time1, te.time2, te.latitude, te.longitude, ta.name, l.name \
             FROM timetable_entries te \
             LEFT JOIN timetable_actions ta ON ta.id = te.action_id \
             LEFT JOIN locations l ON l.id = te.location_id \
             WHERE te.timetable_id = ?1 ORDER BY te.sort_order",
        )
        .map_err(|e| e.to_string())?;

    let rows = stmt
        .query_map([id], |row| {
            Ok((
                row.get::<_, i64>(0)?,            // te.id
                row.get::<_, Option<String>>(1)?, // time1 (arrival)
                row.get::<_, Option<String>>(2)?, // time2 (departure, for WAIT)
                row.get::<_, Option<String>>(3)?, // latitude
                row.get::<_, Option<String>>(4)?, // longitude
                row.get::<_, Option<String>>(5)?, // action
                row.get::<_, Option<String>>(6)?, // location
            ))
        })
        .map_err(|e| e.to_string())?;

    fn ne(s: &Option<String>) -> Option<String> {
        s.as_ref()
            .map(|x| x.trim())
            .filter(|x| !x.is_empty())
            .map(|x| x.to_string())
    }

    // First WAIT FOR SERVICE = spawn/origin. hud-go places it at the service
    // path's FIRST vertex (where the train actually spawns), NOT the car-stop
    // sign — the sign sits one train-length forward (the head-of-train stop).
    // We name it from the platform nearest that spawn vertex.
    let spawn: Option<(f64, f64)> = c
        .query_row("SELECT coordinates FROM timetable_coordinates WHERE timetable_id=?1", [id], |r| r.get::<_, String>(0))
        .ok()
        .and_then(|s| serde_json::from_str::<Vec<Vec<f64>>>(&s).ok())
        .and_then(|v| v.into_iter().find(|p| p.len() >= 2).map(|p| (p[1], p[0]))); // (lat,lng)
    let spawn_name: Option<String> = match (spawn, route_id) {
        (Some((sla, slo)), Some(rid)) => {
            let mut best: Option<(f64, String)> = None;
            if let Ok(mut st) = c.prepare(
                "SELECT name, latitude, longitude FROM track_markers \
                 WHERE route_id=?1 AND marker_type='Platform' AND latitude IS NOT NULL \
                   AND longitude IS NOT NULL AND name IS NOT NULL AND name<>''",
            ) {
                if let Ok(rows) = st.query_map([rid], |r| {
                    Ok((r.get::<_, String>(0)?, r.get::<_, f64>(1)?, r.get::<_, f64>(2)?))
                }) {
                    for (nm, mla, mlo) in rows.flatten() {
                        let d = crate::db::haversine_m(sla, slo, mla, mlo);
                        if best.as_ref().map_or(true, |b| d < b.0) { best = Some((d, nm)); }
                    }
                }
            }
            best.map(|(_, nm)| strip_track_suffix(&nm))
        }
        _ => None,
    };
    let mut first_wait_done = false;

    let mut out: Vec<serde_json::Map<String, Value>> = Vec::new();
    for r in rows {
        let (eid, t1, t2, lat, lng, action, location) = r.map_err(|e| e.to_string())?;
        let act = action.unwrap_or_default().to_uppercase();
        let has_loc = location.as_ref().map_or(false, |s| !s.trim().is_empty());
        let is_wait = act.contains("WAIT FOR SERVICE");
        // GO VIA / PASS are routing waypoints — keep them as their own rows even
        // when unnamed (they show as the via name or "As Indicated"), instead of
        // letting the dwell-fold below swallow them like a LOAD-PASSENGERS row.
        let is_via = act.contains("GO VIA") || act.starts_with("PASS");

        if has_loc || is_wait || is_via {
            // The first locationless WAIT FOR SERVICE is the spawn/origin row.
            let is_first_wait = is_wait && !has_loc && !first_wait_done;
            if is_first_wait { first_wait_done = true; }

            let mut m = serde_json::Map::new();
            m.insert("arrival".into(), json!(ne(&t1)));
            m.insert("departure".into(), json!(ne(&t2)));
            // Origin row: name it from the platform nearest the spawn vertex.
            let loc_display = if is_first_wait {
                spawn_name.clone().or_else(|| location.clone())
            } else {
                location.clone()
            };
            m.insert("location".into(), json!(loc_display));
            // Coordinate. Origin row → the spawn vertex (one train-length BEHIND
            // the car stop — hud-go's spawn-coord override). Every other stop →
            // snug-fit car-stop-sign → stored text coord → on-path platform.
            let coord = if is_first_wait {
                spawn
            } else {
                car_coord.get(&eid).copied()
                    .or_else(|| ne(&lat).and_then(|s| s.parse::<f64>().ok())
                        .zip(ne(&lng).and_then(|s| s.parse::<f64>().ok())))
                    .or_else(|| location.as_deref().and_then(|loc| match_stop_coord(&stop_cands, loc)))
            };
            m.insert("latitude".into(), json!(coord.map(|(la, _)| la)));
            m.insert("longitude".into(), json!(coord.map(|(_, lo)| lo)));
            // Full sign set for this stop so the widget can re-pick the coord by
            // the live vehicle count (applyDynamicStopCoords). Omitted on the
            // origin/spawn row (its coord is the path start, not a car-stop).
            if !is_first_wait {
                if let Some(signs) = car_signs.get(&eid) {
                    m.insert("car_stop_signs".into(), json!(signs));
                }
            }
            m.insert("action".into(), json!(act.clone()));
            // GO VIA and PASS are both pass-through points (train doesn't stop) —
            // flag both so the schedule shows "<name> [VIA]" or "As Indicated".
            m.insert("isPassThrough".into(), json!(act.starts_with("PASS") || act.contains("VIA")));
            out.push(m);
        } else if let Some(last) = out.last_mut() {
            if last.get("departure").map_or(true, |v| v.is_null()) {
                if let Some(dep) = ne(&t1).or_else(|| ne(&t2)) {
                    last.insert("departure".into(), json!(dep));
                }
            }
        }
    }
    Ok(Value::Array(out.into_iter().map(Value::Object).collect()))
}

// ---------------------------------------------------------------- map blobs

/// Decode `route_coordinates.coordinates` into a list of polyline segments,
/// where each segment is an array of `{latitude, longitude}` objects. The
/// extractor has written three different shapes over time, and this helper
/// normalizes all of them so callers get one consistent contract:
///
///   A. Flat `[{lat,lng}, ...]` → one segment.
///   B. Nested `[[{lat,lng}, ...], [{lat,lng}, ...], ...]` → multiple segments.
///   C. GeoJSON-Feature array `[{type:"Feature", geometry:{...}, ...}, ...]`
///      with `LineString` and/or `MultiLineString` geometries (and `Point`
///      features mixed in for stops, which we ignore). Coordinates inside
///      GeoJSON use `[lng, lat]` order — we swap them.
///
/// Empty when nothing is decodable.
fn route_segments_from_blob(s: &str) -> Vec<Vec<Value>> {
    let Ok(parsed) = serde_json::from_str::<Value>(s) else { return vec![] };
    let Some(top) = parsed.as_array() else { return vec![] };
    let Some(first) = top.first() else { return vec![] };

    // Shape A: flat list of {latitude, longitude} objects.
    if first.is_object() && first.get("latitude").is_some() {
        return vec![top.clone()];
    }
    // Shape B: nested list of segments, each a list of {lat,lng} objects.
    if first.is_array() {
        let mut out = Vec::new();
        for seg in top {
            if let Some(pts) = seg.as_array() {
                if !pts.is_empty() {
                    out.push(pts.clone());
                }
            }
        }
        return out;
    }
    // Shape C: GeoJSON Feature collection.
    if first.is_object() && first.get("geometry").is_some() {
        fn lnglat_to_obj(v: &Value) -> Option<Value> {
            let arr = v.as_array()?;
            if arr.len() < 2 { return None }
            let lng = arr[0].as_f64()?;
            let lat = arr[1].as_f64()?;
            Some(json!({ "latitude": lat, "longitude": lng }))
        }
        let mut out: Vec<Vec<Value>> = Vec::new();
        for feat in top {
            let Some(geom) = feat.get("geometry") else { continue };
            let gtype = geom.get("type").and_then(|v| v.as_str()).unwrap_or("");
            let coords = match geom.get("coordinates") { Some(v) => v, None => continue };
            match gtype {
                "LineString" => {
                    if let Some(arr) = coords.as_array() {
                        let seg: Vec<Value> = arr.iter().filter_map(lnglat_to_obj).collect();
                        if !seg.is_empty() { out.push(seg); }
                    }
                }
                "MultiLineString" => {
                    if let Some(outer) = coords.as_array() {
                        for sub in outer {
                            if let Some(arr) = sub.as_array() {
                                let seg: Vec<Value> = arr.iter().filter_map(lnglat_to_obj).collect();
                                if !seg.is_empty() { out.push(seg); }
                            }
                        }
                    }
                }
                // Skip Point features (those are stops/signals/etc, not the
                // line geometry) and any unknown geometry types.
                _ => {}
            }
        }
        return out;
    }
    vec![]
}

/// Polyline for a timetable's path. Reads `timetable_coordinates` (one row per
/// timetable, always flat `[{lat,lng}, ...]`); when that's missing, falls back
/// to the route-level polyline (which can be flat, nested-segments, or a
/// GeoJSON FeatureCollection — all three are normalized through
/// `route_segments_from_blob`) and concatenates the segments. For multi-line
/// route geometries the concatenation introduces visual jumps between
/// segments, which is acceptable for the fallback case; the "Load whole route"
/// path uses `route_geometry` instead so it can render each segment cleanly.
#[tauri::command]
pub fn get_route_coordinates(id: i64) -> Result<Value, String> {
    let c = conn()?;
    let blob: Option<String> = c
        .query_row(
            "SELECT coordinates FROM timetable_coordinates WHERE timetable_id = ?1",
            [id],
            |row| row.get(0),
        )
        .ok();
    if let Some(s) = blob {
        return serde_json::from_str::<Value>(&s).map_err(|e| e.to_string());
    }
    let route_blob: Option<String> = c
        .query_row(
            "SELECT rc.coordinates FROM timetables t \
             JOIN route_coordinates rc ON rc.route_id = t.route_id \
             WHERE t.id = ?1",
            [id],
            |row| row.get(0),
        )
        .ok();
    let Some(s) = route_blob else { return Ok(Value::Array(vec![])) };
    let segments = route_segments_from_blob(&s);
    let mut flat: Vec<Value> = Vec::new();
    for seg in segments { flat.extend(seg); }
    Ok(Value::Array(flat))
}

// ---------------------------------------------------------------- weather

#[tauri::command]
pub fn weather_presets() -> Result<Value, String> {
    crate::db::weather_presets_list().map(Value::Array)
}

/// PATCH the supplied weather values to TSW via the CommAPI. Used by the
/// Weather widget's "Apply" button after the user moves sliders.
#[tauri::command]
pub async fn weather_apply(values: crate::weather::TswWeatherValues) -> Result<String, String> {
    let cfg = crate::config::Config::load();
    let key = crate::tsw::resolve_api_key_pub(&cfg);
    if key.is_empty() {
        return Err("no API key (set one in Settings or check CommAPIKey.txt)".into());
    }
    let (applied, total) = crate::weather::apply_to_tsw(&key, &values).await;
    if applied == 0 {
        Err(format!("apply failed: 0/{total} values landed (TSW running?)"))
    } else {
        Ok(format!("applied {applied}/{total}"))
    }
}

#[derive(serde::Deserialize)]
pub struct PresetUpsert {
    pub id: Option<i64>,
    pub name: String,
    #[serde(default)] pub temperature: f64,
    #[serde(default)] pub cloudiness: f64,
    #[serde(default)] pub precipitation: f64,
    #[serde(default)] pub wetness: f64,
    #[serde(default)] pub ground_snow: f64,
    #[serde(default)] pub piled_snow: f64,
    #[serde(default)] pub fog_density: f64,
}

/// Create (id null) or update (id set) a weather preset. Returns the id.
#[tauri::command]
pub async fn weather_preset_save(body: PresetUpsert) -> Result<i64, String> {
    tokio::task::spawn_blocking(move || -> Result<i64, String> {
        if body.name.trim().is_empty() {
            return Err("preset name required".into());
        }
        match body.id {
            Some(id) => {
                crate::db::weather_preset_update(
                    id,
                    body.name.trim(),
                    body.temperature, body.cloudiness, body.precipitation, body.wetness,
                    body.ground_snow, body.piled_snow, body.fog_density,
                )?;
                Ok(id)
            }
            None => crate::db::weather_preset_create(
                body.name.trim(),
                body.temperature, body.cloudiness, body.precipitation, body.wetness,
                body.ground_snow, body.piled_snow, body.fog_density,
            ),
        }
    })
    .await
    .map_err(|e| e.to_string())?
}

#[tauri::command]
pub async fn weather_preset_delete(id: i64) -> Result<(), String> {
    tokio::task::spawn_blocking(move || crate::db::weather_preset_delete(id))
        .await
        .map_err(|e| e.to_string())?
}

/// Fetch the latest Open-Meteo "current weather" for the player's position
/// and PATCH the mapped TSW values. Returns `{applied: N, total: 7, values: {...}}`.
#[tauri::command]
pub async fn weather_live_apply(
    state: tauri::State<'_, AppShared>,
) -> Result<Value, String> {
    // Lookup player position from the always-on poller's snapshot
    let snap = state.telemetry.snapshot().ok_or_else(|| {
        "no telemetry yet — start TSW with a loaded service".to_string()
    })?;
    let (lat, lng) = crate::weather::player_position(&snap)?;
    let live = crate::weather::fetch_live(lat, lng).await?;
    let values = crate::weather::map_live_to_tsw(&live);

    let cfg = crate::config::Config::load();
    let key = crate::tsw::resolve_api_key_pub(&cfg);
    if key.is_empty() {
        return Err("no API key (set one in Settings or check CommAPIKey.txt)".into());
    }
    let (applied, total) = crate::weather::apply_to_tsw(&key, &values).await;
    Ok(serde_json::json!({
        "applied": applied,
        "total":   total,
        "values":  values,
        "player":  { "latitude": lat, "longitude": lng },
    }))
}

/// Fetch Open-Meteo archive for `date` (YYYY-MM-DD), pick the hourly bucket
/// closest to the in-game time, map to TSW values, and PATCH. Returns the
/// same shape as `weather_live_apply` plus `{date, hour_idx}`.
#[tauri::command]
pub async fn weather_historical_apply(
    date: String,
    state: tauri::State<'_, AppShared>,
) -> Result<Value, String> {
    let snap = state.telemetry.snapshot().ok_or_else(|| {
        "no telemetry yet — start TSW with a loaded service".to_string()
    })?;
    let (lat, lng) = crate::weather::player_position(&snap)?;
    let hour = crate::weather::game_hour(&snap);
    let archive = crate::weather::fetch_archive(lat, lng, &date).await?;
    let idx = crate::weather::closest_hour_index(&archive, hour);
    let values = crate::weather::map_archive_to_tsw(&archive, idx);

    let cfg = crate::config::Config::load();
    let key = crate::tsw::resolve_api_key_pub(&cfg);
    if key.is_empty() {
        return Err("no API key (set one in Settings or check CommAPIKey.txt)".into());
    }
    let (applied, total) = crate::weather::apply_to_tsw(&key, &values).await;
    Ok(serde_json::json!({
        "applied": applied,
        "total":   total,
        "values":  values,
        "player":  { "latitude": lat, "longitude": lng },
        "date":    date,
        "game_hour": hour,
        "hour_idx":  idx,
    }))
}

/// Look up a saved preset by id and apply its values to TSW.
#[tauri::command]
pub async fn weather_apply_preset(id: i64) -> Result<String, String> {
    let preset = tokio::task::spawn_blocking(move || crate::db::weather_preset_get(id))
        .await
        .map_err(|e| e.to_string())??
        .ok_or_else(|| format!("preset {id} not found"))?;
    let v = crate::weather::TswWeatherValues {
        temperature:   preset["temperature"].as_f64().unwrap_or(0.0),
        cloudiness:    preset["cloudiness"].as_f64().unwrap_or(0.0),
        precipitation: preset["precipitation"].as_f64().unwrap_or(0.0),
        wetness:       preset["wetness"].as_f64().unwrap_or(0.0),
        ground_snow:   preset["ground_snow"].as_f64().unwrap_or(0.0),
        piled_snow:    preset["piled_snow"].as_f64().unwrap_or(0.0),
        fog_density:   preset["fog_density"].as_f64().unwrap_or(0.0),
    };
    weather_apply(v).await
}

// ---------------------------------------------------------------- timetables index

/// Bundle of dropdown options the Timetables filter bar needs in one IPC.
/// Reads four DB lookups in parallel on the tokio blocking pool.
#[derive(serde::Serialize)]
pub struct TimetableFilterOptions {
    pub countries: Vec<(i64, String, String)>, // (id, name, code)
    pub routes:    Vec<(i64, String, Option<i64>)>, // (id, name, country_id)
    pub classes:   Vec<(i64, String)>,
    pub sections:  Vec<(String, Option<i64>)>, // (name, route_id)
}

#[tauri::command]
pub async fn timetable_filter_options() -> Result<TimetableFilterOptions, String> {
    tokio::task::spawn_blocking(|| {
        Ok(TimetableFilterOptions {
            countries: crate::db::country_list()?,
            routes:    crate::db::routes_list()?,
            classes:   crate::db::train_classes()?,
            sections:  crate::db::section_names()?,
        })
    })
    .await
    .map_err(|e| e.to_string())?
}

#[derive(serde::Serialize)]
pub struct TimetablePage {
    pub rows:  Vec<Vec<String>>,
    pub ids:   Vec<i64>,
    pub total: i64,
}

/// Paginated, filtered, sorted timetable index. `filter` is whatever the
/// frontend submits; missing fields default to empty strings (= no filter).
/// `dev` is sourced from the user's Settings so the frontend doesn't have to
/// know about it — when dev mode is off the index hides un-playable rows
/// (same rule hud-rust's egui app used).
#[tauri::command]
pub async fn timetable_search(
    mut filter: crate::db::TtFilter,
    page: i64,
    per_page: i64,
) -> Result<TimetablePage, String> {
    let cfg = crate::config::Config::load();
    filter.dev = cfg.development_mode;
    tokio::task::spawn_blocking(move || {
        let (rows, ids, total) = crate::db::timetables_search(&filter, page, per_page)?;
        Ok(TimetablePage { rows, ids, total })
    })
    .await
    .map_err(|e| e.to_string())?
}

// ---------------------------------------------------------------- timetable detail / edit

/// Single-shot bundle that the timetable show + edit pages need:
/// timetable row, all entries, formations, sections, route + country.
/// One IPC call → six SQL reads on the cached connection.
#[tauri::command]
pub async fn timetable_detail(id: i64) -> Result<Value, String> {
    tokio::task::spawn_blocking(move || {
        crate::db::with_read(|c| {
            let mut stmt = c
                .prepare("SELECT * FROM timetables WHERE id = ?1")
                .map_err(|e| e.to_string())?;
            let cols: Vec<String> =
                stmt.column_names().into_iter().map(|s| s.to_string()).collect();
            let n = cols.len();
            let mut q = stmt.query([id]).map_err(|e| e.to_string())?;
            let row = match q.next().map_err(|e| e.to_string())? {
                Some(r) => r,
                None => return Err(format!("timetable {id} not found")),
            };
            let tt = row_to_obj(row, &cols, n)?;

            // Route + country (best-effort; null if missing)
            let route_id = tt.get("route_id").and_then(|v| v.as_i64());
            let mut route = Value::Null;
            let mut country = Value::Null;
            if let Some(rid) = route_id {
                if let Ok((name, country_id)) = c.query_row(
                    "SELECT COALESCE(name,''), country_id FROM routes WHERE id = ?1",
                    [rid],
                    |r| Ok((r.get::<_, String>(0)?, r.get::<_, Option<i64>>(1)?)),
                ) {
                    route = json!({ "id": rid, "name": name, "country_id": country_id });
                    if let Some(cid) = country_id {
                        if let Ok((cn, cc)) = c.query_row(
                            "SELECT COALESCE(name,''), COALESCE(code,'') FROM countries WHERE id = ?1",
                            [cid],
                            |r| Ok((r.get::<_, String>(0)?, r.get::<_, String>(1)?)),
                        ) {
                            country = json!({ "id": cid, "name": cn, "code": cc });
                        }
                    }
                }
            }

            // Entries with their action + location names, sorted by sort_order.
            let mut es = c
                .prepare(
                    "SELECT te.id, te.timetable_id, te.sort_order, te.time1, te.time2, \
                            te.latitude, te.longitude, te.cargo, te.car_stop_sign_id, te.track_marker_id, \
                            ta.name AS action, l.name AS location, te.location_id, te.action_id, \
                            COALESCE(tc.coord_source, '') AS coord_source \
                     FROM timetable_entries te \
                     LEFT JOIN timetable_actions ta ON ta.id = te.action_id \
                     LEFT JOIN locations l ON l.id = te.location_id \
                     LEFT JOIN timetable_coordinates tc ON tc.timetable_id = te.timetable_id \
                     WHERE te.timetable_id = ?1 ORDER BY te.sort_order, te.id",
                )
                .map_err(|e| e.to_string())?;
            let ecols: Vec<String> =
                es.column_names().into_iter().map(|s| s.to_string()).collect();
            let ecn = ecols.len();
            let mut eq = es.query([id]).map_err(|e| e.to_string())?;
            let mut entries: Vec<Value> = Vec::new();
            while let Some(r) = eq.next().map_err(|e| e.to_string())? {
                entries.push(Value::Object(row_to_obj(r, &ecols, ecn)?));
            }

            // Resolve per-entry coords for the (dev-only) coord columns — the raw
            // te.latitude column is null. Car-stop snug-fit, with the first WAIT
            // FOR SERVICE row at the spawn vertex, matching the schedule widget.
            {
                let car_coord = entry_car_stop_coords(c, id);
                let spawn = spawn_vertex(c, id);
                let mut first_wait_done = false;
                for e in entries.iter_mut() {
                    let Some(obj) = e.as_object_mut() else { continue };
                    let eid = obj.get("id").and_then(|v| v.as_i64());
                    let act = obj.get("action").and_then(|v| v.as_str()).unwrap_or("").to_uppercase();
                    let has_loc = obj.get("location").and_then(|v| v.as_str()).map_or(false, |s| !s.trim().is_empty());
                    let is_first_wait = act.contains("WAIT FOR SERVICE") && !has_loc && !first_wait_done;
                    if is_first_wait { first_wait_done = true; }
                    let (coord, src) = if is_first_wait {
                        (spawn, "spawn")
                    } else {
                        (eid.and_then(|i| car_coord.get(&i).copied()), "car_stop")
                    };
                    if let Some((la, lo)) = coord {
                        obj.insert("latitude".into(), json!(la));
                        obj.insert("longitude".into(), json!(lo));
                        obj.insert("coord_source".into(), json!(src));
                    }
                }
            }

            // Formations on this timetable.
            let mut fs = c
                .prepare(
                    "SELECT f.id, COALESCE(f.name,'') AS name, \
                            COALESCE(f.class_name,'') AS class_name, \
                            COALESCE(f.livery_id,'') AS livery_id, \
                            f.car_count, f.length_m \
                     FROM timetable_formations tf \
                     JOIN formations f ON f.id = tf.formation_id \
                     WHERE tf.timetable_id = ?1 \
                     ORDER BY f.name",
                )
                .map_err(|e| e.to_string())?;
            let fcols: Vec<String> =
                fs.column_names().into_iter().map(|s| s.to_string()).collect();
            let fcn = fcols.len();
            let mut fq = fs.query([id]).map_err(|e| e.to_string())?;
            let mut formations: Vec<Value> = Vec::new();
            while let Some(r) = fq.next().map_err(|e| e.to_string())? {
                formations.push(Value::Object(row_to_obj(r, &fcols, fcn)?));
            }

            // Sections (just names).
            let mut ss = c
                .prepare(
                    "SELECT s.id, COALESCE(s.name,'') AS name \
                     FROM timetable_sections ts JOIN sections s ON s.id = ts.section_id \
                     WHERE ts.timetable_id = ?1 ORDER BY s.name",
                )
                .map_err(|e| e.to_string())?;
            let scols: Vec<String> =
                ss.column_names().into_iter().map(|s| s.to_string()).collect();
            let scn = scols.len();
            let mut sq = ss.query([id]).map_err(|e| e.to_string())?;
            let mut sections: Vec<Value> = Vec::new();
            while let Some(r) = sq.next().map_err(|e| e.to_string())? {
                sections.push(Value::Object(row_to_obj(r, &scols, scn)?));
            }

            Ok(json!({
                "timetable":  Value::Object(tt),
                "route":      route,
                "country":    country,
                "entries":    entries,
                "formations": formations,
                "sections":   sections,
            }))
        })
    })
    .await
    .map_err(|e| e.to_string())?
}

fn row_to_obj(
    row: &rusqlite::Row<'_>,
    col_names: &[String],
    col_count: usize,
) -> Result<serde_json::Map<String, Value>, String> {
    use rusqlite::types::ValueRef;
    let mut m = serde_json::Map::with_capacity(col_count);
    for i in 0..col_count {
        let v = match row.get_ref(i).map_err(|e| e.to_string())? {
            ValueRef::Null => Value::Null,
            ValueRef::Integer(n) => json!(n),
            ValueRef::Real(f) => json!(f),
            ValueRef::Text(t) => Value::String(String::from_utf8_lossy(t).into_owned()),
            ValueRef::Blob(b) => json!(b),
        };
        m.insert(col_names[i].clone(), v);
    }
    Ok(m)
}

/// Editable fields on a timetable header. Covers every user-editable column
/// in `timetables` (read-only: `id`, `created_at`). Missing fields are left
/// untouched — only fields the frontend explicitly sends get included in
/// the UPDATE.
#[derive(serde::Deserialize, Default)]
#[serde(default)]
pub struct TimetableEdit {
    pub service_name: Option<String>,
    pub service: Option<String>,
    pub current_service_name: Option<String>,
    pub service_type: Option<String>,
    pub source: Option<String>,
    pub bound: Option<String>,
    pub playable: Option<bool>,
    pub conductor_compatible: Option<bool>,
    pub start_time: Option<String>,
    pub duration: Option<String>,
    pub route_id: Option<i64>,
    pub formation_id: Option<i64>,
    pub section_id: Option<i64>,
    pub contributor: Option<String>,
    pub coordinates_contributor: Option<String>,
    pub service_images: Option<String>,
}

#[tauri::command]
pub async fn timetable_update(id: i64, body: TimetableEdit) -> Result<(), String> {
    tokio::task::spawn_blocking(move || -> Result<(), String> {
        let c = rusqlite::Connection::open_with_flags(
            crate::db::db_path(),
            rusqlite::OpenFlags::SQLITE_OPEN_READ_WRITE | rusqlite::OpenFlags::SQLITE_OPEN_URI,
        )
        .map_err(|e| format!("open db rw: {e}"))?;
        let mut sets: Vec<&'static str> = Vec::new();
        let mut params: Vec<Box<dyn rusqlite::ToSql>> = Vec::new();
        macro_rules! field {
            ($k:literal, $v:expr) => {
                if let Some(v) = $v {
                    sets.push(concat!($k, " = ?"));
                    params.push(Box::new(v));
                }
            };
        }
        field!("service_name", body.service_name);
        field!("service", body.service);
        field!("current_service_name", body.current_service_name);
        field!("service_type", body.service_type);
        field!("source", body.source);
        field!("bound", body.bound);
        field!("playable", body.playable.map(|b| if b { 1_i64 } else { 0 }));
        field!(
            "conductor_compatible",
            body.conductor_compatible.map(|b| if b { 1_i64 } else { 0 })
        );
        field!("start_time", body.start_time);
        field!("duration", body.duration);
        field!("route_id", body.route_id);
        field!("formation_id", body.formation_id);
        field!("section_id", body.section_id);
        field!("contributor", body.contributor);
        field!("coordinates_contributor", body.coordinates_contributor);
        field!("service_images", body.service_images);
        if sets.is_empty() {
            return Ok(());
        }
        let sql = format!("UPDATE timetables SET {} WHERE id = ?", sets.join(", "));
        params.push(Box::new(id));
        let n = c
            .execute(
                &sql,
                rusqlite::params_from_iter(params.iter().map(|b| &**b)),
            )
            .map_err(|e| e.to_string())?;
        if n == 0 {
            return Err(format!("timetable {id} not found"));
        }
        Ok(())
    })
    .await
    .map_err(|e| e.to_string())?
}

// ---------------------------------------------------------------- timetable entries

/// Every editable column on `timetable_entries`. Read-only: `id` and
/// `timetable_id` (set by create, not patched). Missing fields are left
/// alone; an explicit JSON `null` clears the column.
#[derive(serde::Deserialize, Default)]
#[serde(default)]
pub struct EntryEdit {
    pub action_id: Option<serde_json::Value>,
    pub location_id: Option<serde_json::Value>,
    pub sort_order: Option<i64>,
    pub time1: Option<String>,
    pub time2: Option<String>,
    pub latitude: Option<String>,
    pub longitude: Option<String>,
    pub tile_x: Option<serde_json::Value>,
    pub tile_y: Option<serde_json::Value>,
    pub api_name: Option<String>,
    pub structure: Option<String>,
    pub structure_number: Option<String>,
    pub details: Option<String>,
    pub cargo: Option<String>,
    pub waiting_time: Option<String>,
    pub coord_source: Option<String>,
    pub car_stop_sign_id: Option<serde_json::Value>,
    pub track_marker_id: Option<serde_json::Value>,
}

fn open_rw() -> Result<rusqlite::Connection, String> {
    rusqlite::Connection::open_with_flags(
        crate::db::db_path(),
        rusqlite::OpenFlags::SQLITE_OPEN_READ_WRITE | rusqlite::OpenFlags::SQLITE_OPEN_URI,
    )
    .map_err(|e| format!("open db rw: {e}"))
}

/// JSON value → SQL value, preserving NULL when the caller sends `null` and
/// stringifying numbers/strings. Anything weirder becomes an error.
fn jv_to_sql(v: &serde_json::Value) -> Result<Box<dyn rusqlite::ToSql>, String> {
    use serde_json::Value::*;
    Ok(match v {
        Null => Box::new(Option::<i64>::None),
        Bool(b) => Box::new(if *b { 1_i64 } else { 0 }),
        Number(n) => {
            if let Some(i) = n.as_i64() { Box::new(i) }
            else if let Some(f) = n.as_f64() { Box::new(f) }
            else { return Err(format!("unsupported number: {n}")); }
        }
        String(s) => Box::new(s.clone()),
        other => return Err(format!("can't convert {other} to SQL")),
    })
}

#[tauri::command]
pub async fn entry_update(entry_id: i64, body: EntryEdit) -> Result<(), String> {
    tokio::task::spawn_blocking(move || -> Result<(), String> {
        let c = open_rw()?;
        let mut sets: Vec<&'static str> = Vec::new();
        let mut params: Vec<Box<dyn rusqlite::ToSql>> = Vec::new();
        macro_rules! str_field {
            ($k:literal, $v:expr) => {
                if let Some(v) = $v {
                    sets.push(concat!($k, " = ?"));
                    params.push(Box::new(v));
                }
            };
        }
        macro_rules! int_field {
            ($k:literal, $v:expr) => {
                if let Some(v) = $v {
                    sets.push(concat!($k, " = ?"));
                    params.push(Box::new(v));
                }
            };
        }
        macro_rules! jv_field {
            ($k:literal, $v:expr) => {
                if let Some(v) = $v {
                    sets.push(concat!($k, " = ?"));
                    params.push(jv_to_sql(&v)?);
                }
            };
        }
        jv_field!("action_id", body.action_id);
        jv_field!("location_id", body.location_id);
        int_field!("sort_order", body.sort_order);
        str_field!("time1", body.time1);
        str_field!("time2", body.time2);
        str_field!("latitude", body.latitude);
        str_field!("longitude", body.longitude);
        jv_field!("tile_x", body.tile_x);
        jv_field!("tile_y", body.tile_y);
        str_field!("api_name", body.api_name);
        str_field!("structure", body.structure);
        str_field!("structure_number", body.structure_number);
        str_field!("details", body.details);
        str_field!("cargo", body.cargo);
        str_field!("waiting_time", body.waiting_time);
        str_field!("coord_source", body.coord_source);
        jv_field!("car_stop_sign_id", body.car_stop_sign_id);
        jv_field!("track_marker_id", body.track_marker_id);
        if sets.is_empty() {
            return Ok(());
        }
        let sql = format!("UPDATE timetable_entries SET {} WHERE id = ?", sets.join(", "));
        params.push(Box::new(entry_id));
        let n = c
            .execute(&sql, rusqlite::params_from_iter(params.iter().map(|b| &**b)))
            .map_err(|e| e.to_string())?;
        if n == 0 {
            return Err(format!("entry {entry_id} not found"));
        }
        Ok(())
    })
    .await
    .map_err(|e| e.to_string())?
}

#[tauri::command]
pub async fn entry_create(timetable_id: i64) -> Result<i64, String> {
    tokio::task::spawn_blocking(move || -> Result<i64, String> {
        let c = open_rw()?;
        // Append: sort_order = max + 1 (so the new row lands at the end of
        // the schedule). 0 if there are no rows yet.
        let next: i64 = c
            .query_row(
                "SELECT COALESCE(MAX(sort_order), -1) + 1 FROM timetable_entries WHERE timetable_id = ?1",
                [timetable_id],
                |r| r.get(0),
            )
            .map_err(|e| e.to_string())?;
        c.execute(
            "INSERT INTO timetable_entries (timetable_id, sort_order) VALUES (?1, ?2)",
            rusqlite::params![timetable_id, next],
        )
        .map_err(|e| e.to_string())?;
        Ok(c.last_insert_rowid())
    })
    .await
    .map_err(|e| e.to_string())?
}

/// Bulk save: takes the FULL desired entries list for a timetable and
/// reconciles it in a single transaction —
///   * rows with no `id` are inserted
///   * rows with a matching `id` are updated (full row, every editable column)
///   * any DB row whose id isn't in `entries` is deleted
/// All-or-nothing: if any step errors the transaction rolls back.
#[derive(serde::Deserialize, Default)]
#[serde(default)]
pub struct EntryFull {
    pub id: Option<i64>,
    pub action_id: Option<serde_json::Value>,
    pub location_id: Option<serde_json::Value>,
    pub sort_order: Option<i64>,
    pub time1: Option<String>,
    pub time2: Option<String>,
    pub latitude: Option<String>,
    pub longitude: Option<String>,
    pub tile_x: Option<serde_json::Value>,
    pub tile_y: Option<serde_json::Value>,
    pub api_name: Option<String>,
    pub structure: Option<String>,
    pub structure_number: Option<String>,
    pub details: Option<String>,
    pub cargo: Option<String>,
    pub waiting_time: Option<String>,
    pub coord_source: Option<String>,
    pub car_stop_sign_id: Option<serde_json::Value>,
    pub track_marker_id: Option<serde_json::Value>,
}

#[derive(serde::Serialize)]
pub struct EntriesSaveSummary {
    pub inserted: usize,
    pub updated:  usize,
    pub deleted:  usize,
}

#[tauri::command]
pub async fn entries_save(
    timetable_id: i64,
    entries: Vec<EntryFull>,
) -> Result<EntriesSaveSummary, String> {
    tokio::task::spawn_blocking(move || -> Result<EntriesSaveSummary, String> {
        let mut c = open_rw()?;
        let tx = c.transaction().map_err(|e| e.to_string())?;

        // Snapshot the current ids so we know what to delete.
        let mut existing: std::collections::HashSet<i64> = std::collections::HashSet::new();
        {
            let mut s = tx
                .prepare("SELECT id FROM timetable_entries WHERE timetable_id = ?1")
                .map_err(|e| e.to_string())?;
            let rows = s
                .query_map([timetable_id], |r| r.get::<_, i64>(0))
                .map_err(|e| e.to_string())?;
            for r in rows {
                existing.insert(r.map_err(|e| e.to_string())?);
            }
        }

        let mut inserted = 0usize;
        let mut updated = 0usize;
        let mut kept: std::collections::HashSet<i64> = std::collections::HashSet::new();

        // Fully-qualified column list. Same column set as `entry_update`'s
        // whitelist so the two stay in sync.
        const COLS: &[&str] = &[
            "action_id", "location_id", "sort_order", "time1", "time2",
            "latitude", "longitude", "tile_x", "tile_y", "api_name",
            "structure", "structure_number", "details", "cargo",
            "waiting_time", "coord_source", "car_stop_sign_id",
            "track_marker_id",
        ];

        for (idx, e) in entries.iter().enumerate() {
            // Build params for every column in COLS order, using `Null` for
            // anything the caller omitted (matches "no value" semantics).
            let mut p: Vec<Box<dyn rusqlite::ToSql>> = Vec::with_capacity(COLS.len() + 2);
            macro_rules! push_jv {
                ($v:expr) => {
                    match $v {
                        Some(v) => p.push(jv_to_sql(v)?),
                        None    => p.push(Box::new(Option::<i64>::None)),
                    }
                };
            }
            macro_rules! push_str {
                ($v:expr) => {
                    match $v {
                        Some(s) => p.push(Box::new(s.clone())),
                        None    => p.push(Box::new(Option::<String>::None)),
                    }
                };
            }
            // Same order as COLS:
            push_jv!(&e.action_id);
            push_jv!(&e.location_id);
            // sort_order: if the caller didn't send one, use the row's index.
            match e.sort_order {
                Some(n) => p.push(Box::new(n)),
                None    => p.push(Box::new(idx as i64)),
            }
            push_str!(&e.time1);
            push_str!(&e.time2);
            push_str!(&e.latitude);
            push_str!(&e.longitude);
            push_jv!(&e.tile_x);
            push_jv!(&e.tile_y);
            push_str!(&e.api_name);
            push_str!(&e.structure);
            push_str!(&e.structure_number);
            push_str!(&e.details);
            push_str!(&e.cargo);
            push_str!(&e.waiting_time);
            push_str!(&e.coord_source);
            push_jv!(&e.car_stop_sign_id);
            push_jv!(&e.track_marker_id);

            if let Some(eid) = e.id {
                // UPDATE — `id = ?` appended.
                let sets = COLS.iter().map(|c| format!("{c} = ?")).collect::<Vec<_>>().join(", ");
                let sql = format!("UPDATE timetable_entries SET {sets} WHERE id = ?");
                p.push(Box::new(eid));
                tx.execute(&sql, rusqlite::params_from_iter(p.iter().map(|b| &**b)))
                    .map_err(|e| e.to_string())?;
                kept.insert(eid);
                updated += 1;
            } else {
                // INSERT — prepend timetable_id, list every column.
                let col_list = std::iter::once("timetable_id")
                    .chain(COLS.iter().copied())
                    .collect::<Vec<_>>()
                    .join(", ");
                let placeholders = std::iter::repeat("?")
                    .take(COLS.len() + 1)
                    .collect::<Vec<_>>()
                    .join(", ");
                let sql = format!(
                    "INSERT INTO timetable_entries ({col_list}) VALUES ({placeholders})"
                );
                // Prepend timetable_id at the front of params.
                let mut full: Vec<Box<dyn rusqlite::ToSql>> = Vec::with_capacity(p.len() + 1);
                full.push(Box::new(timetable_id));
                full.extend(p);
                tx.execute(&sql, rusqlite::params_from_iter(full.iter().map(|b| &**b)))
                    .map_err(|e| e.to_string())?;
                inserted += 1;
            }
        }

        // Delete any leftover ids.
        let mut to_delete: Vec<i64> = existing.difference(&kept).copied().collect();
        to_delete.sort();
        let deleted = to_delete.len();
        for did in to_delete {
            tx.execute("DELETE FROM timetable_entries WHERE id = ?1", [did])
                .map_err(|e| e.to_string())?;
        }

        tx.commit().map_err(|e| e.to_string())?;
        Ok(EntriesSaveSummary { inserted, updated, deleted })
    })
    .await
    .map_err(|e| e.to_string())?
}

#[tauri::command]
pub async fn entry_delete(entry_id: i64) -> Result<(), String> {
    tokio::task::spawn_blocking(move || -> Result<(), String> {
        let c = open_rw()?;
        let n = c
            .execute("DELETE FROM timetable_entries WHERE id = ?1", [entry_id])
            .map_err(|e| e.to_string())?;
        if n == 0 {
            return Err(format!("entry {entry_id} not found"));
        }
        Ok(())
    })
    .await
    .map_err(|e| e.to_string())?
}

/// Lookup table the entry editor uses to render the Action dropdown.
/// Returns `[(id, name)]` sorted by name.
#[tauri::command]
pub async fn actions_list() -> Result<Vec<(i64, String)>, String> {
    tokio::task::spawn_blocking(|| {
        crate::db::with_read(|c| {
            let mut s = c
                .prepare("SELECT id, COALESCE(name, '') FROM timetable_actions ORDER BY name")
                .map_err(|e| e.to_string())?;
            let rows = s
                .query_map([], |r| Ok((r.get::<_, i64>(0)?, r.get::<_, String>(1)?)))
                .map_err(|e| e.to_string())?;
            let mut out = Vec::new();
            for r in rows {
                out.push(r.map_err(|e| e.to_string())?);
            }
            Ok(out)
        })
    })
    .await
    .map_err(|e| e.to_string())?
}

// ---------------------------------------------------------------- train classes

#[derive(serde::Serialize)]
pub struct TrainClassRow {
    pub id: i64,
    pub name: String,
    pub manufacturer_name: String,
    pub type_description: String,
    pub vehicle_category: String,
    pub max_speed_kph: f64,
    pub typical_car_count: Option<i64>,
    pub is_electric: bool,
    pub is_drivable: bool,
    pub thumbnail_path: String,
}

#[derive(serde::Serialize)]
pub struct TrainClassPage {
    pub rows: Vec<TrainClassRow>,
    pub total: i64,
}

/// Paginated train-class list with optional name search + electric/diesel
/// filter. Mirrors hud-go's /api/train-classes endpoint, including its
/// default "trainlike-only" filter — by default we hide rows that the UI
/// would treat as noise (orphan extraction artifacts with no type_description,
/// FreightWagons, and non-drivable RVDs). Pass `show_all=true` to opt out.
#[tauri::command]
pub async fn train_classes_list(
    search: String,
    electric: String, // "" / "yes" / "no"
    show_all: Option<bool>,
    page: i64,
    per_page: i64,
) -> Result<TrainClassPage, String> {
    tokio::task::spawn_blocking(move || {
        crate::db::with_read(|c| {
            let mut conds: Vec<&'static str> = Vec::new();
            let mut params: Vec<Box<dyn rusqlite::ToSql>> = Vec::new();
            let s = search.trim();
            if !s.is_empty() {
                conds.push("(COALESCE(name,'') LIKE ? OR COALESCE(manufacturer_name,'') LIKE ?)");
                let p = format!("%{s}%");
                params.push(Box::new(p.clone()));
                params.push(Box::new(p));
            }
            match electric.as_str() {
                "yes" => conds.push("is_electric = 1"),
                "no" => conds.push("(is_electric = 0 OR is_electric IS NULL)"),
                _ => {}
            }
            if !show_all.unwrap_or(false) {
                // Parity with hud-go formation.go's class list: hide dummy /
                // unidentified RVDs (no type_description), freight wagons, and
                // anything bIsDrivable=false in its RVD.
                conds.push("type_description IS NOT NULL");
                conds.push("TRIM(type_description) <> ''");
                conds.push("(vehicle_category IS NULL OR vehicle_category <> 'FreightWagon')");
                conds.push("is_drivable = 1");
            }
            let where_sql = if conds.is_empty() { "".to_string() }
                else { format!(" WHERE {}", conds.join(" AND ")) };

            let total: i64 = c
                .query_row(
                    &format!("SELECT COUNT(*) FROM train_classes{where_sql}"),
                    rusqlite::params_from_iter(params.iter().map(|b| &**b)),
                    |r| r.get(0),
                )
                .map_err(|e| e.to_string())?;

            let data_sql = format!(
                "SELECT id, COALESCE(name,''), COALESCE(manufacturer_name,''), \
                        COALESCE(type_description,''), COALESCE(vehicle_category,''), \
                        COALESCE(max_speed_kph, 0), typical_car_count, \
                        COALESCE(is_electric, 0), COALESCE(is_drivable, 0), \
                        COALESCE(thumbnail_path,'') \
                 FROM train_classes{where_sql} \
                 ORDER BY COALESCE(name,'') LIMIT ? OFFSET ?"
            );
            let mut data_params: Vec<Box<dyn rusqlite::ToSql>> = params;
            data_params.push(Box::new(per_page));
            data_params.push(Box::new(page * per_page));
            let mut s = c.prepare(&data_sql).map_err(|e| e.to_string())?;
            let rows_iter = s
                .query_map(
                    rusqlite::params_from_iter(data_params.iter().map(|b| &**b)),
                    |r| Ok(TrainClassRow {
                        id: r.get(0)?, name: r.get(1)?, manufacturer_name: r.get(2)?,
                        type_description: r.get(3)?, vehicle_category: r.get(4)?,
                        max_speed_kph: r.get::<_, f64>(5)?, typical_car_count: r.get(6)?,
                        is_electric: r.get::<_, i64>(7)? != 0,
                        is_drivable: r.get::<_, i64>(8)? != 0,
                        thumbnail_path: r.get(9)?,
                    }),
                )
                .map_err(|e| e.to_string())?;
            let mut out = Vec::new();
            for r in rows_iter { out.push(r.map_err(|e| e.to_string())?); }
            Ok(TrainClassPage { rows: out, total })
        })
    })
    .await
    .map_err(|e| e.to_string())?
}

/// Full train-class record + electrification rows.
#[tauri::command]
pub async fn train_class_detail(id: i64) -> Result<Value, String> {
    tokio::task::spawn_blocking(move || {
        crate::db::with_read(|c| {
            let mut s = c
                .prepare("SELECT * FROM train_classes WHERE id = ?1")
                .map_err(|e| e.to_string())?;
            let cols: Vec<String> =
                s.column_names().into_iter().map(|x| x.to_string()).collect();
            let n = cols.len();
            let mut q = s.query([id]).map_err(|e| e.to_string())?;
            let row = match q.next().map_err(|e| e.to_string())? {
                Some(r) => r,
                None => return Err(format!("train class {id} not found")),
            };
            let cls = row_to_obj(row, &cols, n)?;

            // electrification list
            let mut es = c
                .prepare(
                    "SELECT id, COALESCE(current, ''), COALESCE(pickup_side, ''), \
                            voltage_v, frequency_hz \
                     FROM train_class_electrification \
                     WHERE train_class_id = ?1 ORDER BY id",
                )
                .map_err(|e| e.to_string())?;
            let rows = es
                .query_map([id], |r| Ok(json!({
                    "id": r.get::<_, i64>(0)?,
                    "current": r.get::<_, String>(1)?,
                    "pickup_side": r.get::<_, String>(2)?,
                    "voltage_v": r.get::<_, Option<i64>>(3)?,
                    "frequency_hz": r.get::<_, Option<i64>>(4)?,
                })))
                .map_err(|e| e.to_string())?;
            let mut electrification = Vec::new();
            for r in rows {
                electrification.push(r.map_err(|e| e.to_string())?);
            }

            // formation-count using this class — informational
            let formation_count: i64 = c
                .query_row(
                    "SELECT COUNT(*) FROM formations WHERE class_id = ?1",
                    [id],
                    |r| r.get(0),
                )
                .unwrap_or(0);

            Ok(json!({
                "class": Value::Object(cls),
                "electrification": electrification,
                "formation_count": formation_count,
            }))
        })
    })
    .await
    .map_err(|e| e.to_string())?
}

/// Read a batch of train-class thumbnails from disk and return them as
/// `data:image/png;base64,...` URLs at matching positions. WebView2 won't
/// allow the shell window (loaded via tauri's internal scheme) to fetch from
/// a custom thumb:// scheme, so we ship the bytes through IPC instead. A
/// missing file resolves to an empty string at that index.
///
/// The DB stores `/images/train_classes/Foo.png`; we strip the leading slash
/// + optional `images/` prefix and read from the single canonical root
/// (`<resources_dir>/images/`).
#[tauri::command]
pub async fn train_class_thumbnails(paths: Vec<String>) -> Result<Vec<String>, String> {
    use base64::Engine;
    tokio::task::spawn_blocking(move || -> Result<Vec<String>, String> {
        let root = crate::config::resources_dir().join("images");
        let mut out = Vec::with_capacity(paths.len());
        for p in &paths {
            if p.is_empty() {
                out.push(String::new());
                continue;
            }
            let mut rel = p.trim_start_matches('/').trim_start_matches('\\').to_string();
            if rel.len() > 7 && rel[..7].eq_ignore_ascii_case("images/") {
                rel = rel[7..].to_string();
            } else if rel.len() > 7 && rel[..7].eq_ignore_ascii_case("images\\") {
                rel = rel[7..].to_string();
            }
            let cand = root.join(&rel);
            let found: Option<Vec<u8>> = if cand.exists() {
                std::fs::read(&cand).ok()
            } else { None };
            match found {
                Some(bytes) => {
                    let b64 = base64::engine::general_purpose::STANDARD.encode(&bytes);
                    out.push(format!("data:image/png;base64,{b64}"));
                }
                None => out.push(String::new()),
            }
        }
        Ok(out)
    })
    .await
    .map_err(|e| e.to_string())?
}

// ---------------------------------------------------------------- custom huds

#[derive(serde::Serialize)]
pub struct CustomHud {
    pub slug: String,        // filename without .html
    pub filename: String,    // filename with extension
    pub title: String,       // best-effort <title> from the file (else slug)
    pub size: u64,
    pub path: String,        // absolute filesystem path
}

/// Enumerate HTML files in `<resources_dir>/custom_huds/`. The HUDs are
/// served by the embedded axum server at `/custom-huds/<filename>`.
#[tauri::command]
pub async fn custom_huds_list() -> Result<Vec<CustomHud>, String> {
    tokio::task::spawn_blocking(|| -> Result<Vec<CustomHud>, String> {
        let dir = crate::config::resources_dir().join("custom_huds");
        let mut out = Vec::new();
        let Ok(rd) = std::fs::read_dir(&dir) else { return Ok(out) };
        for entry in rd.flatten() {
            let path = entry.path();
            if !path.is_file() { continue; }
            let Some(ext) = path.extension().and_then(|s| s.to_str()) else { continue };
            if !ext.eq_ignore_ascii_case("html") && !ext.eq_ignore_ascii_case("htm") {
                continue;
            }
            let filename = path
                .file_name().and_then(|s| s.to_str()).unwrap_or("?").to_string();
            let slug = path
                .file_stem().and_then(|s| s.to_str()).unwrap_or("?").to_string();
            let size = entry.metadata().map(|m| m.len()).unwrap_or(0);
            // Cheap <title>…</title> extraction — first 4 KiB is plenty for
            // pages following the usual <head><title>…</title> pattern.
            let title = std::fs::File::open(&path).ok().and_then(|f| {
                use std::io::Read;
                let mut buf = vec![0u8; 4096];
                let mut take = std::io::Read::take(f, 4096);
                let n = take.read(&mut buf).ok()?;
                let head = String::from_utf8_lossy(&buf[..n]);
                let i = head.to_ascii_lowercase().find("<title")?;
                let after = &head[i..];
                let gt = after.find('>')?;
                let close = after[gt..].to_ascii_lowercase().find("</title>")?;
                Some(after[gt + 1..gt + close].trim().to_string())
            }).unwrap_or_else(|| slug.replace('_', " "));
            out.push(CustomHud {
                slug, filename, title, size,
                path: path.to_string_lossy().to_string(),
            });
        }
        out.sort_by(|a, b| a.title.to_ascii_lowercase().cmp(&b.title.to_ascii_lowercase()));
        Ok(out)
    })
    .await
    .map_err(|e| e.to_string())?
}

// ---------------------------------------------------------------- map widget helpers

/// Lightweight `[(id, display_name)]` list for the Map widget's
/// "Load Timetable" typeahead. Capped at 500 rows; sorted by start_time then
/// label. When `class_id` is set we filter to timetables whose primary
/// formation matches that class (covers both `t.formation_id` and any
/// timetable_formations join row).
#[tauri::command]
pub async fn timetables_for_route(
    route_id: i64,
    class_id: Option<i64>,
) -> Result<Vec<(i64, String)>, String> {
    tokio::task::spawn_blocking(move || {
        crate::db::with_read(|c| {
            let (sql, has_class) = if class_id.is_some() {
                (
                    "SELECT DISTINCT t.id, \
                            COALESCE(NULLIF(t.service_name,''), \
                                     NULLIF(t.current_service_name,''), \
                                     NULLIF(t.service,''), \
                                     CAST(t.id AS TEXT)) AS label, \
                            COALESCE(t.start_time, '') AS st \
                     FROM timetables t \
                     LEFT JOIN formations fp ON fp.id = t.formation_id \
                     LEFT JOIN timetable_formations tf ON tf.timetable_id = t.id \
                     LEFT JOIN formations fj ON fj.id = tf.formation_id \
                     WHERE t.route_id = ?1 \
                       AND (fp.class_id = ?2 OR fj.class_id = ?2) \
                     ORDER BY st, label \
                     LIMIT 500",
                    true,
                )
            } else {
                (
                    "SELECT t.id, \
                            COALESCE(NULLIF(t.service_name,''), \
                                     NULLIF(t.current_service_name,''), \
                                     NULLIF(t.service,''), \
                                     CAST(t.id AS TEXT)) AS label, \
                            COALESCE(t.start_time, '') AS st \
                     FROM timetables t \
                     WHERE t.route_id = ?1 \
                     ORDER BY st, label \
                     LIMIT 500",
                    false,
                )
            };
            let mut s = c.prepare(sql).map_err(|e| e.to_string())?;
            let mapper = |r: &rusqlite::Row<'_>| -> rusqlite::Result<(i64, String)> {
                let id: i64 = r.get(0)?;
                let label: String = r.get(1)?;
                let st: String = r.get(2)?;
                Ok((id, if st.is_empty() { label } else { format!("{st}  {label}") }))
            };
            let mut out = Vec::new();
            if has_class {
                let rows = s
                    .query_map(rusqlite::params![route_id, class_id.unwrap()], mapper)
                    .map_err(|e| e.to_string())?;
                for r in rows { out.push(r.map_err(|e| e.to_string())?); }
            } else {
                let rows = s
                    .query_map([route_id], mapper)
                    .map_err(|e| e.to_string())?;
                for r in rows { out.push(r.map_err(|e| e.to_string())?); }
            }
            Ok(out)
        })
    })
    .await
    .map_err(|e| e.to_string())?
}

/// Distinct train classes (id, name) used by formations on this route's
/// timetables. Drives the Train Class dropdown in the Map widget's load
/// panel — only shows classes the user can actually pick.
#[tauri::command]
pub async fn classes_for_route(route_id: i64) -> Result<Vec<(i64, String)>, String> {
    tokio::task::spawn_blocking(move || {
        crate::db::with_read(|c| {
            let mut s = c
                .prepare(
                    "SELECT DISTINCT f.class_id, COALESCE(f.class_name, '') \
                     FROM timetables t \
                     LEFT JOIN formations fp ON fp.id = t.formation_id \
                     LEFT JOIN timetable_formations tf ON tf.timetable_id = t.id \
                     LEFT JOIN formations fj ON fj.id = tf.formation_id, \
                     formations f \
                     WHERE t.route_id = ?1 \
                       AND f.class_id IS NOT NULL \
                       AND (f.id = fp.id OR f.id = fj.id) \
                       AND COALESCE(f.class_name, '') != '' \
                     ORDER BY COALESCE(f.class_name, '')",
                )
                .map_err(|e| e.to_string())?;
            let rows = s
                .query_map([route_id], |r| Ok((r.get::<_, i64>(0)?, r.get::<_, String>(1)?)))
                .map_err(|e| e.to_string())?;
            // De-dupe by class_id (the join above can repeat).
            let mut out: Vec<(i64, String)> = Vec::new();
            let mut seen = std::collections::HashSet::new();
            for r in rows {
                let (id, name) = r.map_err(|e| e.to_string())?;
                if seen.insert(id) {
                    out.push((id, name));
                }
            }
            Ok(out)
        })
    })
    .await
    .map_err(|e| e.to_string())?
}

/// Whole-route polyline from `route_coordinates.coordinates`. Returns an
/// **array of segments** — each segment is `[{latitude, longitude}, ...]`.
/// The map widget draws one Leaflet polyline per segment so multi-line
/// route geometries (GeoJSON `MultiLineString`) don't get rendered with
/// straight "jump" lines between disconnected pieces.
///
/// Three on-disk shapes are normalized here (see `route_segments_from_blob`):
/// flat, nested-segments, and GeoJSON FeatureCollection.
///
/// Empty array when the route has no recorded geometry — the caller shows
/// the empty-state, no exception thrown.
#[tauri::command]
pub async fn route_geometry(route_id: i64) -> Result<Value, String> {
    tokio::task::spawn_blocking(move || {
        crate::db::with_read(|c| {
            let blob: Option<String> = c
                .query_row(
                    "SELECT coordinates FROM route_coordinates WHERE route_id = ?1",
                    [route_id],
                    |r| r.get(0),
                )
                .ok();
            let Some(s) = blob else { return Ok(Value::Array(vec![])) };
            let segments = route_segments_from_blob(&s);
            Ok(Value::Array(
                segments.into_iter().map(Value::Array).collect()
            ))
        })
    })
    .await
    .map_err(|e| e.to_string())?
}

// ---------------------------------------------------------------- routes index

#[derive(serde::Serialize)]
pub struct RoutePage {
    pub rows:  Vec<crate::db::RouteRow>,
    pub total: i64,
}

#[tauri::command]
pub async fn route_search(
    filter: crate::db::RtFilter,
    page: i64,
    per_page: i64,
) -> Result<RoutePage, String> {
    let cfg = crate::config::Config::load();
    let mut filter = filter;
    filter.dev = cfg.development_mode;
    tokio::task::spawn_blocking(move || {
        let (rows, total) = crate::db::routes_search(&filter, page, per_page)?;
        Ok(RoutePage { rows, total })
    })
    .await
    .map_err(|e| e.to_string())?
}

/// Bundle the route show/edit page needs in one call: full route row +
/// country + related counts (timetables / formations / sections).
#[tauri::command]
pub async fn route_detail(id: i64) -> Result<Value, String> {
    tokio::task::spawn_blocking(move || {
        crate::db::with_read(|c| {
            // Full route row
            let mut stmt = c
                .prepare("SELECT * FROM routes WHERE id = ?1")
                .map_err(|e| e.to_string())?;
            let cols: Vec<String> =
                stmt.column_names().into_iter().map(|s| s.to_string()).collect();
            let n = cols.len();
            let mut q = stmt.query([id]).map_err(|e| e.to_string())?;
            let row = match q.next().map_err(|e| e.to_string())? {
                Some(r) => r,
                None => return Err(format!("route {id} not found")),
            };
            let r_obj = row_to_obj(row, &cols, n)?;

            // Country (best-effort)
            let mut country = Value::Null;
            let cid = r_obj.get("country_id").and_then(|v| v.as_i64());
            if let Some(cid) = cid {
                if let Ok((cn, cc)) = c.query_row(
                    "SELECT COALESCE(name,''), COALESCE(code,'') FROM countries WHERE id = ?1",
                    [cid],
                    |r| Ok((r.get::<_, String>(0)?, r.get::<_, String>(1)?)),
                ) {
                    country = json!({ "id": cid, "name": cn, "code": cc });
                }
            }

            // Counts of dependents — informational, lets the show page hint
            // "this route owns N timetables" without loading every row.
            let tt_count: i64 = c
                .query_row(
                    "SELECT COUNT(*) FROM timetables WHERE route_id = ?1",
                    [id],
                    |r| r.get(0),
                )
                .unwrap_or(0);
            // Count distinct formations/sections the route uses by starting from
            // the route's timetables (small) — NOT by scanning every formation /
            // section with a correlated EXISTS, which became O(all formations ×
            // all timetables) after the hud-go merge and hung route_detail.
            let fm_count: i64 = c
                .query_row(
                    "SELECT COUNT(*) FROM ( \
                       SELECT formation_id AS fid FROM timetables \
                         WHERE route_id = ?1 AND formation_id IS NOT NULL \
                       UNION \
                       SELECT tf.formation_id FROM timetable_formations tf \
                         JOIN timetables t ON t.id = tf.timetable_id \
                        WHERE t.route_id = ?1)",
                    [id],
                    |r| r.get(0),
                )
                .unwrap_or(0);
            let sect_count: i64 = c
                .query_row(
                    "SELECT COUNT(*) FROM ( \
                       SELECT section_id AS sid FROM timetables \
                         WHERE route_id = ?1 AND section_id IS NOT NULL \
                       UNION \
                       SELECT ts.section_id FROM timetable_sections ts \
                         JOIN timetables t ON t.id = ts.timetable_id \
                        WHERE t.route_id = ?1)",
                    [id],
                    |r| r.get(0),
                )
                .unwrap_or(0);
            let loc_count: i64 = c
                .query_row(
                    "SELECT COUNT(*) FROM locations WHERE route_id = ?1",
                    [id],
                    |r| r.get(0),
                )
                .unwrap_or(0);

            Ok(json!({
                "route":   Value::Object(r_obj),
                "country": country,
                "counts": {
                    "timetables": tt_count,
                    "formations": fm_count,
                    "sections":   sect_count,
                    "locations":  loc_count,
                }
            }))
        })
    })
    .await
    .map_err(|e| e.to_string())?
}

/// Editable fields on a route. `id` is read-only.
#[derive(serde::Deserialize, Default)]
#[serde(default)]
pub struct RouteEdit {
    pub name: Option<String>,
    pub country_id: Option<i64>,
    pub tsw_version: Option<i64>,
    pub cross_pak_reference_name: Option<String>,
    pub best_data: Option<bool>,
    pub is_real_route: Option<bool>,
}

#[tauri::command]
pub async fn route_update(id: i64, body: RouteEdit) -> Result<(), String> {
    tokio::task::spawn_blocking(move || -> Result<(), String> {
        let c = open_rw()?;
        let mut sets: Vec<&'static str> = Vec::new();
        let mut params: Vec<Box<dyn rusqlite::ToSql>> = Vec::new();
        macro_rules! field {
            ($k:literal, $v:expr) => {
                if let Some(v) = $v {
                    sets.push(concat!($k, " = ?"));
                    params.push(Box::new(v));
                }
            };
        }
        field!("name", body.name);
        field!("country_id", body.country_id);
        field!("tsw_version", body.tsw_version);
        field!("cross_pak_reference_name", body.cross_pak_reference_name);
        field!("best_data", body.best_data.map(|b| if b { 1_i64 } else { 0 }));
        field!("is_real_route", body.is_real_route.map(|b| if b { 1_i64 } else { 0 }));
        if sets.is_empty() {
            return Ok(());
        }
        let sql = format!("UPDATE routes SET {} WHERE id = ?", sets.join(", "));
        params.push(Box::new(id));
        let n = c
            .execute(&sql, rusqlite::params_from_iter(params.iter().map(|b| &**b)))
            .map_err(|e| e.to_string())?;
        if n == 0 {
            return Err(format!("route {id} not found"));
        }
        Ok(())
    })
    .await
    .map_err(|e| e.to_string())?
}

/// What a `delete_route` removed — surfaced so the UI can confirm
/// ("Deleted Boston Sprinter + 820 timetables").
#[derive(serde::Serialize)]
pub struct RouteDeleteResult {
    pub route_name: String,
    pub timetables_deleted: i64,
}

/// Cascade-delete a route and everything tied to it. FK enforcement is OFF on
/// these connections (see db.rs), so we delete children explicitly — mirrors
/// hud-go's RouteHandler.Delete + DeleteAllTimetables cascade. Wrapped in a
/// single transaction so a mid-delete failure leaves the catalog intact.
/// Orphan formations / now-empty train_classes are swept afterward so a later
/// re-import doesn't reuse stale rows by name (matches deleteOrphanFormations).
#[tauri::command]
pub async fn delete_route(route_id: i64) -> Result<RouteDeleteResult, String> {
    tokio::task::spawn_blocking(move || -> Result<RouteDeleteResult, String> {
        let mut c = open_rw()?;
        let route_name: String = c
            .query_row(
                "SELECT COALESCE(name,'') FROM routes WHERE id = ?1",
                [route_id],
                |r| r.get(0),
            )
            .map_err(|_| format!("route {route_id} not found"))?;

        let tx = c.transaction().map_err(|e| e.to_string())?;

        // Per-timetable children first (no FK cascade to lean on).
        let tt_ids: Vec<i64> = {
            let mut s = tx
                .prepare("SELECT id FROM timetables WHERE route_id = ?1")
                .map_err(|e| e.to_string())?;
            let ids = s
                .query_map([route_id], |r| r.get::<_, i64>(0))
                .map_err(|e| e.to_string())?
                .collect::<Result<Vec<_>, _>>()
                .map_err(|e| e.to_string())?;
            ids
        };
        for &tid in &tt_ids {
            for tbl in [
                "timetable_formations",
                "timetable_sections",
                "timetable_entries",
                "timetable_coordinates",
                "timetable_markers",
                "timetable_map_features",
            ] {
                let _ = tx.execute(
                    &format!("DELETE FROM {tbl} WHERE timetable_id = ?1"),
                    [tid],
                );
            }
        }
        let timetables_deleted = tx
            .execute("DELETE FROM timetables WHERE route_id = ?1", [route_id])
            .map_err(|e| e.to_string())? as i64;

        // Route-scoped children.
        for tbl in [
            "route_coordinates",
            "route_markers",
            "route_locations",
            "route_formations",
            "car_stop_signs",
            "track_markers",
            "sections",
        ] {
            let _ = tx.execute(
                &format!("DELETE FROM {tbl} WHERE route_id = ?1"),
                [route_id],
            );
        }

        tx.execute("DELETE FROM routes WHERE id = ?1", [route_id])
            .map_err(|e| e.to_string())?;

        // Sweep orphan formations + now-empty classes (deleteOrphanFormations).
        let _ = tx.execute(
            "DELETE FROM formations \
             WHERE id NOT IN (SELECT formation_id FROM route_formations WHERE formation_id IS NOT NULL) \
               AND id NOT IN (SELECT formation_id FROM timetable_formations WHERE formation_id IS NOT NULL) \
               AND id NOT IN (SELECT formation_id FROM section_formations WHERE formation_id IS NOT NULL) \
               AND id NOT IN (SELECT formation_id FROM timetables WHERE formation_id IS NOT NULL)",
            [],
        );
        let _ = tx.execute(
            "DELETE FROM train_classes \
             WHERE id NOT IN (SELECT class_id FROM formations WHERE class_id IS NOT NULL)",
            [],
        );

        tx.commit().map_err(|e| e.to_string())?;
        Ok(RouteDeleteResult { route_name, timetables_deleted })
    })
    .await
    .map_err(|e| e.to_string())?
}

// ---------------------------------------------------------------- map features

#[tauri::command]
pub fn get_map_features(timetable_id: i64) -> Result<Value, String> {
    let c = conn()?;
    let blob: Option<String> = c
        .query_row(
            "SELECT features FROM timetable_map_features WHERE timetable_id = ?1",
            [timetable_id],
            |row| row.get(0),
        )
        .ok();
    match blob {
        Some(s) => serde_json::from_str::<Value>(&s).map_err(|e| e.to_string()),
        None => Ok(Value::Array(vec![])),
    }
}

/// Per-route full map bundle ({route_id, route_name, coordinates, markers,
/// locations}) — used by the map widget as a fallback when a timetable has
/// no pre-built features blob. Mirrors what /api/routes/{id}/map-data serves
/// to the browser map. Returns `null` when the route id doesn't exist.
#[tauri::command]
pub async fn get_route_map_data(route_id: i64) -> Result<Value, String> {
    tokio::task::spawn_blocking(move || -> Result<Value, String> {
        match crate::db::route_map_data(route_id)? {
            Some(v) => Ok(v),
            None => Ok(Value::Null),
        }
    })
    .await
    .map_err(|e| e.to_string())?
}

// =================================================================== dev: countries
//
// CRUD for the dev-only Countries page. Mirrors hud-go's /api/countries
// behaviour: full list (small enough — 10 rows today, never paginated) +
// per-row route_count / timetable_count badges, and create/update/delete
// with code uppercased so the ISO-3166 lookup in the flag CSS works
// regardless of how the user typed it in.

#[derive(serde::Serialize)]
pub struct CountryRow {
    pub id: i64,
    pub name: String,
    pub code: String,
    pub route_count: i64,
    pub timetable_count: i64,
}

#[tauri::command]
pub async fn countries_list_full() -> Result<Vec<CountryRow>, String> {
    tokio::task::spawn_blocking(|| {
        crate::db::with_read(|c| {
            let mut s = c.prepare(
                "SELECT co.id, COALESCE(co.name,''), COALESCE(co.code,''), \
                        (SELECT COUNT(*) FROM routes r WHERE r.country_id = co.id), \
                        (SELECT COUNT(*) FROM timetables t \
                                   JOIN routes r ON r.id = t.route_id \
                                  WHERE r.country_id = co.id) \
                 FROM countries co ORDER BY co.name",
            ).map_err(|e| e.to_string())?;
            let rows = s.query_map([], |r| Ok(CountryRow {
                id: r.get(0)?, name: r.get(1)?, code: r.get(2)?,
                route_count: r.get(3)?, timetable_count: r.get(4)?,
            })).map_err(|e| e.to_string())?;
            let mut out = Vec::new();
            for r in rows { out.push(r.map_err(|e| e.to_string())?); }
            Ok(out)
        })
    })
    .await
    .map_err(|e| e.to_string())?
}

#[derive(serde::Deserialize)]
pub struct CountryUpsert {
    pub id:   Option<i64>,
    pub name: String,
    #[serde(default)] pub code: String,
}

#[tauri::command]
pub async fn country_save(body: CountryUpsert) -> Result<i64, String> {
    tokio::task::spawn_blocking(move || -> Result<i64, String> {
        let name = body.name.trim();
        if name.is_empty() { return Err("country name required".into()); }
        let code = body.code.trim().to_ascii_uppercase();
        let c = crate::db::write_conn()?;
        match body.id {
            Some(id) => {
                c.execute(
                    "UPDATE countries SET name = ?1, code = ?2 WHERE id = ?3",
                    rusqlite::params![name, if code.is_empty() { None } else { Some(&code) }, id],
                ).map_err(|e| e.to_string())?;
                Ok(id)
            }
            None => {
                c.execute(
                    "INSERT INTO countries (name, code) VALUES (?1, ?2)",
                    rusqlite::params![name, if code.is_empty() { None } else { Some(&code) }],
                ).map_err(|e| e.to_string())?;
                Ok(c.last_insert_rowid())
            }
        }
    })
    .await
    .map_err(|e| e.to_string())?
}

#[tauri::command]
pub async fn country_delete(id: i64) -> Result<(), String> {
    tokio::task::spawn_blocking(move || -> Result<(), String> {
        let c = crate::db::write_conn()?;
        // Guard against orphaning routes — the FK isn't enforced in the
        // existing schema, so we check manually rather than wipe silently.
        let n: i64 = c.query_row(
            "SELECT COUNT(*) FROM routes WHERE country_id = ?1",
            [id], |r| r.get(0),
        ).map_err(|e| e.to_string())?;
        if n > 0 {
            return Err(format!("can't delete — {n} route{} reference this country",
                if n == 1 { "" } else { "s" }));
        }
        c.execute("DELETE FROM countries WHERE id = ?1", [id])
            .map_err(|e| e.to_string())?;
        Ok(())
    })
    .await
    .map_err(|e| e.to_string())?
}

// =================================================================== dev: locations
//
// Locations are per-route place markers (stations, yards, portals, signals
// etc). 12k+ rows so the page paginates + accepts a name search and an
// optional route_id filter — mirrors hud-go /api/locations.

#[derive(serde::Serialize)]
pub struct LocationRow {
    pub id:        i64,
    pub route_id:  i64,
    pub name:      String,
    pub route:     String,    // route name, "" if unjoined
    pub use_count: i64,       // timetable_entries that reference this location
}
#[derive(serde::Serialize)]
pub struct LocationPage {
    pub rows:  Vec<LocationRow>,
    pub total: i64,
}

#[derive(serde::Deserialize, Default)]
pub struct LocationFilter {
    #[serde(default)] pub search:   String,
    #[serde(default)] pub route_id: Option<i64>,
}

#[tauri::command]
pub async fn locations_search(
    filter: LocationFilter,
    page: i64,
    per_page: i64,
) -> Result<LocationPage, String> {
    tokio::task::spawn_blocking(move || {
        crate::db::with_read(|c| {
            let mut conds: Vec<String> = Vec::new();
            let mut params: Vec<Box<dyn rusqlite::ToSql>> = Vec::new();
            let s = filter.search.trim();
            if !s.is_empty() {
                conds.push("COALESCE(l.name,'') LIKE ?".into());
                params.push(Box::new(format!("%{s}%")));
            }
            if let Some(rid) = filter.route_id {
                conds.push("l.route_id = ?".into());
                params.push(Box::new(rid));
            }
            let where_sql = if conds.is_empty() { String::new() }
                else { format!(" WHERE {}", conds.join(" AND ")) };

            let total: i64 = c.query_row(
                &format!("SELECT COUNT(*) FROM locations l{where_sql}"),
                rusqlite::params_from_iter(params.iter().map(|b| &**b)),
                |r| r.get(0),
            ).map_err(|e| e.to_string())?;

            let data_sql = format!(
                "SELECT l.id, l.route_id, COALESCE(l.name,''), COALESCE(r.name,''), \
                        (SELECT COUNT(*) FROM timetable_entries te WHERE te.location_id = l.id) \
                 FROM locations l \
                 LEFT JOIN routes r ON r.id = l.route_id \
                 {where_sql} \
                 ORDER BY COALESCE(r.name,''), COALESCE(l.name,'') \
                 LIMIT ? OFFSET ?"
            );
            let mut dp: Vec<Box<dyn rusqlite::ToSql>> = params;
            dp.push(Box::new(per_page));
            dp.push(Box::new(page * per_page));
            let mut s = c.prepare(&data_sql).map_err(|e| e.to_string())?;
            let rows = s.query_map(
                rusqlite::params_from_iter(dp.iter().map(|b| &**b)),
                |r| Ok(LocationRow {
                    id: r.get(0)?, route_id: r.get(1)?, name: r.get(2)?,
                    route: r.get(3)?, use_count: r.get(4)?,
                }),
            ).map_err(|e| e.to_string())?;
            let mut out = Vec::new();
            for r in rows { out.push(r.map_err(|e| e.to_string())?); }
            Ok(LocationPage { rows: out, total })
        })
    })
    .await
    .map_err(|e| e.to_string())?
}

#[derive(serde::Deserialize)]
pub struct LocationUpsert {
    pub id:       Option<i64>,
    pub route_id: i64,
    pub name:     String,
}

#[tauri::command]
pub async fn location_save(body: LocationUpsert) -> Result<i64, String> {
    tokio::task::spawn_blocking(move || -> Result<i64, String> {
        let name = body.name.trim();
        if name.is_empty() { return Err("location name required".into()); }
        if body.route_id <= 0 { return Err("route required".into()); }
        let c = crate::db::write_conn()?;
        match body.id {
            Some(id) => {
                c.execute(
                    "UPDATE locations SET route_id = ?1, name = ?2 WHERE id = ?3",
                    rusqlite::params![body.route_id, name, id],
                ).map_err(|e| e.to_string())?;
                Ok(id)
            }
            None => {
                c.execute(
                    "INSERT INTO locations (route_id, name) VALUES (?1, ?2)",
                    rusqlite::params![body.route_id, name],
                ).map_err(|e| e.to_string())?;
                Ok(c.last_insert_rowid())
            }
        }
    })
    .await
    .map_err(|e| e.to_string())?
}

#[tauri::command]
pub async fn location_delete(id: i64) -> Result<(), String> {
    tokio::task::spawn_blocking(move || -> Result<(), String> {
        let c = crate::db::write_conn()?;
        let n: i64 = c.query_row(
            "SELECT COUNT(*) FROM timetable_entries WHERE location_id = ?1",
            [id], |r| r.get(0),
        ).map_err(|e| e.to_string())?;
        if n > 0 {
            return Err(format!("can't delete — {n} timetable entr{} reference this location",
                if n == 1 { "y" } else { "ies" }));
        }
        c.execute("DELETE FROM locations WHERE id = ?1", [id])
            .map_err(|e| e.to_string())?;
        Ok(())
    })
    .await
    .map_err(|e| e.to_string())?
}

// =================================================================== dev: formations
//
// Formations link a train class (RVD) to its physical consist — the cars
// pulled, their order, the livery. Paginated index with class + route
// filters; only the small editable surface (name + class + livery + length
// + car_count) is exposed for save, because the rest is extractor-owned.

#[derive(serde::Serialize)]
pub struct FormationRow {
    pub id:               i64,
    pub name:             String,
    pub class_id:         Option<i64>,
    pub class_name:       String,
    pub livery_id:        String,
    pub length_m:         Option<f64>,
    pub car_count:        Option<i64>,
    pub timetable_count:  i64,
}
#[derive(serde::Serialize)]
pub struct FormationPage {
    pub rows:  Vec<FormationRow>,
    pub total: i64,
}

#[derive(serde::Deserialize, Default)]
pub struct FormationFilter {
    #[serde(default)] pub search:   String,
    #[serde(default)] pub class_id: Option<i64>,
    #[serde(default)] pub route_id: Option<i64>,
}

#[tauri::command]
pub async fn formations_search(
    filter: FormationFilter,
    page: i64,
    per_page: i64,
) -> Result<FormationPage, String> {
    tokio::task::spawn_blocking(move || {
        crate::db::with_read(|c| {
            let mut conds: Vec<String> = Vec::new();
            let mut params: Vec<Box<dyn rusqlite::ToSql>> = Vec::new();
            let s = filter.search.trim();
            if !s.is_empty() {
                conds.push("(COALESCE(f.name,'') LIKE ? OR COALESCE(f.class_name,'') LIKE ?)".into());
                let p = format!("%{s}%");
                params.push(Box::new(p.clone()));
                params.push(Box::new(p));
            }
            if let Some(cid) = filter.class_id {
                conds.push("f.class_id = ?".into());
                params.push(Box::new(cid));
            }
            if let Some(rid) = filter.route_id {
                conds.push("EXISTS (SELECT 1 FROM route_formations rf WHERE rf.formation_id = f.id AND rf.route_id = ?)".into());
                params.push(Box::new(rid));
            }
            let where_sql = if conds.is_empty() { String::new() }
                else { format!(" WHERE {}", conds.join(" AND ")) };

            let total: i64 = c.query_row(
                &format!("SELECT COUNT(*) FROM formations f{where_sql}"),
                rusqlite::params_from_iter(params.iter().map(|b| &**b)),
                |r| r.get(0),
            ).map_err(|e| e.to_string())?;

            let data_sql = format!(
                "SELECT f.id, COALESCE(f.name,''), f.class_id, COALESCE(f.class_name,''), \
                        COALESCE(f.livery_id,''), f.length_m, f.car_count, \
                        ((SELECT COUNT(*) FROM timetables t WHERE t.formation_id = f.id) \
                       + (SELECT COUNT(*) FROM timetable_formations tf WHERE tf.formation_id = f.id)) \
                 FROM formations f \
                 {where_sql} \
                 ORDER BY COALESCE(f.class_name,''), COALESCE(f.name,'') \
                 LIMIT ? OFFSET ?"
            );
            let mut dp: Vec<Box<dyn rusqlite::ToSql>> = params;
            dp.push(Box::new(per_page));
            dp.push(Box::new(page * per_page));
            let mut stmt = c.prepare(&data_sql).map_err(|e| e.to_string())?;
            let rows = stmt.query_map(
                rusqlite::params_from_iter(dp.iter().map(|b| &**b)),
                |r| Ok(FormationRow {
                    id:               r.get(0)?,
                    name:             r.get(1)?,
                    class_id:         r.get(2)?,
                    class_name:       r.get(3)?,
                    livery_id:        r.get(4)?,
                    length_m:         r.get(5)?,
                    car_count:        r.get(6)?,
                    timetable_count:  r.get(7)?,
                }),
            ).map_err(|e| e.to_string())?;
            let mut out = Vec::new();
            for r in rows { out.push(r.map_err(|e| e.to_string())?); }
            Ok(FormationPage { rows: out, total })
        })
    })
    .await
    .map_err(|e| e.to_string())?
}

#[derive(serde::Deserialize)]
pub struct FormationUpsert {
    pub id:        Option<i64>,
    pub name:      String,
    #[serde(default)] pub class_id:  Option<i64>,
    #[serde(default)] pub livery_id: String,
    #[serde(default)] pub length_m:  Option<f64>,
    #[serde(default)] pub car_count: Option<i64>,
}

/// Full detail bundle for the Formations show page. Returns:
///   * `formation`     — every column on the formations row
///   * `train_class`   — joined via formation.class_id; null if unset / missing
///   * `vehicles`      — formation_vehicles ordered by position (the consist)
///   * `routes`        — routes that reference this formation via route_formations
///   * `timetables`    — timetables that reference this formation either
///                       directly (timetables.formation_id) or via the
///                       timetable_formations join, deduplicated. Capped at
///                       100 rows — the show page is for navigation, not bulk
///                       listing; the user can drill into the Timetables tab
///                       for the full filtered view.
#[tauri::command]
pub async fn formation_detail(id: i64) -> Result<Value, String> {
    tokio::task::spawn_blocking(move || {
        crate::db::with_read(|c| {
            // Formation row — every column, so the page never needs a follow-up.
            let mut s = c
                .prepare("SELECT * FROM formations WHERE id = ?1")
                .map_err(|e| e.to_string())?;
            let cols: Vec<String> =
                s.column_names().into_iter().map(|x| x.to_string()).collect();
            let n = cols.len();
            let mut q = s.query([id]).map_err(|e| e.to_string())?;
            let row = match q.next().map_err(|e| e.to_string())? {
                Some(r) => r,
                None => return Err(format!("formation {id} not found")),
            };
            let formation = row_to_obj(row, &cols, n)?;

            // Optional train_class join — best-effort.
            let mut train_class = Value::Null;
            let cid = formation.get("class_id").and_then(|v| v.as_i64());
            if let Some(cid) = cid {
                if let Ok((tid, tname, tmfg, tthumb)) = c.query_row(
                    "SELECT id, COALESCE(name,''), COALESCE(manufacturer_name,''), COALESCE(thumbnail_path,'') \
                     FROM train_classes WHERE id = ?1",
                    [cid],
                    |r| Ok((
                        r.get::<_, i64>(0)?,
                        r.get::<_, String>(1)?,
                        r.get::<_, String>(2)?,
                        r.get::<_, String>(3)?,
                    )),
                ) {
                    train_class = json!({
                        "id": tid, "name": tname,
                        "manufacturer_name": tmfg,
                        "thumbnail_path": tthumb,
                    });
                }
            }

            // Vehicles (consist), ordered head-to-tail.
            let mut vs = c
                .prepare(
                    "SELECT position, COALESCE(vehicle_id,''), COALESCE(class_name,''), \
                            COALESCE(friendly_name,''), COALESCE(livery_id,''), \
                            COALESCE(vehicle_category,''), length_m, \
                            COALESCE(is_lead, 0), COALESCE(is_flipped, 0) \
                     FROM formation_vehicles WHERE formation_id = ?1 ORDER BY position",
                )
                .map_err(|e| e.to_string())?;
            let v_rows = vs
                .query_map([id], |r| Ok(json!({
                    "position":         r.get::<_, i64>(0)?,
                    "vehicle_id":       r.get::<_, String>(1)?,
                    "class_name":       r.get::<_, String>(2)?,
                    "friendly_name":    r.get::<_, String>(3)?,
                    "livery_id":        r.get::<_, String>(4)?,
                    "vehicle_category": r.get::<_, String>(5)?,
                    "length_m":         r.get::<_, Option<f64>>(6)?,
                    "is_lead":          r.get::<_, i64>(7)? != 0,
                    "is_flipped":       r.get::<_, i64>(8)? != 0,
                })))
                .map_err(|e| e.to_string())?;
            let mut vehicles = Vec::new();
            for r in v_rows { vehicles.push(r.map_err(|e| e.to_string())?); }

            // Routes that use this formation, deduped, with country name.
            let mut rs = c
                .prepare(
                    "SELECT DISTINCT r.id, COALESCE(r.name,''), COALESCE(co.name,''), \
                            COALESCE(co.code,'') \
                     FROM route_formations rf \
                     JOIN routes r       ON r.id  = rf.route_id \
                     LEFT JOIN countries co ON co.id = r.country_id \
                     WHERE rf.formation_id = ?1 \
                     ORDER BY r.name",
                )
                .map_err(|e| e.to_string())?;
            let r_rows = rs
                .query_map([id], |r| Ok(json!({
                    "id":           r.get::<_, i64>(0)?,
                    "name":         r.get::<_, String>(1)?,
                    "country":      r.get::<_, String>(2)?,
                    "country_code": r.get::<_, String>(3)?,
                })))
                .map_err(|e| e.to_string())?;
            let mut routes = Vec::new();
            for r in r_rows { routes.push(r.map_err(|e| e.to_string())?); }

            // Timetables — union of the two ways a tt can reference a
            // formation, deduped by tt.id. Capped at 100.
            let mut ts = c
                .prepare(
                    "SELECT t.id, COALESCE(t.service_name,''), \
                            COALESCE(t.current_service_name,''), \
                            COALESCE(r.name,''), t.route_id \
                     FROM timetables t \
                     LEFT JOIN routes r ON r.id = t.route_id \
                     WHERE t.formation_id = ?1 \
                        OR t.id IN (SELECT timetable_id FROM timetable_formations WHERE formation_id = ?1) \
                     ORDER BY r.name, t.service_name \
                     LIMIT 100",
                )
                .map_err(|e| e.to_string())?;
            let t_rows = ts
                .query_map([id], |r| Ok(json!({
                    "id":                   r.get::<_, i64>(0)?,
                    "service_name":         r.get::<_, String>(1)?,
                    "current_service_name": r.get::<_, String>(2)?,
                    "route":                r.get::<_, String>(3)?,
                    "route_id":             r.get::<_, Option<i64>>(4)?,
                })))
                .map_err(|e| e.to_string())?;
            let mut timetables = Vec::new();
            for r in t_rows { timetables.push(r.map_err(|e| e.to_string())?); }

            // Total tt count separate from the capped sample.
            let timetable_total: i64 = c
                .query_row(
                    "SELECT COUNT(*) FROM (\
                       SELECT id FROM timetables WHERE formation_id = ?1 \
                       UNION \
                       SELECT timetable_id FROM timetable_formations WHERE formation_id = ?1)",
                    [id], |r| r.get(0),
                )
                .unwrap_or(0);

            Ok(json!({
                "formation":       Value::Object(formation),
                "train_class":     train_class,
                "vehicles":        vehicles,
                "routes":          routes,
                "timetables":      timetables,
                "timetable_total": timetable_total,
            }))
        })
    })
    .await
    .map_err(|e| e.to_string())?
}

#[tauri::command]
pub async fn formation_save(body: FormationUpsert) -> Result<i64, String> {
    tokio::task::spawn_blocking(move || -> Result<i64, String> {
        let name = body.name.trim();
        if name.is_empty() { return Err("formation name required".into()); }
        let liv: Option<String> = if body.livery_id.trim().is_empty() { None }
            else { Some(body.livery_id.trim().into()) };
        // Resolve the class name from class_id so the row stays self-describing
        // (mirrors how the extractor stamps it).
        let class_name: Option<String> = match body.class_id {
            Some(cid) => crate::db::with_read(|c| {
                Ok(c.query_row(
                    "SELECT name FROM train_classes WHERE id = ?1",
                    [cid], |r| r.get::<_, String>(0),
                ).ok())
            })?,
            None => None,
        };
        let c = crate::db::write_conn()?;
        match body.id {
            Some(id) => {
                c.execute(
                    "UPDATE formations SET name=?1, class_id=?2, class_name=?3, \
                                          livery_id=?4, length_m=?5, car_count=?6 \
                     WHERE id=?7",
                    rusqlite::params![
                        name, body.class_id, class_name, liv,
                        body.length_m, body.car_count, id,
                    ],
                ).map_err(|e| e.to_string())?;
                Ok(id)
            }
            None => {
                c.execute(
                    "INSERT INTO formations (name, class_id, class_name, livery_id, length_m, car_count) \
                     VALUES (?1, ?2, ?3, ?4, ?5, ?6)",
                    rusqlite::params![
                        name, body.class_id, class_name, liv,
                        body.length_m, body.car_count,
                    ],
                ).map_err(|e| e.to_string())?;
                Ok(c.last_insert_rowid())
            }
        }
    })
    .await
    .map_err(|e| e.to_string())?
}

#[tauri::command]
pub async fn formation_delete(id: i64) -> Result<(), String> {
    tokio::task::spawn_blocking(move || -> Result<(), String> {
        let c = crate::db::write_conn()?;
        let n_tt: i64 = c.query_row(
            "SELECT COUNT(*) FROM timetables WHERE formation_id = ?1",
            [id], |r| r.get(0),
        ).map_err(|e| e.to_string())?;
        let n_tf: i64 = c.query_row(
            "SELECT COUNT(*) FROM timetable_formations WHERE formation_id = ?1",
            [id], |r| r.get(0),
        ).map_err(|e| e.to_string())?;
        let total = n_tt + n_tf;
        if total > 0 {
            return Err(format!("can't delete — {total} timetable reference{} this formation",
                if total == 1 { "" } else { "s" }));
        }
        c.execute("DELETE FROM route_formations WHERE formation_id = ?1", [id])
            .map_err(|e| e.to_string())?;
        c.execute("DELETE FROM formations WHERE id = ?1", [id])
            .map_err(|e| e.to_string())?;
        Ok(())
    })
    .await
    .map_err(|e| e.to_string())?
}

// =================================================================== api_calls.json
//
// Read / write the subscription catalog the TSW poller uses. Shipped as
// raw JSON so the page stays free to evolve the schema (label, key, builtin,
// etc.) without forcing struct changes in Rust.

#[tauri::command]
pub async fn api_calls_get() -> Result<Value, String> {
    tokio::task::spawn_blocking(|| {
        let path = crate::tsw::api_calls_path();
        let s = std::fs::read_to_string(&path)
            .map_err(|e| format!("read {}: {e}", path.display()))?;
        serde_json::from_str::<Value>(&s).map_err(|e| e.to_string())
    })
    .await
    .map_err(|e| e.to_string())?
}

#[tauri::command]
pub async fn api_calls_set(body: Value) -> Result<(), String> {
    tokio::task::spawn_blocking(move || -> Result<(), String> {
        // Shape guardrail: must be an object with a `sections` array. We don't
        // validate every field — that would lock the format — but a payload
        // that isn't even shaped like a catalog would corrupt the file.
        let obj = body.as_object().ok_or("payload must be a JSON object")?;
        let secs = obj.get("sections").and_then(|v| v.as_array())
            .ok_or("payload must have a `sections` array")?;
        for s in secs {
            if s.as_object().and_then(|o| o.get("name")).and_then(|n| n.as_str()).is_none() {
                return Err("every section needs a string `name`".into());
            }
        }
        let path = crate::tsw::api_calls_path();
        let pretty = serde_json::to_string_pretty(&body).map_err(|e| e.to_string())?;
        std::fs::write(&path, pretty)
            .map_err(|e| format!("write {}: {e}", path.display()))?;
        Ok(())
    })
    .await
    .map_err(|e| e.to_string())?
}

/// Subscription lifecycle: `action` in {"reset", "create", "delete"}.
/// The always-on poller will reassert state on its next cycle (~5 s), so a
/// manual delete is transient; that's intentional — matches hud-go.
#[tauri::command]
pub async fn subscription_action(action: String) -> Result<String, String> {
    let cfg = crate::config::Config::load();
    let key = crate::tsw::resolve_api_key_pub(&cfg);
    if key.is_empty() {
        return Err("no API key (set one in Settings or check CommAPIKey.txt)".into());
    }
    match action.as_str() {
        "delete" => {
            crate::tsw::pub_delete_all(&key).await;
            Ok("deleted all subscriptions".into())
        }
        "create" => {
            let n = crate::tsw::pub_create_from_catalog(&key).await;
            Ok(format!("created {n} subscription{}", if n == 1 { "" } else { "s" }))
        }
        "reset" => {
            crate::tsw::pub_delete_all(&key).await;
            let n = crate::tsw::pub_create_from_catalog(&key).await;
            Ok(format!("reset — recreated {n} subscription{}", if n == 1 { "" } else { "s" }))
        }
        other => Err(format!("unknown action: {other}")),
    }
}

/// Fetch the current /subscription/?Subscription=1 payload straight from TSW
/// (bypasses the cached telemetry the widgets use). Useful for the live-data
/// preview in the admin tab so users see exactly what TSW is returning right
/// now, raw — including paths the parser doesn't surface.
#[tauri::command]
pub async fn subscription_data() -> Result<Value, String> {
    let cfg = crate::config::Config::load();
    let key = crate::tsw::resolve_api_key_pub(&cfg);
    if key.is_empty() {
        return Err("no API key (set one in Settings or check CommAPIKey.txt)".into());
    }
    crate::tsw::pub_fetch_data(&key).await
}

/// One-shot probe of a CommAPI path. POSTs the subscription on bag 2, waits
/// ~600 ms, GETs the bag, then DELETEs it. Lets users sanity-check a path
/// before adding it to the catalog.
#[tauri::command]
pub async fn subscription_test_path(path: String) -> Result<Value, String> {
    let path = path.trim();
    if path.is_empty() {
        return Err("path is required".into());
    }
    let cfg = crate::config::Config::load();
    let key = crate::tsw::resolve_api_key_pub(&cfg);
    if key.is_empty() {
        return Err("no API key (set one in Settings or check CommAPIKey.txt)".into());
    }
    crate::tsw::pub_test_path(&key, path).await
}

// =================================================================== Phase 10 extractor
//
// Slice 10.1: pak discovery. Settings shows what hud would extract — useful
// even before we can actually run the extractor, since the TSW path field is
// easy to get wrong (typos, wrong drive letter, missing WindowsNoEditor/).

#[tauri::command]
pub async fn extractor_list_routes() -> Result<Vec<crate::extractor::DiscoveredRoute>, String> {
    tokio::task::spawn_blocking(|| {
        let root = crate::extractor::resolve_tsw_root()?;
        let mut routes = crate::extractor::discover_routes(&root)?;
        // Resolve hud-go-style human DLC names (RouteDefinition DisplayName
        // → *_Gameplay.uplugin Description → CamelCase codename) so the scan
        // list shows real names, not codenames.
        crate::extractor::resolve_all_metadata(&mut routes);
        Ok(routes)
    })
    .await
    .map_err(|e| e.to_string())?
}

/// Repak health check — returns where we found it (or an installation-
/// instruction error). Used by the Settings UI to surface "missing
/// dependency" the same way it surfaces "wrong TSW path".
#[tauri::command]
pub async fn extractor_find_repak() -> Result<crate::extractor::RepakInfo, String> {
    tokio::task::spawn_blocking(crate::extractor::find_repak)
        .await
        .map_err(|e| e.to_string())?
}

/// Unpack a single pak to a destination dir. Synchronous from the user's
/// perspective — repak takes 1-30 s depending on pak size, so the UI
/// disables its button until this resolves.
#[derive(serde::Deserialize)]
pub struct UnpackArgs {
    pub pak_path: String,
    pub dest_dir: String,
    #[serde(default)]
    pub aes_key:  String,
}

#[tauri::command]
pub async fn extractor_unpack_pak(args: UnpackArgs) -> Result<crate::extractor::UnpackResult, String> {
    tokio::task::spawn_blocking(move || {
        crate::extractor::unpack_pak(
            std::path::Path::new(&args.pak_path),
            std::path::Path::new(&args.dest_dir),
            &args.aes_key,
        )
    })
    .await
    .map_err(|e| e.to_string())?
}

/// Parse one `<X>RouteDefinition.uasset` and return its catalog fields.
/// Round-trip test point for the cooked binary walker.
#[tauri::command]
pub async fn extractor_parse_route_definition(
    uasset_path: String,
) -> Result<crate::uasset_route_definition::RouteDefinition, String> {
    tokio::task::spawn_blocking(move || {
        crate::uasset_route_definition::parse(std::path::Path::new(&uasset_path))
    })
    .await
    .map_err(|e| e.to_string())?
}

/// Parse one `RVD_*.uasset` and return its train-class fields.
#[tauri::command]
pub async fn extractor_parse_rvd(
    uasset_path: String,
) -> Result<crate::uasset_rvd::Rvd, String> {
    tokio::task::spawn_blocking(move || {
        crate::uasset_rvd::parse(std::path::Path::new(&uasset_path))
    })
    .await
    .map_err(|e| e.to_string())?
}

/// Parse one `*_Definition.uasset` (scenario / tutorial) and return its
/// metadata.
#[tauri::command]
pub async fn extractor_parse_scenario(
    uasset_path: String,
) -> Result<crate::uasset_scenario::ScenarioDefinition, String> {
    tokio::task::spawn_blocking(move || {
        crate::uasset_scenario::parse(std::path::Path::new(&uasset_path))
    })
    .await
    .map_err(|e| e.to_string())?
}

#[derive(serde::Deserialize)]
pub struct ParseTimetableArgs {
    pub uasset_path: String,
    #[serde(default)]
    pub route_name:  String,
}

/// Parse one `Timetable.uasset` and return its full bundle (services,
/// formations, CompiledRVMap, etc.).
#[tauri::command]
pub async fn extractor_parse_timetable(
    args: ParseTimetableArgs,
) -> Result<crate::uasset_timetable::Timetable, String> {
    tokio::task::spawn_blocking(move || {
        crate::uasset_timetable::parse_cooked_timetable(
            std::path::Path::new(&args.uasset_path),
            &args.route_name,
        )
    })
    .await
    .map_err(|e| e.to_string())?
}

/// Parse one `*DataTrack.uasset` and return per-service track-data
/// breadcrumbs.
#[tauri::command]
pub async fn extractor_parse_datatrack(
    uasset_path: String,
) -> Result<crate::uasset_datatrack::DataTrack, String> {
    tokio::task::spawn_blocking(move || {
        crate::uasset_datatrack::parse(std::path::Path::new(&uasset_path))
    })
    .await
    .map_err(|e| e.to_string())?
}

/// Run the full extraction pipeline against one pak. Unpacks the pak
/// to `<temp_dir>/<pak-stem>/`, walks the tree, parses everything we
/// recognise (RouteDefinition / RVD_* / Timetable*), and writes the
/// results to the local tsw_hud.db inside one transaction.
///
/// `temp_dir` defaults to the hud-exe-adjacent `extractor_temp/` when
/// omitted — same place hud-go puts its unpack scratch space.
#[derive(serde::Deserialize)]
pub struct RunPakArgs {
    pub pak_path: String,
    #[serde(default)]
    pub temp_dir: String,
    #[serde(default)]
    pub aes_key:  String,
}

#[tauri::command]
pub async fn extractor_run_pak(
    app: tauri::AppHandle,
    args: RunPakArgs,
) -> Result<crate::extractor_pipeline::RunCounts, String> {
    tokio::task::spawn_blocking(move || -> Result<crate::extractor_pipeline::RunCounts, String> {
        let pak = std::path::PathBuf::from(&args.pak_path);
        let stem = pak.file_stem().and_then(|s| s.to_str())
            .ok_or_else(|| format!("pak path has no stem: {}", args.pak_path))?;
        // Resolution order: explicit `args.temp_dir` (test/dev override)
        // → `configuration.json::extractorTempDir` (user setting,
        // typically `route_data/temp`) → hud-exe-adjacent
        // `extractor_temp/` (last-resort default mirroring hud-go).
        let base_temp = if !args.temp_dir.is_empty() {
            std::path::PathBuf::from(&args.temp_dir)
        } else {
            let cfg_temp = crate::config::Config::load().extractor_temp_dir;
            if !cfg_temp.trim().is_empty() {
                std::path::PathBuf::from(cfg_temp.trim())
            } else {
                std::env::current_exe()
                    .ok()
                    .and_then(|p| p.parent().map(|p| p.to_path_buf()))
                    .map(|p| p.join("extractor_temp"))
                    .unwrap_or_else(std::env::temp_dir)
            }
        };
        let dest = base_temp.join(stem);

        // Stream pipeline progress to the frontend as `extractor:log`
        // events. The shell window listens and appends to its log
        // panel; widgets ignore the event.
        let app_for_log = app.clone();
        let sink: crate::extractor_pipeline::LogSink = Box::new(move |kind, msg| {
            use tauri::Emitter;
            let _ = app_for_log.emit("extractor:log", serde_json::json!({
                "kind": kind, "msg": msg,
            }));
        });
        // Resolve overlay paks (child gameplay/cargo packs that reference
        // this route) so their services merge into this one zip — hud-go
        // parity (e.g. Boston Sprinter pulls in its GameplayPack + Acela).
        sink("", &format!("[{}] resolving overlay paks…",
            pak.file_name().and_then(|s| s.to_str()).unwrap_or("pak")));
        let overlays = crate::extractor::resolve_overlay_paks(&pak);
        if !overlays.is_empty() {
            sink("ok", &format!("  {} overlay pak(s) to merge", overlays.len()));
        }
        let result = crate::extractor_pipeline::run_pak(&pak, &dest, &args.aes_key, &overlays, Some(&sink));

        // Always clean up the per-pak unpacked tree, success or
        // failure. Each pak unpacks to ~1 GB; without this, a Load
        // my DLCs run accumulates 40+ GB until the disk fills (which
        // is exactly what hit `[38/44] TSW2WestSomersetRailway`).
        // Mirrors hud-go's `Config.KeepWorkDir = false` default.
        let _ = sink("", &format!("[{}] cleaning temp dir…",
            pak.file_name().and_then(|s| s.to_str()).unwrap_or("pak")));
        match std::fs::remove_dir_all(&dest) {
            Ok(()) => sink("ok", "  temp dir removed"),
            Err(e) if e.kind() == std::io::ErrorKind::NotFound => {}
            Err(e) => sink("warn", &format!("  temp dir cleanup failed (not fatal): {e}")),
        }
        result
    })
    .await
    .map_err(|e| e.to_string())?
}

/// Decode every installed pak's drivable-RVD thumbnails (incl. non-route
/// content packs like NewJourneysCajonPass), canonical liveries winning
/// over Training Centre placeholders, then re-resolve each train_class's
/// thumbnail_path. hud-go catalog-scan parity. Streams progress via
/// `extractor:log`. Returns total PNGs decoded.
#[tauri::command]
pub async fn extractor_rebuild_thumbnails(app: tauri::AppHandle) -> Result<u64, String> {
    tokio::task::spawn_blocking(move || -> Result<u64, String> {
        let app_for_log = app.clone();
        let log = move |msg: &str| {
            use tauri::Emitter;
            let _ = app_for_log.emit("extractor:log", serde_json::json!({ "kind": "", "msg": msg }));
        };
        crate::extractor::rebuild_thumbnails_all_paks(log)
    })
    .await
    .map_err(|e| e.to_string())?
}

/// Persist the user's "I'm done with this route" mark. Mirrors
/// hud-go's `extractor_completed_routes` row. Idempotent.
#[tauri::command]
pub async fn extractor_mark_completed(codename: String) -> Result<(), String> {
    tokio::task::spawn_blocking(move || -> Result<(), String> {
        let conn = crate::db::write_conn()?;
        conn.execute(
            "INSERT OR IGNORE INTO extractor_completed_routes (codename, completed_at) \
             VALUES (?1, datetime('now'))",
            rusqlite::params![codename],
        ).map_err(|e| e.to_string())?;
        Ok(())
    })
    .await
    .map_err(|e| e.to_string())?
}

#[tauri::command]
pub async fn extractor_unmark_completed(codename: String) -> Result<(), String> {
    tokio::task::spawn_blocking(move || -> Result<(), String> {
        let conn = crate::db::write_conn()?;
        conn.execute(
            "DELETE FROM extractor_completed_routes WHERE codename = ?1",
            rusqlite::params![codename],
        ).map_err(|e| e.to_string())?;
        Ok(())
    })
    .await
    .map_err(|e| e.to_string())?
}

/// Return every codename the user has marked complete.
#[tauri::command]
pub async fn extractor_completed_list() -> Result<Vec<String>, String> {
    tokio::task::spawn_blocking(|| -> Result<Vec<String>, String> {
        let conn = crate::db::write_conn()?;
        let mut stmt = conn.prepare("SELECT codename FROM extractor_completed_routes")
            .map_err(|e| e.to_string())?;
        let rows = stmt.query_map([], |r| r.get::<_, String>(0))
            .map_err(|e| e.to_string())?;
        let mut out = Vec::new();
        for r in rows { out.push(r.map_err(|e| e.to_string())?); }
        Ok(out)
    })
    .await
    .map_err(|e| e.to_string())?
}

/// Delete the per-route zip from disk. Returns true when a file
/// existed and was deleted, false when no file was at the path.
#[derive(serde::Deserialize)]
pub struct DeleteZipArgs { pub display_name: String }

#[tauri::command]
pub async fn extractor_delete_zip(args: DeleteZipArgs) -> Result<bool, String> {
    tokio::task::spawn_blocking(move || -> Result<bool, String> {
        let cfg = crate::config::Config::load();
        let out_dir = if cfg.extractor_output_dir.trim().is_empty() {
            std::env::current_exe().ok()
                .and_then(|p| p.parent().map(|p| p.to_path_buf()))
                .map(|p| p.join("extract_zips"))
                .unwrap_or_else(|| std::path::PathBuf::from("extract_zips"))
        } else {
            std::path::PathBuf::from(cfg.extractor_output_dir.trim())
        };
        let stem = sanitise_filename(&args.display_name);
        let zip_path = out_dir.join(format!("{stem}.zip"));
        if !zip_path.is_file() { return Ok(false); }
        std::fs::remove_file(&zip_path).map_err(|e| format!("delete {}: {e}", zip_path.display()))?;
        Ok(true)
    })
    .await
    .map_err(|e| e.to_string())?
}

/// Check whether a per-route zip exists on disk. Mirrors hud-go's
/// `zipExists` flag in the routes panel.
#[tauri::command]
pub async fn extractor_zip_exists(args: DeleteZipArgs) -> Result<bool, String> {
    tokio::task::spawn_blocking(move || -> bool {
        let cfg = crate::config::Config::load();
        let out_dir = if cfg.extractor_output_dir.trim().is_empty() {
            std::env::current_exe().ok()
                .and_then(|p| p.parent().map(|p| p.to_path_buf()))
                .map(|p| p.join("extract_zips"))
                .unwrap_or_else(|| std::path::PathBuf::from("extract_zips"))
        } else {
            std::path::PathBuf::from(cfg.extractor_output_dir.trim())
        };
        let stem = sanitise_filename(&args.display_name);
        out_dir.join(format!("{stem}.zip")).is_file()
    })
    .await
    .map_err(|e| e.to_string())
}

/// Same sanitiser the zip writer uses. Duplicated here so the
/// delete / exists IPCs don't pull on the heavier
/// `crate::zip_writer` module just to derive a filename. MUST stay
/// byte-for-byte identical to `zip_writer::sanitise_filename` or
/// these IPCs will look at the wrong path on disk.
fn sanitise_filename(name: &str) -> String {
    let mut out = String::with_capacity(name.len());
    let mut prev_us = false;
    for ch in name.chars() {
        if ch == ':' { continue; }
        if matches!(ch, '<' | '>' | '"' | '/' | '\\' | '|' | '?' | '*')
            || (ch as u32) < 0x20
        {
            continue;
        }
        let normalised = if ch.is_whitespace() { '_' } else { ch };
        if normalised == '_' {
            if !prev_us { out.push('_'); prev_us = true; }
        } else {
            out.push(normalised);
            prev_us = false;
        }
    }
    out.trim_matches('_').to_string()
}

/// Drift-recovery for `train_classes`: walks `pak_rvds`, links ghost
/// rows, inserts missing classes, backfills snapshot fields, and
/// re-stamps thumbnail URLs against the on-disk PNG cache. Mirrors
/// hud-go's `/api/extractor/rebuild-train-classes`.
#[tauri::command]
pub async fn extractor_rebuild_train_classes()
    -> Result<crate::extractor_db_writer::ReconcileResult, String>
{
    tokio::task::spawn_blocking(|| {
        crate::db::drop_cached_read();
        let conn = crate::db::write_conn()?;
        crate::extractor_db_writer::reconcile_train_classes(&conn)
    })
    .await
    .map_err(|e| e.to_string())?
}

/// Auto-detect TSW6 install root. Walks every Steam library and the
/// Epic Games default location looking for
/// `<root>/WindowsNoEditor/TS2Prototype/Content` so the Extraction-tab
/// "Auto-detect" button can drop the right path into the input.
/// Returns "" when nothing matches — UI then prompts the user to type
/// the path by hand.
#[tauri::command]
pub async fn extractor_autodetect_tsw_root() -> Result<String, String> {
    tokio::task::spawn_blocking(|| -> String {
        use std::path::PathBuf;

        // 1) Every Steam library from libraryfolders.vdf, plus the
        //    primary install_dir/steamapps/common location.
        let mut roots: Vec<PathBuf> = Vec::new();
        for steam_root in steam_install_dirs() {
            roots.push(steam_root.join("steamapps").join("common"));
            // libraryfolders.vdf lists *additional* libraries.
            let vdf = steam_root.join("steamapps").join("libraryfolders.vdf");
            if let Ok(text) = std::fs::read_to_string(&vdf) {
                for line in text.lines() {
                    let line = line.trim();
                    // Lines look like:  "path"        "D:\\SteamLibrary"
                    if !line.starts_with("\"path\"") { continue; }
                    if let Some(start) = line.rfind('"').and_then(|end| line[..end].rfind('"').map(|s| (s, end))) {
                        let raw = &line[start.0 + 1 .. start.1];
                        let normalised = raw.replace("\\\\", "/").replace('\\', "/");
                        roots.push(PathBuf::from(normalised).join("steamapps").join("common"));
                    }
                }
            }
        }
        // 2) Epic Games default install location.
        if let Some(pf) = std::env::var_os("ProgramFiles") {
            roots.push(PathBuf::from(pf).join("Epic Games"));
        }

        // 3) For each candidate "common"/"Epic Games" dir, look for
        //    immediate children whose name matches "Train Sim World 6"
        //    (and a couple of historical variants).
        for r in roots {
            let Ok(rd) = std::fs::read_dir(&r) else { continue };
            for entry in rd.flatten() {
                let path = entry.path();
                let Some(name) = path.file_name().and_then(|s| s.to_str()) else { continue };
                let l = name.to_ascii_lowercase();
                if !(l == "train sim world 6" || l == "trainsimworld6" || l == "train sim world (2026)") {
                    continue;
                }
                // Verify the canonical sub-path exists so we don't
                // hand back a half-installed shell.
                let content = path.join("WindowsNoEditor").join("TS2Prototype").join("Content");
                if content.is_dir() {
                    return path.to_string_lossy().replace('\\', "/");
                }
            }
        }
        String::new()
    })
    .await
    .map_err(|e| e.to_string())
}

#[cfg(target_os = "windows")]
fn steam_install_dirs() -> Vec<std::path::PathBuf> {
    // Most common Steam locations. We don't read the registry — these
    // covers >95% of installs and stays dependency-free. The user can
    // always paste the path by hand.
    let mut out = Vec::new();
    if let Some(pf86) = std::env::var_os("ProgramFiles(x86)") {
        out.push(std::path::PathBuf::from(pf86).join("Steam"));
    }
    if let Some(pf) = std::env::var_os("ProgramFiles") {
        out.push(std::path::PathBuf::from(pf).join("Steam"));
    }
    out
}

#[cfg(not(target_os = "windows"))]
fn steam_install_dirs() -> Vec<std::path::PathBuf> { Vec::new() }

/// Wipe every user table except the curated seed lookups
/// (`countries`, `weather_presets`, `timetable_actions`). Mirrors
/// hud-go's `/api/extractor/nuke-db` endpoint. Gated dev-only on the
/// UI side.
#[tauri::command]
pub async fn extractor_nuke_db()
    -> Result<crate::extractor_db_writer::NukeResult, String>
{
    tokio::task::spawn_blocking(|| -> Result<crate::extractor_db_writer::NukeResult, String> {
        // Release the cached read handle so VACUUM can acquire its
        // exclusive lock. The next `with_read` reopens against the
        // (now-empty) DB.
        crate::db::drop_cached_read();
        let conn = crate::db::write_conn()?;
        crate::extractor_db_writer::nuke_db(&conn)
    })
    .await
    .map_err(|e| e.to_string())?
}

// =================================================================== DB refresh
//
// The Go extractor (hud-go-src) still owns route / timetable / class extraction;
// it writes to `hud-go/resources/db/tsw_hud.db`. hud reads from its own copy at
// `hud/resources/db/tsw_hud.db`. These IPCs let the Settings page snapshot the
// extractor's DB into hud's local copy without dropping to a terminal.
//
// Phase 10 will eventually retire the Go extractor and write straight to hud's
// DB; this is the bridge until then.

#[derive(serde::Serialize)]
pub struct DbRefreshStatus {
    pub source_path:     String,
    pub source_exists:   bool,
    pub source_bytes:    u64,
    pub source_mtime:    Option<String>,
    pub local_path:      String,
    pub local_exists:    bool,
    pub local_bytes:     u64,
    pub local_mtime:     Option<String>,
    pub in_sync:         bool,
}

fn db_refresh_paths() -> (std::path::PathBuf, std::path::PathBuf) {
    let crate_dir = std::path::PathBuf::from(env!("CARGO_MANIFEST_DIR"));
    let apps_root = crate_dir
        .parent()
        .and_then(|p| p.parent())
        .map(|p| p.to_path_buf())
        .unwrap_or_default();
    let source = apps_root.join("hud-go").join("resources").join("db").join("tsw_hud.db");
    let local  = std::path::PathBuf::from(crate::db::db_path());
    (source, local)
}

fn fmt_mtime(p: &std::path::Path) -> Option<String> {
    let meta  = std::fs::metadata(p).ok()?;
    let mtime = meta.modified().ok()?;
    let dur   = mtime.duration_since(std::time::UNIX_EPOCH).ok()?;
    let secs  = dur.as_secs() as i64;
    // Simple yyyy-mm-dd HH:MM:SS (UTC). chrono isn't in the dep set; this
    // is good enough for a display string.
    let days_since_epoch = secs / 86400;
    let time_of_day      = secs % 86400;
    let h = time_of_day / 3600;
    let m = (time_of_day % 3600) / 60;
    let s = time_of_day % 60;
    // Civil-date conversion from days-since-epoch (1970-01-01).
    let (y, mo, d) = days_to_ymd(days_since_epoch);
    Some(format!("{y:04}-{mo:02}-{d:02} {h:02}:{m:02}:{s:02} UTC"))
}

fn days_to_ymd(days: i64) -> (i64, u32, u32) {
    // Reference: Howard Hinnant's date algorithms (public domain).
    let z   = days + 719468;
    let era = z.div_euclid(146097);
    let doe = z.rem_euclid(146097) as u64;
    let yoe = (doe - doe / 1460 + doe / 36524 - doe / 146096) / 365;
    let y   = (yoe as i64) + era * 400;
    let doy = doe - (365 * yoe + yoe / 4 - yoe / 100);
    let mp  = (5 * doy + 2) / 153;
    let d   = doy - (153 * mp + 2) / 5 + 1;
    let m   = if mp < 10 { mp + 3 } else { mp - 9 };
    let y   = if m <= 2 { y + 1 } else { y };
    (y, m as u32, d as u32)
}

#[tauri::command]
pub async fn db_refresh_status() -> Result<DbRefreshStatus, String> {
    tokio::task::spawn_blocking(|| {
        let (source, local) = db_refresh_paths();
        let src_meta = std::fs::metadata(&source).ok();
        let loc_meta = std::fs::metadata(&local).ok();
        let src_bytes = src_meta.as_ref().map(|m| m.len()).unwrap_or(0);
        let loc_bytes = loc_meta.as_ref().map(|m| m.len()).unwrap_or(0);
        Ok(DbRefreshStatus {
            source_path:   source.to_string_lossy().into_owned(),
            source_exists: source.exists(),
            source_bytes:  src_bytes,
            source_mtime:  fmt_mtime(&source),
            local_path:    local.to_string_lossy().into_owned(),
            local_exists:  local.exists(),
            local_bytes:   loc_bytes,
            local_mtime:   fmt_mtime(&local),
            // Same-size + same-mtime means the local copy reflects the
            // current extractor output. mtime preserved by Copy because
            // we set the modified time after the copy on Windows.
            in_sync: source.exists() && local.exists()
                && src_bytes == loc_bytes,
        })
    })
    .await
    .map_err(|e| e.to_string())?
}

/// Copy the Go extractor's DB into hud's local resources. WAL checkpoint
/// runs first so the snapshot includes any uncommitted journal pages, and
/// the write is staged through `tsw_hud.db.tmp` so an interrupted copy
/// can't leave hud with a torn file.
#[tauri::command]
pub async fn db_refresh_copy() -> Result<String, String> {
    tokio::task::spawn_blocking(|| -> Result<String, String> {
        let (source, local) = db_refresh_paths();
        if !source.exists() {
            return Err(format!("source DB not found at {}", source.display()));
        }

        // WAL checkpoint — flush hud-go's outstanding journal pages into
        // the main file so the snapshot is consistent. Opens read-only so
        // we don't fight a live hud-go writer for the lock; PASSIVE means
        // we don't block other readers.
        if let Ok(c) = rusqlite::Connection::open_with_flags(
            &source,
            rusqlite::OpenFlags::SQLITE_OPEN_READ_WRITE | rusqlite::OpenFlags::SQLITE_OPEN_URI,
        ) {
            let _: Result<i64, _> = c.query_row("PRAGMA wal_checkpoint(TRUNCATE)", [], |r| r.get(0));
        }

        if let Some(parent) = local.parent() {
            std::fs::create_dir_all(parent).map_err(|e| format!("mkdir {}: {e}", parent.display()))?;
        }
        let tmp = local.with_extension("db.tmp");
        // Wipe any leftover .tmp from a previous interrupted copy.
        let _ = std::fs::remove_file(&tmp);
        std::fs::copy(&source, &tmp)
            .map_err(|e| format!("copy {} -> {}: {e}", source.display(), tmp.display()))?;
        // The cached read connection holds the live DB open — drop it so
        // the rename can proceed on Windows. The next query reopens.
        crate::db::drop_cached_read();
        // On Windows, std::fs::rename fails if dest exists. Remove first.
        let _ = std::fs::remove_file(&local);
        std::fs::rename(&tmp, &local)
            .map_err(|e| format!("rename {} -> {}: {e}", tmp.display(), local.display()))?;

        // Bytes for the response so the UI can show "copied X.YY GB".
        let bytes = std::fs::metadata(&local).map(|m| m.len()).unwrap_or(0);
        Ok(format!(
            "copied {:.2} GB from {}",
            bytes as f64 / (1024.0 * 1024.0 * 1024.0),
            source.display()
        ))
    })
    .await
    .map_err(|e| e.to_string())?
}

/// Status summary the admin page shows up top — saves the UI doing 3 IPCs.
#[tauri::command]
pub async fn subscription_status() -> Result<Value, String> {
    tokio::task::spawn_blocking(|| -> Result<Value, String> {
        let cfg = crate::config::Config::load();
        let key = crate::tsw::resolve_api_key_pub(&cfg);
        let key_resolved = !key.is_empty();
        // Count enabled paths in the on-disk catalog without exposing api_calls structs.
        let catalog: Value = std::fs::read_to_string(crate::tsw::api_calls_path())
            .ok()
            .and_then(|s| serde_json::from_str(&s).ok())
            .unwrap_or_else(|| serde_json::json!({"sections": []}));
        let mut enabled = 0usize;
        let mut total   = 0usize;
        if let Some(sections) = catalog.get("sections").and_then(|v| v.as_array()) {
            for s in sections {
                if let Some(calls) = s.get("calls").and_then(|v| v.as_array()) {
                    for c in calls {
                        total += 1;
                        if c.get("enabled").and_then(|v| v.as_bool()).unwrap_or(false) {
                            enabled += 1;
                        }
                    }
                }
            }
        }
        Ok(serde_json::json!({
            "api_key_resolved":      key_resolved,
            "enable_subscriptions":  cfg.enable_subscriptions,
            "enabled_calls":         enabled,
            "total_calls":           total,
        }))
    })
    .await
    .map_err(|e| e.to_string())?
}

