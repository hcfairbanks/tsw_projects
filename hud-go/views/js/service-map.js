// Shared service-map renderer used by every Leaflet map that wants the
// "service map" layer set: per-category track layers (platform / siding /
// line / running), plus point markers (stops / platforms / signals /
// switches), a layer toggle bar, and persisted on/off preferences.
//
// Pages that use this:
//   - /timetables/{id} (show.html)        — embedded directly, predates this helper
//   - /timetables/{id}/view (view.html)   — embedded directly, predates this helper
//   - /map (live tracking)                — uses ServiceMap.attach
//   - /huds/desktop|tablet|mobile         — uses ServiceMap.attach
//
// API:
//   ServiceMap.attach(map, opts) → Promise<void>
//
// opts:
//   routeId          (required) route to fetch /api/routes/<id>/map-data for
//   barContainer     (required) Element to render the toggle bar inside
//   pathLatLngs      (optional) [[lat,lng], ...] of the service path. Used
//                    for proximity-based filtering of off-path features.
//                    Omit/empty → no proximity filter (everything shown).
//   scheduleEntries  (optional) array of {location, structure,
//                    structure_number} tuples. Used for STRICT filtering
//                    of platforms/platform_track when present.
//   pathLayer        (optional) Leaflet layer (typically a polyline) for
//                    the path. Pass it in so the "Path" toggle hides/
//                    shows the caller's polyline. The caller should NOT
//                    .addTo(map) it themselves — the helper does.
//   stopLayers       (optional) array of Leaflet layers for caller-drawn
//                    timetable stops. Toggled by the "Stops" checkbox.
//                    Same rule: don't addTo(map) yourself.
//   hideBar          (optional) when true, layer prefs are still applied
//                    from storageKey but the in-map toggle bar is not
//                    rendered. Used by space-constrained HUDs that surface
//                    the toggles on /settings instead.
//   storageKey       (optional) localStorage key for layer prefs.
//                    Default: 'huddev.serviceMap.layers'
//   signalProxM      (optional) signal proximity threshold (m). Default 3.
//   markerProxM      (optional) generic point proximity threshold (m).
//                    Default 50.

