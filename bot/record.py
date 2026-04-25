"""New method — LLM-assisted train counting, service discovery, and level info extraction.

Flow:
1. Send screenshot to local LLM to count trains in a class
2. For each train, capture service list pages and send to Claude API
   to build a complete service CSV (name, time, duration, click coordinates)
3. Loop through each service in the CSV:
   - Scroll to the correct page and click the service
   - Capture service name, tonnage, car count, train length from the level screen
   - Send level screen to local LLM for data extraction
   - Capture the schedule
   - Upload everything to create a timetable entry
4. Cycle to next service, then next train

Usage:
    python new_method.py                              # full run
    python new_method.py --no-launch                  # skip game launch (already at service list)
    python new_method.py "Class Name" <train> <svc>   # resume from specific point
"""

import atexit
import base64
import csv
import json
import os
import re
import shutil
import subprocess
import sys
import time
from datetime import datetime


# ── Run report ────────────────────────────────────────────────────────
class RunReport:
    """Structured run report written incrementally to bot/run_report.txt.

    On resume, detects that the previous run was interrupted (no RUN COMPLETE
    marker) and appends a RESUMED section instead of a fresh header.
    """

    _COMPLETE_MARKER = "RUN COMPLETE"

    def __init__(self):
        self._path = os.path.join(os.path.dirname(os.path.abspath(__file__)), "run_report.txt")
        self._route_start = None
        self._route_mark = None
        self._tt_start = None
        self._tt_mark = None

    def _write(self, text):
        with open(self._path, "a", encoding="utf-8") as f:
            f.write(text)

    def _ts(self, t=None):
        return datetime.fromtimestamp(t or time.time()).strftime("%Y-%m-%d %H:%M:%S")

    def _duration(self, start):
        secs = int(time.time() - start)
        h, m, s = secs // 3600, secs % 3600 // 60, secs % 60
        return f"{h}h {m:02d}m {s:02d}s" if h else f"{m}m {s:02d}s"

    def _is_previous_run_incomplete(self):
        """Check if the report file exists and has no RUN COMPLETE marker."""
        if not os.path.isfile(self._path):
            return False
        try:
            with open(self._path, "r", encoding="utf-8") as f:
                content = f.read()
            return bool(content.strip()) and self._COMPLETE_MARKER not in content
        except OSError:
            return False

    # ── run level ──
    def run_start(self, route_names):
        if self._is_previous_run_incomplete():
            self._write(f"\n--- RESUMED — {self._ts()} ---\n\n")
        else:
            self._write(f"{'='*60}\n")
            self._write(f"RUN REPORT — {self._ts()}\n")
            self._write(f"Routes queued: {len(route_names)}\n")
            for i, name in enumerate(route_names, 1):
                self._write(f"  {i}. {name}\n")
            self._write(f"{'='*60}\n\n")

    def run_end(self, completed, skipped):
        self._write(f"\n{'='*60}\n")
        self._write(f"{self._COMPLETE_MARKER} — {self._ts()}\n")
        self._write(f"  Completed: {len(completed)}  |  Skipped: {len(skipped)}\n")
        if skipped:
            self._write(f"  Skipped: {', '.join(skipped)}\n")
        self._write(f"{'='*60}\n\n")

    def run_stopped(self):
        self._write(f"\n*** Stopped by user — {self._ts()} ***\n")

    # ── route level ──
    def route_start(self, route_name, idx, total):
        self._route_start = time.time()
        self._route_mark = f"_run_report_route_{route_name}_{time.time()}"
        cost_tracker.mark(self._route_mark)
        self._write(f"\nRoute {idx}/{total}: {route_name}\n")
        self._write(f"  Started:  {self._ts()}\n")

    def route_end(self, route_name):
        entries = cost_tracker.entries_since_mark(self._route_mark)
        total_in = sum(e.input_tokens for e in entries)
        total_out = sum(e.output_tokens for e in entries)
        total_cost = sum(e.cost for e in entries)
        self._write(f"  Finished: {self._ts()}\n")
        self._write(f"  Duration: {self._duration(self._route_start)}\n")
        self._write(f"  Claude cost: ${total_cost:.4f}  "
                    f"(calls: {len(entries)}, "
                    f"in: {total_in:,} tokens, out: {total_out:,} tokens)\n")

    def route_skipped(self, route_name, reason):
        self._write(f"  *** SKIPPED: {reason}\n")

    # ── timetable (train class) level ──
    def timetable_start(self, train_name, choice=None):
        self._tt_start = time.time()
        self._tt_mark = f"_run_report_{train_name}_{time.time()}"
        cost_tracker.mark(self._tt_mark)
        label = f"{train_name}"
        if choice:
            label += f" [{choice}]"
        self._write(f"\n    Timetable: {label}\n")
        self._write(f"      Started:  {self._ts()}\n")

    def timetable_trains(self, train_name, total_count, filtered_count):
        self._write(f"      Trains:   {filtered_count}/{total_count}\n")

    def timetable_service_start(self, service_index):
        self._svc_start = time.time()
        self._write(f"\n      --- Service #{service_index} ---\n")
        self._write(f"        Started:  {self._ts()}\n")

    def timetable_service_current(self, current_service_name):
        self._write(f"        currentServiceName: {current_service_name}\n")

    def timetable_service_end(self, service_index, service_name):
        """Write per-service summary after the service is processed."""
        self._write(f"        Finished: {self._ts()}\n")
        self._write(f"        Duration: {self._duration(self._svc_start)}\n")
        if service_name:
            self._write(f"        Service:  {service_name}\n")
        mark = f"service_{service_index}"
        entries = cost_tracker.entries_since_mark(mark)
        if entries:
            total_in = sum(e.input_tokens for e in entries)
            total_out = sum(e.output_tokens for e in entries)
            total_cost = sum(e.cost for e in entries)
            self._write(f"        Cost:     ${total_cost:.4f}  "
                        f"(calls: {len(entries)}, "
                        f"in: {total_in:,}, out: {total_out:,})\n")

    def timetable_end(self, train_name):
        entries = cost_tracker.entries_since_mark(self._tt_mark)
        total_in = sum(e.input_tokens for e in entries)
        total_out = sum(e.output_tokens for e in entries)
        total_cost = sum(e.cost for e in entries)
        self._write(f"      Finished: {self._ts()}\n")
        self._write(f"      Duration: {self._duration(self._tt_start)}\n")
        self._write(f"      Claude cost: ${total_cost:.4f}  "
                    f"(calls: {len(entries)}, "
                    f"in: {total_in:,} tokens, out: {total_out:,} tokens)\n")
        if entries:
            for e in entries:
                self._write(f"        {e.task:<20s}  "
                            f"in:{e.input_tokens:>7,}  out:{e.output_tokens:>6,}  "
                            f"${e.cost:.4f}\n")

run_report = RunReport()


def safe_dirname(name):
    """Replace characters that are invalid in Windows directory names."""
    # Windows forbids: \ / : * ? " < > |
    return re.sub(r'[\\/:*?"<>|]', '_', name)

import anthropic
import numpy as np
import pyautogui
from PIL import Image

pyautogui.FAILSAFE = False

import config
from navigator import (
    launch_game,
    pass_warning_screen,
    pass_splash_screen,
    click_to_the_trains,
    click_choose_a_route,
    select_route,
    click_timetable,
    select_extra_section,
    select_train,
    click_train,
    exit_game,
    kill_game,
    position_game_window,
)
from schedule_capture import capture_schedule
from uploader import (
    check_server,
    resolve_ids,
    get_trains,
    get_section_trains,
    upload_service,
    upload_train_consist,
    reset_route_cache,
)
from claude_cost_tracker import cost_tracker
from service_loop_scroll_method import (
    ServiceError,
    BatchRestart,
    scroll_service_list_to_top,
    scroll_service_list_down,
    screenshot_service_box,
    wait_for_level_load,
    exit_to_main_menu,
    get_visible_service_boxes,
)

# ── LLM settings (from config) ──────────────────────────────────────
LM_STUDIO_URL = config.LOCAL_LLM_URL
VISION_MODEL = config.LOCAL_LLM_MODEL
CLAUDE_MODEL = config.CLAUDE_MODEL

# Game screen region from config
GAME_REGION = config.GAME_REGION

# Progress file
PROGRESS_FILE = os.path.join(config.BASE_DIR, "progress_new_method.json")

# Captured services registry filename (stored per train class directory)
CAPTURED_SERVICES_FILENAME = "captured_services.json"

# Max services per train (safety cap)
MAX_SERVICES = getattr(config, "MAX_SCROLL_SERVICES", 250)


# ═══════════════════════════════════════════════════════════════════════
# LLM helpers
# ═══════════════════════════════════════════════════════════════════════

def _encode_image(image_path):
    """Read an image file and return base64 string."""
    with open(image_path, "rb") as f:
        return base64.standard_b64encode(f.read()).decode("utf-8")


def _encode_pil_image(pil_img):
    """Encode a PIL image to base64 PNG string."""
    import io
    buf = io.BytesIO()
    pil_img.save(buf, format="PNG")
    return base64.standard_b64encode(buf.getvalue()).decode("utf-8")


def _ask_local_llm(image_base64, prompt, max_tokens=1024):
    """Send an image + prompt to the local LM Studio vision model.

    Returns the response text string.
    """
    import urllib.request
    import urllib.error

    url = f"{LM_STUDIO_URL}/v1/chat/completions"
    body = {
        "model": VISION_MODEL,
        "messages": [{
            "role": "user",
            "content": [
                {
                    "type": "image_url",
                    "image_url": {
                        "url": f"data:image/png;base64,{image_base64}",
                    },
                },
                {"type": "text", "text": prompt},
            ],
        }],
        "max_tokens": max_tokens,
        "temperature": 0.1,
    }

    data = json.dumps(body).encode()
    req = urllib.request.Request(
        url, data=data, method="POST",
        headers={"Content-Type": "application/json"},
    )

    try:
        with urllib.request.urlopen(req, timeout=120) as resp:
            result = json.loads(resp.read().decode())
    except urllib.error.HTTPError as e:
        error_body = ""
        try:
            error_body = e.read().decode(errors="replace")
        except Exception:
            pass
        print(f"       LLM HTTP {e.code}: {error_body}")
        if "usage limit" in error_body.lower():
            print(f"\n       FATAL: API usage limit reached. Stopping data collection.")
            raise SystemExit(1)
        raise

    return result["choices"][0]["message"]["content"]


def _parse_json_response(text):
    """Extract JSON from an LLM response that might have markdown fences or preamble."""
    cleaned = text.strip()
    # Strip markdown code fences
    if cleaned.startswith("```"):
        cleaned = cleaned.split("\n", 1)[1]
        cleaned = cleaned.rsplit("```", 1)[0]
    # Strip preamble text before the first '[' or '{' (model sometimes adds commentary)
    bracket_pos = cleaned.find("[")
    brace_pos = cleaned.find("{")
    if bracket_pos >= 0 and (brace_pos < 0 or bracket_pos < brace_pos):
        if bracket_pos > 0:
            cleaned = cleaned[bracket_pos:]
    elif brace_pos > 0:
        cleaned = cleaned[brace_pos:]
    try:
        return json.loads(cleaned)
    except json.JSONDecodeError:
        # Try extracting just the first complete JSON object (handles extra trailing data)
        try:
            obj, _ = json.JSONDecoder().raw_decode(cleaned)
            return obj
        except json.JSONDecodeError:
            pass
        import re
        fixed = re.sub(r'(?<=[\s,{])(\w+)":', r'"\1":', cleaned)
        try:
            return json.loads(fixed)
        except json.JSONDecodeError:
            try:
                obj, _ = json.JSONDecoder().raw_decode(fixed)
                return obj
            except json.JSONDecodeError:
                pass
            print(f"       ERROR: Could not parse LLM response as JSON.")
            print(f"       Raw response ({len(text)} chars): {text[:500]}")
            raise


