//! Direct DB writer for the native extractor (Phase 10.3 slice 1).
//!
//! This module bypasses hud-go's zip→importer roundtrip — the cooked
//! parsers produce structs in memory, and these helpers turn them into
//! `INSERT … ON CONFLICT` upserts against hud's local `tsw_hud.db`.
//!
//! Each helper is **idempotent**: re-running a route extraction updates
//! existing rows in-place rather than creating duplicates. Identity is
//! the obvious thing per table: `routes.name`, `countries.code`,
//! `train_classes.name`, `train_class_electrification (train_class_id,
//! current, voltage_v, frequency_hz)`.
//!
//! Slice 1 scope: routes + countries + train_classes +
//! train_class_electrification. The remaining tables (timetables,
//! formations, formation_vehicles, route_formations,
//! timetable_formations, timetable_entries, route_coordinates,
//! timetable_coordinates, locations, route_locations, route_markers,
//! …) come in follow-up slices.

use rusqlite::{params, Connection};

use crate::uasset_route_definition::RouteDefinition;
use crate::uasset_rvd::Rvd;

/// Tables `nuke_db` preserves: the curated lookup tables that the
/// migrations seed. Mirrors hud-go's `nukeDBSeedTables`.
pub const NUKE_SEED_TABLES: &[&str] = &[
    "countries",
    "weather_presets",
    "timetable_actions",
];

/// What `nuke_db` returns to the UI.
#[derive(Debug, Clone, serde::Serialize)]
pub struct NukeResult {
    pub success:      bool,
    /// `table` → `rows_deleted`. Empty when nothing was wiped.
    pub wiped:        std::collections::BTreeMap<String, i64>,
    /// Tables we left alone (the seed list).
    pub kept:         Vec<String>,
    /// Per-table DELETE errors. None when everything succeeded.
    pub errors:       Vec<String>,
    /// Set when VACUUM itself failed (lock contention typically). The
    /// wipe still happened — just the file didn't shrink.
    pub vacuum_error: Option<String>,
    /// Train-class thumbnail PNGs removed from disk alongside the DB wipe.
    pub images_deleted: u64,
}

/// Delete every decoded train-class thumbnail PNG under
/// `<resources>/images/train_classes/`. The directory itself is kept so
/// the static-file route still resolves; only the `.png` files go. Used
/// by the nuke so a full reset clears the images too (the user wants a
/// clean slate, not stale thumbnails surviving the wipe). Returns the
/// count removed. Best-effort — never fails the nuke.
pub fn wipe_train_class_images() -> u64 {
    let dir = crate::config::resources_dir()
        .join("images")
        .join("train_classes");
    let mut removed = 0u64;
    if let Ok(entries) = std::fs::read_dir(&dir) {
        for e in entries.flatten() {
            let p = e.path();
            let is_png = p.extension()
                .and_then(|x| x.to_str())
                .map(|x| x.eq_ignore_ascii_case("png"))
                .unwrap_or(false);
            if is_png && std::fs::remove_file(&p).is_ok() {
                removed += 1;
            }
        }
    }
    removed
}

/// Wipe every user-table EXCEPT [`NUKE_SEED_TABLES`]. Schema is
/// preserved; only rows are deleted. Resets `sqlite_sequence`
/// autoincrement counters and `VACUUM`s the file so it actually shrinks
/// on disk. Mirrors hud-go's `NukeDB` line-for-line.
pub fn nuke_db(conn: &Connection) -> Result<NukeResult, String> {
    // Enumerate every user table.
    let mut all: Vec<String> = Vec::new();
    {
        let mut s = conn.prepare(
            "SELECT name FROM sqlite_master \
             WHERE type='table' AND name NOT LIKE 'sqlite_%' ORDER BY name"
        ).map_err(|e| e.to_string())?;
        let rows = s.query_map([], |r| r.get::<_, String>(0))
            .map_err(|e| e.to_string())?;
        for r in rows { all.push(r.map_err(|e| e.to_string())?); }
    }

    // Suspend FK checks so DELETE ordering doesn't matter.
    conn.execute("PRAGMA foreign_keys = OFF", [])
        .map_err(|e| format!("disable FK: {e}"))?;

    let mut wiped:  std::collections::BTreeMap<String, i64> = std::collections::BTreeMap::new();
    let mut kept:   Vec<String>   = Vec::new();
    let mut errors: Vec<String>   = Vec::new();

    for t in &all {
        if NUKE_SEED_TABLES.iter().any(|s| *s == t) {
            kept.push(t.clone());
            continue;
        }
        // DELETE the table. Identifiers (table names) can't be bound
        // through params; we got the names from sqlite_master so they're
        // already vetted, and we wrap in double-quotes defensively.
        let stmt = format!("DELETE FROM \"{t}\"");
        match conn.execute(&stmt, []) {
            Ok(n) => {
                wiped.insert(t.clone(), n as i64);
                // Reset autoincrement counter so next INSERTs start at 1.
                // sqlite_sequence row is created lazily — skip on error.
                let _ = conn.execute(
                    "DELETE FROM sqlite_sequence WHERE name = ?1",
                    params![t],
                );
            }
            Err(e) => errors.push(format!("{t}: {e}")),
        }
    }

    // Re-enable FKs regardless of outcome.
    let _ = conn.execute("PRAGMA foreign_keys = ON", []);

    // VACUUM so the file actually shrinks. Holds an exclusive lock and
    // can fail under concurrent access; report best-effort.
    let vacuum_error = conn.execute("VACUUM", []).err().map(|e| e.to_string());

    // Clear the decoded thumbnail cache too — a nuke is a full reset, and
    // leaving stale PNGs on disk means a later re-extract could re-link an
    // old image. fix_train_class_thumbnails re-decodes on the next run.
    let images_deleted = wipe_train_class_images();

    Ok(NukeResult {
        success: errors.is_empty(),
        wiped,
        kept,
        errors,
        vacuum_error,
        images_deleted,
    })
}

/// Result counts for one extraction run. Surfaced through the IPC so
/// the UI can show what changed.
#[derive(Debug, Default, Clone, serde::Serialize)]
pub struct ExtractDbCounts {
    pub routes_upserted:         u64,
    pub countries_upserted:      u64,
    pub train_classes_upserted:  u64,
    pub electrification_rows:    u64,
}

/// Upsert one Country by ISO code. Returns its `id`. Created if absent.
pub fn upsert_country(conn: &Connection, name: &str, code: &str) -> Result<i64, String> {
    // Try by code first (the stable identity); fall back to name match
    // for the legacy rows that have no code stored.
    let code_norm = code.trim().to_ascii_uppercase();
    if !code_norm.is_empty() {
        if let Ok(id) = conn.query_row(
            "SELECT id FROM countries WHERE code = ?1",
            [&code_norm], |r| r.get::<_, i64>(0),
        ) {
            // Update display name when it changed.
            conn.execute(
                "UPDATE countries SET name = ?1 WHERE id = ?2",
                params![name, id],
            ).map_err(|e| e.to_string())?;
            return Ok(id);
        }
    }
    // Fall back: look up by name.
    if let Ok(id) = conn.query_row(
        "SELECT id FROM countries WHERE name = ?1",
        [name], |r| r.get::<_, i64>(0),
    ) {
        if !code_norm.is_empty() {
            conn.execute(
                "UPDATE countries SET code = ?1 WHERE id = ?2",
                params![code_norm, id],
            ).map_err(|e| e.to_string())?;
        }
        return Ok(id);
    }
    // Create.
    conn.execute(
        "INSERT INTO countries (name, code) VALUES (?1, ?2)",
        params![name, if code_norm.is_empty() { None } else { Some(&code_norm) }],
    ).map_err(|e| e.to_string())?;
    Ok(conn.last_insert_rowid())
}

