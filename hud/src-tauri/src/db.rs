//! Read-only (mostly) access to the SQLite database (tsw_hud.db). Override the
//! path with the HUD_DB env var. Index queries return stringified rows so one
//! generic table renderer can drive every index tab.

use rusqlite::{Connection, OpenFlags};

pub fn db_path() -> String {
    // hud now owns its own copy of tsw_hud.db (4 GB, lives at
    // `hud/resources/db/tsw_hud.db`). Resolution order:
    //   1) HUD_DB env var (explicit override — keep for ops + tests)
    //   2) <hud crate>/../resources/db/tsw_hud.db   (the canonical local copy)
    //   3) <hud crate>/../resources/tsw_hud.db      (flat fallback)
    // We deliberately no longer fall back to hud-go/ or hud-rust/ — those
    // legacy locations now drift relative to hud's own copy (the user has
    // to re-copy when the Go extractor refreshes them). Returning the
    // expected local path even when it doesn't exist makes the open error
    // point at the right spot for debugging.
    if let Ok(p) = std::env::var("HUD_DB") {
        return p;
    }
    let crate_root = std::path::PathBuf::from(env!("CARGO_MANIFEST_DIR"));
    let resources = crate_root.join("..").join("resources");
    let primary = resources.join("db").join("tsw_hud.db");
    if primary.exists() {
        return primary.to_string_lossy().into_owned();
    }
    let flat = resources.join("tsw_hud.db");
    if flat.exists() {
        return flat.to_string_lossy().into_owned();
    }
    primary.to_string_lossy().into_owned()
}

// Open a read connection ONCE and reuse it across calls. The previous
// per-call `Connection::open_with_flags` paid a ~2.5 s cold-cache penalty
// on the 4 GB DB every time timetables_search ran (page next/prev), which
// is what the user reported as "slow". Pragmas tune the cache + memory map
// for big-file random-read patterns.
// `None` means "cache vacated — reopen on next access". `Some(c)` is the
// cached read handle that callers stream through `with_read`. The vacated
// state is what `drop_cached_read()` enters before the caller (the DB-refresh
// IPC) replaces the underlying file: with no live handle Windows lets the
// file be renamed/deleted, and the next `with_read` reopens against the
// freshly-copied DB.
static SHARED_READ: std::sync::OnceLock<std::sync::Mutex<Option<Connection>>> =
    std::sync::OnceLock::new();

fn open_read_conn() -> Result<Connection, String> {
    let c = Connection::open_with_flags(
        db_path(),
        OpenFlags::SQLITE_OPEN_READ_ONLY | OpenFlags::SQLITE_OPEN_URI,
    )
    .map_err(|e| format!("open db: {e}"))?;
    // 64 MB mmap + 64 MB page cache — enough for the random-access hot
    // ranges of a 4 GB DB without eating real memory aggressively.
    // mmap_size: how much of the DB is mapped into the address space.
    // cache_size: negative = KiB of in-RAM page cache.
    let _ = c.execute_batch(
        // mmap 1 GiB of the 4 GiB file (Windows handles partial mappings
        // fine). Page cache 256 MiB. ANALYZE updates the optimiser stats
        // so the plan matches what python's sqlite3 + ANALYZE'd file picks.
        "PRAGMA mmap_size = 1073741824;\n\
         PRAGMA cache_size = -262144;\n\
         PRAGMA temp_store = MEMORY;\n\
         PRAGMA query_only = 1;",
    );
    Ok(c)
}

/// Dump the query plan for a given SQL — diagnostic only.
pub fn explain_plan(sql: &str) -> Result<String, String> {
    with_read(|c| {
        let mut s = c
            .prepare(&format!("EXPLAIN QUERY PLAN {sql}"))
            .map_err(|e| e.to_string())?;
        let rows = s
            .query_map([], |row| {
                Ok(format!(
                    "{:>3} {:>3} {:>3} {}",
                    row.get::<_, i64>(0)?,
                    row.get::<_, i64>(1)?,
                    row.get::<_, i64>(2)?,
                    row.get::<_, String>(3)?
                ))
            })
            .map_err(|e| e.to_string())?;
        let mut out = String::new();
        for r in rows {
            out.push_str(&r.map_err(|e| e.to_string())?);
            out.push('\n');
        }
        Ok(out)
    })
}

/// Use a closure to access the cached read connection. Hot read paths
/// (search, lookup, list) should call this instead of `conn()` so they
/// pay the connection-open cost only once per process. If the cache was
/// vacated by `drop_cached_read()` (DB-refresh path), this transparently
/// reopens against the current `db_path()`.
pub fn with_read<F, R>(f: F) -> Result<R, String>
where
    F: FnOnce(&Connection) -> Result<R, String>,
{
    let cell = SHARED_READ.get_or_init(|| std::sync::Mutex::new(None));
    let mut guard = cell.lock().map_err(|e| format!("db mutex poisoned: {e}"))?;
    if guard.is_none() {
        *guard = Some(open_read_conn()?);
    }
    f(guard.as_ref().expect("just populated above"))
}

/// Release the cached read connection so the DB-refresh IPC can replace the
/// underlying file. The next `with_read` will reopen against the new file.
pub fn drop_cached_read() {
    if let Some(cell) = SHARED_READ.get() {
        if let Ok(mut guard) = cell.lock() {
            *guard = None;
        }
    }
}

#[allow(dead_code)]
fn conn() -> Result<Connection, String> {
    // Legacy entry point — still here for code that hasn't been migrated
    // to `with_read`. Opens a fresh connection (slow on cold cache).
    open_read_conn()
}

/// Read-write connection for the rare manual-edit endpoints (e.g. saving a
/// HUD-adjusted timetable_entry coordinate). Fresh connection per call — calls
/// are user-paced and rusqlite::Connection isn't Send across threads.
pub fn write_conn() -> Result<Connection, String> {
    Connection::open_with_flags(
        db_path(),
        OpenFlags::SQLITE_OPEN_READ_WRITE | OpenFlags::SQLITE_OPEN_URI,
    )
    .map_err(|e| format!("open db (rw): {e}"))
}

/// Idempotent schema top-ups for columns added after the shipped DB was
/// baked. Run once at startup. Each ALTER is guarded by a PRAGMA check so
/// re-running (or running against a DB that already has the column) is a
/// no-op. Failures are non-fatal — logged by the caller.
pub fn ensure_schema() -> Result<(), String> {
    let c = write_conn()?;
    let has_col = |table: &str, col: &str| -> bool {
        c.prepare(&format!("PRAGMA table_info({table})"))
            .and_then(|mut s| {
                let rows = s.query_map([], |r| r.get::<_, String>(1))?;
                let mut found = false;
                for r in rows.flatten() {
                    if r.eq_ignore_ascii_case(col) { found = true; break; }
                }
                Ok(found)
            })
            .unwrap_or(false)
    };
    // routes.is_real_route — user-editable flag distinguishing real drivable
    // routes from loco/cargo/content-pack DLC. Defaults to 1 (real); the user
    // marks the exceptions. Drives the routes-index visibility filter.
    if !has_col("routes", "is_real_route") {
        c.execute(
            "ALTER TABLE routes ADD COLUMN is_real_route INTEGER NOT NULL DEFAULT 1",
            [],
        ).map_err(|e| format!("add routes.is_real_route: {e}"))?;
    }
    // timetables.scenario_display_name — the scenario/tutorial's user-facing
    // title (from the sibling *_Definition.uasset), used by the export to
    // replace generic service names (PlayerService/AI_Service) with the
    // scenario name. Stored, never displayed in the catalog UI.
    if !has_col("timetables", "scenario_display_name") {
        c.execute(
            "ALTER TABLE timetables ADD COLUMN scenario_display_name TEXT",
            [],
        ).map_err(|e| format!("add timetables.scenario_display_name: {e}"))?;
    }
    Ok(())
}

