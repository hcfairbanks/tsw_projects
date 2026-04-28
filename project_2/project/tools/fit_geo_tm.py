"""Test whether the game uses Transverse Mercator.

For each tile-transition boundary, compute TM_forward(lat, lng) with the route
origin as the central meridian. If the game uses TM, the result should land
exactly at the expected world position (tile index * 1000 + 500 for center,
or tile index * 1000 for corner).
"""
import json, math, sys, os


# WGS84
A = 6378137.0
F = 1.0 / 298.257223563
E2 = F * (2 - F)
EP2 = E2 / (1 - E2)


def meridional_arc(phi):
    """Meridional arc distance from equator to latitude phi (rad), WGS84."""
    return A * ((1 - E2/4 - 3*E2*E2/64 - 5*E2**3/256) * phi
              - (3*E2/8 + 3*E2*E2/32 + 45*E2**3/1024) * math.sin(2*phi)
              + (15*E2*E2/256 + 45*E2**3/1024) * math.sin(4*phi)
              - (35*E2**3/3072) * math.sin(6*phi))


def tm_forward(lat_deg, lng_deg, lat0_deg, lng0_deg, k0=1.0):
    """TM forward: (lat,lng) → (east_m, north_m) relative to origin."""
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


def tm_inverse(x_m, y_m, lat0_deg, lng0_deg, k0=1.0):
    """TM inverse: (east_m, north_m) + origin → (lat, lng) in degrees."""
    B0 = math.radians(lat0_deg)
    L0 = math.radians(lng0_deg)
    M0 = meridional_arc(B0)
    M = M0 + y_m / k0
    mu = M / (A * (1 - E2/4 - 3*E2*E2/64 - 5*E2**3/256))
    e1 = (1 - math.sqrt(1 - E2)) / (1 + math.sqrt(1 - E2))
    phi1 = (mu + (3*e1/2 - 27*e1**3/32) * math.sin(2*mu)
                + (21*e1*e1/16 - 55*e1**4/32) * math.sin(4*mu)
                + (151*e1**3/96) * math.sin(6*mu)
                + (1097*e1**4/512) * math.sin(8*mu))
    C1 = EP2 * math.cos(phi1)**2
    T1 = math.tan(phi1)**2
    N1 = A / math.sqrt(1 - E2 * math.sin(phi1)**2)
    R1 = A * (1 - E2) / (1 - E2 * math.sin(phi1)**2)**1.5
    D = x_m / (N1 * k0)
    lat = phi1 - (N1 * math.tan(phi1) / R1) * (D*D/2
            - (5 + 3*T1 + 10*C1 - 4*C1*C1 - 9*EP2) * D**4 / 24
            + (61 + 90*T1 + 298*C1 + 45*T1*T1 - 252*EP2 - 3*C1*C1) * D**6 / 720)
    lng = L0 + (D - (1 + 2*T1 + C1) * D**3 / 6
            + (5 - 2*C1 + 28*T1 - 3*C1*C1 + 8*EP2 + 24*T1*T1) * D**5 / 120) / math.cos(phi1)
    return math.degrees(lat), math.degrees(lng)


def load_coords(path):
    with open(path, encoding='utf-8') as f:
        doc = json.load(f)
    return [
        {'lat': c['latitude'], 'lng': c['longitude'], 'tx': c['x'], 'ty': c['y']}
        for c in doc.get('coordinates', [])
        if c.get('latitude') is not None
    ]


def boundaries(samples):
    out, prev = [], None
    for s in samples:
        if prev and (prev['tx'], prev['ty']) != (s['tx'], s['ty']):
            out.append({
                'pre_tile': (prev['tx'], prev['ty']),
                'post_tile': (s['tx'], s['ty']),
                'mid_lat': (prev['lat'] + s['lat']) / 2,
                'mid_lng': (prev['lng'] + s['lng']) / 2,
                'dx': s['tx'] - prev['tx'],
                'dy': s['ty'] - prev['ty'],
            })
        prev = s
    return out


def test_route(label, path, origin_lat, origin_lng):
    samples = load_coords(path)
    bs = boundaries(samples)
    print(f'\n=== {label} — origin ({origin_lat:.5f}, {origin_lng:.5f}) — {len(bs)} boundaries ===')
    # For each boundary, compute TM_forward and compare to expected boundary position
    for conv in ('center', 'corner'):
        print(f'\n  -- {conv} convention — residual of boundary tile mid-point '
              f'vs TM forward prediction --')
        x_resid, y_resid = [], []
        for b in bs:
            tm_x, tm_y = tm_forward(b['mid_lat'], b['mid_lng'], origin_lat, origin_lng)
            # Note: TM forward y is NORTH, but tile.y is SOUTH. Flip sign.
            tm_south = -tm_y
            if b['dx'] != 0 and b['dy'] == 0:
                if conv == 'corner':
                    expected_x = b['post_tile'][0] * 1000 if b['dx'] > 0 else b['pre_tile'][0] * 1000
                else:
                    expected_x = (b['post_tile'][0] - 0.5) * 1000 if b['dx'] > 0 else (b['pre_tile'][0] - 0.5) * 1000
                r = tm_x - expected_x
                x_resid.append(r)
                print(f'    X-transition {b["pre_tile"]}→{b["post_tile"]}: '
                      f'TM_east={tm_x:+.2f}  expected={expected_x:+.1f}  residual={r:+.2f}m')
            elif b['dy'] != 0 and b['dx'] == 0:
                if conv == 'corner':
                    expected_y = b['post_tile'][1] * 1000 if b['dy'] > 0 else b['pre_tile'][1] * 1000
                else:
                    expected_y = (b['post_tile'][1] - 0.5) * 1000 if b['dy'] > 0 else (b['pre_tile'][1] - 0.5) * 1000
                r = tm_south - expected_y
                y_resid.append(r)
                print(f'    Y-transition {b["pre_tile"]}→{b["post_tile"]}: '
                      f'TM_south={tm_south:+.2f}  expected={expected_y:+.1f}  residual={r:+.2f}m')
        if x_resid:
            rms = (sum(r*r for r in x_resid)/len(x_resid))**0.5
            print(f'    X: RMS={rms:.2f}m  max={max(abs(r) for r in x_resid):.2f}m  mean={sum(x_resid)/len(x_resid):+.2f}m')
        if y_resid:
            rms = (sum(r*r for r in y_resid)/len(y_resid))**0.5
            print(f'    Y: RMS={rms:.2f}m  max={max(abs(r) for r in y_resid):.2f}m  mean={sum(y_resid)/len(y_resid):+.2f}m')


if __name__ == '__main__':
    test_route('BP (1.json)', r'C:/Users/hcfai/Desktop/1.json',
               42.3519401550293, -71.05528259277344)
    test_route('IoW (2.json)', r'C:/Users/hcfai/Desktop/2.json',
               50.678375244140625, -1.1386040449142456)
