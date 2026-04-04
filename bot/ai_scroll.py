"""AI-assisted service discovery — captures service list pages and sends them
to a local vision LLM (via LM Studio) to identify service count and positions.

Proof of concept:  one train class, one train.

Phase 1 (game running):
    - Navigate to the train class, click the first train to see its services
    - Scroll top to bottom, capturing a screenshot at each page position
    - Exit game

Phase 2 (game off):
    - Send each page screenshot to the vision LLM
    - Ask it to count service boxes and report their details
    - Report the total unique service count

Usage:
    python ai_scroll.py                # capture + analyse
    python ai_scroll.py --analyse-only # skip capture, just re-run LLM on saved images
"""

import json
import os
import sys
import time

import numpy as np
import pyautogui
pyautogui.FAILSAFE = False

import config
from navigator import (
    launch_game,
    pass_warning_screen,
    pass_splash_screen,
    click_to_the_trains,
    click_choose_a_route,
    select_route,
    click_timetable,
    select_extra_section,
    select_train,
    click_train,
    exit_game,
    kill_game,
)

# ── LM Studio settings ──────────────────────────────────────────────
LM_STUDIO_URL = "http://localhost:1234"
VISION_MODEL = "qwen2.5-vl-7b-instruct"

# ── Output directory for captured pages ──────────────────────────────
AI_CAPTURE_DIR = os.path.join(config.BASE_DIR, "ai_captures")


# ═══════════════════════════════════════════════════════════════════════
# Phase 1 — Capture service list pages
# ═══════════════════════════════════════════════════════════════════════

def capture_service_pages():
    """Scroll through the service list and save a screenshot of each page.

    Returns list of saved page image paths.
    """
    region = (
        config.SERVICE_LIST_LEFT,
        config.SERVICE_LIST_TOP,
        config.SERVICE_LIST_RIGHT - config.SERVICE_LIST_LEFT,
        config.SERVICE_LIST_BOTTOM - config.SERVICE_LIST_TOP,
    )
    center_x = (config.SERVICE_LIST_LEFT + config.SERVICE_LIST_RIGHT) // 2
    center_y = (config.SERVICE_LIST_TOP + config.SERVICE_LIST_BOTTOM) // 2

    os.makedirs(AI_CAPTURE_DIR, exist_ok=True)

    # Scroll to the very top first
    print("Scrolling service list to top...")
    scroll_up = -config.SCROLL_PER_BOX * 8
    pyautogui.moveTo(center_x, center_y)
    time.sleep(0.3)
    prev_img = np.array(pyautogui.screenshot(region=region))
    for _ in range(60):
        pyautogui.scroll(scroll_up)
        time.sleep(0.5)
        curr_img = np.array(pyautogui.screenshot(region=region))
        if curr_img.shape == prev_img.shape:
            diff = np.mean(np.abs(curr_img.astype(float) - prev_img.astype(float)))
            if diff < 5.0:
                break
        prev_img = curr_img
    time.sleep(0.5)
    print("At top of list.")

    # Capture pages
    pages = []
    page_num = 0

    while True:
        # Take a screenshot of the service list region
        img = pyautogui.screenshot(region=region)
        img_np = np.array(img)

        # Save the page
        page_path = os.path.join(AI_CAPTURE_DIR, f"page_{page_num:03d}.png")
        img.save(page_path)
        pages.append(page_path)
        print(f"  Captured page {page_num} -> {os.path.basename(page_path)}")

        # Scroll down one page
        pyautogui.moveTo(center_x, center_y)
        time.sleep(0.3)
        pyautogui.scroll(config.SCROLL_AMOUNT)
        time.sleep(2.0)

        # Check if the list stopped moving (end of list)
        new_img = np.array(pyautogui.screenshot(region=region))
        if new_img.shape == img_np.shape:
            diff = np.mean(np.abs(new_img.astype(float) - img_np.astype(float)))
            if diff < 5.0:
                print(f"  End of list detected after page {page_num}.")
                break

        page_num += 1

    print(f"\nCaptured {len(pages)} page(s) in {AI_CAPTURE_DIR}")
    return pages


# ═══════════════════════════════════════════════════════════════════════
# Phase 2 — Send pages to the vision LLM
# ═══════════════════════════════════════════════════════════════════════

