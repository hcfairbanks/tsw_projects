import os
import shutil
import time

import numpy as np
import pyautogui
from PIL import Image

import config
from utils import wait_and_click, wait_for_image
from schedule_capture import capture_schedule
from uploader import upload_service
from tsw_api import get_player_location
from navigator import (
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
    relaunch_and_navigate,
)


class ServiceError(Exception):
    """Raised when a service fails, carrying position info for resume."""

    def __init__(self, train_name, train_number, service_number, original_error=None):
        self.train_name = train_name
        self.train_number = train_number        # 1-indexed
        self.service_number = service_number    # 1-indexed
        super().__init__(
            f"Failed at {train_name} train {train_number} service {service_number}: "
            f"{original_error}"
        )


class BatchRestart(Exception):
    """Raised when batch limit reached, carrying resume position."""

    def __init__(self, train_name, train_number, service_number):
        self.train_name = train_name
        self.train_number = train_number        # 1-indexed
        self.service_number = service_number    # 1-indexed
        super().__init__(
            f"Batch limit reached at {train_name} train {train_number}, "
            f"resuming at service {service_number}"
        )


def get_visible_service_boxes():
    """Calculate the positions of all 8 visible service boxes using fixed stride.

    Uses FIRST_BOX_TOP offset from SERVICE_LIST_TOP, then SERVICE_BOX_STRIDE
    for each subsequent box. Returns list of (center_x, center_y) tuples in
    screen coordinates.
    """
    center_x = (config.SERVICE_LIST_LEFT + config.SERVICE_LIST_RIGHT) // 2
    boxes = []
    for i in range(config.BOXES_PER_PAGE):
        box_top = config.FIRST_BOX_TOP + i * config.SERVICE_BOX_STRIDE
        box_center_y = config.SERVICE_LIST_TOP + box_top + config.SERVICE_BOX_HEIGHT // 2
        if box_top + config.SERVICE_BOX_HEIGHT > config.SERVICE_LIST_BOTTOM - config.SERVICE_LIST_TOP:
            break
        boxes.append((center_x, box_center_y))
    return boxes



def wait_for_single_highlight():
    """Wait until exactly one service box has the #57a6d0 border.

    Scans the full service list left edge for the border color.
    Retries a few times if zero or multiple boxes are highlighted.
    Returns (count, center_y) where center_y is the screen Y of the
    highlighted box (or None if count != 1).
    """
    target = np.array(config.SERVICE_BOX_BORDER_RGB)
    tol = config.SERVICE_BOX_COLOR_TOLERANCE
    min_height = 30

    for attempt in range(2):
        region = (
            config.SERVICE_LIST_LEFT,
            config.SERVICE_LIST_TOP,
            config.SERVICE_LIST_RIGHT - config.SERVICE_LIST_LEFT,
            config.SERVICE_LIST_BOTTOM - config.SERVICE_LIST_TOP,
        )
        img = np.array(pyautogui.screenshot(region=region))

        strip = img[:, :5, :].astype(int)
        match = np.all(np.abs(strip - target) <= tol, axis=2)
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

        valid_runs = [(s, e) for s, e in runs if (e - s) >= min_height]
        box_count = len(valid_runs)

        if box_count == 1:
            s, e = valid_runs[0]
            center_y = config.SERVICE_LIST_TOP + (s + e) // 2
            print(f"       Single highlight confirmed (attempt {attempt + 1})")
            return 1, center_y

        print(f"       Highlight check: found {box_count} bordered boxes (attempt {attempt + 1}), waiting...")
        time.sleep(0.5)

    print(f"       WARNING: Expected 1 highlighted box, found {box_count}")
    return box_count, None


def _grab_and_crop_service_box(y_center, padding=30):
    """Grab an oversized region around a service box and crop to the #57a6d0 border.

    Returns (cropped_img, raw_img, found_border) where found_border is True if
    the blue border was detected and used for cropping. raw_img is the full
    uncropped grab for debugging.
    """
    grab_left = config.SERVICE_LIST_LEFT
    grab_top = y_center - config.SERVICE_BOX_HEIGHT // 2 - padding
    grab_width = config.SERVICE_LIST_RIGHT - config.SERVICE_LIST_LEFT + config.BOX_EXTRA_RIGHT
    grab_height = config.SERVICE_BOX_HEIGHT + 2 * padding

    screenshot = pyautogui.screenshot(region=(grab_left, grab_top, grab_width, grab_height))
    img = np.array(screenshot)  # RGB
    raw_img = img.copy()

    target = np.array(config.SERVICE_BOX_BORDER_RGB)
    tol = config.SERVICE_BOX_COLOR_TOLERANCE
    strip = img[:, :5, :].astype(int)
    match = np.all(np.abs(strip - target) <= tol, axis=2)
    row_match = np.any(match, axis=1)

    runs = []
    in_run = False
    run_start = 0
    for r in range(len(row_match)):
        if row_match[r] and not in_run:
            run_start = r
            in_run = True
        elif not row_match[r] and in_run:
            runs.append((run_start, r))
            in_run = False
    if in_run:
        runs.append((run_start, len(row_match)))

    if not runs:
        return img, raw_img, False

    expected_center = padding + config.SERVICE_BOX_HEIGHT // 2
    best_run = min(runs, key=lambda r: abs((r[0] + r[1]) / 2 - expected_center))
    row_top, row_bottom = best_run

    box_strip = img[row_top:row_bottom, :, :].astype(int)
    col_match = np.all(np.abs(box_strip - target) <= tol, axis=2)
    col_has_border = np.any(col_match, axis=0)

    left = 0
    right = img.shape[1]
    border_cols = np.where(col_has_border)[0]
    if len(border_cols) > 0:
        left = int(border_cols[0])
        right = int(border_cols[-1]) + 1

    trim = getattr(config, 'SERVICE_BOX_RIGHT_TRIM', 0)
    if trim > 0 and (right - left) > trim:
        right -= trim
    img = img[row_top:row_bottom, left:right, :]
    return img, raw_img, True


