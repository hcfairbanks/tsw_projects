import os
import time
import cv2
import numpy as np
import pyautogui
from PIL import Image

import config


def _get_best_confidence(image_path):
    """Take a screenshot and check the best match confidence for a reference image."""
    screenshot = pyautogui.screenshot(region=config.GAME_REGION)
    screenshot_cv = cv2.cvtColor(np.array(screenshot), cv2.COLOR_RGB2BGR)
    needle = cv2.imread(image_path)
    if needle is None:
        return 0.0
    result = cv2.matchTemplate(screenshot_cv, needle, cv2.TM_CCOEFF_NORMED)
    return result.max()


def wait_for_image(image_path, timeout=60, confidence=0.8, interval=1.0):
    """Poll the screen for an image. Returns the location when found, or None on timeout."""
    name = os.path.basename(image_path)
    start = time.time()
    attempts = 0
    while time.time() - start < timeout:
        try:
            location = pyautogui.locateOnScreen(image_path, confidence=confidence,
                                                region=config.GAME_REGION)
            if location is not None:
                return location
        except pyautogui.ImageNotFoundException:
            pass
        attempts += 1
        if attempts % 5 == 0:
            best = _get_best_confidence(image_path)
            elapsed = int(time.time() - start)
            print(f"       ... still looking for '{name}' (best confidence: {best:.3f}, need: {confidence}, {elapsed}s elapsed)")
        time.sleep(interval)
    # Final debug info on timeout
    best = _get_best_confidence(image_path)
    print(f"       TIMEOUT: '{name}' best confidence was {best:.3f}, needed {confidence}")
    return None


def _screen_changed(image_path, confidence, wait_time, check_interval=2.0):
    """Wait up to wait_time seconds for an image to disappear from screen.

    Returns True if the image disappeared (screen changed), False if still visible.
    """
    start = time.time()
    while time.time() - start < wait_time:
        time.sleep(check_interval)
        try:
            loc = pyautogui.locateOnScreen(image_path, confidence=confidence,
                                           region=config.GAME_REGION)
        except pyautogui.ImageNotFoundException:
            loc = None
        if loc is None:
            return True
    return False


def wait_and_click(image_path, timeout=60, confidence=0.8, interval=1.0, click_duration=0.2,
                   verify=True):
    """Wait for an image to appear on screen, then click its center.

    If verify=True, checks that the clicked image disappeared (screen changed).
    If the screen hasn't changed after RETRY_WAIT seconds, clicks again.
    Tries up to RETRY_MAX times before giving up.

    Set verify=False for elements that stay on screen after clicking (e.g. text fields).

    Returns True if clicked successfully, False if image never appeared or
    screen never changed after all retries.
    """
    location = wait_for_image(image_path, timeout=timeout, confidence=confidence, interval=interval)
    if location is None:
        return False

    name = os.path.basename(image_path)

    for attempt in range(1, config.RETRY_MAX + 1):
        center = pyautogui.center(location)
        # Move mouse to target first, pause, then click — some game UIs need this
        pyautogui.moveTo(center)
        time.sleep(0.5)
        pyautogui.mouseDown()
        time.sleep(click_duration)
        pyautogui.mouseUp()

        if not verify:
            return True

        # Check if the screen changed (clicked image should disappear)
        if _screen_changed(image_path, confidence, wait_time=config.RETRY_WAIT):
            return True

        # Screen hasn't changed — click didn't register
        if attempt < config.RETRY_MAX:
            print(f"       Screen hasn't changed after clicking '{name}' "
                  f"(attempt {attempt}/{config.RETRY_MAX}), clicking again...")
            # Re-locate the image in case it shifted
            try:
                new_loc = pyautogui.locateOnScreen(image_path, confidence=confidence,
                                                   region=config.GAME_REGION)
                if new_loc is not None:
                    location = new_loc
            except pyautogui.ImageNotFoundException:
                return True  # Image disappeared between checks
        else:
            print(f"       WARNING: '{name}' still visible after "
                  f"{config.RETRY_MAX} attempts — giving up")
            return False

    return False
