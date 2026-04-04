const XLSX = require('xlsx');
const { DatabaseSync } = require('node:sqlite');
const fs = require('fs');
const path = require('path');

const DB_PATH = path.join(__dirname, 'database.db');
const ODS_PATH = path.join(__dirname, 'spreadsheets', 'merged_data.ods');

// Preserve prices and user state from existing database before dropping
let savedPrices = {};
let savedUserState = {};
if (fs.existsSync(DB_PATH)) {
  try {
    const oldDb = new DatabaseSync(DB_PATH);
    const rows = oldDb.prepare(`
      SELECT content_name, price, price_value, price_original, price_original_value,
        price_currency, price_discount, price_updated_at
      FROM dlc WHERE price IS NOT NULL
    `).all();
    rows.forEach(r => { savedPrices[r.content_name] = r; });

    // Preserve owned/cart status
    try {
      const stateRows = oldDb.prepare(`SELECT content_name, owned, in_cart FROM dlc WHERE owned = 1 OR in_cart = 1`).all();
      stateRows.forEach(r => { savedUserState[r.content_name] = { owned: r.owned, in_cart: r.in_cart }; });
    } catch (e) {}

    // Preserve settings
    try {
      const settingsRows = oldDb.prepare(`SELECT key, value FROM setting`).all();
      savedUserState._settings = settingsRows;
    } catch (e) {}

    oldDb.close();
    if (Object.keys(savedPrices).length) {
      console.log(`Backed up ${Object.keys(savedPrices).length} prices from existing database`);
    }
    if (Object.keys(savedUserState).length) {
      console.log(`Backed up ${Object.keys(savedUserState).length} owned/cart states`);
    }
  } catch (e) { /* old DB may not have these columns */ }
  fs.unlinkSync(DB_PATH);
  console.log('Removed existing database');
}

const db = new DatabaseSync(DB_PATH);
db.exec('PRAGMA foreign_keys = ON');
db.exec('PRAGMA journal_mode = WAL');

// Apply schema
const schema = fs.readFileSync(path.join(__dirname, 'schema.sql'), 'utf8');
schema.split(';').map(s => s.trim()).filter(Boolean).forEach(s => db.exec(s));
console.log('Schema applied');

// Load workbook
const workbook = XLSX.readFile(ODS_PATH);
console.log('Sheets:', workbook.SheetNames);

// ── Helpers ──────────────────────────────────────────────────────────────────

const regionMap = {
  'UK': 'United Kingdom', 'US': 'United States', 'DE': 'Germany',
  'AT': 'Austria', 'AUS': 'Austria', 'SUI': 'Switzerland', 'CH': 'Switzerland',
  'NL': 'Netherlands', 'FR': 'France', 'CA': 'Canada',
  'CZ': 'Czech Republic', 'CZR': 'Czech Republic',
};

function cleanRegion(val) {
  if (!val) return null;
  // Strip emoji flags (Unicode regional indicators etc.)
  const clean = String(val).replace(/[\u{1F1E0}-\u{1F1FF}\u{1F3F4}\u{E0061}-\u{E007A}\u{E007F}]/gu, '').trim();
  return regionMap[clean] || clean || null;
}

function parseBool(val) {
  if (!val) return 0;
  const s = String(val).trim().toUpperCase();
  return (s === 'YES' || s === 'Y' || s === '1' || s === 'TRUE') ? 1 : 0;
}

function cleanVal(v) {
  if (v === null || v === undefined) return null;
  const s = String(v).trim();
  return (s === '' || s === '-' || s === 'N/A') ? null : s;
}

function cleanType(val) {
  if (!val) return null;
  // Strip emoji from type names like "Loco 🚆" → "Loco"
  return stripEmoji(val);
}

