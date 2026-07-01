//! Phase 10.3 orchestrator: drives the parsers + writers end-to-end
//! against an unpacked pak directory. Mirrors hud-go's
//! `extractor.Extractor.Extract` + `package_writer` + DB-import flow,
//! collapsed to a single pass that writes directly to the local
//! tsw_hud.db (skipping hud-go's zip-on-disk intermediate).
//!
//! Public entry point: [`run_pak`]. Given a pak path + temp work dir,
//! it shells out to repak, walks the unpacked tree for the asset
//! families we care about, parses each, and emits the populated rows.
//!
//! Two-pass model:
//!   * **scan**: walk the FS once, classify each `.uasset` by name
//!     pattern. Cheap, no parsing.
//!   * **process**: parse each classified asset and stream the result
//!     into the DB inside one transaction.

use std::collections::HashMap;
use std::fs;
use std::path::{Path, PathBuf};

use crate::extractor::unpack_pak;
use crate::extractor_db_writer as dbw;
use crate::uasset_route_definition;
use crate::uasset_rvd::{self, Rvd};
use crate::uasset_timetable::{self, Service};

/// Counts surfaced to the UI after a run.
#[derive(Debug, Default, Clone, serde::Serialize)]
pub struct RunCounts {
    pub pak_path:               String,
    pub temp_dir:               String,
    pub files_unpacked:         u64,
    pub rvds_parsed:            u64,
    pub timetables_parsed:      u64,
    pub route_id:               Option<i64>,
    pub country:                String,
    pub route_display_name:     String,
    pub services_written:       u64,
    pub formations_written:     u64,
    pub train_classes_written:  u64,
    pub timetable_entries_written: u64,
    /// Cooked-map feature build stats (rails / signals / switches /
    /// platforms / car-stop signs / route markers / collectables). None
    /// when the map build was skipped (no RouteDefinition, no origin).
    pub map: Option<crate::cookedmap::RouteFeaturesStats>,
    /// Per-table row counts written from the cooked-map data.
    pub car_stop_signs_written: u64,
    pub track_markers_written:  u64,
    pub signals_written:        u64,
    pub switches_written:       u64,
    pub collectables_written:   u64,
    pub route_coords_written:   bool,
    pub timetable_paths_written: u64,
    pub timetable_paths_fallback: u64,
    pub route_locations_written: u64,
    pub thumbnails_written: u64,
    pub zip: Option<crate::zip_writer::ZipResult>,
    /// Set when the pak had nothing extractable (a shared/content pak with
    /// no route, trains, or timetables). NOT a failure — the caller should
    /// report it as "skipped". Carries a human reason for the log.
    pub skipped:                Option<String>,
    /// Count of assets the broad path classifier tagged as Timetable /
    /// DataTrack that didn't actually parse as one (sub-components, shared
    /// definitions). Expected and benign — tracked for transparency, NOT
    /// pushed into `errors`, so the UI's warning count stays meaningful.
    pub assets_unparsed:        u64,
    pub errors:                 Vec<String>,
}

/// Classification of one `.uasset` file under the unpacked tree.
#[derive(Debug, Clone)]
struct Classified {
    path: PathBuf,
    kind: AssetKind,
}

#[derive(Debug, Clone, Copy, PartialEq, Eq)]
enum AssetKind {
    RouteDefinition,
    Rvd,
    Timetable,
    /// `*DataTrack.uasset` — per-service breadcrumbs the service-path
    /// builder slices against the rails vertex map.
    DataTrack,
    // ScenarioDefinition is surfaced through its own IPC; nothing in
    // the orchestrator consumes it yet.
}

/// Boxed line emitter used by [`run_pak`] to surface live progress
/// (stage / counts / errors). The IPC layer wraps this around a Tauri
/// event emit so the frontend's log panel updates mid-extract.
/// `kind` is `"ok"` / `"warn"` / `"err"` / `""`.
pub type LogSink = Box<dyn Fn(&str, &str) + Send + Sync>;

#[inline]
fn log_line(sink: Option<&LogSink>, kind: &str, msg: &str) {
    if let Some(s) = sink { s(kind, msg); }
}

