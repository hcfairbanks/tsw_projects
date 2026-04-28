"""Cross-reference bot-observed stock folders/car counts against our timetable parser's formation reads.

Goal: verify whether the formation -> RVD list we read matches what the bot saw
the player actually loads into.
"""
import sys, json, glob, os
sys.path.insert(0, os.path.dirname(__file__))
from check_guard_mode import parse_timetable
from collections import Counter, defaultdict


def run(tt_path, bot_root, label):
    svcs, forms, cmp = parse_timetable(tt_path)
    all_svc = {s['name']: s for s in svcs}

    bot_info = {}  # svc_name -> (stock_folder, car_count, ui_name)
    for p in glob.glob(f'{bot_root}/**/llm_data.json', recursive=True):
        try: d = json.load(open(p, encoding='utf-8'))
        except Exception: continue
        n = (d.get('location') or {}).get('current_service_name')
        if not n: continue
        parts = p.replace('\\', '/').split('/')
        stock = parts[-4] if len(parts) >= 4 else '?'
        car_cnt = d.get('level_info', {}).get('car_count')
        ui = d.get('level_info', {}).get('service_name')
        if n not in bot_info:
            bot_info[n] = (stock, car_cnt, ui)

    stock_to_rvds = defaultdict(Counter)
    car_mismatches = []
    missing_formation = []
    for svc_name, (stock, bot_cnt, ui) in bot_info.items():
        svc = all_svc.get(svc_name)
        if not svc:
            continue
        formation = svc.get('formation')
        rvids = forms.get(formation, [])
        if not rvids:
            missing_formation.append((svc_name, formation))
            continue
        consist_stems = []
        for rvid in rvids:
            path = cmp.get(rvid)
            if path:
                stem = path.rsplit('/', 1)[-1].rsplit('.', 1)[0]
                consist_stems.append(stem)
                stock_to_rvds[stock][stem] += 1
        tt_cnt = len(rvids)
        if bot_cnt is not None and tt_cnt and bot_cnt != tt_cnt:
            car_mismatches.append((svc_name, stock, bot_cnt, tt_cnt, consist_stems))

    print(f'=== {label} ===')
    print(f'Bot services: {len(bot_info)} | matched to timetable: {sum(1 for n in bot_info if n in all_svc)}')
    print()
    print('Stock folder → top RVDs seen in matched services:')
    for stock in sorted(stock_to_rvds):
        top = stock_to_rvds[stock].most_common(5)
        print(f'  [{stock}]')
        for rvd, c in top:
            print(f'    {rvd}: {c}')
    print()
    print(f'Car count mismatches (bot != timetable): {len(car_mismatches)} / {sum(1 for (_, cnt, _) in bot_info.values() if cnt)}')
    for n, s, b, t, cons in car_mismatches[:10]:
        print(f'  {n} [{s}] bot={b} tt={t} first_3={cons[:3]}')
    print()
    if missing_formation:
        print(f'Services with empty formation in our timetable: {len(missing_formation)}')
        for n, f in missing_formation[:5]:
            print(f'  {n}: formation={f!r}')


if __name__ == '__main__':
    run(
        r'C:/Users/hcfai/AppData/Local/Temp/tsw6-timetable-585114702/BostonProvidence/TS2Prototype/Plugins/DLC/BostonProvidence_Route_Gameplay/Content/Timetable/BPE_Timetable.uasset.json',
        r'C:/Users/hcfai/Desktop/applications_2/bot/screenshots/Boston Sprinter',
        'Boston Sprinter (BP)',
    )
    print()
    run(
        r'C:/Users/hcfai/AppData/Local/Temp/tsw6-timetable-282796129/BostonWorcester/TS2Prototype/Plugins/DLC/BostonWorcester_Route_Gameplay/Content/Timetable/BOW-TT.uasset.json',
        r'C:/Users/hcfai/Desktop/applications_2/bot/screenshots/Boston Worcester',
        'Boston Worcester',
    )
