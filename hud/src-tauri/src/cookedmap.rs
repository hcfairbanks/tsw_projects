//! Cookedmap: walk an unpacked pak's tile umaps and produce the
//! rails-and-features data for one route. Mirrors hud-go's
//! `internal/cookedmap` package — `build.go`, `rails.go`, `tile.go`,
//! `route_map.go` collapsed into Rust. The output is whatever the DB
//! writer needs (lat/lng points + per-ribbon polyline blobs), not a
//! GeoJSON file: hud writes directly to its local SQLite instead of the
//! zip → import flow.
//!
//! Two-pass walk:
//!   1. **TT_*.umap**: ribbons + signals + switches + platforms +
//!      car-stop signs + route markers (the rail-graph layer).
//!   2. **every .umap**: collectables (Mastery item spawn points).
//!
//! Geometry pipeline mirrors `cookedmap.buildRailsFeature` /
//! `sampleRibbon`:
//!   * straight & circular-arc ribbons → analytic curve via
//!     [`crate::geo::arc_delta`].
//!   * clothoid ribbons → endpoint resolved through the topology graph
//!     (next ribbon's start point) then sampled as a cubic Hermite
//!     spline. Slight loss of fidelity vs the Fresnel integral but
//!     matches hud-go's renderer.
//!   * world cm → lat/lng via [`crate::geo::RouteAnchor`].

use std::collections::HashMap;
use std::fs;
use std::path::{Path, PathBuf};

use crate::geo::{self, RouteAnchor};
use crate::uasset::normalize_guid;
use crate::uasset_features_cooked::{
    self, CarStopSign, CookedCollectable, CookedFeatures, LinkedPlatform, RouteMarker, Signal, Switch,
};
use crate::uasset_ribbon_cooked::{self, CookedRibbon};

// =================================================================== tunables

const SAMPLE_STEP_CM:      f64 = 100.0;
const MAX_DEFLECTION_RAD:  f64 = 0.00873;
const MIN_SAMPLES:         usize = 4;
const MAX_SAMPLES:         usize = 5_000;

// =================================================================== output shape

#[derive(Debug, Default, Clone, serde::Serialize)]
pub struct RouteFeatures {
    /// Route anchor — auto-detected or caller-supplied. Stamped on every
    /// per-service JSON the zip writer emits so the importer can place
    /// services without re-detecting. Zero when the route has no UTM
    /// anchor (extremely rare; should never happen for shipped paks).
    pub origin_lat:      f64,
    pub origin_lng:      f64,
    /// Per-ribbon sampled polylines. Outer Vec is in caller-order
    /// (matches hud-go's MultiLineString sub-line ordering); each inner
    /// Vec is `[lng, lat]` pairs, 7 decimal places.
    pub rails:           Vec<Vec<[f64; 2]>>,
    /// Per-feature track-marker LineStrings (platform_track /
    /// siding_track / junction_track). Each `(name, marker_type, line_side, coords)`.
    pub track_feature_lines: Vec<TrackFeatureLine>,
    /// Normalised ribbon-GUID → polyline. Identical content to `rails`
    /// but keyed by ribbon GUID for the per-service path builder.
    pub per_ribbon:      HashMap<String, Vec<[f64; 2]>>,
    /// Aggregate counts surfaced for the UI / logs.
    pub stats:           RouteFeaturesStats,
    /// Minimal ribbon metadata for the Dijkstra-based service-path
    /// fallback: (normalised_guid, length_cm, start_node_guid,
    /// end_node_guid). All ribbons we successfully parsed are
    /// included — the path-builder filters by zero-length on its own.
    pub ribbon_meta:          Vec<RibbonMeta>,
    /// All NetworkTurnoutJunction records — the fallback's adjacency
    /// graph uses these to bridge node-GUID drift between adjacent
    /// ribbons at switches.
    pub switches:             Vec<crate::uasset_features_cooked::Switch>,
    /// Per-feature points with computed lat/lng. The DB writer
    /// consumes these.
    pub signal_points:        Vec<SignalPoint>,
    pub switch_points:        Vec<SwitchPoint>,
    pub car_stop_sign_points: Vec<CarStopSignPoint>,
    pub route_marker_points:  Vec<RouteMarkerPoint>,
    pub platform_points:      Vec<PlatformPoint>,
    pub collectable_points:   Vec<CollectablePoint>,
}

#[derive(Debug, Default, Clone, serde::Serialize)]
pub struct RouteFeaturesStats {
    pub tt_tiles:      u64,
    pub umaps_scanned: u64,
    pub ribbons:       u64,
    pub platforms:     u64,
    pub signals:       u64,
    pub switches:      u64,
    pub car_stop_signs: u64,
    pub route_markers: u64,
    pub collectables:  u64,
    pub elapsed_ms:    u64,
}

