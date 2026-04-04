const express = require('express');
const { DatabaseSync } = require('node:sqlite');
const path = require('path');
const fs = require('fs');

const app = express();
const PORT = 3000;
const DB_PATH = path.join(__dirname, 'database.db');

if (!fs.existsSync(DB_PATH)) {
  console.error('Database not found. Run: node import.js');
  process.exit(1);
}

const db = new DatabaseSync(DB_PATH);
db.exec('PRAGMA foreign_keys = ON');
db.exec('PRAGMA journal_mode = WAL');

// Migrate: add columns if missing
const migrateCols = [
  ['owned', 'INTEGER DEFAULT 0'], ['in_cart', 'INTEGER DEFAULT 0'],
  ['price', 'TEXT'], ['price_value', 'REAL'],
  ['price_original', 'TEXT'], ['price_original_value', 'REAL'],
  ['price_currency', 'TEXT'], ['price_discount', 'TEXT'], ['price_updated_at', 'TEXT'],
  ['tsw_versions', "TEXT DEFAULT '[]'"],
];
for (const [col, type] of migrateCols) {
  try { db.exec(`ALTER TABLE dlc ADD COLUMN ${col} ${type}`); } catch (e) {}
}

// Migrate: tsw_1..tsw_6 booleans → tsw_versions JSON array string
try {
  const hasTsw1 = db.prepare(`SELECT tsw_1 FROM dlc LIMIT 1`).get();
  if (hasTsw1 !== undefined) {
    const rows = db.prepare(`SELECT id, tsw_1, tsw_2, tsw_3, tsw_4, tsw_5, tsw_6, tsw_versions FROM dlc`).all();
    const upd = db.prepare(`UPDATE dlc SET tsw_versions = ? WHERE id = ?`);
    for (const r of rows) {
      if (r.tsw_versions && r.tsw_versions !== '[]') continue;
      const arr = [];
      for (let v = 1; v <= 6; v++) { if (r[`tsw_${v}`]) arr.push(v); }
      upd.run(JSON.stringify(arr), r.id);
    }
    for (let v = 1; v <= 6; v++) {
      try { db.exec(`ALTER TABLE dlc DROP COLUMN tsw_${v}`); } catch (e) {}
    }
    console.log('Migrated tsw_1..tsw_6 → tsw_versions');
  }
} catch (e) { /* columns already dropped */ }

// Migrate: remove steam_link from dlc table
try {
  db.prepare(`SELECT steam_link FROM dlc LIMIT 1`).get();
  db.exec(`ALTER TABLE dlc DROP COLUMN steam_link`);
  console.log('Dropped steam_link column from dlc');
} catch (e) { /* already dropped */ }

// Migrate: settings table
try {
  db.exec(`CREATE TABLE IF NOT EXISTS setting (key TEXT PRIMARY KEY, value TEXT)`);
  db.exec(`INSERT OR IGNORE INTO setting (key, value) VALUES ('default_tsw_version', '6')`);
} catch (e) {}

// Migrate: price fetch error log
try {
  db.exec(`CREATE TABLE IF NOT EXISTS price_fetch_error (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    dlc_id INTEGER,
    dlc_name TEXT,
    steam_link TEXT,
    reason TEXT,
    fetched_at TEXT,
    FOREIGN KEY (dlc_id) REFERENCES dlc(id) ON DELETE CASCADE
  )`);
} catch (e) {}

// Migrate: steam_prices historical table
try {
  db.exec(`CREATE TABLE IF NOT EXISTS steam_price (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    dlc_id INTEGER NOT NULL,
    price TEXT,
    price_value REAL,
    price_original TEXT,
    price_original_value REAL,
    price_currency TEXT,
    price_discount TEXT,
    fetched_at TEXT NOT NULL,
    FOREIGN KEY (dlc_id) REFERENCES dlc(id) ON DELETE CASCADE
  )`);
  db.exec(`CREATE INDEX IF NOT EXISTS idx_steam_price_dlc ON steam_price(dlc_id, fetched_at)`);
} catch (e) {}

// Migrate existing price data from dlc into steam_price (one-time)
try {
  const hasHistory = dbGet(`SELECT COUNT(*) as n FROM steam_price`);
  if (hasHistory.n === 0) {
    const priceRows = dbAll(`SELECT id, price, price_value, price_original, price_original_value, price_currency, price_discount, price_updated_at FROM dlc WHERE price IS NOT NULL`);
    if (priceRows.length) {
      const ins = db.prepare(`INSERT INTO steam_price (dlc_id, price, price_value, price_original, price_original_value, price_currency, price_discount, fetched_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`);
      for (const r of priceRows) {
        ins.run(r.id, r.price, r.price_value, r.price_original, r.price_original_value, r.price_currency, r.price_discount, r.price_updated_at || new Date().toISOString());
      }
      console.log(`Migrated ${priceRows.length} existing prices into steam_price history`);
    }
  }
} catch (e) {}

app.use(express.json());
app.use(express.static(path.join(__dirname, 'public')));

// ─── DB helpers ──────────────────────────────────────────────────────────────

const dbAll = (sql, params = []) => db.prepare(sql).all(...params);
const dbGet = (sql, params = []) => db.prepare(sql).get(...params);
const dbRun = (sql, params = []) => {
  const result = db.prepare(sql).run(...params);
  return { lastID: Number(result.lastInsertRowid), changes: result.changes };
};

function wrap(fn) {
  return async (req, res) => {
    try { await fn(req, res); }
    catch (e) { console.error(e); res.status(500).json({ error: e.message }); }
  };
}

// ─── DLC list ────────────────────────────────────────────────────────────────

