//! Phase 10 extractor — Rust port of hud-go's internal/extractor + internal/pak.
//!
//! Shipping in slices:
//!   * 10.1: pak DISCOVERY — scan TSW6 install dirs for route pak files.
//!   * 10.2 (this slice): pak EXTRACTION via repak.exe shell-out. The trumank
//!     `repak` tool isn't on crates.io as a library, and TSW6 paks need its
//!     Oodle support; bundling/shelling matches what hud-go has been doing
//!     against the same binary the user already has installed.
//!   * 10.3+: UAsset / UMap parsers + DB writers.

use std::fs;
use std::path::{Path, PathBuf};
use std::process::Command;

/// One discovered route pak.
#[derive(Debug, Clone, serde::Serialize)]
pub struct DiscoveredRoute {
    /// Codename pulled from the pak filename (e.g. `IsleOfWight`,
    /// `TrainingCentre`). The stable key used for completed-marks and as a
    /// last-resort display fallback.
    pub name:      String,
    /// Human-facing DLC name resolved the same way hud-go's catalog does —
    /// from the pak's `<X>RouteDefinition` DisplayName, else the
    /// `*_Gameplay.uplugin` Description, else a CamelCase split of the
    /// codename. Populated by [`resolve_all_metadata`]; empty until then.
    #[serde(default)]
    pub display_name: String,
    /// ISO 3166-1 alpha-2 country code resolved from the RouteDefinition
    /// (+ codename override). Empty when unknown. Populated by
    /// [`resolve_all_metadata`].
    #[serde(default)]
    pub country_code: String,
    /// Absolute path to the pak file.
    pub pak_path:  String,
    /// Where this pak was found:
    ///   * `"flat"`     — `Content/DLC/TS2Prototype-WindowsNoEditor-<Name>.pak`
    ///   * `"nested"`   — `Content/DLC/<Name>/<Name>-WindowsNoEditor.pak`
    ///   * `"coredata"` — `Content/Paks/TS2Prototype-WindowsNoEditor-<Name>-coredata.pak`
    pub layout:    &'static str,
}

const PAK_PREFIX: &str = "TS2Prototype-WindowsNoEditor-";
const COREDATA_SUFFIX: &str = "-coredata";

/// Scan the TSW6 install directory for route paks. Mirrors hud-go's
/// `pak.DiscoverRoutes`. Returns rows in stable order (sorted by name then
/// path) so the UI doesn't shuffle between calls.
///
/// `tsw_root` is the directory the user pointed at in Settings — the
/// parent of `WindowsNoEditor/`.
pub fn discover_routes(tsw_root: &Path) -> Result<Vec<DiscoveredRoute>, String> {
    let content = tsw_root
        .join("WindowsNoEditor")
        .join("TS2Prototype")
        .join("Content");
    if !content.exists() {
        return Err(format!(
            "TSW Content directory not found at {} — set the correct TSW install path in Settings",
            content.display()
        ));
    }

    let mut routes = Vec::new();
    scan_dlc(&content.join("DLC"), &mut routes);
    scan_paks(&content.join("Paks"), &mut routes);

    // Display + use the pak paths in the Windows convention (back-slashes), so
    // the extractor table matches the settings paths. repak / PathBuf accept
    // either separator, so this is purely for consistency.
    for r in &mut routes {
        r.pak_path = crate::config::to_win_path(&r.pak_path);
    }
    routes.sort_by(|a, b| a.name.cmp(&b.name).then_with(|| a.pak_path.cmp(&b.pak_path)));
    Ok(routes)
}

fn scan_dlc(dlc_root: &Path, out: &mut Vec<DiscoveredRoute>) {
    let Ok(entries) = fs::read_dir(dlc_root) else { return };
    for entry in entries.flatten() {
        let path = entry.path();
        let Ok(ftype) = entry.file_type() else { continue };
        if ftype.is_dir() {
            // Nested layout: subdirectory contains pak file(s). Each pak
            // becomes its own route entry, named after the directory.
            let dir_name = match path.file_name().and_then(|s| s.to_str()) {
                Some(n) => n.to_string(),
                None => continue,
            };
            let Ok(sub_entries) = fs::read_dir(&path) else { continue };
            for sub in sub_entries.flatten() {
                let sp = sub.path();
                if !sp.extension().is_some_and(|e| e.eq_ignore_ascii_case("pak")) { continue }
                out.push(DiscoveredRoute {
                    name:     dir_name.clone(),
                    display_name: String::new(),
                    country_code: String::new(),
                    pak_path: sp.to_string_lossy().into_owned(),
                    layout:   "nested",
                });
            }
        } else if ftype.is_file() {
            // Flat layout: TS2Prototype-WindowsNoEditor-<Name>.pak
            let Some(filename) = path.file_name().and_then(|s| s.to_str()) else { continue };
            if let Some(name) = route_name_from_pak(filename) {
                out.push(DiscoveredRoute {
                    name,
                    display_name: String::new(),
                    country_code: String::new(),
                    pak_path: path.to_string_lossy().into_owned(),
                    layout:   "flat",
                });
            }
        }
    }
}

