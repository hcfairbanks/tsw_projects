"""Upload captured service data to the parent app for OCR and database storage.

Calls the Node.js server's HTTP API to:
1. Run OCR on captured images (same preprocessing pipeline as the web UI)
2. Create timetable entries in the database

Saves raw OCR output and service name text files locally.
Logs duplicate service names to a separate file.
"""

import json
import os
import re
import urllib.error
import urllib.parse
import urllib.request

import config

# Server base URL (parent Node.js app)
SERVER_URL = getattr(config, "SERVER_URL", "http://localhost:3000")

# Cached IDs — resolved once at startup, then reused
_route_id = None
_train_ids = None


def reset_route_cache():
    """Clear cached route/train IDs so the next call re-resolves them."""
    global _route_id, _train_ids
    _route_id = None
    _train_ids = None


def _api_get(path):
    """GET request to the parent app API. Returns parsed JSON."""
    url = f"{SERVER_URL}{path}"
    req = urllib.request.Request(url, method="GET")
    with urllib.request.urlopen(req, timeout=30) as resp:
        return json.loads(resp.read().decode())


def _api_post_json(path, body):
    """POST JSON to the parent app API. Returns (status_code, parsed_json)."""
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


def _api_post_multipart(path, files):
    """POST multipart/form-data with file uploads. Returns (status_code, parsed_json).

    files: list of (field_name, filename, file_bytes)
    """
    boundary = "----BotUploadBoundary9876543210"
    body_parts = []
    for field, filename, data in files:
        body_parts.append(f"--{boundary}\r\n".encode())
        body_parts.append(
            f'Content-Disposition: form-data; name="{field}"; filename="{filename}"\r\n'
            f"Content-Type: application/octet-stream\r\n\r\n".encode()
        )
        body_parts.append(data)
        body_parts.append(b"\r\n")
    body_parts.append(f"--{boundary}--\r\n".encode())
    body = b"".join(body_parts)

    url = f"{SERVER_URL}{path}"
    req = urllib.request.Request(
        url, data=body, method="POST",
        headers={"Content-Type": f"multipart/form-data; boundary={boundary}"},
    )
    try:
        with urllib.request.urlopen(req, timeout=120) as resp:
            return resp.status, json.loads(resp.read().decode())
    except urllib.error.HTTPError as e:
        body_bytes = e.read()
        try:
            err_json = json.loads(body_bytes.decode())
        except Exception:
            err_json = {"error": body_bytes.decode()}
        return e.code, err_json


def fix_service_name_timestamps(service_name):
    """Auto-correct common OCR errors in the two trailing timestamps.

    Ensures:
    1. Missing colons are inserted: '0800' → '08:00'
    2. A space exists before each trailing time: '[09:50' → '[ 09:50'
    """
    def insert_colon(digits):
        if ':' in digits:
            return digits
        if len(digits) in (3, 4):
            return digits[:-2] + ':' + digits[-2:]
        return digits

    # Find two time-like patterns at the end of the string.
    # (?<!\d) prevents matching inside a longer number (e.g. service code "0537:")
    m = re.search(
        r'(?<!\d)(\d{1,2}:\d{2}|\d{3,4})'  # time 1
        r'\s+'                                # separator
        r'(\d{1,2}:\d{2}|\d{3,4})'          # time 2
        r'\s*$',                              # end of string
        service_name,
    )
    if not m:
        return service_name

    time1 = insert_colon(m.group(1))
    time2 = insert_colon(m.group(2))

    # Everything before the first time — ensure it ends with a space
    before = service_name[:m.start()]
    if before and not before.endswith(' '):
        before += ' '

    return f"{before}{time1} {time2}"


def _resolve_route_id():
    """Look up the route ID for config.ROUTE_NAME by name."""
    encoded_name = urllib.parse.quote(config.ROUTE_NAME)
    try:
        route = _api_get(f"/api/routes/by-name?name={encoded_name}")
        return route["id"]
    except urllib.error.HTTPError as e:
        if e.code == 404:
            raise ValueError(
                f"No route with name '{config.ROUTE_NAME}' found in the database."
            )
        raise


