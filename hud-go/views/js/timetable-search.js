// Reusable timetable search/listing component.
//
// Renders a filter card + paginated results table into a container element,
// backed by /api/timetables/paginated. Same look-and-behaviour everywhere
// it's used — single source of truth.
//
// Usage:
//   TimetableSearch.init(containerEl, opts?)
//
// opts:
//   routeId    (optional) preselect & lock the Route filter (route page)
//   countryId  (optional) preselect the Country filter
//   formationId (optional) preselect the Formation filter
//   readUrlParams  (optional, default false) — also read the same presets
//                  from the page's URL query string (used by /timetables)
//   lockRoute  (optional, derived true when routeId set) — hide the Route
//              filter so the user can't change it
//
// The component uses one DOM ID per element internally — only one instance
// per page is supported. (Both /timetables and /routes/<id> have a single
// instance, so this is fine.)

(function (global) {
    'use strict';

    // Module-owned CSS scoped to .tts-card. Page-level rules for
    // .typeahead-item / .filter-box / .pagination etc. vary across views,
    // which is why /timetables and /routes/<id> looked different despite
    // running identical code. The styles below are scoped (`.tts-card …`)
    // so they win inside the component without leaking back out.
    var STYLES = ''
        + '.tts-card { color: var(--text-primary); }'
        + '.tts-card .filter-box { display: flex; gap: 10px; margin-bottom: 15px; align-items: center; flex-wrap: wrap; }'
        + '.tts-card .filter-box label { color: var(--text-secondary); white-space: nowrap; }'
        + '.tts-card .filter-box select, .tts-card .filter-box input { padding: 10px; border: 1px solid var(--border-color); border-radius: 4px; background: var(--bg-primary); color: var(--text-primary); font-size: 14px; }'
        + '.tts-card .filter-box select:focus, .tts-card .filter-box input:focus { outline: none; border-color: var(--accent-color); }'
        + '.tts-card .filter-box select { min-width: 200px; }'
        + '.tts-card .filter-box input { flex: 1; min-width: 150px; }'
        + '.tts-card .filter-box button { padding: 10px 20px; border: none; border-radius: 4px; cursor: pointer; font-size: 14px; }'
        + '.tts-card .filter-box button.btn-primary { background: var(--btn-primary-bg); color: #fff; }'
        + '.tts-card .filter-box button.btn-primary:hover { background: var(--btn-primary-hover); }'
        + '.tts-card .filter-box button.btn-secondary { background: var(--bg-tertiary); color: var(--text-primary); }'
        + '.tts-card .filter-box button.btn-secondary:hover { background: var(--accent-hover); }'
        + '.tts-card .table-wrapper { overflow-x: auto; }'
        + '.tts-card table { width: 100%; border-collapse: collapse; margin-top: 15px; }'
        + '.tts-card th, .tts-card td { padding: 12px; text-align: left; border-bottom: 1px solid var(--border-color); white-space: nowrap; }'
        + '.tts-card th.tts-c, .tts-card td.tts-c { text-align: center; }'
        + '.tts-card th { background: var(--bg-tertiary); color: var(--accent-color); }'
        + '.tts-card tr:hover { background: var(--bg-primary); }'
        + '.tts-card td a { color: var(--accent-color); text-decoration: none; }'
        + '.tts-card td a:hover { text-decoration: underline; }'
        + '.tts-card .actions { white-space: nowrap; }'
        + '.tts-card .actions button { padding: 5px 10px; font-size: 12px; margin-right: 5px; border: none; border-radius: 4px; cursor: pointer; }'
        + '.tts-card .actions .btn-primary { background: var(--btn-primary-bg); color: #fff; }'
        + '.tts-card .actions .btn-secondary { background: var(--bg-tertiary); color: var(--text-primary); }'
        + '.tts-card .actions .btn-danger { background: var(--btn-danger-bg); color: #fff; }'
        + '.tts-card .empty-message { text-align: center; color: var(--text-muted); padding: 20px; }'
        + '.tts-card .typeahead-container { position: relative; flex: 1; min-width: 150px; }'
        + '.tts-card .typeahead-container input { width: 100%; padding: 10px; border: 1px solid var(--border-color); border-radius: 4px; background: var(--bg-primary); color: var(--text-primary); font-size: 14px; }'
        + '.tts-card .typeahead-container input:focus { outline: none; border-color: var(--accent-color); }'
        + '.tts-card .typeahead-dropdown { display: none; position: absolute; top: 100%; left: 0; right: 0; max-height: 250px; overflow-y: auto; background: var(--bg-secondary); border: 1px solid var(--border-color); border-top: none; border-radius: 0 0 4px 4px; z-index: 1001; }'
        + '.tts-card .typeahead-item { padding: 10px 12px; cursor: pointer; font-size: 13px; border-bottom: 1px solid var(--border-color); }'
        + '.tts-card .typeahead-item:last-child { border-bottom: none; }'
        + '.tts-card .typeahead-item:hover, .tts-card .typeahead-item.highlighted { background: var(--bg-tertiary); }'
        + '.tts-card .typeahead-item .item-name { font-weight: 600; color: var(--text-primary); }'
        + '.tts-card .typeahead-item .item-sub { font-size: 11px; color: var(--text-muted); }'
        + '.tts-card .pagination { display: flex; justify-content: center; align-items: center; gap: 10px; margin-top: 20px; flex-wrap: wrap; }'
        + '.tts-card .pagination button { min-width: 40px; padding: 8px 14px; border: none; border-radius: 4px; cursor: pointer; font-size: 14px; background: var(--bg-tertiary); color: var(--text-primary); }'
        + '.tts-card .pagination button:hover:not(:disabled) { background: var(--accent-hover); }'
        + '.tts-card .pagination button:disabled { opacity: 0.5; cursor: not-allowed; }'
        + '.tts-card .pagination-info { color: var(--text-muted); font-size: 14px; }'
        + '.tts-card .per-page { display: flex; align-items: center; gap: 8px; }'
        + '.tts-card .per-page select { padding: 8px; border: 1px solid var(--border-color); border-radius: 4px; background: var(--bg-primary); color: var(--text-primary); font-size: 14px; }'
        + '.tts-card .filter-country-container { position: relative; width: 200px; flex-shrink: 0; display: inline-block; }'
        + '.tts-card .country-select-display { width: 100%; padding: 8px 10px; border: 1px solid var(--border-color); border-radius: 4px; background: var(--bg-primary); color: var(--text-primary); font-size: 14px; cursor: pointer; display: flex; align-items: center; justify-content: space-between; box-sizing: border-box; }'
        + '.tts-card .country-select-display:hover { border-color: var(--accent-color); }'
        + '.tts-card .country-select-display::after { content: "\\25BC"; font-size: 10px; color: var(--text-muted); }'
        + '.tts-card .country-select-display.open::after { content: "\\25B2"; }'
        + '.tts-card .country-dropdown { display: none; position: absolute; top: 100%; left: 0; right: 0; max-height: 250px; overflow-y: auto; background: var(--bg-secondary); border: 1px solid var(--border-color); border-top: none; border-radius: 0 0 4px 4px; z-index: 1002; }'
        + '.tts-card .country-dropdown.open { display: block; }'
        + '.tts-card .country-dropdown-item { padding: 8px 12px; cursor: pointer; display: flex; align-items: center; border-bottom: 1px solid var(--border-color); }'
        + '.tts-card .country-dropdown-item:last-child { border-bottom: none; }'
        + '.tts-card .country-dropdown-item:hover { background: var(--bg-tertiary); }';

    function injectStyles() {
        if (document.getElementById('tts-module-styles')) return;
        var style = document.createElement('style');
        style.id = 'tts-module-styles';
        style.textContent = STYLES;
        document.head.appendChild(style);
    }

    var SHELL_HTML = ''
        + '<div class="tts-card">'
        + '  <div class="filter-box tts-filter-row1">'
        + '    <label data-i18n="timetables.serviceName">Service:</label>'
        + '    <div class="typeahead-container">'
        + '      <input type="text" id="tts_serviceInput" data-i18n-placeholder="common.search" placeholder="Type to search services..." autocomplete="off">'
        + '      <div id="tts_serviceDropdown" class="typeahead-dropdown"></div>'
        + '      <input type="hidden" id="tts_serviceFilter" value="">'
        + '      <input type="hidden" id="tts_serviceFilterType" value="">'
        + '    </div>'
        + '    <label class="tts-country-label" data-i18n="routes.country">Country:</label>'
        + '    <div class="filter-country-container tts-country-wrap" id="tts_filterCountryContainer">'
        + '      <div class="country-select-display" id="tts_filterCountryDisplay">'
        + '        <span data-i18n="common.all">All</span>'
        + '      </div>'
        + '      <div class="country-dropdown" id="tts_filterCountryDropdown"></div>'
        + '      <input type="hidden" id="tts_countryFilter" value="">'
        + '    </div>'
        + '    <label class="tts-route-label" data-i18n="timetables.route">Route:</label>'
        + '    <div class="typeahead-container tts-route-wrap">'
        + '      <input type="text" id="tts_routeInput" data-i18n-placeholder="common.search" placeholder="Type to search routes..." autocomplete="off">'
        + '      <div id="tts_routeDropdown" class="typeahead-dropdown"></div>'
        + '      <input type="hidden" id="tts_routeFilter" value="">'
        + '    </div>'
        + '    <label class="tts-section-wrap" data-i18n="timetables.section">Section:</label>'
        + '    <select class="tts-section-wrap" id="tts_sectionFilter">'
        + '      <option value="" data-i18n="common.all">All</option>'
        + '      <option value="__none__" data-i18n="timetables.noSection">No Section</option>'
        + '    </select>'
        + '    <label data-i18n="formations.title">Formation:</label>'
        + '    <div class="typeahead-container">'
        + '      <input type="text" id="tts_formationInput" data-i18n-placeholder="common.search" placeholder="Type to search formations..." autocomplete="off">'
        + '      <div id="tts_formationDropdown" class="typeahead-dropdown"></div>'
        + '      <input type="hidden" id="tts_formationFilter" value="">'
        + '    </div>'
        + '  </div>'
        + '  <div class="filter-box tts-filter-row2">'
        + '    <label data-i18n="timetables.startTime">Start Time:</label>'
        + '    <input type="text" id="tts_startTimeMin" title="From" placeholder="HH:MM" maxlength="5" style="min-width: 70px; max-width: 80px; flex: 0;">'
        + '    <span style="color: var(--text-muted);">-</span>'
        + '    <input type="text" id="tts_startTimeMax" title="To" placeholder="HH:MM" maxlength="5" style="min-width: 70px; max-width: 80px; flex: 0;">'
        + '    <label data-i18n="timetables.duration">Duration:</label>'
        + '    <input type="text" id="tts_durationMin" title="Min" placeholder="HH:MM" maxlength="5" style="min-width: 70px; max-width: 80px; flex: 0;">'
        + '    <span style="color: var(--text-muted);">-</span>'
        + '    <input type="text" id="tts_durationMax" title="Max" placeholder="HH:MM" maxlength="5" style="min-width: 70px; max-width: 80px; flex: 0;">'
        + '    <label data-i18n="timetables.stops">Stops:</label>'
        + '    <input type="number" id="tts_stopsMin" title="Min" placeholder="Min" min="0" style="min-width: 70px; max-width: 80px; flex: 0;">'
        + '    <span style="color: var(--text-muted);">-</span>'
        + '    <input type="number" id="tts_stopsMax" title="Max" placeholder="Max" min="0" style="min-width: 70px; max-width: 80px; flex: 0;">'
        + '    <label data-i18n="timetables.conductor">Conductor:</label>'
        + '    <select id="tts_conductorFilter" style="min-width: 80px; width: auto;">'
        + '      <option value="" data-i18n="common.all">All</option>'
        + '      <option value="yes" data-i18n="common.yes">Yes</option>'
        + '      <option value="no" data-i18n="common.no">No</option>'
        + '    </select>'
        + '    <label data-i18n="timetables.serviceTypeLabel">Type:</label>'
        + '    <select id="tts_serviceTypeFilter" style="min-width: 100px; width: auto;">'
        + '      <option value="" data-i18n="common.all">All</option>'
        + '      <option value="passenger" data-i18n="timetables.passenger">Passenger</option>'
        + '      <option value="freight" data-i18n="timetables.freight">Freight</option>'
        + '    </select>'
        + '    <label class="tts-playable-wrap" data-i18n="timetables.playable">Playable:</label>'
        + '    <select class="tts-playable-wrap" id="tts_playableFilter" style="min-width: 80px; width: auto;">'
        + '      <option value="" data-i18n="common.all">All</option>'
        + '      <option value="yes" data-i18n="common.yes">Yes</option>'
        + '      <option value="no" data-i18n="common.no">No</option>'
        + '    </select>'
        + '    <label data-i18n="timetables.source">Source:</label>'
        + '    <select id="tts_sourceFilter" style="min-width: 110px; width: auto;">'
        + '      <option value="" data-i18n="common.all">All</option>'
        + '      <option value="Timetable" data-i18n="timetables.sourceTimetable">Timetable</option>'
        + '      <option value="Scenario" data-i18n="timetables.sourceScenario">Scenario</option>'
        + '      <option value="Training" data-i18n="timetables.sourceTraining">Training</option>'
        + '    </select>'
        + '    <label class="tts-coord-wrap" data-i18n="timetables.coordSource">Coord Source:</label>'
        + '    <select class="tts-coord-wrap" id="tts_coordSourceFilter" style="min-width: 110px; width: auto;">'
        + '      <option value="" data-i18n="common.all">All</option>'
        + '      <option value="backend" data-i18n="timetables.backend">Backend</option>'
        + '      <option value="automatic" data-i18n="timetables.automatic">Automatic</option>'
        + '      <option value="imported" data-i18n="timetables.imported">Imported</option>'
        + '      <option value="predictive" data-i18n="timetables.predictive">Predictive</option>'
        + '      <option value="none" data-i18n="timetables.none">None</option>'
        + '    </select>'
        + '    <div style="flex: 1;"></div>'
        + '    <button class="btn-secondary" id="tts_clearBtn" data-i18n="common.clear">Clear</button>'
        + '    <button class="btn-primary" id="tts_searchBtn" data-i18n="common.search">Search</button>'
        + '  </div>'
        + '  <div id="tts_timetablesList" class="table-wrapper">'
        + '    <p class="empty-message" data-i18n="timetables.loadingTimetables">Loading timetables...</p>'
        + '  </div>'
        + '  <div class="pagination" id="tts_pagination"></div>'
        + '</div>';

    function escapeHtml(text) {
        if (text == null) return '';
        var div = document.createElement('div');
        div.textContent = text;
        return div.innerHTML;
    }

    function countryCodeToFlag(code) {
        if (!code || code.length !== 2) return '';
        return '<span class="fi fi-' + code.toLowerCase() + '" style="margin-right: 5px;"></span>';
    }

    function tt(key, fallback) {
        return (typeof i18n !== 'undefined' && i18n.t) ? i18n.t(key) : fallback;
    }

    // ----- main -----

    function init(container, opts) {
        if (!container) { console.warn('TimetableSearch.init: missing container'); return; }
        opts = opts || {};
        injectStyles();

        // Read URL params if requested.
        if (opts.readUrlParams) {
            var qs = new URLSearchParams(window.location.search);
            if (!opts.routeId && qs.get('route_id')) opts.routeId = qs.get('route_id');
            if (!opts.countryId && qs.get('country_id')) opts.countryId = qs.get('country_id');
            if (!opts.formationId && qs.get('formation_id')) opts.formationId = qs.get('formation_id');
        }

        var lockRoute = opts.lockRoute !== undefined ? !!opts.lockRoute : !!opts.routeId;

        var state = {
            opts: opts,
            lockRoute: lockRoute,
            allRoutes: [],
            allFormations: [],
            allCountries: [],
            routeSummary: [],
            allTimetables: [],
            highlightedIndex: { service: -1, route: -1, formation: -1 },
            currentPage: 1,
            currentLimit: 15,
            devMode: false,
            sortColumn: 'service_name',
            sortDirection: 'asc'
        };

        container.innerHTML = SHELL_HTML;
        if (lockRoute) hideRouteFilter(container);

        // i18n: translate the freshly injected markup if available.
        if (typeof i18n !== 'undefined' && i18n.translate) {
            i18n.translate(container);
        }

        // Dev-mode flag drives extra columns + actions; cheap fetch.
        fetch('/api/config').then(function (r) { return r.json(); }).then(function (cfg) {
            state.devMode = !!(cfg && cfg.developmentMode);
        }).catch(function () {}).then(function () {
            // Dev-only filters: Coord Source (geometry provenance — noise
            // for end users) + Playable (server forces playable=1 outside
            // dev mode anyway, so the dropdown would do nothing).
            if (!state.devMode) {
                container.querySelectorAll('.tts-coord-wrap, .tts-playable-wrap').forEach(function (el) {
                    el.style.display = 'none';
                });
            }
            return loadData(container, state);
        });

        // Re-render on language change so button labels / column headers
        // pick up the new locale.
        document.addEventListener('languageChanged', function () {
            fetchAndRender(container, state, state.currentPage);
        });
    }

    function hideRouteFilter(container) {
        var ids = ['tts_routeInput', 'tts_routeDropdown', 'tts_routeFilter',
                   'tts_filterCountryContainer', 'tts_countryFilter'];
        ids.forEach(function (id) {
            var el = container.querySelector('#' + id);
            if (el) el.style.display = 'none';
        });
        var wrap = container.querySelector('.tts-route-wrap');
        if (wrap) wrap.style.display = 'none';
        var routeLbl = container.querySelector('.tts-route-label');
        if (routeLbl) routeLbl.style.display = 'none';
        var ctryLbl = container.querySelector('.tts-country-label');
        if (ctryLbl) ctryLbl.style.display = 'none';
        var ctryWrap = container.querySelector('.tts-country-wrap');
        if (ctryWrap) ctryWrap.style.display = 'none';
    }

    async function loadData(container, state) {
        try {
            // When the page is locked to one route, scope the formation list to
            // formations used on that route only — otherwise the typeahead
            // surfaces every formation in the DB, including ones belonging to
            // entirely different routes (e.g. Boston's CTC-3 Cab Car
            // formations showing up on the IoW page).
            var formationsUrl = (state.lockRoute && state.opts.routeId)
                ? ('/api/routes/' + state.opts.routeId + '/formations')
                : '/api/formations';
            var responses = await Promise.all([
                fetch('/api/timetables/route-summary'),
                fetch('/api/routes'),
                fetch(formationsUrl),
                fetch('/api/countries')
            ]);
            state.routeSummary = await responses[0].json();
            state.allRoutes = await responses[1].json();
            state.allFormations = await responses[2].json();
            state.allCountries = await responses[3].json();
            // /api/routes/<id>/formations returns {data: [...]} when wrapped in
            // util.Success — flatten so the typeahead/render code can treat
            // both endpoints the same.
            if (state.allFormations && !Array.isArray(state.allFormations) && Array.isArray(state.allFormations.data)) {
                state.allFormations = state.allFormations.data;
            }

            populateCountryFilter(container, state);
            setupTypeaheads(container, state);
            populateSourceFilter(container);
            applyPresets(container, state);
            wireButtons(container, state);
            fetchAndRender(container, state, 1);
        } catch (err) {
            console.error('TimetableSearch.loadData:', err);
            var list = container.querySelector('#tts_timetablesList');
            if (list) list.innerHTML = '<p class="empty-message">' + tt('timetables.errorLoading', 'Error loading timetables') + '</p>';
        }
    }

    // ---- presets ----

    function applyPresets(container, state) {
        if (state.opts.routeId) {
            var route = state.allRoutes.find(function (r) { return String(r.id) === String(state.opts.routeId); });
            if (route) {
                container.querySelector('#tts_routeFilter').value = route.id;
                container.querySelector('#tts_routeInput').value = route.name;
                updateSectionFilter(container, state, route.id);
            }
        }
        if (state.opts.countryId && !state.lockRoute) {
            var country = state.allCountries.find(function (c) { return String(c.id) === String(state.opts.countryId); });
            if (country) {
                container.querySelector('#tts_countryFilter').value = country.id;
                var disp = container.querySelector('#tts_filterCountryDisplay');
                disp.innerHTML = countryCodeToFlag(country.code || '') + escapeHtml(country.name);
            }
        }
        if (state.opts.formationId) {
            var formation = state.allFormations.find(function (t) { return String(t.id) === String(state.opts.formationId); });
            if (formation) {
                container.querySelector('#tts_formationFilter').value = formation.id;
                container.querySelector('#tts_formationInput').value = formation.name;
            }
        }
    }

    // ---- country dropdown ----

    function populateCountryFilter(container, state) {
        var dropdown = container.querySelector('#tts_filterCountryDropdown');
        var allText = tt('common.all', 'All');
        var html = '<div class="country-dropdown-item" data-id="" data-name="' + escapeHtml(allText) + '" data-code="">' + escapeHtml(allText) + '</div>';
        var routeIdsWithTimetables = new Set(state.routeSummary.map(function (s) { return s.route_id; }));
        var routeCountryIds = new Set(state.allRoutes.filter(function (r) { return routeIdsWithTimetables.has(r.id); }).map(function (r) { return r.country_id; }));
        state.allCountries.filter(function (c) { return routeCountryIds.has(c.id); }).forEach(function (c) {
            var flag = c.code ? countryCodeToFlag(c.code) : '';
            html += '<div class="country-dropdown-item" data-id="' + c.id + '" data-name="' + escapeHtml(c.name) + '" data-code="' + escapeHtml(c.code || '') + '">' + flag + escapeHtml(c.name) + '</div>';
        });
        dropdown.innerHTML = html;

        var display = container.querySelector('#tts_filterCountryDisplay');
        display.addEventListener('click', function () {
            var open = dropdown.classList.toggle('open');
            display.classList.toggle('open');
            if (open) {
                setTimeout(function () { document.addEventListener('click', closeOnOutside); }, 0);
            }
        });
        function closeOnOutside(e) {
            if (!container.querySelector('#tts_filterCountryContainer').contains(e.target)) {
                dropdown.classList.remove('open');
                display.classList.remove('open');
                document.removeEventListener('click', closeOnOutside);
            }
        }

        dropdown.querySelectorAll('.country-dropdown-item').forEach(function (item) {
            item.addEventListener('click', function () {
                container.querySelector('#tts_countryFilter').value = item.dataset.id;
                var flag = item.dataset.code ? countryCodeToFlag(item.dataset.code) : '';
                display.innerHTML = '<span>' + flag + escapeHtml(item.dataset.name) + '</span>';
                dropdown.classList.remove('open');
                display.classList.remove('open');
                // Country change resets route + section.
                container.querySelector('#tts_routeInput').value = '';
                container.querySelector('#tts_routeFilter').value = '';
                updateSectionFilter(container, state, '');
            });
        });
    }

    // ---- source dropdown ----

    function populateSourceFilter(container) {
        var sel = container.querySelector('#tts_sourceFilter');
        if (!sel) return;
        var existing = {};
        Array.prototype.forEach.call(sel.options, function (o) { existing[o.value] = true; });
        fetch('/api/timetables/sources').then(function (r) { return r.json(); }).then(function (values) {
            if (!Array.isArray(values)) return;
            values.forEach(function (v) {
                if (existing[v]) return;
                var opt = document.createElement('option');
                opt.value = v;
                opt.textContent = v;
                sel.appendChild(opt);
            });
        }).catch(function () { /* keep hardcoded options */ });
    }

    // ---- section dropdown ----

    // updateSectionFilter populates the section dropdown and toggles its
    // visibility for the picked route. Also flips state.hasSections so the
    // results table can drop the Section column when there's nothing to
    // show. When `state` is provided and the table has already rendered,
    // a re-render is triggered after the async fetch lands.
    function updateSectionFilter(container, state, routeId) {
        var select = container.querySelector('#tts_sectionFilter');
        select.innerHTML = '<option value="" data-i18n="common.all">All</option><option value="__none__" data-i18n="timetables.noSection">No Section</option>';
        if (typeof i18n !== 'undefined' && i18n.translate) i18n.translate(select);
        // Default optimistic: filter + column visible until we positively
        // know the route has zero sections.
        setSectionFilterVisible(container, true);
        if (state) state.hasSections = true;
        if (!routeId) return;
        fetch('/api/routes/' + routeId + '/sections').then(function (r) { return r.json(); }).then(function (sections) {
            sections = Array.isArray(sections) ? sections : [];
            sections.forEach(function (s) {
                var opt = document.createElement('option');
                opt.value = s.name;
                opt.textContent = s.name;
                select.appendChild(opt);
            });
            var hasSections = sections.length > 0;
            setSectionFilterVisible(container, hasSections);
            if (state && state.hasSections !== hasSections) {
                state.hasSections = hasSections;
                // Re-render the table only after a first render has
                // happened — otherwise the initial fetch is still inflight
                // and will pick up the right value already.
                if (state.firstRenderDone) fetchAndRender(container, state, state.currentPage);
            }
        }).catch(function (err) { console.error('TimetableSearch.updateSectionFilter:', err); });
    }

    function setSectionFilterVisible(container, visible) {
        container.querySelectorAll('.tts-section-wrap').forEach(function (el) {
            el.style.display = visible ? '' : 'none';
        });
    }

    // ---- typeaheads ----

    function setupTypeaheads(container, state) {
        setupServiceTypeahead(container, state);
        setupRouteTypeahead(container, state);
        setupFormationTypeahead(container, state);

        // Hide dropdowns when clicking outside any typeahead container.
        document.addEventListener('click', function (e) {
            if (!e.target.closest('.typeahead-container')) {
                ['tts_serviceDropdown', 'tts_routeDropdown', 'tts_formationDropdown'].forEach(function (id) {
                    var el = container.querySelector('#' + id);
                    if (el) el.style.display = 'none';
                });
            }
        });
    }

    function setupServiceTypeahead(container, state) {
        var input = container.querySelector('#tts_serviceInput');
        var dropdown = container.querySelector('#tts_serviceDropdown');
        var hidden = container.querySelector('#tts_serviceFilter');
        var hiddenType = container.querySelector('#tts_serviceFilterType');

        input.addEventListener('focus', function () { showServiceDropdown(container, state, this.value); });
        input.addEventListener('input', function () {
            state.highlightedIndex.service = -1;
            hidden.value = '';
            hiddenType.value = '';
            showServiceDropdown(container, state, this.value);
        });
        input.addEventListener('keydown', function (e) { handleTypeaheadKeydown(e, state, 'service', dropdown); });
    }

    function setupRouteTypeahead(container, state) {
        var input = container.querySelector('#tts_routeInput');
        var dropdown = container.querySelector('#tts_routeDropdown');
        var hidden = container.querySelector('#tts_routeFilter');

        input.addEventListener('focus', function () { showRouteDropdown(container, state, this.value); });
        input.addEventListener('input', function () {
            state.highlightedIndex.route = -1;
            hidden.value = '';
            showRouteDropdown(container, state, this.value);
            if (!this.value.trim()) updateSectionFilter(container, state, null);
        });
        input.addEventListener('keydown', function (e) { handleTypeaheadKeydown(e, state, 'route', dropdown); });
    }

    function setupFormationTypeahead(container, state) {
        var input = container.querySelector('#tts_formationInput');
        var dropdown = container.querySelector('#tts_formationDropdown');
        var hidden = container.querySelector('#tts_formationFilter');

        input.addEventListener('focus', function () { showFormationDropdown(container, state, this.value); });
        input.addEventListener('input', function () {
            state.highlightedIndex.formation = -1;
            hidden.value = '';
            showFormationDropdown(container, state, this.value);
        });
        input.addEventListener('keydown', function (e) { handleTypeaheadKeydown(e, state, 'formation', dropdown); });
    }

    function handleTypeaheadKeydown(e, state, type, dropdown) {
        var items = dropdown.querySelectorAll('.typeahead-item[data-id]');
        if (e.key === 'ArrowDown') {
            e.preventDefault();
            state.highlightedIndex[type] = Math.min(state.highlightedIndex[type] + 1, items.length - 1);
            updateHighlight(items, state.highlightedIndex[type]);
        } else if (e.key === 'ArrowUp') {
            e.preventDefault();
            state.highlightedIndex[type] = Math.max(state.highlightedIndex[type] - 1, 0);
            updateHighlight(items, state.highlightedIndex[type]);
        } else if (e.key === 'Enter') {
            e.preventDefault();
            if (state.highlightedIndex[type] >= 0 && items[state.highlightedIndex[type]]) {
                items[state.highlightedIndex[type]].click();
            }
        } else if (e.key === 'Escape') {
            dropdown.style.display = 'none';
        }
    }

    function updateHighlight(items, index) {
        items.forEach(function (item, idx) { item.classList.toggle('highlighted', idx === index); });
        if (index >= 0 && items[index]) items[index].scrollIntoView({ block: 'nearest' });
    }

    // ----- typeahead helpers (server-search + debounce) -----
    //
    // Each typeahead fires an actual API call against the backend so it
    // sees the entire result set, not just whatever's currently rendered
    // on the visible page. A 150 ms debounce keeps every keystroke from
    // hitting the server, and the latest-request-wins token handles the
    // race when fast typers fire two queries in quick succession.

    var TYPEAHEAD_DEBOUNCE_MS = 150;
    var TYPEAHEAD_LIMIT = 25;

    function makeDebouncedSearch() {
        var timer = null;
        var token = 0;
        return {
            schedule: function (fn) {
                clearTimeout(timer);
                token += 1;
                var myToken = token;
                timer = setTimeout(function () { fn(myToken); }, TYPEAHEAD_DEBOUNCE_MS);
                return myToken;
            },
            isCurrent: function (myToken) { return myToken === token; }
        };
    }

    var serviceSearcher = makeDebouncedSearch();
    var routeSearcher = makeDebouncedSearch();
    var formationSearcher = makeDebouncedSearch();

    function showServiceDropdown(container, state, filter) {
        var dropdown = container.querySelector('#tts_serviceDropdown');
        dropdown.style.display = 'block';
        // Show a quiet loading row immediately so the dropdown isn't empty
        // while the request is in flight.
        if (!dropdown.innerHTML.trim()) {
            dropdown.innerHTML = '<div class="typeahead-item" style="color: var(--text-muted);">…</div>';
        }
        serviceSearcher.schedule(function (myToken) {
            var params = new URLSearchParams({ page: 1, limit: TYPEAHEAD_LIMIT });
            if (filter) params.set('search', filter);
            // Scope search to current route when locked or selected.
            var routeId = state.lockRoute && state.opts.routeId
                ? state.opts.routeId
                : container.querySelector('#tts_routeFilter').value;
            if (routeId) params.set('route_id', routeId);
            fetch('/api/timetables/paginated?' + params.toString())
                .then(function (r) { return r.json(); })
                .then(function (result) {
                    if (!serviceSearcher.isCurrent(myToken)) return;
                    var rows = (result && result.data) || [];
                    var seen = {};
                    var items = [];
                    rows.forEach(function (t) {
                        if (t.service_name && !seen['name:' + t.service_name]) {
                            seen['name:' + t.service_name] = true;
                            items.push({ type: 'name', value: t.service_name, label: t.service_name });
                        }
                        if (t.service && !seen['code:' + t.service]) {
                            seen['code:' + t.service] = true;
                            items.push({ type: 'code', value: t.service, label: t.service });
                        }
                    });
                    if (items.length === 0) {
                        dropdown.innerHTML = '<div class="typeahead-item" style="color: var(--text-muted);">' + tt('timetables.noServicesFound', 'No services found') + '</div>';
                        return;
                    }
                    dropdown.innerHTML = items.map(function (item) {
                        var sub = item.type === 'code' ? tt('timetables.serviceCode', 'Service Code') : tt('timetables.serviceName', 'Service Name');
                        return '<div class="typeahead-item" data-id="' + escapeHtml(item.value) + '" data-name="' + escapeHtml(item.label) + '" data-type="' + item.type + '">'
                            + '<div class="item-name">' + escapeHtml(item.label) + '</div>'
                            + '<div class="item-sub">' + sub + '</div></div>';
                    }).join('');
                    dropdown.querySelectorAll('.typeahead-item[data-id]').forEach(function (item) {
                        item.addEventListener('click', function () {
                            container.querySelector('#tts_serviceInput').value = this.dataset.name;
                            container.querySelector('#tts_serviceFilter').value = this.dataset.id;
                            container.querySelector('#tts_serviceFilterType').value = this.dataset.type;
                            dropdown.style.display = 'none';
                        });
                    });
                })
                .catch(function (e) {
                    if (!serviceSearcher.isCurrent(myToken)) return;
                    dropdown.innerHTML = '<div class="typeahead-item" style="color: var(--text-muted);">Error</div>';
                });
        });
    }

    function showRouteDropdown(container, state, filter) {
        var dropdown = container.querySelector('#tts_routeDropdown');
        dropdown.style.display = 'block';
        if (!dropdown.innerHTML.trim()) {
            dropdown.innerHTML = '<div class="typeahead-item" style="color: var(--text-muted);">…</div>';
        }
        routeSearcher.schedule(function (myToken) {
            var params = new URLSearchParams({ limit: TYPEAHEAD_LIMIT });
            if (filter) params.set('search', filter);
            var selectedCountry = container.querySelector('#tts_countryFilter').value;
            if (selectedCountry) params.set('country_id', selectedCountry);
            fetch('/api/routes?' + params.toString())
                .then(function (r) { return r.json(); })
                .then(function (result) {
                    if (!routeSearcher.isCurrent(myToken)) return;
                    var rows = Array.isArray(result) ? result : (result && result.data) || [];
                    if (rows.length === 0) {
                        dropdown.innerHTML = '<div class="typeahead-item" style="color: var(--text-muted);">' + tt('timetables.noRoutesFound', 'No routes found') + '</div>';
                        return;
                    }
                    dropdown.innerHTML = rows.map(function (route) {
                        return '<div class="typeahead-item" data-id="' + route.id + '" data-name="' + escapeHtml(route.name) + '">'
                            + '<div class="item-name">' + escapeHtml(route.name) + '</div>'
                            + (route.country_name ? '<div class="item-sub">' + escapeHtml(route.country_name) + '</div>' : '')
                            + '</div>';
                    }).join('');
                    dropdown.querySelectorAll('.typeahead-item[data-id]').forEach(function (item) {
                        item.addEventListener('click', function () {
                            container.querySelector('#tts_routeInput').value = this.dataset.name;
                            container.querySelector('#tts_routeFilter').value = this.dataset.id;
                            dropdown.style.display = 'none';
                            updateSectionFilter(container, state, this.dataset.id);
                        });
                    });
                })
                .catch(function () {
                    if (!routeSearcher.isCurrent(myToken)) return;
                    dropdown.innerHTML = '<div class="typeahead-item" style="color: var(--text-muted);">Error</div>';
                });
        });
    }

    function showFormationDropdown(container, state, filter) {
        var dropdown = container.querySelector('#tts_formationDropdown');
        dropdown.style.display = 'block';
        if (!dropdown.innerHTML.trim()) {
            dropdown.innerHTML = '<div class="typeahead-item" style="color: var(--text-muted);">…</div>';
        }
        formationSearcher.schedule(function (myToken) {
            // Server-side search via /api/formations. Locked-route pages already
            // route through /api/routes/<id>/formations in loadData (so the cache
            // is correct for the static page-load), but the typeahead
            // re-queries the global formation list with the search term so the
            // user can find formations beyond what's already cached.
            var params = new URLSearchParams({ limit: TYPEAHEAD_LIMIT });
            if (filter) params.set('search', filter);
            var endpoint = (state.lockRoute && state.opts.routeId)
                ? '/api/routes/' + state.opts.routeId + '/formations'
                : '/api/formations';
            fetch(endpoint + '?' + params.toString())
                .then(function (r) { return r.json(); })
                .then(function (result) {
                    if (!formationSearcher.isCurrent(myToken)) return;
                    var formations = Array.isArray(result) ? result : (result && result.data) || [];
                    // Locked-route endpoint doesn't yet honour ?search — so
                    // filter client-side as a safety net.
                    if (filter) {
                        var lower = filter.toLowerCase();
                        formations = formations.filter(function (t) {
                            return ((t.name || '').toLowerCase().indexOf(lower) !== -1)
                                || ((t.class_name || '').toLowerCase().indexOf(lower) !== -1);
                        });
                    }
                    renderFormationDropdown(container, dropdown, formations);
                })
                .catch(function () {
                    if (!formationSearcher.isCurrent(myToken)) return;
                    dropdown.innerHTML = '<div class="typeahead-item" style="color: var(--text-muted);">Error</div>';
                });
        });
    }

    function renderFormationDropdown(container, dropdown, formations) {
        // Group by class so the user sees one entry per class (e.g. "Isle
        // Of Wight Class 483") even when multiple formations share it.
        // Picking the entry filters timetables for ALL formations in that
        // class via the backend's class_id query param. Formations without a
        // class_id surface as a single formation entry (their own row).
        var classBuckets = {};            // class_id → { class_name, formationIds[], formationNames[] }
        var formationOnly = [];           // legacy formations with no class_id
        formations.forEach(function (formation) {
            if (formation.class_id) {
                var key = formation.class_id;
                if (!classBuckets[key]) {
                    classBuckets[key] = {
                        class_id: formation.class_id,
                        class_name: formation.class_name || formation.name,
                        formationIds: [],
                        formationNames: []
                    };
                }
                classBuckets[key].formationIds.push(formation.id);
                if (formation.name && formation.name !== classBuckets[key].class_name) {
                    classBuckets[key].formationNames.push(formation.name);
                }
            } else {
                formationOnly.push(formation);
            }
        });
        var entries = [];
        Object.keys(classBuckets).forEach(function (k) { entries.push({ kind: 'class', data: classBuckets[k] }); });
        formationOnly.forEach(function (t) { entries.push({ kind: 'formation', data: t }); });
        entries.sort(function (a, b) {
            var an = a.kind === 'class' ? a.data.class_name : a.data.name;
            var bn = b.kind === 'class' ? b.data.class_name : b.data.name;
            return (an || '').localeCompare(bn || '');
        });

        if (entries.length === 0) {
            dropdown.innerHTML = '<div class="typeahead-item" style="color: var(--text-muted);">' + tt('timetables.noFormationsFound', 'No formations found') + '</div>';
        } else {
            dropdown.innerHTML = entries.map(function (e) {
                if (e.kind === 'class') {
                    var c = e.data;
                    var sub = c.formationNames.length > 0
                        ? c.formationNames.join(', ')
                        : '';
                    return '<div class="typeahead-item" data-kind="class" data-id="' + c.class_id + '" data-label="' + escapeHtml(c.class_name) + '">'
                        + '<div class="item-name">' + escapeHtml(c.class_name) + '</div>'
                        + (sub ? '<div class="item-sub">' + escapeHtml(sub) + '</div>' : '')
                        + '</div>';
                }
                var t = e.data;
                return '<div class="typeahead-item" data-kind="formation" data-id="' + t.id + '" data-label="' + escapeHtml(t.name) + '">'
                    + '<div class="item-name">' + escapeHtml(t.name) + '</div></div>';
            }).join('');
            dropdown.querySelectorAll('.typeahead-item[data-id]').forEach(function (item) {
                item.addEventListener('click', function () {
                    container.querySelector('#tts_formationInput').value = this.dataset.label;
                    // Encode the kind alongside the id so fetchAndRender
                    // knows which API param to set.
                    container.querySelector('#tts_formationFilter').value = this.dataset.kind + ':' + this.dataset.id;
                    dropdown.style.display = 'none';
                });
            });
        }
        dropdown.style.display = 'block';
    }

    // ---- buttons ----

    function wireButtons(container, state) {
        container.querySelector('#tts_clearBtn').addEventListener('click', function () { clearFilters(container, state); });
        container.querySelector('#tts_searchBtn').addEventListener('click', function () { fetchAndRender(container, state, 1); });
    }

    function clearFilters(container, state) {
        // Country: skip when the route is locked (those filters are hidden).
        if (!state.lockRoute) {
            container.querySelector('#tts_countryFilter').value = '';
            container.querySelector('#tts_filterCountryDisplay').innerHTML = '<span>' + escapeHtml(tt('common.all', 'All')) + '</span>';
            container.querySelector('#tts_routeInput').value = '';
            container.querySelector('#tts_routeFilter').value = '';
        }
        container.querySelector('#tts_serviceInput').value = '';
        container.querySelector('#tts_serviceFilter').value = '';
        container.querySelector('#tts_serviceFilterType').value = '';
        container.querySelector('#tts_formationInput').value = '';
        container.querySelector('#tts_formationFilter').value = '';
        updateSectionFilter(container, state, state.lockRoute && state.opts.routeId ? state.opts.routeId : null);
        ['tts_startTimeMin','tts_startTimeMax','tts_durationMin','tts_durationMax','tts_stopsMin','tts_stopsMax','tts_conductorFilter','tts_coordSourceFilter','tts_serviceTypeFilter','tts_playableFilter','tts_sourceFilter']
            .forEach(function (id) { var el = container.querySelector('#' + id); if (el) el.value = ''; });
        ['tts_serviceDropdown','tts_routeDropdown','tts_formationDropdown']
            .forEach(function (id) { var el = container.querySelector('#' + id); if (el) el.style.display = 'none'; });
        // Re-apply locked route on next search.
        if (state.lockRoute && state.opts.routeId) {
            container.querySelector('#tts_routeFilter').value = state.opts.routeId;
        }
        state.currentPage = 1;
        fetchAndRender(container, state, 1);
    }

    // ---- table render ----

    async function fetchAndRender(container, state, page) {
        page = page || state.currentPage;
        state.currentPage = page;
        var listEl = container.querySelector('#tts_timetablesList');
        listEl.innerHTML = '<p class="empty-message">Loading...</p>';

        var params = new URLSearchParams({ page: page, limit: state.currentLimit });

        var serviceFilter = container.querySelector('#tts_serviceFilter').value;
        var serviceInput = container.querySelector('#tts_serviceInput').value.trim();
        var routeId = state.lockRoute && state.opts.routeId
            ? state.opts.routeId
            : container.querySelector('#tts_routeFilter').value;
        // Formation filter encodes "kind:id" — `class:N` filters by all
        // formations of a class, `formation:N` by a single formation.
        // (Empty / legacy plain-id values are treated as a formation.)
        var formationFilterRaw = container.querySelector('#tts_formationFilter').value;
        var formationFilterKind = '';
        var formationFilterId = '';
        if (formationFilterRaw) {
            var idx = formationFilterRaw.indexOf(':');
            if (idx > 0) {
                formationFilterKind = formationFilterRaw.slice(0, idx);
                formationFilterId = formationFilterRaw.slice(idx + 1);
            } else {
                formationFilterKind = 'formation';
                formationFilterId = formationFilterRaw;
            }
        }
        var countryId = container.querySelector('#tts_countryFilter').value;
        var sectionFilter = container.querySelector('#tts_sectionFilter').value;
        var coordSourceFilter = container.querySelector('#tts_coordSourceFilter').value;
        var conductorFilter = container.querySelector('#tts_conductorFilter').value;
        var startTimeMin = container.querySelector('#tts_startTimeMin').value;
        var startTimeMax = container.querySelector('#tts_startTimeMax').value;
        var durationMin = container.querySelector('#tts_durationMin').value;
        var durationMax = container.querySelector('#tts_durationMax').value;
        var stopsMin = container.querySelector('#tts_stopsMin').value;
        var stopsMax = container.querySelector('#tts_stopsMax').value;
        var serviceTypeFilter = container.querySelector('#tts_serviceTypeFilter').value;
        var playableFilter = container.querySelector('#tts_playableFilter').value;
        var sourceFilter = container.querySelector('#tts_sourceFilter').value;

        if (serviceFilter) params.set('search', serviceFilter);
        else if (serviceInput) params.set('search', serviceInput);
        if (routeId) params.set('route_id', routeId);
        if (countryId) params.set('country_id', countryId);
        if (formationFilterId && formationFilterKind === 'class') params.set('class_id', formationFilterId);
        else if (formationFilterId) params.set('formation_id', formationFilterId);
        if (sectionFilter) params.set('section', sectionFilter);
        // Dev-only knobs — never query on them outside dev mode. (Server
        // also enforces this for `playable`, but skipping the param
        // here keeps logs clean.)
        if (coordSourceFilter && state.devMode) params.set('coord_source', coordSourceFilter);
        if (conductorFilter) params.set('conductor', conductorFilter);
        if (startTimeMin) params.set('start_time_min', startTimeMin);
        if (startTimeMax) params.set('start_time_max', startTimeMax);
        if (durationMin) params.set('duration_min', durationMin);
        if (durationMax) params.set('duration_max', durationMax);
        if (stopsMin) params.set('stops_min', stopsMin);
        if (stopsMax) params.set('stops_max', stopsMax);
        if (serviceTypeFilter) params.set('service_type', serviceTypeFilter);
        if (playableFilter && state.devMode) params.set('playable', playableFilter);
        if (sourceFilter) params.set('source', sourceFilter);

        try {
            var response = await fetch('/api/timetables/paginated?' + params);
            var result = await response.json();
            var timetables = result.data;
            var total = result.pagination.total;
            var totalPages = result.pagination.totalPages;

            // Cache for service typeahead suggestions.
            state.allTimetables = timetables;

            if (!timetables || timetables.length === 0) {
                listEl.innerHTML = '<p class="empty-message">' + tt('timetables.noTimetablesFound', 'No timetables found.') + '</p>';
                container.querySelector('#tts_pagination').innerHTML = '<span class="pagination-info">0 results</span>';
                return;
            }

            var devMode = state.devMode;
            // Section column tracks the same flag the section filter uses —
            // hide it when the active route has no sections (default true
            // until the section fetch tells us otherwise).
            var showSection = state.hasSections !== false;
            var html = '<table><thead><tr>';
            html += '<th data-i18n="timetables.serviceName">Service Name</th>';
            // Hide Route column when the page is already scoped to one route.
            if (!state.lockRoute) html += '<th data-i18n="timetables.route">Route</th>';
            if (showSection) html += '<th data-i18n="timetables.section">Section</th>';
            html += '<th data-i18n="timetables.formations">Formations</th>';
            html += '<th class="tts-c" data-i18n="timetables.startTime">Start Time</th>';
            html += '<th class="tts-c" data-i18n="timetables.duration">Duration</th>';
            html += '<th class="tts-c" data-i18n="timetables.stops">Stops</th>';
            if (devMode) html += '<th data-i18n="timetables.coordSource">Coord Source</th>';
            html += '<th class="tts-c" data-i18n="timetables.conductor">Conductor</th>';
            if (devMode) html += '<th class="actions" data-i18n="common.actions">Actions</th>';
            html += '</tr></thead><tbody>';
            timetables.forEach(function (t) {
                var route = state.allRoutes.find(function (r) { return r.id == t.route_id; });
                var formationDisplay = '-';
                // Show the user-facing class name when available (e.g. "Isle
                // Of Wight Class 483") rather than the formation name (e.g.
                // "Class483_006"). Falls back to formation when class is
                // unset.
                if (t.formations && t.formations.length > 0) {
                    formationDisplay = t.formations.map(function (formation) {
                        var label = formation.class_name || formation.name;
                        return '<a href="/formations/' + formation.id + '">' + escapeHtml(label) + '</a>';
                    }).join(', ');
                } else if (t.formation_id) {
                    var formation = state.allFormations.find(function (tr) { return tr.id == t.formation_id; });
                    if (formation) {
                        var label = formation.class_name || formation.name;
                        formationDisplay = '<a href="/formations/' + formation.id + '">' + escapeHtml(label) + '</a>';
                    }
                }

                html += '<tr>';
                html += '<td><a href="/timetables/' + t.id + (devMode ? '/edit' : '') + '">' + escapeHtml(t.service_name || 'Untitled') + '</a></td>';
                if (!state.lockRoute) {
                    html += '<td>' + (route ? '<a href="/routes/' + route.id + '">' + escapeHtml(route.name) + '</a>' : '-') + '</td>';
                }
                if (showSection) html += '<td>' + escapeHtml(t.section_name || '-') + '</td>';
                html += '<td>' + formationDisplay + '</td>';
                html += '<td class="tts-c">' + escapeHtml(t.start_time || '-') + '</td>';
                html += '<td class="tts-c">' + escapeHtml(t.duration || '-') + '</td>';
                html += '<td class="tts-c">' + (t.entry_count || 0) + '</td>';
                if (devMode) {
                    var csLabel = t.coordinates_coord_source
                        ? tt('timetables.' + t.coordinates_coord_source, t.coordinates_coord_source.charAt(0).toUpperCase() + t.coordinates_coord_source.slice(1))
                        : tt('timetables.none', 'None');
                    html += '<td>' + escapeHtml(csLabel) + '</td>';
                }
                html += '<td class="tts-c">' + (t.conductor_compatible ? tt('common.yes', 'Yes') : tt('common.no', 'No')) + '</td>';
                if (devMode) {
                    html += '<td class="actions">';
                    html += '<button class="btn-primary" data-tts-show="' + t.id + '" data-i18n="common.show">Show</button>';
                    html += '<button class="btn-secondary" data-tts-edit="' + t.id + '" data-i18n="common.edit">Edit</button>';
                    html += '<button class="btn-danger" data-tts-del="' + t.id + '" data-i18n="common.delete">Delete</button>';
                    html += '</td>';
                }
                html += '</tr>';
            });
            html += '</tbody></table>';
            listEl.innerHTML = html;

            // Wire dev-mode action buttons.
            listEl.querySelectorAll('[data-tts-show]').forEach(function (b) {
                b.addEventListener('click', function () { window.location.href = '/timetables/' + b.getAttribute('data-tts-show'); });
            });
            listEl.querySelectorAll('[data-tts-edit]').forEach(function (b) {
                b.addEventListener('click', function () { window.location.href = '/timetables/' + b.getAttribute('data-tts-edit') + '/edit'; });
            });
            listEl.querySelectorAll('[data-tts-del]').forEach(function (b) {
                b.addEventListener('click', async function () {
                    var id = b.getAttribute('data-tts-del');
                    if (!confirm(tt('timetables.confirmDeleteTimetable', 'Are you sure you want to delete this timetable?'))) return;
                    try {
                        await fetch('/api/timetables/' + id, { method: 'DELETE' });
                        fetchAndRender(container, state, state.currentPage);
                    } catch (err) {
                        alert(tt('timetables.errorDeletingTimetable', 'Error deleting timetable'));
                    }
                });
            });

            if (typeof i18n !== 'undefined' && i18n.translate) i18n.translate(listEl);

            renderPagination(container, state, total, totalPages);
            state.firstRenderDone = true;
        } catch (err) {
            console.error('TimetableSearch.fetchAndRender:', err);
            listEl.innerHTML = '<p class="empty-message">Error loading timetables</p>';
        }
    }

    function renderPagination(container, state, total, totalPages) {
        var pag = container.querySelector('#tts_pagination');
        var ofText = tt('common.of', 'of');
        var showingText = tt('common.showing', 'Showing');
        if (totalPages <= 1) {
            pag.innerHTML = '<span class="pagination-info">' + showingText + ' ' + total + ' ' + tt('timetables.timetablesCount', 'timetables') + '</span>';
            return;
        }

        var html = '<button class="btn-secondary" data-tts-page="1" ' + (state.currentPage === 1 ? 'disabled' : '') + '>&laquo;</button>';
        html += '<button class="btn-secondary" data-tts-page="' + (state.currentPage - 1) + '" ' + (state.currentPage === 1 ? 'disabled' : '') + '>&lsaquo;</button>';
        html += '<span class="pagination-info">' + state.currentPage + ' ' + ofText + ' ' + totalPages + ' (' + total + ' total)</span>';
        html += '<button class="btn-secondary" data-tts-page="' + (state.currentPage + 1) + '" ' + (state.currentPage === totalPages ? 'disabled' : '') + '>&rsaquo;</button>';
        html += '<button class="btn-secondary" data-tts-page="' + totalPages + '" ' + (state.currentPage === totalPages ? 'disabled' : '') + '>&raquo;</button>';
        html += '<div class="per-page"><label>&nbsp;</label><select data-tts-limit>';
        [15, 25, 50, 100].forEach(function (n) {
            html += '<option value="' + n + '" ' + (state.currentLimit === n ? 'selected' : '') + '>' + n + '</option>';
        });
        html += '</select></div>';
        pag.innerHTML = html;

        pag.querySelectorAll('[data-tts-page]').forEach(function (b) {
            b.addEventListener('click', function () { fetchAndRender(container, state, parseInt(b.getAttribute('data-tts-page'), 10)); });
        });
        var limitSel = pag.querySelector('[data-tts-limit]');
        if (limitSel) limitSel.addEventListener('change', function () {
            state.currentLimit = parseInt(limitSel.value, 10);
            state.currentPage = 1;
            fetchAndRender(container, state, 1);
        });
    }

    global.TimetableSearch = { init: init };
})(window);