def call_llm_json(task, image_paths, prompt, max_tokens=4096, retries=3):
    """Call LLM and parse response as JSON, retrying on non-JSON responses."""
    last_exc = None
    for attempt in range(retries):
        response_text = call_llm(task, image_paths, prompt, max_tokens=max_tokens)
        try:
            parsed = _parse_json_response(response_text)
            return parsed, response_text
        except Exception as e:
            last_exc = e
            if attempt < retries - 1:
                print(f"       [{task}] LLM returned non-JSON (attempt {attempt+1}/{retries}), retrying...")
                time.sleep(2)
            else:
                print(f"       [{task}] All {retries} attempts returned non-JSON, giving up.")
                raise last_exc


# Provider config attribute for each LLM task
_LLM_TASK_PROVIDERS = {
    "train_count":    "TRAIN_COUNT_LLM_PROVIDER",
    "schedule":       "SCHEDULE_LLM_PROVIDER",
    "level_info":     "LEVEL_INFO_LLM_PROVIDER",
    "service_list":   "SERVICE_LIST_LLM_PROVIDER",
    "service_locate": "SERVICE_LOCATE_LLM_PROVIDER",
    "verify_times":   "VERIFY_TIMES_LLM_PROVIDER",
}


def call_llm(task, image_paths, prompt, max_tokens=2048):
    """Unified LLM call. Routes to claude or local based on config.

    Args:
        task: one of "train_count", "schedule", "level_info",
              "service_list", "service_locate", "verify_times"
        image_paths: str (single file path) or list of str (file paths)
        prompt: the prompt string
        max_tokens: max response tokens

    Returns response_text string.
    Raises SystemExit(1) if API usage limit is reached.
    """
    if isinstance(image_paths, str):
        image_paths = [image_paths]

    provider_attr = _LLM_TASK_PROVIDERS[task]
    provider = getattr(config, provider_attr, "claude")

    try:
        if provider == "claude":
            if not os.environ.get("ANTHROPIC_API_KEY"):
                raise RuntimeError(f"ANTHROPIC_API_KEY not set (task={task})")
            client = anthropic.Anthropic()
            if len(image_paths) > 1:
                response_text, usage = _ask_claude_multi_image(
                    client, image_paths, prompt, max_tokens=max_tokens)
            else:
                response_text, usage = _ask_claude(
                    client, image_paths[0], prompt, max_tokens=max_tokens)
            entry = cost_tracker.record(task, usage)
            print(f"       [{task}] tokens: {usage.input_tokens}+{usage.output_tokens} "
                  f"(${entry.cost:.4f})")
            return response_text
        else:  # local
            img_path = image_paths[0]
            file_size = os.path.getsize(img_path)
            try:
                with Image.open(img_path) as img:
                    img_w, img_h = img.size
                    img_mode = img.mode
                print(f"       [{task}] local LLM image: {img_path}")
                print(f"       [{task}] dimensions: {img_w}x{img_h}, mode: {img_mode}, "
                      f"file size: {file_size:,} bytes")
            except Exception as img_err:
                print(f"       [{task}] could not read image metadata: {img_err}")
            b64 = _encode_image(img_path)
            print(f"       [{task}] base64 payload: {len(b64):,} chars")
            return _ask_local_llm(b64, prompt, max_tokens)

    except SystemExit:
        raise  # re-raise usage-limit exits from inner helpers
    except Exception as e:
        if "usage limit" in str(e).lower():
            print(f"\n       FATAL: API usage limit reached. Stopping data collection.")
            raise SystemExit(1)
        raise


def _ask_claude(client, image_path, prompt, max_tokens=2048, max_retries=5):
    """Send an image + prompt to Claude API. Returns (response_text, usage)."""
    with open(image_path, "rb") as f:
        image_data = base64.standard_b64encode(f.read()).decode("utf-8")

    for attempt in range(max_retries):
        try:
            message = client.messages.create(
                model=CLAUDE_MODEL,
                max_tokens=max_tokens,
                messages=[{
                    "role": "user",
                    "content": [
                        {
                            "type": "image",
                            "source": {
                                "type": "base64",
                                "media_type": "image/png",
                                "data": image_data,
                            },
                        },
                        {"type": "text", "text": prompt},
                    ],
                }],
            )
            return message.content[0].text, message.usage

        except anthropic.RateLimitError:
            wait = min(2 ** attempt * 5, 60)
            print(f"  Rate limited (attempt {attempt + 1}/{max_retries}), waiting {wait}s...")
            time.sleep(wait)
            if attempt == max_retries - 1:
                raise

        except anthropic.APIStatusError as e:
            print(f"       API error {e.status_code}: {e.message}")
            if "usage limit" in str(e).lower():
                print(f"\n       FATAL: API usage limit reached. Stopping data collection.")
                raise SystemExit(1)
            if e.status_code >= 500:
                wait = min(2 ** attempt * 3, 30)
                print(f"  Server error {e.status_code} (attempt {attempt + 1}/{max_retries}), waiting {wait}s...")
                time.sleep(wait)
                if attempt == max_retries - 1:
                    raise
            else:
                raise


# ═══════════════════════════════════════════════════════════════════════
# Train counting via LLM
# ═══════════════════════════════════════════════════════════════════════

TRAIN_COUNT_PROMPT = (
    "This is a screenshot of a train selection panel from a train simulation game. "
    "Each train appears as a rectangular card/tile stacked vertically in a scrollable list. "
    "Count how many individual train entries are visible in this screenshot. "
    "Each entry typically shows a train image or icon with text below/beside it.\n\n"
    "Respond ONLY with valid JSON:\n"
    '{"train_count": <number>}\n'
    "No other text."
)

def count_trains_llm():
    """Screenshot the train box area and ask the LLM how many trains are visible.

    Scrolls through the train list to count all trains (visible + scrolled).
    Returns the total train count.
    """
    print(f"       Counting trains via LLM ({config.TRAIN_COUNT_LLM_PROVIDER})...")

    region = (config.TRAIN_BOX_LEFT, config.TRAIN_BOX_TOP,
              config.TRAIN_BOX_WIDTH, config.TRAIN_BOX_HEIGHT)
    scroll_x = config.TRAIN_BOX_LEFT + config.TRAIN_BOX_WIDTH // 2
    scroll_y = config.TRAIN_BOX_TOP + config.TRAIN_BOX_HEIGHT // 2

    # Scroll to the very top first
    pyautogui.moveTo(scroll_x, scroll_y)
    time.sleep(0.3)
    pyautogui.scroll(-config.TRAIN_SCROLL_PER_BOX * 30)
    time.sleep(1.5)

    # Capture first frame and ask LLM
    first_img = pyautogui.screenshot(region=region)
    debug_path = os.path.join(config.SCREENSHOTS_DIR, "_train_count_debug.png")
    first_img.save(debug_path)
    result, _ = call_llm_json("train_count", debug_path, TRAIN_COUNT_PROMPT)
    visible_count = result.get("train_count", 0)
    print(f"       LLM says {visible_count} trains visible in first frame")

    # Always attempt scrolling to verify — LLM visible count can be wrong.
    # Scroll down one box at a time until list stops moving.
    prev_arr = np.array(first_img)
    scrolls = 0
    for _ in range(30):
        pyautogui.moveTo(scroll_x, scroll_y)
        time.sleep(0.3)
        pyautogui.scroll(config.TRAIN_SCROLL_PER_BOX)
        time.sleep(1.0)
        curr_arr = np.array(pyautogui.screenshot(region=region))
        if curr_arr.shape == prev_arr.shape:
            diff = np.mean(np.abs(curr_arr.astype(float) - prev_arr.astype(float)))
            if diff < 5.0:
                break
        scrolls += 1
        prev_arr = curr_arr

    if scrolls > 0:
        # LLM may undercount visible trains; use TRAIN_VISIBLE_COUNT as the
        # known screen capacity when scrolling reveals additional trains.
        total = max(visible_count, config.TRAIN_VISIBLE_COUNT) + scrolls
    else:
        total = visible_count

    # Scroll back to top
    if scrolls > 0:
        pyautogui.moveTo(scroll_x, scroll_y)
        time.sleep(0.3)
        pyautogui.scroll(-config.TRAIN_SCROLL_PER_BOX * 30)
        time.sleep(1.5)

    print(f"       Total trains: {total} ({visible_count} visible + {scrolls} scrolled)")
    return total


# ═══════════════════════════════════════════════════════════════════════
# Level screen extraction via LLM
# ═══════════════════════════════════════════════════════════════════════

# x offset in 1_service.png where the time region starts
SERVICE_TIME_CROP_X = 429

LLM_DATA_FILE = "llm_data.json"


def _load_llm_data(service_dir):
    """Load the consolidated llm_data.json, or return empty dict."""
    path = os.path.join(service_dir, LLM_DATA_FILE)
    if os.path.isfile(path):
        with open(path, "r", encoding="utf-8") as f:
            return json.load(f)
    return {}


def _save_llm_data(service_dir, key, value):
    """Save a key into the consolidated llm_data.json (merge with existing)."""
    data = _load_llm_data(service_dir)
    data[key] = value
    path = os.path.join(service_dir, LLM_DATA_FILE)
    with open(path, "w", encoding="utf-8") as f:
        json.dump(data, f, indent=2)


# ═══════════════════════════════════════════════════════════════════════
# LLM schedule extraction → structured JSON (replaces OCR)
# ═══════════════════════════════════════════════════════════════════════

SCHEDULE_JSON_PROMPT = (
    "You are given one or more screenshots of the SAME train schedule/timetable from a "
    "train simulation game. The images may overlap — they were captured by scrolling down "
    "the schedule. Piece them together into ONE complete, deduplicated timetable.\n\n"
    "Each row in the schedule has 4 columns:\n"
    "- ACTION (bold text, left): e.g. WAIT FOR SERVICE, LOAD PASSENGERS, STOP AT LOCATION, "
    "DRIVE TO, GO VIA LOCATION, UNLOAD PASSENGERS, COUPLE, DECOUPLE, CHANGE ENDS, WAIT, etc.\n"
    "- DETAILS (middle text): description or instruction text, e.g. 'Service is scheduled to "
    "start at 06:26:00', 'Milton Keynes Central Platform 6 - 07:02:30'\n"
    "- TIME1 (right column): a time value like 6:28:00 or empty (-)\n"
    "- TIME2 (far right column): a time value like 6:26:00 or empty (-)\n\n"
    "For each row extract:\n"
    "- action: the action text exactly as shown (uppercase)\n"
    "- details: the full details/description text from the second column\n"
    "- location: the station or location name WITHOUT platform/track info "
    "(e.g. 'Milton Keynes Central', 'Glendale'). "
    "Extract this from the details column. Leave empty if no location.\n"
    "- structure: either 'Platform' or 'Track' depending on what the details text shows. "
    "For example if details says 'Edge Hill Platform 4', structure is 'Platform'. "
    "If details says 'Glendale Track 1', structure is 'Track'. "
    "ALWAYS extract this when the details contain a platform or track reference. Leave empty only if there is no platform or track mentioned at all.\n"
    "- structure_number: the platform or track number/identifier only "
    "(e.g. '6', '1', '2A', 'N-1'). For 'Edge Hill Platform 4' this would be '4'. "
    "ALWAYS extract this when the details contain a platform or track reference. Leave empty only if there is no platform or track mentioned at all.\n"
    "- time1: time value WITHOUT the '+' prefix. Use empty string if '-' or blank\n"
    "- time2: time value WITHOUT the '+' prefix. Use empty string if '-' or blank\n\n"
    "IMPORTANT:\n"
    "- Remove '+' from all time values (e.g. '+06:28:00' → '6:28:00')\n"
    "- Use empty string (not '-') for missing times\n"
    "- Deduplicate: if the same row appears in multiple images, include it only ONCE\n"
    "- Maintain chronological order\n"
    "- ALWAYS parse platform/track from the details text. If details says 'Edge Hill Platform 4', "
    "then location='Edge Hill', structure='Platform', structure_number='4'. Do NOT leave structure and structure_number empty when a platform or track is present in the details.\n\n"
    "Respond ONLY with a JSON array, one object per row:\n"
    '[{"action": "...", "details": "...", "location": "...", "structure": "...", "structure_number": "...", "time1": "...", "time2": "..."}, ...]\n'
    "No other text."
)


