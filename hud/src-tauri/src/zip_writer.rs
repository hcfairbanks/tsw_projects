//! Per-route extract zip writer. Mirrors hud-go's
//! `internal/output/package_writer.go`: one `.zip` per route,
//! containing:
//!   * `route_<DisplayName>.json` — FeatureCollection (rails MultiLineString
//!     + per-feature point layers + `train_classes[]` metadata).
//!   * one `<service-stem>.json` per service — the `PackageService`
//!     shape declared in [`crate::output_format`].
//!   * `images/train_classes/<sanitised>.png` — re-read from the
//!     extractor's PNG cache (`<exe>/resources/images/train_classes/`)
//!     and stored verbatim.
//!
//! Result lands in `<extractor_output_dir>/<DisplayName>.zip`. Empty
//! output_dir → defaults to `<exe-adjacent>/extract_zips/`.
//!
//! The importer side (in-process for hud, `/api/routes/import-zip` for
//! hud-go) reads either shape interchangeably, so an extract done in
//! hud can be opened in hud-go and vice versa.

use std::collections::HashMap;
use std::fs;
use std::io::Write;
use std::path::{Path, PathBuf};

use zip::write::{SimpleFileOptions, ZipWriter};
use zip::{CompressionMethod, DateTime as ZipDateTime};

use crate::cookedmap::RouteFeatures;
use crate::output_format::{
    AdditionalFormation, ConsistVehicle, Consist, CsvDataRow, ElectrificationSpec,
    FormationClassEntry, PackageService, RouteTrainClass, ServiceCoord, TimetableRow,
};
use crate::uasset_route_definition::RouteDefinition;
use crate::uasset_rvd::Rvd;
use crate::uasset_timetable::{Service, Timetable};

/// What the zip writer surfaces back to the orchestrator + UI.
#[derive(Debug, Clone, Default, serde::Serialize)]
pub struct ZipResult {
    pub zip_path:           String,
    pub services_written:   u64,
    pub thumbnails_packed:  u64,
    pub bytes:              u64,
}

/// Bundle one route into a zip on disk.
///
/// All inputs come straight off the pipeline's in-memory data — we don't
/// reread the pak. `service_polylines` is the keyed-by-service-name
/// polyline map the path-builder already produced; thumbnails are
/// resolved by looking under `<exe-dir>/resources/images/train_classes/`
/// where [`crate::uasset_texture::extract_texture_to_png`] put them.
pub fn write_route_zip(
    output_dir:        &Path,
    route_def:         &RouteDefinition,
    rvds:              &HashMap<String, Rvd>,
    timetables:        &[Timetable],
    features:          &RouteFeatures,
    service_polylines: &HashMap<String, Vec<ServiceCoord>>,
) -> Result<ZipResult, String> {
    write_route_zip_opts(output_dir, route_def, rvds, timetables, features, service_polylines, &HashMap::new())
}

