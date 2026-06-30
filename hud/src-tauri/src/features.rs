//! Per-timetable feature filter. Port of hud-go's
//! `internal/output/timetable_features.go`. Takes a route's full GeoJSON
//! `Feature` set + the timetable's schedule + the service path coords, returns
//! the subset that should render on this timetable's HUD map.
//!
//! Match rules by feature shape (mirror of the Go file's doc comment):
//!   - Track LineStrings (feature_type: platform_track / siding_track /
//!     line_track / running_track) — keep if (location, structure, number)
//!     appears in the schedule, OR any vertex within MarkerProxM of the path.
//!   - Untyped LineStrings — drop (merged-rails layer, route-only mode).
//!   - Point feature_kind=car_stop_sign — strict usedNames(platform_name).
//!   - Point feature_kind=track_marker — marker_type=Platform AND name in
//!     usedNames.
//!   - Point feature_kind=collectable — proximity only (CollectableProxM).
//!   - Legacy platform Points — strict schedule-tuple match.
//!   - Points with signal_id — proximity (SignalProxM, segment distance).
//!   - Points with jct_guid — proximity (MarkerProxM, vertex distance).

use serde_json::Value;
use std::collections::{HashMap, HashSet};

#[derive(Clone, Copy)]
pub struct FilterOptions {
    pub marker_prox_m: f64,
    pub signal_prox_m: f64,
    pub collectable_prox_m: f64,
}

impl Default for FilterOptions {
    fn default() -> Self {
        FilterOptions { marker_prox_m: 3.0, signal_prox_m: 3.0, collectable_prox_m: 50.0 }
    }
}

pub struct ScheduleEntryRef {
    pub location: String,
    pub structure: String,
    pub structure_number: String,
}

#[derive(Clone, Copy)]
pub struct ServiceCoord {
    pub latitude: f64,
    pub longitude: f64,
}

// ---- bbox ---------------------------------------------------------------

#[derive(Default, Clone, Copy)]
struct Bbox {
    valid: bool,
    min_lat: f64,
    max_lat: f64,
    min_lng: f64,
    max_lng: f64,
}

impl Bbox {
    fn extend(&mut self, lat: f64, lng: f64) {
        if !self.valid {
            self.min_lat = lat;
            self.max_lat = lat;
            self.min_lng = lng;
            self.max_lng = lng;
            self.valid = true;
            return;
        }
        if lat < self.min_lat { self.min_lat = lat; }
        if lat > self.max_lat { self.max_lat = lat; }
        if lng < self.min_lng { self.min_lng = lng; }
        if lng > self.max_lng { self.max_lng = lng; }
    }
    fn point(lat: f64, lng: f64) -> Self {
        Bbox { valid: true, min_lat: lat, max_lat: lat, min_lng: lng, max_lng: lng }
    }
    /// Inflated overlap test. The Go version over-inflates (~111 km/° on both
    /// axes) so a feature near the cull threshold isn't dropped by a
    /// longitude-cosine miscount; false positive costs one wasted distance
    /// check, false negative would lose a real feature.
    fn intersects_inflated(&self, other: &Bbox, inflate_m: f64) -> bool {
        if !self.valid || !other.valid {
            return false;
        }
        let deg = inflate_m / 111_000.0;
        self.max_lat + deg >= other.min_lat - deg
            && self.min_lat - deg <= other.max_lat + deg
            && self.max_lng + deg >= other.min_lng - deg
            && self.min_lng - deg <= other.max_lng + deg
    }
}

// ---- path spatial index -------------------------------------------------

const PATH_INDEX_CELL_DEG: f64 = 0.001; // ~70–110 m at temperate latitudes

pub struct PathIndex {
    coords: Vec<ServiceCoord>,
    cells: HashMap<(i32, i32), Vec<i32>>,
    bbox: Bbox,
}

/// Build a PathIndex from a `[[lng,lat],…]` JSON blob (the
/// `timetable_coordinates.coordinates` shape). Returns None on parse failure
/// or an empty path. Exposed so the overlay map can proximity-filter a
/// route's point features against ONE service's path — the same gate
/// filter_route_features_for_timetable applies.
pub fn path_index_from_lnglat(blob: &str) -> Option<PathIndex> {
    let raw: Vec<Vec<f64>> = serde_json::from_str(blob).ok()?;
    let coords: Vec<ServiceCoord> = raw
        .iter()
        .filter(|p| p.len() >= 2)
        .map(|p| ServiceCoord { longitude: p[0], latitude: p[1] })
        .collect();
    if coords.is_empty() { return None; }
    Some(PathIndex::build(coords))
}