def _validate_border(img):
    """Check that the cropped image has a solid #57a6d0 border on all 4 edges.

    Returns (has_blue, has_edge_border) where:
      has_blue: image contains any #57a6d0 pixels at all
      has_edge_border: all 4 edges have a continuous 2px blue border (>=90% blue)
                       AND image height is at least 80% of SERVICE_BOX_HEIGHT
    """
    target = np.array(config.SERVICE_BOX_BORDER_RGB)
    tol = config.SERVICE_BOX_COLOR_TOLERANCE
    h, w = img.shape[:2]
    min_px = 2
    min_ratio = 0.90  # at least 90% of edge pixels must be blue

    all_match = np.all(np.abs(img.astype(int) - target) <= tol, axis=2)
    has_blue = bool(np.any(all_match))

    if not has_blue:
        return False, False

    # Reject if the crop is too short — likely a partial box
    min_height = int(config.SERVICE_BOX_HEIGHT * 0.8)
    if h < min_height:
        print(f"       Border validation: image too short ({h}px < {min_height}px min)")
        return True, False

    edges_ok = True
    edge_names = ["top", "bottom", "left", "right"]
    edges = [img[:min_px, :, :],       # top
             img[-min_px:, :, :],       # bottom
             img[:, :min_px, :],        # left
             img[:, -min_px:, :]]       # right

    for name, edge in zip(edge_names, edges):
        match = np.all(np.abs(edge.astype(int) - target) <= tol, axis=-1)
        ratio = np.mean(match)
        if ratio < min_ratio:
            print(f"       Border validation: {name} edge only {ratio:.0%} blue (need {min_ratio:.0%})")
            edges_ok = False
            break

    return has_blue, edges_ok


def _find_highlighted_box_y():
    """Scan the service list for the currently highlighted box and return its y_center.

    Looks for the #57a6d0 border in the full service list area.
    Returns the screen y_center of the highlighted box, or None if not found.
    """
    region = (config.SERVICE_LIST_LEFT, config.SERVICE_LIST_TOP,
              config.SERVICE_LIST_RIGHT - config.SERVICE_LIST_LEFT,
              config.SERVICE_LIST_BOTTOM - config.SERVICE_LIST_TOP)
    img = np.array(pyautogui.screenshot(region=region))
    target = np.array(config.SERVICE_BOX_BORDER_RGB)
    tol = config.SERVICE_BOX_COLOR_TOLERANCE

    strip = img[:, :5, :].astype(int)
    match = np.all(np.abs(strip - target) <= tol, axis=2)
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

    boxes = [(s, e) for s, e in runs if (e - s) >= 30]
    if boxes:
        s, e = boxes[0]
        return config.SERVICE_LIST_TOP + (s + e) // 2
    return None


def screenshot_service_box(output_dir, y_center, click_x=None, click_y=None):
    """Take a validated screenshot of a service box.

    Crops to the #57a6d0 border on all 4 sides, then validates:
    1. Blue border present — if not, retake screenshot
    2. Still no blue — re-click the box and retake
    3. Still no blue — save for debugging and raise error
    4. Blue present but not on all edges — micro-scroll (no click) and retake
    5. Still clipped — save with warning

    Saves as 1_service.png inside output_dir.
    Returns (path, final_y) where final_y is the y position of the highlighted
    box after any micro-scrolling (may differ from y_center).
    """
    path = os.path.join(output_dir, "1_service.png")
    final_y = y_center

    # Attempt 1: normal grab
    img, raw, found = _grab_and_crop_service_box(y_center)
    if found:
        has_blue, has_edges = _validate_border(img)
    else:
        has_blue, has_edges = False, False

    if has_blue and has_edges:
        Image.fromarray(img).save(path)
        return path, final_y

    # Attempt 2: retake screenshot
    if not has_blue:
        print("       No blue border in screenshot, retaking...")
        time.sleep(0.5)
        img, raw, found = _grab_and_crop_service_box(y_center)
        if found:
            has_blue, has_edges = _validate_border(img)
        else:
            has_blue, has_edges = False, False

        if has_blue and has_edges:
            Image.fromarray(img).save(path)
            return path, final_y

    # Attempt 3: re-click + retake
    if not has_blue:
        if click_x is not None and click_y is not None:
            print("       Still no blue border, re-clicking box and retaking...")
            pyautogui.moveTo(click_x, click_y)
            time.sleep(0.5)
            pyautogui.mouseDown()
            time.sleep(0.2)
            pyautogui.mouseUp()
            time.sleep(1.5)
            img, raw, found = _grab_and_crop_service_box(y_center)

            if found:
                has_blue, has_edges = _validate_border(img)
            else:
                has_blue, has_edges = False, False

            if has_blue and has_edges:
                Image.fromarray(img).save(path)
                return path, final_y

    # No blue at all after 3 attempts — save for debugging and raise
    if not has_blue:
        Image.fromarray(img).save(path)
        raise RuntimeError(
            f"No blue border (#57a6d0) detected in service screenshot after 3 attempts"
        )

    # Blue is present but edges are clipped — could be:
    # (a) grab region is slightly off from actual box center (drift), or
    # (b) box is genuinely near the edge of the scroll container.
    # First try re-scanning to find the exact highlight position and re-grab.
    # Only micro-scroll if that doesn't fix it.
    if not has_edges:
        # Step 1: Re-scan for the highlight's actual position
        print("       Border clipped — re-scanning for exact highlight position...")
        new_y = _find_highlighted_box_y()
        if new_y is not None and abs(new_y - y_center) > 3:
            print(f"       Highlight found at y={new_y} (was {y_center}, diff={new_y - y_center:+d}px)")
            final_y = new_y
            img, raw, found = _grab_and_crop_service_box(new_y)

            if found:
                has_blue, has_edges = _validate_border(img)
            if has_blue and has_edges:
                Image.fromarray(img).save(path)
                return path, final_y

        # Step 2: Still clipped — micro-scroll to bring box into view
        target = np.array(config.SERVICE_BOX_BORDER_RGB)
        tol = config.SERVICE_BOX_COLOR_TOLERANCE
        min_ratio = 0.90
        bottom_match = np.all(np.abs(img[-2:, :, :].astype(int) - target) <= tol, axis=-1)
        top_match = np.all(np.abs(img[:2, :, :].astype(int) - target) <= tol, axis=-1)
        bottom_ok = float(np.mean(bottom_match)) >= min_ratio
        top_ok = float(np.mean(top_match)) >= min_ratio
        h = img.shape[0]
        height_ok = h >= int(config.SERVICE_BOX_HEIGHT * 0.8)

        if not bottom_ok or not top_ok or not height_ok:
            # Determine scroll direction: if bottom is worse or image is too short,
            # scroll up (positive) to push content down; otherwise scroll down
            if not bottom_ok or (not height_ok and top_ok):
                direction = "bottom"
                scroll_nudge = config.SCROLL_PER_BOX
            else:
                direction = "top"
                scroll_nudge = -config.SCROLL_PER_BOX
            print(f"       Border clipped on {direction} "
                  f"(h={h}px, top={np.mean(top_match):.0%}, bottom={np.mean(bottom_match):.0%}), "
                  f"micro-scrolling to bring box into view...")

            # Scroll the list — move cursor in, scroll, then move out immediately
            # to prevent the game from changing selection on hover
            center_x = (config.SERVICE_LIST_LEFT + config.SERVICE_LIST_RIGHT) // 2
            center_y = (config.SERVICE_LIST_TOP + config.SERVICE_LIST_BOTTOM) // 2
            safe_x = config.SERVICE_LIST_LEFT - 100
            pyautogui.moveTo(center_x, center_y)
            pyautogui.scroll(scroll_nudge)
            pyautogui.moveTo(safe_x, center_y)
            time.sleep(1.0)

            # Find where the highlighted box actually is now
            new_y = _find_highlighted_box_y()
            if new_y is not None:
                final_y = new_y
                img, raw, found = _grab_and_crop_service_box(new_y)
    
                if found:
                    _, has_edges = _validate_border(img)

        if not has_edges:
            print("       WARNING: Border still clipped after scroll adjustment, saving anyway")

    Image.fromarray(img).save(path)
    return path, final_y


