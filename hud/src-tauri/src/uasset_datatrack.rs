//! Port of hud-go's `datatrack_cooked.go`. Parses a
//! `RouteTimetableDataTrack.uasset` and returns the per-service ORDERED
//! breadcrumbs of (ribbon, ribbon-fraction) along each service's exact
//! in-game path. Lets the downstream path-builder draw polylines that
//! match the game's junction routing — no proximity guessing.

use std::collections::HashMap;
use std::path::Path;

use crate::uasset::{Reader, Umap};

#[derive(Debug, Clone, Default, serde::Serialize)]
pub struct TrackDataEvent {
    /// 32-char lowercase hex GUID, normalised so it matches the rails
    /// builder's ribbon-catalog keys.
    pub ribbon_guid:       String,
    pub ribbon_location:   f32,
    /// Cumulative cm from service start.
    pub distance:          f32,
    pub time_ticks:        i64,
    /// `StopPoint` / `GoVia` / `ActionPoint` / `ReversePoint` /
    /// `TrackSectionEntry` / `TrackSectionExit` / `MultiOccupancy`.
    pub data_type:         String,
    pub direction:         String,
    /// Index back into Service.Instructions; `-1` when unrelated.
    pub instruction_index: i32,
    pub go_via_index:      i32,
    pub section_id:        String,
    pub route_ix:          i32,
}

#[derive(Debug, Clone, Default, serde::Serialize)]
pub struct ServiceTrackData {
    pub service_name:   String,
    pub track_data:     Vec<TrackDataEvent>,
    pub action_indices: Vec<i32>,
}

#[derive(Debug, Clone, Default, serde::Serialize)]
pub struct DataTrack {
    pub source:         String,
    pub version_number: i32,
    pub services:       HashMap<String, ServiceTrackData>,
}

/// Read a `*DataTrack.uasset/.uexp` pair and return the parsed per-
/// service track data. Walks every export and takes the first
/// non-`Default__` one whose property stream decodes; mirrors hud-go's
/// `ParseCookedDataTrack`.
pub fn parse(uasset_path: &Path) -> Result<DataTrack, String> {
    let u = Umap::read(uasset_path)
        .map_err(|e| format!("read {}: {e}", uasset_path.display()))?;
    for exp in &u.exports {
        if exp.object_name.is_empty() { continue }
        if exp.object_name.starts_with("Default__") { continue }
        let Some(slice) = u.property_slice(exp) else { continue };
        let mut r = Reader::new(slice, &u.names);
        let mut dt = parse_export(&mut r);
        dt.source = uasset_path.to_string_lossy().into_owned();
        return Ok(dt);
    }
    Err(format!("no parseable DataTrack export in {}", uasset_path.display()))
}

fn parse_export(r: &mut Reader<'_>) -> DataTrack {
    let mut out = DataTrack::default();
    while r.remaining() > 8 {
        let Some(t) = r.read_tag() else { break };
        let dp = r.tell();
        match (t.name.as_str(), t.ptype.as_str()) {
            ("VersionNumber", "IntProperty") => {
                out.version_number = r.i32();
            }
            ("ServiceDataTracks", "MapProperty") => {
                parse_service_data_tracks_map(r, dp, t.size as usize, &mut out.services);
            }
            _ => {}
        }
        if r.tell() != dp + t.size as usize {
            r.seek(dp + t.size as usize);
        }
    }
    out
}

/// Decode the TMap<FName, FStruct> body. Layout: 4 B NumKeysToRemove +
/// i32 NumEntries + count × {key FName (8 B), value tagless property
/// stream terminated by `None`}.
fn parse_service_data_tracks_map(
    r: &mut Reader<'_>,
    dp: usize,
    size: usize,
    out: &mut HashMap<String, ServiceTrackData>,
) {
    let end = dp + size;
    r.seek(dp + 4); // NumKeysToRemove
    let count = r.i32();
    for _ in 0..count {
        if r.tell() >= end { break }
        let key = r.fname();
        let mut std = parse_service_track_data_value(r, end);
        std.service_name = key.clone();
        out.insert(key, std);
    }
    r.seek(end);
}

