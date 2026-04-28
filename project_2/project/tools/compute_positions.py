#!/usr/bin/env python3
"""
Propagate absolute world positions through the IOW ribbon network,
seeded by ribbons that carry a WorldLocation (~90 ribbons out of ~415).

For every ribbon we output:
  - start_world  (x, y) cm, absolute game coords of arc start
  - end_world    (x, y) cm, absolute game coords of arc end
  - stop_world   (x, y) cm, position at RibbonLocation = reasonable default (unused)
  - length_cm    arc length
  - lat, lng     start/end/stop transformed via the corrected formula

Then for each of the 15 timetable target GUIDs we print the final positions.
"""
import json, glob, os, math, sys
from collections import defaultdict

TILES_DIR = os.environ.get("TILES_DIR") or \
    next(iter(glob.glob("/tmp/tsw6-timetable-*/IsleOfWight/TS2Prototype/Plugins/DLC/IsleOfWight/Content/Map/Tiles")), None)
if not TILES_DIR:
    print("Set TILES_DIR env var or ensure a /tmp/tsw6-timetable-*/... tree exists")
    sys.exit(1)

# --------- geographic transformation (derived from RydePierHead/Esplanade/Shanklin) ---------
CENTRAL_LAT = 50.678375
CENTRAL_LNG = -1.138604
COS_LAT = math.cos(math.radians(CENTRAL_LAT))
M_PER_DEG_LAT = 111320.0
M_PER_DEG_LNG = M_PER_DEG_LAT * COS_LAT  # ~70468

def world_to_latlng(x_cm, y_cm):
    x_m = x_cm / 100.0
    y_m = y_cm / 100.0
    e_m = 2.5494 * x_m + 0.12569 * y_m + 3559.8
    n_m = 3.2908 * x_m - 0.92026 * y_m + 8676.4
    lat = CENTRAL_LAT + n_m / M_PER_DEG_LAT
    lng = CENTRAL_LNG + e_m / M_PER_DEG_LNG
    return lat, lng

# --------- arc end-point math ---------
def arc_delta(start_xy, tangent_xy, radius, length):
    """Return (end_xy - start_xy) in 2D for a circular arc (or straight line if radius is None)."""
    sx, sy = start_xy
    tx, ty = tangent_xy
    # Normalise tangent (should already be unit, but belt + braces)
    tn = math.hypot(tx, ty)
    if tn == 0:
        return (0.0, 0.0)
    tx, ty = tx / tn, ty / tn
    if radius is None or radius == 0:
        # Straight segment: end = start + length * tangent
        return (length * tx, length * ty)
    # Circular arc, UE4 convention (verified empirically against seeded ribbons):
    #   perpendicular to center = (+ty, -tx);  sweep = -length / radius.
    cx = sx + radius * ty
    cy = sy - radius * tx
    sweep = -length / radius
    # end = center + rot(sweep) * (start - center)
    dx, dy = sx - cx, sy - cy
    cos_s, sin_s = math.cos(sweep), math.sin(sweep)
    ex = cx + cos_s * dx - sin_s * dy
    ey = cy + sin_s * dx + cos_s * dy
    return (ex - sx, ey - sy)

def arc_point(start_xy, tangent_xy, radius, length, fraction):
    """Point along the arc at fraction in [0, 1]."""
    return arc_delta(start_xy, tangent_xy, radius, length * fraction)

