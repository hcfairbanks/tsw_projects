"""
Import DLC data from merged_data.ods into a fresh database.db

Relationships:
- A Loco/Expansion DLC joins to a Route via "Requirements" (dlc_requirement)
- A Route DLC joins to its Expansion DLCs via "Expansions" (route_layer)
- Additional Playable Locos are trains available per timetable section (route_layer_train)
- Non Playable Layers are scenic/AI trains on a route
"""

import sqlite3
import pandas as pd
import re
import os
from odf.opendocument import load as odf_load
from odf.table import Table, TableRow, TableCell
from odf.text import P, A as OdfA

DB_PATH = os.path.join(os.path.dirname(__file__), 'database.db')
SCHEMA_PATH = os.path.join(os.path.dirname(__file__), 'schema.sql')
ODS_PATH = os.path.join(os.path.dirname(__file__), 'spreadsheets', 'merged_data.ods')

REGION_MAP = {
    'US': 'United States', 'UK': 'United Kingdom', 'DE': 'Germany',
    'CH': 'Switzerland', 'AT': 'Austria', 'CA': 'Canada',
    'FR': 'France', 'CZ': 'Czech Republic', 'NL': 'Netherlands', 'N/A': None,
}

# Manual mapping for requirement/expansion short names -> content_name
REQUIREMENT_NAME_FIXES = {
    'Hauptstrecke Rhien Ruhr': 'Hauptstrecke Rhein-Ruhr: Duisburg - Bochum Route Add-On',
    'Rhien Ruhr Osten': 'Rhein-Ruhr Osten: Wuppertal - Hagen Route Add-On',
    'Koln - Aachen': 'Schnellfahrstrecke Koln-Aachen Route',
    'Hamburg - Lubeck': 'Hauptstrecke Hamburg - Lübeck Route Add-On',
    'Ruhr Sieg Nord': 'Ruhr-Sieg Nord: Hagen - Finnentrop Route Add-On',
    'Linke Rhienstrecke': 'Linke Rheinstrecke: Mainz - Koblenz Add-On',
    'Nahverkehr Dresdenn': 'Nahverkehr Dresden -Riesa Route Add-On',
    'Nahverkehr Dresden': 'Nahverkehr Dresden -Riesa Route Add-On',
    'Leipzig - Dresden': 'Bahnstrecke Leipzig - Dresden Route Add-On',
    'Salzburg - Rosenheim': 'Bahnstrecke Salzburg - Rosenheim Route Add-On',
    'Kassel - Wurzburg': 'Schnellfahrstrecke Kassel - Würzburg Route Add-On',
    'Edinburgh - Glasgow': 'ScotRail Express: Edinburgh - Glasgow Route Add-On',
    'New York Trenton': 'Northeast Corridor: New York - Trenton Route Add-On',
    'Boston - Providence': 'Northeast Corridor: Boston - Providence Route Add-On',
    'Arosa Line': 'Arosalinie: Chur - Arosa Route Add-On',
    'Cajon Pass': 'Cajon Pass: Barstow - San Bernardino Route Add-On',
    'Tees Valley Line': 'Tees Valley Line: Darlington – Saltburn-by-the-Sea Route Add-On',
    'West Somerset Railway': 'West Somerset Railway Route Add-On',
    'WCML: London Euston - Milton Keynes': 'West Coast Main Line: London Euston - Milton Keynes Route Add-On',
    'WCML: Preston - Carlisle': 'WCML: Preston - Carlisle Route Add-On',
    'WCML: Birmingham - Crewe': 'West Coast Main Line: Birmingham - Crewe Route Add-On',
    'Northern Trans-Pennine': 'Northern Trans-Pennine: Manchester - Leeds Route Add-On',
    'Peninsula Corridor': 'Peninsula Corridor: San Francisco - San Jose Route Add-On (Caltrain branded content removed from sale due to expired licence on February 27th 2025. Re-released as an unbranded add-on for TSW 6)',
    'Sand Patch Grade': 'Sand Patch Grade Route Add-On',
    'Midland Main Line': 'Midland Main Line: Leicester - Derby & Nottingham Route Add-On',
    'Great Western Express': 'Great Western Express Route Add-On',
    'Southeastern High Speed': 'Southeastern Highspeed: London St Pancras – Ashford Intl & Faversham Route Add-On',
    'ECML: Peterborough - Doncaster': 'East Coast Main Line: Peterborough - Doncaster Route Add-On',
    'Rapid Transit': 'Rapid Transit Route Add-On',
    'Main Spessart Bahn': 'Main Spessart Bahn: Aschaffenburg - Gemünden Route Add-On',
    'Frankfurt - Fulda': 'Frankfurt - Fulda: Kinzigtalbahn Route Add-On',
    'Horseshoe Curve': 'Horseshoe Curve: Altoona - Johnstown & South Fork Route Add-On',
    'Birmingham Cross-City': 'Birmingham Cross-City Line: Lichfield - Bromsgrove & Redditch Route Add-On',
    'Riviera Line': 'Riviera Line: Exeter - Plymouth & Paignton Route Add-On',
    'East Coastway': 'East Coastway: Brighton - Eastbourne & Seaford Route Add-On',
    'Bakerloo Line': 'Bakerloo Line Route Add-On',
    'Morristown Line': 'Morristown Line: New York & Hoboken - Dover Route Add-On',
    'Antelope Valley Line': 'Antelope Valley Line: Los Angeles - Lancaster Route Add-On',
    'San Bernardino Line': 'San Bernardino Line: Los Angeles - San Bernardino Route Add-On',
    'Fife Circle Line': 'Fife Circle Line: Edinburgh - Markinch via Dunfermline & Kirkcaldy',
    'Cathcart Circle Line': 'Cathcart Circle Line: Glasgow - Newton & Neilston Route Add-On',
    'Long Island Rail Road': 'Long Island Rail Road: New York - Hicksville Route Add-On',
    'Cardiff City Network': 'Cardiff City Network: Radur & Coryton to Penarth & Bae Caerdydd Route Add-On',
    'West Cornwall Local': 'West Cornwall Local: Penzance - St Austell & St Ives Route Add-On',
    'CSX Heavy Haul': 'CSX Heavy Haul (TSW 2020 & PC Only)',
    'Expert 101': 'Expert DB BR 101 & IC Steuerwagen Loco Add-On',
    "Thomas & Friends\u2122 Visit the West Somerset Railway": "Thomas & Friends\u2122 Visit the West Somerset Railway Add-On",
    'LIRR Commuter': 'LIRR Commuter: New York - Long Beach, Hemstead & Hicksville',
    # Expansion short names
    'LU 1938 Stock': 'London Underground 1938 Stock EMU Loco Add-On',
    'NJ Silver 1972 Stock': 'New Journeys - Silver 1972 Stock Add-On',
    'Metrolink F59PHR Loco Add-On': 'Metrolink F59PHR Loco Add-On',
    'BR Class 323': 'Centro Regional Railways BR Class 323 Add-On',
    'BR Class 170': 'West Midlands Railway & CrossCountry BR Class 170 DMU Add-On',
    'BNSF SD70ACe': 'BNSF SD70ACe Loco Add-On',
    'Sante Fe on Cajon Pass': 'Santa Fe on Cajon Pass Add-On',
    'Sante Fe F7': 'Santa Fe F7 Add-On',
    'BR Class 142': 'Transport for Wales BR Class 142 Pacer DMU Add-On',
    'ScotRail Class 380': 'Scotrail Class 380 Loco Add-On',
    'Cargo Line Vol 3: Intermodal': 'Cargo Line Vol 3: Intermodal Add-on',
    'ECML Diesel Railtour Gameplay Pack': 'ECML Diesel Railtour Gameplay Pack',
    'Southern BR Class 313 EMU Add-On': 'Southern BR Class 313 EMU Add-On',
    'Southern BR Class 171 & BR Class 377/3 Add-On': 'Southern BR Class 171 & BR Class 377/3 Add-On',
    'ScotRail BR Class 158': 'ScotRail BR Class 158 Sprinter DMU Add-On',
    'DB BR 111 & n-Wagen Pack': 'DB BR 111 & n-Wagen Pack',
    'FlixTrain BR 193 Vectron Loco Add-On': 'FlixTrain BR 193 Vectron Loco Add-On',
    'Cargo Line Vol. 1: Petroleum': 'Cargo Line Vol. 1: Petroleum Add-On',
    'Cargo Line Vol. 2: Aggregates': 'Cargo Line Vol. 2: Aggregates Add-on',
    'Cargo Line Vol. 4 - Military': 'Cargo Line Vol. 4 - Military Add-On',
    'Diesel Legends of the Great Western': 'Diesel Legends of the Great Western Add-On',
    'DB BR 218': 'DB BR 218 Diesel Loco Add-On',
    'DB BR 101': 'DB BR 101 Loco Add-On',
    'Norfolk Southern Heritage Livery Collection': 'Norfolk Southern Heritage Livery Collection Add-On',
    'Union Pacific Heritage Collection': 'Union Pacific Heritage Collection',
    'BR 194 & E94 Railtour Pack': 'BR 194 & E94 Railtour Pack',
    'DB BR 204': 'DB BR 204 Loco Add-On',
    'NJ TRANSIT ALP-45DP': 'NJ TRANSIT ALP-45DP Electro-Diesel Loco Add-On',
    'Expert DB BR 101 & IC Steuerwagen': 'Expert DB BR 101 & IC Steuerwagen Loco Add-On',
    'Railpool BR 193 Vectron': 'Railpool BR 193 Vectron Loco Add-On',
    'Amtrak Acela Express': 'Amtrak Acela Express Loco Add-On',
    'HSP46 Pack': 'MBTA Providence/Stoughton Line HSP46 Pack',
    'BR Heavy Freight Pack': 'BR Heavy Freight Pack Loco Add-On',
    "MP15DC Diesel Switcher": "MP15DC Diesel Switcher Loco Add-On (Caltrain branded content removed from sale due to expired licence on February 27th 2025. Re-released as an unbranded add-on for TSW 6)",
    "MP36PH-3C 'Baby Bullet'": "MP36PH-3C 'Baby Bullet' Loco Add-On (Caltrain branded content removed from sale due to expired licence on February 27th 2025. Re-released as an unbranded add-on for TSW 6)",
    'DB BR 155': 'DB BR 155 Loco Add-On',
    'DB BR 363': 'DB BR 363 Loco Add-On',
    'Metrolink F59PHR': 'Metrolink F59PHR Loco Add-On',
    'Metrolink Holiday Train Pack': 'Metrolink Holiday Train Pack',
    'CSX C40-8W': 'CSX C40-8W Loco Add-On',
    'NJ - CSX SD40': 'New Journeys - CSX SD40 Add-On',
    'DB BR 403 ICE 3 Railbow': 'DB BR 403 ICE 3 Railbow Loco Add-On',
    'Dispolok BR 182': 'Dispolok BR 182 Add-On',
    'Expert DB BR 101 on Kassel - W\u00fcrzburg Gameplay Pack': 'Expert DB BR 101 on Kassel - W\u00fcrzburg Gameplay Pack',
    'DB BR 187': 'DB BR 187 Loco Add-On',
    'NJ - S-Bahn K\u00f6ln BR 423': 'New Journeys - S-Bahn K\u00f6ln BR 423 Add-On',
    'Edinburgh - Glasgow: Engineering Express Pack': 'Edinburgh - Glasgow: Engineering Express Pack',
    'Rail Head Treatment Train': 'Rail Head Treatment Train Add-On',
    'Rail Operations Group BR Class 37/7': 'Rail Operations Group BR Class 37/7 Loco Add-On',
    'Thameslink BR Class 700/0': 'Thameslink BR Class 700/0 Loco Add-On',
    'BR Class 86/2 & Mk2F Coaches': 'BR Class 86/2 & Mk2F Coaches Loco Add-On',
    'Cargo Line Vol. 5: Nuclear': 'Cargo Line Vol. 5: Nuclear Add-On',
    'BR Class 20 \'Chopper\'': 'BR Class 20 \'Chopper\' Loco Add-On',
    'BR Class 31': 'BR Class 31 Loco Add-On',
    'DB BR 182': 'DB BR 182 Loco Add-On',
    'DB G6 Diesel Shunter': 'DB G6 Diesel Shunter Add-On',
    'XC BR Class 220 Voyager': 'CrossCountry BR Class 220 Voyager DEMU Loco Add-On',
    "BR Class 33": "BR Class 33 Loco Add-On",
    "BR Class 52 'Western'": "BR Class 52 'Western' Loco Add-On",
    "Thomas & Friends 80th Anniversary Expansion": "Thomas & Friends 80th Anniversary Expansion",
    "Thomas & Friends\u2122 Visit the West Somerset Railway": "Thomas & Friends\u2122 Visit the West Somerset Railway Add-On",
    'West Cornwall Steam Railtour': 'West Cornwall Steam Railtour Add-On',
    'AWC BR Class 390 Pendolino': 'Avanti West Coast BR Class 390 Pendolino EMU Loco Add-on',
    'AWC BR Class 805 Evero': 'Avanti West Coast BR Class 805 Evero BMU Add-On',
    'HKA Bogie Hopper Wagon Pack': 'HKA Bogie Hopper Wagon Pack',
    'DB BR 294': 'DB BR 294 Diesel Shunter Loco Add-On',
    'Expert DB BR 145': 'Expert DB BR 145 Loco Add-On',
    'LIRR M3 EMU': 'LIRR M3 EMU Loco Add-On',
    'Flying Scotsman': 'LNER Class A3 60103 Flying Scotsman Steam Loco Add-On',
    'Amtrak SW1000R': 'Amtrak SW1000R Loco Add-On',  # may not exist
    'RhB Anniversary Collection': 'RhB Anniversary Collection',
    'Cargo Line Vol 3: Intermodal': 'Cargo Line Vol 3: Intermodal Add-on',
}


