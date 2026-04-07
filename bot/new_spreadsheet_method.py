"""New spreadsheet method — spreadsheet-based service discovery with LLM-assisted extraction.

Hybrid approach:
- Service discovery: reads service names from xlsx spreadsheet and types them
  into the in-game search field (from the original spreadsheet method)
- Everything else operates like new_method.py:
  - LLM-based train counting
  - LLM-based level info extraction (tonnage, car count, train length)
  - LLM-based schedule extraction
  - Same progress/resume system
  - Same batch restart logic

Usage:
    python new_spreadsheet_method.py                              # full run
    python new_spreadsheet_method.py --no-launch                  # skip game launch
    python new_spreadsheet_method.py "Class Name" <train> <svc>   # resume from specific point
"""

import atexit
import json
import os
import subprocess
import sys
import time

import pyautogui
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
)
from schedule_capture import capture_schedule
from uploader import (
    check_server,
    resolve_ids,
    get_trains,
    upload_service,
    get_existing_services,
)
from tsw_api import get_player_location
from service_loop import (
    read_spreadsheet,
    search_and_select_service,
    clear_service_search,
)
from service_loop_scroll_method import (
    ServiceError,
    BatchRestart,
    screenshot_service_box,
    wait_for_level_load,
    exit_to_main_menu,
    _return_to_main_menu_from_menus,
    navigate_to_service_list,
    navigate_to_train_list,
)
from record import (
    count_trains_llm,
    extract_level_info_from_screen,
    extract_schedule_llm_json,
)

# Progress file
PROGRESS_FILE = os.path.join(config.BASE_DIR, "progress_spreadsheet_method.json")

# Max services per train (safety cap)
MAX_SERVICES = getattr(config, "MAX_SCROLL_SERVICES", 250)


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
        return (data.get("choice"), data["train"], data["train_num"],
                data["service"], data.get("trains_per_class"))
    except (json.JSONDecodeError, KeyError):
        return None, None, None, None, None


def clear_progress():
    if os.path.isfile(PROGRESS_FILE):
        os.remove(PROGRESS_FILE)


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

def process_single_service(service_dir, svc, train_name, train_index,
                           service_index, train_dir):
    """Process one service: search by name, capture level info via LLM, schedule, upload.

    Expects to be at the service list screen.
    Returns the upload result dict, or None if the service couldn't be found.

    After completion, the game will be at the main menu (via exit_to_main_menu).
    """
    svc_name = svc["name"]
    bound = svc["bound"]
    first_station = svc["first_station"]
    print(f"       Service: {svc_name} ({bound})")

    # Search for the service by typing its name
    found, y = search_and_select_service(svc_name)
    if not found:
        print(f"       SKIPPING: Could not find '{svc_name}' in search")
        _log(train_dir, f"Service #{service_index} | NOT FOUND | {svc_name}")
        return None

    # Screenshot the selected service box (may micro-scroll, returning updated y)
    x = (config.SERVICE_LIST_LEFT + config.SERVICE_LIST_RIGHT) // 2
    _, final_y = screenshot_service_box(service_dir, y, click_x=x, click_y=y)

    # Click the highlight's actual position then press Enter twice to load the level
    if final_y != y:
        print(f"       Clicking updated position y={final_y} (was {y})")
        pyautogui.moveTo(x, final_y)
        pyautogui.click()
        time.sleep(0.5)
    pyautogui.press("enter")
    time.sleep(1.0)
    pyautogui.press("enter")

    # Extract level info before clicking 'Get Started'
    level_info_holder = [None]
    def _extract_before_start():
        if config.USE_LLM_LEVEL_INFO:
            level_info_holder[0] = extract_level_info_from_screen(service_dir)

    # Wait for level to load — captures crops/screen, runs LLM, then clicks 'Get Started'
    wait_for_level_load(click_x=x, click_y=y, service_dir=service_dir,
                        before_get_started=_extract_before_start)
    level_info = level_info_holder[0]

    # Fetch player lat/lng right after 'Get Started' is clicked
    first_lat, first_lng, _current_service = get_player_location()

    # Capture the schedule
    capture_schedule(service_dir)

    # LLM schedule extraction → structured JSON (replaces OCR when enabled)
    if config.USE_LLM_SCHEDULE:
        extract_schedule_llm_json(service_dir)

    _log(train_dir, f"Service #{service_index} | {svc_name} | images: {service_dir}")

    # Upload to parent app for OCR + database save
    result = None
    try:
        result = upload_service(
            service_dir, train_name,
            train_index + 1, service_index,
            first_station=first_station,
            bound=bound,
            service=svc_name,
            first_lat=first_lat, first_lng=first_lng,
        )
        svc_uploaded = result.get("service_name", "")
        if result["duplicate"]:
            print(f"       {svc_uploaded} (duplicate — already recorded)")
            _log(train_dir, f"Service #{service_index} | DUPLICATE | {svc_uploaded}")
        elif result["success"]:
            print(f"       {svc_uploaded}")
            _log(train_dir, f"Service #{service_index} | OK | {svc_uploaded} | timetable_id={result.get('timetable_id')}")
        else:
            print(f"       Upload error: {result['error']}")
            _log(train_dir, f"Service #{service_index} | ERROR | {result['error']}")
    except Exception as e:
        print(f"       Upload error: {e}")
        _log(train_dir, f"Service #{service_index} | ERROR | {e}")
        result = {"success": False, "error": str(e)}

    # Exit back to main menu
    exit_to_main_menu()

    return result


