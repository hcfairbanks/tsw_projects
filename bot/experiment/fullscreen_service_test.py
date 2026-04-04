"""Experiment: Full-screen service list capture + Claude analysis + interactive navigation.

Expects to be on the service list screen already (no game launch).

Usage:
    python experiment/fullscreen_service_test.py
"""

import base64
import json
import os
import re
import sys
import time

import anthropic
import numpy as np
import pyautogui
pyautogui.FAILSAFE = False
from PIL import Image

# Add parent dir so we can import config
sys.path.insert(0, os.path.dirname(os.path.dirname(os.path.abspath(__file__))))
import config

# ── Settings ──────────────────────────────────────────────────────────
CLAUDE_MODEL = "claude-sonnet-4-6"
LM_STUDIO_URL = "http://localhost:1234"
VISION_MODEL = "qwen2.5-vl-7b-instruct"

# Game is on the middle monitor (1920x1080, starting at x=1920)
GAME_REGION = (1920, 0, 1920, 1080)

OUTPUT_DIR = os.path.join(os.path.dirname(os.path.abspath(__file__)), "output")
os.makedirs(OUTPUT_DIR, exist_ok=True)


# ── Claude API ────────────────────────────────────────────────────────

def ask_claude(client, image_path, prompt, max_retries=5):
    """Send an image + prompt to Claude API. Returns (response_text, usage)."""
    with open(image_path, "rb") as f:
        image_data = base64.standard_b64encode(f.read()).decode("utf-8")

    for attempt in range(max_retries):
        try:
            message = client.messages.create(
                model=CLAUDE_MODEL,
                max_tokens=2048,
                messages=[{
                    "role": "user",
                    "content": [
                        {
                            "type": "image",
                            "source": {
                                "type": "base64",
                                "media_type": "image/png",
                                "data": image_data,
                            },
                        },
                        {"type": "text", "text": prompt},
                    ],
                }],
            )
            return message.content[0].text, message.usage
        except anthropic.RateLimitError:
            wait = min(2 ** attempt * 5, 60)
            print(f"  Rate limited (attempt {attempt + 1}/{max_retries}), waiting {wait}s...")
            time.sleep(wait)
            if attempt == max_retries - 1:
                raise
        except anthropic.APIStatusError as e:
            if e.status_code >= 500:
                wait = min(2 ** attempt * 3, 30)
                print(f"  Server error {e.status_code} (attempt {attempt + 1}), waiting {wait}s...")
                time.sleep(wait)
                if attempt == max_retries - 1:
                    raise
            else:
                raise


# ── Local LLM ─────────────────────────────────────────────────────────

def _encode_pil_image(pil_img):
    import io
    buf = io.BytesIO()
    pil_img.save(buf, format="PNG")
    return base64.standard_b64encode(buf.getvalue()).decode("utf-8")


def ask_local_llm(image_base64, prompt, max_tokens=1024):
    import urllib.request
    url = f"{LM_STUDIO_URL}/v1/chat/completions"
    body = {
        "model": VISION_MODEL,
        "messages": [{
            "role": "user",
            "content": [
                {"type": "image_url", "image_url": {"url": f"data:image/png;base64,{image_base64}"}},
                {"type": "text", "text": prompt},
            ],
        }],
        "max_tokens": max_tokens,
        "temperature": 0.1,
    }
    data = json.dumps(body).encode()
    req = urllib.request.Request(url, data=data, method="POST",
                                headers={"Content-Type": "application/json"})
    with urllib.request.urlopen(req, timeout=120) as resp:
        result = json.loads(resp.read().decode())
    return result["choices"][0]["message"]["content"]


# ── Prompt ────────────────────────────────────────────────────────────