# ─── helpers ───────────────────────────────────────────────────────

def clean_text(val):
    if pd.isna(val) or not str(val).strip():
        return None
    return str(val).strip()

def extract_region_code(s):
    if not s: return None
    m = re.match(r'^([A-Z/]+)', s.strip())
    return m.group(1) if m else None

def extract_type_name(s):
    if not s: return None
    m = re.match(r'^[\x00-\x7F]+', s.strip())
    return m.group(0).strip() if m else s.strip()

def parse_yes_no(val):
    if pd.isna(val): return 0
    return 1 if str(val).strip().lower() == 'yes' else 0

def strip_emoji_flags(text):
    return re.sub(r'[\U0001F39F\U0001F50A\u2699\uFE0F\u200B]+', '', text).strip()

def get_cell_text(element):
    if hasattr(element, 'data'): return element.data
    return ''.join(get_cell_text(c) for c in element.childNodes)

def read_ods_column_multiline(ods_path, col_name):
    """Read ODS column preserving multi-line cells as list of lines per row."""
    doc = odf_load(ods_path)
    sheets = doc.spreadsheet.getElementsByType(Table)
    sheet = next((s for s in sheets if s.getAttribute('name') == 'company_based'), sheets[0])
    rows = sheet.getElementsByType(TableRow)

    header_cells = rows[0].getElementsByType(TableCell)
    target_col = None
    ci = 0
    for cell in header_cells:
        text = get_cell_text(cell).strip()
        repeat = cell.getAttribute('numbercolumnsrepeated')
        r = int(repeat) if repeat else 1
        if col_name in text:
            target_col = ci
            break
        ci += r
    if target_col is None:
        raise ValueError(f"Column '{col_name}' not found")

    result = {}
    for row_idx in range(1, len(rows)):
        row = rows[row_idx]
        cells = row.getElementsByType(TableCell)
        ci = 0
        for cell in cells:
            repeat = cell.getAttribute('numbercolumnsrepeated')
            r = int(repeat) if repeat else 1
            if ci <= target_col < ci + r:
                ps = cell.getElementsByType(P)
                lines = [get_cell_text(p).strip() for p in ps if get_cell_text(p).strip()]
                if lines:
                    result[row_idx - 1] = lines
                break
            ci += r
    return result