#[derive(Debug, Clone, serde::Serialize)]
pub struct RibbonMeta {
    pub ribbon_guid:     String,  // normalised
    pub length_cm:       f64,
    pub start_node_guid: String,
    pub end_node_guid:   String,
}

#[derive(Debug, Clone, serde::Serialize)]
pub struct TrackFeatureLine {
    pub name:         String,
    pub marker_type:  String,
    pub line_side:    String,
    pub feature_type: String, // platform_track / siding_track / junction_track
    pub ribbon_guid:  String,
    pub start:        f32,
    pub end:          f32,
    pub coords:       Vec<[f64; 2]>,
}

#[derive(Debug, Clone, serde::Serialize)]
pub struct SignalPoint     {
    pub signal_id: String, pub signal_type: String, pub ribbon_guid: String,
    pub location_fraction: f32, pub tile_name: String,
    pub lat: f64, pub lng: f64,
}
#[derive(Debug, Clone, serde::Serialize)]
pub struct SwitchPoint     {
    pub jct_guid: String, pub node_guid: String,
    pub ingoing_ribbon: String, pub outgoing_ribbon: String, pub turnout_ribbon: String,
    pub manually_controlled: bool, pub tile_name: String,
    pub lat: f64, pub lng: f64,
}
#[derive(Debug, Clone, serde::Serialize)]
pub struct CarStopSignPoint {
    pub ribbon_guid: String, pub location: f32, pub max_rail_vehicles: i32,
    pub tile_name: String,
    pub lat: f64, pub lng: f64,
}
#[derive(Debug, Clone, serde::Serialize)]
pub struct RouteMarkerPoint {
    pub name: String, pub marker_type: String, pub line_side: String,
    pub ribbon_guid: String, pub location: f32, pub start: f32, pub end: f32,
    pub tile_name: String,
    pub lat: f64, pub lng: f64,
}
#[derive(Debug, Clone, serde::Serialize)]
pub struct PlatformPoint {
    pub name: String, pub ribbon_guid: String, pub ribbon_location: f32,
    pub tile_name: String,
    pub lat: f64, pub lng: f64,
}
#[derive(Debug, Clone, serde::Serialize)]
pub struct CollectablePoint {
    pub actor_class: String, pub instance_name: String, pub collectable_id: String,
    pub tile_name: String,
    pub lat: f64, pub lng: f64,
}

// =================================================================== entry