fn scan_paks(paks_root: &Path, out: &mut Vec<DiscoveredRoute>) {
    let Ok(entries) = fs::read_dir(paks_root) else { return };
    for entry in entries.flatten() {
        let path = entry.path();
        let Some(filename) = path.file_name().and_then(|s| s.to_str()) else { continue };
        if !path.is_file() { continue }
        if let Some(name) = route_name_from_coredata(filename) {
            out.push(DiscoveredRoute {
                name,
                display_name: String::new(),
                country_code: String::new(),
                pak_path: path.to_string_lossy().into_owned(),
                layout:   "coredata",
            });
        }
    }
}

/// `TS2Prototype-WindowsNoEditor-IsleOfWight.pak` → `"IsleOfWight"`.
/// Returns None for anything that doesn't match the route-pak shape.
fn route_name_from_pak(filename: &str) -> Option<String> {
    let stem = filename.strip_suffix(".pak")?;
    let name = stem.strip_prefix(PAK_PREFIX)?;
    if name.is_empty() { return None }
    Some(name.to_string())
}

/// `TS2Prototype-WindowsNoEditor-TrainingCentre-coredata.pak` →
/// `"TrainingCentre"`. Excludes the base-game pak (`...-WindowsNoEditor.pak`
/// with no `-<Name>-coredata` body) by requiring both prefix AND suffix.
fn route_name_from_coredata(filename: &str) -> Option<String> {
    let stem = filename.strip_suffix(".pak")?;
    let inner = stem.strip_prefix(PAK_PREFIX)?;
    let body  = inner.strip_suffix(COREDATA_SUFFIX)?;
    if body.is_empty() { return None }
    Some(body.to_string())
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn route_name_parsing() {
        // Flat-layout paks.
        assert_eq!(route_name_from_pak("TS2Prototype-WindowsNoEditor-IsleOfWight.pak").as_deref(), Some("IsleOfWight"));
        assert_eq!(route_name_from_pak("TS2Prototype-WindowsNoEditor-BostonSprinter.pak").as_deref(), Some("BostonSprinter"));
        // Coredata paks.
        assert_eq!(route_name_from_coredata("TS2Prototype-WindowsNoEditor-TrainingCentre-coredata.pak").as_deref(), Some("TrainingCentre"));
        // Base game pak — no name body, should be excluded.
        assert_eq!(route_name_from_pak("TS2Prototype-WindowsNoEditor.pak").as_deref(), None);
        assert_eq!(route_name_from_coredata("TS2Prototype-WindowsNoEditor.pak").as_deref(), None);
        // Wrong prefix.
        assert_eq!(route_name_from_pak("SomeOther-IsleOfWight.pak").as_deref(), None);
        // Wrong extension.
        assert_eq!(route_name_from_pak("TS2Prototype-WindowsNoEditor-IsleOfWight.txt").as_deref(), None);
    }
}

/// Resolve `tsw_root` from settings. When the user left the path blank we
/// surface the same canonical not-found error the discovery walker uses
/// so the Settings UI can show a single message.
pub fn resolve_tsw_root() -> Result<PathBuf, String> {
    let cfg = crate::config::Config::load();
    let raw = cfg.extractor_tsw_path.trim();
    if raw.is_empty() {
        return Err("Set the TSW install path in Settings first".into());
    }
    Ok(PathBuf::from(raw))
}

// =================================================================== repak
//
// hud-go shells out to repak.exe for pak extraction. We do the same — the
// trumank `repak` crate isn't on crates.io (the `repak` crate name on
// crates.io is unrelated), and TSW6 paks need Oodle support which the .exe
// has and shipping a Rust port of doesn't have.

#[derive(Debug, Clone, serde::Serialize)]
pub struct RepakInfo {
    /// Resolved absolute path to the binary, or "" when not found.
    pub path:   String,
    /// Where we looked it up: `"hud-resources"`, `"hud-exe-adjacent"`,
    /// `"PATH"`, or `"not-found"`.
    pub source: &'static str,
}

/// Find repak.exe in hud's standard locations + the user's PATH. Returns
/// an `Err` with installation instructions when nothing turns up so the
/// Settings UI can drop them in the user's lap.
pub fn find_repak() -> Result<RepakInfo, String> {
    if let Some(mut p) = locate_repak() {
        p.path = crate::config::to_win_path(&p.path); // Windows-style display
        return Ok(p);
    }
    Err(concat!(
        "repak.exe not found. Download it from ",
        "https://github.com/trumank/repak/releases and drop it into ",
        "hud/resources/ (or add it to your PATH)."
    )
    .into())
}

fn locate_repak() -> Option<RepakInfo> {
    let exe_name = if cfg!(windows) { "repak.exe" } else { "repak" };

    // 1) hud's own resources/ — this is where we'd ship a bundled copy.
    let res = crate::config::resources_dir().join(exe_name);
    if res.is_file() {
        return Some(RepakInfo {
            path:   res.to_string_lossy().into_owned(),
            source: "hud-resources",
        });
    }

    // 2) Next to hud.exe (release layout).
    if let Ok(exe) = std::env::current_exe() {
        if let Some(parent) = exe.parent() {
            let adj = parent.join(exe_name);
            if adj.is_file() {
                return Some(RepakInfo {
                    path:   adj.to_string_lossy().into_owned(),
                    source: "hud-exe-adjacent",
                });
            }
        }
    }

    // 3) PATH lookup. Walk the PATH env var by hand — no portable
    //    `which` in std, and we don't want to pull in a crate for one
    //    function.
    if let Some(path_env) = std::env::var_os("PATH") {
        for dir in std::env::split_paths(&path_env) {
            let cand = dir.join(exe_name);
            if cand.is_file() {
                return Some(RepakInfo {
                    path:   cand.to_string_lossy().into_owned(),
                    source: "PATH",
                });
            }
        }
    }
    None
}