/// Upsert a `routes` row from a parsed RouteDefinition. The route's
/// country is upserted first; `best_data` is set to 1 to mark this row
/// as extractor-sourced (mirrors hud-go's writer).
pub fn upsert_route(conn: &Connection, rd: &RouteDefinition) -> Result<i64, String> {
    let country_id = upsert_country(conn, &rd.country_code_long(), &rd.country_code)?;
    let display_name = if !rd.display_name.is_empty() { &rd.display_name }
                       else { &rd.stat_tracking_name };
    if display_name.is_empty() {
        return Err("RouteDefinition has neither DisplayName nor StatTrackingName".into());
    }
    // Identity: routes.name, matched tolerantly. A re-extraction must
    // collapse onto the existing row rather than spawn a parallel one —
    // the cause of the "7 of the same route" duplication. Match exact
    // name OR the whitespace/case-insensitive normalisation, mirroring
    // hud-go's autoImportZip dedup
    // (`REPLACE(LOWER(name),' ','') = REPLACE(LOWER(?),' ','')`), which
    // also absorbs the historic display-name-vs-codename flip.
    if let Ok(id) = conn.query_row(
        "SELECT id FROM routes \
         WHERE name = ?1 \
            OR REPLACE(LOWER(name), ' ', '') = REPLACE(LOWER(?1), ' ', '') \
         ORDER BY (name = ?1) DESC LIMIT 1",
        [display_name], |r| r.get::<_, i64>(0),
    ) {
        conn.execute(
            "UPDATE routes \
             SET country_id = ?1, cross_pak_reference_name = ?2, best_data = 1 \
             WHERE id = ?3",
            params![country_id,
                if rd.cross_pak_reference_name.is_empty() { None } else { Some(&rd.cross_pak_reference_name) },
                id],
        ).map_err(|e| e.to_string())?;
        Ok(id)
    } else {
        conn.execute(
            "INSERT INTO routes (name, country_id, tsw_version, cross_pak_reference_name, best_data) \
             VALUES (?1, ?2, 6, ?3, 1)",
            params![display_name, country_id,
                if rd.cross_pak_reference_name.is_empty() { None } else { Some(&rd.cross_pak_reference_name) }],
        ).map_err(|e| e.to_string())?;
        Ok(conn.last_insert_rowid())
    }
}

/// Upsert a `train_classes` row from a parsed RVD. Returns the row's
/// id. Identity is the `name` column — for TSW data the name is the
/// RVD's FriendlyName when set, else its RailVehicleClass.
///
/// Electrification rows under `train_class_electrification` are
/// rewritten in full per upsert (old rows for this class are deleted
/// first), matching how the Go importer treats them.
pub fn upsert_train_class(conn: &Connection, rvd: &Rvd) -> Result<i64, String> {
    let name = if !rvd.friendly_name.is_empty() { &rvd.friendly_name }
               else { &rvd.rail_vehicle_class };
    if name.is_empty() {
        return Err("RVD has neither FriendlyName nor RailVehicleClass".into());
    }
    let length_opt = if rvd.approximate_length_m > 0.0 { Some(rvd.approximate_length_m as f64) } else { None };
    let speed_opt  = if rvd.max_speed_kph > 0.0       { Some(rvd.max_speed_kph as f64) }        else { None };
    let power_opt  = if rvd.max_power_kw > 0.0        { Some(rvd.max_power_kw as f64) }         else { None };
    let axles_opt  = if rvd.powered_axle_count > 0    { Some(rvd.powered_axle_count as i64) }   else { None };

    // Identity: an existing row matching this class. Match by `name`
    // first, then by the non-empty `rail_vehicle_class` — the table has a
    // UNIQUE constraint on rail_vehicle_class, so two RVDs that share a
    // class but ship different friendly names (e.g. "GP38-2 NS" on
    // Horseshoe Curve vs "GP38-2" on CSX/Sand Patch) must collapse onto
    // the same row. Keying on `name` alone sent the second one down the
    // INSERT path, which then tripped the rail_vehicle_class UNIQUE
    // constraint and failed the whole RVD (losing its thumbnail).
    let existing = conn.query_row(
        "SELECT id FROM train_classes WHERE name = ?1",
        [name], |r| r.get::<_, i64>(0),
    ).ok().or_else(|| {
        if rvd.rail_vehicle_class.is_empty() { return None; }
        conn.query_row(
            "SELECT id FROM train_classes WHERE rail_vehicle_class = ?1",
            [&rvd.rail_vehicle_class], |r| r.get::<_, i64>(0),
        ).ok()
    });
    let id = if let Some(id) = existing {
        conn.execute(
            "UPDATE train_classes \
             SET livery_id          = ?1, \
                 typical_length_m   = ?2, \
                 is_electric        = ?3, \
                 max_speed_kph      = ?4, \
                 max_power_kw       = ?5, \
                 manufacturer_name  = ?6, \
                 engine_description = ?7, \
                 type_description   = ?8, \
                 vehicle_category   = ?9, \
                 rail_vehicle_class = ?10, \
                 is_drivable        = ?11, \
                 powered_axle_count = ?12 \
             WHERE id = ?13",
            params![
                opt_string(&rvd.livery_id),         length_opt,
                rvd.is_electric as i64,             speed_opt,
                power_opt,                          opt_string(&rvd.manufacturer_name),
                opt_string(&rvd.engine_description),opt_string(&rvd.type_description),
                opt_string(&rvd.vehicle_category),  opt_string(&rvd.rail_vehicle_class),
                rvd.drivable as i64,                axles_opt,
                id,
            ],
        ).map_err(|e| e.to_string())?;
        id
    } else {
        conn.execute(
            "INSERT INTO train_classes \
             (name, livery_id, typical_length_m, is_electric, max_speed_kph, \
              max_power_kw, manufacturer_name, engine_description, type_description, \
              vehicle_category, rail_vehicle_class, is_drivable, powered_axle_count) \
             VALUES (?1, ?2, ?3, ?4, ?5, ?6, ?7, ?8, ?9, ?10, ?11, ?12, ?13)",
            params![
                name, opt_string(&rvd.livery_id), length_opt,
                rvd.is_electric as i64, speed_opt,
                power_opt, opt_string(&rvd.manufacturer_name),
                opt_string(&rvd.engine_description), opt_string(&rvd.type_description),
                opt_string(&rvd.vehicle_category), opt_string(&rvd.rail_vehicle_class),
                rvd.drivable as i64, axles_opt,
            ],
        ).map_err(|e| e.to_string())?;
        conn.last_insert_rowid()
    };

    // Replace electrification rows wholesale — simpler than dedup-by-tuple.
    conn.execute(
        "DELETE FROM train_class_electrification WHERE train_class_id = ?1",
        [id],
    ).map_err(|e| e.to_string())?;
    for e in &rvd.electrification {
        conn.execute(
            "INSERT INTO train_class_electrification \
             (train_class_id, current, pickup_side, voltage_v, frequency_hz) \
             VALUES (?1, ?2, ?3, ?4, ?5)",
            params![id,
                opt_string(&e.current), opt_string(&e.pickup_side),
                if e.voltage_v   == 0 { None } else { Some(e.voltage_v) },
                if e.frequency_hz == 0 { None } else { Some(e.frequency_hz) },
            ],
        ).map_err(|e| e.to_string())?;
    }
    Ok(id)
}

