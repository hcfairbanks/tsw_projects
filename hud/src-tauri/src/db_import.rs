//! On-demand zip → DB importer. The inverse of [`crate::db_export`] /
//! [`crate::zip_writer`]: it reads a route-export zip and (re)populates the
//! catalog, mirroring hud-go's `TimetableHandler.ImportRouteZip` +
//! `autoImportZip` cascade.
//!
//! Flow (matches hud-go):
//!   1. Pull `images/train_classes/*.png` out to `resources/images/...`.
//!   2. Ingest the route file's `train_classes[]` into `train_classes`.
//!   3. Pass 1 — the `route_*.json` FeatureCollection: resolve/update the
//!      route + country, replace `route_coordinates` (the whole features
//!      blob) and the point-feature tables (car_stop_signs / track_markers /
//!      route_markers).
//!   4. Wipe the resolved route's existing timetables (so a re-import is a
//!      clean replace, not an accreting merge — autoImportZip semantics).
//!   5. Pass 2 — each per-service JSON: a `timetables` row + schedule
//!      entries + formation(s) + sections + coordinates.
//!
//! Connections here don't enable FK enforcement (see db.rs), so child rows
//! are deleted explicitly. Everything runs inside one transaction.

use std::collections::{HashMap, HashSet};
use std::io::Read;

use rusqlite::{params, Connection};

use crate::extractor_db_writer as dbw;
use crate::output_format::{ConsistVehicle, FormationClassEntry, PackageService};

/// Summary surfaced back to the UI after an import.
#[derive(Debug, Default, serde::Serialize)]
pub struct ImportResult {
    pub route_name:          String,
    pub route_created:       bool,
    pub country_name:        String,
    pub timetables_imported: i64,
    pub timetables_skipped:  i64,
    pub formations_created:  i64,
    pub thumbnails_written:  i64,
    pub train_classes_ingested: i64,
    pub errors:              Vec<String>,
}

fn open_rw() -> Result<Connection, String> {
    Connection::open_with_flags(
        crate::db::db_path(),
        rusqlite::OpenFlags::SQLITE_OPEN_READ_WRITE | rusqlite::OpenFlags::SQLITE_OPEN_URI,
    )
    .map_err(|e| format!("open db (rw): {e}"))
}

fn ne(s: &str) -> Option<&str> {
    if s.is_empty() { None } else { Some(s) }
}

/// One in-memory zip entry. We slurp the whole archive up front because
/// `ZipArchive` only yields entries via `&mut self` by index, which is
/// awkward to interleave with the multi-pass import.
struct ZipEntry {
    name:  String,
    bytes: Vec<u8>,
}

fn read_zip_entries(zip_path: &str) -> Result<Vec<ZipEntry>, String> {
    let bytes = std::fs::read(zip_path).map_err(|e| format!("read zip {zip_path}: {e}"))?;
    let mut archive = zip::ZipArchive::new(std::io::Cursor::new(bytes))
        .map_err(|e| format!("open zip: {e}"))?;
    let mut out = Vec::with_capacity(archive.len());
    for i in 0..archive.len() {
        let mut f = archive.by_index(i).map_err(|e| format!("zip entry {i}: {e}"))?;
        if f.is_dir() { continue; }
        let name = f.name().to_string();
        let mut buf = Vec::with_capacity(f.size() as usize);
        f.read_to_end(&mut buf).map_err(|e| format!("read {name}: {e}"))?;
        out.push(ZipEntry { name, bytes: buf });
    }
    Ok(out)
}

fn basename(path: &str) -> &str {
    path.rsplit(['/', '\\']).next().unwrap_or(path)
}

