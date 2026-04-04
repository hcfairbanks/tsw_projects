// ─── API ──────────────────────────────────────────────────────────────────────

async function api(method, path, body) {
  const opts = { method, headers: { 'Content-Type': 'application/json' } };
  if (body) opts.body = JSON.stringify(body);
  const r = await fetch('/api' + path, opts);
  if (!r.ok) throw new Error(await r.text());
  return r.json();
}
const GET  = (p, q) => api('GET', p + (q ? '?' + new URLSearchParams(q) : ''));
const POST = (p, b) => api('POST', p, b);
const PUT  = (p, b) => api('PUT',  p, b);
const DEL  = (p)    => api('DELETE', p);

// ─── Country flags ───────────────────────────────────────────────────────────

// Country code + flag image URLs (Windows doesn't render Unicode flag emojis)
const COUNTRY_CODE = {
  'united kingdom': 'GB', 'uk': 'GB',
  'united states': 'US',  'us': 'US', 'usa': 'US',
  'germany': 'DE',        'de': 'DE',
  'austria': 'AT',        'aus': 'AT', 'at': 'AT',
  'switzerland': 'CH',    'sui': 'CH', 'ch': 'CH',
  'netherlands': 'NL',    'nl': 'NL',
  'france': 'FR',         'fr': 'FR',
  'canada': 'CA',         'ca': 'CA',
  'czech republic': 'CZ', 'cze': 'CZ', 'cz': 'CZ',
};

function countryCode(name) {
  if (!name) return null;
  return COUNTRY_CODE[name.toLowerCase()] || null;
}
function countryFlagHtml(name) {
  const code = countryCode(name);
  if (!code) return '';
  return `<span class="fi fi-${code.toLowerCase()}" style="margin-right:6px"></span>`;
}
function countryLabel(name) {
  // HTML version with flag image
  return countryFlagHtml(name) + esc(name || '');
}
function countryFlagEmoji(name) {
  const code = countryCode(name);
  if (!code) return '';
  // Convert country code to regional indicator emoji (A=🇦, B=🇧, etc.)
  return String.fromCodePoint(...[...code].map(c => 0x1F1E6 + c.charCodeAt(0) - 65));
}
function countryLabelText(name) {
  // Text-only version for <option> elements (uses emoji flags)
  const flag = countryFlagEmoji(name);
  return flag ? `${flag} ${name}` : name;
}

// ─── Date helpers ────────────────────────────────────────────────────────────

const MONTHS = ['January','February','March','April','May','June','July','August','September','October','November','December'];

function formatDate(s) {
  if (!s) return '';
  // Handle ISO format: YYYY-MM-DD
  const iso = s.match(/^(\d{4})-(\d{2})-(\d{2})$/);
  if (iso) return `${parseInt(iso[3])} ${MONTHS[parseInt(iso[2]) - 1]} ${iso[1]}`;
  return s;
}
function toDateInput(s) {
  if (!s) return '';
  // Already ISO
  if (/^\d{4}-\d{2}-\d{2}$/.test(s)) return s;
  return '';
}
function fromDateInput(s) { return s || ''; }

// ─── Shared lookup cache ─────────────────────────────────────────────────────

let allCountries = [], allDlcTypes = [], allTrains = [], allTswVersions = [];
let defaultTswVersion = null;
let dlcFiltersInitialized = false;

const DLC_TYPE_ORDER = ['Route', 'Loco', 'Expansion', 'Other'];

async function loadLookups() {
  [allCountries, allDlcTypes, allTrains, allTswVersions] = await Promise.all([
    GET('/countries'), GET('/dlc-types'), GET('/trains'), GET('/tsw-versions'),
  ]);
  const def = allTswVersions.find(v => v.is_default);
  defaultTswVersion = def ? def.id : null;
  // Apply default filter on first load
  if (!dlcFiltersInitialized && defaultTswVersion) {
    dlcFilters.tsw_version = dlcFilters.tsw_version || defaultTswVersion;
    dlcFiltersInitialized = true;
  }
  allDlcTypes.sort((a, b) => {
    const ai = DLC_TYPE_ORDER.indexOf(a.name), bi = DLC_TYPE_ORDER.indexOf(b.name);
    return (ai === -1 ? 999 : ai) - (bi === -1 ? 999 : bi);
  });
  allCountries.sort((a, b) => {
    const af = !!countryCode(a.name), bf = !!countryCode(b.name);
    if (af !== bf) return af ? -1 : 1;
    return a.name.localeCompare(b.name);
  });
}

function countryOptions(selectedId) {
  return allCountries.map(c =>
    `<option value="${c.id}" ${c.id == selectedId ? 'selected' : ''}>${countryLabelText(c.name)}</option>`
  ).join('');
}

// ─── Router ──────────────────────────────────────────────────────────────────

function navigate(path) {
  history.pushState({}, '', path);
  route(path);
}

