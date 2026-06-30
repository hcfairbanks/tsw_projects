//! Cooked-binary walker for NetworkRibbon + linked curve geometry from
//! a single TT_*.umap. Mirrors hud-go's `ribbon_cooked.go`. Outputs one
//! [`CookedRibbon`] per ribbon, with curve fields pre-resolved.
//!
//! World position priority (same as hud-go):
//!   1. `WorldLocation + StartPosition2D` — editor-verified recipe.
//!   2. `CachedStartPosition` (FarVector) — fallback when WorldLocation
//!      is absent (also the only source of Z height).
//!   3. `StartPosition2D` alone — last resort, may be tile-local.

use std::path::Path;

use crate::uasset::{self, Reader, Umap};

#[derive(Debug, Default, Clone, serde::Serialize)]
pub struct CookedRibbon {
    pub tile_name:        String,
    pub ribbon_name:      String,
    /// 32-char hex, lowercase, no separators.
    pub ribbon_guid:      String,
    pub start_node_guid:  String,
    pub end_node_guid:    String,
    /// `NetworkCurveCircularArc` / `NetworkCurveClothoidSpiral` / "".
    pub curve_class:      String,
    pub start_x:          f64,
    pub start_y:          f64,
    /// World-cm Z. 0 when no CachedStartPosition was present.
    pub start_z:          f64,
    pub tangent_x:        f64,
    pub tangent_y:        f64,
    pub length:           f64,
    /// 0 for straight chord or clothoid; >0 for circular arc.
    pub radius:           f64,
    pub has_radius:       bool,
    /// Human-readable name of the FObjectImport this ribbon's
    /// `TrackRule` ObjectProperty references (e.g.
    /// "BostonProvidenceTrackRule" vs "BostonProvidenceSubwayTrackRule").
    /// Empty when the ribbon ships no TrackRule.
    pub track_rule:       String,
}

#[derive(Default, Clone, Copy)]
struct CurveInfo {
    is_arc:      bool,
    is_clothoid: bool,
    sx: f64, sy: f64,
    tx: f64, ty: f64,
    length:     f64,
    radius:     f64,
    has_radius: bool,
}

pub fn parse_cooked_ribbons_from_umap(uasset_path: &Path, tile_name: &str) -> Result<Vec<CookedRibbon>, String> {
    let u = Umap::read(uasset_path)?;

    // Pass 1: walk every curve, store by 1-based export index
    // (FPackageIndex form). Index 0 = "not a curve" placeholder.
    let mut curves: Vec<Option<CurveInfo>> = vec![None; u.exports.len() + 1];
    for (i, e) in u.exports.iter().enumerate() {
        let is_arc = e.object_name.contains("NetworkCurveCircularArc")
            && !e.object_name.starts_with("Default__");
        let is_clothoid = is_clothoid_spiral(&e.object_name);
        if !is_arc && !is_clothoid { continue; }
        let Some(mut r) = u.property_reader(e) else { continue };
        let mut ci = CurveInfo { is_arc, is_clothoid, ..Default::default() };
        walk_curve_geometry(&mut r, &mut ci);
        curves[i + 1] = Some(ci);
    }

    // Pass 2: walk ribbons.
    let mut out: Vec<CookedRibbon> = Vec::new();
    for e in &u.exports {
        if !is_network_ribbon_export(&e.object_name) { continue; }
        let Some(mut r) = u.property_reader(e) else { continue };
        let mut rb = CookedRibbon::default();
        rb.tile_name   = tile_name.to_string();
        rb.ribbon_name = e.object_name.clone();
        let mut curve_idx: i32 = 0;
        let mut track_rule_idx: i32 = 0;
        let mut wl_x = 0.0f64; let mut wl_y = 0.0f64; let mut has_wl = false;
        let mut csp_x = 0.0f64; let mut csp_y = 0.0f64; let mut csp_z = 0.0f64; let mut has_csp = false;
        walk_ribbon_cooked_fields(
            &mut r, &mut rb,
            &mut curve_idx, &mut track_rule_idx,
            &mut wl_x, &mut wl_y, &mut has_wl,
            &mut csp_x, &mut csp_y, &mut csp_z, &mut has_csp,
        );
        rb.track_rule = u.resolve_fpackage_index(track_rule_idx);
        let c = if curve_idx > 0 && (curve_idx as usize) < curves.len() {
            curves[curve_idx as usize]
        } else { None };
        if let Some(c) = c {
            rb.curve_class = if c.is_arc { "NetworkCurveCircularArc".into() }
                             else if c.is_clothoid { "NetworkCurveClothoidSpiral".into() }
                             else { String::new() };
            rb.tangent_x = c.tx;
            rb.tangent_y = c.ty;
            rb.length    = c.length;
            rb.radius    = c.radius;
            rb.has_radius = c.has_radius;
        }
        // World-position priority — same triage as hud-go.
        let c = c.unwrap_or_default();
        if has_wl {
            rb.start_x = c.sx + wl_x;
            rb.start_y = c.sy + wl_y;
        } else if has_csp {
            rb.start_x = csp_x; rb.start_y = csp_y;
        } else {
            rb.start_x = c.sx; rb.start_y = c.sy;
        }
        if has_csp { rb.start_z = csp_z; }
        out.push(rb);
    }
    Ok(out)
}

