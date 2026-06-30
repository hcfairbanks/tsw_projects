//! Clothoid spiral parameters + integrator. Mirrors hud-go's
//! `clothoid.go`. Used by the cookedmap rails builder to render the
//! "transition" curves between straights and circular arcs without
//! resorting to Hermite approximation.
//!
//! A clothoid is fully described by a single parameter A:
//!   * curvature(s) = s / A²
//!   * heading(s)   = θ₀ + s² / (2 A²)
//!   * position(s)  = StartPos + ∫₀ˢ (cos(heading), sin(heading)) ds'
//!
//! The integral is a Fresnel integral (no closed form). We sample it
//! with the trapezoidal rule at a fine step — accurate to micro-metres
//! for typical TSW track curvatures.

use std::collections::HashMap;
use std::path::Path;

use crate::uasset::{self, Reader, Umap};

#[derive(Debug, Default, Clone, Copy)]
pub struct ClothoidParams {
    /// = PowersOfA[0]; clothoid parameter (signed; sign = curve direction).
    pub a:              f64,
    /// = ClothoidLength (cm).
    pub l:              f64,
    pub radial_offset:  f64,
    pub start_x:        f64,
    pub start_y:        f64,
    pub start_tan_x:    f64,
    pub start_tan_y:    f64,
}

/// Walk one TT-tile umap and return a map of ribbon-GUID →
/// [`ClothoidParams`] for every ribbon whose Curve references a
/// `NetworkCurveClothoidSpiral`. Ribbons backed by circular arcs are
/// simply absent from the result.
pub fn parse_clothoids_by_ribbon_guid(uasset_path: &Path) -> Result<HashMap<String, ClothoidParams>, String> {
    let u = Umap::read(uasset_path)?;

    // Pass 1: index clothoid spirals by 1-based FPackageIndex.
    let mut clothoid_by_idx: Vec<Option<ClothoidParams>> = vec![None; u.exports.len() + 1];
    for (i, e) in u.exports.iter().enumerate() {
        if !is_clothoid_spiral(&e.object_name) { continue; }
        let Some(mut r) = u.property_reader(e) else { continue };
        if let Some(p) = walk_clothoid_spiral(&mut r) {
            clothoid_by_idx[i + 1] = Some(p);
        }
    }

    // Pass 2: walk ribbons, link via Curve FPackageIndex.
    let mut out: HashMap<String, ClothoidParams> = HashMap::new();
    for e in &u.exports {
        if !is_network_ribbon_export(&e.object_name) { continue; }
        let Some(mut r) = u.property_reader(e) else { continue };
        let (guid, curve_idx) = walk_ribbon_for_guid_and_curve(&mut r);
        if guid.is_empty() || curve_idx <= 0 { continue; }
        if (curve_idx as usize) >= clothoid_by_idx.len() { continue; }
        if let Some(p) = clothoid_by_idx[curve_idx as usize] {
            out.insert(guid, p);
        }
    }
    Ok(out)
}

fn is_clothoid_spiral(name: &str) -> bool {
    if name.starts_with("Default__") { return false; }
    name.contains("NetworkCurveClothoidSpiral")
}

fn is_network_ribbon_export(name: &str) -> bool {
    if name.starts_with("Default__") { return false; }
    if name == "NetworkRibbon" { return true; }
    name.starts_with("NetworkRibbon_")
}

fn walk_ribbon_for_guid_and_curve(r: &mut Reader<'_>) -> (String, i32) {
    let mut guid = String::new();
    let mut curve_idx: i32 = 0;
    while r.remaining() > 8 {
        let Some(t) = r.read_tag() else { break };
        let dp = r.tell();
        let next = dp + (t.size as usize);
        match (t.name.as_str(), t.ptype.as_str(), t.struct_type.as_str()) {
            ("RibbonGuid", "StructProperty", "Guid") => guid = uasset::normalize_guid(&r.guid_str()),
            ("Curve",      "ObjectProperty", _)     => curve_idx = r.i32(),
            _ => {}
        }
        if next > r.data.len() { break; }
        r.seek(next);
    }
    (guid, curve_idx)
}

