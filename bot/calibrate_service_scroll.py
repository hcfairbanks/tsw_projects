"""Calibrate service list scroll positions.

Assumes the game is already on the service list screen.
Scrolls to top, then walks through every service in the CSV,
clicking each one and asking the user to verify or adjust.

Progress is saved to disk after every service so you can quit (Q)
and resume later — the script picks up where you left off.

All input is via GLOBAL hotkeys (keyboard library) so you never
need to switch focus away from the game window.

Keys (work even when game is focused):
  V          = verified, move to next service
  N          = wrong position, enter adjustment mode
  S          = skip this service
  Q          = quit calibration (resume later)

Adjustment mode:
  Up arrow   = nudge click UP by 5px and re-click
  Down arrow = nudge click DOWN by 5px and re-click
  Page Up    = nudge click UP by 20px and re-click
  Page Down  = nudge click DOWN by 20px and re-click
  C          = confirm this position (record adjustment)
  R          = re-click at current position

Usage:
    python calibrate_service_scroll.py              # start or resume
    python calibrate_service_scroll.py --restart     # wipe progress and start fresh
    python calibrate_service_scroll.py --step 5      # test just service #5 then stop
    python calibrate_service_scroll.py --from 87     # start from service #87 and continue
    python calibrate_service_scroll.py --service 5   # alias for --step
"""

import csv
import json
import os
import re
import sys
import time
import threading

import keyboard
import numpy as np
import pyautogui
from PIL import Image

pyautogui.FAILSAFE = False

import config
from service_loop_scroll_method import (
    scroll_service_list_to_top,
)


def check_highlight(click_x, click_y, margin=50):
    """Quick local highlight check around the click point. Returns True/False."""
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
    return np.sum(match) >= 200

# ── Hardcoded CSV for now ────────────────────────────────────────────
CSV_PATH = os.path.join(
    config.SCREENSHOTS_DIR,
    "London Underground Bakerloo Line",
    "Bakerloo Line 2021",
    "1972 MkII Tube Stock LU",
    "train_01",
    "original_services.csv",
)



OUTPUT_PATH = os.path.join(config.BASE_DIR, "calibration_results.csv")
PROGRESS_PATH = os.path.join(config.BASE_DIR, "calibration_progress.json")

CLICK_REGISTRY_PATH = os.path.join(config.BASE_DIR, "click_registry.json")

# CSV fieldnames for the results file
RESULT_FIELDS = [
    "number", "page", "service_id", "service_name", "start_time", "duration",
    "original_y", "adjusted_y", "final_y", "offset", "status",
]

# Nudge amounts
NUDGE_DEFAULT = 20   # default nudge amount in px
NUDGE_STEP = 5       # Up/Down changes nudge amount by this




def load_click_registry():
    """Load click_registry.json. Returns the full dict."""
    if os.path.isfile(CLICK_REGISTRY_PATH):
        with open(CLICK_REGISTRY_PATH, "r") as f:
            return json.load(f)
    return {"_format": "relative_to_game_window", "entries": {}}


def save_click_registry(reg):
    """Write click_registry.json atomically."""
    tmp = CLICK_REGISTRY_PATH + ".tmp"
    with open(tmp, "w") as f:
        json.dump(reg, f, indent=2)
    os.replace(tmp, CLICK_REGISTRY_PATH)


def update_click_registry_entry(reg, number, page, click_x, click_y):
    """Add or update a single entry in the click registry.

    Coordinates are stored as relative (subtract game window origin) to match
    the v2 convention.
    """
    gx, gy = config.GAME_REGION[0], config.GAME_REGION[1]
    if "entries" not in reg:
        reg["entries"] = {}
    reg["entries"][str(number)] = {
        "page": page,
        "scrolls": page * config.BOXES_TO_SCROLL,
        "click_x": click_x - gx,
        "click_y": click_y - gy,
    }



def load_csv(path):
    """Load services from the experiment CSV."""
    services = []
    with open(path, "r", newline="") as f:
        reader = csv.DictReader(f)
        for row in reader:
            m = re.match(r"click \((\d+)\s+(\d+)\)", row["click"])
            if not m:
                continue
            click_x = int(m.group(1))
            click_y = int(m.group(2))
            page_num = int(re.search(r"\d+", row["page"]).group())
            services.append({
                "number": int(row["number"]),
                "page": page_num,
                "click_x": click_x,
                "click_y": click_y,
                "service_id": row.get("service_id", ""),
                "service_name": row.get("service_name") or row.get("route", ""),
                "start_time": row.get("time", ""),
                "duration": row.get("duration", ""),
            })
    return services


