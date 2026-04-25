import json
import os

# Paths
BASE_DIR = os.path.dirname(os.path.abspath(__file__))
REFERENCES_DIR = os.path.join(BASE_DIR, "references")
SCREENSHOTS_DIR = os.path.join(BASE_DIR, "screenshots")
CALIBRATION_FILE = os.path.join(BASE_DIR, "calibration.json")
CLICK_REGISTRY_FILE = os.path.join(BASE_DIR, "click_registry.json")

# Parent app server URL (for OCR and database uploads)
SERVER_URL = "http://localhost:3000"


# Steam
STEAM_APP_ID = "3656800"
GAME_PROCESS_NAME = "BootstrapPackagedGame.exe"

# Window positioning
REPOSITION_GAME_WINDOW = False   # move/resize game window to top-left on launch

# Timeouts (seconds)
GAME_LAUNCH_TIMEOUT = 120   # TSW takes a while to start
SCREEN_TIMEOUT = 60         # waiting for a menu screen to appear
CLICK_SETTLE_DELAY = 2.0    # pause after clicking before looking for next screen

# Image matching confidence thresholds
CONFIDENCE = 0.8            # default for menu tiles
CONFIDENCE_LOW = 0.6        # for warning/splash screens (simpler visuals)

# Reference image paths
REF_WARNING_CONTINUE = os.path.join(REFERENCES_DIR, "warning_continue.png")
REF_SPLASH_CONTINUE = os.path.join(REFERENCES_DIR, "splash_continue.png")
REF_TO_THE_TRAINS_1 = os.path.join(REFERENCES_DIR, "to_the_trains_1.png")
REF_TO_THE_TRAINS_2 = os.path.join(REFERENCES_DIR, "to_the_trains_2.png")
REF_CHOOSE_A_ROUTE_1 = os.path.join(REFERENCES_DIR, "choose_a_route_1.png")
REF_CHOOSE_A_ROUTE_2 = os.path.join(REFERENCES_DIR, "choose_a_route_2.png")
REF_ROUTE_FILTER = os.path.join(REFERENCES_DIR, "route_search.png")
REF_TIMETABLE_1 = os.path.join(REFERENCES_DIR, "timetable_1.png")
REF_TIMETABLE_2 = os.path.join(REFERENCES_DIR, "timetable_2.png")
REF_CLASS_FILTER = os.path.join(REFERENCES_DIR, "class_search.png")
REF_EXIT_TO_MAIN_MENU_1 = os.path.join(REFERENCES_DIR, "exit_to_main_menu_1.png")
REF_EXIT_TO_MAIN_MENU_2 = os.path.join(REFERENCES_DIR, "exit_to_main_menu_2.png")
REF_GET_STARTED_1 = os.path.join(REFERENCES_DIR, "get_started_1.png")
REF_GET_STARTED_2 = os.path.join(REFERENCES_DIR, "get_started_2.png")
REF_DRIVER_1 = os.path.join(REFERENCES_DIR, "driver_1.png")
REF_DRIVER_2 = os.path.join(REFERENCES_DIR, "driver_2.png")
REF_EXIT_GAME_1 = os.path.join(REFERENCES_DIR, "exit_game_1.png")
REF_EXIT_GAME_2 = os.path.join(REFERENCES_DIR, "exit_game_2.png")
REF_EXIT_GAME_DIALOGBOX = os.path.join(REFERENCES_DIR, "exit_game_dialogbox.png")
REF_EXIT_GAME_YES_1 = os.path.join(REFERENCES_DIR, "exit_game_yes_1.png")
REF_EXIT_GAME_YES_2 = os.path.join(REFERENCES_DIR, "exit_game_yes_2.png")
REF_SERVICE_FILTER = os.path.join(REFERENCES_DIR, "service_search.png")
REF_SERVICE_NAME_SEARCH = os.path.join(REFERENCES_DIR, "service_name_search.png")
REF_EXTRA_SECTION_FILTER = os.path.join(REFERENCES_DIR, "section_search.png")
REF_TRAINING_POPUP = os.path.join(REFERENCES_DIR, "training.png")
REF_TRAINING_DONT_ASK_AGAIN = os.path.join(REFERENCES_DIR, "training_dont_ask_again.png")

