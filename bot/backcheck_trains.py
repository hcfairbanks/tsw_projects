"""
Back-check script: scan the screenshots folder tree and ensure every captured
service has its train and section correctly linked to the timetable in the DB.

Folder structure:
    screenshots/{route}/{section}/{train_class}/train_{num}/service_{num}/llm_data.json

For each llm_data.json found:
  1. Extract the service name (level_info.service_name + start_time + duration)
  2. Look up the timetable in the DB by service name + route
  3. Ensure the train (from folder path) is linked to the timetable
  4. Ensure the section (from folder path) is linked to the timetable

Usage:
    python bot/backcheck_trains.py              # dry run (shows what would change)
    python bot/backcheck_trains.py --apply      # actually make changes

Skips:
    - London Underground Bakerloo Line
"""

import json
import os
import sys
import urllib.request
import urllib.error
import urllib.parse

# ---------------------------------------------------------------------------
# Config
# ---------------------------------------------------------------------------
SCREENSHOTS_DIR = os.path.join(os.path.dirname(__file__), "screenshots")
SERVER_URL = "http://localhost:3000"

# Routes to skip entirely
SKIP_ROUTES = {"London Underground Bakerloo Line"}

DRY_RUN = "--apply" not in sys.argv


# ---------------------------------------------------------------------------
# API helpers
# ---------------------------------------------------------------------------
def api_get(path):
    url = f"{SERVER_URL}{path}"
    req = urllib.request.Request(url, method="GET")
    with urllib.request.urlopen(req, timeout=30) as resp:
        return json.loads(resp.read().decode())


def api_post_json(path, body):
    url = f"{SERVER_URL}{path}"
    data = json.dumps(body).encode()
    req = urllib.request.Request(
        url, data=data, method="POST",
        headers={"Content-Type": "application/json"},
    )
    try:
        with urllib.request.urlopen(req, timeout=30) as resp:
            return resp.status, json.loads(resp.read().decode())
    except urllib.error.HTTPError as e:
        return e.code, json.loads(e.read().decode())


# ---------------------------------------------------------------------------
# Build service name the same way the uploader does
# ---------------------------------------------------------------------------
def build_service_name(level_info):
    """
    Reconstruct the service_name as the uploader would:
    "Service Name HH:MM HH:MM"
    """
    raw = level_info.get("service_name", "Unknown Service")
    start_time = level_info.get("start_time", "")
    duration = level_info.get("duration", "")

    parts = [raw]
    if start_time:
        st = start_time.strip()
        # Convert "HH:MM:SS" to "HH:MM" if needed
        if len(st.split(":")) == 3:
            st = ":".join(st.split(":")[:2])
        parts.append(st)
    if duration:
        dur = duration.strip()
        if len(dur.split(":")) == 3:
            dur = ":".join(dur.split(":")[:2])
        parts.append(dur)

    return " ".join(parts)


# ---------------------------------------------------------------------------
# Scan folder tree for all services
# ---------------------------------------------------------------------------
def _has_train_folders(directory):
    """Check if a directory contains train_XX folders directly."""
    for name in os.listdir(directory):
        if name.startswith("train_") and os.path.isdir(os.path.join(directory, name)):
            return True
    return False


def _scan_train_class(route_name, section_name, train_class, train_class_dir):
    """Yield service entries for all trains/services under a train class folder."""
    for train_folder in sorted(os.listdir(train_class_dir)):
        train_dir = os.path.join(train_class_dir, train_folder)
        if not os.path.isdir(train_dir) or not train_folder.startswith("train_"):
            continue

        for service_folder in sorted(os.listdir(train_dir)):
            service_dir = os.path.join(train_dir, service_folder)
            llm_path = os.path.join(service_dir, "llm_data.json")
            if not os.path.isfile(llm_path):
                continue

            try:
                with open(llm_path, "r", encoding="utf-8") as f:
                    llm_data = json.load(f)
            except (json.JSONDecodeError, OSError) as e:
                print(f"  WARN: Bad llm_data.json at {llm_path}: {e}")
                continue

            level_info = llm_data.get("level_info")
            if not level_info:
                continue

            svc_name = build_service_name(level_info)

            yield {
                "route_name": route_name,
                "section_name": section_name,
                "train_class": train_class,
                "service_name": svc_name,
            }