/// Walk `workdir` and build the per-route feature set. `route_name` is
/// surfaced in stats. When `origin_lat`/`origin_lng` are zero, attempts
/// to auto-detect from a persistent-map .uexp file under the workdir.
pub fn build(
    workdir:    &Path,
    route_name: &str,
    origin_lat: f64,
    origin_lng: f64,
) -> Result<RouteFeatures, String> {
    let t0 = std::time::Instant::now();

    let (lat, lng) = if origin_lat == 0.0 || origin_lng == 0.0 {
        auto_find_origin(workdir)
            .ok_or_else(|| "origin not provided and auto-detect failed".to_string())?
    } else { (origin_lat, origin_lng) };

    let anchor = RouteAnchor::new(lat, lng);
    let _ = route_name;

    // Pass 1: TT_*.umap collection.
    let tt_tiles = walk_umaps(workdir, |name| {
        name.starts_with("TT_") && name.to_ascii_lowercase().ends_with(".umap")
    });

    let mut all_ribbons:  Vec<CookedRibbon>    = Vec::new();
    let mut all_features: CookedFeatures       = CookedFeatures::default();
    for p in &tt_tiles {
        let tile_name = file_stem(p);
        if let Ok(ribs) = uasset_ribbon_cooked::parse_cooked_ribbons_from_umap(p, &tile_name) {
            all_ribbons.extend(ribs);
        }
        if let Ok(fts) = uasset_features_cooked::parse_cooked_features_from_umap(p, &tile_name) {
            all_features.platforms.extend(fts.platforms);
            all_features.signals.extend(fts.signals);
            all_features.switches.extend(fts.switches);
            all_features.car_stop_signs.extend(fts.car_stop_signs);
            all_features.route_markers.extend(fts.route_markers);
        }
    }

    // Pass 2: every .umap → collectables.
    let all_umaps = walk_umaps(workdir, |name| name.to_ascii_lowercase().ends_with(".umap"));
    let mut all_collectables: Vec<(CookedCollectable, f64, f64)> = Vec::new();
    for p in &all_umaps {
        let base = file_stem(p);
        let Some((tx, ty)) = parse_tile_xy(&base) else { continue };
        let Ok(cs) = uasset_features_cooked::parse_cooked_collectables_from_umap(p, &base) else { continue };
        if cs.is_empty() { continue; }
        let ox = (tx as f64) * 100_000.0;
        let oy = (ty as f64) * 100_000.0;
        for c in cs {
            let wx = ox + c.x;
            let wy = oy + c.y;
            all_collectables.push((c, wx, wy));
        }
    }

    // Build the rails geometry.
    let (rails, per_ribbon, _track_rules) = build_rails(&mut all_ribbons, &anchor);
    let ends_by_guid = compute_endpoints(&all_ribbons);
    let ribbons_by_guid: HashMap<String, CookedRibbon> = all_ribbons.iter()
        .map(|r| (normalize_guid(&r.ribbon_guid), r.clone()))
        .collect();

    // Track-feature LineStrings — every route marker whose Start..End
    // spans a non-zero range gets a polyline.
    let track_feature_lines = build_track_feature_lines(
        &all_features.route_markers, &ribbons_by_guid, &ends_by_guid, &anchor,
    );

    // Project all the point features through the anchor.
    let signal_points = project_signals(&all_features.signals, &ribbons_by_guid, &ends_by_guid, &anchor);
    let switch_points = project_switches(&all_features.switches, &ribbons_by_guid, &ends_by_guid, &anchor);
    let car_stop_sign_points = project_car_stops(&all_features.car_stop_signs, &ribbons_by_guid, &ends_by_guid, &anchor);
    let route_marker_points = project_route_markers(&all_features.route_markers, &ribbons_by_guid, &ends_by_guid, &anchor);
    let platform_points = project_platforms(&all_features.platforms, &ribbons_by_guid, &ends_by_guid, &anchor);
    let collectable_points = project_collectables(&all_collectables, &anchor);

    let stats = RouteFeaturesStats {
        tt_tiles:       tt_tiles.len() as u64,
        umaps_scanned:  all_umaps.len() as u64,
        ribbons:        all_ribbons.len() as u64,
        platforms:      all_features.platforms.len() as u64,
        signals:        all_features.signals.len() as u64,
        switches:       all_features.switches.len() as u64,
        car_stop_signs: all_features.car_stop_signs.len() as u64,
        route_markers:  all_features.route_markers.len() as u64,
        collectables:   all_collectables.len() as u64,
        elapsed_ms:     t0.elapsed().as_millis() as u64,
    };

    // Minimal ribbon metadata for the fallback path-builder.
    let ribbon_meta: Vec<RibbonMeta> = all_ribbons.iter()
        .filter(|r| r.length > 0.0)
        .map(|r| RibbonMeta {
            ribbon_guid:     normalize_guid(&r.ribbon_guid),
            length_cm:       r.length,
            start_node_guid: r.start_node_guid.clone(),
            end_node_guid:   r.end_node_guid.clone(),
        })
        .collect();

    Ok(RouteFeatures {
        origin_lat: lat, origin_lng: lng,
        rails, track_feature_lines, per_ribbon,
        stats,
        ribbon_meta,
        switches: all_features.switches,
        signal_points, switch_points, car_stop_sign_points,
        route_marker_points, platform_points, collectable_points,
    })
}

// =================================================================== rails

#[derive(Default, Clone, Copy)]
struct RibbonEnd { ex: f64, ey: f64, etx: f64, ety: f64, has: bool }

fn is_curved_arc(r: &CookedRibbon) -> bool {
    r.curve_class == "NetworkCurveCircularArc"
        && r.has_radius && r.radius != 0.0
        && !r.radius.is_infinite() && !r.radius.is_nan()
}

#[inline] fn round7(v: f64) -> f64 { (v * 1e7).round() / 1e7 }