/// Result of an unpack operation.
#[derive(Debug, Clone, serde::Serialize)]
pub struct UnpackResult {
    /// Where the pak was unpacked.
    pub dest_dir:    String,
    /// Number of files written under `dest_dir` (depth-first count, all
    /// regular files). 0 when repak reported success but the directory is
    /// empty — the caller should treat that as a soft failure.
    pub file_count:  u64,
    /// Repak's stdout (truncated to last ~4 KB) for UI / logging.
    pub stdout_tail: String,
    /// Repak's stderr (truncated to last ~4 KB).
    pub stderr_tail: String,
}

/// Extract `pak_path` to `dest_dir` using `repak.exe`. Mirrors hud-go's
/// `runRepak`:
///
///     repak.exe [--aes-key 0x<key>] unpack --output <dest> <pak>
///
/// `aes_key` is the raw hex key without the `0x` prefix; pass an empty
/// string for unencrypted paks (the TSW6 case for everything we've seen
/// so far). Creates `dest_dir` when it doesn't exist.
pub fn unpack_pak(pak_path: &Path, dest_dir: &Path, aes_key: &str) -> Result<UnpackResult, String> {
    if !pak_path.is_file() {
        return Err(format!("pak not found: {}", pak_path.display()));
    }
    fs::create_dir_all(dest_dir)
        .map_err(|e| format!("mkdir {}: {e}", dest_dir.display()))?;

    let repak = find_repak()?;
    let mut cmd = Command::new(&repak.path);
    if !aes_key.trim().is_empty() {
        cmd.arg("--aes-key").arg(format!("0x{}", aes_key.trim()));
    }
    cmd.arg("unpack")
        .arg("--output").arg(dest_dir)
        .arg(pak_path);
    // Suppress the console window the OS would otherwise pop for the
    // subprocess. Critical for "Load my DLCs" — without this the
    // console flashes once per pak, stealing focus from the app.
    #[cfg(target_os = "windows")]
    {
        use std::os::windows::process::CommandExt;
        const CREATE_NO_WINDOW: u32 = 0x0800_0000;
        cmd.creation_flags(CREATE_NO_WINDOW);
    }

    let output = cmd.output().map_err(|e| format!("spawn repak: {e}"))?;
    let stdout_tail = tail_bytes(&output.stdout, 4096);
    let stderr_tail = tail_bytes(&output.stderr, 4096);

    if !output.status.success() {
        return Err(format!(
            "repak exited {} — stderr: {}",
            output.status.code().map(|c| c.to_string()).unwrap_or_else(|| "?".into()),
            stderr_tail
        ));
    }

    let file_count = count_files(dest_dir);
    Ok(UnpackResult {
        dest_dir:    dest_dir.to_string_lossy().into_owned(),
        file_count,
        stdout_tail,
        stderr_tail,
    })
}

// ============================================================ scan metadata
//
// hud-go's catalog (`ScanPak`) resolves each pak's user-facing DLC name +
// country without a full unpack: `repak list` to find the route's
// `<X>RouteDefinition` (folder-based) or its `*_Gameplay.uplugin`, then
// `repak unpack -i <entry>` to pull just those few small assets and parse
// them. We mirror that here so the "Scan TSW install" list shows the same
// human names hud-go does (e.g. "Morristown Line: New York & Hoboken -
// Dover") instead of the bare codename. repak's index is unencrypted, so
// no AES key is needed (same as the full unpack path, which the frontend
// also invokes keyless).

/// Run repak capturing stdout, suppressing the console window on Windows.
fn run_repak_capture(repak_path: &str, args: &[&str]) -> Result<String, String> {
    let mut cmd = Command::new(repak_path);
    cmd.args(args);
    #[cfg(target_os = "windows")]
    {
        use std::os::windows::process::CommandExt;
        const CREATE_NO_WINDOW: u32 = 0x0800_0000;
        cmd.creation_flags(CREATE_NO_WINDOW);
    }
    let out = cmd.output().map_err(|e| format!("spawn repak: {e}"))?;
    if !out.status.success() {
        return Err(format!(
            "repak {:?} exited {}",
            args.first().copied().unwrap_or(""),
            out.status.code().map(|c| c.to_string()).unwrap_or_else(|| "?".into())
        ));
    }
    Ok(String::from_utf8_lossy(&out.stdout).into_owned())
}

/// Map a repak list entry (forward-slash path) to its on-disk location
/// under an unpack root, joining each segment so the OS separator is used.
fn entry_disk_path(root: &Path, entry: &str) -> PathBuf {
    entry.split('/').filter(|s| !s.is_empty()).fold(root.to_path_buf(), |p, seg| p.join(seg))
}

