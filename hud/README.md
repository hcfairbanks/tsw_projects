# TSW HUD

Tauri 2 companion app for Train Sim World 6 — successor to `hud-rust/` (which is staying on disk for reference).

One process. Multiple webview windows. Vanilla JS frontend, no bundler, no npm.

## Prerequisites

- **Rust** (stable, 1.75+) — `rustup` install
- **Microsoft WebView2 Runtime** — already on Windows 11; install from Microsoft if missing
- **No Node.js / npm needed** — the frontend is static HTML/CSS/JS embedded into the binary at build time (`frontendDist: "../src"`)

## Build & run (dev)

From this folder:

```powershell
cargo build --manifest-path src-tauri/Cargo.toml
./src-tauri/target/debug/hud.exe
```

First build pulls in the full Tauri dep tree (~2 minutes). Incremental rebuilds after that are ~5–10 seconds.

Note: `cargo build` from inside `src-tauri/` works too, and is what `tauri-cli` would call under the hood.

## Build (release)

```powershell
cargo build --release --manifest-path src-tauri/Cargo.toml
./src-tauri/target/release/hud.exe
```

Release is significantly smaller and faster to start. Use this for everyday running.

## What's where

```
hud/
  README.md                  # this file
  src/                       # frontend — static HTML/CSS/JS, embedded into the binary
    index.html               # main shell (tab bar)
    shared/
      common.css             # design tokens used by every page
    (more pages added per phase — settings, collections, weather, ...)
  src-tauri/                 # backend — Rust + Tauri 2
    Cargo.toml
    build.rs                 # tauri_build::build()
    tauri.conf.json          # window declarations + bundle config
    capabilities/default.json
    icons/icon.ico
    src/
      main.rs                # Builder, command handlers, window lifecycle
      (more modules added per phase — config, db, tsw, weather, server, ...)
    target/                  # build output (gitignored)
```

## How it works (one-paragraph version)

`hud.exe` launches a single OS process. `tauri.conf.json` declares one **shell window** (the tabbed UI) that opens immediately. When the user loads a collection (Phase 2), `WebviewWindowBuilder` adds N **widget windows** (transparent, always-on-top overlays) to the *same* process — they share one WebView2 environment, so they appear together with no per-window loading flash. Frontend talks to backend via `window.__TAURI__.core.invoke('command_name', { args })`; backend exposes commands with `#[tauri::command]`.

This is the same pattern as `../hud-overlay-tauri/` (the working POC), generalised to the full hud-rust feature surface.

## Phases (roadmap)

Plan lives at `C:\Users\hcfai\.claude\plans\iterative-plotting-snowglobe.md`.

| Phase | Status | Ships |
|---|---|---|
| 0 | ✅ done | Scaffold + shell window + IPC smoke test |
| 0.5 | in progress | Port `db`, `tsw`, `weather`, `features`, `config`, `server`, `collections` modules from hud-rust |
| 1 | pending | Settings page (config form) |
| 2 | pending | Collections page + overlay opener (the headline fix) |
| 3 | pending | Web HUD page (axum start/stop, QR card) |
| 4 | pending | Weather page (preset CRUD, live, historical) |
| 5 | pending | Timetables index |
| 6 | pending | Routes index |
| 7 | pending | Train Classes + Custom HUDs |
| 8 | pending | Dev pages (Countries, Locations, Formations) |
| 9 | pending | Cutover — hud.exe is the only thing the user launches |

## Troubleshooting

**A console window appears alongside the main app** — `src-tauri/src/main.rs` should have `#![windows_subsystem = "windows"]` at the top *unconditionally* (not the `cfg_attr(not(debug_assertions), …)` variant). The conditional form leaves the console attached in debug builds.

**Window opens off-screen on a wide multi-monitor setup** — Tauri centers on the primary monitor by default. Add `"x": ..., "y": ..."` to the shell window entry in `tauri.conf.json` to pin it.

**Build error mentioning a missing Tauri feature** — Tauri's build script reads `tauri.conf.json` and tells you which cargo feature to enable (e.g. `protocol-asset`). Add it to the `features = [...]` array on the `tauri` dep in `src-tauri/Cargo.toml`.

## Why this exists (short version)

The hud-rust app uses eframe (egui) for its main UI and originally spawned one Tauri overlay process per widget. That hit two walls: per-widget loading-screen flash, and WebView2 init quirks across processes (HRESULT 0x80070057, asset-protocol scope, decoration-less close affordances). One Tauri process opening N windows — like `hud-overlay-tauri/` already proved works — fixes both. The full rewrite extends that pattern to the rest of the hud-rust feature set.