# ── Progress persistence ─────────────────────────────────────────────

def load_progress():
    """Load progress: returns (last_completed_number, list of result dicts)."""
    if not os.path.isfile(PROGRESS_PATH):
        return 0, []
    with open(PROGRESS_PATH, "r") as f:
        data = json.load(f)
    return data.get("last_completed", 0), data.get("results", [])


def save_progress(last_completed, results):
    """Write progress atomically."""
    data = {"last_completed": last_completed, "results": results}
    tmp = PROGRESS_PATH + ".tmp"
    with open(tmp, "w") as f:
        json.dump(data, f, indent=2)
    os.replace(tmp, PROGRESS_PATH)


def write_results_csv(results):
    """Rewrite the full results CSV from the results list."""
    with open(OUTPUT_PATH, "w", newline="") as f:
        writer = csv.DictWriter(f, fieldnames=RESULT_FIELDS)
        writer.writeheader()
        for r in results:
            writer.writerow({k: r.get(k, "") for k in RESULT_FIELDS})


def append_result(results, entry, last_completed):
    """Append a result, save progress + CSV immediately."""
    results.append(entry)
    save_progress(last_completed, results)
    write_results_csv(results)


# ── Game interaction ─────────────────────────────────────────────────

def scroll_to_page(target_page):
    """Scroll from the top of the service list to the target page."""
    if target_page == 0:
        return
    center_x = (config.SERVICE_LIST_LEFT + config.SERVICE_LIST_RIGHT) // 2
    center_y = (config.SERVICE_LIST_TOP + config.SERVICE_LIST_BOTTOM) // 2
    pyautogui.moveTo(center_x, center_y)
    time.sleep(0.3)
    for _ in range(target_page):
        pyautogui.scroll(config.SCROLL_AMOUNT)
        time.sleep(1.5)
    time.sleep(0.5)


def do_scroll(delta_scrolls):
    """Scroll by delta_scrolls units (positive=down, negative=up). Returns actual scrolls done."""
    if delta_scrolls == 0:
        return 0
    center_x = (config.SERVICE_LIST_LEFT + config.SERVICE_LIST_RIGHT) // 2
    center_y = (config.SERVICE_LIST_TOP + config.SERVICE_LIST_BOTTOM) // 2
    pyautogui.moveTo(center_x, center_y)
    time.sleep(0.3)
    steps = abs(delta_scrolls)
    for _ in range(steps):
        pyautogui.scroll(config.SCROLL_PER_BOX if delta_scrolls > 0 else -config.SCROLL_PER_BOX)
        time.sleep(0.4)
    time.sleep(0.5)
    return delta_scrolls


def click_service(x, y):
    """Click a service at the given screen coordinates."""
    list_cx = (config.SERVICE_LIST_LEFT + config.SERVICE_LIST_RIGHT) // 2
    list_cy = (config.SERVICE_LIST_TOP + config.SERVICE_LIST_BOTTOM) // 2
    pyautogui.moveTo(list_cx, list_cy)
    time.sleep(0.3)
    pyautogui.moveTo(x, y)
    time.sleep(0.5)
    pyautogui.mouseDown()
    time.sleep(0.3)
    pyautogui.mouseUp()
    time.sleep(1.5)


def deselect_service():
    """Click an empty area to deselect the current service."""
    dead_x = (config.SERVICE_LIST_LEFT + config.SERVICE_LIST_RIGHT) // 2
    dead_y = config.SERVICE_LIST_TOP - 50
    pyautogui.click(dead_x, dead_y)
    time.sleep(0.5)


def wait_for_key(valid_keys):
    """Block until one of the valid_keys is pressed globally. Returns the key name."""
    result = [None]
    event = threading.Event()

    def on_key(e):
        if e.event_type == "down" and e.name in valid_keys:
            result[0] = e.name
            event.set()

    keyboard.hook(on_key)
    event.wait()
    keyboard.unhook(on_key)
    time.sleep(0.15)
    return result[0]


# ── Main ─────────────────────────────────────────────────────────────

