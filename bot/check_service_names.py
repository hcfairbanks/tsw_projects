"""Check service names captured by the bot against the in-game service index.

Reads service_name.txt files from the screenshots folder, truncates timestamps,
then searches each name per-train in the game's service filter.
If no service boxes appear after filtering, the name is incorrect.

Outputs incorrect_service_names.txt in the route folder so the user can
correct them in the HUD web app.

Usage:
    cd bot/
    python check_service_names.py                    # all classes
    python check_service_names.py "Class 350/1"      # single class
"""

import os
import re
import sys
import time

import numpy as np
import pyautogui

pyautogui.FAILSAFE = False

import config
from utils import wait_and_click
from navigator import click_train, select_train


# ---------------------------------------------------------------------------
# Step 1: Read all service names from screenshot folders
# ---------------------------------------------------------------------------

def read_all_service_names():
    """Walk the screenshots folder and read every service_name.txt.

    Returns a list of (class_name, train_number, service_number, raw_service_name) tuples.
    """
    route_dir = os.path.join(config.SCREENSHOTS_DIR, config.ROUTE_NAME)
    if not os.path.isdir(route_dir):
        print(f"ERROR: Route directory not found: {route_dir}")
        sys.exit(1)

    entries = []
    for root, _dirs, files in os.walk(route_dir):
        if "service_name.txt" not in files:
            continue

        # Parse path: route_dir / {class_parts...} / train_XX / service_XXX
        rel = os.path.relpath(root, route_dir)
        parts = rel.split(os.sep)

        # Find the train_XX component
        train_idx = None
        for i, p in enumerate(parts):
            if re.match(r"^train_\d+$", p):
                train_idx = i
                break

        if train_idx is None:
            continue  # unexpected structure

        class_name = "/".join(parts[:train_idx])
        train_number = int(parts[train_idx].split("_")[1])

        # Extract service number from service_XXX folder
        service_folder = parts[train_idx + 1] if len(parts) > train_idx + 1 else None
        service_match = re.match(r"^service_(\d+)$", service_folder) if service_folder else None
        service_number = int(service_match.group(1)) if service_match else 0

        name_path = os.path.join(root, "service_name.txt")
        with open(name_path, "r", encoding="utf-8") as f:
            raw_name = f.read().strip()

        if raw_name:
            entries.append((class_name, train_number, service_number, raw_name))

    entries.sort(key=lambda e: (e[0], e[1], e[2]))
    return entries


# ---------------------------------------------------------------------------
# Step 2: Truncate timestamps from service names
# ---------------------------------------------------------------------------

def truncate_timestamps(name):
    """Remove the two trailing tokens (timestamps) from a service name.

    Example:
        "0537: Wembley Inter City Depot - London Euston 04:31 00:13"
        → "0537: Wembley Inter City Depot - London Euston"
    """
    parts = name.strip().split()
    if len(parts) <= 2:
        return name.strip()
    return " ".join(parts[:-2])


# ---------------------------------------------------------------------------
# Step 3: Organise by class → train → unique names
# ---------------------------------------------------------------------------

def organise_by_class_and_train(entries):
    """Group entries into class → train → list of unique truncated names.

    Returns:
        class_trains: dict {class_name: {train_num: [unique_truncated_names]}}
        name_to_services: dict {(class_name, train_num, truncated_name): [service_numbers]}
    """
    class_trains = {}     # {class: {train: set(names)}}
    name_to_services = {} # {(class, train, name): [service_nums]}

    for class_name, train_num, service_num, raw_name in entries:
        truncated = truncate_timestamps(raw_name)

        class_trains.setdefault(class_name, {}).setdefault(train_num, set()).add(truncated)
        name_to_services.setdefault((class_name, train_num, truncated), []).append(service_num)

    return class_trains, name_to_services


# ---------------------------------------------------------------------------
# Step 4: Search in the game's service index
# ---------------------------------------------------------------------------