/// Replicates hud-go's `buildRailsFeature`. Returns
/// `(rails_lines, per_ribbon_map, track_rules_parallel)`. Side-effect:
/// normalises each ribbon's tangent in-place.
fn build_rails(
    ribbons: &mut Vec<CookedRibbon>,
    anchor:  &RouteAnchor,
) -> (Vec<Vec<[f64; 2]>>, HashMap<String, Vec<[f64; 2]>>, Vec<String>) {
    // Node graph: node_guid → list of (ribbon idx, at_start?).
    let mut node: HashMap<String, Vec<(usize, bool)>> = HashMap::new();
    for (i, r) in ribbons.iter().enumerate() {
        if !r.start_node_guid.is_empty() {
            node.entry(r.start_node_guid.clone()).or_default().push((i, true));
        }
        if !r.end_node_guid.is_empty() {
            node.entry(r.end_node_guid.clone()).or_default().push((i, false));
        }
    }

    // First pass: resolve endpoints for straights + arcs, normalise tangents.
    let mut ends: Vec<RibbonEnd> = vec![RibbonEnd::default(); ribbons.len()];
    for i in 0..ribbons.len() {
        let r = &mut ribbons[i];
        if r.length <= 0.0 { continue; }
        let tn = r.tangent_x.hypot(r.tangent_y);
        if tn == 0.0 { continue; }
        r.tangent_x /= tn;
        r.tangent_y /= tn;
        if is_curved_arc(r) {
            let (dx, dy) = geo::arc_delta(0.0, 0.0, r.tangent_x, r.tangent_y, r.radius, r.length);
            let sweep = -r.length / r.radius;
            let cs = sweep.cos();
            let sn = sweep.sin();
            ends[i] = RibbonEnd {
                ex:  r.start_x + dx,
                ey:  r.start_y + dy,
                etx: r.tangent_x * cs - r.tangent_y * sn,
                ety: r.tangent_x * sn + r.tangent_y * cs,
                has: true,
            };
        } else if r.curve_class != "NetworkCurveClothoidSpiral" {
            ends[i] = RibbonEnd {
                ex:  r.start_x + r.tangent_x * r.length,
                ey:  r.start_y + r.tangent_y * r.length,
                etx: r.tangent_x,
                ety: r.tangent_y,
                has: true,
            };
        }
    }

    // Second pass: resolve clothoid endpoints via the node graph.
    let len = ribbons.len();
    for i in 0..len {
        if ribbons[i].curve_class != "NetworkCurveClothoidSpiral" { continue; }
        let (sx, sy, length, tx, ty);
        {
            let r = &ribbons[i];
            sx = r.start_x; sy = r.start_y; length = r.length;
            tx = r.tangent_x; ty = r.tangent_y;
        }
        let end_node_guid = ribbons[i].end_node_guid.clone();
        let mut found: Option<(f64, f64, f64, f64)> = None;
        if let Some(neighbours) = node.get(&end_node_guid) {
            for &(idx, at_start) in neighbours {
                if idx == i { continue; }
                let nb = &ribbons[idx];
                let (ex, ey, etx, ety) = if at_start {
                    (nb.start_x, nb.start_y, nb.tangent_x, nb.tangent_y)
                } else if ends[idx].has {
                    (ends[idx].ex, ends[idx].ey, -ends[idx].etx, -ends[idx].ety)
                } else { continue };
                let gap = ((ex - sx).powi(2) + (ey - sy).powi(2)).sqrt();
                if gap > length * 1.5 { continue; }
                found = Some((ex, ey, etx, ety));
                break;
            }
        }
        let (ex, ey, etx, ety) = found.unwrap_or((
            sx + tx * length, sy + ty * length, tx, ty,
        ));
        ends[i] = RibbonEnd { ex, ey, etx, ety, has: true };
    }

    // Emit per-ribbon polylines.
    let mut out: Vec<Vec<[f64; 2]>> = Vec::new();
    let mut rules: Vec<String> = Vec::new();
    let mut per_ribbon: HashMap<String, Vec<[f64; 2]>> = HashMap::with_capacity(ribbons.len());
    for (i, r) in ribbons.iter().enumerate() {
        if r.length <= 0.0 || r.tangent_x.hypot(r.tangent_y) == 0.0 { continue; }
        let coords = sample_ribbon(r, ends[i], anchor);
        if coords.len() >= 2 {
            per_ribbon.insert(normalize_guid(&r.ribbon_guid), coords.clone());
            out.push(coords);
            rules.push(r.track_rule.clone());
        }
    }
    (out, per_ribbon, rules)
}

fn compute_endpoints(ribbons: &[CookedRibbon]) -> HashMap<String, RibbonEnd> {
    let mut out: HashMap<String, RibbonEnd> = HashMap::with_capacity(ribbons.len());
    for r in ribbons {
        if r.length <= 0.0 { continue; }
        let tn = r.tangent_x.hypot(r.tangent_y);
        if tn == 0.0 { continue; }
        let key = normalize_guid(&r.ribbon_guid);
        if is_curved_arc(r) {
            let (dx, dy) = geo::arc_delta(0.0, 0.0, r.tangent_x, r.tangent_y, r.radius, r.length);
            let sweep = -r.length / r.radius;
            let cs = sweep.cos();
            let sn = sweep.sin();
            out.insert(key, RibbonEnd {
                ex:  r.start_x + dx, ey: r.start_y + dy,
                etx: r.tangent_x * cs - r.tangent_y * sn,
                ety: r.tangent_x * sn + r.tangent_y * cs,
                has: true,
            });
        } else {
            out.insert(key, RibbonEnd {
                ex:  r.start_x + r.tangent_x * r.length,
                ey:  r.start_y + r.tangent_y * r.length,
                etx: r.tangent_x,
                ety: r.tangent_y,
                has: true,
            });
        }
    }
    out
}

