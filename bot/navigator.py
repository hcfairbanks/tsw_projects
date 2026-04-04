import os
import subprocess
import time

import numpy as np
import pyautogui

import config
from utils import wait_and_click, wait_for_image


def launch_game():
    """Launch TSW 6 via Steam."""
    print("[1/6] Launching Train Sim World 6 via Steam...")
    subprocess.Popen(
        ["cmd", "/c", "start", f"steam://rungameid/{config.STEAM_APP_ID}"],
        shell=False,
    )
    # Give Steam a moment to start the game process
    time.sleep(5)


def pass_warning_screen():
    """Wait for the warning screen and click to continue."""
    print("[2/6] Waiting for warning screen...")
    time.sleep(10)
    if not wait_and_click(config.REF_WARNING_CONTINUE, timeout=config.GAME_LAUNCH_TIMEOUT, confidence=config.CONFIDENCE_LOW):
        raise TimeoutError("Timed out waiting for warning screen")
    print("       Clicked past warning screen.")
    time.sleep(config.CLICK_SETTLE_DELAY)


def pass_splash_screen():
    """Wait for the splash screen and click to continue."""
    print("[3/6] Waiting for splash screen...")
    if not wait_and_click(config.REF_SPLASH_CONTINUE, timeout=config.SCREEN_TIMEOUT, confidence=config.CONFIDENCE_LOW):
        raise TimeoutError("Timed out waiting for splash screen")
    print("       Clicked past splash screen.")
    time.sleep(config.CLICK_SETTLE_DELAY)


def click_to_the_trains():
    """Click the 'To The Trains' tile (tries both normal and hovered reference images)."""
    print("[4/6] Waiting for main menu — looking for 'To The Trains'...")
    to_trains_refs = [r for r in [config.REF_TO_THE_TRAINS_1, config.REF_TO_THE_TRAINS_2]
                      if os.path.isfile(r)]
    if not to_trains_refs:
        raise FileNotFoundError("No 'To The Trains' reference images found")
    start = time.time()
    while time.time() - start < config.SCREEN_TIMEOUT:
        for ref in to_trains_refs:
            try:
                location = pyautogui.locateOnScreen(ref, confidence=config.CONFIDENCE,
                                                            region=config.GAME_REGION)
            except pyautogui.ImageNotFoundException:
                location = None
            if location is not None:
                center = pyautogui.center(location)
                pyautogui.moveTo(center)
                time.sleep(0.5)
                pyautogui.mouseDown()
                time.sleep(0.2)
                pyautogui.mouseUp()
                print("       Clicked 'To The Trains'.")
                time.sleep(config.CLICK_SETTLE_DELAY)
                return
        time.sleep(1.0)
    raise TimeoutError("Timed out waiting for 'To The Trains' tile")


def click_choose_a_route():
    """Click the 'Choose a Route' tile (tries both normal and hovered reference images)."""
    print("[5/6] Waiting for menu — looking for 'Choose a Route'...")
    choose_refs = [r for r in [config.REF_CHOOSE_A_ROUTE_1, config.REF_CHOOSE_A_ROUTE_2]
                   if os.path.isfile(r)]
    if not choose_refs:
        raise FileNotFoundError("No 'Choose a Route' reference images found")
    start = time.time()
    while time.time() - start < config.SCREEN_TIMEOUT:
        for ref in choose_refs:
            try:
                location = pyautogui.locateOnScreen(ref, confidence=config.CONFIDENCE,
                                                            region=config.GAME_REGION)
            except pyautogui.ImageNotFoundException:
                location = None
            if location is not None:
                center = pyautogui.center(location)
                pyautogui.moveTo(center)
                time.sleep(0.5)
                pyautogui.mouseDown()
                time.sleep(0.2)
                pyautogui.mouseUp()
                print("       Clicked 'Choose a Route'.")
                time.sleep(config.CLICK_SETTLE_DELAY)
                return
        time.sleep(1.0)
    raise TimeoutError("Timed out waiting for 'Choose a Route' tile")


