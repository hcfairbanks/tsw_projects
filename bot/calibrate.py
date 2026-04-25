# ══════════════════════════════════════════════════════════════════════════════
# TSW HUD Bot — Screen Calibration Tool
# ══════════════════════════════════════════════════════════════════════════════
#
# Walks you through every screen in game order so you can capture reference
# images and mark coordinate regions.  Saves coordinate results to
# 1_calibration.json and generates 1_config.py.
#
# For each step:
#   1. Navigate the game to the required screen
#   2. Hover your mouse over the indicated position
#   3. Press Enter in this terminal to record the position
#
# Steps:
#      1. Game Region        — Auto-detect the game window bounds
#      2. Warning Screen     — Capture the 'Continue' button on the disclaimer
#      3. Splash Continue    — Capture the 'Continue' button on the splash/title screen
#      4. To the Trains      — Capture the 'To the Trains' tile (image countdown)
#                               a. Not hovered
#                               b. Hovered
#      5. Choose a Route     — Capture the 'Choose a Route' tile (image countdown)
#                               a. Not hovered
#                               b. Hovered
#      6. Route Filter       — Capture the route filter/search text field
#      7. Timetable          — Capture the 'Timetable' tile (image countdown)
#                               a. Not hovered
#                               b. Hovered
#      8. Section Search     — Capture the section search field (optional extra section)
#      9. Section Tile       — Mark the center of the first section result tile
#     10. Train Class        — Capture the train class filter/search text field
#     11. Train Counting     — Mark the train list area, measure card positions & scroll
#     12. Service List Bounds — Mark the top-left and bottom-right of the service list
#     13. Service Box         — Measure a single service box (height, stride, scroll)
#     14. First Click         — Verify first service click position via Claude vision
#     15. Training Popup      — Capture training popup and 'Don't Ask Again' button (optional)
#     16. Driver              — Capture 'Driver' button (image countdown)
#                               a. Not hovered
#                               b. Hovered
#     17. Level Screen        — Mark the level info panel on the 'Get Started' screen
#     18. Get Started         — Capture 'Get Started' button (image countdown)
#                               a. Not hovered
#                               b. Hovered
#     19. Schedule            — Capture the 'Schedule' button, mark schedule area
#     20. Main Menu Home      — Capture 'return to home screen' button (image countdown)
#                               a. Not hovered
#                               b. Hovered
#     21. Exit                — Capture 'Exit Game' icon (image countdown)
#                               a. Not hovered
#                               b. Hovered
#     22. Exit Dialog Box     — Capture the exit confirmation dialog box background
#     23. Exit Confirm        — Capture 'Yes' button on the exit dialog (image countdown)
#                               a. Not hovered
#                               b. Hovered
#
# Usage:
#     python calibrate.py              # full calibration
#     python calibrate.py --step 3     # jump to a specific step
#     python calibrate.py --from 5     # start from step 5 onward
#     python calibrate.py --list       # list all steps
#
# Requirements:
#     - The game must be running and on the appropriate screen for each step.
#     - You will be asked to hover your mouse over specific UI elements and press Enter.
# ══════════════════════════════════════════════════════════════════════════════

import json
import os
import sys
import time

import pyautogui

pyautogui.FAILSAFE = False

BASE_DIR = os.path.dirname(os.path.abspath(__file__))
CALIBRATION_FILE = os.path.join(BASE_DIR, "calibration.json")
REFERENCES_DIR = os.path.join(BASE_DIR, "references")
os.makedirs(REFERENCES_DIR, exist_ok=True)

# Coordinate keys that are absolute X/Y positions (must convert to relative)
_X_COORD_KEYS = {
    "SERVICE_LIST_LEFT", "SERVICE_LIST_RIGHT",
    "SCHEDULE_LEFT", "SCHEDULE_RIGHT",
    "TRAIN_BOX_LEFT",
    "LEVEL_SCREEN_LEFT", "LEVEL_SCREEN_RIGHT",
    "EXTRA_SECTION_TILE_X",
}
_Y_COORD_KEYS = {
    "SERVICE_LIST_TOP", "SERVICE_LIST_BOTTOM",
    "SCHEDULE_TOP", "SCHEDULE_BOTTOM",
    "TRAIN_BOX_TOP",
    "LEVEL_SCREEN_TOP", "LEVEL_SCREEN_BOTTOM",
    "EXTRA_SECTION_TILE_Y",
}

# ── helpers ──────────────────────────────────────────────────────────────────


