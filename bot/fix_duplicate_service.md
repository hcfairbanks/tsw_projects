# Fix: Duplicate Service at Page Boundary (043/044)

## Problem

After a full run of 50 services, service_043.png and service_044.png are identical
(`1A79: Liverpool Lime Street - London Euston, 21:28, 00:39`). This is the only true
duplicate — the pixel comparison flagged other pairs as similar but visual inspection
confirmed they are all different services with similar route text.

## Root Cause

The duplicate occurs at a page boundary (between page 5 and page 6). Here's how paging works:

- 8 boxes are visible at a time
- `scroll_service_list_down()` scrolls by `SCROLL_PER_BOX * (visible_count - 1)` = `-268 * 7` = `-1876` clicks
- On subsequent pages, `start_box = 1` (skip first box as overlap)
- The intention: scroll 7 boxes, the 8th box from the previous page becomes box 0 on the new page, and we skip it

The problem: by page 5 (after 5 scrolls), small per-scroll drift has accumulated enough
that the scroll lands 1 box short. The "overlap" box 0 is actually a NEW service, and
box 1 (the first one we process) is the SAME as the last box from the previous page.

## The Fix

**File: `service_loop.py`, function `scroll_service_list_down()` (line ~256)**

Change the scroll amount from `visible_count - 1` (7 boxes) to `visible_count` (8 boxes).
This means we scroll a full page instead of page-minus-one. Combined with `start_box = 1`
(skip first box), this gives a 1-box safety margin against drift:

```python
# BEFORE (current code):
def scroll_service_list_down():
    """Scroll the service list down by one page (minus 1 box for overlap)."""
    center_x = (config.SERVICE_LIST_LEFT + config.SERVICE_LIST_RIGHT) // 2
    center_y = (config.SERVICE_LIST_TOP + config.SERVICE_LIST_BOTTOM) // 2
    pyautogui.moveTo(center_x, center_y)
    time.sleep(0.3)
    visible_count = len(get_visible_service_boxes())
    scroll_clicks = config.SCROLL_PER_BOX * (visible_count - 1)   # 7 boxes
    pyautogui.scroll(scroll_clicks)
    time.sleep(2.0)

# AFTER (fixed):
def scroll_service_list_down():
    """Scroll the service list down by one full page."""
    center_x = (config.SERVICE_LIST_LEFT + config.SERVICE_LIST_RIGHT) // 2
    center_y = (config.SERVICE_LIST_TOP + config.SERVICE_LIST_BOTTOM) // 2
    pyautogui.moveTo(center_x, center_y)
    time.sleep(0.3)
    visible_count = len(get_visible_service_boxes())
    scroll_clicks = config.SCROLL_PER_BOX * visible_count          # 8 boxes (full page)
    pyautogui.scroll(scroll_clicks)
    time.sleep(2.0)
```

With this change:
- We scroll 8 boxes (a full page)
- `start_box = 1` still skips the first box on each new page
- Net effect: we advance by 7 NEW services per page (same as before when scroll was accurate)
- But now we have a 1-box buffer against drift, so even if the scroll is off by a few
  pixels after many pages, we won't get duplicates

## Trade-off

There's a small risk of SKIPPING a service if the scroll overshoots by more than 1 box.
Given the calibration of -268 clicks per box is fairly accurate (tested), this is unlikely.
If it does happen, the fix would be to increase the overlap to `start_box = 2` (skip 2 boxes).

## How to Verify

After applying the fix, run the bot and compare all screenshots:

```python
python -c "
import numpy as np
from PIL import Image
import os

path = r'c:\Users\hcfai\Desktop\git\tsw_bot\screenshots'
files = sorted([f for f in os.listdir(path) if f.startswith('service_') and f.endswith('.png')])
imgs = {}
for f in files:
    imgs[f] = np.array(Image.open(os.path.join(path, f)))

for i in range(len(files)):
    for j in range(i+1, len(files)):
        a, b = files[i], files[j]
        if imgs[a].shape == imgs[b].shape:
            diff = np.mean(np.abs(imgs[a].astype(float) - imgs[b].astype(float)))
            if diff < 5:
                print(f'DUPLICATE: {a} vs {b} (diff={diff:.1f})')
print('Done — no output above means no duplicates.')
"
```
