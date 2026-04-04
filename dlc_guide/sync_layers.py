"""
Sync layer relationships from company_based.csv into route_layer / route_layer_train.
Parses "Additional Playable Locos" column to build proper relational layer data.
"""
import csv
import re
import sqlite3
import sys

DB_PATH = 'database.db'
CSV_PATH = 'company_based.csv'

# DB acronym -> CSV short name (from sync_trains.py)
ACR_TO_CSV = {
    'NJ72':'NJ 1972 Stock','38 TS':'1938 Stock','GLE':'BKG','BML':'LBN',
    'BCC RR':'Centro 323','CL 170':'Class 170','TFW 142':'Class 142',
    'PBD':'PDB','FSA3':'Scotsman','ECML RTP':'ECML Pack',
    'CL 377/171':'Class 171','SR158':'Class 158','XC 220':'XC Class 220',
    'IOW22':'Island Line','COL 1':'CLP','COL 2':'CLA','COL 3':'CLI',
    'COL 4':'CLM','COL 5':'CLN','PFT':'PFR','PNC':'PSC','SEHS':'SEH',
    'RHTT 66':'RHTT','ROG 37':'ROG Class 37','SOS':'SoS','WCL SR':'WCS',
    'T&F 80':'Thomas80th','T&F':'TWS','EEX':'EDN Pack','HKA':'WCML Pack',
    'LOD':'Diesel Legends','HFP':'BR Heavy Freight','BR 313':'Class 313',
    'CL 380':'Class 380','CL 86':'Class 86','CL 90':'Class 90',
    'CL 805':'AWC 805','CL 700':'Class 700','CL 31':'Class 31',
    'CL 33':'Class 33','CL 52':'Class 52','CL20':'Class 20',
    'CNK':'CCN','NSH':'NS Heritage','UPH':'UP Heritage',
    'AVL':'ALV','BP':'BPE','LIRR2':'LIC','M3':'LIRR M3','MRT':'MTN',
    'ALP 45DP NJT':'ALP-45DP','MP 15':'MP15DC','MP36  BB':'MP36PH-3C',
    'SD40':'NJ CSX SD40','SD 70ACe':'BNSF SD70ACe','SFL':'Santa Fe CJP',
    'CCR':'CCB','CRR':'CCR','C40-08':'C40-8W','SBDGP':'SBD Pack',
    'F7':'Santa Fe F7',
    'DB101':'DB BR 101','DB 111':'FTF Pack','DB 155':'DB BR 155',
    'DB 182':'Dispolok BR 182','DB 187':'DB BR 187','DB 204':'DB BR 204',
    'DB 218':'BR 218','DB 363':'DB BR 363','DB 403 TB':'ICE3 Railbow',
    'DB 420':'DB BR 420','EX101':'Expert 101','EX145':'Expert 145',
    'Ex101 GP':'Expert 101 Gameplay Pack','DLG':'BLD','BRS':'SRM',
    'VEC':'Vectron','FLIX':'Flixtrain','HHL':'HML','NJ 423':'NJ BR 423',
    'BR 294 DB':'DB BR 294','G6':'DB G6','SHN':'SHB',
    'SEMM':'SBN','VOR':'VBRG','ARL P2':'ARL Pack','BERN':'BLE',
    'RhB AC':'RhB Pack','ZWG':'ZGN','SPT':'LBSP',
}
CSV_TO_ACR = {v: k for k, v in ACR_TO_CSV.items()}


def build_lookups(db):
    """Build mappings from CSV short names and DB acronyms to DLC IDs."""
    all_dlcs = db.execute('SELECT id, content_name, acronym FROM dlc').fetchall()

    db_acr_to_id = {}
    csv_short_to_id = {}

    for dlc in all_dlcs:
        acr = dlc[2] or ''
        if acr:
            db_acr_to_id[acr] = dlc[0]
            csv_short = ACR_TO_CSV.get(acr, acr)
            csv_short_to_id[csv_short] = dlc[0]

    # Also map DB acronyms directly for csv_short lookup
    for acr, did in db_acr_to_id.items():
        if acr not in csv_short_to_id:
            csv_short_to_id[acr] = did

    return db_acr_to_id, csv_short_to_id