/// Minimum distance (metres) from a point to the service path's vertices.
pub fn point_path_distance_m(idx: &PathIndex, lat: f64, lng: f64) -> f64 {
    min_distance_to_path_m(lat, lng, idx)
}

impl PathIndex {
    fn build(coords: Vec<ServiceCoord>) -> Self {
        let mut p = PathIndex { coords, cells: HashMap::new(), bbox: Bbox::default() };
        if p.coords.is_empty() {
            return p;
        }
        p.cells.reserve(p.coords.len());
        for (i, c) in p.coords.iter().enumerate() {
            p.bbox.extend(c.latitude, c.longitude);
            let cx = (c.longitude / PATH_INDEX_CELL_DEG).floor() as i32;
            let cy = (c.latitude / PATH_INDEX_CELL_DEG).floor() as i32;
            p.cells.entry((cx, cy)).or_default().push(i as i32);
        }
        p
    }

    /// 9-cell neighborhood scan; fn returns false to short-circuit.
    fn for_each_nearby<F: FnMut(usize) -> bool>(&self, lat: f64, lng: f64, mut f: F) {
        if self.cells.is_empty() {
            return;
        }
        let cx = (lng / PATH_INDEX_CELL_DEG).floor() as i32;
        let cy = (lat / PATH_INDEX_CELL_DEG).floor() as i32;
        for dx in -1..=1 {
            for dy in -1..=1 {
                if let Some(ids) = self.cells.get(&(cx + dx, cy + dy)) {
                    for &i in ids {
                        if !f(i as usize) {
                            return;
                        }
                    }
                }
            }
        }
    }
}

// ---- distance helpers ---------------------------------------------------

fn equirect_meters(lat1: f64, lng1: f64, lat2: f64, lng2: f64) -> f64 {
    const EARTH_RADIUS_M: f64 = 6_371_000.0;
    let to_rad = std::f64::consts::PI / 180.0;
    let dlat = (lat2 - lat1) * to_rad;
    let dlng = (lng2 - lng1) * to_rad;
    let mid = (lat1 + lat2) * 0.5 * to_rad;
    let x = dlng * mid.cos();
    (dlat.hypot(x)) * EARTH_RADIUS_M
}

fn min_distance_to_path_m(lat: f64, lng: f64, idx: &PathIndex) -> f64 {
    if idx.coords.is_empty() {
        return f64::INFINITY;
    }
    let mut best = f64::INFINITY;
    idx.for_each_nearby(lat, lng, |i| {
        let c = &idx.coords[i];
        let d = equirect_meters(lat, lng, c.latitude, c.longitude);
        if d < best {
            best = d;
            if best < 1.0 { return false; }
        }
        true
    });
    best
}

fn min_distance_to_path_segments_m(lat: f64, lng: f64, idx: &PathIndex) -> f64 {
    if idx.coords.is_empty() {
        return f64::INFINITY;
    }
    if idx.coords.len() == 1 {
        let c = &idx.coords[0];
        return equirect_meters(lat, lng, c.latitude, c.longitude);
    }
    let mut best = f64::INFINITY;
    // Bounded dedup of seen segment-starts in the 9-cell neighborhood (Go
    // uses a stack array of 32; same bound here, linear scan is fastest).
    let mut seen: [i32; 32] = [-1; 32];
    let mut seen_n = 0usize;
    idx.for_each_nearby(lat, lng, |i| {
        for &seg_start in &[i as i32 - 1, i as i32] {
            if seg_start < 0 || seg_start as usize + 1 >= idx.coords.len() {
                continue;
            }
            let mut dup = false;
            for k in 0..seen_n {
                if seen[k] == seg_start { dup = true; break; }
            }
            if dup { continue; }
            if seen_n < seen.len() {
                seen[seen_n] = seg_start;
                seen_n += 1;
            }
            let a = idx.coords[seg_start as usize];
            let b = idx.coords[seg_start as usize + 1];
            let (ax, ay) = (a.longitude, a.latitude);
            let (bx, by) = (b.longitude, b.latitude);
            let (dx, dy) = (bx - ax, by - ay);
            let len2 = dx * dx + dy * dy;
            let (fx, fy) = if len2 > 0.0 {
                let mut t = ((lng - ax) * dx + (lat - ay) * dy) / len2;
                if t < 0.0 { t = 0.0; } else if t > 1.0 { t = 1.0; }
                (ax + t * dx, ay + t * dy)
            } else {
                (ax, ay)
            };
            let d = equirect_meters(lat, lng, fy, fx);
            if d < best {
                best = d;
                if best < 0.3 { return false; }
            }
        }
        true
    });
    best
}

