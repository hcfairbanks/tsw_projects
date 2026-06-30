//! Collection discovery + manifest parsing.
//!
//! A collection lives in `resources/collections/<name>/` with a
//! `collection.toml` manifest enumerating its widgets. Widgets are static
//! HTML/CSS/JS files in the same folder.
//!
//! LLM-authored collections: drop a new folder in next to `default/`,
//! include a manifest + widget files, restart the app and the collection
//! shows up in the Collections tab.
//!
//! Window-opening (formerly `spawn_native` in hud-rust) lives in
//! `widgets::open_collection` now — same process, one `WebviewWindowBuilder`
//! per widget. Subprocess spawn is gone.

use serde::{Deserialize, Serialize};
use std::path::PathBuf;

/// On-disk manifest shape. Mirrors what `collection.toml` looks like.
#[derive(Deserialize, Serialize, Clone, Debug)]
pub struct CollectionManifest {
    pub name: String,
    #[serde(default)]
    pub description: String,
    #[serde(default)]
    pub author: String,
    #[serde(default)]
    pub widgets: Vec<Widget>,
}

#[derive(Deserialize, Serialize, Clone, Debug)]
pub struct Widget {
    pub id: String,
    #[serde(default)]
    pub title: String,
    pub path: String,
    /// Where `path` resolves. Empty (the default) means a file inside this
    /// collection's own folder. `"library"` means a shared widget under
    /// `resources/widgets/` — `path` is then just the library filename and the
    /// loader resolves it via the `__widgets__` route in the widget:// scheme.
    #[serde(default, skip_serializing_if = "String::is_empty")]
    pub source: String,
    #[serde(default = "default_size")]
    pub default_size: [u32; 2],
    #[serde(default)]
    pub default_pos: [i32; 2],
    #[serde(default)]
    pub always_on_top: bool,
    #[serde(default)]
    pub transparent: bool,
}

/// Reserved first URL segment in the widget:// scheme that routes to the shared
/// `resources/widgets/` library instead of a collection folder. Also a forbidden
/// collection slug so a real collection can never shadow it.
pub const LIBRARY_ROUTE: &str = "__widgets__";

fn default_size() -> [u32; 2] {
    [480, 320]
}

/// Loaded collection with its folder slug (used in URLs) and the parsed manifest.
#[derive(Clone, Debug, Serialize)]
pub struct Collection {
    pub slug: String,
    pub dir: PathBuf,
    pub manifest: CollectionManifest,
}

pub fn collections_dir() -> PathBuf {
    crate::config::resources_dir().join("collections")
}

/// Enumerate every sub-folder of `collections/` that contains a parseable
/// `collection.toml`. Folders without a manifest are skipped silently so the
/// user can drop in WIP work without breaking the index.
pub fn list() -> Vec<Collection> {
    let root = collections_dir();
    let mut out = Vec::new();
    let Ok(rd) = std::fs::read_dir(&root) else { return out };
    for entry in rd.flatten() {
        let path = entry.path();
        if !path.is_dir() {
            continue;
        }
        let manifest_path = path.join("collection.toml");
        let Ok(text) = std::fs::read_to_string(&manifest_path) else {
            continue;
        };
        let manifest: CollectionManifest = match toml::from_str(&text) {
            Ok(m) => m,
            Err(e) => {
                eprintln!("[collections] {}: {e}", manifest_path.display());
                continue;
            }
        };
        let slug = path
            .file_name()
            .and_then(|s| s.to_str())
            .unwrap_or("?")
            .to_string();
        out.push(Collection { slug, dir: path, manifest });
    }
    // Stable order: alphabetical by slug.
    out.sort_by(|a, b| a.slug.cmp(&b.slug));
    out
}

/// Find a single collection by its folder slug (URL segment).
pub fn get(slug: &str) -> Option<Collection> {
    list().into_iter().find(|c| c.slug == slug)
}

/// Validate a slug or widget filename — same conservative rule we use for
/// custom HUDs, blocks path traversal.
pub fn is_safe_segment(s: &str) -> bool {
    !s.is_empty()
        && s.chars()
            .all(|c| c.is_ascii_alphanumeric() || c == '-' || c == '_' || c == '.')
}

/// Create or overwrite a collection's `collection.toml`. The folder is created
/// if it doesn't exist yet (the "add new collection" path); an existing folder
/// just gets its manifest rewritten (the "edit" path). Widget HTML/CSS/JS files
/// are managed separately — this only owns the manifest.
pub fn save(slug: &str, manifest: &CollectionManifest) -> Result<(), String> {
    if !is_safe_segment(slug) {
        return Err(format!("invalid collection slug: {slug:?}"));
    }
    if slug == LIBRARY_ROUTE {
        return Err(format!("'{LIBRARY_ROUTE}' is reserved"));
    }
    if manifest.name.trim().is_empty() {
        return Err("collection name is required".to_string());
    }
    let dir = collections_dir().join(slug);
    std::fs::create_dir_all(&dir).map_err(|e| format!("create {}: {e}", dir.display()))?;
    let toml_text =
        toml::to_string_pretty(manifest).map_err(|e| format!("serialize manifest: {e}"))?;
    let manifest_path = dir.join("collection.toml");
    std::fs::write(&manifest_path, toml_text)
        .map_err(|e| format!("write {}: {e}", manifest_path.display()))?;
    Ok(())
}

/// Delete a collection folder and everything in it. Used by the Collections
/// page's per-card Delete affordance.
pub fn delete(slug: &str) -> Result<(), String> {
    if !is_safe_segment(slug) {
        return Err(format!("invalid collection slug: {slug:?}"));
    }
    let dir = collections_dir().join(slug);
    if !dir.is_dir() {
        return Err(format!("collection not found: {slug}"));
    }
    std::fs::remove_dir_all(&dir).map_err(|e| format!("remove {}: {e}", dir.display()))?;
    Ok(())
}
