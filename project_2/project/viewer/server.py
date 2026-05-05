"""server.py — tiny HTTP server for the TSW6 rails viewer.

Two roles:

  1. Static file server. Serves index.html and anything else in this
     directory at http://localhost:8888/.

  2. API proxy. /api/playerinfo forwards to the local TSW CommAPI at
     http://localhost:31270/get/DriverAid.PlayerInfo, injecting the
     DTGCommKey header from CommAPIKey.txt. The viewer can call this
     endpoint with no CORS problems because the page itself is served
     from the same origin.

Usage:
    python server.py [--port 8888]

Then open http://localhost:8888/ in your browser. Click "Track player"
in the viewer to start showing your in-game position as a marker.
"""
import argparse
import http.server
import json
import os
import socketserver
import sys
import urllib.request
from pathlib import Path

VIEWER_DIR = Path(__file__).resolve().parent
TSW_BASE   = "http://localhost:31270"

def find_api_key():
    """Read CommAPIKey.txt from the standard TSW6 install."""
    user = os.environ.get('USERPROFILE') or os.path.expanduser('~')
    p = Path(user) / 'Documents' / 'My Games' / 'TrainSimWorld6' / 'Saved' / 'Config' / 'CommAPIKey.txt'
    if not p.exists():
        return None, str(p)
    return p.read_text(encoding='utf-8').strip(), str(p)

API_KEY, API_KEY_PATH = find_api_key()
if API_KEY is None:
    print(f'WARN: CommAPIKey.txt not found at {API_KEY_PATH}', file=sys.stderr)
    print('      Live tracker will fail until TSW6 is running and writes the key.', file=sys.stderr)
else:
    print(f'API key loaded ({len(API_KEY)} chars) from {API_KEY_PATH}')

class Handler(http.server.SimpleHTTPRequestHandler):
    def __init__(self, *args, **kwargs):
        super().__init__(*args, directory=str(VIEWER_DIR), **kwargs)

    def log_message(self, fmt, *args):
        # Suppress per-request log lines — too noisy for a polling viewer
        if '/api/' in (args[0] if args else ''):
            return
        super().log_message(fmt, *args)

    def do_GET(self):
        if self.path.startswith('/api/playerinfo'):
            self._proxy_playerinfo()
            return
        if self.path == '/':
            self.path = '/index.html'
        return super().do_GET()

    def _proxy_playerinfo(self):
        # Re-read the key on every request so a TSW restart (which rotates
        # the key) doesn't require restarting the server.
        key, key_path = find_api_key()
        if not key:
            print(f'[proxy] NO KEY at {key_path}', file=sys.stderr)
            self._send_json(503, {'error': 'no API key — start TSW6'})
            return
        prefix = key[:6]; suffix = key[-4:]
        try:
            req = urllib.request.Request(
                f'{TSW_BASE}/get/DriverAid.PlayerInfo',
                headers={'DTGCommKey': key})
            with urllib.request.urlopen(req, timeout=2.0) as r:
                body = r.read()
                status = r.status
            preview = body.decode('utf-8', errors='replace')[:120]
            print(f'[proxy] OK  status={status}  key={prefix}...{suffix} ({len(key)})  resp={preview}', file=sys.stderr)
            self.send_response(200)
            self.send_header('Content-Type', 'application/json')
            self.send_header('Cache-Control', 'no-store')
            self.end_headers()
            self.wfile.write(body)
        except urllib.error.HTTPError as e:
            body = b''
            try: body = e.read()
            except: pass
            preview = body.decode('utf-8', errors='replace')[:200]
            print(f'[proxy] HTTP {e.code}  key={prefix}...{suffix} ({len(key)})  resp={preview}', file=sys.stderr)
            # Forward the upstream body verbatim so the viewer can see the
            # real error message (e.g. dtg.comm.InvalidKey) instead of
            # a generic 502.
            self.send_response(e.code)
            self.send_header('Content-Type', 'application/json')
            self.send_header('Cache-Control', 'no-store')
            self.end_headers()
            self.wfile.write(body if body else json.dumps({'error': str(e)}).encode())
        except urllib.error.URLError as e:
            print(f'[proxy] URLError: {e.reason}', file=sys.stderr)
            self._send_json(502, {'error': str(e.reason if hasattr(e, "reason") else e)})

    def _send_json(self, code, payload):
        body = json.dumps(payload).encode('utf-8')
        self.send_response(code)
        self.send_header('Content-Type', 'application/json')
        self.send_header('Content-Length', str(len(body)))
        self.end_headers()
        self.wfile.write(body)

def main():
    ap = argparse.ArgumentParser()
    ap.add_argument('--port', type=int, default=8888)
    ap.add_argument('--bind', default='127.0.0.1')
    args = ap.parse_args()

    # Allow address re-use so Ctrl+C doesn't leave the port stuck.
    socketserver.TCPServer.allow_reuse_address = True
    with socketserver.TCPServer((args.bind, args.port), Handler) as srv:
        print(f'serving viewer at http://{args.bind}:{args.port}/')
        print(f'  proxy:  /api/playerinfo -> {TSW_BASE}/get/DriverAid.PlayerInfo')
        print('Ctrl+C to stop.')
        try:
            srv.serve_forever()
        except KeyboardInterrupt:
            print('\nshutting down')

if __name__ == '__main__':
    main()