def get_trains():
    """Fetch train names for the configured route from the database.

    Returns a list of train name strings.
    Resolves the route ID as a side effect (cached for later use).
    """
    global _route_id
    if _route_id is None:
        print(f"  [uploader] Resolving route '{config.ROUTE_NAME}'...")
        _route_id = _resolve_route_id()
        print(f"  [uploader] Route ID: {_route_id}")

    trains = _api_get(f"/api/routes/{_route_id}/trains")
    names = [tc["name"] for tc in trains if tc.get("name")]
    if not names:
        raise ValueError(
            f"No trains found on route '{config.ROUTE_NAME}'."
        )
    return names


def get_sections():
    """Fetch section names for the configured route from the database.

    Returns a list of section name strings (empty list if no sections).
    Resolves the route ID as a side effect (cached for later use).
    """
    global _route_id
    if _route_id is None:
        print(f"  [uploader] Resolving route '{config.ROUTE_NAME}'...")
        _route_id = _resolve_route_id()
        print(f"  [uploader] Route ID: {_route_id}")

    sections = _api_get(f"/api/routes/{_route_id}/sections")
    names = [s["name"] for s in sections if s.get("name")]
    return names


def get_section_trains(section_name):
    """Fetch train names for a specific section from the database.

    Returns a list of train name strings (empty list if no trains assigned).
    Resolves the route ID as a side effect (cached for later use).
    """
    global _route_id
    if _route_id is None:
        print(f"  [uploader] Resolving route '{config.ROUTE_NAME}'...")
        _route_id = _resolve_route_id()
        print(f"  [uploader] Route ID: {_route_id}")

    # Find the section by name
    sections = _api_get(f"/api/routes/{_route_id}/sections")
    section = None
    for s in sections:
        if s.get("name") == section_name:
            section = s
            break
    if not section:
        return []

    trains = _api_get(f"/api/routes/{_route_id}/sections/{section['id']}/trains")
    return [t["name"] for t in trains if t.get("name")]


def _resolve_train_ids(route_id, train_name):
    """Look up train IDs for the given train on the given route by name."""
    encoded_name = urllib.parse.quote(train_name)
    try:
        train = _api_get(f"/api/trains/by-name?name={encoded_name}")
        train_id = train["id"]
    except urllib.error.HTTPError as e:
        if e.code == 404:
            raise ValueError(
                f"No train with name '{train_name}' found in the database."
            )
        raise

    # Verify this train is on the route
    route_trains = _api_get(f"/api/routes/{route_id}/trains")
    if not any(rt["id"] == train_id for rt in route_trains):
        raise ValueError(
            f"Train '{train_name}' (ID {train_id}) is not assigned to route "
            f"'{config.ROUTE_NAME}'. Please add it to the route in the web UI."
        )

    return [train_id]


def resolve_ids(train_name):
    """Resolve and cache route_id and train_ids for the given train.

    Call once per train (after server is confirmed running).
    Raises ValueError if route or train not found.
    """
    global _route_id, _train_ids
    if _route_id is None:
        print(f"  [uploader] Resolving route '{config.ROUTE_NAME}'...")
        _route_id = _resolve_route_id()
        print(f"  [uploader] Route ID: {_route_id}")

    print(f"  [uploader] Resolving train '{train_name}'...")
    _train_ids = _resolve_train_ids(_route_id, train_name)
    print(f"  [uploader] Train IDs: {_train_ids}")


def check_server():
    """Check that the Node.js server is reachable. Returns True/False."""
    use_llm_only = (getattr(config, 'USE_LLM_SCHEDULE', False)
                    and getattr(config, 'USE_LLM_LEVEL_INFO', False))
    try:
        status = _api_get("/api/ocr-status")
        if not status.get("available") and not use_llm_only:
            print("  [uploader] WARNING: Server is up but OCR is not available.")
            return False
        if not status.get("available") and use_llm_only:
            print("  [uploader] OCR not available, but LLM-only mode is on — OK.")
        return True
    except Exception as e:
        print(f"  [uploader] Cannot reach server at {SERVER_URL}: {e}")
        return False


def check_service_exists(service_code):
    """Check if a timetable with this service code already exists in the DB.

    Returns True if found, False otherwise. On error, returns False so the
    bot proceeds with normal capture rather than skipping.
    """
    encoded = urllib.parse.quote(service_code)
    try:
        result = _api_get(f"/api/timetables/check-service?service={encoded}")
        exists = result.get("exists", False)
        if exists:
            print(f"       [uploader] Service '{service_code}' already exists in DB")
        return exists
    except Exception as e:
        print(f"       [uploader] Check service failed: {e}")
        return False