def has_service_boxes(debug_path=None):
    """Check if any service boxes are visible in the service list area.

    The service index boxes use green (#059744) and light blue (#c5e4e9) borders,
    which are 18px tall x 1470px wide. We scan for either color.

    If debug_path is set, saves a screenshot of the region for inspection.
    """
    region = (
        config.SERVICE_LIST_LEFT,
        config.SERVICE_LIST_TOP,
        config.SERVICE_LIST_RIGHT - config.SERVICE_LIST_LEFT,
        config.SERVICE_LIST_BOTTOM - config.SERVICE_LIST_TOP,
    )
    img = np.array(pyautogui.screenshot(region=region))

    # Save debug screenshot if requested
    if debug_path:
        from PIL import Image
        Image.fromarray(img).save(debug_path)

    tol = config.SERVICE_BOX_COLOR_TOLERANCE

    # Service index boxes use these border colors (same as schedule borders)
    green = np.array(config.SCHEDULE_TOP_BORDER_RGB)       # #059744
    light_blue = np.array(config.SCHEDULE_BOTTOM_BORDER_RGB)  # #c5e4e9

    img_int = img.astype(int)
    green_match = np.sum(np.all(np.abs(img_int - green) <= tol, axis=2))
    blue_match = np.sum(np.all(np.abs(img_int - light_blue) <= tol, axis=2))
    total = green_match + blue_match

    print(f"    Matching pixels: green={green_match}, light_blue={blue_match}, total={total}")

    return bool(total > 50)


def check_name_in_game(name, debug_path=None):
    """Type a service name into the game's filter and check for results.

    Returns True if service boxes appear (name is valid),
    False if no boxes appear (name is incorrect).
    """
    # Click the service filter field
    if not wait_and_click(config.REF_SERVICE_FILTER, timeout=10,
                          confidence=config.CONFIDENCE, verify=False):
        print("  ERROR: Could not find service filter field")
        return None  # unknown — couldn't interact

    time.sleep(0.5)

    # Clear existing text and type the name
    pyautogui.hotkey("ctrl", "a")
    time.sleep(0.2)
    pyautogui.typewrite(name, interval=0.02)
    time.sleep(2.0)  # wait for filter results to appear

    # Check if any service boxes are visible
    found = has_service_boxes(debug_path=debug_path)

    # Clear the filter for the next search
    pyautogui.hotkey("ctrl", "a")
    time.sleep(0.2)
    pyautogui.press("delete")
    time.sleep(1.0)  # wait for full list to reload

    return found


def go_back_to_train_list():
    """Press Escape twice to return from the service list to the train list."""
    print("    Pressing Escape (x2) to return to train list...")
    pyautogui.press("escape")
    time.sleep(2.0)
    pyautogui.press("escape")
    time.sleep(5.0)


# ---------------------------------------------------------------------------
# Step 5: Write results
# ---------------------------------------------------------------------------

def write_results(incorrect_per_train, name_to_services, route_dir):
    """Write incorrect_service_names.txt with each unique bad name listed once.

    For each incorrect name, finds the first occurrence (train + service) so the
    user knows where to find the screenshot to correct it.

    Format:
        Class 350/1
          Train 1 / Service 004
            2ND4: Milton Keynes - London Euston
          Train 1 / Service 003
            2NDO: Northampton - London Euston

    incorrect_per_train: dict {(class_name, train_num): [incorrect_names]}
    name_to_services: dict {(class_name, train_num, name): [service_numbers]}
    """
    # Collect unique incorrect names per class with their first occurrence
    by_class = {}  # {cls: [(train_num, service_num, name)]}
    seen = {}      # {(cls, name): True} — dedup across trains

    for (cls, train_num), bad_names in sorted(incorrect_per_train.items()):
        for name in bad_names:
            if (cls, name) in seen:
                continue
            seen[(cls, name)] = True
            services = name_to_services.get((cls, train_num, name), [])
            svc = min(services) if services else 0
            by_class.setdefault(cls, []).append((train_num, svc, name))

    lines = []
    for cls in sorted(by_class.keys()):
        lines.append(cls)
        for train_num, svc, name in by_class[cls]:
            lines.append(f"  Train {train_num} / Service {svc:03d}")
            lines.append(f"    {name}")
        lines.append("")

    output_path = os.path.join(route_dir, "incorrect_service_names.txt")
    with open(output_path, "w", encoding="utf-8") as f:
        f.write("\n".join(lines))

    return output_path


