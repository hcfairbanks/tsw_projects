"""Take a screenshot and save it so you can compare with your reference images.

Run this while TSW is showing the screen you want to capture:
    python debug_screenshot.py

The screenshot is saved to screenshots/debug_screenshot.png
"""
import os
import pyautogui

out_dir = os.path.join(os.path.dirname(os.path.abspath(__file__)), "screenshots")
os.makedirs(out_dir, exist_ok=True)
out_path = os.path.join(out_dir, "debug_screenshot.png")

screenshot = pyautogui.screenshot()
screenshot.save(out_path)
print(f"Screenshot saved to: {out_path}")
print(f"Screenshot size: {screenshot.size}")

# Also show sizes of all reference images
ref_dir = os.path.join(os.path.dirname(os.path.abspath(__file__)), "references")
if os.path.isdir(ref_dir):
    print("\nReference images:")
    for f in sorted(os.listdir(ref_dir)):
        if f.endswith(".png"):
            from PIL import Image
            img = Image.open(os.path.join(ref_dir, f))
            print(f"  {f}: {img.size}")