function stripEmoji(val) {
  if (!val) return null;
  return String(val)
    .replace(/[\u{1F000}-\u{1FFFF}\u{2600}-\u{27BF}\u{FE00}-\u{FE0F}\u{200D}\u{200B}\u{20E3}]/gu, '')
    .replace(/\s+/g, ' ')
    .trim();
}

function parseList(val) {
  if (!val || String(val).trim() === '-' || String(val).trim() === 'N/A') return [];
  return String(val).split(/\n|,/).map(s => s.trim()).filter(Boolean);
}

function parseDate(val) {
  if (!val) return null;
  const s = String(val).trim();
  // DD/MM/YY format
  const m = s.match(/^(\d{1,2})\/(\d{1,2})\/(\d{2,4})$/);
  if (m) {
    const yr = m[3].length === 2 ? '20' + m[3] : m[3];
    return `${yr}-${m[2].padStart(2, '0')}-${m[1].padStart(2, '0')}`;
  }
  return s;
}

// ── Locomotive name corrections ──────────────────────────────────────────────
// Maps wiki/layer sheet loco names → canonical Base Loco names from company_based
// Sources: tswtracker.com, company_based Base Locos, manual verification

const LOCO_NAME_MAP = {
  // UK - Operator-branded → canonical
  'Avanti West Coast Class 390 Pendolino': 'BR Class 390 Pendolino',
  'CrossCountry Class 220 Voyager': 'XC Class 220',
  'ScotRail Class 158': 'BR Class 158 Sprinter',
  'Thameslink BR Class 700/0': 'BR Class 700/0',

  // UK - BR prefix / suffix variations
  'BR Class 170': 'Class 170',
  'BR Class 170 WMT': 'Class 170',
  'BR Class 170 XC': 'Class 170',
  'BR Class 313': 'BR Class 313/2',
  'BR Class 314': 'Class 314 ScR',
  'BR Class 323 NTR': 'Class 323 NT',
  'BR Class 323 WMT': 'Class 323 WMR',
  'BR Class 350': 'Class 350',
  'BR Class 378': 'Class 378 TFL',
  'BR Class 380': 'BR Class 380 ScR',
  'BR Class 385': 'Class 385 ScR',
  'BR Class 395': 'Class 395',
  'BR Class 465': 'Class 465/9',
  'BR Class 710': 'Class 710 TFL',
  'BR Class 801': 'Class 801 LNER',
  'BR Class 142': 'Class 142',
  'BR Class 150': 'BR Class 150/2',
  'BR Class 153': 'Class 153',
  'BR Class 166': 'Class 166 GWG',
  'BR Class 09': 'Class 09',
  'BR Class 87': 'Class 87',

  // UK - Heritage / steam
  'Flying Scotsman': 'LNER Flying Scotsman',
  'Fowler 4F': 'LMS Fowler 4F',
  'Stanier Class 8F': 'LMS Stainer 8F',
  'Jubilee Class': 'LMS Jubilee',
  'LMS Jubilee Black': 'LMS Jubilee',
  'LMS Jubilee Black Fesitve': 'LMS Jubilee',

  // UK - Class 43 HST variants
  'BR Class 43 HST': 'High Speed Train GWG',
  'BR Class 43 HST GWR': 'High Speed Train GWG',
  'BR Class 43 HST EMT': 'High Speed Train EMT (2 Liveries)',

  // UK - Class 45/47 variants (from Northern Trans-Pennine)
  'BR Class 45': 'Class 45/1',
  'BR Class 47': 'Class 47',
  'BR Class 47 Blue': 'Class 47',
  'BR Class 47 Green': 'Class 47',
  'BR Class 47 InterCity': 'Class 47/4',
  'BR Class 47 Large Logo': 'Class 47/4',
  'BR Class 52 Blue': 'BR Class 52',

  // UK - Class 37 variants
  'BR Class 37': 'BR Class 37 EPX',
  'BR Class 37 RF': 'BR Class 37 EPX',
  'BR Class 37 RSR': 'Class 37/5 RSR',
  'Rail Operation Group BR Class 37/7': 'BR Class 37 EPX',
  'Rail Operation Group BR Class 37/7 (+Spirit of Steam)': 'BR Class 37 EPX',

  // UK - Class 66 variants (from various cargo / route DLCs)
  'BR Class 66': 'Class 66 EWS',
  'BR Class 66 EWS': 'Class 66 EWS',
  'BR Class 66 DB': 'Class 66 EWS',
  'BR Class 66 DBC': 'Class 66 EWS',
  'BR Class 66 DRS': 'BR Class 66 (2 Liveries)',
  'BR Class 66 ONE': 'Class 66 EWS',

  // UK - Class 375/377
  'BR Class 375': 'Class 375/9',
  'BR Class 377': 'Class 377/4 SN',

  // UK - Class 158 variants
  'BR Class 158': 'BR Class 158 Sprinter',
  'BR Class 158 EMT': 'Class 158/0 EMT',

  // UK - Other
  'BR Class 08 (+BR Heavy Freight Pack)': 'BR Class 08',
  'BR Class 08 (+BR Heavy Freight Pack': 'BR Class 08',
  'BR Class 40 (+BR Heavy Freight Pack)': 'BR Class 40',
  'BR Class 40 (+BR Heavy Freight Pack': 'BR Class 40',
  'BR Class 86/2 & Mk2F Coaches': 'BR Class 86',
  'London Underground 1938 Tube Stock': '1938 Tube Stock LT',
  '1972 Mark 2 Stock': '1972 MkII Tube Stock',
  'Rail Head Treatment Train': 'Class 66 EWS (RHTT)',

  // German - DB BR prefix normalization
  'DB BR 103': 'BR 103',
  'DB BR 110.3': 'BR 110',
  'DB BR 112': 'BR 112.1',
  'DB BR 1442': 'BR 1442 Talent 2',
  'DB BR 146': 'DB BR 146.2',
  'DB BR 193': 'DB BR 193 Vectron',
  'DB BR 218 (DB BR 218 Timetable Only)': 'DB BR 218',
  'Rail Operation Group BR Class 37/7 (+Spirit of Steam)': 'BR Class 37 EPX',
  'DB BR 363 DBB': 'DB BR 363',
  'BR 363 DBB': 'BR 363',
  'DB BR 365': 'BR 365',
  'DB BR 403 ICE 3': 'BR 403 ICE 3',
  'DB BR 406 ICE 3M': 'BR 406 ICE 3M',
  'DB BR 411 ICE-T': 'DB BR 411',
  'DB BR 422': 'BR 422',
  'DB BR 612': 'BR 612',
  'DB BR 628': 'BR 628.2',
  'DB BR 642': 'BR642',
  'DB BR 442': 'DB BR 442',
  'DB BR 114': 'DB BR 114',

  // German - Operator variants
  'Dispolok BR 182': 'DB BR 182 Dispolok',
  'FlixTrain BR 193 Vectron': 'BR 193 Vectron FT',
  'Railpool BR 193 Vectron': 'BR 193 Vectron RP',
  'MRCE BR 185.5': 'BR 185.5',
  'Railion DB BR 185.2': 'DB BR 185.2',
  'PRESS BR 155': 'BR 155 PRESS',
  'G6': 'DB G6 Shunter',
  'ES 64 U2': 'BR 182 MRCE',
  'MRCE ES 64 U2': 'BR 182 MRCE',
  'RP BR 185.6': 'BR 185.5',

  // Austrian / Swiss
  '\u00D6BB 1116': 'OBB 1116',
  'SBB RABe 523': 'RABe 523',
  'TGV Duplex': 'TGV Duplex 200 CM',

  // US
  'Amtrak Acela': 'Acela Express Amtrak',
  'ES44C4': 'BNSF ES44C4',
  'UP AC4400CW': 'AC4400CW UP',
  'UP SD70ACe': 'SD70ACe UP',
  'M3': 'M3 LIRR',
  'M7': 'M7 LIRR',
  'Multi-Level Commuter Cab Car': 'Multilevel Cab Car',
  'Union Pacific Heritage Collection': 'UP SD30ACe (6 Liveries)',
  'ALP-46': 'NJ ALP-45DP',
};

