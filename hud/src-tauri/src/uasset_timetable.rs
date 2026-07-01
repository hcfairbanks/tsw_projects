//! Port of hud-go's `timetable_cooked.go` + the parts of `parse.go` that
//! decode a `RouteTimetableDefinition` uasset.
//!
//! This module ships in two parts:
//!   * **Foundation (this slice)** — public types + entry function
//!     `parse_cooked_timetable`. The actual top-level property walker
//!     returns empty data and the entry function returns a Timetable with
//!     just the fields the umap reader gives us (SectionName, AssetPath,
//!     CrossPakReferenceName) plus empty Services/Formations/CompiledRVMap.
//!     This lets downstream code (DB writers in Phase 10.3) compile against
//!     the right shape today.
//!   * **Body (subsequent slices)** — port `parseTopLevel` +
//!     `parseFormationsArray` + `parseServicesArray` + the instruction
//!     walker incrementally. Each slice fills in one more property family
//!     without changing the public API.

use std::path::Path;

use crate::uasset::{Reader, Umap};

#[derive(Debug, Clone, Default, serde::Serialize)]
pub struct ScheduleItem {
    pub action:           String,
    pub details:          String,
    pub location:         String,
    /// Commodity for a LOAD/UNLOAD row, resolved from the loading
    /// criterion's CargoType object ref. Empty on non-load rows.
    pub cargo:            String,
    /// Scheduled hold duration (HH:MM:SS) extracted from the
    /// WaitingTime Timespan on Couple/Uncouple/LoadUnload instructions.
    pub waiting_time:     String,
    /// LoadUnload/Couple/Uncouple instruction's CompletionTime
    /// (HH:MM:SS clock time) — when the instruction is considered
    /// complete (train ready to depart).
    pub completion_time:  String,
    pub time1:            String,
    pub time2:            String,
    pub sort_order:       i32,
    pub structure:        String,
    pub structure_number: String,
    pub ribbon_guid:      String,
    pub ribbon_location:  f32,
    /// Computed downstream by the output layer (from ribbon geometry +
    /// route origin via UTM). Zero when the ribbon isn't anchored.
    pub lat:              f64,
    pub lng:              f64,
}

#[derive(Debug, Clone, Default, serde::Serialize)]
pub struct ServiceStop {
    pub station:    String,
    pub arrival:    String,
    pub departure:  String,
    pub platform:   String,
}

#[derive(Debug, Clone, Default, serde::Serialize)]
pub struct Service {
    pub name:                     String,
    pub headcode:                 String,
    pub friendly_name:            String,
    pub service_number:           String,
    pub layer_name:               String,
    pub service_operator:         String,
    pub formation:                String,
    pub end_of_service_formation: String,
    pub map_point_a:              String,
    pub map_point_b:              String,
    pub is_player_drivable:       bool,
    pub is_hidden:                bool,
    pub spawn_with_conductor:     bool,
    pub spawn_with_engineer:      bool,
    pub player_drivable_side:     String, // "Front" / "Back"
    pub source:                   String, // "Timetable" / "Scenario" / "Training" — set by caller
    pub service_class:            String,
    pub service_type:             String,
    pub description:              String,
    pub start_time:               String,
    /// "HH:MM" run time from StartTime to the final stop's arrival/completion.
    /// Uses the game-simulated times (SimulatedArrival/CompletionTime) as a
    /// fallback, so AI services without explicit scheduled times still get a
    /// real duration rather than 00:00.
    pub duration:                 String,
    pub stop_and_load_count:      i32,
    pub schedule:                 Vec<ScheduleItem>,
    /// Legacy CSV-output fields kept for output-layer parity.
    pub service_name:             String,
    pub stops:                    Vec<ServiceStop>,
}

#[derive(Debug, Clone, Default, serde::Serialize)]
pub struct FormationVehicle {
    /// Per-instance GUID that keys into the timetable's `CompiledRVMap`
    /// to recover the RVD asset path (vehicle model).
    pub rail_vehicle_id:    String,
    pub max_length_m:       f32,
    pub extension_length_m: f32,
    pub flipped:            bool,
}

#[derive(Debug, Clone, Default, serde::Serialize)]
pub struct Formation {
    pub name:                  String,
    pub spawn_ribbon_guid:     String,
    pub spawn_ribbon_location: f32,
    pub vehicles:              Vec<FormationVehicle>,
}

#[derive(Debug, Clone, Default, serde::Serialize)]
pub struct Timetable {
    pub route:                       String,
    pub section_name:                String,
    pub asset_path:                  String,
    pub services:                    Vec<Service>,
    pub formations:                  Vec<Formation>,
    /// Maps each FormationVehicle.rail_vehicle_id (GUID hex) to the
    /// asset path of the RailVehicleDefinition (RVD) describing its
    /// model. Combined with an RVD-by-path map populated by the
    /// extractor, this gives accurate per-vehicle class names.
    pub compiled_rv_map:             std::collections::HashMap<String, String>,
    /// PER-ASSET cross-pak reference. The mount path TSW uses to
    /// reference this timetable's parent route. Pulled from a NameMap
    /// entry of the form `/<X>/RouteDefinition/...`. Reliable even for
    /// cargo/scenario DLC timetables.
    pub cross_pak_reference_name:    String,
    /// DisplayName from the sibling `*_Definition.uasset`, when present.
    /// Used by the zip writer to replace generic service names like
    /// `PlayerService` / `AI_Service` with the scenario's user-facing
    /// title. Populated by the orchestrator post-parse — empty for
    /// pure Timetable assets (which don't have a sibling Definition).
    pub scenario_display_name:       String,
}

/// Categorise a timetable by its asset path. Mirrors hud-go's
/// `classifySource`: ServiceMode wins first (TSW marker for timetable
/// services that some DLCs nest under `Content/Scenarios/`); then
/// Training; then Scenarios; default Timetable.
pub fn classify_source(path: &str) -> &'static str {
    let p = path.replace('\\', "/").to_lowercase();
    if p.contains("/servicemode/") || p.contains("/service_mode/") {
        "Timetable"
    } else if p.contains("/training/") {
        "Training"
    } else if p.contains("/scenarios/") {
        "Scenario"
    } else {
        "Timetable"
    }
}