/// Persist a manual HUD coordinate edit. Marks coord_source='manual' so the
/// next reload distinguishes user fixes from extractor / predictive values.
pub fn update_entry_coords(
    entry_id: i64,
    latitude: f64,
    longitude: f64,
    tile_x: Option<i64>,
    tile_y: Option<i64>,
) -> Result<(), String> {
    let c = write_conn()?;
    c.execute(
        "UPDATE timetable_entries SET latitude = ?1, longitude = ?2, tile_x = ?3, tile_y = ?4, coord_source = 'manual' WHERE id = ?5",
        rusqlite::params![latitude, longitude, tile_x, tile_y, entry_id],
    )
    .map(|_| ())
    .map_err(|e| format!("update entry {entry_id}: {e}"))
}

pub type Page = Result<(Vec<Vec<String>>, i64), String>;

/// Timetables-specific page result: `(rows, ids, total)`. The parallel `ids`
/// vector lets the index page link each row to its detail page without a
/// second IPC round-trip.
pub type TtPage = Result<(Vec<Vec<String>>, Vec<i64>, i64), String>;

/// Run a paginated, single-`?1`-search query: `count_sql` and `data_sql` must
/// both use `?1` for the LIKE pattern; `data_sql` also uses `?2` (limit) `?3`
/// (offset). `ncols` is how many columns `data_sql` selects.
fn search_page(count_sql: &str, data_sql: &str, ncols: usize, search: &str, limit: i64, offset: i64) -> Page {
    let c = conn()?;
    let like = format!("%{}%", search.trim());
    let total: i64 = c.query_row(count_sql, [&like], |r| r.get(0)).map_err(|e| e.to_string())?;
    let mut stmt = c.prepare(data_sql).map_err(|e| e.to_string())?;
    let rows = stmt
        .query_map(rusqlite::params![like, limit, offset], |row| {
            let mut v = Vec::with_capacity(ncols);
            for i in 0..ncols {
                // Everything is selected as TEXT (see queries) so this is safe.
                v.push(row.get::<_, String>(i)?);
            }
            Ok(v)
        })
        .map_err(|e| e.to_string())?;
    let mut out = Vec::new();
    for r in rows {
        out.push(r.map_err(|e| e.to_string())?);
    }
    Ok((out, total))
}

use rusqlite::types::Value;

/// All timetable-index filters (mirrors hud-go's GetPaginated query params).
/// `Deserialize` so the Timetables page can submit it via IPC; `serde(default)`
/// on each field so the frontend can post a partial object.
#[derive(Default, Clone, serde::Serialize, serde::Deserialize)]
#[serde(default)]
pub struct TtFilter {
    pub search: String,
    pub country_id: String,
    pub route_id: String,
    pub class_id: String,
    pub section: String, // "", "__none__", or a section name
    pub start_min: String,
    pub start_max: String,
    pub dur_min: String,
    pub dur_max: String,
    pub stops_min: String,
    pub stops_max: String,
    pub conductor: String,     // "", "yes", "no"
    pub service_type: String,  // "", "passenger", "freight"
    pub playable: String,      // "", "yes", "no" (dev only)
    pub source: String,        // "", "Timetable", "Scenario", "Training"
    pub coord_source: String,  // "", "backend"/"automatic"/"imported"/"predictive"/"none"
    pub sort_by: String,       // "" => newest first
    pub sort_dir: String,      // "ASC" / "DESC"
    pub dev: bool,
}

fn push_int(conds: &mut Vec<String>, params: &mut Vec<Value>, cond: &str, raw: &str) {
    if let Ok(n) = raw.trim().parse::<i64>() {
        conds.push(cond.to_string());
        params.push(Value::Integer(n));
    }
}

/// Faithful port of hud-go's timetables GetPaginated query (filters + sort +
/// pagination). Returns the 10 display columns per row + total match count.
pub fn timetables_search(f: &TtFilter, page: i64, per_page: i64) -> TtPage {
    with_read(|c| timetables_search_inner(c, f, page, per_page))
}