// ── DLC reference corrections ────────────────────────────────────────────────
// Maps wrong DLC(s) needed references to the correct DLC name
// Key: the incorrect DLC reference → Value: the correct loco DLC name
// Based on tswtracker.com verified data

const DLC_NEEDED_FIXES = {
  // These locos have dedicated Loco DLC add-ons, but the layer sheets
  // reference the route DLC (which is the REQUIREMENT of the loco DLC, not the loco DLC itself)
  'BR Class 390 Pendolino': {
    wrong: 'West Coast Main Line: London Euston - Milton Keynes',
    correct: 'Avanti West Coast BR Class 390 Pendolino EMU Loco Add-on',
  },
  'XC Class 220': {
    wrong: 'Riviera Line',
    correct: 'CrossCountry BR Class 220 Voyager DEMU Loco Add-On',
  },
  'BR Class 158 Sprinter': {
    wrong: 'ScotRail Express',
    correct: 'ScotRail BR Class 158 Sprinter DMU Add-On',
  },
  'Class 170': {
    wrong: 'Birmingham Cross-City Line',
    correct: 'West Midlands Railway & CrossCountry BR Class 170 DMU Add-On',
  },
  'BR Class 380 ScR': {
    wrong: 'Cathcart Circle Line',
    correct: 'Scotrail Class 380 Loco Add-On',
  },
  'BR Class 313/2': {
    wrong: 'East Coastway',
    correct: 'Southern BR Class 313 EMU Add-On',
  },
  'Class 142': {
    wrong: 'Blackpool Branches',
    correct: 'Transport for Wales BR Class 142 Pacer DMU Add-On',
  },
  '1938 Tube Stock LT': {
    wrong: 'Bakerloo Line',
    correct: 'London Underground 1938 Stock EMU Loco Add-On',
  },
  'LNER Flying Scotsman': {
    wrong: 'East Coast Main Line',
    correct: 'LNER Class A3 60103 Flying Scotsman Steam Loco Add-On',
  },
};

