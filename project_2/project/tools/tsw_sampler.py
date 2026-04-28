"""Poll the TSW6 API at 2 Hz and log position samples to a JSONL file.

Each line is one sample: route origin (lat/lng), player geoLocation (lat/lng),
currentTile (x/y), and Player.Property.LastPosition (x/y/z, in-train only).

Run this while driving any route. Later we derive the world (tile + x/y) to
geo (lat/lng) conversion from the collected samples.

Usage:
  python tsw_sampler.py [output.jsonl]

Env vars:
  TSW_API_KEY_PATH - override default key path
  TSW_API_URL - override http://localhost:31270
"""
import json, os, sys, time, urllib.request, datetime


API_URL = os.environ.get('TSW_API_URL', 'http://localhost:31270')
KEY_PATH = os.environ.get('TSW_API_KEY_PATH',
    os.path.expanduser(r'~\Documents\My Games\TrainSimWorld6\Saved\Config\CommAPIKey.txt'))

with open(KEY_PATH, encoding='utf-8') as f:
    API_KEY = f.read().strip()

OUT = sys.argv[1] if len(sys.argv) > 1 else 'tsw_samples.jsonl'


def get(endpoint):
    req = urllib.request.Request(
        f'{API_URL}/get/{endpoint}',
        headers={'DTGCommKey': API_KEY},
    )
    try:
        with urllib.request.urlopen(req, timeout=3) as r:
            return json.loads(r.read().decode('utf-8'))
    except Exception as e:
        return {'error': str(e)}


def sample():
    player = get('DriverAid.PlayerInfo').get('Values', {})
    tod = get('TimeOfDay.Data').get('Values', {})
    pos = get('Player.Property.LastPosition').get('Values', {})
    # Also DriverAid.Data has signal and track info; capture nextSignalPosition
    daid = get('DriverAid.Data').get('Values', {})
    return {
        'ts': datetime.datetime.now(datetime.timezone.utc).isoformat(timespec='milliseconds').replace('+00:00', 'Z'),
        'service': player.get('currentServiceName'),
        'geo': player.get('geoLocation'),
        'tile': player.get('currentTile'),
        'origin': {
            'lat': tod.get('OriginLatitude'),
            'lng': tod.get('OriginLongitude'),
        },
        'last_position': pos,
        'next_signal': daid.get('nextSignalPosition'),
        'gradient': daid.get('gradient'),
    }


def format_sample_line(s):
    geo = s.get('geo') or {}
    tile = s.get('tile') or {}
    lat = geo.get('latitude')
    lng = geo.get('longitude')
    lat_s = f'{lat:.5f}' if isinstance(lat, (int, float)) else '?'
    lng_s = f'{lng:.5f}' if isinstance(lng, (int, float)) else '?'
    return (f'svc={s.get("service")} tile=({tile.get("x")},{tile.get("y")}) '
            f'geo=({lat_s},{lng_s})')


if __name__ == '__main__':
    max_samples = int(os.environ.get('TSW_MAX_SAMPLES', '0'))  # 0 = infinite
    period = float(os.environ.get('TSW_PERIOD_S', '0.5'))
    print(f'Sampling to {OUT} every {period}s'
          f'{" (max " + str(max_samples) + ")" if max_samples else ""}. Ctrl+C to stop.',
          file=sys.stderr)
    n = 0
    with open(OUT, 'a', encoding='utf-8') as f:
        try:
            while max_samples == 0 or n < max_samples:
                s = sample()
                f.write(json.dumps(s) + '\n')
                f.flush()
                n += 1
                if n % 30 == 0 or n <= 3:
                    print(f'  [{n:5}] ' + format_sample_line(s), file=sys.stderr)
                time.sleep(period)
        except KeyboardInterrupt:
            pass
    print(f'\nStopped after {n} samples. Wrote {OUT}', file=sys.stderr)