def read_ods_column_links(ods_path, col_name):
    """
    Read an ODS column extracting hyperlinks.
    Returns: dict mapping row_index (0-based) to list of (label, url) tuples.
    """
    doc = odf_load(ods_path)
    sheets = doc.spreadsheet.getElementsByType(Table)
    sheet = next((s for s in sheets if s.getAttribute('name') == 'company_based'), sheets[0])
    rows = sheet.getElementsByType(TableRow)

    header_cells = rows[0].getElementsByType(TableCell)
    target_col = None
    ci = 0
    for cell in header_cells:
        text = get_cell_text(cell).strip()
        repeat = cell.getAttribute('numbercolumnsrepeated')
        r = int(repeat) if repeat else 1
        if col_name in text:
            target_col = ci
            break
        ci += r
    if target_col is None:
        raise ValueError(f"Column '{col_name}' not found")

    result = {}
    for row_idx in range(1, len(rows)):
        row = rows[row_idx]
        cells = row.getElementsByType(TableCell)
        ci = 0
        for cell in cells:
            repeat = cell.getAttribute('numbercolumnsrepeated')
            r = int(repeat) if repeat else 1
            if ci <= target_col < ci + r:
                links = []
                for a in cell.getElementsByType(OdfA):
                    href = a.getAttribute('href') or ''
                    label = get_cell_text(a).strip()
                    if href:
                        links.append((label, href))
                # If no hyperlinks but there's text (e.g. a plain URL)
                if not links:
                    text = get_cell_text(cell).strip()
                    if text and (text.startswith('http') or text.startswith('www')):
                        links.append((text, text))
                if links:
                    result[row_idx - 1] = links
                break
            ci += r
    return result