fn opt_string(s: &str) -> Option<&str> {
    if s.is_empty() { None } else { Some(s) }
}

// =================================================================== Formations
//
// A `Formation` row identifies one logical consist on a route. Identity =
// `formations.name`. `formation_vehicles` are rewritten in full per upsert.

/// Upsert a `formations` row. Returns the row id. `class_id` should be
/// the train-class id resolved from the formation's lead vehicle (typically
/// looked up by RVD asset path → train_classes row). Pass `None` when
/// unknown — many AI consists don't have a player-facing class.
pub fn upsert_formation(
    conn: &Connection,
    name: &str,
    class_name: &str,
    class_id: Option<i64>,
    livery_id: &str,
    length_m: Option<f64>,
    car_count: Option<i64>,
) -> Result<i64, String> {
    if name.is_empty() {
        return Err("formation needs a name".into());
    }
    if let Ok(id) = conn.query_row(
        "SELECT id FROM formations WHERE name = ?1",
        [name], |r| r.get::<_, i64>(0),
    ) {
        conn.execute(
            "UPDATE formations \
             SET class_name = ?1, class_id = ?2, livery_id = ?3, \
                 length_m = ?4, car_count = ?5 \
             WHERE id = ?6",
            params![
                opt_string(class_name), class_id,
                opt_string(livery_id),  length_m, car_count, id,
            ],
        ).map_err(|e| e.to_string())?;
        Ok(id)
    } else {
        conn.execute(
            "INSERT INTO formations \
             (name, class_name, class_id, livery_id, length_m, car_count) \
             VALUES (?1, ?2, ?3, ?4, ?5, ?6)",
            params![
                name, opt_string(class_name), class_id,
                opt_string(livery_id), length_m, car_count,
            ],
        ).map_err(|e| e.to_string())?;
        Ok(conn.last_insert_rowid())
    }
}

/// Replace every `formation_vehicles` row for the given formation. Same
/// strategy as `train_class_electrification`: delete-then-insert.
/// `vehicles` is borrowed from the parsed Formation; the `class_name` /
/// `friendly_name` / `livery_id` / `vehicle_category` / `length_m` per
/// car come from looking up each vehicle's RVD via CompiledRVMap + the
/// route's RVDs.
pub struct VehicleRow<'a> {
    pub position:         i64,
    pub vehicle_id:       &'a str,
    pub class_name:       &'a str,
    pub friendly_name:    &'a str,
    pub livery_id:        &'a str,
    pub vehicle_category: &'a str,
    pub length_m:         Option<f64>,
    pub is_lead:          bool,
    pub is_flipped:       bool,
}

pub fn rewrite_formation_vehicles(
    conn: &Connection,
    formation_id: i64,
    vehicles: &[VehicleRow<'_>],
) -> Result<u64, String> {
    conn.execute(
        "DELETE FROM formation_vehicles WHERE formation_id = ?1",
        [formation_id],
    ).map_err(|e| e.to_string())?;
    let mut n = 0u64;
    for v in vehicles {
        conn.execute(
            "INSERT INTO formation_vehicles \
             (formation_id, position, vehicle_id, class_name, friendly_name, \
              livery_id, vehicle_category, length_m, is_lead, is_flipped) \
             VALUES (?1, ?2, ?3, ?4, ?5, ?6, ?7, ?8, ?9, ?10)",
            params![
                formation_id, v.position, v.vehicle_id,
                opt_string(v.class_name), opt_string(v.friendly_name),
                opt_string(v.livery_id), opt_string(v.vehicle_category),
                v.length_m, v.is_lead as i64, v.is_flipped as i64,
            ],
        ).map_err(|e| e.to_string())?;
        n += 1;
    }
    Ok(n)
}

/// Link a route ↔ formation. Idempotent — no-op when the row already
/// exists.
pub fn link_route_formation(conn: &Connection, route_id: i64, formation_id: i64) -> Result<(), String> {
    let exists: i64 = conn.query_row(
        "SELECT COUNT(*) FROM route_formations WHERE route_id = ?1 AND formation_id = ?2",
        params![route_id, formation_id], |r| r.get(0),
    ).map_err(|e| e.to_string())?;
    if exists == 0 {
        conn.execute(
            "INSERT INTO route_formations (route_id, formation_id) VALUES (?1, ?2)",
            params![route_id, formation_id],
        ).map_err(|e| e.to_string())?;
    }
    Ok(())
}

/// Link a timetable ↔ formation. Idempotent.
pub fn link_timetable_formation(conn: &Connection, timetable_id: i64, formation_id: i64) -> Result<(), String> {
    let exists: i64 = conn.query_row(
        "SELECT COUNT(*) FROM timetable_formations WHERE timetable_id = ?1 AND formation_id = ?2",
        params![timetable_id, formation_id], |r| r.get(0),
    ).map_err(|e| e.to_string())?;
    if exists == 0 {
        conn.execute(
            "INSERT INTO timetable_formations (timetable_id, formation_id) VALUES (?1, ?2)",
            params![timetable_id, formation_id],
        ).map_err(|e| e.to_string())?;
    }
    Ok(())
}

// =================================================================== Timetables
//
// `timetables` row identity is `(route_id, service_name)` — the same
// service can exist on multiple routes with different schedules.
// Schedule rows (`timetable_entries`) are rewritten wholesale per upsert.

pub struct TimetableUpsert<'a> {
    pub route_id:                i64,
    pub formation_id:            Option<i64>,
    pub section_id:              Option<i64>,
    pub service_name:            &'a str,
    pub current_service_name:    &'a str,
    pub scenario_display_name:   &'a str,
    pub service_type:            &'a str, // "passenger" / "cargo" / ...
    pub source:                  &'a str, // "Timetable" / "Scenario" / "Training"
    pub start_time:              &'a str,
    pub duration:                &'a str,
    pub conductor_compatible:    bool,
    pub playable:                bool,
    pub bound:                   &'a str,
    pub service:                 &'a str,
    pub contributor:             &'a str,
    pub coordinates_contributor: &'a str,
}