/// Walk an unpacked pak tree and run the full pipeline. The directory
/// is unpacked from `pak_path` into `dest_dir` first via the shipped
/// repak.exe (same shell-out hud-go uses). When the destination exists
/// the unpack still runs — repak overwrites unchanged files cheaply.
///
/// `log` is an optional sink that receives one call per stage with a
/// short human-readable line. Pass `None` for silent runs.
pub fn run_pak(
    pak_path: &Path,
    dest_dir: &Path,
    aes_key:  &str,
    overlay_paks: &[String],
    log:      Option<&LogSink>,
) -> Result<RunCounts, String> {
    let mut counts = RunCounts {
        pak_path: pak_path.to_string_lossy().into_owned(),
        temp_dir: dest_dir.to_string_lossy().into_owned(),
        ..Default::default()
    };
    let pak_name = pak_path.file_name().and_then(|s| s.to_str()).unwrap_or("pak");
    log_line(log, "", &format!("[{pak_name}] unpacking → {}", dest_dir.display()));

    // Start from an EMPTY dir. repak only overwrites files present in the pak;
    // it never deletes leftovers. A previous route's files lingering here —
    // especially its `Content/Map/<X>Map.uexp` persistent map — would be picked
    // up by the geo-origin auto-detector and anchor this route to the wrong
    // place (the bug that put Cajon Pass's map in Germany). Wiping first makes
    // every extraction self-contained.
    let _ = std::fs::remove_dir_all(dest_dir);
    std::fs::create_dir_all(dest_dir)
        .map_err(|e| format!("create temp dir {}: {e}", dest_dir.display()))?;

    // 1) Unpack. The main pak first, then any overlay paks INTO THE SAME
    //    dir — child gameplay/cargo packs (no RouteDefinition of their own)
    //    whose timetables reference this route. Mirrors hud-go's overlay
    //    loop so one route's zip carries every contributing pak's services
    //    (e.g. Boston Sprinter = BostonProvidence + GameplayPack + BPEAcela).
    let unpack = unpack_pak(pak_path, dest_dir, aes_key)?;
    counts.files_unpacked = unpack.file_count;
    log_line(log, "ok", &format!("[{pak_name}] unpacked {} files", unpack.file_count));
    for overlay in overlay_paks {
        let op = std::path::Path::new(overlay);
        let oname = op.file_name().and_then(|s| s.to_str()).unwrap_or("overlay");
        log_line(log, "", &format!("[{pak_name}] overlaying {oname}…"));
        match unpack_pak(op, dest_dir, aes_key) {
            Ok(u)  => log_line(log, "ok", &format!("[{pak_name}] overlaid {oname} (+{} files)", u.file_count)),
            Err(e) => {
                log_line(log, "warn", &format!("[{pak_name}] overlay {oname} failed (continuing): {e}"));
                counts.errors.push(format!("overlay {oname}: {e}"));
            }
        }
    }

    // 2) Scan.
    log_line(log, "", &format!("[{pak_name}] scanning assets…"));
    let classified = scan_tree(dest_dir);
    if classified.is_empty() {
        // A shared/content pak (e.g. NLContentPack-coredata) that ships no
        // RouteDefinition, RVD, Timetable, or DataTrack. There is nothing
        // to extract — that's expected, not a failure. Report it as a
        // skip so the run summary doesn't count it as failed.
        counts.skipped = Some("content/shared pak — no routes, trains, or timetables".into());
        log_line(log, "warn", &format!(
            "[{pak_name}] skipped — no extractable assets (content/shared pak)"
        ));
        return Ok(counts);
    }

    {
        let mut nr = 0u64; let mut nrv = 0u64; let mut ntt = 0u64; let mut ndt = 0u64;
        for c in &classified {
            match c.kind {
                AssetKind::RouteDefinition => nr  += 1,
                AssetKind::Rvd             => nrv += 1,
                AssetKind::Timetable       => ntt += 1,
                AssetKind::DataTrack       => ndt += 1,
            }
        }
        log_line(log, "", &format!(
            "[{pak_name}] classified: {nr} RouteDef · {nrv} RVD · {ntt} Timetable · {ndt} DataTrack"
        ));
    }

    // Codename for this pak (e.g. "SandPatch", "TrainingCentre"),
    // derived from the pak filename the same way hud-go's pak.Route does.
    // Drives the per-codename country override and the no-RouteDefinition
    // fallback display name below.
    let codename = crate::codename::codename_from_pak(pak_path);

    // 3) Parse the RouteDefinition first — every downstream row keys
    //    off the route id. Mirror hud-go's `PakRouteDefinition`: collect
    //    every candidate, try them shortest-basename-first (the real route
    //    definition's basename is shorter than the `…LevelThresholds` /
    //    `…Rewards` / `…RewardLevelData` sub-assets that share the folder),
    //    and keep the first that parses as a route-level definition. The
    //    parser rejects non-route assets via the RouteDetails-struct check,
    //    so the sub-assets fall through to the next candidate.
    let mut rd_candidates: Vec<&Classified> = classified.iter()
        .filter(|c| c.kind == AssetKind::RouteDefinition)
        .collect();
    // Canonical `…RouteDefinition.uasset` first (keeps existing routes pinned
    // to the same asset), then folder fallbacks like `<Codename>Route.uasset`;
    // shortest basename within each group. The parse loop takes the first that
    // yields a RouteDetails struct, so reward/threshold/UI sub-assets fall
    // through to the real definition.
    rd_candidates.sort_by_key(|c| {
        let name = c.path.file_name().map(|s| s.to_string_lossy().to_ascii_lowercase()).unwrap_or_default();
        let canonical = name.ends_with("routedefinition.uasset");
        (!canonical, name.len())
    });
    let mut route_def = None;
    for c in rd_candidates {
        match uasset_route_definition::parse(&c.path) {
            Ok(rd) => { route_def = Some(rd); break; }
            Err(e) => counts.errors.push(format!("RouteDefinition {}: {e}", c.path.display())),
        }
    }

    // Normalise the parsed Country to an ISO 3166-1 alpha-2 code, THEN
    // apply the per-codename override (which is already ISO and always
    // wins). Mirrors hud-go's data → ISO → override precedence: the raw
    // asset value can be "UK" / "Deutschland" / "North America"; storing
    // it un-normalised gives the flag CSS the wrong key and spawns
    // duplicate country rows ("UK" vs "GB").
    if let Some(rd) = route_def.as_mut() {
        if !rd.country_code.is_empty() {
            rd.country_code = crate::codename::country_iso_from_code(&rd.country_code);
        }
        if let Some(ov) = crate::codename::country_override_for_codename(&codename) {
            rd.country_code = ov.to_string();
        }
    }

    // Bug-B fallback: cargo / wagon / older DLC paks ship no parseable
    // `<X>RouteDefinition.uasset`. hud-go still processes their timetables
    // and lets the importer derive a route name via RouteDisplayName +
    // codenameCountryOverride. We mirror that by synthesising a fallback
    // RouteDefinition from the codename so the route row + per-service
    // timetable rows get written instead of the whole pak being skipped.
    if route_def.is_none() && !codename.is_empty() {
        // Prefer the `*_Gameplay.uplugin` Description (hud-go's
        // PakDLCDisplayName) — cargo / wagon / train DLCs ship a real
        // human name there ("Cargo Line Intermodal") when they carry no
        // RouteDefinition. Fall back to the CamelCase-split codename only
        // when no usable uplugin name is found.
        let display = dlc_display_name_from_uplugin(dest_dir)
            .unwrap_or_else(|| crate::codename::route_display_name(&codename));
        let country = crate::codename::country_override_for_codename(&codename)
            .map(str::to_string)
            .unwrap_or_default();
        if !display.is_empty() {
            log_line(log, "warn", &format!(
                "[{pak_name}] no RouteDefinition — synthesising fallback route '{display}' from codename '{codename}'"
            ));
            route_def = Some(crate::uasset_route_definition::RouteDefinition {
                asset_path:               String::new(),
                stat_tracking_name:       codename.clone(),
                display_name:             display,
                country_code:             country,
                cross_pak_reference_name: codename.clone(),
            });
        }
    }

    // Last-resort country fallback: when the route still has no country
    // after the data value + codename override (a new / unmapped DLC whose
    // RouteDefinition ships an empty Country), infer it from the route
    // origin's lat/lng via the bounding-box table. Mirrors hud-go's
    // `geo.CountryFromOrigin` backfill (extractor.go ~line 327). Only runs
    // when needed, so the persistent-map scan isn't paid on every route.
    if let Some(rd) = route_def.as_mut() {
        if rd.country_code.is_empty() {
            if let Some((lat, lng)) = crate::cookedmap::auto_find_origin(dest_dir) {
                let c = crate::geo::country_from_origin(lat, lng);
                if !c.is_empty() {
                    rd.country_code = crate::codename::country_iso_from_code(c);
                    log_line(log, "ok", &format!(
                        "[{pak_name}] country inferred from origin ({lat:.4},{lng:.4}) → {}",
                        rd.country_code
                    ));
                }
            }
        }
    }

    // 4) Open the write connection + start a transaction so a partial
    //    failure mid-pak doesn't leave the catalog half-written.
    crate::db::drop_cached_read();
    let mut conn = crate::db::write_conn()?;
    // Disable FK enforcement for the duration of the per-pak
    // transaction. Mirrors hud-go's bulk-import pattern: child rows can
    // briefly point at a row that's about to be created (e.g.
    // pak_rvds → pak_catalog when a pak isn't yet in the catalog, or
    // route_locations → locations during an upsert-then-link sequence).
    // The final committed state IS internally consistent — the FK
    // checks just trip on intermediate ordering. Re-enabled
    // immediately after commit.
    conn.execute("PRAGMA foreign_keys = OFF", [])
        .map_err(|e| format!("disable FK: {e}"))?;
    let tx = conn.transaction().map_err(|e| e.to_string())?;

    // Fold-into-section: some service-only DLCs belong under an existing parent
    // route as a section rather than as their own route (e.g. GreatWesternBlue →
    // GWE / "Diesel Legends of the Great Western"). If the parent route is
    // already in the DB, we write this pak's services onto it under the section
    // and skip creating a standalone route. If the parent isn't extracted yet,
    // we fall back to the normal route so no data is lost.
    let fold = crate::codename::fold_into_section(&codename);
    let fold_parent_id: Option<i64> = fold.and_then(|(parent_ref, _, _)| {
        tx.query_row(
            "SELECT id FROM routes WHERE cross_pak_reference_name = ?1",
            [parent_ref],
            |r| r.get::<_, i64>(0),
        ).ok()
    });

    // Route row first, when we have a RouteDefinition (skipped when folding into
    // an existing parent).
    let route_id_opt = if let Some(pid) = fold_parent_id {
        let sec = fold.map(|(_, s, _)| s).unwrap_or("");
        counts.route_id = Some(pid);
        log_line(log, "ok", &format!(
            "[{pak_name}] folding services into parent route id={pid} under section '{sec}'"
        ));
        Some(pid)
    } else if let Some(rd) = &route_def {
        if fold.is_some() {
            log_line(log, "warn", &format!(
                "[{pak_name}] fold target not found — extract its parent route first; \
                 creating a standalone route for now"
            ));
        }
        let id = dbw::upsert_route(&tx, rd)?;
        counts.route_id           = Some(id);
        counts.country            = rd.country_code.clone();
        counts.route_display_name = if !rd.display_name.is_empty() { rd.display_name.clone() }
                                    else { rd.stat_tracking_name.clone() };
        log_line(log, "ok", &format!(
            "[{pak_name}] route '{}' ({}) id={id}",
            counts.route_display_name, counts.country
        ));
        Some(id)
    } else {
        log_line(log, "warn", &format!(
            "[{pak_name}] no RouteDefinition — skipping route + timetable rows"
        ));
        None
    };

    // When folding, create the single destination section up front so every
    // service in this pak lands under it (and so the collision check below can
    // exclude services already in this section, keeping re-extraction stable).
    let fold_section_id: Option<i64> = match (fold_parent_id, fold) {
        (Some(pid), Some((_, sec, _))) => dbw::get_or_create_section(&tx, pid, sec)?,
        _ => None,
    };

    // 5) Parse every RVD and write to train_classes. Build a class-id
    //    lookup keyed by RVD asset path so the formation walker below
    //    can resolve each vehicle's RVD → train_class id.
    let mut rvd_by_path: HashMap<String, Rvd> = HashMap::new();
    let mut class_id_by_rvd_path: HashMap<String, i64> = HashMap::new();
    // Collect pak_rvds rows alongside train_classes upserts so the
    // ReconcileTrainClasses dev tool has a row-set to walk. Mirrors
    // hud-go's catalog scan which keeps the two tables in sync.
    let pak_path_str = pak_path.to_string_lossy().into_owned();
    let mut pak_rvds_rows: Vec<(String, Rvd)> = Vec::new();
    for c in classified.iter().filter(|c| c.kind == AssetKind::Rvd) {
        let rvd = match uasset_rvd::parse(&c.path) {
            Ok(rvd) => rvd,
            Err(e) => { counts.errors.push(format!("RVD {}: {e}", c.path.display())); continue; }
        };
        counts.rvds_parsed += 1;
        // Normalised key: strip the dir + extension so CompiledRVMap's
        // `/Some/Pak/Path/RVD_HSP46.RVD_HSP46` matches our `RVD_HSP46`.
        let basename = c.path.file_stem().and_then(|s| s.to_str()).unwrap_or("").to_string();
        let asset_path_str = c.path.to_string_lossy().into_owned();
        // Upsert the catalog row. A UNIQUE-constraint conflict (a class
        // shared across paks, e.g. GP38-2 on Horseshoe Curve AND Sand
        // Patch) is NON-FATAL: we still register the RVD below so its
        // thumbnail + formation links resolve. Mirrors hud-go, which
        // decodes thumbnails per-pak independent of catalog state.
        match dbw::upsert_train_class(&tx, &rvd) {
            Ok(id) => {
                if !basename.is_empty() { class_id_by_rvd_path.insert(basename.clone(), id); }
                counts.train_classes_written += 1;
            }
            Err(e) => counts.errors.push(format!("upsert_train_class {}: {e}", c.path.display())),
        }
        pak_rvds_rows.push((asset_path_str, rvd.clone()));
        if !basename.is_empty() {
            rvd_by_path.insert(basename, rvd);
        }
    }

    log_line(log, "ok", &format!(
        "[{pak_name}] parsed {} RVDs → {} train_classes",
        counts.rvds_parsed, counts.train_classes_written
    ));

    // Replace pak_rvds for this pak. Snapshot the parsed RVDs so the
    // reconciler can re-derive train_classes later (drift recovery).
    if !pak_rvds_rows.is_empty() {
        let rows: Vec<dbw::PakRvdRow> = pak_rvds_rows.iter().map(|(asset_path, rvd)| dbw::PakRvdRow {
            pak_path:           &pak_path_str,
            asset_path,
            rvd,
            drivable:           rvd.drivable,
            substitutable_unit: false,  // not surfaced by the cooked walker
            has_guard_controls: false,
            service_types:      0,
            regions:            "",
        }).collect();
        let _ = dbw::rewrite_pak_rvds(&tx, &pak_path_str, &rows);
    }

    // Thumbnail extraction: for every RVD whose `thumbnail_asset_ref`
    // resolved, locate the matching `.uasset` under the unpacked tree,
    // decode the Texture2D, and write the PNG to
    // `<exe-dir>/resources/images/train_classes/<sanitised>.png`. The
    // `train_classes.thumbnail_path` column is stamped with the web-
    // relative URL so the route picker / class list can show the image
    // without re-decoding at request time.
    // Mirror hud-go's `addClassThumbnailsToZip` exactly so the output is
    // deterministic and matches: walk RVDs in stable file order (NOT
    // HashMap order — that picked a random livery variant each run, the
    // "thumbnails inch forward between extracts" symptom), skip RVDs with
    // no class or no thumbnail ref, then dedup by `rail_vehicle_class` —
    // marking the class seen BEFORE resolving so the first RVD-with-a-ref
    // per class wins, named by its FriendlyName.
    let images_dir = resources_images_dir();
    let png_dir = images_dir.join("train_classes");
    let mut seen_class: std::collections::HashSet<String> = std::collections::HashSet::new();
    for c in classified.iter().filter(|c| c.kind == AssetKind::Rvd) {
        let basename = match c.path.file_stem().and_then(|s| s.to_str()) {
            Some(b) => b.to_string(),
            None => continue,
        };
        let Some(rvd) = rvd_by_path.get(&basename) else { continue };
        if rvd.rail_vehicle_class.is_empty() || rvd.thumbnail_asset_ref.is_empty() { continue }
        // First RVD-with-a-ref per class wins (matches hud-go marking
        // seenClass before the texture resolve).
        if !seen_class.insert(rvd.rail_vehicle_class.clone()) { continue }
        let Some(uasset) = resolve_asset_path(dest_dir, &rvd.thumbnail_asset_ref) else { continue };
        let class_name = if !rvd.friendly_name.is_empty() {
            &rvd.friendly_name
        } else {
            &rvd.rail_vehicle_class
        };
        let sanitised = crate::uasset_texture::sanitise_thumbnail_name(class_name);
        if sanitised.is_empty() { continue }
        let png_path = png_dir.join(format!("{sanitised}.png"));
        // Don't let a Training Centre render overwrite a canonical one:
        // TC ships gray tutorial placeholders, not marketing thumbnails.
        // A real route's render always wins regardless of extraction order.
        if codename == "TrainingCentre" && png_path.is_file() { continue }
        match crate::uasset_texture::extract_texture_to_png(&uasset, &png_path) {
            Ok(_) => {
                let url = format!("/images/train_classes/{sanitised}.png");
                let _ = dbw::set_train_class_thumbnail(&tx, class_name, &url);
                counts.thumbnails_written += 1;
            }
            Err(e) => counts.errors.push(format!("thumbnail {basename}: {e}")),
        }
    }

    // 6) Parse every Timetable and write its services + formations.
    //    Keep the parsed structs around so the zip writer at the end
    //    can serialise the per-service JSONs without re-parsing the
    //    uassets.
    log_line(log, "", &format!(
        "[{pak_name}] parsing timetables…"
    ));
    let mut parsed_timetables: Vec<crate::uasset_timetable::Timetable> = Vec::new();
    for c in classified.iter().filter(|c| c.kind == AssetKind::Timetable) {
        let mut tt = match uasset_timetable::parse_cooked_timetable(
            &c.path,
            route_def.as_ref().map(|r| r.display_name.as_str()).unwrap_or(""),
        ) {
            Ok(tt) => tt,
            // The classifier tags every asset under a timetable folder as
            // Timetable, but only the route-level RouteTimetableDefinitions
            // parse — the rest are sub-definitions/shared assets. A miss
            // here is expected, not a warning; count it, don't surface it.
            Err(_) => { counts.assets_unparsed += 1; continue; }
        };
        counts.timetables_parsed += 1;

        // Sibling `<stem>_Definition.uasset` / `<stem>_Def.uasset` lookup —
        // mirrors hud-go's `loadSiblingDefinition`. Strip the trailing
        // `_Timetable` / `_TT` suffix from the asset stem, then try each
        // Definition extension. Failure is non-fatal — services keep
        // their FriendlyName / Name; this just rescues the placeholder
        // services (PlayerService / AI_Service) that need the scenario
        // title as their user-facing label.
        if let Some(scen_name) = sibling_scenario_display_name(&c.path) {
            tt.scenario_display_name = scen_name;
        }

        // Map formation_name → formation_id so the per-service link can
        // reuse rows when a formation appears across multiple services.
        let mut formation_id_by_name: HashMap<String, i64> = HashMap::new();
        for f in &tt.formations {
            if f.name.is_empty() { continue }
            let (class_id, length_m, car_count) =
                resolve_formation_class(&tt.compiled_rv_map, &class_id_by_rvd_path, &rvd_by_path, f);

            let class_name_lookup = {
                let lead_id = f.vehicles.first().map(|v| v.rail_vehicle_id.as_str()).unwrap_or("");
                tt.compiled_rv_map.get(lead_id)
                    .and_then(|asset_path| asset_path_basename(asset_path))
                    .unwrap_or_default()
            };

            let fid = dbw::upsert_formation(
                &tx,
                &f.name,
                &class_name_lookup,
                class_id,
                "",                              // livery_id — only set at vehicle level for now
                length_m,
                car_count,
            )?;
            formation_id_by_name.insert(f.name.clone(), fid);
            counts.formations_written += 1;

            // Vehicles: replace wholesale.
            let mut rows: Vec<dbw::VehicleRow> = Vec::with_capacity(f.vehicles.len());
            let mut held: Vec<(String, String, String, String, Option<f64>)> = Vec::new();
            for (i, v) in f.vehicles.iter().enumerate() {
                let asset_path = tt.compiled_rv_map.get(&v.rail_vehicle_id).cloned().unwrap_or_default();
                let basename = asset_path_basename(&asset_path).unwrap_or_default();
                let rvd = rvd_by_path.get(&basename);
                let class_name = basename.clone();
                let friendly = rvd.map(|r| r.friendly_name.clone()).unwrap_or_default();
                let livery   = rvd.map(|r| r.livery_id.clone()).unwrap_or_default();
                let cat      = rvd.map(|r| r.vehicle_category.clone()).unwrap_or_default();
                let len_opt  = if v.max_length_m > 0.0 { Some(v.max_length_m as f64) } else { None };
                held.push((class_name.clone(), friendly, livery, cat, len_opt));
                let _ = (i, class_name);
            }
            for (i, (class_name, friendly, livery, cat, len_opt)) in held.iter().enumerate() {
                let v = &f.vehicles[i];
                rows.push(dbw::VehicleRow {
                    position:         i as i64,
                    vehicle_id:       &v.rail_vehicle_id,
                    class_name:       class_name,
                    friendly_name:    friendly,
                    livery_id:        livery,
                    vehicle_category: cat,
                    length_m:         *len_opt,
                    is_lead:          i == 0, // hud-go marks position 0 as lead
                    is_flipped:       v.flipped,
                });
            }
            dbw::rewrite_formation_vehicles(&tx, fid, &rows)?;

            if let Some(rid) = route_id_opt {
                dbw::link_route_formation(&tx, rid, fid)?;
            }
        }

        // Per service: timetable row + entries + links.
        let route_id_for_tts = match route_id_opt {
            Some(id) => id,
            None     => continue, // can't write timetable without a route
        };
        // The timetable file's TimetableName is its "section" — one section per
        // file, grouping all of its services (mirrors hud-go). When folding into
        // a parent route, every service goes under the single pre-created fold
        // section instead. Resolve once, then tag + junction-link below.
        let section_id = if fold_section_id.is_some() {
            fold_section_id
        } else {
            dbw::get_or_create_section(&tx, route_id_for_tts, &tt.section_name)?
        };
        for svc in &tt.services {
            let fid = formation_id_by_name.get(&svc.formation).copied();
            // When folding, suffix a service whose name collides with one
            // already on the parent route (NOT in our fold section) so we never
            // overwrite the parent's own service. Unique names stay clean.
            let svc_name: String = match (fold_section_id, fold) {
                (Some(fsid), Some((_, _, suffix))) if service_name_collides(
                    &tx, route_id_for_tts, fsid, &svc.name) => {
                    format!("{} ({})", svc.name, suffix)
                }
                _ => svc.name.clone(),
            };
            let tid = dbw::upsert_timetable(&tx, &dbw::TimetableUpsert {
                route_id:                route_id_for_tts,
                formation_id:            fid,
                section_id,
                service_name:            &svc_name,
                current_service_name:    &svc.service_name,
                scenario_display_name:   &tt.scenario_display_name,
                service_type:            &svc.service_type,
                source:                  &svc.source,
                start_time:              &svc.start_time,
                duration:                &svc.duration,
                conductor_compatible:    derive_conductor_compatible(svc),
                playable:                svc.is_player_drivable,
                bound:                   "",
                service:                 &svc.service_class,
                contributor:             "",
                coordinates_contributor: "",
            })?;
            counts.services_written += 1;

            // Junction link so the timetable-detail / section-filter queries
            // (which read timetable_sections, not the section_id column) see it.
            if let Some(sid) = section_id {
                dbw::link_timetable_section(&tx, tid, sid)?;
            }

            // Schedule rows.
            let entries: Vec<dbw::EntryRow> = svc.schedule.iter().map(|it| {
                let action_id = dbw::action_id_for(&tx, &it.action);
                let location_id = if !it.location.is_empty() {
                    dbw::upsert_location(&tx, route_id_for_tts, &it.location).ok()
                } else { None };
                dbw::EntryRow {
                    action_id,
                    details:          &it.details,
                    location_id,
                    structure_number: &it.structure_number,
                    structure:        &it.structure,
                    time1:            &it.time1,
                    time2:            &it.time2,
                    latitude:         "",
                    longitude:        "",
                    api_name:         "",
                    sort_order:       it.sort_order as i64,
                    coord_source:     "",
                    cargo:            &it.cargo,
                    waiting_time:     &it.waiting_time,
                }
            }).collect();
            counts.timetable_entries_written += dbw::rewrite_timetable_entries(&tx, tid, &entries)?;

            if let Some(fid) = fid {
                dbw::link_timetable_formation(&tx, tid, fid)?;
            }
        }
        parsed_timetables.push(tt);
    }

    log_line(log, "ok", &format!(
        "[{pak_name}] timetables: {} parsed · {} services · {} formations · {} entries",
        counts.timetables_parsed, counts.services_written,
        counts.formations_written, counts.timetable_entries_written
    ));

    // Collect DataTrack breadcrumbs per service. One DataTrack
    // sub-asset can carry many services, so we union across every
    // DataTrack file we parse into a single map.
    let mut service_breadcrumbs: HashMap<String, Vec<crate::uasset_datatrack::TrackDataEvent>> = HashMap::new();
    for c in classified.iter().filter(|c| c.kind == AssetKind::DataTrack) {
        let Ok(dt) = crate::uasset_datatrack::parse(&c.path) else {
            // Same as timetables: many DataTrack-classified assets are
            // sub-components that don't parse standalone. Expected, benign.
            counts.assets_unparsed += 1;
            continue;
        };
        for (name, sd) in dt.services {
            // First DataTrack file wins, matching hud-go's CollectDataTracks
            // (`if _, exists := out[name]; exists { continue }`). The same
            // service appears in multiple DataTrack files (overlay paks /
            // repeated tiles); each carries the COMPLETE ordered breadcrumb
            // list, so concatenating them stacks 2-3 duplicate copies in file
            // order — which makes the path double back with huge straight jumps
            // between copies (observed: 44.7 km vs hud-go's 15.6 km for one
            // Franklin service). Take the first complete set and ignore the rest.
            service_breadcrumbs.entry(name).or_insert_with(move || sd.track_data);
        }
    }

    log_line(log, "", &format!(
        "[{pak_name}] DataTrack breadcrumbs: {} services",
        service_breadcrumbs.len()
    ));

    // Cooked-map build: rails + per-feature points. Needs a route id +
    // origin lat/lng (from the RouteDefinition). We tolerate a missing
    // origin by auto-detecting from the persistent-map .uexp.
    // Skip when folding — the parent route already owns the geometry, and
    // rebuilding from this service-only pak would overwrite it.
    if let (Some(rid), Some(rd), None) = (route_id_opt, route_def.as_ref(), fold_parent_id) {
        log_line(log, "", &format!("[{pak_name}] building cookedmap (rails + features)…"));
        // RouteDefinition doesn't carry origin in TSW6 — cookedmap
        // falls back to scanning the persistent-map .uexp under
        // Content/Map/. Pass 0,0 to force auto-detect.
        match crate::cookedmap::build(dest_dir, &rd.display_name, 0.0, 0.0) {
            Ok(rf) => {
                log_line(log, "ok", &format!(
                    "[{pak_name}] cookedmap: {} ribbons · {} signals · {} switches · {} platforms · {} collectables · {}ms",
                    rf.stats.ribbons, rf.stats.signals, rf.stats.switches,
                    rf.stats.platforms, rf.stats.collectables, rf.stats.elapsed_ms
                ));
                counts.map = Some(rf.stats.clone());

                // route_coordinates — store the rails MultiLineString as a
                // JSON blob (importer format compatibility: the legacy
                // shape is a top-level array of polylines).
                if !rf.rails.is_empty() {
                    let blob = serde_json::to_string(&rf.rails)
                        .map_err(|e| format!("serialise rails: {e}"))?;
                    crate::extractor_db_writer::write_route_coordinates(&tx, rid, &blob)?;
                    counts.route_coords_written = true;
                }

                // car_stop_signs.
                let car_rows: Vec<crate::extractor_db_writer::CarStopRow> = rf.car_stop_sign_points.iter()
                    .map(|p| crate::extractor_db_writer::CarStopRow {
                        platform_name: "",
                        ribbon_guid: &p.ribbon_guid,
                        location: p.location as f64,
                        max_rail_vehicles: p.max_rail_vehicles as i64,
                        latitude: p.lat, longitude: p.lng,
                    }).collect();
                counts.car_stop_signs_written = crate::extractor_db_writer::rewrite_car_stop_signs(&tx, rid, &car_rows)?;

                // track_markers — only emit non-empty names.
                let marker_rows: Vec<crate::extractor_db_writer::TrackMarkerRow> = rf.route_marker_points.iter()
                    .filter(|p| !p.name.is_empty())
                    .map(|p| crate::extractor_db_writer::TrackMarkerRow {
                        name: &p.name,
                        marker_type: &p.marker_type,
                        ribbon_guid: &p.ribbon_guid,
                        location: if p.location > 0.0 { Some(p.location as f64) } else { None },
                        start:    if p.start    > 0.0 { Some(p.start    as f64) } else { None },
                        end:      if p.end      > 0.0 { Some(p.end      as f64) } else { None },
                        line_side: &p.line_side,
                        latitude: p.lat, longitude: p.lng,
                    }).collect();
                counts.track_markers_written = crate::extractor_db_writer::rewrite_track_markers(&tx, rid, &marker_rows)?;

                // signals — drop points whose projection failed (lat/lng both 0).
                let signal_rows: Vec<crate::extractor_db_writer::SignalRow> = rf.signal_points.iter()
                    .filter(|p| p.lat != 0.0 || p.lng != 0.0)
                    .map(|p| crate::extractor_db_writer::SignalRow {
                        signal_id: &p.signal_id,
                        signal_type: &p.signal_type,
                        ribbon_guid: &p.ribbon_guid,
                        location_fraction: p.location_fraction as f64,
                        latitude: p.lat, longitude: p.lng,
                    }).collect();
                counts.signals_written = crate::extractor_db_writer::rewrite_signals(&tx, rid, &signal_rows)?;

                // switches.
                let switch_rows: Vec<crate::extractor_db_writer::SwitchRow> = rf.switch_points.iter()
                    .filter(|p| p.lat != 0.0 || p.lng != 0.0)
                    .map(|p| crate::extractor_db_writer::SwitchRow {
                        jct_guid: &p.jct_guid,
                        node_guid: &p.node_guid,
                        manually_controlled: p.manually_controlled,
                        latitude: p.lat, longitude: p.lng,
                    }).collect();
                counts.switches_written = crate::extractor_db_writer::rewrite_switches(&tx, rid, &switch_rows)?;

                // collectables.
                let collectable_rows: Vec<crate::extractor_db_writer::CollectableRow> = rf.collectable_points.iter()
                    .filter(|p| p.lat != 0.0 || p.lng != 0.0)
                    .map(|p| crate::extractor_db_writer::CollectableRow {
                        actor_class: &p.actor_class,
                        instance_name: &p.instance_name,
                        collectable_id: &p.collectable_id,
                        latitude: p.lat, longitude: p.lng,
                    }).collect();
                counts.collectables_written = crate::extractor_db_writer::rewrite_collectables(&tx, rid, &collectable_rows)?;

                // Backfill car_stop_signs.platform_name from the co-ribbon
                // Platform track marker (same ribbon_guid). hud-go derives the
                // name via this exact join; the parsed CarStopSign carries no
                // name of its own. Needed so each scheduled stop resolves to its
                // snug-fit car-stop-sign (where a train of that length stops).
                tx.execute(
                    "UPDATE car_stop_signs SET platform_name = ( \
                        SELECT tm.name FROM track_markers tm \
                        WHERE tm.route_id = car_stop_signs.route_id \
                          AND tm.ribbon_guid = car_stop_signs.ribbon_guid \
                          AND tm.marker_type='Platform' AND tm.name IS NOT NULL AND tm.name<>'' \
                        LIMIT 1) \
                     WHERE route_id = ?1 AND EXISTS ( \
                        SELECT 1 FROM track_markers tm \
                        WHERE tm.route_id = car_stop_signs.route_id \
                          AND tm.ribbon_guid = car_stop_signs.ribbon_guid \
                          AND tm.marker_type='Platform' AND tm.name IS NOT NULL AND tm.name<>'')",
                    rusqlite::params![rid],
                ).map_err(|e| e.to_string())?;

                // route_locations — one row per LinkedPlatform with
                // computed lat/lng. Names come from the platform's
                // .Name (e.g. "London Euston Platform 4"); bound/platform
                // are left empty since LinkedPlatform doesn't carry
                // them — the timetable schedule fills those columns when
                // it stamps locations per service.
                let loc_rows: Vec<crate::extractor_db_writer::RouteLocationRow> = rf.platform_points.iter()
                    .filter(|p| !p.name.is_empty())
                    .map(|p| crate::extractor_db_writer::RouteLocationRow {
                        name: &p.name,
                        platform: "",
                        bound:    "",
                        latitude: p.lat, longitude: p.lng,
                    }).collect();
                counts.route_locations_written = crate::extractor_db_writer::rewrite_route_locations(&tx, rid, &loc_rows)?;

                // Per-service polylines (timetable_coordinates).
                //
                // Two paths: services with DataTrack breadcrumbs get
                // sliced against the rails vertex map directly; services
                // without it fall back to a ribbon-graph Dijkstra over
                // schedule waypoints. Both end up as the same
                // `[lng, lat]` JSON blob in `timetable_coordinates`.
                let mut tt_paths_written: u64 = 0;
                let mut tt_paths_fallback:  u64 = 0;
                // Resolve every parsed timetable's services and remember
                // their schedule waypoints — we need them for the
                // fallback path.
                #[derive(Clone)]
                struct ResolvedService {
                    name: String,
                    waypoints: Vec<crate::service_path_graph::Waypoint>,
                }
                let mut resolved: Vec<ResolvedService> = Vec::new();
                for c in classified.iter().filter(|c| c.kind == AssetKind::Timetable) {
                    let Ok(tt) = crate::uasset_timetable::parse_cooked_timetable(
                        &c.path,
                        route_def.as_ref().map(|r| r.display_name.as_str()).unwrap_or(""),
                    ) else { continue };
                    for svc in &tt.services {
                        let waypoints: Vec<crate::service_path_graph::Waypoint> = svc.schedule.iter()
                            .filter(|it| !it.ribbon_guid.is_empty())
                            .map(|it| crate::service_path_graph::Waypoint {
                                ribbon_guid_norm: crate::uasset::normalize_guid(&it.ribbon_guid),
                                fraction: it.ribbon_location,
                            })
                            .collect();
                        resolved.push(ResolvedService { name: svc.name.clone(), waypoints });
                    }
                }
                // Capture the per-service polylines so the zip writer
                // at the end can embed them in each `<service>.json`.
                let mut service_polylines: HashMap<String, Vec<crate::output_format::ServiceCoord>> = HashMap::new();
                // Build the union: for services in service_breadcrumbs,
                // use DataTrack path; for the rest, use Dijkstra.
                for rs in &resolved {
                    let tid: Option<i64> = tx.query_row(
                        "SELECT id FROM timetables WHERE route_id = ?1 AND service_name = ?2",
                        rusqlite::params![rid, &rs.name],
                        |r| r.get(0),
                    ).ok();
                    let Some(tid) = tid else { continue };
                    let (path, source) = if let Some(crumbs) = service_breadcrumbs.get(&rs.name) {
                        // DataTrack breadcrumbs are the train's actual ribbon+fraction
                        // trail. Route them through the same rails-vertex + node-
                        // adjacency sampler the fallback uses so cross-ribbon
                        // transitions follow the rails THROUGH junctions instead of
                        // cutting a straight line between them. Mirrors hud-go's
                        // BuildServicePathFromTrackData → samplePathBetween (which also
                        // stitches via node adjacency). With dense breadcrumbs each
                        // cross-ribbon pair is a single hop, so the graph search just
                        // confirms the physical junction the train crossed.
                        let wps: Vec<crate::service_path_graph::Waypoint> = crumbs.iter()
                            .filter(|ev| !ev.ribbon_guid.is_empty())
                            .map(|ev| crate::service_path_graph::Waypoint {
                                ribbon_guid_norm: ev.ribbon_guid.clone(), // parser already normalised
                                fraction: ev.ribbon_location,
                            })
                            .collect();
                        let p = crate::service_path_graph::build_service_path(
                            &wps, &rf.ribbon_meta, &rf.switches, &rf.per_ribbon,
                        );
                        (p, "datatrack")
                    } else {
                        let p = crate::service_path_graph::build_service_path(
                            &rs.waypoints, &rf.ribbon_meta, &rf.switches, &rf.per_ribbon,
                        );
                        if !p.is_empty() { tt_paths_fallback += 1; }
                        (p, "graph")
                    };
                    if path.len() < 2 { continue; }
                    // Thin to ~5 m before storing — the rails-vertex sampler emits
                    // a point every ~1 m, which bloats timetable_coordinates ~5x
                    // with no visible map difference (matches hud-go).
                    let path = crate::service_path::decimate_meters(&path, 5.0);
                    let blob = crate::service_path::encode_polyline(&path);
                    crate::extractor_db_writer::write_timetable_coordinates(
                        &tx, tid, &blob, source,
                    )?;
                    // Travel direction from the path (first → last point),
                    // stored as `bound`. The per-stop car-stop resolver needs
                    // it to disambiguate the TWO signs on a through-platform
                    // (one at each end); without a direction it picks the wrong
                    // end and the distance-to-stop comes up ~a platform-length
                    // short. Dominant axis → compass bearing, mirroring hud-go's
                    // computeBound.
                    if let (Some(a), Some(b)) = (path.first(), path.last()) {
                        let d_lat = b.latitude - a.latitude;
                        let d_lng = b.longitude - a.longitude;
                        let bound = if d_lat.abs() >= d_lng.abs() {
                            if d_lat > 0.0 { "northbound" } else { "southbound" }
                        } else if d_lng > 0.0 { "eastbound" } else { "westbound" };
                        let _ = tx.execute(
                            "UPDATE timetables SET bound = ?1 WHERE id = ?2",
                            rusqlite::params![bound, tid],
                        );
                    }
                    // Convert service_path::ServiceCoord → output_format::ServiceCoord
                    // (same wire shape; separate types so the path crate doesn't pull
                    // in serde features the output crate needs).
                    let out_coords: Vec<crate::output_format::ServiceCoord> = path.iter()
                        .map(|c| crate::output_format::ServiceCoord {
                            latitude: c.latitude, longitude: c.longitude, height: 0.0,
                        })
                        .collect();
                    service_polylines.insert(rs.name.clone(), out_coords);
                    tt_paths_written += 1;
                }
                counts.timetable_paths_written  = tt_paths_written;
                counts.timetable_paths_fallback = tt_paths_fallback;
                log_line(log, "ok", &format!(
                    "[{pak_name}] timetable paths: {} written ({} via graph fallback)",
                    tt_paths_written, tt_paths_fallback
                ));

                // Emit per-route zip. Output dir defaults to
                // `<exe-dir>/extract_zips/` when the user hasn't set
                // `extractor_output_dir`. Errors are surfaced into
                // `counts.errors` but don't fail the run — the DB
                // writes above are already committed and useful on
                // their own.
                let cfg = crate::config::Config::load();
                let raw_out = cfg.extractor_output_dir.trim();
                let output_dir = if raw_out.is_empty() {
                    exe_relative("extract_zips")
                } else {
                    std::path::PathBuf::from(raw_out)
                };
                log_line(log, "", &format!("[{pak_name}] writing zip…"));
                match crate::zip_writer::write_route_zip(
                    &output_dir, rd, &rvd_by_path,
                    &parsed_timetables, &rf, &service_polylines,
                ) {
                    Ok(z) => {
                        log_line(log, "ok", &format!(
                            "[{pak_name}] zip: {} ({:.1} MB · {} services · {} thumbs)",
                            z.zip_path,
                            (z.bytes as f64) / 1024.0 / 1024.0,
                            z.services_written, z.thumbnails_packed
                        ));
                        counts.zip = Some(z);
                    }
                    Err(e) => {
                        log_line(log, "err", &format!("[{pak_name}] zip writer: {e}"));
                        counts.errors.push(format!("zip writer: {e}"));
                    }
                }
            }
            Err(e) => counts.errors.push(format!("cookedmap build: {e}")),
        }
    }

    // Reconcile train_classes (hud-go's ReconcileTrainClasses): link rows
    // to their rail_vehicle_class, then backfill is_drivable /
    // type_description / etc. as MAX over the class's RVDs — so an EMU or
    // cab-car class whose lead car is drivable shows up in the Train
    // Classes tab even though its trailer RVD upserted is_drivable=0.
    if let Err(e) = dbw::reconcile_train_classes(&tx) {
        counts.errors.push(format!("reconcile_train_classes: {e}"));
    }
    // Re-resolve every train_class's thumbnail to a PNG that actually
    // exists on disk, preferring the canonical (non-Training-Centre)
    // livery — hud-go's FixTrainClassThumbnails. Runs each extraction so
    // images refresh as more paks are processed; never leaves a class
    // pointing at a missing PNG.
    let fixed = dbw::fix_train_class_thumbnails(&tx, &png_dir);
    if fixed > 0 {
        log_line(log, "ok", &format!("[{pak_name}] resolved {fixed} train-class thumbnails"));
    }

    // NB: orphan pruning (hud-go's deleteOrphanFormations) is deliberately
    // NOT done here per-pak. Mid-batch it would delete a class whose only
    // formation lives in a pak not yet processed (cross-pak consists),
    // and reconcile can't re-link it afterward. The "Load my DLCs" finalise
    // step + the "Refresh train-class images" path run it once globally,
    // after every pak is in, where it's safe.

    tx.commit().map_err(|e| e.to_string())?;
    let _ = conn.execute("PRAGMA foreign_keys = ON", []);
    Ok(counts)
}