def select_route(route_name=None):
    """Filter and select a route by typing into the search field and pressing Enter."""
    if route_name is None:
        route_name = config.ROUTE_NAME
    print(f"[7/7] Selecting route: {route_name}")

    # Click the filter text field (stays on screen after click, so skip verify)
    print("       Clicking route filter field...")
    if not wait_and_click(config.REF_ROUTE_FILTER, timeout=config.SCREEN_TIMEOUT, confidence=config.CONFIDENCE, verify=False):
        raise TimeoutError("Timed out waiting for route filter field")
    time.sleep(0.5)

    # Clear any existing text and type the route name
    print(f"       Typing: {route_name}")
    pyautogui.hotkey("ctrl", "a")
    time.sleep(0.2)
    pyautogui.typewrite(route_name, interval=0.03)
    time.sleep(1.0)

    # Press Enter twice to select
    print("       Pressing Enter to select route...")
    pyautogui.press("enter")
    time.sleep(0.5)
    pyautogui.press("enter")
    time.sleep(config.CLICK_SETTLE_DELAY)
    print("       Route selected!")


def click_timetable():
    """Click the 'Timetable' tile (tries both normal and hovered reference images)."""
    print("[8/8] Waiting for menu — looking for 'Timetable'...")
    timetable_refs = [r for r in [config.REF_TIMETABLE_1, config.REF_TIMETABLE_2]
                      if os.path.isfile(r)]
    if not timetable_refs:
        raise FileNotFoundError("No timetable reference images found")
    start = time.time()
    while time.time() - start < config.SCREEN_TIMEOUT:
        for ref in timetable_refs:
            try:
                location = pyautogui.locateOnScreen(ref, confidence=config.CONFIDENCE,
                                                            region=config.GAME_REGION)
            except pyautogui.ImageNotFoundException:
                location = None
            if location is not None:
                center = pyautogui.center(location)
                pyautogui.moveTo(center)
                time.sleep(0.5)
                pyautogui.mouseDown()
                time.sleep(0.2)
                pyautogui.mouseUp()
                print("       Clicked 'Timetable'.")
                time.sleep(config.CLICK_SETTLE_DELAY)
                return
        time.sleep(1.0)
    raise TimeoutError("Timed out waiting for 'Timetable' tile")


def select_extra_section(choice):
    """Select a choice on the extra section page (between Timetable and class selection).

    Clicks the search/filter text field, types the choice to filter results,
    then clicks the first result tile below the search box by coordinate offset.
    """
    print(f"[extra] Selecting extra section: {choice}")

    # Find the search box location so we can click relative to it
    print("       Waiting for extra section filter field...")
    location = wait_for_image(config.REF_EXTRA_SECTION_FILTER, timeout=config.SCREEN_TIMEOUT,
                              confidence=config.CONFIDENCE)
    if location is None:
        raise TimeoutError("Timed out waiting for extra section filter field")

    # Click the search box
    center = pyautogui.center(location)
    pyautogui.moveTo(center)
    time.sleep(0.5)
    pyautogui.mouseDown()
    time.sleep(0.2)
    pyautogui.mouseUp()
    time.sleep(0.5)

    # Type the choice to filter
    print(f"       Typing: {choice}")
    pyautogui.hotkey("ctrl", "a")
    time.sleep(0.2)
    pyautogui.typewrite(choice, interval=0.03)
    time.sleep(1.5)

    # Click the first result tile at a fixed screen position
    tile_x = config.EXTRA_SECTION_TILE_X
    tile_y = config.EXTRA_SECTION_TILE_Y
    print(f"       Clicking first result tile at ({tile_x}, {tile_y})...")
    pyautogui.moveTo(tile_x, tile_y)
    time.sleep(0.5)
    pyautogui.mouseDown()
    time.sleep(0.2)
    pyautogui.mouseUp()
    time.sleep(config.CLICK_SETTLE_DELAY)
    print("       Extra section choice selected!")