pub fn upsert_timetable(conn: &Connection, t: &TimetableUpsert) -> Result<i64, String> {
    if t.service_name.is_empty() {
        return Err("timetable needs a service_name".into());
    }
    let id_opt: Option<i64> = conn.query_row(
        "SELECT id FROM timetables WHERE route_id = ?1 AND service_name = ?2",
        params![t.route_id, t.service_name],
        |r| r.get(0),
    ).ok();
    let id = if let Some(id) = id_opt {
        conn.execute(
            "UPDATE timetables \
             SET formation_id = ?1, service_type = ?2, contributor = ?3, \
                 coordinates_contributor = ?4, start_time = ?5, duration = ?6, \
                 section_id = ?7, conductor_compatible = ?8, bound = ?9, \
                 service = ?10, current_service_name = ?11, source = ?12, \
                 playable = ?13, scenario_display_name = ?14 \
             WHERE id = ?15",
            params![
                t.formation_id, t.service_type, opt_string(t.contributor),
                opt_string(t.coordinates_contributor), opt_string(t.start_time),
                opt_string(t.duration), t.section_id,
                t.conductor_compatible as i64, opt_string(t.bound),
                opt_string(t.service), opt_string(t.current_service_name),
                opt_string(t.source), t.playable as i64,
                opt_string(t.scenario_display_name),
                id,
            ],
        ).map_err(|e| e.to_string())?;
        id
    } else {
        conn.execute(
            "INSERT INTO timetables \
             (service_name, route_id, formation_id, service_type, contributor, \
              coordinates_contributor, start_time, duration, section_id, \
              conductor_compatible, bound, service, current_service_name, source, \
              playable, scenario_display_name) \
             VALUES (?1,?2,?3,?4,?5,?6,?7,?8,?9,?10,?11,?12,?13,?14,?15,?16)",
            params![
                t.service_name, t.route_id, t.formation_id, t.service_type,
                opt_string(t.contributor), opt_string(t.coordinates_contributor),
                opt_string(t.start_time), opt_string(t.duration), t.section_id,
                t.conductor_compatible as i64, opt_string(t.bound),
                opt_string(t.service), opt_string(t.current_service_name),
                opt_string(t.source), t.playable as i64,
                opt_string(t.scenario_display_name),
            ],
        ).map_err(|e| e.to_string())?;
        conn.last_insert_rowid()
    };
    Ok(id)
}

/// Find-or-create a section for `(route_id, name)` and return its id.
/// Sections are the timetable file's `TimetableName` (one section per
/// timetable .uasset, grouping all of its services) — mirrors hud-go's
/// `findOrCreateSection`. Relies on the `UNIQUE(route_id, name)` index so
/// re-extraction is idempotent. Returns None for an empty name.
pub fn get_or_create_section(
    conn: &Connection,
    route_id: i64,
    name: &str,
) -> Result<Option<i64>, String> {
    let name = name.trim();
    if name.is_empty() {
        return Ok(None);
    }
    conn.execute(
        "INSERT OR IGNORE INTO sections (route_id, name) VALUES (?1, ?2)",
        params![route_id, name],
    ).map_err(|e| e.to_string())?;
    conn.query_row(
        "SELECT id FROM sections WHERE route_id = ?1 AND name = ?2",
        params![route_id, name],
        |r| r.get(0),
    ).map(Some).map_err(|e| e.to_string())
}

/// Link a timetable to a section via the `timetable_sections` junction.
/// Idempotent through `UNIQUE(timetable_id, section_id)`. The timetable's
/// own `section_id` column is set by `upsert_timetable`; this junction is
/// what the timetable-detail / section-filter queries read.
pub fn link_timetable_section(
    conn: &Connection,
    timetable_id: i64,
    section_id: i64,
) -> Result<(), String> {
    conn.execute(
        "INSERT OR IGNORE INTO timetable_sections (timetable_id, section_id) VALUES (?1, ?2)",
        params![timetable_id, section_id],
    ).map_err(|e| e.to_string())?;
    Ok(())
}

/// Look up an action's id from the seed `timetable_actions` table. The
/// seed contains the 8 canonical strings hud-go's schedule builder
/// emits. Returns None when the action isn't seeded — caller usually
/// stores `NULL` then so the entry is still queryable.
pub fn action_id_for(conn: &Connection, action: &str) -> Option<i64> {
    if action.is_empty() { return None }
    conn.query_row(
        "SELECT id FROM timetable_actions WHERE name = ?1",
        [action], |r| r.get(0),
    ).ok()
}

pub struct EntryRow<'a> {
    pub action_id:        Option<i64>,
    pub details:          &'a str,
    pub location_id:      Option<i64>,
    pub structure_number: &'a str,
    pub structure:        &'a str,
    pub time1:            &'a str,
    pub time2:            &'a str,
    pub latitude:         &'a str,
    pub longitude:        &'a str,
    pub api_name:         &'a str,
    pub sort_order:       i64,
    pub coord_source:     &'a str,
    pub cargo:            &'a str,
    pub waiting_time:     &'a str,
}

/// Replace every `timetable_entries` row for the given timetable.
pub fn rewrite_timetable_entries(
    conn: &Connection,
    timetable_id: i64,
    entries: &[EntryRow<'_>],
) -> Result<u64, String> {
    conn.execute(
        "DELETE FROM timetable_entries WHERE timetable_id = ?1",
        [timetable_id],
    ).map_err(|e| e.to_string())?;
    let mut n = 0u64;
    for e in entries {
        conn.execute(
            "INSERT INTO timetable_entries \
             (timetable_id, action_id, details, location_id, structure_number, \
              structure, time1, time2, latitude, longitude, api_name, sort_order, \
              coord_source, cargo, waiting_time) \
             VALUES (?1,?2,?3,?4,?5,?6,?7,?8,?9,?10,?11,?12,?13,?14,?15)",
            params![
                timetable_id, e.action_id, opt_string(e.details), e.location_id,
                opt_string(e.structure_number), opt_string(e.structure),
                opt_string(e.time1), opt_string(e.time2),
                opt_string(e.latitude), opt_string(e.longitude),
                opt_string(e.api_name), e.sort_order,
                opt_string(e.coord_source), opt_string(e.cargo),
                opt_string(e.waiting_time),
            ],
        ).map_err(|e| e.to_string())?;
        n += 1;
    }
    Ok(n)
}

// =================================================================== Locations
//
// `locations.name` is unique per (route_id, name) by convention. We dedupe
// in-app rather than relying on a unique constraint hud-go's schema
// doesn't enforce.

/// Get-or-create a `locations` row. Returns the id.
/// Replace every `pak_rvds` row for a single pak. Mirrors hud-go's
/// catalog scan which keeps the row-set in sync with what's currently
/// extracted. Each row is one RVD asset under `pak_path`; on a
/// re-extract we DELETE then INSERT so dropped RVDs don't linger.
pub struct PakRvdRow<'a> {
    pub pak_path:             &'a str,
    pub asset_path:           &'a str,
    pub rvd:                  &'a Rvd,
    pub drivable:             bool,
    pub substitutable_unit:   bool,
    pub has_guard_controls:   bool,
    pub service_types:        i64,
    pub regions:              &'a str,
}

