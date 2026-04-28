"""Derive the set of train classes (FriendlyNames) available for each service,
using only data from the pak extracts (no bot dependency).

Algorithm:
  1. Read service -> formation from timetable
  2. Read formation -> list of RVD GUIDs (via RailVehicleInfo + CompiledRVMap)
  3. For each RVD in the default formation, identify role (lead Loco / lead CabCar)
  4. Build a global index of all RVDs by (LiveryID, VehicleCategory, regions)
  5. For each vehicle in the formation, find all other RVDs that are
     bIsSubstitutableUnit=true AND same (LiveryID, VehicleCategory) AND regions overlap
  6. Emit the FriendlyNames for the "lead playable" vehicle's substitutes as the
     train-class list for the service.

Validate: compare the computed set to the bot's observed folder names per service.
"""
import sys, base64, json, glob, os, struct
sys.path.insert(0, os.path.dirname(__file__))
from check_guard_mode import parse_timetable, canonicalise_rvd_path, read_tag, read_i32, fname, walk_struct, fstr
from collections import defaultdict, Counter


def rvd_info(path):
    """Return dict with friendly_name, livery_id, category, regions, subs, st, guard."""
    try: d = json.load(open(path, encoding='utf-8'))
    except Exception: return None
    data = d.get('Exports', [{}])[0].get('Data')
    if not isinstance(data, list):
        return None
    info = {
        'friendly': None, 'livery': None, 'cat': None,
        'regions': set(), 'subs': False, 'st': None, 'guard': None,
    }
    for prop in data:
        if not isinstance(prop, dict): continue
        n = prop.get('Name')
        if n == 'FriendlyName':
            info['friendly'] = prop.get('CultureInvariantString')
        elif n == 'LiveryID':
            info['livery'] = prop.get('Value')
        elif n == 'VehicleCategory':
            info['cat'] = prop.get('Value')
        elif n == 'AvailableGeographicRegions':
            v = prop.get('Value', [])
            info['regions'] = set(
                (x.get('Value', '') if isinstance(x, dict) else x)
                for x in (v or [])
            )
        elif n == 'bIsSubstitutableUnit':
            info['subs'] = bool(prop.get('Value'))
        elif n == 'ServiceTypes':
            info['st'] = prop.get('Value')
        elif n == 'bHasGuardModeControls':
            info['guard'] = prop.get('Value')
    return info


def build_rvd_index(extract_roots):
    """Scan all RVD jsons across given roots; return {canonical_path: info}."""
    rvd_map = {}
    for root in extract_roots:
        for p in glob.glob(f'{root}/**/RVD_*.uasset.json', recursive=True):
            info = rvd_info(p)
            if info:
                rvd_map[canonicalise_rvd_path(p)] = info
    return rvd_map


def find_rvd_for_path(asset_path, rvd_map):
    """Given a '.../RVD_Foo.RVD_Foo' path string, find its RVD info."""
    stem = asset_path.rsplit('.', 1)[0]
    for c, v in rvd_map.items():
        if c.endswith(stem) or stem.endswith(c) or c == stem:
            return v
    return None


def lead_vehicle_index(formation_vehicles, svc):
    """Pick the 'lead' playable car based on the service's PlayerDrivableSide.

    PlayerDrivableSide=Front -> vehicle at index 0
    PlayerDrivableSide=Back  -> vehicle at last index
    Fallback: prefer locomotive/cab car at either end.
    """
    if not formation_vehicles:
        return None
    pds = svc.get('player_side') if svc else None
    if pds == 'ESide::Front':
        return 0
    if pds == 'ESide::Back':
        return len(formation_vehicles) - 1
    first = formation_vehicles[0]
    last = formation_vehicles[-1]
    if first and first.get('cat') == 'ERailVehicleCategory::Locomotive':
        return 0
    if last and last.get('cat') == 'ERailVehicleCategory::Locomotive':
        return len(formation_vehicles) - 1
    if first and first.get('cat') == 'ERailVehicleCategory::PassengerCabCar':
        return 0
    if last and last.get('cat') == 'ERailVehicleCategory::PassengerCabCar':
        return len(formation_vehicles) - 1
    return 0


def classes_for_service(svc, forms, cmp, rvd_map):
    """Return set of FriendlyNames representing train classes available for a service."""
    form_name = svc.get('formation')
    rvids = forms.get(form_name, [])
    if not rvids:
        return set()
    # Resolve each vehicle's info
    vehicles = []
    for rvid in rvids:
        path = cmp.get(rvid)
        v = find_rvd_for_path(path, rvd_map) if path else None
        vehicles.append(v)
    # Pick the lead vehicle
    lead_idx = lead_vehicle_index(vehicles, svc)
    if lead_idx is None or vehicles[lead_idx] is None:
        return set()
    lead = vehicles[lead_idx]
    # Formation-region signature = union of all RVD regions that ARE known
    form_regions = set()
    for v in vehicles:
        if v and v.get('regions'):
            form_regions |= v['regions']
    names = set()
    # Always include the lead vehicle's own FriendlyName as an available class
    if lead.get('friendly'):
        names.add(lead['friendly'].strip())
    # Add substitutable alternatives that share LiveryID + VehicleCategory
    for v in rvd_map.values():
        if not v.get('subs'): continue
        if v.get('livery') != lead.get('livery'): continue
        if v.get('cat') != lead.get('cat'): continue
        # Region overlap: skip only if BOTH have regions AND they don't overlap
        if form_regions and v.get('regions') and not (v['regions'] & form_regions):
            continue
        if v.get('friendly'):
            names.add(v['friendly'].strip())
    return names