fn sample_ribbon(r: &CookedRibbon, e: RibbonEnd, anchor: &RouteAnchor) -> Vec<[f64; 2]> {
    let mut n = (r.length / SAMPLE_STEP_CM).ceil() as usize;
    if is_curved_arc(r) {
        let cn = (r.length / (r.radius.abs() * MAX_DEFLECTION_RAD)).ceil() as usize;
        if cn > n { n = cn; }
    }
    n = n.clamp(MIN_SAMPLES, MAX_SAMPLES);
    let nf = n as f64;
    let mut out: Vec<[f64; 2]> = Vec::with_capacity(n + 1);
    for i in 0..=n {
        let t = (i as f64) / nf;
        let (x, y);
        if is_curved_arc(r) {
            let s = t * r.length;
            let (dx, dy) = geo::arc_delta(0.0, 0.0, r.tangent_x, r.tangent_y, r.radius, s);
            x = r.start_x + dx;
            y = r.start_y + dy;
        } else if r.curve_class == "NetworkCurveClothoidSpiral" && e.has {
            let chord = ((e.ex - r.start_x).powi(2) + (e.ey - r.start_y).powi(2)).sqrt();
            let m = chord / 3.0;
            let t2 = t * t;
            let t3 = t2 * t;
            let h00 =  2.0 * t3 - 3.0 * t2 + 1.0;
            let h10 =        t3 - 2.0 * t2 + t;
            let h01 = -2.0 * t3 + 3.0 * t2;
            let h11 =        t3 -       t2;
            x = h00 * r.start_x + h10 * r.tangent_x * m + h01 * e.ex + h11 * e.etx * m;
            y = h00 * r.start_y + h10 * r.tangent_y * m + h01 * e.ey + h11 * e.ety * m;
        } else {
            let s = t * r.length;
            x = r.start_x + r.tangent_x * s;
            y = r.start_y + r.tangent_y * s;
        }
        let (lat, lng) = anchor.world_to_lat_lng(x / 100.0, y / 100.0);
        out.push([round7(lng), round7(lat)]);
    }
    out
}

fn sample_ribbon_range(
    r: &CookedRibbon, e: RibbonEnd,
    start_frac: f64, end_frac: f64,
    anchor: &RouteAnchor,
) -> Vec<[f64; 2]> {
    let span = end_frac - start_frac;
    if span <= 0.0 { return Vec::new(); }
    let range_len = span * r.length;
    let mut n = (range_len / SAMPLE_STEP_CM).ceil() as usize;
    if is_curved_arc(r) {
        let cn = (range_len / (r.radius.abs() * MAX_DEFLECTION_RAD)).ceil() as usize;
        if cn > n { n = cn; }
    }
    n = n.clamp(MIN_SAMPLES, MAX_SAMPLES);
    let tn = r.tangent_x.hypot(r.tangent_y);
    if tn == 0.0 { return Vec::new(); }
    let tx = r.tangent_x / tn;
    let ty = r.tangent_y / tn;
    let nf = n as f64;
    let mut out: Vec<[f64; 2]> = Vec::with_capacity(n + 1);
    for i in 0..=n {
        let f = (i as f64) / nf;
        let t = start_frac + f * span;
        let (x, y);
        if is_curved_arc(r) {
            let s = t * r.length;
            let (dx, dy) = geo::arc_delta(0.0, 0.0, tx, ty, r.radius, s);
            x = r.start_x + dx;
            y = r.start_y + dy;
        } else if r.curve_class == "NetworkCurveClothoidSpiral" && e.has {
            let chord = ((e.ex - r.start_x).powi(2) + (e.ey - r.start_y).powi(2)).sqrt();
            let m = chord / 3.0;
            let t2 = t * t;
            let t3 = t2 * t;
            let h00 =  2.0 * t3 - 3.0 * t2 + 1.0;
            let h10 =        t3 - 2.0 * t2 + t;
            let h01 = -2.0 * t3 + 3.0 * t2;
            let h11 =        t3 -       t2;
            x = h00 * r.start_x + h10 * tx * m + h01 * e.ex + h11 * e.etx * m;
            y = h00 * r.start_y + h10 * ty * m + h01 * e.ey + h11 * e.ety * m;
        } else {
            let s = t * r.length;
            x = r.start_x + tx * s;
            y = r.start_y + ty * s;
        }
        let (lat, lng) = anchor.world_to_lat_lng(x / 100.0, y / 100.0);
        out.push([round7(lng), round7(lat)]);
    }
    out
}

// =================================================================== feature projection