fn walk_clothoid_spiral(r: &mut Reader<'_>) -> Option<ClothoidParams> {
    let mut p = ClothoidParams::default();
    let mut found = false;
    while r.remaining() > 8 {
        let Some(t) = r.read_tag() else { break };
        let dp = r.tell();
        let next = dp + (t.size as usize);
        match (t.name.as_str(), t.ptype.as_str(), t.struct_type.as_str()) {
            ("Spiral", "StructProperty", "SpiralTransition") => {
                walk_spiral_transition(r, dp, t.size as usize, &mut p);
                found = true;
            }
            ("StartPosition2D", "StructProperty", "Vector2D") => {
                p.start_x = r.f32() as f64;
                p.start_y = r.f32() as f64;
            }
            ("StartTangent2D", "StructProperty", "Vector2D") => {
                p.start_tan_x = r.f32() as f64;
                p.start_tan_y = r.f32() as f64;
            }
            ("Length", "FloatProperty", _) => {
                // Prefer ClothoidLength from the inner Spiral when both
                // are present; ignore top-level if we already have one.
                if p.l == 0.0 { p.l = r.f32() as f64; }
            }
            _ => {}
        }
        if next > r.data.len() { break; }
        r.seek(next);
    }
    if found { Some(p) } else { None }
}

fn walk_spiral_transition(r: &mut Reader<'_>, dp: usize, size: usize, p: &mut ClothoidParams) {
    let end = dp + size;
    while r.tell() < end {
        let Some(t) = r.read_tag() else { break };
        let tdp = r.tell();
        let tnext = tdp + (t.size as usize);
        match (t.name.as_str(), t.ptype.as_str(), t.struct_type.as_str()) {
            ("BaseClothoid",   "StructProperty", "Clothoid") => walk_clothoid(r, tdp, t.size as usize, p),
            ("ClothoidLength", "DoubleProperty", _)          => p.l = r.f64(),
            ("RadialOffset",   "DoubleProperty", _)          => p.radial_offset = r.f64(),
            _ => {}
        }
        if tnext > r.data.len() { break; }
        r.seek(tnext);
    }
    if end <= r.data.len() { r.seek(end); }
}

fn walk_clothoid(r: &mut Reader<'_>, dp: usize, size: usize, p: &mut ClothoidParams) {
    let end = dp + size;
    while r.tell() < end {
        let Some(t) = r.read_tag() else { break };
        let tdp = r.tell();
        let tnext = tdp + (t.size as usize);
        if t.name == "PowersOfA" && t.ptype == "DoubleProperty" && p.a == 0.0 {
            // UE static-array layout: each element is its own FPropertyTag.
            // Capture only A^1 (first occurrence) which is A itself.
            p.a = r.f64();
        }
        if tnext > r.data.len() { break; }
        r.seek(tnext);
    }
    if end <= r.data.len() { r.seek(end); }
}

/// Sample a clothoid as a polyline of N+1 points over arc length [0,L].
/// Returns `(xs, ys)` in cm, same coordinate frame as `start_x/start_y`.
/// Trapezoidal Fresnel integral — accurate to micro-metres for typical
/// track curvatures.
pub fn integrate_clothoid(p: ClothoidParams, n: usize) -> (Vec<f64>, Vec<f64>) {
    let n = n.max(2);
    let start_theta = p.start_tan_y.atan2(p.start_tan_x);
    let mut xs = vec![0.0_f64; n + 1];
    let mut ys = vec![0.0_f64; n + 1];
    xs[0] = p.start_x;
    ys[0] = p.start_y;
    if p.radial_offset != 0.0 {
        // Shift start perpendicular to tangent (left-positive).
        let nx = -p.start_tan_y;
        let ny = p.start_tan_x;
        xs[0] += p.radial_offset * nx;
        ys[0] += p.radial_offset * ny;
    }
    let a2 = p.a * p.a;
    if a2 == 0.0 { return (xs, ys); }
    let ds = p.l / (n as f64);
    let mut prev_cos = start_theta.cos();
    let mut prev_sin = start_theta.sin();
    for i in 1..=n {
        let s = (i as f64) * ds;
        let theta = start_theta + s * s / (2.0 * a2);
        let c = theta.cos();
        let sg = theta.sin();
        xs[i] = xs[i - 1] + ds * 0.5 * (prev_cos + c);
        ys[i] = ys[i - 1] + ds * 0.5 * (prev_sin + sg);
        prev_cos = c;
        prev_sin = sg;
    }
    (xs, ys)
}
