#!/bin/bash
# Rebuild tsw_hud.db from migration files
# Usage: cd migrations && bash rebuild.sh

DB_FILE="${1:-../tsw_hud.db}"

rm -f "$DB_FILE"

for sql_file in $(ls *.sql | sort); do
    [ "$sql_file" = "rebuild.sh" ] && continue
    echo "Applying $sql_file..."
    sqlite3 "$DB_FILE" < "$sql_file"
done

echo "Done. Database rebuilt at $DB_FILE"
