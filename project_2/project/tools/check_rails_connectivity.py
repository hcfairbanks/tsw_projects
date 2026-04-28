"""Verify rails GeoJSON connectivity.

For each pair of ribbons that share a NetworkNode (start_node_guid /
end_node_guid), the ribbon endpoints should coincide. Reports the worst
offenders so we can spot remaining coordinate-frame bugs.

Usage:
    python check_rails_connectivity.py <rails.geojson>
"""
import json
import math
import sys
from collections import defaultdict


def haversine_m(a, b):
    """Distance in metres between two [lng, lat] points."""
    lng1, lat1 = a
    lng2, lat2 = b
    R = 6371000.0
    p1 = math.radians(lat1)
    p2 = math.radians(lat2)
    dphi = math.radians(lat2 - lat1)
    dl = math.radians(lng2 - lng1)
    h = math.sin(dphi / 2) ** 2 + math.cos(p1) * math.cos(p2) * math.sin(dl / 2) ** 2
    return 2 * R * math.asin(math.sqrt(h))


def main(path):
    with open(path, encoding="utf-8") as f:
        gj = json.load(f)

    ribs = []
    for feat in gj.get("features", []):
        props = feat.get("properties", {})
        coords = feat.get("geometry", {}).get("coordinates", [])
        if len(coords) < 2:
            continue
        ribs.append({
            "guid": props.get("ribbon_guid", ""),
            "start_node": props.get("start_node_guid", ""),
            "end_node": props.get("end_node_guid", ""),
            "start_pt": coords[0],
            "end_pt": coords[-1],
            "length_m": props.get("length_m", 0),
        })

    by_node = defaultdict(list)
    for r in ribs:
        if r["start_node"]:
            by_node[r["start_node"]].append(("start", r))
        if r["end_node"]:
            by_node[r["end_node"]].append(("end", r))

    print(f"ribbons: {len(ribs)}")
    print(f"unique nodes: {len(by_node)}")

    gaps = []
    isolated = 0
    for node, members in by_node.items():
        if len(members) < 2:
            isolated += 1
            continue
        pts = [(role, r["start_pt"] if role == "start" else r["end_pt"], r) for role, r in members]
        # Compare every member to the first; the worst pairwise gap is interesting.
        worst = 0.0
        worst_pair = None
        for i in range(len(pts)):
            for j in range(i + 1, len(pts)):
                d = haversine_m(pts[i][1], pts[j][1])
                if d > worst:
                    worst = d
                    worst_pair = (pts[i][2], pts[j][2])
        if worst > 0.5 and worst_pair:
            gaps.append((worst, node, worst_pair))

    gaps.sort(reverse=True)
    print(f"shared-node pairs with gap > 0.5 m: {len(gaps)}")
    print(f"single-member nodes (dead ends / isolated): {isolated}")
    print()
    print("Top 15 worst gaps:")
    for d, node, (a, b) in gaps[:15]:
        print(f"  {d:8.2f} m  node {node[:20]}...  {a['guid'][:18]} <-> {b['guid'][:18]}  "
              f"(len {a['length_m']:.1f} / {b['length_m']:.1f})")


if __name__ == "__main__":
    main(sys.argv[1])