def load_bot_folders(bot_root):
    """Return {service_name: set(folder_names)} across all sections."""
    out = defaultdict(set)
    for p in glob.glob(f'{bot_root}/**/llm_data.json', recursive=True):
        try: d = json.load(open(p, encoding='utf-8'))
        except Exception: continue
        n = (d.get('location') or {}).get('current_service_name')
        if not n: continue
        parts = p.replace('\\', '/').split('/')
        stock = parts[-4] if len(parts) >= 4 else '?'
        out[n].add(stock.strip())
    return out


def run(label, tt_paths, extract_root, bot_root):
    rvd_map = build_rvd_index([extract_root])
    print(f'{label}: scanned {len(rvd_map)} RVDs')
    all_svc = {}
    all_forms = {}
    all_cmp = {}
    for tp in tt_paths:
        svcs, forms, cmp = parse_timetable(tp)
        for s in svcs: all_svc[s['name']] = s
        all_forms.update(forms)
        all_cmp.update(cmp)

    # For each formation, reshape vehicles as list of RVD infos
    # formations already have vehicles as rvid list from parse_timetable
    def reshape(form_name):
        infos = []
        for rvid in all_forms.get(form_name, []):
            path = all_cmp.get(rvid)
            infos.append(find_rvd_for_path(path, rvd_map) if path else None)
        return infos

    bot_folders = load_bot_folders(bot_root)
    matched = sum(1 for n in bot_folders if n in all_svc)
    print(f'Bot services: {len(bot_folders)} | matched to timetable: {matched}')

    # Cross-validate: for each matched service, compare computed vs observed
    exact = superset = missing = disjoint = 0
    mismatches = []
    for svc_name, observed_folders in bot_folders.items():
        svc = all_svc.get(svc_name)
        if not svc: continue
        # Build vehicle list for lead selection
        form_name = svc.get('formation')
        vehicles = reshape(form_name)
        lead_idx = lead_vehicle_index(vehicles, svc)
        if lead_idx is None or not vehicles[lead_idx]:
            continue
        computed = classes_for_service(svc, all_forms, all_cmp, rvd_map)
        obs = {f.strip() for f in observed_folders}
        if computed == obs:
            exact += 1
        elif obs.issubset(computed):
            superset += 1
        elif obs & computed:
            # partial overlap (computed is not a superset)
            missing += 1
            if len(mismatches) < 8:
                mismatches.append((svc_name, obs - computed, computed - obs))
        else:
            disjoint += 1
            if len(mismatches) < 8:
                mismatches.append((svc_name, obs, computed))
    print(f'  exact match: {exact}')
    print(f'  computed is SUPERSET of observed (acceptable): {superset}')
    print(f'  computed MISSING observed (BAD): {missing}')
    print(f'  disjoint (BAD): {disjoint}')
    print()
    if mismatches:
        print('  Sample mismatches (service: missing, extra):')
        for n, miss, extra in mismatches[:5]:
            print(f'    {n}')
            print(f'      bot observed: {sorted(miss)[:5]}')
            print(f'      computed extra: {sorted(extra)[:5]}')


if __name__ == '__main__':
    run(
        'Boston Sprinter (BP)',
        [r'C:/Users/hcfai/AppData/Local/Temp/tsw6-timetable-585114702/BostonProvidence/TS2Prototype/Plugins/DLC/BostonProvidence_Route_Gameplay/Content/Timetable/BPE_Timetable.uasset.json'],
        r'C:/Users/hcfai/AppData/Local/Temp/tsw6-timetable-585114702',
        r'C:/Users/hcfai/Desktop/applications_2/bot/screenshots/Boston Sprinter',
    )
    print()
    run(
        'Boston Worcester (BOW)',
        [r'C:/Users/hcfai/AppData/Local/Temp/tsw6-timetable-282796129/BostonWorcester/TS2Prototype/Plugins/DLC/BostonWorcester_Route_Gameplay/Content/Timetable/BOW-TT.uasset.json'],
        r'C:/Users/hcfai/AppData/Local/Temp/tsw6-timetable-282796129',
        r'C:/Users/hcfai/Desktop/applications_2/bot/screenshots/Boston Worcester',
    )
    print()
    run(
        'Morristown',
        [r'C:/Users/hcfai/AppData/Local/Temp/tsw6-timetable-575946546/Morristown/TS2Prototype/Plugins/DLC/Morristown_Route_Gameplay/Content/Timetable/MRT-G8TT.uasset.json',
         r'C:/Users/hcfai/AppData/Local/Temp/tsw6-timetable-575946546/Morristown/TS2Prototype/Plugins/DLC/Morristown_Route_Gameplay/Content/Timetable/MRT-TT.uasset.json'],
        r'C:/Users/hcfai/AppData/Local/Temp/tsw6-timetable-575946546',
        r'C:/Users/hcfai/Desktop/applications_2/bot/screenshots/Morristown Line_ New York',
    )