/// Project a single ribbon location-fraction to world cm via the
/// ribbon's curve geometry.
fn project_on_ribbon(r: &CookedRibbon, e: RibbonEnd, frac: f64) -> Option<(f64, f64)> {
    if r.length <= 0.0 { return None; }
    let tn = r.tangent_x.hypot(r.tangent_y);
    if tn == 0.0 { return None; }
    let tx = r.tangent_x / tn;
    let ty = r.tangent_y / tn;
    let t = frac.clamp(0.0, 1.0);
    if is_curved_arc(r) {
        let s = t * r.length;
        let (dx, dy) = geo::arc_delta(0.0, 0.0, tx, ty, r.radius, s);
        Some((r.start_x + dx, r.start_y + dy))
    } else if r.curve_class == "NetworkCurveClothoidSpiral" && e.has {
        let chord = ((e.ex - r.start_x).powi(2) + (e.ey - r.start_y).powi(2)).sqrt();
        let m = chord / 3.0;
        let t2 = t * t; let t3 = t2 * t;
        let h00 =  2.0 * t3 - 3.0 * t2 + 1.0;
        let h10 =        t3 - 2.0 * t2 + t;
        let h01 = -2.0 * t3 + 3.0 * t2;
        let h11 =        t3 -       t2;
        let x = h00 * r.start_x + h10 * tx * m + h01 * e.ex + h11 * e.etx * m;
        let y = h00 * r.start_y + h10 * ty * m + h01 * e.ey + h11 * e.ety * m;
        Some((x, y))
    } else {
        let s = t * r.length;
        Some((r.start_x + tx * s, r.start_y + ty * s))
    }
}

fn project_signals(
    src:   &[Signal],
    ribbons: &HashMap<String, CookedRibbon>,
    ends:    &HashMap<String, RibbonEnd>,
    anchor:  &RouteAnchor,
) -> Vec<SignalPoint> {
    let mut out: Vec<SignalPoint> = Vec::with_capacity(src.len());
    for s in src {
        let key = normalize_guid(&s.ribbon_guid);
        let Some(rb) = ribbons.get(&key) else { continue };
        let e = ends.get(&key).copied().unwrap_or_default();
        let Some((wx, wy)) = project_on_ribbon(rb, e, s.location_fraction as f64) else { continue };
        let (lat, lng) = anchor.world_to_lat_lng(wx / 100.0, wy / 100.0);
        out.push(SignalPoint {
            signal_id: s.signal_id.clone(),
            signal_type: s.signal_type.clone(),
            ribbon_guid: s.ribbon_guid.clone(),
            location_fraction: s.location_fraction,
            tile_name: s.tile_name.clone(),
            lat: round7(lat), lng: round7(lng),
        });
    }
    out
}

fn project_switches(
    src:   &[Switch],
    ribbons: &HashMap<String, CookedRibbon>,
    ends:    &HashMap<String, RibbonEnd>,
    anchor:  &RouteAnchor,
) -> Vec<SwitchPoint> {
    // Switch position = start of OutgoingRibbon (or end of IngoingRibbon
    // when outgoing isn't on the route). Mirrors hud-go's switch placement.
    let mut out: Vec<SwitchPoint> = Vec::with_capacity(src.len());
    for s in src {
        // (key, frac) — outgoing/turnout start at the junction; ingoing ends
        // at the junction.
        let candidates: [(&str, f64); 3] = [
            (s.outgoing_ribbon.as_str(), 0.0),
            (s.ingoing_ribbon.as_str(),  1.0),
            (s.turnout_ribbon.as_str(),  0.0),
        ];
        let mut placed: Option<(f64, f64)> = None;
        for (key_raw, frac) in candidates {
            if key_raw.is_empty() { continue; }
            let key = normalize_guid(key_raw);
            let Some(rb) = ribbons.get(&key) else { continue };
            let e = ends.get(&key).copied().unwrap_or_default();
            if let Some(p) = project_on_ribbon(rb, e, frac) { placed = Some(p); break; }
        }
        let Some((wx, wy)) = placed else { continue };
        let (lat, lng) = anchor.world_to_lat_lng(wx / 100.0, wy / 100.0);
        out.push(SwitchPoint {
            jct_guid: s.jct_guid.clone(), node_guid: s.node_guid.clone(),
            ingoing_ribbon: s.ingoing_ribbon.clone(),
            outgoing_ribbon: s.outgoing_ribbon.clone(),
            turnout_ribbon: s.turnout_ribbon.clone(),
            manually_controlled: s.manually_controlled,
            tile_name: s.tile_name.clone(),
            lat: round7(lat), lng: round7(lng),
        });
    }
    out
}

fn project_car_stops(
    src:   &[CarStopSign],
    ribbons: &HashMap<String, CookedRibbon>,
    ends:    &HashMap<String, RibbonEnd>,
    anchor:  &RouteAnchor,
) -> Vec<CarStopSignPoint> {
    let mut out: Vec<CarStopSignPoint> = Vec::with_capacity(src.len());
    for c in src {
        let key = normalize_guid(&c.ribbon_guid);
        let Some(rb) = ribbons.get(&key) else { continue };
        let e = ends.get(&key).copied().unwrap_or_default();
        let Some((wx, wy)) = project_on_ribbon(rb, e, c.location as f64) else { continue };
        let (lat, lng) = anchor.world_to_lat_lng(wx / 100.0, wy / 100.0);
        out.push(CarStopSignPoint {
            ribbon_guid: c.ribbon_guid.clone(),
            location: c.location,
            max_rail_vehicles: c.max_number_of_rail_vehicles,
            tile_name: c.tile_name.clone(),
            lat: round7(lat), lng: round7(lng),
        });
    }
    out
}