def get_existing_services(train_name):
    """Fetch all service codes already recorded in the DB for a train.

    Looks up the train by name, then queries for all service codes
    linked to that train. Returns a set of service code strings.
    On error, returns an empty set so the bot proceeds normally.
    """
    try:
        encoded_name = urllib.parse.quote(train_name)
        train = _api_get(f"/api/trains/by-name?name={encoded_name}")
        train_id = train["id"]
        result = _api_get(f"/api/timetables/services-by-train?train_id={train_id}")
        services = set(result.get("services", []))
        print(f"  [uploader] {len(services)} services already in DB for '{train_name}'")
        return services
    except Exception as e:
        print(f"  [uploader] Failed to fetch existing services: {e}")
        return set()


def upload_service(service_dir, train_name="", train_number=0, service_number=0,
                   first_station=None, bound=None, service=None,
                   first_lat=None, first_lng=None, section_name=None,
                   current_service_name=None):
    """Run OCR on captured service images and save to the database.

    Sends all images (1_service.png, 2a_tonnage.png, 2b_car_count.png,
    2c_car_length.png, 3_schedule.png) to /api/extract which handles
    service name, level info, and timetable OCR in a single request.

    Args:
        service_dir: Path to the service folder.
        train_name: Train name (for rejection logging).
        train_number: 1-indexed train number (for rejection logging).
        service_number: 1-indexed service number (for rejection logging).
        first_station: Station name to use as the first entry's location (from spreadsheet).
        bound: Direction of travel ('northbound' or 'southbound').
        first_lat: Latitude for the first timetable entry (from TSW API).
        first_lng: Longitude for the first timetable entry (from TSW API).

    Returns:
        dict with keys:
            success (bool), service_name (str), timetable_id (int or None),
            duplicate (bool), error (str or None)
    """
    if _route_id is None or _train_ids is None:
        return {"success": False, "service_name": "", "timetable_id": None,
                "duplicate": False, "error": "IDs not resolved — call resolve_ids() first."}

    service_img = os.path.join(service_dir, "1_service.png")
    service_name_img = os.path.join(service_dir, "1b_service_name.png")
    schedule_img = os.path.join(service_dir, "3_schedule.png")

    use_llm_schedule = getattr(config, 'USE_LLM_SCHEDULE', False)
    use_llm_level_info = getattr(config, 'USE_LLM_LEVEL_INFO', False)
    use_llm_only = use_llm_schedule and use_llm_level_info

    # --- Step 1: Load LLM data from llm_data.json ---
    llm_data_path = os.path.join(service_dir, "llm_data.json")
    llm_data = {}
    if os.path.isfile(llm_data_path):
        try:
            with open(llm_data_path, "r", encoding="utf-8") as f:
                llm_data = json.load(f)
            print(f"       [uploader] Loaded llm_data.json")
        except Exception as e:
            print(f"       [uploader] WARNING: Failed to load llm_data.json: {e}")

    if use_llm_only:
        # --- LLM-only path: no OCR at all ---
        llm_info = llm_data.get("level_info") or {}
        llm_entries = llm_data.get("schedule")

        # Build service name from LLM level info
        raw_service_name = llm_info.get("service_name", "Unknown Service")
        start_time = llm_info.get("start_time", "")
        duration = llm_info.get("duration", "")
        # Format: "Service Name HH:MM HH:MM"
        name_parts = [raw_service_name]
        if start_time:
            # Convert "HH:MM:SS" to "HH:MM" if needed
            st = start_time.strip()
            if len(st.split(":")) == 3:
                st = ":".join(st.split(":")[:2])
            name_parts.append(st)
        if duration:
            dur = duration.strip()
            if len(dur.split(":")) == 3:
                dur = ":".join(dur.split(":")[:2])
            name_parts.append(dur)
        service_name = " ".join(name_parts)
        print(f"       [uploader] LLM service name: '{service_name}'")

        if llm_entries:
            entries = llm_entries
            print(f"       [uploader] LLM schedule: {len(entries)} entries")
        else:
            print(f"       [uploader] WARNING: No schedule in llm_data.json")
            entries = []

        # Save service name file
        _save_service_name(service_dir, service_name)
        raw_texts = []

    else:
        # --- OCR path (original behavior) ---
        if not os.path.isfile(service_img) or not os.path.isfile(schedule_img):
            return {"success": False, "service_name": "", "timetable_id": None,
                    "duplicate": False, "error": "Missing image files in service directory."}

        print(f"       [uploader] Sending images to OCR...")
        files = []
        with open(service_img, "rb") as f:
            files.append(("images", "1_service.png", f.read()))
        if os.path.isfile(service_name_img):
            with open(service_name_img, "rb") as f:
                files.append(("images", "1b_service_name.png", f.read()))
        if not use_llm_schedule:
            with open(schedule_img, "rb") as f:
                files.append(("images", "3_schedule.png", f.read()))

        status, ocr_result = _api_post_multipart("/api/extract", files)
        if status != 200:
            err = ocr_result.get("error", "Unknown OCR error")
            _log_rejected(train_name, train_number, service_number,
                          f"OCR FAILED ({status}): {err}", "")
            return {"success": False, "service_name": "", "timetable_id": None,
                    "duplicate": False, "error": f"OCR failed ({status}): {err}"}

        service_name = ocr_result.get("service_name", "Unknown Service")
        service_name = fix_service_name_timestamps(service_name)
        entries = ocr_result.get("entries", [])
        raw_texts = ocr_result.get("rawTexts", [])

        print(f"       [uploader] OCR returned: '{service_name}' with {len(entries)} entries")

        if use_llm_schedule:
            llm_entries = llm_data.get("schedule")
            if llm_entries:
                print(f"       [uploader] Using LLM schedule: {len(llm_entries)} entries "
                      f"(replacing {len(entries)} OCR entries)")
                entries = llm_entries
            else:
                print(f"       [uploader] WARNING: No schedule in llm_data.json, "
                      f"falling back to OCR entries")

        # Save raw OCR output
        _save_raw_ocr(service_dir, raw_texts)
        _save_service_name(service_dir, service_name)

        # If new method was used (1b_service_name.png present), append to initial_methode_results_2.txt
        if os.path.isfile(service_name_img):
            _append_initial_methode_results_2(service_name, train_name)

    # --- Step 3: Set first entry location and coordinates ---
    if entries:
        first_loc = (entries[0].get("location") or "").strip()
        if not first_loc:
            if first_station:
                entries[0]["location"] = first_station
                print(f"       [uploader] Set first entry location to '{first_station}'")
            else:
                entries[0]["location"] = "Start"
                print(f"       [uploader] Set first entry location to 'Start'")
        if first_lat is not None and first_lng is not None:
            entries[0]["latitude"] = str(first_lat)
            entries[0]["longitude"] = str(first_lng)
            entries[0]["coord_source"] = "automatic"
            print(f"       [uploader] Set first entry coords: {first_lat}, {first_lng}")

    # --- Step 4: Build timetable body with level info ---
    body = {
        "service_name": service_name,
        "route_id": _route_id,
        "train_ids": _train_ids,
        "entries": entries,
    }
    if bound:
        body["bound"] = bound
    if service:
        body["service"] = service
    if current_service_name:
        body["current_service_name"] = current_service_name
    if llm_data.get("conductor_compatible"):
        body["conductor_compatible"] = True
    if section_name:
        # Resolve section name to ID via the API
        try:
            sections = _api_get(f"/api/routes/{_route_id}/sections")
            for s in sections:
                if s.get("name") == section_name:
                    body["section_id"] = s["id"]
                    print(f"       [uploader] Resolved section '{section_name}' to ID: {s['id']}")
                    break
            else:
                print(f"       [uploader] WARNING: Section '{section_name}' not found in database")
        except Exception as e:
            print(f"       [uploader] WARNING: Failed to resolve section: {e}")

    llm_info = llm_data.get("level_info")

    if use_llm_only:
        # All level info comes from LLM
        if llm_info:
            print(f"       [uploader] LLM level info: start_time={llm_info.get('start_time')}, "
                  f"duration={llm_info.get('duration')}, tonnage={llm_info.get('tonnage')}, "
                  f"car_count={llm_info.get('car_count')}, train_length={llm_info.get('train_length')}")
            if llm_info.get("start_time"):
                body["start_time"] = llm_info["start_time"]
            if llm_info.get("duration"):
                body["duration"] = llm_info["duration"]
            if llm_info.get("tonnage") is not None:
                body["tonnage"] = llm_info["tonnage"]
            if llm_info.get("car_count") is not None:
                body["car_count"] = llm_info["car_count"]
            if llm_info.get("train_length") is not None:
                body["train_length"] = llm_info["train_length"]
        else:
            print(f"       [uploader] WARNING: No level_info in llm_data.json")
    else:
        # OCR path: use OCR level info with LLM as fallback
        tonnage = ocr_result.get("tonnage")
        car_count = ocr_result.get("car_count")
        train_length = ocr_result.get("train_length")
        if tonnage is not None or car_count is not None or train_length is not None:
            print(f"       [uploader] Level info: {tonnage} t, {car_count} cars, {train_length} m")
            level_raw = ocr_result.get("level_info_raw", "")
            _save_level_info(service_dir, level_raw, tonnage, car_count, train_length)
        if tonnage is not None:
            body["tonnage"] = tonnage
        if car_count is not None:
            body["car_count"] = car_count
        if train_length is not None:
            body["train_length"] = train_length

        # Fill in missing values from LLM
        if llm_info:
            print(f"       [uploader] LLM level info: start_time={llm_info.get('start_time')}, "
                  f"duration={llm_info.get('duration')}, tonnage={llm_info.get('tonnage')}, "
                  f"car_count={llm_info.get('car_count')}, train_length={llm_info.get('train_length')}")
            if llm_info.get("start_time"):
                body["start_time"] = llm_info["start_time"]
            if llm_info.get("duration"):
                body["duration"] = llm_info["duration"]
            if tonnage is None and llm_info.get("tonnage") is not None:
                body["tonnage"] = llm_info["tonnage"]
            if car_count is None and llm_info.get("car_count") is not None:
                body["car_count"] = llm_info["car_count"]
            if train_length is None and llm_info.get("train_length") is not None:
                body["train_length"] = llm_info["train_length"]
        else:
            print(f"       [uploader] WARNING: No level_info in llm_data.json")
    # Store relative path to service images directory (relative to project root)
    project_root = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
    body["service_images"] = os.path.relpath(service_dir, project_root).replace("\\", "/")
    status, result = _api_post_json("/api/timetables", body)

    if status == 201:
        tt_id = result.get("id")
        print(f"       [uploader] Timetable created (ID: {tt_id})")
        return {"success": True, "service_name": service_name,
                "timetable_id": tt_id, "duplicate": False, "error": None}

    if status == 200 and result.get("timetable_id"):
        # Duplicate timetable — train(s) and/or section(s) may have been added
        tt_id = result.get("timetable_id")
        trains_added = result.get("trains_added", 0)
        sections_added = result.get("sections_added", 0)
        if trains_added > 0 or sections_added > 0:
            parts = []
            if trains_added > 0:
                parts.append(f"{trains_added} train(s)")
            if sections_added > 0:
                parts.append(f"{sections_added} section(s)")
            print(f"       [uploader] ADDED: '{service_name}' already exists (timetable {tt_id}), added {' and '.join(parts)}.")
            return {"success": True, "service_name": service_name,
                    "timetable_id": tt_id, "duplicate": True,
                    "train_added": trains_added > 0, "section_added": sections_added > 0,
                    "error": None}
        else:
            print(f"       [uploader] DUPLICATE: '{service_name}' already exists (timetable {tt_id}), train/section already linked.")
            _log_rejected(train_name, train_number, service_number,
                          "DUPLICATE", service_name, section_name)
            return {"success": False, "service_name": service_name,
                    "timetable_id": tt_id, "duplicate": True,
                    "train_added": False, "section_added": False, "error": None}

    if status == 409:
        # Duplicate service name (legacy fallback)
        print(f"       [uploader] DUPLICATE: '{service_name}' already exists.")
        _log_rejected(train_name, train_number, service_number,
                      "DUPLICATE", service_name, section_name)
        return {"success": False, "service_name": service_name,
                "timetable_id": None, "duplicate": True, "train_added": False, "section_added": False, "error": None}

    # Other error
    err = result.get("error", "Unknown error")
    print(f"       [uploader] Create failed ({status}): {err}")
    _log_rejected(train_name, train_number, service_number,
                  f"ERROR ({status}): {err}", service_name)
    return {"success": False, "service_name": service_name,
            "timetable_id": None, "duplicate": False, "error": f"Create failed ({status}): {err}"}