def click_service_box(x, y):
    """Click on a service box, then press Enter twice to load the level."""
    pyautogui.moveTo(x, y)
    time.sleep(0.5)
    pyautogui.mouseDown()
    time.sleep(0.2)
    pyautogui.mouseUp()
    time.sleep(1.5)           # give game time to highlight/select the box
    pyautogui.press("enter")
    time.sleep(1.0)
    pyautogui.press("enter")


def _check_for_level_screen():
    """Check once for driver selection or get_started screen.

    Returns 'driver' if driver screen found (and clicks it),
    'get_started' if get_started screen found, or None if neither.
    """
    # Check for training module popup and dismiss it
    if os.path.isfile(config.REF_TRAINING_POPUP):
        try:
            training_loc = pyautogui.locateOnScreen(config.REF_TRAINING_POPUP, confidence=config.CONFIDENCE)
            if training_loc is not None:
                print("       Training module popup detected — clicking 'Don't Ask Again'...")
                if os.path.isfile(config.REF_TRAINING_DONT_ASK_AGAIN):
                    btn_loc = pyautogui.locateOnScreen(config.REF_TRAINING_DONT_ASK_AGAIN, confidence=config.CONFIDENCE)
                    if btn_loc is not None:
                        center = pyautogui.center(btn_loc)
                        pyautogui.moveTo(center)
                        time.sleep(0.5)
                        pyautogui.mouseDown()
                        time.sleep(0.2)
                        pyautogui.mouseUp()
                        time.sleep(config.CLICK_SETTLE_DELAY)
                        print("       Training popup dismissed.")
                    else:
                        print("       WARNING: Training popup visible but 'Don't Ask Again' button not found")
                return None  # continue waiting for actual level screen
        except pyautogui.ImageNotFoundException:
            pass

    # Try each driver reference image
    for driver_ref in (config.REF_DRIVER_1, config.REF_DRIVER_2):
        if not os.path.isfile(driver_ref):
            continue
        try:
            driver_loc = pyautogui.locateOnScreen(driver_ref, confidence=config.CONFIDENCE)
            if driver_loc is not None:
                print(f"       Driver selection detected via '{os.path.basename(driver_ref)}' — clicking...")
                center = pyautogui.center(driver_loc)
                pyautogui.moveTo(center)
                time.sleep(0.5)
                pyautogui.mouseDown()
                time.sleep(0.2)
                pyautogui.mouseUp()
                time.sleep(config.CLICK_SETTLE_DELAY)
                return "driver"
        except pyautogui.ImageNotFoundException:
            pass

    # Try each get_started reference image
    for started_ref in (config.REF_GET_STARTED_1, config.REF_GET_STARTED_2):
        if not os.path.isfile(started_ref):
            continue
        try:
            started_loc = pyautogui.locateOnScreen(started_ref, confidence=config.CONFIDENCE)
            if started_loc is not None:
                return "get_started"
        except pyautogui.ImageNotFoundException:
            pass

    return None