def load_existing():
    """Load existing calibration if available."""
    if os.path.isfile(CALIBRATION_FILE):
        with open(CALIBRATION_FILE, "r") as f:
            cal = json.load(f)
        # If loading a relative (v2) file, convert back to absolute for the
        # interactive session (user points at the screen in absolute coords).
        if cal.get("_version", 1) >= 2:
            gx, gy = cal.get("GAME_REGION", [0, 0, 1920, 1080])[:2]
            for key in list(cal.keys()):
                if key in _X_COORD_KEYS:
                    cal[key] += gx
                elif key in _Y_COORD_KEYS:
                    cal[key] += gy
            cal.pop("_version", None)
        return cal
    return {}


def save_calibration(data):
    """Write calibration data to JSON (relative coords) and generate 1_config.py."""
    # Convert absolute -> relative before saving
    gx, gy = data.get("GAME_REGION", [0, 0, 1920, 1080])[:2]
    save_data = dict(data)  # shallow copy
    for key in list(save_data.keys()):
        if key in _X_COORD_KEYS:
            save_data[key] -= gx
        elif key in _Y_COORD_KEYS:
            save_data[key] -= gy
    save_data["_version"] = 2

    with open(CALIBRATION_FILE, "w") as f:
        json.dump(save_data, f, indent=2)
    print(f"\nSaved calibration to {CALIBRATION_FILE} (relative coords, v2)")

    # Generate 1_config.py — imports everything from config, then overrides
    config_path = os.path.join(BASE_DIR, "1_config.py")

    tuple_keys = {
        "GAME_REGION", "LEVEL_SCREEN_REGION",
    }

    lines = [
        '"""Auto-generated test config — created by calibrate.py.',
        '',
        'Imports everything from config.py, then overrides calibrated values',
        'from 1_calibration.json so you can test without touching the original.',
        '"""',
        'from config import *  # noqa: F401,F403',
        '',
        '# ── Calibration overrides ──',
    ]

    for key, val in sorted(data.items()):
        if key in tuple_keys:
            lines.append(f"{key} = {tuple(val)}")
        else:
            lines.append(f"{key} = {val!r}")

    lines.append('')
    lines.append('# ── Recomputed derived values ──')
    lines.append('BOXES_TO_SCROLL = BOXES_PER_PAGE - OVERLAP_BOXES')
    lines.append('SCROLL_AMOUNT = SCROLL_PER_BOX * BOXES_TO_SCROLL')
    if 'LEVEL_SCREEN_LEFT' in data:
        lines.append('LEVEL_SCREEN_REGION = (LEVEL_SCREEN_LEFT, LEVEL_SCREEN_TOP,')
        lines.append('                       LEVEL_SCREEN_RIGHT - LEVEL_SCREEN_LEFT,')
        lines.append('                       LEVEL_SCREEN_BOTTOM - LEVEL_SCREEN_TOP)')
    lines.append('')

    with open(config_path, "w", encoding="utf-8") as f:
        f.write('\n'.join(lines))
    print(f"Generated test config: {config_path}")


def get_mouse_pos(prompt, default=None):
    """Ask user to position mouse and press Enter. Returns (x, y)."""
    hint = f" [default: {default}]" if default else ""
    print(f"\n  >> {prompt}{hint}")
    input("     Move your mouse to the position, then press Enter... ")
    x, y = pyautogui.position()
    print(f"     Recorded: ({x}, {y})")
    return (x, y)


def get_int(prompt, default=None):
    """Prompt for an integer value."""
    hint = f" [default: {default}]" if default is not None else ""
    val = input(f"\n  >> {prompt}{hint}: ").strip()
    if not val and default is not None:
        print(f"     Using default: {default}")
        return default
    return int(val)


def get_region(prompt_tl, prompt_br, defaults=None):
    """Ask user to mark top-left and bottom-right corners. Returns (left, top, right, bottom)."""
    d_tl = (defaults[0], defaults[1]) if defaults else None
    d_br = (defaults[2], defaults[3]) if defaults else None
    tl = get_mouse_pos(prompt_tl, default=d_tl)
    br = get_mouse_pos(prompt_br, default=d_br)
    return (tl[0], tl[1], br[0], br[1])



def capture_reference(filename, reuse_region=None):
    """Capture a reference image by marking corners, then taking the screenshot.

    If reuse_region is provided as (tl, br), skip corner marking and reuse those points.
    """
    if reuse_region:
        tl, br = reuse_region
        print("  Now capture the HOVERED state.")
        print("  Press enter and you will have 3 seconds to hover your mouse over the area")
        input("  Press Enter when ready... ")
        print("\n     Capturing in...")
        for i in range(3, 0, -1):
            print(f"       {i}...")
            time.sleep(1)
    else:
        tl = get_mouse_pos("TOP-LEFT")
        br = get_mouse_pos("BOTTOM-RIGHT")

        print("\n     Capturing now...")

    screenshot = pyautogui.screenshot()

    cropped = screenshot.crop((tl[0], tl[1], br[0], br[1]))
    out_path = os.path.join(REFERENCES_DIR, filename)
    cropped.save(out_path)
    print(f"     Saved reference: {out_path} ({cropped.size[0]}x{cropped.size[1]}px)")
    return (out_path, tl, br)


