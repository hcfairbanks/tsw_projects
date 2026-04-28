"""Cross-reference bot llm_data conductor_compatible ground truth with
each candidate service property to find which one distinguishes them.
"""
import glob
import json
import sys
from collections import Counter, defaultdict


def load_bot_truth(screenshots_root):
    """Returns {service_name: bool_conductor_compatible}."""
    truth = {}
    for path in glob.glob(f"{screenshots_root}/**/llm_data.json", recursive=True):
        try:
            with open(path, encoding="utf-8") as f:
                d = json.load(f)
        except Exception:
            continue
        loc = d.get("location") or {}
        name = loc.get("current_service_name")
        if not name:
            # fall back to the display name in level_info
            li = d.get("level_info") or {}
            name = (li.get("service_name") or "").split(" ")[0]
        if not name:
            continue
        cc = d.get("conductor_compatible")
        truth[name] = bool(cc)
    return truth


def match_name(service_name, truth_keys):
    """Match a uasset service name to a bot truth key. Bot uses headcode
    (first token of FriendlyName before ' : '). uasset 'Name' is e.g.
    'MBTA-508' (matches directly)."""
    if service_name in truth_keys:
        return service_name
    return None


def main():
    services_path = sys.argv[1]
    screenshots = sys.argv[2]
    services = [json.loads(l) for l in open(services_path) if l.startswith("{")]
    truth = load_bot_truth(screenshots)
    print(f"Loaded {len(services)} services, {len(truth)} bot ground truth rows", file=sys.stderr)

    # Match services to truth. uasset "Name" is typically e.g. "MBTA-508",
    # bot serviceName is typically "MBTA-508" too (from screenshots).
    matched = []
    for svc in services:
        name = svc.get("Name")
        if name in truth:
            matched.append((svc, truth[name]))
    print(f"Matched {len(matched)} services to bot truth", file=sys.stderr)

    cc_true = [s for s, t in matched if t]
    cc_false = [s for s, t in matched if not t]
    print(f"  conductor=true: {len(cc_true)}", file=sys.stderr)
    print(f"  conductor=false: {len(cc_false)}", file=sys.stderr)

    # For each property, count value distribution in each group.
    all_props = set()
    for s, _ in matched:
        all_props.update(s.keys())

    print("\nProperty distinguishing power (prop: cc_true_vals -> cc_false_vals):")
    for prop in sorted(all_props):
        t_vals = Counter(s.get(prop) for s in cc_true)
        f_vals = Counter(s.get(prop) for s in cc_false)
        if set(t_vals) != set(f_vals):
            # Distinguishing
            print(f"  {prop}:")
            print(f"    cc=true ({len(cc_true)}): {dict(t_vals)}")
            print(f"    cc=false({len(cc_false)}): {dict(f_vals)}")


if __name__ == "__main__":
    main()
