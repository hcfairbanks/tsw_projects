//! Codename-derived route fallbacks. Port of the parts of hud-go's
//! `internal/output/package_names.go` and `internal/pak/discover.go`
//! that let a route be named + countried purely from its pak codename
//! when the `<X>RouteDefinition.uasset` is missing or unparseable.
//!
//! Used by the extractor orchestrator (`extractor_pipeline::run_pak`)
//! to synthesise a fallback `RouteDefinition` so cargo / wagon / older
//! DLC paks that ship no parseable RouteDefinition still produce a
//! route row + per-service timetable rows, instead of being skipped.

use std::path::Path;

/// Derive a route codename from a `.pak` filename. Mirrors hud-go's
/// `routeNameFromPak` / `routeNameFromCoredata` (flat + coredata
/// layouts), with a parent-directory fallback for the nested layout.
///
///   * `TS2Prototype-WindowsNoEditor-IsleOfWight.pak`            → `IsleOfWight`
///   * `TS2Prototype-WindowsNoEditor-TrainingCentre-coredata.pak`→ `TrainingCentre`
///   * `<RouteName>/<RouteName>-WindowsNoEditor.pak`             → `<RouteName>` (parent dir)
pub fn codename_from_pak(pak_path: &Path) -> String {
    let stem = pak_path.file_stem().and_then(|s| s.to_str()).unwrap_or("");
    const PREFIX: &str = "TS2Prototype-WindowsNoEditor-";
    if let Some(rest) = stem.strip_prefix(PREFIX) {
        // coredata variant carries a trailing `-coredata` marker.
        return rest.strip_suffix("-coredata").unwrap_or(rest).to_string();
    }
    // Nested layout: the codename is the containing directory's name
    // (Go uses `e.Name()` of the DLC subdirectory). Skip the generic
    // container dirs so we don't name a route "DLC" / "Paks".
    if let Some(parent) = pak_path.parent().and_then(|p| p.file_name()).and_then(|s| s.to_str()) {
        if !parent.is_empty() && parent != "DLC" && parent != "Paks" {
            return parent.to_string();
        }
    }
    stem.to_string()
}

/// Legacy fallback display name for a codename whose RouteDefinition
/// couldn't be read — splits CamelCase, e.g. `"WCMLSouth"` → `"WCML
/// South"`, `"MannheimKaiserslautern"` → `"Mannheim Kaiserslautern"`.
/// Direct port of hud-go's `RouteDisplayName` → `splitCamelCase`.
pub fn route_display_name(codename: &str) -> String {
    split_camel_case(codename)
}

/// Byte-for-byte port of hud-go's `splitCamelCase`. Inserts a space
/// before an uppercase rune that follows a non-uppercase rune, and
/// before an uppercase rune that sits at the start of a new word inside
/// an acronym run (UPPER UPPER lower → split before the second UPPER).
fn split_camel_case(s: &str) -> String {
    let runes: Vec<char> = s.chars().collect();
    let mut out = String::with_capacity(s.len() + 4);
    for (i, &r) in runes.iter().enumerate() {
        if i > 0 && r.is_uppercase() && !runes[i - 1].is_uppercase() {
            out.push(' ');
        } else if i > 0
            && i + 1 < runes.len()
            && r.is_uppercase()
            && runes[i - 1].is_uppercase()
            && runes[i + 1].is_lowercase()
        {
            out.push(' ');
        }
        out.push(r);
    }
    out
}

/// Force an ISO country code for paks where the RouteDefinition's
/// Country is wrong, missing, or too coarse. Always wins over the data.
/// Direct port of hud-go's `codenameCountryOverride` map.
/// Paks that should fold into an existing parent route as a named section
/// rather than becoming their own route entry. Returns
/// `(parent_cross_pak_reference_name, section_name)`.
///
/// Some DLCs ship only timetable / scenario services that run on another DLC's
/// map (no map of their own). TSW models them as separate "routes", but they're
/// really a section of the parent. Folding them keeps re-extraction consistent
/// with the user's chosen consolidation instead of recreating a stray route.
/// e.g. "Diesel Legends of the Great Western" (GreatWesternBlue) is 1970s
/// services on the Great Western Express map (PaddingtonReading).
/// `(parent_cross_pak_reference_name, section_name, collision_suffix)`. The
/// suffix disambiguates services whose names collide with one already on the
/// parent route (TSW reuses generic names like "PlayerService" across DLCs) so
/// folding never overwrites the parent's own services — only colliding names
/// get the suffix; unique ones stay clean.
pub fn fold_into_section(codename: &str) -> Option<(&'static str, &'static str, &'static str)> {
    match codename {
        "GreatWesternBlue" => Some((
            "PaddingtonReading",
            "Diesel Legends of the Great Western",
            "Diesel Legends",
        )),
        _ => None,
    }
}

pub fn country_override_for_codename(codename: &str) -> Option<&'static str> {
    match codename {
        "TrainingCentre"              => Some("DE"),
        "EastCoastMainline"           => Some("GB"),
        "MetrolinkAntelopeValleyLine" => Some("US"),
        "TSW2TorontoIndustrial"       => Some("CA"),
        "TSW2PaddingtonReading"       => Some("GB"), // Great Western Express
        _ => None,
    }
}