def banner(step_num, total, title, description):
    """Print a step banner."""
    print(f"\n{'#'*60}")
    print(f"  STEP {step_num} - {total}: {title}")
    print(f"{'#'*60}")
    if description:
        print(f"\n  {description}")


TOTAL_STEPS = 23


# ── calibration steps (in game flow order) ───────────────────────────────────


def _detect_game_window():
    """Try to auto-detect the TSW game window via win32 API. Returns (x, y, w, h) or None."""
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
                        results.append((
                            title,
                            rect.left, rect.top,
                            rect.right - rect.left,
                            rect.bottom - rect.top,
                        ))
            return True

        user32.EnumWindows(WNDENUMPROC(callback), 0)
        if results:
            return results[0]  # (title, x, y, w, h)
    except Exception as e:
        print(f"  Auto-detect error: {e}")
    return None


def step_00_game_region(cal):
    """Auto-detect the game window region, polling until found."""
    banner(1, TOTAL_STEPS, "GAME REGION", "")

    while True:
        result = _detect_game_window()
        if result is not None:
            title, x, y, w, h = result
            # Clamp to screen bounds — window rect can include invisible borders
            if y < 0:
                h += y
                y = 0
            if x < 0:
                w += x
                x = 0
            detected = (x, y, w, h)
            print(f"{'#'*60}")
            print(f"  Auto-detected game window: \"{title}\"")
            print(f"    Position: ({x}, {y})  Size: {w}x{h}")
            cal["GAME_REGION"] = list(detected)
            print(f"     GAME_REGION = {tuple(detected)}")
            return
        print("  Game window not found. Retrying in 3 seconds...")
        time.sleep(3)


def step_01_warning_screen(cal):
    """Capture the Warning Screen 'Continue' button."""
    banner(2, TOTAL_STEPS, "WARNING SCREEN", "")

    input("  Press Enter when the warning screen is visible... ")
    capture_reference("warning_continue.png")


def step_02_splash_continue(cal):
    """Capture the Splash 'Continue' button."""
    banner(3, TOTAL_STEPS, "SPLASH CONTINUE", "")

    input("  Press Enter when the splash screen is visible... ")
    capture_reference("splash_continue.png")


def step_03_to_the_trains(cal):
    """Capture the 'To the Trains' button in normal and hovered states."""
    banner(4, TOTAL_STEPS, "TO THE TRAINS (IMAGE COUNTDOWN)",
           "Two images are required here.\n"
           "  1. A non-hover state\n"
           "  2. A hover state (you will see a yellow border)")

    _, tl, br = capture_reference("to_the_trains_1.png")
    capture_reference("to_the_trains_2.png", reuse_region=(tl, br))


def step_04_choose_a_route(cal):
    """Capture the 'Choose a Route' tile in normal and hovered states."""
    banner(5, TOTAL_STEPS, "CHOOSE A ROUTE (IMAGE COUNTDOWN)",
           "Two images are required here.\n"
           "  1. A non-hover state\n"
           "  2. A hover state (you will see a yellow border)")

    _, tl, br = capture_reference("choose_a_route_1.png")
    capture_reference("choose_a_route_2.png", reuse_region=(tl, br))


def step_05_route_filter(cal):
    """Capture the route filter/search field."""
    banner(6, TOTAL_STEPS, "ROUTE FILTER", "")

    input("  Press Enter when the route filter field is visible... ")
    capture_reference("route_search.png")


def step_06_route_options(cal):
    """Capture the Timetable tile (route options) in normal and hovered states."""
    banner(7, TOTAL_STEPS, "TIMETABLE (IMAGE COUNTDOWN)",
           "Two images are required here.\n"
           "  1. A non-hover state\n"
           "  2. A hover state (you will see a yellow border)")

    _, tl, br = capture_reference("timetable_1.png")
    capture_reference("timetable_2.png", reuse_region=(tl, br))


def step_07a_section_search(cal):
    """Capture the section search/filter field (extra section screen)."""
    banner(8, TOTAL_STEPS, "SECTION SEARCH", "")

    input("  Press Enter when the section screen is visible... ")
    capture_reference("section_search.png")


