"""Test combined conductor_compatible rule:
    drivable + passenger + consist has (ServiceTypes==3 OR bHasGuardModeControls==true)
    AND not "non-revenue" in Description
    AND stop_and_load_count >= 2
"""
import sys, base64, json, glob, os
sys.path.insert(0, os.path.dirname(__file__))
from check_guard_mode import parse_timetable, canonicalise_rvd_path, read_tag, read_i32, fname, walk_struct, fstr


def read_text(d, p):
    p += 4
    htype = d[p]; p += 1
    if htype == 255:
        return '', p
    if htype == 0:
        ns, p = fstr(d, p); key, p = fstr(d, p); src, p = fstr(d, p)
        return src, p
    return '', p


def rvd_info(path):
    try: d = json.load(open(path, encoding='utf-8'))
    except Exception: return None
    data = d.get('Exports', [{}])[0].get('Data')
    if not isinstance(data, list):
        return None
    info = {'guard': None, 'st': None}
    for prop in data:
        if isinstance(prop, dict):
            n = prop.get('Name')
            if n == 'bHasGuardModeControls': info['guard'] = prop.get('Value')
            elif n == 'ServiceTypes': info['st'] = prop.get('Value')
    return info


def parse_all(tt_path):
    doc = json.load(open(tt_path, encoding='utf-8'))
    nm = doc['NameMap']
    payload = base64.b64decode(doc['Exports'][0]['Data'])
    out = {}
    p = 0
    while p < len(payload) - 8:
        tag, p = read_tag(payload, p, nm)
        if tag is None: break
        dp = p
        if tag['name'] == 'Services' and tag['ptype'] == 'ArrayProperty':
            count, p = read_i32(payload, p)
            if count:
                inner, p = read_tag(payload, p, nm)
                arr_end = p + inner['size']
                for _ in range(count):
                    if p >= arr_end: break
                    svc = {}; sl = [0]; dsc = ['']
                    def ex(t2, d2, pp, svc=svc, slc=sl, dsc=dsc):
                        if t2['name'] == 'Name' and t2['ptype'] == 'NameProperty':
                            v, _ = fname(d2, pp, nm); svc['name'] = v
                        elif t2['name'] == 'FormationName' and t2['ptype'] == 'NameProperty':
                            v, _ = fname(d2, pp, nm); svc['formation'] = v
                        elif t2['name'] == 'bIsPlayerDrivable' and t2['ptype'] == 'BoolProperty':
                            svc['drivable'] = bool(t2['bool'])
                        elif t2['name'] == 'ServiceClass' and t2['ptype'] == 'EnumProperty':
                            v, _ = fname(d2, pp, nm); svc['class'] = v
                        elif t2['name'] == 'Description' and t2['ptype'] == 'TextProperty':
                            try: v, _ = read_text(d2, pp); dsc[0] = v
                            except Exception: pass
                        elif t2['name'] == 'Instructions' and t2['ptype'] == 'ArrayProperty':
                            c2, pp2 = read_i32(d2, pp)
                            if c2:
                                inner2, pp2 = read_tag(d2, pp2, nm)
                                ae2 = pp2 + inner2['size']
                                cur = {'s': None, 'i': None}
                                p_here = pp2
                                while p_here < ae2:
                                    t3, p_new = read_tag(d2, p_here, nm)
                                    if t3 is None:
                                        if cur['s'] and cur['i'] == 'ERouteTimetableServiceInstructionType::LoadUnload':
                                            slc[0] += 1
                                        cur['s'] = None; cur['i'] = None
                                        p_here = p_new
                                        continue
                                    if t3['name'] == 'bIsStopping' and t3['ptype'] == 'BoolProperty':
                                        cur['s'] = bool(t3['bool'])
                                    elif t3['name'] == 'InstructionType' and t3['ptype'] == 'EnumProperty':
                                        v, _ = fname(d2, p_new, nm); cur['i'] = v
                                    p_here = p_new + t3['size']
                    p = walk_struct(payload, p, arr_end, nm, ex)
                    if svc.get('name'):
                        out[svc['name']] = (svc, dsc[0], sl[0])
            p = dp + tag['size']
        else:
            p = dp + tag['size']
    return out


def load_truth_any(root):
    t = {}
    for p in glob.glob(f'{root}/**/llm_data.json', recursive=True):
        try: d = json.load(open(p, encoding='utf-8'))
        except Exception: continue
        n = (d.get('location') or {}).get('current_service_name')
        if not n: continue
        t[n] = t.get(n, False) or bool(d.get('conductor_compatible'))
    return t


