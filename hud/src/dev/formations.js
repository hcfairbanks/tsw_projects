// Formations sub-tab. 4.5k rows → paginated 50/page with search + route + class filters.
import { t } from '../shared/i18n.js';

const { invoke } = window.__TAURI__.core;

const esc = (s) => String(s == null ? '' : s).replace(/[&<>"]/g,
  (c) => ({'&':'&amp;','<':'&lt;','>':'&gt;','"':'&quot;'}[c]));
const fmtNum = (v, sfx) => v == null ? '<span class="muted">—</span>'
  : Math.round(Number(v) * 10) / 10 + (sfx ? ' ' + sfx : '');

const ALL_ROUTES  = () => `— ${t('routes.allRoutes', 'all routes')} —`;
const ALL_CLASSES = () => `— ${t('shell.dev.allClasses', 'all classes')} —`;
const NO_CLASS    = () => `— ${t('shell.dev.noClass', 'no class')} —`;

const markup = () => `
  <div class="card">
    <h2>${esc(t('formations.addNew', 'Add formation'))}</h2>
    <form id="addForm" class="row">
      <div class="grow">
        <label for="addName">${esc(t('common.name', 'Name'))}</label>
        <input type="text" id="addName" placeholder="${esc(t('shell.dev.formationNamePlaceholder', 'e.g. PlayerFormation'))}" required>
      </div>
      <div class="grow">
        <label for="addClass">${esc(t('shell.dev.trainClass', 'Train class'))}</label>
        <select id="addClass"><option value="">${esc(NO_CLASS())}</option></select>
      </div>
      <div class="small">
        <label for="addLivery">${esc(t('formations.livery', 'Livery'))}</label>
        <input type="text" id="addLivery" placeholder="${esc(t('common.optional', 'optional'))}">
      </div>
      <div class="small">
        <label for="addLength">${esc(t('shell.dev.lengthM', 'Length (m)'))}</label>
        <input type="number" id="addLength" step="0.1" min="0">
      </div>
      <div class="small">
        <label for="addCars">${esc(t('formations.cars', 'Cars'))}</label>
        <input type="number" id="addCars" step="1" min="0">
      </div>
      <div><button class="primary" type="submit">${esc(t('common.add', 'Add'))}</button></div>
    </form>
  </div>

  <div class="card" id="editCard">
    <h2>${esc(t('formations.editFormation', 'Edit formation'))}</h2>
    <form id="editForm" class="row">
      <input type="hidden" id="editId">
      <div class="grow">
        <label for="editName">${esc(t('common.name', 'Name'))}</label>
        <input type="text" id="editName" required>
      </div>
      <div class="grow">
        <label for="editClass">${esc(t('shell.dev.trainClass', 'Train class'))}</label>
        <select id="editClass"><option value="">${esc(NO_CLASS())}</option></select>
      </div>
      <div class="small">
        <label for="editLivery">${esc(t('formations.livery', 'Livery'))}</label>
        <input type="text" id="editLivery">
      </div>
      <div class="small">
        <label for="editLength">${esc(t('shell.dev.lengthM', 'Length (m)'))}</label>
        <input type="number" id="editLength" step="0.1" min="0">
      </div>
      <div class="small">
        <label for="editCars">${esc(t('formations.cars', 'Cars'))}</label>
        <input type="number" id="editCars" step="1" min="0">
      </div>
      <div>
        <button class="primary" type="submit">${esc(t('common.save', 'Save'))}</button>
        <button type="button" id="editCancel">${esc(t('common.cancel', 'Cancel'))}</button>
      </div>
    </form>
  </div>

  <div class="card">
    <h2>${esc(t('shell.dev.allFormations', 'All formations'))}</h2>
    <div class="row" style="margin-bottom:10px;">
      <div class="grow"><label>${esc(t('shell.dev.searchNameClass', 'Search (name / class)'))}</label>
        <input type="text" id="fSearch" placeholder="${esc(t('shell.dev.filterPlaceholder', 'filter…'))}"></div>
      <div class="grow"><label>${esc(t('shell.dev.filterByRoute', 'Filter by route'))}</label>
        <select id="fRoute"><option value="">${esc(ALL_ROUTES())}</option></select></div>
      <div class="grow"><label>${esc(t('shell.dev.filterByClass', 'Filter by class'))}</label>
        <select id="fClass"><option value="">${esc(ALL_CLASSES())}</option></select></div>
      <div>
        <button id="fGo" class="primary">${esc(t('common.search', 'Search'))}</button>
        <button id="fClear">${esc(t('common.clear', 'Clear'))}</button>
      </div>
    </div>
    <div id="flash" class="flash"></div>
    <table>
      <thead>
        <tr>
          <th style="width:24%;">${esc(t('common.name', 'Name'))}</th>
          <th style="width:24%;">${esc(t('formations.class', 'Class'))}</th>
          <th>${esc(t('formations.livery', 'Livery'))}</th>
          <th>${esc(t('formations.length', 'Length'))}</th>
          <th>${esc(t('formations.cars', 'Cars'))}</th>
          <th>${esc(t('shell.dev.usedBy', 'Used by'))}</th>
          <th class="actions">${esc(t('common.actions', 'Actions'))}</th>
        </tr>
      </thead>
      <tbody id="rows"><tr><td colspan="7" class="muted" style="padding:24px;text-align:center;">${esc(t('common.loading', 'loading…'))}</td></tr></tbody>
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
  const STATE = { filter: { search: '', class_id: null, route_id: null }, page: 0, total: 0 };

  const flash = (msg, cls = '') => {
    const el = root.querySelector('#flash');
    if (!el) return;
    el.textContent = msg || '';
    el.className = 'flash' + (cls ? ' ' + cls : '');
  };

  async function loadFilterOptions() {
    try {
      const opts = await invoke('timetable_filter_options');
      const routes  = opts.routes  || [];
      const classes = opts.classes || [];
      const opt = (sel, blank, items) => sel.innerHTML =
        (blank ? `<option value="">${blank}</option>` : '') +
        items.map(([id, name]) => `<option value="${id}">${esc(name)}</option>`).join('');
      opt(root.querySelector('#fRoute'),   ALL_ROUTES(),  routes);
      opt(root.querySelector('#fClass'),   ALL_CLASSES(), classes);
      opt(root.querySelector('#addClass'), NO_CLASS(),    classes);
      opt(root.querySelector('#editClass'),NO_CLASS(),    classes);
    } catch (e) { flash(t('shell.dev.filterOptionsFailed', 'filter options failed') + ': ' + e); }
  }

  async function search() {
    try {
      const r = await invoke('formations_search', {
        filter: {
          search:   STATE.filter.search,
          class_id: STATE.filter.class_id,
          route_id: STATE.filter.route_id,
        },
        page: STATE.page,
        perPage: PAGE_SIZE,
      });
      STATE.total = r.total || 0;
      const tbody = root.querySelector('#rows');
      if (!tbody) return;
      if (!r.rows.length) {
        tbody.innerHTML = `<tr><td colspan="7" class="muted" style="padding:24px;text-align:center;">${esc(t('shell.dev.noFormationsMatch', 'no formations match'))}</td></tr>`;
      } else {
        tbody.innerHTML = r.rows.map((f) => `<tr>
          <td><a href="formation-show.html?id=${f.id}" style="color:var(--accent);text-decoration:none;">${esc(f.name)}</a></td>
          <td>${esc(f.class_name) || '<span class="muted">—</span>'}</td>
          <td>${f.livery_id ? `<code>${esc(f.livery_id)}</code>` : '<span class="muted">—</span>'}</td>
          <td>${fmtNum(f.length_m, 'm')}</td>
          <td>${f.car_count != null ? esc(f.car_count) : '<span class="muted">—</span>'}</td>
          <td><span class="pill">${f.timetable_count}</span></td>
          <td class="actions">
            <button data-act="view" data-id="${f.id}">${esc(t('common.view', 'View'))}</button>
            <button data-act="edit" data-id="${f.id}"
                    data-name="${esc(f.name)}"
                    data-cls="${f.class_id || ''}"
                    data-liv="${esc(f.livery_id)}"
                    data-len="${f.length_m != null ? f.length_m : ''}"
                    data-cars="${f.car_count != null ? f.car_count : ''}">${esc(t('common.edit', 'Edit'))}</button>
            <button class="danger" data-act="del" data-id="${f.id}" data-name="${esc(f.name)}">${esc(t('common.delete', 'Delete'))}</button>
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
            ? t('shell.dev.formationSingular', 'formation')
            : t('shell.dev.formationPlural', 'formations'));
      root.querySelector('#prev').disabled = STATE.page <= 0;
      root.querySelector('#next').disabled = STATE.page + 1 >= pages;
    } catch (e) { flash(t('shell.dev.searchFailed', 'search failed') + ': ' + e); }
  }

  root.querySelector('#rows').addEventListener('click', async (ev) => {
    const btn = ev.target.closest('button[data-act]');
    if (!btn) return;
    const id = Number(btn.dataset.id);
    if (btn.dataset.act === 'view') {
      location.href = 'formation-show.html?id=' + id;
      return;
    }
    if (btn.dataset.act === 'edit') {
      root.querySelector('#editId').value     = id;
      root.querySelector('#editName').value   = btn.dataset.name || '';
      root.querySelector('#editClass').value  = btn.dataset.cls || '';
      root.querySelector('#editLivery').value = btn.dataset.liv || '';
      root.querySelector('#editLength').value = btn.dataset.len || '';
      root.querySelector('#editCars').value   = btn.dataset.cars || '';
      root.querySelector('#editCard').classList.add('show');
      root.querySelector('#editName').focus();
      return;
    }
    if (btn.dataset.act === 'del') {
      if (!confirm(t('shell.dev.confirmDeleteFormation', 'Delete formation "{name}"?').replace('{name}', btn.dataset.name))) return;
      try { await invoke('formation_delete', { id }); flash(t('shell.dev.deleted', 'deleted'), 'ok'); search(); }
      catch (e) { flash(t('shell.dev.deleteFailed', 'delete failed') + ': ' + e); }
    }
  });

  function readUpsert(prefix) {
    const num = (s) => { const v = parseFloat(s); return Number.isFinite(v) ? v : null; };
    const int = (s) => { const v = parseInt(s, 10); return Number.isFinite(v) ? v : null; };
    return {
      id:        prefix === 'edit' ? Number(root.querySelector('#editId').value) : null,
      name:      root.querySelector('#' + prefix + 'Name').value.trim(),
      class_id:  Number(root.querySelector('#' + prefix + 'Class').value) || null,
      livery_id: root.querySelector('#' + prefix + 'Livery').value.trim(),
      length_m:  num(root.querySelector('#' + prefix + 'Length').value),
      car_count: int(root.querySelector('#' + prefix + 'Cars').value),
    };
  }

  root.querySelector('#addForm').addEventListener('submit', async (ev) => {
    ev.preventDefault();
    try {
      await invoke('formation_save', { body: readUpsert('add') });
      ['Name','Livery','Length','Cars'].forEach((k) => root.querySelector('#add' + k).value = '');
      root.querySelector('#addClass').value = '';
      flash(t('shell.dev.added', 'added'), 'ok'); search();
    } catch (e) { flash(t('shell.dev.addFailed', 'add failed') + ': ' + e); }
  });

  root.querySelector('#editForm').addEventListener('submit', async (ev) => {
    ev.preventDefault();
    try {
      await invoke('formation_save', { body: readUpsert('edit') });
      root.querySelector('#editCard').classList.remove('show');
      flash(t('shell.dev.saved', 'saved'), 'ok'); search();
    } catch (e) { flash(t('shell.dev.saveFailed', 'save failed') + ': ' + e); }
  });

  root.querySelector('#editCancel').addEventListener('click', () => {
    root.querySelector('#editCard').classList.remove('show');
  });

  // Pull every filter from the form at submit time — never auto-search
  // when a dropdown changes. Matches the locations sub-tab: filters are
  // edited freely, then Search (or Enter in the name box) commits them.
  const runSearch = () => {
    STATE.filter.search   = root.querySelector('#fSearch').value;
    STATE.filter.route_id = root.querySelector('#fRoute').value
      ? Number(root.querySelector('#fRoute').value) : null;
    STATE.filter.class_id = root.querySelector('#fClass').value
      ? Number(root.querySelector('#fClass').value) : null;
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
    root.querySelector('#fClass').value = '';
    STATE.filter = { search: '', class_id: null, route_id: null }; STATE.page = 0; search();
  });
  root.querySelector('#prev').addEventListener('click', () => { if (STATE.page > 0) { STATE.page--; search(); } });
  root.querySelector('#next').addEventListener('click', () => {
    const pages = Math.max(1, Math.ceil(STATE.total / PAGE_SIZE));
    if (STATE.page + 1 < pages) { STATE.page++; search(); }
  });

  (async () => { await loadFilterOptions(); search(); })();
}