# --------- ribbon indexing ---------
def load_ribbons():
    ribbons = {}
    for jf in sorted(glob.glob(os.path.join(TILES_DIR, "TT_x*.umap.json"))):
        try:
            with open(jf) as f:
                doc = json.load(f)
        except Exception:
            continue
        tile = os.path.basename(jf).replace('.umap.json', '')
        exps = doc.get('Exports', [])
        for exp in exps:
            if 'NetworkRibbon' not in exp.get('ObjectName', ''):
                continue
            data = exp.get('Data', [])
            if not isinstance(data, list):
                continue
            r = {
                'tile': tile,
                'obj_name': exp.get('ObjectName', ''),
                'guid': None, 'start_node': None, 'end_node': None,
                'curve_ref': None,
                'arc_sx': None, 'arc_sy': None,
                'arc_tx': None, 'arc_ty': None,
                'radius': None, 'length': None,
                'wl_x': None, 'wl_y': None,
            }
            for prop in data:
                pn = prop.get('Name', '')
                if pn == 'RibbonGuid':
                    v = prop.get('Value', [])
                    if v: r['guid'] = v[0].get('Value', '')
                elif pn == 'StartNodeGuid':
                    v = prop.get('Value', [])
                    if v: r['start_node'] = v[0].get('Value', '')
                elif pn == 'EndNodeGuid':
                    v = prop.get('Value', [])
                    if v: r['end_node'] = v[0].get('Value', '')
                elif pn == 'Curve':
                    r['curve_ref'] = prop.get('Value')
                elif pn == 'WorldLocation':
                    v = prop.get('Value', [])
                    if isinstance(v, list) and v:
                        inner = v[0].get('Value', {})
                        r['wl_x'] = inner.get('X')
                        r['wl_y'] = inner.get('Y')
            # Fill arc from Curve export
            if r['curve_ref'] and isinstance(r['curve_ref'], int):
                cidx = r['curve_ref'] - 1
                if 0 <= cidx < len(exps):
                    cdata = exps[cidx].get('Data', [])
                    for prop in cdata:
                        pn = prop.get('Name', '')
                        if pn == 'StartPosition2D':
                            v = prop.get('Value', [])
                            if isinstance(v, list) and v:
                                inner = v[0].get('Value', {})
                                r['arc_sx'] = inner.get('X')
                                r['arc_sy'] = inner.get('Y')
                        elif pn == 'StartTangent2D':
                            v = prop.get('Value', [])
                            if isinstance(v, list) and v:
                                inner = v[0].get('Value', {})
                                r['arc_tx'] = inner.get('X')
                                r['arc_ty'] = inner.get('Y')
                        elif pn == 'Radius':
                            r['radius'] = prop.get('Value')
                        elif pn == 'Length':
                            r['length'] = prop.get('Value')
            if r['guid'] is None or r['arc_sx'] is None or r['length'] is None:
                continue
            # A ribbon can appear in multiple tile files. Prefer one that has WorldLocation set.
            existing = ribbons.get(r['guid'])
            if existing is None or (r['wl_x'] is not None and existing['wl_x'] is None):
                ribbons[r['guid']] = r
    return ribbons

