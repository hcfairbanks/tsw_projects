"""Validate the Go extractor's output against bot observations.

For each route, compare:
  - conductorCompatible: our field vs bot's conductor_compatible (majority-true dedup)
  - trainClasses: our list vs bot's stock-folder set
"""
import json, glob, os, sys, zipfile


def load_go_output(zip_path):
    cc = {}
    tc = {}
    with zipfile.ZipFile(zip_path) as z:
        for name in z.namelist():
            if not name.endswith('.json'):
                continue
            try:
                d = json.loads(z.read(name))
            except Exception:
                continue
            cn = d.get('current_service_name') or d.get('currentServiceName')
            if not cn:
                continue
            # Multiple variants of the same service (AI layer, etc.) can share
            # the same current_service_name. Take any-true for cc (matches bot
            # dedup) and union for trainClasses.
            cc[cn] = cc.get(cn, False) or bool(d.get('conductorCompatible'))
            existing = set(tc.get(cn, []))
            existing.update(d.get('trainClasses') or [])
            tc[cn] = sorted(existing)
    return cc, tc


def load_bot_data(bot_root):
    cc = {}
    cls = {}
    for p in glob.glob(f'{bot_root}/**/llm_data.json', recursive=True):
        try:
            d = json.load(open(p, encoding='utf-8'))
        except Exception:
            continue
        n = (d.get('location') or {}).get('current_service_name')
        if not n:
            continue
        parts = p.replace('\\', '/').split('/')
        stock = parts[-4] if len(parts) >= 4 else '?'
        cc[n] = cc.get(n, False) or bool(d.get('conductor_compatible'))
        cls.setdefault(n, set()).add(stock.strip())
    return cc, cls


def run(label, zip_path, bot_root):
    go_cc, go_tc = load_go_output(zip_path)
    bot_cc, bot_cls = load_bot_data(bot_root)
    matched = [n for n in bot_cc if n in go_cc]
    print(f'\n=== {label} ===')
    print(f'Matched {len(matched)} of {len(bot_cc)} bot services '
          f'(Go output has {len(go_cc)} total)')

    tt = tf = ft = ff = 0
    for n in matched:
        if go_cc[n] and bot_cc[n]:
            tt += 1
        elif go_cc[n] and not bot_cc[n]:
            tf += 1
        elif not go_cc[n] and bot_cc[n]:
            ft += 1
        else:
            ff += 1
    prec = tt / max(tt + tf, 1)
    rec = tt / max(tt + ft, 1)
    print(f'conductorCompatible: T/T={tt} T/F={tf} F/T={ft} F/F={ff}  '
          f'prec={prec:.1%} rec={rec:.1%}')

    exact = superset = missing = disjoint = 0
    mismatches = []
    for n in matched:
        obs = bot_cls.get(n, set())
        comp = set(go_tc.get(n, []))
        if obs == comp:
            exact += 1
        elif obs.issubset(comp):
            superset += 1
        elif obs & comp:
            missing += 1
            if len(mismatches) < 5:
                mismatches.append(('miss', n, obs, comp))
        else:
            disjoint += 1
            if len(mismatches) < 5:
                mismatches.append(('disj', n, obs, comp))
    print(f'trainClasses: exact={exact} superset={superset} missing={missing} disjoint={disjoint}')
    for tag, n, o, c in mismatches:
        print(f'  [{tag}] {n}: obs={sorted(o)} comp={sorted(c)}')


if __name__ == '__main__':
    args = sys.argv[1:]
    if len(args) >= 2:
        run('custom', args[0], args[1])
    else:
        run(
            'Boston Worcester',
            r'C:/Users/hcfai/AppData/Local/Temp/bow_pkg_v2.zip',
            r'C:/Users/hcfai/Desktop/applications_2/bot/screenshots/Boston Worcester',
        )