/// Resolve one pak's `(display_name, country_code)` the way hud-go's
/// `ScanPak` does: RouteDefinition first (folder-based, shortest-basename-
/// first, RouteDetails-gated), then `*_Gameplay.uplugin` Description, then
/// a CamelCase split of the codename. Country is ISO-normalised + the
/// per-codename override applied. Best-effort: any repak / parse failure
/// degrades to the codename fallback.
pub fn resolve_pak_metadata(pak_path: &Path, codename: &str) -> (String, String) {
    let override_country = crate::codename::country_override_for_codename(codename)
        .map(str::to_string)
        .unwrap_or_default();
    let fallback = || (crate::codename::route_display_name(codename), override_country.clone());

    let Ok(repak) = find_repak() else { return fallback() };
    let pak_str = pak_path.to_string_lossy().to_string();

    let list = match run_repak_capture(&repak.path, &["list", &pak_str]) {
        Ok(s) => s,
        Err(_) => return fallback(),
    };
    let entries: Vec<&str> = list.lines().map(str::trim).filter(|l| !l.is_empty()).collect();

    // Per-pak scratch root, keyed by the pak file stem so concurrent
    // resolutions never collide.
    let stem = pak_path.file_stem().and_then(|s| s.to_str()).unwrap_or("pak");
    let scratch = std::env::temp_dir().join(format!("hud_scan_{stem}"));
    let _ = fs::remove_dir_all(&scratch);
    let _ = fs::create_dir_all(&scratch);
    let scratch_str = scratch.to_string_lossy().to_string();

    let basename = |e: &str| e.rsplit('/').next().unwrap_or(e).to_string();

    // 1) RouteDefinition candidates: the canonical basename suffix plus any
    //    asset living in a `/routedefinition/` folder — some TSW6 routes name
    //    the definition `<Codename>Route.uasset` (e.g. MarseilleAvignonRoute →
    //    "LGV Méditerranée"), not `<X>RouteDefinition.uasset`. The parser's
    //    RouteDetails-struct check rejects the folder's `…Rewards` /
    //    `…LevelThresholds` / UI sub-assets, so casting the net wider is safe.
    //    Try canonical-suffixed files first (preserves existing routes), then
    //    folder fallbacks, shortest basename within each group.
    let mut rd: Vec<&str> = entries.iter().copied().filter(|e| {
        let lc = e.to_ascii_lowercase();
        lc.ends_with("routedefinition.uasset")
            || (lc.contains("/routedefinition/") && lc.ends_with(".uasset"))
    }).collect();
    rd.sort_by_key(|e| {
        let canonical = e.to_ascii_lowercase().ends_with("routedefinition.uasset");
        (!canonical, basename(e).len())
    });

    let mut display = String::new();
    let mut country = String::new();
    for entry in &rd {
        let uexp = format!("{}.uexp", entry.trim_end_matches(".uasset"));
        if run_repak_capture(&repak.path,
            &["unpack", "-f", "-o", &scratch_str, "-i", entry, "-i", &uexp, &pak_str]).is_err()
        {
            continue;
        }
        let on_disk = entry_disk_path(&scratch, entry);
        if let Ok(def) = crate::uasset_route_definition::parse(&on_disk) {
            if !def.display_name.is_empty() {
                display = def.display_name;
                country = crate::codename::country_iso_from_code(&def.country_code);
                break;
            }
        }
    }

    // 2) `*_Gameplay.uplugin` Description fallback for cargo / wagon / train
    //    paks that ship no RouteDefinition.
    if display.is_empty() {
        let mut gameplay: Vec<&str> = Vec::new();
        let mut others:   Vec<&str> = Vec::new();
        for e in &entries {
            let lc = e.to_ascii_lowercase();
            if lc.ends_with("_gameplay.uplugin") { gameplay.push(e); }
            else if lc.ends_with(".uplugin")     { others.push(e); }
        }
        for entry in gameplay.into_iter().chain(others) {
            if run_repak_capture(&repak.path,
                &["unpack", "-f", "-o", &scratch_str, "-i", entry, &pak_str]).is_err()
            {
                continue;
            }
            let Ok(raw) = fs::read_to_string(entry_disk_path(&scratch, entry)) else { continue };
            let Ok(doc) = serde_json::from_str::<serde_json::Value>(&raw) else { continue };
            let desc     = crate::codename::trim_gameplay_suffix(doc.get("Description").and_then(|v| v.as_str()).unwrap_or(""));
            let friendly = crate::codename::trim_gameplay_suffix(doc.get("FriendlyName").and_then(|v| v.as_str()).unwrap_or(""));
            if crate::codename::is_junk_dlc_name(&desc, &friendly) { continue }
            display = desc;
            break;
        }
    }

    let _ = fs::remove_dir_all(&scratch);

    if display.is_empty() {
        display = crate::codename::route_display_name(codename);
    }
    // Per-codename override always wins for country (mirrors ScanPak).
    if !override_country.is_empty() {
        country = override_country;
    }
    (display, country)
}

// ===================================================== overlay aggregation
//
// Port of hud-go's multi-pak merge: a parent route pak is extracted with
// every CHILD pak (no RouteDefinition) whose timetable/scenario assets
// reference it, unpacked into the SAME work dir, so one zip contains all
// their services. This is how "Boston Sprinter" (BostonProvidence +
// BostonProvidenceGameplayPack + BPEAcela) ends up as a single 1038-service
// zip rather than just the 800 in the base pak.
//
// hud-go computes this in its catalog (`scanCrossPakRefs`) + the handler's
// overlay loop (handler/extractor.go:574-588). We mirror both, caching the
// per-pak (has_route_def, cross_pak_refs) scan for the session so a
// "Load all DLCs" run doesn't re-scan every sibling per route.

use std::sync::{Mutex, OnceLock};