# Spreadsheet directory
SPREADSHEETS_DIR = os.path.join(BASE_DIR, "spreadsheets")

################################################################################

# ── RUN_MANIFEST ─────────────────────────────────────────────────────────────
# Unified config that replaces ROUTE_NAMES, TRAINS, and SECTION_TRAINS.
# Each entry is a dict describing what to process. Omit a key to process ALL
# items at that level.
#
# Keys (all optional except "route"):
#   route       - route name (required)
#   section     - section name (for routes with sections)
#   train_class - train class name (e.g. "ALP-46 NJT")
#   train       - train number, 1-indexed (e.g. 1 for train_01)
#   services    - list of service global_index numbers (e.g. [75, 76, 77])
#   overwrite   - if true, ignore "already captured" checks and re-process
#
# Examples:
#   {"route": "Morristown Line: New York"}
#       → all sections, all train classes, all trains, all services
#
#   {"route": "Morristown Line: New York", "train_class": "ALP-46 NJT"}
#       → only ALP-46 NJT, all trains, all services
#
#   {"route": "Morristown Line: New York", "train_class": "ALP-46 NJT", "train": 1, "services": [75, 76, 77]}
#       → only train_01 of ALP-46 NJT, only services 75/76/77
#
#   {"route": "Boston Sprinter", "section": "Boston - Providence", "train_class": "CTC-3 MBTA"}
#       → only CTC-3 in the Boston - Providence section
#
# Multiple entries for the same route are merged — each entry adds to what
# gets processed. This makes corrections easy: just list the specific
# route/class/train/services that need re-doing.

RUN_MANIFEST = [
    {"route": "LIRR Commuter: New York - Long Beach, Hempstead & Hicksville"},
    {"route": "Bahnstrecke Leipzig - Dresden"},
    {"route": "Nahverkehr Dresden"},
    {"route": "Great Western Express"},
    {"route": "WCML South - London Euston to Milton Keynes"}
    # Up Stairs
    # {"route": "Boston Worcester"},
    # {"route": "London Underground Bakerloo Line"}
]



# TODO
# Need to determine which ones of all layers completed

# Finished  ( Does not have consists )
# {"route": "London Overground: Gospel Oak - Barking"},
# {"route": "Antelope Valley Line"},
# {"route": "Morristown Line: New York"},
# {"route": "LGV Mediterranee"},
# {"route": "Harlem Line"},
# {"route": "West Somerset Railway"},
# {"route": "Oakville Subdivision"},
# {"route": "Isle of Wight"},
# {"route": "Spirit of Steam: Liverpool - Crewe"}

# Finished  ( Does have consists )
# {"route": "Boston Sprinter"},
# {"route": "Horseshoe Curve"},
# {"route": "Sand Patch Grade"},
# {"route": "Sherman Hill: Cheyenne - Laramie"},
# {"route": "Berninalinie"},
# {"route": "Riviera Line"},

# RUN_MANIFEST = [
#     {
#         "route": "Morristown Line: New York",
#         "train_class": "Arrow III NJT",
#         "train": 1,
#         "services": [75, 76, 77, 78, 79, 80, 81, 82, 83, 84, 85, 86, 87, 88, 89, 90, 91, 92, 93],
#         "overwrite": True,
#      },
# ]

# ── Derived from RUN_MANIFEST (backward-compatible) ─────────────────────────
# These are computed from RUN_MANIFEST so that existing code that reads
# ROUTE_NAMES, TRAINS, SECTION_TRAINS still works during the transition.
# New code should call the manifest_* helpers below instead.

def _unique_ordered(seq):
    seen = set()
    out = []
    for x in seq:
        if x not in seen:
            seen.add(x)
            out.append(x)
    return out

ROUTE_NAMES = _unique_ordered(e["route"] for e in RUN_MANIFEST)
TRAINS = []          # legacy — always empty, manifest controls filtering
SECTION_TRAINS = {}  # legacy — always empty, manifest controls filtering

# Active route — set automatically when running multiple routes.
ROUTE_NAME = ROUTE_NAMES[0] if ROUTE_NAMES else None


