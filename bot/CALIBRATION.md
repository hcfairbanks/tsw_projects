# TSW Bot Screen Calibration Guide

## Overview
All screen coordinates live in `bot/config.py`. The calibration override system loads from `bot/calibration.json` (created by `bot/calibrate.py`). When coordinates change, either edit config.py defaults or run calibrate.py to generate calibration.json.

## Monitor Setup
- Determine monitor count and resolution: `pyautogui.screenshot().size` for total desktop
- Game runs on one monitor -- find which by capturing each monitor region and checking for game content
- Example: 3 monitors at 1920x1080 = total 5760x1080, game on middle = `GAME_REGION = (1920, 0, 1920, 1080)`
- All absolute coordinates are in desktop space (e.g. x=2072 means 2072-1920=152px from left edge of game screen)

## Calibration Workflow (Claude-assisted)

For each game screen, follow this pattern:
1. User navigates game to the required screen
2. Capture: `pyautogui.screenshot(region=(GAME_X_OFFSET, 0, 1920, 1080))` -> save to `screenshots/calibration_*.png`
3. Analyze the image to find UI element positions (use PIL crops to isolate regions)
4. Update `bot/config.py` with new coordinates
5. Test with `pyautogui.locateOnScreen()` for reference images
6. Run the bot on 1-2 services and check all output images visually before a full run

### Step 1: Reference Images
Reference images are checked FIRST since they gate navigation. Test each with:
```python
pyautogui.locateOnScreen('references/<name>.png', confidence=0.6)
```
If NOT FOUND, recapture from the current game screenshot by cropping the relevant region.

**Key gotchas:**
- `warning_continue.png`: Crop the two static text paragraphs ONLY. Avoid the "PRESS ANY KEY TO CONTINUE" text -- it flashes on/off and will cause intermittent match failures.
- `splash_continue.png`: The old reference (just the "6" logo) is a good choice -- small, distinctive, static.
- `choose_a_route.png`: The tile background rotates between sessions. Crop tightly around the text "CHOOSE A ROUTE / PICK A ROUTE". Works at confidence 0.8 even with background variation.
- `exit_to_main_menu.png`: The pause menu background changes between game updates. Recapture the "MAIN MENU" tile including icon and subtitle text. The tile is in the top-left of a 3x2 grid on the pause menu.
- `to_the_trains.png`: Tile text reference.
- `service_name_search.png`: Used by `service_loop.py` (non-scroll method / spreadsheet method) for the service name search field. Must be recaptured if the game UI changes. Navigate to the service list screen where the search field is visible.

### Step 2: Service List Area (most critical for highlight detection)

**CRITICAL**: The highlight detector (`wait_for_single_highlight()` in `service_loop_scroll_method.py`) scans only the **leftmost 5 pixels** of the `SERVICE_LIST` region for the blue border color `#57a6d0`. If `SERVICE_LIST_LEFT` is even ~13px too far left, the first 5 pixels will be dark background instead of border, and detection returns 0 boxes.

**How to calibrate:**
1. Screenshot the service list screen with a service highlighted (blue border visible)
2. Find the exact x where the blue border starts:
```python
target = np.array([87, 166, 208])  # #57a6d0
for x in range(130, 180):
    px = arr[y_of_highlighted_box, x, :3].astype(int)
    is_blue = all(abs(px[i] - target[i]) <= 20 for i in range(3))
    if is_blue: print(f'Border starts at x={x}'); break
```
3. Set `SERVICE_LIST_LEFT = border_start_x + GAME_X_OFFSET` (so the first 5 pixels hit the border)
4. Measure box heights by finding dark-background runs at a fixed x inside the box
5. Stride = distance between consecutive box tops (skip the highlighted box -- its border makes it taller)
6. Blue border color: #57a6d0 with tolerance 20

