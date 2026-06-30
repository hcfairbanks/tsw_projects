//! Cooked-binary walker for trackside features inside a TT_*.umap:
//! NetworkTurnoutJunction (switches), TrackMarkerProperty (route
//! markers), CarStopSignProperty, Signal references, and the
//! LinkedPlatforms array on station blueprints. Plus a separate entry
//! point [`parse_cooked_collectables_from_umap`] for the in-world
//! Mastery collectable spawn points.
//!
//! Mirrors hud-go's `features_cooked.go`. Output shapes match hud-go's
//! `Signal`/`Switch`/`CarStopSign`/`RouteMarker`/`LinkedPlatform`
//! records so the downstream GeoJSON writers can consume them
//! unchanged once they're ported.

use std::path::Path;

use crate::uasset::{self, Reader, Umap};

#[derive(Debug, Default, Clone, serde::Serialize)]
pub struct CookedFeatures {
    pub platforms:     Vec<LinkedPlatform>,
    pub signals:       Vec<Signal>,
    pub switches:      Vec<Switch>,
    pub car_stop_signs: Vec<CarStopSign>,
    pub route_markers: Vec<RouteMarker>,
}

#[derive(Debug, Default, Clone, serde::Serialize)]
pub struct LinkedPlatform {
    pub name:            String,
    pub ribbon_guid:     String,
    pub ribbon_location: f32,
    pub tile_name:       String,
}

#[derive(Debug, Default, Clone, serde::Serialize)]
pub struct Signal {
    pub signal_id:         String,
    pub signal_type:       String,
    pub ribbon_guid:       String,
    pub location_fraction: f32,
    pub tile_name:         String,
}

#[derive(Debug, Default, Clone, serde::Serialize)]
pub struct Switch {
    pub jct_guid:            String,
    pub node_guid:           String,
    pub ingoing_ribbon:      String,
    pub outgoing_ribbon:     String,
    pub turnout_ribbon:      String,
    pub manually_controlled: bool,
    pub tile_name:           String,
}

#[derive(Debug, Default, Clone, serde::Serialize)]
pub struct CarStopSign {
    pub ribbon_guid:               String,
    pub location:                  f32,
    pub max_number_of_rail_vehicles: i32,
    pub tile_name:                 String,
}

#[derive(Debug, Default, Clone, serde::Serialize)]
pub struct RouteMarker {
    pub ribbon_guid: String,
    pub name:        String,
    pub marker_type: String,
    pub line_side:   String,
    pub location:    f32,
    pub start:       f32,
    pub end:         f32,
    pub tile_name:   String,
}

#[derive(Debug, Default, Clone, serde::Serialize)]
pub struct CookedCollectable {
    pub tile_name:      String,
    pub actor_class:    String,
    pub instance_name:  String,
    pub collectable_id: String,
    pub x: f64, pub y: f64, pub z: f64,
}