fn timetables_search_inner(c: &Connection, f: &TtFilter, page: i64, per_page: i64) -> TtPage {
    let mut joins: Vec<String> = Vec::new();
    let mut conds: Vec<String> = Vec::new();
    let mut params: Vec<Value> = Vec::new();

    if !f.country_id.trim().is_empty() {
        push_int(&mut conds, &mut params, "r.country_id = ?", &f.country_id);
    }
    if !f.route_id.trim().is_empty() {
        push_int(&mut conds, &mut params, "t.route_id = ?", &f.route_id);
    }
    if !f.class_id.trim().is_empty() {
        joins.push("JOIN timetable_formations tt_class ON tt_class.timetable_id = t.id JOIN formations fr_class ON fr_class.id = tt_class.formation_id".into());
        push_int(&mut conds, &mut params, "fr_class.class_id = ?", &f.class_id);
    }
    if f.section == "__none__" {
        conds.push("NOT EXISTS (SELECT 1 FROM timetable_sections ts WHERE ts.timetable_id = t.id)".into());
    } else if !f.section.is_empty() {
        joins.push("JOIN timetable_sections ts ON ts.timetable_id = t.id JOIN sections sec ON sec.id = ts.section_id".into());
        conds.push("sec.name = ?".into());
        params.push(Value::Text(f.section.clone()));
    }
    if f.coord_source == "none" {
        conds.push("NOT EXISTS (SELECT 1 FROM timetable_coordinates tc WHERE tc.timetable_id = t.id)".into());
    } else if !f.coord_source.is_empty() {
        joins.push("JOIN timetable_coordinates tc_cs ON tc_cs.timetable_id = t.id".into());
        conds.push("tc_cs.coord_source = ?".into());
        params.push(Value::Text(f.coord_source.clone()));
    }
    match f.conductor.as_str() {
        "yes" => conds.push("t.conductor_compatible = 1".into()),
        "no" => conds.push("(t.conductor_compatible = 0 OR t.conductor_compatible IS NULL)".into()),
        _ => {}
    }
    if !f.search.trim().is_empty() {
        conds.push("(t.service_name LIKE ? OR t.service LIKE ? OR t.current_service_name LIKE ?)".into());
        let pat = format!("%{}%", f.search.trim());
        params.push(Value::Text(pat.clone()));
        params.push(Value::Text(pat.clone()));
        params.push(Value::Text(pat));
    }
    for (raw, cond) in [
        (&f.start_min, "t.start_time >= ?"),
        (&f.start_max, "t.start_time <= ?"),
        (&f.dur_min, "t.duration >= ?"),
        (&f.dur_max, "t.duration <= ?"),
    ] {
        if !raw.trim().is_empty() {
            conds.push(cond.to_string());
            params.push(Value::Text(raw.trim().to_string()));
        }
    }
    if !f.service_type.is_empty() {
        conds.push("t.service_type = ?".into());
        params.push(Value::Text(f.service_type.clone()));
    }
    // Explicit playable choice always wins (yes/no), regardless of dev
    // mode. When no choice is made and dev mode is off, default to
    // playable=1 — same "hide non-playable junk by default" rule
    // hud-rust's egui index used.
    match f.playable.as_str() {
        "yes" => conds.push("t.playable = 1".into()),
        "no"  => conds.push("(t.playable = 0 OR t.playable IS NULL)".into()),
        _ => {
            if !f.dev {
                conds.push("t.playable = 1".into());
            }
        }
    }
    if !f.source.is_empty() {
        conds.push("t.source = ?".into());
        params.push(Value::Text(f.source.clone()));
    }
    let has_stops = !f.stops_min.trim().is_empty() || !f.stops_max.trim().is_empty();
    if has_stops {
        joins.push("LEFT JOIN (SELECT te.timetable_id, COUNT(*) AS stop_count FROM timetable_entries te JOIN timetable_actions ta ON ta.id = te.action_id WHERE UPPER(ta.name) IN ('WAIT FOR SERVICE','STOP AT LOCATION') GROUP BY te.timetable_id) stops_agg ON stops_agg.timetable_id = t.id".into());
        push_int(&mut conds, &mut params, "COALESCE(stops_agg.stop_count,0) >= ?", &f.stops_min);
        push_int(&mut conds, &mut params, "COALESCE(stops_agg.stop_count,0) <= ?", &f.stops_max);
    }

    let join_sql = if joins.is_empty() { String::new() } else { format!(" {}", joins.join(" ")) };
    let where_sql = if conds.is_empty() { String::new() } else { format!(" WHERE {}", conds.join(" AND ")) };

    // total
    let count_sql = format!("SELECT COUNT(DISTINCT t.id) FROM timetables t LEFT JOIN routes r ON r.id = t.route_id{join_sql}{where_sql}");
    let total: i64 = c
        .query_row(&count_sql, rusqlite::params_from_iter(params.iter()), |row| row.get(0))
        .map_err(|e| e.to_string())?;

    // sort
    let sort_col = match f.sort_by.as_str() {
        "service_name" => "t.service_name",
        "start_time" => "t.start_time",
        "duration" => "t.duration",
        "conductor_compatible" => "t.conductor_compatible",
        "service_type" => "t.service_type",
        "source" => "t.source",
        "route_name" => "r.name",
        _ => "t.id",
    };
    let sort_dir = if f.sort_dir.eq_ignore_ascii_case("ASC") { "ASC" } else { "DESC" };

    // ─── Two-stage paginated SELECT ────────────────────────────────────────
    // Stage 1 (`page_ids`): cheap — pick the 25 ids using the WHERE / ORDER
    // BY only. This hits the primary key (or playable index) directly and
    // doesn't trigger USE TEMP B-TREE FOR ORDER BY across all 82k rows.
    //
    // Stage 2 (main SELECT): run the 6 expensive correlated subqueries on
    // ONLY the 25 picked ids instead of on every row in the filtered set.
    //
    // Before: ~2.5 s per page (subqueries × 82k rows + temp sort).
    // After : ~10–50 ms per page.
    let mut data_params = params.clone();
    data_params.push(Value::Integer(per_page));
    data_params.push(Value::Integer(page * per_page));

    // Trailing `t.id` column so the frontend can link each row to its
    // detail page without a second IPC. Row parsing splits the last
    // column off into the `ids` vector below.
    let data_sql = format!(
        "WITH page_ids AS ( \
           SELECT t.id FROM timetables t \
             LEFT JOIN routes r ON r.id = t.route_id{join_sql}{where_sql} \
           GROUP BY t.id \
           ORDER BY {sort_col} {sort_dir}, t.id DESC \
           LIMIT ? OFFSET ? \
         ) \
         SELECT \
           COALESCE(t.service_name,'') , \
           COALESCE(r.name,'') , \
           COALESCE((SELECT s.name FROM sections s WHERE s.id=t.section_id), (SELECT s2.name FROM timetable_sections ts2 JOIN sections s2 ON s2.id=ts2.section_id WHERE ts2.timetable_id=t.id LIMIT 1), '') , \
           COALESCE((SELECT GROUP_CONCAT(f.name, ', ') FROM timetable_formations tf JOIN formations f ON f.id=tf.formation_id WHERE tf.timetable_id=t.id), (SELECT f2.name FROM formations f2 WHERE f2.id=t.formation_id), '') , \
           COALESCE(CAST(t.start_time AS TEXT),'') , \
           COALESCE(CAST(t.duration AS TEXT),'') , \
           CAST((SELECT COUNT(*) FROM timetable_entries te3 JOIN timetable_actions ta3 ON ta3.id=te3.action_id WHERE te3.timetable_id=t.id AND UPPER(ta3.name) IN ('WAIT FOR SERVICE','STOP AT LOCATION')) AS TEXT) , \
           COALESCE((SELECT tc.coord_source FROM timetable_coordinates tc WHERE tc.timetable_id=t.id LIMIT 1),'') , \
           CASE WHEN t.conductor_compatible=1 THEN 'Yes' ELSE 'No' END , \
           t.id \
         FROM page_ids p \
         JOIN timetables t ON t.id = p.id \
         LEFT JOIN routes r ON r.id = t.route_id \
         ORDER BY {sort_col} {sort_dir}, t.id DESC"
    );
    let _t = std::time::Instant::now();

    let mut stmt = c.prepare(&data_sql).map_err(|e| e.to_string())?;
    let rows = stmt
        .query_map(rusqlite::params_from_iter(data_params.iter()), |row| {
            let mut v = Vec::with_capacity(9);
            for i in 0..9 {
                v.push(row.get::<_, String>(i)?);
            }
            let id: i64 = row.get(9)?;
            Ok((v, id))
        })
        .map_err(|e| e.to_string())?;
    let mut out = Vec::new();
    let mut ids = Vec::new();
    for r in rows {
        let (cells, id) = r.map_err(|e| e.to_string())?;
        out.push(cells);
        ids.push(id);
    }
    Ok((out, ids, total))
}

/// Dropdown option lists for the filter bar.
pub fn id_name(sql: &str) -> Result<Vec<(i64, String)>, String> {
    let c = conn()?;
    let mut stmt = c.prepare(sql).map_err(|e| e.to_string())?;
    let rows = stmt
        .query_map([], |row| Ok((row.get::<_, i64>(0)?, row.get::<_, String>(1)?)))
        .map_err(|e| e.to_string())?;
    let mut out = Vec::new();
    for r in rows {
        out.push(r.map_err(|e| e.to_string())?);
    }
    Ok(out)
}
/// Countries with their 2-letter code (for flag rendering).
pub fn country_list() -> Result<Vec<(i64, String, String)>, String> {
    let c = conn()?;
    let mut stmt = c.prepare("SELECT id, COALESCE(name,''), COALESCE(code,'') FROM countries ORDER BY name").map_err(|e| e.to_string())?;
    let rows = stmt
        .query_map([], |row| Ok((row.get::<_, i64>(0)?, row.get::<_, String>(1)?, row.get::<_, String>(2)?)))
        .map_err(|e| e.to_string())?;
    let mut out = Vec::new();
    for r in rows { out.push(r.map_err(|e| e.to_string())?); }
    Ok(out)
}
/// (id, name, country_id) for every real route — country_id lets the Timetables
/// filter bar narrow the route dropdown to the selected country. Routes flagged
/// `is_real_route = 0` (loco / cargo / content-pack DLC) are excluded so they
/// don't clutter the dropdown. COALESCE guards a DB predating the column.
pub fn routes_list() -> Result<Vec<(i64, String, Option<i64>)>, String> {
    let c = conn()?;
    let mut stmt = c
        .prepare(
            "SELECT id, COALESCE(name,''), country_id FROM routes \
             WHERE COALESCE(is_real_route, 1) = 1 ORDER BY name",
        )
        .map_err(|e| e.to_string())?;
    let rows = stmt
        .query_map([], |r| {
            Ok((r.get::<_, i64>(0)?, r.get::<_, String>(1)?, r.get::<_, Option<i64>>(2)?))
        })
        .map_err(|e| e.to_string())?;
    let mut out = Vec::new();
    for r in rows { out.push(r.map_err(|e| e.to_string())?); }
    Ok(out)
}
pub fn train_classes() -> Result<Vec<(i64, String)>, String> {
    id_name("SELECT DISTINCT class_id, COALESCE(class_name,'') FROM formations WHERE class_id IS NOT NULL AND COALESCE(class_name,'')!='' ORDER BY class_name")
}
/// (name, route_id) for every section — route_id lets the Timetables filter
/// bar narrow the section dropdown to the selected route.
pub fn section_names() -> Result<Vec<(String, Option<i64>)>, String> {
    let c = conn()?;
    let mut stmt = c
        .prepare(
            "SELECT DISTINCT name, route_id FROM sections \
             WHERE COALESCE(name,'')!='' ORDER BY name",
        )
        .map_err(|e| e.to_string())?;
    let rows = stmt
        .query_map([], |row| Ok((row.get::<_, String>(0)?, row.get::<_, Option<i64>>(1)?)))
        .map_err(|e| e.to_string())?;
    let mut out = Vec::new();
    for r in rows { out.push(r.map_err(|e| e.to_string())?); }
    Ok(out)
}