def scan_services():
    """
    Walk the screenshots folder tree and yield dicts:
        route_name, section_name, train_class, service_name
    for each llm_data.json found.

    Handles two folder structures:
        With sections:    route/section/train_class/train_XX/service_XXX/
        Without sections: route/train_class/train_XX/service_XXX/
    """
    for route_name in sorted(os.listdir(SCREENSHOTS_DIR)):
        route_dir = os.path.join(SCREENSHOTS_DIR, route_name)
        if not os.path.isdir(route_dir):
            continue
        if route_name in SKIP_ROUTES:
            continue

        for subfolder in sorted(os.listdir(route_dir)):
            subfolder_path = os.path.join(route_dir, subfolder)
            if not os.path.isdir(subfolder_path):
                continue

            if _has_train_folders(subfolder_path):
                # No section layer: route/train_class/train_XX/...
                yield from _scan_train_class(route_name, None, subfolder, subfolder_path)
            else:
                # Section layer: route/section/train_class/train_XX/...
                section_name = subfolder
                for train_class in sorted(os.listdir(subfolder_path)):
                    train_class_dir = os.path.join(subfolder_path, train_class)
                    if not os.path.isdir(train_class_dir):
                        continue
                    yield from _scan_train_class(route_name, section_name, train_class, train_class_dir)