def wait_for_level_load(click_x=None, click_y=None, service_dir=None, before_get_started=None):
    """Wait for the level to load, handle optional driver selection, then get past 'Get Started'.

    If click coordinates are provided, retries the service box click up to RETRY_MAX times
    (waiting RETRY_WAIT seconds between attempts) if the level doesn't load.
    If service_dir is provided, captures a screenshot of the level info area before clicking
    'Get Started'.
    """
    print("       Waiting for level to load...")

    for attempt in range(1, config.RETRY_MAX + 1):
        # Wait RETRY_WAIT seconds for either driver or get_started screen
        start = time.time()
        found = None
        while time.time() - start < config.RETRY_WAIT:
            found = _check_for_level_screen()
            if found is not None:
                break
            time.sleep(1.0)

        if found is not None:
            break

        # Screen hasn't changed — retry the click if we have coordinates
        if attempt < config.RETRY_MAX:
            if click_x is not None and click_y is not None:
                print(f"       Screen hasn't changed (attempt {attempt}/{config.RETRY_MAX}), "
                      f"clicking service at ({click_x}, {click_y}) again...")
                click_service_box(click_x, click_y)
            else:
                print(f"       Screen hasn't changed (attempt {attempt}/{config.RETRY_MAX}), "
                      f"waiting again...")
        else:
            raise TimeoutError("Timed out waiting for level to load after "
                               f"{config.RETRY_MAX} attempts")

    # If a driver selection screen was detected, record conductor_compatible in llm_data.json
    if found == "driver" and service_dir:
        try:
            import json as _json
            llm_data_path = os.path.join(service_dir, "llm_data.json")
            llm_data = {}
            if os.path.isfile(llm_data_path):
                with open(llm_data_path, "r", encoding="utf-8") as _f:
                    llm_data = _json.load(_f)
            llm_data["conductor_compatible"] = True
            with open(llm_data_path, "w", encoding="utf-8") as _f:
                _json.dump(llm_data, _f, indent=2)
            print(f"       Recorded conductor_compatible=True in llm_data.json")
        except Exception as _e:
            print(f"       WARNING: Failed to save conductor_compatible to llm_data.json: {_e}")

    # Now wait for either get_started variant and click it
    print("       Waiting for 'Get Started' screen...")
    get_started_refs = [r for r in (config.REF_GET_STARTED_1, config.REF_GET_STARTED_2)
                        if os.path.isfile(r)]
    found_loc = None
    start = time.time()
    while time.time() - start < config.SCREEN_TIMEOUT:
        for ref in get_started_refs:
            try:
                loc = pyautogui.locateOnScreen(ref, confidence=config.CONFIDENCE)
                if loc is not None:
                    found_loc = loc
                    break
            except pyautogui.ImageNotFoundException:
                pass
        if found_loc is not None:
            break
        time.sleep(1.0)
    if found_loc is None:
        raise TimeoutError("Timed out waiting for 'Get Started' screen")

    # Capture level splash screen region for LLM extraction
    if service_dir and getattr(config, 'USE_LLM_LEVEL_INFO', False):
        try:
            full_img = pyautogui.screenshot(region=config.LEVEL_SCREEN_REGION)
            full_path = os.path.join(service_dir, "level_screen.png")
            full_img.save(full_path)
            print(f"       Saved level_screen.png: {full_path}")
        except Exception as e:
            print(f"       WARNING: Failed to capture level_screen.png: {e}")

    # Run pre-click callback (e.g. LLM extraction) while level info screen is still visible
    if before_get_started:
        before_get_started()

    # Click 'Get Started' with retry — re-click if it's still visible
    for click_attempt in range(1, config.RETRY_MAX + 1):
        print("       Level loaded — clicking 'Get Started'...")
        center = pyautogui.center(found_loc)
        pyautogui.moveTo(center)
        time.sleep(0.5)
        pyautogui.mouseDown()
        time.sleep(0.2)
        pyautogui.mouseUp()
        time.sleep(3.0)

        # Check if Get Started is still on screen
        still_visible = False
        for ref in get_started_refs:
            try:
                loc = pyautogui.locateOnScreen(ref, confidence=config.CONFIDENCE)
                if loc is not None:
                    still_visible = True
                    found_loc = loc
                    break
            except pyautogui.ImageNotFoundException:
                pass

        if not still_visible:
            break

        if click_attempt < config.RETRY_MAX:
            print(f"       'Get Started' still visible (attempt {click_attempt}/{config.RETRY_MAX}), "
                  f"clicking again...")
        else:
            print(f"       WARNING: 'Get Started' still visible after {config.RETRY_MAX} attempts")

    time.sleep(5.0)           # game needs time to transition into gameplay


def exit_to_main_menu():
    """Press Escape, find 'Exit to Main Menu', and press Enter twice."""
    print("       Pressing Escape...")
    pyautogui.press("escape")
    time.sleep(4.0)           # pause menu needs time to render

    # Move cursor above schedule image to unfreeze any hover state
    schedule_center_x = (config.SCHEDULE_LEFT + config.SCHEDULE_RIGHT) // 2
    pyautogui.moveTo(schedule_center_x, config.SCHEDULE_TOP - 80)
    time.sleep(0.3)

    print("       Clicking 'Exit to Main Menu'...")
    main_menu_refs = [r for r in [config.REF_EXIT_TO_MAIN_MENU_1, config.REF_EXIT_TO_MAIN_MENU_2]
                      if os.path.isfile(r)]
    if not main_menu_refs:
        raise FileNotFoundError("No 'Exit to Main Menu' reference images found")
    clicked = False
    start = time.time()
    while time.time() - start < config.SCREEN_TIMEOUT:
        for ref in main_menu_refs:
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
                clicked = True
                break
        if clicked:
            break
        time.sleep(1.0)
    if not clicked:
        raise TimeoutError("Timed out waiting for 'Exit to Main Menu'")
    time.sleep(1.0)
    print("       Pressing Enter twice...")
    pyautogui.press("enter")
    time.sleep(0.5)
    pyautogui.press("enter")
    time.sleep(config.CLICK_SETTLE_DELAY)


def _count_visible_trains(img):
    """Count visible train entries by scanning the left edge for #dedede borders.

    If no trains are detected (e.g. the only train is highlighted in #4a90b5
    instead of the normal #dedede), falls back to checking for the highlight
    color and returns 1.

    Returns the number of distinct train entries visible in the image.
    """
    border_rgb = np.array([0xde, 0xde, 0xde])
    highlight_rgb = np.array([0x4a, 0x90, 0xb5])
    border_tol = 20
    min_entry_height = 40

    strip = img[:, :3, :].astype(int)
    match = np.all(np.abs(strip - border_rgb) <= border_tol, axis=2)
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

    count = len([(s, e) for s, e in runs if (e - s) >= min_entry_height])

    # Also check for a highlighted/selected train (#4a90b5) — this train
    # won't have the normal #dedede border, so count it separately.
    match_hl = np.all(np.abs(strip - highlight_rgb) <= border_tol, axis=2)
    row_has_hl = np.any(match_hl, axis=1)
    hl_runs = []
    in_hl = False
    hl_start = 0
    for r in range(len(row_has_hl)):
        if row_has_hl[r] and not in_hl:
            hl_start = r
            in_hl = True
        elif not row_has_hl[r] and in_hl:
            hl_runs.append((hl_start, r))
            in_hl = False
    if in_hl:
        hl_runs.append((hl_start, len(row_has_hl)))
    hl_count = len([(s, e) for s, e in hl_runs if (e - s) >= min_entry_height])
    count += hl_count

    return count


