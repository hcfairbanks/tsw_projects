//! Port of hud-go's `scenario_definition_cooked.go` + struct definition
//! in `scenario_definition.go`. Reads a `*_Definition.uasset` (scenario
//! or tutorial) and extracts the catalog fields: display name, internal
//! codename, description, scenario type, start/end location tags,
//! designer estimates.

use std::path::Path;

use crate::uasset::{Reader, Umap};

#[derive(Debug, Clone, Default, serde::Serialize)]
pub struct ScenarioDefinition {
    pub asset_path:           String,
    pub display_name:         String,  // canonical, e.g. "Navigation & Interaction"
    pub plaintext_name:       String,  // codename, e.g. "OnFootNavigation"
    pub description:          String,  // English description text
    pub scenario_type:        String,  // "Tutorial", "Scenario", "Career", …
    pub start_location_tag:   String,
    pub end_location_tag:     String,
    pub minutes_to_complete:  i32,
    pub difficulty_rating:    i32,
}

/// Read a cooked scenario/tutorial Definition asset. Errors when the
/// asset has no `DisplayName` property — that's the discriminator for
/// "not a Definition asset, skip" so directory-walking callers can keep
/// the same skip-on-error loop they use today.
pub fn parse(uasset_path: &Path) -> Result<ScenarioDefinition, String> {
    let u = Umap::read(uasset_path)
        .map_err(|e| format!("read uasset {}: {e}", uasset_path.display()))?;
    if u.exports.is_empty() {
        return Err(format!("no exports in {}", uasset_path.display()));
    }
    for exp in &u.exports {
        let Some(slice) = u.property_slice(exp) else { continue };
        let mut pr = Reader::new(slice, &u.names);
        let mut candidate = ScenarioDefinition {
            asset_path: uasset_path.to_string_lossy().into_owned(),
            ..Default::default()
        };
        if read_export(&mut pr, &mut candidate) {
            return Ok(candidate);
        }
    }
    Err(format!(
        "not a Definition asset (no DisplayName) {}",
        uasset_path.display()
    ))
}

fn read_export(pr: &mut Reader<'_>, out: &mut ScenarioDefinition) -> bool {
    let mut saw_display_name = false;
    while pr.remaining() > 8 {
        let Some(t) = pr.read_tag() else { break };
        let dp = pr.tell();
        let mut handled_seek = false;

        match t.name.as_str() {
            "DisplayName" if t.ptype == "TextProperty" => {
                out.display_name = pr.ftext(t.size as usize).trim().to_string();
                saw_display_name = !out.display_name.is_empty();
                handled_seek = true;
            }
            "Description" if t.ptype == "TextProperty" => {
                out.description = pr.ftext(t.size as usize).trim().to_string();
                handled_seek = true;
            }
            "PlaintextName" => {
                match t.ptype.as_str() {
                    "NameProperty" => { out.plaintext_name = pr.fname(); }
                    "StrProperty"  => { out.plaintext_name = pr.fstr();  }
                    _ => {}
                }
            }
            "ScenarioType" if t.ptype == "ByteProperty" || t.ptype == "EnumProperty" => {
                let v = pr.fname();
                out.scenario_type = strip_enum_prefix(&v);
            }
            "StartLocationTag" => {
                match t.ptype.as_str() {
                    "NameProperty" => { out.start_location_tag = pr.fname(); }
                    "StrProperty"  => { out.start_location_tag = pr.fstr();  }
                    _ => {}
                }
            }
            "EndLocationTag" => {
                match t.ptype.as_str() {
                    "NameProperty" => { out.end_location_tag = pr.fname(); }
                    "StrProperty"  => { out.end_location_tag = pr.fstr();  }
                    _ => {}
                }
            }
            "MinutesToComplete" if t.ptype == "IntProperty" => {
                out.minutes_to_complete = pr.i32();
            }
            "DifficultyRating" if t.ptype == "IntProperty" => {
                out.difficulty_rating = pr.i32();
            }
            _ => {}
        }
        if !handled_seek {
            pr.seek(dp + t.size as usize);
        }
    }
    saw_display_name
}

fn strip_enum_prefix(v: &str) -> String {
    match v.rfind("::") {
        Some(i) => v[i + 2..].to_string(),
        None    => v.to_string(),
    }
}