pub fn routes_rows(search: &str, limit: i64, offset: i64) -> Page {
    let where_sql = "WHERE (COALESCE(r.name,'') LIKE ?1 OR COALESCE(co.name,'') LIKE ?1)";
    let count = format!("SELECT COUNT(*) FROM routes r LEFT JOIN countries co ON co.id=r.country_id {where_sql}");
    let data = format!(
        "SELECT COALESCE(r.name,''), COALESCE(co.name,''), \
                CAST((SELECT COUNT(*) FROM timetables t WHERE t.route_id=r.id) AS TEXT) \
         FROM routes r LEFT JOIN countries co ON co.id=r.country_id \
         {where_sql} ORDER BY co.name, r.name LIMIT ?2 OFFSET ?3"
    );
    search_page(&count, &data, 3, search, limit, offset)
}

/// Routes-index filters (mirrors hud-go's route handler GetPaginated).
#[derive(Default, Clone, serde::Serialize, serde::Deserialize)]
#[serde(default)]
pub struct RtFilter {
    pub search: String,
    pub country_id: String,
    pub best_data: String, // "", "1", "0"
    pub sort_by: String,   // "name" | "country_name" | "best_data" | "id" | "tsw_version"
    pub sort_dir: String,  // "ASC" / "DESC"
    pub dev: bool,
    /// When false (default), routes flagged `is_real_route = 0` (loco / cargo /
    /// content-pack DLC) are hidden. The "Non-route DLC's" toggle sets this true
    /// to reveal them.
    pub show_non_routes: bool,
}

/// Faithful port of hud-go's routes GetPaginated. Returns (rows, total) where
/// each row is [name, country_code, country_name, best_data_marker, country_id_text].
/// country_code drives the inline flag in the GUI; country_id_text is hidden
/// metadata the row can use for a click-through later.
pub fn routes_search(f: &RtFilter, page: i64, per_page: i64) -> Result<(Vec<RouteRow>, i64), String> {
    let c = conn()?;
    let mut conds: Vec<String> = Vec::new();
    let mut params: Vec<Value> = Vec::new();

    let search = f.search.trim();
    if !search.is_empty() {
        conds.push("r.name LIKE ?".to_string());
        params.push(Value::Text(format!("%{search}%")));
    }
    if let Ok(cid) = f.country_id.trim().parse::<i64>() {
        conds.push("r.country_id = ?".to_string());
        params.push(Value::Integer(cid));
    }
    match f.best_data.as_str() {
        "1" => conds.push("r.best_data = 1".to_string()),
        "0" => conds.push("r.best_data = 0".to_string()),
        _ => {}
    }
    // Hide routes the user has flagged as not-real (loco / cargo / content-pack
    // DLC). `is_real_route` defaults to 1, so nothing is hidden until the user
    // marks it on the route page. The "Non-route DLC's" toggle (show_non_routes)
    // reveals them. COALESCE guards a DB that predates the column (treated as
    // real). NOTE: deliberately not gated on dev mode — visibility is the
    // toggle's job, so the filter works the same whether or not dev mode is on.
    if !f.show_non_routes {
        conds.push("COALESCE(r.is_real_route, 1) = 1".to_string());
    }
    let where_clause = if conds.is_empty() {
        String::new()
    } else {
        format!(" WHERE {}", conds.join(" AND "))
    };

    // Sort whitelist — anything outside this falls back to the default order.
    let sort_col = match f.sort_by.as_str() {
        "name" => Some("r.name"),
        "id" => Some("r.id"),
        "tsw_version" => Some("r.tsw_version"),
        "country_name" => Some("co.name"),
        "best_data" => Some("r.best_data"),
        _ => None,
    };
    let sort_dir = if f.sort_dir.eq_ignore_ascii_case("ASC") { "ASC" } else { "DESC" };
    let order_by = match sort_col {
        None => " ORDER BY r.best_data DESC, r.name ASC".to_string(),
        Some(col) => format!(" ORDER BY {col} {sort_dir}, r.best_data DESC, r.name ASC"),
    };

    let count_sql = format!(
        "SELECT COUNT(*) FROM routes r LEFT JOIN countries co ON co.id = r.country_id {where_clause}"
    );
    let total: i64 = c
        .query_row(&count_sql, rusqlite::params_from_iter(params.iter()), |row| row.get(0))
        .map_err(|e| e.to_string())?;

    let offset = page * per_page;
    let data_sql = format!(
        "SELECT COALESCE(r.name,''), COALESCE(co.name,''), COALESCE(co.code,''), \
                CASE WHEN r.best_data=1 THEN 1 ELSE 0 END, \
                COALESCE(CAST(r.country_id AS TEXT),''), \
                COALESCE(CAST(r.tsw_version AS TEXT),''), \
                CAST(r.id AS TEXT) \
         FROM routes r LEFT JOIN countries co ON co.id = r.country_id \
         {where_clause}{order_by} LIMIT ? OFFSET ?"
    );
    let mut data_params = params.clone();
    data_params.push(Value::Integer(per_page));
    data_params.push(Value::Integer(offset));

    let mut stmt = c.prepare(&data_sql).map_err(|e| e.to_string())?;
    let rows_iter = stmt
        .query_map(rusqlite::params_from_iter(data_params.iter()), |row| {
            Ok(RouteRow {
                name: row.get::<_, String>(0)?,
                country_name: row.get::<_, String>(1)?,
                country_code: row.get::<_, String>(2)?,
                best_data: row.get::<_, i64>(3)? == 1,
                country_id: row.get::<_, String>(4)?,
                tsw_version: row.get::<_, String>(5)?,
                id: row.get::<_, String>(6)?,
            })
        })
        .map_err(|e| e.to_string())?;
    let mut rows = Vec::new();
    for r in rows_iter {
        rows.push(r.map_err(|e| e.to_string())?);
    }
    Ok((rows, total))
}

/// Materialized routes-index row. `country_code` is the ISO2 used for the flag.
#[derive(Clone, serde::Serialize)]
pub struct RouteRow {
    pub id: String,
    pub name: String,
    pub country_id: String,
    pub country_name: String,
    pub country_code: String,
    pub best_data: bool,
    pub tsw_version: String,
}

// ---- HUD detect + route-data --------------------------------------------

/// One candidate returned by /api/timetables/detect's underlying query.
pub struct DetectCandidate {
    pub id: i64,
    pub service_name: String,
    pub current_service_name: String,
    pub route_id: Option<i64>,
    pub route_name: String,
    pub first_coord_prefix: String, // first ~200 chars of the coords JSON blob
}

/// Find timetables whose current_service_name matches. Distance ranking happens
/// in the caller (kept here as a plain DB query to stay testable).
pub fn detect_candidates(current_service_name: &str) -> Result<Vec<DetectCandidate>, String> {
    let c = conn()?;
    let mut stmt = c
        .prepare(
            "SELECT t.id, COALESCE(t.service_name,''), COALESCE(t.current_service_name,''), \
                    t.route_id, COALESCE(r.name,''), \
                    COALESCE(SUBSTR(tc.coordinates, 1, 200), '') \
             FROM timetables t \
             LEFT JOIN routes r ON r.id = t.route_id \
             LEFT JOIN timetable_coordinates tc ON tc.timetable_id = t.id \
             WHERE LOWER(TRIM(t.current_service_name)) = LOWER(TRIM(?1)) \
                OR LOWER(TRIM(t.service_name)) = LOWER(TRIM(?1))",
        )
        .map_err(|e| e.to_string())?;
    let rows = stmt
        .query_map([current_service_name], |row| {
            Ok(DetectCandidate {
                id: row.get(0)?,
                service_name: row.get(1)?,
                current_service_name: row.get(2)?,
                route_id: row.get::<_, Option<i64>>(3)?,
                route_name: row.get(4)?,
                first_coord_prefix: row.get(5)?,
            })
        })
        .map_err(|e| e.to_string())?;
    let mut out = Vec::new();
    for r in rows {
        out.push(r.map_err(|e| e.to_string())?);
    }
    Ok(out)
}