fn parse_service_track_data_value(r: &mut Reader<'_>, limit: usize) -> ServiceTrackData {
    let mut out = ServiceTrackData::default();
    while r.tell() < limit {
        let Some(t) = r.read_tag() else { break };
        let dp = r.tell();
        match (t.name.as_str(), t.ptype.as_str()) {
            ("TrackData", "ArrayProperty") => {
                out.track_data = parse_track_data_array(r, dp, t.size as usize);
            }
            ("ActionIndices", "ArrayProperty") => {
                out.action_indices = parse_int32_array(r, dp, t.size as usize);
            }
            _ => {}
        }
        if r.tell() != dp + t.size as usize {
            r.seek(dp + t.size as usize);
        }
    }
    out
}

/// Decode `Array<RouteTimetableTrackData>`. Layout: i32 count + inner
/// FPropertyTag header + count × element bodies (tagless, each
/// terminated by `None`).
fn parse_track_data_array(r: &mut Reader<'_>, dp: usize, size: usize) -> Vec<TrackDataEvent> {
    let end = dp + size;
    let count = r.i32();
    if count <= 0 { r.seek(end); return Vec::new(); }
    let Some(inner_tag) = r.read_tag() else { r.seek(end); return Vec::new() };
    let arr_end = r.tell() + inner_tag.size as usize;
    let mut out = Vec::with_capacity(count as usize);
    for _ in 0..count {
        if r.tell() >= arr_end { break }
        out.push(parse_track_data_entry(r, arr_end));
    }
    r.seek(end);
    out
}

fn parse_track_data_entry(r: &mut Reader<'_>, limit: usize) -> TrackDataEvent {
    let mut out = TrackDataEvent {
        instruction_index: -1,
        go_via_index:      -1,
        route_ix:          -1,
        ..Default::default()
    };
    while r.tell() < limit {
        let Some(t) = r.read_tag() else { break };
        let dp = r.tell();
        match (t.name.as_str(), t.ptype.as_str()) {
            ("Location", "StructProperty") if t.struct_type == "NetworkRibbonLocation" => {
                let (guid, loc) = read_network_ribbon_location(r, dp, t.size as usize);
                out.ribbon_guid     = normalize_guid(&fmt_guid_raw(guid));
                out.ribbon_location = loc;
            }
            ("Time", "StructProperty") if t.struct_type == "Timespan" => {
                out.time_ticks = r.i64();
            }
            ("Distance", "FloatProperty") => {
                out.distance = r.f32();
            }
            ("DataType", "EnumProperty") => {
                out.data_type = trim_enum_prefix(&r.fname(), "ETimetableTrackDataType::");
            }
            ("Direction", "EnumProperty") => {
                out.direction = trim_enum_prefix(&r.fname(), "EDirectionOfTravel::");
            }
            ("InstructionIndex", "IntProperty") => { out.instruction_index = r.i32(); }
            ("GoViaIndex",       "IntProperty") => { out.go_via_index      = r.i32(); }
            ("SectionId", "StructProperty") if t.struct_type == "Guid" => {
                let mut g = [0u8; 16];
                g.copy_from_slice(&r.data[dp..dp + 16]);
                out.section_id = normalize_guid(&fmt_guid_raw(g));
            }
            ("RouteIx", "IntProperty") => { out.route_ix = r.i32(); }
            _ => {}
        }
        if r.tell() != dp + t.size as usize {
            r.seek(dp + t.size as usize);
        }
    }
    out
}