def select_train(train_name):
    """Filter and select a train by typing into the search field and pressing Enter."""
    print(f"[9/9] Selecting train: {train_name}")

    # Click the train filter text field (stays on screen after click, so skip verify)
    print("       Clicking train filter field...")
    if not wait_and_click(config.REF_CLASS_FILTER, timeout=config.SCREEN_TIMEOUT, confidence=config.CONFIDENCE, verify=False):
        raise TimeoutError("Timed out waiting for train filter field")
    time.sleep(0.5)

    # Clear any existing text and type the train name
    print(f"       Typing: {train_name}")
    pyautogui.hotkey("ctrl", "a")
    time.sleep(0.2)
    pyautogui.typewrite(train_name, interval=0.03)
    time.sleep(1.0)

    # Press Enter, then right arrow, then Enter to select
    print("       Pressing Enter to select train...")
    pyautogui.press("enter")
    time.sleep(0.5)
    print("       Pressing Right arrow...")
    pyautogui.press("right")
    time.sleep(0.5)
    print("       Pressing Enter to confirm...")
    pyautogui.press("enter")
    time.sleep(config.CLICK_SETTLE_DELAY)
    print("       Train selected!")
    time.sleep(1.0)


def _detect_train_positions():
    """Capture the train box and return Y centers of visible trains (screen coords).

    Uses #dedede border detection on the left edge, same as count_visible_trains.
    Returns a list of absolute screen Y coordinates for each train center.
    """
    region = (config.TRAIN_BOX_LEFT, config.TRAIN_BOX_TOP,
              config.TRAIN_BOX_WIDTH, config.TRAIN_BOX_HEIGHT)
    img = np.array(pyautogui.screenshot(region=region))

    border_rgb = np.array([0xde, 0xde, 0xde])
    highlight_rgb = np.array([0x4a, 0x90, 0xb5])
    strip = img[:, :3, :].astype(int)
    match = np.all(np.abs(strip - border_rgb) <= 20, axis=2)
    row_has_border = np.any(match, axis=1)

    runs = []
    in_run = False
    run_start = 0
    for r in range(len(row_has_border)):
        if row_has_border[r] and not in_run:
            run_start = r
            in_run = True
        elif not row_has_border[r] and in_run:
            runs.append((run_start, r))
            in_run = False
    if in_run:
        runs.append((run_start, len(row_has_border)))

    # Filter to real entries (>= 40px) and convert to screen Y centers
    positions = []
    for s, e in runs:
        if (e - s) >= 40:
            center_y = config.TRAIN_BOX_TOP + (s + e) // 2
            positions.append(center_y)

    # Fallback: if no #dedede trains found, check for a highlighted train (#4a90b5)
    if not positions:
        match_hl = np.all(np.abs(strip - highlight_rgb) <= 20, axis=2)
        row_has_hl = np.any(match_hl, axis=1)
        hl_runs = []
        in_run = False
        for r in range(len(row_has_hl)):
            if row_has_hl[r] and not in_run:
                run_start = r
                in_run = True
            elif not row_has_hl[r] and in_run:
                hl_runs.append((run_start, r))
                in_run = False
        if in_run:
            hl_runs.append((run_start, len(row_has_hl)))
        for s, e in hl_runs:
            if (e - s) >= 40:
                center_y = config.TRAIN_BOX_TOP + (s + e) // 2
                positions.append(center_y)

    return positions


def click_train(index):
    """Click train at the given index (0-based).

    Uses scroll-based positioning instead of border detection:
    1. Scrolls to the top of the train list
    2. Scrolls down one box at a time (up to `index` times), checking
       via frame comparison whether each scroll actually moved the list
    3. Computes click Y from the number of scrolls that succeeded
       and a fixed stride, so highlighting can't shift the position
    """
    print(f"       Selecting train #{index + 1}...")

    click_x = config.TRAIN_BOX_LEFT + config.TRAIN_BOX_WIDTH // 2
    scroll_x = click_x
    scroll_y = config.TRAIN_BOX_TOP + config.TRAIN_BOX_HEIGHT // 2
    region = (config.TRAIN_BOX_LEFT, config.TRAIN_BOX_TOP,
              config.TRAIN_BOX_WIDTH, config.TRAIN_BOX_HEIGHT)

    # 1. Scroll to the very top for consistent starting position
    pyautogui.moveTo(scroll_x, scroll_y)
    time.sleep(0.3)
    pyautogui.scroll(-config.TRAIN_SCROLL_PER_BOX * 30)
    time.sleep(1.5)

    # 2. Scroll down one box at a time, verifying each scroll moved
    scrolls_done = 0
    for _ in range(index):
        before = np.array(pyautogui.screenshot(region=region))
        pyautogui.moveTo(scroll_x, scroll_y)
        time.sleep(0.3)
        pyautogui.scroll(config.TRAIN_SCROLL_PER_BOX)
        time.sleep(1.0)
        after = np.array(pyautogui.screenshot(region=region))

        if np.mean(np.abs(before.astype(float) - after.astype(float))) < 5.0:
            print(f"       Scroll stopped after {scrolls_done} (list end reached)")
            break
        scrolls_done += 1

    # 3. The target train is at offset (index - scrolls_done) from the top
    offset = index - scrolls_done
    click_y = config.TRAIN_BOX_TOP + config.TRAIN_FIRST_Y_OFFSET + offset * config.TRAIN_BOX_STRIDE

    print(f"       Scrolled {scrolls_done}/{index}, offset in view: {offset}, "
          f"click Y: {click_y}")
    pyautogui.moveTo(click_x, click_y)
    time.sleep(0.5)
    pyautogui.mouseDown()
    time.sleep(0.2)
    pyautogui.mouseUp()
    time.sleep(5.0)           # service list needs time to populate
    print(f"       Train #{index + 1} selected!")


