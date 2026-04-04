"""Test script: click service box #8 (bottom of list) and debug the level load.

Assumes the game is already at the service list screen (train selected).
Run this manually while the service list is visible.
"""
import os
import sys
import time

sys.path.insert(0, os.path.join(os.path.dirname(__file__), '..'))

import pyautogui
pyautogui.FAILSAFE = False

import config
from utils import _get_best_confidence
from service_loop import (
    get_visible_service_boxes,
    screenshot_service_box,
    wait_for_level_load,
    exit_to_main_menu,
)


def main():
    print("=== Test: Service Box #8 ===\n")
    print("You have 5 seconds to make sure the service list is visible...")
    time.sleep(5)

    boxes = get_visible_service_boxes()
    print(f"Visible boxes: {len(boxes)}")
    for i, (bx, by) in enumerate(boxes):
        print(f"  Box {i+1}: ({bx}, {by})")

    # Target box 8 (index 7, last visible box)
    box_index = 7
    if box_index >= len(boxes):
        print(f"ERROR: Only {len(boxes)} boxes visible, can't reach box {box_index + 1}")
        return

    x, y = boxes[box_index]
    print(f"\n--- Targeting box #{box_index + 1} at ({x}, {y}) ---")

    # Click to select/highlight
    print(f"       Clicking service at ({x}, {y})...")
    pyautogui.moveTo(x, y)
    time.sleep(0.5)
    pyautogui.mouseDown()
    time.sleep(0.2)
    pyautogui.mouseUp()
    time.sleep(1.5)

    # Screenshot the selected box
    img_path = screenshot_service_box(8, y)
    print(f"       Saved: {img_path}")

    # Build list of driver references to check
    driver_refs = [(config.REF_DRIVER_1, "driver_1.png")]
    if os.path.isfile(config.REF_DRIVER_2):
        driver_refs.append((config.REF_DRIVER_2, "driver_2.png"))

    # Check confidence for driver and get_started BEFORE pressing Enter
    print("\n       Pre-Enter confidence check:")
    for ref_path, ref_name in driver_refs:
        conf = _get_best_confidence(ref_path)
        print(f"         {ref_name:20s} {conf:.3f} (need {config.CONFIDENCE})")
    started_conf = _get_best_confidence(config.REF_GET_STARTED_1)
    print(f"         {'get_started_1.png':20s} {started_conf:.3f} (need {config.CONFIDENCE})")

    # Press Enter twice to load
    print("\n       Pressing Enter twice...")
    pyautogui.press("enter")
    time.sleep(1.0)
    pyautogui.press("enter")

    # Poll for driver/get_started every 5 seconds, logging confidence
    print(f"\n       Polling for level load (logging every 5s)...")
    print(f"       Checking driver refs: {[name for _, name in driver_refs]}")
    start = time.time()
    found = None
    found_ref = None
    while time.time() - start < 90:
        confs = {}
        for ref_path, ref_name in driver_refs:
            confs[ref_name] = _get_best_confidence(ref_path)
        confs["get_started_1.png"] = _get_best_confidence(config.REF_GET_STARTED_1)

        elapsed = int(time.time() - start)
        parts = "  ".join(f"{name}={conf:.3f}" for name, conf in confs.items())
        print(f"         [{elapsed:3d}s] {parts}")

        for ref_path, ref_name in driver_refs:
            if confs[ref_name] >= config.CONFIDENCE:
                print(f"\n       FOUND {ref_name} at {elapsed}s (confidence {confs[ref_name]:.3f})")
                found = "driver"
                found_ref = ref_path
                break
        if found:
            break
        if confs["get_started_1.png"] >= config.CONFIDENCE:
            print(f"\n       FOUND get_started_1.png at {elapsed}s (confidence {confs['get_started_1.png']:.3f})")
            found = "get_started"
            break

        time.sleep(5)

    if found is None:
        print("\n       FAILED: Neither screen appeared within 90s")
        return

    # If driver found, click it and wait for get_started
    if found == "driver":
        print(f"       Clicking Driver (matched via {os.path.basename(found_ref)})...")
        try:
            loc = pyautogui.locateOnScreen(found_ref, confidence=config.CONFIDENCE)
            if loc:
                center = pyautogui.center(loc)
                pyautogui.moveTo(center)
                time.sleep(0.5)
                pyautogui.mouseDown()
                time.sleep(0.2)
                pyautogui.mouseUp()
                time.sleep(2.0)
        except pyautogui.ImageNotFoundException:
            print("       WARNING: Driver disappeared before we could click it")

        print("       Waiting for get_started after clicking Driver...")
        start2 = time.time()
        while time.time() - start2 < 60:
            started_conf = _get_best_confidence(config.REF_GET_STARTED_1)
            elapsed2 = int(time.time() - start2)
            print(f"         [{elapsed2:3d}s] get_started={started_conf:.3f}")
            if started_conf >= config.CONFIDENCE:
                print(f"\n       FOUND get_started_1.png at {elapsed2}s")
                break
            time.sleep(5)

    print("\n       Level loaded — pressing Enter twice...")
    time.sleep(0.5)
    pyautogui.press("enter")
    time.sleep(0.5)
    pyautogui.press("enter")
    time.sleep(5.0)

    print("       [TODO] Schedule capture not yet implemented")

    # Exit back
    print("\n       Exiting to main menu...")
    exit_to_main_menu()
    print("\n=== Test complete ===")


if __name__ == "__main__":
    main()
