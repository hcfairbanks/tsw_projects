"""Capture a reference image by cropping from pyautogui's own screenshot.

Usage:
    python capture_reference.py <output_name>

Example:
    python capture_reference.py warning_continue

Steps:
    1. Get TSW to the screen you want to capture
    2. Run this script
    3. A full screenshot opens in your default image viewer and is saved
    4. Note the pixel coordinates of the region you want
    5. Enter the coordinates when prompted
    6. The cropped image is saved to references/<output_name>.png
"""
import os
import sys
import subprocess
import pyautogui

REFERENCES_DIR = os.path.join(os.path.dirname(os.path.abspath(__file__)), "references")
SCREENSHOTS_DIR = os.path.join(os.path.dirname(os.path.abspath(__file__)), "screenshots")
os.makedirs(REFERENCES_DIR, exist_ok=True)
os.makedirs(SCREENSHOTS_DIR, exist_ok=True)

if len(sys.argv) < 2:
    print("Usage: python capture_reference.py <output_name>")
    print("Example: python capture_reference.py warning_continue")
    sys.exit(1)

name = sys.argv[1]
if not name.endswith(".png"):
    name += ".png"

# Take screenshot using pyautogui (same method locateOnScreen uses)
print("Taking screenshot...")
screenshot = pyautogui.screenshot()
print(f"Screenshot size: {screenshot.size}")

# Save the full screenshot so user can open it and find coordinates
full_path = os.path.join(SCREENSHOTS_DIR, "full_for_cropping.png")
screenshot.save(full_path)
print(f"\nFull screenshot saved to:\n  {full_path}")
print("\nOpening screenshot... Find the region you want to crop.")
print("Note the coordinates: top-left (x1, y1) and bottom-right (x2, y2)")

# Try to open the image
try:
    os.startfile(full_path)
except Exception:
    print(f"(Could not auto-open. Please open the file manually.)")

print()
print("Enter the crop coordinates (you can read them from your image editor):")
x1 = int(input("  Left   (x1): "))
y1 = int(input("  Top    (y1): "))
x2 = int(input("  Right  (x2): "))
y2 = int(input("  Bottom (y2): "))

cropped = screenshot.crop((x1, y1, x2, y2))
out_path = os.path.join(REFERENCES_DIR, name)
cropped.save(out_path)
print(f"\nReference image saved to: {out_path}")
print(f"Size: {cropped.size}")
print("Done!")
