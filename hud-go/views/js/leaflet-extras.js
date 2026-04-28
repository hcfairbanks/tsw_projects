'use strict';

/**
 * Leaflet helpers shared by the route + service maps:
 *   - basemap layers (Map / Satellite / Topo) with a layer-switcher control
 *   - a fullscreen-toggle control built on the browser Fullscreen API
 *
 * No external dependencies. Tile sources are all keyless.
 */
(function (global) {
    function makeBaseLayers() {
        var carto = L.tileLayer('https://{s}.basemaps.cartocdn.com/rastertiles/voyager/{z}/{x}/{y}{r}.png', {
            subdomains: 'abcd',
            maxZoom: 19,
            attribution: '&copy; OpenStreetMap contributors &copy; CARTO'
        });
        var sat = L.tileLayer('https://server.arcgisonline.com/ArcGIS/rest/services/World_Imagery/MapServer/tile/{z}/{y}/{x}', {
            maxZoom: 19,
            attribution: 'Tiles &copy; Esri &mdash; Source: Esri, Maxar, Earthstar Geographics, and the GIS User Community'
        });
        var topo = L.tileLayer('https://{s}.tile.opentopomap.org/{z}/{x}/{y}.png', {
            subdomains: 'abc',
            maxZoom: 17,
            attribution: 'Map data: &copy; OpenStreetMap contributors, SRTM | Map style: &copy; OpenTopoMap (CC-BY-SA)'
        });
        return { 'Map': carto, 'Satellite': sat, 'Topo': topo };
    }

    /**
     * Attach the basemap switcher to a map. Adds the default 'Map' layer and a
     * Leaflet layer-switcher in the top-right.
     */
    function attachBasemapControls(map) {
        var layers = makeBaseLayers();
        layers['Map'].addTo(map);
        L.control.layers(layers, null, { position: 'topright', collapsed: true }).addTo(map);
        return layers;
    }

    /**
     * Add a fullscreen toggle button to the map. `targetSelector` is the
     * element that should go fullscreen (typically the map's container so the
     * layer toggle bar stays visible). Calls map.invalidateSize() on
     * fullscreen-state changes so Leaflet redraws cleanly.
     */
    function attachFullscreenControl(map, targetSelector) {
        var FullscreenCtl = L.Control.extend({
            options: { position: 'topright' },
            onAdd: function () {
                var container = L.DomUtil.create('div', 'leaflet-bar leaflet-control');
                var a = L.DomUtil.create('a', 'leaflet-fullscreen-btn', container);
                a.href = '#';
                a.title = 'Toggle fullscreen';
                a.setAttribute('role', 'button');
                a.setAttribute('aria-label', 'Toggle fullscreen');
                a.innerHTML = '⛶'; // ⛶ fullscreen glyph
                a.style.cssText =
                    'display:flex;align-items:center;justify-content:center;' +
                    'width:30px;height:30px;font-size:18px;font-weight:bold;' +
                    'color:#222;background:#fff;text-decoration:none;cursor:pointer;';
                L.DomEvent.disableClickPropagation(a);
                L.DomEvent.on(a, 'click', function (e) {
                    L.DomEvent.preventDefault(e);
                    var target = document.querySelector(targetSelector);
                    if (!target) return;
                    if (!document.fullscreenElement) {
                        if (target.requestFullscreen) target.requestFullscreen();
                        else if (target.webkitRequestFullscreen) target.webkitRequestFullscreen();
                    } else {
                        if (document.exitFullscreen) document.exitFullscreen();
                        else if (document.webkitExitFullscreen) document.webkitExitFullscreen();
                    }
                });
                return container;
            }
        });
        map.addControl(new FullscreenCtl());
        var redraw = function () { setTimeout(function () { map.invalidateSize(); }, 100); };
        document.addEventListener('fullscreenchange', redraw);
        document.addEventListener('webkitfullscreenchange', redraw);
    }

    global.LeafletExtras = {
        makeBaseLayers: makeBaseLayers,
        attachBasemapControls: attachBasemapControls,
        attachFullscreenControl: attachFullscreenControl
    };
})(window);