# ── Manifest query helpers ───────────────────────────────────────────────────

def manifest_entries_for_route(route_name):
    """Return all manifest entries for the given route."""
    return [e for e in RUN_MANIFEST if e["route"] == route_name]


def manifest_filter_sections(route_name, db_sections):
    """Filter sections to only those allowed by the manifest.

    If any entry for this route has no 'section' key, ALL sections are allowed.
    Returns the filtered list (preserving order from db_sections).
    """
    entries = manifest_entries_for_route(route_name)
    if not entries:
        return list(db_sections)
    # If any entry omits 'section', all sections are allowed
    if any("section" not in e for e in entries):
        return list(db_sections)
    allowed = {e["section"] for e in entries if "section" in e}
    return [s for s in db_sections if s in allowed]


def manifest_filter_train_classes(route_name, section, db_train_classes):
    """Filter train classes to only those allowed by the manifest.

    If any matching entry omits 'train_class', ALL classes are allowed.
    Returns the filtered list (preserving order).
    """
    entries = manifest_entries_for_route(route_name)
    # Find entries matching this section (or entries with no section = match all)
    matching = [e for e in entries
                if "section" not in e or e.get("section") == section]
    if not matching:
        return list(db_train_classes)
    # If any matching entry omits train_class, all classes are allowed
    if any("train_class" not in e for e in matching):
        return list(db_train_classes)
    allowed = {e["train_class"] for e in matching if "train_class" in e}
    return [t for t in db_train_classes if t in allowed]


def manifest_filter_trains(route_name, section, train_class, train_count):
    """Return list of 1-indexed train numbers to process.

    If any matching entry omits 'train', returns range(1, train_count+1) (all).
    """
    entries = manifest_entries_for_route(route_name)
    matching = [e for e in entries
                if ("section" not in e or e.get("section") == section)
                and ("train_class" not in e or e.get("train_class") == train_class)]
    if not matching:
        return list(range(1, train_count + 1))
    if any("train" not in e for e in matching):
        return list(range(1, train_count + 1))
    allowed = sorted({e["train"] for e in matching if "train" in e})
    return [t for t in allowed if 1 <= t <= train_count]


def manifest_filter_services(route_name, section, train_class, train_num):
    """Return set of allowed service global_index numbers, or None for 'all'.

    If any matching entry omits 'services', returns None (process all).
    """
    entries = manifest_entries_for_route(route_name)
    matching = [e for e in entries
                if ("section" not in e or e.get("section") == section)
                and ("train_class" not in e or e.get("train_class") == train_class)
                and ("train" not in e or e.get("train") == train_num)]
    if not matching:
        return None  # no specific entries → all services
    if any("services" not in e for e in matching):
        return None  # at least one entry says "all services"
    # Merge all service lists
    allowed = set()
    for e in matching:
        if "services" in e:
            allowed.update(e["services"])
    return allowed if allowed else None


def manifest_is_overwrite(route_name, section, train_class, train_num):
    """Return True if any matching manifest entry has overwrite=True."""
    entries = manifest_entries_for_route(route_name)
    matching = [e for e in entries
                if ("section" not in e or e.get("section") == section)
                and ("train_class" not in e or e.get("train_class") == train_class)
                and ("train" not in e or e.get("train") == train_num)]
    return any(e.get("overwrite") for e in matching)





# Game screen region (x, y, width, height) — game is on the middle monitor
GAME_REGION = (1920, 0, 1920, 1080)

EXTRA_SECTION_TILE_X = 2235  # screen X to click the first result tile (overridden by calibration)
EXTRA_SECTION_TILE_Y = 400   # screen Y to click the first result tile (overridden by calibration)

################################################################################

# Trains are resolved dynamically from the database at runtime.
# Trains are resolved by name from the database.