// Apply loco name normalization (with logging)
let locoFixCount = 0;
let dlcFixCount = 0;
const unmappedLocos = new Set();

function normalizeLoco(name) {
  if (!name) return name;
  // Normalize non-breaking spaces (U+00A0) to regular spaces before lookup
  const trimmed = name.replace(/\u00A0/g, ' ').trim();
  const mapped = LOCO_NAME_MAP[trimmed];
  if (mapped) {
    locoFixCount++;
    return mapped;
  }
  return trimmed;
}

// Fix DLC(s) needed based on the normalized loco name
function fixDlcNeeded(locoName, dlcNeeded) {
  if (!dlcNeeded || !locoName) return dlcNeeded;
  const fix = DLC_NEEDED_FIXES[locoName];
  if (!fix) return dlcNeeded;
  // Check if the current DLC reference contains the wrong route name
  if (dlcNeeded.toLowerCase().includes(fix.wrong.toLowerCase())) {
    dlcFixCount++;
    return fix.correct;
  }
  return dlcNeeded;
}

// Track unmatched locos for reporting
function trackUnmatched(locoName, baseLocoNames) {
  if (!baseLocoNames.has(locoName)) {
    unmappedLocos.add(locoName);
  }
}

// Cache for lookup IDs
const lookupCache = {};

