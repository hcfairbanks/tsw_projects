"""Test clicking on services from CSV click-map data.

Navigates the game to the service list, then scrolls to any service
from the CSV click-map and clicks it.

Usage:
    python ai_test_click.py              # interactive — pick a service number
    python ai_test_click.py 42           # go straight to service #42
    python ai_test_click.py 42 100 200   # test services #42, #100, #200 in sequence
    python ai_test_click.py --no-launch 42  # skip game launch, still scrolls from top
    python ai_test_click.py --csv path/to/file.csv 42  # use a specific CSV file
"""

import csv
import os
import re
import sys
import time

import numpy as np
import pyautogui
pyautogui.FAILSAFE = False
from PIL import Image

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
)
from uploader import get_trains

DEFAULT_CSV = os.path.join(config.BASE_DIR, "compare", "1.csv")
DEBUG_DIR = os.path.join(config.BASE_DIR, "ai_captures", "click_debug")


def load_results(csv_path=None):
    """Load services from a CSV click-map file.

    CSV columns: number, page, click, service_id, service_name, time, duration
    The 'click' column has format: click (X Y)
    """
    csv_path = csv_path or DEFAULT_CSV
    if not os.path.isfile(csv_path):
        print(f"ERROR: CSV file not found at {csv_path}")
        sys.exit(1)

    services = []
    with open(csv_path, "r", newline="") as f:
        reader = csv.DictReader(f)
        for row in reader:
            # Parse click coordinates from "click (X Y)"
            m = re.match(r"click \((\d+)\s+(\d+)\)", row["click"])
            if not m:
                print(f"WARNING: Could not parse click coords from '{row['click']}', skipping row {row['number']}")
                continue

            click_x = int(m.group(1))
            click_y = int(m.group(2))
            page_num = int(re.search(r"\d+", row["page"]).group())

            services.append({
                "global_index": int(row["number"]),
                "page": page_num,
                "click_x": click_x,
                "click_y": click_y,
                "service_id": row["service_id"],
                "name": row.get("service_name") or row.get("route", ""),
                "start_time": row["time"],
                "duration": row["duration"],
            })

    if not services:
        print("ERROR: No services parsed from CSV.")
        sys.exit(1)

    print(f"Loaded {len(services)} services from {csv_path}")
    return services


