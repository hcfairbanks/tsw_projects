"""For each service in a route, determine if any vehicle in its
formation comes from an RVD with bHasGuardModeControls=true. Compare
against bot conductor_compatible ground truth.
"""
import base64
import glob
import json
import struct
import sys
from collections import Counter
from pathlib import Path


# -------- Minimal binary walker for the timetable payload ---------


def read_u8(d, p): return d[p], p + 1
def read_i32(d, p): return struct.unpack_from("<i", d, p)[0], p + 4
def read_i64(d, p): return struct.unpack_from("<q", d, p)[0], p + 8
def read_f32(d, p): return struct.unpack_from("<f", d, p)[0], p + 4


def fname(d, p, nm):
    idx, p = read_i32(d, p)
    num, p = read_i32(d, p)
    if idx < 0 or idx >= len(nm): return f"?{idx}", p
    s = nm[idx]
    if num > 0: s = f"{s}_{num-1}"
    return s, p


def fstr(d, p):
    n, p = read_i32(d, p)
    if n == 0: return "", p
    if n > 0:
        return d[p:p + n - 1].decode("latin-1", errors="replace"), p + n
    n = -n
    return d[p:p + n * 2].decode("utf-16-le", errors="replace").rstrip("\x00"), p + n * 2


def read_tag(d, p, nm):
    name, p = fname(d, p, nm)
    if name == "None": return None, p
    ptype, p = fname(d, p, nm)
    size, p = read_i32(d, p)
    p += 4
    stype = ""; inner = ""; bv = 0
    if ptype == "StructProperty":
        stype, p = fname(d, p, nm)
        p += 16
    elif ptype == "BoolProperty":
        bv, p = read_u8(d, p)
    elif ptype in ("ByteProperty", "EnumProperty", "ArrayProperty"):
        inner, p = fname(d, p, nm)
    elif ptype == "MapProperty":
        _, p = fname(d, p, nm); _, p = fname(d, p, nm)
    elif ptype == "SetProperty":
        _, p = fname(d, p, nm)
    hg, p = read_u8(d, p)
    if hg: p += 16
    return {"name": name, "ptype": ptype, "size": size, "struct": stype, "inner": inner, "bool": bv}, p


def walk_struct(d, p, limit, nm, extract):
    """Generic walker: call extract(tag, d, p) for each property."""
    while p < limit:
        tag, p = read_tag(d, p, nm)
        if tag is None: return p
        dp = p
        extract(tag, d, p)
        p = dp + tag["size"]
    return p


def fmt_guid(b16):
    if b16 == b'\x00' * 16: return ""
    return "%08X-%08X-%08X-%08X" % struct.unpack("<IIII", b16)