/// Like [`write_route_zip`] but `duration_by_service` (keyed by the service's
/// backend name, `svc.name`) overrides the schedule-derived duration. The
/// on-demand DB export uses this so the stored (Simulated-time) duration wins
/// over a recompute from the sparse explicit schedule. Empty map = compute.
#[allow(clippy::too_many_arguments)]
pub fn write_route_zip_opts(
    output_dir:        &Path,
    route_def:         &RouteDefinition,
    rvds:              &HashMap<String, Rvd>,
    timetables:        &[Timetable],
    features:          &RouteFeatures,
    service_polylines: &HashMap<String, Vec<ServiceCoord>>,
    duration_by_service: &HashMap<String, String>,
) -> Result<ZipResult, String> {
    let display_name = if !route_def.display_name.is_empty() {
        route_def.display_name.clone()
    } else {
        route_def.stat_tracking_name.clone()
    };
    if display_name.is_empty() {
        return Err("route has no display name — can't pick a zip filename".into());
    }
    let stem = sanitise_filename(&display_name);
    fs::create_dir_all(output_dir)
        .map_err(|e| format!("mkdir {}: {e}", output_dir.display()))?;
    let zip_path = output_dir.join(format!("{stem}.zip"));
    let file = fs::File::create(&zip_path)
        .map_err(|e| format!("create {}: {e}", zip_path.display()))?;
    let mut zw = ZipWriter::new(file);
    let opts = SimpleFileOptions::default()
        .compression_method(CompressionMethod::Deflated)
        .last_modified_time(now_zip_datetime());

    // 1) route_<X>.json — the FeatureCollection.
    let route_json_name = format!("route_{}.json", filename_safe(&display_name));
    let route_doc = build_route_json(&display_name, route_def, rvds, features);
    let bytes = serde_json::to_vec_pretty(&route_doc)
        .map_err(|e| format!("serialise route json: {e}"))?;
    zw.start_file(&route_json_name, opts).map_err(|e| e.to_string())?;
    zw.write_all(&bytes).map_err(|e| e.to_string())?;

    // 1b) <X>_ribbons.json — lightweight per-ribbon graph metadata.
    // Used by importers / runtime graph algorithms that need to map a
    // RibbonGUID to its endpoints + node graph without parsing the
    // full sampled rails layer. hud-go always emits this; the Rust
    // port used to drop it silently.
    let ribbons_json_name = format!("{}_ribbons.json", filename_safe(&display_name));
    let ribbons_doc = build_ribbons_meta_json(features);
    let bytes = serde_json::to_vec_pretty(&ribbons_doc)
        .map_err(|e| format!("serialise ribbons json: {e}"))?;
    zw.start_file(&ribbons_json_name, opts).map_err(|e| e.to_string())?;
    zw.write_all(&bytes).map_err(|e| e.to_string())?;

    // 2) per-service JSONs.
    //
    // Two key fidelity rules from hud-go's `package_writer.go::writeZip`:
    //   * Group services by (composed serviceName, playable) — a service
    //     that appears in N timetable uassets collapses into ONE JSON
    //     (the importer's dedup key). Without this we emit ~1.6× too many
    //     files; ECML jumps from 1422 → 2255.
    //   * Use the descriptive `filenameStem` format
    //     `{csn}__{svc}_{type}_{src}_{playable}` capped at 200 chars, with
    //     `-N` collision suffix. Plain stems like `0E04.json` lose the
    //     importer's namespace and clash with same-codename services in
    //     other sections.
    let variants = group_service_variants(timetables);
    let mut services_written: u64 = 0;
    let mut used_stems: std::collections::HashSet<String> = std::collections::HashSet::new();
    for v in &variants {
        let tt  = &timetables[v.canonical.tt];
        let svc = &tt.services[v.canonical.svc];
        let ps = build_package_service(
            route_def, tt, svc, rvds, service_polylines, features,
            &v.section_names, &v.collect_additional_formations(timetables, rvds),
            duration_by_service.get(&svc.name).map(|s| s.as_str()),
        );
        let stem_full = filename_stem(&ps);
        let stem = unique_name(stem_full, &mut used_stems);
        let body = serde_json::to_vec_pretty(&ps)
            .map_err(|e| format!("serialise service {}: {e}", svc.name))?;
        zw.start_file(format!("{stem}.json"), opts).map_err(|e| e.to_string())?;
        zw.write_all(&body).map_err(|e| e.to_string())?;
        services_written += 1;
    }

    // 3) thumbnails — re-read from the PNG cache.
    let mut thumbnails_packed: u64 = 0;
    let png_dir = exe_relative("resources/images/train_classes");
    let mut seen_pngs: std::collections::HashSet<String> = std::collections::HashSet::new();
    for rvd in rvds.values() {
        let class_name = if !rvd.friendly_name.is_empty() {
            &rvd.friendly_name
        } else {
            &rvd.rail_vehicle_class
        };
        if class_name.is_empty() { continue; }
        let sanitised = crate::uasset_texture::sanitise_thumbnail_name(class_name);
        if sanitised.is_empty() { continue; }
        if !seen_pngs.insert(sanitised.clone()) { continue; }
        let src = png_dir.join(format!("{sanitised}.png"));
        let Ok(png) = fs::read(&src) else { continue };
        let arcname = format!("images/train_classes/{sanitised}.png");
        zw.start_file(&arcname, opts).map_err(|e| e.to_string())?;
        zw.write_all(&png).map_err(|e| e.to_string())?;
        thumbnails_packed += 1;
    }

    zw.finish().map_err(|e| e.to_string())?;
    let meta = fs::metadata(&zip_path).map_err(|e| e.to_string())?;
    Ok(ZipResult {
        zip_path: zip_path.to_string_lossy().into_owned(),
        services_written,
        thumbnails_packed,
        bytes: meta.len(),
    })
}

// =================================================================== route_<X>.json