pub fn parse_cooked_features_from_umap(uasset_path: &Path, tile_name: &str) -> Result<CookedFeatures, String> {
    let u = Umap::read(uasset_path)?;
    let mut out = CookedFeatures::default();

    // Build ribbon-export-index → ribbon-GUID map (1-based FPackageIndex).
    let mut ribbon_guid_by_idx: Vec<Option<String>> = vec![None; u.exports.len() + 1];
    for (i, e) in u.exports.iter().enumerate() {
        if !is_network_ribbon_export(&e.object_name) { continue; }
        let Some(mut r) = u.property_reader(e) else { continue };
        let guid = walk_ribbon_for_guid(&mut r);
        if !guid.is_empty() { ribbon_guid_by_idx[i + 1] = Some(guid); }
    }

    for e in &u.exports {
        let name = &e.object_name;
        if name.starts_with("NetworkTurnoutJunction") && !name.starts_with("Default__") {
            let Some(mut r) = u.property_reader(e) else { continue };
            let mut s = Switch { tile_name: tile_name.to_string(), ..Default::default() };
            walk_network_turnout_junction(&mut r, &mut s);
            if !s.jct_guid.is_empty() || !s.node_guid.is_empty() {
                out.switches.push(s);
            }
        } else if name.starts_with("CarStopSignProperty") || name.starts_with("Default__CarStopSignProperty") {
            let parent_idx = e.outer_index;
            let parent_guid = if parent_idx > 0 && (parent_idx as usize) < ribbon_guid_by_idx.len() {
                ribbon_guid_by_idx[parent_idx as usize].clone()
            } else { None };
            let Some(parent_guid) = parent_guid else { continue };
            let Some(mut r) = u.property_reader(e) else { continue };
            let mut c = CarStopSign {
                ribbon_guid: parent_guid,
                tile_name: tile_name.to_string(),
                ..Default::default()
            };
            walk_car_stop_sign_property(&mut r, &mut c);
            out.car_stop_signs.push(c);
        } else if name.starts_with("TrackMarkerProperty") || name.starts_with("Default__TrackMarkerProperty") {
            let parent_idx = e.outer_index;
            let parent_guid = if parent_idx > 0 && (parent_idx as usize) < ribbon_guid_by_idx.len() {
                ribbon_guid_by_idx[parent_idx as usize].clone()
            } else { None };
            let Some(parent_guid) = parent_guid else { continue };
            let Some(mut r) = u.property_reader(e) else { continue };
            let mut m = RouteMarker {
                ribbon_guid: parent_guid,
                tile_name: tile_name.to_string(),
                ..Default::default()
            };
            walk_track_marker_property(&mut r, &mut m);
            if !m.name.is_empty() { out.route_markers.push(m); }
        }
    }

    // Platforms.
    for e in &u.exports {
        let Some(mut r) = u.property_reader(e) else { continue };
        walk_for_linked_platforms(&mut r, tile_name, &mut out.platforms);
    }

    // Signals — two-step: cache (id,type,location) per signal-shaped export,
    // then resolve ribbon-side references.
    let mut signal_cache: Vec<Option<SigInfo>> = vec![None; u.exports.len() + 1];
    for (i, e) in u.exports.iter().enumerate() {
        if e.object_name.starts_with("Default__") { continue; }
        let Some(mut r) = u.property_reader(e) else { continue };
        if let Some(sig) = walk_for_signal_fields(&mut r) {
            signal_cache[i + 1] = Some(sig);
        }
    }
    let any_signals = signal_cache.iter().any(|s| s.is_some());
    if any_signals {
        let mut seen_ids: std::collections::HashSet<String> = std::collections::HashSet::new();
        for (i, e) in u.exports.iter().enumerate() {
            if !is_network_ribbon_export(&e.object_name) { continue; }
            let Some(ribbon_guid) = ribbon_guid_by_idx.get(i + 1).cloned().flatten() else { continue };
            let Some(mut r) = u.property_reader(e) else { continue };
            for ref_idx in walk_ribbon_network_property_refs(&mut r) {
                if ref_idx <= 0 || (ref_idx as usize) >= signal_cache.len() { continue; }
                let Some(info) = signal_cache[ref_idx as usize].clone() else { continue };
                if seen_ids.contains(&info.id) { continue; }
                seen_ids.insert(info.id.clone());
                out.signals.push(Signal {
                    signal_id:         info.id,
                    signal_type:       info.sig_type,
                    ribbon_guid:       ribbon_guid.clone(),
                    location_fraction: info.location,
                    tile_name:         tile_name.to_string(),
                });
            }
        }
    }

    Ok(out)
}

/// Walk one umap (typically `ST_*.umap`) and emit every CollectableID-
/// carrying actor as a [`CookedCollectable`]. Positions are tile-local
/// cm — caller must add the tile world offset before projecting.
pub fn parse_cooked_collectables_from_umap(uasset_path: &Path, tile_name: &str) -> Result<Vec<CookedCollectable>, String> {
    let u = Umap::read(uasset_path)?;
    #[derive(Default, Clone)]
    struct Cand { idx: i32, guid: String, root_idx: i32, actor_name: String }
    let mut cands: Vec<Cand> = Vec::new();
    for (i, e) in u.exports.iter().enumerate() {
        if e.object_name.starts_with("Default__") { continue; }
        let Some(mut r) = u.property_reader(e) else { continue };
        if let Some((guid, root_idx)) = walk_actor_for_collectable(&mut r) {
            cands.push(Cand {
                idx: (i + 1) as i32,
                guid,
                root_idx,
                actor_name: e.object_name.clone(),
            });
        }
    }
    if cands.is_empty() { return Ok(Vec::new()); }
    let mut out: Vec<CookedCollectable> = Vec::with_capacity(cands.len());
    for c in cands {
        let mut pos: Option<(f64, f64, f64)> = None;
        if c.root_idx > 0 && (c.root_idx as usize) <= u.exports.len() {
            let root_exp = &u.exports[(c.root_idx - 1) as usize];
            if let Some(mut rpr) = u.property_reader(root_exp) {
                if let Some(p) = walk_scene_comp_for_location(&mut rpr) { pos = Some(p); }
            }
        }
        if pos.is_none() {
            if let Some(mut pr) = u.property_reader(&u.exports[(c.idx - 1) as usize]) {
                if let Some(p) = walk_scene_comp_for_location(&mut pr) { pos = Some(p); }
            }
        }
        let Some((x, y, z)) = pos else { continue };
        out.push(CookedCollectable {
            tile_name: tile_name.to_string(),
            actor_class: actor_class_from_name(&c.actor_name),
            instance_name: c.actor_name,
            collectable_id: c.guid,
            x, y, z,
        });
    }
    Ok(out)
}