# Service list scroll area (pixel coordinates)
SERVICE_LIST_LEFT = 2072
SERVICE_LIST_TOP = 320
SERVICE_LIST_RIGHT = 2750
SERVICE_LIST_BOTTOM = 950
SERVICE_BOX_HEIGHT = 58       # actual box height in pixels
SERVICE_BOX_STRIDE = 69       # distance between box tops (58 box + 11 gap)
SCROLL_PER_BOX = -268         # scroll clicks to move one box down (may need re-tuning)
FIRST_BOX_TOP = 22            # row offset from SERVICE_LIST_TOP to top of first box
FIRST_CLICK_X = None          # image-relative x of first service box center (from calibration step 11)
FIRST_CLICK_Y = None          # image-relative y of first service box center (from calibration step 11)
BOXES_PER_PAGE = 8            # visible boxes on one page
BOXES_TO_SCROLL = 6           # boxes to scroll per page advance
OVERLAP_BOXES = 2             # boxes to skip on subsequent pages (overlap)
SCROLL_AMOUNT = SCROLL_PER_BOX * BOXES_TO_SCROLL  # -1608
BOX_EXTRA_RIGHT = 20          # extra px on right for wider box crop
BOX_EXTRA_TOP = 20            # extra px above box for taller crop
SERVICE_BOX_RIGHT_TRIM = 75   # px to trim from right edge of 1_service.png to exclude clock icon
SERVICE_BOX_BORDER_RGB = (0x57, 0xa6, 0xd0)  # #57a6d0 — border color of service rectangles


# Click registry — canonical (page, click_x, click_y) for each service number.
# Built from the Bakerloo Line CSV (standard of truth) at startup, then
# refined/overridden by calibrate_service_scroll.py entries in calibration.json.
# Keys are service numbers (int), values are dicts with page, click_x, click_y.
CLICK_REGISTRY = {}

# Path to the standard-of-truth CSV (Bakerloo Line)
_REFERENCE_CSV = os.path.join(
    SCREENSHOTS_DIR,
    "London Underground Bakerloo Line",
    "Bakerloo Line 2021",
    "1972 MkII Tube Stock LU",
    "train_01",
    "original_services.csv",
)
SERVICE_BOX_COLOR_TOLERANCE = 20              # per-channel tolerance for color matching
LEVEL_LOAD_TIMEOUT = 180     # seconds to wait for a level to load

# Schedule screen area (pixel coordinates)
REF_SCHEDULE = os.path.join(REFERENCES_DIR, "schedule.png")
SCHEDULE_LEFT = 2050
SCHEDULE_TOP = 230
SCHEDULE_RIGHT = 3540
SCHEDULE_BOTTOM = 920
SCHEDULE_TOP_BORDER_RGB = (0x05, 0x97, 0x44)     # #059744 — top border of schedule
SCHEDULE_BOTTOM_BORDER_RGB = (0xc5, 0xe4, 0xe9)  # #c5e4e9 — bottom border of schedule
SCHEDULE_COLOR_TOLERANCE = 20

# Train scroll box (pixel coordinates)
TRAIN_BOX_LEFT = 3212
TRAIN_BOX_TOP = 400
TRAIN_BOX_WIDTH = 476
TRAIN_BOX_HEIGHT = 270
TRAIN_SCROLL_PER_BOX = -310   # scroll clicks to move one train box down
TRAIN_VISIBLE_COUNT = 5       # trains visible without scrolling
TRAIN_FIRST_Y_OFFSET = 60     # Y offset from TRAIN_BOX_TOP to center of first train
TRAIN_BOX_STRIDE = 85         # distance between train box centers

# Level splash screen region (x, y, width, height) — the info panel on the "Get Started" screen
LEVEL_SCREEN_LEFT = 2488
LEVEL_SCREEN_TOP = 301
LEVEL_SCREEN_RIGHT = 3271
LEVEL_SCREEN_BOTTOM = 825
LEVEL_SCREEN_REGION = (LEVEL_SCREEN_LEFT, LEVEL_SCREEN_TOP,
                       LEVEL_SCREEN_RIGHT - LEVEL_SCREEN_LEFT,
                       LEVEL_SCREEN_BOTTOM - LEVEL_SCREEN_TOP)

# LLM settings
LOCAL_LLM_URL = "http://localhost:1234" # local network http://192.168.4.54:1234/
LOCAL_LLM_MODEL = "qwen2.5-vl-7b-instruct"
CLAUDE_MODEL = "claude-sonnet-4-6"