#[derive(Clone)]
struct PakCatalogEntry {
    has_route_def: bool,
    cross_pak_refs: Vec<String>,
}

fn pak_catalog_cache() -> &'static Mutex<std::collections::HashMap<String, PakCatalogEntry>> {
    static CACHE: OnceLock<Mutex<std::collections::HashMap<String, PakCatalogEntry>>> = OnceLock::new();
    CACHE.get_or_init(|| Mutex::new(std::collections::HashMap::new()))
}

/// Port of hud-go's `listTimetableShapeAssets`: every `.uasset` in the pak
/// whose path sits under a `/scenarios/`, `/timetables/`, `/timetable/`, or
/// `/training/` folder. These are byte-scanned for cross-pak references.
fn timetable_shape_assets(repak_path: &str, pak_str: &str) -> Vec<String> {
    let Ok(list) = run_repak_capture(repak_path, &["list", pak_str]) else { return Vec::new() };
    list.lines().map(str::trim).filter(|line| {
        if !line.ends_with(".uasset") { return false }
        let l = line.to_ascii_lowercase();
        l.contains("/scenarios/") || l.contains("/timetables/")
            || l.contains("/timetable/") || l.contains("/training/")
    }).map(str::to_string).collect()
}

/// Scan a pak's timetable/scenario assets for `/<X>/RouteDefinition/`
/// references and return the distinct `<X>` mount names. Port of hud-go's
/// `scanCrossPakRefs` + `crossPakPattern`. The mount name equals the parent
/// route's codename for the routes we aggregate (e.g. `BostonProvidence`).
fn pak_cross_pak_refs(repak_path: &str, pak_path: &Path) -> Vec<String> {
    let pak_str = pak_path.to_string_lossy().to_string();
    let candidates = timetable_shape_assets(repak_path, &pak_str);
    if candidates.is_empty() { return Vec::new() }

    let stem = pak_path.file_stem().and_then(|s| s.to_str()).unwrap_or("pak");
    let scratch = std::env::temp_dir().join(format!("hud_refs_{stem}"));
    let _ = fs::remove_dir_all(&scratch);
    let _ = fs::create_dir_all(&scratch);
    let scratch_str = scratch.to_string_lossy().to_string();

    // Batch the `-i` includes so we stay under Windows' ~32 KB argv cap.
    const MAX_ARGS_BYTES: usize = 4096;
    let mut i = 0;
    while i < candidates.len() {
        let mut args: Vec<String> = vec!["unpack".into(), "-f".into(), "-o".into(), scratch_str.clone()];
        let mut used = 0usize;
        while i < candidates.len() {
            let c = &candidates[i];
            let cost = c.len() + 4;
            if used + cost > MAX_ARGS_BYTES && used > 0 { break }
            args.push("-i".into());
            args.push(c.clone());
            used += cost;
            i += 1;
        }
        args.push(pak_str.clone());
        let argrefs: Vec<&str> = args.iter().map(String::as_str).collect();
        let _ = run_repak_capture(repak_path, &argrefs);
    }

    let mut set = std::collections::HashSet::new();
    let mut stack = vec![scratch.clone()];
    while let Some(d) = stack.pop() {
        let Ok(rd) = fs::read_dir(&d) else { continue };
        for entry in rd.flatten() {
            let path = entry.path();
            if path.is_dir() { stack.push(path); continue }
            if path.extension().and_then(|s| s.to_str()) != Some("uasset") { continue }
            if let Ok(bytes) = fs::read(&path) {
                extract_cross_pak_refs_from_bytes(&bytes, &mut set);
            }
        }
    }
    let _ = fs::remove_dir_all(&scratch);
    set.into_iter().collect()
}

/// Pull every `/<X>/RouteDefinition/` mount name out of raw asset bytes.
/// Manual byte scan equivalent to Go's `crossPakPattern`
/// (`/([A-Za-z0-9_\-]+)/RouteDefinition/`).
fn extract_cross_pak_refs_from_bytes(data: &[u8], out: &mut std::collections::HashSet<String>) {
    const MARKER: &[u8] = b"/RouteDefinition/";
    let mut i = 0;
    while i + MARKER.len() <= data.len() {
        if &data[i..i + MARKER.len()] == MARKER {
            // Walk back over the captured segment [A-Za-z0-9_-]+ ending at i.
            let mut start = i;
            while start > 0 {
                let c = data[start - 1];
                if c.is_ascii_alphanumeric() || c == b'_' || c == b'-' { start -= 1; } else { break }
            }
            if start < i && start > 0 && data[start - 1] == b'/' {
                if let Ok(s) = std::str::from_utf8(&data[start..i]) {
                    if !s.is_empty() { out.insert(s.to_string()); }
                }
            }
            i += MARKER.len();
        } else {
            i += 1;
        }
    }
}

