"""Fetch railway station nodes from OSM Overpass API for each country
we have routes in, then save the (name, lat, lon) triples as a single
SQLite cache. A subsequent backfill pass joins against it.

Why split into two scripts: Overpass is slow + occasionally flaky; we
want the fetched data persisted so backfill iterations don't re-hit the
network.
"""
import urllib.request, urllib.parse, time, sqlite3, os, sys

OVERPASS = 'https://overpass-api.de/api/interpreter'
# ISO 3166-1 alpha-2 codes; OSM Overpass keys countries by these.
COUNTRIES = ['GB', 'DE', 'US', 'AT', 'CH', 'NL', 'CZ', 'FR', 'CA', 'IT']

OUT_DB = os.path.join(os.path.dirname(__file__), 'osm_stations.sqlite')

def fetch_country(code):
    """One Overpass query → list of (name, lat, lon)."""
    # CSV output keeps the response small (~150 KB for UK vs 5+ MB JSON).
    # Pull station + halt + tram_stop nodes. relation+way variants are
    # rare for station nodes; we only want the point of the platform.
    q = f'''[out:csv(::lat,::lon,name;false)][timeout:120];
area["ISO3166-1"="{code}"][admin_level=2]->.a;
(node["railway"~"^(station|halt|tram_stop)$"](area.a);
 node["public_transport"="station"]["railway"](area.a););
out;'''
    data = urllib.parse.urlencode({'data': q}).encode()
    req = urllib.request.Request(OVERPASS, data=data, headers={
        # Overpass rejects requests with no UA / generic Python UA.
        # Identify ourselves as a tool + contact (per Overpass etiquette).
        'User-Agent': 'hud-go-rescue/1.0 (TSW timetable coord backfill; one-shot)',
        'Accept': '*/*',
    })
    print(f"  fetching {code}...", flush=True)
    t = time.time()
    with urllib.request.urlopen(req, timeout=150) as resp:
        body = resp.read().decode('utf-8', errors='replace')
    rows = []
    for line in body.split('\n'):
        line = line.strip()
        if not line: continue
        parts = line.split('\t')
        if len(parts) < 3: continue
        lat, lon, name = parts[0], parts[1], '\t'.join(parts[2:])
        if not name or not lat or not lon: continue
        try:
            rows.append((name, float(lat), float(lon), code))
        except ValueError:
            continue
    print(f"    {len(rows)} rows  ({time.time()-t:.1f}s)", flush=True)
    return rows

def main():
    if os.path.exists(OUT_DB):
        os.remove(OUT_DB)
    db = sqlite3.connect(OUT_DB)
    db.execute('''CREATE TABLE osm_stations (
        name TEXT, lat REAL, lon REAL, country TEXT
    )''')
    db.execute('CREATE INDEX idx_name ON osm_stations(name)')
    total = 0
    for c in COUNTRIES:
        try:
            rows = fetch_country(c)
            db.executemany('INSERT INTO osm_stations VALUES (?,?,?,?)', rows)
            total += len(rows)
            db.commit()
        except Exception as e:
            print(f"  {c} FAILED: {e}", flush=True)
        # Be polite to Overpass: 5s between countries
        time.sleep(5)
    print(f"\ntotal rows cached: {total}")

if __name__ == '__main__':
    main()
