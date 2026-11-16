# Build the Windows GUI client (wails3 + embedded frontend).
# wails3 v3 on Windows is pure Go: CGO_ENABLED=0, no mingw-w64 gcc needed.
# NOTE: keep this script ASCII-only (PowerShell 5.1 may decode UTF-8 without BOM as ANSI).
$ErrorActionPreference = "Stop"
Set-Location (Join-Path $PSScriptRoot "..")

# ---- 1. Version ----
$version = "dev"
try { $version = (git describe --tags --always 2>$null).Trim() } catch { }
if (-not $version) { $version = "dev" }

# ---- 2. Frontend (GUI mode -> frontend/dist) ----
Write-Host "==> Building GUI frontend"
Remove-Item -Recurse -Force frontend/dist -ErrorAction SilentlyContinue
Push-Location frontend
if (-not (Test-Path node_modules)) { npm install }
npm run build
if ($LASTEXITCODE -ne 0) { Pop-Location; exit $LASTEXITCODE }
Pop-Location

# ---- 3. Copy frontend output into the Go embed directory ----
$assets = "cmd/plc-simulater-gui/assets"
Remove-Item -Recurse -Force $assets -ErrorAction SilentlyContinue
New-Item -ItemType Directory -Force -Path $assets | Out-Null
Copy-Item -Recurse -Force frontend/dist/* $assets/
Write-Host "==> Frontend assets copied to $assets"

# ---- 4. Application icon, derived from the repository-root logo.png ----
# logo.png is the single source of truth. This step refreshes:
#   cmd/plc-simulater-gui/icon.png             -> //go:embed, Options.Icon (Linux/macOS)
#   build/windows/icon.ico                     -> multi-size Windows icon
#   cmd/plc-simulater-gui/rsrc_windows_*.syso  -> linked into the .exe (Explorer/taskbar icon)
$wails = Get-Command wails3 -ErrorAction SilentlyContinue
if (-not $wails) {
    throw "wails3 CLI not found. Install it with: go install github.com/wailsapp/wails/v3/cmd/wails3@v3.0.0"
}

$guiDir = "cmd/plc-simulater-gui"
$iconDir = "build/windows"
$iconFile = Join-Path $iconDir "icon.ico"
$arch = (go env GOARCH).Trim()
$syso = Join-Path $guiDir "rsrc_windows_$arch.syso"

Copy-Item -Force logo.png (Join-Path $guiDir "icon.png")

# The CLI also writes a Mac icon; redirect that unused file to TEMP on Windows.
$macTmp = Join-Path $env:TEMP "plc-simulater-icon.icns"
wails3 generate icons -input logo.png -windowsfilename $iconFile -macfilename $macTmp
if ($LASTEXITCODE -ne 0) { exit $LASTEXITCODE }

# generate syso is pure Go (winres): no windres / gcc needed for this step.
# It embeds the .ico, the app manifest and version info as PE resources.
wails3 generate syso -icon $iconFile `
    -manifest (Join-Path $iconDir "wails.exe.manifest") `
    -info (Join-Path $iconDir "info.json") `
    -arch $arch -out $syso
if ($LASTEXITCODE -ne 0) { exit $LASTEXITCODE }
Write-Host "==> Windows icon resource: $syso"

# ---- 5. Go build (pure Go) ----
$env:CGO_ENABLED = "0"
$env:GOOS = "windows"
New-Item -ItemType Directory -Force -Path dist | Out-Null
go build -trimpath `
  -ldflags "-s -w -H windowsgui -X plc-simulater/internal/bridge.Version=$version" `
  -o dist/plc-simulater-gui.exe ./cmd/plc-simulater-gui
if ($LASTEXITCODE -ne 0) { exit $LASTEXITCODE }

# ---- 6. Guard against producing a silent stub ----
$bin = (Resolve-Path .\dist\plc-simulater-gui.exe).Path
$buildInfo = (go version -m $bin) -join "`n"
if ($buildInfo -notmatch "github.com/wailsapp/wails/v3") {
    Remove-Item -Force $bin
    throw "The binary does not contain the wails3 GUI entry (stub built by mistake)."
}

Write-Host "Built dist/plc-simulater-gui.exe (version $version, icon from logo.png)"
