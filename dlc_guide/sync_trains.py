"""
Sync dlc_train relationships from company_based.csv into the database.
Uses CSV 'Base Locos' as the source of truth for train names and relationships.
Joins CSV rows to DB DLCs via acronym/short-name mapping.
"""
import csv
import sqlite3
import sys

DB_PATH = 'database.db'
CSV_PATH = 'company_based.csv'

# DB acronym -> CSV short name mapping (where they differ)
ACR_TO_CSV = {
    # UK
    'NJ72': 'NJ 1972 Stock', '38 TS': '1938 Stock', 'GLE': 'BKG', 'BML': 'LBN',
    'BCC RR': 'Centro 323', 'CL 170': 'Class 170', 'TFW 142': 'Class 142',
    'PBD': 'PDB', 'FSA3': 'Scotsman', 'ECML RTP': 'ECML Pack',
    'CL 377/171': 'Class 171', 'SR158': 'Class 158', 'XC 220': 'XC Class 220',
    'IOW22': 'Island Line', 'COL 1': 'CLP', 'COL 2': 'CLA', 'COL 3': 'CLI',
    'COL 4': 'CLM', 'COL 5': 'CLN', 'PFT': 'PFR', 'PNC': 'PSC', 'SEHS': 'SEH',
    'RHTT 66': 'RHTT', 'ROG 37': 'ROG Class 37', 'SOS': 'SoS', 'WCL SR': 'WCS',
    'T&F 80': 'Thomas80th', 'T&F': 'TWS', 'EEX': 'EDN Pack', 'HKA': 'WCML Pack',
    'LOD': 'Diesel Legends', 'HFP': 'BR Heavy Freight', 'BR 313': 'Class 313',
    'CL 380': 'Class 380', 'CL 86': 'Class 86', 'CL 90': 'Class 90',
    'CL 805': 'AWC 805', 'CL 700': 'Class 700', 'CL 31': 'Class 31',
    'CL 33': 'Class 33', 'CL 52': 'Class 52', 'CL20': 'Class 20',
    'CNK': 'CCN', 'NSH': 'NS Heritage', 'UPH': 'UP Heritage',
    # US
    'AVL': 'ALV', 'BP': 'BPE', 'LIRR2': 'LIC', 'M3': 'LIRR M3', 'MRT': 'MTN',
    'ALP 45DP NJT': 'ALP-45DP', 'MP 15': 'MP15DC', 'MP36  BB': 'MP36PH-3C',
    'SD40': 'NJ CSX SD40', 'SD 70ACe': 'BNSF SD70ACe', 'SFL': 'Santa Fe CJP',
    'CCR': 'CCB', 'CRR': 'CCR', 'C40-08': 'C40-8W', 'SBDGP': 'SBD Pack',
    'F7': 'Santa Fe F7',
    # DE
    'DB101': 'DB BR 101', 'DB 111': 'FTF Pack', 'DB 155': 'DB BR 155',
    'DB 182': 'Dispolok BR 182', 'DB 187': 'DB BR 187', 'DB 204': 'DB BR 204',
    'DB 218': 'BR 218', 'DB 363': 'DB BR 363', 'DB 403 TB': 'ICE3 Railbow',
    'DB 420': 'DB BR 420', 'EX101': 'Expert 101', 'EX145': 'Expert 145',
    'Ex101 GP': 'Expert 101 Gameplay Pack', 'DLG': 'BLD', 'BRS': 'SRM',
    'VEC': 'Vectron', 'FLIX': 'Flixtrain', 'HHL': 'HML', 'NJ 423': 'NJ BR 423',
    'BR 294 DB': 'DB BR 294', 'G6': 'DB G6', 'SHN': 'SHB',
    # AT/CH
    'SEMM': 'SBN', 'VOR': 'VBRG', 'ARL P2': 'ARL Pack', 'BERN': 'BLE',
    'RhB AC': 'RhB Pack',
    # NL/CZ
    'ZWG': 'ZGN', 'SPT': 'LBSP',
}

def parse_csv():
    """Parse CSV and return dict keyed by short name."""
    entries = {}
    with open(CSV_PATH, 'r', encoding='utf-8') as f:
        reader = csv.DictReader(f)
        for row in reader:
            short = (row.get('Short Name') or '').strip()
            if not short:
                continue
            base_locos_raw = (row.get('Base Locos') or '').strip()
            trains = []
            if base_locos_raw and base_locos_raw not in ('N/A', '-', 'N/A '):
                trains = [t.strip() for t in base_locos_raw.split('\n') if t.strip()]
            entries[short] = {
                'name': row['Add-On Name'].strip(),
                'type': (row.get('Type') or '').strip(),
                'base_locos': trains,
            }
    return entries


