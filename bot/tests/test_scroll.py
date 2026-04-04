"""Test scrolling in the service list.

Run while TSW is showing the service list:
    python test_scroll.py
"""
import os
import sys
import time

sys.path.insert(0, os.path.join(os.path.dirname(__file__), '..'))

import pyautogui
pyautogui.FAILSAFE = False

import config

out_dir = config.SCREENSHOTS_DIR
os.makedirs(out_dir, exist_ok=True)

center_x = (config.SERVICE_LIST_LEFT + config.SERVICE_LIST_RIGHT) // 2
center_y = (config.SERVICE_LIST_TOP + config.SERVICE_LIST_BOTTOM) // 2

print(f"Moving mouse to service area center: ({center_x}, {center_y})")
pyautogui.moveTo(center_x, center_y)
time.sleep(1.0)

region = (
    config.SERVICE_LIST_LEFT,
    config.SERVICE_LIST_TOP,
    config.SERVICE_LIST_RIGHT - config.SERVICE_LIST_LEFT,
    config.SERVICE_LIST_BOTTOM - config.SERVICE_LIST_TOP,
)

screenshot = pyautogui.screenshot(region=region)
path = os.path.join(out_dir, "scroll_before.png")
screenshot.save(path)
print(f"Saved before screenshot: {path}")

print("\nScrolling 5000 clicks...")
pyautogui.scroll(-2140)
time.sleep(2.0)

screenshot = pyautogui.screenshot(region=region)
path = os.path.join(out_dir, "scroll_after.png")
screenshot.save(path)
print(f"Saved: {path}")

print("\nDone! Compare scroll_before.png and scroll_after.png.")