// ---- schedule indexes ---------------------------------------------------

struct Indexes {
    full: HashSet<String>,   // loc|structure|number
    loose: HashSet<String>,  // loc|number
    names: HashSet<String>,  // composed "Loc Struct Num"
}

fn build_schedule_indexes(entries: &[ScheduleEntryRef]) -> Indexes {
    let mut ix = Indexes { full: HashSet::new(), loose: HashSet::new(), names: HashSet::new() };
    for e in entries {
        let loc = e.location.trim();
        let num = e.structure_number.trim();
        if loc.is_empty() || num.is_empty() {
            continue;
        }
        let st = e.structure.trim();
        ix.full.insert(format!("{loc}|{st}|{num}"));
        ix.loose.insert(format!("{loc}|{num}"));
        let composed = if st.is_empty() {
            format!("{loc} {num}")
        } else {
            format!("{loc} {st} {num}")
        };
        ix.names.insert(composed);
    }
    ix
}

fn str_prop(v: Option<&Value>) -> &str {
    v.and_then(|v| v.as_str()).unwrap_or("")
}

fn structure_matches_schedule(props: &serde_json::Map<String, Value>, ix: &Indexes) -> bool {
    let loc = str_prop(props.get("location")).trim();
    let num = str_prop(props.get("structure_number")).trim();
    if loc.is_empty() || num.is_empty() {
        return false;
    }
    let st = str_prop(props.get("structure")).trim();
    if ix.full.contains(&format!("{loc}|{st}|{num}")) {
        return true;
    }
    ix.loose.contains(&format!("{loc}|{num}"))
}

// ---- LineString bbox / proximity ----------------------------------------

fn extend_bbox_from_pts(b: &mut Bbox, pts: &[Value]) {
    for p in pts {
        let Some(c) = p.as_array() else { continue };
        if c.len() < 2 { continue; }
        let (Some(lng), Some(lat)) = (c[0].as_f64(), c[1].as_f64()) else { continue };
        b.extend(lat, lng);
    }
}

fn line_bbox(geom: &serde_json::Map<String, Value>) -> Option<Bbox> {
    let mut b = Bbox::default();
    let gtype = str_prop(geom.get("type"));
    if gtype == "MultiLineString" {
        if let Some(lines) = geom.get("coordinates").and_then(|v| v.as_array()) {
            for ln in lines {
                if let Some(pts) = ln.as_array() {
                    extend_bbox_from_pts(&mut b, pts);
                }
            }
        }
    } else {
        if let Some(pts) = geom.get("coordinates").and_then(|v| v.as_array()) {
            extend_bbox_from_pts(&mut b, pts);
        }
    }
    if b.valid { Some(b) } else { None }
}

fn scan_pts_near_path(pts: &[Value], idx: &PathIndex, prox_m: f64) -> bool {
    for p in pts {
        let Some(c) = p.as_array() else { continue };
        if c.len() < 2 { continue; }
        let (Some(lng), Some(lat)) = (c[0].as_f64(), c[1].as_f64()) else { continue };
        if min_distance_to_path_m(lat, lng, idx) <= prox_m {
            return true;
        }
    }
    false
}

fn line_near_path(geom: &serde_json::Map<String, Value>, idx: &PathIndex, prox_m: f64) -> bool {
    let gtype = str_prop(geom.get("type"));
    if gtype == "MultiLineString" {
        if let Some(lines) = geom.get("coordinates").and_then(|v| v.as_array()) {
            for ln in lines {
                if let Some(pts) = ln.as_array() {
                    if scan_pts_near_path(pts, idx, prox_m) {
                        return true;
                    }
                }
            }
        }
        return false;
    }
    geom.get("coordinates")
        .and_then(|v| v.as_array())
        .map(|pts| scan_pts_near_path(pts, idx, prox_m))
        .unwrap_or(false)
}

