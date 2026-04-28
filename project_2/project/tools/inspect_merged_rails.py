"""Quick stats on a merged rails GeoJSON.

Reports number of continuous segments (sub-lines in the MultiLineString),
total point count, and the start/end of each segment.

Usage:
    python inspect_merged_rails.py <path>
"""
import json
import sys


def main(path):
    with open(path, encoding="utf-8") as f:
        gj = json.load(f)

    feats = gj.get("features", [])
    print(f"features: {len(feats)}")
    for fi, feat in enumerate(feats):
        props = feat.get("properties", {})
        geom = feat.get("geometry", {})
        gtype = geom.get("type")
        print(f"\nfeature {fi}: type={gtype}, properties={props}")
        if gtype == "MultiLineString":
            lines = geom.get("coordinates", [])
            total = sum(len(line) for line in lines)
            print(f"  segments: {len(lines)}, total points: {total}")
            # Length distribution
            lengths = sorted([len(line) for line in lines], reverse=True)
            print(f"  longest 5 segments (point counts): {lengths[:5]}")
            print(f"  shortest 5 segments (point counts): {lengths[-5:]}")
        elif gtype == "LineString":
            print(f"  points: {len(geom.get('coordinates', []))}")


if __name__ == "__main__":
    main(sys.argv[1])