def get_or_create_train(db, name):
    """Get train ID by name, or create it. Returns train ID."""
    row = db.execute('SELECT id FROM train WHERE name = ?', (name,)).fetchone()
    if row:
        return row[0]
    cur = db.execute('INSERT INTO train (name) VALUES (?)', (name,))
    return cur.lastrowid


def sync(dry_run=False):
    csv_data = parse_csv()
    db = sqlite3.connect(DB_PATH)
    db.execute('PRAGMA foreign_keys = ON')

    dlcs = db.execute('SELECT id, content_name, acronym FROM dlc ORDER BY id').fetchall()

    stats = {'updated': 0, 'skipped_no_csv': 0, 'skipped_no_trains': 0, 'unchanged': 0,
             'trains_added': 0, 'trains_removed': 0, 'trains_created': 0}

    for dlc_id, content_name, acronym in dlcs:
        acronym = acronym or ''
        csv_short = ACR_TO_CSV.get(acronym, acronym)
        csv_entry = csv_data.get(csv_short)

        if not csv_entry:
            stats['skipped_no_csv'] += 1
            continue

        csv_trains = csv_entry['base_locos']
        if not csv_trains:
            # CSV says no base locos - clear any existing
            existing = db.execute(
                'SELECT t.name FROM dlc_train dt JOIN train t ON t.id = dt.train_id WHERE dt.dlc_id = ?',
                (dlc_id,)
            ).fetchall()
            if existing:
                if not dry_run:
                    db.execute('DELETE FROM dlc_train WHERE dlc_id = ?', (dlc_id,))
                stats['trains_removed'] += len(existing)
                stats['updated'] += 1
                print(f"  DB {dlc_id:>3} [{acronym}] -> [{csv_short}]: CLEARED {len(existing)} trains")
            else:
                stats['skipped_no_trains'] += 1
            continue

        # Get current DB trains for this DLC
        current = db.execute(
            'SELECT t.name FROM dlc_train dt JOIN train t ON t.id = dt.train_id WHERE dt.dlc_id = ?',
            (dlc_id,)
        ).fetchall()
        current_names = sorted([r[0] for r in current])

        if current_names == sorted(csv_trains):
            stats['unchanged'] += 1
            continue

        # Replace dlc_train entries with CSV data
        print(f"  DB {dlc_id:>3} [{acronym}] -> [{csv_short}] {content_name[:45]}")
        print(f"         was:  {current_names}")
        print(f"         now:  {sorted(csv_trains)}")

        if not dry_run:
            db.execute('DELETE FROM dlc_train WHERE dlc_id = ?', (dlc_id,))
            stats['trains_removed'] += len(current_names)

            for train_name in csv_trains:
                train_id = get_or_create_train(db, train_name)
                db.execute(
                    'INSERT OR IGNORE INTO dlc_train (dlc_id, train_id) VALUES (?, ?)',
                    (dlc_id, train_id)
                )
                stats['trains_added'] += 1

        stats['updated'] += 1

    if not dry_run:
        # Clean up orphaned trains (not referenced by any dlc_train)
        orphans = db.execute(
            'SELECT id, name FROM train WHERE id NOT IN (SELECT DISTINCT train_id FROM dlc_train)'
        ).fetchall()
        if orphans:
            print(f"\n  Removing {len(orphans)} orphaned trains:")
            for tid, tname in orphans:
                print(f"    - {tname}")
            db.execute('DELETE FROM train WHERE id NOT IN (SELECT DISTINCT train_id FROM dlc_train)')

        db.commit()

    db.close()

    print(f"\n{'DRY RUN ' if dry_run else ''}Summary:")
    print(f"  Updated:        {stats['updated']}")
    print(f"  Unchanged:      {stats['unchanged']}")
    print(f"  No CSV match:   {stats['skipped_no_csv']}")
    print(f"  Trains added:   {stats['trains_added']}")
    print(f"  Trains removed: {stats['trains_removed']}")


if __name__ == '__main__':
    dry = '--dry-run' in sys.argv
    if dry:
        print("=== DRY RUN (no changes will be made) ===\n")
    else:
        print("=== SYNCING dlc_train from company_based.csv ===\n")
    sync(dry_run=dry)