/// Pull the cross-pak reference name out of the NameMap. Same logic as
/// the route-definition module's `extract_cross_pak_reference_name`: look
/// for an entry of the form `/<X>/RouteDefinition/...` and return `<X>`.
fn extract_cross_pak_reference_name(names: &[String]) -> String {
    const MARKER: &str = "/RouteDefinition/";
    for n in names {
        if n.len() < 2 || !n.starts_with('/') { continue }
        let after_first = &n[1..];
        let Some(slash) = after_first.find('/') else { continue };
        let tail = &n[1 + slash..];
        if tail.starts_with(MARKER) {
            return n[1..1 + slash].to_string();
        }
    }
    String::new()
}

/// Format a 16-byte UE GUID as TSW's canonical 8-8-8-8 uppercase hex
/// form (`AABBCCDD-EEFF0011-22334455-66778899`). Empty bytes → empty
/// string so caller can distinguish "unset" from "all zeros".
fn fmt_guid(g: [u8; 16]) -> String {
    if g == [0; 16] {
        return String::new();
    }
    let a = u32::from_le_bytes(g[0..4].try_into().unwrap());
    let b = u32::from_le_bytes(g[4..8].try_into().unwrap());
    let c = u32::from_le_bytes(g[8..12].try_into().unwrap());
    let d = u32::from_le_bytes(g[12..16].try_into().unwrap());
    format!("{a:08X}-{b:08X}-{c:08X}-{d:08X}")
}

#[derive(Default)]
struct TopLevelData {
    section_name:    String,
    formations:      Vec<Formation>,
    services:        Vec<Service>,
    compiled_rv_map: std::collections::HashMap<String, String>,
}

/// Walk the top-level RouteTimetableDefinition property stream. Mirrors
/// hud-go's `parseTopLevel`. Threads the owning `Umap` through so the
/// nested instruction walker can resolve `CargoType` ObjectProperty
/// FPackageIndex values to their import names.
fn parse_top_level(r: &mut Reader<'_>, umap: &Umap) -> Result<TopLevelData, String> {
    let mut out = TopLevelData::default();
    // Mirror hud-go's `parseTopLevel` failure mode: error out unless
    // we successfully extract at least one service. The caller
    // (`parse_cooked_timetable`) iterates exports and breaks on first
    // Ok, so accepting an empty result locks us onto a stub export and
    // never reaches the real RouteTimetableDefinition.
    //
    // Two distinct stub patterns we've seen:
    //   * Bakerloo / Harlem / Mannheim / Morristown / LIRR: first
    //     export has no `Services` tag at all.
    //   * DresdenLeipzig / TrainingCentre / BostonWorcester / partial
    //     BostonSprinter: first export has a `Services` ArrayProperty
    //     but with count=0 (empty array). hud-go's parseServicesArray
    //     returns `nil` for that case; its `if out.services == nil`
    //     check then errors. We replicate that semantic by requiring
    //     `out.services` to be non-empty before declaring success.
    while r.remaining() > 8 {
        let Some(t) = r.read_tag() else { break };
        let dp = r.tell();
        match (t.name.as_str(), t.ptype.as_str()) {
            ("TimetableName", "TextProperty") => {
                out.section_name = r.ftext(t.size as usize);
                r.seek(dp + t.size as usize);
            }
            ("Formations", "ArrayProperty") => {
                out.formations = parse_formations_array(r, t.size as usize);
            }
            ("CompiledRVMap", "MapProperty") => {
                out.compiled_rv_map = parse_compiled_rv_map(r, dp, t.size as usize);
            }
            ("Services", "ArrayProperty") => {
                out.services = parse_services_array(r, t.size as usize, umap)?;
            }
            _ => { r.skip(t.size as usize); }
        }
    }
    if out.services.is_empty() {
        return Err("Services array not found or empty in payload".into());
    }
    Ok(out)
}

/// Decode the `CompiledRVMap`: `RailVehicleID (GUID) → RVD asset path`.
/// Mirrors hud-go's `parseCompiledRVMap`; serialised layout after the
/// MapProperty tag header is `[4 B alloc flags] [i32 count] [entries...]`
/// where each entry is `[16 B FGuid] [8 B FName] [4 B trailer]`.
fn parse_compiled_rv_map(r: &mut Reader<'_>, dp: usize, size: usize) -> std::collections::HashMap<String, String> {
    let mut out = std::collections::HashMap::new();
    let end = dp + size;
    if size < 8 {
        r.seek(end);
        return out;
    }
    r.seek(dp + 4); // skip alloc flags
    let count = r.i32();
    if count <= 0 {
        r.seek(end);
        return out;
    }
    const ENTRY_SIZE: usize = 28;
    if r.tell() + (count as usize) * ENTRY_SIZE > end {
        r.seek(end);
        return out;
    }
    for i in 0..count as usize {
        let base = r.tell() + i * ENTRY_SIZE;
        let mut g = [0u8; 16];
        g.copy_from_slice(&r.data[base..base + 16]);
        let guid = fmt_guid(g);
        let idx = i32::from_le_bytes(r.data[base + 16..base + 20].try_into().unwrap());
        let num = i32::from_le_bytes(r.data[base + 20..base + 24].try_into().unwrap());
        let mut path = String::new();
        if idx >= 0 && (idx as usize) < r.name_map.len() {
            path = r.name_map[idx as usize].clone();
            if num > 0 {
                path = format!("{path}_{}", num - 1);
            }
        }
        if !guid.is_empty() && !path.is_empty() {
            out.insert(guid, path);
        }
    }
    r.seek(end);
    out
}

/// Walk the `Formations` ArrayProperty<StructProperty> body. The cursor
/// is positioned at the array's int32 count when this is called. Returns
/// the parsed Vec<Formation>.
fn parse_formations_array(r: &mut Reader<'_>, outer_size: usize) -> Vec<Formation> {
    let end = r.tell() + outer_size;
    let count = r.i32();
    if count <= 0 {
        r.seek(end);
        return Vec::new();
    }
    let Some(inner_tag) = r.read_tag() else { r.seek(end); return Vec::new() };
    if inner_tag.size == 0 {
        r.seek(end);
        return Vec::new();
    }
    let arr_end = r.tell() + inner_tag.size as usize;

    let mut out = Vec::with_capacity(count as usize);
    for _ in 0..count {
        if r.tell() >= arr_end { break }
        let (f, next) = parse_formation(r, arr_end);
        out.push(f);
        r.seek(next);
    }
    r.seek(end);
    out
}

