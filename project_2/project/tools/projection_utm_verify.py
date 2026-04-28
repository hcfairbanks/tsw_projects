"""Definitive UTM validation: for EVERY sample in a route, compute UTM forward
from (lat, lng) and check:
  1. Computed tile (from world_east/south) matches API-reported tile
  2. Within-tile offset stays in the [-500, +500] range (tile-center convention)
"""
import json, math, sys
from dataclasses import dataclass


A = 6378137.0
F = 1.0 / 298.257223563
E2 = F * (2 - F)
EP2 = E2 / (1 - E2)
K0 = 0.9996
FE = 500000.0


def meridional_arc(phi):
    return A * ((1 - E2/4 - 3*E2*E2/64 - 5*E2**3/256) * phi
              - (3*E2/8 + 3*E2*E2/32 + 45*E2**3/1024) * math.sin(2*phi)
              + (15*E2*E2/256 + 45*E2**3/1024) * math.sin(4*phi)
              - (35*E2**3/3072) * math.sin(6*phi))


def utm_zone(lng_deg):
    return int((lng_deg + 180) / 6) + 1


def utm_cm(zone):
    return -177 + (zone - 1) * 6


def tm_forward(lat_deg, lng_deg, lat0_deg, lng0_deg, k0):
    B = math.radians(lat_deg); L = math.radians(lng_deg)
    B0 = math.radians(lat0_deg); L0 = math.radians(lng0_deg)
    N = A / math.sqrt(1 - E2 * math.sin(B)**2)
    T = math.tan(B)**2
    C = EP2 * math.cos(B)**2
    Aa = (L - L0) * math.cos(B)
    M = meridional_arc(B)
    M0 = meridional_arc(B0)
    x = k0 * N * (Aa + (1 - T + C) * Aa**3 / 6
                  + (5 - 18*T + T*T + 72*C - 58*EP2) * Aa**5 / 120)
    y = k0 * (M - M0 + N * math.tan(B) * (Aa*Aa/2
                  + (5 - T + 9*C + 4*C*C) * Aa**4 / 24
                  + (61 - 58*T + T*T + 600*C - 330*EP2) * Aa**6 / 720))
    return x, y


def utm_forward(lat, lng, zone):
    cm = utm_cm(zone)
    x, y = tm_forward(lat, lng, 0.0, cm, K0)
    return x + FE, y  # easting, northing (NH)


@dataclass
class Route:
    name: str; path: str; olat: float; olng: float


ROUTES = [
    Route('BP ', r'C:/Users/hcfai/Desktop/1.json', 42.3519401550293, -71.05528259277344),
    Route('IoW', r'C:/Users/hcfai/Desktop/2.json', 50.678375244140625, -1.1386040449142456),
]


def run():
    for r in ROUTES:
        with open(r.path, encoding='utf-8') as f:
            doc = json.load(f)
        samples = [c for c in doc.get('coordinates', []) if c.get('latitude') is not None]
        zone = utm_zone(r.olng)
        cm = utm_cm(zone)
        orig_e, orig_n = utm_forward(r.olat, r.olng, zone)
        print(f'\n=== {r.name} — {len(samples)} samples — UTM zone {zone}N (CM {cm}°E) ===')

        tile_mismatches = 0
        wx_vals, wy_vals = [], []
        for s in samples:
            e, n = utm_forward(s['latitude'], s['longitude'], zone)
            world_east = e - orig_e
            world_south = orig_n - n
            # Tile-center convention: tile N center at N*1000, range [(N-0.5)*1000..(N+0.5)*1000]
            pred_tx = round(world_east / 1000)
            pred_ty = round(world_south / 1000)
            if (pred_tx, pred_ty) != (s['x'], s['y']):
                tile_mismatches += 1
            wx = world_east - s['x'] * 1000   # expected within-tile offset: (-500, +500]
            wy = world_south - s['y'] * 1000
            wx_vals.append(wx)
            wy_vals.append(wy)
        print(f'  Tile-index mismatches: {tile_mismatches} / {len(samples)} ({100*tile_mismatches/len(samples):.3f}%)')
        for name, vals in [('within-tile East (m)', wx_vals), ('within-tile South (m)', wy_vals)]:
            mn = min(vals); mx = max(vals)
            mean = sum(vals) / len(vals)
            # stddev
            var = sum((v-mean)**2 for v in vals) / len(vals)
            std = var ** 0.5
            print(f'  {name}: min={mn:+.1f} max={mx:+.1f} mean={mean:+.1f} std={std:.1f}')
        # Histogram of within-tile positions (50-sample buckets)
        # Not really needed — just verify range is within [-500, +500]
        if min(min(wx_vals), min(wy_vals)) >= -500 and max(max(wx_vals), max(wy_vals)) <= 500:
            print('  ✓ All samples within [-500, +500]m of tile center — tile-center convention CONFIRMED')


if __name__ == '__main__':
    run()
