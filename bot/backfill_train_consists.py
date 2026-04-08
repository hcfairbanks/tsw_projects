"""Backfill the train_consists table from existing llm_data.json files.

Walks the screenshots directory, reads level_info from each llm_data.json,
resolves route/train/timetable IDs via the hud-go API, and POSTs consist data.

Usage:
    python backfill_train_consists.py              # dry run (print what would be sent)
    python backfill_train_consists.py --commit      # actually POST to server
"""

import json
import os
import re
import sys
import urllib.error
import urllib.parse
import urllib.request

SERVER_URL = "http://localhost:3000"
SCREENSHOTS_DIR = os.path.join(os.path.dirname(__file__), "screenshots")


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
        with urllib.request.urlopen(req, timeout=60) as resp:
            return resp.status, json.loads(resp.read().decode())
    except urllib.error.HTTPError as e:
        body_bytes = e.read()
        try:
            err_json = json.loads(body_bytes.decode())
        except Exception:
            err_json = {"error": body_bytes.decode()}
        return e.code, err_json


# Caches
_route_cache = {}     # route_name -> route_id
_train_cache = {}     # train_name -> train_id
_timetable_cache = {} # (service_name, route_id) -> timetable_id


def _folder_name_variants(name):
    """Generate possible DB names from a Windows-safe folder name.

    Windows doesn't allow : or / in folder names, so they get replaced with _.
    Try the original first, then substitutions.
    """
    yield name
    if "_" in name:
        yield name.replace("_", ":")
        yield name.replace("_", "/")


def resolve_route(route_name):
    if route_name in _route_cache:
        return _route_cache[route_name]
    for variant in _folder_name_variants(route_name):
        encoded = urllib.parse.quote(variant)
        try:
            result = api_get(f"/api/routes/by-name?name={encoded}")
            rid = result["id"]
            _route_cache[route_name] = rid
            return rid
        except Exception:
            continue
    _route_cache[route_name] = None
    return None


def resolve_train(train_name):
    if train_name in _train_cache:
        return _train_cache[train_name]
    for variant in _folder_name_variants(train_name):
        encoded = urllib.parse.quote(variant)
        try:
            result = api_get(f"/api/trains/by-name?name={encoded}")
            tid = result["id"]
            _train_cache[train_name] = tid
            return tid
        except Exception:
            continue
    _train_cache[train_name] = None
    return None


def resolve_timetable(service_name, route_id):
    key = (service_name, route_id)
    if key in _timetable_cache:
        return _timetable_cache[key]
    encoded = urllib.parse.quote(service_name)
    try:
        result = api_get(f"/api/timetables/check-service?service_name={encoded}&route_id={route_id}")
        tt_id = result.get("timetable_id") or result.get("id")
        _timetable_cache[key] = tt_id
        return tt_id
    except Exception:
        _timetable_cache[key] = None
        return None


def parse_path(llm_path):
    """Extract route_name, train_name, train_number from the file path.

    Handles both:
      screenshots/<Route>/<Train>/train_NN/service_NNN/llm_data.json
      screenshots/<Route>/<Timetable>/<Train>/train_NN/service_NNN/llm_data.json
    """
    rel = os.path.relpath(llm_path, SCREENSHOTS_DIR).replace("\\", "/")
    parts = rel.split("/")

    # Find the train_NN folder
    train_dir_idx = None
    for i, p in enumerate(parts):
        if re.match(r"^train_\d+$", p):
            train_dir_idx = i
            break

    if train_dir_idx is None:
        return None, None, None

    route_name = parts[0]
    train_name = parts[train_dir_idx - 1]
    train_number = int(parts[train_dir_idx].split("_")[1])

    return route_name, train_name, train_number