### Step 3: Schedule Area
- Config keys: `SCHEDULE_LEFT/TOP/RIGHT/BOTTOM`
- Game screen: in-game, ESC > Schedule visible
- `SCHEDULE_TOP` must be BELOW the tab bar ("OPTIONS | HUD GUIDE | CONTROL GUIDE | ...") and the service name line. If too high, these get included in the capture.
- `SCHEDULE_RIGHT` should exclude the completion icon column (checkmark/AP icons on the far right). The WCML reference schedule was ~1470px wide.
- `SCHEDULE_BOTTOM` must be low enough to capture all rows on screen. If the last row is cut off, increase this. `crop_schedule()` trims empty space below.
- Schedule has green top border (#059744) and light bottom border (#c5e4e9)

### Step 4: Train Selection Box
- Config keys: `TRAIN_BOX_LEFT/TOP/WIDTH/HEIGHT`, `TRAIN_VISIBLE_COUNT`, `TRAIN_BOX_STRIDE`, `TRAIN_FIRST_Y_OFFSET`
- Game screen: route timetable page with train list on the right
- Measure top-left and bottom-right of the train list area, count visible trains

### Step 5: Level Splash Screen (Get Started)
- Config keys: `LEVEL_SCREEN_LEFT/TOP/RIGHT/BOTTOM` -> derives `LEVEL_SCREEN_REGION`
- Config keys: `LEVEL_CROP_TONNAGE`, `LEVEL_CROP_CAR_COUNT`, `LEVEL_CROP_CAR_LENGTH`, `LEVEL_CROP_SERVICE_NAME`
- Game screen: "Get Started" screen after clicking a service
- The level splash panel is centered -- light gray/white bordered panel containing service name, time, description, train info bar (tonnage/cars/length), stations, GET STARTED button

**How to calibrate the splash panel (LEVEL_SCREEN):**
1. Capture full game screen while on "Get Started" splash
2. Find the white panel border by scanning for light pixels (RGB > 200):
```python
# Scan for panel edges
for y in range(height):
    px = img.getpixel((width//2, y))
    if px[0] > 200: print(f'Panel top: y={y}'); break
```
3. Set LEVEL_SCREEN to tightly fit the white panel border -- no background showing

**How to calibrate the crop regions (LEVEL_CROP_*):**
1. Use the `level_screen.png` image to find positions (origin = LEVEL_SCREEN_LEFT, LEVEL_SCREEN_TOP)
2. The info bar is a teal strip (#bad8dd background) with icons and values
3. **Tonnage**: Just the number text. The lock icon is immediately left -- find the teal gap between icon and number by scanning pixel-by-pixel:
```python
# Scan row at info bar y, looking for teal gap then number start
for x in range(icon_area, number_area):
    r, g, b = img.getpixel((x, bar_y))
    is_teal = (170 < r < 200) and (200 < g < 230) and (205 < b < 235)
```
   Start crop 2-3px before the number starts. Values vary from 3 chars ("412") to 5 chars ("392.9") -- make width wide enough for the longest.
4. **Car count**: Just the number after the #/car icon. Narrowest crop -- usually 1-2 digits.
5. **Car length**: Just the number after the train icon. Similar to tonnage approach.
6. **Service name**: The full header text. Crop within the white/light panel area. Must be wide enough for long names like "LA Union - Metrolink CMF (Non-Revenue)". Exclude any dark material at edges. Compare against reference: WCML was 792x74.
7. All crop coords are absolute (image_x + LEVEL_SCREEN_LEFT, image_y + LEVEL_SCREEN_TOP, width, height)

**Iterative calibration tips:**
- Run the bot on 2-3 services with different name lengths and tonnage values
- Check each crop image -- if text is cut off on any edge, adjust that side
- The info bar position is consistent within a route but may vary slightly between routes
- `_crop_level_info()` in service_loop_scroll_method.py auto-trims dark areas below the teal bar

### Step 6: 1_service.png (Service Tile)
- Auto-cropped to the blue #57a6d0 border by `_grab_and_crop_service_box()`
- `SERVICE_BOX_RIGHT_TRIM` in config.py trims pixels from the right edge to exclude the clock/timer icon
- Set to ~75px to remove the icon while keeping the duration text

### Step 7: Extra Section Tile
- Config keys: `EXTRA_SECTION_TILE_X/Y`
- Only for routes with extra selection pages (e.g. timetable variants)
- Measure: center of the first result tile

## Schedule Stitching
- Uses **normalized cross-correlation (NCC)** in `schedule_capture.py` to detect overlap between scroll frames
- NCC is brightness-invariant -- handles row color changes when the game updates completion state between captures
- Only uses the left 60% of the image for matching (avoids time/status columns that change between captures)
- Cut point is snapped to the nearest dark separator line in the **middle** of the overlap zone
- The stitch logic tracks per-frame trimming to handle accumulated results correctly
- Threshold: NCC > 0.7 (tested at 0.83+ on real frames)
- `crop_schedule()` trims below the last detected row and caps width at SCHEDULE_MAX_WIDTH

**Previous issue (now fixed):** The old pixel-diff method (threshold 5.0) failed when row colors changed between captures. Rows that were "active" (green) in one frame became "completed" in the next, causing diff > 20. The old stitch logic also incorrectly used the accumulated `result` array for cutting instead of per-frame math, causing cuts at out-of-bounds positions.

## Files Involved
- `bot/config.py` -- default coordinates, calibration override loading
- `bot/calibration.json` -- override file (auto-loaded by config.py)
- `bot/calibrate.py` -- interactive calibration tool (mouse hover + Enter)
- `bot/capture_reference.py` -- tool to recapture reference images from full screenshot
- `bot/references/*.png` -- template images for screen matching
- `bot/service_loop_scroll_method.py` -- highlight detection, service tile cropping, level info cropping
- `bot/service_loop.py` -- alternate service loop (same crop logic)
- `bot/schedule_capture.py` -- schedule capture, NCC stitching, cropping

## Page Y Drift Correction

### The Problem
When the bot scrolls through the service list, each page scroll introduces a small pixel drift. The computed click_y coordinates assume pixel-perfect scrolling, but the game's scroll is not exact. This drift accumulates over pages, causing the bot to click slightly too high on later pages. On the last box of a page, this can cause a wrong-service click (selecting the neighboring service instead).

### How Drift Manifests
- Pages 0-5: Usually no visible drift (error < 1 box height)
- Pages 6+: Drift becomes large enough to miss the target box
- The last service on each page (box index 5, the 6th box) is most affected since it's farthest from the scroll anchor
- Wrong clicks are identifiable because the captured service name/route differs from the CSV but the time often matches a neighboring service

### How to Evaluate Drift

**Automated: Run the drift evaluation script**

```
python bot/evaluate_drift.py                          # scan all routes/trains
python bot/evaluate_drift.py "Boston Sprinter"        # scan one route
python bot/evaluate_drift.py "Boston Sprinter" "CTC-3 MBTA"  # scan one train
```

The script compares `llm_data.json` in each service folder against `services.csv`, reports mismatches grouped by page, and suggests `PAGE_Y_DRIFT` values. Run this after a full bot capture to identify drift issues.

**Manual evaluation (if needed):**

**Step 1: Run the bot on a train with many services (10+ pages)**

```
python bot/new_method.py "TRAIN_NAME" 1 1
```

**Step 2: Compare captured data to expected CSV**

For each service folder under `bot/screenshots/<Route>/<Timetable>/<Train>/train_01/`, compare the captured service name (from `llm_data.json`) against the expected service in `services.csv`.

To do this comparison:
1. Read `services.csv` -- it has columns including service number, route name, start_time, duration, page, and click coordinates
2. For each `service_NNN` folder, read `llm_data.json` and check the `service_name` or `name` field
3. Flag any service where the captured name doesn't match the CSV expected name
4. Note which page the mismatched service was on (from the CSV `page` column)

**Step 3: Identify the pattern**

Look for:
- Which pages have mismatches?
- Are mismatches always the last box on the page (highest box_index)?
- Is the captured service always the *next* service in the list (one row below)?

If yes, this confirms upward drift -- the click is landing too high.

**Step 4: Add drift corrections to config.py**

Edit `PAGE_Y_DRIFT` in `bot/config.py`:

```python
PAGE_Y_DRIFT = {
    6: 3,   # pixels to shift click down on page 6
    7: 3,
    8: 4,
    9: 4,
    10: 5,
}
```

Positive values shift the click **down** (fixing too-high clicks). The drift typically increases ~0.5px per page.

**Step 5: Re-run only the affected services**

```
python bot/new_method.py "TRAIN_NAME" 1 <service_number>
```

Check that the correct service is now captured.

**Step 6: Verify across both train instances**

If the train has 2 instances (train_01 and train_02), the same drift values should fix both since the scroll behavior is deterministic.

### Current Drift Values (Boston Sprinter, CTC-3 MBTA)
```python
PAGE_Y_DRIFT = {
    6: 3,
    7: 3,
    8: 4,
    9: 4,
    10: 5,
}
```

### Notes
- Drift values may change if `SCROLL_PER_BOX`, `BOXES_TO_SCROLL`, or monitor resolution changes
- Different routes/games may have different drift characteristics -- recalibrate per setup
- The drift correction is applied in `navigate_to_service()` in `new_method.py`
- Short trains (< 40 services, < 6 pages) typically don't need drift correction
- `FIRST_BOX_TOP` in config.py provides a global Y offset for all pages -- use it for constant offset, use `PAGE_Y_DRIFT` for per-page correction

## Common Issues
- **Warning screen not found**: Reference image doesn't match current game UI. Recapture (exclude flashing text).
- **Exit to Main Menu not found**: Pause menu background changed. Recapture the "MAIN MENU" tile from the pause screen.
- **0 bordered boxes found**: `SERVICE_LIST_LEFT` is too far left -- the 5-pixel left-edge scan misses the border. Fix by setting LEFT to where the blue border actually starts.
- **Crops capturing icons instead of numbers**: Tonnage/car_count/car_length crop x is too far left. Scan pixel-by-pixel to find the gap between icon and number.
- **Crop text cut off at top**: Move crop y up (decrease value) and increase height to keep same bottom.
- **Schedule includes tab bar**: `SCHEDULE_TOP` is too low (small number = higher on screen). Increase it to skip past the "OPTIONS | HUD GUIDE..." bar.
- **Schedule bottom cut off**: Increase `SCHEDULE_BOTTOM`. The `crop_schedule()` function trims empty space.
- **Schedule right side has checkmark icons**: Decrease `SCHEDULE_RIGHT` to exclude the completion column.
- **Schedule double bars / duplicates**: Fixed by NCC-based overlap detection replacing old pixel-diff method.
- **1_service.png has clock icon on right**: Set `SERVICE_BOX_RIGHT_TRIM` in config (default 75px).
- **1b_service_name.png has dark edges**: Adjust `LEVEL_CROP_SERVICE_NAME` x and width to stay within the light panel.
- **Tile reference intermittent failures**: Background images on tiles rotate. Crop tightly around text only.
- **Wrong service captured on later pages**: Scroll drift causing click to land on neighboring service. See "Page Y Drift Correction" section above. Run the drift evaluation and add corrections to `PAGE_Y_DRIFT` in config.py.
