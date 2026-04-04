"""One-time migration: rename 'route' column to 'service_name' in all CSV files
under screenshots/, and for services.csv files, prepend service_id back into
service_name where it was previously stripped out.

Usage:
    python bot/migrate_csv_columns.py          # dry-run (preview changes)
    python bot/migrate_csv_columns.py --apply  # apply changes
"""

import csv
import os
import re
import sys

SCREENSHOTS_DIR = os.path.join(os.path.dirname(os.path.abspath(__file__)), "screenshots")


def find_csv_files(root):
    """Find all original_services.csv and services.csv files."""
    results = []
    for dirpath, _, filenames in os.walk(root):
        for fn in filenames:
            if fn in ("original_services.csv", "services.csv"):
                results.append(os.path.join(dirpath, fn))
    return sorted(results)


def migrate_file(csv_path, dry_run=True):
    """Migrate a single CSV file. Returns (changed, message)."""
    with open(csv_path, "r", newline="", encoding="utf-8") as f:
        reader = csv.reader(f)
        rows = list(reader)

    if not rows:
        return False, "empty file"

    header = rows[0]

    # Check if already migrated
    if "service_name" in header and "route" not in header:
        return False, "already migrated"

    # Find the route column index
    if "route" not in header:
        return False, f"no 'route' column found (header: {header})"

    route_idx = header.index("route")
    sid_idx = header.index("service_id") if "service_id" in header else None

    changes = []

    # Rename header
    new_rows = [list(row) for row in rows]
    new_rows[0][route_idx] = "service_name"
    changes.append("renamed header 'route' -> 'service_name'")

    # Prepend service_id back into service_name where it was stripped
    if sid_idx is not None:
        prepend_count = 0
        for i in range(1, len(new_rows)):
            row = new_rows[i]
            if len(row) <= max(route_idx, sid_idx):
                continue
            sid = row[sid_idx].strip()
            name = row[route_idx].strip()
            if sid and not name.startswith(sid):
                row[route_idx] = f"{sid} {name}"
                prepend_count += 1
        if prepend_count > 0:
            changes.append(f"prepended service_id to {prepend_count} rows")

    if not changes:
        return False, "no changes needed"

    if not dry_run:
        with open(csv_path, "w", newline="", encoding="utf-8") as f:
            writer = csv.writer(f)
            writer.writerows(new_rows)

    return True, "; ".join(changes)


def main():
    dry_run = "--apply" not in sys.argv
    if dry_run:
        print("DRY RUN — pass --apply to write changes\n")
    else:
        print("APPLYING changes\n")

    csv_files = find_csv_files(SCREENSHOTS_DIR)
    print(f"Found {len(csv_files)} CSV files\n")

    changed = 0
    skipped = 0
    for path in csv_files:
        rel = os.path.relpath(path, SCREENSHOTS_DIR)
        was_changed, msg = migrate_file(path, dry_run=dry_run)
        if was_changed:
            print(f"  [CHANGE] {rel}: {msg}")
            changed += 1
        else:
            print(f"  [skip]   {rel}: {msg}")
            skipped += 1

    print(f"\nTotal: {changed} changed, {skipped} skipped")
    if dry_run and changed > 0:
        print("\nRe-run with --apply to write changes.")


if __name__ == "__main__":
    main()