/// One Formation struct. Returns `(formation, position past the None
/// sentinel)`. Mirrors hud-go's two-pass approach: the first pass
/// decodes fields, the second pass re-walks the same tag stream to
/// find the position after `None` (since the first pass may exit via
/// the array-element limit before consuming the sentinel).
fn parse_formation(r: &mut Reader<'_>, limit: usize) -> (Formation, usize) {
    let mut f = Formation::default();
    let start = r.tell();

    while r.tell() < limit {
        let Some(t) = r.read_tag() else { break };
        let dp = r.tell();
        match (t.name.as_str(), t.ptype.as_str()) {
            ("Name", "NameProperty") => { f.name = r.fname(); }
            ("SpawnLocation", "StructProperty")
                if t.struct_type == "NetworkRibbonLocation" =>
            {
                let (guid, loc) = parse_network_ribbon_location(r, dp, t.size as usize);
                f.spawn_ribbon_guid = fmt_guid(guid);
                f.spawn_ribbon_location = loc;
            }
            ("RailVehicleInfo", "ArrayProperty") => {
                f.vehicles = parse_rail_vehicle_info_array(r, dp, t.size as usize);
            }
            _ => { r.seek(dp + t.size as usize); continue; }
        }
        if r.tell() != dp + t.size as usize {
            r.seek(dp + t.size as usize);
        }
    }

    // Re-scan past the None sentinel so the caller's `next` lands on
    // the next array element rather than at `limit`.
    let mut next = r.tell();
    r.seek(start);
    while r.tell() < limit {
        match r.read_tag() {
            Some(t) => { r.skip(t.size as usize); }
            None    => { next = r.tell(); break; }
        }
    }
    (f, next)
}

/// Decode an `ArrayProperty<StructProperty>` of FormationVehicle. Cursor
/// is positioned at the body's int32 count.
fn parse_rail_vehicle_info_array(r: &mut Reader<'_>, dp: usize, outer_size: usize) -> Vec<FormationVehicle> {
    let end = dp + outer_size;
    let count = r.i32();
    if count <= 0 {
        r.seek(end);
        return Vec::new();
    }
    let Some(inner_tag) = r.read_tag() else { r.seek(end); return Vec::new() };
    if inner_tag.size == 0 {
        r.seek(end);
        return Vec::new();
    }
    let arr_end = r.tell() + inner_tag.size as usize;

    let mut out = Vec::with_capacity(count as usize);
    for _ in 0..count {
        if r.tell() >= arr_end { break }
        let (v, next) = parse_formation_vehicle(r, arr_end);
        out.push(v);
        r.seek(next);
    }
    r.seek(end);
    out
}

fn parse_formation_vehicle(r: &mut Reader<'_>, limit: usize) -> (FormationVehicle, usize) {
    let mut v = FormationVehicle::default();
    let start = r.tell();

    while r.tell() < limit {
        let Some(t) = r.read_tag() else { break };
        let dp = r.tell();
        match (t.name.as_str(), t.ptype.as_str()) {
            ("RailVehicleID", "StructProperty")
                if t.struct_type == "Guid" && t.size >= 16 =>
            {
                let mut g = [0u8; 16];
                g.copy_from_slice(&r.data[dp..dp + 16]);
                v.rail_vehicle_id = fmt_guid(g);
            }
            ("MaxLength", "StructProperty")
                if t.struct_type == "DistanceQuantity" && t.size >= 4 =>
            {
                v.max_length_m = r.f32();
            }
            ("ExtensionLength", "StructProperty")
                if t.struct_type == "DistanceQuantity" && t.size >= 4 =>
            {
                v.extension_length_m = r.f32();
            }
            ("bFlipped", "BoolProperty") => {
                v.flipped = t.bool_val != 0;
            }
            _ => { r.seek(dp + t.size as usize); continue; }
        }
        if r.tell() != dp + t.size as usize {
            r.seek(dp + t.size as usize);
        }
    }

    let mut next = r.tell();
    r.seek(start);
    while r.tell() < limit {
        match r.read_tag() {
            Some(t) => { r.skip(t.size as usize); }
            None    => { next = r.tell(); break; }
        }
    }
    (v, next)
}