pub fn rewrite_pak_rvds(conn: &Connection, pak_path: &str, rows: &[PakRvdRow<'_>]) -> Result<u64, String> {
    conn.execute("DELETE FROM pak_rvds WHERE pak_path = ?1", [pak_path])
        .map_err(|e| e.to_string())?;
    let mut n = 0u64;
    for r in rows {
        let length_opt = if r.rvd.approximate_length_m > 0.0 { Some(r.rvd.approximate_length_m as f64) } else { None };
        let speed_opt  = if r.rvd.max_speed_kph        > 0.0 { Some(r.rvd.max_speed_kph as f64) }        else { None };
        let power_opt  = if r.rvd.max_power_kw         > 0.0 { Some(r.rvd.max_power_kw as f64) }         else { None };
        let axles_opt  = if r.rvd.powered_axle_count   > 0   { Some(r.rvd.powered_axle_count as i64) }   else { None };
        let electrification_json = if r.rvd.electrification.is_empty() {
            None
        } else {
            serde_json::to_string(&r.rvd.electrification).ok()
        };
        conn.execute(
            "INSERT INTO pak_rvds \
             (pak_path, asset_path, rail_vehicle_class, friendly_name, \
              livery_id, vehicle_category, approximate_length_m, drivable, \
              substitutable_unit, has_guard_controls, service_types, regions, \
              is_electric, max_speed_kph, max_power_kw, powered_axle_count, \
              manufacturer_name, engine_description, type_description, \
              thumbnail_asset_ref, electrification_json) \
             VALUES (?1,?2,?3,?4,?5,?6,?7,?8,?9,?10,?11,?12,?13,?14,?15,?16,?17,?18,?19,?20,?21)",
            params![
                r.pak_path, r.asset_path,
                opt_string(&r.rvd.rail_vehicle_class),
                opt_string(&r.rvd.friendly_name),
                opt_string(&r.rvd.livery_id),
                opt_string(&r.rvd.vehicle_category),
                length_opt,
                r.drivable as i64,
                r.substitutable_unit as i64,
                r.has_guard_controls as i64,
                r.service_types,
                opt_string(r.regions),
                r.rvd.is_electric as i64,
                speed_opt, power_opt, axles_opt,
                opt_string(&r.rvd.manufacturer_name),
                opt_string(&r.rvd.engine_description),
                opt_string(&r.rvd.type_description),
                opt_string(&r.rvd.thumbnail_asset_ref),
                electrification_json,
            ],
        ).map_err(|e| e.to_string())?;
        n += 1;
    }
    Ok(n)
}

/// Result of [`reconcile_train_classes`]. Surfaced to the UI by the
/// dev-tools "Rebuild train_classes" button.
#[derive(Debug, Default, Clone, serde::Serialize)]
pub struct ReconcileResult {
    pub linked:             i64,
    pub duplicates_skipped: i64,
    pub inserted:           i64,
    pub backfilled:         i64,
    pub thumbs_fixed:       i64,
    pub total_train_classes: i64,
    pub total_with_rvc:     i64,
}

/// Sync `train_classes` rows with `pak_rvds`. Verbatim port of
/// hud-go's `catalog.ReconcileTrainClasses`. Idempotent.
pub fn reconcile_train_classes(conn: &Connection) -> Result<ReconcileResult, String> {
    let mut res = ReconcileResult::default();

    // Step 1: link "ghost" rows (NULL rvc) by friendly_name → pak_rvds.
    //
    // Done one row at a time on purpose. `train_classes.rail_vehicle_class`
    // carries a UNIQUE index, so when two name-variants ("Class 166 FGB"
    // and "Class 166") both resolve to the same rvc ('Class166'), a single
    // bulk UPDATE assigns both the same value and trips the constraint —
    // which aborted the ENTIRE reconcile, so Step 3's is_drivable /
    // type_description backfill never ran and every EMU / cab-car / steam
    // class whose lead car shares a class with non-drivable cars fell off
    // the Train Classes tab. Linking sequentially lets us skip a target
    // that's already taken instead of throwing.
    let ghosts: Vec<(i64, String)> = {
        let mut s = conn.prepare(
            "SELECT id, name FROM train_classes
             WHERE (rail_vehicle_class IS NULL OR rail_vehicle_class = '')
               AND name IN (SELECT DISTINCT friendly_name FROM pak_rvds
                            WHERE friendly_name IS NOT NULL AND friendly_name != '')"
        ).map_err(|e| e.to_string())?;
        let r = s.query_map([], |r| Ok((r.get::<_, i64>(0)?, r.get::<_, String>(1)?)))
            .map_err(|e| e.to_string())?;
        r.filter_map(|x| x.ok()).collect()
    };
    for (id, name) in ghosts {
        let target: Option<String> = conn.query_row(
            "SELECT MAX(rail_vehicle_class) FROM pak_rvds
             WHERE friendly_name = ?1 AND rail_vehicle_class IS NOT NULL AND rail_vehicle_class != ''",
            [&name], |r| r.get(0),
        ).ok().flatten();
        let Some(rvc) = target else { continue };
        let taken: bool = conn.query_row(
            "SELECT EXISTS(SELECT 1 FROM train_classes WHERE rail_vehicle_class = ?1)",
            [&rvc], |r| r.get(0),
        ).unwrap_or(false);
        if taken { res.duplicates_skipped += 1; continue; }
        if conn.execute(
            "UPDATE train_classes SET rail_vehicle_class = ?1 WHERE id = ?2",
            params![rvc, id],
        ).is_ok() { res.linked += 1; }
    }

    // Step 2: INSERT pak_rvds rows whose rvc isn't yet in train_classes.
    // OR IGNORE so a single row that collides on UNIQUE(name) — e.g. rvc
    // 'Bi-Level' carries friendly_name "Rotem Bi-Level Cab Car Metrolink"
    // which already belongs to the 'Rotem' class — is skipped instead of
    // aborting the whole INSERT (which otherwise left every genuinely-new
    // catalog class un-inserted). Non-fatal regardless, so Step 3 runs.
    let n = conn.execute(
        "INSERT OR IGNORE INTO train_classes
             (name, rail_vehicle_class, livery_id, typical_length_m,
              is_electric, max_speed_kph, max_power_kw, powered_axle_count,
              manufacturer_name, engine_description, type_description,
              vehicle_category, is_drivable, thumbnail_path)
         SELECT
             MAX(friendly_name),
             rail_vehicle_class,
             MAX(livery_id),
             MAX(approximate_length_m),
             MAX(is_electric),
             MAX(max_speed_kph),
             MAX(max_power_kw),
             MAX(powered_axle_count),
             MAX(manufacturer_name),
             MAX(engine_description),
             MAX(type_description),
             MAX(vehicle_category),
             MAX(drivable),
             NULL
         FROM pak_rvds
         WHERE rail_vehicle_class IS NOT NULL AND rail_vehicle_class != ''
           AND rail_vehicle_class NOT IN (
             SELECT rail_vehicle_class FROM train_classes
             WHERE rail_vehicle_class IS NOT NULL AND rail_vehicle_class != ''
           )
         GROUP BY rail_vehicle_class",
        [],
    ).map(|n| n as i64).unwrap_or(0);
    res.inserted = n;

    // Step 3: backfill snapshot fields onto rows that have an rvc link.
    // Non-fatal — this is the step that lights up the Train Classes tab
    // (is_drivable / type_description), so it must run even if 1/2 hiccup.
    let n = conn.execute(
        "UPDATE train_classes
         SET typical_length_m   = COALESCE(typical_length_m,   (SELECT MAX(approximate_length_m) FROM pak_rvds WHERE rail_vehicle_class = train_classes.rail_vehicle_class)),
             is_electric        = COALESCE(is_electric,        (SELECT MAX(is_electric)          FROM pak_rvds WHERE rail_vehicle_class = train_classes.rail_vehicle_class)),
             max_speed_kph      = COALESCE(max_speed_kph,      (SELECT MAX(max_speed_kph)        FROM pak_rvds WHERE rail_vehicle_class = train_classes.rail_vehicle_class)),
             max_power_kw       = COALESCE(max_power_kw,       (SELECT MAX(max_power_kw)        FROM pak_rvds WHERE rail_vehicle_class = train_classes.rail_vehicle_class)),
             powered_axle_count = COALESCE(powered_axle_count, (SELECT MAX(powered_axle_count)  FROM pak_rvds WHERE rail_vehicle_class = train_classes.rail_vehicle_class)),
             manufacturer_name  = COALESCE(manufacturer_name,  (SELECT MAX(manufacturer_name)   FROM pak_rvds WHERE rail_vehicle_class = train_classes.rail_vehicle_class)),
             engine_description = COALESCE(engine_description, (SELECT MAX(engine_description)  FROM pak_rvds WHERE rail_vehicle_class = train_classes.rail_vehicle_class)),
             type_description   = COALESCE(type_description,   (SELECT MAX(type_description)    FROM pak_rvds WHERE rail_vehicle_class = train_classes.rail_vehicle_class)),
             vehicle_category   = COALESCE(vehicle_category,   (SELECT MAX(vehicle_category)    FROM pak_rvds WHERE rail_vehicle_class = train_classes.rail_vehicle_class)),
             is_drivable        = COALESCE((SELECT MAX(drivable)            FROM pak_rvds WHERE rail_vehicle_class = train_classes.rail_vehicle_class), is_drivable)
         WHERE rail_vehicle_class IS NOT NULL AND rail_vehicle_class != ''",
        [],
    ).map(|n| n as i64).unwrap_or(0);
    res.backfilled = n;

    // Step 4: re-stamp thumbnail_path for any class whose PNG exists.
    let exe_dir = std::env::current_exe()
        .ok().and_then(|p| p.parent().map(|p| p.to_path_buf()))
        .unwrap_or_else(|| std::path::PathBuf::from("."));
    let png_dir = exe_dir.join("resources").join("images").join("train_classes");
    let mut stmt = conn.prepare("SELECT id, name FROM train_classes").map_err(|e| e.to_string())?;
    let rows = stmt.query_map([], |r| Ok((r.get::<_, i64>(0)?, r.get::<_, String>(1)?)))
        .map_err(|e| e.to_string())?;
    let mut thumbs_fixed = 0i64;
    for row in rows {
        let (id, name) = row.map_err(|e| e.to_string())?;
        if name.is_empty() { continue; }
        let sanitised = crate::uasset_texture::sanitise_thumbnail_name(&name);
        if sanitised.is_empty() { continue; }
        let png = png_dir.join(format!("{sanitised}.png"));
        if !png.is_file() { continue; }
        let url = format!("/images/train_classes/{sanitised}.png");
        let n = conn.execute(
            "UPDATE train_classes SET thumbnail_path = ?1 \
             WHERE id = ?2 AND (thumbnail_path IS NULL OR thumbnail_path != ?1)",
            params![url, id],
        ).map_err(|e| e.to_string())?;
        thumbs_fixed += n as i64;
    }
    res.thumbs_fixed = thumbs_fixed;

    res.total_train_classes = conn.query_row("SELECT COUNT(*) FROM train_classes", [], |r| r.get(0)).unwrap_or(0);
    res.total_with_rvc      = conn.query_row(
        "SELECT COUNT(*) FROM train_classes WHERE rail_vehicle_class IS NOT NULL AND rail_vehicle_class != ''",
        [], |r| r.get(0)
    ).unwrap_or(0);
    Ok(res)
}

/// Stamp `train_classes.thumbnail_path` (web-relative URL like
/// `/images/train_classes/<sanitised>.png`). Looks up the class by
/// `name` (FriendlyName) — the same identity `upsert_train_class` uses.
/// No-op when the class isn't found, so the caller can call this from
/// the texture-extract loop without pre-checking.
pub fn set_train_class_thumbnail(conn: &Connection, name: &str, url_path: &str) -> Result<(), String> {
    conn.execute(
        "UPDATE train_classes SET thumbnail_path = ?1 WHERE name = ?2",
        params![url_path, name],
    ).map_err(|e| e.to_string())?;
    Ok(())
}

/// Faithful port of hud-go's `FixTrainClassThumbnails`
/// (internal/catalog/trainclasses.go). Re-resolves every
/// `train_classes.thumbnail_path` by probing candidate PNG stems against
/// the on-disk thumbnails dir, first-that-exists wins. Candidate order:
///   1. FriendlyNames of every `pak_rvds` row sharing this class's
///      `rail_vehicle_class`, ordered to PREFER non-TrainingCentre paks —
///      multiple RVDs share an rvc (e.g. "BNSF SD70ACe" ships in Cajon
///      Pass AND Training Centre); the canonical livery's render is the
///      one users want, not the gray tutorial placeholder.
///   2. The `rail_vehicle_class` itself (catches DLCs whose FriendlyName
///      differs from the row name).
///   3. The row name (last-ditch).
/// Only writes when a candidate PNG actually exists on disk, so it never
/// leaves a dangling `thumbnail_path`. Idempotent. Returns rows updated.
pub fn fix_train_class_thumbnails(conn: &Connection, thumbs_dir: &std::path::Path) -> u64 {
    let rows: Vec<(i64, String, String)> = {
        let mut stmt = match conn.prepare(
            "SELECT id, COALESCE(name,''), COALESCE(rail_vehicle_class,'') FROM train_classes",
        ) { Ok(s) => s, Err(_) => return 0 };
        let mapped = stmt.query_map([], |r| {
            Ok((r.get::<_, i64>(0)?, r.get::<_, String>(1)?, r.get::<_, String>(2)?))
        });
        match mapped { Ok(m) => m.filter_map(Result::ok).collect(), Err(_) => return 0 }
    };
    let san = crate::uasset_texture::sanitise_thumbnail_name;
    let mut fixed = 0u64;
    for (id, name, rvc) in rows {
        let mut candidates: Vec<String> = Vec::new();
        // Friendly names from pak_rvds whose rail_vehicle_class matches this
        // class's rvc OR its name. hud-go matches on rvc alone, but Rust
        // sometimes leaves rvc NULL on rows whose `name` IS the
        // rail_vehicle_class (e.g. "A3"/"Jubilee"/"Stanier 8F"); matching on
        // the name too bridges that so the same canonical PNG (Flying
        // Scotsman, LMS Jubilee, …) resolves. Non-TrainingCentre liveries
        // are preferred so the gray tutorial render never wins.
        if !rvc.is_empty() || !name.is_empty() {
            if let Ok(mut stmt) = conn.prepare(
                "SELECT DISTINCT pv.friendly_name \
                 FROM pak_rvds pv LEFT JOIN pak_catalog pc ON pc.pak_path = pv.pak_path \
                 WHERE pv.rail_vehicle_class IN (?1, ?2) \
                   AND pv.friendly_name IS NOT NULL AND pv.friendly_name != '' \
                 ORDER BY CASE WHEN pc.codename = 'TrainingCentre' THEN 1 ELSE 0 END, \
                          pv.friendly_name",
            ) {
                if let Ok(m) = stmt.query_map(params![rvc, name], |r| r.get::<_, String>(0)) {
                    let mut seen = std::collections::HashSet::new();
                    for f in m.filter_map(Result::ok) {
                        if seen.insert(f.clone()) { candidates.push(san(&f)); }
                    }
                }
            }
        }
        if !rvc.is_empty() { candidates.push(san(&rvc)); }
        if !name.is_empty() { candidates.push(san(&name)); }
        for stem in candidates {
            if stem.is_empty() { continue }
            if thumbs_dir.join(format!("{stem}.png")).is_file() {
                let url = format!("/images/train_classes/{stem}.png");
                if conn.execute(
                    "UPDATE train_classes SET thumbnail_path = ?1 WHERE id = ?2",
                    params![url, id],
                ).is_ok() { fixed += 1; }
                break;
            }
        }
    }
    fixed
}

/// Remove `formations` rows left dangling after a route/timetable
/// re-import — hud-go's `deleteOrphanFormations` (route.go), minus its
/// train_classes prune. A re-extraction wipes-and-rewrites a route's
/// timetables; any `formations` row whose every referrer was just deleted
/// is dead weight. Run after each extraction so "Load my DLC" stays
/// idempotent and never accumulates orphaned consist rows.
///
/// NOTE: we deliberately do NOT delete `train_classes` rows with no
/// formation. Our reconcile (Step 2) seeds train_classes from every
/// drivable RVC in the catalog so the Train Classes tab is COMPLETE —
/// pruning class rows that no formation happens to lead would drop real
/// drivable units (e.g. the NJT "Multi-Level Commuter Cab Car", which
/// leads push-pull sets whose formation class_id points at the loco).
/// The tab's own `is_drivable`/`type_description` filter hides genuine
/// non-drivable catalog rows; they don't need deleting.
///
/// Returns `(formations_deleted, 0)` — the second slot kept for the
/// callers' log format.
pub fn delete_orphan_formations(conn: &Connection) -> (u64, u64) {
    let f = conn.execute(
        "DELETE FROM formations \
         WHERE id NOT IN (SELECT formation_id FROM route_formations    WHERE formation_id IS NOT NULL) \
           AND id NOT IN (SELECT formation_id FROM timetable_formations WHERE formation_id IS NOT NULL) \
           AND id NOT IN (SELECT formation_id FROM section_formations   WHERE formation_id IS NOT NULL) \
           AND id NOT IN (SELECT formation_id FROM timetables           WHERE formation_id IS NOT NULL)",
        [],
    ).unwrap_or(0) as u64;
    (f, 0)
}

pub fn upsert_location(conn: &Connection, route_id: i64, name: &str) -> Result<i64, String> {
    if name.is_empty() {
        return Err("location needs a name".into());
    }
    if let Ok(id) = conn.query_row(
        "SELECT id FROM locations WHERE route_id = ?1 AND name = ?2",
        params![route_id, name],
        |r| r.get(0),
    ) {
        return Ok(id);
    }
    conn.execute(
        "INSERT INTO locations (route_id, name) VALUES (?1, ?2)",
        params![route_id, name],
    ).map_err(|e| e.to_string())?;
    Ok(conn.last_insert_rowid())
}

// =================================================================== Coordinates
//
// route_coordinates / timetable_coordinates store one JSON blob per route
// or timetable — the parsed polyline. We overwrite per upsert (the
// extractor emits the canonical version each run).

pub fn write_route_coordinates(conn: &Connection, route_id: i64, json_blob: &str) -> Result<(), String> {
    let existed: i64 = conn.query_row(
        "SELECT COUNT(*) FROM route_coordinates WHERE route_id = ?1",
        [route_id], |r| r.get(0),
    ).map_err(|e| e.to_string())?;
    if existed > 0 {
        conn.execute(
            "UPDATE route_coordinates \
             SET coordinates = ?1, updated_at = CURRENT_TIMESTAMP \
             WHERE route_id = ?2",
            params![json_blob, route_id],
        ).map_err(|e| e.to_string())?;
    } else {
        conn.execute(
            "INSERT INTO route_coordinates (route_id, coordinates) VALUES (?1, ?2)",
            params![route_id, json_blob],
        ).map_err(|e| e.to_string())?;
    }
    Ok(())
}

// =================================================================== Route locations
//
// `route_locations` is the per-route platform anchor list — one row per
// physical platform with its ribbon_guid-derived lat/lng. We rewrite
// wholesale per import so adding/removing a DLC pak doesn't leave
// stale entries.

pub struct RouteLocationRow<'a> {
    pub name:      &'a str,
    pub platform:  &'a str,
    pub bound:     &'a str,
    pub latitude:  f64,
    pub longitude: f64,
}