# --------- propagation ---------
def propagate(ribbons):
    """
    Fill in abs_start / abs_end for every ribbon by BFS through shared nodes,
    seeded by ribbons that carry a WorldLocation.
    """
    # Precompute arc delta (world coords are pure translation of local, so delta is the same)
    for r in ribbons.values():
        delta = arc_delta(
            (r['arc_sx'], r['arc_sy']),
            (r['arc_tx'] or 0.0, r['arc_ty'] or 0.0),
            r['radius'], r['length']
        )
        r['delta'] = delta
        r['abs_start'] = None
        r['abs_end'] = None
        if r['wl_x'] is not None:
            sx = r['wl_x'] + r['arc_sx']
            sy = r['wl_y'] + r['arc_sy']
            r['abs_start'] = (sx, sy)
            r['abs_end'] = (sx + delta[0], sy + delta[1])

    # Node index: node_guid -> [(ribbon_guid, 'start' | 'end'), ...]
    node_to_ribbons = defaultdict(list)
    for r in ribbons.values():
        if r['start_node']: node_to_ribbons[r['start_node']].append((r['guid'], 'start'))
        if r['end_node']:   node_to_ribbons[r['end_node']].append((r['guid'], 'end'))

    # Seed: collect known node positions from already-solved ribbons
    node_pos = {}
    def set_node(nid, xy):
        """Set a node position, return True if new, False if consistent, raise if inconsistent."""
        if nid in node_pos:
            dx = xy[0] - node_pos[nid][0]
            dy = xy[1] - node_pos[nid][1]
            if abs(dx) > 10.0 or abs(dy) > 10.0:
                # > 10 cm = 0.1 m discrepancy
                return None  # inconsistent
            return False
        node_pos[nid] = xy
        return True

    # Initial node positions from solved ribbons
    for r in ribbons.values():
        if r['abs_start'] is not None and r['start_node']:
            set_node(r['start_node'], r['abs_start'])
        if r['abs_end'] is not None and r['end_node']:
            set_node(r['end_node'], r['abs_end'])

    # Track how a ribbon's position was established
    node_source = {}  # node_id -> ribbon_guid that set it
    parent_of = {}    # ribbon_guid -> parent ribbon_guid (what provided its position)
    for r in ribbons.values():
        if r['abs_start'] is not None:
            r['hops'] = 0
            r['source'] = r['guid']
            if r['start_node'] and r['start_node'] not in node_source:
                node_source[r['start_node']] = r['guid']
            if r['end_node'] and r['end_node'] not in node_source:
                node_source[r['end_node']] = r['guid']
        else:
            r['hops'] = None
            r['source'] = None

    parent_ribbon = {}  # ribbon guid -> parent ribbon guid
    inconsistent_count = [0]
    worst_inconsistency = [0.0]
    def set_node2(nid, xy, tolerance=500.0):
        if nid in node_pos:
            dx = xy[0] - node_pos[nid][0]
            dy = xy[1] - node_pos[nid][1]
            err = abs(dx) + abs(dy)
            if err > tolerance:
                inconsistent_count[0] += 1
                if err > worst_inconsistency[0]: worst_inconsistency[0] = err
            return False
        node_pos[nid] = xy
        return True

    # BFS
    pending = set(g for g, r in ribbons.items() if r['abs_start'] is None)
    progress = True
    iters = 0
    while pending and progress:
        iters += 1
        progress = False
        solved_this_round = []
        for gid in list(pending):
            r = ribbons[gid]
            start_known = r['start_node'] in node_pos
            end_known = r['end_node'] in node_pos
            if start_known:
                r['abs_start'] = node_pos[r['start_node']]
                r['abs_end'] = (r['abs_start'][0] + r['delta'][0],
                                r['abs_start'][1] + r['delta'][1])
                parent = ribbons[node_source[r['start_node']]]
                r['hops'] = (parent['hops'] or 0) + 1
                r['source'] = parent['source']
                parent_ribbon[gid] = parent['guid']
                solved_this_round.append(gid)
            elif end_known:
                r['abs_end'] = node_pos[r['end_node']]
                r['abs_start'] = (r['abs_end'][0] - r['delta'][0],
                                  r['abs_end'][1] - r['delta'][1])
                parent = ribbons[node_source[r['end_node']]]
                r['hops'] = (parent['hops'] or 0) + 1
                r['source'] = parent['source']
                parent_ribbon[gid] = parent['guid']
                solved_this_round.append(gid)
        for gid in solved_this_round:
            r = ribbons[gid]
            pending.discard(gid)
            progress = True
            if r['start_node']:
                if set_node2(r['start_node'], r['abs_start']) is not False:
                    node_source.setdefault(r['start_node'], gid)
            if r['end_node']:
                if set_node2(r['end_node'], r['abs_end']) is not False:
                    node_source.setdefault(r['end_node'], gid)

    return ribbons, node_pos, iters, inconsistent_count[0], worst_inconsistency[0], parent_ribbon