LAYER_ACR_FIXES = {
    'NNL': 'NLL',       # Typo in CSV: NNL -> NLL (London Overground Mildmay)
    'BRo': 'BRO',       # Case typo
    'Heritage Collection': 'UPH',  # UP Heritage Collection
    'MSB< KWG': None,   # Malformed entry, skip
    'CC': None,          # Generic "Cane Creek" abbreviation, ambiguous
    'MHL': None,         # Unknown
    'MSN': None,         # Unknown
    'SBH': None,         # Unknown
    'LIRR Commuter': 'LIRR2',  # LIRR Commuter route
    'LIRR_M3': 'M3',    # LIRR M3 loco
    'Run by LNER Azuma': None,  # Not a DLC reference
    'Flying Scotsman': 'FSA3',  # Flying Scotsman loco
    'FlyingScotsman': 'FSA3',
    'Amtrak Acela': 'NEC',
    'Dresden Riesa': 'DRA',
    'LIRR M3': 'M3',
    'CSX C40-8W': 'C40-08',
    'BR101 Expert': 'EX101',
    'CJP F7': 'F7',     # Santa Fe F7 on Cajon Pass
    'NTP Mk2s': None,   # Coach reference, not DLC
    'HST/158': None,     # Mixed train reference
    '158/700': None,
    '801': None,         # Not a DLC acronym
}

# Skip patterns: livery counts, date ranges, headcodes, generic terms
SKIP_PATTERNS = re.compile(
    r'^\d+ Liveries?$|^x\d+$|^(Jan|Feb|Mar|Apr|May|Jun|Jul|Aug|Sep|Oct|Nov|Dec)'
    r'|^\d[A-Z]\d{2}$|^(AI|FKA|HKA|TEA|BBA|PCA|JNA|FCA|PGA|JNA/MFA|Intermodal|HAR|SJF|SHM|BBC)$'
)


def resolve_dlc_id(acr_text, csv_short_to_id, db_acr_to_id):
    """Try to resolve a parenthetical acronym to a DLC ID."""
    acr = acr_text.strip()
    if not acr or acr in ('None', 'N/A', 'AI'):
        return None

    # Apply fixes
    if acr in LAYER_ACR_FIXES:
        fixed = LAYER_ACR_FIXES[acr]
        if fixed is None:
            return None
        acr = fixed

    # Skip known non-DLC patterns
    if SKIP_PATTERNS.match(acr):
        return None

    # Direct CSV short name lookup
    if acr in csv_short_to_id:
        return csv_short_to_id[acr]

    # Direct DB acronym lookup
    if acr in db_acr_to_id:
        return db_acr_to_id[acr]

    # Try reverse mapping: if acr is a CSV short, get DB acronym
    db_acr = CSV_TO_ACR.get(acr)
    if db_acr and db_acr in db_acr_to_id:
        return db_acr_to_id[db_acr]

    return None


