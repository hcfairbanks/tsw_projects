"""Claude API version of the AI service page analyser.

Processes the same page images captured by ai_scroll.py but sends them
to the Claude API (Sonnet) instead of a local LM Studio model.

Requires:
    pip install anthropic
    export ANTHROPIC_API_KEY="sk-ant-..."

Usage:
    python ai_scroll_claude.py                # analyse existing captures
    python ai_scroll_claude.py --parallel 10  # process 10 pages at a time (default 5)
"""

import base64
import json
import os
import sys
import time
from concurrent.futures import ThreadPoolExecutor, as_completed

import anthropic

# Add bot dir to path for config import
sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))
import config

# ── Settings ─────────────────────────────────────────────────────────
MODEL = "claude-sonnet-4-6"
AI_CAPTURE_DIR = os.path.join(config.BASE_DIR, "ai_captures")
DEFAULT_PARALLEL = 3  # pages to process concurrently (keep low to avoid rate limits)
RATE_LIMIT_RPM = 50   # max requests per minute
RATE_LIMIT_HEADROOM = 5  # stay this many under the limit


# ── Prompt ───────────────────────────────────────────────────────────
PROMPT = (
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


# ── Helper functions (shared with ai_scroll.py) ─────────────────────

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
    """Check if two service names are likely the same despite OCR differences."""
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
    """Check if a service is a fuzzy duplicate of any already seen."""
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


# ── Claude API call ──────────────────────────────────────────────────

def ask_claude(client, image_path, prompt, max_retries=5):
    """Send an image + prompt to Claude and return the response text.

    Retries with exponential backoff on rate limit (429) and server errors (5xx).
    """
    with open(image_path, "rb") as f:
        image_data = base64.standard_b64encode(f.read()).decode("utf-8")

    for attempt in range(max_retries):
        try:
            message = client.messages.create(
                model=MODEL,
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

        except anthropic.RateLimitError as e:
            wait = min(2 ** attempt * 5, 60)  # 5s, 10s, 20s, 40s, 60s
            print(f"  Rate limited (attempt {attempt + 1}/{max_retries}), waiting {wait}s...")
            time.sleep(wait)
            if attempt == max_retries - 1:
                raise

        except anthropic.APIStatusError as e:
            if e.status_code >= 500:
                wait = min(2 ** attempt * 3, 30)
                print(f"  Server error {e.status_code} (attempt {attempt + 1}/{max_retries}), waiting {wait}s...")
                time.sleep(wait)
                if attempt == max_retries - 1:
                    raise
            else:
                raise


def process_page(client, page_index, page_path, overlap_px, no_crop=False):
    """Process a single page: crop if needed, send to Claude, return result."""
    list_height = config.SERVICE_LIST_BOTTOM - config.SERVICE_LIST_TOP
    click_x = (config.SERVICE_LIST_LEFT + config.SERVICE_LIST_RIGHT) // 2

    # Crop overlap for pages after the first (unless --no-crop)
    if page_index > 0 and not no_crop:
        send_path, full_height = _crop_overlap(page_path, overlap_px)
        if send_path is None:
            print(f"  Skipping page {page_index} — too small to crop "
                  f"({full_height}px < {overlap_px}px)")
            return None
        cropped_height = full_height - overlap_px
    else:
        send_path = page_path
        full_height = list_height
        cropped_height = full_height

    start = time.time()
    response_text, usage = ask_claude(client, send_path, PROMPT)
    elapsed = time.time() - start

    # Parse JSON
    cleaned = response_text.strip()
    if cleaned.startswith("```"):
        cleaned = cleaned.split("\n", 1)[1]
        cleaned = cleaned.rsplit("```", 1)[0]

    result = json.loads(cleaned)
    count = result.get("count", 0)

    # Compute click coordinates
    services = result.get("services", [])
    for svc in services:
        y_pct = svc.get("y_percent", 50)
        if page_index > 0:
            y_in_cropped = (y_pct / 100.0) * cropped_height
            y_in_full = y_in_cropped + overlap_px
            svc["click_y"] = int(config.SERVICE_LIST_TOP + y_in_full)
        else:
            svc["click_y"] = int(
                config.SERVICE_LIST_TOP + (y_pct / 100.0) * list_height)
        svc["click_x"] = click_x
        svc["page"] = page_index

    return {
        "page": page_index,
        "count": count,
        "services": services,
        "elapsed": elapsed,
        "input_tokens": usage.input_tokens,
        "output_tokens": usage.output_tokens,
    }


# ── Main ─────────────────────────────────────────────────────────────

def main():
    # Parse flags
    parallel = DEFAULT_PARALLEL
    no_crop = "--no-crop" in sys.argv
    for i, arg in enumerate(sys.argv):
        if arg == "--parallel" and i + 1 < len(sys.argv):
            parallel = int(sys.argv[i + 1])

    print("=== TSW AI Service Discovery (Claude API) ===\n")
    print(f"  Model:    {MODEL}")
    print(f"  Parallel: {parallel} pages at a time")
    if no_crop:
        print(f"  Crop:     DISABLED (sending full uncropped pages)")
    print()

    # Check for API key
    if not os.environ.get("ANTHROPIC_API_KEY"):
        print("ERROR: ANTHROPIC_API_KEY environment variable not set.")
        print("  Get a key from: https://console.anthropic.com/settings/keys")
        print('  Then run: export ANTHROPIC_API_KEY="sk-ant-..."')
        sys.exit(1)

    # Find page images
    if not os.path.isdir(AI_CAPTURE_DIR):
        print(f"ERROR: No capture directory found at {AI_CAPTURE_DIR}")
        print("  Run ai_scroll.py first to capture pages.")
        sys.exit(1)

    page_files = sorted(
        f for f in os.listdir(AI_CAPTURE_DIR)
        if f.startswith("page_") and f.endswith(".png") and "cropped" not in f
    )
    if not page_files:
        print(f"ERROR: No page images found in {AI_CAPTURE_DIR}")
        sys.exit(1)

    page_paths = [os.path.join(AI_CAPTURE_DIR, f) for f in page_files]
    print(f"Found {len(page_paths)} page(s) to process.\n")

    overlap_px = config.OVERLAP_BOXES * config.SERVICE_BOX_STRIDE
    client = anthropic.Anthropic()

    # Process pages in batches, respecting rate limits
    all_results = [None] * len(page_paths)
    total_input_tokens = 0
    total_output_tokens = 0
    total_boxes = 0
    errors = 0
    run_start = time.time()

    batch_size = RATE_LIMIT_RPM - RATE_LIMIT_HEADROOM  # 45 requests per batch
    total_pages = len(page_paths)

    print(f"Processing {total_pages} pages ({batch_size} per minute, {parallel} concurrent)...")

    for batch_start in range(0, total_pages, batch_size):
        batch_end = min(batch_start + batch_size, total_pages)
        batch_indices = list(range(batch_start, batch_end))
        batch_start_time = time.time()

        if batch_start > 0:
            # Wait until 60s have passed since the last batch started
            elapsed_since_batch = time.time() - batch_start_time
            wait = max(0, 62 - elapsed_since_batch)  # 62s for safety margin
            if wait > 0:
                print(f"\n  Rate limit pause: waiting {wait:.0f}s before next batch...")
                time.sleep(wait)
            batch_start_time = time.time()

        print(f"\n  Batch: pages {batch_start}-{batch_end - 1} "
              f"({len(batch_indices)} pages)")

        with ThreadPoolExecutor(max_workers=parallel) as executor:
            futures = {}
            for i in batch_indices:
                future = executor.submit(
                    process_page, client, i, page_paths[i], overlap_px, no_crop)
                futures[future] = i

            for future in as_completed(futures):
                idx = futures[future]
                try:
                    result = future.result()
                    if result is None:
                        # Page was too small to crop, skipped
                        continue
                    all_results[idx] = result
                    total_boxes += result["count"]
                    total_input_tokens += result["input_tokens"]
                    total_output_tokens += result["output_tokens"]
                    print(f"  Page {idx:3d}: {result['count']} services  "
                          f"({result['elapsed']:.1f}s, "
                          f"{result['input_tokens']}+{result['output_tokens']} tokens)")
                except Exception as e:
                    errors += 1
                    print(f"  Page {idx:3d}: ERROR - {e}")
                    all_results[idx] = {"page": idx, "count": 0, "services": []}

    total_elapsed = time.time() - run_start

    # Filter out None entries (shouldn't happen but safety)
    all_results = [r for r in all_results if r is not None]

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

    # ── Cost calculation ──
    # Sonnet 4.6 pricing
    input_cost = (total_input_tokens / 1_000_000) * 3.0
    output_cost = (total_output_tokens / 1_000_000) * 15.0
    total_cost = input_cost + output_cost

    print("\n" + "=" * 70)
    print("SUMMARY")
    print("=" * 70)
    print(f"  Pages processed:     {len(page_paths)}")
    print(f"  Errors:              {errors}")
    print(f"  Total boxes seen:    {total_boxes}")
    print(f"  Duplicates removed:  {dupes_removed}")
    print(f"  Unique services:     {len(unique_services)}")
    print(f"  Total time:          {total_elapsed:.1f}s")
    print(f"  Tokens (in/out):     {total_input_tokens:,} / {total_output_tokens:,}")
    print(f"  Cost:                ${total_cost:.4f} "
          f"(${input_cost:.4f} input + ${output_cost:.4f} output)")
    print("=" * 70)

    # ── Save results ──
    results_path = os.path.join(AI_CAPTURE_DIR, "analysis_results_claude.json")
    with open(results_path, "w") as f:
        json.dump({
            "model": MODEL,
            "pages": len(page_paths),
            "total_boxes": total_boxes,
            "duplicates_removed": dupes_removed,
            "unique_count": len(unique_services),
            "total_time_seconds": total_elapsed,
            "total_input_tokens": total_input_tokens,
            "total_output_tokens": total_output_tokens,
            "total_cost_usd": round(total_cost, 4),
            "per_page": [
                {
                    "page": r["page"],
                    "count": r["count"],
                    "services": r.get("services", []),
                }
                for r in all_results
            ],
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


if __name__ == "__main__":
    main()
