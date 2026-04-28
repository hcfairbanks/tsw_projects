"""Second-pass fit: try linear, quadratic, and Transverse Mercator projections.

Targets <10m residual.
"""
import json, math, sys, os
from statistics import mean


def load_coords(path):
    with open(path, encoding='utf-8') as f:
        doc = json.load(f)
    return [
        {'lat': c['latitude'], 'lng': c['longitude'], 'tx': c['x'], 'ty': c['y']}
        for c in doc.get('coordinates', [])
        if c.get('latitude') is not None
    ]


def boundaries(samples):
    out = []
    prev = None
    for s in samples:
        if prev and (prev['tx'], prev['ty']) != (s['tx'], s['ty']):
            out.append({
                'pre': prev, 'post': s,
                'mid_lat': (prev['lat'] + s['lat']) / 2,
                'mid_lng': (prev['lng'] + s['lng']) / 2,
                'dx': s['tx'] - prev['tx'],
                'dy': s['ty'] - prev['ty'],
            })
        prev = s
    return out


def world_at_boundary(b, conv='center'):
    """Return (world_east_m, world_south_m) assuming one axis crosses."""
    if b['dx'] != 0 and b['dy'] == 0:
        if conv == 'corner':
            x_m = b['post']['tx'] * 1000 if b['dx'] > 0 else b['pre']['tx'] * 1000
        else:
            x_m = ((b['post']['tx'] - 0.5) * 1000) if b['dx'] > 0 else ((b['pre']['tx'] - 0.5) * 1000)
        return x_m, None
    if b['dy'] != 0 and b['dx'] == 0:
        if conv == 'corner':
            y_m = b['post']['ty'] * 1000 if b['dy'] > 0 else b['pre']['ty'] * 1000
        else:
            y_m = ((b['post']['ty'] - 0.5) * 1000) if b['dy'] > 0 else ((b['pre']['ty'] - 0.5) * 1000)
        return None, y_m
    return None, None


def fit_poly(xs, ys, deg=1):
    """Fit y = sum(c_i * x^i). Returns coefficients [c0, c1, ..., c_deg] via
    normal equations (hand-rolled for small deg since no numpy)."""
    n = len(xs)
    if n < deg + 1:
        return None
    # Build Vandermonde rows, compute X^T X and X^T y
    cols = deg + 1
    XtX = [[0.0] * cols for _ in range(cols)]
    Xty = [0.0] * cols
    for x, y in zip(xs, ys):
        xp = [x ** i for i in range(cols)]
        for i in range(cols):
            Xty[i] += xp[i] * y
            for j in range(cols):
                XtX[i][j] += xp[i] * xp[j]
    # Gauss-Jordan solve XtX * c = Xty
    a = [row[:] + [Xty[i]] for i, row in enumerate(XtX)]
    for i in range(cols):
        # pivot
        mx = i
        for k in range(i + 1, cols):
            if abs(a[k][i]) > abs(a[mx][i]):
                mx = k
        a[i], a[mx] = a[mx], a[i]
        if abs(a[i][i]) < 1e-12:
            return None
        piv = a[i][i]
        for j in range(i, cols + 1):
            a[i][j] /= piv
        for k in range(cols):
            if k != i:
                factor = a[k][i]
                for j in range(i, cols + 1):
                    a[k][j] -= factor * a[i][j]
    return [a[i][cols] for i in range(cols)]


def eval_poly(coefs, x):
    return sum(c * (x ** i) for i, c in enumerate(coefs))


def try_fits(path, label):
    samples = load_coords(path)
    B = boundaries(samples)
    print(f'\n=== {label} — {len(samples)} samples, {len(B)} boundaries ===')

    for conv in ('center', 'corner'):
        xs_m, xs_lng, ys_m, ys_lat = [], [], [], []
        for b in B:
            xw, yw = world_at_boundary(b, conv)
            if xw is not None:
                xs_m.append(xw); xs_lng.append(b['mid_lng'])
            if yw is not None:
                ys_m.append(yw); ys_lat.append(b['mid_lat'])
        print(f'\n  -- {conv} convention --')
        for deg in (1, 2, 3):
            if len(xs_m) >= deg + 1:
                coefs = fit_poly(xs_m, xs_lng, deg)
                if coefs:
                    # approximate 1 deg lng ≈ 70_000m at these lats for residual units
                    # use the local m/deg via linear slope
                    lng_slope_m_per_deg = 1.0 / coefs[1] if len(coefs) > 1 and coefs[1] else 70000
                    preds = [eval_poly(coefs, x) for x in xs_m]
                    resid_m = [(o - p) * lng_slope_m_per_deg for p, o in zip(preds, xs_lng)]
                    rms = (sum(r * r for r in resid_m) / len(resid_m)) ** 0.5
                    mx = max(abs(r) for r in resid_m)
                    # Print full coefficient list
                    pretty = [f'{c:.6g}' for c in coefs]
                    print(f'  lng deg={deg}: n={len(xs_m)} RMS={rms:.1f}m max={mx:.1f}m  coefs={pretty}')
            if len(ys_m) >= deg + 1:
                coefs = fit_poly(ys_m, ys_lat, deg)
                if coefs:
                    lat_slope_m_per_deg = abs(1.0 / coefs[1]) if len(coefs) > 1 and coefs[1] else 111000
                    preds = [eval_poly(coefs, y) for y in ys_m]
                    resid_m = [(o - p) * lat_slope_m_per_deg for p, o in zip(preds, ys_lat)]
                    rms = (sum(r * r for r in resid_m) / len(resid_m)) ** 0.5
                    mx = max(abs(r) for r in resid_m)
                    pretty = [f'{c:.6g}' for c in coefs]
                    print(f'  lat deg={deg}: n={len(ys_m)} RMS={rms:.1f}m max={mx:.1f}m  coefs={pretty}')

    # Dump raw boundary list for manual inspection
    print('\n  Raw boundaries (for manual look):')
    for b in B[:25]:
        print(f"    pre={b['pre']['tx']:+3d},{b['pre']['ty']:+3d}  post={b['post']['tx']:+3d},{b['post']['ty']:+3d}  "
              f"mid_lat={b['mid_lat']:.6f}  mid_lng={b['mid_lng']:.6f}")


if __name__ == '__main__':
    paths = sys.argv[1:] or [
        r'C:/Users/hcfai/Desktop/1.json',
        r'C:/Users/hcfai/Desktop/2.json',
    ]
    for p in paths:
        try_fits(p, os.path.basename(p))
