# Building a Custom HUD — guide for an AI

This folder (`resources/custom_huds/`) holds **standalone HTML HUD pages** for the
TSW HUD app. Drop a `.html` file here and it appears automatically on the app's
**Custom HUDs** tab (`/custom-huds`), which generates a QR code + link for it.

You are an AI being asked to build one of these pages. A user will tell you what
they want to see; you produce **one self-contained `.html` file**. Read this
whole document first, then look at the two reference files:

- `german_tablet.html` — a full, real HUD (canvas speedometer + safety blocks).
- `example_minimal.html` — a tiny annotated starter. Pattern-match on this.

---

## 1. How data reaches the page

The page opens a **Server-Sent Events** stream and receives a JSON object
roughly every 100 ms:

```js
const es = new EventSource('/stream');
es.onmessage = (event) => {
    const data = JSON.parse(event.data);
    // ...read fields off `data`, update the DOM/canvas...
};
```

`data` has **two kinds of fields**:

### a) Core fields — already parsed (read directly)
These come from the built-in "Core" section and are normalised server-side.
Read them as plain values:

| field | type | notes |
|---|---|---|
| `speed` | number | in `distanceUnits` (km/h or mph) |
| `limit` | number | current speed limit |
| `nextSpeedLimit`, `distanceToNextSpeedLimit` | number | |
| `powerHandle` | number | throttle/brake notch |
| `reverser` | number | -1 removed, 0 reverse, 1 neutral, 2 forward |
| `trainBreak`, `trainBrakeActive` | number/bool | |
| `locomotiveBrakeHandle`, `locomotiveBrakeActive` | number/bool | |
| `electricDynamicBrake`, `electricBrakeActive` | number/bool | |
| `isSlipping`, `isTractionLocked` | bool | |
| `incline` | number | gradient % |
| `distanceToSignal`, `signalAspectClass` | number/string | |
| `distanceToStation`, `nextStation` | number/string | |
| `localTime`, `timetableTime`, `timetableLabel` | string | |
| `doorFrontLeft`, `doorFrontRight` | bool | |
| `playerPosition` | `{latitude, longitude}` | |
| `currentTile` | `{x, y}` | |
| `weather` | `{Temperature, Cloudiness, ...}` | |
| `distanceUnits` | `"metric"` / `"imperial"` | |
| `temperatureUnits` | `"celsius"` / `"fahrenheit"` | |
| `vehicleCount`, `currentServiceName` | number/string | |

**Do not rely on changing Core** — it's a fixed contract used by other HUDs.

### b) Everything else — RAW passthrough (you interpret it)
Every call **outside** the Core section (e.g. "German Safety", or any section a
user adds) is forwarded **untouched**. `data[<key>]` is the entire TSW
subscription entry:

```json
data.pzbActive = { "Path": "...", "NodeValid": true, "Values": { "Value": false } }
data.afb       = { "Path": "...", "NodeValid": true, "Values": { "Speed": 0, "IsActive": false } }
```

Rules for raw fields:

- The real payload is in **`.Values`**. Read e.g. `data.pzbActive.Values.Value`.
- **`.NodeValid`** is `false` when the call doesn't apply to the current
  train/loco (wrong vehicle, no such system). Treat `NodeValid:false` as "not
  available" and hide that widget. A field may be **absent entirely** if the
  call is disabled — always guard: `data.foo && data.foo.NodeValid`.
- TSW struct members often carry **FGuid hash suffixes** that change between
  game builds, e.g. `"2000Hz_Active_88_8C4C8360..."`. **Match by prefix**, never
  the full key:
  ```js
  function prefixVal(obj, prefix){ for (var k in obj) if (k.indexOf(prefix)===0) return obj[k]; }
  ```

This raw standard is the same for German Safety and **all future APIs** a user
adds — so once you can read one raw field, you can read any of them.

---

## 2. Discovering what fields exist

You usually don't have the game running. Use these endpoints (the app serves
them on `http://localhost:3000`):

- **`GET /api/api-calls`** → the catalog: `{ sections: [ { name, builtin,
  calls: [ {path, key, label, enabled} ] } ] }`. The `key` of each non-Core call
  is the field name you read as `data[key]`. Core calls have empty keys (they're
  the parsed fields in the table above).
- **`GET /api/subscription/data`** → a **live sample** of the exact `data`
  object the stream sends (only meaningful while the game is running). Best way
  to confirm a field's shape.

If the user wants a value that has **no call yet**, tell them to add it on the
**API Subscriptions** page (`/api-subscriptions`): create/choose a section, add
a call with its CommAPI `path` and a `key`, "Test path" to confirm, then
"Save & Reapply". After that, `data[key]` is available to your HUD.

---

## 3. Hard rules for the HTML file

1. **One standalone file.** Inline all your CSS and JS in the `.html`. The only
   external things you may reference are app-served absolute paths and CDNs.
2. **Absolute asset paths only.** The page is served at `/custom-huds/<name>`
   (no trailing slash), so **relative paths break**. Use `/css/theme.css`,
   `/js/...`, `https://cdn...`. Never `css/theme.css` or `./x.js`.
3. **Theme (optional but nice):** `<link rel="stylesheet" href="/css/theme.css">`
   gives you CSS variables: `var(--bg-primary)`, `var(--text-primary)`,
   `var(--accent-color)`, `var(--border-color)`, etc.
4. **Defensive reads.** The stream may arrive before a value exists, or a node
   may be invalid. Always null-check (`data.x && data.x.NodeValid`).
5. **Don't block.** Keep `onmessage` cheap; it fires ~10×/second.

### Optional niceties
- `?timetable=<id>` may be in the URL (the auto-detect flow passes it). Read it
  with `new URLSearchParams(location.search).get('timetable')` if you need the
  loaded service; otherwise ignore it.
- The Custom HUDs tab links to your page via
  `/find-timetable?hud=custom-huds/<name>` (auto-detects the service, then
  redirects to `/custom-huds/<name>?timetable=<id>`). Your page works the same
  whether opened directly or through that flow.

---

## 4. Minimal skeleton

```html
<!DOCTYPE html>
<html lang="en">
<head>
  <meta charset="UTF-8">
  <meta name="viewport" content="width=device-width, initial-scale=1.0">
  <title>My HUD</title>
  <link rel="stylesheet" href="/css/theme.css">
  <style> /* inline styles */ </style>
</head>
<body>
  <div id="speed">--</div>
  <div id="pzb">--</div>
  <script>
    function rawVals(e){ return (e && e.NodeValid && e.Values) ? e.Values : null; }
    const es = new EventSource('/stream');
    es.onmessage = (ev) => {
      const d = JSON.parse(ev.data);
      document.getElementById('speed').textContent = Math.round(d.speed ?? 0);
      const pzb = rawVals(d.pzbActive);            // raw passthrough field
      document.getElementById('pzb').textContent = pzb ? (pzb.Value ? 'PZB ON' : 'PZB') : '—';
    };
  </script>
</body>
</html>
```

See `example_minimal.html` for a fuller annotated version.

---

## 5. Checklist before you hand back the file
- [ ] Single `.html`, all CSS/JS inline.
- [ ] Every asset path is absolute (`/...`) or a full `https://` URL.
- [ ] Reads `/stream`, parses JSON in `onmessage`.
- [ ] Core fields read directly; raw fields read via `.Values` with `NodeValid`
      guards; hash-suffixed struct keys matched by prefix.
- [ ] Degrades gracefully when fields are missing/invalid (no crashes, blank or
      hidden widgets).
- [ ] Filename is lowercase letters/digits/`_`/`-` only (it becomes the URL slug).