/// Public entry point. Imports `zip_path` into the catalog and returns a
/// summary. Best-effort: per-file errors are collected into the result
/// rather than aborting the whole import, matching hud-go.
pub fn import_route_zip(zip_path: &str) -> Result<ImportResult, String> {
    let entries = read_zip_entries(zip_path)?;
    let mut result = ImportResult::default();

    // 1) Thumbnails → resources/images/train_classes/. Independent of the DB.
    result.thumbnails_written = extract_thumbnails(&entries);

    let mut conn = open_rw()?;
    let tx = conn.transaction().map_err(|e| e.to_string())?;

    // 2) Train classes from the route file's train_classes[].
    result.train_classes_ingested = ingest_train_classes(&tx, &entries, &mut result.errors);

    // 3) Pass 1 — the route file.
    let mut country_id: Option<i64> = None;
    let primary_route_id = pass1_route_file(&tx, &entries, &mut result, &mut country_id)?;

    // 4) Clean replace: wipe the primary route's existing timetables so a
    //    re-import doesn't leave stale services behind.
    if let Some(rid) = primary_route_id {
        delete_route_timetables(&tx, rid)?;
    }

    // 5) Pass 2 — per-service files.
    let mut formations_created: HashSet<String> = HashSet::new();
    let mut route_cache: HashMap<String, Option<i64>> = HashMap::new();
    for e in &entries {
        let base = basename(&e.name);
        let lower = base.to_ascii_lowercase();
        if !lower.ends_with(".json") { continue; }
        if base.starts_with("route_") || base.starts_with("train_dlc_") { continue; }
        if lower.ends_with("_ribbons.json") { continue; }

        let ps: PackageService = match serde_json::from_slice(&e.bytes) {
            Ok(p) => p,
            Err(err) => { result.errors.push(format!("{}: invalid JSON: {err}", e.name)); continue; }
        };
        if ps.service_name.is_empty() {
            result.errors.push(format!("{}: missing serviceName", e.name));
            continue;
        }

        // Resolve this entry's parent route. Cargo/scenario DLC zips ship
        // services across several parent routes via cross_pak_reference_name;
        // fall back to the zip-level primary route otherwise.
        let entry_route_id = if !ps.cross_pak_reference_name.is_empty() {
            resolve_route_by_cross_pak_ref(&tx, &ps.cross_pak_reference_name, country_id, &mut route_cache)
                .or(primary_route_id)
        } else {
            primary_route_id
        };
        let Some(route_id) = entry_route_id else {
            result.errors.push(format!("{}: no route resolved", e.name));
            continue;
        };

        match import_one_service(&tx, route_id, &ps, &mut formations_created, &mut result.errors) {
            Ok(()) => result.timetables_imported += 1,
            Err(err) => result.errors.push(format!("{}: {err}", e.name)),
        }
    }
    result.formations_created = formations_created.len() as i64;

    tx.commit().map_err(|e| e.to_string())?;
    Ok(result)
}

// ----------------------------------------------------------------- thumbnails

fn extract_thumbnails(entries: &[ZipEntry]) -> i64 {
    let out_dir = exe_relative("resources/images/train_classes");
    let mut n = 0i64;
    for e in entries {
        if !e.name.starts_with("images/train_classes/") { continue; }
        let base = basename(&e.name);
        if !base.to_ascii_lowercase().ends_with(".png") { continue; }
        if std::fs::create_dir_all(&out_dir).is_err() { return n; }
        if std::fs::write(out_dir.join(base), &e.bytes).is_ok() { n += 1; }
    }
    n
}

fn exe_relative(rel: &str) -> std::path::PathBuf {
    std::env::current_exe()
        .ok()
        .and_then(|p| p.parent().map(|p| p.to_path_buf()))
        .map(|p| p.join(rel))
        .unwrap_or_else(|| std::path::PathBuf::from(rel))
}

// ------------------------------------------------------------- train classes

/// Top-level `train_classes[]` entry in a `route_*.json` / `train_dlc_*.json`.
#[derive(serde::Deserialize, Default)]
struct RouteFileClasses {
    #[serde(default)]
    train_classes: Vec<crate::output_format::RouteTrainClass>,
}

fn ingest_train_classes(tx: &Connection, entries: &[ZipEntry], errs: &mut Vec<String>) -> i64 {
    let mut n = 0i64;
    for e in entries {
        let base = basename(&e.name);
        let lower = base.to_ascii_lowercase();
        let is_route   = base.starts_with("route_")     && lower.ends_with(".json");
        let is_traindlc = base.starts_with("train_dlc_") && lower.ends_with(".json");
        if !is_route && !is_traindlc { continue; }
        let rf: RouteFileClasses = match serde_json::from_slice(&e.bytes) {
            Ok(v) => v,
            Err(_) => continue, // a FeatureCollection with no train_classes — fine
        };
        for c in &rf.train_classes {
            if c.rail_vehicle_class.is_empty() { continue; }
            // Reuse the tested upsert: identity is `name` (friendly) OR the
            // `rail_vehicle_class` UNIQUE column, so two RVDs that share a
            // friendly name collapse onto one row instead of tripping the
            // train_classes.name UNIQUE constraint. It also rewrites
            // train_class_electrification wholesale.
            let rvd = crate::uasset_rvd::Rvd {
                rail_vehicle_class:   c.rail_vehicle_class.clone(),
                friendly_name:        c.friendly_name.clone(),
                livery_id:            c.livery_id.clone(),
                vehicle_category:     c.vehicle_category.clone(),
                drivable:             c.drivable,
                approximate_length_m: c.length_m.unwrap_or(0.0),
                is_electric:          c.is_electric.unwrap_or(false),
                max_speed_kph:        c.max_speed_kph.unwrap_or(0.0),
                max_power_kw:         c.max_power_kw.unwrap_or(0.0),
                powered_axle_count:   c.powered_axle_count.unwrap_or(0),
                manufacturer_name:    c.manufacturer_name.clone(),
                engine_description:   c.engine_description.clone(),
                type_description:     c.type_description.clone(),
                electrification:      c.electrification.iter().map(|s| crate::uasset_rvd::ElectrificationSpec {
                    current:      s.current.clone(),
                    pickup_side:  s.pickup_side.clone(),
                    voltage_v:    s.voltage_v,
                    frequency_hz: s.frequency_hz,
                }).collect(),
                ..Default::default()
            };
            match dbw::upsert_train_class(tx, &rvd) {
                Ok(_) => {
                    n += 1;
                    if !c.thumbnail_rel.is_empty() {
                        let name = if !c.friendly_name.is_empty() { &c.friendly_name } else { &c.rail_vehicle_class };
                        let _ = dbw::set_train_class_thumbnail(tx, name, &format!("/{}", c.thumbnail_rel));
                    }
                }
                Err(err) => errs.push(format!("train_class {}: {err}", c.rail_vehicle_class)),
            }
        }
    }
    n
}