/// Decode a `NetworkRibbonLocation` struct: `(RibbonReference FGuid,
/// RibbonLocation f32)` plus optional unrelated fields we don't decode.
fn parse_network_ribbon_location(r: &mut Reader<'_>, dp: usize, size: usize) -> ([u8; 16], f32) {
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

/// Walk the `Services` ArrayProperty<StructProperty>. Same pattern as
/// `parse_formations_array`; threads `umap` for instruction-level
/// CargoType resolution.
fn parse_services_array(r: &mut Reader<'_>, outer_size: usize, umap: &Umap) -> Result<Vec<Service>, String> {
    let end = r.tell() + outer_size;
    let count = r.i32();
    if count <= 0 {
        r.seek(end);
        return Ok(Vec::new());
    }
    let Some(inner_tag) = r.read_tag() else {
        return Err("missing inner tag for Services array".into());
    };
    if inner_tag.size == 0 {
        return Err("zero-size inner tag for Services array".into());
    }
    let arr_end = r.tell() + inner_tag.size as usize;

    let mut out = Vec::with_capacity(count as usize);
    for _ in 0..count {
        if r.tell() >= arr_end { break }
        let (s, next) = parse_service(r, arr_end, umap);
        out.push(s);
        r.seek(next);
    }
    r.seek(end);
    Ok(out)
}

/// Walk one RouteTimetableServiceDefinition struct. Returns (service,
/// position past None sentinel). Handles all scalar fields, derives
/// Headcode from FriendlyName, and walks the Instructions array to
/// populate Schedule + StopAndLoadCount + StartTime + Stops.
fn parse_service(r: &mut Reader<'_>, limit: usize, umap: &Umap) -> (Service, usize) {
    let mut svc = Service::default();
    let mut start_time_ticks: i64 = 0;
    let mut max_end_ticks: i64 = 0;
    let start = r.tell();

    while r.tell() < limit {
        let Some(t) = r.read_tag() else { break };
        let dp = r.tell();
        let mut handled = true;

        match (t.name.as_str(), t.ptype.as_str()) {
            ("Name",                       "NameProperty") => svc.name             = r.fname(),
            ("LayerName",                  "NameProperty") => svc.layer_name       = r.fname(),
            ("ServiceOperator",            "NameProperty") => svc.service_operator = r.fname(),
            ("FormationName",              "NameProperty") => svc.formation        = r.fname(),
            ("EndOfServiceFormationName",  "NameProperty") => svc.end_of_service_formation = r.fname(),
            ("FriendlyName",               "TextProperty") => svc.friendly_name    = r.ftext(t.size as usize),
            ("ServiceNumber",              "StrProperty")  => svc.service_number   = r.fstr(),
            ("MapPointA",                  "StrProperty")  => svc.map_point_a      = r.fstr(),
            ("MapPointB",                  "StrProperty")  => svc.map_point_b      = r.fstr(),
            ("bIsPlayerDrivable",          "BoolProperty") => svc.is_player_drivable    = t.bool_val != 0,
            ("bIsHiddenInFrontend",        "BoolProperty") => svc.is_hidden             = t.bool_val != 0,
            ("bSpawnWithConductor",        "BoolProperty") => svc.spawn_with_conductor  = t.bool_val != 0,
            ("bSpawnWithEngineer",         "BoolProperty") => svc.spawn_with_engineer   = t.bool_val != 0,
            ("PlayerDrivableSide",         "EnumProperty") => {
                let raw = r.fname();
                svc.player_drivable_side = raw.strip_prefix("ESide::").unwrap_or(&raw).to_string();
            }
            ("ServiceClass",               "EnumProperty") => {
                let raw = r.fname();
                svc.service_class = raw.strip_prefix("EServiceClass::").unwrap_or(&raw).to_string();
                svc.service_type  = service_class_to_type(&svc.service_class);
            }
            ("Description",                "TextProperty") => svc.description = r.ftext(t.size as usize),
            ("StartTime",                  "StructProperty") if t.struct_type == "Timespan" => {
                start_time_ticks = r.i64();
                svc.start_time   = ticks_to_hms(start_time_ticks);
            }
            ("Instructions",               "ArrayProperty") => {
                let (schedule, stop_and_load, end_ticks) = parse_instructions_array(r, dp, t.size as usize, umap);
                svc.schedule            = schedule;
                svc.stop_and_load_count = stop_and_load;
                max_end_ticks           = end_ticks;
            }
            _ => { handled = false; }
        }
        if handled {
            // Make sure we land at dp + t.size even when the matched
            // reader consumed fewer bytes than declared.
            if r.tell() != dp + t.size as usize {
                r.seek(dp + t.size as usize);
            }
        } else {
            r.seek(dp + t.size as usize);
        }
    }

    // Headcode from FriendlyName: "2U02 : Ryde St. Johns - Ryde Pier Head".
    // Fall through to the internal Name when there's no " : " marker.
    if let Some(idx) = svc.friendly_name.find(" : ") {
        svc.headcode = svc.friendly_name[..idx].to_string();
    } else {
        svc.headcode = svc.name.clone();
    }
    svc.service_name = svc.friendly_name.clone();

    // Duration = StartTime → the latest stop tick (explicit or simulated).
    svc.duration = duration_from_ticks(start_time_ticks, max_end_ticks);

    // Prepend WAIT FOR SERVICE row when StartTime was populated.
    if start_time_ticks > 0 {
        svc.schedule = prepend_wait_for_service(svc.schedule, start_time_ticks);
    }
    // Build the legacy Stops slice (CSV output parity).
    svc.stops = build_stops(&svc.schedule);

    // Re-scan past None sentinel for the caller's `next` cursor.
    let mut next = r.tell();
    r.seek(start);
    while r.tell() < limit {
        match r.read_tag() {
            Some(t) => { r.skip(t.size as usize); }
            None    => { next = r.tell(); break; }
        }
    }
    (svc, next)
}

/// Mirror hud-go's `serviceClassToType`: lowercase the EServiceClass
/// value, empty in / empty out.
fn service_class_to_type(class: &str) -> String {
    if class.is_empty() { String::new() } else { class.to_lowercase() }
}

// ============================================================ Instructions

/// One decoded instruction in the cooked Instructions stream. Mirrors
/// hud-go's unexported `instruction` struct. Used internally — the
/// public output is the assembled `Vec<ScheduleItem>`.
#[derive(Debug, Default, Clone)]
struct Instruction {
    instruction_type:     String,
    destination:          String,
    ribbon_guid:          [u8; 16],
    ribbon_location:      f32,
    arrival_time:         i64,
    simulated_arrival:    i64,
    completion_time:      i64,
    /// Game-simulated completion tick; used as a fallback end time when no
    /// explicit CompletionTime/ArrivalTime is set (common for AI services).
    simulated_completion: i64,
    loading_criteria:     Vec<LoadingCriterion>,
    is_stopping:          bool,
    waiting_time_secs:    i64,
    uncouple_count:       i32,
    /// not used yet downstream
    #[allow(dead_code)]
    formation_name:       String,
    destination_display:  String,
}

#[derive(Debug, Default, Clone)]
struct LoadingCriterion {
    b_load:          bool,
    b_unload:        bool,
    min_time_secs:   i64,
    /// not used yet downstream
    #[allow(dead_code)]
    specific_cargo:  bool,
    /// not used yet downstream
    #[allow(dead_code)]
    cargo_class:     String,
    cargo_type:      String,
}

/// Parse the `Instructions` ArrayProperty body. Returns the assembled
/// schedule + a count of instructions where `bIsStopping && InstructionType==LoadUnload`
/// (drives the conductor_compatible revenue-service check downstream).
fn parse_instructions_array(r: &mut Reader<'_>, dp: usize, outer_size: usize, umap: &Umap) -> (Vec<ScheduleItem>, i32, i64) {
    let end = dp + outer_size;
    let count = r.i32();
    if count <= 0 {
        r.seek(end);
        return (Vec::new(), 0, 0);
    }
    let Some(inner_tag) = r.read_tag() else { r.seek(end); return (Vec::new(), 0, 0) };
    let arr_end = r.tell() + inner_tag.size as usize;

    let mut instrs = Vec::with_capacity(count as usize);
    for _ in 0..count {
        if r.tell() >= arr_end { break }
        let (ins, next) = parse_instruction(r, arr_end, umap);
        instrs.push(ins);
        r.seek(next);
    }
    r.seek(end);

    let stop_and_load = instrs.iter()
        .filter(|ins| ins.is_stopping && ins.instruction_type == "LoadUnload")
        .count() as i32;
    // Latest scheduled tick across every instruction — the service's end. Prefer
    // explicit ArrivalTime/CompletionTime, fall back to the game-simulated
    // values so AI services (which often carry only simulated times) still get
    // a real end time. Timespan ticks (100ns units).
    let max_end_ticks = instrs.iter()
        .map(|ins| ins.completion_time.max(ins.simulated_completion)
                       .max(ins.arrival_time).max(ins.simulated_arrival))
        .max()
        .unwrap_or(0);
    (build_schedule(&instrs), stop_and_load, max_end_ticks)
}

fn parse_instruction(r: &mut Reader<'_>, limit: usize, umap: &Umap) -> (Instruction, usize) {
    let mut ins = Instruction::default();
    let start = r.tell();

    while r.tell() < limit {
        let Some(t) = r.read_tag() else { break };
        let dp = r.tell();
        let mut handled = true;
        match (t.name.as_str(), t.ptype.as_str()) {
            ("InstructionType", "EnumProperty") => {
                let v = r.fname();
                ins.instruction_type = simplify_enum(&v);
            }
            ("Destination", "StructProperty") => {
                let (name, guid, loc) = parse_route_location_name(r, dp, t.size as usize);
                ins.destination     = name;
                ins.ribbon_guid     = guid;
                ins.ribbon_location = loc;
            }
            ("ArrivalTime",            "StructProperty") if t.struct_type == "Timespan" => { ins.arrival_time      = r.i64(); }
            ("SimulatedArrivalTime",   "StructProperty") if t.struct_type == "Timespan" => { ins.simulated_arrival = r.i64(); }
            ("CompletionTime",         "StructProperty") if t.struct_type == "Timespan" => { ins.completion_time   = r.i64(); }
            ("SimulatedCompletionTime","StructProperty") if t.struct_type == "Timespan" => { ins.simulated_completion = r.i64(); }
            ("LoadingCriteria", "ArrayProperty") => {
                ins.loading_criteria = parse_loading_criteria_array(r, dp, t.size as usize, umap);
            }
            ("bIsStopping", "BoolProperty") => { ins.is_stopping = t.bool_val != 0; }
            ("WaitingTime", "StructProperty") if t.struct_type == "Timespan" => {
                // Timespan stores 100-ns ticks; divide for seconds.
                ins.waiting_time_secs = r.i64() / 10_000_000;
            }
            ("UncoupleCount", "IntProperty") => { ins.uncouple_count = r.i32(); }
            ("FormationName", "NameProperty") => { ins.formation_name = r.fname(); }
            ("DestinationDisplayName", "TextProperty") => {
                ins.destination_display = r.ftext(t.size as usize);
            }
            _ => { handled = false; }
        }
        if handled {
            if r.tell() != dp + t.size as usize { r.seek(dp + t.size as usize); }
        } else {
            r.seek(dp + t.size as usize);
        }
    }

    // Re-scan for None sentinel — same pattern as parse_formation.
    let mut next = r.tell();
    r.seek(start);
    while r.tell() < limit {
        match r.read_tag() {
            Some(t) => { r.skip(t.size as usize); }
            None    => { next = r.tell(); break; }
        }
    }
    (ins, next)
}

fn parse_loading_criteria_array(r: &mut Reader<'_>, dp: usize, outer_size: usize, umap: &Umap) -> Vec<LoadingCriterion> {
    let end = dp + outer_size;
    let count = r.i32();
    if count <= 0 { r.seek(end); return Vec::new(); }
    let Some(inner_tag) = r.read_tag() else { r.seek(end); return Vec::new() };
    let arr_end = r.tell() + inner_tag.size as usize;

    let mut out = Vec::with_capacity(count as usize);
    for _ in 0..count {
        if r.tell() >= arr_end { break }
        let (c, next) = parse_loading_criterion_item(r, arr_end, umap);
        out.push(c);
        r.seek(next);
    }
    r.seek(end);
    out
}

fn parse_loading_criterion_item(r: &mut Reader<'_>, limit: usize, umap: &Umap) -> (LoadingCriterion, usize) {
    let mut crit = LoadingCriterion::default();
    let start = r.tell();

    while r.tell() < limit {
        let Some(t) = r.read_tag() else { break };
        let dp = r.tell();
        let mut handled = true;
        match (t.name.as_str(), t.ptype.as_str()) {
            ("bLoad",   "BoolProperty") => { crit.b_load   = t.bool_val != 0; }
            ("bUnload", "BoolProperty") => { crit.b_unload = t.bool_val != 0; }
            ("MinimumTime", "StructProperty") if t.struct_type == "Timespan" => {
                crit.min_time_secs = r.i64() / 10_000_000;
            }
            ("bSpecificCargoType", "BoolProperty") => { crit.specific_cargo = t.bool_val != 0; }
            ("CargoClassName", "StructProperty") => {
                crit.cargo_class = read_cargo_class_ref(r, dp, t.size as usize);
            }
            ("CargoType", "ObjectProperty") => {
                // ObjectProperty body is a single i32 FPackageIndex.
                let idx = r.i32();
                let name = umap.resolve_fpackage_index(idx);
                if !name.is_empty() {
                    crit.cargo_type = name;
                }
            }
            _ => { handled = false; }
        }
        if handled {
            if r.tell() != dp + t.size as usize { r.seek(dp + t.size as usize); }
        } else {
            r.seek(dp + t.size as usize);
        }
    }

    let mut next = r.tell();
    r.seek(start);
    while r.tell() < limit {
        match r.read_tag() {
            Some(t) => { r.skip(t.size as usize); }
            None    => { next = r.tell(); break; }
        }
    }
    (crit, next)
}

/// Decode a `RouteLocationName` struct: pulls the `Name` (NameProperty)
/// and the nested `Location` NetworkRibbonLocation (GUID + fraction).
/// Returns (name, ribbon_guid, ribbon_location).
fn parse_route_location_name(r: &mut Reader<'_>, dp: usize, size: usize) -> (String, [u8; 16], f32) {
    let end = dp + size;
    let mut name = String::new();
    let mut guid = [0u8; 16];
    let mut loc  = 0f32;
    while r.tell() < end {
        let Some(t) = r.read_tag() else { break };
        let tdp = r.tell();
        match (t.name.as_str(), t.ptype.as_str()) {
            ("Name", "NameProperty") => {
                let v = r.fname();
                if v != "None" { name = v; }
            }
            ("Location", "StructProperty") if t.struct_type == "NetworkRibbonLocation" => {
                let (g, l) = parse_network_ribbon_location(r, tdp, t.size as usize);
                guid = g;
                loc  = l;
            }
            _ => {}
        }
        r.seek(tdp + t.size as usize);
    }
    r.seek(end);
    (name, guid, loc)
}

/// Walk a `CargoClassReference` struct, returning the first
/// name/string field it carries (cargo class FName). Returns "" when
/// none of the fields decode.
fn read_cargo_class_ref(r: &mut Reader<'_>, dp: usize, size: usize) -> String {
    let end = dp + size;
    let mut got = String::new();
    while r.tell() < end {
        let Some(t) = r.read_tag() else { break };
        let idp = r.tell();
        let v = match t.ptype.as_str() {
            "NameProperty" => r.fname(),
            "StrProperty"  => r.fstr(),
            _              => String::new(),
        };
        if !v.is_empty() && got.is_empty() {
            got = v;
        }
        r.seek(idp + t.size as usize);
    }
    r.seek(end);
    got
}

// ============================================================ Time helpers

/// Strip a UE enum prefix like `EInstructionType::LoadUnload` → `LoadUnload`.
fn simplify_enum(s: &str) -> String {
    match s.rfind("::") {
        Some(i) => s[i + 2..].to_string(),
        None    => s.to_string(),
    }
}

/// HH:MM:SS from a duration in seconds. "" for non-positive.
fn secs_to_hms(s: i64) -> String {
    if s <= 0 { return String::new() }
    format!("{:02}:{:02}:{:02}", s / 3600, (s % 3600) / 60, s % 60)
}

/// HH:MM:SS from UE's 100-ns Timespan ticks. "" for non-positive.
fn ticks_to_hms(ticks: i64) -> String {
    if ticks <= 0 { return String::new() }
    let s = ticks / 10_000_000;
    format!("{:02}:{:02}:{:02}", s / 3600, (s % 3600) / 60, s % 60)
}

/// "HH:MM" run time between two time-of-day Timespan ticks. Handles the
/// after-midnight case (end < start → add a day). "00:00" when either tick is
/// absent or the span is zero.
fn duration_from_ticks(start: i64, end: i64) -> String {
    if start <= 0 || end <= 0 {
        return "00:00".to_string();
    }
    const DAY: i64 = 24 * 3600 * 10_000_000;
    let end = if end < start { end + DAY } else { end };
    let secs = (end - start) / 10_000_000;
    format!("{:02}:{:02}", secs / 3600, (secs % 3600) / 60)
}

/// Format the Couple/Uncouple "<n> vehicles" detail. Falls back to
/// bare "vehicles" when the count is absent.
fn vehicles_detail(n: i32) -> String {
    if n <= 0 { "vehicles".into() } else { format!("{n} vehicles") }
}

/// Split a location label like "Boston South Station Track 02" into
/// (station, structure-type, structure-number). Recognises Platform /
/// Siding / Track / Line separators, longest-first.
fn split_location(loc: &str) -> (String, String, String) {
    const SEPS: &[&str] = &[" Platform ", " Siding ", " Track ", " Line "];
    for sep in SEPS {
        if let Some(idx) = loc.find(sep) {
            let station    = loc[..idx].to_string();
            let stype      = sep.trim().to_string();
            let snum       = loc[idx + sep.len()..].to_string();
            return (station, stype, snum);
        }
    }
    (loc.to_string(), String::new(), String::new())
}

/// Prepend the synthetic "WAIT FOR SERVICE" row that captures the
/// service's StartTime. Mirrors hud-go's `prependWaitForService`.
fn prepend_wait_for_service(schedule: Vec<ScheduleItem>, ticks: i64) -> Vec<ScheduleItem> {
    let s = ticks / 10_000_000;
    let h = s / 3600;
    let m = (s % 3600) / 60;
    let sec = s % 60;
    // time2 uses no leading zero for hour (matches reference "5:20:00").
    let t2 = format!("{h}:{m:02}:{sec:02}");
    let wait = ScheduleItem {
        action: "WAIT FOR SERVICE".into(),
        details: format!("Service is scheduled to start at {h:02}:{m:02}:{sec:02}"),
        time2: t2,
        ..Default::default()
    };
    let mut out = Vec::with_capacity(schedule.len() + 1);
    out.push(wait);
    for (i, mut item) in schedule.into_iter().enumerate() {
        item.sort_order = (i as i32) + 1;
        out.push(item);
    }
    out
}

/// Build legacy ServiceStop entries from a Schedule (CSV output parity).
fn build_stops(schedule: &[ScheduleItem]) -> Vec<ServiceStop> {
    let mut stops: Vec<ServiceStop> = Vec::new();
    for (j, item) in schedule.iter().enumerate() {
        if item.action != "STOP AT LOCATION" { continue }
        let mut stop = ServiceStop {
            station:   item.location.clone(),
            arrival:   item.time1.clone(),
            platform:  item.structure_number.clone(),
            ..Default::default()
        };
        if let Some(next) = schedule.get(j + 1) {
            if next.action == "LOAD PASSENGERS" || next.action == "UNLOAD PASSENGERS" {
                stop.departure = next.time1.clone();
            }
        }
        stops.push(stop);
    }
    stops
}

// ============================================================ Schedule builder

/// Turn the cooked instruction stream into the user-facing schedule
/// rows. Mirrors hud-go's `buildSchedule` — see comments there for the
/// rationale on Couple/Uncouple handling, the leading-LoadUnload split,
/// the CompletionTime vs MinimumTime LOAD-time fallback, and the bare-
/// GoTo bIsStopping classification.
fn build_schedule(instrs: &[Instruction]) -> Vec<ScheduleItem> {
    let mut items: Vec<ScheduleItem> = Vec::new();
    let mut seen_load_unload = false;
    let mut i = 0usize;

    while i < instrs.len() {
        let instr = &instrs[i];
        let itype = instr.instruction_type.as_str();

        // Couple / Uncouple — standalone yard work rows.
        if itype == "Couple" {
            items.push(ScheduleItem {
                action:       "COUPLE TO FORMATION".into(),
                details:      "vehicles".into(),
                waiting_time: secs_to_hms(instr.waiting_time_secs),
                sort_order:   items.len() as i32,
                ..Default::default()
            });
            i += 1;
            continue;
        }
        if itype == "Uncouple" {
            items.push(ScheduleItem {
                action:       "UNCOUPLE VEHICLES".into(),
                details:      vehicles_detail(instr.uncouple_count),
                waiting_time: secs_to_hms(instr.waiting_time_secs),
                sort_order:   items.len() as i32,
                ..Default::default()
            });
            i += 1;
            continue;
        }

        // Service starts at a platform: leading LoadUnload with no
        // preceding GoTo — split each criterion into its own row.
        if itype == "LoadUnload" && !seen_load_unload {
            let lu = instr;
            // Real scheduled ArrivalTime only — never the SimulatedArrivalTime
            // estimate. The simulated value is frequently unset or non-monotonic
            // for a service and fabricated bogus, out-of-order stop times (e.g. a
            // stop the game leaves blank reading 12:01 before an earlier stop at
            // 10:08). When there's no real time, arr_ticks stays 0 → ticks_to_hms
            // yields "" → the schedule shows "--:--:--". (Duration still falls
            // back to the simulated times — that's a separate compute path.)
            let arr_ticks = lu.arrival_time;
            let wt    = secs_to_hms(lu.waiting_time_secs);
            let comp_t = if lu.completion_time > 0 { ticks_to_hms(lu.completion_time) } else { String::new() };
            let wait_t = if arr_ticks > 0 && lu.waiting_time_secs > 0 {
                ticks_to_hms(arr_ticks + lu.waiting_time_secs * 10_000_000)
            } else { String::new() };

            for crit in &lu.loading_criteria {
                let ctime = if lu.completion_time > 0 {
                    ticks_to_hms(lu.completion_time)
                } else if arr_ticks > 0 {
                    ticks_to_hms(arr_ticks + crit.min_time_secs * 10_000_000)
                } else { String::new() };

                if crit.b_load {
                    items.push(ScheduleItem {
                        action:          "LOAD PASSENGERS".into(),
                        cargo:           crit.cargo_type.clone(),
                        waiting_time:    wt.clone(),
                        completion_time: comp_t.clone(),
                        time1:           ctime,
                        sort_order:      items.len() as i32,
                        ..Default::default()
                    });
                } else if crit.b_unload {
                    items.push(ScheduleItem {
                        action:          "UNLOAD PASSENGERS".into(),
                        cargo:           crit.cargo_type.clone(),
                        waiting_time:    wt.clone(),
                        completion_time: comp_t.clone(),
                        sort_order:      items.len() as i32,
                        ..Default::default()
                    });
                } else {
                    items.push(ScheduleItem {
                        action:          "WAIT".into(),
                        details:         "Wait for a moment".into(),
                        waiting_time:    wt.clone(),
                        completion_time: comp_t.clone(),
                        time1:           wait_t.clone(),
                        sort_order:      items.len() as i32,
                        ..Default::default()
                    });
                }
            }
            seen_load_unload = true;
            i += 1;
            continue;
        }

        if itype != "GoTo" {
            i += 1;
            continue;
        }

        let dest = if !instr.destination.is_empty() { &instr.destination } else { &instr.destination_display };
        let has_lu = i + 1 < instrs.len() && instrs[i + 1].instruction_type == "LoadUnload";

        if has_lu {
            let lu = &instrs[i + 1];
            // Real scheduled ArrivalTime only — never the SimulatedArrivalTime
            // estimate. The simulated value is frequently unset or non-monotonic
            // for a service and fabricated bogus, out-of-order stop times (e.g. a
            // stop the game leaves blank reading 12:01 before an earlier stop at
            // 10:08). When there's no real time, arr_ticks stays 0 → ticks_to_hms
            // yields "" → the schedule shows "--:--:--". (Duration still falls
            // back to the simulated times — that's a separate compute path.)
            let arr_ticks = lu.arrival_time;
            let arr_time = ticks_to_hms(arr_ticks);

            let (station, struct_type, struct_num) = split_location(dest);
            let details = if !arr_time.is_empty() {
                format!("{dest} - {arr_time}")
            } else {
                dest.to_string()
            };
            items.push(ScheduleItem {
                action:           "STOP AT LOCATION".into(),
                details,
                location:         station,
                time1:            arr_time,
                sort_order:       items.len() as i32,
                structure:        struct_type,
                structure_number: struct_num,
                ribbon_guid:      fmt_guid(instr.ribbon_guid),
                ribbon_location:  instr.ribbon_location,
                ..Default::default()
            });

            let wt    = secs_to_hms(lu.waiting_time_secs);
            let comp_t = if lu.completion_time > 0 { ticks_to_hms(lu.completion_time) } else { String::new() };
            let wait_t = if arr_ticks > 0 && lu.waiting_time_secs > 0 {
                ticks_to_hms(arr_ticks + lu.waiting_time_secs * 10_000_000)
            } else { String::new() };

            for crit in &lu.loading_criteria {
                let ctime = if lu.completion_time > 0 {
                    ticks_to_hms(lu.completion_time)
                } else if arr_ticks > 0 {
                    ticks_to_hms(arr_ticks + crit.min_time_secs * 10_000_000)
                } else { String::new() };

                if crit.b_load {
                    items.push(ScheduleItem {
                        action:          "LOAD PASSENGERS".into(),
                        cargo:           crit.cargo_type.clone(),
                        waiting_time:    wt.clone(),
                        completion_time: comp_t.clone(),
                        time1:           ctime,
                        sort_order:      items.len() as i32,
                        ..Default::default()
                    });
                } else if crit.b_unload {
                    items.push(ScheduleItem {
                        action:          "UNLOAD PASSENGERS".into(),
                        cargo:           crit.cargo_type.clone(),
                        waiting_time:    wt.clone(),
                        completion_time: comp_t.clone(),
                        sort_order:      items.len() as i32,
                        ..Default::default()
                    });
                } else {
                    items.push(ScheduleItem {
                        action:          "WAIT".into(),
                        details:         "Wait for a moment".into(),
                        waiting_time:    wt.clone(),
                        completion_time: comp_t.clone(),
                        time1:           wait_t.clone(),
                        sort_order:      items.len() as i32,
                        ..Default::default()
                    });
                }
            }
            seen_load_unload = true;
            i += 2;
        } else {
            // Bare GoTo — STOP AT LOCATION when bIsStopping, GO VIA LOCATION otherwise.
            let action = if instr.is_stopping { "STOP AT LOCATION" } else { "GO VIA LOCATION" };
            let eff_dest = if !dest.is_empty() { dest.to_string() } else { instr.destination_display.clone() };
            let (station, struct_type, struct_num) = split_location(&eff_dest);
            let loc = if !struct_type.is_empty() { station.clone() } else { eff_dest.clone() };
            let details = if eff_dest.is_empty() && action == "GO VIA LOCATION" {
                "As Indicated".to_string()
            } else {
                eff_dest
            };
            items.push(ScheduleItem {
                action:           action.into(),
                details,
                location:         loc,
                sort_order:       items.len() as i32,
                structure:        struct_type,
                structure_number: struct_num,
                ribbon_guid:      fmt_guid(instr.ribbon_guid),
                ribbon_location:  instr.ribbon_location,
                ..Default::default()
            });
            i += 1;
        }
    }
    items
}

/// Read a `Timetable.uasset` and return a populated `Timetable`. Mirrors
/// hud-go's `ParseCookedTimetable`. Walks every export until one's
/// property stream parses without error — fall-back is defensive against
/// future paks that re-order the export table.
pub fn parse_cooked_timetable(uasset_path: &Path, route_name: &str) -> Result<Timetable, String> {
    let u = Umap::read(uasset_path)
        .map_err(|e| format!("read uasset {}: {e}", uasset_path.display()))?;
    if u.exports.is_empty() {
        return Err(format!("no exports in {}", uasset_path.display()));
    }
    let mut top: Option<TopLevelData> = None;
    let mut first_err: Option<String> = None;
    for exp in u.exports.iter() {
        let Some(slice) = u.property_slice(exp) else { continue };
        let mut r = Reader::new(slice, &u.names);
        match parse_top_level(&mut r, &u) {
            Ok(t)  => { top = Some(t); break; }
            Err(e) => { if first_err.is_none() { first_err = Some(e); } }
        }
    }
    let top = match top {
        Some(t) => t,
        None    => return Err(first_err.unwrap_or_else(|| format!(
            "no parseable RouteTimetableDefinition export in {}",
            uasset_path.display()
        ))),
    };

    let source = classify_source(uasset_path.to_string_lossy().as_ref());
    let mut services = top.services;
    for s in services.iter_mut() {
        s.source = source.to_string();
    }
    Ok(Timetable {
        route:                    route_name.to_string(),
        section_name:             top.section_name,
        asset_path:               uasset_path.to_string_lossy().into_owned(),
        services,
        formations:               top.formations,
        compiled_rv_map:          top.compiled_rv_map,
        scenario_display_name:    String::new(),
        cross_pak_reference_name: extract_cross_pak_reference_name(&u.names),
    })
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn helpers_time_and_format() {
        assert_eq!(secs_to_hms(0),       "");
        assert_eq!(secs_to_hms(-5),      "");
        assert_eq!(secs_to_hms(75),      "00:01:15");
        assert_eq!(secs_to_hms(3661),    "01:01:01");

        assert_eq!(ticks_to_hms(0),                      "");
        // 1 second = 10_000_000 ticks
        assert_eq!(ticks_to_hms(10_000_000),             "00:00:01");
        assert_eq!(ticks_to_hms(3601 * 10_000_000),      "01:00:01");

        assert_eq!(vehicles_detail(0),  "vehicles");
        assert_eq!(vehicles_detail(50), "50 vehicles");

        assert_eq!(simplify_enum("EInstructionType::LoadUnload"), "LoadUnload");
        assert_eq!(simplify_enum("Bare"),                          "Bare");
    }

    #[test]
    fn split_location_rules() {
        // Longest separator wins (Track 02 inside a Station name).
        let (s, t, n) = split_location("Boston South Station Track 02");
        assert_eq!(s, "Boston South Station");
        assert_eq!(t, "Track");
        assert_eq!(n, "02");

        let (s, t, n) = split_location("London Euston Platform 5");
        assert_eq!(s, "London Euston");
        assert_eq!(t, "Platform");
        assert_eq!(n, "5");

        // No separator — entire string is the station.
        let (s, t, n) = split_location("Cross Over");
        assert_eq!(s, "Cross Over");
        assert!(t.is_empty());
        assert!(n.is_empty());
    }

    #[test]
    fn prepend_wait_for_service_renumbers() {
        let sched = vec![
            ScheduleItem { action: "STOP AT LOCATION".into(), sort_order: 0, ..Default::default() },
            ScheduleItem { action: "LOAD PASSENGERS".into(),  sort_order: 1, ..Default::default() },
        ];
        // ticks for 05:20:00 = 5*3600 + 20*60 = 19200 seconds * 10M
        let out = prepend_wait_for_service(sched, 19200 * 10_000_000);
        assert_eq!(out.len(), 3);
        assert_eq!(out[0].action, "WAIT FOR SERVICE");
        assert_eq!(out[0].time2,  "5:20:00");
        assert_eq!(out[1].sort_order, 1);
        assert_eq!(out[2].sort_order, 2);
    }

    #[test]
    fn fmt_guid_basic() {
        let mut g = [0u8; 16];
        // Empty all-zeros → "".
        assert_eq!(fmt_guid(g), "");
        // 0x11223344-AABBCCDD-… in LE order:
        g[0..4].copy_from_slice(&0x11223344u32.to_le_bytes());
        g[4..8].copy_from_slice(&0xAABBCCDDu32.to_le_bytes());
        g[8..12].copy_from_slice(&0x00000001u32.to_le_bytes());
        g[12..16].copy_from_slice(&0xFFFFFFFFu32.to_le_bytes());
        assert_eq!(fmt_guid(g), "11223344-AABBCCDD-00000001-FFFFFFFF");
    }

    #[test]
    fn classify_source_rules() {
        assert_eq!(classify_source("/Game/Content/ServiceMode/Foo.uasset"), "Timetable");
        assert_eq!(classify_source("/Game/Content/Service_Mode/Foo.uasset"), "Timetable");
        assert_eq!(classify_source("/Game/Content/Training/Foo.uasset"), "Training");
        assert_eq!(classify_source("/Game/Content/Scenarios/Foo.uasset"), "Scenario");
        assert_eq!(classify_source("/Game/Content/Random/Foo.uasset"), "Timetable");
        // ServiceMode wins even when also under Scenarios.
        assert_eq!(
            classify_source("/Game/Content/Scenarios/ServiceMode/Foo.uasset"),
            "Timetable"
        );
        // Backslash path separator gets normalized.
        assert_eq!(
            classify_source(r"C:\TSW\Content\Training\Foo.uasset"),
            "Training"
        );
    }
}
