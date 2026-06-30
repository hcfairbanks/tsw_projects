// Locations sub-tab. 12k rows → paginated 50/page with name + route filters.
import { t } from '../shared/i18n.js';

const { invoke } = window.__TAURI__.core;

const esc = (s) => String(s == null ? '' : s).replace(/[&<>"]/g,
  (c) => ({'&':'&amp;','<':'&lt;','>':'&gt;','"':'&quot;'}[c]));

const ALL_ROUTES = () => `— ${t('routes.allRoutes', 'all routes')} —`;
const PICK_ROUTE = () => `— ${t('shell.dev.pickRoute', 'pick route')} —`;

const markup = () => `
  <div class="card">
    <h2>${esc(t('locations.addNew', 'Add location'))}</h2>
    <form id="addForm" class="row">
      <div class="grow">
        <label for="addRoute">${esc(t('locations.route', 'Route'))}</label>
        <select id="addRoute" required></select>
      </div>
      <div class="grow">
        <label for="addName">${esc(t('locations.locationName', 'Location name'))}</label>
        <input type="text" id="addName" placeholder="${esc(t('locations.locationNamePlaceholder', 'e.g. London Euston'))}" required>
      </div>
      <div><button class="primary" type="submit">${esc(t('common.add', 'Add'))}</button></div>
    </form>
  </div>

  <div class="card" id="editCard">
    <h2>${esc(t('locations.editLocation', 'Edit location'))}</h2>
    <form id="editForm" class="row">
      <input type="hidden" id="editId">
      <div class="grow">
        <label for="editRoute">${esc(t('locations.route', 'Route'))}</label>
        <select id="editRoute" required></select>
      </div>
      <div class="grow">
        <label for="editName">${esc(t('locations.locationName', 'Location name'))}</label>
        <input type="text" id="editName" required>
      </div>
      <div>
        <button class="primary" type="submit">${esc(t('common.save', 'Save'))}</button>
        <button type="button" id="editCancel">${esc(t('common.cancel', 'Cancel'))}</button>
      </div>
    </form>
  </div>

  <div class="card">
    <h2>${esc(t('shell.dev.allLocations', 'All locations'))}</h2>
    <div class="row" style="margin-bottom:10px;">
      <div class="grow"><label>${esc(t('shell.dev.searchName', 'Search name'))}</label>
        <input type="text" id="fSearch" placeholder="${esc(t('shell.dev.filterByNamePlaceholder', 'filter by name…'))}"></div>
      <div class="grow"><label>${esc(t('locations.filterByRoute', 'Filter by route'))}</label>
        <select id="fRoute"><option value="">${esc(ALL_ROUTES())}</option></select></div>
      <div>
        <button id="fGo" class="primary">${esc(t('common.search', 'Search'))}</button>
        <button id="fClear">${esc(t('common.clear', 'Clear'))}</button>
      </div>
    </div>
    <div id="flash" class="flash"></div>
    <table>
      <thead>
        <tr>
          <th style="width:35%;">${esc(t('common.name', 'Name'))}</th>
          <th style="width:35%;">${esc(t('locations.route', 'Route'))}</th>
          <th>${esc(t('shell.dev.usedInEntries', 'Used in entries'))}</th>
          <th class="actions">${esc(t('common.actions', 'Actions'))}</th>
        </tr>
      </thead>
      <tbody id="rows"><tr><td colspan="4" class="muted" style="padding:24px;text-align:center;">${esc(t('common.loading', 'loading…'))}</td></tr></tbody>
    </table>
    <div class="footer-bar">
      <button id="prev">◀ ${esc(t('common.previous', 'Prev'))}</button>
      <span id="pageinfo">${esc(t('shell.dev.pageInfo', 'Page 1 / 1 · 0 rows'))}</span>
      <button id="next">${esc(t('common.next', 'Next'))} ▶</button>
      <span class="spacer"></span>
      <span class="muted">${esc(t('shell.dev.perPage', '50 / page'))}</span>
    </div>
  </div>
`;

export function mount(root) {
  root.innerHTML = markup();
  // Re-render with the new language when the shell switches locale.
  document.addEventListener('languageChanged', () => { if (root.isConnected) mount(root); });

  const PAGE_SIZE = 50;
  const STATE = { filter: { search: '', route_id: null }, page: 0, total: 0 };
  let ROUTES = [];

  const flash = (msg, cls = '') => {
    const el = root.querySelector('#flash');
    if (!el) return;
    el.textContent = msg || '';
    el.className = 'flash' + (cls ? ' ' + cls : '');
  };

  async function loadRoutes() {
    try {
      const opts = await invoke('timetable_filter_options');
      ROUTES = (opts.routes || []).map(([id, name]) => ({ id, name }));
      const opt = (sel, blank) => sel.innerHTML =
        (blank ? `<option value="">${blank}</option>` : '') +
        ROUTES.map((r) => `<option value="${r.id}">${esc(r.name)}</option>`).join('');
      opt(root.querySelector('#fRoute'), ALL_ROUTES());
      opt(root.querySelector('#addRoute'), PICK_ROUTE());
      opt(root.querySelector('#editRoute'), PICK_ROUTE());
    } catch (e) { flash(t('shell.dev.routesLoadFailed', 'routes load failed') + ': ' + e); }
  }

  async function search() {
    try {
      const r = await invoke('locations_search', {
        filter: { search: STATE.filter.search, route_id: STATE.filter.route_id },
        page: STATE.page,
        perPage: PAGE_SIZE,
      });
      STATE.total = r.total || 0;
      const tbody = root.querySelector('#rows');
      if (!tbody) return;
      if (!r.rows.length) {
        tbody.innerHTML = `<tr><td colspan="4" class="muted" style="padding:24px;text-align:center;">${esc(t('shell.dev.noLocationsMatch', 'no locations match'))}</td></tr>`;
      } else {
        tbody.innerHTML = r.rows.map((row) => `<tr>
          <td>${esc(row.name)}</td>
          <td>${esc(row.route) || '<span class="muted">—</span>'}</td>
          <td><span class="pill">${row.use_count}</span></td>
          <td class="actions">
            <button data-act="edit" data-id="${row.id}" data-rid="${row.route_id}" data-name="${esc(row.name)}">${esc(t('common.edit', 'Edit'))}</button>
            <button class="danger" data-act="del" data-id="${row.id}" data-name="${esc(row.name)}">${esc(t('common.delete', 'Delete'))}</button>
          </td>
        </tr>`).join('');
      }
      const pages = Math.max(1, Math.ceil(STATE.total / PAGE_SIZE));
      root.querySelector('#pageinfo').textContent =
        t('shell.dev.pageInfoFmt', 'Page {page} / {pages} · {total} {label}')
          .replace('{page}', STATE.page + 1)
          .replace('{pages}', pages)
          .replace('{total}', STATE.total)
          .replace('{label}', STATE.total === 1
            ? t('shell.dev.locationSingular', 'location')
            : t('shell.dev.locationPlural', 'locations'));
      root.querySelector('#prev').disabled = STATE.page <= 0;
      root.querySelector('#next').disabled = STATE.page + 1 >= pages;
    } catch (e) { flash(t('shell.dev.searchFailed', 'search failed') + ': ' + e); }
  }

  root.querySelector('#rows').addEventListener('click', async (ev) => {
    const btn = ev.target.closest('button[data-act]');
    if (!btn) return;
    const id = Number(btn.dataset.id);
    if (btn.dataset.act === 'edit') {
      root.querySelector('#editId').value    = id;
      root.querySelector('#editRoute').value = btn.dataset.rid;
      root.querySelector('#editName').value  = btn.dataset.name || '';
      root.querySelector('#editCard').classList.add('show');
      root.querySelector('#editName').focus();
      return;
    }
    if (btn.dataset.act === 'del') {
      if (!confirm(t('shell.dev.confirmDeleteNamed', 'Delete "{name}"?').replace('{name}', btn.dataset.name))) return;
      try { await invoke('location_delete', { id }); flash(t('shell.dev.deleted', 'deleted'), 'ok'); search(); }
      catch (e) { flash(t('shell.dev.deleteFailed', 'delete failed') + ': ' + e); }
    }
  });

  root.querySelector('#addForm').addEventListener('submit', async (ev) => {
    ev.preventDefault();
    const body = {
      id: null,
      route_id: Number(root.querySelector('#addRoute').value) || 0,
      name: root.querySelector('#addName').value.trim(),
    };
    try {
      await invoke('location_save', { body });
      root.querySelector('#addName').value = '';
      flash(t('shell.dev.added', 'added'), 'ok'); search();
    } catch (e) { flash(t('shell.dev.addFailed', 'add failed') + ': ' + e); }
  });

  root.querySelector('#editForm').addEventListener('submit', async (ev) => {
    ev.preventDefault();
    const body = {
      id: Number(root.querySelector('#editId').value),
      route_id: Number(root.querySelector('#editRoute').value) || 0,
      name: root.querySelector('#editName').value.trim(),
    };
    try {
      await invoke('location_save', { body });
      root.querySelector('#editCard').classList.remove('show');
      flash(t('shell.dev.saved', 'saved'), 'ok'); search();
    } catch (e) { flash(t('shell.dev.saveFailed', 'save failed') + ': ' + e); }
  });

  root.querySelector('#editCancel').addEventListener('click', () => {
    root.querySelector('#editCard').classList.remove('show');
  });

  // Pull every filter from the form at submit time — never run a search
  // implicitly when a control changes. The user wants explicit control:
  // they set their filters, then click Search (or hit Enter in the name
  // box). Stops the route dropdown from firing a query every time you
  // poke it.
  const runSearch = () => {
    STATE.filter.search   = root.querySelector('#fSearch').value;
    STATE.filter.route_id = root.querySelector('#fRoute').value
      ? Number(root.querySelector('#fRoute').value)
      : null;
    STATE.page = 0;
    search();
  };
  root.querySelector('#fSearch').addEventListener('keydown', (e) => {
    if (e.key === 'Enter') runSearch();
  });
  root.querySelector('#fGo').addEventListener('click', runSearch);
  root.querySelector('#fClear').addEventListener('click', () => {
    root.querySelector('#fSearch').value = '';
    root.querySelector('#fRoute').value = '';
    STATE.filter = { search: '', route_id: null }; STATE.page = 0; search();
  });
  root.querySelector('#prev').addEventListener('click', () => { if (STATE.page > 0) { STATE.page--; search(); } });
  root.querySelector('#next').addEventListener('click', () => {
    const pages = Math.max(1, Math.ceil(STATE.total / PAGE_SIZE));
    if (STATE.page + 1 < pages) { STATE.page++; search(); }
  });

  (async () => { await loadRoutes(); search(); })();
}