/// Pull (latitude, longitude) out of the head of a coordinates JSON blob like
/// `[{"latitude":42.35,"longitude":-71.05},…`. Cheap parse of the first object.
pub fn parse_first_coord(prefix: &str) -> Option<(f64, f64)> {
    let s = prefix.trim_start();
    // Current extractor format: a flat list of [lng, lat] pairs.
    if s.starts_with("[[") {
        let inner = &s[2..];
        let end = inner.find(']')?;
        let mut it = inner[..end].split(',');
        let lng: f64 = it.next()?.trim().parse().ok()?;
        let lat: f64 = it.next()?.trim().parse().ok()?;
        return Some((lat, lng));
    }
    // Legacy format: list of {latitude, longitude} objects.
    let end = prefix.find('}')?;
    let head = &prefix[..=end];
    let trimmed = head.trim_start_matches('[');
    let v: serde_json::Value = serde_json::from_str(trimmed).ok()?;
    let lat = v.get("latitude")?.as_f64()?;
    let lng = v.get("longitude")?.as_f64()?;
    Some((lat, lng))
}

/// Great-circle distance in metres. Used to disambiguate `Detect` candidates
/// when TSW reuses a headcode across routes (the WCML / XC "1S37" case).
pub fn haversine_m(lat1: f64, lng1: f64, lat2: f64, lng2: f64) -> f64 {
    const R: f64 = 6_371_000.0;
    let to_rad = std::f64::consts::PI / 180.0;
    let dlat = (lat2 - lat1) * to_rad;
    let dlng = (lng2 - lng1) * to_rad;
    let a = (dlat / 2.0).sin().powi(2)
        + (lat1 * to_rad).cos() * (lat2 * to_rad).cos() * (dlng / 2.0).sin().powi(2);
    2.0 * R * a.sqrt().asin()
}

// ---- build the per-timetable feature blob ------------------------------

/// Build (or rebuild) the timetable_map_features blob for `timetable_id`.
/// Mirrors hud-go's buildTimetableMapFeaturesCached. Returns Ok(false) when
/// there's nothing to filter (no route_id, no route_coordinates, etc.) so
/// callers can quietly skip — same "no work" semantics hud-go uses.
pub fn build_timetable_map_features(timetable_id: i64) -> Result<bool, String> {
    use crate::features::{filter_route_features_for_timetable, FilterOptions, ScheduleEntryRef, ServiceCoord};
    let c = write_conn()?;

    // Need the timetable's route to load the parent route_coordinates.
    let route_id: Option<i64> = c
        .query_row(
            "SELECT route_id FROM timetables WHERE id = ?1",
            [timetable_id],
            |r| r.get(0),
        )
        .ok()
        .flatten();
    let Some(route_id) = route_id else { return Ok(false) };

    // Route features (the full GeoJSON Feature array).
    let route_json: Option<String> = c
        .query_row(
            "SELECT coordinates FROM route_coordinates WHERE route_id = ?1",
            [route_id],
            |r| r.get::<_, Option<String>>(0),
        )
        .unwrap_or(None);
    let Some(route_json) = route_json else { return Ok(false) };
    let route_feats: Vec<serde_json::Value> =
        serde_json::from_str(&route_json).map_err(|e| format!("route_coordinates parse: {e}"))?;
    if route_feats.is_empty() {
        return Ok(false);
    }

    // Service path coords for proximity. Absent → filter still runs, only
    // proximity branches drop out (schedule-tuple match still works).
    let path_coords: Vec<ServiceCoord> = c
        .query_row(
            "SELECT coordinates FROM timetable_coordinates WHERE timetable_id = ?1",
            [timetable_id],
            |r| r.get::<_, Option<String>>(0),
        )
        .ok()
        .flatten()
        .and_then(|s| serde_json::from_str::<Vec<serde_json::Value>>(&s).ok())
        .map(|raw| {
            raw.into_iter()
                .filter_map(|v| {
                    let lat = v.get("latitude")?.as_f64()?;
                    let lng = v.get("longitude")?.as_f64()?;
                    Some(ServiceCoord { latitude: lat, longitude: lng })
                })
                .collect()
        })
        .unwrap_or_default();

    // Schedule entries (just the tuple the filter needs).
    let mut stmt = c
        .prepare(
            "SELECT COALESCE(l.name, ''), COALESCE(te.structure, ''), COALESCE(te.structure_number, '') \
             FROM timetable_entries te LEFT JOIN locations l ON l.id = te.location_id \
             WHERE te.timetable_id = ?1 ORDER BY te.sort_order",
        )
        .map_err(|e| e.to_string())?;
    let rows = stmt
        .query_map([timetable_id], |r| {
            Ok(ScheduleEntryRef {
                location: r.get(0)?,
                structure: r.get(1)?,
                structure_number: r.get(2)?,
            })
        })
        .map_err(|e| e.to_string())?;
    let mut entries: Vec<ScheduleEntryRef> = Vec::new();
    for r in rows {
        entries.push(r.map_err(|e| e.to_string())?);
    }

    let filtered =
        filter_route_features_for_timetable(&route_feats, &entries, path_coords, &FilterOptions::default());
    let blob = serde_json::to_string(&filtered).map_err(|e| format!("marshal: {e}"))?;

    c.execute(
        "INSERT INTO timetable_map_features (timetable_id, features) VALUES (?1, ?2) \
         ON CONFLICT(timetable_id) DO UPDATE SET features = excluded.features, built_at = CURRENT_TIMESTAMP",
        rusqlite::params![timetable_id, blob],
    )
    .map_err(|e| format!("upsert: {e}"))?;
    Ok(true)
}

/// Quick check used by upload-route's auto-trigger: do we already have a
/// per-timetable feature blob? If yes, the background build skips.
pub fn timetable_map_features_exists(timetable_id: i64) -> Result<bool, String> {
    let c = conn()?;
    let n: i64 = c
        .query_row(
            "SELECT COUNT(*) FROM timetable_map_features WHERE timetable_id = ?1",
            [timetable_id],
            |r| r.get(0),
        )
        .map_err(|e| e.to_string())?;
    Ok(n > 0)
}

// ---- /api/timetables/{id}/map-features ----------------------------------

/// Returns the raw `features` JSON blob for a timetable. The blob is itself a
/// JSON document (rails, signals, switches, station outlines) precomputed at
/// import time; we serve it verbatim so we don't burn CPU re-parsing it.
pub fn timetable_map_features(timetable_id: i64) -> Result<Option<String>, String> {
    let c = conn()?;
    let res = c.query_row(
        "SELECT features FROM timetable_map_features WHERE timetable_id = ?1",
        [timetable_id],
        |row| row.get::<_, String>(0),
    );
    match res {
        Ok(s) => Ok(Some(s)),
        Err(rusqlite::Error::QueryReturnedNoRows) => Ok(None),
        Err(e) => Err(e.to_string()),
    }
}

// ---- /api/routes/{id}/map-data ------------------------------------------

