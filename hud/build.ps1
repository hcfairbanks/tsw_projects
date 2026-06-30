# Build the hud Tauri app and refresh the root-level hud.exe so it can be
# double-clicked from this folder. Pass `-Release` to produce an optimized
# build instead of the default dev profile.
#
# Usage:
#   .\build.ps1            # dev build, copies target\debug\hud.exe -> hud.exe
#   .\build.ps1 -Release   # release build, copies target\release\hud.exe -> hud.exe
[CmdletBinding()]
param(
    [switch]$Release
)

# Do NOT set $ErrorActionPreference='Stop' — PowerShell wraps every stderr
# line from native commands into an ErrorRecord, so cargo's warnings (e.g.
# "unused macro definition") would abort us even on a successful build.
# We check $LASTEXITCODE manually below instead.

$here     = Split-Path -Parent $MyInvocation.MyCommand.Path
$manifest = Join-Path $here 'src-tauri\Cargo.toml'
$profile  = if ($Release) { 'release' } else { 'debug' }
$srcExe   = Join-Path $here "src-tauri\target\$profile\hud.exe"
$dstExe   = Join-Path $here 'hud.exe'

# Stop a running copy of the app first so the overwrite isn't blocked.
Get-Process hud -EA SilentlyContinue | Stop-Process -Force
Start-Sleep -Milliseconds 150

# Force tauri-build to re-run so the `src/` folder (frontendDist) gets
# re-embedded into the binary. Without this, edits to nav.js / *.html
# don't end up in the new hud.exe because Cargo's incremental build
# treats nothing-Rust-changed as nothing-to-do.
$buildRs = Join-Path $here 'src-tauri\build.rs'
if (Test-Path $buildRs) { (Get-Item $buildRs).LastWriteTime = Get-Date }

$cargoArgs = @('build', '--manifest-path', $manifest)
if ($Release) { $cargoArgs += '--release' }
Write-Host "cargo $($cargoArgs -join ' ')" -ForegroundColor Cyan
& cargo @cargoArgs
if ($LASTEXITCODE -ne 0) {
    Write-Host "cargo build failed (exit $LASTEXITCODE)" -ForegroundColor Red
    exit $LASTEXITCODE
}

if (-not (Test-Path $srcExe)) {
    Write-Host "build succeeded but $srcExe is missing" -ForegroundColor Red
    exit 1
}
Copy-Item -LiteralPath $srcExe -Destination $dstExe -Force
$bytes = (Get-Item $dstExe).Length
Write-Host "refreshed $dstExe ($([Math]::Round($bytes/1MB,1)) MB, $profile profile)" -ForegroundColor Green