/// Find the sibling `*_Definition.uasset` / `*_Def.uasset` for a
/// `*_Timetable.uasset` (or `*_TT.uasset`) and return its DisplayName
/// if it parses as a scenario / tutorial Definition. Returns `None`
/// when no sibling exists or the parser rejects it — non-fatal.
fn sibling_scenario_display_name(timetable_path: &std::path::Path) -> Option<String> {
    let stem = timetable_path.file_stem()?.to_str()?;
    // Strip "_Timetable" / "_TT" to recover the scenario stem.
    let scenario_stem = ["_Timetable", "_TT"].iter()
        .find_map(|suf| stem.strip_suffix(suf))
        .unwrap_or(stem);
    let dir = timetable_path.parent()?;
    for suf in ["_Definition", "_Def"] {
        let candidate = dir.join(format!("{scenario_stem}{suf}.uasset"));
        if !candidate.is_file() { continue }
        if let Ok(def) = crate::uasset_scenario::parse(&candidate) {
            if !def.display_name.is_empty() { return Some(def.display_name); }
        }
    }
    None
}

/// Port of hud-go's `PakDLCDisplayName`: derive a human-facing DLC name
/// from the pak's `*_Gameplay.uplugin` Description when the pak ships no
/// parseable RouteDefinition. Walks the already-unpacked tree (no
/// `repak unpack -i` needed), prefers `*_gameplay.uplugin` over other
/// `.uplugin` files, parses `FriendlyName` + `Description`, trims a
/// trailing "Gameplay" tag, and rejects codename-shaped values
/// (`isJunkDLCName`). Returns the first usable Description, or `None`.
fn dlc_display_name_from_uplugin(dest_dir: &Path) -> Option<String> {
    // Collect every .uplugin, gameplay ones first (they carry the route /
    // DLC label; rolling-stock-only uplugins carry the parent's name).
    let mut gameplay: Vec<PathBuf> = Vec::new();
    let mut others:   Vec<PathBuf> = Vec::new();
    let mut stack = vec![dest_dir.to_path_buf()];
    while let Some(d) = stack.pop() {
        let Ok(rd) = fs::read_dir(&d) else { continue };
        for entry in rd.flatten() {
            let Ok(ft) = entry.file_type() else { continue };
            let path = entry.path();
            if ft.is_dir() { stack.push(path); continue }
            if !ft.is_file() { continue }
            let Some(name) = path.file_name().and_then(|s| s.to_str()) else { continue };
            let lower = name.to_ascii_lowercase();
            if !lower.ends_with(".uplugin") { continue }
            if lower.ends_with("_gameplay.uplugin") { gameplay.push(path); }
            else { others.push(path); }
        }
    }
    gameplay.sort();
    others.sort();
    for path in gameplay.into_iter().chain(others) {
        let Ok(raw) = fs::read_to_string(&path) else { continue };
        let Ok(doc) = serde_json::from_str::<serde_json::Value>(&raw) else { continue };
        let description = doc.get("Description").and_then(|v| v.as_str()).unwrap_or("");
        let friendly    = doc.get("FriendlyName").and_then(|v| v.as_str()).unwrap_or("");
        let desc     = crate::codename::trim_gameplay_suffix(description);
        let friendly = crate::codename::trim_gameplay_suffix(friendly);
        if crate::codename::is_junk_dlc_name(&desc, &friendly) { continue }
        return Some(desc);
    }
    None
}

