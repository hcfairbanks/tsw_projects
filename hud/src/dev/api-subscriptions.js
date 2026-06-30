// API Subscriptions sub-tab. CRUD over `resources/api_calls.json` — the
// catalog the TSW poller subscribes to on startup. Mirrors hud-go's
// /api-subscriptions admin page, scoped to the catalog editor: subscription
// lifecycle (reset/create/delete/test) is staged for a follow-up.
//
// Catalog shape:
//   { sections: [ { name, builtin?, calls: [ { path, key?, label?, enabled } ] } ] }
//
// Built-in sections can't be renamed or deleted (the Core subscriptions the
// HUD assumes are live). Their individual calls stay editable so users can
// disable specific paths if they're noisy on a given route.

import { t } from '../shared/i18n.js';

const { invoke } = window.__TAURI__.core;

const esc = (s) => String(s == null ? '' : s).replace(/[&<>"]/g,
  (c) => ({'&':'&amp;','<':'&lt;','>':'&gt;','"':'&quot;'}[c]));

let CATALOG = { sections: [] };
let ROOT = null;

const markup = () => `
  <div class="card">
    <h2>${esc(t('common.status', 'Status'))}</h2>
    <div id="status" class="muted">${esc(t('common.loading', 'loading…'))}</div>
    <div class="row" style="margin-top: 12px;">
      <button data-sub-act="reset">${esc(t('shell.dev.resetRecreate', 'Reset (delete + recreate all)'))}</button>
      <button data-sub-act="create">${esc(t('shell.dev.createFromCatalog', 'Create from catalog'))}</button>
      <button class="danger" data-sub-act="delete">${esc(t('shell.dev.deleteAllSubs', 'Delete all subscriptions'))}</button>
      <button id="refreshStatusBtn">${esc(t('apiSubscriptions.refreshStatus', 'Refresh status'))}</button>
    </div>
    <div id="subActionFlash" class="flash" style="margin-top: 8px;"></div>
  </div>

  <div class="card">
    <h2>${esc(t('shell.dev.testCommApiPath', 'Test a CommAPI path'))}</h2>
    <p class="muted" style="margin:0 0 8px 0;">${esc(t('shell.dev.testPathDesc', 'Sanity-check a path before adding it to the catalog. POSTs the subscription on a temporary bag, samples it for ~600 ms, then deletes it.'))}</p>
    <div class="row">
      <div class="grow"><input type="text" id="testPath" placeholder="${esc(t('shell.dev.testPathPlaceholder', 'e.g. CurrentDrivableActor.Property.SpeedometerKph'))}"></div>
      <div><button id="testPathBtn" class="primary">${esc(t('shell.dev.test', 'Test'))}</button></div>
    </div>
    <pre id="testResult" style="margin:10px 0 0 0; padding:10px; background:var(--bg3); border-radius:6px; font-size:0.78rem; max-height:220px; overflow:auto; display:none;"></pre>
  </div>

  <div class="card">
    <h2>${esc(t('shell.dev.catalog', 'Catalog'))}</h2>
    <p class="muted" style="margin: 0 0 8px 0; line-height: 1.55;">
      ${t('shell.dev.catalogDesc', 'The TSW poller subscribes to one CommAPI path per enabled call below on startup. Built-in sections cover the core HUD telemetry; add or remove sections to capture extra data for specific locos, scenarios, or custom HUDs. Changes save to <code>resources/api_calls.json</code>; click "Reset" above (or relaunch) to push them to TSW.')}
    </p>
    <div class="row" style="margin-top: 12px;">
      <button id="addSectionBtn" class="primary">${esc(t('shell.dev.addSection', '+ Add section'))}</button>
      <button id="reloadBtn">${esc(t('shell.dev.reloadFromDisk', 'Reload from disk'))}</button>
      <button id="saveBtn" class="primary">${esc(t('shell.dev.saveCatalog', 'Save catalog'))}</button>
      <span id="flash" class="flash" style="margin-left:auto;"></span>
    </div>
  </div>

  <div id="sections"></div>

  <div class="card">
    <h2>${esc(t('shell.dev.liveDataPreview', 'Live data preview'))}</h2>
    <p class="muted" style="margin:0 0 8px 0;">${esc(t('shell.dev.liveDataPreviewDesc', "Raw JSON straight from TSW's /subscription/ bag — every Entry, including paths the parser doesn't surface."))}</p>
    <div class="row">
      <button id="livePreviewBtn">${esc(t('shell.dev.fetchSnapshot', 'Fetch snapshot'))}</button>
      <span id="livePreviewFlash" class="muted" style="margin-left:auto;font-size:0.8rem;"></span>
    </div>
    <pre id="livePreview" style="margin:10px 0 0 0; padding:10px; background:var(--bg3); border-radius:6px; font-size:0.78rem; max-height:300px; overflow:auto; display:none;"></pre>
  </div>
`;

function renderSections() {
  const host = ROOT.querySelector('#sections');
  if (!CATALOG.sections.length) {
    host.innerHTML = `<div class="card"><div class="muted">${esc(t('shell.dev.noSections', 'no sections yet — click "Add section" above'))}</div></div>`;
    return;
  }
  host.innerHTML = CATALOG.sections.map((s, si) => {
    const calls = (s.calls || []).map((c, ci) => `<tr>
      <td><input type="checkbox" data-act="toggle" data-si="${si}" data-ci="${ci}" ${c.enabled ? 'checked' : ''}></td>
      <td><input type="text" class="cell" data-act="path"  data-si="${si}" data-ci="${ci}" value="${esc(c.path || '')}" placeholder="${esc(t('shell.dev.commApiPath', 'CommAPI path'))}"></td>
      <td><input type="text" class="cell" data-act="key"   data-si="${si}" data-ci="${ci}" value="${esc(c.key || '')}" placeholder="${esc(t('shell.dev.streamKeyOptional', 'stream key (optional)'))}"></td>
      <td><input type="text" class="cell" data-act="label" data-si="${si}" data-ci="${ci}" value="${esc(c.label || '')}" placeholder="${esc(t('shell.dev.labelOptional', 'label (optional)'))}"></td>
      <td class="actions"><button class="danger" data-act="del-call" data-si="${si}" data-ci="${ci}">${esc(t('common.delete', 'Delete'))}</button></td>
    </tr>`).join('');
    const callCount = (s.calls || []).length;
    const headerActions = s.builtin
      ? `<span class="pill">${esc(t('shell.dev.builtIn', 'built-in'))}</span>`
      : `<button data-act="rename-section" data-si="${si}">${esc(t('shell.dev.rename', 'Rename'))}</button>
         <button class="danger" data-act="del-section" data-si="${si}">${esc(t('shell.dev.deleteSection', 'Delete section'))}</button>`;
    return `<div class="card">
      <h2 style="display:flex;align-items:center;gap:10px;">
        <span>${esc(s.name || '(unnamed)')}</span>
        <span class="muted" style="font-size:0.78rem;font-weight:500;">${callCount} ${esc(callCount === 1 ? t('shell.dev.callSingular', 'call') : t('shell.dev.callPlural', 'calls'))}</span>
        <span style="margin-left:auto;display:flex;gap:6px;">${headerActions}</span>
      </h2>
      <table>
        <thead>
          <tr>
            <th style="width:32px;">${esc(t('shell.dev.colOn', 'On'))}</th>
            <th>${esc(t('shell.dev.colPath', 'Path'))}</th>
            <th style="width:25%;">${esc(t('shell.dev.colStreamKey', 'Stream key'))}</th>
            <th style="width:20%;">${esc(t('shell.dev.colLabel', 'Label'))}</th>
            <th class="actions" style="width:90px;">${esc(t('common.actions', 'Actions'))}</th>
          </tr>
        </thead>
        <tbody>${calls || `<tr><td colspan="5" class="muted" style="text-align:center;padding:14px;">${esc(t('shell.dev.noCallsYet', 'no calls yet'))}</td></tr>`}</tbody>
      </table>
      <div style="margin-top:10px;">
        <button data-act="add-call" data-si="${si}">${esc(t('shell.dev.addCall', '+ Add call'))}</button>
      </div>
    </div>`;
  }).join('');
}

function flash(msg, cls = '') {
  const el = ROOT.querySelector('#flash');
  if (!el) return;
  el.textContent = msg || '';
  el.className = 'flash' + (cls ? ' ' + cls : '');
}

async function load() {
  try {
    const data = await invoke('api_calls_get');
    CATALOG = data && Array.isArray(data.sections) ? data : { sections: [] };
    renderSections();
    flash(t('shell.dev.loaded', 'loaded'), 'ok');
  } catch (e) {
    CATALOG = { sections: [] };
    renderSections();
    flash(t('shell.dev.loadFailed', 'load failed') + ': ' + e);
  }
}

async function save() {
  try {
    await invoke('api_calls_set', { body: CATALOG });
    flash(t('shell.dev.savedRestart', 'saved — restart subscriptions to apply'), 'ok');
  } catch (e) {
    flash(t('shell.dev.saveFailed', 'save failed') + ': ' + e);
  }
}

// All in-card actions delegate through one click handler — the rows are
// re-rendered on every CATALOG mutation so wiring per-row listeners would
// leak.
function attachClicks() {
  ROOT.querySelector('#sections').addEventListener('click', (ev) => {
    const btn = ev.target.closest('button[data-act]');
    if (!btn) return;
    const si = Number(btn.dataset.si);
    const ci = Number(btn.dataset.ci);
    const act = btn.dataset.act;

    if (act === 'add-call') {
      CATALOG.sections[si].calls = CATALOG.sections[si].calls || [];
      CATALOG.sections[si].calls.push({ path: '', key: '', label: '', enabled: true });
      renderSections();
    } else if (act === 'del-call') {
      if (!confirm(t('shell.dev.confirmDeleteCall', 'Delete this call?'))) return;
      CATALOG.sections[si].calls.splice(ci, 1);
      renderSections();
    } else if (act === 'rename-section') {
      const newName = prompt(t('shell.dev.sectionName', 'Section name'), CATALOG.sections[si].name || '');
      if (newName && newName.trim()) {
        CATALOG.sections[si].name = newName.trim();
        renderSections();
      }
    } else if (act === 'del-section') {
      if (!confirm(t('shell.dev.confirmDeleteSection', 'Delete section "{name}" and all {count} calls?')
          .replace('{name}', CATALOG.sections[si].name)
          .replace('{count}', (CATALOG.sections[si].calls || []).length))) return;
      CATALOG.sections.splice(si, 1);
      renderSections();
    }
  });

  // Input handlers bubble through the sections host — cheaper than per-cell
  // listeners and the re-render keeps state in sync.
  ROOT.querySelector('#sections').addEventListener('input', (ev) => {
    const el = ev.target;
    if (!el.matches('input[data-act]')) return;
    const si = Number(el.dataset.si);
    const ci = Number(el.dataset.ci);
    const act = el.dataset.act;
    const call = CATALOG.sections[si] && CATALOG.sections[si].calls
      && CATALOG.sections[si].calls[ci];
    if (!call) return;
    if (act === 'toggle')      call.enabled = el.checked;
    else if (act === 'path')   call.path    = el.value;
    else if (act === 'key')    call.key     = el.value;
    else if (act === 'label')  call.label   = el.value;
  });
}

async function refreshStatus() {
  const el = ROOT.querySelector('#status');
  if (!el) return;
  el.textContent = t('common.loading', 'loading…');
  el.style.color = '';
  try {
    const s = await invoke('subscription_status');
    const parts = [
      s.api_key_resolved ? `<span style="color:var(--good);">● ${esc(t('shell.dev.apiKeyFound', 'API key found'))}</span>`
                         : `<span style="color:var(--bad);">○ ${esc(t('shell.dev.noApiKey', 'No API key (Settings → CommAPI key)'))}</span>`,
      s.enable_subscriptions ? `<span style="color:var(--good);">● ${esc(t('shell.dev.pollerEnabled', 'Poller enabled'))}</span>`
                             : `<span style="color:var(--warn);">○ ${esc(t('shell.dev.pollerDisabled', 'Poller disabled in Settings'))}</span>`,
      `<span class="pill">${esc(t('shell.dev.callsEnabledFmt', '{enabled} / {total} calls enabled').replace('{enabled}', s.enabled_calls).replace('{total}', s.total_calls))}</span>`,
    ];
    el.innerHTML = parts.join('  &nbsp;  ');
  } catch (e) {
    el.innerHTML = `<span style="color:var(--bad);">${esc(t('shell.dev.statusFailed', 'status failed'))}: ${esc(e)}</span>`;
  }
}

function setActionFlash(msg, cls = '') {
  const el = ROOT.querySelector('#subActionFlash');
  if (!el) return;
  el.textContent = msg || '';
  el.className = 'flash' + (cls ? ' ' + cls : '');
}

async function runSubAction(act) {
  setActionFlash(act === 'delete' ? t('shell.dev.deleting', 'deleting…')
    : act === 'create' ? t('shell.dev.creating', 'creating…')
    : t('shell.dev.resetting', 'resetting…'));
  try {
    const msg = await invoke('subscription_action', { action: act });
    setActionFlash(msg, 'ok');
    refreshStatus();
  } catch (e) {
    setActionFlash(String(e), 'err');
  }
}

async function runTestPath() {
  const inp = ROOT.querySelector('#testPath');
  const out = ROOT.querySelector('#testResult');
  const path = (inp.value || '').trim();
  if (!path) { out.style.display = 'none'; return; }
  out.style.display = 'block';
  out.textContent = t('shell.dev.probing', 'probing…');
  out.style.color = '';
  try {
    const res = await invoke('subscription_test_path', { path });
    out.textContent = JSON.stringify(res, null, 2);
    out.style.color = (res && res.ok) ? '' : 'var(--warn)';
  } catch (e) {
    out.textContent = t('shell.dev.failed', 'failed') + ': ' + e;
    out.style.color = 'var(--bad)';
  }
}

async function runLivePreview() {
  const out = ROOT.querySelector('#livePreview');
  const fl  = ROOT.querySelector('#livePreviewFlash');
  out.style.display = 'block';
  out.textContent = t('shell.dev.fetching', 'fetching…');
  fl.textContent = '';
  try {
    const data = await invoke('subscription_data');
    out.textContent = JSON.stringify(data, null, 2);
    const entries = data && data.Entries && Array.isArray(data.Entries) ? data.Entries.length : 0;
    fl.textContent = t('shell.dev.entriesCountFmt', '{n} entries').replace('{n}', entries) + ' · ' + new Date().toLocaleTimeString();
  } catch (e) {
    out.textContent = t('shell.dev.failed', 'failed') + ': ' + e;
    out.style.color = 'var(--bad)';
  }
}

export function mount(root) {
  ROOT = root;
  root.innerHTML = markup();
  // Re-render with the new language when the shell switches locale.
  document.addEventListener('languageChanged', () => { if (root.isConnected) mount(root); });

  root.querySelector('#addSectionBtn').addEventListener('click', () => {
    const name = prompt(t('shell.dev.sectionNamePrompt', 'Section name (e.g. "BR 442 LZB")'));
    if (!name || !name.trim()) return;
    CATALOG.sections.push({ name: name.trim(), calls: [] });
    renderSections();
  });
  root.querySelector('#reloadBtn').addEventListener('click', load);
  root.querySelector('#saveBtn').addEventListener('click', save);

  root.querySelectorAll('button[data-sub-act]').forEach((btn) => {
    btn.addEventListener('click', () => runSubAction(btn.dataset.subAct));
  });
  root.querySelector('#refreshStatusBtn').addEventListener('click', refreshStatus);
  root.querySelector('#testPathBtn').addEventListener('click', runTestPath);
  root.querySelector('#testPath').addEventListener('keydown', (e) => {
    if (e.key === 'Enter') { e.preventDefault(); runTestPath(); }
  });
  root.querySelector('#livePreviewBtn').addEventListener('click', runLivePreview);

  attachClicks();
  refreshStatus();
  load();
}