def parse_timetable(json_path):
    """Returns (services, formations) where:
      services: [{"name":..., "formation":..., "is_drivable":bool, ...}]
      formations: {formation_name: [rv_id_guid, ...]}
      compiled: {rv_id_guid: rvd_asset_path}
    """
    doc = json.load(open(json_path, encoding="utf-8"))
    nm = doc["NameMap"]
    payload = base64.b64decode(doc["Exports"][0]["Data"])

    services = []
    formations = {}
    compiled = {}

    p = 0
    while p < len(payload) - 8:
        tag, p = read_tag(payload, p, nm)
        if tag is None: break
        dp = p
        if tag["name"] == "Services" and tag["ptype"] == "ArrayProperty":
            count, p = read_i32(payload, p)
            if count:
                inner, p = read_tag(payload, p, nm)
                arr_end = p + inner["size"]
                for _ in range(count):
                    if p >= arr_end: break
                    svc = {}
                    def extract(tag, d, pp, svc=svc):
                        nm2 = nm
                        if tag["name"] == "Name" and tag["ptype"] == "NameProperty":
                            v, _ = fname(d, pp, nm2); svc["name"] = v
                        elif tag["name"] == "FormationName" and tag["ptype"] == "NameProperty":
                            v, _ = fname(d, pp, nm2); svc["formation"] = v
                        elif tag["name"] == "LayerName" and tag["ptype"] == "NameProperty":
                            v, _ = fname(d, pp, nm2); svc["layer"] = v
                        elif tag["name"] == "ServiceOperator" and tag["ptype"] == "NameProperty":
                            v, _ = fname(d, pp, nm2); svc["operator"] = v
                        elif tag["name"] == "bIsPlayerDrivable" and tag["ptype"] == "BoolProperty":
                            svc["drivable"] = bool(tag["bool"])
                        elif tag["name"] == "bIsHiddenInFrontend" and tag["ptype"] == "BoolProperty":
                            svc["hidden"] = bool(tag["bool"])
                        elif tag["name"] == "ServiceClass" and tag["ptype"] == "EnumProperty":
                            v, _ = fname(d, pp, nm2); svc["class"] = v
                        elif tag["name"] == "PlayerDrivableSide" and tag["ptype"] == "EnumProperty":
                            v, _ = fname(d, pp, nm2); svc["player_side"] = v
                        elif tag["name"] == "bSpawnWithConductor" and tag["ptype"] == "BoolProperty":
                            svc["spawn_with_conductor"] = bool(tag["bool"])
                        elif tag["name"] == "bSpawnWithEngineer" and tag["ptype"] == "BoolProperty":
                            svc["spawn_with_engineer"] = bool(tag["bool"])
                        elif tag["name"] == "bForceGuardModeEnabled" and tag["ptype"] == "BoolProperty":
                            svc["force_guard_enabled"] = bool(tag["bool"])
                        elif tag["name"] == "bForceGuardModeDisabled" and tag["ptype"] == "BoolProperty":
                            svc["force_guard_disabled"] = bool(tag["bool"])
                    p = walk_struct(payload, p, arr_end, nm, extract)
                    services.append(svc)
            p = dp + tag["size"]
        elif tag["name"] == "Formations" and tag["ptype"] == "ArrayProperty":
            count, p = read_i32(payload, p)
            if count:
                inner, p = read_tag(payload, p, nm)
                arr_end = p + inner["size"]
                for _ in range(count):
                    if p >= arr_end: break
                    form = {"name": None, "vehicles": []}
                    def extract(tag, d, pp, form=form, arr_end=arr_end):
                        if tag["name"] == "Name" and tag["ptype"] == "NameProperty":
                            v, _ = fname(d, pp, nm); form["name"] = v
                        elif tag["name"] == "RailVehicleInfo" and tag["ptype"] == "ArrayProperty":
                            # parse per-vehicle structs to extract RailVehicleID
                            c2, pp2 = read_i32(d, pp)
                            if c2:
                                inner2, pp2 = read_tag(d, pp2, nm)
                                ae2 = pp2 + inner2["size"]
                                for _ in range(c2):
                                    if pp2 >= ae2: break
                                    guid = ""
                                    def ex2(tag2, d2, pp3, captured=[guid]):
                                        if tag2["name"] == "RailVehicleID" and tag2["ptype"] == "StructProperty" and tag2["struct"] == "Guid":
                                            captured[0] = fmt_guid(d2[pp3:pp3+16])
                                    captured = [""]
                                    def ex3(tag2, d2, pp3):
                                        if tag2["name"] == "RailVehicleID" and tag2["ptype"] == "StructProperty" and tag2["struct"] == "Guid":
                                            captured[0] = fmt_guid(d2[pp3:pp3+16])
                                    pp2 = walk_struct(d, pp2, ae2, nm, ex3)
                                    form["vehicles"].append(captured[0])
                    p = walk_struct(payload, p, arr_end, nm, extract)
                    formations[form["name"]] = form["vehicles"]
            p = dp + tag["size"]
        elif tag["name"] == "CompiledRVMap" and tag["ptype"] == "MapProperty":
            # alloc flags + count + entries
            p += 4
            count, p = read_i32(payload, p)
            for _ in range(count):
                guid = fmt_guid(payload[p:p+16]); p += 16
                idx = struct.unpack_from("<i", payload, p)[0]; p += 4
                num = struct.unpack_from("<i", payload, p)[0]; p += 4
                path = nm[idx] if 0 <= idx < len(nm) else ""
                if num > 0: path = f"{path}_{num-1}"
                p += 4
                compiled[guid] = path
            p = dp + tag["size"]
        else:
            p = dp + tag["size"]
    return services, formations, compiled


# -------- Scan RVDs for bHasGuardModeControls ----------


def rvd_has_guard(rvd_json_path):
    try:
        doc = json.load(open(rvd_json_path, encoding="utf-8"))
    except Exception:
        return None
    data = doc.get("Exports", [{}])[0].get("Data")
    if not isinstance(data, list): return None
    for prop in data:
        if isinstance(prop, dict) and prop.get("Name") == "bHasGuardModeControls":
            return bool(prop.get("Value"))
    return None  # property not present


# -------- Bot ground truth ----------


def load_truth(screenshots):
    truth = {}
    for path in glob.glob(f"{screenshots}/**/llm_data.json", recursive=True):
        try:
            d = json.load(open(path, encoding="utf-8"))
        except Exception:
            continue
        name = (d.get("location") or {}).get("current_service_name")
        if not name: continue
        truth[name] = bool(d.get("conductor_compatible"))
    return truth


# -------- Main ----------


def canonicalise_rvd_path(disk_path):
    p = disk_path.replace("\\", "/")
    marker = "/Plugins/DLC/"
    i = p.find(marker)
    if i >= 0: p = p[i+len(marker)-1:]
    p = p.replace("/Content/", "/", 1)
    if p.endswith(".uasset.json"): p = p[:-len(".uasset.json")]
    return p