fn build_route_json(
    display_name: &str,
    route_def:    &RouteDefinition,
    rvds:         &HashMap<String, Rvd>,
    features:     &RouteFeatures,
) -> serde_json::Value {
    let mut all_features: Vec<serde_json::Value> = Vec::new();

    // Rails MultiLineString — one Feature wrapping all the per-ribbon
    // sub-lines (matches cookedmap.buildRailsFeature output).
    let mut rails_props = serde_json::Map::new();
    rails_props.insert("route".into(),         serde_json::Value::String(display_name.to_string()));
    rails_props.insert("ribbon_count".into(),  serde_json::Value::from(features.rails.len() as i64));
    rails_props.insert("geometry_mode".into(), serde_json::Value::String("arc".into()));
    all_features.push(serde_json::json!({
        "type": "Feature",
        "properties": rails_props,
        "geometry": { "type": "MultiLineString", "coordinates": features.rails }
    }));

    // Per-feature track LineStrings (platform_track / siding_track / junction_track).
    for tf in &features.track_feature_lines {
        all_features.push(serde_json::json!({
            "type": "Feature",
            "properties": {
                "feature_type": tf.feature_type,
                "feature_kind": "track_feature",
                "location":     tf.name,
                "marker_type":  tf.marker_type,
                "line_side":    tf.line_side,
                "ribbon_guid":  tf.ribbon_guid,
                "start":        tf.start,
                "end":          tf.end,
            },
            "geometry": { "type": "LineString", "coordinates": tf.coords }
        }));
    }

    // Per-feature point layers.
    for p in &features.platform_points {
        all_features.push(point_feat("platform", &p.name, &p.ribbon_guid, &p.tile_name, p.lat, p.lng, None));
    }
    for s in &features.signal_points {
        let extra = serde_json::json!({ "signal_type": s.signal_type, "signal_id": s.signal_id });
        all_features.push(point_feat("signal", &s.signal_id, &s.ribbon_guid, &s.tile_name, s.lat, s.lng, Some(extra)));
    }
    for s in &features.switch_points {
        let extra = serde_json::json!({
            "jct_guid": s.jct_guid, "node_guid": s.node_guid,
            "ingoing_ribbon": s.ingoing_ribbon,
            "outgoing_ribbon": s.outgoing_ribbon,
            "turnout_ribbon": s.turnout_ribbon,
            "manually_controlled": s.manually_controlled,
        });
        all_features.push(point_feat("switch", &s.jct_guid, "", &s.tile_name, s.lat, s.lng, Some(extra)));
    }
    for m in &features.route_marker_points {
        let extra = serde_json::json!({
            "marker_type": m.marker_type,
            "line_side":   m.line_side,
            "location":    m.location, "start": m.start, "end": m.end,
        });
        all_features.push(point_feat("route_marker", &m.name, &m.ribbon_guid, &m.tile_name, m.lat, m.lng, Some(extra)));
    }
    for c in &features.car_stop_sign_points {
        let extra = serde_json::json!({
            "location": c.location, "max_rail_vehicles": c.max_rail_vehicles,
        });
        all_features.push(point_feat("car_stop_sign", "", &c.ribbon_guid, &c.tile_name, c.lat, c.lng, Some(extra)));
    }
    for col in &features.collectable_points {
        let extra = serde_json::json!({
            "actor_class":    col.actor_class,
            "instance":       col.instance_name,
            "collectable_id": col.collectable_id,
        });
        all_features.push(point_feat("collectable", &col.instance_name, "", &col.tile_name, col.lat, col.lng, Some(extra)));
    }

    let train_classes: Vec<RouteTrainClass> = rvds.values()
        .map(|r| rvd_to_route_train_class(r))
        .collect();

    let mut doc = serde_json::Map::new();
    doc.insert("type".into(),       serde_json::Value::String("FeatureCollection".into()));
    doc.insert("name".into(),       serde_json::Value::String(display_name.to_string()));
    doc.insert("route".into(),      serde_json::Value::String(route_def.stat_tracking_name.clone()));
    doc.insert("origin_lat".into(), serde_json::Value::from(features.origin_lat));
    doc.insert("origin_lng".into(), serde_json::Value::from(features.origin_lng));
    // hud-go writes `country` as the human-readable name alongside the
    // ISO code so importers without a code-lookup table still get a
    // user-facing string. Always emit so this byte stays present.
    doc.insert("country".into(),
        serde_json::Value::String(country_name_from_code(&route_def.country_code)));
    doc.insert("best_data".into(),  serde_json::Value::Bool(true));
    doc.insert("features".into(),   serde_json::Value::Array(all_features));
    if !route_def.cross_pak_reference_name.is_empty() {
        doc.insert("cross_pak_reference_name".into(),
            serde_json::Value::String(route_def.cross_pak_reference_name.clone()));
    }
    if !route_def.country_code.is_empty() {
        doc.insert("country_code".into(),
            serde_json::Value::String(route_def.country_code.clone()));
    }
    if !train_classes.is_empty() {
        doc.insert("train_classes".into(), serde_json::to_value(&train_classes).unwrap_or(serde_json::Value::Null));
    }
    serde_json::Value::Object(doc)
}

/// Build the `<X>_ribbons.json` payload: a JSON object keyed by ribbon
/// GUID. Mirrors hud-go's `WriteRibbonsMeta` shape:
/// `{ start_lat, start_lng, end_lat, end_lng, start_node_guid?,
///    end_node_guid?, length_m }`. Tile name is omitted (the Rust
/// `RibbonMeta` doesn't carry it yet). Endpoints come from the first /
/// last sample of `features.per_ribbon` polyline (already projected
/// through the route anchor), which avoids re-running the analytical
/// arc math hud-go's `ribbonStartEndLatLng` does.
fn build_ribbons_meta_json(features: &RouteFeatures) -> serde_json::Value {
    use serde_json::{Map, Value};
    let mut metas: Vec<&crate::cookedmap::RibbonMeta> = features.ribbon_meta.iter().collect();
    metas.sort_by(|a, b| a.ribbon_guid.cmp(&b.ribbon_guid));
    let mut out: Map<String, Value> = Map::new();
    for m in metas {
        let Some(poly) = features.per_ribbon.get(&m.ribbon_guid) else { continue };
        if poly.is_empty() { continue }
        let s = poly[0];
        let e = poly[poly.len() - 1];
        let mut entry = Map::new();
        entry.insert("start_lat".into(), Value::from(round7(s[1])));
        entry.insert("start_lng".into(), Value::from(round7(s[0])));
        entry.insert("end_lat".into(),   Value::from(round7(e[1])));
        entry.insert("end_lng".into(),   Value::from(round7(e[0])));
        if !m.start_node_guid.is_empty() {
            entry.insert("start_node_guid".into(), Value::from(m.start_node_guid.clone()));
        }
        if !m.end_node_guid.is_empty() {
            entry.insert("end_node_guid".into(),   Value::from(m.end_node_guid.clone()));
        }
        entry.insert("length_m".into(), Value::from(round1(m.length_cm / 100.0)));
        out.insert(m.ribbon_guid.clone(), Value::Object(entry));
    }
    Value::Object(out)
}

fn round7(v: f64) -> f64 { (v * 1e7).round() / 1e7 }
fn round1(v: f64) -> f64 { (v * 10.0).round() / 10.0 }

fn point_feat(feature_type: &str, name: &str, ribbon_guid: &str, tile: &str, lat: f64, lng: f64, extra: Option<serde_json::Value>) -> serde_json::Value {
    let mut props = serde_json::Map::new();
    props.insert("feature_type".into(), serde_json::Value::String(feature_type.into()));
    props.insert("feature_kind".into(), serde_json::Value::String(feature_type.into()));
    if !name.is_empty()        { props.insert("location".into(),    serde_json::Value::String(name.into())); }
    if !ribbon_guid.is_empty() { props.insert("ribbon_guid".into(), serde_json::Value::String(ribbon_guid.into())); }
    if !tile.is_empty()        { props.insert("tile".into(),        serde_json::Value::String(tile.into())); }
    if let Some(serde_json::Value::Object(map)) = extra {
        for (k, v) in map { props.insert(k, v); }
    }
    serde_json::json!({
        "type": "Feature",
        "properties": props,
        "geometry": { "type": "Point", "coordinates": [lng, lat] }
    })
}

