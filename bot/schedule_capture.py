import os
import time

import numpy as np
import pyautogui
from PIL import Image

import config
from tsw_api import get_player_location
from utils import wait_and_click

SCHEDULE_MAX_WIDTH = 1640
SEPARATOR_RGB = np.array([0x3a, 0x55, 0x63])  # #3a5563 — dark line between blocks
SEPARATOR_TOL = 20

# The two alternating box colors in the schedule
BOX_COLORS = [
    np.array(config.SCHEDULE_BOTTOM_BORDER_RGB),   # #c5e4e9 (light blue)
    np.array(config.SCHEDULE_TOP_BORDER_RGB),       # #059744 (green)
]


def capture_schedule_region():
    """Capture the schedule area as a numpy array (RGB)."""
    region = (
        config.SCHEDULE_LEFT,
        config.SCHEDULE_TOP,
        config.SCHEDULE_RIGHT - config.SCHEDULE_LEFT,
        config.SCHEDULE_BOTTOM - config.SCHEDULE_TOP,
    )
    screenshot = pyautogui.screenshot(region=region)
    return np.array(screenshot)


def find_overlap(prev_img, curr_img, band_height=60, ncc_threshold=0.7):
    """Find overlap between two frames using normalized cross-correlation.

    Takes a band from the top of curr_img and searches for its best match
    in the bottom portion of prev_img using NCC on the left 60% of the image
    (avoiding status/time columns that change between captures).

    NCC is brightness-invariant, so it handles row color changes from the
    game updating completion state between scroll captures.

    Returns the number of overlapping rows, or 0 if no confident match.
    """
    h = prev_img.shape[0]
    # Use only left 60% to avoid status/time columns that change
    use_width = int(prev_img.shape[1] * 0.6)

    band_start = 5
    band = curr_img[band_start:band_start + band_height, :use_width, :].astype(float)
    band_std = band.std()
    if band_std < 1.0:
        return 0
    band_norm = (band - band.mean()) / band_std

    best_ncc = -1
    best_row = -1

    # Search bottom 60% of prev_img (overlap won't be in the top half)
    search_start = max(0, int(h * 0.4))
    for row in range(h - band_height, search_start, -1):
        strip = prev_img[row:row + band_height, :use_width, :].astype(float)
        strip_std = strip.std()
        if strip_std < 1.0:
            continue
        strip_norm = (strip - strip.mean()) / strip_std
        ncc = np.mean(band_norm * strip_norm)
        if ncc > best_ncc:
            best_ncc = ncc
            best_row = row

    if best_ncc > ncc_threshold and best_row >= 0:
        overlap = h - best_row + band_start
        return overlap

    return 0


def is_separator_row(img, row):
    """Check if a row in the image is a dark separator line (#132c39)."""
    w = img.shape[1]
    left = w // 10
    right = w - w // 10
    pixels = img[row, left:right, :].astype(int)
    match = np.all(np.abs(pixels - SEPARATOR_RGB) <= SEPARATOR_TOL, axis=1)
    return np.mean(match) > 0.3


def find_nearest_separator(img, target_row, search_range=50):
    """Find the nearest separator row to target_row, searching up and down.

    Returns the first row of the separator, or target_row if none found.
    """
    h = img.shape[0]
    for offset in range(search_range):
        for row in [target_row - offset, target_row + offset]:
            if 0 <= row < h and is_separator_row(img, row):
                return row
    return target_row


def find_separator_end(img, sep_row):
    """Find the last row of a separator starting at sep_row.

    Walks downward while rows still match the separator color.
    Returns the row index just past the separator.
    """
    h = img.shape[0]
    row = sep_row
    while row < h and is_separator_row(img, row):
        row += 1
    return row


def stitch_images(images):
    """Stitch schedule frames pairwise, cutting at dark separator lines.

    For each consecutive pair, finds the overlap using NCC, then snaps
    the cut to the nearest separator within the overlap zone.
    """
    if not images:
        return None
    if len(images) == 1:
        return Image.fromarray(images[0])

    result = images[0]

    for i in range(1, len(images)):
        prev = images[i - 1]
        curr = images[i]
        overlap = find_overlap(prev, curr)

        if overlap > 0:
            # The overlap region in curr is rows [0 .. overlap-1]
            # Find a separator near the middle of the overlap zone in curr
            # to make a clean cut
            mid_overlap = overlap // 2
            cut_in_curr = find_nearest_separator(curr, mid_overlap)

            # The corresponding row in prev:
            # row 0 of curr aligns with row (prev.height - overlap) of prev
            cut_in_prev = prev.shape[0] - overlap + cut_in_curr
            cut_in_prev = find_nearest_separator(prev, cut_in_prev)

            # Clamp to valid range
            cut_in_prev = min(cut_in_prev, prev.shape[0])
            cut_in_curr = max(cut_in_curr, 0)

            print(f"       Frame {i-1} -> {i}: overlap={overlap}px, "
                  f"cut prev at {cut_in_prev}, curr from {cut_in_curr}")

            # How much of result to keep: result may be longer than prev
            # due to previous stitches. Trim the last (prev.height - cut_in_prev) rows.
            rows_to_trim = prev.shape[0] - cut_in_prev
            result = result[:result.shape[0] - rows_to_trim, :, :]
            new_part = curr[cut_in_curr:, :, :]
        else:
            print(f"       Frame {i-1} -> {i}: no overlap found, appending full frame")
            new_part = curr

        if new_part.shape[0] > 0:
            result = np.vstack([result, new_part])

    return Image.fromarray(result)


