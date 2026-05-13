"""
tc_splines.py - Build a high-fidelity track GeoJSON for the TSW6 Training
Centre by reading SplineMeshComponent.SplineParams from every tile umap
and Hermite-sampling each segment densely.

Why: the production extractor reconstructs track geometry from the
NetworkRibbon's analytic-arc fields (StartPosition + Tangent + Radius +
Length). That introduces visible drift at junctions and faceted curves.
The umap files actually carry the *exact* spline UE renders the track
mesh along - start pos + start tangent + end pos + end tangent in cubic
Hermite form, one segment per visible track-mesh piece. Walking those
gives the geometry the engine itself uses.

Usage:
  python tc_splines.py [--out PATH] [--tiles-only LT_x0_y0,...]

Default output is the user's Desktop. The viewer at
file:///C:/Users/hcfai/Desktop/project_2/project/viewer/index.html
loads the resulting `.geojson` directly.

This is a one-off / Training-Centre-only scratch script. If the output
looks better than the production extractor, the next step is porting
the same approach to internal/extractor in Go.
"""

from __future__ import annotations
import argparse
import json
import math
import os
import shutil
import subprocess
import sys
import tempfile
import time
from pathlib import Path

# Hard-coded TSW install + route
TSW_PAK = Path(r"D:\SteamLibrary\steamapps\common\Train Sim World 6\WindowsNoEditor\TS2Prototype\Content\Paks\TS2Prototype-WindowsNoEditor-TrainingCentre-coredata.pak")
REPAK = Path(r"C:\Users\hcfai\Desktop\applications_2\hud-go\repak.exe")
UASSETGUI = Path(r"C:\Users\hcfai\Desktop\applications_2\hud-go\UAssetGUI.exe")

# Training Centre route anchor (from the existing extract's route_*.json).
ORIGIN_LAT = 51.11568832397461
ORIGIN_LNG = 6.209702968597412

# UE world space tile size in cm. TSW tiles are 1 km × 1 km.
TILE_CM = 100_000.0

# Hermite samples per spline segment. 32 keeps even tight S-curves smooth
# without bloating the file too much.
SAMPLES_PER_SEGMENT = 32


# ---------- UTM conversion ----------
# Lifted from internal/geo: per-route anchor with a single UTM zone.

def utm_zone(lng: float) -> int:
    return int(math.floor((lng + 180.0) / 6.0)) + 1


def latlng_to_utm(lat: float, lng: float, zone: int):
    """WGS-84 lat/lng -> (easting, northing) in metres for the given UTM zone."""
    a = 6378137.0
    f = 1 / 298.257223563
    k0 = 0.9996
    e2 = f * (2 - f)
    ep2 = e2 / (1 - e2)
    n = f / (2 - f)
    A = a / (1 + n) * (1 + n*n/4 + n**4/64)
    alpha = [
        n/2 - 2*n*n/3 + 5*n**3/16,
        13*n*n/48 - 3*n**3/5,
        61*n**3/240,
    ]
    lat_r = math.radians(lat)
    lng_r = math.radians(lng)
    lon0 = math.radians((zone - 1) * 6 - 180 + 3)
    t = math.sinh(math.atanh(math.sin(lat_r)) - 2*math.sqrt(n)/(1+n) * math.atanh(2*math.sqrt(n)/(1+n)*math.sin(lat_r)))
    xi_p = math.atan2(t, math.cos(lng_r - lon0))
    eta_p = math.atanh(math.sin(lng_r - lon0) / math.sqrt(1 + t*t))
    xi = xi_p
    eta = eta_p
    for j in range(3):
        xi  += alpha[j] * math.sin(2*(j+1)*xi_p) * math.cosh(2*(j+1)*eta_p)
        eta += alpha[j] * math.cos(2*(j+1)*xi_p) * math.sinh(2*(j+1)*eta_p)
    east = k0 * A * eta + 500000.0
    north = k0 * A * xi
    if lat < 0:
        north += 10_000_000.0
    return east, north


def utm_to_latlng(east: float, north: float, zone: int, southern: bool = False):
    a = 6378137.0
    f = 1 / 298.257223563
    k0 = 0.9996
    n = f / (2 - f)
    A = a / (1 + n) * (1 + n*n/4 + n**4/64)
    beta = [
        n/2 - 2*n*n/3 + 37*n**3/96,
        n*n/48 + n**3/15,
        17*n**3/480,
    ]
    delta = [
        2*n - 2*n*n/3 - 2*n**3,
        7*n*n/3 - 8*n**3/5,
        56*n**3/15,
    ]
    if southern:
        north -= 10_000_000.0
    xi  = north / (k0 * A)
    eta = (east - 500000.0) / (k0 * A)
    xi_p = xi
    eta_p = eta
    for j in range(3):
        xi_p  -= beta[j] * math.sin(2*(j+1)*xi) * math.cosh(2*(j+1)*eta)
        eta_p -= beta[j] * math.cos(2*(j+1)*xi) * math.sinh(2*(j+1)*eta)
    chi = math.asin(math.sin(xi_p) / math.cosh(eta_p))
    lat_r = chi
    for j in range(3):
        lat_r += delta[j] * math.sin(2*(j+1)*chi)
    lon0 = math.radians((zone - 1) * 6 - 180 + 3)
    lng_r = lon0 + math.atan2(math.sinh(eta_p), math.cos(xi_p))
    return math.degrees(lat_r), math.degrees(lng_r)