/// Per-route full GeoJSON fallback used by HUDs when no per-timetable
/// map-features blob exists. Same shape hud-go returns: route_id, route_name,
/// coordinates (parsed), markers, locations.
pub fn route_map_data(route_id: i64) -> Result<Option<serde_json::Value>, String> {
    use serde_json::{json, Value as JV};
    let c = conn()?;

    let route_name: String = match c.query_row(
        "SELECT COALESCE(name,'') FROM routes WHERE id = ?1",
        [route_id],
        |row| row.get(0),
    ) {
        Ok(s) => s,
        Err(rusqlite::Error::QueryReturnedNoRows) => return Ok(None),
        Err(e) => return Err(e.to_string()),
    };

    let raw_coords: JV = c
        .query_row(
            "SELECT coordinates FROM route_coordinates WHERE route_id = ?1",
            [route_id],
            |row| row.get::<_, Option<String>>(0),
        )
        .unwrap_or(None)
        .and_then(|s| serde_json::from_str(&s).ok())
        .unwrap_or_else(|| json!([]));

    // Normalize to a consistent list-of-segments so every map consumer can do
    // `segs.forEach(seg => polyline(seg))`. Stored blobs come in two shapes:
    //   • native extractor:  [[[lng,lat], …], …]   (already segmented)
    //   • merged hud-go data: [{latitude,longitude}, …]  (one flat point list)
    // A flat point list (first element is an object, or an array of numbers)
    // gets wrapped in one outer array so it reads as a single segment.
    let coordinates: JV = match &raw_coords {
        JV::Array(items) => match items.first() {
            // Flat list of {latitude,longitude} point objects → wrap as one segment.
            Some(JV::Object(_)) => json!([raw_coords]),
            // Flat list of [lng,lat] pairs (inner is a number) → wrap as one segment.
            Some(JV::Array(inner)) if matches!(inner.first(), Some(JV::Number(_))) => {
                json!([raw_coords])
            }
            // Already segmented (inner element is an array of points) or empty.
            _ => raw_coords,
        },
        _ => json!([]),
    };

    let markers = scan_rows_to_maps(
        &c,
        "SELECT * FROM route_markers WHERE route_id = ?1 ORDER BY station_name",
        route_id,
    )?;
    let locations = scan_rows_to_maps(
        &c,
        "SELECT * FROM route_locations WHERE route_id = ?1 ORDER BY name, bound, platform",
        route_id,
    )?;

    Ok(Some(json!({
        "route_id": route_id,
        "route_name": route_name,
        "coordinates": coordinates,
        "markers": markers,
        "locations": locations,
    })))
}

// ---- /api/weather-presets CRUD -----------------------------------------

const PRESET_COLS: &str =
    "id, name, temperature, cloudiness, precipitation, wetness, ground_snow, piled_snow, fog_density, created_at";

fn preset_row_to_json(row: &rusqlite::Row) -> rusqlite::Result<serde_json::Value> {
    Ok(serde_json::json!({
        "id":            row.get::<_, i64>(0)?,
        "name":          row.get::<_, String>(1)?,
        "temperature":   row.get::<_, f64>(2)?,
        "cloudiness":    row.get::<_, f64>(3)?,
        "precipitation": row.get::<_, f64>(4)?,
        "wetness":       row.get::<_, f64>(5)?,
        "ground_snow":   row.get::<_, f64>(6)?,
        "piled_snow":    row.get::<_, f64>(7)?,
        "fog_density":   row.get::<_, f64>(8)?,
        "created_at":    row.get::<_, Option<String>>(9)?,
    }))
}

pub fn weather_presets_list() -> Result<Vec<serde_json::Value>, String> {
    let c = conn()?;
    let sql = format!("SELECT {PRESET_COLS} FROM weather_presets ORDER BY name");
    let mut stmt = c.prepare(&sql).map_err(|e| e.to_string())?;
    let rows = stmt.query_map([], preset_row_to_json).map_err(|e| e.to_string())?;
    let mut out = Vec::new();
    for r in rows {
        out.push(r.map_err(|e| e.to_string())?);
    }
    Ok(out)
}

pub fn weather_preset_get(id: i64) -> Result<Option<serde_json::Value>, String> {
    let c = conn()?;
    let sql = format!("SELECT {PRESET_COLS} FROM weather_presets WHERE id = ?1");
    match c.query_row(&sql, [id], preset_row_to_json) {
        Ok(v) => Ok(Some(v)),
        Err(rusqlite::Error::QueryReturnedNoRows) => Ok(None),
        Err(e) => Err(e.to_string()),
    }
}

#[allow(clippy::too_many_arguments)]
pub fn weather_preset_create(
    name: &str,
    temperature: f64,
    cloudiness: f64,
    precipitation: f64,
    wetness: f64,
    ground_snow: f64,
    piled_snow: f64,
    fog_density: f64,
) -> Result<i64, String> {
    let c = write_conn()?;
    c.execute(
        "INSERT INTO weather_presets (name, temperature, cloudiness, precipitation, wetness, ground_snow, piled_snow, fog_density) \
         VALUES (?1, ?2, ?3, ?4, ?5, ?6, ?7, ?8)",
        rusqlite::params![name, temperature, cloudiness, precipitation, wetness, ground_snow, piled_snow, fog_density],
    )
    .map_err(|e| e.to_string())?;
    Ok(c.last_insert_rowid())
}

#[allow(clippy::too_many_arguments)]
pub fn weather_preset_update(
    id: i64,
    name: &str,
    temperature: f64,
    cloudiness: f64,
    precipitation: f64,
    wetness: f64,
    ground_snow: f64,
    piled_snow: f64,
    fog_density: f64,
) -> Result<(), String> {
    let c = write_conn()?;
    let n = c.execute(
        "UPDATE weather_presets SET name=?2, temperature=?3, cloudiness=?4, precipitation=?5, wetness=?6, ground_snow=?7, piled_snow=?8, fog_density=?9 WHERE id=?1",
        rusqlite::params![id, name, temperature, cloudiness, precipitation, wetness, ground_snow, piled_snow, fog_density],
    )
    .map_err(|e| e.to_string())?;
    if n == 0 {
        return Err(format!("preset {id} not found"));
    }
    Ok(())
}

pub fn weather_preset_delete(id: i64) -> Result<(), String> {
    let c = write_conn()?;
    c.execute("DELETE FROM weather_presets WHERE id = ?1", [id])
        .map_err(|e| e.to_string())?;
    Ok(())
}

// ---- /map picker endpoints ---------------------------------------------

/// /api/routes/with-coordinates — distinct routes that have a timetable with
/// imported coordinates. Drives the route typeahead on /map.
pub fn routes_with_coordinates() -> Result<Vec<serde_json::Value>, String> {
    let c = conn()?;
    scan_rows_to_maps_noargs(
        &c,
        "SELECT DISTINCT r.*, co.name AS country_name \
         FROM routes r LEFT JOIN countries co ON r.country_id = co.id \
         INNER JOIN timetables t ON t.route_id = r.id \
         INNER JOIN timetable_coordinates tc ON tc.timetable_id = t.id \
         ORDER BY r.name",
    )
}

/// /api/routes/{id}/formations-with-coordinates — formations linked to a
/// route via timetables (the dropdown that follows the route choice).
pub fn formations_with_coordinates(route_id: i64) -> Result<Vec<serde_json::Value>, String> {
    let c = conn()?;
    scan_rows_to_maps(
        &c,
        "SELECT DISTINCT tr.* FROM formations tr \
         INNER JOIN route_formations rt ON tr.id = rt.formation_id \
         INNER JOIN timetable_formations tt ON tt.formation_id = tr.id \
         INNER JOIN timetables t ON t.id = tt.timetable_id AND t.route_id = ?1 \
         WHERE rt.route_id = ?1 \
         ORDER BY tr.name",
        route_id,
    )
}

/// /api/timetables?route_id=&formation_id= — third-level dropdown. We do the
/// formation filter via the timetable_formations junction since a timetable
/// can list a formation either directly (timetables.formation_id) or via the
/// junction.
pub fn timetables_for_picker(
    route_id: Option<i64>,
    formation_id: Option<i64>,
) -> Result<Vec<serde_json::Value>, String> {
    let c = conn()?;
    let mut sql = String::from("SELECT t.* FROM timetables t");
    let mut params: Vec<Value> = Vec::new();
    let mut wheres: Vec<String> = Vec::new();
    if let Some(rid) = route_id {
        wheres.push("t.route_id = ?".to_string());
        params.push(Value::Integer(rid));
    }
    if let Some(fid) = formation_id {
        wheres.push(
            "(t.formation_id = ? OR EXISTS \
             (SELECT 1 FROM timetable_formations tf \
              WHERE tf.timetable_id = t.id AND tf.formation_id = ?))"
                .to_string(),
        );
        params.push(Value::Integer(fid));
        params.push(Value::Integer(fid));
    }
    if !wheres.is_empty() {
        sql.push_str(" WHERE ");
        sql.push_str(&wheres.join(" AND "));
    }
    sql.push_str(" ORDER BY t.id DESC");

    let mut stmt = c.prepare(&sql).map_err(|e| e.to_string())?;
    let col_names: Vec<String> = stmt
        .column_names()
        .into_iter()
        .map(|s| s.to_string())
        .collect();
    let col_count = col_names.len();
    let mut rows = stmt
        .query(rusqlite::params_from_iter(params.iter()))
        .map_err(|e| e.to_string())?;
    let mut out = Vec::new();
    while let Some(row) = rows.next().map_err(|e| e.to_string())? {
        out.push(row_to_json(row, &col_names, col_count)?);
    }
    Ok(out)
}

