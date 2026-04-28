"""Step 1: test multiple projection candidates against BP + IoW recording data.

Each candidate takes (lat, lng, origin_lat, origin_lng) → (x, y).
We compare to expected world position at boundary transitions and report
residual per candidate.

Winners: any projection that gives sub-10m residuals consistently on BOTH routes
with no route-specific parameters beyond origin_lat/lng.
"""
import json, math, os, sys
from dataclasses import dataclass


# ========== WGS84 & ellipsoid utilities ==========
A = 6378137.0
F = 1.0 / 298.257223563
E2 = F * (2 - F)
EP2 = E2 / (1 - E2)


def wgs84_m_per_deg_lat(phi_rad):
    M = A * (1 - E2) / (1 - E2 * math.sin(phi_rad) ** 2) ** 1.5
    return M * math.pi / 180


def wgs84_m_per_deg_lng(phi_rad):
    N = A / math.sqrt(1 - E2 * math.sin(phi_rad) ** 2)
    return N * math.cos(phi_rad) * math.pi / 180


# ========== Candidate projections ==========


def equirectangular_wgs84(lat, lng, lat0, lng0):
    """Flat Earth using WGS84 ellipsoidal m/deg at origin."""
    phi0 = math.radians(lat0)
    mlat = wgs84_m_per_deg_lat(phi0)
    mlng = wgs84_m_per_deg_lng(phi0)
    return (lng - lng0) * mlng, (lat - lat0) * mlat  # (east, north)


def equirectangular_spherical(R):
    """Closure: spherical Earth equirectangular with a given radius (metres)."""
    def f(lat, lng, lat0, lng0):
        phi0 = math.radians(lat0)
        mlat = R * math.pi / 180
        mlng = R * math.cos(phi0) * math.pi / 180
        return (lng - lng0) * mlng, (lat - lat0) * mlat
    return f


def lambert_conformal_conic_tangent(lat, lng, lat0, lng0):
    """LCC with one standard parallel equal to latitude of origin (tangent case).
    Uses WGS84 ellipsoid."""
    phi = math.radians(lat)
    lam = math.radians(lng)
    phi0 = math.radians(lat0)
    lam0 = math.radians(lng0)
    e = math.sqrt(E2)

    n = math.sin(phi0)

    def t(p):
        return math.tan(math.pi / 4 - p / 2) / ((1 - e * math.sin(p)) / (1 + e * math.sin(p))) ** (e / 2)

    def m(p):
        return math.cos(p) / math.sqrt(1 - E2 * math.sin(p) ** 2)

    F_const = m(phi0) / (n * t(phi0) ** n)
    rho0 = A * F_const * t(phi0) ** n
    rho = A * F_const * t(phi) ** n
    theta = n * (lam - lam0)
    x = rho * math.sin(theta)
    y = rho0 - rho * math.cos(theta)
    return x, y


def oblique_stereographic(lat, lng, lat0, lng0):
    """Oblique stereographic on WGS84 ellipsoid (approximate, often used by UK OS)."""
    phi = math.radians(lat)
    lam = math.radians(lng)
    phi0 = math.radians(lat0)
    lam0 = math.radians(lng0)
    e = math.sqrt(E2)

    def chi(p):
        # conformal latitude
        sp = math.sin(p)
        return 2 * math.atan(math.tan(math.pi/4 + p/2) * ((1 - e*sp)/(1 + e*sp)) ** (e/2)) - math.pi/2

    chi0 = chi(phi0)
    chi_p = chi(phi)
    dlam = lam - lam0
    k = 2 / (1 + math.sin(chi0) * math.sin(chi_p) + math.cos(chi0) * math.cos(chi_p) * math.cos(dlam))
    R = A / math.sqrt(1 - E2 * math.sin(phi0) ** 2)  # local radius
    x = R * k * math.cos(chi_p) * math.sin(dlam)
    y = R * k * (math.cos(chi0) * math.sin(chi_p) - math.sin(chi0) * math.cos(chi_p) * math.cos(dlam))
    return x, y


# ========== Data loading ==========


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


def expected_world(b, conv='center'):
    """Return (expected_east, expected_south) for boundary b, or None-tuple if other axis."""
    ex, sy = None, None
    if b['dx'] != 0 and b['dy'] == 0:
        if conv == 'corner':
            ex = b['post_tx'] * 1000 if b['dx'] > 0 else b['pre_tx'] * 1000
        else:  # center
            ex = ((b['post_tx'] - 0.5) * 1000) if b['dx'] > 0 else ((b['pre_tx'] - 0.5) * 1000)
    if b['dy'] != 0 and b['dx'] == 0:
        if conv == 'corner':
            sy = b['post_ty'] * 1000 if b['dy'] > 0 else b['pre_ty'] * 1000
        else:
            sy = ((b['post_ty'] - 0.5) * 1000) if b['dy'] > 0 else ((b['pre_ty'] - 0.5) * 1000)
    return ex, sy


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


def test_projection(proj_name, proj_fn, convs=('center', 'corner')):
    """Run projection over all routes, report residuals per route per convention."""
    print(f'\n=== {proj_name} ===')
    for r in ROUTES:
        samples = load_coords(r.path)
        bs = boundaries(samples)
        best_line = None
        for conv in convs:
            x_errs, y_errs = [], []
            for b in bs:
                exp_e, exp_s = expected_world(b, conv)
                x, y_north = proj_fn(b['mid_lat'], b['mid_lng'], r.origin_lat, r.origin_lng)
                y_south = -y_north
                if exp_e is not None:
                    x_errs.append(x - exp_e)
                if exp_s is not None:
                    y_errs.append(y_south - exp_s)
            x_rms = (sum(e*e for e in x_errs)/len(x_errs))**0.5 if x_errs else 0
            y_rms = (sum(e*e for e in y_errs)/len(y_errs))**0.5 if y_errs else 0
            x_max = max((abs(e) for e in x_errs), default=0)
            y_max = max((abs(e) for e in y_errs), default=0)
            line = (f'  {r.name} [{conv:>6}]  '
                    f'X RMS={x_rms:6.1f}m max={x_max:6.1f}m   '
                    f'Y RMS={y_rms:6.1f}m max={y_max:6.1f}m')
            print(line)


if __name__ == '__main__':
    test_projection('WGS84 equirectangular (origin cos)', equirectangular_wgs84)
    test_projection('Spherical R=6371000 (mean Earth)', equirectangular_spherical(6371000))
    test_projection('Spherical R=6378137 (equatorial)', equirectangular_spherical(6378137))
    test_projection('Spherical R=6356752 (polar)', equirectangular_spherical(6356752))
    test_projection('Lambert Conformal Conic (tangent, origin)', lambert_conformal_conic_tangent)
    test_projection('Oblique Stereographic on WGS84', oblique_stereographic)
