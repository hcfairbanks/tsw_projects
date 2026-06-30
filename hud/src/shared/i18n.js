// i18n for the desktop shell. Loads a locale's translation map from the backend
// (get_locale reads resources/views/locales/<lang>.json) and applies it to any
// element carrying data-i18n / data-i18n-placeholder / data-i18n-title. Same
// locale files + key namespace the browser HUD pages use.
//
// Static text: add data-i18n="shell.load" (etc.) to the element.
// Dynamic (JS-rendered) text: import { t } and call t('shell.load') when you
// build the DOM, then re-render on the 'languageChanged' document event.
//
// Missing keys fall back to whatever text is already there, so a partially
// tagged page degrades gracefully to its hard-coded English.

let translations = {};
let currentLang = 'en';
let loadPromise = null;

export function getLang() {
  return currentLang;
}

// Nested lookup: t('nav.settings') -> translations.nav.settings.
// `fallback` (or the key) is returned when the key is missing.
export function t(key, fallback) {
  if (!key) return fallback != null ? fallback : '';
  const v = key.split('.').reduce(
    (o, k) => (o && o[k] != null ? o[k] : null),
    translations,
  );
  if (typeof v === 'string') return v;
  return fallback != null ? fallback : key;
}

export async function loadLocale(lang) {
  const invoke = window.__TAURI__ && window.__TAURI__.core && window.__TAURI__.core.invoke;
  if (!invoke) return false;
  try {
    translations = await invoke('get_locale', { lang });
    currentLang = lang;
    return true;
  } catch (e) {
    console.error('[i18n] failed to load locale', lang, e);
    return false;
  }
}

// Load the locale named in configuration.json once per page. Cached so nav and
// page scripts can both await it without double-fetching. Returns the language.
export function ensureLocale() {
  if (!loadPromise) {
    loadPromise = (async () => {
      const invoke = window.__TAURI__ && window.__TAURI__.core && window.__TAURI__.core.invoke;
      let lang = 'en';
      if (invoke) {
        try {
          const cfg = await invoke('get_config');
          if (cfg && cfg.language) lang = cfg.language;
        } catch (e) { /* keep default */ }
      }
      await loadLocale(lang);
      return lang;
    })();
  }
  return loadPromise;
}

// Switch language at runtime: load the locale, re-translate the page, and notify
// page scripts (which re-render their dynamic content) via 'languageChanged'.
export async function setLanguage(lang) {
  await loadLocale(lang);
  applyTranslations();
  document.dispatchEvent(new CustomEvent('languageChanged', { detail: { language: lang } }));
}

// Translate every tagged element under `root` (default: whole document).
export function applyTranslations(root) {
  const scope = root || document;
  // On a missing key, keep whatever text/placeholder/title is already in the
  // markup (the English fallback baked into the HTML) instead of overwriting it
  // with the raw key string.
  scope.querySelectorAll('[data-i18n]').forEach((el) => {
    const v = t(el.getAttribute('data-i18n'), el.textContent);
    if (v != null) el.textContent = v;
  });
  scope.querySelectorAll('[data-i18n-placeholder]').forEach((el) => {
    const v = t(el.getAttribute('data-i18n-placeholder'), el.getAttribute('placeholder'));
    if (v != null) el.placeholder = v;
  });
  scope.querySelectorAll('[data-i18n-title]').forEach((el) => {
    const v = t(el.getAttribute('data-i18n-title'), el.getAttribute('title'));
    if (v != null) el.title = v;
  });
}

// Convenience for page scripts: await the locale, translate static markup, and
// register a re-translate on language change. Pass an optional callback to also
// re-render dynamic content each time the language changes (and once now).
export async function initPageI18n(onChange) {
  await ensureLocale();
  applyTranslations();
  if (onChange) {
    onChange();
    document.addEventListener('languageChanged', () => { applyTranslations(); onChange(); });
  } else {
    document.addEventListener('languageChanged', () => applyTranslations());
  }
}