def count_trains():
    """Count the number of trains in the current train scroll box.

    Detects visible trains from the first frame, then scrolls one box at
    a time until the list stops moving. Returns visible + scrolls.
    Scrolls back to the top when done.
    """
    print("       Counting trains...")

    def _capture():
        region = (config.TRAIN_BOX_LEFT, config.TRAIN_BOX_TOP,
                  config.TRAIN_BOX_WIDTH, config.TRAIN_BOX_HEIGHT)
        return np.array(pyautogui.screenshot(region=region))

    def _frames_match(a, b):
        if a.shape != b.shape:
            return False
        return np.mean(np.abs(a.astype(float) - b.astype(float))) < 5.0

    first_img = _capture()
    visible_count = _count_visible_trains(first_img)
    print(f"       Visible trains in first frame: {visible_count}")

    # If all trains fit on screen (fewer than the max visible), no scrolling needed
    if visible_count < config.TRAIN_VISIBLE_COUNT:
        print(f"       All {visible_count} trains visible (< {config.TRAIN_VISIBLE_COUNT}), no scroll needed.")
        return visible_count

    prev_img = first_img
    scrolls = 0
    center_x = config.TRAIN_BOX_LEFT + config.TRAIN_BOX_WIDTH // 2
    center_y = config.TRAIN_BOX_TOP + config.TRAIN_BOX_HEIGHT // 2

    for _ in range(30):
        pyautogui.moveTo(center_x, center_y)
        time.sleep(0.3)
        pyautogui.scroll(config.TRAIN_SCROLL_PER_BOX)
        time.sleep(1.0)

        curr_img = _capture()
        if _frames_match(prev_img, curr_img):
            break
        scrolls += 1
        prev_img = curr_img

    total = visible_count + scrolls

    # Scroll back to top
    if scrolls > 0:
        pyautogui.moveTo(center_x, center_y)
        time.sleep(0.3)
        pyautogui.scroll(-config.TRAIN_SCROLL_PER_BOX * scrolls)
        time.sleep(1.0)

    print(f"       Found {total} trains ({visible_count} visible + {scrolls} scrolls).")
    return total


def _return_to_main_menu_from_menus():
    """From any in-game menu screen, press Escape until we reach the main menu.

    Checks for the 'To The Trains' tile to confirm we're at the main menu.
    Used when we're at the service list or class selection and need to get
    back to the main menu (not from inside a level — use exit_to_main_menu for that).
    """
    for attempt in range(6):
        # Check if we're already at the main menu (try both normal and hovered refs)
        at_menu = False
        for ref in [config.REF_TO_THE_TRAINS_1, config.REF_TO_THE_TRAINS_2]:
            if not os.path.isfile(ref):
                continue
            try:
                loc = pyautogui.locateOnScreen(ref, confidence=config.CONFIDENCE)
                if loc is not None:
                    at_menu = True
                    break
            except pyautogui.ImageNotFoundException:
                pass
        if at_menu:
            print("       At main menu.")
            return

        print(f"       Pressing Escape (attempt {attempt + 1})...")
        pyautogui.press("escape")
        time.sleep(2.0)

    print("       WARNING: Could not confirm main menu after Escape presses")


def navigate_to_train_list(train_name, extra_section_choice=None):
    """Re-navigate from main menu back to the train list (skips warning/splash).

    Stops at the train selection screen — does NOT click a specific train.
    """
    print("       Re-navigating to train list...")
    click_to_the_trains()
    click_choose_a_route()
    select_route()
    click_timetable()
    if extra_section_choice:
        select_extra_section(extra_section_choice)
    select_train(train_name)
    print("       Back at train list.")


def navigate_to_service_list(train_index, train_name, extra_section_choice=None):
    """Re-navigate from main menu back to the service list for a specific train."""
    navigate_to_train_list(train_name, extra_section_choice=extra_section_choice)
    click_train(train_index)


def scroll_service_list_to_top():
    """Scroll the service list all the way to the top.

    Scrolls up repeatedly until the list stops moving, so it works
    regardless of how far down the list was scrolled previously.
    """
    center_x = (config.SERVICE_LIST_LEFT + config.SERVICE_LIST_RIGHT) // 2
    center_y = (config.SERVICE_LIST_TOP + config.SERVICE_LIST_BOTTOM) // 2
    region = (config.SERVICE_LIST_LEFT, config.SERVICE_LIST_TOP,
              config.SERVICE_LIST_RIGHT - config.SERVICE_LIST_LEFT,
              config.SERVICE_LIST_BOTTOM - config.SERVICE_LIST_TOP)
    scroll_up = -config.SCROLL_PER_BOX * 8  # one page up

    pyautogui.moveTo(center_x, center_y)
    time.sleep(0.3)

    prev_img = np.array(pyautogui.screenshot(region=region))
    for _ in range(60):  # safety limit
        pyautogui.scroll(scroll_up)
        time.sleep(0.5)
        curr_img = np.array(pyautogui.screenshot(region=region))
        if curr_img.shape == prev_img.shape:
            diff = np.mean(np.abs(curr_img.astype(float) - prev_img.astype(float)))
            if diff < 5.0:
                break  # list stopped moving — we're at the top
        prev_img = curr_img
    time.sleep(0.5)


def scroll_service_list_down():
    """Scroll the service list down by BOXES_TO_SCROLL boxes (fixed amount)."""
    center_x = (config.SERVICE_LIST_LEFT + config.SERVICE_LIST_RIGHT) // 2
    center_y = (config.SERVICE_LIST_TOP + config.SERVICE_LIST_BOTTOM) // 2
    pyautogui.moveTo(center_x, center_y)
    time.sleep(0.3)
    pyautogui.scroll(config.SCROLL_AMOUNT)
    time.sleep(2.0)           # let scroll animation settle


def _prescan_service_list(current_page):
    """Screenshot all remaining pages of the service list and extract
    individual service box images for reference matching.

    Returns (box_images, total_pages_scanned, merged_img) where box_images
    is a list of numpy arrays, one per service box from the current page onward.
    """
    # Wider region to capture full service boxes + extra context
    extra_right = config.BOX_EXTRA_RIGHT + 40
    region = (
        config.SERVICE_LIST_LEFT,
        config.SERVICE_LIST_TOP,
        config.SERVICE_LIST_RIGHT - config.SERVICE_LIST_LEFT + extra_right,
        config.SERVICE_LIST_BOTTOM - config.SERVICE_LIST_TOP,
    )
    overlap_px = config.OVERLAP_BOXES * config.SERVICE_BOX_STRIDE  # 142px

    # Park cursor on the yellow scroll bar
    yellow_bar_ref = os.path.join(config.REFERENCES_DIR, "yellow_bar.png")
    bar_loc = pyautogui.locateOnScreen(yellow_bar_ref, confidence=0.8)
    if bar_loc:
        park_x = bar_loc.left + bar_loc.width // 2
        park_y = bar_loc.top + bar_loc.height // 2
    else:
        park_x = config.SERVICE_LIST_RIGHT + 15
        park_y = (config.SERVICE_LIST_TOP + config.SERVICE_LIST_BOTTOM) // 2
    pyautogui.moveTo(park_x, park_y)
    time.sleep(0.5)

    # Capture the current page
    pages = [np.array(pyautogui.screenshot(region=region))]
    prev_arr = pages[0]

    # Scroll and capture until end of list
    while True:
        scroll_service_list_down()
        # Move cursor away again after scroll
        pyautogui.moveTo(park_x, park_y)
        time.sleep(0.5)
        new_screenshot = pyautogui.screenshot(region=region)
        new_arr = np.array(new_screenshot)

        # End-of-list: compare with previous page
        if new_arr.shape == prev_arr.shape:
            diff = np.mean(np.abs(new_arr.astype(float) - prev_arr.astype(float)))
            if diff < 5.0:
                break

        pages.append(new_arr)
        prev_arr = new_arr

    print(f"       Pre-scan: captured {len(pages)} pages")

    # Stitch into one tall image — first page full, subsequent crop overlap
    strips = [pages[0]]
    for p in pages[1:]:
        strips.append(p[overlap_px:])
    merged = np.vstack(strips)

    # Extract individual service boxes
    box_images = []
    y = config.FIRST_BOX_TOP
    while y + config.SERVICE_BOX_HEIGHT <= merged.shape[0]:
        box = merged[y:y + config.SERVICE_BOX_HEIGHT, :]
        box_images.append(box)
        y += config.SERVICE_BOX_STRIDE

    print(f"       Pre-scan: extracted {len(box_images)} service box references")
    return box_images, len(pages), merged