# Manual train name fixes: layer display name -> DB train name
TRAIN_NAME_FIXES = {
    'Class 390 AWC': 'BR Class 390 Pendolino',
    'Voyager Class 220': 'XC Class 220',
    'Voyager Class 220 XC': 'XC Class 220',
    'HST Class 43': 'High Speed Train GWG',
    'HST': 'High Speed Train GWG',
    'Class 375 SEW': 'Class 375/9',
    'Class 375/9 SEB': 'Class 375/9',
    'Class 700/0 TL': 'BR Class 700/0',
    'Class 313/2 SN': 'BR Class 313/2',
    'Class 158 EMT': 'Class 158/0 EMT',
    'Class 158 ScR': 'BR Class 158 Sprinter',
    'Class 170 ScR': 'Class 170',
    'BR 642 DB': 'BR642',
    'BR 642': 'BR642',
    'BR  642': 'BR642',
    'DB BR 642': 'BR642',
    'DB BR 612': 'BR 612',
    'DB BR 628.2 VR': 'BR  628.2',
    'BR 628': 'BR  628.2',
    'BR 628.2': 'BR  628.2',
    'Acela': 'Acela Express Amtrak',
    'Acela Express': 'Acela Express Amtrak',
    'Acela Express Amtrak': 'Acela Express Amtrak',
    'BR 363 DBB': 'BR 363',
    'BR185.2': 'BR 185.2',
    'BR187': 'DB BR 187',
    'BR193 Vectron': 'BR 193 Vectron RP',
    'BR 403 RBW': 'DB BR 403 ICE 3 Railbow',
    'DB ICE 1': 'DB BR 401 ICE 1',
    'DB ICE1': 'DB BR 401 ICE 1',
    'ICE3M': 'BR 403 ICE 3M',
    'Bpmmbdzf 286.1 Expert': 'Bpmmbdzf 286.1 Cab Car',
    'Bnrdzf 463.0 Diesel': 'DB Bnrdzf 463.0',
    'BR 145 Expert': 'Expert DB BR 145',
    'Class 350-2': 'Class 350',
    'Class 350-3': 'Class 350',
    'F59PHR ML': 'Metrolink F59PHR',
    'GP40-2': 'CSX GP40-2',
    'AC440CW': 'AC4400CW UP',
    'SW1000R': 'SW1000R',
    'Multi-Level Commuter Cab': 'Multilevel Cab Car',
    'Ex-Metroliner Cab Car': 'Amfleet Cab Car',
    'MP36-3C': 'MP36PH-3C',
    'SD40 CSX-S': 'CSX SD40 (2 Liveries)',
    'SD40 CSX-s': 'CSX SD40 (2 Liveries)',
    'OBB 116': 'OBB 1116',
    'Class 37/5 RF': 'Class 37/5',
    'Class 37/7 EPX': 'BR Class 37 EPX',
    'Class 20 RF': 'BR Class 20',
    'Class 08 BLU': 'BR Class 08',
    'Class 101 BLG': 'BR Class 101',
    'Class 31/1 BLU': 'BR Class 31',
    'Class 31/1  BLU': 'BR Class 31',
    'Class 33 GRN': 'BR Class 33',
    'Class 40 BLU': 'BR Class 40',
    'Class 45/1 BLU': 'Class 45/1',
    'Class 47 GRN': 'Class 47/4',
    'Class 47/4 BLU': 'Class 47/4',
    'Class 52 BLU': 'BR Class 52',
    'Class 52 BLUE': 'BR Class 52',
    'Class 52 MAR': 'BR Class 52',
    'LMS Jubilee Black': 'LMS Jubilee',
    'LMS Jubilee Black Festive': 'LMS Jubilee',
    'LMS Jubiless': 'LMS Jubilee',
}


def resolve_train_id(train_name, db):
    """Try to find train ID by name. Returns (train_id, matched_name) or (None, None)."""
    name = train_name.strip()
    if not name:
        return None, None

    # Apply name fixes first
    if name in TRAIN_NAME_FIXES:
        name = TRAIN_NAME_FIXES[name]

    # Exact match
    row = db.execute('SELECT id, name FROM train WHERE name = ?', (name,)).fetchone()
    if row:
        return row[0], row[1]

    # Case-insensitive match
    row = db.execute('SELECT id, name FROM train WHERE LOWER(name) = LOWER(?)', (name,)).fetchone()
    if row:
        return row[0], row[1]

    # Strip livery suffixes (BLU, GRN, etc.) and try again
    stripped = re.sub(r'\s+(BLU|GRN|MAR|RED|BLG|RF|SN|TL|SEW|SEB|ScR|EMT|DB|AWC|TFL|NT|WMR|XC)\s*$', '', name)
    if stripped != name:
        row = db.execute("SELECT id, name FROM train WHERE name LIKE ? ORDER BY LENGTH(name) LIMIT 1",
                         (f'%{stripped}%',)).fetchone()
        if row:
            return row[0], row[1]

    # Partial match: DB train name contains our search
    row = db.execute("SELECT id, name FROM train WHERE name LIKE ? ORDER BY LENGTH(name) LIMIT 1",
                     (f'%{name}%',)).fetchone()
    if row:
        return row[0], row[1]

    # Reverse: our search contains DB train name
    row = db.execute("SELECT id, name FROM train WHERE ? LIKE '%' || name || '%' ORDER BY LENGTH(name) DESC LIMIT 1",
                     (name,)).fetchone()
    if row:
        return row[0], row[1]

    return None, None