fn rvd_to_route_train_class(r: &Rvd) -> RouteTrainClass {
    let mut tc = RouteTrainClass {
        rail_vehicle_class: r.rail_vehicle_class.clone(),
        friendly_name:      r.friendly_name.clone(),
        livery_id:          r.livery_id.clone(),
        vehicle_category:   r.vehicle_category.clone(),
        drivable:           r.drivable,
        manufacturer_name:  r.manufacturer_name.clone(),
        engine_description: r.engine_description.clone(),
        type_description:   r.type_description.clone(),
        ..Default::default()
    };
    if r.approximate_length_m > 0.0 { tc.length_m = Some(r.approximate_length_m); }
    if r.max_speed_kph        > 0.0 { tc.max_speed_kph = Some(r.max_speed_kph); }
    if r.max_power_kw         > 0.0 { tc.max_power_kw  = Some(r.max_power_kw); }
    if r.powered_axle_count  >  0   { tc.powered_axle_count = Some(r.powered_axle_count as u32); }
    tc.is_electric = Some(r.is_electric);
    if !r.electrification.is_empty() {
        tc.electrification = r.electrification.iter().map(|e| ElectrificationSpec {
            current:      e.current.clone(),
            pickup_side:  e.pickup_side.clone(),
            voltage_v:    e.voltage_v,
            frequency_hz: e.frequency_hz,
        }).collect();
    }
    let class_name = if !r.friendly_name.is_empty() { &r.friendly_name } else { &r.rail_vehicle_class };
    if !class_name.is_empty() {
        let sanitised = crate::uasset_texture::sanitise_thumbnail_name(class_name);
        if !sanitised.is_empty() {
            tc.thumbnail_rel = format!("images/train_classes/{sanitised}.png");
        }
    }
    tc
}

// =================================================================== per-service JSON
//
// Mirrors hud-go's `package_writer.go::buildPackageServiceWithCtx`.
// The Rust side has the same inputs (parsed Service + Timetable, RVD
// map, pre-built service polylines, route features carrying the route
// anchor) so the port is structural — populate every field hud-go
// stamps, in the same order, with the same string encodings.

#[allow(clippy::too_many_arguments)]
fn build_package_service(
    route_def:         &RouteDefinition,
    tt:                &Timetable,
    svc:               &Service,
    rvds:              &HashMap<String, Rvd>,
    service_polylines: &HashMap<String, Vec<ServiceCoord>>,
    features:          &RouteFeatures,
    section_names:     &[String],
    additional:        &[AdditionalFormation],
    duration_override: Option<&str>,
) -> PackageService {
    let start_hm = hm_only(&svc.start_time);
    let duration = duration_override
        .map(|s| s.to_string())
        .unwrap_or_else(|| compute_duration(svc));

    let display_name = if !route_def.display_name.is_empty() {
        route_def.display_name.clone()
    } else {
        route_def.stat_tracking_name.clone()
    };

    let mut ps = PackageService::default();
    // identity / taxonomy — Go's filenameStem reads CurrentServiceName as
    // the short backend ID and ServiceName as the composed descriptive
    // form, so the previous Rust port had these inverted.
    ps.service_name          = build_service_name(tt, svc, &start_hm, &duration);
    ps.current_service_name  = svc.name.clone();
    ps.description           = svc.description.clone();
    ps.service_type          = svc.service_type.clone();
    ps.source                = svc.source.clone();
    ps.playable              = svc.is_player_drivable;
    ps.hidden                = svc.is_hidden;
    ps.bound                 = compute_bound(svc);
    // location
    ps.route_name            = display_name;
    ps.country_name          = country_name_from_code(&route_def.country_code);
    ps.section_names         = section_names.to_vec();
    ps.cross_pak_reference_name = route_def.cross_pak_reference_name.clone();
    ps.origin_lat            = features.origin_lat;
    ps.origin_lng            = features.origin_lng;
    // scheduling
    ps.start_time            = start_hm;
    ps.duration              = duration;
    // formation
    ps.formations            = build_formations_for_service(tt, svc, rvds);
    ps.conductor_compatible  = svc.stop_and_load_count > 0;
    let trimmed_formation = svc.formation.trim();
    if !trimmed_formation.is_empty() && trimmed_formation != "None" {
        ps.formation_name = trimmed_formation.to_string();
    }
    if !additional.is_empty() {
        ps.additional_formations = additional.to_vec();
    }
    // recording metadata — hud-go stamps "backend" on both regardless of
    // source. Stick to that so importer-side downstream code that
    // branches on these literal strings stays consistent.
    ps.recording_mode        = "backend".into();
    ps.coordinates_source    = "backend".into();
    // schedule rows: both shapes (raw csvData + collapsed timetable)
    let (csv, ttable) = build_schedule_rows(svc);
    ps.csv_data  = csv;
    ps.timetable = ttable;
    // coordinates — decimate to 5m to match hud-go. Path-builders emit
    // sub-metre samples; renderers can't show that density and the zip
    // bloats ~5×.
    if let Some(coords) = service_polylines.get(&svc.name) {
        ps.coordinates = decimate_coords_meters(coords, 5.0);
    }
    ps.total_points  = ps.coordinates.len() as i32;
    ps.total_markers = ps.markers.len()     as i32;
    ps
}