// =============================================================== inner walkers

fn is_network_ribbon_export(name: &str) -> bool {
    if name.starts_with("Default__") { return false; }
    if name == "NetworkRibbon" { return true; }
    name.starts_with("NetworkRibbon_")
}

/// Walk a NetworkRibbon export's property stream and return its RibbonGuid.
/// Strips the rest. Mirrors hud-go's `walkRibbonForGuidAndCurve` minus the
/// curve-index (we don't need it here).
fn walk_ribbon_for_guid(r: &mut Reader<'_>) -> String {
    while r.remaining() > 8 {
        let Some(t) = r.read_tag() else { return String::new(); };
        let dp = r.tell();
        let next = dp + (t.size as usize);
        if t.name == "RibbonGuid" && t.ptype == "StructProperty" && t.struct_type == "Guid" {
            return uasset::normalize_guid(&r.guid_str());
        }
        if next > r.data.len() { return String::new(); }
        r.seek(next);
    }
    String::new()
}

fn walk_network_turnout_junction(r: &mut Reader<'_>, s: &mut Switch) {
    while r.remaining() > 8 {
        let Some(t) = r.read_tag() else { return };
        let dp = r.tell();
        let next = dp + (t.size as usize);
        match (t.name.as_str(), t.ptype.as_str(), t.struct_type.as_str()) {
            ("JctGuid",                 "StructProperty", "Guid") => s.jct_guid  = uasset::normalize_guid(&r.guid_str()),
            ("NodeGuid",                "StructProperty", "Guid") => s.node_guid = uasset::normalize_guid(&r.guid_str()),
            ("IngoingRibbonConnection", "StructProperty", _) => {
                let body = &r.data[dp..next.min(r.data.len())];
                let mut sub = Reader::new(body, r.name_map);
                s.ingoing_ribbon = read_connection_ribbon_guid(&mut sub);
            }
            ("OutgoingRibbonConnection","StructProperty", _) => {
                let body = &r.data[dp..next.min(r.data.len())];
                let mut sub = Reader::new(body, r.name_map);
                s.outgoing_ribbon = read_connection_ribbon_guid(&mut sub);
            }
            ("TurnoutRibbonConnection", "StructProperty", _) => {
                let body = &r.data[dp..next.min(r.data.len())];
                let mut sub = Reader::new(body, r.name_map);
                s.turnout_ribbon = read_connection_ribbon_guid(&mut sub);
            }
            ("bManuallyControlled",     "BoolProperty",   _) => s.manually_controlled = t.bool_val != 0,
            _ => {}
        }
        if next > r.data.len() { return; }
        r.seek(next);
    }
}

fn read_connection_ribbon_guid(r: &mut Reader<'_>) -> String {
    while r.remaining() >= 24 {
        let Some(t) = r.read_tag() else { return String::new(); };
        let dp = r.tell();
        let next = dp + (t.size as usize);
        if t.name == "RibbonGuid" && t.ptype == "StructProperty" && t.struct_type == "Guid" {
            return uasset::normalize_guid(&r.guid_str());
        }
        if next > r.data.len() { return String::new(); }
        r.seek(next);
    }
    String::new()
}

fn walk_car_stop_sign_property(r: &mut Reader<'_>, c: &mut CarStopSign) {
    while r.remaining() > 8 {
        let Some(t) = r.read_tag() else { return };
        let dp = r.tell();
        let next = dp + (t.size as usize);
        match (t.name.as_str(), t.ptype.as_str()) {
            ("Location",                 "FloatProperty") => c.location = r.f32(),
            ("MaxNumberOfRailVehicles",  "IntProperty")   => c.max_number_of_rail_vehicles = r.i32(),
            _ => {}
        }
        if next > r.data.len() { return; }
        r.seek(next);
    }
}

