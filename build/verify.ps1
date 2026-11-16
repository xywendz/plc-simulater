# Full verification for both modes: Go build / vet / test,
# frontend type-check and BOTH frontend templates, plus a Linux cross build.
# NOTE: keep this script ASCII-only.
# NOTE: the repository uses CRLF line endings, so `gofmt -l` reports every file;
#       formatting is intentionally not used as a hard gate here.
$ErrorActionPreference = "Stop"
Set-Location (Join-Path $PSScriptRoot "..")
$env:CGO_ENABLED = "0"

Write-Host "[1/7] go build ./..."
go build ./...
if ($LASTEXITCODE -ne 0) { exit $LASTEXITCODE }

Write-Host "[2/7] go vet ./..."
go vet ./...
if ($LASTEXITCODE -ne 0) { exit $LASTEXITCODE }

Write-Host "[3/7] go test ./..."
go test ./...
if ($LASTEXITCODE -ne 0) { exit $LASTEXITCODE }

Write-Host "[4/7] frontend typecheck"
Push-Location frontend
if (-not (Test-Path node_modules)) { npm install }
npm run typecheck
if ($LASTEXITCODE -ne 0) { Pop-Location; exit $LASTEXITCODE }
Pop-Location

Write-Host "[5/7] frontend build (GUI template)"
Remove-Item -Recurse -Force frontend/dist -ErrorAction SilentlyContinue
Push-Location frontend
npm run build
if ($LASTEXITCODE -ne 0) { Pop-Location; exit $LASTEXITCODE }
Pop-Location

Write-Host "[6/7] frontend build (Web template)"
Remove-Item -Recurse -Force frontend/dist-web -ErrorAction SilentlyContinue
Push-Location frontend
npm run build:web
if ($LASTEXITCODE -ne 0) { Pop-Location; exit $LASTEXITCODE }
Pop-Location

Write-Host "[7/7] Linux cross build of the Web entry"
$env:GOOS = "linux"
$env:GOARCH = "amd64"
$probe = Join-Path $env:TEMP "plc-simulater-verify-server"
go build -trimpath -o $probe ./cmd/plc-simulater-server
if ($LASTEXITCODE -ne 0) { exit $LASTEXITCODE }
Remove-Item -Force $probe -ErrorAction SilentlyContinue

Write-Host "All checks passed (GUI local mode + Web mode)"
