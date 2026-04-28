"""Fit multiple projection models to recorded (lat, lng, tile.x, tile.y) data.

Goal: find the exact function the game uses to convert world coords to lat/lng.
If a simple closed-form model fits to <10m residual, we're done.

Approach:
  - For each tile transition, the world coordinate at the transition midpoint
    is known (either at N*1000 or (N+0.5)*1000, depending on convention).
  - Fit lat/lng to world coords using candidate projections.
  - Report residual per model.
"""
import json
import math
import sys
from statistics import mean, stdev


def load_coords(path):
    """Return list of dicts with keys lat, lng, tx, ty."""
    with open(path, encoding='utf-8') as f:
        doc = json.load(f)
    out = []
    for c in doc.get('coordinates', []):
        if c.get('latitude') is None:
            continue
        out.append({
            'lat': c['latitude'],
            'lng': c['longitude'],
            'tx': c['x'],
            'ty': c['y'],
        })
    return out


def find_boundaries(samples):
    """Find midpoints of tile transitions. Returns list of dicts with
    pre/post tile + midpoint lat/lng."""
    out = []
    prev = None
    for s in samples:
        if prev is not None and (prev['tx'], prev['ty']) != (s['tx'], s['ty']):
            mid_lat = (prev['lat'] + s['lat']) / 2
            mid_lng = (prev['lng'] + s['lng']) / 2
            out.append({
                'pre_tile': (prev['tx'], prev['ty']),
                'post_tile': (s['tx'], s['ty']),
                'mid_lat': mid_lat,
                'mid_lng': mid_lng,
                'dx': s['tx'] - prev['tx'],
                'dy': s['ty'] - prev['ty'],
            })
        prev = s
    return out


def infer_origin(samples):
    """Use the sample closest to (0,0) tile as an estimate of origin geometry."""
    return (mean(s['lat'] for s in samples), mean(s['lng'] for s in samples))


def fit_linear(boundaries, convention='center'):
    """Fit lat = a + b * world_south and lng = c + d * world_east, where
    world_south and world_east are the boundary positions in metres from
    route origin. Returns (b, d) representing the effective 1/m_per_deg_lat
    and 1/m_per_deg_lng — i.e. degrees per metre.

    We find the relationship between lat and world_south using a least-squares
    fit on boundary crossings whose world_south is known from tile arithmetic.
    """
    # Expected world position at a boundary:
    #   corner convention: boundary between N and N+1 at x = (N+1) * 1000 — no wait,
    #     corner: tile N covers [N*1000, (N+1)*1000], boundary N/N+1 at (N+1)*1000
    #   center convention: tile N covers [(N-0.5)*1000, (N+0.5)*1000], boundary at (N+0.5)*1000
    # For X transition dx=+1 (tile goes from N to N+1, moving east):
    #   corner: boundary at (N+1)*1000 = post_tx * 1000
    #   center: boundary at (N+0.5)*1000 = (post_tx - 0.5) * 1000
    # For dx=-1 (moving west, N to N-1):
    #   corner: boundary at N*1000 = pre_tx * 1000
    #   center: boundary at (N-0.5)*1000 = (pre_tx - 0.5) * 1000
    xs_m, xs_lng, ys_m, ys_lat = [], [], [], []
    for b in boundaries:
        if b['dx'] != 0 and b['dy'] == 0:
            # X transition
            if convention == 'corner':
                x_m = b['post_tile'][0] * 1000 if b['dx'] > 0 else b['pre_tile'][0] * 1000
            else:  # center
                if b['dx'] > 0:
                    x_m = (b['post_tile'][0] - 0.5) * 1000
                else:
                    x_m = (b['pre_tile'][0] - 0.5) * 1000
            xs_m.append(x_m)
            xs_lng.append(b['mid_lng'])
        elif b['dy'] != 0 and b['dx'] == 0:
            # Y transition
            if convention == 'corner':
                y_m = b['post_tile'][1] * 1000 if b['dy'] > 0 else b['pre_tile'][1] * 1000
            else:
                if b['dy'] > 0:
                    y_m = (b['post_tile'][1] - 0.5) * 1000
                else:
                    y_m = (b['pre_tile'][1] - 0.5) * 1000
            ys_m.append(y_m)
            ys_lat.append(b['mid_lat'])
    return xs_m, xs_lng, ys_m, ys_lat


def least_sq_slope(xs, ys):
    """Simple OLS: y = a + b*x. Returns (a, b)."""
    n = len(xs)
    if n < 2:
        return None, None
    sx, sy = sum(xs), sum(ys)
    sxx = sum(x * x for x in xs)
    sxy = sum(x * y for x, y in zip(xs, ys))
    denom = n * sxx - sx * sx
    if denom == 0:
        return None, None
    b = (n * sxy - sx * sy) / denom
    a = (sy - b * sx) / n
    return a, b


def evaluate(name, path):
    samples = load_coords(path)
    boundaries = find_boundaries(samples)
    print(f'\n=== {name} ({path}) ===')
    print(f'  Samples: {len(samples)}  Boundaries: {len(boundaries)}')

    for conv in ('center', 'corner'):
        xs_m, xs_lng, ys_m, ys_lat = fit_linear(boundaries, conv)
        print(f'\n  -- {conv} convention --')
        if len(xs_m) >= 2:
            a_lng, b_lng = least_sq_slope(xs_m, xs_lng)
            m_per_deg_lng = 1.0 / b_lng if b_lng else float('inf')
            # Residuals
            preds = [a_lng + b_lng * x for x in xs_m]
            resids_m = [(p - o) * m_per_deg_lng for p, o in zip(preds, xs_lng)]
            rms_m = (sum(r * r for r in resids_m) / len(resids_m)) ** 0.5
            print(f'  lng fit: {len(xs_m)} pts → m_per_deg_lng={m_per_deg_lng:.2f}  '
                  f'lng0={a_lng:.6f}  resid RMS={rms_m:.1f}m  max={max(abs(r) for r in resids_m):.1f}m')
        if len(ys_m) >= 2:
            a_lat, b_lat = least_sq_slope(ys_m, ys_lat)
            m_per_deg_lat = -1.0 / b_lat if b_lat else float('inf')  # negative because +south
            preds = [a_lat + b_lat * y for y in ys_m]
            resids_m = [(p - o) * abs(m_per_deg_lat) for p, o in zip(preds, ys_lat)]
            rms_m = (sum(r * r for r in resids_m) / len(resids_m)) ** 0.5
            print(f'  lat fit: {len(ys_m)} pts → m_per_deg_lat={m_per_deg_lat:.2f}  '
                  f'lat0={a_lat:.6f}  resid RMS={rms_m:.1f}m  max={max(abs(r) for r in resids_m):.1f}m')


if __name__ == '__main__':
    paths = sys.argv[1:] or [
        r'C:/Users/hcfai/Desktop/1.json',
        r'C:/Users/hcfai/Desktop/2.json',
    ]
    for p in paths:
        import os
        evaluate(os.path.basename(p), p)
