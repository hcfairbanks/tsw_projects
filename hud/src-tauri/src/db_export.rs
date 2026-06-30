//! On-demand route export — rebuild a route's shareable zip from the DATABASE
//! (not the pak), so it reflects every post-extraction edit (dedup, renames,
//! durations, sections). Reconstructs the parsed structs the proven
//! `zip_writer` builders expect, then calls `write_route_zip_opts`. Output is
//! byte-format-compatible with what the extractor writes during extraction.

use std::collections::HashMap;
use std::path::Path;

use rusqlite::Connection;

use crate::cookedmap::RouteFeatures;
use crate::output_format::ServiceCoord;
use crate::uasset_route_definition::RouteDefinition;
use crate::uasset_rvd::Rvd;
use crate::uasset_timetable::{Formation, FormationVehicle, ScheduleItem, Service, Timetable};
use crate::zip_writer::ZipResult;

/// Build the export zip for one route into `dest_dir`. Returns the ZipResult
/// (with the on-disk path). Filename is derived from the route's display name,
/// same as the extractor.
pub fn export_route_zip(route_id: i64, dest_dir: &Path) -> Result<ZipResult, String> {
    let c = crate::db::write_conn()?; // a fresh read/write conn; we only read

    // ---- RouteDefinition (name, country, cross-pak ref) -------------------
    let (name, country_code, xref): (String, String, String) = c
        .query_row(
            "SELECT COALESCE(r.name,''), COALESCE(co.code,''), COALESCE(r.cross_pak_reference_name,'') \
             FROM routes r LEFT JOIN countries co ON co.id = r.country_id WHERE r.id = ?1",
            [route_id],
            |row| Ok((row.get(0)?, row.get(1)?, row.get(2)?)),
        )
        .map_err(|e| format!("route {route_id} not found: {e}"))?;
    if name.is_empty() {
        return Err("route has no name".into());
    }
    let route_def = RouteDefinition {
        display_name: name.clone(),
        country_code,
        cross_pak_reference_name: xref,
        ..Default::default()
    };

    // ---- RVDs for THIS route, keyed by the RVD basename that
    //      `compiled_rv_map` (formation_vehicles.class_name) uses -----------
    let rvds = load_rvds(&c, route_id)?;

    // ---- RouteFeatures (rails + origin) from route_coordinates -----------
    let features = load_features(&c, route_id)?;

    // ---- Timetables: one Timetable (one Service) per DB row --------------
    let (timetables, durations, polylines) = load_timetables(&c, route_id)?;

    crate::zip_writer::write_route_zip_opts(
        dest_dir,
        &route_def,
        &rvds,
        &timetables,
        &features,
        &polylines,
        &durations,
    )
}

/// RVDs used by THIS route's formations, keyed by `formation_vehicles.class_name`
/// (the RVD basename, e.g. "RVD_ALV_ML_RotemCars_Cab") — the same key
/// `compiled_rv_map` produces, so `build_formations_for_service` resolves them.
/// Per-vehicle identity comes from formation_vehicles; the richer RVD spec
/// (speed/power/manufacturer/…) is joined from train_classes by friendly name.
fn load_rvds(c: &Connection, route_id: i64) -> Result<HashMap<String, Rvd>, String> {
    let mut out: HashMap<String, Rvd> = HashMap::new();
    let mut s = c
        .prepare(
            "SELECT DISTINCT fv.class_name, COALESCE(fv.friendly_name,''), \
                    COALESCE(fv.livery_id,''), COALESCE(fv.vehicle_category,''), \
                    COALESCE(fv.length_m,0) \
             FROM formation_vehicles fv \
             WHERE COALESCE(fv.class_name,'') <> '' AND fv.formation_id IN ( \
                 SELECT formation_id FROM timetables WHERE route_id = ?1 AND formation_id IS NOT NULL \
                 UNION \
                 SELECT tf.formation_id FROM timetable_formations tf \
                   JOIN timetables t ON t.id = tf.timetable_id WHERE t.route_id = ?1)",
        )
        .map_err(|e| e.to_string())?;
    let list: Vec<(String, String, String, String, f32)> = s
        .query_map([route_id], |r| {
            Ok((
                r.get::<_, String>(0)?,
                r.get::<_, String>(1)?,
                r.get::<_, String>(2)?,
                r.get::<_, String>(3)?,
                r.get::<_, f64>(4)? as f32,
            ))
        })
        .map_err(|e| e.to_string())?
        .collect::<Result<_, _>>()
        .map_err(|e| e.to_string())?;

    for (class_name, friendly, livery, category, len) in list {
        // Richer spec from train_classes, matched by the friendly name.
        let extra = c
            .query_row(
                "SELECT COALESCE(is_electric,0), COALESCE(max_speed_kph,0), \
                        COALESCE(max_power_kw,0), COALESCE(powered_axle_count,0), \
                        COALESCE(manufacturer_name,''), COALESCE(engine_description,''), \
                        COALESCE(type_description,''), COALESCE(is_drivable,0) \
                 FROM train_classes WHERE name = ?1 LIMIT 1",
                [&friendly],
                |r| Ok((
                    r.get::<_, i64>(0)? != 0, r.get::<_, f64>(1)? as f32, r.get::<_, f64>(2)? as f32,
                    r.get::<_, i64>(3)? as u32, r.get::<_, String>(4)?, r.get::<_, String>(5)?,
                    r.get::<_, String>(6)?, r.get::<_, i64>(7)? != 0,
                )),
            )
            .unwrap_or((false, 0.0, 0.0, 0, String::new(), String::new(), String::new(), false));

        out.insert(class_name.clone(), Rvd {
            rail_vehicle_class:   class_name,
            friendly_name:        friendly,
            livery_id:            livery,
            vehicle_category:     category,
            approximate_length_m: len,
            is_electric:          extra.0,
            max_speed_kph:        extra.1,
            max_power_kw:         extra.2,
            powered_axle_count:   extra.3,
            manufacturer_name:    extra.4,
            engine_description:   extra.5,
            type_description:     extra.6,
            drivable:             extra.7,
            ..Default::default()
        });
    }
    Ok(out)
}