/// /api/timetables/{id} — single row.
pub fn timetable_by_id(id: i64) -> Result<Option<serde_json::Value>, String> {
    let c = conn()?;
    let mut stmt = c.prepare("SELECT * FROM timetables WHERE id = ?1").map_err(|e| e.to_string())?;
    let col_names: Vec<String> = stmt
        .column_names()
        .into_iter()
        .map(|s| s.to_string())
        .collect();
    let col_count = col_names.len();
    let mut rows = stmt.query([id]).map_err(|e| e.to_string())?;
    if let Some(row) = rows.next().map_err(|e| e.to_string())? {
        Ok(Some(row_to_json(row, &col_names, col_count)?))
    } else {
        Ok(None)
    }
}

fn row_to_json(row: &rusqlite::Row, col_names: &[String], col_count: usize) -> Result<serde_json::Value, String> {
    use rusqlite::types::ValueRef;
    use serde_json::{json, Map, Value as JV};
    let mut m = Map::new();
    for i in 0..col_count {
        let v = match row.get_ref(i).map_err(|e| e.to_string())? {
            ValueRef::Null => JV::Null,
            ValueRef::Integer(n) => json!(n),
            ValueRef::Real(f) => json!(f),
            ValueRef::Text(t) => JV::String(String::from_utf8_lossy(t).into_owned()),
            ValueRef::Blob(b) => json!(b),
        };
        m.insert(col_names[i].clone(), v);
    }
    Ok(JV::Object(m))
}

fn scan_rows_to_maps_noargs(c: &Connection, sql: &str) -> Result<Vec<serde_json::Value>, String> {
    let mut stmt = c.prepare(sql).map_err(|e| e.to_string())?;
    let col_names: Vec<String> = stmt
        .column_names()
        .into_iter()
        .map(|s| s.to_string())
        .collect();
    let col_count = col_names.len();
    let mut rows = stmt.query([]).map_err(|e| e.to_string())?;
    let mut out = Vec::new();
    while let Some(row) = rows.next().map_err(|e| e.to_string())? {
        out.push(row_to_json(row, &col_names, col_count)?);
    }
    Ok(out)
}

/// Port of hud-go's scanRowsToMaps for a `SELECT *` style query: returns each
/// row as a `{column: value}` JSON object, with type-aware conversion (null /
/// int / real / text / blob).
fn scan_rows_to_maps(c: &Connection, sql: &str, id: i64) -> Result<Vec<serde_json::Value>, String> {
    use rusqlite::types::ValueRef;
    use serde_json::{json, Map, Value as JV};

    let mut stmt = c.prepare(sql).map_err(|e| e.to_string())?;
    let col_names: Vec<String> = stmt
        .column_names()
        .into_iter()
        .map(|s| s.to_string())
        .collect();
    let col_count = col_names.len();

    let mut rows = stmt.query([id]).map_err(|e| e.to_string())?;
    let mut out: Vec<JV> = Vec::new();
    while let Some(row) = rows.next().map_err(|e| e.to_string())? {
        let mut m = Map::new();
        for i in 0..col_count {
            let v = match row.get_ref(i).map_err(|e| e.to_string())? {
                ValueRef::Null => JV::Null,
                ValueRef::Integer(n) => json!(n),
                ValueRef::Real(f) => json!(f),
                ValueRef::Text(t) => JV::String(String::from_utf8_lossy(t).into_owned()),
                ValueRef::Blob(b) => json!(b),
            };
            m.insert(col_names[i].clone(), v);
        }
        out.push(JV::Object(m));
    }
    Ok(out)
}

// ---- /api/map/route-data/{id} -------------------------------------------

pub enum MapRouteErr {
    NotFound,
    Db(String),
}
impl From<rusqlite::Error> for MapRouteErr {
    fn from(e: rusqlite::Error) -> Self { MapRouteErr::Db(e.to_string()) }
}
impl From<String> for MapRouteErr {
    fn from(e: String) -> Self { MapRouteErr::Db(e) }
}

/// Faithful port of hud-go's MapDataHandler.GetRouteDataFromDb. Returns the
/// full route-data bundle the HUDs POST back to /api/upload-route.
pub fn map_route_data(timetable_id: i64) -> Result<serde_json::Value, MapRouteErr> {
    use serde_json::{json, Map, Value as JV};
    let c = conn()?;

    // Timetable header (service_name + optional route_id).
    let (service_name, route_id): (String, Option<i64>) = match c.query_row(
        "SELECT COALESCE(service_name,''), route_id FROM timetables WHERE id = ?1",
        [timetable_id],
        |row| Ok((row.get::<_, String>(0)?, row.get::<_, Option<i64>>(1)?)),
    ) {
        Ok(t) => t,
        Err(rusqlite::Error::QueryReturnedNoRows) => return Err(MapRouteErr::NotFound),
        Err(e) => return Err(MapRouteErr::Db(e.to_string())),
    };

    // Coordinates — single SELECT, verbatim. Importer is responsible for
    // anchor adjustments / dedup / Chatham scrub, not the request path.
    let coordinates: JV = c
        .query_row(
            "SELECT coordinates FROM timetable_coordinates WHERE timetable_id = ?1",
            [timetable_id],
            |row| row.get::<_, Option<String>>(0),
        )
        .unwrap_or(None)
        .and_then(|s| serde_json::from_str(&s).ok())
        .unwrap_or_else(|| json!([]));
    let total_points = coordinates.as_array().map(|a| a.len()).unwrap_or(0);

    // Markers.
    let mut markers: Vec<JV> = Vec::new();
    {
        let mut stmt = c.prepare(
            "SELECT station_name, marker_type, latitude, longitude, platform_length \
             FROM timetable_markers WHERE timetable_id = ?1",
        )?;
        let rows = stmt.query_map([timetable_id], |row| {
            Ok((
                row.get::<_, String>(0)?,
                row.get::<_, Option<String>>(1)?,
                row.get::<_, Option<f64>>(2)?,
                row.get::<_, Option<f64>>(3)?,
                row.get::<_, Option<f64>>(4)?,
            ))
        })?;
        for r in rows {
            let (station_name, marker_type, lat, lng, plat_len) = r?;
            markers.push(json!({
                "stationName": station_name,
                "markerType": marker_type,
                "platformLength": plat_len,
                "latitude": lat,
                "longitude": lng,
            }));
        }
    }

    // Timetable entries with 3-tier coord resolution.
    let mut timetable_arr = build_timetable_array(&c, timetable_id)?;

    // car_stop_signs per entry — snug-fit ranking + direction tiebreaker.
    if let Some(route_id_v) = route_id {
        let bound: String = c
            .query_row(
                "SELECT COALESCE(LOWER(TRIM(bound)),'') FROM timetables WHERE id=?1",
                [timetable_id],
                |r| r.get(0),
            )
            .unwrap_or_default();
        let car_count: i64 = c
            .query_row(
                "SELECT COALESCE(f.car_count, 0) FROM timetables t \
                 LEFT JOIN formations f ON f.id = t.formation_id WHERE t.id = ?1",
                [timetable_id],
                |r| r.get(0),
            )
            .unwrap_or(0);

        let sign_sql = "\
            SELECT te.id, css.max_rail_vehicles, css.latitude, css.longitude \
            FROM timetable_entries te \
            JOIN locations l ON l.id = te.location_id \
            JOIN car_stop_signs css \
              ON css.route_id = ?1 \
             AND css.platform_name = TRIM(l.name || ' ' || \
                 COALESCE(te.structure, '') || ' ' || \
                 COALESCE(te.structure_number, '')) \
            WHERE te.timetable_id = ?2 \
            ORDER BY te.id, \
                     CASE \
                       WHEN css.max_rail_vehicles = ?3 THEN 0 \
                       WHEN css.max_rail_vehicles > ?3 THEN css.max_rail_vehicles - ?3 \
                       WHEN css.max_rail_vehicles = 0 THEN 99999 \
                       ELSE 99999 + (?3 - css.max_rail_vehicles) \
                     END, \
                     CASE ?4 \
                       WHEN 'northbound' THEN -css.latitude \
                       WHEN 'southbound' THEN  css.latitude \
                       WHEN 'eastbound'  THEN -css.longitude \
                       WHEN 'westbound'  THEN  css.longitude \
                       ELSE 0 \
                     END";
        let mut stmt = c.prepare(sign_sql)?;
        let rows = stmt.query_map(
            rusqlite::params![route_id_v, timetable_id, car_count, bound],
            |row| {
                Ok((
                    row.get::<_, i64>(0)?,
                    row.get::<_, i64>(1)?,
                    row.get::<_, f64>(2)?,
                    row.get::<_, f64>(3)?,
                ))
            },
        )?;
        use std::collections::HashMap;
        let mut signs_by_entry: HashMap<i64, Vec<JV>> = HashMap::new();
        for r in rows {
            let (eid, max_v, lat, lng) = r?;
            signs_by_entry.entry(eid).or_default().push(json!({
                "max_rail_vehicles": max_v,
                "latitude": lat,
                "longitude": lng,
            }));
        }
        // Attach to each item.
        for item in timetable_arr.iter_mut() {
            if let Some(eid) = item.get("id").and_then(|v| v.as_i64()) {
                if let Some(signs) = signs_by_entry.remove(&eid) {
                    if let Some(m) = item.as_object_mut() {
                        m.insert("car_stop_signs".into(), JV::Array(signs));
                    }
                }
            }
        }
    }

    // vehicleCount from formation.
    let vehicle_count: i64 = c
        .query_row(
            "SELECT COALESCE(f.car_count, 0) FROM formations f \
             JOIN timetables t ON t.formation_id = f.id WHERE t.id = ?1",
            [timetable_id],
            |r| r.get(0),
        )
        .unwrap_or(0);

    let mut out = Map::new();
    out.insert("routeName".into(), json!(service_name));
    out.insert("routeId".into(), json!(route_id));
    out.insert("timetableId".into(), json!(timetable_id));
    out.insert("totalPoints".into(), json!(total_points));
    out.insert("coordinates".into(), coordinates);
    out.insert("markers".into(), JV::Array(markers));
    out.insert("timetable".into(), JV::Array(timetable_arr));
    out.insert("vehicleCount".into(), json!(vehicle_count));
    Ok(JV::Object(out))
}