/// Build the single default `FormationClassEntry` for a service. Mirrors
/// hud-go's `buildFormations(ctx, svc)` enough for the importer's link.
/// Returns an empty vec when the formation lookup fails (rather than
/// the silently-empty array the previous port produced).
fn build_formations_for_service(
    tt:   &Timetable,
    svc:  &Service,
    rvds: &HashMap<String, Rvd>,
) -> Vec<FormationClassEntry> {
    let Some(f) = tt.formations.iter().find(|f| f.name == svc.formation) else {
        return Vec::new();
    };
    let Some(lead) = f.vehicles.first() else { return Vec::new(); };
    let Some(asset_path) = tt.compiled_rv_map.get(&lead.rail_vehicle_id) else { return Vec::new(); };
    let Some(basename) = asset_path_basename(asset_path) else { return Vec::new(); };
    let Some(rvd) = rvds.get(&basename) else { return Vec::new(); };
    let consist = Consist {
        length_m:  if rvd.approximate_length_m > 0.0 {
            rvd.approximate_length_m as f64 * f.vehicles.len() as f64
        } else { 0.0 },
        car_count: f.vehicles.len() as i32,
        vehicles:  f.vehicles.iter().enumerate().map(|(i, v)| ConsistVehicle {
            vehicle_id:         v.rail_vehicle_id.clone(),
            rail_vehicle_class: basename.clone(),
            friendly_name:      rvd.friendly_name.clone(),
            livery_id:          rvd.livery_id.clone(),
            vehicle_category:   rvd.vehicle_category.clone(),
            length_m:           v.max_length_m as f64,
            is_lead:            i == 0,
            is_flipped:         v.flipped,
            is_electric:        rvd.is_electric,
            max_speed_kph:      rvd.max_speed_kph,
            max_power_kw:       rvd.max_power_kw,
            manufacturer_name:  rvd.manufacturer_name.clone(),
            engine_description: rvd.engine_description.clone(),
            type_description:   rvd.type_description.clone(),
            thumbnail_asset_ref: rvd.thumbnail_asset_ref.clone(),
            electrification:    rvd.electrification.iter().map(|e| ElectrificationSpec {
                current:      e.current.clone(),
                pickup_side:  e.pickup_side.clone(),
                voltage_v:    e.voltage_v,
                frequency_hz: e.frequency_hz,
            }).collect(),
        }).collect(),
    };
    vec![FormationClassEntry {
        class:      basename,
        is_default: true,
        consists:   vec![consist],
    }]
}

// ----- schedule rows ------------------------------------------------

/// Build both shapes of schedule output hud-go writes: raw `csvData`
/// (one row per ScheduleItem) and collapsed `timetable` (one row per
/// physical stop, with arrival + departure merged from the next LOAD
/// PASSENGERS). Mirrors `buildScheduleRows` in hud-go's package_writer.
fn build_schedule_rows(svc: &Service) -> (Vec<CsvDataRow>, Vec<TimetableRow>) {
    let sched = &svc.schedule;

    // Raw csvData
    let mut csv = Vec::with_capacity(sched.len());
    for (i, it) in sched.iter().enumerate() {
        let (lat_s, lng_s) = if it.lat != 0.0 || it.lng != 0.0 {
            (fmt_coord(it.lat), fmt_coord(it.lng))
        } else { (String::new(), String::new()) };
        let mut details = it.details.clone();
        if details.is_empty() && it.action == "STOP AT LOCATION" {
            // hud-go reference format: "<Location> <Structure> <StructureNumber> - <Time1>"
            let mut parts: Vec<&str> = Vec::new();
            if !it.location.is_empty()         { parts.push(&it.location); }
            if !it.structure.is_empty()        { parts.push(&it.structure); }
            if !it.structure_number.is_empty() { parts.push(&it.structure_number); }
            let loc_str = parts.join(" ");
            if !loc_str.is_empty() && !it.time1.is_empty() {
                details = format!("{loc_str} - {}", it.time1);
            }
        }
        csv.push(CsvDataRow {
            action:           it.action.clone(),
            cargo:            it.cargo.clone(),
            waiting_time:     it.waiting_time.clone(),
            coord_source:     Some("backend".into()),
            details,
            index:            i as i32,
            latitude:         lat_s,
            location:         it.location.clone(),
            longitude:        lng_s,
            structure:        it.structure.clone(),
            structure_number: it.structure_number.clone(),
            time1:            it.time1.clone(),
            time2:            it.time2.clone(),
        });
    }

    // Collapsed timetable[]: each station gets ONE row with both arrival
    // and departure. See hud-go's pattern table — LOAD PASSENGERS is
    // skipped (merged into preceding row's departure), UNLOAD PASSENGERS
    // and WAIT are skipped.
    let next_load_time = |start: usize| -> String {
        for j in start..sched.len() {
            match sched[j].action.as_str() {
                "LOAD PASSENGERS" => return sched[j].time1.clone(),
                "STOP AT LOCATION" | "GO VIA LOCATION" | "WAIT FOR SERVICE" => return String::new(),
                _ => {}
            }
        }
        String::new()
    };
    let mut tt_rows: Vec<TimetableRow> = Vec::new();
    for (i, it) in sched.iter().enumerate() {
        let (lat_s, lng_s) = if it.lat != 0.0 || it.lng != 0.0 {
            (fmt_coord(it.lat), fmt_coord(it.lng))
        } else { (String::new(), String::new()) };
        let idx = tt_rows.len() as i32;
        match it.action.as_str() {
            "WAIT FOR SERVICE" => {
                tt_rows.push(TimetableRow {
                    arrival:          it.time2.clone(),
                    coord_source:     Some("backend".into()),
                    departure:        next_load_time(i + 1),
                    index:            idx,
                    latitude:         lat_s,
                    location:         it.location.clone(),
                    longitude:        lng_s,
                    structure:        it.structure.clone(),
                    structure_number: it.structure_number.clone(),
                });
            }
            "STOP AT LOCATION" => {
                tt_rows.push(TimetableRow {
                    arrival:          it.time1.clone(),
                    coord_source:     Some("backend".into()),
                    departure:        next_load_time(i + 1),
                    index:            idx,
                    latitude:         lat_s,
                    location:         it.location.clone(),
                    longitude:        lng_s,
                    structure:        it.structure.clone(),
                    structure_number: it.structure_number.clone(),
                });
            }
            "GO VIA LOCATION" => {
                tt_rows.push(TimetableRow {
                    arrival:          String::new(),
                    coord_source:     Some("backend".into()),
                    departure:        String::new(),
                    index:            idx,
                    latitude:         lat_s,
                    location:         it.location.clone(),
                    longitude:        lng_s,
                    structure:        it.structure.clone(),
                    structure_number: it.structure_number.clone(),
                });
            }
            _ => {}
        }
    }

    (csv, tt_rows)
}