def _save_raw_ocr(service_dir, raw_texts):
    """Save raw OCR output to a text file in the service folder."""
    path = os.path.join(service_dir, "ocr_raw.txt")
    with open(path, "w", encoding="utf-8") as f:
        for entry in raw_texts:
            f.write(f"{'=' * 70}\n")
            f.write(f"File: {entry.get('file', 'Unknown')}\n")
            f.write(f"{'=' * 70}\n")
            f.write(entry.get("text", ""))
            f.write("\n\n")
    print(f"       [uploader] Raw OCR saved: {path}")


def _save_service_name(service_dir, service_name):
    """Save the service name to a separate text file in the service folder."""
    path = os.path.join(service_dir, "service_name.txt")
    with open(path, "w", encoding="utf-8") as f:
        f.write(service_name)


def _append_initial_methode_results_2(service_name, train_name):
    """Append the full service name to initial_methode_results_2.txt, grouped by train class.

    Format:
        ACS-64 V AMTK50

        1 Amtrak Northeast Regional #95 (Boston - Providence) 06:09 00:42
        2 Amtrak Northeast Regional #66 (Providence - Boston) 06:57 00:42

        Some Other Train

        1 ...
    """
    results_path = os.path.join(config.BASE_DIR, "initial_methode_results_2.txt")

    # Read existing content to find the train class section
    existing = ""
    if os.path.isfile(results_path):
        with open(results_path, "r", encoding="utf-8") as f:
            existing = f.read()

    # Parse existing file into sections keyed by train class name
    sections = {}  # train_name -> list of service entries
    section_order = []  # preserve insertion order
    current_section = None
    for line in existing.split("\n"):
        stripped = line.strip()
        if not stripped:
            continue
        # Check if line is a numbered entry (starts with digit(s) followed by space)
        if re.match(r'^\d+\s', stripped):
            if current_section is not None:
                sections[current_section].append(re.sub(r'^\d+\s+', '', stripped))
        else:
            # It's a header line (train class name)
            current_section = stripped
            if current_section not in sections:
                sections[current_section] = []
                section_order.append(current_section)

    # Add the new entry under the correct train class
    if train_name not in sections:
        sections[train_name] = []
        section_order.append(train_name)
    sections[train_name].append(service_name)

    # Rewrite the file
    with open(results_path, "w", encoding="utf-8") as f:
        for i, section_name in enumerate(section_order):
            if i > 0:
                f.write("\n")
            f.write(section_name + "\n\n")
            for idx, entry in enumerate(sections[section_name], 1):
                f.write(f"{idx} {entry}\n")

    count = len(sections[train_name])
    print(f"       [uploader] Appended to initial_methode_results_2.txt: '{service_name}' "
          f"(#{count} under '{train_name}')")