def get_or_create_train(name, db):
    """Get train ID by name, or create it."""
    tid, _ = resolve_train_id(name, db)
    if tid:
        return tid
    cur = db.execute('INSERT INTO train (name) VALUES (?)', (name,))
    return cur.lastrowid


def parse_layer_column(text):
    """Parse an Additional Playable Locos or Layer Requirements column.
    Returns list of {train_display, source_acronyms, timetable, conductor_mode}
    """
    if not text or text.strip() in ('N/A', '-', ''):
        return []

    entries = []
    current_timetable = None

    for line in text.split('\n'):
        line = line.strip()
        if not line:
            continue

        # Timetable header: [Timetable Name] — also handle malformed like "Weekday Timetable]"
        m = re.match(r'^\[?(.+?)\]\s*$', line)
        if m and ('Timetable' in line or 'timetable' in line or line.startswith('[')):
            current_timetable = m.group(1).strip()
            continue

        # Fix malformed lines: trailing commas, missing opening parens
        line = re.sub(r',\s*$', '', line)  # trailing comma
        # Fix missing opening paren: "BR 185.2 MSB, RSN)" -> "BR 185.2 (MSB, RSN)"
        if ')' in line and '(' not in line:
            line = re.sub(r'\s+([A-Z][A-Za-z0-9, <>]+)\)', r' (\1)', line)

        # Detect conductor mode emoji
        conductor = '🎟' in line
        # Clean emojis for parsing
        clean = re.sub(r'[🎟️⚙🔊]+', '', line).strip()

        # Parse: "Train Name (ACR1, ACR2)"
        m = re.match(r'^(.+?)\s*\(([^)]+)\)\s*$', clean)
        if m:
            train_display = m.group(1).strip()
            acrs_raw = m.group(2)
            acronyms = [a.strip() for a in acrs_raw.split(',') if a.strip()]
            entries.append({
                'train_display': train_display,
                'source_acronyms': acronyms,
                'timetable': current_timetable,
                'conductor_mode': conductor,
            })
        else:
            # No parenthetical - train from the route itself or standalone
            train_display = clean.strip()
            if train_display:
                entries.append({
                    'train_display': train_display,
                    'source_acronyms': [],
                    'timetable': current_timetable,
                    'conductor_mode': conductor,
                })

    return entries


