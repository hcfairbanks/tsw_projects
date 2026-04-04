"""Test script: count the number of trains in a train scroll box.

Detects visible trains via #dedede border on left edge, then scrolls
one box at a time until the list stops moving.
Total = detected_visible + scrolls.

Assumes the game is already on the train selection page.
Run with: python test_train_count.py
"""
import os
import sys
import time

sys.path.insert(0, os.path.join(os.path.dirname(__file__), '..'))

import numpy as np
import pyautogui
from PIL import Image

pyautogui.FAILSAFE = False

import config

# Train scroll box coordinates
TRAIN_BOX_LEFT = 3218
TRAIN_BOX_TOP = 418
TRAIN_BOX_WIDTH = 450
TRAIN_BOX_HEIGHT = 472

# Scroll amount for one train box
SCROLL_PER_TRAIN = -310

# Border detection
BORDER_RGB = np.array([0xde, 0xde, 0xde])
BORDER_TOL = 20
MIN_ENTRY_HEIGHT = 40


def capture_train_box():
    """Capture the train scroll box area as a numpy array (RGB)."""
    region = (TRAIN_BOX_LEFT, TRAIN_BOX_TOP, TRAIN_BOX_WIDTH, TRAIN_BOX_HEIGHT)
    screenshot = pyautogui.screenshot(region=region)
    return np.array(screenshot)


def count_visible_trains(img):
    """Count visible train entries by scanning the left edge for #dedede borders.

    Returns (count, runs) where runs is a list of (start_row, end_row) tuples.
    """
    strip = img[:, :3, :].astype(int)
    match = np.all(np.abs(strip - BORDER_RGB) <= BORDER_TOL, axis=2)
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

    entries = [(s, e) for s, e in runs if (e - s) >= MIN_ENTRY_HEIGHT]
    return len(entries), entries, runs


def frames_match(a, b, threshold=5.0):
    """Check if two frames are nearly identical."""
    if a.shape != b.shape:
        return False
    return np.mean(np.abs(a.astype(float) - b.astype(float))) < threshold


def scroll_train_box(amount=SCROLL_PER_TRAIN):
    """Scroll the train box area by one box."""
    center_x = TRAIN_BOX_LEFT + TRAIN_BOX_WIDTH // 2
    center_y = TRAIN_BOX_TOP + TRAIN_BOX_HEIGHT // 2
    pyautogui.moveTo(center_x, center_y)
    time.sleep(0.3)
    pyautogui.scroll(amount)
    time.sleep(1.0)


def main():
    print("=== Test: Train Count ===\n")
    print("You have 5 seconds to make sure the train selection page is visible...")
    time.sleep(5)

    output_dir = os.path.join(config.SCREENSHOTS_DIR, "train_test")
    os.makedirs(output_dir, exist_ok=True)

    # Capture first frame and detect visible trains
    first_img = capture_train_box()
    Image.fromarray(first_img).save(os.path.join(output_dir, "train_frame_00.png"))

    visible, entries, all_runs = count_visible_trains(first_img)
    print(f"       Frame 0 captured and saved")
    print(f"       Border detection — all runs (including small):")
    for s, e in all_runs:
        height = e - s
        marker = " <-- TRAIN" if height >= MIN_ENTRY_HEIGHT else "     (too small)"
        print(f"         rows {s}-{e} ({height}px){marker}")
    print(f"       Detected {visible} visible trains\n")

    # Scroll and count
    prev_img = first_img
    scrolls = 0
    max_scrolls = 30

    for i in range(1, max_scrolls + 1):
        scroll_train_box()
        curr_img = capture_train_box()
        Image.fromarray(curr_img).save(os.path.join(output_dir, f"train_frame_{i:02d}.png"))

        if frames_match(prev_img, curr_img):
            print(f"       Frame {i} matches previous — end of scroll")
            break

        scrolls += 1
        print(f"       Frame {i} captured (scroll #{scrolls})")
        prev_img = curr_img

    total = visible + scrolls
    print(f"\n       Detected visible: {visible}")
    print(f"       Successful scrolls: {scrolls}")
    print(f"       === Total train count: {total} ===")

    print("\n=== Test complete ===")


if __name__ == "__main__":
    main()
