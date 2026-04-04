"""Read the TSW CommAPI key and fetch player location from the game API.

Uses the same configuration.json as the Node.js server to find the
CommAPIKey.txt file path, then makes a single GET request to the
TSW CommAPI to retrieve the player's current latitude and longitude.
"""

import json
import os
import urllib.request
import urllib.error

TSW_API_PORT = 31270
TSW_API_URL = f"http://localhost:{TSW_API_PORT}"

# Path to the shared configuration.json (one level up from bot/)
_CONFIG_PATH = os.path.join(os.path.dirname(os.path.abspath(__file__)), "..", "configuration.json")

# Default CommAPIKey.txt paths (same as utils/apiKey.js)
_USERS_FOLDER = os.environ.get("USERPROFILE", "")
_DEFAULT_TSW5_PATH = os.path.join(_USERS_FOLDER, "Documents", "My Games", "TrainSimWorld5", "Saved", "Config", "CommAPIKey.txt")
_DEFAULT_TSW6_PATH = os.path.join(_USERS_FOLDER, "Documents", "My Games", "TrainSimWorld6", "Saved", "Config", "CommAPIKey.txt")


def _load_config():
    """Load configuration.json."""
    try:
        with open(_CONFIG_PATH, "r", encoding="utf-8") as f:
            return json.load(f)
    except (FileNotFoundError, json.JSONDecodeError):
        return {}


def get_api_key():
    """Read the CommAPI key using the same logic as the Node.js server.

    Priority:
    1. Direct apiKey in configuration.json
    2. CommAPIKey.txt at the path for the selected TSW version
    """
    config = _load_config()

    # Direct key override
    direct_key = (config.get("apiKey") or "").strip()
    if direct_key:
        return direct_key

    # Determine path based on TSW version
    tsw_version = config.get("tswVersion", "tsw6")
    if tsw_version == "tsw5":
        key_path = (config.get("tsw5KeyPath") or "").strip() or _DEFAULT_TSW5_PATH
    else:
        key_path = (config.get("tsw6KeyPath") or "").strip() or _DEFAULT_TSW6_PATH

    try:
        with open(key_path, "r", encoding="utf-8") as f:
            return f.read().strip()
    except FileNotFoundError:
        print(f"  [tsw_api] CommAPIKey.txt not found at: {key_path}")
        return None


def get_player_location():
    """Fetch the player's current lat/lng and currentServiceName from the TSW CommAPI.

    Returns:
        tuple (latitude, longitude, current_service_name) or (None, None, None) on failure.
    """
    api_key = get_api_key()
    if not api_key:
        print("  [tsw_api] No API key available — cannot fetch player location.")
        return None, None, None

    url = f"{TSW_API_URL}/get/DriverAid.PlayerInfo"
    req = urllib.request.Request(url, method="GET", headers={"DTGCommKey": api_key})

    try:
        with urllib.request.urlopen(req, timeout=10) as resp:
            data = json.loads(resp.read().decode())
    except (urllib.error.URLError, urllib.error.HTTPError, TimeoutError,
            ConnectionError, OSError) as e:
        print(f"  [tsw_api] Failed to fetch player location: {e}")
        return None, None, None

    if data.get("Result") != "Success":
        print(f"  [tsw_api] API returned non-success: {data.get('Result')}")
        return None, None, None

    values = data.get("Values", {})
    geo = values.get("geoLocation", {})
    lat = geo.get("latitude")
    lng = geo.get("longitude")
    current_service_name = values.get("currentServiceName")

    if lat is not None and lng is not None:
        print(f"  [tsw_api] Player location: {lat}, {lng} | service: {current_service_name}")
        return lat, lng, current_service_name

    print("  [tsw_api] No geoLocation in API response.")
    return None, None, current_service_name