def _ask_claude_multi_image(client, image_paths, prompt, max_tokens=4096, max_retries=5):
    """Send multiple images + prompt to Claude API. Returns (response_text, usage)."""
    content = []
    for img_path in image_paths:
        with open(img_path, "rb") as f:
            image_data = base64.standard_b64encode(f.read()).decode("utf-8")
        content.append({
            "type": "image",
            "source": {
                "type": "base64",
                "media_type": "image/png",
                "data": image_data,
            },
        })
    content.append({"type": "text", "text": prompt})

    for attempt in range(max_retries):
        try:
            message = client.messages.create(
                model=CLAUDE_MODEL,
                max_tokens=max_tokens,
                messages=[{"role": "user", "content": content}],
            )
            return message.content[0].text, message.usage
        except anthropic.RateLimitError:
            wait = min(2 ** attempt * 5, 60)
            print(f"  Rate limited (attempt {attempt + 1}/{max_retries}), waiting {wait}s...")
            time.sleep(wait)
            if attempt == max_retries - 1:
                raise
        except anthropic.APIStatusError as e:
            print(f"       API error {e.status_code}: {e.message}")
            if "usage limit" in str(e).lower():
                print(f"\n       FATAL: API usage limit reached. Stopping data collection.")
                raise SystemExit(1)
            if e.status_code >= 500:
                wait = min(2 ** attempt * 3, 30)
                print(f"  Server error {e.status_code} (attempt {attempt + 1}/{max_retries}), waiting {wait}s...")
                time.sleep(wait)
                if attempt == max_retries - 1:
                    raise
            else:
                raise


def _clean_time(t):
    """Strip '+' prefix and convert '-' to empty string."""
    t = (t or "").strip()
    if t == "-":
        return ""
    if t.startswith("+"):
        t = t[1:]
    return t


def extract_schedule_llm_json(service_dir):
    """Send all schedule frames to LLM in one call and return structured entries as JSON.

    Uses config.SCHEDULE_LLM_PROVIDER to choose between local LM Studio
    and Claude API.  Saves results to llm_data.json in the service dir.

    Returns list of entry dicts (matching the format expected by upload_service):
        [{"action": "...", "details": "...", "location": "...", "time1": "...", "time2": "...", "sort_order": N}, ...]
    """
    import glob as glob_mod

    frame_paths = sorted(glob_mod.glob(os.path.join(service_dir, "3_schedule_frame_*.png")))
    if not frame_paths:
        print("       No schedule frames found for LLM extraction")
        return []

    provider = config.SCHEDULE_LLM_PROVIDER
    print(f"       Sending {len(frame_paths)} schedule frames to LLM ({provider})...")

    all_entries = []

    if provider == "claude":
        # Claude supports multi-image — send all frames in one call
        try:
            response_text = call_llm("schedule", frame_paths, SCHEDULE_JSON_PROMPT,
                                     max_tokens=4096)
            rows = _parse_json_response(response_text)
            if isinstance(rows, list):
                all_entries = rows
                print(f"       {len(rows)} rows extracted")
            else:
                print(f"       Unexpected response type: {type(rows)}")
        except SystemExit:
            raise
        except Exception as e:
            print(f"       Schedule LLM error: {e}")

    else:  # local — send frames one at a time (no multi-image support)
        for i, frame_path in enumerate(frame_paths):
            try:
                response_text = call_llm("schedule", frame_path, SCHEDULE_JSON_PROMPT,
                                         max_tokens=4096)
                rows = _parse_json_response(response_text)
                if isinstance(rows, list):
                    all_entries.extend(rows)
                    print(f"       Frame {i}: {len(rows)} rows extracted")
                else:
                    print(f"       Frame {i}: unexpected response type: {type(rows)}")
            except SystemExit:
                raise
            except Exception as e:
                print(f"       Frame {i}: LLM error: {e}")

    # Normalize fields, clean times, deduplicate
    seen_keys = set()
    entries = []
    for row in all_entries:
        action = (row.get("action") or "").strip().upper()
        details = (row.get("details") or "").strip()
        location = (row.get("location") or "").strip()
        structure = (row.get("structure") or "").strip()
        structure_number = (row.get("structure_number") or "").strip()
        time1 = _clean_time(row.get("time1"))
        time2 = _clean_time(row.get("time2"))

        # Fallback: parse structure/structure_number from details if LLM missed them
        if not structure and not structure_number and details:
            import re
            m = re.search(r'(Platform|Track)\s+(\S+)', details, re.IGNORECASE)
            if m:
                structure = m.group(1).capitalize()
                structure_number = m.group(2)

        # Dedup key: action + time1 + time2 + location
        dedup_key = f"{action}|{time1}|{time2}|{location}"
        if dedup_key in seen_keys:
            print(f"       Dedup: skipping {action} {location} {time1}")
            continue
        seen_keys.add(dedup_key)

        entries.append({
            "action": action,
            "details": details,
            "location": location,
            "structure": structure,
            "structure_number": structure_number,
            "time1": time1,
            "time2": time2,
            "sort_order": len(entries),
        })

    # Save schedule to llm_data.json
    _save_llm_data(service_dir, "schedule", entries)
    print(f"       LLM schedule saved to llm_data.json ({len(entries)} entries)")

    return entries


def crop_service_times(service_dir):
    """Crop the time region from 1_service.png.

    The right portion of 1_service.png (from SERVICE_TIME_CROP_X onward)
    contains two time values (start_time and duration). Saves the crop
    as 2_times.png.
    """
    service_img_path = os.path.join(service_dir, "1_service.png")
    if not os.path.isfile(service_img_path):
        print("       WARNING: 1_service.png not found, cannot extract times")
        return

    img = Image.open(service_img_path)
    w, h = img.size

    time_left = SERVICE_TIME_CROP_X
    if time_left >= w:
        print("       WARNING: 1_service.png too narrow for time extraction")
        return

    time_crop = img.crop((time_left, 0, w, h))
    time_path = os.path.join(service_dir, "2_times.png")
    time_crop.save(time_path)
    print(f"       Saved 2_times.png")


# ═══════════════════════════════════════════════════════════════════════
# Level info extraction from full screen via LLM
# ═══════════════════════════════════════════════════════════════════════

LEVEL_SCREEN_PROMPT = (
    "This is a composite image from a train simulation game.\n\n"
    "The MAIN part is the level loading/splash screen showing:\n"
    "- service_name: large title text at the top\n"
    "- start_time: a time value below the title (HH:MM format)\n"
    "- tonnage: weight in tonnes (number with a lock/weight icon, e.g. 409.1)\n"
    "- car_count: number of cars (integer with a train/car icon, e.g. 5)\n"
    "- train_length: length in meters (number with a ruler/length icon, e.g. 125.2)\n\n"
    "APPENDED at the bottom is a small strip showing TWO time values side by side:\n"
    "- The LEFT time is the start_time (should match the one on the main screen)\n"
    "- The RIGHT time is the duration (this is ONLY in the bottom strip)\n\n"
    "Extract ALL of the following:\n"
    "1. service_name — from the large title text\n"
    "2. start_time — in HH:MM format\n"
    "3. duration — the RIGHT time from the bottom strip, in HH:MM format\n"
    "4. tonnage — weight number (e.g. 409.1)\n"
    "5. car_count — integer (e.g. 5)\n"
    "6. train_length — length number (e.g. 125.2)\n\n"
    "Respond ONLY with valid JSON:\n"
    '{"service_name": "<text>", "start_time": "<HH:MM>", '
    '"duration": "<HH:MM>", "tonnage": <number>, '
    '"car_count": <number>, "train_length": <number>}\n'
    "No other text."
)


def extract_level_info_from_screen(service_dir):
    """Send a composite of level_screen.png + 2_times.png to LLM for data extraction.

    Stitches level_screen.png (main data) with 2_times.png (start_time + duration)
    into a single composite image. Saves as level_info_composite.png.

    Uses config.LEVEL_INFO_LLM_PROVIDER to choose between local LM Studio
    and Claude API.  Saves results to llm_data.json in the service dir.

    Returns dict with keys: service_name, start_time, duration, tonnage,
    car_count, train_length
    """
    screen_path = os.path.join(service_dir, "level_screen.png")

    if not os.path.isfile(screen_path):
        print("       WARNING: level_screen.png not found, cannot extract level info")
        return {"service_name": None, "tonnage": None, "car_count": None,
                "train_length": None, "start_time": None, "duration": None}

    crop_service_times(service_dir)
    times_path = os.path.join(service_dir, "2_times.png")

    # Build composite: level_screen + times strip at bottom
    screen_img = Image.open(screen_path)
    images = [screen_img]
    if os.path.isfile(times_path):
        times_img = Image.open(times_path)
        images.append(times_img)
    else:
        print("       WARNING: 2_times.png not found, composite will lack duration")

    total_width = max(img.width for img in images)
    total_height = sum(img.height for img in images)
    composite = Image.new("RGB", (total_width, total_height))
    y_offset = 0
    for img in images:
        composite.paste(img, (0, y_offset))
        y_offset += img.height

    composite_path = os.path.join(service_dir, "level_info_composite.png")
    composite.save(composite_path)
    print(f"       Extracting level info via LLM ({config.LEVEL_INFO_LLM_PROVIDER})...")

    try:
        response_text = call_llm("level_info", composite_path, LEVEL_SCREEN_PROMPT)

        result = _parse_json_response(response_text)
        print(f"       LLM extracted: {result}")

        # Save level info to llm_data.json
        _save_llm_data(service_dir, "level_info", result)
        print(f"       LLM level info saved to llm_data.json")

        return result

    except SystemExit:
        raise
    except Exception as e:
        print(f"       WARNING: LLM level info extraction failed: {e}")
        fallback = {"service_name": None, "tonnage": None, "car_count": None,
                    "train_length": None, "start_time": None, "duration": None}
        _save_llm_data(service_dir, "level_info", fallback)
        return fallback


# ═══════════════════════════════════════════════════════════════════════
# Service list capture + Claude API analysis + CSV generation
# ═══════════════════════════════════════════════════════════════════════

# ═══════════════════════════════════════════════════════════════════════
# Service list capture + Claude API analysis + CSV generation
# ═══════════════════════════════════════════════════════════════════════

def _build_service_list_prompt():
    """Build the service list prompt, including calibrated first-click anchor if available."""
    anchor_hint = ""
    if config.FIRST_CLICK_X is not None and config.FIRST_CLICK_Y is not None:
        anchor_hint = (
            f"\nCALIBRATION ANCHOR: The center of the FIRST service box is at "
            f"pixel ({config.FIRST_CLICK_X}, {config.FIRST_CLICK_Y}) in this image. "
            "Use this as your reference point. All other service boxes are stacked "
            "vertically below this one at regular intervals. Report the center x,y "
            "of each box as accurately as possible relative to this anchor.\n"
        )

    return (
        f"This is a {GAME_REGION[2]}x{GAME_REGION[3]} pixel screenshot of a train simulation game. "
        "The service list is on the right side of the screen. "
        "Each service is shown as a rectangular box/card stacked vertically.\n\n"
        "ACCURACY IS CRITICAL. Take your time and read carefully.\n\n"
        + anchor_hint +
        "\nEach service box contains:\n"
        "- A service name (text string, may include route codes like '207-04')\n"
        "- A start time (HH:MM:SS format, e.g. 05:31:00)\n"
        "- A duration (HH:MM:SS format, e.g. 00:28:00)\n\n"
        "IMPORTANT RULES:\n"
        "- TIMESTAMPS ARE KEY: The start_time and duration pair is the unique fingerprint "
        "of each service box. Pay extra attention to reading these values precisely. "
        "Even services with the same name will have different timestamp combinations. "
        "Use the unique combination of service name, start time, and duration to determine whether a box is a new/different service.\n"
        "- Service names CAN repeat — two boxes with the same name but different "
        "times are SEPARATE services. Count each box.\n"
        "- Read EVERY character carefully. Distinguish between similar characters: "
        "0 vs O vs D, 1 vs I vs l, 5 vs S.\n"
        "- The start time and duration are two SEPARATE time values.\n"
        "- Count ONLY fully or mostly visible boxes. SKIP any box that is "
        "cut off or partially visible at the top or bottom edge — do NOT include it.\n"
        "- If a box is even SLIGHTLY cut off at the bottom, still count it if you can "
        "read the service name and both time values. Only skip if truly unreadable.\n"
        "- Never use 'partially visible' or similar phrases as a service name.\n\n"
        "For each service box, report the x,y pixel coordinates of the CENTER "
        "of the box within this image.\n\n"
        "Respond ONLY with valid JSON:\n"
        '{"count": <number>, "services": ['
        '{"index": 1, "x": <pixel>, "y": <pixel>, "name": "<text>", '
        '"start_time": "<HH:MM:SS>", "duration": "<HH:MM:SS>"}]}\n'
        "No other text."
    )


