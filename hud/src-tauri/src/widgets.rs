//! Overlay-widget lifecycle.
//!
//! The headline architectural fix: hud-rust spawned one Tauri subprocess per
//! widget (overlay-host.exe), which caused per-window WebView2 init flashes
//! plus a cascade of platform quirks (HRESULT 0x80070057, asset.localhost
//! resolution, decorations(false) lacking a close affordance, …).
//!
//! Here we stay in-process: when the user loads a collection, we create one
//! `WebviewWindowBuilder` per widget. They share the same WebView2 environment,
//! so they appear together — no per-window flash, no inter-process IPC.
//!
//! Widget HTML/CSS/JS lives outside frontendDist (in
//! `<resources_dir>/collections/<slug>/`) so the user can drop new
//! LLM-authored collections in without rebuilding the binary. We expose those
//! files to the webview via a custom `widget://` URI scheme rather than
//! `file://` (which WebView2 blocks) or asset:// (whose scope rules bit us).

use std::borrow::Cow;
use tauri::{
    AppHandle, LogicalPosition, LogicalSize, Manager, UriSchemeContext, WebviewUrl,
    WebviewWindowBuilder, Wry,
};

use crate::collections::Collection;

// ----------------------------------------------------------- widget:// scheme

fn guess_mime(path: &std::path::Path) -> &'static str {
    match path
        .extension()
        .and_then(|e| e.to_str())
        .map(str::to_ascii_lowercase)
        .as_deref()
    {
        Some("html") | Some("htm") => "text/html; charset=utf-8",
        Some("css") => "text/css; charset=utf-8",
        Some("js") | Some("mjs") => "application/javascript; charset=utf-8",
        Some("json") => "application/json; charset=utf-8",
        Some("svg") => "image/svg+xml",
        Some("png") => "image/png",
        Some("jpg") | Some("jpeg") => "image/jpeg",
        Some("gif") => "image/gif",
        Some("webp") => "image/webp",
        Some("ico") => "image/x-icon",
        Some("woff") => "font/woff",
        Some("woff2") => "font/woff2",
        Some("ttf") => "font/ttf",
        Some("otf") => "font/otf",
        Some("wasm") => "application/wasm",
        _ => "application/octet-stream",
    }
}

/// Minimal percent decoder — enough for the file paths a widget URL will
/// contain. Avoids pulling another crate in just for this.
fn percent_decode(s: &str) -> String {
    let bytes = s.as_bytes();
    let mut out = Vec::with_capacity(bytes.len());
    let mut i = 0;
    while i < bytes.len() {
        if bytes[i] == b'%' && i + 2 < bytes.len() {
            let pair = (hex(bytes[i + 1]), hex(bytes[i + 2]));
            if let (Some(a), Some(b)) = pair {
                out.push((a << 4) | b);
                i += 3;
                continue;
            }
        }
        out.push(bytes[i]);
        i += 1;
    }
    String::from_utf8_lossy(&out).into_owned()
}

fn hex(b: u8) -> Option<u8> {
    match b {
        b'0'..=b'9' => Some(b - b'0'),
        b'a'..=b'f' => Some(b - b'a' + 10),
        b'A'..=b'F' => Some(b - b'A' + 10),
        _ => None,
    }
}

