//! Port of hud-go's `rvd_cooked.go` + `rvd.go` struct definitions.
//!
//! Reads an `RVD_*.uasset` (RailVehicleDefinition) and extracts the
//! subset of fields the catalog stores per train class: friendly name,
//! manufacturer, performance numbers, electrification requirements, and
//! the thumbnail asset reference. Mirrors the cooked-binary walker in
//! `route_definition_cooked.go`-style: scan exports until one looks like
//! an RVD (matches at least one known property), bail.

use std::path::Path;

use crate::uasset::{Reader, Umap};

#[derive(Debug, Clone, Default, serde::Serialize)]
pub struct ElectrificationSpec {
    pub current:      String, // "OverheadWires", "ThirdRail", "FourthRail"…
    pub pickup_side:  String,
    pub voltage_v:    i32,
    pub frequency_hz: i32,    // 0 == DC
}

#[derive(Debug, Clone, Default, serde::Serialize)]
pub struct Rvd {
    pub asset_path:           String,
    pub rail_vehicle_class:   String,
    pub approximate_length_m: f32,
    pub drivable:             bool,
    pub vehicle_category:     String,
    pub friendly_name:        String,
    pub livery_id:            String,
    pub regions:              Vec<String>,
    pub substitutable_unit:   bool,
    pub has_guard_controls:   bool,
    pub service_types:        i32,
    pub is_electric:          bool,
    pub max_speed_kph:        f32,
    pub max_power_kw:         f32,
    pub powered_axle_count:   u32,
    pub manufacturer_name:    String,
    pub engine_description:   String,
    pub type_description:     String,
    pub thumbnail_asset_ref:  String,
    pub electrification:      Vec<ElectrificationSpec>,
}

/// Read an `RVD_*.uasset` and return the populated struct. Walks
/// every export until one matches at least one known property; never
/// errors on "no matching properties" — RVD assets shouldn't be paired
/// with weird filenames but if they are we'd rather return an empty
/// shell with `asset_path` set than fail the entire pak run.
pub fn parse(uasset_path: &Path) -> Result<Rvd, String> {
    let u = Umap::read(uasset_path)
        .map_err(|e| format!("read uasset {}: {e}", uasset_path.display()))?;
    if u.exports.is_empty() {
        return Err(format!("no exports in {}", uasset_path.display()));
    }
    let mut out = Rvd {
        asset_path: uasset_path.to_string_lossy().into_owned(),
        ..Default::default()
    };
    for exp in &u.exports {
        let Some(slice) = u.property_slice(exp) else { continue };
        let mut pr = Reader::new(slice, &u.names);
        if read_export(&mut pr, &mut out) {
            break;
        }
    }
    Ok(out)
}