function getOrInsert(table, field, value) {
  if (!value || String(value).trim() === '' || String(value).trim() === '-') return null;
  const clean = String(value).trim();
  const key = `${table}:${clean}`;
  if (lookupCache[key]) return lookupCache[key];
  const row = db.prepare(`SELECT id FROM ${table} WHERE ${field} = ?`).get(clean);
  if (row) { lookupCache[key] = row.id; return row.id; }
  const result = db.prepare(`INSERT INTO ${table} (${field}) VALUES (?)`).run(clean);
  const id = Number(result.lastInsertRowid);
  lookupCache[key] = id;
  return id;
}

// ── Import company_based sheet ───────────────────────────────────────────────

const mainRows = XLSX.utils.sheet_to_json(workbook.Sheets['company_based'], { defval: null });
console.log(`\nLoaded ${mainRows.length} rows from 'company_based'`);

// Extract hyperlinks from cells
const mainSheet = workbook.Sheets['company_based'];
const mainRange = XLSX.utils.decode_range(mainSheet['!ref']);
const headerRow = [];
for (let c = mainRange.s.c; c <= mainRange.e.c; c++) {
  const cell = mainSheet[XLSX.utils.encode_cell({ r: 0, c })];
  headerRow.push(cell ? String(cell.v) : '');
}
const hyperlinkMap = {};
for (let r = 1; r <= mainRange.e.r; r++) {
  const links = {};
  for (let c = mainRange.s.c; c <= mainRange.e.c; c++) {
    const cell = mainSheet[XLSX.utils.encode_cell({ r, c })];
    if (cell && cell.l && cell.l.Target) {
      links[headerRow[c]] = cell.l.Target;
    }
  }
  if (Object.keys(links).length) hyperlinkMap[r - 1] = links;
}

const insertDlc = db.prepare(`
  INSERT INTO dlc (content_name, steam_name, gameplay_guide, short_name, acronym,
    developer, release_date, tsw_versions,
    new_lighting, new_track_shadows, route_hopping,
    conductor_mode, announcements, train_faults, gen8_compatible,
    requirements_raw, expansions_raw, additional_playable_raw,
    layer_requirements_raw, non_playable_layers_raw, platform_differences,
    dlc_type_id, country_id)
  VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
`);
const insertDlcTrain = db.prepare('INSERT OR IGNORE INTO dlc_train (dlc_id, train_id) VALUES (?, ?)');
const insertStoreLink = db.prepare('INSERT INTO store_link (dlc_id, console_platform, size_gb, store_url) VALUES (?, ?, ?, ?)');
const insertDocLink = db.prepare('INSERT INTO document_link (dlc_id, doc_type, label, url) VALUES (?, ?, ?, ?)');

// Build name→id lookup for DLC (populated during import)
const dlcNameMap = {};

let imported = 0;