def main():
    commit = "--commit" in sys.argv
    if not commit:
        print("DRY RUN — pass --commit to actually send data\n")

    # Find all llm_data.json files
    llm_files = []
    for root, dirs, files in os.walk(SCREENSHOTS_DIR):
        if "llm_data.json" in files:
            llm_files.append(os.path.join(root, "llm_data.json"))

    print(f"Found {len(llm_files)} llm_data.json files\n")

    stats = {"sent": 0, "skipped_no_route": 0, "skipped_no_train": 0,
             "skipped_no_timetable": 0, "skipped_no_level_info": 0, "errors": 0}

    for llm_path in sorted(llm_files):
        route_name, train_name, train_number = parse_path(llm_path)
        if route_name is None:
            continue

        # Skip "old" routes
        if " old" in route_name:
            continue

        # Load llm_data
        with open(llm_path, "r", encoding="utf-8") as f:
            data = json.load(f)

        level_info = data.get("level_info")
        if not level_info:
            stats["skipped_no_level_info"] += 1
            continue

        service_name_raw = level_info.get("service_name", "")
        start_time = level_info.get("start_time", "")
        duration = level_info.get("duration", "")
        tonnage = level_info.get("tonnage")
        car_count = level_info.get("car_count")
        train_length = level_info.get("train_length")

        location = data.get("location") or {}
        lat = location.get("lat")
        lng = location.get("lng")

        # Build full service name (same format as uploader)
        name_parts = [service_name_raw]
        if start_time:
            name_parts.append(start_time)
        if duration:
            name_parts.append(duration)
        full_service_name = " ".join(name_parts)

        # Resolve IDs
        route_id = resolve_route(route_name)
        if not route_id:
            stats["skipped_no_route"] += 1
            continue

        train_id = resolve_train(train_name)
        if not train_id:
            stats["skipped_no_train"] += 1
            continue

        timetable_id = resolve_timetable(full_service_name, route_id)
        if not timetable_id:
            # Try without times (in case format differs)
            timetable_id = resolve_timetable(service_name_raw, route_id)
        if not timetable_id:
            stats["skipped_no_timetable"] += 1
            rel = os.path.relpath(llm_path, SCREENSHOTS_DIR)
            print(f"  [skip] No timetable found for '{full_service_name}' on route {route_name} -- {rel}")
            continue

        body = {
            "timetable_id": timetable_id,
            "train_id": train_id,
            "train_number": train_number,
        }
        if tonnage is not None:
            body["weight"] = tonnage
        if car_count is not None:
            body["car_count"] = car_count
        if train_length is not None:
            body["train_length"] = train_length
        if lat is not None:
            body["latitude"] = lat
        if lng is not None:
            body["longitude"] = lng

        rel = os.path.relpath(llm_path, SCREENSHOTS_DIR)
        if commit:
            status, result = api_post_json("/api/train-consists", body)
            if status == 201:
                stats["sent"] += 1
                print(f"  OK  {rel}")
            else:
                stats["errors"] += 1
                print(f"  ERR {rel} — {result.get('error', status)}")
        else:
            stats["sent"] += 1
            print(f"  [dry] {rel} -> tt={timetable_id} train={train_id} #{train_number} "
                  f"w={tonnage} cc={car_count} tl={train_length}")

    print(f"\n{'COMMITTED' if commit else 'DRY RUN'} Summary:")
    print(f"  Sent:               {stats['sent']}")
    print(f"  Skipped (no route): {stats['skipped_no_route']}")
    print(f"  Skipped (no train): {stats['skipped_no_train']}")
    print(f"  Skipped (no tt):    {stats['skipped_no_timetable']}")
    print(f"  Skipped (no info):  {stats['skipped_no_level_info']}")
    print(f"  Errors:             {stats['errors']}")

    failed_routes = [name for name, rid in _route_cache.items() if rid is None]
    if failed_routes:
        print(f"\n  Routes not found in DB:")
        for r in sorted(failed_routes):
            print(f"    - {r}")

    failed_trains = [name for name, tid in _train_cache.items() if tid is None]
    if failed_trains:
        print(f"\n  Trains not found in DB:")
        for t in sorted(failed_trains):
            print(f"    - {t}")


if __name__ == "__main__":
    main()