def _service_is_duplicate(service_dir, prev_service_img):
    """Compare current service screenshot against the previous one.

    Returns True if they are nearly identical (duplicate from scroll drift).
    """
    if prev_service_img is None:
        return False
    curr_path = os.path.join(service_dir, "1_service.png")
    if not os.path.isfile(curr_path):
        return False
    curr_img = np.array(Image.open(curr_path))
    if curr_img.shape != prev_service_img.shape:
        return False
    diff = np.mean(np.abs(curr_img.astype(float) - prev_service_img.astype(float)))
    print(f"       Diff from previous service: {diff:.1f}")
    return diff < 5.0


def process_all_services(base_dir, train_index, train_name, max_services=None,
                         start_service=None, on_progress=None,
                         extra_section_choice=None):
    """Iterate through services for one train, capture each timetable.

    Always returns with the game at the MAIN MENU (after exit_to_main_menu).

    Args:
        base_dir: Directory for this train's service folders (e.g. screenshots/train_01/).
        train_index: 0-based index of the current train (for re-navigation).
        train_name: Name of the current train (for re-navigation).
        max_services: Maximum services to capture (None = unlimited).
        start_service: 1-indexed service number to resume from (None = start from 1).
        on_progress: Optional callback(train_name, train_number, service_number)
                     called before each service is attempted.
        extra_section_choice: If set, the extra section choice to select during navigation.
    """
    service_index = 0
    batch_count = 0
    page = 0
    previous_screenshot = None
    prev_service_img = None
    prev_service_img_path = None  # path to the previous service screenshot
    prev_service_y = None  # actual y_center of the last successfully captured service
    prescan_refs = None       # list of reference box images from pre-scan
    prescan_start_svc = None  # service index that prescan_refs[0] corresponds to
    prescan_num_pages = 0     # how many pages the prescan captured

    # Scroll the service list to the top before starting
    scroll_service_list_to_top()

    # Skip ahead if resuming from a specific service
    skip_to_box = None  # box index to start from on the first iteration
    if start_service is not None and start_service > 1:
        boxes_per_page = config.BOXES_PER_PAGE
        new_per_page = config.BOXES_TO_SCROLL  # new boxes per scroll (6)

        if start_service <= boxes_per_page:
            # Target is on the first page — no scrolling needed
            page = 0
            skip_to_box = start_service - 1
        else:
            # Target is on a later page
            page = (start_service - boxes_per_page + new_per_page - 1) // new_per_page
            first_on_page = boxes_per_page + (page - 1) * new_per_page + 1
            skip_to_box = config.OVERLAP_BOXES + (start_service - first_on_page)

            print(f"\n=== Resuming from service #{start_service} "
                  f"(page {page}, box {skip_to_box}) ===")

            for p in range(page):
                print(f"       Scrolling to page {p + 1}...")
                scroll_service_list_down()
            time.sleep(1.0)

        service_index = start_service - 1  # will be incremented to start_service in the loop

        # Load previous service image from disk so recovery can compare
        if start_service > 1:
            prev_svc_dir = os.path.join(base_dir, f"service_{start_service - 1:03d}")
            prev_svc_path = os.path.join(prev_svc_dir, "1_service.png")
            if os.path.isfile(prev_svc_path):
                prev_service_img = np.array(Image.open(prev_svc_path))
                prev_service_img_path = prev_svc_path
                # Previous service is one box before the resume target
                if skip_to_box is not None and skip_to_box > 0:
                    prev_box_y = get_visible_service_boxes()[skip_to_box - 1][1]
                    prev_service_y = prev_box_y
                print(f"       Loaded previous service image from {prev_svc_path}")

        if start_service <= boxes_per_page:
            print(f"\n=== Resuming from service #{start_service} (page 0, box {skip_to_box}) ===")
        time.sleep(1.0)

    while True:
        boxes = get_visible_service_boxes()
        page_y_adjust = 0  # reset each page; set by realign scroll nudge

        if skip_to_box is not None:
            start_box = skip_to_box
            skip_to_box = None  # only applies to the first iteration
        else:
            start_box = 0 if page == 0 else config.OVERLAP_BOXES  # skip overlap boxes on subsequent pages

        for i in range(start_box, len(boxes)):
            x, y = boxes[i]
            y += page_y_adjust
            service_index += 1
            print(f"\n--- Service #{service_index} ---")

            # Save progress so we can resume here if the bot crashes
            if on_progress:
                on_progress(train_name, train_index + 1, service_index)

            # Create per-service folder
            service_dir = os.path.join(base_dir, f"service_{service_index:03d}")
            os.makedirs(service_dir, exist_ok=True)

            use_prescan = (service_index >= 252)
            highlight_count = 0

            if use_prescan:
                # --- Pre-scan approach for services 252+ ---

                # One-time pre-scan: screenshot all remaining pages
                if prescan_refs is None:
                    print(f"       Running pre-scan of remaining service list...")
                    # Box i on this page = service_index, so box 0 = service_index - i
                    prescan_start_svc = service_index - i
                    prescan_refs, prescan_num_pages, merged_img = \
                        _prescan_service_list(page)
                    print(f"       Pre-scan complete: {len(prescan_refs)} boxes "
                          f"starting from service {prescan_start_svc}")
                    # Save merged image to train folder
                    merged_path = os.path.join(base_dir, "prescan_all_services.png")
                    Image.fromarray(merged_img).save(merged_path)
                    print(f"       Saved merged image: {merged_path}")
                    del merged_img  # free memory
                    # Scroll back to current page
                    scroll_service_list_to_top()
                    for _ in range(page):
                        scroll_service_list_down()
                    time.sleep(1.0)

                # Get reference image for this service
                ref_idx = service_index - prescan_start_svc
                if ref_idx < 0 or ref_idx >= len(prescan_refs):
                    print(f"       No reference for service {service_index} "
                          f"(idx {ref_idx}, have {len(prescan_refs)})")
                else:
                    ref_img = prescan_refs[ref_idx]

                    # Scan visible boxes for a match, scrolling forward as needed
                    max_scrolls = prescan_num_pages + 2
                    yellow_bar_ref = os.path.join(config.REFERENCES_DIR, "yellow_bar.png")
                    bar_loc = pyautogui.locateOnScreen(yellow_bar_ref, confidence=0.8)
                    if bar_loc:
                        park_x = bar_loc.left + bar_loc.width // 2
                        park_y = bar_loc.top + bar_loc.height // 2
                    else:
                        park_x = config.SERVICE_LIST_RIGHT + 15
                        park_y = (config.SERVICE_LIST_TOP + config.SERVICE_LIST_BOTTOM) // 2
                    for scroll_attempt in range(max_scrolls):
                        visible_boxes = get_visible_service_boxes()
                        # Move cursor off the list before screenshot
                        pyautogui.moveTo(park_x, park_y)
                        time.sleep(0.3)
                        extra_r = config.BOX_EXTRA_RIGHT + 40
                        region = (
                            config.SERVICE_LIST_LEFT,
                            config.SERVICE_LIST_TOP,
                            config.SERVICE_LIST_RIGHT - config.SERVICE_LIST_LEFT + extra_r,
                            config.SERVICE_LIST_BOTTOM - config.SERVICE_LIST_TOP,
                        )
                        screen_img = np.array(pyautogui.screenshot(region=region))

                        best_diff = 999
                        best_box_y = None
                        for bx, by in visible_boxes:
                            # Extract box from screen at this position
                            rel_top = (by - config.SERVICE_BOX_HEIGHT // 2
                                       - config.SERVICE_LIST_TOP)
                            rel_bot = rel_top + config.SERVICE_BOX_HEIGHT
                            if rel_top < 0 or rel_bot > screen_img.shape[0]:
                                continue
                            box_crop = screen_img[rel_top:rel_bot, :]

                            # Resize ref to match if needed
                            r = ref_img
                            if box_crop.shape != r.shape:
                                r = np.array(Image.fromarray(r).resize(
                                    (box_crop.shape[1], box_crop.shape[0]),
                                    Image.NEAREST))
                            diff = np.mean(np.abs(
                                box_crop.astype(float) - r.astype(float)
                            ))
                            if diff < best_diff:
                                best_diff = diff
                                best_box_y = by

                        if best_diff < 15.0 and best_box_y is not None:
                            print(f"       Matched ref at y={best_box_y} "
                                  f"(diff={best_diff:.1f})")
                            # Click it
                            pyautogui.moveTo(x, best_box_y)
                            time.sleep(0.5)
                            pyautogui.mouseDown()
                            time.sleep(0.2)
                            pyautogui.mouseUp()
                            time.sleep(1.5)
                            highlight_count, actual_y = wait_for_single_highlight()
                            if highlight_count > 0:
                                y = actual_y if actual_y else best_box_y
                            break
                        else:
                            # Not on this page — scroll down
                            scroll_service_list_down()
                            time.sleep(0.5)

            else:
                # Normal fixed-position click for services < 252
                pyautogui.moveTo(x, y)
                time.sleep(0.5)
                pyautogui.mouseDown()
                time.sleep(0.2)
                pyautogui.mouseUp()
                time.sleep(1.5)
                highlight_count, _ = wait_for_single_highlight()

            # Still nothing after all attempts — end of list
            if highlight_count == 0:
                shutil.rmtree(service_dir, ignore_errors=True)
                service_index -= 1
                print(f"       End of list. Processed {service_index} services.")
                _return_to_main_menu_from_menus()
                return service_index

            # Screenshot the selected service box (passes click coords for re-click retry)
            img_path, final_y = screenshot_service_box(service_dir, y, click_x=x, click_y=y)
            if final_y != y:
                y = final_y  # use corrected position for entering the level

            # Check for duplicate (scroll drift)
            if _service_is_duplicate(service_dir, prev_service_img):
                shutil.rmtree(service_dir, ignore_errors=True)
                service_index -= 1
                continue

            # Update previous service image and position for next comparison
            prev_service_img = np.array(Image.open(img_path))
            prev_service_img_path = img_path
            prev_service_y = y  # track actual y for position reference

            # Press Enter twice to load the level
            try:
                pyautogui.press("enter")
                time.sleep(1.0)
                pyautogui.press("enter")

                # Wait for level to load and get past "Get Started" screen
                # Passes click coordinates so it can re-click if the screen doesn't change
                wait_for_level_load(click_x=x, click_y=y, service_dir=service_dir)

                # Fetch player lat/lng for the first timetable entry
                first_lat, first_lng, _current_service = get_player_location()

                # Capture the schedule
                capture_schedule(service_dir)

                # Upload to parent app for OCR + database save
                # All 3 images (service, level info, schedule) are sent to /api/extract
                # which handles service name, level info, and timetable OCR together
                try:
                    result = upload_service(
                        service_dir, train_name,
                        train_index + 1, service_index,
                        first_lat=first_lat, first_lng=first_lng,
                    )
                    svc_name = result.get("service_name", "")
                    if result["duplicate"]:
                        print(f"       {svc_name} (duplicate — already recorded)")
                        with open(os.path.join(service_dir, "skip_note.txt"), "w") as f:
                            f.write(f"Service '{svc_name}' already exists in the database.\n")
                            f.write("Duplicate detected after OCR.\n")
                    elif result["success"]:
                        print(f"       {svc_name}")
                    else:
                        print(f"       Upload error: {result['error']}")
                except Exception as e:
                    print(f"       Upload error: {e}")

                # Exit back to main menu (we're now at main menu)
                exit_to_main_menu()

            except TimeoutError as e:
                raise ServiceError(
                    train_name, train_index + 1, service_index, e
                ) from e

            # Check service limit AFTER exiting to main menu, BEFORE re-navigating
            if max_services is not None and service_index >= max_services:
                print(f"\n       Reached service limit ({max_services}), stopping.")
                print(f"\nProcessed {service_index} services for this train.")
                return service_index  # at main menu

            # Check batch limit — restart game periodically to avoid degradation
            if config.BATCH_SIZE is not None:
                batch_count += 1
                if batch_count >= config.BATCH_SIZE:
                    print(f"\n       Batch limit ({config.BATCH_SIZE}) reached, restarting...")
                    _return_to_main_menu_from_menus()
                    raise BatchRestart(
                        train_name, train_index + 1, service_index + 1
                    )

            # Re-navigate to the service list
            try:
                navigate_to_service_list(train_index, train_name,
                                         extra_section_choice=extra_section_choice)
            except TimeoutError as e:
                raise ServiceError(
                    train_name, train_index + 1, service_index, e
                ) from e

            # Always scroll to top first — game may not reset scroll position
            scroll_service_list_to_top()

            # Scroll back to the right position
            for _ in range(page):
                scroll_service_list_down()
            time.sleep(1.0)

        # Scroll down for the next page
        scroll_service_list_down()

        # Take a screenshot to check if we've reached the end
        # (compare with previous page - if identical, we're done)
        check_region = (
            config.SERVICE_LIST_LEFT,
            config.SERVICE_LIST_TOP,
            config.SERVICE_LIST_RIGHT - config.SERVICE_LIST_LEFT,
            config.SERVICE_LIST_BOTTOM - config.SERVICE_LIST_TOP,
        )
        current_page_screenshot = pyautogui.screenshot(region=check_region)

        if previous_screenshot is not None:
            current_arr = np.array(current_page_screenshot)
            prev_arr = np.array(previous_screenshot)
            if current_arr.shape == prev_arr.shape:
                diff = np.mean(np.abs(current_arr.astype(float) - prev_arr.astype(float)))
                print(f"       Page difference: {diff:.1f}")
                if diff < 5.0:  # nearly identical = end of list
                    print("\n=== Reached end of service list ===")
                    break

        previous_screenshot = current_page_screenshot
        page += 1

    # Natural end of service list — we're at the service list (a menu).
    # Need to get back to main menu for the train loop.
    print(f"\n       Reached end of service list. Returning to main menu...")
    _return_to_main_menu_from_menus()
    print(f"\nProcessed {service_index} services for this train.")
    return service_index