/// Read a `NetworkCurveCircularArc` / `NetworkCurveClothoidSpiral`
/// export's tag stream and fill in the geometry fields. Mirrors hud-go's
/// `walkCurveGeometry`.
fn walk_curve_geometry(r: &mut Reader<'_>, ci: &mut CurveInfo) {
    while r.remaining() > 8 {
        let Some(t) = r.read_tag() else { return };
        let dp = r.tell();
        let next = dp + (t.size as usize);
        match (t.name.as_str(), t.ptype.as_str(), t.struct_type.as_str()) {
            ("StartPosition2D", "StructProperty", "Vector2D") => {
                ci.sx = r.f32() as f64; ci.sy = r.f32() as f64;
            }
            ("StartTangent2D",  "StructProperty", "Vector2D") => {
                ci.tx = r.f32() as f64; ci.ty = r.f32() as f64;
            }
            ("Length", "FloatProperty", _) => ci.length = r.f32() as f64,
            ("Radius", "FloatProperty", _) => { ci.radius = r.f32() as f64; ci.has_radius = true; }
            _ => {}
        }
        if next > r.data.len() { return; }
        r.seek(next);
    }
}

/// Read a NetworkRibbon's tag stream. Pulls the GUIDs, the Curve and
/// TrackRule FPackageIndices, plus `WorldLocation` (IntVector or
/// FVector2D — we read whichever variant the tag carries) and
/// `CachedStartPosition` (FarVector: Double X / Double Y / Float Z).
fn walk_ribbon_cooked_fields(
    r: &mut Reader<'_>,
    rb: &mut CookedRibbon,
    curve_idx:      &mut i32,
    track_rule_idx: &mut i32,
    wl_x: &mut f64, wl_y: &mut f64, has_wl: &mut bool,
    csp_x: &mut f64, csp_y: &mut f64, csp_z: &mut f64, has_csp: &mut bool,
) {
    while r.remaining() > 8 {
        let Some(t) = r.read_tag() else { return };
        let dp = r.tell();
        let next = dp + (t.size as usize);
        match (t.name.as_str(), t.ptype.as_str(), t.struct_type.as_str()) {
            ("RibbonGuid",   "StructProperty", "Guid") => rb.ribbon_guid     = uasset::normalize_guid(&r.guid_str()),
            ("StartNodeGuid","StructProperty", "Guid") => rb.start_node_guid = uasset::normalize_guid(&r.guid_str()),
            ("EndNodeGuid",  "StructProperty", "Guid") => rb.end_node_guid   = uasset::normalize_guid(&r.guid_str()),
            ("Curve",        "ObjectProperty", _)     => *curve_idx      = r.i32(),
            ("TrackRule",    "ObjectProperty", _)     => *track_rule_idx = r.i32(),
            ("WorldLocation","StructProperty", _) => {
                let end = next.min(r.data.len());
                let body = &r.data[dp..end];
                let mut sub = Reader::new(body, r.name_map);
                let (x, y, _z, ok) = read_tagged_xyz(&mut sub);
                if ok { *wl_x = x; *wl_y = y; *has_wl = true; }
            }
            ("CachedStartPosition","StructProperty", _) => {
                let end = next.min(r.data.len());
                let body = &r.data[dp..end];
                let mut sub = Reader::new(body, r.name_map);
                let (x, y, z, ok) = read_tagged_xyz(&mut sub);
                if ok { *csp_x = x; *csp_y = y; *csp_z = z; *has_csp = true; }
            }
            _ => {}
        }
        if next > r.data.len() { return; }
        r.seek(next);
    }
}

/// Walk a tagged sub-struct property stream and pull `X`, `Y`, and
/// optionally `Z`. Returns `(x, y, z, ok)` where `ok` is true when both
/// X and Y were read (Z is optional). Tolerates IntProperty,
/// FloatProperty, and DoubleProperty payloads for each axis.
fn read_tagged_xyz(r: &mut Reader<'_>) -> (f64, f64, f64, bool) {
    let mut x = 0.0f64; let mut y = 0.0f64; let mut z = 0.0f64;
    let mut got_x = false; let mut got_y = false;
    while r.remaining() >= 24 {
        let Some(t) = r.read_tag() else { break };
        let dp = r.tell();
        let next = dp + (t.size as usize);
        match t.name.as_str() {
            "X" => match t.ptype.as_str() {
                "IntProperty"    => { x = r.i32() as f64; got_x = true; }
                "FloatProperty"  => { x = r.f32() as f64; got_x = true; }
                "DoubleProperty" => { x = r.f64();        got_x = true; }
                _ => {}
            },
            "Y" => match t.ptype.as_str() {
                "IntProperty"    => { y = r.i32() as f64; got_y = true; }
                "FloatProperty"  => { y = r.f32() as f64; got_y = true; }
                "DoubleProperty" => { y = r.f64();        got_y = true; }
                _ => {}
            },
            "Z" => match t.ptype.as_str() {
                "IntProperty"    => z = r.i32() as f64,
                "FloatProperty"  => z = r.f32() as f64,
                "DoubleProperty" => z = r.f64(),
                _ => {}
            },
            _ => {}
        }
        if next > r.data.len() { break; }
        r.seek(next);
    }
    (x, y, z, got_x && got_y)
}

fn is_clothoid_spiral(object_name: &str) -> bool {
    object_name.contains("NetworkCurveClothoidSpiral") && !object_name.starts_with("Default__")
}

fn is_network_ribbon_export(object_name: &str) -> bool {
    if object_name.starts_with("Default__") { return false; }
    if object_name == "NetworkRibbon" { return true; }
    object_name.starts_with("NetworkRibbon_")
}
