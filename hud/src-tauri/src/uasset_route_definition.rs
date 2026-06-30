//! Port of hud-go's `route_definition_cooked.go` + the matching struct
//! definition in `route_definition.go`. Reads a `<X>RouteDefinition.uasset`
//! directly via the binary walker (no UAssetGUI roundtrip) and returns
//! enough metadata to populate the `routes` row in the catalog:
//! display name, country, stable codename, cross-pak reference name.

use std::path::Path;

use crate::uasset::{Reader, Umap};

#[derive(Debug, Clone, Default, serde::Serialize)]
pub struct RouteDefinition {
    pub asset_path:               String,
    /// Stable internal codename (e.g. `"WCMLSouth"`). Empty when the
    /// asset doesn't carry the property.
    pub stat_tracking_name:       String,
    /// Canonical display name as it appears in the route picker
    /// (e.g. `"WCML South - London Euston to Milton Keynes"`).
    pub display_name:             String,
    /// Short country code as it appears in the data: `"UK"`, `"US"`,
    /// `"FR"`, etc. Pulled from the nested `RouteDetails.Country`
    /// TextProperty.
    pub country_code:             String,
    /// The pak's own asset-mount path (`"EustonMiltonKeynes"` for WCML
    /// South, `"TrainingCentre"` for the tutorial environment).
    /// Recovered from the NameMap by looking for `/<X>/RouteDefinition/`.
    pub cross_pak_reference_name: String,
}

/// Read a cooked `<Codename>RouteDefinition.uasset` and return the
/// populated struct. Errors when the file isn't a RouteDefinition (no
/// `RouteDetails` struct in any export) so directory-walking callers can
/// filter out the level / reward sub-assets that share the basename.
pub fn parse(uasset_path: &Path) -> Result<RouteDefinition, String> {
    let u = Umap::read(uasset_path)
        .map_err(|e| format!("read uasset {}: {e}", uasset_path.display()))?;
    if u.exports.is_empty() {
        return Err(format!("no exports in {}", uasset_path.display()));
    }

    // Walk every export, take the first one that carries a RouteDetails
    // struct as the canonical RouteDefinition. Most paks have it at
    // Exports[0]; the few that bundle level + RouteDefinition together
    // need this scan.
    let mut found: Option<RouteDefinition> = None;
    for exp in &u.exports {
        let Some(slice) = u.property_slice(exp) else { continue };
        let mut pr = Reader::new(slice, &u.names);
        let mut row = RouteDefinition {
            asset_path: uasset_path.to_string_lossy().into_owned(),
            ..Default::default()
        };
        if read_export(&mut pr, &mut row) {
            found = Some(row);
            break;
        }
    }
    let mut out = found.ok_or_else(|| format!(
        "not a RouteDefinition (no RouteDetails) {}",
        uasset_path.display()
    ))?;
    out.cross_pak_reference_name = extract_cross_pak_reference_name(&u.names);
    Ok(out)
}

/// Walk one export's property stream. Mirrors hud-go's
/// `readRouteDefinitionExport` line-for-line, returning `true` iff a
/// RouteDetails struct was seen — that's the discriminator between "this
/// export IS the route definition" and "this is a sub-asset bundled in
/// the same pak".
fn read_export(pr: &mut Reader<'_>, out: &mut RouteDefinition) -> bool {
    let mut saw_details = false;
    while pr.remaining() > 8 {
        let Some(t) = pr.read_tag() else { break };
        let dp = pr.tell();
        let mut consumed_via_ftext = false;

        match t.name.as_str() {
            "DisplayName" if t.ptype == "TextProperty" => {
                out.display_name = pr.ftext(t.size as usize);
                consumed_via_ftext = true;
            }
            "StatTrackingName" => {
                match t.ptype.as_str() {
                    "NameProperty" => { out.stat_tracking_name = pr.fname(); }
                    "StrProperty"  => { out.stat_tracking_name = pr.fstr(); }
                    _ => {}
                }
            }
            "RouteDetails" if t.ptype == "StructProperty" => {
                saw_details = true;
                // Recurse into the RouteDetails struct's own None-
                // terminated tag run. The inner stream is a sub-slice
                // of the parent's data, sharing the same NameMap.
                let inner_slice = &pr.data[dp..dp + t.size as usize];
                let mut inner = Reader::new(inner_slice, pr.name_map);
                while inner.remaining() > 8 {
                    let Some(it) = inner.read_tag() else { break };
                    let idp = inner.tell();
                    if it.name == "Country" && it.ptype == "TextProperty" {
                        out.country_code = inner.ftext(it.size as usize);
                        continue;
                    }
                    inner.seek(idp + it.size as usize);
                }
            }
            _ => {}
        }
        if !consumed_via_ftext {
            pr.seek(dp + t.size as usize);
        }
    }
    saw_details
}

/// Pull the cross-pak reference name out of the NameMap. Mirrors
/// hud-go's `extractCrossPakReferenceName`: look for an entry of the
/// form `/<X>/RouteDefinition/...` and return `<X>`.
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

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn cross_pak_extraction() {
        let names = vec![
            "/Game/Foo/Bar".to_string(),
            "/EustonMiltonKeynes/RouteDefinition/EMK".to_string(),
            "/OtherStuff/Path".to_string(),
        ];
        assert_eq!(extract_cross_pak_reference_name(&names), "EustonMiltonKeynes");

        // No matching marker → empty.
        let none = vec!["/Game/Foo/Bar".to_string(), "/Quux/SomethingElse/Hey".to_string()];
        assert_eq!(extract_cross_pak_reference_name(&none), "");

        // Marker present but in the wrong position → empty.
        let weird = vec!["/RouteDefinition/Game/Foo".to_string()];
        assert_eq!(extract_cross_pak_reference_name(&weird), "");
    }
}