fn walk_track_marker_property(r: &mut Reader<'_>, m: &mut RouteMarker) {
    let mut display_name = String::new();
    let mut marker_name  = String::new();
    while r.remaining() > 8 {
        let Some(t) = r.read_tag() else { break };
        let dp = r.tell();
        let next = dp + (t.size as usize);
        match t.ptype.as_str() {
            "FloatProperty" => match t.name.as_str() {
                "Location" => m.location = r.f32(),
                "Start"    => m.start    = r.f32(),
                "End"      => m.end      = r.f32(),
                _ => {}
            },
            "StrProperty" => {
                if t.name == "MarkerName" { marker_name = r.fstr(); }
            }
            "TextProperty" => {
                if t.name == "DisplayName" {
                    display_name = r.ftext(t.size as usize);
                } else if t.name == "MarkerName" {
                    let v = r.ftext(t.size as usize);
                    if !v.is_empty() { marker_name = v; }
                }
            }
            "NameProperty" => match t.name.as_str() {
                "MarkerName" => marker_name = r.fname(),
                "MarkerType" => m.marker_type = last_segment_after(&r.fname(), "::"),
                "LineSide"   => m.line_side   = last_segment_after(&r.fname(), "::"),
                _ => {}
            },
            "ByteProperty" | "EnumProperty" => match t.name.as_str() {
                "MarkerType" => m.marker_type = last_segment_after(&r.fname(), "::"),
                "LineSide"   => m.line_side   = last_segment_after(&r.fname(), "::"),
                _ => {}
            },
            _ => {}
        }
        if next > r.data.len() { break; }
        r.seek(next);
    }
    if !display_name.is_empty() { m.name = display_name; } else { m.name = marker_name; }
}

fn walk_for_linked_platforms(r: &mut Reader<'_>, tile_name: &str, out: &mut Vec<LinkedPlatform>) {
    while r.remaining() > 8 {
        let Some(t) = r.read_tag() else { return };
        let dp = r.tell();
        let next = dp + (t.size as usize);
        if t.name == "LinkedPlatforms" && t.ptype == "ArrayProperty" {
            let body = &r.data[dp..next.min(r.data.len())];
            let mut sub = Reader::new(body, r.name_map);
            parse_linked_platforms_array(&mut sub, tile_name, out);
        }
        if next > r.data.len() { return; }
        r.seek(next);
    }
}

fn parse_linked_platforms_array(r: &mut Reader<'_>, tile_name: &str, out: &mut Vec<LinkedPlatform>) {
    let Some(inner) = r.read_tag() else { return };
    if inner.size <= 0 { return; }
    if r.remaining() < 4 { return; }
    let count = r.i32();
    if count < 0 || count > 10_000 { return; }
    for _ in 0..count {
        if r.remaining() < 8 { break; }
        let mut lp = LinkedPlatform { tile_name: tile_name.to_string(), ..Default::default() };
        read_route_location_entry(r, &mut lp);
        if !lp.name.is_empty() && !lp.ribbon_guid.is_empty() {
            out.push(lp);
        }
    }
}

fn read_route_location_entry(r: &mut Reader<'_>, lp: &mut LinkedPlatform) {
    while r.remaining() > 8 {
        let Some(t) = r.read_tag() else { return };
        let dp = r.tell();
        let next = dp + (t.size as usize);
        match (t.name.as_str(), t.ptype.as_str()) {
            ("Name",     "StrProperty")    => lp.name = r.fstr(),
            ("Location", "StructProperty") => {
                let body = &r.data[dp..next.min(r.data.len())];
                let mut sub = Reader::new(body, r.name_map);
                read_ribbon_location(&mut sub, lp);
            }
            _ => {}
        }
        if next > r.data.len() { return; }
        r.seek(next);
    }
}

fn read_ribbon_location(r: &mut Reader<'_>, lp: &mut LinkedPlatform) {
    while r.remaining() > 8 {
        let Some(t) = r.read_tag() else { return };
        let dp = r.tell();
        let next = dp + (t.size as usize);
        match (t.name.as_str(), t.ptype.as_str(), t.struct_type.as_str()) {
            ("RibbonReference", "StructProperty", "Guid") => lp.ribbon_guid     = uasset::normalize_guid(&r.guid_str()),
            ("RibbonLocation",  "FloatProperty",  _)      => lp.ribbon_location = r.f32(),
            _ => {}
        }
        if next > r.data.len() { return; }
        r.seek(next);
    }
}

fn walk_for_signal_fields(r: &mut Reader<'_>) -> Option<SigInfo> {
    let mut id = String::new();
    let mut sig_type = String::new();
    let mut location = 0.0f32;
    let mut has_id = false; let mut has_loc = false;
    while r.remaining() > 8 {
        let Some(t) = r.read_tag() else { break };
        let dp = r.tell();
        let next = dp + (t.size as usize);
        match (t.name.as_str(), t.ptype.as_str()) {
            ("SignalID",   "StrProperty")  => { id = r.fstr(); has_id = !id.is_empty(); }
            ("SignalType", "ByteProperty" | "EnumProperty" | "NameProperty") => {
                sig_type = last_segment_after(&r.fname(), "::");
            }
            ("Location",   "FloatProperty") => { location = r.f32(); has_loc = true; }
            _ => {}
        }
        if next > r.data.len() { break; }
        r.seek(next);
    }
    if has_id && has_loc { Some(SigInfo { id, sig_type, location }) } else { None }
}