/// Does this pak ship a parseable route-level RouteDefinition? Folder-based
/// (`/routedefinition/`), shortest-basename-first, RouteDetails-gated —
/// same discovery the extractor uses. Cheap: stops at the first parse.
fn pak_has_route_definition(repak_path: &str, pak_path: &Path) -> bool {
    let pak_str = pak_path.to_string_lossy().to_string();
    let Ok(list) = run_repak_capture(repak_path, &["list", &pak_str]) else { return false };
    let mut cands: Vec<String> = list.lines().map(str::trim).filter(|e| {
        let lc = e.to_ascii_lowercase();
        lc.ends_with("routedefinition.uasset")
            || (lc.contains("/routedefinition/") && lc.ends_with(".uasset"))
    }).map(str::to_string).collect();
    if cands.is_empty() { return false }
    // Canonical `…RouteDefinition.uasset` first, then `/routedefinition/`
    // folder fallbacks (e.g. `<Codename>Route.uasset`), shortest within each.
    cands.sort_by_key(|e| {
        let base = e.rsplit('/').next().unwrap_or(e).to_ascii_lowercase();
        (!base.ends_with("routedefinition.uasset"), base.len())
    });

    let stem = pak_path.file_stem().and_then(|s| s.to_str()).unwrap_or("pak");
    let scratch = std::env::temp_dir().join(format!("hud_rdchk_{stem}"));
    let _ = fs::remove_dir_all(&scratch);
    let _ = fs::create_dir_all(&scratch);
    let mut found = false;
    for entry in &cands {
        let uexp = format!("{}.uexp", entry.trim_end_matches(".uasset"));
        if run_repak_capture(repak_path,
            &["unpack", "-f", "-o", &scratch.to_string_lossy(), "-i", entry, "-i", &uexp, &pak_str]).is_err()
        {
            continue;
        }
        let on_disk = entry_disk_path(&scratch, entry);
        if crate::uasset_route_definition::parse(&on_disk).is_ok() { found = true; break; }
    }
    let _ = fs::remove_dir_all(&scratch);
    found
}

/// Session-cached catalog lookup for one pak.
fn pak_catalog_entry(repak_path: &str, pak_path: &Path) -> PakCatalogEntry {
    let key = pak_path.to_string_lossy().to_string();
    if let Some(e) = pak_catalog_cache().lock().unwrap().get(&key) {
        return e.clone();
    }
    let has_route_def = pak_has_route_definition(repak_path, pak_path);
    // Only children (no RouteDef) need their cross-pak refs for the parent
    // gather; computing them for parents is wasted work.
    let cross_pak_refs = if has_route_def { Vec::new() } else { pak_cross_pak_refs(repak_path, pak_path) };
    let entry = PakCatalogEntry { has_route_def, cross_pak_refs };
    pak_catalog_cache().lock().unwrap().insert(key, entry.clone());
    entry
}

/// Resolve the overlay-pak list for `target_pak`, mirroring hud-go's
/// handler overlay loop (extractor.go:574-588):
///
///   * target HAS a RouteDefinition (parent route) → overlay every sibling
///     pak that has NO RouteDefinition and whose cross-pak refs name the
///     target's codename. (Boston Sprinter: GameplayPack + BPEAcela.)
///   * target has NO RouteDefinition (orphan child) → overlay the parent
///     pak(s) it references, so origin + tiles resolve.
///
/// Returns absolute overlay pak paths. Best-effort: any repak failure
/// yields no overlays (the route still extracts from its own pak).
pub fn resolve_overlay_paks(target_pak: &Path) -> Vec<String> {
    let Ok(repak) = find_repak() else { return Vec::new() };
    let Ok(tsw_root) = resolve_tsw_root() else { return Vec::new() };
    let Ok(siblings) = discover_routes(&tsw_root) else { return Vec::new() };

    let target_codename = crate::codename::codename_from_pak(target_pak);
    let target_entry = pak_catalog_entry(&repak.path, target_pak);
    let mut overlays = Vec::new();

    if target_entry.has_route_def {
        for s in &siblings {
            let sp = Path::new(&s.pak_path);
            if sp == target_pak { continue }
            let e = pak_catalog_entry(&repak.path, sp);
            if e.has_route_def { continue }
            if e.cross_pak_refs.iter().any(|r| r == &target_codename) {
                overlays.push(s.pak_path.clone());
            }
        }
    } else {
        for s in &siblings {
            let sp = Path::new(&s.pak_path);
            if sp == target_pak { continue }
            let e = pak_catalog_entry(&repak.path, sp);
            if !e.has_route_def { continue }
            let sib_codename = crate::codename::codename_from_pak(sp);
            if target_entry.cross_pak_refs.iter().any(|r| r == &sib_codename) {
                overlays.push(s.pak_path.clone());
            }
        }
    }
    overlays
}

// ============================================ global train-class thumbnails
//
// hud-go's catalog scan decodes EVERY installed pak's drivable-RVD
// thumbnails (renderRVDThumbnails) into resources/images/train_classes/,
// then FixTrainClassThumbnails resolves each class's thumbnail_path. This
// is how shared locos that ship in non-route CONTENT packs (e.g. BNSF
// SD70ACe in NewJourneysCajonPass, which has no RouteDefinition and can't
// be extracted as a route) still get a canonical render in the Train
// Classes tab. Per-route extraction alone can't reach them.

/// Is this basename an RVD asset? `RVD_*` prefix OR `*_RVD.uasset` suffix.
fn is_rvd_asset(base: &str) -> bool {
    base.starts_with("RVD_") || base.ends_with("_RVD.uasset")
}