# Precompute origin in UTM so we can convert (eastM, northM) deltas to lat/lng.
_ORIGIN_ZONE = utm_zone(ORIGIN_LNG)
_ORIGIN_E, _ORIGIN_N = latlng_to_utm(ORIGIN_LAT, ORIGIN_LNG, _ORIGIN_ZONE)


def world_cm_to_latlng(world_x_cm: float, world_y_cm: float) -> tuple[float, float]:
    """UE-world cm (X east, Y south, per the existing extractor convention) ->
    WGS-84 (lat, lng).
    """
    east_m = world_x_cm / 100.0
    south_m = world_y_cm / 100.0
    e = _ORIGIN_E + east_m
    n = _ORIGIN_N - south_m  # Y is south-positive in TSW
    lat, lng = utm_to_latlng(e, n, _ORIGIN_ZONE, southern=ORIGIN_LAT < 0)
    return lat, lng


# ---------- pak extraction ----------

def repak_unpack_all_umaps(pak: Path, dest: Path) -> list[Path]:
    """Extract every .umap (and matching .uexp) from the pak under dest.

    Filters by the parent directory `Content/Map/Tiles/` rather than by
    enumerating every file — passing 192 individual `-i` flags blows
    Windows' argv cap and silently drops most of the batches when the
    cap is split across multiple repak invocations. One `-i <dir>`
    catches everything inside without batching.
    """
    print(f"Listing pak entries...")
    out = subprocess.run([str(REPAK), "list", str(pak)], capture_output=True, text=True, check=True)
    expected_umaps = sum(
        1 for line in out.stdout.splitlines()
        if "/Map/Tiles/" in line and line.strip().endswith(".umap")
    )
    print(f"  {expected_umaps} tile umaps in pak")

    # Find the directory prefix used in this pak. repak's `-i` filter
    # matches any pak-internal path that has the include string as a
    # prefix, so we pass the tiles dir and let it pull every file
    # inside (umap, uexp, ubulk).
    tiles_prefix = None
    for line in out.stdout.splitlines():
        line = line.strip()
        if "/Map/Tiles/" in line:
            tiles_prefix = line.split("/Map/Tiles/")[0] + "/Map/Tiles"
            break
    if tiles_prefix is None:
        return []
    args = [str(REPAK), "unpack", "-f", "-o", str(dest), "-i", tiles_prefix, str(pak)]
    subprocess.run(args, check=True, stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)

    umaps = sorted(dest.rglob("LT_*.umap"))
    print(f"  {len(umaps)} tile umaps unpacked to {dest}")
    return umaps


def umap_to_json(umap_path: Path) -> Path:
    json_path = umap_path.with_suffix(umap_path.suffix + ".json")
    if json_path.exists() and json_path.stat().st_size > 0:
        return json_path
    subprocess.run(
        [str(UASSETGUI), "tojson", str(umap_path), str(json_path), "VER_UE4_27"],
        check=False, stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL,
    )
    return json_path


# ---------- spline parsing ----------

def find_prop(props: list, name: str):
    if not isinstance(props, list):
        return None
    for p in props:
        if isinstance(p, dict) and p.get("Name") == name:
            return p
    return None


def fvector(prop: dict) -> tuple[float, float, float] | None:
    """Pull (X, Y, Z) from a struct property whose Value is either a single
    VectorPropertyData or a list containing one."""
    if not isinstance(prop, dict):
        return None
    v = prop.get("Value")
    if isinstance(v, list) and v:
        v = v[0].get("Value") if isinstance(v[0], dict) else None
    if isinstance(v, dict) and "X" in v and "Y" in v:
        return float(v["X"]), float(v["Y"]), float(v.get("Z", 0.0))
    return None


