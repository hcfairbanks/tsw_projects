"""
Generate SQL migration files from tsw_hud.db.

Produces:
  migrations/001_schema.sql          — all CREATE TABLE statements
  migrations/002_data_<table>.sql    — INSERT statements per table, split into
                                       multiple files if a table exceeds MAX_ROWS_PER_FILE

Each file stays well under GitHub's 100 MB limit (targets ~50 MB max).
Run: python generate_migrations.py
Rebuild: sqlite3 tsw_hud.db < migrations/001_schema.sql
         sqlite3 tsw_hud.db < migrations/002_data_countries.sql
         ...etc in order
"""

import sqlite3
import os
import re

DB_PATH = os.path.join(os.path.dirname(os.path.abspath(__file__)), "tsw_hud.db")
MIGRATIONS_DIR = os.path.join(os.path.dirname(os.path.abspath(__file__)), "migrations")
MAX_ROWS_PER_FILE = 20_000  # keeps files small; large-text tables stay under ~50 MB

# Per-table overrides for tables with large text columns
TABLE_ROW_LIMITS = {
    "timetable_coordinates": 3_000,  # ~9KB avg per row, 3K rows ≈ ~27 MB per file
    "route_coordinates": 5_000,
}

# Order tables so foreign key dependencies are satisfied on insert.
# Tables with no FK deps come first, then dependents.
TABLE_ORDER = [
    "countries",
    "train_classes",
    "trains",
    "timetable_actions",
    "weather_presets",
    "routes",
    "locations",
    "sections",
    "route_train_classes",
    "route_trains",
    "route_coordinates",
    "route_markers",
    "route_locations",
    "station_name_mappings",
    "timetables",
    "timetable_trains",
    "timetable_entries",
    "timetable_coordinates",
    "timetable_markers",
    "timetable_sections",
    "section_trains",
]


def escape_value(val):
    """Escape a Python value for SQL."""
    if val is None:
        return "NULL"
    if isinstance(val, (int, float)):
        return str(val)
    # String — escape single quotes
    s = str(val).replace("'", "''")
    return f"'{s}'"