#[derive(Default, Clone)]
struct SigInfo { id: String, sig_type: String, location: f32 }

fn walk_ribbon_network_property_refs(r: &mut Reader<'_>) -> Vec<i32> {
    let mut refs: Vec<i32> = Vec::new();
    while r.remaining() > 8 {
        let Some(t) = r.read_tag() else { break };
        let dp = r.tell();
        let next = dp + (t.size as usize);
        if (t.name == "NetworkProperties" || t.name == "NetworkPropertiesReverseOrder")
            && t.ptype == "ArrayProperty"
        {
            let body = &r.data[dp..next.min(r.data.len())];
            let mut sub = Reader::new(body, r.name_map);
            refs.extend(read_object_array_ints(&mut sub));
        }
        if next > r.data.len() { break; }
        r.seek(next);
    }
    refs
}

fn read_object_array_ints(r: &mut Reader<'_>) -> Vec<i32> {
    if r.remaining() < 4 { return Vec::new(); }
    let count = r.i32();
    if count <= 0 || count > 100_000 { return Vec::new(); }
    let mut out: Vec<i32> = Vec::with_capacity(count as usize);
    for _ in 0..count {
        if r.remaining() < 4 { break; }
        let v = r.i32();
        if v > 0 { out.push(v); }
    }
    out
}

fn walk_actor_for_collectable(r: &mut Reader<'_>) -> Option<(String, i32)> {
    let mut guid = String::new();
    let mut root_idx: i32 = 0;
    let mut has_guid = false; let mut has_root = false;
    while r.remaining() > 8 {
        let Some(t) = r.read_tag() else { break };
        let dp = r.tell();
        let next = dp + (t.size as usize);
        match (t.name.as_str(), t.ptype.as_str(), t.struct_type.as_str()) {
            ("CollectableID", "StructProperty", "Guid") => {
                guid = uasset::normalize_guid(&r.guid_str());
                has_guid = !guid.is_empty();
            }
            ("RootComponent", "ObjectProperty", _) => {
                root_idx = r.i32();
                has_root = root_idx > 0;
            }
            _ => {}
        }
        if next > r.data.len() { break; }
        r.seek(next);
    }
    if has_guid && has_root { Some((guid, root_idx)) } else { None }
}

fn walk_scene_comp_for_location(r: &mut Reader<'_>) -> Option<(f64, f64, f64)> {
    let mut out: Option<(f64, f64, f64)> = None;
    while r.remaining() > 8 {
        let Some(t) = r.read_tag() else { break };
        let dp = r.tell();
        let next = dp + (t.size as usize);
        if t.name == "RelativeLocation" && t.ptype == "StructProperty" {
            let size = t.size as usize;
            if size >= 24 && dp + 24 <= r.data.len() {
                let b = &r.data[dp..];
                let x = f64::from_le_bytes(b[0..8].try_into().unwrap());
                let y = f64::from_le_bytes(b[8..16].try_into().unwrap());
                let z = f64::from_le_bytes(b[16..24].try_into().unwrap());
                out = Some((x, y, z));
            } else if size >= 12 && dp + 12 <= r.data.len() {
                let b = &r.data[dp..];
                let x = f32::from_le_bytes(b[0..4].try_into().unwrap()) as f64;
                let y = f32::from_le_bytes(b[4..8].try_into().unwrap()) as f64;
                let z = f32::from_le_bytes(b[8..12].try_into().unwrap()) as f64;
                out = Some((x, y, z));
            }
        }
        if next > r.data.len() { break; }
        r.seek(next);
    }
    out
}

fn last_segment_after(s: &str, sep: &str) -> String {
    match s.rfind(sep) {
        Some(i) => s[i + sep.len()..].to_string(),
        None    => s.to_string(),
    }
}

/// Strip one trailing `_<digits>` suffix to recover the underlying BP
/// class. `BP_Robot_EMK_01_2` → `BP_Robot_EMK_01`.
fn actor_class_from_name(name: &str) -> String {
    match name.rfind('_') {
        Some(i) if i + 1 < name.len() => {
            let tail = &name[i + 1..];
            if tail.chars().all(|c| c.is_ascii_digit()) {
                name[..i].to_string()
            } else {
                name.to_string()
            }
        }
        _ => name.to_string(),
    }
}