SERVICE_LIST_PROMPT = (
    f"This is a {GAME_REGION[2]}x{GAME_REGION[3]} pixel screenshot of a train simulation game. "
    "The service list is on the right side of the screen. "
    "Each service is shown as a rectangular box/card stacked vertically.\n\n"
    "ACCURACY IS CRITICAL. Take your time and read carefully.\n\n"
    "Each service box contains:\n"
    "- A service name (text string, may include route codes like '207-04')\n"
    "- A start time (HH:MM:SS format, e.g. 05:31:00)\n"
    "- A duration (HH:MM:SS format, e.g. 00:28:00)\n\n"
    "IMPORTANT RULES:\n"
    "- Service names CAN repeat — two boxes with the same name but different "
    "times are SEPARATE services. Count each box.\n"
    "- Read EVERY character carefully. Distinguish between similar characters: "
    "0 vs O vs D, 1 vs I vs l, 5 vs S.\n"
    "- The start time and duration are two SEPARATE time values.\n"
    "- Count ONLY fully or mostly visible boxes.\n\n"
    "For each service box, report the x,y pixel coordinates of the CENTER "
    "of the box within this image.\n\n"
    "Respond ONLY with valid JSON:\n"
    '{"count": <number>, "services": ['
    '{"index": 1, "x": <pixel>, "y": <pixel>, "name": "<text>", '
    '"start_time": "<HH:MM:SS>", "duration": "<HH:MM:SS>"}]}\n'
    "No other text."
)


# ── Helpers ───────────────────────────────────────────────────────────

def _parse_json_response(text):
    cleaned = text.strip()
    if cleaned.startswith("```"):
        lines = cleaned.split("\n")
        lines = lines[1:]  # skip ```json
        for i in range(len(lines) - 1, -1, -1):
            if lines[i].strip().startswith("```"):
                lines = lines[:i]
                break
        cleaned = "\n".join(lines)
    try:
        return json.loads(cleaned)
    except json.JSONDecodeError:
        match = re.search(r'\{.*\}', cleaned, re.DOTALL)
        if match:
            try:
                return json.loads(match.group())
            except json.JSONDecodeError:
                pass
    print(f"  WARNING: Could not parse JSON from response:\n{text[:200]}")
    return {"count": 0, "services": []}


def _normalize_time(t):
    parts = t.strip().split(":")
    if len(parts) == 2:
        return f"{parts[0].zfill(2)}:{parts[1].zfill(2)}:00"
    elif len(parts) == 3:
        return f"{parts[0].zfill(2)}:{parts[1].zfill(2)}:{parts[2].zfill(2)}"
    return t


def _parse_time_seconds(t):
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
    a = a.strip().replace(" ", "")
    b = b.strip().replace(" ", "")
    compare_len = min(len(a), len(b), max(4, min(len(a), len(b))))
    if compare_len < 3:
        return a == b
    a_sub = a[:compare_len]
    b_sub = b[:compare_len]
    if a_sub == b_sub:
        return True
    diffs = sum(1 for ca, cb in zip(a_sub, b_sub) if ca != cb)
    return diffs <= 1


def _is_fuzzy_duplicate(svc, existing_list):
    s_time = _parse_time_seconds(svc.get("start_time", ""))
    s_name = svc.get("name", "")
    if s_time is None:
        return False
    for existing in existing_list:
        e_time = _parse_time_seconds(existing.get("start_time", ""))
        if e_time is None:
            continue
        if abs(s_time - e_time) <= 60:
            if _fuzzy_name_match(s_name, existing.get("name", "")):
                return True
    return False


# ── Scrolling ─────────────────────────────────────────────────────────

def scroll_to_top():
    """Scroll the service list all the way to the top."""
    cx = (config.SERVICE_LIST_LEFT + config.SERVICE_LIST_RIGHT) // 2
    cy = (config.SERVICE_LIST_TOP + config.SERVICE_LIST_BOTTOM) // 2
    region = (config.SERVICE_LIST_LEFT, config.SERVICE_LIST_TOP,
              config.SERVICE_LIST_RIGHT - config.SERVICE_LIST_LEFT,
              config.SERVICE_LIST_BOTTOM - config.SERVICE_LIST_TOP)
    scroll_up = -config.SCROLL_PER_BOX * 8

    pyautogui.moveTo(cx, cy)
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
    print("At top of service list.")