// --------------------------------------------------------------- pass 1: route

/// The subset of `route_*.json` we read (it's also a GeoJSON
/// FeatureCollection; `features` stays as raw JSON we re-serialise into
/// `route_coordinates`).
#[derive(serde::Deserialize, Default)]
struct RouteFile {
    #[serde(default)] name: String,
    #[serde(default)] route: String,
    #[serde(default)] country: String,
    #[serde(default)] country_code: String,
    #[serde(default)] cross_pak_reference_name: String,
    #[serde(default)] best_data: bool,
    #[serde(default)] features: Vec<serde_json::Value>,
}

/// Resolve/update the route + country and replace its geometry/markers.
/// Returns the route id (None when the zip has no route file — a
/// per-service-only DLC zip, handled by cross-pak resolution in pass 2).
fn pass1_route_file(
    tx: &Connection,
    entries: &[ZipEntry],
    result: &mut ImportResult,
    country_id: &mut Option<i64>,
) -> Result<Option<i64>, String> {
    let Some(e) = entries.iter().find(|e| {
        let b = basename(&e.name);
        b.starts_with("route_") && b.to_ascii_lowercase().ends_with(".json")
    }) else {
        return Ok(None);
    };
    let rf: RouteFile = serde_json::from_slice(&e.bytes)
        .map_err(|err| format!("{}: invalid route JSON: {err}", e.name))?;

    // Country: prefer ISO code match, then name, else create.
    let cn = rf.country.trim();
    let cc = rf.country_code.trim();
    if !cn.is_empty() || !cc.is_empty() {
        result.country_name = cn.to_string();
        let mut cid: Option<i64> = None;
        if !cc.is_empty() {
            cid = tx.query_row("SELECT id FROM countries WHERE code = ?1", [cc], |r| r.get(0)).ok();
        }
        if cid.is_none() && !cn.is_empty() {
            if let Ok(id) = tx.query_row("SELECT id FROM countries WHERE name = ?1", [cn], |r| r.get::<_, i64>(0)) {
                cid = Some(id);
                if !cc.is_empty() {
                    let _ = tx.execute("UPDATE countries SET code = ?1 WHERE id = ?2 AND (code IS NULL OR code = '')", params![cc, id]);
                }
            }
        }
        if cid.is_none() {
            tx.execute("INSERT INTO countries (name, code) VALUES (?1, ?2)", params![cn, ne(cc)])
                .map_err(|err| format!("create country: {err}"))?;
            cid = Some(tx.last_insert_rowid());
        }
        *country_id = cid;
    }

    // Route: name preferred over codename. Match by cross-pak ref, then name.
    let rn = if !rf.name.is_empty() { rf.name.clone() } else { rf.route.clone() };
    if rn.is_empty() {
        return Err(format!("{}: route file missing name/route", e.name));
    }
    result.route_name = rn.clone();
    let cross = rf.cross_pak_reference_name.trim();
    let mut route_id: Option<i64> = None;
    if !cross.is_empty() {
        route_id = tx.query_row("SELECT id FROM routes WHERE cross_pak_reference_name = ?1", [cross], |r| r.get(0)).ok();
    }
    if route_id.is_none() {
        route_id = tx.query_row("SELECT id FROM routes WHERE name = ?1", [&rn], |r| r.get(0)).ok();
    }
    let route_id = if let Some(id) = route_id {
        id
    } else {
        let Some(cid) = *country_id else {
            return Err(format!("{}: cannot create route {rn}: no country resolved", e.name));
        };
        tx.execute(
            "INSERT INTO routes (name, country_id, tsw_version, cross_pak_reference_name) VALUES (?1, ?2, 6, ?3)",
            params![rn, cid, ne(cross)],
        ).map_err(|err| format!("create route: {err}"))?;
        result.route_created = true;
        tx.last_insert_rowid()
    };

    // Backfill cross-pak ref + best_data.
    if !cross.is_empty() {
        let _ = tx.execute(
            "UPDATE routes SET cross_pak_reference_name = ?1 WHERE id = ?2 AND (cross_pak_reference_name IS NULL OR cross_pak_reference_name = '')",
            params![cross, route_id],
        );
    }
    let _ = tx.execute("UPDATE routes SET best_data = ?1 WHERE id = ?2", params![rf.best_data as i64, route_id]);

    // Geometry: store the whole features array as the route_coordinates blob.
    if !rf.features.is_empty() {
        let blob = serde_json::to_string(&rf.features).map_err(|e| e.to_string())?;
        dbw::write_route_coordinates(tx, route_id, &blob)?;
    }

    // Point features → marker tables.
    import_point_features(tx, route_id, &rf.features);

    Ok(Some(route_id))
}