def step_07b_section_tile(cal):
    """Capture the position of the first section result tile."""
    banner(9, TOTAL_STEPS, "SECTION TILE POSITION",
           "Type a section name in the search field so a result tile appears,\n"
           "  then hover your mouse over the CENTER of the first result tile.")

    pos = get_mouse_pos(
        "Hover over the CENTER of the first section result tile",
        default=(cal.get("EXTRA_SECTION_TILE_X"), cal.get("EXTRA_SECTION_TILE_Y")),
    )
    cal["EXTRA_SECTION_TILE_X"] = pos[0]
    cal["EXTRA_SECTION_TILE_Y"] = pos[1]
    print(f"     EXTRA_SECTION_TILE_X = {pos[0]}")
    print(f"     EXTRA_SECTION_TILE_Y = {pos[1]}")


def step_07_train_class(cal):
    """Capture the train class filter field."""
    banner(10, TOTAL_STEPS, "TRAIN CLASS", "")

    input("  Press Enter when the train class screen is visible... ")
    capture_reference("class_search.png")


def step_08_train_counting(cal):
    """Calibrate the train selection scroll box."""
    banner(11, TOTAL_STEPS, "TRAIN COUNTING", "")

    input("  Press Enter when the train list is visible on screen... ")

    tl = get_mouse_pos(
        "TOP-LEFT",
        default=(cal.get("TRAIN_BOX_LEFT"), cal.get("TRAIN_BOX_TOP")),
    )
    br = get_mouse_pos(
        "BOTTOM-RIGHT",
        default=(cal.get("TRAIN_BOX_LEFT", 0) + cal.get("TRAIN_BOX_WIDTH", 450),
                 cal.get("TRAIN_BOX_TOP", 0) + cal.get("TRAIN_BOX_HEIGHT", 472))
        if cal.get("TRAIN_BOX_LEFT") else None,
    )
    cal["TRAIN_BOX_LEFT"] = tl[0]
    cal["TRAIN_BOX_TOP"] = tl[1]
    cal["TRAIN_BOX_WIDTH"] = br[0] - tl[0]
    cal["TRAIN_BOX_HEIGHT"] = br[1] - tl[1]

    visible = get_int(
        "How many trains are visible without scrolling?",
        default=cal.get("TRAIN_VISIBLE_COUNT", 5),
    )
    cal["TRAIN_VISIBLE_COUNT"] = visible

    print("\n  Now let's measure the actual train card positions.")
    first_center = get_mouse_pos(
        "Hover over the CENTER of the FIRST train card",
        default=(cal.get("TRAIN_BOX_LEFT", tl[0]) + cal.get("TRAIN_BOX_WIDTH", br[0] - tl[0]) // 2,
                 tl[1] + cal.get("TRAIN_FIRST_Y_OFFSET", 45))
        if cal.get("TRAIN_FIRST_Y_OFFSET") else None,
    )
    first_y_offset = first_center[1] - tl[1]
    cal["TRAIN_FIRST_Y_OFFSET"] = first_y_offset
    print(f"     First train Y offset from box top: {first_y_offset}px")

    if visible >= 2:
        second_center = get_mouse_pos(
            "Hover over the CENTER of the SECOND train card",
        )
        stride = second_center[1] - first_center[1]
    else:
        stride = get_int(
            "Stride (distance between train card centers in px)",
            default=cal.get("TRAIN_BOX_STRIDE", 90),
        )
    cal["TRAIN_BOX_STRIDE"] = stride
    print(f"     Stride (center-to-center): {stride}px")

    scroll_per_box = get_int(
        "Scroll units per train box (negative = down). Press Enter for default",
        default=cal.get("TRAIN_SCROLL_PER_BOX", -310),
    )
    cal["TRAIN_SCROLL_PER_BOX"] = scroll_per_box

    print(f"\n  Train box calibration complete!")
    print(f"    Area: ({tl[0]}, {tl[1]}) — {cal['TRAIN_BOX_WIDTH']}x{cal['TRAIN_BOX_HEIGHT']}")
    print(f"    {visible} visible, first_y_offset={first_y_offset}px, stride={stride}px")


def step_09_services(cal):
    """Calibrate the service list bounds."""
    banner(12, TOTAL_STEPS, "SERVICE LIST BOUNDS", "")

    input("  Press Enter when the service list is visible on screen... ")

    left, top, right, bottom = get_region(
        "TOP-LEFT",
        "BOTTOM-RIGHT",
        defaults=(cal.get("SERVICE_LIST_LEFT"), cal.get("SERVICE_LIST_TOP"),
                  cal.get("SERVICE_LIST_RIGHT"), cal.get("SERVICE_LIST_BOTTOM")),
    )
    cal["SERVICE_LIST_LEFT"] = left
    cal["SERVICE_LIST_TOP"] = top
    cal["SERVICE_LIST_RIGHT"] = right
    cal["SERVICE_LIST_BOTTOM"] = bottom

    print(f"\n  Service list bounds: ({left}, {top}) to ({right}, {bottom})")


