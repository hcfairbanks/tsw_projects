"""Evaluate page drift by comparing captured service data against expected CSV.

Usage:
    python bot/evaluate_drift.py                          # scan all routes/trains
    python bot/evaluate_drift.py "Boston Sprinter"        # scan one route
    python bot/evaluate_drift.py "Boston Sprinter" "CTC-3 MBTA"  # scan one train

Reads services.csv and llm_data.json from each train's screenshot folder.
Reports mismatches where the captured service name differs from the expected name,
grouped by page to help identify scroll drift.

After running, update PAGE_Y_DRIFT in bot/config.py with corrections.
"""

import csv
import json
import os
import sys

SCREENSHOTS_DIR = os.path.join(os.path.dirname(__file__), "screenshots")


def normalize_name(name):
    """Strip whitespace and normalize for comparison."""
    if not name:
        return ""
    return " ".join(name.strip().split())


def load_csv_services(csv_path):
    """Load services.csv and return list of dicts with number, page, service_name."""
    services = []
    with open(csv_path, "r", encoding="utf-8") as f:
        reader = csv.reader(f)
        header = None
        for row in reader:
            if len(row) < 5:
                continue
            number = row[0].strip()
            if not number.isdigit():
                header = row  # capture header for column detection
                continue
            page_str = row[1].strip()
            page_num = int(page_str.replace("page ", "")) if "page" in page_str else -1
            service_name = row[4].strip()
            time_val = row[5].strip() if len(row) > 5 else ""
            duration = row[6].strip() if len(row) > 6 else ""
            services.append({
                "number": int(number),
                "page": page_num,
                "service_name": service_name,
                "time": time_val,
                "duration": duration,
            })
    return services


def load_captured_name(service_dir):
    """Read the captured service name from llm_data.json."""
    llm_path = os.path.join(service_dir, "llm_data.json")
    if not os.path.exists(llm_path):
        return None
    try:
        with open(llm_path, "r", encoding="utf-8") as f:
            data = json.load(f)
        return data.get("level_info", {}).get("service_name", None)
    except (json.JSONDecodeError, KeyError):
        return None


def evaluate_train(train_dir, train_label):
    """Evaluate one train directory. Returns list of mismatch dicts."""
    mismatches = []
    empty_folders = []
    max_page = 0

    for instance in sorted(os.listdir(train_dir)):
        instance_dir = os.path.join(train_dir, instance)
        if not os.path.isdir(instance_dir) or not instance.startswith("train_"):
            continue

        csv_path = os.path.join(instance_dir, "services.csv")
        if not os.path.exists(csv_path):
            continue

        csv_services = load_csv_services(csv_path)
        if not csv_services:
            continue

        max_page = max(s["page"] for s in csv_services)

        for svc in csv_services:
            num = svc["number"]
            folder_name = f"service_{num:03d}"
            service_dir = os.path.join(instance_dir, folder_name)

            if not os.path.isdir(service_dir):
                empty_folders.append({
                    "train": train_label,
                    "instance": instance,
                    "service": num,
                    "page": svc["page"],
                    "expected": svc["service_name"],
                    "status": "missing_folder",
                })
                continue

            if not os.listdir(service_dir):
                empty_folders.append({
                    "train": train_label,
                    "instance": instance,
                    "service": num,
                    "page": svc["page"],
                    "expected": svc["service_name"],
                    "status": "empty_folder",
                })
                continue

            captured = load_captured_name(service_dir)
            if captured is None:
                empty_folders.append({
                    "train": train_label,
                    "instance": instance,
                    "service": num,
                    "page": svc["page"],
                    "expected": svc["service_name"],
                    "status": "no_llm_data",
                })
                continue

            expected = normalize_name(svc["service_name"])
            got = normalize_name(captured)

            if expected and got and expected != got:
                mismatches.append({
                    "train": train_label,
                    "instance": instance,
                    "service": num,
                    "page": svc["page"],
                    "expected": expected,
                    "captured": got,
                })

    return mismatches, empty_folders, max_page


