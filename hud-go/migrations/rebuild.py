"""Rebuild tsw_hud.db from migration SQL files using Python."""
import sqlite3
import os
import glob
import sys

def main():
    db_path = sys.argv[1] if len(sys.argv) > 1 else os.path.join(os.path.dirname(__file__), "..", "tsw_hud.db")
    migrations_dir = os.path.dirname(os.path.abspath(__file__))

    if os.path.exists(db_path):
        os.remove(db_path)
        print(f"Removed existing {db_path}")

    conn = sqlite3.connect(db_path)

    sql_files = sorted(glob.glob(os.path.join(migrations_dir, "*.sql")))
    for sql_file in sql_files:
        name = os.path.basename(sql_file)
        print(f"Applying {name}...")
        with open(sql_file, "r", encoding="utf-8") as fh:
            conn.executescript(fh.read())

    conn.close()
    print(f"Done. Database rebuilt at {db_path}")

if __name__ == "__main__":
    main()
