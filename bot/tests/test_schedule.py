"""Test script: capture and stitch the schedule screen.

Assumes the game is already on the schedule screen.
Captures the scrollable area and stitches into one image.

Run this manually while the schedule is visible.
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


def find_overlap(prev_img, curr_img, band_height=40):
    """Find overlap between two frames using a template-match approach.

    Takes a narrow horizontal band from the top of curr_img and searches
    for its position in prev_img. The overlap is (prev_height - match_row).
    Returns the number of rows to skip from the top of curr_img, or 0.
    """
    h = prev_img.shape[0]

    # Take a band from the top area of the new frame (skip first few rows
    # in case of edge artifacts)
    band_start = 5
    band = curr_img[band_start:band_start + band_height, :, :].astype(float)

    # Slide the band down through prev_img looking for a match
    best_row = -1
    best_diff = float("inf")

    for row in range(0, h - band_height):
        strip = prev_img[row:row + band_height, :, :].astype(float)
        diff = np.mean(np.abs(strip - band))
        if diff < best_diff:
            best_diff = diff
            best_row = row

    if best_diff < 5.0:
        # The overlap is: everything from best_row in prev_img matches
        # everything from band_start in curr_img.
        # So curr_img rows 0..(h - best_row) overlap with prev_img.
        overlap = h - best_row + band_start
        return overlap

    return 0


def paint_seam(img, seam_row, radius=3):
    """Paint over a seam by sampling the dominant color above and below it.

    Takes the most common color from a few rows above the seam and fills
    the seam area with it.
    """
    h = img.shape[0]
    top = max(0, seam_row - radius)
    bottom = min(h, seam_row + radius + 1)

    # Sample color from a clean row above the seam
    sample_row = max(0, seam_row - radius - 2)
    fill_color = img[sample_row, :, :].copy()

    # Paint the seam area with the sampled row
    for r in range(top, bottom):
        img[r, :, :] = fill_color

    return img


def stitch_images(images):
    """Stitch schedule frames, removing overlapping regions and painting seams."""
    if not images:
        return None
    if len(images) == 1:
        return Image.fromarray(images[0])

    result = images[0]
    seam_rows = []

    for i in range(1, len(images)):
        overlap = find_overlap(images[i - 1], images[i])
        if overlap > 0:
            print(f"       Frame {i-1} → {i}: overlap = {overlap}px, cutting from top of frame {i}")
            new_part = images[i][overlap:, :, :]
        else:
            print(f"       Frame {i-1} → {i}: no overlap found, appending full frame")
            new_part = images[i]

        if new_part.shape[0] > 0:
            seam_rows.append(result.shape[0])  # track where the join happens
            result = np.vstack([result, new_part])

    # Paint over seams
    for seam_row in seam_rows:
        print(f"       Painting seam at row {seam_row}")
        result = paint_seam(result, seam_row)

    return Image.fromarray(result)


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


def main():
    print("=== Test: Schedule Capture ===\n")
    print("You have 5 seconds to make sure the schedule screen is visible...")
    time.sleep(5)

    # Create subfolder for test output
    output_dir = os.path.join(config.SCREENSHOTS_DIR, "schedule_test")
    os.makedirs(output_dir, exist_ok=True)

    # Step 1: Capture schedule frames by scrolling
    print("Step 1: Capturing schedule frames...\n")
    frames = []
    max_frames = 20  # safety limit

    for frame_idx in range(max_frames):
        img = capture_schedule_region()

        # Save individual frame for debugging
        frame_path = os.path.join(output_dir, f"frame_{frame_idx:02d}.png")
        Image.fromarray(img).save(frame_path)
        print(f"       Frame {frame_idx}: saved ({img.shape[0]}x{img.shape[1]})")

        # Detect end of scroll: compare with previous frame
        if frames and frames_match(frames[-1], img):
            print(f"       Frame matches previous — reached end of scroll!")
            break

        frames.append(img)

        # Scroll down for next frame
        print(f"       Scrolling down...")
        scroll_schedule_down()

    print(f"\n       Captured {len(frames)} frames total.")

    # Step 2: Stitch frames together
    print("\nStep 2: Stitching frames...")
    stitched = stitch_images(frames)
    if stitched is not None:
        stitched_arr = np.array(stitched)
        print(f"       Stitched size: {stitched_arr.shape[0]}x{stitched_arr.shape[1]}")

        output_path = os.path.join(output_dir, "schedule_stitched.png")
        stitched.save(output_path)
        print(f"       Saved: {output_path}")
    else:
        print("       ERROR: No frames to stitch")

    print("\n=== Test complete ===")


if __name__ == "__main__":
    main()