fn fmt_coord(v: f64) -> String { format!("{v:.7}") }

// ----- service-name composition + dedup -----------------------------

/// "HH:MM" extracted from "HH:MM:SS"; passes through anything shorter.
fn hm_only(s: &str) -> String {
    if s.len() >= 5 && s.as_bytes().get(2) == Some(&b':') {
        s[..5].to_string()
    } else { s.to_string() }
}

/// `parse_hms` returns seconds-since-midnight for "HH:MM" or "HH:MM:SS".
/// Returns -1 on parse failure (mirrors hud-go's sentinel).
fn parse_hms(s: &str) -> i32 {
    if s.is_empty() { return -1; }
    let parts: Vec<&str> = s.split(':').collect();
    if parts.len() < 2 { return -1; }
    let h = parts[0].parse::<i32>().ok();
    let m = parts[1].parse::<i32>().ok();
    let sec = parts.get(2).and_then(|p| p.parse::<i32>().ok()).unwrap_or(0);
    match (h, m) {
        (Some(h), Some(m)) => h * 3600 + m * 60 + sec,
        _ => -1,
    }
}

/// "HH:MM" from the first scheduled time to the last. Handles the
/// midnight-wrap case (end < start ⇒ add 24h). Returns "00:00" only
/// when the service genuinely has no schedule data.
fn compute_duration(svc: &Service) -> String {
    let start = parse_hms(&hm_only(&svc.start_time));
    let mut end: i32 = -1;
    for it in &svc.schedule {
        let t1 = parse_hms(&it.time1); if t1 > end { end = t1; }
        let t2 = parse_hms(&it.time2); if t2 > end { end = t2; }
    }
    if start < 0 || end < 0 { return "00:00".into(); }
    let end = if end < start { end + 24 * 3600 } else { end };
    let diff = end - start;
    format!("{:02}:{:02}", diff / 3600, (diff % 3600) / 60)
}

/// Build the composed descriptive serviceName:
///   `{FriendlyName or Name} [start_hm] [duration]`
/// Placeholder names (PlayerService / AI_Service / PlayerFormation /
/// AI_Train / …) are replaced by `tt.scenario_display_name` when the
/// orchestrator's sibling-Definition lookup populated it. Mirrors
/// hud-go's `buildServiceName` + `isGenericServiceName` so scenario
/// titles like "Cold as Ice" show up instead of "PlayerService".
fn build_service_name(tt: &Timetable, svc: &Service, start_hm: &str, duration: &str) -> String {
    let mut base: String = if !svc.friendly_name.is_empty() {
        svc.friendly_name.clone()
    } else {
        svc.name.clone()
    };
    if !tt.scenario_display_name.is_empty() && is_generic_service_name(&base) {
        base = tt.scenario_display_name.clone();
    }
    let mut out = String::with_capacity(base.len() + 12);
    out.push_str(&base);
    if !start_hm.is_empty() { out.push(' '); out.push_str(start_hm); }
    if !duration.is_empty() { out.push(' '); out.push_str(duration); }
    out
}

/// Placeholder service names that don't distinguish scenarios. Same
/// list as hud-go's `isGenericServiceName`.
fn is_generic_service_name(name: &str) -> bool {
    if name.is_empty() { return true; }
    const GENERIC: &[&str] = &[
        "PlayerService", "PlayerFormation", "PlayerTrain",
        "AI_Service", "AI_Train", "AI_Formation",
    ];
    for &prefix in GENERIC {
        if name == prefix { return true; }
        if let Some(rest) = name.strip_prefix(prefix) {
            let first = rest.as_bytes().first();
            if first == Some(&b'_') || first == Some(&b' ') { return true; }
        }
    }
    false
}

/// Compass direction from first scheduled stop with coords to the last.
/// JSON-encoded as a string when known, otherwise null. Matches hud-go's
/// `computeBound`.
fn compute_bound(svc: &Service) -> serde_json::Value {
    let mut first: Option<(f64, f64)> = None;
    let mut last:  Option<(f64, f64)> = None;
    for it in &svc.schedule {
        if it.lat == 0.0 && it.lng == 0.0 { continue; }
        if first.is_none() { first = Some((it.lat, it.lng)); }
        last = Some((it.lat, it.lng));
    }
    let (Some(f), Some(l)) = (first, last) else { return serde_json::Value::Null; };
    if f == l { return serde_json::Value::Null; }
    let d_lat = l.0 - f.0;
    let d_lng = l.1 - f.1;
    let s = if d_lat.abs() >= d_lng.abs() {
        if d_lat > 0.0 { "northbound" } else { "southbound" }
    } else if d_lng > 0.0 { "eastbound" } else { "westbound" };
    serde_json::Value::String(s.into())
}