def ask_vision_llm(image_path, prompt, max_tokens=2048, retries=3):
    """Send an image + prompt to LM Studio and return the response text.

    Retries on server errors (missing 'choices', timeouts, connection issues).
    """
    import http.client
    import base64
    from urllib.parse import urlparse

    with open(image_path, "rb") as f:
        b64 = base64.b64encode(f.read()).decode("ascii")

    body = json.dumps({
        "model": VISION_MODEL,
        "messages": [{
            "role": "user",
            "content": [
                {"type": "text", "text": prompt},
                {"type": "image_url", "image_url": {
                    "url": f"data:image/png;base64,{b64}"
                }},
            ],
        }],
        "max_tokens": max_tokens,
        "temperature": 0,
    })

    parsed = urlparse(LM_STUDIO_URL)

    for attempt in range(retries):
        try:
            conn = http.client.HTTPConnection(parsed.hostname, parsed.port, timeout=180)
            conn.request("POST", "/v1/chat/completions", body=body,
                         headers={"Content-Type": "application/json"})
            resp = conn.getresponse()
            data = json.loads(resp.read().decode())
            conn.close()

            if "choices" not in data:
                err = data.get("error", data)
                error_msg = err.get("message", str(err)) if isinstance(err, dict) else str(err)
                print(f"  WARNING: Server returned no choices (attempt {attempt + 1}/{retries}): {error_msg}")
                if attempt < retries - 1:
                    time.sleep(5)
                    continue
                raise RuntimeError(f"LM Studio error after {retries} attempts: {error_msg}")

            return data["choices"][0]["message"]["content"]

        except (ConnectionError, OSError, http.client.HTTPException) as e:
            print(f"  WARNING: Connection error (attempt {attempt + 1}/{retries}): {e}")
            if attempt < retries - 1:
                time.sleep(5)
                continue
            raise


def _parse_time_seconds(t):
    """Convert HH:MM:SS or HH:MM to total seconds. Returns None on failure."""
    try:
        parts = t.strip().split(":")
        if len(parts) == 3:
            return int(parts[0]) * 3600 + int(parts[1]) * 60 + int(parts[2])
        elif len(parts) == 2:
            return int(parts[0]) * 3600 + int(parts[1]) * 60
    except (ValueError, AttributeError):
        pass
    return None


def _fuzzy_name_match(a, b):
    """Check if two service names are likely the same despite OCR differences.

    Compares the first 5 non-whitespace characters (handles scrolling text
    that gets cut off at different points) and allows single-character
    substitutions (e.g. 'D' misread as '0').
    """
    a = a.strip().replace(" ", "")
    b = b.strip().replace(" ", "")
    # Use the shorter of the two, minimum 4 chars
    compare_len = min(len(a), len(b), max(4, min(len(a), len(b))))
    if compare_len < 3:
        return a == b
    a_sub = a[:compare_len]
    b_sub = b[:compare_len]
    if a_sub == b_sub:
        return True
    # Allow 1 character difference (OCR substitution)
    diffs = sum(1 for ca, cb in zip(a_sub, b_sub) if ca != cb)
    return diffs <= 1


def _is_fuzzy_duplicate(svc, existing_list):
    """Check if a service is a fuzzy duplicate of any service already seen.

    Returns True if there's a match on start_time (within 60 seconds)
    AND a fuzzy name match.
    """
    s_time = _parse_time_seconds(svc.get("start_time", ""))
    s_name = svc.get("name", "")
    if s_time is None:
        return False

    for existing in existing_list:
        e_time = _parse_time_seconds(existing.get("start_time", ""))
        if e_time is None:
            continue
        # Start times within 60 seconds of each other
        if abs(s_time - e_time) <= 60:
            if _fuzzy_name_match(s_name, existing.get("name", "")):
                return True
    return False


def _crop_overlap(image_path, overlap_px):
    """Crop the top overlap_px pixels from an image. Returns (path, full_height)
    or (None, full_height) if the image is too small to crop."""
    from PIL import Image
    img = Image.open(image_path)
    if img.height <= overlap_px:
        return None, img.height
    cropped = img.crop((0, overlap_px, img.width, img.height))
    crop_path = image_path.replace(".png", "_cropped.png")
    cropped.save(crop_path)
    return crop_path, img.height