/// Walk a FeatureCollection's Point features into car_stop_signs /
/// track_markers / route_markers. Mirrors the importer's per-feature switch
/// in hud-go.
///
/// IMPORTANT: each marker table is replaced (delete-then-insert) ONLY when
/// the zip actually carries features of that kind. A rails-only export (the
/// Rust DB exporter doesn't yet emit point layers) must NOT wipe markers the
/// extractor already populated — otherwise a round-trip silently zeroes
/// car_stop_signs / track_markers. Full hud-go zips carry the points, so
/// they still get the wholesale replace.
fn import_point_features(tx: &Connection, route_id: i64, features: &[serde_json::Value]) {
    let s = |p: &serde_json::Value, k: &str| -> String {
        p.get(k).and_then(|v| v.as_str()).unwrap_or("").to_string()
    };
    let f = |p: &serde_json::Value, k: &str| -> Option<f64> {
        p.get(k).and_then(|v| v.as_f64())
    };

    // Bucket the Point features by destination table first, so we only touch
    // a table when the zip has data for it.
    struct CarStop { ribbon_guid: String, location: f64, max_rail: i64, lat: f64, lng: f64 }
    struct Track { name: String, marker_type: String, ribbon_guid: String, location: Option<f64>, start: Option<f64>, end: Option<f64>, line_side: String, lat: f64, lng: f64 }
    struct Marker { station: String, mtype: String, lat: f64, lng: f64 }
    let mut car_stops: Vec<CarStop> = Vec::new();
    let mut tracks: Vec<Track> = Vec::new();
    let mut markers: Vec<Marker> = Vec::new();

    for feat in features {
        let geom = match feat.get("geometry") { Some(g) => g, None => continue };
        if geom.get("type").and_then(|v| v.as_str()) != Some("Point") { continue; }
        let coords = match geom.get("coordinates").and_then(|v| v.as_array()) { Some(c) if c.len() >= 2 => c, _ => continue };
        let lng = coords[0].as_f64().unwrap_or(0.0);
        let lat = coords[1].as_f64().unwrap_or(0.0);
        let props = feat.get("properties").cloned().unwrap_or(serde_json::Value::Null);
        if props.is_null() { continue; }

        match s(&props, "feature_kind").as_str() {
            "car_stop_sign" => car_stops.push(CarStop {
                ribbon_guid: s(&props, "ribbon_guid"),
                location: f(&props, "location").unwrap_or(0.0),
                max_rail: f(&props, "max_rail_vehicles").unwrap_or(0.0) as i64,
                lat, lng,
            }),
            // hud-go emits "track_marker"; the Rust exporter emits the same
            // payload under "route_marker". Treat both as track markers.
            "track_marker" | "route_marker" => {
                let name = { let n = s(&props, "name"); if n.is_empty() { s(&props, "location") } else { n } };
                if name.is_empty() { continue; }
                tracks.push(Track {
                    name, marker_type: s(&props, "marker_type"), ribbon_guid: s(&props, "ribbon_guid"),
                    location: f(&props, "location"), start: f(&props, "start"), end: f(&props, "end"),
                    line_side: s(&props, "line_side"), lat, lng,
                });
            }
            _ => {
                // Legacy: platforms / signals / switches → route_markers.
                if let Some((station, mtype)) = classify_marker(&props) {
                    markers.push(Marker { station, mtype, lat, lng });
                }
            }
        }
    }

    if !car_stops.is_empty() {
        let _ = tx.execute("DELETE FROM car_stop_signs WHERE route_id = ?1", [route_id]);
        for c in &car_stops {
            let _ = tx.execute(
                "INSERT INTO car_stop_signs (route_id, ribbon_guid, location, max_rail_vehicles, latitude, longitude) \
                 VALUES (?1,?2,?3,?4,?5,?6)",
                params![route_id, c.ribbon_guid, c.location, c.max_rail, c.lat, c.lng],
            );
        }
    }
    if !tracks.is_empty() {
        let _ = tx.execute("DELETE FROM track_markers WHERE route_id = ?1", [route_id]);
        for t in &tracks {
            let _ = tx.execute(
                "INSERT INTO track_markers (route_id, name, marker_type, ribbon_guid, location, start, end, line_side, latitude, longitude) \
                 VALUES (?1,?2,?3,?4,?5,?6,?7,?8,?9,?10)",
                params![route_id, t.name, ne(&t.marker_type), t.ribbon_guid,
                    t.location, t.start, t.end, ne(&t.line_side), t.lat, t.lng],
            );
        }
    }
    if !markers.is_empty() {
        let _ = tx.execute("DELETE FROM route_markers WHERE route_id = ?1", [route_id]);
        for m in &markers {
            let _ = tx.execute(
                "INSERT OR REPLACE INTO route_markers (route_id, station_name, marker_type, latitude, longitude) \
                 VALUES (?1,?2,?3,?4,?5)",
                params![route_id, m.station, m.mtype, m.lat, m.lng],
            );
        }
    }

    // Denormalise platform_name onto each car_stop_sign via its ribbon's
    // Platform track_marker (powers the runtime PK lookup). Only when we
    // actually (re)wrote car stops this import.
    if !car_stops.is_empty() {
        let _ = tx.execute(
            "UPDATE car_stop_signs SET platform_name = ( \
                 SELECT name FROM track_markers \
                 WHERE track_markers.route_id = car_stop_signs.route_id \
                   AND track_markers.ribbon_guid = car_stop_signs.ribbon_guid \
                   AND track_markers.marker_type = 'Platform' LIMIT 1) \
             WHERE route_id = ?1",
            [route_id],
        );
    }
}