for (let i = 0; i < mainRows.length; i++) {
  const row = mainRows[i];
  const contentName = cleanVal(row['Add-On Name']);
  if (!contentName) continue;

  const countryName = cleanRegion(row['Region']);
  const countryId = getOrInsert('country', 'name', countryName);
  const typeName = cleanType(row['Type']);
  const typeId = getOrInsert('dlc_type', 'name', typeName);

  const steamLink = cleanVal(row['Steam Link']);
  const gameplayGuide = cleanVal(row['Gameplay Guide']);

  const tswArr = [];
  for (let v = 1; v <= 6; v++) { if (parseBool(row[`TSW ${v}`])) tswArr.push(v); }

  const result = insertDlc.run(
    contentName,
    cleanVal(row['Steam Name']),
    gameplayGuide,
    cleanVal(row['Short Name']),
    cleanVal(row['Acronym']),
    cleanVal(row['Developer']),
    parseDate(row['Release Date']),
    JSON.stringify(tswArr),
    parseBool(row['New Lighting']),
    parseBool(row['New Track Shadows']),
    parseBool(row['Route Hopping']),
    parseBool(row['Conductor Mode 🎟️']),
    parseBool(row['Annoucements 🔊']),
    parseBool(row['Train Faults ⚙️']),
    parseBool(row['Gen 8 Compatible?']),
    cleanVal(row['Requirements']),
    cleanVal(row['Expansions']),
    cleanVal(row['Additional Playable Locos']),
    cleanVal(row['Layer Requirements']),
    cleanVal(row['NON PLAYABLE LAYERS']),
    cleanVal(row['Platform Differences']),
    typeId,
    countryId,
  );
  const dlcId = Number(result.lastInsertRowid);
  imported++;

  // Index by multiple name variants for layer matching
  dlcNameMap[contentName.toLowerCase()] = dlcId;
  const shortName = cleanVal(row['Short Name']);
  if (shortName) dlcNameMap[shortName.toLowerCase()] = dlcId;
  const acronym = cleanVal(row['Acronym']);
  if (acronym) dlcNameMap[acronym.toLowerCase()] = dlcId;

  // Base Locos → dlc_train (strip emoji markers like 🎟️⚙️🔊 from names)
  const baseLocos = row['Base Locos'];
  if (baseLocos && String(baseLocos).trim() !== 'N/A' && String(baseLocos).trim() !== '-') {
    parseList(String(baseLocos)).forEach(rawName => {
      const trainName = stripEmoji(rawName);
      if (!trainName) return;
      const trainId = getOrInsert('train', 'name', trainName);
      if (trainId) insertDlcTrain.run(dlcId, trainId);
    });
  }

  // Store links
  const rowLinks = hyperlinkMap[i] || {};

  // Steam link (from the Steam Link column)
  if (steamLink) {
    insertStoreLink.run(dlcId, 'PC', null, steamLink);
  }

  // PS5
  const ps5Val = cleanVal(row['PS5 GB + STORE LINK']);
  if (ps5Val) {
    const ps5Url = rowLinks['PS5 GB + STORE LINK'] || null;
    insertStoreLink.run(dlcId, 'PS5', ps5Val, ps5Url);
  }

  // Xbox
  const xboxVal = cleanVal(row['Series S/X GB + STORE LINK']);
  if (xboxVal) {
    const xboxUrl = rowLinks['Series S/X GB + STORE LINK'] || null;
    insertStoreLink.run(dlcId, 'Xbox S/X', xboxVal, xboxUrl);
  }

  // Documents
  const manualLabel = cleanVal(row['MANUALS']);
  if (manualLabel) {
    const manualUrl = rowLinks['MANUALS'] || null;
    insertDocLink.run(dlcId, 'manual', manualLabel, manualUrl);
  }

  const timetableLabel = cleanVal(row['Wonterail TimeTables PDF Links']);
  if (timetableLabel) {
    const timetableUrl = rowLinks['Wonterail TimeTables PDF Links'] || null;
    insertDocLink.run(dlcId, 'timetable', timetableLabel, timetableUrl);
  }

  const guideLabel = cleanVal(row['Collectable Guides']);
  if (guideLabel) {
    const guideUrl = rowLinks['Collectable Guides'] || null;
    insertDocLink.run(dlcId, 'guide', guideLabel, guideUrl);
  }

  if (gameplayGuide) {
    insertDocLink.run(dlcId, 'gameplay_guide', 'Gameplay Guide', gameplayGuide);
  }
}

console.log(`Imported ${imported} DLC records`);

// ── Helper: resolve route name to DLC id ─────────────────────────────────────

function resolveRouteDlc(name) {
  if (!name) return null;
  const lower = name.trim().toLowerCase();
  if (dlcNameMap[lower]) return dlcNameMap[lower];
  // Try partial match — find DLC whose content_name contains this route name
  for (const [key, id] of Object.entries(dlcNameMap)) {
    if (key.includes(lower) || lower.includes(key)) return id;
  }
  return null;
}

// ── Import Layers sheet ──────────────────────────────────────────────────────

const layerRows = XLSX.utils.sheet_to_json(workbook.Sheets['Layers'], { defval: null });
console.log(`\nLoaded ${layerRows.length} rows from 'Layers'`);