def analyse_pages(page_paths, no_crop=False):
    """Send each captured page to the LLM and count services.

    For pages after the first, the top overlap region (2 boxes) is cropped
    off before sending so the LLM never sees duplicate services.
    If no_crop=True, send full uncropped pages and rely on dedup only.
    """
    print("\n" + "=" * 60)
    print("Phase 2: Analysing pages with vision LLM")
    if no_crop:
        print("  (--no-crop: sending full uncropped pages)")
    print("=" * 60)

    # Overlap in pixels to crop from pages 1+
    overlap_px = config.OVERLAP_BOXES * config.SERVICE_BOX_STRIDE

    prompt = (
        "This is a screenshot of a service list from a train simulation game. "
        "Each service is shown as a rectangular box/card stacked vertically. "
        "\n\n"
        "ACCURACY IS CRITICAL. Take your time and read carefully.\n"
        "\n"
        "Each service box contains:\n"
        "- A service name (text string, may include route codes like '207-04')\n"
        "- A start time (HH:MM:SS format, e.g. 05:31:00)\n"
        "- A duration (HH:MM:SS format, e.g. 00:28:00)\n"
        "\n"
        "IMPORTANT RULES:\n"
        "- Service names CAN repeat — two boxes with the same name but different "
        "times are SEPARATE services. Count each box.\n"
        "- Some service names scroll horizontally and may be cut off. "
        "Report whatever text is visible, even if truncated.\n"
        "- Read EVERY character carefully. Distinguish between similar characters: "
        "0 vs O vs D, 1 vs I vs l, 5 vs S. Get the exact digits right.\n"
        "- The start time and duration are two SEPARATE time values. "
        "Do not combine them.\n"
        "- Count ONLY fully or mostly visible boxes. Do not count boxes "
        "that are barely peeking in from the edge.\n"
        "\n"
        "For each service box, report:\n"
        "1. y_percent: vertical center position as percentage "
        "(0% = top, 100% = bottom)\n"
        "2. name: the service name text exactly as shown\n"
        "3. start_time: in HH:MM:SS format\n"
        "4. duration: in HH:MM:SS format\n"
        "\n"
        "Respond ONLY with valid JSON:\n"
        '{"count": <number>, "services": ['
        '{"index": 1, "y_percent": <number>, "name": "<text>", '
        '"start_time": "<HH:MM:SS>", "duration": "<HH:MM:SS>"}]}\n'
        "No other text."
    )

    all_results = []
    total_boxes = 0
    list_height = config.SERVICE_LIST_BOTTOM - config.SERVICE_LIST_TOP
    click_x = (config.SERVICE_LIST_LEFT + config.SERVICE_LIST_RIGHT) // 2

    for i, page_path in enumerate(page_paths):
        # For pages after the first, crop off overlap region (unless --no-crop)
        if i > 0 and not no_crop:
            send_path, full_height = _crop_overlap(page_path, overlap_px)
            if send_path is None:
                print(f"\n  Skipping page {i} — image too small to crop "
                      f"({full_height}px < {overlap_px}px overlap)")
                continue
            cropped_height = full_height - overlap_px
            print(f"\n  Sending page {i} to LLM (cropped, top {overlap_px}px removed)...")
        else:
            send_path = page_path
            full_height = list_height
            cropped_height = full_height
            print(f"\n  Sending page {i} to LLM{' (full, no crop)' if i > 0 else ''}...")

        start = time.time()
        response = ask_vision_llm(send_path, prompt)
        elapsed = time.time() - start
        print(f"  Response in {elapsed:.1f}s:")
        print(f"  {response}")

        # Parse JSON from the response
        try:
            cleaned = response.strip()
            if cleaned.startswith("```"):
                cleaned = cleaned.split("\n", 1)[1]
                cleaned = cleaned.rsplit("```", 1)[0]
            result = json.loads(cleaned)
            count = result.get("count", 0)
            total_boxes += count

            # Compute click coordinates
            # For cropped pages, y_percent is relative to the cropped image,
            # so we need to map it back to the full page coordinates
            services = result.get("services", [])
            for svc in services:
                y_pct = svc.get("y_percent", 50)
                if i > 0:
                    # Map from cropped image coords back to full page coords
                    y_in_cropped = (y_pct / 100.0) * cropped_height
                    y_in_full = y_in_cropped + overlap_px
                    svc["click_y"] = int(config.SERVICE_LIST_TOP + y_in_full)
                else:
                    svc["click_y"] = int(
                        config.SERVICE_LIST_TOP + (y_pct / 100.0) * list_height)
                svc["click_x"] = click_x
                svc["page"] = i

            all_results.append({
                "page": i,
                "count": count,
                "services": services,
            })
        except (json.JSONDecodeError, ValueError):
            print(f"  WARNING: Could not parse JSON from LLM response")
            all_results.append({
                "page": i,
                "count": "?",
                "raw_text": response,
            })

    # ── Build unique service list with fuzzy dedup ──
    unique_services = []
    global_index = 0
    exact_keys = set()
    dupes_removed = 0

    for r in all_results:
        for svc in r.get("services", []):
            name = svc.get("name", "?")
            start = svc.get("start_time", "?")
            duration = svc.get("duration", "?")

            # Pass 1: exact match dedup
            key = (name, start, duration)
            if key in exact_keys:
                dupes_removed += 1
                continue

            # Pass 2: fuzzy match dedup (OCR errors, scrolling text)
            if _is_fuzzy_duplicate(svc, unique_services):
                dupes_removed += 1
                continue

            exact_keys.add(key)
            global_index += 1
            svc["global_index"] = global_index
            unique_services.append(svc)

    # ── Print per-page breakdown ──
    print("\n" + "=" * 70)
    print("SERVICES BY PAGE")
    print("=" * 70)

    for r in all_results:
        page = r["page"]
        count = r["count"]
        print(f"\n  Page {page}: {count} service(s)")
        print(f"  {'─' * 64}")
        for svc in r.get("services", []):
            name = svc.get("name", "?")
            start_t = svc.get("start_time", "?")
            dur = svc.get("duration", "?")
            cx = svc.get("click_x", "?")
            cy = svc.get("click_y", "?")
            print(f"    [{svc.get('index', '?')}] {name}")
            print(f"        start: {start_t}  duration: {dur}  "
                  f"click: ({cx}, {cy})")

    # ── Print unique master list ──
    print("\n" + "=" * 70)
    print(f"UNIQUE SERVICES: {len(unique_services)} total")
    print(f"  (removed {dupes_removed} duplicates from {total_boxes} raw boxes)")
    print("=" * 70)

    for svc in unique_services:
        gi = svc["global_index"]
        name = svc.get("name", "?")
        start_t = svc.get("start_time", "?")
        dur = svc.get("duration", "?")
        page = svc.get("page", "?")
        cx = svc.get("click_x", "?")
        cy = svc.get("click_y", "?")
        print(f"  #{gi:3d}  page {page}  click ({cx}, {cy})  "
              f"{name}  |  {start_t}  |  {dur}")

    print("\n" + "=" * 70)
    print(f"  Pages captured:      {len(page_paths)}")
    print(f"  Total boxes seen:    {total_boxes}")
    print(f"  Duplicates removed:  {dupes_removed}")
    print(f"  Unique services:     {len(unique_services)}")
    print("=" * 70)

    # ── Save results ──
    results_path = os.path.join(AI_CAPTURE_DIR, "analysis_results.json")
    with open(results_path, "w") as f:
        json.dump({
            "pages": len(page_paths),
            "total_boxes": total_boxes,
            "duplicates_removed": dupes_removed,
            "unique_count": len(unique_services),
            "per_page": all_results,
            "unique_services": [
                {
                    "global_index": s["global_index"],
                    "page": s["page"],
                    "click_x": s["click_x"],
                    "click_y": s["click_y"],
                    "name": s.get("name", "?"),
                    "start_time": s.get("start_time", "?"),
                    "duration": s.get("duration", "?"),
                }
                for s in unique_services
            ],
        }, f, indent=2)
    print(f"\n  Results saved to {results_path}")

    return len(unique_services)