def print_summary(results):
    """Print summary from results."""
    adjusted = [r for r in results if r.get("status") == "adjusted"]
    if not adjusted:
        print("No adjustments recorded.")
        return

    print()
    print("=" * 60)
    print("SUMMARY OF ADJUSTMENTS")
    print("=" * 60)
    by_page = {}
    for r in adjusted:
        p = r["page"]
        if p not in by_page:
            by_page[p] = []
        by_page[p].append(r["offset"])
    for p in sorted(by_page):
        offsets = by_page[p]
        avg = sum(offsets) / len(offsets)
        print(f"  Page {p}: {len(offsets)} adjustments, avg offset={avg:+.1f}px")
        print(f"    Individual: {offsets}")

    # Overall stats
    total = len(results)
    verified = sum(1 for r in results if r.get("status") == "verified")
    skipped = sum(1 for r in results if r.get("status") == "skipped")
    adj = len(adjusted)
    print()
    print(f"Total: {total}  Verified: {verified}  Adjusted: {adj}  Skipped: {skipped}")


def parse_int_flag(flag):
    """Check for --flag N in sys.argv. Returns int or None."""
    for i, arg in enumerate(sys.argv):
        if arg == flag and i + 1 < len(sys.argv):
            return int(sys.argv[i + 1])
    return None


def run_single_service(services, target_number):
    """Test a single service by number — no progress tracking."""
    svc = None
    for s in services:
        if s["number"] == target_number:
            svc = s
            break
    if not svc:
        print(f"Service #{target_number} not found in CSV (valid: {services[0]['number']}-{services[-1]['number']})")
        return

    page = svc["page"]
    cx = svc["click_x"]
    cy = svc["click_y"]
    sid = svc["service_id"]
    service_name = svc["service_name"]
    start_time = svc["start_time"]
    duration = svc["duration"]
    adjusted_cy = cy

    scrolls = page * config.BOXES_TO_SCROLL
    print(f"Service #{target_number}  (page {page}, {scrolls} scrolls)")
    print(f"  ID:       {sid}")
    print(f"  Service:  {service_name}")
    print(f"  Time:     {start_time}")
    print(f"  Duration: {duration}")
    print(f"  Click:    ({cx}, {cy})")
    print()

    print("Starting in 3 seconds...")
    time.sleep(3)

    print(f"  Scrolling to top then to page {page}...")
    scroll_service_list_to_top()
    scroll_to_page(page)
    click_service(cx, adjusted_cy)

    highlighted = check_highlight(cx, adjusted_cy)
    print(f"  Highlight: {'yes' if highlighted else 'no'}")

    print(f"  Expected: {service_name} @ {start_time} dur {duration}")
    print(f"  V=ok  Q=quit")
    print(f"  Up/Down=set nudge amount  PgUp/PgDn=move up/down by nudge  C=confirm  R=re-click")

    current_y = adjusted_cy
    nudge_amount = NUDGE_DEFAULT

    while True:
        key = wait_for_key({"v", "q", "c", "r", "up", "down", "page up", "page down"})

        if key == "v":
            print("  Verified OK.")
            break
        elif key == "q":
            print("  Quit.")
            break
        elif key == "c":
            offset = current_y - adjusted_cy
            print(f"  Final y={current_y}  offset={offset:+d}px from adjusted")
            break
        elif key == "r":
            deselect_service()
            time.sleep(0.3)
            click_service(cx, current_y)
            hl = check_highlight(cx, current_y)
            print(f"  Re-clicked at y={current_y}  highlight={'yes' if hl else 'no'}")
        elif key == "up":
            nudge_amount += NUDGE_STEP
            print(f"  Nudge amount: {nudge_amount}px")
        elif key == "down":
            nudge_amount = max(NUDGE_STEP, nudge_amount - NUDGE_STEP)
            print(f"  Nudge amount: {nudge_amount}px")
        elif key == "page up":
            current_y -= nudge_amount
            deselect_service()
            time.sleep(0.3)
            click_service(cx, current_y)
            hl = check_highlight(cx, current_y)
            print(f"  y={current_y} (up {nudge_amount})  highlight={'yes' if hl else 'no'}")
        elif key == "page down":
            current_y += nudge_amount
            deselect_service()
            time.sleep(0.3)
            click_service(cx, current_y)
            hl = check_highlight(cx, current_y)
            print(f"  y={current_y} (down {nudge_amount})  highlight={'yes' if hl else 'no'}")

    deselect_service()
    print("Done.")


