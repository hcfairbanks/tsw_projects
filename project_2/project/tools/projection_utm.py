"""Test proper UTM (Universal Transverse Mercator, k0=0.9996, standard 6° zones).

If TSW uses UTM: for each route, each sample's (lat,lng) → UTM easting/northing.
Relative to origin's UTM easting/northing, we get world coords.
Tile.x and tile.y should equal floor(world / 1000) or similar.
"""
import json, math, sys
from dataclasses import dataclass


A = 6378137.0
F = 1.0 / 298.257223563
E2 = F * (2 - F)
EP2 = E2 / (1 - E2)
K0_UTM = 0.9996
FALSE_EASTING = 500000.0


def meridional_arc(phi):
    return A * ((1 - E2/4 - 3*E2*E2/64 - 5*E2**3/256) * phi
              - (3*E2/8 + 3*E2*E2/32 + 45*E2**3/1024) * math.sin(2*phi)
              + (15*E2*E2/256 + 45*E2**3/1024) * math.sin(4*phi)
              - (35*E2**3/3072) * math.sin(6*phi))


def utm_zone(lng_deg):
    z = int((lng_deg + 180) / 6) + 1
    return z


def utm_cm(zone):
    return -177 + (zone - 1) * 6


def tm_forward(lat_deg, lng_deg, lat0_deg, lng0_deg, k0):
    B = math.radians(lat_deg)
    L = math.radians(lng_deg)
    B0 = math.radians(lat0_deg)
    L0 = math.radians(lng0_deg)
    N = A / math.sqrt(1 - E2 * math.sin(B)**2)
    T = math.tan(B)**2
    C = EP2 * math.cos(B)**2
    Aa = (L - L0) * math.cos(B)
    M = meridional_arc(B)
    M0 = meridional_arc(B0)
    x = k0 * N * (Aa
                  + (1 - T + C) * Aa**3 / 6
                  + (5 - 18*T + T*T + 72*C - 58*EP2) * Aa**5 / 120)
    y = k0 * (M - M0 + N * math.tan(B) * (Aa*Aa/2
                  + (5 - T + 9*C + 4*C*C) * Aa**4 / 24
                  + (61 - 58*T + T*T + 600*C - 330*EP2) * Aa**6 / 720))
    return x, y


def utm_forward(lat, lng, zone):
    """Standard UTM forward. Returns (easting, northing)."""
    cm = utm_cm(zone)
    x, y = tm_forward(lat, lng, 0.0, cm, K0_UTM)
    easting = x + FALSE_EASTING
    if lat >= 0:
        northing = y
    else:
        northing = y + 10_000_000
    return easting, northing


def load_coords(path):
    with open(path, encoding='utf-8') as f:
        doc = json.load(f)
    return [
        {'lat': c['latitude'], 'lng': c['longitude'], 'tx': c['x'], 'ty': c['y']}
        for c in doc.get('coordinates', []) if c.get('latitude') is not None
    ]


def boundaries(samples):
    out, prev = [], None
    for s in samples:
        if prev and (prev['tx'], prev['ty']) != (s['tx'], s['ty']):
            out.append({
                'pre_tx': prev['tx'], 'pre_ty': prev['ty'],
                'post_tx': s['tx'], 'post_ty': s['ty'],
                'mid_lat': (prev['lat'] + s['lat']) / 2,
                'mid_lng': (prev['lng'] + s['lng']) / 2,
                'dx': s['tx'] - prev['tx'],
                'dy': s['ty'] - prev['ty'],
            })
        prev = s
    return out


@dataclass
class Route:
    name: str
    path: str
    origin_lat: float
    origin_lng: float


ROUTES = [
    Route('BP ', r'C:/Users/hcfai/Desktop/1.json', 42.3519401550293, -71.05528259277344),
    Route('IoW', r'C:/Users/hcfai/Desktop/2.json', 50.678375244140625, -1.1386040449142456),
]


def run():
    for r in ROUTES:
        zone = utm_zone(r.origin_lng)
        cm = utm_cm(zone)
        print(f'\n=== {r.name} — origin ({r.origin_lat:.5f}, {r.origin_lng:.5f}) — UTM zone {zone}N (CM {cm}°E) ===')
        orig_e, orig_n = utm_forward(r.origin_lat, r.origin_lng, zone)
        print(f'  origin UTM: E={orig_e:.1f}  N={orig_n:.1f}')

        samples = load_coords(r.path)
        bs = boundaries(samples)
        for conv in ('center', 'corner'):
            print(f'\n  -- {conv} convention --')
            x_errs, y_errs = [], []
            for b in bs:
                e, n = utm_forward(b['mid_lat'], b['mid_lng'], zone)
                world_east = e - orig_e
                world_south = orig_n - n  # north → south flip
                if b['dx'] != 0 and b['dy'] == 0:
                    if conv == 'corner':
                        expected = b['post_tx'] * 1000 if b['dx'] > 0 else b['pre_tx'] * 1000
                    else:
                        expected = ((b['post_tx'] - 0.5) * 1000) if b['dx'] > 0 else ((b['pre_tx'] - 0.5) * 1000)
                    x_errs.append(world_east - expected)
                if b['dy'] != 0 and b['dx'] == 0:
                    if conv == 'corner':
                        expected = b['post_ty'] * 1000 if b['dy'] > 0 else b['pre_ty'] * 1000
                    else:
                        expected = ((b['post_ty'] - 0.5) * 1000) if b['dy'] > 0 else ((b['pre_ty'] - 0.5) * 1000)
                    y_errs.append(world_south - expected)
            def stat(name, lst):
                if not lst:
                    print(f'  {name}: no samples')
                    return
                rms = (sum(v*v for v in lst)/len(lst))**0.5
                print(f'  {name}: n={len(lst)} mean={sum(lst)/len(lst):+.1f}m RMS={rms:.1f}m max={max(abs(v) for v in lst):.1f}m')
            stat('X', x_errs)
            stat('Y', y_errs)


if __name__ == '__main__':
    run()
