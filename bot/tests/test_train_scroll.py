"""Test script: scroll the train list by a configurable amount.

Assumes the game is already on the train selection page.
Run with: python test_train_scroll.py [scroll_amount]

Default scroll amount is -100. Negative scrolls down.
"""
import sys
import time

import pyautogui

pyautogui.FAILSAFE = False

# Train scroll box coordinates
TRAIN_BOX_LEFT = 3218
TRAIN_BOX_TOP = 418
TRAIN_BOX_WIDTH = 450
TRAIN_BOX_HEIGHT = 472


def scroll_train_box(amount):
    center_x = TRAIN_BOX_LEFT + TRAIN_BOX_WIDTH // 2
    center_y = TRAIN_BOX_TOP + TRAIN_BOX_HEIGHT // 2
    pyautogui.moveTo(center_x, center_y)
    time.sleep(0.3)
    pyautogui.scroll(amount)


def main():
    amount = int(sys.argv[1]) if len(sys.argv) > 1 else -100

    print(f"Scrolling train box by {amount} in 3 seconds...")
    time.sleep(3)

    scroll_train_box(amount)
    print(f"Done. Scrolled by {amount}.")


if __name__ == "__main__":
    main()