def main():
    restart = "--restart" in sys.argv
    step = parse_int_flag("--step") or parse_int_flag("--service")
    start_from = parse_int_flag("--from")

    print("=" * 60)
    print("SERVICE SCROLL CALIBRATION")
    print("=" * 60)
    print()
    print(f"CSV:      {CSV_PATH}")
    print(f"Output:   {OUTPUT_PATH}")
    print(f"Progress: {PROGRESS_PATH}")
    print()
    print("HOTKEYS (global — work even with game focused):")
    print("  V = verified    N = wrong/adjust    S = skip    Q = quit")
    print("  Adjustment: Up/Down = +/-5px  PgUp/PgDn = +/-20px  C = confirm  R = re-click")
    print()

    services = load_csv(CSV_PATH)
    print(f"Loaded {len(services)} services")

    # Load click registry for incremental updates
    reg = load_click_registry()

    # Single-step mode: test one service then stop
    if step is not None:
        print(f"\n--- Single service mode: #{step} ---\n")
        run_single_service(services, step)
        return

    # Load or reset progress
    if start_from is not None:
        print(f"--from {start_from}: starting from service #{start_from}")
        last_completed, results = start_from - 1, []
    elif restart:
        print("--restart flag: wiping previous progress")
        last_completed, results = 0, []
        save_progress(0, [])
        if os.path.isfile(OUTPUT_PATH):
            os.remove(OUTPUT_PATH)
    else:
        last_completed, results = load_progress()

    if last_completed > 0 and start_from is None:
        print(f"Resuming after service #{last_completed}  ({len(results)} results on file)")
    print()

    current_scrolls = 0   # tracks how many scroll units we've done from the top

    print("Starting in 3 seconds... make sure the game is on the service list screen.")
    time.sleep(3)

    # Scroll to top so current_scrolls=0 is accurate
    scroll_service_list_to_top()

    for i, svc in enumerate(services):
        number = svc["number"]

        # Skip already-completed services
        if number <= last_completed:
            continue

        page = svc["page"]
        cx = svc["click_x"]
        cy = svc["click_y"]
        sid = svc["service_id"]
        service_name = svc["service_name"]
        start_time = svc["start_time"]
        duration = svc["duration"]

        # Override with click registry values if available
        reg_entry = reg.get("entries", {}).get(str(number))
        if reg_entry:
            gx, gy = config.GAME_REGION[0], config.GAME_REGION[1]
            page = reg_entry["page"]
            cx = reg_entry["click_x"] + gx
            cy = reg_entry["click_y"] + gy

        adjusted_cy = cy

        print("-" * 60)
        scrolls = page * config.BOXES_TO_SCROLL
        print(f"Service {number}/{len(services)}  (page {page}, {scrolls} scrolls)")
        print(f"  ID:       {sid}")
        print(f"  Service:  {service_name}")
        print(f"  Time:     {start_time}")
        print(f"  Duration: {duration}")
        print(f"  Click:    ({cx}, {cy})")

        # Scroll to target if needed
        target_scrolls = page * config.BOXES_TO_SCROLL
        if target_scrolls != current_scrolls:
            delta = target_scrolls - current_scrolls
            direction = "down" if delta > 0 else "up"
            print(f"  Scrolling {direction} {abs(delta)} units (from {current_scrolls} to {target_scrolls})...")
            do_scroll(delta)
            current_scrolls = target_scrolls

        # Click the service
        click_service(cx, adjusted_cy)

        # Check highlight
        highlighted = check_highlight(cx, adjusted_cy)
        print(f"  Highlight: {'yes' if highlighted else 'no'}")

        print(f"  Expected: {service_name} @ {start_time} dur {duration}")
        print(f"  V=ok  S=skip  Q=quit  (at {current_scrolls} scrolls)")
        print(f"  Up/Down=set nudge amount  PgUp/PgDn=move up/down by nudge  C=confirm  R=re-click")
        print(f"  [=scroll up 1 page  ]=scroll down 1 page")

        current_y = adjusted_cy
        effective_page = page  # tracks page changes from [ and ]
        nudge_amount = NUDGE_DEFAULT
        quit_requested = False

        while True:
            key = wait_for_key({"v", "s", "q", "c", "r", "up", "down", "page up", "page down", "[", "]"})

            if key == "q":
                print("  Quitting — progress saved. Run again to resume.")
                save_progress(last_completed, results)
                save_click_registry(reg)
                print(f"  click_registry.json updated ({len(reg.get('entries', {}))} entries)")
                quit_requested = True
                break

            elif key == "s":
                print("  Skipped.")
                entry = {
                    "number": number, "page": effective_page, "service_id": sid,
                    "service_name": service_name, "start_time": start_time, "duration": duration,
                    "original_y": cy, "adjusted_y": adjusted_cy,
                    "final_y": current_y, "offset": 0, "status": "skipped",
                }
                last_completed = number
                append_result(results, entry, last_completed)
                update_click_registry_entry(reg, number, effective_page, cx, current_y)
                save_click_registry(reg)
                deselect_service()
                break

            elif key == "v":
                print("  Verified OK.")
                entry = {
                    "number": number, "page": effective_page, "service_id": sid,
                    "service_name": service_name, "start_time": start_time, "duration": duration,
                    "original_y": cy, "adjusted_y": adjusted_cy,
                    "final_y": current_y, "offset": 0, "status": "verified",
                }
                last_completed = number
                append_result(results, entry, last_completed)
                update_click_registry_entry(reg, number, effective_page, cx, current_y)
                save_click_registry(reg)
                deselect_service()
                break

            elif key == "c":
                offset = current_y - adjusted_cy
                entry = {
                    "number": number, "page": effective_page, "service_id": sid,
                    "service_name": service_name, "start_time": start_time, "duration": duration,
                    "original_y": cy, "adjusted_y": adjusted_cy,
                    "final_y": current_y, "offset": offset, "status": "adjusted",
                }
                last_completed = number
                append_result(results, entry, last_completed)
                update_click_registry_entry(reg, number, effective_page, cx, current_y)
                save_click_registry(reg)
                print(f"  Recorded: offset={offset:+d}px from adjusted position")
                print(f"  click_registry.json updated ({len(reg.get('entries', {}))} entries)")
                deselect_service()
                break

            elif key == "r":
                deselect_service()
                time.sleep(0.3)
                click_service(cx, current_y)
                hl = check_highlight(cx, current_y)
                print(f"  Re-clicked at y={current_y}  highlight={'yes' if hl else 'no'}")
            elif key == "up":
                nudge_amount += NUDGE_STEP
                print(f"  Nudge amount: {nudge_amount}px")
            elif key == "down":
                nudge_amount = max(NUDGE_STEP, nudge_amount - NUDGE_STEP)
                print(f"  Nudge amount: {nudge_amount}px")
            elif key == "page up":
                current_y -= nudge_amount
                deselect_service()
                time.sleep(0.3)
                click_service(cx, current_y)
                hl = check_highlight(cx, current_y)
                print(f"  y={current_y} (up {nudge_amount})  highlight={'yes' if hl else 'no'}")
            elif key == "page down":
                current_y += nudge_amount
                deselect_service()
                time.sleep(0.3)
                click_service(cx, current_y)
                hl = check_highlight(cx, current_y)
                print(f"  y={current_y} (down {nudge_amount})  highlight={'yes' if hl else 'no'}")
            elif key == "[":
                if current_scrolls > 0:
                    scroll_delta = min(config.BOXES_TO_SCROLL, current_scrolls)
                    deselect_service()
                    do_scroll(-scroll_delta)
                    current_scrolls -= scroll_delta
                    effective_page = current_scrolls // config.BOXES_TO_SCROLL
                    click_service(cx, current_y)
                    hl = check_highlight(cx, current_y)
                    print(f"  Scrolled up {scroll_delta} (now at {current_scrolls} scrolls, page {effective_page})  highlight={'yes' if hl else 'no'}")
                else:
                    print(f"  Already at top (0 scrolls)")
            elif key == "]":
                deselect_service()
                do_scroll(config.BOXES_TO_SCROLL)
                current_scrolls += config.BOXES_TO_SCROLL
                effective_page = current_scrolls // config.BOXES_TO_SCROLL
                click_service(cx, current_y)
                hl = check_highlight(cx, current_y)
                print(f"  Scrolled down {config.BOXES_TO_SCROLL} (now at {current_scrolls} scrolls, page {effective_page})  highlight={'yes' if hl else 'no'}")

        if quit_requested:
            break
    else:
        # Loop completed all services
        print()
        print("All services processed!")

    # Final save
    save_click_registry(reg)

    # Final summary
    print_summary(results)
    print()
    registry_count = len(reg.get("entries", {}))
    print(f"click_registry.json: {registry_count} entries")
    print(f"Results: {OUTPUT_PATH}")
    print(f"Progress: {PROGRESS_PATH}")


if __name__ == "__main__":
    main()