/// Decode one pak's drivable-RVD thumbnails into `out_dir`. When
/// `skip_existing` is true (Training Centre — gray placeholder renders) an
/// already-present PNG is left untouched so a canonical livery wins.
/// Returns the number of PNGs written.
fn scan_pak_thumbnails(repak_path: &str, pak_path: &Path, out_dir: &Path, skip_existing: bool) -> u64 {
    let pak_str = pak_path.to_string_lossy().to_string();
    let Ok(list) = run_repak_capture(repak_path, &["list", &pak_str]) else { return 0 };

    let mut entries: Vec<String> = Vec::new();
    let mut has_rvd = false;
    for line in list.lines().map(str::trim) {
        if !line.ends_with(".uasset") { continue }
        let base = line.rsplit('/').next().unwrap_or(line);
        if is_rvd_asset(base) {
            has_rvd = true;
            entries.push(line.to_string());
            entries.push(format!("{}.uexp", line.trim_end_matches(".uasset")));
            continue;
        }
        // Thumbnail textures live under FrontendAssets / FrontEnd / Data/RVD,
        // or carry icon/thumb in the name. Mirrors hud-go's catalog scan.
        let lc = line.to_ascii_lowercase();
        let base_lc = base.to_ascii_lowercase();
        if lc.contains("/frontendassets/") || lc.contains("/frontend/") || lc.contains("/data/rvd/")
            || base_lc.contains("icon") || base_lc.contains("thumb")
        {
            entries.push(line.to_string());
            entries.push(format!("{}.uexp", line.trim_end_matches(".uasset")));
        }
    }
    if !has_rvd { return 0 }

    let stem = pak_path.file_stem().and_then(|s| s.to_str()).unwrap_or("pak");
    let scratch = std::env::temp_dir().join(format!("hud_thumbs_{stem}"));
    let _ = fs::remove_dir_all(&scratch);
    let _ = fs::create_dir_all(&scratch);
    let scratch_str = scratch.to_string_lossy().to_string();

    const MAX_ARGS_BYTES: usize = 4096;
    let mut i = 0;
    while i < entries.len() {
        let mut args: Vec<String> = vec!["unpack".into(), "-f".into(), "-o".into(), scratch_str.clone()];
        let mut used = 0usize;
        while i < entries.len() {
            let c = &entries[i];
            let cost = c.len() + 4;
            if used + cost > MAX_ARGS_BYTES && used > 0 { break }
            args.push("-i".into());
            args.push(c.clone());
            used += cost;
            i += 1;
        }
        args.push(pak_str.clone());
        let argrefs: Vec<&str> = args.iter().map(String::as_str).collect();
        let _ = run_repak_capture(repak_path, &argrefs);
    }

    // Index textures by canonical package path (the form an RVD's
    // ThumbnailAssetRef uses) AND by basename as a fallback. Canonical-
    // first lookup is what keeps same-named textures from different
    // plugins/liveries apart — without it, "BNSF SD70ACe" could grab the
    // UP render, "BR 185.2 DB" the 185-5 render, and "Flying Scotsman"
    // gets shadowed by a same-named non-texture asset and decodes nothing.
    // Mirrors hud-go's indexByCanonical / indexByBase.
    let mut texture_by_canonical: std::collections::HashMap<String, PathBuf> = std::collections::HashMap::new();
    let mut texture_by_base: std::collections::HashMap<String, PathBuf> = std::collections::HashMap::new();
    let mut rvd_files: Vec<PathBuf> = Vec::new();
    let mut stack = vec![scratch.clone()];
    while let Some(d) = stack.pop() {
        let Ok(rd) = fs::read_dir(&d) else { continue };
        for entry in rd.flatten() {
            let path = entry.path();
            if path.is_dir() { stack.push(path); continue }
            if path.extension().and_then(|s| s.to_str()) != Some("uasset") { continue }
            let base = path.file_stem().and_then(|s| s.to_str()).unwrap_or("").to_string();
            if is_rvd_asset(path.file_name().and_then(|s| s.to_str()).unwrap_or("")) {
                rvd_files.push(path.clone());
            }
            texture_by_canonical.insert(crate::extractor_pipeline::canonical_rvd_path(&path), path.clone());
            texture_by_base.entry(base).or_insert(path);
        }
    }
    rvd_files.sort();

    let mut written = 0u64;
    let mut seen: std::collections::HashSet<String> = std::collections::HashSet::new();
    for rvd_path in &rvd_files {
        let Ok(rvd) = crate::uasset_rvd::parse(rvd_path) else { continue };
        if !rvd.drivable || rvd.thumbnail_asset_ref.is_empty() || rvd.friendly_name.is_empty() {
            continue;
        }
        // Strip the trailing `.<AssetName>` to get the canonical ref, then
        // resolve canonical-first, basename-fallback (hud-go's order).
        let ref_canon = rvd.thumbnail_asset_ref.rfind('.')
            .map(|i| &rvd.thumbnail_asset_ref[..i])
            .unwrap_or(&rvd.thumbnail_asset_ref);
        let tex_base = ref_canon.rsplit('/').next().unwrap_or(ref_canon);
        let Some(tex_path) = texture_by_canonical.get(ref_canon)
            .or_else(|| texture_by_base.get(tex_base)) else { continue };
        let fname = format!("{}.png", crate::uasset_texture::sanitise_thumbnail_name(&rvd.friendly_name));
        if !seen.insert(fname.clone()) { continue }
        let out = out_dir.join(&fname);
        if skip_existing && out.is_file() { continue }
        if crate::uasset_texture::extract_texture_to_png(tex_path, &out).is_ok() {
            written += 1;
        }
    }
    let _ = fs::remove_dir_all(&scratch);
    written
}