def parse_date(val):
    if pd.isna(val) or not str(val).strip(): return None
    s = str(val).strip()
    m = re.match(r'^(\d{1,2})/(\d{1,2})/(\d{2,4})$', s)
    if not m: return s
    day, month, year = m.groups()
    if len(year) == 2: year = '20' + year
    return f"{year}-{month.zfill(2)}-{day.zfill(2)}"


def build_dlc_lookup(conn):
    """Map various name forms to DLC IDs."""
    lookup = {}
    for dlc_id, content_name, short_name, acronym in conn.execute(
            "SELECT id, content_name, short_name, acronym FROM dlc").fetchall():
        lookup[content_name] = dlc_id
        if short_name: lookup[short_name] = dlc_id
        if acronym: lookup[acronym] = dlc_id
    for fix_name, real_name in REQUIREMENT_NAME_FIXES.items():
        if real_name in lookup:
            lookup[fix_name] = lookup[real_name]
    return lookup


def resolve_dlc(name, lookup):
    """Resolve informal DLC name to ID."""
    name = name.strip()
    if not name or name.lower() == 'n/a': return None
    if name in lookup: return lookup[name]
    nl = name.lower()
    for key, dlc_id in lookup.items():
        if nl in key.lower() or key.lower() in nl:
            return dlc_id
    return None


