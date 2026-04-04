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
    exit_game,
    kill_game,
    relaunch_and_navigate,
)
from uploader import check_server, resolve_ids, get_trains

# Progress file — saves current position so the bot can resume after a crash
PROGRESS_FILE = os.path.join(config.BASE_DIR, "progress.json")


def save_progress(train_name, train_number, service_number, choice=None):
    """Write current position to disk so the bot can resume later."""
    data = {
        "route": config.ROUTE_NAME,
        "choice": choice,
        "train": train_name,
        "train_num": train_number,
        "service": service_number,
    }
    with open(PROGRESS_FILE, "w") as f:
        json.dump(data, f, indent=2)


def load_progress():
    """Load saved progress if it matches the current route.

    Returns (choice, train_name, train_num, service) or (None, None, None, None)
    if no valid progress file exists.
    """
    if not os.path.isfile(PROGRESS_FILE):
        return None, None, None, None
    try:
        with open(PROGRESS_FILE, "r") as f:
            data = json.load(f)
        if data.get("route") != config.ROUTE_NAME:
            print(f"Progress file is for a different route "
                  f"('{data.get('route')}'), ignoring.")
            return None, None, None, None
        return data.get("choice"), data["train"], data["train_num"], data["service"]
    except (json.JSONDecodeError, KeyError):
        return None, None, None, None


def clear_progress():
    """Delete the progress file (called when all trains are done)."""
    if os.path.isfile(PROGRESS_FILE):
        os.remove(PROGRESS_FILE)

# Background server process — cleaned up on exit
_server_proc = None


def start_server():
    """Launch the Node.js parent app server as a background process."""
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

    # Wait for server to become ready (up to 30 seconds)
    for i in range(30):
        time.sleep(1)
        if _server_proc.poll() is not None:
            # Process exited early — dump output
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
    """Terminate the background server process."""
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


def check_references():
    """Verify that all required reference images exist."""
    required = [
        config.REF_WARNING_CONTINUE,
        config.REF_SPLASH_CONTINUE,
        config.REF_TO_THE_TRAINS_1,
        config.REF_CHOOSE_A_ROUTE_1,
        config.REF_ROUTE_FILTER,
        config.REF_TIMETABLE_1,
        config.REF_TIMETABLE_2,
        config.REF_CLASS_FILTER,
        config.REF_EXIT_TO_MAIN_MENU_1,
        config.REF_GET_STARTED_1,
        config.REF_DRIVER_1,
    ]
    # Check if route has sections in DB (need ref image for section filter)
    try:
        from uploader import get_sections as _get_sections
        if _get_sections():
            required.append(config.REF_EXTRA_SECTION_FILTER)
    except Exception:
        pass
    missing = [path for path in required if not os.path.isfile(path)]
    if missing:
        print("ERROR: Missing reference images:")
        for path in missing:
            print(f"  - {os.path.basename(path)}")
        print(f"\nPlease add them to: {config.REFERENCES_DIR}")
        return False
    return True