def sync(dry_run=False):
    db = sqlite3.connect(DB_PATH)
    db.execute('PRAGMA foreign_keys = ON')
    db.row_factory = sqlite3.Row

    db_acr_to_id, csv_short_to_id = build_lookups(db)

    # Parse CSV
    csv_data = {}
    with open(CSV_PATH, 'r', encoding='utf-8') as f:
        reader = csv.DictReader(f)
        for row in reader:
            short = (row.get('Short Name') or '').strip()
            if not short:
                continue
            csv_data[short] = {
                'name': row['Add-On Name'].strip(),
                'type': (row.get('Type') or '').strip(),
                'addl_playable': (row.get('Additional Playable Locos') or '').strip(),
            }

    stats = {
        'dlcs_processed': 0, 'route_layers_created': 0,
        'trains_linked': 0, 'trains_unresolved': 0,
        'dlcs_unresolved': 0, 'skipped_no_csv': 0,
    }
    unresolved_trains = set()
    unresolved_dlcs = set()

    # Get all DB DLCs
    all_db_dlcs = db.execute('SELECT id, content_name, acronym FROM dlc ORDER BY id').fetchall()

    if not dry_run:
        # Clear existing route_layer data (we'll rebuild from CSV)
        db.execute('DELETE FROM route_layer_train')
        db.execute('DELETE FROM route_layer')

    for dlc in all_db_dlcs:
        dlc_id = dlc['id']
        acr = dlc['acronym'] or ''
        csv_short = ACR_TO_CSV.get(acr, acr)
        csv_entry = csv_data.get(csv_short)

        if not csv_entry:
            stats['skipped_no_csv'] += 1
            continue

        addl_text = csv_entry['addl_playable']
        if not addl_text or addl_text in ('N/A', '-'):
            continue

        entries = parse_layer_column(addl_text)
        if not entries:
            continue

        stats['dlcs_processed'] += 1

        # Group entries by source DLC
        # Each entry can have multiple source acronyms
        for entry in entries:
            source_acrs = entry['source_acronyms']
            if not source_acrs:
                # Self-referencing: train from this route's own base locos
                source_acrs = [csv_short]

            for src_acr in source_acrs:
                src_dlc_id = resolve_dlc_id(src_acr, csv_short_to_id, db_acr_to_id)
                if not src_dlc_id:
                    if src_acr not in ('None', 'N/A', 'AI'):
                        unresolved_dlcs.add(src_acr)
                        stats['dlcs_unresolved'] += 1
                    continue

                # Resolve train (create if missing on real run)
                if dry_run:
                    train_id, _ = resolve_train_id(entry['train_display'], db)
                    if not train_id:
                        unresolved_trains.add(entry['train_display'])
                        stats['trains_unresolved'] += 1
                        continue
                else:
                    train_id = get_or_create_train(entry['train_display'], db)

                if not dry_run:
                    # Get or create route_layer
                    db.execute(
                        'INSERT OR IGNORE INTO route_layer (route_dlc_id, loco_dlc_id) VALUES (?, ?)',
                        (dlc_id, src_dlc_id)
                    )
                    rl = db.execute(
                        'SELECT id FROM route_layer WHERE route_dlc_id = ? AND loco_dlc_id = ?',
                        (dlc_id, src_dlc_id)
                    ).fetchone()

                    db.execute(
                        '''INSERT OR IGNORE INTO route_layer_train
                           (route_layer_id, train_id, train_type, timetable_section, conductor_mode)
                           VALUES (?, ?, 'playable', ?, ?)''',
                        (rl['id'], train_id, entry['timetable'], 1 if entry['conductor_mode'] else 0)
                    )

                stats['trains_linked'] += 1
                stats['route_layers_created'] += 1

    if not dry_run:
        db.commit()

    db.close()

    print(f"\n{'DRY RUN ' if dry_run else ''}Summary:")
    print(f"  DLCs processed:      {stats['dlcs_processed']}")
    print(f"  Route layers/trains: {stats['route_layers_created']}")
    print(f"  Trains linked:       {stats['trains_linked']}")
    print(f"  Trains unresolved:   {stats['trains_unresolved']}")
    print(f"  DLC acrs unresolved: {stats['dlcs_unresolved']}")
    print(f"  Skipped (no CSV):    {stats['skipped_no_csv']}")

    if unresolved_dlcs:
        print(f"\n  Unresolved DLC acronyms ({len(unresolved_dlcs)}):")
        for a in sorted(unresolved_dlcs):
            print(f"    - {a}")

    if unresolved_trains:
        print(f"\n  Unresolved train names ({len(unresolved_trains)}):")
        for t in sorted(unresolved_trains):
            print(f"    - {t}")


if __name__ == '__main__':
    dry = '--dry-run' in sys.argv
    if dry:
        print("=== DRY RUN ===\n")
    else:
        print("=== SYNCING layers from company_based.csv ===\n")
    sync(dry_run=dry)