def process_all_services(train_dir, train_index, train_name,
                         start_service=None, on_progress=None,
                         remaining_services=None, extra_section_choice=None):
    """Iterate through services from spreadsheet, searching for each by name.

    Uses LLM for level info and schedule extraction (like new_method.py).
    Uses spreadsheet + search typing for service discovery (like service_loop.py).

    Returns at the MAIN MENU.
    """
    # Read service list from spreadsheet
    services = read_spreadsheet(train_name)
    total = len(services)
    print(f"\n       Spreadsheet: {total} services for '{train_name}'")

    batch_count = 0
    start_idx = (start_service - 1) if start_service and start_service > 1 else 0

    for svc_idx in range(start_idx, total):
        svc = services[svc_idx]
        service_number = svc_idx + 1  # 1-indexed
        svc_name = svc["name"]

        # Skip services already found on a previous train
        if remaining_services is not None and svc_name not in remaining_services:
            continue

        print(f"\n{'='*60}")
        print(f"--- Service #{service_number}/{total}: {svc_name} ({svc['bound']}) ---")
        print(f"{'='*60}")

        # Save progress
        if on_progress:
            on_progress(train_name, train_index + 1, service_number)

        # Create per-service folder
        service_dir = os.path.join(train_dir, f"service_{service_number:03d}")
        os.makedirs(service_dir, exist_ok=True)

        try:
            result = process_single_service(
                service_dir, svc,
                train_name, train_index, service_number,
                train_dir,
            )

            # Track whether upload succeeded
            upload_ok = (result is not None and
                         (result.get("success") or result.get("duplicate")))

            # Only remove from remaining once the DB confirms it's saved
            if upload_ok and remaining_services is not None:
                remaining_services.discard(svc_name)

            # All services found — no need to continue
            if remaining_services is not None and not remaining_services:
                print(f"\n       All services found! Skipping rest of list.")
                break

        except TimeoutError as e:
            raise ServiceError(
                train_name, train_index + 1, service_number, e
            ) from e

        # If service was not found, we're still on the service list screen —
        # just clear the search box so the next service name can be typed.
        # No need to re-navigate through the full menu chain.
        if result is None:
            clear_service_search()
            continue

        # Check service limit
        if service_number >= MAX_SERVICES:
            print(f"\n       Reached service limit ({MAX_SERVICES}), stopping.")
            print(f"\nProcessed {service_number} services for this train.")
            return service_number

        # Check batch limit
        if config.BATCH_SIZE is not None:
            batch_count += 1
            if batch_count >= config.BATCH_SIZE:
                print(f"\n       Batch limit ({config.BATCH_SIZE}) reached, restarting...")
                _return_to_main_menu_from_menus()
                raise BatchRestart(
                    train_name, train_index + 1, service_number + 1
                )

        # Re-navigate to the service list for the next service
        if svc_idx < total - 1:
            try:
                navigate_to_service_list(train_index, train_name,
                                         extra_section_choice=extra_section_choice)
            except TimeoutError as e:
                raise ServiceError(
                    train_name, train_index + 1, service_number, e
                ) from e

    # All services from spreadsheet processed
    print(f"\n       All {total} services processed. Returning to main menu...")
    _return_to_main_menu_from_menus()
    print(f"\nProcessed {total} services for this train.")
    return total