/// Map a Point feature's properties → (station_name, marker_type) for
/// route_markers. Returns None for shapes the importer skips.
fn classify_marker(props: &serde_json::Value) -> Option<(String, String)> {
    let get = |k: &str| props.get(k).and_then(|v| v.as_str()).unwrap_or("");
    let name = get("name");
    if !name.is_empty() {
        let struc = get("structure");
        let num = get("structure_number");
        let mtype = if struc.is_empty() {
            "Platform".to_string()
        } else if num.is_empty() {
            struc.to_string()
        } else {
            format!("{struc} {num}")
        };
        return Some((name.to_string(), mtype));
    }
    let label = get("display_label");
    if !label.is_empty() {
        if props.get("signal_id").is_some() { return Some((label.to_string(), "Signal".into())); }
        if props.get("jct_guid").is_some()  { return Some((label.to_string(), "Switch".into())); }
    }
    None
}

// ----------------------------------------------------------- pass 2: services

fn import_one_service(
    tx: &Connection,
    route_id: i64,
    ps: &PackageService,
    formations_created: &mut HashSet<String>,
    errs: &mut Vec<String>,
) -> Result<(), String> {
    // Formation for this service.
    let formation_name = ps.formation_name.trim();
    let mut formation_id: Option<i64> = None;
    if !formation_name.is_empty() {
        match resolve_formation(tx, formation_name, &ps.formations) {
            Ok((fid, created)) => {
                formation_id = Some(fid);
                if created { formations_created.insert(formation_name.to_string()); }
            }
            Err(err) => errs.push(format!("resolve formation {formation_name}: {err}")),
        }
    }

    let service_type = if ps.service_type.is_empty() { "passenger" } else { &ps.service_type };
    let bound = ps.bound.as_str().unwrap_or("");
    let start_hm = ps.start_time.clone();

    // Sections from sectionNames[].
    let mut section_ids: Vec<i64> = Vec::new();
    for sn in &ps.section_names {
        if sn.trim().is_empty() { continue; }
        if let Ok(Some(sid)) = dbw::get_or_create_section(tx, route_id, sn) {
            section_ids.push(sid);
        }
    }

    let tid = dbw::upsert_timetable(tx, &dbw::TimetableUpsert {
        route_id,
        formation_id,
        section_id: section_ids.first().copied(),
        service_name:            &ps.service_name,
        current_service_name:    &ps.current_service_name,
        scenario_display_name:   "",
        service_type,
        source:                  &ps.source,
        start_time:              &start_hm,
        duration:                &ps.duration,
        conductor_compatible:    ps.conductor_compatible,
        playable:                ps.playable,
        bound,
        service:                 "",
        contributor:             "",
        coordinates_contributor: "",
    })?;

    // Links.
    if let Some(fid) = formation_id {
        let _ = dbw::link_timetable_formation(tx, tid, fid);
        let _ = dbw::link_route_formation(tx, route_id, fid);
    }
    for &sid in &section_ids {
        let _ = dbw::link_timetable_section(tx, tid, sid);
    }

    // additional_formations[].
    for extra in &ps.additional_formations {
        let efn = extra.formation_name.trim();
        if efn.is_empty() { continue; }
        match resolve_formation(tx, efn, &extra.formations) {
            Ok((eid, created)) => {
                if created { formations_created.insert(efn.to_string()); }
                let _ = dbw::link_timetable_formation(tx, tid, eid);
                let _ = dbw::link_route_formation(tx, route_id, eid);
            }
            Err(err) => errs.push(format!("resolve additional formation {efn}: {err}")),
        }
    }

    // Coordinates → timetable_coordinates as a [{latitude,longitude}] blob.
    if !ps.coordinates.is_empty() {
        let blob = serde_json::to_string(&ps.coordinates).map_err(|e| e.to_string())?;
        let src = if ps.coordinates_source.is_empty() { "backend" } else { &ps.coordinates_source };
        dbw::write_timetable_coordinates(tx, tid, &blob, src)?;
    }

    // Schedule rows from csvData[].
    let mut entries: Vec<dbw::EntryRow> = Vec::with_capacity(ps.csv_data.len());
    // Pre-resolve location ids; keep owned strings alive for the borrows.
    let mut details_buf: Vec<String> = Vec::with_capacity(ps.csv_data.len());
    let mut loc_ids: Vec<Option<i64>> = Vec::with_capacity(ps.csv_data.len());
    let mut actions: Vec<Option<i64>> = Vec::with_capacity(ps.csv_data.len());
    for row in &ps.csv_data {
        let action = row.action.trim().to_ascii_uppercase();
        let is_unload = action == "UNLOAD PASSENGERS";
        let details = if is_unload { String::new() } else { row.details.clone() };
        let loc = row.location.trim();
        let loc_id = if !is_unload && !loc.is_empty() {
            dbw::upsert_location(tx, route_id, loc).ok()
        } else { None };
        actions.push(dbw::action_id_for(tx, &action));
        details_buf.push(details);
        loc_ids.push(loc_id);
    }
    for (i, row) in ps.csv_data.iter().enumerate() {
        let is_unload = row.action.trim().eq_ignore_ascii_case("UNLOAD PASSENGERS");
        entries.push(dbw::EntryRow {
            action_id:        actions[i],
            details:          &details_buf[i],
            location_id:      loc_ids[i],
            structure_number: &row.structure_number,
            structure:        &row.structure,
            time1:            &row.time1,
            time2:            &row.time2,
            latitude:         if is_unload { "" } else { &row.latitude },
            longitude:        if is_unload { "" } else { &row.longitude },
            api_name:         "",
            sort_order:       i as i64,
            coord_source:     row.coord_source.as_deref().unwrap_or(""),
            cargo:            &row.cargo,
            waiting_time:     &row.waiting_time,
        });
    }
    if !entries.is_empty() {
        dbw::rewrite_timetable_entries(tx, tid, &entries)?;
    }

    Ok(())
}

