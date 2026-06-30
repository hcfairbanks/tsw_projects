// Renders the shared tab strip into a `<div id="tabs"></div>` placeholder.
// Each page calls `renderNav('settings')` (etc.) to highlight its own tab.
// Vanilla JS, no framework — matches the hud-overlay-tauri style.
//
// Importing dev.js here (and not on each page) guarantees every page that
// uses renderNav() gets the developmentMode flag detection. Without this,
// dev-only tabs render with the .dev-only class but stay hidden forever
// because nothing toggled `.is-dev` on <html>.

import './dev.js';
import { LANGUAGES, flagSrc, langByCode } from './languages.js';
import { ensureLocale, applyTranslations, setLanguage, getLang } from './i18n.js';

// `dev: true` tabs render with the .dev-only class so they're hidden unless
// shared/dev.js has confirmed developmentMode is on (which adds .is-dev to
// <html>). No JS coordination needed — pure CSS.
// `i18n` is an optional translation key (from the shared locale files). Tabs
// without one keep their hard-coded English label.
const TABS = [
  { id: 'home',         label: 'Start',         href: 'index.html',         i18n: 'nav.start' },
  { id: 'web-hud',      label: 'Web HUD',       href: 'web-hud.html',       i18n: 'nav.hud' },
  { id: 'custom-huds',  label: 'Custom HUDs',   href: 'custom-huds.html',   i18n: 'nav.customHuds',           dev: true },
  { id: 'collections',  label: 'Collections',   href: 'collections.html',   i18n: 'shell.collectionsHeading', dev: true },
  { id: 'widgets',      label: 'Widgets',       href: 'widgets.html',       i18n: 'shell.widgetsHeading',     dev: true },
  { id: 'weather',      label: 'Weather',       href: 'weather.html',       i18n: 'nav.weather' },
  { id: 'timetables',   label: 'Timetables',    href: 'timetables.html',    i18n: 'nav.timetables' },
  { id: 'routes',       label: 'Routes',        href: 'routes.html',        i18n: 'nav.routes' },
  { id: 'train-classes',label: 'Train Classes', href: 'train-classes.html', i18n: 'shell.trainClasses.heading' },
  { id: 'extraction',   label: 'Extraction',    href: 'extraction.html',    i18n: 'shell.extraction.heading' },
  { id: 'dev',          label: 'Dev',           href: 'dev.html', dev: true, i18n: 'shell.dev.title' },
  { id: 'settings',     label: 'Settings',      href: 'settings.html',      i18n: 'nav.settings' },
];

export function renderNav(activeId) {
  const host = document.getElementById('tabs');
  if (!host) return;
  host.innerHTML = TABS.map((t) => {
    const cls = [t.id === activeId ? 'on' : '', t.dev ? 'dev-only' : '']
      .filter(Boolean).join(' ');
    const i18n = t.i18n ? ` data-i18n="${t.i18n}"` : '';
    return `<a href="${t.href}" class="${cls}"${i18n}>${t.label}</a>`;
  }).join('');
  renderLanguageWidget(host);
  applyColorScheme();
  applyTheme();
}

// Apply the saved theme (config.theme: "dark"|"light") by setting data-theme on
// <html>; common.css's html[data-theme="light"] overrides the palette. Exported
// so Settings can re-apply it instantly when changed.
export async function applyTheme(theme) {
  const invoke = window.__TAURI__ && window.__TAURI__.core && window.__TAURI__.core.invoke;
  let s = theme;
  if (s == null) {
    if (!invoke) return;
    try { const cfg = await invoke('get_config'); s = cfg && cfg.theme; } catch (e) { return; }
  }
  document.documentElement.setAttribute('data-theme', s === 'light' ? 'light' : 'dark');
}

// Apply the saved color-blind scheme (config.color_scheme) by setting
// data-color-scheme on <html>; common.css overrides the palette. Exported so the
// Settings page can re-apply it instantly when changed.
export async function applyColorScheme(scheme) {
  const invoke = window.__TAURI__ && window.__TAURI__.core && window.__TAURI__.core.invoke;
  let s = scheme;
  if (s == null) {
    if (!invoke) return;
    try { const cfg = await invoke('get_config'); s = cfg && cfg.colorScheme; } catch (e) { return; }
  }
  const root = document.documentElement;
  if (s && s !== 'default') root.setAttribute('data-color-scheme', s);
  else root.removeAttribute('data-color-scheme');
}

// Flag-based language picker, pinned to the right of the tab strip. Reads and
// writes `configuration.json.language` via the config IPC — the same value the
// browser HUD pages + overlay widgets consume. The desktop shell's own labels
// aren't translated (no i18n strings for them), so this only sets the
// preference; it doesn't relabel these admin pages.
async function renderLanguageWidget(host) {
  const invoke = window.__TAURI__ && window.__TAURI__.core && window.__TAURI__.core.invoke;
  if (!invoke) return;

  // Load the saved locale + translate the page (nav tabs + any [data-i18n]).
  const loaded = await ensureLocale();
  applyTranslations();
  let current = LANGUAGES.some((l) => l.code === loaded) ? loaded : 'en';

  const wrap = document.createElement('div');
  wrap.className = 'lang-widget';

  const btn = document.createElement('button');
  btn.type = 'button';
  btn.className = 'lang-current';
  btn.title = 'Language';
  btn.innerHTML = `<img src="${flagSrc(langByCode(current).flag)}" alt=""><span class="arrow">▼</span>`;

  const menu = document.createElement('div');
  menu.className = 'lang-menu';
  menu.innerHTML = LANGUAGES.map((l) =>
    `<div class="lang-opt${l.code === current ? ' active' : ''}" data-code="${l.code}">` +
      `<img src="${flagSrc(l.flag)}" alt=""><span>${l.name}</span></div>`
  ).join('');

  wrap.appendChild(btn);
  wrap.appendChild(menu);
  host.appendChild(wrap);

  btn.addEventListener('click', (e) => { e.stopPropagation(); menu.classList.toggle('open'); });
  document.addEventListener('click', () => menu.classList.remove('open'));

  menu.addEventListener('click', async (e) => {
    const opt = e.target.closest('.lang-opt');
    if (!opt) return;
    menu.classList.remove('open');
    const code = opt.dataset.code;
    if (code === current) return;
    try {
      // Re-read so we persist the full current config, only swapping language.
      const fresh = await invoke('get_config');
      fresh.language = code;
      await invoke('set_config', { config: fresh });
      current = code;
      btn.querySelector('img').src = flagSrc(langByCode(code).flag);
      menu.querySelectorAll('.lang-opt').forEach((o) =>
        o.classList.toggle('active', o.dataset.code === code));
      // Load the new locale, re-translate static markup, and notify page
      // scripts so they re-render their dynamic content. No reload needed.
      await setLanguage(code);
    } catch (err) {
      console.error('[nav] set language failed', err);
    }
  });
}