# --------- main ---------
TARGETS = {
    '{01AF82CC-49A3-D11C-8DD8-1F89E108CF0E}': 'Ryde Pier Head Platform 1',
    '{E15474B4-4FC5-3F34-61FD-4F8AB1ED9FB2}': 'Ryde Esplanade Platform 1',
    '{699D307D-4685-6C48-6175-3E97AB90D54A}': "Ryde St. John's Road Platform 1",
    '{AC5BD2F6-4661-CB62-5B1D-5AA9E846B529}': "Ryde St. John's Road Platform 2",
    '{3299DBF1-4C38-627D-A05E-8A9A45FA81FC}': "Ryde St. John's Road Platform 3",
    '{EB7057A5-4C25-8B5C-8524-8EB2F3BADB03}': "Ryde St. John's Road Depot Siding 5",
    '{14B40CDD-4F91-61B4-6CE1-F48940040B11}': "Ryde St. John's Road Depot Siding 1",
    '{10073D8E-41BA-5800-1928-9A8982C63D46}': 'Smallbrook Junction Line 1',
    '{5146B6B3-46EB-7E0F-4EE5-E287D02A5BC8}': 'Smallbrook Junction Line 2',
    '{6C0EE0F5-4E2F-CD2F-03B9-268FE4D7D343}': 'Smallbrook Junction Platform 1',
    '{9F72F64A-49AB-E281-98AD-A9B7D6695940}': 'Shanklin Platform 1',
    '{68D9FAAD-4AD3-5330-3EAB-FC87CB22D520}': 'Brading Platform 1',
    '{2DB3D45D-4499-5D1C-77E3-309EAF31EC25}': 'Sandown Platform 1',
    '{C426B2F7-41DD-67C7-1413-02A0E22AB688}': 'Sandown Platform 2',
    '{B7702836-4A0B-1313-F58F-F0825392C033}': 'Lake Platform 1',
}

print(f"Tiles: {TILES_DIR}")
ribbons = load_ribbons()
print(f"Loaded {len(ribbons)} ribbons")
seeded = sum(1 for r in ribbons.values() if r['wl_x'] is not None)
print(f"Seeded (have WorldLocation): {seeded}")

ribbons, node_pos, iters, incons, worst, parent_ribbon = propagate(ribbons)
solved = sum(1 for r in ribbons.values() if r['abs_start'] is not None)
unsolved = len(ribbons) - solved
print(f"After BFS ({iters} iterations): solved={solved}, unsolved={unsolved}, nodes_positioned={len(node_pos)}")
print(f"Node-position inconsistencies during BFS (>5m): {incons}, worst = {worst:.1f} cm")

# Trace Brading's chain back to its seed
BRADING = '{68D9FAAD-4AD3-5330-3EAB-FC87CB22D520}'
print(f"\n=== Brading chain back to seed ===")
cur = BRADING
hop = 0
while cur is not None and hop < 70:
    r = ribbons[cur]
    sx, sy = r['abs_start']
    print(f"  hop {hop}: {cur[:14]} abs_start=({sx:>11.1f},{sy:>11.1f}) R={r['radius']} L={r['length']:.1f} seeded={r['wl_x'] is not None}")
    cur = parent_ribbon.get(cur)
    hop += 1

print("\n=== Target ribbon positions (game world + lat/lng) ===")
for guid, name in TARGETS.items():
    r = ribbons.get(guid)
    if r is None:
        print(f"{name:<40s}  NOT FOUND")
        continue
    if r['abs_start'] is None:
        print(f"{name:<40s}  UNRESOLVED")
        continue
    sx, sy = r['abs_start']
    ex, ey = r['abs_end']
    s_lat, s_lng = world_to_latlng(sx, sy)
    e_lat, e_lng = world_to_latlng(ex, ey)
    was_seeded = "SEED" if r['wl_x'] is not None else f"PROP hops={r.get('hops')} src={(r.get('source') or '')[:12]}"
    print(f"{name:<40s} [{was_seeded}] len={r['length']/100:>6.1f}m")
    print(f"    start: world=({sx:>11.1f},{sy:>11.1f})  lat/lng=({s_lat:.5f},{s_lng:.5f})")
    print(f"    end:   world=({ex:>11.1f},{ey:>11.1f})  lat/lng=({e_lat:.5f},{e_lng:.5f})")
PYEOF