# ═══════════════════════════════════════════════════════════════════════
# Main
# ═══════════════════════════════════════════════════════════════════════

def main():
    analyse_only = "--analyse-only" in sys.argv
    no_crop = "--no-crop" in sys.argv

    print("=== TSW AI Service Discovery (Proof of Concept) ===\n")

    if analyse_only:
        # Re-run analysis on existing captures
        print("Mode: analyse-only (skipping game capture)\n")
        existing = sorted(
            f for f in os.listdir(AI_CAPTURE_DIR)
            if f.startswith("page_") and f.endswith(".png")
            and "_cropped" not in f
        ) if os.path.isdir(AI_CAPTURE_DIR) else []
        if not existing:
            print(f"ERROR: No page images found in {AI_CAPTURE_DIR}")
            sys.exit(1)
        page_paths = [os.path.join(AI_CAPTURE_DIR, f) for f in existing]
        print(f"Found {len(page_paths)} existing page(s).\n")
        analyse_pages(page_paths, no_crop=no_crop)
        return

    # ── Phase 1: Capture ──
    print("Phase 1: Capture service list pages")
    print("-" * 40)

    try:
        print("Launching game and navigating to service list...")
        launch_game()
        pass_warning_screen()
        pass_splash_screen()
        click_to_the_trains()
        click_choose_a_route()
        select_route()
        click_timetable()

        # Handle extra section from database
        from uploader import get_sections
        db_sections = get_sections()
        if db_sections:
            select_extra_section(db_sections[0])

        # Select the first train class from the database
        from uploader import get_trains
        all_trains = get_trains()
        first_train_class = all_trains[0]
        print(f"\nSelecting train class: {first_train_class}")
        select_train(first_train_class)

        # Click the first train (0-based index) to populate the service list
        print("Clicking first train...")
        click_train(0)
        time.sleep(3)

        print("\nCapturing service list pages...")
        page_paths = capture_service_pages()

        print("\nExiting game...")
        try:
            exit_game()
        except TimeoutError:
            kill_game()

    except KeyboardInterrupt:
        print("\nCapture interrupted by user.")
        kill_game()
        sys.exit(0)
    except Exception as e:
        print(f"\nERROR during capture: {e}")
        import traceback
        traceback.print_exc()
        kill_game()
        sys.exit(1)

    # ── Phase 2: Analyse ──
    print("\nGame closed. Starting LLM analysis...\n")
    time.sleep(2)  # brief pause for GPU to free up
    analyse_pages(page_paths, no_crop=no_crop)


if __name__ == "__main__":
    main()