fn exe_relative(rel: &str) -> std::path::PathBuf {
    std::env::current_exe()
        .ok()
        .and_then(|p| p.parent().map(|p| p.to_path_buf()))
        .map(|p| p.join(rel))
        .unwrap_or_else(|| std::path::PathBuf::from(rel))
}

/// Path to the bundled `resources/images/` directory next to the
/// running `hud.exe`. Matches hud-go's convention so the existing web
/// HUD + map UI URLs (`/images/train_classes/...`) keep resolving.
fn resources_images_dir() -> PathBuf {
    std::env::current_exe()
        .ok()
        .and_then(|p| p.parent().map(|p| p.to_path_buf()))
        .map(|p| p.join("resources").join("images"))
        .unwrap_or_else(|| std::path::PathBuf::from("resources").join("images"))
}

/// Resolve a UE soft-object asset reference (e.g.
/// `/Game/MBTA/Textures/Thumbs/T_HSP46_Thumb` or
/// `T_HSP46_Thumb.T_HSP46_Thumb`) to a concrete `.uasset` file under
/// `dest_dir`. `/Game/` corresponds to `Content/` in the unpacked
/// tree; in some cases the soft path is a bare basename and we just
/// search the tree for a matching file. Returns the first match.
fn resolve_asset_path(dest_dir: &Path, asset_ref: &str) -> Option<PathBuf> {
    if asset_ref.is_empty() { return None; }
    // The ref is a UE soft object path: `/<Mount>/<RelPath>.<AssetName>`.
    // Strip the trailing `.<AssetName>` (last dot) to get the canonical
    // package path, exactly as hud-go does. Matching on this FULL path —
    // not just the basename — is what disambiguates same-named textures
    // shipped by different plugins/liveries (the "BNSF SD70ACe shows the
    // UP livery" / "BR 185.2 grabs the 185-5 render" bugs) and a
    // same-named NON-texture asset shadowing the real thumbnail (the
    // "Flying Scotsman has no image" bug).
    let ref_canon = asset_ref.rfind('.').map(|i| &asset_ref[..i]).unwrap_or(asset_ref);

    // Also keep the legacy `/Game/`→`Content/` direct mapping + basename
    // for refs that aren't plugin-mounted (base-game `/Game/...`).
    let raw = ref_canon.trim_start_matches('/');
    let basename = std::path::Path::new(raw)
        .file_name().and_then(|s| s.to_str()).unwrap_or("");
    let target = if basename.is_empty() { String::new() } else { format!("{basename}.uasset") };

    // Single tree walk: an exact canonical match wins immediately; a
    // basename match is remembered only as a fallback (hud-go's order).
    let mut basename_hit: Option<PathBuf> = None;
    let mut stack = vec![dest_dir.to_path_buf()];
    while let Some(d) = stack.pop() {
        let Ok(rd) = std::fs::read_dir(&d) else { continue };
        for entry in rd.flatten() {
            let Ok(ft) = entry.file_type() else { continue };
            let path = entry.path();
            if ft.is_dir() { stack.push(path); continue; }
            if !ft.is_file() { continue; }
            let Some(name) = path.file_name().and_then(|s| s.to_str()) else { continue };
            if !name.ends_with(".uasset") { continue; }
            if canonical_rvd_path(&path) == ref_canon { return Some(path); }
            if basename_hit.is_none() && !target.is_empty() && name == target {
                basename_hit = Some(path);
            }
        }
    }
    basename_hit
}