fn project_route_markers(
    src: &[RouteMarker],
    ribbons: &HashMap<String, CookedRibbon>,
    ends:    &HashMap<String, RibbonEnd>,
    anchor:  &RouteAnchor,
) -> Vec<RouteMarkerPoint> {
    let mut out: Vec<RouteMarkerPoint> = Vec::with_capacity(src.len());
    for m in src {
        let key = normalize_guid(&m.ribbon_guid);
        let Some(rb) = ribbons.get(&key) else { continue };
        let e = ends.get(&key).copied().unwrap_or_default();
        // Marker centre = (start + end) / 2 when both are non-zero, else
        // .Location. Falls back to 0 when none are set.
        let frac = if m.end > m.start {
            (((m.start + m.end) / 2.0) as f64).clamp(0.0, 1.0)
        } else if m.location > 0.0 {
            (m.location as f64).clamp(0.0, 1.0)
        } else { 0.0 };
        let Some((wx, wy)) = project_on_ribbon(rb, e, frac) else { continue };
        let (lat, lng) = anchor.world_to_lat_lng(wx / 100.0, wy / 100.0);
        out.push(RouteMarkerPoint {
            name: m.name.clone(), marker_type: m.marker_type.clone(), line_side: m.line_side.clone(),
            ribbon_guid: m.ribbon_guid.clone(),
            location: m.location, start: m.start, end: m.end,
            tile_name: m.tile_name.clone(),
            lat: round7(lat), lng: round7(lng),
        });
    }
    out
}

fn project_platforms(
    src: &[LinkedPlatform],
    ribbons: &HashMap<String, CookedRibbon>,
    ends:    &HashMap<String, RibbonEnd>,
    anchor:  &RouteAnchor,
) -> Vec<PlatformPoint> {
    let mut out: Vec<PlatformPoint> = Vec::with_capacity(src.len());
    for p in src {
        let key = normalize_guid(&p.ribbon_guid);
        let Some(rb) = ribbons.get(&key) else { continue };
        let e = ends.get(&key).copied().unwrap_or_default();
        let Some((wx, wy)) = project_on_ribbon(rb, e, p.ribbon_location as f64) else { continue };
        let (lat, lng) = anchor.world_to_lat_lng(wx / 100.0, wy / 100.0);
        out.push(PlatformPoint {
            name: p.name.clone(), ribbon_guid: p.ribbon_guid.clone(),
            ribbon_location: p.ribbon_location, tile_name: p.tile_name.clone(),
            lat: round7(lat), lng: round7(lng),
        });
    }
    out
}

fn project_collectables(src: &[(CookedCollectable, f64, f64)], anchor: &RouteAnchor) -> Vec<CollectablePoint> {
    src.iter().map(|(c, wx, wy)| {
        let (lat, lng) = anchor.world_to_lat_lng(wx / 100.0, wy / 100.0);
        CollectablePoint {
            actor_class: c.actor_class.clone(),
            instance_name: c.instance_name.clone(),
            collectable_id: c.collectable_id.clone(),
            tile_name: c.tile_name.clone(),
            lat: round7(lat), lng: round7(lng),
        }
    }).collect()
}

fn build_track_feature_lines(
    markers: &[RouteMarker],
    ribbons: &HashMap<String, CookedRibbon>,
    ends:    &HashMap<String, RibbonEnd>,
    anchor:  &RouteAnchor,
) -> Vec<TrackFeatureLine> {
    let mut out: Vec<TrackFeatureLine> = Vec::new();
    for m in markers {
        let feature_type = match m.marker_type.as_str() {
            "Platform" => "platform_track",
            "Siding"   => "siding_track",
            "Junction" => "junction_track",
            _ => continue,
        };
        let key = normalize_guid(&m.ribbon_guid);
        let Some(rb) = ribbons.get(&key) else { continue };
        if rb.length <= 0.0 { continue; }
        let mut start_frac = m.start as f64;
        let mut end_frac   = m.end   as f64;
        if end_frac <= start_frac {
            if m.location > 0.0 {
                let half = 0.025_f64;
                start_frac = (m.location as f64) - half;
                end_frac   = (m.location as f64) + half;
                if start_frac < 0.0 { start_frac = 0.0; }
                if end_frac   > 1.0 { end_frac   = 1.0; }
            } else { continue; }
        }
        let e = ends.get(&key).copied().unwrap_or_default();
        let coords = sample_ribbon_range(rb, e, start_frac, end_frac, anchor);
        if coords.len() < 2 { continue; }
        out.push(TrackFeatureLine {
            name: m.name.clone(), marker_type: m.marker_type.clone(),
            line_side: m.line_side.clone(),
            feature_type: feature_type.to_string(),
            ribbon_guid: m.ribbon_guid.clone(),
            start: m.start, end: m.end,
            coords,
        });
    }
    out
}