/// Decode every installed pak's drivable-RVD thumbnails into the shared
/// cache (canonical liveries first, Training Centre last and only filling
/// gaps), then run `fix_train_class_thumbnails` so the Train Classes tab
/// points each class at the right PNG. Port of hud-go's catalog scan +
/// FixTrainClassThumbnails. Returns total PNGs written.
pub fn rebuild_thumbnails_all_paks<F: Fn(&str) + Sync>(log: F) -> Result<u64, String> {
    use std::sync::atomic::{AtomicU64, AtomicUsize, Ordering};

    let repak = find_repak()?;
    let tsw_root = resolve_tsw_root()?;
    let paks = discover_routes(&tsw_root)?;
    if paks.is_empty() { return Ok(0) }

    let out_dir = std::env::current_exe().ok()
        .and_then(|p| p.parent().map(|p| p.to_path_buf()))
        .map(|p| p.join("resources").join("images").join("train_classes"))
        .unwrap_or_else(|| PathBuf::from("resources/images/train_classes"));
    fs::create_dir_all(&out_dir).map_err(|e| format!("mkdir {}: {e}", out_dir.display()))?;

    // Canonical paks first (parallel), Training Centre last so its gray
    // placeholder renders only fill gaps the real liveries didn't cover.
    let (tc, canonical): (Vec<_>, Vec<_>) = paks.iter()
        .partition(|p| crate::codename::codename_from_pak(Path::new(&p.pak_path)) == "TrainingCentre");

    log(&format!("Decoding train-class thumbnails across {} paks…", paks.len()));
    let total = AtomicU64::new(0);

    let run_group = |group: &[&DiscoveredRoute], skip_existing: bool, total: &AtomicU64| {
        let next = AtomicUsize::new(0);
        let workers = std::thread::available_parallelism().map(|n| n.get().clamp(2, 8)).unwrap_or(4).min(group.len().max(1));
        std::thread::scope(|s| {
            for _ in 0..workers {
                s.spawn(|| loop {
                    let i = next.fetch_add(1, Ordering::Relaxed);
                    if i >= group.len() { break }
                    let n = scan_pak_thumbnails(&repak.path, Path::new(&group[i].pak_path), &out_dir, skip_existing);
                    total.fetch_add(n, Ordering::Relaxed);
                });
            }
        });
    };
    run_group(&canonical, false, &total);
    run_group(&tc, true, &total);

    // Reconcile classes (link rvc + backfill is_drivable/type_description,
    // MAX-first) then re-resolve thumbnails against the refreshed cache —
    // so the Train Classes tab matches hud-go without a full re-extract.
    if let Ok(conn) = crate::db::write_conn() {
        let _ = crate::extractor_db_writer::reconcile_train_classes(&conn);
        let fixed = crate::extractor_db_writer::fix_train_class_thumbnails(&conn, &out_dir);
        let (of, oc) = crate::extractor_db_writer::delete_orphan_formations(&conn);
        log(&format!(
            "Reconciled classes + resolved {fixed} thumbnail links; pruned {of} orphan formation(s), {oc} orphan class(es)."
        ));
    }

    let t = total.load(Ordering::Relaxed);
    log(&format!("Done — {t} thumbnails decoded into the cache."));
    Ok(t)
}

/// Resolve display name + country for every discovered pak, in parallel.
/// Each pak runs a handful of `repak list`/`unpack` subprocesses; bounded
/// scoped workers keep that to a few concurrent processes. Best-effort —
/// failures leave the codename fallback in place.
pub fn resolve_all_metadata(routes: &mut [DiscoveredRoute]) {
    use std::sync::atomic::{AtomicUsize, Ordering};
    use std::sync::Mutex;

    if routes.is_empty() { return }
    let inputs: Vec<(String, String)> =
        routes.iter().map(|r| (r.pak_path.clone(), r.name.clone())).collect();
    let results: Vec<Mutex<(String, String)>> =
        (0..inputs.len()).map(|_| Mutex::new((String::new(), String::new()))).collect();
    let next = AtomicUsize::new(0);
    let workers = std::thread::available_parallelism()
        .map(|n| n.get().clamp(2, 8))
        .unwrap_or(4)
        .min(inputs.len());

    std::thread::scope(|s| {
        for _ in 0..workers {
            s.spawn(|| loop {
                let i = next.fetch_add(1, Ordering::Relaxed);
                if i >= inputs.len() { break }
                let (pak, code) = &inputs[i];
                let meta = resolve_pak_metadata(Path::new(pak), code);
                *results[i].lock().unwrap() = meta;
            });
        }
    });

    for (i, r) in routes.iter_mut().enumerate() {
        let m = results[i].lock().unwrap();
        r.display_name = m.0.clone();
        r.country_code = m.1.clone();
    }
}

fn tail_bytes(buf: &[u8], max: usize) -> String {
    let start = buf.len().saturating_sub(max);
    String::from_utf8_lossy(&buf[start..]).into_owned()
}

fn count_files(root: &Path) -> u64 {
    let mut stack = vec![root.to_path_buf()];
    let mut n: u64 = 0;
    while let Some(d) = stack.pop() {
        let Ok(rd) = fs::read_dir(&d) else { continue };
        for e in rd.flatten() {
            let Ok(t) = e.file_type() else { continue };
            if t.is_file() { n += 1; }
            else if t.is_dir() { stack.push(e.path()); }
        }
    }
    n
}