/// Resolve `widget://localhost/<collection-slug>/<file>` → bytes on disk.
///
/// Containment check: the canonicalized target MUST live under
/// `<resources_dir>/collections/` so a malicious URL can't escape the sandbox
/// (e.g. `widget://localhost/..%2F..%2Fetc%2Fpasswd`).
pub fn widget_protocol_handler(
    _ctx: UriSchemeContext<'_, Wry>,
    request: tauri::http::Request<Vec<u8>>,
) -> tauri::http::Response<Cow<'static, [u8]>> {
    let uri = request.uri();
    // `path()` yields "/dashboard/dashboard.html" — strip the leading slash.
    let raw = uri.path().trim_start_matches('/');
    let decoded = percent_decode(raw);

    // Library widgets come in as `__widgets__/<file>` and resolve against the
    // shared resources/widgets/ folder; everything else resolves against the
    // collections root. Relative assets a library widget pulls in (common.css,
    // widget.js, …) arrive with the same `__widgets__/` prefix, so they land in
    // the widgets folder too.
    let lib_prefix = format!("{}/", crate::collections::LIBRARY_ROUTE);
    let (collections_root, decoded) = if let Some(rest) = decoded.strip_prefix(&lib_prefix) {
        (crate::widget_library::widgets_dir(), rest.to_string())
    } else {
        (crate::collections::collections_dir(), decoded)
    };
    let candidate = collections_root.join(&decoded);

    // Containment: the candidate may not exist yet (404 path), so canonicalize
    // the deepest existing ancestor and re-attach the tail. Keeps the sandbox
    // even when the URL is wrong.
    let containment_root = collections_root.canonicalize().unwrap_or(collections_root.clone());
    let mut probe = candidate.clone();
    while !probe.exists() {
        if !probe.pop() {
            return not_found(format!("widget://{}: not found", decoded));
        }
    }
    let probe_canon = match probe.canonicalize() {
        Ok(p) => p,
        Err(e) => return not_found(format!("widget://{}: canonicalize: {e}", decoded)),
    };
    if !probe_canon.starts_with(&containment_root) {
        return not_found(format!(
            "widget://{}: outside collections root",
            decoded
        ));
    }

    match std::fs::read(&candidate) {
        Ok(bytes) => tauri::http::Response::builder()
            .status(200)
            .header("Content-Type", guess_mime(&candidate))
            .header("Access-Control-Allow-Origin", "*")
            // No caching: widget HTML/CSS/JS are edited live by the user (or
            // by an LLM) and the previous defaults let WebView2 hold onto
            // stale copies, hiding fixes (e.g. the Follow-train toggle).
            .header("Cache-Control", "no-store, no-cache, must-revalidate, max-age=0")
            .header("Pragma", "no-cache")
            .header("Expires", "0")
            .body(Cow::Owned(bytes))
            .unwrap(),
        Err(e) => not_found(format!("widget://{}: {e}", decoded)),
    }
}

fn not_found(msg: String) -> tauri::http::Response<Cow<'static, [u8]>> {
    tauri::http::Response::builder()
        .status(404)
        .header("Content-Type", "text/plain; charset=utf-8")
        .body(Cow::Owned(msg.into_bytes()))
        .unwrap()
}

// ----------------------------------------------------------- thumb:// scheme
//
// `thumb://localhost/<image-path>` serves images from the single canonical
// images root (`<resources_dir>/images/`). Same canonicalize-containment as
// widget:// so a crafted URL can't escape it.
pub fn thumb_protocol_handler(
    _ctx: UriSchemeContext<'_, Wry>,
    request: tauri::http::Request<Vec<u8>>,
) -> tauri::http::Response<Cow<'static, [u8]>> {
    let uri = request.uri();
    let raw = uri.path().trim_start_matches('/');
    let decoded = percent_decode(raw);

    let root = crate::config::resources_dir().join("images");
    let candidate = root.join(&decoded);
    if candidate.exists() {
        let containment_root = root.canonicalize().unwrap_or_else(|_| root.clone());
        if let Ok(candidate_canon) = candidate.canonicalize() {
            if candidate_canon.starts_with(&containment_root) {
                if let Ok(bytes) = std::fs::read(&candidate) {
                    return tauri::http::Response::builder()
                        .status(200)
                        .header("Content-Type", guess_mime(&candidate))
                        .header("Access-Control-Allow-Origin", "*")
                        .header("Cache-Control", "public, max-age=86400")
                        .body(Cow::Owned(bytes))
                        .unwrap();
                }
            }
        }
    }
    not_found(format!("thumb://{}: not found", decoded))
}

// ----------------------------------------------------------- window opener