/// Turn an on-disk `.uasset` path into the canonical UE package-path form
/// an RVD's `ThumbnailAssetRef` uses: `/<PluginMount>/<RelPath>` with the
/// `Plugins/DLC/` prefix and the `/Content/` segment removed and no
/// `.uasset` extension. Port of hud-go's `pak.CanonicalRVDPath`.
pub(crate) fn canonical_rvd_path(disk_path: &Path) -> String {
    let p = disk_path.to_string_lossy().replace('\\', "/");
    const MARKER: &str = "/Plugins/DLC/";
    let mut s = if let Some(idx) = p.find(MARKER) {
        // Keep the leading '/' before the plugin mount name.
        p[idx + MARKER.len() - 1..].to_string()
    } else {
        p
    };
    if let Some(pos) = s.find("/Content/") {
        s.replace_range(pos..pos + "/Content/".len(), "/");
    }
    s.trim_end_matches(".uasset").to_string()
}

fn candidate_roots(dest_dir: &Path) -> Vec<PathBuf> {
    let mut out = vec![dest_dir.to_path_buf()];
    if let Ok(rd) = std::fs::read_dir(dest_dir) {
        for entry in rd.flatten() {
            if entry.file_type().map(|ft| ft.is_dir()).unwrap_or(false) {
                out.push(entry.path());
            }
        }
    }
    out
}