/// Tiny mirror of hud-go's `CountryNameFromCode`: expand the ISO code
/// the extractor stamps on the route definition into a human name.
/// Returns the code unchanged when no mapping is known.
fn country_name_from_code(code: &str) -> String {
    // Accept both ISO codes ("GB") and the raw TSW values ("UK",
    // "Deutschland", "North America") the asset Country field can carry,
    // so the route line never prints an empty / bare-code country.
    match code {
        "" => String::new(),
        "GB" | "UK" | "United Kingdom"                   => "United Kingdom".into(),
        "US" | "USA" | "United States" | "North America" => "United States".into(),
        "DE" | "Germany" | "Deutschland"                 => "Germany".into(),
        "FR" | "France"                                  => "France".into(),
        "CH" | "Switzerland"                             => "Switzerland".into(),
        "AT" | "Austria"                                 => "Austria".into(),
        "IT" | "Italy"                                   => "Italy".into(),
        "NL" | "Netherlands"                             => "Netherlands".into(),
        "CZ" | "Czech Republic"                          => "Czech Republic".into(),
        "CA" | "Canada"                                  => "Canada".into(),
        other => other.to_string(),
    }
}

// ----- variant grouping --------------------------------------------

/// One (tt, svc) pair, referenced by index into the timetables slice
/// so variants survive across borrows without lifetime gymnastics.
#[derive(Clone, Copy, Debug)]
struct ServiceIdx { tt: usize, svc: usize }

/// One bucket of `(composed_name, playable)` services that should
/// collapse into a single per-service JSON. `canonical` is the
/// first-seen pair; `section_names` is the union of every
/// participating timetable's `section_name`; `additional` is the
/// remaining pairs whose `formation` data rides along on the canonical
/// JSON via `additional_formations[]`. Mirrors `serviceVariant` in
/// hud-go.
#[derive(Debug)]
struct ServiceVariant {
    canonical:     ServiceIdx,
    section_names: Vec<String>,
    additional:    Vec<ServiceIdx>,
}

impl ServiceVariant {
    /// Build the `additional_formations[]` payload from each
    /// non-canonical pair's formation. Dedupes by formation_name and
    /// skips the canonical's own formation (it's already on the parent
    /// `formations[]`).
    fn collect_additional_formations(
        &self,
        timetables: &[Timetable],
        rvds:       &HashMap<String, Rvd>,
    ) -> Vec<AdditionalFormation> {
        if self.additional.is_empty() { return Vec::new(); }
        let canon_formation = {
            let tt = &timetables[self.canonical.tt];
            let svc = &tt.services[self.canonical.svc];
            svc.formation.trim().to_string()
        };
        let mut seen: std::collections::HashSet<String> = std::collections::HashSet::new();
        if !canon_formation.is_empty() && canon_formation != "None" {
            seen.insert(canon_formation);
        }
        let mut out: Vec<AdditionalFormation> = Vec::new();
        for idx in &self.additional {
            let tt = &timetables[idx.tt];
            let svc = &tt.services[idx.svc];
            let f = svc.formation.trim().to_string();
            if f.is_empty() || f == "None" { continue; }
            if !seen.insert(f.clone()) { continue; }
            out.push(AdditionalFormation {
                formation_name: f,
                formations:     build_formations_for_service(tt, svc, rvds),
            });
        }
        out
    }
}

/// Bucket all (tt, svc) pairs across the route by (composed serviceName,
/// playable). Returns variants in first-seen order so output is stable.
fn group_service_variants(timetables: &[Timetable]) -> Vec<ServiceVariant> {
    use std::collections::HashMap;
    #[derive(Hash, Eq, PartialEq, Clone)]
    struct Key { name: String, playable: bool }
    let mut buckets: HashMap<Key, ServiceVariant> = HashMap::new();
    let mut order: Vec<Key> = Vec::new();
    for (ti, tt) in timetables.iter().enumerate() {
        for (si, svc) in tt.services.iter().enumerate() {
            let start_hm = hm_only(&svc.start_time);
            let duration = compute_duration(svc);
            let name = build_service_name(tt, svc, &start_hm, &duration);
            let key = Key { name, playable: svc.is_player_drivable };
            match buckets.get_mut(&key) {
                None => {
                    let section_names = if tt.section_name.is_empty() {
                        Vec::new()
                    } else {
                        vec![tt.section_name.clone()]
                    };
                    buckets.insert(key.clone(), ServiceVariant {
                        canonical:     ServiceIdx { tt: ti, svc: si },
                        section_names,
                        additional:    Vec::new(),
                    });
                    order.push(key);
                }
                Some(v) => {
                    if !tt.section_name.is_empty() && !v.section_names.contains(&tt.section_name) {
                        v.section_names.push(tt.section_name.clone());
                    }
                    v.additional.push(ServiceIdx { tt: ti, svc: si });
                }
            }
        }
    }
    order.into_iter().filter_map(|k| buckets.remove(&k)).collect()
}

// ----- filename ----------------------------------------------------