def main():
    tt_json = sys.argv[1]
    extract_root = sys.argv[2]
    screenshots = sys.argv[3]

    # Build RVD path -> has_guard map
    rvd_map = {}  # canonical asset path fragment -> bool
    for ua_json in glob.glob(f"{extract_root}/**/RVD_*.uasset.json", recursive=True):
        flag = rvd_has_guard(ua_json)
        key = canonicalise_rvd_path(ua_json)
        rvd_map[key] = flag
    print(f"Scanned {len(rvd_map)} RVDs, with_guard={sum(1 for v in rvd_map.values() if v)}", file=sys.stderr)

    services, formations, compiled = parse_timetable(tt_json)
    print(f"Services: {len(services)}, Formations: {len(formations)}, CompiledRVMap: {len(compiled)}", file=sys.stderr)

    def svc_has_guard(svc):
        form = svc.get("formation")
        if not form: return False
        rvids = formations.get(form, [])
        for rvid in rvids:
            path = compiled.get(rvid)
            if not path: continue
            # lookup: RVD path in compiled is like "/BOW_MBTA_DoubleDeckCars/Data/RVD/RVD_BOW_MBTA_DoubleDeckCars_CabCar.RVD_BOW_MBTA_DoubleDeckCars_CabCar"
            # strip trailing ".Name.Name"
            stem = path.rsplit(".", 1)[0]
            # Try both with and without trailing dot-suffix
            for candidate, value in rvd_map.items():
                if candidate.endswith(stem) or stem.endswith(candidate) or candidate == stem:
                    if value: return True
                    break
        return False

    truth = load_truth(screenshots)
    print(f"Bot truth rows: {len(truth)}", file=sys.stderr)

    tt = tf = ft = ff = 0  # predicted x actual
    for svc in services:
        if svc.get("name") not in truth: continue
        actual = truth[svc["name"]]
        pred = svc_has_guard(svc)
        if pred and actual: tt += 1
        elif pred and not actual: tf += 1
        elif not pred and actual: ft += 1
        else: ff += 1

    matched = tt + tf + ft + ff
    print(f"\nMatched to bot truth: {matched}")
    print(f"  HasGuardInConsist=T, cc=T: {tt}   <-correctly identified conductor-compatible")
    print(f"  HasGuardInConsist=T, cc=F: {tf}   <-has guard cars but not chosen-as-conductor")
    print(f"  HasGuardInConsist=F, cc=T: {ft}   <-FALSE NEGATIVES (bad)")
    print(f"  HasGuardInConsist=F, cc=F: {ff}   <-correctly identified non-conductor")

    # Distribution of ServiceClass across matched services
    class_dist = Counter()
    for svc in services:
        if svc.get("name") not in truth: continue
        class_dist[svc.get("class", "<missing>")] += 1
    print("\nServiceClass distribution across matched services:")
    for k, v in class_dist.most_common():
        print(f"  {k}: {v}")

    # Flag distribution report
    sw_c = sum(1 for s in services if s.get("spawn_with_conductor"))
    fge = sum(1 for s in services if s.get("force_guard_enabled"))
    fgd = sum(1 for s in services if s.get("force_guard_disabled"))
    print(f"\nFlag totals over {len(services)} services: SpawnWithConductor={sw_c} ForceGuardEnabled={fge} ForceGuardDisabled={fgd}")

    # Rule: HasGuard in consist, overridden by explicit per-service flags
    print("\n--- Rule: (ForceGuardEnabled OR HasGuardInConsist) AND NOT ForceGuardDisabled ---")
    def rule_override(svc):
        if svc.get("force_guard_disabled"): return False
        if svc.get("force_guard_enabled"): return True
        return svc_has_guard(svc)
    tt=tf=ft=ff=0
    tf_names=[]; ft_names=[]
    for svc in services:
        if svc.get("name") not in truth: continue
        actual = truth[svc["name"]]
        pred = rule_override(svc)
        if pred and actual: tt+=1
        elif pred and not actual: tf+=1; tf_names.append(svc["name"])
        elif not pred and actual: ft+=1; ft_names.append(svc["name"])
        else: ff+=1
    print(f"  T/T={tt}  T/F={tf}  F/T={ft} (bad)  F/F={ff}")
    if tf_names: print(f"  T/F samples: {tf_names[:10]}")
    if ft_names: print(f"  F/T samples (MISSED): {ft_names[:10]}")

    # Scoped evaluation: only drivable + passenger services
    print("\n--- Scoped rule (drivable + passenger only): HasGuardInConsist ---")
    passenger_classes = {"EServiceClass::Passenger"}
    tt = tf = ft = ff = 0
    tf_names = []
    ft_names = []
    scoped = 0
    for svc in services:
        if svc.get("name") not in truth: continue
        if not svc.get("drivable"): continue
        if svc.get("class") not in passenger_classes: continue
        scoped += 1
        actual = truth[svc["name"]]
        pred = svc_has_guard(svc)
        if pred and actual: tt += 1
        elif pred and not actual: tf += 1; tf_names.append(svc["name"])
        elif not pred and actual: ft += 1; ft_names.append(svc["name"])
        else: ff += 1
    print(f"  In-scope services (drivable+passenger, matched to truth): {scoped}")
    print(f"  T/T={tt}  T/F={tf}  F/T={ft} (bad)  F/F={ff}")
    if tf_names:
        print(f"  T/F samples (predicted cc=True, bot=False): {tf_names[:10]}")
    if ft_names:
        print(f"  F/T samples (MISSED conductor services): {ft_names[:10]}")


if __name__ == "__main__":
    main()