# LLM provider settings: "local" = local LM Studio, "claude" = Claude API
# Each LLM task can be independently configured
USE_LLM_SCHEDULE = True
USE_LLM_LEVEL_INFO = True

# TRAIN_COUNT_LLM_PROVIDER = "claude"       # Count trains on screen
# SCHEDULE_LLM_PROVIDER = "claude"          # Parse schedule into structured JSON
# LEVEL_INFO_LLM_PROVIDER = "claude"        # Extract level info from full screen
# SERVICE_LIST_LLM_PROVIDER = "claude"      # Read service list from page screenshots
# SERVICE_LOCATE_LLM_PROVIDER = "claude"    # Find service when computed coordinates fail
# VERIFY_TIMES_LLM_PROVIDER = "claude"      # Verify service start_time/duration


TRAIN_COUNT_LLM_PROVIDER = "claude"       # Count trains on screen
SCHEDULE_LLM_PROVIDER = "claude"          # Parse schedule into structured JSON  The local host is halucinating way too much on this
LEVEL_INFO_LLM_PROVIDER = "claude"        # Extract level info from full screen
SERVICE_LIST_LLM_PROVIDER = "claude"      # Read service list from page screenshots
SERVICE_LOCATE_LLM_PROVIDER = "claude"    # Find service when computed coordinates fail
VERIFY_TIMES_LLM_PROVIDER = "claude"      # Verify service start_time/duration


# Limits (None = unlimited)
MAX_TRAINS_PER_CLASS = None   # cap trains per train for faster testing
MAX_SERVICES_PER_TRAIN = None   # cap services per train for faster testing

# Retry settings (when a click doesn't register and the screen doesn't change)
RETRY_MAX = 3                # total click attempts before giving up
RETRY_WAIT = 30              # seconds to wait for screen change before retrying click

# Auto-restart settings
MAX_RETRIES_PER_SERVICE = 3  # max restart attempts for the same train+service
BATCH_SIZE = 85              # restart game after this many services (None = no restart)

################################################################################
# Calibration override — load values from calibration.json if it exists
################################################################################

_CALIBRATION_KEYS = [
    "EXTRA_SECTION_TILE_X", "EXTRA_SECTION_TILE_Y",
    "SERVICE_LIST_LEFT", "SERVICE_LIST_TOP", "SERVICE_LIST_RIGHT", "SERVICE_LIST_BOTTOM",
    "SERVICE_BOX_HEIGHT", "SERVICE_BOX_STRIDE", "SCROLL_PER_BOX",
    "FIRST_BOX_TOP", "BOXES_PER_PAGE",
    "FIRST_CLICK_X", "FIRST_CLICK_Y",
    "SCHEDULE_LEFT", "SCHEDULE_TOP", "SCHEDULE_RIGHT", "SCHEDULE_BOTTOM",
    "TRAIN_BOX_LEFT", "TRAIN_BOX_TOP", "TRAIN_BOX_WIDTH", "TRAIN_BOX_HEIGHT",
    "TRAIN_SCROLL_PER_BOX", "TRAIN_VISIBLE_COUNT",
    "TRAIN_FIRST_Y_OFFSET", "TRAIN_BOX_STRIDE",
    "LEVEL_SCREEN_LEFT", "LEVEL_SCREEN_TOP",
    "LEVEL_SCREEN_RIGHT", "LEVEL_SCREEN_BOTTOM",
]

_CALIBRATION_TUPLE_KEYS = [
    "GAME_REGION",
    "LEVEL_SCREEN_REGION",
]

# Coordinate keys that are absolute X positions (offset by game window X)
_X_COORD_KEYS = {
    "SERVICE_LIST_LEFT", "SERVICE_LIST_RIGHT",
    "SCHEDULE_LEFT", "SCHEDULE_RIGHT",
    "TRAIN_BOX_LEFT",
    "LEVEL_SCREEN_LEFT", "LEVEL_SCREEN_RIGHT",
    "EXTRA_SECTION_TILE_X",
}

# Coordinate keys that are absolute Y positions (offset by game window Y)
_Y_COORD_KEYS = {
    "SERVICE_LIST_TOP", "SERVICE_LIST_BOTTOM",
    "SCHEDULE_TOP", "SCHEDULE_BOTTOM",
    "TRAIN_BOX_TOP",
    "LEVEL_SCREEN_TOP", "LEVEL_SCREEN_BOTTOM",
    "EXTRA_SECTION_TILE_Y",
}