# ---------------------------------------------------------------------------
# Main
# ---------------------------------------------------------------------------
def main():
    if DRY_RUN:
        print("=== DRY RUN === (use --apply to make changes)\n")
    else:
        print("=== APPLYING CHANGES ===\n")

    # Caches
    route_id_cache = {}        # route_name -> route_id
    train_id_cache = {}        # train_class -> train_id
    section_id_cache = {}      # (route_id, section_name) -> section_id
    tt_cache = {}              # (route_id, service_name) -> timetable_id or None
    tt_trains_cache = {}       # timetable_id -> set of train_ids
    tt_sections_cache = {}     # timetable_id -> set of section_ids

    stats = {
        "scanned": 0,
        "trains_added": 0,
        "sections_added": 0,
        "already_ok": 0,
        "not_found": 0,
        "errors": 0,
    }

    current_route = None

    for entry in scan_services():
        stats["scanned"] += 1
        route_name = entry["route_name"]
        section_name = entry["section_name"]
        train_class = entry["train_class"]
        svc_name = entry["service_name"]

        # Print route header on change
        if route_name != current_route:
            current_route = route_name
            print(f"\n{'='*60}")
            print(f"Route: {route_name}")
            print(f"{'='*60}")

        # --- Resolve route ID ---
        if route_name not in route_id_cache:
            try:
                encoded = urllib.parse.quote(route_name)
                result = api_get(f"/api/routes/by-name?name={encoded}")
                route_id_cache[route_name] = result.get("id") if result else None
            except Exception as e:
                print(f"  ERROR looking up route '{route_name}': {e}")
                route_id_cache[route_name] = None

        route_id = route_id_cache[route_name]
        if not route_id:
            stats["not_found"] += 1
            continue

        # --- Resolve train ID ---
        if train_class not in train_id_cache:
            try:
                encoded = urllib.parse.quote(train_class)
                result = api_get(f"/api/trains/by-name?name={encoded}")
                train_id_cache[train_class] = result.get("id") if result else None
                if not train_id_cache[train_class]:
                    print(f"  WARN: Train '{train_class}' not found in DB")
            except Exception as e:
                print(f"  ERROR looking up train '{train_class}': {e}")
                train_id_cache[train_class] = None

        train_id = train_id_cache[train_class]
        if not train_id:
            stats["not_found"] += 1
            continue

        # --- Resolve section ID ---
        sec_key = (route_id, section_name)
        if sec_key not in section_id_cache:
            try:
                sections = api_get(f"/api/routes/{route_id}/sections")
                found = None
                for s in sections:
                    if s.get("name") == section_name:
                        found = s["id"]
                        break
                section_id_cache[sec_key] = found
                if not found:
                    print(f"  WARN: Section '{section_name}' not found on route {route_id}")
            except Exception as e:
                print(f"  ERROR looking up section '{section_name}': {e}")
                section_id_cache[sec_key] = None

        section_id = section_id_cache[sec_key]

        # --- Look up timetable ---
        tt_key = (route_id, svc_name)
        if tt_key not in tt_cache:
            try:
                encoded_svc = urllib.parse.quote(svc_name)
                check = api_get(f"/api/timetables/check-service?service_name={encoded_svc}&route_id={route_id}")
                if check.get("exists"):
                    tt_cache[tt_key] = check.get("timetable_id")
                else:
                    tt_cache[tt_key] = None
            except Exception as e:
                print(f"  ERROR checking service '{svc_name}': {e}")
                tt_cache[tt_key] = None

        tt_id = tt_cache[tt_key]
        if not tt_id:
            stats["not_found"] += 1
            continue

        # --- Get current trains/sections for this timetable (cached) ---
        if tt_id not in tt_trains_cache:
            try:
                trains = api_get(f"/api/timetables/{tt_id}/trains")
                tt_trains_cache[tt_id] = set(t["id"] for t in trains)
            except Exception:
                tt_trains_cache[tt_id] = set()

        if tt_id not in tt_sections_cache:
            try:
                secs = api_get(f"/api/timetables/{tt_id}/sections")
                tt_sections_cache[tt_id] = set(s["id"] for s in secs)
            except Exception:
                tt_sections_cache[tt_id] = set()

        need_train = train_id not in tt_trains_cache[tt_id]
        need_section = section_id and section_id not in tt_sections_cache[tt_id]

        if not need_train and not need_section:
            stats["already_ok"] += 1
            continue

        # --- Add missing links ---
        if need_train:
            if DRY_RUN:
                print(f"  WOULD ADD train '{train_class}' -> timetable {tt_id} ({svc_name})")
            else:
                try:
                    status, result = api_post_json(f"/api/timetables/{tt_id}/trains", {"train_id": train_id})
                    if status in (200, 201):
                        print(f"  ADDED train '{train_class}' -> timetable {tt_id} ({svc_name})")
                    else:
                        print(f"  FAILED ({status}): train '{train_class}' -> timetable {tt_id}: {result}")
                        stats["errors"] += 1
                        continue
                except Exception as e:
                    print(f"  ERROR adding train: {e}")
                    stats["errors"] += 1
                    continue
            tt_trains_cache[tt_id].add(train_id)
            stats["trains_added"] += 1

        if need_section:
            if DRY_RUN:
                print(f"  WOULD ADD section <{train_class}> '{section_name}' -> timetable {tt_id} ({svc_name})")
            else:
                try:
                    status, result = api_post_json(f"/api/timetables/{tt_id}/sections", {"section_id": section_id})
                    if status in (200, 201):
                        print(f"  ADDED section <{train_class}> '{section_name}' -> timetable {tt_id} ({svc_name})")
                    else:
                        print(f"  FAILED ({status}): section <{train_class}> '{section_name}' -> timetable {tt_id}: {result}")
                        stats["errors"] += 1
                        continue
                except Exception as e:
                    print(f"  ERROR adding section: {e}")
                    stats["errors"] += 1
                    continue
            tt_sections_cache[tt_id].add(section_id)
            stats["sections_added"] += 1

    # Summary
    prefix = "would be " if DRY_RUN else ""
    print(f"\n{'='*60}")
    print("SUMMARY")
    print(f"{'='*60}")
    print(f"  Services scanned:          {stats['scanned']}")
    print(f"  Trains {prefix}added:          {stats['trains_added']}")
    print(f"  Sections {prefix}added:        {stats['sections_added']}")
    print(f"  Already correct:           {stats['already_ok']}")
    print(f"  Timetable/train not found: {stats['not_found']}")
    print(f"  Errors:                    {stats['errors']}")


if __name__ == "__main__":
    main()