def scroll_to_page(target_page):
    if target_page == 0:
        return
    cx = (config.SERVICE_LIST_LEFT + config.SERVICE_LIST_RIGHT) // 2
    cy = (config.SERVICE_LIST_TOP + config.SERVICE_LIST_BOTTOM) // 2
    pyautogui.moveTo(cx, cy)
    time.sleep(0.3)
    for p in range(target_page):
        pyautogui.scroll(config.SCROLL_AMOUNT)
        time.sleep(1.5)
    time.sleep(0.5)


# ── Phase 1: Capture full-screen pages ────────────────────────────────

def capture_fullscreen_pages():
    """Scroll through the service list taking full-screen screenshots."""
    print("\n=== Phase 1: Capturing full-screen pages ===\n")

    scroll_to_top()

    cx = (config.SERVICE_LIST_LEFT + config.SERVICE_LIST_RIGHT) // 2
    cy = (config.SERVICE_LIST_TOP + config.SERVICE_LIST_BOTTOM) // 2

    # Use service list region just for scroll-end detection
    detect_region = (config.SERVICE_LIST_LEFT, config.SERVICE_LIST_TOP,
                     config.SERVICE_LIST_RIGHT - config.SERVICE_LIST_LEFT,
                     config.SERVICE_LIST_BOTTOM - config.SERVICE_LIST_TOP)

    pages = []
    page_num = 0

    while True:
        # Capture the game screen
        img = pyautogui.screenshot(region=GAME_REGION)
        page_path = os.path.join(OUTPUT_DIR, f"page_{page_num:03d}.png")
        img.save(page_path)
        pages.append(page_path)
        print(f"  Captured page {page_num} ({GAME_REGION[2]}x{GAME_REGION[3]})")

        # Also grab just the service list region for scroll detection
        detect_img = np.array(pyautogui.screenshot(region=detect_region))

        # Scroll down
        pyautogui.moveTo(cx, cy)
        time.sleep(0.3)
        pyautogui.scroll(config.SCROLL_AMOUNT)
        time.sleep(2.0)

        # Check if list stopped moving
        new_detect = np.array(pyautogui.screenshot(region=detect_region))
        if new_detect.shape == detect_img.shape:
            diff = np.mean(np.abs(new_detect.astype(float) - detect_img.astype(float)))
            if diff < 5.0:
                print(f"  End of list after page {page_num}.")
                break

        page_num += 1

    # Scroll back to top
    scroll_to_top()
    print(f"\nCaptured {len(pages)} full-screen page(s)\n")
    return pages


# ── Phase 2: Claude analysis ──────────────────────────────────────────

