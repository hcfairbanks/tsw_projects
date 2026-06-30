// Shared Web-HUD server controls — the SINGLE implementation used by both the
// Web HUD tab and the Custom HUDs tab so their "turn on the server" section is
// byte-for-byte identical (Start/Stop buttons, Running/Connecting pills, port/LAN
// line, boot log, error messages). Modeled on the Web HUD tab (the default).
//
// Each page supplies renderRunning(contentEl, status) to fill the area BELOW the
// toolbar when the server is up (Web HUD: QR cards + telemetry; Custom HUDs: the
// HUD cards). opts.unlockWhenRunning=true shows that content as soon as the
// server is running (Custom HUDs); otherwise it waits for live telemetry like the
// Web HUD tab. opts.patch(status) lets a page update its content in place between
// polls (no full re-render → no QR flicker). opts.onStatus(status) fires each poll.
import { t } from './i18n.js';

const { invoke } = window.__TAURI__.core;

function esc(s) {
  return String(s == null ? '' : s).replace(/[&<>"]/g,
    (c) => ({ '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;' }[c]));
}

export function mountServerControls(root, opts = {}) {
  const renderRunning = opts.renderRunning;
  let cardsUnlocked = false;
  let lastView = '';

  function renderStopped(lastErr) {
    root.innerHTML = `
      <div class="start-panel">
        <div class="row">
          <label for="sc-port">${esc(t('shell.webHud.port', 'Port'))}</label>
          <input id="sc-port" type="number" min="1" max="65535" value="3000">
          <button id="sc-start" class="primary">${esc(t('shell.webHud.startServer', 'Start server'))}</button>
        </div>
        ${lastErr ? `<p class="err-text" style="margin-top:10px;">${esc(lastErr)}</p>` : ''}
      </div>`;
    root.querySelector('#sc-start').addEventListener('click', startServer);
  }

  function logHtml(s) {
    return (s.log && s.log.length)
      ? s.log.map((l) => esc(l)).join('\n')
      : `<span class="empty">${esc(t('shell.webHud.waitingFirstLog', '(waiting for first log line…)'))}</span>`;
  }

  function renderConnecting(s) {
    root.innerHTML = `
      <div class="toolbar">
        <span class="pill warn">${esc(t('shell.webHud.connecting', 'Connecting…'))}</span>
        <span class="muted">${esc(t('shell.webHud.port', 'Port'))} ${s.port}</span>
        <button id="sc-stop">${esc(t('common.stop', 'Stop'))}</button>
      </div>
      <div class="connecting-wrap">
        <div>
          <h2>${esc(t('shell.webHud.bootLog', 'Boot log'))}</h2>
          <div class="log" id="sc-log">${logHtml(s)}</div>
        </div>
      </div>`;
    root.querySelector('#sc-stop').addEventListener('click', stopServer);
    const el = root.querySelector('#sc-log'); if (el) el.scrollTop = el.scrollHeight;
  }

  async function renderRunningView(s) {
    root.innerHTML = `
      <div class="toolbar">
        <span class="pill live">${esc(t('shell.webHud.running', 'Running'))}</span>
        <span class="muted">${esc(t('shell.webHud.port', 'Port'))} ${s.port}${s.lan_ip ? ` · ${esc(t('shell.webHud.lan', 'LAN'))} ${esc(s.lan_ip)}` : ''}</span>
        <button id="sc-stop">${esc(t('common.stop', 'Stop'))}</button>
      </div>
      <details class="sc-logwrap">
        <summary>${esc(t('shell.webHud.bootLog', 'Boot log'))}</summary>
        <div class="log" id="sc-log">${logHtml(s)}</div>
      </details>
      <div id="sc-content"></div>`;
    root.querySelector('#sc-stop').addEventListener('click', stopServer);
    if (renderRunning) await renderRunning(root.querySelector('#sc-content'), s);
  }

  function paintLog(s) {
    const el = root.querySelector('#sc-log');
    if (!el) return;
    const txt = (s.log && s.log.length) ? s.log.map((l) => esc(l)).join('\n') : '';
    if (txt && el.textContent !== txt) { el.textContent = txt; el.scrollTop = el.scrollHeight; }
  }

  async function applyStatus(s) {
    if (s.telemetry_live || (opts.unlockWhenRunning && s.running)) cardsUnlocked = true;
    let view;
    if (!s.running) view = 'stopped';
    else if (cardsUnlocked) view = 'running';
    else view = 'connecting';

    if (view !== lastView) {
      lastView = view;
      if (view === 'stopped') renderStopped('');
      else if (view === 'connecting') renderConnecting(s);
      else await renderRunningView(s);
    } else if (view === 'connecting' || view === 'running') {
      paintLog(s);
      if (view === 'running' && opts.patch) opts.patch(s);
    }
    if (opts.onStatus) opts.onStatus(s);
  }

  async function startServer() {
    const inp = root.querySelector('#sc-port');
    const port = Number(inp && inp.value);
    if (!port || port < 1 || port > 65535) { renderStopped(t('shell.webHud.invalidPort', 'invalid port')); return; }
    try {
      cardsUnlocked = false; lastView = '';
      const s = await invoke('web_hud_start', { port });
      await applyStatus(s);
    } catch (e) { renderStopped((e && e.toString()) || t('shell.webHud.startFailed', 'start failed')); }
  }

  async function stopServer() {
    try { await invoke('web_hud_stop'); cardsUnlocked = false; lastView = ''; await refresh(); }
    catch (e) { renderStopped((e && e.toString()) || t('shell.webHud.stopFailed', 'stop failed')); }
  }

  async function refresh() {
    try { const s = await invoke('web_hud_status'); await applyStatus(s); }
    catch (e) { root.innerHTML = `<p class="err-text">${esc(t('shell.webHud.statusFailed', 'status failed:'))} ${esc(e && e.toString())}</p>`; }
  }

  // Force a full repaint on the next poll (used on language change).
  function reset() { lastView = ''; }

  refresh();
  setInterval(refresh, 500);
  return { refresh, reset };
}
