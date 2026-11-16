# Build the Web/B-S server (net/http + embedded frontend).
# Frontend: npm run build:web (--mode web) -> frontend/dist-web
# Go entry:  ./cmd/plc-simulater-server, pure Go, CGO_ENABLED=0
# NOTE: keep this script ASCII-only (PowerShell 5.1 may decode UTF-8 without BOM as ANSI).
$ErrorActionPreference = "Stop"
Set-Location (Join-Path $PSScriptRoot "..")

# ---- 1. Version ----
$version = "dev"
try { $version = (git describe --tags --always 2>$null).Trim() } catch { }
if (-not $version) { $version = "dev" }

# ---- 2. Frontend (Web mode -> frontend/dist-web) ----
Write-Host "==> Building Web frontend"
Remove-Item -Recurse -Force frontend/dist-web -ErrorAction SilentlyContinue
Push-Location frontend
if (-not (Test-Path node_modules)) { npm install }
npm run build:web
if ($LASTEXITCODE -ne 0) { Pop-Location; exit $LASTEXITCODE }
Pop-Location

# ---- 3. Copy output into the Go embed directory ----
$embed = "internal/webui/dist"
Remove-Item -Recurse -Force $embed -ErrorAction SilentlyContinue
New-Item -ItemType Directory -Force -Path $embed | Out-Null
Copy-Item -Recurse -Force frontend/dist-web/* $embed/
Write-Host "==> Frontend assets copied to $embed"

# ---- 4. Go build (default target linux/amd64; override via GOOS_TARGET / GOARCH_TARGET) ----
$env:CGO_ENABLED = "0"
$targetOS = if ($env:GOOS_TARGET) { $env:GOOS_TARGET } else { "linux" }
$targetArch = if ($env:GOARCH_TARGET) { $env:GOARCH_TARGET } else { "amd64" }
$env:GOOS = $targetOS
$env:GOARCH = $targetArch

New-Item -ItemType Directory -Force -Path dist | Out-Null
$output = "dist/plc-simulater-server"
if ($targetOS -eq "windows") { $output += ".exe" }
go build -trimpath `
  -ldflags "-s -w -X plc-simulater/internal/bridge.Version=$version" `
  -o $output ./cmd/plc-simulater-server
if ($LASTEXITCODE -ne 0) { exit $LASTEXITCODE }

Write-Host "Built $output (GOOS=$targetOS GOARCH=$targetArch, version $version)"
Write-Host "Run: $output -addr :8070   (open http://localhost:8070)"
