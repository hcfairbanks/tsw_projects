"""Standalone experiment C: combines changes from A and B.
- Prompt: "unique combination of service name, start time, and duration" (from B)
- Prompt: chronological order / day-wrap hint (from A)
- Dedup: all three must match + day-wrap aware (from A+B)

Usage:
    python bot/experiment_service_pages_c.py
"""

import base64
import csv
import glob
import json
import os
import re
import sys
import time

import anthropic

# ---------------------------------------------------------------------------
# Config (matches new_method.py / config.py values)
# ---------------------------------------------------------------------------
CLAUDE_MODEL = "claude-sonnet-4-6"
GAME_WIDTH = 1920
GAME_HEIGHT = 1080
GAME_REGION_X = 1920

PAGES_DIR = os.path.join(
    os.path.dirname(__file__),
    # "screenshots", "Boston Worcester", "F40PH-3C MBTA", "train_02", "service_pages",
    "screenshots", "Boston Worcester", "ACS-64 V AMTK50", "train_01", "service_pages",
)

OUTPUT_CSV = os.path.join(PAGES_DIR, "experiment_pages_c.csv")
OUTPUT_JSON = os.path.join(PAGES_DIR, "experiment_pages_c.json")

# Geometry for click coords (fallback when no calibration anchor)
SERVICE_LIST_LEFT = 2072
SERVICE_LIST_RIGHT = 2750
SERVICE_LIST_TOP = 320
FIRST_BOX_TOP = 22
SERVICE_BOX_HEIGHT = 58
SERVICE_BOX_STRIDE = 69

FIRST_CLICK_X = None
FIRST_CLICK_Y = None


# ---------------------------------------------------------------------------
# Prompt (A: day-wrap hint + B: unique combination wording)
# ---------------------------------------------------------------------------
def build_prompt():
    anchor_hint = ""
    if FIRST_CLICK_X is not None and FIRST_CLICK_Y is not None:
        anchor_hint = (
            f"\nCALIBRATION ANCHOR: The center of the FIRST service box is at "
            f"pixel ({FIRST_CLICK_X}, {FIRST_CLICK_Y}) in this image. "
            "Use this as your reference point. All other service boxes are stacked "
            "vertically below this one at regular intervals. Report the center x,y "
            "of each box as accurately as possible relative to this anchor.\n"
        )

    return (
        f"This is a {GAME_WIDTH}x{GAME_HEIGHT} pixel screenshot of a train simulation game. "
        "The service list is on the right side of the screen. "
        "Each service is shown as a rectangular box/card stacked vertically.\n\n"
        "ACCURACY IS CRITICAL. Take your time and read carefully.\n\n"
        + anchor_hint +
        "\nEach service box contains:\n"
        "- A service name (text string, may include route codes like '207-04')\n"
        "- A start time (HH:MM:SS format, e.g. 05:31:00)\n"
        "- A duration (HH:MM:SS format, e.g. 00:28:00)\n\n"
        "IMPORTANT RULES:\n"
        "- TIMESTAMPS ARE KEY: The start_time and duration pair is the unique fingerprint "
        "of each service box. Pay extra attention to reading these values precisely. "
        "Even services with the same name will have different timestamp combinations. "
        "Use the unique combination of service name, start time, and duration to determine whether a box is a new/different service.\n"
        "- The service list is in CHRONOLOGICAL order and can WRAP past midnight. "
        "For example you may see services at 22:00, 23:00, then 01:00. "
        "A service at 01:00 after 23:00 is a NEXT-DAY service, NOT a duplicate "
        "of an earlier 01:00 service from the same morning.\n"
        "- Service names CAN repeat — two boxes with the same name but different "
        "times are SEPARATE services. Count each box.\n"
        "- Read EVERY character carefully. Distinguish between similar characters: "
        "0 vs O vs D, 1 vs I vs l, 5 vs S.\n"
        "- The start time and duration are two SEPARATE time values.\n"
        "- Count ONLY fully or mostly visible boxes. SKIP any box that is "
        "cut off or partially visible at the top or bottom edge — do NOT include it.\n"
        "- If a box is even SLIGHTLY cut off at the bottom, still count it if you can "
        "read the service name and both time values. Only skip if truly unreadable.\n"
        "- Never use 'partially visible' or similar phrases as a service name.\n\n"
        "For each service box, report the x,y pixel coordinates of the CENTER "
        "of the box within this image.\n\n"
        "Respond with ONLY valid JSON and nothing else — no explanation, no preamble:\n"
        '{"count": <number>, "services": ['
        '{"index": 1, "x": <pixel>, "y": <pixel>, "name": "<text>", '
        '"start_time": "<HH:MM:SS>", "duration": "<HH:MM:SS>"}]}'
    )