pub fn rewrite_route_locations(conn: &Connection, route_id: i64, rows: &[RouteLocationRow<'_>]) -> Result<u64, String> {
    conn.execute("DELETE FROM route_locations WHERE route_id = ?1", [route_id])
        .map_err(|e| e.to_string())?;
    let mut n = 0u64;
    for r in rows {
        // Upsert the matching `locations` row first so we can stamp the
        // foreign key. `upsert_location` is idempotent.
        let location_id = upsert_location(conn, route_id, r.name).ok();
        conn.execute(
            "INSERT INTO route_locations \
             (route_id, location_id, name, bound, platform, latitude, longitude) \
             VALUES (?1, ?2, ?3, ?4, ?5, ?6, ?7)",
            params![
                route_id, location_id, r.name,
                opt_string(r.bound), opt_string(r.platform),
                r.latitude, r.longitude,
            ],
        ).map_err(|e| e.to_string())?;
        n += 1;
    }
    Ok(n)
}

// =================================================================== Map features
//
// Per-route track-side feature rows (car_stop_signs / track_markers). Both
// tables are rewritten wholesale per import — we delete every existing row
// for `route_id` and re-insert from the freshly extracted set.

pub struct CarStopRow<'a> {
    pub platform_name:    &'a str,
    pub ribbon_guid:      &'a str,
    pub location:         f64,
    pub max_rail_vehicles: i64,
    pub latitude:         f64,
    pub longitude:        f64,
}