fn load_features(c: &Connection, route_id: i64) -> Result<RouteFeatures, String> {
    let mut f = RouteFeatures::default();
    let blob: Option<String> = c
        .query_row(
            "SELECT coordinates FROM route_coordinates WHERE route_id = ?1",
            [route_id],
            |row| row.get(0),
        )
        .ok();
    if let Some(blob) = blob {
        if let Ok(v) = serde_json::from_str::<serde_json::Value>(&blob) {
            f.rails = parse_rail_segments(&v);
        }
    }
    // origin: first rail point (metadata only — coords in the zip are absolute).
    if let Some(seg) = f.rails.first() {
        if let Some(p) = seg.first() {
            f.origin_lng = p[0];
            f.origin_lat = p[1];
        }
    }
    Ok(f)
}

/// Parse a route_coordinates blob into rail segments (Vec<Vec<[lng,lat]>>).
/// Handles the native nested `[[[lng,lat],…],…]` and the merged-hud-go flat
/// `[{latitude,longitude},…]` (wrapped as one segment) shapes.
fn parse_rail_segments(v: &serde_json::Value) -> Vec<Vec<[f64; 2]>> {
    let pt = |x: &serde_json::Value| -> Option<[f64; 2]> {
        if let Some(arr) = x.as_array() {
            if arr.len() >= 2 {
                return Some([arr[0].as_f64()?, arr[1].as_f64()?]);
            }
        } else if let Some(o) = x.as_object() {
            let lat = o.get("latitude").and_then(|n| n.as_f64());
            let lng = o.get("longitude").and_then(|n| n.as_f64());
            if let (Some(lat), Some(lng)) = (lat, lng) {
                return Some([lng, lat]);
            }
        }
        None
    };
    let Some(items) = v.as_array() else { return Vec::new() };
    match items.first() {
        // Flat list of point objects → one segment.
        Some(serde_json::Value::Object(_)) => {
            vec![items.iter().filter_map(pt).collect()]
        }
        // Flat list of [lng,lat] pairs (inner first elem is a number) → one segment.
        Some(serde_json::Value::Array(inner))
            if matches!(inner.first(), Some(serde_json::Value::Number(_))) =>
        {
            vec![items.iter().filter_map(pt).collect()]
        }
        // Already segmented.
        _ => items
            .iter()
            .filter_map(|seg| seg.as_array().map(|s| s.iter().filter_map(pt).collect()))
            .collect(),
    }
}

