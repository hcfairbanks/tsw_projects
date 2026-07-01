# applications_2 — project instructions

## "get ready to deploy"

When the user says **"get ready to deploy"** (or just "deploy"), run the standard
local deploy for the `hud/` Tauri app so the user can launch `hud/hud.exe`. Do all
three steps in order — do not stop after the build:

1. **Build the release** (PowerShell; ~3–5 min):
   - `Set-Location 'C:\Users\hcfai\Desktop\applications_2\hud\src-tauri'; cargo build --release`
   - Filter output for `error[`/`error:`/`Finished`. Only proceed if it finished.
2. **Stop the running app** (the exe can't be overwritten while running — this
   closes the user's HUD; they relaunch it):
   - `Get-Process hud -ErrorAction SilentlyContinue | Stop-Process -Force; Start-Sleep -Milliseconds 400`
3. **Copy the fresh exe over the deployed one:**
   - `Copy-Item 'C:\Users\hcfai\Desktop\applications_2\hud\src-tauri\target\release\hud.exe' 'C:\Users\hcfai\Desktop\applications_2\hud\hud.exe' -Force`

The user runs `hud/hud.exe` (its `resources/` sit adjacent to it), **never**
`target/release`. If you skip the copy, fixes never reach the user.

Which changes need a rebuild:
- `hud/src-tauri/**` (Rust) and `hud/src/*.html` (bundled into the exe via
  `frontendDist`) → **need the full build above.**
- `hud/resources/collections/**` and `hud/resources/widgets/**` are read at
  runtime → live on **widget reload**, no rebuild needed (a rebuild is harmless).

## "copy to desktop" — dated release bundle

When the user says **"copy to desktop"**, bundle the current build as a dated
release on the Desktop (mirrors `28-June-2026-hud-rust` / `.zip`):

1. Checkpoint the DB WAL into the main file first (so the shipped DB is complete):
   `PRAGMA wal_checkpoint(TRUNCATE)` on `hud/resources/db/tsw_hud.db`.
2. Create `C:\Users\hcfai\Desktop\<D-Month-YYYY>-hud-rust\` containing `hud.exe` +
   a copy of `hud/resources/`, **excluding** the DB backups
   (`tsw_hud.db.bak*`, `tsw_hud_backup_*.db`) and `*.db-wal`/`*.db-shm` — ship only
   `tsw_hud.db`. (robocopy `/XF` those.)
3. Zip it to `<same-name>.zip`, nested under the base folder name
   (`CreateFromDirectory(..., includeBaseDirectory: true)`).

## Git

Commits in this repo are authored **`Harry <hcfairbanks@gmail.com>`** — set it
locally (`git config --local user.name/user.email`); the global git config is
broken (both are `=`). The heavy/reproducible hud artifacts (`hud/src-tauri/target/`,
`hud/resources/db/`, `hud/route_data/`, `hud/hud.exe`) are gitignored — never stage
them (they're ~27 GB and will hang the commit).

## Re-extraction (headless)

`hud/hud.exe extract-route "<Codename>"` re-runs the native extractor for one route
(match by pak codename, not display name). Used to refresh a route's catalog data
after an extractor fix. `hud/hud.exe import-zip <path>` / `export-route <id> [dir]`
are the CLI import/export hooks.