pub fn rewrite_car_stop_signs(conn: &Connection, route_id: i64, rows: &[CarStopRow<'_>]) -> Result<u64, String> {
    conn.execute("DELETE FROM car_stop_signs WHERE route_id = ?1", [route_id])
        .map_err(|e| e.to_string())?;
    let mut n = 0u64;
    for r in rows {
        conn.execute(
            "INSERT INTO car_stop_signs \
             (route_id, platform_name, ribbon_guid, location, max_rail_vehicles, latitude, longitude) \
             VALUES (?1, ?2, ?3, ?4, ?5, ?6, ?7)",
            params![
                route_id, opt_string(r.platform_name), r.ribbon_guid,
                r.location, r.max_rail_vehicles, r.latitude, r.longitude,
            ],
        ).map_err(|e| e.to_string())?;
        n += 1;
    }
    Ok(n)
}

pub struct TrackMarkerRow<'a> {
    pub name:        &'a str,
    pub marker_type: &'a str,
    pub ribbon_guid: &'a str,
    pub location:    Option<f64>,
    pub start:       Option<f64>,
    pub end:         Option<f64>,
    pub line_side:   &'a str,
    pub latitude:    f64,
    pub longitude:   f64,
}

pub fn rewrite_track_markers(conn: &Connection, route_id: i64, rows: &[TrackMarkerRow<'_>]) -> Result<u64, String> {
    conn.execute("DELETE FROM track_markers WHERE route_id = ?1", [route_id])
        .map_err(|e| e.to_string())?;
    let mut n = 0u64;
    for r in rows {
        conn.execute(
            "INSERT INTO track_markers \
             (route_id, name, marker_type, ribbon_guid, location, start, end, line_side, latitude, longitude) \
             VALUES (?1, ?2, ?3, ?4, ?5, ?6, ?7, ?8, ?9, ?10)",
            params![
                route_id, r.name, opt_string(r.marker_type), r.ribbon_guid,
                r.location, r.start, r.end, opt_string(r.line_side),
                r.latitude, r.longitude,
            ],
        ).map_err(|e| e.to_string())?;
        n += 1;
    }
    Ok(n)
}

// Signals / switches / collectables — point features cookedmap already
// projects to lat/lng but the original port never persisted. Tables are
// created on demand (no migration framework) and rewritten wholesale per
// route, same as car_stop_signs / track_markers above.

pub struct SignalRow<'a> {
    pub signal_id:         &'a str,
    pub signal_type:       &'a str,
    pub ribbon_guid:       &'a str,
    pub location_fraction: f64,
    pub latitude:          f64,
    pub longitude:         f64,
}