/// `Array<IntProperty>` — primitive, no inner tag. Layout: i32 count +
/// count × int32.
fn parse_int32_array(r: &mut Reader<'_>, dp: usize, size: usize) -> Vec<i32> {
    let end = dp + size;
    let count = r.i32();
    if count <= 0 { r.seek(end); return Vec::new(); }
    let mut out = Vec::with_capacity(count as usize);
    for _ in 0..count {
        if r.tell() >= end { break }
        out.push(r.i32());
    }
    r.seek(end);
    out
}

/// Walk a `NetworkRibbonLocation` struct: `(RibbonReference FGuid,
/// RibbonLocation f32)` plus optional unrelated fields we don't decode.
/// Local copy of the same logic used in `uasset_timetable` — small
/// enough that avoiding an inter-module dependency is worth it.
fn read_network_ribbon_location(r: &mut Reader<'_>, dp: usize, size: usize) -> ([u8; 16], f32) {
    let end = dp + size;
    let mut guid = [0u8; 16];
    let mut loc  = 0f32;
    while r.tell() < end {
        let Some(t) = r.read_tag() else { break };
        let tdp = r.tell();
        match (t.name.as_str(), t.ptype.as_str()) {
            ("RibbonReference", "StructProperty") if t.struct_type == "Guid" => {
                guid.copy_from_slice(&r.data[tdp..tdp + 16]);
            }
            ("RibbonLocation", "FloatProperty") => {
                loc = r.f32();
            }
            _ => {}
        }
        r.seek(tdp + t.size as usize);
    }
    r.seek(end);
    (guid, loc)
}

/// Local fmt_guid copy — datatrack needs both fmt_guid (to produce the
/// 8-8-8-8 form) and normalize_guid (to lowercase + strip dashes).
/// Duplicated rather than introducing a circular module dep on
/// uasset_timetable.
fn fmt_guid_raw(g: [u8; 16]) -> String {
    if g == [0; 16] { return String::new() }
    let a = u32::from_le_bytes(g[0..4].try_into().unwrap());
    let b = u32::from_le_bytes(g[4..8].try_into().unwrap());
    let c = u32::from_le_bytes(g[8..12].try_into().unwrap());
    let d = u32::from_le_bytes(g[12..16].try_into().unwrap());
    format!("{a:08X}-{b:08X}-{c:08X}-{d:08X}")
}

/// Mirror of hud-go's `NormalizeGUID`: strip non-hex chars, lowercase,
/// require exactly 32 hex chars. Returns "" for input that doesn't
/// decode to 32 hex chars.
pub fn normalize_guid(s: &str) -> String {
    if s.is_empty() { return String::new() }
    let mut out = String::with_capacity(32);
    for c in s.chars() {
        match c {
            '0'..='9' | 'a'..='f' => out.push(c),
            'A'..='F'             => out.push(c.to_ascii_lowercase()),
            _ => {}
        }
    }
    if out.len() != 32 { String::new() } else { out }
}

fn trim_enum_prefix(s: &str, prefix: &str) -> String {
    s.strip_prefix(prefix).unwrap_or(s).to_string()
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn normalize_guid_rules() {
        assert_eq!(normalize_guid(""), "");
        // 8-8-8-8 form normalises to 32 lowercase hex chars (no dashes).
        assert_eq!(
            normalize_guid("AABBCCDD-EEFF0011-22334455-66778899"),
            "aabbccddeeff00112233445566778899"
        );
        // Short input → empty (must have exactly 32 hex chars).
        assert_eq!(normalize_guid("ABCD"), "");
        // Non-hex chars stripped, the 32-char body still recovers.
        assert_eq!(
            normalize_guid("AA-BB-CC-DD-EE-FF-00-11-22-33-44-55-66-77-88-99"),
            "aabbccddeeff00112233445566778899"
        );
    }

    #[test]
    fn trim_enum_basic() {
        assert_eq!(trim_enum_prefix("ETimetableTrackDataType::StopPoint", "ETimetableTrackDataType::"), "StopPoint");
        assert_eq!(trim_enum_prefix("Bare", "ENotMatching::"), "Bare");
    }
}