def step_10_service_box(cal):
    """Measure a single service box (height, stride, count, scroll)."""
    banner(13, TOTAL_STEPS, "SERVICE BOX MEASUREMENT", "")

    input("  Press Enter when the service list is visible on screen... ")

    box_tl = get_mouse_pos("Hover over the TOP edge of any single service box")
    box_bl = get_mouse_pos("Hover over the BOTTOM edge of that SAME service box")
    box_height = box_bl[1] - box_tl[1]
    cal["SERVICE_BOX_HEIGHT"] = box_height
    print(f"     Box height: {box_height}px")

    next_tl = get_mouse_pos("Hover over the TOP edge of the NEXT service box below it")
    stride = next_tl[1] - box_tl[1]
    cal["SERVICE_BOX_STRIDE"] = stride
    print(f"     Box stride (top-to-top): {stride}px")

    list_top = cal.get("SERVICE_LIST_TOP", box_tl[1])
    first_box_top = box_tl[1] - list_top
    cal["FIRST_BOX_TOP"] = first_box_top
    print(f"     First box offset from list top: {first_box_top}px")

    boxes_per_page = get_int(
        "How many service boxes are fully visible at once?",
        default=cal.get("BOXES_PER_PAGE", 8),
    )
    cal["BOXES_PER_PAGE"] = boxes_per_page

    scroll_per_box = get_int(
        "Scroll units per box (negative = down). Press Enter for default",
        default=cal.get("SCROLL_PER_BOX", -268),
    )
    cal["SCROLL_PER_BOX"] = scroll_per_box

    print(f"\n  Service box measurement complete!")
    print(f"    Box: {box_height}px tall, {stride}px stride, {boxes_per_page} per page")