app.get('/api/dlc', wrap(async (req, res) => {
  const page = Math.max(1, parseInt(req.query.page) || 1);
  const limit = Math.min(200, Math.max(1, parseInt(req.query.limit) || 50));
  const offset = (page - 1) * limit;
  const { search, country_id, dlc_type_id, tsw_version, price_lo, price_hi, owned } = req.query;

  const where = ['1=1'];
  const params = [];

  if (search) {
    where.push('(d.content_name LIKE ? OR d.acronym LIKE ? OR d.developer LIKE ? OR d.short_name LIKE ?)');
    const s = `%${search}%`;
    params.push(s, s, s, s);
  }
  if (country_id) { where.push('d.country_id = ?'); params.push(country_id); }
  if (dlc_type_id) { where.push('d.dlc_type_id = ?'); params.push(dlc_type_id); }
  if (tsw_version) {
    const v = parseInt(tsw_version);
    if (v >= 1 && v <= 6) {
      where.push(`d.tsw_versions LIKE ?`);
      params.push(`%${v}%`);
    }
  }
  if (owned === 'include') { where.push('d.owned = 1'); }
  else if (owned === 'exclude') { where.push('(d.owned = 0 OR d.owned IS NULL)'); }
  const lo = parseFloat(price_lo), hi = parseFloat(price_hi);
  if (!isNaN(lo) && !isNaN(hi)) {
    where.push('d.price_value >= ? AND d.price_value <= ?');
    params.push(lo, hi);
  } else if (!isNaN(lo)) {
    where.push('d.price_value >= ?');
    params.push(lo);
  } else if (!isNaN(hi)) {
    where.push('d.price_value <= ?');
    params.push(hi);
  }

  const SORT_COLS = {
    name: 'd.content_name', type: 'dt.name', country: 'c.name',
    developer: 'd.developer', tsw_version: 'd.tsw_versions',
    price: 'd.price_value',
  };
  const sortBy = SORT_COLS[req.query.sort_by] || 'd.content_name';
  const sortDir = req.query.sort_dir === 'desc' ? 'DESC' : 'ASC';

  const whereClause = where.join(' AND ');
  const countRow = dbGet(`SELECT COUNT(*) as n FROM dlc d WHERE ${whereClause}`, params);

  const rows = dbAll(`
    SELECT d.*,
      dt.name as dlc_type_name,
      c.name as country_name,
      (SELECT sl.store_url FROM store_link sl WHERE sl.dlc_id = d.id AND sl.console_platform = 'PC' AND sl.store_url IS NOT NULL LIMIT 1) as purchase_url
    FROM dlc d
    LEFT JOIN dlc_type dt ON d.dlc_type_id = dt.id
    LEFT JOIN country c ON c.id = d.country_id
    WHERE ${whereClause}
    GROUP BY d.id
    ORDER BY ${sortBy} ${sortDir}
    LIMIT ? OFFSET ?
  `, [...params, limit, offset]);

  res.json({ total: countRow.n, page, limit, rows });
}));

// ─── Resolve needed_dlc text → steam link ────────────────────────────────────

function normStr(s) {
  return (s || '')
    .toLowerCase()
    .replace(/\u00f6/g, 'o').replace(/\u00fc/g, 'u').replace(/\u00e4/g, 'a')  // umlauts
    .replace(/\u00df/g, 'ss')  // eszett
    .replace(/[^a-z0-9 ]/g, ' ')
    .replace(/\s+/g, ' ')
    .trim();
}

function getDlcFuzzyList() {
  const all = dbAll(`SELECT id, content_name, short_name, acronym, price, price_original, price_discount FROM dlc`);
  return all.map(d => ({
    ...d,
    norm: normStr(d.content_name),
    shortNorm: normStr(d.short_name),
  }));
}

// Common abbreviations used in needed_dlc → full names in DB
const DLC_ALIAS = {
  'nec boston providence': 'northeast corridor boston providence',
  'nec new york trenton': 'northeast corridor new york trenton',
  'west coast main line preston carlisle': 'wcml preston carlisle',
  'west coast main line crewe preston': 'wcml birmingham crewe', // approximate
  'southeastern high speed extended': 'southeastern highspeed',
  'london overground suffragette line': 'london overground suffragette',
  'fife circle line levenmouth rail link': 'fife circle line',
};

function findDlcByName(name) {
  if (!name) return null;
  // Clean: strip parenthetical notes and take first line
  let clean = name.replace(/\s*\(.*?\)\s*/g, '').trim();
  if (clean.includes('\n')) clean = clean.split('\n')[0].trim();
  if (!clean) return null;

  const cache = getDlcFuzzyList();
  const needle = normStr(clean);

  // 1. Exact normalized substring match
  let match = cache.find(d => d.norm.includes(needle) || d.shortNorm === needle);
  if (match) return match;

  // 2. Check alias table
  const alias = DLC_ALIAS[needle];
  if (alias) {
    match = cache.find(d => d.norm.includes(alias));
    if (match) return match;
  }

  // 3. Try without common suffixes
  const simplified = needle.replace(/\b(extended|route add on|add on|loco add on)\b/g, '').replace(/\s+/g, ' ').trim();
  if (simplified !== needle) {
    match = cache.find(d => d.norm.includes(simplified));
    if (match) return match;
  }

  // 4. Try matching key words (at least 2 significant words must match)
  const words = needle.split(' ').filter(w => w.length > 2);
  if (words.length >= 2) {
    let bestScore = 0;
    let bestMatch = null;
    for (const d of cache) {
      const score = words.filter(w => d.norm.includes(w)).length;
      if (score > bestScore && score >= Math.min(words.length, 2)) {
        bestScore = score;
        bestMatch = d;
      }
    }
    if (bestMatch) return bestMatch;
  }

  return null;
}