def analyse_pages(page_paths):
    """Send pages to Claude API and build unique service list."""
    print("\n=== Phase 2: Claude API analysis ===\n")

    if not os.environ.get("ANTHROPIC_API_KEY"):
        print("ERROR: ANTHROPIC_API_KEY environment variable not set.")
        sys.exit(1)

    client = anthropic.Anthropic()
    all_results = []
    total_input_tokens = 0
    total_output_tokens = 0

    for i, page_path in enumerate(page_paths):
        start = time.time()
        response_text, usage = ask_claude(client, page_path, SERVICE_LIST_PROMPT)
        elapsed = time.time() - start
        total_input_tokens += usage.input_tokens
        total_output_tokens += usage.output_tokens

        result = _parse_json_response(response_text)
        count = result.get("count", 0)

        services = result.get("services", [])
        # Compute click coords from box index using known geometry
        # instead of trusting Claude's pixel values (which are inaccurate)
        click_x = (config.SERVICE_LIST_LEFT + config.SERVICE_LIST_RIGHT) // 2
        first_box_center_y = (config.SERVICE_LIST_TOP + config.FIRST_BOX_TOP
                              + config.SERVICE_BOX_HEIGHT // 2)
        for box_idx, svc in enumerate(services):
            svc["page"] = i
            # Keep Claude's raw coords for reference
            svc["claude_x"] = svc.pop("x", None)
            svc["claude_y"] = svc.pop("y", None)
            # Compute actual screen click coords from box position
            svc["x"] = click_x
            svc["y"] = first_box_center_y + (box_idx * config.SERVICE_BOX_STRIDE)
            # Normalize times
            if "start_time" in svc:
                svc["start_time"] = _normalize_time(svc["start_time"])
            if "duration" in svc:
                svc["duration"] = _normalize_time(svc["duration"])

        all_results.append({"page": i, "count": count, "services": services})
        print(f"  Page {i}: {count} services ({elapsed:.1f}s, "
              f"{usage.input_tokens}+{usage.output_tokens} tokens)")

    # Fuzzy dedup
    unique_services = []
    exact_keys = set()
    dupes_removed = 0
    global_index = 0

    for r in all_results:
        for svc in r.get("services", []):
            name = svc.get("name", "?")
            start = svc.get("start_time", "?")
            duration = svc.get("duration", "?")

            key = (name, start, duration)
            if key in exact_keys:
                dupes_removed += 1
                continue
            if _is_fuzzy_duplicate(svc, unique_services):
                dupes_removed += 1
                continue

            exact_keys.add(key)
            global_index += 1
            svc["global_index"] = global_index
            unique_services.append(svc)

    # Cost
    input_cost = (total_input_tokens / 1_000_000) * 3.0
    output_cost = (total_output_tokens / 1_000_000) * 15.0
    total_cost = input_cost + output_cost

    print(f"\n  Pages: {len(page_paths)}")
    print(f"  Total boxes: {sum(r['count'] for r in all_results)}")
    print(f"  Duplicates removed: {dupes_removed}")
    print(f"  Unique services: {len(unique_services)}")
    print(f"  Tokens: {total_input_tokens:,} in / {total_output_tokens:,} out")
    print(f"  Cost: ${total_cost:.4f}\n")

    return unique_services, all_results


# ── Phase 3: Interactive navigation ───────────────────────────────────

def find_service_by_llm(service):
    """Ask the local LLM to find a service on the current game screen."""
    sid = service.get("name", "?")
    start_time = service.get("start_time", "?")[:5]
    duration = service.get("duration", "?")[:5]

    img = pyautogui.screenshot(region=GAME_REGION)
    img_w, img_h = img.size
    b64 = _encode_pil_image(img)

    prompt = (
        f"This is a {img_w}x{img_h} pixel screenshot of a train simulation game. "
        f"The service list is on the right side of the screen.\n\n"
        f"I need to find this service:\n"
        f"  Name: {sid}\n"
        f"  Start time: {start_time}\n"
        f"  Duration: {duration}\n\n"
        f"If you can see this service in the image, return the x and y pixel coordinates "
        f"of the CENTER of that service box within this image.\n\n"
        f"Respond ONLY with valid JSON:\n"
        f'{{"found": true, "x": <pixel>, "y": <pixel>}}\n'
        f"or if not found:\n"
        f'{{"found": false}}\n'
        f"No other text."
    )

    try:
        response = ask_local_llm(b64, prompt)
        result = _parse_json_response(response)
        if result.get("found"):
            img_x = int(result.get("x", img_w // 2))
            img_y = int(result.get("y", img_h // 2))
            # Convert to absolute screen coords
            screen_x = img_x + GAME_REGION[0]
            screen_y = img_y + GAME_REGION[1]
            print(f"  LLM found service at image ({img_x}, {img_y}) → screen ({screen_x}, {screen_y})")
            return (screen_x, screen_y)
        else:
            print(f"  LLM did not find service on screen")
            return None
    except Exception as e:
        print(f"  LLM locate failed: {e}")
        return None


def navigate_and_click(service):
    """Navigate to the service's page, locate it via LLM, and click."""
    target_page = service["page"]

    print(f"\n  Scrolling to top, then page {target_page}...")
    scroll_to_top()
    scroll_to_page(target_page)

    # First try: use the computed x,y from box geometry
    x = service.get("x")
    y = service.get("y")
    if x and y:
        print(f"  Trying computed coordinates ({x}, {y})...")
        pyautogui.moveTo(x, y)
        time.sleep(0.5)
        pyautogui.mouseDown()
        time.sleep(0.3)
        pyautogui.mouseUp()
        time.sleep(2.0)

        # Check if it highlighted
        from service_loop_scroll_method import wait_for_single_highlight
        count, actual_y = wait_for_single_highlight()
        if count > 0:
            print(f"  SUCCESS: Highlight detected at y={actual_y}")
            return True

        print(f"  Claude coords didn't highlight, trying LLM...")

    # Fallback: ask local LLM to find it on screen
    result = find_service_by_llm(service)
    if result:
        lx, ly = result
        print(f"  Clicking LLM coordinates ({lx}, {ly})...")
        pyautogui.moveTo(lx, ly)
        time.sleep(0.5)
        pyautogui.mouseDown()
        time.sleep(0.3)
        pyautogui.mouseUp()
        time.sleep(2.0)

        from service_loop_scroll_method import wait_for_single_highlight
        count, actual_y = wait_for_single_highlight()
        if count > 0:
            print(f"  SUCCESS: Highlight detected at y={actual_y}")
            return True

        print(f"  LLM coords didn't highlight either")

    print(f"  FAILED to click service")
    return False


def print_service_table(services):
    print(f"\n{'ID':>4}  {'Page':>4}  {'X':>5}  {'Y':>5}  {'Start':>8}  {'Dur':>8}  Name")
    print(f"{'─'*4}  {'─'*4}  {'─'*5}  {'─'*5}  {'─'*8}  {'─'*8}  {'─'*40}")
    for s in services:
        gi = s["global_index"]
        page = s.get("page", "?")
        x = s.get("x", "?")
        y = s.get("y", "?")
        start = s.get("start_time", "?")
        dur = s.get("duration", "?")
        name = s.get("name", "?")[:40]
        print(f"{gi:>4}  {page:>4}  {x:>5}  {y:>5}  {start:>8}  {dur:>8}  {name}")


def interactive_loop(services):
    """Interactive console: enter a service ID to navigate to it."""
    print("\n=== Phase 3: Interactive Navigation ===\n")
    print_service_table(services)

    while True:
        print()
        try:
            user_input = input("Enter service ID to navigate (or 'q' to quit): ").strip()
        except (EOFError, KeyboardInterrupt):
            print("\nDone.")
            break

        if user_input.lower() in ("q", "quit", "exit"):
            print("Done.")
            break

        try:
            target_id = int(user_input)
        except ValueError:
            print("  Invalid input — enter a number or 'q'")
            continue

        match = [s for s in services if s["global_index"] == target_id]
        if not match:
            print(f"  Service ID {target_id} not found (valid: 1-{len(services)})")
            continue

        svc = match[0]
        print(f"\n  Target: #{svc['global_index']} — {svc.get('name', '?')}")
        print(f"  Time: {svc.get('start_time', '?')}  Duration: {svc.get('duration', '?')}")
        print(f"  Page: {svc.get('page', '?')}  Click: ({svc.get('x', '?')}, {svc.get('y', '?')})")

        success = navigate_and_click(svc)
        if success:
            print("\n  Service selected! Press Escape in game to deselect if needed.")
            input("  Press Enter here when ready for the next one...")


# ── Main ──────────────────────────────────────────────────────────────

def main():
    results_path = os.path.join(OUTPUT_DIR, "services.json")

    # Check if we already have results
    if os.path.isfile(results_path):
        print(f"Found existing results at {results_path}")
        with open(results_path, "r") as f:
            data = json.load(f)
        services = data["services"]
        print(f"Loaded {len(services)} services")
    else:
        # Phase 1: Capture
        page_paths = capture_fullscreen_pages()

        # Phase 2: Analyse
        services, raw_results = analyse_pages(page_paths)

        # Save results
        data = {
            "pages": len(page_paths),
            "unique_services": len(services),
            "services": services,
            "raw_results": raw_results,
        }
        with open(results_path, "w") as f:
            json.dump(data, f, indent=2)
        print(f"Results saved to {results_path}")

    # Phase 3: Interactive
    interactive_loop(services)


if __name__ == "__main__":
    main()