def main():
    os.makedirs(MIGRATIONS_DIR, exist_ok=True)

    conn = sqlite3.connect(DB_PATH)
    conn.row_factory = sqlite3.Row
    cursor = conn.cursor()

    # ── 001: Schema ──────────────────────────────────────────────────────
    schema_path = os.path.join(MIGRATIONS_DIR, "001_schema.sql")
    with open(schema_path, "w", encoding="utf-8") as f:
        f.write("-- Migration 001: Schema\n")
        f.write("-- Auto-generated from tsw_hud.db\n\n")
        f.write("PRAGMA foreign_keys = OFF;\n\n")

        # Get all CREATE statements in dependency order
        for table in TABLE_ORDER:
            cursor.execute(
                "SELECT sql FROM sqlite_master WHERE type='table' AND name=?",
                (table,),
            )
            row = cursor.fetchone()
            if row and row[0]:
                f.write(f"{row[0]};\n\n")

        # Also include any indexes
        cursor.execute(
            "SELECT sql FROM sqlite_master WHERE type='index' AND sql IS NOT NULL"
        )
        for row in cursor.fetchall():
            f.write(f"{row[0]};\n\n")

        f.write("PRAGMA foreign_keys = ON;\n")

    print(f"  Written: {schema_path}")

    # ── 002+: Data ───────────────────────────────────────────────────────
    file_num = 2

    for table in TABLE_ORDER:
        cursor.execute(f"SELECT COUNT(*) FROM [{table}]")
        total_rows = cursor.fetchone()[0]
        if total_rows == 0:
            continue

        # Get column names
        cursor.execute(f"PRAGMA table_info([{table}])")
        columns = [col[1] for col in cursor.fetchall()]
        col_list = ", ".join(columns)

        row_limit = TABLE_ROW_LIMITS.get(table, MAX_ROWS_PER_FILE)

        offset = 0
        part = 1
        total_parts = (total_rows + row_limit - 1) // row_limit

        while offset < total_rows:
            if total_parts == 1:
                filename = f"{file_num:03d}_data_{table}.sql"
            else:
                filename = f"{file_num:03d}_data_{table}_part{part}.sql"

            filepath = os.path.join(MIGRATIONS_DIR, filename)

            with open(filepath, "w", encoding="utf-8") as f:
                f.write(f"-- Migration {file_num:03d}: Data for {table}")
                if total_parts > 1:
                    f.write(f" (part {part}/{total_parts})")
                f.write(f"\n-- Rows {offset + 1} to {min(offset + row_limit, total_rows)} of {total_rows}\n\n")
                f.write("PRAGMA foreign_keys = OFF;\n\n")
                f.write("BEGIN TRANSACTION;\n\n")

                cursor.execute(
                    f"SELECT * FROM [{table}] ORDER BY rowid LIMIT ? OFFSET ?",
                    (row_limit, offset),
                )

                batch = []
                for row in cursor:
                    values = ", ".join(escape_value(row[col]) for col in columns)
                    batch.append(f"INSERT INTO [{table}] ({col_list}) VALUES ({values});")

                    # Write in chunks of 1000 to avoid huge memory use
                    if len(batch) >= 1000:
                        f.write("\n".join(batch))
                        f.write("\n")
                        batch = []

                if batch:
                    f.write("\n".join(batch))
                    f.write("\n")

                f.write("\nCOMMIT;\n")
                f.write("\nPRAGMA foreign_keys = ON;\n")

            size_mb = os.path.getsize(filepath) / (1024 * 1024)
            print(f"  Written: {filename} ({size_mb:.1f} MB)")

            offset += row_limit
            part += 1
            file_num += 1

    conn.close()

    # ── Generate rebuild script ──────────────────────────────────────────
    rebuild_path = os.path.join(MIGRATIONS_DIR, "rebuild.sh")
    with open(rebuild_path, "w", encoding="utf-8") as f:
        f.write("#!/bin/bash\n")
        f.write("# Rebuild tsw_hud.db from migration files\n")
        f.write('# Usage: cd migrations && bash rebuild.sh\n\n')
        f.write('DB_FILE="${1:-../tsw_hud.db}"\n\n')
        f.write('rm -f "$DB_FILE"\n\n')
        f.write('for sql_file in $(ls *.sql | sort); do\n')
        f.write('    [ "$sql_file" = "rebuild.sh" ] && continue\n')
        f.write('    echo "Applying $sql_file..."\n')
        f.write('    sqlite3 "$DB_FILE" < "$sql_file"\n')
        f.write('done\n\n')
        f.write('echo "Done. Database rebuilt at $DB_FILE"\n')

    # Also a Python rebuild script (since sqlite3 CLI may not be available)
    rebuild_py_path = os.path.join(MIGRATIONS_DIR, "rebuild.py")
    with open(rebuild_py_path, "w", encoding="utf-8") as f:
        f.write('"""Rebuild tsw_hud.db from migration SQL files using Python."""\n')
        f.write("import sqlite3\nimport os\nimport glob\nimport sys\n\n")
        f.write('def main():\n')
        f.write('    db_path = sys.argv[1] if len(sys.argv) > 1 else os.path.join(os.path.dirname(__file__), "..", "tsw_hud.db")\n')
        f.write('    migrations_dir = os.path.dirname(os.path.abspath(__file__))\n\n')
        f.write('    if os.path.exists(db_path):\n')
        f.write('        os.remove(db_path)\n')
        f.write('        print(f"Removed existing {db_path}")\n\n')
        f.write('    conn = sqlite3.connect(db_path)\n\n')
        f.write('    sql_files = sorted(glob.glob(os.path.join(migrations_dir, "*.sql")))\n')
        f.write('    for sql_file in sql_files:\n')
        f.write('        name = os.path.basename(sql_file)\n')
        f.write('        print(f"Applying {name}...")\n')
        f.write('        with open(sql_file, "r", encoding="utf-8") as fh:\n')
        f.write('            conn.executescript(fh.read())\n\n')
        f.write('    conn.close()\n')
        f.write('    print(f"Done. Database rebuilt at {db_path}")\n\n')
        f.write('if __name__ == "__main__":\n')
        f.write('    main()\n')

    print(f"\n  Rebuild scripts: {rebuild_path}")
    print(f"  Rebuild scripts: {rebuild_py_path}")
    print("\nDone! Add migrations/ to git and add tsw_hud.db to .gitignore.")


if __name__ == "__main__":
    main()