# ---------------------------------------------------------------------------
# Helpers
# ---------------------------------------------------------------------------
def parse_json_response(text):
    cleaned = text.strip()
    if cleaned.startswith("```"):
        cleaned = cleaned.split("\n", 1)[1]
        cleaned = cleaned.rsplit("```", 1)[0]
    try:
        return json.loads(cleaned)
    except json.JSONDecodeError:
        try:
            obj, _ = json.JSONDecoder().raw_decode(cleaned)
            return obj
        except json.JSONDecodeError:
            pass
        fixed = re.sub(r'(?<=[\s,{])(\w+)":', r'"\1":', cleaned)
        try:
            return json.loads(fixed)
        except json.JSONDecodeError:
            try:
                obj, _ = json.JSONDecoder().raw_decode(fixed)
                return obj
            except json.JSONDecodeError:
                pass
            print(f"  ERROR: Could not parse JSON ({len(text)} chars): {text[:500]}")
            raise


def normalize_time(t):
    parts = t.strip().split(":")
    if len(parts) == 2:
        return f"{parts[0].zfill(2)}:{parts[1].zfill(2)}:00"
    elif len(parts) == 3:
        return f"{parts[0].zfill(2)}:{parts[1].zfill(2)}:{parts[2].zfill(2)}"
    return t


def parse_time_seconds(t):
    try:
        parts = t.strip().split(":")
        if len(parts) == 3:
            return int(parts[0]) * 3600 + int(parts[1]) * 60 + int(parts[2])
        elif len(parts) == 2:
            return int(parts[0]) * 3600 + int(parts[1]) * 60
    except (ValueError, AttributeError):
        pass
    return None


def fuzzy_name_match(a, b):
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


def is_fuzzy_duplicate(svc, existing_list):
    s_time = parse_time_seconds(svc.get("start_time", ""))
    s_dur = parse_time_seconds(svc.get("duration", ""))
    s_name = svc.get("name", "")
    if s_time is None:
        return False
    for existing in existing_list:
        e_time = parse_time_seconds(existing.get("start_time", ""))
        e_dur = parse_time_seconds(existing.get("duration", ""))
        if e_time is None:
            continue
        # All three must match: name, start_time, AND duration
        if (fuzzy_name_match(s_name, existing.get("name", ""))
                and s_time == e_time
                and s_dur == e_dur):
            return True
    return False


def split_service_name(name):
    m = re.match(r'^(\S+)\s+(.+)$', name)
    if m:
        candidate_id = m.group(1)
        if re.search(r'[\d-]', candidate_id):
            return candidate_id, m.group(2)
    return "", name