def _normalize_time(t):
    """Ensure time is in HH:MM:SS format."""
    parts = t.strip().split(":")
    if len(parts) == 2:
        return f"{parts[0].zfill(2)}:{parts[1].zfill(2)}:00"
    elif len(parts) == 3:
        return f"{parts[0].zfill(2)}:{parts[1].zfill(2)}:{parts[2].zfill(2)}"
    return t


def _parse_time_seconds(t):
    """Convert HH:MM:SS or HH:MM to total seconds. Returns None on failure."""
    try:
        parts = t.strip().split(":")
        if len(parts) == 3:
            return int(parts[0]) * 3600 + int(parts[1]) * 60 + int(parts[2])
        elif len(parts) == 2:
            return int(parts[0]) * 3600 + int(parts[1]) * 60
    except (ValueError, AttributeError):
        pass
    return None


def _fuzzy_name_match(a, b):
    """Check if two service names are likely the same despite OCR differences."""
    a = a.strip().replace(" ", "")
    b = b.strip().replace(" ", "")
    compare_len = min(len(a), len(b), max(4, min(len(a), len(b))))
    if compare_len < 3:
        return a == b
    a_sub = a[:compare_len]
    b_sub = b[:compare_len]
    if a_sub == b_sub:
        return True
    diffs = sum(1 for ca, cb in zip(a_sub, b_sub) if ca != cb)
    return diffs <= 1


def _is_fuzzy_duplicate(svc, existing_list):
    """Check if a service is a fuzzy duplicate of any already seen.
    All three must match: name, start_time, AND duration."""
    s_time = _parse_time_seconds(svc.get("start_time", ""))
    s_dur = _parse_time_seconds(svc.get("duration", ""))
    s_name = svc.get("name", "")
    if s_time is None:
        return False
    for existing in existing_list:
        e_time = _parse_time_seconds(existing.get("start_time", ""))
        e_dur = _parse_time_seconds(existing.get("duration", ""))
        if e_time is None:
            continue
        # All three must match: name, start_time, AND duration
        if (s_time == e_time
                and s_dur == e_dur
                and _fuzzy_name_match(s_name, existing.get("name", ""))):
            return True
    return False


def capture_service_pages(capture_dir):
    """Scroll through the service list and capture full-screen game screenshots.

    Returns list of saved page image paths.
    """
    # Service list region just for scroll-end detection
    detect_region = (
        config.SERVICE_LIST_LEFT,
        config.SERVICE_LIST_TOP,
        config.SERVICE_LIST_RIGHT - config.SERVICE_LIST_LEFT,
        config.SERVICE_LIST_BOTTOM - config.SERVICE_LIST_TOP,
    )
    center_x = (config.SERVICE_LIST_LEFT + config.SERVICE_LIST_RIGHT) // 2
    center_y = (config.SERVICE_LIST_TOP + config.SERVICE_LIST_BOTTOM) // 2

    os.makedirs(capture_dir, exist_ok=True)

    # Scroll to top first
    print("       Scrolling service list to top...")
    scroll_service_list_to_top()
    print("       At top of list.")

    pages = []
    page_num = 0

    while True:
        # Capture full game screen
        img = pyautogui.screenshot(region=GAME_REGION)
        page_path = os.path.join(capture_dir, f"page_{page_num:03d}.png")
        img.save(page_path)
        pages.append(page_path)
        print(f"       Captured page {page_num} ({GAME_REGION[2]}x{GAME_REGION[3]})")

        # Grab service list region for scroll detection
        detect_img = np.array(pyautogui.screenshot(region=detect_region))

        # Scroll down one page — click dead zone to ensure game has focus
        # Click top-left of game window (outside service list) to avoid
        # accidentally selecting a service
        dead_x = GAME_REGION[0] + 100
        dead_y = GAME_REGION[1] + 50
        pyautogui.click(dead_x, dead_y)
        time.sleep(0.3)
        pyautogui.moveTo(center_x, center_y)
        time.sleep(0.3)
        pyautogui.scroll(config.SCROLL_AMOUNT)
        time.sleep(2.0)

        # Check if list stopped moving
        new_detect = np.array(pyautogui.screenshot(region=detect_region))
        if new_detect.shape == detect_img.shape:
            diff = np.mean(np.abs(new_detect.astype(float) - detect_img.astype(float)))
            print(f"       Scroll diff: {diff:.2f} (threshold: 5.0)")
            if diff < 5.0:
                print(f"       End of list after page {page_num}.")
                break

        page_num += 1

    print(f"       Captured {len(pages)} page(s)")

    # Scroll back to top for the service loop
    scroll_service_list_to_top()

    return pages


