//! Port of hud-go's `internal/output/package_format.go` — the wire schema
//! the extractor writes into route export zips. Same JSON field names so
//! either project's zips round-trip through either project's importer.

use serde::{Deserialize, Serialize};

/// Two-axis lat/lng pair for a service polyline. Mirrors hud-go's
/// `ServiceCoord`. `Height` is preserved at runtime by the path builder
/// but NOT serialised — drops ~33% off the JSON blob.
#[derive(Debug, Clone, Default, Serialize, Deserialize)]
pub struct ServiceCoord {
    pub latitude:  f64,
    pub longitude: f64,
    #[serde(skip)]
    pub height:    f64,
}

/// One row in the `csvData` array. Matches hud-go's `CsvDataRow`.
#[derive(Debug, Clone, Default, Serialize, Deserialize)]
pub struct CsvDataRow {
    pub action:           String,
    #[serde(default, skip_serializing_if = "String::is_empty")]
    pub cargo:            String,
    #[serde(default, skip_serializing_if = "String::is_empty")]
    pub waiting_time:     String,
    /// `"automatic"` for seeded rows, `null` otherwise. Mirrors Go's
    /// `any` field — kept as `Option<String>` here for type safety.
    pub coord_source:     Option<String>,
    pub details:          String,
    pub index:            i32,
    pub latitude:         String,
    pub location:         String,
    pub longitude:        String,
    pub structure:        String,
    pub structure_number: String,
    pub time1:            String,
    pub time2:            String,
}

/// One row in the `timetable` array. Matches hud-go's `TimetableRow`.
#[derive(Debug, Clone, Default, Serialize, Deserialize)]
pub struct TimetableRow {
    pub arrival:          String,
    pub coord_source:     Option<String>,
    pub departure:        String,
    pub index:            i32,
    #[serde(default, skip_serializing_if = "String::is_empty")]
    pub latitude:         String,
    pub location:         String,
    #[serde(default, skip_serializing_if = "String::is_empty")]
    pub longitude:        String,
    pub structure:        String,
    pub structure_number: String,
}

/// Mirror of `uasset_rvd::ElectrificationSpec` in the wire schema.
/// Duplicated rather than re-exported so the output crate doesn't pull
/// on the parser types.
#[derive(Debug, Clone, Default, Serialize, Deserialize)]
pub struct ElectrificationSpec {
    #[serde(default, skip_serializing_if = "String::is_empty")]
    pub current:      String,
    #[serde(default, skip_serializing_if = "String::is_empty")]
    pub pickup_side:  String,
    #[serde(default, skip_serializing_if = "is_zero_i32")]
    pub voltage_v:    i32,
    #[serde(default, skip_serializing_if = "is_zero_i32")]
    pub frequency_hz: i32,
}
fn is_zero_i32(v: &i32) -> bool { *v == 0 }
fn is_zero_f32(v: &f32) -> bool { *v == 0.0 }
fn is_false(v: &bool) -> bool { !*v }

/// One car slot in a service's consist. `vehicle_id` is the GUID the
/// game reports via the live API.
#[derive(Debug, Clone, Default, Serialize, Deserialize)]
pub struct ConsistVehicle {
    pub vehicle_id:          String,
    #[serde(default, skip_serializing_if = "String::is_empty")]
    pub rail_vehicle_class:  String,
    #[serde(default, skip_serializing_if = "String::is_empty")]
    pub friendly_name:       String,
    #[serde(default, skip_serializing_if = "String::is_empty")]
    pub livery_id:           String,
    #[serde(default, skip_serializing_if = "String::is_empty")]
    pub vehicle_category:    String,
    pub length_m:            f64,
    #[serde(default, skip_serializing_if = "is_false")]
    pub is_lead:             bool,
    #[serde(default, skip_serializing_if = "is_false")]
    pub is_flipped:          bool,
    #[serde(default, skip_serializing_if = "is_false")]
    pub is_electric:         bool,
    #[serde(default, skip_serializing_if = "is_zero_f32")]
    pub max_speed_kph:       f32,
    #[serde(default, skip_serializing_if = "is_zero_f32")]
    pub max_power_kw:        f32,
    #[serde(default, skip_serializing_if = "String::is_empty")]
    pub manufacturer_name:   String,
    #[serde(default, skip_serializing_if = "String::is_empty")]
    pub engine_description:  String,
    #[serde(default, skip_serializing_if = "String::is_empty")]
    pub type_description:    String,
    #[serde(default, skip_serializing_if = "String::is_empty")]
    pub thumbnail_asset_ref: String,
    #[serde(default, skip_serializing_if = "Vec::is_empty")]
    pub electrification:     Vec<ElectrificationSpec>,
}

/// One consist within a class entry.
#[derive(Debug, Clone, Default, Serialize, Deserialize)]
pub struct Consist {
    pub length_m:  f64,
    pub car_count: i32,
    pub vehicles:  Vec<ConsistVehicle>,
}

/// One drivable class entry the player can pick for a service.
/// `is_default` marks the class matching the formation's lead RVD; that
/// class's `consists` carries the full per-vehicle detail including
/// GUIDs.
#[derive(Debug, Clone, Default, Serialize, Deserialize)]
pub struct FormationClassEntry {
    pub class:      String,
    #[serde(default, skip_serializing_if = "is_false")]
    pub is_default: bool,
    pub consists:   Vec<Consist>,
}