/// Normalise a raw TSW `RouteDetails.Country` value (short codes, full
/// English names, local-language names, regional buckets) to its ISO
/// 3166-1 alpha-2 equivalent. Mirrors hud-go's `tswToISO` /
/// `CountryISOFromCode`. Unknown values pass through unchanged.
pub fn country_iso_from_code(code: &str) -> String {
    if code.is_empty() {
        return String::new();
    }
    let iso = match code {
        "UK" | "United Kingdom"                                  => "GB",
        "US" | "USA" | "United States" | "North America"         => "US",
        "DE" | "Germany" | "Deutschland"                         => "DE",
        "FR" | "France"                                          => "FR",
        "CH" | "Switzerland"                                     => "CH",
        "AT" | "Austria"                                         => "AT",
        "IT" | "Italy"                                           => "IT",
        "NL" | "Netherlands"                                     => "NL",
        "CZ" | "Czech Republic"                                  => "CZ",
        "CA" | "Canada"                                          => "CA",
        other => return other.to_string(),
    };
    iso.to_string()
}

/// Strip a trailing `" Gameplay"` / `"_Gameplay"` tag that TSW's older
/// paks append to a `.uplugin` Description / FriendlyName. Idempotent.
/// Port of hud-go's `trimGameplaySuffix`.
pub fn trim_gameplay_suffix(s: &str) -> String {
    let mut t = s.trim();
    for suffix in ["_Gameplay", " Gameplay"] {
        if let Some(stripped) = t.strip_suffix(suffix) {
            t = stripped;
        }
    }
    t.trim().to_string()
}

/// Decide whether a `.uplugin` Description is too codename-shaped to use
/// as a user-facing DLC label. Port of hud-go's `isJunkDLCName`: empty →
/// junk; underscore in body (verbatim codename like `BPE_AMTK_Acela`) →
/// junk; equals the (trimmed) FriendlyName (which is conventionally the
/// codename) → junk.
pub fn is_junk_dlc_name(description: &str, friendly_name: &str) -> bool {
    if description.is_empty() {
        return true;
    }
    if description.contains('_') {
        return true;
    }
    if !friendly_name.is_empty() && description == friendly_name {
        return true;
    }
    false
}

#[cfg(test)]
mod tests {
    use super::*;
    use std::path::PathBuf;

    #[test]
    fn codename_flat_coredata_nested() {
        assert_eq!(
            codename_from_pak(&PathBuf::from("/x/DLC/TS2Prototype-WindowsNoEditor-IsleOfWight.pak")),
            "IsleOfWight"
        );
        assert_eq!(
            codename_from_pak(&PathBuf::from(
                "/x/Paks/TS2Prototype-WindowsNoEditor-TrainingCentre-coredata.pak"
            )),
            "TrainingCentre"
        );
        // Nested: codename is the parent dir, not the generic container.
        assert_eq!(
            codename_from_pak(&PathBuf::from("/x/DLC/SandPatch/SandPatch-WindowsNoEditor.pak")),
            "SandPatch"
        );
    }

    #[test]
    fn camel_case_split() {
        assert_eq!(route_display_name("WCMLSouth"), "WCML South");
        assert_eq!(route_display_name("MannheimKaiserslautern"), "Mannheim Kaiserslautern");
        assert_eq!(route_display_name("SandPatch"), "Sand Patch");
        assert_eq!(route_display_name("TSW2PaddingtonReading"), "TSW2 Paddington Reading");
    }

    #[test]
    fn overrides_and_iso() {
        assert_eq!(country_override_for_codename("TrainingCentre"), Some("DE"));
        assert_eq!(country_override_for_codename("EastCoastMainline"), Some("GB"));
        assert_eq!(country_override_for_codename("Nope"), None);

        assert_eq!(country_iso_from_code("UK"), "GB");
        assert_eq!(country_iso_from_code("North America"), "US");
        assert_eq!(country_iso_from_code("Deutschland"), "DE");
        assert_eq!(country_iso_from_code(""), "");
        assert_eq!(country_iso_from_code("ZZ"), "ZZ");
    }

    #[test]
    fn uplugin_name_helpers() {
        assert_eq!(trim_gameplay_suffix("Cargo Line Intermodal Gameplay"), "Cargo Line Intermodal");
        assert_eq!(trim_gameplay_suffix("SHG_CL-I_Gameplay"), "SHG_CL-I");
        assert_eq!(trim_gameplay_suffix("  Spaced  "), "Spaced");

        // Real names pass.
        assert!(!is_junk_dlc_name("Cargo Line Intermodal", "SHG CL-I"));
        // Underscore body → codename → junk.
        assert!(is_junk_dlc_name("BPE_AMTK_Acela", ""));
        // Equals friendly → junk.
        assert!(is_junk_dlc_name("SHG CL-I", "SHG CL-I"));
        // Empty → junk.
        assert!(is_junk_dlc_name("", "Whatever"));
    }
}