/// Walk the unpacked tree and classify every `.uasset` we recognise.
fn scan_tree(root: &Path) -> Vec<Classified> {
    let mut out: Vec<Classified> = Vec::new();
    let mut stack = vec![root.to_path_buf()];
    while let Some(d) = stack.pop() {
        let Ok(rd) = fs::read_dir(&d) else { continue };
        for entry in rd.flatten() {
            let Ok(ft) = entry.file_type() else { continue };
            let path = entry.path();
            if ft.is_dir() { stack.push(path); continue }
            if !ft.is_file() { continue }
            let Some(name) = path.file_name().and_then(|s| s.to_str()) else { continue };
            if !name.ends_with(".uasset") { continue }
            // Relative-path string (lower-cased, forward slashes) for the
            // path-based Timetable match below. Mirrors hud-go's use of the
            // root-relative path so the temp dir name can't false-match.
            let rel = path.strip_prefix(root).unwrap_or(&path);
            let rel_lower = rel.to_string_lossy().to_lowercase().replace('\\', "/");
            if let Some(kind) = classify(name, &rel_lower) {
                out.push(Classified { path, kind });
            }
        }
    }
    // Stable ordering so logs / errors are reproducible.
    out.sort_by(|a, b| a.path.cmp(&b.path));
    out
}

