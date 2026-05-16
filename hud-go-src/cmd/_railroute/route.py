"""Backfill a single timetable's path by running a Dijkstra search through
the route's rail graph from the spawn coord to the last GO VIA waypoint.

Graph construction:
  - Each rail LineString's consecutive vertices are connected by edges
    weighted by haversine distance.
  - Vertices with identical (lat, lng) across different LineStrings are
    treated as the same node (junctions in TSW are stored as shared
    coords). Coords are quantised to ~1cm precision to absorb float
    jitter.

This is a one-shot demonstration for timetable 364611. After the user
confirms the result looks right we can productionise the same routing
into the extractor.
"""
import sqlite3, json, math, heapq, sys, time
from collections import defaultdict

HUD = 'hud-go/resources/db/tsw_hud.db'
TID = 364611

def hav(a, b):
    R = 6371000
    p1, p2 = math.radians(a[0]), math.radians(b[0])
    dlat = math.radians(b[0]-a[0]); dlng = math.radians(b[1]-a[1])
    A = math.sin(dlat/2)**2 + math.cos(p1)*math.cos(p2)*math.sin(dlng/2)**2
    return 2*R*math.asin(math.sqrt(A))

def quant(lat, lng):
    """7-decimal precision ~ 1.1 cm at the equator. Floats stored in
    GeoJSON tend to come with full precision, but this absorbs any
    last-bit jitter so coords from different LineStrings that should
    coincide actually hash equal."""
    return (round(lat, 7), round(lng, 7))

