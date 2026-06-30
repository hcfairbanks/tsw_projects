// Dev-mode awareness for shell pages.
//
// Reads `developmentMode` from the user's `configuration.json` (via the
// existing `get_config` command) once on page load, then exposes:
//   - isDev()        — synchronous boolean (false until init resolves)
//   - whenReady()    — Promise that resolves once the flag is known
//   - applyDevGate() — toggles the `is-dev` class on the document root and
//                      reveals every `.dev-only` element when dev is on.
//
// CSS contract: `.dev-only { display: none; }` (hidden by default); pages
// that add the class get visibility for free once we know dev mode is on.

const { invoke } = window.__TAURI__.core;

let _dev = false;
let _ready;

function load() {
  _ready = invoke('get_config')
    .then((cfg) => { _dev = !!(cfg && cfg.developmentMode); })
    .catch(() => { _dev = false; })
    .finally(applyDevGate);
}

export function isDev() { return _dev; }
export function whenReady() { return _ready; }
export function applyDevGate() {
  document.documentElement.classList.toggle('is-dev', _dev);
}

// Auto-init on import.
load();