// ---- per-feature keep decision ------------------------------------------

fn keep_feature(
    feat: &Value,
    ix: &Indexes,
    path_idx: &PathIndex,
    cull_m: f64,
    has_path: bool,
    opts: &FilterOptions,
) -> bool {
    let Some(geom) = feat.get("geometry").and_then(|v| v.as_object()) else { return false };
    let gtype = str_prop(geom.get("type"));
    let empty = serde_json::Map::new();
    let props = feat.get("properties").and_then(|v| v.as_object()).unwrap_or(&empty);

    if gtype == "LineString" || gtype == "MultiLineString" {
        let feat_type = str_prop(props.get("feature_type"));
        if feat_type.is_empty() {
            return false; // untyped = merged-rails layer
        }
        if structure_matches_schedule(props, ix) {
            return true;
        }
        if !has_path {
            return false;
        }
        let Some(feat_bb) = line_bbox(geom) else { return false };
        if !feat_bb.intersects_inflated(&path_idx.bbox, cull_m) {
            return false;
        }
        return line_near_path(geom, path_idx, opts.marker_prox_m);
    }

    if gtype != "Point" {
        return false;
    }
    let Some(coords) = geom.get("coordinates").and_then(|v| v.as_array()) else { return false };
    if coords.len() < 2 {
        return false;
    }
    let (Some(flng), Some(flat)) = (coords[0].as_f64(), coords[1].as_f64()) else { return false };

    match str_prop(props.get("feature_kind")) {
        "car_stop_sign" => {
            let name = str_prop(props.get("platform_name"));
            !name.is_empty() && ix.names.contains(name)
        }
        "track_marker" => {
            if str_prop(props.get("marker_type")) != "Platform" {
                return false;
            }
            let name = str_prop(props.get("name"));
            !name.is_empty() && ix.names.contains(name)
        }
        "collectable" => {
            if !has_path { return false; }
            let bb = Bbox::point(flat, flng);
            if !bb.intersects_inflated(&path_idx.bbox, opts.collectable_prox_m) {
                return false;
            }
            min_distance_to_path_segments_m(flat, flng, path_idx) <= opts.collectable_prox_m
        }
        _ => {
            // Legacy / typeless point branch — same dispatch order as Go.
            let has_signal = match props.get("signal_id") {
                Some(Value::String(s)) => !s.is_empty(),
                Some(Value::Null) | None => false,
                Some(_) => true,
            };
            let has_jct = !matches!(props.get("jct_guid"), Some(Value::Null) | None);
            if has_signal {
                if !has_path { return false; }
                let bb = Bbox::point(flat, flng);
                if !bb.intersects_inflated(&path_idx.bbox, opts.signal_prox_m) {
                    return false;
                }
                return min_distance_to_path_segments_m(flat, flng, path_idx) <= opts.signal_prox_m;
            }
            if has_jct {
                if !has_path { return false; }
                let bb = Bbox::point(flat, flng);
                if !bb.intersects_inflated(&path_idx.bbox, opts.marker_prox_m) {
                    return false;
                }
                return min_distance_to_path_m(flat, flng, path_idx) <= opts.marker_prox_m;
            }
            // Legacy platform Point: schedule-tuple match.
            structure_matches_schedule(props, ix)
        }
    }
}

// ---- public entry point -------------------------------------------------

pub fn filter_route_features_for_timetable(
    route_features: &[Value],
    entries: &[ScheduleEntryRef],
    path_coords: Vec<ServiceCoord>,
    opts: &FilterOptions,
) -> Vec<Value> {
    let ix = build_schedule_indexes(entries);
    let has_path = !path_coords.is_empty();
    let path_idx = PathIndex::build(path_coords);
    let cull_m = opts.marker_prox_m.max(opts.signal_prox_m).max(opts.collectable_prox_m);

    let mut out = Vec::with_capacity(route_features.len());
    for feat in route_features {
        if keep_feature(feat, &ix, &path_idx, cull_m, has_path, opts) {
            out.push(feat.clone());
        }
    }
    out
}