def crop_schedule(img):
    """Crop the stitched schedule: trim below the last box and cap width.

    Scans from the bottom up to find the last row that matches either box
    color (#c5e4e9 or #059744), then crops everything below it.
    Also limits width to SCHEDULE_MAX_WIDTH pixels from the left.
    """
    tol = config.SCHEDULE_COLOR_TOLERANCE
    w = img.shape[1]
    left = w // 10
    right = w - w // 10
    strip = img[:, left:right, :].astype(int)

    last_box_row = img.shape[0]
    for row in range(strip.shape[0] - 1, -1, -1):
        for color in BOX_COLORS:
            match = np.all(np.abs(strip[row] - color) <= tol, axis=1)
            if np.mean(match) > 0.3:
                last_box_row = row + 1
                break
        if last_box_row < img.shape[0]:
            break

    # Keep a few extra rows past the last detected box to avoid cutting off partial rows
    last_box_row = min(last_box_row + 5, img.shape[0])
    img = img[:last_box_row, :min(img.shape[1], SCHEDULE_MAX_WIDTH), :]
    return img


def scroll_schedule_down(amount=-2000):
    """Scroll the schedule area down."""
    center_x = (config.SCHEDULE_LEFT + config.SCHEDULE_RIGHT) // 2
    center_y = (config.SCHEDULE_TOP + config.SCHEDULE_BOTTOM) // 2
    pyautogui.moveTo(center_x, center_y)
    time.sleep(0.3)
    pyautogui.scroll(amount)
    time.sleep(2.0)


def frames_match(a, b, threshold=5.0):
    """Check if two frames are nearly identical (scroll didn't move)."""
    if a.shape != b.shape:
        return False
    diff = np.mean(np.abs(a.astype(float) - b.astype(float)))
    return diff < threshold


def capture_schedule(output_dir, fetch_location=True, close_when_done=True):
    """Capture the full schedule by scrolling and stitching.

    Presses Escape, clicks Schedule, captures frames, stitches them,
    and saves the result as schedule.png in output_dir.

    Args:
        output_dir: Directory to save schedule images.
        fetch_location: If True, fetch player lat/lng after clicking Schedule
                        (game is paused on schedule screen, no timeout risk).
        close_when_done: If True, press Escape to close the schedule when done.
                         If False, leave the schedule open (caller must close it).

    Returns:
        tuple (schedule_path, lat, lng, current_service_name) where lat/lng/current_service_name may be None.
        Returns (None, None, None, None) on failure to open schedule.
    """
    # Press Escape to open pause menu
    print("       Pressing Escape for schedule...")
    pyautogui.press("escape")
    time.sleep(4.0)

    # Click 'Schedule'
    print("       Clicking 'Schedule'...")
    if not wait_and_click(config.REF_SCHEDULE, timeout=config.SCREEN_TIMEOUT,
                          confidence=config.CONFIDENCE):
        print("       ERROR: Could not find 'Schedule' on screen")
        return None, None, None, None
    time.sleep(3.0)

    # Fetch player lat/lng and currentServiceName while game is paused on schedule screen
    sched_lat, sched_lng, sched_current_service_name = None, None, None
    if fetch_location:
        sched_lat, sched_lng, sched_current_service_name = get_player_location()
        print(f"       Location (from schedule screen): {sched_lat}, {sched_lng} | service: {sched_current_service_name}")

    # Capture frames by scrolling
    print("       Capturing schedule frames...")
    frames = []
    max_frames = 20

    for frame_idx in range(max_frames):
        time.sleep(1.0)  # wait for screen to fully render before capturing
        img = capture_schedule_region()

        # Detect end of scroll: compare with previous frame
        if frames and frames_match(frames[-1], img):
            print(f"       Frame {frame_idx} matches previous — end of scroll")
            break

        frames.append(img)

        # Save individual frames for LLM schedule extraction
        if getattr(config, 'USE_LLM_SCHEDULE', False):
            frame_path = os.path.join(output_dir, f"3_schedule_frame_{frame_idx:02d}.png")
            Image.fromarray(img).save(frame_path)
        print(f"       Frame {frame_idx} captured")

        # Scroll down for next frame
        scroll_schedule_down()

    print(f"       Captured {len(frames)} frames, stitching...")

    # Stitch frames together
    stitched = stitch_images(frames)
    if stitched is None:
        print("       ERROR: No frames to stitch")
        return None, sched_lat, sched_lng, sched_current_service_name

    # Crop: trim below last box, cap width
    stitched_arr = crop_schedule(np.array(stitched))
    stitched = Image.fromarray(stitched_arr)
    print(f"       Cropped to {stitched_arr.shape[0]}x{stitched_arr.shape[1]}")

    output_path = os.path.join(output_dir, "3_schedule.png")
    stitched.save(output_path)
    print(f"       Saved schedule: {output_path}")

    # Close schedule (press Escape)
    if close_when_done:
        pyautogui.press("escape")
        time.sleep(2.0)

    return output_path, sched_lat, sched_lng, sched_current_service_name
