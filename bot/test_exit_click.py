"""Test script: finds and clicks the exit game YES button using the same
references and logic as exit_game() in navigator.py.

Run while the exit confirmation dialog is visible on screen.
"""
import os
import sys
import time
import pyautogui

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))
import config

yes_refs = [config.REF_EXIT_GAME_YES_1, config.REF_EXIT_GAME_YES_2]

print(f"Confidence: {config.CONFIDENCE}")
print(f"Searching for YES button in 3 seconds... (switch to the game)")
time.sleep(3)

for ref in yes_refs:
    name = os.path.basename(ref)
    if not os.path.isfile(ref):
        print(f"  {name} — file not found, skipping")
        continue

    print(f"\n  Trying {name}...")
    try:
        loc = pyautogui.locateOnScreen(ref, confidence=config.CONFIDENCE,
                                       region=config.GAME_REGION)
    except pyautogui.ImageNotFoundException:
        loc = None

    if loc is None:
        print(f"  {name} — no match on screen")
        continue

    center = pyautogui.center(loc)
    print(f"  MATCH: {name}")
    print(f"    Region: left={loc.left}, top={loc.top}, w={loc.width}, h={loc.height}")
    print(f"    Center: x={center.x}, y={center.y}")
    print(f"    Clicking in 2 seconds...")
    time.sleep(2)

    pyautogui.moveTo(center)
    time.sleep(0.5)
    pyautogui.mouseDown()
    time.sleep(0.2)
    pyautogui.mouseUp()
    print("  Clicked.")
    break
else:
    print("\nNo YES reference matched anything on screen.")