/// Carries a non-canonical timetable binary's formation_name +
/// formations[] payload when multiple binaries declare the same service.
#[derive(Debug, Clone, Default, Serialize, Deserialize)]
pub struct AdditionalFormation {
    #[serde(default, skip_serializing_if = "String::is_empty")]
    pub formation_name: String,
    #[serde(default, skip_serializing_if = "Vec::is_empty")]
    pub formations:     Vec<FormationClassEntry>,
}

/// One entry in `route_<X>.json`'s top-level `train_classes` array.
/// Importers create one `train_classes` DB row per entry.
#[derive(Debug, Clone, Default, Serialize, Deserialize)]
pub struct RouteTrainClass {
    pub rail_vehicle_class:  String,
    #[serde(default, skip_serializing_if = "String::is_empty")]
    pub friendly_name:       String,
    #[serde(default, skip_serializing_if = "String::is_empty")]
    pub livery_id:           String,
    #[serde(default, skip_serializing_if = "String::is_empty")]
    pub vehicle_category:    String,
    pub drivable:            bool,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub length_m:            Option<f32>,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub is_electric:         Option<bool>,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub max_speed_kph:       Option<f32>,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub max_power_kw:        Option<f32>,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub powered_axle_count:  Option<u32>,
    #[serde(default, skip_serializing_if = "String::is_empty")]
    pub manufacturer_name:   String,
    #[serde(default, skip_serializing_if = "String::is_empty")]
    pub engine_description:  String,
    #[serde(default, skip_serializing_if = "String::is_empty")]
    pub type_description:    String,
    #[serde(default, skip_serializing_if = "String::is_empty")]
    pub thumbnail_rel:       String,
    #[serde(default, skip_serializing_if = "Vec::is_empty")]
    pub electrification:     Vec<ElectrificationSpec>,
}

/// One service's shareable per-service JSON.
/// Field order = identity / metadata first, bulk arrays at the bottom —
/// same as hud-go's `PackageService`.
#[derive(Debug, Clone, Default, Serialize, Deserialize)]
pub struct PackageService {
    // ---- identity / taxonomy ----
    #[serde(rename = "serviceName")]
    pub service_name:         String,
    pub current_service_name: String,
    pub description:          String,
    #[serde(rename = "serviceType")]
    pub service_type:         String,
    pub source:               String,
    pub playable:             bool,
    pub hidden:               bool,
    pub bound:                serde_json::Value,

    // ---- location ----
    #[serde(rename = "routeName")]
    pub route_name:          String,
    #[serde(rename = "countryName")]
    pub country_name:        String,
    #[serde(rename = "sectionNames")]
    pub section_names:       Vec<String>,
    #[serde(default, skip_serializing_if = "String::is_empty")]
    pub cross_pak_reference_name: String,
    #[serde(default, skip_serializing_if = "is_zero_f64")]
    pub origin_lat:          f64,
    #[serde(default, skip_serializing_if = "is_zero_f64")]
    pub origin_lng:          f64,

    // ---- scheduling ----
    #[serde(rename = "startTime")]
    pub start_time: String,
    pub duration:   String,

    // ---- formation / consist ----
    pub formations:           Vec<FormationClassEntry>,
    #[serde(rename = "conductorCompatible")]
    pub conductor_compatible: bool,
    #[serde(default, skip_serializing_if = "String::is_empty")]
    pub formation_name:       String,
    #[serde(default, skip_serializing_if = "Vec::is_empty")]
    pub additional_formations: Vec<AdditionalFormation>,

    // ---- counters / identifiers ----
    #[serde(rename = "totalPoints")]
    pub total_points:  i32,
    #[serde(rename = "totalMarkers")]
    pub total_markers: i32,

    // ---- recording / contributor metadata ----
    #[serde(rename = "recordingMode")]
    pub recording_mode:           String,
    pub contributor:              serde_json::Value,
    pub coordinates_contributor:  serde_json::Value,
    pub coordinates_source:       String,

    // ---- bulk arrays ----
    #[serde(rename = "csvData")]
    pub csv_data:    Vec<CsvDataRow>,
    pub timetable:   Vec<TimetableRow>,
    pub coordinates: Vec<ServiceCoord>,
    pub markers:     Vec<serde_json::Value>,
}
fn is_zero_f64(v: &f64) -> bool { *v == 0.0 }

#[cfg(test)]
mod tests {
    use super::*;

    /// Round-trip a minimal PackageService through serde to confirm the
    /// JSON field names match the hud-go wire format (camelCase mix).
    #[test]
    fn package_service_keys() {
        let svc = PackageService {
            service_name: "T2".into(),
            service_type: "passenger".into(),
            start_time:   "06:00:00".into(),
            duration:     "01:00:00".into(),
            route_name:   "WCMLSouth".into(),
            country_name: "UK".into(),
            ..Default::default()
        };
        let j = serde_json::to_string(&svc).unwrap();
        // The four renamed fields should appear with their hud-go names.
        assert!(j.contains("\"serviceName\":\"T2\""),  "expected serviceName key: {j}");
        assert!(j.contains("\"serviceType\":\"passenger\""), "expected serviceType key: {j}");
        assert!(j.contains("\"startTime\":\"06:00:00\""), "expected startTime key: {j}");
        assert!(j.contains("\"routeName\":\"WCMLSouth\""), "expected routeName key: {j}");
        // ServiceCoord skips `height` from the wire.
        let c = ServiceCoord { latitude: 1.0, longitude: 2.0, height: 99.0 };
        let cj = serde_json::to_string(&c).unwrap();
        assert!(!cj.contains("height"), "height should be skipped, got: {cj}");
    }
}
