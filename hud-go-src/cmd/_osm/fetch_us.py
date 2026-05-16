"""Retry US fetch with the proper relation ID (148838 = United States).
ISO3166-1=US filter returns 0 rows because OSM's US country relation
doesn't carry that tag at admin_level=2 the way Europe does.
"""
import urllib.request, urllib.parse, time, sqlite3, os

OVERPASS = 'https://overpass-api.de/api/interpreter'
OUT_DB = os.path.join(os.path.dirname(__file__), 'osm_stations.sqlite')

# Per /wiki/United_States_of_America at openstreetmap.org, relation id is 148838.
q = '''[out:csv(::lat,::lon,name;false)][timeout:300];
rel(148838);
map_to_area->.a;
(node["railway"~"^(station|halt|tram_stop)$"](area.a);
 node["public_transport"="station"]["railway"](area.a););
out;'''

data = urllib.parse.urlencode({'data': q}).encode()
req = urllib.request.Request(OVERPASS, data=data, headers={
    'User-Agent': 'hud-go-rescue/1.0 (TSW timetable coord backfill; one-shot)',
    'Accept': '*/*',
})

print("fetching US (rel 148838)...", flush=True)
t = time.time()
with urllib.request.urlopen(req, timeout=350) as resp:
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
        rows.append((name, float(lat), float(lon), 'US'))
    except ValueError:
        continue
print(f"  {len(rows)} rows  ({time.time()-t:.1f}s)")

db = sqlite3.connect(OUT_DB)
db.executemany('INSERT INTO osm_stations VALUES (?,?,?,?)', rows)
db.commit()
print("saved.")