def _detect_game_window():
    """Detect TSW game window position. Returns (x, y, w, h) or None."""
    try:
        import ctypes
        from ctypes import wintypes

        user32 = ctypes.windll.user32
        WNDENUMPROC = ctypes.WINFUNCTYPE(
            ctypes.c_bool, wintypes.HWND, wintypes.LPARAM)

        results = []

        def callback(hwnd, lparam):
            if user32.IsWindowVisible(hwnd):
                length = user32.GetWindowTextLengthW(hwnd)
                if length > 0:
                    buf = ctypes.create_unicode_buffer(length + 1)
                    user32.GetWindowTextW(hwnd, buf, length + 1)
                    title = buf.value
                    if 'train sim world' in title.lower():
                        rect = wintypes.RECT()
                        user32.GetWindowRect(hwnd, ctypes.byref(rect))
                        x = rect.left
                        y = rect.top
                        w = rect.right - rect.left
                        h = rect.bottom - rect.top
                        # Clamp negative values from invisible window borders
                        if y < 0:
                            h += y
                            y = 0
                        if x < 0:
                            w += x
                            x = 0
                        results.append((x, y, w, h))
            return True

        user32.EnumWindows(WNDENUMPROC(callback), 0)
        if results:
            return results[0]
    except Exception:
        pass
    return None


def _migrate_to_relative(cal, path):
    """One-time migration: convert absolute coords to relative and add version marker."""
    gx, gy = cal.get("GAME_REGION", [0, 0, 1920, 1080])[:2]
    for key in list(cal.keys()):
        if key in _X_COORD_KEYS:
            cal[key] -= gx
        elif key in _Y_COORD_KEYS:
            cal[key] -= gy
    cal["_version"] = 2
    import shutil
    backup = path + ".bak"
    shutil.copy2(path, backup)
    with open(path, "w") as f:
        json.dump(cal, f, indent=2)
    print(f"[config] Migrated calibration to relative coords (backup: {backup})")