def kill_game():
    """Force-kill the game process (no menu navigation needed).

    Uses PowerShell to find processes by window title and kill them.
    """
    print("       Force-killing game process...")

    try:
        result = subprocess.run(
            ["powershell", "-Command",
             "Get-Process | Where-Object {$_.MainWindowTitle -like '*Train Sim*'} "
             "| ForEach-Object { Write-Output $_.Id; Stop-Process -Id $_.Id -Force }"],
            capture_output=True, text=True, timeout=15,
        )
        pids = [line.strip() for line in result.stdout.splitlines()
                if line.strip().isdigit()]
        if pids:
            for pid in pids:
                print(f"       Killed PID {pid}")
        else:
            print("       No game window found by title.")
    except Exception as e:
        print(f"       PowerShell kill failed: {e}")

    time.sleep(5.0)
    print("       Game kill complete.")


def exit_game():
    """Exit the game completely from any menu screen.

    Looks for 'Exit Game' button, waits for confirmation dialog,
    then clicks 'Yes' to quit.
    """
    print("       Exiting game...")

    # 1. Find and click "Exit Game" (button stays visible after click — dialog overlays)
    exit_refs = [config.REF_EXIT_GAME_1, config.REF_EXIT_GAME_2]
    clicked = False
    for ref in exit_refs:
        if not os.path.isfile(ref):
            continue
        if wait_and_click(ref, timeout=10, confidence=config.CONFIDENCE, verify=False):
            clicked = True
            break
    if not clicked:
        raise TimeoutError("Could not find 'Exit Game' button")
    time.sleep(2.0)

    # 2. Wait for the exit confirmation dialog
    print("       Waiting for exit dialog...")
    loc = wait_for_image(config.REF_EXIT_GAME_DIALOGBOX, timeout=config.SCREEN_TIMEOUT, confidence=config.CONFIDENCE)
    if loc is None:
        raise TimeoutError("Timed out waiting for exit game dialog")
    time.sleep(1.0)

    # 3. Click "Yes" to confirm exit
    print("       Clicking 'Yes' to confirm exit...")
    yes_refs = [config.REF_EXIT_GAME_YES_1, config.REF_EXIT_GAME_YES_2]
    clicked = False
    for ref in yes_refs:
        if not os.path.isfile(ref):
            continue
        if wait_and_click(ref, timeout=10, confidence=config.CONFIDENCE):
            clicked = True
            break
    if not clicked:
        raise TimeoutError("Could not find 'Yes' button on exit dialog")

    # 4. 'Yes' click verify already confirmed the game closed (dialog disappeared).
    #    Brief pause before relaunching.
    print("       Game closed.")
    time.sleep(5.0)


def relaunch_and_navigate(train_name, extra_section_choice=None):
    """Relaunch the game and navigate back to the train selection screen.

    Goes through: launch → warning → splash → To The Trains →
    Choose a Route → route screen → select route → timetable →
    [extra section] → select train.
    """
    print("\n       Relaunching game...")
    launch_game()
    pass_warning_screen()
    pass_splash_screen()
    click_to_the_trains()
    click_choose_a_route()
    select_route()
    click_timetable()
    if extra_section_choice:
        select_extra_section(extra_section_choice)
    select_train(train_name)
    print("       Back at train list after relaunch.")