/// Build the descriptive per-service stem hud-go produces:
///   `{csn}__{svc}_{type}_{src}_{playable_flag}`
/// where `playable_flag` is "playable" or "not_playable". Capped at
/// 200 chars (so `path + zip header + ".json"` stays well under
/// Windows MAX_PATH).
fn filename_stem(ps: &PackageService) -> String {
    const MAX_LEN: usize = 200;
    let csn   = sanitise_filename(&ps.current_service_name);
    let svc   = sanitise_filename(&ps.service_name);
    let stype = sanitise_filename(&ps.service_type);
    let src   = sanitise_filename(&ps.source);
    let playable = if ps.playable { "playable" } else { "not_playable" };
    let mut s = String::with_capacity(csn.len() + svc.len() + stype.len() + src.len() + 24);
    if !csn.is_empty() { s.push_str(&csn); s.push_str("__"); }
    s.push_str(&svc);
    if !stype.is_empty() { s.push('_'); s.push_str(&stype); }
    if !src.is_empty()   { s.push('_'); s.push_str(&src); }
    s.push('_');
    s.push_str(playable);
    if s.len() > MAX_LEN {
        // Cap at byte boundary (sanitiser already keeps ASCII + safe
        // Unicode), trim trailing underscores so we don't end on `_`.
        let cut = s.char_indices().take_while(|(i, _)| *i < MAX_LEN).last().map(|(i, c)| i + c.len_utf8()).unwrap_or(0);
        s.truncate(cut);
        while s.ends_with('_') { s.pop(); }
    }
    s
}

/// Append `-N` (starting from 2) when `stem` already exists. Mirrors
/// hud-go's `uniqueName`.
fn unique_name(stem: String, used: &mut std::collections::HashSet<String>) -> String {
    if used.insert(stem.clone()) { return stem; }
    let mut n = 2usize;
    loop {
        let cand = format!("{stem}-{n}");
        if used.insert(cand.clone()) { return cand; }
        n += 1;
    }
}

// ----- coordinate decimation ---------------------------------------

/// Drop intermediate samples within `min_dist_m` of the previous kept
/// sample. Always keeps the first and last point. Mirrors hud-go's
/// `DecimateCoordsMeters`, minus the `Break=true` carve-out hud-go uses
/// to preserve polyline discontinuities — Rust's `ServiceCoord`
/// doesn't carry a break flag yet, so the path-builder must emit
/// contiguous polylines for now.
fn decimate_coords_meters(coords: &[ServiceCoord], min_dist_m: f64) -> Vec<ServiceCoord> {
    if coords.len() < 3 || min_dist_m <= 0.0 {
        return coords.to_vec();
    }
    let mut out: Vec<ServiceCoord> = Vec::with_capacity(coords.len());
    out.push(coords[0].clone());
    let mut last = coords[0].clone();
    for c in &coords[1..coords.len() - 1] {
        if coord_distance_meters(&last, c) >= min_dist_m {
            out.push(c.clone());
            last = c.clone();
        }
    }
    out.push(coords[coords.len() - 1].clone());
    out
}

fn coord_distance_meters(a: &ServiceCoord, b: &ServiceCoord) -> f64 {
    const R: f64 = 6_371_000.0;
    let d_lat = (b.latitude - a.latitude).to_radians();
    let d_lng = (b.longitude - a.longitude).to_radians();
    let mean_lat = ((a.latitude + b.latitude) * 0.5).to_radians();
    let x = d_lng * mean_lat.cos();
    ((d_lat * d_lat) + (x * x)).sqrt() * R
}

// =================================================================== helpers

fn asset_path_basename(asset_path: &str) -> Option<String> {
    let last_slash = asset_path.rfind('/');
    let tail = match last_slash {
        Some(i) => &asset_path[i + 1..],
        None    => asset_path,
    };
    let stripped = match tail.find('.') {
        Some(i) => &tail[..i],
        None    => tail,
    };
    if stripped.is_empty() { None } else { Some(stripped.to_string()) }
}

/// Sanitise a path component for cross-platform filesystem use.
/// Mirrors hud-go's `output.SanitizeForFilename` exactly so the zips
/// this writer produces have the same filenames as the legacy hud-go
/// extractor (importers key off the filename). Rules:
///   - `:` is removed entirely (so "23:18" → "2318", readable).
///   - Windows-illegal path chars (`< > " / \ | ? *`) and control
///     chars (< 0x20) are removed entirely.
///   - Whitespace (any) collapses to a single `_`.
///   - Runs of `_` collapse to a single `_`; leading/trailing trimmed.
///   - Everything else (including `&`, `,`, accented chars like `é`)
///     is preserved as-is. The previous ASCII-only filter dropped
///     `&` from "Plymouth & Paignton" and `é` from "Méditerranée".
pub fn sanitise_filename(name: &str) -> String {
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

fn filename_safe(name: &str) -> String {
    sanitise_filename(name)
}

fn exe_relative(rel: &str) -> PathBuf {
    std::env::current_exe()
        .ok()
        .and_then(|p| p.parent().map(|p| p.to_path_buf()))
        .map(|p| p.join(rel))
        .unwrap_or_else(|| PathBuf::from(rel))
}

/// Stamp every zip entry with the wall-clock "now" instead of the
/// MS-DOS epoch (`1980-01-01`) that `SimpleFileOptions::default()`
/// produces. The `zip` crate could do this itself via its `time` or
/// `chrono` Cargo feature, but we don't pull in either to keep the
/// build lean — go through `chrono::Utc::now()` (already a dep) and
/// hand-build the `ZipDateTime`. Falls back to the default on parse
/// failure (won't happen for current-time values).
fn now_zip_datetime() -> ZipDateTime {
    use chrono::{Datelike, Timelike};
    let n = chrono::Utc::now();
    ZipDateTime::from_date_and_time(
        n.year()   as u16,
        n.month()  as u8,
        n.day()    as u8,
        n.hour()   as u8,
        n.minute() as u8,
        n.second() as u8,
    ).unwrap_or_default()
}
