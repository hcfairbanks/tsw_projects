PRAGMA foreign_keys = ON;

-- ── Settings ─────────────────────────────────────────────────────────────────

CREATE TABLE IF NOT EXISTS setting (
  key TEXT PRIMARY KEY,
  value TEXT
);

INSERT OR IGNORE INTO setting (key, value) VALUES ('default_tsw_version', '6');

-- ── Lookup tables ────────────────────────────────────────────────────────────

CREATE TABLE IF NOT EXISTS country (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  name TEXT UNIQUE NOT NULL
);

CREATE TABLE IF NOT EXISTS dlc_type (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  name TEXT UNIQUE NOT NULL
);

CREATE TABLE IF NOT EXISTS platform (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  name TEXT UNIQUE NOT NULL
);

CREATE TABLE IF NOT EXISTS train (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  name TEXT UNIQUE NOT NULL
);

-- ── Core DLC record ──────────────────────────────────────────────────────────

CREATE TABLE IF NOT EXISTS dlc (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  content_name TEXT NOT NULL,
  steam_name TEXT,
  gameplay_guide TEXT,
  short_name TEXT,
  acronym TEXT,
  developer TEXT,
  route_length_m REAL,
  route_length_km REAL,
  release_date TEXT,
  tsw_versions TEXT DEFAULT '[]',
  new_lighting INTEGER DEFAULT 0,
  new_track_shadows INTEGER DEFAULT 0,
  route_hopping INTEGER DEFAULT 0,
  conductor_mode INTEGER DEFAULT 0,
  announcements INTEGER DEFAULT 0,
  train_faults INTEGER DEFAULT 0,
  gen8_compatible INTEGER DEFAULT 0,
  requirements_raw TEXT,
  expansions_raw TEXT,
  additional_playable_raw TEXT,
  layer_requirements_raw TEXT,
  non_playable_layers_raw TEXT,
  platform_differences TEXT,
  owned INTEGER DEFAULT 0,
  in_cart INTEGER DEFAULT 0,
  price TEXT,
  price_value REAL,
  price_original TEXT,
  price_original_value REAL,
  price_currency TEXT,
  price_discount TEXT,
  price_updated_at TEXT,
  dlc_type_id INTEGER REFERENCES dlc_type(id) ON DELETE SET NULL,
  country_id INTEGER REFERENCES country(id) ON DELETE SET NULL
);

-- ── Base trains included in a DLC ────────────────────────────────────────────

CREATE TABLE IF NOT EXISTS dlc_train (
  dlc_id INTEGER REFERENCES dlc(id) ON DELETE CASCADE,
  train_id INTEGER REFERENCES train(id) ON DELETE CASCADE,
  PRIMARY KEY (dlc_id, train_id)
);

-- ── Store / purchase links ───────────────────────────────────────────────────

CREATE TABLE IF NOT EXISTS store_link (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  dlc_id INTEGER REFERENCES dlc(id) ON DELETE CASCADE,
  console_platform TEXT NOT NULL,
  size_gb TEXT,
  store_url TEXT
);

-- ── Steam price history ─────────────────────────────────────────────────────

CREATE TABLE IF NOT EXISTS steam_price (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  dlc_id INTEGER NOT NULL REFERENCES dlc(id) ON DELETE CASCADE,
  price TEXT,
  price_value REAL,
  price_original TEXT,
  price_original_value REAL,
  price_currency TEXT,
  price_discount TEXT,
  fetched_at TEXT NOT NULL
);

CREATE INDEX IF NOT EXISTS idx_steam_price_dlc ON steam_price(dlc_id, fetched_at);

-- ── Price fetch error log ───────────────────────────────────────────────────

CREATE TABLE IF NOT EXISTS price_fetch_error (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  dlc_id INTEGER REFERENCES dlc(id) ON DELETE CASCADE,
  dlc_name TEXT,
  steam_link TEXT,
  reason TEXT,
  fetched_at TEXT
);

-- ── Document links (manuals, timetables, guides) ────────────────────────────

CREATE TABLE IF NOT EXISTS document_link (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  dlc_id INTEGER REFERENCES dlc(id) ON DELETE CASCADE,
  doc_type TEXT NOT NULL CHECK(doc_type IN ('manual', 'timetable', 'guide', 'gameplay_guide')),
  label TEXT,
  url TEXT
);

-- ── Playable layers ──────────────────────────────────────────────────────────
-- "Buy needed_dlc → get locomotive running playable services on route_dlc"

CREATE TABLE IF NOT EXISTS layer (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  route_dlc_id INTEGER REFERENCES dlc(id) ON DELETE CASCADE,
  locomotive TEXT NOT NULL,
  needed_dlc TEXT,
  service_type TEXT,
  num_services INTEGER
);

-- ── AI layers ────────────────────────────────────────────────────────────────
-- "Buy needed_dlc → locomotive appears as AI traffic on route_dlc"

CREATE TABLE IF NOT EXISTS ai_layer (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  route_dlc_id INTEGER REFERENCES dlc(id) ON DELETE CASCADE,
  locomotive TEXT NOT NULL,
  needed_dlc TEXT,
  service_type TEXT
);

-- ── Substitutions ────────────────────────────────────────────────────────────
-- "Buy needed_dlc → locomotive replaces replaced_locomotive on route_dlc"

CREATE TABLE IF NOT EXISTS substitution (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  route_dlc_id INTEGER REFERENCES dlc(id) ON DELETE CASCADE,
  locomotive TEXT NOT NULL,
  needed_dlc TEXT,
  replaced_locomotive TEXT
);