def _save_level_info(service_dir, raw_text, tonnage, car_count, train_length):
    """Append level info OCR results to ocr_raw.txt in the service folder."""
    path = os.path.join(service_dir, "ocr_raw.txt")
    with open(path, "a", encoding="utf-8") as f:
        f.write(f"{'=' * 70}\n")
        f.write("Level Info (from LLM)\n")
        f.write(f"{'=' * 70}\n")
        f.write(f"Tonnage:      {tonnage}\n")
        f.write(f"Car Count:    {car_count}\n")
        f.write(f"Train Length: {train_length}\n")
        f.write(f"Raw OCR: {raw_text or '(no text extracted)'}\n\n")
    print(f"       [uploader] Level info appended to: {path}")


def _log_rejected(train_name, train_number, service_number, reason, service_name, section_name=None):
    """Append a rejected service to the log file with full context."""
    log_path = os.path.join(config.SCREENSHOTS_DIR, config.ROUTE_NAME, "duplicate_services.txt")
    with open(log_path, "a", encoding="utf-8") as f:
        line = (f"Service {service_number:03d} | {reason} | "
                f"{service_name} | Train {train_number} | {train_name}")
        if section_name:
            line += f" | {section_name}"
        f.write(line + "\n")
    print(f"       [uploader] Logged rejection to: {log_path}")