(function () {
    'use strict';

    var DEFAULT_STORAGE_KEY = 'huddev.serviceMap.layers';
    var LAYER_KEYS = [
        'path', 'platform_track', 'siding_track', 'line_track',
        'running_track', 'stops', 'platforms', 'signals', 'switches'
    ];

    var TRACK_FEATURE_STYLE = {
        platform_track: { color: '#ff7f00', weight: 5, opacity: 0.85 },
        siding_track:   { color: '#9467bd', weight: 4, opacity: 0.7  },
        line_track:     { color: '#17becf', weight: 4, opacity: 0.7  },
        running_track:  { color: '#1f77b4', weight: 4, opacity: 0.7  }
    };

    function escapeHtml(s) {
        if (s == null) return '';
        var d = document.createElement('div');
        d.textContent = String(s);
        return d.innerHTML;
    }

    function distMeters(lat1, lng1, lat2, lng2) {
        var R = 6371000;
        var dLat = (lat2 - lat1) * Math.PI / 180;
        var dLng = (lng2 - lng1) * Math.PI / 180;
        var m = (lat1 + lat2) * 0.5 * Math.PI / 180;
        var x = dLng * Math.cos(m);
        return Math.hypot(dLat, x) * R;
    }

    function minDistanceToPath(lat, lng, pathLatLngs) {
        var best = Infinity;
        for (var i = 0; i < pathLatLngs.length; i++) {
            var d = distMeters(lat, lng, pathLatLngs[i][0], pathLatLngs[i][1]);
            if (d < best) best = d;
            if (best < 1) return best;
        }
        return best;
    }

    // Tight: point-to-segment distance via perpendicular projection clamped
    // to endpoints. Used for signals where the threshold (a few metres) is
    // smaller than the path's vertex sampling gaps.
    function minDistanceToPathSegments(lat, lng, pathLatLngs) {
        if (pathLatLngs.length === 0) return Infinity;
        if (pathLatLngs.length === 1) {
            return distMeters(lat, lng, pathLatLngs[0][0], pathLatLngs[0][1]);
        }
        var best = Infinity;
        for (var i = 0; i + 1 < pathLatLngs.length; i++) {
            var a = pathLatLngs[i], b = pathLatLngs[i + 1];
            var ax = a[1], ay = a[0], bx = b[1], by = b[0];
            var dx = bx - ax, dy = by - ay;
            var len2 = dx * dx + dy * dy;
            var fx = ax, fy = ay;
            if (len2 > 0) {
                var t = ((lng - ax) * dx + (lat - ay) * dy) / len2;
                if (t < 0) t = 0; else if (t > 1) t = 1;
                fx = ax + t * dx;
                fy = ay + t * dy;
            }
            var d = distMeters(lat, lng, fy, fx);
            if (d < best) best = d;
            if (best < 0.3) return best;
        }
        return best;
    }

    function buildToggleBar(container) {
        // Inject the bar HTML once. Idempotent — re-attaching to the same
        // container reuses the existing bar.
        if (container.querySelector('[data-svc-layer-bar="1"]')) return;
        container.innerHTML = ''
            + '<div data-svc-layer-bar="1" style="margin-top:10px;padding:12px 14px;background:var(--bg-tertiary,#222);border-radius:6px;font-size:14px;">'
            + '  <div style="display:flex;flex-wrap:wrap;align-items:center;gap:14px 18px;">'
            + '    <strong style="font-size:14px;">Layers:</strong>'
            + layerLabel('path',           'Path',           '<span style="display:inline-block;width:28px;height:4px;background:#0066ff;border-radius:2px"></span>')
            + layerLabel('platform_track', 'Platform tracks','<span style="display:inline-block;width:28px;height:6px;background:#ff7f00;border-radius:2px"></span>')
            + layerLabel('siding_track',   'Siding tracks',  '<span style="display:inline-block;width:28px;height:5px;background:#9467bd;border-radius:2px"></span>')
            + layerLabel('line_track',     'Line tracks',    '<span style="display:inline-block;width:28px;height:5px;background:#17becf;border-radius:2px"></span>')
            + layerLabel('running_track',  'Running tracks', '<span style="display:inline-block;width:28px;height:5px;background:#1f77b4;border-radius:2px"></span>')
            + layerLabel('stops',          'Stops',          '<span style="display:inline-block;width:14px;height:14px;border-radius:50%;background:#00cc66;border:2px solid #fff;box-shadow:0 0 0 1px rgba(0,0,0,0.25)"></span>')
            + layerLabel('platforms',      'Platforms',      '<span style="display:inline-block;width:14px;height:14px;border-radius:50%;background:#ff7f00;border:2px solid #fff;box-shadow:0 0 0 1px rgba(0,0,0,0.25)"></span>')
            + layerLabel('signals',        'Signals',        '<span style="display:inline-block;width:14px;height:14px;border-radius:50%;background:#ffd700;border:2px solid #806800"></span>')
            + layerLabel('switches',       'Switches',       '<span style="display:inline-block;width:14px;height:14px;border-radius:50%;background:#cc8400;border:2px solid #663300"></span>')
            + '  </div>'
            + '</div>';
    }

    function layerLabel(key, label, swatchHtml) {
        return '<label style="display:inline-flex;align-items:center;gap:6px;cursor:pointer;">'
            + '<input type="checkbox" data-svc-layer="' + key + '" checked style="width:16px;height:16px;">'
            + swatchHtml
            + '<span>' + label + '</span>'
            + '</label>';
    }

    function loadPrefs(key) {
        try { return JSON.parse(localStorage.getItem(key) || 'null') || {}; }
        catch (_) { return {}; }
    }
    function savePrefs(key, prefs) {
        try { localStorage.setItem(key, JSON.stringify(prefs)); }
        catch (_) {}
    }

    async function attach(map, opts) {
        if (!map || !opts || !opts.barContainer) {
            console.warn('ServiceMap.attach: missing barContainer', opts);
            return;
        }
        // routeId is optional now — without it the route-derived layers stay
        // empty and only caller-owned path/stops appear in the bar.
        var pathLatLngs = Array.isArray(opts.pathLatLngs) ? opts.pathLatLngs : [];
        var entries = Array.isArray(opts.scheduleEntries) ? opts.scheduleEntries : [];
        var storageKey = opts.storageKey || DEFAULT_STORAGE_KEY;
        var signalProxM = typeof opts.signalProxM === 'number' ? opts.signalProxM : 3;
        var markerProxM = typeof opts.markerProxM === 'number' ? opts.markerProxM : 50;

        var hideBar = !!opts.hideBar;
        if (!hideBar) buildToggleBar(opts.barContainer);
        var bar = hideBar ? null : opts.barContainer.querySelector('[data-svc-layer-bar="1"]');

        // Build empty layer groups; we'll fill them as features arrive.
        var groups = {};
        LAYER_KEYS.forEach(function (k) { groups[k] = L.layerGroup(); });

        // Caller-owned layers (path polyline, stop markers) belong in their
        // matching groups so the bar's checkboxes actually toggle them.
        if (opts.pathLayer) {
            // If the caller already added the polyline to the map, take it back
            // — having it in both the map and a group hides the toggle effect.
            if (map.hasLayer(opts.pathLayer)) map.removeLayer(opts.pathLayer);
            groups.path.addLayer(opts.pathLayer);
        }
        if (Array.isArray(opts.stopLayers)) {
            opts.stopLayers.forEach(function (l) {
                if (!l) return;
                if (map.hasLayer(l)) map.removeLayer(l);
                groups.stops.addLayer(l);
            });
        }

        // Build the schedule-strict structure set. Index by both the full
        // (loc, structure, num) tuple and a (loc, num) fallback so callers
        // whose entries lack the `structure` field (notably /api/map/route-
        // data, which doesn't carry structure type) still get matches —
        // the structure_number alone is usually unambiguous within one
        // station for a given service.
        var usedStructuresFull = new Set();
        var usedStructuresLoose = new Set();
        // Defensive: coerce property values to string before .trim(). Some
        // GeoJSON property keys (e.g. cab_stop_sign features' `location`)
        // collide with this function's expected string fields but carry a
        // numeric scalar — without coercion, .trim() throws.
        function strProp(v) { return String(v == null ? '' : v).trim(); }

        entries.forEach(function (e) {
            var loc = strProp(e.location);
            var num = strProp(e.structure_number);
            if (!loc || !num) return;
            var st = strProp(e.structure);
            usedStructuresFull.add(loc + '|' + st + '|' + num);
            usedStructuresLoose.add(loc + '|' + num);
        });
        function structureMatchesSchedule(p) {
            var loc = strProp(p.location);
            var num = strProp(p.structure_number);
            if (!loc || !num) return false;
            var st = strProp(p.structure);
            return usedStructuresFull.has(loc + '|' + st + '|' + num)
                || usedStructuresLoose.has(loc + '|' + num);
        }
        function lineNearPath(geom) {
            if (pathLatLngs.length === 0) return true; // no path → don't filter
            var lines = (geom.type === 'MultiLineString') ? geom.coordinates : [geom.coordinates];
            for (var i = 0; i < lines.length; i++) {
                var line = lines[i];
                for (var j = 0; j < line.length; j++) {
                    var c = line[j];
                    if (minDistanceToPath(c[1], c[0], pathLatLngs) <= markerProxM) return true;
                }
            }
            return false;
        }

        // Fetch route map-data (skip when no route id — route-derived layers
        // stay empty in that case).
        var feats = [];
        if (opts.routeId) {
            try {
                var r = await fetch('/api/routes/' + opts.routeId + '/map-data');
                if (r.ok) {
                    var md = await r.json();
                    feats = (md && Array.isArray(md.coordinates)) ? md.coordinates : [];
                }
            } catch (e) {
                console.warn('ServiceMap.attach: route features fetch failed', e);
            }
        }

        var hasEntries = entries.length > 0;
        var hasPath = pathLatLngs.length > 0;

        feats.forEach(function (feat) {
            var geom = feat.geometry;
            if (!geom) return;
            var p = feat.properties || {};
            // Track-feature LineStrings.
            if ((geom.type === 'LineString' || geom.type === 'MultiLineString') && p.feature_type) {
                var group = groups[p.feature_type];
                if (!group) return;
                if (p.feature_type === 'platform_track') {
                    // Strict schedule-match when we have a schedule, else
                    // proximity to path (best-effort), else show.
                    if (hasEntries) {
                        if (!structureMatchesSchedule(p)) return;
                    } else if (hasPath) {
                        if (!lineNearPath(geom)) return;
                    }
                } else {
                    var named = p.structure && p.structure_number;
                    if (hasEntries && named) {
                        if (!structureMatchesSchedule(p)) return;
                    } else if (hasPath) {
                        if (!lineNearPath(geom)) return;
                    }
                }
                var layer = L.geoJSON(feat, {
                    style: TRACK_FEATURE_STYLE[p.feature_type] || { color: '#0066ff', weight: 3, opacity: 0.8 }
                });
                var popupParts = [];
                if (p.location) popupParts.push('<b>' + escapeHtml(p.location) + '</b>');
                var sub = [p.structure, p.structure_number].filter(Boolean).join(' ');
                if (sub) popupParts.push(escapeHtml(sub));
                if (p.length_m != null) popupParts.push('Length: ' + p.length_m + ' m');
                if (popupParts.length) {
                    layer.eachLayer(function (l) { l.bindPopup(popupParts.join('<br>')); });
                }
                group.addLayer(layer);
                return;
            }
            // Point features: platforms / signals / switches.
            //
            // Skip pak-derived feature kinds that have their own DB tables and
            // are consumed elsewhere (cab_stop_signs feed the HUD's distance
            // calc; track_markers drive Go-Via lookups). They share the
            // FeatureCollection with the platform/signal/switch points but
            // would crash this layer pipeline because their `location`
            // property is a numeric scalar, not a station-name string.
            if (geom.type !== 'Point') return;
            if (p.feature_kind === 'cab_stop_sign' || p.feature_kind === 'track_marker') return;
            var c = geom.coordinates;
            if (!Array.isArray(c) || c.length < 2) return;
            var flng = c[0], flat = c[1];
            var groupKey = 'platforms', fill = '#ff7f00', radius = 4;
            if (p.signal_id) { groupKey = 'signals'; fill = '#ffd700'; radius = 3; }
            else if (p.jct_guid != null) { groupKey = 'switches'; fill = '#cc8400'; }

            if (groupKey === 'platforms') {
                if (hasEntries) {
                    if (!structureMatchesSchedule(p)) return;
                } else if (hasPath) {
                    if (minDistanceToPath(flat, flng, pathLatLngs) > markerProxM) return;
                }
            } else if (groupKey === 'signals') {
                if (hasPath && minDistanceToPathSegments(flat, flng, pathLatLngs) > signalProxM) return;
            } else {
                if (hasPath && minDistanceToPath(flat, flng, pathLatLngs) > markerProxM) return;
            }

            var m = L.circleMarker([flat, flng], {
                radius: radius, color: '#ffffff', weight: 1.5,
                fillColor: fill, fillOpacity: 1
            });
            var label = '';
            if (p.name) {
                var ssub = [p.structure, p.structure_number].filter(Boolean).join(' ');
                label = ssub ? p.name + ' — ' + ssub : p.name;
            } else if (p.display_label) {
                label = p.display_label;
            }
            if (label) m.bindTooltip(label, { permanent: false, direction: 'right', offset: [6, 0] });
            var lines = [];
            if (p.name) lines.push('<b>' + escapeHtml(p.name) + '</b>');
            var psub = [p.structure, p.structure_number].filter(Boolean).join(' ');
            if (psub) lines.push(escapeHtml(psub));
            if (p.signal_id) lines.push('Signal ID: <code>' + escapeHtml(p.signal_id) + '</code>');
            if (p.signal_type) lines.push('Type: ' + escapeHtml(p.signal_type));
            if (lines.length) m.bindPopup(lines.join('<br>'));
            groups[groupKey].addLayer(m);
        });

        // Apply persisted prefs (default all-on) and add visible groups to the map.
        var prefs = loadPrefs(storageKey);
        LAYER_KEYS.forEach(function (k) {
            var visible = prefs[k] === undefined ? true : !!prefs[k];
            if (bar) {
                var box = bar.querySelector('input[data-svc-layer="' + k + '"]');
                if (box) box.checked = visible;
            }
            if (visible) groups[k].addTo(map);
        });

        if (bar) {
            // Hide the bar only if NOTHING is toggleable.
            var totalCount = LAYER_KEYS.reduce(function (n, k) {
                return n + groups[k].getLayers().length;
            }, 0);
            bar.style.display = totalCount > 0 ? 'block' : 'none';

            // Wire toggles.
            bar.querySelectorAll('input[type="checkbox"][data-svc-layer]').forEach(function (box) {
                box.onchange = function () {
                    var k = box.getAttribute('data-svc-layer');
                    var grp = groups[k];
                    if (!grp) return;
                    if (box.checked) grp.addTo(map);
                    else map.removeLayer(grp);
                    var p2 = loadPrefs(storageKey);
                    p2[k] = box.checked;
                    savePrefs(storageKey, p2);
                };
            });
        }

        return {
            groups: groups,
            destroy: function () {
                LAYER_KEYS.forEach(function (k) { if (groups[k]) map.removeLayer(groups[k]); });
                if (bar) bar.remove();
            }
        };
    }

    window.ServiceMap = { attach: attach };
})();
