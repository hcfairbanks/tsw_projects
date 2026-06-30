//! DataTrack-driven per-service polyline builder. Walks one service's
//! ribbon breadcrumbs (parsed from `RouteTimetableDataTrack.uasset`) and
//! splices each consecutive (ribbon, fraction) pair against the rails
//! builder's per-ribbon vertex map. Output is the [lat, lng] list that
//! ships into `timetable_coordinates.coordinates`.
//!
//! Pragmatic subset of hud-go's `output/service_path.go`:
//!   * Uses the DataTrack breadcrumbs directly — no ribbon-graph
//!     Dijkstra. Where the DataTrack is silent (missing service / empty
//!     events), the service polyline is omitted (caller decides whether
//!     to record a `coord_source` of "none").
//!   * Falls back to the analytical ribbon point only when the rails
//!     builder didn't emit a polyline for that ribbon — the rare case
//!     where length==0 or tangent==0 filtered it out. We don't bother
//!     with a Break flag here because hud's renderers treat consecutive
//!     same-ribbon segments as one continuous line; we just emit the
//!     points we have.
//!
//! This is functionally what hud-go's pre-baked DataTrack short-circuit
//! does after the rails layer is in place (see `samplePathBetween` →
//! "When ServiceTrackData is set, slice directly from per-ribbon
//! polylines"); only the cross-ribbon Dijkstra fallback is missing,
//! which only fires for services whose DataTrack didn't ship.

use std::collections::HashMap;

use crate::uasset_datatrack::TrackDataEvent;

#[derive(Debug, Default, Clone, serde::Serialize)]
pub struct ServiceCoord {
    pub latitude:  f64,
    pub longitude: f64,
}

/// Build the per-service polyline from DataTrack breadcrumbs.
///
/// `per_ribbon` is the rails-builder's normalised-GUID → polyline map
/// (each polyline is `[lng, lat]` pairs sampled at SAMPLE_STEP_CM).
/// Returns an empty vec when no breadcrumbs resolved to a known ribbon.
pub fn build_service_path(
    track_data: &[TrackDataEvent],
    per_ribbon: &HashMap<String, Vec<[f64; 2]>>,
) -> Vec<ServiceCoord> {
    if track_data.is_empty() { return Vec::new(); }

    let mut out: Vec<ServiceCoord> = Vec::with_capacity(track_data.len() * 4);

    let mut prev_ribbon: Option<&str> = None;
    let mut prev_frac:   f32 = 0.0;

    for ev in track_data {
        let ribbon = ev.ribbon_guid.as_str();
        if ribbon.is_empty() { continue; }
        let Some(verts) = per_ribbon.get(ribbon) else {
            // Ribbon isn't in the rails map (length==0 / tangent==0 /
            // filtered). Skip — the next event we can resolve will
            // continue the polyline. hud-go's path-builder would
            // analytical-compute here; we keep this minimal.
            continue;
        };
        if verts.len() < 2 { continue; }

        let same_ribbon = prev_ribbon.map(|p| p == ribbon).unwrap_or(false);
        let cur_frac = ev.ribbon_location.clamp(0.0, 1.0);

        if !same_ribbon {
            // Cross-ribbon transition: jump to this ribbon's current
            // fraction. Push the seed point so the next splice has
            // somewhere to grow from.
            push_vert_at(&mut out, verts, cur_frac);
        } else {
            // Same ribbon as the previous breadcrumb: splice from
            // `prev_frac` to `cur_frac` along this ribbon's polyline.
            // Walking direction matters — events come in chronological
            // order, but the train may travel either forward (frac↑) or
            // reverse (frac↓) along a ribbon.
            slice_into(&mut out, verts, prev_frac, cur_frac);
        }

        prev_ribbon = Some(ribbon);
        prev_frac   = cur_frac;
    }

    out
}

