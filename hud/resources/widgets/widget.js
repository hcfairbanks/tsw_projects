// Tauri host-shim — the ONLY frontend file that differs from the Electron POC.
// Same responsibilities (drag / collapse / close / live-dot / hug-content sizing)
// but implemented against the Tauri window API instead of a preload bridge.
//
// `withGlobalTauri: true` (tauri.conf.json) exposes window.__TAURI__ so these
// static HTML pages need no bundler/npm.
(function () {
  const { getCurrentWindow, LogicalSize } = window.__TAURI__.window;
  const appWindow = getCurrentWindow();
  const root = document.querySelector('.widget');

  // Size the OS window to exactly the visible panel + body padding, so
  // transparent regions never sit on top of the game eating clicks.
  function syncSize() {
    if (!root) return;
    const r = root.getBoundingClientRect();
    const cs = getComputedStyle(document.body);
    const padX = (parseFloat(cs.paddingLeft) || 0) + (parseFloat(cs.paddingRight) || 0);
    const padY = (parseFloat(cs.paddingTop) || 0) + (parseFloat(cs.paddingBottom) || 0);
    appWindow.setSize(new LogicalSize(Math.ceil(r.width + padX), Math.ceil(r.height + padY)));
  }

  // Drag the window by its header (but not when clicking the buttons).
  // This replaces Electron's `-webkit-app-region: drag`, which WebView2 ignores.
  const hdr = document.querySelector('.hdr');
  if (hdr) {
    hdr.addEventListener('mousedown', (e) => {
      if (e.button !== 0 || e.target.closest('button')) return;
      appWindow.startDragging();
    });
  }

  // Collapse / expand.
  const collapseBtn = document.querySelector('[data-collapse]');
  if (collapseBtn) {
    collapseBtn.addEventListener('click', () => {
      const collapsed = root.classList.toggle('collapsed');
      collapseBtn.textContent = collapsed ? '▢' : '—';
      collapseBtn.title = collapsed ? 'Expand' : 'Collapse';
      requestAnimationFrame(syncSize);
    });
  }

  // Close just this widget window.
  const closeBtn = document.querySelector('[data-close]');
  if (closeBtn) closeBtn.addEventListener('click', () => appWindow.close());

  // Re-fit whenever the panel size changes.
  if (root && 'ResizeObserver' in window) {
    new ResizeObserver(() => syncSize()).observe(root);
  }
  window.addEventListener('load', syncSize);

  // Loading-overlay state. The overlay surfaces what the backend is doing
  // ("Connecting…", "Subscribed to 48 paths", …) plus an elapsed-time counter
  // so a long boot phase looks like progress instead of a hang.
  let loadingStart = null;
  let loadingTick = null;
  let loadingEl = null;
  let lastStatusText = '';

  function ensureLoadingOverlay() {
    if (loadingEl) return loadingEl;
    const body = document.querySelector('.body');
    if (!body) return null;
    loadingEl = document.createElement('div');
    loadingEl.className = 'loading-overlay';
    loadingEl.innerHTML =
      '<div class="lbl">LOADING…</div>' +
      '<div class="status" data-loading-status></div>' +
      '<div class="elapsed" data-loading-elapsed>0</div>';
    body.appendChild(loadingEl);
    return loadingEl;
  }
  function removeLoadingOverlay() {
    if (loadingEl) { loadingEl.remove(); loadingEl = null; }
    if (loadingTick !== null) { clearInterval(loadingTick); loadingTick = null; }
    loadingStart = null;
  }
  function paintLoading() {
    const el = ensureLoadingOverlay(); if (!el) return;
    const s = el.querySelector('[data-loading-status]');
    const e = el.querySelector('[data-loading-elapsed]');
    if (s) s.textContent = lastStatusText || 'Waking up…';
    if (e && loadingStart != null) {
      const sec = Math.floor((Date.now() - loadingStart) / 1000);
      e.textContent = String(sec);
    }
  }

  // Same helper surface the HUD pages already expect (identical to Electron's).
  // `setStatus` is the 3-state successor: 'live' / 'loading' / 'offline'.
  // `setLive(bool)` is kept for backwards compatibility.
  window.widget = {
    sync: syncSize,
    setStatus(state, statusText) {
      const dot = document.querySelector('.hdr .dot');
      const tag = document.querySelector('[data-source]');
      if (dot) {
        dot.classList.toggle('live', state === 'live');
        dot.classList.toggle('loading', state === 'loading');
      }
      if (tag) {
        tag.textContent = state === 'live' ? tr('shell.widget.live', 'LIVE')
                        : state === 'loading' ? tr('shell.widget.loadingShort', 'LOADING…')
                        : tr('shell.widget.offline', 'OFFLINE');
      }
      if (root) {
        root.classList.toggle('is-loading', state === 'loading');
        root.classList.toggle('is-offline', state === 'offline');
      }
      if (state === 'loading') {
        if (loadingStart == null) loadingStart = Date.now();
        lastStatusText = statusText || lastStatusText;
        paintLoading();
        if (loadingTick === null) loadingTick = setInterval(paintLoading, 250);
      } else {
        removeLoadingOverlay();
      }
    },
    setLive(on) { this.setStatus(on ? 'live' : 'offline'); },
  };

  // hud-go server. (Electron passed this via ?base=; here we default it.)
  window.SERVER_BASE = 'http://127.0.0.1:3000';

  // ---- i18n: translate [data-i18n] using the app locale. Overlay widget
  // windows have window.__TAURI__, so they can read the same locale files the
  // shell uses. Everything is wrapped so an i18n failure can never break the
  // widget's drag / collapse / close behaviour.
  let I18N = {};
  function tr(key, fb) {
    const v = String(key).split('.').reduce((o, k) => (o && o[k] != null ? o[k] : null), I18N);
    return typeof v === 'string' ? v : (fb != null ? fb : key);
  }
  function applyI18n() {
    document.querySelectorAll('[data-i18n]').forEach((el) => {
      const v = tr(el.getAttribute('data-i18n'), null); if (v != null) el.textContent = v;
    });
    document.querySelectorAll('[data-i18n-placeholder]').forEach((el) => {
      const v = tr(el.getAttribute('data-i18n-placeholder'), null); if (v != null) el.placeholder = v;
    });
    document.querySelectorAll('[data-i18n-title]').forEach((el) => {
      const v = tr(el.getAttribute('data-i18n-title'), null); if (v != null) el.title = v;
    });
    requestAnimationFrame(syncSize);
  }
  window.widgetTr = tr;
  (async () => {
    try {
      const inv = window.__TAURI__ && window.__TAURI__.core && window.__TAURI__.core.invoke;
      if (!inv) return;
      let lang = 'en';
      try {
        const c = await inv('get_config');
        if (c) {
          if (c.language) lang = c.language;
          const cs = c.colorScheme;
          if (cs && cs !== 'default') document.documentElement.setAttribute('data-color-scheme', cs);
          else document.documentElement.removeAttribute('data-color-scheme');
        }
      } catch (e) {}
      try { I18N = (await inv('get_locale', { lang })) || {}; } catch (e) { I18N = {}; }
      applyI18n();
    } catch (e) { /* never break the widget on i18n failure */ }
  })();
})();