#[allow(clippy::type_complexity)]
fn load_timetables(
    c: &Connection,
    route_id: i64,
) -> Result<(Vec<Timetable>, HashMap<String, String>, HashMap<String, Vec<ServiceCoord>>), String> {
    // Action id → name + location id → name lookups.
    let actions = id_name_map(c, "SELECT id, COALESCE(name,'') FROM timetable_actions")?;
    let locations = id_name_map(c, "SELECT id, COALESCE(name,'') FROM locations")?;

    let mut timetables = Vec::new();
    let mut durations: HashMap<String, String> = HashMap::new();
    let mut polylines: HashMap<String, Vec<ServiceCoord>> = HashMap::new();

    let mut s = c
        .prepare(
            "SELECT t.id, COALESCE(t.service_name,''), COALESCE(t.current_service_name,''), \
                    COALESCE(t.service_type,'passenger'), COALESCE(t.source,'Timetable'), \
                    COALESCE(t.playable,0), COALESCE(t.start_time,''), COALESCE(t.duration,''), \
                    COALESCE(t.conductor_compatible,0), COALESCE(t.bound,''), \
                    COALESCE(t.service,''), t.formation_id, \
                    COALESCE((SELECT GROUP_CONCAT(s2.name,'\u{1}') FROM timetable_sections ts \
                              JOIN sections s2 ON s2.id=ts.section_id WHERE ts.timetable_id=t.id),''), \
                    COALESCE(t.scenario_display_name,'') \
             FROM timetables t WHERE t.route_id = ?1 ORDER BY t.id",
        )
        .map_err(|e| e.to_string())?;
    let rows = s
        .query_map([route_id], |r| {
            Ok(TtRow {
                id:        r.get(0)?,
                svc_name:  r.get(1)?,
                cur_name:  r.get(2)?,
                svc_type:  r.get(3)?,
                source:    r.get(4)?,
                playable:  r.get::<_, i64>(5)? != 0,
                start:     r.get(6)?,
                duration:  r.get(7)?,
                conductor: r.get::<_, i64>(8)? != 0,
                bound:     r.get(9)?,
                service:   r.get(10)?,
                formation_id: r.get::<_, Option<i64>>(11)?,
                sections:  r.get(12)?,
                scenario_display_name: r.get(13)?,
            })
        })
        .map_err(|e| e.to_string())?
        .collect::<Result<Vec<_>, _>>()
        .map_err(|e| e.to_string())?;

    for row in rows {
        let mut svc = Service::default();
        svc.name = row.svc_name.clone();
        svc.friendly_name = if row.cur_name.is_empty() { row.svc_name.clone() } else { row.cur_name.clone() };
        svc.service_name = svc.friendly_name.clone();
        svc.service_type = row.svc_type;
        svc.service_class = row.service;
        svc.source = row.source;
        svc.is_player_drivable = row.playable;
        svc.start_time = row.start;
        svc.stop_and_load_count = if row.conductor { 1 } else { 0 };
        svc.schedule = load_schedule(c, row.id, &actions, &locations)?;

        // Formation for this service (+ compiled_rv_map for its vehicles).
        let mut formations = Vec::new();
        let mut compiled_rv_map: HashMap<String, String> = HashMap::new();
        if let Some(fid) = row.formation_id {
            if let Some((fname, vehicles, rv_map)) = load_formation(c, fid)? {
                svc.formation = fname.clone();
                compiled_rv_map = rv_map;
                formations.push(Formation { name: fname, vehicles, ..Default::default() });
            }
        }

        if !row.duration.is_empty() {
            durations.insert(svc.name.clone(), row.duration);
        }
        if let Some(poly) = load_service_polyline(c, row.id)? {
            polylines.insert(svc.name.clone(), poly);
        }
        // bound → service map points so compute_bound can echo it (best effort).
        let _ = row.bound;

        let section_name = row.sections.split('\u{1}').next().unwrap_or("").to_string();
        timetables.push(Timetable {
            route: String::new(),
            section_name,
            scenario_display_name: row.scenario_display_name,
            services: vec![svc],
            formations,
            compiled_rv_map,
            ..Default::default()
        });
    }
    Ok((timetables, durations, polylines))
}

struct TtRow {
    id: i64, svc_name: String, cur_name: String, svc_type: String, source: String,
    playable: bool, start: String, duration: String, conductor: bool, bound: String,
    service: String, formation_id: Option<i64>, sections: String,
    scenario_display_name: String,
}