if os.path.isfile(CALIBRATION_FILE):
    with open(CALIBRATION_FILE, "r") as _f:
        _cal = json.load(_f)

    _is_relative = _cal.get("_version", 1) >= 2

    # Determine game window offset for resolving relative coords
    if _is_relative:
        _detected = _detect_game_window()
        _live_game_region = None
        if _detected:
            _gx, _gy = _detected[0], _detected[1]
            _live_game_region = _detected
            GAME_REGION = _detected
            print(f"[config] Live game window at ({_gx}, {_gy}, {GAME_REGION[2]}, {GAME_REGION[3]})")
        else:
            _stored = _cal.get("GAME_REGION", [0, 0, 1920, 1080])
            _gx, _gy = _stored[0], _stored[1]
            GAME_REGION = tuple(_stored)
            print(f"[config] Game not running, using stored origin ({_gx}, {_gy})")
    else:
        _gx, _gy = 0, 0  # legacy absolute — no offset needed

    _g = globals()
    for _key in _CALIBRATION_KEYS:
        if _key in _cal:
            _val = _cal[_key]
            if _is_relative:
                if _key in _X_COORD_KEYS:
                    _val += _gx
                elif _key in _Y_COORD_KEYS:
                    _val += _gy
            _g[_key] = _val
    for _key in _CALIBRATION_TUPLE_KEYS:
        if _key in _cal:
            _g[_key] = tuple(_cal[_key])
    # In relative mode, restore the detected/stored GAME_REGION
    # (the tuple keys loop above may have overwritten it with the raw calibration value)
    if _is_relative:
        if _live_game_region is not None:
            _g["GAME_REGION"] = _live_game_region
        else:
            _g["GAME_REGION"] = GAME_REGION

    # Recompute derived values
    BOXES_TO_SCROLL = BOXES_PER_PAGE - OVERLAP_BOXES
    SCROLL_AMOUNT = SCROLL_PER_BOX * BOXES_TO_SCROLL
    LEVEL_SCREEN_REGION = (LEVEL_SCREEN_LEFT, LEVEL_SCREEN_TOP,
                           LEVEL_SCREEN_RIGHT - LEVEL_SCREEN_LEFT,
                           LEVEL_SCREEN_BOTTOM - LEVEL_SCREEN_TOP)

    # Migrate: if CLICK_REGISTRY still in calibration.json, move it to its own file
    if "CLICK_REGISTRY" in _cal:
        if not os.path.isfile(CLICK_REGISTRY_FILE):
            _reg_data = {"_format": "relative_to_game_window", "entries": _cal["CLICK_REGISTRY"]}
            with open(CLICK_REGISTRY_FILE, "w") as _rf:
                json.dump(_reg_data, _rf, indent=2)
            print(f"[config] Migrated CLICK_REGISTRY from calibration.json to {CLICK_REGISTRY_FILE}")
        del _cal["CLICK_REGISTRY"]
        with open(CALIBRATION_FILE, "w") as _cf:
            json.dump(_cal, _cf, indent=2)

    # Load CLICK_REGISTRY from click_registry.json
    if os.path.isfile(CLICK_REGISTRY_FILE):
        with open(CLICK_REGISTRY_FILE, "r") as _rf:
            _reg_data = json.load(_rf)
        _reg_entries = _reg_data.get("entries", _reg_data)

        # Determine scale factors if source resolution differs from current
        _source_region = _reg_data.get("source_game_region")
        if _source_region:
            _src_w, _src_h = _source_region[2], _source_region[3]
            _cur_w, _cur_h = GAME_REGION[2], GAME_REGION[3]
            _scale_x = _cur_w / _src_w
            _scale_y = _cur_h / _src_h
            _needs_scale = abs(_scale_x - 1.0) > 0.01 or abs(_scale_y - 1.0) > 0.01
            if _needs_scale:
                print(f"[config] Scaling CLICK_REGISTRY: {_src_w}x{_src_h} -> {_cur_w}x{_cur_h} "
                      f"(x{_scale_x:.3f}, y{_scale_y:.3f})")
        else:
            _needs_scale = False

        CLICK_REGISTRY = {}
        for k, v in _reg_entries.items():
            entry = dict(v)
            cx = v["click_x"]
            cy = v["click_y"]
            # Scale from source resolution to current resolution
            if _needs_scale:
                cx = int(round(cx * _scale_x))
                cy = int(round(cy * _scale_y))
            # Apply game window offset (coords are relative to game window)
            if _is_relative:
                cx += _gx
                cy += _gy
            entry["click_x"] = cx
            entry["click_y"] = cy
            CLICK_REGISTRY[int(k)] = entry
        print(f"[config] Loaded CLICK_REGISTRY from click_registry.json ({len(CLICK_REGISTRY)} entries)")

    # One-time migration from absolute to relative
    if not _is_relative:
        _migrate_to_relative(_cal, CALIBRATION_FILE)

    print(f"[config] Loaded calibration from {CALIBRATION_FILE}")

# ── Auto-build CLICK_REGISTRY from Bakerloo CSV (standard of truth) ──────
# If calibration.json already has entries they take priority (written by
# calibrate_service_scroll.py with user-verified positions). Bakerloo CSV
# entries fill in anything not already present.
if os.path.isfile(_REFERENCE_CSV):
    import csv as _csv, re as _re
    _ref_count = 0
    with open(_REFERENCE_CSV, "r", newline="") as _rf:
        for _row in _csv.DictReader(_rf):
            _num = int(_row["number"])
            if _num in CLICK_REGISTRY:
                continue  # calibration entry takes priority
            _m = _re.match(r"click \((\d+)\s+(\d+)\)", _row["click"])
            if not _m:
                continue
            _page = int(_re.search(r"\d+", _row["page"]).group())
            CLICK_REGISTRY[_num] = {
                "page": _page,
                "click_x": int(_m.group(1)),
                "click_y": int(_m.group(2)),
            }
            _ref_count += 1
    if _ref_count:
        print(f"[config] Auto-built {_ref_count} CLICK_REGISTRY entries from Bakerloo CSV "
              f"({len(CLICK_REGISTRY)} total)")
