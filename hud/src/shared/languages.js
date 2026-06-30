// Supported UI languages — mirrors i18n.js SUPPORTED_LANGUAGES and the served
// navbar.js list, so the desktop shell's language picker stays in sync with the
// browser HUD pages. `flag` is the basename of the PNG in src/flags/.
export const LANGUAGES = [
  { code: 'en',    flag: 'gb', name: 'English' },
  { code: 'en-US', flag: 'us', name: 'English (US)' },
  { code: 'fr',    flag: 'fr', name: 'Français' },
  { code: 'de',    flag: 'de', name: 'Deutsch' },
  { code: 'it',    flag: 'it', name: 'Italiano' },
  { code: 'es',    flag: 'es', name: 'Español' },
  { code: 'pl',    flag: 'pl', name: 'Polski' },
  { code: 'ru',    flag: 'ru', name: 'Русский' },
  { code: 'zh',    flag: 'cn', name: '中文' },
  { code: 'ja',    flag: 'jp', name: '日本語' },
];

// Shell pages all live at the src/ root, so a bare relative path resolves.
export function flagSrc(flag) {
  return `flags/${flag}.png`;
}

export function langByCode(code) {
  return LANGUAGES.find((l) => l.code === code) || LANGUAGES[0];
}