def test(name, tt_paths, extract_root, bot_root, restrict_available_rvds=False):
    rvd_map = {}
    for ua in glob.glob(f'{extract_root}/**/RVD_*.uasset.json', recursive=True):
        info = rvd_info(ua)
        if info:
            rvd_map[canonicalise_rvd_path(ua)] = info
    all_svc = {}; all_forms = {}; all_cmp = {}
    for tp in tt_paths:
        d = parse_all(tp); all_svc.update(d)
        svcs, forms, cmp = parse_timetable(tp)
        all_forms.update(forms); all_cmp.update(cmp)

    def consist_cc(svc):
        any_rvd_found = False
        any_rvd_cc = False
        total_vehicles = len(all_forms.get(svc.get('formation'), []))
        matched_vehicles = 0
        for rvid in all_forms.get(svc.get('formation'), []):
            path = all_cmp.get(rvid)
            if not path: continue
            stem = path.rsplit('.', 1)[0]
            for c, v in rvd_map.items():
                if c.endswith(stem) or stem.endswith(c) or c == stem:
                    any_rvd_found = True; matched_vehicles += 1
                    if v['guard'] is True or v['st'] == 3:
                        any_rvd_cc = True
                    break
        # Consider info available only if we matched majority of vehicles
        info_complete = any_rvd_found and matched_vehicles >= max(1, total_vehicles // 2)
        return any_rvd_cc, info_complete

    truth = load_truth_any(bot_root)
    tt = tf = ft = ff = 0; fts = []; tfs = []; skipped_no_rvd = 0
    for n, actual in truth.items():
        d = all_svc.get(n)
        if not d: continue
        svc, desc, sl = d
        drivable = svc.get('drivable'); passenger = svc.get('class') == 'EServiceClass::Passenger'
        cc, had_info = consist_cc(svc)
        if restrict_available_rvds and not had_info:
            skipped_no_rvd += 1
            continue
        non_rev = 'non-revenue' in desc.lower() or 'non revenue' in desc.lower()
        pred = drivable and passenger and cc and not non_rev and sl >= 3
        if pred and actual: tt += 1
        elif pred and not actual: tf += 1; tfs.append(n)
        elif not pred and actual: ft += 1; fts.append(n)
        else: ff += 1
    matched = tt + tf + ft + ff
    prec = tt / max(tt + tf, 1); rec = tt / max(tt + ft, 1)
    extra = f' (skipped {skipped_no_rvd} without RVD info)' if restrict_available_rvds else ''
    print(f'{name}: T/T={tt} T/F={tf} F/T={ft} F/F={ff}  prec={prec:.1%} rec={rec:.1%}{extra}')
    if fts[:3]:
        print(f'  F/T samples: {fts[:3]}')
    if tfs[:3]:
        print(f'  T/F samples: {tfs[:3]}')


if __name__ == '__main__':
    print('=== Rule: drivable+passenger + consist has (ServiceTypes==3 OR guard==true) + not non-revenue + stop_and_load>=2 ===\n')
    print('  ALL truth services:')
    test('Morristown',
         [r'C:/Users/hcfai/AppData/Local/Temp/tsw6-timetable-575946546/Morristown/TS2Prototype/Plugins/DLC/Morristown_Route_Gameplay/Content/Timetable/MRT-G8TT.uasset.json',
          r'C:/Users/hcfai/AppData/Local/Temp/tsw6-timetable-575946546/Morristown/TS2Prototype/Plugins/DLC/Morristown_Route_Gameplay/Content/Timetable/MRT-TT.uasset.json'],
         r'C:/Users/hcfai/AppData/Local/Temp/tsw6-timetable-575946546',
         r'C:/Users/hcfai/Desktop/applications_2/bot/screenshots/Morristown Line_ New York')
    test('Boston Worcester',
         [r'C:/Users/hcfai/AppData/Local/Temp/tsw6-timetable-282796129/BostonWorcester/TS2Prototype/Plugins/DLC/BostonWorcester_Route_Gameplay/Content/Timetable/BOW-TT.uasset.json'],
         r'C:/Users/hcfai/AppData/Local/Temp/tsw6-timetable-282796129',
         r'C:/Users/hcfai/Desktop/applications_2/bot/screenshots/Boston Worcester')
    test('Boston Sprinter',
         [r'C:/Users/hcfai/AppData/Local/Temp/tsw6-timetable-585114702/BostonProvidence/TS2Prototype/Plugins/DLC/BostonProvidence_Route_Gameplay/Content/Timetable/BPE_Timetable.uasset.json'],
         r'C:/Users/hcfai/AppData/Local/Temp/tsw6-timetable-585114702',
         r'C:/Users/hcfai/Desktop/applications_2/bot/screenshots/Boston Sprinter')
    print()
    print('  Morristown restricted to services with RVD info:')
    test('Morristown',
         [r'C:/Users/hcfai/AppData/Local/Temp/tsw6-timetable-575946546/Morristown/TS2Prototype/Plugins/DLC/Morristown_Route_Gameplay/Content/Timetable/MRT-G8TT.uasset.json',
          r'C:/Users/hcfai/AppData/Local/Temp/tsw6-timetable-575946546/Morristown/TS2Prototype/Plugins/DLC/Morristown_Route_Gameplay/Content/Timetable/MRT-TT.uasset.json'],
         r'C:/Users/hcfai/AppData/Local/Temp/tsw6-timetable-575946546',
         r'C:/Users/hcfai/Desktop/applications_2/bot/screenshots/Morristown Line_ New York', True)