/// Classify one `.uasset` by basename + relative path. RouteDefinition /
/// RVD / DataTrack are basename signals (checked first so they win over a
/// `/Timetable/` ancestor folder). Timetable discovery is **path-based**,
/// mirroring hud-go's `findTimetableAssets`: the service-mode timetable
/// files (e.g. `Content/Timetable/LeipzigDresden.uasset`) carry no
/// "Timetable" in their basename — they're only findable via the
/// `/Timetable/` folder. Scenario / tutorial schedules live under
/// `/Scenarios/` and `/Training/` with arbitrary basenames; the downstream
/// parser rejects any that turn out not to carry a Services array.
fn classify(filename: &str, rel_lower: &str) -> Option<AssetKind> {
    // RouteDefinition: hud-go's `PakRouteDefinition` matches on the
    // `/RouteDefinition/` FOLDER, not the basename — route definitions ship
    // with inconsistent basenames (`EMKRouteDefinition`, `WestSomerset-
    // Definition`, `LIRRDefinition` — no "Route"; `MarseilleAvignonRoute` —
    // no "Definition", yields "LGV Méditerranée"). We accept either the legacy
    // `…RouteDefinition.uasset` basename suffix OR ANY `.uasset` living under a
    // `/routedefinition/` folder. The sibling reward / threshold / UI
    // sub-assets (`…Rewards`, `…LevelThresholds`, `…RewardLevelData`,
    // `TSW3_UI_Dynamic_Images…`) that share the folder are rejected by the
    // parser's RouteDetails-struct check, so casting the net to the whole
    // folder is safe and catches the `…Route.uasset` naming. Scenario
    // `…_Definition.uasset` assets live under /scenarios/ (not
    // /routedefinition/) so they don't match here.
    let base_lc = filename.to_ascii_lowercase();
    if base_lc.ends_with("routedefinition.uasset")
        || (rel_lower.contains("/routedefinition/") && base_lc.ends_with(".uasset"))
    {
        return Some(AssetKind::RouteDefinition);
    }
    // RVD: two competing naming conventions across DLCs — prefix form
    // `RVD_<X>.uasset` (Boston Sprinter, LIRR, MBTA…) AND suffix form
    // `<X>_RVD.uasset` (Horseshoe Curve ES44AC, Sand Patch AC4400CW,
    // Sherman Hill SD40-2…). Matching only the prefix dropped ~19 RVDs
    // across 8 paks — their train classes + thumbnails vanished. Port of
    // hud-go's `IsRVDAsset`.
    if filename.starts_with("RVD_") || filename.ends_with("_RVD.uasset") {
        return Some(AssetKind::Rvd);
    }
    // DataTrack: anything containing "DataTrack" in the filename
    // (includes `RouteTimetableDataTrack` and similar variants). Checked
    // before Timetable so DataTrack assets under a /Timetable/ folder
    // aren't mis-bucketed as schedules.
    if filename.contains("DataTrack") {
        return Some(AssetKind::DataTrack);
    }
    // Timetable: path-based, matching hud-go's findTimetableAssets, PLUS a
    // precise `_TT.uasset` basename SUFFIX. The suffix recovers tutorial
    // timetables that live under folders hud-go's path rules miss — e.g.
    // Training Centre's `/Content/TrainingND24/NN-Foo/NN_Foo_TT.uasset`
    // (the folder is "TrainingND24", which doesn't contain "/training/",
    // and the basename has no "Timetable"). The suffix form is deliberately
    // exact: it matches `..._TT.uasset` but NOT the scenery decals/materials
    // named `..._TT_01.uasset` (a number suffix) that a `contains("_TT")`
    // test used to false-match. The downstream parser rejects any file
    // that turns out to carry no Services array.
    if rel_lower.contains("timetable")
        || rel_lower.contains("servicemode")
        || rel_lower.contains("service_mode")
        || rel_lower.contains("/scenarios/")
        || rel_lower.contains("/training/")
        || base_lc.ends_with("_tt.uasset")
    {
        return Some(AssetKind::Timetable);
    }
    None
}

