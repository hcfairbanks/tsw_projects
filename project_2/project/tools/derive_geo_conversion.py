"""Derive the world-tile → real-world lat/lng conversion from a hud-go
recording (raw_data_timetable_*.json).

Each sample has {latitude, longitude, x, y} where (x, y) is the integer tile
grid. By measuring lat/lng delta across a tile boundary, we get the tile
size in metres.

Expected result: tile size ≈ 1024m (Unreal World Composition default).
"""
import json
import math
import sys
from collections import defaultdict


def haversine_m(lat1, lng1, lat2, lng2):
    R = 6_378_137.0
    p1 = math.radians(lat1)
    p2 = math.radians(lat2)
    dp = math.radians(lat2 - lat1)
    dl = math.radians(lng2 - lng1)
    a = (math.sin(dp / 2) ** 2 +
         math.cos(p1) * math.cos(p2) * math.sin(dl / 2) ** 2)
    return 2 * R * math.asin(math.sqrt(a))


def main(path):
    with open(path, encoding='utf-8') as f:
        doc = json.load(f)
    coords = doc.get('coordinates', [])
    print(f'Loaded {len(coords)} samples from {path}')
    if not coords:
        return

    # 1. Tile distribution and lat/lng bounds per tile
    by_tile = defaultdict(list)
    for c in coords:
        tile = (c['x'], c['y'])
        by_tile[tile].append((c['latitude'], c['longitude']))

    print(f'\nUnique tiles visited: {len(by_tile)}')
    # Sort by count to find most-populated
    top = sorted(by_tile.items(), key=lambda kv: -len(kv[1]))
    print('Top 8 tiles by sample count:')
    for tile, pts in top[:8]:
        lats = [p[0] for p in pts]
        lngs = [p[1] for p in pts]
        print(f'  tile=({tile[0]:>4},{tile[1]:>4}) n={len(pts):5} '
              f'lat=[{min(lats):.6f}..{max(lats):.6f}] '
              f'lng=[{min(lngs):.6f}..{max(lngs):.6f}]')

    # 2. Tile size estimation — for each adjacent pair of tiles we both visited,
    # compute the mean lat/lng offset. Should be constant (= tile size) if UE
    # uses a uniform grid.
    print('\nTile size from adjacent-tile centroid offsets:')
    centroid = {
        t: (sum(p[0] for p in pts) / len(pts),
            sum(p[1] for p in pts) / len(pts))
        for t, pts in by_tile.items()
    }
    # Collect X-axis deltas (tiles with same y, neighbouring x)
    x_deltas_m = []
    y_deltas_m = []
    for (tx, ty), (lat, lng) in centroid.items():
        if (tx + 1, ty) in centroid:
            lat2, lng2 = centroid[(tx + 1, ty)]
            x_deltas_m.append(haversine_m(lat, lng, lat2, lng2))
        if (tx, ty + 1) in centroid:
            lat2, lng2 = centroid[(tx, ty + 1)]
            y_deltas_m.append(haversine_m(lat, lng, lat2, lng2))
    if x_deltas_m:
        print(f'  x-axis tile size from {len(x_deltas_m)} neighbour pairs: '
              f'min={min(x_deltas_m):.1f}m mean={sum(x_deltas_m)/len(x_deltas_m):.1f}m max={max(x_deltas_m):.1f}m')
    if y_deltas_m:
        print(f'  y-axis tile size from {len(y_deltas_m)} neighbour pairs: '
              f'min={min(y_deltas_m):.1f}m mean={sum(y_deltas_m)/len(y_deltas_m):.1f}m max={max(y_deltas_m):.1f}m')

    # 3. Better estimate: for each tile visited, compute lat/lng range. Across
    # many tiles, the RANGE within a tile → tile span in each axis.
    # For this we need samples that span a whole tile (the recording may only
    # cover a partial strip). Use the max-range tile as a lower bound.
    best_lat_range = 0
    best_lng_range = 0
    for pts in by_tile.values():
        lats = [p[0] for p in pts]
        lngs = [p[1] for p in pts]
        best_lat_range = max(best_lat_range, max(lats) - min(lats))
        best_lng_range = max(best_lng_range, max(lngs) - min(lngs))
    print(f'\nMax within-tile range seen (lower bound on tile size):')
    print(f'  lat: {best_lat_range:.6f} deg ≈ {best_lat_range * 111194:.1f}m')
    print(f'  lng: {best_lng_range:.6f} deg ≈ {best_lng_range * 111320 * math.cos(math.radians(coords[0]["latitude"])):.1f}m')

    # 4. Jitter analysis: find the longest run of consecutive samples in the
    # same tile, measure lat/lng variance.
    print('\nJitter analysis (stationary runs):')
    # Find runs of samples where tile + movement < threshold
    # Simpler: pick a tile with many samples, look at consecutive 100-sample windows
    if top:
        tile, pts = top[0]
        lats = [p[0] for p in pts]
        lngs = [p[1] for p in pts]
        # consecutive diffs
        lat_diffs = [abs(lats[i] - lats[i-1]) for i in range(1, len(lats))]
        lng_diffs = [abs(lngs[i] - lngs[i-1]) for i in range(1, len(lngs))]
        lat_diffs.sort()
        lng_diffs.sort()
        # median = typical stepping (mix of movement + jitter)
        # 5th percentile = near-stationary jitter
        p5 = lambda l: l[len(l) // 20] if l else 0
        p50 = lambda l: l[len(l) // 2] if l else 0
        print(f'  in busiest tile ({tile}): lat diff p5={p5(lat_diffs):.2e} median={p50(lat_diffs):.2e}')
        print(f'    → jitter floor ≈ {p5(lat_diffs) * 111194 * 1000:.3f}mm/sample (if stationary)')


if __name__ == '__main__':
    path = sys.argv[1] if len(sys.argv) > 1 else (
        r'C:/Users/hcfai/Desktop/applications_2/hud-go/recording_data/raw_data_timetable_34781_2026-04-23_21-26-59.json'
    )
    main(path)