// --------------------------------------------------------- formation resolver

/// Resolve (or create) the `formations` row for a service. Identity is the
/// vehicle-GUID set when consist data exists (so the same physical train on
/// two routes collapses to one row); falls back to name match only when no
/// vehicle data is present. Mirrors hud-go's `resolveFormationFromService`.
/// Returns (formation_id, created).
fn resolve_formation(
    tx: &Connection,
    formation_name: &str,
    formations: &[FormationClassEntry],
) -> Result<(i64, bool), String> {
    let consist = pick_default_consist(formations);
    let vehicles: &[ConsistVehicle] = consist.map(|c| c.vehicles.as_slice()).unwrap_or(&[]);

    if !vehicles.is_empty() {
        let sig = vehicle_guid_set(vehicles);
        if let Some(id) = find_formation_by_vehicle_set(tx, &sig) {
            return Ok((id, false));
        }
    } else if let Ok(id) = tx.query_row(
        "SELECT id FROM formations WHERE name = ?1", [formation_name], |r| r.get::<_, i64>(0),
    ) {
        return Ok((id, false));
    }

    // New formation. Lead vehicle drives the class summary.
    let length_m = consist.map(|c| c.length_m).unwrap_or(0.0);
    let lead = vehicles.iter().find(|v| v.is_lead).or_else(|| vehicles.first());
    let class_name = lead
        .map(|v| if !v.friendly_name.is_empty() { v.friendly_name.clone() } else { v.rail_vehicle_class.clone() })
        .unwrap_or_default();
    let livery = lead.map(|v| v.livery_id.clone()).unwrap_or_default();

    // Find-or-create the class (keyed by name = friendly_name).
    let class_id = if class_name.is_empty() {
        None
    } else {
        Some(upsert_class_by_name(tx, &class_name, lead, vehicles.len() as i64, length_m)?)
    };

    // Always INSERT a fresh formation when the GUID set didn't match — never
    // reuse a different-vehicle formation by name (corruption guard).
    tx.execute(
        "INSERT INTO formations (name, class_name, livery_id, length_m, car_count, class_id) \
         VALUES (?1,?2,?3,?4,?5,?6)",
        params![formation_name, ne(&class_name), ne(&livery), length_m, vehicles.len() as i64, class_id],
    ).map_err(|e| e.to_string())?;
    let fid = tx.last_insert_rowid();

    let rows: Vec<dbw::VehicleRow> = vehicles.iter().enumerate().filter_map(|(i, v)| {
        if v.vehicle_id.is_empty() { return None; }
        Some(dbw::VehicleRow {
            position:         i as i64,
            vehicle_id:       &v.vehicle_id,
            class_name:       &v.rail_vehicle_class,
            friendly_name:    &v.friendly_name,
            livery_id:        &v.livery_id,
            vehicle_category: &v.vehicle_category,
            length_m:         if v.length_m > 0.0 { Some(v.length_m) } else { None },
            is_lead:          v.is_lead,
            is_flipped:       v.is_flipped,
        })
    }).collect();
    if !rows.is_empty() {
        dbw::rewrite_formation_vehicles(tx, fid, &rows)?;
    }
    Ok((fid, true))
}