pub fn rewrite_signals(conn: &Connection, route_id: i64, rows: &[SignalRow<'_>]) -> Result<u64, String> {
    conn.execute_batch(
        "CREATE TABLE IF NOT EXISTS signals ( \
            id INTEGER PRIMARY KEY AUTOINCREMENT, route_id INTEGER NOT NULL, \
            signal_id TEXT, signal_type TEXT, ribbon_guid TEXT, \
            location_fraction REAL, latitude REAL, longitude REAL); \
         CREATE INDEX IF NOT EXISTS idx_signals_route ON signals(route_id);",
    ).map_err(|e| e.to_string())?;
    conn.execute("DELETE FROM signals WHERE route_id = ?1", [route_id])
        .map_err(|e| e.to_string())?;
    let mut n = 0u64;
    for r in rows {
        conn.execute(
            "INSERT INTO signals \
             (route_id, signal_id, signal_type, ribbon_guid, location_fraction, latitude, longitude) \
             VALUES (?1, ?2, ?3, ?4, ?5, ?6, ?7)",
            params![
                route_id, opt_string(r.signal_id), opt_string(r.signal_type),
                r.ribbon_guid, r.location_fraction, r.latitude, r.longitude,
            ],
        ).map_err(|e| e.to_string())?;
        n += 1;
    }
    Ok(n)
}

pub struct SwitchRow<'a> {
    pub jct_guid:             &'a str,
    pub node_guid:            &'a str,
    pub manually_controlled:  bool,
    pub latitude:             f64,
    pub longitude:            f64,
}

pub fn rewrite_switches(conn: &Connection, route_id: i64, rows: &[SwitchRow<'_>]) -> Result<u64, String> {
    conn.execute_batch(
        "CREATE TABLE IF NOT EXISTS switches ( \
            id INTEGER PRIMARY KEY AUTOINCREMENT, route_id INTEGER NOT NULL, \
            jct_guid TEXT, node_guid TEXT, manually_controlled INTEGER, \
            latitude REAL, longitude REAL); \
         CREATE INDEX IF NOT EXISTS idx_switches_route ON switches(route_id);",
    ).map_err(|e| e.to_string())?;
    conn.execute("DELETE FROM switches WHERE route_id = ?1", [route_id])
        .map_err(|e| e.to_string())?;
    let mut n = 0u64;
    for r in rows {
        conn.execute(
            "INSERT INTO switches \
             (route_id, jct_guid, node_guid, manually_controlled, latitude, longitude) \
             VALUES (?1, ?2, ?3, ?4, ?5, ?6)",
            params![
                route_id, opt_string(r.jct_guid), opt_string(r.node_guid),
                r.manually_controlled as i64, r.latitude, r.longitude,
            ],
        ).map_err(|e| e.to_string())?;
        n += 1;
    }
    Ok(n)
}

pub struct CollectableRow<'a> {
    pub actor_class:   &'a str,
    pub instance_name: &'a str,
    pub collectable_id: &'a str,
    pub latitude:      f64,
    pub longitude:     f64,
}

pub fn rewrite_collectables(conn: &Connection, route_id: i64, rows: &[CollectableRow<'_>]) -> Result<u64, String> {
    conn.execute_batch(
        "CREATE TABLE IF NOT EXISTS collectables ( \
            id INTEGER PRIMARY KEY AUTOINCREMENT, route_id INTEGER NOT NULL, \
            actor_class TEXT, instance_name TEXT, collectable_id TEXT, \
            latitude REAL, longitude REAL); \
         CREATE INDEX IF NOT EXISTS idx_collectables_route ON collectables(route_id);",
    ).map_err(|e| e.to_string())?;
    conn.execute("DELETE FROM collectables WHERE route_id = ?1", [route_id])
        .map_err(|e| e.to_string())?;
    let mut n = 0u64;
    for r in rows {
        conn.execute(
            "INSERT INTO collectables \
             (route_id, actor_class, instance_name, collectable_id, latitude, longitude) \
             VALUES (?1, ?2, ?3, ?4, ?5, ?6)",
            params![
                route_id, opt_string(r.actor_class), opt_string(r.instance_name),
                opt_string(r.collectable_id), r.latitude, r.longitude,
            ],
        ).map_err(|e| e.to_string())?;
        n += 1;
    }
    Ok(n)
}

pub fn write_timetable_coordinates(
    conn: &Connection,
    timetable_id: i64,
    json_blob: &str,
    coord_source: &str,
) -> Result<(), String> {
    let existed: i64 = conn.query_row(
        "SELECT COUNT(*) FROM timetable_coordinates WHERE timetable_id = ?1",
        [timetable_id], |r| r.get(0),
    ).map_err(|e| e.to_string())?;
    if existed > 0 {
        conn.execute(
            "UPDATE timetable_coordinates \
             SET coordinates = ?1, coord_source = ?2 \
             WHERE timetable_id = ?3",
            params![json_blob, opt_string(coord_source), timetable_id],
        ).map_err(|e| e.to_string())?;
    } else {
        conn.execute(
            "INSERT INTO timetable_coordinates (timetable_id, coordinates, coord_source) \
             VALUES (?1, ?2, ?3)",
            params![timetable_id, json_blob, opt_string(coord_source)],
        ).map_err(|e| e.to_string())?;
    }
    Ok(())
}

/// Convenience: the RouteDefinition's country code from the asset is a
/// short ISO code ("UK", "US"…). hud-go's importer derives a long-form
/// name from a small lookup table; we mirror just the common cases and
/// fall back to the code itself for unknowns. The country gets created
/// in the DB on first encounter, so unknown codes are still tracked —
/// the display name just stays as the code until corrected manually.
impl RouteDefinition {
    fn country_code_long(&self) -> String {
        match self.country_code.as_str() {
            "UK" | "GB"  => "United Kingdom".into(),
            "US" | "USA" => "United States".into(),
            "DE"         => "Germany".into(),
            "FR"         => "France".into(),
            "CH"         => "Switzerland".into(),
            "AT"         => "Austria".into(),
            "CA"         => "Canada".into(),
            "IT"         => "Italy".into(),
            "NL"         => "Netherlands".into(),
            "CZ"         => "Czech Republic".into(),
            ""           => "Unknown".into(),
            other        => other.into(),
        }
    }
}