def parse_tile(json_path: Path) -> dict:
    """Return {'tile_x':int,'tile_y':int,'segments':[(StartPos,StartTan,EndPos,EndTan), ...]}.

    StartPos / StartTangent / EndPos / EndTangent are tile-local FVector
    in cm (X east, Y south, Z up - TSW's convention). Component-level
    RelativeLocation isn't applied to SplineParams in TSW because the
    SplineMeshComponent's params are already authored in the parent's
    coordinate system; the empirical alignment we'll see across tiles
    confirms this.
    """
    name = json_path.stem.replace(".umap", "")  # "LT_x0_y0"
    parts = name.split("_")
    tx = int(parts[1][1:])  # "x0" -> 0
    ty = int(parts[2][1:])  # "y0" -> 0

    try:
        with open(json_path, "r", encoding="utf-8") as f:
            doc = json.load(f)
    except Exception:
        return {"tile_x": tx, "tile_y": ty, "segments": []}

    segments = []
    for e in doc.get("Exports", []):
        if "SplineMeshComponent" not in str(e.get("ObjectName", "")):
            continue
        data = e.get("Data")
        if not isinstance(data, list):
            continue
        sp = find_prop(data, "SplineParams")
        if not sp:
            continue
        inner = sp.get("Value")
        if not isinstance(inner, list):
            continue
        sp_start = find_prop(inner, "StartPos")
        sp_stan  = find_prop(inner, "StartTangent")
        sp_end   = find_prop(inner, "EndPos")
        sp_etan  = find_prop(inner, "EndTangent")
        s = fvector(sp_start)
        st = fvector(sp_stan)
        ep = fvector(sp_end)
        et = fvector(sp_etan)
        if not (s and st and ep and et):
            continue

        # The component's RelativeLocation translates this segment into
        # the tile frame. SplineParams are authored relative to the
        # SplineMeshComponent itself.
        rl = find_prop(data, "RelativeLocation")
        rx = ry = rz = 0.0
        if rl:
            rv = fvector(rl)
            if rv:
                rx, ry, rz = rv

        # Translate segment endpoints into tile-local frame.
        s2 = (s[0] + rx, s[1] + ry, s[2] + rz)
        e2 = (ep[0] + rx, ep[1] + ry, ep[2] + rz)
        # Tangents are direction × magnitude - translation doesn't affect them.
        segments.append((s2, st, e2, et))

    return {"tile_x": tx, "tile_y": ty, "segments": segments}


# ---------- Hermite sampling ----------

def hermite_sample(p0, t0, p1, t1, n: int):
    """Cubic Hermite curve from p0 (with tangent t0) to p1 (with tangent t1).
    Yields n+1 points along tin[0,1]."""
    out = []
    for i in range(n + 1):
        t = i / n
        h00 = 2*t**3 - 3*t**2 + 1
        h10 = t**3 - 2*t**2 + t
        h01 = -2*t**3 + 3*t**2
        h11 = t**3 - t**2
        x = h00*p0[0] + h10*t0[0] + h01*p1[0] + h11*t1[0]
        y = h00*p0[1] + h10*t0[1] + h01*p1[1] + h11*t1[1]
        z = h00*p0[2] + h10*t0[2] + h01*p1[2] + h11*t1[2]
        out.append((x, y, z))
    return out


# ---------- main ----------

def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--out", default=str(Path.home() / "Desktop" / "TrainingCentre_splines.geojson"))
    ap.add_argument("--tiles-only", help="comma-separated list of tile names to process (e.g. LT_x0_y0)")
    ap.add_argument("--keep-temp", action="store_true", help="don't delete the scratch dir")
    args = ap.parse_args()

    out_path = Path(args.out)
    print(f"Output: {out_path}")
    print(f"Origin: ({ORIGIN_LAT}, {ORIGIN_LNG})  zone={_ORIGIN_ZONE}")

    scratch = Path(tempfile.mkdtemp(prefix="tc_splines_"))
    print(f"Scratch: {scratch}")

    try:
        umaps = repak_unpack_all_umaps(TSW_PAK, scratch)
        if args.tiles_only:
            wanted = set(args.tiles_only.split(","))
            umaps = [u for u in umaps if u.stem in wanted]
            print(f"Filtered to {len(umaps)} tile(s): {[u.stem for u in umaps]}")

        print(f"Converting {len(umaps)} umap(s) -> JSON via UAssetGUI (~1–3 s each)...")
        t0 = time.time()
        for i, u in enumerate(umaps):
            umap_to_json(u)
            if (i + 1) % 20 == 0 or i + 1 == len(umaps):
                print(f"  {i+1}/{len(umaps)} converted ({time.time()-t0:.0f}s)")

        features = []
        total_segments = 0
        total_points = 0
        for u in umaps:
            tile = parse_tile(u.with_suffix(".umap.json"))
            tx, ty = tile["tile_x"], tile["tile_y"]
            world_offset_cm = (tx * TILE_CM, ty * TILE_CM)
            for s, st, e, et in tile["segments"]:
                # Hermite-sample in tile-local cm, then translate by tile
                # world offset, then UE->UTM->lat/lng.
                pts = hermite_sample(s, st, e, et, SAMPLES_PER_SEGMENT)
                line = []
                for px, py, _pz in pts:
                    wx = px + world_offset_cm[0]
                    wy = py + world_offset_cm[1]
                    lat, lng = world_cm_to_latlng(wx, wy)
                    line.append([round(lng, 7), round(lat, 7)])
                features.append({
                    "type": "Feature",
                    "geometry": {"type": "LineString", "coordinates": line},
                    "properties": {"tile_x": tx, "tile_y": ty},
                })
                total_segments += 1
                total_points += len(line)

        gj = {
            "type": "FeatureCollection",
            "name": "TrainingCentre_splines",
            "features": features,
        }
        with open(out_path, "w", encoding="utf-8") as f:
            json.dump(gj, f)
        print(f"Wrote {out_path}")
        print(f"  segments: {total_segments}")
        print(f"  total polyline points: {total_points}")

    finally:
        if not args.keep_temp:
            shutil.rmtree(scratch, ignore_errors=True)


if __name__ == "__main__":
    sys.exit(main())
