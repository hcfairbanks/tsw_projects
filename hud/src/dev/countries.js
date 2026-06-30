// Countries sub-tab. Full CRUD with route/timetable counts + flag icons.
import { t } from '../shared/i18n.js';

const { invoke } = window.__TAURI__.core;

const esc = (s) => String(s == null ? '' : s).replace(/[&<>"]/g,
  (c) => ({'&':'&amp;','<':'&lt;','>':'&gt;','"':'&quot;'}[c]));
const flag = (code) => code && code.length === 2
  ? `<span class="fi fi-${esc(code.toLowerCase())}"></span>` : '';

const markup = () => `
  <div class="card">
    <h2>${esc(t('countries.addNew', 'Add country'))}</h2>
    <form id="addForm" class="row">
      <div class="grow">
        <label for="addName">${esc(t('common.name', 'Name'))}</label>
        <input type="text" id="addName" placeholder="${esc(t('countries.namePlaceholder', 'e.g. United Kingdom'))}" required>
      </div>
      <div class="small">
        <label for="addCode">${esc(t('shell.dev.isoCode', 'ISO code'))}</label>
        <input type="text" id="addCode" placeholder="${esc(t('countries.codePlaceholder', 'GB'))}" maxlength="2">
      </div>
      <div><button class="primary" type="submit">${esc(t('common.add', 'Add'))}</button></div>
    </form>
  </div>

  <div class="card" id="editCard">
    <h2>${esc(t('countries.editCountry', 'Edit country'))}</h2>
    <form id="editForm" class="row">
      <input type="hidden" id="editId">
      <div class="grow">
        <label for="editName">${esc(t('common.name', 'Name'))}</label>
        <input type="text" id="editName" required>
      </div>
      <div class="small">
        <label for="editCode">${esc(t('shell.dev.isoCode', 'ISO code'))}</label>
        <input type="text" id="editCode" maxlength="2">
      </div>
      <div>
        <button class="primary" type="submit">${esc(t('common.save', 'Save'))}</button>
        <button type="button" id="editCancel">${esc(t('common.cancel', 'Cancel'))}</button>
      </div>
    </form>
  </div>

  <div class="card">
    <h2>${esc(t('countries.allCountries', 'All countries'))}</h2>
    <div id="flash" class="flash"></div>
    <table>
      <thead>
        <tr>
          <th style="width:55%;">${esc(t('shell.dev.colCountry', 'Country'))}</th>
          <th>${esc(t('common.code', 'Code'))}</th>
          <th>${esc(t('shell.dev.colRoutes', 'Routes'))}</th>
          <th>${esc(t('shell.dev.colTimetables', 'Timetables'))}</th>
          <th class="actions">${esc(t('common.actions', 'Actions'))}</th>
        </tr>
      </thead>
      <tbody id="rows"><tr><td colspan="5" class="muted" style="padding:24px;text-align:center;">${esc(t('common.loading', 'loading…'))}</td></tr></tbody>
    </table>
  </div>
`;

export function mount(root) {
  root.innerHTML = markup();
  // Re-render with the new language when the shell switches locale.
  document.addEventListener('languageChanged', () => { if (root.isConnected) mount(root); });

  const flash = (msg, cls = '') => {
    const el = root.querySelector('#flash');
    if (!el) return;
    el.textContent = msg || '';
    el.className = 'flash' + (cls ? ' ' + cls : '');
  };

  async function load() {
    try {
      const rows = await invoke('countries_list_full');
      const tbody = root.querySelector('#rows');
      if (!tbody) return;
      if (!rows.length) {
        tbody.innerHTML = `<tr><td colspan="5" class="muted" style="padding:24px;text-align:center;">${esc(t('countries.noCountries', 'no countries'))}</td></tr>`;
        return;
      }
      tbody.innerHTML = rows.map((c) => `<tr>
        <td>${flag(c.code)}${esc(c.name || '(unnamed)')}</td>
        <td>${esc(c.code || '—')}</td>
        <td><span class="pill">${c.route_count}</span></td>
        <td><span class="pill">${c.timetable_count}</span></td>
        <td class="actions">
          <button data-act="edit" data-id="${c.id}" data-name="${esc(c.name)}" data-code="${esc(c.code)}">${esc(t('common.edit', 'Edit'))}</button>
          <button class="danger" data-act="del" data-id="${c.id}" data-name="${esc(c.name)}">${esc(t('common.delete', 'Delete'))}</button>
        </td>
      </tr>`).join('');
    } catch (e) { flash(t('shell.dev.loadFailed', 'load failed') + ': ' + e); }
  }

  root.querySelector('#rows').addEventListener('click', async (ev) => {
    const btn = ev.target.closest('button[data-act]');
    if (!btn) return;
    const id = Number(btn.dataset.id);
    if (btn.dataset.act === 'edit') {
      root.querySelector('#editId').value   = id;
      root.querySelector('#editName').value = btn.dataset.name || '';
      root.querySelector('#editCode').value = btn.dataset.code || '';
      root.querySelector('#editCard').classList.add('show');
      root.querySelector('#editName').focus();
      return;
    }
    if (btn.dataset.act === 'del') {
      if (!confirm(t('shell.dev.confirmDeleteNamed', 'Delete "{name}"?').replace('{name}', btn.dataset.name))) return;
      try { await invoke('country_delete', { id }); flash(t('shell.dev.deleted', 'deleted'), 'ok'); load(); }
      catch (e) { flash(t('shell.dev.deleteFailed', 'delete failed') + ': ' + e); }
    }
  });

  root.querySelector('#addForm').addEventListener('submit', async (ev) => {
    ev.preventDefault();
    const body = {
      id: null,
      name: root.querySelector('#addName').value.trim(),
      code: root.querySelector('#addCode').value.trim(),
    };
    try {
      await invoke('country_save', { body });
      root.querySelector('#addName').value = '';
      root.querySelector('#addCode').value = '';
      flash(t('shell.dev.added', 'added'), 'ok'); load();
    } catch (e) { flash(t('shell.dev.addFailed', 'add failed') + ': ' + e); }
  });

  root.querySelector('#editForm').addEventListener('submit', async (ev) => {
    ev.preventDefault();
    const body = {
      id:   Number(root.querySelector('#editId').value),
      name: root.querySelector('#editName').value.trim(),
      code: root.querySelector('#editCode').value.trim(),
    };
    try {
      await invoke('country_save', { body });
      root.querySelector('#editCard').classList.remove('show');
      flash(t('shell.dev.saved', 'saved'), 'ok'); load();
    } catch (e) { flash(t('shell.dev.saveFailed', 'save failed') + ': ' + e); }
  });

  root.querySelector('#editCancel').addEventListener('click', () => {
    root.querySelector('#editCard').classList.remove('show');
  });

  load();
}