def main():
    t0 = time.time()
    db = sqlite3.connect(HUD)
    cur = db.cursor()
    t = cur.execute("SELECT service_name, route_id FROM timetables WHERE id=?", (TID,)).fetchone()
    print(f"timetable {TID}: {t}")
    route_id = t[1]

    # Pull waypoints (any entry with coords, ordered by sort_order)
    waypoints = []
    for r in cur.execute("""SELECT te.sort_order, te.latitude, te.longitude, ta.name, te.details
                            FROM timetable_entries te LEFT JOIN timetable_actions ta ON ta.id=te.action_id
                            WHERE te.timetable_id=? AND te.latitude IS NOT NULL AND te.latitude != ''
                            ORDER BY te.sort_order""", (TID,)):
        try:
            waypoints.append((float(r[1]), float(r[2]), r[3], r[4]))
        except: pass
    print(f"waypoints: {len(waypoints)}")
    for w in waypoints:
        print(f"  ({w[0]:.5f},{w[1]:.5f}) {w[2]:<22} {w[3]!r}")

    # Build rail graph from route_coordinates LineStrings
    print(f"\nbuilding rail graph for route {route_id}...")
    rc = cur.execute("SELECT coordinates FROM route_coordinates WHERE route_id=?", (route_id,)).fetchone()[0]
    feats = json.loads(rc)
    edges = defaultdict(list)
    node_coord = {}
    for f in feats:
        g = f.get('geometry', {})
        p = f.get('properties', {})
        ftype = p.get('feature_type')
        gtype = g.get('type', '')
        # Include the per-section typed rails (siding/platform/line/running)
        # AND the master untyped MultiLineString layer (the "merged
        # ribbons" — 5,534 segments covering the full route, the only
        # source of through-route connectivity since siding/platform are
        # fragmented yard islands).
        if gtype in ('LineString', 'MultiLineString'):
            is_typed = ftype in ('line_track', 'siding_track', 'running_track', 'platform_track')
            is_master = ftype is None and gtype == 'MultiLineString'
            if not (is_typed or is_master):
                continue
            lines = g['coordinates'] if gtype == 'MultiLineString' else [g['coordinates']]
            for ln in lines:
                prev = None; prev_coord = None
                for v in ln:
                    lng, lat = float(v[0]), float(v[1])
                    q = quant(lat, lng)
                    node_coord.setdefault(q, (lat, lng))
                    if prev is not None:
                        d = hav(prev_coord, (lat, lng))
                        edges[prev].append((q, d))
                        edges[q].append((prev, d))
                    prev, prev_coord = q, (lat, lng)
    print(f"  graph (in-line): {len(node_coord):,} nodes, {sum(len(v) for v in edges.values()):,} edges  ({time.time()-t0:.1f}s)")

    # Junction join: nearby vertices from different LineStrings get
    # cross-connected. A 1-metre spatial join is the standard tolerance
    # for TSW rail data — junctions are stored as separate LineString
    # endpoints whose float coords match within ~mm but not exactly.
    JOIN_M = 1.5
    LAT_DEG_PER_M = 1 / 111000.0
    # At ~42° lat, 1° lng ≈ 82.5 km. Grid cell ~ JOIN_M.
    cell_lat = LAT_DEG_PER_M * JOIN_M
    mid_lat = sum(c[0] for c in node_coord.values()) / len(node_coord)
    lng_per_m = 1 / (111000.0 * math.cos(math.radians(mid_lat)))
    cell_lng = lng_per_m * JOIN_M
    grid = defaultdict(list)
    for q, (lat, lng) in node_coord.items():
        gx = int(lat / cell_lat); gy = int(lng / cell_lng)
        grid[(gx, gy)].append(q)
    print(f"  grid cells: {len(grid):,}")

    added = 0
    for q, (lat, lng) in node_coord.items():
        gx = int(lat / cell_lat); gy = int(lng / cell_lng)
        for dx in (-1, 0, 1):
            for dy in (-1, 0, 1):
                for q2 in grid.get((gx+dx, gy+dy), ()):
                    if q2 == q: continue
                    c2 = node_coord[q2]
                    d = hav((lat, lng), c2)
                    if d <= JOIN_M:
                        # Only add edge once per direction to avoid
                        # quadratic explosion on dense clusters.
                        edges[q].append((q2, d))
                        added += 1
    print(f"  +{added:,} junction edges  ({time.time()-t0:.1f}s)")

    # Dijkstra between consecutive waypoints, concatenating the paths
    def find(target):
        """Find quant key for the rail vertex closest to `target`."""
        best, bestd = None, math.inf
        for q, c in node_coord.items():
            d = (c[0]-target[0])**2 + (c[1]-target[1])**2
            if d < bestd: bestd, best = d, q
        return best, math.sqrt(bestd)*111000

    def dijkstra(src, dst):
        dist = {src: 0.0}
        prev = {src: None}
        pq = [(0.0, src)]
        while pq:
            d, u = heapq.heappop(pq)
            if u == dst: break
            if d > dist[u]: continue
            for v, w in edges[u]:
                nd = d + w
                if nd < dist.get(v, math.inf):
                    dist[v] = nd
                    prev[v] = u
                    heapq.heappush(pq, (nd, v))
        if dst not in prev: return None
        out = []
        u = dst
        while u is not None:
            out.append(u)
            u = prev[u]
        return list(reversed(out)), dist.get(dst, math.inf)

    # Build the path by routing between consecutive waypoints
    full_path = []
    for i in range(len(waypoints) - 1):
        a, b = waypoints[i], waypoints[i+1]
        src_q, src_d = find((a[0], a[1]))
        dst_q, dst_d = find((b[0], b[1]))
        print(f"\nsegment {i}: ({a[0]:.5f},{a[1]:.5f}) -> ({b[0]:.5f},{b[1]:.5f})")
        print(f"  snap: src @{src_d:.2f}m, dst @{dst_d:.2f}m")
        ts = time.time()
        result = dijkstra(src_q, dst_q)
        print(f"  dijkstra: {time.time()-ts:.2f}s")
        if not result:
            print(f"  NO PATH FOUND")
            continue
        path_nodes, total_d = result
        print(f"  found path: {len(path_nodes)} nodes, {total_d/1000:.2f} km")
        for q in path_nodes:
            lat, lng = node_coord[q]
            full_path.append({'latitude': lat, 'longitude': lng})

    print(f"\ntotal computed path: {len(full_path)} points")
    if not full_path:
        print("aborting — no path computed")
        return

    # Write back to timetable_coordinates
    blob = json.dumps(full_path, separators=(',', ':'))
    cur.execute("""INSERT OR REPLACE INTO timetable_coordinates
                   (timetable_id, coordinates, coord_source)
                   VALUES (?, ?, 'rail-graph-backfill')""", (TID, blob))
    db.commit()
    print(f"saved {len(blob):,} bytes to timetable_coordinates")
    print(f"total time: {time.time()-t0:.1f}s")

if __name__ == '__main__':
    main()