// =================================================================== fs helpers

fn walk_umaps<F>(root: &Path, mut accept: F) -> Vec<PathBuf>
where F: FnMut(&str) -> bool
{
    let mut out: Vec<PathBuf> = Vec::new();
    let mut stack = vec![root.to_path_buf()];
    while let Some(d) = stack.pop() {
        let Ok(rd) = fs::read_dir(&d) else { continue };
        for entry in rd.flatten() {
            let Ok(ft) = entry.file_type() else { continue };
            let path = entry.path();
            if ft.is_dir() { stack.push(path); continue; }
            if !ft.is_file() { continue; }
            let Some(name) = path.file_name().and_then(|s| s.to_str()) else { continue };
            if accept(name) { out.push(path); }
        }
    }
    out.sort();
    out
}

fn file_stem(p: &Path) -> String {
    p.file_stem().and_then(|s| s.to_str()).unwrap_or("").to_string()
}

fn parse_tile_xy(base: &str) -> Option<(i32, i32)> {
    // Matches `_x<int>_y<int>` at the end of `base`.
    let bytes = base.as_bytes();
    // Walk backwards to find the second-to-last `_x`. Cheap-ish for short names.
    // Find "_y" first.
    let y_pos = base.rfind("_y")?;
    let after_y = &base[y_pos + 2..];
    let y: i32 = parse_int_trailing(after_y)?;
    let prefix = &base[..y_pos];
    let x_pos = prefix.rfind("_x")?;
    let after_x = &prefix[x_pos + 2..];
    let x: i32 = parse_int_trailing(after_x)?;
    let _ = bytes;
    Some((x, y))
}

fn parse_int_trailing(s: &str) -> Option<i32> {
    // Accept a leading minus sign.
    let mut end = 0;
    let bytes = s.as_bytes();
    if bytes.is_empty() { return None; }
    let mut i = 0;
    if bytes[0] == b'-' { i = 1; end = 1; }
    while i < bytes.len() && bytes[i].is_ascii_digit() {
        end = i + 1;
        i += 1;
    }
    if end == 0 { return None; }
    // If we accepted only a minus sign, end == 1 with no digits → reject.
    if end == 1 && bytes[0] == b'-' { return None; }
    s[..end].parse::<i32>().ok()
}

// =================================================================== origin auto-detect

pub(crate) fn auto_find_origin(workdir: &Path) -> Option<(f64, f64)> {
    // Strategy 1: parent is exactly "Map".
    if let Some(p) = scan_workdir_for_origin(workdir, |parts| {
        if parts.len() < 3 { return false; }
        let p2 = parts[parts.len() - 2].to_ascii_lowercase();
        let p3 = parts[parts.len() - 3].to_ascii_lowercase();
        p2 == "map" && p3 == "content"
    }) { return Some(p); }
    // Strategy 2: parent ends in "Map" but isn't "Map".
    scan_workdir_for_origin(workdir, |parts| {
        if parts.len() < 3 { return false; }
        let p2 = parts[parts.len() - 2].to_ascii_lowercase();
        let p3 = parts[parts.len() - 3].to_ascii_lowercase();
        p3 == "content" && p2 != "map" && p2.ends_with("map")
    })
}

fn scan_workdir_for_origin<F>(workdir: &Path, mut matcher: F) -> Option<(f64, f64)>
where F: FnMut(&[String]) -> bool
{
    let mut stack = vec![workdir.to_path_buf()];
    while let Some(d) = stack.pop() {
        let Ok(rd) = fs::read_dir(&d) else { continue };
        for entry in rd.flatten() {
            let Ok(ft) = entry.file_type() else { continue };
            let path = entry.path();
            if ft.is_dir() { stack.push(path); continue; }
            if !ft.is_file() { continue; }
            let Some(name) = path.file_name().and_then(|s| s.to_str()) else { continue };
            if !name.to_ascii_lowercase().ends_with(".uexp") { continue; }
            // Reject tile uexps and anything under a "Tiles" segment.
            let parts: Vec<String> = path.iter()
                .map(|c| c.to_string_lossy().to_string())
                .collect();
            if parts.iter().any(|p| p.eq_ignore_ascii_case("Tiles")) { continue; }
            if !matcher(&parts) { continue; }
            if let Ok((lat, lng)) = geo::extract_origin_from_uexp(&path) {
                if lat != 0.0 && lng != 0.0 { return Some((lat, lng)); }
            }
        }
    }
    None
}