def step_11_first_click(cal):
    """Verify the first service click location using Claude."""
    banner(14, TOTAL_STEPS, "FIRST CLICK VERIFICATION", "")

    required = ["GAME_REGION", "SERVICE_LIST_LEFT", "SERVICE_LIST_TOP",
                "SERVICE_LIST_RIGHT", "SERVICE_LIST_BOTTOM"]
    missing = [k for k in required if k not in cal]
    if missing:
        print(f"  SKIP: Missing calibration keys: {missing}")
        print(f"  Run steps 9-10 first.")
        return

    game_region = tuple(cal["GAME_REGION"])
    gx, gy = game_region[0], game_region[1]

    input("  Press Enter when the service list is visible on screen... ")

    # Record where the user thinks the first click should be
    user_pos = get_mouse_pos(
        "Hover over the CENTER of the FIRST service box",
        default=None,
    )
    user_x, user_y = user_pos
    print(f"     User position (absolute): ({user_x}, {user_y})")
    print(f"     User position (in image): ({user_x - gx}, {user_y - gy})")

    # Take screenshot
    import base64
    import io

    img = pyautogui.screenshot(region=game_region)
    img_w, img_h = img.size

    temp_dir = os.path.join(BASE_DIR, "screenshots")
    os.makedirs(temp_dir, exist_ok=True)
    screenshot_path = os.path.join(temp_dir, "_calibrate_first_click.png")
    img.save(screenshot_path)
    print(f"  Saved screenshot: {screenshot_path} ({img_w}x{img_h})")

    # Send to Claude
    if not os.environ.get("ANTHROPIC_API_KEY"):
        print("  ERROR: ANTHROPIC_API_KEY not set. Skipping LLM verification.")
        return

    try:
        import anthropic
    except ImportError:
        print("  ERROR: anthropic package not installed. Skipping LLM verification.")
        return

    buf = io.BytesIO()
    img.save(buf, format="PNG")
    img_b64 = base64.standard_b64encode(buf.getvalue()).decode("utf-8")

    # Convert user position to image coordinates
    user_img_x = user_x - gx
    user_img_y = user_y - gy

    prompt = (
        f"This is a {img_w}x{img_h} pixel screenshot of a train simulation game.\n"
        f"The user marked the center of the first service box at pixel ({user_img_x}, {user_img_y}).\n\n"
        "The service list contains rectangular service boxes stacked vertically.\n"
        "Each box has a service name, start time (HH:MM:SS), and duration (HH:MM:SS).\n\n"
        "Please:\n"
        "1. Find the FIRST fully visible service box in the list\n"
        "2. Report the exact CENTER pixel (x, y) of that box within this image\n"
        "3. Report the service name, start_time, and duration from that box\n"
        "4. Report how far off the user's marked position is from the true center\n\n"
        "Respond ONLY with valid JSON:\n"
        '{"center_x": <pixel>, "center_y": <pixel>, '
        '"name": "<service name>", "start_time": "<HH:MM:SS>", "duration": "<HH:MM:SS>", '
        '"user_offset_x": <pixels off>, "user_offset_y": <pixels off>}\n'
        "No other text."
    )

    client = anthropic.Anthropic()
    try:
        response = client.messages.create(
            model="claude-sonnet-4-6",
            max_tokens=1024,
            messages=[{
                "role": "user",
                "content": [
                    {
                        "type": "image",
                        "source": {
                            "type": "base64",
                            "media_type": "image/png",
                            "data": img_b64,
                        },
                    },
                    {"type": "text", "text": prompt},
                ],
            }],
        )
        response_text = response.content[0].text
        tokens_in = response.usage.input_tokens
        tokens_out = response.usage.output_tokens
        print(f"  Claude response: {tokens_in}+{tokens_out} tokens")
    except Exception as e:
        print(f"  ERROR: Claude API call failed: {e}")
        return

    # Parse response
    try:
        cleaned = response_text.strip()
        if cleaned.startswith("```"):
            cleaned = cleaned.split("\n", 1)[1]
            cleaned = cleaned.rsplit("```", 1)[0]
        result = json.loads(cleaned)
    except json.JSONDecodeError:
        print(f"  ERROR: Could not parse LLM response as JSON")
        print(f"  Raw: {response_text[:500]}")
        return

    llm_x = result.get("center_x", user_img_x)
    llm_y = result.get("center_y", user_img_y)
    name = result.get("name", "?")
    start = result.get("start_time", "?")
    dur = result.get("duration", "?")
    off_x = result.get("user_offset_x", 0)
    off_y = result.get("user_offset_y", 0)

    print(f"\n  ── Claude First Click Analysis ──")
    print(f"    First service: {name}  {start} / {dur}")
    print(f"    LLM center (in image):  ({llm_x}, {llm_y})")
    print(f"    User marked (in image): ({user_img_x}, {user_img_y})")
    print(f"    Offset: x={off_x:+d}  y={off_y:+d}")

    # Convert LLM coords back to absolute screen coords
    llm_abs_x = llm_x + gx
    llm_abs_y = llm_y + gy
    print(f"    LLM center (absolute):  ({llm_abs_x}, {llm_abs_y})")

    # Compute calibration values from LLM position
    list_top = cal.get("SERVICE_LIST_TOP", gy)
    box_height = cal.get("SERVICE_BOX_HEIGHT", 60)
    llm_first_box_top = llm_abs_y - box_height // 2 - list_top

    cur_first_top = cal.get("FIRST_BOX_TOP", 0)
    print(f"\n    FIRST_BOX_TOP:  LLM={llm_first_box_top}  current={cur_first_top}  delta={llm_first_box_top - cur_first_top:+d}")

    apply = input("\n  Apply LLM-refined first click position? (Y/n): ").strip().lower()
    if apply in ("", "y", "yes"):
        cal["FIRST_BOX_TOP"] = llm_first_box_top
        cal["FIRST_CLICK_X"] = llm_x   # image-relative x
        cal["FIRST_CLICK_Y"] = llm_y   # image-relative y
        print(f"  Applied: FIRST_BOX_TOP = {llm_first_box_top}")
        print(f"  Applied: FIRST_CLICK_X = {llm_x} (in image)")
        print(f"  Applied: FIRST_CLICK_Y = {llm_y} (in image)")
    else:
        print(f"  Keeping current value: FIRST_BOX_TOP = {cur_first_top}")



def step_12_training(cal):
    """Capture the training module popup and 'Don't Ask Again' button (optional)."""
    banner(15, TOTAL_STEPS, "TRAINING POPUP (OPTIONAL)",
           "If the training module popup appears after clicking a service,\n"
           "  capture two images:\n"
           "  1. The training popup header/body\n"
           "  2. The 'Don't Ask Again' button")

    skip = input("  Type 's' to skip, or press Enter to capture... ").strip().lower()
    if skip == "s":
        print("  Skipping training popup capture.")
        return
    _, tl, br = capture_reference("training.png")
    capture_reference("training_dont_ask_again.png")


def step_12_driver(cal):
    """Capture the Driver button in normal and hovered states."""
    banner(16, TOTAL_STEPS, "DRIVER (IMAGE COUNTDOWN)",
           "Two images are required here.\n"
           "  1. A non-hover state\n"
           "  2. A hover state (you will see a yellow border)")

    _, tl, br = capture_reference("driver_1.png")
    capture_reference("driver_2.png", reuse_region=(tl, br))


