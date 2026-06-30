Multiple projects for TSW 5 & 6.

# Launch

Double-click `hud/hud.exe` — the Tauri 2 rewrite is now the primary app.
Hosts every page (Web HUD, Collections, Weather, Timetables, Routes, Train
Classes, Custom HUDs, Dev, Settings) natively; the embedded LAN server
exposes only the surfaces a phone / second screen actually needs (HUDs,
map, weather, weather-presets, served custom-HUD content). Reads the same
`tsw_hud.db` that the Go extractor populates.

To rebuild the root-level `hud.exe`:

    cd hud
    .\build.ps1            # dev build
    .\build.ps1 -Release   # optimized

# Legacy builds (kept for reference)

The original Go HUD server (`tsw-hud-new.exe` — still the source of truth
for the **extractor** until Phase 10 ports it to Rust):

    cd hud-go-src
    go build -o ..\hud-go\tsw-hud-new.exe .

The original egui-based desktop app (`hud-rust.exe`) is superseded by
`hud.exe` and only kept on disk as a reference for the porting work.

# Misc

`*#06#`