function getPcStoreUrl(dlcId) {
  const sl = dbGet(`SELECT store_url FROM store_link WHERE dlc_id = ? AND console_platform = 'PC' AND store_url IS NOT NULL LIMIT 1`, [dlcId]);
  return sl?.store_url || null;
}

function resolveNeededDlcLinks(rows) {
  const needed = [...new Set(rows.map(r => r.needed_dlc).filter(Boolean))];
  if (!needed.length) return;

  const dlcLinkCache = {};
  for (const name of needed) {
    if (dlcLinkCache[name] !== undefined) continue;
    const dlcRow = findDlcByName(name);
    if (dlcRow) {
      dlcLinkCache[name] = {
        dlc_id: dlcRow.id, dlc_name: dlcRow.content_name, steam_url: getPcStoreUrl(dlcRow.id),
        price: dlcRow.price, price_original: dlcRow.price_original, price_discount: dlcRow.price_discount,
      };
    } else {
      dlcLinkCache[name] = null;
    }
  }

  for (const r of rows) {
    const link = r.needed_dlc ? dlcLinkCache[r.needed_dlc] : null;
    r.needed_dlc_steam_url = link?.steam_url || null;
    r.needed_dlc_id = link?.dlc_id || null;
    r.needed_dlc_price = link?.price || null;
    r.needed_dlc_price_original = link?.price_original || null;
    r.needed_dlc_price_discount = link?.price_discount || null;
  }
}

// ─── DLC detail ──────────────────────────────────────────────────────────────

app.get('/api/dlc/:id', wrap(async (req, res) => {
  const dlc = dbGet(`
    SELECT d.*, dt.name as dlc_type_name, c.id as country_id, c.name as country_name
    FROM dlc d
    LEFT JOIN dlc_type dt ON d.dlc_type_id = dt.id
    LEFT JOIN country c ON c.id = d.country_id
    WHERE d.id = ?`, [req.params.id]);

  if (!dlc) return res.status(404).json({ error: 'Not found' });

  dlc.country = dlc.country_id ? { id: dlc.country_id, name: dlc.country_name } : null;
  dlc.store_links = dbAll('SELECT * FROM store_link WHERE dlc_id = ?', [req.params.id]);
  dlc.documents = dbAll('SELECT * FROM document_link WHERE dlc_id = ? ORDER BY doc_type, label', [req.params.id]);
  dlc.trains_included = dbAll('SELECT t.id, t.name FROM train t JOIN dlc_train dt ON dt.train_id = t.id WHERE dt.dlc_id = ? ORDER BY t.name', [req.params.id]);

  // Layers where this DLC is the route
  dlc.layers_on_route = dbAll('SELECT * FROM layer WHERE route_dlc_id = ? ORDER BY locomotive', [req.params.id]);
  dlc.ai_layers_on_route = dbAll('SELECT * FROM ai_layer WHERE route_dlc_id = ? ORDER BY locomotive', [req.params.id]);
  dlc.substitutions_on_route = dbAll('SELECT * FROM substitution WHERE route_dlc_id = ? ORDER BY locomotive', [req.params.id]);

  // Layers where this DLC's trains appear on OTHER routes (reverse lookup)
  // Match by train name from base locos
  const trainNames = dlc.trains_included.map(t => t.name);
  if (trainNames.length) {
    const placeholders = trainNames.map(() => '?').join(',');
    dlc.layers_providing = dbAll(`
      SELECT l.*, d.content_name as route_name, d.id as route_id
      FROM layer l
      LEFT JOIN dlc d ON d.id = l.route_dlc_id
      WHERE l.locomotive IN (${placeholders}) AND l.route_dlc_id != ?
      ORDER BY d.content_name, l.locomotive`, [...trainNames, req.params.id]);
    dlc.ai_layers_providing = dbAll(`
      SELECT a.*, d.content_name as route_name, d.id as route_id
      FROM ai_layer a
      LEFT JOIN dlc d ON d.id = a.route_dlc_id
      WHERE a.locomotive IN (${placeholders}) AND a.route_dlc_id != ?
      ORDER BY d.content_name, a.locomotive`, [...trainNames, req.params.id]);
    dlc.substitutions_providing = dbAll(`
      SELECT s.*, d.content_name as route_name, d.id as route_id
      FROM substitution s
      LEFT JOIN dlc d ON d.id = s.route_dlc_id
      WHERE s.locomotive IN (${placeholders}) AND s.route_dlc_id != ?
      ORDER BY d.content_name, s.locomotive`, [...trainNames, req.params.id]);
  } else {
    dlc.layers_providing = [];
    dlc.ai_layers_providing = [];
    dlc.substitutions_providing = [];
  }

  // Resolve steam purchase links for all layer rows
  resolveNeededDlcLinks(dlc.layers_on_route);
  resolveNeededDlcLinks(dlc.ai_layers_on_route);
  resolveNeededDlcLinks(dlc.substitutions_on_route);
  resolveNeededDlcLinks(dlc.layers_providing);
  resolveNeededDlcLinks(dlc.ai_layers_providing);
  resolveNeededDlcLinks(dlc.substitutions_providing);

  res.json(dlc);
}));