def step_10_level_screen(cal):
    """Calibrate the level info panel region on the 'Get Started' screen."""
    banner(17, TOTAL_STEPS, "LEVEL SCREEN", "")

    input("  Press Enter when the 'Get Started' screen is visible... ")

    left, top, right, bottom = get_region(
        "TOP-LEFT",
        "BOTTOM-RIGHT",
        defaults=(cal.get("LEVEL_SCREEN_LEFT"), cal.get("LEVEL_SCREEN_TOP"),
                  cal.get("LEVEL_SCREEN_RIGHT"), cal.get("LEVEL_SCREEN_BOTTOM")),
    )
    cal["LEVEL_SCREEN_LEFT"] = left
    cal["LEVEL_SCREEN_TOP"] = top
    cal["LEVEL_SCREEN_RIGHT"] = right
    cal["LEVEL_SCREEN_BOTTOM"] = bottom

    print(f"\n  Level screen region: ({left}, {top}) to ({right}, {bottom})")


def step_15_start(cal):
    """Capture the 'Get Started' button in normal and hovered states."""
    banner(18, TOTAL_STEPS, "GET STARTED (IMAGE COUNTDOWN)",
           "Two images are required here.\n"
           "  1. A non-hover state\n"
           "  2. A hover state (you will see a yellow border)")

    _, tl, br = capture_reference("get_started_1.png")
    capture_reference("get_started_2.png", reuse_region=(tl, br))


def step_16_schedule(cal):
    """Calibrate the schedule screen area."""
    banner(19, TOTAL_STEPS, "SCHEDULE", "")

    input("  Press Enter when the pause menu is visible... ")

    print("\n  Let's capture the 'Schedule' button first.")
    capture_reference("schedule.png")

    input("\n  Now click 'Schedule' so the schedule screen appears, then press Enter... ")

    left, top, right, bottom = get_region(
        "TOP-LEFT",
        "BOTTOM-RIGHT",
        defaults=(cal.get("SCHEDULE_LEFT"), cal.get("SCHEDULE_TOP"),
                  cal.get("SCHEDULE_RIGHT"), cal.get("SCHEDULE_BOTTOM")),
    )
    cal["SCHEDULE_LEFT"] = left
    cal["SCHEDULE_TOP"] = top
    cal["SCHEDULE_RIGHT"] = right
    cal["SCHEDULE_BOTTOM"] = bottom

    print(f"\n  Schedule area: ({left}, {top}) to ({right}, {bottom})")



def step_16b_main_menu_home(cal):
    """Capture the main menu 'return to home screen' button in normal and hovered states."""
    banner(20, TOTAL_STEPS, "MAIN MENU HOME (IMAGE COUNTDOWN)",
           "Two images are required here.\n"
           "  1. A non-hover state\n"
           "  2. A hover state (you will see a yellow border)")

    _, tl, br = capture_reference("exit_to_main_menu_1.png")
    capture_reference("exit_to_main_menu_2.png", reuse_region=(tl, br))


def step_17_exit(cal):
    """Capture the exit icon in normal and hovered states."""
    banner(21, TOTAL_STEPS, "EXIT (IMAGE COUNTDOWN)",
           "Exit the level, go back to the main menu.\n"
           "  In the bottom right you will see the exit icon")

    input("  Press Enter when the main menu is visible... ")

    print("\n  Two images are required here.")
    print("  1. A non-hover state")
    print("  2. A hover state (you will see a yellow border)")

    _, tl, br = capture_reference("exit_game_1.png")
    capture_reference("exit_game_2.png", reuse_region=(tl, br))


def step_18a_exit_dialogbox(cal):
    """Capture the exit confirmation dialog box background."""
    banner(22, TOTAL_STEPS, "EXIT DIALOG BOX",
           "Click the 'Exit Game' icon so the confirmation dialog appears.\n"
           "  Capture the dialog box background (not the Yes/No buttons).")

    input("  Press Enter when the exit dialog is visible... ")

    capture_reference("exit_game_dialogbox.png")


def step_18b_exit_confirm(cal):
    """Capture the Yes button on the exit dialog."""
    banner(23, TOTAL_STEPS, "EXIT CONFIRM (IMAGE COUNTDOWN)",
           "Two images are required here.\n"
           "  1. A non-hover state\n"
           "  2. A hover state (you will see a yellow border)")

    _, tl, br = capture_reference("exit_game_yes_1.png")
    capture_reference("exit_game_yes_2.png", reuse_region=(tl, br))


# ── step registry ────────────────────────────────────────────────────────────