/// Port of buildTimetableArrayFromEntries. 3-tier coord resolution:
/// car_stop_sign (best) → track_marker → timetable_entries.lat/lng text.
fn build_timetable_array(c: &Connection, timetable_id: i64) -> Result<Vec<serde_json::Value>, MapRouteErr> {
    use serde_json::{json, Value as JV};
    struct Row {
        id: i64,
        _sort_order: Option<i64>,
        structure: Option<String>,
        structure_number: Option<String>,
        time1: Option<String>,
        time2: Option<String>,
        latitude_text: Option<String>,
        longitude_text: Option<String>,
        api_name: Option<String>,
        car_lat: Option<f64>,
        car_lng: Option<f64>,
        tm_lat: Option<f64>,
        tm_lng: Option<f64>,
        location: String,
        action: String,
    }

    let mut stmt = c.prepare(
        "SELECT te.id, te.sort_order, te.structure, te.structure_number, te.time1, te.time2, \
                te.latitude, te.longitude, te.api_name, \
                css.latitude AS car_lat, css.longitude AS car_lng, \
                tm.latitude  AS tm_lat,  tm.longitude  AS tm_lng, \
                COALESCE(l.name, '') AS location, \
                COALESCE(ta.name, '') AS action \
         FROM timetable_entries te \
         LEFT JOIN locations l ON te.location_id = l.id \
         LEFT JOIN timetable_actions ta ON te.action_id = ta.id \
         LEFT JOIN car_stop_signs css ON css.id = te.car_stop_sign_id \
         LEFT JOIN track_markers tm ON tm.id = te.track_marker_id \
         WHERE te.timetable_id = ?1 \
         ORDER BY te.sort_order",
    )?;
    let row_iter = stmt.query_map([timetable_id], |r| {
        Ok(Row {
            id: r.get(0)?,
            _sort_order: r.get::<_, Option<i64>>(1)?,
            structure: r.get(2)?,
            structure_number: r.get(3)?,
            time1: r.get(4)?,
            time2: r.get(5)?,
            latitude_text: r.get(6)?,
            longitude_text: r.get(7)?,
            api_name: r.get(8)?,
            car_lat: r.get(9)?,
            car_lng: r.get(10)?,
            tm_lat: r.get(11)?,
            tm_lng: r.get(12)?,
            location: r.get(13)?,
            action: r.get(14)?,
        })
    })?;
    let mut entries: Vec<Row> = Vec::new();
    for r in row_iter {
        entries.push(r?);
    }

    let mut result: Vec<JV> = Vec::new();
    let mut entry_index: i64 = 0;
    for i in 0..entries.len() {
        let e = &entries[i];

        // Resolve coord: car_stop_sign → track_marker → entry text.
        let (resolved, coord_source): (Option<(f64, f64)>, &'static str) =
            if let (Some(lat), Some(lng)) = (e.car_lat, e.car_lng) {
                (Some((lat, lng)), "car_stop_sign")
            } else if let (Some(lat), Some(lng)) = (e.tm_lat, e.tm_lng) {
                (Some((lat, lng)), "track_marker")
            } else if let (Some(lt), Some(ln)) = (e.latitude_text.as_deref(), e.longitude_text.as_deref()) {
                let (lt, ln) = (lt.trim(), ln.trim());
                if !lt.is_empty() && !ln.is_empty() {
                    match (lt.parse::<f64>(), ln.parse::<f64>()) {
                        (Ok(la), Ok(lo)) => (Some((la, lo)), "timetable_entry"),
                        _ => (None, ""),
                    }
                } else {
                    (None, "")
                }
            } else {
                (None, "")
            };
        let has_coord = resolved.is_some();

        // Skip rows the HUD/map can't show at all (no label AND no coord).
        if e.location.trim().is_empty() && !has_coord {
            continue;
        }

        let mut arrival = String::new();
        let mut departure = String::new();
        if e.action != "GO VIA LOCATION" {
            if e.action == "WAIT FOR SERVICE" {
                if let Some(t) = &e.time2 { arrival = t.clone(); }
            } else if let Some(t) = &e.time1 {
                arrival = t.clone();
            }
            if let Some(next) = entries.get(i + 1) {
                if let Some(t) = &next.time1 { departure = t.clone(); }
            }
        }

        let structure = e.structure.clone().unwrap_or_default();
        let structure_number = e.structure_number.clone().unwrap_or_default();
        let api_name = e.api_name.clone().unwrap_or_default();

        let mut item = serde_json::Map::new();
        item.insert("id".into(), json!(e.id));
        item.insert("index".into(), json!(entry_index));
        item.insert("location".into(), json!(e.location));
        item.insert("action".into(), json!(e.action));
        item.insert("arrival".into(), json!(arrival));
        item.insert("departure".into(), json!(departure));
        item.insert("structure".into(), json!(structure));
        item.insert("structure_number".into(), json!(structure_number));
        item.insert("apiName".into(), json!(api_name));
        if let Some((lat, lng)) = resolved {
            item.insert("latitude".into(), json!(lat));
            item.insert("longitude".into(), json!(lng));
            item.insert("coord_source".into(), json!(coord_source));
        }
        if e.action == "GO VIA LOCATION" {
            item.insert("isPassThrough".into(), json!(true));
        }
        result.push(JV::Object(item));
        entry_index += 1;
    }
    Ok(result)
}