const insertLayer = db.prepare(
  'INSERT INTO layer (route_dlc_id, locomotive, needed_dlc, service_type, num_services) VALUES (?, ?, ?, ?, ?)'
);

let currentRoute = null;
let layerCount = 0;
const layerSeen = new Set();

for (const row of layerRows) {
  if (row['Layered route']) currentRoute = row['Layered route'];
  const routeId = resolveRouteDlc(currentRoute);
  const rawLoco = cleanVal(row['Locomotive']);
  if (!rawLoco) continue;

  // Handle multi-value cells (newline-separated locos)
  const locos = rawLoco.split('\n').map(s => s.trim()).filter(Boolean);
  for (const rawL of locos) {
    const loco = normalizeLoco(rawL);
    const dlcNeeded = fixDlcNeeded(loco, cleanVal(row['DLC(s) needed']));
    const dedupKey = `${routeId}|${loco}|${dlcNeeded}|${cleanVal(row['Type of service'])}`;
    if (layerSeen.has(dedupKey)) continue;
    layerSeen.add(dedupKey);
    insertLayer.run(
      routeId,
      loco,
      dlcNeeded,
      cleanVal(row['Type of service']),
      row['Nb of service'] != null ? Number(row['Nb of service']) : null,
    );
    layerCount++;
  }
}
console.log(`Imported ${layerCount} layer rows`);

// ── Import AI Layer sheet ────────────────────────────────────────────────────

const aiRows = XLSX.utils.sheet_to_json(workbook.Sheets['AI Layer'], { defval: null });
console.log(`\nLoaded ${aiRows.length} rows from 'AI Layer'`);

const insertAiLayer = db.prepare(
  'INSERT INTO ai_layer (route_dlc_id, locomotive, needed_dlc, service_type) VALUES (?, ?, ?, ?)'
);

currentRoute = null;
let aiCount = 0;
const aiSeen = new Set();

for (const row of aiRows) {
  if (row['Layered route']) currentRoute = row['Layered route'];
  const routeId = resolveRouteDlc(currentRoute);
  const rawLoco = cleanVal(row['Locomotive']);
  if (!rawLoco) continue;

  const locos = rawLoco.split('\n').map(s => s.trim()).filter(Boolean);
  for (const rawL of locos) {
    const loco = normalizeLoco(rawL);
    const dlcNeeded = fixDlcNeeded(loco, cleanVal(row['DLC(s) needed']));
    const dedupKey = `${routeId}|${loco}|${dlcNeeded}|${cleanVal(row['Type of service'])}`;
    if (aiSeen.has(dedupKey)) continue;
    aiSeen.add(dedupKey);
    insertAiLayer.run(
      routeId,
      loco,
      dlcNeeded,
      cleanVal(row['Type of service']),
    );
    aiCount++;
  }
}
console.log(`Imported ${aiCount} AI layer rows`);

// ── Import Substitutions sheet ───────────────────────────────────────────────

const subRows = XLSX.utils.sheet_to_json(workbook.Sheets['Substitutions'], { defval: null });
console.log(`\nLoaded ${subRows.length} rows from 'Substitutions'`);

const insertSub = db.prepare(
  'INSERT INTO substitution (route_dlc_id, locomotive, needed_dlc, replaced_locomotive) VALUES (?, ?, ?, ?)'
);

currentRoute = null;
let subCount = 0;
const subSeen = new Set();