STEPS = [
    ("Game Region",          step_00_game_region),
    ("Warning Screen",       step_01_warning_screen),
    ("Splash Continue",      step_02_splash_continue),
    ("To the Trains",        step_03_to_the_trains),
    ("Choose a Route",       step_04_choose_a_route),
    ("Route Filter",         step_05_route_filter),
    ("Timetable",            step_06_route_options),
    ("Section Search",       step_07a_section_search),
    ("Section Tile",         step_07b_section_tile),
    ("Train Class",          step_07_train_class),
    ("Train Counting",       step_08_train_counting),
    ("Service List Bounds",  step_09_services),
    ("Service Box",          step_10_service_box),
    ("First Click",          step_11_first_click),
    ("Training Popup",       step_12_training),
    ("Driver",               step_12_driver),
    ("Level Screen",         step_10_level_screen),
    ("Get Started",          step_15_start),
    ("Schedule",             step_16_schedule),
    ("Main Menu Home",       step_16b_main_menu_home),
    ("Exit",                 step_17_exit),
    ("Exit Dialog Box",      step_18a_exit_dialogbox),
    ("Exit Confirm",         step_18b_exit_confirm),
]


# ── review & save ────────────────────────────────────────────────────────────

def review_and_save(cal):
    """Show a summary of coordinate calibration and save."""
    print(f"\n{'#'*60}")
    print(f"  REVIEW & SAVE")
    print(f"{'#'*60}")

    sections = [
        ("Game Region", [
            ("GAME_REGION",),
        ]),
        ("Train Box", [
            ("TRAIN_BOX_LEFT", "TRAIN_BOX_TOP", "TRAIN_BOX_WIDTH", "TRAIN_BOX_HEIGHT"),
            ("TRAIN_VISIBLE_COUNT", "TRAIN_BOX_STRIDE", "TRAIN_FIRST_Y_OFFSET", "TRAIN_SCROLL_PER_BOX"),
        ]),
        ("Service List", [
            ("SERVICE_LIST_LEFT", "SERVICE_LIST_TOP", "SERVICE_LIST_RIGHT", "SERVICE_LIST_BOTTOM"),
            ("SERVICE_BOX_HEIGHT", "SERVICE_BOX_STRIDE", "FIRST_BOX_TOP", "BOXES_PER_PAGE"),
            ("SCROLL_PER_BOX",),
        ]),
        ("Level Screen", [
            ("LEVEL_SCREEN_LEFT", "LEVEL_SCREEN_TOP", "LEVEL_SCREEN_RIGHT", "LEVEL_SCREEN_BOTTOM"),
        ]),
        ("Schedule", [
            ("SCHEDULE_LEFT", "SCHEDULE_TOP", "SCHEDULE_RIGHT", "SCHEDULE_BOTTOM"),
        ]),
    ]

    print()
    for section_name, groups in sections:
        print(f"  {section_name}:")
        for group in groups:
            for key in group:
                if key in cal:
                    print(f"    {key} = {cal[key]}")
        print()

    confirm = input("  Save this calibration? (Y/n): ").strip().lower()
    if confirm in ("", "y", "yes"):
        save_calibration(cal)
        print("\n  Calibration saved!")
        print("  To test: import 1_config instead of config in your scripts.")
        print("  When happy, copy 1_calibration.json -> calibration.json to make it permanent.")
    else:
        print("\n  Calibration NOT saved. Re-run to try again.")


# ── main ─────────────────────────────────────────────────────────────────────

def main():
    # --list: just print steps and exit
    if "--list" in sys.argv:
        for i, (name, _) in enumerate(STEPS, 1):
            print(f"  {i:2d}. {name}")
        return

    single_step = None
    from_step = None

    if "--step" in sys.argv:
        idx = sys.argv.index("--step")
        if idx + 1 < len(sys.argv):
            single_step = int(sys.argv[idx + 1])

    if "--from" in sys.argv:
        idx = sys.argv.index("--from")
        if idx + 1 < len(sys.argv):
            from_step = int(sys.argv[idx + 1])

    cal = load_existing()

    if single_step is not None:
        step_name, step_fn = STEPS[single_step - 1]
        print(f"  Running step {single_step} ({step_name}) only...\n")
        step_fn(cal)
        if cal:
            save_calibration(cal)
    elif from_step is not None:
        print(f"  Starting from step {from_step}...\n")
        for i, (step_name, step_fn) in enumerate(STEPS):
            if i + 1 >= from_step:
                step_fn(cal)
                if cal:
                    save_calibration(cal)
        review_and_save(cal)
    else:
        for step_name, step_fn in STEPS:
            step_fn(cal)
            if cal:
                save_calibration(cal)
        review_and_save(cal)


if __name__ == "__main__":
    main()
