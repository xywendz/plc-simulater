#!/usr/bin/env bash
# Build the Web server on Linux/macOS.
set -euo pipefail
cd "$(dirname "$0")/.."

VERSION="${VERSION:-dev}"
rm -rf frontend/dist-web
(cd frontend && { [ -d node_modules ] || npm install; } && npm run build:web)

rm -rf internal/webui/dist
mkdir -p internal/webui/dist
cp -R frontend/dist-web/* internal/webui/dist/

export CGO_ENABLED=0
export GOOS="${GOOS_TARGET:-linux}"
export GOARCH="${GOARCH_TARGET:-amd64}"
mkdir -p dist
out="dist/plc-simulater-server"
[ "$GOOS" = "windows" ] && out="${out}.exe"
go build -trimpath -ldflags "-s -w -X plc-simulater/internal/bridge.Version=$VERSION" -o "$out" ./cmd/plc-simulater-server
echo "Built $out (GOOS=$GOOS GOARCH=$GOARCH)"