# ---------------------------------------------------------------------------
# Claude API call
# ---------------------------------------------------------------------------
def ask_claude(client, image_path, prompt):
    with open(image_path, "rb") as f:
        image_data = base64.standard_b64encode(f.read()).decode("utf-8")

    message = client.messages.create(
        model=CLAUDE_MODEL,
        max_tokens=4096,
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


# ---------------------------------------------------------------------------
# Main
# ---------------------------------------------------------------------------
def main():
    page_paths = sorted(glob.glob(os.path.join(PAGES_DIR, "page_*.png")))
    if not page_paths:
        print(f"ERROR: No page_*.png files in {PAGES_DIR}")
        sys.exit(1)

    print(f"Found {len(page_paths)} pages in {PAGES_DIR}\n")

    client = anthropic.Anthropic()
    prompt = build_prompt()

    geo_click_x = (SERVICE_LIST_LEFT + SERVICE_LIST_RIGHT) // 2
    geo_first_y = SERVICE_LIST_TOP + FIRST_BOX_TOP + SERVICE_BOX_HEIGHT // 2

    # Phase 1: Send each page to Claude
    all_results = []
    for i, page_path in enumerate(page_paths):
        start = time.time()
        response_text, usage = ask_claude(client, page_path, prompt)
        elapsed = time.time() - start

        result = parse_json_response(response_text)
        count = result.get("count", 0)
        services = result.get("services", [])

        for box_idx, svc in enumerate(services):
            svc["llm_x"] = svc.pop("x", None)
            svc["llm_y"] = svc.pop("y", None)
            svc["click_x"] = geo_click_x
            svc["click_y"] = geo_first_y + (box_idx * SERVICE_BOX_STRIDE)
            svc["page"] = i
            if "start_time" in svc:
                svc["start_time"] = normalize_time(svc["start_time"])
            if "duration" in svc:
                svc["duration"] = normalize_time(svc["duration"])

        all_results.append({"page": i, "count": count, "services": services})
        print(f"  Page {i}: {count} services  ({usage.input_tokens}+{usage.output_tokens} tokens, {elapsed:.1f}s)")

    # Phase 2: Dedup (day-wrap aware, all three fields must match)
    unique_services = []
    exact_keys = set()
    dupes_removed = 0
    total_boxes = sum(r["count"] for r in all_results)
    day_wrapped = False
    prev_seconds = None

    for r in all_results:
        for svc in r["services"]:
            name = svc.get("name", "?")
            if "partially visible" in name.lower():
                dupes_removed += 1
                continue
            start = svc.get("start_time", "?")
            duration = svc.get("duration", "?")

            # Detect day wrap: time jumps backwards by more than 12 hours
            cur_seconds = parse_time_seconds(start)
            if not day_wrapped and cur_seconds is not None and prev_seconds is not None:
                if prev_seconds - cur_seconds > 12 * 3600:
                    day_wrapped = True
                    print(f"  ** Day wrap detected at service '{name}' {start} (prev was {prev_seconds}s)")
            if cur_seconds is not None:
                prev_seconds = cur_seconds

            key = (name, start, duration)

            if not day_wrapped:
                if key in exact_keys:
                    dupes_removed += 1
                    continue
                if is_fuzzy_duplicate(svc, unique_services):
                    dupes_removed += 1
                    continue
                exact_keys.add(key)
            else:
                # After day wrap: only dedup against other post-wrap services
                post_wrap = [s for s in unique_services if s.get("_post_wrap")]
                post_key = key in {(s.get("name","?"), s.get("start_time","?"), s.get("duration","?")) for s in post_wrap}
                if post_key:
                    dupes_removed += 1
                    continue
                if is_fuzzy_duplicate(svc, post_wrap):
                    dupes_removed += 1
                    continue

            svc["global_index"] = len(unique_services) + 1
            svc["_post_wrap"] = day_wrapped
            unique_services.append(svc)

    # Phase 3: Report
    print(f"\n{'='*60}")
    print(f"  Total boxes seen:      {total_boxes}")
    print(f"  Duplicates removed:    {dupes_removed}")
    print(f"  Unique services:       {len(unique_services)}")
    print(f"  Expected:              10 (manual count)")
    print(f"{'='*60}")

    # Show per-page breakdown
    print(f"\nPer-page raw counts:")
    for r in all_results:
        svcs = r["services"]
        times = [s.get("start_time", "?") for s in svcs]
        print(f"  Page {r['page']}: {r['count']} boxes -> {times}")

    # Phase 4: Save CSV
    with open(OUTPUT_CSV, "w", newline="") as f:
        writer = csv.writer(f)
        writer.writerow(["number", "page", "click", "service_id", "service_name", "time", "duration"])
        for svc in unique_services:
            gi = svc["global_index"]
            page = svc["page"]
            cx = svc["click_x"]
            cy = svc["click_y"]
            name = svc.get("name", "?")
            st = normalize_time(svc.get("start_time", "?"))
            dur = normalize_time(svc.get("duration", "?"))
            sid, _ = split_service_name(name)
            writer.writerow([gi, f"page {page}", f"click ({cx} {cy})", sid, name, st, dur])

    print(f"\nCSV saved: {OUTPUT_CSV}")

    # Save JSON for inspection
    with open(OUTPUT_JSON, "w") as f:
        json.dump({"all_pages": all_results, "unique_count": len(unique_services)}, f, indent=2)
    print(f"JSON saved: {OUTPUT_JSON}")


if __name__ == "__main__":
    main()