/// Window label for one widget in one collection. Globally unique within the
/// Tauri app — we keep collection-then-widget so a later "close this
/// collection" command can prefix-match.
fn window_label(c: &Collection, widget_id: &str) -> String {
    format!("widget__{}__{}", c.slug, widget_id)
}

/// Open every widget declared in a collection's manifest as its own webview
/// window. All windows are created in this process; they share one WebView2
/// environment so they appear together with no per-widget loading flash.
///
/// Returns the list of labels that were successfully opened. If a label
/// already exists (collection loaded twice) we bring that window to the
/// front instead of opening a duplicate.
pub fn open_collection(app: &AppHandle, c: &Collection) -> Result<Vec<String>, String> {
    let mut opened = Vec::new();
    let mut errors = Vec::new();

    for w in &c.manifest.widgets {
        let label = window_label(c, &w.id);

        if let Some(existing) = app.get_webview_window(&label) {
            let _ = existing.set_focus();
            opened.push(label);
            continue;
        }

        // Library widgets resolve against resources/widgets/ via the reserved
        // `__widgets__` route; collection-local widgets resolve against the
        // collection's own folder.
        let url_str = if w.source == "library" {
            format!(
                "widget://localhost/{}/{}",
                crate::collections::LIBRARY_ROUTE,
                w.path
            )
        } else {
            format!("widget://localhost/{}/{}", c.slug, w.path)
        };
        let url = match url::Url::parse(&url_str) {
            Ok(u) => u,
            Err(e) => {
                errors.push(format!("{}: bad url ({e})", w.id));
                continue;
            }
        };

        let title = if w.title.is_empty() { &w.id } else { &w.title };

        let mut builder = WebviewWindowBuilder::new(app, &label, WebviewUrl::External(url))
            .title(title)
            .inner_size(w.default_size[0] as f64, w.default_size[1] as f64)
            .position(w.default_pos[0] as f64, w.default_pos[1] as f64)
            .resizable(true)
            .skip_taskbar(true)
            .decorations(false)
            .shadow(false);

        if w.transparent {
            builder = builder.transparent(true);
        }
        if w.always_on_top {
            builder = builder.always_on_top(true);
        }

        match builder.build() {
            Ok(_) => opened.push(label),
            Err(e) => errors.push(format!("{}: {e}", w.id)),
        }
    }

    if !errors.is_empty() && opened.is_empty() {
        return Err(errors.join("; "));
    }
    // Partial success surfaces opened labels; the caller decides how to react
    // to errors (they're logged via stderr too).
    for e in &errors {
        eprintln!("[widgets] {e}");
    }
    Ok(opened)
}

/// Close every webview window belonging to a collection (matched by the label
/// prefix `widget__<slug>__`). Used by the Collections page's "Close all"
/// affordance; harmless if the collection isn't currently loaded.
pub fn close_collection(app: &AppHandle, slug: &str) -> Result<usize, String> {
    let prefix = format!("widget__{}__", slug);
    let mut closed = 0;
    let windows = app.webview_windows();
    for (label, win) in windows.iter() {
        if label.starts_with(&prefix) {
            if win.close().is_ok() {
                closed += 1;
            }
        }
    }
    Ok(closed)
}

/// Window-position offsets are in *physical* pixels under tao's logical-by-default
/// API. Helper that we'll expose to the frontend so a future "tile widgets"
/// feature can read the host's primary-monitor size.
pub fn _primary_monitor_size(app: &AppHandle) -> LogicalSize<f64> {
    if let Some(shell) = app.get_webview_window("shell") {
        if let Ok(Some(m)) = shell.primary_monitor() {
            let scale = m.scale_factor();
            let s = m.size();
            return LogicalSize::new(s.width as f64 / scale, s.height as f64 / scale);
        }
    }
    LogicalSize::new(1920.0, 1080.0)
}

#[allow(dead_code)]
fn _suppress_logical_position_warning(_p: LogicalPosition<f64>) {}