def process_all_trains(train_name, start_train=None, start_service=None,
                       on_progress=None, extra_section_choice=None,
                       trains_per_class=None):
    """Outer loop: iterate through all trains in the class.

    Uses LLM to count trains (or cached value from progress).
    Uses spreadsheet for service discovery.
    Uses LLM for level info and schedule extraction.
    """
    from datetime import datetime, timedelta

    run_start = time.time()

    # Create route/class folder structure
    route_dir = os.path.join(config.SCREENSHOTS_DIR, config.ROUTE_NAME)
    if extra_section_choice:
        route_dir = os.path.join(route_dir, extra_section_choice)
    class_dir = os.path.join(route_dir, train_name)
    os.makedirs(class_dir, exist_ok=True)

    # Read spreadsheet once and build a set of service names still to find.
    # Remove any services already recorded in the database.
    services = read_spreadsheet(train_name)
    all_service_names = {svc["name"] for svc in services}
    print(f"\n       Spreadsheet: {len(all_service_names)} unique services")

    already_recorded = get_existing_services(train_name)
    remaining_services = all_service_names - already_recorded
    if already_recorded & all_service_names:
        print(f"       Already in DB: {len(already_recorded & all_service_names)}")
    print(f"       Still to find: {len(remaining_services)}")

    if not remaining_services:
        print(f"\n       All services already recorded — skipping train '{train_name}'.")
        _return_to_main_menu_from_menus()
        return

    # Use cached train count from progress, or count via LLM
    if trains_per_class is not None:
        train_count = trains_per_class
        print(f"       Using cached train count: {train_count}")
    else:
        train_count = count_trains_llm()
    if config.MAX_TRAINS_PER_CLASS is not None:
        train_count = min(train_count, config.MAX_TRAINS_PER_CLASS)
    print(f"\n=== Processing {train_count} trains for '{train_name}' ===\n")

    # Store train_count immediately so the progress callback can save it
    # (don't wait until process_all_trains returns — a crash would lose it)
    process_all_trains._last_train_count = train_count

    # Save progress right away with the train count so it's cached on crash
    if on_progress:
        on_progress(train_name, start_train or 1, start_service or 1)

    first_train = (start_train - 1) if start_train else 0
    if first_train > 0:
        print(f"Resuming from train {start_train}, service {start_service or 1}\n")

    train_results = []

    for train_idx in range(first_train, train_count):
        # All services already found — skip remaining trains
        if not remaining_services:
            print(f"\n       All services found! Skipping remaining trains.")
            break

        train_start = time.time()
        print(f"\n{'#'*60}")
        print(f"### Train {train_idx + 1}/{train_count} "
              f"({len(remaining_services)} services remaining)")
        print(f"{'#'*60}")

        # Create per-train folder
        train_dir = os.path.join(class_dir, f"train_{train_idx + 1:02d}")
        os.makedirs(train_dir, exist_ok=True)

        # Click the train
        try:
            click_train(train_idx)
        except TimeoutError as e:
            raise ServiceError(train_name, train_idx + 1, 1, e) from e

        # Only use start_service for the first train in a resume
        svc_start = start_service if train_idx == first_train else None

        # Process services for this train (only tries services still in remaining_services)
        svc_count = process_all_services(
            train_dir=train_dir,
            train_index=train_idx,
            train_name=train_name,
            start_service=svc_start,
            on_progress=on_progress,
            remaining_services=remaining_services,
            extra_section_choice=extra_section_choice,
        )

        train_duration = time.time() - train_start
        train_results.append((train_idx + 1, svc_count, train_duration))

        # Exit and relaunch between trains
        # Skip if all services found (next iteration will break)
        if train_idx < train_count - 1 and remaining_services:
            exit_game()
            from navigator import relaunch_and_navigate
            relaunch_and_navigate(train_name, extra_section_choice=extra_section_choice)

    total_duration = time.time() - run_start
    total_services = sum(r[1] for r in train_results)

    print(f"\n=== All {train_count} trains processed for '{train_name}'! ===")
    print(f"    Total services: {total_services}")

    # Write summary report
    report_path = os.path.join(class_dir, "report.txt")
    started = datetime.fromtimestamp(run_start)
    with open(report_path, "w") as f:
        f.write(f"TSW Timetable Bot — Spreadsheet + LLM Method Run Report\n")
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

    # Write out any services that were never found on any train
    if remaining_services:
        not_found_path = os.path.join(class_dir, "services_not_found.txt")
        with open(not_found_path, "w") as f:
            for svc_name in sorted(remaining_services):
                f.write(f"{svc_name}\n")
        print(f"       {len(remaining_services)} services not found — "
              f"see {not_found_path}")