def parse_apl_lines(lines):
    """Parse Additional Playable Locos into (timetable_section, train_name, [acronyms])."""
    results = []
    section = None
    for line in lines:
        line = strip_emoji_flags(line)
        if not line or line.lower() == 'n/a': continue
        sm = re.match(r'^\[(.+?)\]$', line)
        if sm:
            section = sm.group(1)
            continue
        am = re.match(r'^(.+?)\s*\(([^)]+)\)\s*$', line)
        if am:
            train = am.group(1).strip()
            acrs = [a.strip() for a in am.group(2).split(',') if a.strip()]
        else:
            train = line.strip()
            acrs = []
        if train:
            results.append((section, train, acrs))
    return results


# ─── main ──────────────────────────────────────────────────────────

def main():
    print(f"Creating database at {DB_PATH}")
    conn = sqlite3.connect(DB_PATH)
    conn.execute("PRAGMA foreign_keys = ON")

    with open(SCHEMA_PATH, 'r') as f:
        conn.executescript(f.read())

    conn.executescript("""
        CREATE TABLE IF NOT EXISTS route_layer (
            id INTEGER PRIMARY KEY AUTOINCREMENT,
            route_dlc_id INTEGER REFERENCES dlc(id) ON DELETE CASCADE,
            loco_dlc_id INTEGER REFERENCES dlc(id) ON DELETE CASCADE,
            UNIQUE(route_dlc_id, loco_dlc_id)
        );
        CREATE TABLE IF NOT EXISTS route_layer_train (
            id INTEGER PRIMARY KEY AUTOINCREMENT,
            route_layer_id INTEGER REFERENCES route_layer(id) ON DELETE CASCADE,
            train_id INTEGER REFERENCES train(id) ON DELETE CASCADE,
            train_type TEXT NOT NULL CHECK(train_type IN ('playable','scenic')),
            timetable_section TEXT,
            conductor_mode INTEGER DEFAULT 0,
            UNIQUE(route_layer_id, train_id, timetable_section)
        );
    """)

    # ── Read spreadsheet ──
    print(f"Reading {ODS_PATH}")
    df = pd.read_excel(ODS_PATH, engine='odf', sheet_name='company_based')
    print(f"  {len(df)} rows")
    dlc_names = list(df['Add-On Name'].values)

    # ── Lookup tables ──
    country_ids = {}
    for region_str in df['Region'].dropna().unique():
        code = extract_region_code(str(region_str))
        name = REGION_MAP.get(code)
        if name:
            conn.execute("INSERT OR IGNORE INTO country (name) VALUES (?)", (name,))
            country_ids[code] = conn.execute("SELECT id FROM country WHERE name=?", (name,)).fetchone()[0]

    type_ids = {}
    for ts in df['Type'].dropna().unique():
        tn = extract_type_name(str(ts))
        if tn:
            conn.execute("INSERT OR IGNORE INTO dlc_type (name) VALUES (?)", (tn,))
            type_ids[tn] = conn.execute("SELECT id FROM dlc_type WHERE name=?", (tn,)).fetchone()[0]

    for p in ['PC', 'PS5', 'Xbox Series S/X']:
        conn.execute("INSERT OR IGNORE INTO platform (name) VALUES (?)", (p,))
    conn.commit()

    # ── DLC records ──
    for idx, row in df.iterrows():
        cn = clean_text(row.get('Add-On Name'))
        if not cn: continue
        conn.execute("""
            INSERT INTO dlc (content_name, steam_name, short_name, acronym, developer, release_date,
                tsw_1,tsw_2,tsw_3,tsw_4,tsw_5,tsw_6,
                new_lighting, new_track_shadows, route_hopping,
                conductor_mode, announcements, train_faults, gen8_compatible,
                dlc_type_id, country_id)
            VALUES (?,?,?,?,?,?, ?,?,?,?,?,?, ?,?,?, ?,?,?,?, ?,?)
        """, (cn, clean_text(row.get('Steam Name')),
              clean_text(row.get('Short Name')), clean_text(row.get('Acronym')),
              clean_text(row.get('Developer')), parse_date(row.get('Release Date')),
              parse_yes_no(row.get('TSW 1')), parse_yes_no(row.get('TSW 2')),
              parse_yes_no(row.get('TSW 3')), parse_yes_no(row.get('TSW 4')),
              parse_yes_no(row.get('TSW 5')), parse_yes_no(row.get('TSW 6')),
              parse_yes_no(row.get('New Lighting')),
              parse_yes_no(row.get('New Track Shadows')),
              parse_yes_no(row.get('Route Hopping')),
              parse_yes_no(row.get('Conductor Mode \U0001F39F\uFE0F')),
              parse_yes_no(row.get('Annoucements \U0001F50A')),
              parse_yes_no(row.get('Train Faults \u2699\uFE0F')),
              parse_yes_no(row.get('Gen 8 Compatible?')),
              type_ids.get(extract_type_name(clean_text(row.get('Type')))),
              country_ids.get(extract_region_code(clean_text(row.get('Region'))))))
    conn.commit()
    print(f"  {conn.execute('SELECT COUNT(*) FROM dlc').fetchone()[0]} DLCs inserted")

    name_to_id = {r[1]: r[0] for r in conn.execute("SELECT id, content_name FROM dlc").fetchall()}

    # ── Base Locos ──
    print("Populating base locos...")
    for row_idx, lines in read_ods_column_multiline(ODS_PATH, 'Base Locos').items():
        if row_idx >= len(dlc_names): continue
        dlc_id = name_to_id.get(str(dlc_names[row_idx]).strip())
        if not dlc_id: continue
        for raw in lines:
            tn = strip_emoji_flags(raw)
            if not tn or tn.lower() == 'n/a': continue
            conn.execute("INSERT OR IGNORE INTO train (name) VALUES (?)", (tn,))
            tid = conn.execute("SELECT id FROM train WHERE name=?", (tn,)).fetchone()[0]
            conn.execute("INSERT OR IGNORE INTO dlc_train (dlc_id, train_id) VALUES (?,?)", (dlc_id, tid))
    conn.commit()
    print(f"  {conn.execute('SELECT COUNT(*) FROM train').fetchone()[0]} trains, "
          f"{conn.execute('SELECT COUNT(*) FROM dlc_train').fetchone()[0]} links")

    dlc_lookup = build_dlc_lookup(conn)

    # ══════════════════════════════════════════════════════════════════
    # DOCUMENTS: Manuals, Timetables, Guides, Gameplay Guides
    # ══════════════════════════════════════════════════════════════════
    print("Populating documents...")
    doc_count = 0
    doc_cols = [
        ('MANUALS', 'manual'),
        ('Wonterail TimeTables PDF Links', 'timetable'),
        ('Collectable Guides', 'guide'),
        ('Gameplay Guide', 'gameplay_guide'),
    ]
    for col_name, doc_type in doc_cols:
        for row_idx, links in read_ods_column_links(ODS_PATH, col_name).items():
            if row_idx >= len(dlc_names): continue
            dlc_id = name_to_id.get(str(dlc_names[row_idx]).strip())
            if not dlc_id: continue
            for label, url in links:
                if not url or url.lower() == 'n/a': continue
                conn.execute("INSERT INTO document_link (dlc_id, doc_type, label, url) VALUES (?,?,?,?)",
                             (dlc_id, doc_type, label, url))
                doc_count += 1
    conn.commit()
    print(f"  {doc_count} document links")

    # ══════════════════════════════════════════════════════════════════
    # STORE LINKS: Steam, PS5, Xbox
    # ══════════════════════════════════════════════════════════════════
    print("Populating store links...")
    store_count = 0
    store_cols = [
        ('Steam Link', 'PC', None),
        ('PS5 GB + STORE LINK', 'PS5', True),
        ('Series S/X GB + STORE LINK', 'Xbox Series S/X', True),
    ]
    for col_name, platform, has_size in store_cols:
        for row_idx, links in read_ods_column_links(ODS_PATH, col_name).items():
            if row_idx >= len(dlc_names): continue
            dlc_id = name_to_id.get(str(dlc_names[row_idx]).strip())
            if not dlc_id: continue
            for label, url in links:
                if not url or url.lower() == 'n/a': continue
                size_gb = label if has_size else None
                conn.execute("INSERT INTO store_link (dlc_id, console_platform, size_gb, store_url) VALUES (?,?,?,?)",
                             (dlc_id, platform, size_gb, url))
                store_count += 1
    conn.commit()
    print(f"  {store_count} store links")

    # ══════════════════════════════════════════════════════════════════
    # REQUIREMENTS: Loco/Expansion -> Route (dlc_requirement)
    # ══════════════════════════════════════════════════════════════════
    print("Populating requirements (loco->route)...")
    req_missed = []
    req_count = 0
    for row_idx, lines in read_ods_column_multiline(ODS_PATH, 'Requirements').items():
        if row_idx >= len(dlc_names): continue
        dlc_id = name_to_id.get(str(dlc_names[row_idx]).strip())
        if not dlc_id: continue
        group = 0
        for line in lines:
            line = line.strip()
            if not line or line.lower() == 'n/a': continue
            if line.lower() == 'or':
                group += 1
                continue
            rid = resolve_dlc(line, dlc_lookup)
            if rid:
                conn.execute("INSERT OR IGNORE INTO dlc_requirement (dlc_id, required_dlc_id, requirement_group) VALUES (?,?,?)",
                             (dlc_id, rid, group))
                req_count += 1
            else:
                req_missed.append(f"  [{df.iloc[row_idx]['Acronym']}] '{line}'")
    conn.commit()
    print(f"  {req_count} requirement links")
    if req_missed:
        print(f"  UNMATCHED ({len(req_missed)}):")
        for m in req_missed: print(m)

    # ══════════════════════════════════════════════════════════════════
    # EXPANSIONS: Route → Expansion DLCs (route_layer)
    # ══════════════════════════════════════════════════════════════════
    print("Populating expansions (route->expansion DLCs)...")
    exp_count = 0
    exp_missed = []
    for row_idx, lines in read_ods_column_multiline(ODS_PATH, 'Expansions').items():
        if row_idx >= len(dlc_names): continue
        route_id = name_to_id.get(str(dlc_names[row_idx]).strip())
        if not route_id: continue
        for line in lines:
            line = line.strip()
            if not line or line.lower() == 'n/a': continue
            exp_id = resolve_dlc(line, dlc_lookup)
            if exp_id:
                conn.execute("INSERT OR IGNORE INTO route_layer (route_dlc_id, loco_dlc_id) VALUES (?,?)",
                             (route_id, exp_id))
                exp_count += 1
            else:
                exp_missed.append(f"  [{df.iloc[row_idx]['Acronym']}] '{line}'")
    conn.commit()
    print(f"  {exp_count} expansion links (route_layer)")
    if exp_missed:
        print(f"  UNMATCHED ({len(exp_missed)}):")
        for m in exp_missed: print(m)

    # ══════════════════════════════════════════════════════════════════
    # ADDITIONAL PLAYABLE LOCOS → route_layer_train
    # All APL trains go through a self-referencing route_layer (route→route).
    # route_layer stays clean: only Expansions from the column above.
    # ══════════════════════════════════════════════════════════════════
    print("Populating additional playable locos...")
    apl_missed = set()
    for row_idx, lines in read_ods_column_multiline(ODS_PATH, 'Additional Playable Locos').items():
        if row_idx >= len(dlc_names): continue
        route_id = name_to_id.get(str(dlc_names[row_idx]).strip())
        if not route_id: continue

        # Single self-referencing route_layer for this route's APL trains
        conn.execute("INSERT OR IGNORE INTO route_layer (route_dlc_id, loco_dlc_id) VALUES (?,?)",
                     (route_id, route_id))
        rl_id = conn.execute(
            "SELECT id FROM route_layer WHERE route_dlc_id=? AND loco_dlc_id=?",
            (route_id, route_id)).fetchone()[0]

        parsed = parse_apl_lines(lines)
        for section, train_name, acronyms in parsed:
            conn.execute("INSERT OR IGNORE INTO train (name) VALUES (?)", (train_name,))
            train_id = conn.execute("SELECT id FROM train WHERE name=?", (train_name,)).fetchone()[0]
            conn.execute("""INSERT OR IGNORE INTO route_layer_train
                (route_layer_id, train_id, train_type, timetable_section)
                VALUES (?,?,'playable',?)""", (rl_id, train_id, section))

    conn.commit()
    print(f"  {conn.execute('SELECT COUNT(*) FROM route_layer').fetchone()[0]} route_layer entries")
    print(f"  {conn.execute('SELECT COUNT(*) FROM route_layer_train WHERE train_type=?', ('playable',)).fetchone()[0]} playable train entries")

    # ══════════════════════════════════════════════════════════════════
    # NON PLAYABLE LAYERS (scenic trains)
    # ══════════════════════════════════════════════════════════════════
    print("Populating non-playable layers (scenic)...")
    scenic_count = 0
    for row_idx, lines in read_ods_column_multiline(ODS_PATH, 'NON PLAYABLE LAYERS').items():
        if row_idx >= len(dlc_names): continue
        route_id = name_to_id.get(str(dlc_names[row_idx]).strip())
        if not route_id: continue

        # Self-referencing route_layer for scenic trains from the route itself
        conn.execute("INSERT OR IGNORE INTO route_layer (route_dlc_id, loco_dlc_id) VALUES (?,?)",
                     (route_id, route_id))
        rl_id = conn.execute("SELECT id FROM route_layer WHERE route_dlc_id=? AND loco_dlc_id=?",
                             (route_id, route_id)).fetchone()[0]

        for line in lines:
            line = strip_emoji_flags(line)
            if not line or line.lower() == 'n/a': continue
            for name in line.split(','):
                name = name.strip()
                if not name: continue
                conn.execute("INSERT OR IGNORE INTO train (name) VALUES (?)", (name,))
                tid = conn.execute("SELECT id FROM train WHERE name=?", (name,)).fetchone()[0]
                conn.execute("""INSERT OR IGNORE INTO route_layer_train
                    (route_layer_id, train_id, train_type, timetable_section)
                    VALUES (?,?,'scenic',NULL)""", (rl_id, tid))
                scenic_count += 1
    conn.commit()
    print(f"  {scenic_count} scenic train entries")

    # ══════════════════════════════════════════════════════════════════
    # SUMMARY + SANITY CHECKS
    # ══════════════════════════════════════════════════════════════════
    print("\n=== Database Summary ===")
    for t in ['country','dlc_type','platform','dlc','train','dlc_train',
              'dlc_requirement','route_layer','route_layer_train',
              'document_link','store_link']:
        print(f"  {t}: {conn.execute(f'SELECT COUNT(*) FROM {t}').fetchone()[0]}")

    # -- Bakerloo Line check --
    print("\n=== Bakerloo Line ===")
    bkl = conn.execute("SELECT id FROM dlc WHERE acronym='BKL'").fetchone()
    if bkl:
        bid = bkl[0]
        exps = conn.execute("""
            SELECT d.content_name FROM route_layer rl
            JOIN dlc d ON rl.loco_dlc_id = d.id
            WHERE rl.route_dlc_id=? AND rl.loco_dlc_id != ?""", (bid, bid)).fetchall()
        print(f"  Expansions: {[e[0] for e in exps]}")

        scenic = conn.execute("""
            SELECT t.name FROM route_layer_train rlt
            JOIN route_layer rl ON rlt.route_layer_id = rl.id
            JOIN train t ON rlt.train_id = t.id
            WHERE rl.route_dlc_id=? AND rlt.train_type='scenic'""", (bid,)).fetchall()
        print(f"  Scenic trains: {[s[0] for s in scenic]}")

        playable = conn.execute("""
            SELECT t.name, rlt.timetable_section, src.acronym
            FROM route_layer_train rlt
            JOIN route_layer rl ON rlt.route_layer_id = rl.id
            JOIN train t ON rlt.train_id = t.id
            JOIN dlc src ON rl.loco_dlc_id = src.id
            WHERE rl.route_dlc_id=? AND rlt.train_type='playable'
            ORDER BY rlt.timetable_section""", (bid,)).fetchall()
        print(f"  Playable locos ({len(playable)}):")
        for t in playable:
            print(f"    [{t[1]}] {t[0]} (from {t[2]})")

    # -- Avanti check --
    print("\n=== Avanti West Coast Class 390 ===")
    awc = conn.execute("SELECT id, content_name FROM dlc WHERE acronym='AWC 390'").fetchone()
    if awc:
        reqs = conn.execute("""
            SELECT d.content_name FROM dlc_requirement dr
            JOIN dlc d ON dr.required_dlc_id = d.id WHERE dr.dlc_id=?""", (awc[0],)).fetchall()
        print(f"  Requires: {[r[0] for r in reqs]}")
        trains = conn.execute("""
            SELECT t.name FROM dlc_train dt JOIN train t ON dt.train_id=t.id
            WHERE dt.dlc_id=?""", (awc[0],)).fetchall()
        print(f"  Base Locos: {[t[0] for t in trains]}")

    # -- WCML EMK check --
    print("\n=== WCML London Euston - Milton Keynes ===")
    emk = conn.execute("SELECT id FROM dlc WHERE acronym='EMK'").fetchone()
    if emk:
        eid = emk[0]
        exps = conn.execute("""
            SELECT d.content_name, d.acronym FROM route_layer rl
            JOIN dlc d ON rl.loco_dlc_id = d.id
            WHERE rl.route_dlc_id=? AND rl.loco_dlc_id != ?""", (eid, eid)).fetchall()
        print(f"  Expansions ({len(exps)}):")
        for e in exps: print(f"    - {e[0]} ({e[1]})")

        deps = conn.execute("""
            SELECT d.content_name, d.acronym FROM dlc_requirement dr
            JOIN dlc d ON dr.dlc_id = d.id WHERE dr.required_dlc_id=?""", (eid,)).fetchall()
        print(f"  DLCs requiring this route ({len(deps)}):")
        for d in deps: print(f"    - {d[0]} ({d[1]})")

    # -- Docs & Store check (East Coastway has all types) --
    print("\n=== East Coastway Docs & Store ===")
    ecw = conn.execute("SELECT id FROM dlc WHERE acronym='ECW'").fetchone()
    if ecw:
        eid = ecw[0]
        docs = conn.execute("SELECT doc_type, label, url FROM document_link WHERE dlc_id=?", (eid,)).fetchall()
        print(f"  Documents ({len(docs)}):")
        for d in docs: print(f"    [{d[0]}] {d[1]} -> {d[2][:60]}...")

        stores = conn.execute("SELECT console_platform, size_gb, store_url FROM store_link WHERE dlc_id=?", (eid,)).fetchall()
        print(f"  Store links ({len(stores)}):")
        for s in stores: print(f"    [{s[0]}] {s[1]} GB -> {s[2][:60]}...")

    # Flush WAL to main DB file so no stale -shm/-wal files linger
    conn.execute("PRAGMA wal_checkpoint(TRUNCATE)")
    conn.close()
    # Remove leftover WAL files
    for ext in ('-shm', '-wal'):
        path = DB_PATH + ext
        if os.path.exists(path):
            os.remove(path)
    print("\nDone!")


if __name__ == '__main__':
    main()