/// Find-or-create a `train_classes` row by `name` (= friendly name), seeding
/// aggregate specs from the lead vehicle. Returns the class id.
fn upsert_class_by_name(
    tx: &Connection,
    class_name: &str,
    lead: Option<&ConsistVehicle>,
    car_count: i64,
    length_m: f64,
) -> Result<i64, String> {
    let (is_elec, max_spd, max_pwr, manu, eng, typ, cat, livery) = match lead {
        Some(v) => (
            Some(v.is_electric as i64),
            if v.max_speed_kph > 0.0 { Some(v.max_speed_kph as f64) } else { None },
            if v.max_power_kw  > 0.0 { Some(v.max_power_kw  as f64) } else { None },
            v.manufacturer_name.clone(), v.engine_description.clone(),
            v.type_description.clone(), v.vehicle_category.clone(), v.livery_id.clone(),
        ),
        None => (None, None, None, String::new(), String::new(), String::new(), String::new(), String::new()),
    };
    if let Ok(cid) = tx.query_row("SELECT id FROM train_classes WHERE name = ?1", [class_name], |r| r.get::<_, i64>(0)) {
        // Refresh aggregates without clobbering existing non-NULLs.
        let _ = tx.execute(
            "UPDATE train_classes SET \
               is_electric        = COALESCE(?1, is_electric), \
               max_speed_kph      = COALESCE(?2, max_speed_kph), \
               max_power_kw       = COALESCE(?3, max_power_kw), \
               manufacturer_name  = COALESCE(NULLIF(?4,''), manufacturer_name), \
               engine_description = COALESCE(NULLIF(?5,''), engine_description), \
               type_description   = COALESCE(NULLIF(?6,''), type_description), \
               vehicle_category   = COALESCE(NULLIF(?7,''), vehicle_category) \
             WHERE id = ?8",
            params![is_elec, max_spd, max_pwr, manu, eng, typ, cat, cid],
        );
        return Ok(cid);
    }
    tx.execute(
        "INSERT INTO train_classes \
         (name, livery_id, typical_length_m, typical_car_count, is_electric, max_speed_kph, max_power_kw, \
          manufacturer_name, engine_description, type_description, vehicle_category) \
         VALUES (?1,?2,?3,?4,?5,?6,?7,?8,?9,?10,?11)",
        params![class_name, ne(&livery), length_m, car_count, is_elec, max_spd, max_pwr,
            ne(&manu), ne(&eng), ne(&typ), ne(&cat)],
    ).map_err(|e| e.to_string())?;
    Ok(tx.last_insert_rowid())
}