/// Return the last `/`-separated segment of an asset path (with the
/// trailing `.<Name>` stripped), so a CompiledRVMap entry like
/// `/Some/Pak/Path/RVD_HSP46.RVD_HSP46` matches our basename
/// `RVD_HSP46`.
fn asset_path_basename(asset_path: &str) -> Option<String> {
    let last_slash = asset_path.rfind('/');
    let tail = match last_slash {
        Some(i) => &asset_path[i + 1..],
        None    => asset_path,
    };
    let stripped = match tail.find('.') {
        Some(i) => &tail[..i],
        None    => tail,
    };
    if stripped.is_empty() { None } else { Some(stripped.to_string()) }
}

/// Look up the train-class id + per-formation aggregates from the lead
/// vehicle's RVD. Returns `(class_id, length_m, car_count)` — any of
/// these can be `None` when the lookup fails.
fn resolve_formation_class(
    compiled_rv_map: &HashMap<String, String>,
    class_id_by_rvd_path: &HashMap<String, i64>,
    rvd_by_path: &HashMap<String, Rvd>,
    f: &uasset_timetable::Formation,
) -> (Option<i64>, Option<f64>, Option<i64>) {
    let lead_vehicle_id = f.vehicles.first().map(|v| v.rail_vehicle_id.as_str()).unwrap_or("");
    let asset_path = compiled_rv_map.get(lead_vehicle_id).cloned().unwrap_or_default();
    let basename = asset_path_basename(&asset_path).unwrap_or_default();
    let class_id = class_id_by_rvd_path.get(&basename).copied();
    let car_count = if f.vehicles.is_empty() { None } else { Some(f.vehicles.len() as i64) };
    let length_m = if f.vehicles.is_empty() {
        None
    } else {
        let sum: f64 = f.vehicles.iter().map(|v| v.max_length_m as f64).sum();
        if sum > 0.0 { Some(sum) } else {
            rvd_by_path.get(&basename).map(|r| (r.approximate_length_m as f64) * (f.vehicles.len() as f64))
        }
    };
    (class_id, length_m, car_count)
}

/// Mirror hud-go's `conductorCompatible` rule: a service is conductor-
/// compatible when its `stop_and_load_count > 0` (i.e. there's at least
/// one bIsStopping & LoadUnload instruction). Cheap surrogate for "this
/// service actually picks up revenue passengers somewhere".
fn derive_conductor_compatible(svc: &Service) -> bool {
    svc.stop_and_load_count > 0
}


/// True if `name` already names a service on `route_id` that is NOT in our fold
/// section — i.e. a parent-route service the fold would otherwise overwrite.
/// Excluding the fold section keeps re-extraction idempotent (a service we
/// already folded+suffixed isn't treated as a fresh collision).
fn service_name_collides(
    tx: &rusqlite::Transaction,
    route_id: i64,
    fold_section_id: i64,
    name: &str,
) -> bool {
    tx.query_row(
        "SELECT 1 FROM timetables \
         WHERE route_id = ?1 AND service_name = ?2 \
           AND (section_id IS NULL OR section_id <> ?3) LIMIT 1",
        rusqlite::params![route_id, name, fold_section_id],
        |_| Ok(()),
    ).is_ok()
}