/// Walk one export's property tag stream. Returns `true` if at least
/// one known property matched, so the caller's "skip non-RVD exports"
/// loop can stop early.
fn read_export(pr: &mut Reader<'_>, out: &mut Rvd) -> bool {
    let mut matched = false;
    while pr.remaining() > 8 {
        let Some(t) = pr.read_tag() else { break };
        let dp = pr.tell();
        let mut handled_seek = false;

        match t.name.as_str() {
            "RailVehicleClass" if t.ptype == "StrProperty" => {
                out.rail_vehicle_class = pr.fstr();
                matched = true;
            }
            "ApproximateLength" if t.ptype == "FloatProperty" => {
                out.approximate_length_m = pr.f32() / 100.0; // cm → m
                matched = true;
            }
            "bIsDrivable" if t.ptype == "BoolProperty" => {
                out.drivable = t.bool_val != 0;
                matched = true;
                handled_seek = true; // BoolProperty has zero-byte body
            }
            "VehicleCategory" if t.ptype == "ByteProperty" || t.ptype == "EnumProperty" => {
                let v = pr.fname();
                out.vehicle_category = strip_enum_prefix(&v);
                matched = true;
            }
            "FriendlyName" if t.ptype == "TextProperty" => {
                out.friendly_name = pr.ftext(t.size as usize).trim().to_string();
                matched = true;
                handled_seek = true; // ftext advances exactly `size`
            }
            "LiveryID" => {
                match t.ptype.as_str() {
                    "StrProperty"  => { out.livery_id = pr.fstr(); }
                    "NameProperty" => { out.livery_id = pr.fname(); }
                    _ => {}
                }
                matched = true;
            }
            "AvailableGeographicRegions"
                if (t.ptype == "ArrayProperty" || t.ptype == "SetProperty")
                    && t.inner_type == "NameProperty" =>
            {
                let body = &pr.data[dp..dp + t.size as usize];
                out.regions = read_regions_set(body, pr.name_map);
                matched = true;
            }
            "bIsSubstitutableUnit" if t.ptype == "BoolProperty" => {
                out.substitutable_unit = t.bool_val != 0;
                matched = true;
                handled_seek = true;
            }
            "bHasGuardModeControls" if t.ptype == "BoolProperty" => {
                out.has_guard_controls = t.bool_val != 0;
                matched = true;
                handled_seek = true;
            }
            "ServiceTypes" if t.ptype == "IntProperty" => {
                out.service_types = pr.i32();
                matched = true;
            }
            "bIsElectric" if t.ptype == "BoolProperty" => {
                out.is_electric = t.bool_val != 0;
                matched = true;
                handled_seek = true;
            }
            "MaximumSpeed" if t.ptype == "StructProperty" => {
                // SpeedQuantity, 4-byte body: single f32 in m/s → km/h.
                out.max_speed_kph = pr.f32() * 3.6;
                matched = true;
            }
            "PowerOutput" if t.ptype == "StructProperty" => {
                // PowerQuantity, 4-byte body: single f32 in watts → kW.
                out.max_power_kw = pr.f32() / 1000.0;
                matched = true;
            }
            "PoweredAxleCount" if t.ptype == "UInt32Property" => {
                out.powered_axle_count = pr.i32() as u32;
                matched = true;
            }
            "ManufacturerName" if t.ptype == "TextProperty" => {
                out.manufacturer_name = pr.ftext(t.size as usize).trim().to_string();
                matched = true;
                handled_seek = true;
            }
            "EngineDescription" if t.ptype == "TextProperty" => {
                out.engine_description = pr.ftext(t.size as usize).trim().to_string();
                matched = true;
                handled_seek = true;
            }
            "TypeDescription" if t.ptype == "TextProperty" => {
                out.type_description = pr.ftext(t.size as usize).trim().to_string();
                matched = true;
                handled_seek = true;
            }
            "ThumbnailTexture" if t.ptype == "SoftObjectProperty" => {
                // FName AssetPath (8 B), FString SubPath (4 B count == 0 in
                // observed data). Just the FName is enough for the caller
                // to resolve against the unpacked tree.
                out.thumbnail_asset_ref = pr.fname();
                matched = true;
            }
            "ElectrificationRequirements"
                if t.ptype == "ArrayProperty" && t.inner_type == "StructProperty" =>
            {
                let body = &pr.data[dp..dp + t.size as usize];
                out.electrification = read_electrification_array(body, pr.name_map);
                matched = true;
            }
            _ => {}
        }

        if !handled_seek {
            pr.seek(dp + t.size as usize);
        }
    }
    matched
}

/// Strip a UE enum prefix like `EVehicleCategory::Locomotive` → `Locomotive`.
fn strip_enum_prefix(v: &str) -> String {
    match v.rfind("::") {
        Some(i) => v[i + 2..].to_string(),
        None    => v.to_string(),
    }
}