def main():
    print("=== TSW Timetable Bot ===\n")

    # Parse optional resume arguments:
    #   python main.py                                  # auto-resume from progress file
    #   python main.py "Train Name" <train_num> <service>   # resume from specific point
    resume_choice = None
    resume_train_name = None
    resume_train_num = None
    resume_service = None
    if len(sys.argv) >= 4:
        resume_train_name = sys.argv[1]
        resume_train_num = int(sys.argv[2])
        resume_service = int(sys.argv[3])
        print(f"RESUME MODE (args): train='{resume_train_name}', train_num={resume_train_num}, "
              f"service={resume_service}\n")
    else:
        # Check for saved progress from a previous run
        resume_choice, resume_train_name, resume_train_num, resume_service = load_progress()
        if resume_train_name:
            print(f"RESUME MODE (auto): choice='{resume_choice}', train='{resume_train_name}', "
                  f"train_num={resume_train_num}, service={resume_service}\n")

    if not check_references():
        sys.exit(1)

    os.makedirs(config.SCREENSHOTS_DIR, exist_ok=True)

    # Start the parent app server (or connect to an already-running one)
    if check_server():
        print("Parent app server already running.\n")
    else:
        start_server()

    # Fetch trains dynamically from the database
    print("Fetching trains from database...")
    try:
        all_trains = get_trains()
    except ValueError as e:
        print(f"ERROR: {e}")
        sys.exit(1)
    print(f"Found {len(all_trains)} trains: {', '.join(all_trains)}\n")

    # Resolve IDs for all trains upfront
    print("Resolving train IDs...")
    for train_name in all_trains:
        try:
            resolve_ids(train_name)
        except ValueError as e:
            print(f"ERROR: {e}")
            sys.exit(1)
    print("All IDs resolved.\n")

    # Build the list of choices from database sections
    from uploader import get_sections
    db_sections = get_sections()
    if db_sections:
        choices = list(db_sections)
        print(f"Extra section enabled with {len(choices)} choice(s): {choices}\n")
    else:
        choices = [None]  # single pass, no extra section

    try:
        from service_loop import process_all_trains, ServiceError, BatchRestart

        retry_counts = {}  # {(train_name, train_num, service): count}
        need_launch = True  # first iteration always launches fresh
        current_choice = None  # track for progress callback

        # Skip ahead to the resume choice if resuming
        if resume_choice and resume_choice in choices:
            skip_idx = choices.index(resume_choice)
            if skip_idx > 0:
                print(f"Skipping {skip_idx} choice(s) before '{resume_choice}'")
            choices = choices[skip_idx:]

        for choice in choices:
            current_choice = choice

            if choice:
                print(f"\n{'@'*60}")
                print(f"@@@ Extra Section Choice: {choice}")
                print(f"{'@'*60}\n")

            # For each choice, process all trains
            remaining_trains = list(all_trains)

            # If resuming within this choice, skip to the resume train
            if resume_train_name and resume_train_name in remaining_trains:
                skip_idx = remaining_trains.index(resume_train_name)
                if skip_idx > 0:
                    print(f"Skipping {skip_idx} train(s) before '{resume_train_name}'")
                remaining_trains = remaining_trains[skip_idx:]

            def _save_progress_with_choice(train_name, train_number, service_number):
                save_progress(train_name, train_number, service_number, choice=current_choice)

            while remaining_trains:
                train_name = remaining_trains[0]

                print(f"\n{'#'*60}")
                print(f"### Train: {train_name}")
                if choice:
                    print(f"### Choice: {choice}")
                print(f"### Remaining: {len(remaining_trains)} train(s)")
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

                    print(f"\nTrain list reached for '{train_name}' — starting train loop.\n")
                    process_all_trains(
                        train_name,
                        start_train=resume_train_num,
                        start_service=resume_service,
                        on_progress=_save_progress_with_choice,
                        extra_section_choice=choice,
                    )

                    # Success — move to next train
                    remaining_trains.pop(0)
                    resume_train_num = None
                    resume_service = None

                    # Relaunch for next train (if any)
                    if remaining_trains:
                        exit_game()
                        need_launch = True

                except ServiceError as e:
                    key = (e.train_name, e.train_number, e.service_number)
                    retry_counts[key] = retry_counts.get(key, 0) + 1
                    attempts = retry_counts[key]

                    print(f"\n{'!'*60}")
                    print(f"!!! FAILURE: {e}")
                    print(f"!!! Attempt {attempts}/{config.MAX_RETRIES_PER_SERVICE} "
                          f"for this train+service")
                    print(f"{'!'*60}\n")

                    if attempts >= config.MAX_RETRIES_PER_SERVICE:
                        print(f"Skipping to next service after "
                              f"{config.MAX_RETRIES_PER_SERVICE} failures.")
                        resume_service = e.service_number + 1
                    else:
                        resume_service = e.service_number

                    resume_train_num = e.train_number
                    save_progress(train_name, resume_train_num, resume_service,
                                  choice=current_choice)

                    # Kill game and relaunch on next loop iteration
                    kill_game()
                    need_launch = True

                except BatchRestart as e:
                    print(f"\n{'='*60}")
                    print(f"=== BATCH RESTART: processed {config.BATCH_SIZE} services")
                    print(f"=== Resuming at {e.train_name} T{e.train_number} S{e.service_number}")
                    print(f"{'='*60}\n")
                    resume_train_num = e.train_number
                    resume_service = e.service_number
                    save_progress(train_name, resume_train_num, resume_service,
                                  choice=current_choice)
                    kill_game()
                    need_launch = True

                except TimeoutError as e:
                    # Navigation-level failure (not inside service loop)
                    print(f"\n{'!'*60}")
                    print(f"!!! NAVIGATION FAILURE: {e}")
                    print(f"!!! Killing game and retrying train '{train_name}'")
                    print(f"{'!'*60}\n")
                    save_progress(train_name, resume_train_num or 1, resume_service or 1,
                                  choice=current_choice)
                    kill_game()
                    need_launch = True
                    # Keep same resume args and retry the train

            # Clear resume state between choices so the next choice starts fresh
            resume_train_name = None
            resume_train_num = None
            resume_service = None

            # Relaunch for the next choice (if any remain)
            if choice != choices[-1]:
                try:
                    exit_game()
                except TimeoutError:
                    kill_game()
                need_launch = True

        # All trains done — clear progress file
        clear_progress()
        print("\nAll trains processed. Exiting game...")
        try:
            exit_game()
        except TimeoutError:
            kill_game()

        # Write combined report from all per-train reports
        route_dir = os.path.join(config.SCREENSHOTS_DIR, config.ROUTE_NAME)
        combined_path = os.path.join(route_dir, "report.txt")
        with open(combined_path, "w") as out:
            for choice in choices:
                choice_dir = os.path.join(route_dir, choice) if choice else route_dir
                for train_name in all_trains:
                    train_report = os.path.join(choice_dir, train_name, "report.txt")
                    if os.path.isfile(train_report):
                        with open(train_report, "r") as f:
                            out.write(f.read())
                        out.write(f"\n{'-'*80}\n\n")
        print(f"\nCombined report saved to: {combined_path}")

        # Relaunch game and check service names against the in-game index
        print("\n" + "=" * 60)
        print("Relaunching game to check service names...")
        print("=" * 60)
        launch_game()
        pass_warning_screen()
        pass_splash_screen()
        click_to_the_trains()
        click_choose_a_route()
        select_route()
        click_timetable()

        from check_service_names import main as check_service_names_main
        check_service_names_main()

        exit_game()

    except KeyboardInterrupt:
        print("\nBot stopped by user.")
        sys.exit(0)


if __name__ == "__main__":
    main()
