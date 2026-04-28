"""Extract SpawnConfigurationOverride and SpawnTypeOverride per vehicle, then
cross-reference with the bot's stock-folder classification to see if these fields
identify the visible variant (Rotem CTC-5 vs CTC-3 vs F40PH-3C)."""
import sys, base64, json, struct, glob, os
sys.path.insert(0, os.path.dirname(__file__))
from check_guard_mode import read_tag, read_i32, fname, walk_struct, fstr
from collections import Counter, defaultdict


def fmt_guid(b16):
    if b16 == b'\x00' * 16:
        return ''
    return '%08X-%08X-%08X-%08X' % struct.unpack('<IIII', b16)


def parse_formations(tt_path):
    doc = json.load(open(tt_path, encoding='utf-8'))
    nm = doc['NameMap']
    payload = base64.b64decode(doc['Exports'][0]['Data'])
    formations = {}
    services = {}
    p = 0
    while p < len(payload) - 8:
        tag, p = read_tag(payload, p, nm)
        if tag is None: break
        dp = p
        if tag['name'] == 'Formations' and tag['ptype'] == 'ArrayProperty':
            count, p = read_i32(payload, p)
            if count:
                inner, p = read_tag(payload, p, nm)
                arr_end = p + inner['size']
                for _ in range(count):
                    if p >= arr_end: break
                    form_name = [None]; vehicles = []
                    def ex_form(t2, d2, pp, fn=form_name, vs=vehicles):
                        if t2['name'] == 'Name' and t2['ptype'] == 'NameProperty':
                            v, _ = fname(d2, pp, nm); fn[0] = v
                        elif t2['name'] == 'RailVehicleInfo' and t2['ptype'] == 'ArrayProperty':
                            c2, pp2 = read_i32(d2, pp)
                            if c2:
                                inner2, pp2 = read_tag(d2, pp2, nm)
                                ae2 = pp2 + inner2['size']
                                cur = {}
                                p_here = pp2
                                while p_here < ae2:
                                    t3, p_new = read_tag(d2, p_here, nm)
                                    if t3 is None:
                                        vs.append(cur); cur = {}
                                        p_here = p_new; continue
                                    if t3['name'] == 'RailVehicleID' and t3['ptype'] == 'StructProperty' and t3['struct'] == 'Guid':
                                        cur['rvid'] = fmt_guid(d2[p_new:p_new + 16])
                                    elif t3['name'] == 'SpawnConfigurationOverride' and t3['ptype'] == 'NameProperty':
                                        v, _ = fname(d2, p_new, nm); cur['sco'] = v
                                    elif t3['name'] == 'SpawnTypeOverride' and t3['ptype'] == 'EnumProperty':
                                        v, _ = fname(d2, p_new, nm); cur['sto'] = v
                                    elif t3['name'] == 'bOverrideSpawnConfiguration' and t3['ptype'] == 'BoolProperty':
                                        cur['osc'] = bool(t3['bool'])
                                    p_here = p_new + t3['size']
                    p = walk_struct(payload, p, arr_end, nm, ex_form)
                    if form_name[0]:
                        formations[form_name[0]] = vehicles
            p = dp + tag['size']
        elif tag['name'] == 'Services' and tag['ptype'] == 'ArrayProperty':
            count, p = read_i32(payload, p)
            if count:
                inner, p = read_tag(payload, p, nm)
                arr_end = p + inner['size']
                for _ in range(count):
                    if p >= arr_end: break
                    svc = {}
                    def ex(t2, d2, pp, svc=svc):
                        if t2['name'] == 'Name' and t2['ptype'] == 'NameProperty':
                            v, _ = fname(d2, pp, nm); svc['name'] = v
                        elif t2['name'] == 'FormationName' and t2['ptype'] == 'NameProperty':
                            v, _ = fname(d2, pp, nm); svc['formation'] = v
                    p = walk_struct(payload, p, arr_end, nm, ex)
                    if svc.get('name'):
                        services[svc['name']] = svc.get('formation')
            p = dp + tag['size']
        else:
            p = dp + tag['size']
    return formations, services


def run(tt_path, bot_root, label):
    formations, services = parse_formations(tt_path)
    folder_to_sco = defaultdict(Counter)
    for p in glob.glob(f'{bot_root}/**/llm_data.json', recursive=True):
        try: d = json.load(open(p, encoding='utf-8'))
        except Exception: continue
        n = (d.get('location') or {}).get('current_service_name')
        if not n or n not in services: continue
        formation = services[n]
        parts = p.replace('\\', '/').split('/')
        stock = parts[-4] if len(parts) >= 4 else '?'
        vs = formations.get(formation, [])
        for v in vs:
            sco = v.get('sco', '<missing>')
            sto = v.get('sto', '<missing>')
            osc = v.get('osc', False)
            folder_to_sco[stock][(osc, sco, sto)] += 1

    print(f'=== {label} ===')
    for folder in sorted(folder_to_sco):
        print(f'[{folder}]')
        for (osc, sco, sto), c in folder_to_sco[folder].most_common(10):
            print(f'  osc={osc!s:<5} SCO={sco!r:<40} STO={sto!r:<60} count={c}')
        print()


if __name__ == '__main__':
    run(
        r'C:/Users/hcfai/AppData/Local/Temp/tsw6-timetable-585114702/BostonProvidence/TS2Prototype/Plugins/DLC/BostonProvidence_Route_Gameplay/Content/Timetable/BPE_Timetable.uasset.json',
        r'C:/Users/hcfai/Desktop/applications_2/bot/screenshots/Boston Sprinter',
        'Boston Sprinter',
    )
    run(
        r'C:/Users/hcfai/AppData/Local/Temp/tsw6-timetable-282796129/BostonWorcester/TS2Prototype/Plugins/DLC/BostonWorcester_Route_Gameplay/Content/Timetable/BOW-TT.uasset.json',
        r'C:/Users/hcfai/Desktop/applications_2/bot/screenshots/Boston Worcester',
        'Boston Worcester',
    )