# ═══════════════════════════════════════════════════════════════════════
# Main entry point
# ═══════════════════════════════════════════════════════════════════════

def main():
    print("=== TSW Timetable Bot (Spreadsheet + LLM Method) ===")
    print(f"    Route: {config.ROUTE_NAME}")
    print(f"    Max services/train: {MAX_SERVICES}\n")

    # Parse arguments
    no_launch = "--no-launch" in sys.argv
    args = [a for a in sys.argv[1:] if a != "--no-launch"]

    resume_choice = None
    resume_train_name = None
    resume_train_num = None
    resume_service = None
    resume_trains_per_class = None

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

    # Setup
    os.makedirs(config.SCREENSHOTS_DIR, exist_ok=True)

    # Start server if needed
    if check_server():
        print("Parent app server already running.\n")
    else:
        start_server()

    # Fetch trains from database
    print("Fetching trains from database...")
    try:
        all_trains = get_trains()
    except ValueError as e:
        print(f"ERROR: {e}")
        sys.exit(1)
    print(f"Found {len(all_trains)} trains: {', '.join(all_trains)}\n")

    # Resolve train IDs
    print("Resolving train IDs...")
    for train_name in all_trains:
        try:
            resolve_ids(train_name)
        except ValueError as e:
            print(f"ERROR: {e}")
            sys.exit(1)
    print("All IDs resolved.\n")

    # Build choices list from database sections
    from uploader import get_sections
    db_sections = get_sections()
    if db_sections:
        choices = list(db_sections)
    else:
        choices = [None]

    try:
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

            remaining_trains = list(all_trains)

            # Skip to resume train
            if resume_train_name and resume_train_name in remaining_trains:
                skip_idx = remaining_trains.index(resume_train_name)
                if skip_idx > 0:
                    print(f"Skipping {skip_idx} train(s) before '{resume_train_name}'")
                remaining_trains = remaining_trains[skip_idx:]

            cached_train_count = [resume_trains_per_class]

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
                print(f"### Train Class: {train_name} (spreadsheet + LLM method)")
                if choice:
                    print(f"### Choice: {choice}")
                print(f"### Remaining: {len(remaining_trains)} train class(es)")
                print(f"{'#'*60}\n")

                resolve_ids(train_name)

                try:
                    if need_launch:
                        launch_game()
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
        print("\nAll trains processed. Exiting game...")
        try:
            exit_game()
        except TimeoutError:
            kill_game()

    except KeyboardInterrupt:
        print("\nBot stopped by user.")
        sys.exit(0)


if __name__ == "__main__":
    main()