def process_all_trains(train_name, start_train=None, start_service=None,
                       on_progress=None, max_services_override=None,
                       extra_section_choice=None):
    """Outer loop: iterate through all trains in the class, processing services for each.

    Args:
        train_name: Name of the train to process.
        start_train: 1-indexed train number to resume from (None = start from 1).
        start_service: 1-indexed service number to resume from on the first train
                       (None = start from 1). Only applies to the start_train.
        on_progress: Optional callback(train_name, train_number, service_number)
                     called before each service is attempted.
        max_services_override: If set, overrides config.MAX_SERVICES_PER_TRAIN.
    """
    from datetime import datetime, timedelta

    run_start = time.time()

    # Create route/class folder structure
    route_dir = os.path.join(config.SCREENSHOTS_DIR, config.ROUTE_NAME)
    if extra_section_choice:
        route_dir = os.path.join(route_dir, extra_section_choice)
    train_dir = os.path.join(route_dir, train_name)
    os.makedirs(train_dir, exist_ok=True)

    train_count = count_trains()
    if config.MAX_TRAINS_PER_CLASS is not None:
        train_count = min(train_count, config.MAX_TRAINS_PER_CLASS)
    print(f"\n=== Processing {train_count} trains for '{train_name}' ===\n")

    # Determine which train to start from (0-indexed)
    first_train = (start_train - 1) if start_train else 0
    if first_train > 0:
        print(f"Resuming from train {start_train}, service {start_service or 1}\n")

    train_results = []  # list of (train_number, service_count, duration_seconds)

    for train_idx in range(first_train, train_count):
        train_start = time.time()
        print(f"\n{'='*50}")
        print(f"=== Train {train_idx + 1}/{train_count} ===")
        print(f"{'='*50}")

        # Create per-train folder inside class folder
        train_dir = os.path.join(train_dir, f"train_{train_idx + 1:02d}")
        os.makedirs(train_dir, exist_ok=True)

        # Click the train
        try:
            click_train(train_idx)
        except TimeoutError as e:
            raise ServiceError(train_name, train_idx + 1, 1, e) from e

        # Only use start_service for the first train in a resume
        svc_start = start_service if train_idx == first_train else None

        # Process services for this train
        effective_max = max_services_override if max_services_override is not None else config.MAX_SERVICES_PER_TRAIN
        svc_count = process_all_services(
            base_dir=train_dir,
            train_index=train_idx,
            train_name=train_name,
            max_services=effective_max,
            start_service=svc_start,
            on_progress=on_progress,
            extra_section_choice=extra_section_choice,
        )

        train_duration = time.time() - train_start
        train_results.append((train_idx + 1, svc_count, train_duration))

        # Exit and relaunch game between trains to avoid memory issues.
        # process_all_services returns at main menu.
        if train_idx < train_count - 1:
            exit_game()
            relaunch_and_navigate(train_name, extra_section_choice=extra_section_choice)

    total_duration = time.time() - run_start
    total_services = sum(r[1] for r in train_results)

    print(f"\n=== All {train_count} trains processed for '{train_name}'! ===")

    # Write summary report
    report_path = os.path.join(train_dir, "report.txt")
    started = datetime.fromtimestamp(run_start)
    with open(report_path, "w") as f:
        f.write(f"TSW Timetable Bot — Run Report\n")
        f.write(f"{'='*40}\n\n")
        f.write(f"Route:       {config.ROUTE_NAME}\n")
        f.write(f"Train: {train_name}\n")
        f.write(f"Started:     {started:%Y-%m-%d %H:%M:%S}\n")
        f.write(f"Duration:    {timedelta(seconds=int(total_duration))}\n")
        f.write(f"Trains:      {train_count}\n")
        f.write(f"Services:    {total_services}\n\n")
        f.write(f"{'Train':<10} {'Services':<10} {'Duration'}\n")
        f.write(f"{'-'*10} {'-'*10} {'-'*10}\n")
        for train_num, svc_count, dur in train_results:
            f.write(f"Train {train_num:<4} {svc_count:<10} {timedelta(seconds=int(dur))}\n")

    print(f"\nReport saved to: {report_path}")