fn pick_default_consist(formations: &[FormationClassEntry]) -> Option<&crate::output_format::Consist> {
    for f in formations {
        if f.is_default {
            if let Some(c) = f.consists.first() {
                if !c.vehicles.is_empty() { return Some(c); }
            }
        }
    }
    for f in formations {
        if let Some(c) = f.consists.first() {
            if !c.vehicles.is_empty() { return Some(c); }
        }
    }
    None
}

fn vehicle_guid_set(vehicles: &[ConsistVehicle]) -> String {
    let mut guids: Vec<&str> = vehicles.iter()
        .map(|v| v.vehicle_id.as_str())
        .filter(|g| !g.is_empty())
        .collect();
    guids.sort_unstable();
    guids.join(",")
}

fn find_formation_by_vehicle_set(tx: &Connection, target: &str) -> Option<i64> {
    if target.is_empty() { return None; }
    let mut stmt = tx.prepare(
        "SELECT formation_id, GROUP_CONCAT(vehicle_id, ',') FROM ( \
            SELECT formation_id, vehicle_id FROM formation_vehicles ORDER BY formation_id, vehicle_id \
         ) GROUP BY formation_id",
    ).ok()?;
    let rows = stmt.query_map([], |r| Ok((r.get::<_, i64>(0)?, r.get::<_, String>(1)?))).ok()?;
    for row in rows.flatten() {
        let (fid, sig) = row;
        let mut parts: Vec<&str> = sig.split(',').collect();
        parts.sort_unstable();
        if parts.join(",") == target { return Some(fid); }
    }
    None
}

// ------------------------------------------------------ cross-pak resolution

/// Resolve a parent route by its `cross_pak_reference_name`. Simplified port
/// of hud-go's per-entry resolver: match the canonical column, then name;
/// create with the resolved/zip country when missing. Cached per import.
fn resolve_route_by_cross_pak_ref(
    tx: &Connection,
    reference: &str,
    country_id: Option<i64>,
    cache: &mut HashMap<String, Option<i64>>,
) -> Option<i64> {
    if reference.is_empty() { return None; }
    if let Some(v) = cache.get(reference) { return *v; }
    let resolved = (|| {
        if let Ok(id) = tx.query_row("SELECT id FROM routes WHERE cross_pak_reference_name = ?1", [reference], |r| r.get::<_, i64>(0)) {
            return Some(id);
        }
        if let Ok(id) = tx.query_row("SELECT id FROM routes WHERE name = ?1", [reference], |r| r.get::<_, i64>(0)) {
            let _ = tx.execute("UPDATE routes SET cross_pak_reference_name = ?1 WHERE id = ?2 AND (cross_pak_reference_name IS NULL OR cross_pak_reference_name = '')", params![reference, id]);
            return Some(id);
        }
        // Create only when we have a country to satisfy the NOT NULL FK.
        let cid = country_id?;
        if tx.execute(
            "INSERT INTO routes (name, country_id, tsw_version, cross_pak_reference_name) VALUES (?1, ?2, 6, ?3)",
            params![reference, cid, reference],
        ).is_ok() {
            Some(tx.last_insert_rowid())
        } else {
            None
        }
    })();
    cache.insert(reference.to_string(), resolved);
    resolved
}

// ------------------------------------------------------------- cascade delete

/// Delete a route's timetables and their children (no FK cascade to lean on).
/// Used before pass 2 so a re-import replaces rather than accretes.
fn delete_route_timetables(tx: &Connection, route_id: i64) -> Result<(), String> {
    let tt_ids: Vec<i64> = {
        let mut s = tx.prepare("SELECT id FROM timetables WHERE route_id = ?1").map_err(|e| e.to_string())?;
        let ids = s.query_map([route_id], |r| r.get::<_, i64>(0))
            .map_err(|e| e.to_string())?
            .collect::<Result<Vec<_>, _>>()
            .map_err(|e| e.to_string())?;
        ids
    };
    for tid in tt_ids {
        for tbl in ["timetable_formations", "timetable_sections", "timetable_entries",
                    "timetable_coordinates", "timetable_markers", "timetable_map_features"] {
            let _ = tx.execute(&format!("DELETE FROM {tbl} WHERE timetable_id = ?1"), [tid]);
        }
    }
    tx.execute("DELETE FROM timetables WHERE route_id = ?1", [route_id]).map_err(|e| e.to_string())?;
    Ok(())
}