def find_service_by_timestamps(service):
    """OCR the service list area and find the target service by its timestamps.

    Returns (click_x, click_y) in screen coordinates, or None if not found.
    The service's start_time and duration are matched against OCR results.
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

    # OCR with bounding box data
    data = pytesseract.image_to_data(img, output_type=pytesseract.Output.DICT)

    # Find all HH:MM time strings and their Y positions
    time_entries = []
    for i, text in enumerate(data["text"]):
        text = str(text).strip()
        if re.match(r"^\d{2}:\d{2}$", text):
            y = data["top"][i]
            h = data["height"][i]
            center_y = y + h // 2
            time_entries.append({"text": text, "y": center_y, "top": y, "height": h})

    print(f"  OCR found {len(time_entries)} time values: {[(t['text'], t['y']) for t in time_entries]}")

    # Target times from CSV (strip seconds → HH:MM)
    target_time = service["start_time"][:5]     # "10:05:00" → "10:05"
    target_dur = service["duration"][:5]         # "00:24:00" → "00:24"

    print(f"  Looking for time={target_time} + duration={target_dur}")

    # Times come in pairs: (start_time, duration) on the same row.
    # Group OCR times by similar Y (within 10px = same row)
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

    # Search for a row matching both target_time and target_dur
    for row in rows:
        texts = [t["text"] for t in row]
        if target_time in texts and target_dur in texts:
            avg_y = sum(t["y"] for t in row) // len(row)
            screen_y = config.SERVICE_LIST_TOP + avg_y
            print(f"  MATCH: found {texts} at relative Y={avg_y} → screen Y={screen_y}")
            return (cx, screen_y)

    # Fallback: match just the start time (duration might OCR badly)
    for row in rows:
        texts = [t["text"] for t in row]
        if target_time in texts:
            avg_y = sum(t["y"] for t in row) // len(row)
            screen_y = config.SERVICE_LIST_TOP + avg_y
            print(f"  PARTIAL MATCH (time only): found {texts} at relative Y={avg_y} → screen Y={screen_y}")
            return (cx, screen_y)

    print(f"  WARNING: Could not find timestamps {target_time} / {target_dur} on screen")
    return None


def scroll_to_top():
    """Scroll the service list all the way to the top."""
    center_x = (config.SERVICE_LIST_LEFT + config.SERVICE_LIST_RIGHT) // 2
    center_y = (config.SERVICE_LIST_TOP + config.SERVICE_LIST_BOTTOM) // 2
    region = (config.SERVICE_LIST_LEFT, config.SERVICE_LIST_TOP,
              config.SERVICE_LIST_RIGHT - config.SERVICE_LIST_LEFT,
              config.SERVICE_LIST_BOTTOM - config.SERVICE_LIST_TOP)
    scroll_up = -config.SCROLL_PER_BOX * 8

    pyautogui.moveTo(center_x, center_y)
    time.sleep(0.3)
    prev_img = np.array(pyautogui.screenshot(region=region))
    for _ in range(60):
        pyautogui.scroll(scroll_up)
        time.sleep(0.5)
        curr_img = np.array(pyautogui.screenshot(region=region))
        if curr_img.shape == prev_img.shape:
            diff = np.mean(np.abs(curr_img.astype(float) - prev_img.astype(float)))
            if diff < 5.0:
                break
        prev_img = curr_img
    time.sleep(0.5)
    print("At top of service list.")


def scroll_to_page(target_page):
    """Scroll from the top of the list to the target page number."""
    if target_page == 0:
        print(f"Page 0 — no scrolling needed.")
        return

    center_x = (config.SERVICE_LIST_LEFT + config.SERVICE_LIST_RIGHT) // 2
    center_y = (config.SERVICE_LIST_TOP + config.SERVICE_LIST_BOTTOM) // 2
    pyautogui.moveTo(center_x, center_y)
    time.sleep(0.3)

    print(f"  Scrolling {target_page} page(s) down...")
    print(f"  SCROLL_AMOUNT = {config.SCROLL_AMOUNT} per page")
    print(f"  BOXES_TO_SCROLL = {config.BOXES_TO_SCROLL}, "
          f"SCROLL_PER_BOX = {config.SCROLL_PER_BOX}")

    for p in range(target_page):
        pyautogui.scroll(config.SCROLL_AMOUNT)
        time.sleep(1.5)
        print(f"    Scrolled page {p+1}/{target_page} "
              f"(total scroll: {config.SCROLL_AMOUNT * (p+1)})")

    time.sleep(0.5)
    print(f"  Done — now at page {target_page}.")


def capture_debug_screenshot(service, label=""):
    """Take a screenshot of the service list area and save it with a crosshair
    marking where the click will/did land."""
    os.makedirs(DEBUG_DIR, exist_ok=True)

    gi = service["global_index"]
    cx = service["click_x"]
    cy = service["click_y"]

    # Capture the service list region
    region = (
        config.SERVICE_LIST_LEFT,
        config.SERVICE_LIST_TOP,
        config.SERVICE_LIST_RIGHT - config.SERVICE_LIST_LEFT,
        config.SERVICE_LIST_BOTTOM - config.SERVICE_LIST_TOP,
    )
    img = pyautogui.screenshot(region=region)

    # Draw a crosshair at the click position (relative to region)
    from PIL import ImageDraw
    draw = ImageDraw.Draw(img)
    rel_x = cx - config.SERVICE_LIST_LEFT
    rel_y = cy - config.SERVICE_LIST_TOP
    cross_size = 15
    # Red crosshair
    draw.line([(rel_x - cross_size, rel_y), (rel_x + cross_size, rel_y)],
              fill=(255, 0, 0), width=3)
    draw.line([(rel_x, rel_y - cross_size), (rel_x, rel_y + cross_size)],
              fill=(255, 0, 0), width=3)
    # Label
    sid = service.get("service_id", "?")
    draw.text((10, 10), f"#{gi} {sid} page={service['page']} ({cx},{cy}) {label}",
              fill=(255, 0, 0))

    filename = f"click_debug_{gi:03d}_{label}.png"
    path = os.path.join(DEBUG_DIR, filename)
    img.save(path)
    print(f"  Debug screenshot: {filename}")
    return path


def click_service(service, all_services):
    """Find service on screen by OCR timestamp matching, then click it."""
    name = service.get("name", "?")
    gi = service["global_index"]
    page = service["page"]
    sid = service.get("service_id", "?")

    print(f"\n{'='*60}")
    print(f"  Service #{gi}: {sid} — {name}")
    print(f"  Page: {page}")
    print(f"  Start: {service.get('start_time', '?')}  Duration: {service.get('duration', '?')}")

    # OCR the visible service list to find the target timestamps
    result = find_service_by_timestamps(service)

    if result is None:
        print(f"  ERROR: Could not locate service on screen via OCR!")
        print(f"{'='*60}")
        return

    cx, cy = result
    print(f"  Click target: ({cx}, {cy})")
    print(f"{'='*60}")

    # Update service coords for debug screenshot
    service["click_x"] = cx
    service["click_y"] = cy

    # Screenshot BEFORE click
    capture_debug_screenshot(service, "before")

    # Use the game's click method: moveTo + mouseDown/mouseUp
    pyautogui.moveTo(cx, cy)
    time.sleep(0.5)
    pyautogui.mouseDown()
    time.sleep(0.2)
    pyautogui.mouseUp()
    time.sleep(3)

    # Screenshot AFTER click
    capture_debug_screenshot(service, "after")


def navigate_to_service_list():
    """Launch game and navigate to the service list."""
    print("Launching game and navigating to service list...")
    launch_game()
    pass_warning_screen()
    pass_splash_screen()
    click_to_the_trains()
    click_choose_a_route()
    select_route()
    click_timetable()

    from uploader import get_sections
    db_sections = get_sections()
    if db_sections:
        select_extra_section(db_sections[0])

    all_trains = get_trains()
    first_train_class = all_trains[0]
    print(f"\nSelecting train class: {first_train_class}")
    select_train(first_train_class)

    print("Clicking first train...")
    click_train(0)
    time.sleep(3)
    print("At service list.\n")


def go_to_service(service, current_page, all_services):
    """Scroll from top to the correct page and click a service.

    Always scrolls from top first for consistent positioning.
    Returns the page we scrolled to.
    """
    target_page = service["page"]

    # Always scroll from top for consistent box positions
    print(f"\nScrolling to top, then to page {target_page}...")
    scroll_to_top()
    scroll_to_page(target_page)

    click_service(service, all_services)
    return target_page


def print_service_table(services):
    """Print a compact table of all services."""
    print(f"\n{'#':>4}  {'Page':>4}  {'Start':>8}  {'Dur':>8}  {'SvcID':<10}  Route")
    print(f"{'─'*4}  {'─'*4}  {'─'*8}  {'─'*8}  {'─'*10}  {'─'*40}")
    for s in services:
        gi = s["global_index"]
        page = s["page"]
        name = s.get("name", "?")[:40]
        start = s.get("start_time", "?")
        dur = s.get("duration", "?")
        sid = s.get("service_id", "?")[:10]
        print(f"{gi:4d}  {page:4d}  {start:>8}  {dur:>8}  {sid:<10}  {name}")


def main():
    # Parse flags
    args = sys.argv[1:]
    no_launch = "--no-launch" in args or "--on-page" in args
    args = [a for a in args if a not in ("--no-launch", "--on-page")]

    csv_path = None
    if "--csv" in args:
        csv_idx = args.index("--csv")
        if csv_idx + 1 < len(args):
            csv_path = args[csv_idx + 1]
            args = args[:csv_idx] + args[csv_idx + 2:]
        else:
            print("ERROR: --csv requires a file path argument")
            sys.exit(1)

    services = load_results(csv_path)

    # Build lookup by global_index
    by_index = {s["global_index"]: s for s in services}

    # Parse service numbers
    if args:
        targets = []
        for arg in args:
            try:
                idx = int(arg)
                if idx in by_index:
                    targets.append(idx)
                else:
                    print(f"WARNING: Service #{idx} not found (valid: 1-{len(services)})")
            except ValueError:
                print(f"WARNING: Ignoring invalid argument '{arg}'")
        if not targets:
            print("No valid service numbers provided.")
            sys.exit(1)
    else:
        # Interactive mode — show table and ask
        print_service_table(services)
        print(f"\nEnter service number(s) to test (1-{len(services)}), comma-separated:")
        try:
            line = input("> ").strip()
        except (EOFError, KeyboardInterrupt):
            print("\nAborted.")
            return
        targets = []
        for part in line.replace(",", " ").split():
            try:
                idx = int(part)
                if idx in by_index:
                    targets.append(idx)
                else:
                    print(f"  WARNING: #{idx} not found")
            except ValueError:
                pass
        if not targets:
            print("No valid service numbers.")
            return

    print(f"\nWill test {len(targets)} service(s): {targets}")
    for idx in targets:
        s = by_index[idx]
        print(f"  #{idx}: page {s['page']} — {s.get('service_id', '?')} {s.get('name', '?')} ({s.get('start_time', '?')})")

    # Navigate to service list
    if no_launch:
        print("\n--no-launch: Skipping game navigation (assuming already at service list)")
    else:
        try:
            navigate_to_service_list()
        except KeyboardInterrupt:
            print("\nAborted.")
            kill_game()
            return
        except Exception as e:
            print(f"\nERROR during navigation: {e}")
            import traceback
            traceback.print_exc()
            kill_game()
            return

    # Test each service
    current_page = -1  # unknown
    try:
        for idx in targets:
            svc = by_index[idx]
            current_page = go_to_service(svc, current_page, services)

            print(f"\n  >>> Service #{idx} clicked. Check the game screen.")
            if len(targets) > 1:
                print("  Press Enter to continue to next service (or Ctrl+C to stop)...")
                try:
                    input()
                except EOFError:
                    pass

                # Go back to service list (press Escape or click back)
                print("  Pressing Escape to go back to service list...")
                pyautogui.press("escape")
                time.sleep(2)

        print(f"\nAll {len(targets)} service(s) tested.")
        print("Press Enter to exit the game (or Ctrl+C to leave it running)...")
        try:
            input()
            exit_game()
        except (EOFError, KeyboardInterrupt):
            print("\nLeaving game running.")

    except KeyboardInterrupt:
        print("\nStopped by user. Game left running.")


if __name__ == "__main__":
    main()