function route(path) {
  document.querySelectorAll('.nav-link').forEach(a => {
    const r = a.dataset.route;
    a.classList.toggle('active', path === r || path.startsWith(r + '/'));
  });

  const parts = path.replace(/^\//, '').split('/');
  const seg = parts[0] || 'dlc';
  const id = parts[1];

  if (seg === 'dlc' && id)         renderDlcDetail(id);
  else if (seg === 'dlc')          renderDlcList();
  else if (seg === 'trains' && id) renderTrainDetail(id);
  else if (seg === 'trains')       renderTrains();
  else if (seg === 'library')      renderLibrary();
  else if (seg === 'cart')         renderCart();
  else if (seg === 'admin')        renderAdmin(id || 'countries');
  else                             renderDlcList();
}

window.addEventListener('popstate', () => route(location.pathname));

// ─── Render helpers ──────────────────────────────────────────────────────────

const root = document.getElementById('root');

function esc(s) {
  return String(s ?? '').replace(/&/g, '&amp;').replace(/</g, '&lt;').replace(/>/g, '&gt;').replace(/"/g, '&quot;');
}

function fmtPrice(s) {
  if (!s) return '';
  // "CDN$ 20.79" → "$20.79", "CAD 25.99" → "$25.99", "$19.99" → "$19.99", "£15.99" → "£15.99"
  return esc(String(s).replace(/^[A-Z]{2,3}\$?\s*/i, '$'));
}

function typeTag(name) {
  if (!name) return '';
  const n = name.toLowerCase();
  const cls = n === 'route' ? 'tag-route' : n === 'loco' ? 'tag-loco' : 'tag-default';
  return `<span class="tag ${cls}">${esc(name)}</span>`;
}

// ─── DLC List ────────────────────────────────────────────────────────────────

let dlcPage = 1;
let dlcFilters = {};
let dlcSort = { by: 'name', dir: 'asc' };

async function renderDlcList() {
  const [, priceStatus] = await Promise.all([loadLookups(), GET('/prices/status')]);
  const lastFetch = priceStatus.last_updated
    ? new Date(priceStatus.last_updated).toLocaleString()
    : null;
  root.innerHTML = `
    <div class="page-header">
      <div>
        <h1 class="page-title">DLC</h1>
        ${lastFetch ? `<div class="td-sub" style="font-size:11px;margin-top:2px">Prices updated: ${lastFetch}</div>` : ''}
      </div>
      <button class="btn btn-primary" id="btn-add-dlc">+ Add DLC</button>
    </div>
    <div class="filter-bar">
      <input id="dlc-search" type="search" placeholder="Search name, acronym, developer\u2026" value="${esc(dlcFilters.search || '')}">
      <div class="custom-select" id="f-country-wrap">
        <div class="custom-select-display" id="f-country-display">${dlcFilters.country_id ? countryLabel(allCountries.find(c => c.id == dlcFilters.country_id)?.name || '') : 'All Countries'}</div>
        <div class="custom-select-list hidden" id="f-country-list">
          <div class="custom-select-option" data-value="">All Countries</div>
          ${allCountries.map(c => `<div class="custom-select-option" data-value="${c.id}">${countryLabel(c.name)}</div>`).join('')}
        </div>
        <input type="hidden" id="f-country" value="${dlcFilters.country_id || ''}">
      </div>
      <select id="f-version">
        <option value="">All TSW Versions</option>
        ${allTswVersions.map(v => `<option value="${v.id}" ${dlcFilters.tsw_version == v.id ? 'selected' : ''}>${esc(v.name)}</option>`).join('')}
      </select>
      <select id="f-type">
        <option value="">All Types</option>
        ${allDlcTypes.map(t => `<option value="${t.id}" ${dlcFilters.dlc_type_id == t.id ? 'selected' : ''}>${esc(t.name)}</option>`).join('')}
      </select>
    </div>
    <div class="filter-bar">
      <div class="price-slider-wrap">
        <label class="price-slider-label" id="price-slider-label">Price: Any</label>
        <div class="range-slider" id="range-slider">
          <div class="range-track"></div>
          <div class="range-fill" id="range-fill"></div>
          <input type="range" id="price-lo" min="0" max="75" step="1" value="${dlcFilters.price_lo ?? 0}">
          <input type="range" id="price-hi" min="0" max="75" step="1" value="${dlcFilters.price_hi ?? 75}">
        </div>
      </div>
      <select id="f-owned">
        <option value="">All DLCs</option>
        <option value="include" ${dlcFilters.owned === 'include' ? 'selected' : ''}>In My Library</option>
        <option value="exclude" ${dlcFilters.owned === 'exclude' ? 'selected' : ''}>Not In My Library</option>
      </select>
      <button class="btn btn-ghost" id="btn-clear-dlc-search">\u2715 Clear</button>
      <button class="btn btn-primary" id="btn-search-dlc">Search</button>
    </div>
    <div class="table-card">
      <div class="table-scroll">
        <table>
          <thead><tr>
            <th style="width:1%"></th>
            <th class="sortable-th" data-sort="price">Price</th>
            <th class="sortable-th" data-sort="name">Name</th>
            <th class="sortable-th" data-sort="type">Type</th>
            <th class="sortable-th" data-sort="country">Country</th>
            <th style="width:1%"></th>
          </tr></thead>
          <tbody id="dlc-tbody"><tr><td colspan="8" class="empty">Loading\u2026</td></tr></tbody>
        </table>
      </div>
      <div id="dlc-pagination" class="pagination"></div>
    </div>`;

  await fetchDlc();

  document.getElementById('btn-add-dlc').onclick = () => navigate('/dlc/new');

  document.getElementById('btn-clear-dlc-search').onclick = () => {
    dlcFilters = {};
    dlcPage = 1;
    document.getElementById('dlc-search').value = '';
    document.getElementById('f-country').value = '';
    document.getElementById('f-country-display').innerHTML = 'All Countries';
    document.getElementById('f-version').value = '';
    document.getElementById('f-type').value = '';
    document.getElementById('price-lo').value = 0;
    document.getElementById('price-hi').value = 75;
    document.getElementById('f-owned').value = '';
    updatePriceSlider();
    fetchDlc();
  };

  document.getElementById('btn-search-dlc').onclick = () => {
    dlcFilters.search = document.getElementById('dlc-search').value || undefined;
    dlcPage = 1;
    fetchDlc();
  };

  let timer;
  document.getElementById('dlc-search').addEventListener('input', e => {
    clearTimeout(timer);
    timer = setTimeout(() => { dlcFilters.search = e.target.value || undefined; dlcPage = 1; fetchDlc(); }, 250);
  });
  document.getElementById('f-owned').addEventListener('change', e => { dlcFilters.owned = e.target.value || undefined; dlcPage = 1; fetchDlc(); });
  // Custom country dropdown
  const countryDisplay = document.getElementById('f-country-display');
  const countryList = document.getElementById('f-country-list');
  const countryInput = document.getElementById('f-country');
  countryDisplay.onclick = () => countryList.classList.toggle('hidden');
  document.addEventListener('click', e => { if (!document.getElementById('f-country-wrap')?.contains(e.target)) countryList.classList.add('hidden'); });
  countryList.querySelectorAll('.custom-select-option').forEach(opt => {
    opt.onclick = () => {
      countryInput.value = opt.dataset.value;
      countryDisplay.innerHTML = opt.dataset.value ? opt.innerHTML : 'All Countries';
      countryList.classList.add('hidden');
      dlcFilters.country_id = opt.dataset.value || undefined;
      dlcPage = 1;
      fetchDlc();
    };
  });
  document.getElementById('f-version').addEventListener('change', e => { dlcFilters.tsw_version = e.target.value || undefined; dlcPage = 1; fetchDlc(); });
  document.getElementById('f-type').addEventListener('change', e => { dlcFilters.dlc_type_id = e.target.value || undefined; dlcPage = 1; fetchDlc(); });

  // Price range slider
  const priceLo = document.getElementById('price-lo');
  const priceHi = document.getElementById('price-hi');

  function updatePriceSlider() {
    let lo = parseInt(priceLo.value), hi = parseInt(priceHi.value);
    if (lo > hi) { const t = lo; lo = hi; hi = t; priceLo.value = lo; priceHi.value = hi; }
    const max = parseInt(priceHi.max) || 75;
    const fill = document.getElementById('range-fill');
    fill.style.left = (lo / max * 100) + '%';
    fill.style.width = ((hi - lo) / max * 100) + '%';
    const label = document.getElementById('price-slider-label');
    if (lo === 0 && hi === 75) label.textContent = 'Price: Any';
    else if (lo === 0) label.textContent = `Price: Under $${hi}`;
    else if (hi === 75) label.textContent = `Price: $${lo}+`;
    else label.textContent = `Price: $${lo} \u2013 $${hi}`;
  }

  function applyPriceFilter() {
    const lo = parseInt(priceLo.value), hi = parseInt(priceHi.value);
    const min = Math.min(lo, hi), max = Math.max(lo, hi);
    dlcFilters.price_lo = min > 0 ? min : undefined;
    dlcFilters.price_hi = max < 75 ? max : undefined;
    dlcPage = 1;
    fetchDlc();
  }

  let sliderTimer;
  priceLo.addEventListener('input', () => { updatePriceSlider(); clearTimeout(sliderTimer); sliderTimer = setTimeout(applyPriceFilter, 300); });
  priceHi.addEventListener('input', () => { updatePriceSlider(); clearTimeout(sliderTimer); sliderTimer = setTimeout(applyPriceFilter, 300); });
  updatePriceSlider();

  document.querySelectorAll('.sortable-th').forEach(th => {
    th.addEventListener('click', () => {
      const key = th.dataset.sort;
      if (dlcSort.by === key) dlcSort.dir = dlcSort.dir === 'asc' ? 'desc' : 'asc';
      else { dlcSort.by = key; dlcSort.dir = 'asc'; }
      dlcPage = 1;
      fetchDlc();
    });
  });
}

async function fetchDlc() {
  const data = await GET('/dlc', { page: dlcPage, limit: 50, ...dlcFilters, sort_by: dlcSort.by, sort_dir: dlcSort.dir });

  document.querySelectorAll('.sortable-th').forEach(th => {
    const key = th.dataset.sort;
    const arrow = dlcSort.by === key ? (dlcSort.dir === 'asc' ? ' \u2191' : ' \u2193') : '';
    th.textContent = th.textContent.replace(/ [\u2191\u2193]$/, '') + arrow;
  });

  document.getElementById('dlc-tbody').innerHTML = data.rows.length
    ? data.rows.map((d, i) => {
        let priceCell = '\u2014';
        const buyLink = d.purchase_url;
        if (d.price && buyLink) {
          const inner = d.price_discount
            ? `<span style="font-weight:600">${fmtPrice(d.price)}</span> <span style="color:var(--success);font-weight:600;font-size:11px">-${esc(d.price_discount)}</span>`
            : `<span style="font-weight:600">${fmtPrice(d.price)}</span>`;
          priceCell = `<a href="${esc(buyLink)}" target="_blank" rel="noopener" onclick="event.stopPropagation()" style="color:var(--accent);text-decoration:none;white-space:nowrap">${inner}</a>`;
        } else if (d.price) {
          priceCell = d.price_discount
            ? `<span style="font-weight:600;font-size:12px">${fmtPrice(d.price)}</span> <span style="color:var(--success);font-weight:600;font-size:11px">-${esc(d.price_discount)}</span>`
            : `<span style="font-size:12px">${fmtPrice(d.price)}</span>`;
        } else if (buyLink) {
          priceCell = `<a href="${esc(buyLink)}" target="_blank" rel="noopener" onclick="event.stopPropagation()" style="color:var(--accent);text-decoration:none">Steam</a>`;
        }
        return `
      <tr data-href="/dlc/${d.id}">
        <td></td>
        <td style="white-space:nowrap">${priceCell}</td>
        <td class="td-primary">${esc(d.content_name)}</td>
        <td class="td-sub">${esc(d.dlc_type_name || '')}</td>
        <td class="td-sub">${countryLabel(d.country_name || '')}</td>
        <td class="td-actions" style="white-space:nowrap">
          ${cartBtn(d)}
          <button class="pill pill-amber btn-related" data-id="${d.id}" title="Show related DLCs">+</button>
        </td>
      </tr>
      <tr class="related-row hidden" id="related-${d.id}">
        <td colspan="8" style="padding:0;border-bottom:2px solid var(--accent)">
          <div class="related-panel" style="padding:12px 14px;background:var(--surface2)">
            <span class="td-sub">Loading\u2026</span>
          </div>
        </td>
      </tr>`;
      }).join('')
    : '<tr><td colspan="8" class="empty">No DLC found</td></tr>';

  document.querySelectorAll('#dlc-tbody tr[data-href]').forEach(tr => {
    tr.addEventListener('click', e => {
      if (e.target.closest('button')) return;
      navigate(tr.dataset.href);
    });
  });
  document.querySelectorAll('.btn-del-dlc').forEach(b => b.onclick = e => {
    e.stopPropagation();
    confirmDelete('DLC', b.dataset.id, async id => { await DEL(`/dlc/${id}`); fetchDlc(); });
  });
  document.querySelectorAll('.btn-related').forEach(b => b.onclick = e => {
    e.stopPropagation();
    toggleRelated(b.dataset.id, b);
  });
  wireCartButtons();

  const total = Math.ceil(data.total / data.limit);
  const pag = document.getElementById('dlc-pagination');
  if (total <= 1) { pag.innerHTML = ''; return; }
  pag.innerHTML = Array.from({ length: total }, (_, i) => i + 1)
    .filter(p => p === 1 || p === total || Math.abs(p - dlcPage) <= 2)
    .map(p => `<button class="${p === dlcPage ? 'active' : ''}" data-p="${p}">${p}</button>`)
    .join('');
  pag.querySelectorAll('button').forEach(b => b.onclick = () => { dlcPage = parseInt(b.dataset.p); fetchDlc(); });
}

// ─── Related DLCs dropdown ───────────────────────────────────────────────────

const relatedCache = {};

async function toggleRelated(dlcId, btn) {
  const row = document.getElementById('related-' + dlcId);
  if (!row) return;

  // Toggle visibility
  if (!row.classList.contains('hidden')) {
    row.classList.add('hidden');
    btn.textContent = '+';
    return;
  }

  // Show and load
  row.classList.remove('hidden');
  btn.textContent = '\u2212';

  // Check cache
  if (relatedCache[dlcId]) {
    renderRelatedPanel(dlcId, relatedCache[dlcId]);
    return;
  }

  try {
    const related = await GET(`/dlc/${dlcId}/related`);
    relatedCache[dlcId] = related;
    renderRelatedPanel(dlcId, related);
  } catch (e) {
    const panel = row.querySelector('.related-panel');
    panel.innerHTML = `<span style="color:var(--danger)">Failed to load: ${esc(e.message)}</span>`;
  }
}

function renderRelatedPanel(dlcId, related) {
  const row = document.getElementById('related-' + dlcId);
  if (!row) return;
  const panel = row.querySelector('.related-panel');

  if (!related.length) {
    panel.innerHTML = '<span class="td-sub">No related DLCs found</span>';
    return;
  }

  const rows = related.map(r => {
    let priceCell = '\u2014';
    if (r.price && r.purchase_url) {
      const inner = r.price_discount
        ? `<span style="font-weight:600">${fmtPrice(r.price)}</span> <span style="color:var(--success);font-weight:600;font-size:11px">-${esc(r.price_discount)}</span>`
        : `<span style="font-weight:600">${fmtPrice(r.price)}</span>`;
      priceCell = `<a href="${esc(r.purchase_url)}" target="_blank" rel="noopener" onclick="event.stopPropagation()" style="color:var(--accent);text-decoration:none;white-space:nowrap">${inner}</a>`;
    } else if (r.price) {
      priceCell = r.price_discount
        ? `<span style="font-weight:600;font-size:12px">${fmtPrice(r.price)}</span> <span style="color:var(--success);font-weight:600;font-size:11px">-${esc(r.price_discount)}</span>`
        : `<span style="font-size:12px">${fmtPrice(r.price)}</span>`;
    } else if (r.purchase_url) {
      priceCell = `<a href="${esc(r.purchase_url)}" target="_blank" rel="noopener" onclick="event.stopPropagation()" style="color:var(--accent);text-decoration:none">Steam</a>`;
    }

    return `<tr>
      <td>${cartBtn(r)}</td>
      <td style="white-space:nowrap">${priceCell}</td>
      <td class="td-primary"><a class="table-link" data-href="/dlc/${r.id}" onclick="event.stopPropagation(); navigate('/dlc/${r.id}')">${esc(r.content_name)}</a></td>
      <td class="td-sub">${typeTag(r.dlc_type_name)}</td>
    </tr>`;
  }).join('');

  panel.innerHTML = `
    <div class="table-card" style="margin:0;border-color:var(--border2)">
      <div class="table-scroll"><table>
        <thead><tr>
          <th style="width:1%"></th>
          <th>Price</th>
          <th>Related DLC</th>
          <th>Type</th>
        </tr></thead>
        <tbody>${rows}</tbody>
      </table></div>
    </div>`;
  wireCartButtons();
}

// ─── DLC Detail ──────────────────────────────────────────────────────────────

let currentDlcId = null;

async function renderDlcDetail(id) {
  await loadLookups();
  currentDlcId = id === 'new' ? null : id;

  let dlc = null;
  if (currentDlcId) dlc = await GET(`/dlc/${currentDlcId}`);

  const isRoute = dlc?.dlc_type_name === 'Route';

  root.innerHTML = `
    <div class="detail-header">
      <button class="detail-back" id="btn-back">\u2190 Back to DLC</button>
      <div class="detail-title-row">
        <div>
          <div class="detail-title" id="detail-title">${esc(dlc?.content_name || 'New DLC')}</div>
          <div class="detail-meta" id="detail-meta">${dlc ? buildMeta(dlc) : ''}</div>
        </div>
        <div class="detail-actions">
          ${currentDlcId ? `<button class="btn btn-danger btn-ghost" id="btn-del">Delete</button>` : ''}
          <button class="btn btn-primary" id="btn-save">Save Changes</button>
        </div>
      </div>
      <div class="tabs" id="detail-tabs">
        <button class="tab-btn active" data-tab="info">Info</button>
        <button class="tab-btn" data-tab="trains">Base Locos</button>
        <button class="tab-btn" data-tab="layers">Layers</button>
        <button class="tab-btn" data-tab="ai-layers">AI Layers</button>
        <button class="tab-btn" data-tab="substitutions">Substitutions</button>
        <button class="tab-btn" data-tab="documents">Documents</button>
        <button class="tab-btn" data-tab="store">Purchase Links</button>
        ${currentDlcId ? '<button class="tab-btn" data-tab="price-history">Price History</button>' : ''}
      </div>
    </div>

    <div id="tab-info" class="tab-panel active">${buildInfoForm(dlc)}</div>
    <div id="tab-trains" class="tab-panel">${buildTrainsPanel(dlc)}</div>
    <div id="tab-layers" class="tab-panel">${buildLayerTab(dlc, 'layers')}</div>
    <div id="tab-ai-layers" class="tab-panel">${buildLayerTab(dlc, 'ai_layers')}</div>
    <div id="tab-substitutions" class="tab-panel">${buildLayerTab(dlc, 'substitutions')}</div>
    <div id="tab-documents" class="tab-panel">${buildDocsPanel(dlc)}</div>
    <div id="tab-store" class="tab-panel">${buildStorePanel(dlc)}</div>
    ${currentDlcId ? '<div id="tab-price-history" class="tab-panel"><p class="empty">Loading...</p></div>' : ''}
  `;

  document.getElementById('btn-back').onclick = () => navigate('/dlc');
  document.getElementById('btn-save').onclick = () => saveDlc(dlc);

  if (currentDlcId) {
    document.getElementById('btn-del').onclick = () =>
      confirmDelete('DLC', currentDlcId, async id => { await DEL(`/dlc/${id}`); navigate('/dlc'); });
  }

  // Tabs
  let priceHistoryLoaded = false;
  document.querySelectorAll('.tab-btn').forEach(btn => {
    btn.onclick = () => {
      document.querySelectorAll('.tab-btn').forEach(b => b.classList.remove('active'));
      document.querySelectorAll('.tab-panel').forEach(p => p.classList.remove('active'));
      btn.classList.add('active');
      document.getElementById('tab-' + btn.dataset.tab).classList.add('active');
      if (btn.dataset.tab === 'price-history' && !priceHistoryLoaded && currentDlcId) {
        priceHistoryLoaded = true;
        loadPriceHistory(currentDlcId);
      }
    };
  });

  wireTrainsPanel(dlc);
  wireDocsPanel(dlc);
  wireStorePanel(dlc);
  wireAllLinks();
}

function wireAllLinks() {
  root.querySelectorAll('.table-link[data-href], .req-link[data-href]').forEach(a => {
    a.onclick = e => { e.preventDefault(); navigate(a.dataset.href); };
  });
}

async function loadPriceHistory(dlcId) {
  const panel = document.getElementById('tab-price-history');
  if (!panel) return;

  const history = await GET(`/dlc/${dlcId}/price-history`);

  if (!history.length) {
    panel.innerHTML = '<p class="empty" style="padding-top:40px">No price history recorded yet. Run a price fetch from Admin > Prices.</p>';
    return;
  }

  // Build table + chart
  const latest = history[history.length - 1];
  const currency = latest.price?.match(/^([A-Z]{2,3}\$?|\$|\u00a3|\u20ac)/)?.[1] || '$';

  panel.innerHTML = `
    <div style="max-width:900px;padding:4px 0">
      <div class="section-label">Price Over Time</div>
      <div style="background:var(--surface2);border:1px solid var(--border);border-radius:var(--r);padding:16px;margin-bottom:20px">
        <canvas id="price-chart" height="260" style="width:100%"></canvas>
      </div>
      <div class="section-label">History (${history.length} record${history.length !== 1 ? 's' : ''})</div>
      <div class="table-card"><div class="table-scroll"><table>
        <thead><tr><th>Date</th><th>Price</th><th>Original</th><th>Discount</th></tr></thead>
        <tbody>${history.slice().reverse().map(h => `<tr>
          <td class="td-sub" style="white-space:nowrap">${new Date(h.fetched_at).toLocaleString()}</td>
          <td class="td-primary" style="font-weight:600">${h.price ? fmtPrice(h.price) : '\u2014'}</td>
          <td class="td-sub">${h.price_original ? fmtPrice(h.price_original) : '\u2014'}</td>
          <td class="td-sub">${h.price_discount ? `<span style="color:var(--success);font-weight:600">${esc(h.price_discount)}</span>` : '\u2014'}</td>
        </tr>`).join('')}</tbody>
      </table></div></div>
    </div>`;

  drawPriceChart(history, currency);
}

function drawPriceChart(history, currency) {
  const canvas = document.getElementById('price-chart');
  if (!canvas) return;
  const ctx = canvas.getContext('2d');

  // High-DPI scaling
  const rect = canvas.getBoundingClientRect();
  const dpr = window.devicePixelRatio || 1;
  canvas.width = rect.width * dpr;
  canvas.height = rect.height * dpr;
  ctx.scale(dpr, dpr);
  const W = rect.width;
  const H = rect.height;

  const pad = { top: 20, right: 20, bottom: 40, left: 55 };
  const chartW = W - pad.left - pad.right;
  const chartH = H - pad.top - pad.bottom;

  // Extract data points
  const points = history.map(h => ({
    date: new Date(h.fetched_at),
    value: h.price_value,
    original: h.price_original_value,
    hasDiscount: !!h.price_discount,
  })).filter(p => p.value != null);

  if (!points.length) return;

  // Compute ranges
  const allValues = points.flatMap(p => p.original ? [p.value, p.original] : [p.value]);
  let minVal = Math.min(...allValues);
  let maxVal = Math.max(...allValues);
  if (minVal === maxVal) { minVal -= 1; maxVal += 1; }
  const valRange = maxVal - minVal;
  const minDate = points[0].date.getTime();
  const maxDate = points[points.length - 1].date.getTime();
  const dateRange = maxDate - minDate || 1;

  function x(date) { return pad.left + ((date.getTime() - minDate) / dateRange) * chartW; }
  function y(val) { return pad.top + chartH - ((val - minVal) / valRange) * chartH; }

  // Styles from CSS variables
  const style = getComputedStyle(document.documentElement);
  const textColor = style.getPropertyValue('--text-sub').trim() || '#888';
  const lineColor = style.getPropertyValue('--accent').trim() || '#3b82f6';
  const successColor = style.getPropertyValue('--success').trim() || '#22c55e';
  const gridColor = style.getPropertyValue('--border').trim() || '#333';

  // Grid lines and Y labels
  ctx.strokeStyle = gridColor;
  ctx.lineWidth = 0.5;
  ctx.fillStyle = textColor;
  ctx.font = '11px system-ui, sans-serif';
  ctx.textAlign = 'right';
  const ySteps = 5;
  for (let i = 0; i <= ySteps; i++) {
    const val = minVal + (valRange * i / ySteps);
    const yPos = y(val);
    ctx.beginPath(); ctx.moveTo(pad.left, yPos); ctx.lineTo(W - pad.right, yPos); ctx.stroke();
    ctx.fillText(currency + val.toFixed(2), pad.left - 6, yPos + 4);
  }

  // X labels
  ctx.textAlign = 'center';
  const labelCount = Math.min(points.length, 6);
  const step = Math.max(1, Math.floor(points.length / labelCount));
  for (let i = 0; i < points.length; i += step) {
    const p = points[i];
    const xPos = x(p.date);
    ctx.fillText(p.date.toLocaleDateString(), xPos, H - pad.bottom + 18);
  }
  // Always show last label
  if (points.length > 1) {
    const last = points[points.length - 1];
    ctx.fillText(last.date.toLocaleDateString(), x(last.date), H - pad.bottom + 18);
  }

  // Draw original price line (if any discounts exist)
  const hasAnyDiscount = points.some(p => p.hasDiscount && p.original);
  if (hasAnyDiscount) {
    ctx.strokeStyle = gridColor;
    ctx.lineWidth = 1.5;
    ctx.setLineDash([4, 4]);
    ctx.beginPath();
    let started = false;
    for (const p of points) {
      const val = p.original || p.value;
      if (!started) { ctx.moveTo(x(p.date), y(val)); started = true; }
      else ctx.lineTo(x(p.date), y(val));
    }
    ctx.stroke();
    ctx.setLineDash([]);
  }

  // Draw actual price line
  ctx.strokeStyle = lineColor;
  ctx.lineWidth = 2.5;
  ctx.beginPath();
  points.forEach((p, i) => {
    const px = x(p.date), py = y(p.value);
    if (i === 0) ctx.moveTo(px, py);
    else ctx.lineTo(px, py);
  });
  ctx.stroke();

  // Draw dots
  for (const p of points) {
    const px = x(p.date), py = y(p.value);
    ctx.beginPath();
    ctx.arc(px, py, 4, 0, Math.PI * 2);
    ctx.fillStyle = p.hasDiscount ? successColor : lineColor;
    ctx.fill();
    ctx.strokeStyle = '#fff';
    ctx.lineWidth = 1.5;
    ctx.stroke();
  }
}

function buildMeta(dlc) {
  const parts = [];
  if (dlc.dlc_type_name) parts.push(typeTag(dlc.dlc_type_name));
  if (dlc.country) parts.push(`<span class="td-sub">${countryLabel(dlc.country.name)}</span>`);
  if (dlc.developer) parts.push(`<span class="td-sub">${esc(dlc.developer)}</span>`);
  if (dlc.release_date) parts.push(`<span class="td-sub">${esc(formatDate(dlc.release_date))}</span>`);
  if (dlc.price) {
    const priceParts = [];
    if (dlc.price_discount) {
      priceParts.push(`<span style="text-decoration:line-through;color:var(--text-dim);font-size:11px">${fmtPrice(dlc.price_original)}</span>`);
    }
    priceParts.push(`<span style="font-weight:600">${fmtPrice(dlc.price)}</span>`);
    if (dlc.price_discount) {
      priceParts.push(`<span style="color:var(--success);font-weight:600;font-size:11px">-${esc(dlc.price_discount)}</span>`);
    }
    parts.push(`<span>${priceParts.join(' ')}</span>`);
  }
  const pcLink = (dlc.store_links || []).find(s => s.console_platform === 'PC' && s.store_url);
  const buyUrl = pcLink?.store_url;
  if (buyUrl) parts.push(`<a href="${esc(buyUrl)}" target="_blank" class="pill pill-blue" style="font-size:11px;text-decoration:none">Steam</a>`);
  return parts.join('');
}

function buildInfoForm(dlc) {
  return `
  <div style="max-width:640px">
    <div class="form-grid">
      <div class="form-row" style="grid-column:1/-1">
        <label>Content Name *</label>
        <input id="f-name" value="${esc(dlc?.content_name || '')}">
      </div>
      <div class="form-row">
        <label>Steam Name</label>
        <input id="f-steam-name" value="${esc(dlc?.steam_name || '')}">
      </div>
      <div class="form-row">
        <label>Short Name</label>
        <input id="f-short" value="${esc(dlc?.short_name || '')}">
      </div>
      <div class="form-row">
        <label>Acronym</label>
        <input id="f-acronym" value="${esc(dlc?.acronym || '')}">
      </div>
      <div class="form-row">
        <label>Developer</label>
        <input id="f-dev" value="${esc(dlc?.developer || '')}">
      </div>
      <div class="form-row">
        <label>DLC Type</label>
        <select id="f-type">
          <option value="">\u2014 None \u2014</option>
          ${allDlcTypes.map(t => `<option value="${t.id}" ${dlc?.dlc_type_id == t.id ? 'selected' : ''}>${esc(t.name)}</option>`).join('')}
        </select>
      </div>
      <div class="form-row">
        <label>Release Date</label>
        <input id="f-date" type="date" value="${toDateInput(dlc?.release_date || '')}">
      </div>
      <div class="form-row">
        <label>Country</label>
        <select id="f-country">
          <option value="">\u2014 None \u2014</option>
          ${countryOptions(dlc?.country?.id)}
        </select>
      </div>
    </div>

    <div class="section-label">TSW Version</div>
    <div class="pill-list">
      ${[1, 2, 3, 4, 5, 6].map(v => `<label><input type="checkbox" name="tsw_ver" value="${v}" ${(JSON.parse(dlc?.tsw_versions || '[]')).includes(v) ? 'checked' : ''}><span>TSW ${v}</span></label>`).join('')}
    </div>

    <div class="section-label">Features</div>
    <div class="form-grid">
      <div class="form-row"><label class="cb-row"><input type="checkbox" id="f-conductor" ${dlc?.conductor_mode ? 'checked' : ''}> Conductor Mode</label></div>
      <div class="form-row"><label class="cb-row"><input type="checkbox" id="f-announce" ${dlc?.announcements ? 'checked' : ''}> Announcements</label></div>
      <div class="form-row"><label class="cb-row"><input type="checkbox" id="f-faults" ${dlc?.train_faults ? 'checked' : ''}> Train Faults</label></div>
      <div class="form-row"><label class="cb-row"><input type="checkbox" id="f-gen8" ${dlc?.gen8_compatible ? 'checked' : ''}> Gen 8 Compatible</label></div>
      <div class="form-row"><label class="cb-row"><input type="checkbox" id="f-lighting" ${dlc?.new_lighting ? 'checked' : ''}> New Lighting</label></div>
      <div class="form-row"><label class="cb-row"><input type="checkbox" id="f-shadows" ${dlc?.new_track_shadows ? 'checked' : ''}> New Track Shadows</label></div>
      <div class="form-row"><label class="cb-row"><input type="checkbox" id="f-hopping" ${dlc?.route_hopping ? 'checked' : ''}> Route Hopping</label></div>
    </div>

    ${dlc?.requirements_raw ? `<div class="section-label">Requirements</div><div class="layer-text">${esc(dlc.requirements_raw)}</div>` : ''}
    ${dlc?.expansions_raw ? `<div class="section-label">Expansions</div><div class="layer-text">${esc(dlc.expansions_raw)}</div>` : ''}
    ${dlc?.platform_differences ? `<div class="section-label">Platform Differences</div><div class="layer-text">${esc(dlc.platform_differences)}</div>` : ''}
  </div>`;
}

async function saveDlc(existingDlc) {
  const name = document.getElementById('f-name').value.trim();
  if (!name) { alert('Content Name is required'); return; }

  const body = {
    content_name: name,
    steam_name: document.getElementById('f-steam-name').value.trim() || null,
    short_name: document.getElementById('f-short').value.trim() || null,
    acronym: document.getElementById('f-acronym').value.trim() || null,
    developer: document.getElementById('f-dev').value.trim() || null,
    dlc_type_id: document.getElementById('f-type').value || null,
    release_date: fromDateInput(document.getElementById('f-date').value) || null,
    country_id: document.getElementById('f-country').value || null,
    conductor_mode: document.getElementById('f-conductor').checked ? 1 : 0,
    announcements: document.getElementById('f-announce').checked ? 1 : 0,
    train_faults: document.getElementById('f-faults').checked ? 1 : 0,
    gen8_compatible: document.getElementById('f-gen8').checked ? 1 : 0,
    new_lighting: document.getElementById('f-lighting').checked ? 1 : 0,
    new_track_shadows: document.getElementById('f-shadows').checked ? 1 : 0,
    route_hopping: document.getElementById('f-hopping').checked ? 1 : 0,
  };
  body.tsw_versions = [...document.querySelectorAll('[name=tsw_ver]:checked')].map(i => parseInt(i.value));

  let id = currentDlcId;
  if (id) {
    await PUT(`/dlc/${id}`, body);
  } else {
    const res = await POST('/dlc', body);
    id = res.id;
    currentDlcId = id;
    history.replaceState({}, '', `/dlc/${id}`);
  }

  const updated = await GET(`/dlc/${id}`);
  document.getElementById('detail-title').textContent = updated.content_name;
  document.getElementById('detail-meta').innerHTML = buildMeta(updated);

  const btn = document.getElementById('btn-save');
  btn.textContent = 'Saved!';
  setTimeout(() => { btn.textContent = 'Save Changes'; }, 1500);
}

// ─── Base Locos panel ────────────────────────────────────────────────────────

function buildTrainsPanel(dlc) {
  const list = (dlc?.trains_included || []).map(t => `
    <div class="item-row">
      <div class="item-body"><a class="item-title table-link" data-href="/trains/${t.id}">${esc(t.name)}</a></div>
      <div class="item-actions">
        <button class="icon-btn btn-rm-train" data-tid="${t.id}">\u2715</button>
      </div>
    </div>`).join('') || `<p class="empty">No base locos</p>`;

  return `
    <div class="section-header"><h3>Base Locos</h3></div>
    <div id="trains-list">${list}</div>
    <div class="add-row">
      <div class="typeahead-wrap">
        <input type="text" id="train-add-input" placeholder="Search trains\u2026" autocomplete="off">
        <input type="hidden" id="train-add-sel">
        <ul class="typeahead-list hidden" id="train-add-list"></ul>
      </div>
      <button class="btn btn-primary" id="btn-add-train-dlc">Add</button>
    </div>
    <div class="add-row" style="margin-top:32px">
      <input type="text" id="train-new-input" placeholder="Create new train\u2026" style="flex:1">
      <button class="btn btn-ghost" id="btn-create-train">Create &amp; Add</button>
    </div>`;
}

function wireTrainsPanel(dlc) {
  document.querySelectorAll('.btn-rm-train').forEach(b => {
    b.onclick = async () => {
      const updated = await GET(`/dlc/${currentDlcId}`);
      const ids = updated.trains_included.filter(t => t.id != b.dataset.tid).map(t => t.id);
      await PUT(`/dlc/${currentDlcId}/trains`, { train_ids: ids });
      refreshTrains();
    };
  });

  const taInput = document.getElementById('train-add-input');
  const taHidden = document.getElementById('train-add-sel');
  const taList = document.getElementById('train-add-list');
  if (!taInput) return;

  taInput.addEventListener('input', () => {
    const q = taInput.value.trim().toLowerCase();
    taHidden.value = '';
    if (!q) { taList.classList.add('hidden'); return; }
    const matches = allTrains.filter(t => t.name.toLowerCase().includes(q)).slice(0, 12);
    taList.innerHTML = matches.map(t =>
      `<li class="typeahead-item" data-id="${t.id}" data-name="${esc(t.name)}">${esc(t.name)}</li>`
    ).join('');
    taList.classList.toggle('hidden', !matches.length);
  });

  taList.addEventListener('mousedown', e => {
    const item = e.target.closest('.typeahead-item');
    if (!item) return;
    taInput.value = item.dataset.name;
    taHidden.value = item.dataset.id;
    taList.classList.add('hidden');
  });

  taInput.addEventListener('blur', () => setTimeout(() => taList.classList.add('hidden'), 150));

  document.getElementById('btn-add-train-dlc')?.addEventListener('click', async () => {
    if (!taHidden.value || !currentDlcId) return;
    const updated = await GET(`/dlc/${currentDlcId}`);
    const ids = [...new Set([...updated.trains_included.map(t => String(t.id)), taHidden.value])];
    await PUT(`/dlc/${currentDlcId}/trains`, { train_ids: ids });
    taInput.value = '';
    taHidden.value = '';
    refreshTrains();
  });

  document.getElementById('btn-create-train')?.addEventListener('click', async () => {
    const input = document.getElementById('train-new-input');
    const name = input.value.trim();
    if (!name || !currentDlcId) return;
    const { id: newId } = await POST('/trains', { name });
    const updated = await GET(`/dlc/${currentDlcId}`);
    const ids = [...new Set([...updated.trains_included.map(t => String(t.id)), String(newId)])];
    await PUT(`/dlc/${currentDlcId}/trains`, { train_ids: ids });
    input.value = '';
    await loadLookups();
    refreshTrains();
  });
}

async function refreshTrains() {
  const dlc = await GET(`/dlc/${currentDlcId}`);
  document.getElementById('trains-list').innerHTML = (dlc.trains_included || []).map(t => `
    <div class="item-row">
      <div class="item-body"><a class="item-title table-link" data-href="/trains/${t.id}">${esc(t.name)}</a></div>
      <div class="item-actions"><button class="icon-btn btn-rm-train" data-tid="${t.id}">\u2715</button></div>
    </div>`).join('') || `<p class="empty">No base locos</p>`;
  wireTrainsPanel(dlc);
  wireAllLinks();
}

// ─── Layer / AI Layer / Substitution tabs ────────────────────────────────────

function steamBtn(l) {
  const parts = [];
  if (l.needed_dlc_price) {
    if (l.needed_dlc_price_discount) {
      parts.push(`<span style="font-size:11px;text-decoration:line-through;color:var(--text-dim)">${fmtPrice(l.needed_dlc_price_original)}</span>`);
    }
    parts.push(`<span style="font-size:11px;font-weight:600;color:var(--text)">${fmtPrice(l.needed_dlc_price)}</span>`);
    if (l.needed_dlc_price_discount) {
      parts.push(`<span style="font-size:11px;color:var(--success);font-weight:600">-${esc(l.needed_dlc_price_discount)}</span>`);
    }
  }
  if (l.needed_dlc_steam_url) {
    parts.push(`<a class="req-pc-link pl-pc" href="${esc(l.needed_dlc_steam_url)}" target="_blank" rel="noopener">Steam</a>`);
  } else if (l.needed_dlc_id) {
    parts.push(`<a class="req-link" data-href="/dlc/${l.needed_dlc_id}" style="font-size:12px">View</a>`);
  }
  return parts.length ? `<div style="display:flex;align-items:center;gap:6px;flex-wrap:nowrap">${parts.join('')}</div>` : '\u2014';
}

function dlcNeededCell(l) {
  const name = l.needed_dlc ? esc(l.needed_dlc) : '\u2014';
  if (l.needed_dlc_id) {
    return `<a class="req-link" data-href="/dlc/${l.needed_dlc_id}">${name}</a>`;
  }
  return name;
}

function buildLayerTab(dlc, type) {
  if (!dlc) return '<p class="empty">Save the DLC first</p>';

  let onRoute, providing, headers, rowFn;

  if (type === 'layers') {
    onRoute = dlc.layers_on_route || [];
    providing = dlc.layers_providing || [];
    headers = '<th>Locomotive</th><th>DLC Needed</th><th>Service Type</th><th>#</th><th>Buy</th>';
    rowFn = (l) => `<tr>
      <td class="td-primary">${esc(l.locomotive)}</td>
      <td class="td-sub">${dlcNeededCell(l)}</td>
      <td class="td-sub">${esc(l.service_type || '\u2014')}</td>
      <td class="td-sub">${l.num_services != null ? l.num_services : '\u2014'}</td>
      <td>${steamBtn(l)}</td>
    </tr>`;
  } else if (type === 'ai_layers') {
    onRoute = dlc.ai_layers_on_route || [];
    providing = dlc.ai_layers_providing || [];
    headers = '<th>Locomotive</th><th>DLC Needed</th><th>Service Type</th><th>Buy</th>';
    rowFn = (l) => `<tr>
      <td class="td-primary">${esc(l.locomotive)}</td>
      <td class="td-sub">${dlcNeededCell(l)}</td>
      <td class="td-sub">${esc(l.service_type || '\u2014')}</td>
      <td>${steamBtn(l)}</td>
    </tr>`;
  } else {
    onRoute = dlc.substitutions_on_route || [];
    providing = dlc.substitutions_providing || [];
    headers = '<th>Locomotive</th><th>DLC Needed</th><th>Replaces</th><th>Buy</th>';
    rowFn = (l) => `<tr>
      <td class="td-primary">${esc(l.locomotive)}</td>
      <td class="td-sub">${dlcNeededCell(l)}</td>
      <td class="td-sub">${esc(l.replaced_locomotive || '\u2014')}</td>
      <td>${steamBtn(l)}</td>
    </tr>`;
  }

  const typeLabels = { layers: 'Layers', ai_layers: 'AI Layers', substitutions: 'Substitutions' };
  const typeDescs = {
    layers: 'A route layer makes it possible to use a locomotive from another DLC by adding new services to the current route.',
    ai_layers: 'Some layers are not playable and are used for AI scenery only.',
    substitutions: 'A substitution makes it possible to replace an existing locomotive with another to run on an existing service.',
  };
  const label = typeLabels[type];
  const descHtml = `<p style="color:var(--text-sub);font-size:13px;margin-bottom:16px">${typeDescs[type]}</p>`;

  // On this route (if this DLC is a route)
  const onRouteHtml = onRoute.length
    ? `<div class="req-section">
        <div class="req-section-title">${label} on this route <span class="req-count">${onRoute.length}</span></div>
        <div class="table-card"><div class="table-scroll"><table>
          <thead><tr>${headers}</tr></thead>
          <tbody>${onRoute.map(rowFn).join('')}</tbody>
        </table></div></div>
      </div>`
    : '';

  // Providing to other routes (if this DLC's trains appear elsewhere)
  const providingHtml = providing.length
    ? `<div class="req-section" style="margin-top:24px">
        <div class="req-section-title">Your trains on other routes <span class="req-count">${providing.length}</span></div>
        <div class="table-card"><div class="table-scroll"><table>
          <thead><tr><th>Route</th>${headers}</tr></thead>
          <tbody>${providing.map(l => {
            const routeLink = l.route_id
              ? `<td class="td-primary"><a class="req-link" data-href="/dlc/${l.route_id}">${esc(l.route_name)}</a></td>`
              : `<td class="td-sub">\u2014</td>`;
            // Strip the first <tr> and </tr>, inject route cell
            const inner = rowFn(l).replace(/^<tr>/, '').replace(/<\/tr>$/, '');
            return `<tr>${routeLink}${inner}</tr>`;
          }).join('')}</tbody>
        </table></div></div>
      </div>`
    : '';

  if (!onRouteHtml && !providingHtml) {
    return descHtml + `<p class="empty">No ${label.toLowerCase()} data for this DLC</p>`;
  }

  return descHtml + onRouteHtml + providingHtml;
}

// ─── Documents panel ─────────────────────────────────────────────────────────

function buildDocsPanel(dlc) {
  const docs = (dlc?.documents || []).map(docRow).join('') || `<p class="empty">No documents added</p>`;
  return `
    <div class="section-header">
      <h3>Document Links</h3>
      <button class="btn btn-primary" id="btn-add-doc" style="font-size:12px;padding:4px 12px">+ Add</button>
    </div>
    <div id="docs-list">${docs}</div>`;
}

function docRow(doc) {
  return `
  <div class="item-row">
    <span class="doc-badge doc-${doc.doc_type}">${doc.doc_type}</span>
    <div class="item-body">
      <div class="item-title">
        ${doc.url ? `<a href="${esc(doc.url)}" target="_blank" rel="noopener">${esc(doc.label || '\u2014')}</a>` : esc(doc.label || '\u2014')}
      </div>
    </div>
    <div class="item-actions">
      <button class="icon-btn btn-edit-doc" data-did="${doc.id}">\u270F\uFE0F</button>
      <button class="icon-btn btn-del-doc" data-did="${doc.id}">\u{1F5D1}\uFE0F</button>
    </div>
  </div>`;
}

function docForm(doc) {
  return `
    <div class="form-row"><label>Type *</label>
      <select name="dtype">
        <option value="manual" ${doc?.doc_type === 'manual' ? 'selected' : ''}>Manual</option>
        <option value="timetable" ${doc?.doc_type === 'timetable' ? 'selected' : ''}>Timetable</option>
        <option value="guide" ${doc?.doc_type === 'guide' ? 'selected' : ''}>Collectable Guide</option>
        <option value="gameplay_guide" ${doc?.doc_type === 'gameplay_guide' ? 'selected' : ''}>Gameplay Guide</option>
      </select></div>
    <div class="form-row"><label>Label</label><input name="dlabel" value="${esc(doc?.label || '')}"></div>
    <div class="form-row"><label>URL</label><input name="durl" type="url" value="${esc(doc?.url || '')}"></div>`;
}

function wireDocsPanel(dlc) {
  document.getElementById('btn-add-doc')?.addEventListener('click', () => {
    if (!currentDlcId) return;
    openModal('Add Document', docForm(null), async body => {
      await POST(`/dlc/${currentDlcId}/documents`, {
        doc_type: body.querySelector('[name=dtype]').value,
        label: body.querySelector('[name=dlabel]').value || null,
        url: body.querySelector('[name=durl]').value || null,
      });
      refreshDocs();
    });
  });

  document.getElementById('docs-list')?.addEventListener('click', async e => {
    const editBtn = e.target.closest('.btn-edit-doc');
    const delBtn = e.target.closest('.btn-del-doc');
    if (editBtn) {
      const did = editBtn.dataset.did;
      const dlc2 = await GET(`/dlc/${currentDlcId}`);
      const doc = dlc2.documents.find(d => d.id == did);
      openModal('Edit Document', docForm(doc), async body => {
        await PUT(`/documents/${did}`, {
          doc_type: body.querySelector('[name=dtype]').value,
          label: body.querySelector('[name=dlabel]').value || null,
          url: body.querySelector('[name=durl]').value || null,
        });
        refreshDocs();
      });
    }
    if (delBtn) confirmDelete('document', delBtn.dataset.did, async id => { await DEL(`/documents/${id}`); refreshDocs(); });
  });
}

async function refreshDocs() {
  const dlc = await GET(`/dlc/${currentDlcId}`);
  document.getElementById('docs-list').innerHTML = (dlc.documents || []).map(docRow).join('') || `<p class="empty">No documents added</p>`;
  wireDocsPanel(dlc);
}

// ─── Purchase Links panel ────────────────────────────────────────────────────

function buildStorePanel(dlc) {
  const links = (dlc?.store_links || []).map(storeRow).join('') || `<p class="empty">No purchase links</p>`;
  return `
    <div class="section-header">
      <h3>Purchase Links</h3>
      <button class="btn btn-primary" id="btn-add-store" style="font-size:12px;padding:4px 12px">+ Add</button>
    </div>
    <div id="store-list">${links}</div>`;
}

function storeRow(sl) {
  return `
  <div class="store-row">
    <span class="store-platform">${esc(sl.console_platform)}</span>
    <span class="store-size">${sl.size_gb ? sl.size_gb + ' GB' : ''}</span>
    <span class="store-url">${sl.store_url ? `<a href="${esc(sl.store_url)}" target="_blank">${esc(sl.store_url)}</a>` : '<span style="color:var(--text-dim)">No link</span>'}</span>
    <button class="icon-btn btn-edit-sl" data-slid="${sl.id}">\u270F\uFE0F</button>
    <button class="icon-btn btn-del-sl" data-slid="${sl.id}">\u{1F5D1}\uFE0F</button>
  </div>`;
}

function storeForm(sl) {
  return `
    <div class="form-row"><label>Platform *</label>
      <select name="splat">
        <option ${sl?.console_platform === 'PC' ? 'selected' : ''}>PC</option>
        <option ${sl?.console_platform === 'PS5' ? 'selected' : ''}>PS5</option>
        <option ${sl?.console_platform === 'Xbox S/X' ? 'selected' : ''}>Xbox S/X</option>
      </select></div>
    <div class="form-row"><label>Size (GB)</label><input name="sgb" value="${esc(sl?.size_gb || '')}"></div>
    <div class="form-row"><label>Store URL</label><input name="surl" type="url" value="${esc(sl?.store_url || '')}"></div>`;
}

function wireStorePanel(dlc) {
  document.getElementById('btn-add-store')?.addEventListener('click', () => {
    if (!currentDlcId) return;
    openModal('Add Store Link', storeForm(null), async body => {
      await POST(`/dlc/${currentDlcId}/store-links`, {
        console_platform: body.querySelector('[name=splat]').value,
        size_gb: body.querySelector('[name=sgb]').value || null,
        store_url: body.querySelector('[name=surl]').value || null,
      });
      refreshStore();
    });
  });

  document.getElementById('store-list')?.addEventListener('click', async e => {
    const editBtn = e.target.closest('.btn-edit-sl');
    const delBtn = e.target.closest('.btn-del-sl');
    if (editBtn) {
      const slid = editBtn.dataset.slid;
      const dlc2 = await GET(`/dlc/${currentDlcId}`);
      const sl = dlc2.store_links.find(s => s.id == slid);
      openModal('Edit Store Link', storeForm(sl), async body => {
        await PUT(`/store-links/${slid}`, {
          console_platform: body.querySelector('[name=splat]').value,
          size_gb: body.querySelector('[name=sgb]').value || null,
          store_url: body.querySelector('[name=surl]').value || null,
        });
        refreshStore();
      });
    }
    if (delBtn) confirmDelete('store link', delBtn.dataset.slid, async id => { await DEL(`/store-links/${id}`); refreshStore(); });
  });
}

async function refreshStore() {
  const dlc = await GET(`/dlc/${currentDlcId}`);
  document.getElementById('store-list').innerHTML = (dlc.store_links || []).map(storeRow).join('') || `<p class="empty">No purchase links</p>`;
  wireStorePanel(dlc);
}

// ─── Trains page ─────────────────────────────────────────────────────────────

let allTrainsData = [];
let trainSearch = '';

async function renderTrains() {
  allTrainsData = await GET('/trains');
  root.innerHTML = `
    <div class="page-header">
      <h1 class="page-title">Trains</h1>
      <button class="btn btn-primary" id="btn-add-train">+ Add Train</button>
    </div>
    <div class="filter-bar">
      <input id="train-search" type="search" placeholder="Search trains\u2026">
      <button class="btn btn-ghost" id="btn-clear-train-search">\u2715 Clear</button>
    </div>
    <div class="table-card">
      <div class="table-scroll">
        <table>
          <thead><tr><th style="width:1%">#</th><th>Name</th><th style="width:1%"></th></tr></thead>
          <tbody id="trains-tbody"></tbody>
        </table>
      </div>
    </div>`;

  renderTrainsTable();

  document.getElementById('btn-add-train').onclick = () => {
    openModal('Add Train', `<div class="form-row"><label>Name</label><input name="val"></div>`, async body => {
      await POST('/trains', { name: body.querySelector('[name=val]').value });
      allTrainsData = await GET('/trains');
      renderTrainsTable();
      loadLookups();
    });
  };

  document.getElementById('btn-clear-train-search').onclick = () => {
    trainSearch = '';
    document.getElementById('train-search').value = '';
    renderTrainsTable();
  };

  let timer;
  document.getElementById('train-search').addEventListener('input', e => {
    clearTimeout(timer);
    timer = setTimeout(() => { trainSearch = e.target.value; renderTrainsTable(); }, 150);
  });
}

function renderTrainsTable() {
  const filtered = (trainSearch
    ? allTrainsData.filter(t => t.name.toLowerCase().includes(trainSearch.toLowerCase()))
    : [...allTrainsData]
  ).sort((a, b) => a.name.localeCompare(b.name));
  document.getElementById('trains-tbody').innerHTML = filtered.map((t, i) => `
    <tr data-href="/trains/${t.id}">
      <td class="td-sub" style="white-space:nowrap">${i + 1}</td>
      <td class="td-primary"><a class="table-link" data-href="/trains/${t.id}">${esc(t.name)}</a></td>
      <td class="td-actions">
        <button class="pill pill-blue btn-edit-train" data-id="${t.id}" data-name="${esc(t.name)}">Edit</button>
        <button class="pill pill-red btn-del-train" data-id="${t.id}">Delete</button>
      </td>
    </tr>`).join('') || '<tr><td colspan="3" class="empty">No trains found</td></tr>';

  document.querySelectorAll('#trains-tbody tr[data-href]').forEach(tr => {
    tr.addEventListener('click', e => {
      if (e.target.closest('button') || e.target.closest('a')) return;
      navigate(tr.dataset.href);
    });
  });
  document.querySelectorAll('.table-link').forEach(a => {
    a.onclick = e => { e.preventDefault(); navigate(a.dataset.href); };
  });
  document.querySelectorAll('.btn-edit-train').forEach(b => b.onclick = () => {
    openModal('Edit Train', `<div class="form-row"><label>Name</label><input name="val" value="${esc(b.dataset.name)}"></div>`, async body => {
      await PUT(`/trains/${b.dataset.id}`, { name: body.querySelector('[name=val]').value });
      allTrainsData = await GET('/trains');
      renderTrainsTable();
      loadLookups();
    });
  });
  document.querySelectorAll('.btn-del-train').forEach(b => b.onclick = () => {
    confirmDelete('train', b.dataset.id, async id => {
      await DEL(`/trains/${id}`);
      allTrainsData = await GET('/trains');
      renderTrainsTable();
      loadLookups();
    });
  });
}

// ─── Train detail page ───────────────────────────────────────────────────────

async function renderTrainDetail(id) {
  root.innerHTML = `<p class="empty" style="padding-top:60px">Loading\u2026</p>`;
  const data = await GET(`/trains/${id}`);
  const { train, purchase_dlcs, layer_routes, ai_layer_routes, sub_routes, store_links } = data;

  function priceHtml(p) {
    if (!p || !p.price) return '';
    if (p.price_discount) {
      return `<span style="text-decoration:line-through;color:var(--text-dim);font-size:11px">${fmtPrice(p.price_original)}</span>
        <span style="font-weight:600;font-size:12px">${fmtPrice(p.price)}</span>
        <span style="color:var(--success);font-weight:600;font-size:11px">-${esc(p.price_discount)}</span>`;
    }
    return `<span style="font-size:12px">${fmtPrice(p.price)}</span>`;
  }

  function storeLinksHtml(dlcId) {
    const sl = store_links[dlcId];
    const p = data.prices[dlcId];
    const parts = [];
    if (p) parts.push(priceHtml(p));
    if (sl) {
      if (sl['PC']) parts.push(`<a class="req-pc-link pl-pc" href="${esc(sl['PC'])}" target="_blank">Steam</a>`);
      if (sl['PS5']) parts.push(`<a class="req-pc-link pl-ps5" href="${esc(sl['PS5'])}" target="_blank">PS5</a>`);
      if (sl['Xbox S/X']) parts.push(`<a class="req-pc-link pl-xbox" href="${esc(sl['Xbox S/X'])}" target="_blank">Xbox</a>`);
    }
    return parts.length ? `<div style="display:flex;align-items:center;gap:6px;flex-wrap:nowrap">${parts.join('')}</div>` : '\u2014';
  }

  const purchaseRows = purchase_dlcs.length
    ? purchase_dlcs.map(d => {
        const priceParts = [];
        if (d.price) {
          if (d.price_discount) {
            priceParts.push(`<span style="text-decoration:line-through;color:var(--text-dim);font-size:11px">${fmtPrice(d.price_original)}</span>`);
          }
          priceParts.push(`<span style="font-weight:600;font-size:12px">${fmtPrice(d.price)}</span>`);
          if (d.price_discount) {
            priceParts.push(`<span style="color:var(--success);font-weight:600;font-size:11px">-${esc(d.price_discount)}</span>`);
          }
        }
        const pcUrl = d.store_links?.['PC'];
        const steamBtn = pcUrl
          ? `<a class="req-pc-link pl-pc" href="${esc(pcUrl)}" target="_blank">Steam</a>`
          : '';
        const buyCell = [...priceParts, steamBtn].filter(Boolean);
        return `<tr>
        <td class="td-primary"><a class="table-link" data-href="/dlc/${d.id}">${esc(d.content_name)}</a></td>
        <td class="td-sub">${typeTag(d.dlc_type_name)}</td>
        <td>${buyCell.length ? `<div style="display:flex;align-items:center;gap:6px;flex-wrap:nowrap">${buyCell.join('')}</div>` : '\u2014'}</td>
      </tr>`;
      }).join('')
    : `<tr><td colspan="3" class="empty">Not found in any DLC</td></tr>`;

  function layerBuyCell(r) {
    const parts = [];
    if (r.needed_dlc_price) {
      if (r.needed_dlc_price_discount) {
        parts.push(`<span style="text-decoration:line-through;color:var(--text-dim);font-size:11px">${fmtPrice(r.needed_dlc_price_original)}</span>`);
      }
      parts.push(`<span style="font-weight:600;font-size:12px">${fmtPrice(r.needed_dlc_price)}</span>`);
      if (r.needed_dlc_price_discount) {
        parts.push(`<span style="color:var(--success);font-weight:600;font-size:11px">-${esc(r.needed_dlc_price_discount)}</span>`);
      }
    }
    if (r.needed_dlc_steam_url) {
      parts.push(`<a class="req-pc-link pl-pc" href="${esc(r.needed_dlc_steam_url)}" target="_blank">Steam</a>`);
    }
    return parts.length ? `<div style="display:flex;align-items:center;gap:6px;flex-wrap:nowrap">${parts.join('')}</div>` : '\u2014';
  }

  function layerTable(rows, extraHeader, extraCell) {
    if (!rows.length) return '';
    return `<div class="table-card" style="margin-top:12px"><div class="table-scroll"><table>
      <thead><tr><th>Route</th>${extraHeader}<th>DLC Needed</th><th>Buy</th></tr></thead>
      <tbody>${rows.map(r => `<tr>
        <td class="td-primary"><a class="table-link" data-href="/dlc/${r.route_id}">${esc(r.route_name)}</a></td>
        ${extraCell(r)}
        <td class="td-sub">${esc(r.needed_dlc || '\u2014')}</td>
        <td>${layerBuyCell(r)}</td>
      </tr>`).join('')}</tbody>
    </table></div></div>`;
  }

  const layerHtml = layerTable(layer_routes, '<th>Service</th><th>#</th>',
    r => `<td class="td-sub">${esc(r.service_type || '\u2014')}</td><td class="td-sub">${r.num_services != null ? r.num_services : '\u2014'}</td>`);

  const aiHtml = layerTable(ai_layer_routes, '<th>Service</th>',
    r => `<td class="td-sub">${esc(r.service_type || '\u2014')}</td>`);

  const subHtml = layerTable(sub_routes, '<th>Replaces</th>',
    r => `<td class="td-sub">${esc(r.replaced_locomotive || '\u2014')}</td>`);

  root.innerHTML = `
    <div class="detail-header" style="margin-bottom:0">
      <button class="detail-back" id="btn-back">\u2190 Back to Trains</button>
      <div class="detail-title-row">
        <div class="detail-title">${esc(train.name)}</div>
      </div>
    </div>

    <div style="padding:24px;max-width:960px">
      <div class="req-section-title">Purchase DLCs <span class="req-count">${purchase_dlcs.length}</span></div>
      <div class="table-card">
        <div class="table-scroll"><table>
          <thead><tr><th>DLC</th><th>Type</th><th style="width:18%">Purchase</th></tr></thead>
          <tbody>${purchaseRows}</tbody>
        </table></div>
      </div>

      ${layer_routes.length ? `<div class="req-section-title" style="margin-top:24px">Playable Layers <span class="req-count">${layer_routes.length}</span></div>${layerHtml}` : ''}
      ${ai_layer_routes.length ? `<div class="req-section-title" style="margin-top:24px">AI Layers <span class="req-count">${ai_layer_routes.length}</span></div>${aiHtml}` : ''}
      ${sub_routes.length ? `<div class="req-section-title" style="margin-top:24px">Substitutions <span class="req-count">${sub_routes.length}</span></div>${subHtml}` : ''}
    </div>`;

  document.getElementById('btn-back').onclick = () => navigate('/trains');
  root.querySelectorAll('.table-link').forEach(a => {
    a.onclick = e => { e.preventDefault(); navigate(a.dataset.href); };
  });
}

// ─── Admin page ──────────────────────────────────────────────────────────────

async function renderAdmin(tab) {
  root.innerHTML = `
    <div class="page-header"><h1 class="page-title">Admin</h1></div>
    <div class="admin-tabs">
      <a class="admin-tab-btn ${tab === 'countries' ? 'active' : ''}" href="/admin/countries" data-route="/admin/countries">Countries</a>
      <a class="admin-tab-btn ${tab === 'tsw-version' ? 'active' : ''}" href="/admin/tsw-version" data-route="/admin/tsw-version">TSW Version</a>
      <a class="admin-tab-btn ${tab === 'dlc-types' ? 'active' : ''}" href="/admin/dlc-types" data-route="/admin/dlc-types">DLC Types</a>
      <a class="admin-tab-btn ${tab === 'prices' ? 'active' : ''}" href="/admin/prices" data-route="/admin/prices">Prices</a>
      <a class="admin-tab-btn ${tab === 'errors' ? 'active' : ''}" href="/admin/errors" data-route="/admin/errors">Errors Log</a>
    </div>
    <div id="admin-countries" class="admin-panel ${tab === 'countries' ? 'active' : ''}"></div>
    <div id="admin-tsw" class="admin-panel ${tab === 'tsw-version' ? 'active' : ''}"></div>
    <div id="admin-dlctypes" class="admin-panel ${tab === 'dlc-types' ? 'active' : ''}"></div>
    <div id="admin-prices" class="admin-panel ${tab === 'prices' ? 'active' : ''}"></div>
    <div id="admin-errors" class="admin-panel ${tab === 'errors' ? 'active' : ''}"></div>`;

  document.querySelectorAll('.admin-tab-btn').forEach(a => {
    a.addEventListener('click', e => {
      e.preventDefault();
      const r = a.dataset.route;
      history.pushState({}, '', r);
      const newTab = r.split('/')[2] || 'countries';
      document.querySelectorAll('.admin-tab-btn').forEach(x => x.classList.remove('active'));
      a.classList.add('active');
      document.querySelectorAll('.admin-panel').forEach(p => p.classList.remove('active'));
      const panelMap = { countries: 'admin-countries', 'tsw-version': 'admin-tsw', 'dlc-types': 'admin-dlctypes', prices: 'admin-prices', errors: 'admin-errors' };
      document.getElementById(panelMap[newTab])?.classList.add('active');
      loadAdminPanel(newTab);
    });
  });

  loadAdminPanel(tab);
}

async function loadAdminPanel(tab) {
  if (tab === 'countries') await loadCountriesPanel();
  if (tab === 'tsw-version') await loadTswPanel();
  if (tab === 'dlc-types') await loadDlcTypesPanel();
  if (tab === 'prices') await loadPricesPanel();
  if (tab === 'errors') await loadErrorsPanel();
}

async function loadCountriesPanel() {
  const rows = await GET('/countries');
  rows.sort((a, b) => {
    const af = !!countryCode(a.name), bf = !!countryCode(b.name);
    if (af !== bf) return af ? -1 : 1;
    return a.name.localeCompare(b.name);
  });

  const panel = document.getElementById('admin-countries');
  panel.innerHTML = `
    <div class="admin-panel-header">
      <button class="btn btn-primary" id="btn-add-country">+ Add Country</button>
    </div>
    <div class="table-card"><div class="table-scroll"><table>
      <thead><tr><th>Name</th><th>DLC Count</th><th style="width:1%"></th></tr></thead>
      <tbody>
        ${rows.map(c => `<tr>
          <td class="td-primary">${countryLabel(c.name)}</td>
          <td class="td-sub">${c.count || 0}</td>
          <td class="td-actions">
            <button class="pill pill-blue btn-edit-c" data-id="${c.id}" data-name="${esc(c.name)}">Edit</button>
            <button class="pill pill-red btn-del-c" data-id="${c.id}">Delete</button>
          </td>
        </tr>`).join('') || `<tr><td colspan="3" class="empty">No countries</td></tr>`}
      </tbody>
    </table></div></div>`;

  document.getElementById('btn-add-country').onclick = () => {
    openModal('Add Country', `<div class="form-row"><label>Name</label><input name="val"></div>`, async body => {
      await POST('/countries', { name: body.querySelector('[name=val]').value });
      loadCountriesPanel(); loadLookups();
    });
  };
  document.querySelectorAll('.btn-edit-c').forEach(b => b.onclick = () => {
    openModal('Edit Country', `<div class="form-row"><label>Name</label><input name="val" value="${esc(b.dataset.name)}"></div>`, async body => {
      await PUT(`/countries/${b.dataset.id}`, { name: body.querySelector('[name=val]').value });
      loadCountriesPanel(); loadLookups();
    });
  });
  document.querySelectorAll('.btn-del-c').forEach(b => b.onclick = () => {
    confirmDelete('country', b.dataset.id, async id => { await DEL(`/countries/${id}`); loadCountriesPanel(); loadLookups(); });
  });
}

async function loadTswPanel() {
  const rows = await GET('/tsw-versions');
  document.getElementById('admin-tsw').innerHTML = `
    <div class="admin-panel-header">
      <span style="color:var(--text-dim);font-size:13px">Select which TSW version to filter by default on the DLC index</span>
    </div>
    <div class="table-card"><div class="table-scroll"><table>
      <thead><tr><th>Default</th><th>Name</th><th>DLC Count</th></tr></thead>
      <tbody>${rows.map(v => `<tr>
        <td><input type="radio" name="default_tsw" value="${v.id}" ${v.is_default ? 'checked' : ''} class="tsw-default-radio" style="cursor:pointer;accent-color:var(--accent)"></td>
        <td class="td-primary">${esc(v.name)}</td>
        <td class="td-sub">${v.count || 0}</td>
      </tr>`).join('')}</tbody>
    </table></div></div>`;

  document.querySelectorAll('.tsw-default-radio').forEach(radio => {
    radio.onchange = async () => {
      await PUT('/settings/default-tsw', { version: parseInt(radio.value) });
      await loadLookups();
    };
  });
}

async function loadDlcTypesPanel() {
  const rows = await GET('/dlc-types');
  document.getElementById('admin-dlctypes').innerHTML = `
    <div class="admin-panel-header">
      <button class="btn btn-primary" id="btn-add-dt">+ Add Type</button>
    </div>
    <div class="table-card"><div class="table-scroll"><table>
      <thead><tr><th>Name</th><th style="width:1%"></th></tr></thead>
      <tbody>${rows.map(t => `<tr>
        <td>${typeTag(t.name)}</td>
        <td class="td-actions">
          <button class="pill pill-blue btn-edit-dt" data-id="${t.id}" data-name="${esc(t.name)}">Edit</button>
          <button class="pill pill-red btn-del-dt" data-id="${t.id}">Delete</button>
        </td>
      </tr>`).join('') || `<tr><td colspan="2" class="empty">No types</td></tr>`}</tbody>
    </table></div></div>`;

  document.getElementById('btn-add-dt').onclick = () => {
    openModal('Add DLC Type', `<div class="form-row"><label>Name</label><input name="val"></div>`, async body => {
      await POST('/dlc-types', { name: body.querySelector('[name=val]').value });
      loadDlcTypesPanel(); loadLookups();
    });
  };
  document.querySelectorAll('.btn-edit-dt').forEach(b => b.onclick = () => {
    openModal('Edit DLC Type', `<div class="form-row"><label>Name</label><input name="val" value="${esc(b.dataset.name)}"></div>`, async body => {
      await PUT(`/dlc-types/${b.dataset.id}`, { name: body.querySelector('[name=val]').value });
      loadDlcTypesPanel(); loadLookups();
    });
  });
  document.querySelectorAll('.btn-del-dt').forEach(b => b.onclick = () => {
    confirmDelete('DLC type', b.dataset.id, async id => { await DEL(`/dlc-types/${id}`); loadDlcTypesPanel(); loadLookups(); });
  });
}

// ─── Prices panel ────────────────────────────────────────────────────────────

let pricePolling = null;

async function loadPricesPanel() {
  const panel = document.getElementById('admin-prices');
  const status = await GET('/prices/status');

  const lastUpdated = status.last_updated
    ? new Date(status.last_updated).toLocaleString()
    : 'Never';

  panel.innerHTML = `
    <div style="max-width:600px">
      <div class="section-label">Steam Price Fetcher</div>
      <p style="color:var(--text-sub);font-size:13px;margin-bottom:16px">
        Fetches current prices from Steam for all DLCs with a Steam link.
        This takes a few minutes as it paces requests to avoid rate limiting (~1.5s per DLC).
      </p>
      <div class="item-row" style="margin-bottom:16px">
        <div class="item-body">
          <div class="item-title">Status</div>
          <div class="item-sub">
            ${status.dlcs_with_price} / ${status.dlcs_with_steam} DLCs have prices
          </div>
          <div class="item-sub">Last updated: ${lastUpdated}</div>
        </div>
      </div>
      <div id="price-progress" style="display:${status.running ? 'block' : 'none'};margin-bottom:16px">
        <div style="background:var(--surface2);border:1px solid var(--border);border-radius:var(--r);overflow:hidden;height:24px;margin-bottom:8px">
          <div id="price-bar" style="background:var(--accent);height:100%;width:${status.running ? Math.round((status.current / status.total) * 100) : 0}%;transition:width 0.3s"></div>
        </div>
        <div id="price-status-text" class="td-sub" style="font-size:12px">
          ${status.running ? `${status.current} / ${status.total} (${status.updated} updated, ${status.errors} errors)` : ''}
        </div>
      </div>
      <div style="display:flex;gap:8px;align-items:center">
        <button class="btn btn-primary" id="btn-fetch-prices" ${status.running ? 'disabled' : ''}>
          ${status.running ? 'Fetching...' : status.dlcs_with_price > 0 ? 'Update Prices' : 'Fetch All Prices'}
        </button>
        <button class="btn btn-ghost btn-danger" id="btn-clear-price-errors" style="font-size:12px">Clear Errors</button>
      </div>
      <div id="price-errors" style="margin-top:20px">${buildPriceErrors(status.errorDetails || [])}</div>
    </div>`;

  document.getElementById('btn-fetch-prices').onclick = startPriceFetch;
  document.getElementById('btn-clear-price-errors').onclick = async () => {
    await DEL('/prices/errors');
    document.getElementById('price-errors').innerHTML = '';
  };
  wirePriceErrorLinks();

  if (status.running) startPolling();
}

function buildPriceErrors(errors) {
  if (!errors.length) return '';
  return `
    <div class="section-label" style="color:var(--danger)">Errors (${errors.length})</div>
    <div class="table-card"><div class="table-scroll"><table>
      <thead><tr><th>DLC</th><th>Steam Link</th><th>Error</th></tr></thead>
      <tbody>${errors.map(e => `<tr>
        <td class="td-primary"><a class="table-link price-err-link" data-href="/dlc/${e.id}">${esc(e.name)}</a></td>
        <td class="td-sub" style="max-width:200px;overflow:hidden;text-overflow:ellipsis;white-space:nowrap">
          ${e.steam_link ? `<a href="${esc(e.steam_link)}" target="_blank" style="color:var(--accent);font-size:12px">${esc(e.steam_link)}</a>` : 'No link'}
        </td>
        <td class="td-sub" style="color:var(--danger)">${esc(e.reason)}</td>
      </tr>`).join('')}</tbody>
    </table></div></div>`;
}

function wirePriceErrorLinks() {
  document.querySelectorAll('.price-err-link').forEach(a => {
    a.onclick = ev => { ev.preventDefault(); navigate(a.dataset.href); };
  });
}

async function startPriceFetch() {
  const btn = document.getElementById('btn-fetch-prices');
  btn.disabled = true;
  btn.textContent = 'Starting...';

  try {
    await POST('/prices/fetch', {});
    document.getElementById('price-progress').style.display = 'block';
    startPolling();
  } catch (e) {
    btn.disabled = false;
    btn.textContent = 'Fetch All Prices';
    alert('Failed to start: ' + e.message);
  }
}

function startPolling() {
  if (pricePolling) clearInterval(pricePolling);
  const btn = document.getElementById('btn-fetch-prices');
  if (btn) { btn.disabled = true; btn.textContent = 'Fetching...'; }

  pricePolling = setInterval(async () => {
    try {
      const status = await GET('/prices/status');
      const bar = document.getElementById('price-bar');
      const text = document.getElementById('price-status-text');
      const progress = document.getElementById('price-progress');

      if (bar && status.total > 0) {
        bar.style.width = Math.round((status.current / status.total) * 100) + '%';
      }
      if (text) {
        text.textContent = `${status.current} / ${status.total} (${status.updated} updated, ${status.errors} errors)`;
      }
      if (progress) progress.style.display = 'block';

      // Update error list in real-time
      const errEl = document.getElementById('price-errors');
      if (errEl && status.errorDetails && status.errorDetails.length) {
        errEl.innerHTML = buildPriceErrors(status.errorDetails);
        wirePriceErrorLinks();
      }

      if (!status.running) {
        clearInterval(pricePolling);
        pricePolling = null;
        if (btn) { btn.disabled = false; btn.textContent = 'Update Prices'; }
        if (text) text.textContent += ' \u2014 Done!';
        // Refresh the panel to update the last-updated time
        setTimeout(() => loadPricesPanel(), 1000);
      }
    } catch (e) {
      clearInterval(pricePolling);
      pricePolling = null;
    }
  }, 2000);
}

// ─── Errors Log panel ────────────────────────────────────────────────────────

async function loadErrorsPanel() {
  const panel = document.getElementById('admin-errors');
  const data = await GET('/prices/errors');
  const errors = data.errors || [];
  const lastFetched = data.last_fetched
    ? new Date(data.last_fetched).toLocaleString()
    : 'Never';

  panel.innerHTML = `
    <div style="max-width:900px">
      <div class="section-label">Price Fetch Errors</div>
      <p style="color:var(--text-sub);font-size:13px;margin-bottom:8px">
        Errors from the most recent price fetch run.
      </p>
      <div class="item-sub" style="margin-bottom:16px">Last fetch: ${lastFetched}</div>
      ${errors.length ? `
        <div style="margin-bottom:12px;display:flex;align-items:center;gap:12px">
          <span style="color:var(--danger);font-weight:600">${errors.length} error${errors.length !== 1 ? 's' : ''}</span>
          <button class="btn btn-ghost btn-danger" id="btn-clear-errors" style="font-size:12px">Clear All</button>
        </div>
        <div class="table-card"><div class="table-scroll"><table>
          <thead><tr><th>DLC</th><th>Steam Link</th><th>Error</th><th>Date</th></tr></thead>
          <tbody>${errors.map(e => `<tr>
            <td class="td-primary"><a class="table-link err-log-link" data-href="/dlc/${e.dlc_id}">${esc(e.dlc_name)}</a></td>
            <td class="td-sub" style="max-width:200px;overflow:hidden;text-overflow:ellipsis;white-space:nowrap">
              ${e.steam_link ? `<a href="${esc(e.steam_link)}" target="_blank" style="color:var(--accent);font-size:12px">${esc(e.steam_link)}</a>` : 'No link'}
            </td>
            <td class="td-sub" style="color:var(--danger)">${esc(e.reason)}</td>
            <td class="td-sub" style="white-space:nowrap;font-size:11px">${e.fetched_at ? new Date(e.fetched_at).toLocaleString() : ''}</td>
          </tr>`).join('')}</tbody>
        </table></div></div>
      ` : '<p class="empty">No errors recorded</p>'}
    </div>`;

  panel.querySelectorAll('.err-log-link').forEach(a => {
    a.onclick = ev => { ev.preventDefault(); navigate(a.dataset.href); };
  });

  const clearBtn = document.getElementById('btn-clear-errors');
  if (clearBtn) {
    clearBtn.onclick = async () => {
      await DEL('/prices/errors');
      loadErrorsPanel();
    };
  }
}

// ─── Simple modal ────────────────────────────────────────────────────────────

let modalCb = null;

function openModal(title, html, cb) {
  document.getElementById('modal-simple-title').textContent = title;
  document.getElementById('modal-simple-body').innerHTML = html;
  document.getElementById('modal-simple').classList.remove('hidden');
  modalCb = cb;
  setTimeout(() => document.querySelector('#modal-simple-body input, #modal-simple-body select')?.focus(), 40);
}

function closeModal() {
  document.getElementById('modal-simple').classList.add('hidden');
  modalCb = null;
}

document.getElementById('btn-simple-save').onclick = async () => {
  if (!modalCb) return;
  const btn = document.getElementById('btn-simple-save');
  btn.disabled = true;
  try {
    await modalCb(document.getElementById('modal-simple-body'));
    closeModal();
  } catch (e) {
    console.error('Save failed:', e);
    alert('Save failed: ' + e.message);
  } finally { btn.disabled = false; }
};
document.getElementById('btn-simple-cancel').onclick = closeModal;
document.querySelector('#modal-simple .modal-close').onclick = closeModal;
document.querySelector('#modal-simple .modal-overlay').onclick = closeModal;

// ─── Confirm modal ──────────────────────────────────────────────────────────

let confirmCb = null;

function confirmDelete(label, id, cb) {
  document.getElementById('modal-confirm-body').textContent = `Delete this ${label}? This cannot be undone.`;
  document.getElementById('modal-confirm').classList.remove('hidden');
  confirmCb = () => cb(id);
}

document.getElementById('btn-confirm-ok').onclick = async () => {
  document.getElementById('modal-confirm').classList.add('hidden');
  if (confirmCb) await confirmCb();
  confirmCb = null;
};
document.getElementById('btn-confirm-cancel').onclick = () => { document.getElementById('modal-confirm').classList.add('hidden'); confirmCb = null; };
document.querySelector('#modal-confirm .modal-close').onclick = () => { document.getElementById('modal-confirm').classList.add('hidden'); confirmCb = null; };
document.querySelector('#modal-confirm .modal-overlay').onclick = () => { document.getElementById('modal-confirm').classList.add('hidden'); confirmCb = null; };

// ─── Cart & Owned (DB-backed) ────────────────────────────────────────────────

async function updateCartBadge() {
  try {
    const items = await GET('/cart');
    const badge = document.getElementById('cart-badge');
    if (badge) {
      badge.textContent = items.length;
      badge.classList.toggle('hidden', items.length === 0);
    }
  } catch (e) {}
}

function cartBtn(dlc) {
  if (dlc.owned) {
    return `<span class="btn-cart" style="border-color:var(--text-dim);color:var(--text-dim);cursor:default;font-size:10px" title="Owned">\u2713</span>`;
  }
  const inCart = !!dlc.in_cart;
  return `<button class="btn-cart ${inCart ? 'in-cart' : ''}" data-cart-id="${dlc.id}"
    title="${inCart ? 'Remove from cart' : 'Add to cart'}"
    onclick="event.stopPropagation()">${inCart ? '\u2713' : '+'}</button>`;
}

function ownedBtn(dlc) {
  const owned = !!dlc.owned;
  return `<button class="btn-cart ${owned ? 'in-cart' : ''}" data-owned-id="${dlc.id}"
    title="${owned ? 'Mark as not owned' : 'Mark as owned'}"
    onclick="event.stopPropagation()" style="${owned ? 'border-color:var(--accent);color:var(--accent);background:rgba(59,130,246,0.1)' : ''}">${owned ? '\u2713' : '+'}</button>`;
}

function wireCartButtons() {
  document.querySelectorAll('.btn-cart[data-cart-id]').forEach(btn => {
    btn.onclick = async e => {
      e.stopPropagation();
      const id = btn.dataset.cartId;
      const inCart = btn.classList.contains('in-cart');
      await PUT(`/dlc/${id}/cart`, { in_cart: !inCart });
      btn.classList.toggle('in-cart');
      btn.textContent = inCart ? '+' : '\u2713';
      btn.title = inCart ? 'Add to cart' : 'Remove from cart';
      updateCartBadge();
    };
  });
}

function wireOwnedButtons() {
  document.querySelectorAll('.btn-cart[data-owned-id]').forEach(btn => {
    btn.onclick = async e => {
      e.stopPropagation();
      const id = btn.dataset.ownedId;
      const owned = btn.classList.contains('in-cart');
      await PUT(`/dlc/${id}/owned`, { owned: !owned });
      // Re-render the current page to reflect changes
      route(location.pathname);
    };
  });
}

function dlcPriceHtml(d) {
  if (!d.price) return '\u2014';
  if (d.price_discount) {
    return `<span style="text-decoration:line-through;color:var(--text-dim);font-size:11px">${fmtPrice(d.price_original)}</span>
      <span style="font-weight:600">${fmtPrice(d.price)}</span>
      <span style="color:var(--success);font-weight:600;font-size:11px">-${esc(d.price_discount)}</span>`;
  }
  return `<span style="font-weight:600">${fmtPrice(d.price)}</span>`;
}

async function renderCart() {
  const items = await GET('/cart');

  let total = 0;
  let totalOriginal = 0;
  let hasDiscount = false;
  items.forEach(d => {
    if (d.price_value) total += d.price_value;
    if (d.price_discount && d.price_original) {
      const origVal = parseFloat((d.price_original || '').replace(/[^0-9.]/g, ''));
      if (origVal) { totalOriginal += origVal; hasDiscount = true; }
      else totalOriginal += d.price_value || 0;
    } else {
      totalOriginal += d.price_value || 0;
    }
  });

  const currency = items.find(d => d.price)?.price?.match(/^([A-Z]{2,3}\$?|\$|\u00a3|\u20ac)/)?.[1] || '$';

  const itemRows = items.length
    ? items.map(d => {
        const steamHtml = d.purchase_url
          ? `<a class="req-pc-link pl-pc" href="${esc(d.purchase_url)}" target="_blank" rel="noopener">Steam</a>`
          : '';
        return `<tr>
          <td><button class="btn-cart in-cart btn-cart-remove" data-remove-id="${d.id}" title="Remove">\u2713</button></td>
          <td class="td-primary"><a class="table-link" data-href="/dlc/${d.id}">${esc(d.content_name)}</a></td>
          <td class="td-sub">${typeTag(d.dlc_type_name)}</td>
          <td style="white-space:nowrap">${dlcPriceHtml(d)}</td>
          <td style="white-space:nowrap">${steamHtml}</td>
        </tr>`;
      }).join('')
    : `<tr><td colspan="5" class="empty">Your cart is empty. Click + on any DLC to add it.</td></tr>`;

  const savingsHtml = hasDiscount && totalOriginal > total
    ? `<div style="font-size:13px;color:var(--success);margin-top:4px">You save: ${currency} ${(totalOriginal - total).toFixed(2)}</div>`
    : '';

  root.innerHTML = `
    <div class="page-header">
      <h1 class="page-title">Shopping Cart</h1>
      ${items.length ? `<button class="btn btn-ghost" id="btn-clear-cart">Clear Cart</button>` : ''}
    </div>
    <div class="table-card">
      <div class="table-scroll"><table>
        <thead><tr>
          <th style="width:1%"></th>
          <th>DLC</th>
          <th>Type</th>
          <th>Price</th>
          <th>Buy</th>
        </tr></thead>
        <tbody>${itemRows}</tbody>
      </table></div>
    </div>
    ${items.length ? `
      <div style="display:flex;justify-content:flex-end;padding:16px 0">
        <div style="text-align:right">
          <div class="td-sub" style="font-size:12px">${items.length} item${items.length !== 1 ? 's' : ''} (before tax)</div>
          <div class="cart-total">${currency} ${total.toFixed(2)}</div>
          ${savingsHtml}
        </div>
      </div>` : ''}
  `;

  root.querySelectorAll('.table-link').forEach(a => {
    a.onclick = e => { e.preventDefault(); navigate(a.dataset.href); };
  });
  root.querySelectorAll('.btn-cart-remove').forEach(btn => {
    btn.onclick = async () => {
      await PUT(`/dlc/${btn.dataset.removeId}/cart`, { in_cart: false });
      renderCart();
    };
  });
  document.getElementById('btn-clear-cart')?.addEventListener('click', async () => {
    if (!confirm('Clear your entire cart?')) return;
    await DEL('/cart');
    renderCart();
  });
}

async function renderLibrary() {
  const items = await GET('/owned');
  await loadLookups();

  const itemRows = items.length
    ? items.map(d => `<tr>
        <td><button class="btn-cart in-cart btn-unown" data-unown-id="${d.id}" title="Remove from library" style="border-color:var(--danger);color:var(--danger);background:rgba(239,68,68,0.1)">\u2715</button></td>
        <td class="td-primary"><a class="table-link" data-href="/dlc/${d.id}">${esc(d.content_name)}</a></td>
        <td class="td-sub">${typeTag(d.dlc_type_name)}</td>
        <td class="td-sub">${countryLabel(d.country_name || '')}</td>
      </tr>`).join('')
    : `<tr><td colspan="4" class="empty">Your library is empty. Use the search below to add DLCs you own.</td></tr>`;

  root.innerHTML = `
    <div class="page-header">
      <h1 class="page-title">My Library (${items.length})</h1>
    </div>
    <p style="color:var(--text-sub);font-size:13px;margin-bottom:16px">
      Add DLCs you already own. Owned DLCs cannot be added to the shopping cart.
    </p>
    <div style="margin-bottom:20px">
      <div class="filter-bar">
        <div class="typeahead-wrap" style="flex:1">
          <input type="text" id="lib-search" placeholder="Search DLC to add to library\u2026" autocomplete="off">
          <ul class="typeahead-list hidden" id="lib-search-list"></ul>
        </div>
      </div>
    </div>
    <div class="table-card">
      <div class="table-scroll"><table>
        <thead><tr>
          <th style="width:1%"></th>
          <th>DLC</th>
          <th>Type</th>
          <th>Country</th>
        </tr></thead>
        <tbody>${itemRows}</tbody>
      </table></div>
    </div>
  `;

  root.querySelectorAll('.table-link').forEach(a => {
    a.onclick = e => { e.preventDefault(); navigate(a.dataset.href); };
  });
  root.querySelectorAll('.btn-unown').forEach(btn => {
    btn.onclick = async () => {
      await PUT(`/dlc/${btn.dataset.unownId}/owned`, { owned: false });
      renderLibrary();
    };
  });

  // Typeahead search to add DLCs
  const input = document.getElementById('lib-search');
  const list = document.getElementById('lib-search-list');
  const ownedIds = new Set(items.map(d => d.id));
  let searchTimer;

  input.addEventListener('input', () => {
    clearTimeout(searchTimer);
    searchTimer = setTimeout(async () => {
      const q = input.value.trim();
      if (q.length < 1) { list.classList.add('hidden'); return; }
      const res = await GET(`/dlc?search=${encodeURIComponent(q)}&limit=10`);
      const matches = (res.rows || []).filter(d => !ownedIds.has(d.id));
      if (!matches.length) {
        list.innerHTML = '<li style="color:var(--text-dim);cursor:default;padding:8px 14px">No results</li>';
        list.classList.remove('hidden');
        return;
      }
      list.innerHTML = matches.map(d =>
        `<li class="typeahead-item" data-id="${d.id}" style="display:flex;justify-content:space-between;align-items:center">
          <span>${esc(d.content_name)}</span>
          <span class="td-sub" style="font-size:11px;margin-left:12px">${esc(d.dlc_type_name || '')}</span>
        </li>`
      ).join('');
      list.classList.remove('hidden');
    }, 200);
  });

  list.addEventListener('mousedown', async e => {
    const item = e.target.closest('.typeahead-item');
    if (!item) return;
    await PUT(`/dlc/${item.dataset.id}/owned`, { owned: true });
    input.value = '';
    list.classList.add('hidden');
    renderLibrary();
  });

  input.addEventListener('blur', () => setTimeout(() => list.classList.add('hidden'), 200));
  input.addEventListener('keydown', e => {
    if (e.key === 'Escape') { list.classList.add('hidden'); input.blur(); }
    const allItems = [...list.querySelectorAll('.typeahead-item')];
    if (!allItems.length) return;
    const cur = list.querySelector('.typeahead-item.active');
    let idx = cur ? allItems.indexOf(cur) : -1;
    if (e.key === 'ArrowDown') {
      e.preventDefault();
      if (cur) cur.classList.remove('active');
      idx = (idx + 1) % allItems.length;
      allItems[idx].classList.add('active');
      allItems[idx].scrollIntoView({ block: 'nearest' });
    } else if (e.key === 'ArrowUp') {
      e.preventDefault();
      if (cur) cur.classList.remove('active');
      idx = idx <= 0 ? allItems.length - 1 : idx - 1;
      allItems[idx].classList.add('active');
      allItems[idx].scrollIntoView({ block: 'nearest' });
    } else if (e.key === 'Enter') {
      e.preventDefault();
      const active = list.querySelector('.typeahead-item.active') || allItems[0];
      if (active) active.dispatchEvent(new Event('mousedown', { bubbles: true }));
    }
  });
}

// ─── Boot ────────────────────────────────────────────────────────────────────

updateCartBadge();
route(location.pathname);