/// Walk a SetProperty<NameProperty> body. Layout for TSW6 RVDs is:
///   4 B padding (observed 0), 4 B int32 count, count × FName (8 B each).
fn read_regions_set(body: &[u8], name_map: &[String]) -> Vec<String> {
    let mut r = Reader::new(body, name_map);
    if r.remaining() < 8 {
        return Vec::new();
    }
    r.skip(4); // padding
    let count = r.i32();
    if !(0..=1024).contains(&count) {
        return Vec::new();
    }
    let mut out = Vec::with_capacity(count as usize);
    for _ in 0..count {
        if r.remaining() < 8 { break }
        let v = r.fname();
        if !v.is_empty() { out.push(v); }
    }
    out
}

/// Walk an ArrayProperty<StructProperty> body of
/// `ElectrificationRequirements`. See the matching Go function for the
/// layout breakdown; the 53-byte preamble before the first element is
/// the cooked-binary FArrayProperty + inner FPropertyTag header.
fn read_electrification_array(body: &[u8], name_map: &[String]) -> Vec<ElectrificationSpec> {
    let mut r = Reader::new(body, name_map);
    if r.remaining() < 4 {
        return Vec::new();
    }
    let count = r.i32();
    // Mirror hud-go's `count <= 0 || count > 64` guard exactly: reject a
    // zero count up front rather than running the 53 bytes of inner-header
    // skips on an element-less body.
    if count <= 0 || count > 64 {
        return Vec::new();
    }
    // Inner FPropertyTag header for the array's StructProperty element.
    r.skip(8);  // arrayName FName
    r.skip(8);  // arrayType FName ("StructProperty")
    r.skip(4);  // total size
    r.skip(4);  // arrayIndex
    r.skip(8);  // structName FName
    r.skip(16); // struct guid
    r.skip(1);  // has-property-guid byte

    let mut out: Vec<ElectrificationSpec> = Vec::with_capacity(count as usize);
    for _ in 0..count {
        if r.remaining() < 8 { break }
        let mut spec = ElectrificationSpec::default();
        while r.remaining() > 8 {
            let Some(t) = r.read_tag() else { break };
            let dp = r.tell();
            match t.name.as_str() {
                "PowerSource" if t.ptype == "ByteProperty" || t.ptype == "EnumProperty" => {
                    let v = r.fname();
                    spec.current = strip_enum_prefix(&v);
                }
                "PickupSide" if t.ptype == "ByteProperty" || t.ptype == "EnumProperty" => {
                    let v = r.fname();
                    spec.pickup_side = strip_enum_prefix(&v);
                }
                "SupplyType" if t.ptype == "StructProperty" => {
                    let inner_body = &r.data[dp..dp + t.size as usize];
                    read_supply_type_struct(inner_body, name_map, &mut spec);
                }
                _ => {}
            }
            r.seek(dp + t.size as usize);
        }
        out.push(spec);
    }
    out
}

/// Inner `SupplyType` struct's tag list: pulls `Voltage` (V) + `Frequency` (Hz)
/// into the parent spec. Forgiving — the 107-byte body has unaccounted
/// padding between tags, so we scan until `None`.
fn read_supply_type_struct(body: &[u8], name_map: &[String], spec: &mut ElectrificationSpec) {
    let mut r = Reader::new(body, name_map);
    while r.remaining() > 8 {
        let Some(t) = r.read_tag() else { return };
        let dp = r.tell();
        match t.name.as_str() {
            "Voltage"   if t.ptype == "IntProperty" => { spec.voltage_v   = r.i32(); }
            "Frequency" if t.ptype == "IntProperty" => { spec.frequency_hz = r.i32(); }
            _ => {}
        }
        r.seek(dp + t.size as usize);
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn enum_prefix_stripping() {
        assert_eq!(strip_enum_prefix("EVehicleCategory::Locomotive"), "Locomotive");
        assert_eq!(strip_enum_prefix("EElectricType::OverheadWires"), "OverheadWires");
        assert_eq!(strip_enum_prefix("ThirdRail"), "ThirdRail"); // already bare
        assert_eq!(strip_enum_prefix(""), "");
    }
}