fn load_schedule(
    c: &Connection,
    tt_id: i64,
    actions: &HashMap<i64, String>,
    locations: &HashMap<i64, String>,
) -> Result<Vec<ScheduleItem>, String> {
    let mut s = c
        .prepare(
            "SELECT action_id, COALESCE(details,''), location_id, COALESCE(structure,''), \
                    COALESCE(structure_number,''), COALESCE(time1,''), COALESCE(time2,''), \
                    COALESCE(sort_order,0), COALESCE(cargo,''), COALESCE(waiting_time,''), \
                    latitude, longitude \
             FROM timetable_entries WHERE timetable_id = ?1 ORDER BY sort_order",
        )
        .map_err(|e| e.to_string())?;
    let rows = s
        .query_map([tt_id], |r| {
            let action_id: Option<i64> = r.get(0)?;
            let loc_id: Option<i64> = r.get(2)?;
            let lat_s: Option<String> = r.get(10)?;
            let lng_s: Option<String> = r.get(11)?;
            Ok(ScheduleItem {
                action: action_id.and_then(|i| actions.get(&i).cloned()).unwrap_or_default(),
                details: r.get(1)?,
                location: loc_id.and_then(|i| locations.get(&i).cloned()).unwrap_or_default(),
                structure: r.get(3)?,
                structure_number: r.get(4)?,
                time1: r.get(5)?,
                time2: r.get(6)?,
                sort_order: r.get::<_, i64>(7)? as i32,
                cargo: r.get(8)?,
                waiting_time: r.get(9)?,
                lat: lat_s.and_then(|s| s.parse().ok()).unwrap_or(0.0),
                lng: lng_s.and_then(|s| s.parse().ok()).unwrap_or(0.0),
                ..Default::default()
            })
        })
        .map_err(|e| e.to_string())?;
    let mut out = Vec::new();
    for r in rows { out.push(r.map_err(|e| e.to_string())?); }
    Ok(out)
}

/// Returns (formation_name, vehicles, compiled_rv_map[vehicle_id → class_name]).
fn load_formation(
    c: &Connection,
    fid: i64,
) -> Result<Option<(String, Vec<FormationVehicle>, HashMap<String, String>)>, String> {
    let fname: String = match c.query_row(
        "SELECT COALESCE(name,'') FROM formations WHERE id = ?1",
        [fid],
        |r| r.get(0),
    ) {
        Ok(n) => n,
        Err(_) => return Ok(None),
    };
    let mut s = c
        .prepare(
            "SELECT COALESCE(vehicle_id,''), COALESCE(class_name,''), COALESCE(length_m,0), \
                    COALESCE(is_flipped,0) \
             FROM formation_vehicles WHERE formation_id = ?1 ORDER BY position",
        )
        .map_err(|e| e.to_string())?;
    let rows = s
        .query_map([fid], |r| {
            Ok((
                r.get::<_, String>(0)?,
                r.get::<_, String>(1)?,
                r.get::<_, f64>(2)? as f32,
                r.get::<_, i64>(3)? != 0,
            ))
        })
        .map_err(|e| e.to_string())?;
    let mut vehicles = Vec::new();
    let mut rv_map = HashMap::new();
    for r in rows {
        let (vid, class_name, len, flipped) = r.map_err(|e| e.to_string())?;
        if !vid.is_empty() && !class_name.is_empty() {
            rv_map.insert(vid.clone(), class_name);
        }
        vehicles.push(FormationVehicle {
            rail_vehicle_id: vid,
            max_length_m: len,
            flipped,
            ..Default::default()
        });
    }
    Ok(Some((fname, vehicles, rv_map)))
}

fn load_service_polyline(c: &Connection, tt_id: i64) -> Result<Option<Vec<ServiceCoord>>, String> {
    let blob: Option<String> = c
        .query_row(
            "SELECT coordinates FROM timetable_coordinates WHERE timetable_id = ?1 LIMIT 1",
            [tt_id],
            |r| r.get(0),
        )
        .ok();
    let Some(blob) = blob else { return Ok(None) };
    let Ok(v) = serde_json::from_str::<serde_json::Value>(&blob) else { return Ok(None) };
    let segs = parse_rail_segments(&v);
    let mut out = Vec::new();
    for seg in segs {
        for p in seg {
            out.push(ServiceCoord { longitude: p[0], latitude: p[1], height: 0.0 });
        }
    }
    if out.is_empty() { Ok(None) } else { Ok(Some(out)) }
}

fn id_name_map(c: &Connection, sql: &str) -> Result<HashMap<i64, String>, String> {
    let mut s = c.prepare(sql).map_err(|e| e.to_string())?;
    let rows = s
        .query_map([], |r| Ok((r.get::<_, i64>(0)?, r.get::<_, String>(1)?)))
        .map_err(|e| e.to_string())?;
    let mut m = HashMap::new();
    for r in rows {
        let (id, name) = r.map_err(|e| e.to_string())?;
        m.insert(id, name);
    }
    Ok(m)
}