def analyse_pages_claude(page_paths, capture_dir):
    """Send captured pages to Claude API and build the unique service list.

    Returns list of unique service dicts with click coordinates.
    """
    print(f"\n       Analysing {len(page_paths)} pages with Claude API...")
    cost_tracker.mark("service_list_csv")

    if not os.environ.get("ANTHROPIC_API_KEY"):
        print("ERROR: ANTHROPIC_API_KEY environment variable not set.")
        sys.exit(1)

    click_x = (config.SERVICE_LIST_LEFT + config.SERVICE_LIST_RIGHT) // 2
    # Use calibrated first click anchor if available, else fall back to geometry
    has_anchor = (config.FIRST_CLICK_X is not None and config.FIRST_CLICK_Y is not None)
    gx, gy = config.GAME_REGION[0], config.GAME_REGION[1]

    # Fallback geometry-based coords
    geo_click_x = (config.SERVICE_LIST_LEFT + config.SERVICE_LIST_RIGHT) // 2
    geo_first_y = (config.SERVICE_LIST_TOP + config.FIRST_BOX_TOP
                   + config.SERVICE_BOX_HEIGHT // 2)

    prompt = _build_service_list_prompt()
    all_results = []

    for i, page_path in enumerate(page_paths):
        # Send full uncropped pages — fuzzy dedup handles overlap
        start = time.time()
        result, _ = call_llm_json("service_list", page_path, prompt)
        elapsed = time.time() - start
        count = result.get("count", 0)

        services = result.get("services", [])
        for box_idx, svc in enumerate(services):
            llm_x = svc.pop("x", None)
            llm_y = svc.pop("y", None)
            svc["llm_x"] = llm_x
            svc["llm_y"] = llm_y

            if has_anchor and llm_x is not None and llm_y is not None:
                # Convert LLM image-relative coords to absolute screen coords
                svc["click_x"] = llm_x + gx
                svc["click_y"] = llm_y + gy
            else:
                # Fall back to geometry-based coords
                svc["click_x"] = geo_click_x
                svc["click_y"] = geo_first_y + (box_idx * config.SERVICE_BOX_STRIDE)
            svc["page"] = i
            # Normalize times
            if "start_time" in svc:
                svc["start_time"] = _normalize_time(svc["start_time"])
            if "duration" in svc:
                svc["duration"] = _normalize_time(svc["duration"])

        all_results.append({
            "page": i,
            "count": count,
            "services": services,
        })

        print(f"       Page {i}: {count} services ({elapsed:.1f}s)")

    # Build unique service list with fuzzy dedup
    unique_services = []
    global_index = 0
    exact_keys = set()
    dupes_removed = 0
    total_boxes = sum(r.get("count", 0) for r in all_results)

    for r in all_results:
        for svc in r.get("services", []):
            name = svc.get("name", "?")
            # Skip entries where the LLM reported a partially visible box
            if "partially visible" in name.lower():
                dupes_removed += 1
                continue
            start = svc.get("start_time", "?")
            duration = svc.get("duration", "?")

            key = (name, start, duration)
            if key in exact_keys:
                dupes_removed += 1
                continue
            if _is_fuzzy_duplicate(svc, unique_services):
                dupes_removed += 1
                continue

            exact_keys.add(key)
            global_index += 1
            svc["global_index"] = global_index
            unique_services.append(svc)

    print(f"\n       Service discovery complete:")
    print(f"         Pages: {len(page_paths)}")
    print(f"         Total boxes: {total_boxes}")
    print(f"         Duplicates removed: {dupes_removed}")
    print(f"         Unique services: {len(unique_services)}")

    # Print cost breakdown for service list CSV generation
    cost_tracker.print_since_mark("service_list_csv",
                                  label=f"Service List CSV ({len(page_paths)} pages)")

    # Save JSON results
    results_path = os.path.join(capture_dir, "analysis_results.json")
    with open(results_path, "w") as f:
        json.dump({
            "provider": config.SERVICE_LIST_LLM_PROVIDER,
            "pages": len(page_paths),
            "total_boxes": total_boxes,
            "duplicates_removed": dupes_removed,
            "unique_count": len(unique_services),
            "unique_services": [
                {
                    "global_index": s["global_index"],
                    "page": s["page"],
                    "click_x": s["click_x"],
                    "click_y": s["click_y"],
                    "name": s.get("name", "?"),
                    "start_time": s.get("start_time", "?"),
                    "duration": s.get("duration", "?"),
                }
                for s in unique_services
            ],
        }, f, indent=2)

    return unique_services


def _split_service_name(name):
    """Split a service name like '741-05 Stonebridge Park to Kilburn High Road'
    into (service_id, route). If no ID prefix found, returns ('', full_name)."""
    m = re.match(r'^(\S+)\s+(.+)$', name)
    if m:
        candidate_id = m.group(1)
        # Check if it looks like a service ID (contains digit or dash)
        if re.search(r'[\d-]', candidate_id):
            return candidate_id, m.group(2)
    return "", name


def save_services_csv(unique_services, csv_path):
    """Save the unique service list as a CSV file (same format as compare/1.csv)."""
    with open(csv_path, "w", newline="") as f:
        writer = csv.writer(f)
        writer.writerow(["number", "page", "click", "service_id", "service_name", "time", "duration"])
        for svc in unique_services:
            gi = svc["global_index"]
            page = svc["page"]
            cx = svc["click_x"]
            cy = svc["click_y"]
            name = svc.get("name", "?")
            start_time = _normalize_time(svc.get("start_time", "?"))
            duration = _normalize_time(svc.get("duration", "?"))

            # Extract service_id but keep the full name intact in service_name
            service_id, _ = _split_service_name(name)
            writer.writerow([
                gi,
                f"page {page}",
                f"click ({cx} {cy})",
                service_id,
                name,
                start_time,
                duration,
            ])
    print(f"       CSV saved: {csv_path} ({len(unique_services)} services)")
    return csv_path


def load_services_csv(csv_path):
    """Load services from a CSV click-map file. Returns list of service dicts."""
    services = []
    with open(csv_path, "r", newline="") as f:
        reader = csv.DictReader(f)
        for row in reader:
            m = re.match(r"click \((\d+)\s+(\d+)\)", row["click"])
            if not m:
                continue
            click_x = int(m.group(1))
            click_y = int(m.group(2))
            page_num = int(re.search(r"\d+", row["page"]).group())
            # Support both new "service_name" and legacy "route" column
            name = row.get("service_name") or row.get("route", "")
            if "partially visible" in name.lower():
                continue
            services.append({
                "global_index": int(row["number"]),
                "page": page_num,
                "click_x": click_x,
                "click_y": click_y,
                "service_id": row.get("service_id", ""),
                "name": name,
                "start_time": row["time"],
                "duration": row["duration"],
            })
    return services


def build_service_list(train_dir):
    """Capture service list pages, send to Claude, save CSV.

    Returns the list of service dicts from original_services.csv (unfiltered).
    Expects to be at the service list screen.
    """
    capture_dir = os.path.join(train_dir, "service_pages")
    original_csv_path = os.path.join(train_dir, "original_services.csv")

    # Check if original CSV already exists (resume case)
    if os.path.isfile(original_csv_path):
        print(f"       Found existing original_services.csv, loading...")
        services = load_services_csv(original_csv_path)
        print(f"       Loaded {len(services)} services from original CSV")
        return services

    # Also check for legacy services.csv (from before the rename)
    legacy_csv_path = os.path.join(train_dir, "services.csv")
    if os.path.isfile(legacy_csv_path):
        print(f"       Found existing services.csv, renaming to original_services.csv...")
        shutil.copy2(legacy_csv_path, original_csv_path)
        services = load_services_csv(original_csv_path)
        print(f"       Loaded {len(services)} services from CSV")
        return services

    # Check if page screenshots already exist (partial resume — pages captured but
    # Claude analysis or CSV save failed)
    existing_pages = sorted(
        [os.path.join(capture_dir, f) for f in os.listdir(capture_dir)
         if f.startswith("page_") and f.endswith(".png")]
    ) if os.path.isdir(capture_dir) else []

    if existing_pages:
        print(f"\n       === Phase 1: Found {len(existing_pages)} existing page screenshots ===")
        page_paths = existing_pages
    else:
        # Capture all service list pages
        print(f"\n       === Phase 1: Capturing service list pages ===")
        page_paths = capture_service_pages(capture_dir)

    # Send to Claude API for analysis
    print(f"\n       === Phase 2: Claude API service discovery ===")
    unique_services = analyse_pages_claude(page_paths, capture_dir)

    # Save as original_services.csv (unfiltered, complete list)
    save_services_csv(unique_services, original_csv_path)

    return load_services_csv(original_csv_path)


# ═══════════════════════════════════════════════════════════════════════
# Service navigation (scroll to page + OCR click from ai_test_click.py)
# ═══════════════════════════════════════════════════════════════════════

def scroll_to_page(target_page):
    """Scroll from the top of the service list to the target page number.

    Scrolls one box at a time and detects when the list stops moving
    (end of list). Returns the number of scroll units that couldn't be
    completed (shortfall). A shortfall of 0 means a full scroll.
    """
    if target_page == 0:
        return 0

    total_scrolls = target_page * config.BOXES_TO_SCROLL
    center_x = (config.SERVICE_LIST_LEFT + config.SERVICE_LIST_RIGHT) // 2
    center_y = (config.SERVICE_LIST_TOP + config.SERVICE_LIST_BOTTOM) // 2
    region = (config.SERVICE_LIST_LEFT, config.SERVICE_LIST_TOP,
              config.SERVICE_LIST_RIGHT - config.SERVICE_LIST_LEFT,
              config.SERVICE_LIST_BOTTOM - config.SERVICE_LIST_TOP)
    pyautogui.moveTo(center_x, center_y)
    time.sleep(0.3)

    prev_img = np.array(pyautogui.screenshot(region=region))
    actual = 0
    for i in range(total_scrolls):
        pyautogui.scroll(config.SCROLL_PER_BOX)
        time.sleep(0.4)
        curr_img = np.array(pyautogui.screenshot(region=region))
        if curr_img.shape == prev_img.shape:
            diff = np.mean(np.abs(curr_img.astype(float) - prev_img.astype(float)))
            if diff < 5.0:
                print(f"       Scroll stopped after {actual}/{total_scrolls} units (end of list)")
                break
        actual += 1
        prev_img = curr_img

    time.sleep(0.5)
    return total_scrolls - actual


def find_service_by_timestamps(service):
    """OCR the service list area and find the target service by its timestamps.

    Returns (click_x, click_y) in screen coordinates, or None if not found.
    """
    import pytesseract
    pytesseract.pytesseract.tesseract_cmd = r'C:\Program Files\Tesseract-OCR\tesseract.exe'

    cx = (config.SERVICE_LIST_LEFT + config.SERVICE_LIST_RIGHT) // 2

    region = (
        config.SERVICE_LIST_LEFT,
        config.SERVICE_LIST_TOP,
        config.SERVICE_LIST_RIGHT - config.SERVICE_LIST_LEFT,
        config.SERVICE_LIST_BOTTOM - config.SERVICE_LIST_TOP,
    )
    img = pyautogui.screenshot(region=region)

    data = pytesseract.image_to_data(img, output_type=pytesseract.Output.DICT)

    # Find all HH:MM time strings and their Y positions
    time_entries = []
    for i, text in enumerate(data["text"]):
        text = str(text).strip()
        if re.match(r"^\d{2}:\d{2}$", text):
            y = data["top"][i]
            h = data["height"][i]
            center_y = y + h // 2
            time_entries.append({"text": text, "y": center_y})

    # Target times (strip seconds → HH:MM)
    target_time = service["start_time"][:5]
    target_dur = service["duration"][:5]

    # Group OCR times by similar Y (same row)
    rows = []
    used = [False] * len(time_entries)
    for i, t in enumerate(time_entries):
        if used[i]:
            continue
        row = [t]
        used[i] = True
        for j in range(i + 1, len(time_entries)):
            if not used[j] and abs(time_entries[j]["y"] - t["y"]) < 15:
                row.append(time_entries[j])
                used[j] = True
        rows.append(row)

    # Search for a row matching both start_time and duration
    for row in rows:
        texts = [t["text"] for t in row]
        if target_time in texts and target_dur in texts:
            avg_y = sum(t["y"] for t in row) // len(row)
            screen_y = config.SERVICE_LIST_TOP + avg_y
            return (cx, screen_y)

    # Fallback: match just the start time
    for row in rows:
        texts = [t["text"] for t in row]
        if target_time in texts:
            avg_y = sum(t["y"] for t in row) // len(row)
            screen_y = config.SERVICE_LIST_TOP + avg_y
            print(f"       Partial match (time only): {texts}")
            return (cx, screen_y)

    return None


def find_service_by_llm(service):
    """Screenshot the full game screen and ask Claude to locate the target service.

    Returns (click_x, click_y) in screen coordinates, or None if not found.
    """
    sid = service.get("service_id", "?")
    name = service.get("name", "?")
    start_time = service.get("start_time", "?")[:5]
    duration = service.get("duration", "?")[:5]

    img = pyautogui.screenshot(region=GAME_REGION)
    img_w, img_h = img.size

    # Save to temp file for LLM
    temp_path = os.path.join(config.SCREENSHOTS_DIR, "_find_service.png")
    img.save(temp_path)

    prompt = (
        f"This is a {img_w}x{img_h} pixel screenshot of a train simulation game. "
        f"The service list is on the right side of the screen.\n\n"
        f"I need to find this service:\n"
        f"  Name: {sid} {name}\n"
        f"  Start time: {start_time}\n"
        f"  Duration: {duration}\n\n"
        f"If you can see this service in the image, return the x and y pixel coordinates "
        f"of the CENTER of that service box within this image.\n\n"
        f"Respond ONLY with valid JSON:\n"
        f'{{"found": true, "x": <pixel>, "y": <pixel>}}\n'
        f"or if not found:\n"
        f'{{"found": false}}\n'
        f"No other text."
    )

    try:
        response_text = call_llm("service_locate", temp_path, prompt)
        result = _parse_json_response(response_text)
        if result.get("found"):
            img_x = int(result.get("x", img_w // 2))
            img_y = int(result.get("y", img_h // 2))
            # Convert image coords to absolute screen coords
            screen_x = GAME_REGION[0] + img_x
            screen_y = GAME_REGION[1] + img_y
            print(f"       LLM found service at image ({img_x}, {img_y}) → screen ({screen_x}, {screen_y})")
            return (screen_x, screen_y)
        else:
            print(f"       LLM did not find service on screen")
            return None
    except SystemExit:
        raise
    except Exception as e:
        print(f"       LLM locate failed: {e}")
        return None


VERIFY_TIMES_PROMPT = (
    "This is a cropped image from a train simulation game showing a service tile. "
    "Read the two time values visible. The first is the start time and the second is the duration. "
    "Both are in HH:MM format.\n\n"
    "Respond ONLY with valid JSON:\n"
    '{"start_time": "<HH:MM>", "duration": "<HH:MM>"}\n'
    "No other text."
)


def _verify_service_times(y_center, expected_start, expected_duration):
    """Screenshot the highlighted service box and verify start_time/duration match.

    Takes a screenshot of the service tile at y_center, crops the times region,
    sends to Claude to read start_time and duration, compares against expected values.

    Returns True if times match, False otherwise.
    """
    from service_loop_scroll_method import _grab_and_crop_service_box

    img, _raw, found = _grab_and_crop_service_box(y_center)
    if not found:
        print("       Verify: could not grab service box for verification")
        return False

    # Crop the times portion (right side of tile)
    pil_img = Image.fromarray(img)
    w, h = pil_img.size
    if SERVICE_TIME_CROP_X >= w:
        print("       Verify: service tile too narrow for time crop")
        return False

    time_crop = pil_img.crop((SERVICE_TIME_CROP_X, 0, w, h))

    temp_path = os.path.join(config.SCREENSHOTS_DIR, "_verify_times.png")
    time_crop.save(temp_path)

    try:
        response_text = call_llm("verify_times", temp_path, VERIFY_TIMES_PROMPT)

        result = _parse_json_response(response_text)
        read_start = result.get("start_time", "")
        read_duration = result.get("duration", "")

        # Normalize expected times to HH:MM for comparison
        exp_start = expected_start[:5] if expected_start and len(expected_start) >= 5 else expected_start
        exp_dur = expected_duration[:5] if expected_duration and len(expected_duration) >= 5 else expected_duration

        match = (read_start == exp_start and read_duration == exp_dur)
        if match:
            print(f"       Verify: times match ({read_start} / {read_duration})")
        else:
            print(f"       Verify: MISMATCH — expected {exp_start}/{exp_dur}, "
                  f"got {read_start}/{read_duration}")
        return match

    except SystemExit:
        raise
    except Exception as e:
        print(f"       Verify: LLM verification failed: {e}")
        return False


def _check_highlight_near(click_x, click_y, margin=50):
    """Check for the blue highlight border in a region around the click point.

    Grabs a box-sized area (plus margin) centered on the click coords and checks
    for the #57a6d0 border color. This catches highlights that the full-list
    edge scan misses due to scroll drift shifting the box outside the scan region.

    Returns True if the highlight color is found in sufficient quantity.
    """
    target = np.array(config.SERVICE_BOX_BORDER_RGB)
    tol = config.SERVICE_BOX_COLOR_TOLERANCE
    half_h = config.SERVICE_BOX_HEIGHT // 2 + margin
    half_w = (config.SERVICE_LIST_RIGHT - config.SERVICE_LIST_LEFT) // 2 + margin

    left = max(0, click_x - half_w)
    top = max(0, click_y - half_h)
    width = half_w * 2
    height = half_h * 2

    img = np.array(pyautogui.screenshot(region=(left, top, width, height)))
    match = np.all(np.abs(img[:, :, :3].astype(int) - target) <= tol, axis=2)
    match_count = np.sum(match)

    # Need enough matching pixels to indicate a real border (not just noise)
    # A highlighted box border is ~2px wide around a ~58x680 box = ~3000+ pixels
    min_pixels = 200
    print(f"       Local highlight check: {match_count} border pixels (need {min_pixels})")
    return match_count >= min_pixels


def _click_and_check(cx, cy, label="coordinates"):
    """Click at (cx, cy) and return True if a highlight is detected."""
    print(f"       Clicking {label} ({cx}, {cy})...")
    list_cx = (config.SERVICE_LIST_LEFT + config.SERVICE_LIST_RIGHT) // 2
    list_cy = (config.SERVICE_LIST_TOP + config.SERVICE_LIST_BOTTOM) // 2
    pyautogui.moveTo(list_cx, list_cy)
    time.sleep(0.3)
    pyautogui.moveTo(cx, cy)
    time.sleep(0.5)
    pyautogui.mouseDown()
    time.sleep(0.3)
    pyautogui.mouseUp()
    time.sleep(2.0)
    return _check_highlight_near(cx, cy)


def navigate_to_service(service):
    """Navigate to a service using click registry as the primary method.

    Order: click registry → CSV coords → LLM locate → nudge scroll → adjacent pages → search by name.
    Returns (click_x, click_y) of the successful click, or best-effort coords if all fail.
    """
    registry = getattr(config, 'CLICK_REGISTRY', {})
    svc_num = service.get("global_index") or service.get("number")
    csv_page = service["page"]
    csv_cx = service["click_x"]
    csv_cy = service["click_y"]

    # ── Primary: click registry ──────────────────────────────────────
    if svc_num and svc_num in registry:
        reg = registry[svc_num]
        cx = reg["click_x"]
        cy = reg["click_y"]
        target_page = reg["page"]
        print(f"       Click registry #{svc_num}: ({cx}, {cy}) page {target_page}")

        scroll_service_list_to_top()
        shortfall = scroll_to_page(target_page)

        # Adjust for partial last page
        adjusted_cy = cy
        if shortfall > 0:
            adjustment = shortfall * config.SERVICE_BOX_STRIDE
            adjusted_cy = cy + adjustment
            print(f"       Partial page: {shortfall} scrolls short, cy adjusted +{adjustment}px -> {adjusted_cy}")

        if _click_and_check(cx, adjusted_cy, label="registry"):
            print(f"       Registry hit at y={adjusted_cy}")
            return (cx, adjusted_cy)

        # If adjusted coords missed but we had a shortfall, also try original coords
        if shortfall > 0 and _click_and_check(cx, cy, label="registry-original"):
            print(f"       Registry original coords hit at y={cy}")
            return (cx, cy)

        print(f"       Registry coords missed — falling back")
    else:
        print(f"       No click registry entry for #{svc_num}")
        target_page = csv_page
        cx, cy = csv_cx, csv_cy

    # ── Fallback 1: CSV coords ───────────────────────────────────────
    scroll_service_list_to_top()
    csv_shortfall = scroll_to_page(csv_page)

    csv_adjusted_cy = csv_cy
    if csv_shortfall > 0:
        csv_adjustment = csv_shortfall * config.SERVICE_BOX_STRIDE
        csv_adjusted_cy = csv_cy + csv_adjustment
        print(f"       CSV partial page: {csv_shortfall} scrolls short, cy adjusted +{csv_adjustment}px -> {csv_adjusted_cy}")

    if _click_and_check(csv_cx, csv_adjusted_cy, label="CSV"):
        print(f"       CSV coords hit at y={csv_adjusted_cy}")
        return (csv_cx, csv_adjusted_cy)

    # ── Fallback 2: LLM locate ───────────────────────────────────────
    print(f"       No highlight — asking LLM to locate service...")
    result = find_service_by_llm(service)
    if result:
        lx, ly = result
        if _click_and_check(lx, ly, label="LLM"):
            print(f"       LLM hit at y={ly}")
            return (lx, ly)

    # ── Fallback 3: nudge scroll ─────────────────────────────────────
    center_x = (config.SERVICE_LIST_LEFT + config.SERVICE_LIST_RIGHT) // 2
    center_y = (config.SERVICE_LIST_TOP + config.SERVICE_LIST_BOTTOM) // 2
    for nudge in [-500, 500, -1000, 1000]:
        print(f"       Nudging scroll by {nudge}...")
        pyautogui.moveTo(center_x, center_y)
        time.sleep(0.3)
        pyautogui.scroll(nudge)
        time.sleep(1.5)

        result = find_service_by_llm(service)
        if result:
            lx, ly = result
            if _click_and_check(lx, ly, label="LLM+nudge"):
                return (lx, ly)

    # ── Fallback 4: adjacent pages ───────────────────────────────────
    for adj_page in [max(0, target_page - 1), target_page + 1]:
        if adj_page == target_page:
            continue
        print(f"       Trying page {adj_page}...")
        scroll_service_list_to_top()
        scroll_to_page(adj_page)

        result = find_service_by_llm(service)
        if result:
            lx, ly = result
            if _click_and_check(lx, ly, label="LLM+adj page"):
                return (lx, ly)

    # ── Fallback 5: search by service name ───────────────────────────
    svc_name = service.get("name", "")
    if svc_name:
        print(f"       All locate methods failed — trying search by name: '{svc_name}'")
        from service_loop import search_and_select_service, clear_service_search
        found, search_y = search_and_select_service(svc_name)
        if found and search_y:
            expected_start = service.get("start_time", "")
            expected_duration = service.get("duration", "")
            if _verify_service_times(search_y, expected_start, expected_duration):
                print(f"       Search by name succeeded and verified at y={search_y}")
                search_cx = (config.SERVICE_LIST_LEFT + config.SERVICE_LIST_RIGHT) // 2
                return (search_cx, search_y)
            else:
                print(f"       Search found a service but times don't match — wrong service")
                clear_service_search()
        else:
            print(f"       Search by name also failed")
            clear_service_search()

    # ── All methods failed ───────────────────────────────────────────
    print(f"       All locate methods failed, returning registry/CSV coords ({cx}, {cy})")
    from service_loop import clear_service_search
    clear_service_search()
    scroll_service_list_to_top()
    scroll_to_page(target_page)
    return (cx, cy)


# ═══════════════════════════════════════════════════════════════════════
# Logging
# ═══════════════════════════════════════════════════════════════════════

def _log(train_dir, message):
    """Append a line to log.txt in the train folder."""
    log_path = os.path.join(train_dir, "log.txt")
    with open(log_path, "a", encoding="utf-8") as f:
        f.write(message + "\n")


# ═══════════════════════════════════════════════════════════════════════
# Progress save/load
# ═══════════════════════════════════════════════════════════════════════

def save_progress(train_name, train_number, service_number, choice=None,
                   trains_per_class=None):
    data = {
        "route": config.ROUTE_NAME,
        "choice": choice,
        "train": train_name,
        "train_num": train_number,
        "service": service_number,
    }
    if trains_per_class is not None:
        data["trains_per_class"] = trains_per_class
    with open(PROGRESS_FILE, "w") as f:
        json.dump(data, f, indent=2)


def load_progress():
    if not os.path.isfile(PROGRESS_FILE):
        return None, None, None, None, None
    try:
        with open(PROGRESS_FILE, "r") as f:
            data = json.load(f)
        if data.get("route") != config.ROUTE_NAME:
            print(f"Progress file is for a different route "
                  f"('{data.get('route')}'), ignoring.")
            return None, None, None, None, None
        return (data.get("choice"), data["train"], data.get("train_num", 1),
                data.get("service", 1), data.get("trains_per_class"))
    except (json.JSONDecodeError, KeyError):
        return None, None, None, None, None


def clear_progress():
    if os.path.isfile(PROGRESS_FILE):
        os.remove(PROGRESS_FILE)


# ═══════════════════════════════════════════════════════════════════════
# Captured services registry — tracks services already recorded across trains
# ═══════════════════════════════════════════════════════════════════════

def _service_key(service):
    """Return a unique key for a service: service_id,name,start_time,duration."""
    sid = service.get("service_id", "")
    name = service.get("name", "")
    start = service.get("start_time", "")
    dur = service.get("duration", "")
    return f"{sid},{name},{start},{dur}"


def load_captured_services(class_dir):
    """Load the set of already-captured service keys for this train class."""
    filepath = os.path.join(class_dir, CAPTURED_SERVICES_FILENAME)
    if not os.path.isfile(filepath):
        return set()
    try:
        with open(filepath, "r") as f:
            data = json.load(f)
        return set(data)
    except (json.JSONDecodeError, KeyError):
        return set()


def save_captured_service(service, class_dir):
    """Add a service to the captured registry for this train class."""
    filepath = os.path.join(class_dir, CAPTURED_SERVICES_FILENAME)
    if os.path.isfile(filepath):
        try:
            with open(filepath, "r") as f:
                data = json.load(f)
        except (json.JSONDecodeError, KeyError):
            data = []
    else:
        data = []

    svc_key = _service_key(service)
    if svc_key not in data:
        data.append(svc_key)

    with open(filepath, "w") as f:
        json.dump(data, f, indent=2)


# ═══════════════════════════════════════════════════════════════════════
# Server management
# ═══════════════════════════════════════════════════════════════════════

_server_proc = None


def start_server():
    global _server_proc
    parent_dir = os.path.dirname(config.BASE_DIR)
    server_js = os.path.join(parent_dir, "server.js")

    if not os.path.isfile(server_js):
        print(f"ERROR: server.js not found at {server_js}")
        sys.exit(1)

    print("Starting Node.js server...")
    _server_proc = subprocess.Popen(
        ["node", server_js, "--no-subscriptions"],
        cwd=parent_dir,
        stdout=subprocess.PIPE,
        stderr=subprocess.STDOUT,
    )
    atexit.register(_stop_server)

    for i in range(30):
        time.sleep(1)
        if _server_proc.poll() is not None:
            out = _server_proc.stdout.read().decode(errors="replace")
            print(f"ERROR: Server exited immediately:\n{out}")
            sys.exit(1)
        if check_server():
            print("Server started and ready.\n")
            return
        if i % 5 == 4:
            print(f"  Still waiting for server... ({i + 1}s)")

    print("ERROR: Server did not become ready within 30 seconds.")
    _stop_server()
    sys.exit(1)


def _stop_server():
    global _server_proc
    if _server_proc and _server_proc.poll() is None:
        print("\nStopping Node.js server...")
        _server_proc.terminate()
        try:
            _server_proc.wait(timeout=5)
        except subprocess.TimeoutExpired:
            _server_proc.kill()
        print("Server stopped.")
    _server_proc = None


# ═══════════════════════════════════════════════════════════════════════
# Service processing loop
# ═══════════════════════════════════════════════════════════════════════

def process_single_service(service_dir, service, train_name, train_index,
                           service_index, train_dir, section_name=None):
    """Process one service from the CSV: navigate to it, capture level info, schedule, upload.

    Expects to be at the service list screen.
    Returns the upload result dict.

    After completion, the game will be at the main menu (via exit_to_main_menu).
    """
    sid = service.get("service_id", "?")
    name = service.get("name", "?")
    print(f"       Service: {sid} — {name}")
    print(f"       Time: {service.get('start_time', '?')}  Duration: {service.get('duration', '?')}")

    # Navigate to the service (scroll to page, click, verify highlight)
    cx, cy = navigate_to_service(service)

    # Verify highlight using local check (scans area around click point)
    if not _check_highlight_near(cx, cy):
        print(f"       No highlight detected — service may not exist")
        _log(train_dir, f"Service #{service_index} | NO HIGHLIGHT | {sid} {name}")
        return None

    use_y = cy

    # Screenshot the service box — grab from service list bounds
    path = os.path.join(service_dir, "1_service.png")
    half_h = config.SERVICE_BOX_HEIGHT // 2 + 10
    grab_top = max(0, use_y - half_h)
    grab_height = config.SERVICE_BOX_HEIGHT + 20
    grab_left = config.SERVICE_LIST_LEFT
    grab_width = config.SERVICE_LIST_RIGHT - config.SERVICE_LIST_LEFT
    svc_img = pyautogui.screenshot(region=(grab_left, grab_top, grab_width, grab_height))
    svc_img.save(path)
    final_y = use_y
    pyautogui.press("enter")
    time.sleep(1.0)
    pyautogui.press("enter")

    # Extract level info before clicking 'Get Started'
    level_info_holder = [None]
    def _extract_before_start():
        if config.USE_LLM_LEVEL_INFO:
            level_info_holder[0] = extract_level_info_from_screen(service_dir)

    # Wait for level to load — captures crops/screen, runs LLM, then clicks 'Get Started'
    wait_for_level_load(click_x=cx, click_y=use_y, service_dir=service_dir,
                        before_get_started=_extract_before_start)
    level_info = level_info_holder[0]

    # Save section name into level_info if available
    if section_name:
        data = _load_llm_data(service_dir)
        li = data.get("level_info")
        if li and li.get("section") != section_name:
            li["section"] = section_name
            _save_llm_data(service_dir, "level_info", li)

    # Capture the schedule (press Escape → click Schedule → scroll/stitch)
    # Lat/lng is fetched on the schedule screen where the game is paused (no timeout risk)
    # Keep schedule open so LLM extraction runs while game is still paused
    _sched_path, first_lat, first_lng, current_service_name = capture_schedule(service_dir, close_when_done=False)
    _save_llm_data(service_dir, "location", {"lat": first_lat, "lng": first_lng, "current_service_name": current_service_name})
    if current_service_name:
        run_report.timetable_service_current(current_service_name)

    # LLM schedule extraction → structured JSON (replaces OCR when enabled)
    if config.USE_LLM_SCHEDULE:
        extract_schedule_llm_json(service_dir)

    # Now close the schedule (game resumes)
    pyautogui.press("escape")
    time.sleep(2.0)

    # Copy image paths for reference
    _log(train_dir, f"Service #{service_index} | {sid} {name} | images: {service_dir}")

    # Upload to parent app for OCR + database save
    try:
        result = upload_service(
            service_dir, train_name,
            train_index + 1, service_index,
            first_lat=first_lat, first_lng=first_lng,
            section_name=section_name,
            current_service_name=current_service_name,
        )
        svc_name = result.get("service_name", "")
        if result.get("train_added") or result.get("section_added"):
            tt_id = result.get("timetable_id")
            parts = []
            if result.get("train_added"):
                parts.append("train")
            if result.get("section_added"):
                parts.append("section")
            what = " + ".join(parts)
            print(f"       {svc_name} ({what} added to existing timetable {tt_id})")
            _log(train_dir, f"Service #{service_index} | ADDED ({what}) | {svc_name} | timetable_id={tt_id}")
        elif result.get("duplicate"):
            print(f"       {svc_name} (duplicate — already recorded)")
            _log(train_dir, f"Service #{service_index} | DUPLICATE | {svc_name}")
        elif result["success"]:
            print(f"       {svc_name}")
            _log(train_dir, f"Service #{service_index} | OK | {svc_name} | timetable_id={result.get('timetable_id')}")
        else:
            print(f"       Upload error: {result['error']}")
            _log(train_dir, f"Service #{service_index} | ERROR | {result['error']}")
    except Exception as e:
        print(f"       Upload error: {e}")
        _log(train_dir, f"Service #{service_index} | ERROR | {e}")
        result = {"success": False, "error": str(e)}

    # Upload train consist data (weight/car_count/train_length per train)
    tt_id = result.get("timetable_id") if result else None
    if tt_id:
        import uploader as _uploader_mod
        train_id = _uploader_mod._train_ids[0] if _uploader_mod._train_ids else None
        li = level_info or {}
        upload_train_consist(
            timetable_id=tt_id,
            train_id=train_id,
            train_number=train_index + 1,
            weight=li.get("tonnage"),
            car_count=li.get("car_count"),
            train_length=li.get("train_length"),
            latitude=first_lat,
            longitude=first_lng,
        )

    # Exit back to main menu
    exit_to_main_menu()

    return result


def process_all_services(train_dir, train_index, train_name,
                         start_service=None, on_progress=None,
                         extra_section_choice=None, class_dir=None,
                         manifest_services=None, overwrite=False):
    """Iterate through all services for one train using the CSV service list.

    Phase 1: Capture service list pages and send to Claude API → CSV
    Phase 2: Loop through CSV, clicking each service, capturing data, uploading

    manifest_services: set of global_index numbers to process, or None for all.

    Returns at the MAIN MENU.
    """
    # Phase 1: Build service list (capture pages → Claude → CSV)
    service_list = build_service_list(train_dir)
    total_services_csv = len(service_list)

    if not service_list:
        print(f"       No services found for this train.")
        from service_loop_scroll_method import _return_to_main_menu_from_menus
        _return_to_main_menu_from_menus()
        return 0

    # Filter by manifest (specific service numbers)
    if manifest_services is not None:
        before = len(service_list)
        service_list = [s for s in service_list
                        if s["global_index"] in manifest_services]
        if before != len(service_list):
            print(f"       Manifest filter: {len(service_list)}/{before} services "
                  f"(requested: {sorted(manifest_services)})")

    # Filter out services already captured by previous trains
    # When resuming at a specific service, only filter services BEFORE the
    # start point so the requested global_index is always present.
    # In overwrite mode, skip this filter entirely so services are re-processed.
    if overwrite:
        print(f"       Overwrite mode: skipping 'already captured' filter")
        # Delete existing service folders so they get re-captured cleanly
        for svc in service_list:
            svc_dir = os.path.join(train_dir, f"service_{svc['global_index']:03d}")
            if os.path.isdir(svc_dir):
                shutil.rmtree(svc_dir)
                print(f"       Removed existing {os.path.basename(svc_dir)}/")
    else:
        captured = load_captured_services(class_dir) if class_dir else set()
        if captured:
            original_count = len(service_list)
            service_list = [s for s in service_list if _service_key(s) not in captured]
            skipped = original_count - len(service_list)
            if skipped:
                print(f"       Skipping {skipped} services already captured by previous trains")

    # Save filtered list as services.csv (the working list for this train)
    filtered_csv_path = os.path.join(train_dir, "services.csv")
    save_services_csv(service_list, filtered_csv_path)

    total_services = len(service_list)
    print(f"\n       === Phase 3: Processing {total_services} services "
          f"({total_services_csv} in CSV) ===\n")

    if not service_list:
        print(f"       All services already captured for this train.")
        from service_loop_scroll_method import _return_to_main_menu_from_menus
        _return_to_main_menu_from_menus()
        return 0

    # Determine starting point (based on global_index from CSV)
    start_idx = 0
    if start_service is not None and start_service > 1:
        # Find the first service in the filtered list at or after start_service
        for idx, svc in enumerate(service_list):
            if svc["global_index"] >= start_service:
                start_idx = idx
                break
        else:
            print(f"       Start service #{start_service} exceeds remaining services")
            from service_loop_scroll_method import _return_to_main_menu_from_menus
            _return_to_main_menu_from_menus()
            return 0
        print(f"       Resuming from service #{service_list[start_idx]['global_index']}")

    batch_count = 0

    for i in range(start_idx, total_services):
        service = service_list[i]
        service_index = service["global_index"]

        print(f"\n{'='*60}")
        print(f"--- Service #{service_index}/{total_services_csv} (page {service['page']}) ---")
        print(f"{'='*60}")

        # Mark cost tracking for this service
        cost_tracker.mark(f"service_{service_index}")
        run_report.timetable_service_start(service_index)

        # Save progress
        if on_progress:
            on_progress(train_name, train_index + 1, service_index)

        # Create per-service folder
        service_dir = os.path.join(train_dir, f"service_{service_index:03d}")
        os.makedirs(service_dir, exist_ok=True)

        try:
            result = process_single_service(
                service_dir, service,
                train_name, train_index, service_index,
                train_dir, section_name=extra_section_choice,
            )
        except TimeoutError as e:
            raise ServiceError(
                train_name, train_index + 1, service_index, e
            ) from e

        # Print cost breakdown for this service
        cost_tracker.print_since_mark(
            f"service_{service_index}",
            label=f"Service #{service_index}/{total_services_csv}")

        # Record per-service cost in run report
        svc_name = result.get("service_name", "") if result else ""
        run_report.timetable_service_end(service_index, svc_name)

        if result is None:
            # No highlight — we're still on the service list, skip re-navigation
            print(f"       Skipping service #{service_index}, continuing to next...")
            continue

        # Record this service as captured so future trains skip it
        if class_dir:
            save_captured_service(service, class_dir)

        # Check service limit
        if service_index >= MAX_SERVICES:
            print(f"\n       Reached service limit ({MAX_SERVICES}), stopping.")
            return service_index

        # Check batch limit
        if config.BATCH_SIZE is not None:
            batch_count += 1
            if batch_count >= config.BATCH_SIZE:
                print(f"\n       Batch limit ({config.BATCH_SIZE}) reached, restarting...")
                raise BatchRestart(
                    train_name, train_index + 1, service_index + 1
                )

        # Re-navigate to service list for next service (we're at main menu after exit_to_main_menu)
        if i < total_services - 1:
            try:
                from service_loop_scroll_method import navigate_to_service_list
                navigate_to_service_list(train_index, train_name,
                                         extra_section_choice=extra_section_choice)
            except TimeoutError as e:
                raise ServiceError(
                    train_name, train_index + 1, service_index, e
                ) from e

    print(f"\nProcessed {total_services} services for this train.")
    return total_services


def process_all_trains(train_name, start_train=None, start_service=None,
                       on_progress=None, extra_section_choice=None,
                       trains_per_class=None):
    """Outer loop: iterate through all trains in the class.

    Uses LLM to count trains (or cached value from progress), then loops
    through each one.
    """
    from datetime import datetime, timedelta

    run_start = time.time()

    # Create route/class folder structure
    route_dir = os.path.join(config.SCREENSHOTS_DIR, safe_dirname(config.ROUTE_NAME))
    if extra_section_choice:
        route_dir = os.path.join(route_dir, safe_dirname(extra_section_choice))
    class_dir = os.path.join(route_dir, safe_dirname(train_name))
    os.makedirs(class_dir, exist_ok=True)

    # Use cached train count from progress, or count via LLM
    if trains_per_class is not None:
        train_count = trains_per_class
        print(f"       Using cached train count: {train_count}")
    else:
        train_count = count_trains_llm()
    if config.MAX_TRAINS_PER_CLASS is not None:
        train_count = min(train_count, config.MAX_TRAINS_PER_CLASS)

    # Filter train numbers by manifest
    manifest_trains = config.manifest_filter_trains(
        config.ROUTE_NAME, extra_section_choice, train_name, train_count)
    run_report.timetable_trains(train_name, train_count, len(manifest_trains))
    print(f"\n=== Processing {len(manifest_trains)}/{train_count} trains for '{train_name}' ===")
    if len(manifest_trains) < train_count:
        print(f"    Manifest filter: trains {manifest_trains}")
    print()

    # Store train_count immediately so the progress callback can save it
    # (don't wait until process_all_trains returns — a crash would lose it)
    process_all_trains._last_train_count = train_count

    # Save progress right away with the train count so it's cached on crash
    if on_progress:
        on_progress(train_name, start_train or 1, start_service or 1)

    # Build the list of train indices to process (0-indexed)
    train_indices = [t - 1 for t in manifest_trains]

    # Handle resume: skip trains before the resume point
    if start_train:
        first_idx = start_train - 1
        train_indices = [t for t in train_indices if t >= first_idx]
        if train_indices and train_indices[0] == first_idx:
            print(f"Resuming from train {start_train}, service {start_service or 1}\n")

    train_results = []

    for train_idx in train_indices:
        train_num = train_idx + 1  # 1-indexed
        train_start = time.time()
        print(f"\n{'#'*60}")
        print(f"### Train {train_num}/{train_count}")
        print(f"{'#'*60}")

        # Create per-train folder
        train_dir = os.path.join(class_dir, f"train_{train_num:02d}")
        os.makedirs(train_dir, exist_ok=True)

        # Click the train
        try:
            click_train(train_idx)
        except TimeoutError as e:
            raise ServiceError(train_name, train_num, 1, e) from e

        # Only use start_service for the first train in a resume
        svc_start = start_service if start_train and train_idx == start_train - 1 else None

        # Get manifest service filter for this specific train
        manifest_services = config.manifest_filter_services(
            config.ROUTE_NAME, extra_section_choice, train_name, train_num)

        # Check if overwrite mode is enabled for this entry
        overwrite = config.manifest_is_overwrite(
            config.ROUTE_NAME, extra_section_choice, train_name, train_num)
        if overwrite:
            print(f"       OVERWRITE mode — will re-capture specified services")

        # Process all services for this train
        svc_count = process_all_services(
            train_dir=train_dir,
            train_index=train_idx,
            train_name=train_name,
            start_service=svc_start,
            on_progress=on_progress,
            extra_section_choice=extra_section_choice,
            class_dir=class_dir,
            manifest_services=manifest_services,
            overwrite=overwrite,
        )

        train_duration = time.time() - train_start
        train_results.append((train_num, svc_count, train_duration))

        # Exit and relaunch between trains
        if train_idx != train_indices[-1]:
            exit_game()
            from navigator import relaunch_and_navigate
            relaunch_and_navigate(train_name, extra_section_choice=extra_section_choice)

    total_duration = time.time() - run_start
    total_services = sum(r[1] for r in train_results)

    print(f"\n=== All {train_count} trains processed for '{train_name}'! ===")
    print(f"    Total services: {total_services}")
    print(f"    Total time: {timedelta(seconds=int(total_duration))}")

    # Write summary report
    report_path = os.path.join(class_dir, "report.txt")
    started = datetime.fromtimestamp(run_start)
    with open(report_path, "w") as f:
        f.write(f"TSW Timetable Bot — New Method Run Report\n")
        f.write(f"{'='*40}\n\n")
        f.write(f"Route:       {config.ROUTE_NAME}\n")
        f.write(f"Train:       {train_name}\n")
        f.write(f"Started:     {started:%Y-%m-%d %H:%M:%S}\n")
        f.write(f"Duration:    {timedelta(seconds=int(total_duration))}\n")
        f.write(f"Trains:      {train_count}\n")
        f.write(f"Services:    {total_services}\n\n")
        f.write(f"{'Train':<10} {'Services':<10} {'Duration'}\n")
        f.write(f"{'-'*10} {'-'*10} {'-'*10}\n")
        for train_num, svc_count, dur in train_results:
            f.write(f"Train {train_num:<4} {svc_count:<10} {timedelta(seconds=int(dur))}\n")

    print(f"\nReport saved to: {report_path}")

    # Print grand total Claude API cost
    cost_tracker.print_grand_total()
    print(f"Cost log: {cost_tracker.log_path}")


# ═══════════════════════════════════════════════════════════════════════
# Main entry point
# ═══════════════════════════════════════════════════════════════════════

def run_single_route():
    """Run the bot for the current config.ROUTE_NAME."""
    print(f"\n{'*'*60}")
    print(f"*** ROUTE: {config.ROUTE_NAME}")
    print(f"{'*'*60}\n")

    # Parse arguments
    no_launch = "--no-launch" in sys.argv
    args = [a for a in sys.argv[1:] if a != "--no-launch"]

    resume_choice = None
    resume_train_name = None
    resume_train_num = None
    resume_service = None

    if len(args) >= 3:
        resume_train_name = args[0]
        resume_train_num = int(args[1])
        resume_service = int(args[2])
        resume_trains_per_class = None
        print(f"RESUME MODE (args): train='{resume_train_name}', "
              f"train_num={resume_train_num}, service={resume_service}\n")
    else:
        resume_choice, resume_train_name, resume_train_num, resume_service, resume_trains_per_class = load_progress()
        if resume_train_name:
            print(f"RESUME MODE (auto): choice='{resume_choice}', "
                  f"train='{resume_train_name}', train_num={resume_train_num}, "
                  f"service={resume_service}, trains_per_class={resume_trains_per_class}\n")

    # Check reference images
    os.makedirs(config.SCREENSHOTS_DIR, exist_ok=True)

    # Start server if needed
    if check_server():
        print("Parent app server already running.\n")
    else:
        start_server()

    # Build choices list from database sections, filtered by manifest
    from uploader import get_sections
    db_sections = get_sections()
    if db_sections:
        choices = config.manifest_filter_sections(config.ROUTE_NAME, db_sections)
    else:
        choices = [None]

    retry_counts = {}
    need_launch = not no_launch
    current_choice = None

    # Skip to resume choice
    if resume_choice and resume_choice in choices:
        skip_idx = choices.index(resume_choice)
        choices = choices[skip_idx:]

    for choice in choices:
        current_choice = choice

        if choice:
            print(f"\n{'@'*60}")
            print(f"@@@ Extra Section Choice: {choice}")
            print(f"{'@'*60}\n")

        # Fetch trains for this section (or route if no sections)
        if choice is not None:
            print(f"Fetching trains for section '{choice}' from database...")
            try:
                all_trains = get_section_trains(choice)
            except ValueError as e:
                print(f"ERROR: {e}")
                sys.exit(1)
            if not all_trains:
                print(f"No section-specific trains — falling back to route trains...")
                try:
                    all_trains = get_trains()
                except ValueError as e:
                    print(f"ERROR: {e}")
                    sys.exit(1)
            print(f"Found {len(all_trains)} trains from DB: {', '.join(all_trains)}")
        else:
            print("Fetching trains from database...")
            try:
                all_trains = get_trains()
            except ValueError as e:
                print(f"ERROR: {e}")
                sys.exit(1)
            print(f"Found {len(all_trains)} trains from DB: {', '.join(all_trains)}")

        # Filter train classes by manifest
        all_trains = config.manifest_filter_train_classes(
            config.ROUTE_NAME, choice, all_trains)
        print(f"After manifest filter: {len(all_trains)} train(s): {', '.join(all_trains)}\n")

        # Resolve train IDs
        print("Resolving train IDs...")
        for train_name in all_trains:
            try:
                resolve_ids(train_name)
            except ValueError as e:
                print(f"ERROR: {e}")
                sys.exit(1)
        print("All IDs resolved.\n")

        remaining_trains = list(all_trains)

        # Skip to resume train
        if resume_train_name and resume_train_name in remaining_trains:
            skip_idx = remaining_trains.index(resume_train_name)
            if skip_idx > 0:
                print(f"Skipping {skip_idx} train(s) before '{resume_train_name}'")
            remaining_trains = remaining_trains[skip_idx:]

        cached_train_count = [resume_trains_per_class]  # mutable for closure

        def _save_progress_cb(train_name, train_number, service_number):
            # Pick up the train count as soon as process_all_trains sets it
            tc = getattr(process_all_trains, '_last_train_count',
                         cached_train_count[0])
            if tc is not None:
                cached_train_count[0] = tc
            save_progress(train_name, train_number, service_number,
                          choice=current_choice,
                          trains_per_class=cached_train_count[0])

        while remaining_trains:
            train_name = remaining_trains[0]

            print(f"\n{'#'*60}")
            print(f"### Train Class: {train_name} (new method)")
            if choice:
                print(f"### Choice: {choice}")
            print(f"### Remaining: {len(remaining_trains)} train class(es)")
            print(f"{'#'*60}\n")

            resolve_ids(train_name)
            run_report.timetable_start(train_name, choice)

            try:
                if need_launch:
                    launch_game()
                    if config.REPOSITION_GAME_WINDOW:
                        position_game_window()
                    pass_warning_screen()
                    pass_splash_screen()
                    click_to_the_trains()
                    click_choose_a_route()
                    select_route()
                    click_timetable()
                    if choice:
                        select_extra_section(choice)
                    select_train(train_name)
                    need_launch = False

                process_all_trains(
                    train_name,
                    start_train=resume_train_num,
                    start_service=resume_service,
                    on_progress=_save_progress_cb,
                    extra_section_choice=choice,
                    trains_per_class=resume_trains_per_class,
                )
                # Cache the train count for future progress saves
                cached_train_count[0] = getattr(
                    process_all_trains, '_last_train_count', None)

                run_report.timetable_end(train_name)
                remaining_trains.pop(0)
                resume_train_num = None
                resume_service = None
                resume_trains_per_class = None

                if remaining_trains:
                    exit_game()
                    need_launch = True

            except ServiceError as e:
                key = (e.train_name, e.train_number, e.service_number)
                retry_counts[key] = retry_counts.get(key, 0) + 1
                attempts = retry_counts[key]

                print(f"\n{'!'*60}")
                print(f"!!! FAILURE: {e}")
                print(f"!!! Attempt {attempts}/{config.MAX_RETRIES_PER_SERVICE}")
                print(f"{'!'*60}\n")

                if attempts >= config.MAX_RETRIES_PER_SERVICE:
                    resume_service = e.service_number + 1
                else:
                    resume_service = e.service_number

                resume_train_num = e.train_number
                save_progress(train_name, resume_train_num, resume_service,
                              choice=current_choice,
                              trains_per_class=cached_train_count[0])
                kill_game()
                need_launch = True

            except BatchRestart as e:
                print(f"\n{'='*60}")
                print(f"=== BATCH RESTART: resuming at {e.train_name} "
                      f"T{e.train_number} S{e.service_number}")
                print(f"{'='*60}\n")
                resume_train_num = e.train_number
                resume_service = e.service_number
                save_progress(train_name, resume_train_num, resume_service,
                              choice=current_choice,
                              trains_per_class=cached_train_count[0])
                kill_game()
                need_launch = True

            except TimeoutError as e:
                print(f"\n{'!'*60}")
                print(f"!!! NAVIGATION FAILURE: {e}")
                print(f"{'!'*60}\n")
                save_progress(train_name, resume_train_num or 1,
                              resume_service or 1, choice=current_choice,
                              trains_per_class=cached_train_count[0])
                kill_game()
                need_launch = True

        # Clear resume state between choices
        resume_train_name = None
        resume_train_num = None
        resume_service = None
        resume_trains_per_class = None

        if choice != choices[-1]:
            try:
                exit_game()
            except TimeoutError:
                kill_game()
            need_launch = True

    clear_progress()
    print(f"\nRoute '{config.ROUTE_NAME}' complete.")
    try:
        exit_game()
    except TimeoutError:
        kill_game()


def main():
    print("=== TSW Timetable Bot (New Method — LLM Assisted) ===")
    print(f"    LM Studio: {LM_STUDIO_URL}")
    print(f"    Vision Model: {VISION_MODEL}")
    print(f"    Max services/train: {MAX_SERVICES}\n")

    route_names = config.ROUTE_NAMES
    if not route_names:
        print("ERROR: config.ROUTE_NAMES is empty — add at least one route.")
        sys.exit(1)

    run_report.run_start(route_names)

    print(f"Routes queued: {len(route_names)}")
    for i, name in enumerate(route_names, 1):
        print(f"  {i}. {name}")
    print()

    skipped = []
    completed = []

    try:
        for route_idx, route_name in enumerate(route_names):
            config.ROUTE_NAME = route_name
            reset_route_cache()
            print(f"\n>>> Route {route_idx + 1}/{len(route_names)}: {route_name}")
            run_report.route_start(route_name, route_idx + 1, len(route_names))
            try:
                run_single_route()
                completed.append(route_name)
                run_report.route_end(route_name)
            except Exception as e:
                msg = str(e)
                print(f"\n*** SKIPPED route '{route_name}': {msg}\n")
                run_report.route_skipped(route_name, msg)
                skipped.append(route_name)
                continue

        print(f"\n{'='*60}")
        print(f"=== ALL {len(route_names)} ROUTE(S) PROCESSED ===")
        print(f"    Completed: {len(completed)}  |  Skipped: {len(skipped)}")
        if skipped:
            print(f"    Skipped routes: {', '.join(skipped)}")
        print(f"{'='*60}")

        run_report.run_end(completed, skipped)

    except KeyboardInterrupt:
        run_report.run_stopped()
        print("\nBot stopped by user.")
        sys.exit(0)


if __name__ == "__main__":
    main()