/// Encode `out` as the legacy JSON shape hud-go's importer expects in
/// `timetable_coordinates.coordinates`: a flat array of
/// `[lng, lat]` pairs. Produces an empty `"[]"` for an empty list.
pub fn encode_polyline(out: &[ServiceCoord]) -> String {
    let mut s = String::with_capacity(out.len() * 28 + 2);
    s.push('[');
    for (i, c) in out.iter().enumerate() {
        if i > 0 { s.push(','); }
        s.push('[');
        s.push_str(&format!("{:.7}", c.longitude));
        s.push(',');
        s.push_str(&format!("{:.7}", c.latitude));
        s.push(']');
    }
    s.push(']');
    s
}

/// Thin a polyline to ~`min_dist_m` spacing (keeps first + last point).
/// Equirectangular distance. Mirrors zip_writer::decimate_coords_meters /
/// hud-go's DecimateCoordsMeters — applied before storing so the
/// timetable_coordinates blob isn't ~5x oversampled by the 1 m rails-vertex
/// sampler (no visible map difference at 5 m).
pub fn decimate_meters(coords: &[ServiceCoord], min_dist_m: f64) -> Vec<ServiceCoord> {
    if coords.len() < 3 || min_dist_m <= 0.0 {
        return coords.to_vec();
    }
    let mut out: Vec<ServiceCoord> = Vec::with_capacity(coords.len());
    out.push(coords[0].clone());
    let mut last = coords[0].clone();
    for c in &coords[1..coords.len() - 1] {
        if coord_dist_m(&last, c) >= min_dist_m {
            out.push(c.clone());
            last = c.clone();
        }
    }
    out.push(coords[coords.len() - 1].clone());
    out
}

fn coord_dist_m(a: &ServiceCoord, b: &ServiceCoord) -> f64 {
    const R: f64 = 6_371_000.0;
    let d_lat = (b.latitude - a.latitude).to_radians();
    let d_lng = (b.longitude - a.longitude).to_radians();
    let mean_lat = ((a.latitude + b.latitude) * 0.5).to_radians();
    let x = d_lng * mean_lat.cos();
    ((d_lat * d_lat) + (x * x)).sqrt() * R
}

fn push_vert_at(out: &mut Vec<ServiceCoord>, verts: &[[f64; 2]], frac: f32) {
    let i = nearest_vertex_index(verts.len(), frac);
    let v = verts[i];
    push_unique(out, v);
}

fn slice_into(out: &mut Vec<ServiceCoord>, verts: &[[f64; 2]], prev_frac: f32, cur_frac: f32) {
    if verts.len() < 2 { return; }
    let n = verts.len() - 1;
    let i0 = nearest_vertex_index(verts.len(), prev_frac);
    let i1 = nearest_vertex_index(verts.len(), cur_frac);
    if i0 == i1 {
        push_unique(out, verts[i1]);
        return;
    }
    if i0 < i1 {
        for i in i0..=i1 {
            push_unique(out, verts[i]);
        }
    } else {
        let mut i = i0;
        loop {
            push_unique(out, verts[i]);
            if i == i1 { break; }
            if i == 0 { break; }
            i -= 1;
        }
    }
    let _ = n;
}

fn nearest_vertex_index(len: usize, frac: f32) -> usize {
    if len == 0 { return 0; }
    let n = (len - 1) as f32;
    let f = frac.clamp(0.0, 1.0);
    let i = (f * n).round() as i64;
    i.clamp(0, n as i64) as usize
}

#[inline]
fn push_unique(out: &mut Vec<ServiceCoord>, v: [f64; 2]) {
    // Drop the duplicate when we splice across a shared vertex (every
    // segment after the first shares its first vertex with the previous
    // segment's last). Comparing to `last` is enough; coords come from
    // the same rounded source so float equality is reliable.
    if let Some(last) = out.last() {
        if last.longitude == v[0] && last.latitude == v[1] { return; }
    }
    out.push(ServiceCoord { longitude: v[0], latitude: v[1] });
}
