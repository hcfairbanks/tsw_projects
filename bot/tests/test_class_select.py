"""Quick test: select specific trains and count trains.

Usage:
    cd bot/tests/
    python test_class_select.py "Class 66 DB"
    python test_class_select.py "Class 377/2 SN"

Expects the game to be on the train filter screen already.
"""
import os
import sys
import time

sys.path.insert(0, os.path.join(os.path.dirname(__file__), '..'))

import numpy as np
import pyautogui
pyautogui.FAILSAFE = False

import config
from navigator import select_train
from service_loop import count_trains

train_name = sys.argv[1] if len(sys.argv) > 1 else "Class 66 DB"

print(f"Testing train: {train_name}")
print("Starting in 5 seconds...")
time.sleep(5.0)

select_train(train_name)
print("Train selected, counting trains...")

# Save a screenshot of the train box area for debugging
region = (config.TRAIN_BOX_LEFT, config.TRAIN_BOX_TOP,
          config.TRAIN_BOX_WIDTH, config.TRAIN_BOX_HEIGHT)
img = np.array(pyautogui.screenshot(region=region))
from PIL import Image
safe_name = train_name.replace('/', '_')
debug_path = os.path.join(os.path.dirname(__file__), f"debug_train_list_{safe_name}.png")
Image.fromarray(img).save(debug_path)
print(f"Saved train list screenshot to: {debug_path}")

train_count = count_trains()
print(f"\nResult: {train_count} trains found for '{train_name}'")