// POST /api/dlc
app.post('/api/dlc', wrap(async (req, res) => {
  const b = req.body;
  if (!b.content_name) return res.status(400).json({ error: 'content_name required' });

  const r = dbRun(`
    INSERT INTO dlc (content_name, steam_name, gameplay_guide, short_name, acronym,
      developer, release_date, conductor_mode, announcements, train_faults,
      tsw_versions,
      new_lighting, new_track_shadows, route_hopping, gen8_compatible,
      requirements_raw, expansions_raw, platform_differences,
      dlc_type_id, country_id)
    VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
    [b.content_name, b.steam_name || null, b.gameplay_guide || null,
     b.short_name || null, b.acronym || null, b.developer || null, b.release_date || null,
     b.conductor_mode ? 1 : 0, b.announcements ? 1 : 0, b.train_faults ? 1 : 0,
     JSON.stringify(b.tsw_versions || []),
     b.new_lighting ? 1 : 0, b.new_track_shadows ? 1 : 0, b.route_hopping ? 1 : 0, b.gen8_compatible ? 1 : 0,
     b.requirements_raw || null, b.expansions_raw || null, b.platform_differences || null,
     b.dlc_type_id || null, b.country_id || null]);

  res.json({ id: r.lastID });
}));

// PUT /api/dlc/:id
app.put('/api/dlc/:id', wrap(async (req, res) => {
  const b = req.body;
  if (!b.content_name) return res.status(400).json({ error: 'content_name required' });

  dbRun(`
    UPDATE dlc SET content_name=?, steam_name=?, gameplay_guide=?,
      short_name=?, acronym=?, developer=?, release_date=?,
      conductor_mode=?, announcements=?, train_faults=?,
      tsw_versions=?,
      new_lighting=?, new_track_shadows=?, route_hopping=?, gen8_compatible=?,
      requirements_raw=?, expansions_raw=?, platform_differences=?,
      dlc_type_id=?, country_id=? WHERE id=?`,
    [b.content_name, b.steam_name || null, b.gameplay_guide || null,
     b.short_name || null, b.acronym || null, b.developer || null, b.release_date || null,
     b.conductor_mode ? 1 : 0, b.announcements ? 1 : 0, b.train_faults ? 1 : 0,
     JSON.stringify(b.tsw_versions || []),
     b.new_lighting ? 1 : 0, b.new_track_shadows ? 1 : 0, b.route_hopping ? 1 : 0, b.gen8_compatible ? 1 : 0,
     b.requirements_raw || null, b.expansions_raw || null, b.platform_differences || null,
     b.dlc_type_id || null, b.country_id || null, req.params.id]);

  res.json({ ok: true });
}));

// DELETE /api/dlc/:id
app.delete('/api/dlc/:id', wrap(async (req, res) => {
  dbRun('DELETE FROM dlc WHERE id = ?', [req.params.id]);
  res.json({ ok: true });
}));

// GET /api/dlc/:id/price-history
app.get('/api/dlc/:id/price-history', wrap(async (req, res) => {
  const rows = dbAll(`SELECT price, price_value, price_original, price_original_value, price_discount, price_currency, fetched_at FROM steam_price WHERE dlc_id = ? ORDER BY fetched_at ASC`, [req.params.id]);
  res.json(rows);
}));

// PUT /api/dlc/:id/trains
app.put('/api/dlc/:id/trains', wrap(async (req, res) => {
  dbRun('DELETE FROM dlc_train WHERE dlc_id = ?', [req.params.id]);
  for (const tid of (req.body.train_ids || [])) {
    dbRun('INSERT OR IGNORE INTO dlc_train (dlc_id, train_id) VALUES (?, ?)', [req.params.id, tid]);
  }
  res.json({ ok: true });
}));

// ─── Documents ───────────────────────────────────────────────────────────────

app.post('/api/dlc/:id/documents', wrap(async (req, res) => {
  const { doc_type, label, url } = req.body;
  const r = dbRun('INSERT INTO document_link (dlc_id, doc_type, label, url) VALUES (?, ?, ?, ?)',
    [req.params.id, doc_type, label || null, url || null]);
  res.json({ id: r.lastID });
}));

app.put('/api/documents/:id', wrap(async (req, res) => {
  const { doc_type, label, url } = req.body;
  dbRun('UPDATE document_link SET doc_type=?, label=?, url=? WHERE id=?',
    [doc_type, label || null, url || null, req.params.id]);
  res.json({ ok: true });
}));

app.delete('/api/documents/:id', wrap(async (req, res) => {
  dbRun('DELETE FROM document_link WHERE id = ?', [req.params.id]);
  res.json({ ok: true });
}));

// ─── Store links ─────────────────────────────────────────────────────────────

app.post('/api/dlc/:id/store-links', wrap(async (req, res) => {
  const { console_platform, size_gb, store_url } = req.body;
  const r = dbRun('INSERT INTO store_link (dlc_id, console_platform, size_gb, store_url) VALUES (?, ?, ?, ?)',
    [req.params.id, console_platform, size_gb || null, store_url || null]);
  res.json({ id: r.lastID });
}));

app.put('/api/store-links/:id', wrap(async (req, res) => {
  const { console_platform, size_gb, store_url } = req.body;
  dbRun('UPDATE store_link SET console_platform=?, size_gb=?, store_url=? WHERE id=?',
    [console_platform, size_gb || null, store_url || null, req.params.id]);
  res.json({ ok: true });
}));

app.delete('/api/store-links/:id', wrap(async (req, res) => {
  dbRun('DELETE FROM store_link WHERE id = ?', [req.params.id]);
  res.json({ ok: true });
}));

// ─── Layers CRUD ─────────────────────────────────────────────────────────────

app.post('/api/dlc/:id/layers', wrap(async (req, res) => {
  const { locomotive, needed_dlc, service_type, num_services } = req.body;
  const r = dbRun('INSERT INTO layer (route_dlc_id, locomotive, needed_dlc, service_type, num_services) VALUES (?, ?, ?, ?, ?)',
    [req.params.id, locomotive, needed_dlc || null, service_type || null, num_services || null]);
  res.json({ id: r.lastID });
}));

app.delete('/api/layers/:id', wrap(async (req, res) => {
  dbRun('DELETE FROM layer WHERE id = ?', [req.params.id]);
  res.json({ ok: true });
}));

app.post('/api/dlc/:id/ai-layers', wrap(async (req, res) => {
  const { locomotive, needed_dlc, service_type } = req.body;
  const r = dbRun('INSERT INTO ai_layer (route_dlc_id, locomotive, needed_dlc, service_type) VALUES (?, ?, ?, ?)',
    [req.params.id, locomotive, needed_dlc || null, service_type || null]);
  res.json({ id: r.lastID });
}));

app.delete('/api/ai-layers/:id', wrap(async (req, res) => {
  dbRun('DELETE FROM ai_layer WHERE id = ?', [req.params.id]);
  res.json({ ok: true });
}));

app.post('/api/dlc/:id/substitutions', wrap(async (req, res) => {
  const { locomotive, needed_dlc, replaced_locomotive } = req.body;
  const r = dbRun('INSERT INTO substitution (route_dlc_id, locomotive, needed_dlc, replaced_locomotive) VALUES (?, ?, ?, ?)',
    [req.params.id, locomotive, needed_dlc || null, replaced_locomotive || null]);
  res.json({ id: r.lastID });
}));

app.delete('/api/substitutions/:id', wrap(async (req, res) => {
  dbRun('DELETE FROM substitution WHERE id = ?', [req.params.id]);
  res.json({ ok: true });
}));

// ─── Lookup tables ───────────────────────────────────────────────────────────

app.get('/api/countries', wrap(async (req, res) => {
  res.json(dbAll(`
    SELECT c.*, COUNT(d.id) as count FROM country c
    LEFT JOIN dlc d ON d.country_id = c.id
    GROUP BY c.id ORDER BY c.name`));
}));
app.post('/api/countries', wrap(async (req, res) => {
  if (!req.body.name) return res.status(400).json({ error: 'name required' });
  const r = dbRun('INSERT INTO country (name) VALUES (?)', [req.body.name]);
  res.json({ id: r.lastID });
}));
app.put('/api/countries/:id', wrap(async (req, res) => {
  dbRun('UPDATE country SET name=? WHERE id=?', [req.body.name, req.params.id]);
  res.json({ ok: true });
}));
app.delete('/api/countries/:id', wrap(async (req, res) => {
  dbRun('DELETE FROM country WHERE id=?', [req.params.id]);
  res.json({ ok: true });
}));

app.get('/api/tsw-versions', wrap(async (req, res) => {
  const defaultVer = dbGet(`SELECT value FROM setting WHERE key = 'default_tsw_version'`);
  const defaultId = defaultVer ? parseInt(defaultVer.value) : 6;
  const versions = [];
  for (let i = 1; i <= 6; i++) {
    const row = dbGet(`SELECT COUNT(*) as count FROM dlc WHERE tsw_versions LIKE ?`, [`%${i}%`]);
    versions.push({ id: i, name: `TSW ${i}`, count: row.count, is_default: i === defaultId });
  }
  res.json(versions);
}));

app.put('/api/settings/default-tsw', wrap(async (req, res) => {
  const v = parseInt(req.body.version);
  if (v < 1 || v > 6) return res.status(400).json({ error: 'Invalid version' });
  dbRun(`INSERT OR REPLACE INTO setting (key, value) VALUES ('default_tsw_version', ?)`, [String(v)]);
  res.json({ ok: true });
}));

app.get('/api/dlc-types', wrap(async (req, res) => {
  res.json(dbAll('SELECT * FROM dlc_type ORDER BY name'));
}));
app.post('/api/dlc-types', wrap(async (req, res) => {
  if (!req.body.name) return res.status(400).json({ error: 'name required' });
  const r = dbRun('INSERT INTO dlc_type (name) VALUES (?)', [req.body.name]);
  res.json({ id: r.lastID });
}));
app.put('/api/dlc-types/:id', wrap(async (req, res) => {
  dbRun('UPDATE dlc_type SET name=? WHERE id=?', [req.body.name, req.params.id]);
  res.json({ ok: true });
}));
app.delete('/api/dlc-types/:id', wrap(async (req, res) => {
  dbRun('DELETE FROM dlc_type WHERE id=?', [req.params.id]);
  res.json({ ok: true });
}));

app.get('/api/trains', wrap(async (req, res) => {
  res.json(dbAll('SELECT * FROM train ORDER BY name'));
}));

app.get('/api/trains/:id', wrap(async (req, res) => {
  const train = dbGet('SELECT * FROM train WHERE id = ?', [req.params.id]);
  if (!train) return res.status(404).json({ error: 'Not found' });

  const purchaseDlcs = dbAll(`
    SELECT d.id, d.content_name, d.acronym, dt.name as dlc_type_name,
      d.price, d.price_original, d.price_discount
    FROM dlc_train dtr
    JOIN dlc d ON d.id = dtr.dlc_id
    LEFT JOIN dlc_type dt ON dt.id = d.dlc_type_id
    WHERE dtr.train_id = ?
    ORDER BY d.content_name`, [req.params.id]);

  // Routes where this train appears as a layer
  const layerRoutes = dbAll(`
    SELECT l.*, d.content_name as route_name, d.id as route_id, dt.name as dlc_type_name
    FROM layer l
    JOIN dlc d ON d.id = l.route_dlc_id
    LEFT JOIN dlc_type dt ON dt.id = d.dlc_type_id
    WHERE l.locomotive = ?
    ORDER BY d.content_name`, [train.name]);

  const aiLayerRoutes = dbAll(`
    SELECT a.*, d.content_name as route_name, d.id as route_id, dt.name as dlc_type_name
    FROM ai_layer a
    JOIN dlc d ON d.id = a.route_dlc_id
    LEFT JOIN dlc_type dt ON dt.id = d.dlc_type_id
    WHERE a.locomotive = ?
    ORDER BY d.content_name`, [train.name]);

  const subRoutes = dbAll(`
    SELECT s.*, d.content_name as route_name, d.id as route_id, dt.name as dlc_type_name
    FROM substitution s
    JOIN dlc d ON d.id = s.route_dlc_id
    LEFT JOIN dlc_type dt ON dt.id = d.dlc_type_id
    WHERE s.locomotive = ?
    ORDER BY d.content_name`, [train.name]);

  // Fetch store links for all referenced DLCs
  const allIds = [...new Set([
    ...purchaseDlcs.map(d => d.id),
    ...layerRoutes.map(r => r.route_id),
    ...aiLayerRoutes.map(r => r.route_id),
    ...subRoutes.map(r => r.route_id),
  ])];
  const storeLinks = {};
  if (allIds.length) {
    const links = dbAll(
      `SELECT dlc_id, console_platform, store_url FROM store_link
       WHERE dlc_id IN (${allIds.map(() => '?').join(',')})`, allIds);
    links.forEach(l => {
      if (!l.store_url) return;
      if (!storeLinks[l.dlc_id]) storeLinks[l.dlc_id] = {};
      storeLinks[l.dlc_id][l.console_platform] = l.store_url;
    });
  }
  purchaseDlcs.forEach(d => { d.store_links = storeLinks[d.id] || {}; });

  // Build prices lookup for all referenced DLCs
  const prices = {};
  if (allIds.length) {
    const priceRows = dbAll(
      `SELECT id, price, price_original, price_discount FROM dlc
       WHERE id IN (${allIds.map(() => '?').join(',')})`, allIds);
    priceRows.forEach(p => {
      prices[p.id] = { price: p.price, price_original: p.price_original, price_discount: p.price_discount };
    });
  }

  // Resolve needed_dlc links for layer routes
  resolveNeededDlcLinks(layerRoutes);
  resolveNeededDlcLinks(aiLayerRoutes);
  resolveNeededDlcLinks(subRoutes);

  res.json({ train, purchase_dlcs: purchaseDlcs, layer_routes: layerRoutes, ai_layer_routes: aiLayerRoutes, sub_routes: subRoutes, store_links: storeLinks, prices });
}));

app.post('/api/trains', wrap(async (req, res) => {
  if (!req.body.name) return res.status(400).json({ error: 'name required' });
  const r = dbRun('INSERT INTO train (name) VALUES (?)', [req.body.name]);
  if (req.body.dlc_id) {
    dbRun('INSERT OR IGNORE INTO dlc_train (dlc_id, train_id) VALUES (?, ?)', [req.body.dlc_id, r.lastID]);
  }
  res.json({ id: r.lastID });
}));
app.put('/api/trains/:id', wrap(async (req, res) => {
  dbRun('UPDATE train SET name=? WHERE id=?', [req.body.name, req.params.id]);
  if (req.body.dlc_ids !== undefined) {
    dbRun('DELETE FROM dlc_train WHERE train_id = ?', [req.params.id]);
    for (const dlcId of req.body.dlc_ids) {
      dbRun('INSERT OR IGNORE INTO dlc_train (dlc_id, train_id) VALUES (?, ?)', [dlcId, req.params.id]);
    }
  }
  res.json({ ok: true });
}));
app.delete('/api/trains/:id', wrap(async (req, res) => {
  dbRun('DELETE FROM train WHERE id=?', [req.params.id]);
  res.json({ ok: true });
}));

// ─── Stats ───────────────────────────────────────────────────────────────────

app.get('/api/stats', wrap(async (req, res) => {
  res.json({
    dlc: dbGet('SELECT COUNT(*) as n FROM dlc').n,
    trains: dbGet('SELECT COUNT(*) as n FROM train').n,
    layers: dbGet('SELECT COUNT(*) as n FROM layer').n,
    ai_layers: dbGet('SELECT COUNT(*) as n FROM ai_layer').n,
    substitutions: dbGet('SELECT COUNT(*) as n FROM substitution').n,
    by_type: dbAll('SELECT dt.name, COUNT(*) as count FROM dlc d JOIN dlc_type dt ON dt.id = d.dlc_type_id GROUP BY dt.id'),
    by_country: dbAll('SELECT c.name, COUNT(d.id) as count FROM country c LEFT JOIN dlc d ON d.country_id = c.id GROUP BY c.id ORDER BY count DESC'),
  });
}));

// ─── Related DLCs (lightweight) ──────────────────────────────────────────────

app.get('/api/dlc/:id/related', wrap(async (req, res) => {
  const dlcId = req.params.id;
  const dlc = dbGet('SELECT id, content_name, dlc_type_id FROM dlc WHERE id = ?', [dlcId]);
  if (!dlc) return res.status(404).json({ error: 'Not found' });

  // Collect all needed_dlc strings from layers/ai/subs on this route
  const neededStrings = new Set();
  const onRoute = [
    ...dbAll('SELECT needed_dlc FROM layer WHERE route_dlc_id = ? AND needed_dlc IS NOT NULL', [dlcId]),
    ...dbAll('SELECT needed_dlc FROM ai_layer WHERE route_dlc_id = ? AND needed_dlc IS NOT NULL', [dlcId]),
    ...dbAll('SELECT needed_dlc FROM substitution WHERE route_dlc_id = ? AND needed_dlc IS NOT NULL', [dlcId]),
  ];
  onRoute.forEach(r => neededStrings.add(r.needed_dlc));

  // Also check reverse: where this DLC's trains provide layers to other routes
  const trains = dbAll('SELECT t.name FROM train t JOIN dlc_train dt ON dt.train_id = t.id WHERE dt.dlc_id = ?', [dlcId]);
  const trainNames = trains.map(t => t.name);
  const routeIds = new Set();
  if (trainNames.length) {
    const ph = trainNames.map(() => '?').join(',');
    const providing = [
      ...dbAll(`SELECT route_dlc_id FROM layer WHERE locomotive IN (${ph}) AND route_dlc_id != ?`, [...trainNames, dlcId]),
      ...dbAll(`SELECT route_dlc_id FROM ai_layer WHERE locomotive IN (${ph}) AND route_dlc_id != ?`, [...trainNames, dlcId]),
      ...dbAll(`SELECT route_dlc_id FROM substitution WHERE locomotive IN (${ph}) AND route_dlc_id != ?`, [...trainNames, dlcId]),
    ];
    providing.forEach(r => { if (r.route_dlc_id) routeIds.add(r.route_dlc_id); });
  }

  // Resolve needed_dlc strings to DLC records
  const relatedIds = new Set();
  for (const str of neededStrings) {
    const found = findDlcByName(str);
    if (found && found.id !== parseInt(dlcId)) relatedIds.add(found.id);
  }
  // Add route DLCs from reverse lookups
  routeIds.forEach(id => relatedIds.add(id));

  if (!relatedIds.size) return res.json([]);

  const ids = [...relatedIds];
  const related = dbAll(`
    SELECT d.id, d.content_name, d.price, d.price_value, d.price_original, d.price_discount,
      dt.name as dlc_type_name,
      (SELECT sl.store_url FROM store_link sl WHERE sl.dlc_id = d.id AND sl.console_platform = 'PC' AND sl.store_url IS NOT NULL LIMIT 1) as purchase_url
    FROM dlc d
    LEFT JOIN dlc_type dt ON dt.id = d.dlc_type_id
    WHERE d.id IN (${ids.map(() => '?').join(',')})
    ORDER BY d.content_name`, ids);

  // Tag each as 'layer_on' (provides layers to this route) or 'provides_to' (route where your trains go)
  related.forEach(r => {
    r.relationship = routeIds.has(r.id) && !relatedIds.has(r.id) ? 'your_trains_on' : 'adds_to_route';
    if (routeIds.has(r.id) && neededStrings.size === 0) r.relationship = 'your_trains_on';
  });

  res.json(related);
}));

// ─── Owned & Cart ────────────────────────────────────────────────────────────

app.put('/api/dlc/:id/owned', wrap(async (req, res) => {
  const owned = req.body.owned ? 1 : 0;
  dbRun('UPDATE dlc SET owned = ? WHERE id = ?', [owned, req.params.id]);
  // If marking as owned, remove from cart
  if (owned) dbRun('UPDATE dlc SET in_cart = 0 WHERE id = ?', [req.params.id]);
  res.json({ ok: true });
}));

app.put('/api/dlc/:id/cart', wrap(async (req, res) => {
  const inCart = req.body.in_cart ? 1 : 0;
  // Can't add owned DLC to cart
  if (inCart) {
    const dlc = dbGet('SELECT owned FROM dlc WHERE id = ?', [req.params.id]);
    if (dlc?.owned) return res.status(400).json({ error: 'Cannot add owned DLC to cart' });
  }
  dbRun('UPDATE dlc SET in_cart = ? WHERE id = ?', [inCart, req.params.id]);
  res.json({ ok: true });
}));

app.get('/api/cart', wrap(async (req, res) => {
  const items = dbAll(`
    SELECT d.id, d.content_name, d.price, d.price_value, d.price_original,
      d.price_discount, dt.name as dlc_type_name,
      (SELECT sl.store_url FROM store_link sl WHERE sl.dlc_id = d.id AND sl.console_platform = 'PC' AND sl.store_url IS NOT NULL LIMIT 1) as purchase_url
    FROM dlc d LEFT JOIN dlc_type dt ON dt.id = d.dlc_type_id
    WHERE d.in_cart = 1 ORDER BY d.content_name`);
  res.json(items);
}));

app.delete('/api/cart', wrap(async (req, res) => {
  dbRun('UPDATE dlc SET in_cart = 0');
  res.json({ ok: true });
}));

app.get('/api/owned', wrap(async (req, res) => {
  const items = dbAll(`
    SELECT d.id, d.content_name, dt.name as dlc_type_name, c.name as country_name
    FROM dlc d LEFT JOIN dlc_type dt ON dt.id = d.dlc_type_id LEFT JOIN country c ON c.id = d.country_id
    WHERE d.owned = 1 ORDER BY d.content_name`);
  res.json(items);
}));

// ─── Price fetching ──────────────────────────────────────────────────────────

let priceFetchRunning = false;
let priceFetchProgress = { running: false, current: 0, total: 0, updated: 0, errors: 0, errorDetails: [] };

app.get('/api/prices/status', wrap(async (req, res) => {
  const lastFetch = dbGet(`SELECT value FROM setting WHERE key = 'last_price_fetch'`);
  const withPrice = dbGet(`SELECT COUNT(*) as n FROM dlc WHERE price IS NOT NULL`);
  const total = dbGet(`SELECT COUNT(DISTINCT dlc_id) as n FROM store_link WHERE console_platform = 'PC' AND store_url IS NOT NULL`);
  const currencySetting = dbGet(`SELECT value FROM setting WHERE key = 'steam_currency'`);
  res.json({
    ...priceFetchProgress,
    last_updated: lastFetch?.value || null,
    dlcs_with_price: withPrice.n,
    dlcs_with_steam: total.n,
    steam_currency: currencySetting?.value || 'us',
  });
}));

app.put('/api/settings/steam-currency', wrap(async (req, res) => {
  const cc = (req.body.cc || 'us').toLowerCase().trim();
  dbRun(`INSERT OR REPLACE INTO setting (key, value) VALUES ('steam_currency', ?)`, [cc]);
  res.json({ ok: true });
}));

app.get('/api/prices/errors', wrap(async (req, res) => {
  const rows = dbAll(`SELECT * FROM price_fetch_error ORDER BY dlc_name`);
  const lastFetch = dbGet(`SELECT value FROM setting WHERE key = 'last_price_fetch'`);
  res.json({ errors: rows, last_fetched: lastFetch?.value || null });
}));

app.delete('/api/prices/errors', wrap(async (req, res) => {
  dbRun(`DELETE FROM price_fetch_error`);
  res.json({ ok: true });
}));

app.post('/api/prices/fetch', wrap(async (req, res) => {
  if (priceFetchRunning) {
    return res.status(409).json({ error: 'Price fetch already running', progress: priceFetchProgress });
  }

  priceFetchRunning = true;
  priceFetchProgress = { running: true, current: 0, total: 0, updated: 0, errors: 0, errorDetails: [] };
  res.json({ ok: true, message: 'Price fetch started' });

  // Run in background
  const { fetchPrice } = require('./fetch_prices.js');
  const currencySetting = dbGet(`SELECT value FROM setting WHERE key = 'steam_currency'`);
  const cc = currencySetting?.value || 'us';
  const dlcs = dbAll(`
    SELECT d.id, d.content_name, sl.store_url as steam_link
    FROM dlc d
    JOIN store_link sl ON sl.dlc_id = d.id AND sl.console_platform = 'PC' AND sl.store_url IS NOT NULL
    ORDER BY d.content_name`);
  priceFetchProgress.total = dlcs.length;

  const updateStmt = db.prepare(`UPDATE dlc SET price=?, price_value=?, price_original=?, price_original_value=?, price_currency=?, price_discount=?, price_updated_at=? WHERE id=?`);
  const historyStmt = db.prepare(`INSERT INTO steam_price (dlc_id, price, price_value, price_original, price_original_value, price_currency, price_discount, fetched_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`);
  const errorStmt = db.prepare(`INSERT INTO price_fetch_error (dlc_id, dlc_name, steam_link, reason, fetched_at) VALUES (?, ?, ?, ?, ?)`);
  const now = new Date().toISOString();

  // Clear previous errors for this fetch run
  dbRun(`DELETE FROM price_fetch_error`);

  for (let i = 0; i < dlcs.length; i++) {
    priceFetchProgress.current = i + 1;
    const dlc = dlcs[i];

    try {
      const result = await fetchPrice(dlc.steam_link, cc);
      if (result.error) {
        priceFetchProgress.errors++;
        priceFetchProgress.errorDetails.push({ id: dlc.id, name: dlc.content_name, steam_link: dlc.steam_link, reason: result.error });
        errorStmt.run(dlc.id, dlc.content_name, dlc.steam_link, result.error, now);
      } else if (result.price) {
        updateStmt.run(result.price, result.priceValue || null, result.priceOriginal || null, result.priceOriginalValue || null, result.currency || null, result.discount || null, now, dlc.id);
        historyStmt.run(dlc.id, result.price, result.priceValue || null, result.priceOriginal || null, result.priceOriginalValue || null, result.currency || null, result.discount || null, now);
        priceFetchProgress.updated++;
      } else {
        const reason = 'No price found on page';
        priceFetchProgress.errors++;
        priceFetchProgress.errorDetails.push({ id: dlc.id, name: dlc.content_name, steam_link: dlc.steam_link, reason });
        errorStmt.run(dlc.id, dlc.content_name, dlc.steam_link, reason, now);
      }
    } catch (e) {
      priceFetchProgress.errors++;
      priceFetchProgress.errorDetails.push({ id: dlc.id, name: dlc.content_name, steam_link: dlc.steam_link, reason: e.message });
      errorStmt.run(dlc.id, dlc.content_name, dlc.steam_link, e.message, now);
    }

    // Delay between requests
    if (i < dlcs.length - 1) {
      await new Promise(r => setTimeout(r, 1500));
    }
  }

  // Save last fetch timestamp to settings
  dbRun(`INSERT OR REPLACE INTO setting (key, value) VALUES ('last_price_fetch', ?)`, [now]);

  priceFetchProgress.running = false;
  priceFetchRunning = false;
  console.log(`Price fetch done: ${priceFetchProgress.updated} updated, ${priceFetchProgress.errors} errors`);
}));

// ─── Catch-all for SPA ───────────────────────────────────────────────────────

app.get('*', (req, res) => {
  res.sendFile(path.join(__dirname, 'public', 'index.html'));
});

app.listen(PORT, () => {
  console.log(`DLC Guide running at http://localhost:${PORT}`);
});