def write_good_names(entries, incorrect_per_train, route_dir):
    """Write service_names_to_review.txt with all names NOT flagged as incorrect.

    These are names that matched in the game's service filter but may still
    have OCR errors in the timestamps, requiring visual inspection.

    Format:
        Class 350/1
          Train 1 / Service 001
            0537: Wembley Inter City Depot - London Euston 04:31 00:13
          Train 1 / Service 002
            0542: London Euston - Northampton 05:42 01:23
    """
    # Collect all incorrect truncated names per class for quick lookup
    bad_names_by_class = {}  # {cls: set(truncated_names)}
    for (cls, _train_num), bad_list in incorrect_per_train.items():
        bad_names_by_class.setdefault(cls, set()).update(bad_list)

    # Group good entries by class
    by_class = {}  # {cls: [(train, service, raw_name)]}
    for class_name, train_num, service_num, raw_name in entries:
        truncated = truncate_timestamps(raw_name)
        if truncated in bad_names_by_class.get(class_name, set()):
            continue
        by_class.setdefault(class_name, []).append(
            (train_num, service_num, raw_name)
        )

    lines = []
    for cls in sorted(by_class.keys()):
        lines.append(cls)
        for train_num, svc, raw_name in by_class[cls]:
            lines.append(f"  Train {train_num} / Service {svc:03d}")
            lines.append(f"    {raw_name}")
        lines.append("")

    output_path = os.path.join(route_dir, "service_names_to_review.txt")
    with open(output_path, "w", encoding="utf-8") as f:
        f.write("\n".join(lines))

    return output_path


# ---------------------------------------------------------------------------
# Main
# ---------------------------------------------------------------------------

