//! Standalone widget library.
//!
//! A "widget" here is a single self-contained HTML file living under
//! `resources/widgets/`. This is distinct from `collections::Widget` (a manifest
//! entry referencing a file inside a collection folder) — the library is a flat,
//! reusable store the user manages from the Widgets tab: list, rename (display
//! title), upload a single file, delete.
//!
//! The display "name" is the HTML `<title>` text, edited in place — the on-disk
//! filename is the stable identifier and never changes once written, so nothing
//! that references a widget by filename breaks when it's renamed.

use serde::Serialize;
use std::path::{Path, PathBuf};

pub fn widgets_dir() -> PathBuf {
    crate::config::resources_dir().join("widgets")
}

#[derive(Serialize)]
pub struct WidgetFile {
    /// Filename with extension, e.g. `speedo.html`. Stable identifier.
    pub filename: String,
    /// Filename without extension — handy for display fallback.
    pub slug: String,
    /// Display name: the `<title>` text (falls back to the slug).
    pub title: String,
    pub size: u64,
    /// Absolute filesystem path.
    pub path: String,
}

fn is_html(path: &Path) -> bool {
    matches!(
        path.extension().and_then(|s| s.to_str()).map(str::to_ascii_lowercase).as_deref(),
        Some("html") | Some("htm")
    )
}

fn esc_html(s: &str) -> String {
    s.replace('&', "&amp;").replace('<', "&lt;").replace('>', "&gt;")
}

fn unesc_html(s: &str) -> String {
    s.replace("&lt;", "<")
        .replace("&gt;", ">")
        .replace("&quot;", "\"")
        .replace("&#39;", "'")
        .replace("&amp;", "&")
}

/// Best-effort `<title>…</title>` extraction (case-insensitive).
fn extract_title(html: &str) -> Option<String> {
    let lower = html.to_ascii_lowercase();
    let open = lower.find("<title")?;
    let gt = lower[open..].find('>')? + open + 1;
    let close = lower[gt..].find("</title>")? + gt;
    let raw = html[gt..close].trim();
    if raw.is_empty() {
        None
    } else {
        Some(unesc_html(raw))
    }
}

/// Replace (or insert) the document `<title>` with `new_title`.
fn set_title_in_html(html: &str, new_title: &str) -> String {
    let escaped = esc_html(new_title);
    let lower = html.to_ascii_lowercase();

    if let Some(open) = lower.find("<title") {
        if let Some(rel_gt) = lower[open..].find('>') {
            let content_start = open + rel_gt + 1;
            if let Some(rel_close) = lower[content_start..].find("</title>") {
                let close_start = content_start + rel_close;
                let mut out = String::with_capacity(html.len() + escaped.len());
                out.push_str(&html[..content_start]);
                out.push_str(&escaped);
                out.push_str(&html[close_start..]);
                return out;
            }
        }
    }

    // No usable <title> — inject one right after <head …> if present.
    let tag = format!("<title>{escaped}</title>");
    if let Some(head) = lower.find("<head") {
        if let Some(rel_gt) = lower[head..].find('>') {
            let at = head + rel_gt + 1;
            let mut out = String::with_capacity(html.len() + tag.len());
            out.push_str(&html[..at]);
            out.push_str(&tag);
            out.push_str(&html[at..]);
            return out;
        }
    }
    // No <head> at all — prepend a minimal one.
    format!("<head>{tag}</head>\n{html}")
}

/// Coerce a browser-supplied filename into a safe `<stem>.html` with no path
/// components. Non-alphanumeric chars (incl. spaces, stray dots) collapse to
/// `_`, blocking traversal (`..`, `/`, `\`) entirely.
fn safe_widget_filename(raw: &str) -> Result<String, String> {
    let base = raw.rsplit(['/', '\\']).next().unwrap_or(raw).trim();
    let lower = base.to_ascii_lowercase();
    let stem_src = if lower.ends_with(".html") {
        &base[..base.len() - 5]
    } else if lower.ends_with(".htm") {
        &base[..base.len() - 4]
    } else {
        base
    };
    let mapped: String = stem_src
        .chars()
        .map(|c| if c.is_ascii_alphanumeric() || c == '-' || c == '_' { c } else { '_' })
        .collect();
    let stem = mapped.trim_matches('_');
    if stem.is_empty() {
        return Err("filename has no usable name".to_string());
    }
    Ok(format!("{stem}.html"))
}

/// Enumerate every HTML file in `resources/widgets/`. Missing folder → empty.
pub fn list() -> Vec<WidgetFile> {
    let dir = widgets_dir();
    let mut out = Vec::new();
    let Ok(rd) = std::fs::read_dir(&dir) else { return out };
    for entry in rd.flatten() {
        let path = entry.path();
        if !path.is_file() || !is_html(&path) {
            continue;
        }
        let filename = path.file_name().and_then(|s| s.to_str()).unwrap_or("?").to_string();
        let slug = path.file_stem().and_then(|s| s.to_str()).unwrap_or("?").to_string();
        let size = entry.metadata().map(|m| m.len()).unwrap_or(0);
        let title = std::fs::read_to_string(&path)
            .ok()
            .and_then(|h| extract_title(&h))
            .unwrap_or_else(|| slug.replace('_', " "));
        out.push(WidgetFile {
            filename,
            slug,
            title,
            size,
            path: path.to_string_lossy().to_string(),
        });
    }
    out.sort_by(|a, b| a.title.to_ascii_lowercase().cmp(&b.title.to_ascii_lowercase()));
    out
}

/// Rewrite an existing widget's display title (its `<title>`).
pub fn set_title(filename: &str, title: &str) -> Result<(), String> {
    let title = title.trim();
    if title.is_empty() {
        return Err("name cannot be empty".to_string());
    }
    let safe = safe_widget_filename(filename)?;
    let path = widgets_dir().join(&safe);
    if !path.is_file() {
        return Err(format!("widget not found: {safe}"));
    }
    let html = std::fs::read_to_string(&path).map_err(|e| format!("read {safe}: {e}"))?;
    let updated = set_title_in_html(&html, title);
    std::fs::write(&path, updated).map_err(|e| format!("write {safe}: {e}"))?;
    Ok(())
}

/// Save an uploaded single HTML file into the library. `requested_name` is the
/// browser's file name; collisions get a `-N` suffix so an upload never clobbers
/// an existing widget. Returns the final on-disk filename.
pub fn save(requested_name: &str, contents: &str) -> Result<String, String> {
    let safe = safe_widget_filename(requested_name)?;
    let dir = widgets_dir();
    std::fs::create_dir_all(&dir).map_err(|e| format!("create {}: {e}", dir.display()))?;

    let stem = safe.trim_end_matches(".html");
    let mut final_name = safe.clone();
    let mut n = 1;
    while dir.join(&final_name).exists() {
        final_name = format!("{stem}-{n}.html");
        n += 1;
    }

    let path = dir.join(&final_name);
    std::fs::write(&path, contents).map_err(|e| format!("write {}: {e}", path.display()))?;
    Ok(final_name)
}

/// Delete a widget file. The frontend confirms first.
pub fn delete(filename: &str) -> Result<(), String> {
    let safe = safe_widget_filename(filename)?;
    let path = widgets_dir().join(&safe);
    if !path.is_file() {
        return Err(format!("widget not found: {safe}"));
    }
    std::fs::remove_file(&path).map_err(|e| format!("remove {safe}: {e}"))?;
    Ok(())
}