def print_report(all_mismatches, all_empty, train_stats):
    """Print the drift evaluation report."""
    print("=" * 80)
    print("PAGE DRIFT EVALUATION REPORT")
    print("=" * 80)

    if not all_mismatches and not all_empty:
        print("\nAll services match. No drift detected.")
        return

    # Group mismatches by page
    by_page = {}
    for m in all_mismatches:
        page = m["page"]
        if page not in by_page:
            by_page[page] = []
        by_page[page].append(m)

    if all_mismatches:
        print(f"\nMISMATCHES: {len(all_mismatches)} services captured incorrectly\n")
        print(f"{'Svc':>4}  {'Page':>4}  {'Instance':<10}  {'Train':<20}  Expected -> Captured")
        print("-" * 80)
        for m in sorted(all_mismatches, key=lambda x: (x["page"], x["service"])):
            print(f"{m['service']:>4}  {m['page']:>4}  {m['instance']:<10}  "
                  f"{m['train']:<20}  {m['expected']}")
            print(f"{'':>4}  {'':>4}  {'':>10}  {'':>20}  -> {m['captured']}")

        print(f"\nDRIFT BY PAGE:")
        print(f"{'Page':>4}  {'Count':>5}  Services")
        print("-" * 50)
        for page in sorted(by_page.keys()):
            items = by_page[page]
            svc_list = ", ".join(str(m["service"]) for m in items)
            print(f"{page:>4}  {len(items):>5}  {svc_list}")

        # Suggest corrections
        print(f"\nSUGGESTED PAGE_Y_DRIFT (add to bot/config.py):")
        print("PAGE_Y_DRIFT = {")
        for page in sorted(by_page.keys()):
            # Rough heuristic: ~0.5px per page, minimum 2px
            suggested = max(2, round(page * 0.5))
            print(f"    {page}: {suggested},")
        print("}")

    if all_empty:
        print(f"\nMISSING/EMPTY: {len(all_empty)} services not captured\n")
        for e in sorted(all_empty, key=lambda x: (x["train"], x["service"])):
            print(f"  service_{e['service']:03d} (page {e['page']}) "
                  f"[{e['instance']}] {e['train']} - {e['status']}: {e['expected']}")

    # Summary per train
    print(f"\nTRAIN SUMMARY:")
    print(f"{'Train':<30}  {'Total':>5}  {'Match':>5}  {'Wrong':>5}  "
          f"{'Missing':>7}  {'Pages':>5}  {'Accuracy':>8}")
    print("-" * 80)
    for label, stats in sorted(train_stats.items()):
        total = stats["total"]
        wrong = stats["wrong"]
        missing = stats["missing"]
        matched = total - wrong - missing
        pages = stats["max_page"] + 1
        acc = f"{matched/total*100:.1f}%" if total > 0 else "N/A"
        print(f"{label:<30}  {total:>5}  {matched:>5}  {wrong:>5}  "
              f"{missing:>7}  {pages:>5}  {acc:>8}")


def main():
    filter_route = sys.argv[1] if len(sys.argv) > 1 else None
    filter_train = sys.argv[2] if len(sys.argv) > 2 else None

    if not os.path.isdir(SCREENSHOTS_DIR):
        print(f"Screenshots directory not found: {SCREENSHOTS_DIR}")
        sys.exit(1)

    all_mismatches = []
    all_empty = []
    train_stats = {}

    for route_name in sorted(os.listdir(SCREENSHOTS_DIR)):
        route_dir = os.path.join(SCREENSHOTS_DIR, route_name)
        if not os.path.isdir(route_dir):
            continue
        if filter_route and route_name != filter_route:
            continue

        # Routes may have a timetable subfolder
        for sub in sorted(os.listdir(route_dir)):
            sub_dir = os.path.join(route_dir, sub)
            if not os.path.isdir(sub_dir):
                continue

            # Check if this is a train folder (has train_01) or a timetable folder
            has_train = any(d.startswith("train_") for d in os.listdir(sub_dir)
                          if os.path.isdir(os.path.join(sub_dir, d)))

            if has_train:
                # sub is a train name directly under route
                train_name = sub
                train_dir = sub_dir
                if filter_train and train_name != filter_train:
                    continue
                label = f"{route_name}/{train_name}"
                mismatches, empty, max_page = evaluate_train(train_dir, label)
                all_mismatches.extend(mismatches)
                all_empty.extend(empty)
                total = len(mismatches) + len(empty)
                # Count total services from CSV
                for inst in os.listdir(train_dir):
                    inst_dir = os.path.join(train_dir, inst)
                    csv_p = os.path.join(inst_dir, "services.csv")
                    if os.path.exists(csv_p):
                        csv_svcs = load_csv_services(csv_p)
                        inst_label = f"{label}/{inst}"
                        inst_wrong = len([m for m in mismatches if m["instance"] == inst])
                        inst_missing = len([e for e in empty if e["instance"] == inst])
                        train_stats[inst_label] = {
                            "total": len(csv_svcs),
                            "wrong": inst_wrong,
                            "missing": inst_missing,
                            "max_page": max_page,
                        }
            else:
                # sub is a timetable folder containing train folders
                for train_name in sorted(os.listdir(sub_dir)):
                    train_dir = os.path.join(sub_dir, train_name)
                    if not os.path.isdir(train_dir):
                        continue
                    if filter_train and train_name != filter_train:
                        continue
                    label = f"{route_name}/{train_name}"
                    mismatches, empty, max_page = evaluate_train(train_dir, label)
                    all_mismatches.extend(mismatches)
                    all_empty.extend(empty)
                    for inst in os.listdir(train_dir):
                        inst_dir = os.path.join(train_dir, inst)
                        csv_p = os.path.join(inst_dir, "services.csv")
                        if os.path.exists(csv_p):
                            csv_svcs = load_csv_services(csv_p)
                            inst_label = f"{label}/{inst}"
                            inst_wrong = len([m for m in mismatches if m["instance"] == inst])
                            inst_missing = len([e for e in empty if e["instance"] == inst])
                            train_stats[inst_label] = {
                                "total": len(csv_svcs),
                                "wrong": inst_wrong,
                                "missing": inst_missing,
                                "max_page": max_page,
                            }

    print_report(all_mismatches, all_empty, train_stats)


if __name__ == "__main__":
    main()