def main(class_filter=None):
    print("=== Service Name Checker ===\n")

    # Optional class filter from command line or parameter
    if class_filter is None and len(sys.argv) > 1:
        class_filter = sys.argv[1]
    if class_filter:
        print(f"Filtering to class: {class_filter}\n")

    # Step 1: Read all service names
    print("Step 1: Reading service names from files...")
    entries = read_all_service_names()
    if class_filter:
        entries = [(c, t, s, n) for c, t, s, n in entries if c == class_filter]
    print(f"  Found {len(entries)} service name files.\n")

    if not entries:
        print("No service names found. Nothing to check.")
        return

    # Step 2 & 3: Organise by class and train
    print("Step 2-3: Truncating timestamps and organising by class/train...")
    class_trains, name_to_services = organise_by_class_and_train(entries)

    classes = sorted(class_trains.keys())
    multi_class = len(classes) > 1

    for cls in classes:
        trains = sorted(class_trains[cls].keys())
        names_per_train = [len(class_trains[cls][t]) for t in trains]
        print(f"  {cls}: {len(trains)} trains, "
              f"{sum(names_per_train)} unique names across trains")
    print()

    # Check reference image exists
    if not os.path.isfile(config.REF_SERVICE_FILTER):
        print(f"ERROR: Reference image not found: {config.REF_SERVICE_FILTER}")
        print("Please capture a screenshot of the service filter field in the game")
        print(f"and save it as: {os.path.basename(config.REF_SERVICE_FILTER)}")
        print(f"in: {config.REFERENCES_DIR}")
        sys.exit(1)

    # Debug output dir
    debug_dir = os.path.join(config.SCREENSHOTS_DIR, config.ROUTE_NAME, "debug")
    os.makedirs(debug_dir, exist_ok=True)

    # Step 4: Search per class — check each unique name once per class,
    # then apply the result to all trains that share that name.
    incorrect_per_train = {}  # {(class, train): [bad_names]}

    for cls in classes:
        trains = sorted(class_trains[cls].keys())
        print(f"\n{'#'*60}")
        print(f"### Class: {cls} ({len(trains)} trains)")
        print(f"{'#'*60}")

        if cls == classes[0]:
            print(f"\n  Make sure the game is on the class filter screen.")
            print("  Starting in 5 seconds...")
            time.sleep(5.0)

        # Select the train to get to the train list
        select_train(cls)
        print(f"  Train list loaded for '{cls}'.\n")

        # Check each train's names, caching results so duplicates are skipped
        checked = {}  # {name: True/False/None} — cache across trains
        route_dir = os.path.join(config.SCREENSHOTS_DIR, config.ROUTE_NAME)
        on_service_list = False

        for train_num in trains:
            unique_names = sorted(class_trains[cls][train_num])

            # How many names actually need checking on this train?
            unchecked = [n for n in unique_names if n not in checked]
            print(f"\n  --- Train {train_num} ({len(unique_names)} names, "
                  f"{len(unchecked)} to check) ---")

            if unchecked:
                # Go back to train list if we're on a service list from previous train
                if on_service_list:
                    go_back_to_train_list()

                # Navigate to this train's service list
                click_train(train_num - 1)
                print(f"  Service list loaded for Train {train_num}.\n")
                on_service_list = True

                for i, name in enumerate(unchecked):
                    print(f"    [{i + 1}/{len(unchecked)}] {name}")
                    safe_name = re.sub(r'[^\w\-]', '_', name)[:40]
                    safe_cls = re.sub(r'[^\w\-]', '_', cls)
                    dbg = os.path.join(debug_dir, f"check_{safe_cls}_T{train_num}_{safe_name}.png")
                    result = check_name_in_game(name, debug_path=dbg)

                    if result is None:
                        print(f"      SKIP — could not interact with filter")
                    elif result:
                        print(f"      OK")
                    else:
                        print(f"      INCORRECT")
                    checked[name] = result
                    time.sleep(0.5)
            else:
                print(f"  All names already checked, skipping.")

            # Build bad names list from cache (includes both fresh and cached results)
            bad_names = [n for n in unique_names if checked.get(n) is False]
            incorrect_per_train[(cls, train_num)] = bad_names

            found = len(unique_names) - len(bad_names)
            print(f"\n  Train {train_num} result: {found} OK, {len(bad_names)} incorrect")

            write_results(incorrect_per_train, name_to_services, route_dir)

        total_ok = sum(1 for v in checked.values() if v is True)
        total_bad = sum(1 for v in checked.values() if v is False)
        print(f"\n  Class '{cls}' result: {total_ok} OK, {total_bad} incorrect "
              f"(out of {len(checked)} unique names)")

        # Go back to class filter screen for the next class
        if cls != classes[-1]:
            print(f"\n  Returning to class filter screen...")
            if on_service_list:
                # Escape x1: service list → train list
                pyautogui.press("escape")
                time.sleep(2.0)
            # Escape x1: train list → class screen
            pyautogui.press("escape")
            time.sleep(5.0)

    # Summary
    total_checked = sum(len(class_trains[c][t]) for c in classes for t in class_trains[c])
    total_incorrect = sum(len(v) for v in incorrect_per_train.values())
    print(f"\n{'='*60}")
    print(f"Results: {total_incorrect} incorrect out of {total_checked} checked.")

    route_dir = os.path.join(config.SCREENSHOTS_DIR, config.ROUTE_NAME)
    output_path = os.path.join(route_dir, "incorrect_service_names.txt")
    if total_incorrect > 0:
        print(f"Incorrect names written to: {output_path}")
    else:
        print("All service names are correct!")

    # Write the good names list for visual timestamp inspection
    good_path = write_good_names(entries, incorrect_per_train, route_dir)
    good_count = total_checked - total_incorrect
    print(f"\n{good_count} service names to review for timestamps: {good_path}")

    print("\nDone.")


if __name__ == "__main__":
    main()