for (const row of subRows) {
  if (row['Layered route']) currentRoute = row['Layered route'];
  const routeId = resolveRouteDlc(currentRoute);
  const rawLoco = cleanVal(row['Locomotive']);
  if (!rawLoco) continue;

  const locos = rawLoco.split('\n').map(s => s.trim()).filter(Boolean);
  for (const rawL of locos) {
    const loco = normalizeLoco(rawL);
    const dlcNeeded = fixDlcNeeded(loco, cleanVal(row['DLC(s) needed']));
    const replaced = cleanVal(row['Replaced locomotive']);
    const dedupKey = `${routeId}|${loco}|${dlcNeeded}|${replaced}`;
    if (subSeen.has(dedupKey)) continue;
    subSeen.add(dedupKey);
    insertSub.run(
      routeId,
      loco,
      dlcNeeded,
      replaced,
    );
    subCount++;
  }
}
console.log(`Imported ${subCount} substitution rows`);

// ── Summary ──────────────────────────────────────────────────────────────────

// ── Corrections report ───────────────────────────────────────────────────────

// Build set of all base loco names for unmatched tracking
const allBaseLocos = new Set();
db.prepare('SELECT name FROM train').all().forEach(r => allBaseLocos.add(r.name));

// Check which layer locos don't match any base loco
const layerLocoNames = new Set();
db.prepare('SELECT DISTINCT locomotive FROM layer').all().forEach(r => layerLocoNames.add(r.locomotive));
db.prepare('SELECT DISTINCT locomotive FROM ai_layer').all().forEach(r => layerLocoNames.add(r.locomotive));
db.prepare('SELECT DISTINCT locomotive FROM substitution').all().forEach(r => layerLocoNames.add(r.locomotive));

const stillUnmatched = [...layerLocoNames].filter(l => !allBaseLocos.has(l)).sort();

// ── Restore saved prices ─────────────────────────────────────────────────────

if (Object.keys(savedPrices).length) {
  const restoreStmt = db.prepare(`
    UPDATE dlc SET price=?, price_value=?, price_original=?, price_original_value=?,
      price_currency=?, price_discount=?, price_updated_at=?
    WHERE content_name=?
  `);
  let restored = 0;
  for (const [name, p] of Object.entries(savedPrices)) {
    const r = restoreStmt.run(
      p.price, p.price_value, p.price_original, p.price_original_value,
      p.price_currency, p.price_discount, p.price_updated_at, name
    );
    if (r.changes > 0) restored++;
  }
  console.log(`\nRestored ${restored} prices from previous database`);
}

if (Object.keys(savedUserState).length) {
  const restoreStateStmt = db.prepare(`UPDATE dlc SET owned=?, in_cart=? WHERE content_name=?`);
  let restored = 0;
  for (const [name, s] of Object.entries(savedUserState)) {
    const r = restoreStateStmt.run(s.owned || 0, s.in_cart || 0, name);
    if (r.changes > 0) restored++;
  }
  console.log(`Restored ${restored} owned/cart states from previous database`);

  // Restore settings
  if (savedUserState._settings) {
    const settingStmt = db.prepare(`INSERT OR REPLACE INTO setting (key, value) VALUES (?, ?)`);
    savedUserState._settings.forEach(s => settingStmt.run(s.key, s.value));
    console.log(`Restored ${savedUserState._settings.length} settings`);
  }
}

console.log('\n=== Corrections Report ===');
console.log(`  Loco names normalized: ${locoFixCount}`);
console.log(`  DLC references fixed: ${dlcFixCount}`);
console.log(`  Layer locos matching a base loco: ${[...layerLocoNames].filter(l => allBaseLocos.has(l)).length} / ${layerLocoNames.size}`);
if (stillUnmatched.length) {
  console.log(`  Still unmatched (${stillUnmatched.length}):`);
  stillUnmatched.forEach(l => console.log(`    - ${l}`));
}

console.log('\nDatabase summary:');
const tables = ['dlc', 'country', 'dlc_type', 'platform', 'train', 'dlc_train',
  'store_link', 'document_link', 'layer', 'ai_layer', 'substitution'];
tables.forEach(t => {
  const row = db.prepare(`SELECT COUNT(*) as n FROM ${t}`).get();
  console.log(`  ${t}: ${row.n}`);
});

console.log('\nImport complete! Run: node server.js');
db.close();
